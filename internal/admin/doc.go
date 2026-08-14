// Package admin serves health, readiness and the redacted request journal.
//
// It is the only surface that is not provider-shaped, and it is deliberately
// small. Four properties define it.
//
// Readiness is evidence, not decoration. GET /readyz reports true only once
// [Deps.Ready] has been flipped by internal/server, which happens after the
// startup scenario has loaded, validated and resolved and every listener is
// accepting. That is acceptance criterion 4, and scripts/image-smoke.sh polls
// /readyz before it asserts anything else, so a scenario that fails validation
// fails the smoke test rather than serving a subtly wrong contract. A nil
// Ready reads as not ready: the admin surface fails closed like every other
// one.
//
// The journal is served with both timestamps. Entry.ArrivedAt and
// Entry.CompletedAt are real time from provider.Clock, and they are the
// evidence a fusion test rests on when it proves three provider calls
// overlapped rather than ran serially. They are never part of a response body
// (package-design §3.2), which is why serving them here costs no determinism.
//
// No credential reaches the wire. Redaction already happened at the storage
// boundary — journal.Ring.Append redacts before it retains — and this package
// runs journal.Redact again on the way out. That is not superstition: Redact
// is idempotent, the endpoint is not a hot path, and a Deps wired with some
// other journal.Journal implementation would otherwise be the one path by
// which a header, query parameter, body property or finding message could
// leave the process unmasked.
//
// Nothing here mutates scenario state. POST /__admin/reset clears the journal
// and the fault counters and does nothing else, per the CLAUDE.md house rule:
// a mutable admin API becomes hidden shared state between concurrent tests.
// Parallel CI isolates by process; reset is a local-development convenience.
//
// Provider routes are deliberately absent from this mux. A request to
// :8080/search is a 404 with a plain {"error":"not found"} body, which catches
// the common misconfiguration of pointing a provider base URL at the admin
// port instead of leaving it to time out against a route that half-works.
package admin
