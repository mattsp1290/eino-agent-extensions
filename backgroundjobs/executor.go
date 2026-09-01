package backgroundjobs

import (
	"context"

	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/tools"
)

const (
	permissionStart = "background.process.start"
	permissionRead  = "background.process.read"
	permissionKill  = "background.process.kill"
)

func definitions(options canonicalOptions, manager *manager) []tools.Definition {
	return []tools.Definition{
		{
			Name:        StartToolName,
			Description: "Start a bounded non-interactive shell command in the background. The initial working directory is workspace-contained validation, not a sandbox.",
			Parameters:  toolParameters(startInput{}), Normalize: normalizeStart(options),
			Pattern: constantTypedPattern[startInput](permissionStart),
			Execute: tools.TypedExecutor[startInput, StartResult](func(ctx context.Context, execution tools.TypedExecution[startInput]) (StartResult, error) {
				owner, err := runtimeOwner(execution.Call, execution.Context, true)
				if err != nil {
					return StartResult{}, err
				}
				if err := ctx.Err(); err != nil {
					return StartResult{}, err
				}
				return manager.start(ctx, owner, execution.Context.WorkspaceRoot, execution.Input)
			}),
			RetrySafe: false, Permissions: []string{permissionStart}, Retention: options.retention[StartToolName],
		},
		{
			Name:        StatusToolName,
			Description: "Poll one owned background job and return its bounded stdout and stderr tails.",
			Parameters:  toolParameters(idInput{}), Normalize: normalizeID(options),
			Pattern: constantTypedPattern[idInput](permissionRead),
			Execute: tools.TypedExecutor[idInput, StatusResult](func(ctx context.Context, execution tools.TypedExecution[idInput]) (StatusResult, error) {
				owner, err := runtimeOwner(execution.Call, execution.Context, false)
				if err != nil {
					return StatusResult{}, err
				}
				if err := ctx.Err(); err != nil {
					return StatusResult{}, err
				}
				return manager.status(owner, execution.Input.ID)
			}),
			RetrySafe: true, Permissions: []string{permissionRead}, Retention: options.retention[StatusToolName],
		},
		{
			Name:        ListToolName,
			Description: "List bounded summaries for background jobs owned by this session and workspace.",
			Parameters:  toolParameters(listInput{}), Normalize: normalizeList,
			Pattern: constantTypedPattern[listInput](permissionRead),
			Execute: tools.TypedExecutor[listInput, ListResult](func(ctx context.Context, execution tools.TypedExecution[listInput]) (ListResult, error) {
				owner, err := runtimeOwner(execution.Call, execution.Context, false)
				if err != nil {
					return ListResult{}, err
				}
				if err := ctx.Err(); err != nil {
					return ListResult{}, err
				}
				return manager.list(owner), nil
			}),
			RetrySafe: true, Permissions: []string{permissionRead}, Retention: options.retention[ListToolName],
		},
		{
			Name:        KillToolName,
			Description: "Terminate one owned background job and wait for anchored process-group cleanup.",
			Parameters:  toolParameters(idInput{}), Normalize: normalizeID(options),
			Pattern: constantTypedPattern[idInput](permissionKill),
			Execute: tools.TypedExecutor[idInput, KillResult](func(ctx context.Context, execution tools.TypedExecution[idInput]) (KillResult, error) {
				owner, err := runtimeOwner(execution.Call, execution.Context, false)
				if err != nil {
					return KillResult{}, err
				}
				if err := ctx.Err(); err != nil {
					return KillResult{}, err
				}
				return manager.kill(ctx, owner, execution.Input.ID)
			}),
			RetrySafe: false, Permissions: []string{permissionKill}, Retention: options.retention[KillToolName],
		},
	}
}

func constantTypedPattern[I any](pattern string) tools.PermissionPattern {
	return tools.TypedPermissionPattern(func(ctx context.Context, _ I) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return pattern, nil
	})
}

func runtimeOwner(call runtime.ToolCall, executionContext runtime.ToolContext, requireRoot bool) (ownerKey, error) {
	if call.SessionID == "" || executionContext.WorkspaceID == "" {
		return ownerKey{}, runtimeError("owner")
	}
	if executionContext.Turn.SessionID != "" && executionContext.Turn.SessionID != call.SessionID {
		return ownerKey{}, runtimeError("owner")
	}
	if requireRoot && executionContext.WorkspaceRoot == "" {
		return ownerKey{}, runtimeError("workspace-root")
	}
	return ownerKey{sessionID: string(call.SessionID), workspaceID: executionContext.WorkspaceID}, nil
}
