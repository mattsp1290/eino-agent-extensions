# Upstream Request and Blocking Protocol

Use this reference after the user selects a milestone whose required public contract appears to belong in another local repository, and on later invocations that must check whether such a request still blocks progress.

## Outstanding-request resume gate

Before ordinary frontier discovery on every invocation:

1. Search `~/.agents/projects/*/requests/` for Markdown metadata fields named `Blocker consumer` whose scalar value is `eino-agent-extensions`.
2. Match each request to the same-named file under that project's `responses/` when present.
3. Require exactly one scalar `Request status` with a valid value. A missing, repeated, structured, or unknown value is a blocker until the durable record is repaired. Gate on `open`. A request marked `resolved` still requires a matching response and verification of its referenced committed code and consumable pin; an invalid resolved claim reopens the blocker. `withdrawn` and `superseded` do not block but retain their audit metadata.
4. If an open request declares a `Request set`, verify that every existing member agrees on consumer, selected milestone, set ID, and complete member list. If a declared member is missing because an earlier multi-request write stopped partway, re-run the whole set's identity, ownership, deduplication, and safe-path preflight, create only the missing members with the already recorded filenames, then continue this gate. Do not refresh candidates or add a different dependency to the recorded set. Treat inconsistent or unsafe set metadata as a blocker requiring repair.
5. If a response explicitly declines the ask or assigns ownership elsewhere, report the verified response and ask the user whether to withdraw the milestone or supersede it with a named replacement. Stop until the user chooses and the request transition is safely recorded; a decline never authorizes a local workaround or silent reselection.
6. If one or more requests are unresolved, list all of them with request path, target, selected milestone, request set when present, status, and exact unblock condition, then stop. Do not silently choose one or generate replacement candidates.
7. Proceed only when all matching requests are verified resolved or the user explicitly withdraws or supersedes the blocked milestone and that transition is recorded in each affected request.

After a successful gate, refresh normal repository and external research before making new frontier claims.

The only valid `Request status` values are `open`, `withdrawn`, `superseded`, and `resolved`.

## Request threshold

Create or reuse one request per distinct target-owned public contract only when all are true:

1. The selected `eino-agent-extensions` outcome depends on the capability for its bounded acceptance journey.
2. Current target code, tests, plans, requests, and responses do not provide a usable verified public contract and pin.
3. Implementing it here would duplicate upstream ownership, require private/internal imports, weaken safety or durability, contradict an intentional boundary, or create a speculative compatibility surface.
4. The target repository and requested contract can be named precisely enough for its maintainer to decide or implement.

Do not create a request for optional polish, hypothetical Pi parity, a broad roadmap, a dependency already available through a public API, or behavior this repository or the consuming host safely owns. A request is not a substitute for an ordinary local design decision.

## Preflight and deduplication

Complete the selected milestone's dependency and ownership map before writing requests. Group missing contracts by verified target repository; never bundle asks owned by different repositories into one file. Before any write, assign the complete blocker set one stable `YYYY-MM-DD-<selected-milestone-slug>` request-set ID and a complete list of `<target-repo>/<request-filename>` members.

Preflight the entire request set before writing any member:

1. Resolve the target repository under `~/git`, verify its checkout basename and declared module/repository identity, and read applicable `AGENTS.md` and contributor guidance.
2. Record `git rev-parse HEAD` and `git status --short --branch`. Preserve all target worktree changes. Treat uncommitted code as provisional, not a stable consumer contract.
3. Inspect relevant public code, docs, tests, accepted plans, and release/tag state.
4. Search both `~/.agents/projects/<target-repo>/requests/` and `responses/` for the same consumer need, symbols, and acceptance outcome.
5. If an equivalent request with `Blocker consumer: eino-agent-extensions` exists, verify its status. Reuse an `open` request as an already active blocker. Reuse `resolved` only after re-verifying its response, committed implementation, and pin; it then needs no new blocker. A `withdrawn` or `superseded` record is historical: create a new linked request, or reopen the old one only with explicit user authorization and a safely written `open` status-history entry. If an equivalent request belongs to another consumer, cite it as related evidence but create one linked `eino-agent-extensions` blocker so status and resume behavior remain explicit.
6. Validate every target identity, destination, proposed body, existing equivalent, and set member before the first exclusive create. Each new member must record the identical request-set ID and complete member list. An already open equivalent may remain a linked reused blocker without being rewritten; list its existing path as a member in every new set record.

After the full preflight succeeds, create new members in deterministic target/path order with exclusive adds. If a later create loses a race or fails, stop and report the partial set. On the next invocation, the resume gate may create only the recorded missing members after re-running the complete preflight; it must not generate a new set or leave the first open request as an incomplete blocker record.

Use the verified target checkout basename for `<target-repo>`. If the basename and declared module/repository identity disagree, stop and ask the user which project record is authoritative rather than guessing.

## Safe request path

Write one Markdown file at:

```text
~/.agents/projects/<target-repo>/requests/YYYY-MM-DD-<short-kebab-slug>.md
```

After target identity is verified, resolve the canonical projects root and derive the one expected project directory from the verified checkout basename. Reject a symlink or non-directory at any existing project or requests path component. Create the missing project and `requests/` directories when needed, then resolve the canonical project directory, requests directory, and destination. Require the project to remain a direct child of the canonical projects root and the destination to remain a direct child of the canonical requests directory. Reject symlinked path components, traversal, nested slugs, absolute slugs, or any escape. Recheck immediately before writing and use an exclusive create operation that fails if the destination already exists. Never overwrite an existing request. Use the user's local date, and name the consumer-visible contract rather than a proposed implementation.

## Request contents

Include:

```markdown
# Request: <consumer-visible contract>

- **Requested by:** `eino-agent-extensions` next-milestone selection
- **Blocker consumer:** `eino-agent-extensions`
- **Request status:** `open`
- **Date:** YYYY-MM-DD
- **Priority:** <why and whether it hard-blocks the selected milestone>
- **Selected milestone:** <blocked milestone name>
- **Request set:** <YYYY-MM-DD-selected-milestone-slug>
- **Request set members:** <complete comma-separated target-repo/request-filename list>
- **Target repo:** <module/repository and local checkout>
- **Pinned commit under evaluation:** <full SHA>
- **Consumer:** `eino-agent-extensions`

## Background
<Selected consumer journey, current repository evidence, verified owner boundary, and exact missing seam.>

## Ask
<Required behavior and smallest acceptable public contract. Describe outcomes first; label any API shape proposed unless the target already establishes it.>

## Out of scope
<Prevent the target from absorbing extension implementation, consumer presentation, unrelated runtime work, or broader Pi parity.>

## Acceptance
<Public API/behavior, target tests and quality gates, documentation, compatibility statement, and a tag or commit this repository can pin.>

## Response and unblock contract
- Write the decision or completion record under `~/.agents/projects/<target-repo>/responses/` using the same filename.
- The selected milestone remains blocked until the response identifies a usable contract and the referenced committed implementation and pin are verified in the target repository.
- If declined, explain the owning boundary or supported alternative so `eino-agent-extensions` can re-scope.

## References
<Exact consumer and target paths/symbols plus current official sources that constrain the ask. Do not include secrets, raw prompts/session content, or tool payload values.>

## Status history
- YYYY-MM-DD — `open`: created for <selected milestone>.
```

Keep the request implementation-neutral unless a public shape is necessary for compatibility. Do not ask upstream to implement this repository's extension package, comparator behavior, host UI, routing, auth, or deployment policy.

## Mandatory stop and user notice

After creating or reusing every unresolved request required by the selected milestone:

1. Mark the selected milestone `blocked upstream`.
2. Do not create or continue an implementation plan, implementation code, fallback duplicate, vendored fork, private-import adapter, or temporary local replacement.
3. Tell the user prominently:
   - selected milestone;
   - each target repository;
   - every clickable absolute request path;
   - why each request blocks the observable journey;
   - exact response and implementation evidence that clears it;
   - whether each request was newly written or reused.
4. End the run. Do not leave a blocker only under “open questions.”

Before any status or history mutation, repeat the canonical direct-child and no-symlink validation for the projects root, project directory, requests directory, request file, and matching response when present. Re-verify the request's consumer, unique status field, and expected set identity. Snapshot the content immediately before editing, preserve every unrelated byte, and apply only the intended status and history change. Abort rather than overwrite if the content changed concurrently or if any path identity drifted.

On explicit withdrawal or supersession, update only this consumer's request status and append a dated status-history entry with the reason and replacement milestone/request when applicable. Apply the transition consistently to every open member of the same selected-milestone request set; a linked reused blocker from another set changes only when the user explicitly includes it. Never delete a request. On verified completion, set it to `resolved` and append the response path, verified commit/tag, and verification date. Do not alter another consumer's request.

## Resuming later

On the next invocation:

1. Re-open the request, matching response, and target repository.
2. Verify any accepted API, tests, version/tag/commit, and compatibility claims against committed target code.
3. If the response declines or changes ownership, follow the resume gate's explicit user withdrawal/supersession transition and stop until it is recorded.
4. If unresolved, report the same blocker and stop without duplicating it.
5. If implemented and consumable, record the verified pin as `Local dependency fact`, clear the blocker, refresh external research and this repository's frontier, and continue.
