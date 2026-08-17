package exa

import (
	"cmp"
	"net/http"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// patternContents and faultKeyContents are POST /contents' route pattern and
// fault budget key. The key's suffix after the colon ("contents") is the bare
// name a scenario's `when.route:` matches, per D-h.
const (
	patternContents  = "POST /contents"
	faultKeyContents = "exa:contents"
)

// Exa's documented default for a requested identifier this simulator cannot
// resolve: CRAWL_NOT_FOUND, 404 — contracts/exa/README.md's error-codes table,
// which the /contents section cites as "specific to the /contents endpoint".
const tagCrawlNotFound = "CRAWL_NOT_FOUND"

// The two statuses[].status values the contract documents.
const (
	contentsStatusSuccess = "success"
	contentsStatusError   = "error"
)

// codeContentsNoContentFound is journaled when D-g's NO_CONTENT_FOUND branch
// fires: every requested identifier failed to resolve.
const codeContentsNoContentFound = "exa.contents.no_content_found"

// RouteContents returns POST /contents, keyed "exa:contents". Its plan lives
// inside the projection, under `contents.fault`, following the answerFault
// precedent: the block-level `fault:` belongs to /search.
func RouteContents() provider.Route {
	return provider.Route{
		Pattern:     patternContents,
		FaultKey:    faultKeyContents,
		Credentials: authHeaders,
		Fault:       contentsFault,
	}
}

// contentsFault returns the /contents attempt budget: the first turn whose
// projection declares one, following answerFault's shape.
func contentsFault(s *scenario.Scenario) *scenario.Fault {
	e := s.Provider(providerName)
	if e == nil {
		return nil
	}
	for i := range e.Turns {
		var p Projection
		if err := e.Turns[i].DecodeProjection(providerName, i, &p); err != nil {
			continue
		}
		if p.Contents != nil && p.Contents.Fault.HasAttempts() {
			return p.Contents.Fault
		}
	}
	return nil
}

// handleContents serves POST /contents.
//
// D-a: the response is a pure function of the request and the scenario, not a
// relevance query. It shares turn selection and projection decoding with
// /search — the same `results:` a scenario writes for /search is what
// /contents resolves requested identifiers against — but renders from a
// resolution over the request's own ids/urls rather than from p.Results
// directly.
func handleContents(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	authenticate(x, entry)
	validateContentsRequest(x)
	if x.Failed() {
		return rejection(x)
	}

	p, ok := selectProjection(x, entry)
	if !ok {
		return rejection(x)
	}

	requestID := renderedRequestID(x, p.RequestID)
	identifiers := contentsIdentifiers(x)
	resolutions := resolveContentsIdentifiers(x.Deps.Scenario, p, identifiers)

	// D-g's NO_CONTENT_FOUND is decided from D-a's own default resolution,
	// BEFORE any contents.statuses override is applied. An override exists to
	// script a per-URL failure for an identifier the simulator would otherwise
	// have resolved — exactly the per-URL-failure golden this route ships with
	// — and that authored failure must not be reinterpreted as "nothing here at
	// all" just because it happens to be the only identifier in the request.
	if len(identifiers) > 0 && !anyContentsResolved(resolutions) {
		return contentsNoContentFound(x, requestID, len(identifiers))
	}

	applyContentsOverrides(x, resolutions, identifiers, p.Contents)
	body, err := renderContentsBody(resolutions, p.Contents, requestID)
	if err != nil {
		x.Fail(codeRenderFailed, "", "rendering the Exa contents response failed: %v", err)
		return rejection(x)
	}
	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         "exa.contents.ok",
		FaultEligible: true,
		FaultBody:     func(a scenario.FaultAttempt) []byte { return faultBody(requestID, a) },
	}
}

// contentsNoContentFound serves D-g's NO_CONTENT_FOUND branch: every requested
// identifier failed to resolve against the turn's projection and the corpus.
//
// The attempt was already claimed by selectProjection above, so this is a
// rendered scenario outcome and not a pre-attempt rejection (§4.4) — it stays
// FaultEligible, so a scripted fault can still override it the way it can
// override any other successful selection.
func contentsNoContentFound(x *provider.Exchange, requestID string, requested int) provider.Response {
	x.Warn(codeContentsNoContentFound, "",
		"none of the %d requested identifiers resolved against the scenario", requested)
	return provider.Response{
		Status:        http.StatusBadRequest,
		Body:          errorBody(requestID, messageNoContentFound, TagNoContentFound, http.StatusBadRequest),
		Label:         "exa.error." + TagNoContentFound,
		FaultEligible: true,
		FaultBody:     func(a scenario.FaultAttempt) []byte { return faultBody(requestID, a) },
	}
}

// contentsResolution is what became of one requested /contents identifier,
// after D-a's default resolution and any scenario override have both applied.
type contentsResolution struct {
	// id is the identifier exactly as the request sent it — a document id or a
	// URL — echoed back on statuses[].id verbatim, never a resolved value.
	id string

	// result is nil for an identifier that did not resolve, or that a scenario
	// override forced to a non-success status.
	result *Result

	status  string
	source  string
	errTag  string
	errCode *int
}

// contentsIdentifiers reads the request's ids[]/urls[] in D-a's request order.
//
// D-d accepts both fields present at once (with a warning raised by
// validateContentsRequest); this function does not re-decide that, it simply
// walks whichever arrays are present, ids before urls, which is the
// deterministic order this simulator chose for that accepted-but-unusual case.
//
// A non-string element is skipped: validateContentsRequest has already raised
// exa.contents.item.invalid for it (INVALID_URLS, which rejects the request
// before this ever runs), so reaching a non-string element here would mean a
// scenario without validation wired — resolveContentsIdentifiers must not
// panic on it either way.
func contentsIdentifiers(x *provider.Exchange) []string {
	var out []string
	if ids, ok := x.Body["ids"].([]any); ok {
		out = append(out, stringElements(ids)...)
	}
	if urls, ok := x.Body["urls"].([]any); ok {
		out = append(out, stringElements(urls)...)
	}
	return out
}

func stringElements(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// contentsIndex is D-a's lookup table: the selected turn's projection results,
// keyed by the result's source URL and by its declared id — which defaults to
// the URL (renderResult's own default), so a result with no explicit `id:`
// override is still found by either key.
type contentsIndex struct {
	byKey map[string]*ResultProjection
}

func buildContentsIndex(p *Projection) contentsIndex {
	idx := contentsIndex{byKey: make(map[string]*ResultProjection, 2*len(p.Results))}
	for i := range p.Results {
		rp := &p.Results[i]
		src := scenario.Render(rp.SourceRef)
		if src.URL != "" {
			idx.byKey[src.URL] = rp
		}
		if id := cmp.Or(rp.ID, src.URL); id != "" {
			idx.byKey[id] = rp
		}
	}
	return idx
}

// corpusLookup is D-a's fallback step: an exact match against a canonical
// source's URL. This is what makes `search -> contents` on the "happy"
// built-in work with no authoring at all — /search's own results are corpus
// URLs, so a /contents call naming one of them resolves here even when the
// turn's projection never mentions /contents.
func corpusLookup(s *scenario.Scenario, identifier string) (*scenario.Source, bool) {
	if s == nil {
		return nil, false
	}
	for i := range s.Sources {
		if s.Sources[i].URL == identifier {
			return &s.Sources[i], true
		}
	}
	return nil, false
}

// resolveContentsIdentifiers builds one contentsResolution per requested
// identifier, in request order, applying only D-a's two-step DEFAULT
// resolution. Overrides are a separate, later step — see the call site in
// handleContents, which decides D-g's NO_CONTENT_FOUND branch from this
// function's result before any override can change it.
func resolveContentsIdentifiers(s *scenario.Scenario, p *Projection, identifiers []string) []contentsResolution {
	idx := buildContentsIndex(p)
	out := make([]contentsResolution, len(identifiers))
	for i, id := range identifiers {
		out[i] = resolveOneContentsIdentifier(s, idx, id)
	}
	return out
}

func resolveOneContentsIdentifier(s *scenario.Scenario, idx contentsIndex, id string) contentsResolution {
	if rp, ok := idx.byKey[id]; ok {
		r := renderResult(rp)
		return contentsResolution{id: id, result: &r, status: contentsStatusSuccess}
	}
	if src, ok := corpusLookup(s, id); ok {
		r := renderResult(&ResultProjection{SourceRef: scenario.SourceRef{Target: src}})
		return contentsResolution{id: id, result: &r, status: contentsStatusSuccess}
	}
	code := 404
	return contentsResolution{id: id, status: contentsStatusError, errTag: tagCrawlNotFound, errCode: &code}
}

// applyContentsOverrides applies a projection's contents.statuses overrides in
// place, matched by identifier. An override naming an identifier the request
// did not send raises exa.contents.status_unrequested and is not applied — D-a.
func applyContentsOverrides(
	x *provider.Exchange, resolutions []contentsResolution, identifiers []string, cp *ContentsProjection,
) {
	if cp == nil || len(cp.Statuses) == 0 {
		return
	}
	requested := make(map[string]bool, len(identifiers))
	for _, id := range identifiers {
		requested[id] = true
	}
	// A requested identifier can appear more than once (a duplicate ids/urls
	// element); every occurrence gets its own statuses[] entry, so an override
	// naming that identifier must apply to all of them, not just the last one
	// resolveContentsIdentifiers happened to index — otherwise results[] and
	// statuses[] would disagree about the very identifier the override named.
	byID := make(map[string][]int, len(resolutions))
	for i, r := range resolutions {
		byID[r.id] = append(byID[r.id], i)
	}

	for _, override := range cp.Statuses {
		if !requested[override.ID] {
			x.Warn(codeContentsStatusUnrequested, "contents.statuses",
				"a contents.statuses override names %q, which this request did not send in ids or urls; it is not rendered",
				override.ID)
			continue
		}
		for _, idx := range byID[override.ID] {
			applyOneContentsOverride(&resolutions[idx], override)
		}
	}
}

// applyOneContentsOverride applies one matched override to its resolution.
//
// A status forced away from "success" drops the rendered result, whether or
// not the default resolution had found one: a vendor statuses[].error entry
// means the document did not come back, and results[] must agree with
// statuses[] the way the live API's does. The reverse — an override naming
// "success" for an identifier the default resolution could not find any data
// for — is accepted but renders no result either, since there is no source
// text to build one from; that combination is an authoring mistake this
// package does not attempt to paper over.
func applyOneContentsOverride(r *contentsResolution, override ContentsStatusProjection) {
	if override.Status != "" {
		r.status = override.Status
	}
	if override.Source != "" {
		r.source = override.Source
	}
	if override.Error != nil {
		r.errTag = override.Error.Tag
		r.errCode = override.Error.HTTPStatusCode
	}
	if r.status != contentsStatusSuccess {
		r.result = nil
		if r.errTag == "" {
			// The override forced a non-success status without naming its own
			// error (no `error:` block, or one with an empty tag). Left alone
			// this would render statuses[].error.tag as "" — a value outside the
			// six-member enum contracts/exa/README.md documents, which the real
			// API never sends. Falling back to the same CRAWL_NOT_FOUND/404 an
			// unresolved identifier gets keeps the wire shape inside the
			// documented enum; an explicit http_status_code (including the
			// documented null case) is left untouched.
			r.errTag = tagCrawlNotFound
			if r.errCode == nil {
				code := http.StatusNotFound
				r.errCode = &code
			}
		}
	}
}

// anyContentsResolved reports whether at least one requested identifier
// resolved to a result. D-g's NO_CONTENT_FOUND condition is this negated.
func anyContentsResolved(resolutions []contentsResolution) bool {
	for _, r := range resolutions {
		if r.result != nil {
			return true
		}
	}
	return false
}

// renderContentsBody renders the /contents 200 body from its resolutions. The
// caller has already decided the NO_CONTENT_FOUND branch (D-g) before calling
// this — every path reaching here renders a 200-shaped body.
func renderContentsBody(resolutions []contentsResolution, cp *ContentsProjection, requestID string) ([]byte, error) {
	results := make([]Result, 0, len(resolutions))
	statuses := make([]ContentsStatus, 0, len(resolutions))
	for _, r := range resolutions {
		if r.result != nil {
			results = append(results, *r.result)
		}
		status := ContentsStatus{ID: r.id, Status: r.status, Source: r.source}
		if r.status != contentsStatusSuccess {
			status.Error = &ContentsStatusError{Tag: r.errTag, HTTPStatusCode: r.errCode}
		}
		statuses = append(statuses, status)
	}

	var cost *CostProjection
	var extra scenario.ExtraFields
	if cp != nil {
		cost = cp.CostDollars
		extra = cp.ExtraFields
	}

	resp := ContentsResponse{
		RequestID:   requestID,
		Results:     results,
		Statuses:    statuses,
		CostDollars: renderCost(cost),
	}
	return provider.Render(resp, extra, nil)
}
