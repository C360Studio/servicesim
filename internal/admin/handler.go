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

	"github.com/c360studio/servicesim/internal/jobs"
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
	//
	// A scoped reset additionally needs it to isolate attempt counters per
	// namespace, which provider.Faults does not require — see [namespacedFaults]
	// for why the capability is optional and what happens without it.
	Faults provider.Faults

	// Jobs is the async-job registry /__admin/reset drops records from and
	// /__admin/jobs lists. It must be the same instance every provider's
	// Deps.Jobs holds, or a reset drops records no handler consults — the same
	// rule as Journal and Faults, and the reason is the same one that makes
	// this a three-store reset rather than two: a job's poll cursor IS a fault
	// attempt counter (docs/design/async-jobs.md §7.3), so cursors dropped
	// without the records they identify collide the next create with a record
	// still live.
	Jobs jobs.Store

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
	if d.Jobs == nil {
		// A fresh registry, never a shared one — the same rule as Journal above,
		// and for the same reason: two Sims in parallel subtests must not resolve
		// job identifiers out of one another's registry.
		d.Jobs = jobs.NewRegistry(jobs.Limits{})
	}
	if d.Scenario == nil {
		d.Scenario = scenario.Empty()
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	return d
}

// namespacedFaults is the optional capability a fault engine declares when its
// attempt counters — which are also the turn cursors — are isolated per
// namespace.
//
// It is asserted for rather than added to provider.Faults because that
// interface is exported and consumers implement it (CLAUDE.md house rule 7):
// every method added there breaks every implementation outside this repository.
//
// A fault engine that does not declare it makes a scoped reset impossible to
// honour, and the handler says so with a 501 instead of resetting half the
// lane. Clearing a namespace's journal while leaving its cursor mid-plan is the
// wrong-lane failure this whole feature exists to prevent, arrived at from the
// administrative side.
type namespacedFaults interface {
	provider.Faults

	// ResetIn returns one namespace's attempt counters to zero and leaves every
	// other namespace untouched.
	ResetIn(namespace string)
}

// noopFaults stands in for a nil Deps.Faults. Reset on it is a no-op rather
// than a nil dereference, and Next never faults, which keeps a zero Deps usable
// without making the admin surface pretend a fault engine exists.
type noopFaults struct{}

func (noopFaults) Next(key string) provider.FaultDecision {
	return provider.FaultDecision{Index: -1, Key: key}
}

func (noopFaults) Reset() {}

// ResetIn is honestly a no-op: this substitute holds no counter at all, so
// every namespace's fault state is already zero. Declaring the capability is
// what lets a zero Deps answer a scoped reset rather than reporting a limitation
// it does not have.
func (noopFaults) ResetIn(_ string) {}

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

	// Namespace names the lane a scoped reset dropped, and is absent when the
	// reset covered every lane. It is the caller's confirmation that the state
	// which disappeared is the state it asked about.
	Namespace string `json:"namespace,omitempty"`
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
// /__admin/namespaces, GET /__admin/scenario, GET /__admin/jobs, POST
// /__admin/reset. Everything else is 404 {"error":"not found"}, and a known
// path reached with the wrong method is 405 with a sorted Allow header —
// sorted because Go map iteration must never reach output, not even a header
// (package-design §3.3).
func Handler(deps Deps) http.Handler {
	d := deps.normalized()

	mux := http.NewServeMux()
	route(mux, "/healthz", map[string]http.HandlerFunc{http.MethodGet: d.handleHealthz})
	route(mux, "/readyz", map[string]http.HandlerFunc{http.MethodGet: d.handleReadyz})
	route(mux, "/__admin/requests", map[string]http.HandlerFunc{http.MethodGet: d.handleRequests})
	route(mux, "/__admin/namespaces", map[string]http.HandlerFunc{http.MethodGet: d.handleNamespaces})
	route(mux, "/__admin/scenario", map[string]http.HandlerFunc{http.MethodGet: d.handleScenario})
	route(mux, "/__admin/jobs", map[string]http.HandlerFunc{http.MethodGet: d.handleJobs})
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

// handleReset drops retained state and mutates nothing else. It clears journal
// entries, zeroes fault attempt counters — which are also the turn cursors —
// and drops async job records, and it never touches the loaded scenario: an
// admin API that could reconfigure a running simulator is hidden shared state
// between concurrent tests (CLAUDE.md house rule 6).
//
// The scope must be stated explicitly. Either ?namespace=<name> drops one lane
// or ?all=true drops every lane; a bare POST is a 400 that says so.
//
// That is a deliberate breaking change to the bare-reset behaviour, and the
// reason is the deployment this feature exists for. Every sibling e2e suite runs
// one shared container across many concurrent tests, so a reset that defaulted
// to "everything" would let one test's cleanup zero a hundred other tests'
// cursors mid-run. The result is not a failed reset — it is another test
// receiving the turn scripted for a different call, failing somewhere else
// entirely and much later. A caller that genuinely wants the whole process
// cleared still can, by saying so, and the verbosity is the point.
func (d Deps) handleReset(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	namespace, err := namespaceParam(q)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()}, false)
		return
	}
	all := boolParam(q, "all")

	switch {
	case namespace != "" && all:
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: errResetScopeConflict}, false)
	case namespace != "":
		d.resetNamespace(w, namespace)
	case all:
		d.resetAll(w)
	default:
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: errResetScopeRequired}, false)
	}
}

// The reset scope errors. None of them quotes anything from the request: a query
// parameter is one of the places a misconfigured adapter puts its credential
// (CLAUDE.md house rule 4), so the rule is stated and the value is not echoed.
const (
	errResetScopeRequired = "reset requires an explicit scope: either ?namespace=<name> to drop one namespace, " +
		"or ?all=true to drop every namespace"
	errResetScopeConflict = "?namespace= and ?all=true are mutually exclusive: reset one namespace or every " +
		"namespace, not both"
	errJournalNotScoped = "the wired journal does not isolate namespaces, so nothing was reset; " +
		"resetting everything requires ?all=true"
	errFaultsNotScoped = "the wired fault engine does not isolate attempt counters per namespace, so nothing " +
		"was reset; resetting everything requires ?all=true"
)

// resetAll drops every namespace: all journal entries, every fault counter and
// every job record. Do not reorder: see [Deps.resetNamespace] for why the
// scoped path cannot go jobs-first, and this path mirrors it so the two
// spellings of reset behave the same way relative to each other.
func (d Deps) resetAll(w http.ResponseWriter) {
	d.Journal.Reset()
	d.Faults.Reset()
	d.Jobs.Reset()
	d.Logger.Info("admin.reset",
		slog.String("scenario", d.Scenario.Name),
		slog.String("scope", "all"),
		slog.String("hint", "every namespace's journal entries, fault counters and job records were cleared"))

	writeJSON(w, http.StatusOK, statusResponse{Status: "reset"}, false)
}

// resetNamespace drops one namespace's journal entries, fault counters and job
// records, leaving every other namespace untouched.
//
// A surface that can scope only some of the three state stores scopes none of
// them. A half-scoped reset — an empty journal beside a cursor still mid-plan,
// or a cursor reset with the job record it identifies left live — is worse than
// a refusal, because the next request in that namespace is served wrong state
// and nothing says so. The order below is what makes that hold: both capability
// checks run before anything is dropped, so a store that cannot scope leaves
// every store untouched.
//
// jobs.Store declares ResetIn unconditionally rather than as an asserted
// capability, so there is no third type assertion here — every implementation
// of the seam can scope a reset by construction
// (docs/design/async-jobs.md §7.3).
//
// Do not reorder this to jobs-first. The window a reset can race live traffic
// through is symmetric regardless of which store drops first — see §7.3's
// "ordering cannot close the window" — and jobs-first would additionally break
// the invariant above: a store that turns out not to scope would leave job
// state already destroyed by a request that then reports 501 having "changed
// nothing".
func (d Deps) resetNamespace(w http.ResponseWriter, namespace string) {
	scoped, ok := d.Faults.(namespacedFaults)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: errFaultsNotScoped}, false)
		return
	}
	if !journal.ResetIn(d.Journal, namespace) {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: errJournalNotScoped}, false)
		return
	}
	scoped.ResetIn(namespace)
	d.Jobs.ResetIn(namespace)

	// The namespace is safe to log and to echo: it survived ValidNamespace, so it
	// is at most 64 characters of [A-Za-z0-9_-] with no separator, no control
	// character and nothing a log parser or a path join can be made to misread.
	d.Logger.Info("admin.reset",
		slog.String("scenario", d.Scenario.Name),
		slog.String("scope", "namespace"),
		slog.String("namespace", namespace),
		slog.String("hint", "one namespace's journal entries, fault counters and job records were cleared"))

	writeJSON(w, http.StatusOK, statusResponse{Status: "reset", Namespace: namespace}, false)
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
