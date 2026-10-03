package scenarios_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/profiles/perplexity"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/scenarios"
	"github.com/c360studio/servicesim/testkit"
)

const (
	// backgroundRequest is a valid minimal Agent request that asks for the
	// background lifecycle: a model and an input are the required fields.
	backgroundRequest = `{"input":"report","model":"openai/gpt-5","background":true}`

	// backgroundRequestModel is the model backgroundRequest sends, which a create
	// echoes.
	backgroundRequestModel = "openai/gpt-5"

	// retrieveModelPlaceholder is the model a retrieve renders when its snapshot
	// scripts none. The vendor requires a model, a retrieve carries no request to
	// echo one from and the job record holds none, so the profile renders this
	// fixed value (its SIMULATOR-POLICY). The profile keeps the constant
	// unexported, so this asserts on the string, as a consumer would.
	retrieveModelPlaceholder = "servicesim/unscripted"

	// agentJournalLabelCreated and the retrieve label's prefix are the journal
	// labels the profile gives a background create and a retrieve. The journal
	// keeps no response bodies, so the retrieve's label is how a consumer sees a
	// poll that confirmed `completed`.
	agentJournalLabelCreated   = "perplexity.agent.background.created"
	agentJournalLabelRetrieved = "perplexity.agent.retrieved."
)

// backgroundIDPattern is a job's identifier: the prefix the profile mints and 32
// hex characters.
var backgroundIDPattern = regexp.MustCompile(`resp_[0-9a-f]{32}`)

// wire is one exchange with the Perplexity listener as an application under test
// sees it. err is a transport failure with no response at all; readErr is a
// response whose body ended early.
type wire struct {
	status  int
	header  http.Header
	body    []byte
	err     error
	readErr error
}

// perplexityWire sends one request the way an application under test does.
func perplexityWire(t *testing.T, sim *testkit.Sim, method, path, body, key string) wire {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, sim.URL(perplexity.Name)+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+key)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := sim.Client().Do(req)
	if err != nil {
		return wire{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, readErr := io.ReadAll(resp.Body)
	return wire{status: resp.StatusCode, header: resp.Header, body: raw, readErr: readErr}
}

// createAttempt is the attempt the nth create draws from the entry's create plan
// (the entry's own turn-level `fault:`, which the three create spellings and a
// background create share): the plan's attempts with Repeat expanded, then what
// `after:` says. A create with no plan, or past a plan that ends in success,
// draws an attempt that is no fault.
func createAttempt(plan *scenario.Fault, n int) scenario.FaultAttempt {
	if plan == nil {
		return scenario.FaultAttempt{}
	}
	var expanded []scenario.FaultAttempt
	for _, a := range plan.Attempts {
		for range a.Repeats() {
			expanded = append(expanded, a)
		}
	}
	switch {
	case n < len(expanded):
		return expanded[n]
	case plan.After == scenario.FaultAfterRepeatLast && len(expanded) > 0:
		return expanded[len(expanded)-1]
	}
	return scenario.FaultAttempt{}
}

// createsToWalk is how many creates to send: every attempt of the plan, then two
// more. The two past it show what the plan ends in (success, or the last attempt
// for good) and, when it ends in success, that a second job is independent of the
// first. A plan-free entry therefore still creates two jobs.
func createsToWalk(plan *scenario.Fault) int {
	n := 0
	if plan != nil {
		for _, a := range plan.Attempts {
			n += a.Repeats()
		}
	}
	return n + 2
}

// statusWanted is the HTTP status an attempt answers with: the one it declares, or
// 200 when it declares none.
func statusWanted(a scenario.FaultAttempt) int {
	if a.Status == 0 {
		return http.StatusOK
	}
	return a.Status
}

// assertQueuedStub checks the body of a background create that reached the client
// and returns the job's identifier: the snapshot of a run that has not started,
// echoing the request's model, with no output and no usage.
func assertQueuedStub(t *testing.T, s *scenario.Scenario, raw []byte) string {
	t.Helper()

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got), "the queued snapshot must be valid JSON, body: %.200s", raw)
	id, _ := got["id"].(string)
	require.Regexp(t, "^"+backgroundIDPattern.String()+"$", id)
	assert.Equal(t, "response", got["object"])
	assert.Equal(t, "queued", got["status"])
	assert.Equal(t, backgroundRequestModel, got["model"], "a create echoes the request's model")
	assert.EqualValues(t, s.BaseTime().Unix(), got["created_at"])
	assert.Equal(t, []any{}, got["output"], "a run that has not started has produced no output")
	assert.NotContains(t, got, "usage", "a queued run has billed nothing, and an unscripted usage is no billing fact")
	return id
}

// assertCreateOutcome checks one background create against the fault attempt it
// drew, and returns the job's identifier when the client received a queued
// snapshot ("" when it did not). Every expectation is read off the attempt, not
// off the scenario's name:
//
//   - an attempt that delivers its body ([scenario.FaultAttempt.DeliversBody]) — no
//     fault, a delay, a padded body — answers the queued snapshot;
//   - a status answers the vendor's error envelope with the attempt's message and
//     Retry-After;
//   - invalid_json answers the attempt's raw bytes;
//   - truncate_body answers headers and then a body that ends early;
//   - close_before_headers answers nothing at all.
//
// A kind outside that list stops the test: its outcome has not been measured, and
// a guess would be an assertion on a premise nobody checked.
func assertCreateOutcome(t *testing.T, s *scenario.Scenario, a scenario.FaultAttempt, res wire) string {
	t.Helper()

	switch kind := a.EffectiveKind(); kind {
	case scenario.FaultNone, scenario.FaultOversizedBody:
		require.NoError(t, res.err)
		require.NoError(t, res.readErr)
		require.Equal(t, statusWanted(a), res.status, "body: %.200s", res.body)
		id := assertQueuedStub(t, s, res.body)
		if kind == scenario.FaultOversizedBody {
			// Padding is insignificant whitespace after a complete value: the decoded
			// snapshot is the unpadded one, and only the size differs.
			assert.GreaterOrEqual(t, len(res.body), a.BodyBytes)
		}
		return id

	case scenario.FaultStatus:
		require.NoError(t, res.err)
		assert.Equal(t, a.Status, res.status)
		var envelope struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(res.body, &envelope), "body: %.200s", res.body)
		if a.Error != "" {
			assert.Equal(t, a.Error, envelope.Error.Message)
		} else {
			assert.NotEmpty(t, envelope.Error.Message)
		}
		if a.RetryAfter != nil {
			assert.Equal(t, strconv.Itoa(*a.RetryAfter), res.header.Get("Retry-After"))
		}

	case scenario.FaultInvalidJSON:
		require.NoError(t, res.err)
		assert.Equal(t, statusWanted(a), res.status)
		assert.Equal(t, a.RawBody, string(res.body))
		assert.False(t, json.Valid(res.body), "the body must not be valid JSON")

	case scenario.FaultTruncateBody:
		require.NoError(t, res.err, "headers arrive before the body is cut")
		assert.Equal(t, http.StatusOK, res.status)
		assert.Error(t, res.readErr, "the body must end early")
		assert.False(t, json.Valid(res.body))

	case scenario.FaultCloseBeforeHeaders:
		assert.Error(t, res.err, "nothing reaches the client")

	default:
		t.Fatalf("the plan scripts a %q fault on the create, whose effect on a background create has not been measured: "+
			"measure it and extend assertCreateOutcome", kind)
	}
	return ""
}

// retrieveSnapshots is the script of one entry: for each retrieve of a job, the
// snapshot its background block serves, picked by the rule the retrieve route
// applies (first match, no body, then the last unconditional turn). It runs two
// retrieves past the last call_index the script names, which is where its
// unconditional last turn takes over and must keep answering.
func retrieveSnapshots(t *testing.T, e *scenario.ProviderEntry) []backgroundSnapshot {
	t.Helper()

	probe := &scenario.ProviderEntry{Turns: e.Background.Turns}
	var out []backgroundSnapshot
	for poll := range backgroundPollsToWalk(e.Background.Turns) {
		turn, _, err := provider.SelectTurn(probe, poll, perplexityRetrieveRoute, nil)
		require.NoError(t, err, "retrieve %d must be served a snapshot", poll)
		out = append(out, decodeBackgroundSnapshot(t, turn))
	}
	return out
}

// assertSnapshot checks one retrieve's body against the snapshot the scenario
// scripts for it. The identifier is the job's, the creation time the scenario's
// base time, and the usage is present exactly when the snapshot scripts it, with
// the totals the renderer derives when the snapshot leaves them out.
func assertSnapshot(t *testing.T, s *scenario.Scenario, raw []byte, id string, want backgroundSnapshot) {
	t.Helper()

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got), "body: %.300s", raw)
	assert.Equal(t, id, got["id"], "a snapshot's id is always its job's")
	assert.Equal(t, "response", got["object"])
	assert.Equal(t, want.status(), got["status"])
	assert.EqualValues(t, s.BaseTime().Unix(), got["created_at"])
	if want.Model != "" {
		assert.Equal(t, want.Model, got["model"])
	} else {
		assert.Equal(t, retrieveModelPlaceholder, got["model"], "a retrieve has no request to echo a model from")
	}

	output, _ := got["output"].([]any)
	if want.Answer == "" {
		assert.Empty(t, output, "a snapshot with no answer has no message item")
	} else {
		require.Len(t, output, 1)
		message, _ := output[0].(map[string]any)
		assert.Equal(t, "message", message["type"])
		content, _ := message["content"].([]any)
		require.Len(t, content, 1)
		text, _ := content[0].(map[string]any)
		assert.Equal(t, want.Answer, text["text"])
	}

	if want.Usage == nil {
		assert.NotContains(t, got, "usage", "usage is rendered only when the snapshot scripts it")
		return
	}
	usage, _ := got["usage"].(map[string]any)
	require.NotNil(t, usage, "the snapshot scripts usage, so it renders it, body: %.300s", raw)
	assert.EqualValues(t, want.Usage.InputTokens, usage["input_tokens"])
	assert.EqualValues(t, want.Usage.OutputTokens, usage["output_tokens"])
	totalTokens := want.Usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = want.Usage.InputTokens + want.Usage.OutputTokens
	}
	assert.EqualValues(t, totalTokens, usage["total_tokens"])

	require.NotNil(t, want.Usage.Cost, "the corpus guard requires a scripted cost")
	cost, _ := usage["cost"].(map[string]any)
	require.NotNil(t, cost, "the snapshot scripts a cost, so it renders one")
	assert.Equal(t, "USD", cost["currency"])
	assert.InDelta(t, want.Usage.Cost.InputCost, cost["input_cost"], 1e-12)
	assert.InDelta(t, want.Usage.Cost.OutputCost, cost["output_cost"], 1e-12)
	totalCost := want.Usage.Cost.TotalCost
	if totalCost == 0 {
		totalCost = want.Usage.Cost.InputCost + want.Usage.Cost.OutputCost
	}
	assert.InDelta(t, totalCost, cost["total_cost"], 1e-12)
	assert.Positive(t, totalCost, "the billed run's cost is not a zero")
}

// TestBuiltins_ABackgroundRunIsCreatedAndRetrieved runs `background: true` end to
// end on every built-in the registry ships: a create, then the retrieves the
// scenario scripts and one past its end.
//
// What a built-in does to the create is not "succeed" for all of them. A
// background create draws the same fault attempt as a synchronous create — the
// three create spellings share one fault key — so the built-ins that fault the
// Agent create fault this one the same way, and the test derives each create's
// expected outcome from the attempt the scenario scripts for it:
//
//   - brownout's four delayed attempts still answer the queued snapshot and keep
//     their jobs; its two 503s answer the error envelope and keep none;
//   - rate-limited's 429 keeps no job, and the retry's job is the one retrieved;
//   - timeout's 30s delay still delivers the snapshot (delays are skipped here, and
//     the journal records the requested delay); a client that gives up during the
//     real delay is TestTimeout_AnAbandonedBackgroundCreateLeavesItsJob's;
//   - oversized-body pads the first snapshot past 4 MiB, still decodable, still a job;
//   - hang-then-abort's first attempt answers nothing, the next two answer headers
//     and a body that ends early; none keeps a job, so the identifier the cut body
//     still carries is one no job backs, and retrieving it answers 404;
//   - server-error and malformed-json fault the create for good, so no job is ever
//     minted and nothing can be retrieved;
//   - unauthorized refuses every call before anything is claimed; credential-rotation
//     refuses a retrieve with the old key without advancing the job.
//
// Every other built-in has no create fault, so its jobs are the ones it creates.
// A created job is retrieved to the scenario's last snapshot and once more past the
// end, and a second job starts at its own first snapshot, because a job's position
// is its own. Nothing here sleeps: requests are sequential and the journal is read
// through testkit's bounded AwaitRequests.
func TestBuiltins_ABackgroundRunIsCreatedAndRetrieved(t *testing.T) {
	t.Parallel()

	names := scenarios.Names()
	require.NotEmpty(t, names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := loadBuiltin(t, name)
			entry := s.Provider(perplexity.NameAgent)
			require.NotNil(t, entry)
			require.NotNil(t, entry.Background, "TestBuiltins_ABackgroundRunCanBeRetrieved fails first")
			script := retrieveSnapshots(t, entry)
			plan := provider.TurnFault(s, perplexity.NameAgent)

			// WithSkippedDelays: the scenarios' delays (30s) are asserted through the
			// journal's DelayMS, not paid.
			sim := testkit.Start(t, testkit.WithProfiles(referenceProfiles()...),
				testkit.WithBuiltin(name), testkit.WithProviders(perplexity.Name), testkit.WithSkippedDelays())

			key, wrongKey := "test-perplexity-key", ""
			if a := entry.Auth; a != nil && a.ExpectKey != "" {
				key, wrongKey = a.ExpectKey, "not-"+a.ExpectKey
			}

			var (
				labels   []string // the journal label of each request, in order
				delaysMS []int64  // the requested delay of each create, -1 for any other request
			)
			send := func(method, path, body, k, label string, delayMS int64) wire {
				labels = append(labels, label)
				delaysMS = append(delaysMS, delayMS)
				return perplexityWire(t, sim, method, path, body, k)
			}

			// A scenario that rejects every credential refuses a background create and a
			// retrieve alike, before it claims an attempt or mints anything.
			if a := entry.Auth; a != nil && a.Mode == scenario.AuthReject {
				for range 2 {
					res := send(http.MethodPost, "/v1/agent", backgroundRequest, key, "perplexity.agent.error.401", -1)
					require.NoError(t, res.err)
					assert.Equal(t, http.StatusUnauthorized, res.status, "body: %.200s", res.body)
				}
				res := send(http.MethodGet, "/v1/agent/resp_"+strings.Repeat("0", 32), "", key, "perplexity.agent.error.401", -1)
				require.NoError(t, res.err)
				assert.Equal(t, http.StatusUnauthorized, res.status, "a retrieve authenticates before it resolves")
				assert.Empty(t, sim.Jobs(), "a refused create mints no job")
				assertJournal(t, sim, labels, delaysMS, true)
				return
			}

			// A scenario that expects one key refuses a create with another before it claims
			// anything.
			if wrongKey != "" {
				res := send(http.MethodPost, "/v1/agent", backgroundRequest, wrongKey, "perplexity.agent.error.401", -1)
				require.NoError(t, res.err)
				assert.Equal(t, http.StatusUnauthorized, res.status, "body: %.200s", res.body)
				assert.Empty(t, sim.Jobs(), "a create with the wrong key mints no job")
			}

			// The creates, each checked against the attempt the scenario scripts for it.
			var (
				delivered []string // identifiers the client received a queued snapshot for
				deadIDs   []string // identifiers a cut body still carries, which no job backs
			)
			for n := range createsToWalk(plan) {
				a := createAttempt(plan, n)
				// An accepted attempt keeps a job whose identifier the client never
				// learns. No built-in scripts one on this create, so its outcome is
				// unmeasured: stop rather than guess.
				require.Falsef(t, a.Accepted, "create %d scripts accepted: true, whose effect on a background create has not been measured", n)
				res := send(http.MethodPost, "/v1/agent", backgroundRequest, key, agentJournalLabelCreated,
					time.Duration(a.Delay).Milliseconds())
				if id := assertCreateOutcome(t, s, a, res); id != "" {
					delivered = append(delivered, id)
				}
				if a.EffectiveKind() == scenario.FaultTruncateBody {
					id := backgroundIDPattern.FindString(string(res.body))
					if a.TruncateAfterBytes == 0 {
						// The default cut is half the body, and the identifier is its first key.
						require.NotEmpty(t, id, "a default truncation keeps the identifier: %.200s", res.body)
					}
					if id != "" {
						deadIDs = append(deadIDs, id)
					}
				}
			}
			// On the built-ins, a create keeps a job exactly when the client receives its
			// body: a status, a raw body, a cut body or no response at all leaves none.
			assert.Len(t, sim.Jobs(), len(delivered))

			// Every retrieve check below sits behind len(delivered) > 0, so a walk that
			// sent too few creates would skip them all and still pass. A plan that ends
			// in success leaves the two creates past it, and each delivers a job.
			if plan == nil || plan.After != scenario.FaultAfterRepeatLast {
				require.GreaterOrEqualf(t, len(delivered), 2,
					"a create plan that ends in success leaves two jobs to retrieve; fewer means the walk sent too few creates")
			}

			// An identifier from a body that was cut short is a real-looking identifier no
			// job backs.
			for _, id := range deadIDs {
				res := send(http.MethodGet, "/v1/agent/"+id, "", key, "perplexity.agent.error.404", -1)
				require.NoError(t, res.err)
				assert.Equal(t, http.StatusNotFound, res.status, "an identifier no job backs answers 404")
			}

			if len(delivered) > 0 {
				first, last := delivered[0], delivered[len(delivered)-1]

				// A retrieve with the wrong key is refused without advancing the job.
				if wrongKey != "" {
					res := send(http.MethodGet, "/v1/agent/"+first, "", wrongKey, "perplexity.agent.error.401", -1)
					require.NoError(t, res.err)
					assert.Equal(t, http.StatusUnauthorized, res.status)
					for _, j := range sim.Jobs() {
						assert.Zerof(t, j.Polls, "a refused retrieve advanced job %s", j.ID)
					}
				}

				// The retrieves the scenario scripts, and one past its end.
				var previous []byte
				for poll, want := range script {
					res := send(http.MethodGet, "/v1/agent/"+first, "", key, agentJournalLabelRetrieved+want.status(), -1)
					require.NoError(t, res.err)
					require.Equal(t, http.StatusOK, res.status, "retrieve %d, body: %.300s", poll, res.body)
					assertSnapshot(t, s, res.body, first, want)
					if poll == len(script)-1 {
						assert.Equal(t, string(previous), string(res.body),
							"a retrieve past the end of the script answers the last snapshot again, byte for byte")
					}
					previous = res.body
				}
				final := script[len(script)-1]
				assert.Equal(t, "completed", final.status(), "every built-in's background run ends completed")
				require.NotNil(t, final.Usage, "and with the usage and cost it billed, which assertSnapshot has just checked on the wire")

				// A job's position is its own: a second job starts at its first snapshot
				// although the first was polled to the end.
				if last != first {
					res := send(http.MethodGet, "/v1/agent/"+last, "", key, agentJournalLabelRetrieved+script[0].status(), -1)
					require.NoError(t, res.err)
					require.Equal(t, http.StatusOK, res.status)
					assertSnapshot(t, s, res.body, last, script[0])
				}
			}

			// background: true does not stream: the request is refused at validation,
			// before anything is claimed, so it spends no attempt and mints no job.
			jobsBefore := len(sim.Jobs())
			res := send(http.MethodPost, "/v1/agent",
				`{"input":"report","model":"openai/gpt-5","background":true,"stream":true}`, key, "perplexity.agent.error.400", -1)
			require.NoError(t, res.err)
			assert.Equal(t, http.StatusBadRequest, res.status, "body: %.300s", res.body)
			assert.Contains(t, string(res.body), "background: true with stream: true")
			assert.Len(t, sim.Jobs(), jobsBefore)

			for _, j := range sim.Jobs() {
				if len(delivered) > 0 && j.ID == delivered[0] {
					assert.Equal(t, len(script), j.Polls, "the first job was retrieved once per snapshot, plus one past the end")
				}
			}
			assertJournal(t, sim, labels, delaysMS, false)
		})
	}
}

// TestBuiltins_FaultTheAgentCreateEveryWayTheEndToEndTestChecks keeps the end-to-end
// test from going vacuous. Its per-attempt assertions are only exercised by the
// attempts the registry's scenarios script, so this fails when the corpus stops
// scripting a kind the test asserts on (and the assertion for it would then never
// run), or when the test is handed a kind it has no measured outcome for.
func TestBuiltins_FaultTheAgentCreateEveryWayTheEndToEndTestChecks(t *testing.T) {
	t.Parallel()

	seen := map[scenario.FaultKind]bool{}
	var delayed, repeatsLast, endsInSuccess bool
	for _, name := range scenarios.Names() {
		s := loadBuiltin(t, name)
		plan := provider.TurnFault(s, perplexity.NameAgent)
		if plan != nil {
			repeatsLast = repeatsLast || plan.After == scenario.FaultAfterRepeatLast
			endsInSuccess = endsInSuccess || plan.After != scenario.FaultAfterRepeatLast
		}
		for n := range createsToWalk(plan) {
			a := createAttempt(plan, n)
			seen[a.EffectiveKind()] = true
			delayed = delayed || a.Delay > 0
		}
	}

	for _, kind := range []scenario.FaultKind{
		scenario.FaultNone, scenario.FaultStatus, scenario.FaultInvalidJSON, scenario.FaultTruncateBody,
		scenario.FaultCloseBeforeHeaders, scenario.FaultOversizedBody,
	} {
		assert.Truef(t, seen[kind], "no built-in scripts a %q fault on the Agent create, so assertCreateOutcome's branch for it never runs", kind)
		delete(seen, kind)
	}
	assert.Empty(t, seen, "a built-in scripts a fault kind assertCreateOutcome has no measured outcome for")
	assert.True(t, delayed, "no built-in delays the Agent create, so the journal's delay is never compared")
	assert.True(t, repeatsLast, "no built-in faults the Agent create for good, so the no-job-ever path never runs")
	assert.True(t, endsInSuccess, "no built-in faults the Agent create and then recovers")
}

// TestCreateAttempt_ExpandsRepeatThenAfter pins the end-to-end test's helpers
// against the engine's expansion of a plan: each attempt Repeat times, then what
// `after:` says. No built-in scripts `repeat:` on the Agent create, so nothing
// else exercises that branch, and a helper that drifted from the engine would
// check every create against the wrong attempt.
func TestCreateAttempt_ExpandsRepeatThenAfter(t *testing.T) {
	t.Parallel()

	plan := &scenario.Fault{
		Attempts: []scenario.FaultAttempt{{Status: 503, Repeat: 2}, {Status: 429}},
		After:    scenario.FaultAfterRepeatLast,
	}
	var got []int
	for n := range createsToWalk(plan) {
		got = append(got, createAttempt(plan, n).Status)
	}
	assert.Equal(t, []int{503, 503, 429, 429, 429}, got)
}

// assertJournal checks the journal against the requests the test sent, in order:
// each request's label, and the delay each create was scripted to wait. A background
// create or retrieve that was answered carries no finding; a refused one carries
// the finding that says why.
func assertJournal(t *testing.T, sim *testkit.Sim, labels []string, delaysMS []int64, refusals bool) {
	t.Helper()

	entries := sim.AwaitRequests(t, perplexity.Name, len(labels))
	require.Len(t, entries, len(labels))
	for i, e := range entries {
		assert.Equalf(t, labels[i], e.Outcome.Label, "request %d: %s %s", i, e.Method, e.Path)
		if delaysMS[i] >= 0 {
			assert.Equalf(t, delaysMS[i], e.Outcome.DelayMS, "request %d: the journal records the delay the attempt scripts", i)
		}
		if refusals {
			assert.Equal(t, -1, e.Outcome.AttemptIndex, "a refused request claims no attempt")
		}
		if label := e.Outcome.Label; label == agentJournalLabelCreated || strings.HasPrefix(label, agentJournalLabelRetrieved) {
			testkit.AssertNoFindings(t, e)
		}
	}
}

// TestTimeout_AnAbandonedBackgroundCreateLeavesItsJob is the real-delay half of the
// timeout built-in's background create. The end-to-end test above skips delays, so it
// sees the 30s delay delivered; here the client gives up during it, as a consumer's
// timeout test does.
//
// The measured consequence: the job is minted before the delay and kept, because the
// attempt delivers its body, so the run exists although the client that gave up
// holds no identifier for it. A retry mints a second job. That is the orphan a
// create-then-poll client cannot tell from a rejected create, and it is why a timeout
// test against this built-in sees one more job than the client has identifiers.
//
// No client deadline races request delivery here. Because the job is minted before
// the hang, its appearance is the signal that the simulator is inside it, and
// cancelling then is what a deadline does, without a wall clock deciding whether the
// simulator ever read the request.
func TestTimeout_AnAbandonedBackgroundCreateLeavesItsJob(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(referenceProfiles()...),
		testkit.WithBuiltin("timeout"), testkit.WithProviders(perplexity.Name))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sim.URL(perplexity.Name)+"/v1/agent",
		strings.NewReader(backgroundRequest))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-perplexity-key")
	req.Header.Set("Content-Type", "application/json")
	done := make(chan error, 1)
	go func() {
		resp, err := sim.Client().Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()

	require.Eventually(t, func() bool { return len(sim.Jobs()) == 1 }, 5*time.Second, time.Millisecond,
		"the create mints its job before the hang")
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	// The abandoned call's entry lands on the server goroutine after the client returned.
	abandoned := sim.AwaitRequests(t, perplexity.Name, 1)[0]
	assert.True(t, abandoned.Outcome.Aborted)
	assert.Equal(t, int64(30000), abandoned.Outcome.DelayMS)
	assert.Equal(t, agentJournalLabelCreated, abandoned.Outcome.Label)
	assert.Len(t, sim.Jobs(), 1, "the abandoned create's job exists, and its client holds no identifier for it")

	// The retry draws the attempt past the plan, so it is answered, and it is a second job.
	res := perplexityWire(t, sim, http.MethodPost, "/v1/agent", backgroundRequest, "test-perplexity-key")
	require.NoError(t, res.err)
	require.Equal(t, http.StatusOK, res.status, "body: %.200s", res.body)
	id := assertQueuedStub(t, sim.Scenario(), res.body)
	require.Len(t, sim.Jobs(), 2)
	assert.Contains(t, []string{sim.Jobs()[0].ID, sim.Jobs()[1].ID}, id,
		"the retry's job is among the jobs the simulator holds")
}

// TestConversation_ABackgroundCreateCountsAsACall pins the sentence in the
// conversation built-in's description: a `background: true` request answers from
// the entry's background block, not from its turns, but it claims a call index like
// any other create, so the synchronous call after it receives the turn scripted for
// the call after.
func TestConversation_ABackgroundCreateCountsAsACall(t *testing.T) {
	t.Parallel()

	const (
		syncRequest = `{"input":"report","model":"openai/gpt-5"}`
		key         = "test-perplexity-key"
	)
	answer := func(t *testing.T, res wire) string {
		t.Helper()
		require.NoError(t, res.err)
		require.Equal(t, http.StatusOK, res.status, "body: %.200s", res.body)
		var got struct {
			Status string `json:"status"`
			Output []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		require.NoError(t, json.Unmarshal(res.body, &got))
		for _, item := range got.Output {
			for _, c := range item.Content {
				return got.Status + ": " + c.Text
			}
		}
		return got.Status + ":"
	}

	// Baseline: with no background create, the first synchronous call is call 0.
	plain := testkit.Start(t, testkit.WithProfiles(referenceProfiles()...),
		testkit.WithBuiltin("conversation"), testkit.WithProviders(perplexity.Name))
	first := answer(t, perplexityWire(t, plain, http.MethodPost, "/v1/agent", syncRequest, key))
	require.Contains(t, first, "Searching for Report A.", "call 0 is the scripted search turn")

	// A background create first: the synchronous call after it is call 1, so it
	// does not receive call 0's turn.
	sim := testkit.Start(t, testkit.WithProfiles(referenceProfiles()...),
		testkit.WithBuiltin("conversation"), testkit.WithProviders(perplexity.Name))
	created := perplexityWire(t, sim, http.MethodPost, "/v1/agent", backgroundRequest, key)
	require.NoError(t, created.err)
	require.Equal(t, http.StatusOK, created.status, "body: %.200s", created.body)
	afterwards := answer(t, perplexityWire(t, sim, http.MethodPost, "/v1/agent", syncRequest, key))
	assert.NotContains(t, afterwards, "Searching for Report A.", "the background create already spent call 0")
	assert.Contains(t, afterwards, "No further information.", "call 1 matches no predicate, so the fallback answers")
}
