package websearch

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundsInspectOnlyFirstMaxResultsAndDoNotRefill(t *testing.T) {
	limits := testLimits()
	limits.MaxResults = 2
	records := []Source{
		{Title: "invalid", URL: "relative", Snippet: "drop"},
		{Title: "kept", URL: "https://example.test/one?x=1#f", Snippet: "kept"},
		{Title: "later", URL: "https://example.test/later", Snippet: "ignored"},
	}
	result := boundSources(records, limits)
	if result.Results == nil || len(result.Results) != 1 || result.Results[0].URL != records[1].URL {
		t.Fatalf("result=%#v", result)
	}
	if &result.Results[0] == &records[1] {
		t.Fatal("host backing array exposed")
	}
}

func TestBoundsDropInvalidURLsWithoutRepair(t *testing.T) {
	limits := testLimits()
	limits.MaxResults = 20
	over := "https://example.test/" + strings.Repeat("a", limits.MaxURLBytes)
	invalidUTF8 := "https://example.test/" + string([]byte{0xff})
	valid := "https://example.test/a?x=1#fragment"
	records := []Source{
		{URL: ""}, {URL: "/relative"}, {URL: "//example.test/path"},
		{URL: "ftp://example.test/file"}, {URL: "http:///missing-host"},
		{URL: "https://user:pass@example.test/private"}, {URL: "https://example.test/\ncontrol"},
		{URL: invalidUTF8}, {URL: over}, {Title: "ok", URL: valid, Snippet: "ok"},
	}
	result := boundSources(records, limits)
	if len(result.Results) != 1 || result.Results[0].URL != valid {
		t.Fatalf("result=%#v", result)
	}
}

func TestBoundsUTF8PrefixIsByteBoundedAndCodePointSafe(t *testing.T) {
	limits := testLimits()
	limits.MaxTitleBytes = 4
	limits.MaxSnippetBytes = 3
	result := boundSources([]Source{{
		Title: "aéétail", URL: "http://a", Snippet: "a" + string([]byte{0xff}) + "érest",
	}}, limits)
	if len(result.Results) != 1 {
		t.Fatalf("result=%#v", result)
	}
	got := result.Results[0]
	if got.Title != "aé" || got.Snippet != "a" || !utf8.ValidString(got.Title) || !utf8.ValidString(got.Snippet) {
		t.Fatalf("bounded=%#v", got)
	}
}

func TestBoundsPreserveOrderDuplicatesAndEmptyArray(t *testing.T) {
	limits := testLimits()
	records := []Source{
		{Title: "first", URL: "http://a", Snippet: "1"},
		{Title: "second", URL: "http://a", Snippet: "2"},
	}
	result := boundSources(records, limits)
	if len(result.Results) != 2 || result.Results[0].Title != "first" || result.Results[1].Title != "second" {
		t.Fatalf("result=%#v", result)
	}
	empty := boundSources(nil, limits)
	encoded, err := json.Marshal(empty)
	if err != nil || string(encoded) != `{"results":[]}` {
		t.Fatalf("empty=%s err=%v", encoded, err)
	}
}

func TestBoundsWorstCaseEscapingFitsTwoCopies(t *testing.T) {
	limits := testLimits()
	limits.MaxResults = 2
	limits.MaxTitleBytes = 4
	limits.MaxURLBytes = 64
	limits.MaxSnippetBytes = 5
	result := Result{Results: []Source{
		{Title: strings.Repeat("\x01", limits.MaxTitleBytes), URL: "https://example.test/?x=<&y=" + strings.Repeat("a", 23), Snippet: strings.Repeat("\x02", limits.MaxSnippetBytes)},
		{Title: strings.Repeat("\x03", limits.MaxTitleBytes), URL: "https://example.test/?x=<&z=" + strings.Repeat("b", 23), Snippet: strings.Repeat("\x04", limits.MaxSnippetBytes)},
	}}
	for i := range result.Results {
		if len(result.Results[i].URL) > limits.MaxURLBytes {
			t.Fatal("test URL exceeds configured maximum")
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	retention, err := resultRetention(limits)
	if err != nil || int64(len(encoded))*2 > retention.MaxInlineBytes {
		t.Fatalf("encoded=%d retention=%d err=%v", len(encoded), retention.MaxInlineBytes, err)
	}
}
