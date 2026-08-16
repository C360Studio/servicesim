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

// The simulated grammars.
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
	// the chat-completions grammar (GrammarDelta); GrammarTyped's renderer
	// (renderAgentStream, provider/perplexity/agent.go) sets it on every
	// frame.
	Name string

	// Data is written verbatim after "data: ". It is normally compact JSON,
	// and is []byte rather than a struct because the [DONE] sentinel is a
	// bare token and not a JSON value at all.
	Data []byte

	// Pace is the minimum wall time between the previous frame reaching the
	// wire and this one starting, resolved from the scenario's `pace:` key
	// (StreamScript.Pace, a per-delta or per-terminal override) by whichever
	// provider renderer built this event.
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

	// DonePace is the gap before the [DONE] sentinel on GrammarDelta. [DONE]
	// is never an indexed chunk (see Chunks' doc comment), so it has no
	// per-frame StreamChunk.Pace of its own to carry one; the renderer sets
	// this from the script's own default pace (scenario.StreamScript.Pace),
	// which is the rule docs/design/streaming.md §4.3 states explicitly.
	// Shipped as (Phase 5 unit 2): §3.2's illustrative Stream struct predates
	// per-chunk pacing entirely and has no field for this; it is added here
	// for the same reason OmitDone was — nowhere else in this grammar- and
	// provider-blind package could carry a Perplexity-scripted value.
	DonePace time.Duration
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

// streamPlan is the resolved, pure description of how executeStream will
// write a Stream: which chunk (if any) a stream_disconnect aborts before,
// which chunk (if any) a stream_truncate_chunk writes a partial frame for
// before aborting, each chunk's actual pace (folding in a stream_stall's
// extra Delay at its own index), and the [DONE] sentinel's own gap. It is
// computed once, before the first byte, from the stream's own baked-in
// per-chunk paces and the claimed attempt, so executeStream's loop never has
// to re-derive a fault decision itself — the same reason faultOutcome
// computes the journal's planned half up front rather than letting execute
// discover it mid-write.
//
// disconnectAt and truncateAt are never both >= 0: a claimed attempt is
// exactly one kind, so at most one of the two aborting behaviours below is
// active for a given plan.
type streamPlan struct {
	chunks   []StreamChunk
	donePace time.Duration

	// stallAt is the chunk index stream_stall's extra Delay is folded into;
	// -1 means no stall is scripted.
	stallAt    int
	stallExtra time.Duration

	// disconnectAt is the chunk index stream_disconnect aborts BEFORE
	// writing: chunks at every earlier index are written whole, and the
	// chunk AT this index is never written at all — the previous chunk (or
	// the flushed headers, if this is 0) is the last thing the client
	// observes before the connection dies, which is what makes this the
	// frame-boundary-clean sibling of truncateAt below. -1 means no
	// disconnect is scripted.
	disconnectAt int

	// truncateAt is the chunk index stream_truncate_chunk writes
	// truncateBytes of before aborting — a malformed partial frame, not a
	// clean boundary. -1 means no truncation is scripted.
	truncateAt    int
	truncateBytes int

	// reset selects RST over a clean FIN for whichever of disconnectAt or
	// truncateAt is active. Shared because only one is ever active per plan.
	reset bool
}

// planStream resolves a's effect on s into a streamPlan. a may be nil (no
// fault claimed this attempt) or any non-stream_* kind (a trailing "kind:
// none" attempt that only carries headers:, for instance) — planStream
// leaves every abort/stall field at its "nothing scripted" zero value in
// both cases, since only the three stream_* kinds ever populate them.
func planStream(a *scenario.FaultAttempt, s *Stream) streamPlan {
	p := streamPlan{chunks: s.Chunks, donePace: s.DonePace, stallAt: -1, disconnectAt: -1, truncateAt: -1}
	if a == nil {
		return p
	}
	switch a.EffectiveKind() {
	case scenario.FaultStreamStall:
		p.stallAt = a.AfterChunk
		p.stallExtra = a.Delay.Duration()
	case scenario.FaultStreamDisconnect:
		p.disconnectAt = a.AfterChunk
		p.reset = a.Reset
	case scenario.FaultStreamTruncateChunk:
		p.truncateAt = a.AfterChunk
		p.reset = a.Reset
		if a.AfterChunk >= 0 && a.AfterChunk < len(s.Chunks) {
			p.truncateBytes = truncationLenForChunk(a, len(s.Chunks[a.AfterChunk].Bytes))
		}
	}
	return p
}

// paceOf returns the planned gap before writing chunk i, the script's own
// pace plus a stream_stall's extra Delay when i is the stalled index.
func (p streamPlan) paceOf(i int) time.Duration {
	d := p.chunks[i].Pace
	if i == p.stallAt {
		d += p.stallExtra
	}
	return d
}

// paceMS renders every indexed chunk's planned gap in milliseconds, in chunk
// order, for journal.StreamOutcome.PaceMS. It is the PLANNED schedule —
// nothing here reads a clock — so it is identical under DelayReal and
// DelaySkip alike; see docs/design/streaming.md §6.3 and §5.3's
// AssertStreamPacing, which reads this same plan rather than a wall-clock
// measurement.
func (p streamPlan) paceMS() []int64 {
	out := make([]int64, len(p.chunks))
	for i := range p.chunks {
		out[i] = p.paceOf(i).Milliseconds()
	}
	return out
}

// eventNames renders each chunk's "event:" value in order, for
// journal.StreamOutcome.EventNames. It returns nil, not an empty slice, when
// every chunk is unnamed (GrammarDelta), so a reader can tell the two
// grammars apart by the field's presence alone rather than by comparing an
// empty slice against nil.
func (p streamPlan) eventNames() []string {
	named := false
	for _, c := range p.chunks {
		if c.Name != "" {
			named = true
			break
		}
	}
	if !named {
		return nil
	}
	out := make([]string, len(p.chunks))
	for i, c := range p.chunks {
		out[i] = c.Name
	}
	return out
}

// truncationLenForChunk is stream_truncate_chunk's per-chunk analogue of
// fault_exec.go's truncationLen: TruncateAfterBytes unset (zero) means half
// of THIS chunk's bytes, clamped to the chunk's own length — the rule
// reused, not the number, since the whole body and one chunk are different
// lengths. Reusing the field name without reusing this rule would make the
// same YAML key mean two different halves depending on which fault kind it
// appeared on.
func truncationLenForChunk(a *scenario.FaultAttempt, chunkBytes int) int {
	n := a.TruncateAfterBytes
	if n <= 0 {
		n = chunkBytes / 2
	}
	return min(n, chunkBytes)
}

// executeStream writes resp.Stream to w: headers and one flush, then each
// chunk in turn, then — on GrammarDelta, unless the script asked to omit it —
// the [DONE] sentinel. out already carries the PLANNED half of the journal
// outcome (built by Handle before the first byte); this returns it with the
// OBSERVED half filled in, and amends the already-appended journal entry
// through closer along the way.
//
// It never decides whether an attempt applies: suppression is decided once,
// in Handle, before this is ever reached (see suppressStream). What IS
// decided here, through planStream, is how a stream_* attempt that DOES
// apply affects the write loop — a stall's extra pace, a disconnect's abort
// point, a truncation's partial frame — since that decision has nowhere else
// to live: it depends on the stream's own chunk boundaries, which only this
// function is about to walk.
//
// a is the same (post-mismatch, possibly nil) attempt execute already holds.
// A non-suppressing, non-stream_* attempt still reaches here — e.g. a
// trailing "kind: none" entry that carries only headers:/retry_after — and
// its declared headers, Retry-After and status override must reach the wire
// exactly as they would on the non-streaming path, not vanish because this
// path forgot to look at a. docs/design/streaming.md §4.3 is explicit that
// the real call is applyHeader(w, faultHeader(a, resp)), never resp.Header
// alone.
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
	plan := planStream(a, stream)
	written, sent := 0, 0
	for i, c := range plan.chunks {
		if plan.disconnectAt == i {
			// The connection dies here, before anything of chunk i reaches
			// the wire: chunk i-1 (or the flushed headers, if i == 0) is the
			// last thing the client observes, a clean frame boundary rather
			// than a malformed one — see streamPlan.disconnectAt's doc
			// comment for why that distinction is the point of this kind.
			out = closeWith(out, closer, written, sent, journal.StreamAborted)
			if plan.reset {
				hijackReset(w)
			}
			// A plain return would let net/http send the terminating
			// zero-length chunk and the client would see a clean EOF — a
			// complete stream, not a disconnect. The panic is what produces
			// io.ErrUnexpectedEOF or, with reset above, a connection-reset
			// error (verified against Go 1.26.4; docs/design/streaming.md §1).
			panic(http.ErrAbortHandler)
		}

		if err := sleep(ctx, plan.paceOf(i), mode); err != nil {
			// The client's own deadline or cancellation ended the request
			// mid-stream. Nothing more is written; the journal says how far
			// we got.
			return closeWith(out, closer, written, sent, journal.StreamClientGone)
		}

		b := c.Bytes
		truncating := plan.truncateAt == i
		if truncating {
			b = b[:plan.truncateBytes]
		}
		n, err := w.Write(b)
		written += n
		// Without a flush per frame the bytes sit in net/http's buffer, and a
		// client watching for each chunk would see nothing until the buffer
		// fills — the same reason truncateBody flushes today.
		_ = rc.Flush()
		if err != nil {
			return closeWith(out, closer, written, sent, journal.StreamClientGone)
		}
		if truncating {
			// The partial write above IS the malformed frame stream_truncate_chunk
			// scripts; nothing else about chunk i reaches the client, and it is
			// never counted as sent — ChunksSent counts complete indexed chunks
			// the client received, and this one was not.
			out = closeWith(out, closer, written, sent, journal.StreamAborted)
			if plan.reset {
				hijackReset(w)
			}
			panic(http.ErrAbortHandler)
		}
		sent++
	}

	if stream.Grammar == GrammarDelta && !stream.OmitDone {
		if err := sleep(ctx, plan.donePace, mode); err != nil {
			return closeWith(out, closer, written, sent, journal.StreamClientGone)
		}
		done := doneChunk()
		n, err := w.Write(done.Bytes)
		written += n
		_ = rc.Flush()
		if err != nil {
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
