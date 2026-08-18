package mcp

import (
	"testing"

	"github.com/c360studio/servicesim/testkit"
)

// TestConformance is the call an adopter's own CI makes against their own
// profile (docs/proposals/framework-seam.md, "one conformance_test.go per
// profile"). MCP is held to exactly the discipline an out-of-tree profile
// is: nothing here is a special case testkit.ValidateProfile does not also
// run for profiles/exa, profiles/tavily, profiles/perplexity and the
// out-of-tree acme fixture — including MCP's own opposite DefaultAuth
// default, which testkit.ValidateProfile's MissingCredential check skips,
// named, rather than assuming every profile is auth-required.
func TestConformance(t *testing.T) {
	t.Parallel()
	testkit.ValidateProfile(t, Profile())
}
