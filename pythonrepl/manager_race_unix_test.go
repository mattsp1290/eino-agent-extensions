//go:build linux || darwin

package pythonrepl

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

type contextGate struct {
	entered chan struct{}
	release chan struct{}
}

func newContextGate() *contextGate {
	return &contextGate{entered: make(chan struct{}), release: make(chan struct{})}
}
func (gate *contextGate) hook(context.Context) { close(gate.entered); <-gate.release }

func TestCancellationDuringVenvCreationHasNoGeneration(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	gate := newContextGate()
	options.hooks = &testHooks{afterVenvDirectory: gate.hook}
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "venv-cancel", workspaceID: "w"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := manager.execute(ctx, owner, root, "x = 1"); done <- err }()
	<-gate.entered
	cancel()
	close(gate.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("venv cancellation=%v", err)
	}
	session := manager.owners[owner]
	if session.generation != 0 || session.venv != nil || session.runner != nil {
		t.Fatalf("state after venv cancellation: generation=%d venv=%t runner=%t", session.generation, session.venv != nil, session.runner != nil)
	}
	entries, err := os.ReadDir(options.tempRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial venv entries=%d err=%v", len(entries), err)
	}
}

func TestCancellationBeforeVenvPublicationRetainsFailedRemoval(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	ctx, cancel := context.WithCancel(context.Background())
	removeCalls := 0
	options.hooks = &testHooks{
		beforeVenvPublish: func(context.Context) { cancel() },
		removeVenv: func(path string) error {
			removeCalls++
			if removeCalls == 1 {
				return errors.New("synthetic removal failure")
			}
			return os.RemoveAll(path)
		},
	}
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "publish-cancel", workspaceID: "w"}
	if _, err := manager.execute(ctx, owner, root, "1"); !errors.Is(err, context.Canceled) || !errors.Is(err, errCleanupIncomplete) {
		t.Fatalf("publication cancellation=%v", err)
	}
	session := manager.owners[owner]
	if session.venv == nil || !session.venvInvalid || removeCalls != 1 {
		t.Fatalf("retained cleanup venv=%t invalid=%t removals=%d", session.venv != nil, session.venvInvalid, removeCalls)
	}
	partialPath := session.venv.path
	if _, err := manager.execute(context.Background(), owner, root, "6 * 7"); err != nil {
		t.Fatalf("retry execute=%v", err)
	}
	if _, err := os.Stat(partialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retained venv remains after retry: %v", err)
	}
	if session.venv == nil || session.venv.path == partialPath || session.venvInvalid || removeCalls != 2 {
		t.Fatalf("replacement state venv=%#v invalid=%t removals=%d", session.venv, session.venvInvalid, removeCalls)
	}
}

func TestVenvCreatorReapTimeoutBlocksOwnerReuse(t *testing.T) {
	public := testOptions(t)
	public.Limits.KillWait = 20 * time.Millisecond
	options, _ := canonicalize(public)
	release := make(chan struct{})
	options.hooks = &testHooks{beforeVenvCreatorWait: func() { <-release }}
	manager := newManager(options)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "creator-reap", workspaceID: "w"}
	if _, err := manager.execute(context.Background(), owner, root, "1"); !errors.Is(err, errCleanupIncomplete) {
		t.Fatalf("initial creator cleanup=%v", err)
	}
	session := manager.owners[owner]
	if session.venv == nil || !session.venvInvalid || session.runner != nil {
		t.Fatalf("cleanup debt venv=%t invalid=%t runner=%t", session.venv != nil, session.venvInvalid, session.runner != nil)
	}
	retainedPath := session.venv.path
	if _, err := manager.execute(context.Background(), owner, root, "2"); !errors.Is(err, errCleanupIncomplete) {
		t.Fatalf("reuse during creator cleanup=%v", err)
	}
	entries, err := os.ReadDir(options.tempRoot)
	if err != nil || len(entries) != 1 || session.venv == nil || session.venv.path != retainedPath {
		t.Fatalf("owner replacement entries=%d retained=%#v err=%v", len(entries), session.venv, err)
	}
	close(release)
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("cleanup retry=%v", err)
	}
	if _, err := os.Stat(retainedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retained venv remains: %v", err)
	}
}

func TestFirstExecutePublicationLinearizesBeforeConcurrentClear(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	gate := newContextGate()
	options.hooks = &testHooks{afterVenvDirectory: gate.hook}
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "execute-before-clear", workspaceID: "w"}
	type executeResponse struct {
		result ExecuteResult
		err    error
	}
	type clearResponse struct {
		result ClearResult
		err    error
	}
	executeDone := make(chan executeResponse, 1)
	go func() {
		result, err := manager.execute(context.Background(), owner, root, "x = 42")
		executeDone <- executeResponse{result, err}
	}()
	<-gate.entered
	clearDone := make(chan clearResponse, 1)
	go func() {
		result, err := manager.clear(context.Background(), owner, root)
		clearDone <- clearResponse{result, err}
	}()

	session := manager.owners[owner]
	deadline := time.Now().Add(time.Second)
	for {
		session.gate.mu.Lock()
		queued := len(session.gate.waiters)
		session.gate.mu.Unlock()
		if queued == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("clear did not queue behind published execute owner")
		}
	}
	close(gate.release)
	executed := <-executeDone
	if executed.err != nil || executed.result.Status != "completed" {
		t.Fatalf("first execute status=%s err=%v", executed.result.Status, executed.err)
	}
	cleared := <-clearDone
	if cleared.err != nil || !cleared.result.HadState || cleared.result.Generation != 1 {
		t.Fatalf("concurrent clear: had_state=%t generation=%d err=%v", cleared.result.HadState, cleared.result.Generation, cleared.err)
	}
}

func TestCancellationDuringSupervisorReadinessRetainsVenvWithoutGeneration(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	gate := newContextGate()
	hooks := &testHooks{afterSupervisorStart: gate.hook}
	options.hooks = hooks
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "start-cancel", workspaceID: "w"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := manager.execute(ctx, owner, root, "x = 1"); done <- err }()
	<-gate.entered
	cancel()
	close(gate.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("startup cancellation=%v", err)
	}
	session := manager.owners[owner]
	if session.generation != 0 || session.venv == nil || session.runner != nil || session.quarantined {
		t.Fatalf("state after startup cancellation: generation=%d venv=%t runner=%t quarantined=%t", session.generation, session.venv != nil, session.runner != nil, session.quarantined)
	}
	hooks.afterSupervisorStart = nil
	result, err := manager.execute(context.Background(), owner, root, "6 * 7")
	if err != nil || result.Result.Text != "42" || result.StateReset {
		t.Fatalf("startup retry=%#v err=%v", result, err)
	}
}

func TestCancellationAfterRequestWriteResetsGeneration(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	hooks := &testHooks{}
	options.hooks = hooks
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "write-cancel", workspaceID: "w"}
	if _, err := manager.execute(context.Background(), owner, root, "1"); err != nil {
		t.Fatal(err)
	}
	gate := newContextGate()
	hooks.afterRequestWrite = gate.hook
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := manager.execute(ctx, owner, root, "x = 9"); done <- err }()
	<-gate.entered
	cancel()
	close(gate.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("request-write cancellation=%v", err)
	}
	hooks.afterRequestWrite = nil
	result, err := manager.execute(context.Background(), owner, root, "x")
	if err != nil || result.Status != "python_error" || result.Generation != 1 || !result.StateReset || result.StateResetReason != "canceled" {
		t.Fatalf("post-write reset=%#v err=%v", result, err)
	}
}

func TestCancellationBeforeInitialRequestWriteHasNoGeneration(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	gate := newContextGate()
	hooks := &testHooks{beforeRequestWrite: gate.hook}
	options.hooks = hooks
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "initial-write-cancel", workspaceID: "w"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := manager.execute(ctx, owner, root, "x = 1"); done <- err }()
	<-gate.entered
	cancel()
	close(gate.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-write cancellation=%v", err)
	}
	session := manager.owners[owner]
	if session.generation != 0 || session.runner != nil || session.venv == nil {
		t.Fatalf("initial pre-write state: generation=%d venv=%t runner=%t", session.generation, session.venv != nil, session.runner != nil)
	}
	hooks.beforeRequestWrite = nil
	result, err := manager.execute(context.Background(), owner, root, "6 * 7")
	if err != nil || result.Result.Text != "42" || result.StateReset {
		t.Fatalf("pre-write retry=%#v err=%v", result, err)
	}
}

func TestCancellationBeforeWriteOnExistingRunnerAdvancesGeneration(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	hooks := &testHooks{}
	options.hooks = hooks
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "existing-write-cancel", workspaceID: "w"}
	if _, err := manager.execute(context.Background(), owner, root, "x = 1"); err != nil {
		t.Fatal(err)
	}
	gate := newContextGate()
	hooks.beforeRequestWrite = gate.hook
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := manager.execute(ctx, owner, root, "x = 2"); done <- err }()
	<-gate.entered
	cancel()
	close(gate.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("existing pre-write cancellation=%v", err)
	}
	hooks.beforeRequestWrite = nil
	result, err := manager.execute(context.Background(), owner, root, "x")
	if err != nil || result.Status != "python_error" || result.Generation != 1 || result.StateResetReason != "canceled" {
		t.Fatalf("existing pre-write reset=%#v err=%v", result, err)
	}
}

func TestCancellationWinsAtResponseCommitBoundary(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	hooks := &testHooks{}
	options.hooks = hooks
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "response-cancel", workspaceID: "w"}
	if _, err := manager.execute(context.Background(), owner, root, "1"); err != nil {
		t.Fatal(err)
	}
	gate := newContextGate()
	hooks.beforeResponseCommit = gate.hook
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := manager.execute(ctx, owner, root, "x = 11"); done <- err }()
	<-gate.entered
	cancel()
	close(gate.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("response cancellation=%v", err)
	}
	hooks.beforeResponseCommit = nil
	result, err := manager.execute(context.Background(), owner, root, "x")
	if err != nil || result.Status != "python_error" || result.Generation != 1 || result.StateResetReason != "canceled" {
		t.Fatalf("response-boundary reset=%#v err=%v", result, err)
	}
}

func TestCommittedResponseWinsLaterCancellation(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	hooks := &testHooks{}
	options.hooks = hooks
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "response-wins", workspaceID: "w"}
	gate := newContextGate()
	hooks.beforeResponseCommit = gate.hook
	ctx, cancel := context.WithCancel(context.Background())
	type response struct {
		result ExecuteResult
		err    error
	}
	done := make(chan response, 1)
	go func() { result, err := manager.execute(ctx, owner, root, "x = 13"); done <- response{result, err} }()
	<-gate.entered
	close(gate.release)
	completed := <-done
	cancel()
	if completed.err != nil || completed.result.Status != "completed" || completed.result.Generation != 0 {
		t.Fatalf("committed result=%#v err=%v", completed.result, completed.err)
	}
	hooks.beforeResponseCommit = nil
	result, err := manager.execute(context.Background(), owner, root, "x")
	if err != nil || result.Result.Text != "13" || result.Generation != 0 || result.StateReset {
		t.Fatalf("later cancellation reset committed state=%#v err=%v", result, err)
	}
}

func TestCloseDeadlineWhileReapBlockedIsRetryable(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	hooks := &testHooks{}
	options.hooks = hooks
	manager := newManager(options)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "reap-close", workspaceID: "w"}
	if _, err := manager.execute(context.Background(), owner, root, "1"); err != nil {
		t.Fatal(err)
	}
	venvPath := manager.owners[owner].venv.path
	entered, release := make(chan struct{}), make(chan struct{})
	hooks.beforeReapAuthorize = func() { close(entered); <-release }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close(ctx) }()
	<-entered
	if err := <-closeDone; !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errCleanupIncomplete) {
		t.Fatalf("short close=%v", err)
	}
	close(release)
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer retryCancel()
	if err := manager.Close(retryCtx); err != nil {
		t.Fatalf("retry close=%v", err)
	}
	if _, err := os.Stat(venvPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("venv remains after retry: %v", err)
	}
}

func TestConcurrentCloseJoinsSessionCleanup(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	manager := newManager(options)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "concurrent-close", workspaceID: "w"}
	if _, err := manager.execute(context.Background(), owner, root, "1"); err != nil {
		t.Fatal(err)
	}
	venvPath := manager.owners[owner].venv.path

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- manager.Close(context.Background())
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent close=%v", err)
		}
	}
	if _, err := os.Stat(venvPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("venv remains after concurrent close: %v", err)
	}
}
