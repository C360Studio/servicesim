package exa

import (
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// searchResponseWire is the POST /search response body.
//
// Field order here is wire order: encoding/json emits struct fields in
// declaration order, and the golden fixtures in contracts/exa are compared byte
// for byte, so reordering these fields changes the bytes a consumer's test sees.
type searchResponseWire struct {
	RequestID string       `json:"requestId"`
	Results   []resultWire `json:"results"`

	// CostDollars is always present on a real response and entirely absent from
	// the plan's example. A cost-tracking consumer parses it, which is why it is
	// a value rather than a pointer: there is no "absent" state to express.
	CostDollars costDollars `json:"costDollars"`

	// ResolvedSearchType is a deprecated legacy field that current production
	// responses may return as an empty string. Emitted only when the scenario
	// asks for it, so consumers are not encouraged to branch on it.
	ResolvedSearchType *string `json:"resolvedSearchType,omitempty"`

	// Context is a deprecated combined-context string.
	Context *string `json:"context,omitempty"`

	// Output is present only when the request supplied outputSchema.
	Output *structuredOutputWire `json:"output,omitempty"`
}

// resultWire is one Exa search result. There is deliberately no top-level score
// field: Exa's result schema has none, and emitting one would teach consumers to
// parse something the real API never sends. The only score-like field is
// HighlightScores.
type resultWire struct {
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
	// can happen while searchResponseWire.Results stays a slice of a decodable
	// exported type.
	extra map[string]any
	omit  []string
}

// MarshalJSON renders the result, merges its extra_fields and then drops its
// omit_fields — the order docs/scenario-schema.md documents ("the merge
// happens first and the omission second, so omit_fields can remove a key
// extra_fields added"), matching every other provider package. Earlier
// versions of this method applied the two in the opposite order ("so an
// extra field can reinstate a key that was omitted"); that was a documented
// divergence from the schema, fixed here — see the exa response ordering
// test.
func (r resultWire) MarshalJSON() ([]byte, error) {
	// A defined-type copy drops this method, so the marshal below cannot re-enter
	// it. Forgetting that is an infinite recursion, not a compile error.
	type wireResult resultWire

	return provider.Render(wireResult(r), r.extra, r.omit)
}

// costDollars is the per-request cost breakdown.
type costDollars struct {
	Total float64 `json:"total"`

	// Search carries the breakdown. Its "neural" key survives on the response
	// side even though "neural" was removed from the request type enum, so the
	// key name is emitted verbatim.
	Search *costSearch `json:"search,omitempty"`
}

// costSearch is the search-cost breakdown.
type costSearch struct {
	Neural float64 `json:"neural"`
}

// structuredOutputWire is the structured-output branch of the /search response, present only
// when the request supplied outputSchema. It has no analogue on /answer, which
// overloads its own answer key instead and provides no grounding at all.
type structuredOutputWire struct {
	Content   any             `json:"content,omitempty"`
	Grounding []groundingWire `json:"grounding,omitempty"`
}

// groundingWire ties one field of the structured output to the sources supporting
// it.
type groundingWire struct {
	Field      string                  `json:"field"`
	Citations  []groundingCitationWire `json:"citations,omitempty"`
	Confidence string                  `json:"confidence,omitempty"`
}

// groundingCitationWire is one source backing a grounded field. It is a narrower
// object than either a search result or an answer citation, carrying only the
// three fields the documented example shows.
type groundingCitationWire struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	ID    string `json:"id,omitempty"`
}

// answerResponseWire is the POST /answer response body. It is a distinct document
// rather than a variant of searchResponseWire: there is no results key, and the
// citation object drops highlights, highlightScores, summary, subpages, extras
// and entities.
type answerResponseWire struct {
	RequestID string `json:"requestId"`

	// Answer is a true oneOf on one key: a string by default, or an object
	// matching the scenario's structured answer. /search puts structured output
	// under output.content instead, so a shared serialiser would put it in the
	// wrong place on one of the two routes.
	Answer any `json:"answer"`

	// Citations is optional in the schema and present in every documented
	// example, so it is always emitted — as [] when there are none, never as
	// null and never absent.
	Citations []citationWire `json:"citations"`

	CostDollars costDollars `json:"costDollars"`
}

// citationWire is one source supporting an answer. It carries no score: /answer has
// no highlightScores fallback either, so a relevance-ranking consumer must rank
// by array order alone.
type citationWire struct {
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

// searchTypes is the request `type` enum, quoted verbatim from Exa's own
// validation error message. "neural" is not a member.
var searchTypes = []string{"auto", "fast", "instant", "deep-lite", "deep", "deep-reasoning"}

// categories is the request `category` enum. Two members contain a space. It is
// shared by /search's `category` and /findSimilar's, which document the same
// six values.
var categories = []string{"company", "publication", "news", "personal site", "financial report", "people"}

// contentsResponseWire is the POST /contents response body. Unlike /search and
// /findSimilar it is not a relevance-ranked list: results[] and statuses[] are a
// pure function of the request's ids/urls and the resolution D-a describes, see
// contents.go.
//
// It carries no Context field, unlike searchResponseWire and findSimilarResponseWire.
// contracts/exa/README.md documents a deprecated top-level `context` on
// /contents too (§ Response table, "no example value shown"), but the
// projection has no matching knob to ask for it with — see item 6 of that
// section's "NOT verified" list. Adding one is a parity gap worth closing if a
// consumer ever needs it, not an omission this route's design deliberately
// makes.
type contentsResponseWire struct {
	RequestID string       `json:"requestId"`
	Results   []resultWire `json:"results"`

	// Statuses is documented "yes" always present — one element per requested
	// identifier, in request order, whether it resolved or not.
	Statuses []contentsStatusWire `json:"statuses"`

	CostDollars costDollars `json:"costDollars"`
}

// contentsStatusWire is one element of /contents' statuses[], echoing one requested
// identifier and what became of it.
type contentsStatusWire struct {
	// ID echoes the requested identifier verbatim — the id or url as sent, not a
	// resolved document id.
	ID     string `json:"id"`
	Status string `json:"status"`

	// Source is documented present-but-conditional: both vendor success examples
	// omit it even on status: success, so the default rendering omits it too.
	Source string `json:"source,omitempty"`

	// Error is present only when Status is "error" — both vendor success
	// examples omit the key entirely rather than sending error: null.
	Error *contentsStatusErrorWire `json:"error,omitempty"`
}

// contentsStatusErrorWire is one statuses[].error object.
type contentsStatusErrorWire struct {
	Tag string `json:"tag"`

	// HTTPStatusCode has no `omitempty`: the contract types it
	// anyOf[{integer,100-599}, null], and UNSUPPORTED_URL's documented row gives
	// no code at all — the null branch a consumer must be able to see.
	HTTPStatusCode *int `json:"httpStatusCode"`
}

// findSimilarResponseWire is the POST /findSimilar response body. It is a relevance
// route like /search, over its own results and cost, per D-b: a second
// projection over the same result renderer, not a fetch-shaped route like
// /contents.
type findSimilarResponseWire struct {
	RequestID string `json:"requestId"`

	// Context is deprecated per the vendor's own OpenAPI spec and, like
	// /search's Context, is emitted only when the scenario asks for it.
	Context     *string      `json:"context,omitempty"`
	Results     []resultWire `json:"results"`
	CostDollars costDollars  `json:"costDollars"`
}
