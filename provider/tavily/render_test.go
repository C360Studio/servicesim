package tavily

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/httpx"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// TestSearchBodyMatchesSearchResponse pins the marshalling twin to the exported
// type. They must carry the same JSON tags in the same order, or a consumer
// decoding SearchResponse would be decoding a shape the renderer never emits.
func TestSearchBodyMatchesSearchResponse(t *testing.T) {
	t.Parallel()

	exported := reflect.TypeOf(SearchResponse{})
	internal := reflect.TypeOf(searchBody{})
	require.Equal(t, exported.NumField(), internal.NumField())

	for i := range exported.NumField() {
		require.Equal(t, exported.Field(i).Name, internal.Field(i).Name)
		require.Equal(t, exported.Field(i).Tag.Get("json"), internal.Field(i).Tag.Get("json"))
	}
}

// TestResponseTimeIsAJSONNumber is the single most likely bug on this surface
// stated as a test.
//
// The plan document encodes response_time as the string "1.15" because the
// specification's example renders it as a quoted YAML scalar; the declared JSON
// type is number. Both forms round-trip cleanly through a permissive decoder,
// so this asserts on the raw bytes: a struct-level assertion cannot tell them
// apart, and the bug would survive it.
func TestResponseTimeIsAJSONNumber(t *testing.T) {
	t.Parallel()

	body := render(t, `
version: 1
name: numbers
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily:
    response_time: 1.15
    results:
      - source: source-a
        score: 0.93
`, `{"query":"a"}`)

	require.Contains(t, string(body), `"response_time":1.15`)
	require.NotContains(t, string(body), `"response_time":"`)

	// The same trap applies to a result's score, which is also a number.
	require.Contains(t, string(body), `"score":0.93`)
	require.NotContains(t, string(body), `"score":"`)

	// And to the identifiers, which are strings and must stay quoted.
	require.Regexp(t, regexp.MustCompile(`"request_id":"[0-9a-f-]{36}"`), string(body))
	require.Regexp(t, regexp.MustCompile(`"id":"[0-9a-f]{6}-\d{2}"`), string(body))
}

// TestImagesAreObjectsNotBareStrings pins deviation 18: the plan document shows
// images as an empty array, which pins nothing, and a consumer that assumes
// bare URL strings breaks on first use.
func TestImagesAreObjectsNotBareStrings(t *testing.T) {
	t.Parallel()

	body := render(t, `
version: 1
name: images
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily:
    images:
      - url: https://example.test/cover.png
        description: A cover image
    results:
      - source: source-a
        images:
          - url: https://example.test/figure-1.png
            description: Figure 1
`, `{"query":"a","include_images":true,"include_image_descriptions":true}`)

	require.Contains(t, string(body),
		`"images":[{"url":"https://example.test/cover.png","description":"A cover image"}]`)
	require.Contains(t, string(body),
		`"images":[{"url":"https://example.test/figure-1.png","description":"Figure 1"}]`)
	require.NotContains(t, string(body), `"images":["https`)
}

// TestRequestGating covers the fields whose presence is a property of the
// request rather than of the scenario. Getting this wrong is how a consumer
// comes to believe it exercised include_raw_content when it never sent it.
func TestRequestGating(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: gating
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    favicon: https://example.test/favicon.ico
    published_at: 2026-05-20T00:00:00Z
    text: The full text of Report A.
    snippets:
      - An excerpt from Report A.
providers:
  tavily:
    answer: A synthesis of Report A.
    response_time: 1.15
    images:
      - url: https://example.test/cover.png
    usage:
      credits: 4
    results:
      - source: source-a
        score: 0.98
`

	tests := []struct {
		name        string
		request     string
		wantPresent []string
		wantAbsent  []string
	}{
		{
			name:    "a bare request gets the required keys and nothing more",
			request: `{"query":"a"}`,
			wantPresent: []string{
				`"answer":null`, `"images":[]`, `"raw_content":null`,
			},
			wantAbsent: []string{`"favicon"`, `"published_date"`, `"usage"`, `"auto_parameters"`},
		},
		{
			name:        "include_answer produces the answer",
			request:     `{"query":"a","include_answer":"basic"}`,
			wantPresent: []string{`"answer":"A synthesis of Report A."`},
		},
		{
			name:        "include_raw_content falls back to the source text",
			request:     `{"query":"a","include_raw_content":"markdown"}`,
			wantPresent: []string{`"raw_content":"The full text of Report A."`},
		},
		{
			name:        "include_favicon produces the source's favicon",
			request:     `{"query":"a","include_favicon":true}`,
			wantPresent: []string{`"favicon":"https://example.test/favicon.ico"`},
		},
		{
			name:        "topic news produces published_date",
			request:     `{"query":"a","topic":"news"}`,
			wantPresent: []string{`"published_date":"2026-05-20T00:00:00.000Z"`},
		},
		{
			name:        "include_usage produces the scenario's credits",
			request:     `{"query":"a","include_usage":true}`,
			wantPresent: []string{`"usage":{"credits":4}`},
		},
		{
			name:        "auto_parameters echoes the effective parameters",
			request:     `{"query":"a","auto_parameters":true,"search_depth":"advanced","topic":"news"}`,
			wantPresent: []string{`"auto_parameters":{"search_depth":"advanced","topic":"news"}`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body := string(render(t, src, tc.request))
			for _, want := range tc.wantPresent {
				require.Contains(t, body, want)
			}
			for _, absent := range tc.wantAbsent {
				require.NotContains(t, body, absent)
			}
		})
	}
}

// TestDerivedUsageFollowsTheSearchDepth covers the case the scenario leaves
// open: a request that asks for usage against a projection that declares none.
func TestDerivedUsageFollowsTheSearchDepth(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: derived-usage
providers:
  tavily:
    response_time: 1.15
`
	basic := string(render(t, src, `{"query":"a","include_usage":true}`))
	require.Contains(t, basic, `"usage":{"credits":1}`)

	advanced := string(render(t, src, `{"query":"a","include_usage":true,"search_depth":"advanced"}`))
	require.Contains(t, advanced, `"usage":{"credits":2}`)
}

// TestDerivedValuesAreStableAndWellShaped covers the fields a scenario may
// leave to the simulator. They must be reproducible and they must look like the
// vendor's own examples, because a consumer's regex or length check is trained
// on those.
func TestDerivedValuesAreStableAndWellShaped(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: derived
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
  - id: source-b
    url: https://example.test/report-b
    title: Report B
providers:
  tavily:
    results:
      - source: source-a
      - source: source-b
`
	first := render(t, src, `{"query":"a"}`)
	require.Equal(t, string(first), string(render(t, src, `{"query":"a"}`)))

	var decoded SearchResponse
	require.NoError(t, json.Unmarshal(first, &decoded))

	require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, decoded.RequestID)
	require.InDelta(t, 1.375, decoded.ResponseTime, 1.125)

	require.Len(t, decoded.Results, 2)
	for i, result := range decoded.Results {
		require.Regexp(t, `^[0-9a-f]{6}-\d{2}$`, result.ID)
		require.GreaterOrEqual(t, result.Score, scoreFloor)
		require.Less(t, result.Score, scoreCeiling)

		// The suffix is the 1-based position within this response.
		require.Equal(t, [2]string{"-01", "-02"}[i], result.ID[len(result.ID)-3:])
	}
	require.NotEqual(t, decoded.Results[0].ID[:6], decoded.Results[1].ID[:6],
		"two different sources must not derive the same id prefix")
}

// TestMaxResultsTruncatesInDeclarationOrder proves the documented default is
// applied and that truncation never sorts.
func TestMaxResultsTruncatesInDeclarationOrder(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: truncation
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
  - id: source-b
    url: https://example.test/report-b
    title: Report B
  - id: source-c
    url: https://example.test/report-c
    title: Report C
providers:
  tavily:
    results:
      - source: source-c
        score: 0.10
      - source: source-a
        score: 0.99
      - source: source-b
        score: 0.50
`
	var decoded SearchResponse
	require.NoError(t, json.Unmarshal(render(t, src, `{"query":"a","max_results":2}`), &decoded))
	require.Len(t, decoded.Results, 2)
	require.Equal(t, "Report C", decoded.Results[0].Title, "results are never sorted by score")
	require.Equal(t, "Report A", decoded.Results[1].Title)

	var zero SearchResponse
	require.NoError(t, json.Unmarshal(render(t, src, `{"query":"a","max_results":0}`), &zero))
	require.Empty(t, zero.Results)
	require.NotNil(t, zero.Results)
}

// TestExtraAndOmitFields covers the two escape hatches a scenario has for
// exercising a consumer's tolerance: additive vendor fields, and a required
// field that has gone missing.
func TestExtraAndOmitFields(t *testing.T) {
	t.Parallel()

	body := render(t, `
version: 1
name: extras
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily:
    extra_fields:
      request_region: eu-west
    response_time: 1.15
    results:
      - source: source-a
        score: 0.98
        omit_fields: [content]
        extra_fields:
          chunk_count: 3
`, `{"query":"a"}`)

	decoded := map[string]any{}
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Equal(t, "eu-west", decoded["request_region"])

	results, ok := decoded["results"].([]any)
	require.True(t, ok)
	require.Len(t, results, 1)

	result, ok := results[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(3), result["chunk_count"])
	require.NotContains(t, result, "content")
	require.Contains(t, result, "title")
}

// TestScalarShorthandResult covers the YAML form the plan's own examples use: a
// bare source id as a whole result entry.
func TestScalarShorthandResult(t *testing.T) {
	t.Parallel()

	var decoded SearchResponse
	require.NoError(t, json.Unmarshal(render(t, `
version: 1
name: shorthand
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    snippets:
      - An excerpt from Report A.
providers:
  tavily:
    results:
      - source-a
`, `{"query":"a"}`), &decoded))

	require.Len(t, decoded.Results, 1)
	require.Equal(t, "Report A", decoded.Results[0].Title)
	require.Equal(t, "https://example.test/report-a", decoded.Results[0].URL)
	require.Equal(t, "An excerpt from Report A.", decoded.Results[0].Content)
}

// TestUnresolvedReferenceDoesNotPanic covers the render path's defence against
// a scenario that reached it unvalidated: an unresolved reference renders an
// empty result, never a nil dereference on a request path.
func TestUnresolvedReferenceDoesNotPanic(t *testing.T) {
	t.Parallel()

	projection := &Projection{Results: []ResultProjection{{SourceRef: scenario.SourceRef{Ref: "source-z"}}}}
	body, err := renderSearch(projection, &searchRequest{Query: "a", MaxResults: DefaultMaxResults},
		renderKeys{
			Seed: "seed",
			ID:   []string{"seed", Name, FaultKeySearch},
			Call: []string{"seed", Name, FaultKeySearch, "0"},
		})
	require.NoError(t, err)

	var decoded SearchResponse
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Len(t, decoded.Results, 1)
	require.Empty(t, decoded.Results[0].Title)
	require.NotEmpty(t, decoded.Results[0].ID)
}

// derivedRequestIDScenario has no request_id override and no fault plan, so
// request_id is entirely derived — which is what the tests below are about.
const derivedRequestIDScenario = `
version: 1
name: tavily-derived-request-id
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily:
    results:
      - source: source-a
`

// tavilyUUIDv5 is the shape Tavily documents for request_id: a UUID, not a
// readable slug. A consumer regex trained on a slug breaks against the real API.
var tavilyUUIDv5 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestRequestIDIsDistinctPerCall pins the property a real vendor has and a
// collapsed identifier destroys: a consumer correlating a log line to a request
// must be able to tell one call from the next.
//
// The route declares no fault plan on purpose. The call index used to enter the
// identifier tuple only where one did, so every response an unfaulted scenario
// ever served carried a single request_id.
func TestRequestIDIsDistinctPerCall(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, derivedRequestIDScenario, nil)
	body := `{"query":"report a"}`

	first := decodeRequestID(t, post(t, handler, "/search", body, bearer))
	second := decodeRequestID(t, post(t, handler, "/search", body, bearer))

	require.Regexp(t, tavilyUUIDv5, first, "request_id keeps Tavily's documented UUID shape")
	require.Regexp(t, tavilyUUIDv5, second)
	require.NotEqual(t, first, second, "two successive calls must not share a request_id")

	// A namespace is a fresh state lane, so this is call 0 again and must
	// reproduce the first identifier exactly. Distinctness drawn from a clock or
	// from a counter of this package's own would fail here.
	fresh := decodeRequestID(t, post(t, handler, "/n/lane-b/search", body, bearer))
	require.Equal(t, first, fresh,
		"the same call position in a fresh lane must reproduce the same request_id")
}

// decodeRequestID reads request_id out of a rendered search response.
func decodeRequestID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code)
	var decoded SearchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	return decoded.RequestID
}

// --- helpers ----------------------------------------------------------------

// render decodes a scenario's Tavily projection the way the handler does and
// renders it against a request body, returning the wire bytes.
func render(t *testing.T, src, requestBody string) []byte {
	t.Helper()

	loaded, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)

	entry := loaded.Provider(Name)
	require.NotNil(t, entry)
	require.NotEmpty(t, entry.Turns)

	projection := &Projection{}
	require.NoError(t, entry.Turns[0].DecodeProjection(entry.Name, 0, projection))
	require.Empty(t, loaded.ResolveRefs("providers.tavily.turns[0].respond", projection))

	body, err := renderSearch(projection, requestFrom(t, requestBody), renderKeys{
		Seed: loaded.SeedKey(),
		ID:   []string{loaded.SeedKey(), Name, FaultKeySearch},
		Call: []string{loaded.SeedKey(), Name, FaultKeySearch, "0"},
	})
	require.NoError(t, err)
	return body
}

// requestFrom parses a JSON body through the handler's own parser rather than
// through a second copy of the defaulting rules, so a render test cannot
// exercise a request shape validation would have rejected.
func requestFrom(t *testing.T, body string) *searchRequest {
	t.Helper()

	decoded, err := httpx.DecodeObject([]byte(body))
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	exchange := &provider.Exchange{Request: request, Raw: []byte(body), Body: decoded}
	parsed := parseSearchRequest(exchange)
	require.False(t, exchange.Failed(), "a render test must use a request validation accepts")
	return parsed
}
