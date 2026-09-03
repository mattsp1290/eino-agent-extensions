//go:build linux || darwin

package pythonrepl

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func createVenv(ctx context.Context, options canonicalOptions) (*virtualEnvironment, error) {
	path, err := os.MkdirTemp(options.tempRoot, ".pythonrepl-")
	if err != nil {
		return nil, operationError("venv-create")
	}
	venv := &virtualEnvironment{path: path, interpreter: filepath.Join(path, "bin", "python")}
	if options.hooks != nil {
		venv.remove = options.hooks.removeVenv
	}
	cleanup := func(result error) (*virtualEnvironment, error) {
		if cleanupErr := removeVenv(venv); cleanupErr != nil {
			return venv, errors.Join(result, cleanupErr)
		}
		return nil, result
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return cleanup(operationError("venv-directory"))
	}
	if options.hooks != nil {
		runContextHook(options.hooks.afterVenvDirectory, ctx)
	}
	if err := ctx.Err(); err != nil {
		return cleanup(err)
	}
	cmd := exec.Command(options.pythonPath, "-m", "venv", "--without-pip", path)
	cmd.Env = append([]string(nil), options.env...)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return cleanup(operationError("venv-start"))
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			return cleanup(operationError("venv-failed"))
		}
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(options.limits.TerminateGrace)
		select {
		case <-waitDone:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			killTimer := time.NewTimer(options.limits.KillWait)
			select {
			case <-waitDone:
				if !killTimer.Stop() {
					<-killTimer.C
				}
			case <-killTimer.C:
				return cleanup(errors.Join(ctx.Err(), errCleanupIncomplete))
			}
		}
		return cleanup(ctx.Err())
	}
	rootRelative, err := filepath.Rel(options.tempRoot, path)
	if err != nil || rootRelative == "." || rootRelative == ".." || strings.HasPrefix(rootRelative, ".."+string(filepath.Separator)) || filepath.IsAbs(rootRelative) {
		return cleanup(operationError("venv-directory"))
	}
	info, err = os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return cleanup(operationError("venv-directory"))
	}
	interpreterInfo, err := os.Stat(venv.interpreter)
	if err != nil || !interpreterInfo.Mode().IsRegular() || interpreterInfo.Mode().Perm()&0o111 == 0 {
		return cleanup(operationError("venv-integrity"))
	}
	return venv, nil
}
