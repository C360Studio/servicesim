package tavily

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// extractValidationScenario declares a source and nothing under `results:`.
// D-a's corpus fallback is what makes a request naming this source's URL
// resolve with no /search projection at all, so this is deliberately the
// smallest scenario that renders a 200 for /extract.
const extractValidationScenario = `
version: 1
name: extract-validation
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Report A full text.
providers:
  tavily:
    response_time: 1.15
`

// TestExtractRequestValidation walks POST /extract's documented request
// fields. Being strict about the request is the point of the journal: a
// consumer must be able to prove it sent the correct vendor request.
func TestExtractRequestValidation(t *testing.T) {
	t.Parallel()

	const url = `"https://example.test/report-a"`

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{
			name: "a bare string urls is accepted", body: `{"urls":` + url + `}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "an array urls is accepted", body: `{"urls":[` + url + `]}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "urls is required", body: `{}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractURLsMissing,
			wantDetail: "Missing urls. 'urls' is a required string or array of strings.",
		},
		{
			name: "an empty string urls is missing", body: `{"urls":"   "}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractURLsMissing,
		},
		{
			name: "urls of the wrong type", body: `{"urls":7}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractURLsInvalid,
			wantDetail: "Invalid urls. Must be a string or an array of strings.",
		},
		{
			name: "a urls array with a non-string element", body: `{"urls":[` + url + `,7]}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractURLsInvalid,
		},
		{
			name: "extract_depth advanced is valid", body: `{"urls":` + url + `,"extract_depth":"advanced"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "extract_depth outside the two-value enum", body: `{"urls":` + url + `,"extract_depth":"deep"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractDepthInvalid,
			wantDetail: "Invalid extract_depth. Must be 'basic' or 'advanced'.",
		},
		{
			name: "format text is valid", body: `{"urls":` + url + `,"format":"text"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "format outside the two-value enum", body: `{"urls":` + url + `,"format":"html"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractFormatInvalid,
		},
		{
			// The upper bound is 5, not /search's 3 — proving the two fields of
			// the same name draw on separate ranges.
			name:       "chunks_per_source at the upper bound of 5 is valid",
			body:       `{"urls":` + url + `,"chunks_per_source":5}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "chunks_per_source above the range", body: `{"urls":` + url + `,"chunks_per_source":6}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractChunksPerSourceRange,
		},
		{
			name: "chunks_per_source below the range", body: `{"urls":` + url + `,"chunks_per_source":0}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractChunksPerSourceRange,
		},
		{
			name: "a fractional timeout within range is valid", body: `{"urls":` + url + `,"timeout":45.5}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "timeout above the range", body: `{"urls":` + url + `,"timeout":61}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractTimeoutRange,
		},
		{
			name: "timeout below the range", body: `{"urls":` + url + `,"timeout":0.5}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractTimeoutRange,
		},
		{
			name: "a boolean field of the wrong type", body: `{"urls":` + url + `,"include_images":"yes"}`,
			wantStatus: http.StatusBadRequest, wantCode: "tavily.include_images.invalid",
		},
		{
			name: "include_favicon of the wrong type", body: `{"urls":` + url + `,"include_favicon":"yes"}`,
			wantStatus: http.StatusBadRequest, wantCode: "tavily.include_favicon.invalid",
		},
		{
			name: "include_usage of the wrong type", body: `{"urls":` + url + `,"include_usage":"yes"}`,
			wantStatus: http.StatusBadRequest, wantCode: "tavily.include_usage.invalid",
		},
		{
			name: "query of the wrong type", body: `{"urls":` + url + `,"query":7}`,
			wantStatus: http.StatusBadRequest, wantCode: "tavily.query.invalid",
		},
		{
			// The contract documents no nullable /extract field; an explicit
			// null on an optional string property fails type validation the
			// same way any other wrong type does, same as a present-but-wrong
			// type value — this pins that reading rather than leaving it
			// merely probed.
			name:       "explicit null on an optional field is rejected, not treated as absent",
			body:       `{"urls":` + url + `,"query":null}`,
			wantStatus: http.StatusBadRequest, wantCode: "tavily.query.invalid",
		},
		{
			name:       "chunks_per_source is documented as an integer; a fractional value is rejected",
			body:       `{"urls":` + url + `,"chunks_per_source":2.5}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractChunksPerSourceRange,
		},
		{
			// timeout is documented as a float (1.0-60.0); a JSON integer must
			// still be accepted, since encoding/json has no separate integer
			// literal type once decoded into a generic map.
			name:       "timeout sent as a JSON integer is accepted",
			body:       `{"urls":` + url + `,"timeout":10}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "an empty urls array is missing, the same as an empty string",
			body:       `{"urls":[]}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractURLsMissing,
		},
		{
			name: "a body that is not JSON at all", body: `{`,
			wantStatus: http.StatusBadRequest, wantCode: provider.CodeMalformedJSON,
		},
		{
			name: "a body that is not a JSON object", body: `[1,2,3]`,
			wantStatus: http.StatusBadRequest, wantCode: provider.CodeBodyNotObject,
		},
		{
			name: "an absent body", body: ``,
			wantStatus: http.StatusBadRequest, wantCode: CodeExtractURLsMissing,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ring := journal.NewRing(8, 1<<16)
			rec := post(t, newHandler(t, extractValidationScenario, ring), "/extract", tc.body, bearer)

			require.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantCode != "" {
				require.Contains(t, findingCodes(t, ring), tc.wantCode)
			}
			if tc.wantDetail != "" {
				require.Equal(t, tc.wantDetail, errorDetail(t, rec))
			}
		})
	}
}

// TestExtractUnknownFieldIsAWarning proves an unmodelled property is flagged,
// not rejected: the real API accepts fields this build does not model.
func TestExtractUnknownFieldIsAWarning(t *testing.T) {
	t.Parallel()

	ring := journal.NewRing(8, 1<<16)
	rec := post(t, newHandler(t, extractValidationScenario, ring), "/extract",
		`{"urls":"https://example.test/report-a","rerank_model":"v2"}`, bearer)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, findingCodes(t, ring), CodeUnknownField)
}

// TestExtractAuthPlacements makes assertable the owner's 2026-08-15
// reaffirmation that vendor documentation, not a consumer's client, decides a
// wire contract (see extractPlacements' own comment): /extract's vendor page
// documents Authorization: Bearer only, D2's body-api_key placement is not
// extended to this route, and a body-placed api_key is still recognised and
// flagged — via the shared CodeAPIKeyInBody warning — without authenticating.
// An x-api-key header never authenticates anything on this provider.
func TestExtractAuthPlacements(t *testing.T) {
	t.Parallel()

	ring := journal.NewRing(8, 1<<16)
	handler := newHandler(t, extractValidationScenario, ring)

	rec := post(t, handler, "/extract",
		`{"urls":"https://example.test/report-a","api_key":"tvly-test-key"}`, "")
	require.Equal(t, http.StatusUnauthorized, rec.Code,
		"a body api_key must not authenticate /extract; the vendor documents Bearer only")

	codes := findingCodes(t, ring)
	require.Contains(t, codes, CodeAuthMissing, "no accepted placement carried a value")
	require.Contains(t, codes, CodeAPIKeyInBody, "the unaccepted body placement is still named, not silently dropped")
	require.NotContains(t, codes, CodeUnknownField, "api_key is a recognised credential field, not an unmodelled one")

	rec = post(t, handler, "/extract", `{"urls":"https://example.test/report-a"}`, bearer)
	require.Equal(t, http.StatusOK, rec.Code, "the documented Bearer header authenticates")

	req := httptest.NewRequest(http.MethodPost, "/extract",
		strings.NewReader(`{"urls":"https://example.test/report-a"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "tvly-test-key")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	require.Equal(t, http.StatusUnauthorized, rec2.Code, "x-api-key never authenticates on Tavily, /extract included")
}

// extractHeaderOnlyScenario pins the accepted placements to the Authorization
// header alone via an explicit scenario auth.headers override, the same
// mechanism request_test.go pins on /search. /extract's own default is
// already Authorization-only, so this override changes nothing in practice —
// the test exists to prove the override mechanism still applies on /extract
// rather than being ignored by a route with narrowed Route.Credentials.
const extractHeaderOnlyScenario = `
version: 1
name: extract-header-only
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Report A full text.
providers:
  tavily:
    auth:
      headers: [authorization]
    response_time: 1.15
`

// TestExtractAuthHeadersOverrideStillApplies proves a scenario's
// auth.headers override still applies on /extract, and — since a body-placed
// api_key never authenticates this route regardless of the override — that
// the override does not somehow re-open a placement Route.Credentials
// already excludes.
func TestExtractAuthHeadersOverrideStillApplies(t *testing.T) {
	t.Parallel()

	ring := journal.NewRing(8, 1<<16)
	handler := newHandler(t, extractHeaderOnlyScenario, ring)

	rec := post(t, handler, "/extract",
		`{"urls":"https://example.test/report-a","api_key":"tvly-test-key"}`, "")
	require.Equal(t, http.StatusUnauthorized, rec.Code,
		"a body api_key must not authenticate /extract under an explicit authorization-only override")

	codes := findingCodes(t, ring)
	require.Contains(t, codes, CodeAuthMissing)
	require.Contains(t, codes, CodeAPIKeyInBody)
}

// extractResolutionScenario backs the D-a resolution-order tests: source-a is
// named by a /search-shaped `results:` entry with its own raw_content
// override, source-b is corpus-only and never mentioned by any results list.
const extractResolutionScenario = `
version: 1
name: extract-resolution
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Report A corpus text.
  - id: source-b
    url: https://example.test/report-b
    title: Report B
    text: Report B corpus text.
providers:
  tavily:
    response_time: 1.0
    results:
      - source: source-a
        raw_content: Report A projected raw content.
`

// TestExtractResolutionOrder proves D-a's two-step lookup end to end: a URL
// present in the selected turn's ordinary /search results resolves through
// its projection (and that projection's own override wins), a URL that is
// only in the corpus still resolves by exact URL match, and a URL naming
// neither renders a failed_results entry instead of a result.
func TestExtractResolutionOrder(t *testing.T) {
	t.Parallel()

	h := newHandler(t, extractResolutionScenario, nil)
	rec := post(t, h, "/extract", `{"urls":[
		"https://example.test/report-a",
		"https://example.test/report-b",
		"https://example.test/missing"
	]}`, bearer)
	require.Equal(t, http.StatusOK, rec.Code)

	out := decodeExtract(t, rec)
	results := extractResults(t, out)
	failed := extractFailed(t, out)

	require.Len(t, results, 2, "the two resolvable URLs render results, in request order")
	assert.Equal(t, "https://example.test/report-a", results[0]["url"])
	assert.Equal(t, "Report A projected raw content.", results[0]["raw_content"],
		"a result present in this turn's own results list uses ITS raw_content override")
	assert.Equal(t, "https://example.test/report-b", results[1]["url"])
	assert.Equal(t, "Report B corpus text.", results[1]["raw_content"],
		"a URL absent from every results list still resolves against the corpus by exact URL match")

	require.Len(t, failed, 1)
	assert.Equal(t, "https://example.test/missing", failed[0]["url"])
	assert.Equal(t, MessageExtractNotFound, failed[0]["error"])
}

// extractOverrideScenario forces one requested URL to fail via
// `extract.failed_results`, and declares a second override the tests below
// never request.
const extractOverrideScenario = `
version: 1
name: extract-override
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Report A text.
providers:
  tavily:
    response_time: 1.0
    extract:
      failed_results:
        - url: https://example.test/report-a
          error: Robots.txt disallows crawling this URL.
        - url: https://example.test/never-requested
          error: This override names a URL nobody asked for.
`

// TestExtractFailedResultsOverride proves a projection can force a resolvable
// URL to fail — overriding D-a's ordinary resolution outright — and that an
// override naming a URL the request never sent is dropped with a WARNING
// rather than silently rendered.
func TestExtractFailedResultsOverride(t *testing.T) {
	t.Parallel()

	ring := journal.NewRing(8, 1<<16)
	rec := post(t, newHandler(t, extractOverrideScenario, ring), "/extract",
		`{"urls":"https://example.test/report-a"}`, bearer)
	require.Equal(t, http.StatusOK, rec.Code)

	out := decodeExtract(t, rec)
	require.Empty(t, extractResults(t, out), "the override forces a failure even though the URL resolves")

	failed := extractFailed(t, out)
	require.Len(t, failed, 1)
	assert.Equal(t, "https://example.test/report-a", failed[0]["url"])
	assert.Equal(t, "Robots.txt disallows crawling this URL.", failed[0]["error"])

	codes := findingCodes(t, ring)
	require.Contains(t, codes, CodeExtractFailureUnrequested,
		"the second override names a URL this request never sent")

	body := rec.Body.String()
	require.NotContains(t, body, "never-requested", "an unrequested override must not reach the wire")
}

// extractFaultScenario declares /extract's own attempt budget under
// `extract.fault`, apart from /search's block-level `fault:`.
const extractFaultScenario = `
version: 1
name: extract-fault
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Report A text.
providers:
  tavily:
    response_time: 1.0
    extract:
      fault:
        attempts:
          - status: 429
            retry_after: 1
          - status: 200
`

// TestExtractFaultUsesItsOwnBudget proves FaultKeyExtract is a separate
// counter from FaultKeySearch: a rate limit scripted under `extract.fault`
// fires on /extract and leaves /search — which declares no plan of its own on
// this scenario — untouched.
func TestExtractFaultUsesItsOwnBudget(t *testing.T) {
	t.Parallel()

	loaded, report, err := scenario.Parse([]byte(extractFaultScenario))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)

	h := New(provider.Deps{Scenario: loaded, Faults: provider.MustSet(Profile()).Faults(loaded)})

	first := post(t, h, "/extract", `{"urls":"https://example.test/report-a"}`, bearer)
	require.Equal(t, http.StatusTooManyRequests, first.Code)
	require.Equal(t, "1", first.Header().Get("Retry-After"))

	searchOK := post(t, h, "/search", `{"query":"report a"}`, bearer)
	require.Equal(t, http.StatusOK, searchOK.Code, "/search draws on its own budget, untouched by extract.fault")

	second := post(t, h, "/extract", `{"urls":"https://example.test/report-a"}`, bearer)
	require.Equal(t, http.StatusOK, second.Code, "the plan's second attempt succeeds")
}

// TestExtractGatesImagesAndFaviconOnRequestOptions proves the per-result
// images and favicon keys are absent unless the request asks for them, and
// that images render as bare URL strings — NOT the {url, description}
// objects /search uses for the same field name.
func TestExtractGatesImagesAndFaviconOnRequestOptions(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: extract-gating
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Report A text.
    favicon: https://example.test/favicon.ico
providers:
  tavily:
    response_time: 1.0
    results:
      - source: source-a
        images:
          - url: https://example.test/report-a/figure-1.png
`
	h := newHandler(t, src, nil)

	base := decodeExtract(t, post(t, h, "/extract", `{"urls":"https://example.test/report-a"}`, bearer))
	baseResults := extractResults(t, base)
	require.Len(t, baseResults, 1)
	assert.NotContains(t, baseResults[0], "favicon")
	assert.NotContains(t, baseResults[0], "images")

	withOptions := decodeExtract(t, post(t, h, "/extract",
		`{"urls":"https://example.test/report-a","include_images":true,"include_favicon":true}`, bearer))
	withResults := extractResults(t, withOptions)
	require.Len(t, withResults, 1)
	assert.Equal(t, "https://example.test/favicon.ico", withResults[0]["favicon"])

	images, ok := withResults[0]["images"].([]any)
	require.True(t, ok, "images must be an array: %v", withResults[0]["images"])
	require.Len(t, images, 1)
	assert.Equal(t, "https://example.test/report-a/figure-1.png", images[0],
		"a bare URL string, not an object")
}

// TestExtractUsageEitherHolds proves D-f's "either holds" gating for
// /extract: the credit object is emitted when the request asks for it OR the
// scenario declares one, unlike /search's request-only gating.
func TestExtractUsageEitherHolds(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: extract-usage
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Report A text.
providers:
  tavily:
    response_time: 1.0
    usage:
      credits: 7
`
	h := newHandler(t, src, nil)

	// The scenario declares usage but the request does not ask for it: still
	// emitted, because the scenario's declaration is the other half of "either".
	withoutFlag := decodeExtract(t, post(t, h, "/extract", `{"urls":"https://example.test/report-a"}`, bearer))
	usage, ok := withoutFlag["usage"].(map[string]any)
	require.True(t, ok, "usage must be emitted when the scenario declares one: %v", withoutFlag)
	assert.EqualValues(t, 7, usage["credits"])

	// Neither the scenario nor the request name usage anywhere: absent.
	plain := newHandler(t, extractValidationScenario, nil)
	noUsage := decodeExtract(t, post(t, plain, "/extract", `{"urls":"https://example.test/report-a"}`, bearer))
	assert.NotContains(t, noUsage, "usage")

	// The request asks for it and the scenario declares none: derived from
	// extract_depth's documented credit cost.
	withFlag := decodeExtract(t, post(t, plain, "/extract",
		`{"urls":"https://example.test/report-a","include_usage":true}`, bearer))
	usage, ok = withFlag["usage"].(map[string]any)
	require.True(t, ok, "usage must be emitted when the request asks for it: %v", withFlag)
	assert.EqualValues(t, 1, usage["credits"], "basic extract_depth costs 1 credit")

	// The derived cost also tracks extract_depth: advanced is documented as
	// twice the basic rate.
	withAdvanced := decodeExtract(t, post(t, plain, "/extract",
		`{"urls":"https://example.test/report-a","include_usage":true,"extract_depth":"advanced"}`, bearer))
	usage, ok = withAdvanced["usage"].(map[string]any)
	require.True(t, ok, "usage must be emitted: %v", withAdvanced)
	assert.EqualValues(t, 2, usage["credits"], "advanced extract_depth costs 2 credits")
}

// TestExtractRouteAddressableTurn proves D-h's "route-addressable turns"
// design end to end for this provider: a turn's `when: {route: extract}`
// selects on the bare route name, and /search on the same "tavily" entry
// falls through to the unconditional turn instead — the two routes' turn
// cursors, drawn from FaultKeyExtract and FaultKeySearch, never interfere.
func TestExtractRouteAddressableTurn(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: extract-route-turn
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Report A text.
providers:
  tavily:
    turns:
      - when: {route: extract}
        respond:
          response_time: 9.9
      - respond:
          response_time: 1.0
          results:
            - source: source-a
`
	h := newHandler(t, src, nil)

	extractOut := decodeExtract(t, post(t, h, "/extract", `{"urls":"https://example.test/report-a"}`, bearer))
	assert.InDelta(t, 9.9, extractOut["response_time"], 1e-9,
		"the bare route name 'extract' must select the turn scoped to POST /extract")

	searchRec := post(t, h, "/search", `{"query":"report a"}`, bearer)
	require.Equal(t, http.StatusOK, searchRec.Code)
	assert.Contains(t, searchRec.Body.String(), `"response_time":1`,
		"/search must fall through to the unconditional turn, not the extract-scoped one")
}

// --- helpers ----------------------------------------------------------------

// decodeExtract reads a /extract response as a generic object.
func decodeExtract(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), "body: %s", rec.Body.String())
	return out
}

// extractResults reads out["results"] as a slice of objects, failing the test
// on a wrong shape rather than returning nil.
func extractResults(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	return asObjectSlice(t, out["results"])
}

// extractFailed reads out["failed_results"] as a slice of objects.
func extractFailed(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	return asObjectSlice(t, out["failed_results"])
}

func asObjectSlice(t *testing.T, v any) []map[string]any {
	t.Helper()

	raw, ok := v.([]any)
	require.True(t, ok, "%v is not an array", v)

	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		require.True(t, ok, "%v is not an object", item)
		out = append(out, obj)
	}
	return out
}
