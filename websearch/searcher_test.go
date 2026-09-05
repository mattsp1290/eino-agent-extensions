package websearch

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSearcherFuncAndResultContract(t *testing.T) {
	searcher := SearcherFunc(func(_ context.Context, query string) ([]Source, error) {
		return []Source{{Title: query, URL: "https://example.test/", Snippet: "s"}}, nil
	})
	records, err := searcher.Search(context.Background(), "query")
	if err != nil || len(records) != 1 || records[0].Title != "query" {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	encoded, err := json.Marshal(Result{Results: []Source{}})
	if err != nil || string(encoded) != `{"results":[]}` {
		t.Fatalf("empty result=%s err=%v", encoded, err)
	}
	encoded, err = json.Marshal(Result{Results: []Source{{Title: "t", URL: "https://example.test/", Snippet: "s"}}})
	if err != nil || string(encoded) != `{"results":[{"title":"t","url":"https://example.test/","snippet":"s"}]}` {
		t.Fatalf("result=%s err=%v", encoded, err)
	}
}
