package examples_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/c360studio/servicesim/examples"
	"github.com/c360studio/servicesim/profiles/exa"
	"github.com/c360studio/servicesim/profiles/perplexity"
	"github.com/c360studio/servicesim/profiles/tavily"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSearchFusesOverlappingSources is the multi-provider case the
// fusion-overlap scenario was built for.
//
// The scenario returns one canonical document — report-a — from Exa, Tavily and
// Perplexity, and Tavily returns it twice: once through a newsletter link
// carrying tracking parameters and a fragment, and once plainly. Four wire
// results, two documents. A consumer that deduplicates on the raw URL string
// reports three, and the extra copy looks exactly like corroboration.
func TestSearchFusesOverlappingSources(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(exa.Profile(), tavily.Profile(), perplexity.Profile()), testkit.WithBuiltin("fusion-overlap"))
	adapter := newAdapter(sim.Client(), sim.BaseURLs())

	results, err := adapter.Search(t.Context(), "report a")
	require.NoError(t, err)

	require.Equal(t, []string{
		"https://example.test/report-a",
		"https://example.test/report-b",
	}, urlsOf(results), "the URL variant collapsed onto its canonical document")

	// Corroboration is countable only after the fusion: report-a is one document
	// that three providers agree on, not three documents.
	assert.Equal(t,
		[]string{examples.ProviderExa, examples.ProviderTavily, examples.ProviderPerplexity},
		results[0].Providers)

	// Each provider was asked exactly once, and each request was one the vendor
	// would have accepted.
	for _, p := range []provider.Name{exa.Name, tavily.Name, perplexity.Name} {
		testkit.AssertRequestCount(t, sim, p, 1)
		testkit.AssertNoErrors(t, sim.Requests(p)[0])
	}
	testkit.AssertNoCredentialLeak(t, sim, exaKey, tavilyKey, perplexityKey)
}

// concurrentScenario delays every provider's first attempt, so that two calls
// which really did run at the same time have an overlap wide enough to prove.
//
// The delay is the point. With an instant response the two entries can be
// microseconds apart and still not overlap, and an assertion that depends on the
// scheduler winning a race is a future flake — which is worse than no assertion,
// because it fails in someone else's pipeline. Declaring the delay makes the
// window deterministic. Note that testkit.WithSkippedDelays must NOT be used
// here: it would remove the very window being measured.
const concurrentScenario = `
version: 1
name: examples-concurrent-fan-out
description: Every provider takes a visible moment to answer.

sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    snippets:
      - A relevant excerpt from Report A.

providers:
  exa:
    fault:
      attempts:
        - delay: 150ms
    results:
      - source: source-a
        highlights:
          - A relevant excerpt from Report A.
  tavily:
    fault:
      attempts:
        - delay: 150ms
    response_time: 1.15
    results:
      - source: source-a
        score: 0.98
  perplexity:
    fault:
      attempts:
        - delay: 150ms
    answer: A grounded answer citing Report A.
    finish_reason: stop
    search_results:
      - source-a
`

// TestSearchQueriesProvidersConcurrently proves the fan-out is a fan-out.
//
// A serial adapter passes every other test in this directory: it sends the same
// three correct requests and fuses the same results, only slower. The journal's
// arrival and completion instants are the only evidence that distinguishes the
// two, and AssertOverlapped is how that evidence is read.
func TestSearchQueriesProvidersConcurrently(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(exa.Profile(), tavily.Profile(), perplexity.Profile()), testkit.WithScenarioYAML(concurrentScenario))
	adapter := newAdapter(sim.Client(), sim.BaseURLs())

	results, err := adapter.Search(t.Context(), "report a")
	require.NoError(t, err)
	require.Len(t, results, 1)

	exa := sim.Requests(exa.Name)[0]
	tavily := sim.Requests(tavily.Name)[0]
	perplexity := sim.Requests(perplexity.Name)[0]

	testkit.AssertOverlapped(t, exa, tavily)
	testkit.AssertOverlapped(t, exa, perplexity)
	testkit.AssertOverlapped(t, tavily, perplexity)
}

// TestSearchExaClassifiesARateLimitAndRetriesIt is the failure half of the
// contract: a 429 must be recognised as retryable, must carry its Retry-After
// budget to the caller, and must not be mistaken for a malformed request.
//
// The rate-limited scenario answers 429 once per lane and then serves the
// ordinary response, so the retry is counted rather than restarting the plan.
// That is what makes the second call succeed and the third-attempt case
// unreachable.
func TestSearchExaClassifiesARateLimitAndRetriesIt(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithProfiles(exa.Profile()),
		testkit.WithBuiltin("rate-limited"),
		testkit.WithProviders(exa.Name))
	adapter := newAdapter(sim.Client(), sim.BaseURLs())

	_, err := adapter.SearchExa(t.Context(), "report a")

	var apiErr *examples.APIError
	require.ErrorAs(t, err, &apiErr, "a non-2xx must reach the caller as a classifiable error")
	assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
	assert.Equal(t, examples.ProviderExa, apiErr.Provider)
	assert.True(t, apiErr.Retryable(), "a 429 is worth sending again")
	assert.Equal(t, time.Second, apiErr.RetryAfter, "the scenario declares retry_after: 1")

	// A real adapter would sleep for apiErr.RetryAfter here. A test must not:
	// the assertion worth making is "the adapter waits for what the vendor asked
	// for", and paying the wait proves nothing that reading the field does not.
	results, err := adapter.SearchExa(t.Context(), "report a")
	require.NoError(t, err)
	require.NotEmpty(t, results)

	entries := sim.Requests(exa.Name)
	require.Len(t, entries, 2)

	// Both requests were well formed. The 429 was the scenario's declared fault,
	// not a rejection the adapter earned — a distinction a status code alone
	// cannot make, and the reason to assert on the outcome kind.
	for _, e := range entries {
		testkit.AssertNoErrors(t, e)
		testkit.AssertAPIKeyHeader(t, e)
	}
	assert.Equal(t, testkit.OutcomeFault, entries[0].Outcome.Kind)
	assert.Equal(t, testkit.OutcomeScenario, entries[1].Outcome.Kind)

	// The same credential on the retry as on the first attempt, proved by
	// fingerprint: the journal never holds a key, so this is the only comparison
	// available — and the reason it is safe to run against real configuration.
	testkit.AssertSameCredential(t, entries[0], entries[1])
}

// TestSearchExaDoesNotRetryARejectedCredential is the other half of
// classification. A 401 will fail identically forever, and an adapter that
// retries it burns its budget and delays the only useful signal: the key is
// wrong.
func TestSearchExaDoesNotRetryARejectedCredential(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithProfiles(exa.Profile()),
		testkit.WithBuiltin("unauthorized"),
		testkit.WithProviders(exa.Name))
	adapter := newAdapter(sim.Client(), sim.BaseURLs())

	_, err := adapter.SearchExa(t.Context(), "report a")

	var apiErr *examples.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
	assert.False(t, apiErr.Retryable(), "a rejected credential is not a transient failure")

	testkit.AssertRequestCount(t, sim, exa.Name, 1)
	entry := sim.Requests(exa.Name)[0]

	// The finding names the cause. Asserting on it rather than on the status is
	// what distinguishes "the scenario rejects this key" from any other 401 the
	// simulator could have produced.
	testkit.AssertFindings(t, entry, "auth.mismatch")
	testkit.AssertNoCredentialLeak(t, sim, exaKey)
}
