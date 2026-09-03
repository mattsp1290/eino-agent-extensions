//go:build linux || darwin

package pythonrepl

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunnerNamespaceSealingAndDescendantEnvironment(t *testing.T) {
	t.Setenv("PYTHON_REPL_AMBIENT", "ambient-marker")
	public := testOptions(t)
	public.Limits.MaxOutputBytesPerStream = 256
	options, err := canonicalize(public)
	if err != nil {
		t.Fatal(err)
	}
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "protocol", workspaceID: "workspace"}
	code := `
import subprocess, sys
json = None
os = None
struct = None
threading = None
p = subprocess.run([sys.executable, "-c", "import os; print(os.environ.get('PYTHON_REPL_EXPLICIT')); print(os.environ.get('PYTHON_REPL_AMBIENT'))"], capture_output=True, text=True)
print(p.stdout, end="")
`
	result, err := manager.execute(context.Background(), owner, root, code)
	if err != nil || result.Status != "completed" || result.Stdout.Text != "visible\nNone\n" {
		t.Fatalf("descendant result=%#v err=%v", result, err)
	}
	next, err := manager.execute(context.Background(), owner, root, "6 * 7")
	if err != nil || next.Result.Text != "42" {
		t.Fatalf("namespace corrupted protocol: %#v %v", next, err)
	}

	late, err := manager.execute(context.Background(), owner, root, `
import sys, threading, time
w = sys.stdout
def later():
    time.sleep(0.05)
    w.write("late-output")
threading.Thread(target=later, daemon=True).start()
"first"
`)
	if err != nil || late.Result.Text != "'first'" {
		t.Fatalf("thread setup=%#v %v", late, err)
	}
	after, err := manager.execute(context.Background(), owner, root, "import time; time.sleep(0.1); print('next')")
	if err != nil || after.Stdout.Text != "next\n" || strings.Contains(after.Stdout.Text, "late-output") {
		t.Fatalf("late output crossed request: %#v %v", after, err)
	}
}

func TestUnexpectedSupervisorDeathQuarantinesOwner(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	manager := newManager(options)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "supervisor-death", workspaceID: "workspace"}
	if _, err := manager.execute(context.Background(), owner, root, "state = 42"); err != nil {
		t.Fatal(err)
	}
	session := manager.owners[owner]
	runner := session.runner
	venvPath := session.venv.path
	pgid := runner.pgid
	t.Cleanup(func() {
		// This exceptional test deliberately performs the host-operations cleanup
		// documented for a lost supervisor; the package correctly refuses to claim it.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		if removeErr := os.RemoveAll(venvPath); removeErr != nil {
			t.Error(removeErr)
		}
	})
	if err := runner.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.waitDone:
	case <-time.After(time.Second):
		t.Fatal("supervisor was not reaped")
	}
	_, err := manager.execute(context.Background(), owner, root, "state")
	if err == nil || !errors.Is(err, errCleanupIncomplete) || !session.quarantined {
		t.Fatalf("supervisor death result error=%v quarantined=%t", err, session.quarantined)
	}
	if closeErr := manager.Close(context.Background()); !errors.Is(closeErr, errCleanupIncomplete) {
		t.Fatalf("quarantined close error=%v", closeErr)
	}
}

func TestRunnerExitWithSameGroupChildResetsCleanly(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "exit", workspaceID: "workspace"}
	_, err := manager.execute(context.Background(), owner, root, `
import os, subprocess, sys
subprocess.Popen([sys.executable, "-c", "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)"])
os._exit(7)
`)
	if err == nil || errors.Is(err, errCleanupIncomplete) {
		t.Fatalf("runner exit error = %v", err)
	}
	result, err := manager.execute(context.Background(), owner, root, "21 * 2")
	if err != nil || result.Result.Text != "42" || !result.StateReset || result.StateResetReason != "runner_failed" || result.Generation != 1 {
		t.Fatalf("replacement result=%#v err=%v", result, err)
	}
}

func TestRunnerTermIgnoringGroupRequiresKill(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "kill", workspaceID: "workspace"}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := manager.execute(ctx, owner, root, `
import signal, subprocess, sys, time
signal.signal(signal.SIGTERM, signal.SIG_IGN)
subprocess.Popen([sys.executable, "-c", "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)"])
time.sleep(30)
`)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errCleanupIncomplete) {
		t.Fatalf("TERM-ignoring cleanup error = %v", err)
	}
}

func TestDeliberatelyDetachedChildIsOutsideCleanupContract(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "detached", workspaceID: "workspace"}
	result, err := manager.execute(context.Background(), owner, root, `
import subprocess, sys
p = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"], start_new_session=True)
p.pid
`)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(result.Result.Text)
	if err != nil || pid <= 0 {
		t.Fatalf("detached child pid result invalid: %v", err)
	}
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	if _, err := manager.clear(context.Background(), owner, root); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("detached child unexpectedly covered by worker-group cleanup: %v", err)
	}
	// The test performs the host-owned cleanup for the intentionally detached
	// process; stronger containment requires a container, VM, or sandbox.
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("host cleanup of detached child: %v", err)
	}
}

func TestRunnerTruncatesAtUTF8Boundary(t *testing.T) {
	public := testOptions(t)
	public.Limits.MaxOutputBytesPerStream = 5
	options, _ := canonicalize(public)
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	result, err := manager.execute(context.Background(), ownerKey{sessionID: "unicode", workspaceID: "w"}, root, "import sys; sys.stdout.write('🙂🙂')")
	if err != nil || result.Stdout.Text != "🙂" || !result.Stdout.Truncated || len(result.Stdout.Text) != 4 {
		t.Fatalf("Unicode truncation=%#v err=%v", result.Stdout, err)
	}
}

func TestRunnerOutputRemainsUTF8PrefixAcrossWrites(t *testing.T) {
	public := testOptions(t)
	public.Limits.MaxOutputBytesPerStream = 3
	options, _ := canonicalize(public)
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	result, err := manager.execute(context.Background(), ownerKey{sessionID: "unicode-prefix", workspaceID: "w"}, root, "import sys; sys.stdout.write('\U0001f642'); sys.stdout.write('later')")
	if err != nil || result.Stdout.Text != "" || !result.Stdout.Truncated {
		t.Fatalf("Unicode prefix text=%q truncated=%t err=%v", result.Stdout.Text, result.Stdout.Truncated, err)
	}
}

func TestRunnerResponseRequiresStatusSpecificZeroValues(t *testing.T) {
	options, err := canonicalize(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	base := runnerResponse{
		Version: runnerProtocolVersion, ID: 1, Status: ExecuteStatusCompleted,
	}
	if !validRunnerResponse(options, 1, base) {
		t.Fatal("valid completed response rejected")
	}
	completedWithTruncatedException := base
	completedWithTruncatedException.Exception.Truncated = true
	if validRunnerResponse(options, 1, completedWithTruncatedException) {
		t.Fatal("completed response accepted truncated exception")
	}
	pythonErrorWithResult := base
	pythonErrorWithResult.Status = ExecuteStatusPythonError
	pythonErrorWithResult.Result = BoundedText{Text: "forged"}
	if validRunnerResponse(options, 1, pythonErrorWithResult) {
		t.Fatal("python-error response accepted a result")
	}
	pythonErrorWithTruncatedResult := base
	pythonErrorWithTruncatedResult.Status = ExecuteStatusPythonError
	pythonErrorWithTruncatedResult.Result.Truncated = true
	if validRunnerResponse(options, 1, pythonErrorWithTruncatedResult) {
		t.Fatal("python-error response accepted truncated result")
	}
}

func TestRunnerReplacesUnencodableUserTextWithoutBreakingProtocol(t *testing.T) {
	options, _ := canonicalize(testOptions(t))
	manager := newManager(options)
	deferManagerClose(t, manager)
	root, _ := canonicalWorkspaceRoot(t.TempDir())
	owner := ownerKey{sessionID: "surrogate", workspaceID: "w"}
	result, err := manager.execute(context.Background(), owner, root, "raise ValueError(chr(0xD800))")
	if err != nil || result.Status != "python_error" || !strings.Contains(result.Exception.Text, "ValueError: ?") {
		t.Fatalf("surrogate exception status=%s text=%q err=%v", result.Status, result.Exception.Text, err)
	}
	next, err := manager.execute(context.Background(), owner, root, "6 * 7")
	if err != nil || next.Result.Text != "42" || next.Generation != 0 {
		t.Fatalf("runner unhealthy after replacement: result=%q generation=%d err=%v", next.Result.Text, next.Generation, err)
	}
}
