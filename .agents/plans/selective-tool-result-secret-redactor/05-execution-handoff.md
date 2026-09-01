# 05 — Execution handoff

## Implementation sequence

Execute these work packages in order. Keep the selected milestone in one coherent implementation branch or use the boundaries below as sequential human-reviewable changes. Do not parallelize a package before its prerequisite public contract is stable.

### WP1 — Establish the consumable module

- **Result:** A clean Go 1.26.3 module resolves `eino-agent v0.1.3` without local replacements and has baseline CI.
- **Files:** **new** `go.mod`, **new** `go.sum`, **new** `.github/workflows/ci.yml`, existing `README.md`, **new** `toolresultredactor/doc.go`.
- **Prerequisites:** None.
- **Work:** Follow [01-module-foundation.md](01-module-foundation.md). Add only the dependency surface needed by the package.
- **Verification:** `GOWORK=off go mod tidy`, `go list -m all`, `go mod verify`, a compiling placeholder package test, and CI syntax inspection.
- **Acceptance:** Root and nested Eino modules select the verified versions with `replacement=false`; no `replace`/vendor/workspace shortcut exists.

### WP2 — Implement the immutable policy engine

- **Result:** Canonical host policy and built-ins sanitize all in-scope result fields with deterministic range merging and field-local fallback.
- **Files/symbols:** **new** `toolresultredactor/config.go` (`Pattern`, `Limits`, `Options`, `ConfigHash`), **new** `patterns.go`, **new** `redact.go`, **new** `json.go`, and their **new** unit/fuzz tests.
- **Prerequisites:** WP1 exact module pin.
- **Work:** Follow [02-redaction-engine.md](02-redaction-engine.md). Keep the engine free of lifecycle and I/O concerns.
- **Verification:** focused package tests, fuzz seed corpus under normal `go test`, repeated hash tests, concurrent redaction under `-race`, and fixture-output audit.
- **Acceptance:** no-match fully scannable structured JSON is byte-identical; matching values redact only merged spans; matching or unsafe keys placeholderize only their parent `Structured` or metadata-map field; `MaxMatchesPerField` is aggregate across all rules; zero-width-capable host expressions fail validation before mount; other unsafe scalar/collection fields alone become their defined placeholder representation; errors never contain pattern expressions or result values.

### WP3 — Bind the engine to the public Eino transform

- **Result:** Hosts mount one native component that participates in atomic registration, frozen identity, exact resume, and quiescent close.
- **Files/symbols:** **new** `toolresultredactor/mount.go` (`Mount` and late order constant), **new** `mount_test.go`, initial **new** `integration_test.go` lifecycle and fingerprint cases.
- **Prerequisites:** WP2 public options/config-hash behavior is stable.
- **Work:** Follow [03-runtime-integration.md](03-runtime-integration.md). Use only public `composition`, `extension`, and `runtime` contracts from `v0.1.3`.
- **Verification:** descriptor inspection, atomic failed mount, default global and explicit session scope, earlier-transform ordering, same/changed policy resume, deactivation, bounded close, and `go test -race`.
- **Acceptance:** the callback mutates only `Result`; exact exclusions alone bypass; every callback-reachable runtime data condition returns sanitized success; no extension-owned resource survives close.

### WP4 — Prove durable settlement and document delivery

- **Result:** A credential-free black-box test and compilable example prove the observable host journey and bounded claims.
- **Files:** complete **new** `toolresultredactor/integration_test.go`; **new** `examples/tool-result-redactor/main.go` and `main_test.go`; finish existing `README.md`; finish **new** package docs and CI.
- **Prerequisites:** WP3 mount API and lifecycle behavior.
- **Work:** Follow [04-verification-and-delivery.md](04-verification-and-delivery.md).
- **Verification:** real registry/run plan/orchestrator/SQLite/model-turn path; every supported field; exclusions; field-local bounds; cancellation; ordering; resume mismatch before mutation; lifecycle; full repository commands.
- **Acceptance:** neither durable records, next-turn model input, settled notices, diagnostics, nor test output expose the synthetic marker for non-excluded tools.

## Dependency and parallelization constraints

```text
WP1 module pin
  -> WP2 policy API and engine
     -> WP3 mount, identity, lifecycle, resume
        -> WP4 durable acceptance, example, docs, final CI
```

- Unit fixtures for WP2 can be developed in parallel after `Options` and placeholder semantics are fixed.
- Runtime integration must not begin against invented callbacks; first compile a minimal mount against the exact `v0.1.3` public types.
- Documentation can be drafted during WP3 but must be reconciled with the tested public API and accepted limitations in WP4.

## Stop/go gates

1. **Dependency gate:** Stop if any required public contract is absent from `eino-agent v0.1.3`; do not import private code or add a sibling replacement.
2. **Callback-admission gate:** Preserve the accepted `v0.1.3` boundary. Non-empty syntactically invalid `Structured` JSON must skip the waterfall, produce generic durable/model-visible failure, and remain documented as outside the redactor's result-level guarantee.
3. **Runtime disclosure gate:** The redactor's own cancellation/recovery paths must return sanitized success. Document that a failing predecessor skips the callback and a failing successor restores the original pre-waterfall result; either can expose that result to trusted `ToolSettledPoint` observers.
4. **Structured fidelity gate:** Stop if the JSON implementation changes an unmatched fully scannable valid-encoding document's bytes or schema semantics. Do not substitute generic unmarshal/remarshal.
5. **Resume gate:** Stop if an effective policy change does not alter the frozen fingerprint or if mismatch occurs after durable mutation.
6. **Ordering gate:** Do not claim terminal redaction unless the redactor is the final result transform and tests document both predecessor- and successor-failure limitations.

## Integration and regression gates

For each work package, run focused tests first. Before completion, run:

```text
GOWORK=off go mod tidy
git diff --exit-code -- go.mod go.sum
GOWORK=off go list -m all
GOWORK=off go mod verify
GOWORK=off go vet ./...
GOWORK=off go test ./...
GOWORK=off go test -race ./...
```

Manually verify:

- every path named as existing still resolves;
- every new path remains within this repository;
- no comparator code, prompt, schema, or internal boundary was copied;
- no credential-like fixture or raw pattern expression appears in output;
- no feature flag, migration, or compatibility shim was added; and
- the working tree contains no unrelated changes.

## Definition of done

- The package mounts through `composition.Registry` using `runtime.ToolResultTransformPoint` from the verified module pin.
- Built-ins plus optional host patterns inspect all in-scope string keys and values and sanitize every non-excluded callback-admitted tool result.
- Exact exclusions, required budgets, field-local placeholder behavior, and incomplete-detection limits are public and tested.
- Configuration and registration identity are deterministic and participate in exact resume.
- The real durable settlement and next-model-turn path contains no synthetic secret whenever upstream invokes the redactor.
- The malformed-structured limitation test proves the transform is skipped, durable/model output fails closed, and trusted full-result notices remain outside the package guarantee.
- Cancellation, internal recovery, mount failure, concurrent calls, resume mismatch, deactivation, and close have observable tests.
- CI and all final commands pass from a clean checkout with `GOWORK=off`.
- README and example accurately state trust, ordering, scope, ownership, and non-goals.
- No implementation work is claimed by this plan itself.

## Deferred follow-up

- Optional per-built-in enable/disable policy after false-positive evidence.
- Validated external scanner adapters or Wasm containment.
- Metrics that expose only safe aggregate counts under an explicit observability contract.
- Scanning external attachment contents or retroactive durable records under host-owned storage policy.
- Release/tag automation, SBOM/provenance, compatibility guarantees, and multi-platform matrices.
