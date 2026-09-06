package delegatetask

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
)

const (
	// ToolName is the model-facing tool name.
	ToolName = "delegate_task"
	// PermissionDelegate is checked by Eino before Runner admission.
	PermissionDelegate = "session.subagent"
	// DefaultOrder is used when Options.Order is zero.
	DefaultOrder = 100

	maxRunnerIdentityBytes    = 256
	maxProfileIdentifierBytes = 256
	configurationVersion      = "delegate-task-config-v1"
	toolSchemaVersion         = "delegate-task-input-v1"
	resultSchemaVersion       = "delegate-task-result-v1"
	permissionVersion         = "delegate-profile-v1"
	registrationID            = ToolName
)

// Limits bounds every package-owned input, output, wait, and concurrency
// resource. Every field is required and must be positive.
type Limits struct {
	MaxTaskBytes    int
	MaxProfileBytes int
	MaxResultBytes  int
	MaxInFlight     int
	MaxWait         time.Duration
}

// Options describes one immutable delegate_task mount. RunnerIdentity is a
// non-secret host version and must change whenever Runner routing or behavior
// changes.
type Options struct {
	Scope          extension.Scope
	Order          int
	Runner         Runner
	RunnerIdentity string
	Limits         Limits
}

type canonicalOptions struct {
	scope          extension.Scope
	order          int
	runner         Runner
	runnerIdentity string
	limits         Limits
	retention      runtime.RetentionPolicy
}

type hashPolicy struct {
	Version           string `json:"version"`
	Tool              string `json:"tool"`
	Registration      string `json:"registration"`
	ToolSchema        string `json:"tool_schema"`
	ResultSchema      string `json:"result_schema"`
	Permission        string `json:"permission"`
	PermissionVersion string `json:"permission_version"`
	Limits            Limits `json:"limits"`
	RunnerIdentity    string `json:"runner_identity"`
}

// ConfigHash returns the deterministic identity of behavior-bearing options.
// It excludes Runner, host state, scope, and order.
func ConfigHash(options Options) (string, error) {
	canonical, err := canonicalize(options)
	if err != nil {
		return "", err
	}
	return configHash(canonical)
}

func configHash(options canonicalOptions) (string, error) {
	raw, err := json.Marshal(hashPolicy{
		Version: configurationVersion, Tool: ToolName, Registration: registrationID,
		ToolSchema: toolSchemaVersion, ResultSchema: resultSchemaVersion,
		Permission: PermissionDelegate, PermissionVersion: permissionVersion,
		Limits: options.limits, RunnerIdentity: options.runnerIdentity,
	})
	if err != nil {
		return "", configError("hash-encoding")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalize(options Options) (canonicalOptions, error) {
	canonical := canonicalOptions{
		scope: options.Scope, order: options.Order, runner: options.Runner,
		runnerIdentity: options.RunnerIdentity, limits: options.Limits,
	}
	if canonical.scope == (extension.Scope{}) {
		canonical.scope = extension.GlobalScope()
	}
	if canonical.order == 0 {
		canonical.order = DefaultOrder
	}
	if err := extension.ValidateScope(canonical.scope); err != nil {
		return canonicalOptions{}, configError("scope")
	}
	if nilRunner(canonical.runner) {
		return canonicalOptions{}, configError("runner-required")
	}
	if !validIdentity(canonical.runnerIdentity) {
		return canonicalOptions{}, configError("runner-identity")
	}
	if err := validateLimits(canonical.limits); err != nil {
		return canonicalOptions{}, err
	}
	retention, err := resultRetention(canonical.limits)
	if err != nil {
		return canonicalOptions{}, err
	}
	canonical.retention = retention
	return canonical, nil
}

func nilRunner(value Runner) bool {
	if value == nil {
		return true
	}
	kind := reflect.ValueOf(value).Kind()
	return (kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface || kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice) && reflect.ValueOf(value).IsNil()
}

func validIdentity(value string) bool {
	if value == "" || len(value) > maxRunnerIdentityBytes || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validateLimits(limits Limits) error {
	if limits.MaxTaskBytes <= 0 || limits.MaxProfileBytes <= 0 || limits.MaxResultBytes <= 0 || limits.MaxInFlight <= 0 {
		return configError("limits")
	}
	if limits.MaxProfileBytes > maxProfileIdentifierBytes {
		return configError("max-profile")
	}
	if limits.MaxWait <= 0 {
		return configError("max-wait")
	}
	return nil
}

func resultRetention(limits Limits) (runtime.RetentionPolicy, error) {
	// encoding/json can expand each accepted byte to a six-byte escape. The
	// fixed allowance covers both status spellings and object punctuation.
	if limits.MaxResultBytes > (math.MaxInt-128)/6 {
		return runtime.RetentionPolicy{}, configError("result-retention")
	}
	oneCopy := int64(128 + limits.MaxResultBytes*6)
	if oneCopy > math.MaxInt64/2 {
		return runtime.RetentionPolicy{}, configError("result-retention")
	}
	return runtime.RetentionPolicy{MaxInlineBytes: oneCopy * 2, StoreExternal: false, Redact: false}, nil
}
