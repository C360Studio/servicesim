package exa

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
)

// findSimilarScenario backs the relevance-route (D-b) behaviour tests: its own
// `find_similar:` sub-key, independent of the `results:` /search renders.
const findSimilarScenario = `
version: 1
name: exa-find-similar
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Full text of Report A.
  - id: source-b
    url: https://example.test/report-b
    title: Report B
    text: Full text of Report B.
providers:
  exa:
    results:
      - source: source-a
    find_similar:
      results:
        - source: source-a
        - source: source-b
      cost_dollars:
        total: 0.004
`

// --- rendering (D-b) -------------------------------------------------------

func TestFindSimilar_RendersItsOwnScriptedResultsLikeSearch(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarScenario)
	rec := s.findSimilar(`{"url":"https://example.test/report-a"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var got findSimilarResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Results, 2, "find_similar has its own results, independent of /search's")
	assert.Equal(t, "Report A", got.Results[0].Title)
	assert.Equal(t, "Report B", got.Results[1].Title)
	assert.InDelta(t, 0.004, got.CostDollars.Total, 1e-9)
}

func TestFindSimilar_TruncatesToNumResultsInDeclarationOrder(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarScenario)
	rec := s.findSimilar(`{"url":"https://example.test/report-a","numResults":1}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var got findSimilarResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Results, 1)
	assert.Equal(t, "Report A", got.Results[0].Title, "truncation takes the first N, never the highest scored")
}

func TestFindSimilar_EmptyResultsRenderAsAnArrayAndCostIsAlwaysPresent(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-find-similar-empty
providers:
  exa:
    find_similar: {}
`)
	body := s.findSimilar(`{"url":"https://example.test/report-a"}`).Body.String()
	assert.Contains(t, body, `"results":[]`)
	assert.Contains(t, body, `"costDollars":{"total":0}`)
}

func TestFindSimilar_ContextIsEmittedOnlyWhenTheScenarioDeclaresIt(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarScenario)
	plain := s.findSimilar(`{"url":"https://example.test/report-a"}`).Body.String()
	assert.NotContains(t, plain, `"context"`)

	withContext := newSim(t, `
version: 1
name: exa-find-similar-context
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    find_similar:
      results:
        - source: source-a
      context: a deprecated combined-context string
`)
	body := withContext.findSimilar(`{"url":"https://example.test/report-a"}`).Body.String()
	assert.Contains(t, body, `"context":"a deprecated combined-context string"`)
}

func TestFindSimilar_DoesNotInventADeprecationHeaderOrBody(t *testing.T) {
	t.Parallel()

	// D-b: the vendor's own OpenAPI spec marks the route deprecated, but the
	// contract records no deprecation header or body field, so this simulator
	// must not invent one.
	s := newSim(t, findSimilarScenario)
	rec := s.findSimilar(`{"url":"https://example.test/report-a"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Deprecation"))
	assert.Empty(t, rec.Header().Get("Sunset"))
	assert.NotContains(t, rec.Body.String(), "deprecat")
}

// --- request validation --------------------------------------------------

func TestValidateFindSimilar_Rejections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		code string
	}{
		{"url missing", `{}`, codeFindSimilarURLMissing},
		{"url wrong type", `{"url":42}`, codeFindSimilarURLMissing},
		{"url too short", `{"url":"ab"}`, codeFindSimilarURLLength},
		{"numResults below range", `{"url":"https://example.test/x","numResults":0}`, codeNumResultsRange},
		{"numResults above range", `{"url":"https://example.test/x","numResults":101}`, codeNumResultsRange},
		{"numResults fractional", `{"url":"https://example.test/x","numResults":1.5}`, codeFieldType},
		{
			"includeDomains not an array",
			`{"url":"https://example.test/x","includeDomains":"example.test"}`,
			codeFieldType,
		},
		{
			"includeDomains element not a string",
			`{"url":"https://example.test/x","includeDomains":[1]}`,
			codeFieldType,
		},
		{
			"excludeDomains element not a string",
			`{"url":"https://example.test/x","excludeDomains":[true]}`,
			codeFieldType,
		},
		{
			"category outside the enum",
			`{"url":"https://example.test/x","category":"blog"}`,
			codeCategoryInvalid,
		},
		{"contents not an object", `{"url":"https://example.test/x","contents":true}`, codeFieldType},
		{
			"excludeSourceDomain wrong type",
			`{"url":"https://example.test/x","excludeSourceDomain":"yes"}`,
			codeFieldType,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, findSimilarScenario)
			rec := s.findSimilar(tc.body)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
			assert.Equal(t, journal.SeverityError, s.findingSeverity(tc.code), "expected finding %s", tc.code)

			var got errorResponseWire
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.NotEmpty(t, got.Error)
			assert.NotEmpty(t, got.Tag)
		})
	}
}

// TestValidateFindSimilar_URLMinLengthCountsCodePointsNotBytes pins the
// contract's minLength as a JSON Schema string length: a two-character
// multibyte string is two characters, not (necessarily more) bytes, and must
// still fail the 3-character minimum.
func TestValidateFindSimilar_URLMinLengthCountsCodePointsNotBytes(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarScenario)
	// "日本" is two Unicode code points but six UTF-8 bytes; len() would wrongly
	// accept it as meeting the 3-character minimum.
	rec := s.findSimilar(`{"url":"日本"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, journal.SeverityError, s.findingSeverity(codeFindSimilarURLLength))
}

func TestValidateFindSimilar_AcceptsEveryDocumentedCategory(t *testing.T) {
	t.Parallel()

	for _, category := range categories {
		t.Run(category, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, findSimilarScenario)
			rec := s.findSimilar(`{"url":"https://example.test/report-a","category":"` + category + `"}`)
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

// TestValidateFindSimilar_NullIsAcceptedOnEveryOptionalField is D-d made
// assertable for this route: every optional field except `url` is documented
// nullable, and explicit JSON null must not fail validation.
func TestValidateFindSimilar_NullIsAcceptedOnEveryOptionalField(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarScenario)
	rec := s.findSimilar(`{
		"url": "https://example.test/report-a",
		"numResults": null,
		"includeDomains": null,
		"excludeDomains": null,
		"startPublishedDate": null,
		"endPublishedDate": null,
		"startCrawlDate": null,
		"endCrawlDate": null,
		"contents": null,
		"category": null,
		"excludeSourceDomain": null
	}`)
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}

func TestValidateFindSimilar_DeprecatedDatesAreAcceptedAndFlagged(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		code string
	}{
		{"startCrawlDate", `{"url":"https://example.test/x","startCrawlDate":"2026-01-01"}`, codeStartCrawlDateDeprecated},
		{"endCrawlDate", `{"url":"https://example.test/x","endCrawlDate":"2026-01-02"}`, codeEndCrawlDateDeprecated},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, findSimilarScenario)
			rec := s.findSimilar(tc.body)

			require.Equal(t, http.StatusOK, rec.Code, "a deprecated field must never fail the request")
			assert.Equal(t, journal.SeverityWarning, s.findingSeverity(tc.code))
		})
	}
}

func TestValidateFindSimilar_UnknownFieldIsAWarningNotAnError(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarScenario)
	rec := s.findSimilar(`{"url":"https://example.test/report-a","someFutureField":1}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, journal.SeverityWarning, s.findingSeverity(codeUnknownField))
}

// --- faults and lifecycle -------------------------------------------------

func TestFindSimilar_FaultBudgetIsIndependentOfSearchAnswerAndContents(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-find-similar-fault
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
    find_similar:
      fault:
        attempts:
          - status: 503
      results:
        - source: source-a
`)
	assert.Equal(t, http.StatusOK, s.search(`{"query":"report a"}`).Code,
		"/search must stay healthy while /findSimilar is faulted")

	rec := s.findSimilar(`{"url":"https://example.test/report-a"}`)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestFindSimilar_RequestIDIsDistinctPerCall(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarScenario)
	first := requestIDOf(t, s.findSimilar(`{"url":"https://example.test/report-a"}`).Body.Bytes())
	second := requestIDOf(t, s.findSimilar(`{"url":"https://example.test/report-a"}`).Body.Bytes())

	assert.Regexp(t, hex32, first)
	assert.NotEqual(t, first, second)

	contents := requestIDOf(t, s.contents(`{"urls":["https://example.test/report-a"]}`).Body.Bytes())
	assert.NotEqual(t, first, contents, "the two routes derive from different budget keys")
}

func TestFindSimilar_401NoCredential(t *testing.T) {
	t.Parallel()

	s := newSim(t, findSimilarScenario)
	rec := s.do(request{path: "/findSimilar", body: `{"url":"https://example.test/report-a"}`, noAuth: true})

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var got errorResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, tagInvalidAPIKey, got.Tag)
}
