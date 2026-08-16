package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/scenario"
)

// --- test doubles -----------------------------------------------------------

// capturingLogger records every structured record as text so a test can assert
// that a literal credential never reaches it.
type capturingLogger struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	logger *slog.Logger
}

func newCapturingLogger() *capturingLogger {
	c := &capturingLogger{}
	c.logger = slog.New(slog.NewTextHandler(&lockedWriter{c: c}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return c
}

func (c *capturingLogger) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

type lockedWriter struct{ c *capturingLogger }

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	return w.c.buf.Write(p)
}

// countingFaults claims indices and applies nothing, counting the claims so a
// test can prove a rejected request consumed no attempt.
type countingFaults struct {
	mu    sync.Mutex
	calls int
}

func (f *countingFaults) Next(key string) FaultDecision {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.calls
	f.calls++
	return FaultDecision{Index: idx, Key: key}
}

func (f *countingFaults) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = 0
}

// scriptedFaults hands out a fixed attempt list, which is what internal/faults
// does after expansion. It stands in for the engine so this package's tests do
// not depend on the package that imports it.
type scriptedFaults struct {
	mu       sync.Mutex
	attempts []scenario.FaultAttempt
	calls    int
	unknown  bool
}

func (f *scriptedFaults) Next(key string) FaultDecision {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unknown {
		return FaultDecision{Index: -1, Key: key, Unknown: true}
	}
	idx := f.calls
	f.calls++
	dec := FaultDecision{Index: idx, Key: key, Planned: len(f.attempts) > 0}
	if idx < len(f.attempts) {
		dec.Attempt = &f.attempts[idx]
	}
	return dec
}

func (f *scriptedFaults) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = 0
}

// okHandler is the simplest fault-eligible provider handler.
func okHandler(body string) Handler {
	return func(_ *Exchange) Response {
		return Response{
			Status:        http.StatusOK,
			Body:          []byte(body),
			Label:         "test.ok",
			FaultEligible: true,
		}
	}
}

var testRoute = Route{Pattern: "POST /search", FaultKey: "exa:search"}

// serve runs one in-process request through Handle with an httptest recorder.
func serve(d Deps, h Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	Handle(d, Exa, testRoute, h)(w, r)
	return w
}

func postJSON(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// --- tests ------------------------------------------------------------------

func TestHandleJournalsACompletedRequest(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(8, 4096)
	d := Deps{Journal: j}
	w := serve(d, okHandler(`{"ok":true}`), postJSON(`{"query":"climate"}`))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, `{"ok":true}`, w.Body.String())
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	e := entries[0]
	require.Equal(t, "exa", e.Provider)
	require.Equal(t, uint64(1), e.Seq, "sequence numbers are one-based")
	require.Equal(t, http.MethodPost, e.Method)
	require.Equal(t, "/search", e.Path)
	require.Equal(t, "POST /search", e.Route)
	require.False(t, e.ArrivedAt.IsZero())
	require.False(t, e.CompletedAt.Before(e.ArrivedAt))
	require.Equal(t, journal.OutcomeScenario, e.Outcome.Kind)
	require.Equal(t, "test.ok", e.Outcome.Label)
	require.Equal(t, http.StatusOK, e.Outcome.Status)
	require.Equal(t, len(`{"ok":true}`), e.Outcome.BytesWritten)
	require.Empty(t, e.Findings)
	require.JSONEq(t, `{"query":"climate"}`, string(e.Body))
}

// TestHandleJournalsEveryCredentialPlacement is the assertion the single-Header
// journal made unwritable: a client sending two credentials looked identical to
// one sending a single documented credential. A consumer proving "my adapter
// sends exactly the placement this vendor documents" needs to see both.
func TestHandleJournalsEveryCredentialPlacement(t *testing.T) {
	t.Parallel()

	t.Run("two placements are both recorded", func(t *testing.T) {
		t.Parallel()

		j := journal.NewRing(8, 4096)
		r := postJSON(`{"query":"climate"}`)
		r.Header.Set("Authorization", "Bearer test-key")
		r.Header.Set("x-api-key", "other-key")

		serve(Deps{Journal: j}, okHandler(`{"ok":true}`), r)

		e := j.Snapshot()[0]
		require.True(t, e.Auth.Present)
		require.Len(t, e.Auth.Placements, 2, "len(placements) is how 'exactly one' is asserted")

		placements := make([]string, 0, 2)
		for _, p := range e.Auth.Placements {
			placements = append(placements, p.Header)
			require.NotEmpty(t, p.Fingerprint, "a placement carries a fingerprint, never a value")
		}
		require.Equal(t, []string{"authorization", "x-api-key"}, placements)

		// Different keys must fingerprint differently, or a rotation test could
		// not tell the two placements apart.
		require.NotEqual(t, e.Auth.Placements[0].Fingerprint, e.Auth.Placements[1].Fingerprint)

		// The scalar fields still describe the first placement, so every existing
		// consumer of e.Auth.Header keeps reading what it always read.
		require.Equal(t, "authorization", e.Auth.Header)
		require.Equal(t, "Bearer", e.Auth.Scheme)
		require.Equal(t, e.Auth.Placements[0].Fingerprint, e.Auth.Fingerprint)
	})

	t.Run("one placement lists exactly one", func(t *testing.T) {
		t.Parallel()

		j := journal.NewRing(8, 4096)
		r := postJSON(`{"query":"climate"}`)
		r.Header.Set("Authorization", "Bearer test-key")

		serve(Deps{Journal: j}, okHandler(`{"ok":true}`), r)

		e := j.Snapshot()[0]
		require.Len(t, e.Auth.Placements, 1)
		require.Equal(t, "authorization", e.Auth.Placements[0].Header)
	})

	t.Run("no credential lists none", func(t *testing.T) {
		t.Parallel()

		j := journal.NewRing(8, 4096)
		serve(Deps{Journal: j}, okHandler(`{"ok":true}`), postJSON(`{"query":"climate"}`))

		e := j.Snapshot()[0]
		require.False(t, e.Auth.Present)
		require.Empty(t, e.Auth.Placements, "an absent credential must not journal an empty placement")
	})

	t.Run("a raw credential never reaches the journal by this path", func(t *testing.T) {
		t.Parallel()

		j := journal.NewRing(8, 4096)
		r := postJSON(`{"query":"climate"}`)
		r.Header.Set("x-api-key", "sk-live-should-never-appear")

		serve(Deps{Journal: j}, okHandler(`{"ok":true}`), r)

		encoded, err := json.Marshal(j.Snapshot()[0])
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "sk-live-should-never-appear")
	})
}

func TestHandleLoggerNeverSeesRawCredential(t *testing.T) {
	t.Parallel()

	const token = "sk-live-nevershipthis"

	capture := newCapturingLogger()
	j := journal.NewRing(8, 4096)
	r := postJSON(`{"query":"climate","api_key":"` + token + `"}`)
	r.Header.Set("Authorization", "Bearer "+token)

	serve(Deps{Journal: j, Logger: capture.logger}, okHandler(`{}`), r)

	require.NotContains(t, capture.String(), token,
		"redaction happens where the entry is built, not only where it is stored")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.NotContains(t, string(entries[0].Body), token)
	require.NotContains(t, strings.Join(entries[0].Headers["Authorization"], " "), token)

	// The observation still proves a credential arrived, without holding one.
	require.True(t, entries[0].Auth.Present)
	require.Equal(t, "authorization", entries[0].Auth.Header)
	require.Equal(t, "Bearer", entries[0].Auth.Scheme)
	require.NotEmpty(t, entries[0].Auth.Fingerprint)
	require.NotContains(t, entries[0].Auth.Fingerprint, token)
}

func TestHandleBodyFindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		limit     int64
		wantCode  string
		wantBody  bool
		wantParse string
	}{
		{name: "well-formed object decodes", body: `{"query":"x"}`, wantBody: true},
		{name: "absent body raises nothing", body: ``},
		{name: "whitespace body raises nothing", body: "  \n "},
		{
			name:      "malformed JSON",
			body:      `{"query":`,
			wantCode:  CodeMalformedJSON,
			wantParse: "request body is not valid JSON",
		},
		{
			name:      "valid JSON that is not an object",
			body:      `["query"]`,
			wantCode:  CodeBodyNotObject,
			wantParse: "request body is not a JSON object",
		},
		{
			name:      "body over the limit",
			body:      `{"query":"aaaaaaaaaaaaaaaaaaaa"}`,
			limit:     8,
			wantCode:  CodeBodyTooLarge,
			wantParse: "request body exceeds the configured limit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			j := journal.NewRing(8, 4096)
			var seen *Exchange
			h := func(x *Exchange) Response {
				seen = x
				return Response{Status: http.StatusOK, Body: []byte(`{}`), FaultEligible: !x.Failed()}
			}
			serve(Deps{Journal: j, MaxRequestBytes: tc.limit}, h, postJSON(tc.body))

			require.NotNil(t, seen)
			if tc.wantCode == "" {
				require.False(t, seen.Failed())
				require.Empty(t, seen.Findings())
			} else {
				require.True(t, seen.Failed())
				require.True(t, seen.HasFinding(tc.wantCode), "want %s, got %#v", tc.wantCode, seen.Findings())
			}
			require.Equal(t, tc.wantBody, seen.Body != nil)

			entries := j.Snapshot()
			require.Len(t, entries, 1)
			require.Equal(t, tc.wantParse, entries[0].BodyParseError)
		})
	}
}

func TestHandleRejectedRequestConsumesNoAttempt(t *testing.T) {
	t.Parallel()

	engine := &countingFaults{}
	h := func(x *Exchange) Response {
		x.Fail("test.rejected", "query", "query is required")
		return Response{Status: http.StatusBadRequest, Body: []byte(`{"error":"bad"}`), FaultEligible: false}
	}
	j := journal.NewRing(8, 4096)
	serve(Deps{Journal: j, Faults: engine}, h, postJSON(`{}`))

	require.Zero(t, engine.calls, "a rejected request must not eat a retry budget")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.Equal(t, journal.OutcomeError, entries[0].Outcome.Kind)
	require.Equal(t, -1, entries[0].Outcome.AttemptIndex)
}

func TestHandleValidationHasTheLastWordOnFaultEligibility(t *testing.T) {
	t.Parallel()

	// A provider package that forgets to clear FaultEligible on a rejected request
	// would otherwise eat a retry budget and journal the rejection as a scenario
	// response.
	engine := &countingFaults{}
	h := func(x *Exchange) Response {
		x.Fail("test.rejected", "query", "query is required")
		return Response{Status: http.StatusBadRequest, Body: []byte(`{"error":"bad"}`), FaultEligible: true}
	}
	j := journal.NewRing(8, 4096)
	serve(Deps{Journal: j, Faults: engine}, h, postJSON(`{}`))

	require.Zero(t, engine.calls)
	require.Equal(t, journal.OutcomeError, j.Snapshot()[0].Outcome.Kind)
}

func TestHandleUnknownFaultKeyWarns(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(8, 4096)
	serve(Deps{Journal: j, Faults: &scriptedFaults{unknown: true}}, okHandler(`{}`), postJSON(`{}`))

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.Len(t, entries[0].Warnings(), 1)
	require.Equal(t, CodeUnknownFaultKey, entries[0].Warnings()[0].Code)
	require.Equal(t, http.StatusOK, entries[0].Outcome.Status,
		"an unknown key still serves the scenario response; the warning is the evidence")
}

func TestHandleStatusFault(t *testing.T) {
	t.Parallel()

	retryAfter := 3
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{
		Status:     http.StatusTooManyRequests,
		RetryAfter: &retryAfter,
		Body:       map[string]any{"error": "rate limited"},
	}}}
	j := journal.NewRing(8, 4096)
	w := serve(Deps{Journal: j, Faults: engine}, okHandler(`{"ok":true}`), postJSON(`{}`))

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "3", w.Header().Get("Retry-After"))
	require.JSONEq(t, `{"error":"rate limited"}`, w.Body.String())

	e := j.Snapshot()[0]
	require.Equal(t, journal.OutcomeFault, e.Outcome.Kind)
	require.Equal(t, string(scenario.FaultStatus), e.Outcome.FaultKind)
	require.Equal(t, http.StatusTooManyRequests, e.Outcome.Status)
	require.Equal(t, "exa:search", e.Outcome.FaultKey)
	require.Equal(t, 0, e.Outcome.AttemptIndex)
}

func TestHandleStatusFaultUsesTheProviderShapedBody(t *testing.T) {
	t.Parallel()

	// §2.5: the body for a status fault is provider-shaped and built by the
	// provider package. It cannot be built before the fault is known, so the
	// handler supplies the builder and execution calls it.
	h := func(_ *Exchange) Response {
		return Response{
			Status:        http.StatusOK,
			Body:          []byte(`{"ok":true}`),
			FaultEligible: true,
			FaultBody: func(a scenario.FaultAttempt) []byte {
				return []byte(`{"requestId":"abc","error":"` + strconv.Itoa(a.Status) + `","tag":"RATE_LIMIT"}`)
			},
		}
	}
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{Status: http.StatusTooManyRequests}}}
	w := serve(Deps{Faults: engine}, h, postJSON(`{}`))

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.JSONEq(t, `{"requestId":"abc","error":"429","tag":"RATE_LIMIT"}`, w.Body.String())
}

func TestHandleBodyManglingFaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		attempt     scenario.FaultAttempt
		wantStatus  int
		wantBody    string
		wantType    string
		wantKind    scenario.FaultKind
		wantWritten int
	}{
		{
			name:        "invalid_json sends unparseable bytes under a JSON content type",
			attempt:     scenario.FaultAttempt{Kind: scenario.FaultInvalidJSON},
			wantStatus:  http.StatusOK,
			wantBody:    defaultInvalidJSONBody,
			wantType:    "application/json",
			wantKind:    scenario.FaultInvalidJSON,
			wantWritten: len(defaultInvalidJSONBody),
		},
		{
			name:        "invalid_json honours raw_body",
			attempt:     scenario.FaultAttempt{RawBody: "not json at all"},
			wantStatus:  http.StatusOK,
			wantBody:    "not json at all",
			wantType:    "application/json",
			wantKind:    scenario.FaultInvalidJSON,
			wantWritten: len("not json at all"),
		},
		{
			name:        "wrong_content_type keeps the valid body",
			attempt:     scenario.FaultAttempt{Kind: scenario.FaultWrongContentType},
			wantStatus:  http.StatusOK,
			wantBody:    `{"ok":true}`,
			wantType:    defaultWrongContentType,
			wantKind:    scenario.FaultWrongContentType,
			wantWritten: len(`{"ok":true}`),
		},
		{
			name:        "wrong_content_type honours an explicit override",
			attempt:     scenario.FaultAttempt{ContentType: "text/plain"},
			wantStatus:  http.StatusOK,
			wantBody:    `{"ok":true}`,
			wantType:    "text/plain",
			wantKind:    scenario.FaultWrongContentType,
			wantWritten: len(`{"ok":true}`),
		},
		{
			name:       "empty_body writes nothing",
			attempt:    scenario.FaultAttempt{Kind: scenario.FaultEmptyBody},
			wantStatus: http.StatusOK,
			wantBody:   "",
			wantType:   "application/json",
			wantKind:   scenario.FaultEmptyBody,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			j := journal.NewRing(8, 4096)
			engine := &scriptedFaults{attempts: []scenario.FaultAttempt{tc.attempt}}
			w := serve(Deps{Journal: j, Faults: engine}, okHandler(`{"ok":true}`), postJSON(`{}`))

			require.Equal(t, tc.wantStatus, w.Code)
			require.Equal(t, tc.wantBody, w.Body.String())
			require.Equal(t, tc.wantType, w.Header().Get("Content-Type"))

			e := j.Snapshot()[0]
			require.Equal(t, journal.OutcomeFault, e.Outcome.Kind)
			require.Equal(t, string(tc.wantKind), e.Outcome.FaultKind)
			require.Equal(t, tc.wantWritten, e.Outcome.BytesWritten)
		})
	}
}

func TestHandleExtraFieldsFaultMergesIntoTheBody(t *testing.T) {
	t.Parallel()

	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{
		Kind:        scenario.FaultExtraFields,
		ExtraFields: scenario.ExtraFields{"newField": "surprise"},
	}}}
	w := serve(Deps{Faults: engine}, okHandler(`{"ok":true}`), postJSON(`{}`))

	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"ok":true,"newField":"surprise"}`, w.Body.String())
}

func TestHandleDelayFault(t *testing.T) {
	t.Parallel()

	const declared = 30 * time.Second

	t.Run("skip records the requested delay without waiting", func(t *testing.T) {
		t.Parallel()

		j := journal.NewRing(8, 4096)
		engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{Delay: scenario.Duration(declared)}}}

		start := time.Now()
		w := serve(Deps{Journal: j, Faults: engine, DelayMode: DelaySkip}, okHandler(`{"ok":true}`), postJSON(`{}`))
		elapsed := time.Since(start)

		require.Less(t, elapsed, time.Second, "DelaySkip is what keeps a 30s backoff scenario free")
		require.Equal(t, http.StatusOK, w.Code)

		e := j.Snapshot()[0]
		require.Equal(t, declared.Milliseconds(), e.Outcome.DelayMS,
			"the journal records the requested delay under both modes")
		require.Equal(t, faultKindDelay, e.Outcome.FaultKind)
		require.Equal(t, journal.OutcomeFault, e.Outcome.Kind)
	})

	t.Run("real waits on the wire", func(t *testing.T) {
		t.Parallel()

		engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{Delay: scenario.Duration(20 * time.Millisecond)}}}
		start := time.Now()
		serve(Deps{Faults: engine}, okHandler(`{"ok":true}`), postJSON(`{}`))
		require.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)
	})

	t.Run("a cancelled client releases the goroutine", func(t *testing.T) {
		t.Parallel()

		j := journal.NewRing(8, 4096)
		engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{Delay: scenario.Duration(time.Hour)}}}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := postJSON(`{}`).WithContext(ctx)

		start := time.Now()
		serve(Deps{Journal: j, Faults: engine}, okHandler(`{"ok":true}`), r)
		require.Less(t, time.Since(start), time.Second)

		e := j.Snapshot()[0]
		require.True(t, e.Outcome.Aborted)
		require.Zero(t, e.Outcome.BytesWritten)
	})
}

func TestHandleCloseBeforeHeadersIsJournaledBeforeTheAbort(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{Kind: scenario.FaultCloseBeforeHeaders}}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(`{"ok":true}`)))
	defer srv.Close()

	_, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.Error(t, err, "the connection must die before any header reaches the client")

	// Read the journal at exactly the moment the client returned: the entry has to
	// be there already, or every consumer test that inspects it races the server.
	entries := j.Snapshot()
	require.Len(t, entries, 1, "an aborting fault is journaled before the socket is touched")
	require.True(t, entries[0].Outcome.Aborted)
	require.Zero(t, entries[0].Outcome.BytesWritten)
	require.Zero(t, entries[0].Outcome.Status, "nothing reached the client, so no status did either")
	require.Equal(t, string(scenario.FaultCloseBeforeHeaders), entries[0].Outcome.FaultKind)
}

// TestHandleAbortingFaultDelayIsObservedInCompletedAt is the Phase 6 unit 1
// regression test: for a non-streaming aborting attempt that also carries a
// delay:, the journaled CompletedAt must reflect the observed hang
// (completed_at - arrived_at >= delay), not the instant the attempt was
// decided (arrived_at ~ 0). The pre-existing journaled-before-the-abort
// property that TestHandleCloseBeforeHeadersIsJournaledBeforeTheAbort proves
// must keep holding with a delay in play: the synchronous Snapshot() taken
// immediately after the client's own transport error should still find
// exactly one entry, because record() still runs on the server goroutine
// strictly before the socket is touched — the hang now happens before that,
// not after it.
//
// The Snapshot()-after-error check is deterministic under the shipped
// ordering: record() runs on the server goroutine strictly before the socket
// is touched, so the entry exists before the client can observe anything.
// What is NOT deterministic is the check's power to catch a regression that
// moved the record after the socket touch: for close_before_headers and
// truncate_body-with-reset the RST would then race the deferred record and
// the check would fail only sometimes; for plain truncate_body the connection
// closes only after net/http unwinds through the deferred record, so the
// check could not tell that row's ordering from an ordering bug at all. It
// still exercises the DoD-1 property end to end on live sockets, which is
// why it stays.
func TestHandleAbortingFaultDelayIsObservedInCompletedAt(t *testing.T) {
	t.Parallel()

	const delay = 40 * time.Millisecond

	tests := []struct {
		name    string
		attempt scenario.FaultAttempt
	}{
		{
			name:    "close_before_headers",
			attempt: scenario.FaultAttempt{Kind: scenario.FaultCloseBeforeHeaders, Delay: scenario.Duration(delay)},
		},
		{
			name:    "truncate_body",
			attempt: scenario.FaultAttempt{Kind: scenario.FaultTruncateBody, Delay: scenario.Duration(delay)},
		},
		{
			name: "truncate_body with reset",
			attempt: scenario.FaultAttempt{
				Kind: scenario.FaultTruncateBody, Delay: scenario.Duration(delay), Reset: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			j := journal.NewRing(8, 4096)
			engine := &scriptedFaults{attempts: []scenario.FaultAttempt{tc.attempt}}
			srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(`{"ok":true}`)))
			defer srv.Close()

			start := time.Now()
			resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
			if err == nil {
				// truncate_body delivers headers and a prefix successfully; the
				// failure surfaces on the body read, not on Post itself.
				_, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
			require.Error(t, err, "the connection must die before the body completes")
			require.GreaterOrEqual(t, time.Since(start), delay, "the client itself must observe the hang")

			// Read the journal at exactly the moment the client returned: the entry
			// has to be there already, and it has to say the hang actually happened.
			entries := j.Snapshot()
			require.Len(t, entries, 1, "an aborting fault is journaled before the socket is touched")
			require.True(t, entries[0].Outcome.Aborted)
			observed := entries[0].CompletedAt.Sub(entries[0].ArrivedAt)
			require.GreaterOrEqual(t, observed, delay,
				"completed_at must reflect the observed hang, not the instant the attempt was decided")
		})
	}
}

// TestHandleAbortingFaultDelaySkipRecordsRequestedDelayWithoutWaiting proves
// requirement 2: under DelaySkip no real time passes for an aborting fault's
// delay:, and Outcome.DelayMS still records what was requested — unchanged by
// the unit 1 reordering, which only moves WHEN record() runs, never what
// faultOutcome computed.
func TestHandleAbortingFaultDelaySkipRecordsRequestedDelayWithoutWaiting(t *testing.T) {
	t.Parallel()

	const declared = 30 * time.Second

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultCloseBeforeHeaders, Delay: scenario.Duration(declared)},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine, DelayMode: DelaySkip},
		Exa, testRoute, okHandler(`{"ok":true}`)))
	defer srv.Close()

	start := time.Now()
	_, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.Error(t, err, "the connection must die before any header reaches the client")
	require.Less(t, time.Since(start), time.Second, "DelaySkip must not wait out a 30s aborting-fault delay")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.True(t, entries[0].Outcome.Aborted)
	require.Equal(t, declared.Milliseconds(), entries[0].Outcome.DelayMS,
		"the journal still records the requested delay under DelaySkip")
}

// TestHandleAbortingFaultCancelledDuringDelayLandsAnAbortedEntry proves
// requirement 3: when the CLIENT's own cancellation ends the request during
// an aborting attempt's pre-dispatch hang, nothing reaches the wire, but the
// entry still lands with Outcome.Aborted true and BytesWritten zero — this is
// the symmetrical, client-created case package-design.md §2.2 rule 3 already
// names, and the entry landing (even though the socket was never touched)
// matters as much here as journaling before the scripted abort does.
// Deterministic and instant: the request context is already cancelled before
// Handle ever runs, exactly as TestHandleDelayFault/"a cancelled client
// releases the goroutine" does, so sleep()'s select observes ctx.Done()
// immediately rather than waiting out the scripted one-hour delay.
//
// truncate_body is the row that actually discriminates the cancel branch:
// faultOutcome pre-sets Aborted=true and BytesWritten=0 for close_before_headers
// regardless of the delay, so that row alone would pass unchanged even if
// Handle's cancel branch (handle.go's preDispatchDelay error path) were
// deleted outright. faultOutcome pre-sets BytesWritten to the non-zero
// truncation length for truncate_body, so only that row's BytesWritten==0
// assertion depends on the branch actually zeroing it. CompletedAt is not
// asserted here: with a pre-cancelled context it lands at essentially
// ArrivedAt by construction, so it proves nothing beyond what Len/Aborted/
// BytesWritten already do; the observed-hang property is covered separately
// by TestHandleAbortingFaultDelayIsObservedInCompletedAt.
func TestHandleAbortingFaultCancelledDuringDelayLandsAnAbortedEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		attempt scenario.FaultAttempt
	}{
		{
			name:    "close_before_headers",
			attempt: scenario.FaultAttempt{Kind: scenario.FaultCloseBeforeHeaders, Delay: scenario.Duration(time.Hour)},
		},
		{
			name:    "truncate_body",
			attempt: scenario.FaultAttempt{Kind: scenario.FaultTruncateBody, Delay: scenario.Duration(time.Hour)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			j := journal.NewRing(8, 4096)
			engine := &scriptedFaults{attempts: []scenario.FaultAttempt{tc.attempt}}

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			r := postJSON(`{}`).WithContext(ctx)

			start := time.Now()
			require.NotPanics(t, func() {
				serve(Deps{Journal: j, Faults: engine}, okHandler(`{"ok":true}`), r)
			})
			require.Less(t, time.Since(start), time.Second, "the client's own cancellation must release the goroutine")

			entries := j.Snapshot()
			require.Len(t, entries, 1, "the client's own cancellation is not a reason to lose the entry")
			require.True(t, entries[0].Outcome.Aborted)
			require.Zero(t, entries[0].Outcome.BytesWritten)
		})
	}
}

func TestHandleTruncateBody(t *testing.T) {
	t.Parallel()

	const body = `{"results":[{"title":"a full and complete response body"}]}`

	tests := []struct {
		name    string
		attempt scenario.FaultAttempt
		wantN   int
	}{
		{
			name:    "explicit truncation length",
			attempt: scenario.FaultAttempt{Kind: scenario.FaultTruncateBody, TruncateAfterBytes: 20},
			wantN:   20,
		},
		{
			name:    "zero means half the body",
			attempt: scenario.FaultAttempt{Kind: scenario.FaultTruncateBody},
			wantN:   len(body) / 2,
		},
		{
			name:    "reset sends a RST after the same prefix",
			attempt: scenario.FaultAttempt{Kind: scenario.FaultTruncateBody, TruncateAfterBytes: 20, Reset: true},
			wantN:   20,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			j := journal.NewRing(8, 4096)
			engine := &scriptedFaults{attempts: []scenario.FaultAttempt{tc.attempt}}
			srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(body)))
			defer srv.Close()

			resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
			require.NoError(t, err, "headers and a prefix must arrive; only the tail is missing")
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, strconv.Itoa(len(body)), resp.Header.Get("Content-Length"),
				"the declared length is the full body, which is what makes this a truncation")

			read, err := io.ReadAll(resp.Body)
			require.Error(t, err, "the body must not complete")
			require.Equal(t, body[:tc.wantN], string(read))

			entries := j.Snapshot()
			require.Len(t, entries, 1)
			require.True(t, entries[0].Outcome.Aborted)
			require.Equal(t, tc.wantN, entries[0].Outcome.BytesWritten)
			require.Equal(t, string(scenario.FaultTruncateBody), entries[0].Outcome.FaultKind)
		})
	}
}

func TestHandleJournalsAPanickingHandlerAndRepanics(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(8, 4096)
	boom := errors.New("boom")
	h := func(_ *Exchange) Response { panic(boom) }

	require.PanicsWithValue(t, boom, func() {
		serve(Deps{Journal: j}, h, postJSON(`{}`))
	}, "swallowing the panic would turn an abort fault into a 200 with an empty body")

	require.Len(t, j.Snapshot(), 1, "the entry survives because the append is deferred")
}

func TestHandleRecordIsIdempotent(t *testing.T) {
	t.Parallel()

	// close_before_headers records early and then unwinds through the deferred
	// record. Exactly one entry must exist afterwards.
	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{Kind: scenario.FaultCloseBeforeHeaders}}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(`{}`)))
	defer srv.Close()

	_, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.Error(t, err)

	require.Eventually(t, func() bool { return j.Stats().Appended >= 1 }, time.Second, time.Millisecond)
	require.Equal(t, uint64(1), j.Stats().Appended, "record must append once, not twice")
}

func TestHandleNilHandlerFailsClosed(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(8, 4096)
	w := serve(Deps{Journal: j}, nil, postJSON(`{}`))

	require.Equal(t, http.StatusInternalServerError, w.Code)
	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.Equal(t, CodeNoHandler, entries[0].Findings[0].Code)
}

func TestHandleIsRaceFree(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(64, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Status: http.StatusTooManyRequests}, {Status: http.StatusTooManyRequests},
	}}
	handler := Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(`{"ok":true}`))

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			handler(httptest.NewRecorder(), postJSON(`{"query":"x"}`))
		}()
	}
	wg.Wait()

	require.Len(t, j.Snapshot(), n)

	// The counter counts arrivals, so exactly two requests are faulted — but which
	// two is deliberately unspecified for concurrent arrivals (§4.4).
	faulted := 0
	for _, e := range j.Snapshot() {
		if e.Outcome.Kind == journal.OutcomeFault {
			faulted++
		}
	}
	require.Equal(t, 2, faulted)
}
