//go:build linux || darwin

package pythonrepl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent-extensions/toolresultredactor"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func TestIntegrationCancellationResetsDurableStateAndRetainsVenv(t *testing.T) {
	options := testOptions(t)
	registry, mount := mountIntegrationRegistry(t, options)
	defer closeIntegrationMount(t, mount)
	database := openIntegrationStore(t, "cancel.db")
	deferDatabaseClose(t, database)
	workspace := t.TempDir()
	marker := workspace + "/mutated"
	phase := 0
	var after ExecuteResult
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		latest := latestToolMessage(request.Messages)
		if phase == 0 {
			code := fmt.Sprintf("x = 99\nopen(%q, 'w').write('ready')\nimport time\ntime.sleep(30)", marker)
			return toolCallMessage("cancel-call", ExecuteToolName, fmt.Sprintf(`{"code":%q}`, code)), nil
		}
		if latest == nil || latest.ToolCallID != "after-cancel-call" {
			return toolCallMessage("after-cancel-call", ExecuteToolName, `{"code":"x"}`), nil
		}
		if err := decodeIntegrationResult(latest.Content, &after); err != nil {
			return nil, err
		}
		phase = 2
		return []*einoschema.Message{einoschema.AssistantMessage("checked", nil)}, nil
	})
	policy := permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	})
	orchestrator := integrationOrchestrator(t, database, registry, streamer, policy, "cancel-owner")
	snapshot := integrationSnapshot(workspace, "cancel-workspace")
	handle, err := orchestrator.Start(context.Background(), runtime.Request{SessionID: "cancel-session", Message: runtime.UserMessage{Content: "mutate"}, Config: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(marker); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Python mutation gate was not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	before, err := os.ReadDir(options.TempRoot)
	if err != nil || len(before) != 1 {
		t.Fatalf("venv before cancel entries=%d err=%v", len(before), err)
	}
	if err := handle.Interrupt(context.Background(), "synthetic interruption"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunInterrupted || !result.Interrupted {
			t.Fatalf("interrupted result=%#v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted run did not settle")
	}
	call, err := database.GetToolCall(context.Background(), "cancel-call")
	if err != nil || call.Status != session.ToolCallInterrupted {
		t.Fatalf("durable interrupted call=%#v err=%v", call, err)
	}
	phase = 1
	runIntegrationRequest(t, orchestrator, "cancel-session", "read", snapshot)
	if after.Status != "python_error" || after.Generation != 1 || !after.StateReset || after.StateResetReason != "canceled" {
		t.Fatalf("post-cancel result=%#v", after)
	}
	afterEntries, err := os.ReadDir(options.TempRoot)
	if err != nil || len(afterEntries) != 1 || before[0].Name() != afterEntries[0].Name() {
		t.Fatalf("venv was not retained: before=%v after=%v err=%v", before, afterEntries, err)
	}
}

func TestIntegrationResultRedactorComposesAfterPython(t *testing.T) {
	options := testOptions(t)
	registry, pythonMount := mountIntegrationRegistry(t, options)
	redactorMount, err := toolresultredactor.Mount(context.Background(), registry, extension.Component{
		InstanceID: "python-output-redactor", Artifact: extension.Artifact{Name: "tool-result-redactor", Version: "test", Hash: "synthetic-redactor", SourceKind: extension.SourceNative},
	}, toolresultredactor.Options{
		AdditionalPatterns: []toolresultredactor.Pattern{{ID: "python-marker", Expression: `RAW_PYTHON_MARKER`}},
		Limits: toolresultredactor.Limits{
			MaxFieldBytes: 4096, MaxStructuredBytes: 16 << 10, MaxStructuredDepth: 16, MaxStructuredNodes: 256,
			MaxAttachments: 4, MaxMetadataEntries: 8, MaxMatchesPerField: 16, MaxPatterns: 4, MaxPatternBytes: 128,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		redactorMount.Deactivate()
		pythonMount.Deactivate()
		_ = redactorMount.Close(context.Background())
		_ = pythonMount.Close(context.Background())
	}()
	database := openIntegrationStore(t, "redactor.db")
	deferDatabaseClose(t, database)
	var modelEnvelope string
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latest := latestToolMessage(request.Messages); latest != nil {
			modelEnvelope = latest.Content
			return []*einoschema.Message{einoschema.AssistantMessage("redacted", nil)}, nil
		}
		return toolCallMessage("redactor-call", ExecuteToolName, `{"code":"print('RAW_PYTHON_MARKER'); 'RAW_PYTHON_MARKER'"}`), nil
	})
	orchestrator := integrationOrchestrator(t, database, registry, streamer, allowIntegrationPolicy(), "redactor-owner")
	runIntegrationRequest(t, orchestrator, "redactor-session", "execute", integrationSnapshot(t.TempDir(), "redactor-workspace"))
	call, err := database.GetToolCall(context.Background(), "redactor-call")
	if err != nil {
		t.Fatal(err)
	}
	combined := modelEnvelope + string(call.Output)
	if strings.Contains(combined, "RAW_PYTHON_MARKER") || !strings.Contains(combined, toolresultredactor.Placeholder) {
		t.Fatalf("redacted result=%s", combined)
	}
	if !strings.Contains(string(call.Input), "RAW_PYTHON_MARKER") {
		t.Fatalf("test no longer proves durable-input limitation: %s", call.Input)
	}
}

func TestIntegrationPackageTimeoutResetsState(t *testing.T) {
	options := testOptions(t)
	options.Limits.DefaultTimeout = time.Second
	registry, mount := mountIntegrationRegistry(t, options)
	defer closeIntegrationMount(t, mount)
	database := openIntegrationStore(t, "timeout.db")
	deferDatabaseClose(t, database)
	phase := 0
	var prime, after ExecuteResult
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		latest := latestToolMessage(request.Messages)
		switch phase {
		case 0:
			if latest == nil {
				return toolCallMessage("timeout-prime", ExecuteToolName, `{"code":"x = 1"}`), nil
			}
			if err := decodeIntegrationResult(latest.Content, &prime); err != nil {
				return nil, err
			}
			phase = 1
			return []*einoschema.Message{einoschema.AssistantMessage("primed", nil)}, nil
		case 2:
			if latest == nil || latest.ToolCallID != "timeout-call" {
				return toolCallMessage("timeout-call", ExecuteToolName, `{"code":"import time; x = 2; time.sleep(30)"}`), nil
			}
			phase = 3
			return []*einoschema.Message{einoschema.AssistantMessage("timed out", nil)}, nil
		case 4:
			if latest == nil || latest.ToolCallID != "timeout-after" {
				return toolCallMessage("timeout-after", ExecuteToolName, `{"code":"x"}`), nil
			}
			if err := decodeIntegrationResult(latest.Content, &after); err != nil {
				return nil, err
			}
			phase = 5
			return []*einoschema.Message{einoschema.AssistantMessage("checked", nil)}, nil
		default:
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
	})
	orchestrator := integrationOrchestrator(t, database, registry, streamer, allowIntegrationPolicy(), "timeout-owner")
	snapshot := integrationSnapshot(t.TempDir(), "timeout-workspace")
	runIntegrationRequest(t, orchestrator, "timeout-session", "prime", snapshot)
	if prime.Status != "completed" {
		t.Fatalf("prime=%#v", prime)
	}
	phase = 2
	runIntegrationRequest(t, orchestrator, "timeout-session", "timeout", snapshot)
	call, err := database.GetToolCall(context.Background(), "timeout-call")
	if err != nil || call.Status != session.ToolCallInterrupted {
		t.Fatalf("timeout call=%#v err=%v", call, err)
	}
	phase = 4
	runIntegrationRequest(t, orchestrator, "timeout-session", "read", snapshot)
	if after.Status != "python_error" || !after.StateReset || after.StateResetReason != "timed_out" || after.Generation != 1 {
		t.Fatalf("post-timeout=%#v", after)
	}
}

func TestIntegrationMaximumExecuteFieldsRemainInline(t *testing.T) {
	options := testOptions(t)
	options.Limits.MaxOutputBytesPerStream = 32
	options.Limits.MaxResultBytes = 32
	options.Limits.MaxExceptionBytes = 64
	registry, mount := mountIntegrationRegistry(t, options)
	defer closeIntegrationMount(t, mount)
	database := openIntegrationStore(t, "maximum.db")
	deferDatabaseClose(t, database)
	completedArgs, _ := json.Marshal(map[string]string{"code": "import sys\nsys.stdout.write(chr(1) * 32)\nsys.stderr.write(chr(2) * 32)\nchr(3) * 100"})
	errorArgs, _ := json.Marshal(map[string]string{"code": "import sys\nsys.stdout.write(chr(1) * 32)\nsys.stderr.write(chr(2) * 32)\nraise ValueError('e' * 200)"})
	phase := 0
	var completed, pythonError ExecuteResult
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		latest := latestToolMessage(request.Messages)
		switch phase {
		case 0:
			if latest == nil {
				return toolCallMessage("maximum-completed", ExecuteToolName, string(completedArgs)), nil
			}
			if err := decodeIntegrationResult(latest.Content, &completed); err != nil {
				return nil, err
			}
			phase = 1
			return []*einoschema.Message{einoschema.AssistantMessage("completed", nil)}, nil
		case 2:
			if latest == nil || latest.ToolCallID != "maximum-error" {
				return toolCallMessage("maximum-error", ExecuteToolName, string(errorArgs)), nil
			}
			if err := decodeIntegrationResult(latest.Content, &pythonError); err != nil {
				return nil, err
			}
			phase = 3
			return []*einoschema.Message{einoschema.AssistantMessage("error", nil)}, nil
		default:
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
	})
	orchestrator := integrationOrchestrator(t, database, registry, streamer, allowIntegrationPolicy(), "maximum-owner")
	snapshot := integrationSnapshot(t.TempDir(), "maximum-workspace")
	runIntegrationRequest(t, orchestrator, "maximum-session", "completed", snapshot)
	phase = 2
	runIntegrationRequest(t, orchestrator, "maximum-session", "error", snapshot)
	if len(completed.Stdout.Text) != 32 || len(completed.Stderr.Text) != 32 || len(completed.Result.Text) != 32 || !completed.Result.Truncated {
		t.Fatalf("completed maxima=%#v", completed)
	}
	if len(pythonError.Stdout.Text) != 32 || len(pythonError.Stderr.Text) != 32 || len(pythonError.Exception.Text) != 64 || !pythonError.Exception.Truncated {
		t.Fatalf("error maxima=%#v", pythonError)
	}
	for _, id := range []session.ToolCallID{"maximum-completed", "maximum-error"} {
		call, err := database.GetToolCall(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		var output runtime.ToolOutput
		if err := json.Unmarshal(call.Output, &output); err != nil {
			t.Fatal(err)
		}
		if output.Truncated || output.External || output.Content != string(output.Structured) || len(output.Structured) == 0 {
			t.Fatalf("Eino altered maximum inline result %s: %#v", id, output)
		}
	}
}
