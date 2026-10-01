package provider

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
	"github.com/c360studio/servicesim/scenario"
)

// TestAnInstancedAsyncProfilePollsItsOwnJobs pins the arrangement the entry check
// in ResolveJob must not break: a single-entry async profile registered twice —
// the primary "acme" and an instance "acme-fallback" of Kind "acme", which
// v0.5.0 supports — whose create handler passes MintJob the package's own
// constant, "acme", the way every in-tree profile does.
//
// The constant is not the name either listener is served from. Each is served
// from its own scenario block, "acme" and "acme-fallback", so the job has to
// carry the name of the entry its create was served from; a job that carried the
// constant would resolve on the primary listener and 404 on the instance that
// created it.
//
// And the reverse is deliberate: a job minted on one instance does not resolve
// on the other. They are separate scenario blocks with separate scripts, so a
// poll through the other instance would be served from a script the job was never
// minted for — the same cross-entry leak that makes Tavily unable to poll an Exa
// run.
func TestAnInstancedAsyncProfilePollsItsOwnJobs(t *testing.T) {
	t.Parallel()

	const entryConstant = "acme"

	create := func(x *Exchange) Response {
		id, ok := MintJob(x, entryConstant, "job_", Hex32)
		if !ok {
			return Response{Status: http.StatusServiceUnavailable, Body: []byte(`{"error":"refused"}`)}
		}
		return Response{Status: http.StatusCreated, Body: []byte(`{"id":"` + id + `"}`), FaultEligible: true}
	}
	poll := func(x *Exchange) Response {
		if !ResolveJob(x, x.Request.PathValue("id")) {
			return Response{Status: http.StatusNotFound, Body: []byte(`{"error":"not found"}`)}
		}
		return Response{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}
	}
	primary := Profile{
		Name: "acme", Title: "Acme", Summary: "a single-entry async profile",
		Routes: []Route{
			{Pattern: "POST /v1/jobs", FaultKey: "acme:jobs.create"},
			{Pattern: "GET /v1/jobs/{id}", FaultKey: "acme:jobs.poll"},
		},
		Handlers:    map[string]Handler{"POST /v1/jobs": create, "GET /v1/jobs/{id}": poll},
		ErrorBody:   fixedErrorBody(`{"error":"x"}`),
		DefaultAuth: scenario.AuthOptional,
	}
	instance := primary
	instance.Name, instance.Port, instance.Kind = "acme-fallback", 9098, "acme"

	set := MustSet(primary, instance)
	sc := mustScenario(t, `
version: 1
name: instanced-async
providers:
  acme:
    respond: {a: 1}
  acme-fallback:
    kind: acme
    respond: {a: 2}
`)
	ring := journal.NewRing(32, 1<<16)
	deps := Deps{Scenario: sc, Journal: ring, Faults: set.Faults(sc), Jobs: jobs.NewRegistry(jobs.Limits{})}

	handler := func(name Name) http.Handler {
		p, ok := set.Lookup(name)
		require.True(t, ok, "profile %q is registered", name)
		return p.Handler(deps)
	}
	acme, fallback := handler("acme"), handler("acme-fallback")

	do := func(h http.Handler, method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
		return rec
	}
	created := func(h http.Handler) string {
		rec := do(h, http.MethodPost, "/v1/jobs")
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var out struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		require.NotEmpty(t, out.ID)
		return out.ID
	}

	onPrimary, onInstance := created(acme), created(fallback)
	require.NotEqual(t, onPrimary, onInstance)

	assert.Equal(t, http.StatusOK, do(acme, http.MethodGet, "/v1/jobs/"+onPrimary).Code,
		"a job resolves on the listener whose create minted it")
	assert.Equal(t, http.StatusOK, do(fallback, http.MethodGet, "/v1/jobs/"+onInstance).Code,
		"a job minted on an instance resolves on that same instance, whatever constant its handler passed MintJob")

	for _, tc := range []struct {
		name, polled, minted string
		h                    http.Handler
		id                   string
	}{
		{"the instance's job polled through the primary", "acme", "acme-fallback", acme, onInstance},
		{"the primary's job polled through the instance", "acme-fallback", "acme", fallback, onPrimary},
	} {
		assert.Equal(t, http.StatusNotFound, do(tc.h, http.MethodGet, "/v1/jobs/"+tc.id).Code,
			"%s: instances script independently, so a job resolves only through the one that served its create", tc.name)

		entries := ring.Snapshot()
		var found *journal.Finding
		for _, f := range entries[len(entries)-1].Findings {
			if f.Code == CodeJobForeignID {
				found = &f
			}
		}
		require.NotNil(t, found, "%s: the miss must be diagnosed", tc.name)
		assert.Contains(t, found.Message, `"`+tc.minted+`"`)
		assert.Contains(t, found.Message, `"`+tc.polled+`"`)
	}
}
