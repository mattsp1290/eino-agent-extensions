//go:build linux || darwin

package backgroundjobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
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
)

func TestIntegrationStartPollDurabilityPermissionsAndFreshManager(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	registry, mount := mountIntegrationRegistry(t, testOptions())
	plan, err := registry.AcquireRunPlan(ctx, runtime.RunPlanRequest{SessionID: "integration-session"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	plan.Release()

	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "background-jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var requestsMu sync.Mutex
	var permissionRequests []permissions.Request
	policy := permissions.PolicyFunc(func(_ context.Context, request permissions.Request) (permissions.Decision, error) {
		requestsMu.Lock()
		permissionRequests = append(permissionRequests, request)
		requestsMu.Unlock()
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	})

	phase := 0
	var launched StartResult
	var terminal StatusResult
	var startContent string
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		latest := latestToolMessage(request.Messages)
		switch phase {
		case 0:
			if latest != nil && latest.ToolCallID == "integration-start-call" {
				startContent = latest.Content
				if err := decodeIntegrationResult(latest.Content, &launched); err != nil {
					return nil, err
				}
				phase = 1
				return []*einoschema.Message{einoschema.AssistantMessage("background job launched", nil)}, nil
			}
			return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
				ID: "integration-start-call", Type: "function",
				Function: einoschema.FunctionCall{Name: StartToolName, Arguments: `{"command":"printf \"$SAFE\"; i=0; while [ $i -lt 1021 ]; do printf x; i=$((i+1)); done; sleep 1"}`},
			}})}, nil
		case 2:
			if latest != nil && latest.ToolCallID == "integration-status-call" {
				if err := decodeIntegrationResult(latest.Content, &terminal); err != nil {
					return nil, err
				}
				phase = 3
				return []*einoschema.Message{einoschema.AssistantMessage("background job inspected", nil)}, nil
			}
			arguments, _ := json.Marshal(map[string]string{"id": launched.ID})
			return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
				ID: "integration-status-call", Type: "function",
				Function: einoschema.FunctionCall{Name: StatusToolName, Arguments: string(arguments)},
			}})}, nil
		default:
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
	})
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(integrationResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(policy),
		runtime.WithIDGenerator(&integrationIDs{}), runtime.WithClock(time.Now),
		runtime.WithOwnerID("integration-owner"), runtime.WithQueueSize(16),
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := model.Selection{ProviderID: "integration-provider", ModelID: "integration-model"}
	snapshot := config.Snapshot{
		Agent: config.Agent{Name: "integration-agent", Model: selection}, Model: selection,
		Metadata: map[string]string{"workspace_id": "integration-workspace", "workspace_root": workspace},
	}
	started := time.Now()
	handle, err := orchestrator.Start(ctx, runtime.Request{SessionID: "integration-session", Message: runtime.UserMessage{Content: "start"}, Config: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	waitIntegrationRun(t, handle)
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("start call did not settle before command exit: %s", elapsed)
	}
	startCall, err := database.GetToolCall(ctx, "integration-start-call")
	if err != nil {
		t.Fatal(err)
	}
	if startCall.Status != session.ToolCallCompleted || !strings.Contains(string(startCall.Input), `"working_directory":"."`) || !strings.Contains(string(startCall.Input), "while") {
		t.Fatalf("durable start = status=%s input=%s", startCall.Status, startCall.Input)
	}
	if launched.ID == "" || launched.State != JobRunning || launched.TimeoutSeconds != 0 {
		t.Fatalf("launch receipt = %#v content=%q durable-output=%s", launched, startContent, startCall.Output)
	}

	waitIntegrationJobTerminal(t, registry, "integration-session", "integration-workspace", workspace, launched.ID)
	phase = 2
	handle, err = orchestrator.Start(ctx, runtime.Request{SessionID: "integration-session", Message: runtime.UserMessage{Content: "poll"}, Config: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	waitIntegrationRun(t, handle)
	if terminal.ID != launched.ID || terminal.State != JobSucceeded || terminal.ExitCode == nil || *terminal.ExitCode != 0 || terminal.Stdout.Text != "one"+strings.Repeat("x", 1021) || terminal.Stdout.Truncated {
		t.Fatalf("terminal status = %#v", terminal)
	}
	statusCall, err := database.GetToolCall(ctx, "integration-status-call")
	if err != nil || statusCall.Status != session.ToolCallCompleted || len(statusCall.Output) == 0 {
		t.Fatalf("durable status = %#v, %v", statusCall, err)
	}
	requestsMu.Lock()
	requests := append([]permissions.Request(nil), permissionRequests...)
	requestsMu.Unlock()
	if len(requests) != 2 || requests[0].Permission != permissionStart || requests[0].Pattern != permissionStart || requests[1].Permission != permissionRead || requests[1].Pattern != permissionRead {
		t.Fatalf("permission requests = %#v", requests)
	}
	encodedRequests, _ := json.Marshal(requests)
	if strings.Contains(string(encodedRequests), "while") || strings.Contains(string(encodedRequests), launched.ID) {
		t.Fatalf("permission request leaked input: %s", encodedRequests)
	}

	mount.Deactivate()
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	freshRegistry, freshMount := mountIntegrationRegistry(t, testOptions())
	sealed, err := session.VerifyExtensionPlanForSession("integration-session", descriptor)
	if err != nil {
		t.Fatal(err)
	}
	freshPlan, err := freshRegistry.AcquireResumePlan(ctx, runtime.ResumePlanRequest{SessionID: "integration-session", Plan: sealed})
	if err != nil {
		t.Fatalf("strict resume plan: %v", err)
	}
	freshTools, err := freshPlan.ResolveTools(ctx, runtime.ToolScopeContext{SessionID: "integration-session", WorkspaceID: "integration-workspace", WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	statusTool := findIntegrationTool(t, freshTools, StatusToolName)
	rawID, _ := json.Marshal(map[string]string{"id": launched.ID})
	canonicalID, err := statusTool.InputDecoder.DecodeToolInput(ctx, rawID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = statusTool.Executor.Execute(ctx, integrationCall("fresh-status", StatusToolName, canonicalID, workspace))
	if !errors.Is(err, errJobNotFound) {
		t.Fatalf("old ID in fresh manager = %v", err)
	}
	startTool := findIntegrationTool(t, freshTools, StartToolName)
	newInput, _ := startTool.InputDecoder.DecodeToolInput(ctx, []byte(`{"command":"sleep 30"}`))
	newOutput, err := startTool.Executor.Execute(ctx, integrationCall("fresh-start", StartToolName, newInput, workspace))
	if err != nil {
		t.Fatal(err)
	}
	var fresh StartResult
	if err := json.Unmarshal(newOutput.Structured, &fresh); err != nil || fresh.ID == launched.ID || strings.Split(fresh.ID, "_")[1] == strings.Split(launched.ID, "_")[1] {
		t.Fatalf("fresh manager identity = %#v, %v", fresh, err)
	}
	freshPlan.Release()
	freshMount.Deactivate()
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := freshMount.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationPermissionDenialPreventsStartMutation(t *testing.T) {
	registry, mount := mountIntegrationRegistry(t, testOptions())
	defer func() {
		mount.Deactivate()
		_ = mount.Close(context.Background())
	}()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "denial.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	phase := 0
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latestToolMessage(request.Messages) != nil {
			phase++
			return []*einoschema.Message{einoschema.AssistantMessage("denied", nil)}, nil
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID: "denied-start", Type: "function", Function: einoschema.FunctionCall{Name: StartToolName, Arguments: `{"command":"sleep 30"}`},
		}})}, nil
	})
	policy := permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
		return permissions.Decision{Action: permissions.ActionDeny, Message: "blocked"}, nil
	})
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(integrationResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(policy), runtime.WithIDGenerator(&integrationIDs{}),
		runtime.WithClock(time.Now), runtime.WithOwnerID("denial-owner"),
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := model.Selection{ProviderID: "integration-provider", ModelID: "integration-model"}
	workspace := t.TempDir()
	handle, err := orchestrator.Start(context.Background(), runtime.Request{
		SessionID: "denial-session", Message: runtime.UserMessage{Content: "deny start"},
		Config: config.Snapshot{Agent: config.Agent{Name: "agent", Model: selection}, Model: selection, Metadata: map[string]string{"workspace_id": "denial-workspace", "workspace_root": workspace}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitIntegrationRun(t, handle)
	if phase != 1 {
		t.Fatalf("model did not observe denial: phase=%d", phase)
	}
	plan, _ := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "denial-session"})
	resolved, _ := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "denial-session", WorkspaceID: "denial-workspace", WorkspaceRoot: workspace})
	listTool := findIntegrationTool(t, resolved, ListToolName)
	listInput, _ := listTool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{}`))
	output, err := listTool.Executor.Execute(context.Background(), integrationCall("denial-list", ListToolName, listInput, workspace))
	plan.Release()
	if err != nil {
		t.Fatal(err)
	}
	var list ListResult
	if err := json.Unmarshal(output.Structured, &list); err != nil || len(list.Jobs) != 0 {
		t.Fatalf("jobs after denied start = %#v, %v", list, err)
	}
}

func mountIntegrationRegistry(t *testing.T, options Options) (*composition.Registry, *composition.Mount) {
	t.Helper()
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	component := extension.Component{InstanceID: "background-jobs", Artifact: extension.Artifact{Name: "background-jobs", Version: "test", Hash: "synthetic-artifact", SourceKind: extension.SourceNative}}
	mount, err := Mount(context.Background(), registry, component, options)
	if err != nil {
		t.Fatal(err)
	}
	return registry, mount
}

func integrationCall(id, name string, input []byte, workspace string) runtime.ToolCall {
	return runtime.ToolCall{
		ID: session.ToolCallID(id), SessionID: "integration-session", Name: name, Input: input,
		Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "integration-session"}, WorkspaceID: "integration-workspace", WorkspaceRoot: workspace},
	}
}

func findIntegrationTool(t *testing.T, tools []runtime.Tool, name string) runtime.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return runtime.Tool{}
}

func latestToolMessage(messages []*einoschema.Message) *einoschema.Message {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == einoschema.Tool {
			return messages[index]
		}
	}
	return nil
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

func waitIntegrationRun(t *testing.T, handle runtime.Handle) {
	t.Helper()
	select {
	case result := <-handle.Done():
		if result.Status != session.RunCompleted || result.Error != nil {
			t.Fatalf("run result = status=%s error=%v", result.Status, result.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run timed out")
	}
}

func waitIntegrationJobTerminal(t *testing.T, registry *composition.Registry, sessionID, workspaceID, workspace, id string) StatusResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typedSessionID := session.ID(sessionID)
	plan, err := registry.AcquireRunPlan(ctx, runtime.RunPlanRequest{SessionID: typedSessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Release()
	resolved, err := plan.ResolveTools(ctx, runtime.ToolScopeContext{SessionID: typedSessionID, WorkspaceID: workspaceID, WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	statusTool := findIntegrationTool(t, resolved, StatusToolName)
	rawID, _ := json.Marshal(map[string]string{"id": id})
	canonicalID, err := statusTool.InputDecoder.DecodeToolInput(ctx, rawID)
	if err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		output, executeErr := statusTool.Executor.Execute(ctx, runtime.ToolCall{
			ID: session.ToolCallID("eventual-status"), SessionID: typedSessionID, Name: StatusToolName, Input: canonicalID,
			Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: typedSessionID}, WorkspaceID: workspaceID, WorkspaceRoot: workspace},
		})
		if executeErr != nil {
			t.Fatal(executeErr)
		}
		var status StatusResult
		if err := json.Unmarshal(output.Structured, &status); err != nil {
			t.Fatal(err)
		}
		if status.State != JobRunning {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not become terminal: %v", id, ctx.Err())
	return StatusResult{}
}

type integrationResolver struct{ streamer model.Streamer }

func (resolver integrationResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{Provider: model.Provider{ID: "integration-provider"}, Model: model.Descriptor{ID: "integration-model", ProviderID: "integration-provider"}, Streamer: resolver.streamer}, nil
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
	return prefix + "-" + time.Unix(int64(ids.n), 0).UTC().Format("150405")
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
