package exa

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// contentsGoldenScenario backs the /contents goldens. It is dedicated to
// producing them rather than shared with a behavioural test, following the
// agentRunGoldenScenario precedent in agentrun_test.go: a golden's exact bytes
// must stay pinned to a scenario that is free to be dedicated to them, not one
// a behavioural test is free to change the shape of.
const contentsGoldenScenario = `
version: 1
name: exa-contents-golden
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
    request_id: b5947044c4b78efa9552a7c89b306d95
    results:
      - source: source-a
      - source: source-b
    contents:
      cost_dollars:
        total: 0.003
`

// contentsFaultedGoldenScenario backs the 429 golden, following
// render_test.go's faultedScenario precedent: a single always-on attempt is
// how the status-only error goldens are reproduced.
func contentsFaultedGoldenScenario(status int) string {
	return `
version: 1
name: exa-contents-fault-golden
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
    contents:
      fault:
        after: repeat_last
        attempts:
          - status: ` + strconv.Itoa(status) + `
`
}

// TestGolden_ContentsHappy pins the D-a happy path: two requested URLs, both
// resolved against the projection's own /search-shaped results, statuses[]
// carrying "success" with no source key (both vendor examples omit it), and
// costDollars.total read straight from contents.cost_dollars.
func TestGolden_ContentsHappy(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsGoldenScenario)
	rec := s.contents(`{"urls":["https://example.test/report-a","https://example.test/report-b"]}`)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-contents-happy.json", rec.Body.Bytes())
}

// TestGolden_ContentsEmpty pins D-g's NO_CONTENT_FOUND branch: every requested
// identifier failed to resolve, so the response is the documented 400 rather
// than a 200 with an empty results[].
func TestGolden_ContentsEmpty(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsGoldenScenario)
	rec := s.contents(`{"urls":["https://example.test/missing"]}`)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-contents-empty.json", rec.Body.Bytes())
}

// TestGolden_ContentsPerURLFailure pins the mechanism the contract calls out
// explicitly: a per-URL crawl failure is distinct from a top-level HTTP error,
// and the response stays 200 with results[] populated for what DID resolve.
func TestGolden_ContentsPerURLFailure(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsGoldenScenario)
	rec := s.contents(`{"urls":["https://example.test/report-a","https://example.test/missing"]}`)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-contents-per-url-failure.json", rec.Body.Bytes())
}

// TestGolden_ContentsBadRequest pins D-d's one required-field failure: neither
// ids nor urls sent.
func TestGolden_ContentsBadRequest(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsGoldenScenario)
	rec := s.contents(`{}`)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-contents-400.json", rec.Body.Bytes())
}

// TestGolden_ContentsInvalidURLs pins the INVALID_URLS tag: a malformed
// element (an empty string) in urls[] gets the vendor's fixed error text on
// the wire, per classifyCode's codeContentsItemInvalid branch.
func TestGolden_ContentsInvalidURLs(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsGoldenScenario)
	rec := s.contents(`{"urls":[""]}`)

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-contents-400-invalid-urls.json", rec.Body.Bytes())
}

func TestGolden_ContentsUnauthorized(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsGoldenScenario)
	rec := s.do(request{path: "/contents", body: `{"urls":["https://example.test/report-a"]}`, noAuth: true})

	require.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-contents-401.json", rec.Body.Bytes())
}

// TestGolden_ContentsRateLimitUsesTheReducedShape proves /contents shares
// /search's reduced 429 envelope: {error} only, no requestId and no tag.
func TestGolden_ContentsRateLimitUsesTheReducedShape(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsFaultedGoldenScenario(http.StatusTooManyRequests))
	rec := s.contents(`{"urls":["https://example.test/report-a"]}`)

	require.Equal(t, http.StatusTooManyRequests, rec.Code, "body: %s", rec.Body.String())
	assertGoldenWire(t, "exa-contents-429.json", rec.Body.Bytes())
}
