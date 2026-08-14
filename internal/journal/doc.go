// Package journal records every request a provider listener handled, with
// credentials removed at the storage boundary and retention bounded.
//
// Two properties define the package.
//
// Redaction is not optional and not a caller's responsibility. [Ring.Append]
// runs [Redact] over the entry it is handed before anything is retained, so a
// handler that forgets to redact still cannot leak. [Redact] is idempotent,
// which is what lets provider.Handle redact where the entry is built — so its
// own copy, the one the logger serialises, is clean too — and lets Append
// redact again where the entry is stored.
//
// Retention is bounded and deterministic. A [Ring] keeps at most a fixed number
// of entries and at most a fixed number of body bytes per entry, evicting the
// oldest entry when it is full and counting the eviction in [Stats]. Nothing
// here reads a clock or a random source: sequence numbers come from an atomic
// counter and [Ring.Snapshot] returns entries in append order, so a replayed
// scenario journals identically.
//
// The package sits below the provider seam and must stay there.
// [Entry.Provider] is a plain string rather than a provider.Name precisely so
// that provider can import journal and journal never imports provider.
package journal
