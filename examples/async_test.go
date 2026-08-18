package examples_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/profiles/exa"
	"github.com/c360studio/servicesim/provider"
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

	sim := testkit.Start(t, testkit.WithProfiles(exa.Profile()), testkit.WithScenarioYAML(asyncJobScenario), testkit.WithProviders(exa.Name))
	base := sim.URL(exa.Name)

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

	testkit.AssertPollSequence(t, sim.Requests(exa.Name), id, http.StatusOK, http.StatusOK, http.StatusOK)
}

// asyncJobScenarioAliased is design §2.1's shape verbatim: the anchor on the
// first pending poll's respond, aliased by the second poll, then an
// unconditional terminal turn. It exists alongside asyncJobScenario (the
// longhand form used by the tests above) specifically to reproduce the loader
// defect docs/design/async-jobs.md §2.1 documents and to prove it is fixed:
// scenario/model.go's Providers.UnmarshalYAML retained each turn's `respond:`
// as a raw, undecoded *yaml.Node, and DecodeStrict re-marshalled turns one at
// a time — so an anchor declared on turn 0 was invisible when turn 1's alias
// was re-marshalled in isolation, and yaml.Marshal reported "unknown anchor
// 'pending' referenced". The name is shared with asyncJobScenarioLonghand
// (below) so their derived job identifiers are byte-identical too.
const asyncJobScenarioAliased = `
version: 1
name: examples-async-job-aliased
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: &pending
          status: running
      - when: {call_index: 1}
        respond: *pending
      - respond:
          status: completed
          output:
            text: Report A states the finding.
`

// asyncJobScenarioLonghand is asyncJobScenarioAliased with the anchor written
// out literally on both polls instead of aliased — the "fixture authors
// should write the repeated snapshot out literally" workaround the (now
// corrected) design doc once prescribed. Byte-for-byte equal HTTP responses
// between this and the aliased form is what proves the alias is sugar, not a
// different rendering path.
const asyncJobScenarioLonghand = `
version: 1
name: examples-async-job-aliased
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond:
          status: running
      - when: {call_index: 1}
        respond:
          status: running
      - respond:
          status: completed
          output:
            text: Report A states the finding.
`

// TestAsyncJobScenario_AliasedRespondAcrossTurns reproduces
// docs/design/async-jobs.md §2.1 end to end: scenario.Parse must load the
// anchor/alias shape with no findings, and a real create followed by three
// polls through testkit — the same path provider validation and the wire
// handlers actually run — must answer running, running, completed.
func TestAsyncJobScenario_AliasedRespondAcrossTurns(t *testing.T) {
	t.Parallel()

	s, report, err := scenario.Parse([]byte(asyncJobScenarioAliased))
	require.NoError(t, err, "scenario.Parse: %+v", report.Findings)
	assert.Empty(t, report.Findings, "the §2.1 shape must load with no findings")

	sim := testkit.Start(t, testkit.WithProfiles(exa.Profile()), testkit.WithScenario(s), testkit.WithProviders(exa.Name))
	base := sim.URL(exa.Name)

	id := createAsyncJob(t, sim.Client(), base)

	for range 2 {
		got := pollAsyncJob(t, sim.Client(), base, id)
		assert.Equal(t, "running", got["status"])
	}
	done := pollAsyncJob(t, sim.Client(), base, id)
	assert.Equal(t, "completed", done["status"])

	testkit.AssertPollSequence(t, sim.Requests(exa.Name), id, http.StatusOK, http.StatusOK, http.StatusOK)
}

// pollAsyncJobBytes issues one poll and returns the raw response body, for
// byte-for-byte comparison rather than a decoded-and-therefore-normalised one.
func pollAsyncJobBytes(t *testing.T, client *http.Client, base, id string) []byte {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/agent/runs/"+id, nil)
	require.NoError(t, err)
	req.Header.Set("x-api-key", "test-key")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "poll failed")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return body
}

// TestAsyncJobScenario_AliasedRendersByteIdenticalToLonghand is the
// determinism check house rule 2 requires applied to this specific sugar: the
// alias must be exactly that — sugar — so the aliased scenario's create and
// poll responses are byte-identical to the same scenario written out
// longhand, turn for turn. Both fixtures share one `name:` (and therefore one
// derivation seed) specifically so the create response's minted identifier is
// also identical, not just the poll bodies.
func TestAsyncJobScenario_AliasedRendersByteIdenticalToLonghand(t *testing.T) {
	t.Parallel()

	aliased := testkit.Start(t, testkit.WithProfiles(exa.Profile()), testkit.WithScenarioYAML(asyncJobScenarioAliased), testkit.WithProviders(exa.Name))
	longhand := testkit.Start(t, testkit.WithProfiles(exa.Profile()), testkit.WithScenarioYAML(asyncJobScenarioLonghand), testkit.WithProviders(exa.Name))

	aliasedBase, longhandBase := aliased.URL(exa.Name), longhand.URL(exa.Name)

	aliasedID := createAsyncJob(t, aliased.Client(), aliasedBase)
	longhandID := createAsyncJob(t, longhand.Client(), longhandBase)
	require.Equal(t, longhandID, aliasedID, "identical name: and identical call sequence must derive identical job identifiers")

	for i := range 3 {
		aliasedBody := pollAsyncJobBytes(t, aliased.Client(), aliasedBase, aliasedID)
		longhandBody := pollAsyncJobBytes(t, longhand.Client(), longhandBase, longhandID)
		if !bytes.Equal(aliasedBody, longhandBody) {
			t.Errorf("poll %d: aliased body %s\nlonghand body %s\nnot byte-identical", i, aliasedBody, longhandBody)
		}
	}
}

// TestAsyncJobViaHandBuiltDeps is design §4.5's compile-time obligation made
// runtime: a consumer wiring provider.Deps by hand — exactly as it would
// inside its own httptest server, without [testkit.Start] — needs
// [testkit.NewJobs] or a poll can never resolve the identifier its create
// returned; Deps.Jobs left nil means a create still answers but every poll
// after it 404s. The job is read back through Jobs.Lookup, naming only the
// aliased [testkit.Job].
//
// The handler and the fault engine both come from one provider.Set —
// provider.NewSet(exa.Profile()).Lookup(exa.Name).Handler(deps) and
// set.Faults(s) — in place of the deleted exa.New and testkit.NewFaults:
// (*provider.Set).Faults is the only exported fault-engine constructor.
func TestAsyncJobViaHandBuiltDeps(t *testing.T) {
	t.Parallel()

	s, _, err := scenario.Parse([]byte(asyncJobScenario))
	require.NoError(t, err)

	set := provider.MustSet(exa.Profile())
	p, ok := set.Lookup(exa.Name)
	require.True(t, ok)

	store := testkit.NewJobs()
	srv := httptest.NewServer(p.Handler(provider.Deps{
		Scenario: s,
		Faults:   set.Faults(s),
		Jobs:     store,
	}))
	t.Cleanup(srv.Close)

	id := createAsyncJob(t, srv.Client(), srv.URL)

	job, found := store.Lookup(provider.DefaultNamespace, id)
	require.True(t, found, "the create must leave a record a poll can resolve")
	assert.Equal(t, id, job.ID)
}
