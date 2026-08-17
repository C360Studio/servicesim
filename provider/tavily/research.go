package tavily

import (
	"fmt"
	"net/http"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// NameResearch is the scenario provider entry for Tavily's asynchronous research
// surface. It is its own entry, with independent auth, validation, fault plan
// and turns; a scenario that uses only /search omits it.
const NameResearch = "tavily_research"

// Research route patterns and fault keys.
const (
	PatternResearchCreate = "POST /research"
	PatternResearchPoll   = "GET /research/{request_id}"
	PatternResearchHead   = "HEAD /research/{request_id}"

	// Create and poll draw on separate budgets: a poll retry must not consume
	// the create's retries.
	FaultKeyResearchCreate = "tavily:research.create"
	FaultKeyResearchPoll   = "tavily:research.poll"
	FaultKeyResearchHead   = "tavily:research.head"
)

// Research task statuses, verified 2026-08-15 (contracts/tavily/README.md).
//
// Note there is NO cancelled status here. Exa's agent runs have one; this
// surface does not, and offering one on the strength of the sibling would teach
// a consumer to handle a value the vendor never sends.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// terminalResearch is the set a poll stops at.
var terminalResearch = map[string]bool{StatusCompleted: true, StatusFailed: true}

// researchPlacements are the placements POST /research accepts: the documented
// Bearer header, plus the body api_key Tavily's shipped clients actually send.
//
// The GET poll accepts the header alone, because a GET has no body to carry a
// key in — the difference between the two routes comes from the request shape,
// not from the vendor requiring different schemes. An earlier design asserted the
// vendor required a body key on the POST and a header on the GET; the live
// documentation says Bearer for both, and contracts/tavily/README.md records the
// correction.
var (
	researchCreatePlacements = []string{provider.PlacementAuthorization, PlacementBodyAPIKey}
	researchPollPlacements   = []string{provider.PlacementAuthorization}
)

// ResearchRoutes returns the three research routes, in registration order.
func ResearchRoutes() []provider.Route {
	laneFromID := []string{provider.LaneFromPath + "request_id"}
	pollFault := func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, NameResearch) }

	return []provider.Route{
		{
			Pattern:     PatternResearchCreate,
			FaultKey:    FaultKeyResearchCreate,
			Entry:       NameResearch,
			Credentials: researchCreatePlacements,
			Fault:       researchCreateFault,
		},
		{
			Pattern:     PatternResearchPoll,
			FaultKey:    FaultKeyResearchPoll,
			Entry:       NameResearch,
			LaneFrom:    laneFromID,
			Credentials: researchPollPlacements,
			Fault:       pollFault,
		},
		{
			Pattern:     PatternResearchHead,
			FaultKey:    FaultKeyResearchHead,
			Entry:       NameResearch,
			LaneFrom:    laneFromID,
			Credentials: researchPollPlacements,
		},
	}
}

// researchCreateFault reads the create route's plan from `create.fault` on the
// async entry — a different scenario location from the poll's, which is what
// makes the two budgets independent in substance rather than only in name.
func researchCreateFault(s *scenario.Scenario) *scenario.Fault {
	e := s.Provider(NameResearch)
	if e == nil || e.Create == nil || !e.Create.Fault.HasAttempts() {
		return nil
	}
	return e.Create.Fault
}

// ResearchProjection is one poll snapshot of a research task.
type ResearchProjection struct {
	// Status defaults to StatusPending, so a pending snapshot is `respond: {}`.
	Status string `yaml:"status,omitempty"`

	// Content is the report. It is deliberately `any`: the vendor types it as
	// string OR object — a report when the request supplied no output_schema,
	// structured JSON when it did — and a consumer's decoder has to handle both,
	// so a scenario must be able to produce both.
	Content any `yaml:"content,omitempty"`

	// Sources are the documents the report cites.
	Sources []scenario.SourceRef `yaml:"sources,omitempty"`

	// ResponseTime overrides the reported elapsed seconds. It is a scenario knob
	// rather than a clock read: nothing on a response path may read a real clock.
	ResponseTime *float64 `yaml:"response_time,omitempty"`

	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// EffectiveStatus resolves the snapshot's status, defaulting to pending.
func (p *ResearchProjection) EffectiveStatus() string {
	if p == nil || p.Status == "" {
		return StatusPending
	}
	return p.Status
}

// IsTerminal reports whether this snapshot ends the task. Both completed and
// failed are terminal for the purpose of the HTTP status code (202 while the
// task is live, 200 once it stops) and for the "does not un-complete" check —
// see [ResearchProjection.IsCompleted] for the narrower, fields-gating
// question of which terminal state this is.
func (p *ResearchProjection) IsTerminal() bool {
	return terminalResearch[p.EffectiveStatus()]
}

// IsCompleted reports whether this snapshot is specifically the completed
// terminal state, as opposed to failed. Verified 2026-08-15 against
// contracts/tavily/README.md's research poll section, § The poll's status
// code varies with the task state, and its field table: created_at, content
// and sources appear on a completed poll but NOT on a failed one, even though
// both are 200 OK. Gating those three fields on
// [ResearchProjection.IsTerminal] instead — which this package did before
// today — rendered a spurious created_at on every failed poll.
func (p *ResearchProjection) IsCompleted() bool {
	return p.EffectiveStatus() == StatusCompleted
}

// HTTPStatus is the status code a poll returns for this snapshot.
//
// This is the part of the contract most easily lost: the poll's HTTP status
// VARIES with the task state — 202 while pending or in progress, 200 once
// terminal. A client may branch on it before parsing anything, so answering 200
// throughout would leave a consumer's "202 means still working" path completely
// untested, which is the opposite of what this simulator is for.
func (p *ResearchProjection) HTTPStatus() int {
	if p.IsTerminal() {
		return http.StatusOK
	}
	return http.StatusAccepted
}

// Finding codes the research entry raises.
const (
	// CodeResearchStatusUnknown is raised for a status outside the documented
	// set. It is an error: a consumer switching on status would take its default
	// branch for a value the vendor never sends.
	CodeResearchStatusUnknown = "tavily.research.status.unknown"

	// CodeResearchCompletedWithoutContent warns that a completed task carries no
	// content, which is almost always an unfinished fixture.
	CodeResearchCompletedWithoutContent = "tavily.research.completed_without_content"

	// CodeResearchTerminalThenPending is raised for a non-terminal snapshot after
	// a terminal one — a task that un-completes.
	CodeResearchTerminalThenPending = "tavily.research.terminal_then_pending"

	// CodeResearchInputMissing reports an absent or non-string input, the only
	// required field on a create.
	CodeResearchInputMissing = "tavily.research.input.missing"

	// CodeResearchNotFound is journaled when a poll names a task this process
	// does not hold. It carries no diagnosis of why: a well-formed identifier in
	// a namespace that has minted something already raised provider.ResolveJob's
	// job.foreign_id before handleResearchPoll reaches this branch, and that
	// finding — not this one — names the possible causes.
	CodeResearchNotFound = "tavily.research.not_found"

	// CodeResearchModelInvalid warns about a model outside the documented enum.
	CodeResearchModelInvalid = "tavily.research.model.invalid"
)

// researchIDPrefix is empty: Tavily's request_id is a bare UUID on /search, and
// this surface follows it. The vendor documents no format for /research, which
// contracts/tavily/README.md records.
const researchIDPrefix = ""

// handleResearchCreate serves POST /research.
func handleResearchCreate(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	checkAuth(x, entry)
	validateResearchCreate(x)
	if x.Failed() {
		return errorResponse(x)
	}

	id, ok := provider.MintJob(x, NameResearch, researchIDPrefix, provider.UUIDv5)
	if !ok {
		return errorResponse(x)
	}

	input, _ := x.String("input")
	model, hasModel := x.String("model")
	if !hasModel {
		model = "auto"
	}

	body, err := provider.Render(researchCreatedWire{
		RequestID:    id,
		CreatedAt:    x.Deps.Scenario.BaseTime().UTC().Format(scenario.PublishedAtLayout),
		Status:       StatusPending,
		Input:        input,
		Model:        model,
		ResponseTime: 0,
	}, nil, nil)
	if err != nil {
		x.Fail(CodeProjectionInvalid, "", "the research create response could not be rendered: %v", err)
		return errorResponse(x)
	}

	return provider.Response{
		Status:        http.StatusCreated,
		Body:          body,
		Label:         Name + ".research.created",
		FaultEligible: true,
		FaultBody:     faultBody,
	}
}

// handleResearchPoll serves GET /research/{request_id}.
func handleResearchPoll(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	checkAuth(x, entry)
	if x.Failed() {
		return errorResponse(x)
	}

	id := x.Request.PathValue("request_id")
	if !provider.ResolveJob(x, id) {
		x.Warn(CodeResearchNotFound, "request_id", "no research task %q exists in this namespace", id)
		return staticResponse(http.StatusNotFound, MessageNotFound)
	}

	p, ok := selectResearchProjection(x, entry)
	if !ok {
		return errorResponse(x)
	}

	body, err := renderResearchSnapshot(x, p, id)
	if err != nil {
		x.Fail(CodeProjectionInvalid, "", "the research projection could not be rendered: %v", err)
		return errorResponse(x)
	}

	return provider.Response{
		Status:        p.HTTPStatus(),
		Body:          body,
		Label:         fmt.Sprintf("%s.research.%s", Name, p.EffectiveStatus()),
		FaultEligible: true,
		FaultBody:     faultBody,
	}
}

// handleResearchHead serves HEAD /research/{request_id}: existence only, no turn
// selection, no attempt claimed, no cursor advanced.
func handleResearchHead(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	checkAuth(x, entry)
	if x.Failed() {
		return errorResponse(x)
	}
	if !provider.ResolveJob(x, x.Request.PathValue("request_id")) {
		return provider.Response{Status: http.StatusNotFound, Label: Name + ".research.head.missing"}
	}
	return provider.Response{Status: http.StatusOK, Label: Name + ".research.head.ok"}
}

// selectResearchProjection chooses the snapshot serving this poll. It claims the
// attempt index, so it runs only after resolution confirmed the task is real.
func selectResearchProjection(x *provider.Exchange, e *scenario.ProviderEntry) (*ResearchProjection, bool) {
	if e == nil {
		return &ResearchProjection{}, true
	}
	turn, index := provider.SelectTurnFor(x, e)
	if turn == nil {
		return nil, false
	}
	p := &ResearchProjection{}
	if err := turn.DecodeProjection(NameResearch, index, p); err != nil {
		x.Fail(CodeProjectionInvalid, "", "the research projection could not be decoded: %v", err)
		return nil, false
	}

	// Source references carry only an id until they are resolved against the
	// corpus; without this every sources[] entry renders as empty strings. The
	// projection is a fresh value per request, so resolving into it never mutates
	// the scenario and two concurrent polls cannot race here.
	for _, finding := range x.Deps.Scenario.ResolveRefs(
		fmt.Sprintf("providers.%s.turns[%d].respond", e.Name, index), p,
	) {
		x.Warn(CodeProjectionUnresolved, "", "%s: %s", finding.Path, finding.Message)
	}
	return p, true
}

// validateResearchCreate checks a create request.
//
// The required field is `input`, NOT `query`: this surface does not reuse
// /search's request field name, and accepting `query` here would accept traffic
// the live API rejects — which is the failure mode a strict simulator exists to
// prevent.
//
// A body-placed api_key draws the same warning-severity finding it does on
// /search (warnAPIKeyInBody): contracts/tavily/README.md's research route
// table records the placement as "Same as POST /search". days is NOT checked
// here — it is a /search-only field and has no meaning on a research create.
func validateResearchCreate(x *provider.Exchange) {
	if _, ok := x.String("input"); !ok {
		x.Fail(CodeResearchInputMissing, "input", "Missing input. 'input' is a required string.")
	}
	warnAPIKeyInBody(x)
	if model, ok := x.String("model"); ok && !knownResearchModel(model) {
		x.Warn(CodeResearchModelInvalid, "model",
			"model %q is not one of mini, pro, auto; the live API may reject it", model)
	}
}

func knownResearchModel(v string) bool {
	switch v {
	case "mini", "pro", "auto":
		return true
	}
	return false
}

// renderResearchSnapshot renders one poll body.
//
// A non-terminal task carries only request_id, status and response_time. The
// completed fields — created_at, content and sources — appear ONLY at
// `completed`, not at `failed`: both are 200 OK (IsTerminal), but the
// documented field table gates the three extra fields on IsCompleted alone.
// contracts/tavily/README.md records this split, verified 2026-08-15 against
// <https://docs.tavily.com/documentation/api-reference/endpoint/research-get>. // servicesim:allow-live-host
func renderResearchSnapshot(x *provider.Exchange, p *ResearchProjection, id string) ([]byte, error) {
	out := researchPollWire{
		RequestID:    id,
		Status:       p.EffectiveStatus(),
		ResponseTime: 0,
	}
	if p.ResponseTime != nil {
		out.ResponseTime = *p.ResponseTime
	}

	if p.IsCompleted() {
		out.CreatedAt = x.Deps.Scenario.BaseTime().UTC().Format(scenario.PublishedAtLayout)
		out.Content = p.Content
		for _, ref := range p.Sources {
			src := scenario.Render(ref)
			out.Sources = append(out.Sources, researchSourceWire{
				Title:   src.Title,
				URL:     src.URL,
				Favicon: src.Favicon,
			})
		}
	}
	return provider.Render(out, p.ExtraFields, nil)
}

// researchCreatedWire is the 201 body. All six fields are documented as
// required, so none is omitempty.
type researchCreatedWire struct {
	RequestID    string  `json:"request_id"`
	CreatedAt    string  `json:"created_at"`
	Status       string  `json:"status"`
	Input        string  `json:"input"`
	Model        string  `json:"model"`
	ResponseTime float64 `json:"response_time"`
}

// researchPollWire is the poll body. The completed-only fields are omitempty
// because a pending poll genuinely does not carry them.
type researchPollWire struct {
	RequestID    string               `json:"request_id"`
	Status       string               `json:"status"`
	ResponseTime float64              `json:"response_time"`
	CreatedAt    string               `json:"created_at,omitempty"`
	Content      any                  `json:"content,omitempty"`
	Sources      []researchSourceWire `json:"sources,omitempty"`
}

type researchSourceWire struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Favicon string `json:"favicon,omitempty"`
}

// ResearchValidator decodes and checks the research projections in a scenario.
type ResearchValidator struct{}

// Routes implements provider.RouteLister so a `when.route:` in a research entry
// is checked against the research routes alone.
func (ResearchValidator) Routes() []provider.Route { return ResearchRoutes() }

// ValidateProjections decodes every turn of the research entry.
func (ResearchValidator) ValidateProjections(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	if s == nil || e == nil {
		return nil
	}

	var findings []scenario.Finding
	seenTerminal := false

	for i := range e.Turns {
		path := fmt.Sprintf("providers.%s.turns[%d].respond", e.Name, i)

		var p ResearchProjection
		if err := e.Turns[i].DecodeProjection(e.Name, i, &p); err != nil {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     CodeProjectionInvalid,
				Path:     path,
				Message:  err.Error(),
			})
			continue
		}

		status := p.EffectiveStatus()
		if !knownResearchStatus(status) {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     CodeResearchStatusUnknown,
				Path:     path + ".status",
				Message: fmt.Sprintf("status %q is not one of %s, %s, %s, %s",
					status, StatusPending, StatusInProgress, StatusCompleted, StatusFailed),
			})
		}
		if status == StatusCompleted && p.Content == nil {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityWarning,
				Code:     CodeResearchCompletedWithoutContent,
				Path:     path + ".content",
				Message:  "a completed research task declares no content; this is usually an unfinished fixture",
			})
		}
		if seenTerminal && !p.IsTerminal() {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     CodeResearchTerminalThenPending,
				Path:     path + ".status",
				Message: fmt.Sprintf("turn %d is %q after an earlier turn reached a terminal status; a task does not un-complete",
					i, status),
			})
		}
		if p.IsTerminal() {
			seenTerminal = true
		}

		findings = append(findings, s.ResolveRefs(path, &p)...)
	}
	return findings
}

func knownResearchStatus(s string) bool {
	switch s {
	case StatusPending, StatusInProgress, StatusCompleted, StatusFailed:
		return true
	}
	return false
}
