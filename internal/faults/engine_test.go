package faults

import (
	"bytes"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// discard is the logger for tests that exercise a refusal without asserting on
// how it was reported, so a deliberate error never lands in the test output and
// gets read as a failure.
func discard() Option { return WithLogger(slog.New(slog.DiscardHandler)) }

// capturing returns an option wiring the engine to buf. The buffer is written
// only from the goroutine that calls Next, so the tests that use it are the
// single-goroutine ones.
func capturing(buf *bytes.Buffer) Option {
	return WithLogger(slog.New(slog.NewTextHandler(buf, nil)))
}

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

// TestNextIsolatesNamespaces is the regression test for the integration break
// between lane keying and this engine. provider.Handle claims attempts on
// provider.Lane.CursorKey, which embeds the namespace, while a plan is still
// registered per Route.FaultKey. An engine that only knows route keys answers
// every namespaced request with Unknown, so a scenario's declared 429 never
// fires inside a namespace and the turn cursor sticks at -1.
func TestNextIsolatesNamespaces(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{
		route("POST /search", "exa:search", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429}},
		}),
	})

	// Exhaust the default namespace's budget.
	assert.Equal(t, []int{429, 0}, statuses(e, "exa:search", 2))

	// A namespace draws on the same plan from its own counter, so it sees the
	// 429 the default lane already consumed.
	dec := e.Next("t-42/exa:search")
	require.False(t, dec.Unknown, "a namespaced lane over a registered route is not an unknown key")
	assert.Equal(t, 0, dec.Index, "a fresh namespace starts at attempt zero")
	assert.True(t, dec.Planned)
	require.NotNil(t, dec.Attempt)
	assert.Equal(t, 429, dec.Attempt.Status)
	assert.Equal(t, "t-42/exa:search", dec.Key, "the decision reports the lane it was drawn from")

	// Two namespaces are two budgets, and neither is the default's.
	assert.Equal(t, []int{429, 0}, statuses(e, "t-43/exa:search", 2))
	assert.Equal(t, []int{0}, statuses(e, "t-42/exa:search", 1), "t-42 resumes where it left off")
	assert.Equal(t, []int{0}, statuses(e, "exa:search", 1), "the default lane is untouched")
}

// TestNextIsolatesTurnLanes covers the other lane component: a turn_key that
// splits one route into several cursors. Both lanes expand the same plan,
// because a scenario declares one fault block per route and none per lane.
func TestNextIsolatesTurnLanes(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{
		route("POST /chat/completions", "perplexity:completions", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429}},
		}),
	})

	planner := "perplexity:completions|body_json:model=sonar-pro"
	worker := "perplexity:completions|body_json:model=sonar"

	assert.Equal(t, []int{429, 0}, statuses(e, planner, 2))
	assert.Equal(t, []int{429, 0}, statuses(e, worker, 2),
		"a second turn lane on one route gets its own attempt budget, not the first lane's remainder")

	// And a namespace composes with a turn lane rather than colliding with it.
	assert.Equal(t, []int{429}, statuses(e, "t-42/"+planner, 1))
	assert.Equal(t, []int{0}, statuses(e, planner, 1), "the unprefixed planner lane kept its position")
}

// TestNextLaneWithNoRouteComponentStaysUnknown pins the honest limit of plan
// resolution. A turn_key that omits "route" names a lane that embeds no route
// key, so the engine holds no plan it can be said to draw on and says so rather
// than guessing at one.
func TestNextLaneWithNoRouteComponentStaysUnknown(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{route("POST /search", "exa:search", nil)})

	dec := e.Next("t-42/body_json:model=sonar")
	assert.True(t, dec.Unknown)
	assert.Equal(t, -1, dec.Index)
}

// TestResetZeroesLaneCounters proves POST /__admin/reset reaches lanes that were
// created after construction. A reset that only walked the pre-registered map
// would leave every namespace mid-plan while reporting success.
func TestResetZeroesLaneCounters(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{
		route("POST /search", "exa:search", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429}},
		}),
	})

	assert.Equal(t, []int{429, 0}, statuses(e, "t-42/exa:search", 2))
	e.Reset()
	assert.Equal(t, []int{429, 0}, statuses(e, "t-42/exa:search", 2), "the lane replays its plan from attempt zero")
}

// TestNextLaneCountersAreRaceFree drives one new lane from many goroutines. The
// lane map is written at request time, unlike the pre-registered one, so it is
// the only counter map in this package where a lost update is possible: every
// claim must still get a distinct index with no gap.
func TestNextLaneCountersAreRaceFree(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{route("POST /search", "exa:search", nil)})

	const n = 64
	indices := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			indices[i] = e.Next("t-42/exa:search").Index
		}()
	}
	wg.Wait()

	slices.Sort(indices)
	want := make([]int, n)
	for i := range want {
		want[i] = i
	}
	require.Equal(t, want, indices, "every concurrent claim on one new lane got a distinct index")
}

// TestNextConcurrentNamespacesEachGetTheirOwnSequence is the property the whole
// feature exists for: several tests sharing one container, each declaring "one
// 429 then success", must each receive exactly one 429. Route-keyed counting
// hands the plan to whichever request arrives first and answers the rest with an
// unexpected success, which is the failure the sibling repositories hit.
func TestNextConcurrentNamespacesEachGetTheirOwnSequence(t *testing.T) {
	t.Parallel()

	const (
		namespaces = 6
		calls      = 8
		faulted    = 2 // attempts 0 and 1 of every namespace's own sequence
	)

	e := New(nil, []provider.Route{
		route("POST /search", "exa:search", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429, Repeat: faulted}},
		}),
	}, discard())

	// One slot per goroutine, so the results need no lock and the race detector
	// sees only the engine's own synchronisation.
	got := make([][]int, namespaces)
	for ns := range got {
		got[ns] = make([]int, calls)
	}

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
	)
	start.Add(1)
	for ns := range namespaces {
		key := "t-" + strconv.Itoa(ns) + "/exa:search"
		for call := range calls {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait() // explicit synchronisation, not a sleep
				dec := e.Next(key)
				if dec.Attempt == nil {
					return
				}
				got[ns][call] = dec.Attempt.Status
			}()
		}
	}
	start.Done()
	done.Wait()

	for ns := range namespaces {
		got429 := 0
		for _, status := range got[ns] {
			if status == 429 {
				got429++
			}
		}
		assert.Equal(t, faulted, got429,
			"namespace t-%d drew its own attempt sequence: exactly %d of its %d calls were faulted",
			ns, faulted, calls)
	}
}

// TestNextRefusesNamespacesBeyondTheBound covers the growth bound. Namespaces
// are created implicitly, so the map would otherwise grow with every test ID any
// consumer has ever sent.
func TestNextRefusesNamespacesBeyondTheBound(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	e := New(nil, []provider.Route{
		route("POST /search", "exa:search", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429}},
		}),
	}, WithMaxNamespaces(2), capturing(&buf))

	assert.Equal(t, []int{429, 0}, statuses(e, "t-1/exa:search", 2))
	assert.Equal(t, []int{429, 0}, statuses(e, "t-2/exa:search", 2))

	over := e.Next("t-3/exa:search")
	assert.True(t, over.Unknown, "a refused namespace is answered, loudly, with no budget at all")
	assert.Equal(t, -1, over.Index)
	assert.Nil(t, over.Attempt)
	assert.Contains(t, buf.String(), CodeNamespaceLimit)
	assert.Contains(t, buf.String(), "namespace=t-3")

	// The refusal must not have displaced anything: eviction is the failure this
	// bound exists to avoid, so an admitted namespace keeps its position.
	assert.Equal(t, []int{0}, statuses(e, "t-1/exa:search", 1), "t-1 resumed where it left off")
	assert.Equal(t, []int{0}, statuses(e, "t-2/exa:search", 1))

	// And a refusal is not a one-off: the second attempt is refused identically
	// rather than slipping into a budget the first request failed to create.
	assert.Equal(t, over, e.Next("t-3/exa:search"))
}

// TestNextBoundDoesNotCountTheDefaultNamespace pins that the default lane is not
// something a request created. Counting it would make --max-namespaces=1 admit
// no namespace at all.
func TestNextBoundDoesNotCountTheDefaultNamespace(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{
		route("POST /chat/completions", "perplexity:completions", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429}},
		}),
	}, WithMaxNamespaces(1), discard())

	// A turn lane in the default namespace is lane state, and it is still free.
	assert.Equal(t, []int{429, 0}, statuses(e, "perplexity:completions|body_json:model=sonar", 2))
	assert.Equal(t, []int{429, 0}, statuses(e, "perplexity:completions|body_json:model=sonar-pro", 2))

	// The one namespace the bound allows is still available afterwards.
	assert.Equal(t, []int{429}, statuses(e, "t-1/perplexity:completions", 1))
	assert.True(t, e.Next("t-2/perplexity:completions").Unknown, "the second namespace is over the bound")
}

// TestNextBoundCountsANamespaceOnce proves the bound counts namespaces rather
// than lanes: one test that keys turns per model must not consume the budget of
// the tests running beside it.
func TestNextBoundCountsANamespaceOnce(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{
		route("POST /chat/completions", "perplexity:completions", nil),
	}, WithMaxNamespaces(2), discard())

	for _, model := range []string{"sonar", "sonar-pro", "sonar-reasoning"} {
		key := "t-1/perplexity:completions|body_json:model=" + model
		assert.Equal(t, 0, e.Next(key).Index, "lane %q is new and starts at zero", key)
	}
	assert.False(t, e.Next("t-2/perplexity:completions").Unknown,
		"three lanes in one namespace consumed one namespace of budget, not three")
	assert.True(t, e.Next("t-3/perplexity:completions").Unknown)
}

// TestResetDoesNotReleaseAdmission pins the interaction between the two. Reset
// zeroes counters; it is not a namespace-freeing operation, because a namespace
// released by a reset would be re-admitted while the test that owns it is still
// running and would find its cursor back at zero.
func TestResetDoesNotReleaseAdmission(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{route("POST /search", "exa:search", nil)},
		WithMaxNamespaces(1), discard())

	require.Equal(t, 0, e.Next("t-1/exa:search").Index)
	require.True(t, e.Next("t-2/exa:search").Unknown)

	e.Reset()

	assert.Equal(t, 0, e.Next("t-1/exa:search").Index, "the admitted namespace replays from zero")
	assert.True(t, e.Next("t-2/exa:search").Unknown, "a reset did not free the namespace t-1 holds")
}

// TestNewDefaultsTheBoundAndLogger covers the unwired caller. internal/server and
// testkit construct the engine without options today, and a bound that only
// exists when someone remembers to pass it is not a bound.
func TestNewDefaultsTheBoundAndLogger(t *testing.T) {
	t.Parallel()

	e := New(nil, nil)
	assert.Equal(t, provider.DefaultMaxNamespaces, e.maxNamespaces)
	assert.NotNil(t, e.logger)

	assert.Equal(t, provider.DefaultMaxNamespaces, New(nil, nil, WithMaxNamespaces(0)).maxNamespaces,
		"a zero bound is unset, not a bound of zero")
	assert.Equal(t, 7, New(nil, nil, WithMaxNamespaces(7)).maxNamespaces)
	assert.NotNil(t, New(nil, nil, WithLogger(nil)).logger, "a nil logger falls back rather than panicking")
}

// TestNextAdmissionIsRaceFree drives more concurrent namespaces than the bound
// allows. Checking a count and storing without one admits more than the bound
// under a burst, which is a limit that reports a number it does not hold.
func TestNextAdmissionIsRaceFree(t *testing.T) {
	t.Parallel()

	const (
		attempts = 64
		bound    = 8
	)

	e := New(nil, []provider.Route{route("POST /search", "exa:search", nil)},
		WithMaxNamespaces(bound), discard())

	admitted := make([]bool, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			admitted[i] = !e.Next("t-" + strconv.Itoa(i) + "/exa:search").Unknown
		}()
	}
	wg.Wait()

	live := 0
	for _, ok := range admitted {
		if ok {
			live++
		}
	}
	assert.Equal(t, bound, live, "exactly the bound was admitted, no matter how the burst interleaved")

	e.mu.Lock()
	defer e.mu.Unlock()
	assert.Len(t, e.live, bound, "and the engine holds state for exactly that many namespaces")
}

// TestResetInScopesToOneNamespace is the engine half of
// POST /__admin/reset?namespace=<name>. The counter it zeroes is also the turn
// cursor, so the property under test is not "a counter went to zero" but "no
// other test's counter moved": on the shared container this feature exists for,
// a scoped reset that reached a neighbouring lane would serve that neighbour the
// turn scripted for a different call, and it would fail somewhere else entirely.
func TestResetInScopesToOneNamespace(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{
		route("POST /search", "exa:search", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429}},
		}),
	}, discard())

	require.Equal(t, []int{429, 0}, statuses(e, "t-1/exa:search", 2))
	require.Equal(t, []int{429, 0}, statuses(e, "t-2/exa:search", 2))
	require.Equal(t, []int{429, 0}, statuses(e, "exa:search", 2))

	e.ResetIn("t-1")

	assert.Equal(t, []int{429, 0}, statuses(e, "t-1/exa:search", 2),
		"t-1 replays its plan from attempt zero")
	assert.Equal(t, []int{0}, statuses(e, "t-2/exa:search", 1),
		"t-2 kept its position: its 429 was already spent")
	assert.Equal(t, []int{0}, statuses(e, "exa:search", 1),
		"the default lane kept its position too")
}

// TestResetInReachesEveryLaneOfTheNamespace covers a namespace whose turn_key
// split it into several cursors. Resetting the namespace has to reach all of
// them: one lane left mid-plan is the half-reset state the admin surface refuses
// to produce.
func TestResetInReachesEveryLaneOfTheNamespace(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{
		route("POST /chat/completions", "perplexity:completions", &scenario.Fault{
			Attempts: []scenario.FaultAttempt{{Status: 429}},
		}),
	}, discard())

	planner := "t-1/perplexity:completions|body_json:model=sonar-pro"
	worker := "t-1/perplexity:completions|body_json:model=sonar"
	neighbour := "t-2/perplexity:completions|body_json:model=sonar"

	require.Equal(t, []int{429, 0}, statuses(e, planner, 2))
	require.Equal(t, []int{429, 0}, statuses(e, worker, 2))
	require.Equal(t, []int{429, 0}, statuses(e, neighbour, 2))

	e.ResetIn("t-1")

	assert.Equal(t, []int{429}, statuses(e, planner, 1))
	assert.Equal(t, []int{429}, statuses(e, worker, 1))
	assert.Equal(t, []int{0}, statuses(e, neighbour, 1),
		"the same turn lane in another namespace is another test's cursor")
}

// TestResetInDefaultNamespace covers the lane every unprefixed request is served
// in. Its counters are the pre-registered route map rather than the lane map, so
// a scoped reset that only walked the lane map would report success and change
// nothing for a consumer that never used the /n/ prefix.
func TestResetInDefaultNamespace(t *testing.T) {
	t.Parallel()

	for _, name := range []string{provider.DefaultNamespace, ""} {
		t.Run("namespace="+name, func(t *testing.T) {
			t.Parallel()

			e := New(nil, []provider.Route{
				route("POST /search", "exa:search", &scenario.Fault{
					Attempts: []scenario.FaultAttempt{{Status: 429}},
				}),
			}, discard())

			defaultTurnLane := "exa:search|header:x-role=planner"
			require.Equal(t, []int{429, 0}, statuses(e, "exa:search", 2))
			require.Equal(t, []int{429, 0}, statuses(e, defaultTurnLane, 2))
			require.Equal(t, []int{429, 0}, statuses(e, "t-1/exa:search", 2))

			e.ResetIn(name)

			assert.Equal(t, []int{429}, statuses(e, "exa:search", 1),
				"the registered route counter is the default namespace's")
			assert.Equal(t, []int{429}, statuses(e, defaultTurnLane, 1),
				"a turn lane with no namespace prefix belongs to the default namespace")
			assert.Equal(t, []int{0}, statuses(e, "t-1/exa:search", 1),
				"a named namespace is not the default one")
		})
	}
}

// TestResetInReturnsTheNamespaceToTheBudget is the difference from
// TestResetDoesNotReleaseAdmission, and it is what keeps --max-namespaces a
// bound on LIVE namespaces rather than a cap on how many the process may ever
// see. A suite with more tests than the bound depends on each teardown handing
// its slot back; a namespace held forever after its test finished would refuse
// every later one.
//
// It also keeps this store in step with the journal, which frees its lane on a
// scoped reset. Two stores bounded from one --max-namespaces value that admitted
// different sets of namespaces would serve a test cursors with no journal, or a
// journal with no cursors.
func TestResetInReturnsTheNamespaceToTheBudget(t *testing.T) {
	t.Parallel()

	e := New(nil, []provider.Route{route("POST /search", "exa:search", nil)},
		WithMaxNamespaces(1), discard())

	require.Equal(t, 0, e.Next("t-1/exa:search").Index)
	require.True(t, e.Next("t-2/exa:search").Unknown, "the bound of one is spent")

	e.ResetIn("t-1")

	assert.Equal(t, 0, e.Next("t-2/exa:search").Index,
		"the finished namespace's slot was returned, so the next test is admitted")

	e.mu.Lock()
	defer e.mu.Unlock()
	assert.Len(t, e.live, 1, "and exactly one namespace is live, not two")
}

// TestResetInIsRaceFree runs scoped resets against live traffic in other
// namespaces. Resetting a lane while its own namespace is mid-flight renumbers
// that namespace, exactly as the admin surface documents; what must not happen
// is a data race or a lost update in the namespaces that were not named.
func TestResetInIsRaceFree(t *testing.T) {
	t.Parallel()

	const (
		namespaces = 6
		calls      = 40
	)

	e := New(nil, []provider.Route{route("POST /search", "exa:search", nil)},
		WithMaxNamespaces(namespaces+1), discard())

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
	)
	start.Add(1)

	// Every namespace but the last one is driven; the last is the only one reset,
	// so the driven lanes' claims are never renumbered underneath them and their
	// indices can be asserted exactly.
	indices := make([][]int, namespaces)
	for ns := range namespaces {
		indices[ns] = make([]int, calls)
		done.Add(1)
		go func() {
			defer done.Done()
			key := "t-" + strconv.Itoa(ns) + "/exa:search"
			start.Wait()
			for call := range calls {
				indices[ns][call] = e.Next(key).Index
			}
		}()
	}
	done.Add(1)
	go func() {
		defer done.Done()
		start.Wait()
		for range calls {
			e.Next("victim/exa:search")
			e.ResetIn("victim")
		}
	}()

	start.Done()
	done.Wait()

	want := make([]int, calls)
	for i := range want {
		want[i] = i
	}
	for ns := range namespaces {
		got := slices.Clone(indices[ns])
		slices.Sort(got)
		assert.Equal(t, want, got,
			"namespace t-%d claimed every index exactly once while another namespace was being reset", ns)
	}
}

// TestEngineDeclaresTheScopedResetCapability keeps the seam honest. The admin
// surface and internal/server both assert for this method rather than requiring
// it on provider.Faults — an exported interface consumers implement — so a
// silent signature drift here would not fail their builds. It would instead make
// POST /__admin/reset?namespace=<name> answer 501 in the shipped binary, which
// no existing test would notice.
func TestEngineDeclaresTheScopedResetCapability(t *testing.T) {
	t.Parallel()

	var scoped interface {
		provider.Faults
		ResetIn(namespace string)
	} = New(nil, nil)

	scoped.ResetIn("t-1") // a namespace that holds no state is not an error
}
