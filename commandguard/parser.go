package commandguard

import (
	"context"
	"errors"
	"io"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type analysis struct {
	ctx                 context.Context
	policy              *policy
	parse               parseFunc
	bytes, nodes, words int
	decoded             map[*syntax.Word]word
}
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > 1024 {
		p = p[:1024]
	}
	return r.reader.Read(p)
}
func parseScript(r io.Reader, d Dialect) (*syntax.File, error) {
	variant := syntax.LangPOSIX
	if d == DialectBash {
		variant = syntax.LangBash
	}
	return syntax.NewParser(syntax.Variant(variant)).Parse(r, "")
}
func (a *analysis) script(s string, d Dialect, baseDepth, wrappers int) outcome {
	l := a.policy.limits
	if a.ctx.Err() != nil {
		return invalidCommand
	}
	if len(s) > l.MaxCommandBytes || len(s) > l.MaxAnalysisBytes-a.bytes || baseDepth >= l.MaxASTDepth {
		return analysisLimit
	}
	if !validText(s) {
		return invalidCommand
	}
	s, carriage, result := maskCarriageReturns(s)
	if result != abstain {
		return result
	}
	a.bytes += len(s)
	file, err := a.parse(contextReader{a.ctx, strings.NewReader(s)}, d)
	if a.ctx.Err() != nil {
		return invalidCommand
	}
	if err != nil {
		var pe syntax.ParseError
		var le syntax.LangError
		if errors.As(err, &pe) || errors.As(err, &le) {
			return invalidCommand
		}
		panic(errInternal) // GuardTool converts unexpected parser failures to a fixed error.
	}
	if file == nil {
		panic(errInternal)
	}
	return a.walkScript(file, carriage, baseDepth, wrappers)
}

// The parser normalizes CRLF and treats bare CR as whitespace. Real shells
// preserve CR. Use an absent ordinary control byte as a length-preserving mask;
// decoding restores CR without changing any shell metacharacter.
func maskCarriageReturns(s string) (string, byte, outcome) {
	if !strings.ContainsRune(s, '\r') {
		return s, 0, abstain
	}
	for _, candidate := range []byte{1, 2, 3, 4, 5, 6, 7, 8, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31} {
		if !strings.ContainsRune(s, rune(candidate)) {
			return strings.ReplaceAll(s, "\r", string(candidate)), candidate, abstain
		}
	}
	return "", 0, unanalysable
}
