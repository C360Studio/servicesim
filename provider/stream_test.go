package provider

import (
	"bytes"
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
