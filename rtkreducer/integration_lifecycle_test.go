//go:build linux || darwin

package rtkreducer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/config"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	"github.com/mattsp1290/eino-agent/tools"
)

func TestIntegrationCloseWaitsForActiveRTKAndCleansOwnedRoot(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	executable := writeBlockingRTK(t, started)
	options := fixtureOptionsForExecutable(t, executable)
	options.Limits.Timeout = 5 * time.Second
	options.Bindings[0].Mode = JSONMirror
	options.Bindings[0].MatchInput = nil
	options.Bindings[0].Fields = []Field{{Name: "stdout", Filter: Log}}
	registry, database, mounts := newIntegrationFixture(t, options, func(context.Context, tools.Execution) (json.RawMessage, error) {
		return json.RawMessage(`{"stdout":` + strconvQuote(strings.Repeat("verbose output ", 100)) + `}`), nil
	}, allowIntegrationPolicy())
	reducerMount := mounts[0]
	defer func() {
		for _, mount := range mounts {
			mount.Deactivate()
			_ = mount.Close(context.Background())
		}
		_ = database.Close()
	}()

	streamer := integrationScript(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latestIntegrationToolMessage(request.Messages) != nil {
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
		return integrationToolCall("lifecycle-call", integrationToolName, `{}`), nil
	})
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(integrationResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(allowIntegrationPolicy()),
		runtime.WithIDGenerator(&integrationIDs{}), runtime.WithOwnerID("lifecycle-owner"), runtime.WithLease(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := model.Selection{ProviderID: "rtk-integration-provider", ModelID: "rtk-integration-model"}
	handle, err := orchestrator.Start(context.Background(), runtime.Request{
		SessionID: "lifecycle-session", Message: runtime.UserMessage{Content: "run lifecycle fixture"},
		Config: config.Snapshot{Agent: config.Agent{Name: "rtk-integration-agent", Model: selection}, Model: selection,
			Metadata: map[string]string{"workspace_id": "lifecycle-workspace", "workspace_root": t.TempDir()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForLifecycleFile(t, started)
	reducerMount.Deactivate()
	futurePlan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: "future-session"})
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range futurePlan.Descriptor().Components {
		if component.InstanceID == "rtk-reducer" {
			futurePlan.Release()
			t.Fatal("deactivated reducer remained selectable for a future plan")
		}
	}
	futurePlan.Release()
	closeContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	closeErr := reducerMount.Close(closeContext)
	cancel()
	if !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("close while callback active = %v, want deadline exceeded", closeErr)
	}
	select {
	case result := <-handle.Done():
		if result.Error != nil || result.Status != session.RunCompleted {
			t.Fatalf("lifecycle run = %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lifecycle run timed out")
	}
	if err := reducerMount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(options.TempRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".rtk-reducer-") {
			t.Fatalf("owned RTK root remains after successful close: %s", entry.Name())
		}
	}
}

func writeBlockingRTK(t *testing.T, started string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rtk")
	script := "#!/bin/sh\nprintf x >> " + shellQuote(started) + "\n/bin/sleep 1\nprintf reduced\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForLifecycleFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("RTK fixture did not start: %s", path)
}
