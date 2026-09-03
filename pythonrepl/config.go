package pythonrepl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
)

const (
	maxIdentityBytes      = 256
	maxToolInlineBytes    = int64(16 << 20)
	configurationVersion  = "python-repl-config-v1"
	runnerProtocolVersion = "python-repl-runner-v1"
	resultSchemaVersion   = "python-repl-result-v1"
	truncationVersion     = "utf8-prefix-v1"
	executeRegistrationID = "python-repl/execute"
	clearRegistrationID   = "python-repl/clear"
)

// Environment is the explicit environment frozen at mount. Identity is a
// non-secret host version and must rotate whenever any effective value changes.
type Environment struct {
	Entries  map[string]string
	Identity string
}

// String returns a diagnostic summary that excludes keys and values.
func (environment Environment) String() string {
	return fmt.Sprintf("pythonrepl environment (entries=%d)", len(environment.Entries))
}

// GoString returns the same non-sensitive diagnostic summary as String.
func (environment Environment) GoString() string { return environment.String() }

// Limits bounds every package-owned resource and deadline. Every field must be
// positive; DefaultTimeout and MaxTimeout must be whole seconds.
type Limits struct {
	MaxSessions             int
	MaxQueuedPerSession     int
	MaxCodeBytes            int
	MaxOutputBytesPerStream int
	MaxResultBytes          int
	MaxExceptionBytes       int
	MaxEnvironmentEntries   int
	MaxEnvironmentBytes     int
	DefaultTimeout          time.Duration
	MaxTimeout              time.Duration
	VenvCreateTimeout       time.Duration
	RunnerStartTimeout      time.Duration
	TerminateGrace          time.Duration
	KillWait                time.Duration
}

// Options describes one immutable Python REPL mount.
type Options struct {
	Scope          extension.Scope
	Order          int
	PythonPath     string
	PythonIdentity string
	TempRoot       string
	Environment    Environment
	Limits         Limits
}

// String returns a diagnostic summary that excludes paths and environment data.
func (options Options) String() string {
	return fmt.Sprintf("pythonrepl options (environment_entries=%d)", len(options.Environment.Entries))
}

// GoString returns the same non-sensitive diagnostic summary as String.
func (options Options) GoString() string { return options.String() }

type canonicalOptions struct {
	scope          extension.Scope
	order          int
	pythonPath     string
	pythonIdentity string
	tempRoot       string
	environment    Environment
	env            []string
	limits         Limits
	retention      map[string]runtime.RetentionPolicy
	requestMax     uint32
	responseMax    uint32
	hooks          *testHooks
}

type hashPolicy struct {
	Version          string   `json:"version"`
	Protocol         string   `json:"protocol"`
	RunnerDigest     string   `json:"runner_digest"`
	SupervisorDigest string   `json:"supervisor_digest"`
	ResultSchema     string   `json:"result_schema"`
	Truncation       string   `json:"truncation"`
	Tools            []string `json:"tools"`
	Registrations    []string `json:"registrations"`
	PythonPath       string   `json:"python_path"`
	PythonIdentity   string   `json:"python_identity"`
	TempRoot         string   `json:"temp_root"`
	EnvironmentKeys  []string `json:"environment_keys"`
	EnvironmentID    string   `json:"environment_identity"`
	Limits           Limits   `json:"limits"`
}

// ConfigHash returns the deterministic identity of behavior-bearing options.
// It intentionally excludes environment values, scope, and order.
func ConfigHash(options Options) (string, error) {
	canonical, err := canonicalize(options)
	if err != nil {
		return "", err
	}
	return configHash(canonical)
}

func configHash(options canonicalOptions) (string, error) {
	runnerDigest := sha256.Sum256([]byte(fixedRunnerSource))
	supervisorDigest := sha256.Sum256([]byte(fixedSupervisorSource))
	keys := make([]string, 0, len(options.env))
	for _, entry := range options.env {
		key, _, _ := strings.Cut(entry, "=")
		keys = append(keys, key)
	}
	raw, err := json.Marshal(hashPolicy{
		Version: configurationVersion, Protocol: runnerProtocolVersion,
		RunnerDigest: hex.EncodeToString(runnerDigest[:]), SupervisorDigest: hex.EncodeToString(supervisorDigest[:]),
		ResultSchema: resultSchemaVersion, Truncation: truncationVersion,
		Tools: []string{ExecuteToolName, ClearToolName}, Registrations: []string{executeRegistrationID, clearRegistrationID},
		PythonPath: options.pythonPath, PythonIdentity: options.pythonIdentity, TempRoot: options.tempRoot,
		EnvironmentKeys: keys, EnvironmentID: options.environment.Identity, Limits: options.limits,
	})
	if err != nil {
		return "", configError("hash-encoding")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalize(options Options) (canonicalOptions, error) {
	result := canonicalOptions{
		scope: options.Scope, order: options.Order, pythonIdentity: options.PythonIdentity,
		environment: Environment{Identity: options.Environment.Identity}, limits: options.Limits,
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
	if !platformSupported() {
		return canonicalOptions{}, configError("unsupported-platform")
	}
	if err := validateLimits(result.limits); err != nil {
		return canonicalOptions{}, err
	}
	if !validIdentity(options.PythonIdentity) {
		return canonicalOptions{}, configError("python-identity")
	}
	if !validIdentity(options.Environment.Identity) {
		return canonicalOptions{}, configError("environment-identity")
	}
	var err error
	result.pythonPath, err = canonicalExecutable(options.PythonPath)
	if err != nil {
		return canonicalOptions{}, err
	}
	result.tempRoot, err = canonicalDirectory(options.TempRoot)
	if err != nil {
		return canonicalOptions{}, err
	}
	result.env, result.environment.Entries, err = canonicalEnvironment(options.Environment, result.limits)
	if err != nil {
		return canonicalOptions{}, err
	}
	result.retention, err = retentionPolicies(result.limits)
	if err != nil {
		return canonicalOptions{}, err
	}
	requestMax, responseMax, err := protocolMaxima(result.limits)
	if err != nil {
		return canonicalOptions{}, err
	}
	result.requestMax, result.responseMax = requestMax, responseMax
	return result, nil
}

func (options canonicalOptions) String() string {
	return fmt.Sprintf("pythonrepl canonical options (environment_entries=%d)", len(options.env))
}

func (options canonicalOptions) GoString() string { return options.String() }

func validateLimits(limits Limits) error {
	values := []int{
		limits.MaxSessions, limits.MaxQueuedPerSession, limits.MaxCodeBytes,
		limits.MaxOutputBytesPerStream, limits.MaxResultBytes, limits.MaxExceptionBytes,
		limits.MaxEnvironmentEntries, limits.MaxEnvironmentBytes,
	}
	for _, value := range values {
		if value <= 0 {
			return configError("limits")
		}
	}
	if limits.DefaultTimeout < time.Second || limits.DefaultTimeout%time.Second != 0 ||
		limits.MaxTimeout < time.Second || limits.MaxTimeout%time.Second != 0 ||
		limits.DefaultTimeout > limits.MaxTimeout {
		return configError("execution-timeout")
	}
	for _, value := range []time.Duration{limits.VenvCreateTimeout, limits.RunnerStartTimeout, limits.TerminateGrace, limits.KillWait} {
		if value <= 0 {
			return configError("deadline")
		}
	}
	if limits.KillWait > (time.Duration(math.MaxInt64)-time.Second)/2 ||
		limits.TerminateGrace > time.Duration(math.MaxInt64)-2*limits.KillWait-time.Second {
		return configError("termination-bounds")
	}
	return nil
}

func validIdentity(value string) bool {
	if value == "" || len(value) > maxIdentityBytes || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func canonicalExecutable(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", configError("python-path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", configError("python-path")
	}
	resolved, err = filepath.Abs(filepath.Clean(resolved))
	if err != nil {
		return "", configError("python-path")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", configError("python-executable")
	}
	return resolved, nil
}

func canonicalDirectory(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", configError("temp-root")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", configError("temp-root")
	}
	resolved, err = filepath.Abs(filepath.Clean(resolved))
	if err != nil {
		return "", configError("temp-root")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", configError("temp-root")
	}
	return resolved, nil
}

func canonicalEnvironment(environment Environment, limits Limits) ([]string, map[string]string, error) {
	if len(environment.Entries) > limits.MaxEnvironmentEntries {
		return nil, nil, configError("environment-entries")
	}
	keys := make([]string, 0, len(environment.Entries))
	copyValues := make(map[string]string, len(environment.Entries))
	total := 0
	for key, value := range environment.Entries {
		if key == "" || !utf8.ValidString(key) || !utf8.ValidString(value) || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 {
			return nil, nil, configError("environment-entry")
		}
		entryBytes := len(key) + 1 + len(value)
		if entryBytes > limits.MaxEnvironmentBytes-total {
			return nil, nil, configError("environment-bytes")
		}
		total += entryBytes
		keys = append(keys, key)
		copyValues[key] = value
	}
	sort.Strings(keys)
	encoded := make([]string, 0, len(keys))
	for _, key := range keys {
		encoded = append(encoded, key+"="+copyValues[key])
	}
	return encoded, copyValues, nil
}

func retentionPolicies(limits Limits) (map[string]runtime.RetentionPolicy, error) {
	base := func(status string) int64 {
		encoded, _ := json.Marshal(ExecuteResult{
			Status: status, Generation: math.MaxUint64, StateReset: true, StateResetReason: "runner_failed",
		})
		return int64(len(encoded))
	}
	completedText := int64(limits.MaxOutputBytesPerStream)*2 + int64(limits.MaxResultBytes)
	errorText := int64(limits.MaxOutputBytesPerStream)*2 + int64(limits.MaxExceptionBytes)
	if completedText <= 0 || errorText <= 0 || completedText > math.MaxInt64/6 || errorText > math.MaxInt64/6 {
		return nil, configError("result-retention")
	}
	completedOne := base("completed") + 6*completedText
	errorOne := base("python_error") + 6*errorText
	executeOne := completedOne
	if errorOne > executeOne {
		executeOne = errorOne
	}
	if executeOne < 0 {
		return nil, configError("result-retention")
	}
	clearBase, _ := json.Marshal(ClearResult{HadState: false, Generation: math.MaxUint64})
	result := make(map[string]runtime.RetentionPolicy, 2)
	for name, oneCopy := range map[string]int64{ExecuteToolName: executeOne, ClearToolName: int64(len(clearBase))} {
		if oneCopy > math.MaxInt64/2 || oneCopy*2 > maxToolInlineBytes {
			return nil, configError("result-retention")
		}
		result[name] = runtime.RetentionPolicy{MaxInlineBytes: oneCopy * 2, StoreExternal: false, Redact: false}
	}
	return result, nil
}

func protocolMaxima(limits Limits) (uint32, uint32, error) {
	request := int64(128) + int64(limits.MaxCodeBytes)*6
	textBytes := int64(limits.MaxOutputBytesPerStream)*2 + int64(limits.MaxResultBytes) + int64(limits.MaxExceptionBytes)
	if textBytes <= 0 || textBytes > (math.MaxInt64-512)/6 {
		return 0, 0, configError("protocol-bounds")
	}
	response := int64(512) + int64(6)*textBytes
	if request <= 0 || response <= 0 || request > math.MaxUint32 || response > math.MaxUint32 {
		return 0, 0, configError("protocol-bounds")
	}
	return uint32(request), uint32(response), nil
}
