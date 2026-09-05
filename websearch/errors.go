package websearch

import (
	"errors"
	"fmt"

	"github.com/mattsp1290/eino-agent/tools"
)

var (
	errSearchOperation = errors.New("web search unavailable: code=operation")
	errSearchCapacity  = errors.New("web search unavailable: code=capacity")
)

func configError(code string) error {
	return fmt.Errorf("web search configuration invalid: code=%s", code)
}

func mountError(code string) error {
	return fmt.Errorf("web search mount invalid: code=%s", code)
}

func runtimeError(code string) error {
	return fmt.Errorf("web search runtime invalid: code=%s", code)
}

func malformed(code string) error {
	return errors.Join(tools.ErrMalformedInput, fmt.Errorf("web search input invalid: code=%s", code))
}
