package backgroundjobs

import (
	"context"
	"errors"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
)

// Mount validates and freezes options, creates one private manager, and
// atomically registers the four background-job tools and manager cleanup.
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
		manager, err := newManager(canonical)
		if err != nil {
			return err
		}
		if err := registrar.Defer(manager.Close); err != nil {
			return errors.Join(err, manager.Close(context.Background()))
		}
		registrations := []string{startRegistrationID, statusRegistrationID, listRegistrationID, killRegistrationID}
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
