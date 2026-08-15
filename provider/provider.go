package provider

import (
	"github.com/c360studio/servicesim/scenario"
)

// Name identifies a simulated provider. It is the listener's identity, not the
// scenario block's key: one listener may serve several scenario provider entries
// (Perplexity's Sonar and Agent surfaces are separate entries on one listener).
type Name string

// The simulated providers.
const (
	Exa        Name = "exa"
	Tavily     Name = "tavily"
	Perplexity Name = "perplexity"
)

// Credential placements. This is the vocabulary that Route.Credentials and a
// scenario's auth.headers are both written in, which is why it lives here rather
// than in one provider package: a scenario author writing auth.headers should not
// have to know which provider invented the spelling.
//
// Most placements are header names, so the set reads as "header names, plus the
// ones that are not". PlacementBodyAPIKey is the exception that forced the
// vocabulary to exist: Tavily's real clients put the key in the JSON body, and a
// list of header names cannot say so.
const (
	// PlacementAuthorization is the Authorization header, whatever scheme it
	// carries. Servicesim accepts an opaque value here as well as Bearer, because
	// the vendors' own documentation is inconsistent about the scheme.
	PlacementAuthorization = "authorization"

	// PlacementXAPIKey is the x-api-key header, which is what Exa's reference
	// page leads with.
	PlacementXAPIKey = "x-api-key"

	// PlacementBodyAPIKey is an api_key property in the JSON request body. It is
	// not a header, and that is the whole point: Tavily's shipped clients
	// authenticate this way, and v0.1.1 had to stop rejecting them.
	PlacementBodyAPIKey = "body:api_key"
)

// Route binds a ServeMux pattern to the fault budget it draws on and to the
// scenario field that budget is declared in.
type Route struct {
	// Pattern is a Go 1.22 ServeMux pattern, for example "POST /search".
	Pattern string

	// Credentials lists the placements this route accepts, in the vocabulary of
	// the Placement constants. Empty means the provider package's own default,
	// which is what every route registered before this field existed relies on.
	//
	// It is per ROUTE because credential placement is per route in the real
	// vendors: an async surface takes the key in the JSON body on the create POST
	// and a Bearer header on the GET poll, which carries no body to put it in.
	// AuthPolicy is per scenario ENTRY and cannot express that at any level.
	//
	// A scenario's auth.headers still overrides this outright — see
	// Exchange.AcceptedPlacements for the one precedence rule the whole tree
	// shares. The override stays entry-wide on purpose: it exists for negative
	// tests ("prove my client no longer uses the body placement"), and a
	// per-route override would be a second addressing scheme to learn for a case
	// nobody has yet needed.
	Credentials []string

	// FaultKey is what attempt counting is keyed on, and — because the turn cursor
	// shares that counter rather than keeping a second one that could disagree
	// about which call this is — what turn selection is keyed on too. Two routes
	// that are aliases of one operation share a key, so a retry through the alias
	// draws on the same budget: Perplexity's /v1/sonar and /chat/completions are
	// both "perplexity:completions".
	FaultKey string

	// Fault selects this route's fault plan out of a scenario, for example
	// func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, "exa") }
	// (nil-safe on every hop). It exists so the fault engine never has to know
	// which scenario field a key maps to: the package that declares the key
	// declares the mapping next to it, and internal/faults stays at level 4 with no
	// knowledge of provider/exa, provider/tavily or provider/perplexity. A nil
	// Fault means the route declares no plan.
	Fault func(*scenario.Scenario) *scenario.Fault
}
