package exa

import (
	"io/fs"

	"github.com/c360studio/servicesim/contracts"
	"github.com/c360studio/servicesim/internal/config"
	"github.com/c360studio/servicesim/provider"
)

// Name is this listener's identity in a provider.Set, the scenario provider
// entry's key, and — since exa.Name has never had a typed spelling before
// this unit — the canonical replacement for provider.Exa, deprecated in this
// unit and deleted once internal/config and internal/server stop naming it
// (framework-seam.md: "a framework core has no business naming four
// vendors").
const Name provider.Name = "exa"

// Profile returns the registration record Exa is served from: everything
// New used to build by hand, plus the fields New never needed because
// internal/config and internal/server supplied them from their own
// four-vendor switches. Port's default is read from internal/config's own
// constant "for now" — Phase 10 unit 3 begins deriving config's ports from
// the registered Set instead.
func Profile() provider.Profile {
	sub, err := fs.Sub(contracts.FS(), "exa")
	if err != nil {
		// Unreachable: "exa" is a fixed, valid fs.Sub pattern this package
		// has embedded goldens under since before this profile existed.
		panic("provider/exa: contracts sub-FS: " + err.Error())
	}

	return provider.Profile{
		Name:    Name,
		Title:   "Exa",
		Summary: "the Exa search and answer API",
		Port:    config.DefaultExaPort,

		Handlers: handlers(),
		Routes:   Routes(),
		Validators: map[string]provider.Validator{
			string(Name):  Validator{},
			NameAgentRuns: AgentRunValidator{},
		},
		ErrorBody: refusalBody,
		// DefaultAuth is left "" (scenario.AuthRequired): provider/exa/request.go's
		// authenticate defaults an entry with no auth: block to AuthRequired.

		Contracts: sub,
		Hosts: []string{
			"api.exa.ai", // servicesim:allow-live-host -- the DENYLIST entry Profile.Hosts exists to carry; never dialled.
			"exa.ai",     // servicesim:allow-live-host -- see above.
		},
		DerivedIDs:      []string{"requestId"},
		CredentialNames: []string{"Authorization", "x-api-key"},
	}
}

// refusalBody renders a provider.Refusal in Exa's own flat {requestId, error,
// tag} error envelope (the reduced {error} shape for a 429 is unreachable
// through a refusal, which never carries that status). It is what
// handler.go's now-deleted handleNotFound/handleMethodNotAllowed and
// internal/server/listeners.go's now-deleted scenarioNotFoundBody built by
// hand before this unit.
func refusalBody(r provider.Refusal) []byte {
	switch r.Kind {
	case provider.RefuseNotFound:
		return errorBody(refusalRequestID(r), messageNotFound, TagNotFound, r.Status)
	case provider.RefuseMethodNotAllowed:
		return errorBody(refusalRequestID(r), messageMethodNotAllowed, TagMethodNotAllowed, r.Status)
	case provider.RefuseScenarioUnknown:
		return errorBody(refusalRequestID(r), messageNotFound, TagNotFound, r.Status)
	case provider.RefuseInternal:
		return errorBody(refusalRequestID(r), messageInternalError, TagInternalError, r.Status)
	case provider.RefuseRequest:
		if r.X == nil {
			return errorBody(refusalRequestID(r), messageInvalidRequestBody, TagInvalidRequestBody, r.Status)
		}
		_, tag, message := classify(r.X)
		return errorBody(unclaimedRequestID(r.X), message, tag, r.Status)
	default:
		return errorBody(refusalRequestID(r), messageInternalError, TagInternalError, r.Status)
	}
}

// refusalRequestID derives the requestId a refusal's body carries: the same
// derivation unclaimedRequestID uses when an Exchange exists (identical
// bytes to what a rejected request has always carried), and a static
// fallback when it does not — RefuseScenarioUnknown raised outside any
// request is the one case Refusal.X may be nil for.
//
// The fallback hashes the literal "scenario.unknown", not r.Kind's own
// spelling ("scenario_unknown"): that literal is the pre-Phase-10 seed
// internal/server/listeners.go's deleted scenarioNotFoundBody hashed
// (ids.Hex32("exa", "scenario.unknown")), preserved here so this refusal's
// requestId stays byte-identical across the unit-2 refactor rather than
// silently changing for the one refusal kind Refusal.X may lack.
func refusalRequestID(r provider.Refusal) string {
	if r.X != nil {
		return unclaimedRequestID(r.X)
	}
	return provider.Hex32(string(Name), "scenario.unknown")
}
