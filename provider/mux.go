package provider

import (
	"maps"
	"net/http"
	"slices"
	"strings"
)

// MuxSpec is everything a provider package supplies to build its listener's
// mux beyond the refusal shape.
//
// NotFound and MethodNotAllowed no longer live here (Phase 10 unit 2): house
// rule 3 now makes every refusal render through a Profile's own ErrorBody
// (see Refusal and Profile.Refuse), so NewMux builds the 404 and 405
// handlers itself from the refuse function it is given, and a provider
// package no longer supplies its own.
type MuxSpec struct {
	// Routes are the provider's real routes.
	Routes []Route

	// Handlers maps Route.Pattern to the handler serving it.
	Handlers map[string]Handler
}

// NewMux builds a provider listener's mux. It is exported and lives here, rather
// than being written three times in three provider packages, because the
// registration shape is load-bearing and easy to get subtly wrong: see §5.1 of
// the package design. Its verification table (POST /search 200, GET /search 405
// with Allow, POST /nope 404, POST /search/ 404) is provider/mux_test.go's test
// table.
//
// refuse renders every framework-generated refusal — 404 for an unmatched
// path, 405 for a known path with an unsupported method — in the profile's
// own vendor shape (house rule 3). [Profile.Handler] passes its own bound
// Refuse method; a caller building a mux directly may pass any
// func(Refusal) []byte, including one built from a bare Profile's Refuse
// method for a Profile that was never registered in a Set.
//
// The method-less pattern per known path is mandatory, not stylistic. Verified on
// Go 1.26.4: with only "POST /search" and "/" registered, GET /search returns 404
// from the catch-all with Content-Type: text/plain, because "/" matched and
// ServeMux's built-in 405 path is never reached. scripts/image-smoke.sh asserts
// 405, so an implementation that skips this fails the image smoke test — and,
// worse, answers a method error with a body no consumer can parse.
//
// The returned mux is two muxes: an outer one that strips the optional
// /x/<scenario> and /n/<namespace> prefixes, and an inner one holding the real
// routes. Stripping has to happen before pattern matching — that is what makes
// "/n/t-42/search" match "POST /search" — and a ServeMux cannot rewrite a path
// before matching itself. Two muxes rather than one mux dispatching back into
// itself: re-entry would loop on a path like "/n/a/n/b/search", while an inner
// mux with no prefix patterns answers it from the catch-all, which is the
// fail-closed behaviour a malformed prefix should get.
func NewMux(d Deps, p Name, refuse func(Refusal) []byte, spec MuxSpec) *http.ServeMux {
	// Normalise once, so every route on this listener shares one journal sequence
	// counter and one attempt counter. Normalized is idempotent, so Handle's own
	// call is a no-op.
	d = d.Normalized()

	inner := http.NewServeMux()
	paths := map[string][]string{} // path -> allowed methods

	for _, rt := range spec.Routes {
		method, path, _ := strings.Cut(rt.Pattern, " ")
		inner.Handle(rt.Pattern, Handle(d, p, rt, recoverPanic(refuse, spec.Handlers[rt.Pattern])))
		paths[path] = append(paths[path], method)
	}

	// A method-less pattern per known path produces a provider-shaped 405.
	// Paths and methods are both walked in sorted order: map iteration would make
	// the Allow header's method order differ run to run (§3.3).
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		allow := slices.Sorted(slices.Values(paths[path]))
		h := methodNotAllowed(refuse, allow)
		inner.Handle(path, Handle(d, p, Route{Pattern: path}, recoverPanic(refuse, h)))
	}

	// A catch-all produces a provider-shaped 404 for every unknown path.
	inner.Handle("/", Handle(d, p, Route{Pattern: "/"}, recoverPanic(refuse, notFound(refuse))))

	outer := http.NewServeMux()
	outer.Handle("/", inner)
	stripper := stripLanePrefix(inner)
	outer.Handle("/"+scenarioSegment+"/", stripper)
	outer.Handle("/"+namespaceSegment+"/", stripper)
	return outer
}

// recoverPanic wraps h so a non-abort panic renders as the profile's own
// RefuseInternal 500 — with a handler.panic ERROR finding — instead of
// reaching net/http as an unrecovered panic, which today's caller (a
// nil-dereferencing handler, say) meets as a connection reset with no status
// and no body.
//
// http.ErrAbortHandler panics through unchanged: it is net/http's own
// sentinel for a deliberate transport-level abort (close_before_headers,
// stream_disconnect, and siblings), and every existing abort fault still
// needs to reach the server as a genuine abort, not a 500. Re-panicking it
// here lets it reach Handle's own recover/record defer exactly as before —
// nothing about the journal-append-in-defer or the abort-fault timing
// changes.
//
// h == nil passes through untouched, so a route registered with no handler
// still reaches Handle's own noHandler substitution instead of this wrapper
// dereferencing a nil func.
func recoverPanic(refuse func(Refusal) []byte, h Handler) Handler {
	if h == nil {
		return nil
	}
	return func(x *Exchange) (resp Response) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			x.Fail(CodeHandlerPanic, "", "handler panicked: %v", rec)
			resp = Response{
				Status: http.StatusInternalServerError,
				Body:   refuse(Refusal{Kind: RefuseInternal, Status: http.StatusInternalServerError, X: x}),
				Label:  "provider.panic",
			}
		}()
		return h(x)
	}
}

// stripLanePrefix removes the lane path prefixes and hands the parsed result to
// the inner mux through the request context, so Handle reads what routing
// matched on instead of parsing the path a second time.
//
// The request is cloned rather than mutated: net/http owns the one it handed us,
// and a handler that rewrites it in place is a handler that has changed what an
// outer middleware, a panic dump or a later read of r.URL reports. RawPath is
// cleared along with Path because ServeMux matches on the escaped path, and a
// stale RawPath would route the request by the prefix that was just removed.
func stripLanePrefix(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, prefix := splitLanePath(r.URL.Path)
		r2 := r.Clone(withLanePrefix(r.Context(), prefix))
		r2.URL.Path = route
		r2.URL.RawPath = ""
		inner.ServeHTTP(w, r2)
	})
}

// notFound answers an unmatched path with the profile's own 404 shape,
// rendered through refuse, and records route.unmatched — §5.2 requires the
// finding on every unmatched request.
func notFound(refuse func(Refusal) []byte) Handler {
	return func(x *Exchange) Response {
		x.Fail(CodeUnmatched, "", "no route serves %s %s", x.Request.Method, x.Request.URL.Path)
		return Response{
			Status:        http.StatusNotFound,
			Body:          refuse(Refusal{Kind: RefuseNotFound, Status: http.StatusNotFound, X: x}),
			Label:         "route.not_found",
			FaultEligible: false,
		}
	}
}

// methodNotAllowed answers a known path with an unsupported method, rendered
// through refuse, guaranteeing both the finding and the Allow header. The
// header is part of the documented behaviour image-smoke.sh asserts.
func methodNotAllowed(refuse func(Refusal) []byte, allow []string) Handler {
	allowed := strings.Join(allow, ", ")
	return func(x *Exchange) Response {
		x.Fail(CodeMethodNotAllowed, "", "%s is not allowed on %s; allowed: %s",
			x.Request.Method, x.Request.URL.Path, allowed)
		return Response{
			Status: http.StatusMethodNotAllowed,
			Header: http.Header{"Allow": []string{allowed}},
			Body: refuse(Refusal{
				Kind: RefuseMethodNotAllowed, Status: http.StatusMethodNotAllowed, Allow: allow, X: x,
			}),
			Label:         "route.method_not_allowed",
			FaultEligible: false,
		}
	}
}
