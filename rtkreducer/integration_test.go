//go:build linux || darwin

package rtkreducer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

const (
	integrationToolName = "fixture_tool"
	integrationInput    = `{"cmd":"go test -json ./..."}`
)

func TestIntegrationRealRTKMirrorsJSONAndExecutesToolOnce(t *testing.T) {
	rtk := requireRealRTK(t)
	raw := goTestResultFixture()
	var executions atomic.Int32
	registry, database, mounts := newIntegrationFixture(t, rtk.options, func(context.Context, tools.Execution) (json.RawMessage, error) {
		executions.Add(1)
		return append(json.RawMessage(nil), raw...), nil
	}, nil)
	defer closeIntegrationFixture(t, database, mounts)

	var nextModelContent string
	streamer := integrationScript(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if toolMessage := latestIntegrationToolMessage(request.Messages); toolMessage != nil {
			nextModelContent = toolMessage.Content
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
		return integrationToolCall("integration-call", integrationToolName, integrationInput), nil
	})
	runIntegration(t, registry, database, streamer, permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	}))

	if executions.Load() != 1 {
		t.Fatalf("underlying tool executions = %d, want exactly one", executions.Load())
	}
	call, err := database.GetToolCall(context.Background(), "integration-call")
	if err != nil {
		t.Fatal(err)
	}
	var output runtime.ToolOutput
	if err := json.Unmarshal(call.Output, &output); err != nil {
		t.Fatalf("decode durable output: %v; raw=%s", err, call.Output)
	}
	if output.Status != "completed" {
		t.Fatalf("durable status = %q, want completed", output.Status)
	}
	if output.Content == string(raw) || len(output.Content) >= len(raw) {
		t.Fatalf("content was not reduced: input=%d output=%d", len(raw), len(output.Content))
	}
	if !strings.Contains(output.Content, "[Reduced by RTK go-test;") || !strings.Contains(output.Content, "FAILURE_SENTINEL") {
		t.Fatalf("reduced content lost marker or failure sentinel: %q", output.Content)
	}
	if string(output.Structured) != output.Content {
		t.Fatalf("mirrored durable copies differ: content=%q structured=%q", output.Content, output.Structured)
	}
	if output.Truncated || output.External || output.Redacted || output.OriginalSize != int64(2*len(output.Content)) || output.InlineSize != output.OriginalSize {
		t.Fatalf("runtime retention authority changed: %+v", output)
	}
	if !bytesEqualUnselectedJSON(t, raw, output.Structured, "stderr", "exit_code", "unknown") {
		t.Fatal("unselected JSON bytes changed")
	}
	var modelOutput runtime.ToolOutput
	if err := json.Unmarshal([]byte(nextModelContent), &modelOutput); err != nil {
		t.Fatalf("decode next model tool envelope: %v; raw=%q", err, nextModelContent)
	}
	if modelOutput.Content != output.Content || string(modelOutput.Structured) != output.Content {
		t.Fatalf("next model copies differ from reduced content: next=%q durable=%q", nextModelContent, output.Content)
	}
}

func TestIntegrationRealRTKDiffAndLogFilters(t *testing.T) {
	rtk := requireRealRTK(t)
	for _, test := range []struct {
		name   string
		filter Filter
		input  string
	}{
		{name: "git diff", filter: GitDiff, input: verboseDiffFixture()},
		{name: "log", filter: Log, input: verboseLogFixture()},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := rtk.options
			options.Bindings = []Binding{{ToolName: integrationToolName, Mode: JSONMirror, MatchInput: &InputMatch{Field: "cmd", Equals: []string{"fixture"}}, Fields: []Field{{Name: "stdout", Filter: test.filter}}}}
			raw := []byte(`{"stdout":` + strconvQuote(test.input) + `,"stderr":"KEEP_STDERR","unknown":{"keep":"KEEP_UNKNOWN"}}`)
			var next string
			var executions atomic.Int32
			registry, database, mounts := newIntegrationFixture(t, options, func(context.Context, tools.Execution) (json.RawMessage, error) {
				executions.Add(1)
				return append(json.RawMessage(nil), raw...), nil
			}, allowIntegrationPolicy())
			defer closeIntegrationFixture(t, database, mounts)
			streamer := integrationScript(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
				if message := latestIntegrationToolMessage(request.Messages); message != nil {
					next = message.Content
					return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
				}
				return integrationToolCall("real-"+string(test.filter), integrationToolName, `{"cmd":"fixture"}`), nil
			})
			runIntegration(t, registry, database, streamer, allowIntegrationPolicy())
			if executions.Load() != 1 {
				t.Fatalf("filter %s underlying executions=%d, want one", test.filter, executions.Load())
			}
			call, err := database.GetToolCall(context.Background(), session.ToolCallID("real-"+string(test.filter)))
			if err != nil {
				t.Fatal(err)
			}
			var durable, modelVisible runtime.ToolOutput
			if err := json.Unmarshal(call.Output, &durable); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(next), &modelVisible); err != nil {
				t.Fatal(err)
			}
			if durable.Status != "completed" || durable.Content != string(durable.Structured) || modelVisible.Content != durable.Content || string(modelVisible.Structured) != durable.Content {
				t.Fatalf("filter %s durable/model mirrors differ: durable=%+v model=%+v", test.filter, durable, modelVisible)
			}
			if !strings.Contains(durable.Content, "[Reduced by RTK "+string(test.filter)+";") || len(durable.Content) >= len(raw) || !strings.Contains(durable.Content, "KEEP_") {
				t.Fatalf("filter %s did not produce marked shorter output: input=%d output=%d content=%q", test.filter, len(raw), len(durable.Content), durable.Content)
			}
		})
	}
}

func TestIntegrationUnmatchedPermissionAndFailureDoNotStartRTK(t *testing.T) {
	for _, test := range []struct {
		name       string
		id         string
		input      string
		policy     permissions.Policy
		execError  bool
		wantExec   int32
		wantStatus string
	}{
		{name: "mismatched input", id: "mismatch-call", input: `{"cmd":"go test ./..."}`, policy: allowIntegrationPolicy(), wantExec: 1, wantStatus: "completed"},
		{name: "permission denied", id: "permission-call", input: integrationInput, policy: permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
			return permissions.Decision{Action: permissions.ActionDeny, Message: "blocked"}, nil
		}), wantExec: 0, wantStatus: "expected_failure"},
		{name: "approval required", id: "approval-call", input: integrationInput, policy: permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
			return permissions.Decision{Action: permissions.ActionAsk, Message: "needs approval"}, nil
		}), wantExec: 0, wantStatus: "expected_failure"},
		{name: "tool failure", id: "failure-call", input: integrationInput, policy: allowIntegrationPolicy(), execError: true, wantExec: 1, wantStatus: "operational_failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			counter := filepath.Join(t.TempDir(), "invocations")
			executable := writeCountingRTK(t, counter)
			options := fixtureOptionsForExecutable(t, executable)
			var executions atomic.Int32
			registry, database, mounts := newIntegrationFixture(t, options, func(context.Context, tools.Execution) (json.RawMessage, error) {
				executions.Add(1)
				if test.execError {
					return nil, errors.New("fixture tool failure")
				}
				return json.RawMessage(`{"stdout":"` + strings.Repeat("verbose log ", 100) + `"}`), nil
			}, nil)
			defer closeIntegrationFixture(t, database, mounts)
			streamer := integrationScript(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
				if latestIntegrationToolMessage(request.Messages) != nil {
					return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
				}
				return integrationToolCall(test.id, integrationToolName, test.input), nil
			})
			runIntegration(t, registry, database, streamer, test.policy)
			call, err := database.GetToolCall(context.Background(), session.ToolCallID(test.id))
			if err != nil {
				t.Fatal(err)
			}
			var output runtime.ToolOutput
			if err := json.Unmarshal(call.Output, &output); err != nil {
				t.Fatal(err)
			}
			if output.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", output.Status, test.wantStatus)
			}
			if executions.Load() != test.wantExec {
				t.Fatalf("underlying executions = %d, want %d", executions.Load(), test.wantExec)
			}
			if got := countFile(t, counter); got != 0 {
				t.Fatalf("RTK invocation count = %d, want zero", got)
			}
		})
	}
}

func newIntegrationFixture(t *testing.T, options Options, execute tools.Executor, policy permissions.Policy) (*composition.Registry, *store.Store, []*composition.Mount) {
	reducerComponent := integrationComponent("rtk-reducer")
	reducerComponent.Artifact.ConfigHash = ""
	return newIntegrationFixtureWithReducerComponent(t, options, execute, policy, reducerComponent)
}

func newIntegrationFixtureWithReducerComponent(t *testing.T, options Options, execute tools.Executor, policy permissions.Policy, reducerComponent extension.Component) (*composition.Registry, *store.Store, []*composition.Mount) {
	t.Helper()
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "integration.db"))
	if err != nil {
		t.Fatal(err)
	}
	toolMount, err := registry.Mount(context.Background(), integrationComponent("fixture-tool"), composition.InstallerFunc(func(_ context.Context, registrar *composition.Registrar) error {
		return registrar.Tool(composition.ToolRegistration{
			ID: "fixture-tool-registration", Scope: extension.GlobalScope(),
			Definition: tools.Definition{Name: integrationToolName, Description: "integration fixture", Execute: execute, Retention: runtime.RetentionPolicy{MaxInlineBytes: 8 << 20}},
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	reducer, err := Mount(context.Background(), registry, reducerComponent, options)
	if err != nil {
		_ = toolMount.Close(context.Background())
		_ = database.Close()
		t.Fatal(err)
	}
	return registry, database, []*composition.Mount{reducer, toolMount}
}

func runIntegration(t *testing.T, registry *composition.Registry, database *store.Store, streamer model.Streamer, policy permissions.Policy) {
	t.Helper()
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(integrationResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(policy),
		runtime.WithIDGenerator(&integrationIDs{}), runtime.WithClock(time.Now),
		runtime.WithOwnerID("rtk-integration-owner"), runtime.WithQueueSize(16), runtime.WithLease(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	selection := model.Selection{ProviderID: "rtk-integration-provider", ModelID: "rtk-integration-model"}
	handle, err := orchestrator.Start(context.Background(), runtime.Request{
		SessionID: session.ID("rtk-integration-session"), Message: runtime.UserMessage{Content: "run fixture"},
		Config: config.Snapshot{Agent: config.Agent{Name: "rtk-integration-agent", Model: selection}, Model: selection,
			Metadata: map[string]string{"workspace_id": "rtk-integration-workspace", "workspace_root": root}},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunCompleted || result.Error != nil {
			t.Fatalf("integration result = %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("integration run timed out")
	}
}

func closeIntegrationFixture(t *testing.T, database *store.Store, mounts []*composition.Mount) {
	t.Helper()
	for _, mount := range mounts {
		mount.Deactivate()
	}
	for _, mount := range mounts {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := mount.Close(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func integrationComponent(instance string) extension.Component {
	return extension.Component{InstanceID: instance, Artifact: extension.Artifact{
		Name: instance, Version: "test", Hash: "rtk-integration-artifact", ConfigHash: "fixture-config", SourceKind: extension.SourceNative,
	}}
}

func integrationScript(fn func(context.Context, model.Request) ([]*einoschema.Message, error)) model.Streamer {
	return integrationStreamer(fn)
}

type integrationResolver struct{ streamer model.Streamer }

func (r integrationResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{Provider: model.Provider{ID: "rtk-integration-provider"}, Model: model.Descriptor{ID: "rtk-integration-model", ProviderID: "rtk-integration-provider"}, Streamer: r.streamer}, nil
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

func integrationToolCall(id, name, arguments string) []*einoschema.Message {
	return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{ID: id, Type: "function", Function: einoschema.FunctionCall{Name: name, Arguments: arguments}}})}
}

func latestIntegrationToolMessage(messages []*einoschema.Message) *einoschema.Message {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == einoschema.Tool {
			return messages[index]
		}
	}
	return nil
}

func allowIntegrationPolicy() permissions.Policy {
	return permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	})
}

func goTestResultFixture() []byte {
	var builder strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&builder, `{"Time":"2026-09-08T00:00:%02dZ","Action":"run","Package":"fixture/pkg","Test":"TestPass%03d"}`+"\n", i%60, i)
		fmt.Fprintf(&builder, `{"Time":"2026-09-08T00:00:%02dZ","Action":"pass","Package":"fixture/pkg","Test":"TestPass%03d","Elapsed":0.001}`+"\n", i%60, i)
	}
	builder.WriteString(`{"Time":"2026-09-08T00:01:00Z","Action":"output","Package":"fixture/pkg","Output":"FAILURE_SENTINEL\n"}` + "\n")
	builder.WriteString(`{"Time":"2026-09-08T00:01:01Z","Action":"fail","Package":"fixture/pkg","Elapsed":1.0}` + "\n")
	return []byte(`{"stdout":` + strconvQuote(builder.String()) + `,"stderr":"KEEP_STDERR_SENTINEL","exit_code":9007199254740993123456789,"unknown":{"keep":"UNKNOWN_SENTINEL"}}`)
}

func verboseDiffFixture() string {
	var builder strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&builder, "diff --git a/file%d.go b/file%d.go\nindex 1111111..2222222 100644\n--- a/file%d.go\n+++ b/file%d.go\n@@ -1,3 +1,3 @@\n-package old%d\n+package new%d\n KEEP_DIFF_SENTINEL\n", i, i, i, i, i, i)
	}
	return builder.String()
}

func verboseLogFixture() string {
	var builder strings.Builder
	for i := 0; i < 800; i++ {
		fmt.Fprintf(&builder, "2026-09-08T00:00:%02dZ INFO worker=%d repeated log event KEEP_LOG_SENTINEL\n", i%60, i)
	}
	return builder.String()
}

func strconvQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func bytesEqualUnselectedJSON(t *testing.T, original, reduced []byte, fields ...string) bool {
	t.Helper()
	var left, right map[string]json.RawMessage
	if err := json.Unmarshal(original, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(reduced, &right); err != nil {
		t.Fatal(err)
	}
	for _, field := range fields {
		if string(left[field]) != string(right[field]) {
			return false
		}
	}
	return true
}

func writeCountingRTK(t *testing.T, counter string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rtk")
	script := "#!/bin/sh\nprintf x >> " + shellQuote(counter) + "\nprintf reduced\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func countFile(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(raw)
}

func fixtureOptionsForExecutable(t *testing.T, executable string) Options {
	t.Helper()
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	options.ExecutablePath = executable
	options.ExecutableSHA256 = hex.EncodeToString(digest[:])
	options.TempRoot = root
	options.Bindings[0].ToolName = integrationToolName
	options.Bindings[0].Mode = JSONMirror
	options.Bindings[0].MatchInput = &InputMatch{Field: "cmd", Equals: []string{"go test -json ./..."}}
	options.Bindings[0].Fields = []Field{{Name: "stdout", Filter: Log}}
	return options
}
