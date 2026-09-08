package commandguard

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mattsp1290/eino-agent/runtime"
)

func analyze(t testing.TB, s string, d Dialect, o Options) outcome {
	t.Helper()
	p, err := canonicalize(o)
	if err != nil {
		t.Fatal(err)
	}
	a := analysis{ctx: context.Background(), policy: &p, parse: parseScript}
	return a.script(s, d, 0, 0)
}
func TestCommandCorpus(t *testing.T) {
	cases := []struct {
		s    string
		want outcome
	}{
		{`blocked`, ruleMatch}, {`bl{ock,ock}ed`, unanalysable}, {`/usr/bin/blocked`, ruleMatch}, {`./blocked`, ruleMatch}, {`bl'ock'ed`, ruleMatch}, {`bl\ocked`, ruleMatch}, {"bl\\\nocked", ruleMatch},
		{`echo blocked`, abstain}, {`git pushx`, abstain}, {`git status`, abstain}, {`git push origin`, ruleMatch}, {`git 'push'`, ruleMatch}, {`git "$ACTION"`, unanalysable}, {`git status "$PATHSPEC"`, abstain},
		{`$TOOL status`, unanalysable}, {`g* status`, unanalysable}, {`[blo'c'ked]`, unanalysable}, {`''`, unanalysable}, {`/usr/bin/`, unanalysable},
		{`echo ok; blocked`, ruleMatch}, {`false && blocked`, ruleMatch}, {`echo ok | blocked`, ruleMatch}, {`(blocked)`, ruleMatch}, {`{ blocked; }`, ruleMatch},
		{`echo "$(blocked)"`, ruleMatch}, {"echo `blocked`", ruleMatch}, {`X=$(blocked) echo ok`, ruleMatch}, {`echo ok >"$(blocked)"`, ruleMatch},
		{`echo '$(blocked)'`, abstain}, {"cat <<'EOF'\n$(blocked)\nEOF\n", abstain}, {"cat <<EOF\n$(blocked)\nEOF\n", ruleMatch},
		{`if false; then blocked; else echo ok; fi`, ruleMatch}, {`while false; do blocked; done`, ruleMatch}, {`until true; do blocked; done`, ruleMatch},
		{`for item in x; do blocked; done`, ruleMatch}, {`for item in "$(blocked)"; do echo ok; done`, ruleMatch}, {`case x in x) blocked;; esac`, ruleMatch}, {`case "$(blocked)" in x) echo ok;; esac`, ruleMatch},
		{`f() { echo ok; }`, unanalysable}, {`eval 'echo ok'`, unanalysable}, {`source file`, unanalysable}, {`trap 'blocked' EXIT`, unanalysable}, {`command trap 'blocked' 0`, unanalysable},
		{`printf -v 'a[$(blocked)0]' x`, unanalysable}, {`read 'a[$(blocked)0]'`, unanalysable}, {`unset 'a[$(blocked)0]'`, unanalysable}, {`test -v 'a[$(blocked)0]'`, unanalysable},
		{`printf '%s' "$VALUE"`, abstain}, {`printf -- "$FORMAT"`, abstain}, {`printf "$FORMAT"`, unanalysable}, {`printf`, abstain}, {`printf -x`, unanalysable},
		{`test x = y`, abstain}, {`[ x = y ]`, abstain}, {`[ x`, unanalysable}, {`test "$VALUE"`, unanalysable}, {`test -R a`, unanalysable},
		{`echo "$VALUE"`, abstain}, {`echo "${VALUE}"`, abstain}, {`echo "${VALUE:-ok}"`, unanalysable}, {`echo $((1+2))`, unanalysable},
		{`echo "`, invalidCommand}, {``, abstain}, {"# comment\n", abstain}, {`git -C repo push`, abstain}, {`git`, abstain},
		{`"blocked"`, ruleMatch}, {`'blocked'`, ruleMatch}, {`git "pu\sh"`, abstain}, {`git pu\sh`, ruleMatch},
		{`echo "*?[~"`, abstain}, {`'g*' status`, abstain}, {`~user/tool`, unanalysable},
	}
	for _, d := range []Dialect{DialectPOSIX, DialectBash} {
		for _, tc := range cases {
			t.Run(string(d)+"/"+tc.s, func(t *testing.T) {
				if got := analyze(t, tc.s, d, testOptions()); got != tc.want {
					t.Fatalf("got %v want %v", got, tc.want)
				}
			})
		}
	}
}
func TestBashGrammar(t *testing.T) {
	cases := []struct {
		s    string
		want outcome
	}{
		{`echo <(blocked)`, ruleMatch}, {`echo >(blocked)`, ruleMatch}, {`[[ x = y ]]`, unanalysable}, {`(( 1 ))`, unanalysable}, {`for ((i=0;i<1;i++)); do echo x; done`, unanalysable},
		{`a=(x y)`, unanalysable}, {`a[0]=x`, unanalysable}, {`declare x`, unanalysable}, {`let x=1`, unanalysable}, {`time echo x`, unanalysable}, {`coproc echo x`, unanalysable},
		{`echo "${value@P}"`, unanalysable}, {`echo "${!value}"`, unanalysable}, {`echo "${#value}"`, unanalysable}, {`echo "${value[0]}"`, unanalysable},
		{`$'blocked'`, unanalysable}, {`$"blocked"`, unanalysable}, {`echo $'data'`, abstain}, {`git $'push'`, unanalysable}, {`echo $"data"`, abstain},
		{`bl{ock,ock}ed`, unanalysable}, {`echo {a,b}`, abstain}, {`echo "{a,b}"`, abstain},
	}
	for _, tc := range cases {
		t.Run(tc.s, func(t *testing.T) {
			if got := analyze(t, tc.s, DialectBash, testOptions()); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	if got := analyze(t, `echo <(echo ok)`, DialectPOSIX, testOptions()); got != invalidCommand {
		t.Fatalf("POSIX accepted Bash: %v", got)
	}
}
func TestExactWordDecoding(t *testing.T) {
	for _, tc := range []struct {
		script string
		prefix []string
	}{
		{`tool ''`, []string{""}}, {`tool λ`, []string{"λ"}}, {`tool "a\qb"`, []string{`a\qb`}}, {`tool "a\$b"`, []string{"a$b"}}, {`tool a\ b`, []string{"a b"}}, {`tool '*'`, []string{"*"}}, {`tool \~`, []string{"~"}}, {`tool "a\\b"`, []string{`a\b`}}, {"tool 'a\r\nb'", []string{"a\r\nb"}},
	} {
		t.Run(tc.script, func(t *testing.T) {
			o := testOptions()
			o.Rules = []Rule{{"word", "tool", tc.prefix}}
			if got := analyze(t, tc.script, DialectPOSIX, o); got != ruleMatch {
				t.Fatalf("got %v", got)
			}
		})
	}
	o := testOptions()
	o.Rules = []Rule{{"prefix", "tool", []string{"a", "b"}}}
	for _, s := range []string{`tool "$X" mismatch`, `tool a "$X" mismatch`} {
		if got := analyze(t, s, DialectPOSIX, o); got != unanalysable {
			t.Fatal(s, got)
		}
	}
}
func guardRequest(name, field, command string) runtime.ToolGuardRequest {
	raw, _ := json.Marshal(map[string]string{field: command})
	return runtime.ToolGuardRequest{ToolName: name, Call: runtime.ToolCall{Input: raw}}
}

func TestOpaqueBuiltinInventoryDirectAndWrapped(t *testing.T) {
	for _, name := range []string{"eval", ".", "source", "alias", "unalias", "builtin", "enable", "trap", "fc", "history", "bind", "complete", "compgen", "read", "unset", "getopts", "mapfile", "readarray", "declare", "typeset", "local", "export", "readonly", "let"} {
		for _, dialect := range []Dialect{DialectPOSIX, DialectBash} {
			for _, prefix := range []string{"", "command -- ", "env -i "} {
				t.Run(string(dialect)+"/"+prefix+name, func(t *testing.T) {
					if got := analyze(t, prefix+name+" ignored", dialect, testOptions()); got != unanalysable {
						t.Fatalf("opaque builtin got %v", got)
					}
				})
			}
		}
	}
}
