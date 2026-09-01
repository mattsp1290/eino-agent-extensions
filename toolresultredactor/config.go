package toolresultredactor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"sort"

	"github.com/mattsp1290/eino-agent/extension"
)

const (
	// Placeholder is the fixed replacement for a secret-bearing or unsafe field.
	Placeholder = "[REDACTED]"
	// LateOrder is the default transform order. Hosts should keep the redactor
	// after every other tool-result transform.
	LateOrder = 1_000_000

	builtinCatalogVersion = "builtin-v1"
	placeholderVersion    = "placeholder-v1"
)

// Pattern is one host-supplied RE2 expression. ID is safe diagnostic identity;
// Expression is treated as potentially sensitive and is never echoed in errors.
type Pattern struct {
	ID         string
	Expression string
}

// Limits bounds all package-owned traversal and matching work. Every value is
// required and must be positive.
type Limits struct {
	MaxFieldBytes      int // Maximum UTF-8 bytes in one decoded string key or value.
	MaxStructuredBytes int // Maximum raw bytes in Structured.
	MaxStructuredDepth int // Maximum JSON value nesting depth, counting the root as one.
	MaxStructuredNodes int // Maximum JSON values and object keys visited in Structured.
	MaxAttachments     int // Maximum attachments in one result.
	MaxMetadataEntries int // Maximum entries in each result or attachment metadata map.
	MaxMatchesPerField int // Maximum aggregate matches across all rules for one decoded string.
	MaxPatterns        int // Maximum AdditionalPatterns entries; built-in rules are excluded.
	MaxPatternBytes    int // Maximum bytes in one AdditionalPatterns RE2 expression.
}

// Options describes an immutable redaction policy and its registration.
type Options struct {
	Scope              extension.Scope
	Order              int
	ExcludedTools      []string
	AdditionalPatterns []Pattern
	Limits             Limits
}

type canonicalOptions struct {
	scope              extension.Scope
	order              int
	excludedTools      []string
	additionalPatterns []Pattern
	limits             Limits
}

type hashPolicy struct {
	BuiltinCatalog  string    `json:"builtin_catalog"`
	Placeholder     string    `json:"placeholder"`
	Limits          Limits    `json:"limits"`
	ExcludedTools   []string  `json:"excluded_tools"`
	AdditionalRules []Pattern `json:"additional_rules"`
}

// ConfigHash returns the deterministic SHA-256 identity of the effective
// content policy. Scope and order are recorded separately by Eino and are not
// included in this hash.
func ConfigHash(options Options) (string, error) {
	canonical, err := canonicalize(options)
	if err != nil {
		return "", err
	}
	return configHash(canonical)
}

func configHash(canonical canonicalOptions) (string, error) {
	raw, err := json.Marshal(hashPolicy{
		BuiltinCatalog:  builtinCatalogVersion,
		Placeholder:     placeholderVersion + ":" + Placeholder,
		Limits:          canonical.limits,
		ExcludedTools:   canonical.excludedTools,
		AdditionalRules: canonical.additionalPatterns,
	})
	if err != nil {
		return "", errors.New("tool result redactor configuration invalid: code=hash-encoding")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalize(options Options) (canonicalOptions, error) {
	canonical := canonicalOptions{scope: options.Scope, order: options.Order, limits: options.Limits}
	if canonical.scope == (extension.Scope{}) {
		canonical.scope = extension.GlobalScope()
	}
	if canonical.order == 0 {
		canonical.order = LateOrder
	}
	if err := extension.ValidateScope(canonical.scope); err != nil {
		return canonicalOptions{}, configError("scope")
	}
	if err := validateLimits(canonical.limits); err != nil {
		return canonicalOptions{}, err
	}

	excluded := make(map[string]struct{}, len(options.ExcludedTools))
	for _, name := range options.ExcludedTools {
		if extension.ValidateIdentifier(name) != nil {
			return canonicalOptions{}, configError("excluded-tool")
		}
		excluded[name] = struct{}{}
	}
	canonical.excludedTools = make([]string, 0, len(excluded))
	for name := range excluded {
		canonical.excludedTools = append(canonical.excludedTools, name)
	}
	sort.Strings(canonical.excludedTools)

	if len(options.AdditionalPatterns) > canonical.limits.MaxPatterns {
		return canonicalOptions{}, configError("pattern-count")
	}
	seenIDs := make(map[string]struct{}, len(options.AdditionalPatterns))
	canonical.additionalPatterns = append([]Pattern(nil), options.AdditionalPatterns...)
	for _, pattern := range canonical.additionalPatterns {
		if extension.ValidateIdentifier(pattern.ID) != nil {
			return canonicalOptions{}, configError("pattern-id")
		}
		if _, exists := seenIDs[pattern.ID]; exists {
			return canonicalOptions{}, patternError(pattern.ID, "duplicate-id")
		}
		seenIDs[pattern.ID] = struct{}{}
		if pattern.Expression == "" {
			return canonicalOptions{}, patternError(pattern.ID, "empty-expression")
		}
		if len(pattern.Expression) > canonical.limits.MaxPatternBytes {
			return canonicalOptions{}, patternError(pattern.ID, "expression-too-large")
		}
		parsed, err := syntax.Parse(pattern.Expression, syntax.Perl)
		if err != nil {
			return canonicalOptions{}, patternError(pattern.ID, "invalid-expression")
		}
		if minimumWidth(parsed) == 0 {
			return canonicalOptions{}, patternError(pattern.ID, "zero-width-expression")
		}
		if _, err := regexp.Compile(pattern.Expression); err != nil {
			return canonicalOptions{}, patternError(pattern.ID, "invalid-expression")
		}
	}
	sort.Slice(canonical.additionalPatterns, func(i, j int) bool {
		if canonical.additionalPatterns[i].ID == canonical.additionalPatterns[j].ID {
			return canonical.additionalPatterns[i].Expression < canonical.additionalPatterns[j].Expression
		}
		return canonical.additionalPatterns[i].ID < canonical.additionalPatterns[j].ID
	})
	return canonical, nil
}

func validateLimits(limits Limits) error {
	values := []int{
		limits.MaxFieldBytes, limits.MaxStructuredBytes, limits.MaxStructuredDepth,
		limits.MaxStructuredNodes, limits.MaxAttachments, limits.MaxMetadataEntries,
		limits.MaxMatchesPerField, limits.MaxPatterns, limits.MaxPatternBytes,
	}
	for _, value := range values {
		if value <= 0 {
			return configError("limits")
		}
	}
	return nil
}

func configError(code string) error {
	return fmt.Errorf("tool result redactor configuration invalid: code=%s", code)
}

func patternError(id, code string) error {
	return fmt.Errorf("tool result redactor pattern invalid: id=%s code=%s", id, code)
}

func minimumWidth(expression *syntax.Regexp) int {
	if expression == nil {
		return 0
	}
	switch expression.Op {
	case syntax.OpLiteral:
		return len(expression.Rune)
	case syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return 1
	case syntax.OpCapture:
		return minimumWidth(expression.Sub[0])
	case syntax.OpConcat:
		width := 0
		for _, child := range expression.Sub {
			childWidth := minimumWidth(child)
			if childWidth > int(^uint(0)>>1)-width {
				return int(^uint(0) >> 1)
			}
			width += childWidth
		}
		return width
	case syntax.OpAlternate:
		if len(expression.Sub) == 0 {
			return 0
		}
		width := minimumWidth(expression.Sub[0])
		for _, child := range expression.Sub[1:] {
			if candidate := minimumWidth(child); candidate < width {
				width = candidate
			}
		}
		return width
	case syntax.OpPlus:
		return minimumWidth(expression.Sub[0])
	case syntax.OpRepeat:
		if expression.Min == 0 {
			return 0
		}
		width := minimumWidth(expression.Sub[0])
		maximum := int(^uint(0) >> 1)
		if width > maximum/expression.Min {
			return maximum
		}
		return width * expression.Min
	case syntax.OpQuest, syntax.OpStar, syntax.OpEmptyMatch, syntax.OpBeginLine,
		syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary,
		syntax.OpNoWordBoundary:
		return 0
	default:
		return 0
	}
}
