//go:build linux || darwin

package backgroundjobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/tools"
)

func testOptions() Options {
	return Options{
		ShellPath: "/bin/sh", ShellIdentity: "system-sh-v1",
		Environment: Environment{
			Mode: EnvironmentExplicitOnly, Identity: "test-environment-v1",
			Overrides: map[string]string{"PATH": "/usr/bin:/bin", "SAFE": "one"},
		},
		Limits: Limits{
			MaxRunning: 4, MaxTracked: 8, MaxCommandBytes: 4096,
			MaxWorkingDirectoryBytes: 1024, MaxOutputBytesPerStream: 1024,
			MaxEnvironmentEntries: 512, MaxEnvironmentBytes: 64 << 10,
			DefaultTimeout: 0, MaxTimeout: 5 * time.Second,
			TerminateGrace: 50 * time.Millisecond, KillWait: time.Second,
		},
	}
}

func TestConfigHashFreezesSafePolicy(t *testing.T) {
	options := testOptions()
	first, err := ConfigHash(options)
	if err != nil {
		t.Fatal(err)
	}
	options.Environment.Overrides = map[string]string{"SAFE": "one", "PATH": "/usr/bin:/bin"}
	second, err := ConfigHash(options)
	if err != nil || first != second {
		t.Fatalf("equivalent map hash = %q, %v; want %q", second, err, first)
	}
	options.Environment.Overrides["SAFE"] = "raw-secret-marker"
	third, err := ConfigHash(options)
	if err != nil || first != third {
		t.Fatalf("raw values changed hash = %q, %v; want %q", third, err, first)
	}
	options.Environment.Identity = "test-environment-v2"
	fourth, err := ConfigHash(options)
	if err != nil || fourth == first {
		t.Fatalf("identity did not change hash: %q %v", fourth, err)
	}
}

func TestConfigFreezesResolvedShellSymlink(t *testing.T) {
	directory := t.TempDir()
	first := directory + "/shell-one"
	second := directory + "/shell-two"
	link := directory + "/shell"
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	options.ShellPath = link
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	resolvedFirst, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.shellPath != resolvedFirst {
		t.Fatalf("resolved shell = %q, want %q", canonical.shellPath, resolvedFirst)
	}
	firstHash, err := ConfigHash(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	secondHash, err := ConfigHash(options)
	if err != nil || secondHash == firstHash || canonical.shellPath != resolvedFirst {
		t.Fatalf("retargeted shell hash=%q original=%q frozen=%q err=%v", secondHash, firstHash, canonical.shellPath, err)
	}
}

func TestEnvironmentSnapshotAndModes(t *testing.T) {
	t.Setenv("BACKGROUND_JOBS_AMBIENT", "ambient-one")
	explicit := testOptions()
	canonical, err := canonicalize(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(canonical.env, "\n"), "BACKGROUND_JOBS_AMBIENT") {
		t.Fatal("explicit-only inherited ambient environment")
	}
	inherit := testOptions()
	inherit.Environment.Mode = EnvironmentInheritAndOverride
	inherit.Environment.Overrides["BACKGROUND_JOBS_AMBIENT"] = "override"
	canonical, err = canonicalize(inherit)
	if err != nil {
		t.Fatal(err)
	}
	os.Setenv("BACKGROUND_JOBS_AMBIENT", "ambient-two")
	joined := strings.Join(canonical.env, "\n")
	if !strings.Contains(joined, "BACKGROUND_JOBS_AMBIENT=override") || strings.Contains(joined, "ambient-two") {
		t.Fatalf("environment was not frozen with override precedence: %q", joined)
	}
}

func TestCanonicalDiagnosticsDoNotFormatEnvironmentValues(t *testing.T) {
	options := testOptions()
	options.Environment.Overrides["SAFE"] = "raw-secret-marker"
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, formatted := range []string{fmt.Sprintf("%v", canonical), fmt.Sprintf("%#v", canonical)} {
		if strings.Contains(formatted, "raw-secret-marker") || strings.Contains(formatted, "PATH=") {
			t.Fatalf("canonical diagnostics leaked environment: %s", formatted)
		}
	}
}

func TestCanonicalEnvironmentIsDefensiveAgainstHostMapMutation(t *testing.T) {
	options := testOptions()
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash, err := configHash(canonical)
	if err != nil {
		t.Fatal(err)
	}
	options.Environment.Overrides["SAFE"] = "mutated"
	options.Environment.Overrides["NEW"] = "added"
	afterHash, err := configHash(canonical)
	joined := strings.Join(canonical.env, "\n")
	if err != nil || beforeHash != afterHash || !strings.Contains(joined, "SAFE=one") || strings.Contains(joined, "mutated") || strings.Contains(joined, "NEW=added") {
		t.Fatalf("canonical environment changed: hash=%q/%q env=%q err=%v", beforeHash, afterHash, joined, err)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := map[string]func(*Options){
		"shell required":       func(o *Options) { o.ShellPath = "" },
		"shell absolute":       func(o *Options) { o.ShellPath = "bin/sh" },
		"shell identity":       func(o *Options) { o.ShellIdentity = "" },
		"environment identity": func(o *Options) { o.Environment.Identity = "bad\nidentity" },
		"environment mode":     func(o *Options) { o.Environment.Mode = "mystery" },
		"running positive":     func(o *Options) { o.Limits.MaxRunning = 0 },
		"tracked covers run":   func(o *Options) { o.Limits.MaxTracked = 1 },
		"max timeout":          func(o *Options) { o.Limits.MaxTimeout = 500 * time.Millisecond },
		"default exact second": func(o *Options) { o.Limits.DefaultTimeout = 1500 * time.Millisecond },
		"default below max":    func(o *Options) { o.Limits.DefaultTimeout = 6 * time.Second },
		"termination overflow": func(o *Options) { o.Limits.TerminateGrace = time.Duration(1<<63 - 1) },
		"invalid env key":      func(o *Options) { o.Environment.Overrides["BAD=KEY"] = "value" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			options := testOptions()
			mutate(&options)
			if _, err := ConfigHash(options); err == nil || strings.Contains(err.Error(), "raw-secret-marker") {
				t.Fatalf("ConfigHash error = %v", err)
			}
		})
	}
}

func TestToolNormalizationIsStrictAndBounded(t *testing.T) {
	canonical, err := canonicalize(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newManager(canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	for _, definition := range definitions(canonical, manager) {
		materialized, err := tools.Materialize(context.Background(), definition, runtime.ToolScopeContext{SessionID: "session", WorkspaceID: "workspace", WorkspaceRoot: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		var raw []byte
		switch definition.Name {
		case StartToolName:
			raw = []byte(`{"command":"printf ok","unknown":true}`)
		case ListToolName:
			raw = []byte(`{"unknown":true}`)
		default:
			raw = []byte(`{"id":"job_00000000000000000000000000000000_0000000000000001","unknown":true}`)
		}
		if _, err := materialized.InputDecoder.DecodeToolInput(context.Background(), raw); !errors.Is(err, tools.ErrMalformedInput) {
			t.Fatalf("%s unknown property error = %v", definition.Name, err)
		}
	}
	start := definitions(canonical, manager)[0]
	materialized, _ := tools.Materialize(context.Background(), start, runtime.ToolScopeContext{})
	normalized, err := materialized.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"command":"printf ok"}`))
	if err != nil || string(normalized) != `{"command":"printf ok","working_directory":"."}` {
		t.Fatalf("normalized start = %s, %v", normalized, err)
	}
	if _, err := materialized.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"command":"x","timeout_seconds":6}`)); !errors.Is(err, tools.ErrMalformedInput) {
		t.Fatalf("above-maximum timeout error = %v", err)
	}
}
