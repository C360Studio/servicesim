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
	"runtime"
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

// TestHandleCloseBeforeHeadersWithDelayAfterHeadersStillRecordsEarly is a
// regression test for a gap review found while building the deferred-record
// design decision: scenario.Validate rejects close_before_headers combined
// with delay_after_headers (scenario.fault.delay_after_headers.no_headers),
// so a loaded scenario can never present Handle with this combination — but
// a hand-built *scenario.FaultAttempt constructed directly in Go, the way
// this test's scriptedFaults does, bypasses that validation entirely.
// deferAbortRecord (handle.go) must not defer the record for this
// combination: closeBeforeHeaders never calls recordAbort (it has no
// after-headers hang to wait out — the connection dies immediately), so
// deferring here would silently lose the entry rather than merely journal
// it later. This asserts the entry is still present the moment the client
// observes the reset, exactly as TestHandleCloseBeforeHeadersIsJournaledBeforeTheAbort
// does for the ordinary (no delay_after_headers) case.
func TestHandleCloseBeforeHeadersWithDelayAfterHeadersStillRecordsEarly(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultCloseBeforeHeaders, DelayAfterHeaders: scenario.Duration(time.Hour)},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(`{"ok":true}`)))
	defer srv.Close()

	start := time.Now()
	_, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.Error(t, err, "the connection must die before any header reaches the client")
	require.Less(t, time.Since(start), time.Second,
		"close_before_headers must not actually hang: it never reaches afterHeadersDelay")

	entries := j.Snapshot()
	require.Len(t, entries, 1, "the entry must not be lost: close_before_headers cannot defer its own record")
	require.True(t, entries[0].Outcome.Aborted)
	require.Zero(t, entries[0].Outcome.BytesWritten)
	require.Equal(t, string(scenario.FaultCloseBeforeHeaders), entries[0].Outcome.FaultKind)
}

// TestHandleDelayAfterHeadersHeadersArriveBeforeTheHang is Phase 6 unit 5's
// DoD (a): the status line and headers reach the client well before the
// after-headers hang completes, and the body arrives only once it does. A
// bare delay_after_headers attempt (no other kind) still faults the request
// exactly as a bare delay: attempt does. Content-Length is exact and
// declared before the flush, exactly as the un-delayed writeResponse path
// declares it — without that, afterHeadersDelay's Flush would commit the
// headers before net/http can infer a length from the completed write, and
// the response would go out as Transfer-Encoding: chunked instead.
// headersTimestampSkew is the tolerance subtracted from delay when a test
// timestamps headersAt AFTER client.Do has already returned: capturing that
// timestamp costs a little real time itself (network read completion,
// goroutine scheduling), and that time is never available to the ensuing
// "body arrives >= delay after headersAt" measurement, so a hang timed to
// the millisecond can otherwise read a few hundred microseconds short of
// delay under normal scheduling jitter — not a hang that ran short. The
// margin is small next to every delay these tests use (150ms), so it keeps
// full power to catch a mutant that shifts timing by the delay itself.
const headersTimestampSkew = 25 * time.Millisecond

func TestHandleDelayAfterHeadersHeadersArriveBeforeTheHang(t *testing.T) {
	t.Parallel()

	const delay = 150 * time.Millisecond
	const body = `{"ok":true}`

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{DelayAfterHeaders: scenario.Duration(delay)}}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(body)))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	headersAt := time.Now()
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, strconv.Itoa(len(body)), resp.Header.Get("Content-Length"),
		"Content-Length must be exact and declared before the flush, exactly as the un-delayed path declares it")
	require.Empty(t, resp.TransferEncoding, "the hang must not change the response to chunked framing")

	read, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(read))
	require.GreaterOrEqual(t, time.Since(headersAt), delay-headersTimestampSkew,
		"the body must arrive at least the hang after the headers did — a one-directional proof that headers "+
			"were not themselves delayed until after the hang")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.Equal(t, delay.Milliseconds(), entries[0].Outcome.DelayAfterHeadersMS)
	require.Equal(t, journal.OutcomeFault, entries[0].Outcome.Kind,
		"a bare delay_after_headers attempt still faulted the request, the way a bare delay: attempt does")
	require.Equal(t, faultKindDelay, entries[0].Outcome.FaultKind)
}

// TestHandleDelayAndDelayAfterHeadersCompose is DoD (c): delay: and
// delay_after_headers: both apply on one attempt — hang, then headers, then
// hang again — and both are reported on the journal entry.
func TestHandleDelayAndDelayAfterHeadersCompose(t *testing.T) {
	t.Parallel()

	const preDelay = 60 * time.Millisecond
	const afterDelay = 60 * time.Millisecond
	const body = `{"ok":true}`

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Delay: scenario.Duration(preDelay), DelayAfterHeaders: scenario.Duration(afterDelay)},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(body)))
	defer srv.Close()

	start := time.Now()
	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	headersElapsed := time.Since(start)
	defer func() { _ = resp.Body.Close() }()

	require.GreaterOrEqual(t, headersElapsed, preDelay, "headers themselves must wait out the pre-dispatch hang")

	read, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(read))
	require.GreaterOrEqual(t, time.Since(start), preDelay+afterDelay, "the body must wait out both hangs, back to back")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.Equal(t, preDelay.Milliseconds(), entries[0].Outcome.DelayMS)
	require.Equal(t, afterDelay.Milliseconds(), entries[0].Outcome.DelayAfterHeadersMS)
}

// TestHandleDelayAfterHeadersDelaySkipRecordsBothWithoutWaiting is DoD (d):
// under DelaySkip neither hang actually waits, and the journal still records
// both requested durations — unaffected by DelayMode, exactly as DelayMS
// already is.
func TestHandleDelayAfterHeadersDelaySkipRecordsBothWithoutWaiting(t *testing.T) {
	t.Parallel()

	const preDelay = 10 * time.Second
	const afterDelay = 10 * time.Second

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Delay: scenario.Duration(preDelay), DelayAfterHeaders: scenario.Duration(afterDelay)},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine, DelayMode: DelaySkip},
		Exa, testRoute, okHandler(`{"ok":true}`)))
	defer srv.Close()

	start := time.Now()
	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Less(t, time.Since(start), time.Second, "DelaySkip must not wait out either hang")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.Equal(t, preDelay.Milliseconds(), entries[0].Outcome.DelayMS)
	require.Equal(t, afterDelay.Milliseconds(), entries[0].Outcome.DelayAfterHeadersMS)
}

// TestHandleDelayAfterHeadersClientCancelledDuringHangLandsAbortedEntry is
// DoD (e). Pre-cancelling the context, the way
// TestHandleAbortingFaultCancelledDuringDelayLandsAnAbortedEntry does for the
// pre-dispatch hang, is not possible here: the whole point is that headers
// have already gone out over a real socket before the client cancels, so
// this needs a live client whose context is cancelled only after Do returns
// them. Cancelling an in-flight request's context closes the underlying
// connection even once headers have been read but the body has not, which is
// what the server observes as its own r.Context() ending.
//
// The context carries a generous 5s deadline, not an unbounded one: a
// regression that stops flushing headers before the hang (so Do blocks for
// the full one-hour hang instead of returning immediately) would otherwise
// hang this test until the package's default 10-minute -timeout kills the
// whole binary with a stack dump, rather than failing this test alone with a
// clear message. The deadline is never reached on the shipped code — cancel
// runs manually, well within it, the moment headers arrive.
func TestHandleDelayAfterHeadersClientCancelledDuringHangLandsAbortedEntry(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{DelayAfterHeaders: scenario.Duration(time.Hour)},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(`{"ok":true}`)))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/search", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := srv.Client().Do(req) //nolint:bodyclose // cancelled below before any body arrives to close
	require.NoError(t, err, "headers must arrive before the client cancels")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()

	require.Eventually(t, func() bool { return len(j.Snapshot()) == 1 }, time.Second, time.Millisecond,
		"the client's own cancellation is not a reason to lose the entry")

	e := j.Snapshot()[0]
	require.True(t, e.Outcome.Aborted)
	require.Zero(t, e.Outcome.BytesWritten)
}

// TestHandleDelayAfterHeadersTruncateBodyClientCancelledDuringHangLandsDeferredRecord
// is the spec's explicitly named test for the client-cancellation case on
// truncate_body carrying delay_after_headers: the ONE combination where
// recordAbort has not run by the time the client's own deadline ends the
// request (deferAbortRecord, Handle), so the deferred record at the top of
// Handle is the one that has to land it. The one-hour hang also makes the
// pre-cancel snapshot deterministic: the entry cannot possibly exist yet the
// instant after Do returns, no matter how the test goroutine is scheduled,
// which is a stronger, non-racy way to prove "absent while the hang runs"
// than a short real delay's timing window.
func TestHandleDelayAfterHeadersTruncateBodyClientCancelledDuringHangLandsDeferredRecord(t *testing.T) {
	t.Parallel()

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultTruncateBody, DelayAfterHeaders: scenario.Duration(time.Hour), Reset: true},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(`{"ok":true}`)))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/search", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := srv.Client().Do(req) //nolint:bodyclose // cancelled below before any body arrives to close
	require.NoError(t, err, "headers must arrive before the client cancels")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Empty(t, j.Snapshot(),
		"the one-hour hang cannot have finished yet, so recordAbort cannot have run: the entry must be absent")

	cancel()

	require.Eventually(t, func() bool { return len(j.Snapshot()) == 1 }, time.Second, time.Millisecond,
		"the client's own cancellation during the after-headers hang is not a reason to lose the entry — the "+
			"deferred record at the top of Handle must land it, since truncateBody's own recordAbort never ran")

	e := j.Snapshot()[0]
	require.Equal(t, journal.OutcomeFault, e.Outcome.Kind)
	require.Equal(t, string(scenario.FaultTruncateBody), e.Outcome.FaultKind)
	require.True(t, e.Outcome.Aborted)
	require.Zero(t, e.Outcome.BytesWritten)
	require.Equal(t, time.Hour.Milliseconds(), e.Outcome.DelayAfterHeadersMS)
}

// TestHandleDelayAfterHeadersTruncateBodyRecordsAfterTheHangBeforeTheAbort is
// DoD (b) and the unit's central design-decision test: headers 200, the body
// read fails with a reset, the journal entry is present once that read has
// failed, completed_at - arrived_at observes the whole hang, and Aborted/
// FaultKind are set. The companion property the spec calls out by name — the
// entry must NOT exist while the hang is still running, only once it has
// completed — is proved deterministically (no timing window) by
// TestHandleDelayAfterHeadersTruncateBodyClientCancelledDuringHangLandsDeferredRecord's
// one-hour hang instead of here: a require.Empty immediately after Do
// returns, racing a real 150ms hang on this goroutine's own scheduling,
// would be a flake surface for no discriminating power a longer, unbounded
// hang does not already give more reliably.
func TestHandleDelayAfterHeadersTruncateBodyRecordsAfterTheHangBeforeTheAbort(t *testing.T) {
	t.Parallel()

	const delay = 150 * time.Millisecond
	const body = `{"results":[{"title":"a full and complete response body"}]}`

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultTruncateBody, DelayAfterHeaders: scenario.Duration(delay), Reset: true},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(body)))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err, "headers must arrive before the hang; only the body is affected")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	headersAt := time.Now()

	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Error(t, err, "the body must not complete: reset after the hang")
	require.GreaterOrEqual(t, time.Since(headersAt), delay-headersTimestampSkew,
		"the client must observe the hang, measured one-directionally from headers, before the reset")

	// Read the journal at exactly the moment the client saw the read fail:
	// the entry has to be there already, and it has to say the hang actually
	// happened — recordAbort ran on the server goroutine strictly before the
	// destructive write, which itself precedes the client observing anything.
	// This check is deterministic under the shipped ordering; it would only
	// catch a regression that moved recordAbort to AFTER the destructive
	// write racily, the same way the ring's other "record before the abort"
	// checks are inherently probabilistic against that one class of mutation.
	entries := j.Snapshot()
	require.Len(t, entries, 1, "the aborting entry must be present once the body read has failed")
	require.True(t, entries[0].Outcome.Aborted)
	require.Equal(t, string(scenario.FaultTruncateBody), entries[0].Outcome.FaultKind)
	observed := entries[0].CompletedAt.Sub(entries[0].ArrivedAt)
	require.GreaterOrEqual(t, observed, delay, "completed_at must observe the after-headers hang")
	require.Equal(t, delay.Milliseconds(), entries[0].Outcome.DelayAfterHeadersMS)
}

// TestHandleOversizedBodyDelayAfterHeaders is DoD (g): Content-Length is
// exact and declared before the hang exactly as the un-delayed oversized_body
// path already declares it, headers arrive first, then the hang, then the
// padded body.
func TestHandleOversizedBodyDelayAfterHeaders(t *testing.T) {
	t.Parallel()

	const body = `{"results":[{"title":"a small body"}]}`
	const delay = 150 * time.Millisecond
	wantLen := len(body) + 3*64*1024 + 17

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Kind: scenario.FaultOversizedBody, BodyBytes: wantLen, DelayAfterHeaders: scenario.Duration(delay)},
	}}
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, okHandler(body)))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	headersAt := time.Now()
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, strconv.Itoa(wantLen), resp.Header.Get("Content-Length"),
		"Content-Length must be exact and declared before the flush, exactly as the un-delayed path declares it")

	read, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, wantLen, len(read))
	require.GreaterOrEqual(t, time.Since(headersAt), delay-headersTimestampSkew,
		"the padded body must arrive at least the hang after the headers did")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.Equal(t, delay.Milliseconds(), entries[0].Outcome.DelayAfterHeadersMS)
	require.Equal(t, wantLen, entries[0].Outcome.BytesWritten)
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

// TestHandleOversizedBodyFault proves the wire shape of the padding kind: an
// exact Content-Length declared up front (so net/http never falls back to
// chunked transfer encoding across the several Write calls padding takes),
// the requested minimum size reached, and the decoded value unchanged — only
// trailing whitespace was appended.
func TestHandleOversizedBodyFault(t *testing.T) {
	t.Parallel()

	const body = `{"results":[{"title":"a small body"}]}`

	tests := []struct {
		name       string
		attempt    scenario.FaultAttempt
		wantStatus int
		wantLen    int
	}{
		{
			// Past net/http's ~2 KiB bufferBeforeChunkingSize and across three
			// 64 KiB padding-chunk boundaries: a small pad leaves net/http's own
			// automatic Content-Length inference indistinguishable from this
			// package's explicit one, so a body this size is what actually
			// proves writeOversizedBody sets Content-Length itself rather than
			// falling back to chunked transfer encoding.
			name:       "pads to the requested minimum",
			attempt:    scenario.FaultAttempt{Kind: scenario.FaultOversizedBody, BodyBytes: len(body) + 3*64*1024 + 17},
			wantStatus: http.StatusOK,
			wantLen:    len(body) + 3*64*1024 + 17,
		},
		{
			name:       "a body already at or above the minimum appends nothing",
			attempt:    scenario.FaultAttempt{Kind: scenario.FaultOversizedBody, BodyBytes: len(body) - 5},
			wantStatus: http.StatusOK,
			wantLen:    len(body),
		},
		{
			name:       "status override composes: an oversized 500",
			attempt:    scenario.FaultAttempt{Status: http.StatusInternalServerError, BodyBytes: len(body) + 200},
			wantStatus: http.StatusInternalServerError,
			wantLen:    len(body) + 200,
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
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, tc.wantStatus, resp.StatusCode)
			require.Equal(t, strconv.Itoa(tc.wantLen), resp.Header.Get("Content-Length"),
				"Content-Length must be set exactly, or net/http falls back to chunked encoding across the padding writes")

			read, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.wantLen, len(read))

			trimmed := bytes.TrimRight(read, " ")
			require.Equal(t, body, string(trimmed),
				"padding must be trailing whitespace only; nothing about the rendered body may change")

			var got, want any
			require.NoError(t, json.Unmarshal(trimmed, &got))
			require.NoError(t, json.Unmarshal([]byte(body), &want))
			require.Equal(t, want, got, "the decoded value must be byte-for-byte the unpadded response's value")

			entries := j.Snapshot()
			require.Len(t, entries, 1)
			require.False(t, entries[0].Outcome.Aborted, "oversized_body completes normally; nothing aborts")
			require.Equal(t, string(scenario.FaultOversizedBody), entries[0].Outcome.FaultKind)
			require.Equal(t, tc.wantLen, entries[0].Outcome.BytesWritten,
				"BytesWritten is the actual count written: JSON plus padding")
		})
	}
}

// TestHandleOversizedBodyUsesTheProviderShapedBody proves DoD (c) with a real
// FaultBody callback, the way TestHandleStatusFaultUsesTheProviderShapedBody
// does for kind status: a status override still renders through the
// provider's own error-shape builder, and padding is applied to THAT body,
// not the handler's ordinary 200 body.
func TestHandleOversizedBodyUsesTheProviderShapedBody(t *testing.T) {
	t.Parallel()

	const errBody = `{"requestId":"abc","error":"500"}`
	h := func(_ *Exchange) Response {
		return Response{
			Status:        http.StatusOK,
			Body:          []byte(`{"ok":true}`),
			FaultEligible: true,
			FaultBody: func(a scenario.FaultAttempt) []byte {
				return []byte(`{"requestId":"abc","error":"` + strconv.Itoa(a.Status) + `"}`)
			},
		}
	}
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{
		{Status: http.StatusInternalServerError, BodyBytes: len(errBody) + 64},
	}}
	j := journal.NewRing(8, 4096)
	srv := httptest.NewServer(Handle(Deps{Journal: j, Faults: engine}, Exa, testRoute, h))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/search", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Equal(t, strconv.Itoa(len(errBody)+64), resp.Header.Get("Content-Length"))

	read, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, len(errBody)+64, len(read))
	require.Equal(t, errBody, string(bytes.TrimRight(read, " ")),
		"the padded body must be the provider-shaped error, not the handler's ordinary 200 body")

	entries := j.Snapshot()
	require.Equal(t, string(scenario.FaultOversizedBody), entries[0].Outcome.FaultKind)
	require.Equal(t, len(errBody)+64, entries[0].Outcome.BytesWritten)
}

// TestHandleOversizedBodyMergesExtraFieldsBeforePadding proves DoD (d):
// extra_fields is not a transport change for oversized_body either — it
// merges into the body exactly as it does for every other kind, and padding
// is computed AFTER that merge, so the request that decodes the padded body
// sees the merged field.
func TestHandleOversizedBodyMergesExtraFieldsBeforePadding(t *testing.T) {
	t.Parallel()

	const bodyBytes = 4096
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{
		Kind:        scenario.FaultOversizedBody,
		BodyBytes:   bodyBytes,
		ExtraFields: scenario.ExtraFields{"newField": "surprise"},
	}}}
	j := journal.NewRing(8, 4096)
	w := serve(Deps{Journal: j, Faults: engine}, okHandler(`{"ok":true}`), postJSON(`{}`))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, bodyBytes, w.Body.Len())

	var got map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimRight(w.Body.Bytes(), " "), &got))
	require.Equal(t, "surprise", got["newField"])
	require.Equal(t, true, got["ok"])
}

// discardResponseWriter is an http.ResponseWriter whose Write discards bytes
// without retaining them, so TestOversizedBodyPaddingIsBounded can measure
// this package's own allocations for a huge padded write without also
// measuring httptest.ResponseRecorder's internal buffer growth, which would
// otherwise swamp the signal this test is looking for.
type discardResponseWriter struct {
	header http.Header
	code   int
}

func (w *discardResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *discardResponseWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *discardResponseWriter) WriteHeader(code int) { w.code = code }

// TestOversizedBodyPaddingIsBounded proves DoD (e): a scenario asking for a
// large body_bytes must cost the process a fixed-size buffer, never an
// allocation proportional to the request. 64 MiB is requested; the padding
// mechanism is a 64 KiB buffer reused across bounded chunks
// (oversizedBodyPaddingChunk in fault_exec.go), so the bytes actually
// allocated on the heap during the call must stay orders of magnitude below
// the 64 MiB requested — a regression that padded via make([]byte, bodyBytes)
// would allocate the full 64 MiB and fail this bound loudly.
//
// Deliberately not t.Parallel(): the runtime.MemStats delta is process-wide,
// so a concurrent test's allocations would land inside this one's window.
func TestOversizedBodyPaddingIsBounded(t *testing.T) {
	const bodyBytes = 64 << 20 // 64 MiB

	body := []byte(`{"ok":true}`)
	header := http.Header{"Content-Type": []string{"application/json"}}
	w := &discardResponseWriter{}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	written, err := writeOversizedBody(context.Background(), w, http.StatusOK, header, body, bodyBytes, nil, DelayReal)
	require.NoError(t, err)

	runtime.ReadMemStats(&after)

	require.Equal(t, bodyBytes, written)
	allocated := after.TotalAlloc - before.TotalAlloc
	require.Lessf(t, allocated, uint64(2<<20),
		"writeOversizedBody allocated %d bytes serving a %d-byte body; the padding buffer must stay fixed-size, "+
			"not proportional to body_bytes", allocated, bodyBytes)
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
