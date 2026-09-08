//go:build linux || darwin

package rtkreducer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	gort "runtime"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as a private subprocess fixture. Production never enables
// this path: the fixture is selected only by the exact argv used by runProcess.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "pipe" && os.Args[2] == "--filter" {
		fixtureProcess()
		return
	}
	os.Exit(m.Run())
}

func fixtureProcess() {
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	if string(payload) == "sleep" {
		time.Sleep(300 * time.Millisecond)
	}
	if os.Args[3] != string(Log) {
		os.Exit(3)
	}
	switch string(payload) {
	case "nonzero":
		_, _ = fmt.Fprint(os.Stderr, "private failure detail")
		os.Exit(9)
	case "empty":
		return
	case "invalid-utf8":
		_, _ = os.Stdout.Write([]byte{0xff})
		return
	case "stdout-overflow":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("o", 2048))
		return
	case "stderr-overflow":
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("e", 2048))
		_, _ = fmt.Fprint(os.Stdout, "reduced")
		return
	}
	if string(payload) == "environment" {
		_, _ = fmt.Fprintf(os.Stdout, "PATH=%q HOME=%q PWD=%q XDG_CONFIG_HOME=%q TMPDIR=%q\\n", os.Getenv("PATH"), os.Getenv("HOME"), os.Getenv("PWD"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("TMPDIR"))
		return
	}
	// The value proves stdin was supplied and is intentionally much shorter
	// than the test payload. No fixture environment is inherited by the child.
	if len(payload) > 0 {
		_, _ = fmt.Fprint(os.Stdout, "reduced")
	}
}

func fixtureCoordinator(t *testing.T, concurrent int) *coordinator {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	limits := testLimits()
	limits.MaxConcurrent = concurrent
	limits.MaxStdoutBytes = 1024
	limits.MaxStderrBytes = 64
	return newCoordinator(canonicalOptions{
		executablePath: executable,
		tempRoot:       t.TempDir(),
		limits:         limits,
	})
}

func TestCoordinatorUsesFixedPipeAndCleansPrivateInvocation(t *testing.T) {
	coordinator := fixtureCoordinator(t, 1)
	output, ok := coordinator.reduce(context.Background(), Log, strings.Repeat("payload", 20))
	if !ok || output != "reduced" {
		t.Fatalf("output=%q ok=%v", output, ok)
	}
	root := coordinator.root
	if root == "" {
		t.Fatal("lazy root was not created")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root before close: %v", err)
	}
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root after close stat=%v", err)
	}
}

func TestCoordinatorConstructsPrivateEnvironment(t *testing.T) {
	coordinator := fixtureCoordinator(t, 1)
	output, ok := coordinator.reduce(context.Background(), Log, "environment")
	if !ok {
		t.Fatalf("environment fixture failed: %q", output)
	}
	if !strings.Contains(output, `PATH=""`) || !strings.Contains(output, `HOME="`) || !strings.Contains(output, `TMPDIR="`) {
		t.Fatalf("environment was not constrained: %q", output)
	}
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorCapacityIsCallbackScoped(t *testing.T) {
	coordinator := fixtureCoordinator(t, 1)
	lease, ok := coordinator.acquire(context.Background())
	if !ok {
		t.Fatal("first lease not acquired")
	}
	defer func() {
		lease.release()
		_ = coordinator.Close(context.Background())
	}()
	if _, ok := coordinator.acquire(context.Background()); ok {
		t.Fatal("capacity unexpectedly waited or over-admitted")
	}
	if output, ok := lease.reduce(context.Background(), Log, "payload"); !ok || output != "reduced" {
		t.Fatalf("lease reduction output=%q ok=%v", output, ok)
	}
}

func TestCoordinatorCancellationKeepsLeaseUntilOwnerReaps(t *testing.T) {
	coordinator := fixtureCoordinator(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	lease, ok := coordinator.acquire(ctx)
	if !ok {
		t.Fatal("lease not acquired")
	}
	if _, ok := lease.reduce(ctx, Log, "sleep"); ok {
		t.Fatal("cancelled process succeeded")
	}
	lease.release()
	// The direct child is killable, so it may already have been reaped here. A
	// delayed descendant is intentionally not part of this package's contract.
	time.Sleep(350 * time.Millisecond)
	if next, ok := coordinator.acquire(context.Background()); !ok {
		t.Fatal("slot was not released after reap")
	} else {
		next.release()
	}
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorRejectsFailedAndUnsafeTransport(t *testing.T) {
	coordinator := fixtureCoordinator(t, 1)
	defer func() { _ = coordinator.Close(context.Background()) }()
	for _, input := range []string{"nonzero", "empty", "invalid-utf8", "stdout-overflow", "stderr-overflow"} {
		t.Run(input, func(t *testing.T) {
			if output, ok := coordinator.reduce(context.Background(), Log, input); ok || output != "" {
				t.Fatalf("unsafe transport accepted: output=%q ok=%v", output, ok)
			}
		})
	}
}

func TestCoordinatorPassesMetacharactersOnlyOnStdin(t *testing.T) {
	canary := filepath.Join(t.TempDir(), "shell-canary")
	payload := "$(touch " + canary + "); `touch " + canary + "`; payload"
	coordinator := fixtureCoordinator(t, 1)
	defer func() { _ = coordinator.Close(context.Background()) }()
	if output, ok := coordinator.reduce(context.Background(), Log, payload); !ok || output != "reduced" {
		t.Fatalf("reduction output=%q ok=%v", output, ok)
	}
	if _, err := os.Stat(canary); !os.IsNotExist(err) {
		t.Fatalf("stdin metacharacters executed: %v", err)
	}
}

func TestCoordinatorRepeatedCloseCyclesDoNotGrowOwners(t *testing.T) {
	before := gort.NumGoroutine()
	for index := 0; index < 12; index++ {
		coordinator := fixtureCoordinator(t, 1)
		if output, ok := coordinator.reduce(context.Background(), Log, strings.Repeat("payload", 20)); !ok || output != "reduced" {
			t.Fatalf("cycle %d reduction output=%q ok=%v", index, output, ok)
		}
		if err := coordinator.Close(context.Background()); err != nil {
			t.Fatalf("cycle %d close: %v", index, err)
		}
	}
	gort.GC()
	time.Sleep(50 * time.Millisecond)
	after := gort.NumGoroutine()
	if after > before+4 {
		t.Fatalf("goroutines grew across close cycles: before=%d after=%d", before, after)
	}
	t.Logf("repeated close measurement: cycles=12 goroutines_before=%d goroutines_after=%d", before, after)
}

func TestCoordinatorQuarantinesCleanupFailureAndCloseRetries(t *testing.T) {
	coordinator := fixtureCoordinator(t, 1)
	coordinator.inspectInvocation = func(dirs invocationDirs) {
		if err := os.Chmod(filepath.Dir(dirs.root), 0o500); err != nil {
			panic(err)
		}
	}
	if output, ok := coordinator.reduce(context.Background(), Log, strings.Repeat("payload", 20)); ok || output != "" {
		t.Fatalf("cleanup failure published reduction: output=%q ok=%v", output, ok)
	}
	if lease, ok := coordinator.acquire(context.Background()); ok {
		lease.release()
		t.Fatal("quarantined coordinator admitted another callback")
	}
	if err := coordinator.Close(context.Background()); err == nil || err.Error() != cleanupError().Error() {
		t.Fatalf("first close error=%v, want fixed cleanup error", err)
	}
	if err := os.Chmod(coordinator.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatalf("retry close: %v", err)
	}
}
