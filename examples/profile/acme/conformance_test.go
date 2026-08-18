package acme_test

import (
	"testing"

	"example.test/acmesim/acme"

	"github.com/c360studio/servicesim/testkit"
)

// TestValidateProfile runs the same conformance suite each in-tree reference
// profile's own profiles/<name>/conformance_test.go calls
// (docs/proposals/framework-seam.md, "the eleven CONTRIBUTING.md
// conventions"), proving it runs against an out-of-tree profile with no
// modification: NewSet, Contracts (contracts.Conform over acme's own
// embedded bundle), ErrorBody, UnknownPath, WrongMethod, WrongContentType,
// MissingCredential, FaultKeysResolve, RejectionDoesNotClaimAnAttempt,
// Deterministic and RenderShape.
func TestValidateProfile(t *testing.T) {
	testkit.ValidateProfile(t, acme.Profile())
}
