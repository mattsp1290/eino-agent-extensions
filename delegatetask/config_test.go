package delegatetask

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/extension"
)

func testLimits() Limits {
	return Limits{MaxTaskBytes: 1024, MaxProfileBytes: 64, MaxResultBytes: 2048, MaxInFlight: 2, MaxWait: time.Second}
}

func testRunner(context.Context, Request) (Response, error) {
	return Response{Status: ResponseCompleted, Output: "done"}, nil
}

func testOptions() Options {
	return Options{Runner: RunnerFunc(testRunner), RunnerIdentity: "test-runner-v1", Limits: testLimits()}
}

type pointerRunner struct{}

func (*pointerRunner) Run(context.Context, Request) (Response, error) { return Response{}, nil }

func TestConfigDefaultsAndExplicitRegistration(t *testing.T) {
	canonical, err := canonicalize(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if canonical.scope != extension.GlobalScope() || canonical.order != DefaultOrder {
		t.Fatalf("defaults=%#v order=%d", canonical.scope, canonical.order)
	}
	explicit := testOptions()
	explicit.Scope = extension.SessionScope("session")
	explicit.Order = 42
	canonical, err = canonicalize(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.scope != explicit.Scope || canonical.order != explicit.Order {
		t.Fatalf("explicit registration changed: %#v %d", canonical.scope, canonical.order)
	}
}

func TestConfigRejectsInvalidFieldsAndOverflow(t *testing.T) {
	var typedNil *pointerRunner
	tests := map[string]func(*Options){
		"runner":           func(o *Options) { o.Runner = nil },
		"typed nil runner": func(o *Options) { o.Runner = typedNil },
		"identity empty":   func(o *Options) { o.RunnerIdentity = "" },
		"identity control": func(o *Options) { o.RunnerIdentity = "bad\nidentity" },
		"identity nul":     func(o *Options) { o.RunnerIdentity = "bad\x00identity" },
		"identity long":    func(o *Options) { o.RunnerIdentity = strings.Repeat("a", 257) },
		"identity utf8":    func(o *Options) { o.RunnerIdentity = string([]byte{0xff}) },
		"task":             func(o *Options) { o.Limits.MaxTaskBytes = 0 },
		"profile":          func(o *Options) { o.Limits.MaxProfileBytes = -1 },
		"profile max":      func(o *Options) { o.Limits.MaxProfileBytes = 257 },
		"result":           func(o *Options) { o.Limits.MaxResultBytes = 0 },
		"capacity":         func(o *Options) { o.Limits.MaxInFlight = 0 },
		"wait":             func(o *Options) { o.Limits.MaxWait = 0 },
		"retention":        func(o *Options) { o.Limits.MaxResultBytes = math.MaxInt },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			o := testOptions()
			mutate(&o)
			if _, err := ConfigHash(o); err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
}

func TestConfigHashTracksBehaviorNotRunnerOrRegistration(t *testing.T) {
	base := testOptions()
	want, err := ConfigHash(base)
	if err != nil {
		t.Fatal(err)
	}
	equivalent := base
	equivalent.Runner = RunnerFunc(func(context.Context, Request) (Response, error) { return Response{Status: ResponseRejected}, nil })
	equivalent.Scope = extension.SessionScope("other")
	equivalent.Order = 999
	got, err := ConfigHash(equivalent)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("excluded fields changed hash: %s != %s", got, want)
	}
	mutations := map[string]func(*Options){
		"identity": func(o *Options) { o.RunnerIdentity += "-changed" },
		"task":     func(o *Options) { o.Limits.MaxTaskBytes++ },
		"profile":  func(o *Options) { o.Limits.MaxProfileBytes++ },
		"result":   func(o *Options) { o.Limits.MaxResultBytes++ },
		"capacity": func(o *Options) { o.Limits.MaxInFlight++ },
		"wait":     func(o *Options) { o.Limits.MaxWait++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			hash, err := ConfigHash(changed)
			if err != nil {
				t.Fatal(err)
			}
			if hash == want {
				t.Fatal("behavior change retained hash")
			}
		})
	}
}

func TestResultRetentionMaximumSafeBoundary(t *testing.T) {
	maximumSafe := int((int64(math.MaxInt64)/2 - 128) / 6)
	limits := testLimits()
	limits.MaxResultBytes = maximumSafe
	if _, err := resultRetention(limits); err != nil {
		t.Fatalf("maximum safe retention rejected: %v", err)
	}
	limits.MaxResultBytes++
	if _, err := resultRetention(limits); err == nil {
		t.Fatal("first overflowing retention accepted")
	}
}
