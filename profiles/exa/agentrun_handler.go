package exa

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Agent-run route patterns and fault keys.
const (
	patternRunCreate = "POST /agent/runs"
	patternRunPoll   = "GET /agent/runs/{id}"
	patternRunHead   = "HEAD /agent/runs/{id}"
	patternRunCancel = "POST /agent/runs/{id}/cancel"

	// Create, poll and cancel draw on SEPARATE budgets, for the same reason
	// exa:search and exa:answer do: a poll retry must not consume the create's
	// retries, and a cancel retry must consume neither.
	//
	// Separate keys give separate counters. Separate PLANS take separate
	// Route.Fault selectors reading different scenario locations, which is what
	// createFault, TurnFault and cancelFault do below — two independent counters
	// walking one script would be the same bug in a different place.
	faultKeyRunCreate = "exa:agent_runs.create"
	faultKeyRunPoll   = "exa:agent_runs.poll"
	faultKeyRunCancel = "exa:agent_runs.cancel"

	// HEAD gets its own key so an existence check cannot draw on the poll
	// budget, for the same reason it must not advance the poll cursor.
	faultKeyRunHead = "exa:agent_runs.head"

	// runIDPrefix is the prefix AgentRunId documents for new run ids (openapi
	// line 4836, retrieved 2026-10-01). The 32 hex characters after it are this
	// simulator's choice: the spec fixes only the pattern and the 1-200 length,
	// so the 42-character result fits provider.MaxJobIDLen.
	runIDPrefix = "agent_run_"
)

// agentRunRoutes returns the four async routes, in registration order.
//
// All four name the exa_agent_runs ENTRY rather than the listener, so the
// entry's own turn_key and validation block are honoured. Without Route.Entry
// they would silently read the primary `exa` block's.
func agentRunRoutes() []provider.Route {
	laneFromID := []string{provider.LaneFromPath + "id"}
	return []provider.Route{
		{
			Pattern:     patternRunCreate,
			FaultKey:    faultKeyRunCreate,
			Entry:       NameAgentRuns,
			Credentials: authHeaders,
			Fault:       createFault,
		},
		{
			Pattern:  patternRunPoll,
			FaultKey: faultKeyRunPoll,
			Entry:    NameAgentRuns,
			// The poll lane is per JOB. Two runs polled concurrently in one
			// namespace share this route, and a route-keyed cursor would hand each
			// poll the snapshot scripted for the other run.
			LaneFrom:    laneFromID,
			Credentials: authHeaders,
			Fault:       func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, NameAgentRuns) },
		},
		{
			Pattern:     patternRunHead,
			FaultKey:    faultKeyRunHead,
			Entry:       NameAgentRuns,
			LaneFrom:    laneFromID,
			Credentials: authHeaders,
		},
		{
			Pattern:  patternRunCancel,
			FaultKey: faultKeyRunCancel,
			Entry:    NameAgentRuns,
			// Per job, like the poll: every run has its own cancel attempt budget,
			// so one run's failed cancel never spends another's retry.
			LaneFrom:    laneFromID,
			Credentials: authHeaders,
			Fault:       cancelFault,
		},
	}
}

// createFault returns the create route's attempt budget: the `create.fault`
// block on the async entry.
//
// It reads a DIFFERENT scenario location from the poll's plan, which is what
// makes the two budgets independent in substance and not just in name. The
// precedent is answerFault above: two routes on one entry, two selectors, two
// places to declare a plan.
//
// A turn of an async entry is one poll snapshot, so a plan declared on a turn
// belongs to the poll route; the create has no turn to hang one on.
func createFault(s *scenario.Scenario) *scenario.Fault {
	e := s.Provider(NameAgentRuns)
	if e == nil || e.Create == nil {
		return nil
	}
	if !e.Create.Fault.HasAttempts() {
		return nil
	}
	return e.Create.Fault
}

// cancelFault returns the cancel route's attempt budget: the `cancel.fault`
// block on the async entry, a third location beside createFault's and the poll's
// turn-level plan, so the three budgets are independent in substance.
func cancelFault(s *scenario.Scenario) *scenario.Fault {
	e := s.Provider(NameAgentRuns)
	if e == nil || e.Cancel == nil || !e.Cancel.Fault.HasAttempts() {
		return nil
	}
	return e.Cancel.Fault
}

// handleAgentRunCreate serves POST /agent/runs.
//
// The order is the fail-closed order §4.4 requires: everything that can reject
// runs before MintJob, because MintJob claims the call index.
//
// A scenario that declares no exa_agent_runs entry has no poll script, so a run
// created against it could never be polled honestly: the create is refused,
// before it claims anything, the way any request the scenario has nothing to
// say about is (CLAUDE.md house rule 3).
func handleAgentRunCreate(x *provider.Exchange) provider.Response {
	authenticate(x)
	validateAgentRunCreate(x)
	if x.Failed() {
		return rejection(x)
	}
	if x.Entry() == nil {
		x.Fail(provider.CodeNoMatchingTurn, "",
			"the scenario declares no %s entry, so a run created here could never be polled; "+
				"add a providers.%s block whose turns script its polls", NameAgentRuns, NameAgentRuns)
		return rejection(x)
	}

	id, ok := provider.MintJob(x, NameAgentRuns, runIDPrefix, provider.Hex32)
	if !ok {
		return rejection(x)
	}

	body, err := renderRunCreated(x, id)
	if err != nil {
		x.Fail(codeRenderFailed, "", "rendering the Exa agent run failed: %v", err)
		return rejection(x)
	}
	return provider.Response{
		// 200, not 201: createAgentRun documents only "200" (openapi line 831).
		Status: http.StatusOK,
		// MintJob has claimed the call index, so this id is the per-call one.
		Header:        requestIDHeader(renderedRequestID(x, "")),
		Body:          body,
		Label:         "exa.agent_runs.created",
		FaultEligible: true,
		FaultBody:     agentFaultBody,
	}
}

// handleAgentRunPoll serves GET /agent/runs/{id}.
//
// Resolution runs before turn selection: an identifier this process never minted
// is the vendor's 404 and must not consume a poll from some other run's script.
func handleAgentRunPoll(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	authenticate(x)
	if x.Failed() {
		return rejection(x)
	}

	id := x.Request.PathValue("id")
	if !provider.ResolveJob(x, id) {
		return runNotFound(x, id)
	}

	p, ok := selectAgentRunProjection(x, entry)
	if !ok {
		return rejection(x)
	}

	// The label carries the status the snapshot scripts (running when it
	// scripts none), because the journal keeps no bodies: it is how a consumer
	// sees a poll that confirmed `cancelled`.
	return runSnapshotResponse(x, p, id, "exa.agent_runs.polled."+p.EffectiveStatus())
}

// runSnapshotResponse renders p as the 200 AgentRun of a served poll or cancel:
// fault-eligible, so the attempt the request claimed applies to it.
func runSnapshotResponse(x *provider.Exchange, p *agentRunProjection, id, label string) provider.Response {
	body, err := renderRunSnapshot(x, p, id)
	if err != nil {
		x.Fail(codeRenderFailed, "", "rendering the Exa agent run failed: %v", err)
		return rejection(x)
	}
	return provider.Response{
		Status: http.StatusOK,
		// The poll or cancel has claimed its attempt, so this id is the per-call
		// one.
		Header:        requestIDHeader(runRequestID(x, id)),
		Body:          body,
		Label:         label,
		FaultEligible: true,
		FaultBody:     agentFaultBody,
	}
}

// runRequestID is the x-request-id of a served poll or cancel. The call index
// alone is not enough here: both lanes are per job, so the first poll of every
// job is call 0 of its own lane and would carry the same id as the first poll of
// any other job. The job id joins the tuple, which makes the id distinct per job
// as well as per call and still a pure function of the scenario, the job and the
// call position. The route's fault key is already in the tuple, so a poll and a
// cancel of one job at the same index differ too.
func runRequestID(x *provider.Exchange, id string) string {
	return provider.Hex32(append(callParts(x), id)...)
}

// handleAgentRunCancel serves POST /agent/runs/{id}/cancel (cancelAgentRun).
//
// The spec declares no request body, so none is read: a JSON object is accepted
// and ignored, and only what the shared request lifecycle refuses on every route
// — a body that is not a JSON object — is refused here, before anything is
// claimed.
//
// Authentication and resolution run before provider.CancelJob, which claims the
// cancel lane's attempt: a refused or unknown cancel spends nothing and records
// nothing. From there CancelJob decides and records, and this renders. Every
// outcome that has a snapshot is a 200 AgentRun rendered exactly as a poll of
// the same snapshot would be — the invariant that a cancel response is the
// snapshot the job's next poll returns — and is served, fault-eligible, never a
// rejection, which would strip the attempt the cancel claimed. So is the 404 of
// a run a reset removed after it resolved: that cancel claimed its attempt too.
func handleAgentRunCancel(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	authenticate(x)
	if x.Failed() {
		return rejection(x)
	}

	id := x.Request.PathValue("id")
	if !provider.ResolveJob(x, id) {
		return runNotFound(x, id)
	}

	var turns []scenario.Turn
	var cancel *scenario.CancelPolicy
	if entry != nil {
		turns, cancel = entry.Turns, entry.Cancel
	}
	outcome, turn, turnPath := provider.CancelJob(x, "providers."+NameAgentRuns, turns, cancel,
		faultKeyRunPoll, terminalTurn)

	var label string
	switch outcome {
	case provider.CancelRecorded:
		label = "exa.agent_runs.cancel.accepted"
	case provider.CancelAlreadyCancelling:
		label = "exa.agent_runs.cancel.repeated"
	case provider.CancelTerminal:
		label = "exa.agent_runs.cancel.terminal"
	case provider.CancelUncommitted:
		// The scripted fault replaces this response, and nothing was recorded:
		// the label must not claim the cancel took effect.
		label = "exa.agent_runs.cancel.unrecorded"
	case provider.CancelNotFound:
		if x.HasFinding(provider.CodeJobIDInvalid) {
			// CancelJob refused a request that resolved no job and claimed
			// nothing: the unclaimed 404 of an id that never resolved.
			return runNotFound(x, id)
		}
		// A reset landed after ResolveJob, so the job is gone — but this cancel
		// claimed its attempt, and the 404 is the served response to it, as every
		// CancelJob outcome is.
		resp := runNotFound(x, id)
		resp.Header = requestIDHeader(runRequestID(x, id))
		resp.FaultEligible, resp.FaultBody = true, agentFaultBody
		return resp
	case provider.CancelFailed:
		// CancelJob recorded the error finding that says why, and classify maps
		// it onto the vendor's 500.
		return rejection(x)
	default:
		x.Fail(codeRenderFailed, "", "provider.CancelJob answered outcome %q, which this profile does not render", outcome)
		return rejection(x)
	}

	p, ok := decodeAgentRunProjection(x, turn, turnPath)
	if !ok {
		return rejection(x)
	}
	return runSnapshotResponse(x, p, id, label)
}

// terminalTurn reports whether a poll snapshot ends the run: the projection's
// IsTerminal, the predicate the serve-order check judges every script by. A
// snapshot that does not decode is not terminal, which load has already refused.
func terminalTurn(t *scenario.Turn) bool {
	var p agentRunProjection
	return scenario.DecodeStrict(&t.Respond, &p) == nil && p.IsTerminal()
}

// handleAgentRunHead serves HEAD /agent/runs/{id}.
//
// It answers only the question HEAD asks — does this run exist — and claims
// nothing. Letting HEAD reach the GET handler would run turn selection, claim an
// attempt and advance the run's poll cursor for a request whose body net/http
// then discards: one existence check would silently consume a poll, and the
// author would see a run reach completed a poll early with nothing in the
// journal explaining it.
//
// It reports no status, because reporting one would mean selecting a turn, which
// is the thing it must not do. A client that wants status sends GET.
func handleAgentRunHead(x *provider.Exchange) provider.Response {
	authenticate(x)
	if x.Failed() {
		return rejection(x)
	}

	// HEAD claims nothing, so its id is the unclaimed one: the same value a
	// rejected request carries.
	header := requestIDHeader(unclaimedRequestID(x))
	if !provider.ResolveJob(x, x.Request.PathValue("id")) {
		return provider.Response{Status: http.StatusNotFound, Header: header, Label: "exa.agent_runs.head.missing"}
	}
	return provider.Response{Status: http.StatusOK, Header: header, Label: "exa.agent_runs.head.ok"}
}

// runNotFound is the vendor's 404 for an identifier this process does not hold:
// RUN_NOT_FOUND on the agent envelope.
//
// The multi-replica diagnostic is NOT carried here: provider.ResolveJob has
// already raised it, as a job.foreign_id finding plus a servicesim.job_foreign
// log line, before this function ever runs — handleAgentRunPoll and
// handleAgentRunHead call it to decide whether to reach this branch at all.
// This function only renders the vendor-shaped body; it changes nothing about
// the response either way.
func runNotFound(x *provider.Exchange, id string) provider.Response {
	x.Warn(codeAgentRunNotFound, "id", "no agent run %q exists in this namespace", id)
	return provider.Response{
		Status: http.StatusNotFound,
		Header: requestIDHeader(unclaimedRequestID(x)),
		Body:   agentErrorBody(agentError(http.StatusNotFound, "")),
		Label:  "exa.error." + tagNotFound,
	}
}

// codeAgentRunNotFound is journaled when a poll names a run this process does
// not hold.
const (
	codeAgentRunNotFound = "exa.agent_run.not_found"
	codeEffortInvalid    = "exa.effort.invalid"

	codeBudgetDurationRange = "exa.budget.maxDurationSeconds.range"
	codeBudgetCostRange     = "exa.budget.maxCostDollars.range"
)

// Documented request budget bounds. They live only in the prose of the
// AgentBudget schema ("Accepts 300-10,800", "Accepts $1-$100"; openapi lines 4863
// and 4866, retrieved 2026-10-01), not in minimum/maximum keywords. The same
// prose says each applies only to certain efforts; whether violating THAT is a
// rejection is not stated, so it is not checked.
const (
	minBudgetDurationSeconds = 300
	maxBudgetDurationSeconds = 10800
	minBudgetCostDollars     = 1
	maxBudgetCostDollars     = 100
)

// selectAgentRunProjection chooses the snapshot serving this poll and decodes
// it. It claims the attempt index and records it on the run
// (provider.SelectPollTurn), so it runs only after resolution has confirmed the
// run is real — and every served poll advances the run's position, which is
// what a cancel judges "terminal at cancel time" by.
//
// A scenario with no exa_agent_runs entry has no script to serve, and is not
// special-cased: the create refuses to mint a run against one, but a process
// serving several scenarios can still route a poll of a run created under one
// to another that lacks the entry. SelectPollTurn then claims the poll, records
// it on the run, and fails loudly with scenario.no_matching_turn — never a
// default snapshot.
func selectAgentRunProjection(x *provider.Exchange, e *scenario.ProviderEntry) (*agentRunProjection, bool) {
	var turns []scenario.Turn
	var cancel *scenario.CancelPolicy
	if e != nil {
		turns, cancel = e.Turns, e.Cancel
	}
	turn, turnPath := provider.SelectPollTurn(x, "providers."+NameAgentRuns, turns, cancel)
	if turn == nil {
		return nil, false
	}
	return decodeAgentRunProjection(x, turn, turnPath)
}

// decodeAgentRunProjection decodes the snapshot turn — at turnPath, its YAML
// path — into the projection a poll or a cancel renders. It is the one decode
// both share, which is half of what makes a cancel response byte-identical to
// the next poll of the same snapshot.
func decodeAgentRunProjection(
	x *provider.Exchange, turn *scenario.Turn, turnPath string,
) (*agentRunProjection, bool) {
	p := &agentRunProjection{}
	if err := scenario.DecodeStrict(&turn.Respond, p); err != nil {
		x.Fail(codeProjectionInvalid, "", "the scenario's Exa agent-run projection could not be decoded: %s.respond: %v",
			turnPath, err)
		return nil, false
	}

	// Grounding citations carry only a source id until they are resolved against
	// the corpus; without this every citation renders with an empty title and
	// URL. The projection is a fresh value per request, so resolving into it
	// never mutates the scenario and two concurrent polls cannot race here.
	if p.Output != nil {
		path := turnPath + ".respond.output"
		for gi := range p.Output.Grounding {
			for _, f := range x.Deps.Scenario.ResolveRefs(
				fmt.Sprintf("%s.grounding[%d]", path, gi), &p.Output.Grounding[gi],
			) {
				x.Warn(codeSourceUnresolved, "", "%s: %s", f.Path, f.Message)
			}
		}
	}
	return p, true
}

// validateAgentRunCreate checks a create request.
//
// `query` is the only required field. `effort` and the two `budget` limits are
// checked because the spec closes their values. Every other field the vendor
// documents is accepted and not echoed: the create response is derived in full,
// so validating a field this simulator never renders would only reject traffic
// the live API accepts.
func validateAgentRunCreate(x *provider.Exchange) {
	validateContentType(x)
	validateQuery(x)
	validateEffort(x)
	validateBudget(x)
}

// agentDefaultEffort is AgentEffort's declared default (openapi line 4830): a
// request that omits effort runs as auto, so the limits the spec scopes to auto
// apply to it.
const agentDefaultEffort = "auto"

// validateBudget checks the two budget limits the spec states. SIMULATOR-POLICY
// throughout: the spec states the types and the accepted ranges but not how the
// live API answers a violation.
//
//   - A member of the wrong type is always a 400: the schema types both.
//   - A value outside its range is a 400 only when the spec says the limit
//     applies to this request's effort. maxDurationSeconds "applies only to
//     ultra" (line 4866); maxCostDollars "applies only to auto and ultra" (line
//     4863). For any other effort the limit has no meaning, so an out-of-range
//     value is a warning finding — the author sees it — and the request is
//     served unchanged.
func validateBudget(x *provider.Exchange) {
	effort := agentDefaultEffort
	if e, ok := x.String("effort"); ok {
		effort = e
	}

	raw, present := x.Body["budget"]
	if !present {
		return
	}
	budget, ok := raw.(map[string]any)
	if !ok {
		x.Fail(codeFieldType, "budget", "budget must be an object")
		return
	}

	// The offending value is formatted as the float itself: converting an
	// out-of-range float64 to int is implementation-defined in Go, so an int
	// conversion would put different bytes on the wire on arm64 and amd64.
	if v, present := budget["maxDurationSeconds"]; present {
		n, ok := v.(float64)
		switch {
		case !ok || n != math.Trunc(n):
			x.Fail(codeFieldType, "budget.maxDurationSeconds", "budget.maxDurationSeconds must be an integer")
		case n < minBudgetDurationSeconds || n > maxBudgetDurationSeconds:
			budgetRange(x, effort == "ultra", codeBudgetDurationRange, "budget.maxDurationSeconds",
				fmt.Sprintf("%d and %d", minBudgetDurationSeconds, maxBudgetDurationSeconds), "ultra", effort, n)
		}
	}

	if v, present := budget["maxCostDollars"]; present {
		n, ok := v.(float64)
		switch {
		case !ok:
			x.Fail(codeFieldType, "budget.maxCostDollars", "budget.maxCostDollars must be a number")
		case n < minBudgetCostDollars || n > maxBudgetCostDollars:
			budgetRange(x, effort == "auto" || effort == "ultra", codeBudgetCostRange, "budget.maxCostDollars",
				fmt.Sprintf("%d and %d", minBudgetCostDollars, maxBudgetCostDollars), "auto and ultra", effort, n)
		}
	}
}

// budgetRange records an out-of-range budget limit: an error when the limit
// applies to the request's effort, a warning when it does not.
func budgetRange(x *provider.Exchange, applies bool, code, field, bounds, scope, effort string, got float64) {
	if applies {
		x.Fail(code, field, "%s must be between %s, got %v", field, bounds, got)
		return
	}
	x.Warn(code, field, "%s is %v, outside %s, but it applies only to effort %s and this request's effort is %s; "+
		"it is not rejected", field, got, bounds, scope, effort)
}

// validateEffort rejects an effort outside AgentEffort, which is a closed enum
// (openapi line 4819, retrieved 2026-10-01). An earlier reading of the prose
// pages put `max` in the set and left `ultra` out; the spec has it the other way
// round, so `max` is now an error like any other unknown value.
//
// Rejecting is the simulator's strict-request policy (house rule 5): the spec
// states the enum but not the live API's response to a violation.
func validateEffort(x *provider.Exchange) {
	raw, present := x.Body["effort"]
	if !present {
		return
	}
	if effort, ok := raw.(string); ok && slices.Contains(agentEfforts, effort) {
		return
	}
	// The value is quoted as JSON so a null reads as null and a string as "turbo",
	// not as Go's <nil> and bare words.
	got, err := json.Marshal(raw)
	if err != nil {
		got = []byte("an unrepresentable value")
	}
	x.Fail(codeEffortInvalid, "effort", "effort must be one of %s, got %s",
		strings.Join(agentEfforts, ", "), got)
}

// agentEfforts is AgentEffort, in the spec's order.
var agentEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "auto", "ultra"}

// renderRunCreated renders the create response: the run in its initial queued
// status, every required key at its placeholder value.
func renderRunCreated(x *provider.Exchange, id string) ([]byte, error) {
	return provider.Render(newRunWire(x, id, &agentRunProjection{Status: statusQueued}), nil, nil)
}

// renderRunSnapshot renders one poll snapshot.
func renderRunSnapshot(x *provider.Exchange, p *agentRunProjection, id string) ([]byte, error) {
	return provider.Render(newRunWire(x, id, p), p.ExtraFields, nil)
}

// newRunWire builds the AgentRun for one snapshot.
//
// Every key AgentRun requires is always set. The spec fixes the keys and their
// types but has no example bodies, so the VALUES of the keys a snapshot does not
// script are simulator policy, recorded in contracts/README.md: completedAt is
// null until the snapshot is terminal and then the scenario's base time (no
// elapsed-time model), request is null (the job record holds no request body),
// and output, usage and costDollars are the placeholders their projections
// document. A value the scenario scripted always wins over a placeholder.
func newRunWire(x *provider.Exchange, id string, p *agentRunProjection) runWire {
	created := x.Deps.Scenario.BaseTime().UTC().Format(scenario.PublishedAtLayout)
	out := runWire{
		ID:          id,
		Object:      runObject,
		Status:      p.EffectiveStatus(),
		CreatedAt:   created,
		Output:      renderAgentOutput(p.Output),
		Usage:       renderAgentUsage(p.Usage),
		CostDollars: renderAgentCost(p.CostDollars),
	}
	// stopReason has no `omitempty`: it is required and nullable, present and
	// explicitly null while queued or running, not absent. A nil pointer here
	// renders the JSON `null` the schema promises.
	if reason := p.EffectiveStopReason(); reason != "" {
		out.StopReason = &reason
	}
	if p.IsTerminal() {
		out.CompletedAt = &created
	}
	return out
}

// renderAgentOutput renders AgentRunOutput. All three keys are required, so an
// undeclared output is the empty placeholder, not an absent key.
//
// It does not share renderOutput with /search: AgentCitation is {url, title} and
// carries no `id`, and AgentGrounding requires `citations` even when empty.
func renderAgentOutput(o *agentOutputProjection) outputWire {
	out := outputWire{Grounding: []agentGroundingWire{}}
	if o == nil {
		return out
	}
	out.Text = o.Text
	out.Structured = o.Structured
	for _, g := range o.Grounding {
		entry := agentGroundingWire{
			Field:      g.Field,
			Citations:  make([]agentCitationWire, 0, len(g.Citations)),
			Confidence: g.Confidence,
		}
		for _, ref := range g.Citations {
			src := scenario.Render(ref)
			entry.Citations = append(entry.Citations, agentCitationWire{URL: src.URL, Title: src.Title})
		}
		out.Grounding = append(out.Grounding, entry)
	}
	return out
}

// renderAgentUsage renders AgentUsage. An undeclared usage is all zeros: a
// placeholder for four required keys, not a claim about what the vendor meters.
func renderAgentUsage(u *agentUsageProjection) usageWire {
	if u == nil {
		return usageWire{}
	}
	return usageWire{
		AgentComputeUnits: u.AgentComputeUnits,
		Searches:          u.Searches,
		Emails:            u.Emails,
		PhoneNumbers:      u.PhoneNumbers,
		DataSources:       u.DataSources,
	}
}

// renderAgentCost renders AgentCostDollars. An undeclared cost is all zeros, on
// the same placeholder terms as renderAgentUsage.
func renderAgentCost(c *agentCostProjection) costWire {
	if c == nil {
		return costWire{}
	}
	var total float64
	if c.Total != nil {
		total = *c.Total
	}
	return costWire{
		Total:        total,
		AgentCompute: c.AgentCompute,
		Search:       c.Search,
		Emails:       c.Emails,
		PhoneNumbers: c.PhoneNumbers,
		DataSources:  c.DataSources,
	}
}

// runObject is AgentRun.object, a constant in the schema.
const runObject = "agent_run"

// runWire is the wire shape of an agent run: AgentRun in the vendor's OpenAPI
// document (retrieved 2026-10-01, line 5793). The schema is
// additionalProperties: false with all ten keys required, so none carries
// `omitempty`, and the field order is the schema's.
type runWire struct {
	ID          string  `json:"id"`
	Object      string  `json:"object"`
	Status      string  `json:"status"`
	StopReason  *string `json:"stopReason"`
	CreatedAt   string  `json:"createdAt"`
	CompletedAt *string `json:"completedAt"`

	// Request is always null, which the schema permits (anyOf AgentRunRequest |
	// null). Echoing the request would mean retaining the create body, and the
	// job record is immutable and body-free by design; the echo is deferred.
	Request any `json:"request"`

	Output      outputWire `json:"output"`
	Usage       usageWire  `json:"usage"`
	CostDollars costWire   `json:"costDollars"`
}

// outputWire is AgentRunOutput: text, structured and grounding are all required.
type outputWire struct {
	Text string `json:"text"`

	// Structured is `JsonValue | null`, so a nil `any` renders the JSON null the
	// schema allows when no outputSchema was supplied.
	Structured any                  `json:"structured"`
	Grounding  []agentGroundingWire `json:"grounding"`
}

// agentGroundingWire is AgentGrounding: field and citations are required,
// confidence is optional.
type agentGroundingWire struct {
	Field      string              `json:"field"`
	Citations  []agentCitationWire `json:"citations"`
	Confidence string              `json:"confidence,omitempty"`
}

// agentCitationWire is AgentCitation: url is required, title optional, and
// nothing else is allowed.
type agentCitationWire struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// usageWire is AgentUsage. The four counters are required; dataSources lists
// only providers with non-zero usage, so an empty map is omitted.
type usageWire struct {
	AgentComputeUnits float64        `json:"agentComputeUnits"`
	Searches          int            `json:"searches"`
	Emails            int            `json:"emails"`
	PhoneNumbers      int            `json:"phoneNumbers"`
	DataSources       map[string]int `json:"dataSources,omitempty"`
}

// costWire is AgentCostDollars. The five scalars are required and `search` is a
// number here, unlike the {neural} object of the shared CostDollarsOutput.
type costWire struct {
	Total        float64            `json:"total"`
	AgentCompute float64            `json:"agentCompute"`
	Search       float64            `json:"search"`
	Emails       float64            `json:"emails"`
	PhoneNumbers float64            `json:"phoneNumbers"`
	DataSources  map[string]float64 `json:"dataSources,omitempty"`
}
