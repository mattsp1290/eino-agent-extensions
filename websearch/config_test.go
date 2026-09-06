package websearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mattsp1290/eino-agent/extension"
)

type pointerSearcher struct{}

func (*pointerSearcher) Search(context.Context, string) ([]Source, error) { return nil, nil }

func TestConfigDefaultsAndExplicitPlacement(t *testing.T) {
	canonical, err := canonicalize(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if canonical.scope != extension.GlobalScope() || canonical.order != DefaultOrder {
		t.Fatalf("defaults=%#v order=%d", canonical.scope, canonical.order)
	}
	explicit := testOptions()
	explicit.Scope = extension.SessionScope("session")
	explicit.Order = 42
	canonical, err = canonicalize(explicit)
	if err != nil || canonical.scope != explicit.Scope || canonical.order != 42 {
		t.Fatalf("explicit=%#v order=%d err=%v", canonical.scope, canonical.order, err)
	}
}

func TestConfigRejectsInvalidSearcherIdentityAndLimits(t *testing.T) {
	var typedNil *pointerSearcher
	tests := map[string]func(*Options){
		"nil searcher":       func(o *Options) { o.Searcher = nil },
		"typed nil searcher": func(o *Options) { o.Searcher = typedNil },
		"identity empty":     func(o *Options) { o.SearcherIdentity = "" },
		"identity blank":     func(o *Options) { o.SearcherIdentity = "   " },
		"identity control":   func(o *Options) { o.SearcherIdentity = "bad\nidentity" },
		"identity nul":       func(o *Options) { o.SearcherIdentity = "bad\x00identity" },
		"identity utf8":      func(o *Options) { o.SearcherIdentity = string([]byte{0xff}) },
		"identity long":      func(o *Options) { o.SearcherIdentity = strings.Repeat("a", maxSearcherIdentityBytes+1) },
		"raw input zero":     func(o *Options) { o.Limits.MaxRawInputBytes = 0 },
		"raw input negative": func(o *Options) { o.Limits.MaxRawInputBytes = -1 },
		"raw input too small": func(o *Options) {
			o.Limits.MaxRawInputBytes = 6*o.Limits.MaxQueryBytes + len(`{"query":""}`) - 1
		},
		"raw input ceiling": func(o *Options) { o.Limits.MaxRawInputBytes = maxRawInputBytes + 1 },
		"query zero":        func(o *Options) { o.Limits.MaxQueryBytes = 0 },
		"query negative":    func(o *Options) { o.Limits.MaxQueryBytes = -1 },
		"query ceiling":     func(o *Options) { o.Limits.MaxQueryBytes = maxQueryBytes + 1 },
		"results zero":      func(o *Options) { o.Limits.MaxResults = 0 },
		"results negative":  func(o *Options) { o.Limits.MaxResults = -1 },
		"results ceiling":   func(o *Options) { o.Limits.MaxResults = maxResults + 1 },
		"title zero":        func(o *Options) { o.Limits.MaxTitleBytes = 0 },
		"title negative":    func(o *Options) { o.Limits.MaxTitleBytes = -1 },
		"title ceiling":     func(o *Options) { o.Limits.MaxTitleBytes = maxTitleBytes + 1 },
		"url too small":     func(o *Options) { o.Limits.MaxURLBytes = minimumURLBytes - 1 },
		"url negative":      func(o *Options) { o.Limits.MaxURLBytes = -1 },
		"url ceiling":       func(o *Options) { o.Limits.MaxURLBytes = maxURLBytes + 1 },
		"snippet zero":      func(o *Options) { o.Limits.MaxSnippetBytes = 0 },
		"snippet negative":  func(o *Options) { o.Limits.MaxSnippetBytes = -1 },
		"snippet ceiling":   func(o *Options) { o.Limits.MaxSnippetBytes = maxSnippetBytes + 1 },
		"capacity zero":     func(o *Options) { o.Limits.MaxInFlight = 0 },
		"capacity negative": func(o *Options) { o.Limits.MaxInFlight = -1 },
		"capacity ceiling":  func(o *Options) { o.Limits.MaxInFlight = maxInFlight + 1 },
		"wait zero":         func(o *Options) { o.Limits.MaxWait = 0 },
		"wait negative":     func(o *Options) { o.Limits.MaxWait = -time.Nanosecond },
		"wait ceiling":      func(o *Options) { o.Limits.MaxWait = maxWait + time.Nanosecond },
		"near max int":      func(o *Options) { o.Limits.MaxResults = math.MaxInt },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			options := testOptions()
			mutate(&options)
			if _, err := ConfigHash(options); err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
}

func TestConfigHashIncludesFixedContractIdentities(t *testing.T) {
	options, err := canonicalize(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	policy := hashPolicy{
		Version: configurationVersion, Tool: ToolName, Registration: registrationID,
		ToolSchema: toolSchemaVersion, ResultSchema: resultSchemaVersion,
		SourceValidation: sourceVersion, Permission: PermissionSearch,
		PermissionPattern: permissionPattern, PermissionVersion: permissionVersion,
		Limits: options.limits, SearcherIdentity: options.searcherIdentity,
	}
	want, err := configHash(options)
	if err != nil {
		t.Fatal(err)
	}
	if got := testPolicyHash(t, policy); got != want {
		t.Fatalf("literal policy hash=%q want=%q", got, want)
	}
	mutations := map[string]func(*hashPolicy){
		"version":            func(p *hashPolicy) { p.Version += "-changed" },
		"tool":               func(p *hashPolicy) { p.Tool += "-changed" },
		"registration":       func(p *hashPolicy) { p.Registration += "-changed" },
		"tool schema":        func(p *hashPolicy) { p.ToolSchema += "-changed" },
		"result schema":      func(p *hashPolicy) { p.ResultSchema += "-changed" },
		"source validation":  func(p *hashPolicy) { p.SourceValidation += "-changed" },
		"permission":         func(p *hashPolicy) { p.Permission += "-changed" },
		"permission pattern": func(p *hashPolicy) { p.PermissionPattern += "-changed" },
		"permission version": func(p *hashPolicy) { p.PermissionVersion += "-changed" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := policy
			mutate(&changed)
			if got := testPolicyHash(t, changed); got == want {
				t.Fatalf("fixed identity did not affect hash %q", got)
			}
		})
	}
}

func testPolicyHash(t *testing.T, policy hashPolicy) string {
	t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func TestConfigAcceptsPackageCeilings(t *testing.T) {
	options := testOptions()
	options.Limits = Limits{
		MaxRawInputBytes: maxRawInputBytes, MaxQueryBytes: maxQueryBytes, MaxResults: maxResults,
		MaxTitleBytes: maxTitleBytes, MaxURLBytes: maxURLBytes,
		MaxSnippetBytes: maxSnippetBytes, MaxInFlight: maxInFlight, MaxWait: maxWait,
	}
	if _, err := ConfigHash(options); err != nil {
		t.Fatal(err)
	}
}

func TestConfigHashTracksBehaviorButNotPlacementOrCallback(t *testing.T) {
	base := testOptions()
	want, err := ConfigHash(base)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*Options){
		"identity":  func(o *Options) { o.SearcherIdentity += "-changed" },
		"raw input": func(o *Options) { o.Limits.MaxRawInputBytes++ },
		"query":     func(o *Options) { o.Limits.MaxQueryBytes++ },
		"results":   func(o *Options) { o.Limits.MaxResults++ },
		"title":     func(o *Options) { o.Limits.MaxTitleBytes++ },
		"url":       func(o *Options) { o.Limits.MaxURLBytes++ },
		"snippet":   func(o *Options) { o.Limits.MaxSnippetBytes++ },
		"capacity":  func(o *Options) { o.Limits.MaxInFlight++ },
		"wait":      func(o *Options) { o.Limits.MaxWait++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			got, err := ConfigHash(changed)
			if err != nil || got == want {
				t.Fatalf("hash=%q err=%v", got, err)
			}
		})
	}
	equivalent := base
	equivalent.Scope = extension.SessionScope("different")
	equivalent.Order = 99
	equivalent.Searcher = SearcherFunc(func(context.Context, string) ([]Source, error) { return nil, nil })
	got, err := ConfigHash(equivalent)
	if err != nil || got != want {
		t.Fatalf("excluded fields changed hash=%q want=%q err=%v", got, want, err)
	}
}

func TestRetentionMatchesWorstCaseAndRejectsOverflowHelpers(t *testing.T) {
	limits := testLimits()
	worst, err := worstCaseResultBytes(limits)
	if err != nil {
		t.Fatal(err)
	}
	retention, err := resultRetention(limits)
	if err != nil || retention.MaxInlineBytes != 2*worst || retention.StoreExternal || retention.Redact {
		t.Fatalf("retention=%#v worst=%d err=%v", retention, worst, err)
	}
	if _, ok := checkedAdd(math.MaxInt64, 1); ok {
		t.Fatal("addition overflow accepted")
	}
	if _, ok := checkedMul(math.MaxInt64, 2); ok {
		t.Fatal("multiplication overflow accepted")
	}
}

func TestRetentionSmallLiteralWorstCase(t *testing.T) {
	limits := Limits{
		MaxRawInputBytes: 18, MaxQueryBytes: 1, MaxResults: 2, MaxTitleBytes: 3,
		MaxURLBytes: 8, MaxSnippetBytes: 4, MaxInFlight: 1,
		MaxWait: time.Second,
	}
	retention, err := resultRetention(limits)
	if err != nil {
		t.Fatal(err)
	}
	// One JSON copy is 14 envelope bytes plus two 124-byte worst-case
	// records and their comma. Runtime retention must hold two copies.
	const want = int64(526)
	if retention.MaxInlineBytes != want || retention.StoreExternal || retention.Redact {
		t.Fatalf("retention=%#v want-inline=%d", retention, want)
	}
}
