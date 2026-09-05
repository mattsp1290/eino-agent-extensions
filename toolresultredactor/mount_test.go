package toolresultredactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func testComponent(configHash string) extension.Component {
	return extension.Component{
		InstanceID: "fixture-redactor",
		Artifact:   extension.Artifact{Name: "tool-result-redactor", Version: "test", Hash: "synthetic-artifact", ConfigHash: configHash, SourceKind: extension.SourceNative},
	}
}

func TestMountDescriptorDefaultsAndLifecycle(t *testing.T) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	mount, err := Mount(context.Background(), registry, testComponent(""), options)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session-a"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	if len(descriptor.Components) != 1 || len(descriptor.Components[0].Handlers) != 1 {
		t.Fatalf("descriptor component/handler count mismatch")
	}
	handler := descriptor.Components[0].Handlers[0]
	if handler.ID != registrationID || handler.Order != LateOrder || handler.Scope != extension.GlobalScope() || handler.Contract != runtime.ToolResultTransformPoint.Contract().ID {
		t.Fatalf("unexpected handler identity")
	}
	wantHash, err := ConfigHash(options)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Components[0].Artifact.ConfigHash != wantHash {
		t.Fatalf("derived config hash missing from descriptor")
	}

	mount.Deactivate()
	newPlan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(newPlan.Descriptor().Components) != 0 {
		t.Fatalf("deactivated mount selected by new plan")
	}
	newPlan.Release()
	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := mount.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close while plan held did not wait: %v", err)
	}
	plan.Release()
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMountValidationIsAtomicAndIdentityCanBeReused(t *testing.T) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	invalid := testOptions()
	invalid.AdditionalPatterns = []Pattern{{ID: "zero-width", Expression: `^$`}}
	if _, err := Mount(context.Background(), registry, testComponent(""), invalid); err == nil {
		t.Fatal("invalid policy mounted")
	}
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Descriptor().Components) != 0 {
		t.Fatalf("failed mount published handler")
	}
	plan.Release()
	mount, err := Mount(context.Background(), registry, testComponent(""), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mount.Close(context.Background()) }()
}

func TestMountRejectsNonNativeAndMismatchedConfigHash(t *testing.T) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	wasm := testComponent("")
	wasm.Artifact.SourceKind = extension.SourceWasm
	if _, err := Mount(context.Background(), registry, wasm, testOptions()); err == nil {
		t.Fatal("wasm component accepted")
	}
	if _, err := Mount(context.Background(), registry, testComponent("wrong"), testOptions()); err == nil {
		t.Fatal("mismatched config hash accepted")
	}
	if _, err := Mount(context.Background(), nil, testComponent(""), testOptions()); err == nil {
		t.Fatal("nil registry accepted")
	}
}

func TestEquivalentPolicyReproducesFingerprintAndEffectiveChangeDoesNot(t *testing.T) {
	options := testOptions()
	options.ExcludedTools = []string{"b-tool", "a-tool"}
	firstRegistry, _ := composition.NewRegistry(nil)
	firstMount, err := Mount(context.Background(), firstRegistry, testComponent(""), options)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan, err := firstRegistry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := firstPlan.Descriptor()
	sealed, err := session.VerifyExtensionPlanForSession("session", descriptor)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan.Release()
	_ = firstMount.Close(context.Background())

	equivalent := options
	equivalent.ExcludedTools = []string{"a-tool", "b-tool", "a-tool"}
	secondRegistry, _ := composition.NewRegistry(nil)
	secondMount, err := Mount(context.Background(), secondRegistry, testComponent(""), equivalent)
	if err != nil {
		t.Fatal(err)
	}
	resume, err := secondRegistry.AcquireResumePlan(context.Background(), runtime.ResumePlanRequest{SessionID: "session", Plan: sealed})
	if err != nil {
		t.Fatal(err)
	}
	if resume.Descriptor().Fingerprint != descriptor.Fingerprint {
		t.Fatalf("equivalent policy changed fingerprint")
	}
	resume.Release()
	_ = secondMount.Close(context.Background())

	changed := options
	changed.Limits.MaxFieldBytes++
	thirdRegistry, _ := composition.NewRegistry(nil)
	thirdMount, err := Mount(context.Background(), thirdRegistry, testComponent(""), changed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = thirdMount.Close(context.Background()) }()
	resume, err = thirdRegistry.AcquireResumePlan(context.Background(), runtime.ResumePlanRequest{SessionID: "session", Plan: sealed})
	if resume != nil || !errors.Is(err, runtime.ErrExtensionPlanMismatch) {
		t.Fatalf("changed policy resume = (%v, %v), want nil ErrExtensionPlanMismatch", resume, err)
	}
}

func TestSessionScopeSelectionAndOrderIdentity(t *testing.T) {
	options := testOptions()
	options.Scope = extension.SessionScope("selected")
	options.Order = 2345
	registry, _ := composition.NewRegistry(nil)
	mount, err := Mount(context.Background(), registry, testComponent(""), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mount.Close(context.Background()) }()
	selected, _ := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "selected"})
	defer selected.Release()
	if len(selected.Descriptor().Components) != 1 || selected.Descriptor().Components[0].Handlers[0].Order != 2345 {
		t.Fatalf("session scoped handler not selected with explicit order")
	}
	other, _ := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "other"})
	defer other.Release()
	if len(other.Descriptor().Components) != 0 {
		t.Fatalf("session scoped handler selected for other session")
	}
}

func TestScopeAndOrderChangeFrozenFingerprint(t *testing.T) {
	fingerprint := func(t *testing.T, options Options) string {
		t.Helper()
		registry, err := composition.NewRegistry(nil)
		if err != nil {
			t.Fatal(err)
		}
		mount, err := Mount(context.Background(), registry, testComponent(""), options)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "selected"})
		if err != nil {
			t.Fatal(err)
		}
		result := plan.Descriptor().Fingerprint
		plan.Release()
		if err := mount.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		return result
	}
	base := testOptions()
	baseFingerprint := fingerprint(t, base)
	changedOrder := base
	changedOrder.Order = LateOrder + 1
	changedScope := base
	changedScope.Scope = extension.SessionScope("selected")
	if fingerprint(t, changedOrder) == baseFingerprint || fingerprint(t, changedScope) == baseFingerprint {
		t.Fatalf("scope or order change retained frozen fingerprint")
	}
}
