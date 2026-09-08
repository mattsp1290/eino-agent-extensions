//go:build linux || darwin

package commandguard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/session"
)

func bashFive(t *testing.T) string {
	t.Helper()
	explicit := os.Getenv("COMMAND_GUARD_TEST_BASH")
	candidates := []string{explicit, "/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/bin/bash"}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		out, err := exec.CommandContext(ctx, candidate, "--noprofile", "--norc", "-c", `printf '%s' "${BASH_VERSINFO[0]}"`).Output()
		cancel()
		major, _ := strconv.Atoi(string(out))
		if err == nil && major >= 5 {
			return candidate
		}
		if candidate == explicit {
			t.Fatalf("explicit Bash must be version 5+: %s", candidate)
		}
	}
	if os.Getenv("CI") != "" {
		t.Fatal("CI requires COMMAND_GUARD_TEST_BASH pointing to Bash 5+")
	}
	t.Skip("Bash 5+ unavailable; required CI gate remains pending")
	return ""
}
func runShellReference(t *testing.T, binary, workspace, script string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-c", script)
	cmd.Dir = workspace
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "ENV=", "BASH_ENV="}
	// test/unset truth statuses are irrelevant; only the harmless canary proves
	// the shell's deferred/implicit interpretation of the literal operand.
	if err := cmd.Run(); ctx.Err() != nil {
		t.Fatal("reference timed out", err)
	}
}
func TestShellOptionTerminationProduction(t *testing.T) {
	bash := bashFive(t)
	for _, name := range []string{"sh", bash} {
		for _, form := range []string{"-- -c", "-- -lc", "-e -- -c", "-u -- -lc"} {
			t.Run(name+" "+form, func(t *testing.T) {
				f := newIntegrationFixture(t)
				f.standard("/bin/sh")
				f.guard(testOptions())
				canary := filepath.Join(f.workspace, "option-canary")
				for _, file := range []string{"-c", "-lc"} {
					if err := os.WriteFile(filepath.Join(f.workspace, file), []byte("printf OPTION > "+shellQuote(canary)+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				script := shellQuote(name) + " " + form + " 'echo harmless'"
				f.run(toolCall("option", "shell", "cmd", script))
				if f.permissionCalls.Load() != 0 || f.call("option").Status != session.ToolCallFailed {
					t.Fatal("unsupported option boundary reached executor")
				}
				if _, err := os.Stat(canary); !os.IsNotExist(err) {
					t.Fatal("script-file canary executed")
				}
			})
		}
	}
	for _, operand := range []string{"--", "-e", "+e", "-"} {
		t.Run("script operand "+operand, func(t *testing.T) {
			f := newIntegrationFixture(t)
			f.standard("/bin/sh")
			f.guard(testOptions())
			f.run(toolCall("operand", "shell", "cmd", shellQuote(bash)+" -c "+shellQuote(operand)+" 'echo harmless'"))
			if f.permissionCalls.Load() != 0 || f.call("operand").Status != session.ToolCallFailed {
				t.Fatal("option-shaped SCRIPT executed")
			}
		})
	}
	for _, script := range []string{`sh -c '' name "$DATA"`, `sh -c 'echo ok' name "$DATA"`} {
		t.Run(script, func(t *testing.T) {
			f := newIntegrationFixture(t)
			f.standard("/bin/sh")
			f.guard(testOptions())
			f.run(toolCall("control", "shell", "cmd", script))
			if f.permissionCalls.Load() != 1 || f.call("control").Status != session.ToolCallCompleted {
				t.Fatal("valid shell script control failed")
			}
		})
	}
	// Independently prove the file-name boundary with a harmless Bash reference.
	dir := t.TempDir()
	canary := filepath.Join(dir, "reference")
	if err := os.WriteFile(filepath.Join(dir, "-c"), []byte("printf OPTION > "+shellQuote(canary)), 0600); err != nil {
		t.Fatal(err)
	}
	runShellReference(t, "/bin/sh", dir, shellQuote(bash)+" -- -c 'echo harmless'")
	if _, err := os.Stat(canary); err != nil {
		t.Fatal("reference failed to demonstrate -c file execution", err)
	}
}
func TestBuiltinImplicitExecutionProductionAndReferences(t *testing.T) {
	bash := bashFive(t)
	for _, name := range []string{"trap", "printf", "read", "unset", "test"} {
		for _, dialect := range []Dialect{DialectPOSIX, DialectBash} {
			t.Run(name+"/"+string(dialect), func(t *testing.T) {
				f := newIntegrationFixture(t)
				f.standard(bash)
				o := testOptions()
				o.Bindings = []Binding{{"shell", "cmd", dialect}}
				f.guard(o)
				canary := filepath.Join(f.workspace, "implicit-canary")
				command := "printf MARKER > " + shellQuote(canary)
				operand := shellQuote("a[$(" + command + ")0]")
				script, preamble := "", ""
				switch name {
				case "trap":
					script = "trap " + shellQuote(command) + " EXIT"
				case "printf":
					script = "printf -v " + operand + " x"
				case "read":
					script = "read " + operand + " <<EOF\nx\nEOF"
				case "unset":
					script = "unset " + operand
					preamble = "a=(x); "
				case "test":
					script = "test -v " + operand
					preamble = "a=(x); "
				}
				f.run(toolCall("implicit", "shell", "cmd", script))
				if f.permissionCalls.Load() != 0 || !strings.Contains(string(f.call("implicit").Output), unanalysable.message()) {
					t.Fatal("implicit execution reached permission/body")
				}
				if _, err := os.Stat(canary); !os.IsNotExist(err) {
					t.Fatal("guarded builtin wrote canary")
				}
				runShellReference(t, bash, f.workspace, preamble+script)
				if data, err := os.ReadFile(canary); err != nil || string(data) != "MARKER" {
					t.Fatal(fmt.Sprintf("reference %s did not demonstrate implicit execution", name), err, string(data))
				}
			})
		}
	}
}
