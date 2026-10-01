package profiles_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/profiles/exa"
	"github.com/c360studio/servicesim/profiles/tavily"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// entryScopeScenario scripts both async entries, so each surface has a poll of
// its own to be (mis)served from.
const entryScopeScenario = `
version: 1
name: job-entry-scope
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - respond: {status: completed, output: {text: done}}
  tavily_research:
    turns:
      - when: {call_index: 0}
        respond: {status: pending}
      - respond: {status: completed, content: done}
`

// surface is one listener plus the credential its vendor expects.
type surface struct {
	handler http.Handler
	header  string
	value   string
}

// jobWorld is the Exa and Tavily listeners over ONE scenario, ONE job store and
// ONE journal — which is exactly what a running binary is. A job minted by one
// surface sits in the same namespace the other surface resolves against, so this
// is the only arrangement in which a poll can reach the wrong entry's job.
type jobWorld struct {
	t       *testing.T
	exa     surface
	tavily  surface
	journal *journal.Ring
}

func newJobWorld(t *testing.T) *jobWorld {
	t.Helper()

	sc, report, err := scenario.Parse([]byte(entryScopeScenario))
	require.NoErrorf(t, err, "scenario did not load: %v", report.Findings)

	set, err := provider.NewSet(exa.Profile(), tavily.Profile())
	require.NoError(t, err)

	ring := journal.NewRing(64, 1<<16)
	deps := provider.Deps{
		Scenario:  sc,
		Journal:   ring,
		Faults:    set.Faults(sc),
		DelayMode: provider.DelaySkip,
		Jobs:      jobs.NewRegistry(jobs.Limits{}),
	}
	return &jobWorld{
		t:       t,
		exa:     surface{exa.Profile().Handler(deps), "x-api-key", "test-key"},
		tavily:  surface{tavily.Profile().Handler(deps), "Authorization", "Bearer tvly-test-key"},
		journal: ring,
	}
}

// do sends one request to s, authenticated the way that listener's vendor is.
func (w *jobWorld) do(s surface, method, path, body string) *httptest.ResponseRecorder {
	w.t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(s.header, s.value)

	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

// last returns the journal entry of the most recent request.
func (w *jobWorld) last() journal.Entry {
	w.t.Helper()
	entries := w.journal.Snapshot()
	require.NotEmpty(w.t, entries)
	return entries[len(entries)-1]
}

// create posts a create to s and returns the identifier it minted, read from
// idField of the flat response object.
func (w *jobWorld) create(s surface, path, body, idField string) string {
	w.t.Helper()

	rec := w.do(s, http.MethodPost, path, body)
	require.Equal(w.t, http.StatusCreated, rec.Code, "create failed: %s", rec.Body.String())

	var out map[string]any
	require.NoError(w.t, json.Unmarshal(rec.Body.Bytes(), &out))
	id, _ := out[idField].(string)
	require.NotEmpty(w.t, id, "no %s in %s", idField, rec.Body.String())
	return id
}

// A job belongs to the entry that minted it. The job store keys a record by
// (namespace, id) and the two async surfaces mint into the same namespace, so a
// resolver that stops there lets a Tavily poll answer for an Exa run — serving
// the Tavily script's snapshot for a job the Tavily surface never created, and
// claiming attempts on a lane that has no business existing.
func TestAJobResolvesOnlyThroughTheEntryThatMintedIt(t *testing.T) {
	t.Parallel()

	w := newJobWorld(t)
	exaID := w.create(w.exa, "/agent/runs", `{"query":"q"}`, "id")
	tavilyID := w.create(w.tavily, "/research", `{"input":"q"}`, "request_id")
	require.NotEqual(t, exaID, tavilyID)

	// What a miss on the Tavily surface looks like when nothing at all matches.
	// The mismatch must be indistinguishable from it on the wire.
	baseline := w.do(w.tavily, http.MethodGet, "/research/3f2504e0-4f89-11d3-9a0c-0305e82c3301", "")
	require.Equal(t, http.StatusNotFound, baseline.Code)

	crossed := []struct {
		name   string
		s      surface
		method string
		path   string
	}{
		{"Tavily GET of an Exa run", w.tavily, http.MethodGet, "/research/" + exaID},
		{"Tavily HEAD of an Exa run", w.tavily, http.MethodHead, "/research/" + exaID},
		{"Exa GET of a Tavily task", w.exa, http.MethodGet, "/agent/runs/" + tavilyID},
		{"Exa HEAD of a Tavily task", w.exa, http.MethodHead, "/agent/runs/" + tavilyID},
	}
	for _, tc := range crossed {
		// Twice, so a cursor that advances on a claim would show as a second index
		// rather than hiding behind the first.
		for range 2 {
			rec := w.do(tc.s, tc.method, tc.path, "")
			assert.Equal(t, http.StatusNotFound, rec.Code, "%s must be the vendor's 404", tc.name)
			assert.Equal(t, -1, w.last().Outcome.AttemptIndex, "%s must claim no attempt", tc.name)
		}
	}

	// The wire body of the mismatch is the ordinary 404, byte for byte.
	mismatch := w.do(w.tavily, http.MethodGet, "/research/"+exaID, "")
	assert.Equal(t, baseline.Body.Bytes(), mismatch.Body.Bytes(),
		"a mismatch must not tell a client that the identifier exists on another surface")

	// Each job still resolves through its own entry, from poll 0: nothing above
	// advanced its cursor.
	run := w.do(w.exa, http.MethodGet, "/agent/runs/"+exaID, "")
	require.Equal(t, http.StatusOK, run.Code, run.Body.String())
	assert.Contains(t, run.Body.String(), `"status":"running"`)
	assert.Equal(t, http.StatusOK, w.do(w.exa, http.MethodHead, "/agent/runs/"+exaID, "").Code)

	task := w.do(w.tavily, http.MethodGet, "/research/"+tavilyID, "")
	require.Equal(t, http.StatusAccepted, task.Code, task.Body.String())
	assert.Contains(t, task.Body.String(), `"status":"pending"`)
	assert.Equal(t, http.StatusOK, w.do(w.tavily, http.MethodHead, "/research/"+tavilyID, "").Code)
}

// The miss is still diagnosable. job.foreign_id is the finding for "an
// identifier that looks like ours, in a namespace that has minted, that this
// route cannot resolve" — but its usual text says no such job exists, and none of
// its three usual causes is the real one here. The wrong-entry miss gets the same
// code, as a warning, with text that names the two entries.
func TestAWrongEntryPollRaisesTheForeignIDWarningNamingBothEntries(t *testing.T) {
	t.Parallel()

	w := newJobWorld(t)
	exaID := w.create(w.exa, "/agent/runs", `{"query":"q"}`, "id")

	rec := w.do(w.tavily, http.MethodGet, "/research/"+exaID, "")
	require.Equal(t, http.StatusNotFound, rec.Code)

	var found *journal.Finding
	for _, f := range w.last().Findings {
		if f.Code == provider.CodeJobForeignID {
			found = &f
			break
		}
	}
	require.NotNil(t, found, "findings: %+v", w.last().Findings)
	assert.Equal(t, journal.SeverityWarning, found.Severity, "a poll of the wrong surface must not read as an error")
	assert.Contains(t, found.Message, exa.NameAgentRuns, "the finding must name the entry that minted the job")
	assert.Contains(t, found.Message, tavily.NameResearch, "the finding must name the entry that polled it")
	assert.NotContains(t, found.Message, "another replica",
		"none of the usual three causes is the cause here, and naming them would send the reader the wrong way")
}
