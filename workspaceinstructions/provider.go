package workspaceinstructions

import (
	"context"

	"github.com/mattsp1290/eino-agent/runtime"
)

type provider struct {
	options     canonicalOptions
	coordinator *coordinator
	reader      candidateReader
}

func newProvider(options canonicalOptions, coordinator *coordinator) runtime.PromptProvider {
	return newProviderWithReader(options, coordinator, defaultReadCandidate)
}

func newProviderWithReader(options canonicalOptions, coordinator *coordinator, reader candidateReader) runtime.PromptProvider {
	return &provider{options: options, coordinator: coordinator, reader: reader}
}

func (p *provider) ProvidePrompt(ctx context.Context, prompt runtime.PromptContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	request := requestFrom(prompt)
	return p.coordinator.run(ctx, func(workCtx context.Context) (string, error) {
		workspace, err := callResolver(workCtx, p.options.resolver, request)
		if err != nil {
			return "", err
		}
		if !workspace.Trusted || workspace.Root == "" {
			return "", nil
		}
		canonical, err := resolveWorkspace(workspace, p.options.limits)
		if err != nil {
			return "", err
		}
		return renderWorkspaceWithReader(workCtx, canonical, p.options.fileNames, p.options.limits, p.reader)
	})
}

func requestFrom(prompt runtime.PromptContext) Request {
	return Request{
		SessionID: prompt.SessionID, RunID: prompt.RunID, EpochID: prompt.EpochID,
		Attempt: prompt.Attempt, Step: prompt.Step, AgentName: prompt.AgentName,
		ProviderID: prompt.ProviderID, ModelID: prompt.ModelID,
	}
}

func callResolver(ctx context.Context, resolver Resolver, request Request) (workspace Workspace, err error) {
	defer func() {
		if recover() != nil {
			workspace = Workspace{}
			err = providerError("resolver")
		}
	}()
	workspace, err = resolver.ResolveWorkspace(ctx, request)
	if err != nil {
		return Workspace{}, providerError("resolver")
	}
	return workspace, nil
}
