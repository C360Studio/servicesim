package exa

import (
	"net/http"
	"strconv"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// providerName is the scenario provider entry this listener serves, and the
// second component of every derived identifier.
const providerName = string(Name)

// Route patterns, in registration order.
const (
	patternSearch = "POST /search"
	patternAnswer = "POST /answer"
)

// Fault keys. /search and /answer draw on separate budgets: an /answer call must
// not consume /search's retries, and a retry of one must not be answered from
// the other's plan.
const (
	faultKeySearch = "exa:search"
	faultKeyAnswer = "exa:answer"
)

// Routes returns the routes this provider serves, in registration order. Each
// carries the fault budget it draws on and the selector for the scenario field
// that budget is declared in, so internal/server and testkit can build the fault
// engine's key set by concatenating the three providers' Routes().
//
// It is a function, not a package-level var, so no consumer can mutate the route
// table of a package it merely imported.
func Routes() []provider.Route {
	return append(
		[]provider.Route{routeSearch(), routeAnswer(), routeContents(), routeFindSimilar()},
		agentRunRoutes()...,
	)
}

// routeSearch returns POST /search, keyed "exa:search". Its plan is the provider
// block's own `fault:`, which the scenario loader normalises onto the turn.
func routeSearch() provider.Route {
	return provider.Route{
		Pattern:     patternSearch,
		FaultKey:    faultKeySearch,
		Credentials: authHeaders,
		Fault:       func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, providerName) },
	}
}

// routeAnswer returns POST /answer, keyed "exa:answer". Its plan lives inside
// the projection, under `answer.fault`, because the block-level `fault:` belongs
// to /search.
func routeAnswer() provider.Route {
	return provider.Route{
		Pattern:     patternAnswer,
		FaultKey:    faultKeyAnswer,
		Credentials: authHeaders,
		Fault:       answerFault,
	}
}

// answerFault returns the /answer attempt budget: the first turn whose
// projection declares one. It is nil-safe on every hop — the scenario, the
// registry, the entry, its turns and the projection's answer block may each be
// absent — because it runs at composition time against a scenario that may not
// have been validated yet.
func answerFault(s *scenario.Scenario) *scenario.Fault {
	e := s.Provider(providerName)
	if e == nil {
		return nil
	}
	for i := range e.Turns {
		var p projectionBody
		if err := e.Turns[i].DecodeProjection(providerName, i, &p); err != nil {
			// A projection that cannot be decoded is reported by
			// validator.ValidateProjections, which runs before readiness. Here
			// it simply means this turn declares no /answer plan.
			continue
		}
		if p.Answer != nil && p.Answer.Fault.HasAttempts() {
			return p.Answer.Fault
		}
	}
	return nil
}

// handlers maps every route this profile serves to its handler, read by
// Profile().
func handlers() map[string]provider.Handler {
	return map[string]provider.Handler{
		patternSearch:      handleSearch,
		patternAnswer:      handleAnswer,
		patternContents:    handleContents,
		patternFindSimilar: handleFindSimilar,
		patternRunCreate:   handleAgentRunCreate,
		patternRunPoll:     handleAgentRunPoll,
		patternRunHead:     handleAgentRunHead,
	}
}

// handleSearch serves POST /search.
//
// The order below is the fail-closed order §4.4 requires: authentication and
// validation run first and reject without consuming a fault attempt, and only a
// request that survived both reaches turn selection, which is what claims the
// attempt index.
func handleSearch(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	authenticate(x)
	validateSearch(x, streamPolicy(entry))
	if x.Failed() {
		return rejection(x)
	}

	p, ok := selectProjection(x, entry)
	if !ok {
		return rejection(x)
	}

	requestID := renderedRequestID(x, p.RequestID)
	body, err := renderSearch(x, p, requestID)
	if err != nil {
		x.Fail(codeRenderFailed, "", "rendering the Exa search response failed: %v", err)
		return rejection(x)
	}
	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         "exa.search.ok",
		FaultEligible: true,
		FaultBody:     func(a scenario.FaultAttempt) []byte { return faultBody(requestID, a) },
	}
}

// handleAnswer serves POST /answer. It validates a different request surface and
// renders a different document from handleSearch; the two share the lifecycle
// and nothing else.
func handleAnswer(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	authenticate(x)
	validateAnswer(x, streamPolicy(entry))
	if x.Failed() {
		return rejection(x)
	}

	p, ok := selectProjection(x, entry)
	if !ok {
		return rejection(x)
	}

	requestID := renderedRequestID(x, p.RequestID)
	body, err := renderAnswer(x, p, requestID)
	if err != nil {
		x.Fail(codeRenderFailed, "", "rendering the Exa answer response failed: %v", err)
		return rejection(x)
	}
	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         "exa.answer.ok",
		FaultEligible: true,
		FaultBody:     func(a scenario.FaultAttempt) []byte { return faultBody(requestID, a) },
	}
}

// rejection turns a request that failed authentication, validation or turn
// selection into Exa's error envelope. It never claims a fault attempt: a
// rejected request must not consume a retry budget.
func rejection(x *provider.Exchange) provider.Response {
	status, tag, message := classify(x)
	return provider.Response{
		Status: status,
		Body:   errorBody(unclaimedRequestID(x), message, tag, status),
		Label:  "exa.error." + tag,
	}
}

// selectProjection chooses the turn serving this request and decodes its
// projection. It is the first thing on the request path that claims an attempt
// index, which is why it runs only after validation has passed.
func selectProjection(x *provider.Exchange, e *scenario.ProviderEntry) (*projectionBody, bool) {
	if e == nil {
		// The scenario declares nothing for this provider, which is the zero-Deps
		// case: serve a well-shaped empty success rather than an error. A
		// scenario that *does* declare the provider but has no turn for this
		// call is a different thing entirely, and fails closed below.
		return &projectionBody{}, true
	}

	turn, index := provider.SelectTurnFor(x, e)
	if turn == nil {
		return nil, false
	}

	p := &projectionBody{}
	if err := turn.DecodeProjection(providerName, index, p); err != nil {
		// Unreachable through internal/server, which runs
		// provider.ValidateScenario before readiness. Reachable through
		// Profile().Handler(provider.Deps{Scenario: s}) with an unvalidated
		// scenario, and a journaled 500 beats a panic on a request path.
		x.Fail(codeProjectionInvalid, "", "the scenario's Exa projection could not be decoded: %v", err)
		return nil, false
	}

	// Source references inside a projection body are invisible to
	// Scenario.Resolve, which cannot see into an undecoded node. Linking them
	// here is what makes ref.Target non-nil for the renderer.
	for _, f := range x.Deps.Scenario.ResolveRefs(respondPath(e, index), p) {
		x.Warn(codeSourceUnresolved, "", "%s: %s", f.Path, f.Message)
	}
	return p, true
}

// streamPolicy returns the provider block's streaming policy.
//
// It reads the first turn rather than the selected one because the policy
// decides whether a request is *rejected*, and rejection must happen before turn
// selection claims an attempt. A policy that varied per turn would therefore be
// unusable; treating it as a property of the provider block is the honest shape.
func streamPolicy(e *scenario.ProviderEntry) scenario.StreamPolicy {
	if e == nil || len(e.Turns) == 0 {
		return scenario.StreamWarn
	}
	var p projectionBody
	if err := e.Turns[0].DecodeProjection(providerName, 0, &p); err != nil {
		return scenario.StreamWarn
	}
	if p.Stream == "" {
		return scenario.StreamWarn
	}
	return p.Stream
}

// renderedRequestID returns the requestId a successful response carries: the
// scenario's override, or thirty-two lowercase hex characters derived from the
// scenario seed, the provider, the fault key and the call index.
//
// The call index joins the tuple unconditionally, not only where the route
// declares a fault plan. A real vendor returns a distinct identifier per call,
// and a simulator that returned one value for every call collapses a consumer's
// log correlation to a single point — the /search 200, the retry after it and
// the /answer beside it all carrying one requestId.
//
// Determinism is not weakened by this, it is sharpened. The property house rule
// 2 states is that the same scenario and the same request produce byte-identical
// bytes; the precise form of it is "at the same call position", which is what a
// lane cursor measures. Two consecutive calls differ; call 0 of a fresh lane —
// a new namespace, a new process, a reset — reproduces call 0 exactly.
//
// It must be called only after an attempt has been claimed, which selectProjection
// has done by the time a caller has a projection to render.
func renderedRequestID(x *provider.Exchange, override string) string {
	if override != "" {
		return override
	}
	return provider.Hex32(callParts(x)...)
}

// unclaimedRequestID returns the requestId an error body carries. It never
// consults the fault decision, because asking for one claims an attempt and a
// rejected request must not consume one (§4.4).
//
// The consequence is deliberate and is the one place the per-call property above
// does not hold: two requests rejected by routing, authentication or validation
// in the same lane carry the same requestId, because neither advanced the lane's
// cursor and there is no other per-call quantity a rejected request may read. A
// rejected request is still distinguishable from the served ones — its tuple is
// the one with no call index at all.
func unclaimedRequestID(x *provider.Exchange) string {
	return provider.Hex32(identityParts(x)...)
}

// identityParts is the part of the tuple that does not depend on which call this
// is: the scenario seed, the provider and the route's fault key.
func identityParts(x *provider.Exchange) []string {
	return []string{x.Deps.Scenario.SeedKey(), providerName, x.Route.FaultKey}
}

// callParts is identityParts plus the zero-based index of this call within its
// lane. It claims the attempt if the caller has not already; see
// Exchange.CallIndex.
func callParts(x *provider.Exchange) []string {
	return append(identityParts(x), strconv.Itoa(x.CallIndex()))
}
