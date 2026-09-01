//go:build linux || darwin

package backgroundjobs

import (
	"context"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func TestMountExposesFourFrozenTools(t *testing.T) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	component := extension.Component{InstanceID: "background-jobs", Artifact: extension.Artifact{Name: "background-jobs", Version: "1.0.0", Hash: "artifact-v1", SourceKind: extension.SourceNative}}
	mount, err := Mount(context.Background(), registry, component, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "session", WorkspaceID: "workspace", WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 4 {
		t.Fatalf("tool count = %d", len(tools))
	}
	want := map[string]bool{StartToolName: true, StatusToolName: true, ListToolName: true, KillToolName: true}
	for index, tool := range tools {
		if !want[tool.Name] {
			t.Fatalf("unexpected tool[%d] = %q", index, tool.Name)
		}
		if tool.Retention.MaxInlineBytes <= 0 || tool.Retention.StoreExternal || tool.Info == nil || tool.Info.ParamsOneOf == nil {
			t.Fatalf("tool[%d] not fully bounded: %#v", index, tool)
		}
	}
	startTool := findIntegrationTool(t, tools, StartToolName)
	startInput, err := startTool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"command":"sleep 30"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := startTool.Executor.Execute(context.Background(), integrationCall("leased-start", StartToolName, startInput, t.TempDir())); err != nil {
		t.Fatal(err)
	}
	mount.Deactivate()
	closeDone := make(chan error, 1)
	go func() { closeDone <- mount.Close(context.Background()) }()
	select {
	case err := <-closeDone:
		t.Fatal("close completed while plan lease remained", err)
	case <-time.After(50 * time.Millisecond):
	}
	plan.Release()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not complete after plan release")
	}
}

func TestMountStrictResumeRejectsBehaviorDrift(t *testing.T) {
	base := testOptions()
	registry, mount := mountIntegrationRegistry(t, base)
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "resume-session"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	plan.Release()
	mount.Deactivate()
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	sealed, err := session.VerifyExtensionPlanForSession("resume-session", descriptor)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Options){
		"limit":                func(options *Options) { options.Limits.MaxCommandBytes++ },
		"shell identity":       func(options *Options) { options.ShellIdentity = "system-sh-v2" },
		"environment identity": func(options *Options) { options.Environment.Identity = "test-environment-v2" },
		"environment mode":     func(options *Options) { options.Environment.Mode = EnvironmentInheritAndOverride },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			options := testOptions()
			mutate(&options)
			changedRegistry, changedMount := mountIntegrationRegistry(t, options)
			defer func() {
				changedMount.Deactivate()
				_ = changedMount.Close(context.Background())
			}()
			resumed, err := changedRegistry.AcquireResumePlan(context.Background(), runtime.ResumePlanRequest{SessionID: "resume-session", Plan: sealed})
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Release()
			if resumed.Descriptor().Fingerprint == descriptor.Fingerprint {
				t.Fatalf("changed behavior retained persisted fingerprint %s", descriptor.Fingerprint)
			}
		})
	}
}

func TestMountRejectsInvalidInputs(t *testing.T) {
	component := extension.Component{InstanceID: "background-jobs", Artifact: extension.Artifact{Name: "background-jobs", Version: "1", Hash: "hash", SourceKind: extension.SourceNative}}
	if _, err := Mount(context.Background(), nil, component, testOptions()); err == nil {
		t.Fatal("nil registry accepted")
	}
	registry, _ := composition.NewRegistry(nil)
	component.Artifact.SourceKind = extension.SourceWasm
	if _, err := Mount(context.Background(), registry, component, testOptions()); err == nil {
		t.Fatal("non-native component accepted")
	}
	component.Artifact.SourceKind = extension.SourceNative
	component.Artifact.ConfigHash = "mismatch"
	if _, err := Mount(context.Background(), registry, component, testOptions()); err == nil {
		t.Fatal("config hash mismatch accepted")
	}
}

func TestMountDuplicateRegistrationRollsBackCandidate(t *testing.T) {
	registry, first := mountIntegrationRegistry(t, testOptions())
	defer func() {
		first.Deactivate()
		_ = first.Close(context.Background())
	}()
	component := extension.Component{InstanceID: "background-jobs-duplicate", Artifact: extension.Artifact{Name: "background-jobs", Version: "test", Hash: "duplicate-artifact", SourceKind: extension.SourceNative}}
	if duplicate, err := Mount(context.Background(), registry, component, testOptions()); duplicate != nil || err == nil {
		t.Fatalf("duplicate mount = present:%t error:%v", duplicate != nil, err)
	}
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Release()
	resolved, err := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "session", WorkspaceID: "workspace", WorkspaceRoot: t.TempDir()})
	if err != nil || len(resolved) != 4 {
		t.Fatalf("registry after rollback = tools:%d error:%v", len(resolved), err)
	}
}
