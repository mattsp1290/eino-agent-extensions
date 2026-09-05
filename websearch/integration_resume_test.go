package websearch

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
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
)

func TestIntegrationPendingResumeClaimsThenSearchesOnce(t *testing.T) {
	base := testOptions()
	descriptor, canonicalInput := acquireResumeFixture(t, base, testComponent("resume"))
	database, run := seedResumeRun(t, descriptor, canonicalInput, session.ToolCallPending)
	defer database.Close()
	var calls atomic.Int32
	var observedRunning atomic.Bool
	resumed := base
	resumed.Searcher = SearcherFunc(func(_ context.Context, _ string) ([]Source, error) {
		calls.Add(1)
		call, err := database.GetToolCall(context.Background(), "resume-web-search-call")
		if err == nil && call.Status == session.ToolCallRunning {
			observedRunning.Store(true)
		}
		return []Source{{Title: "resumed", URL: "https://example.test/resumed", Snippet: "once"}}, nil
	})
	registry, mount := mountResumeRegistry(t, resumed, testComponent("resume"))
	defer closeTestMount(t, mount)
	handle, err := newResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitIntegration(t, handle)
	call, err := database.GetToolCall(context.Background(), "resume-web-search-call")
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := decodeDurableResult(call.Output, &result); err != nil {
		t.Fatal(err)
	}
	if call.Status != session.ToolCallCompleted || calls.Load() != 1 || !observedRunning.Load() || len(result.Results) != 1 || result.Results[0].Title != "resumed" {
		t.Fatalf("call=%#v calls=%d observed=%t result=%#v", call, calls.Load(), observedRunning.Load(), result)
	}
}

func TestIntegrationRunningResumeInterruptsWithoutSearcher(t *testing.T) {
	base := testOptions()
	descriptor, canonicalInput := acquireResumeFixture(t, base, testComponent("resume"))
	database, run := seedResumeRun(t, descriptor, canonicalInput, session.ToolCallRunning)
	defer database.Close()
	var calls atomic.Int32
	base.Searcher = SearcherFunc(func(context.Context, string) ([]Source, error) {
		calls.Add(1)
		return nil, nil
	})
	registry, mount := mountResumeRegistry(t, base, testComponent("resume"))
	defer closeTestMount(t, mount)
	handle, err := newResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitIntegration(t, handle)
	call, err := database.GetToolCall(context.Background(), "resume-web-search-call")
	if err != nil || call.Status != session.ToolCallInterrupted || calls.Load() != 0 {
		t.Fatalf("call=%#v calls=%d err=%v", call, calls.Load(), err)
	}
}

func TestIntegrationTerminalResumeDoesNotSearchAgain(t *testing.T) {
	base := testOptions()
	descriptor, canonicalInput := acquireResumeFixture(t, base, testComponent("resume"))
	database, run := seedResumeRun(t, descriptor, canonicalInput, session.ToolCallCompleted)
	defer database.Close()
	var calls atomic.Int32
	base.Searcher = SearcherFunc(func(context.Context, string) ([]Source, error) {
		calls.Add(1)
		return nil, nil
	})
	registry, mount := mountResumeRegistry(t, base, testComponent("resume"))
	defer closeTestMount(t, mount)
	handle, err := newResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitIntegration(t, handle)
	call, err := database.GetToolCall(context.Background(), "resume-web-search-call")
	if err != nil || call.Status != session.ToolCallCompleted || calls.Load() != 0 {
		t.Fatalf("call=%#v calls=%d err=%v", call, calls.Load(), err)
	}
}

func TestIntegrationResumeRejectsDriftBeforeDurableMutation(t *testing.T) {
	for name, mutate := range map[string]func(*Options, *extensionFixture){
		"configuration":     func(options *Options, _ *extensionFixture) { options.Limits.MaxTitleBytes++ },
		"searcher identity": func(options *Options, _ *extensionFixture) { options.SearcherIdentity += "-changed" },
		"artifact":          func(_ *Options, fixture *extensionFixture) { fixture.component.Artifact.Hash += "-changed" },
	} {
		t.Run(name, func(t *testing.T) {
			base := testOptions()
			component := testComponent("resume")
			descriptor, canonicalInput := acquireResumeFixture(t, base, component)
			database, run := seedResumeRun(t, descriptor, canonicalInput, session.ToolCallPending)
			defer database.Close()
			beforeCall, _ := database.GetToolCall(context.Background(), "resume-web-search-call")
			beforeRun, _ := database.GetRun(context.Background(), run.ID)
			beforeMessages, _ := database.ListMessages(context.Background(), run.SessionID, session.ReplayCursor{Limit: 100})
			beforeEvents, _ := database.ListEvents(context.Background(), run.SessionID, session.EventCursor{Limit: 100})
			var calls atomic.Int32
			base.Searcher = SearcherFunc(func(context.Context, string) ([]Source, error) {
				calls.Add(1)
				return nil, nil
			})
			fixture := extensionFixture{component: component}
			mutate(&base, &fixture)
			registry, mount := mountResumeRegistry(t, base, fixture.component)
			defer closeTestMount(t, mount)
			handle, err := newResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
			if handle != nil || !errors.Is(err, runtime.ErrExtensionPlanMismatch) {
				t.Fatalf("resume handle=%v err=%v", handle, err)
			}
			afterCall, _ := database.GetToolCall(context.Background(), "resume-web-search-call")
			afterRun, _ := database.GetRun(context.Background(), run.ID)
			afterMessages, _ := database.ListMessages(context.Background(), run.SessionID, session.ReplayCursor{Limit: 100})
			afterEvents, _ := database.ListEvents(context.Background(), run.SessionID, session.EventCursor{Limit: 100})
			if calls.Load() != 0 || afterCall.Status != beforeCall.Status || afterCall.ClaimToken != beforeCall.ClaimToken || afterRun.Status != beforeRun.Status || afterRun.ClaimToken != beforeRun.ClaimToken || len(afterMessages.Parts) != len(beforeMessages.Parts) || len(afterEvents.Events) != len(beforeEvents.Events) {
				t.Fatal("resume mismatch mutated durable state")
			}
		})
	}
}

type extensionFixture struct {
	component extension.Component
}

func acquireResumeFixture(t *testing.T, options Options, component extension.Component) (session.ExtensionPlanDescriptor, json.RawMessage) {
	t.Helper()
	registry, mount := mountResumeRegistry(t, options, component)
	plan, tool := resolveTestTool(t, registry, "resume-session")
	canonical, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(integrationInput))
	if err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	plan.Release()
	closeTestMount(t, mount)
	return descriptor, canonical
}

func mountResumeRegistry(t *testing.T, options Options, component extension.Component) (*composition.Registry, *composition.Mount) {
	t.Helper()
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	mount, err := Mount(context.Background(), registry, component, options)
	if err != nil {
		t.Fatal(err)
	}
	return registry, mount
}

func seedResumeRun(t *testing.T, descriptor session.ExtensionPlanDescriptor, canonical json.RawMessage, status session.ToolCallStatus) (*store.Store, session.Run) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "web-search-resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := database.CreateSession(ctx, session.Session{ID: "resume-session", Directory: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run, err := database.AdmitRun(ctx, session.Run{
		ID: "resume-run", SessionID: "resume-session", OwnerID: "old-owner", ClaimToken: "old-claim",
		Agent: "web-search-integration-agent", ProviderID: "web-search-integration-provider", ModelID: "web-search-integration-model",
		Status: session.RunPending, Config: map[string]string{}, ExtensionPlan: descriptor, CreatedAt: now,
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
		ID: "resume-web-search-call", SessionID: run.SessionID, RunID: run.ID, MessageID: "resume-assistant",
		RequestPartID: "resume-request-part", ResultMessageID: "resume-result-message", ResultPartID: "resume-result-part",
		Name: ToolName, Pattern: permissionPattern, Input: append(json.RawMessage(nil), canonical...),
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
			encodedResult, _ := json.Marshal(Result{Results: []Source{{Title: "already", URL: "https://example.test/already", Snippet: "settled"}}})
			toolOutput, _ := json.Marshal(runtime.ToolOutput{Status: "completed", Content: string(encodedResult), Structured: encodedResult, InlineSize: int64(len(encodedResult)) * 2, OriginalSize: int64(len(encodedResult)) * 2})
			completedAt := now.Add(time.Millisecond)
			if _, err := execution.SettleToolCall(ctx, session.SettleToolCallRequest{
				Settlement: session.ToolSettlement{
					ID: call.ID, ClaimedBy: claimed.Call.ClaimedBy, ClaimToken: claimed.Call.ClaimToken,
					Status: session.ToolCallCompleted, Output: toolOutput, CompletedAt: completedAt,
					ResultMessage: session.Message{ID: call.ResultMessageID, SessionID: call.SessionID, RunID: call.RunID, ParentID: call.MessageID, Role: session.RoleTool, CreatedAt: completedAt, UpdatedAt: completedAt},
					ResultPart:    session.Part{ID: call.ResultPartID, MessageID: call.ResultMessageID, SessionID: call.SessionID, RunID: call.RunID, Kind: session.PartToolResult, Payload: toolOutput, CreatedAt: completedAt, UpdatedAt: completedAt},
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
