//go:build linux || darwin

package rtkreducer

import (
	"os/exec"
)

// configureProcess intentionally does not set a process group. The reducer
// owns the direct child only; supervising arbitrary descendants is outside its
// contract.
func configureProcess(_ *exec.Cmd) error { return nil }
