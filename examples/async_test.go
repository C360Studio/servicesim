package examples_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/provider/exa"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// asyncJobScenario is design §2.1's shape: two pending polls, then a terminal
// snapshot that also answers every poll after it — the smallest loadable
// fixture for Exa's async agent-run surface. There is no built-in async
// scenario yet (A7 adds one), so this is inline.
const asyncJobScenario = `
version: 1
name: examples-async-job
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - when: {call_index: 1}
        respond: {status: running}
      - respond:
          status: completed
          output:
            text: Report A states the finding.
`

// createAsyncJob issues the create call an adapter would issue and returns the
// identifier it minted.
func createAsyncJob(t *testing.T, client *http.Client, base string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		base+"/agent/runs", strings.NewReader(`{"query":"find it"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "create failed")

	var out struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotEmpty(t, out.ID)
	return out.ID
}

// pollAsyncJob issues one poll and returns the decoded body.
func pollAsyncJob(t *testing.T, client *http.Client, base, id string) map[string]any {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/agent/runs/"+id, nil)
	require.NoError(t, err)
	req.Header.Set("x-api-key", "test-key")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "poll failed")

	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// TestAsyncJobCreateThenPollThroughTestkit is the create-then-poll lifecycle
// read back the way a consumer's own test would: sim.Jobs() names the record a
// create minted, and testkit.AssertPollSequence pins that every poll after it
// drew a consecutive attempt from that job's own lane — the create-then-poll
// correlation, checked from the journal alone.
func TestAsyncJobCreateThenPollThroughTestkit(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(asyncJobScenario), testkit.WithProviders(provider.Exa))
	base := sim.URL(provider.Exa)

	id := createAsyncJob(t, sim.Client(), base)

	for range 2 {
		got := pollAsyncJob(t, sim.Client(), base, id)
		assert.Equal(t, "running", got["status"])
	}
	done := pollAsyncJob(t, sim.Client(), base, id)
	assert.Equal(t, "completed", done["status"])

	jobs := sim.Jobs()
	require.Len(t, jobs, 1, "the poll loop above created exactly one job")
	assert.Equal(t, id, jobs[0].ID)

	testkit.AssertPollSequence(t, sim.Requests(provider.Exa), id, http.StatusOK, http.StatusOK, http.StatusOK)
}

// TestAsyncJobViaHandBuiltDeps is design §4.5's compile-time obligation made
// runtime: a consumer wiring provider.Deps by hand — exactly as it would
// inside its own httptest server, without [testkit.Start] — needs
// [testkit.NewJobs] or a poll can never resolve the identifier its create
// returned; Deps.Jobs left nil means a create still answers but every poll
// after it 404s. The job is read back through Jobs.Lookup, naming only the
// aliased [testkit.Job].
func TestAsyncJobViaHandBuiltDeps(t *testing.T) {
	t.Parallel()

	s, _, err := scenario.Parse([]byte(asyncJobScenario))
	require.NoError(t, err)

	store := testkit.NewJobs()
	srv := httptest.NewServer(exa.New(provider.Deps{
		Scenario: s,
		Faults:   testkit.NewFaults(s),
		Jobs:     store,
	}))
	t.Cleanup(srv.Close)

	id := createAsyncJob(t, srv.Client(), srv.URL)

	job, found := store.Lookup(provider.DefaultNamespace, id)
	require.True(t, found, "the create must leave a record a poll can resolve")
	assert.Equal(t, id, job.ID)
}
