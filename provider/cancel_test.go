package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/scenario"
)

// --- a test async profile ----------------------------------------------------
//
// acmeAsync is the smallest profile that owns a poll route and a cancel route,
// shaped the way the cancel units will register theirs: a per-job poll lane and a
// per-job cancel lane (LaneFrom path:id), the cancel's plan read from
// cancel.fault, and a cancel response that is the snapshot CancelJob hands back.
// Its wire shape is a test fixture, not any vendor's.

const (
	acmeAsyncPollKey   = "acme:jobs.poll"
	acmeAsyncCancelKey = "acme:jobs.cancel"
)

// acmeStatus reads a turn's scripted status; acmeTerminal is the profile's
// notion of terminal over it.
func acmeStatus(t *scenario.Turn) string {
	var p struct {
		Status string `yaml:"status"`
	}
	if err := t.Respond.Decode(&p); err != nil {
		return ""
	}
	return p.Status
}

func acmeTerminal(t *scenario.Turn) bool {
	switch acmeStatus(t) {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// acmePoll and acmeCancel call SelectPollTurn and CancelJob with the acme
// entry's own scripts, the way a profile serving a lifecycle from its entry
// does.
func acmePoll(x *Exchange) (*scenario.Turn, string) {
	base, turns, cancel := acmeScripts(x)
	return SelectPollTurn(x, base, turns, cancel)
}

func acmeCancel(x *Exchange) (CancelOutcome, *scenario.Turn, string) {
	base, turns, cancel := acmeScripts(x)
	return CancelJob(x, base, turns, cancel, acmeAsyncPollKey, acmeTerminal)
}

func acmeScripts(x *Exchange) (string, []scenario.Turn, *scenario.CancelPolicy) {
	e := x.Entry()
	if e == nil {
		return "providers.acme", nil, nil
	}
	return "providers." + e.Name, e.Turns, e.Cancel
}

func acmeJSON(v map[string]any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func acmeAsyncProfile() Profile {
	laneFromID := []string{LaneFromPath + "id"}
	faultBody := func(a scenario.FaultAttempt) []byte {
		return acmeJSON(map[string]any{"error": "fault", "status": a.Status})
	}

	create := func(x *Exchange) Response {
		id, ok := MintJob(x, "acme", "job_", Hex32)
		if !ok {
			return Response{Status: http.StatusServiceUnavailable, Body: []byte(`{"error":"refused"}`)}
		}
		return Response{Status: http.StatusCreated, Body: acmeJSON(map[string]any{"id": id}),
			Label: "acme.created", FaultEligible: true, FaultBody: faultBody}
	}
	poll := func(x *Exchange) Response {
		if !ResolveJob(x, x.Request.PathValue("id")) {
			return Response{Status: http.StatusNotFound, Body: []byte(`{"error":"not found"}`), Label: "acme.missing"}
		}
		turn, path := acmePoll(x)
		if turn == nil {
			return Response{Status: http.StatusInternalServerError, Body: []byte(`{"error":"no turn"}`),
				Label: "acme.poll.unscripted"}
		}
		return Response{Status: http.StatusOK, Body: acmeJSON(map[string]any{"status": acmeStatus(turn), "path": path}),
			Label: "acme.polled", FaultEligible: true, FaultBody: faultBody}
	}
	cancel := func(x *Exchange) Response {
		if !ResolveJob(x, x.Request.PathValue("id")) {
			return Response{Status: http.StatusNotFound, Body: []byte(`{"error":"not found"}`), Label: "acme.missing"}
		}
		outcome, turn, path := acmeCancel(x)
		body := map[string]any{"outcome": string(outcome), "path": path}
		status := http.StatusOK
		switch outcome {
		case CancelNotFound:
			status = http.StatusNotFound
		case CancelFailed:
			status = http.StatusInternalServerError
		default:
			body["status"] = acmeStatus(turn)
		}
		return Response{Status: status, Body: acmeJSON(body), Label: "acme.cancel." + string(outcome),
			FaultEligible: true, FaultBody: faultBody}
	}

	return Profile{
		Name: "acme", Title: "Acme", Summary: "a test async profile with a cancel",
		Routes: []Route{
			{Pattern: "POST /v1/jobs", FaultKey: "acme:jobs.create"},
			{
				Pattern: "GET /v1/jobs/{id}", FaultKey: acmeAsyncPollKey, LaneFrom: laneFromID,
				Fault: func(s *scenario.Scenario) *scenario.Fault { return TurnFault(s, "acme") },
			},
			{
				Pattern: "POST /v1/jobs/{id}/cancel", FaultKey: acmeAsyncCancelKey, LaneFrom: laneFromID,
				Fault: func(s *scenario.Scenario) *scenario.Fault {
					if e := s.Provider("acme"); e != nil && e.Cancel != nil && e.Cancel.Fault.HasAttempts() {
						return e.Cancel.Fault
					}
					return nil
				},
			},
		},
		Handlers: map[string]Handler{
			"POST /v1/jobs": create, "GET /v1/jobs/{id}": poll, "POST /v1/jobs/{id}/cancel": cancel,
		},
		ErrorBody:   fixedErrorBody(`{"error":"x"}`),
		DefaultAuth: scenario.AuthOptional,
	}
}

// asyncWorld is one acmeAsync listener on a real socket, with its job store and
// journal exposed so a test can read what was recorded.
type asyncWorld struct {
	t     *testing.T
	url   string
	store *jobs.Registry
	ring  *journal.Ring
}

func newAsyncWorld(t *testing.T, src string) *asyncWorld {
	t.Helper()
	return newAsyncWorldWith(t, src, jobs.NewRegistry(jobs.Limits{}))
}

func newAsyncWorldWith(t *testing.T, src string, store *jobs.Registry) *asyncWorld {
	t.Helper()

	sc := mustScenario(t, src)
	set := MustSet(acmeAsyncProfile())
	p, ok := set.Lookup("acme")
	require.True(t, ok)
	ring := journal.NewRing(256, 1<<20)
	srv := httptest.NewServer(p.Handler(Deps{
		Scenario: sc, Journal: ring, Faults: set.Faults(sc), Jobs: store, DelayMode: DelaySkip,
	}))
	t.Cleanup(srv.Close)
	return &asyncWorld{t: t, url: srv.URL, store: store, ring: ring}
}

// do sends one request on a client that never reuses a connection, and returns
// the status and decoded body, or -1 and nil for a transport error — what a
// client whose reply was lost observes.
func (w *asyncWorld) do(method, path string) (int, map[string]any) {
	w.t.Helper()

	req, err := http.NewRequestWithContext(w.t.Context(), method, w.url+path, strings.NewReader(`{}`))
	require.NoError(w.t, err)
	resp, err := newCreateClient(w.t).Do(req)
	if err != nil {
		return -1, nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return -1, nil
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body
}

func (w *asyncWorld) create() string {
	w.t.Helper()
	status, body := w.do(http.MethodPost, "/v1/jobs")
	require.Equal(w.t, http.StatusCreated, status)
	id, _ := body["id"].(string)
	require.NotEmpty(w.t, id)
	return id
}

func (w *asyncWorld) poll(id string) (int, map[string]any) {
	w.t.Helper()
	return w.do(http.MethodGet, "/v1/jobs/"+id)
}

func (w *asyncWorld) cancel(id string) (int, map[string]any) {
	w.t.Helper()
	return w.do(http.MethodPost, "/v1/jobs/"+id+"/cancel")
}

func (w *asyncWorld) job(id string) jobs.Job {
	w.t.Helper()
	j, ok := w.store.Lookup(DefaultNamespace, id)
	require.True(w.t, ok, "job %s is live", id)
	return j
}

// entries returns the journal entries for requests on path, in arrival order.
func (w *asyncWorld) entries(path string) []journal.Entry {
	var out []journal.Entry
	for _, e := range w.ring.Snapshot() {
		if e.Path == path {
			out = append(out, e)
		}
	}
	return out
}

func codesOf(e journal.Entry) map[string]journal.Severity {
	out := map[string]journal.Severity{}
	for _, f := range e.Findings {
		out[f.Code] = f.Severity
	}
	return out
}

// runningThenCompleted is the proposal's worked example: two running polls, then
// completed; a cancel script acknowledged as running, then cancelled.
const runningThenCompleted = `
version: 1
name: cancel
providers:
  acme:
    cancel:
      turns:
        - when: {call_index: 0}
          respond: {status: running}
        - respond: {status: cancelled}
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - when: {call_index: 1}
        respond: {status: running}
      - respond: {status: completed}
`

// --- the terminal-at-cancel-time table ---------------------------------------

// TestCancelIsJudgedByTheJobsNextPoll is the table the proposal's Q1 rests on.
// "Terminal at cancel time" means the snapshot the job's NEXT poll would be
// served, so a scenario can script "the client saw running while the run had
// already completed" — and a cancel response is always exactly that next
// snapshot.
func TestCancelIsJudgedByTheJobsNextPoll(t *testing.T) {
	t.Parallel()

	twoTurns := strings.Replace(runningThenCompleted,
		"      - when: {call_index: 1}\n        respond: {status: running}\n", "", 1)

	type step struct {
		op      string // "poll" or "cancel"
		outcome CancelOutcome
		status  string
		path    string
	}
	tests := []struct {
		name       string
		src        string
		steps      []step
		wantMarker bool
		wantAt     int
	}{
		{
			name: "create, poll, cancel: the cancel wins",
			src:  runningThenCompleted,
			steps: []step{
				{op: "poll", status: "running", path: "providers.acme.turns[0]"},
				{op: "cancel", outcome: CancelRecorded, status: "running", path: "providers.acme.cancel.turns[0]"},
				{op: "poll", status: "running", path: "providers.acme.cancel.turns[0]"},
				{op: "poll", status: "cancelled", path: "providers.acme.cancel.turns[1]"},
				{op: "poll", status: "cancelled", path: "providers.acme.cancel.turns[1]"},
			},
			wantMarker: true, wantAt: 1,
		},
		{
			name: "a cancel before the first poll",
			src:  runningThenCompleted,
			steps: []step{
				{op: "cancel", outcome: CancelRecorded, status: "running", path: "providers.acme.cancel.turns[0]"},
				{op: "poll", status: "running", path: "providers.acme.cancel.turns[0]"},
				{op: "poll", status: "cancelled", path: "providers.acme.cancel.turns[1]"},
			},
			wantMarker: true, wantAt: 0,
		},
		{
			name: "a second cancel answers from the cancel script",
			src:  runningThenCompleted,
			steps: []step{
				{op: "cancel", outcome: CancelRecorded, status: "running", path: "providers.acme.cancel.turns[0]"},
				{op: "poll", status: "running", path: "providers.acme.cancel.turns[0]"},
				{op: "cancel", outcome: CancelAlreadyCancelling, status: "cancelled", path: "providers.acme.cancel.turns[1]"},
				{op: "poll", status: "cancelled", path: "providers.acme.cancel.turns[1]"},
			},
			wantMarker: true, wantAt: 0,
		},
		{
			// The repeat is judged from the recorded position, not the absolute
			// one: Polls 1 minus CancelAtPoll 1 is cancel.turns[0], and the two
			// cancel turns differ, so peeking at Polls (1) or one past it is caught.
			name: "create, poll, cancel, cancel: a repeat answers from the cancel position",
			src:  runningThenCompleted,
			steps: []step{
				{op: "poll", status: "running", path: "providers.acme.turns[0]"},
				{op: "cancel", outcome: CancelRecorded, status: "running", path: "providers.acme.cancel.turns[0]"},
				{op: "cancel", outcome: CancelAlreadyCancelling, status: "running", path: "providers.acme.cancel.turns[0]"},
				{op: "poll", status: "running", path: "providers.acme.cancel.turns[0]"},
				{op: "cancel", outcome: CancelAlreadyCancelling, status: "cancelled", path: "providers.acme.cancel.turns[1]"},
				{op: "poll", status: "cancelled", path: "providers.acme.cancel.turns[1]"},
			},
			wantMarker: true, wantAt: 1,
		},
		{
			name: "create, poll, poll, cancel: completion wins though the client saw only running",
			src:  runningThenCompleted,
			steps: []step{
				{op: "poll", status: "running"},
				{op: "poll", status: "running"},
				{op: "cancel", outcome: CancelTerminal, status: "completed", path: "providers.acme.turns[2]"},
				{op: "poll", status: "completed", path: "providers.acme.turns[2]"},
			},
		},
		{
			name: "create, poll, poll (terminal), cancel: completion wins",
			src:  twoTurns,
			steps: []step{
				{op: "poll", status: "running"},
				{op: "poll", status: "completed"},
				{op: "cancel", outcome: CancelTerminal, status: "completed", path: "providers.acme.turns[1]"},
				{op: "cancel", outcome: CancelTerminal, status: "completed", path: "providers.acme.turns[1]"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := newAsyncWorld(t, tc.src)
			id := w.create()
			for i, s := range tc.steps {
				var status int
				var body map[string]any
				if s.op == "poll" {
					status, body = w.poll(id)
				} else {
					status, body = w.cancel(id)
					assert.Equal(t, string(s.outcome), body["outcome"], "step %d", i)
				}
				require.Equal(t, http.StatusOK, status, "step %d: %v", i, body)
				assert.Equal(t, s.status, body["status"], "step %d", i)
				if s.path != "" {
					assert.Equal(t, s.path, body["path"], "step %d", i)
				}
			}

			j := w.job(id)
			assert.Equal(t, tc.wantMarker, j.CancelRequested, "marker")
			if tc.wantMarker {
				assert.Equal(t, tc.wantAt, j.CancelAtPoll)
			}

			// Poll attempt indices stay absolute: the journal counts the lane, not
			// the cancel script.
			polls := w.entries("/v1/jobs/" + id)
			for i, e := range polls {
				assert.Equal(t, i, e.Outcome.AttemptIndex, "poll %d", i)
			}
			assert.Equal(t, len(polls), j.Polls, "Polls is the poll lane's claimed count")
			for _, e := range w.ring.Snapshot() {
				for _, f := range e.Findings {
					assert.NotEqual(t, journal.SeverityError, f.Severity, "%s: %+v", e.Path, f)
				}
			}
		})
	}
}

// --- what is recorded, and when ----------------------------------------------

// A cancel that would take effect on a run with no cancel script is an
// authoring error: there is nothing for the following polls to be served. It is
// the vendor's 500 with job.cancel_unscripted, and NO marker, so later polls
// never index into a cancel.turns that does not exist. An already-terminal
// cancel needs no script at all.
func TestCancelWithoutACancelScript(t *testing.T) {
	t.Parallel()

	const noScript = `
version: 1
name: no-cancel-script
providers:
  acme:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - respond: {status: completed}
`
	t.Run("a cancel that would take effect", func(t *testing.T) {
		t.Parallel()

		w := newAsyncWorld(t, noScript)
		id := w.create()
		status, body := w.cancel(id)
		require.Equal(t, http.StatusInternalServerError, status)
		assert.Equal(t, string(CancelFailed), body["outcome"])
		assert.False(t, w.job(id).CancelRequested, "no marker is recorded")

		cancels := w.entries("/v1/jobs/" + id + "/cancel")
		require.Len(t, cancels, 1)
		assert.Equal(t, journal.SeverityError, codesOf(cancels[0])[CodeJobCancelUnscripted])

		_, polled := w.poll(id)
		assert.Equal(t, "running", polled["status"], "the poll script carries on as if no cancel was sent")
	})

	t.Run("a cancel of a terminal run", func(t *testing.T) {
		t.Parallel()

		w := newAsyncWorld(t, noScript)
		id := w.create()
		w.poll(id)
		status, body := w.cancel(id)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, string(CancelTerminal), body["outcome"])
		assert.Equal(t, "completed", body["status"])
		assert.Empty(t, w.entries("/v1/jobs/" + id + "/cancel")[0].Findings)
	})

	t.Run("a cancel script that runs out", func(t *testing.T) {
		t.Parallel()

		w := newAsyncWorld(t, strings.Replace(runningThenCompleted, "        - respond: {status: cancelled}\n", "", 1))
		id := w.create()
		_, body := w.cancel(id)
		require.Equal(t, string(CancelRecorded), body["outcome"])
		w.poll(id) // cancel.turns[0]

		status, body := w.cancel(id)
		require.Equal(t, http.StatusInternalServerError, status, "cancel.turns[1] does not exist")
		assert.Equal(t, string(CancelFailed), body["outcome"])
		cancels := w.entries("/v1/jobs/" + id + "/cancel")
		assert.Equal(t, journal.SeverityError, codesOf(cancels[1])[CodeJobCancelUnscripted])
	})
}

// The cancel is recorded only if its attempt commits: a scripted 500 delivers
// no acknowledgement and is not marked accepted, so nothing is recorded and the
// client's retry — attempt 1 — is the cancel that takes effect.
func TestACancelIsRecordedOnlyWhenItsAttemptCommits(t *testing.T) {
	t.Parallel()

	w := newAsyncWorld(t, strings.Replace(runningThenCompleted, "    cancel:\n",
		"    cancel:\n      fault: {attempts: [{status: 500}, {}]}\n", 1))
	id := w.create()

	status, body := w.cancel(id)
	require.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, "fault", body["error"], "the scripted fault replaced the response")
	assert.False(t, w.job(id).CancelRequested, "a cancel whose attempt does not commit records nothing")

	_, polled := w.poll(id)
	assert.Equal(t, "running", polled["status"])
	assert.Equal(t, "providers.acme.turns[0]", polled["path"], "the poll script carries on")

	status, body = w.cancel(id)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, string(CancelRecorded), body["outcome"])
	j := w.job(id)
	assert.True(t, j.CancelRequested)
	assert.Equal(t, 1, j.CancelAtPoll)

	cancels := w.entries("/v1/jobs/" + id + "/cancel")
	require.Len(t, cancels, 2)
	assert.Equal(t, 0, cancels[0].Outcome.AttemptIndex)
	assert.Equal(t, 1, cancels[1].Outcome.AttemptIndex)
	for _, e := range cancels {
		assert.NotContains(t, codesOf(e), CodeAttemptOnRejection, "every cancel outcome is a served, fault-eligible response")
	}
}

// An accepted cancel took effect and its reply was lost: the client sees the
// connection close, the marker is recorded, and the next poll is served from the
// cancel script. accepted is reachable on a cancel, so it is not reported.
func TestAnAcceptedCancelRecordsAndLosesTheReply(t *testing.T) {
	t.Parallel()

	w := newAsyncWorld(t, strings.Replace(runningThenCompleted, "    cancel:\n",
		"    cancel:\n      fault: {attempts: [{kind: close_before_headers, accepted: true}]}\n", 1))
	id := w.create()

	status, _ := w.cancel(id)
	require.Equal(t, -1, status, "the client never receives the acknowledgement")

	j := w.job(id)
	assert.True(t, j.CancelRequested, "the cancel took effect")
	assert.Equal(t, 0, j.CancelAtPoll)

	// Journaled before the socket was touched, so it exists by now.
	cancels := w.entries("/v1/jobs/" + id + "/cancel")
	require.Len(t, cancels, 1)
	assert.True(t, cancels[0].Outcome.Aborted)
	assert.NotContains(t, codesOf(cancels[0]), CodeAcceptedUnreachable)

	_, polled := w.poll(id)
	assert.Equal(t, "providers.acme.cancel.turns[0]", polled["path"])
}

// TestEveryCancelClaimsExactlyOneAttempt: whatever a cancel decides, it draws
// exactly one attempt from its job's cancel lane. Asserted through a per-index
// plan: the fourth cancel of a job (attempt 3) is the scripted 418, whichever
// outcomes the three before it had. A path that claimed zero or two would move
// the 418 onto the wrong request.
func TestEveryCancelClaimsExactlyOneAttempt(t *testing.T) {
	t.Parallel()

	src := strings.Replace(runningThenCompleted, "    cancel:\n",
		"    cancel:\n      fault: {attempts: [{status: 500}, {}, {}, {status: 418}]}\n", 1)

	tests := []struct {
		name     string
		polls    int
		outcomes []CancelOutcome // what attempts 1 and 2 decide; attempt 0 is the scripted 500
	}{
		{name: "uncommitted, recorded, already cancelling", outcomes: []CancelOutcome{CancelRecorded, CancelAlreadyCancelling}},
		{name: "a terminal run", polls: 2, outcomes: []CancelOutcome{CancelTerminal, CancelTerminal}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := newAsyncWorld(t, src)
			id := w.create()
			for range tc.polls {
				w.poll(id)
			}

			status, _ := w.cancel(id)
			require.Equal(t, http.StatusInternalServerError, status, "attempt 0 is the scripted 500")
			for i, want := range tc.outcomes {
				status, body := w.cancel(id)
				require.Equal(t, http.StatusOK, status, "attempt %d", i+1)
				assert.Equal(t, string(want), body["outcome"], "attempt %d", i+1)
			}
			status, _ = w.cancel(id)
			assert.Equal(t, http.StatusTeapot, status, "attempt 3 must be the fourth cancel")

			for i, e := range w.entries("/v1/jobs/" + id + "/cancel") {
				assert.Equal(t, i, e.Outcome.AttemptIndex)
			}
		})
	}

	t.Run("an unscripted cancel still claims exactly one", func(t *testing.T) {
		t.Parallel()

		// An error finding strips the attempt, so the plan cannot show it; the
		// journal's attempt index can.
		w := newAsyncWorld(t, `
version: 1
name: n
providers:
  acme:
    turns:
      - respond: {status: running}
`)
		id := w.create()
		for range 3 {
			status, _ := w.cancel(id)
			require.Equal(t, http.StatusInternalServerError, status)
		}
		for i, e := range w.entries("/v1/jobs/" + id + "/cancel") {
			assert.Equal(t, i, e.Outcome.AttemptIndex)
			assert.Contains(t, codesOf(e), CodeJobCancelUnscripted)
		}
	})

	t.Run("a cancel unscripted at its first peek still claims exactly one", func(t *testing.T) {
		t.Parallel()

		// The poll script cannot answer position 2, so whether the job is
		// terminal cannot be judged: the cancel fails before it reaches the
		// commit check, and must have claimed already.
		w := newAsyncWorld(t, `
version: 1
name: runs-out
providers:
  acme:
    cancel:
      turns:
        - respond: {status: cancelled}
    turns:
      - when: {call_index: 0}
        respond: {status: running}
`)
		id := w.create()
		w.poll(id)
		w.poll(id)
		for range 2 {
			_, body := w.cancel(id)
			require.Equal(t, string(CancelFailed), body["outcome"])
		}
		for i, e := range w.entries("/v1/jobs/" + id + "/cancel") {
			assert.Equal(t, i, e.Outcome.AttemptIndex, "cancel %d", i)
		}
	})
}

// --- SelectPollTurn ----------------------------------------------------------

// A poll whose turn selection fails has still spent its index, so Polls must
// still count it: otherwise "the next snapshot" a cancel peeks is computed from
// the wrong position.
func TestAPollThatFailsSelectionStillAdvances(t *testing.T) {
	t.Parallel()

	w := newAsyncWorld(t, `
version: 1
name: runs-out
providers:
  acme:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
`)
	id := w.create()

	status, _ := w.poll(id)
	require.Equal(t, http.StatusOK, status)
	status, _ = w.poll(id)
	require.Equal(t, http.StatusInternalServerError, status, "poll 1 matches no turn")
	assert.Contains(t, codesOf(w.entries("/v1/jobs/" + id)[1]), CodeNoMatchingTurn)

	assert.Equal(t, 2, w.job(id).Polls, "both claimed polls are counted")

	// The cancel peeks position 2, which the script cannot answer either.
	status, body := w.cancel(id)
	require.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, string(CancelFailed), body["outcome"])
}

// directExchange is an Exchange built by hand for the paths HTTP cannot reach
// deterministically: a request that never resolved its job, and a reset landing
// between resolution and the store call.
func directExchange(t *testing.T, store jobs.Store, src string, route Route) *Exchange {
	t.Helper()
	sc := mustScenario(t, src)
	return &Exchange{
		Deps:     Deps{Scenario: sc, Jobs: store, Faults: &scriptedFaults{}}.Normalized(),
		Provider: "acme",
		Route:    route,
		lane:     Lane{Namespace: DefaultNamespace, Key: route.FaultKey},
	}
}

var (
	acmePollRoute   = Route{Pattern: "GET /v1/jobs/{id}", FaultKey: acmeAsyncPollKey}
	acmeCancelRoute = Route{Pattern: "POST /v1/jobs/{id}/cancel", FaultKey: acmeAsyncCancelKey}
)

func seedJob(t *testing.T, store jobs.Store, id string) {
	t.Helper()
	_, err := store.Create(jobs.Job{ID: id, Namespace: DefaultNamespace, Entry: "acme"})
	require.NoError(t, err)
}

// Calling either operation on a request that resolved no job is a programming
// error in the profile, and it fails loudly: an error finding, nothing served
// from a default, and no attempt claimed.
func TestPollAndCancelOnAnUnresolvedExchangeFailLoudly(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{})
	seedJob(t, store, "job_a")

	t.Run("SelectPollTurn", func(t *testing.T) {
		t.Parallel()

		x := directExchange(t, store, runningThenCompleted, acmePollRoute)
		turn, path := acmePoll(x)
		assert.Nil(t, turn)
		assert.Empty(t, path)
		require.True(t, x.Failed())
		assert.True(t, x.HasFinding(CodeJobIDInvalid))
		assert.False(t, x.claimed, "nothing to advance, so nothing is claimed")
	})

	t.Run("CancelJob", func(t *testing.T) {
		t.Parallel()

		x := directExchange(t, store, runningThenCompleted, acmeCancelRoute)
		outcome, turn, _ := acmeCancel(x)
		assert.Equal(t, CancelNotFound, outcome)
		assert.Nil(t, turn)
		require.True(t, x.Failed())
		assert.True(t, x.HasFinding(CodeJobIDInvalid))
		assert.False(t, x.claimed)
	})
}

// A reset landing between ResolveJob and the store call is a request in flight
// across a reset — undefined in general, but each operation must still answer
// without panicking: the cancel reports not found having claimed its one
// attempt, and the poll is served from its position.
func TestAResetBetweenResolutionAndTheStoreCall(t *testing.T) {
	t.Parallel()

	t.Run("CancelJob", func(t *testing.T) {
		t.Parallel()

		store := jobs.NewRegistry(jobs.Limits{})
		seedJob(t, store, "job_a")
		x := directExchange(t, store, runningThenCompleted, acmeCancelRoute)
		require.True(t, ResolveJob(x, "job_a"))
		store.Reset()

		outcome, turn, _ := acmeCancel(x)
		assert.Equal(t, CancelNotFound, outcome)
		assert.Nil(t, turn)
		assert.False(t, x.Failed())
		assert.True(t, x.claimed, "the cancel still claimed its one attempt")
		assert.Equal(t, 1, x.Deps.Faults.(*scriptedFaults).calls)
	})

	t.Run("CancelJob, between its Lookup and its MarkCancel", func(t *testing.T) {
		t.Parallel()

		store := &resetBeforeMark{Registry: jobs.NewRegistry(jobs.Limits{})}
		seedJob(t, store, "job_a")
		x := directExchange(t, store, runningThenCompleted, acmeCancelRoute)
		require.True(t, ResolveJob(x, "job_a"))

		outcome, turn, _ := acmeCancel(x)
		assert.Equal(t, CancelNotFound, outcome, "a job gone mid-cancel is not found, not a contended 500")
		assert.Nil(t, turn)
		assert.False(t, x.Failed())
		assert.Equal(t, 1, store.marks, "not found is not retried")
		_, live := store.Lookup(DefaultNamespace, "job_a")
		assert.False(t, live, "nothing was recorded, and nothing re-created the job")
	})

	t.Run("SelectPollTurn", func(t *testing.T) {
		t.Parallel()

		store := jobs.NewRegistry(jobs.Limits{})
		seedJob(t, store, "job_a")
		x := directExchange(t, store, runningThenCompleted, acmePollRoute)
		require.True(t, ResolveJob(x, "job_a"))
		store.Reset()

		turn, path := acmePoll(x)
		require.NotNil(t, turn)
		assert.Equal(t, "providers.acme.turns[0]", path)
		assert.False(t, x.Failed())
	})
}

// movingJobs is a store on which every MarkCancel finds the position moved: a
// poll lands between each read and each compare-and-set, the storm the retry
// bound exists for, made deterministic.
type movingJobs struct {
	*jobs.Registry
	mu    sync.Mutex
	marks int
}

func (m *movingJobs) MarkCancel(namespace, id string, atPoll int) (jobs.Job, jobs.MarkOutcome) {
	m.mu.Lock()
	m.marks++
	m.mu.Unlock()
	m.Advance(namespace, id, atPoll)
	return m.Registry.MarkCancel(namespace, id, atPoll)
}

// A cancel whose position keeps moving gives up after a bounded number of
// tries: the vendor's 500, the loop-exhausted error, and nothing recorded.
func TestACancelGivesUpWhenThePositionKeepsMoving(t *testing.T) {
	t.Parallel()

	store := &movingJobs{Registry: jobs.NewRegistry(jobs.Limits{})}
	seedJob(t, store, "job_a")
	x := directExchange(t, store, `
version: 1
name: n
providers:
  acme:
    cancel:
      turns:
        - respond: {status: cancelled}
    turns:
      - respond: {status: running}
`, acmeCancelRoute)
	require.True(t, ResolveJob(x, "job_a"))

	outcome, turn, _ := acmeCancel(x)
	assert.Equal(t, CancelFailed, outcome)
	assert.Nil(t, turn)
	assert.True(t, x.HasFinding(CodeJobCancelContended))
	assert.True(t, x.Failed(), "an exhausted cancel is an error")
	// Counted against a literal, not the constant: a bound of one would pass a
	// comparison with itself.
	assert.Equal(t, 8, store.marks, "the loop is bounded at eight tries")
	assert.True(t, x.claimed)
	assert.Equal(t, 1, x.Deps.Faults.(*scriptedFaults).calls, "exactly one attempt was claimed")

	j, _ := store.Lookup(DefaultNamespace, "job_a")
	assert.False(t, j.CancelRequested, "nothing was recorded")
}

// unknownOutcomeJobs is a store whose MarkCancel answers an outcome outside the
// four jobs.Store documents — a broken implementation, which an out-of-tree
// store can be.
type unknownOutcomeJobs struct {
	*jobs.Registry
	outcome jobs.MarkOutcome
	mu      sync.Mutex
	marks   int
}

func (u *unknownOutcomeJobs) MarkCancel(namespace, id string, _ int) (jobs.Job, jobs.MarkOutcome) {
	u.mu.Lock()
	u.marks++
	u.mu.Unlock()
	j, _ := u.Lookup(namespace, id)
	return j, u.outcome
}

// An outcome CancelJob does not know is the store's bug, not contention: it is
// not retried as though a poll had landed, it is reported naming the outcome,
// and nothing is recorded. Before, any unknown outcome was treated as
// position-moved, so a store answering "" drew eight retries and a false "another
// poll landed" diagnosis.
func TestACancelReportsAnOutcomeTheStoreShouldNeverAnswer(t *testing.T) {
	t.Parallel()

	for _, outcome := range []jobs.MarkOutcome{"", "maybe"} {
		t.Run(fmt.Sprintf("%q", outcome), func(t *testing.T) {
			t.Parallel()

			store := &unknownOutcomeJobs{Registry: jobs.NewRegistry(jobs.Limits{}), outcome: outcome}
			seedJob(t, store, "job_a")
			x := directExchange(t, store, runningThenCompleted, acmeCancelRoute)
			require.True(t, ResolveJob(x, "job_a"))

			got, turn, _ := acmeCancel(x)
			assert.Equal(t, CancelFailed, got)
			assert.Nil(t, turn)
			assert.Equal(t, 1, store.marks, "an unknown outcome is not retried")
			require.True(t, x.Failed())
			findings := x.Findings()
			require.Len(t, findings, 1)
			assert.Contains(t, findings[0].Message, fmt.Sprintf("%q", outcome), "the message names the outcome")
			assert.NotContains(t, findings[0].Message, "another poll", "it is not diagnosed as contention")

			j, _ := store.Lookup(DefaultNamespace, "job_a")
			assert.False(t, j.CancelRequested, "nothing was recorded")
		})
	}
}

// --- the store races, made deterministic -------------------------------------
//
// Each double below injects one interleaving at the exact point a real race
// would land, so a test can assert what CancelJob and SelectPollTurn do there
// without depending on a scheduler.

// onceMovingJobs: the FIRST MarkCancel finds that a poll of the job landed
// between the cancel's read and its compare-and-set.
type onceMovingJobs struct {
	*jobs.Registry
	mu    sync.Mutex
	marks int
}

func (o *onceMovingJobs) MarkCancel(namespace, id string, atPoll int) (jobs.Job, jobs.MarkOutcome) {
	o.mu.Lock()
	o.marks++
	first := o.marks == 1
	o.mu.Unlock()
	if first {
		o.Advance(namespace, id, atPoll)
	}
	return o.Registry.MarkCancel(namespace, id, atPoll)
}

// otherCancelWinsJobs: another cancel of the same job wins the compare-and-set
// between this cancel's read and its own.
type otherCancelWinsJobs struct {
	*jobs.Registry
	once sync.Once
}

func (o *otherCancelWinsJobs) MarkCancel(namespace, id string, atPoll int) (jobs.Job, jobs.MarkOutcome) {
	o.once.Do(func() { o.Registry.MarkCancel(namespace, id, atPoll) })
	return o.Registry.MarkCancel(namespace, id, atPoll)
}

// cancelBeforeAdvanceJobs: a cancel is recorded after a poll claimed its index
// and before that poll's Advance lands.
type cancelBeforeAdvanceJobs struct {
	*jobs.Registry
	once sync.Once
}

func (c *cancelBeforeAdvanceJobs) Advance(namespace, id string, i int) (jobs.Job, bool) {
	c.once.Do(func() { c.Registry.MarkCancel(namespace, id, i) })
	return c.Registry.Advance(namespace, id, i)
}

// resetBeforeMark: a reset lands between CancelJob's Lookup and its
// MarkCancel.
type resetBeforeMark struct {
	*jobs.Registry
	marks int
}

func (r *resetBeforeMark) MarkCancel(namespace, id string, atPoll int) (jobs.Job, jobs.MarkOutcome) {
	r.marks++
	r.Reset()
	return r.Registry.MarkCancel(namespace, id, atPoll)
}

// A compare-and-set that loses to a poll is retried from the position the store
// returned, never the stale one, so the retry can find the job terminal by
// then — and a bound of one try would have given up instead.
func TestACancelRetriesFromTheMovedPosition(t *testing.T) {
	t.Parallel()

	t.Run("the moved position is terminal: completion wins", func(t *testing.T) {
		t.Parallel()

		store := &onceMovingJobs{Registry: jobs.NewRegistry(jobs.Limits{})}
		seedJob(t, store, "job_a")
		store.Registry.Advance(DefaultNamespace, "job_a", 0) // one poll served: next is turns[1], running
		x := directExchange(t, store, runningThenCompleted, acmeCancelRoute)
		require.True(t, ResolveJob(x, "job_a"))

		outcome, turn, path := acmeCancel(x)
		assert.Equal(t, CancelTerminal, outcome)
		require.NotNil(t, turn)
		assert.Equal(t, "providers.acme.turns[2]", path, "the re-peek used the moved position, 2")
		assert.Equal(t, 1, store.marks, "the terminal re-peek needs no second compare-and-set")
		j, _ := store.Lookup(DefaultNamespace, "job_a")
		assert.False(t, j.CancelRequested)
	})

	t.Run("the moved position is still running: recorded there", func(t *testing.T) {
		t.Parallel()

		store := &onceMovingJobs{Registry: jobs.NewRegistry(jobs.Limits{})}
		seedJob(t, store, "job_a")
		x := directExchange(t, store, runningThenCompleted, acmeCancelRoute)
		require.True(t, ResolveJob(x, "job_a"))

		outcome, _, path := acmeCancel(x)
		assert.Equal(t, CancelRecorded, outcome)
		assert.Equal(t, "providers.acme.cancel.turns[0]", path)
		j, _ := store.Lookup(DefaultNamespace, "job_a")
		assert.True(t, j.CancelRequested)
		assert.Equal(t, 1, j.CancelAtPoll, "recorded at the moved position, not the one first read")
	})
}

// A cancel that loses the compare-and-set to another cancel answers as a repeat
// of the winner — from the record the store returned, at the winner's position —
// and does not claim it recorded anything.
func TestACancelThatLosesToAnotherCancelAnswersAsARepeat(t *testing.T) {
	t.Parallel()

	store := &otherCancelWinsJobs{Registry: jobs.NewRegistry(jobs.Limits{})}
	seedJob(t, store, "job_a")
	store.Registry.Advance(DefaultNamespace, "job_a", 0) // Polls 1, so a stale record would peek cancel.turns[1]
	x := directExchange(t, store, runningThenCompleted, acmeCancelRoute)
	require.True(t, ResolveJob(x, "job_a"))

	outcome, _, path := acmeCancel(x)
	assert.Equal(t, CancelAlreadyCancelling, outcome)
	assert.Equal(t, "providers.acme.cancel.turns[0]", path, "Polls 1 minus the winner's position 1")
	j, _ := store.Lookup(DefaultNamespace, "job_a")
	assert.Equal(t, 1, j.CancelAtPoll)
}

// A poll reads the job's record from its own Advance, after its claim, so a
// cancel recorded between the two is seen: this poll is served from the cancel
// script, as every poll at or past the cancel's position must be.
func TestAPollSeesACancelRecordedBetweenItsClaimAndItsAdvance(t *testing.T) {
	t.Parallel()

	store := &cancelBeforeAdvanceJobs{Registry: jobs.NewRegistry(jobs.Limits{})}
	seedJob(t, store, "job_a")
	x := directExchange(t, store, runningThenCompleted, acmePollRoute)
	require.True(t, ResolveJob(x, "job_a"))

	turn, path := acmePoll(x)
	require.NotNil(t, turn)
	assert.Equal(t, "providers.acme.cancel.turns[0]", path)
}

// TestCancelJobClaimsExactlyOnceOnEveryPath: whatever CancelJob decides, it has
// drawn exactly one attempt from the cancel lane when it returns — counted on
// the fault plan itself, not inferred from an index an unclaimed exchange would
// also report as 0. Over HTTP Handle would claim a fault-eligible response's
// attempt anyway, so this is the test that sees a claim CancelJob skipped.
func TestCancelJobClaimsExactlyOnceOnEveryPath(t *testing.T) {
	t.Parallel()

	const noCancelScript = `
version: 1
name: n
providers:
  acme:
    turns:
      - respond: {status: running}
`
	const runsOut = `
version: 1
name: n
providers:
  acme:
    cancel:
      turns:
        - respond: {status: cancelled}
    turns:
      - when: {call_index: 0}
        respond: {status: running}
`
	// alwaysRunning never turns terminal, so a position that keeps moving keeps
	// the cancel retrying until it gives up.
	const alwaysRunning = `
version: 1
name: n
providers:
  acme:
    cancel:
      turns:
        - respond: {status: cancelled}
    turns:
      - respond: {status: running}
`
	tests := []struct {
		name    string
		src     string
		store   func() jobs.Store
		prepare func(jobs.Store)
		want    CancelOutcome
	}{
		{name: "recorded", src: runningThenCompleted, want: CancelRecorded},
		{
			name: "terminal", src: runningThenCompleted, want: CancelTerminal,
			prepare: func(s jobs.Store) {
				s.Advance(DefaultNamespace, "job_a", 0)
				s.Advance(DefaultNamespace, "job_a", 1)
			},
		},
		{
			name: "already cancelling", src: runningThenCompleted, want: CancelAlreadyCancelling,
			prepare: func(s jobs.Store) { s.MarkCancel(DefaultNamespace, "job_a", 0) },
		},
		{name: "unscripted: no cancel script", src: noCancelScript, want: CancelFailed},
		{
			name: "unscripted at the first peek", src: runsOut, want: CancelFailed,
			prepare: func(s jobs.Store) { s.Advance(DefaultNamespace, "job_a", 1) },
		},
		{
			name: "not found", src: runningThenCompleted, want: CancelNotFound,
			prepare: func(s jobs.Store) { s.Reset() },
		},
		{
			name: "contended", src: alwaysRunning, want: CancelFailed,
			store: func() jobs.Store { return &movingJobs{Registry: jobs.NewRegistry(jobs.Limits{})} },
		},
		{
			name: "an unknown store outcome", src: runningThenCompleted, want: CancelFailed,
			store: func() jobs.Store { return &unknownOutcomeJobs{Registry: jobs.NewRegistry(jobs.Limits{})} },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var store jobs.Store = jobs.NewRegistry(jobs.Limits{})
			if tc.store != nil {
				store = tc.store()
			}
			seedJob(t, store, "job_a")
			x := directExchange(t, store, tc.src, acmeCancelRoute)
			require.True(t, ResolveJob(x, "job_a"))
			if tc.prepare != nil {
				tc.prepare(store)
			}

			outcome, _, _ := acmeCancel(x)
			assert.Equal(t, tc.want, outcome)
			assert.True(t, x.claimed)
			assert.Equal(t, 1, x.Deps.Faults.(*scriptedFaults).calls, "exactly one attempt drawn")
		})
	}
}

// A cancel racing a storm of polls on one job: it records, Polls never
// decreases and ends equal to the number of polls served, and exactly the polls
// at or past the recorded position are answered from the cancel script.
func TestACancelRacingAPollStorm(t *testing.T) {
	t.Parallel()

	const polls = 40
	store := jobs.NewRegistry(jobs.Limits{})
	w := newAsyncWorldWith(t, `
version: 1
name: storm
providers:
  acme:
    cancel:
      turns:
        - respond: {status: cancelled}
    turns:
      - respond: {status: running}
`, store)
	id := w.create()

	start := make(chan struct{})
	done := make(chan struct{})
	var observed sync.WaitGroup
	observed.Add(1)
	go func() {
		defer observed.Done()
		last := 0
		for {
			j, ok := store.Lookup(DefaultNamespace, id)
			if !ok {
				t.Error("the job vanished mid-storm")
				return
			}
			if j.Polls < last {
				t.Errorf("Polls went from %d down to %d", last, j.Polls)
				return
			}
			last = j.Polls
			select {
			case <-done:
				return
			default:
				runtime.Gosched()
			}
		}
	}()

	var wg sync.WaitGroup
	var mu sync.Mutex
	cancelled := 0
	for range polls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, body := w.poll(id)
			assert.Equal(t, http.StatusOK, status)
			if body["status"] == "cancelled" {
				mu.Lock()
				cancelled++
				mu.Unlock()
			}
		}()
	}
	var outcome CancelOutcome
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, body := w.cancel(id)
		outcome = CancelOutcome(fmt.Sprint(body["outcome"]))
	}()
	close(start)
	wg.Wait()
	close(done)
	observed.Wait()

	// The job never turns terminal, so the cancel records: giving up takes
	// eight polls landing inside eight consecutive read-to-compare windows of a
	// few microseconds each, which forty polls over real sockets do not produce.
	require.Equal(t, CancelRecorded, outcome)
	j := w.job(id)
	assert.Equal(t, polls, j.Polls)
	require.True(t, j.CancelRequested)
	// Every poll at or past the recorded position — and only those — was
	// answered from the cancel script: the invariant the claim-then-Advance
	// order exists for.
	assert.Equal(t, polls-j.CancelAtPoll, cancelled, "CancelAtPoll %d", j.CancelAtPoll)
}

// --- scripts that are not an entry --------------------------------------------

// SelectPollTurn and CancelJob take a poll script and its cancel block, not a
// provider entry, so a profile can serve a lifecycle nested inside an entry — a
// background block under an agent entry, say — without faking an entry to hold
// it. base is that block's YAML path, and it addresses every path they return
// and every finding they raise.
func TestPollAndCancelServeAScriptNestedInAnEntry(t *testing.T) {
	t.Parallel()

	const base = "providers.acme.background"
	e := mustScenario(t, runningThenCompleted).Provider("acme")
	store := jobs.NewRegistry(jobs.Limits{})
	seedJob(t, store, "job_a")

	x := directExchange(t, store, runningThenCompleted, acmeCancelRoute)
	require.True(t, ResolveJob(x, "job_a"))
	outcome, _, path := CancelJob(x, base, e.Turns, e.Cancel, acmeAsyncPollKey, acmeTerminal)
	assert.Equal(t, CancelRecorded, outcome)
	assert.Equal(t, base+".cancel.turns[0]", path)

	poll := directExchange(t, store, runningThenCompleted, acmePollRoute)
	require.True(t, ResolveJob(poll, "job_a"))
	_, path = SelectPollTurn(poll, base, e.Turns, e.Cancel)
	assert.Equal(t, base+".cancel.turns[0]", path)

	// A block with no cancel script cannot answer a cancelled job's poll, and the
	// finding says which block.
	poll = directExchange(t, store, runningThenCompleted, acmePollRoute)
	require.True(t, ResolveJob(poll, "job_a"))
	turn, _ := SelectPollTurn(poll, base, e.Turns, nil)
	assert.Nil(t, turn)
	require.Len(t, poll.Findings(), 1)
	assert.Equal(t, CodeNoMatchingTurn, poll.Findings()[0].Code)
	assert.Contains(t, poll.Findings()[0].Message, base+".cancel.turns")
}
