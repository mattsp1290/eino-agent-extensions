// Package websearch mounts a bounded, backend-neutral web_search tool for
// Eino Agent. The embedding host supplies trusted Searcher code and owns its
// credentials, network policy, rate limits, freshness, diagnostics, and
// lifecycle. This package owns the model schema, semantic validation, bounded
// source records, callback admission, timeout, and quiescent cleanup.
//
// Queries and successful source values are durable model-visible content and
// must not contain secrets. Searcher implementations must be concurrency-safe,
// honor cancellation, and transfer ownership of successful result slices and
// string values to this package.
package websearch
