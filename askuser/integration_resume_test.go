package askuser

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

func TestIntegrationPendingResumeClaimsThenInvokesOnce(t *testing.T) {
	base := testOptions()
	descriptor, canonicalInput := acquireAskResumeFixture(t, base)
	database, run := seedAskResumeRun(t, descriptor, canonicalInput, session.ToolCallPending)
	defer database.Close()
	var calls atomic.Int32
	var observedRunning atomic.Bool
	resumedOptions := base
	resumedOptions.Responder = ResponderFunc(func(_ context.Context, request Request) (Response, error) {
		calls.Add(1)
		call, err := database.GetToolCall(context.Background(), request.ToolCallID)
		if err == nil && call.Status == session.ToolCallRunning {
			observedRunning.Store(true)
		}
		return Response{Kind: ResponseSelected, SelectedOption: 1}, nil
	})
	registry, mount := mountTestRegistry(t, resumedOptions)
	defer closeIntegrationMount(t, mount)
	handle, err := newAskResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitAskResume(t, handle)
	call, err := database.GetToolCall(context.Background(), "resume-ask-call")
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := decodeDurableAskResult(call.Output, &result); err != nil {
		t.Fatal(err)
	}
	if call.Status != session.ToolCallCompleted || calls.Load() != 1 || !observedRunning.Load() || result.Status != StatusSelected || result.Answer != "Canary" {
		t.Fatalf("pending resume call=%#v calls=%d observed-running=%t result=%#v", call, calls.Load(), observedRunning.Load(), result)
	}
}

func TestIntegrationRunningResumeInterruptsWithoutResponder(t *testing.T) {
	base := testOptions()
	descriptor, canonicalInput := acquireAskResumeFixture(t, base)
	database, run := seedAskResumeRun(t, descriptor, canonicalInput, session.ToolCallRunning)
	defer database.Close()
	var calls atomic.Int32
	resumedOptions := base
	resumedOptions.Responder = ResponderFunc(func(context.Context, Request) (Response, error) {
		calls.Add(1)
		return Response{Kind: ResponseSelected, SelectedOption: 1}, nil
	})
	registry, mount := mountTestRegistry(t, resumedOptions)
	defer closeIntegrationMount(t, mount)
	handle, err := newAskResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitAskResume(t, handle)
	call, err := database.GetToolCall(context.Background(), "resume-ask-call")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != session.ToolCallInterrupted || calls.Load() != 0 {
		t.Fatalf("running resume status=%s calls=%d output=%s", call.Status, calls.Load(), call.Output)
	}
}

func TestIntegrationResumeRejectsConfigurationDriftBeforeMutation(t *testing.T) {
	base := testOptions()
	descriptor, canonicalInput := acquireAskResumeFixture(t, base)
	database, run := seedAskResumeRun(t, descriptor, canonicalInput, session.ToolCallPending)
	defer database.Close()
	beforeCall, _ := database.GetToolCall(context.Background(), "resume-ask-call")
	beforeRun, _ := database.GetRun(context.Background(), run.ID)
	var calls atomic.Int32
	changed := base
	changed.Limits.MaxQuestionBytes++
	changed.Responder = ResponderFunc(func(context.Context, Request) (Response, error) {
		calls.Add(1)
		return Response{Kind: ResponseSelected, SelectedOption: 1}, nil
	})
	registry, mount := mountTestRegistry(t, changed)
	defer closeIntegrationMount(t, mount)
	handle, err := newAskResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
	if handle != nil || !errors.Is(err, runtime.ErrExtensionPlanMismatch) {
		t.Fatalf("drift resume handle-present=%t err=%v", handle != nil, err)
	}
	afterCall, _ := database.GetToolCall(context.Background(), "resume-ask-call")
	afterRun, _ := database.GetRun(context.Background(), run.ID)
	if calls.Load() != 0 || afterCall.Status != beforeCall.Status || afterCall.ClaimToken != beforeCall.ClaimToken || afterRun.Status != beforeRun.Status || afterRun.ClaimToken != beforeRun.ClaimToken {
		t.Fatalf("resume mismatch mutated durable state")
	}
}

func acquireAskResumeFixture(t *testing.T, options Options) (session.ExtensionPlanDescriptor, json.RawMessage) {
	t.Helper()
	registry, mount := mountTestRegistry(t, options)
	plan, tool := resolveTestTool(t, registry, "resume-session")
	canonical, err := tool.InputDecoder.DecodeToolInput(context.Background(), json.RawMessage(integrationInput))
	if err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	plan.Release()
	closeIntegrationMount(t, mount)
	return descriptor, canonical
}

func seedAskResumeRun(t *testing.T, descriptor session.ExtensionPlanDescriptor, canonical json.RawMessage, status session.ToolCallStatus) (*store.Store, session.Run) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "ask-resume.db"))
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
		Agent: "ask-integration-agent", ProviderID: "ask-integration-provider", ModelID: "ask-integration-model",
		Status:        session.RunPending,
		Config:        map[string]string{"workspace_id": "ask-integration-workspace", "workspace_root": workspace},
		ExtensionPlan: descriptor, CreatedAt: now,
	}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	execution := database.Execution(session.RunFence{RunID: run.ID, ClaimToken: run.ClaimToken})
	if _, err := execution.AppendMessage(ctx, session.Message{
		ID: "resume-assistant", SessionID: run.SessionID, RunID: run.ID,
		Role: session.RoleAssistant, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	call := session.ToolCall{
		ID: "resume-ask-call", SessionID: run.SessionID, RunID: run.ID, MessageID: "resume-assistant",
		RequestPartID: "resume-request-part", ResultMessageID: "resume-result-message", ResultPartID: "resume-result-part",
		Name: ToolName, Pattern: PermissionAsk, Input: append(json.RawMessage(nil), canonical...),
		Status: session.ToolCallPending, RetrySafe: false,
	}
	requestPayload, _ := json.Marshal(map[string]any{"id": call.ID, "name": call.Name, "arguments": canonical})
	created, err := execution.CreateToolCall(ctx, session.CreateToolCallRequest{
		Call: call,
		RequestPart: session.Part{
			ID: call.RequestPartID, MessageID: call.MessageID, SessionID: call.SessionID, RunID: call.RunID,
			Kind: session.PartToolCall, Payload: requestPayload, CreatedAt: now, UpdatedAt: now,
		},
		Event: session.ToolTransitionEvent{ID: "resume-create-event", CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseUntil := run.LeaseUntil
	if status == session.ToolCallRunning {
		claimed, err := execution.ClaimToolCall(ctx, session.ClaimToolCallRequest{
			ID: created.Call.ID, ClaimedBy: "old-owner", ClaimToken: "old-tool-claim",
			StartedAt: now, LeaseDuration: time.Millisecond,
			Event: session.ToolTransitionEvent{ID: "resume-claim-event", CreatedAt: now},
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
		defer timer.Stop()
		<-timer.C
	}
	return database, run
}

func newAskResumeOrchestrator(t *testing.T, database *store.Store, registry *composition.Registry) *runtime.StreamingOrchestrator {
	t.Helper()
	streamer := askIntegrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
		return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
	})
	return newAskIntegrationOrchestrator(t, database, registry, streamer, permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	}), "resume-owner")
}

func waitAskResume(t *testing.T, handle runtime.Handle) {
	t.Helper()
	select {
	case result := <-handle.Done():
		if result.Error != nil {
			t.Fatalf("resume result = %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resume timed out")
	}
}
