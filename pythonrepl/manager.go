package pythonrepl

import (
	"context"
	"errors"
	"sync"
	"time"
)

type manager struct {
	mu      sync.Mutex
	options canonicalOptions
	owners  map[ownerKey]*ownerSession
	closing bool
	closed  bool
}

func newManager(options canonicalOptions) *manager {
	return &manager{options: options, owners: make(map[ownerKey]*ownerSession)}
}

func (manager *manager) execute(ctx context.Context, key ownerKey, workspaceRoot, code string) (ExecuteResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecuteResult{}, err
	}
	session, err := manager.ownerForExecute(ctx, key, workspaceRoot)
	if err != nil {
		return ExecuteResult{}, err
	}
	opCtx, cancel := session.operationContext(ctx)
	defer cancel()
	release, err := session.gate.acquire(opCtx)
	if err != nil {
		if session.lifecycle.Err() != nil {
			return ExecuteResult{}, errManagerClosing
		}
		return ExecuteResult{}, err
	}
	defer release()
	if session.closed {
		return ExecuteResult{}, errManagerClosing
	}
	if session.quarantined {
		return ExecuteResult{}, errOwnerQuarantined
	}
	if session.venvInvalid {
		if err := removeVenv(session.venv); err != nil {
			return ExecuteResult{}, err
		}
		session.venv = nil
		session.venvInvalid = false
	}
	if err := opCtx.Err(); err != nil {
		if session.lifecycle.Err() != nil {
			return ExecuteResult{}, errManagerClosing
		}
		return ExecuteResult{}, err
	}

	if session.venv == nil {
		venvCtx, venvCancel := withCeiling(opCtx, manager.options.limits.VenvCreateTimeout)
		venv, createErr := createVenv(venvCtx, manager.options)
		venvCancel()
		if createErr != nil {
			if venv != nil {
				session.venv = venv
				session.venvInvalid = true
			}
			if session.lifecycle.Err() != nil {
				return ExecuteResult{}, errManagerClosing
			}
			return ExecuteResult{}, createErr
		}
		if err := opCtx.Err(); err != nil {
			_ = removeVenv(venv)
			return ExecuteResult{}, err
		}
		session.venv = venv
	}

	startedNow := false
	if session.runner == nil {
		startCtx, startCancel := withCeiling(opCtx, manager.options.limits.RunnerStartTimeout)
		runner, startErr := startRunner(startCtx, manager.options, session.venv.interpreter, session.workspaceRoot)
		startCancel()
		if startErr != nil {
			if runner != nil {
				session.runner = runner
				session.quarantined = true
				return ExecuteResult{}, errors.Join(startErr, errCleanupIncomplete)
			}
			if errors.Is(startErr, errBootstrapIntegrity) {
				session.venvInvalid = true
				if removeErr := removeVenv(session.venv); removeErr != nil {
					return ExecuteResult{}, errors.Join(startErr, removeErr)
				}
				session.venv = nil
				session.venvInvalid = false
			}
			if session.lifecycle.Err() != nil {
				return ExecuteResult{}, errManagerClosing
			}
			return ExecuteResult{}, startErr
		}
		session.runner = runner
		startedNow = true
	}

	outcome, executeErr := session.runner.execute(opCtx, code)
	if executeErr != nil {
		reason := "runner_failed"
		if errors.Is(executeErr, context.Canceled) {
			reason = "canceled"
		} else if errors.Is(executeErr, context.DeadlineExceeded) {
			reason = "timed_out"
		}
		resetErr := manager.resetRunner(session, outcome.mayHaveExecuted || !startedNow, reason, nil)
		if resetErr != nil {
			return ExecuteResult{}, errors.Join(executeErr, resetErr)
		}
		if session.lifecycle.Err() != nil && ctx.Err() == nil {
			return ExecuteResult{}, errManagerClosing
		}
		return ExecuteResult{}, executeErr
	}

	result := ExecuteResult{
		Status: outcome.response.Status, Stdout: outcome.response.Stdout, Stderr: outcome.response.Stderr,
		Result: outcome.response.Result, Exception: outcome.response.Exception, Generation: session.generation,
	}
	if session.pendingReason != "" {
		result.StateReset = true
		result.StateResetReason = session.pendingReason
		session.pendingReason = ""
	}
	return result, nil
}

func (manager *manager) clear(ctx context.Context, key ownerKey, workspaceRoot string) (ClearResult, error) {
	manager.mu.Lock()
	if manager.closing || manager.closed {
		manager.mu.Unlock()
		return ClearResult{}, errManagerClosing
	}
	if err := ctx.Err(); err != nil {
		manager.mu.Unlock()
		return ClearResult{}, err
	}
	session := manager.owners[key]
	if session == nil {
		manager.mu.Unlock()
		return ClearResult{}, nil
	}
	if session.workspaceRoot != workspaceRoot {
		manager.mu.Unlock()
		return ClearResult{}, runtimeError("workspace-root-mismatch")
	}
	manager.mu.Unlock()

	opCtx, cancel := session.operationContext(ctx)
	defer cancel()
	release, err := session.gate.acquire(opCtx)
	if err != nil {
		if session.lifecycle.Err() != nil {
			return ClearResult{}, errManagerClosing
		}
		return ClearResult{}, err
	}
	defer release()
	if session.quarantined {
		return ClearResult{}, errOwnerQuarantined
	}
	if session.closed {
		return ClearResult{}, errManagerClosing
	}
	if err := opCtx.Err(); err != nil {
		if session.lifecycle.Err() != nil {
			return ClearResult{}, errManagerClosing
		}
		return ClearResult{}, err
	}
	session.pendingReason = ""
	if session.runner == nil {
		return ClearResult{Generation: session.generation}, nil
	}
	if err := manager.resetRunner(session, true, "cleared", nil); err != nil {
		return ClearResult{}, err
	}
	// Clear reports its reset directly, so it consumes the pending notice.
	session.pendingReason = ""
	return ClearResult{HadState: true, Generation: session.generation}, nil
}

func (manager *manager) ownerForExecute(ctx context.Context, key ownerKey, workspaceRoot string) (*ownerSession, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closing || manager.closed {
		return nil, errManagerClosing
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session := manager.owners[key]; session != nil {
		if session.workspaceRoot != workspaceRoot {
			return nil, runtimeError("workspace-root-mismatch")
		}
		return session, nil
	}
	if len(manager.owners) >= manager.options.limits.MaxSessions {
		return nil, errCapacityExhausted
	}
	session := newOwnerSession(key, workspaceRoot, manager.options.limits.MaxQueuedPerSession)
	manager.owners[key] = session
	return session, nil
}

func (manager *manager) resetRunner(session *ownerSession, invalidate bool, reason string, waitCtx context.Context) error {
	if session.runner == nil {
		return nil
	}
	runner := session.runner
	done := runner.terminate()
	if waitCtx == nil {
		maximum := manager.options.limits.TerminateGrace + 2*manager.options.limits.KillWait + time.Second
		timer := time.NewTimer(maximum)
		select {
		case <-done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			session.quarantined = true
			return errCleanupIncomplete
		}
	} else {
		select {
		case <-done:
		case <-waitCtx.Done():
			return errors.Join(errCleanupIncomplete, waitCtx.Err())
		}
	}
	if err := runner.terminationError(); err != nil {
		session.quarantined = true
		return err
	}
	session.runner = nil
	if invalidate {
		session.generation++
		if validResetReason(reason) && reason != "" {
			session.pendingReason = reason
		}
	}
	return nil
}

func (manager *manager) Close(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil
	}
	manager.closing = true
	owners := make([]*ownerSession, 0, len(manager.owners))
	for _, session := range manager.owners {
		session.cancel()
		owners = append(owners, session)
	}
	manager.mu.Unlock()

	var result error
	for _, session := range owners {
		if err := manager.closeSession(ctx, session); err != nil {
			result = errors.Join(result, err)
		}
	}
	if result != nil {
		return result
	}
	manager.mu.Lock()
	manager.closed = true
	manager.mu.Unlock()
	return nil
}

func (manager *manager) closeSession(ctx context.Context, session *ownerSession) error {
	release, err := session.gate.acquire(ctx)
	if err != nil {
		return errors.Join(errCleanupIncomplete, err)
	}
	defer release()
	if session.closed {
		return nil
	}
	if session.quarantined {
		return errCleanupIncomplete
	}
	if session.runner != nil {
		if err := manager.resetRunner(session, false, "", ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(errCleanupIncomplete, err)
	}
	if err := removeVenv(session.venv); err != nil {
		return err
	}
	session.venv = nil
	session.venvInvalid = false
	session.closed = true
	return nil
}
