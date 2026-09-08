package commandguard

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mattsp1290/eino-agent/runtime"
)

func fuzzGuard(t *testing.T, g *guard, raw []byte) {
	t.Helper()
	before := append([]byte(nil), raw...)
	req := runtime.ToolGuardRequest{ToolName: "shell", Call: runtime.ToolCall{Input: raw}}
	first, err := g.GuardTool(context.Background(), req)
	if err != nil {
		t.Fatalf("analysis produced an internal error: %v", err)
	}
	second, err := g.GuardTool(context.Background(), req)
	if err != nil || first != second {
		t.Fatal("nondeterministic analysis")
	}
	fixed := false
	for _, o := range []outcome{abstain, ruleMatch, invalidCommand, unanalysable, analysisLimit} {
		if first == denial(o) {
			fixed = true
		}
	}
	if !fixed || !bytes.Equal(raw, before) || len(g.permits) != 0 {
		t.Fatal("diagnostic, immutability, or admission invariant failed")
	}
	if len(raw) > g.policy.limits.MaxRawInputBytes && first != denial(analysisLimit) {
		t.Fatal("raw bound failed open")
	}
}
func FuzzCommandAnalysis(f *testing.F) {
	for _, s := range []string{"echo ok", "blocked", `git "$X"`, "echo '\xff'", "echo \x00", `echo "$(blocked)"`, "cat <<EOF\n$(blocked)\nEOF", strings.Repeat("(", 1000) + "echo ok" + strings.Repeat(")", 1000), strings.Repeat("env ", 50) + "echo ok", `sh -c 'bash -c "blocked"'`, `printf -v 'a[$(blocked)]' x`, `echo ${x@P}`, `echo $((a[$(blocked)]))`, strings.Repeat("'", 4000), `[blo'c'ked]`} {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, s string, bash bool) {
		o := testOptions()
		if len(s) > o.Limits.MaxRawInputBytes {
			return
		}
		o.Bindings = []Binding{{"shell", "cmd", DialectPOSIX}}
		if bash {
			o.Bindings[0].Dialect = DialectBash
		}
		p, err := canonicalize(o)
		if err != nil {
			t.Fatal(err)
		}
		g := newGuard(p)
		raw, _ := json.Marshal(map[string]string{"cmd": s})
		fuzzGuard(t, g, raw)
		if len(s) > o.Limits.MaxCommandBytes {
			got, _ := g.GuardTool(context.Background(), runtime.ToolGuardRequest{ToolName: "shell", Call: runtime.ToolCall{Input: raw}})
			if got.Decision != runtime.ToolGuardDeny {
				t.Fatal("script bound failed open")
			}
		}
	})
}
func FuzzBoundedInput(f *testing.F) {
	for _, raw := range []string{`{"cmd":"echo ok"}`, `{"cmd":"blocked","cmd":"echo ok"}`, `{"cmd":null}`, `{"cmd":"\u0000"}`, "{\"cmd\":\"\xff\"}", `{"cmd":"echo ok","extra":{"x":1,"x":2}}`, `{"cmd":"` + strings.Repeat(`\u0061`, 2000) + `"}`, `{"cmd":"echo ok","x":` + strings.Repeat("[", 1000) + "0" + strings.Repeat("]", 1000) + "}"} {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		p, err := canonicalize(testOptions())
		if err != nil {
			t.Fatal(err)
		}
		fuzzGuard(t, newGuard(p), raw)
	})
}
