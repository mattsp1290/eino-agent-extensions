package backgroundjobs

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ownerKey struct {
	sessionID   string
	workspaceID string
}

type terminationCause uint8

const (
	causeNone terminationCause = iota
	causeNatural
	causeKill
	causeTimeout
	causeClose
)

type terminationPhase uint8

const (
	phaseInitial terminationPhase = iota
	phaseTermSent
	phaseFinalSignalSent
)

type manager struct {
	mu       sync.Mutex
	options  canonicalOptions
	epoch    [16]byte
	counter  uint64
	jobs     map[string]*job
	hidden   map[string]*job
	starting int
	running  int
	closing  bool
	closed   bool
	starts   sync.WaitGroup
}

type job struct {
	mu sync.Mutex

	manager       *manager
	id            string
	owner         ownerKey
	launchRoot    string
	startedAt     time.Time
	completedAt   time.Time
	effectiveTime time.Duration
	state         JobState
	exitCode      *int
	stdout        *tailWriter
	stderr        *tailWriter
	cmd           *exec.Cmd
	pgid          int
	published     bool
	counted       bool
	ready         bool

	statusReady   chan struct{}
	waitDone      chan struct{}
	done          chan struct{}
	statusOnce    sync.Once
	waitErr       error
	reaped        bool
	statusValid   bool
	statusCode    int
	outputForced  bool
	finalSignalAt time.Time
	timer         *time.Timer
	timeoutDone   chan struct{}
	timeoutOnce   sync.Once
	goroutines    sync.WaitGroup

	cause              terminationCause
	phase              terminationPhase
	coordinatorRunning bool
	attemptDone        chan struct{}
	terminalOnce       sync.Once
}

func newManager(options canonicalOptions) (*manager, error) {
	result := &manager{options: options, jobs: make(map[string]*job), hidden: make(map[string]*job)}
	if _, err := io.ReadFull(rand.Reader, result.epoch[:]); err != nil {
		return nil, operationError("identity-unavailable")
	}
	return result, nil
}

func (manager *manager) start(ctx context.Context, owner ownerKey, workspaceRoot string, input startInput) (StartResult, error) {
	if err := ctx.Err(); err != nil {
		return StartResult{}, err
	}
	id, err := manager.reserveStart()
	if err != nil {
		return StartResult{}, err
	}
	reservationOwned := true
	releaseReservation := func() {
		if reservationOwned {
			manager.mu.Lock()
			manager.starting--
			manager.mu.Unlock()
			manager.starts.Done()
			reservationOwned = false
		}
	}

	root, directory, err := resolveWorkingDirectory(workspaceRoot, input.WorkingDirectory)
	if err != nil {
		releaseReservation()
		return StartResult{}, err
	}
	if err := ctx.Err(); err != nil {
		releaseReservation()
		return StartResult{}, err
	}
	effective := manager.options.limits.DefaultTimeout
	if input.TimeoutSeconds != nil && *input.TimeoutSeconds > 0 {
		effective = time.Duration(*input.TimeoutSeconds) * time.Second
	}
	stdout := newTailWriter(manager.options.limits.MaxOutputBytesPerStream)
	stderr := newTailWriter(manager.options.limits.MaxOutputBytesPerStream)
	prepared, err := prepareProcess(manager.options, directory, input.Command, stdout, stderr)
	if err != nil {
		releaseReservation()
		return StartResult{}, operationError("spawn-failed")
	}
	if err := ctx.Err(); err != nil {
		prepared.closeBeforeStart()
		releaseReservation()
		return StartResult{}, err
	}
	startedAt := time.Now().UTC()
	if err := prepared.cmd.Start(); err != nil {
		prepared.closeBeforeStart()
		releaseReservation()
		return StartResult{}, operationError("spawn-failed")
	}
	prepared.parentAfterStart()
	job := &job{
		manager: manager, id: id, owner: owner, launchRoot: root,
		startedAt: startedAt, effectiveTime: effective, state: JobRunning,
		stdout: stdout, stderr: stderr, cmd: prepared.cmd, pgid: prepared.cmd.Process.Pid,
		statusReady: make(chan struct{}), waitDone: make(chan struct{}), done: make(chan struct{}),
	}
	job.startCoordination(prepared.statusReader)
	readyErr := waitSupervisorReady(prepared.readyReader, manager.options.limits.KillWait)
	job.mu.Lock()
	job.ready = readyErr == nil
	job.mu.Unlock()
	manager.mu.Lock()
	closingBeforeGate := manager.closing
	manager.mu.Unlock()
	gateReleased := readyErr == nil && ctx.Err() == nil && !closingBeforeGate
	if err := prepared.releaseCommand(gateReleased); err != nil {
		readyErr = operationError("supervisor-control")
		gateReleased = false
	}

	manager.mu.Lock()
	callerCanceled := ctx.Err() != nil
	terminal := channelClosed(job.done)
	if readyErr == nil && gateReleased && !manager.closing && !callerCanceled {
		job.published = true
		manager.jobs[id] = job
	} else if !terminal {
		manager.hidden[id] = job
	}
	if !terminal {
		job.counted = true
		manager.running++
	}
	manager.starting--
	manager.mu.Unlock()
	reservationOwned = false

	manager.starts.Done()
	if effective > 0 && job.published {
		job.mu.Lock()
		if job.state == JobRunning {
			job.timeoutDone = make(chan struct{})
			job.timer = time.AfterFunc(effective, func() {
				defer job.timeoutOnce.Do(func() { close(job.timeoutDone) })
				job.beginTermination(causeTimeout)
			})
		}
		job.mu.Unlock()
	}

	if readyErr != nil || !job.published {
		job.beginTermination(causeClose)
		cleanupErr := manager.waitInternalTermination(job)
		if cleanupErr != nil {
			if callerCanceled {
				return StartResult{}, errors.Join(cleanupErr, ctx.Err())
			}
			return StartResult{}, cleanupErr
		}
		if callerCanceled {
			return StartResult{}, ctx.Err()
		}
		if readyErr != nil {
			_ = prepared.readyReader.Close()
			return StartResult{}, operationError("supervisor-readiness")
		}
		return StartResult{}, errManagerClosing
	}
	return job.startResult(), nil
}

func (manager *manager) reserveStart() (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closing || manager.closed {
		return "", errManagerClosing
	}
	manager.pruneLocked()
	if manager.starting+manager.running >= manager.options.limits.MaxRunning || len(manager.jobs)+len(manager.hidden)+manager.starting >= manager.options.limits.MaxTracked {
		return "", errCapacityExhausted
	}
	if manager.counter == ^uint64(0) {
		return "", operationError("identity-exhausted")
	}
	manager.counter++
	id := fmt.Sprintf("job_%s_%016x", hex.EncodeToString(manager.epoch[:]), manager.counter)
	manager.starting++
	manager.starts.Add(1)
	return id, nil
}

func (manager *manager) pruneLocked() {
	for len(manager.jobs)+len(manager.hidden)+manager.starting >= manager.options.limits.MaxTracked {
		var candidate *job
		for _, current := range manager.jobs {
			select {
			case <-current.done:
				if candidate == nil || current.completedAt.Before(candidate.completedAt) || (current.completedAt.Equal(candidate.completedAt) && current.id < candidate.id) {
					candidate = current
				}
			default:
			}
		}
		if candidate == nil {
			return
		}
		delete(manager.jobs, candidate.id)
	}
}

func (manager *manager) status(owner ownerKey, id string) (StatusResult, error) {
	manager.mu.Lock()
	job := manager.jobs[id]
	manager.mu.Unlock()
	if job == nil || job.owner != owner {
		return StatusResult{}, errJobNotFound
	}
	return job.statusResult(), nil
}

func (manager *manager) list(owner ownerKey) ListResult {
	manager.mu.Lock()
	jobs := make([]*job, 0, len(manager.jobs))
	for _, current := range manager.jobs {
		if current.owner == owner {
			jobs = append(jobs, current)
		}
	}
	manager.mu.Unlock()
	sort.Slice(jobs, func(left, right int) bool {
		if jobs[left].startedAt.Equal(jobs[right].startedAt) {
			return jobs[left].id < jobs[right].id
		}
		return jobs[left].startedAt.Before(jobs[right].startedAt)
	})
	result := ListResult{Jobs: make([]JobSummary, 0, len(jobs))}
	for _, current := range jobs {
		result.Jobs = append(result.Jobs, current.summary())
	}
	return result
}

func (manager *manager) kill(ctx context.Context, owner ownerKey, id string) (KillResult, error) {
	manager.mu.Lock()
	job := manager.jobs[id]
	manager.mu.Unlock()
	if job == nil || job.owner != owner {
		return KillResult{}, errJobNotFound
	}
	newly, attempt := job.beginTermination(causeKill)
	if err := waitForTermination(ctx, job, attempt); err != nil {
		return KillResult{}, err
	}
	status := job.statusResult()
	return KillResult{ID: status.ID, State: status.State, NewlyAccepted: newly}, nil
}

func (manager *manager) Close(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil
	}
	manager.closing = true
	manager.mu.Unlock()

	startsDone := make(chan struct{})
	go func() {
		manager.starts.Wait()
		close(startsDone)
	}()
	select {
	case <-startsDone:
	case <-ctx.Done():
		return errors.Join(errTerminationIncomplete, ctx.Err())
	}

	manager.mu.Lock()
	jobs := make([]*job, 0, len(manager.jobs)+len(manager.hidden))
	for _, current := range manager.jobs {
		jobs = append(jobs, current)
	}
	for _, current := range manager.hidden {
		jobs = append(jobs, current)
	}
	manager.mu.Unlock()
	attempts := make([]<-chan struct{}, len(jobs))
	for index, current := range jobs {
		_, attempts[index] = current.beginTermination(causeClose)
	}
	for index, current := range jobs {
		if err := waitForTermination(ctx, current, attempts[index]); err != nil {
			return err
		}
		current.goroutines.Wait()
		current.mu.Lock()
		timeoutDone := current.timeoutDone
		current.mu.Unlock()
		if timeoutDone != nil {
			select {
			case <-timeoutDone:
			case <-ctx.Done():
				return errors.Join(errTerminationIncomplete, ctx.Err())
			}
		}
	}
	manager.mu.Lock()
	manager.jobs = make(map[string]*job)
	manager.hidden = make(map[string]*job)
	manager.closed = true
	manager.mu.Unlock()
	return nil
}

func (manager *manager) waitInternalTermination(job *job) error {
	deadline := manager.options.limits.TerminateGrace + manager.options.limits.KillWait + time.Second
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	job.mu.Lock()
	attempt := job.attemptDone
	job.mu.Unlock()
	return waitForTermination(ctx, job, attempt)
}

func waitForTermination(ctx context.Context, job *job, attempt <-chan struct{}) error {
	select {
	case <-job.done:
		return nil
	case <-attempt:
		select {
		case <-job.done:
			return nil
		default:
			return errTerminationIncomplete
		}
	case <-ctx.Done():
		return errors.Join(errTerminationIncomplete, ctx.Err())
	}
}

func (manager *manager) jobCompleted(job *job) {
	manager.mu.Lock()
	if _, ok := manager.hidden[job.id]; ok {
		delete(manager.hidden, job.id)
	}
	if job.counted {
		job.counted = false
		manager.running--
	}
	manager.mu.Unlock()
}

func waitSupervisorReady(reader *os.File, limit time.Duration) error {
	if err := reader.SetReadDeadline(time.Now().Add(limit)); err != nil {
		return operationError("supervisor-readiness")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 2))
	if err != nil || string(raw) != "R" {
		return operationError("supervisor-readiness")
	}
	return reader.Close()
}

func channelClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func (job *job) startCoordination(statusReader io.ReadCloser) {
	job.goroutines.Add(2)
	go func() {
		defer job.goroutines.Done()
		job.readStatus(statusReader)
	}()
	go func() {
		defer job.goroutines.Done()
		err := job.cmd.Wait()
		job.mu.Lock()
		waitDelayElapsed := !job.finalSignalAt.IsZero() && time.Since(job.finalSignalAt) >= job.manager.options.limits.KillWait-job.manager.options.limits.KillWait/20
		if errors.Is(err, exec.ErrWaitDelay) || waitDelayElapsed {
			job.stdout.markTruncated()
			job.stderr.markTruncated()
		}
		job.waitErr = err
		job.outputForced = errors.Is(err, exec.ErrWaitDelay) || waitDelayElapsed
		job.cmd = nil
		job.reaped = true
		close(job.waitDone)
		finalize := job.phase == phaseFinalSignalSent && job.cause != causeNone
		job.mu.Unlock()
		if finalize {
			job.finishTerminal()
		}
	}()
}

func (job *job) readStatus(reader io.ReadCloser) {
	defer reader.Close()
	limited := bufio.NewReader(io.LimitReader(reader, 65))
	raw, err := io.ReadAll(limited)
	valid, code := parseSupervisorStatus(raw, err)
	job.mu.Lock()
	job.statusValid = valid
	job.statusCode = code
	job.statusOnce.Do(func() { close(job.statusReady) })
	if job.cause == causeNone {
		job.cause = causeNatural
		job.startCoordinatorLocked()
	}
	job.mu.Unlock()
}

func parseSupervisorStatus(raw []byte, readErr error) (bool, int) {
	if readErr != nil || len(raw) < 5 || len(raw) > 8 || !strings.HasPrefix(string(raw), "v1:") || raw[len(raw)-1] != '\n' {
		return false, 0
	}
	value := string(raw[3 : len(raw)-1])
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return false, 0
	}
	code, err := strconv.Atoi(value)
	return err == nil && code >= 0 && code <= 255, code
}

func (job *job) beginTermination(cause terminationCause) (bool, <-chan struct{}) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.state != JobRunning {
		return false, job.done
	}
	newly := false
	if job.cause == causeNone {
		job.cause = cause
		newly = true
	}
	if !job.coordinatorRunning {
		job.startCoordinatorLocked()
	}
	return newly, job.attemptDone
}

func (job *job) startCoordinatorLocked() {
	job.coordinatorRunning = true
	job.attemptDone = make(chan struct{})
	attempt := job.attemptDone
	job.goroutines.Add(1)
	go func() {
		defer job.goroutines.Done()
		job.coordinate(attempt)
	}()
}

func (job *job) coordinate(attempt chan struct{}) {
	job.mu.Lock()
	cause := job.cause
	phase := job.phase
	reaped := job.reaped
	ready := job.ready
	job.mu.Unlock()
	completeAttempt := func() {
		job.mu.Lock()
		job.coordinatorRunning = false
		close(attempt)
		job.mu.Unlock()
	}
	if reaped && phase != phaseFinalSignalSent {
		completeAttempt()
		return
	}

	if cause != causeNatural && phase == phaseInitial && ready {
		job.mu.Lock()
		if job.reaped {
			job.mu.Unlock()
			completeAttempt()
			return
		}
		if err := signalProcessGroup(job.pgid, termSignal()); err != nil {
			job.mu.Unlock()
			completeAttempt()
			return
		}
		job.phase = phaseTermSent
		job.mu.Unlock()
		phase = phaseTermSent
	}
	if cause != causeNatural && phase == phaseTermSent {
		timer := time.NewTimer(job.manager.options.limits.TerminateGrace)
		select {
		case <-job.statusReady:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
	if phase != phaseFinalSignalSent {
		job.mu.Lock()
		if job.reaped {
			job.mu.Unlock()
			completeAttempt()
			return
		}
		if signalProcessGroup(job.pgid, killSignal()) != nil {
			job.mu.Unlock()
			completeAttempt()
			return
		}
		job.phase = phaseFinalSignalSent
		job.finalSignalAt = time.Now()
		job.mu.Unlock()
	}

	timer := time.NewTimer(job.manager.options.limits.KillWait)
	select {
	case <-job.waitDone:
		if !timer.Stop() {
			<-timer.C
		}
		job.finishTerminal()
	case <-timer.C:
		completeAttempt()
	}
}

func (job *job) finishTerminal() {
	job.terminalOnce.Do(func() {
		job.mu.Lock()
		if job.timer != nil {
			if job.timer.Stop() {
				job.timeoutOnce.Do(func() { close(job.timeoutDone) })
			}
		}
		switch job.cause {
		case causeKill, causeClose:
			job.state = JobKilled
		case causeTimeout:
			job.state = JobTimedOut
		case causeNatural:
			if job.statusValid && !job.outputForced {
				code := job.statusCode
				job.exitCode = &code
				if code == 0 {
					job.state = JobSucceeded
				} else {
					job.state = JobFailed
				}
			} else {
				job.state = JobFailed
			}
		}
		job.completedAt = time.Now().UTC()
		job.coordinatorRunning = false
		job.mu.Unlock()
		job.manager.jobCompleted(job)
		job.mu.Lock()
		close(job.done)
		job.mu.Unlock()
	})
}

func (job *job) startResult() StartResult {
	job.mu.Lock()
	defer job.mu.Unlock()
	return StartResult{ID: job.id, State: job.state, StartedAt: formatTime(job.startedAt), TimeoutSeconds: int64(job.effectiveTime / time.Second)}
}

func (job *job) statusResult() StatusResult {
	job.mu.Lock()
	result := StatusResult{
		ID: job.id, State: job.state, StartedAt: formatTime(job.startedAt),
		TimeoutSeconds: int64(job.effectiveTime / time.Second),
	}
	if !job.completedAt.IsZero() {
		result.CompletedAt = formatTime(job.completedAt)
	}
	if job.exitCode != nil {
		code := *job.exitCode
		result.ExitCode = &code
	}
	job.mu.Unlock()
	result.Stdout = job.stdout.snapshot()
	result.Stderr = job.stderr.snapshot()
	return result
}

func (job *job) summary() JobSummary {
	job.mu.Lock()
	defer job.mu.Unlock()
	result := JobSummary{ID: job.id, State: job.state, StartedAt: formatTime(job.startedAt), TimeoutSeconds: int64(job.effectiveTime / time.Second)}
	if !job.completedAt.IsZero() {
		result.CompletedAt = formatTime(job.completedAt)
	}
	return result
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func resolveWorkingDirectory(workspaceRoot, relative string) (string, string, error) {
	if workspaceRoot == "" {
		return "", "", runtimeError("workspace-root")
	}
	root, err := filepath.Abs(filepath.Clean(workspaceRoot))
	if err != nil {
		return "", "", runtimeError("workspace-root")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", runtimeError("workspace-root")
	}
	rootInfo, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", "", runtimeError("workspace-root")
	}
	target := filepath.Join(rootInfo, relative)
	target, err = filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", "", runtimeError("working-directory")
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", runtimeError("working-directory")
	}
	rel, err := filepath.Rel(rootInfo, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", "", runtimeError("working-directory")
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return "", "", runtimeError("working-directory")
	}
	return rootInfo, target, nil
}
