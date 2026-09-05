# Eino Agent Extensions

This repository contains focused extensions for
[`github.com/mattsp1290/eino-agent`](https://github.com/mattsp1290/eino-agent).
It currently provides a session-scoped Python REPL, bounded background command
jobs, a host-mediated `ask_user` tool, a bounded host-mediated `web_search`
bridge, and a trusted native tool-result secret redactor, all verified against
Eino Agent v0.3.3.

## Session-scoped Python REPL

`pythonrepl` atomically mounts `python_repl` and `python_repl_clear`. The first
tool executes bounded Python input in an interpreter owned by the durable
session/workspace pair; the second discards that owner's live interpreter state
without eagerly starting a replacement. Both tools are retry-unsafe and request
the constant permissions `process.python.execute` and
`process.python.manage`, respectively.

The host must supply an absolute Python 3.11-3.14 executable on Linux or macOS,
a behavior identity for that build, an existing temporary root, an explicit
environment plus non-secret environment identity, and finite limits:

```go
mount, err := pythonrepl.Mount(ctx, registry, component, pythonrepl.Options{
	PythonPath: "/absolute/path/to/python3.12",
	PythonIdentity: "host-python-3.12-build-v1", // rotate with build behavior
	TempRoot: "/absolute/trusted/temp/root",
	Environment: pythonrepl.Environment{
		Identity: "python-env-v1", // rotate for every effective value change
		Entries: map[string]string{"LANG": "C.UTF-8"},
	},
	Limits: pythonrepl.Limits{
		MaxSessions: 8, MaxQueuedPerSession: 4, MaxCodeBytes: 32 << 10,
		MaxOutputBytesPerStream: 64 << 10, MaxResultBytes: 64 << 10,
		MaxExceptionBytes: 64 << 10,
		MaxEnvironmentEntries: 64, MaxEnvironmentBytes: 16 << 10,
		DefaultTimeout: 30 * time.Second, MaxTimeout: 2 * time.Minute,
		VenvCreateTimeout: 30 * time.Second, RunnerStartTimeout: 10 * time.Second,
		TerminateGrace: 500 * time.Millisecond, KillWait: 5 * time.Second,
	},
})
if err != nil {
	return err
}
defer func() {
	mount.Deactivate()
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if closeErr := mount.Close(closeCtx); closeErr != nil {
		log.Printf("python REPL cleanup incomplete: %v", closeErr)
	}
}()
```

Zero scope selects global registration and zero order selects
`pythonrepl.DefaultOrder`. Global registration does not share state: the owner
key always includes both the durable session ID and workspace ID, and later
calls must present the same canonical workspace root. Operations serialize per
owner while different owners remain isolated. A successful call preserves
globals; a syntax or runtime exception is returned as a bounded `python_error`
result and may preserve globals established earlier in the healthy runner.

Cancellation or timeout after a runner may have accepted state, explicit clear,
protocol failure, or runner exit discards globals and advances a process-local
generation. Cancellation during initial venv creation or before the first
request can reach a newly started runner cleans up without advancing generation.
Ordinary reset retains the owner's mutable private venv; close removes it after
the runner process group, out-of-group reaper supervisor, and Go child wait all
finish. State never survives remount, host restart, host crash, owner change, or
resume in a new process. Under Eino Agent v0.3.3 a pending durable call may be
claimed and executed once during resume, while a call already marked running is
interrupted without re-execution. Generation is diagnostic state, not a durable
resume token.

`MaxSessions` is a mount-lifetime budget for distinct owners admitted by their
first execute. A failed setup still consumes that owner's slot, clear of an
unknown owner consumes none, and clear of an existing owner reclaims none. Close
and remount—or a host-owned session-scoped mount lifecycle—starts a fresh
budget. `MaxQueuedPerSession` bounds calls waiting behind one active owner.
Serialization covers admitted tool operations only: user-created background
threads, async work, and subprocesses that survive a response are unsupported
across calls and can make globals or external effects nondeterministic. Late
Python-level output is discarded rather than attributed to a later result.

### Trust and durability boundary

This package is trusted native code and **not a sandbox**. Python runs with the
host user's filesystem, network, process, and credential authority. The venv is
created with `-m venv --without-pip`, and children receive only the environment
entries frozen at mount; neither property is a security boundary. Python can
still read host files, use the network, invoke known or absolute executables,
inspect every explicit environment entry, mutate the retained venv, consume
unbounded memory/CPU before a timeout takes effect, and deliberately detach a
descendant from process-group cleanup. A host-managed container, VM, or sandbox
is required for stronger isolation.

Snippets must also be trusted not to tamper with the interpreter's control
machinery. User code shares the process, imported modules, and protocol file
descriptors with the REPL wrapper; deliberate interference can invalidate
result acceptance and cross-call ordering. Put untrusted snippets behind an
external OS isolation boundary.

Eino durably stores normalized Python code before execution and durably stores
the bounded inline stdout, stderr, exception, and result afterward. Never put
secrets in code, and avoid secret environment values because Python can emit
them. The package adds no path, PID, or environment value to results,
fingerprints, permission patterns, or diagnostics, but it deliberately does not
filter user-authored output. Mount `toolresultredactor` last for defense in
depth; it cannot erase already durable tool input. Output has no spill artifact,
and timeout/output bounds do not roll back side effects or impose a memory/CPU
limit.

Hosts own permission and approval policy, Python build validation and identity
rotation, environment-identity rotation, stale-file handling after a hard
crash, bounded mount close, and any presentation/authentication layer. If the
reaper supervisor dies unexpectedly, the owner and venv are quarantined: the
package will not risk a stale process-group signal or falsely report successful
cleanup, so host operations must resolve the escaped resource and discard that
mount. See [`examples/python-repl`](examples/python-repl) for a credential-free,
non-interactive registry/orchestrator/SQLite journey that assigns a global,
reads it in a later run, clears it, and verifies its absence.

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

## Bounded web-search bridge

`github.com/mattsp1290/eino-agent-extensions/websearch` atomically mounts one
synchronous `web_search` tool. The model supplies exactly one bounded `query`;
the host's trusted `Searcher` invokes its selected backend and returns source
records containing only `title`, an absolute HTTP(S) `url`, and `snippet`.

```go
mount, err := websearch.Mount(ctx, registry, component, websearch.Options{
	Searcher: websearch.SearcherFunc(func(ctx context.Context, query string) ([]websearch.Source, error) {
		// Resolve credentials and enforce host egress, freshness, rate, and
		// backend policy here. Never return raw provider diagnostics.
		return searchBackend(ctx, query)
	}),
	SearcherIdentity: "host-search-router-v1", // rotate with behavior
	Limits: websearch.Limits{
		MaxQueryBytes: 16 << 10,
		MaxResults: 10,
		MaxTitleBytes: 1 << 10,
		MaxURLBytes: 8 << 10,
		MaxSnippetBytes: 16 << 10,
		MaxInFlight: 4,
		MaxWait: 30 * time.Second,
	},
})
if err != nil {
	return err
}
defer func() {
	mount.Deactivate()
	// First drain or interrupt runs and release their frozen plans.
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if closeErr := mount.Close(closeCtx); closeErr != nil {
		log.Printf("web_search mount did not quiesce: %v", closeErr)
	}
}()
```

Every limit and `SearcherIdentity` is required. Zero scope selects global
scope; zero order uses `websearch.DefaultOrder`. `ConfigHash` identifies all
behavior-bearing limits and the callback identity while excluding the callback,
scope, order, and component artifact identity. Hosts must rotate
`SearcherIdentity` when backend selection, routing, or normalization changes,
and must honestly rotate component artifact identity when its behavior changes.

The adapter inspects only the first `MaxResults` callback records and never
refills from later candidates. Title and snippet are deterministically limited
to valid UTF-8 byte prefixes. A URL is retained byte-for-byte only when its
complete original value is valid UTF-8, within `MaxURLBytes`, absolute HTTP(S),
has a host, and contains no user information; otherwise the entire record is
dropped. The returned slice and strings transfer to the adapter on successful
callback return and must not later be mutated or reused. The adapter builds a
separate bounded result slice. A successful search with no valid sources is
exactly `{"results":[]}`; it is distinct from timeout, saturation, or backend
failure.

The only requested permission is `network.web.search`, with the constant
pattern `web_search`. Permission denial or approval-required settlement occurs
before capacity admission and invokes `Searcher` zero times. Eino durably
stores the canonical trimmed query before execution and the bounded inline
result afterward, so queries and source fields must not contain credentials or
other secrets. Query text, backend identity, endpoints, credentials, and raw
errors never enter permission identity, tool metadata, or package errors.
Backend errors and panics become a stable sanitized failure.

`MaxWait` adds a finite child deadline while preserving parent cancellation.
`MaxInFlight` bounds callbacks per mount; saturation does not queue. A callback
continues to occupy its slot through source bounding and JSON encoding and, if
it ignores cancellation, until it actually exits. Thus timeout bounds the tool
caller's wait but cannot forcibly terminate arbitrary Go code. `Searcher` must
be concurrency-safe and cancellation-cooperative, and the host remains
responsible for backend-level resource controls.

`web_search` is retry-unsafe. Strict resume may claim and execute a pending call
once, interrupts a recorded running call without another backend invocation,
and never re-executes a terminal call. Backend work can finish before a process
crash that precedes durable settlement, so this is not exactly-once execution.
Drain unfinished runs before changing limits, `SearcherIdentity`, registration
placement, or artifact identity because drift rejects strict resume before
durable mutation.

### Ownership, trust, and cleanup

`websearch` owns the canonical schema, query semantics, source validation,
bounded adapter, timeout, concurrency, and retention budget. Eino Agent owns
generic JSON validation, permission enforcement, composition, durable
settlement, inline retention, frozen plans, and strict resume. The embedding
host owns the backend, credentials, egress and endpoint policy, freshness,
ranking, rate limits, raw-error observability, presentation, and backend
lifecycle.

This is trusted native code, not a sandbox. Successful source values remain
host-controlled untrusted model content even after structural bounding. The
package does not fetch returned URLs and therefore creates no URL-fetch SSRF
boundary; any later fetcher needs separate network and content policy. A
separately mounted `toolresultredactor` is result defense in depth only: it
cannot erase already durable query input and does not replace backend error
sanitization.

Shutdown has two phases. `Deactivate` removes the tool from future plans, while
already retained plans keep authority until release. `Close` first waits for
those leases; a timeout in that phase means the host must continue draining
runs and plans. After leases drain, coordinator cleanup blocks admission,
cancels active callbacks, and waits for their real exit. A timeout in this
second phase requires quarantining the mount until a later close observes
quiescence, or replacing the process if a callback never exits. Shut down the
host-owned backend only after extension close succeeds.

See [`examples/web-search`](examples/web-search) for a deterministic
credential-free registry, permission, orchestrator, and in-memory SQLite
journey. This package does not implement Pi's extension loader or rendering,
Firecrawl search/scrape/crawl, multi-query generated-answer or result-storage
flows, URL fetching, or any provider integration.

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

After result transforms complete, Eino v0.3.3 may reapply its fixed,
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
redactor, while a failing later transform makes Eino v0.3.3 restore the original
pre-waterfall result. Durable and model-visible settlement is generic in either
case, but a trusted `ToolSettledPoint` observer can receive the original full
result. Other native transforms must return sanitized success when full-result
notice protection is required.

Non-empty syntactically invalid `Structured` JSON is rejected by Eino v0.3.3
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
