package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/tools"
)

func TestDefinitionContractSchemaPermissionAndRetention(t *testing.T) {
	canonical := canonicalOptionsForTest(t)
	definition := definition(canonical, newCoordinator(canonical))
	if definition.Name != ToolName || definition.RetrySafe || len(definition.Permissions) != 1 || definition.Permissions[0] != PermissionSearch {
		t.Fatalf("definition=%#v", definition)
	}
	if definition.Metadata["package"] != "websearch-v1" || len(definition.Metadata) != 1 {
		t.Fatalf("metadata=%#v", definition.Metadata)
	}
	pattern, err := definition.Pattern(context.Background(), json.RawMessage(`{"query":"secret-like query"}`))
	if err != nil || pattern != permissionPattern || strings.Contains(pattern, "secret-like") {
		t.Fatalf("pattern=%q err=%v", pattern, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := definition.Pattern(ctx, json.RawMessage(`{"query":"q"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("pattern cancellation=%v", err)
	}
	schema, err := definition.Parameters.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"required":["query"]`, `"additionalProperties":false`, `"query":{"type":"string"}`} {
		if !strings.Contains(string(raw), fragment) {
			t.Fatalf("schema missing %s: %s", fragment, raw)
		}
	}
	if strings.Contains(definition.Description, "answer") || strings.Contains(definition.Description, "provider") || strings.Contains(definition.Description, "fetch") {
		t.Fatalf("description overclaims: %q", definition.Description)
	}
	if definition.Retention != canonical.retention {
		t.Fatalf("retention=%#v want=%#v", definition.Retention, canonical.retention)
	}
}

func TestDefinitionExecutorForwardsContextAndReturnsPreencodedResult(t *testing.T) {
	var observedQuery string
	options := testOptions()
	options.Searcher = SearcherFunc(func(_ context.Context, query string) ([]Source, error) {
		observedQuery = query
		return []Source{{Title: "t", URL: "https://example.test/", Snippet: "s"}}, nil
	})
	canonical, _ := canonicalize(options)
	raw, err := definition(canonical, newCoordinator(canonical)).Execute(context.Background(), tools.Execution{
		Input: json.RawMessage(`{"query":"canonical"}`), Call: testCall(), Context: testToolContext(),
	})
	if err != nil || observedQuery != "canonical" || string(raw) != `{"results":[{"title":"t","url":"https://example.test/","snippet":"s"}]}` {
		t.Fatalf("raw=%s query=%q err=%v", raw, observedQuery, err)
	}
}

func TestMaterializedDecoderPinsShapeAndUnicodeBoundary(t *testing.T) {
	canonical := canonicalOptionsForTest(t)
	tool, err := tools.Materialize(context.Background(), definition(canonical, newCoordinator(canonical)), runtime.ToolScopeContext{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"invalid syntax": []byte(`{"query":`),
		"null":           []byte(`null`),
		"array":          []byte(`[]`),
		"duplicate":      []byte(`{"query":"a","query":"b"}`),
		"unknown":        []byte(`{"query":"a","extra":true}`),
		"trailing":       []byte(`{"query":"a"} {}`),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := tool.InputDecoder.DecodeToolInput(context.Background(), raw); !errors.Is(err, tools.ErrMalformedInput) {
				t.Fatalf("got=%s err=%v", got, err)
			}
		})
	}
	invalidUTF8 := append([]byte(`{"query":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	got, err := tool.InputDecoder.DecodeToolInput(context.Background(), invalidUTF8)
	if !errors.Is(err, tools.ErrMalformedInput) || got != nil {
		t.Fatalf("invalid UTF-8 canonical=%q err=%v", got, err)
	}
	got, err = tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"query":"\ud800"}`))
	if err != nil || !strings.Contains(string(got), "�") {
		t.Fatalf("surrogate canonical=%q err=%v", got, err)
	}
	got, err = tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"query":"legitimate �"}`))
	if err != nil || !strings.Contains(string(got), "legitimate �") {
		t.Fatalf("replacement character canonical=%q err=%v", got, err)
	}
}

func FuzzMaterializedDecoder(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"query":"search"}`), []byte(`{"query":"  trim  "}`),
		[]byte(`{"query":"\ud800"}`), []byte(`{"query":"a","query":"b"}`),
		[]byte(`{"query":"a","extra":1}`), []byte(`null`), []byte{0xff},
	} {
		f.Add(seed)
	}
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
		if err != nil {
			return
		}
		if !json.Valid(got) || !utf8.Valid(got) {
			t.Fatalf("accepted invalid canonical JSON %q", got)
		}
		var input toolInput
		if err := json.Unmarshal(got, &input); err != nil || strings.TrimSpace(input.Query) != input.Query || input.Query == "" || strings.IndexByte(input.Query, 0) >= 0 || len(input.Query) > canonical.limits.MaxQueryBytes {
			t.Fatalf("accepted invalid semantic input %q: %#v err=%v", got, input, err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(got, &keys); err != nil || len(keys) != 1 || keys["query"] == nil {
			t.Fatalf("accepted wrong shape %q", got)
		}
	})
}
