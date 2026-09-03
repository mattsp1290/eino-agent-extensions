package pythonrepl

import (
	"context"
	"sync"
)

type ownerKey struct {
	sessionID   string
	workspaceID string
}

type gateWaiter struct {
	ready   chan struct{}
	granted bool
}

type operationGate struct {
	mu      sync.Mutex
	active  bool
	waiters []*gateWaiter
	maximum int
}

func newOperationGate(maximum int) *operationGate {
	return &operationGate{maximum: maximum}
}

func (gate *operationGate) acquire(ctx context.Context) (func(), error) {
	gate.mu.Lock()
	if !gate.active {
		gate.active = true
		gate.mu.Unlock()
		return gate.release, nil
	}
	if len(gate.waiters) >= gate.maximum {
		gate.mu.Unlock()
		return nil, errQueueFull
	}
	waiter := &gateWaiter{ready: make(chan struct{})}
	gate.waiters = append(gate.waiters, waiter)
	gate.mu.Unlock()

	select {
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			gate.release()
			return nil, err
		}
		return gate.release, nil
	case <-ctx.Done():
		gate.mu.Lock()
		if waiter.granted {
			gate.mu.Unlock()
			gate.release()
			return nil, ctx.Err()
		}
		for index, current := range gate.waiters {
			if current == waiter {
				gate.waiters = append(gate.waiters[:index], gate.waiters[index+1:]...)
				break
			}
		}
		gate.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (gate *operationGate) release() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.waiters) == 0 {
		gate.active = false
		return
	}
	waiter := gate.waiters[0]
	gate.waiters = gate.waiters[1:]
	waiter.granted = true
	close(waiter.ready)
}

type ownerSession struct {
	key           ownerKey
	workspaceRoot string
	gate          *operationGate
	lifecycle     context.Context
	cancel        context.CancelFunc

	venv          *virtualEnvironment
	venvInvalid   bool
	runner        *runnerProcess
	generation    uint64
	pendingReason string
	quarantined   bool
	closed        bool
}

func newOwnerSession(key ownerKey, root string, maximumQueue int) *ownerSession {
	lifecycle, cancel := context.WithCancel(context.Background())
	return &ownerSession{
		key: key, workspaceRoot: root, gate: newOperationGate(maximumQueue),
		lifecycle: lifecycle, cancel: cancel,
	}
}

func (session *ownerSession) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(session.lifecycle, cancel)
	return ctx, func() { stop(); cancel() }
}
