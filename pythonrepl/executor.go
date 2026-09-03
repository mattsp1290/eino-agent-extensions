package pythonrepl

import (
	"context"
	"errors"
	"time"

	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/tools"
)

func definitions(options canonicalOptions, manager *manager) []tools.Definition {
	return []tools.Definition{
		{
			Name:        ExecuteToolName,
			Description: "Execute Python in state scoped to the durable session and workspace. Python has host-user authority and results are bounded.",
			Parameters:  toolParameters(executeInput{}), Normalize: normalizeExecute(options),
			Pattern: constantTypedPattern[executeInput](permissionExecute),
			Execute: tools.TypedExecutor[executeInput, ExecuteResult](func(ctx context.Context, execution tools.TypedExecution[executeInput]) (ExecuteResult, error) {
				key, root, err := runtimeOwner(execution.Call, execution.Context)
				if err != nil {
					return ExecuteResult{}, err
				}
				if err := ctx.Err(); err != nil {
					return ExecuteResult{}, err
				}
				effective := options.limits.DefaultTimeout
				if execution.Input.TimeoutSeconds != nil && *execution.Input.TimeoutSeconds > 0 {
					effective = time.Duration(*execution.Input.TimeoutSeconds) * time.Second
				}
				callCtx, cancel := context.WithTimeout(ctx, effective)
				defer cancel()
				result, executeErr := manager.execute(callCtx, key, root, execution.Input.Code)
				if executeErr != nil && errors.Is(executeErr, context.DeadlineExceeded) {
					if parentErr := ctx.Err(); parentErr != nil {
						return ExecuteResult{}, errors.Join(executeErr, parentErr)
					}
					return ExecuteResult{}, errors.Join(errExecutionTimedOut, context.DeadlineExceeded)
				}
				if executeErr != nil && errors.Is(executeErr, context.Canceled) && ctx.Err() != nil {
					return ExecuteResult{}, errors.Join(executeErr, ctx.Err())
				}
				return result, executeErr
			}),
			RetrySafe: false, Permissions: []string{permissionExecute}, Retention: options.retention[ExecuteToolName],
		},
		{
			Name:        ClearToolName,
			Description: "Discard live Python interpreter state for the durable session and workspace without recreating it.",
			Parameters:  toolParameters(clearInput{}), Normalize: normalizeClear,
			Pattern: constantTypedPattern[clearInput](permissionManage),
			Execute: tools.TypedExecutor[clearInput, ClearResult](func(ctx context.Context, execution tools.TypedExecution[clearInput]) (ClearResult, error) {
				key, root, err := runtimeOwner(execution.Call, execution.Context)
				if err != nil {
					return ClearResult{}, err
				}
				if err := ctx.Err(); err != nil {
					return ClearResult{}, err
				}
				return manager.clear(ctx, key, root)
			}),
			RetrySafe: false, Permissions: []string{permissionManage}, Retention: options.retention[ClearToolName],
		},
	}
}

func runtimeOwner(call runtime.ToolCall, executionContext runtime.ToolContext) (ownerKey, string, error) {
	if call.SessionID == "" || executionContext.WorkspaceID == "" || executionContext.WorkspaceRoot == "" {
		return ownerKey{}, "", runtimeError("owner")
	}
	if executionContext.Turn.SessionID != "" && executionContext.Turn.SessionID != call.SessionID {
		return ownerKey{}, "", runtimeError("owner")
	}
	root, err := canonicalWorkspaceRoot(executionContext.WorkspaceRoot)
	if err != nil {
		return ownerKey{}, "", err
	}
	return ownerKey{sessionID: string(call.SessionID), workspaceID: executionContext.WorkspaceID}, root, nil
}
