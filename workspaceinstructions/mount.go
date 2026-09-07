package workspaceinstructions

import (
	"context"
	"errors"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
)

// Mount validates and freezes options, constructs one private coordinator,
// and atomically registers exactly one workspace instruction prompt section.
func Mount(ctx context.Context, registry *composition.Registry, component extension.Component, options Options) (*composition.Mount, error) {
	return mountWithReader(ctx, registry, component, options, defaultReadCandidate)
}

func mountWithReader(ctx context.Context, registry *composition.Registry, component extension.Component, options Options, reader candidateReader) (*composition.Mount, error) {
	if registry == nil {
		return nil, mountError("registry-required")
	}
	canonical, err := canonicalize(options)
	if err != nil {
		return nil, err
	}
	hash, err := configHash(canonical)
	if err != nil {
		return nil, err
	}
	if component.Artifact.SourceKind != extension.SourceNative {
		return nil, mountError("native-component-required")
	}
	if component.Artifact.ConfigHash == "" {
		component.Artifact.ConfigHash = hash
	} else if component.Artifact.ConfigHash != hash {
		return nil, mountError("config-hash-mismatch")
	}
	if err := extension.ValidateComponent(component); err != nil {
		return nil, mountError("component-identity")
	}
	return registry.Mount(ctx, component, composition.InstallerFunc(func(_ context.Context, registrar *composition.Registrar) error {
		coordinator := newCoordinator(canonical)
		if err := registrar.Defer(coordinator.Close); err != nil {
			return errors.Join(err, coordinator.Close(context.Background()))
		}
		return registrar.Prompt(composition.PromptRegistration{
			ID: registrationID, Name: PromptName, Order: canonical.order,
			Scope: canonical.scope, Provider: newProviderWithReader(canonical, coordinator, reader),
		})
	}))
}
