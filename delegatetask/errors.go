package delegatetask

import (
	"errors"
	"fmt"

	"github.com/mattsp1290/eino-agent/tools"
)

var errRunnerOperation = errors.New("delegate task runner failed: code=operation")

func configError(code string) error {
	return fmt.Errorf("delegate task configuration invalid: code=%s", code)
}

func mountError(code string) error {
	return fmt.Errorf("delegate task mount invalid: code=%s", code)
}

func runtimeError(code string) error {
	return fmt.Errorf("delegate task runtime invalid: code=%s", code)
}

func malformed(code string) error {
	return errors.Join(tools.ErrMalformedInput, fmt.Errorf("delegate task input invalid: code=%s", code))
}
