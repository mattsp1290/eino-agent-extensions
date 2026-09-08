// Package rtkreducer provides an opt-in, bounded tool-result transform that
// sends explicitly selected text fields to a trusted, host-provided RTK
// v0.48.0 executable on Linux or macOS. It invokes only
// "rtk pipe --filter <fixed-filter>" with payload on stdin; it never discovers,
// installs, initializes, or updates RTK and never re-executes the source tool.
//
// No binding is enabled by default. The host must assert that each selected
// field has the format expected by its fixed go-test, git-diff, or log filter
// and must keep the digest-verified executable immutable for the mount life.
// Reduction is deliberately lossy. A marked replacement is accepted only
// when the complete encoded result shrinks; malformed, canceled, saturated,
// failed, and no-gain work returns the original result without changing
// runtime status, permissions, metadata, attachments, or unselected JSON.
//
// Each direct child receives private home, configuration, data, temporary,
// and working directories without the parent's environment. Close owns the
// direct child, its pipes, and package-created directories; arbitrary
// descendants are outside the contract. The package retains no raw-output
// artifact. Hosts that need content redaction should mount their final
// redactor after this transform. Policy, limits, artifact identity, or RTK
// digest drift intentionally fails Eino's strict saved-plan resume checks.
package rtkreducer
