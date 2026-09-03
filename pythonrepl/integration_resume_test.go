//go:build linux || darwin

package pythonrepl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
)

func TestIntegrationPendingResumeExecutesOnceWithFreshState(t *testing.T) {
	options := testOptions(t)
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "pending-marker")
	descriptor, canonical := acquireResumeFixture(t, options, `open("pending-marker", "a").write("x")`)
	database, run := seedResumeRun(t, descriptor, canonical, workspace, session.ToolCallPending)
	deferDatabaseClose(t, database)
	registry, mount := mountIntegrationRegistry(t, options)
	defer closeIntegrationMount(t, mount)
	streamer := integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
		return []*einoschema.Message{einoschema.AssistantMessage("resumed", nil)}, nil
	})
	orchestrator := integrationOrchestrator(t, database, registry, streamer, allowIntegrationPolicy(), "resume-owner")
	handle, err := orchestrator.Resume(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitResumeRun(t, handle)
	call, err := database.GetToolCall(context.Background(), "resume-python-call")
	if err != nil || call.Status != session.ToolCallCompleted {
		t.Fatalf("pending call=%#v err=%v", call, err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "x" {
		t.Fatalf("pending execution marker=%q err=%v", raw, err)
	}
}

func TestIntegrationRunningResumeInterruptsWithoutPython(t *testing.T) {
	options := testOptions(t)
	workspace := t.TempDir()
	descriptor, canonical := acquireResumeFixture(t, options, `open("running-marker", "w").write("bad")`)
	database, run := seedResumeRun(t, descriptor, canonical, workspace, session.ToolCallRunning)
	deferDatabaseClose(t, database)
	registry, mount := mountIntegrationRegistry(t, options)
	defer closeIntegrationMount(t, mount)
	orchestrator := integrationOrchestrator(t, database, registry, integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
		return []*einoschema.Message{einoschema.AssistantMessage("resumed", nil)}, nil
	}), allowIntegrationPolicy(), "resume-owner")
	handle, err := orchestrator.Resume(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitResumeRun(t, handle)
	call, err := database.GetToolCall(context.Background(), "resume-python-call")
	if err != nil || call.Status != session.ToolCallInterrupted {
		t.Fatalf("running call=%#v err=%v", call, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "running-marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("running call executed: %v", err)
	}
	entries, err := os.ReadDir(options.TempRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("running resume created venv entries=%d err=%v", len(entries), err)
	}
}

func TestIntegrationResumeRejectsFingerprintDriftBeforePython(t *testing.T) {
	options := testOptions(t)
	workspace := t.TempDir()
	descriptor, canonical := acquireResumeFixture(t, options, `open("drift-marker", "w").write("bad")`)
	database, run := seedResumeRun(t, descriptor, canonical, workspace, session.ToolCallPending)
	deferDatabaseClose(t, database)
	beforeCall, _ := database.GetToolCall(context.Background(), "resume-python-call")
	changed := options
	changed.PythonIdentity = "test-python-v2"
	registry, mount := mountIntegrationRegistry(t, changed)
	defer closeIntegrationMount(t, mount)
	orchestrator := integrationOrchestrator(t, database, registry, integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
		return []*einoschema.Message{einoschema.AssistantMessage("must-not-run", nil)}, nil
	}), allowIntegrationPolicy(), "changed-owner")
	handle, err := orchestrator.Resume(context.Background(), run.ID)
	if handle != nil || !errors.Is(err, runtime.ErrExtensionPlanMismatch) {
		t.Fatalf("drift handle=%t err=%v", handle != nil, err)
	}
	afterCall, _ := database.GetToolCall(context.Background(), "resume-python-call")
	if afterCall.Status != beforeCall.Status || afterCall.ClaimToken != beforeCall.ClaimToken {
		t.Fatal("drift mutated durable call")
	}
	if _, err := os.Stat(filepath.Join(workspace, "drift-marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drift executed Python: %v", err)
	}
}

func TestIntegrationRemountStartsWithEmptyState(t *testing.T) {
	options := testOptions(t)
	database := openIntegrationStore(t, "remount.db")
	deferDatabaseClose(t, database)
	workspace := t.TempDir()
	snapshot := integrationSnapshot(workspace, "remount-workspace")
	ids := &integrationIDs{}
	firstRegistry, firstMount := mountIntegrationRegistry(t, options)
	firstStreamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latest := latestToolMessage(request.Messages); latest != nil && latest.ToolCallID == "remount-assign" {
			return []*einoschema.Message{einoschema.AssistantMessage("assigned", nil)}, nil
		}
		return toolCallMessage("remount-assign", ExecuteToolName, `{"code":"persistent = 42"}`), nil
	})
	firstOrchestrator := integrationOrchestratorWithIDs(t, database, firstRegistry, firstStreamer, allowIntegrationPolicy(), "remount-first-owner", ids)
	runIntegrationRequest(t, firstOrchestrator, "remount-session", "assign", snapshot)
	closeIntegrationMount(t, firstMount)

	secondRegistry, secondMount := mountIntegrationRegistry(t, options)
	defer closeIntegrationMount(t, secondMount)
	var result ExecuteResult
	secondStreamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latest := latestToolMessage(request.Messages); latest != nil && latest.ToolCallID == "remount-read" {
			if err := decodeIntegrationResult(latest.Content, &result); err != nil {
				return nil, err
			}
			return []*einoschema.Message{einoschema.AssistantMessage("read", nil)}, nil
		}
		return toolCallMessage("remount-read", ExecuteToolName, `{"code":"persistent"}`), nil
	})
	secondOrchestrator := integrationOrchestratorWithIDs(t, database, secondRegistry, secondStreamer, allowIntegrationPolicy(), "remount-second-owner", ids)
	runIntegrationRequest(t, secondOrchestrator, "remount-session", "read", snapshot)
	if result.Status != "python_error" || result.Generation != 0 || result.StateReset {
		t.Fatalf("remount result=%#v", result)
	}
}

func acquireResumeFixture(t *testing.T, options Options, code string) (session.ExtensionPlanDescriptor, json.RawMessage) {
	t.Helper()
	registry, mount := mountIntegrationRegistry(t, options)
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "resume-session"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "resume-session", WorkspaceID: "resume-workspace", WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var execute runtime.Tool
	for _, tool := range resolved {
		if tool.Name == ExecuteToolName {
			execute = tool
		}
	}
	raw, _ := json.Marshal(map[string]string{"code": code})
	canonical, err := execute.InputDecoder.DecodeToolInput(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	plan.Release()
	closeIntegrationMount(t, mount)
	return descriptor, canonical
}

func seedResumeRun(t *testing.T, descriptor session.ExtensionPlanDescriptor, canonical json.RawMessage, workspace string, status session.ToolCallStatus) (*store.Store, session.Run) {
	t.Helper()
	ctx := context.Background()
	database := openIntegrationStore(t, "resume.db")
	now := time.Now().UTC()
	if _, err := database.CreateSession(ctx, session.Session{ID: "resume-session", Directory: workspace, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run, err := database.AdmitRun(ctx, session.Run{
		ID: "resume-run", SessionID: "resume-session", OwnerID: "old-owner", ClaimToken: "old-claim",
		Agent: "python-integration-agent", ProviderID: "python-integration-provider", ModelID: "python-integration-model",
		Status: session.RunPending, Config: map[string]string{"workspace_id": "resume-workspace", "workspace_root": workspace},
		ExtensionPlan: descriptor, CreatedAt: now,
	}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	execution := database.Execution(session.RunFence{RunID: run.ID, ClaimToken: run.ClaimToken})
	if _, err := execution.AppendMessage(ctx, session.Message{ID: "resume-assistant", SessionID: run.SessionID, RunID: run.ID, Role: session.RoleAssistant, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	call := session.ToolCall{
		ID: "resume-python-call", SessionID: run.SessionID, RunID: run.ID, MessageID: "resume-assistant",
		RequestPartID: "resume-request", ResultMessageID: "resume-result-message", ResultPartID: "resume-result-part",
		Name: ExecuteToolName, Pattern: permissionExecute, Input: append(json.RawMessage(nil), canonical...), Status: session.ToolCallPending, RetrySafe: false,
	}
	requestPayload, _ := json.Marshal(map[string]any{"id": call.ID, "name": call.Name, "arguments": canonical})
	created, err := execution.CreateToolCall(ctx, session.CreateToolCallRequest{
		Call:        call,
		RequestPart: session.Part{ID: call.RequestPartID, MessageID: call.MessageID, SessionID: call.SessionID, RunID: call.RunID, Kind: session.PartToolCall, Payload: requestPayload, CreatedAt: now, UpdatedAt: now},
		Event:       session.ToolTransitionEvent{ID: "resume-create", CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseUntil := run.LeaseUntil
	if status == session.ToolCallRunning {
		claimed, err := execution.ClaimToolCall(ctx, session.ClaimToolCallRequest{
			ID: created.Call.ID, ClaimedBy: "old-owner", ClaimToken: "old-tool-claim", StartedAt: now,
			LeaseDuration: time.Millisecond, Event: session.ToolTransitionEvent{ID: "resume-claim", CreatedAt: now},
		})
		if err != nil {
			t.Fatal(err)
		}
		if claimed.Call.LeaseUntil.After(leaseUntil) {
			leaseUntil = claimed.Call.LeaseUntil
		}
	}
	wait := time.Until(leaseUntil) + 10*time.Millisecond
	if wait > 0 {
		timer := time.NewTimer(wait)
		<-timer.C
	}
	return database, run
}

func allowIntegrationPolicy() permissions.Policy {
	return permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	})
}

func waitResumeRun(t *testing.T, handle runtime.Handle) {
	t.Helper()
	select {
	case result := <-handle.Done():
		if result.Error != nil {
			t.Fatalf("resume result=%#v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resume timed out")
	}
}
