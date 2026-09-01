package toolresultredactor

import (
	"bytes"
	"context"
	"encoding/json"
	"unicode/utf16"
	"unicode/utf8"
)

type jsonReplacement struct {
	start int
	end   int
	value []byte
}

type jsonWalker struct {
	ctx          context.Context
	policy       *compiledPolicy
	raw          []byte
	position     int
	nodes        int
	replacements []jsonReplacement
}

func (p *compiledPolicy) scanStructured(ctx context.Context, input json.RawMessage) (json.RawMessage, scanState) {
	if input == nil {
		return nil, scanUnchanged
	}
	if len(input) == 0 {
		return input, scanUnchanged
	}
	if ctx.Err() != nil {
		return append(json.RawMessage(nil), structuredPlaceholder...), scanCanceled
	}
	if len(input) > p.limits.MaxStructuredBytes || !json.Valid(input) {
		return append(json.RawMessage(nil), structuredPlaceholder...), scanUnsafe
	}
	walker := jsonWalker{ctx: ctx, policy: p, raw: input}
	state := walker.parseValue(1)
	walker.skipSpace()
	if ctx.Err() != nil {
		return append(json.RawMessage(nil), structuredPlaceholder...), scanCanceled
	}
	if state == scanCanceled {
		return append(json.RawMessage(nil), structuredPlaceholder...), scanCanceled
	}
	if state == scanUnsafe || walker.position != len(input) {
		return append(json.RawMessage(nil), structuredPlaceholder...), scanUnsafe
	}
	if len(walker.replacements) == 0 {
		return append(json.RawMessage(nil), input...), scanUnchanged
	}
	var output bytes.Buffer
	output.Grow(len(input))
	position := 0
	for _, replacement := range walker.replacements {
		output.Write(input[position:replacement.start])
		output.Write(replacement.value)
		position = replacement.end
	}
	output.Write(input[position:])
	return output.Bytes(), scanChanged
}

func (w *jsonWalker) parseValue(depth int) scanState {
	if w.ctx.Err() != nil {
		return scanCanceled
	}
	if depth > w.policy.limits.MaxStructuredDepth {
		return scanUnsafe
	}
	if !w.consumeNode() {
		return scanUnsafe
	}
	w.skipSpace()
	if w.position >= len(w.raw) {
		return scanUnsafe
	}
	switch w.raw[w.position] {
	case '{':
		return w.parseObject(depth)
	case '[':
		return w.parseArray(depth)
	case '"':
		start, end, decoded, valid := w.parseStringLiteral()
		if !valid {
			return scanUnsafe
		}
		redacted, state := w.policy.scanScalar(w.ctx, decoded)
		switch state {
		case scanCanceled:
			return scanCanceled
		case scanUnsafe:
			w.replacements = append(w.replacements, jsonReplacement{start: start, end: end, value: append([]byte(nil), structuredPlaceholder...)})
		case scanChanged:
			replacement, err := json.Marshal(redacted)
			if err != nil {
				return scanUnsafe
			}
			w.replacements = append(w.replacements, jsonReplacement{start: start, end: end, value: replacement})
		}
		return scanUnchanged
	default:
		return w.parsePrimitive()
	}
}

func (w *jsonWalker) parseObject(depth int) scanState {
	w.position++
	w.skipSpace()
	if w.position < len(w.raw) && w.raw[w.position] == '}' {
		w.position++
		return scanUnchanged
	}
	for {
		if w.ctx.Err() != nil {
			return scanCanceled
		}
		if !w.consumeNode() {
			return scanUnsafe
		}
		w.skipSpace()
		if w.position >= len(w.raw) || w.raw[w.position] != '"' {
			return scanUnsafe
		}
		_, _, key, valid := w.parseStringLiteral()
		if !valid {
			return scanUnsafe
		}
		_, keyState := w.policy.scanScalar(w.ctx, key)
		if keyState == scanCanceled {
			return scanCanceled
		}
		if keyState != scanUnchanged {
			return scanUnsafe
		}
		w.skipSpace()
		if w.position >= len(w.raw) || w.raw[w.position] != ':' {
			return scanUnsafe
		}
		w.position++
		if state := w.parseValue(depth + 1); state != scanUnchanged {
			return state
		}
		w.skipSpace()
		if w.position >= len(w.raw) {
			return scanUnsafe
		}
		switch w.raw[w.position] {
		case '}':
			w.position++
			return scanUnchanged
		case ',':
			w.position++
		default:
			return scanUnsafe
		}
	}
}

func (w *jsonWalker) parseArray(depth int) scanState {
	w.position++
	w.skipSpace()
	if w.position < len(w.raw) && w.raw[w.position] == ']' {
		w.position++
		return scanUnchanged
	}
	for {
		if state := w.parseValue(depth + 1); state != scanUnchanged {
			return state
		}
		w.skipSpace()
		if w.position >= len(w.raw) {
			return scanUnsafe
		}
		switch w.raw[w.position] {
		case ']':
			w.position++
			return scanUnchanged
		case ',':
			w.position++
		default:
			return scanUnsafe
		}
	}
}

func (w *jsonWalker) parsePrimitive() scanState {
	start := w.position
	for w.position < len(w.raw) {
		switch w.raw[w.position] {
		case ' ', '\t', '\r', '\n', ',', '}', ']':
			if w.position == start {
				return scanUnsafe
			}
			return scanUnchanged
		default:
			w.position++
		}
	}
	if w.position == start {
		return scanUnsafe
	}
	return scanUnchanged
}

func (w *jsonWalker) parseStringLiteral() (int, int, string, bool) {
	start := w.position
	w.position++
	for w.position < len(w.raw) {
		current := w.raw[w.position]
		switch {
		case current == '"':
			w.position++
			literal := w.raw[start:w.position]
			if !validJSONStringEncoding(literal) {
				return start, w.position, "", false
			}
			var decoded string
			if json.Unmarshal(literal, &decoded) != nil {
				return start, w.position, "", false
			}
			return start, w.position, decoded, true
		case current == '\\':
			w.position += 2
		case current < 0x20:
			return start, w.position, "", false
		default:
			_, size := utf8.DecodeRune(w.raw[w.position:])
			if size == 0 {
				return start, w.position, "", false
			}
			w.position += size
		}
	}
	return start, w.position, "", false
}

func validJSONStringEncoding(literal []byte) bool {
	if len(literal) < 2 || literal[0] != '"' || literal[len(literal)-1] != '"' {
		return false
	}
	for position := 1; position < len(literal)-1; {
		value := literal[position]
		if value == '\\' {
			if position+1 >= len(literal)-1 {
				return false
			}
			escaped := literal[position+1]
			if escaped != 'u' {
				if !bytes.ContainsRune([]byte(`"\\/bfnrt`), rune(escaped)) {
					return false
				}
				position += 2
				continue
			}
			first, ok := parseHexRune(literal, position+2)
			if !ok {
				return false
			}
			position += 6
			if utf16.IsSurrogate(first) {
				if first < 0xD800 || first > 0xDBFF || position+6 > len(literal)-1 || literal[position] != '\\' || literal[position+1] != 'u' {
					return false
				}
				second, ok := parseHexRune(literal, position+2)
				if !ok || second < 0xDC00 || second > 0xDFFF {
					return false
				}
				position += 6
			}
			continue
		}
		if value < 0x20 {
			return false
		}
		decoded, size := utf8.DecodeRune(literal[position : len(literal)-1])
		if decoded == utf8.RuneError && size == 1 {
			return false
		}
		position += size
	}
	return true
}

func parseHexRune(value []byte, start int) (rune, bool) {
	if start+4 > len(value) {
		return 0, false
	}
	var result rune
	for _, digit := range value[start : start+4] {
		result <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			result += rune(digit - '0')
		case digit >= 'a' && digit <= 'f':
			result += rune(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			result += rune(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return result, true
}

func (w *jsonWalker) consumeNode() bool {
	w.nodes++
	return w.nodes <= w.policy.limits.MaxStructuredNodes
}

func (w *jsonWalker) skipSpace() {
	for w.position < len(w.raw) {
		switch w.raw[w.position] {
		case ' ', '\t', '\r', '\n':
			w.position++
		default:
			return
		}
	}
}
