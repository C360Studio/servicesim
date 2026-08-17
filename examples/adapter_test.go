package examples_test

import (
	"net/http"
	"testing"

	"github.com/c360studio/servicesim/examples"
	"github.com/c360studio/servicesim/profiles/exa"
	"github.com/c360studio/servicesim/profiles/perplexity"
	"github.com/c360studio/servicesim/profiles/tavily"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The credentials the tests send. They are fake, and the point of naming them as
// constants is that testkit.AssertNoCredentialLeak can be handed the same
// literals: the assertion is only as strong as the string it is given.
const (
	exaKey        = "test-exa-key"
	tavilyKey     = "test-tavily-key"
	perplexityKey = "test-perplexity-key"
)

// newAdapter builds the example adapter from a set of environment-shaped base
// URLs — sim.BaseURLs() for a whole simulator, ns.BaseURLs() for one namespace —
// and the simulator's own client.
//
// Taking the map rather than the *testkit.Sim is what lets namespace_test.go
// reuse this verbatim, and it is also how a consumer's own configuration loader
// would be exercised: those three keys are exactly the environment variables
// Compose sets.
func newAdapter(client *http.Client, baseURLs map[string]string) *examples.Adapter {
	return examples.New(examples.Config{
		ExaBaseURL:        baseURLs["EXA_BASE_URL"],
		TavilyBaseURL:     baseURLs["TAVILY_BASE_URL"],
		PerplexityBaseURL: baseURLs["PERPLEXITY_BASE_URL"],

		ExaKey:        exaKey,
		TavilyKey:     tavilyKey,
		PerplexityKey: perplexityKey,

		// The Sim's client has keep-alives disabled, so a connection-abort fault
		// is observed by the adapter rather than absorbed by a pooled connection.
		HTTPClient: client,
	})
}

// TestAdapterSendsACorrectExaRequest is the canonical first test to copy.
//
// Note what it asserts and what it does not. It does not assert "the call
// returned a 200" — a simulator will return a 200 for a request the real vendor
// would reject, so a green status proves nothing about the request. It asserts on
// the journal: the credential was in the placement Exa documents, the body
// carried exactly the fields the adapter meant to send, and the simulator raised
// no error-severity finding against it.
func TestAdapterSendsACorrectExaRequest(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(exa.Profile()), testkit.WithBuiltin("happy"))
	adapter := newAdapter(sim.Client(), sim.BaseURLs())

	results, err := adapter.SearchExa(t.Context(), "report a")
	require.NoError(t, err)
	require.Len(t, results, 2, "the happy scenario projects two canonical sources through Exa")

	testkit.AssertRequestCount(t, sim, exa.Name, 1)
	entry := sim.Requests(exa.Name)[0]

	testkit.AssertAPIKeyHeader(t, entry)
	testkit.AssertJSONBody(t, entry, map[string]any{
		"query":      "report a",
		"numResults": 5,
	})
	// The one assertion that says "this is a request Exa would accept". It is not
	// AssertNoFindings: an unknown request field is a warning on purpose, so that
	// a consumer sending a field Servicesim has not modelled yet is not failed
	// here and passed in production.
	testkit.AssertNoErrors(t, entry)

	// No credential reached the journal by any path — headers, body, query string
	// or a finding message that quoted the request back.
	testkit.AssertNoCredentialLeak(t, sim, exaKey, tavilyKey, perplexityKey)

	// Anything the packaged assertions do not cover is a plain field read on the
	// entry. This is the escape hatch: the journal is data, not an opaque handle.
	assert.Equal(t, http.MethodPost, entry.Method)
	assert.Equal(t, "/search", entry.Path)
	assert.Equal(t, testkit.OutcomeScenario, entry.Outcome.Kind)
	assert.Equal(t, "application/json", http.Header(entry.Headers).Get("Content-Type"))
	assert.Equal(t, provider.DefaultNamespace, entryNamespace(entry))
}

// TestAdapterSendsACorrectTavilyRequest covers the second vendor, whose
// credential goes somewhere else entirely: Authorization: Bearer, never an
// api_key property in the body.
func TestAdapterSendsACorrectTavilyRequest(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(tavily.Profile()), testkit.WithBuiltin("happy"))
	adapter := newAdapter(sim.Client(), sim.BaseURLs())

	results, err := adapter.SearchTavily(t.Context(), "report a")
	require.NoError(t, err)
	require.Len(t, results, 2)

	testkit.AssertRequestCount(t, sim, tavily.Name, 1)
	entry := sim.Requests(tavily.Name)[0]

	testkit.AssertBearerAuth(t, entry)
	testkit.AssertJSONBody(t, entry, map[string]any{
		"query":       "report a",
		"max_results": 5,
	})
	testkit.AssertNoErrors(t, entry)
}

// TestAdapterSendsACorrectPerplexityRequest covers the third, where the request
// is a chat completion and the grounding a research adapter wants is in
// search_results rather than in the answer text.
func TestAdapterSendsACorrectPerplexityRequest(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(perplexity.Profile()), testkit.WithBuiltin("happy"))
	adapter := newAdapter(sim.Client(), sim.BaseURLs())

	results, err := adapter.SearchPerplexity(t.Context(), "report a")
	require.NoError(t, err)
	require.Len(t, results, 2)

	testkit.AssertRequestCount(t, sim, perplexity.Name, 1)
	entry := sim.Requests(perplexity.Name)[0]

	testkit.AssertBearerAuth(t, entry)
	testkit.AssertJSONBody(t, entry, map[string]any{
		"model": "sonar",
		"messages": []map[string]any{
			{"role": "user", "content": "report a"},
		},
	})
	testkit.AssertNoErrors(t, entry)

	assert.Equal(t, "/v1/sonar", entry.Path)
}

// TestAdapterNormalisesEveryProvider is the decoding half of the contract: three
// unrelated wire shapes reduced to one Result type, with each vendor's excerpt
// found where that vendor puts it.
func TestAdapterNormalisesEveryProvider(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(exa.Profile(), tavily.Profile(), perplexity.Profile()), testkit.WithBuiltin("happy"))
	adapter := newAdapter(sim.Client(), sim.BaseURLs())

	fromExa, err := adapter.SearchExa(t.Context(), "report a")
	require.NoError(t, err)
	fromTavily, err := adapter.SearchTavily(t.Context(), "report a")
	require.NoError(t, err)
	fromPerplexity, err := adapter.SearchPerplexity(t.Context(), "report a")
	require.NoError(t, err)

	for _, batch := range [][]examples.Result{fromExa, fromTavily, fromPerplexity} {
		require.NotEmpty(t, batch)
		for _, r := range batch {
			assert.NotEmpty(t, r.Title, "every provider names its documents")
			assert.NotEmpty(t, r.URL)
			assert.NotEmpty(t, r.Snippet, "every provider carries an excerpt somewhere")
			assert.Len(t, r.Providers, 1, "a single-provider call attributes to one provider")
		}
	}

	// The same canonical corpus, so the same documents, whichever vendor is asked.
	assert.Equal(t, urlsOf(fromExa), urlsOf(fromTavily))
	assert.Equal(t, urlsOf(fromExa), urlsOf(fromPerplexity))
}

// TestCanonicalURLCollapsesTrackingLinks pins the fusion key itself, without any
// HTTP involved. The fusion-overlap scenario returns the second URL below from
// one provider and the first from another; a consumer whose canonicaliser
// disagrees with this test double counts the document.
func TestCanonicalURLCollapsesTrackingLinks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"already canonical", "https://example.test/report-a", "https://example.test/report-a"},
		{
			name: "tracking parameters and a fragment",
			raw:  "https://example.test/report-a?utm_source=newsletter&utm_medium=email#summary",
			want: "https://example.test/report-a",
		},
		{"a trailing slash", "https://example.test/report-a/", "https://example.test/report-a"},
		{"a mixed-case host", "https://EXAMPLE.test/report-a", "https://example.test/report-a"},
		{
			name: "a meaningful parameter is kept",
			raw:  "https://example.test/report?id=7&utm_source=newsletter",
			want: "https://example.test/report?id=7",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, examples.CanonicalURL(tc.raw))
		})
	}
}

// urlsOf projects a result set onto its URLs, for comparing two providers'
// answers without comparing their prose.
func urlsOf(results []examples.Result) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, examples.CanonicalURL(r.URL))
	}
	return out
}

// entryNamespace reads the state lane an entry was recorded in, reporting the
// default lane for a request that named none — which is every request that did
// not go through a namespace's base URL.
func entryNamespace(e testkit.Entry) string {
	if e.Namespace == "" {
		return provider.DefaultNamespace
	}
	return e.Namespace
}
