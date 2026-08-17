package examples_test

import (
	"testing"

	"github.com/c360studio/servicesim/profiles/exa"
	"github.com/c360studio/servicesim/profiles/perplexity"
	"github.com/c360studio/servicesim/profiles/tavily"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParallelSubtestsShareOneSimulator is the pattern to copy when a suite runs
// its tests in parallel against one simulator — one process, or one container
// shared by a whole CI job.
//
// The whole mechanism is two lines inside the subtest: NamespaceFor(t) derives a
// state lane from the test's own name, and BaseURLs() hands back the same
// environment-shaped map as the Sim's, with that lane's prefix already in every
// URL. The adapter under test needs no change and no knowledge that namespaces
// exist.
//
// The scenario is rate-limited on purpose. A namespace isolates state, not
// behaviour: the scenario, the routes and the listeners are shared, while fault
// attempt counters, turn cursors and journal entries are per lane. So every
// subtest below sees the 429 first, concurrently, and none of them steals
// another's turn.
func TestParallelSubtestsShareOneSimulator(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(exa.Profile(), tavily.Profile(), perplexity.Profile()), testkit.WithBuiltin("rate-limited"))

	names := []string{"alpha", "beta", "gamma"}
	lanes := make([]*testkit.Namespace, len(names))

	// The wrapper subtest exists so the isolation assertion below runs after the
	// parallel children have finished: a parent's t.Run returns only once its
	// parallel subtests are done, and an assertion made while a request is still
	// in flight reads a gap that means nothing yet.
	t.Run("group", func(t *testing.T) {
		for i, name := range names {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				ns := sim.NamespaceFor(t)
				lanes[i] = ns

				adapter := newAdapter(ns.Client(), ns.BaseURLs())

				// Attempt 0 in this lane: every provider answers 429.
				_, err := adapter.Search(t.Context(), "report a")
				require.Error(t, err, "each namespace meets the rate limit on its own first attempt")

				// Attempt 1 in this lane: every provider serves the scenario.
				results, err := adapter.Search(t.Context(), "report a")
				require.NoError(t, err)
				require.NotEmpty(t, results)

				// The lane sees its own traffic and nothing else, however many
				// sibling subtests are running against the same listeners.
				for _, p := range []provider.Name{exa.Name, tavily.Name, perplexity.Name} {
					own := ns.Requests(p)
					require.Len(t, own, 2, "%s saw only this namespace's two calls", p)
					for _, e := range own {
						assert.Equal(t, ns.Name(), e.Namespace)
						testkit.AssertNoErrors(t, e)
					}
				}
			})
		}
	})

	// The property the whole pattern rests on, asserted rather than assumed: no
	// entry crossed lanes, neither lane was empty, no cursor key was shared, and
	// each lane's attempt indices run 0, 1, … with no gap. A gap is the
	// fingerprint of two lanes drawing on one counter — the failure that presents
	// as one test receiving the turn scripted for another.
	testkit.AssertNamespacesIsolated(t, lanes[0], lanes[1])
	testkit.AssertNamespacesIsolated(t, lanes[1], lanes[2])
}
