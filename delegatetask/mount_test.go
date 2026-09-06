package delegatetask

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func testComponent(instance string) extension.Component {
	return extension.Component{InstanceID: instance, Artifact: extension.Artifact{Name: "delegate-task", Version: "test", Hash: "synthetic-artifact-v1", SourceKind: extension.SourceNative}}
}

func mountTestRegistry(t *testing.T, options Options) (*composition.Registry, *composition.Mount) {
	t.Helper()
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	mount, err := Mount(context.Background(), registry, testComponent("delegate-task"), options)
	if err != nil {
		t.Fatal(err)
	}
	return registry, mount
}

func resolveTestTool(t *testing.T, registry *composition.Registry, sessionID session.ID, workspaceID, workspaceRoot string) (*runtime.RunPlan, runtime.Tool) {
	t.Helper()
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: sessionID, WorkspaceID: workspaceID, WorkspaceRoot: workspaceRoot})
	if err != nil {
		plan.Release()
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Name != ToolName {
		plan.Release()
		t.Fatalf("resolved=%#v", resolved)
	}
	return plan, resolved[0]
}

func TestMountResolvesAndRoutesDistinctWorkspaces(t *testing.T) {
	requests := make(chan Request, 2)
	options := testOptions()
	options.Runner = RunnerFunc(func(_ context.Context, request Request) (Response, error) {
		requests <- request
		return Response{Status: ResponseCompleted, Output: request.WorkspaceID}, nil
	})
	registry, mount := mountTestRegistry(t, options)
	defer func() { mount.Deactivate(); _ = mount.Close(context.Background()) }()
	for i, workspace := range []struct{ id, root string }{{"one", "/one"}, {"two", "/two"}} {
		plan, tool := resolveTestTool(t, registry, "session", workspace.id, workspace.root)
		descriptor := plan.Descriptor()
		identity := descriptor.Components[0].Tools[0]
		if identity.Name != ToolName || identity.RegistrationID != registrationID || identity.Scope != extension.GlobalScope() || identity.Order != DefaultOrder {
			t.Fatalf("identity=%#v", identity)
		}
		input, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"task":"inspect","profile":"read-only"}`))
		if err != nil {
			t.Fatal(err)
		}
		pattern, err := tool.Pattern.ResolvePermissionPattern(context.Background(), input)
		if err != nil || pattern != "delegate-profile:read-only" || len(tool.Scope.Permissions) != 1 || tool.Scope.Permissions[0] != PermissionDelegate {
			t.Fatalf("pattern=%q scope=%#v err=%v", pattern, tool.Scope, err)
		}
		output, err := tool.Executor.Execute(context.Background(), runtime.ToolCall{ID: session.ToolCallID(string(rune('a' + i))), SessionID: "session", RunID: "run", Name: ToolName, Input: input})
		if err != nil {
			t.Fatal(err)
		}
		var result Result
		if err := json.Unmarshal(output.Structured, &result); err != nil || result.Output != workspace.id || output.Output != string(output.Structured) {
			t.Fatalf("output=%#v result=%#v err=%v", output, result, err)
		}
		request := <-requests
		if request.WorkspaceID != workspace.id || request.WorkspaceRoot != workspace.root {
			t.Fatalf("request=%#v", request)
		}
		plan.Release()
	}
}

func TestMountExactSessionOrderAndValidation(t *testing.T) {
	options := testOptions()
	options.Scope = extension.SessionScope("only-session")
	options.Order = 42
	registry, mount := mountTestRegistry(t, options)
	plan, _ := resolveTestTool(t, registry, "only-session", "", "")
	identity := plan.Descriptor().Components[0].Tools[0]
	if identity.Scope != options.Scope || identity.Order != options.Order {
		t.Fatalf("identity=%#v", identity)
	}
	plan.Release()
	other, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := other.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "other"})
	other.Release()
	if err != nil || len(resolved) != 0 {
		t.Fatalf("other=%#v err=%v", resolved, err)
	}
	mount.Deactivate()
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	registry, err = composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Mount(context.Background(), nil, testComponent("nil"), testOptions()); err == nil {
		t.Fatal("nil registry accepted")
	}
	component := testComponent("wasm")
	component.Artifact.SourceKind = extension.SourceWasm
	if _, err := Mount(context.Background(), registry, component, testOptions()); err == nil {
		t.Fatal("non-native accepted")
	}
	component = testComponent("mismatch")
	component.Artifact.ConfigHash = "mismatch"
	if _, err := Mount(context.Background(), registry, component, testOptions()); err == nil {
		t.Fatal("config mismatch accepted")
	}
}

func TestMountDuplicateRollsBackCandidate(t *testing.T) {
	registry, first := mountTestRegistry(t, testOptions())
	defer func() { first.Deactivate(); _ = first.Close(context.Background()) }()
	if duplicate, err := Mount(context.Background(), registry, testComponent("duplicate"), testOptions()); duplicate != nil || err == nil {
		t.Fatalf("duplicate=%v err=%v", duplicate, err)
	}
	plan, _ := resolveTestTool(t, registry, "session", "", "")
	plan.Release()
}

func TestMountPlanLeaseDelaysCleanup(t *testing.T) {
	registry, mount := mountTestRegistry(t, testOptions())
	plan, _ := resolveTestTool(t, registry, "session", "", "")
	mount.Deactivate()
	newPlan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	newTools, err := newPlan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "session"})
	newPlan.Release()
	if err != nil || len(newTools) != 0 {
		t.Fatalf("deactivated tools=%#v err=%v", newTools, err)
	}
	done := make(chan error, 1)
	go func() { done <- mount.Close(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("close completed with lease: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	plan.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not finish")
	}
}

func TestMountCloseQuarantineUntilRunnerExits(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	options := testOptions()
	options.Limits.MaxWait = 10 * time.Millisecond
	options.Runner = RunnerFunc(func(context.Context, Request) (Response, error) {
		close(entered)
		<-release
		return Response{Status: ResponseCompleted}, nil
	})
	registry, mount := mountTestRegistry(t, options)
	plan, tool := resolveTestTool(t, registry, "session", "", "")
	input, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(integrationInput))
	if err != nil {
		t.Fatal(err)
	}
	executionDone := make(chan struct{})
	go func() {
		_, _ = tool.Executor.Execute(context.Background(), runtime.ToolCall{ID: "close-call", SessionID: "session", RunID: "run", Name: ToolName, Input: input})
		close(executionDone)
	}()
	<-entered
	select {
	case <-executionDone:
	case <-time.After(time.Second):
		t.Fatal("tool did not time out")
	}
	plan.Release()
	mount.Deactivate()
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = mount.Close(closeCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close=%v", err)
	}
	close(release)
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMountedRunnerPreservesSelfCloseProtection(t *testing.T) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	var mount *composition.Mount
	closeErrors := make(chan error, 1)
	options := testOptions()
	options.Runner = RunnerFunc(func(ctx context.Context, _ Request) (Response, error) {
		closeErrors <- mount.Close(ctx)
		return Response{Status: ResponseCompleted}, nil
	})
	mount, err = Mount(context.Background(), registry, testComponent("self-close"), options)
	if err != nil {
		t.Fatal(err)
	}
	plan, tool := resolveTestTool(t, registry, "session", "", "")
	input, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(integrationInput))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Executor.Execute(context.Background(), runtime.ToolCall{ID: "self-close", SessionID: "session", RunID: "run", Name: ToolName, Input: input}); err != nil {
		t.Fatal(err)
	}
	if err := <-closeErrors; !errors.Is(err, extension.ErrSelfClose) {
		t.Fatalf("self-close=%v", err)
	}
	plan.Release()
	mount.Deactivate()
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMountStrictResumeFingerprintTracksEveryBehaviorField(t *testing.T) {
	base := testOptions()
	registry, mount := mountTestRegistry(t, base)
	plan, _ := resolveTestTool(t, registry, "resume-session", "", "")
	descriptor := plan.Descriptor()
	plan.Release()
	closeIntegrationMount(t, mount)
	sealed, err := session.VerifyExtensionPlanForSession("resume-session", descriptor)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*Options){
		"runner":   func(o *Options) { o.RunnerIdentity += "-changed" },
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
			changedRegistry, changedMount := mountTestRegistry(t, changed)
			defer closeIntegrationMount(t, changedMount)
			resumed, err := changedRegistry.AcquireResumePlan(context.Background(), runtime.ResumePlanRequest{SessionID: "resume-session", Plan: sealed})
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Release()
			if resumed.Descriptor().Fingerprint == descriptor.Fingerprint {
				t.Fatal("behavior drift retained fingerprint")
			}
		})
	}
}
