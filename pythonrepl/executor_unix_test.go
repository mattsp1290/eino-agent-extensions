//go:build linux || darwin

package pythonrepl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/tools"
)

func TestRuntimeOwnerValidatesDurableIdentityAndCanonicalRoot(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		call    runtime.ToolCall
		context runtime.ToolContext
		valid   bool
	}{
		"missing call session":   {context: runtime.ToolContext{WorkspaceID: "w", WorkspaceRoot: root}},
		"missing workspace ID":   {call: runtime.ToolCall{SessionID: "s"}, context: runtime.ToolContext{WorkspaceRoot: root}},
		"missing workspace root": {call: runtime.ToolCall{SessionID: "s"}, context: runtime.ToolContext{WorkspaceID: "w"}},
		"turn mismatch": {
			call:    runtime.ToolCall{SessionID: "s"},
			context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "other"}, WorkspaceID: "w", WorkspaceRoot: root},
		},
		"relative root": {
			call: runtime.ToolCall{SessionID: "s"}, context: runtime.ToolContext{WorkspaceID: "w", WorkspaceRoot: "relative"},
		},
		"canonical symlink": {
			call:    runtime.ToolCall{SessionID: "s"},
			context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "s"}, WorkspaceID: "w", WorkspaceRoot: link},
			valid:   true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			owner, gotRoot, err := runtimeOwner(test.call, test.context)
			if !test.valid {
				if err == nil {
					t.Fatalf("invalid owner accepted: %#v %q", owner, gotRoot)
				}
				return
			}
			if err != nil || owner != (ownerKey{sessionID: "s", workspaceID: "w"}) || gotRoot != resolvedRoot {
				t.Fatalf("owner=%#v root=%q err=%v", owner, gotRoot, err)
			}
		})
	}
}

func TestExecuteClassifiesPackageTimeoutAndParentCancellation(t *testing.T) {
	public := testOptions(t)
	public.Limits.DefaultTimeout = time.Second
	options, err := canonicalize(public)
	if err != nil {
		t.Fatal(err)
	}
	manager := newManager(options)
	deferManagerClose(t, manager)
	workspace := t.TempDir()
	definition := definitions(options, manager)[0]
	tool, err := tools.Materialize(context.Background(), definition, runtime.ToolScopeContext{
		SessionID: "executor", WorkspaceID: "workspace", WorkspaceRoot: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := tool.InputDecoder.DecodeToolInput(context.Background(), []byte(`{"code":"import time; time.sleep(30)"}`))
	if err != nil {
		t.Fatal(err)
	}
	call := runtime.ToolCall{
		ID: "executor-call", SessionID: "executor", Name: ExecuteToolName, Input: input,
		Context: runtime.ToolContext{Turn: runtime.BoundedTurnMetadata{SessionID: "executor"}, WorkspaceID: "workspace", WorkspaceRoot: workspace},
	}
	if _, err := tool.Executor.Execute(context.Background(), call); !errors.Is(err, errExecutionTimedOut) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("package timeout classification=%v", err)
	}

	parent, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	call.ID = "executor-cancel-call"
	go func() {
		_, executeErr := tool.Executor.Execute(parent, call)
		done <- executeErr
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || errors.Is(err, errExecutionTimedOut) {
			t.Fatalf("parent cancellation classification=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent cancellation did not settle")
	}
}
