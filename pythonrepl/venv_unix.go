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
	"sync"
	"syscall"
	"time"
)

type venvCreationStatus struct {
	Succeeded bool `json:"succeeded"`
}

// venvCreation deliberately does not call Wait until after the final group
// signal. The unreaped group leader anchors its PGID so a package-owned child
// cannot survive cleanup and a recycled PGID can never be signaled.
type venvCreation struct {
	options      canonicalOptions
	cmd          *exec.Cmd
	statusReader *os.File
	holdWriter   *os.File
	status       <-chan venvCreationResult

	mu          sync.Mutex
	waitStarted bool
	waitDone    chan struct{}
	signalErr   error
}

type venvCreationResult struct {
	status venvCreationStatus
	err    error
}

func startVenvCreation(options canonicalOptions, path string) (*venvCreation, error) {
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		return nil, operationError("venv-pipe")
	}
	holdReader, holdWriter, err := os.Pipe()
	if err != nil {
		_ = statusReader.Close()
		_ = statusWriter.Close()
		return nil, operationError("venv-pipe")
	}
	cmd := exec.Command(options.pythonPath, "-I", "-u", "-c", fixedSupervisorSource, "--venv-create", path)
	cmd.Env = append([]string(nil), options.env...)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.ExtraFiles = []*os.File{statusWriter, holdReader}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	closeAll := func() {
		_ = statusReader.Close()
		_ = statusWriter.Close()
		_ = holdReader.Close()
		_ = holdWriter.Close()
	}
	if err := cmd.Start(); err != nil {
		closeAll()
		return nil, operationError("venv-start")
	}
	_ = statusWriter.Close()
	_ = holdReader.Close()
	statusCh := make(chan venvCreationResult, 1)
	creation := &venvCreation{
		options: options, cmd: cmd, statusReader: statusReader, holdWriter: holdWriter,
		status: statusCh, waitDone: make(chan struct{}),
	}
	go func() {
		var status venvCreationStatus
		err := readFrame(statusReader, 128, &status)
		statusCh <- venvCreationResult{status: status, err: err}
	}()
	return creation, nil
}

func (creation *venvCreation) cancel() {
	if creation == nil || creation.cmd == nil || creation.cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-creation.cmd.Process.Pid, syscall.SIGTERM); err != nil && !processGroupGone(err) {
		creation.mu.Lock()
		creation.signalErr = errCleanupIncomplete
		creation.mu.Unlock()
	}
	timer := time.NewTimer(creation.options.limits.TerminateGrace)
	<-timer.C
}

func (creation *venvCreation) finish() error {
	if creation == nil || creation.cmd == nil || creation.cmd.Process == nil {
		return nil
	}
	creation.mu.Lock()
	if !creation.waitStarted {
		if err := syscall.Kill(-creation.cmd.Process.Pid, syscall.SIGKILL); err != nil && !processGroupGone(err) {
			creation.signalErr = errCleanupIncomplete
			creation.mu.Unlock()
			return errCleanupIncomplete
		}
		creation.waitStarted = true
		_ = creation.holdWriter.Close()
		go func() {
			if creation.options.hooks != nil {
				runHook(creation.options.hooks.beforeVenvCreatorWait)
			}
			_ = creation.cmd.Wait()
			close(creation.waitDone)
		}()
	}
	waitDone := creation.waitDone
	creation.mu.Unlock()

	timer := time.NewTimer(creation.options.limits.KillWait)
	select {
	case <-waitDone:
		if !timer.Stop() {
			<-timer.C
		}
		_ = creation.statusReader.Close()
		creation.mu.Lock()
		err := creation.signalErr
		creation.mu.Unlock()
		return err
	case <-timer.C:
		return errCleanupIncomplete
	}
}

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
	creation, err := startVenvCreation(options, path)
	if err != nil {
		return cleanup(err)
	}
	venv.finishCreation = creation.finish
	select {
	case result := <-creation.status:
		if cleanupErr := creation.finish(); cleanupErr != nil {
			return venv, errors.Join(operationError("venv-failed"), cleanupErr)
		}
		venv.finishCreation = nil
		if result.err != nil || !result.status.Succeeded {
			return cleanup(operationError("venv-failed"))
		}
	case <-ctx.Done():
		creation.cancel()
		if cleanupErr := creation.finish(); cleanupErr != nil {
			return venv, errors.Join(ctx.Err(), cleanupErr)
		}
		venv.finishCreation = nil
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
