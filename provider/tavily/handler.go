package tavily

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// FaultKeySearch is the attempt budget POST /search draws on. It is declared
// here, beside the pattern, and nowhere else: internal/faults never has to know
// which scenario field a key maps to.
const FaultKeySearch = "tavily:search"

// PatternSearch is the ServeMux pattern for POST /search. Its path collides
// with Exa's, which is why each provider gets a listener of its own rather
// than a shared mux with a host-based hack.
const PatternSearch = "POST /search"

// Routes returns the routes this provider serves, in registration order. Each
// carries the fault budget it draws on and the selector for the scenario field
// that budget is declared in, so internal/server and testkit can build the
// fault engine's key set by concatenating the providers' Routes.
//
// It is a function, not a package-level var, so no consumer can mutate the
// route table of a package it merely imported.
func Routes() []provider.Route {
	return append([]provider.Route{RouteSearch(), RouteExtract()}, ResearchRoutes()...)
}

// RouteSearch returns POST /search, keyed "tavily:search".
func RouteSearch() provider.Route {
	return provider.Route{
		Pattern:  PatternSearch,
		FaultKey: FaultKeySearch,
		// POST /search carries a JSON body, so both the documented Authorization
		// header and the body api_key real clients send authenticate here. The GET
		// poll routes Phase 3 adds will declare Authorization alone — they have no
		// body to carry a key in, which is why this is per route.
		Credentials: defaultPlacements,
		Fault:       func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, string(Name)) },
	}
}

// handlers maps every route this profile serves to its handler, read by
// Profile().
func handlers() map[string]provider.Handler {
	return map[string]provider.Handler{
		PatternSearch:         handleSearch,
		PatternExtract:        handleExtract,
		PatternResearchCreate: handleResearchCreate,
		PatternResearchPoll:   handleResearchPoll,
		PatternResearchHead:   handleResearchHead,
	}
}

// Validator decodes and checks this package's projection bodies at startup.
// internal/server registers it under [Name] and calls provider.ValidateScenario
// after scenario.Validate and before readiness reports true, so a fixture with
// a bad Tavily field fails at boot rather than on a consumer's first request.
//
// It exists because the scenario package cannot do this itself: under the open
// provider registry a respond body is an undecoded YAML node whose Go type only
// this package knows, and scenario is a level-1 package that must not import
// provider/tavily to find out.
type Validator struct{}

// Routes implements provider.RouteLister, so a `when.route:` in a Tavily entry is
// checked against the routes this package actually serves.
func (Validator) Routes() []provider.Route { return Routes() }

// Compile-time proof that the seam is implemented; provider.ValidateScenario
// takes a map of these and a mismatch would otherwise surface as a build error
// in internal/server rather than here.
var _ provider.Validator = Validator{}

// ValidateProjections decodes every turn's projection body and reports what it
// finds, addressed by the entry's YAML path. It does not mutate the scenario —
// each turn is decoded into a fresh value — and it is safe to call more than
// once.
func (Validator) ValidateProjections(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	if e == nil {
		return nil
	}
	var findings []scenario.Finding
	for i := range e.Turns {
		path := projectionPath(e, i)

		projection := &Projection{}
		if err := e.Turns[i].DecodeProjection(e.Name, i, projection); err != nil {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     CodeProjectionInvalid,
				Path:     path,
				Message:  err.Error(),
			})
			continue
		}
		findings = append(findings, s.ResolveRefs(path, projection)...)
		findings = append(findings, checkProjection(path, projection)...)
	}
	return findings
}

// checkProjection reports the projection values that would render an
// implausible or unparseable response. Findings come back in declaration order,
// never Go map order: a readiness failure that reordered its own reasons
// between runs would be miserable to diff.
func checkProjection(path string, p *Projection) []scenario.Finding {
	var findings []scenario.Finding
	add := func(severity scenario.Severity, code, at, message string) {
		findings = append(findings, scenario.Finding{
			Severity: severity, Code: code, Path: at, Message: message,
		})
	}

	if p.ResponseTime < 0 {
		add(scenario.SeverityError, "tavily.response_time.negative", path+".response_time",
			"response_time is a duration in seconds and cannot be negative")
	}
	if p.Usage != nil && p.Usage.Credits < 0 {
		add(scenario.SeverityError, "tavily.usage.credits.negative", path+".usage.credits",
			"credits cannot be negative")
	}
	for i, img := range p.Images {
		if img.URL == "" {
			add(scenario.SeverityError, "tavily.image.url.missing",
				fmt.Sprintf("%s.images[%d].url", path, i),
				"an images entry is an object with a url; a bare string is not the documented shape")
		}
	}
	for i := range p.Results {
		at := fmt.Sprintf("%s.results[%d]", path, i)
		result := &p.Results[i]
		if result.Score < 0 || result.Score > 1 {
			add(scenario.SeverityWarning, "tavily.result.score.range", at+".score",
				"score is documented as a relevance value between 0 and 1")
		}
		for j, img := range result.Images {
			if img.URL == "" {
				add(scenario.SeverityError, "tavily.image.url.missing",
					fmt.Sprintf("%s.images[%d].url", at, j),
					"an images entry is an object with a url; a bare string is not the documented shape")
			}
		}
	}
	if p.Extract != nil {
		for i, fr := range p.Extract.FailedResults {
			if fr.URL == "" {
				add(scenario.SeverityError, "tavily.extract.failed_results.url.missing",
					fmt.Sprintf("%s.extract.failed_results[%d].url", path, i),
					"a failed_results entry must name the url it forces to fail")
			}
		}
	}
	return findings
}

// handleSearch serves POST /search.
//
// The order is the one §4.4 requires and is not an accident: validation and
// authentication run first and record findings, and only a request that
// survived both is allowed to select a turn — because selecting a turn claims
// an attempt from the counter the fault engine draws on, and a rejected request
// must never consume a retry budget.
func handleSearch(x *provider.Exchange) provider.Response {
	entry := x.Entry()

	req := parseSearchRequest(x)
	checkAuth(x)
	if x.Failed() {
		return errorResponse(x)
	}

	projection, ok := selectProjection(x, entry)
	if !ok {
		return errorResponse(x)
	}

	body, err := renderSearch(projection, req, keysFor(x))
	if err != nil {
		x.Fail(CodeProjectionInvalid, "", "the scenario projection could not be rendered: %v", err)
		return errorResponse(x)
	}

	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         string(Name) + ".search.ok",
		FaultEligible: true,
		FaultBody:     faultBody,
	}
}

// selectProjection picks the turn serving this request and decodes its
// projection body.
//
// A scenario that declares no Tavily block at all is not an error: it renders
// the zero projection, which is a well-shaped empty success, and is what makes
// Profile().Handler(provider.Deps{}) a usable zero-configuration handler. A
// scenario that declares the block but no turn matching this request *is* an error —
// the author wrote a script that cannot answer, and answering with an empty 200
// would hide it.
func selectProjection(x *provider.Exchange, entry *scenario.ProviderEntry) (*Projection, bool) {
	if entry == nil {
		return &Projection{}, true
	}

	turn, index := provider.SelectTurnFor(x, entry)
	if turn == nil {
		return nil, false
	}

	projection := &Projection{}
	if err := turn.DecodeProjection(entry.Name, index, projection); err != nil {
		// Unreachable through internal/server, which runs ValidateScenario
		// before readiness reports true. Reachable by a caller that built a
		// Scenario by hand, and a journaled 500 beats a silent empty body.
		x.Fail(CodeProjectionInvalid, "", "%v", err)
		return nil, false
	}

	// The projection is a fresh value per request, so resolving into it never
	// mutates the scenario and two concurrent requests cannot race here.
	for _, finding := range x.Deps.Scenario.ResolveRefs(projectionPath(entry, index), projection) {
		x.Warn(CodeProjectionUnresolved, "", "%s: %s", finding.Path, finding.Message)
	}
	return projection, true
}

// projectionPath addresses a turn's respond body the way a scenario finding
// does, so a request-time resolution warning reads like the startup one.
func projectionPath(entry *scenario.ProviderEntry, index int) string {
	return fmt.Sprintf("providers.%s.turns[%d].respond", entry.Name, index)
}

// keysFor assembles the stable inputs every derived value hangs off.
//
// Two tuples come out of it because two different questions are being asked.
// renderKeys.Call carries the lane's call index unconditionally and keys
// request_id, so that successive calls are distinguishable the way a real
// vendor's are. renderKeys.ID keys response_time, which is a scenario constant
// rather than a measurement and therefore takes the attempt index only where a
// fault plan actually declares one — the §3.1 rule, unchanged.
//
// Both are derived from the one counter turn selection and fault selection
// share, never from a clock, a random source or a counter of this package's own.
func keysFor(x *provider.Exchange) renderKeys {
	decision := x.Fault()
	seed := x.Deps.Scenario.SeedKey()

	base := []string{seed, string(x.Provider), x.Route.FaultKey}
	planned := append(slices.Clone(base), strconv.Itoa(decision.Index))

	keys := renderKeys{Seed: seed, ID: base, Call: planned}
	if decision.Planned {
		keys.ID = planned
	}
	return keys
}
