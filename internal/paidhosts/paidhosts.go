// Package paidhosts holds the framework's own hand-kept list of paid vendor
// hostnames — the base half of the "base list ∪ --print-hosts" pattern
// docs/proposals/framework-seam.md's rule 3 describes.
//
// It exists so a base host is refused whether or not any registered profile
// names it: house rule 3's guard must not "delete the one vendor the sem*
// workload is about to need" (framework-seam.md) just because no profile has
// been written against it yet — api.openai.com is the shipped example, servicesim:allow-live-host
// no in-tree profile serves it, and it stays in Base regardless.
//
// This is the single Go source of the list. scripts/lint-no-live-hosts.sh
// keeps its own copy as a literal bash string, for CI speed (a shell script
// cannot import a Go package) — internal/paidhosts/paidhosts_test.go is what
// keeps the two from drifting: it parses the script's BASE_PATTERN line and
// fails if its host set is not exactly Base's.
package paidhosts

// Base is the framework's hand-kept list of paid vendor hostnames. Adding an
// entry here means updating scripts/lint-no-live-hosts.sh's BASE_PATTERN in
// the same change — TestBaseMatchesGuardScript fails loudly otherwise.
var Base = []string{ // servicesim:allow-live-host -- the DENYLIST this package exists to carry; never dialled.
	"api.exa.ai", "exa.ai", // servicesim:allow-live-host -- see above.
	"api.tavily.com", "tavily.com", // servicesim:allow-live-host -- see above.
	"api.perplexity.ai", "perplexity.ai", // servicesim:allow-live-host -- see above.
	"api.openai.com", // servicesim:allow-live-host -- see above.
}
