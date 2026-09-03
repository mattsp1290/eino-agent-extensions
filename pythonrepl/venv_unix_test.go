//go:build linux || darwin

package pythonrepl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVenvIsPrivateWithoutPipAndRemovable(t *testing.T) {
	options, err := canonicalize(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	venv, err := createVenv(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(venv.path)
	if err != nil || info.Mode().Perm() != 0o700 || !info.IsDir() {
		t.Fatalf("venv mode=%v err=%v", info.Mode(), err)
	}
	runner, err := startRunner(context.Background(), options, venv.interpreter, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := runner.execute(context.Background(), "import importlib.util; importlib.util.find_spec('pip')")
	if err != nil || outcome.response.Result.Text != "None" {
		t.Fatalf("pip result=%#v err=%v", outcome, err)
	}
	if err := runner.terminateAndWait(); err != nil {
		t.Fatal(err)
	}
	if err := removeVenv(venv); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(venv.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("venv remains: %v", err)
	}
}

func TestVenvFailureAndCancellationRemovePartialDirectory(t *testing.T) {
	for name, script := range map[string]string{
		"failure":      "#!/bin/sh\n/bin/mkdir -p \"$4/junk\"\nexit 1\n",
		"cancellation": "#!/bin/sh\ntrap '' TERM\n/bin/sleep 30\n",
	} {
		t.Run(name, func(t *testing.T) {
			public := testOptions(t)
			wrapper := filepath.Join(t.TempDir(), "python-wrapper")
			if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			public.PythonPath = wrapper
			options, err := canonicalize(public)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			cancel := func() {}
			if name == "cancellation" {
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
			}
			defer cancel()
			if _, err := createVenv(ctx, options); err == nil {
				t.Fatal("venv creation unexpectedly succeeded")
			}
			entries, err := os.ReadDir(options.tempRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("partial venv remains entries=%d err=%v", len(entries), err)
			}
		})
	}
}
