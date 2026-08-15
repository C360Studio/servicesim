package provider

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
)

// rolesYAML keys the turn cursor on the caller's role, which is the shape the
// sibling repositories needed: one route, several concurrent agent roles, each
// walking its own script.
const rolesYAML = `
version: 1
name: roles
providers:
  exa:
    turn_key: ["route", "body_json:role"]
    turns:
      - when:
          call_index: 0
        respond:
          answer: first
      - when:
          call_index: 1
        respond:
          answer: second
      - respond:
          answer: later
`

// headerKeyYAML keys the cursor on a header, the other extractor form. It omits
// "route", which subdivides the route's lane rather than replacing it.
const headerKeyYAML = `
version: 1
name: header-key
providers:
  exa:
    turn_key: ["header:X-Role"]
    turns:
      - respond:
          answer: only
`

// modelKeyYAML declares a discriminator and no "route" at all — the shape
// scenario.Validate has always accepted and that used to produce a lane the fault
// engine could resolve no plan for. The route is now always part of the lane, so
// this keys one cursor per model per route.
const modelKeyYAML = `
version: 1
name: model-key
providers:
  exa:
    turn_key: ["body_json:model"]
    turns:
      - when:
          call_index: 0
        respond:
          answer: first
      - when:
          call_index: 1
        respond:
          answer: second
      - respond:
          answer: later
`

// turnIndexHandler answers with the index of the turn that was selected, which is
// what a lane test needs to see: two callers in two lanes must both start at
// turn 0.
func turnIndexHandler(x *Exchange) Response {
	_, index := SelectTurnFor(x, x.Entry())
	if x.Failed() {
		return Response{Status: http.StatusBadRequest, Body: []byte(`{"error":"failed"}`), Label: "test.error"}
	}
	return Response{
		Status:        http.StatusOK,
		Body:          []byte(strconv.Itoa(index)),
		Label:         "test.ok",
		FaultEligible: true,
	}
}

// laneSpec is a one-route MuxSpec whose handler reports the selected turn index.
func laneSpec(h Handler) MuxSpec {
	return MuxSpec{
		Routes:   []Route{{Pattern: "POST /search", FaultKey: "exa:search"}},
		Handlers: map[string]Handler{"POST /search": h},
	}
}

// twoRouteSpec is a two-route MuxSpec sharing one handler and one provider entry,
// which is what a test about two routes never sharing a cursor needs.
func twoRouteSpec(h Handler) MuxSpec {
	return MuxSpec{
		Routes: []Route{
			{Pattern: "POST /search", FaultKey: "exa:search"},
			{Pattern: "POST /answer", FaultKey: "exa:answer"},
		},
		Handlers: map[string]Handler{"POST /search": h, "POST /answer": h},
	}
}

// --- Route.Entry -----------------------------------------------------------

// twoEntryScenario declares a turn_key on the PRIMARY entry only, which is the
// shape that makes the resolution bug observable: before Route.Entry existed,
// that key subdivided the second entry's lanes too.
const twoEntryScenario = `
version: 1
name: two-entries
providers:
  perplexity:
    turn_key: ["route", "body_json:model"]
    turns:
      - respond: {answer: sonar}
  perplexity_agent:
    turns:
      - respond: {answer: agent}
`

// TestTurnLaneKeyResolvesTheRoutesEntry pins the fix and the behaviour change it
// carries. A listener may serve more than one scenario entry, and before
// Route.Entry every entry after the first had its own turn_key silently ignored
// — entryTurnKey resolved by LISTENER name, so it read the primary entry's key
// for every route on that listener.
//
// This is a REAL behaviour change for perplexity_agent, which is shipped: a
// scenario declaring turn_key on the perplexity block and exercising the Agent
// surface used to have the agent lane subdivided by it, and no longer does. The
// old behaviour was an entry's own turn_key being ignored, which is a bug rather
// than a contract, but it is asserted here so the change is pinned rather than
// rediscovered.
func TestTurnLaneKeyResolvesTheRoutesEntry(t *testing.T) {
	t.Parallel()

	s := mustScenario(t, twoEntryScenario)
	body := `{"model":"sonar"}`

	newExchange := func(route Route) *Exchange {
		return &Exchange{
			Deps:     Deps{Scenario: s}.Normalized(),
			Provider: Perplexity,
			Route:    route,
			Raw:      []byte(body),
		}
	}

	// A route with no Entry resolves the listener's entry, so the primary
	// turn_key applies — unchanged from before.
	primary := newExchange(Route{Pattern: "POST /v1/sonar", FaultKey: "perplexity:completions"})
	gotPrimary := turnLaneKey(primary)
	if !strings.Contains(gotPrimary, "body_json:model=sonar") {
		t.Errorf("primary lane = %q, want the entry's own turn_key applied", gotPrimary)
	}

	// A route naming a different entry resolves THAT entry's turn_key, which
	// here is unset and therefore the default ["route"] — so the lane key is the
	// fault key verbatim.
	second := newExchange(Route{
		Pattern:  "POST /v1/agent",
		FaultKey: "perplexity:agent",
		Entry:    "perplexity_agent",
	})
	gotSecond := turnLaneKey(second)
	if gotSecond != "perplexity:agent" {
		t.Errorf("second-entry lane = %q, want %q — the primary entry's turn_key must not reach it",
			gotSecond, "perplexity:agent")
	}
	if strings.Contains(gotSecond, "body_json") {
		t.Error("the primary entry's turn_key subdivided a second entry's lane")
	}

	// No findings on either: an entry that declares no turn_key is not an
	// unresolved extractor, it is the documented default.
	if f := second.Findings(); len(f) != 0 {
		t.Errorf("unexpected findings on the second entry: %+v", f)
	}
}

// Route.Entry must also decide which entry's validation policy applies. It is
// the same lookup, and fixing one while leaving the other would leave two of the
// three ways to resolve an entry disagreeing.
func TestValidationPolicyResolvesTheRoutesEntry(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: two-entries-validation
providers:
  perplexity:
    validation:
      strict: true
    turns:
      - respond: {answer: sonar}
  perplexity_agent:
    turns:
      - respond: {answer: agent}
`
	s := mustScenario(t, src)

	newExchange := func(route Route) *Exchange {
		return &Exchange{
			Deps:     Deps{Scenario: s}.Normalized(),
			Provider: Perplexity,
			Route:    route,
		}
	}

	primary := newExchange(Route{Pattern: "POST /v1/sonar", FaultKey: "perplexity:completions"})
	primary.Warn("test.code", "", "a warning")
	if !primary.Failed() {
		t.Error("the primary entry declares strict, so its warning must promote to an error")
	}

	second := newExchange(Route{
		Pattern:  "POST /v1/agent",
		FaultKey: "perplexity:agent",
		Entry:    "perplexity_agent",
	})
	second.Warn("test.code", "", "a warning")
	if second.Failed() {
		t.Error("the second entry declares no validation policy and must not inherit the primary's strict")
	}
}

// A route naming an entry the scenario does not declare resolves nothing, and
// must not fall back to the listener's entry — a silent fallback would serve a
// scenario the author never wrote for that surface.
func TestRouteEntryNamingAnUndeclaredEntry(t *testing.T) {
	t.Parallel()

	s := mustScenario(t, twoEntryScenario)
	x := &Exchange{
		Deps:     Deps{Scenario: s}.Normalized(),
		Provider: Perplexity,
		Route:    Route{Pattern: "POST /x", FaultKey: "perplexity:x", Entry: "not_declared"},
	}

	if got := x.Entry(); got != nil {
		t.Errorf("Entry() = %+v, want nil for an entry the scenario does not declare", got)
	}
}

func TestValidNamespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "a test id", in: "t-42", want: true},
		{name: "underscores and digits", in: "suite_7", want: true},
		{name: "mixed case", in: "TestFoo", want: true},
		{name: "the default", in: DefaultNamespace, want: true},
		{name: "at the length bound", in: strings.Repeat("a", MaxNamespaceNameLen), want: true},

		{name: "empty", in: "", want: false},
		{name: "over the length bound", in: strings.Repeat("a", MaxNamespaceNameLen+1), want: false},
		{name: "a dot opens traversal", in: "..", want: false},
		{name: "a slash is a path", in: "a/b", want: false},
		{name: "a percent is an escape", in: "a%2fb", want: false},
		{name: "a newline is log injection", in: "a\nb", want: false},
		{name: "a space", in: "a b", want: false},
		{name: "non-ASCII", in: "café", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, ValidNamespace(tc.in))
		})
	}
}

func TestSplitLanePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		path         string
		wantRoute    string
		wantScenario string
		wantNS       string
		wantBadScen  bool
		wantBadNS    bool
	}{
		{
			name: "no prefix at all", path: "/search", wantRoute: "/search",
		},
		{
			name: "namespace only", path: "/n/t-42/search",
			wantRoute: "/search", wantNS: "t-42",
		},
		{
			name: "scenario only", path: "/x/happy/search",
			wantRoute: "/search", wantScenario: "happy",
		},
		{
			name: "both, in the fixed order", path: "/x/happy/n/t-42/search",
			wantRoute: "/search", wantScenario: "happy", wantNS: "t-42",
		},
		{
			name: "a multi-segment route survives", path: "/n/t-42/v1/chat/completions",
			wantRoute: "/v1/chat/completions", wantNS: "t-42",
		},
		{
			name: "the reverse order is not a prefix", path: "/n/t-42/x/happy/search",
			wantRoute: "/x/happy/search", wantNS: "t-42",
		},
		{
			name: "an empty namespace is present and invalid", path: "/n//search",
			wantRoute: "/search", wantBadNS: true,
		},
		{
			name: "a traversal namespace is invalid", path: "/n/../search",
			wantRoute: "/search", wantBadNS: true,
		},
		{
			name: "an invalid scenario is still stripped", path: "/x/a b/n/t-42/search",
			wantRoute: "/search", wantNS: "t-42", wantBadScen: true,
		},
		{
			name: "a prefix with no route addresses the root", path: "/n/t-42",
			wantRoute: "/", wantNS: "t-42",
		},
		{
			name: "a path that merely starts with n is untouched", path: "/nope",
			wantRoute: "/nope",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			route, p := splitLanePath(tc.path)
			require.Equal(t, tc.wantRoute, route)
			require.Equal(t, tc.wantScenario, p.scenario)
			require.Equal(t, tc.wantNS, p.namespace)
			require.Equal(t, tc.wantBadScen, p.scenarioBad)
			require.Equal(t, tc.wantBadNS, p.namespaceBad)
			require.Equal(t, tc.path, p.path, "the received path is kept for the journal")
		})
	}
}

func TestLaneCursorKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		lane Lane
		want string
	}{
		{
			name: "the default namespace keys exactly as the route did",
			lane: Lane{Namespace: DefaultNamespace, Scenario: "s", Key: "exa:search"},
			want: "exa:search",
		},
		{
			name: "an unresolved lane keys on its key alone",
			lane: Lane{Key: "exa:search"},
			want: "exa:search",
		},
		{
			name: "a namespace is the outermost component",
			lane: Lane{Namespace: "t-42", Scenario: "s", Key: "exa:search"},
			want: "t-42/exa:search",
		},
		{
			name: "the scenario is not part of the cursor key",
			lane: Lane{Namespace: "t-42", Scenario: "other", Key: "exa:search"},
			want: "t-42/exa:search",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.lane.CursorKey())
		})
	}
}

// TestPrefixedAndUnprefixedReachTheSameRoute is the property base-URL injection
// rests on: a consumer that points EXA_BASE_URL at .../n/<test-id> must reach the
// same handler, with the same route pattern, as one that does not.
func TestPrefixedAndUnprefixedReachTheSameRoute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		path      string
		wantNS    string
		wantScen  string
		wantEntry string
	}{
		{name: "unprefixed", path: "/search", wantNS: DefaultNamespace, wantScen: "roles", wantEntry: "/search"},
		{name: "namespace prefix", path: "/n/t-42/search", wantNS: "t-42", wantScen: "roles", wantEntry: "/n/t-42/search"},
		{name: "scenario prefix", path: "/x/alt/search", wantNS: DefaultNamespace, wantScen: "alt", wantEntry: "/x/alt/search"},
		{
			name: "both prefixes", path: "/x/alt/n/t-42/search",
			wantNS: "t-42", wantScen: "alt", wantEntry: "/x/alt/n/t-42/search",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got Lane
			j := journal.NewRing(8, 4096)
			d := Deps{Journal: j, Scenario: mustScenario(t, rolesYAML)}
			mux := NewMux(d, Exa, laneSpec(func(x *Exchange) Response {
				got = x.Lane()
				return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
			}))

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"role":"planner"}`)))
			require.Equal(t, http.StatusOK, w.Code)

			require.Equal(t, tc.wantNS, got.Namespace)
			require.Equal(t, tc.wantScen, got.Scenario)

			entries := j.Snapshot()
			require.Len(t, entries, 1)
			require.Equal(t, "POST /search", entries[0].Route, "the prefix does not change the matched pattern")
			require.Equal(t, tc.wantEntry, entries[0].Path, "the journal records the path as received")
			require.Equal(t, tc.wantNS, entries[0].Namespace)
		})
	}
}

// TestPrefixedRouteStillFailsClosed proves stripping does not turn an unknown
// path or a wrong method into a match.
func TestPrefixedRouteStillFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   string
	}{
		{
			name: "an unknown path under a namespace", method: http.MethodPost, path: "/n/t-42/nope",
			wantStatus: http.StatusNotFound, wantCode: CodeUnmatched,
		},
		{
			name: "a wrong method under a namespace", method: http.MethodGet, path: "/n/t-42/search",
			wantStatus: http.StatusMethodNotAllowed, wantCode: CodeMethodNotAllowed,
		},
		{
			name: "a second namespace prefix is not stripped twice", method: http.MethodPost, path: "/n/a/n/b/search",
			wantStatus: http.StatusNotFound, wantCode: CodeUnmatched,
		},
		{
			name: "the prefixes out of order", method: http.MethodPost, path: "/n/a/x/s/search",
			wantStatus: http.StatusNotFound, wantCode: CodeUnmatched,
		},
		{
			name: "a bare prefix", method: http.MethodPost, path: "/n/t-42",
			wantStatus: http.StatusNotFound, wantCode: CodeUnmatched,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			j := journal.NewRing(8, 4096)
			mux := NewMux(Deps{Journal: j}, Exa, testSpec())

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			require.Equal(t, tc.wantStatus, w.Code)

			entries := j.Snapshot()
			require.Len(t, entries, 1)
			require.Equal(t, journal.OutcomeUnmatched, entries[0].Outcome.Kind)
			codes := make([]string, 0, len(entries[0].Errors()))
			for _, f := range entries[0].Errors() {
				codes = append(codes, f.Code)
			}
			require.Contains(t, codes, tc.wantCode)
		})
	}
}

// TestInvalidNamespaceIsRejected proves an unvalidated name never becomes a lane.
// The request fails with an error finding the provider packages render as their
// own error envelope, and it is served in the default namespace so nothing
// allocates state under the rejected name.
func TestInvalidNamespaceIsRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		header   string
		wantCode string
	}{
		{name: "a dot in the path", path: "/n/a.b/search", wantCode: CodeNamespaceInvalid},
		{name: "an escaped space in the path", path: "/n/a%20b/search", wantCode: CodeNamespaceInvalid},
		{name: "an empty namespace in the path", path: "/n//search", wantCode: CodeNamespaceInvalid},
		{
			name:     "an over-long namespace",
			path:     "/n/" + strings.Repeat("a", MaxNamespaceNameLen+1) + "/search",
			wantCode: CodeNamespaceInvalid,
		},
		{name: "a bad header", path: "/search", header: "not a namespace", wantCode: CodeNamespaceInvalid},
		{name: "a bad scenario name", path: "/x/a.b/search", wantCode: CodeScenarioInvalid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				failed bool
				lane   Lane
			)
			j := journal.NewRing(8, 4096)
			// Handle directly rather than through NewMux: ServeMux rewrites a path
			// that needs cleaning before any handler sees it, and "/n//search" is
			// one. Resolution is what is under test here, so it is fed the path
			// verbatim.
			h := Handle(Deps{Journal: j}, Exa, testRoute, func(x *Exchange) Response {
				failed, lane = x.Failed(), x.Lane()
				if failed {
					return Response{Status: http.StatusBadRequest, Body: []byte(`{}`), Label: "test.error"}
				}
				return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
			})

			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			if tc.header != "" {
				r.Header.Set(NamespaceHeader, tc.header)
			}
			w := httptest.NewRecorder()
			h(w, r)

			require.True(t, failed, "the handler must see the rejection before it renders")
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Equal(t, DefaultNamespace, lane.Namespace, "a rejected name never becomes a lane")

			errs := j.Snapshot()[0].Errors()
			require.Len(t, errs, 1)
			require.Equal(t, tc.wantCode, errs[0].Code)
			require.NotContains(t, errs[0].Message, "a.b", "the finding never echoes the rejected value")
		})
	}
}

// TestInvalidNamespaceThroughTheMux proves the rejection also arrives when the
// request is routed rather than handled directly, and that net/http's own path
// cleaning disposes of a traversal attempt before routing even begins.
func TestInvalidNamespaceThroughTheMux(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(8, 4096)
	mux := NewMux(Deps{Journal: j}, Exa, laneSpec(func(x *Exchange) Response {
		if x.Failed() {
			return Response{Status: http.StatusBadRequest, Body: []byte(`{}`), Label: "test.error"}
		}
		return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
	}))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/n/a.b/search", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, CodeNamespaceInvalid, j.Snapshot()[0].Errors()[0].Code)

	// A traversal never reaches a handler at all: ServeMux redirects the cleaned
	// path, which is a second, independent reason "/n/../x" cannot escape.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/n/../search", strings.NewReader(`{}`)))
	require.True(t, w.Code >= 300 && w.Code < 400, "a dirty path is redirected, not routed: got %d", w.Code)
	require.Equal(t, "/search", w.Header().Get("Location"))
}

// TestNamespaceHeaderIsAcceptedAndLosesToThePath covers the SDK that cannot carry
// a path prefix, and the documented precedence when both are present.
func TestNamespaceHeaderIsAcceptedAndLosesToThePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		header string
		want   string
	}{
		{name: "header only", path: "/search", header: "t-hdr", want: "t-hdr"},
		{name: "path only", path: "/n/t-path/search", want: "t-path"},
		{name: "the path wins", path: "/n/t-path/search", header: "t-hdr", want: "t-path"},
		{name: "neither", path: "/search", want: DefaultNamespace},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got Lane
			mux := NewMux(Deps{}, Exa, laneSpec(func(x *Exchange) Response {
				got = x.Lane()
				return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
			}))

			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			if tc.header != "" {
				r.Header.Set(NamespaceHeader, tc.header)
			}
			mux.ServeHTTP(httptest.NewRecorder(), r)
			require.Equal(t, tc.want, got.Namespace)
		})
	}
}

// TestJournalSequencesAreIsolatedPerNamespace covers the half of the isolation
// claim that is not a cursor: the design's table lists journal entries AND their
// sequence numbers as per-namespace state, so a consumer reading its own
// namespace sees 1, 2, 3 rather than its share of a process-wide count.
//
// The regression this pins is specific and was invisible to every other test:
// the seam claimed Seq from the default lane's counter while storing the entry
// under the request's namespace, so the entries were scoped correctly and only
// the numbers on them were wrong. A consumer that keys on Seq — indexing its own
// traffic, or asserting a call ordinal — would silently read another test's
// arrival count.
func TestJournalSequencesAreIsolatedPerNamespace(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(16, 4096)
	mux := NewMux(Deps{Journal: j}, Exa, laneSpec(func(_ *Exchange) Response {
		return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
	}))

	// Interleaved on purpose, and sequential so the expectation is exact: if the
	// counter were shared, t-1's three calls would land on 1, 3, 5.
	paths := []string{"/search", "/n/t-1/search", "/n/t-2/search",
		"/n/t-1/search", "/search", "/n/t-1/search"}
	for _, path := range paths {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		require.Equal(t, http.StatusOK, w.Code)
	}

	got := map[string][]uint64{}
	for _, e := range j.Snapshot() {
		got[e.Namespace] = append(got[e.Namespace], e.Seq)
	}

	require.Equal(t, map[string][]uint64{
		DefaultNamespace: {1, 2},
		"t-1":            {1, 2, 3},
		"t-2":            {1},
	}, got, "each namespace numbers its own entries from one")
}

// TestNamespaceHeaderSequencesInItsOwnLane proves the header selector reaches the
// sequence claim too. It is a separate case because the claim happens before the
// body is read, and a namespace that arrives on a header rather than the path is
// the one most easily missed by resolution that runs later.
func TestNamespaceHeaderSequencesInItsOwnLane(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(16, 4096)
	mux := NewMux(Deps{Journal: j}, Exa, laneSpec(func(_ *Exchange) Response {
		return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
	}))

	for range 2 {
		mux.ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`)))
	}
	for range 2 {
		r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`))
		r.Header.Set(NamespaceHeader, "t-hdr")
		mux.ServeHTTP(httptest.NewRecorder(), r)
	}

	var got []uint64
	for _, e := range j.Snapshot() {
		if e.Namespace == "t-hdr" {
			got = append(got, e.Seq)
		}
	}
	require.Equal(t, []uint64{1, 2}, got, "the header names a lane the sequence is drawn from")
}

// TestRejectedNamespaceSequencesInTheDefaultLane pins where a rejected name is
// counted. It is served in the default namespace, so its entry must draw on that
// lane's counter — allocating a sequence under the rejected name would create
// exactly the state validation refused.
func TestRejectedNamespaceSequencesInTheDefaultLane(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(16, 4096)
	// Handle directly: ServeMux cleans a path like "/n//search" before routing,
	// and the rejection is what this needs to reach the sequence claim.
	h := Handle(Deps{Journal: j}, Exa, testRoute, func(x *Exchange) Response {
		if x.Failed() {
			return Response{Status: http.StatusBadRequest, Body: []byte(`{}`), Label: "test.error"}
		}
		return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
	})

	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`)))
	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/n/a.b/search", strings.NewReader(`{}`)))

	entries := j.Snapshot()
	require.Len(t, entries, 2)
	for _, e := range entries {
		require.Equal(t, DefaultNamespace, e.Namespace)
	}
	require.Equal(t, []uint64{1, 2}, []uint64{entries[0].Seq, entries[1].Seq},
		"a rejected name draws on the default lane rather than opening one of its own")
	require.Equal(t, CodeNamespaceInvalid, entries[1].Errors()[0].Code)
}

// TestConcurrentNamespacesHaveIndependentCursors is the isolation claim, run with
// real goroutines under -race. Every namespace must see the full sequence 0..n-1
// exactly once; a shared counter would give one namespace 0,3,6 and another
// 1,4,7.
func TestConcurrentNamespacesHaveIndependentCursors(t *testing.T) {
	t.Parallel()

	const (
		namespaces = 6
		perLane    = 8
	)

	var (
		mu   sync.Mutex
		seen = map[string][]int{}
	)
	mux := NewMux(Deps{}, Exa, laneSpec(func(x *Exchange) Response {
		index := x.CallIndex()
		mu.Lock()
		seen[x.Lane().Namespace] = append(seen[x.Lane().Namespace], index)
		mu.Unlock()
		return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
	}))

	var wg sync.WaitGroup
	start := make(chan struct{})
	for n := range namespaces {
		for range perLane {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start // explicit synchronisation, not a sleep: maximise overlap
				path := "/n/t-" + strconv.Itoa(n) + "/search"
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
				require.Equal(t, http.StatusOK, w.Code)
			}()
		}
	}
	close(start)
	wg.Wait()

	require.Len(t, seen, namespaces)
	want := make([]int, perLane)
	for i := range want {
		want[i] = i
	}
	for ns, indices := range seen {
		slices.Sort(indices)
		require.Equal(t, want, indices, "namespace %s drew from its own cursor", ns)
	}
}

// TestTurnKeySeparatesConcurrentRoles is failure (a) from the survey: one route,
// two roles, a body_json turn key. Each role must walk its own script from turn 0
// instead of drawing every other turn out of a shared sequence.
func TestTurnKeySeparatesConcurrentRoles(t *testing.T) {
	t.Parallel()

	mux := NewMux(Deps{Scenario: mustScenario(t, rolesYAML)}, Exa, laneSpec(turnIndexHandler))

	post := func(role string) string {
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"role":"` + role + `"}`)
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/search", body))
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}

	// Interleaved, which is what a route-keyed cursor cannot survive.
	require.Equal(t, "0", post("planner"))
	require.Equal(t, "0", post("critic"))
	require.Equal(t, "1", post("planner"))
	require.Equal(t, "1", post("critic"))
	require.Equal(t, "2", post("planner"), "the third call falls through to the unconditional turn")
}

// TestTurnKeyAndNamespaceCompose proves the two dimensions are independent: the
// same role in two namespaces is two cursors.
func TestTurnKeyAndNamespaceCompose(t *testing.T) {
	t.Parallel()

	mux := NewMux(Deps{Scenario: mustScenario(t, rolesYAML)}, Exa, laneSpec(turnIndexHandler))

	post := func(namespace, role string) string {
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"role":"` + role + `"}`)
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/n/"+namespace+"/search", body))
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}

	require.Equal(t, "0", post("t-1", "planner"))
	require.Equal(t, "0", post("t-2", "planner"))
	require.Equal(t, "1", post("t-1", "planner"))
	require.Equal(t, "0", post("t-1", "critic"))
}

// TestTurnKeyHeaderExtractor covers the header form of the extractor. Its
// turn_key names no route, so the keys also show the route leading a lane that
// never asked for it.
func TestTurnKeyHeaderExtractor(t *testing.T) {
	t.Parallel()

	var keys []string
	mux := NewMux(Deps{Scenario: mustScenario(t, headerKeyYAML)}, Exa, laneSpec(func(x *Exchange) Response {
		keys = append(keys, x.Lane().Key)
		return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
	}))

	for _, role := range []string{"planner", "critic"} {
		r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`))
		r.Header.Set("X-Role", role)
		mux.ServeHTTP(httptest.NewRecorder(), r)
	}
	require.Equal(t, []string{"exa:search|header:X-Role=planner", "exa:search|header:X-Role=critic"}, keys)
}

// TestTurnKeyWithoutRouteStillNamesTheRoute is the decision this file exists to
// pin: turn_key declares discriminators additional to the route, never a
// replacement for it. A lane with no route component resolves no fault plan, so
// every declared fault stops firing and `when: {call_index: 0}` never matches —
// silence where the author asked for behaviour.
func TestTurnKeyWithoutRouteStillNamesTheRoute(t *testing.T) {
	t.Parallel()

	var keys []string
	j := journal.NewRing(8, 8192)
	d := Deps{Journal: j, Scenario: mustScenario(t, modelKeyYAML)}
	mux := NewMux(d, Exa, laneSpec(func(x *Exchange) Response {
		keys = append(keys, x.Lane().Key)
		return turnIndexHandler(x)
	}))

	post := func(model string) string {
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"model":"` + model + `"}`)
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/search", body))
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}

	// A working cursor: the lane counts, so call_index selects a turn.
	require.Equal(t, "0", post("sonar"))
	require.Equal(t, "1", post("sonar"))
	require.Equal(t, "0", post("sonar-pro"), "a second model is a second lane, from turn zero")

	require.Equal(t, []string{
		"exa:search|body_json:model=sonar",
		"exa:search|body_json:model=sonar",
		"exa:search|body_json:model=sonar-pro",
	}, keys)

	// The property the fault engine depends on: a registered route key is
	// recoverable from the lane key, namespaced or not, so the route's plan
	// resolves instead of the request falling through as an unknown key.
	for _, key := range []string{keys[0], "t-42/" + keys[0]} {
		_, parts := SplitCursorKey(key)
		require.Contains(t, parts, "exa:search")
	}

	for _, entry := range j.Snapshot() {
		require.Empty(t, entry.Warnings(),
			"an implicit route is not an unresolved extractor and must not warn")
	}
}

// TestExplicitRouteInTurnKeyIsNotDoubled proves the route is added once however
// the author spelled it. Every spelling of one lane has to produce one key: two
// spellings producing two keys would split a cursor the scenario meant to share,
// and the documented default ["route"] has to stay byte-identical to what a fault
// engine pre-registered.
func TestExplicitRouteInTurnKeyIsNotDoubled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		turnKey string
		want    string
	}{
		{name: "unset, the documented default", turnKey: "", want: "exa:search"},
		{name: "route written out", turnKey: `["route"]`, want: "exa:search"},
		{name: "route twice", turnKey: `["route", "route"]`, want: "exa:search"},
		{
			name:    "route then a discriminator",
			turnKey: `["route", "body_json:model"]`,
			want:    "exa:search|body_json:model=sonar",
		},
		{
			name:    "a discriminator alone",
			turnKey: `["body_json:model"]`,
			want:    "exa:search|body_json:model=sonar",
		},
		{
			// The route leads the key wherever it was written, so this names the
			// same lane as the two cases above rather than a third one.
			name:    "route written last",
			turnKey: `["body_json:model", "route"]`,
			want:    "exa:search|body_json:model=sonar",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			src := "version: 1\nname: dedup\nproviders:\n  exa:\n"
			if tc.turnKey != "" {
				src += "    turn_key: " + tc.turnKey + "\n"
			}
			src += "    turns:\n      - respond:\n          answer: only\n"

			var key string
			mux := NewMux(Deps{Scenario: mustScenario(t, src)}, Exa, laneSpec(func(x *Exchange) Response {
				key = x.Lane().Key
				return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
			}))

			w := httptest.NewRecorder()
			body := strings.NewReader(`{"model":"sonar"}`)
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/search", body))
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, tc.want, key)
		})
	}
}

// TestTwoRoutesAreNeverOneLane pins the other half of the decision. Two routes
// carrying identical turn_key values are two lanes, so neither draws the turn
// scripted for the other — and no turn_key can make them share a cursor.
func TestTwoRoutesAreNeverOneLane(t *testing.T) {
	t.Parallel()

	var keys []string
	d := Deps{Scenario: mustScenario(t, modelKeyYAML)}
	mux := NewMux(d, Exa, twoRouteSpec(func(x *Exchange) Response {
		keys = append(keys, x.Lane().Key)
		return turnIndexHandler(x)
	}))

	post := func(path string) string {
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"model":"sonar"}`)
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, body))
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}

	require.Equal(t, "0", post("/search"))
	require.Equal(t, "0", post("/answer"), "the same turn_key value on another route starts its own cursor")
	require.Equal(t, "1", post("/search"))
	require.Equal(t, "1", post("/answer"))

	require.Equal(t, []string{
		"exa:search|body_json:model=sonar",
		"exa:answer|body_json:model=sonar",
		"exa:search|body_json:model=sonar",
		"exa:answer|body_json:model=sonar",
	}, keys)
}

// TestTurnKeyUnresolvedWarnsRatherThanMerging is the property the design insists
// on: an extractor that resolves nothing must be visible in the journal. Silently
// dropping it merges two lanes, and a merged lane is a coherent-looking response
// from the wrong script that fails somewhere else entirely, much later.
func TestTurnKeyUnresolvedWarnsRatherThanMerging(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		wantKey  string
		wantWarn bool
	}{
		{name: "resolved", body: `{"role":"planner"}`, wantKey: "exa:search|body_json:role=planner"},
		{name: "absent", body: `{}`, wantKey: "exa:search", wantWarn: true},
		{name: "null is not a value", body: `{"role":null}`, wantKey: "exa:search", wantWarn: true},
		{name: "an object is not a scalar", body: `{"role":{"a":1}}`, wantKey: "exa:search", wantWarn: true},
		{
			name:    "an over-long value is not a lane name",
			body:    `{"role":"` + strings.Repeat("r", maxLaneValueLen+1) + `"}`,
			wantKey: "exa:search", wantWarn: true,
		},
		{name: "no body at all", body: "", wantKey: "exa:search", wantWarn: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var key string
			j := journal.NewRing(8, 8192)
			d := Deps{Journal: j, Scenario: mustScenario(t, rolesYAML)}
			mux := NewMux(d, Exa, laneSpec(func(x *Exchange) Response {
				key = x.Lane().Key
				return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
			}))

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(tc.body)))
			require.Equal(t, http.StatusOK, w.Code, "an unresolved extractor warns; it never fails the request")
			require.Equal(t, tc.wantKey, key)

			warnings := j.Snapshot()[0].Warnings()
			if !tc.wantWarn {
				require.Empty(t, warnings)
				return
			}
			require.Len(t, warnings, 1)
			require.Equal(t, CodeTurnKeyUnresolved, warnings[0].Code)
		})
	}
}

// TestTurnKeyBodyJSONPaths covers the dotted-path forms an LLM body needs, and
// proves the scalar formatting matches what `when: {body_json: ...}` compares
// against — a lane and a predicate reading one path must see one string.
func TestTurnKeyBodyJSONPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		body string
		want string
	}{
		{name: "top level string", path: "model", body: `{"model":"sonar"}`, want: "sonar"},
		{name: "array index", path: "messages.0.role", body: `{"messages":[{"role":"system"}]}`, want: "system"},
		{name: "an integer keeps its form", path: "n", body: `{"n":3}`, want: "3"},
		{name: "a large integer is not scientific", path: "n", body: `{"n":10000000000000000000}`, want: "10000000000000000000"},
		{name: "a bool", path: "stream", body: `{"stream":true}`, want: "true"},
		{name: "a missing index", path: "messages.9.role", body: `{"messages":[]}`, want: ""},
		{name: "a scalar walked into", path: "model.name", body: `{"model":"sonar"}`, want: ""},
		{name: "an empty path", path: "", body: `{"model":"sonar"}`, want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := lookupLaneJSON(decodeLaneBody([]byte(tc.body)), tc.path)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.want != "", ok)
		})
	}
}

// TestLaneIsResolvedBeforeTheHandlerRuns pins the ordering the design requires:
// the handler must be able to read the lane on its first line, because turn
// selection is often the first thing it does.
func TestLaneIsResolvedBeforeTheHandlerRuns(t *testing.T) {
	t.Parallel()

	var seen Lane
	h := Handle(Deps{Scenario: mustScenario(t, rolesYAML)}, Exa, testRoute, func(x *Exchange) Response {
		seen = x.Lane()
		return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
	})

	// Direct Handle use, without the mux: the prefix is parsed here rather than
	// stripped, so a listener built by hand still resolves the same lane.
	r := httptest.NewRequest(http.MethodPost, "/n/t-42/search", strings.NewReader(`{"role":"planner"}`))
	h(httptest.NewRecorder(), r)

	require.Equal(t, Lane{
		Namespace: "t-42",
		Scenario:  "roles",
		Key:       "exa:search|body_json:role=planner",
	}, seen)
}

// TestFaultAndTurnDrawOnOneLaneCounter is the single-counter property, restated
// on the lane. The fault decision's key is the lane's cursor key, so a scenario
// that faults call 2 and answers differently on call 3 cannot disagree with
// itself about which call it is looking at.
func TestFaultAndTurnDrawOnOneLaneCounter(t *testing.T) {
	t.Parallel()

	engine := &countingFaults{}
	var key string
	mux := NewMux(Deps{Faults: engine, Scenario: mustScenario(t, rolesYAML)}, Exa, laneSpec(func(x *Exchange) Response {
		index := x.CallIndex()
		key = x.Fault().Key
		require.Equal(t, index, x.CallIndex(), "the claim is memoised, not repeated")
		return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "test.ok", FaultEligible: true}
	}))

	body := strings.NewReader(`{"role":"planner"}`)
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/n/t-42/search", body))

	require.Equal(t, "t-42/exa:search|body_json:role=planner", key)
	require.Equal(t, 1, engine.calls, "one request claims exactly one attempt")
}

// TestLaneScalarFormattingIsDecoderAgnostic covers the float64 branch. Nothing on
// the request path produces one — decodeLaneBody uses UseNumber, so every number
// arrives as json.Number — and that is exactly why the branch is written out:
// the alternative to "unreachable and obvious" is a decoder change that silently
// turns every numeric lane into an unresolved one.
func TestLaneScalarFormattingIsDecoderAgnostic(t *testing.T) {
	t.Parallel()

	got, ok := lookupLaneJSON(map[string]any{"n": float64(3)}, "n")
	require.True(t, ok)
	require.Equal(t, "3", got)
}

// TestTurnKeyUnparseableBodyResolvesNothing proves a malformed body cannot make a
// lane. The request still fails on request.malformed_json; the lane must not
// additionally invent a key out of half a document.
func TestTurnKeyUnparseableBodyResolvesNothing(t *testing.T) {
	t.Parallel()

	var key string
	j := journal.NewRing(8, 4096)
	d := Deps{Journal: j, Scenario: mustScenario(t, rolesYAML)}
	mux := NewMux(d, Exa, laneSpec(func(x *Exchange) Response {
		key = x.Lane().Key
		return Response{Status: http.StatusBadRequest, Body: []byte(`{}`), Label: "test.error"}
	}))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{"role":`)))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, "exa:search", key)

	entry := j.Snapshot()[0]
	require.Equal(t, CodeMalformedJSON, entry.Errors()[0].Code)
	require.Equal(t, CodeTurnKeyUnresolved, entry.Warnings()[0].Code)
}

// TestSplitCursorKey pins the inverse of CursorKey. The fault engine uses it to
// recover the route key a lane was composed from, so a namespace that failed to
// split would silently draw on another lane's attempt budget.
func TestSplitCursorKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		key           string
		wantNamespace string
		wantParts     []string
	}{
		{
			name:          "an unprefixed route key is the default namespace",
			key:           "exa:search",
			wantNamespace: DefaultNamespace,
			wantParts:     []string{"exa:search"},
		},
		{
			name:          "a namespace is the outermost component",
			key:           "t-42/exa:search",
			wantNamespace: "t-42",
			wantParts:     []string{"exa:search"},
		},
		{
			name:          "turn key contributions split on their own separator",
			key:           "t-42/perplexity:completions|body_json:model=sonar",
			wantNamespace: "t-42",
			wantParts:     []string{"perplexity:completions", "body_json:model=sonar"},
		},
		{
			name:          "a turn lane with no namespace still yields every part",
			key:           "exa:search|header:x-role=planner",
			wantNamespace: DefaultNamespace,
			wantParts:     []string{"exa:search", "header:x-role=planner"},
		},
		{
			name:          "an empty key has no parts",
			key:           "",
			wantNamespace: DefaultNamespace,
			wantParts:     nil,
		},
		{
			// A body value may contain '/'. The first segment is only read as a
			// namespace when it is a valid one, and every unprefixed lane key opens
			// with a route key or an "extractor=value" part, both of which carry a
			// ':' that ValidNamespace rejects. Without that check this key would be
			// filed under a namespace invented by an extracted URL.
			name:          "a slash inside an extracted value is not a namespace",
			key:           "exa:search|body_json:url=https://example.test/a",
			wantNamespace: DefaultNamespace,
			wantParts:     []string{"exa:search", "body_json:url=https://example.test/a"},
		},
		{
			name:          "a real namespace still splits ahead of a slashed value",
			key:           "t-42/exa:search|body_json:url=https://example.test/a",
			wantNamespace: "t-42",
			wantParts:     []string{"exa:search", "body_json:url=https://example.test/a"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ns, parts := SplitCursorKey(tc.key)
			require.Equal(t, tc.wantNamespace, ns)
			require.Equal(t, tc.wantParts, parts)
		})
	}
}

// TestSplitCursorKeyRoundTripsEveryLane is the property the fault engine relies
// on: whatever CursorKey composed, SplitCursorKey recovers the namespace and
// leaves the route key findable among the parts.
func TestSplitCursorKeyRoundTripsEveryLane(t *testing.T) {
	t.Parallel()

	for _, namespace := range []string{DefaultNamespace, "t-42", "T_9"} {
		for _, key := range []string{"exa:search", "perplexity:completions|body_json:model=sonar"} {
			lane := Lane{Namespace: namespace, Scenario: "s", Key: key}
			ns, parts := SplitCursorKey(lane.CursorKey())
			require.Equal(t, namespace, ns, "namespace survived the round trip")
			require.Contains(t, parts, strings.Split(key, "|")[0],
				"the route key is recoverable from the parts")
		}
	}
}
