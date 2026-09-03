package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent-extensions/pythonrepl"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/config"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
)

func main() {
	python := flag.String("python", "", "absolute Python 3.11-3.14 executable")
	flag.Parse()
	if err := runExample(context.Background(), *python, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runExample(ctx context.Context, pythonPath string, output io.Writer) (resultErr error) {
	if !filepath.IsAbs(pythonPath) {
		return errors.New("-python must be an absolute Python 3.11-3.14 executable")
	}
	tempRoot, err := os.MkdirTemp("", "python-repl-example-")
	if err != nil {
		return err
	}
	cleanupSafe := true
	defer func() {
		if cleanupSafe {
			resultErr = errors.Join(resultErr, os.RemoveAll(tempRoot))
		}
	}()
	workspace, err := os.MkdirTemp("", "python-repl-workspace-")
	if err != nil {
		return err
	}
	defer func() {
		if cleanupSafe {
			resultErr = errors.Join(resultErr, os.RemoveAll(workspace))
		}
	}()

	registry, err := composition.NewRegistry(nil)
	if err != nil {
		return err
	}
	component := extension.Component{InstanceID: "example-python-repl", Artifact: extension.Artifact{
		Name: "python-repl", Version: "1", Hash: "example-artifact-v1", SourceKind: extension.SourceNative,
	}}
	mount, err := pythonrepl.Mount(ctx, registry, component, pythonrepl.Options{
		PythonPath: pythonPath, PythonIdentity: "example-host-python-v1", TempRoot: tempRoot,
		Environment: pythonrepl.Environment{Identity: "example-empty-environment-v1", Entries: map[string]string{}},
		Limits: pythonrepl.Limits{
			MaxSessions: 2, MaxQueuedPerSession: 2, MaxCodeBytes: 8 << 10,
			MaxOutputBytesPerStream: 8 << 10, MaxResultBytes: 8 << 10, MaxExceptionBytes: 16 << 10,
			MaxEnvironmentEntries: 16, MaxEnvironmentBytes: 4 << 10,
			DefaultTimeout: 10 * time.Second, MaxTimeout: 30 * time.Second,
			VenvCreateTimeout: 10 * time.Second, RunnerStartTimeout: 5 * time.Second,
			TerminateGrace: 100 * time.Millisecond, KillWait: 3 * time.Second,
		},
	})
	if err != nil {
		return err
	}
	cleanupSafe = false
	defer func() {
		mount.Deactivate()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closeErr := mount.Close(closeCtx)
		if closeErr == nil {
			cleanupSafe = true
		}
		resultErr = errors.Join(resultErr, closeErr)
	}()

	database, err := store.Open(ctx, filepath.Join(tempRoot, "example.db"))
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, database.Close()) }()
	host := &scriptedHost{}
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(exampleResolver{streamer: host}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
			return permissions.Decision{Action: permissions.ActionAllow}, nil
		})),
		runtime.WithIDGenerator(&exampleIDs{}), runtime.WithClock(time.Now),
		runtime.WithOwnerID("python-repl-example"), runtime.WithQueueSize(8), runtime.WithLease(time.Second),
	)
	if err != nil {
		return err
	}
	selection := model.Selection{ProviderID: "example-provider", ModelID: "example-model"}
	snapshot := config.Snapshot{
		Agent: config.Agent{Name: "python-repl-example", Model: selection}, Model: selection,
		Metadata: map[string]string{"workspace_id": "example-workspace", "workspace_root": workspace},
	}

	assign, err := host.run(ctx, orchestrator, snapshot, pythonrepl.ExecuteToolName, `{"code":"x = 40"}`)
	if err != nil {
		return err
	}
	var assigned pythonrepl.ExecuteResult
	if err := decodeResult(assign, &assigned); err != nil {
		return err
	}
	read, err := host.run(ctx, orchestrator, snapshot, pythonrepl.ExecuteToolName, `{"code":"x + 2"}`)
	if err != nil {
		return err
	}
	var value pythonrepl.ExecuteResult
	if err := decodeResult(read, &value); err != nil {
		return err
	}
	if value.Result.Text != "42" {
		return errors.New("stateful Python result was not 42")
	}
	clearedRaw, err := host.run(ctx, orchestrator, snapshot, pythonrepl.ClearToolName, `{}`)
	if err != nil {
		return err
	}
	var cleared pythonrepl.ClearResult
	if err := decodeResult(clearedRaw, &cleared); err != nil {
		return err
	}
	missingRaw, err := host.run(ctx, orchestrator, snapshot, pythonrepl.ExecuteToolName, `{"code":"x"}`)
	if err != nil {
		return err
	}
	var missing pythonrepl.ExecuteResult
	if err := decodeResult(missingRaw, &missing); err != nil {
		return err
	}
	if missing.Status != "python_error" {
		return errors.New("clear did not discard Python globals")
	}
	_, err = fmt.Fprintf(output, "assigned=%s result=%s cleared=%t generation=%d after_clear=%s\n", assigned.Status, value.Result.Text, cleared.HadState, cleared.Generation, missing.Status)
	return err
}

type scriptedHost struct {
	mu        sync.Mutex
	toolName  string
	arguments string
	callID    string
	result    string
	done      chan struct{}
	counter   int
}

func (host *scriptedHost) run(ctx context.Context, orchestrator *runtime.StreamingOrchestrator, snapshot config.Snapshot, toolName, arguments string) (string, error) {
	host.mu.Lock()
	host.counter++
	host.toolName, host.arguments = toolName, arguments
	host.callID = fmt.Sprintf("example-call-%d", host.counter)
	host.result = ""
	host.done = make(chan struct{})
	done := host.done
	host.mu.Unlock()
	handle, err := orchestrator.Start(ctx, runtime.Request{SessionID: "example-session", Message: runtime.UserMessage{Content: "run scripted tool"}, Config: snapshot})
	if err != nil {
		return "", err
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunCompleted || result.Error != nil {
			return "", fmt.Errorf("run failed: %v", result.Error)
		}
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case <-done:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.result, nil
}

func (host *scriptedHost) StreamProvider(_ context.Context, request model.Request) (*einoschema.StreamReader[model.StreamDelta], error) {
	host.mu.Lock()
	toolName, arguments, callID := host.toolName, host.arguments, host.callID
	host.mu.Unlock()
	var message *einoschema.Message
	if latest := latestTool(request.Messages); latest != nil && latest.ToolCallID == callID {
		host.mu.Lock()
		host.result = latest.Content
		close(host.done)
		host.mu.Unlock()
		message = einoschema.AssistantMessage("scripted result received", nil)
	} else {
		message = einoschema.AssistantMessage("", []einoschema.ToolCall{{ID: callID, Type: "function", Function: einoschema.FunctionCall{Name: toolName, Arguments: arguments}}})
	}
	reader, writer := einoschema.Pipe[model.StreamDelta](1)
	go func() {
		defer writer.Close()
		writer.Send(model.StreamDelta{Message: message, Usage: model.UsageFromMessage(message)}, nil)
	}()
	return reader, nil
}

func latestTool(messages []*einoschema.Message) *einoschema.Message {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == einoschema.Tool {
			return messages[index]
		}
	}
	return nil
}

func decodeResult(raw string, destination any) error {
	var envelope struct {
		Structured json.RawMessage `json:"structured"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return err
	}
	return json.Unmarshal(envelope.Structured, destination)
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
