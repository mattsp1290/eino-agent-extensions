package commandguard

import (
	"context"
	"io"

	"github.com/mattsp1290/eino-agent/runtime"
	"mvdan.cc/sh/v3/syntax"
)

type parseFunc func(io.Reader, Dialect) (*syntax.File, error)
type guard struct {
	policy  policy
	permits chan struct{}
	parse   parseFunc
}

func newGuard(p policy) *guard {
	return &guard{policy: p, permits: make(chan struct{}, p.limits.MaxInFlight), parse: parseScript}
}

func (g *guard) GuardTool(ctx context.Context, req runtime.ToolGuardRequest) (result runtime.ToolGuardResult, err error) {
	defer func() {
		if recover() != nil {
			result = runtime.ToolGuardResult{}
			err = errInternal
		}
		if canceled := ctx.Err(); canceled != nil {
			result = runtime.ToolGuardResult{}
			err = canceled
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	var binding Binding
	for _, b := range g.policy.bindings {
		if b.ToolName == req.ToolName {
			binding = b
			break
		}
	}
	if binding.ToolName == "" {
		return runtime.ToolGuardResult{Decision: runtime.ToolGuardAbstain}, nil
	}
	select {
	case g.permits <- struct{}{}:
		defer func() { <-g.permits }()
	default:
		return denial(capacity), nil
	}
	a := analysis{ctx: ctx, policy: &g.policy, parse: g.parse}
	command, o := a.input(req.Call.Input, binding.CommandField)
	if o == abstain {
		o = a.script(command, binding.Dialect, 0, 0)
	}
	if err = ctx.Err(); err != nil {
		return runtime.ToolGuardResult{}, err
	}
	return denial(o), nil
}
func denial(o outcome) runtime.ToolGuardResult {
	if o == abstain {
		return runtime.ToolGuardResult{Decision: runtime.ToolGuardAbstain}
	}
	return runtime.ToolGuardResult{Decision: runtime.ToolGuardDeny, Code: "command_policy_denied", Message: o.message()}
}
