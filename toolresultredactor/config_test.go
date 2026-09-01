package toolresultredactor

import (
	"strings"
	"testing"

	"github.com/mattsp1290/eino-agent/extension"
)

func testLimits() Limits {
	return Limits{
		MaxFieldBytes: 1024, MaxStructuredBytes: 4096, MaxStructuredDepth: 16,
		MaxStructuredNodes: 128, MaxAttachments: 8, MaxMetadataEntries: 8,
		MaxMatchesPerField: 16, MaxPatterns: 8, MaxPatternBytes: 256,
	}
}

func testOptions() Options {
	return Options{Limits: testLimits(), AdditionalPatterns: []Pattern{{ID: "fixture-rule", Expression: `SYN_[A-Z]{4}`}}}
}

func TestConfigHashCanonicalizesOrder(t *testing.T) {
	left := testOptions()
	left.ExcludedTools = []string{"tool-b", "tool-a", "tool-a"}
	left.AdditionalPatterns = []Pattern{{ID: "z-rule", Expression: `ZZ+`}, {ID: "a-rule", Expression: `AA+`}}
	right := testOptions()
	right.ExcludedTools = []string{"tool-a", "tool-b"}
	right.AdditionalPatterns = []Pattern{{ID: "a-rule", Expression: `AA+`}, {ID: "z-rule", Expression: `ZZ+`}}

	leftHash, err := ConfigHash(left)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := ConfigHash(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatalf("equivalent hashes differ")
	}
	left.Scope = extension.SessionScope("different-registration-identity")
	left.Order = 42
	scopeOrderHash, err := ConfigHash(left)
	if err != nil {
		t.Fatal(err)
	}
	if scopeOrderHash != leftHash {
		t.Fatalf("scope/order unexpectedly entered policy hash")
	}
}

func TestConfigHashChangesForEffectivePolicy(t *testing.T) {
	base := testOptions()
	baseHash, err := ConfigHash(base)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Options){
		"exclusion":          func(value *Options) { value.ExcludedTools = []string{"skip-tool"} },
		"pattern id":         func(value *Options) { value.AdditionalPatterns[0].ID = "other-rule" },
		"pattern expression": func(value *Options) { value.AdditionalPatterns[0].Expression = `OTHER+` },
		"field bytes":        func(value *Options) { value.Limits.MaxFieldBytes++ },
		"structured bytes":   func(value *Options) { value.Limits.MaxStructuredBytes++ },
		"depth":              func(value *Options) { value.Limits.MaxStructuredDepth++ },
		"nodes":              func(value *Options) { value.Limits.MaxStructuredNodes++ },
		"attachments":        func(value *Options) { value.Limits.MaxAttachments++ },
		"metadata":           func(value *Options) { value.Limits.MaxMetadataEntries++ },
		"matches":            func(value *Options) { value.Limits.MaxMatchesPerField++ },
		"patterns":           func(value *Options) { value.Limits.MaxPatterns++ },
		"pattern bytes":      func(value *Options) { value.Limits.MaxPatternBytes++ },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.AdditionalPatterns = append([]Pattern(nil), base.AdditionalPatterns...)
			mutate(&changed)
			hash, err := ConfigHash(changed)
			if err != nil {
				t.Fatal(err)
			}
			if hash == baseHash {
				t.Fatalf("effective policy change retained hash")
			}
		})
	}
}

func TestConfigValidationRejectsUnsafePatternsWithoutEcho(t *testing.T) {
	secretExpression := "PRIVATE_LITERAL_("
	cases := []struct {
		name       string
		pattern    Pattern
		code       string
		maxPattern int
	}{
		{name: "invalid", pattern: Pattern{ID: "safe-id", Expression: secretExpression}, code: "invalid-expression"},
		{name: "empty", pattern: Pattern{ID: "safe-id", Expression: ""}, code: "empty-expression"},
		{name: "anchor", pattern: Pattern{ID: "safe-id", Expression: `^$`}, code: "zero-width-expression"},
		{name: "optional", pattern: Pattern{ID: "safe-id", Expression: `(?:abc)?`}, code: "zero-width-expression"},
		{name: "star", pattern: Pattern{ID: "safe-id", Expression: `(?:abc)*`}, code: "zero-width-expression"},
		{name: "empty alternative", pattern: Pattern{ID: "safe-id", Expression: `abc|`}, code: "zero-width-expression"},
		{name: "nested", pattern: Pattern{ID: "safe-id", Expression: `(?:(?:abc){0,2})`}, code: "zero-width-expression"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			options := testOptions()
			options.AdditionalPatterns = []Pattern{test.pattern}
			_, err := ConfigHash(options)
			if err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("validation error code missing: present=%t", err != nil)
			}
			if strings.Contains(err.Error(), secretExpression) {
				t.Fatalf("configuration error exposed expression")
			}
		})
	}
}

func TestConfigValidationRejectsLimitsAndDuplicateRuleIDs(t *testing.T) {
	options := testOptions()
	options.Limits.MaxFieldBytes = 0
	if _, err := ConfigHash(options); err == nil {
		t.Fatal("zero limit accepted")
	}
	options = testOptions()
	options.AdditionalPatterns = append(options.AdditionalPatterns, options.AdditionalPatterns[0])
	if _, err := ConfigHash(options); err == nil || !strings.Contains(err.Error(), "duplicate-id") {
		t.Fatal("duplicate pattern id accepted")
	}
	options = testOptions()
	options.ExcludedTools = []string{""}
	if _, err := ConfigHash(options); err == nil {
		t.Fatal("empty exclusion accepted")
	}
}
