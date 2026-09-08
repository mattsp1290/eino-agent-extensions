package commandguard

import (
	"context"
	"errors"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
)

const registrationID = "command-policy/guard"

// Mount freezes the policy and atomically installs one native deny-only guard.
// Deactivation and lease-draining Close are owned by the returned Eino mount.
func Mount(ctx context.Context, registry *composition.Registry, component extension.Component, options Options) (*composition.Mount, error) {
	return mountWithParser(ctx, registry, component, options, parseScript)
}

// The private parser dependency permits deterministic failure/cancellation tests
// through the same validation, registry, and runtime path as production Mount.
func mountWithParser(ctx context.Context, registry *composition.Registry, component extension.Component, options Options, parse parseFunc) (*composition.Mount, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if registry == nil {
		return nil, errors.New("command policy mount invalid: registry-required")
	}
	p, err := canonicalize(options)
	if err != nil {
		return nil, err
	}
	if component.Artifact.SourceKind != extension.SourceNative {
		return nil, errors.New("command policy mount invalid: native-component-required")
	}
	hash := configHash(p)
	if component.Artifact.ConfigHash == "" {
		component.Artifact.ConfigHash = hash
	} else if component.Artifact.ConfigHash != hash {
		return nil, errors.New("command policy mount invalid: config-hash-mismatch")
	}
	if extension.ValidateComponent(component) != nil {
		return nil, errors.New("command policy mount invalid: component-identity")
	}
	g := newGuard(p)
	g.parse = parse
	return registry.Mount(ctx, component, composition.InstallerFunc(func(_ context.Context, r *composition.Registrar) error {
		return r.Guard(composition.GuardRegistration{ID: registrationID, Order: p.order, Scope: p.scope, Guard: g})
	}))
}
