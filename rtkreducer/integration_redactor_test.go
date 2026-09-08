//go:build linux || darwin

package rtkreducer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	toolresultredactor "github.com/mattsp1290/eino-agent-extensions/toolresultredactor"
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

const (
	rtkRedactorStdoutSecret = "RTK_STDOUT_SECRET_SENTINEL"
	rtkRedactorStderrSecret = "RTK_STDERR_SECRET_SENTINEL"
)

func TestIntegrationReducerThenFinalRedactorSanitizesAllDurableObservers(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "rtk-invocations")
	executable := writeCountingSecretRTK(t, counter)
	options := fixtureOptionsForExecutable(t, executable)
	options.Bindings[0].Mode = JSONMirror
	options.Bindings[0].MatchInput = &InputMatch{Field: "cmd", Equals: []string{"synthetic"}}
	options.Bindings[0].Fields = []Field{{Name: "stdout", Filter: GoTest}}

	raw := []byte(`{"stdout":"` + strings.Repeat("verbose Go test event ", 100) + `","stderr":"tool stderr","exit_code":1}`)
	var executions atomic.Int32
	registry, database, mounts := newIntegrationFixture(t, options, func(context.Context, tools.Execution) (json.RawMessage, error) {
		executions.Add(1)
		return append(json.RawMessage(nil), raw...), nil
	}, allowIntegrationPolicy())

	redactorComponent := integrationComponent("tool-result-redactor")
	redactorComponent.Artifact.ConfigHash = ""
	redactorMount, err := toolresultredactor.Mount(context.Background(), registry, redactorComponent, toolresultredactor.Options{
		Order: toolresultredactor.LateOrder,
		Limits: toolresultredactor.Limits{
			MaxFieldBytes: 1 << 20, MaxStructuredBytes: 2 << 20, MaxStructuredDepth: 32,
			MaxStructuredNodes: 16384, MaxAttachments: 16, MaxMetadataEntries: 16,
			MaxMatchesPerField: 32, MaxPatterns: 4, MaxPatternBytes: 128,
		},
		AdditionalPatterns: []toolresultredactor.Pattern{
			{ID: "rtk-stdout-secret", Expression: rtkRedactorStdoutSecret},
			{ID: "rtk-stderr-secret", Expression: rtkRedactorStderrSecret},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	notices := make(chan runtime.ToolSettledNotice, 1)
	observerMount, err := registry.Mount(context.Background(), integrationComponent("tool-result-redactor-observer"), composition.InstallerFunc(func(_ context.Context, registrar *composition.Registrar) error {
		return extension.On(registrar.Extensions(), runtime.ToolSettledPoint, extension.Registration{
			ID: "rtk-redactor/integration-observer", Scope: extension.GlobalScope(),
		}, func(_ context.Context, notice runtime.ToolSettledNotice) error {
			select {
			case notices <- notice:
			default:
			}
			return nil
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	mounts = append(mounts, redactorMount, observerMount)
	defer closeIntegrationFixture(t, database, mounts)

	var nextModelRequest []byte
	streamer := integrationScript(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latestIntegrationToolMessage(request.Messages) != nil {
			nextModelRequest, _ = json.Marshal(request.Messages)
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
		return integrationToolCall("redactor-integration-call", integrationToolName, `{"cmd":"synthetic"}`), nil
	})
	runResult := runRedactorIntegration(t, registry, database, streamer, allowIntegrationPolicy())

	if executions.Load() != 1 {
		t.Fatalf("underlying tool executions = %d, want exactly one", executions.Load())
	}
	if got := countFile(t, counter); got != 1 {
		t.Fatalf("RTK invocations = %d, want exactly one", got)
	}
	call, err := database.GetToolCall(context.Background(), "redactor-integration-call")
	if err != nil {
		t.Fatal(err)
	}
	var output runtime.ToolOutput
	if err := json.Unmarshal(call.Output, &output); err != nil {
		t.Fatalf("decode durable output: %v; raw=%s", err, call.Output)
	}
	if output.Status != "completed" || output.Content == "" {
		t.Fatalf("durable output status/content = %q/%q", output.Status, output.Content)
	}
	if !strings.Contains(output.Content, "[Reduced by RTK go-test;") {
		t.Fatalf("reduced marker missing from durable output: %q", output.Content)
	}
	if string(output.Structured) != output.Content {
		t.Fatalf("durable mirrored copies differ: content=%q structured=%q", output.Content, output.Structured)
	}
	if len(output.Content) >= len(raw) {
		t.Fatalf("reduced output did not shrink: input=%d output=%d", len(raw), len(output.Content))
	}
	if runResult.Error != nil {
		t.Fatalf("run returned public error: %v", runResult.Error)
	}
	if call.Error != "" {
		t.Fatalf("tool call returned public error: %q", call.Error)
	}
	if len(nextModelRequest) == 0 {
		t.Fatal("model did not receive a second turn")
	}

	notice := <-notices
	noticeRaw, err := json.Marshal(notice)
	if err != nil {
		t.Fatal(err)
	}
	for label, value := range map[string][]byte{
		"durable output":     call.Output,
		"next model request": nextModelRequest,
		"settled notice":     noticeRaw,
	} {
		assertSecretAbsent(t, label, value)
	}
	if strings.Contains(string(output.Content), rtkRedactorStdoutSecret) {
		t.Fatalf("stdout secret was not redacted: %q", output.Content)
	}
	if !strings.Contains(string(call.Output), toolresultredactor.Placeholder) || !strings.Contains(string(nextModelRequest), toolresultredactor.Placeholder) {
		t.Fatal("redacted durable/model copies do not contain the redaction placeholder")
	}
}

func runRedactorIntegration(t *testing.T, registry *composition.Registry, database *store.Store, streamer model.Streamer, policy permissions.Policy) runtime.Result {
	t.Helper()
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(integrationResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(policy),
		runtime.WithIDGenerator(&integrationIDs{}), runtime.WithClock(time.Now),
		runtime.WithOwnerID("rtk-redactor-integration-owner"), runtime.WithQueueSize(16), runtime.WithLease(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := model.Selection{ProviderID: "rtk-integration-provider", ModelID: "rtk-integration-model"}
	handle, err := orchestrator.Start(context.Background(), runtime.Request{
		SessionID: session.ID("rtk-redactor-integration-session"), Message: runtime.UserMessage{Content: "run fixture"},
		Config: config.Snapshot{Agent: config.Agent{Name: "rtk-redactor-integration-agent", Model: selection}, Model: selection,
			Metadata: map[string]string{"workspace_id": "rtk-redactor-integration-workspace", "workspace_root": t.TempDir()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunCompleted {
			t.Fatalf("integration result status = %s, want completed", result.Status)
		}
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("integration run timed out")
		return runtime.Result{}
	}
}

func writeCountingSecretRTK(t *testing.T, counter string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rtk")
	script := fmt.Sprintf("#!/bin/sh\nprintf x >> %s\nprintf '%s reduced output\\n'\nprintf '%s\\n' >&2\n", shellQuote(counter), rtkRedactorStdoutSecret, rtkRedactorStderrSecret)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertSecretAbsent(t *testing.T, label string, value []byte) {
	t.Helper()
	if strings.Contains(string(value), rtkRedactorStdoutSecret) {
		t.Fatalf("%s contains stdout secret", label)
	}
	if strings.Contains(string(value), rtkRedactorStderrSecret) {
		t.Fatalf("%s contains stderr secret", label)
	}
}
