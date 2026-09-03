//go:build !linux && !darwin

package pythonrepl

import "context"

func createVenv(context.Context, canonicalOptions) (*virtualEnvironment, error) {
	return nil, operationError("unsupported-platform")
}
