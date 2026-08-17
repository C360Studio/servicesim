package tavily

import (
	"encoding/json"
	"fmt"
	"math"

	"gopkg.in/yaml.v3"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Projection is how the shared corpus renders through the Tavily API. It is the
// decoded form of a turn's respond body: for the single-shot form that is the
// provider block minus its reserved envelope keys, and for a scripted provider
// it is one turn's respond mapping.
//
// It lives here rather than in the scenario package because the scenario
// package is level 1 and cannot know what a Tavily result looks like without
// importing this package and closing an import cycle. Turn.DecodeProjection is
// the seam, and [Validator] is what makes a malformed body fail at startup
// instead of on the first request.
//
// It is deliberately not named TavilyProjection: inside package tavily that
// name stutters, and revive fails the build on it.
type Projection struct {
	// RequestID overrides the derived request_id. Tavily's documented example is
	// a UUID, not a readable slug, so the derived value is a version 5 UUID.
	RequestID string `yaml:"request_id,omitempty"`

	// Answer is a pointer so a scenario can distinguish "this scenario has no
	// answer to give" (nil) from "the answer is the empty string" (""). It is
	// rendered only when the request asked for an answer; otherwise the wire
	// field is null, which is what the real API does.
	Answer *string `yaml:"answer,omitempty"`

	Images  []ImageProjection  `yaml:"images,omitempty"`
	Results []ResultProjection `yaml:"results,omitempty"`

	// ResponseTime is a scenario constant, never a measurement: measuring it
	// would make it the one nondeterministic field of an otherwise
	// byte-reproducible response. When the scenario leaves it zero a stable
	// value is derived from the same key tuple the identifiers use.
	ResponseTime float64 `yaml:"response_time,omitempty"`

	// AutoParameters is echoed as the response's auto_parameters object, and
	// only when the request set auto_parameters: true. When the scenario
	// declares none, the effective search_depth and topic are reported, which is
	// the shape the vendor's own example carries.
	AutoParameters map[string]any `yaml:"auto_parameters,omitempty"`

	// Usage is emitted only when the request set include_usage: true.
	Usage *UsageProjection `yaml:"usage,omitempty"`

	// Extract projects POST /extract's own concerns — its attempt budget and
	// any per-URL failure overrides. Absent means every requested URL resolves
	// through D-a's ordinary lookup (this turn's Results, then the corpus)
	// with no forced failures. See extract.go.
	Extract *ExtractProjection `yaml:"extract,omitempty"`

	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// ResultProjection is one entry of the Tavily results array.
type ResultProjection struct {
	scenario.SourceRef `yaml:",inline"`

	// ID overrides the wire field results[].id, whose default is the derived
	// xxxxxx-NN shape Tavily's documented example uses. It shadows nothing: the
	// inlined reference is SourceRef.Ref.
	ID string `yaml:"id,omitempty"`

	// Content overrides the excerpt, which otherwise comes from the source's
	// first snippet and falls back to its text.
	Content string `yaml:"content,omitempty"`

	// Score is the relevance score. Left zero, a stable value in [0.5, 1.0) is
	// derived from the source id, because a corpus of results all scoring zero
	// gives a consumer that ranks by score nothing to rank.
	Score float64 `yaml:"score,omitempty"`

	// RawContent overrides the raw_content string. It is emitted only when the
	// request asked for raw content; the source's text is the fallback.
	RawContent scenario.Nullable `yaml:"raw_content,omitempty"`

	Favicon string            `yaml:"favicon,omitempty"`
	Images  []ImageProjection `yaml:"images,omitempty"`

	// OmitFields drops named response fields that would otherwise be present,
	// for tests that assert a consumer fails on a missing required field.
	OmitFields  []string             `yaml:"omit_fields,omitempty"`
	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// rawResultProjection is the mapping branch's decode target.
//
// It is a hand-written mirror rather than `type rawResultProjection
// ResultProjection`, and that is not a stylistic choice. A defined-type copy of
// a struct still embeds scenario.SourceRef, and an embedded field's methods are
// promoted to the copy as well — so the copy would still satisfy
// yaml.Unmarshaler through SourceRef.UnmarshalYAML. yaml.v3 v3.0.1 hands an
// inlined Unmarshaler the WHOLE mapping node (getStructInfo's inlineUnmarshalers
// path), so every result field would come back as "not found in type
// scenario.rawSourceRef". The scenario package's own warning about re-entering a
// custom unmarshaler covers same-type recursion; this is the embedded-type
// version of it, and it fails just as loudly but far less legibly.
//
// A field added to ResultProjection and forgotten here fails at load with an
// unknown-key error rather than decoding to zero, because the mirror is decoded
// strictly.
type rawResultProjection struct {
	Ref         string               `yaml:"source"`
	ID          string               `yaml:"id,omitempty"`
	Content     string               `yaml:"content,omitempty"`
	Score       float64              `yaml:"score,omitempty"`
	RawContent  scenario.Nullable    `yaml:"raw_content,omitempty"`
	Favicon     string               `yaml:"favicon,omitempty"`
	Images      []ImageProjection    `yaml:"images,omitempty"`
	OmitFields  []string             `yaml:"omit_fields,omitempty"`
	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// UnmarshalYAML accepts the scalar shorthand as well as the mapping form, so
// that "- source-a" means "this result references that source and takes every
// other default".
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
		SourceRef:   scenario.SourceRef{Ref: raw.Ref},
		ID:          raw.ID,
		Content:     raw.Content,
		Score:       raw.Score,
		RawContent:  raw.RawContent,
		Favicon:     raw.Favicon,
		Images:      raw.Images,
		OmitFields:  raw.OmitFields,
		ExtraFields: raw.ExtraFields,
	}
	return nil
}

// ImageProjection is one entry of a projected images array. Items are objects,
// not bare URL strings, at the top level and inside a result alike.
type ImageProjection struct {
	URL string `yaml:"url"`

	// Description is emitted only when the request set
	// include_image_descriptions: true.
	Description string `yaml:"description,omitempty"`
}

// UsageProjection projects the credit-usage object gated by include_usage.
type UsageProjection struct {
	Credits int `yaml:"credits"`
}

// Derived-value bounds. Every one of them is a function of stable scenario keys
// and never of a clock or a random source.
const (
	// scoreFloor and scoreCeiling bound a derived relevance score.
	scoreFloor   = 0.5
	scoreCeiling = 1.0

	// responseTimeFloor and responseTimeCeiling bound a derived response_time,
	// in seconds.
	responseTimeFloor   = 0.25
	responseTimeCeiling = 2.5

	// derivedIDPrefixLen is how many hex characters of the derived digest open a
	// results[].id, matching the documented example "a3f9c2-04".
	derivedIDPrefixLen = 6
)

// creditsPerDepth is the credit cost a derived usage object reports when the
// request asked for usage and the scenario declared none. Advanced search costs
// two credits; every other depth costs one.
var creditsPerDepth = map[string]int{"advanced": 2}

// renderKeys are the stable inputs every derived value in a response hangs off.
// Nothing in a rendered body is ever read from a clock or a random source, so
// these two fields are the whole of the simulator's entropy.
type renderKeys struct {
	// Seed is Scenario.SeedKey(). It keys the values that must NOT move between
	// attempts of one request — a document's id and its score are properties of
	// the document, not of the call that returned it.
	Seed string

	// ID is the identifier tuple from §3.1 of the package design: the seed, the
	// provider, the fault key, and the attempt index only when the route
	// actually declares a fault plan. It keys response_time, which is a property
	// of the scenario rather than of the call that read it.
	ID []string

	// Call is ID with the zero-based index of this call within its lane appended
	// unconditionally. It keys request_id, and nothing else: a real vendor
	// returns a distinct request_id per call, and one value repeated for every
	// call collapses a consumer's log correlation to a single point.
	//
	// Determinism is sharpened rather than weakened. Two consecutive calls
	// differ; call 0 of a fresh lane — a new namespace, a new process, a reset —
	// reproduces call 0 exactly.
	Call []string
}

// renderSearch projects p through req and returns the response bytes.
func renderSearch(p *Projection, req *searchRequest, keys renderKeys) ([]byte, error) {
	body := searchBody{
		Query:        req.Query,
		Answer:       renderAnswer(p, req),
		Images:       renderImages(p.Images, req),
		ResponseTime: renderResponseTime(p, keys),
		RequestID:    renderRequestID(p, keys),
		Usage:        renderUsage(p, req),
	}
	if req.AutoParameters {
		body.AutoParameters = renderAutoParameters(p, req)
	}

	results, err := renderResults(p, req, keys)
	if err != nil {
		return nil, err
	}
	body.Results = results

	return provider.Render(body, p.ExtraFields, nil)
}

// renderAnswer gates the answer on the request. The key is always present; the
// value is null unless the request asked for an answer and the scenario has one
// to give.
func renderAnswer(p *Projection, req *searchRequest) *string {
	if !req.IncludeAnswer.Enabled || p.Answer == nil {
		return nil
	}
	answer := *p.Answer
	return &answer
}

// renderImages gates the images array on include_images and its descriptions on
// include_image_descriptions. The array itself is always present, empty rather
// than null, because the schema lists it as required.
func renderImages(images []ImageProjection, req *searchRequest) []Image {
	out := make([]Image, 0, len(images))
	if !req.IncludeImages {
		return out
	}
	for _, img := range images {
		entry := Image{URL: img.URL}
		if req.IncludeImageDescriptions {
			entry.Description = img.Description
		}
		out = append(out, entry)
	}
	return out
}

// renderResults projects the scenario's results in declaration order, truncated
// to max_results. Nothing is ever sorted by score: sorting would make the output
// depend on float comparison and would silently reorder equal scores.
func renderResults(p *Projection, req *searchRequest, keys renderKeys) ([]json.RawMessage, error) {
	limit := min(req.MaxResults, len(p.Results))
	if limit < 0 {
		limit = 0
	}
	out := make([]json.RawMessage, 0, limit)

	for i := range limit {
		projected := &p.Results[i]
		src := scenario.Render(projected.SourceRef)

		result := Result{
			Title:      src.Title,
			URL:        src.URL,
			Content:    renderContent(projected, src),
			Score:      renderScore(projected, src, keys),
			RawContent: renderRawContent(projected, src, req),
			ID:         renderResultID(projected, src, keys, i),
		}
		if req.IncludeFavicon {
			result.Favicon = firstNonEmpty(projected.Favicon, src.Favicon)
		}
		if req.IncludeImages {
			result.Images = renderImages(projected.Images, req)
			if len(result.Images) == 0 {
				// omitempty: a result with no images has no images key, which is
				// what the vendor's own single-result example shows.
				result.Images = nil
			}
		}
		if req.Topic == topicNews {
			if published, ok := src.PublishedAt.Get(); ok {
				result.PublishedDate = published
			}
		}

		encoded, err := renderResultBody(result, projected)
		if err != nil {
			return nil, err
		}
		out = append(out, encoded)
	}
	return out, nil
}

// renderResultBody merges a result's own extra fields and then drops its
// omitted ones, in that order, so that omit_fields can also remove a key
// extra_fields added.
func renderResultBody(result Result, projected *ResultProjection) (json.RawMessage, error) {
	encoded, err := provider.Render(result, projected.ExtraFields, projected.OmitFields)
	if err != nil {
		return nil, fmt.Errorf("tavily: rendering result %q: %w", projected.Ref, err)
	}
	return encoded, nil
}

// renderContent picks the result excerpt: the projection's override, then the
// source's first snippet, then its full text.
func renderContent(projected *ResultProjection, src scenario.RenderedSource) string {
	return firstNonEmpty(projected.Content, src.FirstSnippet(), src.Text)
}

// renderScore returns the declared score, or a stable value derived from the
// source id when the scenario declares none. It is rounded to two decimals so a
// golden file carries a readable number rather than seventeen digits.
//
// It is keyed on the seed and the source, never on the identifier tuple: a
// document's relevance is a property of the document, and a score that moved
// between two attempts of one retried request would be a diff no consumer could
// explain.
func renderScore(projected *ResultProjection, src scenario.RenderedSource, keys renderKeys) float64 {
	if projected.Score != 0 {
		return projected.Score
	}
	return round2(provider.FloatIn(scoreFloor, scoreCeiling, keys.Seed, string(Name), "score", resultKey(projected, src)))
}

// renderRawContent gates raw content on include_raw_content. The key is always
// present — Tavily's documented example emits it as null on a request that did
// not ask for it — and the source's text is the fallback when the projection
// declares no override.
func renderRawContent(projected *ResultProjection, src scenario.RenderedSource, req *searchRequest) scenario.Nullable {
	if !req.IncludeRawContent.Enabled {
		return scenario.NullNullable()
	}
	if raw, ok := projected.RawContent.Get(); ok {
		return scenario.SetNullable(raw)
	}
	if src.Text != "" {
		return scenario.SetNullable(src.Text)
	}
	return scenario.NullNullable()
}

// renderResultID returns the declared id or derives one shaped like Tavily's
// documented "a3f9c2-04": six hex characters of a digest over the source id,
// then the result's 1-based position in this response.
//
// Like the score, the digest is keyed on the seed and the source rather than on
// the identifier tuple, so a document keeps its id across attempts.
func renderResultID(projected *ResultProjection, src scenario.RenderedSource, keys renderKeys, index int) string {
	if projected.ID != "" {
		return projected.ID
	}
	digest := provider.Hex32(keys.Seed, string(Name), resultKey(projected, src))
	return fmt.Sprintf("%s-%02d", digest[:derivedIDPrefixLen], index+1)
}

// resultKey is the stable fixture key a result's derived values hang off: the
// resolved source id, falling back to the unresolved reference so that a
// scenario error cannot become a collision between two different results.
func resultKey(projected *ResultProjection, src scenario.RenderedSource) string {
	if src.ID != "" {
		return src.ID
	}
	return projected.Ref
}

// renderResponseTime returns the scenario's constant, or a stable derived value
// when it declares none.
func renderResponseTime(p *Projection, keys renderKeys) float64 {
	if p.ResponseTime != 0 {
		return p.ResponseTime
	}
	parts := append(append([]string{}, keys.ID...), "response_time")
	return round2(provider.FloatIn(responseTimeFloor, responseTimeCeiling, parts...))
}

// renderRequestID returns the scenario's override or a version 5 UUID derived
// from the per-call identifier tuple, matching the UUID shape Tavily documents
// rather than a readable slug.
//
// It is keyed on Call rather than ID: successive calls must not share a
// request_id, or a consumer correlating a log line to a request finds every
// request in the run pointing at the same one.
func renderRequestID(p *Projection, keys renderKeys) string {
	if p.RequestID != "" {
		return p.RequestID
	}
	return provider.UUIDv5(keys.Call...)
}

// renderUsage emits the credit object only when the request asked for it,
// defaulting to the documented cost of the effective search depth when the
// scenario declares none.
func renderUsage(p *Projection, req *searchRequest) *Usage {
	if !req.IncludeUsage {
		return nil
	}
	if p.Usage != nil {
		return &Usage{Credits: p.Usage.Credits}
	}
	credits, ok := creditsPerDepth[req.SearchDepth]
	if !ok {
		credits = 1
	}
	return &Usage{Credits: credits}
}

// renderAutoParameters echoes the parameters the simulator "chose". A scenario
// override wins; otherwise the effective depth and topic are reported, which is
// the shape the vendor's example carries.
func renderAutoParameters(p *Projection, req *searchRequest) map[string]any {
	if len(p.AutoParameters) > 0 {
		out := make(map[string]any, len(p.AutoParameters))
		for k, v := range p.AutoParameters {
			out[k] = v
		}
		return out
	}
	return map[string]any{"search_depth": req.SearchDepth, "topic": req.Topic}
}

// round2 rounds to two decimal places, which is what keeps a derived float
// readable in a golden file. It is applied to derived values only; a scenario's
// own number is emitted exactly as written.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
