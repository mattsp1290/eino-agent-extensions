//go:build darwin

package pythonrepl

import (
	"errors"
	"syscall"
)

// Darwin reports EPERM when a process group contains only an unreaped zombie
// session leader. The supervisor still owns that child and reaps it only after
// this final signal attempt.
func processGroupGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM)
}
