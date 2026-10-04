package exa

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
)

// agentRunGoldenScenario backs the create, running-poll and completed-poll
// goldens. Its first turn (call_index 0) is a non-terminal snapshot, and its
// unconditional second turn is the terminal one — the same two-pending-then-
// terminal shape docs/design/async-jobs.md §2.1 specifies and
// TestAgentRunCreateThenPollToCompletion already exercises for behaviour. This
// scenario exists separately because a golden fixture's exact bytes must stay
// pinned to a scenario dedicated to producing them, not one shared with a
// behavioural test that is free to change shape.
const agentRunGoldenScenario = `
version: 1
name: exa-agent-runs-golden
time:
  base: 2026-01-01T00:00:00Z
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Report A finds that deterministic simulators remove flakiness from adapter test suites.
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - respond:
          status: completed
          output:
            text: Report A finds that deterministic simulators remove flakiness from adapter test suites.
            grounding:
              - field: text
                citations: [source-a]
                confidence: high
          cost_dollars: {total: 0.045}
`

// agentRunGoldenFailedScenario backs the failed-poll golden. A single
// unconditional turn is already terminal on the first poll, and a failed run
// needs nothing beyond its status: the schema has no run-level error.
const agentRunGoldenFailedScenario = `
version: 1
name: exa-agent-runs-failed-golden
time:
  base: 2026-01-01T00:00:00Z
providers:
  exa_agent_runs:
    turns:
      - respond:
          status: failed
`

// agentRunGolden404Scenario backs the unknown-run-id golden. It declares no
// exa_agent_runs entry at all: an unminted identifier 404s the same way
// regardless of what the entry would have scripted.
const agentRunGolden404Scenario = `
version: 1
name: exa-agent-runs-404-golden
`

// agentRunGoldenCancelScenario backs the cancel goldens. Its cancel script is one
// unconditional cancelled snapshot, so a cancel of the running run answers the
// cancelled run itself, with the usage and cost the scenario scripted for it.
const agentRunGoldenCancelScenario = `
version: 1
name: exa-agent-runs-cancel-golden
time:
  base: 2026-01-01T00:00:00Z
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - respond:
          status: completed
          output:
            text: Report A finds that deterministic simulators remove flakiness from adapter test suites.
          cost_dollars: {total: 0.045}
    cancel:
      turns:
        - respond:
            status: cancelled
            usage: {agent_compute_units: 2.5, searches: 3}
            cost_dollars: {total: 0.031, agent_compute: 0.025, search: 0.006}
`

// agentRunGoldenCancelFaultScenario is the cancel golden scenario with a cancel
// fault plan: a 429, then a 400, then the real answer. It is derived rather than
// restated so the 200 it ends on cannot drift from the scenario that backs
// exa-agent-runs-cancelled.json.
var agentRunGoldenCancelFaultScenario = strings.Replace(agentRunGoldenCancelScenario,
	"    cancel:\n", "    cancel:\n      fault: {attempts: [{status: 429}, {status: 400}, {}]}\n", 1)

// TestGolden_AgentRunCancelled pins POST /agent/runs/{id}/cancel's 200 body for a
// cancel that was recorded: the AgentRun its next poll returns, here cancelled,
// with stopReason cancelled and the scripted usage and cost.
func TestGolden_AgentRunCancelled(t *testing.T) {
	t.Parallel()

	s := newSim(t, agentRunGoldenCancelScenario)
	id := createRun(t, s, `{"query":"find the finding"}`)

	rec := s.do(request{method: http.MethodPost, path: "/agent/runs/" + id + "/cancel"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assertGoldenWire(t, "exa-agent-runs-cancelled.json", rec.Body.Bytes())
}

// TestGolden_AgentRunCancelErrors pins the three error bodies the cancel route
// produces on its own: 401 for a missing credential, 404 for a run this process
// does not hold, and the 500 for a cancel the scenario cannot answer. Each is
// AgentErrorResponse. A scripted fault's body is the fault's own; the 429 and the
// 400 a scripted fault renders with the status's default message are pinned by
// TestGolden_AgentRunCancelFaultedStatuses, and the other fault tests pin the
// rest.
func TestGolden_AgentRunCancelErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, src, fixture string
		status             int
		noAuth, unknown    bool
	}{
		{"401", agentRunGoldenCancelScenario, "exa-agent-runs-cancel-401.json", http.StatusUnauthorized, true, false},
		{"404", agentRunGoldenCancelScenario, "exa-agent-runs-cancel-404.json", http.StatusNotFound, false, true},
		{"500", agentRunGoldenScenario, "exa-agent-runs-cancel-500.json", http.StatusInternalServerError, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := newSim(t, tc.src)
			id := createRun(t, s, `{"query":"find the finding"}`)
			if tc.unknown {
				id = "agent_run_neverminted"
			}
			rec := s.do(request{method: http.MethodPost, path: "/agent/runs/" + id + "/cancel", noAuth: tc.noAuth})
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assertGoldenWire(t, tc.fixture, rec.Body.Bytes())
		})
	}
}

// TestGolden_AgentRunCancelFaultedStatuses pins what a scripted cancel.fault of
// status 429 and of status 400 renders when the attempt scripts no message: the
// status's default one. The 429 has no other source on this route. The 400 the
// route refuses a malformed body with carries the finding's text instead and is
// not this golden. A third cancel of the same run, after both faults, reaches the
// real answer.
func TestGolden_AgentRunCancelFaultedStatuses(t *testing.T) {
	t.Parallel()

	require.NotEqual(t, agentRunGoldenCancelScenario, agentRunGoldenCancelFaultScenario,
		"the cancel: anchor moved; the fault plan was not spliced in")

	s := newSim(t, agentRunGoldenCancelFaultScenario)
	id := createRun(t, s, `{"query":"find the finding"}`)

	steps := []struct {
		status  int
		fixture string
	}{
		{http.StatusTooManyRequests, "exa-agent-runs-cancel-429.json"},
		{http.StatusBadRequest, "exa-agent-runs-cancel-400.json"},
		{http.StatusOK, "exa-agent-runs-cancelled.json"},
	}
	for i, step := range steps {
		rec := s.do(request{method: http.MethodPost, path: "/agent/runs/" + id + "/cancel"})
		require.Equal(t, step.status, rec.Code, "cancel %d: %s", i, rec.Body.String())
		assertGoldenWire(t, step.fixture, rec.Body.Bytes())
	}
}

// TestGolden_AgentRunCreated pins POST /agent/runs's 200 body: the run in its
// initial queued status, per contracts/exa/README.md's async section.
func TestGolden_AgentRunCreated(t *testing.T) {
	t.Parallel()

	s := newSim(t, agentRunGoldenScenario)
	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"find the finding"}`})

	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-created.json", rec.Body.Bytes())
}

// TestGolden_AgentRunRunning pins a non-terminal poll: stopReason and
// completedAt present and explicitly null, and output, usage and costDollars the
// zero-valued placeholders for their required keys.
func TestGolden_AgentRunRunning(t *testing.T) {
	t.Parallel()

	s := newSim(t, agentRunGoldenScenario)
	id := createRun(t, s, `{"query":"find the finding"}`)

	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id})
	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-running.json", rec.Body.Bytes())
}

// TestGolden_AgentRunCompleted pins a terminal, successful poll: output with
// RESOLVED grounding (url and title, not a bare source id) and the scripted
// costDollars.total beside zero-filled components.
func TestGolden_AgentRunCompleted(t *testing.T) {
	t.Parallel()

	s := newSim(t, agentRunGoldenScenario)
	id := createRun(t, s, `{"query":"find the finding"}`)
	s.do(request{method: http.MethodGet, path: "/agent/runs/" + id}) // consume the running poll

	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id})
	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-completed.json", rec.Body.Bytes())
}

// TestGolden_AgentRunFailed pins a terminal failure: status failed and stopReason
// error, with no error key, and the required output, usage and costDollars at
// their placeholders.
func TestGolden_AgentRunFailed(t *testing.T) {
	t.Parallel()

	s := newSim(t, agentRunGoldenFailedScenario)
	id := createRun(t, s, `{"query":"find the finding"}`)

	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id})
	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-failed.json", rec.Body.Bytes())
}

// TestGolden_AgentRunNotFound pins the 404 for an identifier this process
// never minted: AgentErrorResponse with type NOT_FOUND and code RUN_NOT_FOUND.
// The spec documents the 404 and both enums but not the pairing, which is
// inference (see contracts/exa/README.md).
func TestGolden_AgentRunNotFound(t *testing.T) {
	t.Parallel()

	s := newSim(t, agentRunGolden404Scenario)
	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/agent_run_neverminted"})

	require.Equal(t, http.StatusNotFound, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-404.json", rec.Body.Bytes())
}

// TestGolden_AgentRunCreateAtTheJobBound pins the 503 a create gets when its
// namespace already holds the configured maximum of live jobs: a Servicesim
// configuration wall, not a vendor status (the spec documents no 503 here), in
// AgentErrorResponse's shape with SERVER_ERROR. The message names the bound and
// the remedy.
func TestGolden_AgentRunCreateAtTheJobBound(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{MaxJobs: 1})
	s := newSimWithJobs(t, agentRunGoldenScenario, store)

	first := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"first"}`})
	require.Equal(t, http.StatusOK, first.Code, "the first create must succeed: %s", first.Body.String())

	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"second"}`})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-503.json", rec.Body.Bytes())
}
