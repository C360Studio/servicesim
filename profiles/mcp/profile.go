package mcp

import (
	"embed"
	"errors"
	"io/fs"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// defaultPort is MCP's default listener port. It lives here rather than in
// internal/config (Phase 10 unit 3: "config no longer names a vendor") —
// this profile is the one place that gets to know it binds 8084 by default,
// and internal/config derives every port flag's default from Profile.Port
// instead of naming one.
const defaultPort = 8084

// contractsFS embeds this profile's own golden fixtures, provenance record
// and README — exactly the bundle an out-of-tree profile embeds beside its
// own package (Phase 10 unit 6: "theirs, embedded beside their package").
// It used to be a sub-FS of the whole-repository contracts.FS(); genericising
// contracts around fs.FS let the bundle move to profiles/mcp/contracts and
// this package own its embedding, the way profiles/mcp/contracts/README.md's
// verification record has always been MCP's own, not shared — MCP's own
// contract was recorded here before this profile even registered (Phase 8
// unit 1), which contracts/README.md "Keeping them honest" calls out as the
// example of the pattern.
//
//go:embed contracts
var contractsFS embed.FS

// Profile returns the registration record MCP is served from: everything
// New used to build by hand, plus the fields New never needed because
// internal/config and internal/server supplied them from their own
// four-vendor switches.
func Profile() provider.Profile {
	sub, err := fs.Sub(contractsFS, "contracts")
	if err != nil {
		// Unreachable: "contracts" is the fixed, valid fs.Sub pattern the
		// //go:embed directive above always populates.
		panic("profiles/mcp: contracts sub-FS: " + err.Error())
	}

	return provider.Profile{
		Name:    Name,
		Title:   "MCP",
		Summary: "a Model Context Protocol Streamable HTTP server",
		Port:    defaultPort,

		Handlers: handlers(),
		Routes:   Routes(),
		Validators: map[string]provider.Validator{
			string(Name): validator{},
		},
		ErrorBody: refusalBody,
		// DefaultAuth is required: MCP's own request.go's authPolicy defaults
		// an entry with no auth: block to AuthOptional — deliberately the
		// opposite of the three research profiles (decision 3), because the
		// specification leaves authentication to the deployment and this
		// profile must not invent a requirement the specification does not
		// make.
		DefaultAuth: scenario.AuthOptional,

		Contracts: sub,
		// Hosts is empty: MCP is a protocol server, not a vendor with a real
		// hostname to guard against (contracts/mcp/README.md names no
		// paid-API host, and scripts/lint-no-live-hosts.sh's pattern gained
		// no MCP row either — the gap framework-seam.md's rule 3 discussion
		// names).
		CredentialNames: []string{"Authorization"},
	}
}

// refusalBody renders a provider.Refusal in MCP's JSON-RPC error envelope —
// the func(Refusal) []byte Profile.ErrorBody needs. It is what handler.go's
// now-deleted inline NotFound/MethodNotAllowed handlers (notFoundResponse,
// methodNotAllowedResponse) and internal/server/listeners.go's now-deleted
// duplicated mcpErrorEnvelope/scenarioNotFoundBody built by hand before this
// unit.
func refusalBody(r provider.Refusal) []byte {
	switch r.Kind {
	case provider.RefuseNotFound:
		return notFoundResponse().Body
	case provider.RefuseMethodNotAllowed:
		return methodNotAllowedResponse().Body
	case provider.RefuseScenarioUnknown:
		// Decision 6's -32600 InvalidRequestError, generic message: the
		// scenario name is in the journal and the log, which is where a
		// person looks, not in a response a consumer's error decoder
		// parses. Answered directly rather than through notFoundResponse:
		// that is -32601 MethodNotFoundError, the shape for "no route
		// answers this path", which a named-but-unloaded scenario is not.
		return invalidRequestResponse("Not Found").Body
	case provider.RefuseRequest:
		return invalidRequestResponse(refusalMessage(r)).Body
	case provider.RefuseInternal:
		// A fixed message, never the finding text: RefuseInternal is raised
		// for a handler panic (mux.go's recoverPanic) and for a stream with
		// an empty Grammar (handle.go), and the finding recorded for either
		// carries arbitrary, author-controlled text (a Go panic value's
		// %v, verbatim) that has not passed through redact.String the way a
		// journal entry has — exa, tavily and perplexity all serve a fixed
		// "internal server error" text for this kind rather than echoing
		// the finding, and MCP matches them here instead of being the one
		// profile that puts a raw panic message on the wire.
		return internalErrorResponse(nullID, errInternal).Body
	default: // any future kind this package does not know
		return internalErrorResponse(nullID, errors.New(refusalMessage(r))).Body
	}
}

// errInternal is the fixed message provider.RefuseInternal's body carries.
var errInternal = errors.New("Internal error")

// refusalMessage returns the text a refusal body carries: the first error in
// Findings order, the same text every other in-tree profile's own
// errorResponse quotes, or a generic fallback when Refusal.X is nil (there
// is no request to have rejected).
func refusalMessage(r provider.Refusal) string {
	if r.X != nil {
		for _, f := range r.X.Findings() {
			if f.Severity == provider.SeverityError {
				return f.Message
			}
		}
	}
	return "the request was rejected"
}
