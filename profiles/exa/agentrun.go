package exa

import (
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// NameAgentRuns is the scenario provider entry for Exa's asynchronous agent
// surface.
//
// It is its own entry rather than a key inside the `exa` block, following the
// perplexity / perplexity_agent precedent exactly: independent auth, validation,
// fault plan and turns. A scenario that uses only /search and /answer omits it
// and is unaffected.
const NameAgentRuns = "exa_agent_runs"

// Run status values: AgentRunStatus in the vendor's OpenAPI document (retrieved
// 2026-10-01, line 5840; see contracts/exa/README.md).
//
// The lifecycle is queued -> running -> completed | failed | cancelled, and the
// last three are terminal. A run is a resource whose payload arrives over
// successive polls, which is the whole reason this surface needs a scenario shape
// a single request/response projection cannot express.
const (
	statusQueued    = "queued"
	statusRunning   = "running"
	statusCompleted = "completed"
	statusFailed    = "failed"
	statusCancelled = "cancelled"
)

// Stop reasons a terminal run carries. A non-terminal run carries JSON null.
//
// The six AgentStopReason members of the vendor's OpenAPI document (retrieved
// 2026-10-01, line 5848). The spec does not pair a status with a reason, so which
// reason a terminal status derives is EffectiveStopReason's policy, not the
// vendor's.
const (
	stopSchemaSatisfied  = "schema_satisfied"
	stopBudgetReached    = "budget_reached"
	stopTimeLimitReached = "time_limit_reached"
	stopStopped          = "stopped"
	stopError            = "error"
	stopCancelled        = "cancelled"
)

// terminalStatuses is the set a poll stops at. It is also what
// ValidateProjections uses to catch a script that un-completes a run.
var terminalStatuses = map[string]bool{
	statusCompleted: true,
	statusFailed:    true,
	statusCancelled: true,
}

// agentRunProjection is ONE POLL SNAPSHOT of an agent run.
//
// That sentence is the whole schema addition and it adds no envelope keys: a
// turn of an async entry is what a single `GET /agent/runs/{id}` returns, and
// the turn cursor walking the list is the run progressing. Two pending turns
// followed by a terminal one is a run that answers "running" twice and then
// completes.
//
// The create response is not projected. It is derived in full — the run in its
// initial queued status, with every required key at its placeholder — because a
// projection body alongside `turns:` is already a load error, so there is
// nowhere honest to put create-side body keys. See contracts/exa/README.md for
// what a create returns.
type agentRunProjection struct {
	// Status is the run's status for this poll. Empty means statusRunning, so a
	// scenario can write a pending snapshot as `respond: {}`.
	Status string `yaml:"status,omitempty"`

	// StopReason overrides the derived stop reason. It is normally left unset:
	// a terminal status implies one, and stating it is only needed to script a
	// budget_reached, time_limit_reached or stopped run that would otherwise
	// derive schema_satisfied.
	StopReason string `yaml:"stop_reason,omitempty"`

	// Output projects the run's output object. The wire object is required and
	// non-nullable on EVERY snapshot, so a snapshot that declares none renders the
	// empty placeholder {text: "", structured: null, grounding: []}; a snapshot
	// that declares one renders it, whatever its status. A completed run with no
	// output is still a warning, not an error — the vendor allows it and a
	// consumer's empty-result branch is worth being able to test.
	Output *agentOutputProjection `yaml:"output,omitempty"`

	// Error is retained ONLY so that a scenario still declaring it fails with a
	// finding that says why, instead of an unhelpful unknown-key error. The
	// AgentRun schema has no run-level error; see codeAgentRunErrorNotInSchema.
	Error *agentErrorProjection `yaml:"error,omitempty"`

	// CostDollars projects the run's cost breakdown. The wire object is required
	// on every snapshot; a snapshot that declares none renders zeros, which are
	// placeholders for required keys and no claim about billing. A declared value
	// always wins over the placeholder.
	CostDollars *agentCostProjection `yaml:"cost_dollars,omitempty"`

	// Usage projects the compute accounting, on the same terms as CostDollars:
	// required on the wire, zero-filled when undeclared, declared values win.
	Usage *agentUsageProjection `yaml:"usage,omitempty"`

	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// agentOutputProjection projects a run's output object.
type agentOutputProjection struct {
	// Text is the natural-language answer or summary (wire: output.text).
	Text string `yaml:"text,omitempty"`

	// Structured is the JSON shaped by the request's outputSchema (wire:
	// output.structured). It is `any` for the same reason the /search structured
	// branch is: the shape is the consumer's schema, not ours.
	Structured any `yaml:"structured,omitempty"`

	// Grounding ties output fields to their citations (wire: output.grounding).
	Grounding []groundingProjection `yaml:"grounding,omitempty"`
}

// agentErrorProjection is the shape of the removed run-level error block. It is
// decoded only to be reported; nothing renders it.
type agentErrorProjection struct {
	Code    string `yaml:"code,omitempty"`
	Message string `yaml:"message,omitempty"`
}

// agentCostProjection projects costDollars on a run: AgentCostDollars in the
// vendor's OpenAPI document (line 6009).
//
// It is NOT costProjection. On /search, costDollars.search is the {neural}
// object of the shared CostDollarsOutput schema; on this surface AgentCostDollars
// is a separate schema whose `search` is a plain number. Copying the /search
// shape across would emit an object where the vendor sends a scalar.
//
// total, agent_compute, search, emails and phone_numbers are all REQUIRED on the
// wire and default to zero when undeclared. Zero is a placeholder for a required
// key: the components are not derived from, and need not sum to, the total.
type agentCostProjection struct {
	// Total is the aggregate (wire: costDollars.total).
	Total float64 `yaml:"total"`

	// AgentCompute, Search, Emails and PhoneNumbers are the per-meter dollar
	// costs (wire: costDollars.agentCompute, .search, .emails, .phoneNumbers).
	AgentCompute float64 `yaml:"agent_compute,omitempty"`
	Search       float64 `yaml:"search,omitempty"`
	Emails       float64 `yaml:"emails,omitempty"`
	PhoneNumbers float64 `yaml:"phone_numbers,omitempty"`

	// DataSources is the per-partner breakdown for Exa Connect sources. Emitted
	// only when a scenario declares it: the spec lists only providers with
	// non-zero usage, so an empty map is omitted rather than rendered.
	DataSources map[string]float64 `yaml:"data_sources,omitempty"`
}

// agentUsageProjection projects the run's usage accounting: AgentUsage in the
// vendor's OpenAPI document (line 5978).
//
// agent_compute_units, searches, emails and phone_numbers are REQUIRED on the
// wire and default to zero when undeclared, on the same placeholder terms as
// agentCostProjection.
type agentUsageProjection struct {
	// AgentComputeUnits measures model computation across the full run (wire:
	// usage.agentComputeUnits).
	AgentComputeUnits float64 `yaml:"agent_compute_units,omitempty"`

	// Searches, Emails and PhoneNumbers are the per-meter counters (wire:
	// usage.searches, .emails, .phoneNumbers). The schema types them as
	// integers.
	Searches     int `yaml:"searches,omitempty"`
	Emails       int `yaml:"emails,omitempty"`
	PhoneNumbers int `yaml:"phone_numbers,omitempty"`

	// DataSources is the per-partner tool-call count (wire: usage.dataSources).
	// The schema types the counts as integers, so a fractional value is a load
	// error rather than a schema-invalid response.
	DataSources map[string]int `yaml:"data_sources,omitempty"`
}

// IsTerminal reports whether this snapshot ends the run, so a poll after it
// keeps returning the same thing rather than advancing to something new.
func (p *agentRunProjection) IsTerminal() bool {
	return terminalStatuses[p.EffectiveStatus()]
}

// EffectiveStatus resolves the snapshot's status, defaulting to running so a
// pending poll can be written as `respond: {}`.
func (p *agentRunProjection) EffectiveStatus() string {
	if p == nil || p.Status == "" {
		return statusRunning
	}
	return p.Status
}

// EffectiveStopReason resolves the stop reason a snapshot reports.
//
// A non-terminal run carries JSON null, which is the vendor's documented shape
// and not an omission. A terminal run derives one from its status unless the
// scenario states otherwise, so the common cases need no `stop_reason:` at all.
func (p *agentRunProjection) EffectiveStopReason() string {
	if p == nil || !p.IsTerminal() {
		return ""
	}
	if p.StopReason != "" {
		return p.StopReason
	}
	switch p.EffectiveStatus() {
	case statusFailed:
		return stopError
	case statusCancelled:
		return stopCancelled
	default:
		return stopSchemaSatisfied
	}
}

// Finding codes the async entry raises at load.
const (
	// CodeAgentRunStatusUnknown is raised for a status outside the documented
	// lifecycle. It is an error: a consumer switching on status would take its
	// default branch for a value the vendor never sends.
	CodeAgentRunStatusUnknown = "exa.agent_run.status.unknown"

	// CodeAgentRunStopReasonUnknown is raised for a stop reason outside the
	// documented set.
	CodeAgentRunStopReasonUnknown = "exa.agent_run.stop_reason.unknown"

	// CodeAgentRunTerminalThenPending is raised for a non-terminal turn declared
	// after a terminal one — a run that un-completes, which no real job API does.
	CodeAgentRunTerminalThenPending = "exa.agent_run.terminal_then_pending"

	// CodeAgentRunScriptExhausted warns that no unconditional final turn exists,
	// so poll N+1 gets scenario.no_matching_turn and a 404 the author did not
	// intend.
	CodeAgentRunScriptExhausted = "exa.agent_run.script_exhausted"

	// CodeAgentRunBodyPredicateOnPoll warns that a turn of an async entry matches
	// on the request body. A GET poll carries no body, so the predicate can never
	// match and the turn can never fire.
	CodeAgentRunBodyPredicateOnPoll = "exa.agent_run.body_predicate_on_poll"

	// CodeAgentRunCompletedWithoutOutput warns that a completed run carries no
	// output. The vendor allows it, so this is not an error — but it is almost
	// always an unfinished fixture.
	CodeAgentRunCompletedWithoutOutput = "exa.agent_run.completed_without_output"
)

// codeAgentRunErrorNotInSchema is raised for an `error:` block on an agent-run
// turn. The AgentRun schema is additionalProperties: false and has no run-level
// error (openapi line 5839), so the block could only put a key outside the schema
// on the wire. It is unexported: a consumer asserts on the string.
const codeAgentRunErrorNotInSchema = "exa.agent_run.error.not_in_schema"

// codeAgentRunValueRange is raised for a scripted usage or cost value outside what
// the schema allows. Every AgentUsage and AgentCostDollars member is a number with
// minimum: 0 (openapi lines 5981-6026), as is every data-source count and cost
// (6003-6007, 6038-6042), and JSON carries no NaN or Infinity, so a negative or
// non-finite value could only put a schema-invalid body on the wire. It is
// unexported: a consumer asserts on the string.
const codeAgentRunValueRange = "exa.agent_run.value.range"

// agentRunValidator decodes and checks the async projections in a scenario.
type agentRunValidator struct{}

// Routes implements provider.RouteLister, so a `when.route:` in an
// exa_agent_runs entry is checked against the async routes alone.
func (agentRunValidator) Routes() []provider.Route { return agentRunRoutes() }

// ProjectionKeys returns the async projection's own top-level keys — the
// vocabulary a turn's `respond:` body under the "exa_agent_runs" entry kind
// may use (Phase 10 unit 8).
func (agentRunValidator) ProjectionKeys() []string {
	return []string{"status", "stop_reason", "output", "error", "cost_dollars", "usage", "extra_fields"}
}

// ValidateProjections decodes every turn of the async entry and reports what it
// finds, addressed by the turn's YAML path.
//
// The last two findings are the ones a fixture author actually hits, and both
// are silent failures without a load-time check: a body predicate on a poll can
// never match, and a script with no unconditional final turn 404s the poll after
// its last snapshot.
func (agentRunValidator) ValidateProjections(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	if e == nil {
		return nil
	}

	var findings []scenario.Finding
	seenTerminal := false

	for i := range e.Turns {
		path := fmt.Sprintf("providers.%s.turns[%d].respond", e.Name, i)

		var p agentRunProjection
		if err := e.Turns[i].DecodeProjection(e.Name, i, &p); err != nil {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     codeProjectionInvalid,
				Path:     path,
				Message:  err.Error(),
			})
			continue
		}

		findings = append(findings, validateAgentRunTurn(s, path, &p, e, i)...)

		// A run that un-completes: a non-terminal snapshot after a terminal one
		// can only be reached by a cursor that has already stopped advancing, so
		// it is unreachable as well as wrong.
		if seenTerminal && !p.IsTerminal() {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     CodeAgentRunTerminalThenPending,
				Path:     path + ".status",
				Message: fmt.Sprintf("turn %d is %q after an earlier turn reached a terminal status; a run does not un-complete",
					i, p.EffectiveStatus()),
			})
		}
		if p.IsTerminal() {
			seenTerminal = true
		}
	}

	findings = append(findings, validateAgentRunFaultTags(e)...)
	return append(findings, validateAgentRunScript(e)...)
}

// validateAgentRunTurn checks one snapshot in isolation.
func validateAgentRunTurn(
	s *scenario.Scenario, path string, p *agentRunProjection, e *scenario.ProviderEntry, index int,
) []scenario.Finding {
	var findings []scenario.Finding

	status := p.EffectiveStatus()
	if !knownStatus(status) {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     CodeAgentRunStatusUnknown,
			Path:     path + ".status",
			Message: fmt.Sprintf("status %q is not one of %s, %s, %s, %s, %s",
				status, statusQueued, statusRunning, statusCompleted, statusFailed, statusCancelled),
		})
	}

	if p.StopReason != "" && !knownStopReason(p.StopReason) {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     CodeAgentRunStopReasonUnknown,
			Path:     path + ".stop_reason",
			Message: fmt.Sprintf("stop_reason %q is not one of %s, %s, %s, %s, %s, %s",
				p.StopReason, stopSchemaSatisfied, stopBudgetReached, stopTimeLimitReached, stopStopped,
				stopError, stopCancelled),
		})
	}

	findings = append(findings, validateAgentRunValues(path, p)...)

	if p.Error != nil {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     codeAgentRunErrorNotInSchema,
			Path:     path + ".error",
			Message: "the live AgentRun schema has no run-level error object (it is additionalProperties: false); " +
				"a failed run is expressed through `status: failed` plus `stop_reason: error`, so remove `error:`",
		})
	}

	if status == statusCompleted && p.Output == nil {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityWarning,
			Code:     CodeAgentRunCompletedWithoutOutput,
			Path:     path + ".output",
			Message:  "a completed run declares no output; the vendor allows it, but this is usually an unfinished fixture",
		})
	}

	// A GET poll has no body, so a body predicate can never match. The turn is
	// dead and nothing at request time would ever say so.
	if when := e.Turns[index].When; when != nil && (when.BodyContains != "" || len(when.BodyJSON) > 0) {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityWarning,
			Code:     CodeAgentRunBodyPredicateOnPoll,
			Path:     fmt.Sprintf("providers.%s.turns[%d].when", e.Name, index),
			Message:  "a poll is a GET and carries no body, so body_contains and body_json can never match here; use call_index or route",
		})
	}

	if p.Output != nil {
		for gi := range p.Output.Grounding {
			findings = append(findings,
				s.ResolveRefs(fmt.Sprintf("%s.output.grounding[%d]", path, gi), &p.Output.Grounding[gi])...)
		}
		// AgentGrounding.confidence is low|medium|high|null. An off-enum value is
		// emitted verbatim with a warning, as on /search.
		findings = append(findings, validateGrounding(&outputProjection{Grounding: p.Output.Grounding}, path)...)
	}
	return findings
}

// validateAgentRunValues rejects a scripted usage or cost value the schema does
// not allow: negative, NaN or infinite. A count that is not an integer never gets
// here — it fails to decode into the projection's int fields, which is its own
// load error.
func validateAgentRunValues(path string, p *agentRunProjection) []scenario.Finding {
	var findings []scenario.Finding
	check := func(field string, v float64) {
		if v >= 0 && !math.IsInf(v, 0) {
			return
		}
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     codeAgentRunValueRange,
			Path:     path + "." + field,
			Message:  fmt.Sprintf("%s is %v; the schema requires a finite number >= 0", field, v),
		})
	}

	if u := p.Usage; u != nil {
		check("usage.agent_compute_units", u.AgentComputeUnits)
		check("usage.searches", float64(u.Searches))
		check("usage.emails", float64(u.Emails))
		check("usage.phone_numbers", float64(u.PhoneNumbers))
		for _, k := range slices.Sorted(maps.Keys(u.DataSources)) {
			check("usage.data_sources."+k, float64(u.DataSources[k]))
		}
	}
	if c := p.CostDollars; c != nil {
		check("cost_dollars.total", c.Total)
		check("cost_dollars.agent_compute", c.AgentCompute)
		check("cost_dollars.search", c.Search)
		check("cost_dollars.emails", c.Emails)
		check("cost_dollars.phone_numbers", c.PhoneNumbers)
		for _, k := range slices.Sorted(maps.Keys(c.DataSources)) {
			check("cost_dollars.data_sources."+k, c.DataSources[k])
		}
	}
	return findings
}

// validateAgentRunScript checks the turn list as a whole.
func validateAgentRunScript(e *scenario.ProviderEntry) []scenario.Finding {
	if len(e.Turns) == 0 {
		return nil
	}
	// A script whose last turn is conditional runs out: the poll after its final
	// snapshot matches nothing, and the consumer sees a 404 for a job that
	// exists. The single-shot form normalises to one unconditional turn, so this
	// only ever fires on a hand-written multi-turn script.
	if last := e.Turns[len(e.Turns)-1]; !last.When.IsEmpty() {
		return []scenario.Finding{{
			Severity: scenario.SeverityWarning,
			Code:     CodeAgentRunScriptExhausted,
			Path:     fmt.Sprintf("providers.%s.turns[%d].when", e.Name, len(e.Turns)-1),
			Message:  "the last turn is conditional, so the poll after it matches no turn and answers 404 for a job that exists",
		}}
	}
	return nil
}

func knownStatus(s string) bool {
	switch s {
	case statusQueued, statusRunning, statusCompleted, statusFailed, statusCancelled:
		return true
	}
	return false
}

func knownStopReason(s string) bool {
	switch s {
	case stopSchemaSatisfied, stopBudgetReached, stopTimeLimitReached, stopStopped, stopError, stopCancelled:
		return true
	}
	return false
}
