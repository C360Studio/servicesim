package perplexity

import (
	"encoding/json"
	"net/http"
	"reflect"
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

// TestAgentValidationGolden pins the Agent surface's validation failure. The
// specification documents only 200 and 400 on createAgent
// (#/paths/~1v1~1agent/post/responses) and no Agent operation documents 422, so a
// request that fails validation is HTTP 400 carrying ErrorInfo, whose message
// takes the "validation failed: <message>" form the max_output_tokens
// description gives (#/components/schemas/ResponsesRequest/properties/max_output_tokens).
func TestAgentValidationGolden(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, agentCorpus))

	resp, body := s.do(t, http.MethodPost, "/v1/agent", `{}`)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, string(goldenBytes(t, "perplexity-agent-400-validation.json")), string(body))
}

// TestAgentValidationFailureShape walks the facts the golden above cannot say on
// its own: the envelope is ErrorInfo and never FastAPI's detail array, it is the
// same through every spelling of the route, the journal labels it 400, and when
// several rules fail at once the message names the first in the request schema's
// declaration order, so the body does not depend on the order checks happen to run.
func TestAgentValidationFailureShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		request     string
		wantMessage string
	}{
		{name: "missing input", path: "/v1/agent", request: `{}`,
			wantMessage: "validation failed: input is required"},
		{name: "through /v1/responses", path: "/v1/responses", request: `{}`,
			wantMessage: "validation failed: input is required"},
		{name: "through /responses", path: "/responses", request: `{}`,
			wantMessage: "validation failed: input is required"},
		{name: "malformed JSON", path: "/v1/agent", request: `{"input":`,
			wantMessage: "validation failed: "},
		{name: "first failing field in schema order wins", path: "/v1/agent",
			request:     `{"input":"hi","model":"openai/gpt-5","temperature":3,"max_steps":0}`,
			wantMessage: "validation failed: max_steps must be an integer of at least 1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, agentCorpus))

			resp, body := s.do(t, http.MethodPost, tc.path, tc.request)
			require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)

			var envelope map[string]any
			require.NoError(t, json.Unmarshal(body, &envelope))
			require.NotContains(t, envelope, "detail", "422's HTTPValidationError is not an Agent body")
			info, ok := envelope["error"].(map[string]any)
			require.True(t, ok, "error is %T, want an ErrorInfo object", envelope["error"])
			require.Equal(t, "invalid_request", info["code"])
			require.Equal(t, "invalid_request_error", info["type"])
			message, _ := info["message"].(string)
			require.True(t, strings.HasPrefix(message, tc.wantMessage),
				"message %q does not start with %q", message, tc.wantMessage)

			entries := s.journal.Snapshot()
			require.Len(t, entries, 1)
			require.Equal(t, "perplexity.agent.error.400", entries[0].Outcome.Label)
		})
	}
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
		{"stream", `{"input":"hi","model":"openai/gpt-5","stream":true}`, CodeAgentStreamUnsupported},
		{"background", `{"input":"hi","model":"openai/gpt-5","background":true}`, CodeAgentBackgroundUnsupported},
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
			wantStatus: http.StatusBadRequest, wantCode: CodeInputMissing},
		{name: "input may be an array of items", request: `{"model":"openai/gpt-5","input":[{"role":"user","content":"hi"}]}`,
			wantStatus: http.StatusOK},
		{name: "input must not be a number", request: `{"input":7,"model":"openai/gpt-5"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeInputInvalid},
		{name: "a model chain is capped at five",
			request:    `{"input":"hi","model":"openai/gpt-5","models":["a/b","a/b","a/b","a/b","a/b","a/b"]}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeModelsTooMany},
		{name: "max_steps is at least one", request: `{"input":"hi","model":"openai/gpt-5","max_steps":0}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeMaxSteps},
		{name: "max_output_tokens is positive", request: `{"input":"hi","model":"openai/gpt-5","max_output_tokens":0}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeMaxOutputTokens},
		{name: "temperature is bounded", request: `{"input":"hi","model":"openai/gpt-5","temperature":3}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeTemperature},
		{name: "top_p is bounded", request: `{"input":"hi","model":"openai/gpt-5","top_p":1.5}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeTopP},
		{name: "store must be a boolean", request: `{"input":"hi","model":"openai/gpt-5","store":"yes"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeStoreInvalid},
		{name: "a bare model name is flagged but accepted", request: `{"input":"hi","model":"gpt-5"}`,
			wantStatus: http.StatusOK, wantCode: CodeModelFormat},
		{name: "an unmodelled property is flagged but accepted", request: `{"input":"hi","model":"openai/gpt-5","curiosity":9}`,
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

// TestAgentModelSelection pins which model-selecting properties a request must
// carry. #/components/schemas/ResponsesRequest/properties/model says "Required
// if neither models nor preset is provided", and models has minItems 1 and items
// of type string.
func TestAgentModelSelection(t *testing.T) {
	t.Parallel()

	const required = "validation failed: model is required if neither models nor preset is provided"

	tests := []struct {
		name        string
		request     string
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{name: "nothing selecting a model", request: `{"input":"hi"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeModelRequired, wantMessage: required},
		{name: "an empty model selects nothing", request: `{"input":"hi","model":""}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeModelRequired, wantMessage: required},
		{name: "models holding only empty strings select nothing", request: `{"input":"hi","models":["",""]}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeModelRequired, wantMessage: required},
		{name: "model alone", request: `{"input":"hi","model":"openai/gpt-5"}`,
			wantStatus: http.StatusOK},
		{name: "models alone", request: `{"input":"hi","models":["openai/gpt-5"]}`,
			wantStatus: http.StatusOK},
		{name: "preset alone", request: `{"input":"hi","preset":"fast"}`,
			wantStatus: http.StatusOK},
		{name: "models and model together", request: `{"input":"hi","model":"openai/gpt-5","models":["openai/gpt-5"]}`,
			wantStatus: http.StatusOK},
		{name: "an empty models chain (minItems 1)", request: `{"input":"hi","model":"openai/gpt-5","models":[]}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeModelsInvalid,
			wantMessage: "validation failed: models must contain at least one model"},
		{name: "models items are strings", request: `{"input":"hi","models":[1]}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeModelsInvalid,
			wantMessage: "validation failed: models entry 0 must be a string"},
		{name: "models is an array", request: `{"input":"hi","models":"openai/gpt-5"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeModelsInvalid,
			wantMessage: "validation failed: models must be an array of model IDs"},
		// The message is pinned, not only the finding code: a wrong-typed model
		// must be reported once, as a type error, and not also as "model is
		// required".
		{name: "model is a string", request: `{"input":"hi","model":5}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeModelInvalid,
			wantMessage: "validation failed: model must be a string"},
		{name: "preset is a string", request: `{"input":"hi","preset":5}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeModelInvalid,
			wantMessage: "validation failed: preset must be a string"},
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
			if tc.wantMessage != "" {
				var envelope struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(body, &envelope))
				require.Equal(t, tc.wantMessage, envelope.Error.Message)
			}
		})
	}
}

// TestAgentResponseModelIsNeverEmpty pins what responsesResponse.model holds
// when the scenario names no model of its own. The specification calls it "Model
// used for generation" and requires it, and says models takes precedence over
// model; it is silent on what a preset-only request echoes, so that case is a
// Servicesim policy: "preset/<name>", deterministic and non-empty.
func TestAgentResponseModelIsNeverEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		request   string
		wantModel string
	}{
		{name: "model", request: `{"input":"hi","model":"openai/gpt-5"}`,
			wantModel: "openai/gpt-5"},
		{name: "models takes precedence over model",
			request:   `{"input":"hi","model":"openai/gpt-5","models":["google/gemini-3","openai/gpt-5"]}`,
			wantModel: "google/gemini-3"},
		{name: "preset alone", request: `{"input":"hi","preset":"fast"}`,
			wantModel: "preset/fast"},
		// An empty string selects nothing in models too: it is skipped when
		// choosing the echoed model, never echoed.
		{name: "an empty first entry of models is skipped",
			request:   `{"input":"hi","models":["","openai/gpt-5"]}`,
			wantModel: "openai/gpt-5"},
		{name: "a models chain of empty strings falls back to model",
			request:   `{"input":"hi","model":"openai/gpt-5","models":[""]}`,
			wantModel: "openai/gpt-5"},
		{name: "a models chain of empty strings falls back to preset",
			request:   `{"input":"hi","preset":"fast","models":[""]}`,
			wantModel: "preset/fast"},
		{name: "model beats preset", request: `{"input":"hi","model":"openai/gpt-5","preset":"fast"}`,
			wantModel: "openai/gpt-5"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, `
version: 1
name: agent-no-scenario-model
providers:
  perplexity_agent:
    answer: hi
`))
			resp, body := s.do(t, http.MethodPost, "/v1/agent", tc.request)
			require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

			var envelope struct {
				Model string `json:"model"`
			}
			require.NoError(t, json.Unmarshal(body, &envelope))
			require.Equal(t, tc.wantModel, envelope.Model)
		})
	}

	t.Run("a scenario model still wins", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, agentCorpus))
		_, body := s.do(t, http.MethodPost, "/v1/agent", `{"input":"hi","preset":"fast"}`)
		require.Contains(t, string(body), `"model":"openai/gpt-5"`)
	})
}

// TestAgentAnthropicRequiresMaxOutputTokens pins the one Agent validation rule
// whose status and message the specification quotes verbatim
// (#/components/schemas/ResponsesRequest/properties/max_output_tokens): "If
// omitted for an Anthropic model, the API returns HTTP 400 with: validation
// failed: max_output_tokens is required when using Anthropic models."
func TestAgentAnthropicRequiresMaxOutputTokens(t *testing.T) {
	t.Parallel()

	const wantMessage = "validation failed: max_output_tokens is required when using Anthropic models."

	tests := []struct {
		name    string
		path    string
		request string
		reject  bool
	}{
		{name: "anthropic model without max_output_tokens", path: "/v1/agent",
			request: `{"input":"hi","model":"anthropic/claude-sonnet-4-6"}`, reject: true},
		{name: "through an alias", path: "/v1/responses",
			request: `{"input":"hi","model":"anthropic/claude-sonnet-4-6"}`, reject: true},
		{name: "an anthropic entry in the models chain", path: "/v1/agent",
			request: `{"input":"hi","models":["openai/gpt-5","anthropic/claude-sonnet-4-6"]}`, reject: true},
		{name: "anthropic model with max_output_tokens", path: "/v1/agent",
			request: `{"input":"hi","model":"anthropic/claude-sonnet-4-6","max_output_tokens":1024}`},
		{name: "another provider without max_output_tokens", path: "/v1/agent",
			request: `{"input":"hi","model":"openai/gpt-5"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, agentCorpus))

			resp, body := s.do(t, http.MethodPost, tc.path, tc.request)
			if !tc.reject {
				require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
				return
			}
			require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
			var envelope struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(body, &envelope))
			require.Equal(t, wantMessage, envelope.Error.Message)
		})
	}
}

// TestAgentProfileField pins the request's profile property, a ProfileReference
// (#/components/schemas/ProfileReference): additionalProperties false, required
// type (enum ["custom"]) and id (1 to 128 characters), optional version (string).
// profile "Cannot be combined with preset"
// (#/components/schemas/ResponsesRequest/properties/profile). It is accepted and
// validated only; what a saved profile would configure is not simulated.
func TestAgentProfileField(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("p", 129)
	tests := []struct {
		name       string
		request    string
		wantStatus int
		wantCode   string
	}{
		{name: "a profile is accepted",
			request:    `{"input":"hi","model":"openai/gpt-5","profile":{"type":"custom","id":"research"}}`,
			wantStatus: http.StatusOK},
		{name: "a profile may pin a version",
			request:    `{"input":"hi","model":"openai/gpt-5","profile":{"type":"custom","id":"research","version":"latest"}}`,
			wantStatus: http.StatusOK},
		{name: "an id of exactly 128 characters",
			request:    `{"input":"hi","model":"openai/gpt-5","profile":{"type":"custom","id":"` + strings.Repeat("p", 128) + `"}}`,
			wantStatus: http.StatusOK},
		{name: "profile must be an object",
			request:    `{"input":"hi","model":"openai/gpt-5","profile":"research"}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeProfileInvalid},
		{name: "profile.type is required",
			request:    `{"input":"hi","model":"openai/gpt-5","profile":{"id":"research"}}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeProfileInvalid},
		{name: "profile.type is custom",
			request:    `{"input":"hi","model":"openai/gpt-5","profile":{"type":"builtin","id":"research"}}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeProfileInvalid},
		{name: "profile.id is required",
			request:    `{"input":"hi","model":"openai/gpt-5","profile":{"type":"custom"}}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeProfileInvalid},
		{name: "profile.id is not empty",
			request:    `{"input":"hi","model":"openai/gpt-5","profile":{"type":"custom","id":""}}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeProfileInvalid},
		{name: "profile.id is at most 128 characters",
			request:    `{"input":"hi","model":"openai/gpt-5","profile":{"type":"custom","id":"` + long + `"}}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeProfileInvalid},
		{name: "profile.version is a string",
			request:    `{"input":"hi","model":"openai/gpt-5","profile":{"type":"custom","id":"research","version":2}}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeProfileInvalid},
		{name: "profile takes no other properties",
			request:    `{"input":"hi","model":"openai/gpt-5","profile":{"type":"custom","id":"research","extra":1}}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeProfileInvalid},
		{name: "profile cannot be combined with preset",
			request:    `{"input":"hi","preset":"fast","profile":{"type":"custom","id":"research"}}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeProfileInvalid},
		// INFERENCE: the specification says model is required "if neither models
		// nor preset is provided" and does not mention profile, but it also calls
		// profile a "Saved, versioned configuration to run with" that "Cannot be
		// combined with preset", which makes the two alternatives. Nothing says a
		// profile does NOT supply the model, so a valid profile alone is accepted
		// rather than rejecting traffic the live API may accept.
		{name: "a valid profile alone selects a model (inference)",
			request:    `{"input":"hi","profile":{"type":"custom","id":"research"}}`,
			wantStatus: http.StatusOK},
		// An invalid profile selects nothing, and is reported once, as itself.
		{name: "an invalid profile alone is not also a missing model",
			request:    `{"input":"hi","profile":{"type":"custom"}}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeProfileInvalid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, agentCorpus))

			resp, body := s.do(t, http.MethodPost, "/v1/agent", tc.request)
			require.Equal(t, tc.wantStatus, resp.StatusCode, "body: %s", body)
			findings := s.findings(t)
			if tc.wantCode != "" {
				require.True(t, hasCode(findings, tc.wantCode), "findings: %+v", findings)
			}
			require.False(t, hasCode(findings, CodeUnknownField),
				"profile is a modelled property and must not draw the unknown-field warning: %+v", findings)
		})
	}
}

// TestAgentProfileSelectsTheModel pins the two INFERENCE / SIMULATOR-POLICY
// choices around a profile-only request: it is accepted, and, with no scenario
// model, it echoes "profile/<id>", mirroring the "preset/<name>" a preset-only
// request echoes. The specification states neither.
func TestAgentProfileSelectsTheModel(t *testing.T) {
	t.Parallel()

	newScenarioSim := func(t *testing.T) *sim {
		t.Helper()
		return newSim(t, mustScenario(t, `
version: 1
name: agent-no-scenario-model
providers:
  perplexity_agent:
    answer: hi
`))
	}

	t.Run("a profile-only request echoes profile/<id>", func(t *testing.T) {
		t.Parallel()
		s := newScenarioSim(t)
		resp, body := s.do(t, http.MethodPost, "/v1/agent",
			`{"input":"hi","profile":{"type":"custom","id":"research","version":"latest"}}`)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
		require.Contains(t, string(body), `"model":"profile/research"`)
	})

	t.Run("an explicit model still wins over the profile", func(t *testing.T) {
		t.Parallel()
		s := newScenarioSim(t)
		_, body := s.do(t, http.MethodPost, "/v1/agent",
			`{"input":"hi","model":"openai/gpt-5","profile":{"type":"custom","id":"research"}}`)
		require.Contains(t, string(body), `"model":"openai/gpt-5"`)
	})

	t.Run("a profile does not satisfy max_output_tokens for an anthropic model", func(t *testing.T) {
		t.Parallel()
		s := newScenarioSim(t)
		resp, body := s.do(t, http.MethodPost, "/v1/agent",
			`{"input":"hi","model":"anthropic/claude-sonnet-4-6","profile":{"type":"custom","id":"research"}}`)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
		require.Contains(t, string(body), "max_output_tokens is required")
	})

	t.Run("an invalid profile is reported once, not also as a missing model", func(t *testing.T) {
		t.Parallel()
		s := newScenarioSim(t)
		resp, body := s.do(t, http.MethodPost, "/v1/agent",
			`{"input":"hi","profile":{"type":"custom"}}`)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
		require.Contains(t, string(body), "validation failed: profile.id is required")
		require.NotContains(t, string(body), "model is required")
	})

	t.Run("profile with preset stays a 400", func(t *testing.T) {
		t.Parallel()
		s := newScenarioSim(t)
		resp, body := s.do(t, http.MethodPost, "/v1/agent",
			`{"input":"hi","preset":"fast","profile":{"type":"custom","id":"research"}}`)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
		require.Contains(t, string(body), "profile cannot be combined with preset")
	})
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

// TestAgentValidatorProjectionKeysMatchesTheDecodeStruct pins
// agentValidator.ProjectionKeys() to perplexityAgent's own top-level yaml
// tags by reflection, so the hand-copied literal (scenarios_test.go's
// documentedProjectionKeys' own source, since Phase 10 unit 8) cannot drift
// from the struct silently again in either direction — additive (a
// documented, decodable key like "created_at" missing from the list, which
// is exactly the gap this test was written to catch) or stale (a listed key
// the struct no longer accepts).
func TestAgentValidatorProjectionKeysMatchesTheDecodeStruct(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(perplexityAgent{})
	want := make(map[string]bool, typ.NumField())
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("yaml")
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			want[name] = true
		}
	}

	got := make(map[string]bool)
	for _, k := range (agentValidator{}).ProjectionKeys() {
		got[k] = true
	}

	require.Equal(t, want, got,
		"agentValidator.ProjectionKeys() must list exactly perplexityAgent's own yaml tags")
}
