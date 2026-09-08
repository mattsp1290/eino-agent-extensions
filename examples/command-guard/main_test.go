package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/runtime"
	"github.com/mattsp1290/eino-agent/session"
)

func TestExampleSuppressionDurabilityAndNextTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := runExample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Executions != 1 || got.Allowed.Status != session.ToolCallCompleted || got.Denied.Status != session.ToolCallFailed {
		t.Fatal("incorrect execution outcomes")
	}
	var out runtime.ToolOutput
	if err := json.Unmarshal(got.Denied.Output, &out); err != nil {
		t.Fatal(err)
	}
	var denial map[string]string
	if err := json.Unmarshal(out.Structured, &denial); err != nil {
		t.Fatal(err)
	}
	if denial["status"] != "denied" || denial["message"] != "command policy denied: rule-match" || !strings.Contains(got.NextRequest, denial["message"]) {
		t.Fatal("denial not persisted and visible")
	}
}
