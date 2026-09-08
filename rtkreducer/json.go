package rtkreducer

import (
	"bytes"
	"encoding/json"
	"unicode/utf16"
	"unicode/utf8"
)

// stringSpan describes one top-level JSON string value. start and end cover
// the encoded token, including its quotes, so callers can replace it without
// re-encoding any surrounding data.
type stringSpan struct {
	start int
	end   int
	value string
}

type objectScanner struct {
	raw       []byte
	position  int
	nodes     int
	maxDepth  int
	maxNodes  int
	requested map[string]struct{}
	found     map[string]stringSpan
}

func scanObjectStrings(raw []byte, requested map[string]struct{}, maxDepth, maxNodes int) (map[string]stringSpan, bool) {
	if len(raw) == 0 || maxDepth <= 0 || maxNodes <= 0 || !json.Valid(raw) || !utf8.Valid(raw) {
		return nil, false
	}
	scanner := objectScanner{
		raw: raw, maxDepth: maxDepth, maxNodes: maxNodes,
		requested: requested, found: make(map[string]stringSpan, len(requested)),
	}
	if !scanner.parseValue(1, true) {
		return nil, false
	}
	scanner.skipSpace()
	if scanner.position != len(raw) {
		return nil, false
	}
	return scanner.found, true
}

func (s *objectScanner) parseValue(depth int, root bool) bool {
	if depth > s.maxDepth || !s.consumeNode() {
		return false
	}
	s.skipSpace()
	if s.position >= len(s.raw) {
		return false
	}
	switch s.raw[s.position] {
	case '{':
		return s.parseObject(depth, root)
	case '[':
		if root {
			return false
		}
		return s.parseArray(depth)
	case '"':
		if root {
			return false
		}
		_, _, _, ok := s.parseStringLiteral()
		return ok
	default:
		if root {
			return false
		}
		return s.parsePrimitive()
	}
}

func (s *objectScanner) parseObject(depth int, root bool) bool {
	s.position++
	s.skipSpace()
	if s.position < len(s.raw) && s.raw[s.position] == '}' {
		s.position++
		return true
	}
	seen := make(map[string]struct{})
	for {
		if !s.consumeNode() {
			return false
		}
		s.skipSpace()
		if s.position >= len(s.raw) || s.raw[s.position] != '"' {
			return false
		}
		_, _, key, ok := s.parseStringLiteral()
		if !ok {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
		s.skipSpace()
		if s.position >= len(s.raw) || s.raw[s.position] != ':' {
			return false
		}
		s.position++
		s.skipSpace()
		if root {
			if _, wanted := s.requested[key]; wanted {
				if depth+1 > s.maxDepth || !s.consumeNode() || s.position >= len(s.raw) || s.raw[s.position] != '"' {
					return false
				}
				start, end, value, valid := s.parseStringLiteral()
				if !valid {
					return false
				}
				s.found[key] = stringSpan{start: start, end: end, value: value}
			} else if !s.parseValue(depth+1, false) {
				return false
			}
		} else if !s.parseValue(depth+1, false) {
			return false
		}
		s.skipSpace()
		if s.position >= len(s.raw) {
			return false
		}
		switch s.raw[s.position] {
		case '}':
			s.position++
			return true
		case ',':
			s.position++
		default:
			return false
		}
	}
}

func (s *objectScanner) parseArray(depth int) bool {
	s.position++
	s.skipSpace()
	if s.position < len(s.raw) && s.raw[s.position] == ']' {
		s.position++
		return true
	}
	for {
		if !s.parseValue(depth+1, false) {
			return false
		}
		s.skipSpace()
		if s.position >= len(s.raw) {
			return false
		}
		switch s.raw[s.position] {
		case ']':
			s.position++
			return true
		case ',':
			s.position++
		default:
			return false
		}
	}
}

func (s *objectScanner) parsePrimitive() bool {
	start := s.position
	for s.position < len(s.raw) {
		switch s.raw[s.position] {
		case ' ', '\t', '\r', '\n', ',', '}', ']':
			return s.position > start
		default:
			s.position++
		}
	}
	return s.position > start
}

func (s *objectScanner) parseStringLiteral() (int, int, string, bool) {
	start := s.position
	s.position++
	for s.position < len(s.raw) {
		switch current := s.raw[s.position]; {
		case current == '"':
			s.position++
			literal := s.raw[start:s.position]
			if !validJSONStringEncoding(literal) {
				return start, s.position, "", false
			}
			var decoded string
			if json.Unmarshal(literal, &decoded) != nil || !utf8.ValidString(decoded) {
				return start, s.position, "", false
			}
			return start, s.position, decoded, true
		case current == '\\':
			s.position += 2
		case current < 0x20:
			return start, s.position, "", false
		default:
			_, size := utf8.DecodeRune(s.raw[s.position:])
			if size == 0 {
				return start, s.position, "", false
			}
			s.position += size
		}
	}
	return start, s.position, "", false
}

func (s *objectScanner) consumeNode() bool {
	if s.nodes >= s.maxNodes {
		return false
	}
	s.nodes++
	return true
}

func (s *objectScanner) skipSpace() {
	for s.position < len(s.raw) {
		switch s.raw[s.position] {
		case ' ', '\t', '\r', '\n':
			s.position++
		default:
			return
		}
	}
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
