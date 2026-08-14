package testkit_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/provider/exa"
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
}

func TestBaseURLsAreEnvironmentShaped(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa, provider.Tavily))

	urls := sim.BaseURLs()

	assert.Equal(t, sim.URL(provider.Exa), urls["EXA_BASE_URL"])
	assert.Equal(t, sim.URL(provider.Tavily), urls["TAVILY_BASE_URL"])
	assert.NotContains(t, urls, "PERPLEXITY_BASE_URL", "WithProviders excluded Perplexity")
	assert.Empty(t, sim.URL(provider.Perplexity))
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
