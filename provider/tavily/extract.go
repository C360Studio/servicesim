package tavily

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// PatternExtract is the ServeMux pattern for POST /extract.
const PatternExtract = "POST /extract"

// FaultKeyExtract is /extract's own attempt budget, kept apart from
// FaultKeySearch so a retry of one call never consumes the other's counter —
// the same reasoning Exa's exa:answer draws on beside exa:search.
const FaultKeyExtract = "tavily:extract"

// extractPlacements are the placements POST /extract accepts: the documented
// Authorization: Bearer header only.
//
// This briefly also accepted a body-placed api_key, on D2's client-level
// reasoning (docs/adopter-backlog.md, "Decisions already taken") extended past
// /search and /research to every route by analogy. The owner reaffirmed on
// 2026-08-15 that vendor documentation, not a consumer's client code, decides
// a wire contract (ADR-0002) — see contracts/tavily/README.md's "POST
// /extract" → "Auth" section. D2 itself stands exactly as shipped for /search
// and /research; it is not a precedent for routes verified after it. A body
// api_key on /extract is still recognised — see extractKnownFields and
// warnExtractCredentialField — so it is flagged with
// tavily.api_key.in_body, but it does not authenticate: a request presenting
// only a body api_key fails auth.missing here, the same as any other
// unaccepted placement.
var extractPlacements = []string{provider.PlacementAuthorization}

// RouteExtract returns POST /extract, keyed "tavily:extract". It serves the
// same "tavily" scenario entry /search does — Route.Entry defaults to the
// listener's own provider name — and its fault plan lives inside the
// projection's `extract:` sub-key (extractFault) rather than the block-level
// `fault:`, which belongs to /search. This mirrors Exa's exa:answer /
// AnswerProjection.Fault precedent exactly.
func RouteExtract() provider.Route {
	return provider.Route{
		Pattern:     PatternExtract,
		FaultKey:    FaultKeyExtract,
		Credentials: extractPlacements,
		Fault:       extractFault,
	}
}

// extractFault returns /extract's attempt budget: the first turn whose
// projection declares one under `extract.fault`. Nil-safe on every hop — the
// scenario, the registry, the entry, its turns and the projection's extract
// block may each be absent — because it runs at composition time against a
// scenario that may not have been validated yet.
func extractFault(s *scenario.Scenario) *scenario.Fault {
	e := s.Provider(Name)
	if e == nil {
		return nil
	}
	for i := range e.Turns {
		var p Projection
		if err := e.Turns[i].DecodeProjection(Name, i, &p); err != nil {
			// A projection that cannot be decoded is reported by
			// Validator.ValidateProjections, which runs before readiness.
			// Here it simply means this turn declares no /extract plan.
			continue
		}
		if p.Extract != nil && p.Extract.Fault.HasAttempts() {
			return p.Extract.Fault
		}
	}
	return nil
}

// Finding codes POST /extract raises. request.unknown_field and the shared
// auth codes declared in request.go apply here too.
const (
	// CodeExtractURLsMissing reports an absent urls property, a blank string,
	// or an empty array — the only required field, with nothing in it to
	// resolve either way. A present value of the wrong JSON type, or an array
	// containing a non-string element, is CodeExtractURLsInvalid instead.
	CodeExtractURLsMissing = "tavily.extract.urls.missing"

	// CodeExtractURLsInvalid reports a urls property of the wrong JSON type, or
	// a urls array containing a non-string element.
	CodeExtractURLsInvalid = "tavily.extract.urls.invalid"

	// CodeExtractDepthInvalid reports an extract_depth outside basic|advanced.
	CodeExtractDepthInvalid = "tavily.extract.extract_depth.invalid"

	// CodeExtractFormatInvalid reports a format outside markdown|text.
	CodeExtractFormatInvalid = "tavily.extract.format.invalid"

	// CodeExtractChunksPerSourceRange reports a chunks_per_source outside
	// 1-5 — a wider range than /search's chunks_per_source, which is 1-3.
	CodeExtractChunksPerSourceRange = "tavily.extract.chunks_per_source.range"

	// CodeExtractTimeoutRange reports a timeout outside 1.0-60.0 seconds.
	CodeExtractTimeoutRange = "tavily.extract.timeout.range"

	// CodeExtractFailureUnrequested warns that a projection's
	// extract.failed_results names a URL the request did not send. The
	// override is not rendered; see D-a in docs/adopter-backlog.md's Phase 4
	// section.
	CodeExtractFailureUnrequested = "tavily.extract.failure_unrequested"
)

// ExtractDepths is the extract_depth enum.
var ExtractDepths = []string{"basic", "advanced"}

// ExtractFormats is the format enum.
var ExtractFormats = []string{"markdown", "text"}

// Documented /extract defaults and bounds.
const (
	// DefaultExtractDepth is the documented default for extract_depth.
	DefaultExtractDepth = "basic"

	// DefaultExtractFormat is the documented default for format.
	DefaultExtractFormat = "markdown"

	extractChunksPerSourceMin = 1
	extractChunksPerSourceMax = 5

	extractTimeoutMin = 1.0
	extractTimeoutMax = 60.0

	// extractTimeoutDefaultBasic and extractTimeoutDefaultAdvanced are the
	// documented default timeout, itself conditional on extract_depth.
	extractTimeoutDefaultBasic    = 10.0
	extractTimeoutDefaultAdvanced = 30.0
)

// extractKnownFields is every property the live /extract schema documents,
// plus api_key — recognised here in order to be flagged by
// warnExtractCredentialField (CodeAPIKeyInBody) rather than falling through to
// the generic request.unknown_field, the same distinction /search's own
// knownFields comment draws. A property outside this set raises
// request.unknown_field.
var extractKnownFields = map[string]bool{
	"urls": true, "query": true, "chunks_per_source": true, "extract_depth": true,
	"include_images": true, "include_favicon": true, "format": true, "timeout": true,
	"include_usage": true,

	// Recognised in order to be flagged by warnExtractCredentialField, not in
	// order to be accepted: /extract authenticates on Authorization: Bearer
	// only — see extractPlacements.
	"api_key": true,
}

// extractRequest is a POST /extract request after validation, with every
// documented default applied. A request that failed validation still
// produces one: the handler decides whether to render it.
type extractRequest struct {
	URLs            []string
	Query           string
	ChunksPerSource int
	ExtractDepth    string
	IncludeImages   bool
	IncludeFavicon  bool
	Format          string
	Timeout         float64
	IncludeUsage    bool
}

// parseExtractRequest validates the request body and returns it with
// defaults applied, recording one finding per problem. It never returns nil.
func parseExtractRequest(x *provider.Exchange) *extractRequest {
	req := &extractRequest{
		ExtractDepth:    DefaultExtractDepth,
		Format:          DefaultExtractFormat,
		ChunksPerSource: DefaultChunksPerSource,
	}

	checkContentType(x)
	if x.Body == nil {
		// A body that was malformed or not an object already carries its own
		// finding from the shared lifecycle; adding "urls is required" on top
		// would report a consequence as though it were a second cause.
		if !x.HasFinding(provider.CodeMalformedJSON) && !x.HasFinding(provider.CodeBodyNotObject) &&
			!x.HasFinding(provider.CodeBodyTooLarge) && !x.HasFinding(provider.CodeBodyRead) {
			x.Fail(CodeExtractURLsMissing, "urls", "Missing urls. 'urls' is a required string or array of strings.")
		}
		return req
	}

	warnExtractUnknownFields(x)
	warnExtractCredentialField(x)

	req.URLs = parseExtractURLs(x)
	req.Query = parseString(x, "query")
	req.ExtractDepth = parseEnum(x, "extract_depth", DefaultExtractDepth, ExtractDepths, CodeExtractDepthInvalid)
	req.Format = parseEnum(x, "format", DefaultExtractFormat, ExtractFormats, CodeExtractFormatInvalid)
	req.ChunksPerSource = parseInt(x, "chunks_per_source", DefaultChunksPerSource,
		extractChunksPerSourceMin, extractChunksPerSourceMax, CodeExtractChunksPerSourceRange)
	req.IncludeImages = parseBool(x, "include_images")
	req.IncludeFavicon = parseBool(x, "include_favicon")
	req.IncludeUsage = parseBool(x, "include_usage")
	req.Timeout = parseFloatRange(x, "timeout", extractDefaultTimeout(req.ExtractDepth),
		extractTimeoutMin, extractTimeoutMax, CodeExtractTimeoutRange)

	return req
}

// extractDefaultTimeout returns the documented default, itself conditional on
// extract_depth: 10 seconds at basic, 30 at advanced.
func extractDefaultTimeout(depth string) float64 {
	if depth == "advanced" {
		return extractTimeoutDefaultAdvanced
	}
	return extractTimeoutDefaultBasic
}

// parseExtractURLs reads the union-typed, required urls property: the
// vendor's own request examples show both a bare string and an array, so a
// validator accepting only one form rejects valid traffic.
//
// No length bound is enforced: the 20-URL cap contracts/tavily/README.md
// records is inferred from the 400 example's text, not a declared schema
// constraint, and D-d says not to invent an enforcement the page does not
// state.
func parseExtractURLs(x *provider.Exchange) []string {
	if !x.Has("urls") {
		x.Fail(CodeExtractURLsMissing, "urls", "Missing urls. 'urls' is a required string or array of strings.")
		return nil
	}
	switch v := x.Body["urls"].(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			x.Fail(CodeExtractURLsMissing, "urls", "Missing urls. 'urls' is a required string or array of strings.")
			return nil
		}
		return []string{v}
	case []any:
		if len(v) == 0 {
			// urls is the one required field; a zero-element array carries no
			// identifier to resolve, the array-form analogue of the blank
			// string rejected above.
			x.Fail(CodeExtractURLsMissing, "urls", "Missing urls. 'urls' is a required string or array of strings.")
			return nil
		}
		out := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				x.Fail(CodeExtractURLsInvalid, fmt.Sprintf("urls[%d]", i),
					"Invalid urls. Must be a string or an array of strings.")
				return nil
			}
			out = append(out, s)
		}
		return out
	default:
		x.Fail(CodeExtractURLsInvalid, "urls", "Invalid urls. Must be a string or an array of strings.")
		return nil
	}
}

// parseFloatRange reads an optional float property constrained to an
// inclusive range. It exists apart from parseInt because /extract's timeout
// is documented as a float, and parseInt's truncate-to-integer check would
// wrongly reject a fractional value the contract allows.
func parseFloatRange(x *provider.Exchange, field string, fallback, low, high float64, code string) float64 {
	if !x.Has(field) {
		return fallback
	}
	value, ok := x.Number(field)
	if !ok || value < low || value > high {
		// %.1f, not %v, so the bounds print the way the contract records the
		// range (1.0-60.0), not Go's default trimmed float formatting.
		x.Fail(code, field, "Invalid %s. Must be a number between %.1f and %.1f.", field, low, high)
		return fallback
	}
	return value
}

// warnExtractUnknownFields flags properties this build does not model for
// POST /extract.
//
// Keys are walked in sorted order, never Go map order, so a request with two
// unknown fields reports the same order on every run.
func warnExtractUnknownFields(x *provider.Exchange) {
	for _, key := range slices.Sorted(maps.Keys(x.Body)) {
		if !extractKnownFields[key] {
			x.Warn(CodeUnknownField, key, "unknown request field %q", key)
		}
	}
}

// warnExtractCredentialField raises CodeAPIKeyInBody for a body-placed
// api_key, the same code /search raises for the same property. Unlike
// /search, /extract does not accept this placement — see extractPlacements —
// so a request presenting only a body api_key still fails auth.missing; the
// finding exists so a consumer can see which placement its adapter used
// instead of debugging a bare 401.
//
// States presence and nothing else. Not the value, not a prefix of it, not
// its length: this message reaches the journal, /__admin/requests and the
// process log.
func warnExtractCredentialField(x *provider.Exchange) {
	if x.Has("api_key") {
		x.Warn(CodeAPIKeyInBody, "api_key",
			"api_key was sent in the request body; the vendor documents Authorization: Bearer")
	}
}

// ExtractProjection projects the POST /extract endpoint's own concerns.
//
// It carries no results of its own: D-a resolves every requested URL against
// this turn's ordinary `results:` list first — a lookup keyed on the result's
// resolved source URL, or its declared id — then against the corpus by exact
// scenario.Source.URL match. That is what makes `search -> extract` work with
// no authoring at all on a scenario like `happy`: the URLs /search already
// returns are corpus URLs.
type ExtractProjection struct {
	// Fault is /extract's own attempt budget, kept apart from the block-level
	// `fault:` that belongs to /search — see extractFault.
	Fault *scenario.Fault `yaml:"fault,omitempty"`

	// FailedResults forces specific requested URLs to render as a failure,
	// overriding whatever D-a's ordinary resolution would otherwise produce.
	// Matched by exact URL. An entry naming a URL the request did not send
	// raises the tavily.extract.failure_unrequested WARNING and is not
	// rendered.
	FailedResults []ExtractFailureProjection `yaml:"failed_results,omitempty"`

	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// ExtractFailureProjection forces one requested URL to fail.
type ExtractFailureProjection struct {
	URL string `yaml:"url"`

	// Error overrides the failed_results[].error text; defaults to
	// MessageExtractNotFound when left empty. failed_results[].error is
	// documented only as free text with no enum, so either is a scenario's
	// own choice.
	Error string `yaml:"error,omitempty"`
}

// MessageExtractNotFound is the fixed, simulator-chosen failed_results[].error
// text for a requested URL that resolves to nothing. contracts/tavily/README.md
// documents the field only as free text with no enum or set of causes, so this
// exact string is Servicesim's choice, not the vendor's.
const MessageExtractNotFound = "No content found at the requested URL."

// handleExtract serves POST /extract.
//
// D-a's rule: for each requested URL, resolve it — first against the selected
// turn's `results:` (this entry's ordinary /search projection), then against
// the corpus by exact URL match — and render one result for it, in request
// order. An unresolved URL renders no result and a failed_results entry
// instead. The order below matches handleSearch's fail-closed order:
// validation and authentication run first, and only a request that survived
// both selects a turn, because selecting a turn claims a fault attempt.
func handleExtract(x *provider.Exchange) provider.Response {
	entry := x.Entry()

	req := parseExtractRequest(x)
	checkAuth(x, entry)
	if x.Failed() {
		return errorResponse(x)
	}

	projection, ok := selectProjection(x, entry)
	if !ok {
		return errorResponse(x)
	}

	body, err := renderExtract(x, projection, req, keysFor(x))
	if err != nil {
		x.Fail(CodeProjectionInvalid, "", "the scenario projection could not be rendered: %v", err)
		return errorResponse(x)
	}

	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         Name + ".extract.ok",
		FaultEligible: true,
		FaultBody:     faultBody,
	}
}

// renderExtract projects p and req into a /extract response body.
func renderExtract(x *provider.Exchange, p *Projection, req *extractRequest, keys renderKeys) ([]byte, error) {
	overrides := extractFailureOverrides(x, p, req.URLs)

	results := make([]json.RawMessage, 0, len(req.URLs))
	failed := make([]extractFailedResultWire, 0, len(req.URLs))

	for _, url := range req.URLs {
		if reason, forced := overrides[url]; forced {
			failed = append(failed, extractFailedResultWire{URL: url, Error: reason})
			continue
		}

		resolution, ok := resolveExtractTarget(x, p, url)
		if !ok {
			failed = append(failed, extractFailedResultWire{URL: url, Error: MessageExtractNotFound})
			continue
		}

		encoded, err := renderExtractedResult(resolution, req)
		if err != nil {
			return nil, err
		}
		results = append(results, encoded)
	}

	body := extractBody{
		Results:       results,
		FailedResults: failed,
		ResponseTime:  renderResponseTime(p, keys),
		Usage:         renderExtractUsage(p, req),
		RequestID:     renderRequestID(p, keys),
	}
	return provider.Render(body, extractExtraFields(p), nil)
}

// extractExtraFields returns the /extract envelope's own extra_fields, which
// live under the `extract:` sub-key rather than the top-level one /search
// uses: the two response envelopes are different shapes, so their
// extra_fields are independent — the same relationship Exa's
// AnswerProjection.ExtraFields has to its top-level Projection.ExtraFields.
func extractExtraFields(p *Projection) scenario.ExtraFields {
	if p.Extract == nil {
		return nil
	}
	return p.Extract.ExtraFields
}

// extractFailureOverrides resolves `extract.failed_results` into a
// url->error lookup, warning and discarding any entry naming a URL the
// request did not send.
func extractFailureOverrides(x *provider.Exchange, p *Projection, requested []string) map[string]string {
	if p.Extract == nil || len(p.Extract.FailedResults) == 0 {
		return nil
	}
	requestedSet := make(map[string]bool, len(requested))
	for _, u := range requested {
		requestedSet[u] = true
	}

	overrides := make(map[string]string, len(p.Extract.FailedResults))
	for _, fr := range p.Extract.FailedResults {
		if !requestedSet[fr.URL] {
			x.Warn(CodeExtractFailureUnrequested, "extract.failed_results",
				"failed_results names %q, which the request did not send; the override is not rendered", fr.URL)
			continue
		}
		overrides[fr.URL] = firstNonEmpty(fr.Error, MessageExtractNotFound)
	}
	return overrides
}

// extractResolution is what rendering needs regardless of how a requested URL
// was resolved: through the selected turn's ordinary /search results, or
// straight off the corpus.
type extractResolution struct {
	URL        string
	RawContent string
	Favicon    string
	Images     []string
	Extra      scenario.ExtraFields
	Omit       []string
}

// resolveExtractTarget implements D-a's two-step lookup for one requested
// URL: first the selected turn's `results:` (keyed on the result's resolved
// source URL, or its declared id), then the corpus by exact
// scenario.Source.URL match.
func resolveExtractTarget(x *provider.Exchange, p *Projection, url string) (extractResolution, bool) {
	if projected, src, ok := findProjectedResult(p, url); ok {
		return extractResolution{
			URL:        url,
			RawContent: extractRawContent(projected, src),
			Favicon:    firstNonEmpty(projected.Favicon, src.Favicon),
			Images:     extractImageURLs(projected.Images),
			Extra:      projected.ExtraFields,
			Omit:       projected.OmitFields,
		}, true
	}
	if src, ok := corpusSourceByURL(x, url); ok {
		rendered := scenario.Render(scenario.SourceRef{Ref: src.ID, Target: src})
		return extractResolution{
			URL:        url,
			RawContent: firstNonEmpty(rendered.Text, rendered.FirstSnippet()),
			Favicon:    rendered.Favicon,
			Images:     corpusImageURLs(rendered.Image),
		}, true
	}
	return extractResolution{}, false
}

// findProjectedResult looks a requested URL up in the selected turn's
// existing /search results: a match on the resolved source URL, or on the
// result's declared id override.
func findProjectedResult(p *Projection, url string) (*ResultProjection, scenario.RenderedSource, bool) {
	for i := range p.Results {
		projected := &p.Results[i]
		src := scenario.Render(projected.SourceRef)
		if src.URL == url || (projected.ID != "" && projected.ID == url) {
			return projected, src, true
		}
	}
	return nil, scenario.RenderedSource{}, false
}

// corpusSourceByURL is D-a's fallback step: an exact match against the
// corpus, independent of whether any turn's results list mentions the source
// at all.
func corpusSourceByURL(x *provider.Exchange, url string) (*scenario.Source, bool) {
	if x.Deps.Scenario == nil {
		return nil, false
	}
	for i := range x.Deps.Scenario.Sources {
		if x.Deps.Scenario.Sources[i].URL == url {
			return &x.Deps.Scenario.Sources[i], true
		}
	}
	return nil, false
}

// extractRawContent picks raw_content: the result's own override, then the
// source's full text, then its search excerpt, then its first snippet.
//
// chunks_per_source and query's documented effect — the top-ranked chunks
// joined by a literal "[...]" separator — is not reproduced: Servicesim does
// not implement relevance ranking (CLAUDE.md, "What Servicesim is not"), so
// there is nothing to rank chunks by. This is simulator-chosen, the same
// spirit as /search's field-gating rule (D-c): render what the
// projection/corpus has rather than simulating vendor-side logic.
func extractRawContent(projected *ResultProjection, src scenario.RenderedSource) string {
	if raw, ok := projected.RawContent.Get(); ok {
		return raw
	}
	return firstNonEmpty(src.Text, projected.Content, src.FirstSnippet())
}

// extractImageURLs projects a result's declared images into the bare URL
// strings /extract documents — NOT the {url, description} objects /search
// uses for the same field name. contracts/tavily/README.md's /extract
// response table is explicit: results[].images is array[string].
func extractImageURLs(images []ImageProjection) []string {
	var out []string
	for _, img := range images {
		if img.URL != "" {
			out = append(out, img.URL)
		}
	}
	return out
}

// corpusImageURLs adapts a corpus source's single Image field to the
// array[string] shape: one element when the source declares an image, none
// when it does not.
func corpusImageURLs(image string) []string {
	if image == "" {
		return nil
	}
	return []string{image}
}

// renderExtractedResult renders one resolved /extract result, applying its
// per-URL extra_fields and omit_fields the same way /search's
// renderResultBody does — reusing ResultProjection's own mechanism rather
// than inventing a second one.
func renderExtractedResult(r extractResolution, req *extractRequest) (json.RawMessage, error) {
	result := extractedResultWire{
		URL:        r.URL,
		RawContent: r.RawContent,
	}
	if req.IncludeFavicon {
		result.Favicon = r.Favicon
	}
	if req.IncludeImages {
		result.Images = r.Images
	}

	encoded, err := provider.Render(result, r.Extra, r.Omit)
	if err != nil {
		return nil, fmt.Errorf("tavily: rendering extract result %q: %w", r.URL, err)
	}
	return encoded, nil
}

// renderExtractUsage emits the credit object when the request asked for it OR
// the scenario declared one — an "either holds" rule in addition to
// /search's request-only gating, because contracts/tavily/README.md marks
// include_usage's gating on this field itself as inferred, not read verbatim
// on the field (D-f). It applies only to /extract's own rendering, not to
// /search's existing renderUsage, which is unchanged.
func renderExtractUsage(p *Projection, req *extractRequest) *Usage {
	if !req.IncludeUsage && p.Usage == nil {
		return nil
	}
	if p.Usage != nil {
		return &Usage{Credits: p.Usage.Credits}
	}
	credits, ok := creditsPerDepth[req.ExtractDepth]
	if !ok {
		credits = 1
	}
	return &Usage{Credits: credits}
}

// extractBody is the POST /extract response envelope, in the field order
// contracts/tavily/README.md's response table documents.
type extractBody struct {
	Results       []json.RawMessage         `json:"results"`
	FailedResults []extractFailedResultWire `json:"failed_results"`
	ResponseTime  float64                   `json:"response_time"`
	Usage         *Usage                    `json:"usage,omitempty"`
	RequestID     string                    `json:"request_id,omitempty"`
}

// extractedResultWire is one entry of results[].
type extractedResultWire struct {
	URL        string   `json:"url"`
	RawContent string   `json:"raw_content"`
	Images     []string `json:"images,omitempty"`
	Favicon    string   `json:"favicon,omitempty"`
}

// extractFailedResultWire is one entry of failed_results[].
type extractFailedResultWire struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}
