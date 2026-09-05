package websearch

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

func TestMountValidatesInputsAndDoesNotMutateComponent(t *testing.T) {
	component := testComponent("validation")
	if _, err := Mount(context.Background(), nil, component, testOptions()); err == nil {
		t.Fatal("nil registry accepted")
	}
	registry, _ := composition.NewRegistry(nil)
	nonNative := component
	nonNative.Artifact.SourceKind = extension.SourceWasm
	if _, err := Mount(context.Background(), registry, nonNative, testOptions()); err == nil {
		t.Fatal("non-native component accepted")
	}
	invalid := component
	invalid.InstanceID = ""
	if _, err := Mount(context.Background(), registry, invalid, testOptions()); err == nil {
		t.Fatal("invalid component accepted")
	}
	mismatch := component
	mismatch.Artifact.ConfigHash = "wrong"
	if _, err := Mount(context.Background(), registry, mismatch, testOptions()); err == nil {
		t.Fatal("mismatched config hash accepted")
	}
	mount, err := Mount(context.Background(), registry, component, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestMount(t, mount)
	if component.Artifact.ConfigHash != "" {
		t.Fatal("caller component mutated")
	}
	plan, tool := resolveTestTool(t, registry, "session")
	defer plan.Release()
	if tool.Name != ToolName || tool.RetrySafe || len(tool.Scope.Permissions) != 1 || tool.Scope.Permissions[0] != PermissionSearch {
		t.Fatalf("tool=%#v", tool)
	}
	descriptor := plan.Descriptor()
	if len(descriptor.Components) != 1 || descriptor.Components[0].Artifact.ConfigHash == "" {
		t.Fatalf("descriptor=%#v", descriptor)
	}
}

func TestMountScopeOrderAndDuplicateRollback(t *testing.T) {
	options := testOptions()
	options.Scope = extension.SessionScope("selected")
	options.Order = 42
	registry, mount := mountTestRegistry(t, options)
	defer closeTestMount(t, mount)
	selected, tool := resolveTestTool(t, registry, "selected")
	if tool.Name != ToolName {
		t.Fatalf("tool=%#v", tool)
	}
	descriptor := selected.Descriptor()
	selected.Release()
	if len(descriptor.Components) != 1 || len(descriptor.Components[0].Tools) != 1 || descriptor.Components[0].Tools[0].Order != 42 || descriptor.Components[0].Tools[0].RegistrationID != registrationID {
		t.Fatalf("descriptor=%#v", descriptor)
	}
	other, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	otherTools, err := other.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "other"})
	other.Release()
	if err != nil || len(otherTools) != 0 {
		t.Fatalf("other tools=%#v err=%v", otherTools, err)
	}
	duplicateOptions := testOptions()
	duplicateOptions.Scope = extension.SessionScope("selected")
	duplicate, err := Mount(context.Background(), registry, testComponent("duplicate"), duplicateOptions)
	if duplicate != nil || err == nil {
		t.Fatalf("duplicate=%v err=%v", duplicate, err)
	}
	plan, _ := resolveTestTool(t, registry, "selected")
	plan.Release()
}

func TestMountPlanDrainTimeoutPreservesRetainedAuthority(t *testing.T) {
	registry, mount := mountTestRegistry(t, testOptions())
	plan, tool := resolveTestTool(t, registry, "session")
	mount.Deactivate()
	later, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	laterTools, err := later.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "session"})
	later.Release()
	if err != nil || len(laterTools) != 0 {
		t.Fatalf("later tools=%#v err=%v", laterTools, err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = mount.Close(closeCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("plan-drain close=%v", err)
	}
	input, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"query":"retained"}`))
	if err != nil {
		t.Fatal(err)
	}
	call := testCall()
	call.Input = input
	if _, err := tool.Executor.Execute(context.Background(), call); err != nil {
		t.Fatalf("retained execution=%v", err)
	}
	plan.Release()
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMountCoordinatorCleanupTimeoutQuarantinesAdmission(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	options := testOptions()
	options.Limits.MaxWait = 10 * time.Millisecond
	options.Searcher = SearcherFunc(func(context.Context, string) ([]Source, error) {
		close(entered)
		<-release
		return nil, nil
	})
	registry, mount := mountTestRegistry(t, options)
	plan, tool := resolveTestTool(t, registry, "session")
	input, _ := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"query":"q"}`))
	call := testCall()
	call.Input = input
	executionDone := make(chan error, 1)
	go func() {
		_, err := tool.Executor.Execute(context.Background(), call)
		executionDone <- err
	}()
	<-entered
	if err := <-executionDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("execution=%v", err)
	}
	plan.Release()
	mount.Deactivate()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err := mount.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup close=%v", err)
	}
	second := testCall()
	second.ID = "second"
	second.Input = input
	if _, err := tool.Executor.Execute(context.Background(), second); !errors.Is(err, errSearchCapacity) {
		t.Fatalf("quarantined execution=%v", err)
	}
	close(release)
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMountedSearcherPreservesSelfCloseProtection(t *testing.T) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	var mount *composition.Mount
	closeErrors := make(chan error, 1)
	options := testOptions()
	options.Searcher = SearcherFunc(func(ctx context.Context, _ string) ([]Source, error) {
		closeErrors <- mount.Close(ctx)
		return nil, nil
	})
	mount, err = Mount(context.Background(), registry, testComponent("self-close"), options)
	if err != nil {
		t.Fatal(err)
	}
	plan, tool := resolveTestTool(t, registry, "session")
	input, _ := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"query":"q"}`))
	call := testCall()
	call.Input = input
	if _, err := tool.Executor.Execute(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if err := <-closeErrors; !errors.Is(err, extension.ErrSelfClose) {
		t.Fatalf("self-close=%v", err)
	}
	plan.Release()
	closeTestMount(t, mount)
}

func TestMountStrictResumeIdentity(t *testing.T) {
	base := testOptions()
	registry, mount := mountTestRegistry(t, base)
	plan, _ := resolveTestTool(t, registry, "resume-session")
	descriptor := plan.Descriptor()
	plan.Release()
	closeTestMount(t, mount)
	sealed, err := session.VerifyExtensionPlanForSession("resume-session", descriptor)
	if err != nil {
		t.Fatal(err)
	}
	equivalentRegistry, equivalentMount := mountTestRegistry(t, base)
	resumed, err := equivalentRegistry.AcquireResumePlan(context.Background(), runtime.ResumePlanRequest{SessionID: "resume-session", Plan: sealed})
	if err != nil || resumed.Descriptor().Fingerprint != descriptor.Fingerprint {
		t.Fatalf("equivalent resume=%v err=%v", resumed, err)
	}
	resumed.Release()
	closeTestMount(t, equivalentMount)
	mutations := map[string]func(*Options){
		"identity": func(o *Options) { o.SearcherIdentity += "-changed" },
		"query":    func(o *Options) { o.Limits.MaxQueryBytes++ },
		"results":  func(o *Options) { o.Limits.MaxResults++ },
		"title":    func(o *Options) { o.Limits.MaxTitleBytes++ },
		"url":      func(o *Options) { o.Limits.MaxURLBytes++ },
		"snippet":  func(o *Options) { o.Limits.MaxSnippetBytes++ },
		"capacity": func(o *Options) { o.Limits.MaxInFlight++ },
		"wait":     func(o *Options) { o.Limits.MaxWait++ },
		"scope":    func(o *Options) { o.Scope = extension.SessionScope("resume-session") },
		"order":    func(o *Options) { o.Order = 41 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			changedRegistry, changedMount := mountTestRegistry(t, changed)
			defer closeTestMount(t, changedMount)
			resumed, err := changedRegistry.AcquireResumePlan(context.Background(), runtime.ResumePlanRequest{SessionID: "resume-session", Plan: sealed})
			if resumed != nil || !errors.Is(err, runtime.ErrExtensionPlanMismatch) {
				t.Fatalf("drift resume=(%v,%v)", resumed, err)
			}
		})
	}
}
