package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/internal/wire"
	"github.com/c360studio/servicesim/scenario"
)

// faultKindDelay names a delay-only or delay-after-headers-only attempt in the
// journal. Delay and DelayAfterHeaders are modifiers rather than a
// scenario.FaultKind, but an attempt that only delays — before dispatch, after
// headers, or both — did fault the request as far as an observer is concerned,
// so the outcome says so.
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
	// DelayAfterHeadersMS carries no such carve-out: unlike Delay, it is never
	// folded into a stream's pace schedule (delay_after_headers cannot apply
	// to a stream_* kind or to an exchange that will stream at all — see
	// Handle's mismatch handling and scenario.ValidateStreamFaultMismatch), so
	// every kind that can carry it past validation reports it exactly as
	// requested; a kind that cannot (stream_* or close_before_headers) has it
	// pinned at zero by validation, and zero is exactly what this reports.
	out.DelayAfterHeadersMS = a.DelayAfterHeaders.Duration().Milliseconds()

	if kind == scenario.FaultNone {
		// A trailing "status: 200" attempt renders the scenario response. A
		// delay-only, or delay-after-headers-only, attempt still faulted it.
		if a.Delay > 0 || a.DelayAfterHeaders > 0 {
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

// preDispatchDelay waits out attempt's Delay under mode before Handle journals
// a non-streaming exchange or touches the socket, returning ctx.Err() when the
// client's own deadline or cancellation ends the request first. A stream is
// already journaled by the time this runs — its entry was appended before the
// first byte, and its closer amends CompletedAt at close — so for a stream
// this only paces the time to first byte, never the journal stamp. It lives in
// Handle's call path, not execute's: for a non-streaming aborting fault the
// hang has to finish BEFORE record() runs, so that CompletedAt (and
// logRequest's duration_ms) measures the observed delay instead of the instant
// the attempt was decided — execute itself is only ever reached once the
// socket is about to be touched, which is too late to still be the
// pre-dispatch wait.
//
// stream_stall is carved out because its Delay is not a time-to-first-byte
// wait at all: planStream folds it into chunk AfterChunk's own pace instead
// (docs/design/streaming.md §3.1). Running the generic sleep for it too would
// sleep Delay TWICE — once here, before headers, and again inside
// executeStream before chunk AfterChunk.
func preDispatchDelay(ctx context.Context, a *scenario.FaultAttempt, mode DelayMode) error {
	if a == nil || a.Delay <= 0 || a.EffectiveKind() == scenario.FaultStreamStall {
		return nil
	}
	return sleep(ctx, a.Delay.Duration(), mode)
}

// execute applies a fault to the wire and returns the completed outcome (out
// with BytesWritten filled in). By the time Handle calls this, the
// pre-dispatch delay has already run (see preDispatchDelay) and, for a
// non-streaming aborting fault not carrying delay_after_headers, the entry is
// already journaled — execute's only remaining job is to touch the socket.
// mode is still threaded through to executeStream, which paces chunks with
// sleep after the first byte, and to afterHeadersDelay below, which paces the
// one non-streaming hang every writer can carry. For aborting kinds it does
// not return: it panics with http.ErrAbortHandler, and Handle's deferred
// record — already run for those kinds, unless recordAbort was deferred to
// truncateBody below — is a no-op before the re-panic.
//
// recordAbort journals the aborting entry (Handle's closure over entry/out/
// record). Handle always passes its closure here, never nil; it is nil only
// for a direct caller outside Handle, such as a unit test. truncateBody
// below is the only writer that ever calls it: headers ARE the socket being
// touched, so a truncate_body attempt that also hangs after them cannot be
// journaled before execute runs at all, and Handle passes its closure in
// unrecorded for that one combination — every other aborting kind has
// already had it called before execute runs, which makes truncateBody's own
// call a no-op re-record (record is idempotent) rather than the first and
// only one. See Handle's own comment at the call site for the full reasoning
// and the alternatives that were rejected.
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
	resp Response, mode DelayMode, out journal.Outcome, closer func(journal.StreamClose), recordAbort func(),
) journal.Outcome {
	if resp.Stream != nil {
		return executeStream(ctx, w, a, resp, mode, out, closer)
	}

	if a == nil {
		n, _ := writeResponse(ctx, w, resp.Status, resp.Header, contentTypeOf(resp), resp.Body, nil, mode)
		out.BytesWritten = n
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
		if err := truncateBody(ctx, w, a, resp, body, status, header, mode, recordAbort); err != nil {
			// The client's own deadline or cancellation ended the request during
			// the after-headers hang, before the partial write. Nothing more is
			// written, and recordAbort was never called — see truncateBody.
			out.Aborted = true
			out.BytesWritten = 0
			return out
		}
		panic(http.ErrAbortHandler)

	case scenario.FaultEmptyBody:
		applyHeader(w, header)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(statusOr(status))
		if err := afterHeadersDelay(ctx, w, a, mode); err != nil {
			out.Aborted = true
			out.BytesWritten = 0
			return out
		}
		out.BytesWritten = 0
		return out

	case scenario.FaultOversizedBody:
		n, err := writeOversizedBody(ctx, w, status, header, body, a.BodyBytes, a, mode)
		if err != nil {
			out.Aborted = true
			out.BytesWritten = 0
			return out
		}
		out.BytesWritten = n
		return out

	default:
		n, err := writeResponse(ctx, w, status, header, header.Get("Content-Type"), body, a, mode)
		if err != nil {
			out.Aborted = true
			out.BytesWritten = 0
			return out
		}
		out.BytesWritten = n
		return out
	}
}

// afterHeadersDelay is the one implementation of "headers are already on the
// wire, now hang before the rest": every non-streaming writer that can carry
// delay_after_headers calls this exactly once, immediately after
// WriteHeader, so the flush-then-sleep pair is never duplicated. A nil a or a
// non-positive DelayAfterHeaders returns nil immediately without touching the
// flusher, so a writer that carries no after-headers delay pays nothing extra
// and behaves exactly as it did before this modifier existed.
//
// The explicit Flush is what makes "headers reach the client before the
// hang" true rather than aspirational: net/http buffers a small response and
// only commits Content-Length/headers to the wire once it has seen enough of
// the body to decide the framing, or once something forces the issue.
// Flushing before any body byte is written forces it now, which is the
// entire point — the client must be able to observe that the response has
// started and is not yet finished.
//
// Returns ctx.Err() when the client's own deadline or cancellation ends the
// request during the hang, exactly as sleep does; every caller below turns
// that into Outcome.Aborted=true, BytesWritten=0 rather than writing a body
// into a connection nothing is reading from any more.
//
// Trickle/slow-drip bodies (deferred; docs/adopter-backlog.md Phase 6) are a
// different fault sitting on this same after-headers seam: headers, then
// several paced partial writes instead of one hang and one write. Out of
// scope here.
func afterHeadersDelay(ctx context.Context, w http.ResponseWriter, a *scenario.FaultAttempt, mode DelayMode) error {
	if a == nil || a.DelayAfterHeaders <= 0 {
		return nil
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return sleep(ctx, a.DelayAfterHeaders.Duration(), mode)
}

// faultBody returns the bytes a faulted response writes. The order is deliberate:
// a raw body override wins outright because it is the only way to send bytes that
// are not JSON, then the provider's own error shape, then the attempt's declared
// body, and only then the rendered scenario body.
//
// resp.FaultBody is called only when the attempt actually applies — its own
// Status is >= 400, or it declares an explicit Body of its own to hand the
// provider's renderer — which is what Response.FaultBody's doc comment has
// always promised and, before this unit, three of the four in-tree provider
// packages had to re-implement by hand (`if a.Status < 400 { return nil }`
// at the top of their own FaultBody). A `{status: 200}` (or bare `- {}`)
// attempt on any profile, including one written by an author who never
// copied that guard, now serves the scenario body with no fault body: the
// call this guard exists to prevent — a scripted 200 wrapped in a
// 4xx-shaped envelope — is unconstructible rather than merely
// convention-avoided.
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
	switch {
	case resp.FaultBody != nil && (len(a.Body) > 0 || a.Status >= http.StatusBadRequest):
		if b := resp.FaultBody(*a); b != nil {
			body = b
		}
	case len(a.Body) > 0:
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
// one. truncate_body and oversized_body are deliberately absent: they are the
// two non-streaming kinds whose interaction with a streaming exchange is
// reported rather than silently reinterpreted, because each sets an exact
// Content-Length over a JSON body in a way none of these six kinds do (see
// Handle's mirror-case handling and scenario.CodeStreamFaultMismatch).
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
		EventNames:    plan.eventNames(),
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

// truncateBody declares the full length, sends a prefix, then aborts. When a
// carries delay_after_headers, the sequence is headers -> flush -> hang ->
// recordAbort -> partial write -> flush -> reset, and a client cancellation
// during the hang returns a non-nil error with NOTHING further written —
// recordAbort is never called in that case, which is the property Handle's
// caller relies on: the deferred record (Handle's top-level defer) is then
// the one that lands, exactly as it already does for a client cancellation
// during the pre-dispatch hang.
//
// recordAbort journals the entry immediately before the destructive write —
// see execute's doc comment for why that timing, not "before headers", is
// what this specific combination needs. It is called unconditionally
// whenever non-nil and the hang (if any) completed without cancellation:
// record is idempotent, so a caller that already journaled this entry before
// execute ran (every case except truncate_body+delay_after_headers) passing
// its own closure through here anyway would cost one no-op call, not a
// second entry. A nil recordAbort — a direct caller outside Handle, for
// instance a unit test — is simply skipped.
func truncateBody(ctx context.Context, w http.ResponseWriter, a *scenario.FaultAttempt, resp Response,
	body []byte, status int, header http.Header, mode DelayMode, recordAbort func(),
) error {
	n := truncationLen(a, resp)
	applyHeader(w, header)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(statusOr(status))

	if err := afterHeadersDelay(ctx, w, a, mode); err != nil {
		return err
	}

	if recordAbort != nil {
		recordAbort()
	}

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
	return nil
}

// oversizedBodyPaddingChunk is the fixed-size buffer FaultOversizedBody reuses
// to reach BodyBytes without ever allocating BodyBytes: a scenario asking for
// 512 MiB must cost the process this one 64 KiB buffer, not 512 MiB. Its
// content is a single insignificant JSON whitespace byte (space) repeated —
// every JSON decoder accepts trailing whitespace after a complete top-level
// value, so appending this after the rendered body changes nothing about the
// decoded value, only its length on the wire.
var oversizedBodyPaddingChunk = bytes.Repeat([]byte{' '}, 64*1024)

// writeOversizedBody writes body, then enough bytes from
// oversizedBodyPaddingChunk to bring the total to at least bodyBytes, and
// returns the total byte count written (body plus padding). If body is
// already >= bodyBytes, no padding is written at all — the scenario asked
// for "at least this many bytes", not "exactly".
//
// Content-Length is declared exactly (len(body) plus the padding length)
// BEFORE any Write call: padding is written in several Write calls, and
// without a declared length net/http falls back to chunked transfer
// encoding once headers are already committed, which is invisible to a
// Content-Length-based size gate — the one thing this fault exists to
// exercise. That declaration happens before afterHeadersDelay's own flush
// too, so a client hung after headers still sees the true, final
// Content-Length, not a value chunked encoding would have replaced.
//
// A carries the optional delay_after_headers hang, applied by
// afterHeadersDelay immediately after WriteHeader and before the first body
// byte; a non-nil error means the client's own deadline or cancellation
// ended the request during that hang, and nothing was written. Ordinary
// Write errors past that point are ignored the same way writeResponse
// ignores them: this is a simulator serving a local socket, not a
// production server that needs to log a client hanging up mid-write.
func writeOversizedBody(ctx context.Context, w http.ResponseWriter, status int, header http.Header, body []byte,
	bodyBytes int, a *scenario.FaultAttempt, mode DelayMode,
) (int, error) {
	padding := bodyBytes - len(body)
	if padding < 0 {
		padding = 0
	}

	applyHeader(w, header)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)+padding))
	w.WriteHeader(statusOr(status))

	if err := afterHeadersDelay(ctx, w, a, mode); err != nil {
		return 0, err
	}

	written, err := w.Write(body)
	if err != nil {
		return written, nil
	}
	for padding > 0 {
		chunk := oversizedBodyPaddingChunk
		if padding < len(chunk) {
			chunk = chunk[:padding]
		}
		n, err := w.Write(chunk)
		written += n
		padding -= n
		if err != nil || n == 0 {
			// n == 0 with a nil error is not something net/http's writer does,
			// but a writer that did would otherwise spin here forever.
			break
		}
	}
	return written, nil
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

// writeResponse writes the headers, status and, unless the after-headers
// hang is cancelled by the client first, the body — returning the byte
// count written and, on cancellation, sleep's error. a and mode exist only to
// run afterHeadersDelay; a nil a or a zero DelayAfterHeaders means no hang,
// and this behaves exactly as it did before the modifier existed. This is
// the writer for close_before_headers's siblings that carry no transport
// change of their own — none, status, extra_fields, wrong_content_type,
// invalid_json — and for the unfaulted (a == nil) path.
//
// When a hang is about to run, Content-Length is declared explicitly before
// WriteHeader, exactly as truncateBody and writeOversizedBody already
// declare theirs: without it, afterHeadersDelay's Flush commits the headers
// before net/http has seen the whole body to infer a length from, and
// net/http falls back to Transfer-Encoding: chunked — a wire-framing change
// this modifier must not cause. The un-delayed path is left exactly as it
// was: net/http's own inference already matches what an explicit
// Content-Length here would say, and this function must not become a second
// place that decides framing for the common case.
func writeResponse(ctx context.Context, w http.ResponseWriter, status int, header http.Header, contentType string,
	body []byte, a *scenario.FaultAttempt, mode DelayMode,
) (int, error) {
	applyHeader(w, header)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if a != nil && a.DelayAfterHeaders > 0 {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.WriteHeader(statusOr(status))
	if err := afterHeadersDelay(ctx, w, a, mode); err != nil {
		return 0, err
	}
	n, _ := w.Write(body)
	return n, nil
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
