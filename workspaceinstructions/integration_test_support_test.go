package workspaceinstructions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/config"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
	"github.com/mattsp1290/eino-agent/tools"
)

const integrationBasePrompt = "base synthetic system prompt"

type integrationHarness struct {
	t        *testing.T
	database *store.Store
	registry *composition.Registry
	mounts   []*composition.Mount
	ids      *integrationIDs
}

func newIntegrationHarness(t *testing.T) *integrationHarness {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "workspace-instructions.db"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	harness := &integrationHarness{t: t, database: database, registry: registry, ids: &integrationIDs{}}
	t.Cleanup(func() {
		for index := len(harness.mounts) - 1; index >= 0; index-- {
			closeTestMount(t, harness.mounts[index])
		}
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	return harness
}

func (h *integrationHarness) mountInstructions(instance string, options Options) *composition.Mount {
	h.t.Helper()
	mount, err := Mount(context.Background(), h.registry, testComponent(instance), options)
	if err != nil {
		h.t.Fatal(err)
	}
	h.mounts = append(h.mounts, mount)
	return mount
}

func (h *integrationHarness) mountTouch(file, content string) {
	h.t.Helper()
	mount := mountIntegrationTouch(h.t, h.registry, file, content)
	h.mounts = append(h.mounts, mount)
}

func mountIntegrationTouch(t *testing.T, registry *composition.Registry, file, content string) *composition.Mount {
	t.Helper()
	component := extension.Component{InstanceID: "instructions-touch", Artifact: extension.Artifact{
		Name: "instructions-touch", Version: "test", Hash: "touch-artifact", ConfigHash: "touch-config", SourceKind: extension.SourceNative,
	}}
	mount, err := registry.Mount(context.Background(), component, composition.InstallerFunc(func(_ context.Context, registrar *composition.Registrar) error {
		return registrar.Tool(composition.ToolRegistration{
			ID: "instructions/touch", Scope: extension.GlobalScope(),
			Definition: tools.Definition{
				Name: "instructions_touch", Description: "Rewrite the synthetic root instructions.",
				Retention: runtime.RetentionPolicy{MaxInlineBytes: 1024},
				Execute: func(context.Context, tools.Execution) (json.RawMessage, error) {
					if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
						return nil, err
					}
					return json.RawMessage(`{"ok":true}`), nil
				},
			},
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	return mount
}

func (h *integrationHarness) run(sessionID session.ID, streamer integrationStreamer) runtime.Result {
	h.t.Helper()
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(h.database), runtime.WithModelResolver(integrationModelResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(h.registry), runtime.WithPermissions(permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
			return permissions.Decision{Action: permissions.ActionAllow}, nil
		})), runtime.WithIDGenerator(h.ids), runtime.WithClock(time.Now), runtime.WithOwnerID("workspace-integration-owner"),
		runtime.WithQueueSize(16), runtime.WithLease(time.Second),
	)
	if err != nil {
		h.t.Fatal(err)
	}
	selection := model.Selection{ProviderID: "workspace-integration-provider", ModelID: "workspace-integration-model"}
	handle, err := orchestrator.Start(context.Background(), runtime.Request{
		SessionID: sessionID, Message: runtime.UserMessage{Content: "read synthetic instructions"},
		Config: config.Snapshot{
			Agent: config.Agent{Name: "workspace-integration-agent", SystemPrompt: integrationBasePrompt, Model: selection},
			Model: selection, Metadata: map[string]string{"workspace_id": "metadata-only", "workspace_root": filepath.Clean(os.TempDir())},
		},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	select {
	case result := <-handle.Done():
		return result
	case <-time.After(3 * time.Second):
		h.t.Fatal("integration run timed out")
		return runtime.Result{}
	}
}

type integrationModelResolver struct{ streamer model.Streamer }

func (resolver integrationModelResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{
		Provider: model.Provider{ID: "workspace-integration-provider"},
		Model:    model.Descriptor{ID: "workspace-integration-model", ProviderID: "workspace-integration-provider"},
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

func successfulStreamer(systems *[]string) integrationStreamer {
	return func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		*systems = append(*systems, request.System)
		return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
	}
}
