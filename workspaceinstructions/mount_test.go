package workspaceinstructions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func testComponent(instance string) extension.Component {
	return extension.Component{InstanceID: instance, Artifact: extension.Artifact{
		Name: "workspace-instructions", Version: "test", Hash: "synthetic-artifact-v1", SourceKind: extension.SourceNative,
	}}
}

func mountTestRegistry(t *testing.T, component extension.Component, options Options) (*composition.Registry, *composition.Mount) {
	t.Helper()
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	mount, err := Mount(context.Background(), registry, component, options)
	if err != nil {
		t.Fatal(err)
	}
	return registry, mount
}

func closeTestMount(t *testing.T, mount *composition.Mount) {
	t.Helper()
	if mount == nil {
		return
	}
	mount.Deactivate()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := mount.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMountValidatesComponentWithoutMutation(t *testing.T) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Mount(context.Background(), nil, testComponent("nil-registry"), testOptions()); err == nil || !strings.Contains(err.Error(), "code=registry-required") {
		t.Fatalf("nil registry error = %v", err)
	}
	nonNative := testComponent("non-native")
	nonNative.Artifact.SourceKind = extension.SourceWasm
	if _, err := Mount(context.Background(), registry, nonNative, testOptions()); err == nil || !strings.Contains(err.Error(), "code=native-component-required") {
		t.Fatalf("non-native error = %v", err)
	}
	mismatch := testComponent("mismatch")
	mismatch.Artifact.ConfigHash = "wrong"
	if _, err := Mount(context.Background(), registry, mismatch, testOptions()); err == nil || !strings.Contains(err.Error(), "code=config-hash-mismatch") {
		t.Fatalf("mismatch error = %v", err)
	}
	invalid := testComponent("")
	if _, err := Mount(context.Background(), registry, invalid, testOptions()); err == nil || !strings.Contains(err.Error(), "code=component-identity") {
		t.Fatalf("identity error = %v", err)
	}

	component := testComponent("valid")
	if component.Artifact.ConfigHash != "" {
		t.Fatal("fixture unexpectedly has config hash")
	}
	mount, err := Mount(context.Background(), registry, component, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestMount(t, mount)
	if component.Artifact.ConfigHash != "" {
		t.Fatalf("caller component mutated: %#v", component)
	}
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Release()
	descriptor := plan.Descriptor()
	if len(descriptor.Components) != 1 || len(descriptor.Components[0].Prompts) != 1 {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	identity := descriptor.Components[0].Prompts[0]
	if identity.Name != PromptName || identity.RegistrationID != registrationID || identity.Order != DefaultOrder || identity.Scope != extension.GlobalScope() {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestMountDuplicateRollbackAndDifferentScope(t *testing.T) {
	registry, first := mountTestRegistry(t, testComponent("first"), testOptions())
	defer closeTestMount(t, first)
	if duplicate, err := Mount(context.Background(), registry, testComponent("duplicate"), testOptions()); duplicate != nil || !errors.Is(err, extension.ErrDuplicateRegistration) {
		t.Fatalf("duplicate = %v, %v", duplicate, err)
	}
	sessionOptions := testOptions()
	sessionOptions.Scope = extension.SessionScope("session")
	sessionMount, err := Mount(context.Background(), registry, testComponent("session"), sessionOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestMount(t, sessionMount)
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Release()
	prompts := plan.Prompts()
	if len(prompts) != 1 || prompts[0].InstanceID != "session" || prompts[0].Scope != sessionOptions.Scope {
		t.Fatalf("session prompts = %#v", prompts)
	}
	other, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	otherPrompts := other.Prompts()
	if len(otherPrompts) != 1 || otherPrompts[0].InstanceID != "first" || otherPrompts[0].Scope != extension.GlobalScope() {
		t.Fatalf("other prompts = %#v", otherPrompts)
	}
}

func TestMountDeactivationWaitsForPlanAndPreservesProvider(t *testing.T) {
	registry, mount := mountTestRegistry(t, testComponent("lifecycle"), testOptions())
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	prompts := plan.Prompts()
	mount.Deactivate()
	section, err := prompts[0].Provider.ProvidePrompt(context.Background(), runtime.PromptContext{SessionID: "session"})
	if err != nil || section != "" {
		t.Fatalf("leased provider = %q, %v", section, err)
	}
	newPlan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if got := newPlan.Prompts(); len(got) != 0 {
		t.Fatalf("deactivated prompts = %#v", got)
	}
	newPlan.Release()
	done := make(chan error, 1)
	go func() { done <- mount.Close(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("close while leased = %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	plan.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := mount.Close(context.Background()); err != nil {
		t.Fatalf("repeated close = %v", err)
	}
}

func TestMountStrictResumeFingerprintTracksAllBehavior(t *testing.T) {
	base := testOptions()
	base.FileNames = []string{"A", "B"}
	registry, mount := mountTestRegistry(t, testComponent("base"), base)
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "resume-session"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	plan.Release()
	closeTestMount(t, mount)
	sealed, err := session.VerifyExtensionPlanForSession("resume-session", descriptor)
	if err != nil {
		t.Fatal(err)
	}

	equivalent := base
	equivalent.Resolver = ResolverFunc(func(context.Context, Request) (Workspace, error) { return Workspace{Trusted: false}, nil })
	equivalentRegistry, equivalentMount := mountTestRegistry(t, testComponent("base"), equivalent)
	resumed, err := equivalentRegistry.AcquireResumePlan(context.Background(), runtime.ResumePlanRequest{SessionID: "resume-session", Plan: sealed})
	if err != nil {
		t.Fatal(err)
	}
	resumed.Release()
	closeTestMount(t, equivalentMount)

	mutations := []struct {
		name   string
		mutate func(*Options)
	}{
		{"file-name", func(o *Options) { o.FileNames = []string{"A", "C"} }},
		{"file-order", func(o *Options) { o.FileNames = []string{"B", "A"} }},
		{"identity", func(o *Options) { o.ResolverIdentity += "-changed" }},
		{"file-count", func(o *Options) { o.Limits.MaxFileNames++ }},
		{"depth", func(o *Options) { o.Limits.MaxChainDepth++ }},
		{"file-bytes", func(o *Options) { o.Limits.MaxFileBytes++ }},
		{"section-bytes", func(o *Options) { o.Limits.MaxSectionBytes++ }},
		{"capacity", func(o *Options) { o.Limits.MaxInFlight++ }},
		{"wait", func(o *Options) { o.Limits.MaxWait++ }},
		{"order", func(o *Options) { o.Order = 999 }},
		{"scope", func(o *Options) { o.Scope = extension.SessionScope("resume-session") }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := base
			changed.FileNames = append([]string(nil), base.FileNames...)
			mutation.mutate(&changed)
			changedRegistry, changedMount := mountTestRegistry(t, testComponent("base"), changed)
			defer closeTestMount(t, changedMount)
			resumed, err := changedRegistry.AcquireResumePlan(context.Background(), runtime.ResumePlanRequest{SessionID: "resume-session", Plan: sealed})
			if resumed != nil || !errors.Is(err, runtime.ErrExtensionPlanMismatch) {
				t.Fatalf("resume = %v, %v", resumed, err)
			}
		})
	}
}
