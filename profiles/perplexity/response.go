package perplexity

import (
	"encoding/json"
	"time"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// sunsetDate is when Sonar chat completions stop being supported. The successor
// is the Agent API at /v1/agent. It is logged once at handler construction and
// is deliberately not a per-request finding: it is a property of the simulated
// API rather than of any request, and per-request noise would drown the findings
// that are actionable.
var sunsetDate = time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC)

// objectChatCompletion is the constant CompletionResponse.object carries.
const objectChatCompletion = "chat.completion"

// objectResponse is the constant ResponsesResponse.object carries.
const objectResponse = "response"

// roleAssistant is the role every rendered answer is attributed to.
const roleAssistant = "assistant"

// -----------------------------------------------------------------------------
// Surface 1 — Sonar
// -----------------------------------------------------------------------------

// completionResponse is the POST /v1/sonar response body.
//
// Field order here is the order the golden fixtures carry, which is the order a
// human reads the response in. JSON object order is not part of the contract,
// but keeping the struct and the goldens in step makes a byte-level diff
// readable.
type completionResponse struct {
	ID      string    `json:"id"`
	Object  string    `json:"object"`
	Model   string    `json:"model"`
	Created int64     `json:"created"`
	Choices []choice  `json:"choices"`
	Usage   usageWire `json:"usage"`

	// SearchResults is the field consumers should parse.
	SearchResults []searchResult `json:"search_results,omitempty"`

	// Citations is a bare URL array, deprecated since May 2025 in favour of
	// SearchResults but still declared in the response schema. It is emitted so
	// existing consumers keep working; contract tests should assert on
	// SearchResults.
	Citations []string `json:"citations,omitempty"`

	Images           []imageWire `json:"images,omitempty"`
	RelatedQuestions []string    `json:"related_questions,omitempty"`
}

// choice is one completion choice.
//
// Delta is declared required alongside Message by the specification, which is
// unusual — a non-streaming response still carries a delta object. Servicesim
// emits both, because a consumer generated from the schema will expect both.
type choice struct {
	Index        int         `json:"index"`
	FinishReason string      `json:"finish_reason"`
	Message      messageWire `json:"message"`
	Delta        messageWire `json:"delta"`
}

// messageWire is a chat message. Content is typed as any because the schema allows a
// plain string or an array of content chunks; the simulator renders a string.
type messageWire struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// searchResult is one entry of the Sonar search_results array.
//
// It is deliberately not the Agent API's search-result type: that one carries an
// integer id and this one carries none. Sharing a Go type between the two
// surfaces is how one surface's spelling ends up on the other.
type searchResult struct {
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
	// perplexityResult.SourceType, which carries the YAML key "source_type"
	// because the projection's inlined SourceRef already owns the YAML key
	// "source". The wire key is unaffected: it stays "source".
	Source string `json:"source"`

	// omit names top-level properties this result must not send, backing
	// perplexityResult.OmitFields. It is unexported because it is a rendering
	// instruction rather than a wire field; the zero value omits nothing.
	omit []string
}

// MarshalJSON renders the result and then drops the properties the scenario
// asked to omit. Omission round-trips the object through a map, so an omitting
// result's keys come back alphabetically ordered; that is deterministic, and
// JSON object order is not part of this contract.
func (r searchResult) MarshalJSON() ([]byte, error) {
	// A defined-type copy carries the field tags but not this method, so the
	// render below cannot re-enter it. Forgetting that is infinite recursion,
	// not a compile error.
	type plain searchResult
	return provider.Render(plain(r), nil, r.omit)
}

// usageWire is the Sonar token and cost accounting.
//
// Note the token field names: prompt_tokens and completion_tokens. The Agent
// API's usage object spells the same quantities input_tokens and output_tokens.
type usageWire struct {
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
	Cost costWire `json:"cost"`
}

// costWire is the Sonar per-request dollar breakdown. TotalCost is last because that
// is where the goldens put it: the optional component costs are read alongside
// the two required ones, and the total reads as the sum beneath them.
type costWire struct {
	InputTokensCost     float64  `json:"input_tokens_cost"`
	OutputTokensCost    float64  `json:"output_tokens_cost"`
	ReasoningTokensCost *float64 `json:"reasoning_tokens_cost,omitempty"`
	RequestCost         *float64 `json:"request_cost,omitempty"`
	CitationTokensCost  *float64 `json:"citation_tokens_cost,omitempty"`
	SearchQueriesCost   *float64 `json:"search_queries_cost,omitempty"`
	TotalCost           float64  `json:"total_cost"`
}

// objectChatCompletionChunk is the constant ChatCompletionChunkResponse.object
// carries.
const objectChatCompletionChunk = "chat.completion.chunk"

// chatCompletionChunkResponse is one GrammarDelta full-mode SSE frame's
// payload for POST /v1/sonar's stream: true response
// (docs/design/streaming.md §7; contracts/perplexity/README.md "Streaming
// (SSE)"). It has no schema in the vendor's OpenAPI document — no
// ChatCompletionChunk/chat.completion.chunk schema exists there, confirmed
// by full-text search — so this shape is reconstructed from
// sonar/pro-search/stream-mode.md's concise-mode examples and recorded as
// simulator-chosen in contracts/perplexity/provenance.yaml.
//
// It is a distinct type from completionResponse, not a reuse of it, because
// the two disagree on one field FastAPI-schema fidelity cannot paper over:
// Choice.FinishReason is a required non-null string on the non-streaming
// body (every choice always finishes stop or length), while a non-terminal
// stream chunk's finish_reason is a present, literal JSON null — a shape
// only a *string field can carry. No citations: they appear in no fetched
// vendor streaming frame, at any scope.
type chatCompletionChunkResponse struct {
	ID      string                      `json:"id"`
	Object  string                      `json:"object"`
	Model   string                      `json:"model"`
	Created int64                       `json:"created"`
	Choices []chatCompletionChunkChoice `json:"choices"`

	// Usage, Images and RelatedQuestions are terminal-only: they are
	// properties of the completed turn, not of an individual delta.
	// Non-terminal chunks leave them nil, which omits the keys.
	Usage            *usageWire     `json:"usage,omitempty"`
	SearchResults    []searchResult `json:"search_results,omitempty"`
	Images           []imageWire    `json:"images,omitempty"`
	RelatedQuestions []string       `json:"related_questions,omitempty"`
}

// chatCompletionChunkChoice is one chunk's single choice.
type chatCompletionChunkChoice struct {
	Index int         `json:"index"`
	Delta messageWire `json:"delta"`

	// Message carries the AGGREGATE content through this chunk — the vendor
	// states full mode aggregates server-side and includes choices.message;
	// the exact field shape is taken from the concise-mode
	// chat.completion.chunk example (contracts/perplexity/README.md), marked
	// there as an inference for full mode.
	Message messageWire `json:"message"`

	// FinishReason is a pointer because a non-terminal chunk carries a
	// present, literal JSON null (inferred from the same concise-mode
	// example; unstated for full mode by any fetched page) and the terminal
	// chunk carries "stop", or p.FinishReason when the turn scripts one.
	FinishReason *string `json:"finish_reason"`
}

// imageWire is one entry of the Sonar images array.
type imageWire struct {
	ImageURL  string `json:"image_url"`
	OriginURL string `json:"origin_url,omitempty"`
	Height    int    `json:"height,omitempty"`
	Width     int    `json:"width,omitempty"`
}

// -----------------------------------------------------------------------------
// Surface 2 — Agent API
// -----------------------------------------------------------------------------

// responsesResponse is the POST /v1/agent response body.
//
// It shares no field with completionResponse. Where Sonar returns one answer in
// choices[], the Agent API returns an ordered execution trace in Output: the
// agent searched, then it answered.
type responsesResponse struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Model     string `json:"model"`
	CreatedAt int64  `json:"created_at"`
	Status    string `json:"status"`

	// Output is emitted as an empty array rather than null when the response
	// carries no trace, which a terminal failure does.
	Output []outputItem `json:"output"`

	// Error is the published errorInfo shape, present on a failed response. A
	// consumer that only branches on the HTTP status misses it, which is exactly
	// why a scenario can produce it inside a 200.
	Error *errorInfo `json:"error,omitempty"`

	Usage responsesUsage `json:"usage"`
}

// Output item type discriminators. The specification declares ten; Servicesim
// renders the two a research adapter parses.
const (
	outputTypeSearchResults = "search_results"
	outputTypeMessage       = "message"
)

// contentPartTypeOutputText is the content-part discriminator for rendered text.
// The generated contract does not enumerate ContentPartType's members; this
// value is inferred from the surface's OpenAI-compatible naming and is recorded
// as such in contracts/perplexity/provenance.yaml.
const contentPartTypeOutputText = "output_text"

// annotationTypeURLCitation is the annotation discriminator for a cited source.
// Like contentPartTypeOutputText it is inferred rather than enumerated.
const annotationTypeURLCitation = "url_citation"

// outputItem is one element of the Agent API's ordered output trace.
//
// The interface is closed by an unexported method: the specification's item
// types are a fixed union and a consumer must not be able to add a member that
// Servicesim would then render into a trace no vendor produces. The two
// implementations are [searchResultsOutput] and [messageOutput].
type outputItem interface {
	// OutputType returns the item's type discriminator.
	OutputType() string

	// isOutputItem closes the interface.
	isOutputItem()
}

// searchResultsOutput is the search_results output item: the searches the agent
// reports having run and what they returned.
type searchResultsOutput struct {
	Type    string              `json:"type"`
	Queries []string            `json:"queries,omitempty"`
	Results []agentSearchResult `json:"results"`
}

// OutputType returns outputTypeSearchResults.
func (searchResultsOutput) OutputType() string { return outputTypeSearchResults }

func (searchResultsOutput) isOutputItem() {}

// messageOutput is the message output item: the agent's answer.
type messageOutput struct {
	Type    string        `json:"type"`
	ID      string        `json:"id"`
	Role    string        `json:"role"`
	Status  string        `json:"status"`
	Content []contentPart `json:"content"`
}

// OutputType returns outputTypeMessage.
func (messageOutput) OutputType() string { return outputTypeMessage }

func (messageOutput) isOutputItem() {}

// contentPart is one part of a message's content.
type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`

	// Annotations is emitted as an empty array rather than omitted, so a
	// consumer's citation-span handling is exercised on every response.
	Annotations []annotation `json:"annotations"`
}

// annotation is a url_citation span over the answer text.
type annotation struct {
	Type       string `json:"type"`
	StartIndex int    `json:"start_index"`
	EndIndex   int    `json:"end_index"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}

// agentSearchResult is one entry of the Agent API's results array.
//
// ID is an integer. It is the one identifier in this repository that is not a
// string, not a source ID and not a URL, and encoding it as a string is the
// single most likely implementation error on this surface — which is why
// render_test.go asserts on the raw JSON bytes rather than on a round-tripped
// struct. A Go int field and a string field both round-trip cleanly through a
// permissive decoder and the bug survives.
type agentSearchResult struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	Date        string `json:"date,omitempty"`
	LastUpdated string `json:"last_updated,omitempty"`
	Source      string `json:"source,omitempty"`
}

// responsesUsage is the Agent API's token and cost accounting.
//
// The token field names differ from Sonar's UsageInfo: input_tokens and
// output_tokens here, prompt_tokens and completion_tokens there.
type responsesUsage struct {
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	TotalTokens  int           `json:"total_tokens"`
	Cost         responsesCost `json:"cost"`
}

// responsesCost is the Agent API's cost breakdown. Currency, InputCost,
// OutputCost and TotalCost are required by the specification; the cache and tool
// costs are optional and are omitted when zero rather than emitted as 0.
type responsesCost struct {
	Currency          string  `json:"currency"`
	InputCost         float64 `json:"input_cost"`
	OutputCost        float64 `json:"output_cost"`
	CacheCreationCost float64 `json:"cache_creation_cost,omitempty"`
	CacheReadCost     float64 `json:"cache_read_cost,omitempty"`
	ToolCallsCost     float64 `json:"tool_calls_cost,omitempty"`
	TotalCost         float64 `json:"total_cost"`
}

// currencyUSD is the default ResponsesCost.currency.
const currencyUSD = "USD"

// errorInfo is the Agent API's published error shape. Only Message is required;
// the code and type values Servicesim sends are its own, because the
// specification declares the fields without enumerating their members.
//
// Field order matches the goldens: code, message, type.
type errorInfo struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
}

// agentErrorResponse is the Agent API's non-422 error envelope.
type agentErrorResponse struct {
	Error errorInfo `json:"error"`
}

// -----------------------------------------------------------------------------
// Surface 2 — Agent API — GrammarTyped SSE events
// -----------------------------------------------------------------------------

// The GrammarTyped event names this build emits: six of the fourteen members
// the specification's ResponseStreamEvent/EventType union declares
// (contracts/perplexity/README.md "Responses / Agent"). The other eight — the
// reasoning.* family and response.failed — have no scenario vocabulary yet
// and are never emitted; see renderAgentStream's doc comment.
const (
	eventResponseCreated   = "response.created"
	eventOutputItemAdded   = "response.output_item.added"
	eventOutputTextDelta   = "response.output_text.delta"
	eventOutputTextDone    = "response.output_text.done"
	eventOutputItemDone    = "response.output_item.done"
	eventResponseCompleted = "response.completed"
)

// responseCreatedEvent is the response.created frame's payload — the
// specification's own name for this schema. Response carries the
// responsesResponse in its initial in_progress state: empty output, zero
// usage, nothing has streamed yet. It is json.RawMessage rather than
// responsesResponse because renderAgentStream builds it directly rather than
// through wire.Render's extra-fields path, unlike responseCompletedEvent's.
type responseCreatedEvent struct {
	Type           string          `json:"type"`
	SequenceNumber int64           `json:"sequence_number"`
	Response       json.RawMessage `json:"response"`
}

// outputItemAddedEvent is the response.output_item.added frame's payload.
// Item is the outputItem interface, matching the specification's
// discriminated union field, even though this build only ever populates it
// with a messageOutput: the reasoning.*/search_results item types have no
// streaming vocabulary yet (see renderAgentStream).
type outputItemAddedEvent struct {
	Type           string     `json:"type"`
	SequenceNumber int64      `json:"sequence_number"`
	Item           outputItem `json:"item"`
	OutputIndex    int        `json:"output_index"`
}

// outputItemDoneEvent is the response.output_item.done frame's payload.
type outputItemDoneEvent struct {
	Type           string     `json:"type"`
	SequenceNumber int64      `json:"sequence_number"`
	Item           outputItem `json:"item"`
	OutputIndex    int        `json:"output_index"`
}

// textDeltaEvent is the response.output_text.delta frame's payload.
type textDeltaEvent struct {
	Type           string `json:"type"`
	SequenceNumber int64  `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Delta          string `json:"delta"`
}

// textDoneEvent is the response.output_text.done frame's payload.
type textDoneEvent struct {
	Type           string `json:"type"`
	SequenceNumber int64  `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Text           string `json:"text"`
}

// responseCompletedEvent is the response.completed frame's payload: the FULL
// responsesResponse the non-streaming Agent route would have rendered for
// this turn — output[], usage and cost included — built once by
// agentResponse and shared by both transports (docs/design/streaming.md §7's
// "one mechanism serves both" rule). Response is json.RawMessage, not
// responsesResponse, so renderAgentStream can drop the usage key with
// wire.Omit when the script sets terminal.omit_usage: responsesResponse.Usage
// is a plain (non-pointer) field, always present on the non-streaming body,
// and changing its type to accommodate one streaming-only edge case would
// ripple into every non-streaming Agent response.
type responseCompletedEvent struct {
	Type           string          `json:"type"`
	SequenceNumber int64           `json:"sequence_number"`
	Response       json.RawMessage `json:"response"`
}

// -----------------------------------------------------------------------------
// Error bodies shared by both surfaces
// -----------------------------------------------------------------------------

// validationErrorResponse is the FastAPI HTTPValidationError returned for 422 on
// both surfaces. It is the only error body Perplexity formally schematises, and
// the asymmetry with errorInfo is real: Sonar is a FastAPI surface whose
// validation errors follow FastAPI's convention, while the Agent API declares
// its own envelope for everything else. Do not unify them.
type validationErrorResponse struct {
	Detail []validationErrorEntry `json:"detail"`
}

// validationErrorEntry is one FastAPI validation failure. Loc elements are strings or
// integers in the same array — "messages", then 0, then "role" — which is why it
// is []any and not []string.
type validationErrorEntry struct {
	Loc  []any  `json:"loc"`
	Msg  string `json:"msg"`
	Type string `json:"type"`
}

// messageErrorResponse is the simulator-chosen body for every non-422 Sonar
// error and for both fail-closed routing statuses, modelled on FastAPI's default
// {"detail": "<string>"}. The specification declares no body for those statuses,
// so this is an inference recorded as unverified in
// contracts/perplexity/provenance.yaml, and correcting it from a real response
// captured by a person is a job for a dated re-verification
// (contracts/README.md "Keeping them honest") — there is no live contract
// canary.
//
// Note that detail is an array for 422 and a string here. Both are legal
// FastAPI, and a consumer must not assume one type.
type messageErrorResponse struct {
	Detail string `json:"detail"`
}
