package tavily

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// researchScenario walks pending -> in_progress -> completed, which is the
// lifecycle a consumer's poll loop is written against.
const researchScenario = `
version: 1
name: tavily-research
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
      - when: {call_index: 1}
        respond: {status: in_progress}
      - respond:
          status: completed
          content: The research found that deterministic simulation removes flakiness.
          sources: [source-a]
          response_time: 12.5
`

func researchCreate(t *testing.T, h http.Handler, body string) string {
	t.Helper()
	rec := post(t, h, "/research", body, bearer)
	require.Equal(t, http.StatusCreated, rec.Code, "create failed: %s", rec.Body.String())

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	id, _ := out["request_id"].(string)
	require.NotEmpty(t, id)
	assert.Equal(t, StatusPending, out["status"])
	// The create echoes input and model back, which /search does not do.
	assert.Equal(t, "research this", out["input"])
	assert.NotEmpty(t, out["model"], "model is documented as required in the create response")
	return id
}

// getResearch issues a bare GET against path, for a poll whose response is not
// necessarily a research snapshot — an unknown identifier's 404 in particular.
func getResearch(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", bearer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func researchPoll(t *testing.T, h http.Handler, id string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/research/"+id, nil)
	req.Header.Set("Authorization", bearer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), "body: %s", rec.Body.String())
	return rec, out
}

// TestResearchPollStatusCodeTracksTheTaskState is the assertion this surface is
// most likely to lose. The poll's HTTP STATUS is part of the contract: 202 while
// the task is running, 200 once it is terminal. Answering 200 throughout would
// leave a consumer's "202 means keep polling" branch entirely untested.
func TestResearchPollStatusCodeTracksTheTaskState(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchScenario, nil)
	id := researchCreate(t, h, `{"input":"research this"}`)

	rec, out := researchPoll(t, h, id)
	assert.Equal(t, http.StatusAccepted, rec.Code, "a pending task answers 202")
	assert.Equal(t, StatusPending, out["status"])

	rec, out = researchPoll(t, h, id)
	assert.Equal(t, http.StatusAccepted, rec.Code, "an in-progress task answers 202")
	assert.Equal(t, StatusInProgress, out["status"])

	rec, out = researchPoll(t, h, id)
	assert.Equal(t, http.StatusOK, rec.Code, "a completed task answers 200")
	assert.Equal(t, StatusCompleted, out["status"])
}

// A non-terminal poll carries only the three always-present fields. The
// completed-only fields appear at a terminal status, which pairs with the status
// code above.
func TestResearchPendingBodyOmitsTheCompletedFields(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchScenario, nil)
	id := researchCreate(t, h, `{"input":"research this"}`)

	_, pending := researchPoll(t, h, id)
	assert.Contains(t, pending, "request_id")
	assert.Contains(t, pending, "status")
	assert.Contains(t, pending, "response_time")
	assert.NotContains(t, pending, "content", "a running task has produced no content")
	assert.NotContains(t, pending, "sources")

	researchPoll(t, h, id) // in_progress
	_, done := researchPoll(t, h, id)

	assert.Equal(t, "The research found that deterministic simulation removes flakiness.", done["content"])
	assert.InDelta(t, 12.5, done["response_time"], 1e-9)

	sources, ok := done["sources"].([]any)
	require.True(t, ok, "a completed task carries sources: %v", done)
	require.Len(t, sources, 1)

	first, _ := sources[0].(map[string]any)
	assert.Equal(t, "Report A", first["title"])
	assert.Equal(t, "https://example.test/report-a", first["url"])
	assert.Equal(t, "https://example.test/favicon.ico", first["favicon"])
}

// content is string OR object, so a scenario must be able to produce both: a
// consumer's decoder has to handle either and cannot be tested against one.
func TestResearchContentCanBeAnObject(t *testing.T) {
	t.Parallel()

	h := newHandler(t, `
version: 1
name: tavily-research-structured
providers:
  tavily_research:
    turns:
      - respond:
          status: completed
          content:
            finding: deterministic simulation removes flakiness
            confidence: high
`, nil)
	id := researchCreate(t, h, `{"input":"research this"}`)

	rec, out := researchPoll(t, h, id)
	require.Equal(t, http.StatusOK, rec.Code)

	content, ok := out["content"].(map[string]any)
	require.True(t, ok, "content must survive as an object: %v", out["content"])
	assert.Equal(t, "deterministic simulation removes flakiness", content["finding"])
}

// Two tasks polled in one namespace keep separate cursors.
func TestResearchPollCursorsArePerTask(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchScenario, nil)
	first := researchCreate(t, h, `{"input":"research this"}`)
	second := researchCreate(t, h, `{"input":"research this"}`)
	require.NotEqual(t, first, second)

	for range 2 {
		researchPoll(t, h, first)
	}
	rec, out := researchPoll(t, h, first)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, StatusCompleted, out["status"])

	// The second task starts at its own poll 0.
	rec, out = researchPoll(t, h, second)
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, StatusPending, out["status"], "the second task drew from the first task's cursor")
}

// A create refused at the job bound is a Servicesim configuration wall, not a
// malformed client request: it must answer the provider-shaped 503, not the
// 400 a validation failure would, and it must name the remedy.
func TestResearchCreateAtTheJobBound(t *testing.T) {
	t.Parallel()

	loaded, report, err := scenario.Parse([]byte(researchScenario))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)
	findings := provider.ValidateScenario(loaded, map[string]provider.Validator{
		Name:         Validator{},
		NameResearch: ResearchValidator{},
	})
	require.Empty(t, findings, "the fixture must validate before it is served")

	h := New(provider.Deps{Scenario: loaded, Jobs: jobs.NewRegistry(jobs.Limits{MaxJobs: 1})})

	rec := post(t, h, "/research", `{"input":"first"}`, bearer)
	require.Equal(t, http.StatusCreated, rec.Code, "the first create must succeed: %s", rec.Body.String())

	rec = post(t, h, "/research", `{"input":"second"}`, bearer)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"a create past the bound is a Servicesim configuration wall, not a malformed client request")
	assert.Contains(t, rec.Body.String(), "holds its maximum of 1 jobs",
		"the refusal should name the configured bound")
}

// The required field is `input`, not `query`. Accepting /search's field name
// here would accept traffic the live API rejects, which is the failure a strict
// simulator exists to prevent.
func TestResearchCreateRequiresInputNotQuery(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchScenario, nil)

	rec := post(t, h, "/research", `{"query":"research this"}`, bearer)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"query is /search's field name and must not satisfy /research")

	rec = post(t, h, "/research", `{"input":"research this"}`, bearer)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

// The credential placement sets differ per route, and the difference is the
// request shape: a POST has a body to carry an api_key, a GET does not.
func TestResearchCredentialPlacementsDifferPerRoute(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchScenario, nil)

	// The body placement authenticates a create, as it does on /search.
	rec := post(t, h, "/research", `{"input":"research this","api_key":"tvly-body-key"}`, "")
	require.Equal(t, http.StatusCreated, rec.Code, "a body api_key must authenticate a create: %s", rec.Body.String())

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	id, _ := out["request_id"].(string)

	// The poll accepts the header. It has no body, so there is no second
	// placement to accept.
	req := httptest.NewRequest(http.MethodGet, "/research/"+id, nil)
	req.Header.Set("Authorization", bearer)
	poll := httptest.NewRecorder()
	h.ServeHTTP(poll, req)
	assert.Equal(t, http.StatusAccepted, poll.Code)

	// An unauthenticated poll is refused.
	req = httptest.NewRequest(http.MethodGet, "/research/"+id, nil)
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, req)
	assert.Equal(t, http.StatusUnauthorized, unauth.Code)
}

// An identifier this process never minted is a 404 and must not consume a poll
// from any task's script.
func TestResearchPollUnknownIdentifier(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchScenario, nil)
	id := researchCreate(t, h, `{"input":"research this"}`)

	req := httptest.NewRequest(http.MethodGet, "/research/00000000-0000-0000-0000-000000000000", nil)
	req.Header.Set("Authorization", bearer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// The real task is untouched.
	_, out := researchPoll(t, h, id)
	assert.Equal(t, StatusPending, out["status"])
}

// HEAD answers existence and consumes nothing.
func TestResearchHeadDoesNotConsumeAPoll(t *testing.T) {
	t.Parallel()

	h := newHandler(t, researchScenario, nil)
	id := researchCreate(t, h, `{"input":"research this"}`)

	for range 3 {
		req := httptest.NewRequest(http.MethodHead, "/research/"+id, nil)
		req.Header.Set("Authorization", bearer)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	_, out := researchPoll(t, h, id)
	assert.Equal(t, StatusPending, out["status"], "a HEAD consumed a poll")
}

// TestResearchPollForeignIDDiagnostic is design §8's diagnostic, end to end: a
// poll for an identifier that is SHAPED like one this process mints, but that
// this process never minted, is still the vendor's ordinary 404 — the response
// is byte-identical either way — but in a namespace that has minted a real
// task it additionally raises provider.CodeJobForeignID as a WARNING, never an
// error, so testkit.AssertNoErrors would still pass it.
func TestResearchPollForeignIDDiagnostic(t *testing.T) {
	t.Parallel()

	ring := journal.NewRing(64, 1<<16)
	h := newHandler(t, researchScenario, ring)
	// Well-formed (a bare UUID, the shape researchIDPrefix produces), never
	// minted by this process.
	const foreignID = "00000000-0000-0000-0000-000000000000"

	// X: the default namespace has minted a real task.
	researchCreate(t, h, `{"input":"research this"}`)

	pollX := getResearch(t, h, "/research/"+foreignID)
	require.Equal(t, http.StatusNotFound, pollX.Code)
	codesX := findingCodes(t, ring)
	assert.Contains(t, codesX, CodeResearchNotFound, "the vendor-shaped 404 finding still fires")
	require.Contains(t, codesX, provider.CodeJobForeignID, "a minted namespace must raise the diagnostic on a miss")

	entriesX := ring.Snapshot()
	entryX := entriesX[len(entriesX)-1]
	require.Equal(t, journal.SeverityWarning, findingByCode(t, entryX, provider.CodeJobForeignID).Severity,
		"a typo'd fixture id in a one-process suite must not read as an error")
	require.Empty(t, entryX.Errors(), "testkit.AssertNoErrors must still pass a request that only warned")

	// Y: a namespace that has minted nothing at all.
	pollY := getResearch(t, h, "/n/foreign-empty/research/"+foreignID)
	assert.Equal(t, http.StatusNotFound, pollY.Code)
	assert.NotContains(t, findingCodes(t, ring), provider.CodeJobForeignID,
		"an empty namespace has minted nothing, so a miss there is a typo, not a divergence")

	assert.Equal(t, pollX.Body.Bytes(), pollY.Body.Bytes(),
		"the diagnostic must not change the vendor-shaped response body")

	// HEAD asks the same question as GET — does this task exist here — and gets
	// the same diagnostic.
	headReq := httptest.NewRequest(http.MethodHead, "/research/"+foreignID, nil)
	headReq.Header.Set("Authorization", bearer)
	head := httptest.NewRecorder()
	h.ServeHTTP(head, headReq)
	assert.Equal(t, http.StatusNotFound, head.Code)
	assert.Contains(t, findingCodes(t, ring), provider.CodeJobForeignID, "HEAD raises the same diagnostic as GET")
}

// There is no cancelled status on this surface, unlike Exa's agent runs.
// Offering one on the strength of the sibling would teach a consumer to handle a
// value the vendor never sends.
func TestResearchValidatorRejectsAnUnknownStatus(t *testing.T) {
	t.Parallel()

	loaded := mustParse(t, `
version: 1
name: v
providers:
  tavily_research:
    turns:
      - respond: {status: cancelled}
`)
	findings := provider.ValidateScenario(loaded, map[string]provider.Validator{
		NameResearch: ResearchValidator{},
	})

	var found bool
	for _, f := range findings {
		if f.Code == CodeResearchStatusUnknown {
			found = true
			assert.Contains(t, f.Message, "pending", "the message must name the documented set")
		}
	}
	assert.True(t, found, "cancelled is not a Tavily research status: %+v", findings)
}

func TestResearchValidatorRejectsTerminalThenPending(t *testing.T) {
	t.Parallel()

	loaded := mustParse(t, `
version: 1
name: v
providers:
  tavily_research:
    turns:
      - when: {call_index: 0}
        respond: {status: completed, content: done}
      - respond: {status: pending}
`)
	findings := provider.ValidateScenario(loaded, map[string]provider.Validator{
		NameResearch: ResearchValidator{},
	})

	var found bool
	for _, f := range findings {
		if f.Code == CodeResearchTerminalThenPending {
			found = true
		}
	}
	assert.True(t, found, "a task that un-completes must be an error: %+v", findings)
}

// mustParse loads a scenario WITHOUT running the projection validators, which
// is what a test about a validator finding needs: newHandler refuses a scenario
// whose projections do not validate, and that is exactly the input here.
func mustParse(t *testing.T, src string) *scenario.Scenario {
	t.Helper()
	loaded, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err, "%v", report.Findings)
	return loaded
}
