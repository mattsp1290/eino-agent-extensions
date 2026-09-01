package toolresultredactor

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mattsp1290/eino-agent/runtime"
)

func TestRedactCoversEveryResultFieldAndDoesNotMutateInput(t *testing.T) {
	policy := policyForTest(t, testOptions())
	marker := "SYN_ABCD"
	input := runtime.ToolResult{
		Output:     "safe " + marker,
		Structured: json.RawMessage(`{"safe":"SYN_ABCD","number":1}`),
		Metadata:   map[string]string{"safe": "value " + marker},
		Attachments: []runtime.Attachment{{
			ID: marker, MIMEType: "text/" + marker, Name: "name-" + marker, URL: "https://invalid/" + marker,
			Metadata: map[string]string{"safe": marker},
		}},
	}
	originalStructured := append([]byte(nil), input.Structured...)
	output := policy.redact(context.Background(), input)
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), marker) {
		t.Fatalf("transformed result still contains fixture marker")
	}
	if !strings.Contains(string(raw), Placeholder) {
		t.Fatalf("transformed result lacks placeholder")
	}
	if !bytes.Equal(input.Structured, originalStructured) || input.Metadata["safe"] != "value "+marker || input.Attachments[0].ID != marker || input.Attachments[0].Metadata["safe"] != marker {
		t.Fatalf("input was mutated")
	}
	output.Metadata["safe"] = "changed"
	output.Attachments[0].Metadata["safe"] = "changed"
	if input.Metadata["safe"] == "changed" || input.Attachments[0].Metadata["safe"] == "changed" {
		t.Fatalf("output aliases input maps")
	}
}

func TestKeyAndCollectionFallbacksAreFieldLocal(t *testing.T) {
	options := testOptions()
	options.Limits.MaxMetadataEntries = 1
	options.Limits.MaxAttachments = 1
	policy := policyForTest(t, options)
	marker := "SYN_ABCD"
	cases := map[string]runtime.ToolResult{
		"metadata key":            {Output: "safe", Structured: json.RawMessage(`{"safe":"value"}`), Metadata: map[string]string{marker: "value"}},
		"attachment metadata key": {Output: "safe", Structured: json.RawMessage(`{"safe":"value"}`), Attachments: []runtime.Attachment{{Name: "safe", Metadata: map[string]string{marker: "value"}}}},
		"metadata count":          {Output: "safe", Structured: json.RawMessage(`{"safe":"value"}`), Metadata: map[string]string{"a": "1", "b": "2"}},
		"attachment count":        {Output: "safe", Structured: json.RawMessage(`{"safe":"value"}`), Attachments: []runtime.Attachment{{Name: "a"}, {Name: "b"}}},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			output := policy.redact(context.Background(), input)
			if output.Output != "safe" || string(output.Structured) != `{"safe":"value"}` {
				t.Fatalf("safe sibling changed")
			}
			if strings.Contains(string(mustJSON(t, output)), marker) {
				t.Fatalf("fallback retained fixture marker")
			}
		})
	}
}

func TestScalarUnsafeAndCancellationFallback(t *testing.T) {
	options := testOptions()
	options.Limits.MaxFieldBytes = 4
	policy := policyForTest(t, options)
	input := runtime.ToolResult{Output: "12345", Structured: json.RawMessage(`{"x":"ok"}`), Metadata: map[string]string{"x": string([]byte{0xff})}}
	output := policy.redact(context.Background(), input)
	if output.Output != Placeholder || output.Metadata["x"] != Placeholder || string(output.Structured) != string(input.Structured) {
		t.Fatalf("unsafe scalar fallback was not field local")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output = policy.redact(ctx, input)
	if output.Output != Placeholder || string(output.Structured) != string(structuredPlaceholder) || !reflect.DeepEqual(output.Metadata, metadataPlaceholder()) {
		t.Fatalf("cancellation did not sanitize unproven fields")
	}
}

func TestNilAndEmptyContainersArePreserved(t *testing.T) {
	policy := policyForTest(t, testOptions())
	input := runtime.ToolResult{Metadata: map[string]string{}, Attachments: []runtime.Attachment{}, Structured: json.RawMessage{}}
	output := policy.redact(context.Background(), input)
	if output.Metadata == nil || output.Attachments == nil || output.Structured == nil {
		t.Fatalf("non-nil empty container became nil")
	}
	output = policy.redact(context.Background(), runtime.ToolResult{})
	if output.Metadata != nil || output.Attachments != nil || output.Structured != nil {
		t.Fatalf("nil container became non-nil")
	}
}

func TestFixedPlaceholderIsStable(t *testing.T) {
	options := testOptions()
	options.AdditionalPatterns = []Pattern{{ID: "placeholder-word", Expression: `REDACTED`}}
	policy := policyForTest(t, options)
	output, state := policy.scanScalar(context.Background(), Placeholder)
	if state != scanUnchanged || output != Placeholder {
		t.Fatalf("fixed placeholder was rewritten: state=%d", state)
	}
}

func TestCompiledPolicySupportsConcurrentUse(t *testing.T) {
	policy := policyForTest(t, testOptions())
	input := runtime.ToolResult{Output: "SYN_ABCD", Structured: json.RawMessage(`{"x":"SYN_ABCD"}`)}
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				output := policy.redact(context.Background(), input)
				if strings.Contains(output.Output, "SYN_") {
					t.Errorf("concurrent result retained marker")
					return
				}
			}
		}()
	}
	wait.Wait()
}

func TestCancellationAndPanicFallbackNeverReturnUnprovenContent(t *testing.T) {
	input := runtime.ToolResult{
		Output: "unsafe-output", Structured: json.RawMessage(`{"unsafe":"value"}`),
		Metadata: map[string]string{"unsafe": "value"}, Attachments: []runtime.Attachment{{Name: "unsafe"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	canceling := &compiledPolicy{limits: testLimits(), rules: []matcher{matcherFunc(func(string, int) []byteRange {
		cancel()
		return nil
	})}}
	output := canceling.redact(ctx, input)
	assertFullyPlaceholderized(t, output)

	structuredInput := runtime.ToolResult{
		Output:     "safe",
		Structured: json.RawMessage(`{"first":"one","second":"two"}`),
		Metadata:   map[string]string{"later": "unproven"},
		Attachments: []runtime.Attachment{{
			Name: "unproven",
		}},
	}
	before := append([]byte(nil), mustJSON(t, structuredInput)...)
	structuredCtx, structuredCancel := context.WithCancel(context.Background())
	structuredPolicy := &compiledPolicy{limits: testLimits(), rules: []matcher{matcherFunc(func(value string, _ int) []byteRange {
		if value == "two" {
			structuredCancel()
		}
		return nil
	})}}
	output = structuredPolicy.redact(structuredCtx, structuredInput)
	assertFullyPlaceholderized(t, output)
	if !bytes.Equal(before, mustJSON(t, structuredInput)) {
		t.Fatalf("structured cancellation mutated input")
	}

	panicking := &compiledPolicy{limits: testLimits(), rules: []matcher{matcherFunc(func(string, int) []byteRange {
		panic("fixture matcher panic")
	})}}
	output = panicking.redact(context.Background(), input)
	assertFullyPlaceholderized(t, output)
}

type matcherFunc func(string, int) []byteRange

func (f matcherFunc) find(value string, limit int) []byteRange { return f(value, limit) }

func assertFullyPlaceholderized(t *testing.T, output runtime.ToolResult) {
	t.Helper()
	if output.Output != Placeholder || string(output.Structured) != string(structuredPlaceholder) ||
		!reflect.DeepEqual(output.Metadata, metadataPlaceholder()) || !reflect.DeepEqual(output.Attachments, attachmentPlaceholder()) {
		t.Fatalf("fallback returned unproven content")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
