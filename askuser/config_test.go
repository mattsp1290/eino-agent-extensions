package askuser

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/extension"
)

func testLimits() Limits {
	return Limits{
		MaxQuestionBytes: 1024, MaxOptionLabelBytes: 128,
		MaxOptionDescriptionBytes: 512, MaxCustomAnswerBytes: 1024,
		MaxInFlight: 2, MaxWait: time.Second,
	}
}

func testResponder(context.Context, Request) (Response, error) {
	return Response{Kind: ResponseSelected, SelectedOption: 1}, nil
}

func testOptions() Options {
	return Options{Responder: ResponderFunc(testResponder), ResponderIdentity: "test-responder-v1", Limits: testLimits()}
}

type pointerResponder struct{}

func (r *pointerResponder) Respond(context.Context, Request) (Response, error) {
	return Response{}, nil
}

func TestCanonicalizeDefaultsAndPreservesExplicitRegistration(t *testing.T) {
	canonical, err := canonicalize(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if canonical.scope != extension.GlobalScope() || canonical.order != DefaultOrder {
		t.Fatalf("defaults = %#v order=%d", canonical.scope, canonical.order)
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

func TestConfigRejectsRequiredFieldsAndTypedNil(t *testing.T) {
	var typedNil *pointerResponder
	tests := map[string]func(*Options){
		"responder":           func(options *Options) { options.Responder = nil },
		"typed nil responder": func(options *Options) { options.Responder = typedNil },
		"identity empty":      func(options *Options) { options.ResponderIdentity = "" },
		"identity control":    func(options *Options) { options.ResponderIdentity = "bad\nidentity" },
		"identity nul":        func(options *Options) { options.ResponderIdentity = "bad\x00identity" },
		"identity long":       func(options *Options) { options.ResponderIdentity = strings.Repeat("a", 257) },
		"identity utf8":       func(options *Options) { options.ResponderIdentity = string([]byte{0xff}) },
		"question limit":      func(options *Options) { options.Limits.MaxQuestionBytes = 0 },
		"label limit":         func(options *Options) { options.Limits.MaxOptionLabelBytes = -1 },
		"description limit":   func(options *Options) { options.Limits.MaxOptionDescriptionBytes = 0 },
		"answer limit":        func(options *Options) { options.Limits.MaxCustomAnswerBytes = 0 },
		"capacity":            func(options *Options) { options.Limits.MaxInFlight = 0 },
		"wait":                func(options *Options) { options.Limits.MaxWait = 0 },
		"retention overflow":  func(options *Options) { options.Limits.MaxCustomAnswerBytes = math.MaxInt },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			options := testOptions()
			mutate(&options)
			if _, err := ConfigHash(options); err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
}

func TestConfigHashTracksBehaviorNotResponderValue(t *testing.T) {
	base := testOptions()
	want, err := ConfigHash(base)
	if err != nil {
		t.Fatal(err)
	}
	equivalent := base
	equivalent.Responder = ResponderFunc(func(context.Context, Request) (Response, error) {
		return Response{Kind: ResponseDismissed}, nil
	})
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
		"identity":    func(options *Options) { options.ResponderIdentity += "-changed" },
		"question":    func(options *Options) { options.Limits.MaxQuestionBytes++ },
		"label":       func(options *Options) { options.Limits.MaxOptionLabelBytes++ },
		"description": func(options *Options) { options.Limits.MaxOptionDescriptionBytes++ },
		"answer":      func(options *Options) { options.Limits.MaxCustomAnswerBytes++ },
		"capacity":    func(options *Options) { options.Limits.MaxInFlight++ },
		"wait":        func(options *Options) { options.Limits.MaxWait++ },
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
