// Package askuser provides a trusted native Eino tool for one bounded,
// host-mediated multiple-choice question. Presentation, routing, authentication,
// notifications, and responder persistence remain host responsibilities.
//
// Eino durably stores the question, fixed options, and returned answer. Hosts
// must never use this package to collect credentials or other secrets. The
// package has no built-in UI and makes no Wasm or Pi compatibility claim.
package askuser
