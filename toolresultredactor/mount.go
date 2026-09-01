package toolresultredactor

import (
	"context"
	"errors"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
)

const registrationID = "tool-result/secret-redactor"

// Mount validates and freezes options, derives the component configuration
// identity, and atomically registers one tool-result transform.
func Mount(ctx context.Context, registry *composition.Registry, component extension.Component, options Options) (*composition.Mount, error) {
	if registry == nil {
		return nil, errors.New("tool result redactor mount invalid: code=registry-required")
	}
	canonical, err := canonicalize(options)
	if err != nil {
		return nil, err
	}
	policy, err := compileCanonicalPolicy(canonical)
	if err != nil {
		return nil, err
	}
	hash, err := configHash(canonical)
	if err != nil {
		return nil, err
	}
	if component.Artifact.SourceKind != extension.SourceNative {
		return nil, errors.New("tool result redactor mount invalid: code=native-component-required")
	}
	if component.Artifact.ConfigHash == "" {
		component.Artifact.ConfigHash = hash
	} else if component.Artifact.ConfigHash != hash {
		return nil, errors.New("tool result redactor mount invalid: code=config-hash-mismatch")
	}
	if err := extension.ValidateComponent(component); err != nil {
		return nil, errors.New("tool result redactor mount invalid: code=component-identity")
	}

	return registry.Mount(ctx, component, composition.InstallerFunc(func(_ context.Context, registrar *composition.Registrar) error {
		return registerPolicy(registrar.Extensions(), canonical, policy)
	}))
}

func registerPolicy(registrar extension.Registrar, canonical canonicalOptions, policy *compiledPolicy) error {
	return extension.OnTransform(registrar, runtime.ToolResultTransformPoint, extension.Registration{
		ID: registrationID, Order: canonical.order, Scope: canonical.scope,
	}, func(ctx context.Context, input runtime.ToolResultTransform) (runtime.ToolResultTransform, error) {
		if _, excluded := policy.excluded[input.ToolName]; excluded {
			return input, nil
		}
		input.Result = policy.redact(ctx, input.Result)
		return input, nil
	})
}
