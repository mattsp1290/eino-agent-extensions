package workspaceinstructions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/runtime"
)

func newTestProvider(t *testing.T, options Options) runtime.PromptProvider {
	t.Helper()
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	return newProvider(canonical, newCoordinator(canonical))
}

func TestProviderMapsRequestAndRereadsFiles(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(file, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	var observed Request
	options.Resolver = ResolverFunc(func(_ context.Context, request Request) (Workspace, error) {
		observed = request
		return Workspace{Root: root, Trusted: true}, nil
	})
	prompt := runtime.PromptContext{
		SessionID: "session", RunID: "run", EpochID: "epoch", Attempt: 2, Step: 3,
		AgentName: "agent", ProviderID: "provider", ModelID: "model",
	}
	provider := newTestProvider(t, options)
	first, err := provider.ProvidePrompt(context.Background(), prompt)
	if err != nil || !strings.Contains(first, "first") || observed.SessionID != "session" || observed.RunID != "run" || observed.EpochID != "epoch" || observed.Attempt != 2 || observed.Step != 3 || observed.AgentName != "agent" || observed.ProviderID != "provider" || observed.ModelID != "model" {
		t.Fatalf("first = %q, request = %#v, err = %v", first, observed, err)
	}
	if err := os.WriteFile(file, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := provider.ProvidePrompt(context.Background(), prompt)
	if err != nil || !strings.Contains(second, "second") || strings.Contains(second, "first") {
		t.Fatalf("second = %q, err = %v", second, err)
	}
}

func TestProviderEmptyOutcomes(t *testing.T) {
	for name, workspace := range map[string]Workspace{
		"untrusted": {Root: "/private/path", Trusted: false}, "empty": {Trusted: true},
	} {
		t.Run(name, func(t *testing.T) {
			options := testOptions()
			options.Resolver = ResolverFunc(func(context.Context, Request) (Workspace, error) { return workspace, nil })
			section, err := newTestProvider(t, options).ProvidePrompt(context.Background(), runtime.PromptContext{})
			if err != nil || section != "" {
				t.Fatalf("section = %q, err = %v", section, err)
			}
		})
	}
}

func TestProviderSanitizesResolverFaults(t *testing.T) {
	tests := map[string]Resolver{
		"error": ResolverFunc(func(context.Context, Request) (Workspace, error) {
			return Workspace{}, errors.New("private host path /secret")
		}),
		"wrapped-canceled": ResolverFunc(func(context.Context, Request) (Workspace, error) {
			return Workspace{}, errors.Join(context.Canceled, errors.New("private"))
		}),
		"wrapped-deadline": ResolverFunc(func(context.Context, Request) (Workspace, error) {
			return Workspace{}, errors.Join(context.DeadlineExceeded, errors.New("private"))
		}),
		"panic": ResolverFunc(func(context.Context, Request) (Workspace, error) { panic("private panic") }),
	}
	for name, resolver := range tests {
		t.Run(name, func(t *testing.T) {
			options := testOptions()
			options.Resolver = resolver
			_, err := newTestProvider(t, options).ProvidePrompt(context.Background(), runtime.PromptContext{})
			if err == nil || err.Error() != providerError("resolver").Error() || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "private") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProviderWorkspaceErrorsAreSanitized(t *testing.T) {
	options := testOptions()
	options.Resolver = ResolverFunc(func(context.Context, Request) (Workspace, error) {
		return Workspace{Root: "/definitely/private/missing", Trusted: true}, nil
	})
	_, err := newTestProvider(t, options).ProvidePrompt(context.Background(), runtime.PromptContext{})
	if err == nil || err.Error() != workspaceError("root").Error() || strings.Contains(err.Error(), "private") {
		t.Fatalf("error = %v", err)
	}
}

func TestProviderDeadlineAndParentCancellation(t *testing.T) {
	options := testOptions()
	options.Limits.MaxWait = 10 * time.Millisecond
	options.Resolver = ResolverFunc(func(ctx context.Context, _ Request) (Workspace, error) {
		<-ctx.Done()
		return Workspace{}, ctx.Err()
	})
	provider := newTestProvider(t, options)
	if _, err := provider.ProvidePrompt(context.Background(), runtime.PromptContext{}); err == nil || err.Error() != providerError("deadline").Error() {
		t.Fatalf("deadline error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.ProvidePrompt(ctx, runtime.PromptContext{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestProviderRecoversInternalDiscoveryPanic(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := readCandidate
	readCandidate = func(context.Context, *os.Root, string, int) ([]byte, error) { panic("private internal panic") }
	t.Cleanup(func() { readCandidate = original })
	options := testOptions()
	options.Resolver = ResolverFunc(func(context.Context, Request) (Workspace, error) { return Workspace{Root: root, Trusted: true}, nil })
	_, err := newTestProvider(t, options).ProvidePrompt(context.Background(), runtime.PromptContext{})
	if err == nil || err.Error() != providerError("internal").Error() || strings.Contains(err.Error(), "private") {
		t.Fatalf("error = %v", err)
	}
}
