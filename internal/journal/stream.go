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

	// ChunkCount is len(provider.Stream.Chunks) — how many indexed chunks
	// this grammar's own renderer produced, not a fixed formula: N+1 for
	// GrammarDelta (the N scripted deltas plus the one terminal chunk), N+5
	// for GrammarTyped (the same N deltas plus its five envelope events), or
	// fewer still for a GrammarTyped turn whose message item never renders
	// at all (a failed or cancelled status). The [DONE] sentinel, when
	// written, is never one of them.
	ChunkCount int `json:"chunk_count"`

	// BytesPlanned is the total bytes the plan will write, [DONE] included.
	BytesPlanned int `json:"bytes_planned"`

	// PaceMS is the PLANNED gap before each indexed chunk, in the same order
	// as Stream.Chunks — the scenario's scripted pace, folding in a
	// stream_stall's extra Delay at its AfterChunk index (so PaceMS[N]
	// already includes the stall; see StallBeforeMS below). It is the
	// schedule, not a measurement: nothing here reads the wall clock, so
	// this is stable under both provider.DelayReal and provider.DelaySkip
	// and safe to read before the exchange closes. len(PaceMS) ==
	// ChunkCount; [DONE], never being indexed, is not one of them.
	PaceMS []int64 `json:"pace_ms,omitempty"`

	// EventNames is each frame's "event:" value, in the same order as
	// Stream.Chunks. It is empty for GrammarDelta, whose frames carry no
	// "event:" line at all — which is how a reader tells the two grammars
	// apart without parsing a byte. len(EventNames) == ChunkCount when set.
	EventNames []string `json:"event_names,omitempty"`

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

	// AbortAfterChunk and TruncatedAtByte record the SCRIPTED fault, not what
	// happened: both are nil when the script aborts nothing. AbortAfterChunk
	// is set for either aborting stream_* kind (stream_disconnect and
	// stream_truncate_chunk); TruncatedAtByte is set only for the latter,
	// alongside it. A claimed attempt that could not apply to this exchange
	// (provider.Handle's abort_unreachable mismatch) leaves both nil, exactly
	// as it leaves Outcome.Aborted false — the attempt is named in
	// Outcome.FaultKind but never affected the plan these fields describe.
	AbortAfterChunk *int `json:"abort_after_chunk,omitempty"`
	TruncatedAtByte *int `json:"truncated_at_byte,omitempty"`

	// StallBeforeMS is a stream_stall's extra Delay, lifted back out as its
	// own field so a reader does not have to know which PaceMS index carries
	// a fold-in to recover it. Nil when no stall is scripted.
	StallBeforeMS *int64 `json:"stall_before_ms,omitempty"`

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
