package workspaceinstructions

import (
	"context"
	"reflect"
	"testing"

	"github.com/mattsp1290/eino-agent/runtime"
)

func TestResolverFuncAndRequestMapping(t *testing.T) {
	prompt := runtime.PromptContext{
		SessionID: "session", RunID: "run", EpochID: "epoch", Attempt: 2, Step: 3,
		AgentName: "agent", ProviderID: "provider", ModelID: "model",
	}
	want := Request{
		SessionID: "session", RunID: "run", EpochID: "epoch", Attempt: 2, Step: 3,
		AgentName: "agent", ProviderID: "provider", ModelID: "model",
	}
	var observed Request
	var resolver Resolver = ResolverFunc(func(_ context.Context, request Request) (Workspace, error) {
		observed = request
		return Workspace{}, nil
	})
	if _, err := resolver.ResolveWorkspace(context.Background(), requestFrom(prompt)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("request = %#v, want %#v", observed, want)
	}
}
