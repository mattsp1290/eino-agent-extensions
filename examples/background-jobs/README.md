# Background jobs example

This credential-free example mounts the native component with `/bin/sh`, an
explicit non-secret shell identity, explicit-only environment, a host-rotated
non-secret environment identity, and finite concurrency, tracking, input,
output, environment, timeout, TERM-grace, and KILL-wait limits. It acquires a
real frozen Eino plan, starts a harmless command, polls status, and demonstrates
an optional one-second timeout without a model provider or network access.

Linux and Darwin are supported. The command's initial working directory is
resolved beneath the runtime workspace, but that validation is not a
filesystem, network, credential, process, container, or operating-system
sandbox. Commands must not contain credentials because Eino durably records
canonical tool input. Output is an in-memory bounded tail and disappears with
the host process.

Run it with:

```sh
GOWORK=off go run ./examples/background-jobs
GOWORK=off go test ./examples/background-jobs
```
