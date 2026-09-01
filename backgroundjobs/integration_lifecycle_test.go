//go:build linux || darwin

package backgroundjobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent-extensions/toolresultredactor"
	"github.com/mattsp1290/eino-agent/config"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
)

func TestIntegrationKillAndTimeoutThroughOrchestrator(t *testing.T) {
	registry, mount := mountIntegrationRegistry(t, testOptions())
	defer func() {
		mount.Deactivate()
		_ = mount.Close(context.Background())
	}()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	phase := 0
	var longJob, timeoutJob StartResult
	var killed KillResult
	var timedOut StatusResult
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		latest := latestToolMessage(request.Messages)
		switch phase {
		case 0:
			if latest != nil && latest.ToolCallID == "lifecycle-start-long" {
				if err := decodeIntegrationResult(latest.Content, &longJob); err != nil {
					return nil, err
				}
				phase = 1
				return []*einoschema.Message{einoschema.AssistantMessage("started", nil)}, nil
			}
			return toolCallMessage("lifecycle-start-long", StartToolName, `{"command":"sleep 30"}`), nil
		case 2:
			if latest != nil && latest.ToolCallID == "lifecycle-kill" {
				if err := decodeIntegrationResult(latest.Content, &killed); err != nil {
					return nil, err
				}
				phase = 3
				return []*einoschema.Message{einoschema.AssistantMessage("killed", nil)}, nil
			}
			arguments, _ := json.Marshal(map[string]string{"id": longJob.ID})
			return toolCallMessage("lifecycle-kill", KillToolName, string(arguments)), nil
		case 4:
			if latest != nil && latest.ToolCallID == "lifecycle-start-timeout" {
				if err := decodeIntegrationResult(latest.Content, &timeoutJob); err != nil {
					return nil, err
				}
				phase = 5
				return []*einoschema.Message{einoschema.AssistantMessage("timeout started", nil)}, nil
			}
			return toolCallMessage("lifecycle-start-timeout", StartToolName, `{"command":"sleep 30","timeout_seconds":1}`), nil
		case 6:
			if latest != nil && latest.ToolCallID == "lifecycle-status-timeout" {
				if err := decodeIntegrationResult(latest.Content, &timedOut); err != nil {
					return nil, err
				}
				phase = 7
				return []*einoschema.Message{einoschema.AssistantMessage("timed out", nil)}, nil
			}
			arguments, _ := json.Marshal(map[string]string{"id": timeoutJob.ID})
			return toolCallMessage("lifecycle-status-timeout", StatusToolName, string(arguments)), nil
		default:
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
	})
	orchestrator := integrationOrchestrator(t, database, registry, streamer, "lifecycle-owner")
	workspace := t.TempDir()
	snapshot := integrationSnapshot(workspace, "lifecycle-workspace")
	runIntegrationRequest(t, orchestrator, "lifecycle-session", "start long", snapshot)
	if longJob.ID == "" || longJob.State != JobRunning {
		t.Fatalf("long receipt = %#v", longJob)
	}
	phase = 2
	runIntegrationRequest(t, orchestrator, "lifecycle-session", "kill", snapshot)
	if killed.ID != longJob.ID || killed.State != JobKilled || !killed.NewlyAccepted {
		t.Fatalf("orchestrated kill = %#v", killed)
	}
	phase = 4
	runIntegrationRequest(t, orchestrator, "lifecycle-session", "start timeout", snapshot)
	if timeoutJob.ID == "" || timeoutJob.TimeoutSeconds != 1 {
		t.Fatalf("timeout receipt = %#v", timeoutJob)
	}
	time.Sleep(1200 * time.Millisecond)
	phase = 6
	runIntegrationRequest(t, orchestrator, "lifecycle-session", "status timeout", snapshot)
	if timedOut.ID != timeoutJob.ID || timedOut.State != JobTimedOut || timedOut.ExitCode != nil {
		t.Fatalf("orchestrated timeout = %#v", timedOut)
	}
}

func TestIntegrationRedactorSanitizesModelAndDurableStatusButNotManagerTail(t *testing.T) {
	registry, backgroundMount := mountIntegrationRegistry(t, testOptions())
	redactorComponent := extension.Component{InstanceID: "background-output-redactor", Artifact: extension.Artifact{Name: "tool-result-redactor", Version: "test", Hash: "redactor-artifact", SourceKind: extension.SourceNative}}
	redactorMount, err := toolresultredactor.Mount(context.Background(), registry, redactorComponent, toolresultredactor.Options{
		Limits: toolresultredactor.Limits{
			MaxFieldBytes: 4096, MaxStructuredBytes: 16 << 10, MaxStructuredDepth: 16, MaxStructuredNodes: 256,
			MaxAttachments: 4, MaxMetadataEntries: 8, MaxMatchesPerField: 16, MaxPatterns: 4, MaxPatternBytes: 128,
		},
		AdditionalPatterns: []toolresultredactor.Pattern{{ID: "background-marker", Expression: `RAW_BACKGROUND_MARKER`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		redactorMount.Deactivate()
		backgroundMount.Deactivate()
		_ = redactorMount.Close(context.Background())
		_ = backgroundMount.Close(context.Background())
	}()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "redactor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	phase := 0
	var receipt StartResult
	var modelStatusEnvelope string
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		latest := latestToolMessage(request.Messages)
		switch phase {
		case 0:
			if latest != nil && latest.ToolCallID == "redactor-start" {
				if err := decodeIntegrationResult(latest.Content, &receipt); err != nil {
					return nil, err
				}
				phase = 1
				return []*einoschema.Message{einoschema.AssistantMessage("started", nil)}, nil
			}
			return toolCallMessage("redactor-start", StartToolName, `{"command":"printf RAW_BACKGROUND_MARKER"}`), nil
		case 2:
			if latest != nil && latest.ToolCallID == "redactor-status" {
				modelStatusEnvelope = latest.Content
				phase = 3
				return []*einoschema.Message{einoschema.AssistantMessage("inspected", nil)}, nil
			}
			arguments, _ := json.Marshal(map[string]string{"id": receipt.ID})
			return toolCallMessage("redactor-status", StatusToolName, string(arguments)), nil
		default:
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
	})
	orchestrator := integrationOrchestrator(t, database, registry, streamer, "redactor-owner")
	workspace := t.TempDir()
	snapshot := integrationSnapshot(workspace, "redactor-workspace")
	runIntegrationRequest(t, orchestrator, "redactor-session", "start", snapshot)
	time.Sleep(200 * time.Millisecond)
	phase = 2
	runIntegrationRequest(t, orchestrator, "redactor-session", "status", snapshot)
	if strings.Contains(modelStatusEnvelope, "RAW_BACKGROUND_MARKER") || !strings.Contains(modelStatusEnvelope, toolresultredactor.Placeholder) {
		t.Fatalf("model-visible status was not redacted: %s", modelStatusEnvelope)
	}
	durable, err := database.GetToolCall(context.Background(), "redactor-status")
	if err != nil || strings.Contains(string(durable.Output), "RAW_BACKGROUND_MARKER") || !strings.Contains(string(durable.Output), toolresultredactor.Placeholder) {
		t.Fatalf("durable status was not redacted: %s, %v", durable.Output, err)
	}
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "redactor-session"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "redactor-session", WorkspaceID: "redactor-workspace", WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	statusTool := findIntegrationTool(t, resolved, StatusToolName)
	idRaw, _ := json.Marshal(map[string]string{"id": receipt.ID})
	canonicalID, _ := statusTool.InputDecoder.DecodeToolInput(context.Background(), idRaw)
	raw, err := statusTool.Executor.Execute(context.Background(), runtime.ToolCall{
		ID: "raw-status", SessionID: "redactor-session", Name: StatusToolName, Input: canonicalID,
		Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "redactor-session"}, WorkspaceID: "redactor-workspace", WorkspaceRoot: workspace},
	})
	plan.Release()
	if err != nil || !strings.Contains(string(raw.Structured), "RAW_BACKGROUND_MARKER") {
		t.Fatalf("manager tail did not retain raw bounded output: %s, %v", raw.Structured, err)
	}
}

func TestIntegrationMaximumTrackedListSettlement(t *testing.T) {
	options := testOptions()
	options.Limits.MaxRunning = 2
	options.Limits.MaxTracked = 2
	registry, mount := mountIntegrationRegistry(t, options)
	defer func() {
		mount.Deactivate()
		_ = mount.Close(context.Background())
	}()
	workspace := t.TempDir()
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "list-session"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "list-session", WorkspaceID: "list-workspace", WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	startTool := findIntegrationTool(t, resolved, StartToolName)
	for index := 0; index < 2; index++ {
		input, _ := startTool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"command":"sleep 30"}`))
		if _, err := startTool.Executor.Execute(context.Background(), runtime.ToolCall{
			ID: session.ToolCallID("seed-" + string(rune('a'+index))), SessionID: "list-session", Name: StartToolName, Input: input,
			Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "list-session"}, WorkspaceID: "list-workspace", WorkspaceRoot: workspace},
		}); err != nil {
			t.Fatal(err)
		}
	}
	plan.Release()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "list.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var listed ListResult
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latest := latestToolMessage(request.Messages); latest != nil && latest.ToolCallID == "maximum-list" {
			if err := decodeIntegrationResult(latest.Content, &listed); err != nil {
				return nil, err
			}
			return []*einoschema.Message{einoschema.AssistantMessage("listed", nil)}, nil
		}
		return toolCallMessage("maximum-list", ListToolName, `{}`), nil
	})
	orchestrator := integrationOrchestrator(t, database, registry, streamer, "list-owner")
	runIntegrationRequest(t, orchestrator, "list-session", "list", integrationSnapshot(workspace, "list-workspace"))
	if len(listed.Jobs) != 2 || listed.Jobs[0].ID >= listed.Jobs[1].ID && listed.Jobs[0].StartedAt == listed.Jobs[1].StartedAt {
		t.Fatalf("maximum list = %#v", listed)
	}
	durable, err := database.GetToolCall(context.Background(), "maximum-list")
	if err != nil || len(durable.Output) == 0 || strings.Contains(string(durable.Output), "sleep 30") {
		t.Fatalf("durable maximum list = %s, %v", durable.Output, err)
	}
}

func TestIntegrationResumeDoesNotReplaySettledStart(t *testing.T) {
	ctx := context.Background()
	registry, mount := mountIntegrationRegistry(t, testOptions())
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "resume-settled.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	phase := 0
	var receipt StartResult
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latest := latestToolMessage(request.Messages); latest != nil && latest.ToolCallID == "original-settled-start" {
			if err := decodeIntegrationResult(latest.Content, &receipt); err != nil {
				return nil, err
			}
			phase++
			return []*einoschema.Message{einoschema.AssistantMessage("settled", nil)}, nil
		}
		return toolCallMessage("original-settled-start", StartToolName, `{"command":"printf settled-resume"}`), nil
	})
	original := integrationOrchestrator(t, database, registry, streamer, "original-owner")
	workspace := t.TempDir()
	snapshot := integrationSnapshot(workspace, "resume-workspace")
	handle, err := original.Start(ctx, runtime.Request{SessionID: "resume-settled-session", Message: runtime.UserMessage{Content: "start"}, Config: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	waitIntegrationRun(t, handle)
	if phase != 1 || receipt.ID == "" {
		t.Fatalf("original start did not settle: phase=%d receipt=%#v", phase, receipt)
	}
	originalCall, err := database.GetToolCall(ctx, "original-settled-start")
	if err != nil {
		t.Fatal(err)
	}
	originalRun, err := database.GetRun(ctx, handle.RunID())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manualRun, err := database.AdmitRun(ctx, session.Run{
		ID: "settled-resume-run", SessionID: "resume-settled-session", OwnerID: "expired-owner", ClaimToken: "expired-run-claim",
		Agent: originalRun.Agent, ProviderID: originalRun.ProviderID, ModelID: originalRun.ModelID,
		Status: session.RunPending, Config: originalRun.Config, ExtensionPlan: originalRun.ExtensionPlan, CreatedAt: now,
	}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	execution := database.Execution(session.RunFence{RunID: manualRun.ID, ClaimToken: manualRun.ClaimToken})
	assistant := session.Message{ID: "settled-resume-assistant", SessionID: manualRun.SessionID, RunID: manualRun.ID, Role: session.RoleAssistant, CreatedAt: now, UpdatedAt: now}
	if _, err := execution.AppendMessage(ctx, assistant); err != nil {
		t.Fatal(err)
	}
	call := session.ToolCall{
		ID: "settled-resume-call", SessionID: manualRun.SessionID, RunID: manualRun.ID, MessageID: assistant.ID,
		RequestPartID: "settled-resume-request", ResultMessageID: "settled-resume-result-message", ResultPartID: "settled-resume-result-part",
		Name: StartToolName, Pattern: permissionStart, Input: append(json.RawMessage(nil), originalCall.Input...), Status: session.ToolCallPending,
	}
	requestPayload, _ := json.Marshal(map[string]any{"id": call.ID, "name": call.Name, "arguments": json.RawMessage(call.Input)})
	created, err := execution.CreateToolCall(ctx, session.CreateToolCallRequest{
		Call:        call,
		RequestPart: session.Part{ID: call.RequestPartID, MessageID: call.MessageID, SessionID: call.SessionID, RunID: call.RunID, Kind: session.PartToolCall, Payload: requestPayload, CreatedAt: now, UpdatedAt: now},
		Event:       session.ToolTransitionEvent{ID: "settled-resume-create-event", CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := execution.ClaimToolCall(ctx, session.ClaimToolCallRequest{
		ID: created.Call.ID, ClaimedBy: "expired-owner", ClaimToken: "expired-tool-claim", StartedAt: now.Add(time.Millisecond), LeaseDuration: time.Millisecond,
		Event: session.ToolTransitionEvent{ID: "settled-resume-claim-event", CreatedAt: now.Add(time.Millisecond)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(originalCall.Output, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["tool_call_id"] = string(call.ID)
	output, _ := json.Marshal(envelope)
	completedAt := now.Add(2 * time.Millisecond)
	if _, err := execution.SettleToolCall(ctx, session.SettleToolCallRequest{
		Settlement: session.ToolSettlement{
			ID: call.ID, ClaimedBy: claimed.Call.ClaimedBy, ClaimToken: claimed.Call.ClaimToken, Status: session.ToolCallCompleted,
			Output: output, CompletedAt: completedAt,
			ResultMessage: session.Message{ID: call.ResultMessageID, SessionID: call.SessionID, RunID: call.RunID, ParentID: call.MessageID, Role: session.RoleTool, CreatedAt: completedAt, UpdatedAt: completedAt},
			ResultPart:    session.Part{ID: call.ResultPartID, MessageID: call.ResultMessageID, SessionID: call.SessionID, RunID: call.RunID, Kind: session.PartToolResult, Payload: output, CreatedAt: completedAt, UpdatedAt: completedAt},
		},
		Event: session.ToolTransitionEvent{ID: "settled-resume-terminal-event", CreatedAt: completedAt},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	mount.Deactivate()
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond)
	beforeCall, _ := database.GetToolCall(ctx, call.ID)
	beforeRun, _ := database.GetRun(ctx, manualRun.ID)
	changedOptions := testOptions()
	changedOptions.ShellIdentity = "system-sh-v2"
	changedRegistry, changedMount := mountIntegrationRegistry(t, changedOptions)
	changedStreamer := integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
		return []*einoschema.Message{einoschema.AssistantMessage("must not run", nil)}, nil
	})
	changedResumer := integrationOrchestrator(t, database, changedRegistry, changedStreamer, "changed-owner")
	changedHandle, changedErr := changedResumer.Resume(ctx, manualRun.ID)
	if changedHandle != nil || !errors.Is(changedErr, runtime.ErrExtensionPlanMismatch) {
		t.Fatalf("changed configuration resume = handle:%t error:%v", changedHandle != nil, changedErr)
	}
	afterCall, _ := database.GetToolCall(ctx, call.ID)
	afterRun, _ := database.GetRun(ctx, manualRun.ID)
	if afterCall.Status != beforeCall.Status || afterCall.ClaimToken != beforeCall.ClaimToken || afterRun.Status != beforeRun.Status || afterRun.ClaimToken != beforeRun.ClaimToken {
		t.Fatal("strict resume mismatch mutated durable state")
	}
	changedMount.Deactivate()
	if err := changedMount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	freshRegistry, freshMount := mountIntegrationRegistry(t, testOptions())
	defer func() {
		freshMount.Deactivate()
		_ = freshMount.Close(context.Background())
	}()
	var permissionChecks atomic.Int32
	resumeStreamer := integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
		return []*einoschema.Message{einoschema.AssistantMessage("resumed without replay", nil)}, nil
	})
	resumer, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(integrationResolver{streamer: resumeStreamer}), runtime.WithRunPlanProvider(freshRegistry),
		runtime.WithPermissions(permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
			permissionChecks.Add(1)
			return permissions.Decision{Action: permissions.ActionAllow}, nil
		})),
		runtime.WithIDGenerator(&integrationIDs{}), runtime.WithClock(time.Now), runtime.WithOwnerID("fresh-owner"), runtime.WithLease(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := resumer.Resume(ctx, manualRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-resumed.Done():
		if result.Error != nil || result.Status != session.RunInterrupted {
			t.Fatalf("settled start resume = status:%s error:%v", result.Status, result.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("settled start resume timed out")
	}
	if permissionChecks.Load() != 0 {
		t.Fatalf("resume replayed settled start through permissions: checks=%d", permissionChecks.Load())
	}
	settled, err := database.GetToolCall(ctx, call.ID)
	if err != nil || settled.Status != session.ToolCallCompleted || string(settled.Output) != string(output) {
		t.Fatalf("settled start changed on resume: status=%s error=%v", settled.Status, err)
	}
	plan, err := freshRegistry.AcquireRunPlan(ctx, runtime.RunPlanRequest{SessionID: manualRun.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := plan.ResolveTools(ctx, runtime.ToolScopeContext{SessionID: manualRun.SessionID, WorkspaceID: "resume-workspace", WorkspaceRoot: workspace})
	listTool := findIntegrationTool(t, resolved, ListToolName)
	listInput, _ := listTool.InputDecoder.DecodeToolInput(ctx, []byte(`{}`))
	listOutput, err := listTool.Executor.Execute(ctx, runtime.ToolCall{
		ID: "resume-list", SessionID: manualRun.SessionID, Name: ListToolName, Input: listInput,
		Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: manualRun.SessionID}, WorkspaceID: "resume-workspace", WorkspaceRoot: workspace},
	})
	plan.Release()
	var list ListResult
	if err != nil || json.Unmarshal(listOutput.Structured, &list) != nil || len(list.Jobs) != 0 {
		t.Fatalf("fresh manager reconstructed settled job: %#v, %v", list, err)
	}
}

func toolCallMessage(id, name, arguments string) []*einoschema.Message {
	return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{ID: id, Type: "function", Function: einoschema.FunctionCall{Name: name, Arguments: arguments}}})}
}

func integrationSnapshot(workspace, workspaceID string) config.Snapshot {
	selection := model.Selection{ProviderID: "integration-provider", ModelID: "integration-model"}
	return config.Snapshot{Agent: config.Agent{Name: "integration-agent", Model: selection}, Model: selection, Metadata: map[string]string{"workspace_id": workspaceID, "workspace_root": workspace}}
}

func integrationOrchestrator(t *testing.T, database *store.Store, registry runtime.RunPlanProvider, streamer model.Streamer, owner string) *runtime.StreamingOrchestrator {
	t.Helper()
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(integrationResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
			return permissions.Decision{Action: permissions.ActionAllow}, nil
		})),
		runtime.WithIDGenerator(&integrationIDs{}), runtime.WithClock(time.Now), runtime.WithOwnerID(owner), runtime.WithQueueSize(16),
	)
	if err != nil {
		t.Fatal(err)
	}
	return orchestrator
}

func runIntegrationRequest(t *testing.T, orchestrator *runtime.StreamingOrchestrator, sessionID, content string, snapshot config.Snapshot) {
	t.Helper()
	handle, err := orchestrator.Start(context.Background(), runtime.Request{SessionID: session.ID(sessionID), Message: runtime.UserMessage{Content: content}, Config: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	waitIntegrationRun(t, handle)
}
