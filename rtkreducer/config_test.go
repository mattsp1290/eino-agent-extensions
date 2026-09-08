package rtkreducer

import (
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent-extensions/toolresultredactor"
	"github.com/mattsp1290/eino-agent/extension"
)

func testLimits() Limits {
	return Limits{
		MaxBindings: 16, MaxFieldsPerBinding: 4, MaxInputMatches: 16,
		MaxConfigBytes: 64 << 10, MaxInputBytes: 64 << 10,
		MaxResultBytes: 2 << 20, MaxFieldBytes: 256 << 10,
		MaxJSONDepth: 32, MaxJSONNodes: 16384, MaxConcurrent: 4,
		MaxStdoutBytes: 256 << 10, MaxStderrBytes: 8 << 10,
		Timeout: 2 * time.Second, KillWait: time.Second,
	}
}

func testOptions() Options {
	return Options{
		ExecutablePath:   "/tmp/rtk",
		ExecutableSHA256: strings.Repeat("a", 64),
		TempRoot:         "/tmp",
		Bindings: []Binding{{
			ToolName: "shell", Mode: JSONMirror,
			MatchInput: &InputMatch{Field: "cmd", Equals: []string{"go test -json ./...", "go test -json ./pkg"}},
			Fields:     []Field{{Name: "stdout", Filter: GoTest}},
		}},
		Limits: testLimits(),
	}
}

func TestConfigHashCanonicalizesPolicyOrdering(t *testing.T) {
	left := testOptions()
	left.Scope = extension.SessionScope("different")
	left.Order = 42
	left.Bindings[0].MatchInput.Equals = []string{"go test -json ./pkg", "go test -json ./..."}
	right := testOptions()
	right.ExecutablePath = "/var/empty/relocated-rtk"
	right.TempRoot = "/var/empty/tmp"

	leftHash, err := ConfigHash(left)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := ConfigHash(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatalf("equivalent policy hashes differ: %s != %s", leftHash, rightHash)
	}
}

func TestConfigHashChangesForBehaviorBearingOptions(t *testing.T) {
	base := testOptions()
	baseHash, err := ConfigHash(base)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Options){
		"digest": func(value *Options) { value.ExecutableSHA256 = strings.Repeat("b", 64) },
		"mode": func(value *Options) {
			value.Bindings[0].Mode = TextOnly
			value.Bindings[0].MatchInput = nil
			value.Bindings[0].Fields = []Field{{Filter: Log}}
		},
		"tool":   func(value *Options) { value.Bindings[0].ToolName = "other-tool" },
		"filter": func(value *Options) { value.Bindings[0].Fields[0].Filter = GitDiff },
		"match":  func(value *Options) { value.Bindings[0].MatchInput.Equals = []string{"other"} },
		"limits": func(value *Options) { value.Limits.MaxFieldBytes++ },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.Bindings = append([]Binding(nil), base.Bindings...)
			changed.Bindings[0] = base.Bindings[0]
			changed.Bindings[0].Fields = append([]Field(nil), base.Bindings[0].Fields...)
			if base.Bindings[0].MatchInput != nil {
				match := *base.Bindings[0].MatchInput
				match.Equals = append([]string(nil), match.Equals...)
				changed.Bindings[0].MatchInput = &match
			}
			mutate(&changed)
			hash, err := ConfigHash(changed)
			if err != nil {
				t.Fatal(err)
			}
			if hash == baseHash {
				t.Fatalf("behavior-bearing %s change retained hash", name)
			}
		})
	}
}

func TestConfigValidationRejectsInvalidPolicy(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Options)
		code string
	}{
		{"no bindings", func(value *Options) { value.Bindings = nil }, "bindings-required"},
		{"invalid digest", func(value *Options) { value.ExecutableSHA256 = "SECRET" }, "executable-digest"},
		{"too late", func(value *Options) { value.Order = toolresultredactor.LateOrder }, "order-too-late"},
		{"duplicate tool", func(value *Options) { value.Bindings = append(value.Bindings, value.Bindings[0]) }, "duplicate-tool"},
		{"duplicate field", func(value *Options) {
			value.Bindings[0].Fields = append(value.Bindings[0].Fields, value.Bindings[0].Fields[0])
		}, "duplicate-field"},
		{"duplicate input", func(value *Options) {
			value.Bindings[0].MatchInput.Equals = append(value.Bindings[0].MatchInput.Equals, "go test -json ./...")
		}, "duplicate-input-match"},
		{"unknown filter", func(value *Options) { value.Bindings[0].Fields[0].Filter = "secret-filter" }, "filter"},
		{"oversized config", func(value *Options) { value.Limits.MaxConfigBytes = 1; value.Bindings[0].ToolName = "long-tool-name" }, "config-bytes"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			options := testOptions()
			// The edits above intentionally mutate nested values.
			options.Bindings = append([]Binding(nil), options.Bindings...)
			options.Bindings[0].Fields = append([]Field(nil), options.Bindings[0].Fields...)
			if options.Bindings[0].MatchInput != nil {
				match := *options.Bindings[0].MatchInput
				match.Equals = append([]string(nil), match.Equals...)
				options.Bindings[0].MatchInput = &match
			}
			test.edit(&options)
			_, err := ConfigHash(options)
			if err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("error = %v, want code %q", err, test.code)
			}
		})
	}
}

func TestConfigHashDoesNotInspectPaths(t *testing.T) {
	options := testOptions()
	options.ExecutablePath = "/does/not/exist/rtk"
	options.TempRoot = "/does/not/exist/tmp"
	if _, err := ConfigHash(options); err != nil {
		t.Fatalf("pure hash inspected filesystem: %v", err)
	}
}
