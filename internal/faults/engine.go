package faults

import (
	"sync/atomic"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Engine selects faults. It is safe for concurrent use without locking on the hot
// path: the key set is fixed at construction from the loaded scenario, so both
// maps are read-only afterwards and only the per-key counters are mutable.
type Engine struct {
	plans    map[string]plan          // read-only after New
	counters map[string]*atomic.Int64 // read-only map, mutable values
}

// Engine is the module's only implementation of the seam's fault interface.
// Asserting it here means a signature drift in provider.Faults fails this
// package's build rather than internal/server's, where the cause is one import
// further away.
var _ provider.Faults = (*Engine)(nil)

// plan is a Fault with Repeat expanded into a flat per-attempt slice, so the
// request path indexes a slice instead of walking a run-length encoding.
type plan struct {
	attempts []scenario.FaultAttempt
	after    scenario.FaultAfter
}

// New builds an Engine from a scenario and the routes whose budgets it must
// serve. It expands each Repeat into consecutive attempts and pre-creates a
// counter for every route's FaultKey, including keys whose plan is empty.
// Pre-creation is what makes the counter map immutable and therefore lock-free to
// read; routes sharing a key (Perplexity's two patterns) collapse into one entry.
//
// The routes are passed in rather than derived here, because the keys are
// declared in provider/exa, provider/tavily and provider/perplexity at level 5,
// which this package must not import. Each route carries its own Route.Fault
// selector, so the scenario-field mapping travels with the key instead of being
// duplicated as a switch here — where a typo would silently disable a scenario's
// declared fault. internal/server and testkit build the slice by concatenating
// exa.Routes(), tavily.Routes() and perplexity.Routes().
//
// A nil scenario and a nil Route.Fault are both fine and both mean "this route
// declares no plan": the selectors are nil-safe on every hop so that a partial
// scenario produces a fault-free engine rather than a panic at construction.
func New(s *scenario.Scenario, routes []provider.Route) *Engine {
	e := &Engine{
		plans:    make(map[string]plan, len(routes)),
		counters: make(map[string]*atomic.Int64, len(routes)),
	}
	for _, rt := range routes { // every declared route, not just faulted ones
		if _, seen := e.counters[rt.FaultKey]; seen {
			continue // aliases share one budget: Perplexity's two patterns
		}
		var f *scenario.Fault
		if rt.Fault != nil {
			f = rt.Fault(s)
		}
		e.plans[rt.FaultKey] = expand(f)
		e.counters[rt.FaultKey] = new(atomic.Int64)
	}
	return e
}

// Next claims the next attempt index for key and returns what that attempt
// receives. It is the only mutating operation on the request path and is a single
// atomic add: Add returns the claimed value, so selection happens after the claim
// with no window in between and no lock is needed.
//
// An unregistered key yields FaultDecision{Index: -1, Key: key, Unknown: true}:
// no fault, and a fault.unknown_key warning recorded by provider.Handle, so a
// route added without a registered key is visible in /__admin/requests rather
// than silently serving a 200 where the scenario declares a 429.
func (e *Engine) Next(key string) provider.FaultDecision {
	c, ok := e.counters[key]
	if !ok {
		// Unknown key: fail open to "no fault" rather than panicking, but say so.
		// The silent version of this branch means a scenario's declared 429 never
		// fires and the consumer sees a 200 with no log line, no finding and no
		// failing test inside Servicesim.
		return provider.FaultDecision{Index: -1, Key: key, Unknown: true}
	}
	index := int(c.Add(1) - 1) // claim a unique zero-based index
	p := e.plans[key]
	return provider.FaultDecision{
		Attempt: p.at(index),
		Index:   index,
		Key:     key,
		Planned: len(p.attempts) > 0,
	}
}

// Reset stores zero into every counter. It backs POST /__admin/reset, which is a
// local-development convenience and not a concurrency mechanism: a reset racing
// live requests renumbers them, exactly as the admin surface documents.
func (e *Engine) Reset() {
	for _, c := range e.counters {
		c.Store(0)
	}
}

// expand flattens Repeat into consecutive attempts once, at construction, so the
// request path never has to. It is nil-safe: a route with no declared plan
// expands to the empty plan, whose every attempt is unfaulted.
func expand(f *scenario.Fault) plan {
	p := plan{after: scenario.FaultAfterSuccess}
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

// at returns the attempt serving zero-based index i, or nil when this attempt is
// not faulted. For a plan of length L:
//
//	i < L                          attempts[i]
//	i >= L and after == success    no fault — the scenario response is served
//	i >= L and after == repeat_last attempts[L-1], permanently
//
// The N+1th attempt after "fail the first N" is index N, which is >= L, which is
// the success branch. It is a pure function over an immutable slice, so it is
// trivially safe to call concurrently.
func (p plan) at(i int) *scenario.FaultAttempt {
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
