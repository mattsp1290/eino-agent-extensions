package toolresultredactor_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent-extensions/toolresultredactor"
	"github.com/mattsp1290/eino-agent/composition"
	"github.com/mattsp1290/eino-agent/config"
	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
	"github.com/mattsp1290/eino-agent/tools"
)

const fixtureMarker = "FIX_QWERTY"

func acceptanceLimits() toolresultredactor.Limits {
	return toolresultredactor.Limits{
		MaxFieldBytes: 2048, MaxStructuredBytes: 8192, MaxStructuredDepth: 16,
		MaxStructuredNodes: 256, MaxAttachments: 8, MaxMetadataEntries: 8,
		MaxMatchesPerField: 32, MaxPatterns: 8, MaxPatternBytes: 256,
	}
}

func TestMountedRedactorSanitizesSQLiteNextModelTurnAndFullNotice(t *testing.T) {
	result := runAcceptance(t, acceptanceCase{
		options: toolresultredactor.Options{
			Limits:             acceptanceLimits(),
			AdditionalPatterns: []toolresultredactor.Pattern{{ID: "fixture-marker", Expression: `FIX_[A-Z]{6}`}},
		},
		produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
			input.Result = runtime.ToolResult{
				Output:     "output before " + fixtureMarker + " after",
				Structured: json.RawMessage(`{"safe":"before FIX_QWERTY after","nested":["FIX_QWERTY",1]}`),
				Metadata:   map[string]string{"safe": "metadata " + fixtureMarker},
				Attachments: []runtime.Attachment{{
					ID: "id-" + fixtureMarker, MIMEType: "type/" + fixtureMarker,
					Name: "name-" + fixtureMarker, URL: "https://invalid/" + fixtureMarker,
					Metadata: map[string]string{"safe": "attachment " + fixtureMarker},
				}},
			}
			return input
		},
	})
	assertAbsent(t, result.durable, fixtureMarker, "durable tool output")
	assertAbsent(t, result.parts, fixtureMarker, "durable result part")
	assertAbsent(t, result.nextModelRequest, fixtureMarker, "next model request")
	assertAbsent(t, result.notice, fixtureMarker, "full settled notice")
	for label, value := range map[string][]byte{
		"durable": result.durable, "part": result.parts,
		"next model": result.nextModelRequest, "notice": result.notice,
	} {
		if !strings.Contains(string(value), toolresultredactor.Placeholder) {
			t.Fatalf("%s observation lacks placeholder", label)
		}
	}
}

func TestMountedRedactorScansKeysAndUsesParentFallbacks(t *testing.T) {
	cases := map[string]func(runtime.ToolResultTransform) runtime.ToolResultTransform{
		"structured key": func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
			input.Result = runtime.ToolResult{Output: "safe", Structured: json.RawMessage(`{"FIX_QWERTY":"value","safe":"value"}`), Metadata: map[string]string{"safe": "value"}}
			return input
		},
		"result metadata key": func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
			input.Result = runtime.ToolResult{Output: "safe", Structured: json.RawMessage(`{"safe":"value"}`), Metadata: map[string]string{fixtureMarker: "value"}}
			return input
		},
		"attachment metadata key": func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
			input.Result = runtime.ToolResult{Output: "safe", Structured: json.RawMessage(`{"safe":"value"}`), Attachments: []runtime.Attachment{{Name: "safe", Metadata: map[string]string{fixtureMarker: "value"}}}}
			return input
		},
	}
	for name, produce := range cases {
		t.Run(name, func(t *testing.T) {
			result := runAcceptance(t, acceptanceCase{
				options: toolresultredactor.Options{Limits: acceptanceLimits(), AdditionalPatterns: []toolresultredactor.Pattern{{ID: "fixture-marker", Expression: `FIX_[A-Z]{6}`}}},
				produce: produce,
			})
			assertAbsent(t, result.durable, fixtureMarker, "durable output")
			assertAbsent(t, result.nextModelRequest, fixtureMarker, "model request")
			assertAbsent(t, result.notice, fixtureMarker, "settled notice")
		})
	}
}

func TestMountedRedactorUsesBuiltinsAndExactExclusions(t *testing.T) {
	builtins := map[string]string{
		"private key":           "-----BEGIN PRIVATE KEY-----\nQUJD\n-----END PRIVATE KEY-----",
		"encrypted private key": "-----BEGIN ENCRYPTED PRIVATE KEY-----\nQUJD\n-----END ENCRYPTED PRIVATE KEY-----",
		"bearer":                "Authorization: Bearer A",
		"github":                "ghp_ABCDEFGHIJKLMNOP",
	}
	for name, value := range builtins {
		t.Run(name, func(t *testing.T) {
			result := runAcceptance(t, acceptanceCase{
				options: toolresultredactor.Options{Limits: acceptanceLimits()},
				produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
					input.Result = runtime.ToolResult{Output: value, Structured: json.RawMessage(`{"safe":"value"}`)}
					return input
				},
			})
			assertAbsent(t, result.durable, value, "builtin durable output")
			if !strings.Contains(string(result.durable), toolresultredactor.Placeholder) {
				t.Fatalf("builtin output lacks placeholder")
			}
		})
	}

	t.Run("exact bypass only", func(t *testing.T) {
		result := runAcceptance(t, acceptanceCase{
			toolName: "fixture-tool",
			options: toolresultredactor.Options{
				Limits: acceptanceLimits(), ExcludedTools: []string{"fixture-tool"},
				AdditionalPatterns: []toolresultredactor.Pattern{{ID: "bypass-sentinel", Expression: `BYPASS_SENTINEL`}},
			},
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				input.Result = runtime.ToolResult{Output: "BYPASS_SENTINEL", Structured: json.RawMessage(`{"safe":"BYPASS_SENTINEL"}`)}
				return input
			},
		})
		if !strings.Contains(string(result.durable), "BYPASS_SENTINEL") {
			t.Fatalf("exact excluded tool was scanned")
		}
	})

	for _, toolName := range []string{"Fixture-tool", "fixture-tool-suffix", "prefix-fixture-tool"} {
		t.Run(toolName, func(t *testing.T) {
			result := runAcceptance(t, acceptanceCase{
				toolName: toolName,
				options: toolresultredactor.Options{
					Limits: acceptanceLimits(), ExcludedTools: []string{"fixture-tool"},
					AdditionalPatterns: []toolresultredactor.Pattern{{ID: "bypass-sentinel", Expression: `BYPASS_SENTINEL`}},
				},
				produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
					input.Result = runtime.ToolResult{Output: "BYPASS_SENTINEL", Structured: json.RawMessage(`{"safe":"value"}`)}
					return input
				},
			})
			assertAbsent(t, result.durable, "BYPASS_SENTINEL", "non-exact exclusion output")
		})
	}
}

func TestMountedRedactorFieldLocalBudgetFallbacks(t *testing.T) {
	cases := map[string]struct {
		limits   func(*toolresultredactor.Limits)
		patterns []toolresultredactor.Pattern
		produce  func(runtime.ToolResultTransform) runtime.ToolResultTransform
		check    func(*testing.T, acceptanceResult)
	}{
		"output bytes": {
			limits: func(limits *toolresultredactor.Limits) { limits.MaxFieldBytes = 4 },
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				input.Result = runtime.ToolResult{Output: "12345", Structured: json.RawMessage(`{"safe":"ok"}`)}
				return input
			},
			check: func(t *testing.T, result acceptanceResult) {
				assertContains(t, result.durable, `"safe":"ok"`, "safe structured sibling")
			},
		},
		"json value bytes": {
			limits: func(limits *toolresultredactor.Limits) { limits.MaxFieldBytes = 4 },
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				input.Result = runtime.ToolResult{Output: "safe", Structured: json.RawMessage(`{"bad":"12345","safe":"ok"}`)}
				return input
			},
			check: func(t *testing.T, result acceptanceResult) {
				assertContains(t, result.durable, `"safe":"ok"`, "safe JSON value sibling")
			},
		},
		"structured depth": {
			limits: func(limits *toolresultredactor.Limits) { limits.MaxStructuredDepth = 2 },
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				input.Result = runtime.ToolResult{Output: "safe-output", Structured: json.RawMessage(`{"a":{"b":{"c":1}}}`)}
				return input
			},
			check: func(t *testing.T, result acceptanceResult) {
				assertContains(t, result.durable, "safe-output", "safe output sibling")
			},
		},
		"structured nodes": {
			limits: func(limits *toolresultredactor.Limits) { limits.MaxStructuredNodes = 2 },
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				input.Result = runtime.ToolResult{Output: "safe-output", Structured: json.RawMessage(`{"a":[1,2,3]}`)}
				return input
			},
			check: func(t *testing.T, result acceptanceResult) {
				assertContains(t, result.durable, "safe-output", "safe output sibling")
			},
		},
		"structured raw bytes": {
			limits: func(limits *toolresultredactor.Limits) { limits.MaxStructuredBytes = 8 },
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				input.Result = runtime.ToolResult{Output: "safe-output", Structured: json.RawMessage(`{"safe":"long-enough"}`)}
				return input
			},
			check: func(t *testing.T, result acceptanceResult) {
				assertContains(t, result.durable, "safe-output", "safe output sibling")
			},
		},
		"metadata entries": {
			limits: func(limits *toolresultredactor.Limits) { limits.MaxMetadataEntries = 1 },
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				input.Result = runtime.ToolResult{Output: "safe-output", Structured: json.RawMessage(`{"safe":"ok"}`), Metadata: map[string]string{"a": "1", "b": "2"}}
				return input
			},
			check: func(t *testing.T, result acceptanceResult) {
				assertContains(t, result.notice, `"":"[REDACTED]"`, "metadata sentinel")
			},
		},
		"attachment count": {
			limits: func(limits *toolresultredactor.Limits) { limits.MaxAttachments = 1 },
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				input.Result = runtime.ToolResult{Output: "safe-output", Structured: json.RawMessage(`{"safe":"ok"}`), Attachments: []runtime.Attachment{{Name: "a"}, {Name: "b"}}}
				return input
			},
			check: func(t *testing.T, result acceptanceResult) {
				assertContains(t, result.notice, `"Name":"[REDACTED]"`, "attachment sentinel")
			},
		},
		"aggregate match count": {
			limits:   func(limits *toolresultredactor.Limits) { limits.MaxMatchesPerField = 1 },
			patterns: []toolresultredactor.Pattern{{ID: "wide", Expression: `FIX_[A-Z]+`}, {ID: "narrow", Expression: `[A-Z]{6}`}},
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				input.Result = runtime.ToolResult{Output: fixtureMarker, Structured: json.RawMessage(`{"safe":"ok"}`)}
				return input
			},
			check: func(t *testing.T, result acceptanceResult) {
				assertContains(t, result.durable, `"safe":"ok"`, "safe match-budget sibling")
			},
		},
		"invalid UTF-8 scalars": {
			limits: func(*toolresultredactor.Limits) {},
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				invalid := string([]byte{0xff})
				input.Result = runtime.ToolResult{
					Output: invalid, Structured: json.RawMessage(`{"safe":"ok"}`), Metadata: map[string]string{"safe": invalid},
					Attachments: []runtime.Attachment{{Name: invalid, Metadata: map[string]string{"safe": invalid}}},
				}
				return input
			},
			check: func(t *testing.T, result acceptanceResult) {
				assertContains(t, result.durable, `"safe":"ok"`, "safe invalid-encoding sibling")
			},
		},
		"invalid UTF-8 metadata key": {
			limits: func(*toolresultredactor.Limits) {},
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				input.Result = runtime.ToolResult{Output: "safe", Structured: json.RawMessage(`{"safe":"ok"}`), Metadata: map[string]string{string([]byte{0xff}): "value"}}
				return input
			},
			check: func(t *testing.T, result acceptanceResult) {
				assertContains(t, result.notice, `"":"[REDACTED]"`, "invalid-key metadata sentinel")
			},
		},
		"unsafe structured string encoding": {
			limits: func(*toolresultredactor.Limits) {},
			produce: func(input runtime.ToolResultTransform) runtime.ToolResultTransform {
				input.Result = runtime.ToolResult{Output: "safe-output", Structured: json.RawMessage(`{"bad":"\ud800","safe":"ok"}`)}
				return input
			},
			check: func(t *testing.T, result acceptanceResult) {
				assertContains(t, result.durable, "safe-output", "safe invalid-structured sibling")
			},
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			limits := acceptanceLimits()
			test.limits(&limits)
			result := runAcceptance(t, acceptanceCase{options: toolresultredactor.Options{Limits: limits, AdditionalPatterns: test.patterns}, produce: test.produce})
			if !strings.Contains(string(result.notice), toolresultredactor.Placeholder) {
				t.Fatalf("unsafe field did not receive placeholder")
			}
			test.check(t, result)
		})
	}
}

func TestFailingPredecessorSkipsRedactorAndKeepsFullObserversTrusted(t *testing.T) {
	result := runAcceptance(t, acceptanceCase{
		options:       toolresultredactor.Options{Limits: acceptanceLimits(), AdditionalPatterns: []toolresultredactor.Pattern{{ID: "fixture-marker", Expression: `FIX_[A-Z]{6}`}}},
		toolOutput:    json.RawMessage(`{"unsafe":"FIX_QWERTY"}`),
		producerError: true,
		produce:       func(input runtime.ToolResultTransform) runtime.ToolResultTransform { return input },
	})
	assertAbsent(t, result.durable, fixtureMarker, "generic durable failure")
	assertAbsent(t, result.nextModelRequest, fixtureMarker, "generic model-visible failure")
	if !strings.Contains(string(result.notice), fixtureMarker) {
		t.Fatalf("trusted full-result observer did not receive skipped original result")
	}
}

func TestFailingSuccessorRestoresOriginalResultForFullObservers(t *testing.T) {
	result := runAcceptance(t, acceptanceCase{
		options:        toolresultredactor.Options{Limits: acceptanceLimits(), AdditionalPatterns: []toolresultredactor.Pattern{{ID: "fixture-marker", Expression: `FIX_[A-Z]{6}`}}},
		toolOutput:     json.RawMessage(`{"unsafe":"FIX_QWERTY"}`),
		successorError: true,
		produce:        func(input runtime.ToolResultTransform) runtime.ToolResultTransform { return input },
	})
	assertAbsent(t, result.durable, fixtureMarker, "generic durable failure")
	assertAbsent(t, result.nextModelRequest, fixtureMarker, "generic model-visible failure")
	if !strings.Contains(string(result.notice), fixtureMarker) {
		t.Fatalf("trusted full-result observer did not receive restored original result")
	}
}

func TestResumeUsesCanonicalPolicyAndRejectsChangedPolicyBeforeMutation(t *testing.T) {
	base := toolresultredactor.Options{
		Limits: acceptanceLimits(), ExcludedTools: []string{"unused-b", "unused-a"},
		AdditionalPatterns: []toolresultredactor.Pattern{
			{ID: "fixture-marker", Expression: `FIX_[A-Z]{6}`},
			{ID: "other-marker", Expression: `OTHER_[0-9]+`},
		},
	}

	t.Run("equivalent reordered policy", func(t *testing.T) {
		var executions atomic.Int32
		originalRegistry, originalMounts := mountResumeRegistry(t, base, &executions)
		descriptor := acquireDescriptor(t, originalRegistry, "resume-session")
		closeMounts(t, originalMounts)
		database, run := createPendingResumeRun(t, descriptor)
		defer func() { _ = database.Close() }()

		equivalent := base
		equivalent.ExcludedTools = []string{"unused-a", "unused-b", "unused-a"}
		equivalent.AdditionalPatterns = []toolresultredactor.Pattern{base.AdditionalPatterns[1], base.AdditionalPatterns[0]}
		registry, mounts := mountResumeRegistry(t, equivalent, &executions)
		defer closeMounts(t, mounts)
		orchestrator := newResumeOrchestrator(t, database, registry)
		handle, err := orchestrator.Resume(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-handle.Done():
			if result.Error != nil {
				t.Fatalf("resume result error-present=true status=%s", result.Status)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("resume timed out")
		}
		call, err := database.GetToolCall(context.Background(), "resume-call")
		if err != nil {
			t.Fatal(err)
		}
		if call.Status != session.ToolCallCompleted || executions.Load() != 1 {
			t.Fatalf("pending call did not execute once: status=%s executions=%d", call.Status, executions.Load())
		}
		assertAbsent(t, call.Output, fixtureMarker, "resumed durable output")
	})

	t.Run("changed policy", func(t *testing.T) {
		var executions atomic.Int32
		originalRegistry, originalMounts := mountResumeRegistry(t, base, &executions)
		descriptor := acquireDescriptor(t, originalRegistry, "resume-session")
		closeMounts(t, originalMounts)
		database, run := createPendingResumeRun(t, descriptor)
		defer func() { _ = database.Close() }()
		beforeCall, _ := database.GetToolCall(context.Background(), "resume-call")
		beforeRun, _ := database.GetRun(context.Background(), run.ID)

		changed := base
		changed.Limits.MaxFieldBytes++
		registry, mounts := mountResumeRegistry(t, changed, &executions)
		defer closeMounts(t, mounts)
		orchestrator := newResumeOrchestrator(t, database, registry)
		handle, err := orchestrator.Resume(context.Background(), run.ID)
		if handle != nil || !errors.Is(err, runtime.ErrExtensionPlanMismatch) {
			t.Fatalf("changed policy resume mismatch not enforced: handle-present=%t err-is-mismatch=%t", handle != nil, errors.Is(err, runtime.ErrExtensionPlanMismatch))
		}
		afterCall, _ := database.GetToolCall(context.Background(), "resume-call")
		afterRun, _ := database.GetRun(context.Background(), run.ID)
		if executions.Load() != 0 || afterCall.Status != beforeCall.Status || afterCall.ClaimToken != beforeCall.ClaimToken || afterRun.Status != beforeRun.Status || afterRun.ClaimToken != beforeRun.ClaimToken {
			t.Fatalf("resume mismatch mutated durable state")
		}
	})

	t.Run("running call is interrupted without re-execution", func(t *testing.T) {
		var executions atomic.Int32
		originalRegistry, originalMounts := mountResumeRegistry(t, base, &executions)
		descriptor := acquireDescriptor(t, originalRegistry, "resume-session")
		closeMounts(t, originalMounts)
		database, run := createResumeRun(t, descriptor, session.ToolCallRunning)
		defer func() { _ = database.Close() }()
		registry, mounts := mountResumeRegistry(t, base, &executions)
		defer closeMounts(t, mounts)
		handle, err := newResumeOrchestrator(t, database, registry).Resume(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		result := <-handle.Done()
		if result.Error != nil {
			t.Fatalf("running-call resume error-present=true")
		}
		call, err := database.GetToolCall(context.Background(), "resume-call")
		if err != nil {
			t.Fatal(err)
		}
		if call.Status != session.ToolCallInterrupted || executions.Load() != 0 {
			t.Fatalf("running call recovery mismatch: status=%s executions=%d", call.Status, executions.Load())
		}
	})
}

type acceptanceCase struct {
	toolName       string
	options        toolresultredactor.Options
	toolOutput     json.RawMessage
	producerError  bool
	successorError bool
	produce        func(runtime.ToolResultTransform) runtime.ToolResultTransform
}

type acceptanceResult struct {
	durable          []byte
	parts            []byte
	nextModelRequest []byte
	notice           []byte
}

func runAcceptance(t *testing.T, test acceptanceCase) acceptanceResult {
	t.Helper()
	ctx := context.Background()
	if test.toolName == "" {
		test.toolName = "fixture-tool"
	}
	if test.toolOutput == nil {
		test.toolOutput = json.RawMessage(`{"safe":"value"}`)
	}
	if test.produce == nil {
		test.produce = func(input runtime.ToolResultTransform) runtime.ToolResultTransform { return input }
	}
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}

	toolComponent := component("fixture-tool-component")
	toolMount, err := registry.Mount(ctx, toolComponent, composition.InstallerFunc(func(_ context.Context, registrar *composition.Registrar) error {
		return registrar.Tool(composition.ToolRegistration{
			ID: "fixture-tool-registration", Scope: extension.GlobalScope(),
			Definition: tools.Definition{
				Name: test.toolName, Description: "credential-free acceptance driver",
				Execute: func(context.Context, tools.Execution) (json.RawMessage, error) {
					return append(json.RawMessage(nil), test.toolOutput...), nil
				},
				Retention: runtime.RetentionPolicy{MaxInlineBytes: 1 << 20},
			},
		})
	}))
	if err != nil {
		t.Fatal(err)
	}

	notices := make(chan runtime.ToolSettledNotice, 1)
	fixtureMount, err := registry.Mount(ctx, component("fixture-result-producer"), composition.InstallerFunc(func(_ context.Context, registrar *composition.Registrar) error {
		if err := extension.OnTransform(registrar.Extensions(), runtime.ToolResultTransformPoint, extension.Registration{
			ID: "fixture/result-producer", Order: runtime.OrderApplication, Scope: extension.GlobalScope(),
		}, func(_ context.Context, input runtime.ToolResultTransform) (runtime.ToolResultTransform, error) {
			output := test.produce(input)
			if test.producerError {
				return output, errors.New("fixture predecessor failure")
			}
			return output, nil
		}); err != nil {
			return err
		}
		if test.successorError {
			if err := extension.OnTransform(registrar.Extensions(), runtime.ToolResultTransformPoint, extension.Registration{
				ID: "fixture/result-successor", Order: toolresultredactor.LateOrder + 1, Scope: extension.GlobalScope(),
			}, func(_ context.Context, input runtime.ToolResultTransform) (runtime.ToolResultTransform, error) {
				return input, errors.New("fixture successor failure")
			}); err != nil {
				return err
			}
		}
		return extension.On(registrar.Extensions(), runtime.ToolSettledPoint, extension.Registration{
			ID: "fixture/settled-observer", Scope: extension.GlobalScope(),
		}, func(_ context.Context, notice runtime.ToolSettledNotice) error {
			select {
			case notices <- notice:
			default:
			}
			return nil
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	redactorComponent := component("fixture-redactor")
	redactorComponent.Artifact.ConfigHash = ""
	redactorMount, err := toolresultredactor.Mount(ctx, registry, redactorComponent, test.options)
	if err != nil {
		t.Fatal(err)
	}

	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	var nextRequest []byte
	streamer := scriptedStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		for _, message := range request.Messages {
			if message.Role == einoschema.Tool {
				nextRequest, _ = json.Marshal(request.Messages)
				return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
			}
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID: "call-redactor", Type: "function",
			Function: einoschema.FunctionCall{Name: test.toolName, Arguments: `{}`},
		}})}, nil
	})
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(fixtureResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithIDGenerator(&fixtureIDs{}),
		runtime.WithClock(func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) }),
		runtime.WithOwnerID("fixture-owner"), runtime.WithQueueSize(16),
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := model.Selection{ProviderID: "fixture-provider", ModelID: "fixture-model"}
	handle, err := orchestrator.Start(ctx, runtime.Request{
		SessionID: "fixture-session", Message: runtime.UserMessage{Content: "run fixture"},
		Config: config.Snapshot{
			Agent: config.Agent{Name: "fixture-agent", Model: selection}, Model: selection,
			Metadata: map[string]string{"workspace_id": "fixture-workspace", "workspace_root": t.TempDir()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case completed := <-handle.Done():
		if completed.Status != session.RunCompleted || completed.Error != nil {
			t.Fatalf("orchestrator did not complete: status=%s error-present=%t", completed.Status, completed.Error != nil)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("orchestrator completion timed out")
	}
	call, err := database.GetToolCall(ctx, "call-redactor")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := database.ListMessages(ctx, "fixture-session", session.ReplayCursor{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	parts, _ := json.Marshal(batch.Parts)
	var notice runtime.ToolSettledNotice
	select {
	case notice = <-notices:
	case <-time.After(2 * time.Second):
		t.Fatal("settled notice timed out")
	}
	noticeRaw, _ := json.Marshal(notice.Result)
	if len(nextRequest) == 0 {
		t.Fatal("model did not receive a second turn")
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	for _, mounted := range []*composition.Mount{redactorMount, fixtureMount, toolMount} {
		mounted.Deactivate()
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := mounted.Close(closeCtx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	return acceptanceResult{durable: call.Output, parts: parts, nextModelRequest: nextRequest, notice: noticeRaw}
}

func mountResumeRegistry(t *testing.T, options toolresultredactor.Options, executions *atomic.Int32) (*composition.Registry, []*composition.Mount) {
	t.Helper()
	registry, err := composition.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	toolMount, err := registry.Mount(context.Background(), component("resume-tool-component"), composition.InstallerFunc(func(_ context.Context, registrar *composition.Registrar) error {
		return registrar.Tool(composition.ToolRegistration{
			ID: "resume-tool-registration", Scope: extension.GlobalScope(),
			Definition: tools.Definition{
				Name: "resume-tool", Description: "resume fixture",
				Execute: func(context.Context, tools.Execution) (json.RawMessage, error) {
					executions.Add(1)
					return json.RawMessage(`{"unsafe":"FIX_QWERTY"}`), nil
				},
				Retention: runtime.RetentionPolicy{MaxInlineBytes: 1 << 20},
			},
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	redactorComponent := component("resume-redactor")
	redactorComponent.Artifact.ConfigHash = ""
	redactorMount, err := toolresultredactor.Mount(context.Background(), registry, redactorComponent, options)
	if err != nil {
		t.Fatal(err)
	}
	return registry, []*composition.Mount{redactorMount, toolMount}
}

func acquireDescriptor(t *testing.T, registry *composition.Registry, sessionID session.ID) session.ExtensionPlanDescriptor {
	t.Helper()
	plan, err := registry.AcquireRunPlan(context.Background(), runtime.RunPlanRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := plan.Descriptor()
	plan.Release()
	return descriptor
}

func createPendingResumeRun(t *testing.T, descriptor session.ExtensionPlanDescriptor) (*store.Store, session.Run) {
	return createResumeRun(t, descriptor, session.ToolCallPending)
}

func createResumeRun(t *testing.T, descriptor session.ExtensionPlanDescriptor, status session.ToolCallStatus) (*store.Store, session.Run) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := database.CreateSession(ctx, session.Session{ID: "resume-session", Directory: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run, err := database.AdmitRun(ctx, session.Run{
		ID: "resume-run", SessionID: "resume-session", OwnerID: "old-owner", ClaimToken: "old-claim",
		Agent: "fixture-agent", ProviderID: "fixture-provider", ModelID: "fixture-model",
		Status: session.RunPending, Config: map[string]string{"workspace_id": "fixture-workspace", "workspace_root": t.TempDir()},
		ExtensionPlan: descriptor, CreatedAt: now,
	}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	execution := database.Execution(session.RunFence{RunID: run.ID, ClaimToken: run.ClaimToken})
	if _, err := execution.AppendMessage(ctx, session.Message{ID: "resume-assistant", SessionID: run.SessionID, RunID: run.ID, Role: session.RoleAssistant, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	call := session.ToolCall{
		ID: "resume-call", SessionID: run.SessionID, RunID: run.ID, MessageID: "resume-assistant",
		RequestPartID: "resume-request-part", ResultMessageID: "resume-result-message", ResultPartID: "resume-result-part",
		Name: "resume-tool", Pattern: "resume-tool", Input: json.RawMessage(`{}`), Status: session.ToolCallPending,
	}
	requestPayload, _ := json.Marshal(map[string]any{"id": call.ID, "name": call.Name, "arguments": json.RawMessage(`{}`)})
	created, err := execution.CreateToolCall(ctx, session.CreateToolCallRequest{
		Call:        call,
		RequestPart: session.Part{ID: call.RequestPartID, MessageID: call.MessageID, SessionID: call.SessionID, RunID: call.RunID, Kind: session.PartToolCall, Payload: requestPayload, CreatedAt: now, UpdatedAt: now},
		Event:       session.ToolTransitionEvent{ID: "resume-create-event", CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status == session.ToolCallRunning {
		if _, err := execution.ClaimToolCall(ctx, session.ClaimToolCallRequest{
			ID: created.Call.ID, ClaimedBy: "old-owner", ClaimToken: "old-tool-claim",
			StartedAt: now, LeaseDuration: time.Millisecond,
			Event: session.ToolTransitionEvent{ID: "resume-claim-event", CreatedAt: now},
		}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(3 * time.Millisecond)
	return database, run
}

func newResumeOrchestrator(t *testing.T, database *store.Store, registry *composition.Registry) *runtime.StreamingOrchestrator {
	t.Helper()
	streamer := scriptedStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
		return []*einoschema.Message{einoschema.AssistantMessage("done", nil)}, nil
	})
	orchestrator, err := runtime.NewStreamingOrchestrator(
		runtime.WithStore(database), runtime.WithModelResolver(fixtureResolver{streamer: streamer}),
		runtime.WithRunPlanProvider(registry), runtime.WithIDGenerator(&fixtureIDs{}),
		runtime.WithClock(time.Now), runtime.WithOwnerID("resume-owner"), runtime.WithLease(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	return orchestrator
}

func closeMounts(t *testing.T, mounts []*composition.Mount) {
	t.Helper()
	for _, mount := range mounts {
		mount.Deactivate()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := mount.Close(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func component(instance string) extension.Component {
	return extension.Component{InstanceID: instance, Artifact: extension.Artifact{
		Name: instance, Version: "test", Hash: "synthetic-artifact", ConfigHash: "synthetic-config", SourceKind: extension.SourceNative,
	}}
}

type fixtureResolver struct{ streamer model.Streamer }

func (r fixtureResolver) Resolve(context.Context, model.Selection, model.Runtime) (model.Resolved, error) {
	return model.Resolved{
		Provider: model.Provider{ID: "fixture-provider"},
		Model:    model.Descriptor{ID: "fixture-model", ProviderID: "fixture-provider"},
		Streamer: r.streamer,
	}, nil
}

type scriptedStreamer func(context.Context, model.Request) ([]*einoschema.Message, error)

func (s scriptedStreamer) StreamProvider(ctx context.Context, request model.Request) (*einoschema.StreamReader[model.StreamDelta], error) {
	messages, err := s(ctx, request)
	if err != nil {
		return nil, err
	}
	reader, writer := einoschema.Pipe[model.StreamDelta](len(messages))
	go func() {
		defer writer.Close()
		for _, message := range messages {
			if writer.Send(model.StreamDelta{Message: message, Usage: model.UsageFromMessage(message)}, nil) {
				return
			}
		}
	}()
	return reader, nil
}

type fixtureIDs struct {
	mu sync.Mutex
	n  int
}

func (i *fixtureIDs) next(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return prefix + "-" + time.Unix(int64(i.n), 0).UTC().Format("150405")
}
func (i *fixtureIDs) NewRunID() session.RunID           { return session.RunID(i.next("run")) }
func (i *fixtureIDs) NewMessageID() session.MessageID   { return session.MessageID(i.next("message")) }
func (i *fixtureIDs) NewPartID() session.PartID         { return session.PartID(i.next("part")) }
func (i *fixtureIDs) NewToolCallID() session.ToolCallID { return session.ToolCallID(i.next("call")) }
func (i *fixtureIDs) NewEventID() session.EventID       { return session.EventID(i.next("event")) }
func (i *fixtureIDs) NewEpochID() session.EpochID       { return session.EpochID(i.next("epoch")) }

func assertAbsent(t *testing.T, raw []byte, marker, label string) {
	t.Helper()
	if strings.Contains(string(raw), marker) {
		t.Fatalf("%s retained fixture marker", label)
	}
}

func assertContains(t *testing.T, raw []byte, marker, label string) {
	t.Helper()
	if !strings.Contains(string(raw), marker) {
		t.Fatalf("%s missing expected safe content", label)
	}
}
