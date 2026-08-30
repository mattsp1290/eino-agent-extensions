# Repository and Current-Research Playbook

Use this reference before generating milestone candidates. The output is an evidence set, not a roadmap.

## Repository survey

Resolve the `eino-agent-extensions` repository root and inspect only evidence that affects the frontier:

1. Read every applicable `AGENTS.md`, root or component README, contributor guide, architecture decision, and product note.
2. Record `git status --short --branch`, the current revision, recent commit metadata, relevant branch names, and path lists. Open targeted diffs only after confirming relevance.
3. Survey `.agents/plans/`, `.agents/skills/`, `.agents/requests/`, `~/.agents/projects/eino-agent-extensions/{requests,responses}/`, and tracker state when present.
4. Trace module manifests, public constructors and types, extension installers/loaders, composition registrations, examples, fixtures, tests, CI, and release/install paths.
5. Run or identify quality commands proportionally to repository maturity. Record existing unrelated failures separately.

Never read `.env*` values, credential stores, auth exports, private keys, shell history, raw session transcripts, or other known secret-bearing paths. Learn configuration from public types, documentation, and variable names. Inspect Git history through metadata and safe path lists before opening diffs.

Classify repository capabilities as:

- `implemented`: an executable public path plus a meaningful test or observed behavior proves it;
- `partial`: real code exists, but a required host, lifecycle, or architecture seam is missing;
- `planned`: a repository plan claims the outcome, but runtime evidence does not;
- `absent`: grounded search finds no relevant path or dependency;
- `blocked`: a named decision, request, contract, or external gate prevents safe planning;
- `unknown`: evidence is insufficient.

`implemented` and `partial` require a path plus a symbol, command, test, or observed behavior. A README claim, directory name, dependency entry, copied comparator shape, or registration-only mock is insufficient.

### Local Eino ecosystem

Enumerate every unique resolved `~/git/eino-*` Git root rather than relying on a fixed sibling list. Include `eino-agent`, exclude the current repository, and deduplicate aliases or nested matches. Shallow-inventory each repository before deciding relevance:

1. Resolve its Git root and read applicable `AGENTS.md` and contributor guidance.
2. Record revision and worktree status without modifying or cleaning it.
3. Record module/repository identity, top-level public package inventory, README or consumer guidance, and matching project request/response inventory.
4. Deep-inspect public packages, examples, contract tests, accepted plans, and release/tag state only for repositories connected to affected capability lanes.
5. Distinguish committed public contracts from uncommitted work, proposals, examples, and application-owned behavior.
6. Record module versions, tags, or commit pins this repository could consume. Do not insert local dependency replacements as a planning shortcut.

Start with this ownership hypothesis, then correct it from current evidence:

| Concern | Likely owner |
| --- | --- |
| Reusable native or Wasm extension packages, installers, adapters, fixtures, and consumer examples | `eino-agent-extensions` |
| Typed extension points, composition registry, frozen run plans, durable extension identity, orchestration, permissions, sessions, and resume | `eino-agent` |
| Reusable coding leaf tools and their safety contracts | `eino-tools` |
| Provider/model construction and provider-specific behavior | `eino-providers` |
| AG-UI conversion, emission, stream tapping, and client-tool binding | `eino-agui` |
| Agent/model/tool observability contracts and exporters | `eino-obs` |
| TUI widgets, keymaps, commands, themes, HTTP routes, auth, credentials, and deployment policy | consuming application |

Do not force a capability into the hypothesized owner when current public contracts show otherwise.

### Existing-plan and request classification

Classify each plan before it affects the frontier:

- `Product/library`: executable extension, integration, public contract, packaging, or consumer-facing foundation; relevant work can establish `planned` state.
- `Development-system/meta`: skills, review loops, agent infrastructure, or developer workflow; constrains execution but does not establish extension capability.
- `Mixed`: classify each package and use only product/library parts.

Exclude this skill's own creation from product evidence. Show fully planned outcomes outside the selectable list. A request is a dependency record, not proof that its ask was accepted or implemented. A response is decision evidence; verify any claimed implementation and pin in the target repository before clearing the blocker.

## Mandatory current research

Browse on every run that reaches candidate selection. An unresolved-request resume check may stop after verifying the request, response, and target repository; once cleared, refresh this full set. Resolve the current default branch and revision before trusting seed paths, and search for moved files, replacements, releases, and deprecations. For every material source, record title, publisher/repository, direct URL, revision or update date when available, and access date.

| Lane | Required coverage | Seed authorities |
| --- | --- | --- |
| Pi extension contract | Current Extension API, registration and lifecycle events, tools, commands, shortcuts, renderers/UI availability, session persistence, provider/model seams, cancellation, loading/discovery, examples, and tests | Official Pi repository and docs at `https://github.com/earendil-works/pi`; locate current extension documentation and types at the inspected revision |
| davis7dotsh suite | Current extension inventory; focused tools; background terminals; subagent and workflow lifecycle; result delivery; summaries; search; shared utilities; UI coupling; test and packaging patterns | `https://github.com/davis7dotsh/my-pi-setup/tree/main/extensions` and its repository guidance |
| ava-silver suite | Current extension inventory and standards; lifecycle wiring; shared typed event contracts; trust checks; cancellation/timeouts; output bounds; secret handling; testing; dependency and release organization | `https://github.com/ava-silver/pi-config/tree/main/extensions`, including current extension guidance, README, manifest, source, and tests |
| Eino runtime contract | Current `eino-agent` extension, composition, runtime, session, permission, Wasm, security, consumer, and example contracts; public versions and compatibility | Local committed `~/git/eino-agent` is consumer authority; use its official remote, docs, releases, and dependency source when external confirmation matters |
| Candidate dependencies | Any Eino-family module, Go library, Wasm component contract, protocol, tool, external executable, API, or service named in an option | Current official project documentation, source, releases, and security guidance |

The two comparator directories and the official Pi contract are mandatory. `eino-agent` is mandatory local evidence; browse its official upstream when a remote revision, release, or volatile claim affects a candidate. A source path here is a discovery seed, not a permanent contract.

If mandatory web access or an authoritative lane is unavailable, label affected claims `unverified-current`, list the missing lane, and stop before producing a plan-ready brief. A user-approved repo-only comparison remains exploratory.

## Comparator analysis

Build a feature inventory from observable behavior and public extension APIs. For each relevant comparator extension, record:

- user or host outcome;
- Pi surfaces used: lifecycle event, tool, command, shortcut, renderer, UI, session entry, provider hook, or event bus;
- state and resource lifetime;
- cancellation, timeout, output bounding, trust, and secret behavior;
- test strategy and external dependencies;
- whether current `eino-agent` has an equivalent supported seam;
- what must intentionally remain host-owned or unsupported.

The comparator suites overlap. Treat duplicated concepts as evidence of demand or alternative design, not as two roadmap items. Preserve meaningful differences such as in-process versus subprocess work, TUI-coupled versus headless behavior, monolithic versus shared-module organization, and conservative versus broad capability access.

## Evidence hierarchy and clean-room boundary

Rank evidence by implementation authority:

1. Current `eino-agent-extensions` code, tests, contracts, and accepted decisions for repository behavior.
2. Committed public contracts and verified pins in local Eino-family repositories.
3. Official Eino, Go, Wasm, library, protocol, and service source/documentation for implementable APIs.
4. Official Pi source/documentation and the two named public comparator suites for behavior and architecture lessons.

Do not copy comparator implementations, prompts, private endpoints, internal schemas, or undocumented internals. Check licenses before adapting any code. Prefer behavioral clean-room comparison: describe the outcome and generic flow, then design independently against Go and local public Eino contracts.

Label every material statement:

- `Repo fact`: verified repository path, symbol, command, test, or behavior.
- `Local dependency fact`: verified sibling public contract, revision, path, or test.
- `External fact`: current opened official source with direct URL and access date.
- `Inference`: reasoning from labeled facts.
- `Proposal`: a candidate design rather than existing behavior.
- `User decision`: selection or follow-up answer.
