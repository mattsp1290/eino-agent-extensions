# 02 — Redaction engine

## Goal and prerequisite state

Implement an immutable, deterministic, bounded policy engine that inspects every string key and value in the payload of a callback-admitted `runtime.ToolResult`. It must not depend on Eino lifecycle state, I/O, a clock, randomness, environment variables, or credentials.

Prerequisite: [01-module-foundation.md](01-module-foundation.md) has established the module and `eino-agent v0.1.3` pin.

## Proposed package surface

All names below are **proposed** in the **new** `toolresultredactor/` directory. The implementation may refine unexported names, but it must preserve the described public responsibilities.

| Path/symbol | State | Responsibility |
| --- | --- | --- |
| `toolresultredactor/config.go` | **new** | Export `Pattern`, `Limits`, `Options`, `Placeholder`, configuration validation, canonicalization, and `ConfigHash` |
| `toolresultredactor/patterns.go` | **new** | Own versioned built-in rule definitions and compile built-in plus host RE2 rules |
| `toolresultredactor/redact.go` | **new** | Inspect every in-scope string key and value in `runtime.ToolResult`, redact matching values, and apply parent-field fallback for matching or unsafe keys using an immutable compiled policy |
| `toolresultredactor/json.go` | **new** | Walk valid structured JSON by byte offsets, scan key and value string literals, and rewrite only matching value literals within explicit budgets |
| `toolresultredactor/config_test.go` | **new** | Prove validation, canonicalization, hash stability, and secret-free errors |
| `toolresultredactor/patterns_test.go` | **new** | Prove each built-in with synthetic fixtures plus precision-oriented nonmatches and overlap handling |
| `toolresultredactor/redact_test.go` | **new** | Prove field coverage, exact exclusions, field-local fallback, immutability, and concurrency |
| `toolresultredactor/json_test.go` | **new** | Prove byte preservation, escapes, duplicate keys, numeric spelling, nesting, bounds, and fuzz seeds |

### Proposed public values

```text
const Placeholder = "[REDACTED]"

type Pattern struct {
    ID         string
    Expression string
}

type Limits struct {
    MaxFieldBytes       int
    MaxStructuredBytes  int
    MaxStructuredDepth  int
    MaxStructuredNodes  int
    MaxAttachments      int
    MaxMetadataEntries  int
    MaxMatchesPerField  int
    MaxPatterns         int
    MaxPatternBytes     int
}

type Options struct {
    Scope              extension.Scope
    Order              int
    ExcludedTools      []string
    AdditionalPatterns []Pattern
    Limits             Limits
}

func ConfigHash(Options) (string, error)
```

These are proposed API shapes, not existing contracts. `Limits` values are required and positive; this plan intentionally does not invent operational defaults. A host must choose budgets based on its tool-result retention and latency envelope. Zero `Scope` canonicalizes to `extension.GlobalScope()`. Zero `Order` canonicalizes to the package's documented late fixed order constant. The fixed value must be architecture-independent and must not use `math.MaxInt`.

## Canonical configuration and identity

Build one canonical policy before mount:

1. validate every exclusion as a non-empty exact tool name using the upstream identifier constraints where applicable;
2. deduplicate and lexicographically sort exclusions;
3. validate host rule IDs as stable non-secret identifiers, reject duplicate IDs, reject empty expressions, and enforce `MaxPatterns` and `MaxPatternBytes` before compilation;
4. parse every host expression with `regexp/syntax` and reject expressions whose minimum consumed width is zero, including empty alternatives, assertions or anchors alone, optional/star-only forms, and nested equivalents;
5. compute minimum consumed width recursively over concatenation, alternation, capture, and repetition nodes, then compile accepted expressions with Go `regexp`, whose syntax and execution use RE2 semantics;
6. sort host rules by `(ID, Expression)` because match ranges are unioned and rule order has no behavioral priority;
7. include the built-in catalog version, fixed placeholder version, canonical limits, canonical exclusions, and canonical host rule identities/expressions in a canonical JSON hash payload; and
8. hash that payload with SHA-256 to produce lowercase hex `ConfigHash`.

Do not include compiled regexp internals, pointer identities, map iteration order, runtime values, environment data, credentials, or absolute paths. Never place raw result content in config errors. A regexp compilation error should name only the safe rule ID and a bounded error code; it must not echo an expression because a host may have embedded a literal value.

Scope and order are already durable registration identity upstream. Keep them out of the policy config hash, but test that changing either still changes the complete run-plan fingerprint.

## Built-in conservative catalog

Create this normative, explicitly versioned `builtin-v1` catalog. Each rule redacts its entire matched span; it never relies on capture-group replacement.

| Stable rule ID | Required match boundary and span | Authority | Synthetic fixtures and exclusions |
| --- | --- | --- | --- |
| `pem-private-key-block` | Match a complete ASCII-armored block from an exact `-----BEGIN <label>-----` line through the corresponding end line when `<label>` is `PRIVATE KEY` or `ENCRYPTED PRIVATE KEY`. Include embedded base64/newlines in the redacted span. | [RFC 7468 textual-encoding boundaries and private-key labels](https://www.rfc-editor.org/rfc/rfc7468.html) | Positive: unusable short synthetic armored bodies with both allowed labels and LF/CRLF. Near miss: `PUBLIC KEY`, `CERTIFICATE`, missing end boundary, and prose containing `BEGIN` only. |
| `authorization-bearer` | Case-insensitive ASCII `Authorization`, optional horizontal space, `:`, optional horizontal space, `Bearer`, one or more spaces, then an RFC 6750 `b64token` containing one or more permitted characters. Redact the complete header assignment so no credential fragment remains. | [RFC 6750 section 2.1 bearer grammar](https://www.rfc-editor.org/rfc/rfc6750.html#section-2.1) | Positive/boundary: a one-character unusable token, longer tokens using each permitted class, and optional padding. Near miss: an empty token, `Bearer` prose without the header label, other auth schemes, and invalid token characters. |
| `github-prefixed-token` | At an ASCII non-token boundary, match prefix `ghp_`, `github_pat_`, `gho_`, `ghu_`, `ghs_`, or `ghr_` followed by at least 16 documented token characters through the next non-token boundary. Match the full token, including the 2026 `ghs_APPID_JWT` shape; do not require legacy fixed total length. | [GitHub token-format documentation](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/about-authentication-to-github#githubs-token-formats) | One unusable synthetic token per prefix plus boundary cases. Near miss: prefix alone, fewer than 16 payload characters, embedded identifier text, ordinary `GITHUB_TOKEN`, and similar `ghx_` text. |

Do not add a generic labeled-assignment or another provider format to `builtin-v1` without amending this plan or a follow-up decision with equally precise authoritative evidence. Optional host rules cover application-specific labels and providers.

Do not use pure entropy as a built-in detector, match ordinary UUIDs or hashes merely because they are long, or claim the catalog is exhaustive. Each rule needs positive, boundary, escaped, duplicate, overlap, and near-miss fixtures. Fixtures must be unmistakably synthetic and unusable.

Host rules extend rather than replace built-ins. The first API has no host option to disable individual built-ins; a future false-positive policy can add that only with concrete evidence.

## Scalar redaction algorithm

For each scannable string field:

1. if it is invalid UTF-8 or exceeds `MaxFieldBytes`, replace the entire field with `Placeholder` and return success;
2. maintain one aggregate match count for the field across all built-in and host rules, querying each rule for at most the remaining budget plus one non-empty match;
3. if the next non-empty match would make the aggregate count exceed `MaxMatchesPerField`, stop scanning, replace the entire scalar field with `Placeholder`, and return success;
4. collect every accepted byte range, sort by start/end, and merge overlapping or adjacent ranges;
5. build one output that preserves every unmatched byte and substitutes one `Placeholder` for each merged range; and
6. return the original string allocation/value when no range matches where practical.

Never apply one rule to text already changed by another rule. Unioning ranges prevents placeholder text from becoming a new match and makes output independent of rule order. Detection count and rule IDs are not added to result metadata in this milestone because that creates a new observability and information-disclosure surface.

## Tool-result field coverage

Unless `ToolName` is in the exact canonical exclusion set, scan:

- `Result.Output` as one field;
- every JSON string key and value in `Result.Structured`;
- every key and value in `Result.Metadata`;
- every attachment's `ID`, `MIMEType`, `Name`, and `URL`; and
- every key and value in each attachment's metadata.

Treat each scalar value above as an independently affected field. Scan keys but never rewrite them because changing schema or map keys can alter tool semantics or create collisions. If a key matches, is unsafe, or exceeds a scalar budget, replace its entire containing field: top-level `Structured`, `Result.Metadata`, or that attachment's metadata map. Preserve slice order, safe map keys, nil versus empty containers, and non-string JSON values. Return a defensive transformed copy; never mutate the callback input's backing slices or maps even though upstream currently clones it.

Before traversing a collection, enforce the canonical cardinality limits:

- if raw `Structured` exceeds `MaxStructuredBytes`, replace only `Structured` with the JSON string placeholder;
- if result or attachment metadata exceeds `MaxMetadataEntries`, or contains a matching or unsafe key, replace that entire map field with the package-reserved sentinel `map[string]string{"": Placeholder}`; the empty key cannot match any accepted positive-width rule;
- if `Attachments` exceeds `MaxAttachments`, replace that entire slice field with one sentinel attachment containing only `Name: Placeholder`; and
- once these gates pass, `MaxFieldBytes`, attachment count, metadata count, structured bytes/nodes/depth, pattern count, and matches per field jointly bound package-owned traversal and allocation.

The package cannot bound memory already spent by a tool or by upstream's defensive pre-callback clone. README must require hosts and tool implementations to bound result construction before this extension.

## Structured JSON deep dive

`runtime.ToolResultTransformPoint` rejects syntactically invalid non-empty `Structured` JSON before callback invocation. The user explicitly accepted that upstream-invalid result as outside this component's guarantee. The byte walker still needs fail-safe validation for callback-admitted JSON with unsafe string encodings and for resource budgets.

Implement a small byte-offset walker over the original `json.RawMessage`:

- distinguish object keys from values, scan both, and never rewrite keys;
- identify exact byte spans for every string literal;
- validate raw UTF-8 and escaped UTF-16 surrogate pairing losslessly before decoding a value literal; never allow `encoding/json` to normalize unsafe sequences to U+FFFD;
- decode a proven-valid candidate literal with `encoding/json` and run scalar scanning; re-encode only a changed value literal;
- copy every untouched byte verbatim so whitespace, key order, duplicate keys, exponent form, and number spelling remain stable;
- count structural nodes and depth before descending;
- treat a matching key, an over-byte-limit key, unsafe raw UTF-8, lone/misordered UTF-16 surrogate escapes, key match-budget exhaustion, depth exhaustion, node-budget exhaustion, or raw structured-byte exhaustion as making the **top-level `Structured` field** unscannable and replace it with the valid JSON string literal `"[REDACTED]"`; and
- treat an over-byte-limit individual JSON string value as affecting only that value and replace only its literal.

Do not unmarshal the document into `map[string]any`; that can collapse duplicate keys, reorder keys, and rewrite numbers. Object keys are detection inputs, but are never individually changed: any matching or unsafe key triggers top-level `Structured` fallback.

## Runtime failure policy

The compiled policy must be immutable and safe for concurrent calls. Content conditions are data, not callback failures.

- Invalid/over-limit fields return a placeholderized result and `nil` error.
- If the callback observes cancellation while walking a result, it must quickly replace every not-yet-proven-safe target field with `Placeholder` and still return a sanitized result with `nil` error. This avoids returning the original result through upstream failure notices.
- Wrap the internal transform boundary with a narrow recovery that discards unproven content, placeholderizes all target fields, and returns success. The recovery must not expose a recovered value or result data.
- Only configuration and mount errors may return errors. Runtime result content must not produce a raw error path.

This policy is intentionally stricter than simply returning `ctx.Err()`: at `eino-agent v0.1.3`, a failed result transform makes durable model output generic, but the untransformed `ToolResult` can still reach `ToolSettledNotice`. Returning a sanitized value prevents that side-channel.

## Tests and acceptance

### Unit matrix

- Every built-in positive and near-miss fixture, including a one-character RFC-valid Bearer credential.
- Multiple, duplicate, adjacent, nested, and overlapping host/built-in matches, plus a multi-rule case proving `MaxMatchesPerField` is aggregate rather than per rule.
- Unicode before and after byte ranges; invalid UTF-8; exact byte boundary; empty string; placeholder already present.
- Every top-level result and attachment field, JSON/map keys and values, key-triggered parent-field fallback, nil containers, map-copy isolation, and concurrent calls.
- Exact exclusion match, case difference, prefix/suffix difference, and empty/duplicate exclusion validation.
- Canonical hash invariance under input ordering and hash changes for every effective policy field and built-in catalog version.
- JSON objects, arrays, scalars, escaped Unicode, valid surrogate pairs, lone/misordered surrogates, invalid raw UTF-8, duplicate keys, whitespace, large numbers, exponent spellings, safe keys, secret-shaped keys, and string values.
- Structured depth/node exhaustion replaces only `Structured`; one over-limit JSON value preserves siblings.
- Huge structured whitespace/key/numeric literals hit `MaxStructuredBytes`; too many attachments or metadata entries replace only the affected collection field; aggregate high-match input reaches field fallback; and every zero-width-capable host expression is rejected before mount with an error that names only its safe rule ID.

### Fuzz targets

Add fuzz entry points whose seed corpus runs under ordinary `go test`:

- arbitrary UTF-8/invalid-byte scalar input never panics and never returns an invalid string transformation state;
- arbitrary `json.Valid` documents remain valid after transformation;
- no-match, fully scannable valid-encoding JSON is byte-identical;
- a transformed document never contains any injected synthetic secret covered by a supplied test rule; and
- bounds/cancellation/recovery paths complete without returning raw fixture data in errors.

## Dependencies, risks, and exclusions

- This package uses no external scanner or regex package.
- RE2 avoids catastrophic backtracking, but total work is still proportional to configured pattern count times scanned bytes. Required byte/cardinality/match limits bound package-owned work after upstream cloning.
- Conservative regexes can still produce false positives. Field-local replacement limits blast radius, and exact exclusions are the explicit host escape hatch.
- Do not expose a generic matcher API, dynamic reload, mutable rule registry, statistics callback, or environment-driven configuration in this milestone.
