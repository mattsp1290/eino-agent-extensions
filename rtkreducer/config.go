package rtkreducer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattsp1290/eino-agent-extensions/toolresultredactor"
	"github.com/mattsp1290/eino-agent/extension"
)

const (
	// DefaultOrder is used when Options.Order is zero. Hosts should leave room
	// after this transform for their final result redactor.
	DefaultOrder = 1000

	rtkVersion                 = "0.48.0"
	environmentContractVersion = "private-env-v1"
	policyVersion              = "rtk-reducer-v1"
	markerVersion              = "marker-v1"

	absoluteMaxBindings     = 256
	absoluteMaxFields       = 256
	absoluteMaxInputMatches = 256
	absoluteMaxConfigBytes  = 1 << 20
	absoluteMaxInputBytes   = 1 << 20
	absoluteMaxResultBytes  = 32 << 20
	absoluteMaxFieldBytes   = 1 << 20
	absoluteMaxJSONDepth    = 128
	absoluteMaxJSONNodes    = 262144
	absoluteMaxConcurrent   = 64
	absoluteMaxStdoutBytes  = 1 << 20
	absoluteMaxStderrBytes  = 64 << 10
	absoluteMaxTimeout      = time.Minute
	absoluteMaxKillWait     = time.Minute
	maxPathBytes            = 4096
	maxExecutableBytes      = 128 << 20
	registrationID          = "tool-result/rtk-reducer"
)

// Mode selects how the selected text is represented in a tool result.
type Mode string

const (
	// TextOnly selects Result.Output. Structured must be absent.
	TextOnly Mode = "text-only"
	// JSONMirror selects mirrored JSON bytes in Result.Output and Structured.
	JSONMirror Mode = "json-mirror"
)

// Filter is one of the fixed RTK filters supported by this package.
type Filter string

const (
	GoTest  Filter = "go-test"
	GitDiff Filter = "git-diff"
	Log     Filter = "log"
)

// Field identifies one top-level result string and its fixed RTK filter.
// TextOnly bindings use one field with an empty Name.
type Field struct {
	Name   string
	Filter Filter
}

// InputMatch restricts a binding to exact values of one top-level string in
// the canonical JSON tool input. Values are compared byte-for-byte; no shell
// parsing, globbing, regular expressions, or whitespace normalization occurs.
type InputMatch struct {
	Field  string
	Equals []string
}

// Binding maps one model/runtime tool name to one result representation.
type Binding struct {
	ToolName   string
	Mode       Mode
	MatchInput *InputMatch
	Fields     []Field
}

// Limits bounds policy and callback-owned work. Every field is required and
// must be positive. Hosts must choose every bound explicitly so upgrades never
// opt existing deployments into newly introduced resource budgets. Values
// above the package's absolute guards are rejected.
type Limits struct {
	MaxBindings         int
	MaxFieldsPerBinding int
	MaxInputMatches     int
	MaxConfigBytes      int
	MaxInputBytes       int
	MaxResultBytes      int
	MaxFieldBytes       int
	MaxJSONDepth        int
	MaxJSONNodes        int
	MaxConcurrent       int
	MaxStdoutBytes      int
	MaxStderrBytes      int
	Timeout             time.Duration
	KillWait            time.Duration
}

// Options describes one immutable RTK reducer mount.
type Options struct {
	Scope            extension.Scope
	Order            int
	ExecutablePath   string
	ExecutableSHA256 string
	TempRoot         string
	Bindings         []Binding
	Limits           Limits
}

type canonicalOptions struct {
	scope            extension.Scope
	order            int
	executablePath   string
	executableSHA256 string
	tempRoot         string
	bindings         []canonicalBinding
	limits           Limits
}

type canonicalBinding struct {
	toolName   string
	mode       Mode
	matchInput *canonicalInputMatch
	fields     []Field
}

type canonicalInputMatch struct {
	field  string
	equals []string
}

type hashPolicy struct {
	Version             string             `json:"version"`
	RTKVersion          string             `json:"rtk_version"`
	EnvironmentContract string             `json:"environment_contract"`
	MarkerVersion       string             `json:"marker_version"`
	ExecutableSHA256    string             `json:"executable_sha256"`
	Limits              Limits             `json:"limits"`
	Bindings            []canonicalBinding `json:"bindings"`
}

// ConfigHash validates and hashes behavior-bearing policy. It does not read,
// resolve, execute, or otherwise inspect ExecutablePath or TempRoot.
func ConfigHash(options Options) (string, error) {
	canonical, err := canonicalize(options)
	if err != nil {
		return "", err
	}
	return configHash(canonical)
}

func configHash(options canonicalOptions) (string, error) {
	raw, err := json.Marshal(hashPolicy{
		Version: policyVersion, RTKVersion: rtkVersion,
		EnvironmentContract: environmentContractVersion, MarkerVersion: markerVersion,
		ExecutableSHA256: options.executableSHA256, Limits: options.limits,
		Bindings: options.bindings,
	})
	if err != nil {
		return "", configError("hash-encoding")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalize(options Options) (canonicalOptions, error) {
	result := canonicalOptions{
		scope: options.Scope, order: options.Order,
		executablePath: options.ExecutablePath, executableSHA256: options.ExecutableSHA256,
		tempRoot: options.TempRoot, limits: options.Limits,
	}
	if result.scope == (extension.Scope{}) {
		result.scope = extension.GlobalScope()
	}
	if result.order == 0 {
		result.order = DefaultOrder
	}
	if err := extension.ValidateScope(result.scope); err != nil {
		return canonicalOptions{}, configError("scope")
	}
	if result.order >= toolresultredactor.LateOrder {
		return canonicalOptions{}, configError("order-too-late")
	}
	if err := validateLimits(result.limits); err != nil {
		return canonicalOptions{}, err
	}
	if err := validatePathSyntax(result.executablePath, "executable-path"); err != nil {
		return canonicalOptions{}, err
	}
	if err := validatePathSyntax(result.tempRoot, "temp-root"); err != nil {
		return canonicalOptions{}, err
	}
	if len(result.executableSHA256) != sha256.Size*2 || result.executableSHA256 != strings.ToLower(result.executableSHA256) {
		return canonicalOptions{}, configError("executable-digest")
	}
	if _, err := hex.DecodeString(result.executableSHA256); err != nil {
		return canonicalOptions{}, configError("executable-digest")
	}
	if len(options.Bindings) == 0 {
		return canonicalOptions{}, configError("bindings-required")
	}
	if len(options.Bindings) > result.limits.MaxBindings || len(options.Bindings) > absoluteMaxBindings {
		return canonicalOptions{}, configError("binding-count")
	}
	if err := validateRawConfigSize(options, result.limits.MaxConfigBytes); err != nil {
		return canonicalOptions{}, err
	}

	// Validate all caller-owned slices before allocating the canonical copies.
	seenTools := make(map[string]struct{}, len(options.Bindings))
	for _, binding := range options.Bindings {
		if extension.ValidateIdentifier(binding.ToolName) != nil {
			return canonicalOptions{}, configError("tool-name")
		}
		if _, exists := seenTools[binding.ToolName]; exists {
			return canonicalOptions{}, configError("duplicate-tool")
		}
		seenTools[binding.ToolName] = struct{}{}
		if binding.Mode != TextOnly && binding.Mode != JSONMirror {
			return canonicalOptions{}, configError("mode")
		}
		if binding.Mode == TextOnly && (len(binding.Fields) != 1 || binding.Fields[0].Name != "") {
			return canonicalOptions{}, configError("text-fields")
		}
		if binding.Mode == JSONMirror && len(binding.Fields) == 0 {
			return canonicalOptions{}, configError("json-fields")
		}
		if len(binding.Fields) > result.limits.MaxFieldsPerBinding || len(binding.Fields) > absoluteMaxFields {
			return canonicalOptions{}, configError("field-count")
		}
		if binding.MatchInput != nil {
			if err := validateInputMatch(*binding.MatchInput, result.limits); err != nil {
				return canonicalOptions{}, err
			}
		}
		seenFields := make(map[string]struct{}, len(binding.Fields))
		for _, field := range binding.Fields {
			if binding.Mode == JSONMirror && !validJSONFieldName(field.Name) {
				return canonicalOptions{}, configError("field-name")
			}
			if binding.Mode == TextOnly && field.Name != "" {
				return canonicalOptions{}, configError("text-field-name")
			}
			if _, exists := seenFields[field.Name]; exists {
				return canonicalOptions{}, configError("duplicate-field")
			}
			seenFields[field.Name] = struct{}{}
			if !validFilter(field.Filter) {
				return canonicalOptions{}, configError("filter")
			}
		}
	}

	result.bindings = make([]canonicalBinding, 0, len(options.Bindings))
	for _, binding := range options.Bindings {
		copyBinding := canonicalBinding{toolName: binding.ToolName, mode: binding.Mode}
		if binding.MatchInput != nil {
			copyBinding.matchInput = &canonicalInputMatch{field: binding.MatchInput.Field, equals: append([]string(nil), binding.MatchInput.Equals...)}
			sort.Strings(copyBinding.matchInput.equals)
		}
		copyBinding.fields = append([]Field(nil), binding.Fields...)
		sort.Slice(copyBinding.fields, func(i, j int) bool {
			if copyBinding.fields[i].Name != copyBinding.fields[j].Name {
				return copyBinding.fields[i].Name < copyBinding.fields[j].Name
			}
			return copyBinding.fields[i].Filter < copyBinding.fields[j].Filter
		})
		result.bindings = append(result.bindings, copyBinding)
	}
	sort.Slice(result.bindings, func(i, j int) bool { return result.bindings[i].toolName < result.bindings[j].toolName })
	return result, nil
}

func validateLimits(limits Limits) error {
	positive := []int{limits.MaxBindings, limits.MaxFieldsPerBinding, limits.MaxInputMatches,
		limits.MaxConfigBytes, limits.MaxInputBytes, limits.MaxResultBytes, limits.MaxFieldBytes,
		limits.MaxJSONDepth, limits.MaxJSONNodes, limits.MaxConcurrent,
		limits.MaxStdoutBytes, limits.MaxStderrBytes}
	for _, value := range positive {
		if value <= 0 {
			return configError("limits")
		}
	}
	if limits.MaxBindings > absoluteMaxBindings || limits.MaxFieldsPerBinding > absoluteMaxFields || limits.MaxInputMatches > absoluteMaxInputMatches ||
		limits.MaxConfigBytes > absoluteMaxConfigBytes || limits.MaxInputBytes > absoluteMaxInputBytes || limits.MaxResultBytes > absoluteMaxResultBytes ||
		limits.MaxFieldBytes > absoluteMaxFieldBytes || limits.MaxJSONDepth > absoluteMaxJSONDepth || limits.MaxJSONNodes > absoluteMaxJSONNodes ||
		limits.MaxConcurrent > absoluteMaxConcurrent || limits.MaxStdoutBytes > absoluteMaxStdoutBytes || limits.MaxStderrBytes > absoluteMaxStderrBytes {
		return configError("limits")
	}
	if limits.MaxStdoutBytes > limits.MaxFieldBytes || limits.MaxFieldBytes > limits.MaxResultBytes {
		return configError("limit-order")
	}
	if limits.Timeout <= 0 || limits.Timeout > absoluteMaxTimeout || limits.KillWait <= 0 || limits.KillWait > absoluteMaxKillWait {
		return configError("timeouts")
	}
	if limits.Timeout > time.Duration(math.MaxInt64)-limits.KillWait {
		return configError("timeouts")
	}
	return nil
}

func validateRawConfigSize(options Options, max int) error {
	total := 0
	add := func(n int) bool {
		if n < 0 || n > max-total {
			return false
		}
		total += n
		return true
	}
	for _, binding := range options.Bindings {
		if !add(len(binding.ToolName)) {
			return configError("config-bytes")
		}
		for _, field := range binding.Fields {
			if !add(len(field.Name)) || !add(len(field.Filter)) {
				return configError("config-bytes")
			}
		}
		if binding.MatchInput != nil {
			if !add(len(binding.MatchInput.Field)) {
				return configError("config-bytes")
			}
			for _, value := range binding.MatchInput.Equals {
				if !add(len(value)) {
					return configError("config-bytes")
				}
			}
		}
	}
	return nil
}

func validateInputMatch(match InputMatch, limits Limits) error {
	if !validJSONFieldName(match.Field) {
		return configError("match-field")
	}
	if len(match.Equals) == 0 || len(match.Equals) > limits.MaxInputMatches || len(match.Equals) > absoluteMaxInputMatches {
		return configError("input-match-count")
	}
	seen := make(map[string]struct{}, len(match.Equals))
	for _, value := range match.Equals {
		if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || len(value) > limits.MaxInputBytes {
			return configError("input-match-value")
		}
		if _, exists := seen[value]; exists {
			return configError("duplicate-input-match")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validJSONFieldName(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0
}

func validFilter(filter Filter) bool {
	return filter == GoTest || filter == GitDiff || filter == Log
}

func validatePathSyntax(path, code string) error {
	if path == "" || len(path) > maxPathBytes || !utf8.ValidString(path) || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return configError(code)
	}
	return nil
}

func (b canonicalBinding) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ToolName   string               `json:"tool_name"`
		Mode       Mode                 `json:"mode"`
		MatchInput *canonicalInputMatch `json:"match_input,omitempty"`
		Fields     []Field              `json:"fields"`
	}{ToolName: b.toolName, Mode: b.mode, MatchInput: b.matchInput, Fields: b.fields})
}

func (m canonicalInputMatch) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Field  string   `json:"field"`
		Equals []string `json:"equals"`
	}{Field: m.field, Equals: m.equals})
}
