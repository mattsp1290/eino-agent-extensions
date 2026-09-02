package askuser

import (
	"context"
	"encoding/json"
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
		Name: "ask-user", Version: "test", Hash: "synthetic-artifact-v1", SourceKind: extension.SourceNative,
	}}
}

func mountTestRegistry(t *testing.T, options Options) (*composition.Registry, *composition.Mount) {
	t.Helper()
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	mount, err := Mount(context.Background(), registry, testComponent("ask-user"), options)
	if err != nil {
		t.Fatal(err)
	}
	return registry, mount
}

func resolveTestTool(t *testing.T, registry *composition.Registry, sessionID session.ID) (*runtime.RunPlan, runtime.Tool) {
	t.Helper()
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: sessionID, WorkspaceID: "workspace"})
	if err != nil {
		plan.Release()
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Name != ToolName {
		plan.Release()
		t.Fatalf("resolved tools = %#v", resolved)
	}
	return plan, resolved[0]
}

type schemaSubset struct {
	AdditionalProperties *bool                   `json:"additionalProperties"`
	Properties           map[string]schemaSubset `json:"properties"`
	Items                *schemaSubset           `json:"items"`
}

func TestMountResolvesFrozenToolAndExecutes(t *testing.T) {
	registry, mount := mountTestRegistry(t, testOptions())
	plan, tool := resolveTestTool(t, registry, "session")
	descriptor := plan.Descriptor()
	if len(descriptor.Components) != 1 || len(descriptor.Components[0].Tools) != 1 {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	identity := descriptor.Components[0].Tools[0]
	if identity.Name != ToolName || identity.RegistrationID != registrationID || identity.Scope != extension.GlobalScope() || identity.Order != DefaultOrder {
		t.Fatalf("tool identity = %#v", identity)
	}
	schema, err := tool.Info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	rawSchema, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"minItems":2`, `"maxItems":5`} {
		if !strings.Contains(string(rawSchema), required) {
			t.Fatalf("resolved schema missing %s: %s", required, rawSchema)
		}
	}
	var schemaDocument schemaSubset
	if err := json.Unmarshal(rawSchema, &schemaDocument); err != nil {
		t.Fatal(err)
	}
	optionsSchema, ok := schemaDocument.Properties["options"]
	if !ok || optionsSchema.Items == nil {
		t.Fatalf("options schema = %#v", optionsSchema)
	}
	for name, value := range map[string]*bool{
		"top-level": schemaDocument.AdditionalProperties,
		"option":    optionsSchema.Items.AdditionalProperties,
	} {
		if value == nil || *value {
			t.Fatalf("%s additionalProperties = %#v, want false", name, value)
		}
	}
	input, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"question":"Which?","options":[{"label":"A"},{"label":"B"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	pattern, err := tool.Pattern.ResolvePermissionPattern(context.Background(), input)
	if err != nil || pattern != PermissionAsk || len(tool.Scope.Permissions) != 1 || tool.Scope.Permissions[0] != PermissionAsk {
		t.Fatalf("permission pattern=%q scope=%#v err=%v", pattern, tool.Scope, err)
	}
	output, err := tool.Executor.Execute(context.Background(), runtime.ToolCall{
		ID: "call", SessionID: "session", RunID: "run", Name: ToolName, Input: input,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := json.Unmarshal(output.Structured, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSelected || result.Answer != "A" || result.SelectedOption != 1 || output.Output != string(output.Structured) {
		t.Fatalf("output=%#v result=%#v", output, result)
	}
	plan.Release()
	mount.Deactivate()
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMountSupportsExactSessionScopeAndExplicitOrder(t *testing.T) {
	options := testOptions()
	options.Scope = extension.SessionScope("only-session")
	options.Order = 42
	registry, mount := mountTestRegistry(t, options)
	plan, _ := resolveTestTool(t, registry, "only-session")
	descriptor := plan.Descriptor()
	identity := descriptor.Components[0].Tools[0]
	if identity.Scope != options.Scope || identity.Order != options.Order {
		t.Fatalf("identity = %#v", identity)
	}
	plan.Release()
	other, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "other-session"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := other.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "other-session", WorkspaceID: "workspace"})
	other.Release()
	if err != nil || len(resolved) != 0 {
		t.Fatalf("other session tools=%#v err=%v", resolved, err)
	}
	mount.Deactivate()
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMountRejectsInvalidIdentityAndConfiguration(t *testing.T) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Mount(context.Background(), nil, testComponent("ask-user"), testOptions()); err == nil {
		t.Fatal("nil registry accepted")
	}
	component := testComponent("ask-user")
	component.Artifact.SourceKind = extension.SourceWasm
	if _, err := Mount(context.Background(), registry, component, testOptions()); err == nil {
		t.Fatal("non-native component accepted")
	}
	component = testComponent("ask-user")
	component.Artifact.ConfigHash = "mismatch"
	if _, err := Mount(context.Background(), registry, component, testOptions()); err == nil {
		t.Fatal("config mismatch accepted")
	}
}

func TestMountedDecoderRejectsLossyUnicodeBeforeDurability(t *testing.T) {
	registry, mount := mountTestRegistry(t, testOptions())
	plan, tool := resolveTestTool(t, registry, "session")
	defer func() {
		plan.Release()
		mount.Deactivate()
		_ = mount.Close(context.Background())
	}()
	invalidQuestion := append([]byte(`{"question":"`), 0xff)
	invalidQuestion = append(invalidQuestion, []byte(`","options":[{"label":"A"},{"label":"B"}]}`)...)
	invalidLabel := append([]byte(`{"question":"Q","options":[{"label":"`), 0xff)
	invalidLabel = append(invalidLabel, []byte(`"},{"label":"B"}]}`)...)
	invalidDescription := append([]byte(`{"question":"Q","options":[{"label":"A","description":"`), 0xff)
	invalidDescription = append(invalidDescription, []byte(`"},{"label":"B"}]}`)...)
	for name, raw := range map[string][]byte{
		"invalid question utf8":    invalidQuestion,
		"invalid label utf8":       invalidLabel,
		"invalid description utf8": invalidDescription,
		"isolated high surrogate":  []byte(`{"question":"Q\ud800","options":[{"label":"A"},{"label":"B"}]}`),
		"isolated low surrogate":   []byte(`{"question":"Q\udc00","options":[{"label":"A"},{"label":"B"}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			canonical, err := tool.InputDecoder.DecodeToolInput(context.Background(), raw)
			if err == nil {
				t.Fatalf("lossy input accepted as %s", canonical)
			}
			if strings.Contains(string(canonical), "�") {
				t.Fatalf("lossy replacement returned: %s", canonical)
			}
		})
	}
}

func TestMountDuplicateRollsBackCandidate(t *testing.T) {
	registry, first := mountTestRegistry(t, testOptions())
	defer func() {
		first.Deactivate()
		_ = first.Close(context.Background())
	}()
	if duplicate, err := Mount(context.Background(), registry, testComponent("ask-user-duplicate"), testOptions()); duplicate != nil || err == nil {
		t.Fatalf("duplicate mount present=%t err=%v", duplicate != nil, err)
	}
	plan, _ := resolveTestTool(t, registry, "session")
	plan.Release()
}

func TestMountPlanLeaseDelaysCleanup(t *testing.T) {
	registry, mount := mountTestRegistry(t, testOptions())
	plan, _ := resolveTestTool(t, registry, "session")
	mount.Deactivate()
	newPlan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	newTools, err := newPlan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "session", WorkspaceID: "workspace"})
	newPlan.Release()
	if err != nil || len(newTools) != 0 {
		t.Fatalf("deactivated mount remained visible: tools=%#v err=%v", newTools, err)
	}
	done := make(chan error, 1)
	go func() { done <- mount.Close(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("close completed while plan leased: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	plan.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not finish after release")
	}
}

func TestMountedToolEnforcesSharedCapacity(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	options := testOptions()
	options.Limits.MaxInFlight = 1
	options.Limits.MaxWait = 20 * time.Millisecond
	options.Responder = ResponderFunc(func(context.Context, Request) (Response, error) {
		close(entered)
		<-release
		return Response{Kind: ResponseDismissed}, nil
	})
	registry, mount := mountTestRegistry(t, options)
	plan, tool := resolveTestTool(t, registry, "session")
	input, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"question":"Q?","options":[{"label":"A"},{"label":"B"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan runtime.ToolResult, 1)
	go func() {
		result, _ := tool.Executor.Execute(context.Background(), runtime.ToolCall{ID: "first", SessionID: "session", RunID: "run", Name: ToolName, Input: input})
		firstDone <- result
	}()
	<-entered
	second, err := tool.Executor.Execute(context.Background(), runtime.ToolCall{ID: "second", SessionID: "session", RunID: "run", Name: ToolName, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	var secondResult Result
	if err := json.Unmarshal(second.Structured, &secondResult); err != nil || secondResult.Status != StatusUnavailable {
		t.Fatalf("saturated result=%#v err=%v", secondResult, err)
	}
	select {
	case first := <-firstDone:
		var firstResult Result
		if err := json.Unmarshal(first.Structured, &firstResult); err != nil || firstResult.Status != StatusTimedOut {
			t.Fatalf("first result=%#v err=%v", firstResult, err)
		}
	case <-time.After(time.Second):
		t.Fatal("first tool did not time out")
	}
	close(release)
	plan.Release()
	mount.Deactivate()
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMountCloseReportsDeadlineUntilResponderQuiesces(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	options := testOptions()
	options.Limits.MaxWait = 10 * time.Millisecond
	options.Responder = ResponderFunc(func(context.Context, Request) (Response, error) {
		close(entered)
		<-release
		return Response{Kind: ResponseDismissed}, nil
	})
	registry, mount := mountTestRegistry(t, options)
	plan, tool := resolveTestTool(t, registry, "session")
	input, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"question":"Q?","options":[{"label":"A"},{"label":"B"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	executionDone := make(chan struct{})
	go func() {
		_, _ = tool.Executor.Execute(context.Background(), runtime.ToolCall{ID: "close-deadline", SessionID: "session", RunID: "run", Name: ToolName, Input: input})
		close(executionDone)
	}()
	<-entered
	select {
	case <-executionDone:
	case <-time.After(time.Second):
		t.Fatal("tool wait did not time out")
	}
	plan.Release()
	mount.Deactivate()
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = mount.Close(closeCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close error = %v", err)
	}
	close(release)
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mount.Close(context.Background()); err != nil {
		t.Fatalf("repeated close error = %v", err)
	}
}

func TestMountedResponderPreservesSelfCloseProtection(t *testing.T) {
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	var mount *composition.Mount
	closeErrors := make(chan error, 1)
	options := testOptions()
	options.Responder = ResponderFunc(func(ctx context.Context, _ Request) (Response, error) {
		closeErrors <- mount.Close(ctx)
		return Response{Kind: ResponseDismissed}, nil
	})
	mount, err = Mount(context.Background(), registry, testComponent("ask-user"), options)
	if err != nil {
		t.Fatal(err)
	}
	plan, tool := resolveTestTool(t, registry, "session")
	input, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"question":"Q?","options":[{"label":"A"},{"label":"B"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Executor.Execute(context.Background(), runtime.ToolCall{ID: "self-close", SessionID: "session", RunID: "run", Name: ToolName, Input: input}); err != nil {
		t.Fatal(err)
	}
	if err := <-closeErrors; !errors.Is(err, extension.ErrSelfClose) {
		t.Fatalf("self-close error = %v", err)
	}
	plan.Release()
	mount.Deactivate()
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMountStrictResumeFingerprintTracksBehavior(t *testing.T) {
	base := testOptions()
	registry, mount := mountTestRegistry(t, base)
	plan, _ := resolveTestTool(t, registry, "resume-session")
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
	mutations := map[string]func(*Options){
		"responder identity": func(options *Options) { options.ResponderIdentity += "-changed" },
		"question":           func(options *Options) { options.Limits.MaxQuestionBytes++ },
		"label":              func(options *Options) { options.Limits.MaxOptionLabelBytes++ },
		"description":        func(options *Options) { options.Limits.MaxOptionDescriptionBytes++ },
		"answer":             func(options *Options) { options.Limits.MaxCustomAnswerBytes++ },
		"capacity":           func(options *Options) { options.Limits.MaxInFlight++ },
		"wait":               func(options *Options) { options.Limits.MaxWait++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			changedRegistry, changedMount := mountTestRegistry(t, changed)
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
				t.Fatal("behavior drift retained fingerprint")
			}
		})
	}
}
