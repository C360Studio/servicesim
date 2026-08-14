package tavily

import (
	"encoding/json"

	"github.com/c360studio/servicesim/scenario"
)

// SearchResponse is the POST /search response body.
//
// It is the shape a consumer decodes. The renderer marshals an internal twin of
// this struct whose results are pre-rendered bytes, because per-result
// extra_fields and omit_fields are merged into each result before the envelope
// is assembled; render_test.go pins the two shapes together field by field, so
// a field added here without being added there fails a test rather than
// silently vanishing from the wire.
type SearchResponse struct {
	Query string `json:"query"`

	// Answer is in the 200 schema's required list even though its description
	// says it appears only when include_answer is requested — the specification
	// contradicts itself. The simulator emits the key always, null when not
	// requested, because a required key that is sometimes missing breaks
	// stricter consumers than a present null does.
	Answer *string `json:"answer"`

	// Images items are objects, not bare URL strings.
	Images  []Image  `json:"images"`
	Results []Result `json:"results"`

	// ResponseTime is a JSON number. The plan document encodes it as the string
	// "1.15"; the schema declares type number, format float, and a string
	// breaks any typed consumer.
	ResponseTime float64 `json:"response_time"`

	RequestID      string         `json:"request_id,omitempty"`
	AutoParameters map[string]any `json:"auto_parameters,omitempty"`
	Usage          *Usage         `json:"usage,omitempty"`
}

// Result is one Tavily search result.
type Result struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`

	// RawContent is tri-state, and the renderer always sets it: Tavily's own
	// documented example emits raw_content as an explicit null on a request
	// that did not ask for raw content, so the key is present and null rather
	// than absent. omit_fields is how a scenario drops it entirely.
	RawContent scenario.Nullable `json:"raw_content,omitzero"`

	Favicon string  `json:"favicon,omitempty"`
	Images  []Image `json:"images,omitempty"`
	ID      string  `json:"id,omitempty"`

	// PublishedDate is documented in prose for topic=news only, not in the
	// response schema. It is emitted only when the request's topic is "news".
	PublishedDate string `json:"published_date,omitempty"`
}

// Image is one entry of an images array, at the top level or inside a result.
// Items are objects; a bare URL string is not the documented shape.
type Image struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// Usage is the credit-usage object gated by include_usage.
type Usage struct {
	Credits int `json:"credits"`
}

// ErrorResponse is the envelope every documented Tavily error uses — a nested
// object, not a flat {"error": …}. It is the same shape for 400, 401, 429, the
// non-standard 432 and 433, 500, and this simulator's fail-closed 404 and 405.
type ErrorResponse struct {
	Detail ErrorDetail `json:"detail"`
}

// ErrorDetail carries the message.
type ErrorDetail struct {
	Error string `json:"error"`
}

// Non-standard plan-limit statuses Tavily documents alongside the usual ones.
// Neither is in net/http's status table, and a consumer's retry policy that
// branches on 429 alone will not see them.
const (
	// StatusPlanLimitExceeded is "Key limit or Plan Limit exceeded".
	StatusPlanLimitExceeded = 432

	// StatusPayGoLimitExceeded is "PayGo limit exceeded".
	StatusPayGoLimitExceeded = 433
)

// SearchDepths is the search_depth enum. Four values, not the two the plan
// document assumes: a validator that rejects "fast" or "ultra-fast" rejects
// valid current traffic.
var SearchDepths = []string{"advanced", "basic", "fast", "ultra-fast"}

// Topics is the topic enum. Three values; "finance" is current, and the
// vendor's own 400 example text ("Must be 'general' or 'news'") is stale
// against it.
var Topics = []string{"general", "news", "finance"}

// TimeRanges is the time_range enum, long and single-letter aliases alike.
var TimeRanges = []string{"day", "week", "month", "year", "d", "w", "m", "y"}

// IncludeAnswerModes are the string members of include_answer, which is
// oneOf[boolean, string].
var IncludeAnswerModes = []string{"basic", "advanced"}

// IncludeRawContentModes are the string members of include_raw_content, which
// is oneOf[boolean, string].
var IncludeRawContentModes = []string{"markdown", "text"}

// searchBody is the marshalling twin of SearchResponse: identical JSON tags in
// identical order, except that results arrive pre-rendered so that a result's
// own extra_fields and omit_fields can be applied before the envelope closes
// over them.
type searchBody struct {
	Query          string            `json:"query"`
	Answer         *string           `json:"answer"`
	Images         []Image           `json:"images"`
	Results        []json.RawMessage `json:"results"`
	ResponseTime   float64           `json:"response_time"`
	RequestID      string            `json:"request_id,omitempty"`
	AutoParameters map[string]any    `json:"auto_parameters,omitempty"`
	Usage          *Usage            `json:"usage,omitempty"`
}
