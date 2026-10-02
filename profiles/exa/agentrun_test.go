package exa

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/scenarios"
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
	require.Equal(t, http.StatusOK, rec.Code, "create failed: %s", rec.Body.String())

	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out.ID)
	assert.Equal(t, statusQueued, out.Status, "a create returns the initial status, never a terminal one")
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
		assert.Equal(t, statusRunning, got["status"], "poll %d should still be running", i)
		assert.Equal(t, map[string]any{"text": "", "structured": nil, "grounding": []any{}}, got["output"],
			"output is a required non-nullable object, so a run that has not finished carries an empty one")
		assert.Nil(t, got["completedAt"], "a run that has not finished has not completed")
		assert.Nil(t, got["stopReason"], "a non-terminal run carries a null stop reason")
		_, present := got["stopReason"]
		assert.True(t, present, "stopReason must be present and explicitly null, not omitted")
	}

	done := pollRun(t, s, id)
	assert.Equal(t, statusCompleted, done["status"])
	assert.Equal(t, stopSchemaSatisfied, done["stopReason"], "a completed run derives its stop reason")

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

	// costDollars is required on every run and its total is scripted here; the
	// components the scenario did not script are zero placeholders.
	cost, ok := done["costDollars"].(map[string]any)
	require.True(t, ok, "a terminal run carries costDollars: %v", done)
	assert.InDelta(t, 0.045, cost["total"], 1e-9)
	assert.Equal(t, 0.0, cost["search"],
		"costDollars.search is a required scalar on this surface; unscripted it is a zero placeholder")

	// The terminal turn is unconditional, so it answers every later poll too —
	// which is what every real job API does with a finished run.
	again := pollRun(t, s, id)
	assert.Equal(t, statusCompleted, again["status"])
}

// Every served poll records its position on the run, from the first poll on: a
// cancel judges "terminal at cancel time" by the run's next poll, so a poll that
// did not advance would leave every later cancel judging the wrong snapshot. HEAD
// claims nothing and so advances nothing.
func TestAgentRunPollsAdvanceTheRun(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{})
	s := newSimWithJobs(t, asyncScenario, store)
	id := createRun(t, s, `{"query":"q"}`)

	for want := 1; want <= 4; want++ {
		pollRun(t, s, id)
		job, ok := store.Lookup(provider.DefaultNamespace, id)
		require.True(t, ok)
		assert.Equal(t, want, job.Polls, "after poll %d", want)
	}

	rec := s.do(request{method: http.MethodHead, path: "/agent/runs/" + id})
	require.Equal(t, http.StatusOK, rec.Code)
	job, _ := store.Lookup(provider.DefaultNamespace, id)
	assert.Equal(t, 4, job.Polls, "HEAD is not a poll")
	assert.False(t, job.CancelRequested)
}

// noAgentRunsEntry is a scenario that scripts Exa's synchronous surface and
// declares no exa_agent_runs entry at all.
const noAgentRunsEntry = `
version: 1
name: no-agent-runs-entry
providers:
  exa: {}
`

// codesIn lists the finding codes of one journal entry.
func codesIn(e journal.Entry) []string {
	var out []string
	for _, f := range e.Findings {
		out = append(out, f.Code)
	}
	return out
}

// A scenario with no exa_agent_runs entry has no poll script, so a run created
// against it could never be polled honestly. The create fails closed — the
// agent envelope's 404, with scenario.no_matching_turn — before it claims
// anything, and no job is recorded. Before this, the create succeeded and every
// poll served a pending snapshot forever while the run's position never moved.
func TestAgentRunCreateWithNoEntryFailsClosed(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{})
	s := newSimWithJobs(t, noAgentRunsEntry, store)

	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), agentCodeRunNotFound)

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	assert.Contains(t, codesIn(entries[0]), provider.CodeNoMatchingTurn)
	assert.NotContains(t, codesIn(entries[0]), provider.CodeAttemptOnRejection, "the refusal claims nothing")
	assert.Empty(t, store.List(), "no run is recorded")
}

// A poll that reaches a scenario with no exa_agent_runs entry — one process
// serving two scenarios, the run created under the one that declares it — fails
// loudly rather than serving a default: scenario.no_matching_turn and a 404. It
// still claims its poll and records it on the run, so the run's position keeps
// matching its poll lane.
func TestAgentRunPollWithNoEntryFailsLoudly(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{})
	id := createRun(t, newSimWithJobs(t, asyncScenario, store), `{"query":"q"}`)

	other := newSimWithJobs(t, noAgentRunsEntry, store)
	rec := other.do(request{method: http.MethodGet, path: "/agent/runs/" + id})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	entries := other.journal.Snapshot()
	require.Len(t, entries, 1)
	assert.Contains(t, codesIn(entries[0]), provider.CodeNoMatchingTurn)

	job, ok := store.Lookup(provider.DefaultNamespace, id)
	require.True(t, ok)
	assert.Equal(t, 1, job.Polls, "the claimed poll is recorded on the run")
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
	require.Equal(t, http.StatusOK, rec.Code, "create failed: %s", rec.Body.String())

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
		assert.Equal(t, statusRunning, pollRun(t, s, first)["status"])
	}
	assert.Equal(t, statusCompleted, pollRun(t, s, first)["status"])

	// The second run starts at ITS poll 0, not at the first run's position.
	assert.Equal(t, statusRunning, pollRun(t, s, second)["status"],
		"the second run's first poll drew from the first run's cursor")
}

// An identifier this process never minted is the vendor's 404, and it must not
// consume a poll from any run's script.
func TestAgentRunPollUnknownIdentifier(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	id := createRun(t, s, `{"query":"q"}`)

	rec := s.do(request{method: http.MethodGet, path: "/agent/runs/agent_run_neverminted"})
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// The real run is untouched: its first poll is still poll 0.
	assert.Equal(t, statusRunning, pollRun(t, s, id)["status"])
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

	rec := s.do(request{method: http.MethodHead, path: "/agent/runs/agent_run_neverminted"})
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Three HEADs and a miss consumed nothing: the first GET is still poll 0.
	assert.Equal(t, statusRunning, pollRun(t, s, id)["status"])
}

// TestAgentRunPollForeignIDDiagnostic is design §8's diagnostic, end to end: a
// poll for an identifier that is SHAPED like one this process mints, but that
// this process never minted, is still the vendor's ordinary 404 — the response
// is byte-identical either way — but in a namespace that has minted a real run
// it additionally raises provider.CodeJobForeignID as a WARNING, never an
// error, so testkit.AssertNoErrors would still pass it.
func TestAgentRunPollForeignIDDiagnostic(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	// Well-formed for Exa's own scheme (runIDPrefix + 32 hex), never minted by
	// this process.
	const foreignID = "agent_run_deadbeefdeadbeefdeadbeefdeadbeef"

	// X: the default namespace has minted a real run.
	createRun(t, s, `{"query":"seed X"}`)

	pollX := s.do(request{method: http.MethodGet, path: "/agent/runs/" + foreignID})
	require.Equal(t, http.StatusNotFound, pollX.Code)
	assert.True(t, s.hasFinding(codeAgentRunNotFound), "the vendor-shaped 404 finding still fires")
	require.True(t, s.hasFinding(provider.CodeJobForeignID), "a minted namespace must raise the diagnostic on a miss")
	assert.Equal(t, journal.SeverityWarning, s.findingSeverity(provider.CodeJobForeignID),
		"a typo'd fixture id in a one-process suite must not read as an error")

	entries := s.journal.Snapshot()
	require.NotEmpty(t, entries)
	require.Empty(t, entries[len(entries)-1].Errors(),
		"testkit.AssertNoErrors must still pass a request that only warned")

	// Y: a namespace that has minted nothing at all.
	pollY := s.do(request{method: http.MethodGet, path: "/n/foreign-empty/agent/runs/" + foreignID})
	assert.Equal(t, http.StatusNotFound, pollY.Code)
	assert.False(t, s.hasFinding(provider.CodeJobForeignID),
		"an empty namespace has minted nothing, so a miss there is a typo, not a divergence")

	assert.Equal(t, pollX.Body.Bytes(), pollY.Body.Bytes(),
		"the diagnostic must not change the vendor-shaped response body")

	// HEAD asks the same question as GET — does this run exist here — and gets
	// the same diagnostic.
	head := s.do(request{method: http.MethodHead, path: "/agent/runs/" + foreignID})
	assert.Equal(t, http.StatusNotFound, head.Code)
	assert.True(t, s.hasFinding(provider.CodeJobForeignID), "HEAD raises the same diagnostic as GET")
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
		assert.Equal(t, statusRunning, pollRun(t, s, id)["status"])
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

	assert.Equal(t, statusCompleted, got["status"])
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

// The createAgentRun operation documents ONLY a 200 (openapi line 831), and
// AgentRunId says new run ids carry the `agent_run_` prefix (line 4836). Both
// were simulator-chosen before: 201 and `run_`.
func TestAgentRunCreateAnswers200AndMintsAnAgentRunPrefixedID(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})

	require.Equal(t, http.StatusOK, rec.Code, "the spec documents no 201 for createAgentRun: %s", rec.Body.String())

	var out struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Regexp(t, `^agent_run_[0-9a-f]{32}$`, out.ID)
	assert.Len(t, out.ID, 42, "agent_run_ plus 32 hex characters")
	assert.LessOrEqual(t, len(out.ID), provider.MaxJobIDLen, "a minted id must fit the job-id bound")
	assert.True(t, provider.ValidJobID(out.ID), "a minted identifier must be resolvable: %q", out.ID)
}

// effort is a closed enum in the spec (AgentEffort, line 4819): minimal, low,
// medium, high, xhigh, auto, ultra. `max` is not a member and `ultra` is.
func TestAgentRunCreateEffortIsAClosedEnum(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		body   string
		wantOK bool
	}{
		{name: "absent", body: `{"query":"q"}`, wantOK: true},
		{name: "minimal", body: `{"query":"q","effort":"minimal"}`, wantOK: true},
		{name: "low", body: `{"query":"q","effort":"low"}`, wantOK: true},
		{name: "medium", body: `{"query":"q","effort":"medium"}`, wantOK: true},
		{name: "high", body: `{"query":"q","effort":"high"}`, wantOK: true},
		{name: "xhigh", body: `{"query":"q","effort":"xhigh"}`, wantOK: true},
		{name: "auto", body: `{"query":"q","effort":"auto"}`, wantOK: true},
		{name: "ultra", body: `{"query":"q","effort":"ultra"}`, wantOK: true},
		{name: "max was removed", body: `{"query":"q","effort":"max"}`},
		{name: "unknown value", body: `{"query":"q","effort":"turbo"}`},
		{name: "wrong case", body: `{"query":"q","effort":"High"}`},
		{name: "not a string", body: `{"query":"q","effort":5}`},
		{name: "null", body: `{"query":"q","effort":null}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := asyncSim(t, asyncScenario)
			rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: tc.body})

			if tc.wantOK {
				assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
				assert.False(t, s.hasFinding(codeEffortInvalid))
				return
			}
			assert.Equal(t, http.StatusBadRequest, rec.Code, "an off-enum effort is a documented-enum violation")
			assert.Equal(t, journal.SeverityError, s.findingSeverity(codeEffortInvalid))
		})
	}
}

// A rejected create mints nothing: the effort rejection runs before MintJob, so
// the next valid create is still call 0 and gets the first identifier.
func TestAgentRunCreateEffortRejectionMintsNothing(t *testing.T) {
	t.Parallel()

	rejected := asyncSim(t, asyncScenario)
	require.Equal(t, http.StatusBadRequest,
		rejected.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q","effort":"max"}`}).Code)
	after := createRun(t, rejected, `{"query":"q"}`)

	assert.Equal(t, createRun(t, asyncSim(t, asyncScenario), `{"query":"q"}`), after,
		"a rejected create must not claim the call index")
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
			name:     "a run-level error block",
			src:      `{status: failed, error: {code: X, message: y}}`,
			wantCode: codeAgentRunErrorNotInSchema,
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
				NameAgentRuns: agentRunValidator{},
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

// AgentStopReason (openapi line 5848) has six members. Four of them were
// authorable before; time_limit_reached and stopped were rejected at load.
func TestAgentRunValidatorAcceptsEverySpecStopReason(t *testing.T) {
	t.Parallel()

	for _, reason := range []string{
		"schema_satisfied", "budget_reached", "time_limit_reached", "stopped", "error", "cancelled",
	} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()

			sc := mustScenario(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n    turns:\n"+
				"      - respond: {status: completed, stop_reason: "+reason+", output: {text: x}}\n")
			findings := provider.ValidateScenario(sc, map[string]provider.Validator{
				NameAgentRuns: agentRunValidator{},
			})
			for _, f := range findings {
				assert.NotEqual(t, CodeAgentRunStopReasonUnknown, f.Code, "%s is a documented stop reason", reason)
			}
		})
	}
}

// A scripted stop reason reaches the wire verbatim, including the two the
// validator used to refuse.
func TestAgentRunRendersEverySpecStopReason(t *testing.T) {
	t.Parallel()

	for _, reason := range []string{
		"schema_satisfied", "budget_reached", "time_limit_reached", "stopped", "error", "cancelled",
	} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()

			s := asyncSim(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n"+
				"    status: completed\n    stop_reason: "+reason+"\n    output: {text: x}\n")
			id := createRun(t, s, `{"query":"q"}`)

			assert.Equal(t, reason, pollRun(t, s, id)["stopReason"])
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
		NameAgentRuns: agentRunValidator{},
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
		NameAgentRuns: agentRunValidator{},
	})

	var found bool
	for _, f := range findings {
		if f.Code == CodeAgentRunTerminalThenPending {
			found = true
		}
	}
	assert.True(t, found, "a run that un-completes must be an error: %+v", findings)
}

// TestAgentRunTerminalIsAbsorbingInServeOrder is D3
// (docs/proposals/cancellation-and-accepted-create.md): turns are served by
// first match on call_index, not in declaration order, so "terminal is
// absorbing" has to be judged on the order polls are actually served in.
// Judged in declaration order, the built-in async-failed scenario with its
// first Exa turn moved from call_index 0 to 1 loaded clean and served failed,
// running, failed — a run a client watched fail, then resume.
func TestAgentRunTerminalIsAbsorbingInServeOrder(t *testing.T) {
	t.Parallel()

	validate := func(t *testing.T, sc *scenario.Scenario) []scenario.Finding {
		t.Helper()
		var found []scenario.Finding
		for _, f := range provider.ValidateScenario(sc, map[string]provider.Validator{NameAgentRuns: agentRunValidator{}}) {
			if f.Code == CodeAgentRunTerminalThenPending {
				found = append(found, f)
			}
		}
		return found
	}

	t.Run("the async-failed reproduction", func(t *testing.T) {
		t.Parallel()

		src, err := scenarios.Read("async-failed")
		require.NoError(t, err)
		const running = "      - when: {call_index: 0}\n        respond: {status: running}\n"
		require.Equal(t, 1, strings.Count(string(src), running), "the Exa poll script changed shape; update this test")
		sc := mustScenario(t, strings.Replace(string(src), running,
			"      - when: {call_index: 1}\n        respond: {status: running}\n", 1))

		// The defect's mechanics, through the selector the poll route uses.
		e := sc.Provider(NameAgentRuns)
		var served []string
		for poll := range 3 {
			turn, at, err := provider.SelectTurn(e, poll, faultKeyRunPoll, nil)
			require.NoError(t, err)
			var p agentRunProjection
			require.NoError(t, turn.DecodeProjection(e.Name, at, &p))
			served = append(served, p.EffectiveStatus())
		}
		require.Equal(t, []string{statusFailed, statusRunning, statusFailed}, served)

		found := validate(t, sc)
		require.Len(t, found, 1, "the run un-fails at poll 1, so load must refuse it")
		assert.Equal(t, scenario.SeverityError, found[0].Severity)
		assert.Equal(t, "providers.exa_agent_runs.turns[0].respond.status", found[0].Path)
		assert.Contains(t, found[0].Message, "poll 1")
	})

	t.Run("a shadowed turn is never served, so it cannot un-complete anything", func(t *testing.T) {
		t.Parallel()

		sc := mustScenario(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n    turns:\n"+
			"      - when: {call_index: 0}\n        respond: {status: completed, output: {text: x}}\n"+
			"      - when: {call_index: 0}\n        respond: {status: running}\n"+
			"      - respond: {status: completed, output: {text: x}}\n")
		assert.Empty(t, validate(t, sc))
	})

	t.Run("a terminal snapshot scheduled after the fallback", func(t *testing.T) {
		t.Parallel()

		// Polls 0 and 1 fall through to running, poll 2 completes, and poll 3
		// falls through to running again.
		sc := mustScenario(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n    turns:\n"+
			"      - when: {call_index: 2}\n        respond: {status: completed, output: {text: x}}\n"+
			"      - respond: {status: running}\n")
		found := validate(t, sc)
		require.Len(t, found, 1)
		assert.Equal(t, "providers.exa_agent_runs.turns[1].respond.status", found[0].Path)
		assert.Contains(t, found[0].Message, "poll 3")
	})

	t.Run("the cancel script is checked on its own", func(t *testing.T) {
		t.Parallel()

		sc := mustScenario(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n    cancel:\n      turns:\n"+
			"        - when: {call_index: 1}\n          respond: {status: running}\n"+
			"        - respond: {status: cancelled}\n"+
			"    turns:\n      - respond: {status: running}\n")
		found := validate(t, sc)
		require.Len(t, found, 1)
		assert.Equal(t, "providers.exa_agent_runs.cancel.turns[0].respond.status", found[0].Path)
	})

	t.Run("each script is judged independently", func(t *testing.T) {
		t.Parallel()

		// A cancel is recorded only while the next poll is non-terminal, so a
		// poll script that ends terminal says nothing about how a cancel script
		// may start.
		sc := mustScenario(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n    cancel:\n      turns:\n"+
			"        - when: {call_index: 0}\n          respond: {status: running}\n"+
			"        - respond: {status: cancelled}\n"+
			"    turns:\n      - when: {call_index: 0}\n        respond: {status: running}\n"+
			"      - respond: {status: completed, output: {text: x}}\n")
		assert.Empty(t, validate(t, sc))
	})
}

// cancel.turns are poll snapshots of the same run, so every check a poll
// snapshot gets applies to them, addressed by their own paths. `completed` is
// allowed there: "acknowledged, then completed anyway" is a real outcome.
func TestAgentRunValidatorChecksCancelTurns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		turns    string
		fault    string
		wantCode string
		wantPath string
		wantErr  bool
	}{
		{
			name:     "an undecodable snapshot",
			turns:    "        - respond: {status: cancelled, bogus: 1}\n",
			wantCode: codeProjectionInvalid,
			wantPath: "providers.exa_agent_runs.cancel.turns[0].respond",
			wantErr:  true,
		},
		{
			name:     "an unknown status",
			turns:    "        - respond: {status: stopping}\n",
			wantCode: CodeAgentRunStatusUnknown,
			wantPath: "providers.exa_agent_runs.cancel.turns[0].respond.status",
			wantErr:  true,
		},
		{
			name:     "a negative usage value",
			turns:    "        - respond: {status: cancelled, usage: {agent_compute_units: -1}}\n",
			wantCode: codeAgentRunValueRange,
			wantPath: "providers.exa_agent_runs.cancel.turns[0].respond.usage.agent_compute_units",
			wantErr:  true,
		},
		{
			name:     "a body predicate",
			turns:    "        - when: {body_contains: x}\n          respond: {status: running}\n        - respond: {status: cancelled}\n",
			wantCode: CodeAgentRunBodyPredicateOnPoll,
			wantPath: "providers.exa_agent_runs.cancel.turns[0].when",
		},
		{
			name:     "a script that runs out",
			turns:    "        - when: {call_index: 0}\n          respond: {status: cancelled}\n",
			wantCode: CodeAgentRunScriptExhausted,
			wantPath: "providers.exa_agent_runs.cancel.turns[0].when",
		},
		{
			name:     "an off-vocabulary tag on the cancel plan",
			turns:    "        - respond: {status: cancelled}\n",
			fault:    "      fault: {attempts: [{status: 500, tag: RATE_LIMITED_FLAT}]}\n",
			wantCode: codeAgentRunFaultTagUnknown,
			wantPath: "providers.exa_agent_runs.cancel.fault.attempts[0].tag",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sc := mustScenario(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n    cancel:\n"+tc.fault+
				"      turns:\n"+tc.turns+"    turns:\n      - respond: {status: completed, output: {text: x}}\n")
			findings := provider.ValidateScenario(sc, provider.MustSet(Profile()).Validators())

			var found []scenario.Finding
			for _, f := range findings {
				if f.Code == tc.wantCode {
					found = append(found, f)
				}
			}
			require.Len(t, found, 1, "want one %s, got %+v", tc.wantCode, findings)
			assert.Equal(t, tc.wantPath, found[0].Path)
			if tc.wantErr {
				assert.Equal(t, scenario.SeverityError, found[0].Severity)
			}
		})
	}

	t.Run("completed is allowed", func(t *testing.T) {
		t.Parallel()

		sc := mustScenario(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n    cancel:\n      turns:\n"+
			"        - when: {call_index: 0}\n          respond: {status: running}\n"+
			"        - respond: {status: completed, output: {text: x}}\n"+
			"    turns:\n      - respond: {status: running}\n")
		for _, f := range provider.ValidateScenario(sc, provider.MustSet(Profile()).Validators()) {
			assert.NotEqual(t, scenario.SeverityError, f.Severity, "unexpected error: %+v", f)
		}
	})
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
	require.Equal(t, http.StatusOK, rec.Code)

	rec = s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"second"}`})
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"a create past the bound is a Servicesim configuration wall, not a malformed client request")
	assert.True(t, s.hasFinding(provider.CodeJobLimitReached))
}

// --- create.fault and job retention ----------------------------------------

// A create is only worth a job when the response carries the identifier to the
// client. oversized_body pads the rendered body and writes it whole, so the
// client does hold the identifier — and every poll for it has to resolve.
func TestAgentRunCreateUnderOversizedBodyStillLeavesAPollableJob(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: exa-oversized-create
providers:
  exa_agent_runs:
    create:
      fault:
        attempts:
          - {kind: oversized_body, body_bytes: 4096}
    turns:
      - respond: {status: completed, output: {text: done}}
`
	store := jobs.NewRegistry(jobs.Limits{})
	s := newSimWithJobs(t, src, store)

	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
	require.Equal(t, http.StatusOK, rec.Code, "create failed: %s", rec.Body.String())
	assert.GreaterOrEqual(t, rec.Body.Len(), 4096, "the response must actually be padded")

	var out struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), "padding is insignificant whitespace, so the body still decodes")
	require.NotEmpty(t, out.ID)

	assert.Equal(t, 1, store.StatsIn(provider.DefaultNamespace).Count,
		"the client holds an identifier, so exactly one job must back it")

	got := pollRun(t, s, out.ID)
	assert.Equal(t, statusCompleted, got["status"], "the identifier the padded create returned must resolve")
}

// An attempt carrying a body: replaces the response even below 400, so the client
// never receives the identifier the handler minted. Recording a job for it would
// leave a record nobody can poll and spend a slot against the namespace's bound.
func TestAgentRunCreateUnderABodyOverrideLeavesNoJob(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: exa-body-override-create
providers:
  exa_agent_runs:
    create:
      fault:
        attempts:
          - {status: 200, body: {id: agent_run_scripted, status: queued}}
    turns:
      - respond: {status: completed, output: {text: done}}
`
	store := jobs.NewRegistry(jobs.Limits{})
	s := newSimWithJobs(t, src, store)

	rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"id":"agent_run_scripted","status":"queued"}`, rec.Body.String(),
		"the scripted body replaces the rendered one")

	assert.Zero(t, store.StatsIn(provider.DefaultNamespace).Count,
		"no client holds the minted identifier, so no job may be recorded for it")

	// The scripted identifier was never minted either, so it 404s. The retry is the
	// next attempt in the plan — exhausted, so it succeeds — and its job resolves
	// like any other create's.
	missing := s.do(request{method: http.MethodGet, path: "/agent/runs/agent_run_scripted"})
	assert.Equal(t, http.StatusNotFound, missing.Code)

	id := createRun(t, s, `{"query":"retry"}`)
	assert.Equal(t, 1, store.StatsIn(provider.DefaultNamespace).Count)
	assert.Equal(t, statusCompleted, pollRun(t, s, id)["status"])
}
