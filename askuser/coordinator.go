package askuser

import (
	"context"
	"sync"
	"time"

	"github.com/mattsp1290/eino-agent/runtime"
)

type coordinator struct {
	mu        sync.Mutex
	responder Responder
	limits    Limits
	closing   bool
	closed    bool
	live      int
	nextID    uint64
	cancels   map[uint64]context.CancelFunc
	done      chan struct{}
	doneOnce  sync.Once
	clock     coordinatorClock
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
		responder: options.responder, limits: options.limits,
		cancels: make(map[uint64]context.CancelFunc), done: make(chan struct{}),
		clock: realCoordinatorClock{},
	}
}

func (c *coordinator) ask(ctx context.Context, call runtime.ToolCall, input toolInput) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if call.SessionID == "" || call.RunID == "" || call.ID == "" {
		return Result{}, runtimeError("call-identity")
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
		Question: input.Question, Options: make([]Option, len(input.Options)),
		AllowCustom: true, CustomLabel: CustomOptionLabel,
	}
	for index, option := range input.Options {
		request.Options[index] = Option{Label: option.Label, Description: option.Description}
	}
	state := &responseState{done: make(chan struct{})}
	go c.runResponder(child, cancel, id, cloneRequest(request), state)

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
			return mapResponse(envelope, input, c.limits)
		}
		if !c.clock.Now().Before(deadline) || (published && !envelope.completedAt.Before(deadline)) {
			cancel()
			return Result{Status: StatusTimedOut}, nil
		}
	}
}

func (c *coordinator) runResponder(ctx context.Context, cancel context.CancelFunc, id uint64, request Request, state *responseState) {
	defer cancel()
	defer c.release(id)
	envelope := responseEnvelope{}
	func() {
		defer func() {
			if recover() != nil {
				envelope.err = errResponderOperation
			}
		}()
		envelope.response, envelope.err = c.responder.Respond(ctx, request)
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

func mapResponse(envelope responseEnvelope, input toolInput, limits Limits) (Result, error) {
	if envelope.err != nil {
		return Result{}, errResponderOperation
	}
	response := envelope.response
	switch response.Kind {
	case ResponseSelected:
		if response.SelectedOption < 1 || response.SelectedOption > len(input.Options) || response.CustomAnswer != "" {
			return Result{}, errResponderOperation
		}
		return Result{Status: StatusSelected, Answer: input.Options[response.SelectedOption-1].Label, SelectedOption: response.SelectedOption}, nil
	case ResponseCustom:
		if response.SelectedOption != 0 || !validRequiredText(response.CustomAnswer, limits.MaxCustomAnswerBytes) {
			return Result{}, errResponderOperation
		}
		return Result{Status: StatusCustom, Answer: response.CustomAnswer}, nil
	case ResponseDismissed:
		if response.SelectedOption != 0 || response.CustomAnswer != "" {
			return Result{}, errResponderOperation
		}
		return Result{Status: StatusDismissed}, nil
	case ResponseUnavailable:
		if response.SelectedOption != 0 || response.CustomAnswer != "" {
			return Result{}, errResponderOperation
		}
		return Result{Status: StatusUnavailable}, nil
	default:
		return Result{}, errResponderOperation
	}
}

func cloneRequest(request Request) Request {
	request.Options = append([]Option(nil), request.Options...)
	return request
}
