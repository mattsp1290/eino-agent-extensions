package commandguard

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type word struct {
	text  string
	known bool
}

// decode removes ordinary shell quoting only. Nested executable syntax has
// already been visited; no environment or expansion engine is consulted.
func (a *analysis) decode(w *syntax.Word, carriage byte) (word, outcome) {
	var b strings.Builder
	known := true
	bracket := -1
	appendText := func(s string) bool {
		if len(s) > a.policy.limits.MaxWordBytes-b.Len() {
			return false
		}
		if carriage != 0 {
			s = strings.ReplaceAll(s, string(carriage), "\r")
		}
		b.WriteString(s)
		return true
	}
	var parts func([]syntax.WordPart, bool) outcome
	parts = func(ps []syntax.WordPart, quoted bool) outcome {
		for _, part := range ps {
			if a.ctx.Err() != nil {
				return invalidCommand
			}
			switch p := part.(type) {
			case *syntax.Lit:
				for i := 0; i < len(p.Value); i++ {
					c := p.Value[i]
					if c == '\\' && i+1 < len(p.Value) {
						next := p.Value[i+1]
						if !quoted || strings.ContainsRune("$`\"\\\n", rune(next)) {
							i++
							if next == '\n' {
								continue
							}
							c = next
						}
					} else if !quoted {
						if strings.ContainsRune("*?", rune(c)) || (c == '~' && b.Len() == 0) {
							known = false
						}
						if c == '[' && bracket < 0 {
							bracket = b.Len()
						}
					}
					if !appendText(p.Value[i : i+1]) {
						return analysisLimit
					}
				}
			case *syntax.SglQuoted:
				if p.Dollar {
					known = false
				}
				if !appendText(p.Value) {
					return analysisLimit
				}
			case *syntax.DblQuoted:
				if p.Dollar {
					known = false
				}
				if o := parts(p.Parts, true); o != abstain {
					return o
				}
			default:
				known = false
			}
		}
		return abstain
	}
	if o := parts(w.Parts, false); o != abstain {
		return word{}, o
	}
	if bracket >= 0 && strings.Contains(b.String()[bracket+1:], "]") {
		known = false
	}
	// SplitBraces is syntax-only and operates on a private shallow word copy.
	// Quoted/escaped braces remain literal. Do not invoke expand or interp.
	// Bash-based sh can expand braces even in POSIX mode. Treat this
	// extension as unknown under either binding, just like builtin operands.
	copyWord := *w
	if syntax.SplitBraces(&copyWord) {
		known = false
	}
	return word{text: b.String(), known: known}, abstain
}
