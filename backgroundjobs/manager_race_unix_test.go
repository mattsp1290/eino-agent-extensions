//go:build linux || darwin

package backgroundjobs

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestManager_ExitKillRace(t *testing.T) {
	manager := newTestManager(t)
	owner := ownerKey{sessionID: "race-session", workspaceID: "race-workspace"}
	receipt, err := manager.start(context.Background(), owner, t.TempDir(), startInput{Command: `sleep 0.02`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, killErr := manager.kill(ctx, owner, receipt.ID)
	if killErr != nil && !errors.Is(killErr, errTerminationIncomplete) {
		t.Fatal(killErr)
	}
	status := waitTerminal(t, manager, owner, receipt.ID)
	if status.State != JobSucceeded && status.State != JobKilled {
		t.Fatalf("exit/kill race state = %s", status.State)
	}
}

func TestManager_ESRCHFinalizesAndReleasesCapacity(t *testing.T) {
	manager := newTestManager(t, func(options *Options) {
		options.Limits.MaxRunning = 1
		options.Limits.MaxTracked = 2
	})
	owner := ownerKey{sessionID: "race-session", workspaceID: "race-workspace"}
	receipt, err := manager.start(context.Background(), owner, t.TempDir(), startInput{
		Command:          `sleep 30 & child=$!; printf '%s\n' "$child"; wait "$child"`,
		WorkingDirectory: ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	var childPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := manager.status(owner, receipt.ID)
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		childPID, _ = strconv.Atoi(strings.TrimSpace(status.Stdout.Text))
		if childPID > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("child PID was not reported")
	}

	realSignal := manager.signal
	manager.signal = func(pgid int, signal os.Signal) error {
		err := realSignal(pgid, signal)
		if signal == killSignal() && err == nil {
			return syscall.ESRCH
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := manager.kill(ctx, owner, receipt.ID); err != nil {
		t.Fatalf("kill after synthetic ESRCH = %v", err)
	}
	if status := waitTerminal(t, manager, owner, receipt.ID); status.State != JobKilled {
		t.Fatalf("state after synthetic ESRCH = %s", status.State)
	}
	if err := syscall.Kill(childPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("same-group child %d survived: %v", childPID, err)
	}

	manager.mu.Lock()
	running := manager.running
	manager.mu.Unlock()
	if running != 0 {
		t.Fatalf("running capacity after ESRCH = %d", running)
	}
	second, err := manager.start(context.Background(), owner, t.TempDir(), startInput{Command: `printf ok`, WorkingDirectory: "."})
	if err != nil {
		t.Fatalf("capacity was not released: %v", err)
	}
	if status := waitTerminal(t, manager, owner, second.ID); status.State != JobSucceeded {
		t.Fatalf("replacement job state = %s", status.State)
	}
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("close after ESRCH = %v", err)
	}
}

func TestManager_TimeoutKillRace(t *testing.T) {
	manager := newTestManager(t)
	owner := ownerKey{sessionID: "race-session", workspaceID: "race-workspace"}
	receipt, err := manager.start(context.Background(), owner, t.TempDir(), startInput{Command: `sleep 30`, WorkingDirectory: "."})
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	job := manager.jobs[receipt.ID]
	manager.mu.Unlock()
	var group sync.WaitGroup
	group.Add(2)
	go func() { defer group.Done(); job.beginTermination(causeTimeout) }()
	go func() { defer group.Done(); job.beginTermination(causeKill) }()
	group.Wait()
	status := waitTerminal(t, manager, owner, receipt.ID)
	if status.State != JobTimedOut && status.State != JobKilled {
		t.Fatalf("timeout/kill race state = %s", status.State)
	}
}

func TestManager_CloseRetry(t *testing.T) {
	options := testOptions()
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newManager(canonical)
	if err != nil {
		t.Fatal(err)
	}
	owner := ownerKey{sessionID: "race-session", workspaceID: "race-workspace"}
	if _, err := manager.start(context.Background(), owner, t.TempDir(), startInput{Command: `sleep 30`, WorkingDirectory: "."}); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Close(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("first close error = %v", err)
	}
	ctx, retryCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer retryCancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("retry close = %v", err)
	}
}

func TestManager_StartCloseRace(t *testing.T) {
	options := testOptions()
	options.Limits.MaxRunning = 8
	options.Limits.MaxTracked = 8
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newManager(canonical)
	if err != nil {
		t.Fatal(err)
	}
	owner := ownerKey{sessionID: "race-session", workspaceID: "race-workspace"}
	workspace := t.TempDir()
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := 0; index < 12; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, startErr := manager.start(context.Background(), owner, workspace, startInput{Command: `sleep 30`, WorkingDirectory: "."})
			if startErr != nil && !errors.Is(startErr, errManagerClosing) && !errors.Is(startErr, errCapacityExhausted) {
				t.Errorf("raced start = %v", startErr)
			}
		}()
	}
	close(start)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.starting != 0 || manager.running != 0 || len(manager.jobs) != 0 || len(manager.hidden) != 0 {
		t.Fatalf("post-close state: starting=%d running=%d jobs=%d hidden=%d", manager.starting, manager.running, len(manager.jobs), len(manager.hidden))
	}
}
