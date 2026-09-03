package pythonrepl

import (
	"errors"
	"fmt"
)

var (
	errManagerClosing     = errors.New("python repl operation failed: code=manager-closing")
	errCapacityExhausted  = errors.New("python repl operation failed: code=capacity-exhausted")
	errQueueFull          = errors.New("python repl operation failed: code=queue-full")
	errCleanupIncomplete  = errors.New("python repl operation failed: code=cleanup-incomplete")
	errOwnerQuarantined   = errors.New("python repl operation failed: code=owner-quarantined")
	errExecutionTimedOut  = errors.New("python repl operation failed: code=timed-out")
	errBootstrapIntegrity = errors.New("python repl operation failed: code=bootstrap-integrity")
)

func configError(code string) error {
	return fmt.Errorf("python repl configuration invalid: code=%s", code)
}

func mountError(code string) error {
	return fmt.Errorf("python repl mount invalid: code=%s", code)
}

func runtimeError(code string) error {
	return fmt.Errorf("python repl runtime invalid: code=%s", code)
}

func operationError(code string) error {
	return fmt.Errorf("python repl operation failed: code=%s", code)
}
