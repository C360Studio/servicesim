package testkit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubTB captures what an assertion reports instead of failing the real test, so
// this package can assert that an assertion fails when it should. Every helper
// here reports through Errorf, never Fatalf, which is what lets a plain embedded
// testing.TB stand in: the embedded interface is nil, so a method this stub does
// not override would panic rather than silently pass, and that is the intended
// failure mode if an assertion ever starts calling one.
type stubTB struct {
	testing.TB

	mu       sync.Mutex
	messages []string
}

// Helper is a no-op: there is no real test frame to attribute a failure to.
func (s *stubTB) Helper() {}

// Errorf records a failure.
func (s *stubTB) Errorf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, fmt.Sprintf(format, args...))
}

// Logf records an informational message alongside the failures, so a golden
// update's log line is observable.
func (s *stubTB) Logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, fmt.Sprintf(format, args...))
}

// Failed reports whether anything was recorded through Errorf.
func (s *stubTB) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages) > 0
}

// Message returns everything recorded, joined, for a Contains assertion.
func (s *stubTB) Message() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.messages, "\n")
}

// entryFor issues one Exa request against the happy scenario and returns its
// journal entry, which is the input every assertion here takes.
func entryFor(tb testing.TB, sim *testkit.Sim, body string, headers map[string]string) testkit.Entry {
	tb.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		sim.URL(provider.Exa)+"/search", strings.NewReader(body))
	require.NoError(tb, err)
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := sim.Client().Do(req)
	require.NoError(tb, err)
	require.NoError(tb, resp.Body.Close())

	entries := sim.Requests(provider.Exa)
	require.NotEmpty(tb, entries)
	return entries[len(entries)-1]
}

func TestAssertRequestCount(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	entryFor(t, sim, `{"query":"report a"}`, map[string]string{"x-api-key": "test-key"})

	testkit.AssertRequestCount(t, sim, provider.Exa, 1)

	stub := &stubTB{}
	testkit.AssertRequestCount(stub, sim, provider.Exa, 2)
	assert.True(t, stub.Failed())
	assert.Contains(t, stub.Message(), "want 2")
}

func TestCredentialAssertions(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))

	apiKey := entryFor(t, sim, `{"query":"report a"}`, map[string]string{"x-api-key": "test-key"})
	bearer := entryFor(t, sim, `{"query":"report a"}`, map[string]string{"Authorization": "Bearer test-key"})
	other := entryFor(t, sim, `{"query":"report a"}`, map[string]string{"x-api-key": "other-key"})

	testkit.AssertAPIKeyHeader(t, apiKey)
	testkit.AssertBearerAuth(t, bearer)

	// The same secret in two placements is the same credential: the fingerprint
	// is over the value, which is what lets a consumer prove its client is
	// reusing one key without the journal ever holding it.
	testkit.AssertSameCredential(t, apiKey, bearer)
	assert.NotEmpty(t, apiKey.Auth.Fingerprint)

	tests := []struct {
		name    string
		assert  func(testing.TB)
		wantMsg string
	}{
		{"bearer against an api key", func(tb testing.TB) { testkit.AssertBearerAuth(tb, apiKey) }, "authorization"},
		{"api key against a bearer", func(tb testing.TB) { testkit.AssertAPIKeyHeader(tb, bearer) }, "x-api-key"},
		{"different credentials", func(tb testing.TB) { testkit.AssertSameCredential(tb, apiKey, other) }, "different"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubTB{}
			tc.assert(stub)

			assert.True(t, stub.Failed())
			assert.Contains(t, stub.Message(), tc.wantMsg)
		})
	}
}

// TestAssertNoCredentialLeak is the assertion form of the rule that credentials
// never survive a round trip: the literal is sent in the header, in the body and
// in the query string, and must appear in none of them.
func TestAssertNoCredentialLeak(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		sim.URL(provider.Exa)+"/search?api_key=sk-live-leak",
		strings.NewReader(`{"query":"report a","api_key":"sk-live-leak"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-live-leak")

	resp, err := sim.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	testkit.AssertNoCredentialLeak(t, sim, "sk-live-leak")

	// A literal that really is in the journal must be caught, or the assertion
	// above proves nothing.
	stub := &stubTB{}
	testkit.AssertNoCredentialLeak(stub, sim, "report a")
	assert.True(t, stub.Failed())
	assert.Contains(t, stub.Message(), "leaked")
}

// TestAssertNoCredentialLeakCatchesTurnKeyLaneKey pins that outcome.fault_key
// really is in leakFields' scan, using a turn_key extractor whose credential
// name is nested and array-indexed (body_json:api_keys.0) — the exact shape
// provider/lane.go's fingerprinting fix has to cover, since the array index
// "0" is never a credential name by itself. Before that fix this scenario put
// the raw literal into outcome.fault_key; this test would have caught it.
func TestAssertNoCredentialLeakCatchesTurnKeyLaneKey(t *testing.T) {
	t.Parallel()

	const laneKeyYAML = `
version: 1
name: lane-key-credential
providers:
  exa:
    turn_key: ["body_json:api_keys.0"]
    turns:
      - respond:
          results: []
`
	sim := testkit.Start(t, testkit.WithScenarioYAML(laneKeyYAML), testkit.WithProviders(provider.Exa))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		sim.URL(provider.Exa)+"/search", strings.NewReader(`{"api_keys":["sk-live-lane-leak"]}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := sim.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	testkit.AssertNoCredentialLeak(t, sim, "sk-live-lane-leak")
}

func TestAssertJSONBody(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	entry := entryFor(t, sim, `{"numResults":5,"query":"report a"}`,
		map[string]string{"x-api-key": "test-key"})

	// Key order differs from the wire body on purpose: JSON object order is not
	// part of any of these contracts.
	testkit.AssertJSONBody(t, entry, map[string]any{"query": "report a", "numResults": 5})

	stub := &stubTB{}
	testkit.AssertJSONBody(stub, entry, map[string]any{"query": "report b", "numResults": 5})
	assert.True(t, stub.Failed())
	assert.Contains(t, stub.Message(), "body mismatch")
}

func TestFindingAssertions(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))

	clean := entryFor(t, sim, `{"query":"report a"}`, map[string]string{"x-api-key": "test-key"})
	testkit.AssertNoErrors(t, clean)
	testkit.AssertNoFindings(t, clean)
	testkit.AssertFindings(t, clean)

	// An unmodelled field is a warning, never an error: being stricter than the
	// vendor would fail a consumer here and pass in production.
	warned := entryFor(t, sim, `{"query":"report a","futureField":true}`,
		map[string]string{"x-api-key": "test-key"})
	testkit.AssertNoErrors(t, warned)
	testkit.AssertFindings(t, warned, "request.unknown_field")

	strict := &stubTB{}
	testkit.AssertNoFindings(strict, warned)
	assert.True(t, strict.Failed(), "AssertNoFindings is the strict form and must see the warning")

	rejected := entryFor(t, sim, `{}`, map[string]string{"x-api-key": "test-key"})
	failing := &stubTB{}
	testkit.AssertNoErrors(failing, rejected)
	assert.True(t, failing.Failed())
	assert.Contains(t, failing.Message(), "exa.query.missing")

	mismatch := &stubTB{}
	testkit.AssertFindings(mismatch, rejected, "some.other.code")
	assert.True(t, mismatch.Failed())
}

// TestAssertGoldenJSON is deliberately not parallel: one subtest sets the update
// environment variable, and t.Setenv refuses to run under a parallel ancestor.
func TestAssertGoldenJSON(t *testing.T) {
	body := []byte(`{"requestId":"abc","results":[{"title":"Report A"}],"costDollars":{"total":0.005}}`)
	path := filepath.Join(t.TempDir(), "nested", "search.json")

	t.Run("missing golden names the update variable", func(t *testing.T) {
		stub := &stubTB{}
		testkit.AssertGoldenJSON(stub, path, body)

		assert.True(t, stub.Failed())
		assert.Contains(t, stub.Message(), testkit.UpdateGoldenEnv+"=1")
	})

	t.Run("the environment variable writes the file", func(t *testing.T) {
		t.Setenv(testkit.UpdateGoldenEnv, "1")
		testkit.AssertGoldenJSON(t, path, body)

		written, err := os.ReadFile(path) //nolint:gosec // the path is this test's own temp dir
		require.NoError(t, err)
		assert.True(t, json.Valid(written))
	})

	t.Run("a written golden then compares clean", func(t *testing.T) {
		testkit.AssertGoldenJSON(t, path, body)
	})

	t.Run("derived identifiers are ignored by default", func(t *testing.T) {
		// A route with a fault plan varies requestId per attempt by design, so the
		// default must not compare it.
		varied := []byte(`{"requestId":"zzz","results":[{"title":"Report A"}],"costDollars":{"total":0.005}}`)
		testkit.AssertGoldenJSON(t, path, varied)

		stub := &stubTB{}
		testkit.AssertGoldenJSON(stub, path, varied, testkit.GoldenExactIDs())
		assert.True(t, stub.Failed(), "GoldenExactIDs opts back into comparing requestId")
	})

	t.Run("GoldenIgnore excludes a dotted path", func(t *testing.T) {
		changed := []byte(`{"requestId":"abc","results":[{"title":"Report A"}],"costDollars":{"total":9.99}}`)

		stub := &stubTB{}
		testkit.AssertGoldenJSON(stub, path, changed)
		assert.True(t, stub.Failed())

		testkit.AssertGoldenJSON(t, path, changed, testkit.GoldenIgnore("costDollars.total"))
	})

	t.Run("a response that is not JSON fails", func(t *testing.T) {
		stub := &stubTB{}
		testkit.AssertGoldenJSON(stub, path, []byte(`{"unterminated"`))

		assert.True(t, stub.Failed())
		assert.Contains(t, stub.Message(), "not JSON")
	})
}

// TestAssertOverlappedRejectsAdjacentRequests pins the strictness of the
// comparison: a request that completed exactly when the next one arrived was not
// in flight at the same time.
func TestAssertOverlappedRejectsAdjacentRequests(t *testing.T) {
	t.Parallel()

	instant := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	first := testkit.Entry{Seq: 1, ArrivedAt: instant, CompletedAt: instant.Add(time.Second)}
	second := testkit.Entry{Seq: 2, ArrivedAt: instant.Add(time.Second), CompletedAt: instant.Add(2 * time.Second)}

	stub := &stubTB{}
	testkit.AssertOverlapped(stub, first, second)
	assert.True(t, stub.Failed())

	overlapping := testkit.Entry{Seq: 3, ArrivedAt: instant.Add(500 * time.Millisecond), CompletedAt: instant.Add(2 * time.Second)}
	testkit.AssertOverlapped(t, first, overlapping)
}

// TestAssertNamespacesIsolated is the property the namespace feature exists to
// provide, asserted directly: two lanes of one Sim, each seeing the scenario from
// its own first attempt.
func TestAssertNamespacesIsolated(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(retryScenario), testkit.WithProviders(provider.Exa))
	alpha := sim.Namespace(t, "alpha")
	beta := sim.Namespace(t, "beta")

	for range 2 {
		searchIn(t, sim, alpha.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
		searchIn(t, sim, beta.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
	}

	stub := &stubTB{}
	testkit.AssertNamespacesIsolated(stub, alpha, beta)
	assert.False(t, stub.Failed(), "two independent lanes are isolated: %s", stub.Message())
}

// TestAssertNamespacesIsolatedRefusesAVacuousComparison covers the ways the
// assertion can be asked a question it cannot answer. Each one passes silently if
// it is not checked, which is worse than failing: a consumer would read a green
// test as evidence of isolation it never established.
func TestAssertNamespacesIsolatedRefusesAVacuousComparison(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	used := sim.Namespace(t, "used")
	searchIn(t, sim, used.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)

	otherSim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	elsewhere := otherSim.Namespace(t, "elsewhere")
	searchIn(t, otherSim, elsewhere.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)

	tests := []struct {
		name    string
		a, b    *testkit.Namespace
		wantMsg string
	}{
		{name: "a nil handle", a: used, b: nil, wantMsg: "nil"},
		{name: "two Sims", a: used, b: elsewhere, wantMsg: "different Sims"},
		{name: "one namespace with itself", a: used, b: sim.Namespace(t, "used"), wantMsg: "with itself"},
		{name: "a namespace nothing was sent to", a: used, b: sim.Namespace(t, "unused"), wantMsg: "vacuously"},
		{name: "the unused namespace given first", a: sim.Namespace(t, "unused-first"), b: used, wantMsg: "vacuously"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubTB{}
			testkit.AssertNamespacesIsolated(stub, tc.a, tc.b)
			assert.True(t, stub.Failed(), "the comparison proves nothing and must say so")
			assert.Contains(t, stub.Message(), tc.wantMsg)
		})
	}
}

// exaPollScenario is the shape provider/exa/agentrun_test.go's asyncScenario
// uses: two pending polls, then a terminal snapshot that also answers every
// poll after it. Exa's poll HTTP status is 200 throughout — only the attempt
// index moves — which is why the Exa case is where AssertPollSequence's
// attempt-index half carries the assertion.
const exaPollScenario = `
version: 1
name: testkit-exa-poll
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - when: {call_index: 1}
        respond: {status: running}
      - respond:
          status: completed
          output:
            text: done
`

// tavilyResearchPollScenario walks pending -> in_progress -> completed, verified
// against provider/tavily/research.go: the poll's HTTP status VARIES with task
// state (202 while pending or in progress, 200 once terminal).
const tavilyResearchPollScenario = `
version: 1
name: testkit-tavily-research-poll
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  tavily_research:
    turns:
      - when: {call_index: 0}
        respond: {status: pending}
      - when: {call_index: 1}
        respond: {status: in_progress}
      - respond:
          status: completed
          content: done
          sources: [source-a]
`

// createResearch issues the create call an adapter would issue against
// Tavily's async research surface and returns the identifier it minted.
func createResearch(tb testing.TB, sim *testkit.Sim, base string) string {
	tb.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		base+"/research", strings.NewReader(`{"input":"research this"}`))
	require.NoError(tb, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")

	resp, err := sim.Client().Do(req)
	require.NoError(tb, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(tb, http.StatusCreated, resp.StatusCode, "create failed")

	var out struct {
		RequestID string `json:"request_id"`
	}
	require.NoError(tb, json.NewDecoder(resp.Body).Decode(&out))
	require.NotEmpty(tb, out.RequestID)
	return out.RequestID
}

// requestJob issues a bare request of the given method against a job's poll
// path, with the credential its provider documents. The caller reads nothing
// off the response but its status code arriving without error;
// [testkit.AssertPollSequence] reads the journal for everything else.
func requestJob(tb testing.TB, sim *testkit.Sim, p provider.Name, method, path string) *http.Response {
	tb.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, path, nil)
	require.NoError(tb, err)
	if p == provider.Exa {
		req.Header.Set("x-api-key", "test-key")
	} else {
		req.Header.Set("Authorization", "Bearer test-key")
	}

	resp, err := sim.Client().Do(req)
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// pollJob issues a bare GET against a job's poll path.
func pollJob(tb testing.TB, sim *testkit.Sim, p provider.Name, path string) *http.Response {
	tb.Helper()
	return requestJob(tb, sim, p, http.MethodGet, path)
}

// headJob issues a bare HEAD against a job's poll path, to prove it draws on
// no poll budget.
func headJob(tb testing.TB, sim *testkit.Sim, p provider.Name, path string) *http.Response {
	tb.Helper()
	return requestJob(tb, sim, p, http.MethodHead, path)
}

// TestAssertPollSequence covers both providers acceptance criterion 58 names:
// Tavily, where the HTTP status is the signal (202, 202, 200), and Exa, where
// every poll answers 200 and only the attempt index moves — the case where
// AssertPollSequence's attempt-index half is doing the work.
func TestAssertPollSequence(t *testing.T) {
	t.Parallel()

	t.Run("tavily research status tracks task state", func(t *testing.T) {
		t.Parallel()

		sim := testkit.Start(t, testkit.WithScenarioYAML(tavilyResearchPollScenario), testkit.WithProviders(provider.Tavily))
		id := createResearch(t, sim, sim.URL(provider.Tavily))

		for range 3 {
			pollJob(t, sim, provider.Tavily, sim.URL(provider.Tavily)+"/research/"+id)
		}

		testkit.AssertPollSequence(t, sim.Requests(provider.Tavily), id,
			http.StatusAccepted, http.StatusAccepted, http.StatusOK)
	})

	t.Run("exa agent run attempt indices carry the assertion", func(t *testing.T) {
		t.Parallel()

		sim := testkit.Start(t, testkit.WithScenarioYAML(exaPollScenario), testkit.WithProviders(provider.Exa))
		id := createAgentRun(t, sim, sim.URL(provider.Exa))

		for range 3 {
			pollJob(t, sim, provider.Exa, sim.URL(provider.Exa)+"/agent/runs/"+id)
		}

		testkit.AssertPollSequence(t, sim.Requests(provider.Exa), id,
			http.StatusOK, http.StatusOK, http.StatusOK)
	})
}

// TestAssertPollSequenceIgnoresHeadAndOtherJobs is the negative half: a HEAD
// between polls and a poll of a different id in the same namespace must not
// shift this job's sequence, because each draws on its own per-job lane.
func TestAssertPollSequenceIgnoresHeadAndOtherJobs(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(exaPollScenario), testkit.WithProviders(provider.Exa))
	id := createAgentRun(t, sim, sim.URL(provider.Exa))
	// other lands at call_index 1 of the SAME lane as id (call_index 0), which is
	// why this NotEqual holds. It is not general: two creates in DIFFERENT
	// namespaces at the same call index mint the identical id, because job
	// identifiers carry no namespace component (see
	// TestSimJobsAndNamespaceJobs). Moving this second create into another
	// namespace to "strengthen" the test would make it collide with id instead.
	other := createAgentRun(t, sim, sim.URL(provider.Exa))
	require.NotEqual(t, id, other, "two creates must mint different identifiers")

	pollJob(t, sim, provider.Exa, sim.URL(provider.Exa)+"/agent/runs/"+id)    // id's attempt 0
	headJob(t, sim, provider.Exa, sim.URL(provider.Exa)+"/agent/runs/"+id)    // must not consume a poll
	pollJob(t, sim, provider.Exa, sim.URL(provider.Exa)+"/agent/runs/"+other) // other's poll, not id's
	pollJob(t, sim, provider.Exa, sim.URL(provider.Exa)+"/agent/runs/"+id)    // id's attempt 1
	pollJob(t, sim, provider.Exa, sim.URL(provider.Exa)+"/agent/runs/"+id)    // id's attempt 2

	testkit.AssertPollSequence(t, sim.Requests(provider.Exa), id, http.StatusOK, http.StatusOK, http.StatusOK)
}

// TestAssertPollSequenceFailsOnWrongStatus proves the assertion actually
// checks what it claims to, using the existing failing-TB pattern, and that
// its failure message carries the evidence table a developer needs to debug
// it: the observed (method, path, namespace, status, attempt_index) rows.
func TestAssertPollSequenceFailsOnWrongStatus(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(exaPollScenario), testkit.WithProviders(provider.Exa))
	id := createAgentRun(t, sim, sim.URL(provider.Exa))
	pollJob(t, sim, provider.Exa, sim.URL(provider.Exa)+"/agent/runs/"+id)

	stub := &stubTB{}
	testkit.AssertPollSequence(stub, sim.Requests(provider.Exa), id, http.StatusTooManyRequests)
	assert.True(t, stub.Failed())
	assert.Contains(t, stub.Message(), "want status 429")
	assert.Contains(t, stub.Message(), "observed polls:")
	assert.Contains(t, stub.Message(), "attempt_index=0")
	assert.Contains(t, stub.Message(), `ns="default"`)
}

// TestAssertPollSequenceFailsOnWrongCount covers the branch
// TestAssertPollSequenceFailsOnWrongStatus does not reach: a count mismatch,
// which must fail before any per-poll status or attempt-index comparison
// runs, and must carry the same evidence table.
func TestAssertPollSequenceFailsOnWrongCount(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(exaPollScenario), testkit.WithProviders(provider.Exa))
	id := createAgentRun(t, sim, sim.URL(provider.Exa))
	pollJob(t, sim, provider.Exa, sim.URL(provider.Exa)+"/agent/runs/"+id)

	stub := &stubTB{}
	testkit.AssertPollSequence(stub, sim.Requests(provider.Exa), id, http.StatusOK, http.StatusOK)
	assert.True(t, stub.Failed())
	assert.Contains(t, stub.Message(), "recorded 1 polls, want 2")
	assert.Contains(t, stub.Message(), "observed polls:")
}

// TestAssertPollSequenceRejectsCrossNamespaceCollision pins the fix for the
// namespace-conflation bug: job identifiers carry no namespace component, so
// two namespaces that each create at the same call index mint the identical
// id — exactly the per-test-namespace idiom [testkit.Sim.NamespaceFor]
// exists to support. Reading such an id across the whole Sim must report the
// ambiguity rather than silently merge the two jobs' polls into one
// sequence; reading it through either single [*testkit.Namespace] view must
// work normally.
func TestAssertPollSequenceRejectsCrossNamespaceCollision(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(exaPollScenario), testkit.WithProviders(provider.Exa))
	alpha := sim.Namespace(t, "alpha")
	beta := sim.Namespace(t, "beta")

	alphaID := createAgentRun(t, sim, alpha.URL(provider.Exa))
	betaID := createAgentRun(t, sim, beta.URL(provider.Exa))
	require.Equal(t, alphaID, betaID, "same call index, no namespace component: ids collide by design")
	id := alphaID

	pollJob(t, sim, provider.Exa, alpha.URL(provider.Exa)+"/agent/runs/"+id)
	pollJob(t, sim, provider.Exa, beta.URL(provider.Exa)+"/agent/runs/"+id)
	pollJob(t, sim, provider.Exa, alpha.URL(provider.Exa)+"/agent/runs/"+id)

	// Scoped to one namespace, the assertion sees only that lane's polls and
	// passes: alpha claimed attempts 0 and 1, in order.
	testkit.AssertPollSequence(t, alpha.Requests(provider.Exa), id, http.StatusOK, http.StatusOK)
	testkit.AssertPollSequence(t, beta.Requests(provider.Exa), id, http.StatusOK)

	// Read across the whole Sim, the same id's polls span two namespaces.
	// That must fail loudly, not merge into a plausible-looking sequence of
	// three.
	stub := &stubTB{}
	testkit.AssertPollSequence(stub, sim.Requests(provider.Exa), id, http.StatusOK, http.StatusOK, http.StatusOK)
	require.True(t, stub.Failed())
	assert.Contains(t, stub.Message(), "different namespaces")
	assert.Contains(t, stub.Message(), "alpha")
	assert.Contains(t, stub.Message(), "beta")
}

// fakePollEntry hand-seeds one journal entry for AssertPollSequence to read,
// standing in for a live Sim when the shape under test — an attempt-index gap
// with everything else about the entries correct — cannot be produced by
// driving one: every route this repository serves hands out consecutive
// attempt indices to a job's own polls, so the only way to exercise the
// AttemptIndex half of the check in isolation from the count and status
// checks is to construct entries directly. That the assertion takes entries
// rather than a Sim is what makes this possible from outside the module.
func fakePollEntry(path string, status, attemptIndex int) testkit.Entry {
	return testkit.Entry{
		Provider: string(provider.Exa),
		Method:   http.MethodGet,
		Path:     path,
		Outcome:  testkit.Outcome{Status: status, AttemptIndex: attemptIndex},
	}
}

// TestAssertPollSequencePinsAttemptIndex is the mutation this assertion's
// suite was missing: every test reachable by driving a real Sim happens to
// have attempt indices 0..n-1 whenever the count and statuses are right, so
// deleting the "|| e.Outcome.AttemptIndex != i" half of the check left every
// existing test green. Three hand-seeded entries with correct statuses but
// attempt indices 0, 2, 1 (a duplicate and a gap, never produced by a real
// per-job lane) close that gap: the count matches, every status matches, and
// only the attempt-index leg can fail it.
func TestAssertPollSequencePinsAttemptIndex(t *testing.T) {
	t.Parallel()

	const path = "/n/default/agent/runs/run_fake"
	entries := []testkit.Entry{
		fakePollEntry(path, http.StatusOK, 0),
		fakePollEntry(path, http.StatusOK, 2),
		fakePollEntry(path, http.StatusOK, 1),
	}

	stub := &stubTB{}
	testkit.AssertPollSequence(stub, entries, "run_fake", http.StatusOK, http.StatusOK, http.StatusOK)
	require.True(t, stub.Failed(), "count and every status matched; only attempt_index could have failed this")
	assert.Contains(t, stub.Message(), "attempt_index")
}

// TestAssertNamespacesIsolatedReportsABrokenCursor drives the detector that
// matters: a namespace whose claimed attempt indices are not 0, 1, 2 … has had a
// call served somewhere else.
//
// Eviction is used to produce the gap, because a lane that skipped an index
// without one would be the bug itself. Both facts are reported: the eviction,
// which explains why the journal can no longer answer the question, and the gap
// it left.
func TestAssertNamespacesIsolatedReportsABrokenCursor(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithScenarioYAML(retryScenario),
		testkit.WithProviders(provider.Exa),
		testkit.WithJournalCapacity(1))
	alpha := sim.Namespace(t, "alpha")
	beta := sim.Namespace(t, "beta")

	for range 2 {
		searchIn(t, sim, alpha.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
	}
	searchIn(t, sim, beta.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)

	require.Len(t, alpha.Journal(), 1, "capacity 1 retains only the second call")

	stub := &stubTB{}
	testkit.AssertNamespacesIsolated(stub, alpha, beta)
	require.True(t, stub.Failed())
	assert.Contains(t, stub.Message(), "evicted 1 entries")
	assert.Contains(t, stub.Message(), "consecutive from 0")
}
