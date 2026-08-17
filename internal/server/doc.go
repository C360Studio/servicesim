// Package server composes handlers, binds listeners and manages lifecycle.
//
// It names no profile package (Phase 10 unit 3, docs/proposals/
// framework-seam.md): everything it used to wire by importing profiles/exa,
// profiles/tavily, profiles/perplexity and profiles/mcp by hand — the fault
// engine's key set, the projection validator registry, every listener's
// handler — it now derives from the single [provider.Set] its [config.Config]
// carries, through [provider.Set.Faults], [provider.Set.Validators] and
// [provider.Set.Lookup]. The shared journal is still wired here, because it
// is process-wide rather than per profile. Everything below this level is
// deliberately ignorant of which providers this build ships; so, now, is
// this one.
//
// # Startup order is the acceptance criterion
//
// [New] performs the whole of acceptance criterion 4 — "startup scenarios are
// deterministic and validated before readiness succeeds" — in a fixed order:
//
//  1. Open the configured scenario. A mounted file is resolved through
//     config.OpenScenario, which is os.Root-confined; a "builtin:" scenario is
//     read out of the binary. scenario.Load is never called, because it performs
//     no path containment.
//  2. Decode and validate the envelope. scenario.Parse checks the schema
//     version, source references, turn ordering and fault plans. An error
//     finding fails the process rather than binding a listener.
//  3. Validate the projection bodies. provider.ValidateScenario hands each
//     provider entry to the handler registered for its kind, which decodes the
//     `respond:` node the scenario package deliberately left undecoded. An error
//     finding here also fails the process, so a fixture with a bad field is
//     found at boot rather than on a consumer's first call.
//  4. Only then does [Server.Run] bind, and only once every enabled listener is
//     accepting does readiness report true.
//
// A provider named in the scenario that this build has no handler for is a
// warning, never an error: a scenario file shared across repositories must not
// break the moment one consumer pins an older Servicesim, or runs a subset of
// the listeners.
//
// # One process, several listeners
//
// Exa and Tavily both serve POST /search, so preserving each vendor's real path
// requires one listener per provider. They run concurrently in one process over
// one journal and one fault engine, which is what lets /__admin/requests prove
// that a fusion adapter's three calls overlapped.
//
// The admin listener binds first, so GET /healthz answers while the provider
// listeners are still binding, and it is shut down last, so a container's health
// probe keeps getting an answer for as long as anything is still draining.
//
// # Logging
//
// The per-request event is emitted by provider.Handle from the journal entry, so
// it is credential-free by construction: redaction happens at the storage
// boundary, before anything is retained. This package supplies the logger and
// attaches the scenario name to it, so every request event carries provider,
// scenario, sequence, route, outcome, fault, finding counts and duration.
package server
