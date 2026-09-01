package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestNormalizeInputAcceptsBoundsAndPreservesText(t *testing.T) {
	normalize := normalizeInput(canonicalOptionsForTest(t))
	for _, raw := range []string{
		`{"question":" Which? ","options":[{"label":"A"},{"label":"B","description":" second "}]}`,
		`{"question":"Which?","options":[{"label":"A"},{"label":"B"},{"label":"C"},{"label":"D"},{"label":"E"}]}`,
		`{"question":"Emoji \ud83d\ude80?","options":[{"label":"A"},{"label":"B"}]}`,
	} {
		encoded, err := normalize(context.Background(), json.RawMessage(raw))
		if err != nil {
			t.Fatalf("normalize(%s): %v", raw, err)
		}
		if !json.Valid(encoded) || strings.Contains(string(encoded), "�") {
			t.Fatalf("invalid or lossy output: %s", encoded)
		}
	}
}

func TestNormalizeInputRejectsMalformedAndLossyInputs(t *testing.T) {
	normalize := normalizeInput(canonicalOptionsForTest(t))
	invalidUTF8 := append([]byte(`{"question":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`","options":[{"label":"A"},{"label":"B"}]}`)...)
	invalidLabelUTF8 := append([]byte(`{"question":"Q","options":[{"label":"`), 0xff)
	invalidLabelUTF8 = append(invalidLabelUTF8, []byte(`"},{"label":"B"}]}`)...)
	invalidDescriptionUTF8 := append([]byte(`{"question":"Q","options":[{"label":"A","description":"`), 0xff)
	invalidDescriptionUTF8 = append(invalidDescriptionUTF8, []byte(`"},{"label":"B"}]}`)...)
	tests := map[string][]byte{
		"unknown top":              []byte(`{"question":"Q","options":[{"label":"A"},{"label":"B"}],"extra":true}`),
		"unknown option":           []byte(`{"question":"Q","options":[{"label":"A","extra":true},{"label":"B"}]}`),
		"duplicate top field":      []byte(`{"question":"Q","question":"R","options":[{"label":"A"},{"label":"B"}]}`),
		"duplicate option field":   []byte(`{"question":"Q","options":[{"label":"A","label":"B"},{"label":"C"}]}`),
		"trailing":                 []byte(`{"question":"Q","options":[{"label":"A"},{"label":"B"}]} {}`),
		"blank question":           []byte(`{"question":" ","options":[{"label":"A"},{"label":"B"}]}`),
		"too few":                  []byte(`{"question":"Q","options":[{"label":"A"}]}`),
		"too many":                 []byte(`{"question":"Q","options":[{"label":"A"},{"label":"B"},{"label":"C"},{"label":"D"},{"label":"E"},{"label":"F"}]}`),
		"blank label":              []byte(`{"question":"Q","options":[{"label":" "},{"label":"B"}]}`),
		"duplicate":                []byte(`{"question":"Q","options":[{"label":"A"},{"label":"A"}]}`),
		"nul":                      []byte(`{"question":"Q\u0000","options":[{"label":"A"},{"label":"B"}]}`),
		"isolated high":            []byte(`{"question":"Q\ud800","options":[{"label":"A"},{"label":"B"}]}`),
		"isolated low":             []byte(`{"question":"Q\udc00","options":[{"label":"A"},{"label":"B"}]}`),
		"wrong surrogate":          []byte(`{"question":"Q\ud800\u0041","options":[{"label":"A"},{"label":"B"}]}`),
		"invalid question utf8":    invalidUTF8,
		"invalid label utf8":       invalidLabelUTF8,
		"invalid description utf8": invalidDescriptionUTF8,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			encoded, err := normalize(context.Background(), raw)
			if err == nil || !errors.Is(err, tools.ErrMalformedInput) {
				t.Fatalf("result=%q err=%v", encoded, err)
			}
			if utf8.Valid(encoded) && strings.Contains(string(encoded), "�") {
				t.Fatalf("lossy replacement returned: %q", encoded)
			}
		})
	}
}

func TestNormalizeInputEnforcesEveryByteLimit(t *testing.T) {
	options := testOptions()
	options.Limits.MaxQuestionBytes = 1
	options.Limits.MaxOptionLabelBytes = 1
	options.Limits.MaxOptionDescriptionBytes = 1
	canonical, err := canonicalize(options)
	if err != nil {
		t.Fatal(err)
	}
	normalize := normalizeInput(canonical)
	for name, raw := range map[string]string{
		"question":    `{"question":"QQ","options":[{"label":"A"},{"label":"B"}]}`,
		"label":       `{"question":"Q","options":[{"label":"AA"},{"label":"B"}]}`,
		"description": `{"question":"Q","options":[{"label":"A","description":"DD"},{"label":"B"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalize(context.Background(), json.RawMessage(raw)); err == nil {
				t.Fatal("overflow accepted")
			}
		})
	}
}

func TestDefinitionSchemaPermissionAndRetention(t *testing.T) {
	canonical := canonicalOptionsForTest(t)
	coordinator := newCoordinator(canonical)
	definition := definition(canonical, coordinator)
	if definition.Name != ToolName || definition.RetrySafe || len(definition.Permissions) != 1 || definition.Permissions[0] != PermissionAsk {
		t.Fatalf("definition contract = %#v", definition)
	}
	pattern, err := definition.Pattern(context.Background(), json.RawMessage(`{"question":"secret-like synthetic text"}`))
	if err != nil || pattern != PermissionAsk {
		t.Fatalf("pattern=%q err=%v", pattern, err)
	}
	schema, err := definition.Parameters.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, fragment := range []string{`"required":["question","options"]`, `"minItems":2`, `"maxItems":5`, `"additionalProperties":false`, `"required":["label"]`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("schema missing %s: %s", fragment, text)
		}
	}
	if definition.Retention.MaxInlineBytes <= 0 || definition.Retention.StoreExternal || definition.Retention.Redact {
		t.Fatalf("retention = %#v", definition.Retention)
	}
}

func TestMapResponseValidatesAllShapes(t *testing.T) {
	input := toolInput{Question: "Q", Options: []toolOption{{Label: "A"}, {Label: "B"}}}
	limits := testLimits()
	tests := []struct {
		response Response
		status   Status
		answer   string
		index    int
		wantErr  bool
	}{
		{response: Response{Kind: ResponseSelected, SelectedOption: 2}, status: StatusSelected, answer: "B", index: 2},
		{response: Response{Kind: ResponseCustom, CustomAnswer: "something else"}, status: StatusCustom, answer: "something else"},
		{response: Response{Kind: ResponseDismissed}, status: StatusDismissed},
		{response: Response{Kind: ResponseUnavailable}, status: StatusUnavailable},
		{response: Response{Kind: ResponseSelected, SelectedOption: 0}, wantErr: true},
		{response: Response{Kind: ResponseSelected, SelectedOption: 1, CustomAnswer: "mixed"}, wantErr: true},
		{response: Response{Kind: ResponseCustom, SelectedOption: 1, CustomAnswer: "mixed"}, wantErr: true},
		{response: Response{Kind: ResponseCustom, CustomAnswer: " "}, wantErr: true},
		{response: Response{Kind: ResponseCustom, CustomAnswer: strings.Repeat("x", limits.MaxCustomAnswerBytes+1)}, wantErr: true},
		{response: Response{Kind: ResponseCustom, CustomAnswer: "bad\x00answer"}, wantErr: true},
		{response: Response{Kind: ResponseCustom, CustomAnswer: string([]byte{0xff})}, wantErr: true},
		{response: Response{Kind: ResponseDismissed, SelectedOption: 1}, wantErr: true},
		{response: Response{Kind: "invalid"}, wantErr: true},
	}
	for _, test := range tests {
		result, err := mapResponse(responseEnvelope{response: test.response}, input, limits)
		if test.wantErr {
			if err == nil || err.Error() != errResponderOperation.Error() {
				t.Fatalf("response %#v err=%v", test.response, err)
			}
			continue
		}
		if err != nil || result.Status != test.status || result.Answer != test.answer || result.SelectedOption != test.index {
			t.Fatalf("response %#v result=%#v err=%v", test.response, result, err)
		}
	}
}

func FuzzNormalizeInput(f *testing.F) {
	f.Add([]byte(`{"question":"Q","options":[{"label":"A"},{"label":"B"}]}`))
	f.Add([]byte(`{"question":"\ud800","options":[]}`))
	canonical, err := canonicalize(testOptions())
	if err != nil {
		f.Fatal(err)
	}
	normalize := normalizeInput(canonical)
	f.Fuzz(func(t *testing.T, raw []byte) {
		result, err := normalize(context.Background(), raw)
		if err == nil && (!json.Valid(result) || !utf8.Valid(result)) {
			t.Fatalf("accepted invalid output %q", result)
		}
	})
}

func FuzzMapResponse(f *testing.F) {
	f.Add("selected", 1, "")
	f.Add("custom", 0, "synthetic")
	f.Add("dismissed", 0, "")
	f.Add("unknown", 99, "mixed")
	input := toolInput{Question: "Q", Options: []toolOption{{Label: "A"}, {Label: "B"}}}
	limits := testLimits()
	f.Fuzz(func(t *testing.T, kind string, index int, answer string) {
		result, err := mapResponse(responseEnvelope{response: Response{
			Kind: ResponseKind(kind), SelectedOption: index, CustomAnswer: answer,
		}}, input, limits)
		if err == nil {
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil || !json.Valid(encoded) {
				t.Fatalf("valid response produced invalid result: %#v %v", result, marshalErr)
			}
		}
	})
}
