package exa

import (
	"net/http"
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
// unconditional turn is already terminal on the first poll.
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
          error:
            code: AGENT_RUN_FAILED
            message: the run could not be completed
`

// agentRunGolden404Scenario backs the unknown-run-id golden. It declares no
// exa_agent_runs entry at all: an unminted identifier 404s the same way
// regardless of what the entry would have scripted.
const agentRunGolden404Scenario = `
version: 1
name: exa-agent-runs-404-golden
`

// TestGolden_AgentRunCreated pins POST /agent/runs's 201 body: the initial
// status and nothing else, per contracts/exa/README.md's async section.
func TestGolden_AgentRunCreated(t *testing.T) {
	t.Parallel()

	s := newSim(t, agentRunGoldenScenario)
	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"find the finding"}`})

	require.Equal(t, http.StatusCreated, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-created.json", rec.Body.Bytes())
}

// TestGolden_AgentRunRunning pins a non-terminal poll: stopReason present and
// explicitly null, output/error/usage/costDollars all absent.
func TestGolden_AgentRunRunning(t *testing.T) {
	t.Parallel()

	s := newSim(t, agentRunGoldenScenario)
	id := createRun(t, s, `{"query":"find the finding"}`)

	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id})
	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-running.json", rec.Body.Bytes())
}

// TestGolden_AgentRunCompleted pins a terminal, successful poll: output with
// RESOLVED grounding (title and url, not a bare source id) and costDollars.
func TestGolden_AgentRunCompleted(t *testing.T) {
	t.Parallel()

	s := newSim(t, agentRunGoldenScenario)
	id := createRun(t, s, `{"query":"find the finding"}`)
	s.do(request{method: http.MethodGet, path: "/agent/runs/" + id}) // consume the running poll

	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id})
	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-completed.json", rec.Body.Bytes())
}

// TestGolden_AgentRunFailed pins a terminal failure: the error object, and
// costDollars.total present as 0 rather than omitted (the terminal-run
// inference in contracts/exa/README.md applies regardless of outcome).
func TestGolden_AgentRunFailed(t *testing.T) {
	t.Parallel()

	s := newSim(t, agentRunGoldenFailedScenario)
	id := createRun(t, s, `{"query":"find the finding"}`)

	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id})
	require.Equal(t, http.StatusOK, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-failed.json", rec.Body.Bytes())
}

// TestGolden_AgentRunNotFound pins the 404 for an identifier this process
// never minted: the same flat {requestId, error, tag} envelope and NOT_FOUND
// tag as unmatched routing, because the Agent API documents no run-specific
// 404 body of its own.
func TestGolden_AgentRunNotFound(t *testing.T) {
	t.Parallel()

	s := newSim(t, agentRunGolden404Scenario)
	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/run_neverminted"})

	require.Equal(t, http.StatusNotFound, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-404.json", rec.Body.Bytes())
}

// TestGolden_AgentRunCreateAtTheJobBound pins the 503 a create gets when its
// namespace already holds the configured maximum of live jobs: a Servicesim
// configuration wall, not a vendor status, and the message names the bound and
// the remedy.
func TestGolden_AgentRunCreateAtTheJobBound(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{MaxJobs: 1})
	s := newSimWithJobs(t, agentRunGoldenScenario, store)

	first := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"first"}`})
	require.Equal(t, http.StatusCreated, first.Code, "the first create must succeed: %s", first.Body.String())

	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"second"}`})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assertGoldenWire(t, "exa-agent-runs-503.json", rec.Body.Bytes())
}
