package delegatetask

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

const integrationInput = `{"task":"Inspect the synthetic repository.","profile":"read-only"}`

type freshIntegrationResult struct {
	call        session.ToolCall
	nextRequest []byte
	requests    []Request
	permissions []permissions.Request
	parts       []byte
}

func decodeDurableResult(raw json.RawMessage, result *Result) error {
	var output runtime.ToolOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return err
	}
	if len(output.Structured) == 0 {
		return errors.New("durable tool output lacks structured result")
	}
	return json.Unmarshal(output.Structured, result)
}

func runFreshIntegration(t *testing.T, runner Runner, action permissions.Action, mutateLimits func(*Limits)) freshIntegrationResult {
	t.Helper()
	options := testOptions()
	options.RunnerIdentity = "fresh-integration-runner-v1"
	if mutateLimits != nil {
		mutateLimits(&options.Limits)
	}
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "delegate-task.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var mu sync.Mutex
	var runnerRequests []Request
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
	options.Runner = RunnerFunc(func(ctx context.Context, request Request) (Response, error) {
		mu.Lock()
		runnerRequests = append(runnerRequests, request)
		mu.Unlock()
		return runner.Run(ctx, request)
	})
	mount, err := Mount(context.Background(), registry, testComponent("delegate-task"), options)
	if err != nil {
		t.Fatal(err)
	}
	defer closeIntegrationMount(t, mount)
	var nextRequest []byte
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latestToolMessage(request.Messages) != nil {
			nextRequest, _ = json.Marshal(request.Messages)
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID: "delegate-call", Type: "function", Function: einoschema.FunctionCall{Name: ToolName, Arguments: integrationInput},
		}})}, nil
	})
	orchestrator := newIntegrationOrchestrator(t, database, registry, streamer, policy, "fresh-owner")
	handle, err := orchestrator.Start(context.Background(), integrationRuntimeRequest("delegate-session"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunCompleted || result.Error != nil {
			t.Fatalf("run result=%#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("integration run timed out")
	}
	call, err := database.GetToolCall(context.Background(), "delegate-call")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := database.ListMessages(context.Background(), "delegate-session", session.ReplayCursor{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	parts, _ := json.Marshal(batch.Parts)
	mu.Lock()
	requestsCopy := append([]Request(nil), runnerRequests...)
	permissionsCopy := append([]permissions.Request(nil), permissionRequests...)
	mu.Unlock()
	return freshIntegrationResult{call: call, nextRequest: nextRequest, requests: requestsCopy, permissions: permissionsCopy, parts: parts}
}

func allowPolicy() permissions.Policy {
	return permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	})
}

func newIntegrationOrchestrator(t *testing.T, database *store.Store, registry *composition.Registry, streamer model.Streamer, policy permissions.Policy, owner string) *runtime.StreamingOrchestrator {
	t.Helper()
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(integrationResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(policy),
		runtime.WithIDGenerator(&integrationIDs{}), runtime.WithClock(time.Now),
		runtime.WithOwnerID(owner), runtime.WithQueueSize(16), runtime.WithLease(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	return orchestrator
}

func integrationRuntimeRequest(sessionID session.ID) runtime.Request {
	selection := model.Selection{ProviderID: "delegate-integration-provider", ModelID: "delegate-integration-model"}
	return runtime.Request{
		SessionID: sessionID, Message: runtime.UserMessage{Content: "delegate a synthetic task"},
		Config: config.Snapshot{
			Agent: config.Agent{Name: "delegate-integration-agent", Model: selection}, Model: selection,
			Metadata: map[string]string{"workspace_id": "delegate-integration-workspace", "workspace_root": filepath.Clean(os.TempDir())},
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

func latestToolMessage(messages []*einoschema.Message) *einoschema.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == einoschema.Tool {
			return messages[i]
		}
	}
	return nil
}

type integrationResolver struct{ streamer model.Streamer }

func (r integrationResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{
		Provider: model.Provider{ID: "delegate-integration-provider"},
		Model:    model.Descriptor{ID: "delegate-integration-model", ProviderID: "delegate-integration-provider"},
		Streamer: r.streamer,
	}, nil
}

type integrationStreamer func(context.Context, model.Request) ([]*einoschema.Message, error)

func (s integrationStreamer) StreamProvider(ctx context.Context, request model.Request) (*einoschema.StreamReader[model.StreamDelta], error) {
	messages, err := s(ctx, request)
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

type integrationIDs struct {
	mu sync.Mutex
	n  int
}

func (ids *integrationIDs) next(prefix string) string {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.n++
	return fmt.Sprintf("%s-%d", prefix, ids.n)
}
func (ids *integrationIDs) NewRunID() session.RunID { return session.RunID(ids.next("run")) }
func (ids *integrationIDs) NewMessageID() session.MessageID {
	return session.MessageID(ids.next("message"))
}
func (ids *integrationIDs) NewPartID() session.PartID { return session.PartID(ids.next("part")) }
func (ids *integrationIDs) NewToolCallID() session.ToolCallID {
	return session.ToolCallID(ids.next("call"))
}
func (ids *integrationIDs) NewEventID() session.EventID { return session.EventID(ids.next("event")) }
func (ids *integrationIDs) NewEpochID() session.EpochID { return session.EpochID(ids.next("epoch")) }
