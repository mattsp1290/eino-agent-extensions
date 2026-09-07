// Command workspace-instructions demonstrates a credential-free trusted
// workspace resolver through a real frozen Eino composition plan.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mattsp1290/eino-agent-extensions/workspaceinstructions"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
)

func main() {
	section, err := renderSyntheticInstructions(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(section)
}

func renderSyntheticInstructions(ctx context.Context) (section string, err error) {
	boundary, err := os.MkdirTemp("", "workspace-instructions-example-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(boundary)
	root := filepath.Join(boundary, "project")
	if err := os.Mkdir(root, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(boundary, "AGENTS.md"), []byte("Apply the outer synthetic policy."), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Apply the project synthetic policy."), 0o600); err != nil {
		return "", err
	}

	registry, err := composition.NewRegistry(nil)
	if err != nil {
		return "", err
	}
	mount, err := workspaceinstructions.Mount(ctx, registry, extension.Component{
		InstanceID: "example-workspace-instructions",
		Artifact: extension.Artifact{
			Name: "workspace-instructions", Version: "example", Hash: "host-supplied-artifact-hash",
			SourceKind: extension.SourceNative,
		},
	}, workspaceinstructions.Options{
		Resolver: workspaceinstructions.ResolverFunc(func(context.Context, workspaceinstructions.Request) (workspaceinstructions.Workspace, error) {
			return workspaceinstructions.Workspace{Root: root, Boundary: boundary, Trusted: true}, nil
		}),
		ResolverIdentity: "example-fixed-workspace-v1",
		Limits: workspaceinstructions.Limits{
			MaxFileNames: 1, MaxChainDepth: 4, MaxFileBytes: 4 << 10,
			MaxSectionBytes: 16 << 10, MaxInFlight: 2, MaxWait: time.Second,
		},
	})
	if err != nil {
		return "", err
	}
	defer func() {
		mount.Deactivate()
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = errors.Join(err, mount.Close(closeCtx))
	}()
	plan, err := registry.AcquireRunPlan(ctx, runtime.RunPlanRequest{SessionID: "example-session"})
	if err != nil {
		return "", err
	}
	defer plan.Release()
	prompts := plan.Prompts()
	if len(prompts) != 1 || prompts[0].Name != workspaceinstructions.PromptName {
		return "", fmt.Errorf("workspace instruction prompt unavailable")
	}
	return prompts[0].Provider.ProvidePrompt(ctx, runtime.PromptContext{SessionID: "example-session"})
}
