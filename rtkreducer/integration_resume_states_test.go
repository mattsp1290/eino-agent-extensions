//go:build linux || darwin

package rtkreducer

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
	"github.com/mattsp1290/eino-agent/tools"
)

func TestIntegrationResumeToolCallStates(t *testing.T) {
	for _, test := range []struct {
		name               string
		status             session.ToolCallStatus
		wantStatus         session.ToolCallStatus
		wantExecutions     int32
		wantRTKInvocations int
		wantReduction      bool
	}{
		{name: "pending executes and reduces", status: session.ToolCallPending, wantStatus: session.ToolCallCompleted, wantExecutions: 1, wantRTKInvocations: 1, wantReduction: true},
		{name: "running interrupts without execution or reduction", status: session.ToolCallRunning, wantStatus: session.ToolCallInterrupted, wantRTKInvocations: 0},
		{name: "terminal is not executed or reduced again", status: session.ToolCallCompleted, wantStatus: session.ToolCallCompleted, wantRTKInvocations: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			counter := filepath.Join(t.TempDir(), "rtk-invocations")
			executable := writeCountingRTK(t, counter)
			options := fixtureOptionsForExecutable(t, executable)
			options.Bindings[0].Mode = JSONMirror
			options.Bindings[0].Fields = []Field{{Name: "stdout", Filter: Log}}

			var executions atomic.Int32
			registry, database, mounts := newIntegrationFixture(t, options, func(context.Context, tools.Execution) (json.RawMessage, error) {
				executions.Add(1)
				return resumeStateToolResult(), nil
			}, allowIntegrationPolicy())
			defer closeIntegrationFixture(t, database, mounts)

			// Persist the descriptor through the public acquisition/release boundary;
			// Resume then performs Eino's VerifyExtensionPlanForSession and
			// AcquireResumePlan sequence against that saved descriptor.
			plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "resume-session"})
			if err != nil {
				t.Fatal(err)
			}
			descriptor := plan.Descriptor()
			plan.Release()

			raw := resumeStateToolResult()
			run := seedIntegrationResumeStateRun(t, database, descriptor, test.status, raw)
			orchestrator, err := runtime.NewStreamingOrchestrator(
				runtime.WithStore(database), runtime.WithRunPlanProvider(registry),
				runtime.WithModelResolver(integrationResolver{streamer: integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
					return nil, fmt.Errorf("resume state test must not request a model")
				})}),
				runtime.WithPermissions(allowIntegrationPolicy()), runtime.WithOwnerID("new-resume-owner"),
				runtime.WithIDGenerator(&integrationIDs{}), runtime.WithLease(time.Second),
			)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := orchestrator.Resume(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-handle.Done():
				if result.Status != session.RunInterrupted || result.Error != nil {
					t.Fatalf("resume result = %+v", result)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("resume timed out")
			}

			call, err := database.GetToolCall(context.Background(), "resume-state-call")
			if err != nil {
				t.Fatal(err)
			}
			if call.Status != test.wantStatus {
				t.Fatalf("call status = %s, want %s", call.Status, test.wantStatus)
			}
			if executions.Load() != test.wantExecutions {
				t.Fatalf("tool executions = %d, want %d", executions.Load(), test.wantExecutions)
			}
			if got := countFile(t, counter); got != test.wantRTKInvocations {
				t.Fatalf("RTK invocations = %d, want %d; call status=%s output=%s", got, test.wantRTKInvocations, call.Status, call.Output)
			}
			if test.wantReduction {
				var output runtime.ToolOutput
				if err := json.Unmarshal(call.Output, &output); err != nil {
					t.Fatalf("decode resumed output: %v", err)
				}
				if !strings.Contains(output.Content, "[Reduced by RTK log;") {
					t.Fatalf("resumed output was not reduced: %q", output.Content)
				}
				if output.Content != string(output.Structured) {
					t.Fatalf("resumed output mirrors differ: content=%q structured=%q", output.Content, output.Structured)
				}
			} else if test.status == session.ToolCallCompleted {
				var output runtime.ToolOutput
				if err := json.Unmarshal(call.Output, &output); err != nil {
					t.Fatalf("decode terminal output: %v", err)
				}
				if output.Content != string(raw) || string(output.Structured) != string(raw) || strings.Contains(output.Content, "[Reduced by RTK ") {
					t.Fatalf("terminal output changed: content=%q structured=%q", output.Content, output.Structured)
				}
			}
		})
	}
}

func resumeStateToolResult() json.RawMessage {
	return json.RawMessage(`{"stdout":"` + strings.Repeat("verbose resume output ", 100) + `","stderr":"KEEP_RESUME_STDERR","exit_code":9007199254740993123456789}`)
}

func seedIntegrationResumeStateRun(t *testing.T, database *store.Store, descriptor session.ExtensionPlanDescriptor, status session.ToolCallStatus, raw json.RawMessage) session.Run {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := database.CreateSession(ctx, session.Session{ID: "resume-session", Directory: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run, err := database.AdmitRun(ctx, session.Run{
		ID: "resume-state-run", SessionID: "resume-session", OwnerID: "old-owner", ClaimToken: "old-claim",
		Agent: "rtk-integration-agent", ProviderID: "rtk-integration-provider", ModelID: "rtk-integration-model",
		Status: session.RunPending, Config: map[string]string{"workspace_id": "resume-workspace", "workspace_root": t.TempDir()},
		ExtensionPlan: descriptor, CreatedAt: now,
	}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	execution := database.Execution(session.RunFence{RunID: run.ID, ClaimToken: run.ClaimToken})
	if _, err := execution.AppendMessage(ctx, session.Message{ID: "resume-state-assistant", SessionID: run.SessionID, RunID: run.ID, Role: session.RoleAssistant, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	call := session.ToolCall{
		ID: "resume-state-call", SessionID: run.SessionID, RunID: run.ID, MessageID: "resume-state-assistant",
		RequestPartID: "resume-state-request-part", ResultMessageID: "resume-state-result-message", ResultPartID: "resume-state-result-part",
		Name: integrationToolName, Pattern: integrationToolName, Input: json.RawMessage(`{"cmd":"go test -json ./..."}`), Status: session.ToolCallPending,
	}
	payload, _ := json.Marshal(map[string]any{"id": call.ID, "name": call.Name, "arguments": call.Input})
	created, err := execution.CreateToolCall(ctx, session.CreateToolCallRequest{
		Call:        call,
		RequestPart: session.Part{ID: call.RequestPartID, MessageID: call.MessageID, SessionID: call.SessionID, RunID: call.RunID, Kind: session.PartToolCall, Payload: payload, CreatedAt: now, UpdatedAt: now},
		Event:       session.ToolTransitionEvent{ID: "resume-state-create-event", CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseUntil := run.LeaseUntil
	if status == session.ToolCallRunning || status == session.ToolCallCompleted {
		claimed, err := execution.ClaimToolCall(ctx, session.ClaimToolCallRequest{
			ID: created.Call.ID, ClaimedBy: "old-owner", ClaimToken: "old-tool-claim", StartedAt: now,
			LeaseDuration: time.Millisecond, Event: session.ToolTransitionEvent{ID: "resume-state-claim-event", CreatedAt: now},
		})
		if err != nil {
			t.Fatal(err)
		}
		if claimed.Call.LeaseUntil.After(leaseUntil) {
			leaseUntil = claimed.Call.LeaseUntil
		}
		if status == session.ToolCallCompleted {
			completedAt := now.Add(time.Millisecond)
			encoded, _ := json.Marshal(runtime.ToolOutput{ToolCallID: string(call.ID), Status: "completed", Content: string(raw), Structured: raw, InlineSize: int64(len(raw)) * 2, OriginalSize: int64(len(raw)) * 2})
			if _, err := execution.SettleToolCall(ctx, session.SettleToolCallRequest{
				Settlement: session.ToolSettlement{
					ID: call.ID, ClaimedBy: claimed.Call.ClaimedBy, ClaimToken: claimed.Call.ClaimToken, Status: session.ToolCallCompleted, Output: encoded, CompletedAt: completedAt,
					ResultMessage: session.Message{ID: call.ResultMessageID, SessionID: call.SessionID, RunID: call.RunID, ParentID: call.MessageID, Role: session.RoleTool, CreatedAt: completedAt, UpdatedAt: completedAt},
					ResultPart:    session.Part{ID: call.ResultPartID, MessageID: call.ResultMessageID, SessionID: call.SessionID, RunID: call.RunID, Kind: session.PartToolResult, Payload: encoded, CreatedAt: completedAt, UpdatedAt: completedAt},
				},
				Event: session.ToolTransitionEvent{ID: "resume-state-terminal-event", CreatedAt: completedAt},
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
