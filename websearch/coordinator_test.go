package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func testCall() runtime.ToolCall {
	return runtime.ToolCall{ID: "call", SessionID: "session", RunID: "run", Name: ToolName}
}

func testToolContext() runtime.ToolContext {
	return runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "session", RunID: "run"}}
}

func TestCoordinatorCallsSearcherOnceWithCanonicalQuery(t *testing.T) {
	var calls atomic.Int32
	options := testOptions()
	options.Searcher = SearcherFunc(func(_ context.Context, query string) ([]Source, error) {
		calls.Add(1)
		if query != "canonical query" {
			t.Fatalf("query=%q", query)
		}
		return []Source{{Title: "t", URL: "https://example.test/", Snippet: "s"}}, nil
	})
	canonical, _ := canonicalize(options)
	raw, err := newCoordinator(canonical).search(context.Background(), testCall(), testToolContext(), toolInput{Query: "canonical query"})
	if err != nil || calls.Load() != 1 || string(raw) != `{"results":[{"title":"t","url":"https://example.test/","snippet":"s"}]}` {
		t.Fatalf("raw=%s calls=%d err=%v", raw, calls.Load(), err)
	}
}

func TestCoordinatorRejectsInvalidCallAndTurnIdentity(t *testing.T) {
	coordinator := newCoordinator(canonicalOptionsForTest(t))
	for name, fixture := range map[string]struct {
		call runtime.ToolCall
		ctx  runtime.ToolContext
	}{
		"empty call":       {},
		"session mismatch": {call: testCall(), ctx: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "other"}}},
		"run mismatch":     {call: testCall(), ctx: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{RunID: "other"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := coordinator.search(context.Background(), fixture.call, fixture.ctx, toolInput{Query: "q"}); err == nil {
				t.Fatal("invalid execution accepted")
			}
		})
	}
}

func TestCoordinatorSanitizesBackendErrorsPanicsAndFalseContextErrors(t *testing.T) {
	fixtures := map[string]Searcher{
		"error": SearcherFunc(func(context.Context, string) ([]Source, error) { return nil, errors.New("SECRET BACKEND BODY") }),
		"panic": SearcherFunc(func(context.Context, string) ([]Source, error) { panic("SECRET PANIC") }),
		"false canceled": SearcherFunc(func(context.Context, string) ([]Source, error) {
			return nil, fmt.Errorf("SECRET: %w", context.Canceled)
		}),
		"false deadline": SearcherFunc(func(context.Context, string) ([]Source, error) {
			return nil, fmt.Errorf("SECRET: %w", context.DeadlineExceeded)
		}),
	}
	for name, searcher := range fixtures {
		t.Run(name, func(t *testing.T) {
			options := testOptions()
			options.Searcher = searcher
			canonical, _ := canonicalize(options)
			_, err := newCoordinator(canonical).search(context.Background(), testCall(), testToolContext(), toolInput{Query: "secret query"})
			if !errors.Is(err, errSearchOperation) || err.Error() != errSearchOperation.Error() {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestCoordinatorParentCancellationAndPackageDeadline(t *testing.T) {
	for name, parentCancel := range map[string]bool{"parent": true, "package": false} {
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{})
			observed := make(chan error, 1)
			options := testOptions()
			options.Limits.MaxWait = 30 * time.Millisecond
			options.Searcher = SearcherFunc(func(ctx context.Context, _ string) ([]Source, error) {
				close(entered)
				<-ctx.Done()
				observed <- ctx.Err()
				return nil, ctx.Err()
			})
			canonical, _ := canonicalize(options)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, err := newCoordinator(canonical).search(ctx, testCall(), testToolContext(), toolInput{Query: "q"})
				done <- err
			}()
			<-entered
			if parentCancel {
				cancel()
			} else {
				defer cancel()
			}
			err := <-done
			if parentCancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("parent err=%v", err)
			}
			if !parentCancel && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("package err=%v", err)
			}
			if childErr := <-observed; parentCancel && !errors.Is(childErr, context.Canceled) || !parentCancel && !errors.Is(childErr, context.DeadlineExceeded) {
				t.Fatalf("child err=%v", childErr)
			}
		})
	}
}

func TestCoordinatorSaturationDoesNotQueueAndTimedOutCallRetainsSlot(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	options := testOptions()
	options.Limits.MaxInFlight = 1
	options.Limits.MaxWait = 20 * time.Millisecond
	options.Searcher = SearcherFunc(func(context.Context, string) ([]Source, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return nil, nil
	})
	canonical, _ := canonicalize(options)
	coordinator := newCoordinator(canonical)
	first := make(chan error, 1)
	go func() {
		_, err := coordinator.search(context.Background(), testCall(), testToolContext(), toolInput{Query: "one"})
		first <- err
	}()
	<-entered
	if err := <-first; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first err=%v", err)
	}
	call := testCall()
	call.ID = "second"
	started := time.Now()
	if _, err := coordinator.search(context.Background(), call, testToolContext(), toolInput{Query: "two"}); !errors.Is(err, errSearchCapacity) {
		t.Fatalf("second err=%v", err)
	}
	if calls.Load() != 1 || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("calls=%d elapsed=%s", calls.Load(), time.Since(started))
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		coordinator.mu.Lock()
		live := coordinator.live
		coordinator.mu.Unlock()
		if live == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("callback did not release slot")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCoordinatorConcurrencyCeilingAndClose(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var current atomic.Int32
	var peak atomic.Int32
	options := testOptions()
	options.Limits.MaxInFlight = 2
	options.Searcher = SearcherFunc(func(ctx context.Context, _ string) ([]Source, error) {
		live := current.Add(1)
		defer current.Add(-1)
		for {
			old := peak.Load()
			if live <= old || peak.CompareAndSwap(old, live) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	canonical, _ := canonicalize(options)
	coordinator := newCoordinator(canonical)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			call := testCall()
			call.ID = session.ToolCallID(fmt.Sprintf("call-%d", index))
			_, _ = coordinator.search(context.Background(), call, testToolContext(), toolInput{Query: "q"})
		}(i)
	}
	<-entered
	<-entered
	third := testCall()
	third.ID = "third"
	if _, err := coordinator.search(context.Background(), third, testToolContext(), toolInput{Query: "q"}); !errors.Is(err, errSearchCapacity) {
		t.Fatalf("third err=%v", err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()
	if peak.Load() != 2 {
		t.Fatalf("peak=%d", peak.Load())
	}
	if _, err := coordinator.search(context.Background(), third, testToolContext(), toolInput{Query: "q"}); !errors.Is(err, errSearchCapacity) {
		t.Fatalf("post-close err=%v", err)
	}
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorNonCooperativeCloseTimeoutThenQuiescence(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	options := testOptions()
	options.Limits.MaxWait = 10 * time.Millisecond
	options.Searcher = SearcherFunc(func(context.Context, string) ([]Source, error) {
		close(entered)
		<-release
		return nil, nil
	})
	canonical, _ := canonicalize(options)
	coordinator := newCoordinator(canonical)
	done := make(chan error, 1)
	go func() {
		_, err := coordinator.search(context.Background(), testCall(), testToolContext(), toolInput{Query: "q"})
		done <- err
	}()
	<-entered
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("search err=%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err := coordinator.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close err=%v", err)
	}
	close(release)
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorEncodedResultOwnsSeparateSlice(t *testing.T) {
	host := []Source{{Title: "title", URL: "https://example.test/", Snippet: "snippet"}}
	options := testOptions()
	options.Searcher = SearcherFunc(func(context.Context, string) ([]Source, error) { return host, nil })
	canonical, _ := canonicalize(options)
	raw, err := newCoordinator(canonical).search(context.Background(), testCall(), testToolContext(), toolInput{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	result.Results[0].Title = "changed"
	if host[0].Title != "title" {
		t.Fatal("bounded result reused host backing array")
	}
}

type lateCompletionClock struct {
	mu       sync.Mutex
	start    time.Time
	deadline time.Time
	calls    int
}

func (clock *lateCompletionClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.calls++
	if clock.calls == 1 {
		return clock.start
	}
	return clock.deadline
}

func (clock *lateCompletionClock) NewDeadlineTimer(deadline time.Time) coordinatorTimer {
	return realCoordinatorTimer{timer: time.NewTimer(time.Until(deadline))}
}

func TestCoordinatorDiscardsSuccessCompletedAtPackageDeadline(t *testing.T) {
	options := testOptions()
	options.Limits.MaxWait = 20 * time.Millisecond
	options.Searcher = SearcherFunc(func(context.Context, string) ([]Source, error) {
		return []Source{{Title: "late", URL: "https://example.test/late", Snippet: "discard"}}, nil
	})
	canonical, _ := canonicalize(options)
	coordinator := newCoordinator(canonical)
	start := time.Now()
	coordinator.clock = &lateCompletionClock{start: start, deadline: start.Add(options.Limits.MaxWait)}
	raw, err := coordinator.search(context.Background(), testCall(), testToolContext(), toolInput{Query: "q"})
	if raw != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
}
