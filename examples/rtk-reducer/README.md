# RTK reducer example

This example mounts `rtkreducer` with an explicitly trusted RTK v0.48.0
executable, runs a synthetic JSON-native Go-test tool through a real Eino
registry/orchestrator and temporary SQLite store, and prints bounded byte
counts plus a short reduced result.

Provision RTK independently from the [official v0.48.0 release](https://github.com/rtk-ai/rtk/releases/tag/v0.48.0), verify its Apache-2.0 license, then run:

```text
go run ./examples/rtk-reducer \
  -rtk /absolute/path/to/rtk \
  -rtk-sha256 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
```

The digest is the extracted executable's lowercase SHA-256, not the release
archive digest. The mount validates the path, digest, private temporary root,
Linux/macOS platform, and finite limits before registering. It does not invoke
RTK during mount and does not install, update, initialize, or discover it.

The example's binding is opt-in and exact: only `synthetic_go_test` input
`{"cmd":"synthetic-go-test"}` selects JSON-mirrored `stdout` for the fixed
`go-test` filter. A host may instead use `TextOnly` for `Output`, or bind a
dedicated tool whose output contract is known. Standard shell integration is a
separate test; an exact binding for the standard shell's JSON event stream is:

```go
rtkreducer.Binding{
    ToolName: "shell",
    Mode: rtkreducer.JSONMirror,
    MatchInput: &rtkreducer.InputMatch{
        Field: "cmd", Equals: []string{"go test -json ./..."},
    },
    Fields: []rtkreducer.Field{{Name: "stdout", Filter: rtkreducer.GoTest}},
}
```

The host is responsible for asserting that selected text has the filter's
expected format. Reduction is lossy and only accepted when the marked encoded
result is strictly smaller; no-gain, malformed, canceled, capacity-exhausted,
or failed reductions retain the original result. The marker reports measured
bytes; RTK's own token estimate is not a billing or compression guarantee.

Keep the verified executable immutable for the mount lifetime and mount
`toolresultredactor` after this reducer when retained content needs secret
redaction. RTK startup/config side effects are directed into private child
directories; the extension owns only its direct child, pipes, and private
directories, not arbitrary descendants or the host workspace. There is no raw
output artifact or retrieval API. Config, artifact, binding, limit, or RTK
digest changes make strict Eino resume fail; do not edit saved fingerprints.
