// Package servicesim is the composition root: everything a binary needs to
// serve a [provider.Set] as a process — flags, listeners, the admin surface,
// the journal, scenario loading, health checks and startup logs — built from
// nothing but the Set a caller registers. It names no vendor: the four
// reference profiles this repository ships (profiles/exa, profiles/tavily,
// profiles/perplexity, profiles/mcp) are consumed by [cmd/servicesim/main.go]
// exactly the way an out-of-tree profile is consumed by anyone else's.
//
// A consumer's whole main.go is:
//
//	func main() {
//		os.Exit(servicesim.Main(
//			servicesim.Build{Program: "acmesim", Version: version},
//			provider.MustSet(acme.Profile(), exa.Profile()),
//		))
//	}
//
// with their own profile (or any of ours) in the [provider.Set]. [Main] and
// [Run] give that binary the same flags, listeners, journal, scenario
// loading, health check and startup logs this repository's own binary has.
//
// The one thing this package does not offer, on purpose, is an admin route
// that mutates scenario state. The admin surface — /healthz, /readyz,
// /__admin/requests and the rest — is framework-owned and closed: [Build] and
// [Run] accept no admin-route field, and [provider.Profile] has none either.
// House rule 6 (CLAUDE.md, "process isolation over shared mutable state") is
// why: a mutable admin API is hidden shared state between concurrent test
// suites, and the flakes it produces are miserable to debug. A design that
// let a consumer add one would trade that guarantee for a PR-review gate this
// package cannot enforce out of tree (docs/proposals/framework-seam.md, house
// rule 6 and owner decision D-3).
package servicesim
