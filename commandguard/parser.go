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
	// The parser normalizes CRLF and treats bare CR as whitespace. Real shells
	// preserve CR. Mask it with an unused ordinary control byte before parsing,
	// then restore decoded literals. Length and all shell metacharacters stay put.
	var carriage byte
	if strings.ContainsRune(s, '\r') {
		for _, candidate := range []byte{1, 2, 3, 4, 5, 6, 7, 8, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31} {
			if !strings.ContainsRune(s, rune(candidate)) {
				carriage = candidate
				break
			}
		}
		if carriage == 0 {
			return unanalysable
		}
		s = strings.ReplaceAll(s, "\r", string(carriage))
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
	if a.decoded == nil {
		a.decoded = make(map[*syntax.Word]word)
	}
	var stack []syntax.Node
	result := abstain
	syntax.Walk(file, func(n syntax.Node) bool {
		if n == nil {
			parent := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if result != abstain {
				return false
			}
			switch v := parent.(type) {
			case *syntax.Word:
				w, o := a.decode(v, carriage)
				result = o
				a.decoded[v] = w
			case *syntax.CallExpr:
				if len(v.Args) > 0 {
					args := make([]word, len(v.Args))
					for i, w := range v.Args {
						args[i] = a.decoded[w]
					}
					result = a.command(args, baseDepth+len(stack)+1, wrappers)
				}
			}
			return false
		}
		if result != abstain {
			return false
		}
		if a.ctx.Err() != nil {
			result = invalidCommand
			return false
		}
		if a.nodes >= l.MaxASTNodes || len(stack) >= l.MaxASTDepth-baseDepth {
			result = analysisLimit
			return false
		}
		a.nodes++
		switch v := n.(type) {
		case *syntax.File, *syntax.Stmt, *syntax.Comment, *syntax.CallExpr, *syntax.BinaryCmd, *syntax.Block, *syntax.Subshell, *syntax.IfClause, *syntax.WhileClause, *syntax.CaseClause, *syntax.CaseItem, *syntax.WordIter, *syntax.Redirect, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted, *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ExtGlob:
		case *syntax.ForClause:
			if _, ok := v.Loop.(*syntax.WordIter); !ok {
				result = unanalysable
			}
		case *syntax.Assign:
			if v.Index != nil || v.Array != nil || v.Naked || v.Append {
				result = unanalysable
			}
		case *syntax.ParamExp:
			if !simpleParameter(v) {
				result = unanalysable
			}
		case *syntax.Word:
			if a.words >= l.MaxWords {
				result = analysisLimit
			} else {
				a.words++
			}
		default:
			result = unanalysable
		}
		if result != abstain {
			return false
		}
		stack = append(stack, n)
		return true
	})
	return result
}

func simpleParameter(p *syntax.ParamExp) bool {
	return p.Param != nil && p.Flags == nil && !p.Excl && !p.Length && !p.Width && !p.IsSet && p.Split == syntax.OptUnset && p.GlobSubst == syntax.OptUnset && p.RcExpand == syntax.OptUnset && p.NestedParam == nil && p.Index == nil && len(p.Modifiers) == 0 && p.Slice == nil && p.Repl == nil && p.Names == 0 && p.Exp == nil
}
