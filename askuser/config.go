package askuser

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
	// DefaultOrder is used when Options.Order is zero.
	DefaultOrder = 100

	maxResponderIdentityBytes = 256
	configurationVersion      = "ask-user-config-v1"
	toolSchemaVersion         = "ask-user-input-v1"
	resultSchemaVersion       = "ask-user-result-v1"
	permissionVersion         = "interaction-ask-v1"
	customChoiceVersion       = "custom-choice-v1"
	registrationID            = ToolName
)

// Limits bounds all package-owned text, waiting, and concurrency. Every field
// is required and must be positive.
type Limits struct {
	MaxQuestionBytes          int
	MaxOptionLabelBytes       int
	MaxOptionDescriptionBytes int
	MaxCustomAnswerBytes      int
	MaxInFlight               int
	MaxWait                   time.Duration
}

// Options describes one immutable ask_user mount. ResponderIdentity is a
// non-secret host version that must change with response-routing behavior.
type Options struct {
	Scope             extension.Scope
	Order             int
	Responder         Responder
	ResponderIdentity string
	Limits            Limits
}

type canonicalOptions struct {
	scope             extension.Scope
	order             int
	responder         Responder
	responderIdentity string
	limits            Limits
	retention         runtime.RetentionPolicy
}

type hashPolicy struct {
	Version             string `json:"version"`
	Tool                string `json:"tool"`
	Registration        string `json:"registration"`
	ToolSchema          string `json:"tool_schema"`
	ResultSchema        string `json:"result_schema"`
	Permission          string `json:"permission"`
	PermissionVersion   string `json:"permission_version"`
	CustomLabel         string `json:"custom_label"`
	CustomChoiceVersion string `json:"custom_choice_version"`
	Limits              Limits `json:"limits"`
	ResponderIdentity   string `json:"responder_identity"`
}

// ConfigHash returns the deterministic identity of behavior-bearing options.
// It excludes the responder value, host state, scope, and order.
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
		Permission: PermissionAsk, PermissionVersion: permissionVersion,
		CustomLabel: CustomOptionLabel, CustomChoiceVersion: customChoiceVersion,
		Limits: options.limits, ResponderIdentity: options.responderIdentity,
	})
	if err != nil {
		return "", configError("hash-encoding")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalize(options Options) (canonicalOptions, error) {
	canonical := canonicalOptions{
		scope: options.Scope, order: options.Order, responder: options.Responder,
		responderIdentity: options.ResponderIdentity, limits: options.Limits,
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
	if nilResponder(canonical.responder) {
		return canonicalOptions{}, configError("responder-required")
	}
	if !validIdentity(canonical.responderIdentity) {
		return canonicalOptions{}, configError("responder-identity")
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

func nilResponder(value Responder) bool {
	if value == nil {
		return true
	}
	kind := reflect.ValueOf(value).Kind()
	return (kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface || kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice) && reflect.ValueOf(value).IsNil()
}

func validIdentity(value string) bool {
	if value == "" || len(value) > maxResponderIdentityBytes || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
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
	values := []int{
		limits.MaxQuestionBytes, limits.MaxOptionLabelBytes,
		limits.MaxOptionDescriptionBytes, limits.MaxCustomAnswerBytes,
		limits.MaxInFlight,
	}
	for _, value := range values {
		if value <= 0 {
			return configError("limits")
		}
	}
	if limits.MaxWait <= 0 {
		return configError("max-wait")
	}
	return nil
}

func resultRetention(limits Limits) (runtime.RetentionPolicy, error) {
	largestText := limits.MaxOptionLabelBytes
	if limits.MaxCustomAnswerBytes > largestText {
		largestText = limits.MaxCustomAnswerBytes
	}
	// encoding/json may expand one accepted input byte to a six-byte escape.
	if largestText > (math.MaxInt-128)/6 {
		return runtime.RetentionPolicy{}, configError("result-retention")
	}
	oneCopy := int64(128 + largestText*6)
	if oneCopy > math.MaxInt64/2 {
		return runtime.RetentionPolicy{}, configError("result-retention")
	}
	return runtime.RetentionPolicy{MaxInlineBytes: oneCopy * 2, StoreExternal: false, Redact: false}, nil
}
