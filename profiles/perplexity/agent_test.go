package perplexity

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/scenario"
)

// agentCorpus is the two-source corpus the Agent goldens project from.
const agentCorpus = `
version: 1
name: golden-agent
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
  - id: source-b
    url: https://example.test/report-b
    title: Report B
providers:
  perplexity_agent:
    response_id: resp_9f2c1d8b7a6e5f4c3b2a1908f7e6d5c4
    message_id: msg_4e3d2c1b0a9f8e7d6c5b4a39281706f5
    model: openai/gpt-5
    answer: Report A finds that deterministic simulation removes flakiness, and Report B agrees.
    queries:
      - deterministic simulator adapter tests
    search_results:
      - source: source-a
        snippet: Report A finds that deterministic simulators remove flakiness from adapter test suites.
        date: "2025-11-16"
        last_updated: "2025-12-01"
      - source: source-b
        snippet: Report B corroborates Report A across a second dataset.
    annotations:
      - source: source-a
        start_index: 0
        end_index: 8
      - source: source-b
        start_index: 68
        end_index: 76
    usage:
      input_tokens: 42
      output_tokens: 128
      total_tokens: 170
      cost:
        input_cost: 0.00021
        output_cost: 0.00128
        total_cost: 0.00149
`

// TestAgentHappyGolden compares the rendered trace with the contract fixture as
// raw JSON bytes, which is the only comparison that can catch the trap this
// surface sets: results[].id is an integer, and a string id round-trips cleanly
// through any permissive decoder.
func TestAgentHappyGolden(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, agentCorpus))

	resp, body := s.do(t, http.MethodPost, "/v1/agent", agentRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, string(goldenBytes(t, "perplexity-agent-happy.json")), string(body))

	// Stated separately from the golden comparison, because this is the assertion
	// whose failure message must name the actual defect.
	require.Contains(t, string(body), `{"id":1,"title":"Report A"`)
	require.Contains(t, string(body), `{"id":2,"title":"Report B"`)
	require.NotContains(t, string(body), `"id":"1"`)
}

// TestAgentAliasRendersTheSameBody proves /v1/responses is an alias in the
// strict sense and not a second implementation.
func TestAgentAliasRendersTheSameBody(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, agentCorpus))

	_, canonical := s.do(t, http.MethodPost, "/v1/agent", agentRequest)
	_, alias := s.do(t, http.MethodPost, "/v1/responses", agentRequest)
	require.Equal(t, string(canonical), string(alias))
}

// TestAgentEmptyGolden pins the zero-result success: results and annotations are
// emitted as empty arrays rather than omitted, so a consumer's array handling is
// always exercised.
func TestAgentEmptyGolden(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: golden-agent-empty
providers:
  perplexity_agent:
    response_id: resp_1a2b3c4d5e6f708192a3b4c5d6e7f809
    message_id: msg_809f7e6d5c4b3a2918070605040302f1
    model: openai/gpt-5
    queries:
      - deterministic simulator adapter tests
    usage:
      input_tokens: 12
`))

	resp, body := s.do(t, http.MethodPost, "/v1/agent", agentRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, string(goldenBytes(t, "perplexity-agent-empty.json")), string(body))
}

// TestAgentFailedGolden pins a terminal failure reported inside a 200 body. A
// consumer that only branches on the HTTP status misses it entirely, which is
// exactly why the case exists.
func TestAgentFailedGolden(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: golden-agent-failed
providers:
  perplexity_agent:
    response_id: resp_5d6e7f80912a3b4c5d6e7f80912a3b4c
    model: openai/gpt-5
    status: failed
    error:
      code: model_error
      message: The model could not complete the research loop.
      type: server_error
    usage:
      input_tokens: 42
      cost:
        input_cost: 0.00021
        total_cost: 0.00021
`))

	resp, body := s.do(t, http.MethodPost, "/v1/agent", agentRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode, "a failed status is not an HTTP failure")
	require.Equal(t, string(goldenBytes(t, "perplexity-agent-failed.json")), string(body))
}

// TestAgentOutputOrderIsFixed pins the trace order: the agent searched, then it
// answered. A scenario cannot reorder it, so a consumer may rely on the index.
func TestAgentOutputOrderIsFixed(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, agentCorpus))
	_, body := s.do(t, http.MethodPost, "/v1/agent", agentRequest)

	var envelope struct {
		Output []struct {
			Type string `json:"type"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Len(t, envelope.Output, 2)
	require.Equal(t, outputTypeSearchResults, envelope.Output[0].Type)
	require.Equal(t, outputTypeMessage, envelope.Output[1].Type)

	// And at the byte level, because the decode above would pass just as happily
	// on a body whose items were swapped by a later refactor of the render order.
	require.Less(t,
		strings.Index(string(body), `"type":"search_results"`),
		strings.Index(string(body), `"type":"message"`))
}

// TestAgentEnvelopesShareNothing is the design constraint made executable. The
// two surfaces spell the same quantities differently, and a shared Go type would
// eventually leak one spelling onto the other.
func TestAgentEnvelopesShareNothing(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: both-surfaces
providers:
  perplexity:
    answer: sonar
    usage:
      prompt_tokens: 1
      completion_tokens: 2
  perplexity_agent:
    answer: agent
    usage:
      input_tokens: 1
      output_tokens: 2
`))

	_, sonar := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	require.Contains(t, string(sonar), `"prompt_tokens":1`)
	require.Contains(t, string(sonar), `"completion_tokens":2`)
	require.NotContains(t, string(sonar), `"input_tokens":`)
	require.NotContains(t, string(sonar), `"output_tokens":`)
	require.NotContains(t, string(sonar), `"output":`)

	_, agent := s.do(t, http.MethodPost, "/v1/agent", agentRequest)
	require.Contains(t, string(agent), `"input_tokens":1`)
	require.Contains(t, string(agent), `"output_tokens":2`)
	require.NotContains(t, string(agent), `"prompt_tokens":`)
	require.NotContains(t, string(agent), `"completion_tokens":`)
	require.NotContains(t, string(agent), `"choices":`)
	require.NotContains(t, string(agent), `"citations":`)
}

// TestAgentUsageDerivations pins the derived total, currency and cost.
func TestAgentUsageDerivations(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: agent-derivations
providers:
  perplexity_agent:
    answer: hello
    usage:
      input_tokens: 10
      output_tokens: 5
      cost:
        input_cost: 0.001
        output_cost: 0.002
`))

	_, body := s.do(t, http.MethodPost, "/v1/agent", agentRequest)
	require.Contains(t, string(body), `"total_tokens":15`)
	require.Contains(t, string(body), `"currency":"USD"`)
	require.Contains(t, string(body), `"total_cost":0.003`)
	// The optional cache and tool costs are omitted rather than emitted as zero.
	require.NotContains(t, string(body), "cache_creation_cost")
	require.NotContains(t, string(body), "tool_calls_cost")
}

// TestAgentValidationGolden pins the 422 body. It is FastAPI's
// HTTPValidationError even on the Agent surface, whose every other status is
// errorInfo — an asymmetry that is real and must not be unified.
func TestAgentValidationGolden(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, agentCorpus))

	resp, body := s.do(t, http.MethodPost, "/v1/agent", `{}`)
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Equal(t, string(goldenBytes(t, "perplexity-agent-422.json")), string(body))
}

// TestAgentUnauthorizedGolden pins the non-422 envelope, which is the published
// errorInfo shape rather than Sonar's {"detail": "<string>"}.
func TestAgentUnauthorizedGolden(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, agentCorpus))

	resp, body := s.doHeaders(t, http.MethodPost, "/v1/agent", agentRequest,
		map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, string(goldenBytes(t, "perplexity-agent-401.json")), string(body))
}

// TestAgentDeferredFeaturesWarnLoudly is the addendum's rule: a deferred
// feature must fail loudly, never silently. background is deferred
// unconditionally; stream is deferred under agentCorpus's default (no
// `stream:` key, hence `warn`) policy specifically — Phase 5 unit 3 gives
// this surface a stream: key that serves a real GrammarTyped sequence, see
// stream_test.go's TestAgentStreamPolicySwitch. Both requests here still
// receive an ordinary non-streaming, synchronous body — and both leave a
// named finding behind, so a consumer cannot believe it exercised a path it
// never touched.
func TestAgentDeferredFeaturesWarnLoudly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request string
		want    string
	}{
		{"stream", `{"input":"hi","stream":true}`, CodeAgentStreamUnsupported},
		{"background", `{"input":"hi","background":true}`, CodeAgentBackgroundUnsupported},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, agentCorpus))

			resp, body := s.do(t, http.MethodPost, "/v1/agent", tc.request)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Contains(t, string(body), `"object":"response"`)
			require.NotContains(t, string(body), `"status":"queued"`)

			findings := s.findings(t)
			require.True(t, hasCode(findings, tc.want), "findings: %+v", findings)
		})
	}
}

// TestAgentRequestValidation walks the Agent request surface.
func TestAgentRequestValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		request    string
		wantStatus int
		wantCode   string
	}{
		{name: "input is required", request: `{"model":"openai/gpt-5"}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeInputMissing},
		{name: "input may be an array of items", request: `{"input":[{"role":"user","content":"hi"}]}`,
			wantStatus: http.StatusOK},
		{name: "input must not be a number", request: `{"input":7}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeInputInvalid},
		{name: "a model chain is capped at five",
			request:    `{"input":"hi","models":["a/b","a/b","a/b","a/b","a/b","a/b"]}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeModelsTooMany},
		{name: "max_steps is at least one", request: `{"input":"hi","max_steps":0}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeMaxSteps},
		{name: "max_output_tokens is positive", request: `{"input":"hi","max_output_tokens":0}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeMaxOutputTokens},
		{name: "temperature is bounded", request: `{"input":"hi","temperature":3}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeTemperature},
		{name: "top_p is bounded", request: `{"input":"hi","top_p":1.5}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeTopP},
		{name: "store must be a boolean", request: `{"input":"hi","store":"yes"}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: CodeStoreInvalid},
		{name: "a bare model name is flagged but accepted", request: `{"input":"hi","model":"gpt-5"}`,
			wantStatus: http.StatusOK, wantCode: CodeModelFormat},
		{name: "an unmodelled property is flagged but accepted", request: `{"input":"hi","curiosity":9}`,
			wantStatus: http.StatusOK, wantCode: CodeUnknownField},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, agentCorpus))

			resp, body := s.do(t, http.MethodPost, "/v1/agent", tc.request)
			require.Equal(t, tc.wantStatus, resp.StatusCode, "body: %s", body)
			if tc.wantCode != "" {
				require.True(t, hasCode(s.findings(t), tc.wantCode), "findings: %+v", s.findings(t))
			}
		})
	}
}

// TestAgentValidatorRejectsBadProjections proves a bad Agent fixture fails at
// boot. An annotation span past the end of the answer is the case that would
// otherwise reach a consumer as an index into a string that is too short.
func TestAgentValidatorRejectsBadProjections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		respond  string
		wantCode string
	}{
		{
			name:     "a failed status with no error",
			respond:  "    status: failed\n",
			wantCode: "perplexity.agent.error.missing",
		},
		{
			name:     "a status outside the enum",
			respond:  "    status: nearly\n",
			wantCode: "perplexity.agent.status.invalid",
		},
		{
			name:     "an error with no message",
			respond:  "    error:\n      code: model_error\n",
			wantCode: "perplexity.agent.error.message",
		},
		{
			name:     "an annotation past the end of the answer",
			respond:  "    answer: short\n    annotations:\n      - source: source-a\n        start_index: 0\n        end_index: 99\n",
			wantCode: "perplexity.agent.annotation.range",
		},
		{
			name:     "an inverted annotation span",
			respond:  "    answer: a longer answer\n    annotations:\n      - source: source-a\n        start_index: 5\n        end_index: 2\n",
			wantCode: "perplexity.agent.annotation.range",
		},
		{
			name:     "an unknown source reference",
			respond:  "    search_results:\n      - source: source-z\n",
			wantCode: "scenario.source.unknown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := mustScenario(t, `
version: 1
name: bad-agent-fixture
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  perplexity_agent:
`+tc.respond)

			findings := agentValidator{}.ValidateProjections(s, s.Provider(NameAgent))
			require.NotEmpty(t, findings)

			codes := make([]string, 0, len(findings))
			for _, f := range findings {
				require.Equal(t, scenario.SeverityError, f.Severity)
				require.True(t, strings.HasPrefix(f.Path, "providers.perplexity_agent.turns[0].respond"),
					"finding is not addressed by its YAML path: %+v", f)
				codes = append(codes, f.Code)
			}
			require.Contains(t, codes, tc.wantCode)
		})
	}
}

// TestAgentValidatorAcceptsAGoodFixture is the other half of the check above: a
// correct fixture must produce no findings at all, or readiness would never
// succeed.
func TestAgentValidatorAcceptsAGoodFixture(t *testing.T) {
	t.Parallel()
	s := mustScenario(t, agentCorpus)
	require.Empty(t, agentValidator{}.ValidateProjections(s, s.Provider(NameAgent)))
}
