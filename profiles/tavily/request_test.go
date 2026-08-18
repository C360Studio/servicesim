package tavily

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
)

// validationScenario is the smallest scenario that renders a 200, so that every
// test below is about the request rather than about the projection.
const validationScenario = `
version: 1
name: validation
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily:
    answer: A short synthesis of Report A.
    response_time: 1.15
    results:
      - source: source-a
        score: 0.98
`

// TestRequestValidation walks the §6.3 table. Being strict about requests is
// the point of the journal: a consumer must be able to prove it sent the
// correct vendor request, not merely that it got a response.
func TestRequestValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{
			name: "a well-formed request is accepted", body: `{"query":"report a"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "query is required", body: `{"topic":"news"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeQueryMissing,
			wantDetail: "Missing query. 'query' is a required string.",
		},
		{
			name: "an empty query is a missing query", body: `{"query":"   "}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeQueryMissing,
		},
		{
			name: "a non-string query is a missing query", body: `{"query":7}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeQueryMissing,
		},
		{
			name: "search_depth fast is valid", body: `{"query":"a","search_depth":"fast"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "search_depth ultra-fast is valid", body: `{"query":"a","search_depth":"ultra-fast"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "search_depth outside the four-value enum", body: `{"query":"a","search_depth":"deep"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeSearchDepthInvalid,
			wantDetail: "Invalid search_depth. Must be 'advanced', 'basic', 'fast' or 'ultra-fast'.",
		},
		{
			name: "topic finance is valid", body: `{"query":"a","topic":"finance"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "topic outside the three-value enum", body: `{"query":"a","topic":"sports"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeTopicInvalid,
			wantDetail: "Invalid topic. Must be 'general', 'news' or 'finance'.",
		},
		{
			name: "max_results above the range", body: `{"query":"a","max_results":21}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeMaxResultsRange,
			wantDetail: "Invalid max_results. Must be an integer between 0 and 20.",
		},
		{
			name: "max_results below the range", body: `{"query":"a","max_results":-1}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeMaxResultsRange,
		},
		{
			name: "a fractional max_results is not an integer", body: `{"query":"a","max_results":2.5}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeMaxResultsRange,
		},
		{
			name: "chunks_per_source above the range", body: `{"query":"a","chunks_per_source":4}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeChunksPerSourceRange,
		},
		{
			name: "include_answer accepts a boolean", body: `{"query":"a","include_answer":true}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "include_answer accepts advanced", body: `{"query":"a","include_answer":"advanced"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "include_answer rejects another string", body: `{"query":"a","include_answer":"deep"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeIncludeAnswerInvalid,
			wantDetail: "Invalid include_answer. Must be a boolean or 'basic' or 'advanced'.",
		},
		{
			name: "include_raw_content accepts markdown", body: `{"query":"a","include_raw_content":"markdown"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "include_raw_content rejects another string", body: `{"query":"a","include_raw_content":"html"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeIncludeRawContentInvalid,
		},
		{
			name: "time_range accepts a single-letter alias", body: `{"query":"a","time_range":"w"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "time_range outside the enum", body: `{"query":"a","time_range":"fortnight"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeTimeRangeInvalid,
		},
		{
			name: "include_domains beyond the limit", body: domainsBody("include_domains", includeDomainsMax+1),
			wantStatus: http.StatusBadRequest, wantCode: CodeIncludeDomainsMax,
		},
		{
			name: "include_domains at the limit", body: domainsBody("include_domains", includeDomainsMax),
			wantStatus: http.StatusOK,
		},
		{
			name:       "exclude_domains has the lower asymmetric limit",
			body:       domainsBody("exclude_domains", excludeDomainsMax+1),
			wantStatus: http.StatusBadRequest, wantCode: CodeExcludeDomainsMax,
		},
		{
			name: "a boolean field of the wrong type", body: `{"query":"a","include_images":"yes"}`,
			wantStatus: http.StatusBadRequest, wantCode: "tavily.include_images.invalid",
			wantDetail: "Invalid include_images. Must be a boolean.",
		},
		{
			name: "a string field of the wrong type", body: `{"query":"a","country":7}`,
			wantStatus: http.StatusBadRequest, wantCode: "tavily.country.invalid",
		},
		{
			name: "a domain list of the wrong type", body: `{"query":"a","include_domains":"example.test"}`,
			wantStatus: http.StatusBadRequest, wantCode: "tavily.include_domains.invalid",
		},
		{
			name: "a body that is not JSON at all", body: `{`,
			wantStatus: http.StatusBadRequest, wantCode: provider.CodeMalformedJSON,
		},
		{
			name: "a body that is not a JSON object", body: `[1,2,3]`,
			wantStatus: http.StatusBadRequest, wantCode: provider.CodeBodyNotObject,
		},
		{
			name: "an absent body", body: ``,
			wantStatus: http.StatusBadRequest, wantCode: CodeQueryMissing,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ring := journal.NewRing(8, 1<<16)
			rec := post(t, newHandler(t, validationScenario, ring), "/search", tc.body, bearer)

			require.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantCode != "" {
				require.Contains(t, findingCodes(t, ring), tc.wantCode)
			}
			if tc.wantDetail != "" {
				require.Equal(t, tc.wantDetail, errorDetail(t, rec))
			}
		})
	}
}

// TestRequestWarnings covers the findings that are journal-only. A deprecated
// or unmodelled field must not break a request — the real API accepts it — but
// it must be visible, which is the whole point of the split.
func TestRequestWarnings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		headers  map[string]string
		wantCode string
	}{
		{
			name: "days was removed from the schema", body: `{"query":"a","days":7}`,
			wantCode: CodeDaysRemoved,
		},
		{
			name: "country applies only to the general topic",
			body: `{"query":"a","country":"france","topic":"news"}`, wantCode: CodeCountryRequiresGeneralTopic,
		},
		{
			name: "an unmodelled field is flagged, not rejected",
			body: `{"query":"a","rerank_model":"v2"}`, wantCode: CodeUnknownField,
		},
		{
			name: "a body that does not declare JSON", body: `{"query":"a"}`,
			headers: map[string]string{"Content-Type": "text/plain"}, wantCode: CodeContentType,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ring := journal.NewRing(8, 1<<16)
			handler := newHandler(t, validationScenario, ring)

			req := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", bearer)
			for name, value := range tc.headers {
				req.Header.Set(name, value)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code, "a warning must not change the response")
			require.Contains(t, findingCodes(t, ring), tc.wantCode)
		})
	}
}

// TestValidationPolicyPromotesAWarning proves the scenario can reshape the
// mapping without any code change, which is what §6.1 promises.
func TestValidationPolicyPromotesAWarning(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: strict
providers:
  tavily:
    validation:
      promote: [request.unknown_field]
    response_time: 1.15
`
	rec := post(t, newHandler(t, src, nil), "/search", `{"query":"a","rerank_model":"v2"}`, bearer)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, errorDetail(t, rec), "rerank_model")
}

// TestAPIKeyFindingDoesNotQuoteValue is the finding whose entire subject is a
// credential. Its message states presence and nothing else, because it reaches
// the journal, the admin API and the process log — and the credential must not
// survive the round trip by any path, including the retained request body.
func TestAPIKeyFindingDoesNotQuoteValue(t *testing.T) {
	t.Parallel()

	const secret = "tvly-secret-value-do-not-leak"

	ring := journal.NewRing(8, 1<<16)
	rec := post(t, newHandler(t, validationScenario, ring),
		"/search", `{"query":"a","api_key":"`+secret+`"}`, bearer)

	// An api_key in the body authenticates and is flagged, never rejected; this
	// request presented a Bearer credential as well.
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, findingCodes(t, ring), CodeAPIKeyInBody)

	entries := ring.Snapshot()
	require.Len(t, entries, 1)

	serialized, err := json.Marshal(entries[0])
	require.NoError(t, err)
	require.NotContains(t, string(serialized), secret,
		"a credential must not reach a retained structure by any path")
	require.NotContains(t, rec.Body.String(), secret)
}

// TestBodyAPIKeyAuthenticates is the adopter-reported defect made assertable.
// A client that sends its key as a body property on POSTs — which the real
// Tavily API accepts — was answered 401 auth.missing, which sends the consumer
// hunting a bug in code that works in production.
//
// The three properties asserted together are the whole fix: the request is
// served, the placement stays visible in the journal (both as the auth
// observation and as a warning-severity finding, so AssertNoErrors passes), and
// the credential still does not survive the round trip.
func TestBodyAPIKeyAuthenticates(t *testing.T) {
	t.Parallel()

	const secret = "tvly-body-placed-key"

	ring := journal.NewRing(8, 1<<16)
	rec := post(t, newHandler(t, validationScenario, ring),
		"/search", `{"query":"report a","api_key":"`+secret+`"}`, "")

	require.Equal(t, http.StatusOK, rec.Code, "a body-placed api_key must authenticate")

	entries := ring.Snapshot()
	require.Len(t, entries, 1)
	entry := entries[0]

	require.Empty(t, entry.Errors(),
		"testkit.AssertNoErrors must pass for a request authenticated by its body key")

	placement := findingByCode(t, entry, CodeAPIKeyInBody)
	require.Equal(t, journal.SeverityWarning, placement.Severity,
		"the placement is an observation, not a rejection")
	require.NotContains(t, placement.Message, secret)

	require.True(t, entry.Auth.Present, "the journal must record that a credential arrived")
	require.Equal(t, placementBodyAPIKey, entry.Auth.Header,
		"a consumer asserts on the placement its adapter used")
	require.NotEmpty(t, entry.Auth.Fingerprint)

	serialized, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), secret,
		"accepting the body placement must not weaken redaction")
	require.NotContains(t, rec.Body.String(), secret)
}

// TestBothCredentialPlacementsAreRecorded pins what a consumer asserting "my
// adapter sent the credential the way we intended" reads. Presenting both is
// accepted; the header — the placement the vendor documents — keeps the scalar
// auth observation, and BOTH appear in Auth.Placements.
//
// Handle's header scan cannot see a body-placed key, so the body placement is
// added by this package after the body is decoded. Before Placements existed the
// second credential was visible only as a side effect of a finding, which meant
// "my client sends exactly one credential" could not be asserted directly.
func TestBothCredentialPlacementsAreRecorded(t *testing.T) {
	t.Parallel()

	ring := journal.NewRing(8, 1<<16)
	rec := post(t, newHandler(t, validationScenario, ring),
		"/search", `{"query":"report a","api_key":"tvly-body-key"}`, bearer)

	require.Equal(t, http.StatusOK, rec.Code)

	entries := ring.Snapshot()
	require.Len(t, entries, 1)
	entry := entries[0]

	require.Empty(t, entry.Errors())
	require.Equal(t, "authorization", entry.Auth.Header)
	require.Equal(t, "Bearer", entry.Auth.Scheme)
	require.Contains(t, findingCodes(t, ring), CodeAPIKeyInBody)

	require.Len(t, entry.Auth.Placements, 2,
		"a request sending two credentials must journal two placements")
	placements := make([]string, 0, 2)
	for _, p := range entry.Auth.Placements {
		placements = append(placements, p.Header)
	}
	require.Equal(t, []string{"authorization", placementBodyAPIKey}, placements,
		"the header is observed by Handle, the body placement added after decoding")
}

// A body-placed key on its own is one placement, not two, and it is the primary.
// This is the shape the first adopter's real client produces.
func TestBodyOnlyCredentialIsASinglePlacement(t *testing.T) {
	t.Parallel()

	ring := journal.NewRing(8, 1<<16)
	rec := post(t, newHandler(t, validationScenario, ring),
		"/search", `{"query":"report a","api_key":"tvly-body-key"}`, "")

	require.Equal(t, http.StatusOK, rec.Code)

	entry := ring.Snapshot()[0]
	require.Len(t, entry.Auth.Placements, 1)
	require.Equal(t, placementBodyAPIKey, entry.Auth.Placements[0].Header)
	require.Equal(t, placementBodyAPIKey, entry.Auth.Header,
		"with no header credential, the body placement is the primary observation")
	require.Equal(t, entry.Auth.Placements[0].Fingerprint, entry.Auth.Fingerprint)
}

// TestAuthentication covers §6.4: either the documented Authorization: Bearer
// header or a body api_key authenticates, an x-api-key is seen and journaled
// without authenticating, and the scenario's AuthPolicy overrides all of it.
func TestAuthentication(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// body defaults to a minimal valid search when empty, so a case that is
		// about a header does not have to restate one.
		body       string
		scenario   string
		headers    map[string]string
		wantStatus int
		wantCodes  []string
	}{
		{
			name: "a bearer credential authenticates", scenario: validationScenario,
			headers: map[string]string{"Authorization": bearer}, wantStatus: http.StatusOK,
		},
		{
			name: "no credential is rejected", scenario: validationScenario,
			wantStatus: http.StatusUnauthorized, wantCodes: []string{CodeAuthMissing},
		},
		{
			name: "a body api_key authenticates with no header at all", scenario: validationScenario,
			body:       `{"query":"report a","api_key":"tvly-test-key"}`,
			wantStatus: http.StatusOK, wantCodes: []string{CodeAPIKeyInBody},
		},
		{
			name: "both placements together authenticate", scenario: validationScenario,
			body:       `{"query":"report a","api_key":"tvly-test-key"}`,
			headers:    map[string]string{"Authorization": bearer},
			wantStatus: http.StatusOK, wantCodes: []string{CodeAPIKeyInBody},
		},
		{
			name: "a blank body api_key is not a credential", scenario: validationScenario,
			body:       `{"query":"report a","api_key":"   "}`,
			wantStatus: http.StatusUnauthorized, wantCodes: []string{CodeAuthMissing},
		},
		{
			name: "a non-string body api_key is not a credential", scenario: validationScenario,
			body:       `{"query":"report a","api_key":7}`,
			wantStatus: http.StatusUnauthorized, wantCodes: []string{CodeAuthMissing},
		},
		{
			name: "an x-api-key is seen but does not authenticate", scenario: validationScenario,
			headers:    map[string]string{"x-api-key": "tvly-test-key"},
			wantStatus: http.StatusUnauthorized,
			wantCodes:  []string{CodeAuthWrongHeader, CodeAuthMissing},
		},
		{
			name: "a non-Bearer scheme is flagged and still authenticates", scenario: validationScenario,
			headers:    map[string]string{"Authorization": "Basic dXNlcjpwYXNz"},
			wantStatus: http.StatusOK, wantCodes: []string{CodeAuthWrongPlacement},
		},
		{
			name: "auth mode optional accepts no credential",
			scenario: `
version: 1
name: optional
providers:
  tavily:
    auth:
      mode: optional
    response_time: 1.15
`,
			wantStatus: http.StatusOK,
		},
		{
			name: "auth mode reject refuses a valid credential",
			scenario: `
version: 1
name: unauthorized
providers:
  tavily:
    auth:
      mode: reject
    response_time: 1.15
`,
			headers:    map[string]string{"Authorization": bearer},
			wantStatus: http.StatusUnauthorized, wantCodes: []string{CodeAuthMismatch},
		},
		{
			name: "expect_key rejects the wrong key",
			scenario: `
version: 1
name: expects
providers:
  tavily:
    auth:
      expect_key: tvly-expected
    response_time: 1.15
`,
			headers:    map[string]string{"Authorization": "Bearer tvly-other"},
			wantStatus: http.StatusUnauthorized, wantCodes: []string{CodeAuthMismatch},
		},
		{
			name: "expect_key accepts the right key",
			scenario: `
version: 1
name: expects
providers:
  tavily:
    auth:
      expect_key: tvly-expected
    response_time: 1.15
`,
			headers:    map[string]string{"Authorization": "Bearer tvly-expected"},
			wantStatus: http.StatusOK,
		},
		{
			name: "expect_key accepts a body-placed key",
			scenario: `
version: 1
name: expects
providers:
  tavily:
    auth:
      expect_key: tvly-expected
    response_time: 1.15
`,
			body:       `{"query":"report a","api_key":"tvly-expected"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "expect_key rejects the wrong body-placed key",
			scenario: `
version: 1
name: expects
providers:
  tavily:
    auth:
      expect_key: tvly-expected
    response_time: 1.15
`,
			body:       `{"query":"report a","api_key":"tvly-other"}`,
			wantStatus: http.StatusUnauthorized, wantCodes: []string{CodeAuthMismatch},
		},
		{
			name: "auth mode reject refuses a body-placed key",
			scenario: `
version: 1
name: unauthorized
providers:
  tavily:
    auth:
      mode: reject
    response_time: 1.15
`,
			body:       `{"query":"report a","api_key":"tvly-test-key"}`,
			wantStatus: http.StatusUnauthorized, wantCodes: []string{CodeAuthMismatch},
		},
		{
			name: "a scenario may narrow the placements back to the header alone",
			scenario: `
version: 1
name: header-only
providers:
  tavily:
    auth:
      headers: [authorization]
    response_time: 1.15
`,
			body:       `{"query":"report a","api_key":"tvly-test-key"}`,
			wantStatus: http.StatusUnauthorized, wantCodes: []string{CodeAuthMissing, CodeAPIKeyInBody},
		},
		{
			name: "a narrowed scenario may still name the body placement",
			scenario: `
version: 1
name: body-only
providers:
  tavily:
    auth:
      headers: ["body:api_key"]
    response_time: 1.15
`,
			body:       `{"query":"report a","api_key":"tvly-test-key"}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "a scenario may widen the accepted placements",
			scenario: `
version: 1
name: widened
providers:
  tavily:
    auth:
      headers: [authorization, x-api-key]
    response_time: 1.15
`,
			headers:    map[string]string{"x-api-key": "tvly-test-key"},
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ring := journal.NewRing(8, 1<<16)
			handler := newHandler(t, tc.scenario, ring)

			body := tc.body
			if body == "" {
				body = `{"query":"report a"}`
			}
			req := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			for name, value := range tc.headers {
				req.Header.Set(name, value)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantStatus == http.StatusUnauthorized {
				require.Equal(t, messageUnauthorized, errorDetail(t, rec))
			}
			codes := findingCodes(t, ring)
			for _, want := range tc.wantCodes {
				require.Contains(t, codes, want)
			}
		})
	}
}

// TestRejectedRequestConsumesNoAttempt is §4.4's rule made assertable: a
// request that failed validation must not spend a retry the consumer's own test
// is counting on.
func TestRejectedRequestConsumesNoAttempt(t *testing.T) {
	t.Parallel()

	ring := journal.NewRing(8, 1<<16)
	handler := newHandler(t, validationScenario, ring)

	rejected := post(t, handler, "/search", `{"query":"a","topic":"sports"}`, bearer)
	require.Equal(t, http.StatusBadRequest, rejected.Code)

	entries := ring.Snapshot()
	require.Len(t, entries, 1)
	require.Equal(t, -1, entries[0].Outcome.AttemptIndex, "no attempt may be claimed by a rejected request")
	require.Equal(t, journal.OutcomeError, entries[0].Outcome.Kind)
}

// --- helpers ----------------------------------------------------------------

// findingCodes returns every finding code recorded for the last journaled
// request.
func findingCodes(t *testing.T, ring *journal.Ring) []string {
	t.Helper()

	entries := ring.Snapshot()
	require.NotEmpty(t, entries)

	last := entries[len(entries)-1]
	codes := make([]string, 0, len(last.Findings))
	for _, f := range last.Findings {
		codes = append(codes, f.Code)
	}
	return codes
}

// findingByCode returns the one finding carrying code, failing the test rather
// than returning a zero value if the entry does not hold it.
func findingByCode(t *testing.T, entry journal.Entry, code string) journal.Finding {
	t.Helper()

	for _, f := range entry.Findings {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no finding with code %q; entry has %v", code, entry.Findings)
	return journal.Finding{}
}

// errorDetail reads the message out of the documented nested envelope, which is
// also a check that the envelope is nested at all.
func errorDetail(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body errorResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Detail.Error
}

// domainsBody builds a request carrying n domains, all under a reserved name.
func domainsBody(field string, n int) string {
	domains := make([]string, 0, n)
	for i := range n {
		domains = append(domains, "example"+strings.Repeat("x", i%3)+".test")
	}
	encoded, err := json.Marshal(map[string]any{"query": "a", field: domains})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
