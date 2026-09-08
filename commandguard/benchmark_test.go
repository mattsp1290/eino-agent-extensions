package commandguard

import (
	"context"
	"strings"
	"testing"
)

func BenchmarkParserConstruction(b *testing.B) {
	// Construction is deliberately measured before any AST node/depth checks.
	// Every script fits the example's 4096-byte per-script bound.
	for name, s := range map[string]string{
		"ordinary":      "git status --short",
		"parentheses":   strings.Repeat("(", 2000) + "echo ok" + strings.Repeat(")", 2000),
		"substitutions": strings.Repeat("echo $(", 500) + "echo ok" + strings.Repeat(")", 500),
		"quotes":        "echo '" + strings.Repeat("x", 4000) + "'",
		"heredoc":       "cat <<EOF\n" + strings.Repeat("x", 4000) + "\nEOF\n",
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := parseScript(strings.NewReader(s), DialectPOSIX); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
func BenchmarkAnalysis(b *testing.B) {
	for name, s := range map[string]string{"ordinary": "git status --short", "nested": "sh -c 'sh -c \"echo ok\"'", "adversarial": strings.Repeat("(", 2000) + "echo ok" + strings.Repeat(")", 2000)} {
		b.Run(name, func(b *testing.B) {
			p, _ := canonicalize(testOptions())
			g := newGuard(p)
			req := guardRequest("shell", "cmd", s)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := g.GuardTool(context.Background(), req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
func BenchmarkConcurrentCapacity(b *testing.B) {
	p, _ := canonicalize(testOptions())
	g := newGuard(p)
	for range p.limits.MaxInFlight {
		g.permits <- struct{}{}
	}
	req := guardRequest("shell", "cmd", "echo ok")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			got, err := g.GuardTool(context.Background(), req)
			if err != nil || got != denial(capacity) {
				b.Error("capacity outcome changed")
			}
		}
	})
}
