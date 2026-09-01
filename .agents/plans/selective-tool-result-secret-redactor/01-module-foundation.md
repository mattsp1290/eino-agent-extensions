# 01 — Module foundation

## Goal and prerequisites

Create the smallest independently consumable Go library foundation for the selected extension. This work starts from repository revision `80684c8ba17ede18b4ad6aac716dbe4398f02b82` and must not depend on a sibling checkout.

## Existing evidence

- Existing `README.md` contains only the repository title and one-line description.
- No `go.mod`, `go.sum`, Go package, test command, CI workflow, release automation, or contributor guide exists.
- The upstream external-consumer fixture independently verified `eino-agent v0.1.3` with Go 1.26.3 and no consumer replacement.
- Root `LICENSE` already supplies the repository license and must remain unchanged.

## Change surface

| Path | State | Planned purpose |
| --- | --- | --- |
| `go.mod` | **new**, parent is repository root | Declare module `github.com/mattsp1290/eino-agent-extensions`, Go 1.26.3, and direct `github.com/mattsp1290/eino-agent v0.1.3` dependency |
| `go.sum` | **new**, generated from `go mod tidy` | Pin the complete verified dependency graph |
| `.github/workflows/ci.yml` | **new**, parent `.github/` is proposed under repository root | Run deterministic module, vet, test, and race gates on Linux with the `go.mod` toolchain |
| `README.md` | existing | Replace the placeholder description with scope, install/import guidance, minimal mount example, security limits, and verification commands |
| `toolresultredactor/doc.go` | **new**, parent package directory proposed under repository root | Define the package boundary and security claim in Go documentation |

No implementation package other than `toolresultredactor` is introduced in this milestone. Do not create a generic extension framework, shared matcher package, CLI, or internal plugin loader.

## Intended behavior and invariants

### Module contract

- Use the exact root module path `github.com/mattsp1290/eino-agent-extensions`.
- Set the Go language/toolchain requirement consistently with the verified dependency contract: Go 1.26.3.
- Require `github.com/mattsp1290/eino-agent v0.1.3` directly.
- Do not add `replace`, `exclude`, `retract`, vendor state, or a committed `go.work`.
- Keep the first dependency set to Go standard library plus Eino Agent and the transitive graph selected by `go mod tidy`. A secret-scanning library requires a separate decision and is out of scope.

### Repository checks

The CI workflow should use pinned major versions of standard GitHub actions and run these gates as separate named steps:

1. `go mod tidy` followed by `git diff --exit-code -- go.mod go.sum`;
2. `go mod verify`;
3. `go vet ./...`;
4. `go test ./...`;
5. `go test -race ./...`.

Use the version from `go.mod`; do not duplicate a divergent Go version in several files. The implementation PR should record the exact action revisions chosen, but release signing and supply-chain attestation remain deferred.

### Compatibility and rollout

There are no active consumers and no backward-compatibility requirement. This foundation defines the first API. Do not add feature flags, deprecated aliases, migration code, or version-detection branches.

## Error paths and edge cases

- If `v0.1.3` no longer resolves through the configured module proxy, stop rather than add a local replacement.
- If Go 1.26.3 is unavailable in CI, report the toolchain provisioning failure; do not lower the directive without re-verifying the upstream module.
- Keep generated dependency changes confined to `go.mod` and `go.sum`. Unrelated repository drift is not part of this plan.

## Verification and acceptance

Run from the repository root:

```text
GOWORK=off go mod tidy
GOWORK=off go list -m all
GOWORK=off go mod verify
GOWORK=off go vet ./...
GOWORK=off go test ./...
GOWORK=off go test -race ./...
```

Acceptance requires:

- `go list -m all` selects `github.com/mattsp1290/eino-agent v0.1.3` and `github.com/mattsp1290/eino-agent/wasmext/gen v0.1.0` with no replacement;
- a black-box test package can import `toolresultredactor` through the module path;
- CI runs the same public gates from a clean checkout; and
- README language does not claim a release, complete secret detection, external scanning, or Pi parity.

## Dependencies and exclusions

This work package precedes every package and integration file. It may land in the same coherent implementation change, but all later work assumes the exact dependency pin. Publishing tags, GoReleaser, SBOM generation, multi-platform matrices, and compatibility promises are deferred.
