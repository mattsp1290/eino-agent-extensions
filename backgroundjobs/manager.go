package backgroundjobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

type ownerKey struct {
	sessionID   string
	workspaceID string
}

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
	signal   processGroupSignaler
}

func newManager(options canonicalOptions) (*manager, error) {
	result := &manager{
		options: options,
		jobs:    make(map[string]*job),
		hidden:  make(map[string]*job),
		signal:  signalProcessGroup,
	}
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
	defer releaseReservation()

	_, directory, err := resolveWorkingDirectory(workspaceRoot, input.WorkingDirectory)
	if err != nil {
		return StartResult{}, err
	}
	if err := ctx.Err(); err != nil {
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
		return StartResult{}, operationError("spawn-failed")
	}
	if err := ctx.Err(); err != nil {
		prepared.closeBeforeStart()
		return StartResult{}, err
	}
	startedAt := time.Now().UTC()
	if err := prepared.cmd.Start(); err != nil {
		prepared.closeBeforeStart()
		return StartResult{}, operationError("spawn-failed")
	}
	prepared.parentAfterStart()
	job := &job{
		manager: manager, id: id, owner: owner,
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

func channelClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}
