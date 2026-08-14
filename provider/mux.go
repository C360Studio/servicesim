package provider

import (
	"maps"
	"net/http"
	"slices"
	"strings"
)

// MuxSpec is everything a provider package supplies to build its listener's mux.
type MuxSpec struct {
	// Routes are the provider's real routes.
	Routes []Route

	// Handlers maps Route.Pattern to the handler serving it.
	Handlers map[string]Handler

	// NotFound answers any unknown path with a provider-shaped 404.
	NotFound Handler

	// MethodNotAllowed answers a known path with an unsupported method. allow is
	// the sorted list of methods that path does support, for the Allow header.
	MethodNotAllowed func(allow []string) Handler
}

// NewMux builds a provider listener's mux. It is exported and lives here, rather
// than being written three times in three provider packages, because the
// registration shape is load-bearing and easy to get subtly wrong: see §5.1 of
// the package design. Its verification table (POST /search 200, GET /search 405
// with Allow, POST /nope 404, POST /search/ 404) is provider/mux_test.go's test
// table.
//
// The method-less pattern per known path is mandatory, not stylistic. Verified on
// Go 1.26.4: with only "POST /search" and "/" registered, GET /search returns 404
// from the catch-all with Content-Type: text/plain, because "/" matched and
// ServeMux's built-in 405 path is never reached. scripts/image-smoke.sh asserts
// 405, so an implementation that skips this fails the image smoke test — and,
// worse, answers a method error with a body no consumer can parse.
func NewMux(d Deps, p Name, spec MuxSpec) *http.ServeMux {
	// Normalise once, so every route on this listener shares one journal sequence
	// counter and one attempt counter. Normalized is idempotent, so Handle's own
	// call is a no-op.
	d = d.Normalized()

	m := http.NewServeMux()
	paths := map[string][]string{} // path -> allowed methods

	for _, rt := range spec.Routes {
		method, path, _ := strings.Cut(rt.Pattern, " ")
		m.Handle(rt.Pattern, Handle(d, p, rt, spec.Handlers[rt.Pattern]))
		paths[path] = append(paths[path], method)
	}

	// A method-less pattern per known path produces a provider-shaped 405.
	// Paths and methods are both walked in sorted order: map iteration would make
	// the Allow header's method order differ run to run (§3.3).
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		allow := slices.Sorted(slices.Values(paths[path]))
		h := methodNotAllowed(spec, allow)
		m.Handle(path, Handle(d, p, Route{Pattern: path}, h))
	}

	// A catch-all produces a provider-shaped 404 for every unknown path.
	m.Handle("/", Handle(d, p, Route{Pattern: "/"}, notFound(spec)))
	return m
}

// notFound wraps the provider's 404 handler so the route.unmatched finding is
// journaled whether or not the provider remembered to record it. §5.2 requires
// the finding on every unmatched request; leaving that to three parallel
// implementations is how one of them ends up without it.
func notFound(spec MuxSpec) Handler {
	inner := spec.NotFound
	if inner == nil {
		inner = func(_ *Exchange) Response {
			return Response{Status: http.StatusNotFound, Label: "route.not_found"}
		}
	}
	return func(x *Exchange) Response {
		resp := inner(x)
		if !x.HasFinding(CodeUnmatched) {
			x.Fail(CodeUnmatched, "", "no route serves %s %s", x.Request.Method, x.Request.URL.Path)
		}
		resp.FaultEligible = false
		return resp
	}
}

// methodNotAllowed wraps the provider's 405 handler, guaranteeing both the
// finding and the Allow header. The header is part of the documented behaviour
// image-smoke.sh asserts, so it is set here rather than trusted to each provider.
func methodNotAllowed(spec MuxSpec, allow []string) Handler {
	var inner Handler
	if spec.MethodNotAllowed != nil {
		inner = spec.MethodNotAllowed(allow)
	}
	if inner == nil {
		inner = func(_ *Exchange) Response {
			return Response{Status: http.StatusMethodNotAllowed, Label: "route.method_not_allowed"}
		}
	}
	allowed := strings.Join(allow, ", ")
	return func(x *Exchange) Response {
		resp := inner(x)
		if !x.HasFinding(CodeMethodNotAllowed) {
			x.Fail(CodeMethodNotAllowed, "", "%s is not allowed on %s; allowed: %s",
				x.Request.Method, x.Request.URL.Path, allowed)
		}
		if resp.Header == nil {
			resp.Header = http.Header{}
		}
		if resp.Header.Get("Allow") == "" {
			resp.Header.Set("Allow", allowed)
		}
		resp.FaultEligible = false
		return resp
	}
}
