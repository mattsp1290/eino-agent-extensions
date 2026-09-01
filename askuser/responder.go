package askuser

import (
	"context"

	"github.com/mattsp1290/eino-agent/session"
)

// CustomOptionLabel is host display metadata for the automatic free-form
// choice. It is not model input and cannot be changed per question.
const CustomOptionLabel = "Other (write your own answer)"

// Option is one fixed, ordered answer offered to a person.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Request is the data-only value delivered to a host Responder. Options is a
// defensive copy. The durable IDs let a concurrent host route the response.
type Request struct {
	SessionID   session.ID
	RunID       session.RunID
	ToolCallID  session.ToolCallID
	Question    string
	Options     []Option
	AllowCustom bool
	CustomLabel string
}

// ResponseKind identifies a host interaction outcome.
type ResponseKind string

const (
	// ResponseSelected chooses one fixed option by one-based index.
	ResponseSelected ResponseKind = "selected"
	// ResponseCustom supplies a bounded free-form answer.
	ResponseCustom ResponseKind = "custom"
	// ResponseDismissed means the person declined the question.
	ResponseDismissed ResponseKind = "dismissed"
	// ResponseUnavailable means the host cannot present the question.
	ResponseUnavailable ResponseKind = "unavailable"
)

// Response is returned by a host Responder. SelectedOption is one-based.
type Response struct {
	Kind           ResponseKind
	SelectedOption int
	CustomAnswer   string
}

// Responder presents one question and routes its response. Respond may be
// called concurrently up to the mount's MaxInFlight limit. Implementations
// must honor context cancellation and be concurrency-safe.
type Responder interface {
	Respond(context.Context, Request) (Response, error)
}

// ResponderFunc adapts a function to Responder.
type ResponderFunc func(context.Context, Request) (Response, error)

// Respond calls fn.
func (fn ResponderFunc) Respond(ctx context.Context, request Request) (Response, error) {
	return fn(ctx, request)
}

// Status is one normal structured tool outcome.
type Status string

const (
	StatusSelected    Status = "selected"
	StatusCustom      Status = "custom"
	StatusDismissed   Status = "dismissed"
	StatusUnavailable Status = "unavailable"
	StatusTimedOut    Status = "timed_out"
)

// Result is the durable, model-visible ask_user result.
type Result struct {
	Status         Status `json:"status"`
	Answer         string `json:"answer,omitempty"`
	SelectedOption int    `json:"selected_option,omitempty"`
}
