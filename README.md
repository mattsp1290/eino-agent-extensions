# Eino Agent Extensions

This repository contains focused extensions for
[`github.com/mattsp1290/eino-agent`](https://github.com/mattsp1290/eino-agent).
It currently provides bounded background command jobs, a host-mediated
`ask_user` tool, and a trusted native tool-result secret redactor, all verified
against Eino Agent v0.2.0.

## Bounded background command jobs

`backgroundjobs` atomically mounts four tools: `background_job_start`,
`background_job_status`, `background_job_list`, and `background_job_kill`.
Start returns an opaque job ID promptly; callers poll status for bounded stdout
and stderr tails, list jobs owned by the same session/workspace, or terminate a
job. The live registry and raw tails are memory-only and disappear with the host
process, although Eino durably retains settled tool inputs and short results.

Every mount requires an explicit absolute POSIX-compatible `sh` path that
accepts `-c`, a non-secret shell identity, a selected environment policy, a
non-secret environment identity, and finite limits. Shell compatibility is a
host precondition:

```go
mount, err := backgroundjobs.Mount(ctx, registry, component, backgroundjobs.Options{
	ShellPath: "/bin/sh",
	ShellIdentity: "host-system-sh-v1", // rotate when shell behavior changes
	Environment: backgroundjobs.Environment{
		Mode: backgroundjobs.EnvironmentExplicitOnly,
		Identity: "background-env-v1", // rotate for every effective env change
		Overrides: map[string]string{"PATH": "/usr/bin:/bin"},
	},
	Limits: backgroundjobs.Limits{
		MaxRunning: 4, MaxTracked: 32,
		MaxCommandBytes: 16 << 10, MaxWorkingDirectoryBytes: 4 << 10,
		MaxOutputBytesPerStream: 256 << 10,
		MaxEnvironmentEntries: 256, MaxEnvironmentBytes: 64 << 10,
		DefaultTimeout: 0, MaxTimeout: 30 * time.Minute,
		TerminateGrace: 2 * time.Second, KillWait: 5 * time.Second,
	},
})
```

Zero scope selects global registration, and zero order selects
`backgroundjobs.DefaultOrder`; explicit nonzero values are preserved.

`EnvironmentExplicitOnly` uses only the supplied overrides.
`EnvironmentInheritAndOverride` snapshots the host environment once at mount
and then applies overrides. Changing any inherited or override key/value without
rotating `Environment.Identity` can make strict resume reuse a stale component
identity. Environment values are never included in the component hash,
permission patterns, list output, or package diagnostic errors.

An omitted or zero `timeout_seconds` uses `DefaultTimeout`; a zero default means
no automatic timeout. A positive per-job value replaces the default but cannot
exceed the required whole-second `MaxTimeout`. Command text is canonical durable
Eino input, so credentials must never be placed in a command. Output is a text-
oriented suffix, not a complete transcript; mount `toolresultredactor` as the
last result transform when process output may contain secrets.

Linux and Darwin launches use a package supervisor that anchors the original
POSIX process-group identity through TERM/KILL and is reaped before kill,
timeout, or close succeeds. Other platforms reject the mount explicitly.
Initial working-directory resolution follows symlinks and must remain beneath
the host-admitted runtime workspace root. This is launch validation only—not a
filesystem, network, credential, process, container, or operating-system
sandbox. A command can still access absolute paths, use the network, or
deliberately detach from the launched process group.

## Host-mediated ask-user tool

`github.com/mattsp1290/eino-agent-extensions/askuser` atomically mounts one
`ask_user` tool. It lets a model ask one question with two through five fixed,
ordered options; every question also has the package-owned
`Other (write your own answer)` choice. The host supplies the
presentation-neutral responder and owns UI, routing, authentication,
notifications, host-side persistence, and adapter lifecycle.

```go
mount, err := askuser.Mount(ctx, registry, component, askuser.Options{
	Responder: askuser.ResponderFunc(func(ctx context.Context, request askuser.Request) (askuser.Response, error) {
		// Route by request.SessionID, request.RunID, and request.ToolCallID.
		// A fixed selection is one-based.
		return askuser.Response{Kind: askuser.ResponseSelected, SelectedOption: 1}, nil
	}),
	ResponderIdentity: "host-question-router-v1", // rotate with routing behavior
	Limits: askuser.Limits{
		MaxQuestionBytes: 4 << 10,
		MaxOptionLabelBytes: 512,
		MaxOptionDescriptionBytes: 2 << 10,
		MaxCustomAnswerBytes: 4 << 10,
		MaxInFlight: 8,
		MaxWait: 2 * time.Minute,
	},
})
if err != nil {
	return err
}
defer func() {
	mount.Deactivate()
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if closeErr := mount.Close(closeCtx); closeErr != nil {
		log.Printf("ask_user mount did not quiesce: %v", closeErr)
	}
}()
```

All limits and `ResponderIdentity` are required. Zero scope means global scope,
and zero order uses `askuser.DefaultOrder`. The responder receives a defensive
copy of the normalized question and options, durable call identities,
`AllowCustom=true`, and the fixed custom-choice label. `Respond` calls can
overlap up to `MaxInFlight`; adapters must be concurrency-safe and route every
response using the supplied IDs.

| Status | Meaning | Tool error? |
| --- | --- | --- |
| `selected` | A fixed option was chosen. | no |
| `custom` | A bounded free-form answer was supplied. | no |
| `dismissed` | The person declined the question. | no |
| `unavailable` | Presentation is unavailable or capacity is full. | no |
| `timed_out` | `MaxWait` expired while the run remained active. | no |

Capacity admission does not queue. `MaxInFlight` counts responder callbacks
until they actually exit, even after a tool caller times out. `MaxWait` bounds
the tool's wait, not arbitrary host code: Go cannot forcibly terminate a
non-cooperative responder, and such a callback may retain its bounded slot and
delay close. Responders must honor cancellation to remove presentation
promptly. Parent cancellation observed during classification wins; otherwise a
response completed strictly before the package deadline wins, while completion
at or after the deadline is `timed_out`.

The tool requests only the stable `interaction.ask` permission with the same
constant permission pattern. The host must allow it or provide Eino approval
handling. Denial and approval-required settlement happens before responder
admission and never calls the responder. Parent cancellation remains
cancellation, and responder errors, invalid responses, and panics are sanitized
tool failures. A responder error that merely wraps `context.Canceled` or
`context.DeadlineExceeded` does not acquire sentinel identity while the actual
parent and package deadline sources are inactive.

Eino durably stores the question, fixed options, and selected or custom answer,
and exposes normal results to the next model turn. Never collect credentials or
secrets with this tool. A result redactor can provide defense in depth for
output but cannot erase the already durable tool input. Pending calls may run
once after Eino claims them during resume; calls already marked running are
interrupted without re-prompting. Limit or `ResponderIdentity` changes alter the
exact-plan fingerprint and can reject resume, so drain unfinished runs before
upgrading or removing the component.

This package is trusted in-process native code. It supplies no terminal, web,
AG-UI, or other built-in presentation, performs no transport or storage work of
its own, and claims no Wasm or Pi/comparator parity. See
[`examples/ask-user`](examples/ask-user) for a deterministic, non-interactive
host adapter; package integration tests cover the full orchestrator/SQLite
path.

## Tool-result redactor

`toolresultredactor` mounts one ordered transform at
`runtime.ToolResultTransformPoint`. A global mount scans every callback-admitted
tool result except exact tool names excluded by the host. It redacts matching
spans in string values with `[REDACTED]`; a key match or a field that cannot be
safely scanned within its configured budget replaces only its containing field
with the documented placeholder representation.

Install and import the module:

```sh
go get github.com/mattsp1290/eino-agent-extensions
```

```go
registry, err := composition.NewRegistry(nil)
if err != nil {
	return err
}

component := extension.Component{
	InstanceID: "host-tool-result-redactor",
	Artifact: extension.Artifact{
		Name: "tool-result-redactor", Version: "1", Hash: artifactHash,
		SourceKind: extension.SourceNative,
	},
}

mount, err := toolresultredactor.Mount(ctx, registry, component, toolresultredactor.Options{
	ExcludedTools: []string{"exact-safe-tool"},
	AdditionalPatterns: []toolresultredactor.Pattern{{
		ID: "host-synthetic-rule", Expression: `HOST_MARKER_[A-Z]+`,
	}},
	Limits: toolresultredactor.Limits{
		MaxFieldBytes: 64 << 10, MaxStructuredBytes: 256 << 10,
		MaxStructuredDepth: 32, MaxStructuredNodes: 10_000,
		MaxAttachments: 32, MaxMetadataEntries: 128,
		MaxMatchesPerField: 256, MaxPatterns: 32, MaxPatternBytes: 4 << 10,
	},
})
if err != nil {
	return err
}
defer func() {
	mount.Deactivate()
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if closeErr := mount.Close(closeCtx); closeErr != nil {
		log.Printf("tool-result-redactor mount did not quiesce: %v", closeErr)
	}
}()
```

Operational limits are required host inputs; the package intentionally has no
latency or retention defaults. Zero scope means global scope. Zero order means
`toolresultredactor.LateOrder`. A nonzero scope or order is allowed. Exclusions
are exact and case-sensitive; there are no glob or regex exclusions.

The versioned built-in catalog conservatively detects complete PKCS private-key
armor, Authorization Bearer assignments, and documented GitHub-prefixed token
shapes. It is intentionally incomplete. Host RE2 expressions extend rather
than replace the built-ins, and expressions capable of a zero-width match are
rejected before the atomic mount is published. Canonical exclusions, host
rules, limits, built-in version, and placeholder version form a deterministic
configuration hash. Scope and order enter Eino's frozen identity separately.

## Coverage and fallback behavior

The transform scans `ToolResult.Output`, every string key and value in
`Structured`, result metadata, attachment `ID`, `MIMEType`, `Name`, and `URL`,
and attachment metadata. Non-string JSON values are preserved. Matching spans
in values are replaced while unmatched content remains intact.

After result transforms complete, Eino v0.2.0 may reapply its fixed,
runtime-owned `permission_status` metadata projection. That enum is not
tool-controlled content and is outside host-pattern matching at this transform.

JSON and map keys are scanned but never rewritten. A matching, invalid, or
over-limit JSON key replaces the top-level `Structured` value with the valid
JSON string `"[REDACTED]"`. The equivalent condition in a metadata map replaces
that map with `map[string]string{"": "[REDACTED]"}`. Attachment-count overflow
replaces the slice with one attachment whose `Name` is `[REDACTED]`. Invalid or
over-limit scalar values are replaced individually. Depth, node, or raw
structured-byte exhaustion replaces only `Structured`. Unsafe runtime content,
cancellation, and contained internal panics yield sanitized success so
settlement can continue.

The structured walker preserves every untouched byte, including whitespace,
key order, duplicate keys, escaped text, and number spelling. It validates raw
UTF-8 and UTF-16 surrogate pairing before decoding a JSON string.

## Trust and limitations

This package is trusted in-process native code. It performs no network call,
filesystem access, subprocess execution, external scanning, credential lookup,
or dynamic reload. Package budgets bound callback work after Eino invokes the
transform; they cannot bound memory already allocated by a tool or Eino's
defensive pre-callback clone. Hosts and tool implementations must bound result
construction.

The transform point is an ordered waterfall, not an enforced terminal hook.
When full-result notice protection is required, keep the redactor as the final
`ToolResultTransformPoint` callback. A failing earlier transform skips the
redactor, while a failing later transform makes Eino v0.2.0 restore the original
pre-waterfall result. Durable and model-visible settlement is generic in either
case, but a trusted `ToolSettledPoint` observer can receive the original full
result. Other native transforms must return sanitized success when full-result
notice protection is required.

Non-empty syntactically invalid `Structured` JSON is rejected by Eino v0.2.0
before any result transform runs. The redactor therefore cannot sanitize that
result or its sibling fields. Durable/model-visible settlement is generic, but
full-result observers remain trusted. This is outside the package's
result-level guarantee.

The package does not scan prompts, tool-call inputs, model responses, external
attachment contents, existing session files, logs, other databases, or host
storage outside Eino settlement. It does not rewrite earlier durable records.
The conservative catalog cannot prove that content is secret-free, and the
package makes no Pi/comparator parity or complete leak-prevention claim.

Deactivation affects only newly acquired plans. `Close` waits for already
acquired plans to release and is safe to call again. Removing or changing a
component can cause unfinished durable runs to fail Eino's exact resume check;
hosts should drain them before removal.

## Verification

The credential-free black-box suite uses a real composition registry, frozen
run plan, streaming orchestrator, SQLite store, second model turn, settled
notice, release, and close path. No provider account is involved.

```sh
GOWORK=off go mod tidy
git diff --exit-code -- go.mod go.sum
GOWORK=off go list -m all
GOWORK=off go mod verify
GOWORK=off go vet ./...
GOWORK=off go test ./...
GOWORK=off go test -race ./...
```
