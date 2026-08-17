package provider

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/c360studio/servicesim/internal/jobs"
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

	// DefaultMaxNamespaces bounds live namespaces when Deps leaves MaxNamespaces
	// zero.
	DefaultMaxNamespaces int = 1024
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

	// Planned reports whether Key has a non-empty expanded fault plan.
	//
	// Derived IDENTIFIERS no longer gate on it. A requestId, request_id or
	// completion id folds Index in unconditionally, because a real vendor issues
	// a distinct identifier per call and a simulator that repeated one value
	// collapses a consumer's log correlation to a single point. Determinism is
	// preserved in its precise form: the same request at the same call position
	// renders the same bytes, and a fresh lane starts at call 0 again.
	//
	// What still gates on it is a derived value that is a property of the
	// SCENARIO rather than of the call — Tavily's response_time is the one that
	// exists. See §3.1.
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

// NamespaceAdmitter is implemented by a Faults engine that bounds how many
// namespaces may hold state. [Handle] type-asserts for it, so an implementation
// that does not bound anything simply does not implement it and nothing is
// refused.
//
// It exists as a separate interface rather than a third field on
// [FaultDecision] because the question has to be asked at a different TIME than
// a fault decision is made. A decision is claimed after the handler has produced
// its response; admission has to be settled before, or a refusal cannot be
// rendered as the provider's own error and the client receives a 200.
type NamespaceAdmitter interface {
	// AdmitNamespace reports whether ns may hold state in this process.
	//
	// It must be idempotent for an already-admitted namespace — every request in
	// a namespace asks, not just the first — and it must reserve on first
	// admission, so two concurrent first requests naming new namespaces cannot
	// both be admitted past the bound.
	AdmitNamespace(ns string) bool
}

// noopFaults applies no fault while still claiming a per-key attempt index.
//
// It counts deliberately, and this is a documented amendment to §2.2, which
// specifies FaultDecision{Index: -1}. Under the turn model the attempt counter is
// also the turn cursor, so a substitute that never counts would pin every request
// of a zero-Deps handler to call index -1 and make `when: {call_index: 0}`
// unmatchable — the exact "two counters that disagree" bug class the addendum
// exists to prevent, arrived at from the other direction. Counting is free of
// determinism cost even though a derived identifier folds the index in: the
// index is a position within a lane rather than a clock reading, so the same
// request at the same call position still renders the same bytes (§3.1).
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
// usable: exa.Profile().Handler(provider.Deps{}) serves well-shaped empty
// successes with no journal, no faults and a real clock.
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
	// Deps by hand gets one from a Set's own (*Set).Faults(s) — the only
	// exported fault-engine constructor.
	//
	// Normalized substitutes a no-op implementation for nil: it claims attempt
	// indices, because the turn cursor reads them, and never returns an attempt.
	Faults Faults

	// Jobs holds the create-then-poll records the async surfaces mint and
	// resolve. nil means no job state at all: a create still derives and returns
	// an identifier, and the poll that follows simply cannot resolve it.
	//
	// That is the same shape as a nil Faults — usable, and wrong for a test that
	// meant to exercise the feature — rather than a nil-check in every handler.
	// Normalized does NOT substitute a registry, deliberately: a store created
	// per Deps would be invisible shared state for anyone who built two Deps
	// expecting one process, and the async surfaces are opt-in. internal/server
	// and testkit wire it explicitly.
	Jobs jobs.Store

	// MaxJobs is the per-namespace job bound this process was configured with.
	// Zero means jobs.DefaultMaxJobs.
	//
	// Like MaxNamespaces, the seam records the number rather than imposing it:
	// the bound belongs to the store that actually holds the records, and
	// internal/server builds the registry from this same value.
	MaxJobs int

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

	// MaxNamespaces is the bound on live namespaces this process was configured
	// with. Zero means DefaultMaxNamespaces.
	//
	// Namespaces are created implicitly on first use — requiring registration
	// would put a setup call in every consumer test, which is the friction the
	// feature exists to remove — so they are an unbounded-growth surface and need
	// a ceiling. Total journal retention is bounded by MaxNamespaces × the
	// per-journal capacity, and both are configurable.
	//
	// The seam does not enforce it. Lane resolution only NAMES a namespace, and
	// naming one must stay free; the bound belongs where namespace state is
	// actually created, which is the two stores wired into the fields above. The
	// journal's Ring refuses a lane beyond its Limits.MaxNamespaces, and the fault
	// engine refuses one beyond its own. internal/server builds both from the same
	// --max-namespaces value it puts here, so this field records the number those
	// stores are holding rather than imposing a third, separate ceiling.
	//
	// Neither store ever evicts. Silently dropping a namespace would reset a
	// running test's cursor mid-loop, which is the single worst failure this
	// design can produce, so a refusal is loud instead: the fault engine logs
	// faults.namespace_limit at error level naming the namespace and the bound,
	// the request's entry carries a CodeUnknownFaultKey warning because the engine
	// holds no budget that lane may draw on, and the journal retains nothing for
	// it while counting the append in its dropped total. The request is still
	// served — the seam has no refusal path of its own today, and inventing a
	// silent one would be worse than the noise.
	MaxNamespaces int
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
	if d.MaxNamespaces <= 0 {
		d.MaxNamespaces = DefaultMaxNamespaces
	}
	if d.MaxJobs <= 0 {
		d.MaxJobs = jobs.DefaultMaxJobs
	}
	if d.Faults == nil {
		if d.Scenario.HasFaults() {
			d.Logger.Warn("deps.faults_ignored",
				slog.String("scenario", d.Scenario.Name),
				slog.String("hint", "the scenario declares faults but Deps.Faults is nil; pass NewSet(ps...).Faults(s)"))
		}
		d.Faults = &noopFaults{}
	}
	return d
}
