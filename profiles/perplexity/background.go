package perplexity

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// The background lifecycle's retrieve route (issue #6, U5): retrieveAgent in
// the specification, GET /v1/agent/{id}. Unexported, like the cancel route that
// follows it: a consumer names the route by its path, not by a Go constant.
const (
	patternAgentRetrieve = "GET /v1/agent/{id}"

	// faultKeyAgentRetrieve is the retrieve's own budget. A retrieve retry must
	// not spend a create's, so it is not FaultKeyAgent, and its plan is read from
	// background.turns rather than the entry's own turns (backgroundFault).
	faultKeyAgentRetrieve = "perplexity:agent.retrieve"

	// backgroundIDPrefix is the prefix of a background job's id. The
	// specification describes the retrieve's id only as "Response id
	// (`resp_<...>`)"; the 32 hex characters after it are this simulator's.
	backgroundIDPrefix = "resp_"

	// unscriptedModel is the model a retrieve renders when its snapshot scripts
	// none. SIMULATOR-POLICY: ResponsesResponse requires model, a retrieve
	// carries no request to echo one from, and the job record holds none, so the
	// value is a fixed, non-empty, provider/model-shaped placeholder that no
	// vendor model is.
	unscriptedModel = "servicesim/unscripted"
)

// Finding codes the background script raises at load, beside the exported
// [CodeAgentBackgroundField]. Unexported: a consumer asserts on the string.
const (
	// codeAgentBackgroundTerminalThenPending is raised for a non-terminal
	// snapshot SERVED after a terminal one — a run that un-completes. It is
	// judged in poll order (provider.TerminalRegressions), not declaration
	// order.
	codeAgentBackgroundTerminalThenPending = "perplexity.agent.background.terminal_then_pending"

	// codeAgentBackgroundScriptExhausted warns that the script's last turn is
	// conditional, so the retrieve after its final snapshot matches no turn and
	// answers 404 for a job that exists.
	codeAgentBackgroundScriptExhausted = "perplexity.agent.background.script_exhausted"

	// codeAgentBackgroundBodyPredicate warns that a background turn matches on
	// the request body. A retrieve is a GET and carries none, so the turn can
	// never fire.
	codeAgentBackgroundBodyPredicate = "perplexity.agent.background.body_predicate"
)

// backgroundFault selects the retrieve route's fault plan: the first background
// turn that declares one, as provider.TurnFault selects the first of an
// entry's own turns. It reads a different scenario location from agentFault,
// which is what makes the retrieve's budget independent of the create's in
// substance and not just in name.
func backgroundFault(s *scenario.Scenario) *scenario.Fault {
	e := s.Provider(NameAgent)
	if e == nil || e.Background == nil {
		return nil
	}
	for i := range e.Background.Turns {
		if e.Background.Turns[i].Fault.HasAttempts() {
			return e.Background.Turns[i].Fault
		}
	}
	return nil
}

// routeAgentRetrieve returns GET /v1/agent/{id}. Its lane is per JOB: two runs
// retrieved in one namespace share this route, and a route-keyed cursor would
// hand each retrieve the snapshot scripted for the other run's position.
func routeAgentRetrieve() provider.Route {
	return provider.Route{Pattern: patternAgentRetrieve, FaultKey: faultKeyAgentRetrieve,
		Entry:       NameAgent,
		LaneFrom:    []string{provider.LaneFromPath + "id"},
		Credentials: bearerOnly, Fault: backgroundFault}
}

// backgroundRoutes returns the background lifecycle's routes. It is separate
// from agentRoutes because agentValidator.Routes returns agentRoutes to check a
// `when.route:` in the entry's own turns, which only the create spellings
// select; a background turn's route is checked against this route alone
// (validateBackground).
func backgroundRoutes() []provider.Route {
	return []provider.Route{routeAgentRetrieve()}
}

// wantsBackground reports whether this request asked for the background
// lifecycle.
func wantsBackground(x *provider.Exchange) bool {
	background, ok := x.Bool("background")
	return ok && background
}

// handleAgentBackground answers a create that asked for background: true and
// passed validation (background together with stream already failed there).
//
// What the scenario or the request can refuse is refused before anything is
// claimed; only the job store's own refusal (a full namespace, a live
// identifier) comes after MintJob's claim, as it does on every async create. A
// scenario with no background block cannot answer a retrieve of the run
// honestly, so the create fails closed (ruling 4 on issue #6). store: false is
// the one variant that keeps no job — the specification hides such a response
// from retrieve — and is answered under the identifier the synchronous path
// derives. Every other create mints a job. Both claim the create lane's call
// index, so a background create shifts which of the entry's turns the next
// synchronous call in the same lane receives. That is the shared index the
// contract notes document, not something to correct here.
//
// The create's fault plan is agentFault, the turn-level plan synchronous
// creates draw on too; the engine holds one plan per route key, and the three
// create spellings share one key.
func handleAgentBackground(x *provider.Exchange, entry *scenario.ProviderEntry, model string) provider.Response {
	if entry == nil || entry.Background == nil {
		x.Fail(CodeAgentBackgroundUnscripted, "body.background",
			"background: true needs a scripted lifecycle, and this scenario declares no providers.%s.background "+
				"block; add one whose turns script what GET /v1/agent/{id} returns", NameAgent)
		return errorResponse(surfaceAgent, http.StatusNotFound, "")
	}

	if store, ok := x.Bool("store"); ok && !store {
		x.Warn(CodeAgentBackgroundUnstored, "body.store",
			"background: true with store: false keeps no job: the response is hidden from retrieve, so "+
				"GET /v1/agent/{id} of it answers 404")
		// A promoted warning refuses the request here, before the claim below.
		if x.Failed() {
			return validationResponse(surfaceAgent, x.Findings(), agentFields)
		}
		id, _, _, _ := renderAgentIdentity(x, &perplexityAgent{}, x.CallIndex())
		return backgroundCreated(x, id, model, "perplexity.agent.background.unstored")
	}

	id, ok := provider.MintJob(x, NameAgent, backgroundIDPrefix, provider.Hex32)
	if !ok {
		return backgroundRefused(x)
	}
	return backgroundCreated(x, id, model, "perplexity.agent.background.created")
}

// backgroundCreated renders the queued response a background create answers
// with: the snapshot of a run that has not started, under the job's id,
// echoing the request's model, with no output and no usage. It is served and
// fault-eligible, so the create's claimed attempt applies to it.
func backgroundCreated(x *provider.Exchange, id, model, label string) provider.Response {
	body, _, err := renderAgentSnapshot(x, &perplexityAgent{Status: statusQueued}, id, model)
	if err != nil {
		x.Fail(CodeRenderFailed, "", "response body could not be rendered: %s", err)
		return errorResponse(surfaceAgent, http.StatusInternalServerError, "")
	}
	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         label,
		FaultEligible: true,
		FaultBody:     faultBody(surfaceAgent),
	}
}

// backgroundRefused answers a create provider.MintJob refused, from the error
// finding MintJob recorded. A job bound reached is 503, a collision 500, each
// carrying the finding's own message because it names the fix — a Servicesim
// configuration problem, not a vendor error. Anything else, an underivable
// identifier among them, is a plain 500.
func backgroundRefused(x *provider.Exchange) provider.Response {
	for _, f := range x.Findings() {
		switch f.Code {
		case provider.CodeJobLimitReached:
			return errorResponse(surfaceAgent, http.StatusServiceUnavailable, f.Message)
		case provider.CodeJobIDCollision:
			return errorResponse(surfaceAgent, http.StatusInternalServerError, f.Message)
		}
	}
	return errorResponse(surfaceAgent, http.StatusInternalServerError, "")
}

// handleAgentRetrieve serves GET /v1/agent/{id} (retrieveAgent).
//
// It claims nothing until it has authenticated and resolved the job: a refused
// or unknown retrieve spends nothing and advances nothing. provider.SelectPollTurn
// then claims the job's lane index, records it on the job and selects the
// snapshot from background.turns.
//
// Only background jobs resolve. A synchronous response's id answers 404 — on
// the real API a response stored by default is retrievable; this is the named
// divergence ruling 7 on issue #6 accepts — as does an id a store: false create
// answered with, which the specification also hides.
//
// HEAD is refused first. Go's ServeMux delivers HEAD to a GET pattern, so
// without this branch a HEAD would claim the job's next retrieve and advance its
// poll position for a body net/http then discards: one existence check would
// silently consume a snapshot. The specification declares no HEAD on this path
// (ruling 1: spec-declared routes only), so it is the Agent 405 with Allow: GET,
// claiming and resolving nothing.
func handleAgentRetrieve(x *provider.Exchange) provider.Response {
	if x.Request.Method == http.MethodHead {
		x.Fail(provider.CodeMethodNotAllowed, "", "HEAD is not allowed on %s; allowed: GET", x.Request.URL.Path)
		return provider.Response{
			Status: http.StatusMethodNotAllowed,
			Header: http.Header{"Allow": []string{http.MethodGet}},
			Body:   errorBody(surfaceAgent, http.StatusMethodNotAllowed, ""),
			Label:  "perplexity.agent.retrieve.head_refused",
		}
	}

	checkAuth(x)
	if x.Failed() {
		return validationResponse(surfaceAgent, x.Findings(), agentFields)
	}

	id := x.Request.PathValue("id")
	if !provider.ResolveJob(x, id) {
		return errorResponse(surfaceAgent, http.StatusNotFound, "")
	}

	var turns []scenario.Turn
	if e := x.Entry(); e != nil && e.Background != nil {
		turns = e.Background.Turns
	}
	turn, turnPath := provider.SelectPollTurn(x, "providers."+NameAgent+".background", turns, nil)
	if turn == nil {
		// SelectPollTurn recorded scenario.no_matching_turn: the scenario has no
		// snapshot for this retrieve, answered as the create the scenario cannot
		// answer is.
		return errorResponse(surfaceAgent, http.StatusNotFound, "")
	}

	var p perplexityAgent
	if err := scenario.DecodeStrict(&turn.Respond, &p); err != nil {
		x.Fail(CodeProjectionInvalid, "", "projection could not be decoded: %s.respond: %s", turnPath, err)
		return errorResponse(surfaceAgent, http.StatusInternalServerError, "")
	}
	noteUnresolved(x, x.Deps.Scenario.ResolveRefs(turnPath+".respond", &p))

	body, status, err := renderAgentSnapshot(x, &p, id, unscriptedModel)
	if err != nil {
		x.Fail(CodeRenderFailed, "", "response body could not be rendered: %s", err)
		return errorResponse(surfaceAgent, http.StatusInternalServerError, "")
	}
	return provider.Response{
		Status: http.StatusOK,
		Body:   body,
		// The journal keeps no bodies, so the label carries the status: it is how
		// a consumer sees a retrieve that confirmed completed.
		Label:         "perplexity.agent.retrieved." + string(status),
		FaultEligible: true,
		FaultBody:     faultBody(surfaceAgent),
	}
}

// renderAgentSnapshot renders one background snapshot — the queued response a
// create answers with, or what a retrieve serves — and returns it with the
// status it reports.
//
// It differs from the synchronous renderAgent in what it derives:
//
//   - id is the job's, never derived from a call index and never the
//     projection's response_id, which load refuses on a background turn.
//   - model is the projection's, else fallbackModel: the request's echo on a
//     create, unscriptedModel on a retrieve.
//   - the message item's id, when the projection scripts none, derives from
//     the job id, so it is stable across a job's retrieves and distinct
//     between jobs.
//   - a queued or in_progress snapshot with no answer renders no message item:
//     the run has not answered yet.
//   - usage renders only when the projection scripts it, and its cost only
//     when that is scripted too. A usage or cost the scenario did not script
//     is no billing fact.
func renderAgentSnapshot(
	x *provider.Exchange, p *perplexityAgent, id, fallbackModel string,
) ([]byte, agentStatus, error) {
	created := p.CreatedAt
	if created == 0 {
		created = x.Deps.Scenario.BaseTime().Unix()
	}
	status := p.Status
	if status == "" {
		status = statusCompleted
	}
	messageID := p.MessageID
	if messageID == "" {
		messageID = "msg_" + provider.Hex32(x.Deps.Scenario.SeedKey(), string(Name), "background", id, "message")
	}

	output := renderAgentOutput(p, messageID, status)
	if (status == statusQueued || status == statusInProgress) && p.Answer == "" {
		output = slices.DeleteFunc(output, func(item outputItem) bool {
			return item.OutputType() == outputTypeMessage
		})
	}

	resp := responsesResponse{
		ID:        id,
		Object:    objectResponse,
		Model:     firstNonEmpty(p.Model, fallbackModel),
		CreatedAt: created,
		Status:    string(status),
		Output:    output,
	}
	if p.Error != nil {
		resp.Error = &errorInfo{Code: p.Error.Code, Message: p.Error.Message, Type: p.Error.Type}
	}
	if p.Usage != nil {
		resp.Usage = renderAgentUsage(p.Usage)
		if p.Usage.Cost == nil {
			resp.Usage.Cost = nil
		}
	}
	body, err := provider.Render(resp, p.ExtraFields, nil)
	return body, status, err
}

// backgroundTerminal reports whether a snapshot's status ends the run, and
// whether that is known. SIMULATOR-POLICY: the specification's Status enum has
// no terminal/non-terminal split, so completed, failed, incomplete and
// cancelled are taken as terminal and queued and in_progress as not. An absent
// status is completed, the projection's zero value. A status outside the enum
// is reported by its own finding and is not judged here.
func backgroundTerminal(status agentStatus) (terminal, known bool) {
	switch status {
	case "", statusCompleted, statusFailed, statusIncomplete, statusCancelled:
		return true, true
	case statusQueued, statusInProgress:
		return false, true
	}
	return false, false
}

// backgroundFields are the respond keys a background snapshot may not carry:
// response_id would contradict the job id the snapshot always renders, and a
// retrieve serves no stream for a stream script to play on.
var backgroundFields = []string{"response_id", "stream"}

// validateBackground decodes and checks the entry's background script, each
// turn addressed providers.<name>.background.turns[i].
//
// Each snapshot gets the per-projection checks a synchronous turn gets, and not
// the stream-script checks: no stream is served. The script as a whole is
// judged the way an async poll script is — terminal absorbing in serve order,
// and an unconditional last turn — and each turn's `when.route` is checked
// against the retrieve route alone. The framework leaves that last check to
// this validator (provider.Profile.Backgroundable): the entry's own RouteLister
// lists the create spellings, and a background turn naming one would load
// clean and never fire.
func validateBackground(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	if e == nil || e.Background == nil {
		return nil
	}
	base := "providers." + e.Name + ".background.turns"
	turns := e.Background.Turns
	decoded := make([]*perplexityAgent, len(turns))

	var findings []scenario.Finding
	for i := range turns {
		turnPath := fmt.Sprintf("%s[%d]", base, i)
		path := turnPath + ".respond"
		findings = append(findings, backgroundFieldFindings(path, &turns[i])...)

		var p perplexityAgent
		if err := scenario.DecodeStrict(&turns[i].Respond, &p); err != nil {
			findings = append(findings, decodeFinding(path, fmt.Errorf("%s: %w", path, err)))
		} else {
			decoded[i] = &p
			findings = append(findings, s.ResolveRefs(path, &p)...)
			findings = append(findings, validateAgentProjection(path, &p)...)
		}
		findings = append(findings, backgroundTurnFindings(turnPath, &turns[i])...)
	}

	regressions := provider.TerminalRegressions(turns, faultKeyAgentRetrieve, func(i int) (bool, bool) {
		if decoded[i] == nil {
			return false, false
		}
		return backgroundTerminal(decoded[i].Status)
	})
	for _, r := range regressions {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     codeAgentBackgroundTerminalThenPending,
			Path:     fmt.Sprintf("%s[%d].respond.status", base, r.Turn),
			Message: fmt.Sprintf("retrieve %d is served turn %d (%s) after retrieve %d was served turn %d (%s), "+
				"which is terminal; turns are served by first match on call_index, not in declaration order, and "+
				"a run does not un-complete",
				r.Poll, r.Turn, effectiveStatus(decoded[r.Turn]),
				r.TerminalPoll, r.TerminalTurn, effectiveStatus(decoded[r.TerminalTurn])),
		})
	}

	if n := len(turns); n > 0 && !turns[n-1].When.IsEmpty() {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityWarning,
			Code:     codeAgentBackgroundScriptExhausted,
			Path:     fmt.Sprintf("%s[%d].when", base, n-1),
			Message: "the last background turn is conditional, so the retrieve after it matches no turn and " +
				"answers 404 for a job that exists",
		})
	}
	return findings
}

// effectiveStatus is the status a decoded snapshot reports.
func effectiveStatus(p *perplexityAgent) agentStatus {
	if p.Status == "" {
		return statusCompleted
	}
	return p.Status
}

// backgroundFieldFindings refuses a respond key a background snapshot may not
// carry. The keys are read from the YAML itself, aliases and merges resolved,
// rather than from the decoded projection, so an explicitly empty value is
// refused too: the author still wrote the key.
func backgroundFieldFindings(path string, turn *scenario.Turn) []scenario.Finding {
	if turn.Respond.Kind == 0 {
		return nil
	}
	var keys map[string]any
	if err := turn.Respond.Decode(&keys); err != nil {
		return nil // not a mapping: the strict decode reports the shape
	}
	var findings []scenario.Finding
	for _, key := range backgroundFields {
		if _, ok := keys[key]; !ok {
			continue
		}
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     CodeAgentBackgroundField,
			Path:     path + "." + key,
			Message: fmt.Sprintf("%s is not allowed in a background snapshot: a snapshot's id is always its "+
				"job's, and GET /v1/agent/{id} serves no stream; remove %s", key, key),
		})
	}
	return findings
}

// backgroundTurnFindings checks what a background turn declares beside its
// respond body: its route, its body predicates and its fault kinds.
func backgroundTurnFindings(turnPath string, turn *scenario.Turn) []scenario.Finding {
	var findings []scenario.Finding
	if w := turn.When; w != nil {
		if w.Route != "" && !scenario.RouteMatches(w.Route, faultKeyAgentRetrieve) {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     provider.CodeTurnRouteUnknown,
				Path:     turnPath + ".when.route",
				Message: fmt.Sprintf("route %q never selects a background snapshot: background.turns are served "+
					"only by the retrieve route %q (GET /v1/agent/{id}); the create spellings select the entry's "+
					"own turns", w.Route, faultKeyAgentRetrieve),
			})
		}
		if w.BodyContains != "" || len(w.BodyJSON) > 0 {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityWarning,
				Code:     codeAgentBackgroundBodyPredicate,
				Path:     turnPath + ".when",
				Message: "a retrieve is a GET and carries no body, so body_contains and body_json can never " +
					"match here; use call_index or route",
			})
		}
	}
	if f := turn.Fault; f != nil {
		for j := range f.Attempts {
			if kind := f.Attempts[j].EffectiveKind(); kind.IsStream() {
				findings = append(findings, scenario.Finding{
					Severity: scenario.SeverityError,
					Code:     scenario.CodeStreamFaultMismatch,
					Path:     fmt.Sprintf("%s.fault.attempts[%d].kind", turnPath, j),
					Message: fmt.Sprintf("kind %q assumes a chunked SSE transport, but GET /v1/agent/{id} never "+
						"streams: a background snapshot is always an ordinary JSON body", kind),
				})
			}
		}
	}
	return findings
}
