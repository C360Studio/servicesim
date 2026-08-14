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
// another, and the two error-body builders a real provider package supplies.
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
		NotFound: func(_ *Exchange) Response {
			return Response{
				Status: http.StatusNotFound,
				Body:   []byte(`{"error":"Not Found","tag":"NOT_FOUND"}`),
				Label:  "exa.error.NOT_FOUND",
			}
		},
		MethodNotAllowed: func(allow []string) Handler {
			return func(_ *Exchange) Response {
				return Response{
					Status: http.StatusMethodNotAllowed,
					Header: http.Header{"Allow": []string{strings.Join(allow, ", ")}},
					Body:   []byte(`{"error":"Method Not Allowed","tag":"METHOD_NOT_ALLOWED"}`),
					Label:  "exa.error.METHOD_NOT_ALLOWED",
				}
			}
		},
	}
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
			mux := NewMux(Deps{Journal: j}, Exa, testSpec())

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
		NotFound:         func(_ *Exchange) Response { return Response{Status: http.StatusNotFound} },
		MethodNotAllowed: func(_ []string) Handler { return func(_ *Exchange) Response { return Response{Status: 405} } },
	}

	for range 20 {
		mux := NewMux(Deps{}, Exa, spec)
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
	mux := NewMux(Deps{Journal: j}, Exa, spec)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/search", nil))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Equal(t, "POST", w.Header().Get("Allow"))

	require.Equal(t, CodeMethodNotAllowed, j.Snapshot()[0].Errors()[0].Code)
}

func TestNewMuxUnmatchedConsumesNoFaultBudget(t *testing.T) {
	t.Parallel()

	engine := &countingFaults{}
	mux := NewMux(Deps{Faults: engine}, Exa, testSpec())

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
	mux := NewMux(Deps{Journal: j}, Exa, testSpec())

	for _, path := range []string{"/search", "/answer", "/search"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		require.Equal(t, http.StatusOK, w.Code)
	}

	entries := j.Snapshot()
	require.Len(t, entries, 3)
	require.Equal(t, []uint64{1, 2, 3}, []uint64{entries[0].Seq, entries[1].Seq, entries[2].Seq})
}
