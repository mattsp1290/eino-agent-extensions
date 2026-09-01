# 03 — Eino runtime integration

## Goal and prerequisite state

Mount the compiled redactor as one native Eino component through committed public `v0.1.3` contracts. Preserve upstream atomic registration, protected tool identity, frozen run plans, exact resume, and quiescent shutdown.

Prerequisites: [01-module-foundation.md](01-module-foundation.md) and [02-redaction-engine.md](02-redaction-engine.md).

## Existing upstream contract

At `eino-agent v0.1.3`:

- `composition.Registry.Mount(ctx, component, installer)` atomically stages callbacks and component capabilities.
- `composition.Registrar.Extensions()` exposes the typed callback registrar.
- `extension.OnTransform(registrar, runtime.ToolResultTransformPoint, registration, fn)` registers the ordered waterfall callback.
- `runtime.ToolResultTransform` exposes protected `ToolName`/`Call` plus mutable `Result` data without tool executors or approval capabilities.
- transform validation rejects mutation of `ToolName` or `Call` but permits a valid `ToolResult` replacement.
- the orchestrator applies transforms before `BuildToolSettlement`, then commits the terminal call, result message, result part, and event atomically.
- component artifact/config and registration scope/order enter the frozen descriptor and resume fingerprint.

Do not import `internal` packages, duplicate the registry, call unexported dispatch helpers, or add a local replacement for Eino Agent.

## Proposed change surface

| Path/symbol | State | Responsibility |
| --- | --- | --- |
| `toolresultredactor/mount.go` | **new** | Export `Mount`, validate/canonicalize options, bind component identity, and register the transform |
| `toolresultredactor/mount_test.go` | **new** | Test public mount validation, atomic failure, scope, order, config identity, and callback registration |
| `toolresultredactor/integration_test.go` | **new** external test package | Drive real composition, run-plan, orchestrator, SQLite settlement, resume, and teardown paths |

### Proposed mount API

```text
func Mount(
    ctx context.Context,
    registry *composition.Registry,
    component extension.Component,
    options Options,
) (*composition.Mount, error)
```

This is a proposed API. The host supplies `InstanceID` and artifact `Name`, `Version`, and `Hash`. `Mount` must:

1. require a non-nil registry and valid native artifact identity;
2. reject `SourceWasm` because this is trusted native code;
3. canonicalize `Options` and compile the policy before calling the registry;
4. derive `ConfigHash` from the canonical policy;
5. set an empty `component.Artifact.ConfigHash` to that value, or reject a non-empty mismatched value;
6. call `registry.Mount` with a `composition.InstallerFunc`;
7. register exactly one `extension.OnTransform` using `registrar.Extensions()`, stable registration ID `tool-result/secret-redactor`, the canonical scope, and canonical order; and
8. return the upstream `*composition.Mount` without wrapping or weakening its lifecycle.

Expose a read-only `ConfigHash(options)` helper so a host can prepare and audit component identity before mount. It must execute the same canonicalization code as `Mount`.

## Transform behavior

The callback receives a defensive `runtime.ToolResultTransform` value.

```text
if exactExcluded(input.ToolName):
    return input unchanged

input.Result = compiledPolicy.Redact(ctx, input.Result)
return input, nil
```

Never change `ToolName`, `Call`, permission state, disposition, error classification, retention policy, tool metadata keys, or run/session identity. The callback performs no network, filesystem, subprocess, model, store, clock, or observer work.

After upstream output validation succeeds, the redactor runs for successful, denied, approval-required, interrupted, and failed outcomes because upstream invokes the result transform after outcome construction. Non-empty syntactically invalid `Structured` JSON fails validation before the waterfall, so the redactor does not run and makes no sibling-field guarantee. Built-ins should not alter upstream permission metadata unless one of its keys or values independently matches a secret rule; a matching key invokes the documented parent-map fallback.

## Scope and ordering

- Zero scope means global, matching “inspect all callback-admitted tool results by default.”
- Hosts may explicitly select one `extension.SessionScope` for a session-specific policy.
- Zero order maps to a documented late, fixed, cross-platform integer constant. It must execute after ordinary `runtime.OrderApplication` transforms in the example and tests.
- A non-zero host order is allowed and becomes durable registration identity.
- Documentation must say that Eino's transform point is a waterfall, not an enforced terminal hook. Hosts requiring the full-result settlement guarantee must keep this redactor last among all `ToolResultTransformPoint` callbacks.
- A failing earlier transform prevents this callback from running, while a failing later transform makes Eino v0.1.3 restore the original pre-waterfall result. In either case, durable/model-visible settlement becomes a generic failure, but trusted full-result observers can receive the original result. Hosts requiring sanitized full-result notices must ensure other native transforms return sanitized success.
- Add ordering tests where an earlier transform inserts a synthetic secret and the redactor removes it, and where an explicitly later failing transform proves the original-result observer limitation.

## Component identity and resume

Identity rules:

- `InstanceID` distinguishes separately configured mounts.
- artifact `Name`, `Version`, `Hash`, and `SourceNative` describe the implementation supplied by the host/build.
- derived `ConfigHash` describes built-in catalog version, placeholder version, canonical exclusions, host rules, and resource limits.
- registration ID, scope, and order are recorded by upstream independently.

Resume behavior:

- an identical mounted component and canonical policy must reproduce the persisted descriptor and resume pending tool work;
- reordered equivalent exclusions/rules must preserve the fingerprint;
- changed rules, exclusions, limits, scope, order, artifact identity, or built-in catalog version must produce `runtime.ErrExtensionPlanMismatch` before a run or tool changes state;
- running calls found during resume remain upstream-owned and are never re-executed; and
- pending calls re-execute under the exact resumed plan and pass their result through the redactor before settlement.

No secret value may enter component identity, handler identity, configuration hashes, request records, or diagnostics.

## Cancellation, failure, and concurrency

- Policy compilation happens before the atomic mount and may return bounded configuration errors.
- The transform is immutable and safe for simultaneous tool completions. Do not serialize unrelated results.
- The callback checks cancellation between fields and structured nodes. On cancellation, it placeholderizes all unproven fields and returns success, as specified in the engine plan.
- A runtime panic is contained inside the package boundary and yields a fully placeholderized result without exposing the panic or raw result.
- The extension owns no goroutine, queue, file, process, network client, cache, or cleanup callback.
- Upstream `RunPlan.Release`, `Mount.Deactivate`, and `Mount.Close` remain the only lifecycle mechanisms.
- Package budgets do not bound raw tool-result allocation or upstream's defensive clone before callback invocation. Host tools must bound result construction.

## Deactivation and close acceptance

Test the public lifecycle:

1. mount the component and acquire a run plan that contains its handler identity;
2. deactivate the mount;
3. acquire a new plan and prove it does not select the handler;
4. attempt a bounded close while the old plan is held and prove it does not report successful cleanup;
5. release the old plan; and
6. close successfully and idempotently without a package-owned resource leak.

Also test failed configuration and failed registration leave the registry empty and allow reusing the component identity on a valid mount.

## Compatibility, rollback, and removal

There is no existing API or stored local configuration to migrate. Rollback/removal is host-controlled:

- deactivate and close the mount to stop selection after acquired plans drain;
- remove the mount call and policy from host composition;
- use the prior module version/artifact identity for new sessions if a future release regresses; and
- expect exact resume mismatch for durable runs whose persisted component is no longer mounted. A host must drain or finish those runs before removal; the extension must not bypass that upstream safety rule.

No feature flag is added.

## Verification and acceptance

- Public mount succeeds with a valid native component and canonical options.
- Invalid limits, pattern, scope, artifact, or config-hash mismatch fails before any handler becomes visible.
- The frozen descriptor contains one component/handler with the expected artifact, config hash, transform contract, scope, and order.
- The transform changes only `Result` and passes upstream protected-mutation validation.
- Exact exclusions bypass; all other tool names enter the redactor.
- A direct native tool returning non-empty syntactically invalid `Structured` JSON skips the transform waterfall; durable/model-visible output stays generic, and documentation identifies the original full-result notice as trusted upstream behavior.
- Concurrent transforms pass `go test -race`.
- Same-policy resume succeeds; changed-policy resume returns `ErrExtensionPlanMismatch` before durable mutation.
- Deactivation and close follow the public lifecycle above.
