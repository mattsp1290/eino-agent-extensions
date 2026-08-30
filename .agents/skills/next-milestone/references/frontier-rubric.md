# Eino Agent Extensions Frontier Rubric

Use this reference after repository, local-dependency, and web evidence has been collected. It defines comparison, candidate generation, readiness, and post-selection architecture framing.

## Capability matrix

Assess relevant lanes independently. Record state, repository evidence, local dependency and owner, Pi contract, davis7dotsh evidence, ava-silver evidence, gap, and relevance to the next one or two milestones.

| Lane | Questions to test |
| --- | --- |
| Repository and module foundation | Go module identity, supported Go/Eino versions, package boundaries, consumer install path, examples, fixtures, tests, CI, lint, release/tag strategy, compatibility policy |
| Component construction and provenance | Public constructors/installers, native versus Wasm artifact identity, version/hash/config hash, stable IDs, scope, deterministic order, atomic registration, rollback on failed mount |
| Lifecycle and teardown | Startup boundary, per-run acquisition, cancellation/deadlines, long-lived resources, session/run transitions, deactivation, quiescent close, idempotent cleanup, leak and race behavior |
| Tools and external processes | Typed schemas, decode/execute/encode, permissions and patterns, working-directory authority, subprocess trees, output limits/artifacts, timeout/escalation, retry safety, platform dependencies |
| Context, prompts, and hooks | Supported semantic point, ordering, data-only payloads, contribution roles, prompt identity, failure behavior, durable request relationship, resume semantics |
| Guards and middleware | Deny-only authority, argument normalization, permission ordering, result protection, fail-closed behavior, exactly-once boundaries, native/Wasm parity |
| Events and observability | Runtime notices, post-commit delivery, backpressure, error containment, redaction, correlation, low-cardinality metadata, no raw content leakage |
| Durable identity and recovery | Frozen plan descriptor, fingerprints, configuration drift, exact resume matching, pending versus running tool behavior, session scope, restart behavior |
| Configuration and discovery | Host-supplied config, validation, trust, file/network loading boundary, defaults and overrides, hot reload or explicit non-support, secret resolution, capability grants |
| Host integration | Minimal mount example, orchestrator composition, errors and diagnostics, TUI/HTTP adapter seam, explicit host responsibilities, coexistence with other extensions |
| Wasm containment | WIT world/version, allowed-root and digest checks, no ambient WASI, resource bounds, epoch interruption, curated imports/DTOs, loader shutdown, CGO/platform implications |
| Security and delivery | Secret/redaction policy, path/symlink containment, network and process capabilities, supply-chain provenance, fuzz/race/integration tests, cross-platform support, documentation and migration |

Do not require every lane in every milestone. Include only lanes whose behavior or ownership changes and show important deferred lanes explicitly.

## Reference extension flow

Build three grounded views:

1. `Current flow`: host config/construction → extension component and installer/loader → `composition.Registry` atomic mount → frozen `runtime.RunPlan` → typed callback/tool/prompt/guard/restriction or event sink → durable/live runtime behavior → quiescent release and cleanup.
2. `Reference lessons`: relevant Pi and comparator user behavior plus lifecycle and organization lessons, without copying their TypeScript structure or TUI-private boundaries.
3. `Candidate delta`: the smallest before/after flow for each option, labeling missing, proposed, mocked, external, host-owned, and upstream-owned seams.

For affected flows, test:

- registration is atomic and uses stable component, artifact, capability, scope, and configuration identity;
- blocking filesystem, network, model, or subprocess work starts only after the appropriate runtime boundary and observes cancellation and deadlines;
- mutable or secret host values do not enter plan fingerprints, request records, logs, or observability;
- tool inputs and outputs are validated and bounded, permissions evaluate final normalized input, and retries cannot duplicate non-idempotent effects;
- extension point ordering and failure mode match the selected public semantic contract;
- deactivation prevents new acquisition while acquired plans drain; cleanup is idempotent, bounded, and race-safe;
- resume either proves an exact compatible plan or fails before durable mutation;
- Wasm candidates have no ambient authority and native candidates state their trusted-code boundary;
- deterministic credential-free tests prove the real public integration path, while mocks prove only their seam.

## Readiness states

- `Ready`: repository and dependency evidence plus current contracts are sufficient to plan the bounded extension outcome.
- `Bootstrap first`: repository or module foundation must land first or be included in the candidate.
- `Decision needed`: a user or product decision materially changes behavior or architecture.
- `Upstream request likely`: evidence suggests a required sibling contract is missing; confirm after selection before writing a request.
- `Blocked upstream`: an existing or newly written request must be resolved and verified before planning.
- `Discovery`: the outcome is clear, but focused research or contract validation must precede safe planning.
- `Planned`: an existing plan fully claims the outcome; show it as context, not a selectable duplicate.

Readiness is separate from candidate type. A request, ADR, discovery task, plan update, dependency bump, or copied comparator by itself is not a selectable milestone.

## Candidate contract

Offer 2–4 candidates with the recommendation first. Every selectable option must include:

- concise name;
- type, exactly `bootstrap repository` or `add extension`;
- primary consumer and observable host/agent outcome;
- exact current repository evidence or clearly labeled `proposed` insertion point;
- relevant Pi, davis7dotsh, and ava-silver comparison, preserving differences;
- current Eino owner, public API/pin, and whether an upstream request appears likely;
- readiness state;
- before/after extension-flow delta;
- native, Wasm, or explicitly undecided delivery and trust boundary;
- largest dependency or material decision;
- reason it is one coherent implementation plan;
- explicit scope and parity-claim guard;
- verification approach;
- one-sentence rank rationale.

Options must differ in consumer value, capability lane, or dependency trade-off, not merely package layout or implementation language. Do not put framework construction, static registration, UI polish, or generic refactors ahead of the nearest verifiable host-consumable journey unless that foundation is the only proven blocker and has its own observable acceptance path.

A working extension candidate must mount through the real public Eino composition/run-plan path, exercise its selected semantic capability, produce an observable bounded outcome, and demonstrate applicable cancellation, failure, and cleanup behavior. If durable state is affected, it must also prove resume/fingerprint behavior. A fake host or scripted model is acceptable for credential-free determinism only when it drives the real public Eino contracts; it proves no live-provider behavior.

## Ranking

Use qualitative evidence in this order:

1. Produces the nearest complete, repeatable consumer journey or removes its only hard blocker.
2. Fits current public Eino contracts and establishes the correct ownership boundary.
3. Exercises a real mount → frozen plan → behavior → observable outcome → cleanup path.
4. Can be verified locally without credentials or uncontrolled external effects.
5. Preserves provenance, scope, cancellation, bounds, redaction, deterministic ordering, and resume safety.
6. Establishes a reusable pattern for later extensions without prematurely building a broad framework.
7. Avoids Pi parity claims, literal TypeScript ports, TUI ownership leakage, and upstream duplication.

Avoid unexplained numeric scoring. Preserve a user's different choice and its trade-off.

## Choice and error paths

- Pause after displaying candidates and require explicit selection.
- Move fully planned work outside the candidate list.
- If every candidate needs the same repository foundation, include it in each coherent vertical slice or offer a bootstrap candidate only when it has an observable consumer acceptance journey.
- If a candidate may need upstream work, label that risk; do not write the request until the candidate is selected and the missing contract is verified.
- If a demonstration uses a mocked registry, fake runtime, scripted model, or static events, name the seam and prohibit claims beyond what it exercises.
- If the two comparator suites solve a problem differently, preserve the trade-off instead of averaging their designs.
- If Pi exposes a presentation or provider surface that `eino-agent` deliberately assigns to a host or native adapter, do not manufacture a generic extension seam solely for parity.
- If the request spans the entire extension ecosystem, require one consumer journey; return a scope blocker if the user declines.

## Material follow-up decisions

Ask 1–3 short questions only when answers change architecture or observable behavior. Examples include:

- native Go, Wasm component, or both as separately bounded deliverables;
- target host and minimum public `eino-agent` version/pin;
- global versus exact-session scope and configuration identity;
- external process/network/filesystem capabilities and supported platforms;
- credential-free first milestone versus optional configured service integration;
- persistence, artifact retention, redaction, and observability expectations;
- compatibility policy for public package APIs and saved extension-plan identity;
- whether a missing upstream capability should block the chosen milestone or trigger reselection.

Do not silently choose capability grants, secret handling, persistence semantics, supported platforms, or compatibility policy. An unanswered material decision blocks the brief with owner and exact unblock action.

## Post-selection architecture pass

Answer or explicitly defer each item in the resolved brief:

1. Primary consumer and in-scope extension journey.
2. Functional and non-functional success requirements.
3. Supported hosts/platforms and bounded performance/resource expectations.
4. Current and proposed component ownership and public contracts.
5. Current and before/after construction, mount, run, and cleanup flow.
6. Component/artifact identity, scope, registration order, configuration, and capability grants.
7. The component requiring a deep dive in this milestone.
8. Cancellation, timeout, backpressure, resume, teardown, security/redaction, supply-chain, and compatibility risks.
9. Tests and measurements proving the bounded outcome without claiming Pi or comparator parity.

Do not invent performance or capacity numbers. Ask only when a number changes the design; otherwise require a configurable or measured bound.
