package tavily

import (
	"io/fs"

	"github.com/c360studio/servicesim/contracts"
	"github.com/c360studio/servicesim/provider"
)

// defaultPort is Tavily's default listener port. It lives here rather than
// in internal/config (Phase 10 unit 3: "config no longer names a vendor") —
// this profile is the one place that gets to know it binds 8082 by default,
// and internal/config derives every port flag's default from Profile.Port
// instead of naming one.
const defaultPort = 8082

// Profile returns the registration record Tavily is served from: everything
// New used to build by hand, plus the fields New never needed because
// internal/config and internal/server supplied them from their own
// four-vendor switches.
func Profile() provider.Profile {
	sub, err := fs.Sub(contracts.FS(), "tavily")
	if err != nil {
		// Unreachable: "tavily" is a fixed, valid fs.Sub pattern this package
		// has embedded goldens under since before this profile existed.
		panic("provider/tavily: contracts sub-FS: " + err.Error())
	}

	return provider.Profile{
		Name:    Name,
		Title:   "Tavily",
		Summary: "the Tavily search API",
		Port:    defaultPort,

		Handlers: handlers(),
		Routes:   Routes(),
		Validators: map[string]provider.Validator{
			string(Name): Validator{},
			NameResearch: ResearchValidator{},
		},
		ErrorBody: refusalBody,
		// DefaultAuth is left "" (scenario.AuthRequired): provider/tavily/request.go's
		// checkAuth defaults an entry with no auth: block to AuthRequired.

		Contracts: sub,
		Hosts: []string{
			"api.tavily.com", // servicesim:allow-live-host -- the DENYLIST entry Profile.Hosts exists to carry; never dialled.
			"tavily.com",     // servicesim:allow-live-host -- see above.
		},
		DerivedIDs:      []string{"request_id"},
		CredentialNames: []string{"Authorization", "api_key"},
	}
}

// refusalBody renders a provider.Refusal in Tavily's one documented error
// envelope, {"detail":{"error":"…"}}. It is what handler.go's now-deleted
// inline NotFound/MethodNotAllowed handlers and
// internal/server/listeners.go's now-deleted scenarioNotFoundBody built by
// hand before this unit.
func refusalBody(r provider.Refusal) []byte {
	switch r.Kind {
	case provider.RefuseNotFound:
		return errorBody(MessageNotFound)
	case provider.RefuseMethodNotAllowed:
		return errorBody(MessageMethodNotAllowed)
	case provider.RefuseScenarioUnknown:
		return errorBody(MessageNotFound)
	case provider.RefuseInternal:
		return errorBody(MessageInternalServerError)
	case provider.RefuseRequest:
		if r.X == nil {
			return errorBody(MessageBadRequest)
		}
		// errorResponse already derives the documented text from r.X's
		// findings, in the same order and with the same precedence Reject's
		// caller used to decide r.Status; reusing it here is what keeps the
		// two from being two independent readings of one classification.
		return errorResponse(r.X).Body
	default:
		return errorBody(MessageInternalServerError)
	}
}

// Name is this listener's identity in a provider.Set and the scenario
// provider entry's key. It was already exported and untyped before this
// unit (framework-seam.md: "tavily.Name and mcp.Name exist (untyped
// today; they become typed)"); this typed constant replaces the now-deprecated
// provider.Tavily, which named the same listener as an untyped string
// constant on the wrong package.
const Name provider.Name = "tavily"
