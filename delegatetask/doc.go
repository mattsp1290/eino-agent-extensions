// Package delegatetask mounts one bounded synchronous delegate_task tool.
//
// The package is a bridge to a trusted, host-supplied Runner. Profile is an
// opaque, non-secret routing identifier: only the Runner decides what a
// profile means and enforces model, tool, filesystem, network, credential,
// resource, and isolation policy. This package does not create child agents or
// provide a sandbox.
//
// Task input and successful Result values travel through Eino's ordinary
// durable tool-call path and must not contain secrets. Runner side effects are
// not transactional: cancellation, timeout, or a host crash does not roll them
// back.
package delegatetask
