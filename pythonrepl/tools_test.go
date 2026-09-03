//go:build linux || darwin

package pythonrepl

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/tools"
)

func TestToolSchemasNormalizationAndPermissions(t *testing.T) {
	canonical, err := canonicalize(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	manager := newManager(canonical)
	deferManagerClose(t, manager)
	workspace := t.TempDir()
	for _, definition := range definitions(canonical, manager) {
		tool, err := tools.Materialize(context.Background(), definition, runtime.ToolScopeContext{SessionID: "s", WorkspaceID: "w", WorkspaceRoot: workspace})
		if err != nil {
			t.Fatal(err)
		}
		if definition.RetrySafe || tool.Retention.StoreExternal || tool.Retention.MaxInlineBytes <= 0 {
			t.Fatalf("unbounded/retry-safe tool: %#v", tool)
		}
		var valid, invalid []byte
		wantPattern := permissionManage
		if definition.Name == ExecuteToolName {
			valid = []byte(`{"code":" x = 1\\n","timeout_seconds":0}`)
			invalid = []byte(`{"code":"x","unknown":true}`)
			wantPattern = permissionExecute
		} else {
			valid = []byte(`{}`)
			invalid = []byte(`{"unknown":true}`)
		}
		normalized, err := tool.InputDecoder.DecodeToolInput(context.Background(), valid)
		if err != nil {
			t.Fatal(err)
		}
		pattern, err := tool.Pattern.ResolvePermissionPattern(context.Background(), normalized)
		if err != nil || pattern != wantPattern {
			t.Fatalf("pattern=%q err=%v", pattern, err)
		}
		if _, err := tool.InputDecoder.DecodeToolInput(context.Background(), invalid); !errors.Is(err, tools.ErrMalformedInput) {
			t.Fatalf("unknown property error = %v", err)
		}
	}
	execute := definitions(canonical, manager)[0]
	tool, _ := tools.Materialize(context.Background(), execute, runtime.ToolScopeContext{})
	for _, raw := range [][]byte{[]byte(`{"code":""}`), []byte(`{"code":"x","code":"y"}`), []byte(`{"code":"x","timeout_seconds":-1}`), []byte(`{"code":"x","timeout_seconds":1.5}`), []byte(`{"code":"x","timeout_seconds":6}`)} {
		if _, err := tool.InputDecoder.DecodeToolInput(context.Background(), raw); !errors.Is(err, tools.ErrMalformedInput) {
			t.Fatalf("input %s error = %v", raw, err)
		}
	}
	clear := definitions(canonical, manager)[1]
	clearTool, _ := tools.Materialize(context.Background(), clear, runtime.ToolScopeContext{})
	if _, err := clearTool.InputDecoder.DecodeToolInput(context.Background(), []byte(`null`)); !errors.Is(err, tools.ErrMalformedInput) {
		t.Fatalf("clear accepted null: %v", err)
	}
}

func TestTypedExecuteRetainsIdenticalBoundedCopies(t *testing.T) {
	canonical, err := canonicalize(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	manager := newManager(canonical)
	deferManagerClose(t, manager)
	workspace := t.TempDir()
	definition := definitions(canonical, manager)[0]
	tool, err := tools.Materialize(context.Background(), definition, runtime.ToolScopeContext{SessionID: "copies", WorkspaceID: "workspace", WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	input, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"code":"print('x' * 4096); 'y' * 4096"}`))
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.Executor.Execute(context.Background(), runtime.ToolCall{
		ID: "copy-call", SessionID: "copies", Name: ExecuteToolName, Input: input,
		Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "copies"}, WorkspaceID: "workspace", WorkspaceRoot: workspace},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Output != string(output.Structured) || len(output.Output) == 0 {
		t.Fatalf("typed copies differ output=%d structured=%d", len(output.Output), len(output.Structured))
	}
	if int64(len(output.Output)+len(output.Structured)) > tool.Retention.MaxInlineBytes {
		t.Fatalf("copies=%d retention=%d", len(output.Output)+len(output.Structured), tool.Retention.MaxInlineBytes)
	}
}

func TestToolNormalizersRejectOversizedRawJSONBeforeStructuralValidation(t *testing.T) {
	canonical, err := canonicalize(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	execute := normalizeExecute(canonical)
	maximum := int(canonical.requestMax) + 1
	inputs := map[string][]byte{
		"whitespace": bytes.Repeat([]byte(" "), maximum),
		"wide":       append(append([]byte(`{"code":"x","unknown":{`), bytes.Repeat([]byte(`"a":0,`), maximum/6)...), []byte(`"z":0}}`)...),
		"deep":       append(bytes.Repeat([]byte("["), maximum), bytes.Repeat([]byte("]"), maximum)...),
	}
	for name, raw := range inputs {
		t.Run(name, func(t *testing.T) {
			if _, err := execute(context.Background(), raw); !errors.Is(err, tools.ErrMalformedInput) {
				t.Fatalf("oversized input error=%v", err)
			}
		})
	}
	if _, err := normalizeClear(context.Background(), bytes.Repeat([]byte(" "), maxClearInputBytes+1)); !errors.Is(err, tools.ErrMalformedInput) {
		t.Fatalf("oversized clear error=%v", err)
	}
}
