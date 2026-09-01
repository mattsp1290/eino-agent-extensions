package toolresultredactor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/config"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
)

func TestUpstreamInvalidStructuredSkipsEntireTransformWaterfall(t *testing.T) {
	ctx := context.Background()
	options := testOptions()
	canonical, policy, err := compilePolicy(options)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := ConfigHash(options)
	if err != nil {
		t.Fatal(err)
	}
	component := extension.Component{InstanceID: "invalid-structured-component", Artifact: extension.Artifact{
		Name: "invalid-structured", Version: "test", Hash: "synthetic-artifact", ConfigHash: hash, SourceKind: extension.SourceNative,
	}}
	registry, err := extension.NewRegistry[struct{}](nil, runtime.ExtensionPoints()...)
	if err != nil {
		t.Fatal(err)
	}
	var transformCalls atomic.Int32
	notices := make(chan runtime.ToolSettledNotice, 1)
	prepared, err := registry.PrepareMount(ctx, component, extension.InstallerFunc(func(_ context.Context, registrar extension.Registrar) error {
		if err := extension.OnTransform(registrar, runtime.ToolResultTransformPoint, extension.Registration{
			ID: "fixture/count-transform", Order: runtime.OrderApplication, Scope: extension.GlobalScope(),
		}, func(_ context.Context, input runtime.ToolResultTransform) (runtime.ToolResultTransform, error) {
			transformCalls.Add(1)
			return input, nil
		}); err != nil {
			return err
		}
		if err := registerPolicy(registrar, canonical, policy); err != nil {
			return err
		}
		return extension.On(registrar, runtime.ToolSettledPoint, extension.Registration{ID: "fixture/settled", Scope: extension.GlobalScope()}, func(_ context.Context, notice runtime.ToolSettledNotice) error {
			notices <- notice
			return nil
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	mount, err := registry.CommitMount(prepared, struct{}{}, []extension.Scope{extension.GlobalScope()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mount.Close(context.Background()) }()

	provider := &directPlanProvider{registry: registry, component: component}
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "invalid.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var nextRequest []byte
	streamer := internalScriptedStreamer(func(_ context.Context, request model.Request) []*einoschema.Message {
		for _, message := range request.Messages {
			if message.Role == einoschema.Tool {
				nextRequest, _ = json.Marshal(request.Messages)
				return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}
			}
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID: "invalid-call", Type: "function", Function: einoschema.FunctionCall{Name: "invalid-tool", Arguments: `{}`},
		}})}
	})
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(internalResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(provider), runtime.WithIDGenerator(&internalIDs{}),
		runtime.WithClock(time.Now), runtime.WithOwnerID("invalid-owner"),
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := model.Selection{ProviderID: "invalid-provider", ModelID: "invalid-model"}
	handle, err := orchestrator.Start(ctx, runtime.Request{
		SessionID: "invalid-session", ParentID: "invalid-user", Input: []*einoschema.Message{einoschema.UserMessage("run")},
		Config: config.Snapshot{Agent: config.Agent{Name: "agent", Model: selection}, Model: selection, Metadata: map[string]string{"workspace_root": t.TempDir()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := <-handle.Done()
	if result.Error != nil || result.Status != session.RunCompleted {
		t.Fatalf("run did not settle after invalid result: status=%s error-present=%t", result.Status, result.Error != nil)
	}
	if transformCalls.Load() != 0 {
		t.Fatalf("initial validation invoked transform waterfall: calls=%d", transformCalls.Load())
	}
	call, err := database.GetToolCall(ctx, "invalid-call")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != session.ToolCallFailed || strings.Contains(string(call.Output), "FIX_QWERTY") || strings.Contains(string(nextRequest), "FIX_QWERTY") {
		t.Fatalf("upstream invalid result was not generic: status=%s durable-marker=%t model-marker=%t", call.Status, strings.Contains(string(call.Output), "FIX_QWERTY"), strings.Contains(string(nextRequest), "FIX_QWERTY"))
	}
	select {
	case notice := <-notices:
		containsOriginal := strings.Contains(notice.Result.Output, "FIX_QWERTY") ||
			strings.Contains(string(notice.Result.Structured), "FIX_QWERTY") ||
			strings.Contains(notice.Result.Metadata["unsafe"], "FIX_QWERTY")
		if !containsOriginal {
			t.Fatalf("trusted observer did not receive original invalid result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("settled notice timed out")
	}
}

type directPlanProvider struct {
	registry  *extension.Registry[struct{}]
	component extension.Component
}

func (p *directPlanProvider) AcquireRunPlan(_ context.Context, request runtime.RunPlanRequest) (*runtime.RunPlan, error) {
	target := extension.GlobalScope()
	if request.SessionID != "" {
		target = extension.SessionScope(string(request.SessionID))
	}
	snapshot, err := p.registry.Snapshot(target)
	if err != nil {
		return nil, err
	}
	plan, err := runtime.NewRunPlan(runtime.RunPlanSpec{
		SessionID: request.SessionID, Dispatch: snapshot.Dispatch(),
		Components: []runtime.PlanComponent{{Component: p.component, Tools: []runtime.PlanTool{{
			Name: "invalid-tool", RegistrationID: "invalid-tool-registration", Scope: extension.GlobalScope(), Order: runtime.OrderApplication,
			SchemaHash: strings.Repeat("a", 64), ExecutorHash: strings.Repeat("b", 64),
			Resolve: func(context.Context, runtime.ToolScopeContext) (runtime.Tool, error) {
				return runtime.Tool{
					Name: "invalid-tool", Info: &einoschema.ToolInfo{Name: "invalid-tool"},
					Executor: directExecutorFunc(func(context.Context, runtime.ToolCall) (runtime.ToolResult, error) {
						return runtime.ToolResult{Output: "FIX_QWERTY", Structured: json.RawMessage(`{"broken":`), Metadata: map[string]string{"unsafe": "FIX_QWERTY"}}, nil
					}),
					Retention: runtime.RetentionPolicy{MaxInlineBytes: 1 << 20},
				}, nil
			},
		}}}},
	})
	if err != nil {
		snapshot.Release()
		return nil, err
	}
	return plan, nil
}

func (p *directPlanProvider) AcquireResumePlan(context.Context, runtime.ResumePlanRequest) (*runtime.RunPlan, error) {
	return nil, runtime.ErrExtensionPlanMismatch
}

type directExecutorFunc func(context.Context, runtime.ToolCall) (runtime.ToolResult, error)

func (f directExecutorFunc) Execute(ctx context.Context, call runtime.ToolCall) (runtime.ToolResult, error) {
	return f(ctx, call)
}

type internalResolver struct{ streamer model.Streamer }

func (r internalResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{Provider: model.Provider{ID: "invalid-provider"}, Model: model.Descriptor{ID: "invalid-model", ProviderID: "invalid-provider"}, Streamer: r.streamer}, nil
}

type internalScriptedStreamer func(context.Context, model.Request) []*einoschema.Message

func (s internalScriptedStreamer) StreamProvider(ctx context.Context, request model.Request) (*einoschema.StreamReader[model.StreamDelta], error) {
	messages := s(ctx, request)
	reader, writer := einoschema.Pipe[model.StreamDelta](len(messages))
	go func() {
		defer writer.Close()
		for _, message := range messages {
			if writer.Send(model.StreamDelta{Message: message}, nil) {
				return
			}
		}
	}()
	return reader, nil
}

type internalIDs struct {
	mu sync.Mutex
	n  int
}

func (i *internalIDs) next(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return prefix + "-" + string(rune('a'+i.n))
}
func (i *internalIDs) NewRunID() session.RunID           { return session.RunID(i.next("run")) }
func (i *internalIDs) NewMessageID() session.MessageID   { return session.MessageID(i.next("message")) }
func (i *internalIDs) NewPartID() session.PartID         { return session.PartID(i.next("part")) }
func (i *internalIDs) NewToolCallID() session.ToolCallID { return session.ToolCallID(i.next("call")) }
func (i *internalIDs) NewEventID() session.EventID       { return session.EventID(i.next("event")) }
func (i *internalIDs) NewEpochID() session.EpochID       { return session.EpochID(i.next("epoch")) }
