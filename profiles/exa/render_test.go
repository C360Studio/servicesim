package exa

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/contracts"
)

// --- golden comparison ------------------------------------------------------

// assertGoldenWire compares a rendered body against a verified fixture as RAW
// JSON BYTES.
//
// Byte comparison rather than a decode-and-compare is the point of this file. A
// wrong JSON type — an integer where the vendor sends a string, a string where
// it sends a number — round-trips cleanly through a permissive decoder, so a
// struct-level assertion passes while the simulator teaches consumers the wrong
// contract. Bytes catch it; structs do not.
//
// One normalisation is applied: the fixture's requestId is substituted with the
// one observed. A derived identifier depends on the scenario seed and, where a
// fault plan exists, on the attempt index (§3.1), so pinning the vendor's
// documented example value would assert something no scenario can control.
// Every other byte, including key order, must match.
func assertGoldenWire(t *testing.T, fixture string, got []byte) {
	t.Helper()

	raw, err := contracts.Read(Profile().Contracts, fixture)
	require.NoError(t, err)

	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, raw))
	want := compact.String()

	if id := requestIDOf(t, raw); id != "" {
		want = strings.Replace(want, id, requestIDOf(t, got), 1)
	}
	assert.Equal(t, want, string(got), "rendered %s does not match contracts/exa/%s", fixture, fixture)
}

// assertGoldenWireCanonical is assertGoldenWire for a fixture whose body carries
// a scenario-authored JSON object — the structured answer.
//
// A scenario's structured content decodes into a Go map, and encoding/json
// orders map keys alphabetically, so the object's key order is deterministic but
// not the order the fixture was hand-written in. Both sides are therefore
// re-encoded through one decoder before comparison. Numbers keep their literal
// form (json.Number), so the type fidelity the byte comparison exists for
// survives: a number rendered as a string still fails.
func assertGoldenWireCanonical(t *testing.T, fixture string, got []byte) {
	t.Helper()

	raw, err := contracts.Read(Profile().Contracts, fixture)
	require.NoError(t, err)

	want := canonicalJSON(t, raw)
	if id := requestIDOf(t, raw); id != "" {
		want = strings.Replace(want, id, requestIDOf(t, got), 1)
	}
	assert.Equal(t, want, canonicalJSON(t, got))
}

func canonicalJSON(t *testing.T, b []byte) string {
	t.Helper()

	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()

	var v any
	require.NoError(t, dec.Decode(&v))
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return string(out)
}

// --- success goldens --------------------------------------------------------

func TestGolden_SearchHappy(t *testing.T) {
	t.Parallel()

	s := newSim(t, twoSourceScenario)
	rec := s.search(`{"query":"deterministic simulation"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-search-happy.json", rec.Body.Bytes())
}

func TestGolden_SearchEmpty(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-search-empty-golden
providers:
  exa:
    request_id: 1f0d2c3b4a596877665544332211ffee
    results: []
    cost_dollars:
      total: 0
`)
	rec := s.search(`{"query":"nothing matches"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-search-empty.json", rec.Body.Bytes())
}

// structuredScenario backs both structured-output goldens. Its single source
// carries no image or favicon, which is what the fixtures show: the /search
// structured fixture and the /answer structured fixture were captured against a
// leaner document than the happy path.
const structuredScenario = `
version: 1
name: exa-structured-golden
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    author: A. Author
    published_at: 2025-11-16T01:36:32.547Z
    text: Report A finds that deterministic simulators remove flakiness from adapter test suites.
providers:
  exa:
    request_id: 9c8b7a6d5e4f30211122334455667788
    results:
      - source: source-a
    cost_dollars:
      total: 0.01
      neural: 0.01
    resolved_search_type: auto
    context: Report A finds that deterministic simulators remove flakiness from adapter test suites.
    output:
      content:
        finding: Deterministic simulation removes flakiness from adapter suites.
      grounding:
        - field: finding
          citations:
            - source-a
          confidence: high
    answer:
      answer:
        finding: Deterministic simulation removes flakiness from adapter suites.
        confidence: high
      citations:
        - source-a
      cost_dollars:
        total: 0.005
`

func TestGolden_SearchStructured(t *testing.T) {
	t.Parallel()

	s := newSim(t, structuredScenario)
	rec := s.search(`{"query":"report a","outputSchema":{"type":"object","properties":{"finding":{"type":"string"}}}}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-search-structured.json", rec.Body.Bytes())
}

func TestGolden_AnswerHappy(t *testing.T) {
	t.Parallel()

	s := newSim(t, twoSourceScenario)
	rec := s.answer(`{"query":"do the reports agree?"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-answer-happy.json", rec.Body.Bytes())
}

func TestGolden_AnswerEmpty(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-answer-empty-golden
providers:
  exa:
    request_id: e6f708192a3b4c5d6e7f80912a3b4c5d
    answer:
      answer: ""
      citations: []
      cost_dollars:
        total: 0
`)
	rec := s.answer(`{"query":"nothing matches"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-answer-empty.json", rec.Body.Bytes())
}

func TestGolden_AnswerStructured(t *testing.T) {
	t.Parallel()

	s := newSim(t, structuredScenario)
	rec := s.answer(`{"query":"report a","outputSchema":{"type":"object","properties":{"finding":{"type":"string"}}}}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWireCanonical(t, "exa-answer-structured.json", rec.Body.Bytes())
}

// TestGolden_AnswerStructuredPutsStructureOnTheAnswerKey pins the difference the
// contract calls the easiest thing to get wrong: /search returns structured
// output under output.content, /answer overloads the answer key itself and has no
// grounding array at all.
func TestGolden_AnswerStructuredPutsStructureOnTheAnswerKey(t *testing.T) {
	t.Parallel()

	s := newSim(t, structuredScenario)
	body := s.answer(`{"query":"report a","outputSchema":{"type":"object"}}`).Body.String()

	assert.NotContains(t, body, `"output"`)
	assert.NotContains(t, body, `"grounding"`)
	assert.Contains(t, body, `"answer":{`)
}

// --- error goldens ----------------------------------------------------------

// faultedScenario declares a single always-on fault attempt, which is how the
// status-only error goldens are reproduced: their bodies are what Exa documents
// for a status, with no scenario-supplied message or tag.
func faultedScenario(status int) string {
	return `
version: 1
name: exa-fault-golden
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    fault:
      after: repeat_last
      attempts:
        - status: ` + strconv.Itoa(status) + `
    results:
      - source: source-a
    answer:
      fault:
        after: repeat_last
        attempts:
          - status: ` + strconv.Itoa(status) + `
      answer: Report A states the finding.
      citations:
        - source-a
`
}

func TestGolden_ErrorBodies(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		fixture  string
		scenario string
		request  request
		status   int
	}{
		{
			name:     "400 invalid type enum",
			fixture:  "exa-search-400.json",
			scenario: minimalScenario,
			request:  request{path: "/search", body: `{"query":"report a","type":"slow"}`},
			status:   http.StatusBadRequest,
		},
		{
			name:     "401 no credential",
			fixture:  "exa-search-401.json",
			scenario: minimalScenario,
			request:  request{path: "/search", body: `{"query":"report a"}`, noAuth: true},
			status:   http.StatusUnauthorized,
		},
		{
			name:     "402 out of credits",
			fixture:  "exa-search-402.json",
			scenario: faultedScenario(http.StatusPaymentRequired),
			request:  request{path: "/search", body: `{"query":"report a"}`},
			status:   http.StatusPaymentRequired,
		},
		{
			name:     "404 unknown path",
			fixture:  "exa-search-404.json",
			scenario: minimalScenario,
			request:  request{path: "/nope", body: `{"query":"report a"}`},
			status:   http.StatusNotFound,
		},
		{
			name:     "405 wrong method",
			fixture:  "exa-search-405.json",
			scenario: minimalScenario,
			request:  request{method: http.MethodGet, path: "/search"},
			status:   http.StatusMethodNotAllowed,
		},
		{
			name:     "422 invalid request",
			fixture:  "exa-search-422.json",
			scenario: faultedScenario(http.StatusUnprocessableEntity),
			request:  request{path: "/search", body: `{"query":"report a"}`},
			status:   http.StatusUnprocessableEntity,
		},
		{
			name:     "500 internal error",
			fixture:  "exa-search-500.json",
			scenario: faultedScenario(http.StatusInternalServerError),
			request:  request{path: "/search", body: `{"query":"report a"}`},
			status:   http.StatusInternalServerError,
		},
		{
			name:     "501 unable to generate a response",
			fixture:  "exa-answer-501.json",
			scenario: faultedScenario(http.StatusNotImplemented),
			request:  request{path: "/answer", body: `{"query":"report a"}`},
			status:   http.StatusNotImplemented,
		},
		{
			name:     "400 invalid output schema on answer",
			fixture:  "exa-answer-400-invalid-json-schema.json",
			scenario: minimalScenario,
			request: request{
				path: "/answer",
				body: `{"query":"report a","outputSchema":{"type":"object","properties":` +
					`{"a":{"type":"object","properties":{"b":{"type":"object","properties":` +
					`{"c":{"type":"string"}}}}}}}}`,
			},
			status: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, tc.scenario)
			rec := s.do(tc.request)

			require.Equal(t, tc.status, rec.Code)
			assertGoldenWire(t, tc.fixture, rec.Body.Bytes())
		})
	}
}

// TestGolden_RateLimitUsesTheReducedShape is separated from the table because it
// is the one exception in Exa's error model, and it is exactly the case a
// consumer's retry logic branches on. A simulator that emitted {requestId,
// error, tag} here would be wrong where wrongness costs the most, and the
// assertion below is a whole-body byte comparison with nothing normalised: the
// 429 fixture carries no requestId to substitute.
func TestGolden_RateLimitUsesTheReducedShape(t *testing.T) {
	t.Parallel()

	s := newSim(t, faultedScenario(http.StatusTooManyRequests))
	rec := s.search(`{"query":"report a"}`)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assertGoldenWire(t, "exa-search-429.json", rec.Body.Bytes())

	assert.NotContains(t, rec.Body.String(), "requestId")
	assert.NotContains(t, rec.Body.String(), "tag")
}

// TestGolden_FaultAttemptCanOverrideMessageAndTag proves the documented
// override path, and that the 429 shape still drops the tag it is given: the
// reduced body has nowhere to put one.
func TestGolden_FaultAttemptCanOverrideMessageAndTag(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-fault-override
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    fault:
      after: repeat_last
      attempts:
        - status: 429
          error: Rate limit exceeded.
          tag: RATE_LIMIT
    results:
      - source: source-a
`)
	rec := s.search(`{"query":"report a"}`)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.JSONEq(t, `{"error":"Rate limit exceeded."}`, rec.Body.String())
}

func TestGolden_FaultAttemptWithAVerbatimBodyWins(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-fault-verbatim
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    fault:
      after: repeat_last
      attempts:
        - status: 503
          body:
            message: a shape Exa does not document
    results:
      - source: source-a
`)
	rec := s.search(`{"query":"report a"}`)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, `{"message":"a shape Exa does not document"}`, rec.Body.String())
}

// --- derivations ------------------------------------------------------------

func TestRender_PublishedDateIsRFC3339WithMillisecondPrecision(t *testing.T) {
	t.Parallel()

	s := newSim(t, twoSourceScenario)
	body := s.search(`{"query":"report a"}`).Body.String()

	assert.Contains(t, body, `"publishedDate":"2025-11-16T01:36:32.547Z"`,
		"a date parser tuned to whole seconds must be made to fail here rather than in production")
	assert.Contains(t, body, `"publishedDate":null`,
		"a source with no date renders an explicit null, so the null branch is exercised")
	assert.Contains(t, body, `"author":null`)
}

func TestRender_ResultIDDefaultsToTheSourceURL(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-ids
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
  - id: source-b
    url: https://example.test/report-b
    title: Report B
providers:
  exa:
    results:
      - source: source-a
      - source: source-b
        id: an-explicit-override
`)
	var got struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(s.search(`{"query":"report a"}`).Body.Bytes(), &got))

	require.Len(t, got.Results, 2)
	assert.Equal(t, "https://example.test/report-a", got.Results[0].ID,
		"the default is the source URL, never a slug like source-1")
	assert.Equal(t, "an-explicit-override", got.Results[1].ID)
}

// TestRender_OmitFieldsWinsOverExtraFields pins the fix to exa's response
// ordering bug (Phase 10 unit 1, docs/adopter-backlog.md's Phase 10 section):
// provider.Render merges extra_fields into the body FIRST and drops
// omit_fields SECOND, matching docs/scenario-schema.md's documented order
// ("the merge happens first and the omission second, so omit_fields can
// remove a key extra_fields added") and every other provider package.
//
// Before the fix, exa's resultWire.MarshalJSON applied the two in the opposite
// order, so a key named by both omit_fields and extra_fields survived —
// "reinstated" by the extra field, in the old doc comment's own words. This
// test asserts the field is ABSENT: omit_fields wins.
func TestRender_OmitFieldsWinsOverExtraFields(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-omit-vs-extra
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
        omit_fields: [title]
        extra_fields:
          title: "reinstated by extra_fields"
`)
	body := s.search(`{"query":"report a"}`).Body.String()

	var got struct {
		Results []map[string]any `json:"results"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	require.Len(t, got.Results, 1)

	_, present := got.Results[0]["title"]
	assert.False(t, present,
		"omit_fields must win over extra_fields for the same key; title must not reach the wire, got body: %s", body)
}
