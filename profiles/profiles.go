// Package profiles is where the four reference profiles this repository ships
// stop being distinguished code and become an ordinary consumer of the seam:
// they live in their own subpackages (exa, tavily, perplexity, mcp), each with
// its own exported Profile(), exactly the shape an out-of-tree profile has.
// This package's only job is to name the four in the order our binary and our
// tests register them.
//
// Nothing under provider/, internal/, testkit/, scenario/, contracts/ or the
// repository root may import this package or any of its subpackages
// (no_privilege_test.go proves it): a reference profile has no privilege an
// out-of-tree one lacks. cmd/servicesim and examples/ are the two places
// allowed to — the registration site, and consumers.
package profiles

import (
	"github.com/c360studio/servicesim/profiles/exa"
	"github.com/c360studio/servicesim/profiles/mcp"
	"github.com/c360studio/servicesim/profiles/perplexity"
	"github.com/c360studio/servicesim/profiles/tavily"
	"github.com/c360studio/servicesim/provider"
)

// Reference returns the four reference profiles this repository ships, in the
// order cmd/servicesim/main.go registers them: exa, tavily, perplexity, mcp.
// This is the ONE place besides main that names them — an out-of-tree binary
// lists its own profiles the same way main does, with any of these in place
// of, or alongside, its own.
func Reference() []provider.Profile {
	return []provider.Profile{
		exa.Profile(),
		tavily.Profile(),
		perplexity.Profile(),
		mcp.Profile(),
	}
}
