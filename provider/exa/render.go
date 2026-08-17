package exa

import (
	"cmp"
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Projection is how the shared corpus renders through the Exa API. It is the
// decoded form of a turn's `respond:` body.
//
// It is named Projection rather than ExaProjection — the name the package design
// uses for the same type while it still lived in the scenario package — because
// revive's exported rule rejects exa.ExaProjection as a stutter. The YAML keys
// are unchanged, which is what a scenario file actually depends on.
//
// The reserved envelope keys (kind, auth, validation, fault, turns, turn_key)
// are stripped by the scenario loader before this is decoded, so they are
// deliberately absent here. `extra_fields` is not stripped: every provider
// projection declares its own.
type Projection struct {
	// RequestID overrides the derived 32-character lowercase hex request ID.
	RequestID string `yaml:"request_id,omitempty"`

	Results     []ResultProjection `yaml:"results,omitempty"`
	CostDollars *CostProjection    `yaml:"cost_dollars,omitempty"`
	Output      *OutputProjection  `yaml:"output,omitempty"`

	// ResolvedSearchType and Context project the two deprecated top-level
	// response fields. They exist because the design requires the wire fields to
	// be emitted "only when the scenario asks for it", and the scenario needs a
	// key to ask with.
	ResolvedSearchType *string `yaml:"resolved_search_type,omitempty"`
	Context            *string `yaml:"context,omitempty"`

	// Answer projects the POST /answer endpoint. Absent means /answer returns an
	// empty answer with no citations rather than an error: the route exists on
	// this listener whether or not a scenario has anything to say through it.
	Answer *AnswerProjection `yaml:"answer,omitempty"`

	// Contents projects the POST /contents endpoint's own knobs: its fault plan,
	// per-identifier status overrides and cost. It declares no `results` of its
	// own — D-a is that /contents renders by resolving each requested id/url
	// against THIS projection's own Results (above) and then the corpus, so the
	// same `search:` fixture that answers /search also answers /contents with no
	// extra authoring.
	Contents *ContentsProjection `yaml:"contents,omitempty"`

	// FindSimilar projects the POST /findSimilar endpoint. Unlike Contents, it IS
	// a relevance route — D-b — so it carries its own Results, rendered through
	// the same renderResult as /search.
	FindSimilar *FindSimilarProjection `yaml:"find_similar,omitempty"`

	// Stream selects the behaviour for a request carrying "stream": true.
	// Defaults to StreamWarn: a journal warning plus the ordinary JSON body.
	Stream scenario.StreamPolicy `yaml:"stream,omitempty"`

	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// ResultProjection is one entry of the Exa /search results array.
type ResultProjection struct {
	scenario.SourceRef `yaml:",inline"`

	// ID overrides the wire field results[].id. The default is the source URL,
	// because Exa's own documented example uses a URL there, not an opaque slug.
	// It shadows nothing: the inlined reference is SourceRef.Ref.
	ID string `yaml:"id,omitempty"`

	Text            string    `yaml:"text,omitempty"`
	Highlights      []string  `yaml:"highlights,omitempty"`
	HighlightScores []float64 `yaml:"highlight_scores,omitempty"`
	Summary         string    `yaml:"summary,omitempty"`

	// Score is accepted and never emitted. Exa's result schema has no top-level
	// score field; emitting one would teach consumers to parse something the
	// real API never sends. Setting it raises exa.result.score.not_emitted at
	// load time, so the scenario author learns it was ignored.
	Score *float64 `yaml:"score,omitempty"`

	// OmitFields drops named response fields that would otherwise be present,
	// for tests that assert a consumer fails on a missing required field. It is
	// also the only way to reach the "publishedDate absent" state, since the
	// renderer emits an explicit null for a source with no date.
	OmitFields []string `yaml:"omit_fields,omitempty"`

	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// rawResultProjection is the mapping branch's decode target.
//
// It is a hand-written mirror rather than `type rawResultProjection
// ResultProjection`, and that is not a stylistic choice. A defined-type copy of
// a struct still embeds scenario.SourceRef, and an embedded field's methods are
// promoted to the copy as well — so the copy would still satisfy
// yaml.Unmarshaler through SourceRef.UnmarshalYAML, the decoder would hand it
// the whole result mapping, and every result field would come back as "not found
// in type scenario.rawSourceRef". The scenario package's own warning about
// re-entering a custom unmarshaler covers same-type recursion; this is the
// embedded-type version of it, and it fails just as silently.
//
// SourceRef contributes exactly one YAML key, so the mirror costs one field.
type rawResultProjection struct {
	Ref             string               `yaml:"source"`
	ID              string               `yaml:"id,omitempty"`
	Text            string               `yaml:"text,omitempty"`
	Highlights      []string             `yaml:"highlights,omitempty"`
	HighlightScores []float64            `yaml:"highlight_scores,omitempty"`
	Summary         string               `yaml:"summary,omitempty"`
	Score           *float64             `yaml:"score,omitempty"`
	OmitFields      []string             `yaml:"omit_fields,omitempty"`
	ExtraFields     scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// UnmarshalYAML accepts the scalar shorthand for a source reference as well as
// the mapping form, so `- source-a` and `- {source: source-a, summary: ...}` are
// both valid list entries.
func (r *ResultProjection) UnmarshalYAML(value *yaml.Node) error {
	var raw rawResultProjection
	if err := scenario.DecodeRefOrMapping(value, &r.SourceRef, &raw); err != nil {
		return err
	}
	if value == nil || value.Kind != yaml.MappingNode {
		// The scalar branch has already filled in the reference and there is
		// nothing else on the node to copy.
		return nil
	}
	*r = ResultProjection{
		SourceRef:       scenario.SourceRef{Ref: raw.Ref},
		ID:              raw.ID,
		Text:            raw.Text,
		Highlights:      raw.Highlights,
		HighlightScores: raw.HighlightScores,
		Summary:         raw.Summary,
		Score:           raw.Score,
		OmitFields:      raw.OmitFields,
		ExtraFields:     raw.ExtraFields,
	}
	return nil
}

// CostProjection projects the costDollars object, which is always present on a
// real Exa response and entirely absent from the plan's example.
type CostProjection struct {
	Total  float64  `yaml:"total"`
	Neural *float64 `yaml:"neural,omitempty"`
}

// OutputProjection projects the structured-output branch of the /search
// response, which the wire carries only when the request supplied outputSchema.
type OutputProjection struct {
	Content   any                   `yaml:"content,omitempty"`
	Grounding []GroundingProjection `yaml:"grounding,omitempty"`
}

// GroundingProjection ties one field of the structured output to its citations.
type GroundingProjection struct {
	Field      string               `yaml:"field"`
	Citations  []scenario.SourceRef `yaml:"citations,omitempty"`
	Confidence string               `yaml:"confidence,omitempty"`
}

// AnswerProjection projects the POST /answer response.
type AnswerProjection struct {
	// Fault is /answer's own attempt budget. It lives inside the projection
	// rather than on the provider block because the block-level `fault:` is a
	// reserved envelope key belonging to /search: an /answer call must not
	// consume /search's retries, and vice versa.
	Fault *scenario.Fault `yaml:"fault,omitempty"`

	// Answer is a string by default, or a mapping for the structured-output
	// branch. It is the same wire key either way — unlike /search, which keeps
	// prose and structure in separate places.
	Answer any `yaml:"answer,omitempty"`

	Citations   []scenario.SourceRef `yaml:"citations,omitempty"`
	CostDollars *CostProjection      `yaml:"cost_dollars,omitempty"`
	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// ContentsProjection projects the POST /contents endpoint. It carries no
// `results` of its own — see Projection.Contents — only the route's own fault
// plan, per-identifier status overrides and cost.
type ContentsProjection struct {
	// Fault is /contents' own attempt budget, kept separate from /search's
	// block-level `fault:` and from /findSimilar's, following the answerFault
	// precedent: a retry of one route must not consume another's budget.
	Fault *scenario.Fault `yaml:"fault,omitempty"`

	// Statuses overrides the status D-a's resolution would otherwise compute for
	// one identifier, matched by ID against what the request actually sent. An
	// override naming an identifier the request did not send raises
	// exa.contents.status_unrequested and is not rendered — see
	// resolveContentsIdentifier.
	Statuses []ContentsStatusProjection `yaml:"statuses,omitempty"`

	CostDollars *CostProjection      `yaml:"cost_dollars,omitempty"`
	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// ContentsStatusProjection overrides one statuses[] entry, matched by ID.
type ContentsStatusProjection struct {
	// ID must match a requested identifier exactly (the raw ids[]/urls[]
	// element), not a resolved document id.
	ID string `yaml:"id"`

	// Status overrides the computed status ("success" or "error"). Empty keeps
	// whatever D-a's resolution computed. Setting it to "error" drops the
	// identifier's entry from results[] even if it had resolved, because a
	// vendor statuses[].error is what "this one did not come back" means.
	Status string `yaml:"status,omitempty"`

	// Source overrides statuses[].source (`cached` or `crawled`). The computed
	// default omits this key, matching both vendor success examples.
	Source string `yaml:"source,omitempty"`

	Error *ContentsStatusErrorProjection `yaml:"error,omitempty"`
}

// ContentsStatusErrorProjection projects one statuses[].error object.
type ContentsStatusErrorProjection struct {
	// Tag should be one of the six documented CRAWL_*/SOURCE_*/UNSUPPORTED_URL
	// values; ValidateProjections does not enforce the enum, since an author
	// scripting a fault-injection scenario may deliberately want an unknown tag.
	Tag string `yaml:"tag,omitempty"`

	// HTTPStatusCode is a pointer so the projection can express the documented
	// null case (UNSUPPORTED_URL's row gives no code) as well as omission
	// (defaults to 404, matching D-a's CRAWL_NOT_FOUND default).
	HTTPStatusCode *int `yaml:"http_status_code,omitempty"`
}

// FindSimilarProjection projects the POST /findSimilar endpoint. Per D-b it is
// a relevance route rendered exactly like /search — a second projection over
// the same renderResult — not a fetch-shaped route like /contents.
type FindSimilarProjection struct {
	// Fault is /findSimilar's own attempt budget, independent of /search's,
	// /answer's and /contents'.
	Fault *scenario.Fault `yaml:"fault,omitempty"`

	Results []ResultProjection `yaml:"results,omitempty"`

	CostDollars *CostProjection `yaml:"cost_dollars,omitempty"`

	// Context projects the deprecated top-level `context` field, emitted only
	// when the scenario asks for it — the same rule Projection.Context applies
	// for /search.
	Context *string `yaml:"context,omitempty"`

	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// groundingConfidences is the documented confidence enum.
var groundingConfidences = []string{"low", "medium", "high"}

// contentsStatusValues is statuses[].status's documented enum.
var contentsStatusValues = []string{contentsStatusSuccess, contentsStatusError}

// renderSearch projects the scenario into a /search response body.
//
// Nothing here reads a clock or a random source: identifiers derive from the
// scenario's seed and timestamps from Scenario.BaseTime, so the same scenario and
// request render byte-identical bytes on every run.
func renderSearch(x *provider.Exchange, p *Projection, requestID string) ([]byte, error) {
	limit := requestedNumResults(x)

	results := make([]Result, 0, len(p.Results))
	for i := range p.Results {
		if len(results) >= limit {
			break
		}
		results = append(results, renderResult(&p.Results[i]))
	}

	resp := SearchResponse{
		RequestID:          requestID,
		Results:            results,
		CostDollars:        renderCost(p.CostDollars),
		ResolvedSearchType: p.ResolvedSearchType,
		Context:            p.Context,
	}
	// The structured branch exists on the wire only when the request asked for
	// it. A scenario that declares `output:` and a request that sends no
	// outputSchema is the consumer's mistake to see, not the simulator's to
	// paper over.
	if p.Output != nil && x.Has("outputSchema") {
		resp.Output = renderOutput(p.Output)
	}
	return provider.Render(resp, p.ExtraFields, nil)
}

// renderFindSimilar projects the scenario into a /findSimilar response body.
//
// D-b: this is a relevance route, scripted and rendered exactly like /search —
// a second projection over renderResult — not resolved from the request the
// way /contents is. It shares /search's numResults bounds (1-100, default 10):
// the contract records the same range and default for both.
func renderFindSimilar(x *provider.Exchange, p *Projection, requestID string) ([]byte, error) {
	fs := p.FindSimilar
	if fs == nil {
		fs = &FindSimilarProjection{}
	}

	limit := requestedNumResults(x)
	results := make([]Result, 0, len(fs.Results))
	for i := range fs.Results {
		if len(results) >= limit {
			break
		}
		results = append(results, renderResult(&fs.Results[i]))
	}

	resp := FindSimilarResponse{
		RequestID:   requestID,
		Context:     fs.Context,
		Results:     results,
		CostDollars: renderCost(fs.CostDollars),
	}
	return provider.Render(resp, fs.ExtraFields, nil)
}

// renderAnswer projects the scenario into an /answer response body.
//
// It shares no rendering with renderSearch on purpose. A citation is a strict
// subset of a search result, and emitting a result's extra keys here would teach
// consumers to parse fields the real API never sends on this route.
func renderAnswer(x *provider.Exchange, p *Projection, requestID string) ([]byte, error) {
	a := p.Answer
	if a == nil {
		a = &AnswerProjection{}
	}

	// text defaults to false, and when it is false the key is omitted from every
	// citation rather than emitted empty. It is the only content-gating knob
	// this endpoint has.
	withText, _ := x.Bool("text")

	citations := make([]Citation, 0, len(a.Citations))
	for _, ref := range a.Citations {
		src := scenario.Render(ref)
		citation := Citation{
			Title:         src.Title,
			URL:           src.URL,
			ID:            src.URL,
			PublishedDate: orNull(src.PublishedAt),
			Author:        orNull(src.Author),
			Image:         src.Image,
			Favicon:       src.Favicon,
		}
		if withText {
			citation.Text = src.Text
		}
		citations = append(citations, citation)
	}

	answer := a.Answer
	if answer == nil {
		answer = ""
	}
	resp := AnswerResponse{
		RequestID:   requestID,
		Answer:      answer,
		Citations:   citations,
		CostDollars: renderCost(a.CostDollars),
	}
	return provider.Render(resp, a.ExtraFields, nil)
}

// renderResult projects one canonical source into an Exa result.
func renderResult(rp *ResultProjection) Result {
	src := scenario.Render(rp.SourceRef)
	return Result{
		Title:           src.Title,
		URL:             src.URL,
		ID:              cmp.Or(rp.ID, src.URL),
		PublishedDate:   orNull(src.PublishedAt),
		Author:          orNull(src.Author),
		Text:            cmp.Or(rp.Text, src.Text),
		Highlights:      rp.Highlights,
		HighlightScores: rp.HighlightScores,
		Summary:         rp.Summary,
		Image:           src.Image,
		Favicon:         src.Favicon,
		extra:           rp.ExtraFields,
		omit:            rp.OmitFields,
	}
}

// renderCost projects the cost breakdown, which is always present on the wire
// even when the scenario declares none.
func renderCost(c *CostProjection) CostDollars {
	if c == nil {
		return CostDollars{}
	}
	out := CostDollars{Total: c.Total}
	if c.Neural != nil {
		out.Search = &CostSearch{Neural: *c.Neural}
	}
	return out
}

// renderOutput projects the structured-output branch.
func renderOutput(o *OutputProjection) *Output {
	out := &Output{Content: o.Content}
	if len(o.Grounding) == 0 {
		return out
	}
	out.Grounding = make([]Grounding, 0, len(o.Grounding))
	for _, g := range o.Grounding {
		grounding := Grounding{Field: g.Field, Confidence: g.Confidence}
		if len(g.Citations) > 0 {
			grounding.Citations = make([]GroundingCitation, 0, len(g.Citations))
			for _, ref := range g.Citations {
				src := scenario.Render(ref)
				grounding.Citations = append(grounding.Citations,
					GroundingCitation{Title: src.Title, URL: src.URL, ID: src.URL})
			}
		}
		out.Grounding = append(out.Grounding, grounding)
	}
	return out
}

// orNull turns an absent nullable into an explicit JSON null.
//
// Exa types publishedDate and author as anyOf[string, null], and a consumer that
// only ever sees a field absent has not exercised the null branch it must handle
// in production. The absent state is still reachable, through a result's
// omit_fields.
func orNull(n scenario.Nullable) scenario.Nullable {
	if n.IsZero() {
		return scenario.NullNullable()
	}
	return n
}

// Validation-time finding codes. These are raised once at load, not per request.
const (
	codeProjectionInvalid     = "exa.projection.invalid"
	codeScoreNotEmitted       = "exa.result.score.not_emitted"
	codeStreamPolicy          = "exa.stream.policy.unknown"
	codeConfidenceUnknown     = "exa.output.grounding.confidence.unknown"
	codeAnswerFaultEmpty      = "exa.answer.fault.attempts.empty"
	codeContentsFaultEmpty    = "exa.contents.fault.attempts.empty"
	codeFindSimilarFaultEmpty = "exa.find_similar.fault.attempts.empty"
	codeRenderFailed          = "exa.render.failed"
	codeSourceUnresolved      = "exa.source.unresolved"
	codeContentsStatusInvalid = "exa.contents.statuses.status.invalid"
)

// Validator decodes and checks Exa projection bodies at startup. It implements
// provider.Validator, which is the seam scenario.Validate cannot cross: under
// the open provider registry a `respond:` body is an undecoded YAML node whose
// Go type only this package knows.
//
// It is a zero-sized value with no state, so internal/server registers it as
// exa.Validator{} and testkit can do the same without wiring anything.
type Validator struct{}

// Routes implements provider.RouteLister, so a `when.route:` in an Exa entry is
// checked against the routes this package actually serves.
func (Validator) Routes() []provider.Route { return Routes() }

// ValidateProjections decodes every turn's Exa projection and reports what it
// finds, addressed by the turn's YAML path. internal/server calls it through
// provider.ValidateScenario before readiness reports true, so a fixture with a
// bad field fails at boot rather than on the first request.
//
// It does not mutate the scenario apart from linking source references, which is
// what Resolve would have done had a level-1 package been able to see inside a
// projection body. Linking is idempotent, so calling this more than once is safe.
func (Validator) ValidateProjections(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	if s == nil || e == nil {
		return nil
	}

	var findings []scenario.Finding
	for i := range e.Turns {
		path := respondPath(e, i)

		var p Projection
		if err := e.Turns[i].DecodeProjection(e.Name, i, &p); err != nil {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     codeProjectionInvalid,
				Path:     path,
				Message:  err.Error(),
			})
			continue
		}

		findings = append(findings, s.ResolveRefs(path, &p)...)
		findings = append(findings, validateProjection(&p, path)...)
	}
	return findings
}

// respondPath addresses a turn's projection body the way provider.Validator
// documents: providers.<name>.turns[i].respond, whichever form the author wrote.
func respondPath(e *scenario.ProviderEntry, i int) string {
	return fmt.Sprintf("providers.%s.turns[%d].respond", e.Name, i)
}

// validateProjection checks everything about a decoded projection that decoding
// itself cannot: values outside a documented enum, and fields this simulator
// accepts but deliberately never emits.
func validateProjection(p *Projection, path string) []scenario.Finding {
	var findings []scenario.Finding

	switch p.Stream {
	case "", scenario.StreamWarn, scenario.StreamReject:
	default:
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     codeStreamPolicy,
			Path:     path + ".stream",
			Message:  fmt.Sprintf("unknown stream policy %q; expected warn or reject", p.Stream),
		})
	}

	findings = append(findings, validateResultScores(p.Results, path+".results")...)
	if p.FindSimilar != nil {
		findings = append(findings, validateResultScores(p.FindSimilar.Results, path+".find_similar.results")...)
	}

	if p.Output != nil {
		findings = append(findings, validateGrounding(p.Output, path)...)
	}
	if p.Contents != nil {
		findings = append(findings, validateContentsStatuses(p.Contents, path)...)
	}
	if p.Answer != nil && p.Answer.Fault != nil && !p.Answer.Fault.HasAttempts() {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     codeAnswerFaultEmpty,
			Path:     path + ".answer.fault.attempts",
			Message:  "a fault plan must declare at least one attempt",
		})
	}
	if p.Contents != nil && p.Contents.Fault != nil && !p.Contents.Fault.HasAttempts() {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     codeContentsFaultEmpty,
			Path:     path + ".contents.fault.attempts",
			Message:  "a fault plan must declare at least one attempt",
		})
	}
	if p.FindSimilar != nil && p.FindSimilar.Fault != nil && !p.FindSimilar.Fault.HasAttempts() {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     codeFindSimilarFaultEmpty,
			Path:     path + ".find_similar.fault.attempts",
			Message:  "a fault plan must declare at least one attempt",
		})
	}
	return findings
}

// validateResultScores raises exa.result.score.not_emitted for every result in
// results that sets Score, addressed at resultsPath — a caller-supplied prefix
// so the same check covers both /search's results[] and /findSimilar's own
// find_similar.results[], which reuse the identical ResultProjection type and
// would otherwise silently swallow the same authoring mistake without warning.
func validateResultScores(results []ResultProjection, resultsPath string) []scenario.Finding {
	var findings []scenario.Finding
	for i := range results {
		if results[i].Score == nil {
			continue
		}
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityWarning,
			Code:     codeScoreNotEmitted,
			Path:     fmt.Sprintf("%s[%d].score", resultsPath, i),
			Message: "Exa's result schema has no top-level score field, so this value is accepted and never " +
				"emitted; the only score-like field is highlight_scores",
		})
	}
	return findings
}

// validateContentsStatuses flags a contents.statuses override whose status is
// outside the documented success|error enum. It is a warning, not a rejection,
// following codeConfidenceUnknown's precedent: a scenario author scripting
// fault injection may want an off-enum value on purpose, and this is
// scenario-authoring time, not request-handling time.
func validateContentsStatuses(cp *ContentsProjection, path string) []scenario.Finding {
	var findings []scenario.Finding
	for i, st := range cp.Statuses {
		if st.Status == "" || slices.Contains(contentsStatusValues, st.Status) {
			continue
		}
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityWarning,
			Code:     codeContentsStatusInvalid,
			Path:     fmt.Sprintf("%s.contents.statuses[%d].status", path, i),
			Message: fmt.Sprintf("status %q is outside the documented enum (success, error) and is emitted verbatim",
				st.Status),
		})
	}
	return findings
}

func validateGrounding(o *OutputProjection, path string) []scenario.Finding {
	var findings []scenario.Finding
	for i, g := range o.Grounding {
		if g.Confidence == "" || slices.Contains(groundingConfidences, g.Confidence) {
			continue
		}
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityWarning,
			Code:     codeConfidenceUnknown,
			Path:     fmt.Sprintf("%s.output.grounding[%d].confidence", path, i),
			Message: fmt.Sprintf("confidence %q is outside the documented set %v and is emitted verbatim",
				g.Confidence, groundingConfidences),
		})
	}
	return findings
}
