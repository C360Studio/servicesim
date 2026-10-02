package exa

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
)

// The agent routes answer errors with AgentErrorResponse — {error: {type, code,
// message}} (openapi lines 6044-6083, retrieved 2026-10-01) — not the flat
// {requestId, error, tag} the other Exa routes use. The spec lists the status
// codes each operation can return and closes the type and code enums, but it
// does NOT pair a type and code with a status: that pairing is INFERENCE, pinned
// here so a change to it is deliberate.

var (
	agentErrorTypes = []string{"INVALID_REQUEST", "AUTHENTICATION_ERROR", "RATE_LIMIT_ERROR", "NOT_FOUND", "SERVER_ERROR"}
	agentErrorCodes = []string{
		"INVALID_REQUEST", "TEAM_NOT_FOUND", "RUN_NOT_FOUND", "PREVIOUS_RUN_NOT_FOUND", "PREVIOUS_RUN_NOT_COMPLETED",
		"CONCURRENCY_LIMIT_REACHED", "INVALID_OUTPUT_SCHEMA", "INVALID_DATA_SOURCE", "TIMEOUT", "SERVER_ERROR",
	}
)

// decodeAgentError asserts the body is exactly an AgentErrorResponse — one
// `error` key holding exactly type, code and message — and returns the three
// strings. The enum check is skipped for a body a scenario scripted verbatim.
func decodeAgentError(t *testing.T, rec *httptest.ResponseRecorder, enums bool) (typ, code, message string) {
	t.Helper()
	return decodeAgentErrorBody(t, rec.Body.Bytes(), enums)
}

func decodeAgentErrorBody(t *testing.T, body []byte, enums bool) (typ, code, message string) {
	t.Helper()

	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &top), "body: %s", body)
	require.Len(t, top, 1, "AgentErrorResponse has only an `error` key: %s", body)
	require.Contains(t, top, "error")

	var inner map[string]any
	require.NoError(t, json.Unmarshal(top["error"], &inner), "`error` is an object, not the flat string: %s", body)
	assert.ElementsMatch(t, []string{"type", "code", "message"}, keysOf(inner))

	typ, _ = inner["type"].(string)
	code, _ = inner["code"].(string)
	message, _ = inner["message"].(string)
	if enums {
		assert.Contains(t, agentErrorTypes, typ)
		assert.Contains(t, agentErrorCodes, code)
	}
	assert.NotEmpty(t, message)
	return typ, code, message
}

// assertRequestIDHeader asserts the documented x-request-id response header is a
// 32-hex id and returns it.
func assertRequestIDHeader(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	id := rec.Header().Get("x-request-id")
	assert.Regexp(t, hex32, id, "x-request-id is documented on every agent response")
	return id
}

func TestAgentErrorEnvelopeForRejectedRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		req      request
		setup    func(t *testing.T, s *sim) // optional, runs first
		store    *jobs.Registry
		status   int
		typ      string
		code     string
		contains string // a substring the message must carry, "" for none
	}{
		{
			name:   "create without a credential",
			req:    request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`, noAuth: true},
			status: http.StatusUnauthorized, typ: "AUTHENTICATION_ERROR", code: "TEAM_NOT_FOUND",
		},
		{
			name:   "poll without a credential",
			req:    request{method: http.MethodGet, path: "/agent/runs/agent_run_x", noAuth: true},
			status: http.StatusUnauthorized, typ: "AUTHENTICATION_ERROR", code: "TEAM_NOT_FOUND",
		},
		{
			name:   "create without a query",
			req:    request{method: http.MethodPost, path: "/agent/runs", body: `{}`},
			status: http.StatusBadRequest, typ: "INVALID_REQUEST", code: "INVALID_REQUEST",
			contains: "query is required",
		},
		{
			name:   "create with an effort outside the enum",
			req:    request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q","effort":"max"}`},
			status: http.StatusBadRequest, typ: "INVALID_REQUEST", code: "INVALID_REQUEST",
			contains: "effort",
		},
		{
			name:   "create with a budget out of range",
			req:    request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q","effort":"ultra","budget":{"maxDurationSeconds":1}}`},
			status: http.StatusBadRequest, typ: "INVALID_REQUEST", code: "INVALID_REQUEST",
			contains: "maxDurationSeconds",
		},
		{
			name:   "poll for a run that does not exist",
			req:    request{method: http.MethodGet, path: "/agent/runs/agent_run_neverminted"},
			status: http.StatusNotFound, typ: "NOT_FOUND", code: "RUN_NOT_FOUND",
		},
		{
			name:  "create past the job bound",
			store: jobs.NewRegistry(jobs.Limits{MaxJobs: 1}),
			setup: func(t *testing.T, s *sim) {
				t.Helper()
				createRun(t, s, `{"query":"first"}`)
			},
			req:    request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"second"}`},
			status: http.StatusServiceUnavailable, typ: "SERVER_ERROR", code: "SERVER_ERROR",
			contains: "maximum of 1 jobs",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := tc.store
			if store == nil {
				store = jobs.NewRegistry(jobs.Limits{})
			}
			s := newSimWithJobs(t, asyncScenario, store)
			if tc.setup != nil {
				tc.setup(t, s)
			}

			rec := s.do(tc.req)
			require.Equal(t, tc.status, rec.Code, "body: %s", rec.Body.String())
			typ, code, message := decodeAgentError(t, rec, true)
			assert.Equal(t, tc.typ, typ)
			assert.Equal(t, tc.code, code)
			assert.Contains(t, message, tc.contains)
			assertRequestIDHeader(t, rec)
		})
	}
}

// The no-matching-turn 404 is the same RUN_NOT_FOUND envelope as an unknown id.
func TestAgentErrorEnvelopeForAnExhaustedScript(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, `
version: 1
name: exhausted
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
`)
	id := createRun(t, s, `{"query":"q"}`)
	pollRun(t, s, id)

	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id})
	require.Equal(t, http.StatusNotFound, rec.Code)
	typ, code, _ := decodeAgentError(t, rec, true)
	assert.Equal(t, "NOT_FOUND", typ)
	assert.Equal(t, "RUN_NOT_FOUND", code)
}

// A scripted fault status becomes the nested envelope on both routes, with the
// type and code the pairing table gives it.
func TestAgentErrorEnvelopeForFaultInjectedStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status int
		typ    string
		code   string
	}{
		{http.StatusBadRequest, "INVALID_REQUEST", "INVALID_REQUEST"},
		{http.StatusUnauthorized, "AUTHENTICATION_ERROR", "TEAM_NOT_FOUND"},
		{http.StatusNotFound, "NOT_FOUND", "RUN_NOT_FOUND"},
		{http.StatusTooManyRequests, "RATE_LIMIT_ERROR", "CONCURRENCY_LIMIT_REACHED"},
		{http.StatusInternalServerError, "SERVER_ERROR", "SERVER_ERROR"},
		// Statuses the spec does not document for these operations are simulator
		// policy: any other 4xx is INVALID_REQUEST, any 5xx SERVER_ERROR.
		{http.StatusPaymentRequired, "INVALID_REQUEST", "INVALID_REQUEST"},
		{http.StatusServiceUnavailable, "SERVER_ERROR", "SERVER_ERROR"},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			t.Parallel()

			// create.fault is the create route's plan; the turn's fault is the poll's.
			s := asyncSim(t, `
version: 1
name: faulted
providers:
  exa_agent_runs:
    create:
      fault:
        attempts:
          - {status: `+strconv.Itoa(tc.status)+`}
    turns:
      - fault:
          attempts:
            - {status: `+strconv.Itoa(tc.status)+`}
        respond: {status: running}
`)
			create := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
			require.Equal(t, tc.status, create.Code)
			typ, code, _ := decodeAgentError(t, create, true)
			assert.Equal(t, tc.typ, typ)
			assert.Equal(t, tc.code, code)
			assertRequestIDHeader(t, create)

			// The create budget is spent; the next create serves the scenario.
			id := createRun(t, s, `{"query":"q"}`)

			poll := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id})
			require.Equal(t, tc.status, poll.Code)
			typ, code, _ = decodeAgentError(t, poll, true)
			assert.Equal(t, tc.typ, typ)
			assert.Equal(t, tc.code, code)
			assertRequestIDHeader(t, poll)
		})
	}
}

// A fault attempt's `error:` and `tag:` are the flat envelope's fields. On the
// nested envelope `error:` becomes message and `tag:` becomes code, verbatim —
// even off the spec's enum, so a scenario can test a consumer's handling of a
// code the vendor adds later. type stays derived from the status.
func TestAgentFaultErrorAndTagMapOntoMessageAndCode(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: faulted
providers:
  exa_agent_runs:
    create:
      fault:
        attempts:
          - {status: 429, error: "slow down", tag: CONCURRENCY_LIMIT_REACHED}
          - {status: 500, tag: SOMETHING_NEW}
          - {status: 400, error: "bad shape"}
    turns:
      - respond: {status: running}
`
	s := asyncSim(t, src)

	// SOMETHING_NEW is outside the enum: it is allowed, and it is warned about.
	sc := mustScenario(t, src)
	var warned []string
	for _, f := range provider.ValidateScenario(sc, map[string]provider.Validator{NameAgentRuns: agentRunValidator{}}) {
		if f.Code == codeAgentRunFaultTagUnknown {
			warned = append(warned, f.Path)
		}
	}
	require.Len(t, warned, 1, "only SOMETHING_NEW is outside the enum")
	assert.Contains(t, warned[0], "attempts[1].tag")

	first := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
	require.Equal(t, http.StatusTooManyRequests, first.Code)
	typ, code, message := decodeAgentError(t, first, true)
	assert.Equal(t, "RATE_LIMIT_ERROR", typ)
	assert.Equal(t, "CONCURRENCY_LIMIT_REACHED", code)
	assert.Equal(t, "slow down", message)

	second := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
	typ, code, _ = decodeAgentError(t, second, false)
	assert.Equal(t, "SERVER_ERROR", typ, "type is derived from the status, never from tag")
	assert.Equal(t, "SOMETHING_NEW", code, "tag is emitted verbatim as code")

	third := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
	typ, code, message = decodeAgentError(t, third, true)
	assert.Equal(t, "INVALID_REQUEST", typ)
	assert.Equal(t, "INVALID_REQUEST", code, "an absent tag leaves the status's default code")
	assert.Equal(t, "bad shape", message)
}

// The verbatim tag -> code mapping stays, but a tag outside the spec's ten-member
// AgentError code enum is warned about at load: an adopter scenario carrying a
// flat-envelope tag (RATE_LIMIT, INTERNAL, INVALID_API_KEY) would otherwise
// silently render `code: "RATE_LIMIT"`. It is a warning, never an error, because
// scripting an off-enum code is allowed on purpose.
func TestAgentRunValidatorWarnsOnAFaultTagOutsideTheCodeEnum(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string // the exa_agent_runs entry, indented under `providers:`
		want []string
	}{
		{
			name: "create fault with a flat-envelope tag",
			body: "    create:\n      fault:\n        attempts:\n          - {status: 429, tag: RATE_LIMIT}\n" +
				"    turns:\n      - respond: {status: running}\n",
			want: []string{"create.fault.attempts[0].tag"},
		},
		{
			name: "poll fault with a flat-envelope tag",
			body: "    turns:\n      - fault:\n          attempts:\n            - {status: 500, tag: INTERNAL}\n" +
				"        respond: {status: running}\n",
			want: []string{"turns[0].fault.attempts[0].tag"},
		},
		{
			name: "single-shot block fault",
			body: "    fault:\n      attempts:\n        - {status: 401, tag: INVALID_API_KEY}\n    status: running\n",
			want: []string{"fault.attempts[0].tag"},
		},
		{
			name: "only the off-enum attempts warn",
			body: "    create:\n      fault:\n        attempts:\n          - {status: 429, tag: CONCURRENCY_LIMIT_REACHED}\n" +
				"          - {status: 500, tag: SOMETHING_NEW}\n          - {status: 500}\n" +
				"    turns:\n      - respond: {status: running}\n",
			want: []string{"create.fault.attempts[1].tag"},
		},
		{
			name: "every enum member is quiet",
			body: "    create:\n      fault:\n        attempts:\n" +
				"          - {status: 400, tag: INVALID_REQUEST}\n          - {status: 401, tag: TEAM_NOT_FOUND}\n" +
				"          - {status: 404, tag: RUN_NOT_FOUND}\n          - {status: 400, tag: PREVIOUS_RUN_NOT_FOUND}\n" +
				"          - {status: 400, tag: PREVIOUS_RUN_NOT_COMPLETED}\n" +
				"          - {status: 429, tag: CONCURRENCY_LIMIT_REACHED}\n" +
				"          - {status: 400, tag: INVALID_OUTPUT_SCHEMA}\n          - {status: 400, tag: INVALID_DATA_SOURCE}\n" +
				"          - {status: 500, tag: TIMEOUT}\n          - {status: 500, tag: SERVER_ERROR}\n" +
				"    turns:\n      - respond: {status: running}\n",
		},
		{
			name: "a tag beside a verbatim body is never rendered, so it is quiet",
			body: "    create:\n      fault:\n        attempts:\n          - {status: 500, tag: RATE_LIMIT, body: {custom: shape}}\n" +
				"    turns:\n      - respond: {status: running}\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sc := mustScenario(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n"+tc.body)
			findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameAgentRuns: agentRunValidator{}})

			var got []string
			for _, f := range findings {
				if f.Code != codeAgentRunFaultTagUnknown {
					continue
				}
				assert.Equal(t, "warning", string(f.Severity), "an off-enum tag is allowed on purpose")
				assert.Contains(t, f.Message, "verbatim")
				got = append(got, f.Path)
			}
			require.Len(t, got, len(tc.want), "findings: %+v", findings)
			for i, suffix := range tc.want {
				assert.Contains(t, got[i], suffix)
			}
		})
	}
}

// An attempt that declares its own `body:` still wins verbatim.
func TestAgentFaultBodyStillWinsVerbatim(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, `
version: 1
name: faulted
providers:
  exa_agent_runs:
    create:
      fault:
        attempts:
          - {status: 500, body: {custom: shape}}
    turns:
      - respond: {status: running}
`)
	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"custom":"shape"}`, rec.Body.String())
}

// x-request-id is documented on every agent response, success and error alike
// (XRequestId, openapi line 11084). Agent bodies carry no requestId, so the
// header is the only request id the response has. It is derived, never random:
// the same scenario at the same call position yields the same id.
func TestAgentResponsesCarryADeterministicRequestIDHeader(t *testing.T) {
	t.Parallel()

	walk := func() (create, poll, head, miss, rejected string) {
		s := asyncSim(t, asyncScenario)

		createRec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
		require.Equal(t, http.StatusOK, createRec.Code)
		create = assertRequestIDHeader(t, createRec)

		var out struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &out))

		pollRec := s.do(request{method: http.MethodGet, path: "/agent/runs/" + out.ID})
		require.Equal(t, http.StatusOK, pollRec.Code)
		poll = assertRequestIDHeader(t, pollRec)

		headRec := s.do(request{method: http.MethodHead, path: "/agent/runs/" + out.ID})
		require.Equal(t, http.StatusOK, headRec.Code)
		head = assertRequestIDHeader(t, headRec)

		missRec := s.do(request{method: http.MethodHead, path: "/agent/runs/agent_run_neverminted"})
		require.Equal(t, http.StatusNotFound, missRec.Code)
		miss = assertRequestIDHeader(t, missRec)

		rejectedRec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{}`})
		require.Equal(t, http.StatusBadRequest, rejectedRec.Code)
		rejected = assertRequestIDHeader(t, rejectedRec)
		return create, poll, head, miss, rejected
	}

	create, poll, head, miss, rejected := walk()
	create2, poll2, head2, miss2, rejected2 := walk()
	assert.Equal(t, []string{create, poll, head, miss, rejected}, []string{create2, poll2, head2, miss2, rejected2},
		"a second run of the same walk reproduces every id")
	assert.NotEqual(t, create, poll, "consecutive claimed calls carry distinct ids")
}

// The 404 for an unknown run and the 401 for a missing credential are not
// claimed attempts, and neither may consume one.
func TestAgentRunNotFoundClaimsNoPoll(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	id := createRun(t, s, `{"query":"q"}`)

	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/agent_run_neverminted"})
	require.Equal(t, http.StatusNotFound, rec.Code)
	decodeAgentError(t, rec, true)

	assert.Equal(t, statusRunning, pollRun(t, s, id)["status"], "the miss consumed no poll")
}

// The other Exa routes keep the flat {requestId, error, tag} envelope.
func TestNonAgentRoutesKeepTheFlatErrorEnvelope(t *testing.T) {
	t.Parallel()

	s := newSim(t, twoSourceScenario)
	rec := s.do(request{path: "/search", body: `{"query":"q"}`, noAuth: true})
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	var got errorResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, tagInvalidAPIKey, got.Tag)
	assert.Regexp(t, hex32, got.RequestID)
	assert.Empty(t, rec.Header().Get("x-request-id"), "the header is documented for the agent routes only")
}

// A route that is not one of the four agent routes — an agent operation the
// profile does not simulate — is refused by the mux in the flat shape, with no
// x-request-id: only the routed agent operations speak AgentErrorResponse. Which
// refusal it is depends on whether the path is routed for another method: the list
// (GET /agent/runs), DELETE /agent/runs/{id} and a GET of the cancel path share a
// path with a routed operation, so the mux answers 405 and names the methods it
// does serve; the stop and events sub-resources are not routed at all and answer
// 404. stop is a separate, ultra-only operation and is deliberately not aliased to
// cancel.
func TestUnroutedAgentOperationsKeepTheFlatRefusal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string // %s is a minted run id
		wantStatus int
		wantTag    string
		wantAllow  string
	}{
		{"list", http.MethodGet, "/agent/runs", http.StatusMethodNotAllowed, tagMethodNotAllowed, "POST"},
		{"delete", http.MethodDelete, "/agent/runs/%s", http.StatusMethodNotAllowed, tagMethodNotAllowed, "GET, HEAD"},
		{"get on the cancel path", http.MethodGet, "/agent/runs/%s/cancel", http.StatusMethodNotAllowed, tagMethodNotAllowed, "POST"},
		{"stop", http.MethodPost, "/agent/runs/%s/stop", http.StatusNotFound, tagNotFound, ""},
		{"events", http.MethodGet, "/agent/runs/%s/events", http.StatusNotFound, tagNotFound, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := asyncSim(t, asyncScenario)
			id := createRun(t, s, `{"query":"q"}`)

			path := tc.path
			if strings.Contains(path, "%s") {
				path = fmt.Sprintf(path, id)
			}
			rec := s.do(request{method: tc.method, path: path, body: `{}`})
			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantAllow, rec.Header().Get("Allow"))
			assert.Empty(t, rec.Header().Get("x-request-id"))

			var got errorResponseWire
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, tc.wantTag, got.Tag)
		})
	}
}

// A handler panic on an agent route answers in the nested envelope too, since the
// route is an agent route, and a panic on any other Exa route keeps the flat one.
// This goes through the production refusal path — the mux's panic recovery
// building a provider.Refusal for the profile's own ErrorBody — with one handler
// swapped for a panicking stub, not through a hand-built Exchange.
func TestAgentRefusalBodyIsNestedOnAgentRoutes(t *testing.T) {
	t.Parallel()

	panics := func(*provider.Exchange) provider.Response { panic("boom") }

	serve := func(t *testing.T, pattern, method, path string) *httptest.ResponseRecorder {
		t.Helper()

		sc := mustScenario(t, asyncScenario)
		p := Profile()
		p.Handlers[pattern] = panics
		h := p.Handler(provider.Deps{
			Scenario:  sc,
			Journal:   journal.NewRing(8, 1<<16),
			Faults:    provider.MustSet(Profile()).Faults(sc),
			DelayMode: provider.DelaySkip,
			Jobs:      jobs.NewRegistry(jobs.Limits{}),
		})

		req := httptest.NewRequest(method, path, strings.NewReader(`{"query":"q"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", "test-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, tc := range []struct{ name, pattern, method, path string }{
		{"create", patternRunCreate, http.MethodPost, "/agent/runs"},
		{"poll", patternRunPoll, http.MethodGet, "/agent/runs/agent_run_x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := serve(t, tc.pattern, tc.method, tc.path)
			require.Equal(t, http.StatusInternalServerError, rec.Code)
			typ, code, _ := decodeAgentError(t, rec, true)
			assert.Equal(t, "SERVER_ERROR", typ)
			assert.Equal(t, "SERVER_ERROR", code)
			// The body is nested, but a refusal rendered by the framework has no
			// header hook: a panic carries no x-request-id.
			assert.Empty(t, rec.Header().Get("x-request-id"))
		})
	}

	t.Run("search keeps the flat envelope", func(t *testing.T) {
		t.Parallel()

		rec := serve(t, patternSearch, http.MethodPost, "/search")
		require.Equal(t, http.StatusInternalServerError, rec.Code)

		var got errorResponseWire
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, tagInternalError, got.Tag)
	})
}

// x-request-id is derived per call, not per route. Two creates are two call
// positions on the create lane, two polls of one job are two on that job's lane,
// and the first polls of two different jobs — both call 0 of their own lanes —
// must not collide either, or a consumer correlating logs by this header would see
// one id on two requests.
func TestAgentRequestIDHeaderDiffersPerCall(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	create := func() (id, header string) {
		rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
		require.Equal(t, http.StatusOK, rec.Code)
		var out struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out.ID, assertRequestIDHeader(t, rec)
	}
	poll := func(id string) string {
		rec := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id})
		require.Equal(t, http.StatusOK, rec.Code)
		return assertRequestIDHeader(t, rec)
	}

	firstID, firstCreate := create()
	secondID, secondCreate := create()
	assert.NotEqual(t, firstCreate, secondCreate, "two creates")

	firstPoll := poll(firstID)
	assert.NotEqual(t, firstPoll, poll(firstID), "two polls of one job")
	assert.NotEqual(t, firstPoll, poll(secondID), "the first polls of two jobs")
}

// HEAD claims nothing: not a poll on the job's lane, and not an attempt on its own
// either. The journal records the attempt each request claimed, and -1 means the
// route claimed none.
func TestAgentRunHeadClaimsNoAttemptOnAnyLane(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	id := createRun(t, s, `{"query":"q"}`)

	for _, path := range []string{"/agent/runs/" + id, "/agent/runs/agent_run_neverminted"} {
		for range 2 {
			s.do(request{method: http.MethodHead, path: path})
		}
	}

	var heads int
	for _, entry := range s.journal.Snapshot() {
		if entry.Method != http.MethodHead {
			continue
		}
		heads++
		assert.Equal(t, -1, entry.Outcome.AttemptIndex, "HEAD %s claimed an attempt on lane %q", entry.Path,
			entry.Outcome.FaultKey)
	}
	assert.Equal(t, 4, heads)

	// And the job's own lanes are untouched: its first poll is still call 0.
	assert.Equal(t, statusRunning, pollRun(t, s, id)["status"])
	poll := s.journal.Snapshot()
	assert.Equal(t, 0, poll[len(poll)-1].Outcome.AttemptIndex)
}
