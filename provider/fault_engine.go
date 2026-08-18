package provider

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/c360studio/servicesim/scenario"
)

// faultConfig is FaultOption's target: exactly the two knobs internal/server
// passes today (server.go's faults.WithMaxNamespaces and faults.WithLogger)
// — nothing else is promoted.
type faultConfig struct {
	logger        *slog.Logger
	maxNamespaces int
}

// FaultOption configures the engine [(*Set).Faults] builds.
type FaultOption func(*faultConfig)

// WithFaultLogger sets the logger a refused namespace is reported to. nil
// selects slog.Default().
func WithFaultLogger(l *slog.Logger) FaultOption {
	return func(c *faultConfig) { c.logger = l }
}

// WithMaxNamespaces bounds how many namespaces may hold lane state in the
// engine [(*Set).Faults] builds. A value of zero or less selects
// DefaultMaxNamespaces.
func WithMaxNamespaces(n int) FaultOption {
	return func(c *faultConfig) { c.maxNamespaces = n }
}

// codeFaultNamespaceLimit is the log event a refused namespace raises. It is
// an error rather than a warning because the request that triggers it is
// served without the attempt budget its scenario declares, and the only
// correct response is to raise --max-namespaces or to stop creating
// namespaces.
//
// It is unexported, unlike internal/faults.CodeNamespaceLimit, which this
// type replaces: nothing outside this package constructed an engine
// directly even when internal/faults existed (testkit.NewFaults and
// internal/server both went through the package-level New), so there is no
// caller to keep a name public for. It carries a different spelling from
// the exported [CodeNamespaceLimit] in handle.go on purpose — that one is a
// namespace ADMISSION refusal raised by [Handle] before a route runs, this
// one is the engine's own log line when a LANE inside an admitted namespace
// cannot get a counter; the two are different events; see [CodeNamespaceLimit].
const codeFaultNamespaceLimit = "faults.namespace_limit"

// setFaultEngine is [(*Set).Faults]'s own implementation of the [Faults]
// seam, and — since Phase 10 unit 3 deleted internal/faults and rewired
// internal/server and testkit.NewFaults onto (*Set).Faults — the module's
// only fault engine. It carries over every behaviour internal/faults.Engine
// pinned: per-lane counters, namespace admission bounded by maxNamespaces,
// ResetIn's scoped release, the logger option, and Next's Unknown-key
// fail-open (see internal/faults/engine_test.go's git history for the tests
// this package's own fault_engine_test.go now owns).
type setFaultEngine struct {
	plans    map[string]faultPlan     // read-only after construction
	counters map[string]*atomic.Int64 // read-only map, mutable values

	maxNamespaces int
	logger        *slog.Logger

	// lanes holds counters for keys not themselves registered route keys:
	// namespaced and turn-keyed lanes. A sync.Map because its key set is
	// discovered at request time, unlike counters above.
	lanes sync.Map // string -> *atomic.Int64

	mu   sync.Mutex
	live map[string]struct{} // admitted namespaces, never removed
}

var (
	_ Faults            = (*setFaultEngine)(nil)
	_ NamespaceAdmitter = (*setFaultEngine)(nil)
)

// faultPlan is a scenario.Fault with Repeat expanded into a flat per-attempt
// slice, so the request path indexes a slice instead of walking a
// run-length encoding.
type faultPlan struct {
	attempts []scenario.FaultAttempt
	after    scenario.FaultAfter
}

// newSetFaultEngine builds the engine [(*Set).Faults] returns: one counter
// per route FaultKey in routes, pre-created so the counter map is read-only
// and therefore lock-free to read on the hot path. Aliases (two routes
// sharing one FaultKey) collapse into one entry. A nil sc and a nil
// Route.Fault are both fine and both mean "this route declares no plan".
func newSetFaultEngine(sc *scenario.Scenario, routes []Route, opts ...FaultOption) *setFaultEngine {
	var cfg faultConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.maxNamespaces <= 0 {
		cfg.maxNamespaces = DefaultMaxNamespaces
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}

	e := &setFaultEngine{
		plans:         make(map[string]faultPlan, len(routes)),
		counters:      make(map[string]*atomic.Int64, len(routes)),
		live:          make(map[string]struct{}),
		maxNamespaces: cfg.maxNamespaces,
		logger:        cfg.logger,
	}
	for _, rt := range routes { // every declared route, not just faulted ones
		if _, seen := e.counters[rt.FaultKey]; seen {
			continue // aliases share one budget
		}
		var f *scenario.Fault
		if rt.Fault != nil {
			f = rt.Fault(sc)
		}
		e.plans[rt.FaultKey] = expandFault(f)
		e.counters[rt.FaultKey] = new(atomic.Int64)
	}
	return e
}

// Next claims the next attempt index for key and returns what it receives.
// An unregistered key yields FaultDecision{Index: -1, Unknown: true}: no
// fault, and Handle records a fault.unknown_key warning, so a route added
// without a registered key is visible in the journal rather than silently
// serving a fault-free 200.
func (e *setFaultEngine) Next(key string) FaultDecision {
	c, planKey, ok := e.resolve(key)
	if !ok {
		return FaultDecision{Index: -1, Key: key, Unknown: true}
	}
	index := int(c.Add(1) - 1) // claim a unique zero-based index
	p := e.plans[planKey]
	return FaultDecision{
		Attempt: p.at(index),
		Index:   index,
		Key:     key,
		Planned: len(p.attempts) > 0,
	}
}

// resolve returns the counter key claims from and the plan key it draws its
// attempts from, or ok=false when nothing registered can serve it.
func (e *setFaultEngine) resolve(key string) (c *atomic.Int64, planKey string, ok bool) {
	if c, hit := e.counters[key]; hit {
		return c, key, true
	}
	namespace, parts := SplitCursorKey(key)
	planKey, ok = e.planFor(parts)
	if !ok {
		return nil, "", false
	}
	if c = e.laneCounter(key, namespace); c == nil {
		return nil, "", false // the namespace bound refused this lane
	}
	return c, planKey, true
}

// planFor returns the registered route key embedded in a lane key.
func (e *setFaultEngine) planFor(parts []string) (string, bool) {
	for _, part := range parts {
		if _, hit := e.plans[part]; hit {
			return part, true
		}
	}
	return "", false
}

// laneCounter returns the counter for a lane key, creating it on first use,
// or nil when creating it would put the namespace over the bound.
func (e *setFaultEngine) laneCounter(key, namespace string) *atomic.Int64 {
	if c, hit := e.lanes.Load(key); hit {
		return c.(*atomic.Int64)
	}

	e.mu.Lock()
	c, admitted := e.createLocked(key, namespace)
	e.mu.Unlock()

	if !admitted {
		e.logger.Error(codeFaultNamespaceLimit,
			slog.String("namespace", namespace),
			slog.Int("max_namespaces", e.maxNamespaces),
			slog.String("hint", "raise --max-namespaces; namespaces are never evicted, "+
				"because evicting one resets a running test's cursor mid-loop"))
	}
	return c
}

// createLocked returns the lane's counter, creating the lane and admitting
// its namespace if needed. admitted is false, and nothing is created, when
// the namespace is new and the bound is already full. The default namespace
// never consumes budget: it is not created by a request.
func (e *setFaultEngine) createLocked(key, namespace string) (c *atomic.Int64, admitted bool) {
	if existing, hit := e.lanes.Load(key); hit {
		return existing.(*atomic.Int64), true // another goroutine created it first
	}
	if _, live := e.live[namespace]; !live && namespace != DefaultNamespace {
		if len(e.live) >= e.maxNamespaces {
			return nil, false
		}
		e.live[namespace] = struct{}{}
	}
	c = new(atomic.Int64)
	e.lanes.Store(key, c)
	return c, true
}

// AdmitNamespace implements [NamespaceAdmitter], which [Handle] asks before
// running a route's handler, so a refusal renders as the profile's own
// error envelope instead of a 200 with no journal entry. Idempotent for an
// already-admitted namespace; the default namespace never consumes budget.
func (e *setFaultEngine) AdmitNamespace(ns string) bool {
	if ns == "" || ns == DefaultNamespace {
		return true
	}

	e.mu.Lock()
	_, live := e.live[ns]
	full := !live && len(e.live) >= e.maxNamespaces
	if !live && !full {
		e.live[ns] = struct{}{}
	}
	e.mu.Unlock()

	if full {
		e.logger.Error(codeFaultNamespaceLimit,
			slog.String("namespace", ns),
			slog.Int("max_namespaces", e.maxNamespaces),
			slog.String("hint", "raise --max-namespaces; namespaces are never evicted, "+
				"because evicting one resets a running test's cursor mid-loop"))
		return false
	}
	return true
}

// Reset stores zero into every counter, lane counters included.
func (e *setFaultEngine) Reset() {
	for _, c := range e.counters {
		c.Store(0)
	}
	e.lanes.Range(func(_, v any) bool {
		v.(*atomic.Int64).Store(0)
		return true
	})
}

// ResetIn drops one namespace's counters and leaves every other namespace
// exactly where it was, freeing its slot in the namespace bound. An empty
// namespace is the default one, whose route counters are the pre-registered
// map and so are zeroed in place rather than dropped.
func (e *setFaultEngine) ResetIn(namespace string) {
	if namespace == "" {
		namespace = DefaultNamespace
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if namespace == DefaultNamespace {
		for _, c := range e.counters {
			c.Store(0)
		}
	} else {
		delete(e.live, namespace)
	}

	e.lanes.Range(func(k, _ any) bool {
		if ns, _ := SplitCursorKey(k.(string)); ns == namespace {
			e.lanes.Delete(k)
		}
		return true
	})
}

// expandFault flattens Repeat into consecutive attempts once, at
// construction, so the request path never has to. It is nil-safe: a route
// with no declared plan expands to the empty plan, whose every attempt is
// unfaulted.
func expandFault(f *scenario.Fault) faultPlan {
	p := faultPlan{after: scenario.FaultAfterSuccess}
	if f == nil {
		return p
	}
	if f.After != "" {
		p.after = f.After
	}
	total := 0
	for _, a := range f.Attempts {
		total += a.Repeats()
	}
	p.attempts = make([]scenario.FaultAttempt, 0, total)
	for _, a := range f.Attempts {
		for range a.Repeats() {
			p.attempts = append(p.attempts, a)
		}
	}
	return p
}

// at returns the attempt serving zero-based index i, or nil when this
// attempt is not faulted. See internal/faults's identical plan.at for the
// index arithmetic this mirrors.
func (p faultPlan) at(i int) *scenario.FaultAttempt {
	if i < 0 || len(p.attempts) == 0 {
		return nil
	}
	if i < len(p.attempts) {
		return &p.attempts[i]
	}
	if p.after == scenario.FaultAfterRepeatLast {
		return &p.attempts[len(p.attempts)-1]
	}
	return nil
}
