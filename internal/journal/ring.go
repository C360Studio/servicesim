package journal

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
)

// Namespace defaults and bounds.
const (
	// DefaultNamespace is the state lane an entry with no Namespace belongs to.
	// It is a real lane, not a null one: the single-test case is simply the case
	// where every request shares one lane.
	//
	// provider declares the same constant. The duplication is deliberate — this
	// package sits below the provider seam and must never import it, or the seam
	// and the journal form an import cycle (see [Entry.Provider]).
	DefaultNamespace = "default"

	// DefaultMaxNamespaces bounds live namespaces when [Limits] leaves
	// MaxNamespaces zero. It matches provider.DefaultMaxNamespaces, so a Ring
	// built without explicit limits retains a lane for every namespace the
	// provider seam will admit.
	DefaultMaxNamespaces = 1024
)

// Limits bounds what a Ring retains.
//
// Capacity is per namespace, so total retention is MaxNamespaces × Capacity and
// both are configurable. A single per-process capacity would mean one busy test
// evicting a quiet test's entries — the journal answers "was this my request?",
// and it cannot answer that about entries another lane pushed out.
type Limits struct {
	// Capacity is how many entries each namespace retains before its oldest is
	// evicted. Zero (or negative — a direct library caller must not be able to
	// panic make) retains nothing while still allocating sequence numbers.
	Capacity int

	// MaxBodyBytes is how many body bytes each entry retains. Zero or negative
	// retains no body bytes at all.
	MaxBodyBytes int

	// MaxNamespaces bounds live NAMED namespaces. Zero or negative means
	// [DefaultMaxNamespaces]; there is no unbounded setting, because namespaces
	// are created implicitly by the first request that names one and an
	// unbounded map keyed on client-supplied text is a growth surface with no
	// ceiling.
	//
	// [DefaultNamespace] is exempt. It is not created by a request naming a
	// namespace — it is the lane every unprefixed request has always been served
	// in — so counting it would make a bound of 1 mean "no namespaces at all",
	// and it would put this store one namespace out of step with the fault
	// engine, which exempts it for the same reason.
	MaxNamespaces int
}

// normalized returns l with every out-of-range field replaced by its documented
// value, so no request path has to check them.
func (l Limits) normalized() Limits {
	if l.Capacity < 0 {
		l.Capacity = 0
	}
	if l.MaxBodyBytes < 0 {
		l.MaxBodyBytes = 0
	}
	if l.MaxNamespaces <= 0 {
		l.MaxNamespaces = DefaultMaxNamespaces
	}
	return l
}

// Ring is a bounded, concurrency-safe [Namespaced] Journal. Each namespace gets
// its own lane: its own sequence counter, its own bounded buffer and its own
// retention counters. When a lane is full it drops its oldest entry and
// increments that lane's dropped count.
//
// Sequence numbers are per namespace and therefore start at 1 in each, which is
// the property that makes them usable. Two concurrent tests sharing one process
// draw 1, 2, 3 each; an interleaved process-wide sequence would leave every
// entry's number depending on what some unrelated test did, and "was this my
// request?" — the whole job of the journal — unanswerable.
type Ring struct {
	// order stamps every stored entry in claim order. Snapshot merges lanes on
	// it, because map iteration order must never reach output and two lanes'
	// entries genuinely interleave in time. It is not a clock: CLAUDE.md house
	// rule 2 keeps clocks off anything a replay has to reproduce, and
	// CompletedAt is real time that two entries can share exactly.
	order atomic.Uint64

	mu    sync.RWMutex
	lanes map[string]*lane

	// refusedAppends counts appends that named a namespace beyond
	// limits.MaxNamespaces. They have no lane to be counted in and are still
	// appends, so Stats counts them here rather than losing them.
	refusedAppends uint64

	limits Limits
}

// lane is one namespace's state: its sequence counter and its bounded buffer.
type lane struct {
	seq atomic.Uint64

	mu      sync.Mutex
	entries []stored
	// head is the index of the oldest stored entry. It only ever leaves zero once
	// entries has reached capacity, so a partially filled lane reads in index
	// order and a full one reads from head.
	head     int
	appended uint64
	dropped  uint64
}

// stored is a retained entry with the Ring-wide stamp that orders it against
// entries retained in other lanes.
type stored struct {
	entry Entry
	order uint64
}

// Ring is this package's only namespace-isolating Journal. Asserting the
// interface here means a signature drift fails this package's build rather than
// a caller's, where the cause is one import further away.
var _ Namespaced = (*Ring)(nil)

// Ring is also this package's only [StreamCloser] implementation, asserted
// here for the same reason as the Namespaced assertion above.
var _ StreamCloser = (*Ring)(nil)

// NewRing returns a Ring retaining at most capacity entries per namespace and at
// most maxBodyBytes of body per entry, admitting at most [DefaultMaxNamespaces]
// namespaces. It is [NewRingWithLimits] with the default namespace bound; a
// caller that configures --max-namespaces uses that instead.
//
// A capacity of zero (or negative — a direct library caller must not be able to
// panic make) stores nothing while still allocating sequence numbers, so
// ordering assertions keep working with retention switched off. It returns a
// concrete *Ring: return structs, accept interfaces. Callers assign it to a
// Journal field.
//
// maxBodyBytes is read literally and clamped the same way: zero or negative
// retains no body bytes at all, and internal/config substitutes its documented
// default before it ever reaches here. A body longer than the limit is clipped
// *after* redaction (see [Ring.Append]) and Entry.BodyTruncated is set. The
// limit bounds the bytes actually stored, with one exception: an empty JSON
// string costs two bytes, so a limit below that stores two.
//
// A retained body that is not a JSON value — a clipped document, or a body that
// never was JSON, such as the form-encoded one redact.JSONBytes masks as text —
// is stored as a JSON string holding those already-redacted bytes. Entry has to
// stay marshalable: /__admin/requests encodes the journal in one pass, and a
// json.RawMessage holding non-JSON fails the whole encode.
func NewRing(capacity, maxBodyBytes int) *Ring {
	return NewRingWithLimits(Limits{Capacity: capacity, MaxBodyBytes: maxBodyBytes})
}

// NewRingWithLimits returns a Ring bounded by l. Every field of l is clamped to
// its documented range first, so no combination of flag values can produce a
// Ring that panics or grows without bound.
func NewRingWithLimits(l Limits) *Ring {
	return &Ring{
		lanes:  make(map[string]*lane),
		limits: l.normalized(),
	}
}

// NewDiscard returns a Journal that allocates sequence numbers and stores
// nothing. Each call returns a *fresh* instance with its own counters.
//
// There is deliberately no package-level Discard value. A shared one is
// process-global mutable state in a design whose stated isolation boundary is
// the process: two Sims in parallel subtests would draw Seq from one counter,
// and Reset in one test would zero the sequence another test was mid-way
// through asserting on. That failure reproduces only under parallelism, in the
// helper the plan's Layer 2 tells consumers to use.
//
// It is a zero-capacity Ring rather than a second implementation, so "stores
// nothing" cannot drift away from "retention switched off".
func NewDiscard() Journal {
	return NewRing(0, 0)
}

// Next claims the next arrival-ordered sequence number in the default
// namespace. The first call after construction or Reset returns 1, so a zero
// Entry.Seq means "never journaled".
func (r *Ring) Next() uint64 {
	return r.NextIn(DefaultNamespace)
}

// NextIn claims the next arrival-ordered sequence number in namespace, creating
// the namespace on first use. An empty namespace is the default one.
//
// Sequences are independent: the first call in each namespace returns 1, so two
// concurrent tests see 1, 2, 3 each rather than halves of one interleaved
// sequence.
//
// It returns 0 — "never journaled" — when namespace is one the Ring will not
// admit because [Limits].MaxNamespaces is already reached. Nothing about that
// request will be retained, and a zero Seq says so rather than implying a
// sequence position the journal never held.
//
// A refused namespace does not refuse the request. The provider seam has no
// admission path of its own — naming a namespace stays free, and the bound lives
// in the stores that actually allocate state — so such a request is still served
// and still answered; it is simply not retained here, and Append counts it in the
// dropped total so Stats stays honest about the difference.
func (r *Ring) NextIn(namespace string) uint64 {
	l := r.laneFor(namespace)
	if l == nil {
		return 0
	}
	return l.seq.Add(1)
}

// Append stores a completed entry in the namespace Entry.Namespace names,
// redacting it and only then bounding it.
//
// The order is normative. Clipping first leaves a prefix that is no longer
// valid JSON, so redact.JSONBytes falls to its non-JSON branch and the document
// is masked by the weaker text matcher instead of the structural one — and a
// credential straddling the cut can be left half-stored. Clipping an
// already-redacted document can only drop trailing bytes, never expose a masked
// one. Redaction runs identically in every namespace: it is the storage
// boundary, and namespaces are a state boundary that does not move it.
//
// Append retains nothing the caller can still reach: Redact rebuilds the header
// map and the findings slice, and the body is copied. It cannot, however,
// redact the caller's own Entry — the parameter is a value — so a caller that
// logs or serialises its copy must call Redact itself first.
//
// Entry.Namespace is stored as given. An entry recorded with no namespace
// belongs to the default lane but keeps its empty field, so the admin API's
// JSON is unchanged for traffic that never used the /n/ prefix.
func (r *Ring) Append(e Entry) {
	l := r.laneFor(e.Namespace)
	if l == nil {
		// The namespace was refused. Count the append so Stats stays honest about
		// how many requests reached the journal, and retain nothing.
		r.mu.Lock()
		r.refusedAppends++
		r.mu.Unlock()
		return
	}

	if r.limits.Capacity == 0 {
		// Nothing is retained, so there is nothing to redact and no reason to pay
		// for a body walk on every request when retention is switched off.
		l.mu.Lock()
		l.appended++
		l.dropped++
		l.mu.Unlock()
		return
	}

	redacted := bound(Redact(e), r.limits.MaxBodyBytes)

	l.mu.Lock()
	defer l.mu.Unlock()

	// The stamp is claimed under the lane's lock so a lane's buffer is always in
	// stamp order. Claimed outside it, two appends to one lane could store 6
	// before 5 and Snapshot would disagree with SnapshotIn about their order.
	s := stored{entry: redacted, order: r.order.Add(1)}

	l.appended++
	if len(l.entries) < r.limits.Capacity {
		l.entries = append(l.entries, s)
		return
	}
	l.entries[l.head] = s
	l.head = (l.head + 1) % r.limits.Capacity
	l.dropped++
}

// Snapshot returns a deep copy of every namespace's stored entries in append
// order, oldest first. Entries from different namespaces interleave exactly as
// they were appended, which is what makes an unfiltered /__admin/requests read
// like the single-namespace journal it replaces.
func (r *Ring) Snapshot() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var all []stored
	for _, l := range r.lanes {
		all = append(all, l.snapshot()...)
	}
	// Stamps are unique and totally ordered, so this recovers append order from
	// however the map happened to be walked. Map iteration order never reaches
	// output (CLAUDE.md house rule 2).
	slices.SortFunc(all, func(a, b stored) int { return cmp.Compare(a.order, b.order) })

	out := make([]Entry, 0, len(all))
	for _, s := range all {
		out = append(out, s.entry)
	}
	return out
}

// SnapshotIn returns a deep copy of one namespace's stored entries in append
// order, oldest first. An empty namespace is the default one, and a namespace
// nothing was ever appended to is empty rather than an error — a test polling
// its own lane before its first request is asking a legitimate question.
func (r *Ring) SnapshotIn(namespace string) []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	l := r.lanes[laneKey(namespace)]
	if l == nil {
		return nil
	}

	held := l.snapshot()
	out := make([]Entry, 0, len(held))
	for _, s := range held {
		out = append(out, s.entry)
	}
	return out
}

// CloseStream implements [StreamCloser]. It finds the stored entry by
// namespace and Seq and amends the OBSERVED half of a streamed exchange:
// Outcome.BytesWritten, Outcome.CompletedAt, and Outcome.Stream's
// ChunksSent/State. It is a no-op for a namespace or sequence the Ring does
// not hold — a namespace it never admitted, or an entry its own capacity
// already evicted — which is the honest degradation [CloseStreamIn]
// documents: nothing is written and, if the entry still exists elsewhere,
// it keeps its planned fields and "open" state forever.
//
// The stored Outcome.Stream pointer is replaced, never mutated through: a
// [Ring.Snapshot] taken before this call already holds a *copy* of the Entry
// (see [cloneEntry]) whose Outcome.Stream still points at the pre-close
// value, so replacing the pointer here cannot retroactively change bytes a
// caller already read. Both this method and Snapshot take the lane's own
// lock, so the replacement itself is race-free.
func (r *Ring) CloseStream(namespace string, seq uint64, c StreamClose) {
	r.mu.RLock()
	l := r.lanes[laneKey(namespace)]
	r.mu.RUnlock()
	if l == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.entries {
		e := &l.entries[i].entry
		if e.Seq != seq {
			continue
		}
		if e.Outcome.Stream == nil {
			// StreamCloser's doc comment promises it "can only write the
			// observed fields of a stream" — matched here rather than
			// amending CompletedAt/BytesWritten on an entry that was never
			// a stream in the first place.
			return
		}
		e.CompletedAt = c.CompletedAt
		e.Outcome.BytesWritten = c.BytesWritten
		amended := *e.Outcome.Stream
		amended.ChunksSent = c.ChunksSent
		amended.State = c.State
		e.Outcome.Stream = &amended
		return
	}
}

// Namespaces returns the live namespace names, sorted. Sorted because map
// iteration order must not reach a caller that renders it.
func (r *Ring) Namespaces() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.lanes))
	for name := range r.lanes {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// Reset drops every namespace: stored entries, retention counters and sequence
// counters all go back to their initial state, so the next Next in any
// namespace returns 1 again.
//
// This is the "everything" reset the admin surface makes a caller ask for
// explicitly. A bare reset that silently wiped a hundred concurrent tests'
// journals is the trap the design names, which is why [Ring.ResetIn] exists.
func (r *Ring) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Drop the lanes rather than clearing them: a retained entry keeps its header
	// map and body alive for as long as the Ring lives otherwise, and a lane that
	// is gone is exactly a namespace nobody has used yet.
	//
	// A request that is mid-Append holds a pointer to a dropped lane and writes
	// there harmlessly. Reset racing live requests renumbers them either way,
	// exactly as the admin surface documents.
	r.lanes = make(map[string]*lane)
	r.refusedAppends = 0
}

// ResetIn drops one namespace's state and leaves every other namespace
// untouched. An empty namespace is the default one.
//
// The lane is removed rather than emptied, which also returns its slot to the
// [Limits].MaxNamespaces budget: a finished test's namespace is dead state, and
// a lane recreated by a later request in the same namespace is indistinguishable
// from an emptied one — its sequence starts at 1 either way.
//
// The fault engine's scoped reset frees its slot too, so the two stores go on
// admitting exactly the same set of namespaces. A store that released while the
// other held would admit a namespace here and refuse it there, and the direction
// that bites is the silent one: a live test whose entries this store drops with
// no log line anywhere.
//
// The whole-process resets do diverge — [Ring.Reset] drops every lane while the
// engine's zeroes its counters and keeps its admissions — and the divergence
// leans the safe way: this store is then the one with room to spare, so a
// namespace it admits and the engine refuses is refused loudly, at error level,
// by the engine.
func (r *Ring) ResetIn(namespace string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.lanes, laneKey(namespace))
}

// Stats reports retention across every namespace. Capacity is the per-namespace
// capacity, not the total: it is the number at which a lane starts evicting, and
// the total ceiling is that times [Limits].MaxNamespaces.
func (r *Ring) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := Stats{Capacity: r.limits.Capacity, Appended: r.refusedAppends, Dropped: r.refusedAppends}
	for _, l := range r.lanes {
		s := l.stats()
		out.Stored += s.Stored
		out.Appended += s.Appended
		out.Dropped += s.Dropped
	}
	return out
}

// StatsIn reports one namespace's retention. An empty namespace is the default
// one, and a namespace nothing was appended to reports zeroes against the
// per-namespace capacity.
func (r *Ring) StatsIn(namespace string) Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := Stats{Capacity: r.limits.Capacity}
	if l := r.lanes[laneKey(namespace)]; l != nil {
		s := l.stats()
		out.Stored, out.Appended, out.Dropped = s.Stored, s.Appended, s.Dropped
	}
	return out
}

// laneFor returns the lane for a namespace, creating it on first use, or nil
// when creating it would exceed Limits.MaxNamespaces.
//
// Namespaces are created implicitly because requiring registration would mean a
// setup call in every consumer test, which is the friction the feature removes.
// The read is optimistic and the create re-checks under the write lock, so two
// requests naming a new namespace at once share one lane rather than each
// getting a fresh sequence.
//
// Refusal never evicts an existing lane. Silent eviction would reset a running
// test's sequence mid-loop, which the design names as the single worst failure
// this feature can produce.
func (r *Ring) laneFor(namespace string) *lane {
	key := laneKey(namespace)

	r.mu.RLock()
	l := r.lanes[key]
	r.mu.RUnlock()
	if l != nil {
		return l
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if l = r.lanes[key]; l != nil {
		return l
	}
	if key != DefaultNamespace && r.namedLanesLocked() >= r.limits.MaxNamespaces {
		return nil
	}
	l = &lane{}
	r.lanes[key] = l
	return l
}

// namedLanesLocked counts the lanes that spend namespace budget. The caller
// holds r.mu.
//
// The default lane is exempt, and the fault engine exempts it for the same
// reason: it is not created by a request naming a namespace, it is the lane
// every unprefixed request has always been served in, so counting it would make
// --max-namespaces=1 mean "no namespaces at all".
//
// The two stores must agree about exactly which namespaces are admitted, not
// merely about the number. They are bounded from one --max-namespaces value but
// refuse independently, so a store that counted one lane the other did not would
// admit a namespace here and refuse it there — and the direction that matters is
// this one: the fault engine logs its refusal loudly, while a journal that
// refused alone would drop a live test's entries with no log line anywhere.
func (r *Ring) namedLanesLocked() int {
	named := len(r.lanes)
	if _, hasDefault := r.lanes[DefaultNamespace]; hasDefault {
		named--
	}
	return named
}

// laneKey maps a namespace name to the lane holding its state, treating the
// empty name as the default namespace. An entry recorded before namespaces
// existed carries no namespace and belongs to the default lane, which is the
// same rule the admin surface's filter applies.
func laneKey(namespace string) string {
	if namespace == "" {
		return DefaultNamespace
	}
	return namespace
}

// snapshot returns deep copies of the lane's entries in append order, oldest
// first. The caller holds Ring.mu; this takes the lane's own lock, and the two
// are always acquired in that order.
func (l *lane) snapshot() []stored {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]stored, 0, len(l.entries))
	for i := range l.entries {
		// head is zero until the lane is full, so this indexing is correct in both
		// states.
		s := l.entries[(l.head+i)%len(l.entries)]
		out = append(out, stored{entry: cloneEntry(s.entry), order: s.order})
	}
	return out
}

// stats returns the lane's retention counters.
func (l *lane) stats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()

	return Stats{Stored: len(l.entries), Appended: l.appended, Dropped: l.dropped}
}

// bound clips an already-redacted entry's body to maxBodyBytes and takes
// ownership of the bytes. It must never run before Redact.
//
// It also guarantees the stored body is a JSON value, which is not the same
// thing as the body having been JSON. A clipped document is not a value any
// more, and redact.JSONBytes returns masked *text* for a body that never was
// one (a form-encoded body is the common case). Either left in a
// json.RawMessage as-is would make json.Marshal fail on the entry, and
// /__admin/requests encodes the whole journal in one pass — one such body would
// take the entire endpoint down with it. So an unparseable retained body is
// stored as a JSON string holding the redacted bytes; BodyTruncated and
// BodyParseError are what tell a reader which case they are looking at.
func bound(e Entry, maxBodyBytes int) Entry {
	if len(e.Body) == 0 {
		e.Body = nil
		return e
	}

	original := len(e.Body)
	kept := e.Body
	if len(kept) > maxBodyBytes {
		kept = kept[:maxBodyBytes]
	}

	if json.Valid(kept) {
		e.Body = append(json.RawMessage(nil), kept...)
	} else {
		e.Body, kept = jsonStringWithin(kept, maxBodyBytes)
	}
	if len(kept) < original {
		e.BodyTruncated = true
	}
	return e
}

// jsonStringWithin encodes b as a JSON string that fits maxBodyBytes, shrinking
// b until it does, and returns the encoding alongside the payload that survived
// it. Quoting inflates: every `"` in the retained bytes costs two, and a cut
// mid-rune becomes a three-byte replacement character, so encoding a payload
// already clipped to the limit can overshoot it.
//
// The bytes are already redacted, so quoting them cannot reveal anything.
func jsonStringWithin(b []byte, maxBodyBytes int) (json.RawMessage, []byte) {
	for {
		encoded := jsonString(b)
		overflow := len(encoded) - maxBodyBytes
		if overflow <= 0 || len(b) == 0 {
			return encoded, b
		}
		if overflow > len(b) {
			overflow = len(b)
		}
		b = b[:len(b)-overflow]
	}
}

// jsonString encodes b as a JSON string. HTML escaping is off so an "&" in a
// query survives as itself, matching how redact re-encodes a document.
func jsonString(b []byte) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(string(b)); err != nil {
		// json.Encoder cannot fail on a string, but a silently dropped body would
		// be a worse answer than an explicitly empty one.
		return json.RawMessage(`""`)
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n"))
}
