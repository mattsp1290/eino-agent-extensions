package websearch

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

const integrationInput = `{"query":"  bounded query  "}`

type integrationResult struct {
	call        session.ToolCall
	nextRequest []byte
	queries     []string
	permissions []permissions.Request
	parts       []byte
	events      []byte
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

func runFreshIntegration(t *testing.T, searcher Searcher, action permissions.Action, mutateLimits func(*Limits)) integrationResult {
	t.Helper()
	options := testOptions()
	options.SearcherIdentity = "fresh-integration-searcher-v1"
	if mutateLimits != nil {
		mutateLimits(&options.Limits)
	}
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "web-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var mu sync.Mutex
	var queries []string
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
	options.Searcher = SearcherFunc(func(ctx context.Context, query string) ([]Source, error) {
		mu.Lock()
		queries = append(queries, query)
		mu.Unlock()
		return searcher.Search(ctx, query)
	})
	mount, err := Mount(context.Background(), registry, testComponent("integration"), options)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestMount(t, mount)
	var nextRequest []byte
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latestToolMessage(request.Messages) != nil {
			nextRequest, _ = json.Marshal(request.Messages)
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID: "web-search-call", Type: "function", Function: einoschema.FunctionCall{Name: ToolName, Arguments: integrationInput},
		}})}, nil
	})
	orchestrator := newIntegrationOrchestrator(t, database, registry, streamer, policy, "fresh-owner")
	handle, err := orchestrator.Start(context.Background(), integrationRuntimeRequest("web-search-session"))
	if err != nil {
		t.Fatal(err)
	}
	waitIntegration(t, handle)
	call, err := database.GetToolCall(context.Background(), "web-search-call")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := database.ListMessages(context.Background(), "web-search-session", session.ReplayCursor{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	parts, _ := json.Marshal(batch.Parts)
	eventBatch, err := database.ListEvents(context.Background(), "web-search-session", session.EventCursor{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	events, _ := json.Marshal(eventBatch.Events)
	mu.Lock()
	queriesCopy := append([]string(nil), queries...)
	permissionsCopy := append([]permissions.Request(nil), permissionRequests...)
	mu.Unlock()
	return integrationResult{call: call, nextRequest: nextRequest, queries: queriesCopy, permissions: permissionsCopy, parts: parts, events: events}
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
	selection := model.Selection{ProviderID: "web-search-integration-provider", ModelID: "web-search-integration-model"}
	return runtime.Request{
		SessionID: sessionID, Message: runtime.UserMessage{Content: "search synthetic records"},
		Config: config.Snapshot{
			Agent: config.Agent{Name: "web-search-integration-agent", Model: selection}, Model: selection,
			Metadata: map[string]string{"workspace_id": "web-search-integration-workspace", "workspace_root": filepath.Clean(os.TempDir())},
		},
	}
}

func waitIntegration(t *testing.T, handle runtime.Handle) runtime.Result {
	t.Helper()
	select {
	case result := <-handle.Done():
		if result.Error != nil {
			t.Fatalf("run result=%#v", result)
		}
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("integration run timed out")
		return runtime.Result{}
	}
}

func latestToolMessage(messages []*einoschema.Message) *einoschema.Message {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == einoschema.Tool {
			return messages[index]
		}
	}
	return nil
}

type integrationResolver struct{ streamer model.Streamer }

func (resolver integrationResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{
		Provider: model.Provider{ID: "web-search-integration-provider"},
		Model:    model.Descriptor{ID: "web-search-integration-model", ProviderID: "web-search-integration-provider"},
		Streamer: resolver.streamer,
	}, nil
}

type integrationStreamer func(context.Context, model.Request) ([]*einoschema.Message, error)

func (streamer integrationStreamer) StreamProvider(ctx context.Context, request model.Request) (*einoschema.StreamReader[model.StreamDelta], error) {
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
