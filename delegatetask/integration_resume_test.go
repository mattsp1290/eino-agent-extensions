package delegatetask

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
)

func TestIntegrationPendingResumeClaimsAndInvokesOnce(t *testing.T) {
	base := testOptions()
	descriptor, canonical := acquireResumeFixture(t, base)
	database, run := seedResumeRun(t, descriptor, canonical, session.ToolCallPending)
	defer database.Close()
	var calls atomic.Int32
	var observedRunning atomic.Bool
	resumed := base
	resumed.Runner = RunnerFunc(func(_ context.Context, request Request) (Response, error) {
		calls.Add(1)
		call, err := database.GetToolCall(context.Background(), request.ToolCallID)
		if err == nil && call.Status == session.ToolCallRunning {
			observedRunning.Store(true)
		}
		return Response{Status: ResponseCompleted, Output: "resumed"}, nil
	})
	registry, mount := mountTestRegistry(t, resumed)
	defer closeIntegrationMount(t, mount)
	handle, err := newResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitResume(t, handle)
	call, err := database.GetToolCall(context.Background(), "resume-delegate-call")
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := decodeDurableResult(call.Output, &result); err != nil {
		t.Fatal(err)
	}
	if call.Status != session.ToolCallCompleted || calls.Load() != 1 || !observedRunning.Load() || result != (Result{Status: StatusCompleted, Output: "resumed"}) {
		t.Fatalf("call=%#v calls=%d running=%t result=%#v", call, calls.Load(), observedRunning.Load(), result)
	}
}

func TestIntegrationRunningResumeInterruptsWithoutRunner(t *testing.T) {
	base := testOptions()
	descriptor, canonical := acquireResumeFixture(t, base)
	database, run := seedResumeRun(t, descriptor, canonical, session.ToolCallRunning)
	defer database.Close()
	var calls atomic.Int32
	resumed := base
	resumed.Runner = RunnerFunc(func(context.Context, Request) (Response, error) {
		calls.Add(1)
		return Response{Status: ResponseCompleted}, nil
	})
	registry, mount := mountTestRegistry(t, resumed)
	defer closeIntegrationMount(t, mount)
	handle, err := newResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitResume(t, handle)
	call, err := database.GetToolCall(context.Background(), "resume-delegate-call")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != session.ToolCallInterrupted || calls.Load() != 0 {
		t.Fatalf("call=%#v calls=%d", call, calls.Load())
	}
}

func TestIntegrationTerminalResumeDoesNotInvokeRunner(t *testing.T) {
	base := testOptions()
	descriptor, canonical := acquireResumeFixture(t, base)
	database, run := seedResumeRun(t, descriptor, canonical, session.ToolCallCompleted)
	defer database.Close()
	var calls atomic.Int32
	resumed := base
	resumed.Runner = RunnerFunc(func(context.Context, Request) (Response, error) {
		calls.Add(1)
		return Response{Status: ResponseCompleted}, nil
	})
	registry, mount := mountTestRegistry(t, resumed)
	defer closeIntegrationMount(t, mount)
	handle, err := newResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitResume(t, handle)
	call, err := database.GetToolCall(context.Background(), "resume-delegate-call")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != session.ToolCallCompleted || calls.Load() != 0 {
		t.Fatalf("call=%#v calls=%d", call, calls.Load())
	}
}

func TestIntegrationResumeRejectsEveryConfigurationDriftBeforeMutation(t *testing.T) {
	mutations := map[string]func(*Options){
		"runner identity": func(o *Options) { o.RunnerIdentity += "-changed" },
		"task":            func(o *Options) { o.Limits.MaxTaskBytes++ },
		"profile":         func(o *Options) { o.Limits.MaxProfileBytes++ },
		"result":          func(o *Options) { o.Limits.MaxResultBytes++ },
		"capacity":        func(o *Options) { o.Limits.MaxInFlight++ },
		"wait":            func(o *Options) { o.Limits.MaxWait++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			base := testOptions()
			descriptor, canonical := acquireResumeFixture(t, base)
			database, run := seedResumeRun(t, descriptor, canonical, session.ToolCallPending)
			defer database.Close()
			beforeCall, _ := database.GetToolCall(context.Background(), "resume-delegate-call")
			beforeRun, _ := database.GetRun(context.Background(), run.ID)
			var calls atomic.Int32
			changed := base
			mutate(&changed)
			changed.Runner = RunnerFunc(func(context.Context, Request) (Response, error) {
				calls.Add(1)
				return Response{Status: ResponseCompleted}, nil
			})
			registry, mount := mountTestRegistry(t, changed)
			defer closeIntegrationMount(t, mount)
			handle, err := newResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
			if handle != nil || !errors.Is(err, runtime.ErrExtensionPlanMismatch) {
				t.Fatalf("handle=%v err=%v", handle, err)
			}
			afterCall, _ := database.GetToolCall(context.Background(), "resume-delegate-call")
			afterRun, _ := database.GetRun(context.Background(), run.ID)
			if calls.Load() != 0 || afterCall.Status != beforeCall.Status || afterCall.ClaimToken != beforeCall.ClaimToken || afterRun.Status != beforeRun.Status || afterRun.ClaimToken != beforeRun.ClaimToken {
				t.Fatal("resume mismatch mutated durable state")
			}
		})
	}
}

func acquireResumeFixture(t *testing.T, options Options) (session.ExtensionPlanDescriptor, json.RawMessage) {
	t.Helper()
	registry, mount := mountTestRegistry(t, options)
	plan, tool := resolveTestTool(t, registry, "resume-session", "resume-workspace", t.TempDir())
	canonical, err := tool.InputDecoder.DecodeToolInput(context.Background(), json.RawMessage(integrationInput))
	if err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	plan.Release()
	closeIntegrationMount(t, mount)
	return descriptor, canonical
}

func seedResumeRun(t *testing.T, descriptor session.ExtensionPlanDescriptor, canonical json.RawMessage, status session.ToolCallStatus) (*store.Store, session.Run) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "delegate-resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	workspace := t.TempDir()
	if _, err := database.CreateSession(ctx, session.Session{ID: "resume-session", Directory: workspace, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run, err := database.AdmitRun(ctx, session.Run{
		ID: "resume-run", SessionID: "resume-session", OwnerID: "old-owner", ClaimToken: "old-claim",
		Agent: "delegate-integration-agent", ProviderID: "delegate-integration-provider", ModelID: "delegate-integration-model",
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
		ID: "resume-delegate-call", SessionID: run.SessionID, RunID: run.ID, MessageID: "resume-assistant",
		RequestPartID: "resume-request-part", ResultMessageID: "resume-result-message", ResultPartID: "resume-result-part",
		Name: ToolName, Pattern: "delegate-profile:read-only", Input: append(json.RawMessage(nil), canonical...),
		Status: session.ToolCallPending, RetrySafe: false,
	}
	requestPayload, _ := json.Marshal(map[string]any{"id": call.ID, "name": call.Name, "arguments": canonical})
	created, err := execution.CreateToolCall(ctx, session.CreateToolCallRequest{
		Call:        call,
		RequestPart: session.Part{ID: call.RequestPartID, MessageID: call.MessageID, SessionID: call.SessionID, RunID: call.RunID, Kind: session.PartToolCall, Payload: requestPayload, CreatedAt: now, UpdatedAt: now},
		Event:       session.ToolTransitionEvent{ID: "resume-create-event", CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseUntil := run.LeaseUntil
	if status == session.ToolCallRunning || status == session.ToolCallCompleted {
		claimed, err := execution.ClaimToolCall(ctx, session.ClaimToolCallRequest{
			ID: created.Call.ID, ClaimedBy: "old-owner", ClaimToken: "old-tool-claim", StartedAt: now,
			LeaseDuration: time.Millisecond, Event: session.ToolTransitionEvent{ID: "resume-claim-event", CreatedAt: now},
		})
		if err != nil {
			t.Fatal(err)
		}
		if claimed.Call.LeaseUntil.After(leaseUntil) {
			leaseUntil = claimed.Call.LeaseUntil
		}
		if status == session.ToolCallCompleted {
			structured, _ := json.Marshal(Result{Status: StatusCompleted, Output: "already settled"})
			durableOutput, _ := json.Marshal(runtime.ToolOutput{ToolCallID: string(call.ID), Status: "completed", Content: string(structured), Structured: structured})
			completedAt := now.Add(2 * time.Millisecond)
			if _, err := execution.SettleToolCall(ctx, session.SettleToolCallRequest{
				Settlement: session.ToolSettlement{
					ID: call.ID, ClaimedBy: claimed.Call.ClaimedBy, ClaimToken: claimed.Call.ClaimToken,
					Status: session.ToolCallCompleted, Output: durableOutput, CompletedAt: completedAt,
					ResultMessage: session.Message{ID: call.ResultMessageID, SessionID: call.SessionID, RunID: call.RunID, ParentID: call.MessageID, Role: session.RoleTool, CreatedAt: completedAt, UpdatedAt: completedAt},
					ResultPart:    session.Part{ID: call.ResultPartID, MessageID: call.ResultMessageID, SessionID: call.SessionID, RunID: call.RunID, Kind: session.PartToolResult, Payload: durableOutput, CreatedAt: completedAt, UpdatedAt: completedAt},
				},
				Event: session.ToolTransitionEvent{ID: "resume-terminal-event", CreatedAt: completedAt},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	wait := time.Until(leaseUntil) + 10*time.Millisecond
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		<-timer.C
	}
	return database, run
}

func newResumeOrchestrator(t *testing.T, database *store.Store, registry *composition.Registry) *runtime.StreamingOrchestrator {
	t.Helper()
	streamer := integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
		return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
	})
	return newIntegrationOrchestrator(t, database, registry, streamer, permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	}), "resume-owner")
}

func waitResume(t *testing.T, handle runtime.Handle) {
	t.Helper()
	select {
	case result := <-handle.Done():
		if result.Error != nil {
			t.Fatalf("resume=%#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resume timed out")
	}
}
