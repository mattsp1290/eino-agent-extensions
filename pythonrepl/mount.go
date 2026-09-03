package pythonrepl

import (
	"context"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
)

// Mount validates and freezes options and atomically registers the execute and
// clear tools plus manager cleanup. It creates no virtual environment or Python
// process; those resources are provisioned lazily by the first execution.
func Mount(ctx context.Context, registry *composition.Registry, component extension.Component, options Options) (*composition.Mount, error) {
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
		manager := newManager(canonical)
		if err := registrar.Defer(manager.Close); err != nil {
			return err
		}
		registrations := []string{executeRegistrationID, clearRegistrationID}
		for index, definition := range definitions(canonical, manager) {
			if err := registrar.Tool(composition.ToolRegistration{
				ID: registrations[index], Order: canonical.order, Scope: canonical.scope, Definition: definition,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
}
