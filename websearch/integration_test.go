package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/mattsp1290/eino-agent/model"
	"github.com/mattsp1290/eino-agent/permissions"
	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
	store "github.com/mattsp1290/eino-agent/store/sqlite"
)

func TestIntegrationBoundedDurableSuccessAndNextTurn(t *testing.T) {
	searcher := SearcherFunc(func(context.Context, string) ([]Source, error) {
		return []Source{
			{Title: "bad", URL: "https://user:secret@example.test/private", Snippet: "drop"},
			{Title: strings.Repeat("t", 40), URL: "https://example.test/kept?x=1#f", Snippet: strings.Repeat("s", 60)},
			{Title: "later", URL: "https://example.test/later", Snippet: "ignored"},
		}, nil
	})
	result := runFreshIntegration(t, searcher, permissions.ActionAllow, nil)
	if result.call.Status != session.ToolCallCompleted || result.call.Name != ToolName || result.call.Pattern != permissionPattern || result.call.RetrySafe {
		t.Fatalf("call=%#v", result.call)
	}
	if string(result.call.Input) != `{"query":"bounded query"}` || len(result.queries) != 1 || result.queries[0] != "bounded query" {
		t.Fatalf("input=%s queries=%#v", result.call.Input, result.queries)
	}
	if len(result.permissions) != 1 || result.permissions[0].Permission != PermissionSearch || result.permissions[0].Pattern != permissionPattern {
		t.Fatalf("permissions=%#v", result.permissions)
	}
	permissionRaw, _ := json.Marshal(result.permissions)
	if bytes.Contains(permissionRaw, []byte("bounded query")) {
		t.Fatalf("permission leaked query: %s", permissionRaw)
	}
	var durable runtime.ToolOutput
	if err := json.Unmarshal(result.call.Output, &durable); err != nil {
		t.Fatal(err)
	}
	if durable.Status != "completed" || durable.Truncated || durable.External || durable.Redacted || !bytes.Equal(durable.Structured, []byte(durable.Content)) {
		t.Fatalf("durable=%#v", durable)
	}
	if durable.InlineSize != 2*int64(len(durable.Content)) || durable.OriginalSize != durable.InlineSize {
		t.Fatalf("sizes=%#v", durable)
	}
	var bounded Result
	if err := json.Unmarshal(durable.Structured, &bounded); err != nil {
		t.Fatal(err)
	}
	if len(bounded.Results) != 1 || bounded.Results[0].URL != "https://example.test/kept?x=1#f" || len(bounded.Results[0].Title) != testLimits().MaxTitleBytes || len(bounded.Results[0].Snippet) != testLimits().MaxSnippetBytes {
		t.Fatalf("bounded=%#v", bounded)
	}
	combined := string(result.call.Output) + string(result.parts) + string(result.nextRequest)
	if !strings.Contains(combined, bounded.Results[0].URL) || strings.Contains(combined, "user:secret") || strings.Contains(combined, "later") {
		t.Fatalf("durable visibility=%s", combined)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(durable.Structured, &envelope); err != nil || len(envelope) != 1 || envelope["results"] == nil {
		t.Fatalf("envelope=%s err=%v", durable.Structured, err)
	}
	var record map[string]json.RawMessage
	recordRaw, _ := json.Marshal(bounded.Results[0])
	if err := json.Unmarshal(recordRaw, &record); err != nil || len(record) != 3 || record["title"] == nil || record["url"] == nil || record["snippet"] == nil {
		t.Fatalf("record=%s err=%v", recordRaw, err)
	}
}

func TestIntegrationExactLimitEscapingSurvivesBothInlineCopies(t *testing.T) {
	limits := testLimits()
	limits.MaxResults = 1
	limits.MaxTitleBytes = 4
	limits.MaxURLBytes = 64
	limits.MaxSnippetBytes = 5
	urlPrefix := "https://example.test/?q="
	source := Source{
		Title:   strings.Repeat("\x01", limits.MaxTitleBytes),
		URL:     urlPrefix + strings.Repeat("&", limits.MaxURLBytes-len(urlPrefix)),
		Snippet: strings.Repeat("\x02", limits.MaxSnippetBytes),
	}
	result := runFreshIntegration(t, SearcherFunc(func(context.Context, string) ([]Source, error) {
		return []Source{source}, nil
	}), permissions.ActionAllow, func(current *Limits) { *current = limits })
	var output runtime.ToolOutput
	if err := json.Unmarshal(result.call.Output, &output); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(Result{Results: []Source{source}})
	if err != nil {
		t.Fatal(err)
	}
	if output.Truncated || output.External || output.Redacted || output.Content != string(want) || !bytes.Equal(output.Structured, want) || output.InlineSize != int64(2*len(want)) || output.OriginalSize != output.InlineSize {
		t.Fatalf("output=%#v want=%s", output, want)
	}
	retention, err := resultRetention(limits)
	if err != nil || output.InlineSize > retention.MaxInlineBytes {
		t.Fatalf("inline=%d retention=%d err=%v", output.InlineSize, retention.MaxInlineBytes, err)
	}
}

func TestIntegrationEmptySuccessDistinctFromPermissionAndBackendFailures(t *testing.T) {
	empty := runFreshIntegration(t, SearcherFunc(func(context.Context, string) ([]Source, error) { return nil, nil }), permissions.ActionAllow, nil)
	var output runtime.ToolOutput
	if err := json.Unmarshal(empty.call.Output, &output); err != nil || string(output.Structured) != `{"results":[]}` {
		t.Fatalf("empty output=%#v err=%v", output, err)
	}
	for _, action := range []permissions.Action{permissions.ActionDeny, permissions.ActionAsk} {
		t.Run(string(action), func(t *testing.T) {
			var calls atomic.Int32
			result := runFreshIntegration(t, SearcherFunc(func(context.Context, string) ([]Source, error) {
				calls.Add(1)
				return nil, nil
			}), action, nil)
			if result.call.Status != session.ToolCallFailed || calls.Load() != 0 || len(result.queries) != 0 {
				t.Fatalf("call=%#v callback=%d queries=%#v", result.call, calls.Load(), result.queries)
			}
		})
	}
}

func TestIntegrationSanitizesBackendErrorAndPanicEverywhere(t *testing.T) {
	const sentinel = "SYNTHETIC_PRIVATE_BACKEND_SECRET"
	fixtures := map[string]Searcher{
		"error": SearcherFunc(func(context.Context, string) ([]Source, error) {
			return nil, fmt.Errorf("endpoint response %s", sentinel)
		}),
		"panic": SearcherFunc(func(context.Context, string) ([]Source, error) { panic(sentinel) }),
	}
	for name, searcher := range fixtures {
		t.Run(name, func(t *testing.T) {
			result := runFreshIntegration(t, searcher, permissions.ActionAllow, nil)
			if result.call.Status != session.ToolCallFailed || result.call.Error != errSearchOperation.Error() {
				t.Fatalf("call=%#v", result.call)
			}
			combined := result.call.Error + string(result.call.Output) + string(result.parts) + string(result.events) + string(result.nextRequest)
			if strings.Contains(combined, sentinel) || strings.Contains(combined, "endpoint response") {
				t.Fatalf("private data leaked: %s", combined)
			}
		})
	}
}

func TestIntegrationPackageTimeoutIsSanitizedDeadline(t *testing.T) {
	result := runFreshIntegration(t, SearcherFunc(func(ctx context.Context, _ string) ([]Source, error) {
		<-ctx.Done()
		return nil, errors.New("SYNTHETIC_POST_CANCEL_SECRET")
	}), permissions.ActionAllow, func(limits *Limits) { limits.MaxWait = 10 * time.Millisecond })
	if result.call.Status != session.ToolCallInterrupted || result.call.Error != context.DeadlineExceeded.Error() {
		t.Fatalf("call=%#v", result.call)
	}
	combined := string(result.call.Output) + string(result.parts) + string(result.nextRequest)
	if strings.Contains(combined, "SYNTHETIC_POST_CANCEL_SECRET") || strings.Contains(combined, `{"results":[]}`) {
		t.Fatalf("timeout leaked or became empty success: %s", combined)
	}
}

func TestIntegrationParentInterruptionCancelsSearcherOnce(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	var calls atomic.Int32
	options := testOptions()
	options.SearcherIdentity = "integration-cancel-v1"
	options.Searcher = SearcherFunc(func(ctx context.Context, _ string) ([]Source, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	registry, mount := mountTestRegistry(t, options)
	defer closeTestMount(t, mount)
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	streamer := integrationStreamer(func(_ context.Context, request model.Request) ([]*einoschema.Message, error) {
		if latestToolMessage(request.Messages) != nil {
			return []*einoschema.Message{einoschema.AssistantMessage("unexpected", nil)}, nil
		}
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID: "cancel-call", Type: "function", Function: einoschema.FunctionCall{Name: ToolName, Arguments: integrationInput},
		}})}, nil
	})
	orchestrator := newIntegrationOrchestrator(t, database, registry, streamer, permissions.StaticPolicy{}, "cancel-owner")
	handle, err := orchestrator.Start(context.Background(), integrationRuntimeRequest("cancel-session"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("searcher did not start")
	}
	if err := handle.Interrupt(context.Background(), "synthetic interruption"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-handle.Done():
		if result.Status != session.RunInterrupted || !result.Interrupted {
			t.Fatalf("result=%#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interrupted run did not settle")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("child not canceled")
	}
	call, err := database.GetToolCall(context.Background(), "cancel-call")
	if err != nil || call.Status != session.ToolCallInterrupted || calls.Load() != 1 {
		t.Fatalf("call=%#v calls=%d err=%v", call, calls.Load(), err)
	}
}

func TestIntegrationRejectedInputCreatesNoDurableCall(t *testing.T) {
	var calls atomic.Int32
	options := testOptions()
	options.Searcher = SearcherFunc(func(context.Context, string) ([]Source, error) {
		calls.Add(1)
		return nil, nil
	})
	registry, mount := mountTestRegistry(t, options)
	defer closeTestMount(t, mount)
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "invalid-input.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	streamer := integrationStreamer(func(context.Context, model.Request) ([]*einoschema.Message, error) {
		return []*einoschema.Message{einoschema.AssistantMessage("", []einoschema.ToolCall{{
			ID: "invalid-web-search-call", Type: "function",
			Function: einoschema.FunctionCall{Name: ToolName, Arguments: `{"query":"   ","extra":true}`},
		}})}, nil
	})
	orchestrator := newIntegrationOrchestrator(t, database, registry, streamer, permissions.StaticPolicy{}, "invalid-owner")
	handle, err := orchestrator.Start(context.Background(), integrationRuntimeRequest("invalid-input-session"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("invalid-input run did not settle")
	}
	if call, err := database.GetToolCall(context.Background(), "invalid-web-search-call"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("durable call=%#v err=%v", call, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("rejected input invoked searcher %d times", calls.Load())
	}
}
