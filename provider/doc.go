// Package provider is the seam every Servicesim provider handler is built on. It
// owns provider identity, route declarations, the dependency set a handler is
// constructed with, the injectable clock, the fault-selection contract, fault
// execution, turn selection, projection validation, the mux builder, and the
// shared per-request lifecycle.
//
// Everything a provider package needs in order to serve a request lives here, so
// that provider/exa, provider/tavily and provider/perplexity write provider
// semantics and nothing else. Two responsibilities are here rather than where
// they might first be looked for, and both placements are forced:
//
//   - Fault EXECUTION (fault_exec.go) and fault SELECTION (fault_engine.go) are
//     both here, as one engine, because selection needs the Set's own routes: an
//     engine built from anything less could silently serve a clean 200 where the
//     scenario scripted a fault for a route it did not know about. Handle is the
//     only caller of execution. See §2.5 of the package design.
//   - Mux construction is here, in mux.go, rather than being written once per
//     provider package. The registration shape is load-bearing — a mux without a
//     method-less pattern per known path answers GET /search with 404 instead of
//     405 — and three parallel copies of it diverge on a documented status code.
//     See §5.1.
package provider
