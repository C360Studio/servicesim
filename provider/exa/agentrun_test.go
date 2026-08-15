package exa

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/provider"
)

// asyncScenario is the shape §2.1 of the design specifies: two pending polls,
// then a terminal snapshot that also answers every poll after it.
const asyncScenario = `
version: 1
name: exa-agent-run-completes
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: The report states the finding.
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
            grounding:
              - field: answer
                citations: [source-a]
                confidence: high
          cost_dollars: {total: 0.045}
`

// asyncSim builds a sim with a job store wired, which the async surface needs:
// without one a create still answers but no poll can resolve its identifier.
func asyncSim(t *testing.T, src string) *sim {
	t.Helper()
	s := newSim(t, src)
	return s
}

func createRun(t *testing.T, s *sim, body string) string {
	t.Helper()
	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: body})
	require.Equal(t, http.StatusCreated, rec.Code, "create failed: %s", rec.Body.String())

	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out.ID)
	assert.Equal(t, StatusQueued, out.Status, "a create returns the initial status, never a terminal one")
	return out.ID
}

func pollRun(t *testing.T, s *sim, id string) map[string]any {
	t.Helper()
	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id})
	require.Equal(t, http.StatusOK, rec.Code, "poll failed: %s", rec.Body.String())

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

// TestAgentRunCreateThenPollToCompletion is the lifecycle this whole unit
// exists for: a create returns an identifier immediately, and the output exists
// only once a poll reaches a terminal status.
func TestAgentRunCreateThenPollToCompletion(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	id := createRun(t, s, `{"query":"find the finding"}`)

	for i := range 2 {
		got := pollRun(t, s, id)
		assert.Equal(t, StatusRunning, got["status"], "poll %d should still be running", i)
		assert.Nil(t, got["output"], "output must not exist before a terminal status")
		assert.Nil(t, got["costDollars"], "a run that has not finished has spent nothing")
		assert.Nil(t, got["stopReason"], "a non-terminal run carries a null stop reason")
	}

	done := pollRun(t, s, id)
	assert.Equal(t, StatusCompleted, done["status"])
	assert.Equal(t, StopSchemaSatisfied, done["stopReason"], "a completed run derives its stop reason")

	output, ok := done["output"].(map[string]any)
	require.True(t, ok, "a terminal run carries output: %v", done)
	assert.Equal(t, "Report A states the finding.", output["text"])

	// Grounding citations must be RESOLVED against the corpus. A source
	// reference carries only an id until it is, so asserting on text alone would
	// pass with every citation rendering an empty title and URL.
	grounding, ok := output["grounding"].([]any)
	require.True(t, ok, "a grounded run carries grounding: %v", output)
	require.Len(t, grounding, 1)

	first, _ := grounding[0].(map[string]any)
	citations, ok := first["citations"].([]any)
	require.True(t, ok, "grounding carries citations: %v", first)
	require.Len(t, citations, 1)

	citation, _ := citations[0].(map[string]any)
	assert.Equal(t, "Report A", citation["title"], "the citation did not resolve against the corpus")
	assert.Equal(t, "https://example.test/report-a", citation["url"])

	// costDollars.total is emitted on every terminal run. See the recorded
	// inference in contracts/exa/README.md for why it ships on evidence rather
	// than on a vendor example — and note it must be here from the FIRST
	// release, because adding a cost key after adopters hold goldens rewrites
	// the bytes of every one of those files.
	cost, ok := done["costDollars"].(map[string]any)
	require.True(t, ok, "a terminal run carries costDollars: %v", done)
	assert.InDelta(t, 0.045, cost["total"], 1e-9)
	assert.NotContains(t, cost, "search",
		"costDollars.search is not confirmed on this surface and must not be copied across from /search")

	// The terminal turn is unconditional, so it answers every later poll too —
	// which is what every real job API does with a finished run.
	again := pollRun(t, s, id)
	assert.Equal(t, StatusCompleted, again["status"])
}

// TestAgentRunCreateCredentialTurnKeyNeverLeaksTheToken is the async half of the
// turn_key credential fix (provider/lane.go turnLaneKey, internal/journal Redact):
// a scenario may legitimately key the create route's lane on which credential
// was presented (turn_key: [header:authorization] — Phase 6's credential-rotation
// shape), and the raw token must not survive that choice by ANY path
// (CLAUDE.md house rule 4). This is the end-to-end path through provider.Handle,
// so it also proves the async surface specifically: MintJob copies x.Lane().Key
// into jobs.Job.LaneKey verbatim, and that record is never redacted (it is never
// served — see internal/admin/jobs.go's JobSummary doc comment), so the
// fingerprinting has to happen at composition or the registry itself leaks.
func TestAgentRunCreateCredentialTurnKeyNeverLeaksTheToken(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: agent-run-header-auth-key
providers:
  exa_agent_runs:
    turn_key: ["header:authorization"]
    turns:
      - respond: {status: completed}
`
	const sentinel = "Bearer sk-live-SENTINEL"

	store := jobs.NewRegistry(jobs.Limits{})
	s := newSimWithJobs(t, src, store)

	rec := s.do(request{
		method:  http.MethodPost,
		path:    "/agent/runs",
		body:    `{"query":"find it"}`,
		headers: map[string]string{"Authorization": sentinel},
		noAuth:  true, // the header above IS the credential under test
	})
	require.Equal(t, http.StatusCreated, rec.Code, "create failed: %s", rec.Body.String())

	var out struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	entry := entries[0]

	require.NotContains(t, entry.Outcome.FaultKey, "SENTINEL",
		"the credential must not survive into the journal's outcome.fault_key")
	require.Contains(t, entry.Outcome.FaultKey, "header:authorization=",
		"the extractor must still name which discriminator contributed")

	encoded, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "SENTINEL",
		"the credential must not survive anywhere in the entry's wire encoding")

	job, found := store.Lookup(provider.DefaultNamespace, out.ID)
	require.True(t, found, "the create must leave a record a poll can resolve")
	require.NotContains(t, job.LaneKey, "SENTINEL",
		"the credential must not survive into the job registry, which is never redacted")
}

// Two runs polled in one namespace must not share a cursor. This is the failure
// Route.LaneFrom exists to prevent, and it is invisible without two runs in
// flight at once.
func TestAgentRunPollCursorsArePerRun(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	first := createRun(t, s, `{"query":"first"}`)
	second := createRun(t, s, `{"query":"second"}`)
	require.NotEqual(t, first, second, "two creates must mint different identifiers")

	// Walk the first run to completion while the second stays untouched.
	for range 2 {
		assert.Equal(t, StatusRunning, pollRun(t, s, first)["status"])
	}
	assert.Equal(t, StatusCompleted, pollRun(t, s, first)["status"])

	// The second run starts at ITS poll 0, not at the first run's position.
	assert.Equal(t, StatusRunning, pollRun(t, s, second)["status"],
		"the second run's first poll drew from the first run's cursor")
}

// An identifier this process never minted is the vendor's 404, and it must not
// consume a poll from any run's script.
func TestAgentRunPollUnknownIdentifier(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	id := createRun(t, s, `{"query":"q"}`)

	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/run_neverminted"})
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// The real run is untouched: its first poll is still poll 0.
	assert.Equal(t, StatusRunning, pollRun(t, s, id)["status"])
}

// HEAD answers existence and claims nothing. A status assertion alone would pass
// even if HEAD fell through to the GET handler, so this checks the cursor.
func TestAgentRunHeadDoesNotConsumeAPoll(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	id := createRun(t, s, `{"query":"q"}`)

	for range 3 {
		rec := s.do(request{method: http.MethodHead, path: "/agent/runs/" + id})
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Body.String(), "a HEAD carries no body")
	}

	rec := s.do(request{method: http.MethodHead, path: "/agent/runs/run_neverminted"})
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Three HEADs and a miss consumed nothing: the first GET is still poll 0.
	assert.Equal(t, StatusRunning, pollRun(t, s, id)["status"])
}

// A create is rejected before it mints anything, so a bad request cannot consume
// a job slot or an identifier.
func TestAgentRunCreateRejectsAMissingQuery(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{}`})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.True(t, s.hasFinding(codeQueryMissing))
}

// A failed run carries its error, which is what a consumer's failure branch
// reads. The scenario declaring failed without an error is a load error, tested
// separately in the validator tests.
func TestAgentRunFailedCarriesItsError(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, `
version: 1
name: exa-agent-run-fails
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - respond:
          status: failed
          error:
            code: AGENT_RUN_FAILED
            message: the run could not be completed
`)
	id := createRun(t, s, `{"query":"q"}`)
	assert.Equal(t, StatusRunning, pollRun(t, s, id)["status"])

	got := pollRun(t, s, id)
	assert.Equal(t, StatusFailed, got["status"])
	assert.Equal(t, StopError, got["stopReason"], "a failed run derives the error stop reason")

	failure, ok := got["error"].(map[string]any)
	require.True(t, ok, "a failed run carries an error: %v", got)
	assert.Equal(t, "AGENT_RUN_FAILED", failure["code"])
}

// A stuck run never terminates: the consumer's own timeout is what fires, which
// is the behaviour under test. Servicesim does not decide the run is stuck.
func TestAgentRunStuckPending(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, `
version: 1
name: exa-agent-run-stuck
providers:
  exa_agent_runs:
    turns:
      - respond: {status: running}
`)
	id := createRun(t, s, `{"query":"q"}`)
	for range 5 {
		assert.Equal(t, StatusRunning, pollRun(t, s, id)["status"])
	}
}

// The single-shot form is the zero-pending-poll run: one unconditional turn, so
// the first poll is already terminal.
func TestAgentRunSingleShotIsTerminalImmediately(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, `
version: 1
name: exa-agent-run-immediate
providers:
  exa_agent_runs:
    status: completed
    output:
      text: done
    cost_dollars: {total: 0.01}
`)
	id := createRun(t, s, `{"query":"q"}`)
	got := pollRun(t, s, id)

	assert.Equal(t, StatusCompleted, got["status"])
	require.NotNil(t, got["output"])
}

// Identifiers are derived, so the same scenario and the same call position mint
// the same identifier on every run — which is what makes a golden portable.
func TestAgentRunIdentifiersAreDeterministic(t *testing.T) {
	t.Parallel()

	first := createRun(t, asyncSim(t, asyncScenario), `{"query":"q"}`)
	second := createRun(t, asyncSim(t, asyncScenario), `{"query":"q"}`)

	assert.Equal(t, first, second, "the same scenario and call position must mint the same identifier")
	assert.True(t, provider.ValidJobID(first), "a minted identifier must be resolvable: %q", first)
}

// --- validator ------------------------------------------------------------

func TestAgentRunValidatorFindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		src      string
		wantCode string
		wantErr  bool
	}{
		{
			name:     "failed without an error",
			src:      `{status: failed}`,
			wantCode: CodeAgentRunFailedWithoutError,
			wantErr:  true,
		},
		{
			name:     "an unknown status",
			src:      `{status: nearly}`,
			wantCode: CodeAgentRunStatusUnknown,
			wantErr:  true,
		},
		{
			name:     "an unknown stop reason",
			src:      `{status: completed, stop_reason: bored, output: {text: x}}`,
			wantCode: CodeAgentRunStopReasonUnknown,
			wantErr:  true,
		},
		{
			name:     "completed with no output",
			src:      `{status: completed}`,
			wantCode: CodeAgentRunCompletedWithoutOutput,
			wantErr:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sc := mustScenario(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n    turns:\n      - respond: "+tc.src+"\n")
			findings := provider.ValidateScenario(sc, map[string]provider.Validator{
				NameAgentRuns: AgentRunValidator{},
			})

			var found bool
			for _, f := range findings {
				if f.Code == tc.wantCode {
					found = true
					if tc.wantErr {
						assert.Equal(t, "error", string(f.Severity), "%s must be an error", tc.wantCode)
					}
				}
			}
			assert.True(t, found, "want %s, got %+v", tc.wantCode, findings)
		})
	}
}

// A body predicate on a poll can never match, because a GET carries no body.
// Without a load-time check the turn is simply dead and nothing says so.
func TestAgentRunValidatorRejectsABodyPredicateOnAPoll(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: v
providers:
  exa_agent_runs:
    turns:
      - when: {body_contains: anything}
        respond: {status: running}
      - respond: {status: completed, output: {text: x}}
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{
		NameAgentRuns: AgentRunValidator{},
	})

	var found bool
	for _, f := range findings {
		if f.Code == CodeAgentRunBodyPredicateOnPoll {
			found = true
		}
	}
	assert.True(t, found, "a body predicate on a poll must warn: %+v", findings)
}

// A run does not un-complete. A non-terminal snapshot after a terminal one is
// unreachable as well as wrong.
func TestAgentRunValidatorRejectsTerminalThenPending(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: v
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: completed, output: {text: x}}
      - respond: {status: running}
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{
		NameAgentRuns: AgentRunValidator{},
	})

	var found bool
	for _, f := range findings {
		if f.Code == CodeAgentRunTerminalThenPending {
			found = true
		}
	}
	assert.True(t, found, "a run that un-completes must be an error: %+v", findings)
}

// --- create.fault ---------------------------------------------------------

// The create and poll routes draw on SEPARATE plans, read from different places
// in the scenario. Two independent counters walking one script would be the same
// bug in a different location.
func TestCreateFaultIsIndependentOfThePollPlan(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: v
providers:
  exa_agent_runs:
    create:
      fault:
        attempts:
          - {status: 429}
          - {status: 200}
    turns:
      - fault:
          attempts:
            - {status: 200}
            - {status: 503}
        respond: {status: completed, output: {text: x}}
`)

	create := createFault(sc)
	require.NotNil(t, create, "the create plan comes from create.fault")
	require.True(t, create.HasAttempts())
	assert.Equal(t, 429, create.Attempts[0].Status)

	poll := provider.TurnFault(sc, NameAgentRuns)
	require.NotNil(t, poll, "the poll plan comes from the first turn declaring attempts")
	assert.Equal(t, 200, poll.Attempts[0].Status)
	assert.Equal(t, 503, poll.Attempts[1].Status)
}

// A scenario whose only fault is on the create must still report HasFaults, or
// Deps.Normalized skips the deps.faults_ignored warning and the scripted fault
// silently never fires.
func TestCreateOnlyFaultCountsAsAFault(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: v
providers:
  exa_agent_runs:
    create:
      fault:
        attempts:
          - {status: 429}
    turns:
      - respond: {status: completed, output: {text: x}}
`)
	assert.True(t, sc.HasFaults(), "a create-only plan is still a fault plan")
}

// A create refused at the job bound must not report success, and must name the
// remedy rather than only the symptom.
func TestAgentRunCreateAtTheJobBound(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{MaxJobs: 1})
	s := newSimWithJobs(t, asyncScenario, store)

	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"first"}`})
	require.Equal(t, http.StatusCreated, rec.Code)

	rec = s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"second"}`})
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"a create past the bound is a Servicesim configuration wall, not a malformed client request")
	assert.True(t, s.hasFinding(provider.CodeJobLimitReached))
}
