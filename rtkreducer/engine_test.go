//go:build linux || darwin

package rtkreducer

import (
	"context"
	"sync"
	"testing"
)

func TestCoordinatorCloseIsIdempotent(t *testing.T) {
	coordinator := fixtureCoordinator(t, 1)
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := coordinator.acquire(context.Background()); ok {
		t.Fatal("closed coordinator admitted work")
	}
}

func TestCoordinatorConcurrentCloseIsSafe(t *testing.T) {
	coordinator := fixtureCoordinator(t, 1)
	var wait sync.WaitGroup
	errors := make(chan error, 16)
	for index := 0; index < cap(errors); index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- coordinator.Close(context.Background())
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
}
