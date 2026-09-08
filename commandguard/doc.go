// Package commandguard mounts a trusted native, deny-only command policy through
// Eino composition. It inspects final prepared input before permissions and tool
// execution. A complete nonmatch abstains; it never grants execution authority.
//
// Rules match case-sensitive POSIX slash-separated executable basenames and
// exact positional argument prefixes. Paths are never resolved. For example,
// a git/push rule matches /usr/bin/git push but does not match git -C repo push.
// Empty prefixes match all invocations; empty quoted argument tokens are real
// tokens. Unresolved executables and possibly matching unknown prefixes deny.
// Unrelated dynamic data is allowed after its nested executable syntax is checked.
//
// Nil bindings select shell/cmd and background_job_start/command in POSIX mode.
// A nonnil list replaces those defaults; append to DefaultBindings to extend
// them. Custom fields are literal top-level keys. Bash is an explicit syntax
// selection, not a platform or executable-resolution promise.
//
// Analysis walks every supported compound branch, substitution, assignment and
// redirection, including expandable heredocs. Functions, arithmetic, arrays,
// extended globs, non-simple parameter expansions and other unsupported
// execution grammar deny. Extended-glob patterns are literal parser payloads
// whose nested execution cannot be inspected by walking the AST.
// Quoted literal heredocs remain data. Ordinary shell quoting is removed without
// expansion; ANSI-C and locale quoting remain unknown. CR bytes are preserved
// despite the pinned parser's CRLF normalization, using a length-preserving
// internal mask that is restored during literal decoding.
//
// Bounded wrappers are env, command, exec, sudo, timeout, and literal sh/bash
// -c or -lc scripts. Only the exact option forms documented in the repository
// README are supported. Shell -- before mode selection, option-shaped script
// operands, script files, and dynamic selectors deny. Nested scripts share all
// resource counters and select their named shell's dialect.
// Assignments, loop targets, and env/sudo assignment operands reject shell-owned
// arithmetic, prompt, and startup state, including all BASH_* targets.
// The exact inventory is documented in the README. This applies in both dialects
// and preserves ordinary scalar variables; their inherited attributes are a
// host concern. Quoted values assigned to special targets can execute later.
//
// Builtins that reinterpret operands or shell state (including eval, source,
// trap, variable-target builtins, aliases and completion/history builtins) are
// opaque. printf requires a static non-option first argument or exact --;
// test and [ require static operands and reject -v/-R queries. These structural
// denials implement incomplete-analysis handling, not a dangerous-command list.
//
// Every Limits field is required and positive. Parsing is synchronous with a
// fresh parser per call. Byte bounds and per-mount admission limit construction
// inputs; AST limits apply only after construction. Cancellation is cooperative
// between reads and traversal steps, with no detached parser work or hard CPU,
// memory, or deadline guarantee. Saturation denies immediately without queuing.
//
// Mount freezes caller collections. Zero scope is global; exact durable-session
// scope is supported. Zero order is runtime.OrderHostPolicy. Acquired plans keep
// enforcement after deactivation; Eino owns release, close and exact resume
// identity. Hosts must rotate honest artifact identity for behavior changes and
// must not rewrite saved policy fingerprints to bypass recovery checks.
//
// Fixed added diagnostics contain no command, rule, parser error or panic value.
// Original normalized input is already durable: result redaction does not erase
// command input. Host configuration is non-secret. Analysis performs no process,
// filesystem, environment, credential, network or configuration discovery.
// This package is not a sandbox: profiles, inherited environment, executable
// contents, aliases, external scripts and arbitrary interpreter languages remain
// trusted host concerns governed by the underlying permission/process policy.
package commandguard
