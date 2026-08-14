package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/admin"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// secret is the literal that must not survive onto the wire by any path. It
// carries a vendor prefix and a digit so redact.String recognises it even where
// no credential-shaped name accompanies it, which is what makes the free-text
// shapes (a finding message, a parse error) real assertions rather than
// assertions about the name matcher.
const secret = "sk-test-9f3c1d7b2a4e"

// baseTime is a fixed instant. Journal timestamps are real time in production;
// the tests pin them so an assertion about ordering cannot become an assertion
// about how fast the machine ran.
var baseTime = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// fakeFaults records that reset reached the fault engine. The admin surface must
// zero the counters the handlers actually consult, so "reset cleared the
// journal" is only half the assertion. The counter is atomic because the
// concurrency test resets from several goroutines at once, which is what a
// developer curling the admin surface while a suite runs does to the real
// engine.
type fakeFaults struct{ resets atomic.Int64 }

func (f *fakeFaults) Next(key string) provider.FaultDecision {
	return provider.FaultDecision{Index: -1, Key: key}
}

func (f *fakeFaults) Reset() { f.resets.Add(1) }

// rawJournal is a journal.Journal that retains entries verbatim, without the
// redaction Ring.Append performs. It exists to prove the admin surface redacts
// on the way out too: a Deps wired with a third-party journal implementation
// must not be the one path by which a credential leaves the process.
type rawJournal struct {
	seq     atomic.Uint64
	entries []journal.Entry
}

func (j *rawJournal) Next() uint64              { return j.seq.Add(1) }
func (j *rawJournal) Append(e journal.Entry)    { j.entries = append(j.entries, e) }
func (j *rawJournal) Snapshot() []journal.Entry { return j.entries }
func (j *rawJournal) Reset()                    { j.entries = nil }
func (j *rawJournal) Stats() journal.Stats      { return journal.Stats{Stored: len(j.entries)} }

// entryOpt mutates an entry a test is building.
type entryOpt func(*journal.Entry)

// newEntry builds a plausible journaled request. Only the fields a test asserts
// on are worth setting, so everything else is a fixed, boring value.
func newEntry(seq uint64, providerName string, opts ...entryOpt) journal.Entry {
	e := journal.Entry{
		Provider:    providerName,
		Seq:         seq,
		Method:      http.MethodPost,
		Path:        "/search",
		Route:       "POST /search",
		ArrivedAt:   baseTime,
		CompletedAt: baseTime.Add(10 * time.Millisecond),
		Headers:     map[string][]string{"Content-Type": {"application/json"}},
		Body:        json.RawMessage(`{"query":"go"}`),
		Auth:        journal.AuthObservation{Present: true, Header: "authorization", Scheme: "Bearer", Fingerprint: "0a1b2c3d"},
		Outcome:     journal.Outcome{Kind: journal.OutcomeScenario, Status: http.StatusOK, AttemptIndex: 0},
	}
	for _, opt := range opts {
		opt(&e)
	}
	return e
}

// serve runs one request against a handler and returns the recorder.
func serve(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// decodeRequests decodes a /__admin/requests body, failing the test rather than
// returning an error: a body that does not decode is never the assertion a
// caller wanted to make.
func decodeRequests(t *testing.T, rec *httptest.ResponseRecorder) admin.RequestsResponse {
	t.Helper()
	var got admin.RequestsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), "body: %s", rec.Body.String())
	return got
}

func TestHandler_Healthz(t *testing.T) {
	h := admin.Handler(admin.Deps{Version: "1.2.3"})

	rec := serve(t, h, http.MethodGet, "/healthz")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "ok", got["status"])
	assert.Equal(t, "1.2.3", got["version"])
}

// TestHandler_HealthzAnswersBeforeReadiness pins the property the container's
// HEALTHCHECK depends on: liveness must not consult readiness, because the admin
// listener binds before the provider listeners do.
func TestHandler_HealthzAnswersBeforeReadiness(t *testing.T) {
	h := admin.Handler(admin.Deps{Ready: new(atomic.Bool)})

	assert.Equal(t, http.StatusOK, serve(t, h, http.MethodGet, "/healthz").Code)
	assert.Equal(t, http.StatusServiceUnavailable, serve(t, h, http.MethodGet, "/readyz").Code)
}

func TestHandler_Readyz(t *testing.T) {
	ready := new(atomic.Bool)
	ready.Store(true)

	notReady := new(atomic.Bool)

	tests := []struct {
		name       string
		ready      *atomic.Bool
		wantStatus int
		wantBody   string
	}{
		// A nil Ready is the state internal/server leaves Deps in only if it forgot
		// to wire readiness at all, so it must fail closed rather than report ready.
		{name: "unwired reads as not ready", ready: nil, wantStatus: http.StatusServiceUnavailable, wantBody: "not_ready"},
		{name: "scenario not yet loaded", ready: notReady, wantStatus: http.StatusServiceUnavailable, wantBody: "not_ready"},
		{name: "scenario loaded and listeners up", ready: ready, wantStatus: http.StatusOK, wantBody: "ready"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, admin.Handler(admin.Deps{Ready: tc.ready}), http.MethodGet, "/readyz")

			require.Equal(t, tc.wantStatus, rec.Code)

			var got map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, tc.wantBody, got["status"])
		})
	}
}

// TestHandler_RequestsServesBothTimestamps is acceptance criterion 9's
// prerequisite: a consumer proves its provider calls overlapped rather than ran
// serially by reading arrival and completion instants out of this endpoint, so
// both must survive the round trip intact.
func TestHandler_RequestsServesBothTimestamps(t *testing.T) {
	ring := journal.NewRing(8, 4096)

	// exa arrives first and completes last; tavily runs entirely inside that
	// window. That is the shape "these calls overlapped" is read from.
	exa := newEntry(1, "exa", func(e *journal.Entry) {
		e.ArrivedAt = baseTime
		e.CompletedAt = baseTime.Add(300 * time.Millisecond)
	})
	tavily := newEntry(2, "tavily", func(e *journal.Entry) {
		e.ArrivedAt = baseTime.Add(50 * time.Millisecond)
		e.CompletedAt = baseTime.Add(120 * time.Millisecond)
	})
	ring.Append(exa)
	ring.Append(tavily)

	rec := serve(t, admin.Handler(admin.Deps{Journal: ring}), http.MethodGet, "/__admin/requests")

	require.Equal(t, http.StatusOK, rec.Code)
	got := decodeRequests(t, rec)
	require.Len(t, got.Entries, 2)

	assert.True(t, got.Entries[0].ArrivedAt.Equal(exa.ArrivedAt))
	assert.True(t, got.Entries[0].CompletedAt.Equal(exa.CompletedAt))
	assert.True(t, got.Entries[1].ArrivedAt.Equal(tavily.ArrivedAt))
	assert.True(t, got.Entries[1].CompletedAt.Equal(tavily.CompletedAt))

	assert.True(t, got.Entries[1].ArrivedAt.Before(got.Entries[0].CompletedAt),
		"the served journal must let a consumer prove the two calls overlapped")

	assert.Equal(t, 2, got.Stats.Stored)
	assert.Equal(t, 8, got.Stats.Capacity)
}

// TestHandler_RequestsNeverServesACredential is the assertion CLAUDE.md house
// rule 4 actually asks for: not "does redaction work" but "can a credential
// reach the wire by any path". Every shape runs twice — through a Ring, which
// redacts at the storage boundary, and through a journal that retains entries
// verbatim, which is what proves the admin surface redacts on the way out too.
func TestHandler_RequestsNeverServesACredential(t *testing.T) {
	shapes := []struct {
		name  string
		entry journal.Entry
	}{
		{
			name: "authorization header",
			entry: newEntry(1, "tavily", func(e *journal.Entry) {
				e.Headers = map[string][]string{"Authorization": {"Bearer " + secret}}
			}),
		},
		{
			name: "x-api-key header",
			entry: newEntry(1, "exa", func(e *journal.Entry) {
				e.Headers = map[string][]string{"X-Api-Key": {secret}}
			}),
		},
		{
			name: "query string",
			entry: newEntry(1, "exa", func(e *journal.Entry) {
				e.Query = "api_key=" + secret + "&q=go"
			}),
		},
		{
			name: "body property",
			entry: newEntry(1, "tavily", func(e *journal.Entry) {
				e.Body = json.RawMessage(`{"query":"go","api_key":"` + secret + `"}`)
			}),
		},
		{
			name: "nested body property",
			entry: newEntry(1, "perplexity", func(e *journal.Entry) {
				e.Body = json.RawMessage(`{"auth":{"token":"` + secret + `"}}`)
			}),
		},
		{
			name: "finding message quoting the request",
			entry: newEntry(1, "tavily", func(e *journal.Entry) {
				e.Findings = []journal.Finding{{
					Severity: journal.SeverityWarning,
					Code:     "tavily.api_key.in_body",
					Field:    "api_key",
					Message:  "credential presented as api_key=" + secret + " in the body",
				}}
			}),
		},
		{
			name: "absolute-form userinfo in a finding message",
			entry: newEntry(1, "exa", func(e *journal.Entry) {
				e.Findings = []journal.Finding{{
					Severity: journal.SeverityError,
					Code:     "request.malformed",
					Message:  "POST http://user:" + secret + "@exa.test/search",
				}}
			}),
		},
		{
			name: "body parse error quoting the body",
			entry: newEntry(1, "exa", func(e *journal.Entry) {
				e.Body = nil
				e.BodyParseError = `invalid character 'k' looking for beginning of value near "` + secret + `"`
			}),
		},
	}

	journals := map[string]func() journal.Journal{
		// The production wiring: redaction happened before anything was retained.
		"ring": func() journal.Journal { return journal.NewRing(8, 4096) },
		// A journal that retains verbatim. Only the serve-side pass can save this.
		"unredacting journal": func() journal.Journal { return &rawJournal{} },
	}

	for _, tc := range shapes {
		for journalName, newJournal := range journals {
			t.Run(tc.name+"/"+journalName, func(t *testing.T) {
				j := newJournal()
				j.Append(tc.entry)

				rec := serve(t, admin.Handler(admin.Deps{Journal: j}), http.MethodGet, "/__admin/requests")

				require.Equal(t, http.StatusOK, rec.Code)
				body := rec.Body.String()
				assert.NotContains(t, body, secret, "the credential reached the admin payload")
				assert.Contains(t, body, "[REDACTED]", "the field carrying the credential was not masked")
			})
		}
	}
}

// TestHandler_RequestsPreservesEvidenceAroundTheMask guards the other half of
// redaction: masking a credential must not destroy the evidence a consumer came
// for. The placement is the assertion an adapter contract test makes.
func TestHandler_RequestsPreservesEvidenceAroundTheMask(t *testing.T) {
	ring := journal.NewRing(8, 4096)
	ring.Append(newEntry(1, "exa", func(e *journal.Entry) {
		e.Query = "api_key=" + secret + "&q=go"
		e.Headers = map[string][]string{"X-Api-Key": {secret}}
	}))

	rec := serve(t, admin.Handler(admin.Deps{Journal: ring}), http.MethodGet, "/__admin/requests")

	got := decodeRequests(t, rec)
	require.Len(t, got.Entries, 1)
	assert.Contains(t, got.Entries[0].Headers, "X-Api-Key", "the placement is the evidence; only the value is secret")
	assert.Contains(t, got.Entries[0].Query, "api_key=", "the parameter name must survive so a misplaced key is visible")
	assert.Contains(t, got.Entries[0].Query, "q=go")
	assert.Equal(t, "0a1b2c3d", got.Entries[0].Auth.Fingerprint, "the fingerprint is how two calls are compared without a key")
}

func TestHandler_RequestsFilters(t *testing.T) {
	newJournal := func(t *testing.T) journal.Journal {
		t.Helper()
		ring := journal.NewRing(16, 4096)
		ring.Append(newEntry(1, "exa"))
		ring.Append(newEntry(2, "tavily"))
		ring.Append(newEntry(3, "exa", func(e *journal.Entry) { e.Namespace = "t-42" }))
		ring.Append(newEntry(4, "perplexity"))
		return ring
	}

	tests := []struct {
		name     string
		target   string
		wantSeqs []uint64
	}{
		{name: "unfiltered", target: "/__admin/requests", wantSeqs: []uint64{1, 2, 3, 4}},
		{name: "provider", target: "/__admin/requests?provider=exa", wantSeqs: []uint64{1, 3}},
		{name: "provider is case-insensitive", target: "/__admin/requests?provider=EXA", wantSeqs: []uint64{1, 3}},
		{name: "several providers", target: "/__admin/requests?provider=exa,perplexity", wantSeqs: []uint64{1, 3, 4}},
		{name: "repeated provider parameter", target: "/__admin/requests?provider=exa&provider=tavily", wantSeqs: []uint64{1, 2, 3}},
		{name: "unknown provider matches nothing", target: "/__admin/requests?provider=nope", wantSeqs: nil},
		{name: "since is exclusive", target: "/__admin/requests?since=2", wantSeqs: []uint64{3, 4}},
		{name: "limit keeps the oldest matches", target: "/__admin/requests?limit=2", wantSeqs: []uint64{1, 2}},
		// since plus limit is the forward pager: the caller passes the highest
		// sequence it has already read and gets the next page, not a window that
		// skips whatever arrived in between.
		{name: "since and limit page forward", target: "/__admin/requests?since=1&limit=2", wantSeqs: []uint64{2, 3}},
		{name: "limit zero is no limit", target: "/__admin/requests?limit=0", wantSeqs: []uint64{1, 2, 3, 4}},
		{name: "namespace", target: "/__admin/requests?namespace=t-42", wantSeqs: []uint64{3}},
		// An entry recorded before namespaces were wired carries no namespace and
		// belongs to the default lane, or asking for the default lane would return
		// nothing at all.
		{name: "default namespace covers unset", target: "/__admin/requests?namespace=default", wantSeqs: []uint64{1, 2, 4}},
		{name: "unknown parameters are ignored", target: "/__admin/requests?nonsense=1", wantSeqs: []uint64{1, 2, 3, 4}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := admin.Handler(admin.Deps{Journal: newJournal(t)})

			rec := serve(t, h, http.MethodGet, tc.target)

			require.Equal(t, http.StatusOK, rec.Code)
			got := decodeRequests(t, rec)

			seqs := make([]uint64, 0, len(got.Entries))
			for _, e := range got.Entries {
				seqs = append(seqs, e.Seq)
			}
			assert.Equal(t, tc.wantSeqs, nilIfEmpty(seqs))

			// Stats describe retention, not the filtered page, so a filtered read
			// still reports what the journal holds.
			assert.Equal(t, 4, got.Stats.Stored)
		})
	}
}

func nilIfEmpty(s []uint64) []uint64 {
	if len(s) == 0 {
		return nil
	}
	return s
}

func TestHandler_RequestsRejectsUnparseableFilters(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{name: "since is not a number", target: "/__admin/requests?since=yesterday"},
		{name: "since is negative", target: "/__admin/requests?since=-1"},
		{name: "limit is not a number", target: "/__admin/requests?limit=all"},
		{name: "limit is negative", target: "/__admin/requests?limit=-3"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, admin.Handler(admin.Deps{}), http.MethodGet, tc.target)

			require.Equal(t, http.StatusBadRequest, rec.Code)

			var got map[string]string
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.NotEmpty(t, got["error"])
			// The rejected value is never echoed: a query parameter is one of the
			// places a misconfigured adapter puts its credential.
			assert.NotContains(t, rec.Body.String(), strings.TrimPrefix(tc.target, "/__admin/requests?"))
		})
	}
}

// TestHandler_RequestsIsUnindentedByDefault pins the encoding
// scripts/image-smoke.sh greps for. SetIndent would insert a space after the
// colon and break a passing smoke test with no code change anywhere near it.
func TestHandler_RequestsIsUnindentedByDefault(t *testing.T) {
	ring := journal.NewRing(4, 4096)
	ring.Append(newEntry(1, "exa"))
	h := admin.Handler(admin.Deps{Journal: ring})

	plain := serve(t, h, http.MethodGet, "/__admin/requests").Body.String()
	assert.Contains(t, plain, `"provider":"exa"`, "the image smoke test greps for exactly this literal")
	assert.NotContains(t, plain, "\n  ")

	for _, target := range []string{"/__admin/requests?pretty=1", "/__admin/requests?pretty"} {
		indented := serve(t, h, http.MethodGet, target).Body.String()
		assert.Contains(t, indented, "\n  ", "%s should indent", target)
		assert.Contains(t, indented, `"provider": "exa"`)
	}

	off := serve(t, h, http.MethodGet, "/__admin/requests?pretty=0").Body.String()
	assert.Equal(t, plain, off, "pretty=0 is the default encoding")
}

// TestHandler_RequestsIsByteIdenticalAcrossReads guards determinism: the same
// journal read twice must produce the same bytes, or a golden over this endpoint
// is worthless. Header maps and finding slices are the two places Go's
// randomised map iteration could otherwise reach the output.
func TestHandler_RequestsIsByteIdenticalAcrossReads(t *testing.T) {
	ring := journal.NewRing(8, 4096)
	ring.Append(newEntry(1, "exa", func(e *journal.Entry) {
		e.Headers = map[string][]string{
			"Content-Type": {"application/json"},
			"X-Api-Key":    {secret},
			"User-Agent":   {"servicesim-test"},
			"Accept":       {"application/json"},
		}
		e.Findings = []journal.Finding{
			{Severity: journal.SeverityWarning, Code: "request.unknown_field", Field: "b"},
			{Severity: journal.SeverityWarning, Code: "request.unknown_field", Field: "a"},
		}
	}))
	h := admin.Handler(admin.Deps{Journal: ring})

	first := serve(t, h, http.MethodGet, "/__admin/requests").Body.String()
	for range 20 {
		assert.Equal(t, first, serve(t, h, http.MethodGet, "/__admin/requests").Body.String())
	}
}

func TestHandler_RequestsWithAnEmptyJournalServesAnEmptyArray(t *testing.T) {
	rec := serve(t, admin.Handler(admin.Deps{}), http.MethodGet, "/__admin/requests")

	require.Equal(t, http.StatusOK, rec.Code)
	// Not "entries":null — a consumer decoding into a typed list should not have
	// to distinguish "no requests" from "no field".
	assert.Contains(t, rec.Body.String(), `"entries":[]`)
}

func TestHandler_Reset(t *testing.T) {
	ring := journal.NewRing(8, 4096)
	ring.Append(newEntry(1, "exa"))
	faults := &fakeFaults{}
	h := admin.Handler(admin.Deps{Journal: ring, Faults: faults})

	rec := serve(t, h, http.MethodPost, "/__admin/reset")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, ring.Snapshot(), "reset must clear the journal")
	assert.Equal(t, int64(1), faults.resets.Load(), "reset must zero the counters the handlers consult")

	got := decodeRequests(t, serve(t, h, http.MethodGet, "/__admin/requests"))
	assert.Empty(t, got.Entries)
}

// TestHandler_ResetRefusesAScopedReset records the one thing a reset must never
// do: silently wipe state a caller did not ask about. Per-namespace lanes are
// not wired yet, so a ?namespace= reset is refused rather than quietly resetting
// every namespace.
func TestHandler_ResetRefusesAScopedReset(t *testing.T) {
	ring := journal.NewRing(8, 4096)
	ring.Append(newEntry(1, "exa"))
	faults := &fakeFaults{}
	h := admin.Handler(admin.Deps{Journal: ring, Faults: faults})

	rec := serve(t, h, http.MethodPost, "/__admin/reset?namespace=t-42")

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Len(t, ring.Snapshot(), 1, "a refused reset must not clear anything")
	assert.Zero(t, faults.resets.Load())
}

func TestHandler_Scenario(t *testing.T) {
	s := scenario.Empty()
	s.Name = "happy"
	report := scenario.Report{Findings: []scenario.Finding{{
		Severity: scenario.SeverityWarning,
		Code:     "scenario.unused_source",
		Path:     "sources[0]",
		Message:  "declared but never projected",
	}}}

	rec := serve(t, admin.Handler(admin.Deps{Scenario: s, Report: report}), http.MethodGet, "/__admin/scenario")

	require.Equal(t, http.StatusOK, rec.Code)
	var got admin.ScenarioResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "happy", got.Name)
	assert.Equal(t, scenario.SchemaVersion, got.Version)
	assert.Equal(t, "happy", got.Seed, "an unset seed falls back to the name")
	assert.Equal(t, 0, got.Sources)
	require.Len(t, got.Findings, 1)
	assert.Equal(t, "scenario.unused_source", got.Findings[0].Code)
}

// TestHandler_FailsClosed covers the admin listener's half of house rule 3. The
// provider path case is the one that matters in practice: it is what a consumer
// sees when it points a provider base URL at the admin port.
func TestHandler_FailsClosed(t *testing.T) {
	h := admin.Handler(admin.Deps{})

	tests := []struct {
		name   string
		method string
		target string
	}{
		{name: "unknown path", method: http.MethodGet, target: "/nope"},
		{name: "provider path is not registered here", method: http.MethodPost, target: "/search"},
		{name: "perplexity path is not registered here", method: http.MethodPost, target: "/chat/completions"},
		{name: "admin prefix without a route", method: http.MethodGet, target: "/__admin/"},
		{name: "unknown admin route", method: http.MethodGet, target: "/__admin/nope"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, h, tc.method, tc.target)

			require.Equal(t, http.StatusNotFound, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

			var got map[string]string
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, "not found", got["error"])
		})
	}
}

func TestHandler_WrongMethodIsMethodNotAllowed(t *testing.T) {
	h := admin.Handler(admin.Deps{})

	tests := []struct {
		name      string
		method    string
		target    string
		wantAllow string
	}{
		{name: "post to a read endpoint", method: http.MethodPost, target: "/healthz", wantAllow: "GET, HEAD"},
		{name: "post to readyz", method: http.MethodPost, target: "/readyz", wantAllow: "GET, HEAD"},
		{name: "delete the journal", method: http.MethodDelete, target: "/__admin/requests", wantAllow: "GET, HEAD"},
		{name: "get the reset endpoint", method: http.MethodGet, target: "/__admin/reset", wantAllow: "POST"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, h, tc.method, tc.target)

			require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
			assert.Equal(t, tc.wantAllow, rec.Header().Get("Allow"))
		})
	}
}

// TestHandler_ZeroDepsAnswersEveryRoute matters because the admin listener binds
// before anything else is wired, and because internal/server builds Deps field
// by field: a nil journal, fault engine or scenario must degrade, not panic.
func TestHandler_ZeroDepsAnswersEveryRoute(t *testing.T) {
	h := admin.Handler(admin.Deps{})

	tests := []struct {
		method string
		target string
		want   int
	}{
		{method: http.MethodGet, target: "/healthz", want: http.StatusOK},
		{method: http.MethodGet, target: "/readyz", want: http.StatusServiceUnavailable},
		{method: http.MethodGet, target: "/__admin/requests", want: http.StatusOK},
		{method: http.MethodGet, target: "/__admin/scenario", want: http.StatusOK},
		{method: http.MethodPost, target: "/__admin/reset", want: http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			assert.Equal(t, tc.want, serve(t, h, tc.method, tc.target).Code)
		})
	}
}

// TestHandler_IsSafeUnderConcurrentReadsAndResets is the -race assertion: the
// journal is read and cleared from many goroutines at once in any process where
// a developer curls the admin surface while tests are running.
func TestHandler_IsSafeUnderConcurrentReadsAndResets(t *testing.T) {
	ring := journal.NewRing(64, 4096)
	h := admin.Handler(admin.Deps{Journal: ring, Faults: &fakeFaults{}})

	// The requests are issued without the serve helper: t.Helper and the require
	// package are not safe to reach from a non-test goroutine, and a race here
	// would surface as a race report rather than an assertion anyway.
	call := func(method, target string) {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, target, nil))
	}

	done := make(chan struct{})
	for range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := range 50 {
				ring.Append(newEntry(uint64(i+1), "exa"))
				call(http.MethodGet, "/__admin/requests")
				if i%10 == 0 {
					call(http.MethodPost, "/__admin/reset")
				}
			}
		}()
	}
	for range 4 {
		<-done
	}

	// The surface is still serving, and Stats still describes a coherent ring
	// rather than a count some interleaving left behind.
	got := decodeRequests(t, serve(t, h, http.MethodGet, "/__admin/requests"))
	assert.Len(t, got.Entries, got.Stats.Stored)
}
