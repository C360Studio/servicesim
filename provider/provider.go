package provider

import (
	"github.com/c360studio/servicesim/scenario"
)

// Name identifies a simulated provider. It is the listener's identity, not the
// scenario block's key: one listener may serve several scenario provider entries
// (Perplexity's Sonar and Agent surfaces are separate entries on one listener).
type Name string

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

	// Entry names the scenario provider entry this route is served from. Empty
	// means the listener's own provider name, which is what every route
	// registered before this field existed relies on.
	//
	// It exists because a listener may serve MORE THAN ONE scenario entry —
	// Perplexity's Sonar and Agent surfaces today, the async surfaces next — and
	// without it every such entry after the first has its own `turn_key:` and
	// `validation:` block silently ignored. Both are resolved through
	// [Exchange.Entry], which had no way to tell which entry a request belonged
	// to and fell back to the listener name.
	//
	// Resolving it from the ROUTE keeps the single-resolution rule intact: the
	// route is known to [Handle] before the handler runs, so the lane is still
	// resolved once, in one place. Reading it from the handler's choice of entry
	// instead would be the derive-it-twice shape that rule forbids.
	Entry string

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

	// LaneFrom names lane discriminators this ROUTE contributes, in the same
	// extractor grammar as scenario.TurnKey and evaluated after the scenario's,
	// so a lane key reads "<route key> | <scenario extractors> | <route
	// extractors>".
	//
	// It exists because a poll route's per-job lane is not a scenario author's
	// choice. Two jobs polled concurrently in one namespace share a route, and a
	// route-keyed cursor would hand each poll the snapshot scripted for the other
	// job — the fan-out failure turn_key exists to prevent, arriving from the
	// routing side rather than the scenario side. A wrong status code fails
	// loudly; a coherent "running" belonging to somebody else's job fails
	// somewhere else entirely, much later.
	//
	// It is declared here rather than in scenario, and that is what keeps the
	// schema additive: scenario.Validate rejects an unrecognised turn_key
	// extractor as a LOAD ERROR, so a "path:" extractor written in a scenario
	// file would be the one change in this design that an older binary refuses to
	// load. Declaring it on the route means no scenario file mentions it and no
	// older binary ever sees it.
	LaneFrom []string

	// Fault selects this route's fault plan out of a scenario, for example
	// func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, "exa") }
	// (nil-safe on every hop). It exists so the fault engine never has to know
	// which scenario field a key maps to: the package that declares the key
	// declares the mapping next to it, and the fault engine (fault_engine.go)
	// stays at level 4 with no knowledge of profiles/exa, profiles/tavily or
	// profiles/perplexity. A nil Fault means the route declares no plan.
	Fault func(*scenario.Scenario) *scenario.Fault
}
