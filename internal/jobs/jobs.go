// Package jobs holds the create-then-poll job records that the async provider
// surfaces mint and resolve.
//
// It is the first cross-request state in this process. Until now the only state
// outliving a request was the journal ring and the fault attempt counters, and
// both are observational: losing an entry loses a test's ability to SEE what
// happened, never its ability to be served correctly. A job record is different.
// An identifier minted by one request and presented by another is the thing the
// second request is answered from, so losing one turns a poll into the vendor's
// 404 for a job that genuinely exists.
//
// That difference drives every decision here, and the two worth knowing before
// reading further are:
//
//   - The bound REFUSES; it never evicts. journal.Ring evicts its oldest entry
//     when a lane fills, which is right for it and wrong here — see [Registry].
//   - Records are keyed on (namespace, id), never on id alone. Identifiers are
//     derived without the namespace so a golden file is portable between an
//     unnamespaced run and a namespaced one, which means two concurrent
//     namespaces legitimately mint the SAME identifier at the same call
//     position. Keying on id alone would make them collide.
//
// This package sits below the provider seam and must never import it: provider
// consumes [Store] through its Deps, so an import in this direction is a cycle.
package jobs

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

// Namespace defaults and bounds.
const (
	// DefaultNamespace is the state lane a record with no namespace belongs to.
	// It is a real lane, not a null one: the single-test case is simply the case
	// where every request shares one lane.
	//
	// provider and journal declare the same constant. The duplication is
	// deliberate for the reason given in the package comment — this package sits
	// below the provider seam and must never import it.
	DefaultNamespace = "default"

	// DefaultMaxJobs bounds one namespace's live jobs when [Limits] leaves
	// MaxJobs zero.
	//
	// 256 is chosen against the namespace bound rather than on its own: at
	// provider.DefaultMaxNamespaces (1024) the worst case is 262144 records of
	// roughly 150 bytes, about 40 MiB. That is a ceiling a shared container
	// survives, and it is reached only by a suite that creates 256 jobs in each
	// of 1024 namespaces and never resets.
	DefaultMaxJobs = 256

	// HighWaterPercent is the occupancy at which [Stats.Near] starts reporting
	// true, so a caller can warn before the bound is reached rather than only
	// when a create is refused.
	//
	// A suite creeping toward the bound over weeks otherwise gets no signal at
	// all until the create that fails, and by then the failing request is the
	// one AFTER the one that filled the namespace.
	HighWaterPercent = 80
)

// Errors returned by [Registry.Create]. Both are sentinel values so a caller can
// distinguish them with errors.Is and render the provider-shaped body its own
// vendor uses.
var (
	// ErrLimit reports that the namespace already holds MaxJobs live records.
	// The create is refused; nothing is evicted to make room.
	ErrLimit = errors.New("jobs: namespace is at its job bound")

	// ErrDuplicate reports that the identifier is already live in this
	// namespace. It means a create re-minted an identifier that a previous
	// create still holds, which in practice means the fault cursors were reset
	// without the job records being reset alongside them.
	ErrDuplicate = errors.New("jobs: identifier is already live in this namespace")
)

// Job is one create-then-poll record: what a create minted, and what a later
// poll in the same namespace resolves.
//
// It holds no response body. A poll re-renders from the scenario rather than
// replaying a stored copy, so nothing here can drift from what the scenario
// says, and a record stays small enough that the bound above is about
// bookkeeping rather than memory.
type Job struct {
	// ID is the identifier the create returned and a poll presents. It is unique
	// within a namespace and deliberately NOT unique across them.
	ID string

	// Namespace is the state lane this record belongs to. Empty means
	// [DefaultNamespace]; [Registry] normalises it on the way in so a caller
	// cannot create a record it can never look up.
	Namespace string

	// Entry is the scenario provider entry that minted this job, for example
	// "exa_agent_runs". A poll resolves its turns from the same entry.
	Entry string

	// LaneKey is the turn lane the create was served in, without its namespace
	// prefix. It is what the identifier was derived from, and it is retained so
	// a poll can be answered from the same lane the create used.
	LaneKey string

	// CreateIndex is the call index the create claimed. It is part of the
	// identifier derivation, so it is retained to make a record self-describing
	// when read back from an admin listing.
	CreateIndex int

	// CreatedAt is stamped from the injectable clock. It is the one real-time
	// field a record carries and it is NEVER rendered into a response body —
	// same rule as journal.Entry.ArrivedAt. A response that carried it would
	// stop being byte-identical between runs, which is the property this whole
	// simulator exists to provide.
	CreatedAt time.Time
}

// Stats reports one namespace's occupancy against its bound.
type Stats struct {
	// Count is the number of live records in the namespace.
	Count int

	// Bound is the effective MaxJobs, after defaults are applied.
	Bound int
}

// Near reports whether the namespace has reached [HighWaterPercent] of its
// bound, which is a caller's cue to warn while creates still succeed.
//
// It is false for a zero Stats, so a caller that asks about a namespace holding
// nothing does not warn about it.
func (s Stats) Near() bool {
	return s.Bound > 0 && s.Count*100 >= s.Bound*HighWaterPercent
}

// Full reports whether the namespace is at its bound, so the next create will be
// refused with [ErrLimit].
func (s Stats) Full() bool {
	return s.Bound > 0 && s.Count >= s.Bound
}

// Store is the job state the provider seam consumes. It is an interface for the
// same reason journal.Journal is: a consumer may substitute its own, and the
// zero-dependency default must not force one.
//
// Implementations must be safe for concurrent use. Every method takes the
// namespace explicitly rather than reading it from ambient state, because the
// namespace is resolved once per request by provider.Handle and passing it is
// what keeps this package from needing to know how that resolution works.
type Store interface {
	// Create records j and reports the namespace's occupancy afterwards.
	//
	// It returns [ErrLimit] when the namespace is at its bound and
	// [ErrDuplicate] when the identifier is already live there. In both cases
	// nothing is recorded, and the returned Stats still describes the namespace
	// so a caller can report how full it is.
	Create(j Job) (Stats, error)

	// Lookup returns the record for id in namespace, and whether one exists.
	Lookup(namespace, id string) (Job, bool)

	// StatsIn reports one namespace's occupancy against its bound.
	StatsIn(namespace string) Stats

	// ResetIn drops one namespace's records and returns its slots to the bound.
	//
	// It must be called by every surface that resets fault counters, in the same
	// call: the turn cursor and the identifier derivation share the call index,
	// so cursors reset without records dropped means the next create re-mints a
	// live identifier and fails with [ErrDuplicate].
	ResetIn(namespace string)

	// Reset drops every namespace's records.
	Reset()
}

// Limits bounds what a [Registry] holds.
//
// MaxJobs is PER NAMESPACE, so the total ceiling is bounded by the namespace
// count times MaxJobs — the same product shape journal.Limits documents. There
// is no namespace bound here on purpose; see [Registry].
type Limits struct {
	// MaxJobs is how many live jobs one namespace may hold. Zero or negative
	// means [DefaultMaxJobs].
	MaxJobs int
}

// normalized returns l with every out-of-range field replaced by its documented
// default.
func (l Limits) normalized() Limits {
	if l.MaxJobs <= 0 {
		l.MaxJobs = DefaultMaxJobs
	}
	return l
}

// Registry is the in-process [Store].
//
// # The bound refuses rather than evicting
//
// journal.Ring evicts its oldest entry when a lane fills. This does not, and the
// difference is not an inconsistency: the two stores lose different things. An
// evicted journal entry costs observability — the request still happened and was
// still served correctly. An evicted job record costs correctness: a poll for a
// job the client successfully created returns the vendor's 404, with no finding,
// intermittently, depending on how many jobs a co-tenant test happened to create
// first. That is indistinguishable from the multi-replica divergence this
// simulator documents at length, manufactured locally.
//
// A create refused at the boundary is loud, attributable to the request that hit
// the wall, and carries its own remedy. That is the better failure by a wide
// margin.
//
// # It does not admit namespaces
//
// provider.Handle asks the fault engine's NamespaceAdmitter before any handler
// runs, so a job can only be created in a namespace that was admitted a few
// lines earlier. Adding a third admission authority would mean three stores that
// must agree about exactly which namespaces are live, and making two agree
// already takes a careful paragraph in journal.Ring.
type Registry struct {
	mu     sync.RWMutex
	limits Limits

	// jobs is namespace -> id -> record. The outer map is not bounded here; see
	// the type comment on why admission lives upstream.
	jobs map[string]map[string]Job
}

// NewRegistry returns a Registry bounded by l, with every out-of-range field
// replaced by its documented default.
func NewRegistry(l Limits) *Registry {
	return &Registry{
		limits: l.normalized(),
		jobs:   make(map[string]map[string]Job),
	}
}

// namespaceOf normalises a namespace name, mapping the empty name to
// [DefaultNamespace]. A record created with no namespace must be findable by a
// poll that also names none, so both directions go through this.
func namespaceOf(namespace string) string {
	if namespace == "" {
		return DefaultNamespace
	}
	return namespace
}

// Create records j and reports the namespace's occupancy afterwards.
//
// The duplicate check runs before the bound check, so a re-minted identifier is
// reported as the duplicate it is rather than as a full namespace. The two have
// different causes and different remedies, and reporting the wrong one sends the
// reader to the wrong place.
func (r *Registry) Create(j Job) (Stats, error) {
	ns := namespaceOf(j.Namespace)
	j.Namespace = ns

	r.mu.Lock()
	defer r.mu.Unlock()

	live := r.jobs[ns]
	stats := Stats{Count: len(live), Bound: r.limits.MaxJobs}

	if _, exists := live[j.ID]; exists {
		return stats, ErrDuplicate
	}
	if stats.Full() {
		return stats, ErrLimit
	}

	if live == nil {
		live = make(map[string]Job)
		r.jobs[ns] = live
	}
	live[j.ID] = j

	stats.Count = len(live)
	return stats, nil
}

// Lookup returns the record for id in namespace, and whether one exists.
func (r *Registry) Lookup(namespace, id string) (Job, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	j, ok := r.jobs[namespaceOf(namespace)][id]
	return j, ok
}

// StatsIn reports one namespace's occupancy against its bound. A namespace
// holding nothing reports a zero count and the real bound, so a caller can
// always render "n of m".
func (r *Registry) StatsIn(namespace string) Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return Stats{Count: len(r.jobs[namespaceOf(namespace)]), Bound: r.limits.MaxJobs}
}

// ResetIn drops one namespace's records, returning its slots to the bound and
// leaving every other namespace untouched.
func (r *Registry) ResetIn(namespace string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.jobs, namespaceOf(namespace))
}

// Reset drops every namespace's records.
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	clear(r.jobs)
}

// List returns every live record across every namespace, in the declared total
// order: namespace ascending, then entry, then create index, then id. Every
// field compared is a string or an int, and (namespace, id) is unique, so the
// order is total — Go's map iteration must never reach a caller through this
// method (CLAUDE.md house rule 2).
//
// It is not part of [Store]: a consumer's own implementation is not obliged to
// support enumeration, and the admin surface asserts for this method as an
// optional capability the same way it asserts for a namespace-scoped fault
// reset. It returns a snapshot copy, so a caller mutating the slice or the
// registry mutating afterwards cannot affect the other.
func (r *Registry) List() []Job {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Job, 0)
	for _, live := range r.jobs {
		for _, j := range live {
			out = append(out, j)
		}
	}
	slices.SortFunc(out, func(a, b Job) int {
		if c := strings.Compare(a.Namespace, b.Namespace); c != 0 {
			return c
		}
		if c := strings.Compare(a.Entry, b.Entry); c != 0 {
			return c
		}
		if c := cmp.Compare(a.CreateIndex, b.CreateIndex); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}
