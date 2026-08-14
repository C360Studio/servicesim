package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/c360studio/servicesim/internal/httpx"
	"github.com/c360studio/servicesim/internal/journal"
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
)

// Handle wraps h with the shared lifecycle and returns an http.HandlerFunc:
// sequence claim, arrival stamp, bounded body read through httpx.ReadBody, JSON
// decode through httpx.DecodeObject, credential observation through
// httpx.Observe, handler call, fault selection through Deps.Faults, fault
// execution, journal append and one structured log event.
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
//  3. An aborting fault is journaled *before* the socket is touched, and record
//     is idempotent. The client observes the reset while this goroutine is still
//     unwinding, so a test that read the journal at that moment would otherwise
//     see nothing — intermittently, and more often under -race.
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
		x := &Exchange{
			Deps: d, Provider: p, Route: route, Request: r,
			Seq: d.Journal.Next(), ArrivedAt: d.Clock.Now(),
			decision: FaultDecision{Index: -1, Key: route.FaultKey},
		}

		entry := journal.Entry{
			Provider: string(p), Seq: x.Seq, Method: r.Method,
			// Path is r.URL.Path and Query is r.URL.RawQuery — never the raw request
			// target and never r.URL.String(), both of which render userinfo
			// verbatim for an absolute-form target. journal.Redact masks the query.
			Path: r.URL.Path, Query: r.URL.RawQuery,
			Route: route.Pattern, RemoteAddr: r.RemoteAddr, ArrivedAt: x.ArrivedAt,
		}

		appended := false
		record := func() {
			if appended {
				return
			}
			appended = true
			entry.CompletedAt = d.Clock.Now()
			entry.Findings = x.Findings()
			entry.Headers = r.Header
			entry.Body = json.RawMessage(x.Raw)
			entry.Auth = x.Auth

			// Redact BEFORE the logger sees it. Append redacts too, but Append takes
			// Entry by value, so the local entry would still hold the raw
			// Authorization header when logRequest ran. journal.Redact is idempotent
			// precisely so it can be called at both points.
			entry = journal.Redact(entry)
			d.Journal.Append(entry)
			logRequest(d.Logger, entry)
		}

		defer func() {
			rec := recover()
			record()
			if rec != nil {
				panic(rec)
			}
		}()

		readRequest(x, &entry)
		if creds := httpx.ExtractCredentials(r); len(creds) > 0 {
			x.Auth = httpx.Observe(creds[0], true)
		}

		resp := h(x)
		if x.Failed() {
			// A handler that left FaultEligible set on a rejected request would
			// otherwise consume a retry budget and journal the rejection as though
			// it were the scenario's own response. Validation has the last word.
			resp.FaultEligible = false
		}

		if resp.FaultEligible {
			if dec := x.Fault(); dec.Unknown {
				x.Warn(CodeUnknownFaultKey, "", "no fault plan registered for key %q", dec.Key)
			}
		}
		dec := x.decision

		out := faultOutcome(dec, resp)
		if x.HasFinding(CodeUnmatched) || x.HasFinding(CodeMethodNotAllowed) {
			out.Kind = journal.OutcomeUnmatched
		}
		if out.Aborted {
			entry.Outcome = out
			record() // journal BEFORE the client can observe the abort
		}
		entry.Outcome = execute(r.Context(), w, dec.Attempt, resp, d.DelayMode, out)
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
