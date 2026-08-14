package journal

import (
	"bytes"
	"encoding/json"
	"sync"
	"sync/atomic"
)

// Ring is a bounded, concurrency-safe Journal. When full it drops the oldest
// entry and increments Stats.Dropped.
type Ring struct {
	seq atomic.Uint64

	mu      sync.RWMutex
	entries []Entry
	// head is the index of the oldest stored entry. It only ever leaves zero once
	// entries has reached capacity, so a partially filled ring reads in index
	// order and a full one reads from head.
	head     int
	appended uint64
	dropped  uint64

	capacity     int
	maxBodyBytes int
}

// NewRing returns a Ring retaining at most capacity entries and at most
// maxBodyBytes of body per entry. A capacity of zero (or negative — a direct
// library caller must not be able to panic make) stores nothing while still
// allocating sequence numbers, so ordering assertions keep working with
// retention switched off. It returns a concrete *Ring: return structs, accept
// interfaces. Callers assign it to a Journal field.
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
	if capacity < 0 {
		capacity = 0
	}
	if maxBodyBytes < 0 {
		maxBodyBytes = 0
	}
	return &Ring{
		entries:      make([]Entry, 0, capacity),
		capacity:     capacity,
		maxBodyBytes: maxBodyBytes,
	}
}

// NewDiscard returns a Journal that allocates sequence numbers and stores
// nothing. Each call returns a *fresh* instance with its own atomic counter.
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

// Next claims the next arrival-ordered sequence number. The first call after
// construction or Reset returns 1, so a zero Entry.Seq means "never journaled".
func (r *Ring) Next() uint64 {
	return r.seq.Add(1)
}

// Append stores a completed entry, redacting it and only then bounding it.
//
// The order is normative. Clipping first leaves a prefix that is no longer
// valid JSON, so redact.JSONBytes falls to its non-JSON branch and the document
// is masked by the weaker text matcher instead of the structural one — and a
// credential straddling the cut can be left half-stored. Clipping an
// already-redacted document can only drop trailing bytes, never expose a masked
// one.
//
// Append retains nothing the caller can still reach: Redact rebuilds the header
// map and the findings slice, and the body is copied. It cannot, however,
// redact the caller's own Entry — the parameter is a value — so a caller that
// logs or serialises its copy must call Redact itself first.
func (r *Ring) Append(e Entry) {
	if r.capacity == 0 {
		// Nothing is retained, so there is nothing to redact and no reason to pay
		// for a body walk on every request when retention is switched off.
		r.mu.Lock()
		r.appended++
		r.dropped++
		r.mu.Unlock()
		return
	}

	stored := bound(Redact(e), r.maxBodyBytes)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.appended++
	if len(r.entries) < r.capacity {
		r.entries = append(r.entries, stored)
		return
	}
	r.entries[r.head] = stored
	r.head = (r.head + 1) % r.capacity
	r.dropped++
}

// Snapshot returns a deep copy of the stored entries in append order, oldest
// first.
func (r *Ring) Snapshot() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stored := len(r.entries)
	out := make([]Entry, 0, stored)
	for i := range stored {
		// head is zero until the ring is full, so this indexing is correct in both
		// states.
		out = append(out, cloneEntry(r.entries[(r.head+i)%stored]))
	}
	return out
}

// Reset clears stored entries, zeroes the retention counters and returns the
// sequence counter to zero.
func (r *Ring) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Drop the references rather than only the length: a retained entry keeps its
	// header map and body alive for as long as the ring lives otherwise.
	r.entries = make([]Entry, 0, r.capacity)
	r.head = 0
	r.appended = 0
	r.dropped = 0
	r.seq.Store(0)
}

// Stats reports retention counters.
func (r *Ring) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return Stats{
		Capacity: r.capacity,
		Stored:   len(r.entries),
		Appended: r.appended,
		Dropped:  r.dropped,
	}
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
