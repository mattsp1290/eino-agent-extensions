// Command background-jobs demonstrates credential-free polling through a real
// frozen Eino composition plan. Linux and Darwin are the supported hosts.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/mattsp1290/eino-agent-extensions/backgroundjobs"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	agentruntime "github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func main() {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		fmt.Println("background jobs are unsupported on this platform")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := runBackgroundJob(ctx, ".", `printf 'example complete'`, nil)
	if err != nil {
		panic(err)
	}
	fmt.Printf("job %s: %s (%s)\n", status.ID, status.State, status.Stdout.Text)
}

func runBackgroundJob(ctx context.Context, workspace, command string, timeoutSeconds *int64) (backgroundjobs.StatusResult, error) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		return backgroundjobs.StatusResult{}, err
	}
	component := extension.Component{InstanceID: "example-background-jobs", Artifact: extension.Artifact{
		Name: "background-jobs", Version: "example", Hash: "host-supplied-artifact-hash", SourceKind: extension.SourceNative,
	}}
	mount, err := backgroundjobs.Mount(ctx, registry, component, backgroundjobs.Options{
		ShellPath: "/bin/sh", ShellIdentity: "example-system-sh-v1",
		Environment: backgroundjobs.Environment{Mode: backgroundjobs.EnvironmentExplicitOnly, Identity: "example-environment-v1", Overrides: map[string]string{"PATH": "/usr/bin:/bin"}},
		Limits: backgroundjobs.Limits{
			MaxRunning: 2, MaxTracked: 4, MaxCommandBytes: 4096, MaxWorkingDirectoryBytes: 1024,
			MaxOutputBytesPerStream: 4096, MaxEnvironmentEntries: 16, MaxEnvironmentBytes: 4096,
			DefaultTimeout: 0, MaxTimeout: 5 * time.Second, TerminateGrace: 50 * time.Millisecond, KillWait: time.Second,
		},
	})
	if err != nil {
		return backgroundjobs.StatusResult{}, err
	}
	defer func() {
		mount.Deactivate()
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = mount.Close(closeCtx)
	}()
	plan, err := registry.AcquireRunPlan(ctx, agentruntime.RunPlanRequest{SessionID: "example-session"})
	if err != nil {
		return backgroundjobs.StatusResult{}, err
	}
	defer plan.Release()
	tools, err := plan.ResolveTools(ctx, agentruntime.ToolScopeContext{SessionID: "example-session", WorkspaceID: "example-workspace", WorkspaceRoot: workspace})
	if err != nil {
		return backgroundjobs.StatusResult{}, err
	}
	start := findTool(tools, backgroundjobs.StartToolName)
	statusTool := findTool(tools, backgroundjobs.StatusToolName)
	if start == nil || statusTool == nil {
		return backgroundjobs.StatusResult{}, errors.New("background job tools unavailable")
	}
	input := map[string]any{"command": command}
	if timeoutSeconds != nil {
		input["timeout_seconds"] = *timeoutSeconds
	}
	raw, _ := json.Marshal(input)
	canonical, err := start.InputDecoder.DecodeToolInput(ctx, raw)
	if err != nil {
		return backgroundjobs.StatusResult{}, err
	}
	started, err := start.Executor.Execute(ctx, exampleCall("example-start", backgroundjobs.StartToolName, canonical, workspace))
	if err != nil {
		return backgroundjobs.StatusResult{}, err
	}
	var receipt backgroundjobs.StartResult
	if err := json.Unmarshal(started.Structured, &receipt); err != nil {
		return backgroundjobs.StatusResult{}, err
	}
	for {
		idRaw, _ := json.Marshal(map[string]string{"id": receipt.ID})
		canonicalID, err := statusTool.InputDecoder.DecodeToolInput(ctx, idRaw)
		if err != nil {
			return backgroundjobs.StatusResult{}, err
		}
		output, err := statusTool.Executor.Execute(ctx, exampleCall("example-status", backgroundjobs.StatusToolName, canonicalID, workspace))
		if err != nil {
			return backgroundjobs.StatusResult{}, err
		}
		var status backgroundjobs.StatusResult
		if err := json.Unmarshal(output.Structured, &status); err != nil {
			return backgroundjobs.StatusResult{}, err
		}
		if status.State != backgroundjobs.JobRunning {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return backgroundjobs.StatusResult{}, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func findTool(tools []agentruntime.Tool, name string) *agentruntime.Tool {
	for index := range tools {
		if tools[index].Name == name {
			return &tools[index]
		}
	}
	return nil
}

func exampleCall(id, name string, input []byte, workspace string) agentruntime.ToolCall {
	return agentruntime.ToolCall{
		ID: session.ToolCallID(id), SessionID: "example-session", Name: name, Input: input,
		Context: agentruntime.ToolContext{Turn: agentruntime.BoundedTurnMetadata{SessionID: "example-session"}, WorkspaceID: "example-workspace", WorkspaceRoot: workspace},
	}
}
