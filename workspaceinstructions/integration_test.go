package workspaceinstructions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func trustedOptions(root, boundary string) Options {
	options := testOptions()
	options.ResolverIdentity = "integration-resolver-v1"
	options.Resolver = ResolverFunc(func(context.Context, Request) (Workspace, error) {
		return Workspace{Root: root, Boundary: boundary, Trusted: true}, nil
	})
	return options
}

func TestIntegrationTrustedChainRereadsAndPersistsEveryModelRequest(t *testing.T) {
	boundary := t.TempDir()
	middle := filepath.Join(boundary, "middle")
	root := filepath.Join(middle, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtures := []struct{ directory, content string }{{boundary, "outer-body"}, {middle, "middle-body"}, {root, "original-root-body"}}
	for _, fixture := range fixtures {
		if err := os.WriteFile(filepath.Join(fixture.directory, "AGENTS.md"), []byte(fixture.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	harness := newIntegrationHarness(t)
	options := trustedOptions(root, boundary)
	var resolverRequests []Request
	options.Resolver = ResolverFunc(func(_ context.Context, request Request) (Workspace, error) {
		resolverRequests = append(resolverRequests, request)
		return Workspace{Root: root, Boundary: boundary, Trusted: true}, nil
	})
	harness.mountInstructions("trusted", options)
	harness.mountTouch(filepath.Join(root, "AGENTS.md"), "rewritten-root-body")
	var systems []string
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		systems = append(systems, request.System)
		for _, message := range request.Messages {
			if message.Role == einoschema.Tool {
				return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
			}
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID: "touch-call", Type: "function", Function: einoschema.FunctionCall{Name: "instructions_touch", Arguments: `{}`},
		}})}, nil
	})
	result := harness.run("trusted-session", streamer)
	if result.Status != session.RunCompleted || result.Error != nil || len(systems) != 2 {
		t.Fatalf("result = %#v, systems = %#v", result, systems)
	}
	if len(resolverRequests) != 2 {
		t.Fatalf("resolver requests = %#v", resolverRequests)
	}
	for index, request := range resolverRequests {
		if request.SessionID != "trusted-session" || request.RunID != result.RunID || request.EpochID == "" || request.Attempt != 1 || request.Step != index+1 || request.AgentName != "workspace-integration-agent" || request.ProviderID != "workspace-integration-provider" || request.ModelID != "workspace-integration-model" {
			t.Fatalf("resolver request[%d] = %#v", index, request)
		}
	}
	first := systems[0]
	positions := []int{strings.Index(first, `path="../../AGENTS.md"`), strings.Index(first, `path="../AGENTS.md"`), strings.Index(first, `path="./AGENTS.md"`)}
	if !strings.HasPrefix(first, integrationBasePrompt+"\n\n") || positions[0] < 0 || positions[1] <= positions[0] || positions[2] <= positions[1] || !strings.Contains(first, "original-root-body") {
		t.Fatalf("first system = %q", first)
	}
	if !strings.Contains(systems[1], "rewritten-root-body") || strings.Contains(systems[1], "original-root-body") {
		t.Fatalf("second system = %q", systems[1])
	}
	batch, err := harness.database.ListModelRequests(context.Background(), result.RunID, session.ModelRequestCursor{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Records) != 2 || !strings.Contains(batch.Records[0].System, "original-root-body") || !strings.Contains(batch.Records[1].System, "rewritten-root-body") {
		t.Fatalf("durable records = %#v", batch.Records)
	}
}

func TestIntegrationEmptyWorkspaceOutcomesCompleteWithoutSection(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name      string
		workspace Workspace
	}{
		{"untrusted", Workspace{Root: "/private/untrusted", Trusted: false}},
		{"no-workspace", Workspace{Root: "", Trusted: true}},
		{"missing-file", Workspace{Root: root, Trusted: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newIntegrationHarness(t)
			options := testOptions()
			options.Resolver = ResolverFunc(func(context.Context, Request) (Workspace, error) { return test.workspace, nil })
			harness.mountInstructions(test.name, options)
			var systems []string
			result := harness.run(session.ID(test.name), successfulStreamer(&systems))
			if result.Status != session.RunCompleted || result.Error != nil || len(systems) != 1 || systems[0] != integrationBasePrompt {
				t.Fatalf("result = %#v, systems = %#v", result, systems)
			}
		})
	}
}

func TestIntegrationTruncatesInstructionFile(t *testing.T) {
	root := t.TempDir()
	options := trustedOptions(root, "")
	options.Limits.MaxFileBytes = 8
	options.Limits.MaxSectionBytes = 2048
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	harness := newIntegrationHarness(t)
	harness.mountInstructions("truncate", options)
	var systems []string
	result := harness.run("truncate-session", successfulStreamer(&systems))
	if result.Status != session.RunCompleted || len(systems) != 1 || !strings.Contains(systems[0], `truncated="true"`) || !strings.Contains(systems[0], truncatedMarker) {
		t.Fatalf("result = %#v, systems = %#v", result, systems)
	}
}

func TestIntegrationResolverFaultAndDeadlineFailWithoutRetry(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Options)
	}{
		{"resolver", func(options *Options) {
			options.Resolver = ResolverFunc(func(context.Context, Request) (Workspace, error) {
				return Workspace{}, errors.New("private fixture /path")
			})
		}},
		{"deadline", func(options *Options) {
			options.Limits.MaxWait = 10 * time.Millisecond
			options.Resolver = ResolverFunc(func(ctx context.Context, _ Request) (Workspace, error) {
				<-ctx.Done()
				return Workspace{}, ctx.Err()
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newIntegrationHarness(t)
			options := testOptions()
			test.configure(&options)
			resolverCalls := 0
			resolver := options.Resolver
			options.Resolver = ResolverFunc(func(ctx context.Context, request Request) (Workspace, error) {
				resolverCalls++
				return resolver.ResolveWorkspace(ctx, request)
			})
			harness.mountInstructions(test.name, options)
			var calls int
			result := harness.run(session.ID(test.name+"-session"), integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
				calls++
				return []*einoschema.Message{einoschema.AssistantMessage("unexpected", nil)}, nil
			}))
			if result.Status == session.RunCompleted || result.Error == nil || !strings.Contains(result.Error.Error(), "code="+test.name) || strings.Contains(result.Error.Error(), "private") || calls != 0 || resolverCalls != 1 {
				t.Fatalf("result = %#v, model calls = %d, resolver calls = %d", result, calls, resolverCalls)
			}
		})
	}
}

func TestIntegrationSessionMountShadowsGlobalOnlyForItsSession(t *testing.T) {
	globalRoot := t.TempDir()
	sessionRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(globalRoot, "AGENTS.md"), []byte("global-body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, "AGENTS.md"), []byte("session-body"), 0o600); err != nil {
		t.Fatal(err)
	}
	harness := newIntegrationHarness(t)
	harness.mountInstructions("global", trustedOptions(globalRoot, ""))
	sessionOptions := trustedOptions(sessionRoot, "")
	sessionOptions.Scope = extension.SessionScope("shadowed")
	harness.mountInstructions("session", sessionOptions)
	var shadowedSystems, otherSystems []string
	shadowed := harness.run("shadowed", successfulStreamer(&shadowedSystems))
	other := harness.run("other", successfulStreamer(&otherSystems))
	if shadowed.Status != session.RunCompleted || other.Status != session.RunCompleted || len(shadowedSystems) != 1 || len(otherSystems) != 1 || !strings.Contains(shadowedSystems[0], "session-body") || strings.Contains(shadowedSystems[0], "global-body") || !strings.Contains(otherSystems[0], "global-body") || strings.Contains(otherSystems[0], "session-body") {
		t.Fatalf("shadowed=%#v other=%#v systems=%#v / %#v", shadowed, other, shadowedSystems, otherSystems)
	}
}

func TestIntegrationBlockedReadReturnsDeadlineAndRetainsCloseSlot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	reader := func(context.Context, *os.Root, string, int) (candidateRead, error) {
		close(entered)
		<-release
		return candidateRead{data: []byte("late")}, nil
	}
	harness := newIntegrationHarness(t)
	options := trustedOptions(root, "")
	options.Limits.MaxWait = 10 * time.Millisecond
	mount, err := mountWithReader(context.Background(), harness.registry, testComponent("blocked-read"), options, reader)
	if err != nil {
		t.Fatal(err)
	}
	harness.mounts = append(harness.mounts, mount)
	resultDone := make(chan runtime.Result, 1)
	go func() {
		resultDone <- harness.run("blocked-read-session", successfulStreamer(&[]string{}))
	}()
	<-entered
	result := <-resultDone
	if result.Error == nil || !strings.Contains(result.Error.Error(), "code=deadline") {
		t.Fatalf("result = %#v", result)
	}
	mount.Deactivate()
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = mount.Close(closeCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close while blocked = %v", err)
	}
	close(release)
	if err := mount.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for index, candidate := range harness.mounts {
		if candidate == mount {
			harness.mounts = append(harness.mounts[:index], harness.mounts[index+1:]...)
			break
		}
	}
}
