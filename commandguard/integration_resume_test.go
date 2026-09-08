package commandguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func seedResumeRun(t *testing.T, database *store.Store, workspace string, descriptor session.ExtensionPlanDescriptor, canonical json.RawMessage, status session.ToolCallStatus) session.Run {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := database.CreateSession(ctx, session.Session{ID: "guard-session", WorkspaceID: "fixture-workspace", Title: "guard-session", Directory: workspace, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run, err := database.AdmitRun(ctx, session.Run{
		ID: "resume-run", SessionID: "guard-session", OwnerID: "old-owner", ClaimToken: "old-claim",
		Agent: "fixture-agent", ProviderID: "fixture-provider", ModelID: "fixture-model",
		Status: session.RunPending, Config: map[string]string{"workspace_id": "fixture-workspace", "workspace_root": workspace}, ExtensionPlan: descriptor, CreatedAt: now,
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
		ID: "resume-call", SessionID: run.SessionID, RunID: run.ID, MessageID: "resume-assistant",
		RequestPartID: "resume-request-part", ResultMessageID: "resume-result-message", ResultPartID: "resume-result-part",
		Name: "fixture-command", Pattern: "fixture-command", Input: append(json.RawMessage(nil), canonical...),
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
			encodedResult, _ := json.Marshal(map[string]string{"outcome": "already settled"})
			toolOutput, _ := json.Marshal(runtime.ToolOutput{ToolCallID: string(call.ID), Status: "completed", Content: string(encodedResult), Structured: encodedResult, InlineSize: int64(len(encodedResult)) * 2, OriginalSize: int64(len(encodedResult)) * 2})
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
	return run
}

func resumeDescriptor(t *testing.T, o Options) session.ExtensionPlanDescriptor {
	t.Helper()
	f := newIntegrationFixture(t)
	f.tool()
	f.guard(o)
	installResumeProbe(f, new(atomic.Int32), new(atomic.Int32))
	p := testPlan(t, f.registry, "guard-session")
	d := p.Descriptor()
	p.Release()
	return d
}
func installResumeProbe(f *integrationFixture, guards, prepares *atomic.Int32) {
	f.mount("resume-probe", func(_ context.Context, r *composition.Registrar) error {
		if err := r.Guard(composition.GuardRegistration{ID: "probe", Scope: extension.GlobalScope(), Order: runtime.OrderHostPolicy + 1, Guard: runtime.ToolGuardFunc(func(context.Context, runtime.ToolGuardRequest) (runtime.ToolGuardResult, error) {
			guards.Add(1)
			return runtime.ToolGuardResult{Decision: runtime.ToolGuardAbstain}, nil
		})}); err != nil {
			return err
		}
		return extension.OnTransform(r.Extensions(), runtime.ToolPreparePoint, extension.Registration{ID: "prepare", Scope: extension.GlobalScope()}, func(_ context.Context, p runtime.PreparedToolCall) (runtime.PreparedToolCall, error) {
			prepares.Add(1)
			p.Call.Input = []byte(`{"script":"blocked"}`)
			return p, nil
		})
	})
}
func TestResumePendingRunningAndTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, script       string
		status, want       session.ToolCallStatus
		executions, guards int32
	}{
		{"pending blocked", "blocked SAVED_MARKER", session.ToolCallPending, session.ToolCallFailed, 0, 1},
		{"pending allowed", "echo ok", session.ToolCallPending, session.ToolCallCompleted, 1, 1},
		{"running", "blocked", session.ToolCallRunning, session.ToolCallInterrupted, 0, 0},
		{"terminal", "blocked", session.ToolCallCompleted, session.ToolCallCompleted, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := resumeDescriptor(t, integrationOptions())
			f := newIntegrationFixture(t)
			f.tool()
			f.guard(integrationOptions())
			var guards, prepares atomic.Int32
			installResumeProbe(f, &guards, &prepares)
			raw, _ := json.Marshal(commandInput{Script: tc.script})
			run := seedResumeRun(t, f.database, f.workspace, d, raw, tc.status)
			var unrelated atomic.Int32
			f.mount("later-unrelated", func(_ context.Context, r *composition.Registrar) error {
				return r.Guard(composition.GuardRegistration{ID: "later", Scope: extension.GlobalScope(), Guard: runtime.ToolGuardFunc(func(context.Context, runtime.ToolGuardRequest) (runtime.ToolGuardResult, error) {
					unrelated.Add(1)
					return denial(ruleMatch), nil
				})})
			})
			var next []byte
			streamer := scriptedStreamer(func(_ context.Context, r model.Request) ([]*einoschema.Message, error) {
				next, _ = json.Marshal(r.Messages)
				return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
			})
			orchestrator := f.orchestrator(streamer)
			h, err := orchestrator.Resume(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-h.Done():
				if result.Status != session.RunInterrupted || result.Error != nil {
					t.Fatal(result)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("resume timed out")
			}
			call := f.call("resume-call")
			if call.Status != tc.want || f.executions.Load() != tc.executions || f.permissionCalls.Load() != tc.executions || guards.Load() != tc.guards || prepares.Load() != 0 || unrelated.Load() != 0 || !bytes.Equal(call.Input, raw) {
				t.Fatalf("resume contract: status=%s bodies=%d permissions=%d guards=%d prepares=%d unrelated=%d", call.Status, f.executions.Load(), f.permissionCalls.Load(), guards.Load(), prepares.Load(), unrelated.Load())
			}
			// Eino Resume settles recovery work and ends interrupted. A fresh turn
			// consumes the recovered result without repeating it.
			if tc.status == session.ToolCallPending {
				h, err = orchestrator.Start(context.Background(), f.request())
				if err != nil {
					t.Fatal(err)
				}
				waitRun(t, h)
			}
			if tc.name == "pending blocked" && (!strings.Contains(string(call.Output), ruleMatch.message()) || !strings.Contains(string(next), ruleMatch.message())) {
				t.Fatal("pending denial not visible")
			}
		})
	}
}
func TestResumeRejectsEveryConfigAndArtifactDriftBeforeMutation(t *testing.T) {
	changes := map[string]func(*Options, *extension.Component){
		"rule ID":          func(o *Options, _ *extension.Component) { o.Rules[0].ID += "-changed" },
		"executable":       func(o *Options, _ *extension.Component) { o.Rules[0].Executable = "other" },
		"prefix":           func(o *Options, _ *extension.Component) { o.Rules[1].ArgPrefix[0] = "other" },
		"tool binding":     func(o *Options, _ *extension.Component) { o.Bindings[2].ToolName = "other" },
		"field":            func(o *Options, _ *extension.Component) { o.Bindings[2].CommandField = "other" },
		"dialect":          func(o *Options, _ *extension.Component) { o.Bindings[2].Dialect = DialectPOSIX },
		"scope":            func(o *Options, _ *extension.Component) { o.Scope = extension.SessionScope("guard-session") },
		"order":            func(o *Options, _ *extension.Component) { o.Order = runtime.OrderHostPolicy + 1 },
		"artifact version": func(_ *Options, c *extension.Component) { c.Artifact.Version += "-changed" },
		"artifact hash":    func(_ *Options, c *extension.Component) { c.Artifact.Hash += "-changed" },
		"instance":         func(_ *Options, c *extension.Component) { c.InstanceID += "-changed" },
	}
	fields := reflect.TypeOf(Limits{})
	for i := 0; i < fields.NumField(); i++ {
		changes[fields.Field(i).Name] = func(o *Options, _ *extension.Component) {
			v := reflect.ValueOf(&o.Limits).Elem().Field(i)
			v.SetInt(v.Int() + 1)
		}
	}
	d := resumeDescriptor(t, integrationOptions())
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := newIntegrationFixture(t)
			f.tool()
			var guards, prepares atomic.Int32
			installResumeProbe(f, &guards, &prepares)
			o := integrationOptions()
			c := testComponent("")
			change(&o, &c)
			testMount(t, f.registry, c, o)
			run := seedResumeRun(t, f.database, f.workspace, d, []byte(`{"script":"blocked"}`), session.ToolCallPending)
			before := resumeSnapshot(t, f, run)
			h, err := f.orchestrator(scriptedStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
				return nil, errors.New("must not request model")
			})).Resume(context.Background(), run.ID)
			if h != nil || !errors.Is(err, runtime.ErrExtensionPlanMismatch) {
				t.Fatal("identity drift accepted", err)
			}
			if !bytes.Equal(before, resumeSnapshot(t, f, run)) || guards.Load() != 0 || prepares.Load() != 0 || f.executions.Load() != 0 || f.permissionCalls.Load() != 0 {
				t.Fatal("mismatch mutated durable state")
			}
		})
	}
}
func resumeSnapshot(t *testing.T, f *integrationFixture, run session.Run) []byte {
	t.Helper()
	ctx := context.Background()
	call := f.call("resume-call")
	stored, err := f.database.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := f.database.ListMessages(ctx, run.SessionID, session.ReplayCursor{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	events, err := f.database.ListEvents(ctx, run.SessionID, session.EventCursor{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	requests, err := f.database.ListModelRequests(ctx, run.ID, session.ModelRequestCursor{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(struct {
		Call     session.ToolCall
		Run      session.Run
		Messages session.ReplayBatch
		Events   session.EventBatch
		Requests session.ModelRequestBatch
	}{call, stored, messages, events, requests})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestResumeRejectsBehaviorVersionDrift(t *testing.T) {
	p, err := canonicalize(integrationOptions())
	if err != nil {
		t.Fatal(err)
	}
	original := resumeDescriptor(t, integrationOptions())
	for i := 0; i < reflect.TypeOf(behaviorIdentity{}).NumField(); i++ {
		t.Run(reflect.TypeOf(behaviorIdentity{}).Field(i).Name, func(t *testing.T) {
			old := currentBehavior()
			v := reflect.ValueOf(&old).Elem().Field(i)
			v.SetString(v.String() + "-previous")
			oldHash := hashBehavior(p, old)
			if oldHash == configHash(p) {
				t.Fatal("behavior version omitted from hash")
			}
			// Reconstruct an honestly versioned historical descriptor using the public
			// sealing contract; never invent or overwrite a persisted fingerprint.
			d := original.Clone()
			d.Fingerprint = ""
			for i := range d.Components {
				if d.Components[i].InstanceID == "command-guard" {
					d.Components[i].Artifact.ConfigHash = oldHash
				}
			}
			seal, err := session.SealExtensionPlanForSession("guard-session", d)
			if err != nil {
				t.Fatal(err)
			}
			f := newIntegrationFixture(t)
			f.tool()
			f.guard(integrationOptions())
			installResumeProbe(f, new(atomic.Int32), new(atomic.Int32))
			run := seedResumeRun(t, f.database, f.workspace, seal.Descriptor(), []byte(`{"script":"blocked"}`), session.ToolCallPending)
			before := resumeSnapshot(t, f, run)
			h, err := f.orchestrator(scriptedStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
				return nil, errors.New("unexpected provider call")
			})).Resume(context.Background(), run.ID)
			if h != nil || !errors.Is(err, runtime.ErrExtensionPlanMismatch) || !bytes.Equal(before, resumeSnapshot(t, f, run)) {
				t.Fatal("behavior drift accepted or mutated state", err)
			}
		})
	}
}
