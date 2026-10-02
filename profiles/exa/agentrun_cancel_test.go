package exa

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

// These tests are the behaviour of POST /agent/runs/{id}/cancel over real
// sockets. Every wait on the journal is testkit's bounded AwaitRequests, and the
// one concurrent test synchronises on a channel; nothing here sleeps.

// cancelYAML is a run that answers running twice and then completes, with a
// cancel script that acknowledges once and then reports the cancellation with
// the usage and cost it accrued. The acknowledgement carries a usage the poll
// script's own running snapshots do not, so a body says which script served it.
const cancelYAML = `
version: 1
name: exa-agent-run-cancel
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - when: {call_index: 1}
        respond: {status: running}
      - respond:
          status: completed
          output: {text: done}
          cost_dollars: {total: 0.045}
    cancel:
      turns:
        - when: {call_index: 0}
          respond: {status: running, usage: {agent_compute_units: 1}}
        - respond:
            status: cancelled
            usage: {agent_compute_units: 3, searches: 2}
            cost_dollars: {total: 0.012, agent_compute: 0.01, search: 0.002}
`

// withCancelFault is cancelYAML with attempts as the cancel route's plan.
func withCancelFault(attempts string) string {
	return strings.Replace(cancelYAML, "    cancel:\n", "    cancel:\n      fault: {attempts: ["+attempts+"]}\n", 1)
}

// startCancel runs a real Exa listener over src.
func startCancel(t *testing.T, src string) *testkit.Sim {
	t.Helper()
	return testkit.Start(t,
		testkit.WithProfiles(Profile()),
		testkit.WithScenarioYAML(src),
		testkit.WithSkippedDelays())
}

// agentClient drives one listener, or one namespace of it, the way an
// application under test does.
type agentClient struct {
	t      *testing.T
	client *http.Client
	base   string
}

func clientFor(t *testing.T, sim *testkit.Sim) agentClient {
	return agentClient{t: t, client: sim.Client(), base: sim.URL(Name)}
}

// agentReply is everything a client received from one call. It is built
// without failing the test, so a goroutine may produce one.
type agentReply struct {
	status    int
	allow     string
	requestID string
	body      []byte
	err       error
}

// run decodes the reply as a JSON object.
func (r agentReply) run(t *testing.T) map[string]any {
	t.Helper()
	require.NoError(t, r.err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(r.body, &out), "body: %s", r.body)
	return out
}

// agentErr decodes the reply as an AgentErrorResponse.
func (r agentReply) agentErr(t *testing.T) (typ, code, message string) {
	t.Helper()
	require.NoError(t, r.err)
	return decodeAgentErrorBody(t, r.body, true)
}

// send issues one request with the default credential unless header names an
// Authorization or x-api-key of its own, or noAuth is set.
func (c agentClient) send(ctx context.Context, method, path, body string, header map[string]string, noAuth bool) agentReply {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return agentReply{err: err}
	}
	if !noAuth {
		req.Header.Set("x-api-key", "test-key")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return agentReply{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return agentReply{
		status:    resp.StatusCode,
		allow:     resp.Header.Get("Allow"),
		requestID: resp.Header.Get("x-request-id"),
		body:      data,
		err:       err,
	}
}

func (c agentClient) create() string {
	c.t.Helper()
	r := c.send(context.Background(), http.MethodPost, "/agent/runs", `{"query":"find the finding"}`,
		map[string]string{"Content-Type": "application/json"}, false)
	require.NoError(c.t, r.err)
	require.Equal(c.t, http.StatusOK, r.status, "create: %s", r.body)
	id, _ := r.run(c.t)["id"].(string)
	require.NotEmpty(c.t, id)
	return id
}

func (c agentClient) poll(id string) agentReply {
	return c.send(context.Background(), http.MethodGet, "/agent/runs/"+id, "", nil, false)
}

// cancel sends the request the vendor's own sample does: a POST with no body
// and no Content-Type.
func (c agentClient) cancel(id string) agentReply {
	return c.send(context.Background(), http.MethodPost, "/agent/runs/"+id+"/cancel", "", nil, false)
}

// ok requires a 200 and returns the reply.
func ok(t *testing.T, r agentReply) agentReply {
	t.Helper()
	require.NoError(t, r.err)
	require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
	return r
}

// statusOf is the run status a reply's body reports.
func statusOf(t *testing.T, r agentReply) string {
	t.Helper()
	s, _ := r.run(t)["status"].(string)
	return s
}

// onPath filters entries to one request path.
func onPath(entries []journal.Entry, path string) []journal.Entry {
	var out []journal.Entry
	for _, e := range entries {
		if e.Path == path {
			out = append(out, e)
		}
	}
	return out
}

func labels(entries []journal.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Outcome.Label
	}
	return out
}

func attempts(entries []journal.Entry) []int {
	out := make([]int, len(entries))
	for i, e := range entries {
		out[i] = e.Outcome.AttemptIndex
	}
	return out
}

// onlyJob returns the one job the sim holds.
func onlyJob(t *testing.T, sim *testkit.Sim) testkit.Job {
	t.Helper()
	all := sim.Jobs()
	require.Len(t, all, 1)
	return all[0]
}

// TestCancelOfARunningRunAnswersTheNextPollsSnapshot is the route's headline
// case: the cancel is recorded at the run's position, its response is exactly the
// snapshot the next poll returns, and the polls after it follow cancel.turns to
// cancelled with the usage and cost the scenario scripted.
func TestCancelOfARunningRunAnswersTheNextPollsSnapshot(t *testing.T) {
	t.Parallel()

	sim := startCancel(t, cancelYAML)
	c := clientFor(t, sim)
	id := c.create()

	first := ok(t, c.poll(id))
	cancel := ok(t, c.cancel(id))
	next := ok(t, c.poll(id))

	assert.Equal(t, string(next.body), string(cancel.body),
		"a cancel response is exactly the snapshot the job's next poll returns")
	assert.NotEqual(t, string(first.body), string(next.body), "the poll after the cancel is served from cancel.turns")
	acknowledged := cancel.run(t)
	assert.Equal(t, statusRunning, acknowledged["status"])
	assert.Equal(t, id, acknowledged["id"])
	assert.Nil(t, acknowledged["stopReason"])
	assert.Equal(t, 1.0, acknowledged["usage"].(map[string]any)["agentComputeUnits"])

	final := ok(t, c.poll(id))
	run := final.run(t)
	assert.Equal(t, statusCancelled, run["status"])
	assert.Equal(t, stopCancelled, run["stopReason"])
	assert.NotNil(t, run["completedAt"], "a cancelled run is terminal")
	assert.Equal(t, map[string]any{"agentComputeUnits": 3.0, "searches": 2.0, "emails": 0.0, "phoneNumbers": 0.0},
		run["usage"], "usage accrued before the cancellation is the scripted usage")
	assert.Equal(t, map[string]any{"total": 0.012, "agentCompute": 0.01, "search": 0.002, "emails": 0.0, "phoneNumbers": 0.0},
		run["costDollars"], "the cost billed for it is the scripted cost")
	assert.Equal(t, string(final.body), string(ok(t, c.poll(id)).body), "a cancelled run stays cancelled")

	job := onlyJob(t, sim)
	assert.True(t, job.CancelRequested)
	assert.Equal(t, 1, job.CancelAtPoll, "recorded at the position of the poll it answered for")
	assert.Equal(t, 4, job.Polls)

	entries := sim.AwaitRequests(t, Name, 6)
	assert.Equal(t, []string{
		"exa.agent_runs.created",
		"exa.agent_runs.polled.running",
		"exa.agent_runs.cancel.accepted",
		"exa.agent_runs.polled.running",
		"exa.agent_runs.polled.cancelled",
		"exa.agent_runs.polled.cancelled",
	}, labels(entries))

	cancels := onPath(entries, "/agent/runs/"+id+"/cancel")
	require.Len(t, cancels, 1)
	assert.Equal(t, "exa:agent_runs.cancel|path:id="+id, cancels[0].Outcome.FaultKey,
		"every job has its own cancel lane")
	assert.Equal(t, 0, cancels[0].Outcome.AttemptIndex)
	assert.Equal(t, http.StatusOK, cancels[0].Outcome.Status)
	testkit.AssertNoFindings(t, cancels[0])

	assert.Equal(t, []int{0, 1, 2, 3}, attempts(onPath(entries, "/agent/runs/"+id)),
		"the poll lane's attempt index stays absolute across the cancel")
}

// TestCancelIsJudgedByTheRunsNextPoll walks the proposal's two sequences and the
// one before them: a cancel recorded while the next poll is pending wins; a
// cancel sent once the next poll would be terminal answers that terminal run and
// records nothing, and the polls keep serving it.
func TestCancelIsJudgedByTheRunsNextPoll(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		polls     int
		recorded  bool
		label     string
		cancelled string // the status the cancel answers with
	}{
		{"create, cancel: the cancel wins", 0, true, "exa.agent_runs.cancel.accepted", statusRunning},
		{"create, poll, cancel: the cancel wins", 1, true, "exa.agent_runs.cancel.accepted", statusRunning},
		{"create, poll, poll, cancel: completion wins", 2, false, "exa.agent_runs.cancel.terminal", statusCompleted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sim := startCancel(t, cancelYAML)
			c := clientFor(t, sim)
			id := c.create()
			for range tc.polls {
				ok(t, c.poll(id))
			}

			cancel := ok(t, c.cancel(id))
			assert.Equal(t, tc.cancelled, statusOf(t, cancel))
			next := ok(t, c.poll(id))
			assert.Equal(t, string(next.body), string(cancel.body), "the cancel answered the next poll's snapshot")

			job := onlyJob(t, sim)
			assert.Equal(t, tc.recorded, job.CancelRequested)
			if tc.recorded {
				assert.Equal(t, tc.polls, job.CancelAtPoll)
				assert.Equal(t, statusCancelled, statusOf(t, ok(t, c.poll(id))))
			} else {
				assert.Equal(t, string(cancel.body), string(ok(t, c.poll(id)).body), "later polls are unchanged")
			}

			entries := sim.AwaitRequests(t, Name, tc.polls+4)
			cancels := onPath(entries, "/agent/runs/"+id+"/cancel")
			require.Len(t, cancels, 1)
			assert.Equal(t, tc.label, cancels[0].Outcome.Label)
			testkit.AssertNoFindings(t, cancels[0])
		})
	}
}

// TestEveryStatusIsJudgedByTheTerminalPredicate cancels a run whose next poll is
// each status in turn: queued and running are pending, so the cancel is recorded
// and answers the cancel script's acknowledgement; completed, failed and
// cancelled end the run, so the cancel answers that run and records nothing.
// Either way the cancel answers exactly what the next poll serves. The
// acknowledgement is written `respond: {}`, so the poll serving it pins the
// label to the status actually rendered, never the empty one scripted.
func TestEveryStatusIsJudgedByTheTerminalPredicate(t *testing.T) {
	t.Parallel()

	const tmpl = `
version: 1
name: terminal-predicate
providers:
  exa_agent_runs:
    turns:
      - respond: {status: STATUS, cost_dollars: {total: 0.01}}
    cancel:
      turns:
        - when: {call_index: 0}
          respond: {}
        - respond: {status: cancelled, cost_dollars: {total: 0.02}}
`
	tests := []struct {
		status   string
		recorded bool
	}{
		{statusQueued, true},
		{statusRunning, true},
		{statusCompleted, false},
		{statusFailed, false},
		{statusCancelled, false},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()

			sim := startCancel(t, strings.Replace(tmpl, "STATUS", tc.status, 1))
			c := clientFor(t, sim)
			id := c.create()

			cancel := ok(t, c.cancel(id))
			next := ok(t, c.poll(id))
			assert.Equal(t, string(next.body), string(cancel.body), "the cancel answered the next poll's snapshot")
			assert.Equal(t, tc.recorded, onlyJob(t, sim).CancelRequested)

			want := []string{"exa.agent_runs.cancel.terminal", "exa.agent_runs.polled." + tc.status}
			if tc.recorded {
				assert.Equal(t, statusRunning, statusOf(t, cancel), "the acknowledgement is running by default")
				want = []string{"exa.agent_runs.cancel.accepted", "exa.agent_runs.polled." + statusRunning}
			} else {
				assert.Equal(t, tc.status, statusOf(t, cancel), "a terminal run is answered as it stands")
			}
			assert.Equal(t, want, labels(sim.AwaitRequests(t, Name, 3)[1:]))
		})
	}
}

// TestACancelPeeksTheSnapshotThePollWouldSelect: a turn that names the poll's
// route is selected by a poll, so the cancel — which answers what the next poll
// serves — must judge and answer the same turn, in turns and in cancel.turns.
func TestACancelPeeksTheSnapshotThePollWouldSelect(t *testing.T) {
	t.Parallel()

	sim := startCancel(t, `
version: 1
name: cancel-peeks-the-poll
providers:
  exa_agent_runs:
    turns:
      - when: {route: agent_runs.poll, call_index: 0}
        respond: {status: running}
      - respond: {status: completed, output: {text: x}, cost_dollars: {total: 0.01}}
    cancel:
      turns:
        - when: {route: agent_runs.poll, call_index: 0}
          respond: {status: running, usage: {agent_compute_units: 1}}
        - respond: {status: cancelled, cost_dollars: {total: 0.02}}
`)
	c := clientFor(t, sim)
	id := c.create()

	cancel := ok(t, c.cancel(id))
	assert.Equal(t, statusRunning, statusOf(t, cancel), "the next poll is turns[0], which is pending")
	assert.Equal(t, 1.0, cancel.run(t)["usage"].(map[string]any)["agentComputeUnits"],
		"the cancel answered cancel.turns[0], the snapshot the next poll selects")
	repeat := ok(t, c.cancel(id))
	assert.Equal(t, string(cancel.body), string(repeat.body))
	assert.Equal(t, string(repeat.body), string(ok(t, c.poll(id)).body), "the cancel answered the next poll's snapshot")
	assert.Equal(t, statusCancelled, statusOf(t, ok(t, c.poll(id))))

	job := onlyJob(t, sim)
	assert.True(t, job.CancelRequested)
	assert.Zero(t, job.CancelAtPoll)
	cancels := onPath(sim.AwaitRequests(t, Name, 5), "/agent/runs/"+id+"/cancel")
	assert.Equal(t, []string{"exa.agent_runs.cancel.accepted", "exa.agent_runs.cancel.repeated"}, labels(cancels))
}

// TestACancelResolvesGroundingAsThePollDoes: a cancel renders its snapshot
// through the poll's own decode, so a citation in it resolves against the
// corpus exactly as the poll's does.
func TestACancelResolvesGroundingAsThePollDoes(t *testing.T) {
	t.Parallel()

	sim := startCancel(t, `
version: 1
name: cancel-grounding
sources:
  - {id: source-a, url: "https://example.test/report-a", title: Report A, text: The report states the finding.}
providers:
  exa_agent_runs:
    turns:
      - respond: {status: running}
    cancel:
      turns:
        - respond:
            status: cancelled
            output:
              text: partial
              grounding:
                - {field: answer, citations: [source-a]}
            cost_dollars: {total: 0.02}
`)
	c := clientFor(t, sim)
	id := c.create()

	for _, r := range []agentReply{ok(t, c.cancel(id)), ok(t, c.cancel(id))} {
		output, _ := r.run(t)["output"].(map[string]any)
		grounding, _ := output["grounding"].([]any)
		require.Len(t, grounding, 1, "body: %s", r.body)
		first, _ := grounding[0].(map[string]any)
		citations, _ := first["citations"].([]any)
		require.Len(t, citations, 1, "body: %s", r.body)
		citation, _ := citations[0].(map[string]any)
		assert.Equal(t, "Report A", citation["title"], "the citation did not resolve against the corpus")
		assert.Equal(t, "https://example.test/report-a", citation["url"])
		assert.Equal(t, string(r.body), string(ok(t, c.poll(id)).body), "the cancel answered the next poll's snapshot")
	}
}

// TestACancelRacingAPollResolvesOneWayOrTheOther sends a poll and a cancel at
// the same moment, at the position where the poll decides the race: if the
// cancel lands first it is recorded and the poll serves the acknowledgement; if
// the poll lands first the cancel sees the completed run next and records
// nothing. Either is correct; a mix of the two is the bug.
func TestACancelRacingAPollResolvesOneWayOrTheOther(t *testing.T) {
	t.Parallel()

	sim := startCancel(t, cancelYAML)
	c := clientFor(t, sim)
	id := c.create()
	ok(t, c.poll(id))

	start := make(chan struct{})
	var wg sync.WaitGroup
	var polled, cancelled agentReply
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		polled = c.poll(id)
	}()
	go func() {
		defer wg.Done()
		<-start
		cancelled = c.cancel(id)
	}()
	close(start)
	wg.Wait()

	ok(t, polled)
	ok(t, cancelled)
	after := ok(t, c.poll(id))

	if job := onlyJob(t, sim); job.CancelRequested {
		assert.Equal(t, 1, job.CancelAtPoll, "cancel before poll: recorded at the racing poll's position")
		assert.Equal(t, string(cancelled.body), string(polled.body), "the racing poll served the acknowledgement")
		assert.Equal(t, statusCancelled, statusOf(t, after))
	} else {
		assert.Equal(t, statusCompleted, statusOf(t, cancelled), "poll before cancel: completion wins")
		assert.Equal(t, statusRunning, statusOf(t, polled))
		assert.Equal(t, string(cancelled.body), string(after.body), "the poll after the cancel serves what the cancel answered")
	}
}

// TestASecondCancelIsARepeat records nothing new: it answers the cancel script's
// snapshot for the run's next poll, and the marker stays where the first put it.
func TestASecondCancelIsARepeat(t *testing.T) {
	t.Parallel()

	sim := startCancel(t, cancelYAML)
	c := clientFor(t, sim)
	id := c.create()

	first := ok(t, c.cancel(id))
	repeat := ok(t, c.cancel(id))
	assert.Equal(t, string(first.body), string(repeat.body), "the same position answers the same snapshot")
	assert.Equal(t, string(first.body), string(ok(t, c.poll(id)).body))

	later := ok(t, c.cancel(id))
	assert.Equal(t, statusCancelled, statusOf(t, later), "a repeat answers the cancel script at the run's next poll")
	assert.Equal(t, string(later.body), string(ok(t, c.poll(id)).body))

	job := onlyJob(t, sim)
	assert.True(t, job.CancelRequested)
	assert.Zero(t, job.CancelAtPoll, "one marker, where the first cancel put it")

	cancels := onPath(sim.AwaitRequests(t, Name, 6), "/agent/runs/"+id+"/cancel")
	assert.Equal(t, []string{
		"exa.agent_runs.cancel.accepted", "exa.agent_runs.cancel.repeated", "exa.agent_runs.cancel.repeated",
	}, labels(cancels))
	assert.Equal(t, []int{0, 1, 2}, attempts(cancels), "every cancel claims exactly one attempt")
	assert.NotEqual(t, first.requestID, repeat.requestID, "each cancel is its own call")
}

// TestACancelWhoseAttemptDoesNotCommitRecordsNothing is a scripted 500 that a
// vendor never acted on: nothing is recorded, the next poll carries on from the
// run's own script, and the retry is the cancel that takes effect.
func TestACancelWhoseAttemptDoesNotCommitRecordsNothing(t *testing.T) {
	t.Parallel()

	sim := startCancel(t, withCancelFault(`{status: 500}, {}`))
	c := clientFor(t, sim)
	id := c.create()

	failed := c.cancel(id)
	require.Equal(t, http.StatusInternalServerError, failed.status)
	typ, code, message := failed.agentErr(t)
	assert.Equal(t, "SERVER_ERROR", typ)
	assert.Equal(t, "SERVER_ERROR", code)
	assert.Equal(t, "Server error", message)
	assert.False(t, onlyJob(t, sim).CancelRequested, "an attempt that does not commit records nothing")

	polled := ok(t, c.poll(id))
	assert.Equal(t, statusRunning, statusOf(t, polled))
	assert.Equal(t, 0.0, polled.run(t)["usage"].(map[string]any)["agentComputeUnits"], "served from the run's own turns")

	ok(t, c.cancel(id))
	job := onlyJob(t, sim)
	assert.True(t, job.CancelRequested)
	assert.Equal(t, 1, job.CancelAtPoll)

	entries := sim.AwaitRequests(t, Name, 4)
	cancels := onPath(entries, "/agent/runs/"+id+"/cancel")
	assert.Equal(t, []string{"exa.agent_runs.cancel.unrecorded", "exa.agent_runs.cancel.accepted"}, labels(cancels))
	assert.Equal(t, []int{0, 1}, attempts(cancels))
	assert.Equal(t, http.StatusInternalServerError, cancels[0].Outcome.Status)
	assert.Equal(t, journal.OutcomeFault, cancels[0].Outcome.Kind)
	for _, e := range cancels {
		testkit.AssertNoFindings(t, e)
	}
	assert.Equal(t, "exa.agent_runs.polled.running", onPath(entries, "/agent/runs/"+id)[0].Outcome.Label)
}

// TestAnAcceptedCancelTakesEffectAndLosesItsReply is the cancel twin of the
// accepted create: the client sees an error or a dead connection, and the run is
// cancelled anyway, which its next poll and the job record both show.
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

			sim := startCancel(t, withCancelFault(tc.attempt))
			c := clientFor(t, sim)
			id := c.create()

			got := c.cancel(id)
			if tc.status == 0 {
				require.Error(t, got.err, "the client must see a connection failure, got status %d", got.status)
			} else {
				require.NoError(t, got.err)
				require.Equal(t, tc.status, got.status)
			}

			cancel := onPath(sim.AwaitRequests(t, Name, 2), "/agent/runs/"+id+"/cancel")
			require.Len(t, cancel, 1)
			assert.Equal(t, "exa.agent_runs.cancel.accepted", cancel[0].Outcome.Label)
			testkit.AssertNoErrors(t, cancel[0])

			job := onlyJob(t, sim)
			assert.True(t, job.CancelRequested, "the cancel took effect although its reply was lost")
			assert.Zero(t, job.CancelAtPoll)
			assert.Equal(t, 1.0, ok(t, c.poll(id)).run(t)["usage"].(map[string]any)["agentComputeUnits"],
				"the next poll is the cancel script's acknowledgement")
		})
	}
}

// TestACancelTheScenarioCannotAnswerIsTheVendors500: with no cancel script the
// cancel would take effect but nothing could answer the polls after it, so it is
// refused with the vendor's 500, an error finding, and nothing recorded.
func TestACancelTheScenarioCannotAnswerIsTheVendors500(t *testing.T) {
	t.Parallel()

	sim := startCancel(t, asyncScenario)
	c := clientFor(t, sim)
	id := c.create()

	for range 2 {
		r := c.cancel(id)
		require.Equal(t, http.StatusInternalServerError, r.status, "body: %s", r.body)
		typ, code, _ := r.agentErr(t)
		assert.Equal(t, "SERVER_ERROR", typ)
		assert.Equal(t, "SERVER_ERROR", code)
		assert.Len(t, r.requestID, requestIDLen)
	}
	assert.False(t, onlyJob(t, sim).CancelRequested, "nothing is recorded")
	assert.Equal(t, statusRunning, statusOf(t, ok(t, c.poll(id))), "the polls carry on from the run's own turns")

	cancels := onPath(sim.AwaitRequests(t, Name, 4), "/agent/runs/"+id+"/cancel")
	require.Len(t, cancels, 2)
	assert.Equal(t, []int{0, 1}, attempts(cancels), "the claimed attempt stays spent")
	for _, e := range cancels {
		assert.Equal(t, "exa.error."+tagInternalError, e.Outcome.Label)
		codes := map[string]journal.Severity{}
		for _, f := range e.Findings {
			codes[f.Code] = f.Severity
			if f.Code == provider.CodeJobCancelUnscripted {
				assert.Contains(t, f.Message, "providers."+NameAgentRuns+" scripts no cancel.turns",
					"the finding names the entry whose script could not answer")
			}
		}
		assert.Equal(t, map[string]journal.Severity{
			provider.CodeJobCancelUnscripted: journal.SeverityError,
			provider.CodeAttemptOnRejection:  journal.SeverityWarning,
		}, codes, "the finding says why; the stripped attempt is reported, as U3 documents")
	}
}

// requestIDLen is the length of a derived x-request-id.
const requestIDLen = 32

// TestACancelOfARunThisNamespaceDoesNotHoldClaimsNothing: an unknown identifier,
// one minted in another namespace and a malformed one are each the vendor's 404,
// claim no cancel attempt, and leave every real run untouched.
func TestACancelOfARunThisNamespaceDoesNotHoldClaimsNothing(t *testing.T) {
	t.Parallel()

	sim := startCancel(t, cancelYAML)
	home := agentClient{t: t, client: sim.Client(), base: sim.Namespace(t, "home").URL(Name)}
	away := agentClient{t: t, client: sim.Client(), base: sim.Namespace(t, "away").URL(Name)}
	id := home.create()

	for _, tc := range []struct {
		name   string
		client agentClient
		id     string
	}{
		{"an identifier never minted", home, "agent_run_neverminted"},
		{"an identifier minted in another namespace", away, id},
	} {
		r := tc.client.cancel(tc.id)
		require.Equal(t, http.StatusNotFound, r.status, "%s: %s", tc.name, r.body)
		typ, code, _ := r.agentErr(t)
		assert.Equal(t, "NOT_FOUND", typ, tc.name)
		assert.Equal(t, "RUN_NOT_FOUND", code, tc.name)
	}

	// A malformed identifier is answered exactly as a poll of it is: read, then
	// matched here.
	const malformed = "agent.run$bad"
	polled, cancelled := home.poll(malformed), home.cancel(malformed)
	assert.Equal(t, http.StatusNotFound, cancelled.status)
	assert.Equal(t, polled.status, cancelled.status)
	assert.Equal(t, string(polled.body), string(cancelled.body))

	job := sim.Jobs()
	require.Len(t, job, 1)
	assert.False(t, job[0].CancelRequested)
	assert.Zero(t, job[0].Polls)
	first := ok(t, home.poll(id))
	assert.Equal(t, 0.0, first.run(t)["usage"].(map[string]any)["agentComputeUnits"], "the run's first poll is still turns[0]")

	// The journal records each path as received, namespace prefix included.
	entries := sim.AwaitRequests(t, Name, 6)
	for _, path := range []string{"/n/home/agent/runs/agent_run_neverminted/cancel", "/n/away/agent/runs/" + id + "/cancel"} {
		cancels := onPath(entries, path)
		require.Len(t, cancels, 1, path)
		assert.Equal(t, -1, cancels[0].Outcome.AttemptIndex, "%s claimed an attempt", path)
		assert.Equal(t, "exa.error."+tagNotFound, cancels[0].Outcome.Label, path)
	}
	pollOfMalformed := onPath(entries, "/n/home/agent/runs/"+malformed)
	cancelOfMalformed := onPath(entries, "/n/home/agent/runs/"+malformed+"/cancel")
	require.Len(t, pollOfMalformed, 1)
	require.Len(t, cancelOfMalformed, 1)
	assert.Equal(t, codesIn(pollOfMalformed[0]), codesIn(cancelOfMalformed[0]), "the same findings as the poll")
	assert.Equal(t, pollOfMalformed[0].Outcome.AttemptIndex, cancelOfMalformed[0].Outcome.AttemptIndex)
	assert.Equal(t, -1, cancelOfMalformed[0].Outcome.AttemptIndex)
}

// TestACancelWithoutTheRightCredentialClaimsNothingAndLeaksNothing: a missing or
// wrong key is the vendor's 401, claims nothing, records nothing, and the key
// presented reaches neither the journal nor the response.
func TestACancelWithoutTheRightCredentialClaimsNothingAndLeaksNothing(t *testing.T) {
	t.Parallel()

	const sentinel = "sk-live-SENTINEL-cancel"
	src := strings.Replace(cancelYAML, "  exa_agent_runs:\n", "  exa_agent_runs:\n    auth: {expect_key: the-expected-key}\n", 1)
	sim := startCancel(t, src)
	c := clientFor(t, sim)

	created := c.send(context.Background(), http.MethodPost, "/agent/runs", `{"query":"q"}`,
		map[string]string{"Content-Type": "application/json", "x-api-key": "the-expected-key"}, true)
	id, _ := ok(t, created).run(t)["id"].(string)
	require.NotEmpty(t, id)

	path := "/agent/runs/" + id + "/cancel"
	tests := []struct {
		name   string
		path   string
		header map[string]string
	}{
		{"no credential", path, nil},
		{"a wrong x-api-key", path, map[string]string{"x-api-key": sentinel}},
		{"a wrong bearer token", path, map[string]string{"Authorization": "Bearer " + sentinel}},
		{"a key in the query string", path + "?api_key=" + sentinel, nil},
	}
	for _, tc := range tests {
		r := c.send(context.Background(), http.MethodPost, tc.path, "", tc.header, true)
		require.Equal(t, http.StatusUnauthorized, r.status, "%s: %s", tc.name, r.body)
		typ, code, _ := r.agentErr(t)
		assert.Equal(t, "AUTHENTICATION_ERROR", typ, tc.name)
		assert.Equal(t, "TEAM_NOT_FOUND", code, tc.name)
		assert.NotContains(t, string(r.body), sentinel, tc.name)
		assert.NotContains(t, r.requestID, sentinel, tc.name)
	}

	assert.False(t, onlyJob(t, sim).CancelRequested)
	cancels := sim.AwaitRequests(t, Name, 1+len(tests))[1:]
	for _, e := range cancels {
		assert.Equal(t, -1, e.Outcome.AttemptIndex, "a refused cancel claims nothing")
	}
	testkit.AssertNoCredentialLeak(t, sim, sentinel)
}

// inProcess is the Exa handler served in process rather than over a socket, for
// what a real-socket Sim cannot reach: a job store that misbehaves on cue, and
// the log lines a Sim's logger discards.
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

// do sends one request with exactly the headers given.
func (p inProcess) do(method, target string, header map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	p.handler.ServeHTTP(rec, req)
	return rec
}

// create mints a run, presenting key, and returns its id.
func (p inProcess) create(key string) string {
	p.t.Helper()
	created := p.do(http.MethodPost, "/agent/runs",
		map[string]string{"Content-Type": "application/json", "x-api-key": key}, `{"query":"q"}`)
	require.Equal(p.t, http.StatusOK, created.Code, created.Body.String())
	var run struct {
		ID string `json:"id"`
	}
	require.NoError(p.t, json.Unmarshal(created.Body.Bytes(), &run))
	require.NotEmpty(p.t, run.ID)
	return run.ID
}

// entryFor returns the one journal entry recorded for path.
func (p inProcess) entryFor(path string) journal.Entry {
	p.t.Helper()
	entries := onPath(p.ring.Snapshot(), path)
	require.Len(p.t, entries, 1, path)
	return entries[0]
}

// TestACancelCredentialNeverReachesALogLine is the log half of the credential
// rule, which a real-socket Sim cannot observe: its logger discards. Every
// line the request path writes is captured here and searched.
func TestACancelCredentialNeverReachesALogLine(t *testing.T) {
	t.Parallel()

	const sentinel = "sk-live-SENTINEL-log"
	src := strings.Replace(cancelYAML, "  exa_agent_runs:\n", "  exa_agent_runs:\n    auth: {expect_key: the-expected-key}\n", 1)
	var logs bytes.Buffer
	p := serveInProcess(t, src, jobs.NewRegistry(jobs.Limits{}), &logs)
	ring := p.ring

	path := "/agent/runs/" + p.create("the-expected-key") + "/cancel"
	for _, rec := range []*httptest.ResponseRecorder{
		p.do(http.MethodPost, path, map[string]string{"x-api-key": sentinel}, ""),
		p.do(http.MethodPost, path, map[string]string{"Authorization": "Bearer " + sentinel}, ""),
		p.do(http.MethodPost, path+"?api_key="+sentinel, nil, ""),
		p.do(http.MethodPost, path, map[string]string{"x-api-key": sentinel, "Content-Type": "application/json"},
			`{"api_key":"`+sentinel+`"}`),
	} {
		require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
		assert.NotContains(t, rec.Body.String(), sentinel)
	}

	require.NotEmpty(t, logs.String(), "the request path must have logged, or this proves nothing")
	assert.NotContains(t, logs.String(), sentinel, "a log line carried the credential")
	for _, e := range ring.Snapshot() {
		encoded, err := json.Marshal(e)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), sentinel, "journal entry %d carried the credential", e.Seq)
	}
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

// TestACancelWhoseRunVanishesAfterItResolvedIsServed: a cancel that resolved its
// run has claimed its attempt, so when a reset removes the run before the cancel
// is decided, the vendor's 404 it answers is a served response to that attempt —
// it carries the per-call request id, a scripted fault on the attempt applies to
// it, and nothing reports the attempt as stripped. A cancel of an id that never
// resolved claims nothing and keeps the fixed request id.
func TestACancelWhoseRunVanishesAfterItResolvedIsServed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		src    string
		status int
		code   string
		kind   journal.OutcomeKind
	}{
		{"the vendor's 404, served", cancelYAML, http.StatusNotFound, "RUN_NOT_FOUND", journal.OutcomeScenario},
		{"a scripted fault on the claimed attempt", withCancelFault(`{status: 500}`),
			http.StatusInternalServerError, "SERVER_ERROR", journal.OutcomeFault},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := serveInProcess(t, tc.src, vanishingStore{jobs.NewRegistry(jobs.Limits{})}, io.Discard)
			auth := map[string]string{"x-api-key": "test-key"}
			never := p.do(http.MethodPost, "/agent/runs/agent_run_neverminted/cancel", auth, "")
			require.Equal(t, http.StatusNotFound, never.Code, never.Body.String())

			id := p.create("test-key")
			path := "/agent/runs/" + id + "/cancel"
			rec := p.do(http.MethodPost, path, auth, "")
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			_, code, _ := decodeAgentErrorBody(t, rec.Body.Bytes(), true)
			assert.Equal(t, tc.code, code)

			e := p.entryFor(path)
			assert.Equal(t, 0, e.Outcome.AttemptIndex, "the cancel claimed its attempt")
			assert.Equal(t, tc.kind, e.Outcome.Kind)
			assert.NotContains(t, codesIn(e), provider.CodeAttemptOnRejection, "a served response keeps its attempt")
			assert.NotContains(t, codesIn(e), provider.CodeJobCancelUnscripted)
			if tc.status == http.StatusNotFound {
				assert.Len(t, rec.Header().Get("x-request-id"), requestIDLen)
				assert.NotEqual(t, never.Header().Get("x-request-id"), rec.Header().Get("x-request-id"),
					"a served 404 carries the per-call request id, not the fixed one of a cancel that claimed nothing")
			}

			neverEntry := p.entryFor("/agent/runs/agent_run_neverminted/cancel")
			assert.Equal(t, -1, neverEntry.Outcome.AttemptIndex, "an id that never resolved claims nothing")
		})
	}
}

// contendedStore is a job store in which a poll always lands between a cancel
// reading the run's position and recording against it.
type contendedStore struct{ *jobs.Registry }

func (s contendedStore) MarkCancel(namespace, id string, _ int) (jobs.Job, jobs.MarkOutcome) {
	job, _ := s.Lookup(namespace, id)
	return job, jobs.PositionMoved
}

// TestACancelThatCannotBeRecordedForContentionIsTheVendors500: when the run's
// position never holds still long enough to record the cancel, the client gets
// the vendor's 500 with the simulator's fixed message, the finding that says why
// stays in the journal, and nothing is recorded.
func TestACancelThatCannotBeRecordedForContentionIsTheVendors500(t *testing.T) {
	t.Parallel()

	store := contendedStore{jobs.NewRegistry(jobs.Limits{})}
	p := serveInProcess(t, cancelYAML, store, io.Discard)
	id := p.create("test-key")
	path := "/agent/runs/" + id + "/cancel"

	rec := p.do(http.MethodPost, path, map[string]string{"x-api-key": "test-key"}, "")
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	typ, code, message := decodeAgentErrorBody(t, rec.Body.Bytes(), true)
	assert.Equal(t, "SERVER_ERROR", typ)
	assert.Equal(t, "SERVER_ERROR", code)
	assert.Equal(t, messageInternalError, message, "the wire carries the fixed message, never the finding's")

	e := p.entryFor(path)
	assert.Contains(t, codesIn(e), provider.CodeJobCancelContended)
	for _, f := range e.Findings {
		if f.Code == provider.CodeJobCancelContended {
			assert.NotContains(t, rec.Body.String(), f.Message)
		}
	}
	assert.NotContains(t, rec.Body.String(), id, "the finding names the run; the wire does not")
	job, found := store.Lookup(provider.DefaultNamespace, id)
	require.True(t, found)
	assert.False(t, job.CancelRequested, "nothing is recorded")
}

// TestCancellingOneRunLeavesEveryOtherAlone: the cancel lane, the poll lane and
// the create lane each draw on their own budget, and each job has its own cancel
// lane, so cancelling one run neither advances nor marks another, and no retry
// on one route spends another's attempts.
func TestCancellingOneRunLeavesEveryOtherAlone(t *testing.T) {
	t.Parallel()

	src := strings.Replace(withCancelFault(`{status: 500}, {}`), "    turns:\n      - when: {call_index: 0}\n",
		"    create:\n      fault: {attempts: [{status: 503}, {}]}\n    turns:\n      - when: {call_index: 0}\n"+
			"        fault: {attempts: [{status: 503}, {}]}\n", 1)
	sim := startCancel(t, src)
	c := clientFor(t, sim)

	refused := c.send(context.Background(), http.MethodPost, "/agent/runs", `{"query":"q"}`,
		map[string]string{"Content-Type": "application/json"}, false)
	require.Equal(t, http.StatusServiceUnavailable, refused.status, "create attempt 0 is the scripted 503")
	a := c.create()
	b := c.create()

	require.Equal(t, http.StatusInternalServerError, c.cancel(a).status, "a's cancel attempt 0")
	require.Equal(t, http.StatusServiceUnavailable, c.poll(a).status, "a's poll attempt 0")
	ok(t, c.cancel(a))
	ok(t, c.poll(a))

	byID := map[string]testkit.Job{}
	for _, j := range sim.Jobs() {
		byID[j.ID] = j
	}
	assert.True(t, byID[a].CancelRequested)
	assert.Equal(t, 1, byID[a].CancelAtPoll)
	assert.False(t, byID[b].CancelRequested, "cancelling a marks nothing on b")
	assert.Zero(t, byID[b].Polls, "cancelling a advances nothing on b")

	require.Equal(t, http.StatusInternalServerError, c.cancel(b).status, "b has its own cancel budget, starting at attempt 0")

	entries := sim.AwaitRequests(t, Name, 8)
	assert.Equal(t, []int{0, 1, 2}, attempts(onPath(entries, "/agent/runs")), "the create budget")
	assert.Equal(t, []int{0, 1}, attempts(onPath(entries, "/agent/runs/"+a)), "a's poll budget")
	assert.Equal(t, []int{0, 1}, attempts(onPath(entries, "/agent/runs/"+a+"/cancel")), "a's cancel budget")
	assert.Equal(t, []int{0}, attempts(onPath(entries, "/agent/runs/"+b+"/cancel")), "b's cancel budget")
}

// TestAClientThatAbandonsAPollIsNotACancel: a client that closes its own
// connection mid-poll leaves no cancel-route entry and no marker. The poll it
// abandoned still spent its position, and the journal shows the abort.
func TestAClientThatAbandonsAPollIsNotACancel(t *testing.T) {
	t.Parallel()

	src := strings.Replace(cancelYAML, "      - when: {call_index: 0}\n        respond: {status: running}\n",
		"      - when: {call_index: 0}\n        fault: {attempts: [{delay_after_headers: 1h}]}\n"+
			"        respond: {status: running}\n", 1)
	// Real delays: the poll must still be in flight when the client leaves.
	sim := testkit.Start(t, testkit.WithProfiles(Profile()), testkit.WithScenarioYAML(src))
	c := clientFor(t, sim)
	id := c.create()

	ctx, leave := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/agent/runs/"+id, nil)
	require.NoError(t, err)
	req.Header.Set("x-api-key", "test-key")
	resp, err := c.client.Do(req)
	require.NoError(t, err, "the poll's headers arrive before the hold")
	leave()
	_ = resp.Body.Close()

	entries := sim.AwaitRequests(t, Name, 2)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Path, "/cancel"), "no cancel-route entry: %s", e.Path)
	}
	abandoned := onPath(entries, "/agent/runs/"+id)
	require.Len(t, abandoned, 1)
	assert.True(t, abandoned[0].Outcome.Aborted, "the journal shows the poll was cut short")

	job := onlyJob(t, sim)
	assert.False(t, job.CancelRequested, "a hang-up is not a cancel")
	assert.Equal(t, 1, job.Polls, "the abandoned poll spent its position")
}

// TestTheCancelLifecycleIsDeterministic replays one sequence on two fresh sims:
// every status, body and request id is byte-identical, and a cancel's request id
// differs from the poll's at the same call position of the same run.
func TestTheCancelLifecycleIsDeterministic(t *testing.T) {
	t.Parallel()

	replay := func() []agentReply {
		sim := startCancel(t, cancelYAML)
		c := clientFor(t, sim)
		id := c.create()
		return []agentReply{c.poll(id), c.cancel(id), c.cancel(id), c.poll(id), c.poll(id)}
	}
	first, second := replay(), replay()
	// What each call answered — a cancel recorded at poll 1, its repeat, then
	// the acknowledgement and the cancellation — so two sims that failed alike
	// cannot pass as deterministic.
	want := []string{statusRunning, statusRunning, statusRunning, statusRunning, statusCancelled}
	for i := range first {
		require.NoError(t, first[i].err)
		require.Equal(t, http.StatusOK, first[i].status, "call %d: %s", i, first[i].body)
		assert.Equal(t, want[i], statusOf(t, first[i]), "call %d", i)
		assert.Equal(t, first[i].status, second[i].status, "call %d", i)
		assert.Equal(t, first[i].requestID, second[i].requestID, "call %d", i)
		assert.Equal(t, string(first[i].body), string(second[i].body), "call %d", i)
	}
	assert.NotEqual(t, first[0].requestID, first[1].requestID,
		"poll 0 and cancel 0 of one run are different calls and carry different request ids")
}

// TestTheCancelPathRefusesEveryOtherMethod: the mux owns the cancel path's other
// methods, fail-closed, before any handler can claim or record anything.
func TestTheCancelPathRefusesEveryOtherMethod(t *testing.T) {
	t.Parallel()

	sim := startCancel(t, cancelYAML)
	c := clientFor(t, sim)
	id := c.create()

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete} {
		r := c.send(context.Background(), method, "/agent/runs/"+id+"/cancel", "", nil, false)
		require.NoError(t, r.err)
		assert.Equal(t, http.StatusMethodNotAllowed, r.status, method)
		assert.Equal(t, http.MethodPost, r.allow, method)
		if method != http.MethodHead {
			var flat errorResponseWire
			require.NoError(t, json.Unmarshal(r.body, &flat), "%s: %s", method, r.body)
			assert.Equal(t, tagMethodNotAllowed, flat.Tag, method)
		}
	}
	job := onlyJob(t, sim)
	assert.False(t, job.CancelRequested)
	assert.Zero(t, job.Polls)
}

// TestACancelReadsNoBody: the spec declares no request body. A JSON object is
// accepted and ignored, and the cancel answers exactly as a bare one does; a body
// that is not JSON is refused by the shared request lifecycle, as on every
// route, before anything is claimed.
func TestACancelReadsNoBody(t *testing.T) {
	t.Parallel()

	sims := [2]*testkit.Sim{startCancel(t, cancelYAML), startCancel(t, cancelYAML)}
	bare := clientFor(t, sims[0])
	withBody := clientFor(t, sims[1])
	bareID, bodyID := bare.create(), withBody.create()
	require.Equal(t, bareID, bodyID)

	want := ok(t, bare.cancel(bareID))
	got := ok(t, withBody.send(context.Background(), http.MethodPost, "/agent/runs/"+bodyID+"/cancel",
		`{"reason":"user asked","effort":"not-an-effort"}`, map[string]string{"Content-Type": "application/json"}, false))
	assert.Equal(t, string(want.body), string(got.body))

	other := clientFor(t, sims[0])
	malformed := other.send(context.Background(), http.MethodPost, "/agent/runs/"+bareID+"/cancel",
		`{not json`, map[string]string{"Content-Type": "application/json"}, false)
	assert.Equal(t, http.StatusBadRequest, malformed.status, "body: %s", malformed.body)
	typ, _, _ := malformed.agentErr(t)
	assert.Equal(t, "INVALID_REQUEST", typ)
	var refused []journal.Entry
	for _, e := range onPath(sims[0].AwaitRequests(t, Name, 3), "/agent/runs/"+bareID+"/cancel") {
		if e.Outcome.Status == http.StatusBadRequest {
			refused = append(refused, e)
		}
	}
	require.Len(t, refused, 1)
	assert.Equal(t, -1, refused[0].Outcome.AttemptIndex, "a refused body claims nothing")
}

// TestAWhenRouteNamingTheCancelRouteIsRefusedAtLoad: a cancel selects no turn of
// its own — it peeks the snapshot the run's next POLL serves, matching on the
// poll's route — so a turn that names the cancel route could never fire, in
// turns or in cancel.turns, and is refused exactly as any other route the entry
// does not select on. The poll's own route is still accepted in both.
func TestAWhenRouteNamingTheCancelRouteIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	const tmpl = `
version: 1
name: cancel-route-name
providers:
  exa_agent_runs:
    turns:
      - when: {TURNS_WHEN}
        respond: {status: running}
      - respond: {status: completed, output: {text: x}, cost_dollars: {total: 0.01}}
    cancel:
      turns:
        - when: {CANCEL_WHEN}
          respond: {status: running}
        - respond: {status: cancelled, cost_dollars: {total: 0.02}}
`
	tests := []struct {
		name     string
		script   string // "turns" or "cancel.turns": where the route is named
		route    string
		rejected bool
	}{
		{"the cancel route's bare name in turns", "turns", "agent_runs.cancel", true},
		{"the cancel route's full key in turns", "turns", "exa:agent_runs.cancel", true},
		{"the cancel route's bare name in cancel.turns", "cancel.turns", "agent_runs.cancel", true},
		{"the cancel route's full key in cancel.turns", "cancel.turns", "exa:agent_runs.cancel", true},
		{"the poll route in turns", "turns", "agent_runs.poll", false},
		{"the poll route in cancel.turns", "cancel.turns", "agent_runs.poll", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			turnsWhen, cancelWhen := "call_index: 0", "call_index: 0"
			named := "route: " + tc.route + ", call_index: 0"
			if tc.script == "turns" {
				turnsWhen = named
			} else {
				cancelWhen = named
			}
			src := strings.NewReplacer("TURNS_WHEN", turnsWhen, "CANCEL_WHEN", cancelWhen).Replace(tmpl)

			var got []scenario.Finding
			for _, f := range provider.ValidateScenario(mustScenario(t, src), provider.MustSet(Profile()).Validators()) {
				if f.Code == provider.CodeTurnRouteUnknown {
					got = append(got, f)
				}
			}
			if !tc.rejected {
				assert.Empty(t, got, "the poll's route selects poll snapshots, in both scripts")
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, scenario.SeverityError, got[0].Severity)
			assert.Equal(t, "providers.exa_agent_runs."+tc.script+"[0].when.route", got[0].Path)
			_, offered, found := strings.Cut(got[0].Message, "it serves ")
			require.True(t, found, got[0].Message)
			assert.NotContains(t, offered, "agent_runs.cancel", "the message offers only routes a turn can name")
		})
	}
}

// TestTheCostRuleCoversEveryTerminalSnapshot is ruling 6, as the owner tightened
// it on 2026-10-02: every terminal turn, in turns and in cancel.turns, that does
// not script costDollars.total is warned about, cancelled like any other. A
// snapshot that scripts no cost_dollars, an empty one, or one that scripts only
// other meters still renders an unscripted 0 for total, so none of them counts as
// scripted; an explicit `total: 0` is a statement and does. A snapshot that is not
// terminal is never warned about.
func TestTheCostRuleCoversEveryTerminalSnapshot(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: cost-rule
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - when: {call_index: 1}
        respond: {status: failed}
      - when: {call_index: 2}
        respond: {status: failed, cost_dollars: {}}
      - when: {call_index: 3}
        respond: {status: failed, cost_dollars: {search: 0.01}}
      - when: {call_index: 4}
        respond: {status: failed, cost_dollars: {total: 0}}
      - respond: {status: completed, output: {text: x}, cost_dollars: {total: 0.01}}
    cancel:
      turns:
        - when: {call_index: 0}
          respond: {status: running}
        - when: {call_index: 1}
          respond: {status: cancelled, cost_dollars: {total: 0}}
        - when: {call_index: 2}
          respond: {status: cancelled, cost_dollars: {agent_compute: 0.01}}
        - respond: {status: cancelled}
`
	var got []string
	for _, f := range provider.ValidateScenario(mustScenario(t, src), provider.MustSet(Profile()).Validators()) {
		if f.Code == codeAgentRunCostUnscripted {
			assert.Equal(t, scenario.SeverityWarning, f.Severity)
			assert.Contains(t, f.Message, "script cost_dollars, at least total, on every terminal snapshot",
				"the message tells the author what to script")
			got = append(got, f.Path)
		}
	}
	assert.ElementsMatch(t, []string{
		"providers.exa_agent_runs.turns[1].respond.cost_dollars",
		"providers.exa_agent_runs.turns[2].respond.cost_dollars",
		"providers.exa_agent_runs.turns[3].respond.cost_dollars",
		"providers.exa_agent_runs.cancel.turns[2].respond.cost_dollars",
		"providers.exa_agent_runs.cancel.turns[3].respond.cost_dollars",
	}, got)
}
