# Eino Agent Extensions

This repository contains focused extensions for
[`github.com/mattsp1290/eino-agent`](https://github.com/mattsp1290/eino-agent).
It currently provides a session-scoped Python REPL, bounded background command
jobs, a host-mediated `ask_user` tool, a bounded delegated-task bridge, a
bounded host-mediated `web_search` bridge, a trusted workspace-instructions
prompt section, a trusted native tool-result secret redactor, and a command-policy
guard, all verified
against Eino Agent v0.3.3.

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

## Bounded delegated-task tool

`github.com/mattsp1290/eino-agent-extensions/delegatetask` atomically mounts one
synchronous `delegate_task` tool. A model supplies a bounded task and one
bounded `profile`; the host's trusted `Runner` performs or rejects that work.
The profile is an opaque, non-secret identifier. The extension validates its
syntax but never interprets, upgrades, substitutes, or grants capabilities from
it.

```go
mount, err := delegatetask.Mount(ctx, registry, component, delegatetask.Options{
	Runner: delegatetask.RunnerFunc(func(ctx context.Context, request delegatetask.Request) (delegatetask.Response, error) {
		// Validate request.Profile and request.WorkspaceID/WorkspaceRoot under
		// host policy before constructing any child runtime.
		return delegatetask.Response{
			Status: delegatetask.ResponseCompleted,
			Output: "bounded non-secret summary",
		}, nil
	}),
	RunnerIdentity: "host-delegate-router-v1", // rotate with routing behavior
	Limits: delegatetask.Limits{
		MaxTaskBytes: 16 << 10,
		MaxProfileBytes: 64,
		MaxResultBytes: 64 << 10,
		MaxInFlight: 4,
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
		log.Printf("delegate_task mount did not quiesce: %v", closeErr)
	}
}()
```

Every limit and `RunnerIdentity` is required. Zero scope selects global scope,
and zero order uses `delegatetask.DefaultOrder`. A global definition can resolve
in different workspaces. Each `Request` carries the durable session, run, and
tool-call IDs plus Eino's authoritative workspace ID and root exactly as
supplied, including empty values. These are routing inputs, not grants: the
Runner decides whether its selected profile requires workspace context and is
responsible for validating paths and isolation policy.

| Status | Meaning | Tool error? |
| --- | --- | --- |
| `completed` | Delegated work completed. | no |
| `failed` | Delegated work ran but did not complete successfully. | no |
| `rejected` | Host policy rejected the task or profile. | no |
| `unavailable` | The Runner cannot accept work, or mount capacity is full. | no |
| `timed_out` | The package's `MaxWait` expired while the parent remained active. | no |

Parent cancellation remains cancellation. Runner errors, malformed responses,
and panics become sanitized tool failures and never expose host error or panic
text. The package does not queue at capacity. A callback continues to count
against `MaxInFlight` until it actually exits, even after timeout, so a
non-cooperative Runner can delay cleanup but cannot create more than the
configured number of package-owned callback goroutines. Timeout and
cancellation do not roll back Runner side effects.

The tool requests Eino's stable `session.subagent` permission with the pattern
`delegate-profile:<profile>`. Permission denial or approval-required settlement
happens before capacity admission and never calls Runner. Task text, workspace
paths, results, and host errors never enter the permission pattern.

Eino durably stores the canonical task and profile before execution and the
bounded inline result afterward; neither may contain secrets. A separately
mounted `toolresultredactor` is output defense in depth only and cannot erase
already durable task input. Eino v0.3.3 canonicalizes raw JSON before package
normalization: its materialized decoder rejects raw invalid UTF-8 in the
current Go toolchain and replaces isolated UTF-16 surrogate escapes with
U+FFFD. Because a legitimate U+FFFD is indistinguishable from repaired input,
the package accepts replacement characters.

`delegate_task` is retry-unsafe. A pending durable call may be claimed and run
once during strict resume; a call already marked running is interrupted without
calling Runner again, and terminal calls are not re-executed. Runner side
effects may finish before a host crash that precedes durable settlement, so
this is non-reexecution after a recorded running claim—not exactly-once
execution. Rotate `RunnerIdentity` whenever profile routing or Runner behavior
changes and drain unfinished runs before changing it or any limit, since such
drift rejects exact resume before durable mutation.

### Trust and capability boundary

This extension and its Runner are trusted native code, not a sandbox. Runner
alone owns profile existence and privilege checks, child model and tool
selection, filesystem/network/process/credential policy, resource limits, and
isolation. Hosts executing untrusted work must place Runner behind an OS or
remote isolation boundary. The extension itself creates no child Eino agent,
process, background task, recursive delegation, provider, or credential flow.

Mount shutdown first blocks admission, cancels active callbacks, and waits for
them to exit. If `Close` reaches its caller's deadline, cleanup has not
succeeded: quarantine that mount, do not remount a replacement Runner
generation in the same process while the old callback lives, and retry Close
only to observe quiescence. Replace the host process if the callback never
exits. For rollback, quiesce and unmount before reverting package construction
or the dependency pin; already settled records remain ordinary tool history.

See [`examples/delegate-task`](examples/delegate-task) for a credential-free,
deterministic Runner using a real registry, frozen plan, explicit workspace
routing, and structured result.

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
		MaxRawInputBytes: 128 << 10,
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

Every limit and `SearcherIdentity` is required. `MaxRawInputBytes` bounds the
complete model-supplied JSON before parsing and must accommodate the worst-case
escaped `MaxQueryBytes` value; `MaxQueryBytes` applies to the UTF-8 query after
trimming. Zero scope selects global scope; zero order uses
`websearch.DefaultOrder`. `ConfigHash` identifies all behavior-bearing limits
and the callback identity while excluding the callback, scope, order, and
component artifact identity. Hosts must rotate
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
before capacity admission and invokes `Searcher` zero times. Under Eino Agent
v0.3.3, the runtime durably creates the canonical call as pending, claims it as
running, and only then evaluates permission before entering the package
executor. A crash during policy evaluation therefore leaves a running call;
strict resume interrupts that call without reevaluating permission or invoking
`Searcher`. The bounded inline result is stored after execution, so queries and
source fields must not contain credentials or other secrets. Query text,
backend identity, endpoints, credentials, and raw errors never enter permission
identity, tool metadata, or package errors. Backend errors and panics become a
stable sanitized failure.

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

## Workspace instructions prompt section

`github.com/mattsp1290/eino-agent-extensions/workspaceinstructions` atomically
mounts one prompt registration named `workspace/instructions`. The host resolves
the trusted workspace for every model call; the package reads configured plain
file names from the admitted boundary down to the workspace root and renders
them as one bounded section in Eino's single system prompt.

```go
mount, err := workspaceinstructions.Mount(ctx, registry, component,
	workspaceinstructions.Options{
		Resolver: workspaceinstructions.ResolverFunc(func(ctx context.Context,
			request workspaceinstructions.Request,
		) (workspaceinstructions.Workspace, error) {
			// Map request.SessionID to host-owned workspace and trust state.
			return workspaceinstructions.Workspace{
				Root: workspaceRoot, Boundary: repositoryBoundary, Trusted: true,
			}, nil
		}),
		ResolverIdentity: "host-workspace-router-v1", // non-secret; rotate with behavior
		FileNames: []string{"AGENTS.md"},
		Limits: workspaceinstructions.Limits{
			MaxFileNames: 4, MaxChainDepth: 16, MaxFileBytes: 32 << 10,
			MaxSectionBytes: 128 << 10, MaxInFlight: 16,
			MaxWait: 2 * time.Second,
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
		log.Printf("workspace instructions mount did not quiesce: %v", closeErr)
	}
}()
```

The discovery chain includes the boundary and root, outermost first. A boundary
equal to the root reads only the root. In each directory, file-name order is the
configured order; no directory listing or recursive search occurs. Nil
`FileNames` selects `AGENTS.md`, while an explicitly empty list is invalid.
Missing, unreadable, non-regular, symlinked, empty, NUL-bearing, or invalid-UTF-8
files are skipped. An oversized file is cut at a UTF-8 boundary and visibly
marked; remaining files that cannot fit are visibly omitted. If no file is
admitted, Eino omits the section and the run continues.

Files are re-read on every model call, including later steps and attempts of the
same run; the package has no cache or watcher. Zero scope selects global scope,
and zero order selects `workspaceinstructions.DefaultOrder` (100), after the
runtime base prompt. A session mount shadows a global mount with the same
`PromptName` for only that session. File names and their order, all limits,
`ResolverIdentity`, the registration, and the versioned render format enter
`ConfigHash`; the resolver value, host state, scope, and order do not. Scope and
order are frozen separately in Eino's durable plan identity.

The resolver must be concurrency-safe, honor cancellation, and accept every
durable identity in `workspaceinstructions.Request`. `MaxWait` covers resolver
work, workspace validation, file reads, and rendering. `MaxInFlight` is a
per-mount, non-queuing limit. Resolver faults and panics, malformed workspaces,
saturation, and deadline expiry return sanitized errors and fail that model
attempt without retry; they never silently drop policy. Size `MaxInFlight` for
the host's concurrent-run count.

### Workspace trust and durability boundary

This is trusted native code, not a sandbox. The resolver makes the trust
decision; an untrusted or absent workspace contributes nothing. The package
validates structure and containment but neither sanitizes nor interprets the
semantics of an admitted instruction file. Rendered instructions are persisted
in Eino's model-request audit records. Never put secrets in instruction files:
`toolresultredactor` scans tool results, not system prompts.

Root and boundary are canonicalized and must be an ancestor-or-equal pair.
Every candidate access then goes through one `os.Root` opened at the boundary,
so the operating system rejects symlink escapes at any path component;
instruction-file symlinks are skipped even when their target is inside the
boundary. Root-relative `path` labels such as `../AGENTS.md` avoid absolute host
paths, but they are model guidance, not tamper-evident provenance. File bodies
have trailing Unicode whitespace removed; the remaining bytes are emitted
unchanged and can forge envelope text.

A blocked filesystem or non-cooperative resolver call cannot be forcibly
terminated. The caller receives `code=deadline`, while that goroutine retains
its slot until it exits; later calls can receive `code=saturated`. One global
mount shares this pool across all sessions, so a stalled workspace can exhaust
capacity for unrelated sessions. Hosts serving several isolation boundaries
should mount one instance per boundary—typically session scoped—and treat
sustained saturation as a host-level outage signal.

`MaxSectionBytes`, the base prompt, history, and tool schemas together must fit
under the orchestrator model-request ledger cap (4 MiB by default, configurable
with `runtime.WithModelRequestMaxBytes`). An oversized audited request fails
with `session.ErrModelRequestTooLarge`. Linux and macOS are tested; Windows
behavior is not exercised by this repository's CI.

Strict resume requires the identical component instance ID, artifact,
configuration hash, prompt name, scope, and order. Drain unfinished runs before
changing limits, file names, resolver identity, placement, or artifact identity.
To remove the extension, deactivate it, release acquired plans, then close it
with a deadline. See
[`examples/workspace-instructions`](examples/workspace-instructions) for a
credential-free deterministic frozen-plan example.


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

## Command-policy guard

`commandguard` atomically mounts one native deny-only guard before Eino's
permission policy and command executor. The host supplies executable/prefix
rules and finite limits. A matched rule, unreliable analysis, invalid covered
input, or exhausted bound denies the entire call. A complete nonmatch abstains
and leaves permissions and approval requirements in force.

```go
bindings := append(commandguard.DefaultBindings(), commandguard.Binding{
    ToolName: "host_command", CommandField: "command",
})
mount, err := commandguard.Mount(ctx, registry, component, commandguard.Options{
    Bindings: bindings,
    Rules: []commandguard.Rule{{
        ID: "host-git-push", Executable: "git", ArgPrefix: []string{"push"},
    }},
    Limits: commandguard.Limits{
        MaxBindings: 8, MaxRules: 32, MaxRuleBytes: 2048, MaxPrefixArgs: 16,
        MaxRawInputBytes: 16384, MaxJSONDepth: 16, MaxJSONNodes: 256,
        MaxCommandBytes: 4096, MaxAnalysisBytes: 8192,
        MaxASTNodes: 2048, MaxASTDepth: 64, MaxWords: 512, MaxWordBytes: 4096,
        MaxWrapperDepth: 8, MaxInFlight: 4,
    },
})
if err != nil {
    return err
}
// Acquire/run/release plans through Eino, then drain this mount.
mount.Deactivate()
closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
return mount.Close(closeCtx)
```

The component must have a native source kind and honest artifact name, version,
and hash. `ConfigHash` validates and hashes the effective immutable policy; Mount
fills an empty component config hash and rejects a conflicting supplied hash.
Caller slices and argument prefixes are copied. Rule and binding order do not
change the hash. Every limit, parser/version, grammar version, and fixed diagnostic
version participates. Scope and effective order are separately fingerprinted by
Eino. Rotate artifact version/hash when implementation behavior changes. Saved
runs require their exact original policy; never edit fingerprints to force resume.

`Dialect` is a closed enum: `DialectPOSIX` and `DialectBash`. Empty dialect means
POSIX. `Binding` names an exact model tool and literal top-level JSON string key;
periods are ordinary key characters. Nil `Options.Bindings` selects
`shell/cmd/POSIX` and `background_job_start/command/POSIX`. A nonnil list replaces
these defaults, and an empty list or duplicate tool name is invalid. Use
`DefaultBindings()` to obtain a fresh slice when extending them. `standard.shell`
is a registration ID, not the model tool name. Unbound calls abstain without
parsing or reserving capacity.

`Rule` contains a unique identifier, one executable basename, and an exact
positional `ArgPrefix`. Matching is case-sensitive, removes ordinary quoting,
and compares the final POSIX slash-separated basename without filesystem access.
`/usr/bin/git push` and `g'it' 'push'` match the example; `echo git push`,
`git pushx`, and `git -C repo push` do not. Flags are never skipped. An empty
prefix denies every invocation of that basename; an empty argument token is
valid and distinct from no argument. Executable paths, regexes, globs and
caller-supplied messages are not rule configuration.

Unknown executable words deny. `git "$ACTION"` also denies because it could
match `push`; `git status "$PATHSPEC"` can abstain after a known mismatch.
Unknown words may split into multiple arguments, so later tokens cannot resolve
an earlier ambiguous prefix. The guard walks all supported branches, including
unreachable commands, pipelines, blocks, subshells, loops, case arms, scalar
assignments, redirects, substitutions and expandable heredocs. Quoted literal
heredocs stay data. Unquoted globs, tilde/brace expansion, simple parameters,
command/process substitution and Bash dollar quoting are unknown words; nested
execution is still inspected. CR bytes are preserved rather than inheriting the
parser's CRLF normalization. Functions, arithmetic, arrays, extended globs,
extended test/decl/time/coprocess syntax and non-simple parameter expansions deny
conservatively. The parser stores extended-glob patterns as literal text, so
walking them cannot reliably inspect nested execution.

Assignments and loop targets reject shell-owned evaluation state: `OPTIND`,
`RANDOM`, `SRANDOM`, `SECONDS`, `HISTCMD`, `MAILCHECK`, `PS0` through `PS4`,
`PROMPT_COMMAND`, `ENV`, and the entire `BASH_*` family.
The same restriction applies to `env` and `sudo` assignment operands. These
targets include arithmetic, prompt and startup evaluation state;
quoted values are not necessarily inert. The conservative
boundary applies in both dialects, while ordinary scalar variables remain
supported. Inherited attributes on host-owned variables remain a host concern.

Wrapper rules apply before delegation, then again to every delegated executable.
Only these exact forms are supported; unlisted flags, clusters, missing operands
and dynamic selectors deny:

| Wrapper | Supported operands before command |
| --- | --- |
| `env` | Repeated `-i`/`--ignore-environment`, `-u NAME`/`--unset=NAME`, `-C DIR`/`--chdir=DIR`; optional `--`, static ASCII `NAME=value` assignments. No command is an environment-only operation. |
| `command` | Optional exact `-p`, optional `--`, required command. Query modes `-v`/`-V` deny. |
| `exec` | Optional `--`, required command. |
| `sudo` | Repeated `-n`, `-E`, `-H`; `-u USER`/`--user=USER`, `-g GROUP`/`--group=GROUP`, `-D DIR`/`--chdir=DIR`; optional `--`, static assignments, required command. |
| `timeout` | Repeated `--foreground`, `--preserve-status`, `-v`/`--verbose`; `-s SIGNAL`/`--signal=SIGNAL`, `-k DURATION`/`--kill-after=DURATION`; optional `--`, required duration and command. Durations are digits, optional fractional digits, optional `s/m/h/d`; signals are positive decimal or ASCII names. |
| `sh`, `bash` | Repeated exact `-e`, `-u`, `-x`, then exact `-c SCRIPT` or `-lc SCRIPT`, optional static `$0`, then positional data. SCRIPT must be literal and empty or begin with neither `-` nor `+`. Nested language follows the named shell. |

`env -S`, shell/login/edit sudo modes, option-shaped sudo command selectors,
script files, stdin/interactive shell
modes, shell `--` before `-c`/`-lc`, and option-shaped SCRIPT operands are opaque.
`sh -- -c ...` can execute a file named `-c`; it is never treated as an inline
script. Trailing positional data remains subject to nested-execution checks.

Structural builtin denials apply in both dialects, including POSIX bindings
executed by a Bash-based `sh`: `eval`, `.`, `source`, `alias`, `unalias`, `builtin`,
`enable`, `trap`, `fc`, `history`, `bind`, `complete`, `compgen`, `read`, `unset`,
`getopts`, `mapfile`, `readarray`, `declare`, `typeset`, `local`, `export`,
`readonly`, and `let`. These can reinterpret operands or shell state. `printf`
with arguments requires a static first argument that is exact `--` or a non-option
format; `printf -v` and a dynamic first argument deny. `test` and `[` require
static operands, reject `-v`/`-R`, and `[` requires a final `]`. These are
unsupported-analysis boundaries, not a built-in dangerous-command catalog.

All fifteen `Limits` values are required and positive. Binding/rule counts,
aggregate bytes per rule (including its ID), prefix argument counts, raw JSON
bytes, JSON depth/nodes (keys count), decoded script bytes, total recursively
parsed script bytes, AST nodes/depth, words, decoded word bytes, delegation depth,
and per-mount in-flight analyses are bounded. `MaxAnalysisBytes` must be at least
`MaxCommandBytes`. Nested scripts never reset budgets. Covered inputs must be
one JSON object with EOF, unique keys at every level, a string command field,
and valid visible UTF-8 without NUL. Unknown fields consume JSON budgets.
Eino may repair invalid original wire bytes before this guard sees them.

Parsing is synchronous and uses a fresh parser for each admitted analysis.
Saturation returns a fixed capacity denial immediately; there is no queue.
Cancellation checks occur between bounded reader calls and traversal steps, and
permits return only after work ends. The parser has no pre-construction AST depth
limit: script byte caps and admission bound its inputs, while AST limits apply
after construction. These limits and context deadlines are not hard CPU, stack,
process-memory, or real-time guarantees.

The example settings above were measured on macOS/arm64, Apple M4 Max, Go 1.26.3:
ordinary complete analysis allocated about 7.7 KB/op; nested `-c` analysis about
20 KB/op. Parsing a 4,007-byte script with 2,000 nested parentheses, before AST
limits, allocated about 342 KB/op; full bounded analysis allocated about 367 KB/op.
A 500-level substitution fixture allocated about 200 KB/op during parsing.
Sampling during parser reads measured about 4 MB of process stack growth for
the parentheses fixture and 1 MB for substitutions in a fresh parsing goroutine.
The resource test rejects these fixtures if allocation exceeds 4 MB or sampled
stack growth exceeds 8 MB; these are regression checks for the example inputs.
Four admitted analyses can retain several such stacks concurrently. Saturated
admission allocated zero bytes/op. These are fixture measurements,
not throughput or worst-case memory guarantees. Re-run
`go test ./commandguard -run '^$' -bench . -benchmem` for the host's environment.

Zero scope selects global policy; `extension.SessionScope` selects one exact
durable session. Zero order selects `runtime.OrderHostPolicy`. Acquired plans
continue enforcing after deactivation. Future plans exclude the guard; Close
waits for acquired leases, can time out and retry, and is idempotent after success.
Resume checks saved pending input without rerunning prepare transforms; running
calls are interrupted and terminal calls skipped. Later mounts do not join an
old saved plan. Eino's Resume settles recovery work and ends interrupted; the
host can start a subsequent model turn using the durable results.

Added diagnostics are fixed: `command policy denied: rule-match`,
`invalid-command`, `unanalysable-command`, `analysis-limit`, or `capacity`
(with the same prefix). Internal failures use `command policy failed: internal`;
actual cancellation returns the context error. Denial code is always
`command_policy_denied`, but Eino persists the protected result derived from the
message, not a separate guard-code field. Diagnostics never echo rules, command
text, paths, parser errors or panic values. Normalized command input is already
stored before the guard runs; result redaction does not erase it.

This is trusted native syntax inspection, **not a sandbox**. It performs no
process execution, workspace reads, environment resolution, credential access,
network calls or configuration discovery. Basenames do not certify executable
identity. Inherited environment, profiles, aliases/functions, executable contents,
external scripts and arbitrary interpreter languages remain host concerns. Hosts
must choose honest tool fields/dialects and retain their permission/process policy.
PowerShell, cmd.exe, Zsh, project rule discovery, hot reload and Wasm are outside
this package.

Run the portable, subprocess-free example with
`GOWORK=off go run ./examples/command-guard`. It uses a scripted model, typed host
tool, real composition and SQLite, and prints one allowed and one denied outcome.
Unix integration tests separately exercise the actual standard shell and
background-job bindings. CI runs these on Linux/macOS with verified Bash 5+ for
implicit-builtin references, and compiles the guard and example for Windows.

The parser dependency is `mvdan.cc/sh/v3@v3.14.1` (BSD-3-Clause), imported only
through `syntax`. Its module graph selects `golang.org/x/sys@v0.47.0`.
Production catalog tests directly import Eino's existing `eino-tools` pin and
its `doublestar/v4` dependency; Eino Agent v0.3.3 and Go 1.26.3 remain unchanged.
