package faults

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// CodeNamespaceLimit is the log event a refused namespace raises. It is an error
// rather than a warning because the request that triggers it is served without
// the attempt budget its scenario declares, and the only correct response is to
// raise --max-namespaces or to stop creating namespaces.
const CodeNamespaceLimit = "faults.namespace_limit"

// Engine selects faults. It is safe for concurrent use: plans and the
// pre-registered counter map are read-only after New, so the hot path for
// unprefixed traffic is a map read and one atomic add with no locking. Lane
// counters, which cannot be pre-registered because a namespace is created
// implicitly by the first request that names it, live in a sync.Map alongside.
type Engine struct {
	plans    map[string]plan          // read-only after New
	counters map[string]*atomic.Int64 // read-only map, mutable values

	// maxNamespaces bounds how many namespaces may hold lane state at once, and
	// logger receives the refusal. Both are read-only after New.
	maxNamespaces int
	logger        *slog.Logger

	// lanes holds counters for keys that are not themselves registered route
	// keys: namespaced and turn-keyed lanes, keyed by the full
	// provider.Lane.CursorKey. It is a sync.Map rather than the fixed map above
	// because its key set is discovered at request time.
	//
	// It must never evict. Dropping a lane resets a running test's cursor
	// mid-loop, which the design names as the single worst failure this feature
	// can produce; growth is bounded by refusing a new namespace at admission
	// instead.
	lanes sync.Map // string -> *atomic.Int64

	// mu guards every write to lanes and every access to live. Reads of lanes are
	// lock-free through the sync.Map; a lane is stored only after its namespace
	// has been admitted, so an observed lane always has an admitted namespace.
	//
	// The lock is taken once per new lane, not once per request: it is the
	// serialisation point that makes the bound exact. Checking a count and then
	// storing without one would admit maxNamespaces+N under a burst, which is the
	// shape of bound that reports a limit it does not actually hold.
	mu   sync.Mutex
	live map[string]struct{} // admitted namespaces, never removed
}

// Option configures an Engine at construction.
type Option func(*Engine)

// WithMaxNamespaces bounds how many namespaces may hold lane state in this
// engine. A value of zero or less selects provider.DefaultMaxNamespaces.
//
// Namespaces are created implicitly by the first request that names one, so
// without a bound a shared container's lane map grows with every test ID any
// consumer has ever sent. Exceeding the bound refuses the new namespace loudly;
// it never evicts an existing one, because evicting resets a running test's
// cursor mid-loop and that failure is invisible to the test it breaks.
func WithMaxNamespaces(n int) Option {
	return func(e *Engine) { e.maxNamespaces = n }
}

// WithLogger sets the logger the engine reports a refused namespace to. nil
// selects slog.Default(), because a bound whose refusal goes to a discard
// handler by default is a bound nobody finds out they hit.
func WithLogger(l *slog.Logger) Option {
	return func(e *Engine) { e.logger = l }
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
//
// Options configure the namespace bound and where a refused namespace is
// reported. The defaults — provider.DefaultMaxNamespaces and slog.Default() —
// are what an unwired caller gets, so the bound holds whether or not the caller
// remembered to pass it.
func New(s *scenario.Scenario, routes []provider.Route, opts ...Option) *Engine {
	e := &Engine{
		plans:    make(map[string]plan, len(routes)),
		counters: make(map[string]*atomic.Int64, len(routes)),
		live:     make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.maxNamespaces <= 0 {
		e.maxNamespaces = provider.DefaultMaxNamespaces
	}
	if e.logger == nil {
		e.logger = slog.Default()
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
//
// A lane whose namespace the bound refuses yields the same decision, and
// additionally logs faults.namespace_limit at error level. The seam carries no
// third outcome — provider.Faults returns a FaultDecision and nothing else — and
// of the two available answers this is the honest one: the engine holds no
// budget this lane may draw on, and says so on the entry and in the log rather
// than quietly lending it another namespace's.
func (e *Engine) Next(key string) provider.FaultDecision {
	c, planKey, ok := e.resolve(key)
	if !ok {
		// Unknown key: fail open to "no fault" rather than panicking, but say so.
		// The silent version of this branch means a scenario's declared 429 never
		// fires and the consumer sees a 200 with no log line, no finding and no
		// failing test inside Servicesim.
		return provider.FaultDecision{Index: -1, Key: key, Unknown: true}
	}
	index := int(c.Add(1) - 1) // claim a unique zero-based index
	p := e.plans[planKey]
	return provider.FaultDecision{
		Attempt: p.at(index),
		Index:   index,
		Key:     key,
		Planned: len(p.attempts) > 0,
	}
}

// resolve returns the counter key claims from and the plan key it draws its
// attempts from, or ok=false when nothing registered can serve it.
//
// The two keys differ exactly when the request carried a namespace or a
// turn_key: the plan stays per route, because a scenario declares one fault
// block per route and nothing per lane, while the counter is per lane, because
// two concurrent tests that share a route must not share its attempt budget.
//
// A registered route key hits the first branch and behaves precisely as it did
// before lanes existed — same fixed map, same atomic, no sync.Map traffic on the
// path every unprefixed request takes.
//
// The key is decomposed once and the parts are passed on, because splitting it
// again in each helper is a second reading of the same grammar and the two
// readings disagreeing is a lane counting on another lane's budget.
func (e *Engine) resolve(key string) (c *atomic.Int64, planKey string, ok bool) {
	if c, hit := e.counters[key]; hit {
		return c, key, true
	}

	namespace, parts := provider.SplitCursorKey(key)
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
//
// The parts come from provider.SplitCursorKey rather than a local copy of the
// grammar, and only an exact match against a registered key is accepted, so a
// lane named by a body field that happens to read like a route key cannot
// capture that route's plan.
//
// A lane whose turn_key declares no "route" extractor embeds no route key and
// resolves nothing. That request keeps the pre-lane unknown-key behaviour — no
// fault, and a fault.unknown_key warning on the journal entry — which is the
// honest answer: the engine holds no plan that this lane can be said to draw on.
func (e *Engine) planFor(parts []string) (string, bool) {
	for _, part := range parts {
		if _, hit := e.plans[part]; hit {
			return part, true
		}
	}
	return "", false
}

// laneCounter returns the counter for a lane key, creating it on first use, or
// nil when creating it would put namespace over the bound.
//
// The lock-free Load first: an established lane is the common case once a test
// is running, and it must not serialise on the admission mutex. A miss takes the
// lock and re-checks, because two requests naming a new lane at once must end up
// on one counter — otherwise each claims index 0 and both receive the first
// attempt of the plan.
func (e *Engine) laneCounter(key, namespace string) *atomic.Int64 {
	if c, hit := e.lanes.Load(key); hit {
		return c.(*atomic.Int64)
	}

	e.mu.Lock()
	c, admitted := e.createLocked(key, namespace)
	e.mu.Unlock()

	if !admitted {
		// Reported outside the lock: the logger's handler is caller-supplied, and
		// holding the admission mutex across it would let a slow handler serialise
		// every new lane in the process.
		e.logger.Error(CodeNamespaceLimit,
			slog.String("namespace", namespace),
			slog.Int("max_namespaces", e.maxNamespaces),
			slog.String("hint", "raise --max-namespaces; namespaces are never evicted, "+
				"because evicting one resets a running test's cursor mid-loop"))
	}
	return c
}

// createLocked returns the lane's counter, creating the lane and admitting its
// namespace if needed. It reports admitted=false, and creates nothing, when the
// namespace is new and the bound is already full.
//
// The default namespace never consumes budget. It is not created by a request —
// it is the lane every unprefixed request has always been served in — so
// counting it would make --max-namespaces=1 mean "no namespaces at all".
func (e *Engine) createLocked(key, namespace string) (c *atomic.Int64, admitted bool) {
	if existing, hit := e.lanes.Load(key); hit {
		return existing.(*atomic.Int64), true // another goroutine created it first
	}
	if _, live := e.live[namespace]; !live && namespace != provider.DefaultNamespace {
		if len(e.live) >= e.maxNamespaces {
			return nil, false
		}
		e.live[namespace] = struct{}{}
	}
	c = new(atomic.Int64)
	e.lanes.Store(key, c)
	return c, true
}

// AdmitNamespace reports whether ns may hold state in this process, reserving a
// slot on first admission. It implements [provider.NamespaceAdmitter], which
// [provider.Handle] asks BEFORE running a route's handler.
//
// The timing is the whole point. Admission used to be decided inside
// [Engine.Next], which a handler reaches only after it has produced its response:
// the refusal was logged at error level and the client still received a 200 with
// no journal entry. A test in the refused namespace then failed on an assertion
// about requests it had apparently never sent, with the real cause visible only
// in the simulator's stderr. Deciding here lets the refusal render as the
// provider's own error envelope, which is the one place the consumer will look.
//
// Idempotent for an already-admitted namespace, because every request asks, not
// just the first. The default namespace never consumes budget — it is not
// created by a request, so counting it would make --max-namespaces=1 mean "no
// namespaces at all".
func (e *Engine) AdmitNamespace(ns string) bool {
	if ns == "" || ns == provider.DefaultNamespace {
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
		// Reported outside the lock: the logger's handler is caller-supplied, and
		// holding the admission mutex across it would serialise every new lane in
		// the process behind a slow handler.
		e.logger.Error(CodeNamespaceLimit,
			slog.String("namespace", ns),
			slog.Int("max_namespaces", e.maxNamespaces),
			slog.String("hint", "raise --max-namespaces; namespaces are never evicted, "+
				"because evicting one resets a running test's cursor mid-loop"))
		return false
	}
	return true
}

// Reset stores zero into every counter, lane counters included. It backs
// POST /__admin/reset?all=true, which is a local-development convenience and not
// a concurrency mechanism: a reset racing live requests renumbers them, exactly
// as the admin surface documents. A caller that wants one test's counters and
// nobody else's asks [Engine.ResetIn] instead, which is why the admin surface
// makes "everything" the explicit spelling.
//
// Lane counters are zeroed rather than deleted. Deleting would be indisputably
// tidier and is wrong here: a lane counter is created by admission, and dropping
// the entry would let a reset silently undo an admission bound the process had
// already granted. Reset names no namespace, so it cannot be read as the caller
// saying it has finished with one; [Engine.ResetIn], which does name one, frees
// that namespace's slot.
func (e *Engine) Reset() {
	for _, c := range e.counters {
		c.Store(0)
	}
	e.lanes.Range(func(_, v any) bool {
		v.(*atomic.Int64).Store(0)
		return true
	})
}

// ResetIn drops one namespace's counters and leaves every other namespace
// exactly where it was. It is what makes POST /__admin/reset?namespace=<name>
// honourable: the attempt counter is also the turn cursor, so a scoped reset
// that could not reach it would empty a namespace's journal while leaving its
// script mid-plan, and the next request in that namespace would be served the
// turn written for a different call.
//
// An empty namespace is the default one. A lane belongs to a namespace exactly
// when provider.SplitCursorKey says so, which is the same grammar Next resolves
// on, so a reset cannot scope to a different set of lanes than the requests do.
// The default namespace's route counters are the pre-registered map, which is
// read-only by construction and is therefore zeroed in place rather than
// dropped; either way the next call in that lane claims index 0.
//
// A named namespace's slot returns to the maxNamespaces budget, which is the
// difference from [Engine.Reset] and is deliberate. The bound is over LIVE
// namespaces, and a suite that runs more tests than the bound depends on each
// test's teardown handing its slot back; a lane kept as an empty husk would turn
// --max-namespaces into a cap on how many distinct namespaces the process may
// ever see. It is not the silent eviction the design forbids either: the caller
// named this namespace, which is the caller stating it is finished with it, and
// the reset had already returned that namespace's cursor to zero. Reset takes no
// name and so cannot be read as saying that about anything, which is why it
// zeroes without releasing. journal.Ring.ResetIn frees its lane for the same
// reason, so after a scoped reset the two stores still admit exactly the same
// set of namespaces.
//
// The admission mutex is held throughout, because releasing a slot and dropping
// the lanes that hold it must not interleave with a concurrent admission — that
// is the same serialisation point that makes the bound exact on the way in. A
// request that loaded a counter before the drop increments an orphan and the
// next request in that lane starts a fresh one; reset racing live requests
// renumbers them either way, exactly as the admin surface documents.
func (e *Engine) ResetIn(namespace string) {
	if namespace == "" {
		namespace = provider.DefaultNamespace
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if namespace == provider.DefaultNamespace {
		for _, c := range e.counters {
			c.Store(0)
		}
	} else {
		delete(e.live, namespace)
	}

	e.lanes.Range(func(k, _ any) bool {
		if ns, _ := provider.SplitCursorKey(k.(string)); ns == namespace {
			e.lanes.Delete(k)
		}
		return true
	})
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
