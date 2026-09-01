package toolresultredactor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStructuredNoMatchIsByteIdentical(t *testing.T) {
	policy := policyForTest(t, testOptions())
	input := json.RawMessage(" { \"duplicate\" : 1.2300e+04, \"duplicate\" : [true, null, \"\\u0061\\ud83d\\ude00\"] } \n")
	output, state := policy.scanStructured(context.Background(), input)
	if state != scanUnchanged || string(output) != string(input) {
		t.Fatalf("safe document bytes changed: state=%d", state)
	}
	input[0] = 'X'
	if output[0] == 'X' {
		t.Fatalf("safe structured output aliases input")
	}
}

func TestStructuredRedactsOnlyMatchingValueLiteral(t *testing.T) {
	policy := policyForTest(t, testOptions())
	input := json.RawMessage(`{"safe":1.00e+2,"target":"before SYN_ABCD after","tail":"\u0061"}`)
	want := `{"safe":1.00e+2,"target":"before [REDACTED] after","tail":"\u0061"}`
	output, state := policy.scanStructured(context.Background(), input)
	if state != scanChanged || string(output) != want {
		t.Fatalf("value literal rewrite mismatch: state=%d", state)
	}
}

func TestStructuredUnsafeKeyReplacesTopLevelOnly(t *testing.T) {
	cases := map[string]json.RawMessage{
		"matching":             json.RawMessage(`{"SYN_ABCD":"value","safe":1}`),
		"oversize":             json.RawMessage(`{"long-key":"value"}`),
		"invalid raw utf8":     append(json.RawMessage(`{"`), append([]byte{0xff}, []byte(`":"value"}`)...)...),
		"lone high surrogate":  json.RawMessage(`{"\ud800":"value"}`),
		"lone low surrogate":   json.RawMessage(`{"\udc00":"value"}`),
		"misordered surrogate": json.RawMessage(`{"\udc00\ud800":"value"}`),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			options := testOptions()
			if name == "oversize" {
				options.Limits.MaxFieldBytes = 4
			}
			policy := policyForTest(t, options)
			output, state := policy.scanStructured(context.Background(), input)
			if state != scanUnsafe || string(output) != string(structuredPlaceholder) {
				t.Fatalf("unsafe key did not replace structured: state=%d", state)
			}
		})
	}
}

func TestStructuredUnsafeValueIsLocal(t *testing.T) {
	options := testOptions()
	options.Limits.MaxFieldBytes = 4
	policy := policyForTest(t, options)
	input := json.RawMessage(`{"a":"12345","b":"ok","n":1}`)
	output, state := policy.scanStructured(context.Background(), input)
	if state != scanChanged || string(output) != `{"a":"[REDACTED]","b":"ok","n":1}` {
		t.Fatalf("unsafe value fallback was not local: state=%d", state)
	}
}

func TestStructuredBudgetsReplaceOnlyStructured(t *testing.T) {
	cases := map[string]func(*Options){
		"raw bytes": func(options *Options) { options.Limits.MaxStructuredBytes = 4 },
		"depth":     func(options *Options) { options.Limits.MaxStructuredDepth = 2 },
		"nodes":     func(options *Options) { options.Limits.MaxStructuredNodes = 2 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			options := testOptions()
			mutate(&options)
			policy := policyForTest(t, options)
			input := json.RawMessage(`{"a":{"b":[1,2,3]}}`)
			output, state := policy.scanStructured(context.Background(), input)
			if state != scanUnsafe || string(output) != string(structuredPlaceholder) {
				t.Fatalf("structured budget did not fallback: state=%d", state)
			}
		})
	}
}

func TestStructuredInvalidEncodingInValueFallsBackLocally(t *testing.T) {
	policy := policyForTest(t, testOptions())
	cases := []json.RawMessage{
		append(json.RawMessage(`{"bad":"`), append([]byte{0xff}, []byte(`","safe":"ok"}`)...)...),
		json.RawMessage(`{"bad":"\ud800","safe":"ok"}`),
		json.RawMessage(`{"bad":"\udc00","safe":"ok"}`),
	}
	for _, input := range cases {
		output, state := policy.scanStructured(context.Background(), input)
		if state != scanUnsafe || string(output) != string(structuredPlaceholder) {
			// Encoding-invalid JSON strings make the structured field unsafe,
			// unlike a valid but over-limit decoded value.
			t.Fatalf("invalid string encoding did not fallback: state=%d", state)
		}
	}
}

func FuzzScalarNeverPanics(f *testing.F) {
	f.Add([]byte("safe"))
	f.Add([]byte("SYN_ABCD"))
	f.Add([]byte{0xff, 'x'})
	policy := policyForTest(f, testOptions())
	f.Fuzz(func(t *testing.T, raw []byte) {
		output, _ := policy.scanScalar(context.Background(), string(raw))
		if !utf8.ValidString(output) {
			t.Fatal("transformed scalar is not valid UTF-8")
		}
	})
}

func FuzzValidJSONRemainsValid(f *testing.F) {
	f.Add([]byte(`{"x":"safe"}`))
	f.Add([]byte(` [1, {"x":"SYN_ABCD"}] `))
	f.Add([]byte(`{"x":"\ud83d\ude00"}`))
	policy := policyForTest(f, testOptions())
	f.Fuzz(func(t *testing.T, raw []byte) {
		if !json.Valid(raw) {
			t.Skip()
		}
		output, state := policy.scanStructured(context.Background(), raw)
		if !json.Valid(output) {
			t.Fatal("transformed valid document became invalid")
		}
		if state == scanUnchanged && string(output) != string(raw) {
			t.Fatal("unchanged valid document was not byte-identical")
		}
	})
}

func FuzzInjectedSecretIsRemoved(f *testing.F) {
	f.Add([]byte("prefix"), []byte("suffix"))
	f.Add([]byte{0xff}, []byte("safe"))
	policy := policyForTest(f, testOptions())
	f.Fuzz(func(t *testing.T, prefix, suffix []byte) {
		input := string(prefix) + "SYN_ABCD" + string(suffix)
		output, _ := policy.scanScalar(context.Background(), input)
		if strings.Contains(output, "SYN_ABCD") {
			t.Fatal("injected fixture marker survived transformation")
		}
	})
}
