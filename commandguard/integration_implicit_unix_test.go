//go:build linux || darwin

package commandguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattsp1290/eino-agent/session"
)

func TestImplicitSyntaxProductionAndReferences(t *testing.T) {
	bash := bashFive(t)
	cases := []struct {
		name     string
		dialects []Dialect
		script   func(string) string
	}{
		{"arithmetic scalar", []Dialect{DialectPOSIX, DialectBash}, func(command string) string {
			return "OPTIND=" + shellQuote("a[$("+command+")0]")
		}},
		{"random scalar", []Dialect{DialectPOSIX, DialectBash}, func(command string) string {
			return "RANDOM=" + shellQuote("a[$("+command+")0]")
		}},
		{"seconds scalar after read", []Dialect{DialectPOSIX, DialectBash}, func(command string) string {
			return `: "$SECONDS"; SECONDS=` + shellQuote("a[$("+command+")0]")
		}},
		{"loop target", []Dialect{DialectPOSIX, DialectBash}, func(command string) string {
			return "for OPTIND in " + shellQuote("a[$("+command+")0]") + "; do :; done"
		}},
		{"trace prompt", []Dialect{DialectPOSIX, DialectBash}, func(command string) string {
			return "PS4=" + shellQuote("$("+command+")") + "; set -x; echo ok"
		}},
		{"inline trace prompt", []Dialect{DialectPOSIX, DialectBash}, func(command string) string {
			return "PS4=" + shellQuote("$("+command+")") + " sh -x -c 'echo ok'"
		}},
		{"wrapper trace prompt", []Dialect{DialectPOSIX, DialectBash}, func(command string) string {
			return "env " + shellQuote("PS4=$("+command+")") + " sh -x -c 'echo ok'"
		}},
		{"startup variable", []Dialect{DialectPOSIX, DialectBash}, func(command string) string {
			return "env " + shellQuote("BASH_ENV=$("+command+")") + " " + shellQuote(bash) + " -c 'echo ok'"
		}},
		{"extended glob argument", []Dialect{DialectBash}, func(command string) string {
			return "shopt -s extglob\necho @(x|$(" + command + "))"
		}},
		{"extended glob case", []Dialect{DialectBash}, func(command string) string {
			return "shopt -s extglob\ncase x in @(x|$(" + command + "))) echo ok;; esac"
		}},
	}
	for _, tc := range cases {
		for _, dialect := range tc.dialects {
			t.Run(tc.name+"/"+string(dialect), func(t *testing.T) {
				f := newIntegrationFixture(t)
				f.standard(bash)
				o := testOptions()
				o.Bindings = []Binding{{"shell", "cmd", dialect}}
				o.Rules = []Rule{{ID: "canary", Executable: "printf", ArgPrefix: []string{"MARKER"}}}
				f.guard(o)
				canary := filepath.Join(f.workspace, "implicit-canary")
				script := tc.script("printf MARKER > " + shellQuote(canary))
				f.run(toolCall("implicit", "shell", "cmd", script))
				call := f.call("implicit")
				if f.permissionCalls.Load() != 0 || call.Status != session.ToolCallFailed || !strings.Contains(string(call.Output), unanalysable.message()) {
					t.Fatal("implicit execution was not denied before permissions")
				}
				if _, err := os.Stat(canary); !os.IsNotExist(err) {
					t.Fatal("guarded command wrote canary", err)
				}
				runShellReference(t, bash, f.workspace, script)
				if data, err := os.ReadFile(canary); err != nil || string(data) != "MARKER" {
					t.Fatal("reference did not demonstrate implicit execution", err, string(data))
				}
			})
		}
	}
}
