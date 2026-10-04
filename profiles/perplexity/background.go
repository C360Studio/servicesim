package perplexity

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// The background lifecycle's routes: retrieveAgent in the specification, GET
// /v1/agent/{id} (issue #6, U5), and cancelAgentResponse, POST
// /v1/agent/{id}/cancel (issue #31, U6). Unexported: a consumer names a route
// by its path, not by a Go constant.
const (
	patternAgentRetrieve = "GET /v1/agent/{id}"
	patternAgentCancel   = "POST /v1/agent/{id}/cancel"

	// faultKeyAgentRetrieve is the retrieve's own budget. A retrieve retry must
	// not spend a create's, so it is not FaultKeyAgent, and its plan is read from
	// background.turns rather than the entry's own turns (backgroundFault).
	faultKeyAgentRetrieve = "perplexity:agent.retrieve"

	// faultKeyAgentCancel is the cancel's own budget, its plan read from
	// background.cancel.fault (cancelFault): a cancel retry spends neither a
	// create's retries nor a retrieve's.
	faultKeyAgentCancel = "perplexity:agent.cancel"

	// backgroundBase is the YAML path of the block the background scripts are
	// declared in, which the retrieve and the cancel name in their findings.
	backgroundBase = "providers." + NameAgent + ".background"

	// statusCancelling is the one status a cancel's 200 carries: the
	// specification's enum for it has this single member, "the cancel was
	// accepted and the run stops asynchronously".
	statusCancelling = "cancelling"

	// cancelTerminalMessage is the message of the 400 a cancel of a terminal
	// run answers. SIMULATOR-POLICY: the specification documents the 400 ("The
	// response is already terminal, or the request is invalid") and gives no
	// message for it, so this plain sentence is Servicesim's.
	cancelTerminalMessage = "The response is already terminal and cannot be cancelled."

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

// Finding codes the background script raises at load. Unexported, like every
// other load-time code of this profile and of Exa's: a consumer asserts on the
// string, which docs/scenario-schema.md lists.
const (
	// codeAgentBackgroundField is raised, as an error, for a `response_id` or
	// `stream` key in a background snapshot's respond body, an `id` or `status`
	// key in its `extra_fields` or in the `extra_fields` of one of the turn's
	// fault attempts, or a `response_id` or `status` key in the `extra_fields`
	// of an attempt of background.cancel.fault. A snapshot's id is always its
	// job's, so a scripted one would contradict it; a retrieve serves no stream,
	// so a script for one could never play; and extra fields win over the body
	// they are merged into, so those keys would replace the job's id and the
	// status the journal label reports, or forge the two keys of the cancel's
	// acknowledgement. Each key is refused rather than silently ignored, and an
	// extra_fields key in every spelling a JSON decoder reads as it ("Status",
	// "ſtatus"; backgroundExtraFieldFindings).
	codeAgentBackgroundField = "perplexity.agent.background.field"

	// codeAgentBackgroundTerminalThenPending is raised for a non-terminal
	// snapshot SERVED after a terminal one — a run that un-completes. It is
	// judged in poll order (provider.TerminalRegressions), not declaration
	// order.
	codeAgentBackgroundTerminalThenPending = "perplexity.agent.background.terminal_then_pending"

	// codeAgentBackgroundScriptExhausted warns that the script's last turn has
	// a condition a retrieve can fail, so the retrieve after its final snapshot
	// matches no turn and answers 404 for a job that exists, and a cancel judged
	// by that retrieve answers 500 (provider.CodeJobCancelUnscripted). A last
	// turn conditioned only on the retrieve route matches every retrieve and is
	// not warned about (backgroundTurnCanMiss).
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

// cancelFault selects the cancel route's fault plan: background.cancel.fault, a
// third scenario location beside agentFault's and backgroundFault's, so the
// three budgets are independent in substance and not just in name.
func cancelFault(s *scenario.Scenario) *scenario.Fault {
	e := s.Provider(NameAgent)
	if e == nil || e.Background == nil || e.Background.Cancel == nil || !e.Background.Cancel.Fault.HasAttempts() {
		return nil
	}
	return e.Background.Cancel.Fault
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

// routeAgentCancel returns POST /v1/agent/{id}/cancel. Its lane is per job,
// like the retrieve's, so every run has its own cancel attempt budget and one
// run's failed cancel never spends another's retry.
func routeAgentCancel() provider.Route {
	return provider.Route{Pattern: patternAgentCancel, FaultKey: faultKeyAgentCancel,
		Entry:       NameAgent,
		LaneFrom:    []string{provider.LaneFromPath + "id"},
		Credentials: bearerOnly, Fault: cancelFault}
}

// backgroundRoutes returns the background lifecycle's routes. It is separate
// from agentRoutes because agentValidator.Routes returns agentRoutes to check a
// `when.route:` in the entry's own turns, which only the create spellings
// select; a background turn's route is checked against the retrieve alone
// (validateBackground), since a cancel selects no snapshot of its own.
func backgroundRoutes() []provider.Route {
	return []provider.Route{routeAgentRetrieve(), routeAgentCancel()}
}

// backgroundScripts returns the entry's retrieve script and the cancel script
// beside it, nil when the scenario declares none.
func backgroundScripts(e *scenario.ProviderEntry) ([]scenario.Turn, *scenario.CancelPolicy) {
	if e == nil || e.Background == nil {
		return nil, nil
	}
	return e.Background.Turns, e.Background.Cancel
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
// snapshot: from background.turns, or, once a cancel is recorded, from
// background.cancel.turns at the retrieve's position since the cancel.
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

	turns, cancel := backgroundScripts(x.Entry())
	turn, turnPath := provider.SelectPollTurn(x, backgroundBase, turns, cancel)
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

// handleAgentCancel serves POST /v1/agent/{id}/cancel (cancelAgentResponse).
//
// The specification declares no request body, so none is read: a bare POST is
// the cancel, a JSON object is accepted and ignored, and only what the shared
// request lifecycle refuses on every route — a body that is not a JSON object —
// is refused here, before anything is claimed.
//
// Authentication and resolution run before provider.CancelJob, which claims the
// cancel lane's attempt: a refused or unknown cancel spends nothing and records
// nothing. From there CancelJob decides and records, judging the run by the
// snapshot its next RETRIEVE would serve, and this renders. Every outcome after
// the claim but one is a served, fault-eligible response — the 400 of a run
// already terminal and the 404 of a job a reset removed included. Built as a
// rejection, it would lose the attempt the cancel claimed and raise
// fault.attempt_on_rejection, so "completion wins" would read as an error and
// the attempt a retry draws would depend on the run's state rather than on how
// many cancels were sent.
//
// The one exception is provider.CancelFailed, the 500 of a cancel CancelJob
// could not script or record (job.cancel_unscripted, job.cancel_contended). It
// carries an error finding, so the attempt it claimed stays claimed and spent —
// a retry still draws the next index — but that attempt's fault is stripped
// (fault.attempt_on_rejection) and the 500 is served in its place: an attempt
// scripted {status: 502, accepted: true} against a pending run whose
// background.cancel scripts no turns answers 500, not 502.
//
// The 200 never carries a snapshot, unlike Exa's cancel: the specification's
// body is the response id and the status cancelling, nothing else. What the
// run did after the cancel is what its next retrieve serves.
func handleAgentCancel(x *provider.Exchange) provider.Response {
	checkAuth(x)
	if x.Failed() {
		return validationResponse(surfaceAgent, x.Findings(), agentFields)
	}

	id := x.Request.PathValue("id")
	if !provider.ResolveJob(x, id) {
		return errorResponse(surfaceAgent, http.StatusNotFound, "")
	}

	turns, cancel := backgroundScripts(x.Entry())
	outcome, turn, _ := provider.CancelJob(x, backgroundBase, turns, cancel, faultKeyAgentRetrieve, terminalSnapshot)

	switch outcome {
	case provider.CancelRecorded:
		return cancelAccepted(x, id, "perplexity.agent.cancel.accepted")
	case provider.CancelUncommitted:
		// The scripted fault replaces this response, and nothing was recorded:
		// the label must not claim the cancel took effect.
		return cancelAccepted(x, id, "perplexity.agent.cancel.unrecorded")
	case provider.CancelAlreadyCancelling:
		// SIMULATOR-POLICY: the specification is silent on a second cancel. It
		// is judged as the first was, by the run's next retrieve, which is now
		// the cancel script's: a run that has stopped is the 400 a terminal run
		// answers, and one still stopping is acknowledged again.
		if terminalSnapshot(turn) {
			return cancelTerminal()
		}
		return cancelAccepted(x, id, "perplexity.agent.cancel.repeated")
	case provider.CancelTerminal:
		return cancelTerminal()
	case provider.CancelNotFound:
		resp := errorResponse(surfaceAgent, http.StatusNotFound, "")
		if x.HasFinding(provider.CodeJobIDInvalid) {
			// CancelJob refused a request that resolved no job and claimed
			// nothing: the unclaimed 404 of an id that never resolved.
			return resp
		}
		// A reset landed after ResolveJob, so the job is gone — but this cancel
		// claimed its attempt, and the 404 is the served response to it.
		resp.FaultEligible, resp.FaultBody = true, faultBody(surfaceAgent)
		return resp
	case provider.CancelFailed:
		// CancelJob recorded the error finding that says why. It is the
		// vendor's 500 built here, not through validationResponse, which would
		// answer a finding it does not classify with the Agent's validation 400
		// — a fault of the request, which this is not. The wire carries the
		// plain 500, never the finding's message, which names the job.
		return errorResponse(surfaceAgent, http.StatusInternalServerError, "")
	}
	x.Fail(CodeRenderFailed, "", "provider.CancelJob answered outcome %q, which this profile does not render", outcome)
	return errorResponse(surfaceAgent, http.StatusInternalServerError, "")
}

// cancelAccepted renders the cancel's 200 for id: served and fault-eligible, so
// the attempt the cancel claimed applies to it.
func cancelAccepted(x *provider.Exchange, id, label string) provider.Response {
	body, err := provider.Render(cancelResponse{ResponseID: id, Status: statusCancelling}, nil, nil)
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

// cancelTerminal renders the 400 of a cancel of a run already terminal: the
// specification's documented answer, so a served, fault-eligible response, not
// a validation rejection.
func cancelTerminal() provider.Response {
	resp := errorResponse(surfaceAgent, http.StatusBadRequest, cancelTerminalMessage)
	resp.Label = "perplexity.agent.cancel.terminal"
	resp.FaultEligible, resp.FaultBody = true, faultBody(surfaceAgent)
	return resp
}

// terminalSnapshot reports whether a background snapshot ends the run, by
// backgroundTerminal: the predicate a cancel judges the run's next retrieve by.
// A snapshot that does not decode, or whose status is outside the enum, is not
// terminal; load has already refused it.
func terminalSnapshot(t *scenario.Turn) bool {
	var p perplexityAgent
	if scenario.DecodeStrict(&t.Respond, &p) != nil {
		return false
	}
	terminal, known := backgroundTerminal(p.Status)
	return terminal && known
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

// backgroundBody is what one background route always serves, which a scripted
// body may not contradict: the route as a finding names it, what it serves in
// place of a stream, where its extra_fields are written, and the extra_fields
// keys refused there, each with the reason.
//
// Extra fields are merged into the body last and win — a snapshot's by
// provider.Render, a fault attempt's by the fault executor — so a refused key
// would let a route contradict what it is. An attempt that sets nothing but
// extra_fields is no fault in the journal either, so its entry would carry no
// fault_kind to read the label beside.
type backgroundBody struct {
	route, serves, where string
	refused              []struct{ key, why string }
}

// retrieveBody is GET /v1/agent/{id}'s: a snapshot, whose id is always its
// job's and whose status the journal label reports. It covers a background
// turn's snapshot and each of the turn's fault attempts.
var retrieveBody = backgroundBody{
	route:  patternAgentRetrieve,
	serves: "a background snapshot is always an ordinary JSON body",
	where:  "on a background turn",
	refused: []struct{ key, why string }{
		{"id", "a snapshot's id is always its job's"},
		{"status", "a snapshot's status is its respond.status, which the journal label reports and the " +
			"terminal check judged"},
	},
}

// cancelBody is POST /v1/agent/{id}/cancel's: the specification's fixed
// acknowledgement, {response_id, status}, or an error. It covers each attempt
// of background.cancel.fault. A key the acknowledgement does not carry is the
// additive field extra_fields exists for, so only its own two are refused.
var cancelBody = backgroundBody{
	route:  patternAgentCancel,
	serves: "its acknowledgement and its errors are always ordinary JSON bodies",
	where:  "in background.cancel.fault",
	refused: []struct{ key, why string }{
		{"response_id", "the acknowledgement's response_id is always its job's id"},
		{"status", "the acknowledgement's status is always cancelling, the one value the specification gives it"},
	},
}

// validateBackground decodes and checks the entry's background scripts: the
// retrieve script, each turn addressed providers.<name>.background.turns[i],
// and the cancel script beside it, providers.<name>.background.cancel.turns[i].
//
// Each snapshot gets the per-projection checks a synchronous turn gets, and not
// the stream-script checks: no stream is served. Each script as a whole is
// judged the way an async poll script is — terminal absorbing in serve order,
// and an unconditional last turn — on its own, because a cancel is recorded
// only while the run's next retrieve is pending, so a terminal retrieve script
// followed by a pending cancel script is no run that un-completes. Each turn's
// `when.route` is checked against the retrieve route alone, in both scripts:
// the cancel judges and answers by the snapshot the next retrieve serves, so a
// turn naming the cancel route could never fire. The framework leaves that
// check to this validator (provider.Profile.Backgroundable): the entry's own
// RouteLister lists the create spellings, and a background turn naming one
// would load clean and never fire. `completed` is allowed in the cancel
// script, to script a run that was acknowledged and completed anyway.
//
// The cancel route's own fault plan, background.cancel.fault, is checked
// against what the cancel serves, as each retrieve turn's plan is against what
// the retrieve serves (backgroundFaultFindings); scenario.Validate has judged
// only its shape.
func validateBackground(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	if e == nil || e.Background == nil {
		return nil
	}
	base := "providers." + e.Name + ".background"
	findings := validateBackgroundScript(s, base+".turns", e.Background.Turns, false)
	if c := e.Background.Cancel; c != nil {
		findings = append(findings, backgroundFaultFindings(base+".cancel.fault", c.Fault, cancelBody)...)
		findings = append(findings, validateBackgroundScript(s, base+".cancel.turns", c.Turns, true)...)
	}
	return findings
}

// validateBackgroundScript decodes and checks one background script, each turn
// addressed base[i]. It is the one walk the retrieve script and the cancel
// script share. afterCancel marks the cancel script, whose call_index counts
// retrieves since the cancel and whose findings say so.
func validateBackgroundScript(
	s *scenario.Scenario, base string, turns []scenario.Turn, afterCancel bool,
) []scenario.Finding {
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
			findings = append(findings, backgroundExtraFieldFindings(path, p.ExtraFields, retrieveBody)...)
			findings = append(findings, s.ResolveRefs(path, &p)...)
			findings = append(findings, validateAgentProjection(path, &p)...)
		}
		findings = append(findings, backgroundTurnFindings(turnPath, &turns[i])...)
	}

	// A cancel peeks the snapshot the run's next retrieve would serve — from
	// this script before a cancel is recorded, from the cancel script once one
	// is, so only a repeated cancel reaches that one — and CancelJob answers a
	// peek that matches nothing with job.cancel_unscripted and the 500.
	since, last, canceller := "", "the last background turn", "a cancel"
	if afterCancel {
		since, last, canceller = " since the cancel", "the last turn of background.cancel.turns", "a repeated cancel"
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
			Message: fmt.Sprintf("retrieve %d%s is served turn %d (%s) after retrieve %d%s was served turn %d (%s), "+
				"which is terminal; turns are served by first match on call_index, not in declaration order, and "+
				"a run does not un-complete",
				r.Poll, since, r.Turn, effectiveStatus(decoded[r.Turn]),
				r.TerminalPoll, since, r.TerminalTurn, effectiveStatus(decoded[r.TerminalTurn])),
		})
	}

	if n := len(turns); n > 0 && backgroundTurnCanMiss(turns[n-1].When) {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityWarning,
			Code:     codeAgentBackgroundScriptExhausted,
			Path:     fmt.Sprintf("%s[%d].when", base, n-1),
			Message: last + " has a condition a retrieve can fail, so the retrieve after it matches no turn and " +
				"answers 404 for a job that exists, and " + canceller + " there answers 500; end the script with a " +
				"turn that has no when, or only route: agent.retrieve",
		})
	}
	return findings
}

// backgroundTurnCanMiss reports whether a background turn's `when` can fail to
// match a retrieve. Every retrieve is served by the retrieve route, so a route
// condition naming it matches them all; with that one condition set aside,
// whatever is left — a call_index, a body predicate, a route that is not the
// retrieve's — can miss. Setting the route aside on a copy, rather than listing
// the other fields, keeps a condition added to scenario.Match later on the
// warning side.
func backgroundTurnCanMiss(w *scenario.Match) bool {
	if w == nil {
		return false
	}
	rest := *w
	if scenario.RouteMatches(rest.Route, faultKeyAgentRetrieve) {
		rest.Route = ""
	}
	return !rest.IsEmpty()
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
// refused too: the author still wrote the key. extra_fields is read decoded
// instead (backgroundExtraFieldFindings).
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
			Code:     codeAgentBackgroundField,
			Path:     path + "." + key,
			Message: fmt.Sprintf("%s is not allowed in a background snapshot: a snapshot's id is always its "+
				"job's, and GET /v1/agent/{id} serves no stream; remove %s", key, key),
		})
	}
	return findings
}

// backgroundExtraFieldFindings refuses an extra_fields key body's route may not
// be given, path being the respond or the fault attempt that declares extra.
// It reads the DECODED map, whose keys are the strings the wire carries: a
// raw-YAML read decodes a mapping with any non-string key (`1`, `true`) into a
// map not keyed by string, and would then see no id at all, while the
// projection's decode coerces each key to its text, so the id is still served.
// A key written with a null or empty value is still in the decoded map.
//
// A key is refused in every spelling strings.EqualFold matches, which is how
// Go's encoding/json matches an object key to a field: case-insensitively under
// Unicode simple folding, the last match winning. The merged body's keys come
// out sorted, so "ſtatus" (U+017F) is read after "status" and replaces it in a
// Go client's struct while the journal label still reports the real one. Each
// finding names the key as written. Findings follow body.refused and then the
// sorted keys, never Go's map iteration, so two loads report alike.
func backgroundExtraFieldFindings(path string, extra scenario.ExtraFields, body backgroundBody) []scenario.Finding {
	keys := slices.Sorted(maps.Keys(extra))
	var findings []scenario.Finding
	for _, f := range body.refused {
		for _, key := range keys {
			if !strings.EqualFold(key, f.key) {
				continue
			}
			folded := ""
			if key != f.key {
				folded = fmt.Sprintf("keys are compared the way a JSON decoder compares them, case-insensitively, "+
					"so %s is read as %s; ", key, f.key)
			}
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     codeAgentBackgroundField,
				Path:     path + ".extra_fields." + key,
				Message: fmt.Sprintf("extra_fields.%s is not allowed %s: %sextra fields are merged into the body "+
					"last and win, and %s; remove extra_fields.%s", key, body.where, folded, f.why, key),
			})
		}
	}
	return findings
}

// backgroundFaultFindings checks each attempt of a background route's fault
// plan, addressed faultPath.attempts[j], against what body's route serves: a
// stream_* kind can never play on it, and a refused key in the attempt's
// extra_fields would contradict it. An attempt's `body:` replaces the body
// rather than merging into it, and is not read here. A nil plan has nothing to
// check.
func backgroundFaultFindings(faultPath string, f *scenario.Fault, body backgroundBody) []scenario.Finding {
	if f == nil {
		return nil
	}
	var findings []scenario.Finding
	for j := range f.Attempts {
		attemptPath := fmt.Sprintf("%s.attempts[%d]", faultPath, j)
		if kind := f.Attempts[j].EffectiveKind(); kind.IsStream() {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     scenario.CodeStreamFaultMismatch,
				Path:     attemptPath + ".kind",
				Message: fmt.Sprintf("kind %q assumes a chunked SSE transport, but %s never streams: %s",
					kind, body.route, body.serves),
			})
		}
		findings = append(findings, backgroundExtraFieldFindings(attemptPath, f.Attempts[j].ExtraFields, body)...)
	}
	return findings
}

// backgroundTurnFindings checks what a background turn declares beside its
// respond body: its route, its body predicates, and its fault plan, which the
// retrieve route reads.
func backgroundTurnFindings(turnPath string, turn *scenario.Turn) []scenario.Finding {
	var findings []scenario.Finding
	if w := turn.When; w != nil {
		if w.Route != "" && !scenario.RouteMatches(w.Route, faultKeyAgentRetrieve) {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     provider.CodeTurnRouteUnknown,
				Path:     turnPath + ".when.route",
				Message: fmt.Sprintf("route %q never selects a background snapshot: every background snapshot, "+
					"before a cancel and after one, is selected by the retrieve route %q (GET /v1/agent/{id}); the "+
					"create spellings select the entry's own turns, and a cancel selects none",
					w.Route, faultKeyAgentRetrieve),
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
	return append(findings, backgroundFaultFindings(turnPath+".fault", turn.Fault, retrieveBody)...)
}
