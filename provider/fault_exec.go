package provider

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/internal/wire"
	"github.com/c360studio/servicesim/scenario"
)

// faultKindDelay names a delay-only attempt in the journal. Delay is a modifier
// rather than a scenario.FaultKind, but an attempt that only delays did fault the
// request as far as an observer is concerned, so the outcome says so.
const faultKindDelay = "delay"

// defaultInvalidJSONBody is what an invalid_json fault sends when the attempt
// declares no raw_body: transport-valid, JSON-invalid, with a correct
// Content-Length and a reusable connection. That is what makes it distinct from
// truncation, which is a transport failure.
const defaultInvalidJSONBody = `{"results": [{"title": "unterminated"`

// defaultWrongContentType is the override a wrong_content_type fault applies when
// the attempt names none. The body bytes stay valid JSON: the point of the fault
// is a consumer that trusts the header.
const defaultWrongContentType = "text/html; charset=utf-8"

// defaultContentType is what a response carries when neither the handler nor the
// fault says otherwise.
const defaultContentType = "application/json"

// faultOutcome computes the journal Outcome an attempt will produce, without
// writing anything. Splitting this out of execute is what lets Handle journal an
// aborting fault before the client can observe the abort. For aborting kinds the
// byte count is known in advance: zero for close_before_headers, the truncation
// length for truncate_body.
//
// When resp.Stream is non-nil — which, by the time Handle calls this, means
// "this exchange WILL stream" (suppression already ran) — out.Stream is the
// PLANNED half of the streaming outcome, final before the first byte; see
// plannedStreamOutcome.
func faultOutcome(dec FaultDecision, resp Response) journal.Outcome {
	out := journal.Outcome{
		Kind:         journal.OutcomeScenario,
		Label:        resp.Label,
		Status:       resp.Status,
		FaultKey:     dec.Key,
		AttemptIndex: dec.Index,
	}
	if resp.Stream != nil {
		out.Stream = plannedStreamOutcome(dec.Attempt, resp.Stream)
	}
	if !resp.FaultEligible {
		// Routing, authentication or validation rejected the request, so this is
		// the provider's own error response and no attempt was consumed.
		out.Kind = journal.OutcomeError
	}

	a := dec.Attempt
	if a == nil {
		return out
	}

	kind := a.EffectiveKind()
	// DelayMS means time-to-first-byte for every kind that has one — but
	// stream_stall's Delay is the mid-stream pause planStream folds into
	// PaceMS at AfterChunk instead (docs/design/streaming.md §3.1), so it is
	// deliberately excluded here and reported only through
	// Outcome.Stream.StallBeforeMS below. Reporting it as DelayMS too would
	// double-count one duration under two different journal fields.
	if kind != scenario.FaultStreamStall {
		out.DelayMS = a.Delay.Duration().Milliseconds()
	}

	if kind == scenario.FaultNone {
		// A trailing "status: 200" attempt renders the scenario response. A
		// delay-only attempt still faulted it.
		if a.Delay > 0 {
			out.Kind = journal.OutcomeFault
			out.FaultKind = faultKindDelay
		}
		return out
	}

	out.Kind = journal.OutcomeFault
	out.FaultKind = string(kind)
	if a.Status > 0 {
		out.Status = a.Status
	}

	switch kind {
	case scenario.FaultCloseBeforeHeaders:
		// Nothing reaches the client, so there is no status to report either.
		out.Aborted = true
		out.Status = 0
		out.BytesWritten = 0
	case scenario.FaultTruncateBody:
		out.Aborted = true
		out.BytesWritten = truncationLen(a, resp)
	case scenario.FaultEmptyBody:
		out.BytesWritten = 0
	case scenario.FaultStreamDisconnect, scenario.FaultStreamTruncateChunk:
		// Outcome.Aborted reflects the SCRIPT, set here at append — before
		// the first byte — exactly as it already is for close_before_headers
		// and truncate_body above: a streamed fault's Aborted and FaultKind
		// must read the way the non-streaming aborting faults' do. The
		// observed byte/chunk counts are filled in later by closeWith/closer
		// once the exchange actually closes; nothing here can know them yet.
		out.Aborted = true
	}
	return out
}

// execute applies a fault to the wire and returns the completed outcome (out with
// BytesWritten filled in). Delay runs first for every kind, under mode. For
// aborting kinds it does not return: it panics with http.ErrAbortHandler, and
// Handle's deferred record — already run for those kinds — is a no-op before the
// re-panic.
//
// One branch, not a switch, decides whether this exchange streams:
// resp.Stream != nil dispatches to executeStream and nothing else in this
// function inspects it. execute does not decide suppression and cannot tell
// that it happened — Handle applied it before this was called (see
// suppressStream), so a suppressed stream arrives with Stream already nil and
// takes the ordinary path below exactly as any non-streaming response does.
// Adding a suppressesStream call here would be the single most likely thing
// for a change to reintroduce: it would be a second derivation of a decision
// Handle already made, invisible to everything that read the first one.
func execute(ctx context.Context, w http.ResponseWriter, a *scenario.FaultAttempt,
	resp Response, mode DelayMode, out journal.Outcome, closer func(journal.StreamClose),
) journal.Outcome {
	// stream_stall's Delay is the mid-stream pause planStream resolves
	// instead (folded into PaceOf at AfterChunk), never a time-to-first-byte
	// sleep — docs/design/streaming.md §3.1. Running this block for it too
	// would sleep Delay TWICE: once here, before the status line, and again
	// inside executeStream before chunk AfterChunk.
	if a != nil && a.Delay > 0 && a.EffectiveKind() != scenario.FaultStreamStall {
		if err := sleep(ctx, a.Delay.Duration(), mode); err != nil {
			// The client's own deadline or cancellation ended the request while the
			// delay was still running. Writing now would be shouting into a closed
			// socket; the journal records that nothing was delivered.
			//
			// out.Aborted is set here only for a NON-streaming exchange, where this
			// returned Outcome is what gets retained. For a STREAMING exchange,
			// record() already ran (Handle's widened journal-early condition)
			// before execute was ever called, with Aborted reflecting the SCRIPT —
			// nothing about the script aborted this, the client's own
			// cancellation did — so Aborted must stay false there too; State
			// (amended to client_gone via closer, below) is the authoritative
			// signal for a streamed exchange instead.
			if resp.Stream == nil {
				out.Aborted = true
			}
			out.BytesWritten = 0
			return out
		}
	}

	if resp.Stream != nil {
		return executeStream(ctx, w, a, resp, mode, out, closer)
	}

	if a == nil {
		out.BytesWritten = writeResponse(w, resp.Status, resp.Header, contentTypeOf(resp), resp.Body)
		return out
	}

	body := faultBody(a, resp)
	status := resp.Status
	if a.Status > 0 {
		status = a.Status
	}
	header := faultHeader(a, resp)

	switch a.EffectiveKind() {
	case scenario.FaultCloseBeforeHeaders:
		closeBeforeHeaders(w)
		panic(http.ErrAbortHandler)

	case scenario.FaultTruncateBody:
		truncateBody(w, a, resp, body, status, header)
		panic(http.ErrAbortHandler)

	case scenario.FaultEmptyBody:
		applyHeader(w, header)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(statusOr(status))
		out.BytesWritten = 0
		return out

	default:
		out.BytesWritten = writeResponse(w, status, header, header.Get("Content-Type"), body)
		return out
	}
}

// faultBody returns the bytes a faulted response writes. The order is deliberate:
// a raw body override wins outright because it is the only way to send bytes that
// are not JSON, then the provider's own error shape, then the attempt's declared
// body, and only then the rendered scenario body.
func faultBody(a *scenario.FaultAttempt, resp Response) []byte {
	switch a.EffectiveKind() {
	case scenario.FaultInvalidJSON:
		if a.RawBody != "" {
			return []byte(a.RawBody)
		}
		return []byte(defaultInvalidJSONBody)
	case scenario.FaultEmptyBody:
		return nil
	}

	body := resp.Body
	if resp.FaultBody != nil {
		if b := resp.FaultBody(*a); b != nil {
			body = b
		}
	} else if len(a.Body) > 0 {
		if b, err := json.Marshal(a.Body); err == nil {
			body = b
		}
	}

	// extra_fields is not a transport change: it is merged into the body, which is
	// why the fault catalogue lists it for symmetry and execution does nothing
	// special for it.
	if len(a.ExtraFields) > 0 {
		if merged, err := wire.MergeJSON(body, a.ExtraFields); err == nil {
			body = merged
		}
	}
	return body
}

// suppressesStream reports whether kind is a fault a real vendor answers
// before a stream would ever start — an ordinary JSON error, not SSE wrapping
// one. truncate_body is deliberately absent: it is the one non-streaming kind
// whose interaction with a streaming exchange is reported rather than
// silently reinterpreted, because a byte-offset cut is wrong for a chunked
// body in a way none of these six kinds are (see Handle's mirror-case
// handling and scenario.CodeStreamFaultMismatch).
func suppressesStream(kind scenario.FaultKind) bool {
	switch kind {
	case scenario.FaultStatus, scenario.FaultInvalidJSON, scenario.FaultWrongContentType,
		scenario.FaultEmptyBody, scenario.FaultExtraFields, scenario.FaultCloseBeforeHeaders:
		return true
	default:
		return false
	}
}

// suppressStream drops resp.Stream and resp.Header, so the ordinary
// non-streaming path below renders resp.Body under the fault's own headers
// instead. Header is reset rather than filtered because a streaming
// Response's Header only ever holds streamHeader()'s two entries: without
// this, faultHeader would find "Content-Type: text/event-stream" already
// present and never fall back to its JSON default, leaking the streaming
// content type onto a JSON error body — a fidelity bug that would teach a
// consumer to parse the wrong thing.
func suppressStream(resp Response) Response {
	resp.Stream = nil
	resp.Header = nil
	return resp
}

// plannedStreamOutcome builds the PLANNED half of a streamed exchange's
// journal outcome from a fully rendered Stream and the (possibly nil,
// possibly non-stream_*) attempt claiming this exchange — everything that is
// known before the first byte is written. The observed half (ChunksSent,
// State) is left at its zero value; closeWith fills it in once the exchange
// closes.
//
// a is resolved the same way execute's is: Handle has already nilled it out
// for a claimed-but-unreachable mismatch, so a stream_* kind only ever
// reaches here when it truly applies to this exchange. planStream is the
// single source of truth for what a claimed attempt does to the plan;
// re-deriving abort/stall indices here with a second switch would be exactly
// the "two derivations of one decision" this design's suppression rule
// already warns against for a different case.
func plannedStreamOutcome(a *scenario.FaultAttempt, s *Stream) *journal.StreamOutcome {
	plan := planStream(a, s)
	out := &journal.StreamOutcome{
		Grammar:       string(s.Grammar),
		ChunkCount:    len(s.Chunks),
		BytesPlanned:  s.Bytes(),
		PaceMS:        plan.paceMS(),
		TerminalIndex: -1,
		Usage:         s.Usage,
		CostTotal:     s.CostTotal,
		State:         journal.StreamOpen,
	}
	for i, c := range s.Chunks {
		if c.Terminal {
			out.TerminalIndex = i
			break
		}
	}
	switch {
	case plan.disconnectAt >= 0:
		out.AbortAfterChunk = ptrTo(plan.disconnectAt)
	case plan.truncateAt >= 0:
		out.AbortAfterChunk = ptrTo(plan.truncateAt)
		out.TruncatedAtByte = ptrTo(plan.truncateBytes)
	}
	if plan.stallAt >= 0 {
		ms := plan.stallExtra.Milliseconds()
		out.StallBeforeMS = &ms
	}
	return out
}

// ptrTo returns a pointer to a copy of v, for populating an optional
// journal.StreamOutcome field from a plain int.
func ptrTo(v int) *int { return &v }

// faultHeader merges the attempt's headers over the handler's, and applies the
// Retry-After and Content-Type shorthands. The result is a fresh Header: the
// handler's response may be shared across requests once a provider caches a
// rendered body.
func faultHeader(a *scenario.FaultAttempt, resp Response) http.Header {
	h := http.Header{}
	for name, values := range resp.Header {
		h[name] = append([]string(nil), values...)
	}
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", defaultContentType)
	}
	for name, value := range a.Headers {
		h.Set(name, value)
	}
	if a.RetryAfter != nil {
		h.Set("Retry-After", strconv.Itoa(*a.RetryAfter))
	}
	switch {
	case a.ContentType != "":
		h.Set("Content-Type", a.ContentType)
	case a.EffectiveKind() == scenario.FaultWrongContentType:
		h.Set("Content-Type", defaultWrongContentType)
	case a.EffectiveKind() == scenario.FaultInvalidJSON:
		// Transport-valid, JSON-invalid: the header must still claim JSON, or the
		// consumer's decoder never runs and the fault proves nothing.
		h.Set("Content-Type", defaultContentType)
	}
	return h
}

// truncationLen is how many body bytes reach the client before the connection
// dies. Zero means half the body, which is the documented default and is always
// within range.
func truncationLen(a *scenario.FaultAttempt, resp Response) int {
	body := faultBody(a, resp)
	n := a.TruncateAfterBytes
	if n <= 0 {
		n = len(body) / 2
	}
	return min(n, len(body))
}

// closeBeforeHeaders sends nothing and destroys the connection.
func closeBeforeHeaders(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		// HTTP/2 has no Hijacker. Servicesim serves cleartext HTTP/1.1 only, so
		// this branch is unreachable in practice; aborting is the honest fallback.
		panic(http.ErrAbortHandler)
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	resetAndClose(conn)
}

// truncateBody declares the full length, sends a prefix, then aborts.
func truncateBody(w http.ResponseWriter, a *scenario.FaultAttempt, resp Response,
	body []byte, status int, header http.Header,
) {
	n := truncationLen(a, resp)
	applyHeader(w, header)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(statusOr(status))
	_, _ = w.Write(body[:n])

	// Flush pushes the partial bytes onto the wire. Without it net/http still
	// holds them in its buffer, ErrAbortHandler discards the buffer, and the
	// client sees a connection error with zero bytes — a connection fault, not a
	// truncation fault.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	if a.Reset {
		// A RST after the partial write makes the client report "connection reset
		// by peer" instead of an unexpected EOF, which is a different error class
		// for a consumer's retry policy to branch on.
		hijackReset(w)
	}
}

// hijackReset hijacks w's connection and destroys it with a RST rather than a
// clean FIN, through resetAndClose — the one mechanism truncateBody above and
// executeStream's stream_disconnect/stream_truncate_chunk abort path both
// reuse, per docs/design/streaming.md §4.3's instruction to find and reuse
// the existing flush-then-abort/SetLinger(0) machinery rather than duplicate
// it. Unlike closeBeforeHeaders, a failed Hijack here is not fatal on its
// own: bytes have already reached the client, so the caller's own
// panic(http.ErrAbortHandler) still aborts the connection either way, just
// without the RST.
func hijackReset(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return // HTTP/2 has no Hijacker; unreachable in this HTTP/1.1-only container.
	}
	if conn, _, err := hj.Hijack(); err == nil {
		resetAndClose(conn)
	}
}

// resetAndClose destroys a connection with a RST rather than a FIN.
//
// SetLinger(0) is what makes Close emit RST. It matters: after a clean FIN with
// zero bytes written, net/http.Transport may transparently retry an idempotent
// request with a rewindable body, and the test that was meant to observe a
// connection failure quietly observes a success instead.
func resetAndClose(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = conn.Close()
}

// writeResponse writes the headers, status and body, returning the byte count.
func writeResponse(w http.ResponseWriter, status int, header http.Header, contentType string, body []byte) int {
	applyHeader(w, header)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(statusOr(status))
	n, _ := w.Write(body)
	return n
}

// applyHeader copies header onto w. Header order on the wire does not depend on
// Go map iteration: net/http sorts header keys when it serialises them.
func applyHeader(w http.ResponseWriter, header http.Header) {
	dst := w.Header()
	for name, values := range header {
		dst.Del(name)
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}

// contentTypeOf returns the response's declared content type, defaulting to JSON.
func contentTypeOf(resp Response) string {
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		return ct
	}
	return defaultContentType
}

// statusOr substitutes 200 for a handler that left Status zero.
func statusOr(status int) int {
	if status <= 0 {
		return http.StatusOK
	}
	return status
}
