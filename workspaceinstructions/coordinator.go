package workspaceinstructions

import (
	"context"
	"errors"
	"sync"
	"time"
)

type coordinator struct {
	mu       sync.Mutex
	limits   Limits
	closing  bool
	closed   bool
	live     int
	nextID   uint64
	cancels  map[uint64]context.CancelCauseFunc
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

type workEnvelope struct {
	section     string
	err         error
	completedAt time.Time
	cause       error
}

type workState struct {
	mu       sync.Mutex
	envelope *workEnvelope
	done     chan struct{}
}

type admission uint8

const (
	admitted admission = iota
	rejectedClosed
	rejectedSaturated
)

var (
	errCoordinatorClosing = errors.New("workspace instructions coordinator closing")
	errPackageDeadline    = errors.New("workspace instructions package deadline")
)

func newCoordinator(options canonicalOptions) *coordinator {
	return &coordinator{
		limits: options.limits, cancels: make(map[uint64]context.CancelCauseFunc),
		done: make(chan struct{}), clock: realCoordinatorClock{},
	}
}

func (c *coordinator) run(ctx context.Context, work func(context.Context) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	deadline := c.clock.Now().Add(c.limits.MaxWait)
	deadlineCtx, stopDeadline := context.WithDeadlineCause(ctx, deadline, errPackageDeadline)
	child, cancelChild := context.WithCancelCause(deadlineCtx)
	cancel := func(cause error) {
		cancelChild(cause)
		stopDeadline()
	}
	id, status := c.acquire(cancel)
	if status != admitted {
		cancel(nil)
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if status == rejectedClosed {
			return "", providerError("closed")
		}
		return "", providerError("saturated")
	}
	if err := ctx.Err(); err != nil {
		cancel(nil)
		c.release(id)
		return "", err
	}

	state := &workState{done: make(chan struct{})}
	go c.runWork(child, cancel, id, work, state)
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
			cancel(nil)
			return "", ctx.Err()
		case <-state.done:
		case <-timer.C():
		}
		if err := ctx.Err(); err != nil {
			cancel(nil)
			return "", err
		}
		envelope, published := state.snapshot()
		if section, err, settled := envelope.resultBefore(deadline, published); settled {
			cancel(nil)
			return section, err
		}
		if !c.clock.Now().Before(deadline) || (published && !envelope.completedAt.Before(deadline)) {
			cancel(errPackageDeadline)
			return "", providerError("deadline")
		}
	}
}

func (envelope workEnvelope) resultBefore(deadline time.Time, published bool) (string, error, bool) {
	if !published || !envelope.completedAt.Before(deadline) {
		return "", nil, false
	}
	if errors.Is(envelope.cause, errCoordinatorClosing) {
		return "", providerError("closed"), true
	}
	return envelope.section, envelope.err, true
}

func (c *coordinator) runWork(ctx context.Context, cancel context.CancelCauseFunc, id uint64, work func(context.Context) (string, error), state *workState) {
	defer cancel(nil)
	defer c.release(id)
	envelope := workEnvelope{}
	func() {
		defer func() {
			if recover() != nil {
				envelope.section = ""
				envelope.err = providerError("internal")
			}
		}()
		envelope.section, envelope.err = work(ctx)
	}()
	envelope.completedAt = c.clock.Now()
	envelope.cause = context.Cause(ctx)
	state.publish(envelope)
}

func (state *workState) publish(envelope workEnvelope) {
	state.mu.Lock()
	state.envelope = &envelope
	state.mu.Unlock()
	close(state.done)
}

func (state *workState) snapshot() (workEnvelope, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.envelope == nil {
		return workEnvelope{}, false
	}
	return *state.envelope, true
}

func (c *coordinator) acquire(cancel context.CancelCauseFunc) (uint64, admission) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.closed {
		return 0, rejectedClosed
	}
	if c.live >= c.limits.MaxInFlight {
		return 0, rejectedSaturated
	}
	c.nextID++
	id := c.nextID
	c.live++
	c.cancels[id] = cancel
	return id, admitted
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
		return providerError("close-context")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closing = true
	cancels := make([]context.CancelCauseFunc, 0, len(c.cancels))
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
		cancel(errCoordinatorClosing)
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
