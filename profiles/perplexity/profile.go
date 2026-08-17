package perplexity

import (
	"embed"
	"io/fs"

	"github.com/c360studio/servicesim/provider"
)

// defaultPort is Perplexity's default listener port. It lives here rather
// than in internal/config (Phase 10 unit 3: "config no longer names a
// vendor") — this profile is the one place that gets to know it binds 8083
// by default, and internal/config derives every port flag's default from
// Profile.Port instead of naming one.
const defaultPort = 8083

// contractsFS embeds this profile's own golden fixtures, provenance record
// and README — exactly the bundle an out-of-tree profile embeds beside its
// own package (Phase 10 unit 6: "theirs, embedded beside their package").
// It used to be a sub-FS of the whole-repository contracts.FS(); genericising
// contracts around fs.FS let the bundle move to
// profiles/perplexity/contracts and this package own its embedding, the way
// profiles/perplexity/contracts/README.md's verification record has always
// been Perplexity's own, not shared.
//
//go:embed contracts
var contractsFS embed.FS

// Name is this listener's identity in a provider.Set. It is the canonical
// replacement for the deleted provider.Perplexity constant (Phase 10 unit 4)
// (framework-seam.md: "a framework core has no business naming four
// vendors") and is deliberately a separate spelling from [NameSonar] and
// [NameAgent], which stay as entry-KIND names: a listener may serve more
// than one scenario entry, and Name is the listener's own identity, not
// either surface's.
const Name provider.Name = "perplexity"

// Profile returns the registration record Perplexity is served from:
// everything New used to build by hand, plus the fields New never needed
// because internal/config and internal/server supplied them from their own
// four-vendor switches.
func Profile() provider.Profile {
	sub, err := fs.Sub(contractsFS, "contracts")
	if err != nil {
		// Unreachable: "contracts" is the fixed, valid fs.Sub pattern the
		// //go:embed directive above always populates.
		panic("profiles/perplexity: contracts sub-FS: " + err.Error())
	}

	return provider.Profile{
		Name:       Name,
		Title:      "Perplexity",
		Summary:    "the Perplexity Sonar and Agent APIs",
		Port:       defaultPort,
		Handlers:   handlers(),
		Routes:     Routes(),
		Validators: validators(),
		ErrorBody:  refusalBody,
		// DefaultAuth is left "" (scenario.AuthRequired): profiles/perplexity/request.go's
		// checkAuth defaults an entry with no auth: block to AuthRequired.
		Announce: announce,

		Contracts: sub,
		Hosts: []string{
			"api.perplexity.ai", // servicesim:allow-live-host -- the DENYLIST entry Profile.Hosts exists to carry; never dialled.
			"perplexity.ai",     // servicesim:allow-live-host -- see above.
		},
		DerivedIDs:       []string{"id"},
		StreamDerivedIDs: []string{"response.id", "item.id", "item_id"},
		CredentialNames:  []string{"Authorization"},
	}
}

// refusalBody renders a provider.Refusal in Perplexity's own shape — the
// func(Refusal) []byte Profile.ErrorBody needs; named apart from this
// package's own errorBody(surface, status, message), which it is built
// from.
//
// An unmatched path cannot know which of the two surfaces was intended, so
// fail-closed routing — RefuseNotFound, RefuseMethodNotAllowed and
// RefuseScenarioUnknown alike — uses the Sonar-shaped body, exactly as
// handler.go's now-deleted inline NotFound/MethodNotAllowed handlers and
// internal/server/listeners.go's now-deleted scenarioNotFoundBody did.
func refusalBody(r provider.Refusal) []byte {
	switch r.Kind {
	case provider.RefuseRequest:
		if r.X == nil {
			return errorResponse(surfaceSonar, r.Status, "").Body
		}
		// validationResponse already derives the documented 422 body, or a
		// surface's ordinary envelope for any other status, from r.X's
		// findings — the same classification Reject's caller used to decide
		// r.Status.
		return validationResponse(surfaceSonar, r.X.Findings(), sonarFields).Body
	default:
		return errorResponse(surfaceSonar, r.Status, "").Body
	}
}
