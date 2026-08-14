package perplexity

import (
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/c360studio/servicesim/internal/ids"
	"github.com/c360studio/servicesim/internal/wire"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Agent request finding codes.
const (
	CodeInputMissing      = "perplexity.input.missing"
	CodeInputInvalid      = "perplexity.input.invalid"
	CodeModelsTooMany     = "perplexity.agent.models.max"
	CodeModelFormat       = "perplexity.agent.model.format"
	CodeMaxSteps          = "perplexity.agent.max_steps.range"
	CodeMaxOutputTokens   = "perplexity.agent.max_output_tokens.range"
	CodeStoreInvalid      = "perplexity.agent.store.invalid"
	CodeBackgroundInvalid = "perplexity.agent.background.invalid"

	// CodeAgentStreamUnsupported is raised, as a warning, for stream: true.
	// Streaming is deferred; the request still receives the ordinary
	// non-streaming body. A deferred feature must fail loudly, because silence
	// would let a consumer believe it had exercised a path it never touched.
	CodeAgentStreamUnsupported = "perplexity.agent.stream.unsupported"

	// CodeAgentBackgroundUnsupported is raised, as a warning, for
	// background: true. The queued/poll lifecycle is deferred; the request
	// receives the ordinary synchronous body rather than a queued stub.
	CodeAgentBackgroundUnsupported = "perplexity.agent.background.unsupported"
)

// MaxModelChain is the longest model fallback chain the Agent API accepts.
const MaxModelChain = 5

// agentFields are the Agent request properties this build models, in the order
// the specification declares them. As with sonarFields the order is what the 422
// detail array is sorted into.
var agentFields = []string{
	"input", "background", "instructions", "language_preference",
	"max_output_tokens", "max_steps", "model", "models", "preset",
	"previous_response_id", "reasoning", "response_format", "store", "stream",
	"tools", "skills", "temperature", "top_p",
}

// AgentStatus is ResponsesResponse.status. The zero value renders as
// StatusCompleted, so a minimal scenario projects a successful response.
type AgentStatus string

// The Status enum members.
const (
	StatusCompleted  AgentStatus = "completed"
	StatusFailed     AgentStatus = "failed"
	StatusIncomplete AgentStatus = "incomplete"
	StatusInProgress AgentStatus = "in_progress"
	StatusQueued     AgentStatus = "queued"
	StatusCancelled  AgentStatus = "cancelled"
)

// AgentStatuses is the Status enum, for validation and for tests that want to
// walk every member.
var AgentStatuses = []AgentStatus{
	StatusCompleted, StatusFailed, StatusIncomplete,
	StatusInProgress, StatusQueued, StatusCancelled,
}

// PerplexityAgent projects the shared corpus into an Agent API response.
//
// The Agent envelope shares no fields with the Sonar envelope: Sonar returns
// choices[] with a message, the Agent API returns an ordered output[] trace. The
// two are rendered by separate functions from the same canonical sources, which
// is the point of the scenario model.
type PerplexityAgent struct { //revive:disable-line:exported // the scenario schema names this type; renaming it would break every fixture that documents it
	// ResponseID overrides the derived "resp_<32 hex>" identifier.
	ResponseID string `yaml:"response_id,omitempty"`

	// MessageID overrides the derived "msg_<32 hex>" identifier of the message
	// output item.
	MessageID string `yaml:"message_id,omitempty"`

	// Model is echoed as ResponsesResponse.model. Agent model IDs are
	// "provider/model" strings; when empty the request's model is echoed, and
	// when the request omits it too the scenario default applies.
	Model string `yaml:"model,omitempty"`

	// CreatedAt overrides the derived Unix timestamp. When zero it is
	// Scenario.BaseTime().Unix(), never time.Now().
	CreatedAt int64 `yaml:"created_at,omitempty"`

	// Status defaults to StatusCompleted. Setting it to "failed" renders Error
	// and is how a consumer's terminal-state handling is exercised without an
	// HTTP-level fault — a consumer that only branches on the status code misses
	// it entirely.
	Status AgentStatus `yaml:"status,omitempty"`

	// Answer becomes the text of the single message output item.
	Answer string `yaml:"answer,omitempty"`

	// Queries populates the search_results item's queries — the searches the
	// agent reports having run. It is independent of SearchResults so that a
	// scenario can project "searched but found nothing".
	Queries []string `yaml:"queries,omitempty"`

	// SearchResults become the search_results output item. Ordering is the
	// scenario's; results[].id is the 1-based index within the item, rendered as
	// a JSON integer.
	SearchResults []AgentResult `yaml:"search_results,omitempty"`

	// Annotations attach url_citation spans to the answer text.
	Annotations []AgentAnnotation `yaml:"annotations,omitempty"`

	// Error renders ResponsesResponse.error. It is required when Status is
	// StatusFailed; validation rejects a failed status with no error.
	Error *AgentError `yaml:"error,omitempty"`

	Usage       *AgentUsage          `yaml:"usage,omitempty"`
	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// AgentResult is one entry of the Agent API's search_results item.
//
// The addendum types this field []scenario.SourceRef. This is a superset of that
// shape — the scalar shorthand and the bare `- source: x` mapping both still
// decode — and it exists because the Agent search result carries a snippet, a
// date and a last_updated that the canonical corpus cannot always supply in the
// form a fixture needs: Source.PublishedAt renders RFC 3339 with millisecond
// precision, and this surface's goldens carry plain calendar dates.
type AgentResult struct {
	scenario.SourceRef `yaml:",inline"`

	Snippet     string `yaml:"snippet,omitempty"`
	Date        string `yaml:"date,omitempty"`
	LastUpdated string `yaml:"last_updated,omitempty"`

	// SourceType is the web/attachment discriminator. It is named and keyed
	// exactly as PerplexityResult.SourceType is, and for the same reason: the
	// inlined SourceRef already owns the YAML key "source".
	SourceType string `yaml:"source_type,omitempty"`
}

// rawAgentResult is the decode target for the mapping form. It restates the
// fields for the reason rawPerplexityResult does: a defined-type copy of a
// struct embedding scenario.SourceRef still promotes SourceRef.UnmarshalYAML,
// and yaml.v3 would hand that method the whole result mapping.
type rawAgentResult struct {
	Source      string `yaml:"source"`
	Snippet     string `yaml:"snippet,omitempty"`
	Date        string `yaml:"date,omitempty"`
	LastUpdated string `yaml:"last_updated,omitempty"`
	SourceType  string `yaml:"source_type,omitempty"`
}

// UnmarshalYAML accepts the scalar shorthand for a source reference.
func (r *AgentResult) UnmarshalYAML(value *yaml.Node) error {
	var raw rawAgentResult
	if err := scenario.DecodeRefOrMapping(value, &r.SourceRef, &raw); err != nil {
		return err
	}
	if value == nil || value.Kind != yaml.MappingNode {
		return nil
	}
	r.SourceRef.Ref = raw.Source
	r.Snippet = raw.Snippet
	r.Date = raw.Date
	r.LastUpdated = raw.LastUpdated
	r.SourceType = raw.SourceType
	return nil
}

// AgentAnnotation is a url_citation span over the answer text.
//
// StartIndex and EndIndex are byte offsets into Answer. Validation rejects
// offsets outside the answer or with End <= Start, because an out-of-range span
// is a fixture bug that would otherwise surface as a consumer panic.
type AgentAnnotation struct {
	Source     scenario.SourceRef `yaml:"source"`
	StartIndex int                `yaml:"start_index"`
	EndIndex   int                `yaml:"end_index"`
}

// AgentError is ResponsesResponse.error, the published ErrorInfo shape.
type AgentError struct {
	Message string `yaml:"message"`
	Code    string `yaml:"code,omitempty"`
	Type    string `yaml:"type,omitempty"`
}

// AgentUsage is ResponsesUsage. The field names differ from Sonar's usage:
// input_tokens and output_tokens here, prompt_tokens and completion_tokens
// there. Do not share a Go type between them.
type AgentUsage struct {
	InputTokens  int        `yaml:"input_tokens,omitempty"`
	OutputTokens int        `yaml:"output_tokens,omitempty"`
	TotalTokens  int        `yaml:"total_tokens,omitempty"` // derived when zero
	Cost         *AgentCost `yaml:"cost,omitempty"`
}

// AgentCost is ResponsesCost. Currency, InputCost, OutputCost and TotalCost are
// required by the specification; the cache and tool fields are optional and are
// omitted when zero rather than emitted as 0.
type AgentCost struct {
	Currency          string  `yaml:"currency,omitempty"` // defaults to USD
	InputCost         float64 `yaml:"input_cost,omitempty"`
	OutputCost        float64 `yaml:"output_cost,omitempty"`
	TotalCost         float64 `yaml:"total_cost,omitempty"` // derived when zero
	CacheCreationCost float64 `yaml:"cache_creation_cost,omitempty"`
	CacheReadCost     float64 `yaml:"cache_read_cost,omitempty"`
	ToolCallsCost     float64 `yaml:"tool_calls_cost,omitempty"`
}

// validateAgentRequest applies the Agent API's request checks.
//
// stream and background are the two deferred features a consumer is most likely
// to reach for. Both produce a named warning and an ordinary non-streaming,
// synchronous response: loud enough to assert on, and never a silent success
// that looks like the real path.
func validateAgentRequest(x *provider.Exchange) string {
	if bodyUnusable(x) {
		return ""
	}
	checkUnknownFields(x, agentFields)

	if !x.Has("input") {
		x.Fail(CodeInputMissing, "body.input", "input is required")
	} else {
		switch x.Body["input"].(type) {
		case string, []any:
		default:
			x.Fail(CodeInputInvalid, "body.input", "input must be a string or an array of input items")
		}
	}

	model := validateAgentModel(x)
	validateNumericRange(x, "temperature", CodeTemperature, 0, 2)
	validateNumericRange(x, "top_p", CodeTopP, 0, 1)

	if x.Has("max_steps") {
		if v, ok := x.Number("max_steps"); !ok || v < 1 {
			x.Fail(CodeMaxSteps, "body.max_steps", "max_steps must be an integer of at least 1")
		}
	}
	if x.Has("max_output_tokens") {
		if v, ok := x.Number("max_output_tokens"); !ok || v <= 0 {
			x.Fail(CodeMaxOutputTokens, "body.max_output_tokens",
				"max_output_tokens must be an integer greater than 0")
		}
	}
	if x.Has("store") {
		if _, ok := x.Bool("store"); !ok {
			x.Fail(CodeStoreInvalid, "body.store", "store must be a boolean")
		}
	}

	if x.Has("background") {
		background, ok := x.Bool("background")
		switch {
		case !ok:
			x.Fail(CodeBackgroundInvalid, "body.background", "background must be a boolean")
		case background:
			x.Warn(CodeAgentBackgroundUnsupported, "body.background",
				"background execution is not simulated; this request receives the ordinary synchronous body")
		}
	}
	if stream, ok := x.Bool("stream"); ok && stream {
		x.Warn(CodeAgentStreamUnsupported, "body.stream",
			"streaming is not simulated; this request receives the ordinary non-streaming body")
	}
	return model
}

// validateAgentModel checks model and models, and returns the model to echo.
//
// A model that is not in provider/model form is a warning rather than an error:
// the Agent API is a multi-provider router whose model set is not enumerated
// anywhere Servicesim can verify, and rejecting an unknown-but-well-formed model
// would mean rejecting valid traffic the moment a router adds a provider.
func validateAgentModel(x *provider.Exchange) string {
	model, _ := x.String("model")
	if model != "" && !strings.Contains(model, "/") {
		x.Warn(CodeModelFormat, "body.model",
			"model %q is not in provider/model form, for example openai/gpt-5", model)
	}
	if !x.Has("models") {
		return model
	}
	models, ok := x.Body["models"].([]any)
	if !ok {
		return model
	}
	if len(models) > MaxModelChain {
		x.Fail(CodeModelsTooMany, "body.models",
			"models accepts at most %d entries, got %d", MaxModelChain, len(models))
	}
	if model == "" && len(models) > 0 {
		first, _ := models[0].(string)
		return first
	}
	return model
}

// renderAgent projects p into the Agent API wire envelope.
//
// The order of output[] is fixed and deterministic: search_results first, then
// message. That mirrors the execution order the trace represents — the agent
// searches, then it answers — and gives consumers a stable index. A scenario
// cannot reorder it.
func renderAgent(x *provider.Exchange, p *PerplexityAgent, requestModel string) ([]byte, error) {
	s := x.Deps.Scenario
	dec := x.Fault()

	id := p.ResponseID
	if id == "" {
		id = "resp_" + ids.Hex32(idParts(s, dec, "agent")...)
	}
	messageID := p.MessageID
	if messageID == "" {
		messageID = "msg_" + ids.Hex32(idParts(s, dec, "agent", "message")...)
	}
	created := p.CreatedAt
	if created == 0 {
		created = s.BaseTime().Unix()
	}
	status := p.Status
	if status == "" {
		status = StatusCompleted
	}

	resp := ResponsesResponse{
		ID:        id,
		Object:    ObjectResponse,
		Model:     firstNonEmpty(p.Model, requestModel),
		CreatedAt: created,
		Status:    string(status),
		Output:    renderAgentOutput(p, messageID, status),
		Usage:     renderAgentUsage(p.Usage),
	}
	if p.Error != nil {
		resp.Error = &ErrorInfo{Code: p.Error.Code, Message: p.Error.Message, Type: p.Error.Type}
	}
	return wire.Render(resp, p.ExtraFields)
}

// renderAgentOutput builds the ordered trace.
//
// The search_results item appears when the scenario declares queries or results,
// so that "searched and found nothing" is expressible as queries with an empty
// results array. The message item appears unless the response failed: a failed
// response reports its reason in error and carries no answer, which is what the
// failed golden shows.
func renderAgentOutput(p *PerplexityAgent, messageID string, status AgentStatus) []OutputItem {
	out := make([]OutputItem, 0, 2)

	if len(p.Queries) > 0 || len(p.SearchResults) > 0 {
		results := make([]AgentSearchResult, 0, len(p.SearchResults))
		for i := range p.SearchResults {
			r := &p.SearchResults[i]
			src := scenario.Render(r.SourceRef)
			snippet := r.Snippet
			if snippet == "" {
				snippet = src.FirstSnippet()
			}
			sourceType := r.SourceType
			if sourceType == "" {
				sourceType = SourceTypeWeb
			}
			results = append(results, AgentSearchResult{
				// The specification types this an integer, and it is the 1-based
				// index within the item — not the source ID and not a URL.
				ID:          i + 1,
				Title:       src.Title,
				URL:         src.URL,
				Snippet:     snippet,
				Date:        r.Date,
				LastUpdated: r.LastUpdated,
				Source:      sourceType,
			})
		}
		out = append(out, SearchResultsOutput{
			Type:    OutputTypeSearchResults,
			Queries: p.Queries,
			Results: results,
		})
	}

	if status == StatusFailed || status == StatusCancelled {
		return out
	}

	annotations := make([]Annotation, 0, len(p.Annotations))
	for _, a := range p.Annotations {
		src := scenario.Render(a.Source)
		annotations = append(annotations, Annotation{
			Type:       AnnotationTypeURLCitation,
			StartIndex: a.StartIndex,
			EndIndex:   a.EndIndex,
			Title:      src.Title,
			URL:        src.URL,
		})
	}
	return append(out, MessageOutput{
		Type:   OutputTypeMessage,
		ID:     messageID,
		Role:   RoleAssistant,
		Status: string(status),
		Content: []ContentPart{{
			Type:        ContentPartTypeOutputText,
			Text:        p.Answer,
			Annotations: annotations,
		}},
	})
}

// renderAgentUsage projects the usage object, deriving the totals and the
// currency when the scenario leaves them at their zero values.
func renderAgentUsage(u *AgentUsage) ResponsesUsage {
	out := ResponsesUsage{Cost: ResponsesCost{Currency: CurrencyUSD}}
	if u == nil {
		return out
	}
	out.InputTokens = u.InputTokens
	out.OutputTokens = u.OutputTokens
	out.TotalTokens = u.TotalTokens
	if out.TotalTokens == 0 {
		out.TotalTokens = u.InputTokens + u.OutputTokens
	}
	if c := u.Cost; c != nil {
		out.Cost = ResponsesCost{
			Currency:          c.Currency,
			InputCost:         c.InputCost,
			OutputCost:        c.OutputCost,
			CacheCreationCost: c.CacheCreationCost,
			CacheReadCost:     c.CacheReadCost,
			ToolCallsCost:     c.ToolCallsCost,
			TotalCost:         c.TotalCost,
		}
		if out.Cost.Currency == "" {
			out.Cost.Currency = CurrencyUSD
		}
		if out.Cost.TotalCost == 0 {
			out.Cost.TotalCost = c.InputCost + c.OutputCost
		}
	}
	return out
}

// validateAgentProjection checks one decoded Agent projection at startup.
func validateAgentProjection(path string, p *PerplexityAgent) []scenario.Finding {
	var findings []scenario.Finding
	add := func(code, at, message string) {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError, Code: code, Path: at, Message: message,
		})
	}

	if p.Status != "" && !slices.Contains(AgentStatuses, p.Status) {
		add("perplexity.agent.status.invalid", path+".status",
			"status "+strconv.Quote(string(p.Status))+" is not a member of the Status enum")
	}
	if p.Status == StatusFailed && p.Error == nil {
		add("perplexity.agent.error.missing", path+".error",
			"a failed status must declare an error; the specification requires ErrorInfo.message")
	}
	if p.Error != nil && p.Error.Message == "" {
		add("perplexity.agent.error.message", path+".error.message",
			"error.message is required by the specification")
	}
	for i := range p.SearchResults {
		if st := p.SearchResults[i].SourceType; st != "" && !slices.Contains(SourceTypes, st) {
			add("perplexity.source_type.invalid",
				path+".search_results["+strconv.Itoa(i)+"].source_type",
				"source_type "+strconv.Quote(st)+" is not web or attachment")
		}
	}
	// An annotation span outside the answer is a fixture bug that would otherwise
	// reach a consumer as an index into a string that is too short.
	for i, a := range p.Annotations {
		at := path + ".annotations[" + strconv.Itoa(i) + "]"
		switch {
		case a.StartIndex < 0 || a.EndIndex <= a.StartIndex:
			add("perplexity.agent.annotation.range", at,
				"end_index must be greater than start_index and both must be non-negative")
		case a.EndIndex > len(p.Answer):
			add("perplexity.agent.annotation.range", at,
				"end_index "+strconv.Itoa(a.EndIndex)+" is past the end of the answer, which is "+
					strconv.Itoa(len(p.Answer))+" bytes")
		}
	}
	return findings
}
