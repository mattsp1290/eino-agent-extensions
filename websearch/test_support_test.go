package websearch

import (
	"context"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func testLimits() Limits {
	return Limits{
		MaxQueryBytes: 128, MaxResults: 2, MaxTitleBytes: 16,
		MaxURLBytes: 128, MaxSnippetBytes: 32, MaxInFlight: 2,
		MaxWait: time.Second,
	}
}

func testOptions() Options {
	return Options{
		Searcher: SearcherFunc(func(context.Context, string) ([]Source, error) {
			return []Source{{Title: "title", URL: "https://example.test/", Snippet: "snippet"}}, nil
		}),
		SearcherIdentity: "test-searcher-v1", Limits: testLimits(),
	}
}

func testComponent(name string) extension.Component {
	return extension.Component{
		InstanceID: "fixture-" + name,
		Artifact: extension.Artifact{
			Name: "web-search", Version: "test", Hash: "synthetic-artifact-" + name,
			SourceKind: extension.SourceNative,
		},
	}
}

func canonicalOptionsForTest(t *testing.T) canonicalOptions {
	t.Helper()
	options, err := canonicalize(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	return options
}

func mountTestRegistry(t *testing.T, options Options) (*composition.Registry, *composition.Mount) {
	t.Helper()
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	mount, err := Mount(context.Background(), registry, testComponent("mount"), options)
	if err != nil {
		t.Fatal(err)
	}
	return registry, mount
}

func resolveTestTool(t *testing.T, registry *composition.Registry, sessionID string) (*runtime.RunPlan, runtime.Tool) {
	t.Helper()
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: session.ID(sessionID)})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := plan.ResolveTools(context.Background(), runtime.ToolScopeContext{SessionID: session.ID(sessionID)})
	if err != nil || len(resolved) != 1 {
		plan.Release()
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	return plan, resolved[0]
}

func closeTestMount(t *testing.T, mount *composition.Mount) {
	t.Helper()
	if mount == nil {
		return
	}
	mount.Deactivate()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := mount.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
