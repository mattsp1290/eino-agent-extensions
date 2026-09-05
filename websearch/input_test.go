package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mattsp1290/eino-agent/tools"
)

func TestInputNormalization(t *testing.T) {
	options := canonicalOptionsForTest(t)
	normalize := normalizeInput(options)
	got, err := normalize(context.Background(), json.RawMessage(` { "query" : "  one query  " } `))
	if err != nil || string(got) != `{"query":"one query"}` {
		t.Fatalf("got=%s err=%v", got, err)
	}
	limit := options.limits.MaxQueryBytes
	exact := strings.Repeat("a", limit-2) + "é"
	got, err = normalize(context.Background(), json.RawMessage(`{"query":`+mustJSON(t, exact)+`}`))
	if err != nil || !strings.Contains(string(got), exact) {
		t.Fatalf("exact input=%s err=%v", got, err)
	}
}

func TestInputRejectsMalformedAndSemanticFailures(t *testing.T) {
	normalize := normalizeInput(canonicalOptionsForTest(t))
	tooLong := strings.Repeat("q", testLimits().MaxQueryBytes+1)
	tests := []json.RawMessage{
		json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`{"query":""}`),
		json.RawMessage(`{"query":"   "}`), json.RawMessage(`{"query":"bad\u0000query"}`),
		json.RawMessage(`{"query":"ok","extra":true}`),
		json.RawMessage(`{"query":"one","query":"two"}`),
		json.RawMessage(`{"query":"one"} {}`),
		json.RawMessage(`{"query":` + mustJSON(t, tooLong) + `}`),
	}
	for _, raw := range tests {
		if got, err := normalize(context.Background(), raw); !errors.Is(err, tools.ErrMalformedInput) {
			t.Errorf("raw=%q got=%s err=%v", raw, got, err)
		}
	}
}

func TestInputHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := normalizeInput(canonicalOptionsForTest(t))(ctx, json.RawMessage(`{"query":"ok"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
