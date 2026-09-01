package backgroundjobs

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
	// DefaultOrder is used when Options.Order is zero.
	DefaultOrder = 100

	EnvironmentExplicitOnly       EnvironmentMode = "explicit-only"
	EnvironmentInheritAndOverride EnvironmentMode = "inherit-and-override"

	maxIdentityBytes     = 256
	maxToolInlineBytes   = int64(16 << 20)
	configurationVersion = "background-jobs-v1"
	supervisorProtocol   = "posix-supervisor-v1"
	startRegistrationID  = "background-job/start"
	statusRegistrationID = "background-job/status"
	listRegistrationID   = "background-job/list"
	killRegistrationID   = "background-job/kill"
)

// EnvironmentMode selects the mount-time environment snapshot policy.
type EnvironmentMode string

// Environment is frozen at mount. Identity is a non-secret host version and
// must change whenever the effective inherited or override environment changes.
type Environment struct {
	Mode      EnvironmentMode
	Overrides map[string]string
	Identity  string
}

// Limits bounds every package-owned resource. All integer fields and all
// durations other than DefaultTimeout are required to be positive.
type Limits struct {
	MaxRunning               int
	MaxTracked               int
	MaxCommandBytes          int
	MaxWorkingDirectoryBytes int
	MaxOutputBytesPerStream  int
	MaxEnvironmentEntries    int
	MaxEnvironmentBytes      int
	DefaultTimeout           time.Duration
	MaxTimeout               time.Duration
	TerminateGrace           time.Duration
	KillWait                 time.Duration
}

// Options describes one immutable background-job mount.
type Options struct {
	Scope         extension.Scope
	Order         int
	ShellPath     string
	ShellIdentity string
	Environment   Environment
	Limits        Limits
}

type canonicalOptions struct {
	scope         extension.Scope
	order         int
	shellPath     string
	shellIdentity string
	environment   Environment
	env           []string
	limits        Limits
	retention     map[string]runtime.RetentionPolicy
}

type hashPolicy struct {
	Version          string          `json:"version"`
	Tools            []string        `json:"tools"`
	Registrations    []string        `json:"registrations"`
	ShellPath        string          `json:"shell_path"`
	ShellIdentity    string          `json:"shell_identity"`
	Supervisor       string          `json:"supervisor_protocol"`
	SupervisorDigest string          `json:"supervisor_digest"`
	Limits           Limits          `json:"limits"`
	EnvironmentMode  EnvironmentMode `json:"environment_mode"`
	EnvironmentID    string          `json:"environment_identity"`
}

// ConfigHash returns the deterministic identity of behavior-bearing mount
// configuration. It intentionally excludes environment values, scope, and order.
func ConfigHash(options Options) (string, error) {
	canonical, err := canonicalize(options)
	if err != nil {
		return "", err
	}
	return configHash(canonical)
}

func configHash(options canonicalOptions) (string, error) {
	scriptDigest := sha256.Sum256([]byte(fixedSupervisorScript))
	raw, err := json.Marshal(hashPolicy{
		Version:       configurationVersion,
		Tools:         []string{StartToolName, StatusToolName, ListToolName, KillToolName},
		Registrations: []string{startRegistrationID, statusRegistrationID, listRegistrationID, killRegistrationID},
		ShellPath:     options.shellPath, ShellIdentity: options.shellIdentity,
		Supervisor: supervisorProtocol, SupervisorDigest: hex.EncodeToString(scriptDigest[:]),
		Limits: options.limits, EnvironmentMode: options.environment.Mode,
		EnvironmentID: options.environment.Identity,
	})
	if err != nil {
		return "", configError("hash-encoding")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalize(options Options) (canonicalOptions, error) {
	result := canonicalOptions{
		scope: options.Scope, order: options.Order, shellIdentity: options.ShellIdentity,
		environment: Environment{Mode: options.Environment.Mode, Identity: options.Environment.Identity},
		limits:      options.Limits,
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
	if err := validateLimits(result.limits); err != nil {
		return canonicalOptions{}, err
	}
	if !validIdentity(options.ShellIdentity) {
		return canonicalOptions{}, configError("shell-identity")
	}
	if !validIdentity(options.Environment.Identity) {
		return canonicalOptions{}, configError("environment-identity")
	}
	resolved, err := canonicalShellPath(options.ShellPath)
	if err != nil {
		return canonicalOptions{}, err
	}
	result.shellPath = resolved

	effective, err := effectiveEnvironment(options.Environment, result.limits)
	if err != nil {
		return canonicalOptions{}, err
	}
	result.env = effective
	result.retention, err = retentionPolicies(result.limits)
	if err != nil {
		return canonicalOptions{}, err
	}
	return result, nil
}

func (options canonicalOptions) String() string {
	return fmt.Sprintf("backgroundjobs canonical options (mode=%s environment_entries=%d)", options.environment.Mode, len(options.env))
}

func (options canonicalOptions) GoString() string { return options.String() }

func validateLimits(limits Limits) error {
	values := []int{
		limits.MaxRunning, limits.MaxTracked, limits.MaxCommandBytes,
		limits.MaxWorkingDirectoryBytes, limits.MaxOutputBytesPerStream,
		limits.MaxEnvironmentEntries, limits.MaxEnvironmentBytes,
	}
	for _, value := range values {
		if value <= 0 {
			return configError("limits")
		}
	}
	if limits.MaxTracked < limits.MaxRunning {
		return configError("tracked-below-running")
	}
	if limits.MaxTimeout < time.Second || limits.MaxTimeout%time.Second != 0 {
		return configError("max-timeout")
	}
	if limits.DefaultTimeout < 0 || limits.DefaultTimeout%time.Second != 0 || limits.DefaultTimeout > limits.MaxTimeout {
		return configError("default-timeout")
	}
	if limits.TerminateGrace <= 0 || limits.KillWait <= 0 {
		return configError("termination-bounds")
	}
	if limits.TerminateGrace > time.Duration(math.MaxInt64)-limits.KillWait-time.Second {
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

func canonicalShellPath(path string) (string, error) {
	if !platformSupported() {
		return "", configError("unsupported-platform")
	}
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", configError("shell-path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", configError("shell-path")
	}
	resolved, err = filepath.Abs(filepath.Clean(resolved))
	if err != nil {
		return "", configError("shell-path")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", configError("shell-executable")
	}
	return resolved, nil
}

func effectiveEnvironment(environment Environment, limits Limits) ([]string, error) {
	if environment.Mode != EnvironmentExplicitOnly && environment.Mode != EnvironmentInheritAndOverride {
		return nil, configError("environment-mode")
	}
	values := make(map[string]string)
	if environment.Mode == EnvironmentInheritAndOverride {
		for _, item := range os.Environ() {
			key, value, ok := strings.Cut(item, "=")
			if ok {
				values[key] = value
			}
		}
	}
	for key, value := range environment.Overrides {
		values[key] = value
	}
	if len(values) > limits.MaxEnvironmentEntries {
		return nil, configError("environment-entries")
	}
	keys := make([]string, 0, len(values))
	total := 0
	for key, value := range values {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 {
			return nil, configError("environment-entry")
		}
		entryBytes := len(key) + 1 + len(value)
		if entryBytes > limits.MaxEnvironmentBytes-total {
			return nil, configError("environment-bytes")
		}
		total += entryBytes
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result, nil
}

func retentionPolicies(limits Limits) (map[string]runtime.RetentionPolicy, error) {
	maxID := "job_" + strings.Repeat("f", 32) + "_" + strings.Repeat("f", 16)
	maxTime := "9999-12-31T23:59:59.999999999Z"
	maxTimeout := limits.MaxTimeout / time.Second
	exitCode := 255
	emptyStatus, _ := json.Marshal(StatusResult{
		ID: maxID, State: JobTimedOut, StartedAt: maxTime, CompletedAt: maxTime,
		TimeoutSeconds: int64(maxTimeout), ExitCode: &exitCode,
		Stdout: TailResult{Truncated: true}, Stderr: TailResult{Truncated: true},
	})
	outputBytes, ok := safeMul(int64(limits.MaxOutputBytesPerStream), 12)
	if !ok {
		return nil, configError("retention-overflow")
	}
	statusBytes, ok := safeAdd(int64(len(emptyStatus)), outputBytes)
	if !ok {
		return nil, configError("retention-overflow")
	}
	summary := JobSummary{ID: maxID, State: JobTimedOut, StartedAt: maxTime, CompletedAt: maxTime, TimeoutSeconds: int64(maxTimeout)}
	encodedSummary, _ := json.Marshal(summary)
	emptyList, _ := json.Marshal(ListResult{Jobs: []JobSummary{}})
	listBytes := int64(len(emptyList))
	if limits.MaxTracked > 0 {
		var added int64
		added, ok = safeMul(int64(limits.MaxTracked), int64(len(encodedSummary)))
		if !ok {
			return nil, configError("retention-overflow")
		}
		listBytes, ok = safeAdd(listBytes, added)
		if !ok {
			return nil, configError("retention-overflow")
		}
		listBytes, ok = safeAdd(listBytes, int64(limits.MaxTracked-1))
		if !ok {
			return nil, configError("retention-overflow")
		}
	}
	startBytes, _ := json.Marshal(StartResult{ID: maxID, State: JobTimedOut, StartedAt: maxTime, TimeoutSeconds: int64(maxTimeout)})
	killBytes, _ := json.Marshal(KillResult{ID: maxID, State: JobTimedOut, NewlyAccepted: false})
	maxima := map[string]int64{
		StartToolName: int64(len(startBytes)), StatusToolName: statusBytes,
		ListToolName: listBytes, KillToolName: int64(len(killBytes)),
	}
	result := make(map[string]runtime.RetentionPolicy, len(maxima))
	for name, maximum := range maxima {
		inline, valid := safeMul(maximum, 2)
		if !valid || inline > maxToolInlineBytes {
			return nil, configError("retention-limit")
		}
		result[name] = runtime.RetentionPolicy{MaxInlineBytes: inline, StoreExternal: false}
	}
	return result, nil
}

func safeAdd(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}

func safeMul(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || (left != 0 && right > math.MaxInt64/left) {
		return 0, false
	}
	return left * right, true
}
