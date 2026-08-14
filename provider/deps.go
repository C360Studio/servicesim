package provider

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/scenario"
)

// Documented defaults for the zero Deps.
const (
	// DefaultMaxRequestBytes bounds the request body read when Deps leaves
	// MaxRequestBytes zero.
	DefaultMaxRequestBytes int64 = 1 << 20 // 1 MiB

	// DefaultMaxJournalBodyBytes bounds the body stored per journal entry when
	// Deps leaves MaxJournalBodyBytes zero.
	DefaultMaxJournalBodyBytes int = 64 << 10 // 64 KiB
)

// FaultDecision is the outcome of asking the fault engine what this attempt gets.
type FaultDecision struct {
	// Attempt is nil when this attempt is not faulted.
	Attempt *scenario.FaultAttempt

	// Index is the zero-based attempt number for the key, unique per arrival. It
	// is also the call index turn selection matches on: one counter, so the fault
	// plan and the conversation script cannot disagree about which call this is.
	// It is -1 only when no attempt was ever claimed — a request rejected by
	// routing, authentication or validation.
	Index int

	// Key is the fault budget this decision was drawn from.
	Key string

	// Planned reports whether Key has a non-empty expanded fault plan. It is what
	// keeps derived identifiers stable: the attempt index enters the identifier
	// tuple only for a route that actually declares a fault plan, so two identical
	// happy-path requests against one Sim render byte-identical bodies. See §3.1.
	Planned bool

	// Unknown reports that the engine holds no plan for Key at all — a route whose
	// key was never registered. Handle records a fault.unknown_key warning on the
	// entry so the drift is visible in /__admin/requests instead of silently
	// serving a fault-free 200 where the scenario declares a 429.
	Unknown bool
}

// Faulted reports whether a fault applies.
func (d FaultDecision) Faulted() bool {
	return d.Attempt != nil && (d.Attempt.EffectiveKind() != scenario.FaultNone || d.Attempt.Delay > 0)
}

// Faults selects a fault for an attempt. Implementations must be safe for
// concurrent use; see §4 of the package design.
type Faults interface {
	// Next claims the next attempt index for key and returns what it receives.
	Next(key string) FaultDecision

	// Reset returns every counter to zero.
	Reset()
}

// noopFaults applies no fault while still claiming a per-key attempt index.
//
// It counts deliberately, and this is a documented amendment to §2.2, which
// specifies FaultDecision{Index: -1}. Under the turn model the attempt counter is
// also the turn cursor, so a substitute that never counts would pin every request
// of a zero-Deps handler to call index -1 and make `when: {call_index: 0}`
// unmatchable — the exact "two counters that disagree" bug class the addendum
// exists to prevent, arrived at from the other direction. Counting is free of
// determinism cost because Planned stays false, so the index never enters a
// derived identifier (§3.1).
type noopFaults struct {
	counters sync.Map // string -> *atomic.Int64
}

func (f *noopFaults) counter(key string) *atomic.Int64 {
	if c, ok := f.counters.Load(key); ok {
		return c.(*atomic.Int64)
	}
	c, _ := f.counters.LoadOrStore(key, new(atomic.Int64))
	return c.(*atomic.Int64)
}

// Next claims an index and applies no fault. Unknown stays false: there is no
// plan to be missing, so no request collects a spurious fault.unknown_key
// warning.
func (f *noopFaults) Next(key string) FaultDecision {
	return FaultDecision{Index: int(f.counter(key).Add(1) - 1), Key: key}
}

// Reset zeroes every counter this instance has handed out.
func (f *noopFaults) Reset() {
	f.counters.Range(func(_, v any) bool {
		v.(*atomic.Int64).Store(0)
		return true
	})
}

// Deps is everything a provider handler is constructed with. The zero value is
// usable: exa.New(provider.Deps{}) serves well-shaped empty successes with no
// journal, no faults and a real clock.
type Deps struct {
	// Scenario is the loaded, validated, resolved corpus. nil means scenario.Empty().
	Scenario *scenario.Scenario

	// Journal records every request. nil means a fresh journal.NewDiscard(), which
	// is per-Deps and never shared, so two handlers in parallel tests cannot draw
	// sequence numbers from one another's counter.
	Journal journal.Journal

	// Faults selects deterministic failures. nil means no faults are applied at
	// all — including faults the Scenario declares. Because that combination is
	// almost always a wiring mistake rather than an intent, Normalized logs a
	// deps.faults_ignored warning when Scenario.HasFaults() is true and Faults is
	// nil. testkit.Start and internal/server always wire it; a consumer building
	// Deps by hand gets one from testkit.NewFaults(s).
	//
	// Normalized substitutes a no-op implementation for nil: it claims attempt
	// indices, because the turn cursor reads them, and never returns an attempt.
	Faults Faults

	// Clock stamps journal timestamps and nothing else. nil means SystemClock{}.
	Clock Clock

	// DelayMode selects whether a delay fault actually waits. The zero value is
	// DelayReal, so a scenario that declares delay: 250ms blocks a client for
	// 250ms whether it is served in-process or from the container.
	DelayMode DelayMode

	// Logger receives one structured event per completed request. nil means
	// slog.New(slog.DiscardHandler).
	Logger *slog.Logger

	// MaxRequestBytes bounds the request body read. Zero means
	// DefaultMaxRequestBytes.
	MaxRequestBytes int64

	// MaxJournalBodyBytes bounds the body stored per journal entry. Zero means
	// DefaultMaxJournalBodyBytes. It is applied by the journal implementation, at
	// the storage boundary where redaction happens, not by the request path: a
	// body clipped before redaction is a body redaction can no longer parse.
	MaxJournalBodyBytes int
}

// Normalized returns a copy of d with every nil and zero field replaced by its
// documented default. Handler constructors call it once, so no request path ever
// nil-checks a dependency. It is also the one place a misconfiguration is
// reported: a scenario that declares faults with no Faults engine logs
// deps.faults_ignored at warn level, once per handler construction, rather than
// silently serving fault-free responses.
//
// It is idempotent, which is what lets NewMux normalise once and share the result
// across every route it registers. Without that, each route would build its own
// substitute journal and its own substitute fault counter, and two aliases of one
// operation would stop sharing a budget the moment the caller passed a zero Deps.
func (d Deps) Normalized() Deps {
	if d.Scenario == nil {
		d.Scenario = scenario.Empty()
	}
	if d.Journal == nil {
		d.Journal = journal.NewDiscard()
	}
	if d.Clock == nil {
		d.Clock = SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if d.MaxRequestBytes <= 0 {
		d.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if d.MaxJournalBodyBytes <= 0 {
		d.MaxJournalBodyBytes = DefaultMaxJournalBodyBytes
	}
	if d.Faults == nil {
		if d.Scenario.HasFaults() {
			d.Logger.Warn("deps.faults_ignored",
				slog.String("scenario", d.Scenario.Name),
				slog.String("hint", "the scenario declares faults but Deps.Faults is nil; pass testkit.NewFaults(s)"))
		}
		d.Faults = &noopFaults{}
	}
	return d
}
