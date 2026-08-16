package examples_test

import (
	"testing"
	"time"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/testkit"
	"github.com/stretchr/testify/require"
)

// spacingLimiter is the few-line client-side limiter this test proves
// against the journal: it paces calls at least interval apart by waiting on
// a time.Timer channel rather than calling time.Sleep directly. The wait is
// the same real time either way — the limiter's own pacing is the thing
// under test — but reading from a channel keeps it an explicit
// synchronisation point instead of a bare sleep, which is the spirit of
// this repository's "no sleeps in tests" rule even though what is being
// proved here is the limiter's real-time behaviour, not a goroutine
// handoff.
//
// It deliberately does NOT wait on a time.Ticker. A Ticker fires on a fixed
// schedule anchored at its creation instant, not at its last delivery: a
// call that runs long enough to miss a tick leaves the next one already
// buffered, so wait() returns immediately and two dispatches land only
// milliseconds apart — a real burst, not a scheduling artifact, and exactly
// the kind of flake decision D5 exists to be safe from. Anchoring next on
// the PREVIOUS dispatch instead, and re-deriving it from time.Now() after
// every wait, means a slow round trip can only push the next slot later,
// never closer: dispatch-to-dispatch spacing is provably >= interval.
type spacingLimiter struct {
	interval time.Duration
	next     time.Time
}

func newSpacingLimiter(interval time.Duration) *spacingLimiter {
	return &spacingLimiter{interval: interval}
}

// wait blocks until the limiter's next slot, then reschedules the following
// one interval from now — not from the slot that was just filled — which is
// what keeps a stall from ever compressing a later gap.
func (l *spacingLimiter) wait() {
	if d := time.Until(l.next); d > 0 {
		t := time.NewTimer(d)
		<-t.C
	}
	l.next = time.Now().Add(l.interval)
}

// TestClientLimiterHoldsItsBudget is decision D5's pattern end to end
// (docs/adopter-backlog.md): Servicesim ships no enforced rate limiter — an
// enforced one would make response status a function of wall-clock time,
// exactly the flake the determinism doctrine refuses — so a consumer proves
// its OWN limiter against the journal's real arrival timestamps instead.
//
// spacingLimiter paces one call every interval; testkit.AssertMaxRate then
// reads the journal and confirms no window of length assertPer ever held
// more than one arrival — the RPS evidence a limiter is judged against.
//
// assertPer is deliberately smaller than interval, not equal to it. A Go
// timer never fires early relative to the monotonic clock, and
// spacingLimiter's own dispatch-to-dispatch spacing is provably >=
// interval — but AssertMaxRate reads the journal's SERVER-side arrival
// stamps, and every call here opens a fresh TCP connection
// (DisableKeepAlives is on sim.Client()), so each arrival adds its own
// independent connect/dispatch jitter on top of the dispatch time, and nothing
// requires consecutive calls' jitter to be equal. A busy machine widens that
// jitter further. Asserting the exact interval back would make this test
// flake on the very "loaded CI runner" D5's whole design is meant to be safe
// on. The margin below interval is what stays true to that: a limiter that
// is actually spacing its calls clears it easily, while a broken one
// (bursting with no wait at all) still fails it outright.
//
// Nothing here asserts an upper bound on elapsed time: a slower CI runner
// can only spread the calls further apart, which can never turn a budget
// that held into one that looks broken — see testkit.AssertMaxRate's own
// doc comment for why that direction is safe.
func TestClientLimiterHoldsItsBudget(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	adapter := newAdapter(sim.Client(), sim.BaseURLs())

	const (
		n         = 3
		interval  = 200 * time.Millisecond
		assertPer = 100 * time.Millisecond // see the doc comment for the margin
	)
	limiter := newSpacingLimiter(interval)

	for range n {
		limiter.wait()
		_, err := adapter.SearchExa(t.Context(), "report a")
		require.NoError(t, err)
	}

	entries := sim.Requests(provider.Exa)
	require.Len(t, entries, n)

	testkit.AssertMaxRate(t, entries, 1, assertPer)
}
