# 04 — Verification and delivery

## Goal and prerequisite state

Prove the selected host-visible journey through real public Eino composition and runtime contracts, then document exactly what the package protects and what remains host-owned.

Prerequisites: all preceding work files.

## Change surface

| Path | State | Planned purpose |
| --- | --- | --- |
| `toolresultredactor/integration_test.go` | **new**, external package test | Credential-free end-to-end orchestration, settlement, resume, ordering, and lifecycle tests |
| `examples/tool-result-redactor/main.go` | **new**, parent `examples/` proposed under repository root | Minimal compilable mount example using only synthetic patterns and no credentials |
| `examples/tool-result-redactor/main_test.go` | **new** | Exercise the example's mount and cleanup path without a provider credential |
| `README.md` | existing | Consumer setup, policy table, exact exclusions, required limits, ordering, trust, lifecycle, and limitations |
| `.github/workflows/ci.yml` | **new** | Run all verification commands from a clean checkout |

## Credential-free black-box acceptance path

Write the main acceptance test in package `toolresultredactor_test` so it can use only exported APIs from this module and `eino-agent v0.1.3`.

Construct:

- a real `composition.Registry`;
- a valid native `extension.Component` with synthetic artifact identity;
- `toolresultredactor.Mount` with global scope and required small test budgets;
- a real SQLite execution store in `t.TempDir()`;
- a deterministic ID generator and clock;
- a credential-free fake model resolver/streamer that first emits one tool call, then captures the persisted/model-visible tool-result message and returns a final assistant message; and
- one runtime tool whose result places the synthetic marker in every supported payload field, with safe sibling content beside it.

Drive `runtime.NewStreamingOrchestrator` and `Start`, wait for `Handle.Done`, then query public store/session APIs. Prove:

1. the tool executes once;
2. the run and tool reach their expected terminal states;
3. the durable tool-call output, result message, and result part contain no marker;
4. every supported field observed after transformation contains either preserved safe content plus `[REDACTED]` or the field-local placeholder;
5. the second model turn sees sanitized `Output` and `Structured` content;
6. a `ToolSettledPoint` observer receives the sanitized full `ToolResult`; wait on a bounded buffered channel and fail if the expected asynchronous notice is absent;
7. the Eino observability path contains only its documented sanitized `permission_status` projection and is not described as a full-result observer;
8. no error or diagnostic contains the marker; and
9. releasing the run plan and closing the mount completes without leaked goroutines or resources.

The fake model is only a deterministic driver of the real orchestration path. Documentation must not call this a live-provider test.

## Required integration scenarios

### Default coverage and exclusions

- With `AdditionalPatterns` empty, drive one unmistakably synthetic unusable fixture for each normative `builtin-v1` rule through the public mount/orchestrator path and prove the default catalog is active.
- In a separate case, configure one synthetic host rule whose marker is not credential-shaped and prove host rules extend the active built-ins.
- Put a synthetic match in a `Structured` object key, a result-metadata key, and an attachment-metadata key in separate black-box cases. Prove the redactor never rewrites a key, replaces only the containing `Structured` or metadata-map field with its defined placeholder representation, and leaves no marker in durable, model-visible, or settled-notice observations as applicable.
- A non-excluded arbitrary tool name is sanitized without an allowlist entry.
- An exact excluded tool name bypasses all result fields unchanged using a non-secret sentinel.
- Case, prefix, and suffix variants do not bypass.
- A session-scoped mount affects only that exact durable session.

### Field-local continuation

Use separate cases for:

- over-byte-limit `Output` with safe `Structured` sibling;
- one over-limit JSON value with safe sibling values;
- structured depth/node exhaustion, which replaces only the top-level `Structured` field;
- raw structured-byte, attachment-count, metadata-entry, and match-count exhaustion, each replacing only its defined scalar or collection field;
- a multi-rule input proving `MaxMatchesPerField` is one aggregate budget across all built-in and host rules;
- a matching, invalid-encoding, or over-byte-limit JSON/map key, each replacing only its parent `Structured` or metadata-map field;
- invalid UTF-8 in one metadata or attachment field; and
- syntactically valid JSON containing invalid raw UTF-8 or lone/misordered surrogate escapes, which replaces only `Structured`; and
- cancellation injected between structured nodes.

Every case must settle rather than fail because of redaction, preserve unaffected siblings, and keep the unsafe field out of durable/model/notice observations.

Also prove that every host expression capable of a zero-width match is rejected before mount publication. The error may name only the safe rule ID and bounded error code, never the expression.

### Transform ordering

Mount a first transform that adds a synthetic marker, then mount the redactor at its canonical late order. The settled result must be sanitized. Separately verify the descriptor records the order. Do not mount a later content-producing transform in the success example because the package cannot claim terminal-hook authority.

Add a limitation test where an earlier transform returns an error. Assert durable/model-visible settlement is generic and contains no marker, then document that `ToolSettledPoint` can receive the original full result because the redactor was skipped. The assertion must not print the marker. This is a host trust precondition, not redactor success.

### Upstream-invalid structured result

Use a direct native executor that returns non-empty syntactically invalid `Structured` JSON plus synthetic markers in sibling fields. Prove the accepted boundary without exposing fixture data:

1. register a counting transform at `ToolResultTransformPoint` and assert its invocation count remains zero because initial validation skips the entire waterfall;
2. assert the tool becomes an operational failure;
3. assert durable tool output, result part, and the next model turn contain only upstream's generic failure and no marker;
4. use boolean or digest comparisons to prove `ToolSettledPoint` receives the original full result; and
5. state that neither this component nor its exact exclusions make a result-level guarantee for this upstream-invalid case.

### Resume and fingerprint

Build a public fixture around a durable pending tool call:

1. persist/admit a run with the redactor's frozen descriptor and a pending normalized tool call;
2. reconstruct the registry with the same artifact and semantically identical reordered policy input;
3. call the public resume path and prove the pending tool executes once and settles sanitized;
4. repeat from an isolated fixture with one effective policy change;
5. assert `runtime.ErrExtensionPlanMismatch`; and
6. query the store to prove the pending call and run state did not change after the mismatch.

Also cover upstream's running-call rule: a running call is terminalized/interrupted without re-execution, and the extension does not claim to rescan previously persisted raw output.

### Mount lifecycle

Run the deactivation and quiescent-close sequence in [03-runtime-integration.md](03-runtime-integration.md), including timeout while a frozen plan is held and successful close after release.

## Unit, fuzz, and race gates

The full suite must include:

- table-driven unit tests for configuration, patterns, scalar fields, structured JSON, and mount validation;
- fuzz targets and seed invariants from [02-redaction-engine.md](02-redaction-engine.md);
- black-box integration against real composition/runtime/store boundaries;
- `go test -race ./...` for immutable concurrent use and mount lifecycle; and
- `go vet ./...` plus clean module verification.

Tests must never print the synthetic marker in failure messages. Compare using booleans or digests and use labels such as `fixture-secret-present=true`, not raw values.

## Documentation contract

README and package docs must state:

- this is trusted native code operating on in-memory tool-result fields;
- global mount scans all callback-admitted tool results by default; exclusions are exact and host-controlled;
- the built-in catalog is conservative and incomplete; host rules extend it;
- operational limits are required host inputs and unsafe fields become `[REDACTED]`;
- package limits bound callback work but cannot bound tool allocation or upstream pre-callback cloning;
- JSON and metadata-map keys are scanned but never rewritten; a matching or unsafe key placeholderizes its containing `Structured` or metadata-map field, while non-string JSON values are not scanned;
- non-empty syntactically invalid `Structured` JSON is rejected before callback invocation; the redactor does not sanitize that result or its siblings, durable/model-visible settlement is generic, and full-result observers must remain trusted;
- external attachment contents, existing session files, prompts, model responses, logs, and host storage outside Eino settlement are not scanned;
- `eino-agent v0.1.3` is the verified minimum pin for this first release;
- the redactor must be the final `ToolResultTransformPoint` callback when full-result notice protection is required;
- other transforms must not fail with an unsanitized result when full-result notice protection is required;
- no external scanner, subprocess, network, credential, or dynamic reload is used;
- deactivation affects new plans, close drains acquired plans, and removing a component can make unfinished durable runs fail exact resume; and
- no Pi/comparator parity or complete leak-prevention claim is made.

The example should show:

1. host construction of `composition.Registry`;
2. synthetic native artifact identity;
3. explicit required `Limits`;
4. one exact exclusion and one synthetic additional pattern;
5. `Mount`, deferred `Deactivate`, and bounded `Close`; and
6. host responsibility for final transform-chain order.

Do not include real-looking credential literals, environment-variable reads, or provider setup.

## Final verification procedure

Run from a clean worktree with no `go.work` influence:

```text
GOWORK=off go mod tidy
git diff --exit-code -- go.mod go.sum
GOWORK=off go list -m all
GOWORK=off go mod verify
GOWORK=off go vet ./...
GOWORK=off go test ./...
GOWORK=off go test -race ./...
```

Then inspect test output and documentation for accidental fixture values. Confirm `git status --short` includes only the implementation's intended files.

## Definition of acceptance

The milestone is complete only when the public mount → frozen plan → transform → durable SQLite settlement → next model turn → release/close path passes without credentials and proves that every callback-admitted in-scope unsafe field is sanitized. Unit-only matcher success or direct invocation of an unmounted callback is insufficient.

No configured-service smoke test applies because this design has no service dependency.
