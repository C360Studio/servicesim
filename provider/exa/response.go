package exa

import (
	"github.com/c360studio/servicesim/internal/wire"
	"github.com/c360studio/servicesim/scenario"
)

// SearchResponse is the POST /search response body.
//
// Field order here is wire order: encoding/json emits struct fields in
// declaration order, and the golden fixtures in contracts/exa are compared byte
// for byte, so reordering these fields changes the bytes a consumer's test sees.
type SearchResponse struct {
	RequestID string   `json:"requestId"`
	Results   []Result `json:"results"`

	// CostDollars is always present on a real response and entirely absent from
	// the plan's example. A cost-tracking consumer parses it, which is why it is
	// a value rather than a pointer: there is no "absent" state to express.
	CostDollars CostDollars `json:"costDollars"`

	// ResolvedSearchType is a deprecated legacy field that current production
	// responses may return as an empty string. Emitted only when the scenario
	// asks for it, so consumers are not encouraged to branch on it.
	ResolvedSearchType *string `json:"resolvedSearchType,omitempty"`

	// Context is a deprecated combined-context string.
	Context *string `json:"context,omitempty"`

	// Output is present only when the request supplied outputSchema.
	Output *Output `json:"output,omitempty"`
}

// Result is one Exa search result. There is deliberately no top-level score
// field: Exa's result schema has none, and emitting one would teach consumers to
// parse something the real API never sends. The only score-like field is
// HighlightScores.
type Result struct {
	Title string `json:"title"`
	URL   string `json:"url"`

	// ID is documented as "the temporary ID for the document" and Exa's own
	// example is a URL, not an opaque slug.
	ID string `json:"id,omitempty"`

	// PublishedDate is RFC 3339 with millisecond precision and is tri-state:
	// absent, explicit null, or a value. The renderer emits null rather than
	// nothing for a source with no date, because a consumer that only ever sees
	// the field absent has not exercised the null branch; a scenario reaches the
	// absent state with omit_fields.
	PublishedDate scenario.Nullable `json:"publishedDate,omitzero"`

	// Author is explicitly anyOf[string, null] and is tri-state for the same
	// reason.
	Author scenario.Nullable `json:"author,omitzero"`

	Text            string    `json:"text,omitempty"`
	Highlights      []string  `json:"highlights,omitempty"`
	HighlightScores []float64 `json:"highlightScores,omitempty"`
	Summary         string    `json:"summary,omitempty"`
	Image           string    `json:"image,omitempty"`
	Favicon         string    `json:"favicon,omitempty"`

	// extra and omit carry the scenario's per-result extra_fields and
	// omit_fields. They are unexported because they are not wire fields: they
	// are applied by MarshalJSON, which is the only place a per-element merge
	// can happen while SearchResponse.Results stays a slice of a decodable
	// exported type.
	extra map[string]any
	omit  []string
}

// MarshalJSON renders the result, drops the scenario's omit_fields and merges
// its extra_fields.
//
// The order is deliberate: omission applies to the fields this simulator
// projects, and extra fields are additive and therefore applied last, so an
// extra field can reinstate a key that was omitted rather than being silently
// deleted by it.
func (r Result) MarshalJSON() ([]byte, error) {
	// A defined-type copy drops this method, so the marshal below cannot re-enter
	// it. Forgetting that is an infinite recursion, not a compile error.
	type wireResult Result

	body, err := wire.Render(wireResult(r), nil)
	if err != nil {
		return nil, err
	}
	body, err = wire.Omit(body, r.omit)
	if err != nil {
		return nil, err
	}
	return wire.MergeJSON(body, r.extra)
}

// CostDollars is the per-request cost breakdown.
type CostDollars struct {
	Total float64 `json:"total"`

	// Search carries the breakdown. Its "neural" key survives on the response
	// side even though "neural" was removed from the request type enum, so the
	// key name is emitted verbatim.
	Search *CostSearch `json:"search,omitempty"`
}

// CostSearch is the search-cost breakdown.
type CostSearch struct {
	Neural float64 `json:"neural"`
}

// Output is the structured-output branch of the /search response, present only
// when the request supplied outputSchema. It has no analogue on /answer, which
// overloads its own answer key instead and provides no grounding at all.
type Output struct {
	Content   any         `json:"content,omitempty"`
	Grounding []Grounding `json:"grounding,omitempty"`
}

// Grounding ties one field of the structured output to the sources supporting
// it.
type Grounding struct {
	Field      string              `json:"field"`
	Citations  []GroundingCitation `json:"citations,omitempty"`
	Confidence string              `json:"confidence,omitempty"`
}

// GroundingCitation is one source backing a grounded field. It is a narrower
// object than either a search result or an answer citation, carrying only the
// three fields the documented example shows.
type GroundingCitation struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	ID    string `json:"id,omitempty"`
}

// AnswerResponse is the POST /answer response body. It is a distinct document
// rather than a variant of SearchResponse: there is no results key, and the
// citation object drops highlights, highlightScores, summary, subpages, extras
// and entities.
type AnswerResponse struct {
	RequestID string `json:"requestId"`

	// Answer is a true oneOf on one key: a string by default, or an object
	// matching the scenario's structured answer. /search puts structured output
	// under output.content instead, so a shared serialiser would put it in the
	// wrong place on one of the two routes.
	Answer any `json:"answer"`

	// Citations is optional in the schema and present in every documented
	// example, so it is always emitted — as [] when there are none, never as
	// null and never absent.
	Citations []Citation `json:"citations"`

	CostDollars CostDollars `json:"costDollars"`
}

// Citation is one source supporting an answer. It carries no score: /answer has
// no highlightScores fallback either, so a relevance-ranking consumer must rank
// by array order alone.
type Citation struct {
	Title         string            `json:"title"`
	URL           string            `json:"url"`
	ID            string            `json:"id,omitempty"`
	PublishedDate scenario.Nullable `json:"publishedDate,omitzero"`
	Author        scenario.Nullable `json:"author,omitzero"`
	Image         string            `json:"image,omitempty"`
	Favicon       string            `json:"favicon,omitempty"`

	// Text is gated on the request's text flag, which defaults to false. When it
	// is false the key is omitted rather than emitted empty, because that is the
	// only content-gating knob this endpoint has.
	Text string `json:"text,omitempty"`
}

// SearchTypes is the request `type` enum, quoted verbatim from Exa's own
// validation error message. "neural" is not a member.
var SearchTypes = []string{"auto", "fast", "instant", "deep-lite", "deep", "deep-reasoning"}

// Categories is the request `category` enum. Two members contain a space.
var Categories = []string{"company", "publication", "news", "personal site", "financial report", "people"}
