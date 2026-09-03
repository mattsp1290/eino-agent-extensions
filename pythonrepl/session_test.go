package pythonrepl

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestOperationGateFIFOQueueBoundAndCancellation(t *testing.T) {
	gate := newOperationGate(2)
	release, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []int
	done := make(chan struct{}, 2)
	start := func(id int, ctx context.Context) {
		go func() {
			releaseNext, acquireErr := gate.acquire(ctx)
			if acquireErr == nil {
				mu.Lock()
				order = append(order, id)
				mu.Unlock()
				releaseNext()
			}
			done <- struct{}{}
		}()
	}
	start(1, context.Background())
	for {
		gate.mu.Lock()
		queued := len(gate.waiters)
		gate.mu.Unlock()
		if queued == 1 {
			break
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	start(2, canceled)
	for {
		gate.mu.Lock()
		queued := len(gate.waiters)
		gate.mu.Unlock()
		if queued == 2 {
			break
		}
	}
	if _, err := gate.acquire(context.Background()); !errors.Is(err, errQueueFull) {
		t.Fatalf("queue overflow = %v", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled waiter stuck")
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first waiter stuck")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 1 || order[0] != 1 {
		t.Fatalf("execution order=%v", order)
	}
}
