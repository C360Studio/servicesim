package tavily

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// researchGoldenScenario backs the create, pending-poll and completed-poll
// goldens: one pending turn at call_index 0, then an unconditional terminal
// turn. It is dedicated to producing these exact bytes rather than shared with
// researchScenario in research_test.go, which is free to change shape for
// behavioural coverage.
const researchGoldenScenario = `
version: 1
name: tavily-research-golden
time:
  base: 2026-01-01T00:00:00Z
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    favicon: https://example.test/favicon.ico
providers:
  tavily_research:
    turns:
      - when: {call_index: 0}
        respond: {status: pending}
      - respond:
          status: completed
          content: The research found that deterministic simulation removes flakiness.
          sources: [source-a]
          response_time: 12.5
`

// researchGoldenFailedScenario backs the failed-poll golden: a single
// unconditional turn, already terminal on the first poll.
const researchGoldenFailedScenario = `
version: 1
name: tavily-research-failed-golden
time:
  base: 2026-01-01T00:00:00Z
providers:
  tavily_research:
    turns:
      - respond:
          status: failed
          response_time: 3.2
`

// researchGolden404Scenario backs the unknown-request_id golden. It declares
// no tavily_research entry at all: an unminted identifier 404s the same way
// regardless of what the entry would have scripted.
const researchGolden404Scenario = `
version: 1
name: tavily-research-404-golden
`

// TestGolden_ResearchCreated pins POST /research's 201 body: all six documented
// fields, including input and model echoed back.
func TestGolden_ResearchCreated(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchGoldenScenario, nil)
	rec := post(t, h, "/research", `{"input":"research this"}`, bearer)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, goldenBytes(t, "tavily-research-created.json"), rec.Body.String())
}

// TestGolden_ResearchPending pins a non-terminal poll: exactly request_id,
// status and response_time, at 202, per contracts/tavily/README.md's
// poll-status-code table.
func TestGolden_ResearchPending(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchGoldenScenario, nil)
	id := researchCreate(t, h, `{"input":"research this"}`)

	rec, _ := researchPoll(t, h, id)
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Equal(t, goldenBytes(t, "tavily-research-pending.json"), rec.Body.String())
}

// TestGolden_ResearchCompleted pins a terminal success at 200: created_at,
// content and sources join the always-present three fields.
func TestGolden_ResearchCompleted(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchGoldenScenario, nil)
	id := researchCreate(t, h, `{"input":"research this"}`)
	researchPoll(t, h, id) // consume the pending poll

	rec, _ := researchPoll(t, h, id)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, goldenBytes(t, "tavily-research-completed.json"), rec.Body.String())
}

// TestGolden_ResearchFailed pins a terminal failure at 200: exactly
// request_id, status and response_time, with no created_at, content or
// sources — verified 2026-08-15 against
// <https://docs.tavily.com/documentation/api-reference/endpoint/research-get>. // servicesim:allow-live-host
// See contracts/tavily/provenance.yaml's tavily-research-failed.json entry.
func TestGolden_ResearchFailed(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchGoldenFailedScenario, nil)
	id := researchCreate(t, h, `{"input":"research this"}`)

	rec, _ := researchPoll(t, h, id)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, goldenBytes(t, "tavily-research-failed.json"), rec.Body.String())
}

// TestGolden_ResearchNotFound pins the 404 for a request_id this process never
// minted: byte-identical to tavily-404.json, because the research surface
// documents no task-specific 404 body of its own.
func TestGolden_ResearchNotFound(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchGolden404Scenario, nil)
	rec := getResearch(t, h, "/research/00000000-0000-0000-0000-000000000000")

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, goldenBytes(t, "tavily-research-404.json"), rec.Body.String())
}

// TestGolden_ResearchCreateAtTheJobBound pins the 503 a create gets when its
// namespace already holds the configured maximum of live jobs.
func TestGolden_ResearchCreateAtTheJobBound(t *testing.T) {
	t.Parallel()

	loaded, report, err := scenario.Parse([]byte(researchGoldenScenario))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)

	h := New(provider.Deps{Scenario: loaded, Jobs: jobs.NewRegistry(jobs.Limits{MaxJobs: 1})})

	first := post(t, h, "/research", `{"input":"first"}`, bearer)
	require.Equal(t, http.StatusCreated, first.Code, "the first create must succeed: %s", first.Body.String())

	rec := post(t, h, "/research", `{"input":"second"}`, bearer)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, goldenBytes(t, "tavily-research-503.json"), rec.Body.String())
}
