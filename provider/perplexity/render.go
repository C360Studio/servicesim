package perplexity

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// PerplexityProjection is how the shared corpus renders through the Sonar
// chat-completions API.
//
// It is decoded from the selected turn's `respond:` body by this package rather
// than by the scenario package: a level-1 package cannot know what a Perplexity
// search result looks like without importing this one and closing an import
// cycle. The envelope keys — auth, validation, fault, turns, turn_key — are
// stripped by the scenario package before the body reaches here, so declaring
// them again would be declaring fields that can never be set.
type PerplexityProjection struct { //revive:disable-line:exported // the scenario schema names this type; renaming it would break every fixture that documents it
	// CompletionID overrides the derived UUID. The documentation pins no prefix
	// and calls it an opaque completion id, so an unprefixed UUID is the safest
	// default.
	CompletionID string `yaml:"completion_id,omitempty"`

	// Created overrides the derived Unix timestamp. When zero it is
	// Scenario.BaseTime().Unix(), never time.Now(): a golden containing
	// "created": 1767225600 must stay valid forever.
	Created int64 `yaml:"created,omitempty"`

	// Model overrides the echoed model. When empty the request's model is
	// echoed, and when the request named none either the first member of Models
	// is used.
	Model string `yaml:"model,omitempty"`

	Answer string `yaml:"answer,omitempty"`

	// FinishReason is stop or length. Empty means stop.
	FinishReason string `yaml:"finish_reason,omitempty"`

	// Stream selects the behaviour for a request carrying "stream": true, and —
	// when the policy is StreamServe — scripts the deltas the SSE sequence
	// serves. Defaults to StreamWarn: a journal warning plus the ordinary
	// non-streaming JSON body. StreamReject turns that into a 422 naming
	// body.stream. StreamServe serves the scripted GrammarDelta full-mode
	// sequence (renderSonarStream) instead of the JSON body, though the JSON
	// body is still rendered — see Response.Stream's doc comment for why.
	//
	// The POLICY is a property of the provider block rather than of a turn —
	// see streamPolicy — because rejection has to happen before turn selection
	// claims a fault attempt. Only the first turn's policy is read. The
	// DELTAS, in contrast, are genuinely per turn: they are the answer, and
	// each turn has its own.
	//
	// The field exists because a consumer whose primary path always streams
	// otherwise records fixtures against a complete non-streaming 200 that the
	// real API would never have sent, and their suite passes green against
	// behaviour that is about to change under them. Being able to make that fail
	// loudly is the point.
	Stream scenario.StreamScript `yaml:"stream,omitempty"`

	Citations        []scenario.SourceRef `yaml:"citations,omitempty"`
	SearchResults    []PerplexityResult   `yaml:"search_results,omitempty"`
	Usage            *PerplexityUsage     `yaml:"usage,omitempty"`
	Images           []PerplexityImage    `yaml:"images,omitempty"`
	RelatedQuestions []string             `yaml:"related_questions,omitempty"`
	ExtraFields      scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// PerplexityResult is one entry of the Sonar search_results array.
type PerplexityResult struct { //revive:disable-line:exported // the scenario schema names this type; renaming it would break every fixture that documents it
	scenario.SourceRef `yaml:",inline"`

	Snippet     string            `yaml:"snippet,omitempty"`
	Date        scenario.Nullable `yaml:"date,omitempty"`
	LastUpdated scenario.Nullable `yaml:"last_updated,omitempty"`

	// SourceType is the web/attachment discriminator. It is deliberately not
	// named Source and its key is deliberately not "source": the inlined
	// SourceRef already owns the YAML key "source", and a second field claiming
	// it makes yaml.v3 panic with "duplicated key 'source'" on every scenario
	// that declares a search result. The wire field is still named "source".
	SourceType string `yaml:"source_type,omitempty"`

	// OmitFields names wire properties this result must not send, for a test
	// that needs to prove its adapter tolerates a missing optional field.
	OmitFields []string `yaml:"omit_fields,omitempty"`
}

// rawPerplexityResult is the decode target for the mapping form of a search
// result.
//
// It restates the fields rather than being `type rawPerplexityResult
// PerplexityResult`, and that is not a stylistic choice. A defined-type copy
// drops the methods of the type it copies but keeps its embedded fields, so a
// copy of a struct embedding scenario.SourceRef still *promotes*
// SourceRef.UnmarshalYAML — and yaml.v3 would then hand that method the whole
// result mapping, which rejects every key except "source". The failure mode is a
// decode error naming scenario.rawSourceRef from a file that never mentions it.
//
// TestProjectionYAMLRoundTrip is the guard against this list drifting from
// PerplexityResult: the strict decode rejects any field that was added there and
// not here.
type rawPerplexityResult struct {
	Source      string            `yaml:"source"`
	Snippet     string            `yaml:"snippet,omitempty"`
	Date        scenario.Nullable `yaml:"date,omitempty"`
	LastUpdated scenario.Nullable `yaml:"last_updated,omitempty"`
	SourceType  string            `yaml:"source_type,omitempty"`
	OmitFields  []string          `yaml:"omit_fields,omitempty"`
}

// UnmarshalYAML accepts the scalar shorthand, so that
//
//	search_results:
//	  - source-a
//
// and the mapping form both decode. The plan document's own example uses the
// shorthand.
func (r *PerplexityResult) UnmarshalYAML(value *yaml.Node) error {
	var raw rawPerplexityResult
	if err := scenario.DecodeRefOrMapping(value, &r.SourceRef, &raw); err != nil {
		return err
	}
	if value == nil || value.Kind != yaml.MappingNode {
		// The scalar branch already filled in the reference.
		return nil
	}
	r.SourceRef.Ref = raw.Source
	r.Snippet = raw.Snippet
	r.Date = raw.Date
	r.LastUpdated = raw.LastUpdated
	r.SourceType = raw.SourceType
	r.OmitFields = raw.OmitFields
	return nil
}

// PerplexityUsage projects the Sonar usage object. Cost is required by the live
// schema and absent from the plan document's example, so a nil Cost renders a
// derived zero-cost object rather than being omitted.
type PerplexityUsage struct { //revive:disable-line:exported // the scenario schema names this type; renaming it would break every fixture that documents it
	PromptTokens      int             `yaml:"prompt_tokens,omitempty"`
	CompletionTokens  int             `yaml:"completion_tokens,omitempty"`
	TotalTokens       int             `yaml:"total_tokens,omitempty"`
	SearchContextSize string          `yaml:"search_context_size,omitempty"`
	CitationTokens    *int            `yaml:"citation_tokens,omitempty"`
	NumSearchQueries  *int            `yaml:"num_search_queries,omitempty"`
	ReasoningTokens   *int            `yaml:"reasoning_tokens,omitempty"`
	Cost              *PerplexityCost `yaml:"cost,omitempty"`
}

// PerplexityCost projects the required usage.cost object.
type PerplexityCost struct { //revive:disable-line:exported // the scenario schema names this type; renaming it would break every fixture that documents it
	InputTokensCost     float64  `yaml:"input_tokens_cost,omitempty"`
	OutputTokensCost    float64  `yaml:"output_tokens_cost,omitempty"`
	TotalCost           float64  `yaml:"total_cost,omitempty"`
	ReasoningTokensCost *float64 `yaml:"reasoning_tokens_cost,omitempty"`
	RequestCost         *float64 `yaml:"request_cost,omitempty"`
	CitationTokensCost  *float64 `yaml:"citation_tokens_cost,omitempty"`
	SearchQueriesCost   *float64 `yaml:"search_queries_cost,omitempty"`
}

// PerplexityImage is one entry of the Sonar images array.
type PerplexityImage struct { //revive:disable-line:exported // the scenario schema names this type; renaming it would break every fixture that documents it
	ImageURL  string `yaml:"image_url"`
	OriginURL string `yaml:"origin_url,omitempty"`
	Height    int    `yaml:"height,omitempty"`
	Width     int    `yaml:"width,omitempty"`
}

// SourceTypes is the search_results source discriminator enum.
var SourceTypes = []string{"web", "attachment"}

// SourceTypeWeb is the default search-result source discriminator.
const SourceTypeWeb = "web"

// FinishReasons is the finish_reason enum.
var FinishReasons = []string{"stop", "length"}

// idParts is the tuple every derived identifier hangs off, per §3.1 of the
// package design: the scenario seed, the provider, the route's fault key, and
// the zero-based index of this call within its lane.
//
// The call index is folded in unconditionally, not only where the route declares
// a fault plan. A real vendor issues a distinct id per call; one value repeated
// for every call means a consumer correlating a log line to a request finds the
// whole run pointing at a single id, which is the failure this tuple exists to
// prevent rather than to cause.
//
// Determinism is sharpened rather than weakened. The property house rule 2 states
// is that the same scenario and the same request render byte-identical bytes; its
// precise form is "at the same call position", which is what callIndex measures.
// Two consecutive calls differ; call 0 of a fresh lane — a new namespace, a new
// process, a reset — reproduces call 0 exactly. Nothing here reads a clock or a
// random source, and callIndex comes from the single counter turn selection and
// fault selection share rather than from a second one of this package's own.
//
// The key is Route.FaultKey rather than the lane's cursor key on purpose, and it
// is what keeps the fresh-lane half of that property true. A namespace is a state
// boundary, not a behaviour boundary: two tests sharing one container must get
// independent cursors, not different response bytes at the same call position.
func idParts(x *provider.Exchange, callIndex int, extra ...string) []string {
	parts := []string{
		x.Deps.Scenario.SeedKey(),
		string(provider.Perplexity),
		x.Route.FaultKey,
		strconv.Itoa(callIndex),
	}
	return append(parts, extra...)
}

// renderSonarIdentity computes the id, model and finish_reason shared by the
// JSON body and the SSE stream for one turn, so the two transports render
// identical values everywhere docs/design/streaming.md §6.1 says they must
// agree. created is deliberately NOT here: the JSON body's created follows
// p.Created-or-BaseTime (below), while the stream pins it CONSTANT at
// BaseTime regardless of p.Created (renderSonarStream) — the one field this
// design deliberately makes the two transports disagree on.
//
// The attempt index is claimed whether or not the scenario pins the
// completion id, so the journal's attempt number never depends on whether a
// fixture happened to declare one.
func renderSonarIdentity(x *provider.Exchange, p *PerplexityProjection, requestModel string, callIndex int) (id, model, finish string) {
	id = p.CompletionID
	if id == "" {
		id = provider.UUIDv5(idParts(x, callIndex)...)
	}
	model = firstNonEmpty(p.Model, requestModel, Models[0])
	finish = p.FinishReason
	if finish == "" {
		finish = FinishReasons[0]
	}
	return id, model, finish
}

// renderSonar projects p into the Sonar wire envelope and returns the response
// bytes, extras already merged.
func renderSonar(x *provider.Exchange, p *PerplexityProjection, requestModel string) ([]byte, error) {
	s := x.Deps.Scenario

	callIndex := x.CallIndex()
	id, model, finish := renderSonarIdentity(x, p, requestModel, callIndex)
	created := p.Created
	if created == 0 {
		created = s.BaseTime().Unix()
	}

	resp := CompletionResponse{
		ID:      id,
		Object:  ObjectChatCompletion,
		Model:   model,
		Created: created,
		Choices: []Choice{{
			Index:        0,
			FinishReason: finish,
			// Delta is required alongside Message even when not streaming, so an
			// empty one is emitted rather than the key being dropped.
			Message: Message{Role: RoleAssistant, Content: p.Answer},
			Delta:   Message{Role: RoleAssistant, Content: ""},
		}},
		Usage:            renderSonarUsage(p.Usage),
		SearchResults:    renderSonarResults(p.SearchResults),
		Citations:        renderCitations(p.Citations),
		Images:           renderImages(p.Images),
		RelatedQuestions: p.RelatedQuestions,
	}
	return provider.Render(resp, p.ExtraFields, nil)
}

// renderSonarResults projects the scenario's search results in declaration
// order. No sorting, ever: sorting would make output depend on float comparison
// and would silently reorder equal scores.
func renderSonarResults(results []PerplexityResult) []SearchResult {
	if len(results) == 0 {
		return nil
	}
	out := make([]SearchResult, 0, len(results))
	for i := range results {
		r := &results[i]
		src := scenario.Render(r.SourceRef)

		snippet := r.Snippet
		if snippet == "" {
			snippet = src.FirstSnippet()
		}
		sourceType := r.SourceType
		if sourceType == "" {
			sourceType = SourceTypeWeb
		}
		out = append(out, SearchResult{
			Title:       src.Title,
			URL:         src.URL,
			Snippet:     snippet,
			Date:        nullableOr(r.Date, src.PublishedAt),
			LastUpdated: nullableOr(r.LastUpdated, scenario.Nullable{}),
			Source:      sourceType,
			omit:        r.OmitFields,
		})
	}
	return out
}

// nullableOr returns the projection's value, falling back to a derived one and
// then to an explicit null.
//
// The null fallback is deliberate. A date key that is present on one result and
// absent on the next teaches a consumer nothing about its null branch, and a
// consumer that has never seen null is the one that panics in production.
func nullableOr(pinned, derived scenario.Nullable) scenario.Nullable {
	if !pinned.IsZero() {
		return pinned
	}
	if !derived.IsZero() {
		return derived
	}
	return scenario.NullNullable()
}

// renderCitations projects the deprecated bare-URL citations array.
func renderCitations(refs []scenario.SourceRef) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, scenario.Render(ref).URL)
	}
	return out
}

func renderImages(images []PerplexityImage) []Image {
	if len(images) == 0 {
		return nil
	}
	out := make([]Image, 0, len(images))
	for _, img := range images {
		out = append(out, Image{
			ImageURL:  img.ImageURL,
			OriginURL: img.OriginURL,
			Height:    img.Height,
			Width:     img.Width,
		})
	}
	return out
}

// renderSonarUsage projects the usage object, filling in the required cost even
// when the scenario declares none. TotalTokens and TotalCost are derived when
// left at zero so a scenario only has to state the two numbers it cares about.
func renderSonarUsage(u *PerplexityUsage) Usage {
	if u == nil {
		return Usage{}
	}
	out := Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CitationTokens:   u.CitationTokens,
		NumSearchQueries: u.NumSearchQueries,
		ReasoningTokens:  u.ReasoningTokens,
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	if u.SearchContextSize != "" {
		size := u.SearchContextSize
		out.SearchContextSize = &size
	}
	if c := u.Cost; c != nil {
		out.Cost = Cost{
			InputTokensCost:     c.InputTokensCost,
			OutputTokensCost:    c.OutputTokensCost,
			ReasoningTokensCost: c.ReasoningTokensCost,
			RequestCost:         c.RequestCost,
			CitationTokensCost:  c.CitationTokensCost,
			SearchQueriesCost:   c.SearchQueriesCost,
			TotalCost:           c.TotalCost,
		}
		if out.Cost.TotalCost == 0 {
			out.Cost.TotalCost = c.InputTokensCost + c.OutputTokensCost +
				deref(c.ReasoningTokensCost) + deref(c.RequestCost) +
				deref(c.CitationTokensCost) + deref(c.SearchQueriesCost)
		}
	}
	return out
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// renderSonarStream projects p into the GrammarDelta full-mode SSE sequence
// (docs/design/streaming.md §7, "Resolved 2026-08-15" A3): one chunk per
// scripted delta carrying that delta's role+content and the running
// aggregate message, then one terminal chunk carrying finish_reason, the
// full aggregated message, usage+cost (unless the script omits usage) and
// search_results together — a frame-level choice this build makes because
// the vendor pins that placement only for stream_mode: concise
// (contracts/perplexity/README.md "Streaming (SSE)") — plus images,
// related_questions and extra_fields, mirroring where usage lands rather
// than being scripted onto every chunk.
//
// created is held CONSTANT at Scenario.BaseTime().Unix() across every chunk,
// regardless of p.Created: a byte-stable golden needs a constant, and the
// design deliberately departs here from what the vendor's own concise-mode
// example shows (created moving while id stays fixed) — see §6.1. id, model
// and finish_reason otherwise follow the exact rules the non-streaming body
// uses (renderSonarIdentity), so a scenario that pins them renders a stream
// and a JSON body that agree on every field except created.
func renderSonarStream(x *provider.Exchange, p *PerplexityProjection, requestModel string) (*provider.Stream, error) {
	s := x.Deps.Scenario
	callIndex := x.CallIndex()
	id, model, finish := renderSonarIdentity(x, p, requestModel, callIndex)
	created := s.BaseTime().Unix()

	deltas := p.Stream.Deltas
	searchResults := renderSonarResults(p.SearchResults)

	// pace resolves one chunk's gap: its own override when it set one
	// (nonzero — see scenario.StreamDelta.Pace's doc comment for why zero
	// means "no override" rather than "an explicit zero gap"), falling back
	// to the script's own default otherwise. It covers every chunk this
	// stream writes: each delta, the terminal chunk, and (via
	// Stream.SentinelPace below) the [DONE] sentinel, per
	// docs/design/streaming.md §4.3.
	pace := func(override scenario.Duration) time.Duration {
		if override != 0 {
			return override.Duration()
		}
		return p.Stream.Pace.Duration()
	}

	events := make([]provider.SSEEvent, 0, len(deltas)+1)
	var aggregate strings.Builder
	for _, d := range deltas {
		aggregate.WriteString(d.Text)
		chunk := ChatCompletionChunkResponse{
			ID: id, Object: ObjectChatCompletionChunk, Model: model, Created: created,
			Choices: []ChatCompletionChunkChoice{{
				Index:        0,
				Delta:        Message{Role: RoleAssistant, Content: d.Text},
				Message:      Message{Role: RoleAssistant, Content: aggregate.String()},
				FinishReason: nil,
			}},
			SearchResults: searchResults,
		}
		data, err := provider.Render(chunk, nil, nil)
		if err != nil {
			return nil, err
		}
		events = append(events, provider.SSEEvent{Data: data, Pace: pace(d.Pace)})
	}

	terminal := p.Stream.Terminal
	omitUsage := terminal != nil && terminal.OmitUsage
	usage := renderSonarUsage(p.Usage)
	var usagePtr *Usage
	if !omitUsage {
		usagePtr = &usage
	}
	finishValue := finish
	termChunk := ChatCompletionChunkResponse{
		ID: id, Object: ObjectChatCompletionChunk, Model: model, Created: created,
		Choices: []ChatCompletionChunkChoice{{
			Index:        0,
			Delta:        Message{Role: RoleAssistant, Content: ""},
			Message:      Message{Role: RoleAssistant, Content: aggregate.String()},
			FinishReason: &finishValue,
		}},
		Usage:            usagePtr,
		SearchResults:    searchResults,
		Images:           renderImages(p.Images),
		RelatedQuestions: p.RelatedQuestions,
	}
	// When p.ExtraFields is non-empty, the terminal frame's keys come out
	// alphabetised rather than in struct order — wire.MergeJSON's own doc
	// comment explains why (a map round-trip) — so an extra-fields turn
	// renders its N delta frames in struct order and its one terminal frame
	// alphabetised. Deterministic either way, and the same divergence the
	// non-streaming body already has; noted here only so a captured
	// transcript's mixed ordering does not read as a bug later.
	termData, err := provider.Render(termChunk, p.ExtraFields, nil)
	if err != nil {
		return nil, err
	}
	var terminalPaceOverride scenario.Duration
	if terminal != nil {
		terminalPaceOverride = terminal.Pace
	}
	events = append(events, provider.SSEEvent{Data: termData, Terminal: true, Pace: pace(terminalPaceOverride)})

	stream := &provider.Stream{
		Grammar:      provider.GrammarDelta,
		Chunks:       provider.EncodeSSE(events),
		Sentinel:     provider.DoneSentinel,
		SentinelPace: p.Stream.Pace.Duration(),
	}
	if terminal != nil && terminal.OmitDone {
		stream.Sentinel = nil
	}
	if !omitUsage {
		usageBytes, err := json.Marshal(usage)
		if err != nil {
			return nil, err
		}
		stream.Usage = json.RawMessage(usageBytes)
		costTotal := usage.Cost.TotalCost
		stream.CostTotal = &costTotal
	}
	return stream, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
