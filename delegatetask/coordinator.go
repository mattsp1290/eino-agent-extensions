package delegatetask

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mattsp1290/eino-agent/runtime"
)

type coordinator struct {
	mu       sync.Mutex
	runner   Runner
	limits   Limits
	closing  bool
	closed   bool
	live     int
	nextID   uint64
	cancels  map[uint64]context.CancelFunc
	done     chan struct{}
	doneOnce sync.Once
	clock    coordinatorClock
}

type coordinatorClock interface {
	Now() time.Time
	NewDeadlineTimer(time.Time) coordinatorTimer
}

type coordinatorTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type realCoordinatorClock struct{}

func (realCoordinatorClock) Now() time.Time { return time.Now() }
func (realCoordinatorClock) NewDeadlineTimer(deadline time.Time) coordinatorTimer {
	return realCoordinatorTimer{timer: time.NewTimer(time.Until(deadline))}
}

type realCoordinatorTimer struct{ timer *time.Timer }

func (timer realCoordinatorTimer) C() <-chan time.Time { return timer.timer.C }
func (timer realCoordinatorTimer) Stop() bool          { return timer.timer.Stop() }

type responseEnvelope struct {
	response    Response
	err         error
	completedAt time.Time
}

type responseState struct {
	mu       sync.Mutex
	envelope *responseEnvelope
	done     chan struct{}
}

func newCoordinator(options canonicalOptions) *coordinator {
	return &coordinator{
		runner: options.runner, limits: options.limits,
		cancels: make(map[uint64]context.CancelFunc), done: make(chan struct{}),
		clock: realCoordinatorClock{},
	}
}

func (c *coordinator) run(ctx context.Context, call runtime.ToolCall, executionContext runtime.ToolContext, input toolInput) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if call.SessionID == "" || call.RunID == "" || call.ID == "" {
		return Result{}, runtimeError("call-identity")
	}
	if executionContext.Turn.SessionID != "" && executionContext.Turn.SessionID != call.SessionID {
		return Result{}, runtimeError("turn-session")
	}
	if executionContext.Turn.RunID != "" && executionContext.Turn.RunID != call.RunID {
		return Result{}, runtimeError("turn-run")
	}
	startedAt := c.clock.Now()
	deadline := startedAt.Add(c.limits.MaxWait)
	child, cancel := context.WithDeadline(ctx, deadline)
	id, admitted := c.acquire(cancel)
	if !admitted {
		cancel()
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusUnavailable}, nil
	}
	if err := ctx.Err(); err != nil {
		cancel()
		c.release(id)
		return Result{}, err
	}
	request := Request{
		SessionID: call.SessionID, RunID: call.RunID, ToolCallID: call.ID,
		WorkspaceID: executionContext.WorkspaceID, WorkspaceRoot: executionContext.WorkspaceRoot,
		Task: input.Task, Profile: input.Profile,
	}
	state := &responseState{done: make(chan struct{})}
	go c.runRunner(child, cancel, id, request, state)

	timer := c.clock.NewDeadlineTimer(deadline)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C():
			default:
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			cancel()
			return Result{}, ctx.Err()
		case <-state.done:
		case <-timer.C():
		}
		if err := ctx.Err(); err != nil {
			cancel()
			return Result{}, err
		}
		envelope, published := state.snapshot()
		if published && envelope.completedAt.Before(deadline) {
			cancel()
			return mapResponse(envelope, c.limits)
		}
		if !c.clock.Now().Before(deadline) || (published && !envelope.completedAt.Before(deadline)) {
			cancel()
			return Result{Status: StatusTimedOut}, nil
		}
	}
}

func (c *coordinator) runRunner(ctx context.Context, cancel context.CancelFunc, id uint64, request Request, state *responseState) {
	defer cancel()
	defer c.release(id)
	envelope := responseEnvelope{}
	func() {
		defer func() {
			if recover() != nil {
				envelope.err = errRunnerOperation
			}
		}()
		envelope.response, envelope.err = c.runner.Run(ctx, request)
	}()
	envelope.completedAt = c.clock.Now()
	state.publish(envelope)
}

func (state *responseState) publish(envelope responseEnvelope) {
	state.mu.Lock()
	state.envelope = &envelope
	state.mu.Unlock()
	close(state.done)
}

func (state *responseState) snapshot() (responseEnvelope, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.envelope == nil {
		return responseEnvelope{}, false
	}
	return *state.envelope, true
}

func (c *coordinator) acquire(cancel context.CancelFunc) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.closed || c.live >= c.limits.MaxInFlight {
		return 0, false
	}
	c.nextID++
	id := c.nextID
	c.live++
	c.cancels[id] = cancel
	return id, true
}

func (c *coordinator) release(id uint64) {
	c.mu.Lock()
	if _, exists := c.cancels[id]; exists {
		delete(c.cancels, id)
		c.live--
	}
	if c.closing && c.live == 0 {
		c.closed = true
		c.doneOnce.Do(func() { close(c.done) })
	}
	c.mu.Unlock()
}

func (c *coordinator) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		return runtimeError("close-context")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closing = true
	cancels := make([]context.CancelFunc, 0, len(c.cancels))
	for _, cancel := range c.cancels {
		cancels = append(cancels, cancel)
	}
	if c.live == 0 {
		c.closed = true
		c.doneOnce.Do(func() { close(c.done) })
	}
	done := c.done
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func mapResponse(envelope responseEnvelope, limits Limits) (Result, error) {
	if envelope.err != nil {
		return Result{}, errRunnerOperation
	}
	if !utf8.ValidString(envelope.response.Output) || len(envelope.response.Output) > limits.MaxResultBytes {
		return Result{}, errRunnerOperation
	}
	for _, b := range []byte(envelope.response.Output) {
		if b == 0 {
			return Result{}, errRunnerOperation
		}
	}
	switch envelope.response.Status {
	case ResponseCompleted:
		return Result{Status: StatusCompleted, Output: envelope.response.Output}, nil
	case ResponseFailed:
		return Result{Status: StatusFailed, Output: envelope.response.Output}, nil
	case ResponseRejected:
		return Result{Status: StatusRejected, Output: envelope.response.Output}, nil
	case ResponseUnavailable:
		return Result{Status: StatusUnavailable, Output: envelope.response.Output}, nil
	default:
		return Result{}, errRunnerOperation
	}
}
