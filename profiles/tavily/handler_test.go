package tavily

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/contracts"
	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/scenarios"
)

// bearer is the credential every request in this file presents. It is a fake
// key in the vendor's documented shape; nothing here ever reaches a real API.
const bearer = "Bearer tvly-test-key"

// goldenScenario projects exactly the corpus contracts/tavily's goldens were
// written from. request_id and results[].id are pinned to the goldens' values,
// because those two are derived from the scenario seed and a golden cannot
// carry a digest of a fixture it does not contain.
const goldenScenario = `
version: 1
name: golden
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    favicon: https://example.test/favicon.ico
    published_at: 2025-11-16T01:36:32.547Z
    snippets:
      - Report A finds that deterministic simulators remove flakiness from adapter test suites.
  - id: source-b
    url: https://example.test/report-b
    title: Report B
    snippets:
      - Report B corroborates Report A across a second dataset.
providers:
  tavily:
    request_id: 123e4567-e89b-12d3-a456-426614174000
    answer: Both reports agree that deterministic simulation removes flakiness from adapter suites.
    response_time: 1.15
    results:
      - source: source-a
        id: a3f9c2-01
        score: 0.93
      - source: source-b
        id: a3f9c2-02
        score: 0.71
`

// TestSearchGoldenWire compares the rendered bytes against the fixtures in
// contracts/tavily, which are the authority on every field name and JSON type.
//
// The comparison is at the byte level, not through a round-tripped struct: a
// wrong JSON type — response_time as a string, images as bare URLs, score as a
// quoted number — survives a permissive decoder unchanged and would pass a
// struct-level assertion while breaking every typed consumer.
func TestSearchGoldenWire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scenario string
		request  string
		golden   string
	}{
		{
			name:     "happy",
			scenario: goldenScenario,
			request:  `{"query":"deterministic simulator adapter tests","include_answer":true}`,
			golden:   "tavily-search-happy.json",
		},
		{
			name: "empty results",
			scenario: `
version: 1
name: golden-empty
providers:
  tavily:
    request_id: 4b1f9a3c-2d5e-5a7b-9c0d-1e2f3a4b5c6d
    response_time: 0.42
    results: []
`,
			request: `{"query":"deterministic simulator adapter tests"}`,
			golden:  "tavily-search-empty.json",
		},
		{
			name: "topic news with every optional field",
			scenario: `
version: 1
name: golden-news
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    favicon: https://example.test/favicon.ico
    published_at: 2025-11-16T01:36:32.547Z
    snippets:
      - Report A finds that deterministic simulators remove flakiness from adapter test suites.
providers:
  tavily:
    request_id: 5c2a0b4d-3e6f-5b8c-ad1e-2f3a4b5c6d7e
    answer: Report A is the most recent coverage of deterministic simulation.
    response_time: 2.03
    images:
      - url: https://example.test/report-a/cover.png
        description: Cover image of Report A
    results:
      - source: source-a
        id: a3f9c2-01
        score: 0.93
        raw_content: >-
          Report A finds that deterministic simulators remove flakiness from
          adapter test suites. Full text follows.
        images:
          - url: https://example.test/report-a/figure-1.png
            description: Figure 1 of Report A
`,
			request: `{
				"query": "deterministic simulator adapter tests",
				"topic": "news",
				"search_depth": "advanced",
				"include_answer": true,
				"include_images": true,
				"include_image_descriptions": true,
				"include_favicon": true,
				"include_raw_content": true,
				"include_usage": true,
				"auto_parameters": true
			}`,
			golden: "tavily-search-news.json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := post(t, newHandler(t, tc.scenario, nil), "/search", tc.request, bearer)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			require.Equal(t, goldenBytes(t, tc.golden), rec.Body.String())
		})
	}
}

// TestErrorGoldenWire covers the four error bodies a request can reach without
// a fault plan. Every one of them uses the nested {"detail":{"error":…}}
// envelope; a flat {"error":…} is what an implementer invents from the plan
// document, and no consumer decoder written against the real API parses it.
func TestErrorGoldenWire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		auth       string
		wantStatus int
		wantAllow  string
		golden     string
	}{
		{
			name: "an invalid topic is 400", method: http.MethodPost, path: "/search",
			body: `{"query":"report a","topic":"sports"}`, auth: bearer,
			wantStatus: http.StatusBadRequest, golden: "tavily-400.json",
		},
		{
			name: "a missing credential is 401", method: http.MethodPost, path: "/search",
			body: `{"query":"report a"}`, auth: "",
			wantStatus: http.StatusUnauthorized, golden: "tavily-401.json",
		},
		{
			name: "an unknown path is 404", method: http.MethodPost, path: "/nope",
			body: `{"query":"report a"}`, auth: bearer,
			wantStatus: http.StatusNotFound, golden: "tavily-404.json",
		},
		{
			name: "a known path with the wrong method is 405", method: http.MethodGet, path: "/search",
			auth: bearer, wantStatus: http.StatusMethodNotAllowed, wantAllow: "POST",
			golden: "tavily-405.json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			handler := newHandler(t, goldenScenario, nil)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, tc.wantStatus, rec.Code)
			require.Equal(t, tc.wantAllow, rec.Header().Get("Allow"))
			require.Equal(t, goldenBytes(t, tc.golden), rec.Body.String())
		})
	}
}

// TestFaultBodyGoldenWire pins the fault-driven error bodies, including the two
// non-standard statuses — 432 (plan limit) and 433 (pay-as-you-go limit) — that
// the plan document does not mention at all and that no net/http status table
// knows about.
func TestFaultBodyGoldenWire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		golden string
	}{
		{name: "429", status: http.StatusTooManyRequests, golden: "tavily-429.json"},
		{name: "432 plan limit", status: statusPlanLimitExceeded, golden: "tavily-432.json"},
		{name: "433 pay-as-you-go limit", status: statusPayGoLimitExceeded, golden: "tavily-433.json"},
		{name: "500", status: http.StatusInternalServerError, golden: "tavily-500.json"},
		{name: "401", status: http.StatusUnauthorized, golden: "tavily-401.json"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body := faultBody(scenario.FaultAttempt{Status: tc.status})
			require.Equal(t, goldenBytes(t, tc.golden), string(body))
		})
	}
}

// TestFaultBodyPrecedence proves the three ways an attempt can shape its body,
// and the one case where it must not: a fault whose point is the transport
// keeps the scenario's own payload.
//
// A `- {}` / status: 200 attempt is no longer exercised directly against
// this package's own faultBody: the "FaultBody is never even called for a
// non-error status" guarantee moved into provider/fault_exec.go (Phase 10
// unit 2), which is where it is now tested, on a hand-built profile —
// see provider's own profile_test.go
// (TestFaultBodyGuardSkipsANonErrorAttempt).
func TestFaultBodyPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("an explicit body wins outright", func(t *testing.T) {
		t.Parallel()
		body := faultBody(scenario.FaultAttempt{Status: 429, Body: map[string]any{"detail": "custom"}})
		require.JSONEq(t, `{"detail":"custom"}`, string(body))
	})

	t.Run("an error message fills the documented envelope", func(t *testing.T) {
		t.Parallel()
		body := faultBody(scenario.FaultAttempt{Status: 429, Error: "Rate limit exceeded."})
		require.Equal(t, `{"detail":{"error":"Rate limit exceeded."}}`, string(body))
	})

	t.Run("a non-status fault keeps the scenario body", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, faultBody(scenario.FaultAttempt{Kind: scenario.FaultTruncateBody}))
	})
}

// TestFaultWithErrorButNoErrorStatusServesTheScenarioBody pins a behaviour
// change Phase 10 unit 2 made, undisclosed in either implementer's report:
// before this unit, an attempt carrying `error:` with no error status (a
// bare `- error: "..."` or `- {status: 200, error: "..."}`) served this
// package's error envelope at 200, because faultBody's own (now-deleted)
// `if a.Status < 400 { return nil }` guard ran AFTER the a.Error check, not
// before it — the opposite order from exa's and perplexity's own guards,
// which already served the scenario body in this case. provider/fault_exec.go
// now gates on `a.Status >= 400 || len(a.Body) > 0` BEFORE faultBody ever
// runs (Phase 10 unit 2, "the Status < 400 guard"), so all three profiles
// agree: `error:` with no error status is a no-op attempt, and the
// scenario's own body is served. Intentional — the framework guarantee
// this unit added — not a regression; recorded in docs/adopter-backlog.md's
// Phase 10 unit 2 entry.
func TestFaultWithErrorButNoErrorStatusServesTheScenarioBody(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: error-no-status
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily:
    fault:
      attempts:
        - error: "Odd attempt with error but no status."
        - status: 200
          error: "Odd attempt with error but no status, explicit 200."
    results:
      - source: source-a
        score: 0.98
`
	loaded, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)

	handler := Profile().Handler(provider.Deps{
		Scenario: loaded,
		Faults:   provider.MustSet(Profile()).Faults(loaded),
	})

	for i := 0; i < 2; i++ {
		resp := post(t, handler, "/search", `{"query":"report a"}`, bearer)
		require.Equal(t, http.StatusOK, resp.Code)
		require.Contains(t, resp.Body.String(), `"results"`,
			"attempt %d: error: with no error status must not produce the error envelope; "+
				"the scenario's own body is served", i)
		require.NotContains(t, resp.Body.String(), "Odd attempt")
	}
}

// TestRateLimitFaultServesTheDocumentedEnvelope drives a declared fault plan
// through the real engine, because the interesting part is the seam: the
// provider package supplies the body shape and the engine supplies the status,
// the Retry-After header and the retry that follows.
func TestRateLimitFaultServesTheDocumentedEnvelope(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: rate-limited
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily:
    fault:
      attempts:
        - status: 429
          retry_after: 1
        - status: 200
    response_time: 1.15
    results:
      - source: source-a
        score: 0.98
`
	loaded, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)

	handler := Profile().Handler(provider.Deps{
		Scenario: loaded,
		Faults:   provider.MustSet(Profile()).Faults(loaded),
	})

	first := post(t, handler, "/search", `{"query":"report a"}`, bearer)
	require.Equal(t, http.StatusTooManyRequests, first.Code)
	require.Equal(t, "1", first.Header().Get("Retry-After"))
	require.Equal(t, goldenBytes(t, "tavily-429.json"), first.Body.String())

	second := post(t, handler, "/search", `{"query":"report a"}`, bearer)
	require.Equal(t, http.StatusOK, second.Code)
	require.Contains(t, second.Body.String(), `"response_time":1.15`)
}

// TestOversizedBodyFaultServesTheDocumentedEnvelope proves the real seam
// (Parse → Set.Faults → Handle) that TestFaultBodyGoldenWire's unit-level
// call cannot: an oversized_body attempt with a status override pads this
// surface's documented error envelope, not the scenario's search body. This
// is the property that a stale `EffectiveKind() != FaultStatus` guard in
// faultBody would silently miss, because a fake FaultBody in a unit test
// renders regardless of kind.
func TestOversizedBodyFaultServesTheDocumentedEnvelope(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: oversized-500
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily:
    fault:
      attempts:
        - status: 500
          body_bytes: 100
    response_time: 1.15
    results:
      - source: source-a
        score: 0.98
`
	loaded, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)

	handler := Profile().Handler(provider.Deps{
		Scenario: loaded,
		Faults:   provider.MustSet(Profile()).Faults(loaded),
	})

	rec := post(t, handler, "/search", `{"query":"report a"}`, bearer)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "100", rec.Header().Get("Content-Length"))

	trimmed := strings.TrimRight(rec.Body.String(), " ")
	require.Equal(t, goldenBytes(t, "tavily-500.json"), trimmed,
		"an oversized 500 must pad the documented error envelope, not the search body")
}

// TestMultiTurnScript proves the handler serves a scripted provider, not only
// the single-shot form: turns are matched in declaration order against the same
// call counter the fault engine draws on, and a turn with no `when` is the
// fallback.
func TestMultiTurnScript(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: scripted
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily:
    turns:
      - when:
          call_index: 0
        respond:
          answer: First answer.
          response_time: 1.0
          results: []
      - when:
          body_contains: report-b
        respond:
          answer: Report B answer.
          response_time: 2.0
          results: []
      - respond:
          answer: Fallback answer.
          response_time: 3.0
          results:
            - source: source-a
`
	handler := newHandler(t, src, nil)
	body := `{"query":"report a","include_answer":true}`

	first := decode(t, post(t, handler, "/search", body, bearer))
	require.Equal(t, "First answer.", derefAnswer(t, first))

	second := decode(t, post(t, handler, "/search", `{"query":"report-b","include_answer":true}`, bearer))
	require.Equal(t, "Report B answer.", derefAnswer(t, second))

	third := decode(t, post(t, handler, "/search", body, bearer))
	require.Equal(t, "Fallback answer.", derefAnswer(t, third))
	require.Len(t, third.Results, 1)
}

// TestNoMatchingTurnFailsClosed covers the authoring error the fallback exists
// to prevent: a script that cannot answer must say so, never serve an empty
// 200 that looks like a successful search with no results.
func TestNoMatchingTurnFailsClosed(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: unanswerable
providers:
  tavily:
    turns:
      - when:
          call_index: 7
        respond:
          answer: Unreachable on the first call.
`
	rec := post(t, newHandler(t, src, nil), "/search", `{"query":"report a"}`, bearer)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.JSONEq(t, `{"detail":{"error":"`+messageNoMatchingTurn+`"}}`, rec.Body.String())
}

// TestIdenticalRequestsRenderIdenticalBytes is determinism stated as a test, in
// its precise form: the same scenario and the same request AT THE SAME CALL
// POSITION render the same bytes. A simulator whose bodies move between two
// runs of one test makes every consumer's suite flaky, and they will blame
// their own code first.
//
// request_id folds in the lane's call index, because a real vendor issues a
// distinct one per call, so two CONSECUTIVE calls differ by design. A namespace
// is a fresh state lane, which is what puts each request below at call 0 again —
// as a new process or an admin reset would.
func TestIdenticalRequestsRenderIdenticalBytes(t *testing.T) {
	t.Parallel()

	// No request_id override and no fault plan, so every identifier in the body
	// is derived and must still reproduce exactly at the same call position.
	src := `
version: 1
name: derived
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily:
    results:
      - source: source-a
`
	handler := newHandler(t, src, nil)
	body := `{"query":"report a"}`

	first := post(t, handler, "/search", body, bearer).Body.String()
	for i := range 5 {
		path := fmt.Sprintf("/n/lane-%d/search", i)
		require.Equal(t, first, post(t, handler, path, body, bearer).Body.String())
	}
}

// TestZeroDepsServesAWellShapedEmptySuccess pins the documented zero-value
// behaviour: no scenario, no journal, no faults, and still a body a consumer's
// decoder accepts.
func TestZeroDepsServesAWellShapedEmptySuccess(t *testing.T) {
	t.Parallel()

	rec := post(t, Profile().Handler(provider.Deps{}), "/search", `{"query":"report a"}`, bearer)
	require.Equal(t, http.StatusOK, rec.Code)

	decoded := decode(t, rec)
	require.Equal(t, "report a", decoded.Query)
	require.Nil(t, decoded.Answer)
	require.NotNil(t, decoded.Images)
	require.Empty(t, decoded.Images)
	require.NotNil(t, decoded.Results)
	require.Empty(t, decoded.Results)
	require.NotEmpty(t, decoded.RequestID)

	// The required keys are present as empty arrays, not absent and not null.
	require.Contains(t, rec.Body.String(), `"images":[]`)
	require.Contains(t, rec.Body.String(), `"results":[]`)
	require.Contains(t, rec.Body.String(), `"answer":null`)
}

// TestRoutesDeclareTheFaultKeyAndSelector pins the two things internal/server
// and the fault engine read off a route.
func TestRoutesDeclareTheFaultKeyAndSelector(t *testing.T) {
	t.Parallel()

	routes := Routes()
	require.Len(t, routes, 5)
	require.Equal(t, PatternSearch, routes[0].Pattern)
	require.Equal(t, FaultKeySearch, routes[0].FaultKey)

	// Every route carries a distinct fault key: a poll retry must not consume
	// the create's retries, and an existence check must not consume either.
	keys := map[string]string{}
	for _, r := range routes {
		if prior, seen := keys[r.FaultKey]; seen {
			t.Errorf("%s shares fault key %q with %s", r.Pattern, r.FaultKey, prior)
		}
		keys[r.FaultKey] = r.Pattern
	}

	// The research routes accept DIFFERENT credential placement sets, and the
	// difference is the request shape rather than a vendor rule: the POST has a
	// body to carry an api_key, the GET does not. contracts/tavily/README.md
	// records that the vendor documents Bearer for both, correcting an earlier
	// design that claimed otherwise.
	byPattern := map[string]provider.Route{}
	for _, r := range routes {
		byPattern[r.Pattern] = r
	}
	require.Equal(t,
		[]string{provider.PlacementAuthorization, placementBodyAPIKey},
		byPattern[PatternResearchCreate].Credentials)
	require.Equal(t,
		[]string{provider.PlacementAuthorization},
		byPattern[PatternResearchPoll].Credentials,
		"a GET has no body to carry a key in")

	// /extract accepts Authorization: Bearer only: the vendor's /extract page
	// documents Bearer alone, and D2's client-level reasoning is not extended
	// to routes verified after it — contracts/tavily/README.md "POST
	// /extract" § "Auth" records the owner's 2026-08-15 reaffirmation that
	// vendor documentation, not a consumer's client, decides a wire contract.
	require.Equal(t,
		[]string{provider.PlacementAuthorization},
		byPattern[PatternExtract].Credentials,
		"a body api_key does not authenticate /extract; the vendor documents Bearer only")

	for _, p := range []string{PatternResearchCreate, PatternResearchPoll, PatternResearchHead} {
		require.Equal(t, NameResearch, byPattern[p].Entry, "%s resolves the wrong scenario entry", p)
	}

	// The selector is nil-safe on every hop, which is what lets the engine ask
	// a partial scenario for a plan it may not declare.
	require.Nil(t, routes[0].Fault(nil))

	loaded, _, err := scenario.Parse([]byte(`
version: 1
name: faulted
providers:
  tavily:
    fault:
      attempts:
        - status: 429
`))
	require.NoError(t, err)
	require.NotNil(t, routes[0].Fault(loaded))
}

// TestValidatorRejectsABadProjectionAtStartup is the readiness guarantee: a
// fixture with a bad field must fail at boot, not on the first request.
func TestValidatorRejectsABadProjectionAtStartup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		src      string
		wantCode string
	}{
		{
			name: "an unknown projection key",
			src: `
version: 1
name: bad
providers:
  tavily:
    responseTime: 1.15
`,
			wantCode: CodeProjectionInvalid,
		},
		{
			name: "a wrongly typed projection value",
			src: `
version: 1
name: bad
providers:
  tavily:
    response_time: soon
`,
			wantCode: CodeProjectionInvalid,
		},
		{
			// The result decode path goes through a hand-written mirror of
			// resultProjection, so this is what proves the mirror is still
			// strict and still in step with the type it mirrors.
			name: "an unknown key inside a result",
			src: `
version: 1
name: bad
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily:
    results:
      - source: source-a
        rawContent: not the key
`,
			wantCode: CodeProjectionInvalid,
		},
		{
			name: "a source reference naming no declared source",
			src: `
version: 1
name: bad
providers:
  tavily:
    results:
      - source: source-z
`,
			wantCode: "scenario.source.unknown",
		},
		{
			name: "an images entry with no url",
			src: `
version: 1
name: bad
providers:
  tavily:
    images:
      - description: no url here
`,
			wantCode: "tavily.image.url.missing",
		},
		{
			name: "an extract.failed_results entry with no url",
			src: `
version: 1
name: bad
providers:
  tavily:
    extract:
      failed_results:
        - error: forced failure with nothing to key it on
`,
			wantCode: "tavily.extract.failed_results.url.missing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			loaded, report, err := scenario.Parse([]byte(tc.src))
			require.NoError(t, err)
			require.True(t, report.OK(), "%v", report.Findings)

			findings := provider.ValidateScenario(loaded, map[string]provider.Validator{string(Name): validator{}})
			require.NotEmpty(t, findings)

			codes := make([]string, 0, len(findings))
			for _, f := range findings {
				codes = append(codes, f.Code)
				require.Equal(t, scenario.SeverityError, f.Severity)
			}
			require.Contains(t, codes, tc.wantCode)
		})
	}
}

// TestValidatorAcceptsTheBuiltInProjections proves the shipped fixtures decode,
// which is the case that would otherwise only be exercised by a container that
// failed to become ready.
func TestValidatorAcceptsTheBuiltInProjections(t *testing.T) {
	t.Parallel()

	loaded, report, err := scenario.Parse([]byte(goldenScenario))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)

	findings := provider.ValidateScenario(loaded, map[string]provider.Validator{string(Name): validator{}})
	require.Empty(t, findings)
}

// TestBuiltInScenariosProjectThroughTavily runs every shipped scenario through
// this package's decoder. It is the check that would otherwise only fire as a
// container that never became ready, and it catches the case where a fixture
// and a projection type drift apart between two units.
func TestBuiltInScenariosProjectThroughTavily(t *testing.T) {
	t.Parallel()

	served := 0
	for _, name := range scenarios.Names() {
		loaded, report, err := scenarios.Load(name)
		require.NoError(t, err, name)
		require.True(t, report.OK(), "%s: %v", name, report.Findings)

		if loaded.Provider(string(Name)) == nil {
			continue
		}
		findings := provider.ValidateScenario(loaded, map[string]provider.Validator{string(Name): validator{}})
		for _, f := range findings {
			require.NotEqual(t, scenario.SeverityError, f.Severity, "%s: %+v", name, f)
		}

		rec := post(t, Profile().Handler(provider.Deps{Scenario: loaded}), "/search",
			`{"query":"report a","include_answer":true}`, bearer)
		require.Contains(t, []int{http.StatusOK, http.StatusUnauthorized}, rec.Code, name)
		served++
	}
	require.NotZero(t, served, "no built-in scenario declares a Tavily provider")
}

// --- helpers ----------------------------------------------------------------

// newHandler parses a scenario, proves its Tavily projections validate, and
// returns the handler. Passing a ring wires the journal so a test can assert on
// findings.
func newHandler(t *testing.T, src string, ring *journal.Ring) http.Handler {
	t.Helper()

	loaded, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)

	findings := provider.ValidateScenario(loaded, map[string]provider.Validator{
		string(Name): validator{},
		NameResearch: researchValidator{},
	})
	require.Empty(t, findings, "the fixture must validate before it is served")

	// A job store is wired by default: without one a research create still
	// answers with a request_id and every poll 404s, which is a confusing way for
	// an unrelated test to fail.
	deps := provider.Deps{Scenario: loaded, Jobs: jobs.NewRegistry(jobs.Limits{})}
	if ring != nil {
		deps.Journal = ring
	}
	return Profile().Handler(deps)
}

// post issues a POST /search-shaped request against the handler.
func post(t *testing.T, handler http.Handler, path, body, auth string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// decode reads a response through the exported wire type, which is what a
// consumer does. It is never the only assertion in a test: a permissive decoder
// cannot tell a JSON number from a JSON string, which is the single most likely
// bug on this surface.
func decode(t *testing.T, rec *httptest.ResponseRecorder) searchResponseWire {
	t.Helper()

	var out searchResponseWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

// derefAnswer fails the test rather than panicking on a null answer.
func derefAnswer(t *testing.T, resp searchResponseWire) string {
	t.Helper()

	require.NotNil(t, resp.Answer, "answer was null")
	return *resp.Answer
}

// goldenBytes returns a contract fixture compacted to one line, so a comparison
// against a rendered body is a byte comparison rather than a whitespace
// comparison.
func goldenBytes(t *testing.T, name string) string {
	t.Helper()

	data, err := contracts.Read(contracts.Tavily, name)
	require.NoError(t, err)

	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, data))
	return compact.String()
}
