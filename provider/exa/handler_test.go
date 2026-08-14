package exa

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/faults"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/scenarios"
)

// --- harness ----------------------------------------------------------------

// sim is one configured Exa listener plus the journal it writes to, which is
// where a test reads the findings a request produced.
type sim struct {
	t       *testing.T
	handler http.Handler
	journal *journal.Ring
}

// newSim builds a listener over a scenario written inline. Faults are wired from
// the scenario, because a handler built without them serves a fault-free
// response even where the scenario declares a 429 — the exact wiring mistake
// Deps.Normalized warns about.
func newSim(t *testing.T, src string) *sim {
	t.Helper()

	s, report, err := scenario.Parse([]byte(src))
	require.NoErrorf(t, err, "scenario did not load: %v", report.Findings)

	// Projection bodies are invisible to scenario.Validate, so the seam that
	// checks them runs here too: a fixture with a bad field must fail in this
	// test, not on the first request.
	findings := provider.ValidateScenario(s, map[string]provider.Validator{providerName: Validator{}})
	for _, f := range findings {
		require.NotEqualf(t, scenario.SeverityError, f.Severity, "%s: %s", f.Path, f.Message)
	}

	ring := journal.NewRing(64, 1<<16)
	deps := provider.Deps{
		Scenario:  s,
		Journal:   ring,
		Faults:    faults.New(s, Routes()),
		DelayMode: provider.DelaySkip,
	}
	return &sim{t: t, handler: New(deps), journal: ring}
}

// request is one HTTP call against the listener.
type request struct {
	method  string
	path    string
	body    string
	headers map[string]string
	noAuth  bool
}

func (s *sim) do(r request) *httptest.ResponseRecorder {
	s.t.Helper()

	method := r.method
	if method == "" {
		method = http.MethodPost
	}
	req := httptest.NewRequest(method, r.path, strings.NewReader(r.body))
	req.Header.Set("Content-Type", "application/json")
	if !r.noAuth {
		req.Header.Set("x-api-key", "test-key")
	}
	for name, value := range r.headers {
		req.Header.Set(name, value)
	}

	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

// search posts a /search body with the default credential.
func (s *sim) search(body string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.do(request{path: "/search", body: body})
}

// answer posts an /answer body with the default credential.
func (s *sim) answer(body string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.do(request{path: "/answer", body: body})
}

// findings returns the journal findings of the most recent request.
func (s *sim) findings() []journal.Finding {
	s.t.Helper()
	entries := s.journal.Snapshot()
	require.NotEmpty(s.t, entries, "no journal entry was recorded")
	return entries[len(entries)-1].Findings
}

// hasFinding reports whether the most recent request recorded code.
func (s *sim) hasFinding(code string) bool {
	for _, f := range s.findings() {
		if f.Code == code {
			return true
		}
	}
	return false
}

// findingSeverity returns the severity recorded for code, or the empty string.
func (s *sim) findingSeverity(code string) journal.Severity {
	for _, f := range s.findings() {
		if f.Code == code {
			return f.Severity
		}
	}
	return ""
}

// hex32 is the shape Exa documents for requestId. A slug would fail it, which is
// the point: a consumer regex trained on a slug breaks against the real API.
var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// requestIDOf extracts requestId from a response body, or "" when there is none.
func requestIDOf(t *testing.T, body []byte) string {
	t.Helper()
	var probe struct {
		RequestID string `json:"requestId"`
	}
	require.NoError(t, json.Unmarshal(body, &probe))
	return probe.RequestID
}

// --- scenarios --------------------------------------------------------------

// twoSourceScenario is the corpus the golden fixtures in contracts/exa were
// written against: Report A carries every optional field, Report B carries none,
// so one scenario exercises both the "value" and the "null" branch of every
// tri-state field.
const twoSourceScenario = `
version: 1
name: exa-goldens
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
        highlights:
          - deterministic simulators remove flakiness
        highlight_scores:
          - 0.87
        summary: Report A argues for deterministic simulation.
      - source: source-b
    cost_dollars:
      total: 0.005
      neural: 0.005
    answer:
      answer: Both reports agree that deterministic simulation removes flakiness from adapter suites.
      citations:
        - source-a
        - source-b
      cost_dollars:
        total: 0.005
`

// minimalScenario is one source and one result, for tests about behaviour rather
// than about the exact wire bytes.
const minimalScenario = `
version: 1
name: exa-minimal
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Full source text of Report A.
providers:
  exa:
    results:
      - source: source-a
    cost_dollars:
      total: 0.005
`

// --- routing ----------------------------------------------------------------

func TestRoutes_DeclareSeparateFaultBudgets(t *testing.T) {
	t.Parallel()

	routes := Routes()
	require.Len(t, routes, 2)
	assert.Equal(t, patternSearch, routes[0].Pattern)
	assert.Equal(t, patternAnswer, routes[1].Pattern)
	assert.NotEqual(t, routes[0].FaultKey, routes[1].FaultKey,
		"an /answer call must not consume /search's retry budget")
}

func TestRouting_FailsClosedInExasOwnEnvelope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		request request
		status  int
		tag     string
		message string
		allow   string
	}{
		{
			name:    "unknown path",
			request: request{path: "/nope", body: `{"query":"report a"}`},
			status:  http.StatusNotFound,
			tag:     TagNotFound,
			message: messageNotFound,
		},
		{
			name:    "trailing slash is a different path",
			request: request{path: "/search/", body: `{"query":"report a"}`},
			status:  http.StatusNotFound,
			tag:     TagNotFound,
			message: messageNotFound,
		},
		{
			name:    "wrong method on a known path",
			request: request{method: http.MethodGet, path: "/search"},
			status:  http.StatusMethodNotAllowed,
			tag:     TagMethodNotAllowed,
			message: messageMethodNotAllowed,
			allow:   "POST",
		},
		{
			name:    "wrong method on answer",
			request: request{method: http.MethodDelete, path: "/answer"},
			status:  http.StatusMethodNotAllowed,
			tag:     TagMethodNotAllowed,
			message: messageMethodNotAllowed,
			allow:   "POST",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, minimalScenario)
			rec := s.do(tc.request)

			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			if tc.allow != "" {
				assert.Equal(t, tc.allow, rec.Header().Get("Allow"))
			}

			var got ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, tc.tag, got.Tag)
			assert.Equal(t, tc.message, got.Error)
			assert.Regexp(t, hex32, got.RequestID)
		})
	}
}

// --- authentication ---------------------------------------------------------

func TestAuth_AcceptsBothDocumentedPlacements(t *testing.T) {
	t.Parallel()

	cases := map[string]map[string]string{
		"x-api-key":            {"x-api-key": "test-key"},
		"authorization bearer": {"Authorization": "Bearer test-key"},
		"lower-case bearer":    {"Authorization": "bearer test-key"},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, minimalScenario)
			rec := s.do(request{path: "/search", body: `{"query":"report a"}`, noAuth: true, headers: headers})
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestAuth_MissingCredentialIs401WithInvalidAPIKeyTag(t *testing.T) {
	t.Parallel()

	s := newSim(t, minimalScenario)
	rec := s.do(request{path: "/search", body: `{"query":"report a"}`, noAuth: true})

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var got ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, TagInvalidAPIKey, got.Tag)
	assert.Equal(t, messageInvalidAPIKey, got.Error)
	assert.True(t, s.hasFinding(codeAuthMissing))
}

func TestAuth_RejectModeAlwaysFails(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-unauthorized
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    auth:
      mode: reject
    results:
      - source: source-a
`)
	rec := s.search(`{"query":"report a"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.True(t, s.hasFinding(codeAuthMismatch))
}

func TestAuth_ExpectKeyMismatchNeverQuotesTheCredential(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-expect-key
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    auth:
      expect_key: the-expected-key
    results:
      - source: source-a
`)
	rec := s.do(request{
		path:    "/search",
		body:    `{"query":"report a"}`,
		noAuth:  true,
		headers: map[string]string{"x-api-key": "sk-live-should-never-be-quoted"},
	})

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotContains(t, rec.Body.String(), "sk-live-should-never-be-quoted")
	for _, f := range s.findings() {
		assert.NotContains(t, f.Message, "sk-live-should-never-be-quoted")
		assert.NotContains(t, f.Message, "the-expected-key")
	}
}

func TestAuth_PolicyHeadersNarrowTheAcceptedPlacements(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-bearer-only
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    auth:
      headers: [authorization]
    results:
      - source: source-a
`)
	rec := s.do(request{
		path:    "/search",
		body:    `{"query":"report a"}`,
		noAuth:  true,
		headers: map[string]string{"x-api-key": "test-key"},
	})

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, journal.SeverityWarning, s.findingSeverity(codeAuthWrongPlacement))
}

func TestAuth_OptionalModeServesAnUnauthenticatedRequest(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-optional-auth
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    auth:
      mode: optional
    results:
      - source: source-a
`)
	rec := s.do(request{path: "/search", body: `{"query":"report a"}`, noAuth: true})
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- determinism ------------------------------------------------------------

// TestSearch_IsByteIdenticalAtTheSameCallPosition is house rule 2 in its precise
// form: the same scenario and the same request AT THE SAME CALL POSITION render
// the same bytes.
//
// The call position is what a lane cursor measures, and a namespace is a fresh
// lane, so the second request below is call 0 all over again — a new process or
// an admin reset would put it there just the same. Comparing two CONSECUTIVE
// calls instead would be asserting that requestId never moves, which is the bug
// this file's sibling test exists to prevent.
func TestSearch_IsByteIdenticalAtTheSameCallPosition(t *testing.T) {
	t.Parallel()

	s := newSim(t, minimalScenario)
	first := s.search(`{"query":"report a"}`).Body.Bytes()
	fresh := s.do(request{path: "/n/second-lane/search", body: `{"query":"report a"}`}).Body.Bytes()

	assert.Equal(t, string(first), string(fresh),
		"call 0 of a fresh lane must reproduce call 0 byte for byte")
	assert.Regexp(t, hex32, requestIDOf(t, first))
}

// TestSearch_RequestIDIsDistinctPerCall pins the property a real vendor has and
// a collapsed identifier destroys: a consumer correlating a log line to a
// request must be able to tell one call from the next.
//
// The route here declares no fault plan on purpose. The call index used to enter
// the tuple only where one did, which meant every response an unfaulted scenario
// ever served — the 200, the 401 beside it, the next 200 — carried one requestId.
func TestSearch_RequestIDIsDistinctPerCall(t *testing.T) {
	t.Parallel()

	s := newSim(t, minimalScenario)
	first := requestIDOf(t, s.search(`{"query":"report a"}`).Body.Bytes())
	second := requestIDOf(t, s.search(`{"query":"report a"}`).Body.Bytes())

	assert.Regexp(t, hex32, first, "requestId keeps Exa's 32 lowercase hex shape")
	assert.Regexp(t, hex32, second)
	assert.NotEqual(t, first, second, "two successive calls must not share a requestId")

	// A namespace is a fresh state lane, so this is call 0 again and must
	// reproduce the first identifier exactly. Distinctness that came from a clock
	// or a counter of the handler's own would fail here.
	fresh := requestIDOf(t, s.do(request{path: "/n/lane-b/search", body: `{"query":"report a"}`}).Body.Bytes())
	assert.Equal(t, first, fresh, "the same call position in a fresh lane must reproduce the same requestId")
}

// TestAnswer_RequestIDIsDistinctPerCall covers the second Exa route. It draws on
// its own budget key, so its identifiers must move independently of /search's.
func TestAnswer_RequestIDIsDistinctPerCall(t *testing.T) {
	t.Parallel()

	// No request_id override: the derived identifier is the thing under test, and
	// an override would pin it to a constant and prove nothing.
	s := newSim(t, `
version: 1
name: exa-answer-derived
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
    answer:
      answer: Report A argues for deterministic simulation.
      citations:
        - source-a
`)
	first := requestIDOf(t, s.answer(`{"query":"report a"}`).Body.Bytes())
	second := requestIDOf(t, s.answer(`{"query":"report a"}`).Body.Bytes())

	assert.Regexp(t, hex32, first)
	assert.NotEqual(t, first, second, "two successive /answer calls must not share a requestId")

	fresh := requestIDOf(t, s.do(request{path: "/n/lane-b/answer", body: `{"query":"report a"}`}).Body.Bytes())
	assert.Equal(t, first, fresh)

	// /search at the same call position sits on a different fault key, so it
	// cannot collide with /answer.
	search := requestIDOf(t, s.do(request{path: "/n/lane-b/search", body: `{"query":"report a"}`}).Body.Bytes())
	assert.NotEqual(t, first, search, "the two routes derive from different budget keys")
}

// TestSearch_RejectedRequestsCarryTheUnclaimedRequestID records the one place the
// per-call property deliberately stops.
//
// A request rejected by authentication or validation must not consume a fault
// attempt (§4.4), and claiming the call index is what consuming one means, so a
// rejected request has no call position to derive from. Its requestId is
// therefore the tuple with no index at all: distinguishable from every served
// response, and shared with the other rejections in its lane. That is a cost of
// the attempt rule rather than a property worth having, and it is written down
// here so the next reader does not mistake it for one.
func TestSearch_RejectedRequestsCarryTheUnclaimedRequestID(t *testing.T) {
	t.Parallel()

	s := newSim(t, minimalScenario)
	served := requestIDOf(t, s.search(`{"query":"report a"}`).Body.Bytes())

	rec := s.do(request{path: "/search", body: `{"query":"report a"}`, noAuth: true})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rejected := requestIDOf(t, rec.Body.Bytes())

	assert.Regexp(t, hex32, rejected)
	assert.NotEqual(t, served, rejected,
		"a 401 must not carry the same requestId as the 200 beside it")
}

func TestSearch_RequestIDMovesPerAttemptUnderAFaultPlan(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-planned
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    fault:
      attempts:
        - status: 200
        - status: 200
    results:
      - source: source-a
`)
	first := requestIDOf(t, s.search(`{"query":"report a"}`).Body.Bytes())
	second := requestIDOf(t, s.search(`{"query":"report a"}`).Body.Bytes())

	assert.NotEqual(t, first, second,
		"the attempt index joins the identifier tuple, so a retry is distinguishable from the original")
	assert.Regexp(t, hex32, first)
	assert.Regexp(t, hex32, second)
}

// --- turns ------------------------------------------------------------------

const scriptedScenario = `
version: 1
name: exa-scripted
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    turns:
      - when:
          call_index: 0
        respond:
          results: []
          cost_dollars:
            total: 0
      - when:
          body_contains: report-b
        respond:
          results:
            - source: source-a
              summary: matched by body
      - respond:
          results:
            - source: source-a
`

func TestSearch_MultiTurnScriptAdvancesWithTheCallIndex(t *testing.T) {
	t.Parallel()

	s := newSim(t, scriptedScenario)

	var first SearchResponse
	require.NoError(t, json.Unmarshal(s.search(`{"query":"report a"}`).Body.Bytes(), &first))
	assert.Empty(t, first.Results, "call 0 must select the turn scripted for it")

	var second SearchResponse
	require.NoError(t, json.Unmarshal(s.search(`{"query":"report-b"}`).Body.Bytes(), &second))
	require.Len(t, second.Results, 1)
	assert.Equal(t, "matched by body", second.Results[0].Summary)

	var third SearchResponse
	require.NoError(t, json.Unmarshal(s.search(`{"query":"anything"}`).Body.Bytes(), &third))
	require.Len(t, third.Results, 1)
	assert.Empty(t, third.Results[0].Summary, "the unconditional turn is the fallback")
}

func TestAnswer_HasItsOwnTurnCursor(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-two-cursors
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    turns:
      - when:
          call_index: 0
        respond:
          answer:
            answer: first answer
      - respond:
          answer:
            answer: later answer
`)
	// Two /search calls advance the search cursor only.
	s.search(`{"query":"report a"}`)
	s.search(`{"query":"report a"}`)

	var got AnswerResponse
	require.NoError(t, json.Unmarshal(s.answer(`{"query":"report a"}`).Body.Bytes(), &got))
	assert.Equal(t, "first answer", got.Answer,
		"/answer is keyed on its own fault budget, so /search calls must not advance it")
}

func TestSearch_NoMatchingTurnFailsClosed(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-no-fallback
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    turns:
      - when:
          call_index: 5
        respond:
          results:
            - source: source-a
`)
	rec := s.search(`{"query":"report a"}`)

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.True(t, s.hasFinding(provider.CodeNoMatchingTurn))
	var got ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, TagNotFound, got.Tag)
}

// --- faults -----------------------------------------------------------------

func TestFault_RateLimitUsesTheReducedBodyAndThenSucceeds(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-rate-limited
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    fault:
      attempts:
        - status: 429
          retry_after: 1
        - status: 200
    results:
      - source: source-a
`)
	limited := s.search(`{"query":"report a"}`)
	require.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.Equal(t, "1", limited.Header().Get("Retry-After"))

	// The reduced shape is the whole point: no requestId, no tag.
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(limited.Body.Bytes(), &fields))
	assert.Len(t, fields, 1)
	assert.Contains(t, fields, "error")

	after := s.search(`{"query":"report a"}`)
	assert.Equal(t, http.StatusOK, after.Code, "the trailing status: 200 attempt serves the scenario response")
}

func TestFault_RejectedRequestConsumesNoAttempt(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-budget
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    fault:
      attempts:
        - status: 429
    results:
      - source: source-a
`)
	// A request rejected by validation must not spend the 429.
	invalid := s.search(`{}`)
	require.Equal(t, http.StatusBadRequest, invalid.Code)

	limited := s.search(`{"query":"report a"}`)
	assert.Equal(t, http.StatusTooManyRequests, limited.Code,
		"the rejected request must have left the attempt budget untouched")
}

func TestFault_AnswerBudgetIsIndependentOfSearch(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-answer-fault
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
    answer:
      fault:
        attempts:
          - status: 501
      answer: Report A states the finding.
      citations:
        - source-a
`)
	assert.Equal(t, http.StatusOK, s.search(`{"query":"report a"}`).Code,
		"/search must stay healthy while /answer is faulted")

	rec := s.answer(`{"query":"report a"}`)
	require.Equal(t, http.StatusNotImplemented, rec.Code)
	var got ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, TagUnableToGenerateResponse, got.Tag)
}

// --- projection behaviour ---------------------------------------------------

func TestSearch_TruncatesToNumResultsInDeclarationOrder(t *testing.T) {
	t.Parallel()

	s := newSim(t, twoSourceScenario)

	// Decoded into a local shape rather than SearchResponse: scenario.Nullable
	// implements MarshalJSON and not UnmarshalJSON, so a non-null publishedDate
	// cannot be read back into the exported wire type.
	var got struct {
		Results []struct {
			Title string `json:"title"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(s.search(`{"query":"report a","numResults":1}`).Body.Bytes(), &got))

	require.Len(t, got.Results, 1)
	assert.Equal(t, "Report A", got.Results[0].Title, "truncation takes the first N, never the highest scored")
}

func TestSearch_OmitFieldsReachesTheAbsentStateOfATriStateField(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-omit
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
        omit_fields: [publishedDate, author]
`)
	body := s.search(`{"query":"report a"}`).Body.Bytes()

	var decoded struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Len(t, decoded.Results, 1)
	assert.NotContains(t, decoded.Results[0], "publishedDate")
	assert.NotContains(t, decoded.Results[0], "author")
}

func TestSearch_ExtraFieldsMergeAtBothLevels(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-extra
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    extra_fields:
      experimentalRanking: b
    results:
      - source: source-a
        extra_fields:
          relevanceExplanation: an unreleased field
`)
	body := s.search(`{"query":"report a"}`).Body.Bytes()

	var decoded struct {
		Experimental string                       `json:"experimentalRanking"`
		Results      []map[string]json.RawMessage `json:"results"`
	}
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, "b", decoded.Experimental)
	require.Len(t, decoded.Results, 1)
	assert.Contains(t, decoded.Results[0], "relevanceExplanation")
}

func TestSearch_OutputIsEmittedOnlyWhenTheRequestSuppliedOutputSchema(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: exa-structured
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
    output:
      content:
        finding: a structured finding
      grounding:
        - field: finding
          citations:
            - source-a
          confidence: high
`
	s := newSim(t, src)

	plain := s.search(`{"query":"report a"}`).Body.Bytes()
	assert.NotContains(t, string(plain), `"output"`)

	structured := s.search(`{"query":"report a","outputSchema":{"type":"object"}}`).Body.Bytes()
	var got SearchResponse
	require.NoError(t, json.Unmarshal(structured, &got))
	require.NotNil(t, got.Output)
	require.Len(t, got.Output.Grounding, 1)
	assert.Equal(t, "high", got.Output.Grounding[0].Confidence)
	require.Len(t, got.Output.Grounding[0].Citations, 1)
	assert.Equal(t, "https://example.test/report-a", got.Output.Grounding[0].Citations[0].ID)
}

func TestAnswer_CitationTextIsGatedOnTheRequest(t *testing.T) {
	t.Parallel()

	s := newSim(t, twoSourceScenario)

	withoutText := s.answer(`{"query":"report a"}`).Body.Bytes()
	assert.NotContains(t, string(withoutText), `"text"`,
		"text defaults to false, and the key is omitted rather than emitted empty")

	withText := s.answer(`{"query":"report a","text":true}`).Body.Bytes()
	var got struct {
		Citations []struct {
			Text string `json:"text"`
		} `json:"citations"`
	}
	require.NoError(t, json.Unmarshal(withText, &got))
	require.Len(t, got.Citations, 2)
	assert.NotEmpty(t, got.Citations[0].Text)
}

func TestAnswer_AbsentProjectionStillAnswersWellShaped(t *testing.T) {
	t.Parallel()

	s := newSim(t, minimalScenario)
	rec := s.answer(`{"query":"report a"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"answer":""`)
	assert.Contains(t, rec.Body.String(), `"citations":[]`,
		"citations is emitted as an empty array, never as null and never absent")
}

func TestSearch_EmptyResultsRenderAsAnArrayAndCostIsAlwaysPresent(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-empty
providers:
  exa:
    results: []
`)
	body := s.search(`{"query":"report a"}`).Body.String()
	assert.Contains(t, body, `"results":[]`)
	assert.Contains(t, body, `"costDollars":{"total":0}`,
		"costDollars is always present, even on an empty result set")
}

func TestSearch_NoScoreFieldIsEverEmitted(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-score
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
        score: 0.95
        highlight_scores: [0.87]
`)
	body := s.search(`{"query":"report a"}`).Body.String()
	assert.NotContains(t, body, `"score"`,
		"Exa's result schema has no top-level score field")
	assert.Contains(t, body, `"highlightScores":[0.87]`,
		"highlightScores is the only score-like field")
}

func TestValidator_ReportsAnAcceptedButNeverEmittedScore(t *testing.T) {
	t.Parallel()

	s, _, err := scenario.Parse([]byte(`
version: 1
name: exa-score
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
        score: 0.95
`))
	require.NoError(t, err)

	findings := Validator{}.ValidateProjections(s, s.Provider(providerName))
	require.Len(t, findings, 1)
	assert.Equal(t, codeScoreNotEmitted, findings[0].Code)
	assert.Equal(t, scenario.SeverityWarning, findings[0].Severity)
	assert.Equal(t, "providers.exa.turns[0].respond.results[0].score", findings[0].Path)
}

func TestValidator_RejectsAnUnknownSourceReferenceBeforeReadiness(t *testing.T) {
	t.Parallel()

	s, _, err := scenario.Parse([]byte(`
version: 1
name: exa-bad-ref
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-z
`))
	require.NoError(t, err)

	findings := Validator{}.ValidateProjections(s, s.Provider(providerName))
	require.NotEmpty(t, findings)
	assert.Equal(t, scenario.SeverityError, findings[0].Severity)
	assert.Contains(t, findings[0].Message, "source-z")
}

func TestValidator_RejectsAProjectionBodyWithABadFieldType(t *testing.T) {
	t.Parallel()

	s, _, err := scenario.Parse([]byte(`
version: 1
name: exa-bad-field
providers:
  exa:
    cost_dollars:
      total: not-a-number
`))
	require.NoError(t, err)

	findings := Validator{}.ValidateProjections(s, s.Provider(providerName))
	require.NotEmpty(t, findings)
	assert.Equal(t, codeProjectionInvalid, findings[0].Code)
	assert.Equal(t, scenario.SeverityError, findings[0].Severity)
}

// TestValidator_AcceptsEveryBuiltInScenario is the check that keeps this
// package's projection schema and the shipped scenario files from drifting
// apart. The built-ins are what the image serves and what docs/scenario-schema.md
// documents; a key renamed here and not there would otherwise surface as a
// readiness failure in a consumer's container.
func TestValidator_AcceptsEveryBuiltInScenario(t *testing.T) {
	t.Parallel()

	for _, name := range scenarios.Names() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, report, err := scenarios.Load(name)
			require.NoErrorf(t, err, "%v", report.Findings)

			entry := s.Provider(providerName)
			require.NotNilf(t, entry, "built-in %s declares no exa provider", name)

			for _, f := range (Validator{}).ValidateProjections(s, entry) {
				assert.NotEqualf(t, scenario.SeverityError, f.Severity, "%s: %s", f.Path, f.Message)
			}
		})
	}
}

// TestBuiltIn_HappyRendersBothRoutes proves the default scenario the image serves
// answers on both Exa surfaces, which is the property that makes one --scenario
// flag configure the whole listener coherently.
func TestBuiltIn_HappyRendersBothRoutes(t *testing.T) {
	t.Parallel()

	src, err := scenarios.Read("happy")
	require.NoError(t, err)
	s := newSim(t, string(src))

	var search struct {
		Results []struct {
			Title string `json:"title"`
		} `json:"results"`
		CostDollars CostDollars `json:"costDollars"`
	}
	require.NoError(t, json.Unmarshal(s.search(`{"query":"report a"}`).Body.Bytes(), &search))
	require.Len(t, search.Results, 2)
	assert.Equal(t, "Report A", search.Results[0].Title)
	assert.InDelta(t, 0.005, search.CostDollars.Total, 1e-9)

	var answer struct {
		Answer    string `json:"answer"`
		Citations []struct {
			URL string `json:"url"`
		} `json:"citations"`
	}
	require.NoError(t, json.Unmarshal(s.answer(`{"query":"report a"}`).Body.Bytes(), &answer))
	assert.NotEmpty(t, answer.Answer)
	require.Len(t, answer.Citations, 2)
	assert.Equal(t, "https://example.test/report-a", answer.Citations[0].URL)
}

func TestNew_ZeroDepsServesAWellShapedEmptySuccess(t *testing.T) {
	t.Parallel()

	handler := New(provider.Deps{})
	req := httptest.NewRequest(http.MethodPost, "/search", bytes.NewBufferString(`{"query":"report a"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got SearchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Empty(t, got.Results)
	assert.Regexp(t, hex32, got.RequestID)
}
