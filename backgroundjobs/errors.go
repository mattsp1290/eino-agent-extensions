package backgroundjobs

import (
	"errors"
	"fmt"
)

var (
	errJobNotFound           = errors.New("background jobs operation failed: code=job-not-found")
	errManagerClosing        = errors.New("background jobs operation failed: code=manager-closing")
	errCapacityExhausted     = errors.New("background jobs operation failed: code=capacity-exhausted")
	errTerminationIncomplete = errors.New("background jobs operation failed: code=termination-incomplete")
)

func configError(code string) error {
	return fmt.Errorf("background jobs configuration invalid: code=%s", code)
}

func mountError(code string) error {
	return fmt.Errorf("background jobs mount invalid: code=%s", code)
}

func runtimeError(code string) error {
	return fmt.Errorf("background jobs runtime invalid: code=%s", code)
}

func operationError(code string) error {
	return fmt.Errorf("background jobs operation failed: code=%s", code)
}
