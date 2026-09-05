package websearch

import "context"

// Source is one host-provided search record. The adapter bounds Title and
// Snippet and retains URL only when it is a bounded, user-info-free absolute
// HTTP(S) URL.
type Source struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Result is the complete model-facing result. Results is always non-nil for a
// successful search, including an empty search.
type Result struct {
	Results []Source `json:"results"`
}

// Searcher performs one host-owned search. Implementations must be
// concurrency-safe and honor context cancellation. A successful return
// transfers ownership of the returned slice and string values to the adapter;
// implementations must not mutate or reuse them afterward.
type Searcher interface {
	Search(context.Context, string) ([]Source, error)
}

// SearcherFunc adapts a function to Searcher.
type SearcherFunc func(context.Context, string) ([]Source, error)

// Search calls fn.
func (fn SearcherFunc) Search(ctx context.Context, query string) ([]Source, error) {
	return fn(ctx, query)
}
