// Command rtk-reducer demonstrates an explicit, trusted RTK reducer mount.
// It uses a synthetic JSON-native tool, a real Eino registry/orchestrator, and
// a temporary SQLite store; it never executes a command in the caller's
// workspace.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent-extensions/rtkreducer"
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
	syntheticToolName   = "synthetic_go_test"
	syntheticPermission = "example.synthetic.go"
	syntheticCallID     = "example-rtk-reducer-call"
)

func main() {
	rtkPath := flag.String("rtk", "", "absolute path to the trusted RTK v0.48.0 executable")
	rtkSHA256 := flag.String("rtk-sha256", "", "lowercase SHA-256 digest of that executable")
	flag.Parse()
	if err := runExample(context.Background(), *rtkPath, *rtkSHA256, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runExample(ctx context.Context, rtkPath, rtkSHA256 string, output io.Writer) (resultErr error) {
	if !filepath.IsAbs(rtkPath) {
		return errors.New("-rtk must be an absolute RTK v0.48.0 executable path")
	}
	if len(rtkSHA256) != sha256.Size*2 || rtkSHA256 != strings.ToLower(rtkSHA256) {
		return errors.New("-rtk-sha256 must be a lowercase 64-character SHA-256 digest")
	}
	if _, err := hex.DecodeString(rtkSHA256); err != nil {
		return errors.New("-rtk-sha256 must be hexadecimal")
	}

	tempRoot, err := os.MkdirTemp("", "rtk-reducer-example-")
	if err != nil {
		return err
	}
	tempRoot, err = filepath.EvalSymlinks(tempRoot)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(tempRoot)) }()

	registry, err := composition.NewRegistry(nil)
	if err != nil {
		return err
	}
	synthetic := &syntheticTool{}
	component := extension.Component{
		InstanceID: "example-rtk-reducer",
		Artifact: extension.Artifact{
			Name: "rtk-reducer", Version: "example", Hash: "example-rtk-reducer-artifact-v1",
			SourceKind: extension.SourceNative,
		},
	}
	mount, err := rtkreducer.Mount(ctx, registry, component, rtkreducer.Options{
		ExecutablePath: rtkPath, ExecutableSHA256: rtkSHA256, TempRoot: tempRoot,
		Bindings: []rtkreducer.Binding{{
			ToolName: syntheticToolName, Mode: rtkreducer.JSONMirror,
			MatchInput: &rtkreducer.InputMatch{Field: "cmd", Equals: []string{"synthetic-go-test"}},
			Fields:     []rtkreducer.Field{{Name: "stdout", Filter: rtkreducer.GoTest}},
		}},
		Limits: rtkreducer.Limits{
			MaxBindings: 8, MaxFieldsPerBinding: 2, MaxInputMatches: 4,
			MaxConfigBytes: 16 << 10, MaxInputBytes: 8 << 10,
			MaxResultBytes: 256 << 10, MaxFieldBytes: 128 << 10,
			MaxJSONDepth: 16, MaxJSONNodes: 4096, MaxConcurrent: 2,
			MaxStdoutBytes: 64 << 10, MaxStderrBytes: 4 << 10,
			Timeout: 5 * time.Second, KillWait: time.Second,
		},
	})
	if err != nil {
		return err
	}
	defer func() {
		mount.Deactivate()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, mount.Close(closeCtx))
	}()

	toolMount, err := mountSyntheticTool(ctx, registry, synthetic)
	if err != nil {
		return err
	}
	defer func() {
		toolMount.Deactivate()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, toolMount.Close(closeCtx))
	}()

	database, err := store.Open(ctx, filepath.Join(tempRoot, "example.sqlite"))
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, database.Close()) }()

	streamer := &syntheticStreamer{}
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(exampleResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(permissions.PolicyFunc(func(_ context.Context, request permissions.Request) (permissions.Decision, error) {
			if request.Permission != syntheticPermission || request.ToolName != syntheticToolName {
				return permissions.Decision{}, errors.New("unexpected synthetic permission request")
			}
			return permissions.Decision{Action: permissions.ActionAllow}, nil
		})),
		runtime.WithIDGenerator(&exampleIDs{}), runtime.WithClock(time.Now),
		runtime.WithOwnerID("example-rtk-reducer"), runtime.WithQueueSize(4), runtime.WithLease(time.Second),
	)
	if err != nil {
		return err
	}
	selection := model.Selection{ProviderID: "example-provider", ModelID: "example-model"}
	snapshot := config.Snapshot{Agent: config.Agent{Name: "rtk-reducer-example", Model: selection}, Model: selection}
	handle, err := orchestrator.Start(ctx, runtime.Request{
		SessionID: "example-rtk-reducer-session", Message: runtime.UserMessage{Content: "run synthetic go test"}, Config: snapshot,
	})
	if err != nil {
		return err
	}
	select {
	case completed := <-handle.Done():
		if completed.Error != nil || completed.Status != session.RunCompleted {
			return fmt.Errorf("synthetic run failed: %v", completed.Error)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	call, err := database.GetToolCall(ctx, syntheticCallID)
	if err != nil {
		return err
	}
	var envelope runtime.ToolOutput
	if err := json.Unmarshal(call.Output, &envelope); err != nil {
		return err
	}
	if call.Status != session.ToolCallCompleted || synthetic.Count() != 1 || !streamer.SawToolResult() {
		return errors.New("synthetic reducer journey was incomplete")
	}
	if envelope.Structured == nil {
		return errors.New("synthetic result did not retain structured JSON")
	}
	var result syntheticResult
	if err := json.Unmarshal(envelope.Structured, &result); err != nil {
		return err
	}
	if result.Stdout == "" {
		return errors.New("synthetic result had no stdout")
	}
	originalResult, err := json.Marshal(syntheticOriginal())
	if err != nil {
		return err
	}
	originalStdoutBytes := len([]byte(syntheticOriginal().Stdout))
	originalResultBytes := len(originalResult)
	reducedResultBytes := len(envelope.Structured)
	marker := strings.Contains(result.Stdout, "[Reduced by RTK ")
	bounded := result.Stdout
	if len(bounded) > 240 {
		bounded = bounded[:240] + "..."
	}
	_, err = fmt.Fprintf(output, "original_stdout_bytes=%d original_result_bytes=%d reduced_result_bytes=%d reduced=%t result=%q\n", originalStdoutBytes, originalResultBytes, reducedResultBytes, marker, bounded)
	return err
}

func mountSyntheticTool(ctx context.Context, registry *composition.Registry, synthetic *syntheticTool) (*composition.Mount, error) {
	return registry.Mount(ctx, extension.Component{
		InstanceID: "example-synthetic-tool",
		Artifact:   extension.Artifact{Name: "synthetic-go-tool", Version: "example", Hash: "example-synthetic-tool-v1", ConfigHash: "example-synthetic-tool-config-v1", SourceKind: extension.SourceNative},
	}, composition.InstallerFunc(func(_ context.Context, registrar *composition.Registrar) error {
		return registrar.Tool(composition.ToolRegistration{
			ID: syntheticToolName, Order: runtime.OrderApplication, Scope: extension.GlobalScope(),
			Definition: syntheticToolDefinition(synthetic),
		})
	}))
}

func syntheticToolDefinition(synthetic *syntheticTool) tools.Definition {
	return tools.Definition{
		Name: syntheticToolName, Description: "Return deterministic synthetic go test events.",
		Parameters: einoschema.NewParamsOneOfByParams(map[string]*einoschema.ParameterInfo{
			"cmd": {Type: einoschema.String, Required: true, Desc: "The exact synthetic command label."},
		}),
		Pattern: func(_ context.Context, _ json.RawMessage) (string, error) { return syntheticToolName, nil },
		Execute: func(ctx context.Context, execution tools.Execution) (json.RawMessage, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var input struct {
				Cmd string `json:"cmd"`
			}
			if err := json.Unmarshal(execution.Input, &input); err != nil || input.Cmd != "synthetic-go-test" {
				return nil, errors.New("unexpected synthetic command")
			}
			synthetic.Increment()
			return json.Marshal(syntheticOriginal())
		},
		RetrySafe: true, Permissions: []string{syntheticPermission},
		Retention: runtime.RetentionPolicy{MaxInlineBytes: 128 << 10},
	}
}

type syntheticResult struct {
	Stdout    string          `json:"stdout"`
	Stderr    string          `json:"stderr"`
	ExitCode  int             `json:"exit_code"`
	BigNumber json.RawMessage `json:"big_number"`
}

func syntheticOriginal() syntheticResult {
	var events strings.Builder
	for index := 0; index < 96; index++ {
		fmt.Fprintf(&events, `{"Time":"2026-01-01T00:00:%02dZ","Action":"run","Package":"example.test","Test":"TestSynthetic%02d"}`+"\n", index%60, index)
		fmt.Fprintf(&events, `{"Time":"2026-01-01T00:00:%02dZ","Action":"output","Package":"example.test","Test":"TestSynthetic%02d","Output":"ok synthetic event %02d\n"}`+"\n", index%60, index, index)
		fmt.Fprintf(&events, `{"Time":"2026-01-01T00:00:%02dZ","Action":"pass","Package":"example.test","Test":"TestSynthetic%02d","Elapsed":0.01}`+"\n", index%60, index)
	}
	return syntheticResult{
		Stdout: events.String(), Stderr: "UNSELECTED_STDERR_SENTINEL", ExitCode: 0,
		BigNumber: json.RawMessage("90071992547409931234567890"),
	}
}

type syntheticTool struct {
	mu    sync.Mutex
	calls int
}

func (tool *syntheticTool) Count() int {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	return tool.calls
}

func (tool *syntheticTool) Increment() {
	tool.mu.Lock()
	tool.calls++
	tool.mu.Unlock()
}

type syntheticStreamer struct {
	mu       sync.Mutex
	observed bool
}

func (streamer *syntheticStreamer) StreamProvider(_ context.Context, request model.Request) (*einoschema.StreamReader[model.StreamDelta], error) {
	message := einoschema.AssistantMessage("", []einoschema.ToolCall{{
		ID: syntheticCallID, Type: "function",
		Function: einoschema.FunctionCall{Name: syntheticToolName, Arguments: `{"cmd":"synthetic-go-test"}`},
	}})
	for _, candidate := range request.Messages {
		if candidate.Role == einoschema.Tool {
			streamer.mu.Lock()
			streamer.observed = true
			streamer.mu.Unlock()
			message = einoschema.AssistantMessage("synthetic reducer result received", nil)
			break
		}
	}
	reader, writer := einoschema.Pipe[model.StreamDelta](1)
	go func() {
		defer writer.Close()
		writer.Send(model.StreamDelta{Message: message, Usage: model.UsageFromMessage(message)}, nil)
	}()
	return reader, nil
}

func (streamer *syntheticStreamer) SawToolResult() bool {
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	return streamer.observed
}

type exampleResolver struct{ streamer model.Streamer }

func (resolver exampleResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{Provider: model.Provider{ID: "example-provider"}, Model: model.Descriptor{ID: "example-model", ProviderID: "example-provider"}, Streamer: resolver.streamer}, nil
}

type exampleIDs struct {
	mu sync.Mutex
	n  int
}

func (ids *exampleIDs) next(prefix string) string {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.n++
	if prefix == "call" {
		return syntheticCallID
	}
	return fmt.Sprintf("%s-%d", prefix, ids.n)
}

func (ids *exampleIDs) NewRunID() session.RunID { return session.RunID(ids.next("run")) }
func (ids *exampleIDs) NewMessageID() session.MessageID {
	return session.MessageID(ids.next("message"))
}
func (ids *exampleIDs) NewPartID() session.PartID { return session.PartID(ids.next("part")) }
func (ids *exampleIDs) NewToolCallID() session.ToolCallID {
	return session.ToolCallID(ids.next("call"))
}
func (ids *exampleIDs) NewEventID() session.EventID { return session.EventID(ids.next("event")) }
func (ids *exampleIDs) NewEpochID() session.EpochID { return session.EpochID(ids.next("epoch")) }
