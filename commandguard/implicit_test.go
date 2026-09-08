package commandguard

import "testing"

func TestShellSpecialVariableTargets(t *testing.T) {
	for _, dialect := range []Dialect{DialectPOSIX, DialectBash} {
		for _, script := range []string{
			`OPTIND='a[$(blocked)0]'`,
			`RANDOM='a[$(blocked)0]'`,
			`SRANDOM='a[$(blocked)0]'`,
			`HISTCMD='a[$(blocked)0]'`,
			`: "$SECONDS"; SECONDS='a[$(blocked)0]'`,
			`for OPTIND in 'a[$(blocked)0]'; do :; done`,
			`PS4='$(blocked)'; set -x; echo ok`,
			`PS4='$(blocked)' sh -x -c 'echo ok'`,
			`env 'PS4=$(blocked)' sh -x -c 'echo ok'`,
			`sudo 'PS4=$(blocked)' sh -x -c 'echo ok'`,
			`env 'BASH_ENV=$(blocked)' bash -c 'echo ok'`,
		} {
			t.Run(string(dialect)+"/"+script, func(t *testing.T) {
				if got := analyze(t, script, dialect, testOptions()); got != unanalysable {
					t.Fatalf("implicit execution: got %v, want unanalysable", got)
				}
			})
		}
		for _, script := range []string{
			`SAFE='$(blocked)'; echo "$SAFE"`,
			`for item in '$(blocked)'; do echo "$item"; done`,
			`env 'SAFE=$(blocked)' sh -c 'echo "$SAFE"'`,
			`sudo 'SAFE=$(blocked)' sh -c 'echo "$SAFE"'`,
		} {
			t.Run(string(dialect)+"/safe/"+script, func(t *testing.T) {
				if got := analyze(t, script, dialect, testOptions()); got != abstain {
					t.Fatalf("ordinary variable data: got %v, want abstain", got)
				}
			})
		}
	}
}

func TestExtendedGlobsAreOpaque(t *testing.T) {
	for _, script := range []string{
		"shopt -s extglob\necho @(x|$(blocked))",
		"shopt -s extglob\ncase x in @(x|$(blocked))) echo ok;; esac",
		`echo @(x|y)`,
	} {
		t.Run(script, func(t *testing.T) {
			if got := analyze(t, script, DialectBash, testOptions()); got != unanalysable {
				t.Fatalf("opaque extended glob: got %v, want unanalysable", got)
			}
		})
	}
	if got := analyze(t, `echo '@(x|$(blocked))'`, DialectBash, testOptions()); got != abstain {
		t.Fatalf("quoted literal: got %v, want abstain", got)
	}
}
