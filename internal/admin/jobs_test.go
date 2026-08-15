package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/admin"
	"github.com/c360studio/servicesim/internal/jobs"
)

// decodeJobs decodes a /__admin/jobs body.
func decodeJobs(t *testing.T, rec *httptest.ResponseRecorder) admin.JobsResponse {
	t.Helper()
	var got admin.JobsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), "body: %s", rec.Body.String())
	return got
}

// unlistableJobs is a jobs.Store that cannot enumerate its records — a
// consumer's own implementation, which is the whole interface it has to
// satisfy. GET /__admin/jobs must refuse rather than report an empty list
// that would look like "no jobs" instead of "cannot answer".
type unlistableJobs struct{}

func (unlistableJobs) Create(jobs.Job) (jobs.Stats, error)    { return jobs.Stats{}, nil }
func (unlistableJobs) Lookup(string, string) (jobs.Job, bool) { return jobs.Job{}, false }
func (unlistableJobs) StatsIn(string) jobs.Stats              { return jobs.Stats{Bound: jobs.DefaultMaxJobs} }
func (unlistableJobs) ResetIn(string)                         {}
func (unlistableJobs) Reset()                                 {}

// TestHandler_Jobs pins the declared total order: namespace ascending, then
// entry, then create index, then id. Records are seeded out of that order,
// across two namespaces and two entries, so a listing that merely reflected
// map iteration would fail this most of the time.
func TestHandler_Jobs(t *testing.T) {
	store := jobs.NewRegistry(jobs.Limits{})
	insert := func(ns, entry, id string, index int) {
		_, err := store.Create(jobs.Job{
			ID: id, Namespace: ns, Entry: entry, CreateIndex: index, CreatedAt: baseTime,
		})
		require.NoError(t, err)
	}
	insert("t-2", "tavily_research", "run_a", 0)
	insert("t-1", "exa_agent_runs", "run_z", 1)
	insert("t-1", "exa_agent_runs", "run_a", 0)
	insert("t-1", "tavily_research", "run_b", 0)

	h := admin.Handler(admin.Deps{Jobs: store})
	rec := serve(t, h, http.MethodGet, "/__admin/jobs")

	require.Equal(t, http.StatusOK, rec.Code)
	got := decodeJobs(t, rec)
	require.Len(t, got.Jobs, 4)
	assert.Equal(t, jobs.DefaultMaxJobs, got.Bound)

	want := []struct{ namespace, entry, id string }{
		{"t-1", "exa_agent_runs", "run_a"},
		{"t-1", "exa_agent_runs", "run_z"},
		{"t-1", "tavily_research", "run_b"},
		{"t-2", "tavily_research", "run_a"},
	}
	for i, w := range want {
		assert.Equal(t, w.namespace, got.Jobs[i].Namespace, "entry %d", i)
		assert.Equal(t, w.entry, got.Jobs[i].Entry, "entry %d", i)
		assert.Equal(t, w.id, got.Jobs[i].ID, "entry %d", i)
	}

	// Determinism across repeated reads: a golden over this body must not
	// flake, the same property TestHandler_RequestsIsByteIdenticalAcrossReads
	// pins for the journal.
	first := rec.Body.String()
	for range 10 {
		assert.Equal(t, first, serve(t, h, http.MethodGet, "/__admin/jobs").Body.String())
	}
}

// TestHandler_JobsNamespaceFilter covers the ?namespace= scope: only the
// named lane's records are returned, and the bound is unaffected by the
// filter — it describes the process's configuration, not the filtered page.
func TestHandler_JobsNamespaceFilter(t *testing.T) {
	store := jobs.NewRegistry(jobs.Limits{})
	_, err := store.Create(jobs.Job{ID: "run_a", Namespace: "t-1", Entry: "exa_agent_runs"})
	require.NoError(t, err)
	_, err = store.Create(jobs.Job{ID: "run_b", Namespace: "t-2", Entry: "exa_agent_runs"})
	require.NoError(t, err)

	h := admin.Handler(admin.Deps{Jobs: store})
	rec := serve(t, h, http.MethodGet, "/__admin/jobs?namespace=t-1")

	require.Equal(t, http.StatusOK, rec.Code)
	got := decodeJobs(t, rec)
	require.Len(t, got.Jobs, 1)
	assert.Equal(t, "run_a", got.Jobs[0].ID)
	assert.Equal(t, jobs.DefaultMaxJobs, got.Bound, "the bound describes the process's configuration, not the filtered page")
}

// TestHandler_JobsWithAnEmptyRegistryServesAnEmptyArrayAndTheBound covers the
// fresh-process case: no jobs yet, and the bound a create will be measured
// against.
func TestHandler_JobsWithAnEmptyRegistryServesAnEmptyArrayAndTheBound(t *testing.T) {
	rec := serve(t, admin.Handler(admin.Deps{}), http.MethodGet, "/__admin/jobs")

	require.Equal(t, http.StatusOK, rec.Code)
	// Not "jobs":null — a consumer decoding into a typed list should not have
	// to distinguish "no jobs" from "no field".
	body := rec.Body.String()
	assert.Contains(t, body, `"jobs":[]`)
	assert.Contains(t, body, `"bound":256`)
}

func TestHandler_JobsRejectsAnInvalidNamespace(t *testing.T) {
	rec := serve(t, admin.Handler(admin.Deps{}), http.MethodGet, "/__admin/jobs?namespace=t.1")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	// The rejected value is never echoed: a query parameter is one of the
	// places a misconfigured adapter puts its credential.
	assert.NotContains(t, rec.Body.String(), "t.1")
}

// TestHandler_JobsWithoutTheListCapabilityIsNotImplemented covers a Deps
// wired with a consumer's own Store implementation that never grew List.
func TestHandler_JobsWithoutTheListCapabilityIsNotImplemented(t *testing.T) {
	rec := serve(t, admin.Handler(admin.Deps{Jobs: unlistableJobs{}}), http.MethodGet, "/__admin/jobs")

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	var got map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.NotEmpty(t, got["error"])
}

// TestHandler_JobsNeverServesALaneKey is CLAUDE.md house rule 4's assertion
// applied to the newest retained structure. A lane key can embed a turn_key or
// Route.LaneFrom extractor's raw value verbatim (provider/lane.go
// turnLaneKey) — scenario validation does not refuse a "header:authorization"
// extractor, and nothing redacts a lane key, which is exactly why the journal
// has never carried one either. The listing must not be the first place one
// leaks.
func TestHandler_JobsNeverServesALaneKey(t *testing.T) {
	store := jobs.NewRegistry(jobs.Limits{})
	_, err := store.Create(jobs.Job{
		ID:        "run_a",
		Namespace: "t-1",
		Entry:     "exa_agent_runs",
		LaneKey:   "exa:agent_runs.create|header:authorization=Bearer sk-secret",
	})
	require.NoError(t, err)

	rec := serve(t, admin.Handler(admin.Deps{Jobs: store}), http.MethodGet, "/__admin/jobs")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "sk-secret", "a lane key must never reach the admin listing")
}
