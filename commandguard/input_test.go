package commandguard

import (
	"context"
	"strings"
	"testing"
)

func TestInputStrictAndBounded(t *testing.T) {
	cases := []struct {
		raw  string
		want outcome
	}{
		{`{"cmd":"echo ok"}`, abstain}, {`{"extra":{"a":[1,true,null]},"cmd":"echo ok"}`, abstain},
		{`[]`, invalidCommand}, {`null`, invalidCommand}, {`{"cmd":null}`, invalidCommand}, {`{"cmd":1}`, invalidCommand}, {`{"cmd":[]}`, invalidCommand}, {`{}`, invalidCommand},
		{`{"cmd":"ok","cmd":"blocked"}`, invalidCommand}, {`{"extra":{"x":1,"x":2},"cmd":"ok"}`, invalidCommand}, {`{"cmd":"ok"} {}`, invalidCommand},
		{`{"cmd":"\u0000"}`, invalidCommand}, {`{"cmd":"ok","extra":"\u0000"}`, invalidCommand}, {`{"cmd":"ok","\u0000":0}`, invalidCommand}, {"{\"cmd\":\"\xff\"}", invalidCommand},
		{`{"cmd":`, invalidCommand}, {`{"cmd":"echo ok",}`, invalidCommand},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			p, _ := canonicalize(testOptions())
			a := analysis{ctx: context.Background(), policy: &p}
			_, got := a.input([]byte(tc.raw), "cmd")
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name, raw string
		change    func(*Limits)
	}{
		{"raw", `{"cmd":"echo ok"}`, func(l *Limits) { l.MaxRawInputBytes = 8 }},
		{"command", `{"cmd":"echo ok"}`, func(l *Limits) { l.MaxCommandBytes = 4 }},
		{"depth", `{"cmd":"ok","x":[[1]]}`, func(l *Limits) { l.MaxJSONDepth = 3 }},
		{"nodes", `{"cmd":"ok","x":[1,2,3]}`, func(l *Limits) { l.MaxJSONNodes = 7 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := testOptions()
			tc.change(&o.Limits)
			p, _ := canonicalize(o)
			a := analysis{ctx: context.Background(), policy: &p}
			if _, got := a.input([]byte(tc.raw), "cmd"); got != analysisLimit {
				t.Fatal(got)
			}
		})
	}
	p, _ := canonicalize(testOptions())
	a := analysis{ctx: context.Background(), policy: &p}
	if s, o := a.input([]byte(`{"literal.key":"echo ok","literal":{"key":"blocked"}}`), "literal.key"); o != abstain || s != "echo ok" {
		t.Fatal(s, o)
	}
	o := testOptions()
	o.Limits.MaxCommandBytes = 8
	p, _ = canonicalize(o)
	a.policy = &p
	if s, got := a.input([]byte(`{"cmd":"\u0065cho ok"}`), "cmd"); s != "echo ok" || got != abstain {
		t.Fatal(s, got)
	}
	if _, got := a.input([]byte(`{"cmd":"`+strings.Repeat(`\u0061`, 9)+`"}`), "cmd"); got != analysisLimit {
		t.Fatal(got)
	}
}
func TestAnalysisBudgets(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		change       func(*Limits)
	}{
		{"script", "echo 12345", func(l *Limits) { l.MaxCommandBytes = 8 }},
		{"bytes", `sh -c 'echo ok'`, func(l *Limits) { l.MaxCommandBytes = 18; l.MaxAnalysisBytes = 18 }},
		{"nodes", `echo ok`, func(l *Limits) { l.MaxASTNodes = 4 }},
		{"depth", `( ( echo ok ) )`, func(l *Limits) { l.MaxASTDepth = 5 }},
		{"words", `echo a b c`, func(l *Limits) { l.MaxWords = 3 }},
		{"word bytes", `echo abcdefghijklmnopqrstuvwxyz`, func(l *Limits) { l.MaxWordBytes = 20 }},
		{"nested words", `sh -c 'echo a b'`, func(l *Limits) { l.MaxWords = 5 }},
		{"nested depth", `sh -c 'sh -c "echo ok"'`, func(l *Limits) { l.MaxASTDepth = 8 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := testOptions()
			tc.change(&o.Limits)
			if got := analyze(t, tc.script, DialectPOSIX, o); got != analysisLimit {
				t.Fatal(got)
			}
		})
	}
}
