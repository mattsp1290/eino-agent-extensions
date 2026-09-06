package delegatetask

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func testInput() toolInput { return toolInput{Task: "inspect synthetic code", Profile: "read-only"} }

func coordinatorWithRunner(t *testing.T, runner Runner, mutate func(*Limits)) *coordinator {
	t.Helper()
	options := testOptions()
	options.Runner = runner
	if mutate != nil {
		mutate(&options.Limits)
	}
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	return newCoordinator(canonical)
}

func TestCoordinatorForwardsExactRequestAndWorkspace(t *testing.T) {
	var observed Request
	c := coordinatorWithRunner(t, RunnerFunc(func(_ context.Context, request Request) (Response, error) {
		observed = request
		request.Task = "host mutation"
		return Response{Status: ResponseCompleted, Output: "done"}, nil
	}), nil)
	call := testCall("call")
	executionContext := agentruntime.ToolContext{
		Turn:        agentruntime.BoundedTurnMetadata{SessionID: call.SessionID, RunID: call.RunID},
		WorkspaceID: "workspace-id", WorkspaceRoot: "/synthetic/workspace",
	}
	result, err := c.run(context.Background(), call, executionContext, testInput())
	if err != nil || result != (Result{Status: StatusCompleted, Output: "done"}) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	want := Request{SessionID: "session", RunID: "run", ToolCallID: "call", WorkspaceID: "workspace-id", WorkspaceRoot: "/synthetic/workspace", Task: "inspect synthetic code", Profile: "read-only"}
	if observed != want {
		t.Fatalf("request=%#v want=%#v", observed, want)
	}
	var empty Request
	c = coordinatorWithRunner(t, RunnerFunc(func(_ context.Context, request Request) (Response, error) {
		empty = request
		return Response{Status: ResponseRejected}, nil
	}), nil)
	if _, err := c.run(context.Background(), testCall("empty"), agentruntime.ToolContext{}, testInput()); err != nil {
		t.Fatal(err)
	}
	if empty.WorkspaceID != "" || empty.WorkspaceRoot != "" {
		t.Fatalf("empty workspace changed: %#v", empty)
	}
}

func TestCoordinatorRejectsIdentityBeforeAdmission(t *testing.T) {
	var calls atomic.Int32
	c := coordinatorWithRunner(t, RunnerFunc(func(context.Context, Request) (Response, error) {
		calls.Add(1)
		return Response{Status: ResponseCompleted}, nil
	}), nil)
	tests := map[string]struct {
		call agentruntime.ToolCall
		ctx  agentruntime.ToolContext
	}{
		"missing call":     {},
		"session mismatch": {call: testCall("a"), ctx: agentruntime.ToolContext{Turn: agentruntime.BoundedTurnMetadata{SessionID: "other"}}},
		"run mismatch":     {call: testCall("b"), ctx: agentruntime.ToolContext{Turn: agentruntime.BoundedTurnMetadata{RunID: "other"}}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := c.run(context.Background(), test.call, test.ctx, testInput()); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("runner calls=%d", calls.Load())
	}
}

func TestCoordinatorMapsNormalResponsesAndValidatesOutput(t *testing.T) {
	valid := map[ResponseStatus]Status{
		ResponseCompleted: StatusCompleted, ResponseFailed: StatusFailed,
		ResponseRejected: StatusRejected, ResponseUnavailable: StatusUnavailable,
	}
	for response, want := range valid {
		t.Run(string(response), func(t *testing.T) {
			c := coordinatorWithRunner(t, RunnerFunc(func(context.Context, Request) (Response, error) {
				return Response{Status: response, Output: " summary\n"}, nil
			}), nil)
			got, err := c.run(context.Background(), testCall(string(response)), agentruntime.ToolContext{}, testInput())
			if err != nil || got.Status != want || got.Output != " summary\n" {
				t.Fatalf("result=%#v err=%v", got, err)
			}
		})
	}
	tests := map[string]Response{
		"invalid status": {Status: "timed_out"},
		"invalid utf8":   {Status: ResponseCompleted, Output: string([]byte{0xff})},
		"nul":            {Status: ResponseCompleted, Output: "bad\x00output"},
		"oversized":      {Status: ResponseCompleted, Output: strings.Repeat("x", testLimits().MaxResultBytes+1)},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			c := coordinatorWithRunner(t, RunnerFunc(func(context.Context, Request) (Response, error) { return response, nil }), nil)
			if _, err := c.run(context.Background(), testCall(name), agentruntime.ToolContext{}, testInput()); !errors.Is(err, errRunnerOperation) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCoordinatorSanitizesRunnerFailuresAndPanics(t *testing.T) {
	private := "synthetic-host-private-detail"
	tests := map[string]Runner{
		"error": RunnerFunc(func(context.Context, Request) (Response, error) { return Response{}, errors.New(private) }),
		"wrapped canceled": RunnerFunc(func(context.Context, Request) (Response, error) {
			return Response{}, fmt.Errorf("%s: %w", private, context.Canceled)
		}),
		"panic": RunnerFunc(func(context.Context, Request) (Response, error) { panic(private) }),
	}
	for name, runner := range tests {
		t.Run(name, func(t *testing.T) {
			c := coordinatorWithRunner(t, runner, nil)
			_, err := c.run(context.Background(), testCall(name), agentruntime.ToolContext{}, testInput())
			if !errors.Is(err, errRunnerOperation) || strings.Contains(err.Error(), private) || errors.Is(err, context.Canceled) {
				t.Fatalf("unsanitized error=%v", err)
			}
		})
	}
}

func TestCoordinatorCapacityRetainedUntilRunnerExit(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	c := coordinatorWithRunner(t, RunnerFunc(func(context.Context, Request) (Response, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return Response{Status: ResponseCompleted}, nil
	}), func(l *Limits) { l.MaxInFlight = 1; l.MaxWait = 20 * time.Millisecond })
	first := make(chan Result, 1)
	go func() {
		result, _ := c.run(context.Background(), testCall("first"), agentruntime.ToolContext{}, testInput())
		first <- result
	}()
	<-entered
	second, err := c.run(context.Background(), testCall("second"), agentruntime.ToolContext{}, testInput())
	if err != nil || second.Status != StatusUnavailable || calls.Load() != 1 {
		t.Fatalf("saturation result=%#v calls=%d err=%v", second, calls.Load(), err)
	}
	if result := <-first; result.Status != StatusTimedOut {
		t.Fatalf("first=%#v", result)
	}
	third, err := c.run(context.Background(), testCall("third"), agentruntime.ToolContext{}, testInput())
	if err != nil || third.Status != StatusUnavailable || calls.Load() != 1 {
		t.Fatalf("retained result=%#v calls=%d err=%v", third, calls.Load(), err)
	}
	close(release)
	waitLive(t, c, 0)
	fourth, err := c.run(context.Background(), testCall("fourth"), agentruntime.ToolContext{}, testInput())
	if err != nil || fourth.Status != StatusCompleted || calls.Load() != 2 {
		t.Fatalf("released result=%#v calls=%d err=%v", fourth, calls.Load(), err)
	}
}

func TestCoordinatorAdmitsAtMostConfiguredConcurrentCallbacks(t *testing.T) {
	entered := make(chan session.ToolCallID, 2)
	release := make(chan struct{})
	c := coordinatorWithRunner(t, RunnerFunc(func(_ context.Context, request Request) (Response, error) {
		entered <- request.ToolCallID
		<-release
		return Response{Status: ResponseCompleted}, nil
	}), func(l *Limits) { l.MaxInFlight = 2 })
	done := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			_, err := c.run(context.Background(), testCall(id), agentruntime.ToolContext{}, testInput())
			done <- err
		}(id)
	}
	seen := map[session.ToolCallID]bool{}
	for range 2 {
		select {
		case id := <-entered:
			seen[id] = true
		case <-time.After(time.Second):
			t.Fatal("callback was not admitted")
		}
	}
	if !seen["one"] || !seen["two"] {
		t.Fatalf("seen=%#v", seen)
	}
	third, err := c.run(context.Background(), testCall("three"), agentruntime.ToolContext{}, testInput())
	if err != nil || third.Status != StatusUnavailable {
		t.Fatalf("third=%#v err=%v", third, err)
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCoordinatorParentCancellationWinsAndCancelsRunner(t *testing.T) {
	entered := make(chan struct{})
	childCanceled := make(chan struct{})
	c := coordinatorWithRunner(t, RunnerFunc(func(ctx context.Context, _ Request) (Response, error) {
		close(entered)
		<-ctx.Done()
		close(childCanceled)
		return Response{}, ctx.Err()
	}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.run(ctx, testCall("cancel"), agentruntime.ToolContext{}, testInput()); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	select {
	case <-childCanceled:
	case <-time.After(time.Second):
		t.Fatal("runner did not observe cancellation")
	}
}

func TestCoordinatorPreCanceledParentNeverAdmits(t *testing.T) {
	var calls atomic.Int32
	c := coordinatorWithRunner(t, RunnerFunc(func(context.Context, Request) (Response, error) {
		calls.Add(1)
		return Response{Status: ResponseCompleted}, nil
	}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.run(ctx, testCall("canceled"), agentruntime.ToolContext{}, testInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("runner calls=%d", calls.Load())
	}
}

func TestCoordinatorCloseDeadlineThenQuiesces(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	c := coordinatorWithRunner(t, RunnerFunc(func(context.Context, Request) (Response, error) {
		close(entered)
		<-release
		return Response{Status: ResponseCompleted}, nil
	}), func(l *Limits) { l.MaxWait = 20 * time.Millisecond })
	go func() { _, _ = c.run(context.Background(), testCall("close"), agentruntime.ToolContext{}, testInput()) }()
	<-entered
	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error=%v", err)
	}
	if result, err := c.run(context.Background(), testCall("closed"), agentruntime.ToolContext{}, testInput()); err != nil || result.Status != StatusUnavailable {
		t.Fatalf("post-close result=%#v err=%v", result, err)
	}
	close(release)
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var nilCoordinator *coordinator
	if err := nilCoordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorCloseCancelsActiveRunnerAndWaitsForExit(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	c := coordinatorWithRunner(t, RunnerFunc(func(ctx context.Context, _ Request) (Response, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return Response{}, ctx.Err()
	}), nil)
	callDone := make(chan error, 1)
	go func() {
		_, err := c.run(context.Background(), testCall("close-cancel"), agentruntime.ToolContext{}, testInput())
		callDone <- err
	}()
	<-entered
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("close returned before runner exit")
	}
	if err := <-callDone; !errors.Is(err, errRunnerOperation) {
		t.Fatalf("call error=%v", err)
	}
}

func TestCoordinatorNilCloseContextBeforeClosure(t *testing.T) {
	c := coordinatorWithRunner(t, RunnerFunc(testRunner), nil)
	if err := c.Close(nil); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestCoordinatorConcurrentCloseIsIdempotent(t *testing.T) {
	c := coordinatorWithRunner(t, RunnerFunc(testRunner), nil)
	errorsCh := make(chan error, 16)
	for range 16 {
		go func() { errorsCh <- c.Close(context.Background()) }()
	}
	for range 16 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCoordinatorCompletionBoundary(t *testing.T) {
	for name, offset := range map[string]time.Duration{"before": -time.Nanosecond, "at": 0, "after": time.Nanosecond} {
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			c := coordinatorWithRunner(t, RunnerFunc(func(context.Context, Request) (Response, error) {
				close(entered)
				<-release
				return Response{Status: ResponseCompleted}, nil
			}), func(l *Limits) { l.MaxWait = 5 * time.Second })
			clock := newBlockedClock()
			c.clock = clock
			done := make(chan Result, 1)
			go func() {
				result, _ := c.run(context.Background(), testCall(name), agentruntime.ToolContext{}, testInput())
				done <- result
			}()
			<-entered
			<-clock.timerRequested
			clock.set(clock.base.Add(5*time.Second + offset))
			close(release)
			waitLive(t, c, 0)
			clock.fire()
			close(clock.allowTimer)
			result := <-done
			want := StatusTimedOut
			if offset < 0 {
				want = StatusCompleted
			}
			if result.Status != want {
				t.Fatalf("result=%#v want=%s", result, want)
			}
		})
	}
}

func waitLive(t *testing.T, c *coordinator, want int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		got := c.live
		c.mu.Unlock()
		if got == want {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("live=%d want=%d", got, want)
		}
	}
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
	return &blockedClock{base: base, now: base, timer: make(chan time.Time, 1), timerRequested: make(chan struct{}), allowTimer: make(chan struct{})}
}

func (c *blockedClock) Now() time.Time    { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *blockedClock) set(now time.Time) { c.mu.Lock(); c.now = now; c.mu.Unlock() }
func (c *blockedClock) NewDeadlineTimer(time.Time) coordinatorTimer {
	c.requestOnce.Do(func() { close(c.timerRequested) })
	<-c.allowTimer
	return blockedTimer{c.timer}
}
func (c *blockedClock) fire() { c.timer <- c.Now() }

type blockedTimer struct{ channel <-chan time.Time }

func (t blockedTimer) C() <-chan time.Time { return t.channel }
func (t blockedTimer) Stop() bool          { return false }
