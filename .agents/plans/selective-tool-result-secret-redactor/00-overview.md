# Selective tool-result secret redactor

- **Status:** Ready
- **Implementation state:** Planning only. No implementation described here has occurred.
- **Change type:** Add extension, including the minimum module and CI foundation required by this currently empty library repository.
- **Selected milestone:** Native selective tool-result secret redactor.
- **Primary consumer:** A Go host that composes `eino-agent` extensions and needs secret-bearing tool-result fields sanitized before durable settlement and the next model turn.

## Application context

```json
{
  "application_context": {
    "has_active_users": false,
    "backward_compatibility_required": false,
    "feature_flags": "not-applicable",
    "confirmation_digest": "aa0017022cccea2465128fb9002a3f26596db73314e78bb5d97b4a6f95b9e240",
    "confirmed_at": "2026-08-30T20:37:42-04:00"
  }
}
```

The first public API may be designed directly around the selected behavior. There is no migration, compatibility shim, staged rollout, or feature-flag work.

## Requested outcome

Add one trusted native Go extension that:

1. mounts through `composition.Registry` and registers an ordered transform at `runtime.ToolResultTransformPoint`;
2. scans every callback-admitted tool result in the mounted scope except exact tool names explicitly excluded by the host;
3. applies a conservative built-in rule catalog plus optional host-supplied RE2 expressions;
4. redacts only matching spans in scannable string values;
5. when a key matches or a target cannot be safely scanned within budget, replaces only the affected scalar, `Structured`, metadata-map, or attachment-slice field with its fixed placeholder representation and continues settlement;
6. when upstream invokes the callback, sanitizes the result before durable tool output, result messages, downstream model input, and full-result lifecycle notices can consume it; and
7. preserves deterministic component identity, frozen-plan selection, resume matching, and quiescent unmount behavior supplied by `eino-agent@v0.1.3`.

## Measurable success

- A credential-free black-box integration test drives a synthetic tool result through a real `composition.Registry`, frozen `runtime.RunPlan`, `runtime.StreamingOrchestrator`, and SQLite settlement. Neither durable output nor the next model turn contains the synthetic secret.
- The integration test covers `runtime.ToolResult.Output`, every string key and value in `Structured`, result metadata keys and values, and attachment string fields plus attachment-metadata keys and values.
- Exact excluded tool names bypass scanning. Non-excluded tools scan by default.
- An invalid-encoding, over-byte-limit, over-cardinality, over-depth, over-node-budget, or over-match-budget field that reaches the callback becomes `[REDACTED]`; unaffected sibling fields remain unchanged and the tool call continues to terminal settlement.
- Invalid configuration fails before mount publication. Runtime content never causes the transform to return an error containing result data.
- Identical canonical configuration produces the same config hash regardless of map or exclusion input order. Any effective policy change changes the config hash and therefore the frozen-plan fingerprint.
- Unit, fuzz-seed, integration, race, vet, and module-resolution gates pass with Go 1.26.3 and `github.com/mattsp1290/eino-agent v0.1.3`.

## Scope

### In scope

- A new root Go module and a focused public `toolresultredactor` package.
- Conservative built-in detection rules, host rules, canonical configuration hashing, exact tool exclusions, explicit resource limits, field-local fallback redaction, and fixed placeholder behavior.
- Native component mounting, global or exact-session scope, deterministic late transform ordering, frozen-plan/resume identity, deactivation, close, docs, and tests.
- A credential-free host example and black-box acceptance path.

### Out of scope

- Non-empty syntactically invalid `runtime.ToolResult.Structured` JSON. `eino-agent v0.1.3` rejects that result before the transform waterfall, so the redactor cannot sanitize it or any sibling fields. Upstream makes durable/model-visible settlement generic, but trusted full-result observers may receive the original result.
- Scanning tool-call inputs, prompts, model responses, existing session files, filesystem artifacts, external attachment contents, logs, databases outside runtime settlement, or host credentials.
- An external scanner, network call, subprocess, Wasm component, TUI notification, command, widget, route, storage-retention policy, credential store, or deployment policy.
- Entropy-only guessing, validation against live providers, retroactive session rewriting, or a claim of complete secret detection.
- Publishing a release or modifying `eino-agent` or any other sibling repository.
- Pi API parity or a port of either comparator implementation.

## Repository-grounded findings

- **Repo fact:** The repository at revision `80684c8ba17ede18b4ad6aac716dbe4398f02b82` contains only `README.md`, license/ignore files, and the `next-milestone` skill. It has no Go module, code, tests, CI, example, plan, or release surface.
- **Local dependency fact:** `eino-agent@v0.1.3` resolves externally at commit `5e4f3ad2cc4b608379d22fa153940878b9110bc1`; its nested generated module resolves at `wasmext/gen@v0.1.0` commit `f8a2784061bb9df52ccb0db3a431c5100a99b798`.
- **Local dependency fact:** `runtime.ToolResultTransformPoint` is an ordered transform waterfall invoked in `runtime.StreamingOrchestrator.transformToolOutcome` before `BuildToolSettlement`. Its validator permits changes to `ToolResult` but protects `ToolName` and `ToolCall` identity.
- **Local dependency fact:** `extension.ApplyTransforms` validates the current value before calling the next handler. At `v0.1.3`, syntactically invalid non-empty `Structured` JSON is rejected before this redactor can inspect or replace it.
- **Local dependency fact:** If an earlier transform fails, the waterfall returns its original input and skips this late redactor. Durable/model-visible settlement becomes a generic tool failure, but `ToolSettledPoint` can receive the original full result. The host must treat other native transforms and full-result observers as trusted code.
- **Local dependency fact:** `runtime.ToolResult` contains `Output`, `Structured`, `Attachments`, and `Metadata`. Durable `runtime.ToolOutput` persists only bounded `Output` and `Structured`, while the transformed full result also reaches settled notices and metadata observations.
- **Local dependency fact:** `composition.Registry.Mount` atomically stages callback registration; `RunPlan` freezes handler/component/config identity; resume requires an exact fingerprint; `Deactivate` blocks new acquisition; and `Close` waits for acquired plans.
- **Local dependency fact:** The resolved module graph was independently verified with a clean external consumer running `go mod tidy`, `go list -m all`, `go mod verify`, `go test ./...`, and `go build ./...` without a consumer replacement. The durable request is `resolved` under the consumer's direct-verification rule.

## Capability-gap table

| Affected lane | Current state | Verified owner/contract | Planned delta |
| --- | --- | --- | --- |
| Repository/module foundation | Absent | This repository owns its module, package, CI, docs, and examples | Add the minimum Go 1.26.3 library foundation pinned to `eino-agent v0.1.3` |
| Guards and middleware | Runtime seam implemented upstream; reusable redactor absent here | `runtime.ToolResultTransformPoint`, `extension.OnTransform`, `composition.Registrar.Extensions()` | Add one immutable result-transform installer with explicit ordering and scope |
| Configuration and provenance | No local public config | Upstream fingerprints component artifact, config hash, registration, scope, and order | Canonicalize policy and derive a secret-free config hash included in component identity |
| Security and bounds | Runtime bounds settlement output but does not selectively detect secrets; invalid structured JSON is rejected before transforms | This repository owns reusable matching; host owns exclusions and policy choices; upstream owns pre-transform validation | Scan all callback-admitted result payload strings by default, placeholderize unsafe fields, and document the invalid-result exclusion |
| Durable identity and recovery | Upstream exact resume exists; no local component identity | `RunPlan.Descriptor`, `AcquireResumePlan`, `ErrExtensionPlanMismatch` | Prove identical policy resumes and changed policy fails before durable mutation |
| Host integration and teardown | No local installer/example | `composition.Registry.Mount`, `Mount.Deactivate`, `Mount.Close` | Add a black-box host example and quiescent unmount coverage; no extension-owned resources |

## Evidence from current references

All sources were accessed on 2026-08-30.

- **Pi, revision `853a80d26c90a14c1886f0ebb8ffaae133ca2185`:** its `tool_result` event runs after tool execution and before final result messages, lets ordered handlers modify the latest result, and supplies an abort signal. Pi separately requires bounded tool output. See [Pi extension documentation](https://github.com/earendil-works/pi/blob/853a80d26c90a14c1886f0ebb8ffaae133ca2185/packages/coding-agent/docs/extensions.md#tool_result).
- **davis7dotsh/my-pi-setup, revision `73bf4d826f39b5cab6b7865e706ba4a2669629ca`:** focused extensions consistently bound tool and subprocess output, but the suite has no equivalent all-tool result-secret transform. See its [extension inventory](https://github.com/davis7dotsh/my-pi-setup/tree/73bf4d826f39b5cab6b7865e706ba4a2669629ca/extensions).
- **ava-silver/pi-config, revision `faae3dca841378df7ca416f2e7d5458fac66d6a0`:** `session-secret-redaction` scans session files on lifecycle events using a provisioned external scanner, serializes runs, preserves JSONL validity, and tests duplicate findings. The selected milestone intentionally moves protection earlier to in-memory tool settlement, has no subprocess, and does not rewrite session files. See [source](https://github.com/ava-silver/pi-config/blob/faae3dca841378df7ca416f2e7d5458fac66d6a0/extensions/session-secret-redaction/index.ts) and [tests](https://github.com/ava-silver/pi-config/blob/faae3dca841378df7ca416f2e7d5458fac66d6a0/extensions/session-secret-redaction/index.test.ts).
- **Eino agent, tag `v0.1.3`:** authoritative public contracts are [component and transform types](https://github.com/mattsp1290/eino-agent/blob/v0.1.3/extension/types.go), [composition registry](https://github.com/mattsp1290/eino-agent/blob/v0.1.3/composition/registry.go), [tool-result transform](https://github.com/mattsp1290/eino-agent/blob/v0.1.3/runtime/extension_tool.go), and [tool settlement](https://github.com/mattsp1290/eino-agent/blob/v0.1.3/runtime/tool_settlement.go).

These references are behavioral and architectural evidence only. Implementation must be original Go code against Eino's public contracts.

## Decisions

1. **Native trusted code only.** The extension receives full in-process result data and performs no external I/O. Wasm and external scanners are separate milestones.
2. **All mounted-scope tools are included by default.** The only bypass is an exact, canonical tool-name exclusion supplied by the host. No glob or regex exclusions enter the first API.
3. **Detection combines built-ins and optional host rules.** Go's RE2 engine provides bounded regular-expression execution. Rules are compiled and validated before atomic mount.
4. **Callback-reachable runtime content is fail-safe and non-blocking.** Matching spans are redacted. A field that cannot be safely decoded or scanned within its configured budget becomes `[REDACTED]`. Other fields continue through settlement.
5. **Structured JSON keeps its structure and unchanged bytes when its keys are safe.** A bounded byte walker scans every JSON string key and value but rewrites only value literals. It never round-trips through `map[string]any`, reorders keys, changes number spellings, or redacts an object key; a matching or unsafe key makes the top-level `Structured` field the affected field and replaces it with its placeholder representation.
6. **The host retains transform-chain authority.** The package supplies a deterministic late default order. A trusted later transform can introduce new data, and a failing earlier transform prevents redactor invocation. The host must keep the redactor last and require earlier transforms to return sanitized success when it needs the full-result notice guarantee.
7. **Malformed non-empty `Structured` JSON is outside the result-level guarantee.** The user accepted the existing `v0.1.3` boundary: only results admitted to `ToolResultTransformPoint` are in scope. Invalid structured JSON keeps upstream's fail-closed durable/model behavior and trusted-observer limitation.

Rejected alternatives: full-result replacement on one bad field violates the user's field-local continuation decision; an external scanner adds a runtime dependency and failure mode; retroactive session-file rewriting leaves an avoidable pre-settlement exposure window; and unmarshal/remarshal of `Structured` can change valid tool semantics.

## Current and target flow

### Current

```text
host
  -> no local component or loader
  -> no registry mount
  -> no local frozen-plan identity
  -> tool result passes through upstream transform point unchanged
  -> bounded durable settlement / model-visible result
  -> upstream plan release and mount close only
```

### Target

```text
host-owned artifact identity + host policy
  -> toolresultredactor canonicalizes rules, exclusions, and limits
  -> composition.Registry.Mount atomically registers OnTransform
  -> frozen RunPlan records component + config hash + scope + order
  -> tool executes under upstream permissions
  -> upstream initial validation and earlier transforms succeed
  -> ToolResultTransformPoint waterfall reaches the late redactor
  -> excluded exact tool: unchanged
     otherwise: redact matching spans; placeholderize only unsafe fields
  -> upstream BuildToolSettlement bounds and commits sanitized Output/Structured
  -> next model turn + full-result settled notice observe sanitized result
  -> RunPlan.Release; Deactivate blocks new plans; Close drains acquired plans
```

## Ownership boundary

- **This repository:** immutable policy compilation, built-in rule catalog, field walker, structured JSON literal rewriter, config hash, native installer, docs, examples, and acceptance tests.
- **`eino-agent`:** typed transform contract, protected identity validation, ordering, composition, component/run-plan fingerprints, durable settlement, SQLite store, resume, cancellation classification, and quiescent mount lifecycle.
- **Host application:** component instance/artifact identity, scope, exclusions, extra expressions, resource budgets, transform-chain ordering, mount lifetime, storage retention, UI, auth, credentials, deployment, and release pin.

## Risks, assumptions, and gates

- **Stop/go gate:** implementation must pin `eino-agent v0.1.3` without `replace` or workspace dependencies. Any need for a private or newer upstream symbol stops implementation.
- **Security gate:** no error, diagnostic, test failure, config hash input, example, or metric may echo a tool-result value or host pattern expression.
- **Ordering risk:** a trusted transform after this component can add a secret. Docs and tests must make the late-order contract explicit without claiming an upstream terminal hook.
- **Predecessor-failure risk:** a failing earlier transform skips the redactor. Upstream keeps durable/model-visible output generic but can expose the original result to trusted `ToolSettledPoint` observers. This package cannot close that side channel at `v0.1.3`.
- **Ingress-bound risk:** upstream defensively clones `ToolResult` before invoking the callback. Package limits bound its own processing, not result construction or upstream pre-callback cloning; hosts must bound tool result creation.
- **Detection limitation:** built-ins intentionally favor precision and cannot prove absence of every credential. Documentation must state this and show host-rule extension.
- **Assumption:** exact tool names are the correct first exclusion key because `ToolName` is protected and stable at the transform boundary.
- **No blocking questions:** The user resolved detection, coverage, field-local fallback, malformed structured JSON scope, active-user, and compatibility decisions. Operational limits remain explicit required host configuration rather than invented defaults.

## Document map

- [01-module-foundation.md](01-module-foundation.md) — establish the consumable Go module, dependency pin, repository checks, and CI.
- [02-redaction-engine.md](02-redaction-engine.md) — define canonical policy, built-ins, match replacement, structured JSON walking, and bounds.
- [03-runtime-integration.md](03-runtime-integration.md) — mount the native component through public Eino contracts and preserve identity, ordering, resume, and teardown behavior.
- [04-verification-and-delivery.md](04-verification-and-delivery.md) — prove the public journey through SQLite settlement and document consumer use and limitations.
- [05-execution-handoff.md](05-execution-handoff.md) — execute dependency-ordered work packages and final gates.
