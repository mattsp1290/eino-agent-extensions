package websearch

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
	"github.com/mattsp1290/eino-agent/runtime"
)

const (
	// ToolName is the model-facing tool name and registration identity.
	ToolName = "web_search"
	// PermissionSearch is checked by Eino before Searcher admission.
	PermissionSearch = "network.web.search"
	// DefaultOrder is used when Options.Order is zero.
	DefaultOrder = 100

	maxQueryBytes            = 16 << 10
	maxResults               = 100
	maxTitleBytes            = 1 << 10
	maxURLBytes              = 8 << 10
	maxSnippetBytes          = 16 << 10
	maxInFlight              = 256
	maxWait                  = 10 * time.Minute
	maxSearcherIdentityBytes = 256
	minimumURLBytes          = len("http://a")

	configurationVersion = "web-search-config-v1"
	toolSchemaVersion    = "web-search-input-v1"
	resultSchemaVersion  = "web-search-result-v1"
	sourceVersion        = "web-search-source-validation-v1"
	permissionVersion    = "web-search-permission-v1"
	permissionPattern    = ToolName
	registrationID       = ToolName
)

// Limits bounds every package-owned input, candidate inspection, output,
// wait, and concurrent callback resource. Every field is required and must be
// positive.
type Limits struct {
	MaxQueryBytes   int
	MaxResults      int
	MaxTitleBytes   int
	MaxURLBytes     int
	MaxSnippetBytes int
	MaxInFlight     int
	MaxWait         time.Duration
}

// Options describes one immutable web_search mount. SearcherIdentity is a
// bounded non-secret host version and must change whenever callback routing or
// behavior changes.
type Options struct {
	Scope            extension.Scope
	Order            int
	Searcher         Searcher
	SearcherIdentity string
	Limits           Limits
}

type canonicalOptions struct {
	scope            extension.Scope
	order            int
	searcher         Searcher
	searcherIdentity string
	limits           Limits
	retention        runtime.RetentionPolicy
}

type hashPolicy struct {
	Version           string `json:"version"`
	Tool              string `json:"tool"`
	Registration      string `json:"registration"`
	ToolSchema        string `json:"tool_schema"`
	ResultSchema      string `json:"result_schema"`
	SourceValidation  string `json:"source_validation"`
	Permission        string `json:"permission"`
	PermissionPattern string `json:"permission_pattern"`
	PermissionVersion string `json:"permission_version"`
	Limits            Limits `json:"limits"`
	SearcherIdentity  string `json:"searcher_identity"`
}

// ConfigHash returns the deterministic identity of behavior-bearing options.
// It excludes Searcher, host state, scope, order, and component artifact
// identity.
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
		SourceValidation: sourceVersion, Permission: PermissionSearch,
		PermissionPattern: permissionPattern, PermissionVersion: permissionVersion,
		Limits: options.limits, SearcherIdentity: options.searcherIdentity,
	})
	if err != nil {
		return "", configError("hash-encoding")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalize(options Options) (canonicalOptions, error) {
	canonical := canonicalOptions{
		scope: options.Scope, order: options.Order, searcher: options.Searcher,
		searcherIdentity: options.SearcherIdentity, limits: options.Limits,
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
	if nilSearcher(canonical.searcher) {
		return canonicalOptions{}, configError("searcher-required")
	}
	if !validIdentity(canonical.searcherIdentity) {
		return canonicalOptions{}, configError("searcher-identity")
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

func nilSearcher(value Searcher) bool {
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
	if strings.TrimSpace(value) == "" || len(value) > maxSearcherIdentityBytes || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
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
	if limits.MaxQueryBytes <= 0 || limits.MaxQueryBytes > maxQueryBytes {
		return configError("max-query")
	}
	if limits.MaxResults <= 0 || limits.MaxResults > maxResults {
		return configError("max-results")
	}
	if limits.MaxTitleBytes <= 0 || limits.MaxTitleBytes > maxTitleBytes {
		return configError("max-title")
	}
	if limits.MaxURLBytes < minimumURLBytes || limits.MaxURLBytes > maxURLBytes {
		return configError("max-url")
	}
	if limits.MaxSnippetBytes <= 0 || limits.MaxSnippetBytes > maxSnippetBytes {
		return configError("max-snippet")
	}
	if limits.MaxInFlight <= 0 || limits.MaxInFlight > maxInFlight {
		return configError("max-in-flight")
	}
	if limits.MaxWait <= 0 || limits.MaxWait > maxWait {
		return configError("max-wait")
	}
	return nil
}

func resultRetention(limits Limits) (runtime.RetentionPolicy, error) {
	oneCopy, err := worstCaseResultBytes(limits)
	if err != nil {
		return runtime.RetentionPolicy{}, err
	}
	twoCopies, ok := checkedMul(oneCopy, 2)
	if !ok {
		return runtime.RetentionPolicy{}, configError("result-retention")
	}
	return runtime.RetentionPolicy{MaxInlineBytes: twoCopies, StoreExternal: false, Redact: false}, nil
}
