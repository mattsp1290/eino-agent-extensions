//go:build linux || darwin

package backgroundjobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	"github.com/mattsp1290/eino-agent/tools"
)

func TestExecutorOwnerPrivacyAndConstantPermissions(t *testing.T) {
	canonical, err := canonicalize(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newManager(canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	workspace := t.TempDir()
	materialized := make(map[string]runtime.Tool)
	for _, definition := range definitions(canonical, manager) {
		tool, err := tools.Materialize(context.Background(), definition, runtime.ToolScopeContext{SessionID: "session", WorkspaceID: "workspace", WorkspaceRoot: workspace})
		if err != nil {
			t.Fatal(err)
		}
		materialized[tool.Name] = tool
	}
	start := materialized[StartToolName]
	first, _ := start.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"command":"sleep 30","timeout_seconds":1}`))
	second, _ := start.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"command":"printf private-marker"}`))
	for _, input := range [][]byte{first, second} {
		pattern, err := start.Pattern.ResolvePermissionPattern(context.Background(), input)
		if err != nil || pattern != permissionStart || strings.Contains(pattern, "private-marker") {
			t.Fatalf("start pattern = %q, %v", pattern, err)
		}
	}
	badCall := runtime.ToolCall{
		ID: "bad-owner", SessionID: "session", Name: StartToolName, Input: second,
		Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "different"}, WorkspaceID: "workspace", WorkspaceRoot: workspace},
	}
	if _, err := start.Executor.Execute(context.Background(), badCall); err == nil || len(manager.list(ownerKey{sessionID: "session", workspaceID: "workspace"}).Jobs) != 0 {
		t.Fatalf("mismatched owner mutated manager: %v", err)
	}

	call := runtime.ToolCall{
		ID: "start", SessionID: "session", Name: StartToolName, Input: second,
		Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "session"}, WorkspaceID: "workspace", WorkspaceRoot: workspace},
	}
	output, err := start.Executor.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var receipt StartResult
	if err := json.Unmarshal(output.Structured, &receipt); err != nil || receipt.ID == "" || strings.Contains(string(output.Structured), "private-marker") || strings.Contains(string(output.Structured), workspace) {
		t.Fatalf("start output = %s, %v", output.Structured, err)
	}
	listTool := materialized[ListToolName]
	listInput, _ := listTool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{}`))
	listOutput, err := listTool.Executor.Execute(context.Background(), runtime.ToolCall{
		ID: "list", SessionID: "session", Name: ListToolName, Input: listInput,
		Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "session"}, WorkspaceID: "workspace"},
	})
	if err != nil || strings.Contains(string(listOutput.Structured), "private-marker") || strings.Contains(string(listOutput.Structured), workspace) {
		t.Fatalf("list output = %s, %v", listOutput.Structured, err)
	}

	statusTool := materialized[StatusToolName]
	idRaw, _ := json.Marshal(map[string]string{"id": receipt.ID})
	idInput, _ := statusTool.InputDecoder.DecodeToolInput(context.Background(), idRaw)
	foreignCall := runtime.ToolCall{
		ID: session.ToolCallID("foreign"), SessionID: "session", Name: StatusToolName, Input: idInput,
		Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "session"}, WorkspaceID: "foreign"},
	}
	_, foreignErr := statusTool.Executor.Execute(context.Background(), foreignCall)
	unknownRaw, _ := json.Marshal(map[string]string{"id": "job_00000000000000000000000000000000_0000000000000001"})
	unknownInput, _ := statusTool.InputDecoder.DecodeToolInput(context.Background(), unknownRaw)
	foreignCall.Input = unknownInput
	foreignCall.Context.WorkspaceID = "workspace"
	_, unknownErr := statusTool.Executor.Execute(context.Background(), foreignCall)
	if !errors.Is(foreignErr, errJobNotFound) || foreignErr.Error() != unknownErr.Error() {
		t.Fatalf("foreign=%v unknown=%v", foreignErr, unknownErr)
	}

	killTool := materialized[KillToolName]
	killInput, _ := killTool.InputDecoder.DecodeToolInput(context.Background(), idRaw)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := killTool.Executor.Execute(ctx, runtime.ToolCall{
		ID: "kill", SessionID: "session", Name: KillToolName, Input: killInput,
		Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "session"}, WorkspaceID: "workspace"},
	}); err != nil {
		t.Fatal(err)
	}
}
