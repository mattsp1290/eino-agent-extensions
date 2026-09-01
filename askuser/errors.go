package askuser

import (
	"errors"
	"fmt"

	"github.com/mattsp1290/eino-agent/tools"
)

var errResponderOperation = errors.New("ask user responder failed: code=operation")

func configError(code string) error {
	return fmt.Errorf("ask user configuration invalid: code=%s", code)
}

func mountError(code string) error {
	return fmt.Errorf("ask user mount invalid: code=%s", code)
}

func runtimeError(code string) error {
	return fmt.Errorf("ask user runtime invalid: code=%s", code)
}

func malformed(code string) error {
	return errors.Join(tools.ErrMalformedInput, fmt.Errorf("ask user input invalid: code=%s", code))
}
