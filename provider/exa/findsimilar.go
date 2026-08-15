package exa

import (
	"net/http"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// patternFindSimilar and faultKeyFindSimilar are POST /findSimilar's route
// pattern and fault budget key. The key's suffix after the colon
// ("find_similar") is the bare name a scenario's `when.route:` matches, per
// D-h.
const (
	patternFindSimilar  = "POST /findSimilar"
	faultKeyFindSimilar = "exa:find_similar"
)

// RouteFindSimilar returns POST /findSimilar, keyed "exa:find_similar". The
// vendor's own OpenAPI spec marks the operation deprecated in favour of
// /search — contracts/exa/README.md — but it is simulated anyway: Phase 0's
// lesson is that rejecting valid production traffic is the worst failure a
// simulator can produce, and a client written before the deprecation is still
// production traffic.
func RouteFindSimilar() provider.Route {
	return provider.Route{
		Pattern:     patternFindSimilar,
		FaultKey:    faultKeyFindSimilar,
		Credentials: authHeaders,
		Fault:       findSimilarFault,
	}
}

// findSimilarFault returns the /findSimilar attempt budget: the first turn
// whose projection declares one, following answerFault's shape.
func findSimilarFault(s *scenario.Scenario) *scenario.Fault {
	e := s.Provider(providerName)
	if e == nil {
		return nil
	}
	for i := range e.Turns {
		var p Projection
		if err := e.Turns[i].DecodeProjection(providerName, i, &p); err != nil {
			continue
		}
		if p.FindSimilar != nil && p.FindSimilar.Fault.HasAttempts() {
			return p.FindSimilar.Fault
		}
	}
	return nil
}

// handleFindSimilar serves POST /findSimilar.
//
// D-b: unlike /contents this IS a relevance route — its results are exactly
// what the selected turn's `find_similar.results` declares, the same shape of
// projection /search renders, over the same renderResult.
func handleFindSimilar(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	authenticate(x, entry)
	validateFindSimilarRequest(x)
	if x.Failed() {
		return rejection(x)
	}

	p, ok := selectProjection(x, entry)
	if !ok {
		return rejection(x)
	}

	requestID := renderedRequestID(x, p.RequestID)
	body, err := renderFindSimilar(x, p, requestID)
	if err != nil {
		x.Fail(codeRenderFailed, "", "rendering the Exa findSimilar response failed: %v", err)
		return rejection(x)
	}
	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         "exa.find_similar.ok",
		FaultEligible: true,
		FaultBody:     func(a scenario.FaultAttempt) []byte { return faultBody(requestID, a) },
	}
}
