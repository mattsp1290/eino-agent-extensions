package commandguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mattsp1290/eino-agent/extension"
	"github.com/mattsp1290/eino-agent/runtime"
)

// Dialect selects the input language, independently of the host OS.
type Dialect string

const (
	DialectPOSIX Dialect = "posix"
	DialectBash  Dialect = "bash"
)

// Binding selects one exact model tool name and literal top-level JSON key.
// Empty Dialect means POSIX. Periods in CommandField are literal.
type Binding struct {
	ToolName     string
	CommandField string
	Dialect      Dialect
}

// DefaultBindings returns a fresh list of production command-tool bindings.
func DefaultBindings() []Binding {
	return []Binding{{"shell", "cmd", DialectPOSIX}, {"background_job_start", "command", DialectPOSIX}}
}

// Rule denies a case-sensitive POSIX executable basename and exact positional
// argument prefix. An empty prefix matches every invocation of the basename.
type Rule struct {
	ID         string
	Executable string
	ArgPrefix  []string
}

// Limits contains required positive bounds. AST bounds apply after parser
// construction; byte caps and admission bound its inputs, not wall-clock time.
type Limits struct {
	MaxBindings      int
	MaxRules         int
	MaxRuleBytes     int
	MaxPrefixArgs    int
	MaxRawInputBytes int
	MaxJSONDepth     int
	MaxJSONNodes     int
	MaxCommandBytes  int
	MaxAnalysisBytes int
	MaxASTNodes      int
	MaxASTDepth      int
	MaxWords         int
	MaxWordBytes     int
	MaxWrapperDepth  int
	MaxInFlight      int
}

// Options is a host-owned policy, frozen by Mount. Nil Bindings selects defaults;
// a nonnil list replaces them. An explicit empty list is invalid.
type Options struct {
	Scope    extension.Scope
	Order    int
	Bindings []Binding
	Rules    []Rule
	Limits   Limits
}
type policy struct {
	scope    extension.Scope
	order    int
	bindings []Binding
	rules    []Rule
	limits   Limits
}

// ConfigHash returns the effective behavior's SHA-256 identity. Scope and order
// are separately recorded in Eino's descriptor.
func ConfigHash(o Options) (string, error) {
	p, err := canonicalize(o)
	if err != nil {
		return "", err
	}
	return configHash(p), nil
}

type behaviorIdentity struct {
	Schema, Diagnostics, Matching, Wrappers, Builtins, AST, Parser string
}

func currentBehavior() behaviorIdentity {
	return behaviorIdentity{"command-policy-v1", "fixed-denials-v1", "posix-basename-positional-prefix-v1", "bounded-wrappers-v1", "builtin-operands-v1", "ast-simple-parameter-cr-preserving-v1", "mvdan.cc/sh/v3@v3.14.1"}
}
func configHash(p policy) string { return hashBehavior(p, currentBehavior()) }
func hashBehavior(p policy, identity behaviorIdentity) string {
	raw, _ := json.Marshal(struct {
		behaviorIdentity
		Bindings []Binding
		Rules    []Rule
		Limits   Limits
	}{identity, p.bindings, p.rules, p.limits})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
func canonicalize(o Options) (policy, error) {
	p := policy{scope: o.Scope, order: o.Order, limits: o.Limits}
	if p.scope == (extension.Scope{}) {
		p.scope = extension.GlobalScope()
	}
	if p.order == 0 {
		p.order = runtime.OrderHostPolicy
	}
	if extension.ValidateScope(p.scope) != nil {
		return policy{}, configError("scope")
	}
	l := o.Limits
	for _, n := range []int{l.MaxBindings, l.MaxRules, l.MaxRuleBytes, l.MaxPrefixArgs, l.MaxRawInputBytes, l.MaxJSONDepth, l.MaxJSONNodes, l.MaxCommandBytes, l.MaxAnalysisBytes, l.MaxASTNodes, l.MaxASTDepth, l.MaxWords, l.MaxWordBytes, l.MaxWrapperDepth, l.MaxInFlight} {
		if n <= 0 {
			return policy{}, configError("limits")
		}
	}
	if l.MaxAnalysisBytes < l.MaxCommandBytes {
		return policy{}, configError("limits")
	}
	bindings := o.Bindings
	if bindings == nil {
		bindings = DefaultBindings()
	}
	if len(bindings) == 0 || len(bindings) > l.MaxBindings || len(o.Rules) == 0 || len(o.Rules) > l.MaxRules {
		return policy{}, configError("count")
	}
	// Check sizes before copies, maps, or hashing. Subtraction avoids overflow.
	for _, b := range bindings {
		if b.Dialect == "" {
			b.Dialect = DialectPOSIX
		}
		if len(b.ToolName) > l.MaxWordBytes || len(b.CommandField) > l.MaxWordBytes || len(b.Dialect) > l.MaxWordBytes {
			return policy{}, configError("binding-size")
		}
		if extension.ValidateIdentifier(b.ToolName) != nil || strings.TrimSpace(b.CommandField) == "" || !validText(b.CommandField) {
			return policy{}, configError("binding")
		}
		if b.Dialect != "" && b.Dialect != DialectPOSIX && b.Dialect != DialectBash {
			return policy{}, configError("dialect")
		}
	}
	for _, r := range o.Rules {
		remaining := l.MaxRuleBytes
		if len(r.ArgPrefix) > l.MaxPrefixArgs {
			return policy{}, configError("rule-size")
		}
		for _, s := range []string{r.ID, r.Executable} {
			if len(s) > remaining {
				return policy{}, configError("rule-size")
			}
			remaining -= len(s)
		}
		for _, s := range r.ArgPrefix {
			if len(s) > remaining || len(s) > l.MaxWordBytes {
				return policy{}, configError("rule-size")
			}
			remaining -= len(s)
			if !validText(s) {
				return policy{}, configError("rule-token")
			}
		}
		if extension.ValidateIdentifier(r.ID) != nil || !validText(r.Executable) || len(r.Executable) > l.MaxWordBytes || r.Executable == "" || r.Executable == "." || r.Executable == ".." || strings.ContainsAny(r.Executable, "/\\") {
			return policy{}, configError("rule")
		}
	}
	p.bindings = append([]Binding(nil), bindings...)
	for i := range p.bindings {
		if p.bindings[i].Dialect == "" {
			p.bindings[i].Dialect = DialectPOSIX
		}
	}
	sort.Slice(p.bindings, func(i, j int) bool { return p.bindings[i].ToolName < p.bindings[j].ToolName })
	for i := 1; i < len(p.bindings); i++ {
		if p.bindings[i-1].ToolName == p.bindings[i].ToolName {
			return policy{}, configError("duplicate-binding")
		}
	}
	p.rules = make([]Rule, len(o.Rules))
	for i, r := range o.Rules {
		p.rules[i] = r
		p.rules[i].ArgPrefix = append([]string{}, r.ArgPrefix...)
	}
	sort.Slice(p.rules, func(i, j int) bool { return p.rules[i].ID < p.rules[j].ID })
	for i := 1; i < len(p.rules); i++ {
		if p.rules[i-1].ID == p.rules[i].ID {
			return policy{}, configError("duplicate-rule")
		}
	}
	return p, nil
}
