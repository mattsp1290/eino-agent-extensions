// Command ask-user demonstrates a credential-free, presentation-neutral host
// responder through a real frozen Eino composition plan.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mattsp1290/eino-agent-extensions/askuser"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
)

func main() {
	result, err := askSyntheticQuestion(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("ask_user: %s (%s)\n", result.Status, result.Answer)
}

type deterministicResponder struct {
	mu     sync.Mutex
	routed map[string]askuser.Request
}

func (responder *deterministicResponder) Respond(ctx context.Context, request askuser.Request) (askuser.Response, error) {
	if err := ctx.Err(); err != nil {
		return askuser.Response{}, err
	}
	responder.mu.Lock()
	responder.routed[string(request.ToolCallID)] = request
	responder.mu.Unlock()
	return askuser.Response{Kind: askuser.ResponseSelected, SelectedOption: 2}, nil
}

func askSyntheticQuestion(ctx context.Context) (askuser.Result, error) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		return askuser.Result{}, err
	}
	responder := &deterministicResponder{routed: make(map[string]askuser.Request)}
	mount, err := askuser.Mount(ctx, registry, extension.Component{
		InstanceID: "example-ask-user",
		Artifact: extension.Artifact{
			Name: "ask-user", Version: "example", Hash: "host-supplied-artifact-hash",
			SourceKind: extension.SourceNative,
		},
	}, askuser.Options{
		Responder: responder, ResponderIdentity: "example-deterministic-responder-v1",
		Limits: askuser.Limits{
			MaxQuestionBytes: 1024, MaxOptionLabelBytes: 128,
			MaxOptionDescriptionBytes: 512, MaxCustomAnswerBytes: 1024,
			MaxInFlight: 4, MaxWait: 30 * time.Second,
		},
	})
	if err != nil {
		return askuser.Result{}, err
	}
	defer func() {
		mount.Deactivate()
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = mount.Close(closeCtx)
	}()
	plan, err := registry.AcquireRunPlan(ctx, runtime.RunPlanRequest{SessionID: "example-session"})
	if err != nil {
		return askuser.Result{}, err
	}
	defer plan.Release()
	resolved, err := plan.ResolveTools(ctx, runtime.ToolScopeContext{SessionID: "example-session", WorkspaceID: "example-workspace"})
	if err != nil {
		return askuser.Result{}, err
	}
	if len(resolved) != 1 || resolved[0].Name != askuser.ToolName {
		return askuser.Result{}, fmt.Errorf("ask_user tool unavailable")
	}
	input, err := resolved[0].InputDecoder.DecodeToolInput(ctx, []byte(`{"question":"Which synthetic route should continue?","options":[{"label":"Canary","description":"Use the canary route."},{"label":"Stable","description":"Use the stable route."}]}`))
	if err != nil {
		return askuser.Result{}, err
	}
	output, err := resolved[0].Executor.Execute(ctx, runtime.ToolCall{
		ID: "example-call", SessionID: "example-session", RunID: "example-run",
		Name: askuser.ToolName, Input: input,
	})
	if err != nil {
		return askuser.Result{}, err
	}
	var result askuser.Result
	if err := json.Unmarshal(output.Structured, &result); err != nil {
		return askuser.Result{}, err
	}
	return result, nil
}
