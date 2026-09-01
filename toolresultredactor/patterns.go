package toolresultredactor

import (
	"regexp"
	"sort"
)

type byteRange struct {
	start int
	end   int
}

type matcher interface {
	find(string, int) []byteRange
}

type regexpMatcher struct {
	expression *regexp.Regexp
}

func (m regexpMatcher) find(value string, limit int) []byteRange {
	indices := m.expression.FindAllStringIndex(value, limit)
	ranges := make([]byteRange, 0, len(indices))
	for _, index := range indices {
		if index[0] != index[1] {
			ranges = append(ranges, byteRange{start: index[0], end: index[1]})
		}
	}
	return ranges
}

type boundaryMatcher struct {
	expression *regexp.Regexp
	leftByte   func(byte) bool
	rightByte  func(byte) bool
}

func (m boundaryMatcher) find(value string, limit int) []byteRange {
	ranges := make([]byteRange, 0, min(limit, len(value)))
	position := 0
	for position < len(value) && len(ranges) < limit {
		index := m.expression.FindStringIndex(value[position:])
		if index == nil {
			break
		}
		index[0] += position
		index[1] += position
		position = index[1]
		if index[0] > 0 && m.leftByte(value[index[0]-1]) {
			continue
		}
		if index[1] < len(value) && m.rightByte(value[index[1]]) {
			continue
		}
		ranges = append(ranges, byteRange{start: index[0], end: index[1]})
	}
	return ranges
}

func compileRules(patterns []Pattern) ([]matcher, error) {
	rules := builtinRules()
	for _, pattern := range patterns {
		expression, err := regexp.Compile(pattern.Expression)
		if err != nil {
			return nil, patternError(pattern.ID, "invalid-expression")
		}
		rules = append(rules, regexpMatcher{expression: expression})
	}
	return rules, nil
}

func builtinRules() []matcher {
	return []matcher{
		regexpMatcher{expression: regexp.MustCompile(`(?ms)^-----BEGIN PRIVATE KEY-----\r?$.*?^-----END PRIVATE KEY-----\r?$`)},
		regexpMatcher{expression: regexp.MustCompile(`(?ms)^-----BEGIN ENCRYPTED PRIVATE KEY-----\r?$.*?^-----END ENCRYPTED PRIVATE KEY-----\r?$`)},
		boundaryMatcher{
			expression: regexp.MustCompile(`[Aa][Uu][Tt][Hh][Oo][Rr][Ii][Zz][Aa][Tt][Ii][Oo][Nn][\t ]*:[\t ]*[Bb][Ee][Aa][Rr][Ee][Rr][ ]+[A-Za-z0-9\-._~+/]+={0,}`),
			leftByte: func(value byte) bool {
				return value == '_' || value == '-' || value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
			},
			rightByte: func(value byte) bool {
				return value == '-' || value == '.' || value == '_' || value == '~' || value == '+' || value == '/' || value == '=' || value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
			},
		},
		boundaryMatcher{
			expression: regexp.MustCompile(`ghs_[0-9]+_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
			leftByte:   githubStatelessTokenByte,
			rightByte:  githubStatelessTokenByte,
		},
		boundaryMatcher{
			expression: regexp.MustCompile(`(?:ghp_|github_pat_|gho_|ghu_|ghs_|ghr_)[A-Za-z0-9_]{16,}`),
			leftByte: func(value byte) bool {
				return value == '_' || value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
			},
			rightByte: func(value byte) bool {
				return value == '_' || value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
			},
		},
	}
}

func githubStatelessTokenByte(value byte) bool {
	return value == '-' || value == '.' || value == '_' ||
		value >= '0' && value <= '9' ||
		value >= 'A' && value <= 'Z' ||
		value >= 'a' && value <= 'z'
}

func mergeRanges(ranges []byteRange) []byteRange {
	if len(ranges) < 2 {
		return ranges
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start == ranges[j].start {
			return ranges[i].end < ranges[j].end
		}
		return ranges[i].start < ranges[j].start
	})
	merged := ranges[:1]
	for _, candidate := range ranges[1:] {
		last := &merged[len(merged)-1]
		if candidate.start <= last.end {
			if candidate.end > last.end {
				last.end = candidate.end
			}
			continue
		}
		merged = append(merged, candidate)
	}
	return merged
}
