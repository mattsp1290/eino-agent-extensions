// Command command-guard demonstrates a credential-free guard/runtime journey.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent-extensions/commandguard"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/config"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
	"github.com/mattsp1290/eino-agent/tools"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := runExample(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("allowed: executed\ndenied: rule-match")
}

type exampleResult struct {
	Executions      int32
	Allowed, Denied session.ToolCall
	NextRequest     string
}
type commandInput struct {
	Command string `json:"command"`
}

func runExample(ctx context.Context) (result exampleResult, err error) {
	directory, err := os.MkdirTemp("", "command-guard-example-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(directory)
	db, err := store.Open(ctx, filepath.Join(directory, "session.db"))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		return result, err
	}
	var mounts []*composition.Mount
	defer func() {
		for i := len(mounts) - 1; i >= 0; i-- {
			mounts[i].Deactivate()
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			err = errors.Join(err, mounts[i].Close(closeCtx))
			cancel()
		}
	}()
	var executions atomic.Int32
	toolMount, err := registry.Mount(ctx, component("example-command-tool", "scripted-tool-v1"), composition.InstallerFunc(func(_ context.Context, r *composition.Registrar) error {
		return r.Tool(composition.ToolRegistration{ID: "example-command", Scope: extension.GlobalScope(), Definition: tools.Definition{Name: "host_command", Description: "Harmless host command recorder; does not spawn processes", Execute: tools.TypedExecutor(func(_ context.Context, _ tools.TypedExecution[commandInput]) (map[string]string, error) {
			executions.Add(1)
			return map[string]string{"outcome": "executed"}, nil
		}), Retention: runtime.RetentionPolicy{MaxInlineBytes: 4096}}})
	}))
	if err != nil {
		return result, err
	}
	mounts = append(mounts, toolMount)
	bindings := append(commandguard.DefaultBindings(), commandguard.Binding{ToolName: "host_command", CommandField: "command"})
	guardMount, err := commandguard.Mount(ctx, registry, component("example-command-guard", ""), commandguard.Options{
		Bindings: bindings, Rules: []commandguard.Rule{{ID: "host-git-push", Executable: "git", ArgPrefix: []string{"push"}}},
		Limits: commandguard.Limits{MaxBindings: 8, MaxRules: 32, MaxRuleBytes: 2048, MaxPrefixArgs: 16, MaxRawInputBytes: 16384, MaxJSONDepth: 16, MaxJSONNodes: 256, MaxCommandBytes: 4096, MaxAnalysisBytes: 8192, MaxASTNodes: 2048, MaxASTDepth: 64, MaxWords: 512, MaxWordBytes: 4096, MaxWrapperDepth: 8, MaxInFlight: 4},
	})
	if err != nil {
		return result, err
	}
	mounts = append(mounts, guardMount)
	streamer := &scriptedModel{}
	orchestrator, err := runtime.NewStreamingOrchestrator(runtime.WithStore(db), runtime.WithRunPlanProvider(registry), runtime.WithModelResolver(resolver{streamer}), runtime.WithIDGenerator(&ids{}), runtime.WithOwnerID("example-owner"), runtime.WithClock(time.Now), runtime.WithQueueSize(16))
	if err != nil {
		return result, err
	}
	selection := model.Selection{ProviderID: "scripted", ModelID: "scripted"}
	handle, err := orchestrator.Start(ctx, runtime.Request{SessionID: "example-session", Message: runtime.UserMessage{Content: "demonstrate allowed and denied commands"}, Config: config.Snapshot{Agent: config.Agent{Name: "example", Model: selection}, Model: selection, Metadata: map[string]string{"workspace_id": "example", "workspace_root": directory}}})
	if err != nil {
		return result, err
	}
	// The runtime releases its acquired plan before signaling completion.
	done := <-handle.Done()
	if done.Error != nil {
		return result, done.Error
	}
	if done.Status != session.RunCompleted {
		return result, errors.New("example run did not complete")
	}
	result.Executions = executions.Load()
	result.NextRequest = streamer.nextRequest
	result.Allowed, err = db.GetToolCall(ctx, "example-allowed")
	if err != nil {
		return result, err
	}
	result.Denied, err = db.GetToolCall(ctx, "example-denied")
	if err != nil {
		return result, err
	}
	if result.Executions != 1 || result.Allowed.Status != session.ToolCallCompleted || result.Denied.Status != session.ToolCallFailed || !strings.Contains(result.NextRequest, "command policy denied: rule-match") {
		return result, errors.New("example enforcement verification failed")
	}
	return result, nil
}
func component(name, hash string) extension.Component {
	return extension.Component{InstanceID: name, Artifact: extension.Artifact{Name: name, Version: "example-v1", Hash: "host-example-artifact-v1", ConfigHash: hash, SourceKind: extension.SourceNative}}
}

type resolver struct{ streamer model.Streamer }

func (r resolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{Provider: model.Provider{ID: "scripted"}, Model: model.Descriptor{ID: "scripted", ProviderID: "scripted"}, Streamer: r.streamer}, nil
}

type scriptedModel struct {
	turn        int
	nextRequest string
}

func (s *scriptedModel) StreamProvider(_ context.Context, request model.Request) (*einoschema.StreamReader[model.StreamDelta], error) {
	var message *einoschema.Message
	if s.turn < 2 {
		id, cmd := "example-allowed", "git status"
		if s.turn == 1 {
			id, cmd = "example-denied", "git push"
		}
		raw, _ := json.Marshal(commandInput{Command: cmd})
		message = einoschema.AssistantMessage("", []einoschema.ToolCall{{ID: id, Type: "function", Function: einoschema.FunctionCall{Name: "host_command", Arguments: string(raw)}}})
	} else {
		raw, _ := json.Marshal(request.Messages)
		s.nextRequest = string(raw)
		message = einoschema.AssistantMessage("done", nil)
	}
	s.turn++
	reader, writer := einoschema.Pipe[model.StreamDelta](1)
	writer.Send(model.StreamDelta{Message: message}, nil)
	writer.Close()
	return reader, nil
}

type ids struct{ n atomic.Int64 }

func (i *ids) next(prefix string) string         { return fmt.Sprintf("%s-%d", prefix, i.n.Add(1)) }
func (i *ids) NewRunID() session.RunID           { return session.RunID(i.next("run")) }
func (i *ids) NewMessageID() session.MessageID   { return session.MessageID(i.next("message")) }
func (i *ids) NewPartID() session.PartID         { return session.PartID(i.next("part")) }
func (i *ids) NewToolCallID() session.ToolCallID { return session.ToolCallID(i.next("call")) }
func (i *ids) NewEventID() session.EventID       { return session.EventID(i.next("event")) }
func (i *ids) NewEpochID() session.EpochID       { return session.EpochID(i.next("epoch")) }
