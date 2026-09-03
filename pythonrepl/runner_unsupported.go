//go:build !linux && !darwin

package pythonrepl

import "context"

type runnerProcess struct{}

func startRunner(context.Context, canonicalOptions, string, string) (*runnerProcess, error) {
	return nil, operationError("unsupported-platform")
}

func (*runnerProcess) execute(context.Context, string) (executionOutcome, error) {
	return executionOutcome{}, operationError("unsupported-platform")
}

func (*runnerProcess) terminate() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func (*runnerProcess) terminationError() error { return operationError("unsupported-platform") }
