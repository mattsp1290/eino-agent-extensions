//go:build linux || darwin

package rtkreducer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/config"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
	"github.com/mattsp1290/eino-agent/tools/einotools"
	"github.com/mattsp1290/eino-tools/catalog"
	"github.com/mattsp1290/eino-tools/search"
	"github.com/mattsp1290/eino-tools/shell"
)

func TestIntegrationRealStandardShellJSONMirror(t *testing.T) {
	rtk := requireRealRTK(t)
	workspace := writeShellGoModule(t)
	environment := standardShellEnvironment(t)

	options := rtk.options
	options.Bindings[0].ToolName = shell.Name
	options.Bindings[0].MatchInput = &InputMatch{Field: "cmd", Equals: []string{"go test -json ./..."}}

	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "integration.db"))
	if err != nil {
		t.Fatal(err)
	}
	standardMount, err := einotools.MountStandard(context.Background(), registry, integrationComponent("eino-tools-standard"), einotools.Options{
		Scope: extension.GlobalScope(),
		Catalog: catalog.Options{
			SearchOptions: &search.Options{RGBinary: environment["rg"], Env: environmentEntries(environment)},
			ShellOptions:  &shell.Options{ShellBinary: environment["sh"], Env: environmentEntries(environment)},
		},
		Permissions: map[string][]string{catalog.IDShell: {"workspace.execute"}},
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	reducerComponent := integrationComponent("rtk-reducer-shell")
	reducerComponent.Artifact.ConfigHash = ""
	reducerMount, err := Mount(context.Background(), registry, reducerComponent, options)
	if err != nil {
		_ = standardMount.Close(context.Background())
		_ = database.Close()
		t.Fatal(err)
	}
	defer func() {
		for _, mount := range []*composition.Mount{reducerMount, standardMount} {
			mount.Deactivate()
		}
		for _, mount := range []*composition.Mount{reducerMount, standardMount} {
			closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := mount.Close(closeContext); err != nil {
				t.Errorf("close mount: %v", err)
			}
			cancel()
		}
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()

	var nextModelContent string
	streamer := integrationScript(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if message := latestIntegrationToolMessage(request.Messages); message != nil {
			nextModelContent = message.Content
			return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
		}
		return integrationToolCall("standard-shell-call", shell.Name, integrationInput), nil
	})
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(integrationResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithPermissions(allowIntegrationPolicy()),
		runtime.WithIDGenerator(&integrationIDs{}), runtime.WithClock(time.Now),
		runtime.WithOwnerID("rtk-standard-shell-owner"), runtime.WithQueueSize(16), runtime.WithLease(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := model.Selection{ProviderID: "rtk-integration-provider", ModelID: "rtk-integration-model"}
	handle, err := orchestrator.Start(context.Background(), runtime.Request{
		SessionID: session.ID("rtk-standard-shell-session"), Message: runtime.UserMessage{Content: "run standard shell fixture"},
		Config: config.Snapshot{Agent: config.Agent{Name: "rtk-integration-agent", Model: selection}, Model: selection,
			Metadata: map[string]string{"workspace_id": "rtk-standard-shell-workspace", "workspace_root": workspace}},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunCompleted || result.Error != nil {
			t.Fatalf("standard shell integration result = %+v", result)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("standard shell integration run timed out")
	}

	call, err := database.GetToolCall(context.Background(), "standard-shell-call")
	if err != nil {
		t.Fatal(err)
	}
	var output runtime.ToolOutput
	if err := json.Unmarshal(call.Output, &output); err != nil {
		t.Fatalf("decode durable shell output: %v; raw=%s", err, call.Output)
	}
	countBytes, err := os.ReadFile(filepath.Join(workspace, "execution-count"))
	if err != nil || string(countBytes) != "x" {
		t.Fatalf("standard shell command execution count evidence=%q err=%v; want exactly one", countBytes, err)
	}
	if output.Content == "" || string(output.Structured) != output.Content {
		t.Fatalf("JSON mirror copies differ: content=%q structured=%q", output.Content, output.Structured)
	}
	if nextModelContent == "" {
		t.Fatal("orchestrator did not send a tool result to the next model turn")
	}
	var next runtime.ToolOutput
	if err := json.Unmarshal([]byte(nextModelContent), &next); err != nil {
		t.Fatalf("decode next model shell output: %v; raw=%q", err, nextModelContent)
	}
	if next.Content != output.Content || string(next.Structured) != output.Content {
		t.Fatalf("next model copies differ: next=%q durable=%q", nextModelContent, output.Content)
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output.Content), &result); err != nil {
		t.Fatalf("decode reduced shell result: %v; raw=%q", err, output.Content)
	}
	var stdout string
	if err := json.Unmarshal(result["stdout"], &stdout); err != nil {
		t.Fatalf("decode reduced stdout: %v", err)
	}
	if !strings.Contains(stdout, "[Reduced by RTK go-test;") {
		t.Fatalf("stdout was not reduced by RTK: %q", stdout)
	}
	markerEnd := strings.IndexByte(stdout, ']')
	if markerEnd <= 0 {
		t.Fatalf("reduced stdout marker is malformed: %q", stdout)
	}
	marker := stdout[:markerEnd+1]
	const originalPrefix = "[Reduced by RTK go-test; original_bytes="
	if !strings.HasPrefix(marker, originalPrefix) {
		t.Fatalf("reduced stdout marker is malformed: %q", marker)
	}
	var originalBytes int
	if _, err := fmt.Sscanf(strings.TrimPrefix(strings.TrimSuffix(marker, "]"), originalPrefix), "%d", &originalBytes); err != nil || originalBytes <= len(stdout) {
		t.Fatalf("reduced stdout is not shorter: original=%d reduced=%d marker=%q", originalBytes, len(stdout), marker)
	}

	assertJSONField(t, result, "outcome", `"succeeded"`)
	assertJSONField(t, result, "exit_code", `0`)
	assertJSONField(t, result, "stderr", `""`)
	assertJSONFieldAbsent(t, result, "timed_out")
	assertJSONFieldAbsent(t, result, "stdout_truncated")
	assertJSONFieldAbsent(t, result, "stderr_truncated")
	assertJSONFieldAbsent(t, result, "error")
}

func writeShellGoModule(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	module := "example.com/rtk-shell-fixture/" + filepath.Base(workspace)
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module "+module+"\n\ngo 1.20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	source.WriteString("package fixture\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\n")
	source.WriteString("func TestPass(t *testing.T) {\n")
	source.WriteString("f, err := os.OpenFile(\"execution-count\", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)\n")
	source.WriteString("if err != nil { t.Fatal(err) }\n")
	source.WriteString("if _, err := f.WriteString(\"x\"); err != nil { t.Fatal(err) }\n")
	source.WriteString("if err := f.Close(); err != nil { t.Fatal(err) }\n")
	for i := 0; i < 256; i++ {
		fmt.Fprintf(&source, "t.Logf(\"verbose pass event %03d\")\n", i)
	}
	source.WriteString("}\n")
	if err := os.WriteFile(filepath.Join(workspace, "fixture_test.go"), []byte(source.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func standardShellEnvironment(t *testing.T) map[string]string {
	t.Helper()
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		t.Fatal(err)
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	goDir := filepath.Dir(goPath)
	rgDir := filepath.Dir(rgPath)
	path := goDir + string(os.PathListSeparator) + rgDir + string(os.PathListSeparator) + "/usr/bin:/bin"
	return map[string]string{
		"PATH": path, "HOME": t.TempDir(), "ENV": "", "BASH_ENV": "", "NO_COLOR": "1", "TERM": "dumb",
		"LANG": "C", "LC_ALL": "C", "GOWORK": "off", "GOTELEMETRY": "off", "GOCACHE": t.TempDir(),
		"go": goPath, "rg": rgPath, "sh": shPath,
	}
}

func environmentEntries(values map[string]string) []string {
	keys := []string{"PATH", "HOME", "ENV", "BASH_ENV", "NO_COLOR", "TERM", "LANG", "LC_ALL", "GOWORK", "GOTELEMETRY", "GOCACHE"}
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, key+"="+values[key])
	}
	return entries
}

func assertJSONField(t *testing.T, fields map[string]json.RawMessage, name, want string) {
	t.Helper()
	got, exists := fields[name]
	if !exists || string(got) != want {
		t.Fatalf("%s = %s, want %s", name, got, want)
	}
}

func assertJSONFieldAbsent(t *testing.T, fields map[string]json.RawMessage, name string) {
	t.Helper()
	if _, exists := fields[name]; exists {
		t.Fatalf("%s unexpectedly present: %s", name, fields[name])
	}
}
