// Package pythonrepl mounts a stateful Python REPL as two native Eino Agent
// tools. Interpreter state is isolated by durable session and workspace and is
// intentionally process-local: it is lost on cancellation, timeout, clear,
// runner failure, remount, restart, or resume in a new process.
//
// Python runs with the host user's authority. The package is not a sandbox:
// Python may access files, the network, processes, and every explicitly
// supplied environment value. Tool input and output are durable Eino data and
// must not contain secrets. Output and protocol frames are bounded, but Python
// memory, CPU use, side effects, and intentionally detached descendants are not.
package pythonrepl
