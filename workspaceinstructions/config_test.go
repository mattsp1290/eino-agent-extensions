package workspaceinstructions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/extension"
)

func testLimits() Limits {
	return Limits{
		MaxFileNames: 4, MaxChainDepth: 8, MaxFileBytes: 1024,
		MaxSectionBytes: 4096, MaxInFlight: 2, MaxWait: time.Second,
	}
}

func testOptions() Options {
	return Options{
		Resolver:         ResolverFunc(func(context.Context, Request) (Workspace, error) { return Workspace{}, nil }),
		ResolverIdentity: "test-resolver-v1", Limits: testLimits(),
	}
}

func TestConfigCanonicalDefaultsAndCopiesFileNames(t *testing.T) {
	options := testOptions()
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.scope != extension.GlobalScope() || canonical.order != DefaultOrder {
		t.Fatalf("defaults = (%#v, %d)", canonical.scope, canonical.order)
	}
	if len(canonical.fileNames) != 1 || canonical.fileNames[0] != DefaultFileName {
		t.Fatalf("file names = %#v", canonical.fileNames)
	}

	options = testOptions()
	options.Scope = extension.SessionScope("session")
	options.Order = -7
	options.FileNames = []string{"AGENTS.local.md", "AGENTS.md"}
	canonical, err = canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	options.FileNames[0] = "changed"
	if canonical.scope != options.Scope || canonical.order != -7 || canonical.fileNames[0] != "AGENTS.local.md" {
		t.Fatalf("canonical = %#v", canonical)
	}
}

func TestConfigFileNames(t *testing.T) {
	invalid := []struct {
		name  string
		names []string
	}{
		{"empty-list", []string{}}, {"empty-name", []string{""}},
		{"slash", []string{"a/b"}}, {"backslash", []string{`a\b`}},
		{"colon", []string{"a:b"}}, {"star", []string{"a*b"}},
		{"question", []string{"a?b"}}, {"quote", []string{`a"b`}},
		{"less", []string{"a<b"}}, {"greater", []string{"a>b"}},
		{"pipe", []string{"a|b"}}, {"nul", []string{"a\x00b"}},
		{"control", []string{"a\nb"}}, {"dot", []string{"."}},
		{"dotdot", []string{".."}}, {"duplicate", []string{"A", "A"}},
		{"too-long", []string{strings.Repeat("a", maxFileNameBytes+1)}},
		{"invalid-utf8", []string{string([]byte{0xff})}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			options := testOptions()
			options.FileNames = test.names
			if _, err := canonicalize(options); err == nil || !strings.Contains(err.Error(), "code=file-names") {
				t.Fatalf("error = %v", err)
			}
		})
	}

	options := testOptions()
	options.Limits.MaxFileNames = 1
	options.FileNames = []string{"A", "B"}
	if _, err := canonicalize(options); err == nil || !strings.Contains(err.Error(), "code=file-names") {
		t.Fatalf("over configured count error = %v", err)
	}
	options = testOptions()
	options.Limits.MaxFileNames = maxFileNames
	options.FileNames = make([]string, maxFileNames+1)
	for index := range options.FileNames {
		options.FileNames[index] = string(rune('a' + index))
	}
	if _, err := canonicalize(options); err == nil || !strings.Contains(err.Error(), "code=file-names") {
		t.Fatalf("absolute over-count error = %v", err)
	}
}

func TestConfigIdentityValidation(t *testing.T) {
	invalid := []string{"", " \t ", strings.Repeat("a", maxResolverIdentityBytes+1), "a\x00b", "a\nb", string([]byte{0xff})}
	for _, identity := range invalid {
		options := testOptions()
		options.ResolverIdentity = identity
		if _, err := canonicalize(options); err == nil || !strings.Contains(err.Error(), "code=resolver-identity") {
			t.Fatalf("identity %q error = %v", identity, err)
		}
	}
}

type pointerResolver struct{}

func (*pointerResolver) ResolveWorkspace(context.Context, Request) (Workspace, error) {
	return Workspace{}, nil
}

func TestConfigNilResolverForms(t *testing.T) {
	var nilFunc ResolverFunc
	var nilPointer *pointerResolver
	for name, resolver := range map[string]Resolver{"interface": nil, "func": nilFunc, "pointer": nilPointer} {
		t.Run(name, func(t *testing.T) {
			options := testOptions()
			options.Resolver = resolver
			if _, err := canonicalize(options); err == nil || !strings.Contains(err.Error(), "code=resolver-required") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestConfigLimitsBoundaries(t *testing.T) {
	validMin := Limits{
		MaxFileNames: 1, MaxChainDepth: 1, MaxFileBytes: 1,
		MaxSectionBytes: 1 + envelopeOverhead, MaxInFlight: 1, MaxWait: time.Nanosecond,
	}
	validMax := Limits{
		MaxFileNames: maxFileNames, MaxChainDepth: maxChainDepth, MaxFileBytes: maxFileBytes,
		MaxSectionBytes: maxSectionBytes, MaxInFlight: maxInFlight, MaxWait: maxWait,
	}
	for name, limits := range map[string]Limits{"minimum": validMin, "maximum": validMax} {
		t.Run(name, func(t *testing.T) {
			options := testOptions()
			options.Limits = limits
			if _, err := canonicalize(options); err != nil {
				t.Fatal(err)
			}
		})
	}

	tests := []struct {
		name string
		code string
		set  func(*Limits)
	}{
		{"file-names-low", "max-file-names", func(l *Limits) { l.MaxFileNames = 0 }},
		{"file-names-high", "max-file-names", func(l *Limits) { l.MaxFileNames = maxFileNames + 1 }},
		{"depth-low", "max-chain-depth", func(l *Limits) { l.MaxChainDepth = 0 }},
		{"depth-high", "max-chain-depth", func(l *Limits) { l.MaxChainDepth = maxChainDepth + 1 }},
		{"file-low", "max-file-bytes", func(l *Limits) { l.MaxFileBytes = 0 }},
		{"file-high", "max-file-bytes", func(l *Limits) { l.MaxFileBytes = maxFileBytes + 1 }},
		{"section-low", "max-section-bytes", func(l *Limits) { l.MaxSectionBytes = l.MaxFileBytes + envelopeOverhead - 1 }},
		{"section-high", "max-section-bytes", func(l *Limits) { l.MaxSectionBytes = maxSectionBytes + 1 }},
		{"flight-low", "max-in-flight", func(l *Limits) { l.MaxInFlight = 0 }},
		{"flight-high", "max-in-flight", func(l *Limits) { l.MaxInFlight = maxInFlight + 1 }},
		{"wait-low", "max-wait", func(l *Limits) { l.MaxWait = 0 }},
		{"wait-high", "max-wait", func(l *Limits) { l.MaxWait = maxWait + time.Nanosecond }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := testOptions()
			test.set(&options.Limits)
			if _, err := canonicalize(options); err == nil || !strings.Contains(err.Error(), "code="+test.code) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestConfigHashSensitivityAndExclusions(t *testing.T) {
	base := testOptions()
	base.FileNames = []string{"A", "B"}
	want, err := ConfigHash(base)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := ConfigHash(base)
	if again != want || len(want) != 64 {
		t.Fatalf("hashes = %q, %q", want, again)
	}

	excluded := []Options{base, base, base}
	excluded[0].Resolver = ResolverFunc(func(context.Context, Request) (Workspace, error) { return Workspace{Trusted: true}, nil })
	excluded[1].Scope = extension.SessionScope("other")
	excluded[2].Order = 999
	for index, options := range excluded {
		got, hashErr := ConfigHash(options)
		if hashErr != nil || got != want {
			t.Fatalf("excluded[%d] = %q, %v", index, got, hashErr)
		}
	}

	mutations := []func(*Options){
		func(o *Options) { o.FileNames = []string{"B", "A"} },
		func(o *Options) { o.ResolverIdentity = "test-resolver-v2" },
		func(o *Options) { o.Limits.MaxFileNames++ },
		func(o *Options) { o.Limits.MaxChainDepth++ },
		func(o *Options) { o.Limits.MaxFileBytes++ },
		func(o *Options) { o.Limits.MaxSectionBytes++ },
		func(o *Options) { o.Limits.MaxInFlight++ },
		func(o *Options) { o.Limits.MaxWait++ },
	}
	for index, mutate := range mutations {
		changed := base
		changed.FileNames = append([]string(nil), base.FileNames...)
		mutate(&changed)
		got, hashErr := ConfigHash(changed)
		if hashErr != nil || got == want {
			t.Fatalf("mutation[%d] = %q, %v", index, got, hashErr)
		}
	}
}
