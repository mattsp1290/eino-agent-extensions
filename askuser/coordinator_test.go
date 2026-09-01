package askuser

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func testCall(id string) agentruntime.ToolCall {
	return agentruntime.ToolCall{ID: session.ToolCallID(id), SessionID: "session", RunID: "run", Name: ToolName}
}

func testInput() toolInput {
	return toolInput{Question: "Which synthetic option?", Options: []toolOption{{Label: "A", Description: "first"}, {Label: "B"}}}
}

func coordinatorWithResponder(t *testing.T, responder Responder, mutate func(*Limits)) *coordinator {
	t.Helper()
	options := testOptions()
	options.Responder = responder
	if mutate != nil {
		mutate(&options.Limits)
	}
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	return newCoordinator(canonical)
}

func TestCoordinatorMapsRequestAndDefendsCanonicalInput(t *testing.T) {
	var observed Request
	coordinator := coordinatorWithResponder(t, ResponderFunc(func(_ context.Context, request Request) (Response, error) {
		observed = request
		request.Options[0].Label = "mutated by host"
		return Response{Kind: ResponseSelected, SelectedOption: 1}, nil
	}), nil)
	result, err := coordinator.ask(context.Background(), testCall("call"), testInput())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSelected || result.Answer != "A" || result.SelectedOption != 1 {
		t.Fatalf("result = %#v", result)
	}
	if observed.SessionID != "session" || observed.RunID != "run" || observed.ToolCallID != "call" || !observed.AllowCustom || observed.CustomLabel != CustomOptionLabel {
		t.Fatalf("request = %#v", observed)
	}
}

func TestCoordinatorNormalOutcomes(t *testing.T) {
	for name, response := range map[string]Response{
		"custom":      {Kind: ResponseCustom, CustomAnswer: "custom synthetic answer"},
		"dismissed":   {Kind: ResponseDismissed},
		"unavailable": {Kind: ResponseUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			coordinator := coordinatorWithResponder(t, ResponderFunc(func(context.Context, Request) (Response, error) { return response, nil }), nil)
			result, err := coordinator.ask(context.Background(), testCall(name), testInput())
			if err != nil || string(result.Status) != name {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestCoordinatorCapacityCountsResponderUntilExit(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	coordinator := coordinatorWithResponder(t, ResponderFunc(func(context.Context, Request) (Response, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release // deliberately non-cooperative
		return Response{Kind: ResponseDismissed}, nil
	}), func(limits *Limits) {
		limits.MaxInFlight = 1
		limits.MaxWait = 20 * time.Millisecond
	})
	coordinator.released = make(chan struct{}, 1)
	firstDone := make(chan Result, 1)
	go func() {
		result, _ := coordinator.ask(context.Background(), testCall("first"), testInput())
		firstDone <- result
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("responder did not enter")
	}
	second, err := coordinator.ask(context.Background(), testCall("second"), testInput())
	if err != nil || second.Status != StatusUnavailable || calls.Load() != 1 {
		t.Fatalf("saturated result=%#v calls=%d err=%v", second, calls.Load(), err)
	}
	preCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := coordinator.ask(preCanceled, testCall("canceled-saturated"), testInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled saturated call error = %v", err)
	}
	select {
	case first := <-firstDone:
		if first.Status != StatusTimedOut {
			t.Fatalf("first result = %#v", first)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout did not bound caller wait")
	}
	third, err := coordinator.ask(context.Background(), testCall("third"), testInput())
	if err != nil || third.Status != StatusUnavailable || calls.Load() != 1 {
		t.Fatalf("retained-capacity result=%#v calls=%d err=%v", third, calls.Load(), err)
	}
	close(release)
	waitRelease(t, coordinator)
	fourth, err := coordinator.ask(context.Background(), testCall("fourth"), testInput())
	if err != nil || fourth.Status != StatusDismissed || calls.Load() != 2 {
		t.Fatalf("released-capacity result=%#v calls=%d err=%v", fourth, calls.Load(), err)
	}
}

func TestCoordinatorParentCancellationWins(t *testing.T) {
	entered := make(chan struct{})
	childCanceled := make(chan struct{})
	coordinator := coordinatorWithResponder(t, ResponderFunc(func(ctx context.Context, _ Request) (Response, error) {
		close(entered)
		<-ctx.Done()
		close(childCanceled)
		return Response{}, ctx.Err()
	}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := coordinator.ask(ctx, testCall("cancel"), testInput())
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	select {
	case <-childCanceled:
	case <-time.After(time.Second):
		t.Fatal("responder child was not canceled")
	}
}

func TestCoordinatorAllowsBoundedConcurrentRouting(t *testing.T) {
	entered := make(chan session.ToolCallID, 2)
	release := make(chan struct{})
	coordinator := coordinatorWithResponder(t, ResponderFunc(func(_ context.Context, request Request) (Response, error) {
		entered <- request.ToolCallID
		<-release
		return Response{Kind: ResponseDismissed}, nil
	}), func(limits *Limits) {
		limits.MaxInFlight = 2
		limits.MaxWait = time.Second
	})
	done := make(chan error, 2)
	for _, id := range []string{"overlap-a", "overlap-b"} {
		go func(id string) {
			_, err := coordinator.ask(context.Background(), testCall(id), testInput())
			done <- err
		}(id)
	}
	seen := make(map[session.ToolCallID]bool)
	for range 2 {
		select {
		case id := <-entered:
			seen[id] = true
		case <-time.After(time.Second):
			t.Fatal("concurrent responder did not enter")
		}
	}
	if !seen["overlap-a"] || !seen["overlap-b"] {
		t.Fatalf("routed call IDs = %#v", seen)
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCoordinatorSanitizesResponderFailuresAndPanics(t *testing.T) {
	tests := map[string]Responder{
		"raw error": ResponderFunc(func(context.Context, Request) (Response, error) {
			return Response{}, errors.New("synthetic-host-private-detail")
		}),
		"wrapped canceled": ResponderFunc(func(context.Context, Request) (Response, error) {
			return Response{}, fmt.Errorf("synthetic wrapper: %w", context.Canceled)
		}),
		"wrapped deadline": ResponderFunc(func(context.Context, Request) (Response, error) {
			return Response{}, fmt.Errorf("synthetic wrapper: %w", context.DeadlineExceeded)
		}),
		"panic": ResponderFunc(func(context.Context, Request) (Response, error) {
			panic("synthetic-panic-private-detail")
		}),
	}
	for name, responder := range tests {
		t.Run(name, func(t *testing.T) {
			coordinator := coordinatorWithResponder(t, responder, nil)
			_, err := coordinator.ask(context.Background(), testCall(name), testInput())
			if err == nil || err.Error() != errResponderOperation.Error() {
				t.Fatalf("error = %v", err)
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("responder error retained context identity: %v", err)
			}
		})
	}
}

func TestCoordinatorCloseDeadlineAndLaterSuccess(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	coordinator := coordinatorWithResponder(t, ResponderFunc(func(context.Context, Request) (Response, error) {
		close(entered)
		<-release
		return Response{Kind: ResponseDismissed}, nil
	}), func(limits *Limits) { limits.MaxWait = 20 * time.Millisecond })
	go func() { _, _ = coordinator.ask(context.Background(), testCall("close"), testInput()) }()
	<-entered
	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := coordinator.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close error = %v", err)
	}
	preCanceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := coordinator.ask(preCanceled, testCall("closing"), testInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("closing admission error = %v", err)
	}
	close(release)
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
}

func TestCoordinatorCompletionBoundaryIndependentOfSelectOrdering(t *testing.T) {
	for name, offset := range map[string]time.Duration{
		"strictly before": -time.Nanosecond,
		"exactly at":      0,
		"after":           time.Nanosecond,
	} {
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			coordinator := coordinatorWithResponder(t, ResponderFunc(func(context.Context, Request) (Response, error) {
				close(entered)
				<-release
				return Response{Kind: ResponseSelected, SelectedOption: 1}, nil
			}), func(limits *Limits) { limits.MaxWait = 5 * time.Second })
			clock := newBlockedClock()
			coordinator.clock = clock
			coordinator.released = make(chan struct{}, 1)
			done := make(chan Result, 1)
			go func() {
				result, _ := coordinator.ask(context.Background(), testCall(name), testInput())
				done <- result
			}()
			<-entered
			<-clock.timerRequested
			clock.set(clock.base.Add(5*time.Second + offset))
			close(release)
			waitRelease(t, coordinator) // publication precedes capacity release
			clock.fire()
			close(clock.allowTimer)
			select {
			case result := <-done:
				want := StatusTimedOut
				if offset < 0 {
					want = StatusSelected
				}
				if result.Status != want {
					t.Fatalf("result=%#v want=%s", result, want)
				}
			case <-time.After(time.Second):
				t.Fatal("ask did not classify ready signals")
			}
		})
	}
}

func TestCoordinatorMaxWaitIncludesSetupTime(t *testing.T) {
	release := make(chan struct{})
	coordinator := coordinatorWithResponder(t, ResponderFunc(func(context.Context, Request) (Response, error) {
		<-release
		return Response{Kind: ResponseDismissed}, nil
	}), func(limits *Limits) { limits.MaxWait = 5 * time.Second })
	clock := newDelayedTimerClock(6 * time.Second)
	coordinator.clock = clock
	coordinator.released = make(chan struct{}, 1)
	result, err := coordinator.ask(context.Background(), testCall("delayed-setup"), testInput())
	close(release)
	waitRelease(t, coordinator)
	if err != nil || result.Status != StatusTimedOut {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if clock.requestedDeadline != clock.base.Add(5*time.Second) {
		t.Fatalf("timer deadline=%v want=%v", clock.requestedDeadline, clock.base.Add(5*time.Second))
	}
}

func waitRelease(t *testing.T, coordinator *coordinator) {
	t.Helper()
	select {
	case <-coordinator.released:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not release capacity")
	}
}

type delayedTimerClock struct {
	base              time.Time
	now               time.Time
	delay             time.Duration
	requestedDeadline time.Time
}

func newDelayedTimerClock(delay time.Duration) *delayedTimerClock {
	base := time.Now()
	return &delayedTimerClock{base: base, now: base, delay: delay}
}

func (clock *delayedTimerClock) Now() time.Time { return clock.now }

func (clock *delayedTimerClock) NewDeadlineTimer(deadline time.Time) coordinatorTimer {
	clock.requestedDeadline = deadline
	clock.now = clock.base.Add(clock.delay)
	ready := make(chan time.Time, 1)
	ready <- clock.now
	return blockedTimer{ready}
}

type blockedClock struct {
	mu             sync.Mutex
	base           time.Time
	now            time.Time
	timer          chan time.Time
	timerRequested chan struct{}
	allowTimer     chan struct{}
	requestOnce    sync.Once
}

func newBlockedClock() *blockedClock {
	base := time.Now()
	return &blockedClock{
		base: base, now: base, timer: make(chan time.Time, 1),
		timerRequested: make(chan struct{}), allowTimer: make(chan struct{}),
	}
}

func (clock *blockedClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *blockedClock) set(now time.Time) {
	clock.mu.Lock()
	clock.now = now
	clock.mu.Unlock()
}

func (clock *blockedClock) NewDeadlineTimer(time.Time) coordinatorTimer {
	clock.requestOnce.Do(func() { close(clock.timerRequested) })
	<-clock.allowTimer
	return blockedTimer{clock.timer}
}

func (clock *blockedClock) fire() { clock.timer <- clock.Now() }

type blockedTimer struct{ channel <-chan time.Time }

func (timer blockedTimer) C() <-chan time.Time { return timer.channel }
func (timer blockedTimer) Stop() bool          { return false }
