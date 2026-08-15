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
