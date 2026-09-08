package commandguard

import "mvdan.cc/sh/v3/syntax"

// walkScript coordinates entry checks and postorder analysis. A denied child
// prevents parent matching; the first denial survives the remaining exit visits.
func (a *analysis) walkScript(file *syntax.File, carriage byte, baseDepth, wrappers int) outcome {
	if a.decoded == nil {
		a.decoded = make(map[*syntax.Word]word)
	}
	var stack []syntax.Node
	result := abstain
	syntax.Walk(file, func(n syntax.Node) bool {
		if n == nil {
			parent := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if result == abstain {
				result = a.finishNode(parent, carriage, baseDepth+len(stack)+1, wrappers)
			}
			return false
		}
		if result != abstain {
			return false
		}
		result = a.inspectNode(n, baseDepth+len(stack))
		if result != abstain {
			return false
		}
		stack = append(stack, n)
		return true
	})
	return result
}

// inspectNode is the canonical syntax-admission boundary, charged before descent.
func (a *analysis) inspectNode(n syntax.Node, depth int) outcome {
	if a.ctx.Err() != nil {
		return invalidCommand
	}
	if a.nodes >= a.policy.limits.MaxASTNodes || depth >= a.policy.limits.MaxASTDepth {
		return analysisLimit
	}
	a.nodes++
	switch v := n.(type) {
	case *syntax.File, *syntax.Stmt, *syntax.Comment, *syntax.CallExpr, *syntax.BinaryCmd, *syntax.Block, *syntax.Subshell, *syntax.IfClause, *syntax.WhileClause, *syntax.CaseClause, *syntax.CaseItem, *syntax.Redirect, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted, *syntax.CmdSubst, *syntax.ProcSubst:
	case *syntax.ExtGlob:
		// The pinned parser stores the pattern as a Lit; walking it does
		// not inspect command substitutions inside that pattern.
		return unanalysable
	case *syntax.WordIter:
		if v.Name == nil || opaqueVariableTarget(v.Name.Value) {
			return unanalysable
		}
	case *syntax.ForClause:
		if _, ok := v.Loop.(*syntax.WordIter); !ok {
			return unanalysable
		}
	case *syntax.Assign:
		if v.Name == nil || opaqueVariableTarget(v.Name.Value) || v.Index != nil || v.Array != nil || v.Naked || v.Append {
			return unanalysable
		}
	case *syntax.ParamExp:
		if !simpleParameter(v) {
			return unanalysable
		}
	case *syntax.Word:
		if a.words >= a.policy.limits.MaxWords {
			return analysisLimit
		}
		a.words++
	default:
		return unanalysable
	}
	return abstain
}

// finishNode runs only after all executable descendants have been inspected.
func (a *analysis) finishNode(n syntax.Node, carriage byte, depth, wrappers int) outcome {
	switch v := n.(type) {
	case *syntax.Word:
		w, result := a.decode(v, carriage)
		a.decoded[v] = w
		return result
	case *syntax.CallExpr:
		if len(v.Args) > 0 {
			args := make([]word, len(v.Args))
			for i, w := range v.Args {
				args[i] = a.decoded[w]
			}
			return a.command(args, depth, wrappers)
		}
	}
	return abstain
}

func simpleParameter(p *syntax.ParamExp) bool {
	return p.Param != nil && p.Flags == nil && !p.Excl && !p.Length && !p.Width && !p.IsSet && p.Split == syntax.OptUnset && p.GlobSubst == syntax.OptUnset && p.RcExpand == syntax.OptUnset && p.NestedParam == nil && p.Index == nil && len(p.Modifiers) == 0 && p.Slice == nil && p.Repl == nil && p.Names == 0 && p.Exp == nil
}
