package websearch

import (
	"context"
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
		"query zero":         func(o *Options) { o.Limits.MaxQueryBytes = 0 },
		"query ceiling":      func(o *Options) { o.Limits.MaxQueryBytes = maxQueryBytes + 1 },
		"results zero":       func(o *Options) { o.Limits.MaxResults = 0 },
		"results ceiling":    func(o *Options) { o.Limits.MaxResults = maxResults + 1 },
		"title zero":         func(o *Options) { o.Limits.MaxTitleBytes = 0 },
		"title ceiling":      func(o *Options) { o.Limits.MaxTitleBytes = maxTitleBytes + 1 },
		"url too small":      func(o *Options) { o.Limits.MaxURLBytes = minimumURLBytes - 1 },
		"url ceiling":        func(o *Options) { o.Limits.MaxURLBytes = maxURLBytes + 1 },
		"snippet zero":       func(o *Options) { o.Limits.MaxSnippetBytes = 0 },
		"snippet ceiling":    func(o *Options) { o.Limits.MaxSnippetBytes = maxSnippetBytes + 1 },
		"capacity zero":      func(o *Options) { o.Limits.MaxInFlight = 0 },
		"capacity ceiling":   func(o *Options) { o.Limits.MaxInFlight = maxInFlight + 1 },
		"wait zero":          func(o *Options) { o.Limits.MaxWait = 0 },
		"wait ceiling":       func(o *Options) { o.Limits.MaxWait = maxWait + time.Nanosecond },
		"near max int":       func(o *Options) { o.Limits.MaxResults = math.MaxInt },
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

func TestConfigAcceptsPackageCeilings(t *testing.T) {
	options := testOptions()
	options.Limits = Limits{
		MaxQueryBytes: maxQueryBytes, MaxResults: maxResults,
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
		"identity": func(o *Options) { o.SearcherIdentity += "-changed" },
		"query":    func(o *Options) { o.Limits.MaxQueryBytes++ },
		"results":  func(o *Options) { o.Limits.MaxResults++ },
		"title":    func(o *Options) { o.Limits.MaxTitleBytes++ },
		"url":      func(o *Options) { o.Limits.MaxURLBytes++ },
		"snippet":  func(o *Options) { o.Limits.MaxSnippetBytes++ },
		"capacity": func(o *Options) { o.Limits.MaxInFlight++ },
		"wait":     func(o *Options) { o.Limits.MaxWait++ },
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
