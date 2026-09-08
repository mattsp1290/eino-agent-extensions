//go:build linux || darwin

package rtkreducer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type realRTKFixture struct {
	path    string
	options Options
}

func requireRealRTK(t *testing.T) realRTKFixture {
	t.Helper()
	path := os.Getenv("RTK_REDUCER_TEST_EXECUTABLE")
	if path == "" {
		t.Skip("RTK_REDUCER_TEST_EXECUTABLE is not set")
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("RTK_REDUCER_TEST_EXECUTABLE must be absolute: %q", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve RTK executable: %v", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		t.Fatalf("RTK executable is not a regular executable: %q", resolved)
	}
	versionCtx, cancel := context.WithTimeout(context.Background(), realRTKVersionTimeout)
	defer cancel()
	versionCommand := exec.CommandContext(versionCtx, resolved, "--version") //nolint:gosec // explicit test fixture path
	versionCommand.Env = []string{"HOME=" + t.TempDir(), "RTK_TELEMETRY_DISABLED=1", "NO_COLOR=1", "TERM=dumb", "LANG=C", "LC_ALL=C"}
	versionOutput, err := versionCommand.Output()
	if err != nil {
		t.Fatalf("run RTK --version: %v", err)
	}
	if strings.TrimSpace(string(versionOutput)) != "rtk 0.48.0" {
		t.Fatalf("unexpected RTK version: %q", strings.TrimSpace(string(versionOutput)))
	}
	contents, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	options.ExecutablePath = resolved
	options.ExecutableSHA256 = hex.EncodeToString(digest[:])
	options.TempRoot = tempRoot
	options.Bindings = []Binding{{
		ToolName: integrationToolName,
		Mode:     JSONMirror,
		MatchInput: &InputMatch{Field: "cmd", Equals: []string{
			"go test -json ./...",
		}},
		Fields: []Field{{Name: "stdout", Filter: GoTest}},
	}}
	return realRTKFixture{path: resolved, options: options}
}

const realRTKVersionTimeout = 5 * time.Second

func TestRealRTKExecutableEnvIsExplicit(t *testing.T) {
	if os.Getenv("RTK_REDUCER_TEST_EXECUTABLE") == "" {
		return
	}
	fixture := requireRealRTK(t)
	if fixture.path == "" || fixture.options.ExecutableSHA256 == "" {
		t.Fatal("real RTK fixture did not produce immutable identity")
	}
}

func TestRealRTKGoTestPreservesRepresentativeFailures(t *testing.T) {
	fixture := requireRealRTK(t)
	coordinator := newCoordinator(mustCanonicalOptions(t, fixture.options))
	defer func() {
		if err := coordinator.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}()

	fixtures := map[string]struct {
		events   string
		sentinel string
	}{
		"individual failure": {
			events: strings.Join([]string{
				`{"Action":"run","Package":"fixture/pkg","Test":"TestFailure"}`,
				`{"Action":"output","Package":"fixture/pkg","Test":"TestFailure","Output":"Error: INDIVIDUAL_FAILURE_SENTINEL expected 5, got 3\n"}`,
				`{"Action":"fail","Package":"fixture/pkg","Test":"TestFailure","Elapsed":0.01}`,
				`{"Action":"fail","Package":"fixture/pkg","Elapsed":0.02}`,
			}, "\n") + "\n",
			sentinel: "INDIVIDUAL_FAILURE_SENTINEL",
		},
		"package panic timeout": {
			events: strings.Join([]string{
				`{"Action":"output","Package":"fixture/pkg","Output":"panic: test timed out after 1s PACKAGE_TIMEOUT_SENTINEL\n"}`,
				`{"Action":"output","Package":"fixture/pkg","Output":"goroutine 7 [running]: PANIC_STACK_SENTINEL\n"}`,
				`{"Action":"fail","Package":"fixture/pkg","Elapsed":1.0}`,
			}, "\n") + "\n",
			sentinel: "PACKAGE_TIMEOUT_SENTINEL",
		},
		"build failure": {
			events: strings.Join([]string{
				`{"ImportPath":"fixture/pkg","Action":"build-output","Output":"BUILD_FAILURE_SENTINEL.go:10:5: undefined: missingFunc\n"}`,
				`{"ImportPath":"fixture/pkg","Action":"build-fail"}`,
				`{"Action":"fail","Package":"fixture/pkg","Elapsed":0,"FailedBuild":"fixture/pkg"}`,
			}, "\n") + "\n",
			sentinel: "BUILD_FAILURE_SENTINEL",
		},
	}

	for name, test := range fixtures {
		t.Run(name, func(t *testing.T) {
			input := strings.Repeat(`{"Action":"pass","Package":"fixture/pass","Test":"TestPass","Elapsed":0.001}`+"\n", 80) + test.events
			started := time.Now()
			output, ok := coordinator.reduce(context.Background(), GoTest, input)
			if !ok {
				t.Fatal("real RTK reduction failed")
			}
			if !strings.Contains(output, test.sentinel) {
				t.Fatalf("real RTK lost %s: %q", test.sentinel, output)
			}
			if len(output) >= len(input) {
				t.Fatalf("real RTK did not reduce representative failure: input=%d output=%d", len(input), len(output))
			}
			t.Logf("real RTK %s: input_bytes=%d output_bytes=%d wall=%s", name, len(input), len(output), time.Since(started))
		})
	}
}

func TestRealRTKIgnoresAmbientConfigHomeAndWorkspace(t *testing.T) {
	fixture := requireRealRTK(t)
	ambient := t.TempDir()
	home := filepath.Join(ambient, "home")
	configRoot := filepath.Join(ambient, "config")
	workspace := filepath.Join(ambient, "workspace")
	for _, directory := range []string{home, filepath.Join(configRoot, "rtk"), workspace} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	hostileConfig := filepath.Join(configRoot, "rtk", "config.toml")
	const hostileContents = "this is deliberately invalid RTK configuration"
	if err := os.WriteFile(hostileConfig, []byte(hostileContents), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceCanary := filepath.Join(workspace, "WORKSPACE_CANARY")
	if err := os.WriteFile(workspaceCanary, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_DATA_HOME", filepath.Join(ambient, "data"))
	t.Setenv("RTK_CONFIG_PATH", hostileConfig)
	t.Setenv("PWD", workspace)

	coordinator := newCoordinator(mustCanonicalOptions(t, fixture.options))
	inspection := make(chan error, 1)
	coordinator.inspectInvocation = func(dirs invocationDirs) {
		inspection <- filepath.Walk(dirs.root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(dirs.root, path)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("private side effect escaped invocation root: %s", path)
			}
			if info.IsDir() && info.Mode().Perm()&0o022 != 0 {
				return fmt.Errorf("private directory is writable by group/other: %s mode=%o", relative, info.Mode().Perm())
			}
			return nil
		})
	}
	input := verboseLogFixture()
	output, ok := coordinator.reduce(context.Background(), Log, input)
	if !ok || len(output) >= len(input) {
		t.Fatalf("private-environment RTK reduction failed: input=%d output=%d ok=%v", len(input), len(output), ok)
	}
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-inspection; err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(hostileConfig); err != nil || string(raw) != hostileContents {
		t.Fatalf("ambient RTK config was accessed or modified: raw=%q err=%v", raw, err)
	}
	if raw, err := os.ReadFile(workspaceCanary); err != nil || string(raw) != "unchanged" {
		t.Fatalf("workspace canary was modified: raw=%q err=%v", raw, err)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("ambient home gained RTK state: entries=%v err=%v", entries, err)
	}
}

func TestRealRTKAllocationAndRetainedBufferMeasurement(t *testing.T) {
	fixture := requireRealRTK(t)
	canonical := mustCanonicalOptions(t, fixture.options)
	coordinator := newCoordinator(canonical)
	defer func() {
		if err := coordinator.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}()
	input := verboseLogFixture()
	lastOutputBytes := 0
	allSucceeded := true
	allocations := testing.AllocsPerRun(3, func() {
		output, ok := coordinator.reduce(context.Background(), Log, input)
		lastOutputBytes = len(output)
		allSucceeded = allSucceeded && ok
	})
	if !allSucceeded || lastOutputBytes == 0 || lastOutputBytes >= len(input) || allocations <= 0 || allocations > 10_000 {
		t.Fatalf("unexpected real RTK measurement: ok=%v input=%d output=%d allocations=%.1f", allSucceeded, len(input), lastOutputBytes, allocations)
	}
	streamBufferCap := canonical.limits.MaxStdoutBytes + canonical.limits.MaxStderrBytes
	t.Logf("real RTK bounded measurement: input_bytes=%d output_bytes=%d allocations_per_run=%.1f retained_stream_cap_bytes=%d max_concurrent=%d", len(input), lastOutputBytes, allocations, streamBufferCap, canonical.limits.MaxConcurrent)
}

func mustCanonicalOptions(t *testing.T, options Options) canonicalOptions {
	t.Helper()
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
