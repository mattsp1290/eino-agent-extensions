//go:build linux || darwin

package backgroundjobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessSupervisorReadyBeforeImmediateKill(t *testing.T) {
	manager := newTestManager(t)
	owner := ownerKey{sessionID: "process-session", workspaceID: "process-workspace"}
	receipt, err := manager.start(context.Background(), owner, t.TempDir(), startInput{Command: `sleep 30`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	killed, err := manager.kill(ctx, owner, receipt.ID)
	if err != nil || killed.State != JobKilled || !killed.NewlyAccepted {
		t.Fatalf("immediate kill = %#v, %v", killed, err)
	}
	manager.mu.Lock()
	job := manager.jobs[receipt.ID]
	manager.mu.Unlock()
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.phase != phaseFinalSignalSent || !job.reaped || job.pgid <= 0 {
		t.Fatalf("supervisor cleanup = phase:%d reaped:%t pgid:%d", job.phase, job.reaped, job.pgid)
	}
}

func TestProcessCanceledAfterSpawnDoesNotReleaseCommandGate(t *testing.T) {
	workspace := t.TempDir()
	wrapper := filepath.Join(t.TempDir(), "delayed-shell")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nsleep 0.2\nexec /bin/sh \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	options.ShellPath = wrapper
	options.ShellIdentity = "delayed-test-shell-v1"
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newManager(canonical)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, startErr := manager.start(ctx, ownerKey{sessionID: "session", workspaceID: "workspace"}, workspace, startInput{Command: `printf ran > marker`, WorkingDirectory: "."})
		result <- startErr
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled command crossed launch gate: %v", err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err := manager.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}
