package commandguard

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
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

type integrationFixture struct {
	notices                     chan runtime.ToolSettledNotice
	t                           *testing.T
	registry                    *composition.Registry
	database                    *store.Store
	workspace                   string
	executions, permissionCalls atomic.Int32
	action                      permissions.Action
}

func newIntegrationFixture(t *testing.T) *integrationFixture {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &integrationFixture{t: t, registry: testRegistry(t), database: db, workspace: workspace, action: permissions.ActionAllow, notices: make(chan runtime.ToolSettledNotice, 32)}
	f.mount("settled-observer", func(_ context.Context, r *composition.Registrar) error {
		return extension.On(r.Extensions(), runtime.ToolSettledPoint, extension.Registration{ID: "observe", Scope: extension.GlobalScope()}, func(_ context.Context, n runtime.ToolSettledNotice) error { f.notices <- n; return nil })
	})
	return f
}
func fixtureComponent(name string) extension.Component {
	c := testComponent("")
	c.InstanceID = name
	c.Artifact.Name = name
	c.Artifact.ConfigHash = "fixture-config"
	return c
}
func (f *integrationFixture) mount(name string, install composition.InstallerFunc) *composition.Mount {
	f.t.Helper()
	m, err := f.registry.Mount(context.Background(), fixtureComponent(name), install)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		m.Deactivate()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			f.t.Error(err)
		}
	})
	return m
}

type commandInput struct {
	Script string `json:"script"`
}

func (f *integrationFixture) tool() {
	reflector := jsonschema.Reflector{Anonymous: true, DoNotReference: true}
	f.mount("fixture-tool", func(_ context.Context, r *composition.Registrar) error {
		return r.Tool(composition.ToolRegistration{ID: "fixture-tool", Scope: extension.GlobalScope(), Definition: tools.Definition{
			Name: "fixture-command", Description: "Harmless typed host command fixture", Parameters: einoschema.NewParamsOneOfByJSONSchema(reflector.Reflect(commandInput{})),
			Execute: tools.TypedExecutor(func(_ context.Context, _ tools.TypedExecution[commandInput]) (map[string]string, error) {
				f.executions.Add(1)
				return map[string]string{"outcome": "executed"}, nil
			}),
			Permissions: []string{"fixture:execute"}, Retention: runtime.RetentionPolicy{MaxInlineBytes: 1 << 16},
		}})
	})
}
func (f *integrationFixture) guard(o Options) *composition.Mount {
	return testMount(f.t, f.registry, testComponent(""), o)
}
func integrationOptions() Options {
	o := testOptions()
	o.Bindings = append(DefaultBindings(), Binding{"fixture-command", "script", DialectBash})
	return o
}
func (f *integrationFixture) orchestrator(streamer model.Streamer) *runtime.StreamingOrchestrator {
	f.t.Helper()
	o, err := runtime.NewStreamingOrchestrator(runtime.WithStore(f.database), runtime.WithModelResolver(fixtureResolver{streamer}), runtime.WithRunPlanProvider(f.registry), runtime.WithIDGenerator(&fixtureIDs{}), runtime.WithOwnerID("guard-fixture"), runtime.WithQueueSize(32), runtime.WithClock(time.Now), runtime.WithPermissions(permissions.PolicyFunc(func(ctx context.Context, r permissions.Request) (permissions.Decision, error) {
		f.permissionCalls.Add(1)
		call, err := f.database.GetToolCall(ctx, session.ToolCallID(r.ToolCallID))
		if err != nil {
			return permissions.Decision{}, err
		}
		if call.Status != session.ToolCallRunning {
			return permissions.Decision{}, fmt.Errorf("permission before durable claim")
		}
		return permissions.Decision{Action: f.action, Message: "fixture permission"}, nil
	})))
	if err != nil {
		f.t.Fatal(err)
	}
	return o
}
func (f *integrationFixture) request() runtime.Request {
	selection := model.Selection{ProviderID: "fixture-provider", ModelID: "fixture-model"}
	return runtime.Request{SessionID: "guard-session", Message: runtime.UserMessage{Content: "run harmless fixture commands"}, Config: config.Snapshot{Agent: config.Agent{Name: "fixture-agent", Model: selection}, Model: selection, Metadata: map[string]string{"workspace_id": "fixture-workspace", "workspace_root": f.workspace}}}
}
func waitRun(t *testing.T, h runtime.Handle) {
	t.Helper()
	select {
	case done := <-h.Done():
		if done.Status != session.RunCompleted || done.Error != nil {
			t.Fatalf("run failed: %s %v", done.Status, done.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run timed out")
	}
}
func (f *integrationFixture) run(calls ...einoschema.ToolCall) []byte {
	f.t.Helper()
	var next []byte
	turn := 0
	streamer := scriptedStreamer(func(_ context.Context, r model.Request) ([]*einoschema.Message, error) {
		if turn < len(calls) {
			call := calls[turn]
			turn++
			return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{call})}, nil
		}
		next, _ = json.Marshal(r.Messages)
		return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
	})
	h, err := f.orchestrator(streamer).Start(context.Background(), f.request())
	if err != nil {
		f.t.Fatal(err)
	}
	waitRun(f.t, h)
	return next
}
func toolCall(id, name, field, script string) einoschema.ToolCall {
	raw, _ := json.Marshal(map[string]string{field: script})
	return einoschema.ToolCall{ID: id, Type: "function", Function: einoschema.FunctionCall{Name: name, Arguments: string(raw)}}
}
func (f *integrationFixture) call(id string) session.ToolCall {
	f.t.Helper()
	c, err := f.database.GetToolCall(context.Background(), session.ToolCallID(id))
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}
func (f *integrationFixture) durable() ([]byte, []session.EventRecord) {
	f.t.Helper()
	msgs, err := f.database.ListMessages(context.Background(), "guard-session", session.ReplayCursor{Limit: 100})
	if err != nil {
		f.t.Fatal(err)
	}
	events, err := f.database.ListEvents(context.Background(), "guard-session", session.EventCursor{Limit: 100})
	if err != nil {
		f.t.Fatal(err)
	}
	raw, _ := json.Marshal(msgs.Parts)
	return raw, events.Events
}

type fixtureResolver struct{ streamer model.Streamer }

func (r fixtureResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{Provider: model.Provider{ID: "fixture-provider"}, Model: model.Descriptor{ID: "fixture-model", ProviderID: "fixture-provider"}, Streamer: r.streamer}, nil
}

type scriptedStreamer func(context.Context, model.Request) ([]*einoschema.Message, error)

func (s scriptedStreamer) StreamProvider(ctx context.Context, r model.Request) (*einoschema.StreamReader[model.StreamDelta], error) {
	messages, err := s(ctx, r)
	if err != nil {
		return nil, err
	}
	reader, writer := einoschema.Pipe[model.StreamDelta](len(messages))
	for _, m := range messages {
		writer.Send(model.StreamDelta{Message: m, Usage: model.UsageFromMessage(m)}, nil)
	}
	writer.Close()
	return reader, nil
}

type fixtureIDs struct {
	mu sync.Mutex
	n  int
}

func (i *fixtureIDs) next(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("%s-%d", prefix, i.n)
}
func (i *fixtureIDs) NewRunID() session.RunID           { return session.RunID(i.next("run")) }
func (i *fixtureIDs) NewMessageID() session.MessageID   { return session.MessageID(i.next("message")) }
func (i *fixtureIDs) NewPartID() session.PartID         { return session.PartID(i.next("part")) }
func (i *fixtureIDs) NewToolCallID() session.ToolCallID { return session.ToolCallID(i.next("call")) }
func (i *fixtureIDs) NewEventID() session.EventID       { return session.EventID(i.next("event")) }
func (i *fixtureIDs) NewEpochID() session.EpochID       { return session.EpochID(i.next("epoch")) }
