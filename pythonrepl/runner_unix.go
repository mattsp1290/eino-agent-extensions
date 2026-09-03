//go:build linux || darwin

package pythonrepl

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type runnerProcess struct {
	options canonicalOptions
	cmd     *exec.Cmd
	pgid    int

	requestWriter  *os.File
	responseReader *os.File
	statusReader   *os.File
	controlWriter  *os.File

	waitDone chan struct{}
	waitErr  error
	waitMu   sync.Mutex
	ioWG     sync.WaitGroup

	requestID uint64
	termMu    sync.Mutex
	termDone  chan struct{}
	termErr   error
}

type runnerReadiness struct {
	status supervisorStatus
	err    error
}

func startRunner(ctx context.Context, options canonicalOptions, pythonPath, directory string) (*runnerProcess, error) {
	requestReader, requestWriter, err := os.Pipe()
	if err != nil {
		return nil, operationError("runner-pipe")
	}
	responseReader, responseWriter, err := os.Pipe()
	if err != nil {
		_ = requestReader.Close()
		_ = requestWriter.Close()
		return nil, operationError("runner-pipe")
	}
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		_ = requestReader.Close()
		_ = requestWriter.Close()
		_ = responseReader.Close()
		_ = responseWriter.Close()
		return nil, operationError("runner-pipe")
	}
	controlReader, controlWriter, err := os.Pipe()
	if err != nil {
		_ = requestReader.Close()
		_ = requestWriter.Close()
		_ = responseReader.Close()
		_ = responseWriter.Close()
		_ = statusReader.Close()
		_ = statusWriter.Close()
		return nil, operationError("runner-pipe")
	}

	args := []string{"-I", "-u", "-c", fixedSupervisorSource, fixedRunnerSource, runnerProtocolVersion,
		strconv.FormatUint(uint64(options.requestMax), 10), strconv.FormatUint(uint64(options.responseMax), 10),
		strconv.Itoa(options.limits.MaxOutputBytesPerStream), strconv.Itoa(options.limits.MaxOutputBytesPerStream),
		strconv.Itoa(options.limits.MaxResultBytes), strconv.Itoa(options.limits.MaxExceptionBytes)}
	cmd := exec.Command(pythonPath, args...)
	cmd.Dir = directory
	cmd.Env = append([]string(nil), options.env...)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.ExtraFiles = []*os.File{requestReader, responseWriter, statusWriter, controlReader}
	process := &runnerProcess{
		options: options, cmd: cmd, requestWriter: requestWriter, responseReader: responseReader,
		statusReader: statusReader, controlWriter: controlWriter, waitDone: make(chan struct{}),
	}
	closeAll := func() {
		_ = requestReader.Close()
		_ = requestWriter.Close()
		_ = responseReader.Close()
		_ = responseWriter.Close()
		_ = statusReader.Close()
		_ = statusWriter.Close()
		_ = controlReader.Close()
		_ = controlWriter.Close()
	}
	if err := cmd.Start(); err != nil {
		closeAll()
		return nil, operationError("runner-start")
	}
	_ = requestReader.Close()
	_ = responseWriter.Close()
	_ = statusWriter.Close()
	_ = controlReader.Close()
	go func() {
		err := cmd.Wait()
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		close(process.waitDone)
	}()

	readyCh := make(chan runnerReadiness, 1)
	go func() {
		var status supervisorStatus
		err := readFrame(statusReader, 1024, &status)
		readyCh <- runnerReadiness{status, err}
	}()
	if options.hooks != nil {
		runContextHook(options.hooks.afterSupervisorStart, ctx)
	}
	if ctx.Err() != nil {
		return process.cancelStartup(ctx, readyCh)
	}
	select {
	case ready := <-readyCh:
		return process.classifyReadiness(ready.status, ready.err)
	case <-ctx.Done():
		return process.cancelStartup(ctx, readyCh)
	}
}

func (process *runnerProcess) cancelStartup(ctx context.Context, readyCh <-chan runnerReadiness) (*runnerProcess, error) {
	timer := time.NewTimer(process.options.limits.KillWait)
	select {
	case ready := <-readyCh:
		if !timer.Stop() {
			<-timer.C
		}
		if process.readinessHasAnchoredGroup(ready.status, ready.err) {
			process.pgid = ready.status.PGID
			if err := process.terminateAndWait(); err != nil {
				return process, errors.Join(ctx.Err(), err)
			}
			return nil, ctx.Err()
		}
	case <-timer.C:
	}
	process.forceSupervisorStop()
	return process, errors.Join(ctx.Err(), errCleanupIncomplete)
}

func (process *runnerProcess) readinessHasAnchoredGroup(status supervisorStatus, err error) bool {
	return err == nil && status.Phase == "ready" && status.PID > 0 && status.PGID == status.PID
}

func (process *runnerProcess) classifyReadiness(status supervisorStatus, readErr error) (*runnerProcess, error) {
	if !process.readinessHasAnchoredGroup(status, readErr) {
		process.forceSupervisorStop()
		return process, errors.Join(operationError("runner-readiness"), errCleanupIncomplete)
	}
	process.pgid = status.PGID
	compatible := status.Version == runnerProtocolVersion && len(status.Python) == 2 &&
		status.Python[0] == 3 && status.Python[1] >= 11 && status.Python[1] <= 14
	if compatible {
		return process, nil
	}
	if err := process.terminateAndWait(); err != nil {
		return process, errors.Join(errBootstrapIntegrity, err)
	}
	return nil, errBootstrapIntegrity
}

func (process *runnerProcess) terminateAndWait() error {
	done := process.terminate()
	maximum := process.options.limits.TerminateGrace + 2*process.options.limits.KillWait + time.Second
	timer := time.NewTimer(maximum)
	select {
	case <-done:
		if !timer.Stop() {
			<-timer.C
		}
		return process.terminationError()
	case <-timer.C:
		return errCleanupIncomplete
	}
}

func (process *runnerProcess) execute(ctx context.Context, code string) (executionOutcome, error) {
	select {
	case <-process.waitDone:
		return executionOutcome{}, operationError("supervisor-exited")
	default:
	}
	process.requestID++
	request := runnerRequest{Version: runnerProtocolVersion, ID: process.requestID, Code: code}
	frame, err := encodeFrame(request, process.options.requestMax)
	if err != nil {
		return executionOutcome{}, err
	}
	coordinator, stopCancel := newRaceCoordinator(ctx)
	defer stopCancel()
	if process.options.hooks != nil {
		runContextHook(process.options.hooks.beforeRequestWrite, ctx)
	}
	if err := ctx.Err(); err != nil {
		coordinator.interrupt()
		return executionOutcome{}, err
	}
	writeCh := make(chan struct {
		n   int
		err error
	}, 1)
	process.ioWG.Add(1)
	go func() {
		defer process.ioWG.Done()
		n, writeErr := process.requestWriter.Write(frame)
		writeCh <- struct {
			n   int
			err error
		}{n, writeErr}
	}()
	var wrote int
	select {
	case write := <-writeCh:
		wrote = write.n
		if write.err != nil || write.n != len(frame) {
			return executionOutcome{mayHaveExecuted: write.n > 0}, operationError("protocol-write")
		}
		if process.options.hooks != nil {
			runContextHook(process.options.hooks.afterRequestWrite, ctx)
		}
		if err := ctx.Err(); err != nil {
			return executionOutcome{mayHaveExecuted: true}, err
		}
	case <-coordinator.cancelCh:
		return executionOutcome{mayHaveExecuted: true}, ctx.Err()
	case <-process.waitDone:
		return executionOutcome{mayHaveExecuted: wrote > 0}, operationError("supervisor-exited")
	}

	responseCh := make(chan struct {
		response runnerResponse
		err      error
	}, 1)
	process.ioWG.Add(1)
	go func() {
		defer process.ioWG.Done()
		var response runnerResponse
		readErr := readFrame(process.responseReader, process.options.responseMax, &response)
		responseCh <- struct {
			response runnerResponse
			err      error
		}{response, readErr}
	}()
	select {
	case received := <-responseCh:
		if received.err != nil || !validRunnerResponse(process.options, request.ID, received.response) {
			return executionOutcome{mayHaveExecuted: wrote > 0}, operationError("protocol-response")
		}
		select {
		case <-process.waitDone:
			return executionOutcome{mayHaveExecuted: true}, operationError("supervisor-exited")
		default:
		}
		if process.options.hooks != nil {
			runContextHook(process.options.hooks.beforeResponseCommit, ctx)
		}
		if err := ctx.Err(); err != nil {
			coordinator.interrupt()
			return executionOutcome{mayHaveExecuted: true}, err
		}
		if !coordinator.commit() {
			return executionOutcome{mayHaveExecuted: true}, ctx.Err()
		}
		stopCancel()
		return executionOutcome{response: received.response, mayHaveExecuted: true}, nil
	case <-coordinator.cancelCh:
		return executionOutcome{mayHaveExecuted: true}, ctx.Err()
	case <-process.waitDone:
		return executionOutcome{mayHaveExecuted: true}, operationError("supervisor-exited")
	}
}

func validRunnerResponse(options canonicalOptions, id uint64, response runnerResponse) bool {
	if response.Version != runnerProtocolVersion || response.ID != id || (response.Status != ExecuteStatusCompleted && response.Status != ExecuteStatusPythonError) {
		return false
	}
	fields := []struct {
		value   BoundedText
		maximum int
	}{
		{response.Stdout, options.limits.MaxOutputBytesPerStream}, {response.Stderr, options.limits.MaxOutputBytesPerStream},
		{response.Result, options.limits.MaxResultBytes}, {response.Exception, options.limits.MaxExceptionBytes},
	}
	for _, field := range fields {
		if len(field.value.Text) > field.maximum || !utf8.ValidString(field.value.Text) {
			return false
		}
	}
	empty := func(value BoundedText) bool { return value.Text == "" && !value.Truncated }
	if response.Status == ExecuteStatusCompleted && !empty(response.Exception) {
		return false
	}
	if response.Status == ExecuteStatusPythonError && !empty(response.Result) {
		return false
	}
	return true
}

func (process *runnerProcess) terminate() <-chan struct{} {
	process.termMu.Lock()
	defer process.termMu.Unlock()
	if process.termDone != nil {
		return process.termDone
	}
	process.termDone = make(chan struct{})
	go process.coordinateTermination()
	return process.termDone
}

func (process *runnerProcess) coordinateTermination() {
	var result error
	_ = process.requestWriter.Close()
	_ = process.responseReader.Close()
	process.ioWG.Wait()
	if process.pgid <= 0 {
		result = errCleanupIncomplete
	} else {
		if err := syscall.Kill(-process.pgid, syscall.SIGTERM); err != nil && !processGroupGone(err) {
			result = errCleanupIncomplete
		}
		timer := time.NewTimer(process.options.limits.TerminateGrace)
		<-timer.C
		if err := syscall.Kill(-process.pgid, syscall.SIGKILL); err != nil && !processGroupGone(err) {
			result = errCleanupIncomplete
		}
	}
	control, _ := encodeFrame(map[string]string{"command": "reap"}, 32)
	if process.options.hooks != nil {
		runHook(process.options.hooks.beforeReapAuthorize)
	}
	if _, err := process.controlWriter.Write(control); err != nil {
		result = errCleanupIncomplete
	}
	_ = process.controlWriter.Close()

	statusCh := make(chan error, 1)
	go func() {
		var status supervisorStatus
		err := readFrame(process.statusReader, 1024, &status)
		if err == nil && (status.Phase != "reaped" || status.Version != runnerProtocolVersion) {
			err = operationError("supervisor-status")
		}
		statusCh <- err
	}()
	timer := time.NewTimer(process.options.limits.KillWait)
	select {
	case err := <-statusCh:
		if err != nil {
			result = errCleanupIncomplete
		}
		if !timer.Stop() {
			<-timer.C
		}
	case <-timer.C:
		result = errCleanupIncomplete
	}
	_ = process.statusReader.Close()

	timer = time.NewTimer(process.options.limits.KillWait)
	select {
	case <-process.waitDone:
		if !timer.Stop() {
			<-timer.C
		}
	case <-timer.C:
		result = errCleanupIncomplete
	}
	process.termMu.Lock()
	process.termErr = result
	close(process.termDone)
	process.termMu.Unlock()
}

func (process *runnerProcess) terminationError() error {
	process.termMu.Lock()
	defer process.termMu.Unlock()
	return process.termErr
}

func (process *runnerProcess) forceSupervisorStop() {
	_ = process.requestWriter.Close()
	_ = process.responseReader.Close()
	process.ioWG.Wait()
	_ = process.statusReader.Close()
	_ = process.controlWriter.Close()
	if process.cmd.Process != nil {
		_ = process.cmd.Process.Kill()
	}
	select {
	case <-process.waitDone:
	case <-time.After(process.options.limits.KillWait):
	}
}
