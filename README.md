# Eino Agent Extensions

This repository contains focused extensions for
[`github.com/mattsp1290/eino-agent`](https://github.com/mattsp1290/eino-agent).
Its first package is a trusted native tool-result secret redactor verified
against Eino Agent v0.1.3.

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
	_ = mount.Close(closeCtx)
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

After result transforms complete, Eino v0.1.3 may reapply its fixed,
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
redactor, while a failing later transform makes Eino v0.1.3 restore the original
pre-waterfall result. Durable and model-visible settlement is generic in either
case, but a trusted `ToolSettledPoint` observer can receive the original full
result. Other native transforms must return sanitized success when full-result
notice protection is required.

Non-empty syntactically invalid `Structured` JSON is rejected by Eino v0.1.3
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
