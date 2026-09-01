package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

type freshIntegrationResult struct {
	call        session.ToolCall
	nextRequest []byte
	requests    []Request
	permissions []permissions.Request
	parts       []byte
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
