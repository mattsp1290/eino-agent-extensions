# Resolved Milestone Brief and Planning Handoff

Use this reference only after the user selects a candidate and no upstream request blocks the milestone. Material decisions should be resolved; if the user explicitly declines or cannot resolve one, record a decision-blocked brief instead. The brief remains conversation context and is not a separate repository artifact.

## Brief schema

Populate every field. Use `None` only when evidence proves the field irrelevant. Preserve uncertainty.

```markdown
# Resolved milestone brief

## Selection
- Selected milestone:
- Planning status: ready | blocked
- Suggested kebab-case plan name:
- Primary consumer and observable host/agent outcome:
- Milestone type: bootstrap repository | add extension
- Selection rationale or accepted non-recommended trade-off:

## Scope
- In-scope extension journey:
- Out of scope:
- Supported host/platform boundary:
- Native/Wasm delivery and trust boundary:
- Explicit Pi/comparator parity-claim boundary:

## Evidence
- Eino-agent-extensions facts and exact paths/symbols/tests:
- Existing-plan or tracker relationship:
- Local dependency facts, revisions, public contracts, and pins:
- External facts, direct URLs, revisions/dates, publishers, and access dates:
- Inferences:
- Proposals:

## UX and architecture
- Relevant capability gaps:
- Pi and comparator lessons and intentional differences:
- Current construction/mount/run/cleanup flow:
- Candidate before/after flow:
- Proposed ownership, packages, public API, configuration, and host integration:
- Component/artifact identity, scope, ordering, provenance, and capability grants:

## Requirements and execution
- Functional requirements:
- Non-functional requirements:
- Dependencies and execution order:
- Deep-dive component:

## Gates
- Cancellation, deadline, timeout, backpressure, and retry behavior:
- Durable plan, fingerprint, resume, and compatibility behavior:
- Deactivation, quiescence, cleanup, and failure behavior:
- Permission, trust, security, redaction, and secret behavior:
- Input/output/resource bounds and artifact retention:
- Supply-chain and external-dependency behavior:
- Migration, rollback, removal, or exit seam:

## Decisions and requests
- User decisions:
- Assumptions:
- Unresolved upstream requests: none
- Resolved request/response evidence and verified pin:
- Blocking open questions, owner, and exact unblock action:
- Non-blocking open questions:

## Verification
- Unit, integration, race, fuzz, Wasm, and host-example tests as applicable:
- Credential-free acceptance path:
- Optional configured-service smoke path:
- Observable acceptance criteria:
```

Planning status is `ready` only when no material architecture, user-visible, compatibility, security, or upstream request decision remains open. Preserve a user's non-recommended selection and its trade-off.

## Resume `$implementation-plan`

For a composed invocation, treat the resolved brief as the concrete requested change and resume the active `$implementation-plan` workflow:

1. Ask and record `$implementation-plan`'s required operating-context questions; this skill does not answer them by inference.
2. Derive the plan name only now, normalize it to one safe kebab-case segment, and create exactly one direct child under `.agents/plans/`.
3. Incorporate the brief into the normal overview and cohesive work files before review. Do not write the brief as an extra artifact.
4. Retain every standard `$implementation-plan` structure, grounding, review, revision, and delivery requirement.
5. Give reviewers only the context permitted by `$implementation-plan`; they inspect the incorporated brief through the plan directory. Do not attach this brief separately.

If the brief is decision-blocked, preserve that status through review, name the decision owner and exact unblock action, and do not call the plan implementation-ready.

The resulting plan must additionally contain:

- a capability-gap table limited to lanes affected by the selected milestone;
- current and before/after host → component/loader → registry mount → frozen run plan → selected semantic behavior → observable outcome → cleanup flow;
- relevant Pi and comparator behavior with direct citations, revision/date, and access date;
- verified compatible Eino-family versions and public contracts;
- clearly proposed extension-repository ownership, verified upstream ownership, and explicit host-owned behavior;
- component/artifact identity, scopes, ordering, configuration hashes, capability restrictions, side effects, cancellation, and concurrency boundaries;
- durable plan/fingerprint and resume behavior where relevant;
- bounds, redaction, trust, permission, credentials, path, network, subprocess, Wasm, and supply-chain decisions where relevant;
- relationships to planned-but-unimplemented foundations;
- the captured user choice and material decisions;
- bounded verification that does not imply Pi or comparator parity;
- for any working-extension claim, an acceptance path through real public composition and runtime contracts to an observable result and bounded cleanup, with applicable failure, cancellation, and resume coverage.

If any upstream request is unresolved, do not resume `$implementation-plan`; the upstream-request protocol's mandatory stop takes precedence.

## Standalone behavior

When `$next-milestone` runs without `$implementation-plan`, return the populated brief in conversation after selection. Do not create a plan. End with:

```text
Use $implementation-plan with this resolved milestone brief as the concrete request to create the repository's reviewed implementation plan.
```

Do not recommend re-running `$next-milestone` for a brief that is already resolved. If the user nevertheless invokes the composed form, refresh request and current-evidence gates but preserve the prior selection unless new evidence invalidates it.
