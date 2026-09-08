package rtkreducer

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mattsp1290/eino-agent/runtime"
)

func TestTransformerPatchesMirrorsAndPreservesUnselectedBytes(t *testing.T) {
	options := testOptions()
	options.Bindings = []Binding{{ToolName: "fixture", Mode: JSONMirror, Fields: []Field{{Name: "stdout", Filter: Log}}}}
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	transformer := newTransformer(canonical, fakeAcquire(func(_ context.Context, filter Filter, input string) (string, bool) {
		calls++
		if filter != Log || !strings.Contains(input, "verbose") {
			t.Fatalf("reduce input = %q filter=%q", input, filter)
		}
		return "summary\n", true
	}))
	raw := ` {"huge":1.2300e+4000,"stdout":"` + strings.Repeat("verbose ", 40) + `","status":"ok"} `
	input := runtime.ToolResultTransform{ToolName: "fixture", Result: runtime.ToolResult{Output: raw, Structured: json.RawMessage(raw), Metadata: map[string]string{"status": "owned"}}}
	output, err := transformer.transform(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || output.Result.Output != string(output.Result.Structured) || len(output.Result.Output) >= len(raw) {
		t.Fatalf("unexpected reduction calls=%d bytes=%d", calls, len(output.Result.Output))
	}
	if !strings.Contains(output.Result.Output, `[Reduced by RTK log; original_bytes=320]\nsummary\n`) || !strings.Contains(output.Result.Output, `"huge":1.2300e+4000`) || !strings.Contains(output.Result.Output, `"status":"ok"`) {
		t.Fatalf("patched JSON = %s", output.Result.Output)
	}
	if !reflect.DeepEqual(output.Result.Metadata, input.Result.Metadata) {
		t.Fatal("metadata changed")
	}
	output.Result.Structured[0] = '['
	if input.Result.Structured[0] != ' ' {
		t.Fatal("output aliases original structured bytes")
	}
}

func TestTransformerAllOrNothingAndAbstentions(t *testing.T) {
	options := testOptions()
	options.Bindings = []Binding{{ToolName: "fixture", Mode: JSONMirror, MatchInput: &InputMatch{Field: "cmd", Equals: []string{"exact"}}, Fields: []Field{{Name: "a", Filter: Log}, {Name: "b", Filter: GitDiff}}}}
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("repeated line\n", 40)
	rawBytes, _ := json.Marshal(map[string]string{"a": long, "b": long})
	raw := string(rawBytes)
	base := runtime.ToolResultTransform{ToolName: "fixture", Call: runtime.ToolCall{Input: json.RawMessage(`{"cmd":"exact"}`)}, Result: runtime.ToolResult{Output: raw, Structured: append(json.RawMessage(nil), rawBytes...)}}

	t.Run("later failure rolls back", func(t *testing.T) {
		calls := 0
		transformer := newTransformer(canonical, fakeAcquire(func(_ context.Context, _ Filter, _ string) (string, bool) {
			calls++
			return "tiny", calls == 1
		}))
		got, _ := transformer.transform(context.Background(), base)
		if calls != 2 || !reflect.DeepEqual(got, base) {
			t.Fatalf("rollback failed calls=%d", calls)
		}
	})

	for name, edit := range map[string]func(*runtime.ToolResultTransform){
		"input mismatch": func(value *runtime.ToolResultTransform) { value.Call.Input = json.RawMessage(`{"cmd":"other"}`) },
		"permission": func(value *runtime.ToolResultTransform) {
			value.Result.Metadata = map[string]string{"permission_status": "denied"}
		},
		"mirror mismatch": func(value *runtime.ToolResultTransform) { value.Result.Output += " " },
		"missing field": func(value *runtime.ToolResultTransform) {
			value.Result.Output, value.Result.Structured = `{"a":"x"}`, json.RawMessage(`{"a":"x"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.Result.Structured = append(json.RawMessage(nil), base.Result.Structured...)
			edit(&candidate)
			acquired := false
			transformer := newTransformer(canonical, func(context.Context) (reduceFieldFunc, func(), bool) {
				acquired = true
				return func(context.Context, Filter, string) (string, bool) { return "tiny", true }, func() {}, true
			})
			got, _ := transformer.transform(context.Background(), candidate)
			if !reflect.DeepEqual(got, candidate) {
				t.Fatal("abstention changed result")
			}
			if (name == "input mismatch" || name == "permission") && acquired {
				t.Fatal("early abstention acquired process capacity")
			}
		})
	}
}

func TestTransformerNoGainTextAndPanicFallback(t *testing.T) {
	options := testOptions()
	options.Bindings = []Binding{{ToolName: "text", Mode: TextOnly, Fields: []Field{{Filter: GoTest}}}}
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	original := runtime.ToolResultTransform{ToolName: "text", Result: runtime.ToolResult{Output: strings.Repeat("x", 100)}}
	for name, reduce := range map[string]reduceFieldFunc{
		"larger":  func(context.Context, Filter, string) (string, bool) { return strings.Repeat("y", 200), true },
		"failure": func(context.Context, Filter, string) (string, bool) { return "", false },
		"panic":   func(context.Context, Filter, string) (string, bool) { panic("secret") },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := newTransformer(canonical, fakeAcquire(reduce)).transform(context.Background(), original)
			if err != nil || !reflect.DeepEqual(got, original) {
				t.Fatalf("fallback = %#v, %v", got, err)
			}
		})
	}
}

func TestTransformerAcceptsPartialGainAndSkipsEmptyField(t *testing.T) {
	options := testOptions()
	options.Bindings = []Binding{{ToolName: "fixture", Mode: JSONMirror, Fields: []Field{{Name: "empty", Filter: Log}, {Name: "gain", Filter: GitDiff}, {Name: "same", Filter: GoTest}}}}
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("verbose ", 80)
	raw := ` { "empty" : "", "gain" : ` + testJSONQuote(long) + `, "same" : ` + testJSONQuote(long) + `, "n" : 1.2300e+4000 } `
	calls := 0
	transformer := newTransformer(canonical, fakeAcquire(func(_ context.Context, filter Filter, input string) (string, bool) {
		calls++
		if filter == GitDiff {
			return "summary", true
		}
		return input, true
	}))
	input := runtime.ToolResultTransform{ToolName: "fixture", Result: runtime.ToolResult{Output: raw, Structured: json.RawMessage(raw)}}
	output, err := transformer.transform(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("process calls=%d, want two nonempty fields", calls)
	}
	if !strings.Contains(output.Result.Output, `"empty" : ""`) || !strings.Contains(output.Result.Output, `"same" : `+testJSONQuote(long)) || !strings.Contains(output.Result.Output, `"n" : 1.2300e+4000`) {
		t.Fatalf("unchanged spans were rewritten: %s", output.Result.Output)
	}
	if !strings.Contains(output.Result.Output, `[Reduced by RTK git-diff;`) || output.Result.Output != string(output.Result.Structured) {
		t.Fatalf("gaining field was not mirrored: %s", output.Result.Output)
	}
}

func TestTransformerCapacityAndNULFallback(t *testing.T) {
	options := testOptions()
	options.Bindings = []Binding{{ToolName: "fixture", Mode: JSONMirror, Fields: []Field{{Name: "stdout", Filter: Log}}}}
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"stdout":"unsafe\u0000value"}`
	input := runtime.ToolResultTransform{ToolName: "fixture", Result: runtime.ToolResult{Output: raw, Structured: json.RawMessage(raw)}}
	called := false
	transformer := newTransformer(canonical, func(context.Context) (reduceFieldFunc, func(), bool) {
		called = true
		return nil, nil, false
	})
	if output, _ := transformer.transform(context.Background(), input); !reflect.DeepEqual(output, input) || !called {
		t.Fatal("capacity exhaustion did not preserve original")
	}
	transformer = newTransformer(canonical, fakeAcquire(func(context.Context, Filter, string) (string, bool) {
		t.Fatal("NUL-bearing field reached process")
		return "", false
	}))
	if output, _ := transformer.transform(context.Background(), input); !reflect.DeepEqual(output, input) {
		t.Fatal("NUL-bearing field changed")
	}
}

func TestTransformerAllocationMeasurementIsBoundedPerCallback(t *testing.T) {
	options := testOptions()
	options.Bindings = []Binding{{ToolName: "text", Mode: TextOnly, Fields: []Field{{Filter: Log}}}}
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	transformer := newTransformer(canonical, fakeAcquire(func(context.Context, Filter, string) (string, bool) {
		return "tiny", true
	}))
	input := runtime.ToolResultTransform{ToolName: "text", Result: runtime.ToolResult{Output: strings.Repeat("bounded input ", 1024)}}
	allocations := testing.AllocsPerRun(25, func() {
		output, transformErr := transformer.transform(context.Background(), input)
		if transformErr != nil || len(output.Result.Output) >= len(input.Result.Output) {
			panic("unexpected measurement result")
		}
	})
	if allocations <= 0 || allocations > 256 {
		t.Fatalf("unexpected allocations per bounded callback: %.1f", allocations)
	}
	t.Logf("bounded callback allocation measurement: input_bytes=%d allocations_per_run=%.1f", len(input.Result.Output), allocations)
}

func fakeAcquire(reduce reduceFieldFunc) acquireReductionFunc {
	return func(context.Context) (reduceFieldFunc, func(), bool) { return reduce, func() {}, true }
}

func testJSONQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
