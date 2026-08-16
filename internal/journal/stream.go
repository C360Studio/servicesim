package journal

import (
	"encoding/json"
	"time"
)

// StreamState is how far a streamed exchange got.
type StreamState string

// Stream states.
const (
	StreamOpen       StreamState = "open"        // appended, not yet closed
	StreamCompleted  StreamState = "completed"   // every scripted chunk delivered
	StreamAborted    StreamState = "aborted"     // the scenario's scripted abort fired
	StreamClientGone StreamState = "client_gone" // the client hung up or timed out first
)

// StreamOutcome is what a streamed exchange planned and then delivered. Every
// field above the "observed" line is PLANNED and is final when the entry is
// appended, before the first byte reaches the client — see
// provider.Handle's widened journal-early condition. Every field below it is
// OBSERVED and is filled in by [CloseStreamIn] once the exchange closes; see
// [StreamState] for how a reader tells the two apart without a second read.
type StreamOutcome struct {
	// Grammar names the SSE dialect, as plain text: this package sits below
	// the provider seam and must not import provider.SSEGrammar, for the
	// same reason Entry.Provider is a plain string rather than a
	// provider.Name.
	Grammar string `json:"grammar"`

	// ---- planned: final at append, never amended -------------------------

	// ChunkCount is len(provider.Stream.Chunks): the N scripted deltas plus
	// the one terminal chunk. The [DONE] sentinel, when written, is never
	// one of them.
	ChunkCount int `json:"chunk_count"`

	// BytesPlanned is the total bytes the plan will write, [DONE] included.
	BytesPlanned int `json:"bytes_planned"`

	// TerminalIndex is the chunk carrying usage and cost, or -1 when no
	// chunk is marked terminal — reserved for a future grammar this build
	// does not define; every grammar this build renders has exactly one.
	TerminalIndex int `json:"terminal_index"`

	// Usage is the terminal chunk's usage object verbatim, lifted out so a
	// consumer's spend-attribution read is final before the client has seen
	// a byte. Nil when the script omits usage.
	Usage json.RawMessage `json:"usage,omitempty"`

	// CostTotal is the same number lifted to a provider-neutral field, so a
	// cross-provider spend assertion is one field read.
	CostTotal *float64 `json:"cost_total,omitempty"`

	// ---- observed: written by CloseStreamIn -------------------------------

	State      StreamState `json:"state"`
	ChunksSent int         `json:"chunks_sent"`
}

// StreamCloser is a Journal that can complete a streamed entry it already
// holds.
//
// It is deliberately narrow rather than a general Amend(func(*Entry)). A
// general mutator could rewrite the body or the findings and would need
// re-redaction; this one can only write the observed fields of a stream,
// none of which can carry a credential.
type StreamCloser interface {
	Journal

	// CloseStream completes the entry with this sequence number in this
	// namespace. It is a no-op for an unknown sequence, which is what a
	// journal whose ring already evicted the entry does.
	CloseStream(namespace string, seq uint64, c StreamClose)
}

// StreamClose is the observed reality of a streamed exchange.
type StreamClose struct {
	CompletedAt  time.Time
	BytesWritten int
	ChunksSent   int
	State        StreamState
}

// CloseStreamIn completes a streamed entry in j, reporting whether j could.
//
// False means the journal does not implement the capability and NOTHING was
// written: the entry keeps its planned fields and state "open" forever. That
// is honest and visible, which is the only acceptable degradation — silently
// reporting a stream as completed when nothing confirmed it is the failure
// this whole mechanism exists to avoid. [Ring] implements it; a consumer's
// own Journal need not.
func CloseStreamIn(j Journal, namespace string, seq uint64, c StreamClose) bool {
	sc, ok := j.(StreamCloser)
	if !ok {
		return false
	}
	sc.CloseStream(namespace, seq, c)
	return true
}
