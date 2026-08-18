package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
)

// testSpec is a provider-shaped MuxSpec: two routes on one path, one route on
// another. NewMux's 404/405 bodies come from testExaRefuse, passed at the
// call site — MuxSpec itself no longer carries them (Phase 10 unit 2).
func testSpec() MuxSpec {
	return MuxSpec{
		Routes: []Route{
			{Pattern: "POST /search", FaultKey: "exa:search"},
			{Pattern: "POST /answer", FaultKey: "exa:answer"},
		},
		Handlers: map[string]Handler{
			"POST /search": okHandler(`{"route":"search"}`),
			"POST /answer": okHandler(`{"route":"answer"}`),
		},
	}
}

// testExaRefuse renders a Refusal the way an Exa-shaped profile's ErrorBody
// would: a fixed body per kind, so a test asserting on 404/405 body content
// has something real to assert against.
func testExaRefuse(r Refusal) []byte {
	switch r.Kind {
	case RefuseNotFound:
		return []byte(`{"error":"Not Found","tag":"NOT_FOUND"}`)
	case RefuseMethodNotAllowed:
		return []byte(`{"error":"Method Not Allowed","tag":"METHOD_NOT_ALLOWED"}`)
	default:
		return []byte(`{"error":"Internal Server Error","tag":"INTERNAL_ERROR"}`)
	}
}

// --- a served HEAD on a poll-shaped route ----------------------------------

// pollSpec is the async poll shape: a GET whose lane is per job and a HEAD on
// the same path with its OWN fault key and its own handler.
//
// The HEAD handler must not be the GET handler. Go routes HEAD to a GET pattern
// automatically, and for a poll that means turn selection runs, an attempt is
// claimed and the job's cursor advances — for a request whose body net/http then
// discards. One existence check would silently consume a poll.
func pollSpec(get, head Handler) MuxSpec {
	return MuxSpec{
		Routes: []Route{
			{
				Pattern:  "GET /runs/{id}",
				FaultKey: "exa:runs.poll",
				LaneFrom: []string{LaneFromPath + "id"},
			},
			{
				// Its own key: a HEAD must not draw on the poll budget for the same
				// reason it must not advance the cursor.
				Pattern:  "HEAD /runs/{id}",
				FaultKey: "exa:runs.head",
				LaneFrom: []string{LaneFromPath + "id"},
			},
		},
		Handlers: map[string]Handler{
			"GET /runs/{id}":  get,
			"HEAD /runs/{id}": head,
		},
	}
}

// TestHeadIsServedByItsOwnHandler pins the mechanism. Registering "HEAD /p"
// alongside "GET /p" needs NO change to NewMux: the registration loop splits on
// the method, so the pattern registers like any other and paths[path] picks HEAD
// up for the Allow header for free.
func TestHeadIsServedByItsOwnHandler(t *testing.T) {
	t.Parallel()

	var getCalls, headCalls int
	spec := pollSpec(
		func(_ *Exchange) Response {
			getCalls++
			return Response{Status: http.StatusOK, Body: []byte(`{"status":"running"}`), Label: "get"}
		},
		func(_ *Exchange) Response {
			headCalls++
			return Response{Status: http.StatusOK, Label: "head"}
		},
	)
	mux := NewMux(Deps{}, testProviderExa, testRefuse, spec)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/runs/run_a", nil))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, headCalls, "HEAD must reach its own handler")
	require.Zero(t, getCalls, "HEAD must not fall through to the GET handler")
}

// The Allow header advertises HEAD because the route genuinely serves it. This
// is the half an earlier design got right for the wrong reason: it wanted to
// advertise HEAD while still letting it reach the GET handler.
func TestHeadAppearsInAllow(t *testing.T) {
	t.Parallel()

	spec := pollSpec(
		okHandler(`{"status":"running"}`),
		func(_ *Exchange) Response { return Response{Status: http.StatusOK, Label: "head"} },
	)
	mux := NewMux(Deps{}, testProviderExa, testRefuse, spec)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/runs/run_a", nil))

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Equal(t, "GET, HEAD", w.Header().Get("Allow"))
}

// TestHeadDoesNotAdvanceThePollCursor is the test that actually guards the bug.
//
// A status-code assertion alone would still pass if the dedicated handler were
// removed, because HEAD reaching the GET handler ALSO returns 200. Only the
// following GET's call index can tell the two apart.
func TestHeadDoesNotAdvanceThePollCursor(t *testing.T) {
	t.Parallel()

	var seen []int
	spec := pollSpec(
		func(x *Exchange) Response {
			seen = append(seen, x.CallIndex())
			return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "get", FaultEligible: true}
		},
		func(_ *Exchange) Response {
			// A real headJob resolves the job and claims nothing. Claiming nothing
			// is the property under test.
			return Response{Status: http.StatusOK, Label: "head"}
		},
	)
	mux := NewMux(Deps{}, testProviderExa, testRefuse, spec)

	for range 3 {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/runs/run_a", nil))
		require.Equal(t, http.StatusOK, w.Code)
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/runs/run_a", nil))
	require.Equal(t, http.StatusOK, w.Code)

	require.Equal(t, []int{0}, seen,
		"three HEADs consumed %d polls; the first real GET must still be poll 0", len(seen))
}

// Two jobs polled through one route keep separate cursors, which is what
// Route.LaneFrom exists for. Without it both would draw from one sequence and
// each poll would receive the snapshot scripted for the other job.
func TestPollCursorsArePerJob(t *testing.T) {
	t.Parallel()

	byID := map[string][]int{}
	spec := pollSpec(
		func(x *Exchange) Response {
			id := x.Request.PathValue("id")
			byID[id] = append(byID[id], x.CallIndex())
			return Response{Status: http.StatusOK, Body: []byte(`{}`), Label: "get", FaultEligible: true}
		},
		func(_ *Exchange) Response { return Response{Status: http.StatusOK, Label: "head"} },
	)
	mux := NewMux(Deps{}, testProviderExa, testRefuse, spec)

	for _, id := range []string{"run_a", "run_b", "run_a", "run_b", "run_a"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/runs/"+id, nil))
		require.Equal(t, http.StatusOK, w.Code)
	}

	require.Equal(t, []int{0, 1, 2}, byID["run_a"])
	require.Equal(t, []int{0, 1}, byID["run_b"], "run_b's polls must not be numbered by run_a's")
}

// TestNewMuxRoutingTable is §5.1's verification table, verbatim. It runs once,
// against NewMux, rather than three times against three copies of the
// registration logic.
func TestNewMuxRoutingTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantAllow  string
		wantBody   string
		wantCode   string
	}{
		{
			name: "the handler", method: http.MethodPost, path: "/search",
			wantStatus: http.StatusOK, wantBody: `{"route":"search"}`,
		},
		{
			name: "GET on a real path is 405, not 404", method: http.MethodGet, path: "/search",
			wantStatus: http.StatusMethodNotAllowed, wantAllow: "POST",
			wantCode: CodeMethodNotAllowed,
		},
		{
			name: "PUT on a real path is 405", method: http.MethodPut, path: "/search",
			wantStatus: http.StatusMethodNotAllowed, wantAllow: "POST",
			wantCode: CodeMethodNotAllowed,
		},
		{
			name: "an unknown path is 404", method: http.MethodPost, path: "/nope",
			wantStatus: http.StatusNotFound, wantCode: CodeUnmatched,
		},
		{
			name: "the root is 404", method: http.MethodGet, path: "/",
			wantStatus: http.StatusNotFound, wantCode: CodeUnmatched,
		},
		{
			name: "a trailing slash is 404, not a redirect", method: http.MethodPost, path: "/search/",
			wantStatus: http.StatusNotFound, wantCode: CodeUnmatched,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			j := journal.NewRing(8, 4096)
			mux := NewMux(Deps{Journal: j}, testProviderExa, testExaRefuse, testSpec())

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))

			require.Equal(t, tc.wantStatus, w.Code)
			if tc.wantAllow != "" {
				require.Equal(t, tc.wantAllow, w.Header().Get("Allow"))
			}
			if tc.wantBody != "" {
				require.JSONEq(t, tc.wantBody, w.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				// Fail closed, never proxy: an error body a consumer's decoder can
				// parse, not net/http's text/plain default.
				require.Equal(t, "application/json", w.Header().Get("Content-Type"))
				require.True(t, json.Valid(w.Body.Bytes()), "error bodies are provider-shaped JSON")
			}

			entries := j.Snapshot()
			require.Len(t, entries, 1, "unmatched traffic is journaled like everything else")
			if tc.wantCode == "" {
				require.Equal(t, journal.OutcomeScenario, entries[0].Outcome.Kind)
				return
			}
			require.Equal(t, journal.OutcomeUnmatched, entries[0].Outcome.Kind)
			require.Len(t, entries[0].Errors(), 1)
			require.Equal(t, tc.wantCode, entries[0].Errors()[0].Code)
		})
	}
}

func TestNewMuxAllowHeaderIsSorted(t *testing.T) {
	t.Parallel()

	// Registration walks a map[path][]method. Sorted iteration is what stops the
	// Allow header differing run to run (§3.3).
	spec := MuxSpec{
		Routes: []Route{
			{Pattern: "PUT /search", FaultKey: "k"},
			{Pattern: "POST /search", FaultKey: "k"},
			{Pattern: "DELETE /search", FaultKey: "k"},
		},
		Handlers: map[string]Handler{
			"PUT /search":    okHandler(`{}`),
			"POST /search":   okHandler(`{}`),
			"DELETE /search": okHandler(`{}`),
		},
	}

	for range 20 {
		mux := NewMux(Deps{}, testProviderExa, testRefuse, spec)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/search", nil))

		require.Equal(t, http.StatusMethodNotAllowed, w.Code)
		require.Equal(t, "DELETE, POST, PUT", w.Header().Get("Allow"))
	}
}

func TestNewMuxSuppliesAllowAndFindingWhenTheProviderDoesNot(t *testing.T) {
	t.Parallel()

	// A provider package that forgets the Allow header or the finding would still
	// pass its own tests and fail scripts/image-smoke.sh. NewMux guarantees both.
	spec := MuxSpec{
		Routes:   []Route{{Pattern: "POST /search", FaultKey: "k"}},
		Handlers: map[string]Handler{"POST /search": okHandler(`{}`)},
	}
	j := journal.NewRing(8, 4096)
	mux := NewMux(Deps{Journal: j}, testProviderExa, testRefuse, spec)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/search", nil))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Equal(t, "POST", w.Header().Get("Allow"))

	require.Equal(t, CodeMethodNotAllowed, j.Snapshot()[0].Errors()[0].Code)
}

func TestNewMuxUnmatchedConsumesNoFaultBudget(t *testing.T) {
	t.Parallel()

	engine := &countingFaults{}
	mux := NewMux(Deps{Faults: engine}, testProviderExa, testExaRefuse, testSpec())

	for _, target := range []string{"/nope", "/search/"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, target, nil))
		require.Equal(t, http.StatusNotFound, w.Code)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/search", nil))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)

	require.Zero(t, engine.calls, "a 404 or 405 must leave the attempt counter untouched")
}

func TestNewMuxSharesOneJournalSequenceAcrossRoutes(t *testing.T) {
	t.Parallel()

	// Deps is normalised once, in NewMux, so two routes on one listener cannot
	// draw sequence numbers from two different substitute journals.
	j := journal.NewRing(8, 4096)
	mux := NewMux(Deps{Journal: j}, testProviderExa, testExaRefuse, testSpec())

	for _, path := range []string{"/search", "/answer", "/search"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		require.Equal(t, http.StatusOK, w.Code)
	}

	entries := j.Snapshot()
	require.Len(t, entries, 3)
	require.Equal(t, []uint64{1, 2, 3}, []uint64{entries[0].Seq, entries[1].Seq, entries[2].Seq})
}
