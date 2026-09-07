package workspaceinstructions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mattsp1290/eino-agent/extension"
)

const (
	// PromptName is shared by global and session mounts so a session mount can
	// shadow the global registration.
	PromptName = "workspace/instructions"
	// DefaultOrder places workspace instructions after Eino's runtime prompt.
	DefaultOrder = 100
	// DefaultFileName is used when Options.FileNames is nil.
	DefaultFileName = "AGENTS.md"

	registrationID       = "prompt/workspace-instructions"
	configurationVersion = "workspace-instructions-config-v1"
	renderVersion        = "workspace-instructions-render-v1"

	maxFileNames             = 16
	maxChainDepth            = 64
	maxFileBytes             = 512 << 10
	maxSectionBytes          = 1 << 20
	maxInFlight              = 256
	maxWait                  = 10 * time.Minute
	maxResolverIdentityBytes = 256
	maxFileNameBytes         = 255

	// In addition to the longest fixed envelope strings, this reserves room
	// for the maximum root-relative display path and either visible marker.
	envelopeOverhead = len(`<workspace_instructions version="workspace-instructions-render-v1">`) +
		len(`</workspace_instructions>`) + len(`<file path="" truncated="true">`) +
		len(`</file>`) + 512
)

// Limits bounds all package-owned discovery, output, waiting, and concurrency.
// Every field is required.
type Limits struct {
	MaxFileNames    int
	MaxChainDepth   int
	MaxFileBytes    int
	MaxSectionBytes int
	MaxInFlight     int
	MaxWait         time.Duration
}

// Options describes one immutable workspace-instructions mount.
// ResolverIdentity is a bounded, non-secret host version identifier.
type Options struct {
	Scope            extension.Scope
	Order            int
	FileNames        []string
	Resolver         Resolver
	ResolverIdentity string
	Limits           Limits
}

type canonicalOptions struct {
	scope            extension.Scope
	order            int
	fileNames        []string
	resolver         Resolver
	resolverIdentity string
	limits           Limits
}

type hashPolicy struct {
	Version          string   `json:"version"`
	Registration     string   `json:"registration"`
	PromptName       string   `json:"prompt_name"`
	RenderVersion    string   `json:"render_version"`
	FileNames        []string `json:"file_names"`
	Limits           Limits   `json:"limits"`
	ResolverIdentity string   `json:"resolver_identity"`
}

// ConfigHash returns the deterministic identity of behavior-bearing options.
// It excludes Resolver, host state, Scope, Order, and artifact identity.
func ConfigHash(options Options) (string, error) {
	canonical, err := canonicalize(options)
	if err != nil {
		return "", err
	}
	return configHash(canonical)
}

func configHash(options canonicalOptions) (string, error) {
	raw, err := json.Marshal(hashPolicy{
		Version: configurationVersion, Registration: registrationID,
		PromptName: PromptName, RenderVersion: renderVersion,
		FileNames: options.fileNames, Limits: options.limits,
		ResolverIdentity: options.resolverIdentity,
	})
	if err != nil {
		return "", configError("hash-encoding")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalize(options Options) (canonicalOptions, error) {
	canonical := canonicalOptions{
		scope: options.Scope, order: options.Order, resolver: options.Resolver,
		resolverIdentity: options.ResolverIdentity, limits: options.Limits,
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
	if options.FileNames == nil {
		canonical.fileNames = []string{DefaultFileName}
	} else {
		canonical.fileNames = append([]string(nil), options.FileNames...)
	}
	if err := validateFileNames(canonical.fileNames, maxFileNames); err != nil {
		return canonicalOptions{}, err
	}
	if nilResolver(canonical.resolver) {
		return canonicalOptions{}, configError("resolver-required")
	}
	if !validIdentity(canonical.resolverIdentity) {
		return canonicalOptions{}, configError("resolver-identity")
	}
	if err := validateLimits(canonical.limits); err != nil {
		return canonicalOptions{}, err
	}
	if len(canonical.fileNames) > canonical.limits.MaxFileNames {
		return canonicalOptions{}, configError("file-names")
	}
	return canonical, nil
}

func nilResolver(value Resolver) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func validIdentity(value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > maxResolverIdentityBytes || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validateFileNames(names []string, configuredMax int) error {
	if len(names) == 0 || configuredMax <= 0 || len(names) > configuredMax {
		return configError("file-names")
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !validFileName(name) {
			return configError("file-names")
		}
		if _, exists := seen[name]; exists {
			return configError("file-names")
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validFileName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > maxFileNameBytes || !utf8.ValidString(name) {
		return false
	}
	if strings.ContainsAny(name, `/\\:*?"<>|`) || strings.IndexByte(name, 0) >= 0 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validateLimits(limits Limits) error {
	if limits.MaxFileNames <= 0 || limits.MaxFileNames > maxFileNames {
		return configError("max-file-names")
	}
	if limits.MaxChainDepth <= 0 || limits.MaxChainDepth > maxChainDepth {
		return configError("max-chain-depth")
	}
	if limits.MaxFileBytes <= 0 || limits.MaxFileBytes > maxFileBytes {
		return configError("max-file-bytes")
	}
	if limits.MaxSectionBytes < limits.MaxFileBytes+envelopeOverhead || limits.MaxSectionBytes > maxSectionBytes {
		return configError("max-section-bytes")
	}
	if limits.MaxInFlight <= 0 || limits.MaxInFlight > maxInFlight {
		return configError("max-in-flight")
	}
	if limits.MaxWait <= 0 || limits.MaxWait > maxWait {
		return configError("max-wait")
	}
	return nil
}
