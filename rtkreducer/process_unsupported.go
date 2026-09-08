//go:build !linux && !darwin

package rtkreducer

import (
	"errors"
	"os/exec"
)

func configureProcess(_ *exec.Cmd) error { return errors.New("unsupported platform") }
