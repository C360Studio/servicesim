package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/scenario"
)

// --- EncodeSSE / Stream --------------------------------------------------

func TestEncodeSSEFraming(t *testing.T) {
	t.Parallel()

	t.Run("unnamed frame is data-only", func(t *testing.T) {
		t.Parallel()
		chunks := EncodeSSE([]SSEEvent{{Data: []byte(`{"i":0}`)}})
		require.Len(t, chunks, 1)
		require.Equal(t, "data: {\"i\":0}\n\n", string(chunks[0].Bytes))
		require.Empty(t, chunks[0].Name)
	})

	t.Run("a named frame carries an event line first", func(t *testing.T) {
		t.Parallel()
		chunks := EncodeSSE([]SSEEvent{{Name: "response.created", Data: []byte(`{"type":"response.created"}`)}})
		require.Equal(t, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n", string(chunks[0].Bytes))
		require.Equal(t, "response.created", chunks[0].Name)
	})

	t.Run("a payload with an embedded newline splits into one data line per line", func(t *testing.T) {
		t.Parallel()
		chunks := EncodeSSE([]SSEEvent{{Data: []byte("a\nb")}})
		require.Equal(t, "data: a\ndata: b\n\n", string(chunks[0].Bytes))
	})

	t.Run("the bare DONE token is not JSON and is framed identically", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "data: [DONE]\n\n", string(doneChunk().Bytes))
	})

	t.Run("Terminal and Pace survive into the encoded chunk", func(t *testing.T) {
		t.Parallel()
		chunks := EncodeSSE([]SSEEvent{{Data: []byte("x"), Terminal: true, Pace: 5 * time.Millisecond}})
		require.True(t, chunks[0].Terminal)
		require.Equal(t, 5*time.Millisecond, chunks[0].Pace)
	})

	t.Run("a second terminal frame panics: two terminal chunks is a fixture bug", func(t *testing.T) {
		t.Parallel()
		require.Panics(t, func() {
			EncodeSSE([]SSEEvent{{Data: []byte("a"), Terminal: true}, {Data: []byte("b"), Terminal: true}})
		})
	})

	t.Run("chunk_count never includes DONE", func(t *testing.T) {
		t.Parallel()
		chunks := EncodeSSE([]SSEEvent{{Data: []byte("a")}, {Data: []byte("b"), Terminal: true}})
		require.Len(t, chunks, 2, "N deltas plus one terminal chunk is N+1, and DONE is never a Stream.Chunks element")
	})
}

func TestStreamBytes(t *testing.T) {
	t.Parallel()

	events := []SSEEvent{{Data: []byte("a")}, {Data: []byte("bb"), Terminal: true}}
	chunkBytes := 0
	for _, c := range EncodeSSE(events) {
		chunkBytes += len(c.Bytes)
	}

	t.Run("GrammarDelta counts DONE's bytes even though it is never a chunk", func(t *testing.T) {
		t.Parallel()
		s := &Stream{Grammar: GrammarDelta, Chunks: EncodeSSE(events)}
		require.Equal(t, chunkBytes+len(doneChunk().Bytes), s.Bytes())
	})

	t.Run("OmitDone drops DONE's bytes from the total", func(t *testing.T) {
		t.Parallel()
		s := &Stream{Grammar: GrammarDelta, Chunks: EncodeSSE(events), OmitDone: true}
		require.Equal(t, chunkBytes, s.Bytes())
	})

	t.Run("GrammarTyped never adds DONE's bytes", func(t *testing.T) {
		t.Parallel()
		s := &Stream{Grammar: GrammarTyped, Chunks: EncodeSSE(events)}
		require.Equal(t, chunkBytes, s.Bytes())
	})
}

func TestStreamHeader(t *testing.T) {
	t.Parallel()
	h := StreamHeader()
	require.Equal(t, "text/event-stream", h.Get("Content-Type"))
	require.Equal(t, "no-cache", h.Get("Cache-Control"))
	require.Len(t, h, 2, "exactly these two headers, nothing else — see the doc comment for why")
}

// --- Handle integration ---------------------------------------------------

// twoChunkStream is a small deterministic fixture: one delta chunk, one
// terminal chunk, [DONE] after.
func twoChunkStream() *Stream {
	events := []SSEEvent{
		{Data: []byte(`{"i":0}`)},
		{Data: []byte(`{"i":1,"usage":{}}`), Terminal: true},
	}
	return &Stream{
		Grammar:   GrammarDelta,
		Chunks:    EncodeSSE(events),
		Usage:     json.RawMessage(`{}`),
		CostTotal: func() *float64 { v := 0.5; return &v }(),
	}
}

// capturingWriter is a minimal http.ResponseWriter that proves the
// journal-before-first-byte and client-gone orderings as program invariants,
// synchronously in the same goroutine Handle runs in — no real-time sleep, no
// reader goroutine racing the server's writer, no window to lose. onWrite
// runs before each Write's payload is recorded; returning a non-nil error
// makes that Write itself fail, standing in for a client that hung up
// mid-stream exactly the way executeStream would observe a dead connection
// (a write error), not a timing stand-in for one.
type capturingWriter struct {
	header  http.Header
	status  int
	body    bytes.Buffer
	onWrite func(call int) error
	writes  int
}

func (w *capturingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *capturingWriter) WriteHeader(status int) { w.status = status }

func (w *capturingWriter) Write(b []byte) (int, error) {
	w.writes++
	if w.onWrite != nil {
		if err := w.onWrite(w.writes); err != nil {
			return 0, err
		}
	}
	return w.body.Write(b)
}

// Flush satisfies http.Flusher so http.NewResponseController(w).Flush(),
// which executeStream calls after every frame, is a no-op rather than an
// error against this fake transport.
func (w *capturingWriter) Flush() {}

// streamHandler answers with a fully rendered stream, mirroring what a real
// provider handler builds once it decides a request will stream.
func streamHandler(s *Stream) Handler {
	return func(_ *Exchange) Response {
		return Response{
			Status: http.StatusOK, Body: []byte(`{"ok":true}`), Stream: s,
			Label: "test.stream", FaultEligible: true, Header: StreamHeader(),
		}
	}
}

// TestHandleStreamsAndJournalsBeforeTheFirstByte proves the entry is
// appended, fully planned, before the first byte reaches the transport — as
// a program invariant rather than a real-time race a test happens to win.
// Handle's widened journal-early branch calls record() before execute(),
// and execute() dispatches straight to executeStream with no work in
// between, so the FIRST w.Write executeStream makes (chunk 0's bytes) can
// only run after the append. The onWrite hook below checks the journal
// synchronously, in the same goroutine Handle runs in, from inside that
// first Write call: there is no window here for a mutation that moved the
// append later to slip through undetected, real-time or otherwise.
func TestHandleStreamsAndJournalsBeforeTheFirstByte(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream()
	stream.Usage = json.RawMessage(`{}`)
	costTotal := 0.5
	stream.CostTotal = &costTotal
	j := journal.NewRing(8, 4096)
	hf := Handle(Deps{Journal: j}, Exa, testRoute, streamHandler(stream))

	var sawBeforeFirstByte bool
	w := &capturingWriter{}
	w.onWrite = func(call int) error {
		if call != 1 {
			return nil
		}
		sawBeforeFirstByte = true
		entries := j.Snapshot()
		require.Len(t, entries, 1, "the entry is journaled before the first byte, not after the stream closes")
		so := entries[0].Outcome.Stream
		require.NotNil(t, so, "Outcome.Stream is planned before the first byte")
		require.Equal(t, journal.StreamOpen, so.State)
		require.Equal(t, string(GrammarDelta), so.Grammar)
		require.Equal(t, 2, so.ChunkCount, "N deltas (1) plus the terminal chunk")
		require.Equal(t, 1, so.TerminalIndex)
		require.Equal(t, stream.Bytes(), so.BytesPlanned)
		require.JSONEq(t, `{}`, string(so.Usage))
		require.NotNil(t, so.CostTotal)
		require.InDelta(t, 0.5, *so.CostTotal, 0)
		require.Zero(t, so.ChunksSent, "observed fields are zero until close")
		return nil
	}

	r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`))
	hf(w, r)

	require.True(t, sawBeforeFirstByte, "the onWrite hook never ran: this proved nothing")
	require.Equal(t, "text/event-stream", w.header.Get("Content-Type"))
	require.Equal(t, "no-cache", w.header.Get("Cache-Control"))
	require.Empty(t, w.header.Get("Content-Length"), "Content-Length would defeat chunked framing")

	want := append(append(append([]byte{}, stream.Chunks[0].Bytes...), stream.Chunks[1].Bytes...), doneChunk().Bytes...)
	require.Equal(t, want, w.body.Bytes(), "the wire bytes are chunk 0, chunk 1, then DONE")

	// Handle has already returned by the time hf(w, r) above does, so the
	// observed half is amended synchronously too — no polling needed.
	final := j.Snapshot()[0]
	require.Equal(t, journal.StreamCompleted, final.Outcome.Stream.State)
	require.Equal(t, 2, final.Outcome.Stream.ChunksSent)
	require.Equal(t, len(want), final.Outcome.BytesWritten)
	require.Equal(t, journal.OutcomeScenario, final.Outcome.Kind)
	require.Equal(t, "test.stream", final.Outcome.Label)
}

func TestHandleStreamDeterministicAcrossRuns(t *testing.T) {
	t.Parallel()

	run := func() []byte {
		j := journal.NewRing(8, 4096)
		srv := httptest.NewServer(Handle(Deps{Journal: j}, Exa, testRoute, streamHandler(twoChunkStream())))
		defer srv.Close()
		resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return body
	}

	a, b := run(), run()
	require.Equal(t, a, b, "the same scripted stream must render byte-identical transcripts across runs")
}

// TestHandleSuppressesStreamOnAFaultThatPrecedesTheStream proves the six
// suppressing kinds turn a would-be stream into an ordinary JSON error, with
// the streaming Content-Type reset rather than leaking onto the error body.
func TestHandleSuppressesStreamOnAFaultThatPrecedesTheStream(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{Status: http.StatusTooManyRequests}}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, streamHandler(twoChunkStream())))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.NotEqual(t, "text/event-stream", resp.Header.Get("Content-Type"),
		"the streaming Content-Type must not leak onto a suppressed fault's JSON body")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.Nil(t, entries[0].Outcome.Stream, "a suppressed stream journals no stream outcome at all")
	require.Equal(t, journal.OutcomeFault, entries[0].Outcome.Kind)
	require.Equal(t, string(scenario.FaultStatus), entries[0].Outcome.FaultKind)
}

// TestHandleStreamAppliesAttemptHeaders proves a non-suppressing attempt's
// declared headers, Retry-After and status override reach the wire on the
// stream path exactly as they do on the non-streaming default branch —
// docs/design/streaming.md §4.3: "the real call is applyHeader(w,
// faultHeader(a, resp))". A trailing "kind: none" attempt with only
// headers:/retry_after:/status: is fault-eligible, is not one of the six
// suppressing kinds, and is not the truncate_body mismatch case, so it
// reaches executeStream — and previously vanished silently there.
func TestHandleStreamAppliesAttemptHeaders(t *testing.T) {
	t.Parallel()

	retryAfter := 7
	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Status: http.StatusCreated, Headers: map[string]string{"X-Custom": "yes"}, RetryAfter: &retryAfter},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, streamHandler(twoChunkStream())))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusCreated, resp.StatusCode, "the attempt's status override reaches the stream path")
	require.Equal(t, "yes", resp.Header.Get("X-Custom"), "the attempt's declared header must reach the stream path")
	require.Equal(t, "7", resp.Header.Get("Retry-After"))
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"),
		"faultHeader must not clobber the streaming Content-Type: none of the six suppressing kinds applies here")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "data: [DONE]", "the stream itself is unaffected by a non-suppressing attempt")
}

// TestHandleStreamAbortUnreachable is the mirror case: a truncate_body attempt
// claimed against an exchange that will stream cannot apply — truncate_body
// assumes a Content-Length JSON body — so it is reported and the stream is
// served in full, exactly as scripted.
func TestHandleStreamAbortUnreachable(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream()
	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultTruncateBody, TruncateAfterBytes: 5},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, streamHandler(stream)))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"),
		"the stream is served in full — the claimed attempt cannot apply to it")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	want := append(append(append([]byte{}, stream.Chunks[0].Bytes...), stream.Chunks[1].Bytes...), doneChunk().Bytes...)
	require.Equal(t, want, body, "the scripted attempt never touches the stream")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.False(t, entries[0].Outcome.Aborted, "nothing was aborted: the attempt never applied")
	require.Equal(t, string(scenario.FaultTruncateBody), entries[0].Outcome.FaultKind,
		"the journal still names what was SCRIPTED, even though it never fired")
	require.Zero(t, entries[0].Outcome.DelayMS, "the attempt's delay, if any, is scoped to the stream that never happens")

	var found *journal.Finding
	for i := range entries[0].Findings {
		if entries[0].Findings[i].Code == scenario.CodeStreamAbortUnreachable {
			found = &entries[0].Findings[i]
		}
	}
	require.NotNil(t, found, "findings: %+v", entries[0].Findings)
	require.Equal(t, journal.SeverityError, found.Severity)
}

// TestHandleStreamAbortUnreachableOversizedBody is oversized_body's mirror of
// TestHandleStreamAbortUnreachable, above: an oversized_body attempt claimed
// against an exchange that will stream cannot apply either — it sets a
// Content-Length over a padded JSON body, which is exactly as wrong for
// chunked SSE as truncate_body's Content-Length is — so it is reported and
// the stream is served in full, exactly as scripted, with no padding at all.
func TestHandleStreamAbortUnreachableOversizedBody(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream()
	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultOversizedBody, BodyBytes: 4 << 20},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, streamHandler(stream)))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"),
		"the stream is served in full — the claimed attempt cannot apply to it")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	want := append(append(append([]byte{}, stream.Chunks[0].Bytes...), stream.Chunks[1].Bytes...), doneChunk().Bytes...)
	require.Equal(t, want, body, "the scripted attempt never touches the stream — no padding at all")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.False(t, entries[0].Outcome.Aborted, "nothing was aborted: the attempt never applied")
	require.Equal(t, string(scenario.FaultOversizedBody), entries[0].Outcome.FaultKind,
		"the journal still names what was SCRIPTED, even though it never fired")

	var oversizedFound *journal.Finding
	for i := range entries[0].Findings {
		if entries[0].Findings[i].Code == scenario.CodeStreamAbortUnreachable {
			oversizedFound = &entries[0].Findings[i]
		}
	}
	require.NotNil(t, oversizedFound, "findings: %+v", entries[0].Findings)
	require.Equal(t, journal.SeverityError, oversizedFound.Severity)
}

// TestHandleStreamClientGone proves the third path a stream can end on: the
// client hangs up mid-stream, with no fault involved at all.
// TestHandleStreamClientGone proves the third path a stream can end on: the
// client hangs up mid-stream, with no fault involved at all. The second
// w.Write call — the terminal chunk — is made to fail, which is exactly how
// executeStream observes a connection the client already closed; that
// failure is a program fact, not a race window, so no real-time pacing is
// needed to force it.
func TestHandleStreamClientGone(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream()
	j := journal.NewRing(8, 4096)
	hf := Handle(Deps{Journal: j}, Exa, testRoute, streamHandler(stream))

	w := &capturingWriter{}
	w.onWrite = func(call int) error {
		if call == 2 {
			return errors.New("write: broken pipe")
		}
		return nil
	}

	r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`))
	hf(w, r)

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, journal.StreamClientGone, so.State)
	require.Equal(t, 1, so.ChunksSent, "chunk 0 reached the client before it hung up")
	require.Equal(t, len(stream.Chunks[0].Bytes), entries[0].Outcome.BytesWritten)
}

// TestHandleStreamFalseServesOrdinaryJSON proves a stream: false request on a
// stream-scripted turn is unaffected by any of this: Response.Stream is
// simply nil, and the ordinary path runs exactly as it always has.
func TestHandleStreamFalseServesOrdinaryJSON(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(8, 4096)
	h := func(_ *Exchange) Response {
		return Response{Status: http.StatusOK, Body: []byte(`{"ok":true}`), Label: "test.ok", FaultEligible: true}
	}
	srv := httptest.NewServer(Handle(Deps{Journal: j}, Exa, testRoute, h))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, `{"ok":true}`, string(body))

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.Nil(t, entries[0].Outcome.Stream)
}

// --- planStream (pure) ------------------------------------------------------

// pacedThreeChunkStream is a 2-delta, 1-terminal stream with a distinct
// scripted pace per chunk, so paceOf/paceMS tests can tell indices apart by
// value alone.
func pacedThreeChunkStream() *Stream {
	events := []SSEEvent{
		{Data: []byte(`{"i":0}`), Pace: 40 * time.Millisecond},
		{Data: []byte(`{"i":1}`), Pace: 10 * time.Millisecond},
		{Data: []byte(`{"i":2}`), Pace: 5 * time.Millisecond, Terminal: true},
	}
	return &Stream{Grammar: GrammarDelta, Chunks: EncodeSSE(events), DonePace: 7 * time.Millisecond}
}

func TestPlanStream(t *testing.T) {
	t.Parallel()

	t.Run("nil attempt: paces come straight from the chunks, nothing aborts or stalls", func(t *testing.T) {
		t.Parallel()
		p := planStream(nil, pacedThreeChunkStream())
		require.Equal(t, -1, p.disconnectAt)
		require.Equal(t, -1, p.truncateAt)
		require.Equal(t, -1, p.stallAt)
		require.Equal(t, []int64{40, 10, 5}, p.paceMS())
		require.Equal(t, 7*time.Millisecond, p.donePace)
	})

	t.Run("a non-stream_* attempt behaves exactly like nil", func(t *testing.T) {
		t.Parallel()
		p := planStream(&scenario.FaultAttempt{Status: http.StatusCreated}, pacedThreeChunkStream())
		require.Equal(t, -1, p.disconnectAt)
		require.Equal(t, -1, p.truncateAt)
		require.Equal(t, -1, p.stallAt)
	})

	t.Run("stream_disconnect sets disconnectAt and carries reset", func(t *testing.T) {
		t.Parallel()
		p := planStream(&scenario.FaultAttempt{Kind: scenario.FaultStreamDisconnect, AfterChunk: 1, Reset: true},
			pacedThreeChunkStream())
		require.Equal(t, 1, p.disconnectAt)
		require.True(t, p.reset)
		require.Equal(t, -1, p.truncateAt)
		require.Equal(t, -1, p.stallAt)
	})

	t.Run("stream_truncate_chunk with no explicit length halves the target chunk", func(t *testing.T) {
		t.Parallel()
		stream := pacedThreeChunkStream()
		p := planStream(&scenario.FaultAttempt{Kind: scenario.FaultStreamTruncateChunk, AfterChunk: 1}, stream)
		require.Equal(t, 1, p.truncateAt)
		require.Equal(t, len(stream.Chunks[1].Bytes)/2, p.truncateBytes)
		require.Equal(t, -1, p.disconnectAt)
	})

	t.Run("stream_truncate_chunk's explicit length is clamped to the chunk's own bytes", func(t *testing.T) {
		t.Parallel()
		stream := pacedThreeChunkStream()
		p := planStream(&scenario.FaultAttempt{
			Kind: scenario.FaultStreamTruncateChunk, AfterChunk: 0, TruncateAfterBytes: 10_000,
		}, stream)
		require.Equal(t, len(stream.Chunks[0].Bytes), p.truncateBytes,
			"clamped to the chunk's own length, not the whole stream")
	})

	t.Run("stream_stall folds Delay into paceOf at AfterChunk only", func(t *testing.T) {
		t.Parallel()
		p := planStream(&scenario.FaultAttempt{
			Kind: scenario.FaultStreamStall, AfterChunk: 1, Delay: scenario.Duration(65 * time.Second),
		}, pacedThreeChunkStream())
		require.Equal(t, 1, p.stallAt)
		require.Equal(t, 65*time.Second, p.stallExtra)
		require.Equal(t, 40*time.Millisecond, p.paceOf(0), "every other index is untouched")
		require.Equal(t, 10*time.Millisecond+65*time.Second, p.paceOf(1))
		require.Equal(t, 5*time.Millisecond, p.paceOf(2))
		require.Equal(t, []int64{40, 65_010, 5}, p.paceMS(), "PaceMS[AfterChunk] already includes the stall")
	})

	t.Run("an out-of-range AfterChunk on a hand-built attempt never matches — a silent no-op, not a panic", func(t *testing.T) {
		t.Parallel()
		stream := pacedThreeChunkStream()
		p := planStream(&scenario.FaultAttempt{Kind: scenario.FaultStreamDisconnect, AfterChunk: 99}, stream)
		require.Equal(t, 99, p.disconnectAt, "the plan still records what was asked; the loop simply never reaches it")
	})

	t.Run("eventNames is nil for GrammarDelta — unnamed frames", func(t *testing.T) {
		t.Parallel()
		p := planStream(nil, pacedThreeChunkStream())
		require.Nil(t, p.eventNames(), "GrammarDelta's frames carry no event: line at all")
	})

	t.Run("eventNames returns every chunk's Name, in order, for a named (GrammarTyped-shaped) stream", func(t *testing.T) {
		t.Parallel()
		events := []SSEEvent{
			{Name: "response.created", Data: []byte(`{"a":0}`)},
			{Name: "response.output_text.delta", Data: []byte(`{"a":1}`)},
			{Name: "response.completed", Data: []byte(`{"a":2}`), Terminal: true},
		}
		stream := &Stream{Grammar: GrammarTyped, Chunks: EncodeSSE(events)}
		p := planStream(nil, stream)
		require.Equal(t, []string{"response.created", "response.output_text.delta", "response.completed"}, p.eventNames())
	})
}

// --- executeStream: aborting fault kinds ------------------------------------

// TestHandleStreamDisconnect proves stream_disconnect's frame-boundary-clean
// semantics end to end: chunk 0 (before AfterChunk) reaches the client
// whole, the connection then dies before chunk 1 (the terminal chunk, which
// AfterChunk: 1 targets) is written at all, and the journal names exactly
// that split — the client observes a transport error after exactly
// AfterChunk complete frames, not AfterChunk+1.
func TestHandleStreamDisconnect(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream()
	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultStreamDisconnect, AfterChunk: 1, Reset: true},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, streamHandler(stream)))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err, "headers and chunk 0 arrive; only the terminal chunk and DONE are missing")
	defer func() { _ = resp.Body.Close() }()
	require.Empty(t, resp.Header.Get("Content-Length"), "no Content-Length anywhere on the stream path, faulted or not")

	body, err := io.ReadAll(resp.Body)
	require.Error(t, err, "the connection must die before the terminal chunk (index 1) ever reaches the client")
	require.Equal(t, stream.Chunks[0].Bytes, body, "exactly chunk 0 — the last complete frame before the disconnect")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.True(t, entries[0].Outcome.Aborted, "a scripted stream_disconnect is an aborting fault, set at append")
	require.Equal(t, string(scenario.FaultStreamDisconnect), entries[0].Outcome.FaultKind)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, journal.StreamAborted, so.State)
	require.Equal(t, 1, so.ChunksSent, "chunk 0 was fully delivered; chunk 1 (AfterChunk) never was")
	require.NotNil(t, so.AbortAfterChunk)
	require.Equal(t, 1, *so.AbortAfterChunk)
	require.Nil(t, so.TruncatedAtByte, "stream_disconnect never truncates a frame")
	require.Equal(t, len(stream.Chunks[0].Bytes), entries[0].Outcome.BytesWritten)
}

// TestHandleStreamDisconnectAtChunkZero proves the boundary case where
// AfterChunk: 0 aborts before ANY chunk is written — only the flushed
// headers reach the client.
func TestHandleStreamDisconnectAtChunkZero(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream()
	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultStreamDisconnect, AfterChunk: 0},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, streamHandler(stream)))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)

	// Checked BEFORE the error assertion below: the journal is appended
	// before the first byte (§5, Handle's widened journal-early condition),
	// so this is available regardless of what the client's read observed,
	// and it is what tells a future flake here (see the comment on
	// require.Error below) whether the client silently made a SECOND
	// request rather than merely observing the disconnect oddly.
	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.Equal(t, 1, engine.calls, "exactly one attempt claimed — never a silent retry")

	// This assertion has been observed to flake once under heavy -race
	// -count=N hammering, without a known root cause (net/http's client
	// transparently retrying, or ephemeral-port pressure in the test
	// harness, are the leading suspects) — see the go-developer review that
	// found it. The two assertions above run first specifically so a future
	// occurrence leaves more evidence than "an error is expected but got
	// nil".
	require.Error(t, err)
	require.Empty(t, body, "not even chunk 0 was written")

	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Zero(t, so.ChunksSent)
	require.Equal(t, 0, entries[0].Outcome.BytesWritten)
	require.Equal(t, journal.StreamAborted, so.State)
}

// TestHandleStreamTruncateChunk proves the malformed-frame sibling of
// disconnect: a partial "data:" line reaches the client, distinguishable
// from the clean boundary above.
func TestHandleStreamTruncateChunk(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream()
	fullLen := len(stream.Chunks[0].Bytes)
	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultStreamTruncateChunk, AfterChunk: 0, TruncateAfterBytes: 5},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, streamHandler(stream)))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err, "headers reach the client; only the frame at index 0 is malformed")
	defer func() { _ = resp.Body.Close() }()
	require.Empty(t, resp.Header.Get("Content-Length"), "no Content-Length anywhere on the stream path, faulted or not")

	body, err := io.ReadAll(resp.Body)
	require.Error(t, err, "the connection must die mid-frame, not at a boundary")
	require.Equal(t, stream.Chunks[0].Bytes[:5], body, "exactly 5 bytes of chunk 0's frame — a malformed data: line")
	require.Less(t, len(body), fullLen)

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.True(t, entries[0].Outcome.Aborted)
	require.Equal(t, string(scenario.FaultStreamTruncateChunk), entries[0].Outcome.FaultKind)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, journal.StreamAborted, so.State)
	require.Zero(t, so.ChunksSent, "the truncated chunk was never fully delivered, so it does not count as sent")
	require.NotNil(t, so.AbortAfterChunk)
	require.Equal(t, 0, *so.AbortAfterChunk)
	require.NotNil(t, so.TruncatedAtByte)
	require.Equal(t, 5, *so.TruncatedAtByte)
	require.Equal(t, 5, entries[0].Outcome.BytesWritten)
}

// TestHandleStreamTruncateChunkZeroBytesHalvesTheChunk pins the "zero means
// half of THIS chunk" rule, distinct from truncate_body's "half of the
// body": the two fault kinds reuse one YAML key with different halves.
func TestHandleStreamTruncateChunkZeroBytesHalvesTheChunk(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream()
	want := len(stream.Chunks[0].Bytes) / 2
	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultStreamTruncateChunk, AfterChunk: 0},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, streamHandler(stream)))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, want, len(body))

	so := j.Snapshot()[0].Outcome.Stream
	require.Equal(t, want, *so.TruncatedAtByte)
}

// TestHandleStreamDisconnectAbortedIsTrueBeforeTheFirstByte proves
// Outcome.Aborted reads true from the append onward — before the abort ever
// actually happens on the wire — exactly as it already does for
// close_before_headers and truncate_body. Synchronous, no real-time race: the
// onWrite hook inspects the journal from inside executeStream's own first
// Write call, in the same goroutine Handle runs in.
func TestHandleStreamDisconnectAbortedIsTrueBeforeTheFirstByte(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream()
	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultStreamDisconnect, AfterChunk: 1},
	}}
	hf := Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, streamHandler(stream))

	var sawBeforeFirstByte bool
	w := &capturingWriter{}
	w.onWrite = func(call int) error {
		if call != 1 {
			return nil
		}
		sawBeforeFirstByte = true
		entries := j.Snapshot()
		require.Len(t, entries, 1)
		require.True(t, entries[0].Outcome.Aborted,
			"Outcome.Aborted must already read true here, not only after the abort actually fires on the wire")
		require.Equal(t, string(scenario.FaultStreamDisconnect), entries[0].Outcome.FaultKind)
		return nil
	}

	r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`))
	// hf panics with http.ErrAbortHandler once it reaches AfterChunk — the
	// same sentinel a real net/http server recovers silently; called
	// directly (not through httptest.NewServer) there is nothing to catch it
	// here except this. The onWrite hook above already ran and made its
	// assertions by the time the panic unwinds to this line.
	require.Panics(t, func() { hf(w, r) })

	require.True(t, sawBeforeFirstByte, "the onWrite hook never ran: this proved nothing")
}

// TestHandleStreamCancelledPreDispatchDelayLeavesAbortedFalse is the mirror
// of the test above, for the case the pre-existing unit-1 comment in
// fault_exec.go's execute() only documented rather than proved: the CLIENT's
// own cancellation during the generic pre-dispatch delay — not a scripted
// abort — ends a streaming exchange. Outcome.Aborted must stay false, because
// nothing about the SCRIPT aborted anything; State (via the deferred
// fallback closer) is the authoritative signal instead. Deterministic and
// instant: the request context is cancelled before Handle ever runs, so
// sleep()'s select observes ctx.Done() immediately rather than waiting out
// the scripted one-hour delay.
func TestHandleStreamCancelledPreDispatchDelayLeavesAbortedFalse(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream()
	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{Delay: scenario.Duration(time.Hour)}}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`)).WithContext(ctx)

	hf := Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, streamHandler(stream))
	w := httptest.NewRecorder()
	hf(w, r)

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.False(t, entries[0].Outcome.Aborted,
		"the client's own cancellation, not a scripted abort, ended this exchange")
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, journal.StreamClientGone, so.State,
		"State, not Aborted, is the authoritative signal for a streamed exchange's own client-side ending")
	require.Zero(t, so.ChunksSent)
}

// TestHandleStreamPaceIsHonouredUnderDelayReal proves plan.paceOf(i)'s sleep
// is not a no-op: PaceMS records the PLANNED schedule under every DelayMode
// (§6.3), which does not by itself prove a real clock ever sleeps it — a
// build that slept 0 regardless of the plan would leave every PaceMS-only
// assertion green. Deterministic and instant, the same technique the test
// above uses: the request context is already cancelled, so chunk 0's own
// zero pace writes immediately (sleep's d<=0 branch never consults ctx at
// all) and chunk 1's one-hour pace fails on ctx.Done() the instant it is
// slept — proving the sleep actually ran, not merely that it was planned.
func TestHandleStreamPaceIsHonouredUnderDelayReal(t *testing.T) {
	t.Parallel()

	events := []SSEEvent{
		{Data: []byte(`{"i":0}`)},
		{Data: []byte(`{"i":1}`), Pace: time.Hour, Terminal: true},
	}
	stream := &Stream{Grammar: GrammarDelta, Chunks: EncodeSSE(events)}

	j := journal.NewRing(8, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`)).WithContext(ctx)

	hf := Handle(Deps{Journal: j, DelayMode: DelayReal}, Exa, testRoute, streamHandler(stream))
	w := httptest.NewRecorder()
	hf(w, r)

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, journal.StreamClientGone, so.State,
		"chunk 1's one-hour pace must actually be slept under DelayReal, not skipped")
	require.Equal(t, 1, so.ChunksSent, "chunk 0 (zero pace) reached the wire; chunk 1 never got its pace slept out")
	require.Equal(t, stream.Chunks[0].Bytes, w.Body.Bytes())
}

// TestHandleStreamDonePaceIsHonouredUnderDelayReal is the DONE-sentinel
// sibling of the test above: [DONE] is never an indexed chunk (§3.2), so its
// gap is Stream.DonePace, slept once — through plan.donePace — after the loop
// over plan.chunks completes, never through plan.paceOf. A build that slept 0
// there regardless of DonePace would still pass every PaceMS assertion, since
// PaceMS never includes DONE.
func TestHandleStreamDonePaceIsHonouredUnderDelayReal(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream() // both chunks pace 0; only DonePace is nonzero
	stream.DonePace = time.Hour

	j := journal.NewRing(8, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`)).WithContext(ctx)

	hf := Handle(Deps{Journal: j, DelayMode: DelayReal}, Exa, testRoute, streamHandler(stream))
	w := httptest.NewRecorder()
	hf(w, r)

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, journal.StreamClientGone, so.State, "DONE's own pace must be honoured under DelayReal too")
	require.Equal(t, 2, so.ChunksSent, "both indexed chunks were written; only DONE never arrived")
	wantBody := append(append([]byte{}, stream.Chunks[0].Bytes...), stream.Chunks[1].Bytes...)
	require.Equal(t, wantBody, w.Body.Bytes())
}

// --- executeStream: stream_stall (never aborts) -----------------------------

// TestHandleStreamStallFoldsIntoPaceMS proves the fold-in rule end to end
// under DelaySkip, so the scripted 65s costs nothing here while the PLANNED
// schedule is still recorded (docs/design/streaming.md §6.3): a test
// asserting the SCHEDULE, as opposed to a real timeout, must run free of
// wall-clock cost.
func TestHandleStreamStallFoldsIntoPaceMS(t *testing.T) {
	t.Parallel()

	events := []SSEEvent{
		{Data: []byte(`{"i":0}`), Pace: 40 * time.Millisecond},
		{Data: []byte(`{"i":1}`), Pace: 10 * time.Millisecond, Terminal: true},
	}
	stream := &Stream{Grammar: GrammarDelta, Chunks: EncodeSSE(events)}

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultStreamStall, AfterChunk: 1, Delay: scenario.Duration(65 * time.Second)},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine, DelayMode: DelaySkip},
		Exa, testRoute, streamHandler(stream)))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err, "a stall never aborts; the stream completes normally")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.False(t, entries[0].Outcome.Aborted, "a stall is not an aborting fault")
	require.Equal(t, string(scenario.FaultStreamStall), entries[0].Outcome.FaultKind)
	require.Zero(t, entries[0].Outcome.DelayMS, "a stall's Delay is never a time-to-first-byte sleep")
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, journal.StreamCompleted, so.State)
	require.Equal(t, 2, so.ChunksSent)
	require.Equal(t, []int64{40, 65_000 + 10}, so.PaceMS, "PaceMS[1] folds the stall's 65s into chunk 1's own 10ms pace")
	require.NotNil(t, so.StallBeforeMS)
	require.Equal(t, int64(65_000), *so.StallBeforeMS)
}

// TestHandleStreamStallSkipsThePreDispatchDelayCarveOut proves the
// FaultStreamStall carve-out in execute (fault_exec.go) end to end: a stall's
// Delay is the mid-stream pause planStream folds into chunk AfterChunk's own
// pace, never the generic time-to-first-byte sleep execute otherwise runs for
// every kind before dispatch. Without the carve-out, that generic sleep would
// run FIRST — before headers, before chunk 0 — and would itself observe the
// already-cancelled context and fail before anything reaches the wire.
// Deterministic and instant: chunk 0's own pace is zero, so with the
// carve-out chunk 0 always reaches the wire before the stall's one-hour
// Delay is slept (folded into chunk 1's pace) and fails on ctx.Done().
func TestHandleStreamStallSkipsThePreDispatchDelayCarveOut(t *testing.T) {
	t.Parallel()

	stream := twoChunkStream() // both chunks pace 0
	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultStreamStall, AfterChunk: 1, Delay: scenario.Duration(time.Hour)},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`)).WithContext(ctx)

	hf := Handle(Deps{Journal: j, Faults: engine, DelayMode: DelayReal}, Exa, testRoute, streamHandler(stream))
	w := httptest.NewRecorder()
	hf(w, r)

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, journal.StreamClientGone, so.State)
	require.Equal(t, 1, so.ChunksSent,
		"chunk 0 must reach the wire before the stall's Delay is slept at chunk 1 — without the carve-out, "+
			"execute's generic pre-dispatch delay sleeps the stall's Delay BEFORE dispatch and nothing is written")
	require.Equal(t, stream.Chunks[0].Bytes, w.Body.Bytes())
}

// --- the other abort_unreachable mirror: a stream_* kind on an exchange
// that will not stream ------------------------------------------------------

// TestHandleStreamFaultKindClaimedOnNonStreamingExchange is the direction
// TestHandleStreamAbortUnreachable (unit 1) does not cover: a stream_* kind
// is claimed by a request that draws it at turn selection, but THIS request
// will not stream at all (an ordinary handler with no Response.Stream). The
// entry's policy answers "does this surface serve a stream when asked", not
// "does this call ask" — a consumer may send stream: true on one call and
// stream: false on the next in the same lane — so this is reachable even
// under a fully valid, load-time-clean fixture. Reported, never silently
// served as a plain 200.
func TestHandleStreamFaultKindClaimedOnNonStreamingExchange(t *testing.T) {
	t.Parallel()

	for _, kind := range []scenario.FaultKind{
		scenario.FaultStreamDisconnect, scenario.FaultStreamTruncateChunk, scenario.FaultStreamStall,
	} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			j := journal.NewRing(8, 4096)
			engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{Kind: kind, AfterChunk: 0}}}
			h := func(_ *Exchange) Response {
				return Response{Status: http.StatusOK, Body: []byte(`{"ok":true}`), Label: "test.ok", FaultEligible: true}
			}
			srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, h))
			defer srv.Close()

			resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusOK, resp.StatusCode,
				"served exactly as scripted — the claimed attempt cannot apply and is reported, not absorbed silently")
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, `{"ok":true}`, string(body))

			entries := j.Snapshot()
			require.Len(t, entries, 1)
			require.Nil(t, entries[0].Outcome.Stream)
			require.False(t, entries[0].Outcome.Aborted, "nothing was aborted: the attempt never applied")
			require.Equal(t, string(kind), entries[0].Outcome.FaultKind, "the journal still names what was SCRIPTED")
			require.Zero(t, entries[0].Outcome.DelayMS)

			var found *journal.Finding
			for i := range entries[0].Findings {
				if entries[0].Findings[i].Code == scenario.CodeStreamAbortUnreachable {
					found = &entries[0].Findings[i]
				}
			}
			require.NotNil(t, found, "findings: %+v", entries[0].Findings)
			require.Equal(t, journal.SeverityError, found.Severity)
		})
	}
}
