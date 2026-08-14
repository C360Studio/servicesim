package faults

import (
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// fixed returns a route selector that always yields f, so a test can declare a
// plan without going through YAML. The real selectors are provider.TurnFault
// closures; TestNewFromScenario exercises that path.
func fixed(f *scenario.Fault) func(*scenario.Scenario) *scenario.Fault {
	return func(*scenario.Scenario) *scenario.Fault { return f }
}

func route(pattern, key string, f *scenario.Fault) provider.Route {
	return provider.Route{Pattern: pattern, FaultKey: key, Fault: fixed(f)}
}

// statuses drains n attempts from key and reports the status each received, with
// 0 standing for "no fault, serve the scenario response".
func statuses(e *Engine, key string, n int) []int {
	got := make([]int, 0, n)
	for range n {
		dec := e.Next(key)
		if dec.Attempt == nil {
			got = append(got, 0)
			continue
		}
		got = append(got, dec.Attempt.Status)
	}
	return got
}

func TestNextSelectsPerAttempt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		fault *scenario.Fault
		want  []int
	}{
		{
			name:  "no plan means no fault ever",
			fault: nil,
			want:  []int{0, 0, 0},
		},
		{
			name:  "empty attempt list behaves like no plan",
			fault: &scenario.Fault{},
			want:  []int{0, 0},
		},
		{
			name: "repeat expands into consecutive attempts and then succeeds",
			fault: &scenario.Fault{
				Attempts: []scenario.FaultAttempt{{Status: 429, Repeat: 3}},
			},
			want: []int{429, 429, 429, 0, 0},
		},
		{
			name: "repeat_last repeats the final attempt permanently",
			fault: &scenario.Fault{
				Attempts: []scenario.FaultAttempt{{Status: 500, Repeat: 2}},
				After:    scenario.FaultAfterRepeatLast,
			},
			want: []int{500, 500, 500, 500},
		},
		{
			name: "repeat zero and one are equivalent",
			fault: &scenario.Fault{
				Attempts: []scenario.FaultAttempt{{Status: 429}, {Status: 503, Repeat: 1}},
			},
			want: []int{429, 503, 0},
		},
		{
			name: "the plan document's own example still works",
			fault: &scenario.Fault{
				Attempts: []scenario.FaultAttempt{{Status: 429}, {Status: 200}},
			},
			want: []int{429, 200, 0},
		},
		{
			name:  "repeat_last with an empty list still never faults",
			fault: &scenario.Fault{After: scenario.FaultAfterRepeatLast},
			want:  []int{0, 0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := New(nil, []provider.Route{route("POST /search", "exa:search", tc.fault)})
			assert.Equal(t, tc.want, statuses(e, "exa:search", len(tc.want)))
		})
	}
}

// The trailing "status: 200" attempt of the plan document's example is a declared
// attempt that infers FaultNone, which is not the same thing as running off the
// end of the list. Both must reach execution as "serve the scenario response".
func TestNextDistinguishesDeclaredSuccessFromExhaustion(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{route("POST /search", "exa:search", &scenario.Fault{
		Attempts: []scenario.FaultAttempt{{Status: 429}, {Status: 200}},
	})})

	require.True(t, e.Next("exa:search").Faulted(), "attempt 0 is the 429")

	declared := e.Next("exa:search")
	require.NotNil(t, declared.Attempt, "attempt 1 is declared, so it carries an attempt")
	assert.Equal(t, scenario.FaultNone, declared.Attempt.EffectiveKind())
	assert.False(t, declared.Faulted(), "a declared 200 renders the scenario response")

	exhausted := e.Next("exa:search")
	assert.Nil(t, exhausted.Attempt, "past the end of the list there is no attempt at all")
	assert.False(t, exhausted.Faulted())
}

func TestNextReportsIndexAndPlanned(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{
		route("POST /search", "exa:search", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429}},
		}),
		route("POST /answer", "exa:answer", nil),
	})

	for i := range 3 {
		dec := e.Next("exa:search")
		assert.Equal(t, i, dec.Index, "the index is the zero-based arrival count")
		assert.Equal(t, "exa:search", dec.Key)
		assert.True(t, dec.Planned, "a key with a non-empty plan is Planned")
		assert.False(t, dec.Unknown)
	}

	// Planned stays false for a registered key with no plan, which is what keeps
	// the attempt index out of the derived identifier tuple (§3.1).
	dec := e.Next("exa:answer")
	assert.Equal(t, 0, dec.Index, "an unfaulted route still claims an index: the turn cursor reads it")
	assert.False(t, dec.Planned)
	assert.False(t, dec.Unknown)
	assert.Nil(t, dec.Attempt)
}

func TestNextUnknownKey(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{route("POST /search", "exa:search", nil)})

	dec := e.Next("tavily:search")
	assert.Equal(t, provider.FaultDecision{Index: -1, Key: "tavily:search", Unknown: true}, dec,
		"an unregistered key yields Unknown rather than a silent no-op")
	assert.False(t, dec.Faulted())

	// It must not create a counter, or the second unknown request would report a
	// different decision from the first and the warning would be intermittent.
	assert.Equal(t, dec, e.Next("tavily:search"))
	assert.Equal(t, 0, e.Next("exa:search").Index, "an unknown key consumes nothing from a real budget")
}

func TestNewSharesOneBudgetPerKey(t *testing.T) {
	t.Parallel()

	budget := &scenario.Fault{Attempts: []scenario.FaultAttempt{{Status: 429, Repeat: 2}}}
	e := New(nil, []provider.Route{
		route("POST /v1/sonar", "perplexity:completions", budget),
		route("POST /chat/completions", "perplexity:completions", budget),
		route("POST /search", "exa:search", budget),
	})

	// Perplexity's two patterns are aliases of one operation: a retry through the
	// alias must not be handed a fresh set of retries.
	assert.Equal(t, []int{429, 429, 0}, statuses(e, "perplexity:completions", 3))

	// Exa's routes are separate budgets, so an answer call cannot silently consume
	// the retries a search test declared.
	assert.Equal(t, []int{429, 429, 0}, statuses(e, "exa:search", 3))
}

// A duplicate key keeps the first route's plan. The alias case is the reason the
// key is shared at all, and a second selector for the same key is a wiring bug
// that must not silently win.
func TestNewKeepsTheFirstPlanForADuplicateKey(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{
		route("POST /v1/sonar", "perplexity:completions", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429}},
		}),
		route("POST /chat/completions", "perplexity:completions", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 500}},
		}),
	})

	assert.Equal(t, []int{429, 0}, statuses(e, "perplexity:completions", 2))
}

func TestNewToleratesNilSelectorsAndNoRoutes(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{{Pattern: "POST /search", FaultKey: "exa:search"}})
	dec := e.Next("exa:search")
	assert.False(t, dec.Unknown, "a route with a nil selector is registered, just unfaulted")
	assert.Nil(t, dec.Attempt)

	empty := New(nil, nil)
	assert.True(t, empty.Next("exa:search").Unknown)
	assert.NotPanics(t, empty.Reset)
}

func TestReset(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{
		route("POST /search", "exa:search", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429, Repeat: 2}},
		}),
		route("POST /search", "tavily:search", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 500}},
		}),
	})

	require.Equal(t, []int{429, 429, 0}, statuses(e, "exa:search", 3))
	require.Equal(t, []int{500, 0}, statuses(e, "tavily:search", 2))

	e.Reset()

	assert.Equal(t, []int{429, 429, 0}, statuses(e, "exa:search", 3), "every counter returns to zero")
	assert.Equal(t, []int{500, 0}, statuses(e, "tavily:search", 2))
	assert.Equal(t, 3, e.Next("exa:search").Index, "counting resumes from the reset sequence")
}

// The counter counts arrivals, so concurrent requests against one key must each
// claim a distinct index and the set must be exactly 0..n-1 with no gap and no
// duplicate. Which goroutine sees which index is deliberately unspecified (§4.4).
func TestNextConcurrentClaimsAreUnique(t *testing.T) {
	t.Parallel()

	const goroutines = 64

	e := New(nil, []provider.Route{
		route("POST /search", "exa:search", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429, Repeat: goroutines / 2}},
		}),
		route("POST /search", "tavily:search", nil),
	})

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		mu    sync.Mutex
	)
	indexes := make([]int, 0, 2*goroutines)
	start.Add(1)

	for range goroutines {
		for _, key := range []string{"exa:search", "tavily:search"} {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait() // explicit synchronisation, not a sleep
				dec := e.Next(key)
				mu.Lock()
				defer mu.Unlock()
				if key == "exa:search" {
					indexes = append(indexes, dec.Index)
				}
			}()
		}
	}
	start.Done()
	done.Wait()

	slices.Sort(indexes)
	want := make([]int, goroutines)
	for i := range want {
		want[i] = i
	}
	assert.Equal(t, want, indexes, "every arrival claims a unique zero-based index")

	assert.Equal(t, goroutines, e.Next("exa:search").Index, "the shared counter counted every arrival")
	assert.Equal(t, goroutines, e.Next("tavily:search").Index, "an unfaulted key counts arrivals too")
}

// The route selectors the server actually passes are provider.TurnFault closures
// over a loaded scenario. Exercising one end to end is what proves the engine is
// wired to the YAML rather than only to a hand-built Fault.
func TestNewFromScenario(t *testing.T) {
	t.Parallel()

	const src = `
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
          repeat: 2
        - status: 200
    results:
      - source: source-a
`

	s, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err, "report: %v", report)

	tavily := func(sc *scenario.Scenario) *scenario.Fault { return provider.TurnFault(sc, "tavily") }
	e := New(s, []provider.Route{
		{Pattern: "POST /search", FaultKey: "tavily:search", Fault: tavily},
		{Pattern: "POST /search", FaultKey: "exa:search", Fault: func(sc *scenario.Scenario) *scenario.Fault {
			return provider.TurnFault(sc, "exa")
		}},
	})

	assert.Equal(t, []int{429, 429, 200, 0}, statuses(e, "tavily:search", 4))

	first := New(s, []provider.Route{{Pattern: "POST /search", FaultKey: "tavily:search", Fault: tavily}}).
		Next("tavily:search")
	require.NotNil(t, first.Attempt)
	require.NotNil(t, first.Attempt.RetryAfter, "the whole attempt travels, not just its status")
	assert.Equal(t, 1, *first.Attempt.RetryAfter)

	assert.False(t, e.Next("exa:search").Planned, "a provider the scenario does not declare has no plan")
}
