package workspaceinstructions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
)

func TestIntegrationResumeIdenticalPlanRereadsCurrentInstructions(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(file, []byte("original-before-interruption"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := trustedOptions(root, "")
	descriptor := acquireIntegrationResumeDescriptor(t, base, file)
	if err := os.WriteFile(file, []byte("current-at-resume"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, mounts := mountIntegrationResumeRegistry(t, base, file)
	defer closeIntegrationResumeMounts(t, mounts)
	sealed, err := session.VerifyExtensionPlanForSession("resume-session", descriptor)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := registry.AcquireResumePlan(context.Background(), runtime.ResumePlanRequest{SessionID: "resume-session", Plan: sealed})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Release()
	prompts := plan.Prompts()
	if len(prompts) != 1 {
		t.Fatalf("prompts = %#v", prompts)
	}
	section, err := prompts[0].Provider.ProvidePrompt(context.Background(), runtime.PromptContext{SessionID: "resume-session", RunID: "resume-run"})
	if err != nil || !bytes.Contains([]byte(section), []byte("current-at-resume")) || bytes.Contains([]byte(section), []byte("original-before-interruption")) {
		t.Fatalf("section = %q, err = %v", section, err)
	}
}

func TestIntegrationResumeRejectsEveryDriftBeforeDurableMutation(t *testing.T) {
	mutations := map[string]func(*Options){
		"file-name":         func(o *Options) { o.FileNames = []string{"OTHER.md"} },
		"file-order":        func(o *Options) { o.FileNames = []string{"SECOND.md", "AGENTS.md"} },
		"resolver-identity": func(o *Options) { o.ResolverIdentity += "-changed" },
		"max-file-names":    func(o *Options) { o.Limits.MaxFileNames++ },
		"max-chain-depth":   func(o *Options) { o.Limits.MaxChainDepth++ },
		"max-file-bytes":    func(o *Options) { o.Limits.MaxFileBytes++ },
		"max-section-bytes": func(o *Options) { o.Limits.MaxSectionBytes++ },
		"max-in-flight":     func(o *Options) { o.Limits.MaxInFlight++ },
		"max-wait":          func(o *Options) { o.Limits.MaxWait++ },
		"order":             func(o *Options) { o.Order = DefaultOrder + 1 },
		"scope":             func(o *Options) { o.Scope = extension.SessionScope("resume-session") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "AGENTS.md")
			if err := os.WriteFile(file, []byte("body"), 0o600); err != nil {
				t.Fatal(err)
			}
			base := trustedOptions(root, "")
			base.FileNames = []string{"AGENTS.md", "SECOND.md"}
			if name == "file-name" {
				base.FileNames = []string{"AGENTS.md"}
			}
			descriptor := acquireIntegrationResumeDescriptor(t, base, file)
			database, run := seedIntegrationResumeRun(t, descriptor)
			defer database.Close()
			before := integrationResumeSnapshot(t, database, run)
			changed := base
			changed.FileNames = append([]string(nil), base.FileNames...)
			mutate(&changed)
			registry, mounts := mountIntegrationResumeRegistry(t, changed, file)
			defer closeIntegrationResumeMounts(t, mounts)
			var modelCalls int
			orchestrator := newIntegrationResumeOrchestrator(t, database, registry, integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
				modelCalls++
				return []*einoschema.Message{einoschema.AssistantMessage("unexpected", nil)}, nil
			}))
			handle, err := orchestrator.Resume(context.Background(), run.ID)
			if handle != nil || !errors.Is(err, runtime.ErrExtensionPlanMismatch) {
				t.Fatalf("resume = %v, %v", handle, err)
			}
			after := integrationResumeSnapshot(t, database, run)
			if modelCalls != 0 || !bytes.Equal(before, after) {
				t.Fatal("resume mismatch mutated durable state")
			}
		})
	}
}

func acquireIntegrationResumeDescriptor(t *testing.T, options Options, file string) session.ExtensionPlanDescriptor {
	t.Helper()
	registry, mounts := mountIntegrationResumeRegistry(t, options, file)
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "resume-session"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: "resume-session"})
	if err != nil || len(resolved) != 1 {
		t.Fatalf("tools = %#v, %v", resolved, err)
	}
	if _, err := resolved[0].InputDecoder.DecodeToolInput(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	plan.Release()
	closeIntegrationResumeMounts(t, mounts)
	return descriptor
}

func mountIntegrationResumeRegistry(t *testing.T, options Options, file string) (*composition.Registry, []*composition.Mount) {
	t.Helper()
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	instructions, err := Mount(context.Background(), registry, testComponent("resume-instructions"), options)
	if err != nil {
		t.Fatal(err)
	}
	touch := mountIntegrationTouch(t, registry, file, "current-at-resume")
	return registry, []*composition.Mount{touch, instructions}
}

func closeIntegrationResumeMounts(t *testing.T, mounts []*composition.Mount) {
	t.Helper()
	for _, mount := range mounts {
		closeTestMount(t, mount)
	}
}

func seedIntegrationResumeRun(t *testing.T, descriptor session.ExtensionPlanDescriptor) (*store.Store, session.Run) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "workspace-resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := database.CreateSession(ctx, session.Session{ID: "resume-session", Directory: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run, err := database.AdmitRun(ctx, session.Run{
		ID: "resume-run", SessionID: "resume-session", OwnerID: "old-owner", ClaimToken: "old-claim",
		Agent: "workspace-integration-agent", ProviderID: "workspace-integration-provider", ModelID: "workspace-integration-model",
		Status: session.RunPending, Config: map[string]string{}, ExtensionPlan: descriptor, CreatedAt: now,
	}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	execution := database.Execution(session.RunFence{RunID: run.ID, ClaimToken: run.ClaimToken})
	if _, err := execution.AppendMessage(ctx, session.Message{
		ID: "resume-assistant", SessionID: run.SessionID, RunID: run.ID,
		Role: session.RoleAssistant, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	call := session.ToolCall{
		ID: "resume-touch-call", SessionID: run.SessionID, RunID: run.ID, MessageID: "resume-assistant",
		RequestPartID: "resume-request-part", ResultMessageID: "resume-result-message", ResultPartID: "resume-result-part",
		Name: "instructions_touch", Pattern: "instructions_touch", Input: json.RawMessage(`{}`),
		Status: session.ToolCallPending, RetrySafe: false,
	}
	payload, _ := json.Marshal(map[string]any{"id": call.ID, "name": call.Name, "arguments": json.RawMessage(`{}`)})
	if _, err := execution.CreateToolCall(ctx, session.CreateToolCallRequest{
		Call: call,
		RequestPart: session.Part{
			ID: call.RequestPartID, MessageID: call.MessageID, SessionID: call.SessionID, RunID: call.RunID,
			Kind: session.PartToolCall, Payload: payload, CreatedAt: now, UpdatedAt: now,
		},
		Event: session.ToolTransitionEvent{ID: "resume-create-event", CreatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	wait := time.Until(run.LeaseUntil) + 10*time.Millisecond
	if wait > 0 {
		timer := time.NewTimer(wait)
		<-timer.C
		timer.Stop()
	}
	return database, run
}

func newIntegrationResumeOrchestrator(t *testing.T, database *store.Store, registry *composition.Registry, streamer model.Streamer) *runtime.StreamingOrchestrator {
	t.Helper()
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(integrationModelResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(permissions.PolicyFunc(func(context.Context, permissions.Request) (permissions.Decision, error) {
			return permissions.Decision{Action: permissions.ActionAllow}, nil
		})), runtime.WithIDGenerator(&integrationIDs{}), runtime.WithClock(time.Now),
		runtime.WithOwnerID("resume-owner"), runtime.WithQueueSize(16), runtime.WithLease(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	return orchestrator
}

func waitIntegrationResume(t *testing.T, handle runtime.Handle) runtime.Result {
	t.Helper()
	select {
	case result := <-handle.Done():
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("resume timed out")
		return runtime.Result{}
	}
}

func integrationResumeSnapshot(t *testing.T, database *store.Store, run session.Run) []byte {
	t.Helper()
	storedRun, err := database.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	call, err := database.GetToolCall(context.Background(), "resume-touch-call")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(struct {
		Run  session.Run
		Call session.ToolCall
	}{storedRun, call})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
