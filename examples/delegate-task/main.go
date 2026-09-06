// Command delegate-task demonstrates a credential-free host Runner through a
// real frozen Eino composition plan. The synthetic Runner is not a sandbox or
// child agent.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mattsp1290/eino-agent-extensions/delegatetask"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
)

func main() {
	result, err := delegateSyntheticTask(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("delegate_task: %s (%s)\n", result.Status, result.Output)
}

func deterministicRunner(ctx context.Context, request delegatetask.Request) (delegatetask.Response, error) {
	if err := ctx.Err(); err != nil {
		return delegatetask.Response{}, err
	}
	if request.Profile != "read-only" {
		return delegatetask.Response{Status: delegatetask.ResponseRejected, Output: "unknown synthetic profile"}, nil
	}
	return delegatetask.Response{Status: delegatetask.ResponseCompleted, Output: "synthetic inspection complete"}, nil
}

func delegateSyntheticTask(ctx context.Context) (result delegatetask.Result, err error) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		return delegatetask.Result{}, err
	}
	mount, err := delegatetask.Mount(ctx, registry, extension.Component{
		InstanceID: "example-delegate-task",
		Artifact: extension.Artifact{
			Name: "delegate-task", Version: "example", Hash: "host-supplied-artifact-hash",
			SourceKind: extension.SourceNative,
		},
	}, delegatetask.Options{
		Runner: delegatetask.RunnerFunc(deterministicRunner), RunnerIdentity: "example-deterministic-runner-v1",
		Limits: delegatetask.Limits{
			MaxTaskBytes: 4096, MaxProfileBytes: 64, MaxResultBytes: 8192,
			MaxInFlight: 4, MaxWait: 30 * time.Second,
		},
	})
	if err != nil {
		return delegatetask.Result{}, err
	}
	defer func() {
		mount.Deactivate()
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = errors.Join(err, mount.Close(closeCtx))
	}()
	plan, err := registry.AcquireRunPlan(ctx, runtime.RunPlanRequest{SessionID: "example-session"})
	if err != nil {
		return delegatetask.Result{}, err
	}
	defer plan.Release()
	resolved, err := plan.ResolveTools(ctx, runtime.ToolScopeContext{
		SessionID: "example-session", WorkspaceID: "example-workspace", WorkspaceRoot: "/synthetic/workspace",
	})
	if err != nil {
		return delegatetask.Result{}, err
	}
	if len(resolved) != 1 || resolved[0].Name != delegatetask.ToolName {
		return delegatetask.Result{}, fmt.Errorf("delegate_task tool unavailable")
	}
	input, err := resolved[0].InputDecoder.DecodeToolInput(ctx, []byte(`{"task":"Inspect the synthetic project.","profile":"read-only"}`))
	if err != nil {
		return delegatetask.Result{}, err
	}
	output, err := resolved[0].Executor.Execute(ctx, runtime.ToolCall{
		ID: "example-call", SessionID: "example-session", RunID: "example-run",
		Name: delegatetask.ToolName, Input: input,
	})
	if err != nil {
		return delegatetask.Result{}, err
	}
	if err := json.Unmarshal(output.Structured, &result); err != nil {
		return delegatetask.Result{}, err
	}
	return result, nil
}
