//go:build linux || darwin

package pythonrepl

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func testComponent(instance string) extension.Component {
	return extension.Component{InstanceID: instance, Artifact: extension.Artifact{
		Name: "python-repl", Version: "test", Hash: "synthetic-artifact", SourceKind: extension.SourceNative,
	}}
}

func TestMountPublishesBothToolsWithoutPythonSideEffects(t *testing.T) {
	options := testOptions(t)
	before, err := os.ReadDir(options.TempRoot)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	mount, err := Mount(context.Background(), registry, testComponent("python-repl"), options)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadDir(options.TempRoot)
	if err != nil || len(after) != len(before) {
		t.Fatalf("mount created files: before=%d after=%d err=%v", len(before), len(after), err)
	}
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "session", WorkspaceID: "workspace", WorkspaceRoot: t.TempDir()})
	if err != nil || len(tools) != 2 {
		t.Fatalf("tools=%d err=%v", len(tools), err)
	}
	if tools[0].Name != ClearToolName || tools[1].Name != ExecuteToolName {
		// Eino's canonical descriptor ordering is by registration identity/name,
		// not installation call order.
		names := map[string]bool{}
		for _, tool := range tools {
			names[tool.Name] = true
		}
		if !names[ClearToolName] || !names[ExecuteToolName] {
			t.Fatalf("tool names = %#v", names)
		}
	}
	mount.Deactivate()
	closeDone := make(chan error, 1)
	go func() { closeDone <- mount.Close(context.Background()) }()
	select {
	case err := <-closeDone:
		t.Fatalf("close completed with acquired plan: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	plan.Release()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not complete after plan release")
	}
}

func TestMountRejectsInvalidAndRollsBackCollision(t *testing.T) {
	options := testOptions(t)
	component := testComponent("invalid")
	if _, err := Mount(context.Background(), nil, component, options); err == nil {
		t.Fatal("nil registry accepted")
	}
	registry, _ := composition.NewRegistry(nil)
	component.Artifact.SourceKind = extension.SourceWasm
	if _, err := Mount(context.Background(), registry, component, options); err == nil {
		t.Fatal("non-native component accepted")
	}
	component.Artifact.SourceKind = extension.SourceNative
	component.Artifact.ConfigHash = "mismatch"
	if _, err := Mount(context.Background(), registry, component, options); err == nil {
		t.Fatal("hash mismatch accepted")
	}

	registry, _ = composition.NewRegistry(nil)
	first, err := Mount(context.Background(), registry, testComponent("first"), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { first.Deactivate(); _ = first.Close(context.Background()) }()
	if duplicate, err := Mount(context.Background(), registry, testComponent("duplicate"), options); duplicate != nil || err == nil {
		t.Fatalf("duplicate mount present=%t error=%v", duplicate != nil, err)
	}
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Release()
	tools, err := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "session", WorkspaceID: "workspace", WorkspaceRoot: t.TempDir()})
	if err != nil || len(tools) != 2 {
		t.Fatalf("registry after rollback tools=%d err=%v", len(tools), err)
	}
}

func TestMountScopeAndOrderRemainEinoOwnedIdentity(t *testing.T) {
	base := testOptions(t)
	fingerprint := func(options Options, sessionID string) string {
		registry, _ := composition.NewRegistry(nil)
		mount, err := Mount(context.Background(), registry, testComponent("identity"), options)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: session.ID(sessionID)})
		if err != nil {
			t.Fatal(err)
		}
		result := plan.Descriptor().Fingerprint
		plan.Release()
		mount.Deactivate()
		if err := mount.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		return result
	}
	baseHash, _ := ConfigHash(base)
	baseFingerprint := fingerprint(base, "identity-session")
	ordered := base
	ordered.Order = 912
	orderedHash, _ := ConfigHash(ordered)
	orderedFingerprint := fingerprint(ordered, "identity-session")
	scoped := base
	scoped.Scope = extension.SessionScope("identity-session")
	scopedHash, _ := ConfigHash(scoped)
	scopedFingerprint := fingerprint(scoped, "identity-session")
	if baseHash != orderedHash || baseHash != scopedHash {
		t.Fatal("scope/order entered ConfigHash")
	}
	if baseFingerprint == orderedFingerprint || baseFingerprint == scopedFingerprint {
		t.Fatal("scope/order did not enter frozen Eino identity")
	}
}
