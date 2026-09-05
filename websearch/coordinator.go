package websearch

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/mattsp1290/eino-agent/runtime"
)

type coordinator struct {
	mu       sync.Mutex
	searcher Searcher
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
	encoded     json.RawMessage
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
		searcher: options.searcher, limits: options.limits,
		cancels: make(map[uint64]context.CancelFunc), done: make(chan struct{}),
		clock: realCoordinatorClock{},
	}
}

func (c *coordinator) search(ctx context.Context, call runtime.ToolCall, executionContext runtime.ToolContext, input toolInput) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if call.SessionID == "" || call.RunID == "" || call.ID == "" {
		return nil, runtimeError("call-identity")
	}
	if executionContext.Turn.SessionID != "" && executionContext.Turn.SessionID != call.SessionID {
		return nil, runtimeError("turn-session")
	}
	if executionContext.Turn.RunID != "" && executionContext.Turn.RunID != call.RunID {
		return nil, runtimeError("turn-run")
	}
	startedAt := c.clock.Now()
	deadline := startedAt.Add(c.limits.MaxWait)
	child, cancel := context.WithDeadline(ctx, deadline)
	id, admitted := c.acquire(cancel)
	if !admitted {
		cancel()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errSearchCapacity
	}
	if err := ctx.Err(); err != nil {
		cancel()
		c.release(id)
		return nil, err
	}
	state := &responseState{done: make(chan struct{})}
	go c.runSearcher(child, cancel, id, input.Query, state)

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
			return nil, ctx.Err()
		case <-state.done:
		case <-timer.C():
		}
		if err := ctx.Err(); err != nil {
			cancel()
			return nil, err
		}
		envelope, published := state.snapshot()
		if published && envelope.completedAt.Before(deadline) {
			cancel()
			if envelope.err != nil {
				return nil, envelope.err
			}
			return append(json.RawMessage(nil), envelope.encoded...), nil
		}
		if !c.clock.Now().Before(deadline) || (published && !envelope.completedAt.Before(deadline)) {
			// Let the finite child deadline own cancellation identity. The
			// package timer and context deadline target the same instant, but
			// the timer may win the scheduler race by a few instructions.
			if child.Err() == nil {
				select {
				case <-child.Done():
				case <-ctx.Done():
					cancel()
					return nil, ctx.Err()
				}
			}
			if err := ctx.Err(); err != nil {
				cancel()
				return nil, err
			}
			cancel()
			return nil, context.DeadlineExceeded
		}
	}
}

func (c *coordinator) runSearcher(ctx context.Context, cancel context.CancelFunc, id uint64, query string, state *responseState) {
	defer cancel()
	defer c.release(id)
	envelope := responseEnvelope{}
	func() {
		defer func() {
			if recover() != nil {
				envelope.err = errSearchOperation
			}
		}()
		records, err := c.searcher.Search(ctx, query)
		if err != nil {
			envelope.err = errSearchOperation
			return
		}
		result := boundSources(records, c.limits)
		envelope.encoded, err = json.Marshal(result)
		if err != nil {
			envelope.err = runtimeError("result-encoding")
		}
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
