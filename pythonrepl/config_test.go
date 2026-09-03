//go:build linux || darwin

package pythonrepl

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/extension"
)

func TestConfigHashFreezesExplicitEnvironment(t *testing.T) {
	options := testOptions(t)
	first, err := ConfigHash(options)
	if err != nil {
		t.Fatal(err)
	}
	options.Environment.Entries = map[string]string{"PYTHON_REPL_EXPLICIT": "visible"}
	second, err := ConfigHash(options)
	if err != nil || first != second {
		t.Fatalf("equivalent hashes %q %q: %v", first, second, err)
	}
	options.Environment.Entries["PYTHON_REPL_EXPLICIT"] = "private-marker"
	third, err := ConfigHash(options)
	if err != nil || third != first {
		t.Fatalf("value affected hash %q %q: %v", first, third, err)
	}
	options.Environment.Identity = "test-environment-v2"
	fourth, err := ConfigHash(options)
	if err != nil || fourth == first {
		t.Fatalf("identity did not affect hash: %q %v", fourth, err)
	}
}

func TestConfigHashTracksResolvedPathsIdentityAndLimitsButNotScopeOrder(t *testing.T) {
	options := testOptions(t)
	base, err := ConfigHash(options)
	if err != nil {
		t.Fatal(err)
	}
	changed := options
	changed.PythonIdentity = "test-python-v2"
	identityHash, _ := ConfigHash(changed)
	changed = options
	changed.TempRoot = t.TempDir()
	rootHash, _ := ConfigHash(changed)
	changed = options
	changed.Limits.MaxCodeBytes++
	limitHash, _ := ConfigHash(changed)
	for name, hash := range map[string]string{"identity": identityHash, "root": rootHash, "limit": limitHash} {
		if hash == base {
			t.Fatalf("%s did not change hash", name)
		}
	}
	changed = options
	changed.Order = 912
	changed.Scope = extension.SessionScope("scoped-session")
	scopeHash, err := ConfigHash(changed)
	if err != nil || scopeHash != base {
		t.Fatalf("Eino-owned scope/order changed hash=%q base=%q err=%v", scopeHash, base, err)
	}

	directory := t.TempDir()
	first := filepath.Join(directory, "first")
	second := filepath.Join(directory, "second")
	link := filepath.Join(directory, "python")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	changed = options
	changed.PythonPath = link
	firstHash, err := ConfigHash(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	secondHash, err := ConfigHash(changed)
	if err != nil || firstHash == secondHash {
		t.Fatalf("symlink target hashes=%q/%q err=%v", firstHash, secondHash, err)
	}
}

func TestConfigSnapshotAndDiagnosticsHideValues(t *testing.T) {
	options := testOptions(t)
	options.Environment.Entries["PRIVATE"] = "private-marker"
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := configHash(canonical)
	options.Environment.Entries["PRIVATE"] = "mutated-marker"
	after, _ := configHash(canonical)
	if before != after {
		t.Fatal("canonical hash changed after host map mutation")
	}
	for _, formatted := range []string{fmt.Sprintf("%v", canonical), fmt.Sprintf("%+v", canonical), fmt.Sprintf("%#v", canonical)} {
		if strings.Contains(formatted, "private-marker") || strings.Contains(formatted, "mutated-marker") {
			t.Fatalf("diagnostics leaked an environment value: %s", formatted)
		}
	}
	for _, formatted := range []string{fmt.Sprintf("%v", options), fmt.Sprintf("%+v", options), fmt.Sprintf("%#v", options), fmt.Sprintf("%v", options.Environment), fmt.Sprintf("%+v", options.Environment), fmt.Sprintf("%#v", options.Environment)} {
		if strings.Contains(formatted, "private-marker") || strings.Contains(formatted, options.PythonPath) || strings.Contains(formatted, options.TempRoot) {
			t.Fatalf("public diagnostics leaked private configuration: %s", formatted)
		}
	}
}

func TestConfigValidationAndRetentionBounds(t *testing.T) {
	tests := map[string]func(*Options){
		"python path":          func(o *Options) { o.PythonPath = "python3" },
		"python identity":      func(o *Options) { o.PythonIdentity = "" },
		"temp root":            func(o *Options) { o.TempRoot = "relative" },
		"environment identity": func(o *Options) { o.Environment.Identity = "bad\nidentity" },
		"environment key":      func(o *Options) { o.Environment.Entries["BAD=KEY"] = "x" },
		"environment value":    func(o *Options) { o.Environment.Entries["BAD"] = "x\x00y" },
		"limit":                func(o *Options) { o.Limits.MaxSessions = 0 },
		"default timeout":      func(o *Options) { o.Limits.DefaultTimeout = 1500 * time.Millisecond },
		"timeout order":        func(o *Options) { o.Limits.DefaultTimeout = 6 * time.Second },
		"duration overflow":    func(o *Options) { o.Limits.TerminateGrace = time.Duration(math.MaxInt64) },
		"retention maximum":    func(o *Options) { o.Limits.MaxResultBytes = 16 << 20 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			options := testOptions(t)
			mutate(&options)
			if _, err := ConfigHash(options); err == nil || strings.Contains(err.Error(), "private-marker") {
				t.Fatalf("ConfigHash error = %v", err)
			}
		})
	}
}

func TestRetentionPoliciesCoverExactWorstCaseCopies(t *testing.T) {
	options, err := canonicalize(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	bounded := func(size int) BoundedText { return BoundedText{Text: strings.Repeat("\x00", size)} }
	results := []ExecuteResult{
		{
			Status: "completed", Stdout: bounded(options.limits.MaxOutputBytesPerStream), Stderr: bounded(options.limits.MaxOutputBytesPerStream),
			Result: bounded(options.limits.MaxResultBytes), Generation: math.MaxUint64, StateReset: true, StateResetReason: "runner_failed",
		},
		{
			Status: "python_error", Stdout: bounded(options.limits.MaxOutputBytesPerStream), Stderr: bounded(options.limits.MaxOutputBytesPerStream),
			Exception: bounded(options.limits.MaxExceptionBytes), Generation: math.MaxUint64, StateReset: true, StateResetReason: "runner_failed",
		},
	}
	maximum := 0
	for _, result := range results {
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > maximum {
			maximum = len(encoded)
		}
	}
	if got, want := options.retention[ExecuteToolName].MaxInlineBytes, int64(maximum*2); got != want {
		t.Fatalf("execute retention=%d want=%d", got, want)
	}
	clear, _ := json.Marshal(ClearResult{HadState: false, Generation: math.MaxUint64})
	if got, want := options.retention[ClearToolName].MaxInlineBytes, int64(len(clear)*2); got != want {
		t.Fatalf("clear retention=%d want=%d", got, want)
	}
}
