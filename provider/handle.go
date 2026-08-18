package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/c360studio/servicesim/internal/httpx"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/scenario"
)

// Finding codes the shared lifecycle raises. Provider packages map them onto
// their own status codes and error bodies; §6.2 of the package design is the
// table.
const (
	// CodeBodyTooLarge is raised when the request body exceeds Deps.MaxRequestBytes.
	CodeBodyTooLarge = "request.body_too_large"

	// CodeBodyRead is raised when the request body could not be read at all — a
	// transport failure, not an authoring one. It quotes nothing: a wrapped
	// transport error can carry the request target, and an absolute-form target
	// carries userinfo.
	CodeBodyRead = "request.body_read"

	// CodeMalformedJSON is raised when a non-empty body is not valid JSON.
	CodeMalformedJSON = "request.malformed_json"

	// CodeBodyNotObject is raised when a body is valid JSON but not a JSON object.
	CodeBodyNotObject = "request.body_not_object"

	// CodeUnknownFaultKey is raised when the fault engine holds no plan for this
	// route's key, which means a route was added without being registered.
	CodeUnknownFaultKey = "fault.unknown_key"

	// CodeNamespaceLimit is raised when a request names a namespace that would
	// put the process over MaxNamespaces.
	//
	// It is an ERROR, not a warning, and the request is refused. Reporting it any
	// more gently was tried and is worse: the engine logged the refusal loudly
	// and served a 200 anyway, so a test in the refused namespace saw success,
	// collected no journal entries, and failed later on an assertion counting
	// requests that were never recorded. The cause was visible only in the
	// simulator's own stderr, which is the one place a consumer's test output
	// does not reach.
	CodeNamespaceLimit = "namespace.limit_exceeded"

	// CodeUnmatched is raised by the catch-all handler for an unknown path.
	CodeUnmatched = "route.unmatched"

	// CodeMethodNotAllowed is raised for a known path with an unsupported method.
	CodeMethodNotAllowed = "route.method_not_allowed"

	// CodeNoMatchingTurn is raised when no turn of a provider's script matches the
	// request and the script declares no unconditional fallback.
	CodeNoMatchingTurn = "scenario.no_matching_turn"

	// CodeNoHandler is raised when a route was registered with no handler. It is
	// unreachable through NewMux and exists so a direct Handle caller gets a
	// journaled failure rather than a nil dereference.
	CodeNoHandler = "route.no_handler"

	// CodeAttemptOnRejection is raised when a handler claimed a fault attempt
	// (via x.Fault, x.CallIndex, or a turn selector such as SelectTurnFor) and the
	// request was then rejected before that claimed decision could be applied.
	// CONTRIBUTING.md's "validate before you claim" convention names the ordinary
	// way a handler avoids this, but SelectTurnFor (scenario.no_matching_turn) and
	// MintJob (job.limit_reached) both claim before they can know whether the
	// request will be rejected, by design — so this branch is the ordinary path
	// for those, not only a fallback for a handler that broke convention. It is a
	// WARNING, not an error: the request is already rejected on its own terms,
	// and this finding exists only to make the claim visible, because Handle
	// discards the attempt rather than applying it. See the doc comment above
	// "Validation has the last word" in Handle for the mechanism and its known
	// limitation.
	CodeAttemptOnRejection = "fault.attempt_on_rejection"
)

// Handle wraps h with the shared lifecycle and returns an http.HandlerFunc:
// sequence claim, arrival stamp, bounded body read through httpx.ReadBody, JSON
// decode through httpx.DecodeObject, credential observation through
// httpx.Observe, lane resolution, handler call, fault selection through
// Deps.Faults, fault execution, journal append and one structured log event.
//
// Lane resolution happens once, before the handler runs, and is what fault
// selection and turn selection both key on. It is placed after the body read
// because a body_json turn-key extractor needs the body, and before the handler
// because the handler may claim an attempt on the first line it executes.
//
// Its namespace half is settled earlier still, before the sequence claim, since
// the sequence must be drawn from the counter of the lane that will retain the
// entry. That needs only the path prefix and one header, so nothing waits on the
// body for it.
//
// Four properties of the body below are load-bearing and are not stylistic; see
// §2.2 of the package design:
//
//  1. The journal append is in a defer, because transport faults abort the
//     handler by panicking with http.ErrAbortHandler and their entries would
//     otherwise be lost — exactly the cases a fault test cares about.
//  2. The recover re-panics. http.ErrAbortHandler is a sentinel net/http
//     interprets; swallowing it turns a connection-abort fault into a 200 with an
//     empty body.
//  3. An aborting fault is journaled before the connection is destroyed — after
//     every hang, before the abort — and record is idempotent. The client
//     observes the reset while this goroutine is still unwinding, so a test
//     that read the journal at that moment would otherwise see nothing —
//     intermittently, and more often under -race. The one exception is
//     truncate_body carrying delay_after_headers, where the record cannot
//     move before the headers (headers ARE the socket being touched) and
//     instead runs from inside truncateBody, after the after-headers hang and
//     immediately before the destructive write; see deferAbortRecord below.
//  4. The entry is redacted where it is built, not only where it is stored.
//     Journal.Append takes Entry by value, so redacting only inside Append leaves
//     this copy — the one the logger is about to serialise — holding the raw
//     Authorization header.
func Handle(d Deps, p Name, route Route, h Handler) http.HandlerFunc {
	d = d.Normalized()
	if h == nil {
		h = noHandler
	}

	return func(w http.ResponseWriter, r *http.Request) {
		// The prefix is parsed before anything else because the journal records the
		// path as received, not the stripped path routing matched on.
		prefix := requestLanePrefix(r)

		// The namespace is settled before the sequence is claimed, because the
		// sequence has to come from the counter of the lane that will retain the
		// entry. Namespaces are a state boundary and journal sequence numbers are
		// part of that state, so two tests sharing one container each see 1, 2, 3
		// rather than halves of one interleaved sequence.
		//
		// Only the namespace can be settled this early. The rest of the lane — the
		// route key and any body_json turn-key extractor — needs the body and is
		// resolved by resolveLane below. The rejection finding is raised there too,
		// where the exchange exists to carry it.
		namespace, _ := requestNamespace(r, prefix)

		x := &Exchange{
			Deps: d, Provider: p, Route: route, Request: r,
			Seq: journal.NextIn(d.Journal, namespace), ArrivedAt: d.Clock.Now(),
			decision: FaultDecision{Index: -1, Key: route.FaultKey},
			// resolveLane fills in the rest of the lane below. Seeding the namespace
			// with the value the sequence was drawn from means a request that never
			// reaches resolveLane still journals the lane it was counted in, rather
			// than an empty string a reader would have to interpret.
			lane: Lane{Namespace: namespace},
		}

		entry := journal.Entry{
			Provider: string(p), Seq: x.Seq, Method: r.Method,
			// Path is the received path — r.URL.Path before the mux stripped any
			// /x/ or /n/ prefix — and Query is r.URL.RawQuery. Never the raw request
			// target and never r.URL.String(), both of which render userinfo
			// verbatim for an absolute-form target. journal.Redact masks the query.
			Path: prefix.path, Query: r.URL.RawQuery,
			Route: route.Pattern, RemoteAddr: r.RemoteAddr, ArrivedAt: x.ArrivedAt,
		}

		appended := false
		record := func() {
			if appended {
				return
			}
			appended = true
			entry.CompletedAt = d.Clock.Now()
			entry.Namespace = x.lane.Namespace
			entry.Findings = x.journalFindings()
			entry.Headers = r.Header
			entry.Body = json.RawMessage(x.Raw)
			entry.Auth = x.auth

			// Redact BEFORE the logger sees it. Append redacts too, but Append takes
			// Entry by value, so the local entry would still hold the raw
			// Authorization header when logRequest ran. journal.Redact is idempotent
			// precisely so it can be called at both points. d.CredentialNames widens
			// the vocabulary by whatever the registered profiles declared (house
			// rule 4); Append widens it the same way via the Ring's own
			// Limits.CredentialNames, so both redaction points see one union.
			entry = journal.Redact(entry, d.CredentialNames...)
			d.Journal.Append(entry)
			logRequest(d.Logger, entry)
		}

		// resp and closer are declared here, above the deferred recover/record
		// block, and assigned (never re-declared with :=) below. The deferred
		// closure captures them by reference at the point the defer statement
		// runs, which is before either is set to anything meaningful — a
		// variable declared after the defer statement is simply not visible to
		// it. Easy to miss because the code reads correctly in isolation and
		// fails to compile only once assembled.
		var resp Response
		var closer func(journal.StreamClose)

		// closeStream applies the OBSERVED half of a streamed exchange to the
		// entry that record() already appended. It is idempotent for the same
		// reason record is: executeStream calls it before returning, and the
		// deferred fallback below calls it again for a request that never got
		// there (a panic mid-stream, once a fault kind that can produce one
		// exists) or that fell through without executeStream ever running.
		closed := false
		closer = func(c journal.StreamClose) {
			if closed {
				return
			}
			closed = true
			c.CompletedAt = d.Clock.Now()
			if !journal.CloseStreamIn(d.Journal, x.lane.Namespace, x.Seq, c) {
				// Plain Warn, not a warn-once helper: no such helper exists in this
				// repository, and the closed guard above already bounds this to at
				// most one line per request. A consumer whose Journal does not
				// implement StreamCloser wants the line every time, because every
				// entry it affects is one that will stay "open" forever.
				d.Logger.Warn("journal.stream_not_amendable", slog.Uint64("seq", x.Seq))
			}
		}

		defer func() {
			rec := recover()
			record()
			if resp.Stream != nil {
				// BytesWritten and ChunksSent are left at their zero value
				// here deliberately: this fallback is reachable only when
				// executeStream never ran at all (the pre-dispatch delay was
				// cancelled before dispatch — see the preDispatchDelay error
				// branch below, where resp.Stream != nil), where zero is
				// correct because nothing was written. executeStream's own
				// abort branches — stream_disconnect
				// and stream_truncate_chunk — call closer with the real
				// counts BEFORE panicking (closeWith, stream.go), exactly as
				// the design's §4.3 requires, so this fallback stays a true
				// fallback and never clobbers real byte counts with zero: the
				// closed guard above makes it a no-op whenever executeStream
				// already reported.
				closer(journal.StreamClose{State: journal.StreamClientGone})
			}
			if rec != nil {
				panic(rec)
			}
		}()

		readRequest(x, &entry)
		// Every presented placement is recorded, not just the first. A request
		// carrying both a Bearer header and an x-api-key is a real client
		// misconfiguration, and a journal that showed only one of them could not
		// be used to prove which placements an adapter actually sends.
		if creds := httpx.ExtractCredentials(r); len(creds) > 0 {
			x.auth = httpx.ObserveAll(creds)
		}

		// One resolution, here, after the body is readable and before the handler
		// runs. Fault selection and turn selection both read x.Lane(); neither may
		// derive it, because two derivations are two chances to disagree about
		// which lane — and therefore which call — this request is.
		resolveLane(x, prefix)

		// Admission is asked HERE, before the handler runs, because a refusal has
		// to become the provider's own error envelope. Asking at fault-claim time
		// is too late: the handler has already produced a body by then, and the
		// only reachable outcome is a 200 the client cannot distinguish from a
		// served response.
		if admitter, ok := d.Faults.(NamespaceAdmitter); ok {
			if ns := x.Lane().Namespace; !admitter.AdmitNamespace(ns) {
				x.Fail(CodeNamespaceLimit, "",
					"namespace %q cannot be served: the process is at its --max-namespaces bound", ns)
			}
		}

		resp = h(x)

		// The SSE grammar vocabulary is open (Phase 10 unit 2, house rule 3):
		// the framework can no longer default a missing Grammar to one
		// dialect's framing, so a profile that forgets to set it fails loudly
		// — RefuseInternal, not a silent fallback — rather than a third-party
		// grammar silently inheriting behaviour nobody asked for. This must
		// run before the suppression/fault logic below, which assumes
		// resp.Stream != nil means "this exchange WILL stream": replacing
		// resp here, rather than mutating it, is what keeps that true.
		if resp.Stream != nil && resp.Stream.Grammar == "" {
			x.Fail(CodeStreamGrammarMissing, "",
				"route %q returned a Stream with an empty Grammar; the SSE grammar vocabulary is open, "+
					"so a stream must declare its own", route.Pattern)
			resp = Response{
				Status: http.StatusInternalServerError,
				Body:   x.refuse(RefuseInternal, http.StatusInternalServerError),
				Label:  "provider.stream_grammar_missing",
			}
		}

		if x.Failed() {
			// A handler that left FaultEligible set on a rejected request would
			// otherwise journal the rejection as though it were the scenario's
			// own response. Validation has the last word — but "the last word"
			// is enforced here, not by every handler's own ordering: the branch
			// below is what stops a decision already CLAIMED (by the handler
			// calling x.Fault or x.CallIndex, or by a turn selector such as
			// SelectTurnFor, or by MintJob) before the request was rejected
			// from reaching the wire anyway. A handler is still expected to
			// validate before it claims (CONTRIBUTING.md) where it can, but
			// SelectTurnFor and MintJob claim first BY DESIGN — they cannot
			// know the request will be rejected until the claimed index tells
			// them so — which makes this branch their ordinary path, not a
			// fallback for a convention violation.
			resp.FaultEligible = false
		}

		switch {
		case resp.FaultEligible:
			if dec := x.Fault(); dec.Unknown {
				x.Warn(CodeUnknownFaultKey, "", "no fault plan registered for key %q", dec.Key)
			}
		case x.claimed && x.decision.Index >= 0:
			// An attempt was claimed — from Deps.Faults, on this (possibly
			// namespaced) lane's cursor key — and this response is not
			// fault-eligible: either the request was rejected (x.Failed()) or
			// the handler opted a served response out of faults directly,
			// without failing (a documented way to serve a real success
			// outside the fault plan). Deps.Faults.Next already incremented
			// its counter for that key, and that budget is NOT returned here:
			// releasing a claimed lane touches the fault engine's counter under
			// concurrency and is deferred by design (framework-seam.md rule 5)
			// until a real out-of-tree profile trips this warning — a later,
			// separately spiked unit, not this one.
			//
			// What unit 0 guarantees is narrower: only the claimed Attempt is
			// cleared here, before faultOutcome or execute (both below) can
			// see it, so no fault is ever applied to the wire response.
			// Index and Key are deliberately left exactly as claimed —
			// Deps.Faults really did hand out that index on that key, so the
			// journal must keep saying so. testkit's namespace-isolation and
			// attempt-budget assertions read Outcome.FaultKey and
			// Outcome.AttemptIndex to audit precisely this, and a Key or
			// Index that did not match the counter actually drawn on would
			// make those assertions either miss a real budget gap or flag a
			// namespace collision that never happened.
			//
			// The x.decision.Index >= 0 guard excludes an Unknown-key claim
			// (Index -1, nothing consumed, see FaultDecision.Unknown): x.Fault
			// still sets x.claimed on that path, but there is no claimed
			// attempt to speak of, so no warning is owed.
			x.decision.Attempt = nil
			x.Warn(CodeAttemptOnRejection, "",
				"fault attempt %d on key %q was claimed but this response is not fault-eligible; "+
					"the claimed attempt was not applied and its index stays spent",
				x.decision.Index, x.decision.Key)
		}
		dec := x.decision

		// Suppression is decided HERE — before faultOutcome, before the journal
		// condition, before anything else reads resp.Stream — so resp.Stream !=
		// nil means "this exchange WILL stream" everywhere downstream. Deciding
		// it inside execute instead would leave every one of those readers
		// looking at the outer resp, which a local reassignment inside execute
		// would never touch: see docs/design/streaming.md §4.4.
		attempt := dec.Attempt
		var scriptedUnreachableKind string
		switch {
		case attempt != nil && resp.Stream != nil && suppressesStream(attempt.EffectiveKind()):
			// A real vendor does not answer stream: true with a 429 wrapped in
			// SSE: the error happens before the stream starts, so the response
			// becomes an ordinary JSON error and the fault executes exactly as
			// it always has.
			resp = suppressStream(resp)
		case attempt != nil && resp.Stream != nil &&
			(attempt.EffectiveKind() == scenario.FaultTruncateBody || attempt.EffectiveKind() == scenario.FaultOversizedBody):
			// The mirror case: this exchange WILL stream, but the claimed attempt
			// cannot apply to it — truncate_body and oversized_body both set a
			// Content-Length that is wrong for a chunked SSE body. Reported,
			// never silently absorbed into a plain 200: see
			// scenario.CodeStreamFaultMismatch, the load-time guard this catches
			// when validation was skipped.
			x.Fail(scenario.CodeStreamAbortUnreachable, "",
				"fault attempt %q cannot apply to this exchange, which will stream; "+
					"it assumes an ordinary JSON body it can set an exact Content-Length over", attempt.EffectiveKind())
			scriptedUnreachableKind = string(attempt.EffectiveKind())
			attempt = nil
		case attempt != nil && resp.Stream == nil && attempt.EffectiveKind().IsStream():
			// The OTHER mirror case: a stream_* kind was claimed at turn
			// selection, but THIS request will not stream. An entry's policy
			// answers "does this surface serve a stream when asked", never
			// "does this call ask" — the preamble is explicit that a
			// consumer may send stream: true on one call and stream: false
			// on the next in the same lane, so a stream_* attempt claimed by
			// a call that did not ask to stream is reachable even under a
			// fully valid, load-time-clean stream-policy entry. Reported,
			// never silently served as a plain 200.
			x.Fail(scenario.CodeStreamAbortUnreachable, "",
				"fault attempt %q cannot apply to this exchange, which will not stream; "+
					"it assumes a chunked SSE body", attempt.EffectiveKind())
			scriptedUnreachableKind = string(attempt.EffectiveKind())
			attempt = nil
		case attempt != nil && resp.Stream != nil && attempt.DelayAfterHeaders > 0:
			// A third mirror case, Phase 6 unit 5: this exchange WILL stream,
			// and the claimed attempt's own kind does not suppress that (a
			// suppressing kind already matched the first case above and never
			// reaches here) — but it carries delay_after_headers, which
			// assumes an ordinary JSON body to hang before writing. A chunked
			// SSE body has no such point; stream_stall with after_chunk: 0 is
			// the streaming equivalent. Reported, never silently ignored: a
			// scenario that skipped validation would otherwise have this
			// modifier vanish with no trace.
			// label names the MODIFIER, not the kind, whenever the kind is
			// empty (a bare delay_after_headers attempt, EffectiveKind() ==
			// scenario.FaultNone == ""): the finding and the restored
			// FaultKind below both name what was actually asked for —
			// "delay", matching faultOutcome's own label for the same shape
			// on the non-streaming path — rather than the empty string a
			// raw %q on FaultNone would otherwise render.
			label := string(attempt.EffectiveKind())
			if label == "" {
				label = faultKindDelay
			}
			x.Fail(scenario.CodeStreamAbortUnreachable, "",
				"fault attempt %q carries delay_after_headers, which cannot apply to this exchange, which will "+
					"stream; it assumes an ordinary JSON body to hang after headers on", label)
			scriptedUnreachableKind = label
			attempt = nil
		}

		effectiveDec := dec
		effectiveDec.Attempt = attempt
		out := faultOutcome(effectiveDec, resp)
		if scriptedUnreachableKind != "" {
			// faultOutcome, given a nil attempt, reports FaultNone: Aborted stays
			// false and no DelayMS is applied, because the attempt's Delay, if
			// any, is scoped to the stream that never happens. FaultKind alone is
			// restored so a reader can see what was asked for.
			out.FaultKind = scriptedUnreachableKind
		}
		if x.HasFinding(CodeUnmatched) || x.HasFinding(CodeMethodNotAllowed) {
			out.Kind = journal.OutcomeUnmatched
		}
		// The journal-visibility rule generalises: today's rule is "an aborting
		// fault is journaled before the socket is touched"; a stream makes that
		// true of every response, because the client consumes chunk 0 seconds
		// before the handler returns. The aborting case was always the special
		// case; streaming is the general one.
		//
		// For a stream the entry is recorded HERE, before the pre-dispatch delay
		// runs at all: closer (above) amends CompletedAt when the exchange
		// actually closes, exactly as streaming.md §5.1 describes, so an early
		// CompletedAt here is provisional by design. For a non-streaming
		// exchange record() is NOT called here — it is deferred until after the
		// delay below, which is the Phase 6 unit 1 fix: CompletedAt must measure
		// the observed hang, not the instant this attempt was decided.
		if resp.Stream != nil {
			entry.Outcome = out
			record()
		}

		// Every non-streaming kind that carries a delay waits here, before
		// anything is journaled or written. For a non-streaming aborting fault
		// that is the Phase 6 unit 1 fix — record() used to run before this
		// sleep — so CompletedAt (and logRequest's duration_ms) now reflect the
		// hang the client actually observed. Only stream_stall's Delay is
		// carved out inside preDispatchDelay, folded into a chunk's own pace
		// instead.
		if err := preDispatchDelay(r.Context(), attempt, d.DelayMode); err != nil {
			// The client's own deadline or cancellation ended the request while the
			// delay was still running. Writing now would be shouting into a closed
			// socket; the journal records that nothing was delivered.
			//
			// out.Aborted is set here only for a NON-streaming exchange, where this
			// Outcome is what gets retained. For a STREAMING exchange, record()
			// already ran above with Aborted reflecting the SCRIPT — nothing about
			// the script aborted this, the client's own cancellation did — so
			// Aborted must stay false there too; State (amended to client_gone via
			// closer) is the authoritative signal for a streamed exchange instead.
			if resp.Stream == nil {
				out.Aborted = true
			}
			out.BytesWritten = 0
			entry.Outcome = out
			return // the deferred record() (and, for streams, the fallback closer) stamps CompletedAt now.
		}

		// recordAbort journals the aborting entry. It is called from here
		// directly for every aborting shape except one (deferAbortRecord,
		// below), and passed into execute either way so FaultTruncateBody's
		// writer can call it itself when the record has to wait on the
		// after-headers hang. record() is idempotent, so a call from both
		// places would cost a no-op, never a second entry.
		recordAbort := func() {
			entry.Outcome = out
			record()
		}

		// Aborting, non-streaming faults (close_before_headers, truncate_body) are
		// normally journaled here, after the pre-dispatch hang and before the
		// socket is touched — the property package-design.md §2.2 rule 3
		// requires: the client cannot observe the reset before this entry
		// exists.
		//
		// truncate_body carrying delay_after_headers is the one exception,
		// Phase 6 unit 5: headers ARE the socket being touched, so the record
		// cannot move before them the way the pre-dispatch hang's record
		// does, and it still has to wait for the AFTER-headers hang on top of
		// that. Deliberately, the record for that combination is deferred
		// past this point — recordAbort is passed into execute unrecorded,
		// and truncateBody (fault_exec.go) calls it itself, after the hang
		// and immediately before the destructive partial write + RST. The
		// property that matters — journaled before the client can observe
		// the abort — still holds exactly, because the partial write and RST
		// are still what follows; what changes is only that a client reading
		// the journal DURING the after-headers hang sees no entry yet, the
		// same as it already sees during any pre-dispatch hang since unit 1.
		// completed_at then observes the whole exchange. If the client
		// cancels during that hang instead, truncateBody returns before ever
		// calling recordAbort, and the deferred record() at the top of this
		// function (in the recover/record defer) is the one that lands —
		// exactly the client-cancellation case already named below, just
		// reached one hang later.
		//
		// Two rejected alternatives, so a later review does not re-open this
		// without new evidence: recording before headers and leaving
		// completed_at stale for this one shape (a permanent version of the
		// ambiguity unit 1 removed); recording before headers and amending
		// completed_at before the RST (needs a non-stream amend path the
		// journal design deliberately does not have, journal.StreamCloser is
		// narrow on purpose).
		//
		// The kind check (not just DelayAfterHeaders > 0) is deliberate and
		// safety-relevant, not merely precise: scenario.Validate rejects
		// close_before_headers+delay_after_headers together
		// (scenario.fault.delay_after_headers.no_headers), so a loaded
		// scenario can never reach this with any other aborting kind — but a
		// hand-built *scenario.FaultAttempt constructed directly in Go, past
		// validation, could. closeBeforeHeaders (below) has no recordAbort
		// parameter and never calls one; deferring for a kind it cannot
		// honour would silently lose the entry rather than merely journal it
		// one hang later.
		deferAbortRecord := out.Aborted && resp.Stream == nil &&
			attempt != nil && attempt.DelayAfterHeaders > 0 && attempt.EffectiveKind() == scenario.FaultTruncateBody
		if out.Aborted && resp.Stream == nil && !deferAbortRecord {
			recordAbort()
		}
		entry.Outcome = execute(r.Context(), w, attempt, resp, d.DelayMode, out, closer, recordAbort)
	}
}

// readRequest fills in the exchange's raw and decoded body, recording the shared
// findings §6.2 declares. An absent body is not a finding here: an unmatched
// route has no body by definition, and a route that requires one reports the
// fields it is missing, which is a better message than "malformed JSON".
func readRequest(x *Exchange, entry *journal.Entry) {
	raw, err := httpx.ReadBody(x.Request, x.Deps.MaxRequestBytes)
	switch {
	case errors.Is(err, httpx.ErrBodyTooLarge):
		x.Fail(CodeBodyTooLarge, "", "request body exceeds the %d byte limit", x.Deps.MaxRequestBytes)
		entry.BodyParseError = "request body exceeds the configured limit"
		return
	case err != nil:
		x.Fail(CodeBodyRead, "", "request body could not be read")
		entry.BodyParseError = "request body could not be read"
		return
	}

	x.Raw = raw
	if len(bytes.TrimSpace(raw)) == 0 {
		return
	}

	body, err := httpx.DecodeObject(raw)
	switch {
	case errors.Is(err, httpx.ErrNotObject):
		x.Fail(CodeBodyNotObject, "", "request body must be a JSON object")
		entry.BodyParseError = "request body is not a JSON object"
	case err != nil:
		x.Fail(CodeMalformedJSON, "", "request body is not valid JSON")
		entry.BodyParseError = "request body is not valid JSON"
	default:
		x.Body = body
	}
}

// noHandler answers a route registered without a handler. NewMux cannot produce
// one; a direct Handle caller can, and a journaled 500 beats a nil dereference.
func noHandler(x *Exchange) Response {
	x.Fail(CodeNoHandler, "", "route %q has no handler", x.Route.Pattern)
	return Response{Status: http.StatusInternalServerError, Label: "provider.no_handler"}
}

// logRequest emits one structured event per completed request. It is given the
// already-redacted entry and still logs neither headers nor body: the journal is
// where a request's content is inspected, and a log line is the surface most
// likely to be shipped somewhere with weaker access control.
func logRequest(l *slog.Logger, e journal.Entry) {
	if l == nil {
		return
	}
	attrs := []any{
		slog.String("provider", e.Provider),
		slog.Uint64("seq", e.Seq),
		slog.String("method", e.Method),
		slog.String("path", e.Path),
		slog.String("route", e.Route),
		slog.String("outcome", string(e.Outcome.Kind)),
		slog.Int("status", e.Outcome.Status),
		slog.Int64("duration_ms", e.CompletedAt.Sub(e.ArrivedAt).Milliseconds()),
		slog.Int("errors", len(e.Errors())),
		slog.Int("warnings", len(e.Warnings())),
	}
	if e.Outcome.Label != "" {
		attrs = append(attrs, slog.String("label", e.Outcome.Label))
	}
	if e.Outcome.FaultKind != "" {
		attrs = append(attrs,
			slog.String("fault_kind", e.Outcome.FaultKind),
			slog.String("fault_key", e.Outcome.FaultKey),
			slog.Int("attempt", e.Outcome.AttemptIndex))
	}
	l.Info("request", attrs...)
}
