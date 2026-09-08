package commandguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/runtime"
	"mvdan.cc/sh/v3/syntax"
)

func TestGuardOutcomesPrivacyAndBindings(t *testing.T) {
	p, _ := canonicalize(testOptions())
	g := newGuard(p)
	for _, tc := range []struct {
		script string
		want   outcome
	}{{"blocked SECRET_MARKER", ruleMatch}, {"echo ok", abstain}, {"' SECRET_MARKER", invalidCommand}, {"$SECRET_MARKER", unanalysable}, {strings.Repeat("x", 4097), analysisLimit}} {
		req := guardRequest("shell", "cmd", tc.script)
		before := append([]byte(nil), req.Call.Input...)
		got, err := g.GuardTool(context.Background(), req)
		if err != nil || got != denial(tc.want) {
			t.Fatalf("got %+v %v want %v", got, err, tc.want)
		}
		if string(before) != string(req.Call.Input) || strings.Contains(got.Message, "SECRET_MARKER") {
			t.Fatal("input mutated or marker leaked")
		}
	}
	for _, name := range []string{"Shell", "shell-other", "standard.shell", "custom"} {
		got, err := g.GuardTool(context.Background(), runtime.ToolGuardRequest{ToolName: name, Call: runtime.ToolCall{Input: []byte("not JSON")}})
		if err != nil || got.Decision != runtime.ToolGuardAbstain {
			t.Fatal(name, got, err)
		}
	}
	o := testOptions()
	o.Bindings = []Binding{{"custom", "literal.key", DialectBash}}
	p, _ = canonicalize(o)
	g = newGuard(p)
	for _, tc := range []struct {
		name, field, cmd string
		want             outcome
	}{{"custom", "literal.key", `echo <(blocked)`, ruleMatch}, {"custom", "wrong", `echo ok`, invalidCommand}, {"shell", "cmd", "blocked", abstain}} {
		got, err := g.GuardTool(context.Background(), guardRequest(tc.name, tc.field, tc.cmd))
		if err != nil || got != denial(tc.want) {
			t.Fatal(got, err)
		}
	}
}
func TestGuardCapacityCancellationAndPermitRecovery(t *testing.T) {
	o := testOptions()
	o.Limits.MaxInFlight = 1
	p, _ := canonicalize(o)
	g := newGuard(p)
	entered, release := make(chan struct{}), make(chan struct{})
	g.parse = func(r io.Reader, d Dialect) (*syntax.File, error) {
		close(entered)
		<-release
		return parseScript(r, d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := g.GuardTool(ctx, guardRequest("shell", "cmd", "echo ok")); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("parser not entered")
	}
	got, err := g.GuardTool(context.Background(), guardRequest("shell", "cmd", "echo ok"))
	if err != nil || got != denial(capacity) {
		t.Fatal(got, err)
	}
	got, err = g.GuardTool(context.Background(), guardRequest("unbound", "cmd", "blocked"))
	if err != nil || got != denial(abstain) {
		t.Fatal(got, err)
	}
	cancel()
	select {
	case <-done:
		t.Fatal("returned with parser still live")
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(g.permits) != 0 {
		t.Fatal("permit leaked")
	}
	g.parse = parseScript
	if got, err := g.GuardTool(context.Background(), guardRequest("shell", "cmd", "echo ok")); err != nil || got != denial(abstain) {
		t.Fatal(got, err)
	}
	for _, name := range []string{"shell", "unbound"} {
		if got, err := g.GuardTool(ctx, guardRequest(name, "cmd", "echo ok")); !errors.Is(err, context.Canceled) || got != (runtime.ToolGuardResult{}) {
			t.Fatal(got, err)
		}
	}
}
func TestGuardUnexpectedParserFailureIsFixed(t *testing.T) {
	for _, mode := range []string{"error", "panic", "nil"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := canonicalize(testOptions())
			g := newGuard(p)
			g.parse = func(io.Reader, Dialect) (*syntax.File, error) {
				switch mode {
				case "error":
					return nil, errors.New("SECRET_MARKER")
				case "panic":
					panic("SECRET_MARKER")
				}
				return nil, nil
			}
			got, err := g.GuardTool(context.Background(), guardRequest("shell", "cmd", "echo ok"))
			if got != (runtime.ToolGuardResult{}) || err != errInternal || strings.Contains(err.Error(), "SECRET_MARKER") || len(g.permits) != 0 {
				t.Fatal(got, err)
			}
			g.parse = parseScript
			if got, err := g.GuardTool(context.Background(), guardRequest("shell", "cmd", "blocked")); err != nil || got != denial(ruleMatch) {
				t.Fatal(got, err)
			}
		})
	}
}
func TestConcurrentGuardIsolation(t *testing.T) {
	o := testOptions()
	o.Bindings = append(DefaultBindings(), Binding{"bash-tool", "script", DialectBash})
	o.Limits.MaxInFlight = 64
	p, _ := canonicalize(o)
	g := newGuard(p)
	requests := []runtime.ToolGuardRequest{guardRequest("shell", "cmd", `echo "$X"`), guardRequest("bash-tool", "script", `echo <(blocked)`), guardRequest("shell", "cmd", `git push`), guardRequest("bash-tool", "script", `echo $'data'`)}
	expected := []outcome{abstain, ruleMatch, ruleMatch, abstain}
	before := append([]Binding(nil), o.Bindings...)
	var wg sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wg.Go(func() {
			for i := 0; i < 20; i++ {
				j := i % len(requests)
				got, err := g.GuardTool(context.Background(), requests[j])
				if err != nil || got != denial(expected[j]) {
					t.Errorf("got %+v %v", got, err)
					return
				}
			}
		})
	}
	wg.Wait()
	if len(g.permits) != 0 || !reflect.DeepEqual(o.Bindings, before) {
		t.Fatal("shared state changed")
	}
}

func TestGuardCancellationAtSynchronousBoundaries(t *testing.T) {
	for _, boundary := range []string{"before parse", "after parse"} {
		t.Run(boundary, func(t *testing.T) {
			p, _ := canonicalize(testOptions())
			g := newGuard(p)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			g.parse = func(r io.Reader, d Dialect) (*syntax.File, error) {
				if boundary == "before parse" {
					cancel()
				}
				file, err := parseScript(r, d)
				cancel()
				return file, err
			}
			got, err := g.GuardTool(ctx, guardRequest("shell", "cmd", "echo ok"))
			if !errors.Is(err, context.Canceled) || got != (runtime.ToolGuardResult{}) || len(g.permits) != 0 {
				t.Fatal(got, err)
			}
		})
	}
}

// Cancel the real underlying context at different cooperative checks, covering
// input token reads, AST/word visits, rule comparisons and wrapper delegation.
type cancellationChecks struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *cancellationChecks) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestGuardCancellationAcrossAnalysisWork(t *testing.T) {
	for _, checks := range []int{1, 4, 10, 20, 40, 80, 160, 320} {
		t.Run(fmt.Sprint(checks), func(t *testing.T) {
			underlying, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &cancellationChecks{Context: underlying, cancel: cancel, remaining: checks}
			o := testOptions()
			p, _ := canonicalize(o)
			g := newGuard(p)
			req := guardRequest("shell", "cmd", strings.Repeat("env command echo x; ", 100))
			got, err := g.GuardTool(ctx, req)
			if !errors.Is(err, context.Canceled) || got != (runtime.ToolGuardResult{}) || len(g.permits) != 0 {
				t.Fatal(got, err)
			}
		})
	}
}
