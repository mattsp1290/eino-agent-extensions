package delegatetask

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/tools"
)

func TestDefinitionContractSchemaPatternAndRetention(t *testing.T) {
	canonical := canonicalOptionsForTest(t)
	definition := definition(canonical, newCoordinator(canonical))
	if definition.Name != ToolName || definition.RetrySafe || len(definition.Permissions) != 1 || definition.Permissions[0] != PermissionDelegate {
		t.Fatalf("definition=%#v", definition)
	}
	if definition.Metadata["package"] != "delegatetask-v1" {
		t.Fatalf("metadata=%#v", definition.Metadata)
	}
	pattern, err := definition.Pattern(context.Background(), json.RawMessage(`{"task":"secret-like synthetic text","profile":"read-only"}`))
	if err != nil || pattern != "delegate-profile:read-only" || strings.Contains(pattern, "secret-like") {
		t.Fatalf("pattern=%q err=%v", pattern, err)
	}
	schema, err := definition.Parameters.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"required":["task","profile"]`, `"additionalProperties":false`, `"task":{"type":"string"}`, `"profile":{"type":"string"}`} {
		if !strings.Contains(string(raw), fragment) {
			t.Fatalf("schema missing %s: %s", fragment, raw)
		}
	}
	if definition.Retention.MaxInlineBytes <= 0 || definition.Retention.StoreExternal || definition.Retention.Redact {
		t.Fatalf("retention=%#v", definition.Retention)
	}
}

func TestDefinitionExecutorForwardsExecutionContext(t *testing.T) {
	var observed Request
	options := testOptions()
	options.Runner = RunnerFunc(func(_ context.Context, request Request) (Response, error) {
		observed = request
		return Response{Status: ResponseCompleted, Output: "complete"}, nil
	})
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	definition := definition(canonical, newCoordinator(canonical))
	raw, err := definition.Execute(context.Background(), tools.Execution{
		Input:   json.RawMessage(`{"task":"inspect","profile":"read-only"}`),
		Call:    runtime.ToolCall{ID: "call", SessionID: "session", RunID: "run", Name: ToolName},
		Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "session", RunID: "run"}, WorkspaceID: "workspace", WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"status":"completed","output":"complete"}` || observed.WorkspaceID != "workspace" || observed.WorkspaceRoot != "/workspace" {
		t.Fatalf("raw=%s request=%#v", raw, observed)
	}
}

func TestRetentionBoundsWorstCaseResult(t *testing.T) {
	canonical := canonicalOptionsForTest(t)
	definition := definition(canonical, newCoordinator(canonical))
	encoded, err := json.Marshal(Result{Status: StatusCompleted, Output: strings.Repeat("\x01", canonical.limits.MaxResultBytes)})
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(encoded))*2 > definition.Retention.MaxInlineBytes {
		t.Fatalf("copies=%d retention=%d", len(encoded)*2, definition.Retention.MaxInlineBytes)
	}
}

func TestMaterializedDecoderPinsUnicodeAndShapeBoundary(t *testing.T) {
	canonical := canonicalOptionsForTest(t)
	tool, err := tools.Materialize(context.Background(), definition(canonical, newCoordinator(canonical)), runtime.ToolScopeContext{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"invalid syntax": []byte(`{"task":`),
		"duplicate":      []byte(`{"task":"a","task":"b","profile":"read-only"}`),
		"unknown":        []byte(`{"task":"a","profile":"read-only","extra":true}`),
		"trailing":       []byte(`{"task":"a","profile":"read-only"} {}`),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := tool.InputDecoder.DecodeToolInput(context.Background(), raw); err == nil {
				t.Fatalf("accepted=%s", got)
			}
		})
	}
	invalidUTF8 := append([]byte(`{"task":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`","profile":"read-only"}`)...)
	if got, err := tool.InputDecoder.DecodeToolInput(context.Background(), invalidUTF8); err == nil {
		t.Fatalf("invalid UTF-8 accepted as %q", got)
	}
	got, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"task":"\ud800","profile":"read-only"}`))
	if err != nil || !strings.Contains(string(got), "�") {
		t.Fatalf("surrogate canonical=%q err=%v", got, err)
	}
}

func FuzzMaterializedDecoder(f *testing.F) {
	f.Add([]byte(`{"task":"inspect","profile":"read-only"}`))
	f.Add([]byte(`{"task":"\ud800","profile":"read-only"}`))
	canonical, err := canonicalize(testOptions())
	if err != nil {
		f.Fatal(err)
	}
	tool, err := tools.Materialize(context.Background(), definition(canonical, newCoordinator(canonical)), runtime.ToolScopeContext{SessionID: "session"})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		got, err := tool.InputDecoder.DecodeToolInput(context.Background(), raw)
		if err == nil && (!json.Valid(got) || !utf8.Valid(got)) {
			t.Fatalf("accepted invalid canonical JSON %q", got)
		}
	})
}
