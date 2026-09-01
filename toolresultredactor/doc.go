// Package toolresultredactor provides a bounded native Eino transform that
// sanitizes callback-admitted tool-result strings before settlement.
//
// A global mount scans all tools by default. ExcludedTools entries are exact,
// case-sensitive host choices. The conservative versioned built-in catalog is
// incomplete; positive-width host RE2 patterns extend it. Every Limits value
// is required. Matching value spans are replaced with Placeholder. Unsafe
// scalars and the containing Structured, metadata-map, or attachment-slice
// fields of unsafe keys and collection limits receive fixed placeholder
// representations. JSON and map keys are scanned but never rewritten, and
// non-string JSON values are not scanned.
//
// The package is trusted in-process code verified against eino-agent v0.1.3.
// It uses no external scanner, subprocess, network, credential, or dynamic
// reload. Package budgets cannot bound tool allocation or Eino's pre-callback
// clone. It scans no external attachment content, prompts, inputs, model
// responses, logs, host storage, or previously persisted records.
//
// When full-result notice protection is required, a host must keep the
// redactor as the final ToolResultTransformPoint callback. A failing predecessor
// skips it, while a failing successor makes Eino v0.1.3 restore the original
// pre-waterfall result. Either case may expose that original result to trusted
// full-result observers even though durable/model-visible settlement is generic.
// Eino also rejects syntactically invalid non-empty Structured JSON
// before this callback, so that result and its siblings are outside this
// package's guarantee. Deactivation affects new plans; Close drains acquired
// plans, and removing a component can make unfinished runs fail exact resume.
package toolresultredactor
