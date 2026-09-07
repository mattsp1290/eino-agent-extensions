package workspaceinstructions

import (
	"context"

	"github.com/mattsp1290/eino-agent/session"
)

// Request identifies the durable model call whose workspace must be resolved.
type Request struct {
	SessionID  session.ID
	RunID      session.RunID
	EpochID    session.EpochID
	Attempt    int
	Step       int
	AgentName  string
	ProviderID string
	ModelID    string
}

// Workspace is the host's trust decision and admitted directory range. Root
// and Boundary must be absolute directories. An empty Boundary means Root.
type Workspace struct {
	Root     string
	Boundary string
	Trusted  bool
}

// Resolver maps one durable model call to its host-admitted workspace.
type Resolver interface {
	ResolveWorkspace(context.Context, Request) (Workspace, error)
}

// ResolverFunc adapts a function to Resolver.
type ResolverFunc func(context.Context, Request) (Workspace, error)

// ResolveWorkspace calls f.
func (f ResolverFunc) ResolveWorkspace(ctx context.Context, request Request) (Workspace, error) {
	return f(ctx, request)
}
