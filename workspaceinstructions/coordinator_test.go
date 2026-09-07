package workspaceinstructions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func testCoordinator(t *testing.T, mutate func(*Limits)) *coordinator {
	t.Helper()
	options := testOptions()
	if mutate != nil {
		mutate(&options.Limits)
	}
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	return newCoordinator(canonical)
}

func TestCoordinatorSaturationCountsWorkUntilExit(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	coordinator := testCoordinator(t, func(l *Limits) {
		l.MaxInFlight = 1
		l.MaxWait = 20 * time.Millisecond
	})
	firstDone := make(chan error, 1)
	go func() {
		_, err := coordinator.run(context.Background(), func(context.Context) (string, error) {
			close(entered)
			<-release
			return "late", nil
		})
		firstDone <- err
	}()
	<-entered
	if _, err := coordinator.run(context.Background(), func(context.Context) (string, error) { return "second", nil }); err == nil || err.Error() != providerError("saturated").Error() {
		t.Fatalf("saturation error = %v", err)
	}
	if err := <-firstDone; err == nil || err.Error() != providerError("deadline").Error() {
		t.Fatalf("deadline error = %v", err)
	}
	if _, err := coordinator.run(context.Background(), func(context.Context) (string, error) { return "third", nil }); err == nil || err.Error() != providerError("saturated").Error() {
		t.Fatalf("retained saturation error = %v", err)
	}
	close(release)
	waitCoordinatorLive(t, coordinator, 0)
	section, err := coordinator.run(context.Background(), func(context.Context) (string, error) { return "ready", nil })
	if err != nil || section != "ready" {
		t.Fatalf("released result = %q, %v", section, err)
	}
}

func TestCoordinatorParentCancellationWins(t *testing.T) {
	coordinator := testCoordinator(t, nil)
	entered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := coordinator.run(ctx, func(workCtx context.Context) (string, error) {
			close(entered)
			<-workCtx.Done()
			return "", workCtx.Err()
		})
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestCoordinatorRecoversInternalPanic(t *testing.T) {
	coordinator := testCoordinator(t, nil)
	_, err := coordinator.run(context.Background(), func(context.Context) (string, error) {
		panic("private panic detail")
	})
	if err == nil || err.Error() != providerError("internal").Error() || errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestCoordinatorCloseDeadlineAndIdempotence(t *testing.T) {
	coordinator := testCoordinator(t, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = coordinator.run(context.Background(), func(context.Context) (string, error) {
			close(entered)
			<-release
			return "", nil
		})
	}()
	<-entered
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := coordinator.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close = %v", err)
	}
	if _, err := coordinator.run(context.Background(), func(context.Context) (string, error) { return "", nil }); err == nil || err.Error() != providerError("closed").Error() {
		t.Fatalf("closed admission = %v", err)
	}
	close(release)
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatalf("repeated close = %v", err)
	}
}

func TestCoordinatorCloseCancellationIsSanitized(t *testing.T) {
	coordinator := testCoordinator(t, nil)
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := coordinator.run(context.Background(), func(workCtx context.Context) (string, error) {
			close(entered)
			<-workCtx.Done()
			return "", workCtx.Err()
		})
		result <- err
	}()
	<-entered
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := <-result
	if err == nil || err.Error() != providerError("closed").Error() || errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}
}

func TestCoordinatorCompletionBoundary(t *testing.T) {
	tests := []struct {
		name   string
		offset time.Duration
	}{
		{"strictly-before", -time.Nanosecond}, {"exactly-at", 0}, {"after", time.Nanosecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := testCoordinator(t, func(l *Limits) { l.MaxWait = 5 * time.Second })
			clock := newWorkspaceBlockedClock()
			coordinator.clock = clock
			entered := make(chan struct{})
			release := make(chan struct{})
			done := make(chan struct {
				section string
				err     error
			}, 1)
			go func() {
				section, err := coordinator.run(context.Background(), func(context.Context) (string, error) {
					close(entered)
					<-release
					return "section", nil
				})
				done <- struct {
					section string
					err     error
				}{section, err}
			}()
			<-entered
			<-clock.timerRequested
			clock.set(clock.base.Add(5*time.Second + test.offset))
			close(release)
			waitCoordinatorLive(t, coordinator, 0)
			clock.fire()
			close(clock.allowTimer)
			result := <-done
			if test.offset < 0 {
				if result.err != nil || result.section != "section" {
					t.Fatalf("result = %q, %v", result.section, result.err)
				}
			} else if result.err == nil || result.err.Error() != providerError("deadline").Error() {
				t.Fatalf("deadline result = %q, %v", result.section, result.err)
			}
		})
	}
}

func waitCoordinatorLive(t *testing.T, coordinator *coordinator, want int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		coordinator.mu.Lock()
		got := coordinator.live
		coordinator.mu.Unlock()
		if got == want {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("live = %d, want %d", got, want)
		}
	}
}

type workspaceBlockedClock struct {
	mu             sync.Mutex
	base           time.Time
	now            time.Time
	timer          chan time.Time
	timerRequested chan struct{}
	allowTimer     chan struct{}
	requestOnce    sync.Once
}

func newWorkspaceBlockedClock() *workspaceBlockedClock {
	base := time.Now()
	return &workspaceBlockedClock{
		base: base, now: base, timer: make(chan time.Time, 1),
		timerRequested: make(chan struct{}), allowTimer: make(chan struct{}),
	}
}

func (clock *workspaceBlockedClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *workspaceBlockedClock) set(now time.Time) {
	clock.mu.Lock()
	clock.now = now
	clock.mu.Unlock()
}

func (clock *workspaceBlockedClock) NewDeadlineTimer(time.Time) coordinatorTimer {
	clock.requestOnce.Do(func() { close(clock.timerRequested) })
	<-clock.allowTimer
	return workspaceBlockedTimer{clock.timer}
}

func (clock *workspaceBlockedClock) fire() { clock.timer <- clock.Now() }

type workspaceBlockedTimer struct{ channel <-chan time.Time }

func (timer workspaceBlockedTimer) C() <-chan time.Time { return timer.channel }
func (timer workspaceBlockedTimer) Stop() bool          { return false }
