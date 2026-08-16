package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/scenario"
)

// SSEGrammar names the Server-Sent Events dialect a stream is written in. The
// two in play differ in whether frames are named; see
// docs/design/streaming.md §7.
type SSEGrammar string

// The simulated grammars. GrammarTyped is declared for §3.2's completeness —
// the [SSEGrammar] enum this build's transport layer understands — but no
// provider package renders it yet; that is a later unit (Agent API SSE).
const (
	// GrammarDelta is the OpenAI-compatible chat-completions dialect: unnamed
	// frames whose payload is a chat.completion.chunk object, closed by the
	// bare token [DONE].
	GrammarDelta SSEGrammar = "chat_completions"

	// GrammarTyped is the Responses/Agent dialect: every frame carries an
	// "event:" line naming one of the published EventType members, and the
	// payload repeats the name in its "type" property.
	GrammarTyped SSEGrammar = "responses"
)

// SSEEvent is one frame before encoding. Provider packages build these; the
// encoder below turns them into bytes.
type SSEEvent struct {
	// Name fills the "event:" line. Empty omits the line entirely, which is
	// the chat-completions grammar. Built by [EncodeSSE] but not yet used by
	// any renderer this unit ships — GrammarTyped is a later unit.
	Name string

	// Data is written verbatim after "data: ". It is normally compact JSON,
	// and is []byte rather than a struct because the [DONE] sentinel is a
	// bare token and not a JSON value at all.
	Data []byte

	// Pace is the minimum wall time between the previous frame reaching the
	// wire and this one starting. No renderer this unit ships sets it above
	// zero — the scenario grammar has no `pace:` key yet — but the field and
	// the sleep it drives through [executeStream] are real: honouring a
	// future nonzero value needs no further plumbing, only a source for one.
	Pace time.Duration

	// Terminal marks the frame carrying usage and cost. Exactly zero or one
	// frame in a sequence may set it; [EncodeSSE] panics on a second,
	// because a stream with two terminal chunks is a fixture bug that would
	// otherwise surface as a consumer double-counting spend.
	Terminal bool
}

// StreamChunk is one fully encoded frame. Bytes are final: the plan is
// complete before the first byte is written, which is what makes the journal
// safe to read as soon as the client has seen anything.
type StreamChunk struct {
	Bytes []byte
	Pace  time.Duration

	// Name is the "event:" value, for the journal. Empty on GrammarDelta.
	Name string

	Terminal bool
}

// Stream is a fully rendered SSE response.
type Stream struct {
	Grammar SSEGrammar

	// Chunks holds exactly the indexed sequence: the N delta chunks and the
	// one terminal chunk, len(Chunks) == N+1. It never holds the [DONE]
	// sentinel — see [Stream.Bytes] and [OmitDone] — which is what lets
	// ChunkCount, TerminalIndex and an abort loop's index all range over
	// [0, len(Chunks)) with no separate accounting anywhere.
	Chunks []StreamChunk

	// Usage is the terminal chunk's usage object, verbatim, lifted out so
	// the journal can carry it without re-parsing a frame. Nil when the
	// script omits usage.
	Usage json.RawMessage

	// CostTotal is the total the terminal chunk declares, lifted from
	// whichever vendor field carries it. Usage remains the authority; this
	// is a convenience and is nil when the script omits usage.
	CostTotal *float64

	// OmitDone drops the terminating "data: [DONE]" sentinel that
	// [executeStream] would otherwise write after Chunks on GrammarDelta.
	//
	// docs/design/streaming.md §3.2's illustrative Stream struct lists only
	// Grammar/Chunks/Usage/CostTotal, with nowhere for a scripted
	// terminal.omit_done to live: executeStream is grammar- and
	// provider-blind (it lives in this package, not in provider/perplexity),
	// so it cannot reach into a Perplexity-specific StreamTerminal to find
	// this bit. This field is the corrected shape — noted here per the
	// design's own instruction that shipped code, once it disagrees with an
	// illustrative block, wins and the design should say so.
	OmitDone bool
}

// Bytes returns the total the plan will write, which is known before the
// first write and is what StreamOutcome.BytesPlanned records. It answers "how
// many bytes will the wire carry", which is allowed to disagree with
// ChunkCount ("how many chunks are there") by design: [DONE] counts toward
// the former and is never one of the latter.
func (s *Stream) Bytes() int {
	n := 0
	for _, c := range s.Chunks {
		n += len(c.Bytes)
	}
	if s.Grammar == GrammarDelta && !s.OmitDone {
		n += len(doneChunk().Bytes)
	}
	return n
}

// EncodeSSE encodes events into chunks. Framing is fixed and deterministic:
// an optional "event: <name>\n" line, then one "data: " line per line of Data
// (payloads are compact JSON and contain none, but the SSE grammar requires
// the split and a payload that grows a newline must not silently split a
// frame), then one blank line. Nothing here reads a clock or a map.
func EncodeSSE(events []SSEEvent) []StreamChunk {
	chunks := make([]StreamChunk, 0, len(events))
	terminalSeen := false
	for _, e := range events {
		if e.Terminal {
			if terminalSeen {
				panic("provider: EncodeSSE: a stream may declare at most one terminal frame")
			}
			terminalSeen = true
		}
		chunks = append(chunks, StreamChunk{
			Bytes:    encodeFrame(e.Name, e.Data),
			Pace:     e.Pace,
			Name:     e.Name,
			Terminal: e.Terminal,
		})
	}
	return chunks
}

// encodeFrame renders one SSE frame's bytes: an optional "event:" line, one
// "data:" line per line of data, then a blank line.
func encodeFrame(name string, data []byte) []byte {
	var buf bytes.Buffer
	if name != "" {
		buf.WriteString("event: ")
		buf.WriteString(name)
		buf.WriteByte('\n')
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		buf.WriteString("data: ")
		buf.Write(line)
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

// doneChunk is the encoded "data: [DONE]\n\n" sentinel frame written after
// every GrammarDelta stream unless [Stream.OmitDone] is set. It is never a
// Stream.Chunks element (see that field's doc comment) but reuses the same
// framing rule, which is why it goes through [EncodeSSE] rather than a
// hand-written literal that could drift from real frames.
func doneChunk() StreamChunk {
	return EncodeSSE([]SSEEvent{{Data: []byte("[DONE]")}})[0]
}

// StreamHeader returns the two headers a streaming response carries:
// Content-Type: text/event-stream and Cache-Control: no-cache. Nothing else —
// no Connection (chunked transfer already keeps the connection open) and no
// X-Accel-Buffering (that header exists for an nginx reverse proxy this
// container does not run, and stating one would be a fidelity claim this
// simulator cannot back). Exported because a provider package's handler is
// what sets Response.Header when it decides to stream; execute never touches
// it itself, only whatever the handler already put there.
func StreamHeader() http.Header {
	return http.Header{
		"Content-Type":  {"text/event-stream"},
		"Cache-Control": {"no-cache"},
	}
}

// executeStream writes resp.Stream to w: headers and one flush, then each
// chunk with its pace and a flush per frame, then — on GrammarDelta, unless
// the script asked to omit it — the [DONE] sentinel. out already carries the
// PLANNED half of the journal outcome (built by Handle before the first
// byte); this returns it with the OBSERVED half filled in, and amends the
// already-appended journal entry through closer along the way.
//
// It never decides whether an attempt applies: suppression is decided once,
// in Handle, before this is ever reached (see suppressStream), and no fault
// kind that could abort a stream mid-write exists in this build yet — the
// three stream_* fault kinds and their chunk-boundary abort logic are a
// later unit. This is deliberately the happy path only: the one thing
// besides a scripted fault that can end a stream early — the client hanging
// up — is handled, because it needs no fault at all to happen.
//
// a is the same (post-mismatch, possibly nil) attempt execute already holds.
// A non-suppressing attempt still reaches here — e.g. a trailing "kind:
// none" entry that carries only headers:/retry_after, or (once a later unit
// adds them) a stream_* kind — and its declared headers, Retry-After and
// status override must reach the wire exactly as they would on the
// non-streaming path, not vanish because this path forgot to look at a.
// docs/design/streaming.md §4.3 is explicit that the real call is
// applyHeader(w, faultHeader(a, resp)), never resp.Header alone.
func executeStream(ctx context.Context, w http.ResponseWriter, a *scenario.FaultAttempt, resp Response,
	mode DelayMode, out journal.Outcome, closer func(journal.StreamClose),
) journal.Outcome {
	header, status := resp.Header, resp.Status
	if a != nil {
		// faultHeader copies resp.Header first and only overrides
		// Content-Type for content_type/wrong_content_type/invalid_json —
		// none of which a stream-eligible attempt is — so
		// text/event-stream survives the merge; see faultHeader's doc
		// comment and docs/design/streaming.md §4.3.
		header = faultHeader(a, resp)
		if a.Status > 0 {
			status = a.Status
		}
	}
	applyHeader(w, header)
	// Deliberately NO Content-Length: setting it switches net/http to
	// identity encoding and defeats chunked framing (verified against Go
	// 1.26.4; docs/design/streaming.md §1).
	w.WriteHeader(statusOr(status))
	rc := http.NewResponseController(w)
	_ = rc.Flush() // the status line and headers reach the client now

	stream := resp.Stream
	written, sent := 0, 0
	for _, c := range stream.Chunks {
		if err := sleep(ctx, c.Pace, mode); err != nil {
			// The client's own deadline or cancellation ended the request
			// mid-stream. Nothing more is written; the journal says how far
			// we got.
			return closeWith(out, closer, written, sent, journal.StreamClientGone)
		}
		n, err := w.Write(c.Bytes)
		written += n
		// Without a flush per frame the bytes sit in net/http's buffer, and a
		// client watching for each chunk would see nothing until the buffer
		// fills — the same reason truncateBody flushes today.
		_ = rc.Flush()
		if err != nil {
			return closeWith(out, closer, written, sent, journal.StreamClientGone)
		}
		sent++
	}

	if stream.Grammar == GrammarDelta && !stream.OmitDone {
		done := doneChunk()
		if err := sleep(ctx, done.Pace, mode); err == nil {
			n, err := w.Write(done.Bytes)
			written += n
			_ = rc.Flush()
			if err != nil {
				return closeWith(out, closer, written, sent, journal.StreamClientGone)
			}
		} else {
			return closeWith(out, closer, written, sent, journal.StreamClientGone)
		}
	}

	return closeWith(out, closer, written, sent, journal.StreamCompleted)
}

// closeWith amends out with the observed bytes/chunk count/state and reports
// the same facts to closer, which amends the already-appended journal entry.
// Both happen from one call so a caller cannot update one and forget the
// other.
func closeWith(out journal.Outcome, closer func(journal.StreamClose),
	written, sent int, state journal.StreamState,
) journal.Outcome {
	out.BytesWritten = written
	if out.Stream != nil {
		amended := *out.Stream
		amended.ChunksSent = sent
		amended.State = state
		out.Stream = &amended
	}
	closer(journal.StreamClose{BytesWritten: written, ChunksSent: sent, State: state})
	return out
}
