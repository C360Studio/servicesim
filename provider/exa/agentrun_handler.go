package exa

import (
	"net/http"

	"github.com/c360studio/servicesim/internal/ids"
	"github.com/c360studio/servicesim/internal/wire"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Agent-run route patterns and fault keys.
const (
	patternRunCreate = "POST /agent/runs"
	patternRunPoll   = "GET /agent/runs/{id}"
	patternRunHead   = "HEAD /agent/runs/{id}"

	// Create and poll draw on SEPARATE budgets, for the same reason exa:search
	// and exa:answer do: a poll retry must not consume the create's retries.
	//
	// Separate keys give separate counters. Separate PLANS take separate
	// Route.Fault selectors reading different scenario locations, which is what
	// createFault and TurnFault do below — two independent counters walking one
	// script would be the same bug in a different place.
	faultKeyRunCreate = "exa:agent_runs.create"
	faultKeyRunPoll   = "exa:agent_runs.poll"

	// HEAD gets its own key so an existence check cannot draw on the poll
	// budget, for the same reason it must not advance the poll cursor.
	faultKeyRunHead = "exa:agent_runs.head"

	// runIDPrefix matches the house shape Exa's own identifiers use. The vendor
	// does not document a run-id format, which contracts/exa/README.md records;
	// this is simulator-chosen and stable.
	runIDPrefix = "run_"
)

// AgentRunRoutes returns the three async routes, in registration order.
//
// All three name the exa_agent_runs ENTRY rather than the listener, so the
// entry's own turn_key and validation block are honoured. Without Route.Entry
// they would silently read the primary `exa` block's.
func AgentRunRoutes() []provider.Route {
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

// handleAgentRunCreate serves POST /agent/runs.
//
// The order is the fail-closed order §4.4 requires: everything that can reject
// runs before MintJob, because MintJob claims the call index.
func handleAgentRunCreate(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	authenticate(x, entry)
	validateAgentRunCreate(x)
	if x.Failed() {
		return rejection(x)
	}

	job, ok := provider.MintJob(x, NameAgentRuns, runIDPrefix, ids.Hex32)
	if !ok {
		return rejection(x)
	}

	body, err := renderRunCreated(x, job.ID)
	if err != nil {
		x.Fail(codeRenderFailed, "", "rendering the Exa agent run failed: %v", err)
		return rejection(x)
	}
	return provider.Response{
		Status:        http.StatusCreated,
		Body:          body,
		Label:         "exa.agent_runs.created",
		FaultEligible: true,
		FaultBody:     func(a scenario.FaultAttempt) []byte { return faultBody(job.ID, a) },
	}
}

// handleAgentRunPoll serves GET /agent/runs/{id}.
//
// Resolution runs before turn selection: an identifier this process never minted
// is the vendor's 404 and must not consume a poll from some other run's script.
func handleAgentRunPoll(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	authenticate(x, entry)
	if x.Failed() {
		return rejection(x)
	}

	id := x.Request.PathValue("id")
	if _, found := provider.ResolveJob(x, id); !found {
		return runNotFound(x, id)
	}

	p, ok := selectAgentRunProjection(x, entry)
	if !ok {
		return rejection(x)
	}

	body, err := renderRunSnapshot(x, p, id)
	if err != nil {
		x.Fail(codeRenderFailed, "", "rendering the Exa agent run failed: %v", err)
		return rejection(x)
	}
	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         "exa.agent_runs.polled",
		FaultEligible: true,
		FaultBody:     func(a scenario.FaultAttempt) []byte { return faultBody(id, a) },
	}
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
	entry := x.Entry()
	authenticate(x, entry)
	if x.Failed() {
		return rejection(x)
	}

	if _, found := provider.ResolveJob(x, x.Request.PathValue("id")); !found {
		return provider.Response{Status: http.StatusNotFound, Label: "exa.agent_runs.head.missing"}
	}
	return provider.Response{Status: http.StatusOK, Label: "exa.agent_runs.head.ok"}
}

// runNotFound is the vendor's 404 for an identifier this process does not hold.
//
// It also carries the diagnostic for the multi-replica case: an identifier that
// is well-formed for this scheme, in a namespace that has minted something, is
// the signature of a run created on another replica — or of a reset that dropped
// the records. It changes nothing about the response.
func runNotFound(x *provider.Exchange, id string) provider.Response {
	x.Warn(codeAgentRunNotFound, "id", "no agent run %q exists in this namespace", id)
	status := http.StatusNotFound
	return provider.Response{
		Status: status,
		Body:   errorBody(unclaimedRequestID(x), "Not Found", TagNotFound, status),
		Label:  "exa.error." + TagNotFound,
	}
}

// codeAgentRunNotFound is journaled when a poll names a run this process does
// not hold.
const (
	codeAgentRunNotFound = "exa.agent_run.not_found"
	codeEffortInvalid    = "exa.effort.invalid"
)

// selectAgentRunProjection chooses the snapshot serving this poll and decodes
// it. It claims the attempt index, so it runs only after resolution has
// confirmed the run is real.
func selectAgentRunProjection(x *provider.Exchange, e *scenario.ProviderEntry) (*AgentRunProjection, bool) {
	if e == nil {
		// The scenario declares no async entry. A run cannot have been minted
		// without one, so this is unreachable through a resolved poll; the
		// well-shaped pending snapshot is the safe answer either way.
		return &AgentRunProjection{}, true
	}

	turn, index := provider.SelectTurnFor(x, e)
	if turn == nil {
		return nil, false
	}

	p := &AgentRunProjection{}
	if err := turn.DecodeProjection(NameAgentRuns, index, p); err != nil {
		x.Fail(codeProjectionInvalid, "", "the scenario's Exa agent-run projection could not be decoded: %v", err)
		return nil, false
	}
	return p, true
}

// validateAgentRunCreate checks a create request.
//
// `query` is the only required field. Everything else the vendor documents is
// accepted and not echoed: the create response is derived in full, so validating
// a field this simulator never renders would only reject traffic the live API
// accepts.
func validateAgentRunCreate(x *provider.Exchange) {
	validateContentType(x)
	validateQuery(x)
	if effort, ok := x.String("effort"); ok && !knownEffort(effort) {
		x.Warn(codeEffortInvalid, "effort",
			"effort %q is not one of the seven documented values; the live API may reject it", effort)
	}
}

// knownEffort reports whether an effort value is one the vendor documents.
//
// Seven values, verified 2026-08-15. `max` is beta and gated behind a header the
// simulator does not check: a scenario testing a beta path should not need one.
func knownEffort(v string) bool {
	switch v {
	case "minimal", "low", "medium", "high", "xhigh", "auto", "max":
		return true
	}
	return false
}

// renderRunCreated renders the create response: the identifier and the initial
// status, and nothing else.
func renderRunCreated(x *provider.Exchange, id string) ([]byte, error) {
	return wire.Render(runWire{
		ID:        id,
		Status:    StatusQueued,
		CreatedAt: x.Deps.Scenario.BaseTime().UTC().Format(scenario.PublishedAtLayout),
	}, nil)
}

// renderRunSnapshot renders one poll snapshot.
func renderRunSnapshot(x *provider.Exchange, p *AgentRunProjection, id string) ([]byte, error) {
	out := runWire{
		ID:        id,
		Status:    p.EffectiveStatus(),
		CreatedAt: x.Deps.Scenario.BaseTime().UTC().Format(scenario.PublishedAtLayout),
	}
	if reason := p.EffectiveStopReason(); reason != "" {
		out.StopReason = &reason
	}

	if p.Output != nil {
		rendered := renderOutput(&OutputProjection{
			Content:   p.Output.Structured,
			Grounding: p.Output.Grounding,
		})
		out.Output = &outputWire{
			Text:       p.Output.Text,
			Structured: rendered.Content,
			Grounding:  rendered.Grounding,
		}
	}
	if p.Error != nil {
		out.Error = &errorWire{Code: p.Error.Code, Message: p.Error.Message}
	}
	if p.Usage != nil {
		out.Usage = &usageWire{
			AgentComputeUnits: p.Usage.AgentComputeUnits,
			DataSources:       p.Usage.DataSources,
		}
	}

	// costDollars is emitted on every TERMINAL snapshot whether or not the
	// scenario declares one, because a real terminal run always carries it and a
	// consumer's spend-attribution path needs something to read. A non-terminal
	// run has spent nothing yet and carries none.
	if p.IsTerminal() {
		cost := &costWire{}
		if p.CostDollars != nil {
			cost.Total = p.CostDollars.Total
			cost.DataSources = p.CostDollars.DataSources
		}
		out.CostDollars = cost
	}

	return wire.Render(out, p.ExtraFields)
}

// runWire is the wire shape of an agent run, verified 2026-08-15 —
// see contracts/exa/README.md, including which parts are inferred.
type runWire struct {
	ID          string      `json:"id"`
	Status      string      `json:"status"`
	StopReason  *string     `json:"stopReason,omitempty"`
	CreatedAt   string      `json:"createdAt,omitempty"`
	Output      *outputWire `json:"output,omitempty"`
	Error       *errorWire  `json:"error,omitempty"`
	Usage       *usageWire  `json:"usage,omitempty"`
	CostDollars *costWire   `json:"costDollars,omitempty"`
}

type outputWire struct {
	Text       string      `json:"text,omitempty"`
	Structured any         `json:"structured,omitempty"`
	Grounding  []Grounding `json:"grounding,omitempty"`
}

type errorWire struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type usageWire struct {
	AgentComputeUnits *float64           `json:"agentComputeUnits,omitempty"`
	DataSources       map[string]float64 `json:"dataSources,omitempty"`
}

// costWire carries total plus the confirmed dataSources breakdown, and NOT the
// `search` key /search emits — that one is part of the shared CostDollarsOutput
// schema and is not confirmed on this surface.
type costWire struct {
	Total       float64            `json:"total"`
	DataSources map[string]float64 `json:"dataSources,omitempty"`
}
