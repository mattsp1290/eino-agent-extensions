// Command web-search demonstrates a credential-free websearch.Searcher through
// a real Eino registry, permission policy, orchestrator, and in-memory SQLite
// store. The synthetic Searcher performs no network activity.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent-extensions/websearch"
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
	result, err := searchSyntheticSources(context.Background())
	if err != nil {
		panic(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(encoded))
}

type sequenceState struct {
	mu             sync.Mutex
	permissionSeen bool
	searchCalls    int
}

func searchSyntheticSources(ctx context.Context) (result websearch.Result, err error) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		return result, err
	}
	sequence := &sequenceState{}
	policy := permissions.PolicyFunc(func(_ context.Context, request permissions.Request) (permissions.Decision, error) {
		if request.Permission != websearch.PermissionSearch || request.Pattern != websearch.ToolName {
			return permissions.Decision{}, fmt.Errorf("unexpected synthetic permission")
		}
		sequence.mu.Lock()
		sequence.permissionSeen = true
		sequence.mu.Unlock()
		return permissions.Decision{Action: permissions.ActionAllow}, nil
	})
	searcher := websearch.SearcherFunc(func(ctx context.Context, query string) ([]websearch.Source, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sequence.mu.Lock()
		defer sequence.mu.Unlock()
		if !sequence.permissionSeen || sequence.searchCalls != 0 || query != "synthetic bounded search" {
			return nil, fmt.Errorf("synthetic ordering contract failed")
		}
		sequence.searchCalls++
		return []websearch.Source{
			{Title: "Synthetic source", URL: "https://example.test/source", Snippet: "Deterministic source record."},
			{Title: "Excluded credential URL", URL: "https://user:secret@example.test/private", Snippet: "This record is dropped."},
		}, nil
	})
	mount, err := websearch.Mount(ctx, registry, extension.Component{
		InstanceID: "example-web-search",
		Artifact: extension.Artifact{
			Name: "web-search", Version: "example", Hash: "host-supplied-artifact-hash",
			SourceKind: extension.SourceNative,
		},
	}, websearch.Options{
		Searcher: searcher, SearcherIdentity: "example-deterministic-searcher-v1",
		Limits: websearch.Limits{
			MaxQueryBytes: 4096, MaxResults: 4, MaxTitleBytes: 512,
			MaxURLBytes: 2048, MaxSnippetBytes: 4096, MaxInFlight: 2,
			MaxWait: 10 * time.Second,
		},
	})
	if err != nil {
		return result, err
	}
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		mount.Deactivate()
		_ = mount.Close(context.Background())
		return result, err
	}
	defer func() {
		mount.Deactivate()
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = errors.Join(err, mount.Close(closeCtx), database.Close())
	}()
	streamer := &syntheticStreamer{}
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(syntheticResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(policy),
		runtime.WithIDGenerator(&syntheticIDs{}), runtime.WithClock(time.Now),
		runtime.WithOwnerID("example-web-search-owner"), runtime.WithQueueSize(8), runtime.WithLease(time.Second),
	)
	if err != nil {
		return result, err
	}
	selection := model.Selection{ProviderID: "synthetic-provider", ModelID: "synthetic-model"}
	handle, err := orchestrator.Start(ctx, runtime.Request{
		SessionID: "example-web-search-session", Message: runtime.UserMessage{Content: "search synthetic sources"},
		Config: config.Snapshot{
			Agent: config.Agent{Name: "synthetic-agent", Model: selection}, Model: selection,
		},
	})
	if err != nil {
		return result, err
	}
	select {
	case completed := <-handle.Done():
		if completed.Error != nil || completed.Status != session.RunCompleted {
			return result, fmt.Errorf("synthetic run failed")
		}
	case <-ctx.Done():
		return result, ctx.Err()
	}
	call, err := database.GetToolCall(ctx, "example-web-search-call")
	if err != nil {
		return result, err
	}
	var output runtime.ToolOutput
	if err := json.Unmarshal(call.Output, &output); err != nil {
		return result, err
	}
	if err := json.Unmarshal(output.Structured, &result); err != nil {
		return result, err
	}
	sequence.mu.Lock()
	calls := sequence.searchCalls
	seen := sequence.permissionSeen
	sequence.mu.Unlock()
	if !seen || calls != 1 || !streamer.sawToolResult() {
		return result, fmt.Errorf("synthetic journey incomplete")
	}
	return result, nil
}

type syntheticResolver struct{ streamer model.Streamer }

func (resolver syntheticResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{
		Provider: model.Provider{ID: "synthetic-provider"},
		Model:    model.Descriptor{ID: "synthetic-model", ProviderID: "synthetic-provider"},
		Streamer: resolver.streamer,
	}, nil
}

type syntheticStreamer struct {
	mu       sync.Mutex
	observed bool
}

func (streamer *syntheticStreamer) StreamProvider(_ context.Context, request model.Request) (*einoschema.StreamReader[model.StreamDelta], error) {
	message := einoschema.AssistantMessage("", []einoschema.ToolCall{{
		ID: "example-web-search-call", Type: "function",
		Function: einoschema.FunctionCall{Name: websearch.ToolName, Arguments: `{"query":"synthetic bounded search"}`},
	}})
	for _, candidate := range request.Messages {
		if candidate.Role == einoschema.Tool {
			streamer.mu.Lock()
			streamer.observed = true
			streamer.mu.Unlock()
			message = einoschema.AssistantMessage("done", nil)
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

func (streamer *syntheticStreamer) sawToolResult() bool {
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	return streamer.observed
}

type syntheticIDs struct {
	mu sync.Mutex
	n  int
}

func (ids *syntheticIDs) next(prefix string) string {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.n++
	return fmt.Sprintf("%s-%d", prefix, ids.n)
}
func (ids *syntheticIDs) NewRunID() session.RunID { return session.RunID(ids.next("run")) }
func (ids *syntheticIDs) NewMessageID() session.MessageID {
	return session.MessageID(ids.next("message"))
}
func (ids *syntheticIDs) NewPartID() session.PartID { return session.PartID(ids.next("part")) }
func (ids *syntheticIDs) NewToolCallID() session.ToolCallID {
	return session.ToolCallID(ids.next("call"))
}
func (ids *syntheticIDs) NewEventID() session.EventID { return session.EventID(ids.next("event")) }
func (ids *syntheticIDs) NewEpochID() session.EpochID { return session.EpochID(ids.next("epoch")) }
