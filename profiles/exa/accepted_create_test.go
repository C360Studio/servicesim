package exa

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/testkit"
)

// These tests are the evidence for "an async create was accepted and its reply
// was lost" (issue #7), over real sockets: only a real server can abort a
// connection, and only a real client can tell "I got an error" from "I got an
// identifier". Every wait is the journal's bounded AwaitRequests; nothing here
// sleeps.

// acceptedYAML is an async Exa entry whose create plan is attempts. The poll
// script is the shortest one that proves a job is real: running, then completed.
func acceptedYAML(attempts string) string {
	return `
version: 1
name: accepted-create
providers:
  exa_agent_runs:
    create:
      fault: {attempts: [` + attempts + `]}
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - respond: {status: completed, cost_dollars: {total: 0.01}}
`
}

// startAccepted runs a real Exa listener over acceptedYAML(attempts). Delays are
// skipped because no scenario here declares one; the cost is none.
func startAccepted(t *testing.T, attempts string) *testkit.Sim {
	t.Helper()
	return testkit.Start(t,
		testkit.WithProfiles(Profile()),
		testkit.WithScenarioYAML(acceptedYAML(attempts)),
		testkit.WithSkippedDelays())
}

// createResult is everything the application under test learns from one create.
type createResult struct {
	status     int
	statusLine string // "HTTP/1.1 504 Gateway Timeout"; empty when no response arrived
	header     http.Header
	body       []byte
	err        error // a transport failure, or a body read that was cut short
}

// received is every byte of the response the client could read, as one string:
// the status line, every header, and the body. A test that wants to prove the
// client cannot learn a job's identifier searches this for the WHOLE identifier,
// so it catches the id in a header or an error envelope, not just in an `id` field.
func (r createResult) received() string {
	var b strings.Builder
	b.WriteString(r.statusLine)
	b.WriteByte('\n')
	for name, values := range r.header {
		for _, v := range values {
			b.WriteString(name + ": " + v + "\n")
		}
	}
	b.Write(r.body)
	return b.String()
}

// heldID is what a create-then-poll client does: it takes the identifier from a
// complete response with a status below 400 and a JSON body carrying "id", and
// from nothing else. "" means the client does not know the run it started.
func (r createResult) heldID() string {
	if r.err != nil || r.status >= http.StatusBadRequest {
		return ""
	}
	var out struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(r.body, &out) != nil {
		return ""
	}
	return out.ID
}

// postCreate sends one POST /agent/runs to base, which is a listener URL or a
// namespace's.
func postCreate(t *testing.T, client *http.Client, base string) createResult {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/agent/runs",
		strings.NewReader(`{"query":"find the finding"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")

	resp, err := client.Do(req)
	if err != nil {
		return createResult{err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	return createResult{
		status:     resp.StatusCode,
		statusLine: resp.Proto + " " + resp.Status,
		header:     resp.Header.Clone(),
		body:       body,
		err:        err,
	}
}

// getRun polls base's run id and returns the status and the decoded body.
func getRun(t *testing.T, client *http.Client, base, id string) (int, map[string]any) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/agent/runs/"+id, nil)
	require.NoError(t, err)
	req.Header.Set("x-api-key", "test-key")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return resp.StatusCode, out
}

// jobIDs lists the ids of jobs in their declared order.
func jobIDs(jobs []testkit.Job) []string {
	ids := make([]string, len(jobs))
	for i, j := range jobs {
		ids[i] = j.ID
	}
	return ids
}

// TestAcceptedCreateLosesItsReplyButKeepsExactlyOneJob is the issue's headline
// case: the simulator saves one job, the connection dies before a byte of the
// response, and the application is left with an error and no identifier. The
// evidence that the run exists is the simulator's own: the job registry and the
// journal's create entry, which agree on the attempt that did it.
func TestAcceptedCreateLosesItsReplyButKeepsExactlyOneJob(t *testing.T) {
	t.Parallel()

	sim := startAccepted(t, `{kind: close_before_headers, accepted: true}`)
	client := sim.Client()

	got := postCreate(t, client, sim.URL(Name))
	require.Error(t, got.err, "the client must see a connection failure, got status %d", got.status)
	require.Empty(t, got.heldID(), "the application must not learn the run it started")

	// The aborted entry is journaled before the socket is destroyed, but the
	// client can observe the reset first; the bounded wait is the documented way
	// to read it.
	entries := sim.AwaitRequests(t, Name, 1)
	create := entries[0]
	assert.Equal(t, "/agent/runs", create.Path)
	assert.Equal(t, "exa:agent_runs.create", create.Outcome.FaultKey)
	assert.Equal(t, 0, create.Outcome.AttemptIndex)
	assert.Equal(t, string(scenario.FaultCloseBeforeHeaders), create.Outcome.FaultKind)
	assert.True(t, create.Outcome.Aborted, "the reply was lost, and the journal says so")
	assert.Empty(t, create.Errors(), "an accepted create is a scripted fault, not a simulator error")

	// Exactly one job, and it is the one the aborted create minted: the evidence
	// matches on namespace, entry and create index, never on the identifier the
	// client lacks.
	jobs := sim.Jobs()
	require.Len(t, jobs, 1)
	assert.Equal(t, NameAgentRuns, jobs[0].Entry)
	assert.Equal(t, provider.DefaultNamespace, jobs[0].Namespace)
	assert.Equal(t, create.Outcome.AttemptIndex, jobs[0].CreateIndex)

	// The job is real, not a placeholder: a poll of the listed identifier resolves.
	// Only a test controller can do this — the application never held the id.
	status, run := getRun(t, client, sim.URL(Name), jobs[0].ID)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, jobs[0].ID, run["id"])
	assert.Equal(t, statusRunning, run["status"])
}

// TestTheSameAttemptWithoutAcceptedLeavesNoJob is the explicit counterpart, in
// the same shape: the request never took effect, so there is nothing to orphan.
// The two tests differ in one word, which is what lets a consumer tell the cases
// apart.
func TestTheSameAttemptWithoutAcceptedLeavesNoJob(t *testing.T) {
	t.Parallel()

	sim := startAccepted(t, `{kind: close_before_headers}`)

	got := postCreate(t, sim.Client(), sim.URL(Name))
	require.Error(t, got.err)

	create := sim.AwaitRequests(t, Name, 1)[0]
	assert.Equal(t, string(scenario.FaultCloseBeforeHeaders), create.Outcome.FaultKind)
	assert.True(t, create.Outcome.Aborted)
	assert.Empty(t, sim.Jobs(), "a create rejected before acceptance leaves no job")
}

// The byte layout of Exa's create body, which the table below and the schema
// documentation both quote. The create is derived in full and cannot be scripted,
// so these are constants of the wire shape, and
// TestTheDocumentedExaCreateBodyLayoutHolds fails if one moves.
const (
	createBodyBytes = 390 // the whole create body
	idOffset        = 7   // `{"id":"` — the id is the body's first key
	idBytes         = 42  // "agent_run_" plus 32 hex characters
	idEnd           = idOffset + idBytes
)

// whatTheClientLearns is how much of the accepted job's identifier the application
// ends up holding for one shape.
type whatTheClientLearns int

const (
	nothing    whatTheClientLearns = iota // the id appears nowhere in what it received
	idInPrefix                            // the whole id is in the bytes received, but the body does not decode
	wholeID                               // a complete, ordinary response: it holds the id and can poll it
)

// TestEveryNonDeliveringShapeKeepsAJobUnderAccepted runs each shape with and
// without the modifier, through a real client: the job is kept exactly when
// `accepted` says so. Together with the delivery-table tests in provider this is
// what makes "accepted" a statement about the request and not about a kind.
//
// `accepted` keeps the JOB; whether the client learns the id depends on the shape,
// so each row says which, and the table pins it. Hidden is searched for as the
// WHOLE job id in everything the client received — status line, every header
// (x-request-id included) and the body — not just in an `id` field: a status-shaped
// fault is the one shape where a full error response arrives, and an envelope or
// request id that reused the run's id would hand the application the very
// identifier the scenario says it lost. truncate_body is the shape that does NOT
// hide it by default: it sends a prefix of the rendered body, the id is that body's
// first key, so only a cut inside the id withholds it. The id searched for is one
// the registry lists and a poll of it resolves, so an absent id cannot be a vacuous
// pass over a job that never existed.
func TestEveryNonDeliveringShapeKeepsAJobUnderAccepted(t *testing.T) {
	t.Parallel()

	truncate := func(n int) string { return fmt.Sprintf("kind: truncate_body, truncate_after_bytes: %d", n) }
	shapes := []struct {
		name, attempt string
		responds      bool // a response, partial or complete, reaches the client
		learns        whatTheClientLearns
	}{
		{"close_before_headers", `kind: close_before_headers`, false, nothing},
		// 8 < idEnd: the cut falls inside the id, after `{"id":"a`, so the id is
		// withheld. This is the hiding case; a default or larger cut is not.
		{"truncate_body cut at 8 bytes", truncate(8), true, nothing},
		{"truncate_body cut one byte short of the id's end", truncate(idEnd - 1), true, nothing},
		{"truncate_body cut at the id's last byte", truncate(idEnd), true, idInPrefix},
		{"truncate_body with the default cut, half the body", `kind: truncate_body`, true, idInPrefix},
		{"truncate_body cut at the body's length", truncate(createBodyBytes), true, wholeID},
		{"truncate_body cut past the body's length", truncate(createBodyBytes * 10), true, wholeID},
		{"empty_body", `kind: empty_body`, true, nothing},
		{"invalid_json", `kind: invalid_json, raw_body: "not json"`, true, nothing},
		{"a 504 status", `status: 504`, true, nothing},
		{"a body override", `body: {scripted: true}`, true, nothing},
	}
	for _, shape := range shapes {
		for _, accepted := range []bool{true, false} {
			attempt, name := shape.attempt, shape.name+" rejected"
			if accepted {
				attempt, name = shape.attempt+", accepted: true", shape.name+" accepted"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				sim := startAccepted(t, "{"+attempt+"}")

				got := postCreate(t, sim.Client(), sim.URL(Name))
				if shape.learns != wholeID {
					require.Empty(t, got.heldID(), "the client must not hold a usable identifier (status %d, err %v)", got.status, got.err)
				}
				if shape.responds {
					require.NotEmpty(t, got.statusLine, "a response should have reached the client (err %v)", got.err)
				} else {
					require.Error(t, got.err)
				}

				sim.AwaitRequests(t, Name, 1)
				if !accepted {
					assert.Empty(t, sim.Jobs(), "without accepted nothing took effect")
					return
				}

				jobs := sim.Jobs()
				require.Len(t, jobs, 1, "accepted keeps the job whose reply was lost")
				id := jobs[0].ID
				require.NotEmpty(t, id)

				// The job is real, so its id is worth hiding.
				status, run := getRun(t, sim.Client(), sim.URL(Name), id)
				require.Equal(t, http.StatusOK, status)
				require.Equal(t, id, run["id"])

				switch shape.learns {
				case nothing:
					assert.NotContains(t, got.received(), id,
						"the client must not be able to read the accepted job's identifier from anything it received")
				case idInPrefix:
					assert.Contains(t, string(got.body), id,
						"a truncation that reaches the end of the id still delivers all of it: the job is kept AND the client holds the id")
				case wholeID:
					assert.Equal(t, id, got.heldID(),
						"a truncation at or past the body's length is a complete response: no reply was lost")
				}
				if shape.name == "a 504 status" {
					// The one shape with a full error response: make sure the check above
					// had a request id and an envelope to find the id in.
					assert.NotEmpty(t, got.header.Get("x-request-id"))
					assert.NotEmpty(t, got.body)
				}
			})
		}
	}
}

// TestTheDocumentedExaCreateBodyLayoutHolds pins the byte numbers the table above
// and docs/scenario-schema.md quote for truncate_body: the create body is 390
// bytes, opens with `{"id":"`, and carries a 42-character identifier at byte 7, so
// the identifier ends at byte 49 and the default cut (half the body, 195) is well
// past it. If the wire shape moves, this fails and the documented numbers must move
// with it.
func TestTheDocumentedExaCreateBodyLayoutHolds(t *testing.T) {
	t.Parallel()

	sim := startAccepted(t, `{status: 200}`)
	got := postCreate(t, sim.Client(), sim.URL(Name))
	require.Equal(t, http.StatusOK, got.status)

	id := got.heldID()
	require.NotEmpty(t, id)
	assert.Len(t, got.body, createBodyBytes)
	assert.Len(t, id, idBytes)
	assert.Equal(t, `{"id":"`, string(got.body[:idOffset]))
	assert.Equal(t, idOffset, bytes.Index(got.body, []byte(id)))
	assert.GreaterOrEqual(t, createBodyBytes/2, idEnd, "the default cut must land past the id's end")
}

// TestAcceptedStaysAnErrorOnADeliveringShape pins the other direction through the
// loader: an attempt whose client does receive the identifier gains nothing from
// the modifier, and saying otherwise is a load error rather than a quiet no-op.
func TestAcceptedStaysAnErrorOnADeliveringShape(t *testing.T) {
	t.Parallel()

	for _, attempt := range []string{
		`{accepted: true}`,
		`{status: 201, accepted: true}`,
		`{delay: 1s, accepted: true}`,
		`{kind: extra_fields, extra_fields: {x: 1}, accepted: true}`,
		`{kind: stream_disconnect, accepted: true}`,
	} {
		t.Run(attempt, func(t *testing.T) {
			t.Parallel()

			_, report, err := scenario.Parse([]byte(acceptedYAML(attempt)))
			require.Error(t, err)
			var codes []string
			for _, f := range report.Findings {
				codes = append(codes, f.Code)
			}
			assert.Contains(t, codes, scenario.CodeAcceptedRedundant)
		})
	}
}

// TestARetryAfterAnAcceptedCreateMintsASecondJob is the no-invented-idempotency
// rule: neither Agent API documents an idempotency key, so the client's retry
// claims the next attempt and creates a NEW run. Both runs exist; the client
// knows only the second. The repeat form proves each lost attempt orphans one.
func TestARetryAfterAnAcceptedCreateMintsASecondJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		attempts string
		lost     int
	}{
		{"one lost reply", `{kind: close_before_headers, accepted: true}`, 1},
		{"two lost replies", `{kind: close_before_headers, accepted: true, repeat: 2}`, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sim := startAccepted(t, tc.attempts)
			client := sim.Client()

			for range tc.lost {
				require.Error(t, postCreate(t, client, sim.URL(Name)).err)
			}
			retry := postCreate(t, client, sim.URL(Name))
			require.NoError(t, retry.err)
			held := retry.heldID()
			require.NotEmpty(t, held, "the retry is served: attempt %d is past the plan", tc.lost)

			entries := sim.AwaitRequests(t, Name, tc.lost+1)
			last := entries[len(entries)-1]
			assert.Equal(t, tc.lost, last.Outcome.AttemptIndex, "the retry claims the next attempt")
			assert.Empty(t, last.Outcome.FaultKind, "and is not faulted")

			jobs := sim.Jobs()
			require.Len(t, jobs, tc.lost+1, "every lost reply left a job, and the retry made another")
			ids := jobIDs(jobs)
			assert.Len(t, uniqueStrings(ids), len(ids), "identifiers are distinct: %v", ids)
			assert.Equal(t, held, jobs[tc.lost].ID, "the client holds only the last run's identifier")
			assert.NotContains(t, ids[:tc.lost], held)
		})
	}
}

// uniqueStrings returns the distinct members of in.
func uniqueStrings(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		out[s] = struct{}{}
	}
	return out
}

// TestAcceptedCreatesAreIsolatedByNamespace runs one script in two namespaces.
// Each namespace draws attempt 0 from its own cursor, so each loses its first
// reply and keeps its own job; a namespace's retry neither consumes nor sees the
// other's budget or jobs.
func TestAcceptedCreatesAreIsolatedByNamespace(t *testing.T) {
	t.Parallel()

	sim := startAccepted(t, `{kind: close_before_headers, accepted: true}`)
	a, b := sim.Namespace(t, "ns-a"), sim.Namespace(t, "ns-b")

	require.Error(t, postCreate(t, a.Client(), a.URL(Name)).err)
	require.Error(t, postCreate(t, b.Client(), b.URL(Name)).err,
		"a shared cursor would have served this as attempt 1, past the plan")
	a.AwaitRequests(t, Name, 1)
	b.AwaitRequests(t, Name, 1)

	require.Len(t, a.Jobs(), 1)
	require.Len(t, b.Jobs(), 1)
	assert.Equal(t, 0, a.Requests(Name)[0].Outcome.AttemptIndex)
	assert.Equal(t, 0, b.Requests(Name)[0].Outcome.AttemptIndex)

	retry := postCreate(t, a.Client(), a.URL(Name))
	require.NotEmpty(t, retry.heldID())
	a.AwaitRequests(t, Name, 2)
	assert.Len(t, a.Jobs(), 2, "the retry in a mints a second job in a")
	assert.Len(t, b.Jobs(), 1, "and leaves b's alone")
}

// TestResetDropsAnAcceptedJobWithItsCursors: a reset that rewound the fault
// cursors without dropping the job would have the next create re-mint the same
// derived identifier and collide with the orphan. Jobs drop with the cursors, so
// the script starts over, the create is lost again, and the identifier is the
// one the first run derived — which is also what determinism requires.
func TestResetDropsAnAcceptedJobWithItsCursors(t *testing.T) {
	t.Parallel()

	sim := startAccepted(t, `{kind: close_before_headers, accepted: true}`)

	require.Error(t, postCreate(t, sim.Client(), sim.URL(Name)).err)
	sim.AwaitRequests(t, Name, 1)
	first := jobIDs(sim.Jobs())
	require.Len(t, first, 1)

	sim.Reset()
	require.Empty(t, sim.Jobs(), "a reset drops the accepted job")

	require.Error(t, postCreate(t, sim.Client(), sim.URL(Name)).err, "the script starts over: attempt 0 again")
	again := sim.AwaitRequests(t, Name, 1)
	assert.Empty(t, again[0].Errors(), "no job.id_collision: the record went with the cursor")
	assert.Equal(t, first, jobIDs(sim.Jobs()), "the same script derives the same identifier")
}

// TestAcceptedJobIdentifiersAreIdenticalAcrossRuns is house rule 2 applied to the
// new path: two independent simulators given the same scenario and the same
// requests mint the same identifiers, in the same order.
func TestAcceptedJobIdentifiersAreIdenticalAcrossRuns(t *testing.T) {
	t.Parallel()

	run := func() []string {
		sim := startAccepted(t, `{kind: close_before_headers, accepted: true}`)
		require.Error(t, postCreate(t, sim.Client(), sim.URL(Name)).err)
		require.NotEmpty(t, postCreate(t, sim.Client(), sim.URL(Name)).heldID())
		sim.AwaitRequests(t, Name, 2)
		return jobIDs(sim.Jobs())
	}

	first, second := run(), run()
	require.Len(t, first, 2)
	assert.Equal(t, first, second)
}

// TestAnAcceptedAttemptInAPollPlanIsReportedAndStillApplies is the runtime half
// of placement: load allows `accepted` under any plan, and only the request that
// claims it knows whether it can mint. A create plan raises nothing; the same
// modifier in a poll plan raises fault.accepted_unreachable on the poll that
// claimed it, while the scripted 503 is served exactly as it would be without it.
func TestAnAcceptedAttemptInAPollPlanIsReportedAndStillApplies(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithProfiles(Profile()),
		testkit.WithSkippedDelays(),
		testkit.WithScenarioYAML(`
version: 1
name: accepted-in-a-poll-plan
providers:
  exa_agent_runs:
    create:
      fault: {attempts: [{kind: close_before_headers, accepted: true}, {}]}
    turns:
      - when: {call_index: 0}
        fault: {attempts: [{status: 503, accepted: true}]}
        respond: {status: running}
      - respond: {status: completed, cost_dollars: {total: 0.01}}
`))
	client := sim.Client()

	require.Error(t, postCreate(t, client, sim.URL(Name)).err)
	id := postCreate(t, client, sim.URL(Name)).heldID()
	require.NotEmpty(t, id)

	status, _ := getRun(t, client, sim.URL(Name), id)
	assert.Equal(t, http.StatusServiceUnavailable, status, "the attempt still applies as an ordinary fault")
	status, run := getRun(t, client, sim.URL(Name), id)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, statusCompleted, run["status"], "the next poll draws attempt 1 and the script's second snapshot")

	entries := sim.AwaitRequests(t, Name, 4)
	var carrying []int
	for i, e := range entries {
		for _, f := range e.Findings {
			if f.Code == provider.CodeAcceptedUnreachable {
				carrying = append(carrying, i)
				assert.Equal(t, "error", string(f.Severity), "a modifier that cannot apply is an authoring error")
			}
		}
	}
	assert.Equal(t, []int{2}, carrying,
		"only the first poll, which claimed the accepted attempt, carries the finding: not the lost create "+
			"(which mints), the retried create, or the second poll")
}
