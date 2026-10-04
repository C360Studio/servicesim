package perplexity

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/testkit"
)

// backgroundScenario scripts a background run that is queued on its first
// retrieve, in progress on its second and completed, with scripted usage and
// cost, from its third on. Its synchronous turn is a single unconditional
// answer, so a synchronous call against the same entry still works.
const backgroundScenario = `
version: 1
name: background-lifecycle
time:
  base: 2026-01-01T00:00:00Z
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  perplexity_agent:
    answer: A synchronous answer.
    background:
      turns:
        - when: {call_index: 0}
          respond: {status: queued}
        - when: {call_index: 1}
          respond: {status: in_progress}
        - respond:
            status: completed
            model: openai/gpt-5
            answer: Report A finds that deterministic simulation removes flakiness.
            queries: [deterministic simulator adapter tests]
            search_results:
              - source: source-a
                snippet: Report A finds that deterministic simulators remove flakiness.
                date: "2025-11-16"
            usage:
              input_tokens: 42
              output_tokens: 128
              cost:
                input_cost: 0.00021
                output_cost: 0.00128
                total_cost: 0.00149
`

// backgroundRequest asks for a background run.
const backgroundRequest = `{"input":"what does report A find?","model":"openai/gpt-5","background":true}`

// startBackground runs a real Perplexity listener, with a job store, over src.
func startBackground(t *testing.T, src string) *testkit.Sim {
	t.Helper()
	return testkit.Start(t,
		testkit.WithProfiles(Profile()),
		testkit.WithScenarioYAML(src),
		testkit.WithSkippedDelays())
}

// bgReply is everything a client received from one call.
type bgReply struct {
	status int
	header http.Header
	body   []byte
}

// json decodes the reply as a JSON object.
func (r bgReply) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(r.body, &out), "body: %s", r.body)
	return out
}

// bgSend issues one request against base with the test bearer credential unless
// header carries an Authorization of its own or noAuth is set.
func bgSend(t *testing.T, sim *testkit.Sim, base, method, path, body string, header map[string]string, noAuth bool) bgReply {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, reader)
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if !noAuth {
		req.Header.Set("Authorization", "Bearer "+testKey)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := sim.Client().Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return bgReply{status: resp.StatusCode, header: resp.Header, body: data}
}

// bgCreate creates a background run on base and returns its id.
func bgCreate(t *testing.T, sim *testkit.Sim, base string) string {
	t.Helper()
	r := bgSend(t, sim, base, http.MethodPost, "/v1/agent", backgroundRequest, nil, false)
	require.Equal(t, http.StatusOK, r.status, "create: %s", r.body)
	id, _ := r.json(t)["id"].(string)
	require.NotEmpty(t, id)
	return id
}

// bgRetrieve retrieves id on base.
func bgRetrieve(t *testing.T, sim *testkit.Sim, base, id string) bgReply {
	t.Helper()
	return bgSend(t, sim, base, http.MethodGet, "/v1/agent/"+id, "", nil, false)
}

// statusIn is the status a 200 snapshot reports.
func statusIn(t *testing.T, r bgReply) string {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
	s, _ := r.json(t)["status"].(string)
	return s
}

// onRoute filters journal entries to one route pattern.
func onRoute(entries []journal.Entry, pattern string) []journal.Entry {
	var out []journal.Entry
	for _, e := range entries {
		if e.Route == pattern {
			out = append(out, e)
		}
	}
	return out
}

// codesOf lists an entry's finding codes.
func codesOf(e journal.Entry) []string {
	out := make([]string, 0, len(e.Findings))
	for _, f := range e.Findings {
		out = append(out, f.Code)
	}
	return out
}

// TestRetrieveRouteIsSeparateFromTheCreateSpellings pins the retrieve route's
// registration: its own fault key and a per-job lane, served from the Agent
// entry, and NOT among the routes a synchronous turn's when.route is checked
// against — listing it there would let a create turn name it, load clean and
// never fire.
func TestRetrieveRouteIsSeparateFromTheCreateSpellings(t *testing.T) {
	t.Parallel()

	var retrieve *provider.Route
	for _, r := range Routes() {
		if r.Pattern == "GET /v1/agent/{id}" {
			retrieve = &r
		}
	}
	require.NotNil(t, retrieve, "GET /v1/agent/{id} is not registered")
	assert.Equal(t, "perplexity:agent.retrieve", retrieve.FaultKey)
	assert.Equal(t, NameAgent, retrieve.Entry)
	assert.Equal(t, []string{provider.LaneFromPath + "id"}, retrieve.LaneFrom)
	assert.NotNil(t, retrieve.Fault)
	assert.Contains(t, handlers(), retrieve.Pattern)

	for _, r := range (agentValidator{}).Routes() {
		assert.NotEqual(t, retrieve.FaultKey, r.FaultKey, "the retrieve key must not be a create turn's route")
	}
	assert.Equal(t, []string{NameAgent}, Profile().Backgroundable)
	assert.Equal(t, []string{NameAgent}, Profile().BackgroundCancellable, "the Agent entry serves background.cancel")
	assert.NotContains(t, Profile().Cancellable, NameAgent, "Perplexity's cancel is not an entry-level cancel:")
}

// TestBackgroundCancelLoadsOnTheAgentEntry: the framework rejects a cancel:
// under background: unless the profile opts the entry in, and Perplexity's
// Agent entry is opted in, so a scenario scripting one loads through the real
// Set with no finding at all.
func TestBackgroundCancelLoadsOnTheAgentEntry(t *testing.T) {
	t.Parallel()
	findings := bgValidate(t, bgEntry("        - respond: {status: in_progress}\n")+
		"      cancel:\n        fault: {attempts: [{status: 500}, {}]}\n"+
		"        turns:\n          - respond: {status: cancelled}\n")
	assert.Empty(t, findings)
}

// TestBackgroundCreateAnswersTheQueuedStub: a create that asks for the
// background lifecycle mints a job and answers the queued snapshot at once, its
// id the job's.
func TestBackgroundCreateAnswersTheQueuedStub(t *testing.T) {
	t.Parallel()
	sim := startBackground(t, backgroundScenario)

	r := bgSend(t, sim, sim.URL(Name), http.MethodPost, "/v1/agent", backgroundRequest, nil, false)
	require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
	assert.Equal(t, "application/json", r.header.Get("Content-Type"))
	assert.Equal(t, string(goldenBytes(t, "perplexity-agent-background-queued.json")), string(r.body))

	got := r.json(t)
	assert.Equal(t, "queued", got["status"])
	assert.Equal(t, []any{}, got["output"])
	assert.Equal(t, "openai/gpt-5", got["model"], "the create echoes the request-selected model")
	assert.NotContains(t, got, "usage", "an unscripted usage is omitted, never an invented zero")

	jobs := sim.Jobs()
	require.Len(t, jobs, 1)
	assert.Equal(t, got["id"], jobs[0].ID)
	assert.Equal(t, NameAgent, jobs[0].Entry)
	assert.Equal(t, 0, jobs[0].CreateIndex)

	entries := sim.AwaitRequests(t, Name, 1)
	assert.Equal(t, "perplexity.agent.background.created", entries[0].Outcome.Label)
	assert.Equal(t, 0, entries[0].Outcome.AttemptIndex)
	testkit.AssertNoFindings(t, entries[0])
}

// TestBackgroundRetrieveWalksTheScript walks a three-turn script and one poll
// past its end: the job's own id every time, the scripted status, output only
// once the run answers, usage only where the scenario scripts it, and a
// terminal snapshot that stays terminal.
func TestBackgroundRetrieveWalksTheScript(t *testing.T) {
	t.Parallel()
	sim := startBackground(t, backgroundScenario)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	queued := bgRetrieve(t, sim, base, id)
	assert.Equal(t, "queued", statusIn(t, queued))
	inProgress := bgRetrieve(t, sim, base, id)
	assert.Equal(t, "in_progress", statusIn(t, inProgress))
	completed := bgRetrieve(t, sim, base, id)
	assert.Equal(t, "completed", statusIn(t, completed))
	again := bgRetrieve(t, sim, base, id)
	assert.Equal(t, "completed", statusIn(t, again), "a terminal snapshot is absorbing")

	for i, r := range []bgReply{queued, inProgress, completed, again} {
		got := r.json(t)
		assert.Equal(t, id, got["id"], "retrieve %d: the snapshot's id is the job's", i)
		assert.Equal(t, "response", got["object"], "retrieve %d", i)
		assert.EqualValues(t, 1767225600, got["created_at"], "retrieve %d: the scenario base time", i)
	}
	for i, r := range []bgReply{queued, inProgress} {
		got := r.json(t)
		assert.Equal(t, []any{}, got["output"], "retrieve %d: a pending run with no answer has no output yet", i)
		assert.Equal(t, "servicesim/unscripted", got["model"], "retrieve %d: an unscripted model is the placeholder", i)
		assert.NotContains(t, got, "usage", "retrieve %d: usage is rendered only when scripted", i)
	}
	assert.Equal(t, string(goldenBytes(t, "perplexity-agent-background-completed.json")), string(completed.body))
	assert.Equal(t, string(completed.body), string(again.body), "a poll past the end serves the same snapshot")

	jobs := sim.Jobs()
	require.Len(t, jobs, 1)
	assert.Equal(t, 4, jobs[0].Polls)

	retrieves := onRoute(sim.AwaitRequests(t, Name, 5), "GET /v1/agent/{id}")
	require.Len(t, retrieves, 4)
	assert.Equal(t, []string{
		"perplexity.agent.retrieved.queued", "perplexity.agent.retrieved.in_progress",
		"perplexity.agent.retrieved.completed", "perplexity.agent.retrieved.completed",
	}, []string{
		retrieves[0].Outcome.Label, retrieves[1].Outcome.Label,
		retrieves[2].Outcome.Label, retrieves[3].Outcome.Label,
	})
	for i, e := range retrieves {
		assert.Equal(t, i, e.Outcome.AttemptIndex, "retrieve %d claims its own lane's index", i)
		testkit.AssertNoFindings(t, e)
	}
}

// TestBackgroundSnapshotInventsNoCost: a snapshot that scripts usage but no
// cost renders the token counts and no cost object — ResponsesUsage.cost is
// optional, and a zero the scenario did not script is not a billing fact
// (issue #6). The synchronous path's own rendering is not this test's concern.
func TestBackgroundSnapshotInventsNoCost(t *testing.T) {
	t.Parallel()
	sim := startBackground(t, `
version: 1
name: background-usage-no-cost
providers:
  perplexity_agent:
    answer: A synchronous answer.
    background:
      turns:
        - respond: {status: completed, answer: done, usage: {input_tokens: 3, output_tokens: 4}}
`)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	r := bgRetrieve(t, sim, base, id)
	require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
	assert.Contains(t, string(r.body), `"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}`)
	assert.NotContains(t, string(r.body), "cost")
}

// TestBackgroundSnapshotRendersWhatTheScenarioScripts: a retrieve renders every
// field its snapshot scripts — an absent status as completed, a pending run's
// partial answer, a terminal run's message even with no answer, a scripted
// error, extra_fields — and labels the journal entry with the status the body
// reports. Only queued and in_progress with no answer drop the message item, so
// both edges of that rule are pinned here.
func TestBackgroundSnapshotRendersWhatTheScenarioScripts(t *testing.T) {
	t.Parallel()

	messages := func(got map[string]any) []map[string]any {
		var out []map[string]any
		items, _ := got["output"].([]any)
		for _, it := range items {
			if m, _ := it.(map[string]any); m["type"] == "message" {
				out = append(out, m)
			}
		}
		return out
	}
	textOf := func(t *testing.T, m map[string]any) string {
		t.Helper()
		content, _ := m["content"].([]any)
		require.Len(t, content, 1)
		part, _ := content[0].(map[string]any)
		s, _ := part["text"].(string)
		return s
	}

	tests := []struct {
		name, respond, wantStatus string
		check                     func(t *testing.T, got map[string]any)
	}{
		{"an absent status is completed", "{answer: Done.}", "completed",
			func(t *testing.T, got map[string]any) {
				msgs := messages(got)
				require.Len(t, msgs, 1)
				assert.Equal(t, "Done.", textOf(t, msgs[0]))
			}},
		{"a pending snapshot with an answer renders it", "{status: in_progress, answer: Partial.}", "in_progress",
			func(t *testing.T, got map[string]any) {
				msgs := messages(got)
				require.Len(t, msgs, 1, "a scripted partial answer is not dropped")
				assert.Equal(t, "Partial.", textOf(t, msgs[0]))
				assert.Equal(t, "in_progress", msgs[0]["status"])
			}},
		{"a completed snapshot with no answer still has its message", "{status: completed}", "completed",
			func(t *testing.T, got map[string]any) {
				msgs := messages(got)
				require.Len(t, msgs, 1, "only queued and in_progress omit the message")
				assert.Empty(t, textOf(t, msgs[0]))
			}},
		{"a failed snapshot carries its scripted error",
			"{status: failed, error: {code: run_failed, message: The run failed., type: server_error}}", "failed",
			func(t *testing.T, got map[string]any) {
				assert.Equal(t, map[string]any{"code": "run_failed", "message": "The run failed.", "type": "server_error"},
					got["error"])
			}},
		{"extra_fields reach the snapshot", "{status: completed, answer: a, extra_fields: {vendor_hint: kept}}", "completed",
			func(t *testing.T, got map[string]any) {
				assert.Equal(t, "kept", got["vendor_hint"])
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sim := startBackground(t, `
version: 1
name: background-render
providers:
  perplexity_agent:
    answer: A synchronous answer.
    background:
      turns:
        - respond: `+tc.respond+`
`)
			base := sim.URL(Name)
			id := bgCreate(t, sim, base)
			r := bgRetrieve(t, sim, base, id)
			assert.Equal(t, tc.wantStatus, statusIn(t, r))
			tc.check(t, r.json(t))

			e := sim.AwaitRequests(t, Name, 2)[1]
			assert.Equal(t, "perplexity.agent.retrieved."+tc.wantStatus, e.Outcome.Label)
		})
	}
}

// TestBackgroundRetrieveOfAnythingElseIs404 is every id the retrieve route does
// not answer: one never minted, one minted in another namespace, a synchronous
// response's id (the named divergence, ruling 7), a store:false background id
// (the spec's own 404), and a malformed one. Each is the 404 ErrorInfo, claims
// nothing and advances nothing.
func TestBackgroundRetrieveOfAnythingElseIs404(t *testing.T) {
	t.Parallel()

	sim := startBackground(t, backgroundScenario)
	base := sim.URL(Name)
	other := sim.Namespace(t, "other").URL(Name)

	minted := bgCreate(t, sim, base)
	// A job id is unique within its namespace and deliberately not across
	// them: call 0 of the other namespace mints minted's id again. Its call 1
	// mints an id the default namespace never holds, because the default's
	// call 1 below is synchronous.
	bgCreate(t, sim, other)
	elsewhere := bgCreate(t, sim, other)

	sync := bgSend(t, sim, base, http.MethodPost, "/v1/agent", agentRequest, nil, false)
	require.Equal(t, http.StatusOK, sync.status, "body: %s", sync.body)
	syncID, _ := sync.json(t)["id"].(string)
	require.NotEmpty(t, syncID)

	unstored := bgSend(t, sim, base, http.MethodPost, "/v1/agent",
		`{"input":"q","model":"openai/gpt-5","background":true,"store":false}`, nil, false)
	require.Equal(t, http.StatusOK, unstored.status, "body: %s", unstored.body)
	unstoredID, _ := unstored.json(t)["id"].(string)
	require.NotEmpty(t, unstoredID)

	tests := []struct{ name, id string }{
		{"an id never minted", "resp_00000000000000000000000000000000"},
		{"an id minted in another namespace", elsewhere},
		{"a synchronous response's id", syncID},
		{"a store:false background id", unstoredID},
		{"a malformed id", "resp%2F" + strings.TrimPrefix(minted, "resp_")},
	}
	// The five requests above, waited for rather than counted from a journal
	// read that could run ahead of the last entry (testkit's Sim.Requests).
	before := len(sim.AwaitRequests(t, Name, 5))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := bgRetrieve(t, sim, base, tc.id)
			assert.Equal(t, http.StatusNotFound, r.status, "body: %s", r.body)
			assert.Equal(t, string(goldenBytes(t, "perplexity-agent-retrieve-404.json")), string(r.body))
		})
	}

	entries := sim.AwaitRequests(t, Name, before+len(tests))[before:]
	for i, e := range entries {
		assert.Equal(t, -1, e.Outcome.AttemptIndex, "%s claims nothing", tests[i].name)
		assert.Equal(t, "perplexity.agent.error.404", e.Outcome.Label, tests[i].name)
	}
	for _, j := range sim.Jobs() {
		assert.Zero(t, j.Polls, "job %s was never polled", j.ID)
	}
}

// TestBackgroundHeadClaimsNothing pins the HEAD refusal. Go's ServeMux delivers
// HEAD to a GET pattern, so without its own branch a HEAD would claim the job's
// next retrieve and advance its poll position for a body net/http discards.
func TestBackgroundHeadClaimsNothing(t *testing.T) {
	t.Parallel()
	sim := startBackground(t, backgroundScenario)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	assert.Equal(t, "queued", statusIn(t, bgRetrieve(t, sim, base, id)))

	head := bgSend(t, sim, base, http.MethodHead, "/v1/agent/"+id, "", nil, false)
	assert.Equal(t, http.StatusMethodNotAllowed, head.status)
	assert.Equal(t, http.MethodGet, head.header.Get("Allow"))
	assert.Equal(t, 1, sim.Jobs()[0].Polls, "a HEAD must not advance the job")

	assert.Equal(t, "in_progress", statusIn(t, bgRetrieve(t, sim, base, id)),
		"the retrieve after a HEAD is the job's second, not its third")

	heads := sim.AwaitRequests(t, Name, 4)[2]
	assert.Equal(t, http.MethodHead, heads.Method)
	assert.Equal(t, "perplexity.agent.retrieve.head_refused", heads.Outcome.Label)
	assert.Equal(t, -1, heads.Outcome.AttemptIndex, "a HEAD claims nothing")
	assert.Contains(t, codesOf(heads), provider.CodeMethodNotAllowed)
}

// TestBackgroundHeadIsRefusedBeforeAuthAndResolve: the HEAD refusal is the
// retrieve's first branch, ahead of authentication and job resolution, so a
// HEAD with no credential, or naming no job, is still the 405 with Allow: GET
// and records only the refusal — not a 401, and not a 404 with job.foreign_id.
func TestBackgroundHeadIsRefusedBeforeAuthAndResolve(t *testing.T) {
	t.Parallel()
	sim := startBackground(t, backgroundScenario)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	tests := []struct {
		name, path string
		noAuth     bool
	}{
		{"no credential", "/v1/agent/" + id, true},
		{"an id never minted", "/v1/agent/resp_00000000000000000000000000000000", false},
	}
	for _, tc := range tests {
		r := bgSend(t, sim, base, http.MethodHead, tc.path, "", nil, tc.noAuth)
		assert.Equal(t, http.StatusMethodNotAllowed, r.status, tc.name)
		assert.Equal(t, http.MethodGet, r.header.Get("Allow"), tc.name)
	}
	entries := sim.AwaitRequests(t, Name, 1+len(tests))[1:]
	for i, e := range entries {
		assert.Equal(t, []string{provider.CodeMethodNotAllowed}, codesOf(e),
			"%s: a HEAD neither authenticates nor resolves", tests[i].name)
		assert.Equal(t, -1, e.Outcome.AttemptIndex, tests[i].name)
	}
	assert.Zero(t, sim.Jobs()[0].Polls)
}

// TestBackgroundFailsClosed: a background request the scenario cannot answer
// honestly is refused with a named finding, before anything is claimed and
// without a job.
func TestBackgroundFailsClosed(t *testing.T) {
	t.Parallel()

	const noBlock = `
version: 1
name: background-no-block
providers:
  perplexity_agent:
    answer: A synchronous answer.
`
	const noEntry = `
version: 1
name: background-no-entry
providers:
  perplexity:
    answer: sonar only
`
	streaming := strings.Replace(backgroundScenario, "    answer: A synchronous answer.\n",
		"    answer: A synchronous answer.\n    stream:\n      when_requested: stream\n      deltas: [\"A synchronous \", \"answer.\"]\n", 1)
	rejecting := strings.Replace(backgroundScenario, "    answer: A synchronous answer.\n",
		"    answer: A synchronous answer.\n    stream: reject\n", 1)

	tests := []struct {
		name, src, request string
		wantStatus         int
		wantCode           string
		wantMessage        string
	}{
		{name: "background with stream", src: backgroundScenario,
			request:    `{"input":"q","model":"openai/gpt-5","background":true,"stream":true}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeAgentBackgroundStream,
			wantMessage: "validation failed: "},
		{name: "background with stream on a streaming entry", src: streaming,
			request:    `{"input":"q","model":"openai/gpt-5","background":true,"stream":true}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeAgentBackgroundStream,
			wantMessage: "validation failed: "},
		{name: "a reject policy still answers first", src: rejecting,
			request:    `{"input":"q","model":"openai/gpt-5","background":true,"stream":true}`,
			wantStatus: http.StatusBadRequest, wantCode: CodeAgentStreamUnsupported,
			wantMessage: "validation failed: streaming responses are not simulated"},
		{name: "no background block", src: noBlock, request: backgroundRequest,
			wantStatus: http.StatusNotFound, wantCode: CodeAgentBackgroundUnscripted, wantMessage: "Not Found"},
		{name: "no Agent entry at all", src: noEntry, request: backgroundRequest,
			wantStatus: http.StatusNotFound, wantCode: CodeAgentBackgroundUnscripted, wantMessage: "Not Found"},
		{name: "store false and no block", src: noBlock,
			request:    `{"input":"q","model":"openai/gpt-5","background":true,"store":false}`,
			wantStatus: http.StatusNotFound, wantCode: CodeAgentBackgroundUnscripted, wantMessage: "Not Found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sim := startBackground(t, tc.src)

			r := bgSend(t, sim, sim.URL(Name), http.MethodPost, "/v1/agent", tc.request, nil, false)
			assert.Equal(t, tc.wantStatus, r.status, "body: %s", r.body)
			var envelope struct {
				Error errorInfo `json:"error"`
			}
			require.NoError(t, json.Unmarshal(r.body, &envelope), "body: %s", r.body)
			assert.True(t, strings.HasPrefix(envelope.Error.Message, tc.wantMessage),
				"message %q does not start with %q", envelope.Error.Message, tc.wantMessage)

			e := sim.AwaitRequests(t, Name, 1)[0]
			assert.Contains(t, codesOf(e), tc.wantCode)
			assert.Equal(t, -1, e.Outcome.AttemptIndex, "a refused background create claims nothing")
			assert.Empty(t, sim.Jobs(), "a refused background create leaves no job")
		})
	}
}

// TestBackgroundUnstored: store:false hides a response from retrieve, so a
// background create with it answers the queued snapshot under the id the
// synchronous path would derive, keeps no job, and says so.
func TestBackgroundUnstored(t *testing.T) {
	t.Parallel()
	sim := startBackground(t, backgroundScenario)

	r := bgSend(t, sim, sim.URL(Name), http.MethodPost, "/v1/agent",
		`{"input":"q","model":"openai/gpt-5","background":true,"store":false}`, nil, false)
	require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
	got := r.json(t)
	assert.Equal(t, "queued", got["status"])
	assert.Equal(t, []any{}, got["output"])
	assert.Equal(t, "resp_"+provider.Hex32("background-lifecycle", string(Name), FaultKeyAgent, "0", "agent"), got["id"],
		"the id the synchronous path derives for call 0")
	assert.Empty(t, sim.Jobs())

	e := sim.AwaitRequests(t, Name, 1)[0]
	assert.Equal(t, "perplexity.agent.background.unstored", e.Outcome.Label)
	testkit.AssertFindings(t, e, CodeAgentBackgroundUnstored)
	require.Len(t, e.Findings, 1)
	assert.Equal(t, journal.SeverityWarning, e.Findings[0].Severity)
	assert.Equal(t, "body.store", e.Findings[0].Field)
}

// TestBackgroundUnstoredUnderStrictValidationIsRefused: strict validation
// promotes the store:false warning, and the create is refused before anything
// is claimed. Without that refusal the client would get a 200 queued body while
// the journal recorded an error: a success-shaped failure.
func TestBackgroundUnstoredUnderStrictValidationIsRefused(t *testing.T) {
	t.Parallel()
	src := strings.Replace(backgroundScenario, "  perplexity_agent:\n",
		"  perplexity_agent:\n    validation: {strict: true}\n", 1)
	sim := startBackground(t, src)

	r := bgSend(t, sim, sim.URL(Name), http.MethodPost, "/v1/agent",
		`{"input":"q","model":"openai/gpt-5","background":true,"store":false}`, nil, false)
	assert.Equal(t, http.StatusBadRequest, r.status, "body: %s", r.body)
	assert.NotContains(t, string(r.body), `"queued"`)

	e := sim.AwaitRequests(t, Name, 1)[0]
	assert.Contains(t, codesOf(e), CodeAgentBackgroundUnstored)
	assert.Equal(t, -1, e.Outcome.AttemptIndex, "a refused create claims nothing")
	assert.Empty(t, sim.Jobs())
}

// TestBackgroundAbsentOrFalseIsTheSynchronousPath: background false, or absent,
// is the ordinary synchronous create, byte for byte.
func TestBackgroundAbsentOrFalseIsTheSynchronousPath(t *testing.T) {
	t.Parallel()

	render := func(request string) []byte {
		sim := startBackground(t, agentCorpus)
		r := bgSend(t, sim, sim.URL(Name), http.MethodPost, "/v1/agent", request, nil, false)
		require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
		return r.body
	}
	want := string(goldenBytes(t, "perplexity-agent-happy.json"))
	assert.Equal(t, want, string(render(agentRequest)))
	assert.Equal(t, want, string(render(`{"input":"what do the reports say?","model":"openai/gpt-5","background":false}`)))
}

// TestBackgroundRetrieveFaultIsItsOwnBudget: the retrieve route's plan is read
// from background.turns, per job, and is independent of the create's plan.
func TestBackgroundRetrieveFaultIsItsOwnBudget(t *testing.T) {
	t.Parallel()

	src := strings.Replace(backgroundScenario, "    answer: A synchronous answer.\n",
		"    fault: {attempts: [{status: 429}]}\n    answer: A synchronous answer.\n", 1)
	src = strings.Replace(src, "        - when: {call_index: 0}\n          respond: {status: queued}\n",
		"        - when: {call_index: 0}\n          respond: {status: queued}\n          fault: {attempts: [{status: 503}]}\n", 1)
	sim := startBackground(t, src)
	base := sim.URL(Name)

	limited := bgSend(t, sim, base, http.MethodPost, "/v1/agent", backgroundRequest, nil, false)
	assert.Equal(t, http.StatusTooManyRequests, limited.status, "the create's own plan: body %s", limited.body)
	assert.Empty(t, sim.Jobs(), "a create whose reply carries no id keeps no job")

	first := bgCreate(t, sim, base)
	second := bgCreate(t, sim, base)

	for _, id := range []string{first, second} {
		r := bgRetrieve(t, sim, base, id)
		assert.Equal(t, http.StatusServiceUnavailable, r.status, "each job's first retrieve: %s", r.body)
		assert.Contains(t, string(r.body), `"error"`, "a scripted fault on the retrieve is Agent-shaped")
	}
	for _, id := range []string{first, second} {
		assert.Equal(t, "in_progress", statusIn(t, bgRetrieve(t, sim, base, id)),
			"the faulted retrieve spent call 0 of its own job's lane")
	}
}

// TestBackgroundRetrieveFaultIsTheFirstDeclaredPlan: with two background turns
// that each declare a plan, the retrieve draws on the first, as
// provider.TurnFault does for an entry's own turns. The engine holds one plan
// per route key, so the second is never read.
func TestBackgroundRetrieveFaultIsTheFirstDeclaredPlan(t *testing.T) {
	t.Parallel()
	sim := startBackground(t, `
version: 1
name: background-two-plans
providers:
  perplexity_agent:
    answer: A synchronous answer.
    background:
      turns:
        - when: {call_index: 0}
          respond: {status: queued}
          fault: {attempts: [{status: 503}]}
        - respond: {status: completed, answer: done}
          fault: {attempts: [{status: 429}]}
`)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)
	r := bgRetrieve(t, sim, base, id)
	assert.Equal(t, http.StatusServiceUnavailable, r.status, "body: %s", r.body)
}

// TestBackgroundRetrievePastAnExhaustedScriptIs404: a retrieve the script has no
// snapshot for is the Agent 404 with scenario.no_matching_turn, never a default
// snapshot — and its index is spent, so the job's position still moves.
func TestBackgroundRetrievePastAnExhaustedScriptIs404(t *testing.T) {
	t.Parallel()
	sim := startBackground(t, `
version: 1
name: background-exhausted
providers:
  perplexity_agent:
    answer: A synchronous answer.
    background:
      turns:
        - when: {call_index: 0}
          respond: {status: queued}
`)
	base := sim.URL(Name)
	id := bgCreate(t, sim, base)

	assert.Equal(t, "queued", statusIn(t, bgRetrieve(t, sim, base, id)))
	r := bgRetrieve(t, sim, base, id)
	assert.Equal(t, http.StatusNotFound, r.status, "body: %s", r.body)
	assert.Equal(t, string(goldenBytes(t, "perplexity-agent-retrieve-404.json")), string(r.body))
	assert.Equal(t, 2, sim.Jobs()[0].Polls, "the unanswered retrieve still spent its index")

	e := sim.AwaitRequests(t, Name, 3)[2]
	assert.Contains(t, codesOf(e), provider.CodeNoMatchingTurn)
	assert.Equal(t, 1, e.Outcome.AttemptIndex)
}

// refusingStore is a job store whose Create always fails with err, standing in
// for a store that has hit its bound or already holds the identifier.
type refusingStore struct {
	*jobs.Registry
	err error
}

func (s refusingStore) Create(_ jobs.Job) (jobs.Stats, error) {
	return jobs.Stats{Count: 1, Bound: 1}, s.err
}

// TestBackgroundCreateTheJobStoreRefuses: a create the job store refuses is the
// Agent error envelope carrying the finding's own message — a Servicesim
// configuration problem named as one, not a plausible vendor error — and
// leaves no job.
func TestBackgroundCreateTheJobStoreRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		store      jobs.Store
		wantStatus int
		wantCode   string
		wantText   string
	}{
		{"the namespace is at its bound", jobs.NewRegistry(jobs.Limits{MaxJobs: 1}),
			http.StatusServiceUnavailable, provider.CodeJobLimitReached, "holds its maximum of 1 jobs"},
		{"the identifier is already live", refusingStore{jobs.NewRegistry(jobs.Limits{}), jobs.ErrDuplicate},
			http.StatusInternalServerError, provider.CodeJobIDCollision, "is already live"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := mustScenario(t, backgroundScenario)
			ring := journal.NewRing(64, 1<<16)
			srv := httptest.NewServer(Profile().Handler(provider.Deps{
				Scenario: s, Journal: ring, Faults: provider.MustSet(Profile()).Faults(s), Jobs: tc.store,
			}))
			t.Cleanup(srv.Close)
			sent := 0
			do := func() (int, []byte) {
				sent++
				req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/agent", strings.NewReader(backgroundRequest))
				require.NoError(t, err)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+testKey)
				resp, err := srv.Client().Do(req)
				require.NoError(t, err)
				defer func() { require.NoError(t, resp.Body.Close()) }()
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				return resp.StatusCode, body
			}

			if _, ok := tc.store.(refusingStore); !ok {
				status, body := do()
				require.Equal(t, http.StatusOK, status, "the first create fits: %s", body)
			}
			status, body := do()
			assert.Equal(t, tc.wantStatus, status, "body: %s", body)
			var envelope struct {
				Error errorInfo `json:"error"`
			}
			require.NoError(t, json.Unmarshal(body, &envelope), "body: %s", body)
			assert.Contains(t, envelope.Error.Message, tc.wantText)

			// The handler appends its entry after writing the response, so the
			// client can hold the body first. A bare ring has no AwaitRequests;
			// wait for the entry the same bounded way it does.
			require.Eventually(t, func() bool { return len(ring.Snapshot()) >= sent },
				5*time.Second, time.Millisecond, "the journal never recorded the %d creates sent", sent)
			entries := ring.Snapshot()
			last := entries[len(entries)-1]
			assert.Contains(t, codesOf(last), tc.wantCode)
			assert.Contains(t, codesOf(last), provider.CodeAttemptOnRejection,
				"MintJob claims before the store can refuse; the claimed attempt is reported, not applied")
		})
	}
}

// TestBackgroundJobsAreIndependent: two jobs polled in interleaved order each
// see their own script position.
func TestBackgroundJobsAreIndependent(t *testing.T) {
	t.Parallel()
	sim := startBackground(t, backgroundScenario)
	base := sim.URL(Name)
	a, b := bgCreate(t, sim, base), bgCreate(t, sim, base)
	require.NotEqual(t, a, b)

	var got []string
	for _, id := range []string{a, a, b, a, b, b} {
		got = append(got, statusIn(t, bgRetrieve(t, sim, base, id)))
	}
	assert.Equal(t, []string{"queued", "in_progress", "queued", "completed", "in_progress", "completed"}, got)
}

// TestBackgroundRetrieveLaneIgnoresTheEntryTurnKey: a run's retrieve lane is the
// run (issue #29). A turn_key on perplexity_agent keys the create and nothing
// after it: a retrieve that read it too would put the run in one lane per tenant
// value, each walking the background script from its first retrieve, and a
// retrieve without the header in a lane of its own besides.
func TestBackgroundRetrieveLaneIgnoresTheEntryTurnKey(t *testing.T) {
	t.Parallel()
	src := strings.Replace(backgroundScenario, "  perplexity_agent:\n",
		"  perplexity_agent:\n    turn_key: [\"route\", \"header:x-tenant\"]\n", 1)
	sim := startBackground(t, src)
	base := sim.URL(Name)

	r := bgSend(t, sim, base, http.MethodPost, "/v1/agent", backgroundRequest,
		map[string]string{"x-tenant": "acme"}, false)
	require.Equal(t, http.StatusOK, r.status, "create: %s", r.body)
	id, _ := r.json(t)["id"].(string)
	require.NotEmpty(t, id)

	var got []string
	for _, header := range []map[string]string{{"x-tenant": "acme"}, nil, {"x-tenant": "globex"}} {
		got = append(got, statusIn(t, bgSend(t, sim, base, http.MethodGet, "/v1/agent/"+id, "", header, false)))
	}
	assert.Equal(t, []string{"queued", "in_progress", "completed"}, got,
		"retrieved with the create's tenant, without the header and with another tenant")

	entries := sim.AwaitRequests(t, Name, 4)
	creates := onRoute(entries, "POST /v1/agent")
	require.Len(t, creates, 1)
	assert.Contains(t, creates[0].Outcome.FaultKey, "header:x-tenant=acme",
		"the create declares no LaneFrom, so the entry's turn_key must still lane it")

	retrieves := onRoute(entries, "GET /v1/agent/{id}")
	require.Len(t, retrieves, 3)
	for i, e := range retrieves {
		assert.Equal(t, i, e.Outcome.AttemptIndex, "retrieve %d", i)
		assert.Equal(t, faultKeyAgentRetrieve+"|path:id="+id, e.Outcome.FaultKey,
			"retrieve %d: the lane is the run and nothing else", i)
		testkit.AssertNoFindings(t, e)
	}

	// The job store counts retrieves by run, the lane cursor by lane; with one
	// lane per run the two agree.
	jobs := sim.Jobs()
	require.Len(t, jobs, 1)
	assert.Equal(t, 3, jobs[0].Polls)
}

// TestBackgroundAcceptedCreateKeepsTheJob: Perplexity's background create takes
// part in the accepted-create mechanism through the turn-level plan it shares
// with synchronous creates. The client sees the 500, and the job exists.
func TestBackgroundAcceptedCreateKeepsTheJob(t *testing.T) {
	t.Parallel()
	src := strings.Replace(backgroundScenario, "    answer: A synchronous answer.\n",
		"    fault: {attempts: [{status: 500, accepted: true}]}\n    answer: A synchronous answer.\n", 1)
	sim := startBackground(t, src)
	base := sim.URL(Name)

	r := bgSend(t, sim, base, http.MethodPost, "/v1/agent", backgroundRequest, nil, false)
	assert.Equal(t, http.StatusInternalServerError, r.status, "body: %s", r.body)
	assert.NotContains(t, string(r.body), "resp_", "the lost reply names no job")

	jobs := sim.Jobs()
	require.Len(t, jobs, 1, "accepted keeps the job the create made")
	assert.Equal(t, "queued", statusIn(t, bgRetrieve(t, sim, base, jobs[0].ID)))

	e := sim.AwaitRequests(t, Name, 2)[0]
	testkit.AssertNoErrors(t, e)
}

// TestBackgroundCreateSharesTheCreateIndex: a background create claims the
// create lane's call index, so it shifts which synchronous turn the next
// synchronous call receives. Documented, not "fixed".
func TestBackgroundCreateSharesTheCreateIndex(t *testing.T) {
	t.Parallel()
	sim := startBackground(t, `
version: 1
name: background-shared-index
providers:
  perplexity_agent:
    turns:
      - when: {call_index: 0}
        respond: {answer: first}
      - respond: {answer: second}
    background:
      turns:
        - respond: {status: completed, answer: done}
`)
	base := sim.URL(Name)
	bgCreate(t, sim, base)

	r := bgSend(t, sim, base, http.MethodPost, "/v1/agent", agentRequest, nil, false)
	require.Equal(t, http.StatusOK, r.status, "body: %s", r.body)
	assert.Contains(t, string(r.body), `"text":"second"`, "the background create spent call 0")
}

// TestBackgroundIsDeterministicAndServed: the same scenario and the same
// requests give byte-identical create and retrieve bodies in two fresh
// processes, and those bodies are the lifecycle — the create is the queued
// snapshot and each retrieve the scripted status. Comparing bytes alone would
// pass for two runs that failed the same way.
func TestBackgroundIsDeterministicAndServed(t *testing.T) {
	t.Parallel()

	run := func() []string {
		sim := startBackground(t, backgroundScenario)
		base := sim.URL(Name)
		created := bgSend(t, sim, base, http.MethodPost, "/v1/agent", backgroundRequest, nil, false)
		require.Equal(t, "queued", statusIn(t, created))
		id, _ := created.json(t)["id"].(string)
		out := []string{string(created.body)}
		for _, want := range []string{"queued", "in_progress", "completed"} {
			r := bgRetrieve(t, sim, base, id)
			require.Equal(t, want, statusIn(t, r))
			out = append(out, string(r.body))
		}
		return out
	}
	assert.Equal(t, run(), run())
}

// TestBackgroundRetrieveCredentialsNeverSurvive: a missing or wrong credential
// is the 401 ErrorInfo, claims nothing, advances nothing, and the value
// presented — in a header, the query string or URL userinfo — reaches neither
// the response nor the journal.
func TestBackgroundRetrieveCredentialsNeverSurvive(t *testing.T) {
	t.Parallel()

	const sentinel = "pplx-SENTINEL-retrieve"
	src := strings.Replace(backgroundScenario, "  perplexity_agent:\n",
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
		{"no credential", base, "/v1/agent/" + id, nil},
		// Authentication comes before resolution, so an id that names no job
		// is still the 401, not the 404.
		{"no credential, an id never minted", base, "/v1/agent/resp_00000000000000000000000000000000", nil},
		{"a wrong bearer token", base, "/v1/agent/" + id, map[string]string{"Authorization": "Bearer " + sentinel}},
		{"a key in the query string", base, "/v1/agent/" + id + "?api_key=" + sentinel, nil},
		{"a key in URL userinfo", userinfo, "/v1/agent/" + id, nil},
	}
	for _, tc := range tests {
		r := bgSend(t, sim, tc.base, http.MethodGet, tc.path, "", tc.header, true)
		assert.Equal(t, http.StatusUnauthorized, r.status, "%s: %s", tc.name, r.body)
		assert.Equal(t, string(goldenBytes(t, "perplexity-agent-401.json")), string(r.body), tc.name)
		assert.NotContains(t, string(r.body), sentinel, tc.name)
	}

	entries := sim.AwaitRequests(t, Name, 1+len(tests))[1:]
	for i, e := range entries {
		assert.Equal(t, -1, e.Outcome.AttemptIndex, "%s claims nothing", tests[i].name)
	}
	assert.Zero(t, sim.Jobs()[0].Polls)
	testkit.AssertNoCredentialLeak(t, sim, sentinel)
}

// bgValidate loads src and runs the Agent validator over it the way readiness
// does, returning every finding at or under providers.perplexity_agent.
func bgValidate(t *testing.T, src string) []scenario.Finding {
	t.Helper()
	s, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err, "findings: %+v", report.Findings)
	require.True(t, report.OK(), "scenario findings: %+v", report.Findings)
	return provider.ValidateScenario(s, provider.MustSet(Profile()).Validators())
}

// bgEntry wraps a background block (indented under the entry) in a scenario
// whose synchronous turn is a plain answer.
func bgEntry(block string) string {
	return `
version: 1
name: background-validation
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  perplexity_agent:
    answer: A synchronous answer.
    background:
      turns:
` + block
}

// TestBackgroundValidator walks what readiness rejects or warns about in a
// background script, each addressed at the background turn it is about.
func TestBackgroundValidator(t *testing.T) {
	t.Parallel()

	const base = "providers.perplexity_agent.background.turns"
	tests := []struct {
		name     string
		block    string
		code     string
		severity scenario.Severity
		path     string
	}{
		{"response_id would contradict the job id",
			"        - respond: {status: completed, response_id: resp_x}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.response_id"},
		{"a retrieve serves no stream",
			"        - respond: {status: completed, stream: {when_requested: stream}}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.stream"},
		{"extra_fields would replace the job id",
			"        - respond: {status: completed, extra_fields: {id: resp_forged}}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.extra_fields.id"},
		{"extra_fields would contradict the scripted status",
			"        - respond: {status: queued, extra_fields: {status: completed}}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.extra_fields.status"},
		// A mapping with any non-string key decodes, as raw YAML, into a map
		// whose keys are not strings; the projection still decodes it and the
		// wire still carries the id, so the refusal must not depend on it.
		{"extra_fields with a non-string key still may not replace the job id",
			"        - respond: {status: completed, extra_fields: {id: resp_forged, 1: y}}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.extra_fields.id"},
		{"extra_fields with a non-string key still may not contradict the status",
			"        - respond: {status: completed, extra_fields: {status: failed, true: y}}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.extra_fields.status"},
		{"an explicitly null id in extra_fields is still written",
			"        - respond: {status: completed, extra_fields: {id: null}}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.extra_fields.id"},
		{"an explicitly empty status in extra_fields is still written",
			"        - respond: {status: completed, extra_fields: {status: \"\"}}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.extra_fields.status"},
		{"a !!str-tagged id in extra_fields is the id",
			"        - respond: {status: completed, extra_fields: {!!str id: resp_forged}}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.extra_fields.id"},
		{"extra_fields reached through an alias",
			"        - when: {call_index: 0}\n          respond: {status: queued, extra_fields: &forged {id: resp_forged}}\n" +
				"        - respond: {status: completed, extra_fields: *forged}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[1].respond.extra_fields.id"},
		{"an id merged into extra_fields",
			"        - when: {call_index: 0}\n          respond: {status: queued, extra_fields: &forged {id: resp_forged}}\n" +
				"        - respond: {status: completed, extra_fields: {<<: *forged, vendor_hint: kept}}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[1].respond.extra_fields.id"},
		{"extra_fields merged into the respond",
			"        - when: {call_index: 0}\n          respond: &snap {status: queued, extra_fields: {status: completed}}\n" +
				"        - respond: {<<: *snap, status: completed}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[1].respond.extra_fields.status"},
		{"an explicitly empty response_id is still written",
			"        - respond: {status: completed, response_id: \"\"}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.response_id"},
		{"an explicitly null stream is still written",
			"        - respond: {status: completed, stream: null}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].respond.stream"},
		// A fault attempt's extra_fields are merged into the body it serves just
		// as a snapshot's are, and an attempt that sets nothing else is no fault
		// in the journal, so the forged id or status would carry no fault_kind.
		{"a retrieve fault's extra_fields would replace the job id",
			"        - respond: {status: completed}\n          fault: {attempts: [{extra_fields: {id: resp_forged}}]}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].fault.attempts[0].extra_fields.id"},
		{"a retrieve fault's extra_fields would contradict the status",
			"        - when: {call_index: 0}\n          respond: {status: queued}\n" +
				"          fault: {attempts: [{status: 503}, {extra_fields: {status: completed}}]}\n" +
				"        - respond: {status: completed}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].fault.attempts[1].extra_fields.status"},
		{"a retrieve fault's extra_fields with a non-string key",
			"        - respond: {status: completed}\n          fault: {attempts: [{extra_fields: {id: resp_forged, 1: y}}]}\n",
			codeAgentBackgroundField, scenario.SeverityError, base + "[0].fault.attempts[0].extra_fields.id"},
		{"a create route never selects a background turn",
			"        - when: {route: \"perplexity:agent\"}\n          respond: {status: queued}\n        - respond: {status: completed}\n",
			provider.CodeTurnRouteUnknown, scenario.SeverityError, base + "[0].when.route"},
		{"an unknown route",
			"        - when: {route: agent.retreive}\n          respond: {status: queued}\n        - respond: {status: completed}\n",
			provider.CodeTurnRouteUnknown, scenario.SeverityError, base + "[0].when.route"},
		{"a terminal snapshot served before a pending one",
			"        - when: {call_index: 0}\n          respond: {status: completed}\n        - respond: {status: in_progress}\n",
			"perplexity.agent.background.terminal_then_pending", scenario.SeverityError, base + "[1].respond.status"},
		{"an absent status is completed, so terminal",
			"        - when: {call_index: 0}\n          respond: {answer: done}\n        - respond: {status: queued}\n",
			"perplexity.agent.background.terminal_then_pending", scenario.SeverityError, base + "[1].respond.status"},
		{"a failed snapshot is terminal",
			"        - when: {call_index: 0}\n          respond: {status: failed, error: {message: boom}}\n        - respond: {status: in_progress}\n",
			"perplexity.agent.background.terminal_then_pending", scenario.SeverityError, base + "[1].respond.status"},
		{"a cancelled snapshot is terminal",
			"        - when: {call_index: 0}\n          respond: {status: cancelled}\n        - respond: {status: queued}\n",
			"perplexity.agent.background.terminal_then_pending", scenario.SeverityError, base + "[1].respond.status"},
		{"a regression is judged on the retrieve route",
			"        - when: {route: agent.retrieve, call_index: 0}\n          respond: {status: completed}\n        - respond: {status: in_progress}\n",
			"perplexity.agent.background.terminal_then_pending", scenario.SeverityError, base + "[1].respond.status"},
		{"a script whose last turn is conditional runs out",
			"        - when: {call_index: 0}\n          respond: {status: completed}\n",
			"perplexity.agent.background.script_exhausted", scenario.SeverityWarning, base + "[0].when"},
		{"a last turn on the retrieve route that also names a call_index runs out",
			"        - when: {route: agent.retrieve, call_index: 0}\n          respond: {status: completed}\n",
			"perplexity.agent.background.script_exhausted", scenario.SeverityWarning, base + "[0].when"},
		{"a body predicate never matches a GET",
			"        - when: {body_contains: x}\n          respond: {status: queued}\n        - respond: {status: completed}\n",
			"perplexity.agent.background.body_predicate", scenario.SeverityWarning, base + "[0].when"},
		{"a body_json predicate never matches a GET either",
			"        - when: {body_json: {model: x}}\n          respond: {status: queued}\n        - respond: {status: completed}\n",
			"perplexity.agent.background.body_predicate", scenario.SeverityWarning, base + "[0].when"},
		{"a stream fault cannot apply to a retrieve",
			"        - respond: {status: completed}\n          fault: {attempts: [{kind: stream_disconnect, after_chunk: 1}]}\n",
			scenario.CodeStreamFaultMismatch, scenario.SeverityError, base + "[0].fault.attempts[0].kind"},
		{"a status outside the enum",
			"        - respond: {status: running}\n",
			"perplexity.agent.status.invalid", scenario.SeverityError, base + "[0].respond.status"},
		{"a failed snapshot needs its error",
			"        - respond: {status: failed}\n",
			"perplexity.agent.error.missing", scenario.SeverityError, base + "[0].respond.error"},
		{"an unresolved source",
			"        - respond: {status: completed, search_results: [source-z]}\n",
			"scenario.source.unknown", scenario.SeverityError, base + "[0].respond.search_results"},
		{"a key the projection does not have",
			"        - respond: {status: completed, colour: blue}\n",
			CodeProjectionInvalid, scenario.SeverityError, base + "[0].respond"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := bgValidate(t, bgEntry(tc.block))
			// A prefix, because a source reference's finding is addressed at the
			// reference inside the result, one segment below the field.
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

	t.Run("a well-formed script loads clean", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, bgValidate(t, backgroundScenario))
	})

	t.Run("the retrieve route may be named, in either spelling", func(t *testing.T) {
		t.Parallel()
		for _, route := range []string{"perplexity:agent.retrieve", "agent.retrieve"} {
			assert.Empty(t, bgValidate(t, bgEntry(
				"        - when: {route: \""+route+"\", call_index: 0}\n          respond: {status: queued}\n"+
					"        - respond: {status: completed}\n")), route)
		}
	})

	// A last turn whose only condition is the retrieve route matches every
	// retrieve, so the script cannot run out and there is nothing to warn about.
	t.Run("a last turn conditioned only on the retrieve route never runs out", func(t *testing.T) {
		t.Parallel()
		for _, route := range []string{"perplexity:agent.retrieve", "agent.retrieve"} {
			assert.Empty(t, bgValidate(t, bgEntry(
				"        - when: {call_index: 0}\n          respond: {status: queued}\n"+
					"        - when: {route: \""+route+"\"}\n          respond: {status: completed}\n")), route)
		}
	})

	// Only id and status are refused: any other key is what extra_fields
	// exists for, an additive field the consumer must tolerate.
	t.Run("extra_fields other than id and status load clean", func(t *testing.T) {
		t.Parallel()
		for _, block := range []string{
			"        - respond: {status: completed, extra_fields: {vendor_hint: kept}}\n",
			"        - respond: {status: completed, extra_fields: {vendor_hint: kept, 1: y}}\n",
			"        - respond: {status: completed, extra_fields: null}\n",
			"        - respond: {status: completed, extra_fields: {}}\n",
			"        - respond: {status: completed}\n          fault: {attempts: [{extra_fields: {vendor_hint: kept}}]}\n",
		} {
			assert.Empty(t, bgValidate(t, bgEntry(block)), block)
		}
	})

	// A cancel judges the run by its next retrieve, so where the run's script
	// runs out, a cancel fails as the retrieve does (job.cancel_unscripted).
	t.Run("the exhaustion warning says a cancel there fails too", func(t *testing.T) {
		t.Parallel()
		findings := bgValidate(t, bgEntry("        - when: {call_index: 0}\n          respond: {status: queued}\n"))
		require.Len(t, findings, 1, "%+v", findings)
		assert.Equal(t, codeAgentBackgroundScriptExhausted, findings[0].Code)
		assert.Contains(t, findings[0].Message, "the last background turn has a condition a retrieve can fail, so the "+
			"retrieve after it matches no turn and answers 404 for a job that exists, and a cancel there answers 500")
	})

	t.Run("a synchronous turn may not name the retrieve route", func(t *testing.T) {
		t.Parallel()
		findings := bgValidate(t, `
version: 1
name: sync-names-retrieve
providers:
  perplexity_agent:
    turns:
      - when: {route: "perplexity:agent.retrieve"}
        respond: {answer: never}
      - respond: {answer: always}
`)
		require.Len(t, findings, 1, "%+v", findings)
		assert.Equal(t, provider.CodeTurnRouteUnknown, findings[0].Code)
		assert.Equal(t, "providers.perplexity_agent.turns[0].when.route", findings[0].Path)
	})

	t.Run("the synchronous turns are judged exactly as without a block", func(t *testing.T) {
		t.Parallel()
		const syncTurns = `
version: 1
name: sync-findings
providers:
  perplexity_agent:
    turns:
      - when: {call_index: 0}
        respond: {status: failed}
      - respond: {status: running, stream: {when_requested: stream}}
`
		without := bgValidate(t, syncTurns)
		with := bgValidate(t, syncTurns+"    background:\n      turns:\n        - respond: {status: completed}\n")
		require.NotEmpty(t, without)
		assert.Equal(t, without, with)
	})
}

// TestBackgroundFindingCodesAreTheDocumentedStrings pins the code strings
// themselves. Every other test compares against the constant, so a changed
// string would break none of them, while docs/scenario-schema.md and the
// contract notes list the string and a consumer filters on it.
func TestBackgroundFindingCodesAreTheDocumentedStrings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ got, want string }{
		{CodeAgentBackgroundUnscripted, "perplexity.agent.background.unscripted"},
		{CodeAgentBackgroundStream, "perplexity.agent.background.stream"},
		{CodeAgentBackgroundUnstored, "perplexity.agent.background.unstored"},
		{codeAgentBackgroundField, "perplexity.agent.background.field"},
	} {
		assert.Equal(t, tc.want, tc.got)
	}
}

// longS is U+017F LATIN SMALL LETTER LONG S, written as an escape so the source
// shows which character it is. Unicode simple folding, which Go's encoding/json
// uses to match an object key to a struct field, folds it to s.
const longS = "\u017f"

// TestBackgroundExtraFieldsAreRefusedInEverySpellingADecoderReads: a refused
// extra_fields key is refused in every spelling a JSON decoder reads as that
// key. Go's encoding/json matches an object key to a field case-insensitively
// under Unicode simple folding, the last match winning, and a merged body's keys
// come out sorted, so "ſtatus" sorts after "status", is read after it, and
// replaces it: a retrieve the journal labels in_progress decodes as completed,
// and a cancel acknowledgement as another id, completed. An ASCII case variant
// sorts before the real key and loses, but it is the same key to the decoder
// and is refused alike. Each finding is addressed at the key as written, in an
// order Go's map iteration does not decide, and is an error, which stops
// readiness, so the forged body is never served.
func TestBackgroundExtraFieldsAreRefusedInEverySpellingADecoderReads(t *testing.T) {
	t.Parallel()

	const (
		retrieveRespond = "providers.perplexity_agent.background.turns[0].respond.extra_fields."
		retrieveFault   = "providers.perplexity_agent.background.turns[0].fault.attempts[0].extra_fields."
		cancelRespond   = "providers.perplexity_agent.background.cancel.turns[0].respond.extra_fields."
		cancelFault     = "providers.perplexity_agent.background.cancel.fault.attempts[0].extra_fields."
	)
	status, responseID := longS+"tatus", "re"+longS+"ponse_id"

	// Each places one extra_fields mapping where a background route reads it.
	inRetrieveRespond := func(extra string) string {
		return bgEntry("        - respond: {status: in_progress, extra_fields: " + extra + "}\n")
	}
	inRetrieveFault := func(extra string) string {
		return bgEntry("        - respond: {status: completed}\n          fault: {attempts: [{extra_fields: " +
			extra + "}]}\n")
	}
	inCancelRespond := func(extra string) string {
		return bgCancelEntry("        - respond: {status: queued}\n",
			"        turns:\n          - respond: {status: cancelled, extra_fields: "+extra+"}\n")
	}
	inCancelFault := func(extra string) string { return withCancelFault("{extra_fields: " + extra + "}") }

	type finding struct {
		code, path string
		severity   scenario.Severity
	}
	refused := func(paths ...string) []finding {
		out := make([]finding, 0, len(paths))
		for _, p := range paths {
			out = append(out, finding{codeAgentBackgroundField, p, scenario.SeverityError})
		}
		return out
	}

	tests := []struct {
		name string
		src  string
		want []finding // in order
	}{
		{"cancel fault: the long-s spellings forge the acknowledgement",
			inCancelFault(`{"` + responseID + `": resp_forged, "` + status + `": completed}`),
			refused(cancelFault+responseID, cancelFault+status)},
		{"cancel fault: an ASCII case variant of status",
			inCancelFault(`{Status: completed}`), refused(cancelFault + "Status")},
		{"cancel fault: an ASCII case variant of response_id",
			inCancelFault(`{RESPONSE_ID: resp_forged}`), refused(cancelFault + "RESPONSE_ID")},
		{"cancel fault: the exact keys are still refused",
			inCancelFault(`{response_id: resp_forged, status: completed}`),
			refused(cancelFault+"response_id", cancelFault+"status")},
		{"cancel fault: every spelling at once, by refused key and then by key",
			inCancelFault(`{"` + status + `": a, STATUS: b, status: c, Status: d, "` + responseID + `": e, ` +
				`RESPONSE_ID: f, response_id: g}`),
			refused(cancelFault+"RESPONSE_ID", cancelFault+"response_id", cancelFault+responseID,
				cancelFault+"STATUS", cancelFault+"Status", cancelFault+"status", cancelFault+status)},
		{"cancel fault: keys the acknowledgement does not carry load clean",
			inCancelFault(`{note: x, trace_id: t, id: resp_x}`), nil},

		{"retrieve fault: the long-s status",
			inRetrieveFault(`{"` + status + `": completed}`), refused(retrieveFault + status)},
		{"retrieve fault: ASCII case variants",
			inRetrieveFault(`{Status: completed, ID: resp_forged}`),
			refused(retrieveFault+"ID", retrieveFault+"Status")},
		{"retrieve fault: the exact keys are still refused",
			inRetrieveFault(`{id: resp_forged, status: completed}`),
			refused(retrieveFault+"id", retrieveFault+"status")},
		{"retrieve fault: other keys load clean",
			inRetrieveFault(`{note: x, trace_id: t}`), nil},

		{"retrieve respond: the long-s status",
			inRetrieveRespond(`{"` + status + `": completed}`), refused(retrieveRespond + status)},
		{"retrieve respond: ASCII case variants",
			inRetrieveRespond(`{Status: completed, Id: resp_forged}`),
			refused(retrieveRespond+"Id", retrieveRespond+"Status")},
		{"retrieve respond: the exact keys are still refused",
			inRetrieveRespond(`{id: resp_forged, status: completed}`),
			refused(retrieveRespond+"id", retrieveRespond+"status")},
		{"retrieve respond: every spelling at once, by refused key and then by key",
			inRetrieveRespond(`{"` + status + `": a, Status: b, status: c, ID: d, id: e}`),
			refused(retrieveRespond+"ID", retrieveRespond+"id",
				retrieveRespond+"Status", retrieveRespond+"status", retrieveRespond+status)},
		{"retrieve respond: other keys load clean",
			inRetrieveRespond(`{note: x, trace_id: t}`), nil},

		{"cancel script respond: the long-s status",
			inCancelRespond(`{"` + status + `": in_progress}`), refused(cancelRespond + status)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := bgValidate(t, tc.src)
			var got []finding // nil when the scenario loads clean, as want is
			for _, f := range findings {
				got = append(got, finding{f.Code, f.Path, f.Severity})
			}
			assert.Equal(t, tc.want, got, "%+v", findings)
			assert.Equal(t, len(tc.want) == 0, scenario.Report{Findings: findings}.OK(),
				"an error stops readiness, so the forged body is never served")

			// The order is the validator's, not Go's map iteration: every load of
			// the same scenario gives the same findings, messages included.
			for range 4 {
				assert.Equal(t, findings, bgValidate(t, tc.src))
			}
		})
	}
}

// TestBackgroundFieldFindingsSayWhatTheRouteServes pins the whole message of
// each finding a background route's body raises, at the route that raises it.
// The retrieve's text is the text it carried before the cancel route came to
// share its checks (4ef15ac), so rewording what the retrieve serves, or where
// its extra_fields are written, is a change a reader of the documented messages
// sees, not a refactor's side effect. The cancel's is pinned whole for the same
// reason; and the text for a key a decoder folds to a refused one is what the
// documentation quotes.
func TestBackgroundFieldFindingsSayWhatTheRouteServes(t *testing.T) {
	t.Parallel()

	const (
		retrieveTurn = "providers.perplexity_agent.background.turns[0]"
		cancelFault  = "providers.perplexity_agent.background.cancel.fault.attempts[0]"
	)
	status, responseID := longS+"tatus", "re"+longS+"ponse_id"
	tests := []struct {
		name, src, code, path, message string
	}{
		{"a retrieve snapshot's extra_fields.id",
			bgEntry("        - respond: {status: completed, extra_fields: {id: resp_forged}}\n"),
			codeAgentBackgroundField, retrieveTurn + ".respond.extra_fields.id",
			"extra_fields.id is not allowed on a background turn: extra fields are merged into the body last and " +
				"win, and a snapshot's id is always its job's; remove extra_fields.id"},
		{"a retrieve snapshot's extra_fields.status",
			bgEntry("        - respond: {status: queued, extra_fields: {status: completed}}\n"),
			codeAgentBackgroundField, retrieveTurn + ".respond.extra_fields.status",
			"extra_fields.status is not allowed on a background turn: extra fields are merged into the body last " +
				"and win, and a snapshot's status is its respond.status, which the journal label reports and the " +
				"terminal check judged; remove extra_fields.status"},
		{"a retrieve fault attempt's extra_fields.id",
			bgEntry("        - respond: {status: completed}\n          fault: {attempts: [{extra_fields: {id: resp_forged}}]}\n"),
			codeAgentBackgroundField, retrieveTurn + ".fault.attempts[0].extra_fields.id",
			"extra_fields.id is not allowed on a background turn: extra fields are merged into the body last and " +
				"win, and a snapshot's id is always its job's; remove extra_fields.id"},
		{"a retrieve fault attempt's extra_fields.status",
			bgEntry("        - respond: {status: completed}\n          fault: {attempts: [{extra_fields: {status: failed}}]}\n"),
			codeAgentBackgroundField, retrieveTurn + ".fault.attempts[0].extra_fields.status",
			"extra_fields.status is not allowed on a background turn: extra fields are merged into the body last " +
				"and win, and a snapshot's status is its respond.status, which the journal label reports and the " +
				"terminal check judged; remove extra_fields.status"},
		{"a stream kind on a retrieve fault",
			bgEntry("        - respond: {status: completed}\n          fault: {attempts: [{kind: stream_disconnect}]}\n"),
			scenario.CodeStreamFaultMismatch, retrieveTurn + ".fault.attempts[0].kind",
			`kind "stream_disconnect" assumes a chunked SSE transport, but GET /v1/agent/{id} never streams: a ` +
				"background snapshot is always an ordinary JSON body"},
		{"a cancel fault attempt's extra_fields.response_id",
			withCancelFault(`{extra_fields: {response_id: resp_forged}}`),
			codeAgentBackgroundField, cancelFault + ".extra_fields.response_id",
			"extra_fields.response_id is not allowed in background.cancel.fault: extra fields are merged into the " +
				"body last and win, and the acknowledgement's response_id is always its job's id; remove " +
				"extra_fields.response_id"},
		{"a cancel fault attempt's extra_fields.status",
			withCancelFault(`{extra_fields: {status: completed}}`),
			codeAgentBackgroundField, cancelFault + ".extra_fields.status",
			"extra_fields.status is not allowed in background.cancel.fault: extra fields are merged into the body " +
				"last and win, and the acknowledgement's status is always cancelling, the one value the " +
				"specification gives it; remove extra_fields.status"},
		{"a stream kind on a cancel fault",
			withCancelFault(`{kind: stream_disconnect}`),
			scenario.CodeStreamFaultMismatch, cancelFault + ".kind",
			`kind "stream_disconnect" assumes a chunked SSE transport, but POST /v1/agent/{id}/cancel never ` +
				"streams: its acknowledgement and its errors are always ordinary JSON bodies"},
		{"a retrieve snapshot's long-s status",
			bgEntry("        - respond: {status: in_progress, extra_fields: {\"" + status + "\": completed}}\n"),
			codeAgentBackgroundField, retrieveTurn + ".respond.extra_fields." + status,
			"extra_fields." + status + " is not allowed on a background turn: keys are compared the way a JSON " +
				"decoder compares them, case-insensitively, so " + status + " is read as status; extra fields are " +
				"merged into the body last and win, and a snapshot's status is its respond.status, which the " +
				"journal label reports and the terminal check judged; remove extra_fields." + status},
		{"a cancel fault attempt's long-s response_id",
			withCancelFault(`{extra_fields: {"` + responseID + `": resp_forged}}`),
			codeAgentBackgroundField, cancelFault + ".extra_fields." + responseID,
			"extra_fields." + responseID + " is not allowed in background.cancel.fault: keys are compared the way " +
				"a JSON decoder compares them, case-insensitively, so " + responseID + " is read as response_id; " +
				"extra fields are merged into the body last and win, and the acknowledgement's response_id is " +
				"always its job's id; remove extra_fields." + responseID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := bgValidate(t, tc.src)
			require.Len(t, findings, 1, "%+v", findings)
			assert.Equal(t, tc.code, findings[0].Code)
			assert.Equal(t, tc.path, findings[0].Path)
			assert.Equal(t, scenario.SeverityError, findings[0].Severity)
			assert.Equal(t, tc.message, findings[0].Message)
		})
	}
}
