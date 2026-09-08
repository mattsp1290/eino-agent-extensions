package commandguard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func testComponent(hash string) extension.Component {
	return extension.Component{InstanceID: "command-guard", Artifact: extension.Artifact{Name: "command-guard", Version: "test", Hash: "fixture-artifact", ConfigHash: hash, SourceKind: extension.SourceNative}}
}
func testRegistry(t *testing.T) *composition.Registry {
	t.Helper()
	r, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func testMount(t *testing.T, r *composition.Registry, c extension.Component, o Options) *composition.Mount {
	t.Helper()
	m, err := Mount(context.Background(), r, c, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.Deactivate()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}
func testPlan(t *testing.T, r *composition.Registry, id session.ID) *runtime.RunPlan {
	t.Helper()
	p, err := r.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Release)
	return p
}
func TestMountFrozenIdentityAndLifecycle(t *testing.T) {
	r := testRegistry(t)
	o := testOptions()
	o.Bindings = DefaultBindings()
	want, _ := ConfigHash(o)
	component := testComponent("")
	m := testMount(t, r, component, o)
	p := testPlan(t, r, "session")
	d := p.Descriptor()
	if len(d.Components) != 1 || len(d.Components[0].Guards) != 1 {
		t.Fatal("guard descriptor missing")
	}
	g := d.Components[0].Guards[0]
	if g.RegistrationID != registrationID || g.Order != runtime.OrderHostPolicy || g.Scope != extension.GlobalScope() || d.Components[0].Artifact.ConfigHash != want || component.Artifact.ConfigHash != "" {
		t.Fatal("incorrect immutable identity")
	}
	o.Rules[1].ArgPrefix[0] = "status"
	o.Bindings[0].CommandField = "changed"
	m.Deactivate()
	next := testPlan(t, r, "session")
	if len(next.Guards()) != 0 {
		t.Fatal("deactivated guard selected")
	}
	next.Release()
	got, err := p.Guards()[0].Guard.GuardTool(context.Background(), guardRequest("shell", "cmd", "git push"))
	if err != nil || got != denial(ruleMatch) {
		t.Fatal("old plan lost frozen enforcement", got, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := m.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	p.Release()
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestMountValidationRollback(t *testing.T) {
	for _, kind := range []string{"policy", "hash", "native", "identity", "context", "registry"} {
		t.Run(kind, func(t *testing.T) {
			r := testRegistry(t)
			target := r
			o := testOptions()
			c := testComponent("")
			ctx := context.Background()
			switch kind {
			case "policy":
				o.Rules = nil
			case "hash":
				c.Artifact.ConfigHash = "wrong"
			case "native":
				c.Artifact.SourceKind = extension.SourceWasm
			case "identity":
				c.Artifact.Hash = ""
			case "context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "registry":
				target = nil
			}
			if _, err := Mount(ctx, target, c, o); err == nil {
				t.Fatal("invalid mount accepted")
			}
			p := testPlan(t, r, "session")
			if len(p.Descriptor().Components) != 0 {
				t.Fatal("failed mount published state")
			}
			p.Release()
			testMount(t, r, testComponent(""), testOptions())
		})
	}
	r := testRegistry(t)
	testMount(t, r, testComponent(""), testOptions())
	if _, err := Mount(context.Background(), r, testComponent(""), testOptions()); err == nil {
		t.Fatal("duplicate mount accepted")
	}
	if p := testPlan(t, r, "session"); len(p.Guards()) != 1 {
		t.Fatal("duplicate leaked registration")
	}
}
func TestMountGlobalSessionOrderAndConcurrentLeases(t *testing.T) {
	r := testRegistry(t)
	global := testOptions()
	global.Order = 20
	testMount(t, r, testComponent(""), global)
	local := testOptions()
	local.Scope = extension.SessionScope("exact-session")
	local.Order = 10
	c := testComponent("")
	c.InstanceID = "session-guard"
	m := testMount(t, r, c, local)
	p := testPlan(t, r, "exact-session")
	if gs := p.Guards(); len(gs) != 2 || gs[0].InstanceID != "session-guard" || gs[1].InstanceID != "command-guard" {
		t.Fatal("scope/order incorrect", gs)
	}
	p.Release()
	p = testPlan(t, r, "other-session")
	if len(p.Guards()) != 1 {
		t.Fatal("session policy leaked")
	}
	p.Release()
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			for range 20 {
				plan, err := r.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "exact-session"})
				if err != nil {
					t.Error(err)
					return
				}
				for _, g := range plan.Guards() {
					got, err := g.Guard.GuardTool(context.Background(), guardRequest("shell", "cmd", "blocked"))
					if err != nil || got.Decision != runtime.ToolGuardDeny {
						t.Error(got, err)
					}
				}
				plan.Release()
			}
		})
	}
	m.Deactivate()
	wg.Wait()
}
