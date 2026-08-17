package testkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/provider/exa"
	"github.com/c360studio/servicesim/provider/perplexity"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// delayScenario delays the first attempt against Exa and Tavily and then serves
// the ordinary response. Both surfaces are needed because the overlap assertion
// compares two providers' entries.
const delayScenario = `
version: 1
name: testkit-delay
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    fault:
      attempts:
        - delay: 150ms
    results:
      - source: source-a
  tavily:
    fault:
      attempts:
        - delay: 150ms
    results:
      - source: source-a
        score: 0.9
`

// abortScenario closes the connection before any header reaches the client, so
// the client observes a transport error and the entry is completed by a server
// goroutine the client is no longer waiting on.
const abortScenario = `
version: 1
name: testkit-abort
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    fault:
      attempts:
        - kind: close_before_headers
    results:
      - source: source-a
`

// retryScenario answers 429 once and then serves the scenario response.
const retryScenario = `
version: 1
name: testkit-retry
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    fault:
      attempts:
        - status: 429
          error: Rate limit exceeded.
        - status: 200
    results:
      - source: source-a
`

// jobsScenario is Exa's async agent-run surface with no pending turn: the
// zero-poll degenerate case (design §2.4), which is enough to mint a job
// record without needing a poll loop to exercise Sim.Jobs and Namespace.Jobs.
const jobsScenario = `
version: 1
name: testkit-jobs
providers:
  exa_agent_runs:
    status: completed
    output:
      text: done
`

// createAgentRun issues the create call an adapter would issue against Exa's
// async surface and returns the identifier it minted.
func createAgentRun(tb testing.TB, sim *testkit.Sim, base string) string {
	tb.Helper()

	req := newRequest(context.Background(), tb, base+"/agent/runs", provider.Exa, `{"query":"report a"}`)
	resp, err := sim.Client().Do(req)
	require.NoError(tb, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(tb, http.StatusCreated, resp.StatusCode, "create failed")

	var out struct {
		ID string `json:"id"`
	}
	require.NoError(tb, json.NewDecoder(resp.Body).Decode(&out))
	require.NotEmpty(tb, out.ID)
	return out.ID
}

// search issues the vendor request an adapter would issue, with a credential in
// the placement each provider documents.
func search(tb testing.TB, sim *testkit.Sim, p provider.Name, path, body string) *http.Response {
	tb.Helper()

	resp, err := sim.Client().Do(newRequest(context.Background(), tb, sim.URL(p)+path, p, body))
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// newRequest builds a vendor-shaped request. Exa documents x-api-key, Tavily and
// Perplexity document a Bearer token.
func newRequest(ctx context.Context, tb testing.TB, url string, p provider.Name, body string) *http.Request {
	tb.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	require.NoError(tb, err)
	req.Header.Set("Content-Type", "application/json")
	if p == provider.Exa {
		req.Header.Set("x-api-key", "test-key")
	} else {
		req.Header.Set("Authorization", "Bearer test-key")
	}
	return req
}

// mcpDiscoverBody is a well-formed server/discover request body, carrying
// the two _meta fields the profile requires on every request (decision
// 13, contracts/mcp/README.md).
const mcpDiscoverBody = `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":` +
	`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`

// newMCPDiscoverRequest builds a server/discover request against url,
// carrying the standard request-metadata headers the profile validates
// (MCP-Protocol-Version, Mcp-Method) and the Accept pair the transport
// requires — a shape unrelated to the three research vendors' simple
// path-plus-auth-header requests, so it does not reuse newRequest.
func newMCPDiscoverRequest(ctx context.Context, tb testing.TB, url string) *http.Request {
	tb.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(mcpDiscoverBody))
	require.NoError(tb, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "server/discover")
	return req
}

// TestStartIsThreeLines is the consumer's test, written the way a consumer would
// write it: start, call, assert the vendor request was correct.
func TestStartIsThreeLines(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"))

	resp := search(t, sim, provider.Exa, "/search", `{"query":"report a"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	entries := sim.Requests(provider.Exa)
	require.Len(t, entries, 1)
	testkit.AssertNoErrors(t, entries[0])
	testkit.AssertAPIKeyHeader(t, entries[0])
}

func TestStartServesEveryProvider(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"))

	tests := []struct {
		name     string
		provider provider.Name
		path     string
		body     string
	}{
		{"exa search", provider.Exa, "/search", `{"query":"report a"}`},
		{"exa answer", provider.Exa, "/answer", `{"query":"report a"}`},
		{"tavily search", provider.Tavily, "/search", `{"query":"report a"}`},
		{"perplexity sonar", provider.Perplexity, "/chat/completions",
			`{"model":"sonar","messages":[{"role":"user","content":"report a"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := search(t, sim, tc.provider, tc.path, tc.body)

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
		})
	}

	t.Run("mcp server/discover", func(t *testing.T) {
		resp, err := sim.Client().Do(newMCPDiscoverRequest(context.Background(), t, sim.URL(provider.MCP)+"/mcp"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), `"supportedVersions"`)
	})
}

func TestBaseURLsAreEnvironmentShaped(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa, provider.Tavily))

	urls := sim.BaseURLs()

	assert.Equal(t, sim.URL(provider.Exa), urls["EXA_BASE_URL"])
	assert.Equal(t, sim.URL(provider.Tavily), urls["TAVILY_BASE_URL"])
	assert.NotContains(t, urls, "PERPLEXITY_BASE_URL", "WithProviders excluded Perplexity")
	assert.NotContains(t, urls, "MCP_BASE_URL", "WithProviders excluded MCP")
	assert.Empty(t, sim.URL(provider.Perplexity))
	assert.Empty(t, sim.URL(provider.MCP))
}

// TestBaseURLsIncludesMCP proves MCP_BASE_URL appears once the MCP
// listener is actually started — the fourth provider testkit.Start now
// wires up alongside Exa, Tavily and Perplexity.
func TestBaseURLsIncludesMCP(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"))

	urls := sim.BaseURLs()
	assert.Equal(t, sim.URL(provider.MCP), urls["MCP_BASE_URL"])
	assert.NotEmpty(t, urls["MCP_BASE_URL"])
}

// TestAssertOverlapped is plan acceptance criterion 9's mechanism: it must pass
// for concurrent calls and fail for serial ones, under the default configuration
// with a real clock and real delays.
func TestAssertOverlapped(t *testing.T) {
	t.Parallel()

	t.Run("concurrent requests overlap", func(t *testing.T) {
		t.Parallel()

		sim := testkit.Start(t, testkit.WithScenarioYAML(delayScenario))

		var wg sync.WaitGroup
		wg.Add(2)
		for _, p := range []provider.Name{provider.Exa, provider.Tavily} {
			go func() {
				defer wg.Done()
				search(t, sim, p, "/search", `{"query":"report a"}`)
			}()
		}
		wg.Wait()

		testkit.AssertOverlapped(t, sim.Requests(provider.Exa)[0], sim.Requests(provider.Tavily)[0])
	})

	t.Run("serial requests do not overlap", func(t *testing.T) {
		t.Parallel()

		sim := testkit.Start(t, testkit.WithScenarioYAML(delayScenario))
		search(t, sim, provider.Exa, "/search", `{"query":"report a"}`)
		search(t, sim, provider.Tavily, "/search", `{"query":"report a"}`)

		stub := &stubTB{}
		testkit.AssertOverlapped(stub, sim.Requests(provider.Exa)[0], sim.Requests(provider.Tavily)[0])

		assert.True(t, stub.Failed(), "serial requests must not be reported as overlapping")
		assert.Contains(t, stub.Message(), "did not overlap")
	})
}

// TestClientDeadlineObservesRealDelay is fusion invariant 9's mechanism. A
// deadline is observed by bytes not arriving, so it needs a real delay: this is
// the test a fake clock would silently make untestable.
func TestClientDeadlineObservesRealDelay(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(delayScenario))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := newRequest(ctx, t, sim.URL(provider.Exa)+"/search", provider.Exa, `{"query":"report a"}`)
	resp, err := sim.Client().Do(req) //nolint:bodyclose // err is non-nil, so there is no body
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("the request returned %d, want a deadline error", resp.StatusCode)
	}

	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestSkippedDelaysStillRecordTheRequest proves WithSkippedDelays pays no wall
// clock while still reporting what the scenario asked for.
func TestSkippedDelaysStillRecordTheRequest(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(delayScenario), testkit.WithSkippedDelays())

	started := time.Now()
	resp := search(t, sim, provider.Exa, "/search", `{"query":"report a"}`)
	elapsed := time.Since(started)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Less(t, elapsed, 150*time.Millisecond, "the delay must not have been paid")
	assert.Equal(t, int64(150), sim.Requests(provider.Exa)[0].Outcome.DelayMS)
}

// TestAwaitRequestsAfterAbortingFault is the third mandatory test: an entry
// completed by a goroutine the client is no longer waiting on is only reliably
// observable after AwaitRequests.
func TestAwaitRequestsAfterAbortingFault(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(abortScenario), testkit.WithProviders(provider.Exa))

	req := newRequest(context.Background(), t, sim.URL(provider.Exa)+"/search", provider.Exa, `{"query":"report a"}`)
	resp, err := sim.Client().Do(req) //nolint:bodyclose // the connection was closed before any header
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("the request returned %d, want a transport error", resp.StatusCode)
	}

	entries := sim.AwaitRequests(t, provider.Exa, 1)
	require.Len(t, entries, 1)
	assert.True(t, entries[0].Outcome.Aborted, "the aborting fault must be journaled as aborted")
	assert.Equal(t, testkit.OutcomeFault, entries[0].Outcome.Kind)
	assert.Equal(t, string(scenario.FaultCloseBeforeHeaders), entries[0].Outcome.FaultKind)
}

func TestResetClearsTheJournalAndTheFaultCounters(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(retryScenario), testkit.WithProviders(provider.Exa))

	first := search(t, sim, provider.Exa, "/search", `{"query":"report a"}`)
	require.Equal(t, http.StatusTooManyRequests, first.StatusCode)

	sim.Reset()
	assert.Empty(t, sim.Journal(), "Reset clears the journal")

	// The counter is the turn cursor too, so a reset replays the plan from its
	// first attempt rather than continuing into the success.
	again := search(t, sim, provider.Exa, "/search", `{"query":"report a"}`)
	assert.Equal(t, http.StatusTooManyRequests, again.StatusCode)
}

// TestSimJobsAndNamespaceJobs covers the consumer-facing half of the async job
// surface: a create's record is visible through Sim.Jobs in the declared
// (namespace, entry, create_index, id) order, Namespace.Jobs is the same view
// filtered to one lane, and Reset drops jobs and cursors together — §7.3's
// invariant checked from the consumer's side: a create straight after Reset
// must re-mint the same identifier rather than fail with job.id_collision.
func TestSimJobsAndNamespaceJobs(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(jobsScenario), testkit.WithProviders(provider.Exa))
	alpha := sim.Namespace(t, "alpha")
	beta := sim.Namespace(t, "beta")

	// alphaID and betaID are, in fact, THE SAME STRING: both creates land at
	// call_index 0 of their namespace's own lane, and a job id derives from
	// (seed key, entry, lane key, call index) with no namespace component —
	// ids are unique within a namespace and deliberately not across them. The
	// assertions below never compare alphaID with betaID for that reason; do
	// not add a require.NotEqual here, it would fail for a reason unrelated
	// to whatever this test is meant to pin.
	alphaID := createAgentRun(t, sim, alpha.URL(provider.Exa))
	betaID := createAgentRun(t, sim, beta.URL(provider.Exa))
	require.Equal(t, alphaID, betaID, "same call index, same lane key, no namespace component: ids collide by design")

	all := sim.Jobs()
	require.Len(t, all, 2, "one live job per namespace")
	assert.Equal(t, "alpha", all[0].Namespace, "namespace ascending is the declared order")
	assert.Equal(t, alphaID, all[0].ID)
	assert.Equal(t, "beta", all[1].Namespace)
	assert.Equal(t, betaID, all[1].ID)

	alphaJobs := alpha.Jobs()
	require.Len(t, alphaJobs, 1)
	assert.Equal(t, alphaID, alphaJobs[0].ID)

	betaJobs := beta.Jobs()
	require.Len(t, betaJobs, 1)
	assert.Equal(t, betaID, betaJobs[0].ID)

	// A second create in an otherwise-untouched namespace pins the third
	// ordering key List() promises: create_index. alpha and beta already
	// prove the namespace key sorts ascending; gamma proves that within one
	// namespace, declared order follows creation order too.
	gamma := sim.Namespace(t, "gamma")
	gammaFirst := createAgentRun(t, sim, gamma.URL(provider.Exa))
	gammaSecond := createAgentRun(t, sim, gamma.URL(provider.Exa))
	require.NotEqual(t, gammaFirst, gammaSecond, "distinct call indices in one lane mint distinct ids")

	gammaJobs := gamma.Jobs()
	require.Len(t, gammaJobs, 2)
	assert.Equal(t, 0, gammaJobs[0].CreateIndex)
	assert.Equal(t, gammaFirst, gammaJobs[0].ID)
	assert.Equal(t, 1, gammaJobs[1].CreateIndex)
	assert.Equal(t, gammaSecond, gammaJobs[1].ID)

	sim.Reset()
	assert.Empty(t, sim.Jobs(), "Reset drops every namespace's job records")

	// The cursor Reset rewound and the record it dropped move together, so the
	// next create in the same namespace re-mints the SAME identifier rather
	// than colliding with a record that should no longer exist.
	again := createAgentRun(t, sim, alpha.URL(provider.Exa))
	assert.Equal(t, alphaID, again, "identifiers derive from the call index, which Reset also rewound")
	require.Len(t, sim.Jobs(), 1)
}

// TestNewFaultsWiresAConsumerBuiltDeps is why NewFaults exists: without it the
// scenario's declared 429 never fires and the consumer sees a silent 200.
func TestNewFaultsWiresAConsumerBuiltDeps(t *testing.T) {
	t.Parallel()

	s, _, err := scenario.Parse([]byte(retryScenario))
	require.NoError(t, err)

	srv := httptest.NewServer(exa.New(provider.Deps{Scenario: s, Faults: testkit.NewFaults(s)}))
	t.Cleanup(srv.Close)

	client := srv.Client()
	statuses := []int{}
	for range 2 {
		req := newRequest(context.Background(), t, srv.URL+"/search", provider.Exa, `{"query":"report a"}`)
		resp, err := client.Do(req)
		require.NoError(t, err)
		statuses = append(statuses, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}

	assert.Equal(t, []int{http.StatusTooManyRequests, http.StatusOK}, statuses)
}

// TestExaHandlerReturnsItsSim covers the handler constructors: the Sim is
// returned because the journal is the only source of an Entry, and a bare
// handler could only prove that a response arrived.
func TestExaHandlerReturnsItsSim(t *testing.T) {
	t.Parallel()

	handler, sim := testkit.ExaHandler(t, testkit.WithBuiltin("happy"))
	require.NotNil(t, handler)
	assert.Empty(t, sim.URL(provider.Exa), "the handler constructors start no servers")

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	req := newRequest(context.Background(), t, srv.URL+"/search", provider.Exa, `{"query":"report a"}`)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Equal(t, http.StatusOK, resp.StatusCode)
	entries := sim.Requests(provider.Exa)
	require.Len(t, entries, 1)
	testkit.AssertNoErrors(t, entries[0])
}

func TestHandlerConstructorsCoverEveryProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		build   func(testing.TB, ...testkit.Option) (http.Handler, *testkit.Sim)
		wanted  provider.Name
		path    string
		payload string
	}{
		{"exa", testkit.ExaHandler, provider.Exa, "/search", `{"query":"report a"}`},
		{"tavily", testkit.TavilyHandler, provider.Tavily, "/search", `{"query":"report a"}`},
		{"perplexity", testkit.PerplexityHandler, provider.Perplexity, "/v1/sonar",
			`{"model":"sonar","messages":[{"role":"user","content":"report a"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, sim := tc.build(t, testkit.WithBuiltin("happy"))
			require.NotNil(t, handler)

			req := newRequest(context.Background(), t, "http://sim.test"+tc.path, tc.wanted, tc.payload)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, handler, sim.Handler(tc.wanted))
			testkit.AssertRequestCount(t, sim, tc.wanted, 1)
		})
	}

	t.Run("mcp", func(t *testing.T) {
		handler, sim := testkit.MCPHandler(t, testkit.WithBuiltin("happy"))
		require.NotNil(t, handler)

		req := newMCPDiscoverRequest(context.Background(), t, "http://sim.test/mcp")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, handler, sim.Handler(provider.MCP))
		testkit.AssertRequestCount(t, sim, provider.MCP, 1)
	})
}

func TestScenarioSelectionOptions(t *testing.T) {
	t.Parallel()

	parsed, _, err := scenario.Parse([]byte(retryScenario))
	require.NoError(t, err)

	tests := []struct {
		name string
		opt  testkit.Option
		want string
	}{
		{"builtin", testkit.WithBuiltin("rate-limited"), "rate-limited"},
		{"yaml", testkit.WithScenarioYAML(retryScenario), "testkit-retry"},
		{"scenario", testkit.WithScenario(parsed), "testkit-retry"},
		{"file", testkit.WithScenarioFile("../scenarios/protocol/happy.yaml"), "happy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sim := testkit.Start(t, tc.opt, testkit.WithProviders(provider.Exa))
			assert.Equal(t, tc.want, sim.Scenario().Name)
		})
	}
}

// TestJournalCapacityBoundsRetention proves the option reaches the journal, and
// that sequence numbers keep being allocated with retention switched off.
func TestJournalCapacityBoundsRetention(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"),
		testkit.WithProviders(provider.Exa), testkit.WithJournalCapacity(1))

	for range 3 {
		resp := search(t, sim, provider.Exa, "/search", `{"query":"report a"}`)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	entries := sim.Journal()
	require.Len(t, entries, 1)
	assert.Equal(t, uint64(3), entries[0].Seq, "the retained entry is the newest")
}

// TestClientDisablesKeepAlives is what makes a connection-abort fault observable
// rather than absorbed by a pooled connection.
func TestClientDisablesKeepAlives(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	transport, ok := sim.Client().Transport.(*http.Transport)

	require.True(t, ok, "the client's transport is an *http.Transport")
	assert.True(t, transport.DisableKeepAlives)
	assert.Nil(t, transport.Proxy, "the simulator's own client must never consult a proxy variable")
}

// TestCloseIsIdempotent guards the cleanup Start registers: a test that closes
// early must not panic when tb.Cleanup closes again.
func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	sim.Close()
	sim.Close()
}

// TestUnmatchedRouteFailsClosed proves the Sim inherits the fail-closed routing
// rather than falling through to a bare 404 with no journal entry.
func TestUnmatchedRouteFailsClosed(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))

	resp := search(t, sim, provider.Exa, "/nope", `{"query":"report a"}`)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Contains(t, string(body), "{", "the 404 is provider-shaped JSON, not text/plain")

	entries := sim.Requests(provider.Exa)
	require.Len(t, entries, 1)
	assert.Equal(t, testkit.OutcomeUnmatched, entries[0].Outcome.Kind)
}

// TestErrorsSurviveAsAliases is the compile-time half of the alias set: a
// consumer must be able to name every type reachable from an Entry. The runtime
// half is that these values come out of a real request.
func TestErrorsSurviveAsAliases(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	search(t, sim, provider.Exa, "/search", `{}`)

	var (
		entry    testkit.Entry
		outcome  testkit.Outcome
		kind     testkit.OutcomeKind
		auth     testkit.AuthObservation
		finding  testkit.Finding
		severity testkit.Severity
		stats    testkit.Stats
	)
	entry = sim.Requests(provider.Exa)[0]
	outcome = entry.Outcome
	kind = outcome.Kind
	auth = entry.Auth
	require.NotEmpty(t, entry.Errors())
	finding = entry.Errors()[0]
	severity = finding.Severity
	stats = testkit.Stats{Capacity: 1}

	assert.Equal(t, testkit.OutcomeError, kind)
	assert.True(t, auth.Present)
	assert.Equal(t, testkit.SeverityError, severity)
	assert.Equal(t, 1, stats.Capacity)
}

// countingJournal proves the Journal alias is implementable from outside this
// module: every method names only aliased types.
type countingJournal struct {
	mu      sync.Mutex
	seq     uint64
	entries []testkit.Entry
}

func (j *countingJournal) Next() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	return j.seq
}

func (j *countingJournal) Append(e testkit.Entry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = append(j.entries, e)
}

func (j *countingJournal) Snapshot() []testkit.Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]testkit.Entry(nil), j.entries...)
}

func (j *countingJournal) Reset() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = nil
	j.seq = 0
}

func (j *countingJournal) Stats() testkit.Stats {
	j.mu.Lock()
	defer j.mu.Unlock()
	return testkit.Stats{Capacity: len(j.entries), Stored: len(j.entries), Appended: j.seq}
}

var _ testkit.Journal = (*countingJournal)(nil)

func TestJournalAliasIsImplementable(t *testing.T) {
	t.Parallel()

	s, _, err := scenario.Parse([]byte(retryScenario))
	require.NoError(t, err)

	own := &countingJournal{}
	srv := httptest.NewServer(exa.New(provider.Deps{Scenario: s, Journal: own, Faults: testkit.NewFaults(s)}))
	t.Cleanup(srv.Close)

	req := newRequest(context.Background(), t, srv.URL+"/search", provider.Exa, `{"query":"report a"}`)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	entries := own.Snapshot()
	require.Len(t, entries, 1)
	assert.Equal(t, testkit.OutcomeFault, entries[0].Outcome.Kind)
	assert.Equal(t, 1, own.Stats().Stored)
}

// countingStreamJournal extends countingJournal with the CloseStream method
// journal.StreamCloser declares. It proves two things docs/design/streaming.md
// §5.2 promises together: that testkit.StreamClose is nameable in a
// consumer's own method signature (the compile-time half the var _ assertion
// below pins), and that a real streamed request through this repository's
// own Handle recognises the type as implementing the capability and drives
// it (the runtime half, proven by TestStreamAliasesSurviveAsAliasesAndAreDriven).
type countingStreamJournal struct {
	countingJournal
	mu     sync.Mutex
	closed []testkit.StreamClose
}

func (j *countingStreamJournal) CloseStream(_ string, _ uint64, c testkit.StreamClose) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.closed = append(j.closed, c)
}

func (j *countingStreamJournal) closes() []testkit.StreamClose {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]testkit.StreamClose(nil), j.closed...)
}

var _ interface {
	testkit.Journal
	CloseStream(namespace string, seq uint64, c testkit.StreamClose)
} = (*countingStreamJournal)(nil)

// TestStreamAliasesSurviveAsAliasesAndAreDriven is the streaming half of the
// alias-set closure: testkit.StreamOutcome, testkit.StreamState and
// testkit.StreamClose must be nameable outside this module AND a consumer's
// own Journal built from only those names must be recognised by the real
// production Handle, not merely satisfy an interface no code ever checks.
func TestStreamAliasesSurviveAsAliasesAndAreDriven(t *testing.T) {
	t.Parallel()

	s, _, err := scenario.Parse([]byte(`
version: 1
name: stream-alias-check
providers:
  perplexity:
    answer: hi
    stream:
      when_requested: stream
      deltas: ["hi"]
`))
	require.NoError(t, err)

	own := &countingStreamJournal{}
	srv := httptest.NewServer(perplexity.New(provider.Deps{Scenario: s, Journal: own, Faults: testkit.NewFaults(s)}))
	t.Cleanup(srv.Close)

	req := newRequest(context.Background(), t, srv.URL+"/v1/sonar", provider.Perplexity,
		`{"model":"sonar","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())

	entries := own.Snapshot()
	require.Len(t, entries, 1)
	var so *testkit.StreamOutcome = entries[0].Outcome.Stream
	require.NotNil(t, so, "testkit.StreamOutcome must be reachable off a real Entry")
	var state testkit.StreamState = so.State
	assert.Equal(t, testkit.StreamOpen, state,
		"planned at append; this journal never amends the stored entry itself, only records the close below")

	closed := own.closes()
	require.Len(t, closed, 1,
		"Handle must have recognised countingStreamJournal as a journal.StreamCloser (via testkit's aliased "+
			"CloseStream signature) and driven it — false here means the alias set silently stopped closing the loop")
	assert.Equal(t, testkit.StreamCompleted, closed[0].State)
}

// ownJobs proves the Jobs alias is implementable from outside this module:
// every method names only aliased types (Job, JobStats).
type ownJobs struct {
	mu   sync.Mutex
	jobs map[string]map[string]testkit.Job
}

// errOwnJobsDuplicate mirrors jobs.ErrDuplicate closely enough for this test's
// purposes: an own Store implementation is free to define its own sentinel.
var errOwnJobsDuplicate = errors.New("ownJobs: identifier already live in this namespace")

func (o *ownJobs) namespaceOf(namespace string) string {
	if namespace == "" {
		return provider.DefaultNamespace
	}
	return namespace
}

func (o *ownJobs) Create(j testkit.Job) (testkit.JobStats, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	ns := o.namespaceOf(j.Namespace)
	j.Namespace = ns
	if o.jobs == nil {
		o.jobs = map[string]map[string]testkit.Job{}
	}
	if _, exists := o.jobs[ns][j.ID]; exists {
		return testkit.JobStats{Count: len(o.jobs[ns])}, errOwnJobsDuplicate
	}
	if o.jobs[ns] == nil {
		o.jobs[ns] = map[string]testkit.Job{}
	}
	o.jobs[ns][j.ID] = j
	return testkit.JobStats{Count: len(o.jobs[ns])}, nil
}

func (o *ownJobs) Lookup(namespace, id string) (testkit.Job, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()

	j, ok := o.jobs[o.namespaceOf(namespace)][id]
	return j, ok
}

func (o *ownJobs) StatsIn(namespace string) testkit.JobStats {
	o.mu.Lock()
	defer o.mu.Unlock()

	return testkit.JobStats{Count: len(o.jobs[o.namespaceOf(namespace)])}
}

func (o *ownJobs) ResetIn(namespace string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	delete(o.jobs, o.namespaceOf(namespace))
}

func (o *ownJobs) Reset() {
	o.mu.Lock()
	defer o.mu.Unlock()

	clear(o.jobs)
}

var _ testkit.Jobs = (*ownJobs)(nil)

// TestJobsAliasIsImplementable is the compile-time half of the job alias set:
// a consumer must be able to name every type provider.Deps.Jobs requires
// (testkit.Jobs, and testkit.Job and testkit.JobStats from its method set) and
// wire its own store in place of testkit.NewJobs. The runtime half is a real
// create followed by a real poll, both answered from the own store.
func TestJobsAliasIsImplementable(t *testing.T) {
	t.Parallel()

	s, _, err := scenario.Parse([]byte(jobsScenario))
	require.NoError(t, err)

	own := &ownJobs{}
	srv := httptest.NewServer(exa.New(provider.Deps{Scenario: s, Faults: testkit.NewFaults(s), Jobs: own}))
	t.Cleanup(srv.Close)

	createReq := newRequest(context.Background(), t, srv.URL+"/agent/runs", provider.Exa, `{"query":"report a"}`)
	createResp, err := srv.Client().Do(createReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	var out struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&out))
	require.NoError(t, createResp.Body.Close())

	job, found := own.Lookup(provider.DefaultNamespace, out.ID)
	require.True(t, found, "the create must leave a record the own store can resolve")
	assert.Equal(t, out.ID, job.ID)

	pollReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		srv.URL+"/agent/runs/"+out.ID, nil)
	require.NoError(t, err)
	pollReq.Header.Set("x-api-key", "test-key")

	pollResp, err := srv.Client().Do(pollReq)
	require.NoError(t, err)
	require.NoError(t, pollResp.Body.Close())
	assert.Equal(t, http.StatusOK, pollResp.StatusCode, "the own store must resolve the poll too")
}

// searchIn issues the vendor request an adapter would issue against a base URL
// the caller chose. A namespaced call differs from an unprefixed one only in that
// base URL, which is the property the whole feature rests on: a consumer changes
// an environment variable and changes nothing else.
func searchIn(tb testing.TB, sim *testkit.Sim, base string, p provider.Name, body string) *http.Response {
	tb.Helper()

	resp, err := sim.Client().Do(newRequest(context.Background(), tb, base+"/search", p, body))
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// fatalTB extends stubTB with the two methods the namespace constructors use:
// Name, which NamespaceFor derives a namespace from, and Fatalf, which both
// constructors reject a name through. Recording the Fatalf rather than exiting
// lets a test read the rejection message and check the constructor returned nil,
// which is what a real testing.TB would never let it observe.
type fatalTB struct {
	*stubTB
	name string
}

// Name returns the test name this stub stands in for.
func (f *fatalTB) Name() string { return f.name }

// Fatalf records the message. The constructors return nil immediately after
// calling it, so control returning here matches what they expect.
func (f *fatalTB) Fatalf(format string, args ...any) { f.Errorf(format, args...) }

// newFatalTB builds a stub standing in for a test of the given name.
func newFatalTB(name string) *fatalTB {
	return &fatalTB{stubTB: &stubTB{}, name: name}
}

// TestNamespaceIsolatesFaultCursors is the failure this feature exists to fix.
// The scenario answers 429 once and then 200. Two namespaces sharing one Sim must
// each see the 429 first: a cursor keyed on the route alone would hand beta the
// success scripted for alpha's second call.
func TestNamespaceIsolatesFaultCursors(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(retryScenario), testkit.WithProviders(provider.Exa))
	alpha := sim.Namespace(t, "alpha")
	beta := sim.Namespace(t, "beta")

	first := searchIn(t, sim, alpha.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
	require.Equal(t, http.StatusTooManyRequests, first.StatusCode)

	// beta's first call is beta's first call.
	betaFirst := searchIn(t, sim, beta.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
	assert.Equal(t, http.StatusTooManyRequests, betaFirst.StatusCode,
		"a second namespace must start the fault plan from its first attempt")

	// alpha's own cursor advanced, and only alpha's.
	second := searchIn(t, sim, alpha.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
	assert.Equal(t, http.StatusOK, second.StatusCode)

	betaSecond := searchIn(t, sim, beta.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
	assert.Equal(t, http.StatusOK, betaSecond.StatusCode)

	testkit.AssertNamespacesIsolated(t, alpha, beta)
}

// TestNamespaceScopesTheJournal proves a parallel subtest can assert on its own
// traffic without knowing what else the process is serving.
func TestNamespaceScopesTheJournal(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	alpha := sim.Namespace(t, "alpha")
	beta := sim.Namespace(t, "beta")

	searchIn(t, sim, alpha.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
	searchIn(t, sim, beta.URL(provider.Exa), provider.Exa, `{"query":"report b"}`)
	searchIn(t, sim, alpha.URL(provider.Exa), provider.Exa, `{"query":"report c"}`)
	search(t, sim, provider.Exa, "/search", `{"query":"unprefixed"}`)

	assert.Len(t, alpha.Journal(), 2)
	assert.Len(t, beta.Journal(), 1)
	assert.Len(t, sim.Journal(), 4, "the Sim still sees every namespace")

	entries := alpha.Requests(provider.Exa)
	require.Len(t, entries, 2)
	for _, e := range entries {
		assert.Equal(t, "alpha", e.Namespace)
		testkit.AssertNoErrors(t, e)
	}
	assert.Equal(t, "beta", beta.Requests(provider.Exa)[0].Namespace)

	// The provider filter still applies inside a namespace.
	assert.Empty(t, alpha.Requests(provider.Tavily))
}

// TestNamespaceBaseURLsCarryThePrefix is the one-line adoption path: the same
// environment-shaped map the unprefixed Sim hands out, with isolation attached.
func TestNamespaceBaseURLsCarryThePrefix(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"))
	ns := sim.Namespace(t, "t-42")

	base := sim.BaseURLs()
	scoped := ns.BaseURLs()
	require.Len(t, scoped, len(base))
	for name, url := range base {
		assert.Equal(t, url+"/n/t-42", scoped[name], "%s must carry the namespace prefix", name)
	}
	assert.Equal(t, sim.URL(provider.Exa)+"/n/t-42", ns.URL(provider.Exa))
	assert.Equal(t, "t-42", ns.Name())
	assert.Same(t, sim.Client(), ns.Client())
}

// TestNamespaceURLIsEmptyWithoutAServer keeps the namespace view honest about a
// provider the Sim does not serve, exactly as Sim.URL is.
func TestNamespaceURLIsEmptyWithoutAServer(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	ns := sim.Namespace(t, "t-42")

	assert.Empty(t, ns.URL(provider.Tavily))
	assert.NotContains(t, ns.BaseURLs(), "TAVILY_BASE_URL")
}

// TestNamespaceChangesStateNotBehaviour pins the design's central claim: a
// namespace is a state boundary, so the response bytes are unchanged by it.
func TestNamespaceChangesStateNotBehaviour(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))

	plain := search(t, sim, provider.Exa, "/search", `{"query":"report a"}`)
	plainBody, err := io.ReadAll(plain.Body)
	require.NoError(t, err)

	scoped := searchIn(t, sim, sim.Namespace(t, "t-42").URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
	scopedBody, err := io.ReadAll(scoped.Body)
	require.NoError(t, err)

	assert.Equal(t, plain.StatusCode, scoped.StatusCode)
	assert.JSONEq(t, string(plainBody), string(scopedBody))
}

// TestNamespaceForIsOneLinePerSubtest is the consumer's test, written the way a
// consumer sharing one simulator across parallel subtests would write it. Each
// subtest sees the scenario from its first attempt because each has its own lane.
func TestNamespaceForIsOneLinePerSubtest(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(retryScenario), testkit.WithProviders(provider.Exa))

	for _, name := range []string{"first caller", "second caller", "third caller", "fourth caller"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ns := sim.NamespaceFor(t)

			rateLimited := searchIn(t, sim, ns.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
			require.Equal(t, http.StatusTooManyRequests, rateLimited.StatusCode)

			retried := searchIn(t, sim, ns.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
			require.Equal(t, http.StatusOK, retried.StatusCode)

			entries := ns.Requests(provider.Exa)
			require.Len(t, entries, 2, "the namespace sees its own two requests and no others")
			assert.Equal(t, 0, entries[0].Outcome.AttemptIndex)
			assert.Equal(t, 1, entries[1].Outcome.AttemptIndex)
		})
	}
}

// TestNamespaceForSanitisesTheTestName covers the reason NamespaceFor exists: a
// test name is not a legal namespace.
func TestNamespaceForSanitisesTheTestName(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))

	t.Run("a name with slashes, spaces and a dot.", func(t *testing.T) {
		t.Parallel()

		ns := sim.NamespaceFor(t)
		require.True(t, provider.ValidNamespace(ns.Name()), "derived %q", ns.Name())
		assert.NotContains(t, ns.Name(), "/")
		assert.Equal(t, ns.Name(), sim.NamespaceFor(t).Name(), "deriving twice for one test is stable")

		searchIn(t, sim, ns.URL(provider.Exa), provider.Exa, `{"query":"report a"}`)
		require.Len(t, ns.Requests(provider.Exa), 1)
		assert.Equal(t, ns.Name(), ns.Requests(provider.Exa)[0].Namespace)
	})
}

// TestNamespaceForKeepsTheTailOfALongName pins the truncation direction. Sibling
// subtests share a prefix and differ at the end, so keeping the front would put
// two tests in one lane.
func TestNamespaceForKeepsTheTailOfALongName(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	shared := strings.Repeat("Long", 30)

	first := sim.NamespaceFor(newFatalTB(shared + "/case-one"))
	second := sim.NamespaceFor(newFatalTB(shared + "/case-two"))

	require.NotNil(t, first)
	require.NotNil(t, second)
	assert.LessOrEqual(t, len(first.Name()), provider.MaxNamespaceNameLen)
	assert.NotEqual(t, first.Name(), second.Name())
	assert.True(t, provider.ValidNamespace(first.Name()))
}

// TestNamespaceForRejectsCollidingTestNames proves the collision fails loudly.
// Silently merging two tests' cursors is the failure namespaces exist to prevent.
func TestNamespaceForRejectsCollidingTestNames(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))

	first := newFatalTB("TestCollide/case a")
	require.NotNil(t, sim.NamespaceFor(first))
	require.False(t, first.Failed())

	second := newFatalTB("TestCollide#case-a")
	assert.Nil(t, sim.NamespaceFor(second), "a colliding derivation must not hand back a lane")
	assert.Contains(t, second.Message(), "TestCollide/case a")
	assert.Contains(t, second.Message(), "TestCollide#case-a")
}

// TestNamespaceForRejectsANamelessTest covers the one test name that yields
// nothing to derive from. A caller in that position is told to name the namespace
// itself rather than handed the default lane, which every other test shares.
func TestNamespaceForRejectsANamelessTest(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))

	stub := newFatalTB("")
	assert.Nil(t, sim.NamespaceFor(stub))
	assert.Contains(t, stub.Message(), "no usable namespace")
}

// TestNamespaceRejectsAnUnusableName fails at the line that made the mistake,
// rather than as a provider-shaped error on the first request.
func TestNamespaceRejectsAnUnusableName(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))

	for _, name := range []string{"", "has/slash", "has space", strings.Repeat("a", provider.MaxNamespaceNameLen+1)} {
		stub := newFatalTB("TestNamespaceRejectsAnUnusableName")
		assert.Nil(t, sim.Namespace(stub, name), "%q must be refused", name)
		assert.Contains(t, stub.Message(), "cannot be a namespace")
	}
}

// TestNamespaceAwaitRequestsAfterAbortingFault is Sim.AwaitRequests scoped to a
// lane: the entry is completed by a goroutine the client is no longer waiting on,
// so reading the namespace journal directly would be a race.
func TestNamespaceAwaitRequestsAfterAbortingFault(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(abortScenario), testkit.WithProviders(provider.Exa))
	ns := sim.Namespace(t, "t-42")

	req := newRequest(context.Background(), t, ns.URL(provider.Exa)+"/search", provider.Exa, `{"query":"report a"}`)
	resp, err := sim.Client().Do(req) //nolint:bodyclose // the connection was closed before any header
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("the request returned %d, want a transport error", resp.StatusCode)
	}

	entries := ns.AwaitRequests(t, provider.Exa, 1)
	require.Len(t, entries, 1)
	assert.True(t, entries[0].Outcome.Aborted)
	assert.Equal(t, "t-42", entries[0].Namespace)
}
