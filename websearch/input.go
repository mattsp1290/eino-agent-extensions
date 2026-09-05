package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/mattsp1290/eino-agent/tools"
)

type toolInput struct {
	Query string `json:"query" jsonschema:"required"`
}

func normalizeInput(options canonicalOptions) tools.InputNormalizer {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !utf8.Valid(raw) {
			return nil, malformed("utf8")
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
		input.Query = strings.TrimSpace(input.Query)
		if input.Query == "" || strings.IndexByte(input.Query, 0) >= 0 || !utf8.ValidString(input.Query) || len(input.Query) > options.limits.MaxQueryBytes {
			return nil, malformed("query")
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, malformed("encoding")
		}
		return encoded, nil
	}
}

func decodeCanonicalInput(raw json.RawMessage) (toolInput, error) {
	var input toolInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return toolInput{}, runtimeError("canonical-input")
	}
	return input, nil
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
