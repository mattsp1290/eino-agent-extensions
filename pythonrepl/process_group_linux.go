//go:build linux

package pythonrepl

import (
	"errors"
	"syscall"
)

func processGroupGone(err error) bool { return errors.Is(err, syscall.ESRCH) }
