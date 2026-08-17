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

// TestValidateSearch_Rejections covers the request checks that must produce a
// 400 in Exa's own envelope. A consumer must be able to prove it sent the
// correct vendor request, not merely that it got a response.
func TestValidateSearch_Rejections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		code string
	}{
		{name: "query missing", body: `{}`, code: codeQueryMissing},
		{name: "query wrong type", body: `{"query":42}`, code: codeQueryMissing},
		{name: "query empty", body: `{"query":"   "}`, code: codeQueryEmpty},
		{name: "type outside the enum", body: `{"query":"a","type":"slow"}`, code: codeTypeInvalid},
		{name: "legacy neural type", body: `{"query":"a","type":"neural"}`, code: codeTypeLegacyNeural},
		{name: "numResults below range", body: `{"query":"a","numResults":0}`, code: codeNumResultsRange},
		{name: "numResults above range", body: `{"query":"a","numResults":101}`, code: codeNumResultsRange},
		{name: "numResults fractional", body: `{"query":"a","numResults":1.5}`, code: codeFieldType},
		{name: "category outside the enum", body: `{"query":"a","category":"blog"}`, code: codeCategoryInvalid},
		{name: "includeDomains not an array", body: `{"query":"a","includeDomains":"example.test"}`, code: codeFieldType},
		{name: "contents not an object", body: `{"query":"a","contents":true}`, code: codeFieldType},
		{name: "outputSchema not an object", body: `{"query":"a","outputSchema":"{}"}`, code: codeFieldType},
		{name: "stream not a boolean", body: `{"query":"a","stream":"yes"}`, code: codeFieldType},
		{
			name: "outputSchema too deeply nested",
			body: `{"query":"a","outputSchema":{"properties":{"x":{"properties":{"y":{"properties":` +
				`{"z":{"type":"string"}}}}}}}}`,
			code: codeOutputSchemaDepth,
		},
		{
			name: "outputSchema declares too many properties",
			body: `{"query":"a","outputSchema":{"properties":{"a":{},"b":{},"c":{},"d":{},"e":{},` +
				`"f":{},"g":{},"h":{},"i":{},"j":{},"k":{}}}}`,
			code: codeOutputSchemaProperties,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, minimalScenario)
			rec := s.search(tc.body)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, journal.SeverityError, s.findingSeverity(tc.code), "expected finding %s", tc.code)

			var got errorResponseWire
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.NotEmpty(t, got.Error)
			assert.NotEmpty(t, got.Tag)
		})
	}
}

// TestValidateSearch_TypeEnumErrorIsTheVendorsOwnMessage pins the one verbatim
// error body Exa's documentation gives. It doubles as proof of the exact enum, so
// paraphrasing it would break a consumer asserting on the vendor's text.
func TestValidateSearch_TypeEnumErrorIsTheVendorsOwnMessage(t *testing.T) {
	t.Parallel()

	s := newSim(t, minimalScenario)
	rec := s.search(`{"query":"report a","type":"slow"}`)

	var got errorResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, tagInvalidRequestBody, got.Tag)
	assert.Equal(t,
		"Invalid request body | Validation error: Invalid enum value. Expected 'auto' | 'fast' | 'instant' | "+
			"'deep-lite' | 'deep' | 'deep-reasoning', received 'slow' at \"type\"",
		got.Error)
}

func TestValidateSearch_AcceptsEveryDocumentedEnumMember(t *testing.T) {
	t.Parallel()

	for _, searchType := range searchTypes {
		t.Run("type "+searchType, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, minimalScenario)
			rec := s.search(`{"query":"report a","type":"` + searchType + `"}`)
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
	for _, category := range categories {
		t.Run("category "+category, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, minimalScenario)
			rec := s.search(`{"query":"report a","category":"` + category + `"}`)
			assert.Equal(t, http.StatusOK, rec.Code,
				"two members contain a space; a naive enum split would reject them")
		})
	}
}

func TestValidateSearch_DomainListBoundsAreEnforced(t *testing.T) {
	t.Parallel()

	oversized := `["a.test"` + strings.Repeat(`,"a.test"`, maxIncludeDomains) + `]`

	for _, field := range []string{"includeDomains", "excludeDomains"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, minimalScenario)
			rec := s.search(`{"query":"a","` + field + `":` + oversized + `}`)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

// TestValidateSearch_DeprecatedFieldsAreAcceptedAndFlagged is the plan's
// accept-and-flag rule made assertable. Rejecting these would fail a request the
// real API serves; swallowing them silently would leave an adapter emitting
// fields documented as having no effect.
func TestValidateSearch_DeprecatedFieldsAreAcceptedAndFlagged(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		code string
	}{
		{"useAutoprompt", `{"query":"a","useAutoprompt":true}`, codeUseAutopromptDeprecated},
		{"livecrawl", `{"query":"a","livecrawl":"always"}`, codeLivecrawlDeprecated},
		{"startCrawlDate", `{"query":"a","startCrawlDate":"2026-01-01"}`, codeStartCrawlDateDeprecated},
		{"endCrawlDate", `{"query":"a","endCrawlDate":"2026-01-02"}`, codeEndCrawlDateDeprecated},
		{"context", `{"query":"a","context":true}`, codeContextDeprecated},
		{
			"numSentences",
			`{"query":"a","contents":{"highlights":{"numSentences":3}}}`,
			codeNumSentencesDeprecated,
		},
		{
			"highlightsPerUrl",
			`{"query":"a","contents":{"highlights":{"highlightsPerUrl":2}}}`,
			codeHighlightsPerURLDeprecated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, minimalScenario)
			rec := s.search(tc.body)

			require.Equal(t, http.StatusOK, rec.Code, "a deprecated field must never fail the request")
			assert.Equal(t, journal.SeverityWarning, s.findingSeverity(tc.code))
		})
	}
}

func TestValidateSearch_UnknownFieldIsAWarningNotAnError(t *testing.T) {
	t.Parallel()

	s := newSim(t, minimalScenario)
	rec := s.search(`{"query":"a","someFutureField":1}`)

	require.Equal(t, http.StatusOK, rec.Code,
		"being stricter than the vendor makes a consumer fail here and pass in production")
	assert.Equal(t, journal.SeverityWarning, s.findingSeverity(codeUnknownField))
}

// TestValidateSearch_FindingOrderIsStableAcrossRuns guards the property §3.3
// calls out: findings are produced by walking a map, and an unsorted walk makes
// the journal — and any body built from it — differ run to run.
func TestValidateSearch_FindingOrderIsStableAcrossRuns(t *testing.T) {
	t.Parallel()

	body := `{"query":"a","alpha":1,"zulu":2,"mike":3,"bravo":4,"tango":5}`

	s := newSim(t, minimalScenario)
	var first []string
	for run := range 20 {
		s.search(body)
		var codes []string
		for _, f := range s.findings() {
			codes = append(codes, f.Code+"/"+f.Field)
		}
		if run == 0 {
			first = codes
			continue
		}
		assert.Equal(t, first, codes, "finding order must not depend on Go's map iteration")
	}
	assert.NotEmpty(t, first)
}

func TestValidate_ContentTypeIsAWarningByDefaultAndPromotable(t *testing.T) {
	t.Parallel()

	t.Run("warns", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, minimalScenario)
		rec := s.do(request{
			path:    "/search",
			body:    `{"query":"a"}`,
			headers: map[string]string{"Content-Type": "text/plain"},
		})
		require.Equal(t, http.StatusOK, rec.Code,
			"no simulated vendor documents a 415, so inventing one would be inventing vendor behaviour")
		assert.Equal(t, journal.SeverityWarning, s.findingSeverity(codeContentType))
	})

	t.Run("promoted by the scenario", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, `
version: 1
name: exa-strict-content-type
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    validation:
      promote: [request.content_type]
    results:
      - source: source-a
`)
		rec := s.do(request{
			path:    "/search",
			body:    `{"query":"a"}`,
			headers: map[string]string{"Content-Type": "text/plain"},
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestValidate_MalformedBodyIsRejectedInExasEnvelope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"not JSON", `{"query":`},
		{"not an object", `["report a"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, minimalScenario)
			rec := s.search(tc.body)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			var got errorResponseWire
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, tagInvalidRequestBody, got.Tag)
		})
	}
}

func TestValidate_StreamPolicyDefaultsToWarnAndCanReject(t *testing.T) {
	t.Parallel()

	t.Run("warn", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, minimalScenario)
		rec := s.search(`{"query":"a","stream":true}`)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
			"the ordinary JSON body is returned; streaming is a plan non-goal")
		assert.Equal(t, journal.SeverityWarning, s.findingSeverity(codeStreamUnimplemented))
	})

	t.Run("reject", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, `
version: 1
name: exa-stream-reject
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    stream: reject
    results:
      - source: source-a
`)
		rec := s.search(`{"query":"a","stream":true}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, journal.SeverityError, s.findingSeverity(codeStreamUnimplemented))
	})

	t.Run("stream false is silent", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, minimalScenario)
		require.Equal(t, http.StatusOK, s.search(`{"query":"a","stream":false}`).Code)
		assert.False(t, s.hasFinding(codeStreamUnimplemented))
	})
}

// --- /answer diverges from /search ------------------------------------------

// TestValidateAnswer_DoesNotShareTheSearchDecoder is the divergence the contract
// warns about. /answer's schema has four properties; a shared decoder would
// wrongly accept contents{} and a `text` object here.
func TestValidateAnswer_DoesNotShareTheSearchDecoder(t *testing.T) {
	t.Parallel()

	t.Run("search-only fields are flagged", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, minimalScenario)
		rec := s.answer(`{"query":"a","numResults":5,"includeDomains":["a.test"],"contents":{"text":true}}`)

		require.Equal(t, http.StatusOK, rec.Code)
		var flagged []string
		for _, f := range s.findings() {
			if f.Code == codeUnknownField {
				flagged = append(flagged, f.Field)
			}
		}
		assert.ElementsMatch(t, []string{"contents", "includeDomains", "numResults"}, flagged)
	})

	t.Run("text must be a boolean", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, minimalScenario)
		rec := s.answer(`{"query":"a","text":{"maxCharacters":100}}`)

		require.Equal(t, http.StatusBadRequest, rec.Code,
			"on /search the equivalent is contents.text and accepts an object; on /answer it does not")
		assert.Equal(t, journal.SeverityError, s.findingSeverity(codeFieldType))
	})

	t.Run("sdk-only fields are accepted and not echoed", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, minimalScenario)
		rec := s.answer(`{"query":"a","model":"exa","systemPrompt":"be brief","userLocation":"US"}`)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.False(t, s.hasFinding(codeUnknownField),
			"the TypeScript SDK sends these, so they are accepted and ignored")
		assert.NotContains(t, rec.Body.String(), "systemPrompt")
	})

	t.Run("query is required here too", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, minimalScenario)
		rec := s.answer(`{"text":true}`)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, journal.SeverityError, s.findingSeverity(codeQueryMissing))
	})
}

// TestValidate_ScenarioCanDemoteAnError proves the policy runs in one place: the
// same request that fails by default succeeds when the scenario demotes its code.
func TestValidate_ScenarioCanDemoteAnError(t *testing.T) {
	t.Parallel()

	s := newSim(t, `
version: 1
name: exa-demoted
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    validation:
      demote: [exa.numResults.range]
    results:
      - source: source-a
`)
	rec := s.search(`{"query":"a","numResults":0}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, journal.SeverityWarning, s.findingSeverity(codeNumResultsRange))
}
