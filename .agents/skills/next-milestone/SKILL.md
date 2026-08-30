---
name: next-milestone
description: Choose the next eino-agent-extensions milestone by comparing the live repository and local Eino extension contracts with current Pi extension APIs and the davis7dotsh/my-pi-setup and ava-silver/pi-config extension sets; present bounded candidates for user selection; create and block on cross-repository requests when a required upstream contract is missing; and hand a resolved milestone to $implementation-plan. Use when asked what extension to build next in eino-agent-extensions or when invoked as $implementation-plan $next-milestone.
---

# Next Eino Agent Extension Milestone

Choose one bounded, host-consumable extension increment for this repository, then let `$implementation-plan` plan it. Re-evaluate the repository, the local Eino ecosystem, and the external references on every invocation; none is a frozen roadmap.

## Ownership and ordering

When invoked as `$implementation-plan $next-milestone`, preserve this order:

1. Let `$implementation-plan` resolve the repository root and applicable guidance, but do not let it name or create a plan directory yet.
2. Run this skill's outstanding-request resume gate. If it does not stop the run, survey the repository and local Eino contracts and research the current external references. For a fresh selection, compare the frontier, present candidates, and pause for the user's choice. If the current conversation already contains this skill's resolved brief, refresh its evidence and preserve its selection unless new facts invalidate it; do not force a duplicate choice checkpoint.
3. Resolve the selected milestone, material decisions, integration flow, and component ownership.
4. If the selected milestone needs a missing public contract from another local repository, follow the upstream-request protocol, report every blocking request, and stop. Do not create an eino-agent-extensions plan while any request remains unresolved.
5. Otherwise, build the resolved milestone brief and resume `$implementation-plan`, including its user-confirmed operating context, normal plan format, and required reviews.

`$next-milestone` owns milestone selection and upstream-request gating. `$implementation-plan` owns plan naming, plan files, plan review, revision, and delivery. Blocking requests are the only artifacts this skill may create. Never create an interim plan, `/big-change` prompt, Beads issue, implementation change, or second plan format.

When invoked alone, complete discovery, selection, decisions, and either the resolved brief or blocking request set. Do not write an implementation plan.

## Workflow

### 1. Establish the live frontier

Resolve the Git repository root and read applicable `AGENTS.md` and contributor guidance. Read [references/research-playbook.md](references/research-playbook.md), then inspect the current worktree, history, existing plans and requests, code, module and package boundaries, tests, examples, CI, release files, and quality state.

Before ordinary discovery, scan `~/.agents/projects/*/requests/` for records whose `Blocker consumer` is `eino-agent-extensions`. Evaluate every matching durable `Request status` using [references/upstream-request-protocol.md](references/upstream-request-protocol.md). If any request remains open or its claimed resolution cannot be verified, report every matching blocker and stop before refreshing candidates. Do not evade a blocker by selecting a replacement milestone unless the user explicitly withdraws or supersedes the blocked milestone and the request records that transition.

Build a shallow inventory of every unique resolved `~/git/eino-*` Git root, including `eino-agent` and excluding this repository. Deduplicate repeated paths, read each relevant repository's guidance, and deep-inspect only siblings connected to the affected capability lanes. Treat sibling checkouts as contract and ownership evidence, never as permission to modify them. Preserve unrelated and uncommitted work everywhere.

Distinguish executable, tested extension behavior from scaffolds, examples, plans, names, and generated-but-absent artifacts. Never read secret-bearing environment, credential, session, or prompt-content files.

### 2. Research current references

Browse on every unblocked candidate-selection invocation using the mandatory lanes in the research playbook.

- Current `eino-agent` public extension and composition contracts are the implementation authority this repository can consume.
- Pi's official extension API and source establish the upstream extension model and lifecycle being adapted.
- `davis7dotsh/my-pi-setup/extensions` is a comparator for focused tools, background work, subagents, workflows, summaries, search, and TUI integration.
- `ava-silver/pi-config/extensions` is a comparator for a larger maintained extension suite, shared contracts, lifecycle discipline, security controls, testing, and repository organization.

Record direct URLs, inspected revisions, and access dates. Treat comparator behavior as product and architecture evidence, not code to port or an API specification. Preserve license and clean-room boundaries.

If mandatory browsing or authoritative coverage is unavailable, finish the local survey, label volatile claims `unverified-current`, name the missing research lanes, and stop before declaring a candidate plan-ready. A user may accept a repo-only exploratory brief, but that does not make external claims current.

### 3. Compare and build candidates

Read [references/frontier-rubric.md](references/frontier-rubric.md). Build its capability matrix and end-to-end extension flow. Keep these evidence classes distinct: `Repo fact`, `Local dependency fact`, `External fact`, `Inference`, `Proposal`, and `User decision`.

Present 2–4 coherent candidates, recommendation first. Every candidate type must be exactly `bootstrap repository` or `add extension`; readiness is a separate state. Show fully planned outcomes outside the selectable list. Exclude duplicate, request-only, plan-maintenance, comparator-porting, and purely cosmetic options.

Candidates must differ in user or host value, capability lane, or dependency trade-off. Prefer thin, reusable extension journeys that mount through supported public contracts and prove observable runtime behavior. Do not claim Pi parity from a package skeleton, mock-only registration, copied UI behavior, or a partial lifecycle.

### 4. Pause for the user's choice

For every fresh selection, show the 2–4 options and stop before writing a request or plan. This checkpoint applies even if the initial request says “pick for me.” Use structured user input when available; otherwise ask one concise plain-text question with numbered options. A resolved brief already present in the current conversation may resume after refreshed evidence without repeating the options unless new facts invalidate its selection.

Only after the options are visible may the user delegate the choice. Preserve a non-recommended selection and explain its trade-off without overriding it.

### 5. Resolve decisions and ownership

After selection, ask 1–3 short questions per interaction only when answers materially change observable behavior, public API, native-versus-Wasm delivery, host integration, compatibility, persistence, security, capability access, configuration, platform support, or scope. Continue focused rounds while independent material decisions remain.

If the user explicitly declines or cannot resolve a material decision, keep the selected milestone and mark its brief `blocked`, naming the decision owner and exact unblock action. Do not invent an answer. This decision-blocked brief is distinct from an upstream-request blocker.

Trace every required capability to a verified owner. The expected direction is that this repository owns reusable extension components and adapters, while `eino-agent` owns runtime extension points, composition, durable run behavior, and core policy; other Eino siblings retain their established provider, tool, protocol, and observability contracts. Verify that division against current code rather than treating it as permanent. Host application presentation, routing, authentication, credential storage, and deployment policy remain outside a reusable extension unless current public contracts establish otherwise.

If a required public contract is absent or ambiguous upstream, read [references/upstream-request-protocol.md](references/upstream-request-protocol.md). Complete the ownership and dependency map, then create or reuse one blocker request per distinct target-owned contract. Mark the selected milestone `blocked upstream`, tell the user every request path and exact unblock condition, and stop. Never bundle asks for different repositories, bypass a missing seam with private imports, duplicate upstream behavior locally, or present a speculative adapter as completion.

### 6. Hand off to planning

When no upstream request blocks the milestone, read [references/implementation-plan-handoff.md](references/implementation-plan-handoff.md) and build the complete milestone brief in conversation context. Mark it `ready` only when material decisions are resolved; otherwise produce the explicitly decision-blocked brief described above.

For a composed run, incorporate the brief into the normal `$implementation-plan` files before its reviewers run. Do not add the brief as a separate repository artifact or extra reviewer prompt context. Derive the kebab-case plan name only after selection, and create exactly one selected-milestone plan.

## Invariants

- Require exact repository evidence for `implemented` and `partial`; plans prove only `planned`.
- Adapt observable behavior and architecture lessons from Pi and the two comparator suites; do not translate their TypeScript modules, internal schemas, prompts, or private boundaries literally into Go.
- Use only current committed public Eino-family contracts. A missing upstream seam is a request/blocker, not authorization to modify a sibling repository.
- Any candidate described as a working extension must prove host construction or loading → atomic mount → frozen per-run plan → supported callback/tool/prompt/guard/restriction or event behavior → observable outcome → quiescent cleanup. Exercise cancellation, failure, and resume/fingerprint behavior when the affected contract requires them.
- Keep runtime extension work separate from host-owned TUI commands, shortcuts, widgets, themes, routes, auth, credentials, storage policy, and deployment. A host-facing adapter may expose an explicit seam without claiming ownership of the host behavior.
- Treat component identity, scope, ordering, provenance, configuration hashing, capability restriction, bounded payloads, cancellation, teardown, and secret redaction as first-class for affected candidates.
- Never expose credentials, live or secret-bearing prompt/session contents, raw reasoning, tool payload values, private repository data, or raw provider errors through research notes, request files, or plans. Public contract shapes, synthetic fixtures, paths, and redaction-policy descriptions are allowed when needed.
- Narrow “port all Pi extensions” or “build the extension ecosystem” requests to one reusable extension journey; return a scope blocker if the user declines.
