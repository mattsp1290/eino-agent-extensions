package commandguard

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func TestRuntimeDenialDurabilityAndNextModelTurn(t *testing.T) {
	f := newIntegrationFixture(t)
	f.tool()
	f.guard(integrationOptions())
	next := f.run(toolCall("allowed", "fixture-command", "script", "echo ok"), toolCall("denied", "fixture-command", "script", "blocked SECRET_MARKER"))
	if f.executions.Load() != 1 || f.permissionCalls.Load() != 1 {
		t.Fatal("guard denial reached permission/body")
	}
	if f.call("allowed").Status != session.ToolCallCompleted {
		t.Fatal("allowed control failed")
	}
	seenDenied := false
	for range 2 {
		select {
		case n := <-f.notices:
			if n.ToolCallID == "denied" {
				seenDenied = true
				if n.Result.Metadata["permission_status"] != "denied" {
					t.Fatal("protected denial metadata missing")
				}
			}
		case <-time.After(time.Second):
			t.Fatal("settled notice missing")
		}
	}
	if !seenDenied {
		t.Fatal("denied notice missing")
	}
	denied := f.call("denied")
	if denied.Status != session.ToolCallFailed {
		t.Fatal(denied.Status)
	}
	var output runtime.ToolOutput
	if err := json.Unmarshal(denied.Output, &output); err != nil {
		t.Fatal(err)
	}
	if structuredStatus(t, output) != "denied" || !strings.Contains(output.Content, ruleMatch.message()) || strings.Contains(string(denied.Output), "SECRET_MARKER") {
		t.Fatalf("protected output: %+v", output)
	}
	if !strings.Contains(string(denied.Input), "SECRET_MARKER") {
		t.Fatal("expected durable input missing")
	}
	parts, events := f.durable()
	for label, raw := range map[string][]byte{"parts": parts, "next model request": next} {
		if !strings.Contains(string(raw), ruleMatch.message()) {
			t.Fatal(label, "lacks denial")
		}
	}
	transitions := []session.ToolTransitionPhase{}
	for _, e := range events {
		if e.ToolCallID == denied.ID && e.ToolTransition != "" {
			transitions = append(transitions, e.ToolTransition)
			if e.ToolTransition == session.ToolTransitionTerminal && !strings.Contains(string(e.Payload), ruleMatch.message()) {
				t.Fatal("terminal event lacks protected denial")
			}
		}
	}
	if len(transitions) != 3 || transitions[0] != session.ToolTransitionPending || transitions[1] != session.ToolTransitionRunning || transitions[2] != session.ToolTransitionTerminal {
		t.Fatal("noncanonical transitions", transitions)
	}
}
func TestRuntimeAbstentionPreservesPermissions(t *testing.T) {
	for _, action := range []permissions.Action{permissions.ActionDeny, permissions.ActionAsk} {
		t.Run(string(action), func(t *testing.T) {
			f := newIntegrationFixture(t)
			f.action = action
			f.tool()
			f.guard(integrationOptions())
			next := f.run(toolCall("permission", "fixture-command", "script", "echo ok"))
			call := f.call("permission")
			if f.executions.Load() != 0 || f.permissionCalls.Load() != 1 || call.Status != session.ToolCallFailed || !strings.Contains(string(next), "permission") {
				t.Fatal("permission authority lost")
			}
			var out runtime.ToolOutput
			json.Unmarshal(call.Output, &out)
			want := "denied"
			if action == permissions.ActionAsk {
				want = "approval_required"
			}
			if structuredStatus(t, out) != want {
				t.Fatal(out)
			}
		})
	}
}
func TestRuntimeChecksFinalPreparedInputAndAllGuards(t *testing.T) {
	f := newIntegrationFixture(t)
	f.tool()
	f.guard(integrationOptions())
	var observed atomic.Int32
	f.mount("prepare-and-observe", func(_ context.Context, r *composition.Registrar) error {
		if err := extension.OnTransform(r.Extensions(), runtime.ToolPreparePoint, extension.Registration{ID: "prepare", Scope: extension.GlobalScope()}, func(_ context.Context, p runtime.PreparedToolCall) (runtime.PreparedToolCall, error) {
			p.Call.Input = []byte(`{"script":"blocked PREPARED_MARKER"}`)
			return p, nil
		}); err != nil {
			return err
		}
		return r.Guard(composition.GuardRegistration{ID: "second", Scope: extension.GlobalScope(), Order: runtime.OrderHostPolicy + 1, Guard: runtime.ToolGuardFunc(func(_ context.Context, req runtime.ToolGuardRequest) (runtime.ToolGuardResult, error) {
			observed.Add(1)
			return runtime.ToolGuardResult{Decision: runtime.ToolGuardAbstain}, nil
		})})
	})
	f.run(toolCall("prepared", "fixture-command", "script", "echo harmless"))
	c := f.call("prepared")
	if observed.Load() != 1 || f.permissionCalls.Load() != 0 || f.executions.Load() != 0 || !strings.Contains(string(c.Input), "PREPARED_MARKER") || !strings.Contains(string(c.Output), ruleMatch.message()) {
		t.Fatal("final input or all-guards contract lost", c)
	}
}
func TestRuntimeStructuralDenials(t *testing.T) {
	for _, tc := range []struct {
		script string
		want   outcome
	}{
		{`echo "`, invalidCommand}, {`$TOOL SECRET_MARKER`, unanalysable}, {`trap 'echo harmless' EXIT`, unanalysable}, {`printf -v 'a[$(echo harmless)0]' x`, unanalysable}, {`test -v 'a[$(echo harmless)0]'`, unanalysable}, {strings.Repeat("x", 4097), analysisLimit},
	} {
		t.Run(tc.script[:min(len(tc.script), 40)], func(t *testing.T) {
			f := newIntegrationFixture(t)
			f.tool()
			f.guard(integrationOptions())
			next := f.run(toolCall("opaque", "fixture-command", "script", tc.script))
			if f.permissionCalls.Load() != 0 || f.executions.Load() != 0 || !strings.Contains(string(f.call("opaque").Output), tc.want.message()) || !strings.Contains(string(next), tc.want.message()) {
				t.Fatal("structural denial not enforced")
			}
		})
	}
}

func structuredStatus(t *testing.T, out runtime.ToolOutput) string {
	t.Helper()
	var p map[string]string
	if err := json.Unmarshal(out.Structured, &p); err != nil {
		t.Fatal(err)
	}
	return p["status"]
}
