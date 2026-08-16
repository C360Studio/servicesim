package testkit

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// AwaitStreamClosed blocks until the streamed entry with this sequence
// number, recorded in the default namespace, has closed — its
// outcome.stream.state has left [StreamOpen] — or fails tb after a short
// deadline, and returns the entry either way.
//
// It exists because the client observes [DONE] (or a fault-scripted abort)
// before the handler goroutine finishes amending the already-appended
// journal entry (docs/design/streaming.md §5.3): bytes_written, chunks_sent,
// state and completed_at are only safe to read after this returns. usage,
// cost_total, chunk_count, planned pacing, grammar and event names are NOT
// what this waits for — every one of those is final before the client sees
// a byte, and §5.3's own table is explicit that reading them needs no wait
// at all.
//
// [Namespace.AwaitStreamClosed] is the namespaced form. Both take only seq,
// not a namespace parameter, on the same terms as [Sim.AwaitRequests] and
// [Namespace.AwaitRequests] already do: journal.NextIn draws Seq from each
// namespace's own counter, so a sequence number alone does not identify an
// entry across namespaces, and a single function taking a namespace string
// could not say in its own failure message which lane came up short.
func (s *Sim) AwaitStreamClosed(tb testing.TB, seq uint64) Entry {
	tb.Helper()
	return awaitStreamClosed(tb, "", seq, s.Journal)
}

// AwaitStreamClosed is [Sim.AwaitStreamClosed] scoped to this namespace.
func (ns *Namespace) AwaitStreamClosed(tb testing.TB, seq uint64) Entry {
	tb.Helper()
	return awaitStreamClosed(tb, ns.name, seq, ns.Journal)
}

// awaitStreamClosed polls read for the entry carrying seq and waits for its
// stream to leave StreamOpen. scope names the namespace in a failure message
// and is empty for the default-namespace form, the same convention await (in
// server.go) already uses for AwaitRequests.
func awaitStreamClosed(tb testing.TB, scope string, seq uint64, read func() []Entry) Entry {
	tb.Helper()

	in := ""
	if scope != "" {
		in = " in namespace " + scope
	}

	deadline := time.Now().Add(awaitTimeout)
	for {
		e, found := entryWithSeq(read(), seq)
		if found && e.Outcome.Stream == nil {
			tb.Fatalf("testkit: seq %d%s is not a streamed entry: outcome.stream is nil", seq, in)
			return e
		}
		if found && e.Outcome.Stream.State != StreamOpen {
			return e
		}
		if time.Now().After(deadline) {
			state := "not yet recorded"
			if found {
				state = string(e.Outcome.Stream.State)
			}
			hint := ""
			if state == string(StreamOpen) {
				hint = "; if the Sim uses a custom Journal, it must implement journal.StreamCloser for the close to land"
			}
			tb.Fatalf("testkit: seq %d%s did not close after waiting %s (state: %s)%s",
				seq, in, awaitTimeout, state, hint)
			return e
		}
		time.Sleep(awaitPoll)
	}
}

// entryWithSeq finds the entry carrying seq among entries, if one has been
// recorded yet.
func entryWithSeq(entries []Entry, seq uint64) (Entry, bool) {
	for _, e := range entries {
		if e.Seq == seq {
			return e, true
		}
	}
	return Entry{}, false
}

// AssertStreamUsage asserts that a streamed entry's terminal chunk declared
// this usage object, decoded from outcome.stream.usage and compared against
// want semantically, through go-cmp, after round-tripping want through JSON
// so a Go struct or map literal compares against the decoded response on
// equal terms — the same reason [AssertGoldenJSON] decodes both sides
// before diffing rather than comparing bytes.
//
// Like [AssertStreamPacing], it reads a field that is final before the
// client sees a byte (docs/design/streaming.md §5.3's own table), so it
// needs no [Sim.AwaitStreamClosed] wait. A scenario that declares
// terminal.omit_usage leaves outcome.stream.usage nil; assert that shape the
// same way any other declared shape is asserted, by passing nil for want.
func AssertStreamUsage(tb testing.TB, e Entry, want any) {
	tb.Helper()

	if e.Outcome.Stream == nil {
		tb.Errorf("seq %d is not a streamed entry: outcome.stream is nil, want usage %v", e.Seq, want)
		return
	}

	var got any
	if usage := e.Outcome.Stream.Usage; usage != nil {
		if err := json.Unmarshal(usage, &got); err != nil {
			tb.Errorf("seq %d: outcome.stream.usage is not JSON: %v", e.Seq, err)
			return
		}
	}

	wantValue, err := roundTripJSON(want)
	if err != nil {
		tb.Errorf("seq %d: want does not round-trip through JSON: %v", e.Seq, err)
		return
	}

	if diff := cmp.Diff(wantValue, got); diff != "" {
		tb.Errorf("seq %d usage does not match (-want +got):\n%s", e.Seq, diff)
	}
}

// roundTripJSON encodes v and decodes it back into an any, so a Go value
// compares against a json.Unmarshal result on equal terms: cmp.Diff would
// otherwise see two different concrete types (a caller's struct vs a decoded
// map[string]any) and report a spurious mismatch even when the JSON they
// represent is identical. A nil v round-trips to a nil any, matching what a
// nil json.RawMessage decodes to above.
func roundTripJSON(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AssertStreamPacing asserts a streamed entry's PLANNED inter-chunk gaps —
// outcome.stream.pace_ms, read back as durations, one per indexed chunk in
// order — against want.
//
// It compares the PLAN, not a wall-clock measurement: the server cannot
// prove what the client observed, only what it scheduled
// (docs/design/streaming.md §5.3). That schedule is itself a declared LOWER
// bound on the real gap — §6.3's own probe measured 121-122ms of actual
// elapsed time for a scripted 120ms pace — so an exact comparison against
// PaceMS is an assertion on the ordering and lower bounds a scenario
// author actually controls, never on the epsilon a real socket adds on top.
// Nothing this reads is a clock measurement, which is what makes it
// identical under provider.DelayReal and [WithSkippedDelays] alike (§6.3): a
// 12s heartbeat scenario is free to assert on under a unit test's default
// skipped delays, and a test that must observe the real timeout still reads
// the same planned numbers here.
func AssertStreamPacing(tb testing.TB, e Entry, want ...time.Duration) {
	tb.Helper()

	if e.Outcome.Stream == nil {
		tb.Errorf("seq %d is not a streamed entry: outcome.stream is nil, want pacing %v", e.Seq, want)
		return
	}

	got := e.Outcome.Stream.PaceMS
	if len(got) != len(want) {
		tb.Errorf("seq %d recorded %d chunks of planned pacing, want %d", e.Seq, len(got), len(want))
		return
	}
	for i, w := range want {
		if g := time.Duration(got[i]) * time.Millisecond; g != w {
			tb.Errorf("seq %d chunk %d planned pace %s, want %s", e.Seq, i, g, w)
		}
	}
}
