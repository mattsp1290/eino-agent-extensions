package delegatetask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
)

func TestIntegrationDurabilityPermissionWorkspaceAndNextTurn(t *testing.T) {
	integration := runFreshIntegration(t, RunnerFunc(func(_ context.Context, request Request) (Response, error) {
		return Response{Status: ResponseCompleted, Output: "synthetic result"}, nil
	}), permissions.ActionAllow, nil)
	if integration.call.Status != session.ToolCallCompleted || string(integration.call.Input) != `{"profile":"read-only","task":"Inspect the synthetic repository."}` {
		t.Fatalf("call=%#v", integration.call)
	}
	var output Result
	if err := decodeDurableResult(integration.call.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output != (Result{Status: StatusCompleted, Output: "synthetic result"}) {
		t.Fatalf("output=%#v", output)
	}
	if len(integration.requests) != 1 {
		t.Fatalf("requests=%#v", integration.requests)
	}
	request := integration.requests[0]
	if request.SessionID != integration.call.SessionID || request.RunID != integration.call.RunID || request.ToolCallID != integration.call.ID || request.Task != "Inspect the synthetic repository." || request.Profile != "read-only" || request.WorkspaceID != "delegate-integration-workspace" || request.WorkspaceRoot == "" {
		t.Fatalf("request=%#v call=%#v", request, integration.call)
	}
	if len(integration.permissions) != 1 || integration.permissions[0].Permission != PermissionDelegate || integration.permissions[0].Pattern != "delegate-profile:read-only" {
		t.Fatalf("permissions=%#v", integration.permissions)
	}
	permissionText := fmt.Sprintf("%#v", integration.permissions)
	if strings.Contains(permissionText, request.Task) || strings.Contains(permissionText, request.WorkspaceRoot) {
		t.Fatalf("permission leaked task/workspace: %s", permissionText)
	}
	for label, raw := range map[string][]byte{"next model": integration.nextRequest, "durable parts": integration.parts} {
		if !strings.Contains(string(raw), "completed") || !strings.Contains(string(raw), "synthetic result") {
			t.Fatalf("%s missing result: %s", label, raw)
		}
	}
}

func TestIntegrationAllNormalOutcomes(t *testing.T) {
	for response, want := range map[ResponseStatus]Status{
		ResponseCompleted: StatusCompleted, ResponseFailed: StatusFailed,
		ResponseRejected: StatusRejected, ResponseUnavailable: StatusUnavailable,
	} {
		t.Run(string(response), func(t *testing.T) {
			integration := runFreshIntegration(t, RunnerFunc(func(context.Context, Request) (Response, error) {
				return Response{Status: response, Output: "bounded"}, nil
			}), permissions.ActionAllow, nil)
			var result Result
			if err := decodeDurableResult(integration.call.Output, &result); err != nil {
				t.Fatal(err)
			}
			if integration.call.Status != session.ToolCallCompleted || result.Status != want || result.Output != "bounded" || !strings.Contains(string(integration.nextRequest), string(want)) {
				t.Fatalf("call=%#v result=%#v next=%s", integration.call, result, integration.nextRequest)
			}
		})
	}
	t.Run("timed_out", func(t *testing.T) {
		integration := runFreshIntegration(t, RunnerFunc(func(ctx context.Context, _ Request) (Response, error) {
			<-ctx.Done()
			return Response{}, ctx.Err()
		}), permissions.ActionAllow, func(l *Limits) { l.MaxWait = 10 * time.Millisecond })
		var result Result
		if err := decodeDurableResult(integration.call.Output, &result); err != nil {
			t.Fatal(err)
		}
		if integration.call.Status != session.ToolCallCompleted || result.Status != StatusTimedOut {
			t.Fatalf("call=%#v result=%#v", integration.call, result)
		}
	})
}

func TestIntegrationProfileReachesRunnerForHostRejection(t *testing.T) {
	// Syntactic profile validity is the package boundary; existence and policy
	// remain exclusively host-owned.
	var observedProfile string
	integration := runFreshIntegration(t, RunnerFunc(func(_ context.Context, request Request) (Response, error) {
		observedProfile = request.Profile
		return Response{Status: ResponseRejected, Output: "unknown profile"}, nil
	}), permissions.ActionAllow, nil)
	if observedProfile != "read-only" || integration.call.Status != session.ToolCallCompleted {
		t.Fatalf("profile=%q call=%#v", observedProfile, integration.call)
	}
	var result Result
	if err := decodeDurableResult(integration.call.Output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusRejected {
		t.Fatalf("result=%#v", result)
	}
}

func TestIntegrationPermissionDenyAndApprovalSkipRunner(t *testing.T) {
	for name, action := range map[string]permissions.Action{"denied": permissions.ActionDeny, "approval_required": permissions.ActionAsk} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			integration := runFreshIntegration(t, RunnerFunc(func(context.Context, Request) (Response, error) {
				calls.Add(1)
				return Response{Status: ResponseCompleted}, nil
			}), action, nil)
			if calls.Load() != 0 {
				t.Fatalf("runner calls=%d", calls.Load())
			}
			combined := string(integration.call.Output) + string(integration.nextRequest)
			if !strings.Contains(combined, name) {
				t.Fatalf("missing %q: %s", name, combined)
			}
			for _, status := range []Status{StatusCompleted, StatusFailed, StatusRejected, StatusUnavailable, StatusTimedOut} {
				if strings.Contains(combined, `"status":"`+string(status)+`"`) {
					t.Fatalf("permission settlement became delegated result: %s", combined)
				}
			}
		})
	}
}

func TestIntegrationSanitizesRunnerFailures(t *testing.T) {
	private := "SYNTHETIC_PRIVATE_RUNNER_DETAIL"
	for name, runner := range map[string]Runner{
		"error": RunnerFunc(func(context.Context, Request) (Response, error) { return Response{}, errors.New(private) }),
		"wrapped deadline": RunnerFunc(func(context.Context, Request) (Response, error) {
			return Response{}, fmt.Errorf("%s: %w", private, context.DeadlineExceeded)
		}),
		"invalid": RunnerFunc(func(context.Context, Request) (Response, error) {
			return Response{Status: "invalid", Output: private}, nil
		}),
		"panic": RunnerFunc(func(context.Context, Request) (Response, error) { panic(private) }),
	} {
		t.Run(name, func(t *testing.T) {
			integration := runFreshIntegration(t, runner, permissions.ActionAllow, nil)
			if integration.call.Status != session.ToolCallFailed || integration.call.Error != errRunnerOperation.Error() {
				t.Fatalf("call=%#v", integration.call)
			}
			combined := string(integration.call.Output) + integration.call.Error + string(integration.nextRequest) + string(integration.parts)
			if strings.Contains(combined, private) || strings.Contains(combined, "context deadline exceeded") {
				t.Fatalf("private detail leaked: %s", combined)
			}
		})
	}
}

func TestIntegrationParentDeadlineInterruptsWithoutNormalResult(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	options := testOptions()
	options.Limits.MaxWait = time.Second
	options.RunnerIdentity = "parent-deadline-v1"
	options.Runner = RunnerFunc(func(ctx context.Context, _ Request) (Response, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return Response{}, ctx.Err()
	})
	registry, mount := mountTestRegistry(t, options)
	defer closeIntegrationMount(t, mount)
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "parent-deadline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latestToolMessage(request.Messages) != nil {
			return []*einoschema.Message{einoschema.AssistantMessage("unexpected", nil)}, nil
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{ID: "delegate-call", Type: "function", Function: einoschema.FunctionCall{Name: ToolName, Arguments: integrationInput}}})}, nil
	})
	orchestrator := newIntegrationOrchestrator(t, database, registry, streamer, allowPolicy(), "deadline-owner")
	parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	handle, err := orchestrator.Start(parent, integrationRuntimeRequest("deadline-session"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("runner did not enter")
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunInterrupted {
			t.Fatalf("run=%#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadline run did not settle")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("runner did not observe cancellation")
	}
	call, err := database.GetToolCall(context.Background(), "delegate-call")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != session.ToolCallInterrupted || strings.Contains(string(call.Output), string(StatusTimedOut)) {
		t.Fatalf("call=%#v", call)
	}
}

func TestIntegrationExactLimitWorstCaseResultIsComplete(t *testing.T) {
	const limit = 257
	worstCase := strings.Repeat("\x01", limit)
	integration := runFreshIntegration(t, RunnerFunc(func(context.Context, Request) (Response, error) {
		return Response{Status: ResponseCompleted, Output: worstCase}, nil
	}), permissions.ActionAllow, func(l *Limits) { l.MaxResultBytes = limit })
	if integration.call.Status != session.ToolCallCompleted {
		t.Fatalf("call=%#v", integration.call)
	}
	var durable runtime.ToolOutput
	if err := json.Unmarshal(integration.call.Output, &durable); err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := json.Unmarshal(durable.Structured, &result); err != nil {
		t.Fatal(err)
	}
	if result.Output != worstCase || durable.Content != string(durable.Structured) || durable.Truncated || durable.External || durable.Redacted {
		t.Fatalf("durable=%#v result-bytes=%d", durable, len(result.Output))
	}
	if !strings.Contains(string(integration.nextRequest), `\u0001`) {
		t.Fatalf("next request lacks complete escaped result: %s", integration.nextRequest)
	}

	over := runFreshIntegration(t, RunnerFunc(func(context.Context, Request) (Response, error) {
		return Response{Status: ResponseCompleted, Output: strings.Repeat("x", limit+1)}, nil
	}), permissions.ActionAllow, func(l *Limits) { l.MaxResultBytes = limit })
	if over.call.Status != session.ToolCallFailed || over.call.Error != errRunnerOperation.Error() {
		t.Fatalf("over-limit call=%#v", over.call)
	}
}

func TestIntegrationRejectedInputCreatesNoDurableToolCall(t *testing.T) {
	for name, arguments := range map[string]string{
		"invalid syntax": `{"task":`,
		"duplicate":      `{"task":"a","task":"b","profile":"read-only"}`,
		"unknown":        `{"task":"a","profile":"read-only","extra":true}`,
		"trailing":       `{"task":"a","profile":"read-only"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			options := testOptions()
			options.Runner = RunnerFunc(func(context.Context, Request) (Response, error) {
				calls.Add(1)
				return Response{Status: ResponseCompleted}, nil
			})
			registry, mount := mountTestRegistry(t, options)
			defer closeIntegrationMount(t, mount)
			database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "invalid-input.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			streamer := integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
				return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{ID: "invalid-call", Type: "function", Function: einoschema.FunctionCall{Name: ToolName, Arguments: arguments}}})}, nil
			})
			orchestrator := newIntegrationOrchestrator(t, database, registry, streamer, allowPolicy(), "invalid-owner")
			handle, err := orchestrator.Start(context.Background(), integrationRuntimeRequest("invalid-session"))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-handle.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("invalid-input run did not settle")
			}
			if _, err := database.GetToolCall(context.Background(), "invalid-call"); !errors.Is(err, session.ErrNotFound) {
				t.Fatalf("durable tool call exists or lookup error=%v", err)
			}
			batch, err := database.ListMessages(context.Background(), "invalid-session", session.ReplayCursor{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, message := range batch.Messages {
				if message.Role == session.RoleTool {
					t.Fatalf("unexpected durable tool-result message=%#v", message)
				}
			}
			for _, part := range batch.Parts {
				if part.Kind == session.PartToolResult {
					t.Fatalf("unexpected durable tool-result part=%#v", part)
				}
			}
			if calls.Load() != 0 {
				t.Fatalf("runner calls=%d", calls.Load())
			}
		})
	}
}

func TestIntegrationSurrogateRepairIsDurableAndReachesRunner(t *testing.T) {
	const repairedInput = `{"task":"repair \ud800 marker","profile":"read-only"}`
	var observed Request
	options := testOptions()
	options.Runner = RunnerFunc(func(_ context.Context, request Request) (Response, error) {
		observed = request
		return Response{Status: ResponseCompleted}, nil
	})
	registry, mount := mountTestRegistry(t, options)
	defer closeIntegrationMount(t, mount)
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "unicode-repair.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latestToolMessage(request.Messages) != nil {
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{ID: "unicode-call", Type: "function", Function: einoschema.FunctionCall{Name: ToolName, Arguments: repairedInput}}})}, nil
	})
	orchestrator := newIntegrationOrchestrator(t, database, registry, streamer, allowPolicy(), "unicode-owner")
	handle, err := orchestrator.Start(context.Background(), integrationRuntimeRequest("unicode-session"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunCompleted {
			t.Fatalf("run=%#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unicode run did not settle")
	}
	call, err := database.GetToolCall(context.Background(), "unicode-call")
	if err != nil {
		t.Fatal(err)
	}
	if observed.Task != "repair � marker" || !strings.Contains(string(call.Input), "�") {
		t.Fatalf("observed=%#v durable=%s", observed, call.Input)
	}
}
