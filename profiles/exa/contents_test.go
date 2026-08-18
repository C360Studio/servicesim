package exa

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
)

// contentsScenario is the corpus D-a's "no extra authoring" claim depends on:
// its exa `results:` are the same list /search renders, and /contents resolves
// requested identifiers against that same list — the whole reason search ->
// contents on the "happy" built-in needs no contents-specific fixture at all.
const contentsScenario = `
version: 1
name: exa-contents
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
      - source: source-b
    cost_dollars:
      total: 0.005
    contents:
      cost_dollars:
        total: 0.01
`

// contentsCorpusOnlyScenario declares sources but no exa `results:`, so the
// only way a /contents call can resolve anything is D-a's corpus fallback.
const contentsCorpusOnlyScenario = `
version: 1
name: exa-contents-corpus-only
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Full text of Report A.
providers:
  exa: {}
`

// --- resolution (D-a) --------------------------------------------------

func TestContents_ResolvesRequestedURLsAgainstTheProjectionResults(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsScenario)
	rec := s.contents(`{"urls":["https://example.test/report-a"]}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var got contentsResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Results, 1)
	assert.Equal(t, "Report A", got.Results[0].Title)
	require.Len(t, got.Statuses, 1)
	assert.Equal(t, "https://example.test/report-a", got.Statuses[0].ID,
		"statuses[].id echoes the requested identifier, not a resolved document id")
	assert.Equal(t, "success", got.Statuses[0].Status)
	assert.Nil(t, got.Statuses[0].Error)

	assert.NotContains(t, rec.Body.String(), `"source"`,
		"both vendor success examples omit statuses[].source even on status: success")
}

func TestContents_ResolvesIDsAgainstTheirDeclaredID(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-contents-declared-id
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
        id: doc-a
    contents: {}
`)
	rec := s.contents(`{"ids":["doc-a"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var got contentsResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Results, 1)
	assert.Equal(t, "doc-a", got.Results[0].ID)
}

func TestContents_FallsBackToTheCorpusWhenNoProjectionResultNamesIt(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsCorpusOnlyScenario)
	rec := s.contents(`{"urls":["https://example.test/report-a"]}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var got contentsResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Results, 1, "an unauthored /contents fixture must still resolve a corpus URL directly")
	assert.Equal(t, "Report A", got.Results[0].Title)
}

func TestContents_UnresolvedIdentifierGetsCrawlNotFoundAndNoResult(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsScenario)
	rec := s.contents(`{"urls":["https://example.test/report-a","https://example.test/missing"]}`)
	require.Equal(t, http.StatusOK, rec.Code, "one resolved identifier keeps the response 200 (D-a)")

	var got contentsResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Results, 1, "an unresolved identifier renders no result")
	require.Len(t, got.Statuses, 2, "statuses[] carries one element per requested identifier")

	assert.Equal(t, "success", got.Statuses[0].Status)
	assert.Equal(t, "error", got.Statuses[1].Status)
	require.NotNil(t, got.Statuses[1].Error)
	assert.Equal(t, "CRAWL_NOT_FOUND", got.Statuses[1].Error.Tag)
	require.NotNil(t, got.Statuses[1].Error.HTTPStatusCode)
	assert.Equal(t, 404, *got.Statuses[1].Error.HTTPStatusCode)
}

func TestContents_AllUnresolvedIsNoContentFound(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsScenario)
	rec := s.contents(`{"urls":["https://example.test/missing-a","https://example.test/missing-b"]}`)

	require.Equal(t, http.StatusBadRequest, rec.Code, "D-g: NO_CONTENT_FOUND when no requested identifier resolves")
	var got errorResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, tagNoContentFound, got.Tag)
	assert.Equal(t, messageNoContentFound, got.Error)
}

func TestContents_RequestedResultsNotNamedByTheRequestAreNotRendered(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsScenario)
	rec := s.contents(`{"urls":["https://example.test/report-a"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.NotContains(t, body, "Report B",
		"a projection result the request did not name must not be rendered")
}

// --- statuses overrides (D-a) -------------------------------------------

func TestContents_StatusOverrideReplacesTheDefaultAndDropsTheResult(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-contents-override
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
    contents:
      statuses:
        - id: https://example.test/report-a
          status: error
          error:
            tag: SOURCE_NOT_AVAILABLE
            http_status_code: 403
`)
	rec := s.contents(`{"urls":["https://example.test/report-a"]}`)
	require.Equal(t, http.StatusOK, rec.Code, "an override does not change the response's own HTTP status")

	var got contentsResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Empty(t, got.Results, "an override forcing status: error must drop the rendered result")
	require.Len(t, got.Statuses, 1)
	assert.Equal(t, "error", got.Statuses[0].Status)
	require.NotNil(t, got.Statuses[0].Error)
	assert.Equal(t, "SOURCE_NOT_AVAILABLE", got.Statuses[0].Error.Tag)
	require.NotNil(t, got.Statuses[0].Error.HTTPStatusCode)
	assert.Equal(t, 403, *got.Statuses[0].Error.HTTPStatusCode)
}

func TestContents_StatusOverrideNamingAnUnrequestedIdentifierWarnsAndIsNotRendered(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-contents-override-unrequested
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
    contents:
      statuses:
        - id: https://example.test/never-requested
          status: error
`)
	rec := s.contents(`{"urls":["https://example.test/report-a"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var got contentsResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Statuses, 1, "an override for an identifier the request never sent is not rendered")
	assert.Equal(t, "https://example.test/report-a", got.Statuses[0].ID)
	assert.Equal(t, journal.SeverityWarning, s.findingSeverity(codeContentsStatusUnrequested))
}

// TestContents_StatusOverrideForcingErrorWithNoErrorBlockFallsBackToCrawlNotFound
// pins the fallback applyOneContentsOverride now applies: an override that
// forces a non-success status without naming its own error must not render
// the out-of-enum tag "" — it falls back to the same CRAWL_NOT_FOUND/404 an
// unresolved identifier gets.
func TestContents_StatusOverrideForcingErrorWithNoErrorBlockFallsBackToCrawlNotFound(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-contents-override-no-error-block
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
    contents:
      statuses:
        - id: https://example.test/report-a
          status: error
`)
	rec := s.contents(`{"urls":["https://example.test/report-a"]}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var got contentsResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Statuses, 1)
	require.NotNil(t, got.Statuses[0].Error, "a forced error status must never render a nil error block")
	assert.Equal(t, "CRAWL_NOT_FOUND", got.Statuses[0].Error.Tag,
		"an omitted error block falls back to the same default an unresolved identifier gets, "+
			"not the out-of-enum empty string")
	require.NotNil(t, got.Statuses[0].Error.HTTPStatusCode)
	assert.Equal(t, 404, *got.Statuses[0].Error.HTTPStatusCode)
}

// TestContents_StatusOverrideAppliesToEveryDuplicateIdentifier pins the fix to
// applyContentsOverrides: a requested identifier sent twice gets its override
// applied to BOTH resolutions, keeping results[] and statuses[] in agreement
// the way the code's own invariant comment requires.
func TestContents_StatusOverrideAppliesToEveryDuplicateIdentifier(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-contents-override-duplicate
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
    contents:
      statuses:
        - id: https://example.test/report-a
          status: error
`)
	rec := s.contents(`{"urls":["https://example.test/report-a","https://example.test/report-a"]}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var got contentsResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Statuses, 2, "one statuses[] entry per requested identifier, duplicates included")
	assert.Empty(t, got.Results, "the override forces both duplicate resolutions to error, so no result renders")
	for i, st := range got.Statuses {
		assert.Equal(t, "error", st.Status, "statuses[%d] must agree with results[] being empty", i)
	}
}

func TestContents_StatusOverrideCanAddASourceValue(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-contents-override-source
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
    contents:
      statuses:
        - id: https://example.test/report-a
          source: cached
`)
	rec := s.contents(`{"urls":["https://example.test/report-a"]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"source":"cached"`)
}

// --- request validation --------------------------------------------------

func TestValidateContents_Rejections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		code string
	}{
		{"neither ids nor urls", `{}`, codeContentsTargetMissing},
		{"ids not an array", `{"ids":"doc-a"}`, codeFieldType},
		{"ids empty", `{"ids":[]}`, codeContentsItemsRange},
		{"urls element not a string", `{"urls":[1]}`, codeContentsItemInvalid},
		{"urls element empty string", `{"urls":[""]}`, codeContentsItemInvalid},
		{"compliance outside the enum", `{"urls":["u"],"compliance":"soc2"}`, codeContentsComplianceInvalid},
		{"text wrong type", `{"urls":["u"],"text":"yes"}`, codeFieldType},
		{"text.verbosity outside the enum", `{"urls":["u"],"text":{"verbosity":"terse"}}`, codeContentsVerbosityInvalid},
		{
			"text.includeSections outside the enum",
			`{"urls":["u"],"text":{"includeSections":["intro"]}}`,
			codeContentsSectionInvalid,
		},
		{"highlights wrong type", `{"urls":["u"],"highlights":"yes"}`, codeFieldType},
		{"summary wrong type", `{"urls":["u"],"summary":42}`, codeFieldType},
		{"livecrawl outside the enum", `{"urls":["u"],"livecrawl":"sometimes"}`, codeContentsLivecrawlInvalid},
		{"maxAgeHours below range", `{"urls":["u"],"maxAgeHours":-2}`, codeContentsMaxAgeHoursRange},
		{"maxAgeHours above range", `{"urls":["u"],"maxAgeHours":721}`, codeContentsMaxAgeHoursRange},
		{"maxAgeHours fractional", `{"urls":["u"],"maxAgeHours":1.5}`, codeFieldType},
		{"subpageTarget wrong type", `{"urls":["u"],"subpageTarget":42}`, codeFieldType},
		{"subpageTarget array element wrong type", `{"urls":["u"],"subpageTarget":[1]}`, codeFieldType},
		{"context wrong type", `{"urls":["u"],"context":"yes"}`, codeFieldType},
		{"extras wrong type", `{"urls":["u"],"extras":"yes"}`, codeFieldType},
		{"extras.links wrong type", `{"urls":["u"],"extras":{"links":"many"}}`, codeFieldType},
		{"extras.links below range", `{"urls":["u"],"extras":{"links":-1}}`, codeContentsExtrasRange},
		{"extras.links above range", `{"urls":["u"],"extras":{"links":1001}}`, codeContentsExtrasRange},
		{"livecrawlTimeout wrong type", `{"urls":["u"],"livecrawlTimeout":true}`, codeFieldType},
		{"livecrawlTimeout below range", `{"urls":["u"],"livecrawlTimeout":0}`, codeContentsTimeoutRange},
		{"livecrawlTimeout above range", `{"urls":["u"],"livecrawlTimeout":90001}`, codeContentsTimeoutRange},
		{"subpages wrong type", `{"urls":["u"],"subpages":"many"}`, codeFieldType},
		{"subpages below range", `{"urls":["u"],"subpages":-1}`, codeContentsSubpagesRange},
		{"subpages above range", `{"urls":["u"],"subpages":101}`, codeContentsSubpagesRange},
		{"text.maxCharacters wrong type", `{"urls":["u"],"text":{"maxCharacters":"lots"}}`, codeFieldType},
		{"text.maxCharacters below range", `{"urls":["u"],"text":{"maxCharacters":0}}`, codeContentsCharactersRange},
		{"text.includeHtmlTags wrong type", `{"urls":["u"],"text":{"includeHtmlTags":"yes"}}`, codeFieldType},
		{"highlights.query wrong type", `{"urls":["u"],"highlights":{"query":42}}`, codeFieldType},
		{
			"highlights.maxCharacters above range",
			`{"urls":["u"],"highlights":{"maxCharacters":10001}}`,
			codeContentsCharactersRange,
		},
		{"summary.query wrong type", `{"urls":["u"],"summary":{"query":42}}`, codeFieldType},
		{"summary.schema wrong type", `{"urls":["u"],"summary":{"schema":"not an object"}}`, codeFieldType},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, contentsScenario)
			rec := s.contents(tc.body)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
			assert.Equal(t, journal.SeverityError, s.findingSeverity(tc.code), "expected finding %s", tc.code)

			var got errorResponseWire
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.NotEmpty(t, got.Error)
			assert.NotEmpty(t, got.Tag)
		})
	}
}

func TestValidateContents_InvalidItemUsesTheInvalidURLsTag(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsScenario)
	rec := s.contents(`{"urls":[""]}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var got errorResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, tagInvalidURLs, got.Tag)
	assert.Equal(t, messageInvalidURLs, got.Error,
		"the wire message is the vendor's fixed text, not the finding's own per-element reason")
}

func TestValidateContents_BothIDsAndURLsIsAWarningNotAnError(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsScenario)
	rec := s.contents(`{"ids":["https://example.test/report-a"],"urls":["https://example.test/report-b"]}`)

	require.Equal(t, http.StatusOK, rec.Code,
		"the docs say one-of; rejecting both present would be inventing a rule the docs do not state")
	assert.Equal(t, journal.SeverityWarning, s.findingSeverity(codeContentsIDsAndURLs))

	var got contentsResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Statuses, 2, "both ids and urls elements are resolved, ids first then urls")
	assert.Equal(t, "https://example.test/report-a", got.Statuses[0].ID)
	assert.Equal(t, "https://example.test/report-b", got.Statuses[1].ID)
}

// TestValidateContents_NullIsAcceptedOnEveryOptionalField is D-d made
// assertable: every optional /contents field is documented anyOf[<type>, null]
// and an explicit JSON null must not fail validation.
func TestValidateContents_NullIsAcceptedOnEveryOptionalField(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsScenario)
	rec := s.contents(`{
		"urls": ["https://example.test/report-a"],
		"compliance": null,
		"text": null,
		"highlights": null,
		"summary": null,
		"extras": null,
		"maxAgeHours": null,
		"livecrawl": null,
		"livecrawlTimeout": null,
		"subpages": null,
		"subpageTarget": null,
		"context": null
	}`)
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}

func TestValidateContents_DeprecatedFieldsAreAcceptedAndFlagged(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		code string
	}{
		{"livecrawl", `{"urls":["https://example.test/report-a"],"livecrawl":"always"}`, codeLivecrawlDeprecated},
		{"context", `{"urls":["https://example.test/report-a"],"context":true}`, codeContextDeprecated},
		{"highlights.numSentences", `{"urls":["https://example.test/report-a"],"highlights":{"numSentences":3}}`, codeNumSentencesDeprecated},
		{
			"highlights.highlightsPerUrl",
			`{"urls":["https://example.test/report-a"],"highlights":{"highlightsPerUrl":2}}`,
			codeHighlightsPerURLDeprecated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, contentsScenario)
			rec := s.do(request{path: "/contents", body: tc.body})

			require.Equal(t, http.StatusOK, rec.Code, "a deprecated field must never fail the request")
			assert.Equal(t, journal.SeverityWarning, s.findingSeverity(tc.code))
		})
	}
}

func TestValidateContents_AcceptsBothSummaryForms(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`{"urls":["https://example.test/report-a"],"summary":true}`,
		`{"urls":["https://example.test/report-a"],"summary":{"query":"what changed?"}}`,
	} {
		s := newSim(t, contentsScenario)
		rec := s.contents(body)
		assert.Equal(t, http.StatusOK, rec.Code,
			"D-c: the OpenAPI spec and the guide disagree on summary's type, so both forms are accepted")
	}
}

func TestValidateContents_UnknownFieldIsAWarningNotAnError(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsScenario)
	rec := s.contents(`{"urls":["https://example.test/report-a"],"someFutureField":1}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, journal.SeverityWarning, s.findingSeverity(codeUnknownField))
}

func TestValidateContents_ItemsCountBoundIsEnforced(t *testing.T) {
	t.Parallel()

	oversized := `["u"` + strings.Repeat(`,"u"`, maxContentsItems) + `]`
	s := newSim(t, contentsScenario)
	rec := s.contents(`{"urls":` + oversized + `}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, journal.SeverityError, s.findingSeverity(codeContentsItemsRange))
}

// --- faults and lifecycle -------------------------------------------------

func TestContents_FaultBudgetIsIndependentOfSearchAndAnswer(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-contents-fault
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
        attempts:
          - status: 503
`)
	assert.Equal(t, http.StatusOK, s.search(`{"query":"report a"}`).Code,
		"/search must stay healthy while /contents is faulted")

	rec := s.contents(`{"urls":["https://example.test/report-a"]}`)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestContents_RequestIDIsDistinctPerCall(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsScenario)
	first := requestIDOf(t, s.contents(`{"urls":["https://example.test/report-a"]}`).Body.Bytes())
	second := requestIDOf(t, s.contents(`{"urls":["https://example.test/report-a"]}`).Body.Bytes())

	assert.Regexp(t, hex32, first)
	assert.NotEqual(t, first, second)

	search := requestIDOf(t, s.search(`{"query":"report a"}`).Body.Bytes())
	assert.NotEqual(t, first, search, "the two routes derive from different budget keys")
}

func TestContents_401NoCredential(t *testing.T) {
	t.Parallel()

	s := newSim(t, contentsScenario)
	rec := s.do(request{
		path: "/contents", body: `{"urls":["https://example.test/report-a"]}`, noAuth: true,
	})

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var got errorResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, tagInvalidAPIKey, got.Tag)
}
