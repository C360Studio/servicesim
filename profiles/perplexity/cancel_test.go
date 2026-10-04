package perplexity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/testkit"
)

// These tests are the behaviour of POST /v1/agent/{id}/cancel (cancelAgentResponse,
// issue #31). Every wait on the journal is testkit's bounded AwaitRequests, and
// the one concurrent test synchronises on a channel; nothing here sleeps.

// cancelScenario is a background run that is queued on its first retrieve, in
// progress on its second and completed from its third on, with a cancel script
// that is still in progress on the first retrieve after a cancel and cancelled
// from the next on. The cancel script's snapshots carry a usage the run's own
// do not, so a body says which script served it.
const cancelScenario = `
version: 1
name: background-cancel
time:
  base: 2026-01-01T00:00:00Z
providers:
  perplexity_agent:
    answer: A synchronous answer.
    background:
      turns:
        - when: {call_index: 0}
          respond: {status: queued}
        - when: {call_index: 1}
          respond: {status: in_progress}
        - respond: {status: completed, answer: Report A finds that deterministic simulation removes flakiness.}
      cancel:
        turns:
          - when: {call_index: 0}
            respond: {status: in_progress, usage: {input_tokens: 42, output_tokens: 7}}
          - respond: {status: cancelled, usage: {input_tokens: 42, output_tokens: 9}}
`

// cancelRoute is the cancel route's pattern, as the journal records it.
const cancelRoute = "POST /v1/agent/{id}/cancel"

// retrieveRoute is the retrieve route's pattern, as the journal records it.
const retrieveRoute = "GET /v1/agent/{id}"

// withCancelFault is cancelScenario with attempts as the cancel route's plan.
func withCancelFault(attempts string) string {
	return strings.Replace(cancelScenario, "      cancel:\n",
		"      cancel:\n        fault: {attempts: ["+attempts+"]}\n", 1)
}

// cancelPath is the cancel route's path for id.
func cancelPath(id string) string { return "/v1/agent/" + id + "/cancel" }

// bgCancel sends the request the specification declares: a POST with no body,
// and so no Content-Type either.
func bgCancel(t *testing.T, sim *testkit.Sim, base, id string) bgReply {
	t.Helper()
	return bgSend(t, sim, base, http.MethodPost, cancelPath(id), "", nil, false)
}

// trySend sends one request with the test credential and reports a transport
// failure as an error rather than failing the test, for a goroutine and for a
// test that expects the connection to drop.
func trySend(sim *testkit.Sim, url, method string) (bgReply, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return bgReply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := sim.Client().Do(req)
	if err != nil {
		return bgReply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return bgReply{status: resp.StatusCode, header: resp.Header, body: body}, err
}

// acknowledged requires the cancel's 200 acknowledgement for id: exactly the
// two properties the specification requires, in its order, and nothing else.
func acknowledged(t *testing.T, r bgReply, id string) {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
	assert.Equal(t, `{"response_id":"`+id+`","status":"cancelling"}`, string(r.body))
	assert.Equal(t, "application/json", r.header.Get("Content-Type"))
}

// refusedAsTerminal requires the 400 a cancel of a terminal response answers:
// the bytes of perplexity-agent-cancel-400.json, the Agent ErrorInfo envelope
// with the simulator's message. The 400 names no id, so one golden serves every
// scenario this helper is used under.
func refusedAsTerminal(t *testing.T, r bgReply) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, r.status, "body: %s", r.body)
	assert.Equal(t, "application/json", r.header.Get("Content-Type"))
	assert.Equal(t, string(goldenBytes(t, "perplexity-agent-cancel-400.json")), string(r.body))
}

// goldenCancelScenario is backgroundScenario with a cancel script. It keeps that
// scenario's name, so the job it mints is the one
// perplexity-agent-background-queued.json is the create of, and the 200 below can
// be compared with its golden byte for byte, id included.
const goldenCancelScenario = backgroundScenario + `      cancel:
        turns:
          - respond: {status: cancelled}
`

// TestCancelAcknowledgementIsTheGolden ties perplexity-agent-cancel-200.json to
// what the handler writes: the whole body, key order included, not a decoded
// comparison that a wrong key order or a stringified value would survive.
func TestCancelAcknowledgementIsTheGolden(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, goldenCancelScenario)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	var queued struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(goldenBytes(t, "perplexity-agent-background-queued.json"), &queued))
	require.Equal(t, queued.ID, id, "the scenario must mint the job the queued golden is the create of")

	r := bgCancel(t, sim, base, id)
	require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
	assert.Equal(t, "application/json", r.header.Get("Content-Type"))
	assert.Equal(t, string(goldenBytes(t, "perplexity-agent-cancel-200.json")), string(r.body))
}

// usageOf is the output_tokens a snapshot's usage reports, or -1 when it
// renders none: the run's own script scripts no usage, the cancel script does.
func usageOf(t *testing.T, r bgReply) float64 {
	t.Helper()
	usage, ok := r.json(t)["usage"].(map[string]any)
	if !ok {
		return -1
	}
	n, _ := usage["output_tokens"].(float64)
	return n
}

// labelsOf lists the journal labels of entries.
func labelsOf(entries []journal.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Outcome.Label
	}
	return out
}

// attemptsOf lists the attempt indices of entries.
func attemptsOf(entries []journal.Entry) []int {
	out := make([]int, len(entries))
	for i, e := range entries {
		out[i] = e.Outcome.AttemptIndex
	}
	return out
}

// onePath filters entries to one request path.
func onePath(entries []journal.Entry, path string) []journal.Entry {
	var out []journal.Entry
	for _, e := range entries {
		if e.Path == path {
			out = append(out, e)
		}
	}
	return out
}

// theJob returns the one job the sim holds.
func theJob(t *testing.T, sim *testkit.Sim) testkit.Job {
	t.Helper()
	all := sim.Jobs()
	require.Len(t, all, 1)
	return all[0]
}

// TestCancelRouteIsRegistered pins the cancel route: its own fault key, a lane
// per job, the Agent entry, bearer credentials and a plan selector — and it is
// neither a route an entry's own turns may name nor an entry-level cancel:.
func TestCancelRouteIsRegistered(t *testing.T) {
	t.Parallel()

	var cancel *provider.Route
	for _, r := range Routes() {
		if r.Pattern == cancelRoute {
			cancel = &r
		}
	}
	require.NotNil(t, cancel, "%s is not registered", cancelRoute)
	assert.Equal(t, "perplexity:agent.cancel", cancel.FaultKey)
	assert.Equal(t, NameAgent, cancel.Entry)
	assert.Equal(t, []string{provider.LaneFromPath + "id"}, cancel.LaneFrom)
	assert.Equal(t, []string{"authorization"}, cancel.Credentials)
	assert.NotNil(t, cancel.Fault)
	assert.Contains(t, handlers(), cancel.Pattern)

	for _, r := range (agentValidator{}).Routes() {
		assert.NotEqual(t, cancel.FaultKey, r.FaultKey, "the cancel key must not be a create turn's route")
	}
	assert.NotContains(t, Profile().Cancellable, NameAgent, "the cancel is background.cancel, not an entry-level cancel:")
}

// TestCancelOfARunningBackgroundRun is the route's headline case: the cancel is
// recorded at the run's position and acknowledged, and the retrieves after it
// walk background.cancel.turns from the cancel — its call_index counting
// retrieves since the cancel — while the journal's attempt index stays absolute.
func TestCancelOfARunningBackgroundRun(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, cancelScenario)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	assert.Equal(t, "queued", statusIn(t, bgRetrieve(t, sim, base, id)))
	acknowledged(t, bgCancel(t, sim, base, id), id)

	stopping := bgRetrieve(t, sim, base, id)
	assert.Equal(t, "in_progress", statusIn(t, stopping))
	assert.Equal(t, 7.0, usageOf(t, stopping), "the retrieve after the cancel is cancel.turns[0], not turns[1]")
	cancelled := bgRetrieve(t, sim, base, id)
	assert.Equal(t, "cancelled", statusIn(t, cancelled))
	assert.Equal(t, 9.0, usageOf(t, cancelled), "the usage accrued before the cancellation is the scripted usage")
	assert.Equal(t, id, cancelled.json(t)["id"])
	assert.Equal(t, string(cancelled.body), string(bgRetrieve(t, sim, base, id).body), "a cancelled run stays cancelled")

	job := theJob(t, sim)
	assert.True(t, job.CancelRequested)
	assert.Equal(t, 1, job.CancelAtPoll, "recorded at the position of the retrieve it was judged by")
	assert.Equal(t, 4, job.Polls)

	entries := sim.AwaitRequests(t, Name, 6)
	assert.Equal(t, []string{
		"perplexity.agent.background.created",
		"perplexity.agent.retrieved.queued",
		"perplexity.agent.cancel.accepted",
		"perplexity.agent.retrieved.in_progress",
		"perplexity.agent.retrieved.cancelled",
		"perplexity.agent.retrieved.cancelled",
	}, labelsOf(entries))

	cancels := onRoute(entries, cancelRoute)
	require.Len(t, cancels, 1)
	assert.Equal(t, "perplexity:agent.cancel|path:id="+id, cancels[0].Outcome.FaultKey, "every job has its own cancel lane")
	assert.Equal(t, 0, cancels[0].Outcome.AttemptIndex)
	assert.Equal(t, http.StatusOK, cancels[0].Outcome.Status)
	assert.Equal(t, journal.OutcomeScenario, cancels[0].Outcome.Kind)
	testkit.AssertNoFindings(t, cancels[0])

	assert.Equal(t, []int{0, 1, 2, 3}, attemptsOf(onRoute(entries, retrieveRoute)),
		"the retrieve lane's attempt index stays absolute across the cancel")
}

// TestCancelIsJudgedByTheRunsNextRetrieve walks both races. A cancel recorded
// while the run's next retrieve is pending wins: it is acknowledged and every
// later retrieve is the cancel script's. A cancel sent once the next retrieve
// would be terminal — whether or not the client has seen that retrieve yet —
// loses to completion: it is the 400, nothing is recorded, and the later
// retrieves are still the run's own.
func TestCancelIsJudgedByTheRunsNextRetrieve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		retrieves  int
		recorded   bool
		wantLabel  string
		wantStatus string // what the retrieve after the cancel serves
	}{
		{"create, cancel: the cancel wins", 0, true, "perplexity.agent.cancel.accepted", "in_progress"},
		{"create, retrieve, cancel: the cancel wins", 1, true, "perplexity.agent.cancel.accepted", "in_progress"},
		{"the run completed before the client saw it: completion wins", 2, false,
			"perplexity.agent.cancel.terminal", "completed"},
		{"the client saw the run complete: completion wins", 3, false,
			"perplexity.agent.cancel.terminal", "completed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sim := startBackground(t, cancelScenario)
			base := sim.URL(Name)
			id := bgCreate(t, sim, base)
			for range tc.retrieves {
				bgRetrieve(t, sim, base, id)
			}

			r := bgCancel(t, sim, base, id)
			if tc.recorded {
				acknowledged(t, r, id)
			} else {
				refusedAsTerminal(t, r)
			}

			next := bgRetrieve(t, sim, base, id)
			assert.Equal(t, tc.wantStatus, statusIn(t, next))
			job := theJob(t, sim)
			assert.Equal(t, tc.recorded, job.CancelRequested)
			if tc.recorded {
				assert.Equal(t, tc.retrieves, job.CancelAtPoll)
				assert.Equal(t, 7.0, usageOf(t, next), "the cancel script's first snapshot")
				assert.Equal(t, "cancelled", statusIn(t, bgRetrieve(t, sim, base, id)))
			} else {
				assert.Equal(t, -1.0, usageOf(t, next), "the run's own script")
				assert.Equal(t, string(next.body), string(bgRetrieve(t, sim, base, id).body), "later retrieves are unchanged")
			}

			cancels := onRoute(sim.AwaitRequests(t, Name, tc.retrieves+4), cancelRoute)
			require.Len(t, cancels, 1)
			assert.Equal(t, tc.wantLabel, cancels[0].Outcome.Label)
			assert.Equal(t, 0, cancels[0].Outcome.AttemptIndex, "a cancel claims its attempt whatever it decides")
			assert.Equal(t, journal.OutcomeScenario, cancels[0].Outcome.Kind, "the 400 is served, not a rejection")
			testkit.AssertNoFindings(t, cancels[0])
		})
	}
}

// TestEveryStatusIsJudgedByTheTerminalPredicate cancels a run whose next
// retrieve is each status in turn: queued and in_progress are pending, so the
// cancel is recorded and acknowledged; completed (written, or left absent),
// failed, incomplete and cancelled end the run, so the cancel is the 400 and
// records nothing.
func TestEveryStatusIsJudgedByTheTerminalPredicate(t *testing.T) {
	t.Parallel()

	const tmpl = `
version: 1
name: terminal-predicate
providers:
  perplexity_agent:
    answer: A synchronous answer.
    background:
      turns:
        - respond: {RESPOND}
      cancel:
        turns:
          - respond: {status: cancelled}
`
	tests := []struct {
		name, respond string
		recorded      bool
	}{
		{"queued", "status: queued", true},
		{"in_progress", "status: in_progress", true},
		{"completed", "status: completed", false},
		{"an absent status, which is completed", "answer: done", false},
		{"failed", "status: failed, error: {message: boom}", false},
		{"incomplete", "status: incomplete", false},
		{"cancelled", "status: cancelled", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sim := startBackground(t, strings.Replace(tmpl, "RESPOND", tc.respond, 1))
			base := sim.URL(Name)
			id := bgCreate(t, sim, base)

			r := bgCancel(t, sim, base, id)
			want := "perplexity.agent.cancel.terminal"
			if tc.recorded {
				acknowledged(t, r, id)
				want = "perplexity.agent.cancel.accepted"
			} else {
				refusedAsTerminal(t, r)
			}
			assert.Equal(t, tc.recorded, theJob(t, sim).CancelRequested)
			assert.Equal(t, want, sim.AwaitRequests(t, Name, 2)[1].Outcome.Label)
		})
	}
}

// TestARepeatedCancel records nothing new. It is acknowledged again while the
// cancel script's snapshot for the run's next retrieve is pending, and is the
// 400 once that snapshot is terminal — SIMULATOR-POLICY, the specification being
// silent on a second cancel. The marker stays where the first cancel put it.
func TestARepeatedCancel(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, cancelScenario)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	first := bgCancel(t, sim, base, id)
	acknowledged(t, first, id)
	repeat := bgCancel(t, sim, base, id)
	acknowledged(t, repeat, id)

	assert.Equal(t, "in_progress", statusIn(t, bgRetrieve(t, sim, base, id)), "cancel.turns[0]")
	refusedAsTerminal(t, bgCancel(t, sim, base, id))
	assert.Equal(t, "cancelled", statusIn(t, bgRetrieve(t, sim, base, id)), "cancel.turns[1]")

	job := theJob(t, sim)
	assert.True(t, job.CancelRequested)
	assert.Zero(t, job.CancelAtPoll, "one marker, where the first cancel put it")

	cancels := onRoute(sim.AwaitRequests(t, Name, 6), cancelRoute)
	assert.Equal(t, []string{
		"perplexity.agent.cancel.accepted", "perplexity.agent.cancel.repeated", "perplexity.agent.cancel.terminal",
	}, labelsOf(cancels))
	assert.Equal(t, []int{0, 1, 2}, attemptsOf(cancels), "every cancel claims exactly one attempt")
	for _, e := range cancels {
		testkit.AssertNoFindings(t, e)
	}
}

// TestARepeatedCancelOfAnImmediatelyCancelledRun: a cancel script whose first
// snapshot is terminal acknowledges the first cancel — the specification's 200
// is always cancelling — and a second cancel before any retrieve is the 400,
// because the run's next retrieve is now that terminal snapshot.
func TestARepeatedCancelOfAnImmediatelyCancelledRun(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, `
version: 1
name: cancelled-at-once
providers:
  perplexity_agent:
    answer: A synchronous answer.
    background:
      turns:
        - respond: {status: in_progress}
      cancel:
        turns:
          - respond: {status: cancelled}
`)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	acknowledged(t, bgCancel(t, sim, base, id), id)
	refusedAsTerminal(t, bgCancel(t, sim, base, id))
	assert.Equal(t, "cancelled", statusIn(t, bgRetrieve(t, sim, base, id)))
	assert.Equal(t, []string{"perplexity.agent.cancel.accepted", "perplexity.agent.cancel.terminal"},
		labelsOf(onRoute(sim.AwaitRequests(t, Name, 4), cancelRoute)))
}

// TestACancelRacingARetrieveResolvesOneWayOrTheOther sends a retrieve and a
// cancel at the same moment, at the position where the retrieve decides the
// race: if the cancel lands first it is recorded and the racing retrieve serves
// the cancel script; if the retrieve lands first the cancel sees the completed
// run next, is the 400, and records nothing. Either is correct; a mix of the two
// is the bug.
func TestACancelRacingARetrieveResolvesOneWayOrTheOther(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, cancelScenario)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)
	assert.Equal(t, "queued", statusIn(t, bgRetrieve(t, sim, base, id)))

	start := make(chan struct{})
	var wg sync.WaitGroup
	var retrieved, cancelled bgReply
	var retrieveErr, cancelErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		retrieved, retrieveErr = trySend(sim, base+"/v1/agent/"+id, http.MethodGet)
	}()
	go func() {
		defer wg.Done()
		<-start
		cancelled, cancelErr = trySend(sim, base+cancelPath(id), http.MethodPost)
	}()
	close(start)
	wg.Wait()
	require.NoError(t, retrieveErr)
	require.NoError(t, cancelErr)

	assert.Equal(t, "in_progress", statusIn(t, retrieved))
	after := bgRetrieve(t, sim, base, id)
	if job := theJob(t, sim); job.CancelRequested {
		assert.Equal(t, 1, job.CancelAtPoll, "cancel before retrieve: recorded at the racing retrieve's position")
		acknowledged(t, cancelled, id)
		assert.Equal(t, 7.0, usageOf(t, retrieved), "the racing retrieve served the cancel script")
		assert.Equal(t, "cancelled", statusIn(t, after))
	} else {
		refusedAsTerminal(t, cancelled)
		assert.Equal(t, -1.0, usageOf(t, retrieved), "retrieve before cancel: the run's own script")
		assert.Equal(t, "completed", statusIn(t, after), "completion wins")
	}
}

// TestACancelWhoseAttemptDoesNotCommitRecordsNothing is a scripted 500 that a
// vendor never acted on: nothing is recorded, a retrieve in between carries on
// from the run's own script, and the retry draws the next attempt and is the
// cancel that takes effect.
func TestACancelWhoseAttemptDoesNotCommitRecordsNothing(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, withCancelFault(`{status: 500}, {}`))
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	failed := bgCancel(t, sim, base, id)
	require.Equal(t, http.StatusInternalServerError, failed.status)
	assert.Equal(t, string(goldenBytes(t, "perplexity-agent-500.json")), string(failed.body))
	assert.False(t, theJob(t, sim).CancelRequested, "an attempt that does not commit records nothing")

	queued := bgRetrieve(t, sim, base, id)
	assert.Equal(t, "queued", statusIn(t, queued))
	assert.Equal(t, -1.0, usageOf(t, queued), "served from the run's own script")

	acknowledged(t, bgCancel(t, sim, base, id), id)
	job := theJob(t, sim)
	assert.True(t, job.CancelRequested)
	assert.Equal(t, 1, job.CancelAtPoll)

	entries := sim.AwaitRequests(t, Name, 4)
	cancels := onRoute(entries, cancelRoute)
	assert.Equal(t, []string{"perplexity.agent.cancel.unrecorded", "perplexity.agent.cancel.accepted"}, labelsOf(cancels))
	assert.Equal(t, []int{0, 1}, attemptsOf(cancels))
	assert.Equal(t, http.StatusInternalServerError, cancels[0].Outcome.Status)
	assert.Equal(t, journal.OutcomeFault, cancels[0].Outcome.Kind)
	for _, e := range cancels {
		testkit.AssertNoFindings(t, e)
	}
	retrieves := onRoute(entries, retrieveRoute)
	require.Len(t, retrieves, 1)
	assert.Equal(t, 0, retrieves[0].Outcome.AttemptIndex, "the failed cancel spent nothing of the retrieve's budget")
}

// TestAnAcceptedCancelTakesEffectAndLosesItsReply is the cancel twin of the
// accepted create: the client sees an error or a dead connection, and the run is
// cancelled anyway, which its next retrieve and the job record both show.
func TestAnAcceptedCancelTakesEffectAndLosesItsReply(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, attempt string
		status        int // 0: the client sees no response at all
	}{
		{"a 500 the vendor acted on", `{status: 500, accepted: true}`, http.StatusInternalServerError},
		{"a connection closed before the headers", `{kind: close_before_headers, accepted: true}`, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sim := startBackground(t, withCancelFault(tc.attempt))
			base := sim.URL(Name)
			id := bgCreate(t, sim, base)

			got, err := trySend(sim, base+cancelPath(id), http.MethodPost)
			if tc.status == 0 {
				require.Error(t, err, "the client must see a connection failure, got status %d", got.status)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.status, got.status)
				assert.NotContains(t, string(got.body), "cancelling", "the reply was lost")
			}

			cancels := onRoute(sim.AwaitRequests(t, Name, 2), cancelRoute)
			require.Len(t, cancels, 1)
			assert.Equal(t, "perplexity.agent.cancel.accepted", cancels[0].Outcome.Label)
			testkit.AssertNoErrors(t, cancels[0])

			job := theJob(t, sim)
			assert.True(t, job.CancelRequested, "the cancel took effect although its reply was lost")
			assert.Zero(t, job.CancelAtPoll)
			assert.Equal(t, 7.0, usageOf(t, bgRetrieve(t, sim, base, id)), "the next retrieve is the cancel script's")
		})
	}
}

// TestACancelTheScenarioCannotAnswerIsTheVendors500: with no background.cancel
// script the cancel would take effect but nothing could answer the retrieves
// after it, so it is the vendor's 500 with an error finding, nothing is
// recorded, and the retrieves carry on from the run's own script.
func TestACancelTheScenarioCannotAnswerIsTheVendors500(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, backgroundScenario)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	for range 2 {
		r := bgCancel(t, sim, base, id)
		require.Equal(t, http.StatusInternalServerError, r.status, "body: %s", r.body)
		assert.Equal(t, string(goldenBytes(t, "perplexity-agent-500.json")), string(r.body))
	}
	assert.False(t, theJob(t, sim).CancelRequested, "nothing is recorded")
	assert.Equal(t, "queued", statusIn(t, bgRetrieve(t, sim, base, id)), "the retrieves carry on from the run's own script")

	cancels := onRoute(sim.AwaitRequests(t, Name, 4), cancelRoute)
	require.Len(t, cancels, 2)
	assert.Equal(t, []int{0, 1}, attemptsOf(cancels), "the claimed attempt stays spent")
	for _, e := range cancels {
		assert.Equal(t, "perplexity.agent.error.500", e.Outcome.Label)
		codes := map[string]journal.Severity{}
		for _, f := range e.Findings {
			codes[f.Code] = f.Severity
			if f.Code == provider.CodeJobCancelUnscripted {
				assert.Contains(t, f.Message, "providers."+NameAgent+".background scripts no cancel.turns",
					"the finding names the block whose script could not answer")
			}
		}
		assert.Equal(t, map[string]journal.Severity{
			provider.CodeJobCancelUnscripted: journal.SeverityError,
			provider.CodeAttemptOnRejection:  journal.SeverityWarning,
		}, codes, "the finding says why; the stripped attempt is reported, as U3 documents")
	}
}

// TestAnAlreadyTerminalCancelNeedsNoCancelBlock: completion wins without a
// cancel script to consult, so a scenario that scripts none still answers a
// cancel of a completed run with the 400, cleanly.
func TestAnAlreadyTerminalCancelNeedsNoCancelBlock(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, backgroundScenario)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)
	for range 2 {
		bgRetrieve(t, sim, base, id)
	}

	refusedAsTerminal(t, bgCancel(t, sim, base, id))
	assert.False(t, theJob(t, sim).CancelRequested)
	cancels := onRoute(sim.AwaitRequests(t, Name, 4), cancelRoute)
	require.Len(t, cancels, 1)
	assert.Equal(t, "perplexity.agent.cancel.terminal", cancels[0].Outcome.Label)
	testkit.AssertNoFindings(t, cancels[0])
}

// TestACancelOfAJobThisNamespaceDoesNotHoldClaimsNothing: an unknown id, one
// minted in another namespace, and a malformed one are each the Agent 404 a
// retrieve of them answers, claim no cancel attempt, and leave every real job
// untouched.
func TestACancelOfAJobThisNamespaceDoesNotHoldClaimsNothing(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, cancelScenario)
	home := sim.Namespace(t, "home").URL(Name)
	away := sim.Namespace(t, "away").URL(Name)
	id := bgCreate(t, sim, home)
	malformed := "resp%2F" + strings.TrimPrefix(id, "resp_")

	tests := []struct{ name, base, id string }{
		{"an id never minted", home, "resp_00000000000000000000000000000000"},
		{"an id minted in another namespace", away, id},
		// Through the default namespace, as the retrieve's own test sends it:
		// the /n/<namespace> prefix is stripped from the decoded path, so under
		// a prefix an escaped slash is a path separator and the mux fails the
		// request closed before any route sees it.
		{"a malformed id", sim.URL(Name), malformed},
	}
	for _, tc := range tests {
		r := bgCancel(t, sim, tc.base, tc.id)
		assert.Equal(t, http.StatusNotFound, r.status, "%s: %s", tc.name, r.body)
		assert.Equal(t, string(goldenBytes(t, "perplexity-agent-retrieve-404.json")), string(r.body), tc.name)
	}

	job := theJob(t, sim)
	assert.False(t, job.CancelRequested)
	assert.Zero(t, job.Polls)
	assert.Equal(t, "queued", statusIn(t, bgRetrieve(t, sim, home, id)), "the job's first retrieve is still turns[0]")

	entries := sim.AwaitRequests(t, Name, 2+len(tests))
	cancels := onRoute(entries, cancelRoute)
	require.Len(t, cancels, len(tests))
	for i, e := range cancels {
		assert.Equal(t, -1, e.Outcome.AttemptIndex, "%s claimed an attempt", tests[i].name)
		assert.Equal(t, "perplexity.agent.error.404", e.Outcome.Label, tests[i].name)
	}
}

// TestACancelWithoutTheRightCredentialClaimsNothing: a missing or wrong
// credential is the Agent 401, ahead of resolution, claims nothing, records
// nothing, and the value presented — in a header, the query string or URL
// userinfo — reaches neither the response nor the journal.
func TestACancelWithoutTheRightCredentialClaimsNothing(t *testing.T) {
	t.Parallel()

	const sentinel = "pplx-SENTINEL-cancel"
	src := strings.Replace(cancelScenario, "  perplexity_agent:\n",
		"  perplexity_agent:\n    auth: {expect_key: the-expected-key}\n", 1)
	sim := startBackground(t, src)
	base := sim.URL(Name)

	created := bgSend(t, sim, base, http.MethodPost, "/v1/agent", backgroundRequest,
		map[string]string{"Authorization": "Bearer the-expected-key"}, true)
	require.Equal(t, http.StatusOK, created.status, "body: %s", created.body)
	id, _ := created.json(t)["id"].(string)

	userinfo := strings.Replace(base, "http://", "http://user:"+sentinel+"@", 1)
	tests := []struct {
		name, base, path string
		header           map[string]string
	}{
		{"no credential", base, cancelPath(id), nil},
		{"no credential, an id never minted", base, cancelPath("resp_00000000000000000000000000000000"), nil},
		{"a wrong bearer token", base, cancelPath(id), map[string]string{"Authorization": "Bearer " + sentinel}},
		{"a key in the query string", base, cancelPath(id) + "?api_key=" + sentinel, nil},
		{"a key in URL userinfo", userinfo, cancelPath(id), nil},
		{"a key in a JSON body", base, cancelPath(id), nil},
	}
	for _, tc := range tests {
		body := ""
		if tc.name == "a key in a JSON body" {
			body = `{"api_key":"` + sentinel + `"}`
		}
		r := bgSend(t, sim, tc.base, http.MethodPost, tc.path, body, tc.header, true)
		assert.Equal(t, http.StatusUnauthorized, r.status, "%s: %s", tc.name, r.body)
		assert.Equal(t, string(goldenBytes(t, "perplexity-agent-401.json")), string(r.body), tc.name)
	}

	assert.False(t, theJob(t, sim).CancelRequested)
	entries := sim.AwaitRequests(t, Name, 1+len(tests))[1:]
	for i, e := range entries {
		assert.Equal(t, -1, e.Outcome.AttemptIndex, "%s claims nothing", tests[i].name)
	}
	testkit.AssertNoCredentialLeak(t, sim, sentinel)
}

// inProcess serves the profile in process rather than over a socket, for what
// a real-socket Sim cannot reach: a job store that misbehaves on cue, and the
// log lines a Sim's logger discards.
type inProcess struct {
	t       *testing.T
	handler http.Handler
	ring    *journal.Ring
}

// serveInProcess builds the handler over src with store as its job store and
// every log line, at every level, written to logs.
func serveInProcess(t *testing.T, src string, store jobs.Store, logs io.Writer) inProcess {
	t.Helper()
	s := mustScenario(t, src)
	ring := journal.NewRing(64, 1<<16)
	return inProcess{t: t, ring: ring, handler: Profile().Handler(provider.Deps{
		Scenario:  s,
		Journal:   ring,
		Faults:    provider.MustSet(Profile()).Faults(s),
		DelayMode: provider.DelaySkip,
		Jobs:      store,
		Logger:    slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})}
}

// do sends one request with exactly the headers given. ServeHTTP has journaled
// it by the time it returns.
func (p inProcess) do(method, target string, header map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	p.handler.ServeHTTP(rec, req)
	return rec
}

// create mints a background run with the test credential and returns its id.
func (p inProcess) create() string {
	p.t.Helper()
	rec := p.do(http.MethodPost, "/v1/agent",
		map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + testKey}, backgroundRequest)
	require.Equal(p.t, http.StatusOK, rec.Code, rec.Body.String())
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(p.t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(p.t, created.ID)
	return created.ID
}

// entryFor returns the one journal entry recorded for path.
func (p inProcess) entryFor(path string) journal.Entry {
	p.t.Helper()
	entries := onePath(p.ring.Snapshot(), path)
	require.Len(p.t, entries, 1, path)
	return entries[0]
}

// vanishingStore is a job store in which a reset lands right after every lookup
// that finds a job: the window between a cancel resolving its run and deciding
// about it, which no real-socket test can hold open.
type vanishingStore struct{ *jobs.Registry }

func (s vanishingStore) Lookup(namespace, id string) (jobs.Job, bool) {
	job, found := s.Registry.Lookup(namespace, id)
	if found {
		s.ResetIn(namespace)
	}
	return job, found
}

// TestACancelWhoseJobVanishesAfterItResolvedIsServed: a cancel that resolved its
// job has claimed its attempt, so when a reset removes the job before the
// cancel is decided, the 404 it answers is a served response to that attempt —
// a scripted fault on the attempt applies to it, and nothing reports the
// attempt as stripped. A cancel of an id that never resolved claims nothing.
func TestACancelWhoseJobVanishesAfterItResolvedIsServed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		src    string
		status int
		golden string
		kind   journal.OutcomeKind
	}{
		{"the Agent 404, served", cancelScenario, http.StatusNotFound, "perplexity-agent-retrieve-404.json",
			journal.OutcomeScenario},
		{"a scripted fault on the claimed attempt", withCancelFault(`{status: 500}`),
			http.StatusInternalServerError, "perplexity-agent-500.json", journal.OutcomeFault},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := serveInProcess(t, tc.src, vanishingStore{jobs.NewRegistry(jobs.Limits{})}, io.Discard)
			auth := map[string]string{"Authorization": "Bearer " + testKey}
			never := p.do(http.MethodPost, cancelPath("resp_00000000000000000000000000000000"), auth, "")
			require.Equal(t, http.StatusNotFound, never.Code, never.Body.String())
			assert.Equal(t, string(goldenBytes(t, "perplexity-agent-retrieve-404.json")), never.Body.String())

			path := cancelPath(p.create())
			rec := p.do(http.MethodPost, path, auth, "")
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, string(goldenBytes(t, tc.golden)), rec.Body.String(),
				"the served answer is the same bytes as every other Agent %d", tc.status)

			e := p.entryFor(path)
			assert.Equal(t, 0, e.Outcome.AttemptIndex, "the cancel claimed its attempt")
			assert.Equal(t, tc.kind, e.Outcome.Kind)
			assert.NotContains(t, codesOf(e), provider.CodeAttemptOnRejection, "a served response keeps its attempt")
			assert.NotContains(t, codesOf(e), provider.CodeJobCancelUnscripted)

			assert.Equal(t, -1, p.entryFor(cancelPath("resp_00000000000000000000000000000000")).Outcome.AttemptIndex,
				"an id that never resolved claims nothing")
		})
	}
}

// contendedStore is a job store in which a retrieve always lands between a
// cancel reading the job's position and recording against it.
type contendedStore struct{ *jobs.Registry }

func (s contendedStore) MarkCancel(namespace, id string, _ int) (jobs.Job, jobs.MarkOutcome) {
	job, _ := s.Lookup(namespace, id)
	return job, jobs.PositionMoved
}

// TestACancelThatCannotBeRecordedForContentionIsTheVendors500: when the job's
// position never holds still long enough to record the cancel, the client gets
// the 500 with the fixed message, the finding that says why stays in the
// journal, and nothing is recorded.
func TestACancelThatCannotBeRecordedForContentionIsTheVendors500(t *testing.T) {
	t.Parallel()

	store := contendedStore{jobs.NewRegistry(jobs.Limits{})}
	p := serveInProcess(t, cancelScenario, store, io.Discard)
	id := p.create()
	path := cancelPath(id)

	rec := p.do(http.MethodPost, path, map[string]string{"Authorization": "Bearer " + testKey}, "")
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Equal(t, string(goldenBytes(t, "perplexity-agent-500.json")), rec.Body.String(),
		"the wire carries the fixed message, never the finding's")

	e := p.entryFor(path)
	assert.Contains(t, codesOf(e), provider.CodeJobCancelContended)
	job, found := store.Lookup(provider.DefaultNamespace, id)
	require.True(t, found)
	assert.False(t, job.CancelRequested, "nothing is recorded")
}

// TestACancelCredentialNeverReachesALogLine is the log half of the credential
// rule, which a real-socket Sim cannot observe: its logger discards. Every line
// the request path writes is captured here and searched.
func TestACancelCredentialNeverReachesALogLine(t *testing.T) {
	t.Parallel()

	const sentinel = "pplx-SENTINEL-log"
	src := strings.Replace(cancelScenario, "  perplexity_agent:\n",
		"  perplexity_agent:\n    auth: {expect_key: "+testKey+"}\n", 1)
	var logs bytes.Buffer
	p := serveInProcess(t, src, jobs.NewRegistry(jobs.Limits{}), &logs)

	path := cancelPath(p.create())
	for _, rec := range []*httptest.ResponseRecorder{
		p.do(http.MethodPost, path, map[string]string{"Authorization": "Bearer " + sentinel}, ""),
		p.do(http.MethodPost, path+"?api_key="+sentinel, nil, ""),
		p.do(http.MethodPost, path, map[string]string{"Authorization": "Bearer " + sentinel, "Content-Type": "application/json"},
			`{"api_key":"`+sentinel+`"}`),
	} {
		require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
		assert.NotContains(t, rec.Body.String(), sentinel)
	}

	require.NotEmpty(t, logs.String(), "the request path must have logged, or this proves nothing")
	assert.NotContains(t, logs.String(), sentinel, "a log line carried the credential")
	for _, e := range p.ring.Snapshot() {
		encoded, err := json.Marshal(e)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), sentinel, "journal entry %d carried the credential", e.Seq)
	}
}

// TestCancellingOneJobLeavesEveryOtherAlone: the create, retrieve and cancel
// routes each draw on their own budget, and each job has its own retrieve and
// cancel lanes, so cancelling one run neither advances nor marks another, and no
// retry on one route spends another's attempts.
func TestCancellingOneJobLeavesEveryOtherAlone(t *testing.T) {
	t.Parallel()

	src := strings.Replace(withCancelFault(`{status: 500}, {}`), "    answer: A synchronous answer.\n",
		"    fault: {attempts: [{status: 429}, {}]}\n    answer: A synchronous answer.\n", 1)
	src = strings.Replace(src, "        - when: {call_index: 0}\n          respond: {status: queued}\n",
		"        - when: {call_index: 0}\n          respond: {status: queued}\n"+
			"          fault: {attempts: [{status: 503}, {}]}\n", 1)
	sim := startBackground(t, src)
	base := sim.URL(Name)

	limited := bgSend(t, sim, base, http.MethodPost, "/v1/agent", backgroundRequest, nil, false)
	require.Equal(t, http.StatusTooManyRequests, limited.status, "create attempt 0 is the scripted 429")
	a := bgCreate(t, sim, base)
	b := bgCreate(t, sim, base)

	require.Equal(t, http.StatusInternalServerError, bgCancel(t, sim, base, a).status, "a's cancel attempt 0")
	require.Equal(t, http.StatusServiceUnavailable, bgRetrieve(t, sim, base, a).status, "a's retrieve attempt 0")
	acknowledged(t, bgCancel(t, sim, base, a), a)
	assert.Equal(t, 7.0, usageOf(t, bgRetrieve(t, sim, base, a)), "a's retrieve after its cancel")

	byID := map[string]testkit.Job{}
	for _, j := range sim.Jobs() {
		byID[j.ID] = j
	}
	assert.True(t, byID[a].CancelRequested)
	assert.Equal(t, 1, byID[a].CancelAtPoll)
	assert.False(t, byID[b].CancelRequested, "cancelling a marks nothing on b")
	assert.Zero(t, byID[b].Polls, "cancelling a advances nothing on b")

	require.Equal(t, http.StatusInternalServerError, bgCancel(t, sim, base, b).status,
		"b has its own cancel budget, starting at attempt 0")
	assert.Equal(t, http.StatusServiceUnavailable, bgRetrieve(t, sim, base, b).status,
		"b has its own retrieve budget, starting at attempt 0")

	entries := sim.AwaitRequests(t, Name, 9)
	assert.Equal(t, []int{0, 1, 2}, attemptsOf(onePath(entries, "/v1/agent")), "the create budget")
	assert.Equal(t, []int{0, 1}, attemptsOf(onePath(entries, "/v1/agent/"+a)), "a's retrieve budget")
	assert.Equal(t, []int{0, 1}, attemptsOf(onePath(entries, cancelPath(a))), "a's cancel budget")
	assert.Equal(t, []int{0}, attemptsOf(onePath(entries, cancelPath(b))), "b's cancel budget")
}

// TestAClientThatAbandonsARetrieveIsNotACancel: a client that closes its own
// connection mid-retrieve leaves no cancel-route entry and no marker — the
// journal tells a hang-up from a vendor cancel request. The retrieve it
// abandoned still spent its position, and the journal shows the abort.
func TestAClientThatAbandonsARetrieveIsNotACancel(t *testing.T) {
	t.Parallel()

	src := strings.Replace(cancelScenario, "        - when: {call_index: 0}\n          respond: {status: queued}\n",
		"        - when: {call_index: 0}\n          respond: {status: queued}\n"+
			"          fault: {attempts: [{delay_after_headers: 1h}]}\n", 1)
	// Real delays: the retrieve must still be in flight when the client leaves.
	sim := testkit.Start(t, testkit.WithProfiles(Profile()), testkit.WithScenarioYAML(src))
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	ctx, leave := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/agent/"+id, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := sim.Client().Do(req)
	require.NoError(t, err, "the retrieve's headers arrive before the hold")
	leave()
	_ = resp.Body.Close()

	entries := sim.AwaitRequests(t, Name, 2)
	assert.Empty(t, onRoute(entries, cancelRoute), "no cancel-route entry")
	abandoned := onRoute(entries, retrieveRoute)
	require.Len(t, abandoned, 1)
	assert.True(t, abandoned[0].Outcome.Aborted, "the journal shows the retrieve was cut short")

	job := theJob(t, sim)
	assert.False(t, job.CancelRequested, "a hang-up is not a cancel")
	assert.Equal(t, 1, job.Polls, "the abandoned retrieve spent its position")
}

// TestTheCancelLifecycleIsDeterministic replays one sequence on two fresh sims:
// every status and body is byte-identical, and each is what that call answers —
// so two sims that failed alike cannot pass as deterministic.
func TestTheCancelLifecycleIsDeterministic(t *testing.T) {
	t.Parallel()

	type call struct {
		status int
		body   string
	}
	replay := func() []call {
		sim := startBackground(t, cancelScenario)
		base := sim.URL(Name)
		id := bgCreate(t, sim, base)
		var out []call
		for _, r := range []bgReply{
			bgRetrieve(t, sim, base, id),
			bgCancel(t, sim, base, id),
			bgCancel(t, sim, base, id),
			bgRetrieve(t, sim, base, id),
			bgRetrieve(t, sim, base, id),
			bgCancel(t, sim, base, id),
		} {
			out = append(out, call{r.status, string(r.body)})
		}
		return out
	}
	first, second := replay(), replay()
	assert.Equal(t, first, second)

	want := []struct {
		status int
		has    string
	}{
		{http.StatusOK, `"status":"queued"`},
		{http.StatusOK, `"status":"cancelling"`},
		{http.StatusOK, `"status":"cancelling"`},
		{http.StatusOK, `"status":"in_progress"`},
		{http.StatusOK, `"status":"cancelled"`},
		{http.StatusBadRequest, string(goldenBytes(t, "perplexity-agent-cancel-400.json"))},
	}
	require.Len(t, first, len(want))
	for i, w := range want {
		assert.Equal(t, w.status, first[i].status, "call %d: %s", i, first[i].body)
		assert.Contains(t, first[i].body, w.has, "call %d", i)
	}
}

// TestTheCancelPathRefusesEveryOtherMethod: the mux owns the cancel path's other
// methods, fail-closed, before any handler can claim or record anything.
func TestTheCancelPathRefusesEveryOtherMethod(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, cancelScenario)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	methods := []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete}
	for _, method := range methods {
		r := bgSend(t, sim, base, method, cancelPath(id), "", nil, false)
		assert.Equal(t, http.StatusMethodNotAllowed, r.status, method)
		assert.Equal(t, http.MethodPost, r.header.Get("Allow"), method)
		if method != http.MethodHead {
			assert.Equal(t, string(goldenBytes(t, "perplexity-405.json")), string(r.body), method)
		}
	}
	job := theJob(t, sim)
	assert.False(t, job.CancelRequested)
	assert.Zero(t, job.Polls)
	for _, e := range sim.AwaitRequests(t, Name, 1+len(methods))[1:] {
		assert.Equal(t, -1, e.Outcome.AttemptIndex, "%s claimed an attempt", e.Method)
	}
}

// TestACancelReadsNoBody: the specification declares no request body, so a bare
// POST — no body, no Content-Type, what `curl -X POST` sends — is the cancel,
// and a JSON object is accepted and ignored: both answer the same bytes. A body
// that is not a JSON object is refused by the shared request lifecycle, as on
// every route, before anything is claimed or recorded.
func TestACancelReadsNoBody(t *testing.T) {
	t.Parallel()

	sims := [2]*testkit.Sim{startBackground(t, cancelScenario), startBackground(t, cancelScenario)}
	bareBase, bodyBase := sims[0].URL(Name), sims[1].URL(Name)
	bareID, bodyID := bgCreate(t, sims[0], bareBase), bgCreate(t, sims[1], bodyBase)
	require.Equal(t, bareID, bodyID)

	for _, refused := range []string{`{not json`, `["reason"]`} {
		r := bgSend(t, sims[0], bareBase, http.MethodPost, cancelPath(bareID), refused, nil, false)
		assert.Equal(t, http.StatusBadRequest, r.status, "%s: %s", refused, r.body)
		assert.Contains(t, string(r.body), `"message":"validation failed: request body`, refused)
	}
	assert.False(t, theJob(t, sims[0]).CancelRequested, "a refused body records nothing")

	bare := bgCancel(t, sims[0], bareBase, bareID)
	acknowledged(t, bare, bareID)
	withBody := bgSend(t, sims[1], bodyBase, http.MethodPost, cancelPath(bodyID),
		`{"reason":"user asked","model":"not-a-model"}`, nil, false)
	assert.Equal(t, string(bare.body), string(withBody.body))

	cancels := onRoute(sims[0].AwaitRequests(t, Name, 4), cancelRoute)
	assert.Equal(t, []int{-1, -1, 0}, attemptsOf(cancels), "a refused body claims nothing")
	testkit.AssertNoFindings(t, cancels[2])
}

// bgCancelEntry is bgEntry with a cancel block, indented under background:.
func bgCancelEntry(turns, cancel string) string {
	return bgEntry(turns) + "      cancel:\n" + cancel
}

// TestBackgroundCancelValidator walks what readiness rejects or warns about in
// a background cancel script: every per-snapshot check a background turn gets,
// the serve-order and last-turn checks on the script on its own, and a route
// check against the retrieve alone — each addressed at the cancel turn.
func TestBackgroundCancelValidator(t *testing.T) {
	t.Parallel()

	const base = "providers.perplexity_agent.background.cancel.turns"
	const queued = "        - respond: {status: queued}\n"
	tests := []struct {
		name     string
		cancel   string
		code     string
		severity scenario.Severity
		path     string
	}{
		{"response_id would contradict the job id",
			"        turns:\n          - respond: {status: cancelled, response_id: resp_x}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.response_id"},
		{"extra_fields would contradict the scripted status",
			"        turns:\n          - respond: {status: in_progress, extra_fields: {status: cancelled}}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.extra_fields.status"},
		{"a terminal snapshot served before a pending one",
			"        turns:\n          - when: {call_index: 0}\n            respond: {status: cancelled}\n" +
				"          - respond: {status: in_progress}\n",
			codeAgentBackgroundTerminalThenPending, scenario.SeverityError, base + "[1].respond.status"},
		{"a script whose last turn is conditional runs out",
			"        turns:\n          - when: {call_index: 0}\n            respond: {status: cancelled}\n",
			codeAgentBackgroundScriptExhausted, scenario.SeverityWarning, base + "[0].when"},
		{"the cancel route never selects a snapshot",
			"        turns:\n          - when: {route: agent.cancel}\n            respond: {status: in_progress}\n" +
				"          - respond: {status: cancelled}\n",
			provider.CodeTurnRouteUnknown, scenario.SeverityError, base + "[0].when.route"},
		{"the cancel route's full key never selects a snapshot",
			"        turns:\n          - when: {route: \"perplexity:agent.cancel\"}\n            respond: {status: in_progress}\n" +
				"          - respond: {status: cancelled}\n",
			provider.CodeTurnRouteUnknown, scenario.SeverityError, base + "[0].when.route"},
		{"a create route never selects a snapshot",
			"        turns:\n          - when: {route: \"perplexity:agent\"}\n            respond: {status: in_progress}\n" +
				"          - respond: {status: cancelled}\n",
			provider.CodeTurnRouteUnknown, scenario.SeverityError, base + "[0].when.route"},
		{"a body predicate never matches",
			"        turns:\n          - when: {body_contains: x}\n            respond: {status: in_progress}\n" +
				"          - respond: {status: cancelled}\n",
			codeAgentBackgroundBodyPredicate, scenario.SeverityWarning, base + "[0].when"},
		{"a status outside the enum",
			"        turns:\n          - respond: {status: cancelling}\n",
			"perplexity.agent.status.invalid", scenario.SeverityError, base + "[0].respond.status"},
		{"a failed snapshot needs its error",
			"        turns:\n          - respond: {status: failed}\n",
			"perplexity.agent.error.missing", scenario.SeverityError, base + "[0].respond.error"},
		{"an unresolved source",
			"        turns:\n          - respond: {status: cancelled, search_results: [source-z]}\n",
			"scenario.source.unknown", scenario.SeverityError, base + "[0].respond.search_results"},
		{"a key the projection does not have",
			"        turns:\n          - respond: {status: cancelled, colour: blue}\n",
			CodeProjectionInvalid, scenario.SeverityError, base + "[0].respond"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := bgValidate(t, bgCancelEntry(queued, tc.cancel))
			var found *scenario.Finding
			for i := range findings {
				if findings[i].Code == tc.code && strings.HasPrefix(findings[i].Path, tc.path) {
					found = &findings[i]
				}
			}
			require.NotNil(t, found, "want %s at %s; got %+v", tc.code, tc.path, findings)
			assert.Equal(t, tc.severity, found.Severity)
			assert.NotEmpty(t, found.Message)
		})
	}

	// The cancel script's findings say what is different about it: its
	// call_index counts retrieves since the cancel, and where it runs out a
	// repeated cancel fails as well as the retrieve.
	t.Run("the cancel script's findings speak of the cancel", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			cancel, code, says string
		}{
			{"        turns:\n          - when: {call_index: 0}\n            respond: {status: cancelled}\n" +
				"          - respond: {status: in_progress}\n",
				codeAgentBackgroundTerminalThenPending,
				"retrieve 1 since the cancel is served turn 1 (in_progress) after retrieve 0 since the cancel was " +
					"served turn 0 (cancelled), which is terminal"},
			{"        turns:\n          - when: {call_index: 0}\n            respond: {status: cancelled}\n",
				codeAgentBackgroundScriptExhausted,
				"the last turn of background.cancel.turns has a condition a retrieve can fail, so the retrieve after " +
					"it matches no turn and answers 404 for a job that exists, and a repeated cancel there answers 500"},
		}
		for _, tc := range tests {
			var messages []string
			for _, f := range bgValidate(t, bgCancelEntry(queued, tc.cancel)) {
				if f.Code == tc.code {
					messages = append(messages, f.Message)
				}
			}
			require.Len(t, messages, 1, tc.code)
			assert.Contains(t, messages[0], tc.says, tc.code)
		}
	})

	t.Run("a well-formed cancel script loads clean", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, bgValidate(t, cancelScenario))
	})

	t.Run("completed may follow a cancel, and the script may be empty", func(t *testing.T) {
		t.Parallel()
		for _, cancel := range []string{
			"        turns:\n          - when: {call_index: 0}\n            respond: {status: in_progress}\n" +
				"          - respond: {status: completed, answer: done anyway}\n",
			"        fault: {attempts: [{status: 500}]}\n",
			"        turns: []\n",
		} {
			assert.Empty(t, bgValidate(t, bgCancelEntry(queued, cancel)), cancel)
		}
	})

	t.Run("the retrieve route may be named, in either spelling", func(t *testing.T) {
		t.Parallel()
		for _, route := range []string{"perplexity:agent.retrieve", "agent.retrieve"} {
			assert.Empty(t, bgValidate(t, bgCancelEntry(queued,
				"        turns:\n          - when: {route: \""+route+"\", call_index: 0}\n"+
					"            respond: {status: in_progress}\n          - respond: {status: cancelled}\n")), route)
		}
	})

	// The cancel peeks the snapshot the run's next RETRIEVE serves, so a run's
	// own background turn naming the cancel route could never fire either.
	t.Run("a background turn may not name the cancel route", func(t *testing.T) {
		t.Parallel()
		findings := bgValidate(t, bgEntry(
			"        - when: {route: agent.cancel}\n          respond: {status: queued}\n"+
				"        - respond: {status: completed}\n"))
		require.Len(t, findings, 1, "%+v", findings)
		assert.Equal(t, provider.CodeTurnRouteUnknown, findings[0].Code)
		assert.Equal(t, "providers.perplexity_agent.background.turns[0].when.route", findings[0].Path)
	})

	// The two scripts are judged each on its own: a terminal run script and a
	// pending cancel script are no regression, because a cancel of a run whose
	// next retrieve is terminal records nothing.
	t.Run("each script is judged on its own", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, bgValidate(t, bgCancelEntry(
			"        - respond: {status: completed}\n",
			"        turns:\n          - when: {call_index: 0}\n            respond: {status: queued}\n"+
				"          - respond: {status: cancelled}\n")))
	})
}

// TestBackgroundCancelFaultValidator: background.cancel.fault is the cancel
// route's plan, so each attempt is checked against what that route serves. POST
// /v1/agent/{id}/cancel never streams, so a stream_* kind could never play; and an
// attempt's extra_fields are merged into the body last and win, so a response_id
// or a status there would forge the acknowledgement — another id, or a run
// completed, under the label perplexity.agent.cancel.accepted. Each is an error
// at its attempt, and an error stops readiness, so the forged body is never
// served.
func TestBackgroundCancelFaultValidator(t *testing.T) {
	t.Parallel()

	const base = "providers.perplexity_agent.background.cancel.fault.attempts"
	const neverStreams = "but POST /v1/agent/{id}/cancel never streams"
	type finding struct{ code, path string }
	tests := []struct {
		name     string
		attempts string
		want     []finding
		says     string // every finding's message says it
	}{
		{"a forged acknowledgement",
			`{extra_fields: {status: completed, response_id: resp_ffffffffffffffffffffffffffffffff}}`,
			[]finding{
				{codeAgentBackgroundField, base + "[0].extra_fields.response_id"},
				{codeAgentBackgroundField, base + "[0].extra_fields.status"},
			}, "is not allowed in background.cancel.fault: extra fields are merged into the body last and win"},
		{"even the status the acknowledgement carries", `{extra_fields: {status: cancelling}}`,
			[]finding{{codeAgentBackgroundField, base + "[0].extra_fields.status"}},
			"the acknowledgement's status is always cancelling"},
		{"an explicitly null response_id is still written", `{extra_fields: {response_id: null}}`,
			[]finding{{codeAgentBackgroundField, base + "[0].extra_fields.response_id"}},
			"the acknowledgement's response_id is always its job's id"},
		{"stream_disconnect", `{kind: stream_disconnect}`,
			[]finding{{scenario.CodeStreamFaultMismatch, base + "[0].kind"}}, neverStreams},
		{"stream_truncate_chunk", `{kind: stream_truncate_chunk}`,
			[]finding{{scenario.CodeStreamFaultMismatch, base + "[0].kind"}}, neverStreams},
		{"stream_stall", `{kind: stream_stall, delay: 1s}`,
			[]finding{{scenario.CodeStreamFaultMismatch, base + "[0].kind"}}, neverStreams},
		{"each attempt is addressed by its own index", `{status: 500}, {kind: stream_disconnect}, {extra_fields: {status: x}}`,
			[]finding{
				{scenario.CodeStreamFaultMismatch, base + "[1].kind"},
				{codeAgentBackgroundField, base + "[2].extra_fields.status"},
			}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// bgValidate requires the framework's own checks to pass: they judge a
			// fault plan's shape and know nothing of what this route serves.
			findings := bgValidate(t, withCancelFault(tc.attempts))
			got := make([]finding, 0, len(findings))
			for _, f := range findings {
				got = append(got, finding{f.Code, f.Path})
				assert.Equal(t, scenario.SeverityError, f.Severity, f.Path)
				assert.Contains(t, f.Message, tc.says, f.Path)
			}
			assert.ElementsMatch(t, tc.want, got, "%+v", findings)
			assert.False(t, scenario.Report{Findings: findings}.OK(), "an error stops readiness, so the plan is never served")
		})
	}

	// What a cancel fault plan is for: a status, a delay, a failure the vendor
	// acted on, a field the acknowledgement does not carry, and a body override,
	// which replaces the body rather than merging into it and is not read here,
	// as a retrieve's is not.
	t.Run("a plan that faults the cancel honestly loads clean", func(t *testing.T) {
		t.Parallel()
		for _, attempts := range []string{
			`{status: 500}`,
			`{status: 502, accepted: true}`,
			`{delay: 1s}`,
			`{extra_fields: {note: x}}`,
			`{extra_fields: {id: resp_x}}`,
			`{status: 503, body: {status: completed}}`,
			`{kind: truncate_body}, {kind: close_before_headers, accepted: true}`,
		} {
			assert.Empty(t, bgValidate(t, withCancelFault(attempts)), attempts)
		}
	})
}

// TestALegitimateCancelFaultPlanPlays: a plan the validator accepts is served as
// scripted — a failure first, and then an acknowledgement that carries an
// additive field beside its own two keys, which keep their values.
func TestALegitimateCancelFaultPlanPlays(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, withCancelFault(`{status: 502}, {delay: 1s, extra_fields: {note: x}}`))
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	require.Equal(t, http.StatusBadGateway, bgCancel(t, sim, base, id).status)
	assert.False(t, theJob(t, sim).CancelRequested, "the 502 did not commit")

	ack := bgCancel(t, sim, base, id)
	require.Equal(t, http.StatusOK, ack.status, "body: %s", ack.body)
	assert.Equal(t, map[string]any{"response_id": id, "status": "cancelling", "note": "x"}, ack.json(t))
	assert.True(t, theJob(t, sim).CancelRequested)

	cancels := onRoute(sim.AwaitRequests(t, Name, 3), cancelRoute)
	assert.Equal(t, []string{"perplexity.agent.cancel.unrecorded", "perplexity.agent.cancel.accepted"}, labelsOf(cancels))
	assert.Equal(t, []int{0, 1}, attemptsOf(cancels))
}

// TestACancelPeeksAsTheRetrieveSelects: a cancel judges the run by the snapshot
// its next RETRIEVE would serve, so it selects under the retrieve's route key.
// Here the run's first turn is conditioned on the retrieve route alone and serves
// every retrieve in_progress, so the cancel is recorded. A cancel that selected
// under any other key — its own included — would pass that turn by, judge the
// run by `completed`, and answer the 400 while every retrieve still served
// in_progress.
func TestACancelPeeksAsTheRetrieveSelects(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, `
version: 1
name: routed-background
providers:
  perplexity_agent:
    answer: A synchronous answer.
    background:
      turns:
        - when: {route: agent.retrieve}
          respond: {status: in_progress}
        - respond: {status: completed}
      cancel:
        turns:
          - respond: {status: cancelled}
`)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	assert.Equal(t, "in_progress", statusIn(t, bgRetrieve(t, sim, base, id)))
	acknowledged(t, bgCancel(t, sim, base, id), id)
	assert.Equal(t, "cancelled", statusIn(t, bgRetrieve(t, sim, base, id)))

	job := theJob(t, sim)
	assert.True(t, job.CancelRequested)
	assert.Equal(t, 1, job.CancelAtPoll)
}

// TestACancelFaultWithNoCancelScriptFaultsTheTerminal400: a background.cancel
// that scripts a fault plan and no turns — the cancel script may be empty — is
// still the cancel route's plan, and the 400 of a run already terminal is a
// served response, so the plan applies to it. The first cancel is the scripted
// 503, its label still saying what the cancel decided; the retry draws attempt 1
// and is the 400.
func TestACancelFaultWithNoCancelScriptFaultsTheTerminal400(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, `
version: 1
name: fault-only-cancel
providers:
  perplexity_agent:
    answer: A synchronous answer.
    background:
      turns:
        - respond: {status: completed}
      cancel:
        fault: {attempts: [{status: 503}, {}]}
`)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	faulted := bgCancel(t, sim, base, id)
	require.Equal(t, http.StatusServiceUnavailable, faulted.status, "body: %s", faulted.body)
	refusedAsTerminal(t, bgCancel(t, sim, base, id))
	assert.False(t, theJob(t, sim).CancelRequested)

	cancels := onRoute(sim.AwaitRequests(t, Name, 3), cancelRoute)
	require.Len(t, cancels, 2)
	assert.Equal(t, []string{"perplexity.agent.cancel.terminal", "perplexity.agent.cancel.terminal"}, labelsOf(cancels))
	assert.Equal(t, []int{0, 1}, attemptsOf(cancels))
	assert.Equal(t, journal.OutcomeFault, cancels[0].Outcome.Kind)
	assert.Equal(t, http.StatusServiceUnavailable, cancels[0].Outcome.Status)
	assert.Equal(t, journal.OutcomeScenario, cancels[1].Outcome.Kind)
	for _, e := range cancels {
		testkit.AssertNoFindings(t, e)
	}
}

// TestBackgroundCancelIsNotAnEntryLevelCancel: Perplexity's cancel is scripted
// under background:, so an entry-level cancel: on perplexity_agent stays a load
// error, a fault plan on a cancel turn does not load at all, and a
// background.cancel on the Sonar entry is rejected with the background block.
func TestBackgroundCancelIsNotAnEntryLevelCancel(t *testing.T) {
	t.Parallel()

	t.Run("an entry-level cancel on perplexity_agent", func(t *testing.T) {
		t.Parallel()
		findings := bgValidate(t, cancelScenario+"    cancel:\n      turns:\n        - respond: {status: cancelled}\n")
		require.Len(t, findings, 1, "%+v", findings)
		assert.Equal(t, provider.CodeCancelUnsupported, findings[0].Code)
		assert.Equal(t, "providers.perplexity_agent.cancel", findings[0].Path)
	})

	t.Run("a fault plan on a cancel turn", func(t *testing.T) {
		t.Parallel()
		src := strings.Replace(cancelScenario, "          - respond: {status: cancelled, usage",
			"          - fault: {attempts: [{status: 503}]}\n            respond: {status: cancelled, usage", 1)
		_, _, err := scenario.Parse([]byte(src))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "providers.perplexity_agent.background.cancel.turns[1].fault")
	})

	t.Run("a background cancel on the Sonar entry", func(t *testing.T) {
		t.Parallel()
		findings := bgValidate(t, `
version: 1
name: sonar-background-cancel
providers:
  perplexity:
    answer: sonar
    background:
      turns:
        - respond: {status: queued}
      cancel:
        turns:
          - respond: {status: cancelled}
`)
		var codes []string
		for _, f := range findings {
			codes = append(codes, f.Code)
			if f.Code == provider.CodeBackgroundUnsupported {
				assert.Equal(t, "providers.perplexity.background", f.Path)
			}
		}
		assert.Contains(t, codes, provider.CodeBackgroundUnsupported)
	})
}
