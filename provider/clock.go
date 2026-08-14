package provider

import (
	"context"
	"time"
)

// Clock is injectable time with exactly one job: stamping journal timestamps.
// Response bodies never read it (§3.2 of the package design), and fault delays
// deliberately do not go through it — see DelayMode. One method, because the
// two-method version made "how long the server sleeps" and "what the journal says
// the time is" the same knob, and any fake then broke client-deadline tests and
// AssertOverlapped at once.
//
// There is deliberately no FakeClock anywhere in this module. A clock pinned to a
// constant gives every journal entry the same instant, which makes
// testkit.AssertOverlapped either always false or vacuous.
type Clock interface {
	// Now returns the current instant. Two requests in flight must receive
	// distinguishable arrival and completion instants, which is what makes
	// testkit.AssertOverlapped meaningful; time.Now satisfies this and a clock
	// pinned to a constant does not.
	Now() time.Time
}

// SystemClock is the real-time Clock. It is the default in the binary, in the
// zero Deps, and in testkit.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// DelayMode selects what a delay fault does to the goroutine serving a request.
type DelayMode int

// Delay modes.
const (
	// DelayReal waits for the declared duration, cancellable by the request
	// context. It is the default, including in testkit, because a client deadline,
	// a context cancellation and a transport timeout are observed by *bytes not
	// arriving*: no in-process fake on the server side of the socket can produce
	// them. Timeout and deadline tests therefore use short scenario delays, not a
	// fake clock.
	DelayReal DelayMode = iota

	// DelaySkip returns immediately and still records the requested delay in
	// Outcome.DelayMS, which is what a test asserting "the scenario asked for 30s"
	// compares against. It is what keeps a 30-second backoff scenario free in unit
	// tests. A test that asserts context.DeadlineExceeded must not use it.
	DelaySkip
)

// String names the mode for logs and test failures.
func (m DelayMode) String() string {
	switch m {
	case DelayReal:
		return "real"
	case DelaySkip:
		return "skip"
	default:
		return "unknown"
	}
}

// sleep waits for d under mode, returning ctx.Err() when the context ended first
// so a delay fault yields promptly to a client deadline instead of pinning a
// goroutine. Under DelaySkip it returns nil immediately. Unexported: DelayMode is
// the entire public surface, and there is no FakeClock to keep in step with it.
func sleep(ctx context.Context, d time.Duration, mode DelayMode) error {
	if mode == DelaySkip || d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	if ctx == nil {
		<-timer.C
		return nil
	}
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
