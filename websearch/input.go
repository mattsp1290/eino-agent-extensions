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
		if len(raw) > options.limits.MaxRawInputBytes {
			return nil, malformed("raw-input")
		}
		if !utf8.Valid(raw) {
			return nil, malformed("utf8")
		}
		var input toolInput
		decoder := json.NewDecoder(bytes.NewReader(raw))
		opening, err := decoder.Token()
		if err != nil || opening != json.Delim('{') {
			return nil, malformed("shape")
		}
		seenQuery := false
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok || key != "query" {
				return nil, malformed("shape")
			}
			if seenQuery {
				return nil, malformed("duplicate-field")
			}
			if err := decoder.Decode(&input.Query); err != nil {
				return nil, malformed("shape")
			}
			seenQuery = true
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') || !seenQuery || !errors.Is(decoder.Decode(new(any)), io.EOF) {
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
