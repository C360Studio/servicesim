package perplexity

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSonarValidationGolden pins the FastAPI 422 body, which is the one error
// shape the specification schematises.
//
// The comparison is against raw bytes because two things in this body are
// type-sensitive and survive a decode: detail is an array here and a string for
// every other Sonar status, and the loc array mixes strings with the integer
// message index.
func TestSonarValidationGolden(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, sonarScenario))

	resp, body := s.do(t, http.MethodPost, "/v1/sonar",
		`{"messages":[{"role":"wizard","content":"hi"}]}`)
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Equal(t, string(goldenBytes(t, "perplexity-sonar-422.json")), string(body))

	// The message index inside loc is a JSON number, not the string "0".
	require.Contains(t, string(body), `"loc":["body","messages",0,"role"]`)
}

// TestSonarUnauthorizedGolden pins the non-422 Sonar body: detail as a string.
// It is simulator-chosen and recorded as unverified, and it is deliberately not
// the Agent API's ErrorInfo envelope.
func TestSonarUnauthorizedGolden(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, sonarScenario))

	resp, body := s.doHeaders(t, http.MethodPost, "/v1/sonar", sonarRequest,
		map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, string(goldenBytes(t, "perplexity-sonar-401.json")), string(body))
}

// TestSonarRequestValidation walks §6.3's Sonar table. A consumer must be able
// to prove it sent the correct vendor request, not merely that it got a
// response, so each rejection carries its own code.
func TestSonarRequestValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		request    string
		wantStatus int
		wantCode   string
	}{
		{name: "a valid request", request: sonarRequest, wantStatus: http.StatusOK},
		{name: "model is required", request: `{"messages":[{"role":"user","content":"hi"}]}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeModelMissing},
		{name: "an unknown model is rejected",
			request:    `{"model":"sonar-tiny","messages":[{"role":"user","content":"hi"}]}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeModelInvalid},
		{name: "the withdrawn model has its own code",
			request:    `{"model":"sonar-reasoning","messages":[{"role":"user","content":"hi"}]}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeModelRemoved},
		{name: "messages is required", request: `{"model":"sonar"}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeMessagesMissing},
		{name: "messages must not be empty", request: `{"model":"sonar","messages":[]}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeMessagesEmpty},
		{name: "messages must be an array", request: `{"model":"sonar","messages":"hi"}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeMessagesInvalid},
		{name: "a message must be an object", request: `{"model":"sonar","messages":["hi"]}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeMessageInvalid},
		{name: "a role outside the enum is rejected",
			request:    `{"model":"sonar","messages":[{"role":"wizard","content":"hi"}]}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeRoleInvalid},
		{name: "content may be an array of chunks",
			request:    `{"model":"sonar","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			wantStatus: http.StatusOK},
		{name: "content must not be a number",
			request:    `{"model":"sonar","messages":[{"role":"user","content":7}]}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeContentInvalid},
		{name: "temperature is bounded at 2",
			request:    `{"model":"sonar","messages":[{"role":"user","content":"hi"}],"temperature":2.5}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeTemperature},
		{name: "top_p is bounded at 1",
			request:    `{"model":"sonar","messages":[{"role":"user","content":"hi"}],"top_p":1.2}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeTopP},
		{name: "max_tokens is bounded",
			request:    `{"model":"sonar","messages":[{"role":"user","content":"hi"}],"max_tokens":200000}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeMaxTokens},
		{name: "search_mode is an enum",
			request:    `{"model":"sonar","messages":[{"role":"user","content":"hi"}],"search_mode":"telepathy"}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeSearchMode},
		{name: "reasoning_effort is an enum",
			request:    `{"model":"sonar","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"maximum"}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeReasoningEffort},
		{name: "search_recency_filter is an enum",
			request:    `{"model":"sonar","messages":[{"role":"user","content":"hi"}],"search_recency_filter":"fortnight"}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeRecencyFilter},
		{name: "streaming is flagged, not rejected",
			request:    `{"model":"sonar","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantStatus: http.StatusOK, wantCode: CodeStreamUnimplemented},
		{name: "an unmodelled property is flagged, not rejected",
			request:    `{"model":"sonar","messages":[{"role":"user","content":"hi"}],"curiosity":9}`,
			wantStatus: http.StatusOK, wantCode: CodeUnknownField},
		{name: "a malformed body reports the decode error alone", request: `{"model":`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "request.malformed_json"},
		{name: "a non-object body is rejected", request: `["model"]`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "request.body_not_object"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, sonarScenario))

			resp, body := s.do(t, http.MethodPost, "/v1/sonar", tc.request)
			require.Equal(t, tc.wantStatus, resp.StatusCode, "body: %s", body)
			if tc.wantCode != "" {
				require.True(t, hasCode(s.findings(t), tc.wantCode), "findings: %+v", s.findings(t))
			}
		})
	}
}

// TestMalformedBodyReportsOnlyTheDecodeError guards against the noise a naive
// implementation produces: a body that never parsed has no fields to be missing,
// and reporting "model is required" alongside the decode error tells the reader
// nothing and hides the real problem.
func TestMalformedBodyReportsOnlyTheDecodeError(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, sonarScenario))

	resp, body := s.do(t, http.MethodPost, "/v1/sonar", `{"model":`)
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

	var decoded ValidationErrorResponse
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Len(t, decoded.Detail, 1)
	require.Equal(t, "json_invalid", decoded.Detail[0].Type)
	require.Equal(t, []any{"body"}, decoded.Detail[0].Loc)
}

// TestAuthentication walks §6.4. Perplexity declares HTTPBearer and nothing
// else, so an x-api-key is observed and flagged but never accepted.
func TestAuthentication(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		policy     string
		headers    map[string]string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "a bearer credential is accepted",
			headers:    map[string]string{"Authorization": "Bearer " + testKey},
			wantStatus: http.StatusOK,
		},
		{
			name:       "no credential is rejected",
			wantStatus: http.StatusUnauthorized,
			wantCode:   CodeAuthMissing,
		},
		{
			name:       "an x-api-key is flagged and not accepted",
			headers:    map[string]string{"x-api-key": testKey},
			wantStatus: http.StatusUnauthorized,
			wantCode:   CodeAuthWrongPlacement,
		},
		{
			name:       "optional mode serves an anonymous request",
			policy:     "    auth:\n      mode: optional\n",
			wantStatus: http.StatusOK,
		},
		{
			name:       "reject mode refuses a valid credential",
			policy:     "    auth:\n      mode: reject\n",
			headers:    map[string]string{"Authorization": "Bearer " + testKey},
			wantStatus: http.StatusUnauthorized,
			wantCode:   CodeAuthRejected,
		},
		{
			name:       "expect_key rejects the wrong key",
			policy:     "    auth:\n      expect_key: the-expected-key\n",
			headers:    map[string]string{"Authorization": "Bearer " + testKey},
			wantStatus: http.StatusUnauthorized,
			wantCode:   CodeAuthMismatch,
		},
		{
			name:       "expect_key accepts the right key",
			policy:     "    auth:\n      expect_key: " + testKey + "\n",
			headers:    map[string]string{"Authorization": "Bearer " + testKey},
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, `
version: 1
name: auth-policy
providers:
  perplexity:
`+tc.policy+"    answer: hello\n"))

			headers := map[string]string{"Content-Type": "application/json"}
			for k, v := range tc.headers {
				headers[k] = v
			}
			resp, body := s.doHeaders(t, http.MethodPost, "/v1/sonar", sonarRequest, headers)
			require.Equal(t, tc.wantStatus, resp.StatusCode, "body: %s", body)
			if tc.wantCode != "" {
				require.True(t, hasCode(s.findings(t), tc.wantCode), "findings: %+v", s.findings(t))
			}
		})
	}
}

// TestCredentialsNeverSurviveARoundTrip is house rule 4. The question that
// matters is not "does redaction work" but "can a credential reach a retained
// structure by any path", so this serialises the whole journal entry — headers,
// body, findings, auth observation — and looks for the key in all of it.
func TestCredentialsNeverSurviveARoundTrip(t *testing.T) {
	t.Parallel()
	const secret = "sk-not-a-real-key-0123456789"

	s := newSim(t, mustScenario(t, `
version: 1
name: redaction
providers:
  perplexity:
    auth:
      expect_key: something-else
    answer: hello
`))

	// Present the credential in every placement at once, including the query
	// string, which is the misconfiguration the journal exists to surface.
	_, _ = s.doHeaders(t, http.MethodPost, "/v1/sonar?api_key="+secret, sonarRequest, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + secret,
		"x-api-key":     secret,
	})

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	serialised, err := json.Marshal(entries[0])
	require.NoError(t, err)
	require.NotContains(t, string(serialised), secret)

	// The observation must still be useful: presence and a fingerprint.
	require.True(t, entries[0].Auth.Present)
	require.NotEmpty(t, entries[0].Auth.Fingerprint)
}

// TestContentTypeIsAWarning guards §6.2's decision. No vendor documents a 415
// for this, so returning one would invent vendor behaviour; the finding is
// assertable and the request still succeeds.
func TestContentTypeIsAWarning(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, sonarScenario))

	resp, _ := s.doHeaders(t, http.MethodPost, "/v1/sonar", sonarRequest, map[string]string{
		"Content-Type":  "text/plain",
		"Authorization": "Bearer " + testKey,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, hasCode(s.findings(t), CodeContentType))
}

// TestValidationPolicyPromotes proves a scenario can reshape the mapping without
// any code change, which is what makes the warning defaults safe.
func TestValidationPolicyPromotes(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: strict-content-type
providers:
  perplexity:
    validation:
      promote: [request.content_type]
    answer: hello
`))

	resp, _ := s.doHeaders(t, http.MethodPost, "/v1/sonar", sonarRequest, map[string]string{
		"Content-Type":  "text/plain",
		"Authorization": "Bearer " + testKey,
	})
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

// TestValidationBodyIsDeterministic is §3.3's required test. The 422 body is
// built from the findings list, unknown fields are discovered by walking a
// map[string]any, and Go randomises map iteration per run — so without a total
// order these bytes would differ from run to run and every golden over them
// would flake.
func TestValidationBodyIsDeterministic(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: strict-unknown-fields
providers:
  perplexity:
    validation:
      strict: true
    answer: hello
`))

	const request = `{"model":"sonar","messages":[{"role":"user","content":"hi"}],` +
		`"epsilon":1,"alpha":2,"gamma":3,"beta":4,"delta":5}`

	resp, first := s.do(t, http.MethodPost, "/v1/sonar", request)
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

	var decoded ValidationErrorResponse
	require.NoError(t, json.Unmarshal(first, &decoded))
	require.Len(t, decoded.Detail, 5)
	require.Equal(t, []any{"body", "alpha"}, decoded.Detail[0].Loc)
	require.Equal(t, []any{"body", "gamma"}, decoded.Detail[4].Loc)

	for range 20 {
		_, again := s.do(t, http.MethodPost, "/v1/sonar", request)
		require.Equal(t, string(first), string(again))
	}
}

// TestRejectedRequestsConsumeNoAttempt is §4.4's rule. A malformed request must
// not eat a retry budget, or a fault scenario becomes sensitive to a consumer's
// unrelated request bug.
func TestRejectedRequestsConsumeNoAttempt(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: budget-guard
providers:
  perplexity:
    fault:
      attempts:
        - status: 429
        - status: 200
    answer: recovered
`))

	// Three rejections: no credential, a bad model, and an unknown path.
	resp, _ := s.doHeaders(t, http.MethodPost, "/v1/sonar", sonarRequest,
		map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	resp, _ = s.do(t, http.MethodPost, "/v1/sonar", `{"model":"sonar-tiny","messages":[]}`)
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

	resp, _ = s.do(t, http.MethodPost, "/v1/nope", "{}")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// The plan is still on attempt zero, so a valid request takes the 429.
	resp, _ = s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	resp, body := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "recovered")
}
