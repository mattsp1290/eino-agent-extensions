//go:build linux || darwin

package rtkreducer

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
	"github.com/mattsp1290/eino-agent/tools"
)

func TestIntegrationResumeRejectsRTKConfigHashDriftBeforeMutation(t *testing.T) {
	executable := writeCountingRTK(t, filepath.Join(t.TempDir(), "invocations"))
	originalOptions := fixtureOptionsForExecutable(t, executable)
	firstRegistry, firstDatabase, firstMounts := newIntegrationFixture(t, originalOptions, func(context.Context, tools.Execution) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}, allowIntegrationPolicy())
	plan, err := firstRegistry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "resume-session"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	plan.Release()
	closeIntegrationMounts(t, firstMounts)

	run := seedIntegrationPendingRun(t, firstDatabase, descriptor)
	defer firstDatabase.Close()

	changedOptions := originalOptions
	changedOptions.Limits.MaxResultBytes++
	secondRegistry, secondDatabase, secondMounts := newIntegrationFixture(t, changedOptions, func(context.Context, tools.Execution) (json.RawMessage, error) {
		return json.RawMessage(`{"unexpected":true}`), nil
	}, allowIntegrationPolicy())
	defer func() {
		closeIntegrationMounts(t, secondMounts)
		_ = secondDatabase.Close()
	}()

	sealed, err := session.VerifyExtensionPlanForSession("resume-session", descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if resumed, resumeErr := secondRegistry.AcquireResumePlan(context.Background(), runtime.ResumePlanRequest{SessionID: "resume-session", Plan: sealed}); resumed != nil || !errors.Is(resumeErr, runtime.ErrExtensionPlanMismatch) {
		if resumed != nil {
			resumed.Release()
		}
		t.Fatalf("changed RTK config accepted: plan=%v err=%v", resumed, resumeErr)
	}

	before := integrationResumeSnapshot(t, firstDatabase, run.ID)
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(firstDatabase), runtime.WithRunPlanProvider(secondRegistry),
		runtime.WithModelResolver(integrationResolver{streamer: integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
			return nil, errors.New("model must not be called after resume mismatch")
		})}), runtime.WithOwnerID("new-owner"), runtime.WithIDGenerator(&integrationIDs{}), runtime.WithLease(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	handle, resumeErr := orchestrator.Resume(context.Background(), run.ID)
	if handle != nil || !errors.Is(resumeErr, runtime.ErrExtensionPlanMismatch) {
		t.Fatalf("Resume drift = handle=%v err=%v", handle, resumeErr)
	}
	if after := integrationResumeSnapshot(t, firstDatabase, run.ID); string(before) != string(after) {
		t.Fatalf("resume mismatch mutated durable state:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestIntegrationResumeRejectsArtifactDigestBindingAndRemovalDrift(t *testing.T) {
	originalExecutable := writeCountingRTK(t, filepath.Join(t.TempDir(), "original-invocations"))
	originalOptions := fixtureOptionsForExecutable(t, originalExecutable)
	execute := func(context.Context, tools.Execution) (json.RawMessage, error) {
		return json.RawMessage(`{"stdout":"fixture"}`), nil
	}
	originalRegistry, originalDatabase, originalMounts := newIntegrationFixture(t, originalOptions, execute, allowIntegrationPolicy())
	plan, err := originalRegistry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "resume-drift-session"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	plan.Release()
	closeIntegrationMounts(t, originalMounts)
	if err := originalDatabase.Close(); err != nil {
		t.Fatal(err)
	}
	sealed, err := session.VerifyExtensionPlanForSession("resume-drift-session", descriptor)
	if err != nil {
		t.Fatal(err)
	}
	assertMismatch := func(t *testing.T, registry *composition.Registry) {
		t.Helper()
		resumed, acquireErr := registry.AcquireResumePlan(context.Background(), runtime.ResumePlanRequest{SessionID: "resume-drift-session", Plan: sealed})
		if resumed != nil {
			resumed.Release()
		}
		if resumed != nil || !errors.Is(acquireErr, runtime.ErrExtensionPlanMismatch) {
			t.Fatalf("identity drift accepted: plan=%v err=%v", resumed, acquireErr)
		}
	}

	t.Run("binding", func(t *testing.T) {
		changed := originalOptions
		changed.Bindings = append([]Binding(nil), originalOptions.Bindings...)
		changed.Bindings[0].Fields = []Field{{Name: "stdout", Filter: GitDiff}}
		registry, database, mounts := newIntegrationFixture(t, changed, execute, allowIntegrationPolicy())
		defer database.Close()
		defer closeIntegrationMounts(t, mounts)
		assertMismatch(t, registry)
	})

	t.Run("executable digest", func(t *testing.T) {
		changedExecutable := writeCountingRTK(t, filepath.Join(t.TempDir(), "changed-invocations"))
		changed := fixtureOptionsForExecutable(t, changedExecutable)
		registry, database, mounts := newIntegrationFixture(t, changed, execute, allowIntegrationPolicy())
		defer database.Close()
		defer closeIntegrationMounts(t, mounts)
		assertMismatch(t, registry)
	})

	t.Run("artifact", func(t *testing.T) {
		component := integrationComponent("rtk-reducer")
		component.Artifact.Hash = "different-rtk-artifact"
		component.Artifact.ConfigHash = ""
		registry, database, mounts := newIntegrationFixtureWithReducerComponent(t, originalOptions, execute, allowIntegrationPolicy(), component)
		defer database.Close()
		defer closeIntegrationMounts(t, mounts)
		assertMismatch(t, registry)
	})

	t.Run("removal", func(t *testing.T) {
		registry, err := composition.NewRegistry(nil)
		if err != nil {
			t.Fatal(err)
		}
		toolMount, err := registry.Mount(context.Background(), integrationComponent("fixture-tool"), composition.InstallerFunc(func(_ context.Context, registrar *composition.Registrar) error {
			return registrar.Tool(composition.ToolRegistration{
				ID: "fixture-tool-registration", Scope: extension.GlobalScope(),
				Definition: tools.Definition{Name: integrationToolName, Description: "integration fixture", Execute: execute, Retention: runtime.RetentionPolicy{MaxInlineBytes: 8 << 20}},
			})
		}))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			toolMount.Deactivate()
			if err := toolMount.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		}()
		assertMismatch(t, registry)
	})
}

func seedIntegrationPendingRun(t *testing.T, database *store.Store, descriptor session.ExtensionPlanDescriptor) session.Run {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := database.CreateSession(ctx, session.Session{ID: "resume-session", Directory: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run, err := database.AdmitRun(ctx, session.Run{
		ID: "resume-run", SessionID: "resume-session", OwnerID: "old-owner", ClaimToken: "old-claim",
		Agent: "rtk-integration-agent", ProviderID: "rtk-integration-provider", ModelID: "rtk-integration-model",
		Status: session.RunPending, Config: map[string]string{"workspace_id": "resume-workspace", "workspace_root": t.TempDir()},
		ExtensionPlan: descriptor, CreatedAt: now,
	}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	execution := database.Execution(session.RunFence{RunID: run.ID, ClaimToken: run.ClaimToken})
	if _, err := execution.AppendMessage(ctx, session.Message{ID: "resume-assistant", SessionID: run.SessionID, RunID: run.ID, Role: session.RoleAssistant, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	call := session.ToolCall{
		ID: "resume-call", SessionID: run.SessionID, RunID: run.ID, MessageID: "resume-assistant",
		RequestPartID: "resume-request-part", ResultMessageID: "resume-result-message", ResultPartID: "resume-result-part", Name: integrationToolName,
		Pattern: integrationToolName, Input: json.RawMessage(`{}`), Status: session.ToolCallPending,
	}
	payload, _ := json.Marshal(map[string]any{"id": call.ID, "name": call.Name, "arguments": json.RawMessage(`{}`)})
	if _, err := execution.CreateToolCall(ctx, session.CreateToolCallRequest{
		Call:        call,
		RequestPart: session.Part{ID: "resume-request-part", MessageID: call.MessageID, SessionID: call.SessionID, RunID: call.RunID, Kind: session.PartToolCall, Payload: payload, CreatedAt: now, UpdatedAt: now},
		Event:       session.ToolTransitionEvent{ID: "resume-create-event", CreatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	return run
}

func integrationResumeSnapshot(t *testing.T, database *store.Store, runID session.RunID) []byte {
	t.Helper()
	run, err := database.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	call, err := database.GetToolCall(context.Background(), "resume-call")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(struct {
		Run  session.Run
		Call session.ToolCall
	}{run, call})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func closeIntegrationMounts(t *testing.T, mounts []*composition.Mount) {
	t.Helper()
	for _, mount := range mounts {
		mount.Deactivate()
	}
	for _, mount := range mounts {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := mount.Close(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
	}
}
