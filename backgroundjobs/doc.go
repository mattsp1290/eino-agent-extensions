// Package backgroundjobs mounts four Eino tools for starting, polling, listing,
// and killing bounded non-interactive shell jobs.
//
// The tools are background_job_start, background_job_status,
// background_job_list, and background_job_kill. A mount freezes an explicit
// absolute shell, a non-secret shell identity, environment policy, finite
// resource limits, and a required finite maximum timeout. A per-job hard
// deadline remains optional when the default timeout is zero. Explicit-only environment uses
// only overrides; inherit-and-override snapshots the ambient environment once
// and overlays overrides. The host must rotate the non-secret environment
// identity after every effective environment change.
//
// Omitted or zero per-job timeouts use the host default; a zero default disables
// automatic timeout. Positive overrides cannot exceed the required maximum.
// Jobs and raw output tails exist only in memory; Eino may durably retain the
// short tool calls and results. Commands therefore must not contain credentials.
// Jobs are visible only to the creating session/workspace owner, and output is a
// bounded text tail rather than a complete transcript. Commands, environment
// values, and output are excluded from package diagnostics and permission
// identities. Hosts may mount toolresultredactor as a final result transform.
//
// On Linux and Darwin a package supervisor anchors each launched process group
// through final TERM/KILL signaling and is reaped before termination succeeds.
// Working-directory validation only constrains the initial directory: this
// package is not a filesystem, network, credential, process, or OS sandbox, and
// deliberately detached processes are outside its containment promise.
// Unsupported platforms reject configuration explicitly.
package backgroundjobs
