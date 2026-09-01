//go:build linux || darwin

package backgroundjobs

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newTestManager(t *testing.T, mutate ...func(*Options)) *manager {
	t.Helper()
	options := testOptions()
	for _, apply := range mutate {
		apply(&options)
	}
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newManager(canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("manager close: %v", err)
		}
	})
	return manager
}

func TestManagerNaturalExitAndBoundedOutput(t *testing.T) {
	manager := newTestManager(t, func(options *Options) { options.Limits.MaxOutputBytesPerStream = 4 })
	owner := ownerKey{sessionID: "session-a", workspaceID: "workspace-a"}
	result, err := manager.start(context.Background(), owner, t.TempDir(), startInput{Command: `printf abcdef; printf uvwxyz >&2; exit 7`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	status := waitTerminal(t, manager, owner, result.ID)
	if status.State != JobFailed || status.ExitCode == nil || *status.ExitCode != 7 || status.Stdout.Text != "cdef" || status.Stderr.Text != "wxyz" || !status.Stdout.Truncated || !status.Stderr.Truncated {
		t.Fatalf("terminal status = %#v", status)
	}
}

func TestManagerJobOutlivesStartContextAndOwnerIsolation(t *testing.T) {
	manager := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	owner := ownerKey{sessionID: "session-a", workspaceID: "workspace-a"}
	result, err := manager.start(ctx, owner, t.TempDir(), startInput{Command: `sleep 2`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	status, err := manager.status(owner, result.ID)
	if err != nil || status.State != JobRunning {
		t.Fatalf("status after call cancellation = %#v, %v", status, err)
	}
	foreign := ownerKey{sessionID: "session-a", workspaceID: "workspace-b"}
	_, foreignErr := manager.status(foreign, result.ID)
	_, unknownErr := manager.status(owner, "job_00000000000000000000000000000000_0000000000000000")
	if !errors.Is(foreignErr, errJobNotFound) || foreignErr.Error() != unknownErr.Error() {
		t.Fatalf("foreign=%v unknown=%v", foreignErr, unknownErr)
	}
	if listed := manager.list(foreign); len(listed.Jobs) != 0 {
		t.Fatalf("foreign list = %#v", listed)
	}
	ctxKill, cancelKill := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelKill()
	if killed, err := manager.kill(ctxKill, owner, result.ID); err != nil || killed.State != JobKilled || !killed.NewlyAccepted {
		manager.mu.Lock()
		job := manager.jobs[result.ID]
		manager.mu.Unlock()
		if job != nil {
			job.mu.Lock()
			t.Logf("kill failure: cause=%d phase=%d coordinator=%t reaped=%t status-valid=%t status=%d wait=%v", job.cause, job.phase, job.coordinatorRunning, job.reaped, job.statusValid, job.statusCode, job.waitErr)
			job.mu.Unlock()
		}
		t.Fatalf("kill = %#v, %v", killed, err)
	}
}

func TestManagerTimeoutAndZeroDefault(t *testing.T) {
	manager := newTestManager(t)
	owner := ownerKey{sessionID: "session", workspaceID: "workspace"}
	zero, err := manager.start(context.Background(), owner, t.TempDir(), startInput{Command: `sleep 2`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if status, _ := manager.status(owner, zero.ID); status.State != JobRunning || status.TimeoutSeconds != 0 {
		t.Fatalf("zero-timeout status = %#v", status)
	}
	one := int64(1)
	timed, err := manager.start(context.Background(), owner, t.TempDir(), startInput{Command: `sleep 5`, WorkingDirectory: ".", TimeoutSeconds: &one})
	if err != nil {
		t.Fatal(err)
	}
	status := waitTerminal(t, manager, owner, timed.ID)
	if status.State != JobTimedOut || status.ExitCode != nil {
		t.Fatalf("timeout status = %#v", status)
	}
}

func TestManagerPositiveDefaultAppliesToOmittedAndExplicitZero(t *testing.T) {
	manager := newTestManager(t, func(options *Options) { options.Limits.DefaultTimeout = time.Second })
	owner := ownerKey{sessionID: "session", workspaceID: "workspace"}
	workspace := t.TempDir()
	zero := int64(0)
	omitted, err := manager.start(context.Background(), owner, workspace, startInput{Command: `sleep 5`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	explicitZero, err := manager.start(context.Background(), owner, workspace, startInput{Command: `sleep 5`, WorkingDirectory: ".", TimeoutSeconds: &zero})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{omitted.ID, explicitZero.ID} {
		status := waitTerminal(t, manager, owner, id)
		if status.State != JobTimedOut || status.TimeoutSeconds != 1 {
			t.Fatalf("default-timeout status = %#v", status)
		}
	}
}

func TestManagerKillRemovesSameGroupChild(t *testing.T) {
	manager := newTestManager(t)
	owner := ownerKey{sessionID: "session", workspaceID: "workspace"}
	result, err := manager.start(context.Background(), owner, t.TempDir(), startInput{Command: `sleep 30 & child=$!; printf '%s\n' "$child"; wait "$child"`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	var childPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, _ := manager.status(owner, result.ID)
		line := strings.TrimSpace(status.Stdout.Text)
		if line != "" {
			childPID, _ = strconv.Atoi(line)
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("child PID was not reported")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := manager.kill(ctx, owner, result.ID); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("same-group child %d still exists", childPID)
}

func TestManagerEnvironmentModesUseFrozenRealChildEnvironment(t *testing.T) {
	t.Setenv("BACKGROUND_JOBS_AMBIENT", "ambient-one")
	owner := ownerKey{sessionID: "session", workspaceID: "workspace"}
	explicit := newTestManager(t, func(options *Options) {
		options.Environment.Overrides["BACKGROUND_JOBS_OVERRIDE"] = "explicit"
	})
	receipt, err := explicit.start(context.Background(), owner, t.TempDir(), startInput{Command: `printf '%s|%s' "${BACKGROUND_JOBS_AMBIENT-unset}" "$BACKGROUND_JOBS_OVERRIDE"`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	if status := waitTerminal(t, explicit, owner, receipt.ID); status.Stdout.Text != "unset|explicit" {
		t.Fatalf("explicit-only output = %q", status.Stdout.Text)
	}

	inherit := newTestManager(t, func(options *Options) {
		options.Environment.Mode = EnvironmentInheritAndOverride
		options.Environment.Overrides["BACKGROUND_JOBS_OVERRIDE"] = "override"
	})
	t.Setenv("BACKGROUND_JOBS_AMBIENT", "ambient-two")
	receipt, err = inherit.start(context.Background(), owner, t.TempDir(), startInput{Command: `printf '%s|%s' "$BACKGROUND_JOBS_AMBIENT" "$BACKGROUND_JOBS_OVERRIDE"`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	if status := waitTerminal(t, inherit, owner, receipt.ID); status.Stdout.Text != "ambient-one|override" {
		t.Fatalf("frozen inherit output = %q", status.Stdout.Text)
	}
}

func TestManagerTerminationContinuesAfterKillCallerCancellation(t *testing.T) {
	manager := newTestManager(t, func(options *Options) { options.Limits.TerminateGrace = 200 * time.Millisecond })
	owner := ownerKey{sessionID: "session", workspaceID: "workspace"}
	receipt, err := manager.start(context.Background(), owner, t.TempDir(), startInput{Command: `trap '' TERM; while :; do sleep 30; done`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		_, killErr := manager.kill(ctx, owner, receipt.ID)
		returned <- killErr
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-returned; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled kill error = %v", err)
	}
	status := waitTerminal(t, manager, owner, receipt.ID)
	if status.State != JobKilled {
		t.Fatalf("manager-owned escalation state = %s", status.State)
	}
}

func TestManagerCloseTerminatesJobsConcurrentlyAndRecoversCapacity(t *testing.T) {
	options := testOptions()
	options.Limits.MaxRunning = 2
	options.Limits.MaxTracked = 3
	options.Limits.TerminateGrace = 200 * time.Millisecond
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newManager(canonical)
	if err != nil {
		t.Fatal(err)
	}
	owner := ownerKey{sessionID: "session", workspaceID: "workspace"}
	workspace := t.TempDir()
	first, err := manager.start(context.Background(), owner, workspace, startInput{Command: `trap '' TERM; sleep 30`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.start(context.Background(), owner, workspace, startInput{Command: `sleep 30`, WorkingDirectory: "."}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.start(context.Background(), owner, workspace, startInput{Command: `printf capacity`, WorkingDirectory: "."}); !errors.Is(err, errCapacityExhausted) {
		t.Fatalf("running capacity error = %v", err)
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= 600*time.Millisecond {
		t.Fatalf("close serialized per-job grace: %s", elapsed)
	}
	if status, err := manager.status(owner, first.ID); !errors.Is(err, errJobNotFound) || status.ID != "" {
		t.Fatalf("closed manager retained public job = %#v, %v", status, err)
	}
}

func TestManagerCapacityRecoversAfterValidationFailureAndRootRemovalDoesNotBreakKill(t *testing.T) {
	manager := newTestManager(t, func(options *Options) {
		options.Limits.MaxRunning = 1
		options.Limits.MaxTracked = 2
	})
	owner := ownerKey{sessionID: "session", workspaceID: "workspace"}
	workspace := t.TempDir()
	if _, err := manager.start(context.Background(), owner, workspace, startInput{Command: `printf no`, WorkingDirectory: "missing"}); err == nil {
		t.Fatal("missing working directory accepted")
	}
	receipt, err := manager.start(context.Background(), owner, workspace, startInput{Command: `sleep 30`, WorkingDirectory: "."})
	if err != nil {
		t.Fatalf("capacity did not recover: %v", err)
	}
	renamed := workspace + "-renamed"
	if err := syscall.Rename(workspace, renamed); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if result, err := manager.kill(ctx, owner, receipt.ID); err != nil || result.State != JobKilled {
		t.Fatalf("kill after root rename = %#v, %v", result, err)
	}
	foreign := ownerKey{sessionID: "session", workspaceID: "other"}
	if _, err := manager.status(foreign, receipt.ID); !errors.Is(err, errJobNotFound) {
		t.Fatalf("foreign status after root rename = %v", err)
	}
}

func TestWorkingDirectoryContainment(t *testing.T) {
	root := t.TempDir()
	inside := root + "/inside"
	if err := syscall.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, target, err := resolveWorkingDirectory(root, "inside")
	if err != nil || target != canonicalRoot+"/inside" {
		t.Fatalf("inside resolution = %q %q %v", canonicalRoot, target, err)
	}
	outside := t.TempDir()
	if err := syscall.Symlink(outside, root+"/escape"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveWorkingDirectory(root, "escape"); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func waitTerminal(t *testing.T, manager *manager, owner ownerKey, id string) StatusResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.status(owner, id)
		if err != nil {
			t.Fatal(err)
		}
		if status.State != JobRunning {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	manager.mu.Lock()
	job := manager.jobs[id]
	manager.mu.Unlock()
	if job != nil {
		job.mu.Lock()
		t.Logf("stuck job: cause=%d phase=%d coordinator=%t reaped=%t status-valid=%t status=%d wait=%v", job.cause, job.phase, job.coordinatorRunning, job.reaped, job.statusValid, job.statusCode, job.waitErr)
		job.mu.Unlock()
	}
	t.Fatal("job did not become terminal")
	return StatusResult{}
}
