package perplexity

import (
	"time"

	"github.com/c360studio/servicesim/internal/wire"
	"github.com/c360studio/servicesim/scenario"
)

// SunsetDate is when Sonar chat completions stop being supported. The successor
// is the Agent API at /v1/agent. It is logged once at handler construction and
// is deliberately not a per-request finding: it is a property of the simulated
// API rather than of any request, and per-request noise would drown the findings
// that are actionable.
var SunsetDate = time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC)

// ObjectChatCompletion is the constant CompletionResponse.object carries.
const ObjectChatCompletion = "chat.completion"

// ObjectResponse is the constant ResponsesResponse.object carries.
const ObjectResponse = "response"

// RoleAssistant is the role every rendered answer is attributed to.
const RoleAssistant = "assistant"

// -----------------------------------------------------------------------------
// Surface 1 — Sonar
// -----------------------------------------------------------------------------

// CompletionResponse is the POST /v1/sonar response body.
//
// Field order here is the order the golden fixtures carry, which is the order a
// human reads the response in. JSON object order is not part of the contract,
// but keeping the struct and the goldens in step makes a byte-level diff
// readable.
type CompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Model   string   `json:"model"`
	Created int64    `json:"created"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`

	// SearchResults is the field consumers should parse.
	SearchResults []SearchResult `json:"search_results,omitempty"`

	// Citations is a bare URL array, deprecated since May 2025 in favour of
	// SearchResults but still declared in the response schema. It is emitted so
	// existing consumers keep working; contract tests should assert on
	// SearchResults.
	Citations []string `json:"citations,omitempty"`

	Images           []Image  `json:"images,omitempty"`
	RelatedQuestions []string `json:"related_questions,omitempty"`
}

// Choice is one completion choice.
//
// Delta is declared required alongside Message by the specification, which is
// unusual — a non-streaming response still carries a delta object. Servicesim
// emits both, because a consumer generated from the schema will expect both.
type Choice struct {
	Index        int     `json:"index"`
	FinishReason string  `json:"finish_reason"`
	Message      Message `json:"message"`
	Delta        Message `json:"delta"`
}

// Message is a chat message. Content is typed as any because the schema allows a
// plain string or an array of content chunks; the simulator renders a string.
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// SearchResult is one entry of the Sonar search_results array.
//
// It is deliberately not the Agent API's search-result type: that one carries an
// integer id and this one carries none. Sharing a Go type between the two
// surfaces is how one surface's spelling ends up on the other.
type SearchResult struct {
	Title string `json:"title"`
	URL   string `json:"url"`

	// Snippet defaults to the empty string rather than null or absent.
	Snippet string `json:"snippet"`

	// Date and LastUpdated are rendered as an explicit null when the scenario
	// pins no value, rather than omitted. A key that is sometimes missing breaks
	// stricter consumers than a present null does, and a consumer that only ever
	// sees the field absent has not exercised its null branch.
	Date        scenario.Nullable `json:"date,omitzero"`
	LastUpdated scenario.Nullable `json:"last_updated,omitzero"`

	// Source is "web" or "attachment". It is filled from
	// PerplexityResult.SourceType, which carries the YAML key "source_type"
	// because the projection's inlined SourceRef already owns the YAML key
	// "source". The wire key is unaffected: it stays "source".
	Source string `json:"source"`

	// omit names top-level properties this result must not send, backing
	// PerplexityResult.OmitFields. It is unexported because it is a rendering
	// instruction rather than a wire field; the zero value omits nothing.
	omit []string
}

// MarshalJSON renders the result and then drops the properties the scenario
// asked to omit. Omission round-trips the object through a map, so an omitting
// result's keys come back alphabetically ordered; that is deterministic, and
// JSON object order is not part of this contract.
func (r SearchResult) MarshalJSON() ([]byte, error) {
	// A defined-type copy carries the field tags but not this method, so the
	// render below cannot re-enter it. Forgetting that is infinite recursion,
	// not a compile error.
	type plain SearchResult
	base, err := wire.Render(plain(r), nil)
	if err != nil {
		return nil, err
	}
	return wire.Omit(base, r.omit)
}

// Usage is the Sonar token and cost accounting.
//
// Note the token field names: prompt_tokens and completion_tokens. The Agent
// API's usage object spells the same quantities input_tokens and output_tokens.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// SearchContextSize echoes low|medium|high as a string. Encoding it as a
	// number is a common simulator bug.
	SearchContextSize *string `json:"search_context_size,omitempty"`

	CitationTokens   *int `json:"citation_tokens,omitempty"`
	NumSearchQueries *int `json:"num_search_queries,omitempty"`
	ReasoningTokens  *int `json:"reasoning_tokens,omitempty"`

	// Cost is required by the specification and is absent from the plan
	// document's example. A consumer validating against the real schema rejects
	// a usage object that omits it, so it is always emitted.
	Cost Cost `json:"cost"`
}

// Cost is the Sonar per-request dollar breakdown. TotalCost is last because that
// is where the goldens put it: the optional component costs are read alongside
// the two required ones, and the total reads as the sum beneath them.
type Cost struct {
	InputTokensCost     float64  `json:"input_tokens_cost"`
	OutputTokensCost    float64  `json:"output_tokens_cost"`
	ReasoningTokensCost *float64 `json:"reasoning_tokens_cost,omitempty"`
	RequestCost         *float64 `json:"request_cost,omitempty"`
	CitationTokensCost  *float64 `json:"citation_tokens_cost,omitempty"`
	SearchQueriesCost   *float64 `json:"search_queries_cost,omitempty"`
	TotalCost           float64  `json:"total_cost"`
}

// Image is one entry of the Sonar images array.
type Image struct {
	ImageURL  string `json:"image_url"`
	OriginURL string `json:"origin_url,omitempty"`
	Height    int    `json:"height,omitempty"`
	Width     int    `json:"width,omitempty"`
}

// -----------------------------------------------------------------------------
// Surface 2 — Agent API
// -----------------------------------------------------------------------------

// ResponsesResponse is the POST /v1/agent response body.
//
// It shares no field with CompletionResponse. Where Sonar returns one answer in
// choices[], the Agent API returns an ordered execution trace in Output: the
// agent searched, then it answered.
type ResponsesResponse struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Model     string `json:"model"`
	CreatedAt int64  `json:"created_at"`
	Status    string `json:"status"`

	// Output is emitted as an empty array rather than null when the response
	// carries no trace, which a terminal failure does.
	Output []OutputItem `json:"output"`

	// Error is the published ErrorInfo shape, present on a failed response. A
	// consumer that only branches on the HTTP status misses it, which is exactly
	// why a scenario can produce it inside a 200.
	Error *ErrorInfo `json:"error,omitempty"`

	Usage ResponsesUsage `json:"usage"`
}

// Output item type discriminators. The specification declares ten; Servicesim
// renders the two a research adapter parses.
const (
	OutputTypeSearchResults = "search_results"
	OutputTypeMessage       = "message"
)

// ContentPartTypeOutputText is the content-part discriminator for rendered text.
// The generated contract does not enumerate ContentPartType's members; this
// value is inferred from the surface's OpenAI-compatible naming and is recorded
// as such in contracts/perplexity/provenance.yaml.
const ContentPartTypeOutputText = "output_text"

// AnnotationTypeURLCitation is the annotation discriminator for a cited source.
// Like ContentPartTypeOutputText it is inferred rather than enumerated.
const AnnotationTypeURLCitation = "url_citation"

// OutputItem is one element of the Agent API's ordered output trace.
//
// The interface is closed by an unexported method: the specification's item
// types are a fixed union and a consumer must not be able to add a member that
// Servicesim would then render into a trace no vendor produces. The two
// implementations are [SearchResultsOutput] and [MessageOutput].
type OutputItem interface {
	// OutputType returns the item's type discriminator.
	OutputType() string

	// isOutputItem closes the interface.
	isOutputItem()
}

// SearchResultsOutput is the search_results output item: the searches the agent
// reports having run and what they returned.
type SearchResultsOutput struct {
	Type    string              `json:"type"`
	Queries []string            `json:"queries,omitempty"`
	Results []AgentSearchResult `json:"results"`
}

// OutputType returns OutputTypeSearchResults.
func (SearchResultsOutput) OutputType() string { return OutputTypeSearchResults }

func (SearchResultsOutput) isOutputItem() {}

// MessageOutput is the message output item: the agent's answer.
type MessageOutput struct {
	Type    string        `json:"type"`
	ID      string        `json:"id"`
	Role    string        `json:"role"`
	Status  string        `json:"status"`
	Content []ContentPart `json:"content"`
}

// OutputType returns OutputTypeMessage.
func (MessageOutput) OutputType() string { return OutputTypeMessage }

func (MessageOutput) isOutputItem() {}

// ContentPart is one part of a message's content.
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`

	// Annotations is emitted as an empty array rather than omitted, so a
	// consumer's citation-span handling is exercised on every response.
	Annotations []Annotation `json:"annotations"`
}

// Annotation is a url_citation span over the answer text.
type Annotation struct {
	Type       string `json:"type"`
	StartIndex int    `json:"start_index"`
	EndIndex   int    `json:"end_index"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}

// AgentSearchResult is one entry of the Agent API's results array.
//
// ID is an integer. It is the one identifier in this repository that is not a
// string, not a source ID and not a URL, and encoding it as a string is the
// single most likely implementation error on this surface — which is why
// render_test.go asserts on the raw JSON bytes rather than on a round-tripped
// struct. A Go int field and a string field both round-trip cleanly through a
// permissive decoder and the bug survives.
type AgentSearchResult struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	Date        string `json:"date,omitempty"`
	LastUpdated string `json:"last_updated,omitempty"`
	Source      string `json:"source,omitempty"`
}

// ResponsesUsage is the Agent API's token and cost accounting.
//
// The token field names differ from Sonar's UsageInfo: input_tokens and
// output_tokens here, prompt_tokens and completion_tokens there.
type ResponsesUsage struct {
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	TotalTokens  int           `json:"total_tokens"`
	Cost         ResponsesCost `json:"cost"`
}

// ResponsesCost is the Agent API's cost breakdown. Currency, InputCost,
// OutputCost and TotalCost are required by the specification; the cache and tool
// costs are optional and are omitted when zero rather than emitted as 0.
type ResponsesCost struct {
	Currency          string  `json:"currency"`
	InputCost         float64 `json:"input_cost"`
	OutputCost        float64 `json:"output_cost"`
	CacheCreationCost float64 `json:"cache_creation_cost,omitempty"`
	CacheReadCost     float64 `json:"cache_read_cost,omitempty"`
	ToolCallsCost     float64 `json:"tool_calls_cost,omitempty"`
	TotalCost         float64 `json:"total_cost"`
}

// CurrencyUSD is the default ResponsesCost.currency.
const CurrencyUSD = "USD"

// ErrorInfo is the Agent API's published error shape. Only Message is required;
// the code and type values Servicesim sends are its own, because the
// specification declares the fields without enumerating their members.
//
// Field order matches the goldens: code, message, type.
type ErrorInfo struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
}

// AgentErrorResponse is the Agent API's non-422 error envelope.
type AgentErrorResponse struct {
	Error ErrorInfo `json:"error"`
}

// -----------------------------------------------------------------------------
// Error bodies shared by both surfaces
// -----------------------------------------------------------------------------

// ValidationErrorResponse is the FastAPI HTTPValidationError returned for 422 on
// both surfaces. It is the only error body Perplexity formally schematises, and
// the asymmetry with ErrorInfo is real: Sonar is a FastAPI surface whose
// validation errors follow FastAPI's convention, while the Agent API declares
// its own envelope for everything else. Do not unify them.
type ValidationErrorResponse struct {
	Detail []ValidationError `json:"detail"`
}

// ValidationError is one FastAPI validation failure. Loc elements are strings or
// integers in the same array — "messages", then 0, then "role" — which is why it
// is []any and not []string.
type ValidationError struct {
	Loc  []any  `json:"loc"`
	Msg  string `json:"msg"`
	Type string `json:"type"`
}

// MessageErrorResponse is the simulator-chosen body for every non-422 Sonar
// error and for both fail-closed routing statuses, modelled on FastAPI's default
// {"detail": "<string>"}. The specification declares no body for those statuses,
// so this is an inference recorded as unverified in
// contracts/perplexity/provenance.yaml and correcting it from a real response is
// an explicit job for the live contract canary.
//
// Note that detail is an array for 422 and a string here. Both are legal
// FastAPI, and a consumer must not assume one type.
type MessageErrorResponse struct {
	Detail string `json:"detail"`
}
