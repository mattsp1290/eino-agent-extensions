//go:build linux || darwin

package pythonrepl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestManagerRealStateErrorAndClear(t *testing.T) {
	options, err := canonicalize(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	manager := newManager(options)
	workspace := t.TempDir()
	root, _ := canonicalWorkspaceRoot(workspace)
	owner := ownerKey{sessionID: "session", workspaceID: "workspace"}
	first, err := manager.execute(context.Background(), owner, root, "x = 40")
	if err != nil || first.Status != "completed" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := manager.execute(context.Background(), owner, root, "x + 2")
	if err != nil || second.Result.Text != "42" || second.Generation != 0 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	pythonError, err := manager.execute(context.Background(), owner, root, "print('before'); raise ValueError('expected')")
	if err != nil || pythonError.Status != "python_error" || pythonError.Stdout.Text != "before\n" || !strings.Contains(pythonError.Exception.Text, "ValueError: expected") {
		t.Fatalf("python error=%#v err=%v", pythonError, err)
	}
	if strings.Contains(pythonError.Exception.Text, "<string>") {
		t.Fatalf("exception exposed runner bootstrap frame: %q", pythonError.Exception.Text)
	}
	preserved, err := manager.execute(context.Background(), owner, root, "x")
	if err != nil || preserved.Result.Text != "40" {
		t.Fatalf("preserved=%#v err=%v", preserved, err)
	}
	venvPath := manager.owners[owner].venv.path
	cleared, err := manager.clear(context.Background(), owner, root)
	if err != nil || !cleared.HadState || cleared.Generation != 1 {
		t.Fatalf("clear=%#v err=%v", cleared, err)
	}
	missing, err := manager.execute(context.Background(), owner, root, "x")
	if err != nil || missing.Status != "python_error" || !strings.Contains(missing.Exception.Text, "NameError") || missing.StateReset {
		t.Fatalf("after clear=%#v err=%v", missing, err)
	}
	if manager.owners[owner].venv.path != venvPath {
		t.Fatal("clear replaced retained venv")
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(venvPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("venv remains: %v", err)
	}
}

func TestManagerCancellationResetsAndReportsOnce(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "cancel", workspaceID: "workspace"}
	ctx, cancel := context.WithCancel(context.Background())
	marker := root + "/mutated"
	executeDone := make(chan error, 1)
	go func() {
		_, executeErr := manager.execute(ctx, owner, root, fmt.Sprintf("x = 9\nopen(%q, 'w').write('ready')\nimport time\ntime.sleep(10)", marker))
		executeDone <- executeErr
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(marker); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mutation gate not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	err := <-executeDone
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	venvPath := manager.owners[owner].venv.path
	result, err := manager.execute(context.Background(), owner, root, "x")
	if err != nil || result.Status != "python_error" || !result.StateReset || result.StateResetReason != "canceled" || result.Generation != 1 {
		t.Fatalf("post-cancel=%#v err=%v", result, err)
	}
	if manager.owners[owner].venv.path != venvPath {
		t.Fatal("cancellation replaced venv")
	}
	again, err := manager.execute(context.Background(), owner, root, "1 + 1")
	if err != nil || again.StateReset || again.Result.Text != "2" {
		t.Fatalf("notice repeated: %#v %v", again, err)
	}
}

func TestManagerEnvironmentOutputBoundsAndOwnerIsolation(t *testing.T) {
	t.Setenv("PYTHON_REPL_AMBIENT", "ambient-marker")
	public := testOptions(t)
	public.Limits.MaxOutputBytesPerStream = 5
	public.Limits.MaxResultBytes = 5
	options, _ := canonicalize(public)
	public.Environment.Entries["PYTHON_REPL_EXPLICIT"] = "mutated-after-mount"
	manager := newManager(options)
	deferManagerClose(t, manager)
	rootA, _ := canonicalWorkspaceRoot(t.TempDir())
	rootB, _ := canonicalWorkspaceRoot(t.TempDir())
	a := ownerKey{sessionID: "a", workspaceID: "workspace"}
	b := ownerKey{sessionID: "b", workspaceID: "workspace"}
	result, err := manager.execute(context.Background(), a, rootA, "import os\nprint(os.environ.get('PYTHON_REPL_AMBIENT'))\nprint(os.environ['PYTHON_REPL_EXPLICIT'])\nos.environ['PYTHON_REPL_EXPLICIT']")
	if err != nil || !result.Stdout.Truncated || len(result.Stdout.Text) > 5 || !result.Result.Truncated || result.Result.Text != "'visi" || strings.Contains(result.Stdout.Text, "ambient-marker") {
		t.Fatalf("bounded environment result=%#v err=%v", result, err)
	}
	if _, err := manager.execute(context.Background(), a, rootA, "shared = 'a'"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.execute(context.Background(), b, rootB, "shared = 'b'"); err != nil {
		t.Fatal(err)
	}
	readA, _ := manager.execute(context.Background(), a, rootA, "shared")
	readB, _ := manager.execute(context.Background(), b, rootB, "shared")
	if readA.Result.Text != "'a'" || readB.Result.Text != "'b'" {
		t.Fatalf("owners shared state: %#v %#v", readA, readB)
	}
}

func TestManagerLifetimeBudgetAndUnknownClear(t *testing.T) {
	public := testOptions(t)
	public.Limits.MaxSessions = 1
	options, _ := canonicalize(public)
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	unknown := ownerKey{sessionID: "unknown", workspaceID: "w"}
	clear, err := manager.clear(context.Background(), unknown, root)
	if err != nil || clear != (ClearResult{}) || len(manager.owners) != 0 {
		t.Fatalf("unknown clear=%#v owners=%d err=%v", clear, len(manager.owners), err)
	}
	owner := ownerKey{sessionID: "one", workspaceID: "w"}
	if _, err := manager.execute(context.Background(), owner, root, "1"); err != nil {
		t.Fatal(err)
	}
	clear, err = manager.clear(context.Background(), unknown, root)
	if err != nil || clear != (ClearResult{}) || len(manager.owners) != 1 {
		t.Fatalf("unknown clear at capacity=%#v owners=%d err=%v", clear, len(manager.owners), err)
	}
	firstClear, err := manager.clear(context.Background(), owner, root)
	if err != nil || !firstClear.HadState || firstClear.Generation != 1 {
		t.Fatalf("first clear=%#v err=%v", firstClear, err)
	}
	secondClear, err := manager.clear(context.Background(), owner, root)
	if err != nil || secondClear.HadState || secondClear.Generation != 1 {
		t.Fatalf("idempotent clear=%#v err=%v", secondClear, err)
	}
	if _, err := manager.execute(context.Background(), ownerKey{sessionID: "two", workspaceID: "w"}, root, "1"); !errors.Is(err, errCapacityExhausted) {
		t.Fatalf("capacity after clear = %v", err)
	}
}

func TestManagerCanceledNewOwnerDoesNotConsumeCapacity(t *testing.T) {
	public := testOptions(t)
	public.Limits.MaxSessions = 1
	options, _ := canonicalize(public)
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.execute(ctx, ownerKey{sessionID: "canceled", workspaceID: "w"}, root, "1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled owner admission=%v", err)
	}
	if _, err := manager.clear(ctx, ownerKey{sessionID: "unknown", workspaceID: "w"}, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled unknown clear=%v", err)
	}
	if len(manager.owners) != 0 {
		t.Fatalf("canceled operation consumed owners=%d", len(manager.owners))
	}
	if _, err := manager.execute(context.Background(), ownerKey{sessionID: "admitted", workspaceID: "w"}, root, "1"); err != nil {
		t.Fatalf("capacity unavailable after canceled operation: %v", err)
	}
}

func TestManagerFailedFirstSetupStillConsumesOwnerSlot(t *testing.T) {
	public := testOptions(t)
	public.Limits.MaxSessions = 1
	// A regular executable satisfies mount-time validation but cannot create a
	// Python venv. Admission must still permanently consume this owner slot.
	public.PythonPath = "/usr/bin/false"
	options, err := canonicalize(public)
	if err != nil {
		t.Fatal(err)
	}
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	first := ownerKey{sessionID: "failed", workspaceID: "w"}
	if _, err := manager.execute(context.Background(), first, root, "1"); err == nil {
		t.Fatal("invalid Python setup succeeded")
	}
	if len(manager.owners) != 1 {
		t.Fatalf("failed owner count=%d", len(manager.owners))
	}
	if _, err := manager.execute(context.Background(), ownerKey{sessionID: "other", workspaceID: "w"}, root, "1"); !errors.Is(err, errCapacityExhausted) {
		t.Fatalf("failed setup did not retain capacity: %v", err)
	}
}

func TestManagerRetainsAndRetriesPartialVenvCleanupObligation(t *testing.T) {
	public := testOptions(t)
	public.PythonPath = "/usr/bin/false"
	options, err := canonicalize(public)
	if err != nil {
		t.Fatal(err)
	}
	removeCalls := 0
	options.hooks = &testHooks{removeVenv: func(path string) error {
		removeCalls++
		if removeCalls == 1 {
			return errors.New("synthetic removal failure")
		}
		return os.RemoveAll(path)
	}}
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "partial-cleanup", workspaceID: "w"}

	if _, err := manager.execute(context.Background(), owner, root, "1"); !errors.Is(err, errCleanupIncomplete) {
		t.Fatalf("initial cleanup obligation=%v", err)
	}
	session := manager.owners[owner]
	if session.venv == nil || !session.venvInvalid || removeCalls != 1 {
		t.Fatalf("retained cleanup obligation: venv=%t invalid=%t removals=%d", session.venv != nil, session.venvInvalid, removeCalls)
	}
	partialPath := session.venv.path
	if _, err := manager.execute(context.Background(), owner, root, "1"); err == nil {
		t.Fatal("invalid Python setup retry succeeded")
	}
	if _, err := os.Stat(partialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial venv remains after retry: %v", err)
	}
	if session.venv != nil || session.venvInvalid {
		t.Fatalf("cleanup obligation remained: venv=%t invalid=%t", session.venv != nil, session.venvInvalid)
	}
}

func TestManagerRejectsWorkspaceRootDriftWithoutLosingState(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	manager := newManager(options)
	deferManagerClose(t, manager)
	rootA, _ := canonicalWorkspaceRoot(t.TempDir())
	rootB, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "root", workspaceID: "w"}
	if _, err := manager.execute(context.Background(), owner, rootA, "x = 42"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.execute(context.Background(), owner, rootB, "x"); err == nil || !strings.Contains(err.Error(), "workspace-root-mismatch") {
		t.Fatalf("root drift error=%v", err)
	}
	result, err := manager.execute(context.Background(), owner, rootA, "x")
	if err != nil || result.Result.Text != "42" {
		t.Fatalf("original state=%#v err=%v", result, err)
	}
}

func TestManagerCloseDeadlineIsRetryable(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	manager := newManager(options)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "close-retry", workspaceID: "w"}
	if _, err := manager.execute(context.Background(), owner, root, "1"); err != nil {
		t.Fatal(err)
	}
	venvPath := manager.owners[owner].venv.path
	marker := root + "/running"
	executeDone := make(chan error, 1)
	go func() {
		_, err := manager.execute(context.Background(), owner, root, fmt.Sprintf("open(%q, 'w').write('ready')\nimport time\ntime.sleep(30)", marker))
		executeDone <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active execution gate not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	short, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if err := manager.Close(short); !errors.Is(err, errCleanupIncomplete) {
		t.Fatalf("short close error=%v", err)
	}
	select {
	case <-executeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("active execution did not join close")
	}
	ctx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer retryCancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("retry close=%v", err)
	}
	if _, err := os.Stat(venvPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("venv remains after retry: %v", err)
	}
}

func TestManagerCanceledQueuedCallNeverReachesPython(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "queued", workspaceID: "w"}
	if _, err := manager.execute(context.Background(), owner, root, "requests = []"); err != nil {
		t.Fatal(err)
	}
	entered := root + "/entered"
	releaseFile := root + "/release"
	firstDone := make(chan error, 1)
	go func() {
		code := fmt.Sprintf("import os, time\nrequests.append('first')\nopen(%q, 'w').write('ready')\nwhile not os.path.exists(%q): time.sleep(0.01)", entered, releaseFile)
		_, err := manager.execute(context.Background(), owner, root, code)
		firstDone <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first execution did not enter")
		}
		time.Sleep(10 * time.Millisecond)
	}
	queuedCtx, queuedCancel := context.WithCancel(context.Background())
	queuedDone := make(chan error, 1)
	go func() {
		_, err := manager.execute(queuedCtx, owner, root, "requests.append('canceled')")
		queuedDone <- err
	}()
	gate := manager.owners[owner].gate
	for {
		gate.mu.Lock()
		queued := len(gate.waiters)
		gate.mu.Unlock()
		if queued == 1 {
			break
		}
	}
	queuedCancel()
	if err := <-queuedDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation=%v", err)
	}
	if err := os.WriteFile(releaseFile, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	result, err := manager.execute(context.Background(), owner, root, "requests")
	if err != nil || result.Result.Text != "['first']" {
		t.Fatalf("request history=%#v err=%v", result, err)
	}
}

func TestManagerDifferentOwnersProgressConcurrently(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	manager := newManager(options)
	deferManagerClose(t, manager)
	rootA, _ := canonicalWorkspaceRoot(t.TempDir())
	rootB, _ := canonicalWorkspaceRoot(t.TempDir())
	barrier := t.TempDir()
	type response struct {
		value ExecuteResult
		err   error
	}
	results := make(chan response, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := func(owner ownerKey, root, mine, other, value string) {
		go func() {
			code := fmt.Sprintf("import os, time\nvalue = %q\nopen(%q, 'w').write('ready')\nwhile not os.path.exists(%q): time.sleep(0.01)\nvalue", value, mine, other)
			result, err := manager.execute(ctx, owner, root, code)
			results <- response{result, err}
		}()
	}
	markerA, markerB := barrier+"/a", barrier+"/b"
	start(ownerKey{sessionID: "concurrent-a", workspaceID: "w"}, rootA, markerA, markerB, "a")
	start(ownerKey{sessionID: "concurrent-b", workspaceID: "w"}, rootB, markerB, markerA, "b")
	seen := map[string]bool{}
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		seen[result.value.Result.Text] = true
	}
	if !seen["'a'"] || !seen["'b'"] {
		t.Fatalf("concurrent results=%v", seen)
	}
}
