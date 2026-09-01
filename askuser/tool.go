package askuser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	einoschema "github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/mattsp1290/eino-agent/tools"
)

const (
	// ToolName is the model-facing tool name.
	ToolName = "ask_user"
	// PermissionAsk is the stable low-cardinality permission requested before
	// responder admission.
	PermissionAsk = "interaction.ask"
)

type toolInput struct {
	Question string       `json:"question" jsonschema:"required"`
	Options  []toolOption `json:"options" jsonschema:"required,minItems=2,maxItems=5"`
}

type toolOption struct {
	Label       string `json:"label" jsonschema:"required"`
	Description string `json:"description,omitempty"`
}

func definition(options canonicalOptions, coordinator *coordinator) tools.Definition {
	reflector := jsonschema.Reflector{
		Anonymous: true, DoNotReference: true, AllowAdditionalProperties: false,
		RequiredFromJSONSchemaTags: true,
	}
	return tools.Definition{
		Name:        ToolName,
		Description: "Ask one bounded multiple-choice question with an automatic free-form choice. The result may be selected, custom, dismissed, unavailable, or timed out.",
		Parameters:  einoschema.NewParamsOneOfByJSONSchema(reflector.Reflect(toolInput{})),
		Normalize:   normalizeInput(options),
		Pattern: func(ctx context.Context, _ json.RawMessage) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return PermissionAsk, nil
		},
		Execute: func(ctx context.Context, execution tools.Execution) (json.RawMessage, error) {
			var input toolInput
			if err := json.Unmarshal(execution.Input, &input); err != nil {
				return nil, runtimeError("canonical-input")
			}
			result, err := coordinator.ask(ctx, execution.Call, input)
			if err != nil {
				return nil, err
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				return nil, runtimeError("result-encoding")
			}
			return encoded, nil
		},
		RetrySafe: false, Permissions: []string{PermissionAsk}, Retention: options.retention,
		Metadata: map[string]string{"package": "askuser-v1"},
	}
}

func normalizeInput(options canonicalOptions) tools.InputNormalizer {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !utf8.Valid(raw) {
			return nil, malformed("utf8")
		}
		if !validUnicodeEscapes(raw) {
			return nil, malformed("unicode-escape")
		}
		if !uniqueJSONKeys(raw) {
			return nil, malformed("duplicate-field")
		}
		var input toolInput
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) {
			return nil, malformed("shape")
		}
		if !validText(input.Question, options.limits.MaxQuestionBytes, true) {
			return nil, malformed("question")
		}
		if len(input.Options) < 2 || len(input.Options) > 5 {
			return nil, malformed("option-count")
		}
		seen := make(map[string]struct{}, len(input.Options))
		for index := range input.Options {
			item := input.Options[index]
			if !validText(item.Label, options.limits.MaxOptionLabelBytes, true) {
				return nil, malformed("option-label-" + strconv.Itoa(index+1))
			}
			if !validText(item.Description, options.limits.MaxOptionDescriptionBytes, false) {
				return nil, malformed("option-description-" + strconv.Itoa(index+1))
			}
			if _, exists := seen[item.Label]; exists {
				return nil, malformed("duplicate-option-label")
			}
			seen[item.Label] = struct{}{}
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, malformed("encoding")
		}
		return encoded, nil
	}
}

func uniqueJSONKeys(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !consumeUniqueJSONValue(decoder) {
		return false
	}
	return errors.Is(decoder.Decode(new(any)), io.EOF)
}

func consumeUniqueJSONValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return true
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return false
			}
			if _, duplicate := seen[key]; duplicate {
				return false
			}
			seen[key] = struct{}{}
			if !consumeUniqueJSONValue(decoder) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim('}')
	case '[':
		for decoder.More() {
			if !consumeUniqueJSONValue(decoder) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim(']')
	default:
		return false
	}
}

func validText(value string, maximum int, nonblank bool) bool {
	if !utf8.ValidString(value) || len(value) > maximum || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	return !nonblank || strings.TrimSpace(value) != ""
}

// validUnicodeEscapes losslessly preflights JSON string escapes because
// encoding/json replaces unpaired surrogate escapes with U+FFFD.
func validUnicodeEscapes(raw []byte) bool {
	inString := false
	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || index+1 >= len(raw) {
				continue
			}
			index++
			if raw[index] != 'u' {
				continue
			}
			if index+4 >= len(raw) {
				return false
			}
			first, ok := parseHex4(raw[index+1 : index+5])
			if !ok {
				return false
			}
			index += 4
			if first >= 0xdc00 && first <= 0xdfff {
				return false
			}
			if first >= 0xd800 && first <= 0xdbff {
				if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
					return false
				}
				second, ok := parseHex4(raw[index+3 : index+7])
				if !ok || second < 0xdc00 || second > 0xdfff {
					return false
				}
				index += 6
			}
		}
	}
	return !inString
}

func parseHex4(raw []byte) (uint16, bool) {
	if len(raw) != 4 {
		return 0, false
	}
	var value uint16
	for _, current := range raw {
		value <<= 4
		switch {
		case current >= '0' && current <= '9':
			value += uint16(current - '0')
		case current >= 'a' && current <= 'f':
			value += uint16(current-'a') + 10
		case current >= 'A' && current <= 'F':
			value += uint16(current-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
