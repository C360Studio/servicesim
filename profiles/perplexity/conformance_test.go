package perplexity

import (
	"testing"

	"github.com/c360studio/servicesim/testkit"
)

// TestConformance is the call an adopter's own CI makes against their own
// profile (docs/proposals/framework-seam.md, "one conformance_test.go per
// profile"). Perplexity is held to exactly the discipline an out-of-tree
// profile is: nothing here is a special case testkit.ValidateProfile does
// not also run for profiles/exa, profiles/tavily, profiles/mcp and the
// out-of-tree acme fixture.
func TestConformance(t *testing.T) {
	t.Parallel()
	testkit.ValidateProfile(t, Profile())
}
