package tavily

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// extractGoldenScenario backs the happy /extract golden. It exercises both
// legs of D-a's resolution order in one request: report-a is named by this
// turn's own `results:` (and overrides its raw_content and images), while
// report-b is corpus-only and resolves purely by exact URL match, picking up
// its corpus Text and single Image field.
const extractGoldenScenario = `
version: 1
name: tavily-extract-golden
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    favicon: https://example.test/favicon.ico
    snippets:
      - Report A finds that deterministic simulators remove flakiness from adapter test suites.
  - id: source-b
    url: https://example.test/report-b
    title: Report B
    image: https://example.test/report-b/cover.png
    snippets:
      - Report B corroborates Report A across a second dataset.
providers:
  tavily:
    request_id: 123e4567-e89b-12d3-a456-426614174111
    response_time: 1.42
    results:
      - source: source-a
        raw_content: >-
          Report A finds that deterministic simulators remove flakiness from
          adapter test suites. Full text follows.
        images:
          - url: https://example.test/report-a/figure-1.png
`

// extractGoldenEmptyScenario backs the all-failed /extract golden: a
// scenario with no sources and no results, so every requested URL is
// unresolvable.
const extractGoldenEmptyScenario = `
version: 1
name: tavily-extract-empty-golden
providers:
  tavily:
    request_id: 4b1f9a3c-2d5e-5a7b-9c0d-1e2f3a4b5c6d
    response_time: 0.5
`

// extractGoldenPartialFailureScenario backs the per-URL-failure golden: one
// resolvable URL and one URL a projection override forces to fail with its
// own error text, regardless of the ordinary D-a lookup.
const extractGoldenPartialFailureScenario = `
version: 1
name: tavily-extract-partial-failure-golden
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Report A full text.
providers:
  tavily:
    request_id: 5c2a0b4d-3e6f-5b8c-ad1e-2f3a4b5c6d7e
    response_time: 0.87
    results:
      - source: source-a
    extract:
      failed_results:
        - url: https://example.test/blocked
          error: Robots.txt disallows crawling this URL.
`

// TestGolden_ExtractHappy pins the 200 body for two resolvable URLs — one
// resolved through this turn's own /search-shaped results, one resolved
// purely against the corpus — with include_images, include_favicon and
// include_usage all set.
func TestGolden_ExtractHappy(t *testing.T) {
	t.Parallel()

	rec := post(t, newHandler(t, extractGoldenScenario, nil), "/extract", `{
		"urls": ["https://example.test/report-a", "https://example.test/report-b"],
		"include_images": true,
		"include_favicon": true,
		"include_usage": true
	}`, bearer)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, goldenBytes(t, "tavily-extract-happy.json"), rec.Body.String())
}

// TestGolden_ExtractEmpty pins the 200 body for a request whose every URL is
// unresolvable: results is an empty array (not null, not omitted) and
// failed_results carries one entry per requested URL, in request order.
func TestGolden_ExtractEmpty(t *testing.T) {
	t.Parallel()

	rec := post(t, newHandler(t, extractGoldenEmptyScenario, nil), "/extract", `{
		"urls": ["https://example.test/missing-a", "https://example.test/missing-b"]
	}`, bearer)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, goldenBytes(t, "tavily-extract-empty.json"), rec.Body.String())
}

// TestGolden_ExtractPartialFailure pins a request with one successful result
// and one projection-forced failure, proving extract.failed_results can force
// a resolvable URL to fail with a scenario-chosen error string.
func TestGolden_ExtractPartialFailure(t *testing.T) {
	t.Parallel()

	rec := post(t, newHandler(t, extractGoldenPartialFailureScenario, nil), "/extract", `{
		"urls": ["https://example.test/report-a", "https://example.test/blocked"]
	}`, bearer)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, goldenBytes(t, "tavily-extract-partial-failure.json"), rec.Body.String())
}

// TestGolden_ExtractUnauthorized pins /extract's 401, byte-identical to
// tavily-401.json: it is the same documented envelope and message, on a route
// whose accepted placement set happens to be narrower.
func TestGolden_ExtractUnauthorized(t *testing.T) {
	t.Parallel()

	rec := post(t, newHandler(t, extractGoldenScenario, nil), "/extract",
		`{"urls":"https://example.test/report-a"}`, "")

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, goldenBytes(t, "tavily-extract-401.json"), rec.Body.String())
}

// TestGolden_ExtractBadRequest pins /extract's 400 for a missing urls field.
func TestGolden_ExtractBadRequest(t *testing.T) {
	t.Parallel()

	rec := post(t, newHandler(t, extractGoldenScenario, nil), "/extract", `{}`, bearer)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, goldenBytes(t, "tavily-extract-400.json"), rec.Body.String())
}

// TestGolden_ExtractRateLimited drives a declared extract.fault plan through
// the real fault engine, pinned to the shared 429 envelope tavily-429.json
// already carries: the body shape does not vary by route, only the attempt
// budget it is drawn from does.
func TestGolden_ExtractRateLimited(t *testing.T) {
	t.Parallel()

	loaded, report, err := scenario.Parse([]byte(extractFaultScenario))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)

	h := New(provider.Deps{Scenario: loaded, Faults: provider.MustSet(Profile()).Faults(loaded)})
	rec := post(t, h, "/extract", `{"urls":"https://example.test/report-a"}`, bearer)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "1", rec.Header().Get("Retry-After"))
	require.Equal(t, goldenBytes(t, "tavily-429.json"), rec.Body.String())
}
