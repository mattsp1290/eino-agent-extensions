package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/config"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
)

const integrationInput = `{"question":"Which synthetic deployment should continue?","options":[{"label":"Canary","description":"Use the synthetic canary."},{"label":"Stable","description":"Use the synthetic stable route."}]}`

type freshIntegrationResult struct {
	call        session.ToolCall
	nextRequest []byte
	requests    []Request
	permissions []permissions.Request
	parts       []byte
}

func TestIntegrationSelectionDurabilityPermissionAndNextModelTurn(t *testing.T) {
	result := runFreshIntegration(t, ResponderFunc(func(_ context.Context, request Request) (Response, error) {
		return Response{Kind: ResponseSelected, SelectedOption: 2}, nil
	}), permissions.ActionAllow, nil)
	if result.call.Status != session.ToolCallCompleted {
		t.Fatalf("durable call status = %s error=%q", result.call.Status, result.call.Error)
	}
	var input toolInput
	if err := json.Unmarshal(result.call.Input, &input); err != nil {
		t.Fatal(err)
	}
	if input.Question != "Which synthetic deployment should continue?" || len(input.Options) != 2 || input.Options[1].Label != "Stable" {
		t.Fatalf("durable input = %#v", input)
	}
	var output Result
	if err := decodeDurableAskResult(result.call.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Status != StatusSelected || output.Answer != "Stable" || output.SelectedOption != 2 {
		t.Fatalf("durable output = %#v", output)
	}
	if len(result.requests) != 1 {
		t.Fatalf("responder requests = %#v", result.requests)
	}
	request := result.requests[0]
	if request.SessionID != result.call.SessionID || request.RunID != result.call.RunID || request.ToolCallID != result.call.ID || !request.AllowCustom || request.CustomLabel != CustomOptionLabel {
		t.Fatalf("responder identity = %#v durable=%#v", request, result.call)
	}
	if len(result.permissions) != 1 || result.permissions[0].Permission != PermissionAsk || result.permissions[0].Pattern != PermissionAsk {
		t.Fatalf("permission requests = %#v", result.permissions)
	}
	permissionRaw, _ := json.Marshal(result.permissions)
	if strings.Contains(string(permissionRaw), input.Question) || strings.Contains(string(permissionRaw), "Stable") {
		t.Fatalf("permission pattern leaked question content: %s", permissionRaw)
	}
	for label, raw := range map[string][]byte{"next model": result.nextRequest, "durable parts": result.parts} {
		if !strings.Contains(string(raw), "selected") || !strings.Contains(string(raw), "Stable") {
			t.Fatalf("%s does not contain selected result: %s", label, raw)
		}
	}
}

func TestIntegrationAllNormalResponderOutcomes(t *testing.T) {
	tests := map[string]struct {
		response  Response
		limits    func(*Limits)
		responder Responder
	}{
		"custom":      {response: Response{Kind: ResponseCustom, CustomAnswer: "synthetic custom route"}},
		"dismissed":   {response: Response{Kind: ResponseDismissed}},
		"unavailable": {response: Response{Kind: ResponseUnavailable}},
		"timed_out": {
			limits: func(limits *Limits) { limits.MaxWait = 10 * time.Millisecond },
			responder: ResponderFunc(func(ctx context.Context, _ Request) (Response, error) {
				<-ctx.Done()
				return Response{}, ctx.Err()
			}),
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			responder := test.responder
			if responder == nil {
				response := test.response
				responder = ResponderFunc(func(context.Context, Request) (Response, error) { return response, nil })
			}
			integration := runFreshIntegration(t, responder, permissions.ActionAllow, test.limits)
			if integration.call.Status != session.ToolCallCompleted {
				t.Fatalf("call status=%s error=%q", integration.call.Status, integration.call.Error)
			}
			var result Result
			if err := decodeDurableAskResult(integration.call.Output, &result); err != nil {
				t.Fatal(err)
			}
			if string(result.Status) != name {
				t.Fatalf("result=%#v", result)
			}
			if !strings.Contains(string(integration.nextRequest), name) {
				t.Fatalf("next model turn missing %q: %s", name, integration.nextRequest)
			}
			if name == "custom" && !strings.Contains(string(integration.nextRequest), "synthetic custom route") {
				t.Fatalf("next model turn missing custom answer: %s", integration.nextRequest)
			}
		})
	}
}

func TestIntegrationPermissionDenialAndApprovalRequiredSkipResponder(t *testing.T) {
	for name, action := range map[string]permissions.Action{
		"denied": permissions.ActionDeny, "approval_required": permissions.ActionAsk,
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			integration := runFreshIntegration(t, ResponderFunc(func(context.Context, Request) (Response, error) {
				calls.Add(1)
				return Response{Kind: ResponseSelected, SelectedOption: 1}, nil
			}), action, nil)
			if calls.Load() != 0 {
				t.Fatalf("responder calls = %d", calls.Load())
			}
			combined := string(integration.call.Output) + string(integration.nextRequest)
			if !strings.Contains(combined, name) {
				t.Fatalf("permission result missing %q: %s", name, combined)
			}
			for _, status := range []Status{StatusSelected, StatusCustom, StatusDismissed, StatusUnavailable, StatusTimedOut} {
				if strings.Contains(combined, `\"status\":\"`+string(status)+`\"`) {
					t.Fatalf("permission result became askuser status: %s", combined)
				}
			}
		})
	}
}

func TestIntegrationSanitizesResponderFailures(t *testing.T) {
	for name, responder := range map[string]Responder{
		"raw error": ResponderFunc(func(context.Context, Request) (Response, error) {
			return Response{}, errors.New("SYNTHETIC_PRIVATE_RESPONDER_ERROR")
		}),
		"wrapped canceled": ResponderFunc(func(context.Context, Request) (Response, error) {
			return Response{}, fmt.Errorf("SYNTHETIC_PRIVATE_CANCEL: %w", context.Canceled)
		}),
		"wrapped deadline": ResponderFunc(func(context.Context, Request) (Response, error) {
			return Response{}, fmt.Errorf("SYNTHETIC_PRIVATE_DEADLINE: %w", context.DeadlineExceeded)
		}),
		"invalid response": ResponderFunc(func(context.Context, Request) (Response, error) {
			return Response{Kind: ResponseSelected, SelectedOption: 99}, nil
		}),
		"panic": ResponderFunc(func(context.Context, Request) (Response, error) {
			panic("SYNTHETIC_PRIVATE_PANIC")
		}),
	} {
		t.Run(name, func(t *testing.T) {
			integration := runFreshIntegration(t, responder, permissions.ActionAllow, nil)
			if integration.call.Status != session.ToolCallFailed || integration.call.Error != errResponderOperation.Error() {
				t.Fatalf("call status=%s error=%q", integration.call.Status, integration.call.Error)
			}
			combined := string(integration.call.Output) + integration.call.Error + string(integration.nextRequest) + string(integration.parts)
			if strings.Contains(combined, "SYNTHETIC_PRIVATE") || strings.Contains(combined, "context canceled") || strings.Contains(combined, "context deadline exceeded") {
				t.Fatalf("failure leaked raw detail: %s", combined)
			}
		})
	}
}

func TestIntegrationParentInterruptionCancelsResponder(t *testing.T) {
	ctx := context.Background()
	responderEntered := make(chan struct{})
	responderCanceled := make(chan struct{})
	registry, mount := mountTestRegistry(t, Options{
		Responder: ResponderFunc(func(child context.Context, _ Request) (Response, error) {
			close(responderEntered)
			<-child.Done()
			close(responderCanceled)
			return Response{}, child.Err()
		}),
		ResponderIdentity: "integration-cancel-v1", Limits: testLimits(),
	})
	defer closeIntegrationMount(t, mount)
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	streamer := askIntegrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latestAskToolMessage(request.Messages) != nil {
			return []*einoschema.Message{einoschema.AssistantMessage("unexpected", nil)}, nil
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID: "ask-call", Type: "function", Function: einoschema.FunctionCall{Name: ToolName, Arguments: integrationInput},
		}})}, nil
	})
	orchestrator := newAskIntegrationOrchestrator(t, database, registry, streamer, allowAskPolicy(nil), "cancel-owner")
	handle, err := orchestrator.Start(ctx, integrationRuntimeRequest("cancel-session"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-responderEntered:
	case <-time.After(time.Second):
		t.Fatal("responder did not enter")
	}
	if err := handle.Interrupt(context.Background(), "synthetic interruption"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunInterrupted || !result.Interrupted {
			t.Fatalf("run result = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interrupted run did not settle")
	}
	select {
	case <-responderCanceled:
	case <-time.After(time.Second):
		t.Fatal("responder child not canceled")
	}
	call, err := database.GetToolCall(ctx, "ask-call")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != session.ToolCallInterrupted {
		t.Fatalf("interrupted call = %#v", call)
	}
	for _, status := range []Status{StatusSelected, StatusCustom, StatusDismissed, StatusUnavailable, StatusTimedOut} {
		if strings.Contains(string(call.Output), `"status":"`+string(status)+`"`) {
			t.Fatalf("interrupted call claimed normal result: %s", call.Output)
		}
	}
}

func TestIntegrationParentDeadlineIsNotPackageTimeout(t *testing.T) {
	responderEntered := make(chan struct{})
	responderCanceled := make(chan struct{})
	options := testOptions()
	options.Limits.MaxWait = time.Second
	options.Responder = ResponderFunc(func(child context.Context, _ Request) (Response, error) {
		close(responderEntered)
		<-child.Done()
		close(responderCanceled)
		return Response{}, child.Err()
	})
	options.ResponderIdentity = "integration-parent-deadline-v1"
	registry, mount := mountTestRegistry(t, options)
	defer closeIntegrationMount(t, mount)
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "parent-deadline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	streamer := askIntegrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latestAskToolMessage(request.Messages) != nil {
			return []*einoschema.Message{einoschema.AssistantMessage("unexpected", nil)}, nil
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID: "ask-call", Type: "function", Function: einoschema.FunctionCall{Name: ToolName, Arguments: integrationInput},
		}})}, nil
	})
	orchestrator := newAskIntegrationOrchestrator(t, database, registry, streamer, allowAskPolicy(nil), "deadline-owner")
	parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	handle, err := orchestrator.Start(parent, integrationRuntimeRequest("deadline-session"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-responderEntered:
	case <-time.After(time.Second):
		t.Fatal("responder did not enter")
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunInterrupted {
			t.Fatalf("deadline run result = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadline run did not settle")
	}
	select {
	case <-responderCanceled:
	case <-time.After(time.Second):
		t.Fatal("deadline did not cancel responder child")
	}
	call, err := database.GetToolCall(context.Background(), "ask-call")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != session.ToolCallInterrupted || strings.Contains(string(call.Output), string(StatusTimedOut)) || strings.Contains(string(call.Output), string(StatusUnavailable)) {
		t.Fatalf("parent deadline misclassified call: status=%s output=%s", call.Status, call.Output)
	}
}

func decodeDurableAskResult(raw json.RawMessage, result *Result) error {
	var output runtime.ToolOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return err
	}
	if len(output.Structured) == 0 {
		return errors.New("durable tool output lacks structured result")
	}
	return json.Unmarshal(output.Structured, result)
}

func runFreshIntegration(t *testing.T, responder Responder, action permissions.Action, mutateLimits func(*Limits)) freshIntegrationResult {
	t.Helper()
	options := testOptions()
	options.ResponderIdentity = "fresh-integration-responder-v1"
	if mutateLimits != nil {
		mutateLimits(&options.Limits)
	}
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ask-user.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var mu sync.Mutex
	var responderRequests []Request
	var permissionRequests []permissions.Request
	policy := permissions.PolicyFunc(func(_ context.Context, request permissions.Request) (permissions.Decision, error) {
		mu.Lock()
		permissionRequests = append(permissionRequests, request)
		mu.Unlock()
		return permissions.Decision{Action: action, Message: string(action)}, nil
	})
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Responder = ResponderFunc(func(ctx context.Context, request Request) (Response, error) {
		mu.Lock()
		responderRequests = append(responderRequests, cloneRequest(request))
		mu.Unlock()
		return responder.Respond(ctx, request)
	})
	mount, err := Mount(context.Background(), registry, testComponent("ask-user"), options)
	if err != nil {
		t.Fatal(err)
	}
	defer closeIntegrationMount(t, mount)
	var nextRequest []byte
	streamer := askIntegrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latestAskToolMessage(request.Messages) != nil {
			nextRequest, _ = json.Marshal(request.Messages)
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID: "ask-call", Type: "function", Function: einoschema.FunctionCall{Name: ToolName, Arguments: integrationInput},
		}})}, nil
	})
	orchestrator := newAskIntegrationOrchestrator(t, database, registry, streamer, policy, "fresh-owner")
	handle, err := orchestrator.Start(context.Background(), integrationRuntimeRequest("ask-session"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunCompleted || result.Error != nil {
			t.Fatalf("run result = %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fresh integration run timed out")
	}
	call, err := database.GetToolCall(context.Background(), "ask-call")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := database.ListMessages(context.Background(), "ask-session", session.ReplayCursor{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	parts, _ := json.Marshal(batch.Parts)
	mu.Lock()
	requestsCopy := append([]Request(nil), responderRequests...)
	permissionsCopy := append([]permissions.Request(nil), permissionRequests...)
	mu.Unlock()
	return freshIntegrationResult{call: call, nextRequest: nextRequest, requests: requestsCopy, permissions: permissionsCopy, parts: parts}
}

func allowAskPolicy(observed *[]permissions.Request) permissions.Policy {
	return permissions.PolicyFunc(func(_ context.Context, request permissions.Request) (permissions.Decision, error) {
		if observed != nil {
			*observed = append(*observed, request)
		}
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	})
}

func newAskIntegrationOrchestrator(t *testing.T, database *store.Store, registry *composition.Registry, streamer model.Streamer, policy permissions.Policy, owner string) *runtime.StreamingOrchestrator {
	t.Helper()
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(askIntegrationResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(policy),
		runtime.WithIDGenerator(&askIntegrationIDs{}), runtime.WithClock(time.Now),
		runtime.WithOwnerID(owner), runtime.WithQueueSize(16), runtime.WithLease(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	return orchestrator
}

func integrationRuntimeRequest(sessionID session.ID) runtime.Request {
	selection := model.Selection{ProviderID: "ask-integration-provider", ModelID: "ask-integration-model"}
	return runtime.Request{
		SessionID: sessionID, Message: runtime.UserMessage{Content: "ask a synthetic question"},
		Config: config.Snapshot{
			Agent: config.Agent{Name: "ask-integration-agent", Model: selection}, Model: selection,
			Metadata: map[string]string{"workspace_id": "ask-integration-workspace", "workspace_root": filepath.Clean(os.TempDir())},
		},
	}
}

func closeIntegrationMount(t *testing.T, mount *composition.Mount) {
	t.Helper()
	if mount == nil {
		return
	}
	mount.Deactivate()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := mount.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func latestAskToolMessage(messages []*einoschema.Message) *einoschema.Message {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == einoschema.Tool {
			return messages[index]
		}
	}
	return nil
}

type askIntegrationResolver struct{ streamer model.Streamer }

func (resolver askIntegrationResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{
		Provider: model.Provider{ID: "ask-integration-provider"},
		Model:    model.Descriptor{ID: "ask-integration-model", ProviderID: "ask-integration-provider"},
		Streamer: resolver.streamer,
	}, nil
}

type askIntegrationStreamer func(context.Context, model.Request) ([]*einoschema.Message, error)

func (streamer askIntegrationStreamer) StreamProvider(ctx context.Context, request model.Request) (*einoschema.StreamReader[model.StreamDelta], error) {
	messages, err := streamer(ctx, request)
	if err != nil {
		return nil, err
	}
	reader, writer := einoschema.Pipe[model.StreamDelta](len(messages))
	go func() {
		defer writer.Close()
		for _, message := range messages {
			if writer.Send(model.StreamDelta{Message: message, Usage: model.UsageFromMessage(message)}, nil) {
				return
			}
		}
	}()
	return reader, nil
}

type askIntegrationIDs struct {
	mu sync.Mutex
	n  int
}

func (ids *askIntegrationIDs) next(prefix string) string {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.n++
	return fmt.Sprintf("%s-%d", prefix, ids.n)
}

func (ids *askIntegrationIDs) NewRunID() session.RunID { return session.RunID(ids.next("run")) }
func (ids *askIntegrationIDs) NewMessageID() session.MessageID {
	return session.MessageID(ids.next("message"))
}
func (ids *askIntegrationIDs) NewPartID() session.PartID { return session.PartID(ids.next("part")) }
func (ids *askIntegrationIDs) NewToolCallID() session.ToolCallID {
	return session.ToolCallID(ids.next("call"))
}
func (ids *askIntegrationIDs) NewEventID() session.EventID { return session.EventID(ids.next("event")) }
func (ids *askIntegrationIDs) NewEpochID() session.EpochID { return session.EpochID(ids.next("epoch")) }
