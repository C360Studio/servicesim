// Package acme is a worked example of an out-of-tree Servicesim profile: a
// vendor invented for this repository's own migration proof, written against
// nothing but github.com/c360studio/servicesim/provider and
// github.com/c360studio/servicesim/scenario — the two packages a real
// profile author reads. docs/building-a-profile.md's code blocks are literal
// excerpts of these files (see that file's own top for the excerpt
// convention); the root module's docs_test.go fails the moment the guide
// and this package drift apart, so the guide can never describe code that no
// longer exists.
//
// No file in this package imports servicesim/internal/... — imports_test.go
// (this module's root) proves it the same way examples/imports_test.go does
// for the in-tree examples, and Go's own import rules make it true for any
// consumer outside this repository regardless.
//
// # What Acme simulates
//
// Two routes on one listener: POST /v1/answer takes a documented required
// "query" string and returns a derived request_id plus a scripted answer;
// GET /v1/status takes no body and returns the same request_id shape plus a
// scripted status string. Both authenticate with Authorization: Bearer,
// required by default (Profile.DefaultAuth).
//
// Both read the SAME scripted fault plan — the one providers.acme.fault
// block, reached through each Route.Fault's provider.TurnFault — but each
// keeps its own attempt cursor under its own FaultKey (FaultKeyAnswer,
// FaultKeyStatus). So a 429 consumed by POST /v1/answer does not advance
// where GET /v1/status sits in that plan: both routes see attempt 0 first.
// What is independent is the cursor, not the script. A vendor whose two
// routes should be scripted independently gives each its own scenario entry
// (Route.Entry) and therefore its own fault block, the way profiles/tavily's
// /search and /research do.
//
// Both routes are served from ONE scenario entry, "acme" (Route.Entry is
// left empty on each, which defaults to the listener's own name): a real
// vendor with two routes that document genuinely independent behaviour would
// more likely give each its own entry the way profiles/tavily's /search and
// /research do (Route.Entry, and profiles/tavily/handler.go's researchRoutes
// for the worked example) — Acme shares one to keep this package small
// enough to read end to end, and projectionBody's own doc comment names which
// fields each handler reads.
//
// # What it does not
//
// Acme is not a real vendor. Its "contract" (contracts/README.md) says so:
// the spec: block in contracts/provenance.yaml records a placeholder
// document's hash, not a fetched vendor specification, because there is no
// vendor to fetch one from — see that file's own header for what a REAL
// profile's spec: block records instead, and contracts/README.md "Keeping
// them honest" in the root of this repository for the procedure a dated
// re-verification follows.
//
// Acme also does not demonstrate: a second scenario entry per listener
// (profiles/perplexity's Sonar/Agent split is the worked example), an
// asynchronous create-then-poll surface (profiles/tavily's /research), or
// streaming (profiles/perplexity and profiles/mcp). docs/building-a-profile.md
// points at each of those by name rather than repeating them here in
// miniature — a profile with every reference profile's feature folded into
// one fifteen-field struct would teach the union of four vendors, not how to
// write one.
//
// # Decisions a real profile author has to make, that Acme made one way
//
//   - Two routes sharing one scenario entry (above) — a real vendor with
//     routes that behave independently would more likely give each its own
//     entry.
//   - DefaultAuth: required — MCP's own default is optional
//     (provider.Profile.DefaultAuth's own doc comment explains why: a
//     protocol's own transport-level checks, not an API key, gate access).
//     Pick the one that matches what the real vendor documents.
//   - One error envelope for every refusal kind, including the framework's
//     own 404/405 (errors.go's ErrorBody) — every reference profile in this
//     repository does the same, because a consumer's error-path test should
//     not need five envelopes for one vendor's four failure shapes.
package acme
