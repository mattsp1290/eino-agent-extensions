// Package workspaceinstructions contributes trusted workspace instruction files
// as one named Eino system-prompt section.
//
// A host resolver decides which workspace is trusted for each prompt call. The
// package then reads configured file names from the host-supplied boundary down
// to the workspace root, using an os.Root for every filesystem access. Files are
// re-read on every call and are never cached.
//
// Rendered instructions are trusted prompt content, not sandboxed input. They
// become part of Eino's durable model-request audit records. Hosts must not put
// secrets in instruction files and must drain acquired plans before changing or
// removing a mount.
package workspaceinstructions
