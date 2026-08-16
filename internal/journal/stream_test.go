package journal_test

import (
	"testing"
	"time"

	"github.com/c360studio/servicesim/internal/journal"
)

// discardOnly is a minimal Journal that does NOT implement StreamCloser, so
// CloseStreamIn's honest-degradation path — nothing implements the
// capability, nothing is written, and the entry stays "open" forever — has
// something to be tested against. journal.NewDiscard already returns a real
// *Ring, which DOES implement it, so it cannot stand in for this case.
type discardOnly struct{}

func (discardOnly) Next() uint64         { return 1 }
func (discardOnly) Append(journal.Entry) {}
func (discardOnly) Snapshot() []journal.Entry {
	return nil
}
func (discardOnly) Reset()               {}
func (discardOnly) Stats() journal.Stats { return journal.Stats{} }

var _ journal.Journal = discardOnly{}

func TestCloseStreamInFalseForAJournalThatDoesNotImplementIt(t *testing.T) {
	t.Parallel()

	ok := journal.CloseStreamIn(discardOnly{}, "default", 1, journal.StreamClose{State: journal.StreamCompleted})
	if ok {
		t.Fatal("CloseStreamIn = true, want false: discardOnly implements no StreamCloser capability")
	}
}

func TestCloseStreamInTrueForRing(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(4, 4096)
	r.Append(journal.Entry{
		Seq: r.Next(), Provider: "perplexity",
		Outcome: journal.Outcome{Stream: &journal.StreamOutcome{Grammar: "chat_completions", ChunkCount: 2, State: journal.StreamOpen}},
	})

	ok := journal.CloseStreamIn(r, "", 1, journal.StreamClose{
		CompletedAt: time.Unix(1700000000, 0), BytesWritten: 42, ChunksSent: 2, State: journal.StreamCompleted,
	})
	if !ok {
		t.Fatal("CloseStreamIn = false, want true: Ring implements StreamCloser")
	}

	entries := r.Snapshot()
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Outcome.BytesWritten != 42 {
		t.Errorf("Outcome.BytesWritten = %d, want 42", e.Outcome.BytesWritten)
	}
	if !e.CompletedAt.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("CompletedAt = %v, want the closed instant", e.CompletedAt)
	}
	if e.Outcome.Stream == nil {
		t.Fatal("Outcome.Stream = nil after close, want the amended planned+observed struct")
	}
	if e.Outcome.Stream.State != journal.StreamCompleted {
		t.Errorf("Outcome.Stream.State = %q, want completed", e.Outcome.Stream.State)
	}
	if e.Outcome.Stream.ChunksSent != 2 {
		t.Errorf("Outcome.Stream.ChunksSent = %d, want 2", e.Outcome.Stream.ChunksSent)
	}
	// The planned field survives the amendment untouched.
	if e.Outcome.Stream.ChunkCount != 2 {
		t.Errorf("Outcome.Stream.ChunkCount = %d, want 2 (planned fields must survive the close)", e.Outcome.Stream.ChunkCount)
	}
}

func TestRingCloseStreamIsANoopForAnUnknownSequence(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(4, 4096)
	r.Append(journal.Entry{
		Seq: r.Next(), Provider: "perplexity",
		Outcome: journal.Outcome{Stream: &journal.StreamOutcome{ChunkCount: 1, State: journal.StreamOpen}},
	})

	// Neither an unknown namespace nor an unknown sequence in a known
	// namespace amends anything or panics.
	r.CloseStream("some-other-namespace", 1, journal.StreamClose{State: journal.StreamCompleted})
	r.CloseStream("", 999, journal.StreamClose{State: journal.StreamCompleted})

	e := r.Snapshot()[0]
	if e.Outcome.Stream.State != journal.StreamOpen {
		t.Errorf("Outcome.Stream.State = %q, want open: a close for a different entry must not touch this one", e.Outcome.Stream.State)
	}
	if e.Outcome.BytesWritten != 0 {
		t.Errorf("Outcome.BytesWritten = %d, want 0", e.Outcome.BytesWritten)
	}
}

// TestRingSnapshotDeepCopiesStreamOutcome pins Snapshot's documented deep-copy
// contract for the Stream sub-struct specifically: a caller that mutates a
// snapshotted *StreamOutcome (or appends into its Usage backing array) must
// not perturb the ring's own stored entry, the same guarantee cloneEntry
// already gives every other mutable field on Entry.
func TestRingSnapshotDeepCopiesStreamOutcome(t *testing.T) {
	t.Parallel()

	cost := 0.5
	abortAt, truncAt, stallMS := 2, 12, int64(65000)
	r := journal.NewRing(4, 4096)
	r.Append(journal.Entry{
		Seq: r.Next(), Provider: "perplexity",
		Outcome: journal.Outcome{Stream: &journal.StreamOutcome{
			ChunkCount: 2, State: journal.StreamOpen, Usage: []byte(`{"a":1}`),
			PaceMS: []int64{0, 40}, EventNames: []string{"response.created", "response.completed"},
			CostTotal:       &cost,
			AbortAfterChunk: &abortAt, TruncatedAtByte: &truncAt, StallBeforeMS: &stallMS,
		}},
	})

	snap := r.Snapshot()[0]
	snap.Outcome.Stream.State = journal.StreamAborted
	snap.Outcome.Stream.Usage[2] = 'X' // corrupt the byte at "a" if it aliases
	snap.Outcome.Stream.PaceMS[1] = 999
	snap.Outcome.Stream.EventNames[1] = "corrupted"
	*snap.Outcome.Stream.CostTotal = 999
	*snap.Outcome.Stream.AbortAfterChunk = 999
	*snap.Outcome.Stream.TruncatedAtByte = 999
	*snap.Outcome.Stream.StallBeforeMS = 999

	fresh := r.Snapshot()[0]
	if fresh.Outcome.Stream.State != journal.StreamOpen {
		t.Errorf("Outcome.Stream.State = %q after mutating a snapshot, want open: Snapshot must deep-copy Stream",
			fresh.Outcome.Stream.State)
	}
	if string(fresh.Outcome.Stream.Usage) != `{"a":1}` {
		t.Errorf("Outcome.Stream.Usage = %q after mutating a snapshot's backing array, want unchanged: "+
			"Snapshot must not alias the ring's Usage bytes", fresh.Outcome.Stream.Usage)
	}
	if fresh.Outcome.Stream.PaceMS[1] != 40 {
		t.Errorf("PaceMS[1] = %d after mutating a snapshot's backing array, want unchanged (40)",
			fresh.Outcome.Stream.PaceMS[1])
	}
	if fresh.Outcome.Stream.EventNames[1] != "response.completed" {
		t.Errorf("EventNames[1] = %q after mutating a snapshot's backing array, want unchanged: "+
			"Snapshot must not alias the ring's EventNames slice", fresh.Outcome.Stream.EventNames[1])
	}
	if *fresh.Outcome.Stream.CostTotal != 0.5 {
		t.Errorf("CostTotal = %v after writing through a snapshot's pointer, want unchanged (0.5)",
			*fresh.Outcome.Stream.CostTotal)
	}
	if *fresh.Outcome.Stream.AbortAfterChunk != 2 {
		t.Errorf("AbortAfterChunk = %v after writing through a snapshot's pointer, want unchanged (2)",
			*fresh.Outcome.Stream.AbortAfterChunk)
	}
	if *fresh.Outcome.Stream.TruncatedAtByte != 12 {
		t.Errorf("TruncatedAtByte = %v after writing through a snapshot's pointer, want unchanged (12)",
			*fresh.Outcome.Stream.TruncatedAtByte)
	}
	if *fresh.Outcome.Stream.StallBeforeMS != 65000 {
		t.Errorf("StallBeforeMS = %v after writing through a snapshot's pointer, want unchanged (65000)",
			*fresh.Outcome.Stream.StallBeforeMS)
	}
}

// TestRingCloseStreamNamespaced proves CloseStream's namespace parameter on
// its positive path, not only as a no-op for an unrelated namespace
// (TestRingCloseStreamIsANoopForAnUnknownSequence covers that side): a
// stream entry appended in a named lane is amended when CloseStream is
// called with that same namespace.
func TestRingCloseStreamNamespaced(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(4, 4096)
	r.Append(journal.Entry{
		Seq: r.NextIn("tenant-a"), Namespace: "tenant-a", Provider: "perplexity",
		Outcome: journal.Outcome{Stream: &journal.StreamOutcome{ChunkCount: 1, State: journal.StreamOpen}},
	})

	r.CloseStream("tenant-a", 1, journal.StreamClose{ChunksSent: 1, State: journal.StreamCompleted})

	e := r.SnapshotIn("tenant-a")[0]
	if e.Outcome.Stream.State != journal.StreamCompleted {
		t.Errorf("Outcome.Stream.State = %q, want completed: CloseStream must reach an entry in its own namespace",
			e.Outcome.Stream.State)
	}
}

// TestRingCloseStreamDoesNotRetroactivelyChangeAnEarlierSnapshot pins the
// property CloseStream's own doc comment claims: the stored Outcome.Stream
// pointer is replaced, never mutated through, so a Snapshot taken before the
// close keeps observing the pre-close value forever — proof that a reader
// holding an earlier snapshot cannot be surprised by a later close mutating
// memory it still holds a reference into.
func TestRingCloseStreamDoesNotRetroactivelyChangeAnEarlierSnapshot(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(4, 4096)
	r.Append(journal.Entry{
		Seq: r.Next(), Provider: "perplexity",
		Outcome: journal.Outcome{Stream: &journal.StreamOutcome{ChunkCount: 3, State: journal.StreamOpen}},
	})

	before := r.Snapshot()[0]

	r.CloseStream("", 1, journal.StreamClose{ChunksSent: 3, State: journal.StreamCompleted})

	if before.Outcome.Stream.State != journal.StreamOpen {
		t.Errorf("the earlier snapshot's State changed to %q after a later close; "+
			"CloseStream must replace the pointer, never mutate through it", before.Outcome.Stream.State)
	}

	after := r.Snapshot()[0]
	if after.Outcome.Stream.State != journal.StreamCompleted {
		t.Errorf("a fresh Snapshot after the close still reports %q, want completed", after.Outcome.Stream.State)
	}
}
