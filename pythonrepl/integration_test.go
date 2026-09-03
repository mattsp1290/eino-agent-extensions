//go:build linux || darwin

package pythonrepl

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/session"
)

func TestIntegrationStatefulDurableJourney(t *testing.T) {
	options := testOptions(t)
	registry, mount := mountIntegrationRegistry(t, options)
	defer closeIntegrationMount(t, mount)
	database := openIntegrationStore(t, "python-repl.db")
	deferDatabaseClose(t, database)
	var mu sync.Mutex
	var observedPermissions []permissions.Request
	policy := permissions.PolicyFunc(func(_ context.Context, request permissions.Request) (permissions.Decision, error) {
		mu.Lock()
		observedPermissions = append(observedPermissions, request)
		mu.Unlock()
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	})
	phase := 0
	var first, second ExecuteResult
	var secondEnvelope string
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		latest := latestToolMessage(request.Messages)
		switch phase {
		case 0:
			if latest == nil {
				return toolCallMessage("python-assign", ExecuteToolName, `{"code":"x = 40"}`), nil
			}
			if err := decodeIntegrationResult(latest.Content, &first); err != nil {
				return nil, err
			}
			phase = 1
			return []*einoschema.Message{einoschema.AssistantMessage("assigned", nil)}, nil
		case 2:
			if latest == nil || latest.ToolCallID != "python-read" {
				return toolCallMessage("python-read", ExecuteToolName, `{"code":"x + 2"}`), nil
			}
			secondEnvelope = latest.Content
			if err := decodeIntegrationResult(latest.Content, &second); err != nil {
				return nil, err
			}
			phase = 3
			return []*einoschema.Message{einoschema.AssistantMessage("read", nil)}, nil
		default:
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
	})
	orchestrator := integrationOrchestrator(t, database, registry, streamer, policy, "python-owner")
	workspace := t.TempDir()
	snapshot := integrationSnapshot(workspace, "python-workspace")
	runIntegrationRequest(t, orchestrator, "python-session", "assign", snapshot)
	phase = 2
	runIntegrationRequest(t, orchestrator, "python-session", "read", snapshot)
	if first.Status != "completed" || second.Status != "completed" || second.Result.Text != "42" || second.Generation != 0 {
		t.Fatalf("stateful results first=%#v second=%#v", first, second)
	}
	call, err := database.GetToolCall(context.Background(), "python-read")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != session.ToolCallCompleted || string(call.Input) != `{"code":"x + 2"}` || len(call.Output) == 0 || !strings.Contains(secondEnvelope, `"42"`) {
		t.Fatalf("durable call=%#v envelope=%s", call, secondEnvelope)
	}
	mu.Lock()
	requests := append([]permissions.Request(nil), observedPermissions...)
	mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("permission requests=%#v", requests)
	}
	for _, request := range requests {
		encoded, _ := json.Marshal(request)
		if request.Permission != permissionExecute || request.Pattern != permissionExecute || strings.Contains(string(encoded), "x + 2") || strings.Contains(string(encoded), workspace) {
			t.Fatalf("permission leaked behavior: %s", encoded)
		}
	}
	mount.Deactivate()
	closeIntegrationMount(t, mount)
	entries, err := os.ReadDir(options.TempRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temp root after close entries=%d err=%v", len(entries), err)
	}
}

func TestIntegrationGlobalMountIsolatesDurableOwners(t *testing.T) {
	options := testOptions(t)
	registry, mount := mountIntegrationRegistry(t, options)
	defer closeIntegrationMount(t, mount)
	database := openIntegrationStore(t, "owner-isolation.db")
	deferDatabaseClose(t, database)
	ids := &integrationIDs{}

	type ownerScript struct {
		name   string
		value  string
		phase  int
		result ExecuteResult
	}
	newStreamer := func(script *ownerScript) model.Streamer {
		return integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
			latest := latestToolMessage(request.Messages)
			assignID, readID := script.name+"-assign", script.name+"-read"
			switch {
			case script.phase == 0 && latest != nil && latest.ToolCallID == assignID:
				script.phase = 1
				return []*einoschema.Message{einoschema.AssistantMessage("assigned", nil)}, nil
			case script.phase == 0:
				arguments, _ := json.Marshal(map[string]string{"code": "shared = " + script.value})
				return toolCallMessage(assignID, ExecuteToolName, string(arguments)), nil
			case latest != nil && latest.ToolCallID == readID:
				if err := decodeIntegrationResult(latest.Content, &script.result); err != nil {
					return nil, err
				}
				return []*einoschema.Message{einoschema.AssistantMessage("read", nil)}, nil
			default:
				return toolCallMessage(readID, ExecuteToolName, `{"code":"shared"}`), nil
			}
		})
	}
	a := &ownerScript{name: "owner-a", value: "'a'"}
	b := &ownerScript{name: "owner-b", value: "'b'"}
	orchestratorA := integrationOrchestratorWithIDs(t, database, registry, newStreamer(a), allowIntegrationPolicy(), "isolation-host-a", ids)
	orchestratorB := integrationOrchestratorWithIDs(t, database, registry, newStreamer(b), allowIntegrationPolicy(), "isolation-host-b", ids)
	snapshotA := integrationSnapshot(t.TempDir(), "workspace-a")
	snapshotB := integrationSnapshot(t.TempDir(), "workspace-b")

	runIntegrationRequest(t, orchestratorA, "session-a", "assign", snapshotA)
	runIntegrationRequest(t, orchestratorB, "session-b", "assign", snapshotB)
	runIntegrationRequest(t, orchestratorA, "session-a", "read", snapshotA)
	runIntegrationRequest(t, orchestratorB, "session-b", "read", snapshotB)
	if a.result.Result.Text != "'a'" || b.result.Result.Text != "'b'" || a.result.Generation != 0 || b.result.Generation != 0 {
		t.Fatalf("isolated results: a=%s/%d b=%s/%d", a.result.Result.Text, a.result.Generation, b.result.Result.Text, b.result.Generation)
	}
}

func TestIntegrationPermissionDenialAndApprovalHaveNoPythonSideEffect(t *testing.T) {
	for _, action := range []permissions.Action{permissions.ActionDeny, permissions.ActionAsk} {
		t.Run(string(action), func(t *testing.T) {
			options := testOptions(t)
			registry, mount := mountIntegrationRegistry(t, options)
			defer closeIntegrationMount(t, mount)
			database := openIntegrationStore(t, "permission.db")
			deferDatabaseClose(t, database)
			seenResult := false
			streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
				if latestToolMessage(request.Messages) != nil {
					seenResult = true
					return []*einoschema.Message{einoschema.AssistantMessage("permission handled", nil)}, nil
				}
				return toolCallMessage("permission-call", ExecuteToolName, `{"code":"x = 1"}`), nil
			})
			policy := permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
				return permissions.Decision{Action: action, Message: "synthetic"}, nil
			})
			orchestrator := integrationOrchestrator(t, database, registry, streamer, policy, "permission-owner")
			runIntegrationRequest(t, orchestrator, "permission-session", "execute", integrationSnapshot(t.TempDir(), "permission-workspace"))
			if !seenResult {
				t.Fatal("model did not observe permission result")
			}
			entries, err := os.ReadDir(options.TempRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("permission path created venv entries=%d err=%v", len(entries), err)
			}
		})
	}
}

func TestIntegrationDeniedClearPreservesState(t *testing.T) {
	options := testOptions(t)
	registry, mount := mountIntegrationRegistry(t, options)
	defer closeIntegrationMount(t, mount)
	database := openIntegrationStore(t, "clear-denial.db")
	deferDatabaseClose(t, database)
	phase := 0
	var assigned, preserved ExecuteResult
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		latest := latestToolMessage(request.Messages)
		switch phase {
		case 0:
			if latest == nil {
				return toolCallMessage("clear-denial-assign", ExecuteToolName, `{"code":"kept = 42"}`), nil
			}
			if err := decodeIntegrationResult(latest.Content, &assigned); err != nil {
				return nil, err
			}
			phase = 1
			return []*einoschema.Message{einoschema.AssistantMessage("assigned", nil)}, nil
		case 2:
			if latest == nil || latest.ToolCallID != "clear-denial-clear" {
				return toolCallMessage("clear-denial-clear", ClearToolName, `{}`), nil
			}
			phase = 3
			return []*einoschema.Message{einoschema.AssistantMessage("clear denied", nil)}, nil
		case 4:
			if latest == nil || latest.ToolCallID != "clear-denial-read" {
				return toolCallMessage("clear-denial-read", ExecuteToolName, `{"code":"kept"}`), nil
			}
			if err := decodeIntegrationResult(latest.Content, &preserved); err != nil {
				return nil, err
			}
			phase = 5
			return []*einoschema.Message{einoschema.AssistantMessage("read", nil)}, nil
		default:
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
	})
	policy := permissions.PolicyFunc(func(_ context.Context, request permissions.Request) (permissions.Decision, error) {
		if request.Permission == permissionManage {
			return permissions.Decision{Action: permissions.ActionDeny}, nil
		}
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	})
	orchestrator := integrationOrchestrator(t, database, registry, streamer, policy, "clear-denial-owner")
	snapshot := integrationSnapshot(t.TempDir(), "clear-denial-workspace")
	runIntegrationRequest(t, orchestrator, "clear-denial-session", "assign", snapshot)
	phase = 2
	runIntegrationRequest(t, orchestrator, "clear-denial-session", "clear", snapshot)
	call, err := database.GetToolCall(context.Background(), "clear-denial-clear")
	if err != nil || call.Status != session.ToolCallFailed {
		t.Fatalf("denied clear call=%#v err=%v", call, err)
	}
	phase = 4
	runIntegrationRequest(t, orchestrator, "clear-denial-session", "read", snapshot)
	if assigned.Status != "completed" || preserved.Result.Text != "42" || preserved.Generation != 0 {
		t.Fatalf("state after denied clear assigned=%#v preserved=%#v", assigned, preserved)
	}
}
