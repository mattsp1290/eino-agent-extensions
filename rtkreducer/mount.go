package rtkreducer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
)

// Mount validates and freezes one trusted native reducer policy, verifies its
// supplied executable digest, and atomically registers a tool-result
// transform. It performs no child-process execution or directory creation.
func Mount(ctx context.Context, registry *composition.Registry, component extension.Component, options Options) (*composition.Mount, error) {
	if registry == nil {
		return nil, mountError("registry-required")
	}
	canonical, err := canonicalize(options)
	if err != nil {
		return nil, err
	}
	if !platformSupported() {
		return nil, mountError("unsupported-platform")
	}
	if err := validateTempRoot(canonical.tempRoot); err != nil {
		return nil, err
	}
	resolvedExecutable, err := verifyExecutable(ctx, canonical.executablePath, canonical.executableSHA256)
	if err != nil {
		return nil, err
	}
	canonical.executablePath = resolvedExecutable
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
			return err
		}
		transformer := newTransformer(canonical, func(ctx context.Context) (reduceFieldFunc, func(), bool) {
			lease, ok := coordinator.acquire(ctx)
			if !ok {
				return nil, nil, false
			}
			return lease.reduce, lease.release, true
		})
		return registerTransform(registrar.Extensions(), canonical, transformer)
	}))
}

func registerTransform(registrar extension.Registrar, canonical canonicalOptions, transformer *transformer) error {
	return extension.OnTransform(registrar, runtime.ToolResultTransformPoint, extension.Registration{
		ID: registrationID, Order: canonical.order, Scope: canonical.scope,
	}, transformer.transform)
}

func validateTempRoot(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != filepath.Clean(path) {
		return mountError("temp-root")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 || !platformPathControlled(info) {
		return mountError("temp-root")
	}
	return nil
}

func verifyExecutable(ctx context.Context, path, expected string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", mountError("executable-path")
	}
	resolved, err = filepath.Abs(filepath.Clean(resolved))
	if err != nil || len(resolved) > maxPathBytes {
		return "", mountError("executable-path")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxExecutableBytes {
		return "", mountError("executable-file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", mountError("executable-permission")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", mountError("executable-read")
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := copyContext(ctx, hash, file, maxExecutableBytes); err != nil {
		return "", mountError("executable-read")
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return "", mountError("executable-digest-mismatch")
	}
	return resolved, nil
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader, max int64) (int64, error) {
	if max <= 0 {
		return 0, errors.New("invalid copy bound")
	}
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		remaining := max - total
		readSize := int64(len(buffer))
		if remaining < readSize-1 {
			readSize = remaining + 1
		}
		n, readErr := src.Read(buffer[:readSize])
		if n > 0 {
			if int64(n) > remaining {
				return total, mountError("executable-too-large")
			}
			if _, err := dst.Write(buffer[:n]); err != nil {
				return total, err
			}
			total += int64(n)
			if total > max {
				return total, mountError("executable-too-large")
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
