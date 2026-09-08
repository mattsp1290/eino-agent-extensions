package commandguard

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent-extensions/toolresultredactor"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	"mvdan.cc/sh/v3/syntax"
)

func TestRuntimeCancellationDuringClaimedAnalysis(t *testing.T) {
	f := newIntegrationFixture(t)
	f.tool()
	entered, release := make(chan struct{}), make(chan struct{})
	o := integrationOptions()
	o.Limits.MaxInFlight = 1
	parse := func(r io.Reader, d Dialect) (*syntax.File, error) {
		close(entered)
		<-release
		return parseScript(r, d)
	}
	m, err := mountWithParser(context.Background(), f.registry, testComponent(""), o, parse)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.Deactivate()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	p := testPlan(t, f.registry, "guard-session")
	stream := scriptedStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{toolCall("cancel", "fixture-command", "script", "echo ok")})}, nil
	})
	h, err := f.orchestrator(stream).Start(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("analysis not reached")
	}
	if f.call("cancel").Status != session.ToolCallRunning {
		t.Fatal("analysis before durable claim")
	}
	if err := h.Interrupt(context.Background(), "fixture cancellation"); err != nil {
		t.Fatal(err)
	}
	// Cancellation must not detach the live parser or return its permit early.
	got, err := p.Guards()[0].Guard.GuardTool(context.Background(), guardRequest("fixture-command", "script", "echo ok"))
	if err != nil || got != denial(capacity) {
		t.Fatal(got, err)
	}
	close(release)
	select {
	case result := <-h.Done():
		if result.Status != session.RunInterrupted || !errors.Is(result.Error, context.Canceled) {
			t.Fatal(result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation failed to settle")
	}
	if f.executions.Load() != 0 || f.permissionCalls.Load() != 0 || f.call("cancel").Status != session.ToolCallInterrupted {
		t.Fatal("canceled call executed or did not settle")
	}
	// A malformed covered command skips parsing and demonstrates permit recovery.
	req := guardRequest("fixture-command", "wrong", "echo ok")
	got, err = p.Guards()[0].Guard.GuardTool(context.Background(), req)
	if err != nil || got != denial(invalidCommand) {
		t.Fatal("permit not recovered", got, err)
	}
	p.Release()
}
func TestRuntimeInternalFailureDoesNotLeak(t *testing.T) {
	for _, mode := range []string{"error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			f := newIntegrationFixture(t)
			f.tool()
			m, err := mountWithParser(context.Background(), f.registry, testComponent(""), integrationOptions(), func(io.Reader, Dialect) (*syntax.File, error) {
				if mode == "panic" {
					panic("SECRET_MARKER")
				}
				return nil, errors.New("SECRET_MARKER")
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				m.Deactivate()
				if err := m.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			next := f.run(toolCall("internal", "fixture-command", "script", "echo ok"))
			call := f.call("internal")
			if f.executions.Load() != 0 || f.permissionCalls.Load() != 0 || call.Status != session.ToolCallFailed || strings.Contains(string(call.Output)+call.Error+string(next), "SECRET_MARKER") {
				t.Fatal("internal failure escaped protection")
			}
			if !strings.Contains(string(call.Output)+call.Error, errInternal.Error()) {
				t.Fatal("fixed internal error missing")
			}
		})
	}
}
func TestRuntimeRedactionCannotUndoProtectedDenial(t *testing.T) {
	f := newIntegrationFixture(t)
	f.tool()
	f.guard(integrationOptions())
	// A host result transform attempts to replace the protected authority marker.
	f.mount("result-transform", func(_ context.Context, r *composition.Registrar) error {
		return extension.OnTransform(r.Extensions(), runtime.ToolResultTransformPoint, extension.Registration{ID: "rewrite", Scope: extension.GlobalScope()}, func(_ context.Context, in runtime.ToolResultTransform) (runtime.ToolResultTransform, error) {
			in.Result.Metadata = map[string]string{"permission_status": "allowed"}
			return in, nil
		})
	})
	c := fixtureComponent("redactor")
	c.Artifact.ConfigHash = ""
	m, err := toolresultredactor.Mount(context.Background(), f.registry, c, toolresultredactor.Options{AdditionalPatterns: []toolresultredactor.Pattern{{ID: "denial-text", Expression: "rule-match"}}, Limits: toolresultredactor.Limits{MaxFieldBytes: 4096, MaxStructuredBytes: 8192, MaxStructuredDepth: 16, MaxStructuredNodes: 256, MaxAttachments: 8, MaxMetadataEntries: 8, MaxMatchesPerField: 16, MaxPatterns: 8, MaxPatternBytes: 128}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.Deactivate()
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	f.run(toolCall("redacted", "fixture-command", "script", "blocked"))
	if f.call("redacted").Status != session.ToolCallFailed || f.executions.Load() != 0 || f.permissionCalls.Load() != 0 {
		t.Fatal("protected authority widened")
	}
	select {
	case n := <-f.notices:
		if n.Result.Metadata["permission_status"] != "denied" {
			t.Fatal("denial authority changed")
		}
	case <-time.After(time.Second):
		t.Fatal("notice missing")
	}
}
