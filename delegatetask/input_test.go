package delegatetask

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mattsp1290/eino-agent/tools"
)

func canonicalOptionsForTest(t *testing.T) canonicalOptions {
	t.Helper()
	options, err := canonicalize(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	return options
}

func TestInputAcceptsBoundsAndPreservesTask(t *testing.T) {
	options := testOptions()
	options.Limits.MaxTaskBytes = len(" keep \n spacing ")
	options.Limits.MaxProfileBytes = len("read-only")
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	got, err := normalizeInput(canonical)(context.Background(), json.RawMessage(`{"task":" keep \n spacing ","profile":"read-only"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"task":" keep \n spacing ","profile":"read-only"}` {
		t.Fatalf("canonical=%s", got)
	}
}

func TestInputRejectsMalformedShapeAndLimits(t *testing.T) {
	canonical := canonicalOptionsForTest(t)
	normalize := normalizeInput(canonical)
	invalidUTF8 := append([]byte(`{"task":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`","profile":"read-only"}`)...)
	tests := map[string][]byte{
		"invalid JSON":     []byte(`{"task":`),
		"duplicate":        []byte(`{"task":"a","task":"b","profile":"read-only"}`),
		"unknown":          []byte(`{"task":"a","profile":"read-only","extra":true}`),
		"trailing":         []byte(`{"task":"a","profile":"read-only"} {}`),
		"missing task":     []byte(`{"profile":"read-only"}`),
		"missing profile":  []byte(`{"task":"a"}`),
		"wrong task type":  []byte(`{"task":1,"profile":"read-only"}`),
		"blank task":       []byte(`{"task":" \n\t","profile":"read-only"}`),
		"task nul":         []byte(`{"task":"a\u0000","profile":"read-only"}`),
		"bad profile":      []byte(`{"task":"a","profile":"spaces invalid"}`),
		"invalid raw utf8": invalidUTF8,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if output, err := normalize(context.Background(), raw); err == nil || !errors.Is(err, tools.ErrMalformedInput) {
				t.Fatalf("output=%q err=%v", output, err)
			}
		})
	}
	limited := testOptions()
	limited.Limits.MaxTaskBytes = 1
	limited.Limits.MaxProfileBytes = 1
	canonical, err := canonicalize(limited)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"task":    `{"task":"ab","profile":"p"}`,
		"profile": `{"task":"a","profile":"pp"}`,
	} {
		t.Run("limit "+name, func(t *testing.T) {
			if _, err := normalizeInput(canonical)(context.Background(), json.RawMessage(raw)); err == nil {
				t.Fatal("limit violation accepted")
			}
		})
	}
}

func TestInputAcceptsReplacementCharacter(t *testing.T) {
	raw := json.RawMessage(`{"task":"legitimate � and repaired \ud800","profile":"read-only"}`)
	got, err := normalizeInput(canonicalOptionsForTest(t))(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "�") != 2 {
		t.Fatalf("replacement canonicalization=%s", got)
	}
}
