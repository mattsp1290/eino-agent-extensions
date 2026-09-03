//go:build linux || darwin

package pythonrepl

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	store "github.com/mattsp1290/eino-agent/store/sqlite"
)

func testPython(t *testing.T) string {
	t.Helper()
	path := os.Getenv("PYTHON_REPL_TEST_PYTHON")
	if path == "" {
		var err error
		path, err = exec.LookPath("python3")
		if err != nil {
			t.Fatal("PYTHON_REPL_TEST_PYTHON is required when python3 is unavailable")
		}
		path, err = filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("test Python %q: %v", path, err)
	}
	return resolved
}

func deferManagerClose(t *testing.T, manager *manager) {
	t.Helper()
	t.Cleanup(func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
}

func deferDatabaseClose(t *testing.T, database *store.Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
}

func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		PythonPath: testPython(t), PythonIdentity: "test-python-v1", TempRoot: t.TempDir(),
		Environment: Environment{Identity: "test-environment-v1", Entries: map[string]string{"PYTHON_REPL_EXPLICIT": "visible"}},
		Limits: Limits{
			MaxSessions: 4, MaxQueuedPerSession: 4, MaxCodeBytes: 16 << 10,
			MaxOutputBytesPerStream: 1024, MaxResultBytes: 1024, MaxExceptionBytes: 2048,
			MaxEnvironmentEntries: 16, MaxEnvironmentBytes: 4096,
			DefaultTimeout: 3 * time.Second, MaxTimeout: 5 * time.Second,
			VenvCreateTimeout: 3 * time.Second, RunnerStartTimeout: 2 * time.Second,
			TerminateGrace: 20 * time.Millisecond, KillWait: 2 * time.Second,
		},
	}
}
