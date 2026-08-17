package exa

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// findSimilarGoldenScenario backs the /findSimilar goldens, dedicated to
// producing them the same way contentsGoldenScenario is for /contents.
const findSimilarGoldenScenario = `
version: 1
name: exa-find-similar-golden
time:
  base: 2026-01-01T00:00:00Z
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    author: A. Author
    published_at: 2025-11-16T01:36:32.547Z
    text: Report A finds that deterministic simulators remove flakiness from adapter test suites.
    image: https://example.test/report-a/cover.png
    favicon: https://example.test/favicon.ico
  - id: source-b
    url: https://example.test/report-b
    title: Report B
    text: Report B corroborates Report A across a second dataset.
providers:
  exa:
    request_id: c6a858155d5e6a9dfd83b64c8a306d95
    find_similar:
      results:
        - source: source-a
        - source: source-b
      cost_dollars:
        total: 0.004
`

func findSimilarFaultedGoldenScenario(status int) string {
	return `
version: 1
name: exa-find-similar-fault-golden
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    find_similar:
      results:
        - source: source-a
      fault:
        after: repeat_last
        attempts:
          - status: ` + strconv.Itoa(status) + `
`
}

// TestGolden_FindSimilarHappy pins D-b: a relevance route rendered from its
// own find_similar.results, through the same result shape /search uses.
func TestGolden_FindSimilarHappy(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarGoldenScenario)
	rec := s.findSimilar(`{"url":"https://example.test/report-a"}`)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-find-similar-happy.json", rec.Body.Bytes())
}

// TestGolden_FindSimilarEmpty pins the zero-result success: results stays an
// empty array and costDollars stays present, following /search's own empty
// golden.
func TestGolden_FindSimilarEmpty(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-find-similar-empty-golden
providers:
  exa:
    request_id: 2f1e3d4c5b6a7988776655443322110f
    find_similar:
      results: []
      cost_dollars:
        total: 0
`)
	rec := s.findSimilar(`{"url":"https://example.test/nothing-similar"}`)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-find-similar-empty.json", rec.Body.Bytes())
}

func TestGolden_FindSimilarBadRequest(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarGoldenScenario)
	rec := s.findSimilar(`{}`)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-find-similar-400.json", rec.Body.Bytes())
}

func TestGolden_FindSimilarUnauthorized(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarGoldenScenario)
	rec := s.do(request{path: "/findSimilar", body: `{"url":"https://example.test/report-a"}`, noAuth: true})

	require.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-find-similar-401.json", rec.Body.Bytes())
}

func TestGolden_FindSimilarRateLimitUsesTheReducedShape(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarFaultedGoldenScenario(http.StatusTooManyRequests))
	rec := s.findSimilar(`{"url":"https://example.test/report-a"}`)

	require.Equal(t, http.StatusTooManyRequests, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-find-similar-429.json", rec.Body.Bytes())
}
