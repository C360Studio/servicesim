// Package faults selects deterministic per-attempt failures. It does not touch
// the wire: execution lives in package provider, which calls it, and which this
// package imports.
//
// The split is forced rather than stylistic. Selection is stateful — it owns the
// per-key attempt counters — and that state must not be reachable from a consumer
// importing provider/exa, so it lives here in internal. Execution is called by
// provider.Handle, so it must live at or below provider; putting it here would
// require provider -> internal/faults -> provider, which does not compile.
//
// [Engine] is the only implementation of provider.Faults in the module.
// internal/server and testkit construct one and assign it to provider.Deps.Faults.
//
// Two properties define it, and both are documented at length in
// docs/design/package-design.md §4.
//
// The key set is fixed at construction. [New] takes the routes whose budgets the
// engine must serve and pre-creates a counter for every route's FaultKey,
// including keys whose plan is empty. Both maps are therefore read-only
// afterwards, which is what reduces the request path to a single atomic add with
// no mutex anywhere. Routes that are aliases of one operation share a key and so
// share a budget.
//
// The counter counts arrivals, not completions. An index is claimed before the
// response is written, so two concurrent requests against a plan of [429, 200]
// receive indices 0 and 1 and may complete in either order. A test that needs a
// strict sequence must issue its requests serially. This is stated loudly because
// the alternative is an intermittently failing test that looks like a simulator
// bug.
package faults
