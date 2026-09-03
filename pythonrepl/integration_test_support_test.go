//go:build linux || darwin

package pythonrepl

import (
	"context"
	"encoding/json"
	"fmt"
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

func mountIntegrationRegistry(t *testing.T, options Options) (*composition.Registry, *composition.Mount) {
	t.Helper()
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	mount, err := Mount(context.Background(), registry, testComponent("python-repl"), options)
	if err != nil {
		t.Fatal(err)
	}
	return registry, mount
}

func closeIntegrationMount(t *testing.T, mount *composition.Mount) {
	t.Helper()
	mount.Deactivate()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mount.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func integrationSnapshot(workspace, workspaceID string) config.Snapshot {
	selection := model.Selection{ProviderID: "python-integration-provider", ModelID: "python-integration-model"}
	return config.Snapshot{
		Agent: config.Agent{Name: "python-integration-agent", Model: selection}, Model: selection,
		Metadata: map[string]string{"workspace_id": workspaceID, "workspace_root": workspace},
	}
}

func integrationOrchestrator(t *testing.T, database *store.Store, registry *composition.Registry, streamer model.Streamer, policy permissions.Policy, owner string) *runtime.StreamingOrchestrator {
	return integrationOrchestratorWithIDs(t, database, registry, streamer, policy, owner, &integrationIDs{})
}

func integrationOrchestratorWithIDs(t *testing.T, database *store.Store, registry *composition.Registry, streamer model.Streamer, policy permissions.Policy, owner string, ids *integrationIDs) *runtime.StreamingOrchestrator {
	t.Helper()
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(integrationResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(policy),
		runtime.WithIDGenerator(ids), runtime.WithClock(time.Now),
		runtime.WithOwnerID(owner), runtime.WithQueueSize(16), runtime.WithLease(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	return orchestrator
}

func runIntegrationRequest(t *testing.T, orchestrator *runtime.StreamingOrchestrator, sessionID session.ID, message string, snapshot config.Snapshot) {
	t.Helper()
	handle, err := orchestrator.Start(context.Background(), runtime.Request{SessionID: sessionID, Message: runtime.UserMessage{Content: message}, Config: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunCompleted || result.Error != nil {
			t.Fatalf("run result = %#v", result)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("integration run timed out")
	}
}

func openIntegrationStore(t *testing.T, name string) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func latestToolMessage(messages []*einoschema.Message) *einoschema.Message {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == einoschema.Tool {
			return messages[index]
		}
	}
	return nil
}

func toolCallMessage(id, name, arguments string) []*einoschema.Message {
	return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
		ID: id, Type: "function", Function: einoschema.FunctionCall{Name: name, Arguments: arguments},
	}})}
}

func decodeIntegrationResult(raw string, destination any) error {
	var envelope struct {
		Structured json.RawMessage `json:"structured"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return err
	}
	return json.Unmarshal(envelope.Structured, destination)
}

type integrationResolver struct{ streamer model.Streamer }

func (resolver integrationResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{
		Provider: model.Provider{ID: "python-integration-provider"},
		Model:    model.Descriptor{ID: "python-integration-model", ProviderID: "python-integration-provider"},
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
