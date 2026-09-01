package backgroundjobs

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

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

type job struct {
	mu sync.Mutex

	manager       *manager
	id            string
	owner         ownerKey
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
	ready := job.ready
	job.mu.Unlock()
	completeAttempt := func() {
		job.mu.Lock()
		job.coordinatorRunning = false
		close(attempt)
		job.mu.Unlock()
	}
	if cause != causeNatural && phase == phaseInitial && ready {
		var advanced bool
		phase, advanced = job.advanceSignal(termSignal(), phaseTermSent)
		if !advanced {
			completeAttempt()
			return
		}
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
		if _, advanced := job.advanceSignal(killSignal(), phaseFinalSignalSent); !advanced {
			completeAttempt()
			return
		}
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

func (job *job) advanceSignal(signal os.Signal, successPhase terminationPhase) (terminationPhase, bool) {
	job.mu.Lock()
	defer job.mu.Unlock()
	err := job.manager.signal(job.pgid, signal)
	if err != nil && !processGroupGone(err) {
		return job.phase, false
	}
	if err != nil {
		successPhase = phaseFinalSignalSent
	}
	job.phase = successPhase
	if successPhase == phaseFinalSignalSent {
		job.finalSignalAt = time.Now()
	}
	return successPhase, true
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
