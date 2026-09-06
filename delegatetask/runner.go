package delegatetask

import (
	"context"

	"github.com/mattsp1290/eino-agent/session"
)

// Request is the defensive, data-only value delivered to Runner. WorkspaceID
// and WorkspaceRoot are authoritative Eino routing values and may be empty.
// They do not grant access; Runner must validate them under host policy.
type Request struct {
	SessionID     session.ID
	RunID         session.RunID
	ToolCallID    session.ToolCallID
	WorkspaceID   string
	WorkspaceRoot string
	Task          string
	Profile       string
}

// ResponseStatus identifies one normal host-declared task outcome.
type ResponseStatus string

const (
	ResponseCompleted   ResponseStatus = "completed"
	ResponseFailed      ResponseStatus = "failed"
	ResponseRejected    ResponseStatus = "rejected"
	ResponseUnavailable ResponseStatus = "unavailable"
)

// Response is the host-only result returned by Runner. It intentionally has no
// JSON serialization contract. Failed and rejected are normal task outcomes;
// a non-nil Go error means the Runner integration itself failed.
type Response struct {
	Status ResponseStatus
	Output string
}

// Runner performs one delegated task under host-owned policy. Run may be
// called concurrently up to the mount's MaxInFlight limit. Implementations
// must be concurrency-safe and honor context cancellation.
type Runner interface {
	Run(context.Context, Request) (Response, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(context.Context, Request) (Response, error)

// Run calls fn.
func (fn RunnerFunc) Run(ctx context.Context, request Request) (Response, error) {
	return fn(ctx, request)
}

// Status identifies one normal durable, model-visible outcome.
type Status string

const (
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusRejected    Status = "rejected"
	StatusUnavailable Status = "unavailable"
	StatusTimedOut    Status = "timed_out"
)

// Result is the bounded durable result exposed to the parent model.
type Result struct {
	Status Status `json:"status"`
	Output string `json:"output,omitempty"`
}
