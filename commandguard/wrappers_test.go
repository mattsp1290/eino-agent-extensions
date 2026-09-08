package commandguard

import (
	"strings"
	"testing"
)

func TestWrapperAcceptedForms(t *testing.T) {
	prefixes := []string{
		"env ", "env -i ", "env --ignore-environment ", "env -u NAME ", "env --unset=NAME ", "env -C /tmp ", "env --chdir=/tmp ", "env -i -u NAME -C /tmp -- SAFE=x ", "env SAFE=x ",
		"command ", "command -p ", "command -- ", "command -p -- ", "exec ", "exec -- ",
		"sudo ", "sudo -n -E -H ", "sudo -u user ", "sudo --user=user ", "sudo -g group ", "sudo --group=group ", "sudo -D /tmp ", "sudo --chdir=/tmp ", "sudo -n -u root -- SAFE=value ",
		"timeout 1 ", "timeout --foreground --preserve-status -v --verbose 1.5s ", "timeout -s TERM 1m ", "timeout --signal=SIGTERM 0.1h ", "timeout -s 9 0d ", "timeout -k 0.2s 1 ", "timeout --kill-after=0.5m -- 1 ",
		"timeout -s TERM 1s command -p -- env -i ", "/usr/bin/env /usr/bin/command ",
	}
	for _, prefix := range prefixes {
		t.Run(prefix, func(t *testing.T) {
			if got := analyze(t, prefix+"blocked", DialectPOSIX, testOptions()); got != ruleMatch {
				t.Fatalf("blocked got %v", got)
			}
			if got := analyze(t, prefix+`echo "$DATA"`, DialectPOSIX, testOptions()); got != abstain {
				t.Fatalf("allowed got %v", got)
			}
		})
	}
	for _, name := range []string{"env", "command", "exec", "sudo", "timeout"} {
		o := testOptions()
		o.Rules = []Rule{{ID: "outer", Executable: name}}
		cmd := name + " echo ok"
		if name == "timeout" {
			cmd = name + " 1 echo ok"
		}
		if got := analyze(t, cmd, DialectPOSIX, o); got != ruleMatch {
			t.Fatal("outer wrapper rule missed", name, got)
		}
	}
	for _, s := range []string{"env", "env -i", "env -- SAFE=x", "env -u NAME"} {
		if got := analyze(t, s, DialectPOSIX, testOptions()); got != abstain {
			t.Fatal(s, got)
		}
	}
}
func TestWrapperUnsupportedForms(t *testing.T) {
	cases := []string{
		`env -S 'blocked'`, `env --split-string='blocked'`, `env --default-signal blocked`, `env -u`, `env -C`, `env -u "$NAME" blocked`, `env --unset= blocked`, `env --chdir= blocked`, `env -u A-B blocked`, `env A-B=x blocked`, `env "$ASSIGN" blocked`,
		`command`, `command -v blocked`, `command -V blocked`, `command -pp blocked`, `command -p -p blocked`, `command -p -v blocked`, `command "$CMD"`,
		`exec`, `exec -a name blocked`, `exec -c blocked`, `exec -l blocked`, `exec "$CMD"`,
		`sudo`, `sudo SAFE=x -s blocked`, `sudo SAFE=x -- blocked`, `sudo -i blocked`, `sudo -s blocked`, `sudo -e blocked`, `sudo -nE blocked`, `sudo -u`, `sudo -g`, `sudo -D`, `sudo -u "$USER" blocked`, `sudo --user= blocked`, `sudo --preserve-env=NAME blocked`, `sudo -h host blocked`, `sudo -R /tmp blocked`,
		`timeout`, `timeout 1`, `timeout -s`, `timeout -k`, `timeout --signal= 1 blocked`, `timeout -s 0 1 blocked`, `timeout -s 'bad!' 1 blocked`, `timeout -k bad 1 blocked`, `timeout -s "$SIGNAL" 1 blocked`, `timeout --unknown 1 blocked`, `timeout -vs TERM 1 blocked`, `timeout "$DURATION" blocked`, `timeout 1.2.3 blocked`, `timeout -1 blocked`, `timeout 1ms blocked`,
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			if got := analyze(t, s, DialectPOSIX, testOptions()); got != unanalysable {
				t.Fatalf("got %v", got)
			}
		})
	}
}
func TestNestedShellGrammar(t *testing.T) {
	for _, name := range []string{"sh", "bash", "/bin/sh", "/usr/bin/bash"} {
		for _, flags := range []string{"-c", "-lc", "-e -u -x -c"} {
			for _, tc := range []struct {
				script string
				want   outcome
			}{{"blocked", ruleMatch}, {"echo ok", abstain}, {"", abstain}, {`git "$1"`, unanalysable}} {
				s := name + " " + flags + " '" + tc.script + `' name "$DATA"`
				t.Run(s, func(t *testing.T) {
					if got := analyze(t, s, DialectPOSIX, testOptions()); got != tc.want {
						t.Fatalf("got %v want %v", got, tc.want)
					}
				})
			}
		}
		for _, suffix := range []string{`-- -c 'echo ok'`, `-- -lc 'echo ok'`, `-e -- -lc 'echo ok'`, `-c -- 'blocked'`, `-c -e 'blocked'`, `-c +e 'blocked'`, `-c - 'blocked'`, `-c "$SCRIPT"`, `"$SCRIPT"`, `file.sh`, `-s`, `-c`, `-ec 'echo ok'`, `-c 'echo ok' "$ZERO"`} {
			s := name + " " + suffix
			t.Run(s, func(t *testing.T) {
				if got := analyze(t, s, DialectPOSIX, testOptions()); got != unanalysable {
					t.Fatalf("got %v", got)
				}
			})
		}
	}
	for _, s := range []string{`sh -c 'echo ok' "$(blocked)"`, `sh -c 'echo ok' name "$(blocked)"`, `bash -c 'echo <(blocked)'`} {
		if got := analyze(t, s, DialectPOSIX, testOptions()); got != ruleMatch {
			t.Fatal(s, got)
		}
	}
	if got := analyze(t, `sh -c 'echo <(echo ok)'`, DialectBash, testOptions()); got != invalidCommand {
		t.Fatal("nested dialect", got)
	}
	o := testOptions()
	o.Limits.MaxWrapperDepth = 2
	if got := analyze(t, `command env echo ok`, DialectPOSIX, o); got != abstain {
		t.Fatal(got)
	}
	for _, s := range []string{`command env command echo ok`, `command sh -c 'command echo ok'`, strings.Repeat("env ", 20) + "echo ok"} {
		if got := analyze(t, s, DialectPOSIX, o); got != analysisLimit {
			t.Fatal(s, got)
		}
	}
}
