package perplexity

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/contracts"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// testKey is the credential every test presents. It is not a secret and cannot
// be one: Servicesim must never be pointed at a real key.
const testKey = "test-key"

// mustScenario parses a scenario from source and fails the test on any finding.
func mustScenario(t *testing.T, src string) *scenario.Scenario {
	t.Helper()
	s, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err, "scenario findings: %+v", report.Findings)
	require.True(t, report.OK(), "scenario findings: %+v", report.Findings)
	return s
}

// sim is a running Perplexity listener plus the journal it writes to.
type sim struct {
	server  *httptest.Server
	journal *journal.Ring
}

// newSim starts a listener over s with a real fault engine, so a scenario's
// declared faults actually fire.
func newSim(t *testing.T, s *scenario.Scenario) *sim {
	t.Helper()
	ring := journal.NewRing(64, 1<<16)
	srv := httptest.NewServer(New(provider.Deps{
		Scenario: s,
		Journal:  ring,
		Faults:   provider.MustSet(Profile()).Faults(s),
	}))
	t.Cleanup(srv.Close)
	return &sim{server: srv, journal: ring}
}

// do issues a request with a bearer credential and returns the response and its
// body. The body is read and closed here so no test can leak a connection.
func (s *sim) do(t *testing.T, method, path, body string) (*http.Response, []byte) {
	t.Helper()
	return s.doHeaders(t, method, path, body, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + testKey,
	})
}

func (s *sim) doHeaders(t *testing.T, method, path, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.server.URL+path, reader)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.server.Client().Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, out
}

// findings returns the findings of the most recent journal entry.
func (s *sim) findings(t *testing.T) []journal.Finding {
	t.Helper()
	entries := s.journal.Snapshot()
	require.NotEmpty(t, entries)
	return entries[len(entries)-1].Findings
}

func hasCode(findings []journal.Finding, code string) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

// goldenBytes returns a contract fixture with its pretty-printing removed, so a
// comparison against a rendered body is a comparison of the JSON bytes
// themselves — key order and scalar types included.
//
// This is deliberately not a semantic comparison. A wrong JSON type round-trips
// cleanly through a permissive decoder: an integer id decoded into `any` and an
// id decoded from the string "1" both compare equal once they have been through
// a struct, and the bug survives the test that was supposed to catch it.
func goldenBytes(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := contracts.Read(contracts.Perplexity, name)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, json.Compact(&buf, raw))
	return buf.Bytes()
}

// sonarScenario is the corpus the Sonar goldens project from.
const sonarScenario = `
version: 1
name: golden-sonar
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
  - id: source-b
    url: https://example.test/report-b
    title: Report B
providers:
  perplexity:
    completion_id: 3c90c3cc-0d44-4b50-8888-8dd25736052a
    model: sonar-pro
    answer: Both reports agree that deterministic simulation removes flakiness from adapter suites.
    search_results:
      - source: source-a
        snippet: Report A finds that deterministic simulators remove flakiness from adapter test suites.
        date: "2025-11-16"
        last_updated: "2025-12-01"
      - source: source-b
        snippet: Report B corroborates Report A across a second dataset.
    citations: [source-a, source-b]
    usage:
      prompt_tokens: 42
      completion_tokens: 128
      total_tokens: 170
      search_context_size: medium
      citation_tokens: 64
      num_search_queries: 2
      cost:
        input_tokens_cost: 0.00021
        output_tokens_cost: 0.00128
        request_cost: 0.005
        total_cost: 0.00649
`

// sonarRequest is a minimal valid Sonar request.
const sonarRequest = `{"model":"sonar-pro","messages":[{"role":"user","content":"what do the reports say?"}]}`

// agentRequest is a minimal valid Agent request.
const agentRequest = `{"input":"what do the reports say?","model":"openai/gpt-5"}`

// TestRoutesFaultKeys pins the six routes and the fault budgets they draw on.
// Every alias of a surface must share that surface's key — a retry through an
// alias draws on the same budget — and the two surfaces must not, or a scenario
// could not rate-limit one while leaving the other healthy.
func TestRoutesFaultKeys(t *testing.T) {
	t.Parallel()

	routes := Routes()
	require.Len(t, routes, 6)

	// Entry is empty on the Sonar routes and NameAgent on the Agent routes. Both
	// halves matter. Sonar is the listener's own entry, so leaving it empty keeps
	// the shipped resolution; the Agent surface is a SECOND entry on this
	// listener, and without an explicit Entry its own turn_key and validation
	// block are silently ignored in favour of Sonar's.
	want := []struct{ pattern, key, entry string }{
		{"POST /v1/sonar", "perplexity:completions", ""},
		{"POST /chat/completions", "perplexity:completions", ""},
		{"POST /v1/chat/completions", "perplexity:completions", ""},
		{"POST /v1/agent", "perplexity:agent", NameAgent},
		{"POST /v1/responses", "perplexity:agent", NameAgent},
		{"POST /responses", "perplexity:agent", NameAgent},
	}
	for i, w := range want {
		require.Equal(t, w.pattern, routes[i].Pattern)
		require.Equal(t, w.key, routes[i].FaultKey)
		require.Equal(t, w.entry, routes[i].Entry, "%s resolves the wrong scenario entry", w.pattern)
		require.NotNil(t, routes[i].Fault, "%s declares no fault selector", w.pattern)
	}
}

// The Agent surface must read its OWN turn_key, not Sonar's. This is a shipped
// behaviour change: before Route.Entry, a turn_key on the perplexity block
// subdivided the agent lane too, because entryTurnKey resolved by listener name.
// The old behaviour was an entry's own turn_key being ignored — a bug, not a
// contract — but it is pinned here so the change is asserted rather than found.
func TestAgentSurfaceReadsItsOwnTurnKey(t *testing.T) {
	t.Parallel()

	for _, r := range AgentRoutes() {
		require.Equal(t, NameAgent, r.Entry,
			"%s must resolve the perplexity_agent entry, or its turn_key is ignored", r.Pattern)
	}
	for _, r := range SonarRoutes() {
		require.Empty(t, r.Entry,
			"%s is the listener's own entry and must keep resolving by listener name", r.Pattern)
	}
}

// TestRoutingTable walks every route plus the two fail-closed cases. The 405 is
// the one most easily lost: without a method-less pattern per known path the
// catch-all answers 404 with a body no consumer can parse.
func TestRoutingTable(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, sonarScenario))

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantGolden string
		wantAllow  string
	}{
		{name: "sonar canonical", method: http.MethodPost, path: "/v1/sonar", body: sonarRequest,
			wantStatus: http.StatusOK, wantGolden: "perplexity-sonar-happy.json"},
		{name: "sonar alias from a /v1 base_url", method: http.MethodPost, path: "/chat/completions",
			body: sonarRequest, wantStatus: http.StatusOK, wantGolden: "perplexity-sonar-happy.json"},
		{name: "sonar alias from a bare base_url", method: http.MethodPost, path: "/v1/chat/completions",
			body: sonarRequest, wantStatus: http.StatusOK, wantGolden: "perplexity-sonar-happy.json"},
		{name: "agent canonical", method: http.MethodPost, path: "/v1/agent", body: agentRequest,
			wantStatus: http.StatusOK},
		{name: "agent alias from a bare base_url", method: http.MethodPost, path: "/v1/responses",
			body: agentRequest, wantStatus: http.StatusOK},
		{name: "agent alias from a /v1 base_url", method: http.MethodPost, path: "/responses",
			body: agentRequest, wantStatus: http.StatusOK},
		{name: "GET on an alias path is 405", method: http.MethodGet, path: "/v1/chat/completions",
			wantStatus: http.StatusMethodNotAllowed, wantGolden: "perplexity-405.json", wantAllow: "POST"},
		{name: "GET on the bare responses alias is 405", method: http.MethodGet, path: "/responses",
			wantStatus: http.StatusMethodNotAllowed, wantGolden: "perplexity-405.json", wantAllow: "POST"},
		{name: "unknown path is 404", method: http.MethodPost, path: "/v1/nope", body: "{}",
			wantStatus: http.StatusNotFound, wantGolden: "perplexity-404.json"},
		{name: "GET on a real path is 405", method: http.MethodGet, path: "/v1/sonar",
			wantStatus: http.StatusMethodNotAllowed, wantGolden: "perplexity-405.json", wantAllow: "POST"},
		{name: "GET on the agent path is 405", method: http.MethodGet, path: "/v1/agent",
			wantStatus: http.StatusMethodNotAllowed, wantGolden: "perplexity-405.json", wantAllow: "POST"},
		{name: "a trailing slash is 404, not a redirect", method: http.MethodPost, path: "/v1/sonar/", body: "{}",
			wantStatus: http.StatusNotFound, wantGolden: "perplexity-404.json"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := s.do(t, tc.method, tc.path, tc.body)
			require.Equal(t, tc.wantStatus, resp.StatusCode, "body: %s", body)
			require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
			if tc.wantAllow != "" {
				require.Equal(t, tc.wantAllow, resp.Header.Get("Allow"))
			}
			if tc.wantGolden != "" {
				require.Equal(t, string(goldenBytes(t, tc.wantGolden)), string(body))
			}
		})
	}
}

// TestAliasesShareOneFaultBudget proves no alias hands a retrying client a fresh
// set of retries. Each surface declares two 429s then success, and the three
// spellings are walked in turn: if any of them had a budget of its own it would
// answer 429 where the shared counter says 200, and a scenario's fault plan
// would silently stop meaning anything the moment a consumer retried through a
// different base-URL convention.
func TestAliasesShareOneFaultBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		request  string
		paths    [3]string
		wantOK   string
	}{
		{
			name: "sonar", provider: "perplexity", request: sonarRequest,
			paths:  [3]string{"/v1/sonar", "/chat/completions", "/v1/chat/completions"},
			wantOK: `"content":"recovered"`,
		},
		{
			name: "agent", provider: "perplexity_agent", request: agentRequest,
			paths:  [3]string{"/v1/agent", "/v1/responses", "/responses"},
			wantOK: `"text":"recovered"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, `
version: 1
name: shared-budget
providers:
  `+tc.provider+`:
    fault:
      attempts:
        - status: 429
        - status: 429
        - status: 200
    answer: recovered
`))

			for i, path := range tc.paths[:2] {
				resp, body := s.do(t, http.MethodPost, path, tc.request)
				require.Equal(t, http.StatusTooManyRequests, resp.StatusCode,
					"call %d on %s: %s", i, path, body)
			}

			resp, body := s.do(t, http.MethodPost, tc.paths[2], tc.request)
			require.Equal(t, http.StatusOK, resp.StatusCode,
				"%s must not restart the budget: %s", tc.paths[2], body)
			require.Contains(t, string(body), tc.wantOK)
		})
	}
}

// TestJournalRecordsWhichSpellingWasUsed is the other half of the alias promise:
// the routes behave identically, so the journal is the only place an adapter test
// can prove it reached the path it meant to configure. Both the request path and
// the matched route pattern must name the spelling that was used.
func TestJournalRecordsWhichSpellingWasUsed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path    string
		request string
	}{
		{"/v1/sonar", sonarRequest},
		{"/chat/completions", sonarRequest},
		{"/v1/chat/completions", sonarRequest},
		{"/v1/agent", agentRequest},
		{"/v1/responses", agentRequest},
		{"/responses", agentRequest},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, `
version: 1
name: journalled-routes
providers:
  perplexity:
    answer: hello
  perplexity_agent:
    answer: hello
`))
			resp, body := s.do(t, http.MethodPost, tc.path, tc.request)
			require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

			entries := s.journal.Snapshot()
			require.Len(t, entries, 1)
			require.Equal(t, tc.path, entries[0].Path)
			require.Equal(t, http.MethodPost+" "+tc.path, entries[0].Route)
		})
	}
}

// TestStreamPolicy covers the field docs/design/package-design.md already claimed
// Perplexity had. The default is warn — streaming is not simulated, so the
// request still receives a complete non-streaming 200 — and `stream: reject`
// turns that into a loud 422.
//
// The reject branch is what stops a consumer whose primary deep-research path
// always streams from recording fixtures against a body the real API would never
// have sent on that request.
func TestStreamPolicy(t *testing.T) {
	t.Parallel()

	const streamRequest = `{"model":"sonar","messages":[{"role":"user","content":"hi"}],"stream":true}`

	t.Run("the default warns and still answers", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: stream-default
providers:
  perplexity:
    answer: hello
`))
		resp, body := s.do(t, http.MethodPost, "/v1/sonar", streamRequest)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
		require.True(t, hasCode(s.findings(t), CodeStreamUnimplemented))
	})

	t.Run("reject fails the request", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: stream-reject
providers:
  perplexity:
    stream: reject
    answer: hello
`))
		resp, body := s.do(t, http.MethodPost, "/v1/sonar", streamRequest)
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, "body: %s", body)
		require.Contains(t, string(body), `["body","stream"]`)

		findings := s.findings(t)
		require.True(t, hasCode(findings, CodeStreamUnimplemented))
		for _, f := range findings {
			if f.Code == CodeStreamUnimplemented {
				require.Equal(t, journal.SeverityError, f.Severity)
				require.NotContains(t, f.Message, "receives the ordinary non-streaming body",
					"a rejected request must not also be told it was answered")
			}
		}
	})

	t.Run("reject applies through every spelling", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: stream-reject-aliases
providers:
  perplexity:
    stream: reject
    answer: hello
`))
		for _, path := range []string{"/chat/completions", "/v1/chat/completions"} {
			resp, _ := s.do(t, http.MethodPost, path, streamRequest)
			require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, "path %s", path)
		}
	})

	t.Run("reject leaves non-streaming requests alone", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: stream-reject-passthrough
providers:
  perplexity:
    stream: reject
    answer: hello
`))
		for _, request := range []string{
			sonarRequest,
			`{"model":"sonar","messages":[{"role":"user","content":"hi"}],"stream":false}`,
		} {
			resp, body := s.do(t, http.MethodPost, "/v1/sonar", request)
			require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
			require.False(t, hasCode(s.findings(t), CodeStreamUnimplemented))
		}
	})

	t.Run("a rejected request consumes no fault attempt", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: stream-reject-budget
providers:
  perplexity:
    stream: reject
    fault:
      attempts:
        - status: 429
        - status: 200
    answer: recovered
`))
		resp, _ := s.do(t, http.MethodPost, "/v1/sonar", streamRequest)
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

		// The 429 is still waiting: the streaming rejection must not have eaten it.
		resp, _ = s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
		require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	})
}

// TestStreamPolicyValidation pins what startup validation says about the field.
// A value outside the enum is an error, because the silent fallback is warn —
// the permissive behaviour the author was trying to switch off — and a policy
// declared on a later turn is a warning, because only the first turn's value can
// be honoured.
func TestStreamPolicyValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		source   string
		wantCode string
		wantSev  scenario.Severity
	}{
		{
			name: "reject is accepted",
			source: `
version: 1
name: ok
providers:
  perplexity:
    stream: reject
    answer: hello
`,
		},
		{
			name: "warn is accepted",
			source: `
version: 1
name: ok
providers:
  perplexity:
    stream: warn
    answer: hello
`,
		},
		{
			name: "an unknown value is an error",
			source: `
version: 1
name: typo
providers:
  perplexity:
    stream: rejct
    answer: hello
`,
			wantCode: scenario.CodeStreamPolicyUnknown, wantSev: scenario.SeverityError,
		},
		{
			name: "a later turn's policy is reported as ignored",
			source: `
version: 1
name: late-policy
providers:
  perplexity:
    turns:
      - when:
          call_index: 0
        respond:
          answer: first
      - respond:
          stream: reject
          answer: second
`,
			wantCode: scenario.CodeStreamPolicyIgnored, wantSev: scenario.SeverityWarning,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := mustScenario(t, tc.source)
			findings := provider.ValidateScenario(sc, map[string]provider.Validator{
				NameSonar: SonarValidator{},
			})

			if tc.wantCode == "" {
				require.Empty(t, findings)
				return
			}
			var found *scenario.Finding
			for i := range findings {
				if findings[i].Code == tc.wantCode {
					found = &findings[i]
				}
			}
			require.NotNil(t, found, "findings: %+v", findings)
			require.Equal(t, tc.wantSev, found.Severity)
			require.Contains(t, found.Path, ".stream")
		})
	}
}

// TestSurfacesFaultIndependently is the migration-fallback case the two fault
// keys exist for: Sonar rate-limited while the Agent surface stays healthy.
func TestSurfacesFaultIndependently(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: sonar-down
providers:
  perplexity:
    fault:
      after: repeat_last
      attempts:
        - status: 429
    answer: never reached
  perplexity_agent:
    answer: the successor answered
`))

	resp, _ := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	resp, body := s.do(t, http.MethodPost, "/v1/agent", agentRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "the successor answered")

	// And again, to prove the Agent surface never drew on Sonar's budget.
	resp, _ = s.do(t, http.MethodPost, "/v1/responses", agentRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestFaultBodiesAreSurfaceShaped pins the error envelope a declared fault
// produces on each surface. They differ, and unifying them would be wrong for
// both: Sonar has no published body and the Agent API publishes ErrorInfo.
func TestFaultBodiesAreSurfaceShaped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		path     string
		request  string
		status   int
		golden   string
	}{
		{"sonar 400", "perplexity", "/v1/sonar", sonarRequest, 400, "perplexity-sonar-400.json"},
		{"sonar 429", "perplexity", "/v1/sonar", sonarRequest, 429, "perplexity-sonar-429.json"},
		{"sonar 500", "perplexity", "/v1/sonar", sonarRequest, 500, "perplexity-sonar-500.json"},
		{"agent 400", "perplexity_agent", "/v1/agent", agentRequest, 400, "perplexity-agent-400.json"},
		{"agent 403", "perplexity_agent", "/v1/agent", agentRequest, 403, "perplexity-agent-403.json"},
		{"agent 429", "perplexity_agent", "/v1/agent", agentRequest, 429, "perplexity-agent-429.json"},
		{"agent 500", "perplexity_agent", "/v1/agent", agentRequest, 500, "perplexity-agent-500.json"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, `
version: 1
name: faulted
providers:
  `+tc.provider+`:
    fault:
      after: repeat_last
      attempts:
        - status: `+itoa(tc.status)+`
`))
			resp, body := s.do(t, http.MethodPost, tc.path, tc.request)
			require.Equal(t, tc.status, resp.StatusCode)
			require.Equal(t, string(goldenBytes(t, tc.golden)), string(body))
		})
	}
}

// TestFaultBodyOverrides pins the precedence a fault attempt's own fields have
// over the surface's default error text: a spelled-out body wins outright,
// because an author who wrote one meant it, and `error:` fills the message slot
// inside the surface's own envelope rather than replacing the envelope.
func TestFaultBodyOverrides(t *testing.T) {
	t.Parallel()

	t.Run("error fills the envelope's message", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: fault-message
providers:
  perplexity_agent:
    fault:
      after: repeat_last
      attempts:
        - status: 429
          error: Slow down, you have used this month's quota.
`))
		resp, body := s.do(t, http.MethodPost, "/v1/agent", agentRequest)
		require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
		require.Equal(t,
			`{"error":{"code":"rate_limit_exceeded","message":"Slow down, you have used this month's quota.",`+
				`"type":"rate_limit_error"}}`,
			string(body))
	})

	t.Run("a spelled-out body wins outright", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: fault-body
providers:
  perplexity:
    fault:
      after: repeat_last
      attempts:
        - status: 503
          retry_after: 7
          body:
            detail: The service is briefly unavailable.
`))
		resp, body := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		require.Equal(t, "7", resp.Header.Get("Retry-After"))
		require.JSONEq(t, `{"detail":"The service is briefly unavailable."}`, string(body))
	})
}

// itoa keeps the scenario templates above readable without importing strconv
// into every call site.
func itoa(n int) string {
	return string([]byte{byte('0' + n/100), byte('0' + n/10%10), byte('0' + n%10)})
}

// TestMultiTurnScript proves the handler works for a scripted conversation and
// not only for the single-shot form: turns are matched in declaration order, the
// first whose predicate holds wins, and the unconditional turn is the fallback.
func TestMultiTurnScript(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: scripted-loop
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  perplexity:
    turns:
      - when:
          call_index: 0
        respond:
          answer: Let me search for that.
      - when:
          body_contains: report-a
        respond:
          answer: Report A says deterministic simulation removes flakiness.
          citations: [source-a]
      - respond:
          answer: I don't know.
`))

	// Call 0 matches on call_index even though its body would also match the
	// second turn's substring, because turns are evaluated in order.
	_, body := s.do(t, http.MethodPost, "/v1/sonar",
		`{"model":"sonar","messages":[{"role":"user","content":"tell me about report-a"}]}`)
	require.Contains(t, string(body), "Let me search for that.")

	_, body = s.do(t, http.MethodPost, "/v1/sonar",
		`{"model":"sonar","messages":[{"role":"user","content":"tell me about report-a"}]}`)
	require.Contains(t, string(body), "Report A says")
	require.Contains(t, string(body), `"citations":["https://example.test/report-a"]`)

	// Nothing matches on call 2, so the unconditional turn answers.
	_, body = s.do(t, http.MethodPost, "/v1/sonar",
		`{"model":"sonar","messages":[{"role":"user","content":"something else"}]}`)
	require.Contains(t, string(body), "I don't know.")

	// The alias draws on the same turn cursor, because it draws on the same
	// counter: one counter, so the script and the fault plan cannot disagree
	// about which call this is.
	_, body = s.do(t, http.MethodPost, "/chat/completions",
		`{"model":"sonar","messages":[{"role":"user","content":"something else"}]}`)
	require.Contains(t, string(body), "I don't know.")
}

// TestMultiTurnAgent proves the Agent surface runs a script of its own, on its
// own cursor.
func TestMultiTurnAgent(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: scripted-agent
providers:
  perplexity_agent:
    turns:
      - when:
          call_index: 0
        respond:
          answer: first
      - respond:
          answer: later
`))

	_, body := s.do(t, http.MethodPost, "/v1/agent", agentRequest)
	require.Contains(t, string(body), `"text":"first"`)

	_, body = s.do(t, http.MethodPost, "/v1/responses", agentRequest)
	require.Contains(t, string(body), `"text":"later"`)
}

// TestZeroDepsServesEmptySuccesses pins the promise New's documentation makes:
// a handler built with no scenario, no journal and no fault engine still answers
// every route with a well-shaped body rather than a 404 from turn selection.
func TestZeroDepsServesEmptySuccesses(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(New(provider.Deps{}))
	t.Cleanup(srv.Close)
	s := &sim{server: srv, journal: journal.NewRing(1, 1)}

	resp, body := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `"object":"chat.completion"`)
	require.Contains(t, string(body), `"cost":{`)

	resp, body = s.do(t, http.MethodPost, "/v1/agent", agentRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `"object":"response"`)
	require.Contains(t, string(body), `"currency":"USD"`)
}

// TestDeterminism is house rule 2 made executable: the same scenario and the
// same request must produce byte-identical bodies, twenty times running.
func TestDeterminism(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, sonarScenario))

	_, first := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	for range 20 {
		_, again := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
		require.Equal(t, string(first), string(again))
	}
}

// TestDerivedIdentifiersAreStable pins §3.1 in its precise form: an identifier
// is stable AT A CALL POSITION, not across calls.
//
// The completion id folds in the lane's call index, because a real vendor issues
// a distinct id per call, so two consecutive calls differ by design — see
// TestDerivedIdentifiersAreDistinctPerCall in render_test.go. What must not move
// is call 0 of one lane against call 0 of another: a namespace is a fresh state
// lane, and a new process or an admin reset puts a request there just the same.
func TestDerivedIdentifiersAreStable(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: derived-ids
providers:
  perplexity:
    answer: hello
  perplexity_agent:
    answer: hello
`))

	_, first := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	_, fresh := s.do(t, http.MethodPost, "/n/second-lane/v1/sonar", sonarRequest)
	require.Equal(t, string(first), string(fresh),
		"call 0 of a fresh lane must reproduce call 0 byte for byte")

	var sonar CompletionResponse
	require.NoError(t, json.Unmarshal(first, &sonar))
	require.Len(t, sonar.ID, 36, "the completion id is an unprefixed UUID: %q", sonar.ID)

	// ResponsesResponse.Output is a closed interface, so the envelope is read
	// back through a decode target rather than through the render type.
	_, agentBody := s.do(t, http.MethodPost, "/v1/agent", agentRequest)
	var agent struct {
		ID     string `json:"id"`
		Output []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal(agentBody, &agent))
	require.True(t, strings.HasPrefix(agent.ID, "resp_"), "agent id: %q", agent.ID)
	require.Len(t, agent.ID, len("resp_")+32)
	require.Len(t, agent.Output, 1)
	require.Equal(t, OutputTypeMessage, agent.Output[0].Type)
	require.True(t, strings.HasPrefix(agent.Output[0].ID, "msg_"), "message id: %q", agent.Output[0].ID)
	require.Len(t, agent.Output[0].ID, len("msg_")+32)
}

// TestSunsetIsNotAPerRequestFinding guards §6.3's decision. The sunset is a
// property of the simulated API, not of any request, and per-request noise would
// drown the findings a consumer can act on.
func TestSunsetIsNotAPerRequestFinding(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, sonarScenario))
	s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)

	for _, f := range s.findings(t) {
		require.NotContains(t, f.Code, "sunset")
	}
	require.False(t, SunsetDate.IsZero())
}
