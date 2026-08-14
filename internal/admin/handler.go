package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/internal/wire"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Deps is what the admin surface needs.
//
// The zero value is usable and answers every route: a nil Journal serves an
// empty journal, a nil Faults resets nothing, a nil Scenario reports the empty
// scenario, and a nil Ready reads as not ready. That matters because the admin
// listener binds before the provider listeners do, so it must be able to answer
// /healthz while the rest of the process is still coming up.
type Deps struct {
	// Journal is the journal /__admin/requests serves and /__admin/reset clears.
	// It must be the same instance the provider handlers were wired with.
	Journal journal.Journal

	// Faults is the fault engine /__admin/reset zeroes. It must be the same
	// instance every provider's Deps.Faults holds, or reset would clear counters
	// no handler consults.
	Faults provider.Faults

	// Scenario is the loaded, validated, resolved corpus /__admin/scenario
	// describes.
	Scenario *scenario.Scenario

	// Report is the validation outcome for Scenario. Its warnings are served on
	// /__admin/scenario: a scenario with error findings never gets this far,
	// because internal/server fails the process instead of binding.
	Report scenario.Report

	// Ready is flipped true by internal/server once the startup scenario has
	// loaded, validated and resolved and every listener is accepting. Nil reads
	// as not ready.
	Ready *atomic.Bool

	// Version is the build version reported by /healthz.
	Version string

	// Logger receives one event per reset. Nil means slog.DiscardHandler.
	Logger *slog.Logger
}

// normalized returns a copy of d with every nil replaced by its documented
// default, so no request path nil-checks a dependency. Handler calls it once.
func (d Deps) normalized() Deps {
	if d.Journal == nil {
		// A fresh discard journal, never a shared one: two Sims in parallel
		// subtests must not draw sequence numbers from one counter.
		d.Journal = journal.NewDiscard()
	}
	if d.Faults == nil {
		d.Faults = noopFaults{}
	}
	if d.Scenario == nil {
		d.Scenario = scenario.Empty()
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	return d
}

// noopFaults stands in for a nil Deps.Faults. Reset on it is a no-op rather
// than a nil dereference, and Next never faults, which keeps a zero Deps usable
// without making the admin surface pretend a fault engine exists.
type noopFaults struct{}

func (noopFaults) Next(key string) provider.FaultDecision {
	return provider.FaultDecision{Index: -1, Key: key}
}

func (noopFaults) Reset() {}

// ScenarioResponse is the GET /__admin/scenario body: what was loaded, and every
// validation warning that did not prevent loading.
type ScenarioResponse struct {
	Name     string             `json:"name"`
	Version  int                `json:"version"`
	Seed     string             `json:"seed"`
	Sources  int                `json:"sources"`
	Findings []scenario.Finding `json:"findings,omitempty"`
}

// statusResponse is the body of every endpoint that reports a state rather than
// data. It is unexported because a consumer asserting on the admin surface
// asserts on the status code; exporting it would make the field set a
// compatibility obligation for no gain (CLAUDE.md house rule 7).
type statusResponse struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

// errorResponse is the admin surface's error body. It is plain rather than
// provider-shaped: the admin listener imitates no vendor, and a consumer that
// receives this shape from what it thought was a provider base URL has learned
// something useful (package-design §5.3).
type errorResponse struct {
	Error string `json:"error"`
}

// Handler builds the admin mux.
//
// Routes: GET /healthz, GET /readyz, GET /__admin/requests, GET
// /__admin/scenario, POST /__admin/reset. Everything else is 404
// {"error":"not found"}, and a known path reached with the wrong method is 405
// with a sorted Allow header — sorted because Go map iteration must never reach
// output, not even a header (package-design §3.3).
func Handler(deps Deps) http.Handler {
	d := deps.normalized()

	mux := http.NewServeMux()
	route(mux, "/healthz", map[string]http.HandlerFunc{http.MethodGet: d.handleHealthz})
	route(mux, "/readyz", map[string]http.HandlerFunc{http.MethodGet: d.handleReadyz})
	route(mux, "/__admin/requests", map[string]http.HandlerFunc{http.MethodGet: d.handleRequests})
	route(mux, "/__admin/scenario", map[string]http.HandlerFunc{http.MethodGet: d.handleScenario})
	route(mux, "/__admin/reset", map[string]http.HandlerFunc{http.MethodPost: d.handleReset})
	mux.HandleFunc("/", d.handleNotFound)
	return mux
}

// route registers one admin path with the methods it answers, plus a
// method-less pattern that returns 405 for every other method. Registering both
// is what makes the wrong method a 405 instead of falling through to the
// catch-all 404: the method-specific pattern is the more specific of the two, so
// ServeMux prefers it and only an unmatched method reaches the fallback.
func route(mux *http.ServeMux, path string, methods map[string]http.HandlerFunc) {
	for _, m := range slices.Sorted(maps.Keys(methods)) {
		mux.HandleFunc(m+" "+path, methods[m])
	}
	mux.HandleFunc(path, methodNotAllowed(allowHeader(methods)))
}

// allowHeader renders the Allow header value for a route. The methods are
// sorted, and HEAD is added wherever GET is answered because ServeMux serves
// HEAD from a GET pattern and an Allow header that omitted it would be a lie.
func allowHeader(methods map[string]http.HandlerFunc) string {
	allow := slices.Sorted(maps.Keys(methods))
	if slices.Contains(allow, http.MethodGet) && !slices.Contains(allow, http.MethodHead) {
		allow = append(allow, http.MethodHead)
		slices.Sort(allow)
	}
	return strings.Join(allow, ", ")
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"}, false)
	}
}

// handleNotFound is the catch-all. Provider paths are not registered here, so
// POST :8080/search lands on it — which is the point: pointing a provider base
// URL at the admin port fails loudly and immediately.
func (d Deps) handleNotFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"}, false)
}

// handleHealthz reports that the process is alive. It answers as soon as the
// admin listener binds, which is before the provider listeners are up, so it
// must never consult readiness.
func (d Deps) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok", Version: d.Version}, false)
}

// handleReadyz reports whether the startup scenario has loaded, validated and
// resolved and every listener is accepting. A nil Ready is not ready.
func (d Deps) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if d.Ready == nil || !d.Ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, statusResponse{Status: "not_ready"}, false)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: "ready", Version: d.Version}, false)
}

// handleScenario describes what was loaded, with every validation warning that
// did not prevent loading. An error finding cannot appear here: internal/server
// fails the process before it binds.
func (d Deps) handleScenario(w http.ResponseWriter, r *http.Request) {
	body := ScenarioResponse{
		Name:     d.Scenario.Name,
		Version:  d.Scenario.Version,
		Seed:     d.Scenario.SeedKey(),
		Sources:  len(d.Scenario.Sources),
		Findings: d.Report.Findings,
	}
	writeJSON(w, http.StatusOK, body, pretty(r.URL.Query()))
}

// handleReset clears the journal and zeroes every fault counter, and mutates
// nothing else. It is a local-development convenience: parallel CI isolates by
// process, and no admin endpoint may reconfigure a running simulator.
//
// A ?namespace= parameter is refused rather than honoured. Per-namespace state
// lanes are not wired yet, so scoping a reset would silently reset every other
// namespace's cursors — the single worst failure this surface can produce, and
// exactly the trap the extended-surfaces addendum names. ?all=true is accepted
// as the explicit spelling of the full reset the bare form performs.
func (d Deps) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("namespace") {
		writeJSON(w, http.StatusNotImplemented, errorResponse{
			Error: "per-namespace reset is not implemented; omit ?namespace= to reset everything",
		}, false)
		return
	}

	d.Journal.Reset()
	d.Faults.Reset()
	d.Logger.Info("admin.reset",
		slog.String("scenario", d.Scenario.Name),
		slog.String("hint", "the journal and every fault counter were cleared"))

	writeJSON(w, http.StatusOK, statusResponse{Status: "reset"}, false)
}

// writeJSON encodes v into a buffer before it writes anything, so a marshalling
// failure becomes a 500 with an intact body rather than a 200 truncated
// mid-object. HTML escaping is off to match internal/wire: an ampersand in a
// journaled query URL survives as itself rather than being rewritten as a
// unicode escape, which is what a consumer grepping the journal expects.
//
// The default has no indentation, and that is normative rather than a taste:
// scripts/image-smoke.sh greps the journal for the literal "provider":"exa", and
// SetIndent would insert a space after every colon and break it. ?pretty=1 opts
// in.
func writeJSON(w http.ResponseWriter, status int, v any, indent bool) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		// The error text can quote the value that failed to encode, so it is not
		// served. Nothing has been written yet, so the status is still ours to set.
		w.Header().Set("Content-Type", wire.ContentTypeJSON)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"encode failed"}`+"\n")
		return
	}
	wire.WriteJSON(w, status, buf.Bytes())
}
