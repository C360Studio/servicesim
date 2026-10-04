package scenarios_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/c360studio/servicesim/profiles/perplexity"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/scenarios"
	"github.com/c360studio/servicesim/testkit"
)

// The journal labels the Perplexity profile gives a cancel of a background run.
// The journal keeps no response bodies, so the label is how a consumer sees which
// of a cancel's four answers it got. The profile keeps them unexported, so these
// assert on the strings, as a consumer would.
const (
	cancelLabelAccepted = "perplexity.agent.cancel.accepted"
	cancelLabelRepeated = "perplexity.agent.cancel.repeated"
	cancelLabelTerminal = "perplexity.agent.cancel.terminal"
	labelUnauthorized   = "perplexity.agent.error.401"
	labelNotFound       = "perplexity.agent.error.404"
)

// ghostRunID is a well-formed identifier no job backs.
const ghostRunID = "resp_00000000000000000000000000000000"

// firstRetrieveTerminal reports whether the first snapshot the entry's background
// script serves a retrieve is already terminal. A cancel of such a run is answered
// 400 and records nothing, so the run needs no cancel script.
func firstRetrieveTerminal(t *testing.T, e *scenario.ProviderEntry) bool {
	t.Helper()

	probe := &scenario.ProviderEntry{Turns: e.Background.Turns}
	first, _, err := provider.SelectTurn(probe, 0, perplexityRetrieveRoute, nil)
	require.NoError(t, err, "the background script must answer its first retrieve")
	return terminalBackgroundStatus(decodeBackgroundSnapshot(t, first).status())
}

// pendingAtFirstRetrieve is every built-in whose background run is not terminal at
// its first retrieve: the ones a cancel can land on, which is the rule the static
// guard and the end-to-end tests share. Deriving it, rather than naming the
// built-ins, keeps both from drifting when a script gains or loses its pending
// snapshots.
func pendingAtFirstRetrieve(t *testing.T) []string {
	t.Helper()

	var names []string
	for _, name := range scenarios.Names() {
		entry := loadBuiltin(t, name).Provider(perplexity.NameAgent)
		require.NotNilf(t, entry, "%s declares no %q block", name, perplexity.NameAgent)
		require.NotNilf(t, entry.Background, "%s declares no background block: TestBuiltins_ABackgroundRunCanBeRetrieved fails first", name)
		if !firstRetrieveTerminal(t, entry) {
			names = append(names, name)
		}
	}
	require.NotEmpty(t, names, "no built-in scripts a pending background run, so the cancel guards would check nothing")
	return names
}

// backgroundCancelGap reports why a cancel of one of this entry's background runs
// would not be answered by a script the corpus can stand behind, or "" when it
// would be. A cancel is answered when the run is terminal at its first retrieve
// (completion wins: it answers 400 and records nothing, and a cancel script
// beside it is one nothing reads), or when the entry scripts what the retrieves
// after the cancel serve. The one case left is the old gap: a run still pending
// at its first retrieve and no `cancel:` block, which the framework fails closed
// with 500 and job.cancel_unscripted.
//
// The script the corpus teaches is an acknowledgement and then a stop: the first
// retrieve after a cancel is still pending, because an acknowledgement is not
// proof billing stopped, and the run ends at `cancelled` with the usage and cost
// it billed up to then. An entry with no background block is backgroundGap's.
func backgroundCancelGap(t *testing.T, e *scenario.ProviderEntry) string {
	t.Helper()

	if e.Background == nil || len(e.Background.Turns) == 0 {
		return ""
	}
	cancel := e.Background.Cancel

	if firstRetrieveTerminal(t, e) {
		if cancel != nil {
			return "the first retrieve is terminal, so a cancel answers 400 and background.cancel is a script nothing reads"
		}
		return ""
	}

	if cancel == nil || len(cancel.Turns) == 0 {
		return "the first retrieve is not terminal and the entry has no background.cancel.turns, " +
			"so a cancel answers 500 job.cancel_unscripted"
	}
	if last := cancel.Turns[len(cancel.Turns)-1]; lastTurnCanMiss(last.When) {
		return "the last background.cancel.turns snapshot is conditional, so a retrieve past it matches no turn " +
			"and a repeated cancel there answers 500"
	}

	probe := &scenario.ProviderEntry{Turns: cancel.Turns}
	var served []backgroundSnapshot
	for poll := range backgroundPollsToWalk(cancel.Turns) {
		turn, _, err := provider.SelectTurn(probe, poll, perplexityRetrieveRoute, nil)
		if err != nil {
			return fmt.Sprintf("retrieve %d since the cancel is served no snapshot, so it answers 404 for a job that exists", poll)
		}
		served = append(served, decodeBackgroundSnapshot(t, turn))
	}
	if first := served[0].status(); terminalBackgroundStatus(first) {
		return fmt.Sprintf("the first retrieve after a cancel is served a %q snapshot: an acknowledgement is not proof "+
			"the run stopped, so the corpus acknowledges first and confirms on a later retrieve", first)
	}
	last := served[len(served)-1]
	if status := last.status(); status != "cancelled" {
		return fmt.Sprintf("background.cancel.turns ends in a %q snapshot, not a cancelled one", status)
	}
	if reason := last.scriptsBilling(); reason != "" {
		return "the cancelled snapshot " + reason + ": the usage accrued before a cancel is billed"
	}

	// The corpus scripts total_cost: left to the renderer it is the float sum of the
	// two parts, and 0.0001 + 0.00005 renders as 0.00015000000000000001.
	cost := last.Usage.Cost
	switch {
	case cost.TotalCost <= 0:
		return "the cancelled snapshot scripts no total_cost, so the renderer sums the two costs in float64 " +
			"and may print a long tail (0.0001 + 0.00005 is 0.00015000000000000001)"
	case math.Abs(cost.TotalCost-(cost.InputCost+cost.OutputCost)) > 1e-12:
		return fmt.Sprintf("the cancelled snapshot scripts total_cost %v, which is not the sum of its parts %v + %v",
			cost.TotalCost, cost.InputCost, cost.OutputCost)
	}
	return ""
}

// TestBuiltins_ABackgroundRunStillPendingCanBeCancelled keeps the reference corpus
// answering a cancel: a built-in whose background run is not terminal at its first
// retrieve scripts a cancel that ends in `cancelled` with the usage and cost it
// billed, so a consumer that tries a cancel on the shipped corpus gets a run back
// rather than the framework's fail-closed 500. A built-in whose run starts
// terminal needs none and carries none.
//
// The first subtests prove the check matches what it says it matches; a check
// that matched nothing would pass forever.
func TestBuiltins_ABackgroundRunStillPendingCanBeCancelled(t *testing.T) {
	t.Parallel()

	const (
		done = "{status: completed, answer: ok, usage: {input_tokens: 1, output_tokens: 2, cost: {input_cost: 0.1, output_cost: 0.2}}}"
		ack  = "{status: in_progress}"
		stop = "{status: cancelled, usage: {input_tokens: 1, output_tokens: 1, cost: {input_cost: 0.1, output_cost: 0.1, total_cost: 0.2}}}"
	)
	pending := "      turns:\n        - when: {call_index: 0}\n          respond: {status: queued}\n        - respond: " + done + "\n"
	terminalFirst := "      turns:\n        - respond: " + done + "\n"
	cancel := func(turns ...string) string {
		out := "      cancel:\n        turns:\n"
		for _, turn := range turns {
			out += "          - " + turn + "\n"
		}
		return out
	}
	parse := func(t *testing.T, background string) *scenario.ProviderEntry {
		t.Helper()
		s, report, err := scenario.Parse([]byte(
			"version: 1\nname: probe\nproviders:\n  perplexity_agent:\n    answer: x\n    background:\n" + background))
		require.NoErrorf(t, err, "%v", report.Findings)
		return s.Provider(perplexity.NameAgent)
	}

	for _, tc := range []struct {
		name string
		body string
		want string // a fragment of the reason, or "" for no gap
	}{
		{"pending with no cancel block", pending, "no background.cancel.turns"},
		{"pending with an empty cancel block", pending + "      cancel: {}\n", "no background.cancel.turns"},
		{"pending with a cancel that ends in progress", pending + cancel("respond: "+ack), `ends in a "in_progress"`},
		{"pending with a cancel that ends completed",
			pending + cancel("when: {call_index: 0}\n            respond: "+ack, "respond: "+done), `ends in a "completed"`},
		{"pending with a cancel that ends failed",
			pending + cancel("when: {call_index: 0}\n            respond: "+ack, "respond: {status: failed}"), `ends in a "failed"`},
		{"pending with a cancel whose last turn is conditional",
			pending + cancel("when: {call_index: 0}\n            respond: "+stop), "conditional"},
		{"pending with a cancel that ends cancelled",
			pending + cancel("when: {call_index: 0}\n            respond: "+ack, "respond: "+stop), ""},
		{"pending with a cancel that is cancelled at once",
			pending + cancel("respond: "+stop), "acknowledgement is not proof"},
		{"pending with a cancelled snapshot that scripts no usage",
			pending + cancel("when: {call_index: 0}\n            respond: "+ack, "respond: {status: cancelled}"), "scripts no usage"},
		{"pending with a cancelled snapshot that scripts usage but no cost",
			pending + cancel("when: {call_index: 0}\n            respond: "+ack,
				"respond: {status: cancelled, usage: {input_tokens: 1, output_tokens: 1}}"), "usage without a cost"},
		{"pending with a cancelled snapshot that scripts a zero cost",
			pending + cancel("when: {call_index: 0}\n            respond: "+ack,
				"respond: {status: cancelled, usage: {input_tokens: 1, output_tokens: 1, cost: {input_cost: 0, output_cost: 0}}}"),
			"zero cost"},
		{"pending with a cancelled snapshot that scripts no total_cost",
			pending + cancel("when: {call_index: 0}\n            respond: "+ack,
				"respond: {status: cancelled, usage: {input_tokens: 1, output_tokens: 1, cost: {input_cost: 0.1, output_cost: 0.1}}}"),
			"scripts no total_cost"},
		{"pending with a cancelled snapshot whose total_cost is not the sum of its parts",
			pending + cancel("when: {call_index: 0}\n            respond: "+ack,
				"respond: {status: cancelled, usage: {input_tokens: 1, output_tokens: 1, cost: {input_cost: 0.1, output_cost: 0.1, total_cost: 0.5}}}"),
			"not the sum of its parts"},
		{"pending with a cancelled snapshot that scripts no token counts",
			pending + cancel("when: {call_index: 0}\n            respond: "+ack,
				"respond: {status: cancelled, usage: {cost: {input_cost: 0.1, output_cost: 0.1}}}"), "no token counts"},
		{"terminal first needs no cancel block", terminalFirst, ""},
		{"failed first needs no cancel block", "      turns:\n        - respond: {status: failed, error: {message: x}}\n", ""},
		{"terminal first must not carry a cancel block", terminalFirst + cancel("respond: "+stop), "nothing reads"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := backgroundCancelGap(t, parse(t, tc.body))
			if tc.want == "" {
				assert.Empty(t, got)
				return
			}
			assert.Contains(t, got, tc.want)
		})
	}

	for _, name := range scenarios.Names() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entry := loadBuiltin(t, name).Provider(perplexity.NameAgent)
			require.NotNilf(t, entry, "%s declares no %q block", name, perplexity.NameAgent)
			require.NotNilf(t, entry.Background, "%s declares no background block", name)
			assert.Emptyf(t, backgroundCancelGap(t, entry), "%s: providers.%s.background", name, perplexity.NameAgent)

			// A fault on a cancel turn is a load error, and the cancel route's own plan is
			// not the shared corpus's to script: it would fault every built-in's cancel
			// for a reason unrelated to the built-in's story.
			if c := entry.Background.Cancel; c != nil {
				assert.Falsef(t, c.Fault.HasAttempts(), "%s: background.cancel.fault", name)
			}
		})
	}
}

// cancelSnapshots is the cancel script of one entry: for each retrieve of a job
// since its cancel was recorded, the snapshot background.cancel.turns serves. It
// runs the same walk retrieveSnapshots runs over background.turns, and ends where
// the cancel script's unconditional last turn takes over.
func cancelSnapshots(t *testing.T, e *scenario.ProviderEntry) []backgroundSnapshot {
	t.Helper()

	require.NotNil(t, e.Background.Cancel, "TestBuiltins_ABackgroundRunStillPendingCanBeCancelled fails first")
	return retrieveSnapshots(t, &scenario.ProviderEntry{
		Background: &scenario.BackgroundPolicy{Turns: e.Background.Cancel.Turns},
	})
}

// firstDeliveredCreate is the index of the first create of the walk whose attempt
// delivers a queued snapshot, so mints a job the client learns the identifier of,
// or -1 when no create does: the scenario faults the create for good, or refuses
// every credential. An attempt that accepts without answering keeps a job whose
// identifier the client never learns, and no built-in scripts one.
func firstDeliveredCreate(s *scenario.Scenario, e *scenario.ProviderEntry) int {
	if a := e.Auth; a != nil && a.Mode == scenario.AuthReject {
		return -1
	}
	plan := provider.TurnFault(s, perplexity.NameAgent)
	for n := range createsToWalk(plan) {
		if createAttempt(plan, n).DeliversBody() {
			return n
		}
	}
	return -1
}

// mintsAJob reports whether some create of the built-in's walk delivers a job.
func mintsAJob(t *testing.T, name string) bool {
	t.Helper()

	s := loadBuiltin(t, name)
	return firstDeliveredCreate(s, s.Provider(perplexity.NameAgent)) >= 0
}

// cancelRun is one built-in under test: a simulator started on it, the requests
// sent so far (the labels the journal must show, in order), and the job the walk
// of creates minted ("" when it mints none).
type cancelRun struct {
	t        *testing.T
	sim      *testkit.Sim
	s        *scenario.Scenario
	entry    *scenario.ProviderEntry
	key      string
	wrongKey string // "" unless the scenario expects one key
	labels   []string
	id       string
	dead     []string // identifiers a cut body still carries, which no job backs
}

// send sends one request as an application under test does and records the journal
// label the profile must give it.
func (r *cancelRun) send(method, path, body, key, label string) wire {
	r.t.Helper()
	r.labels = append(r.labels, label)
	return perplexityWire(r.t, r.sim, method, path, body, key)
}

// startCancelRun starts the simulator on a built-in and sends the creates up to the
// first one that delivers a job, each checked against the attempt the scenario
// scripts for it. When no create delivers one, it sends the whole walk, and when
// every credential is refused it sends one create and expects the 401.
func startCancelRun(t *testing.T, name string) *cancelRun {
	t.Helper()

	s := loadBuiltin(t, name)
	entry := s.Provider(perplexity.NameAgent)
	require.NotNil(t, entry)
	r := &cancelRun{
		t: t, s: s, entry: entry, key: "test-perplexity-key",
		// WithSkippedDelays: the scenarios' delays (30s) are asserted through the
		// journal's DelayMS in TestBuiltins_ABackgroundRunIsCreatedAndRetrieved, not paid.
		sim: testkit.Start(t, testkit.WithProfiles(referenceProfiles()...),
			testkit.WithBuiltin(name), testkit.WithProviders(perplexity.Name), testkit.WithSkippedDelays()),
	}
	if a := entry.Auth; a != nil && a.ExpectKey != "" {
		r.key, r.wrongKey = a.ExpectKey, "not-"+a.ExpectKey
	}

	if a := entry.Auth; a != nil && a.Mode == scenario.AuthReject {
		res := r.send(http.MethodPost, "/v1/agent", backgroundRequest, r.key, labelUnauthorized)
		require.NoError(t, res.err)
		require.Equal(t, http.StatusUnauthorized, res.status, "body: %.200s", res.body)
		return r
	}

	plan := provider.TurnFault(s, perplexity.NameAgent)
	deliver := firstDeliveredCreate(s, entry)
	for n := range createsToWalk(plan) {
		a := createAttempt(plan, n)
		require.Falsef(t, a.Accepted, "create %d scripts accepted: true, whose effect on a cancel has not been measured", n)
		res := r.send(http.MethodPost, "/v1/agent", backgroundRequest, r.key, agentJournalLabelCreated)
		id := assertCreateOutcome(t, s, a, res)
		if a.EffectiveKind() == scenario.FaultTruncateBody && a.TruncateAfterBytes == 0 {
			// The default cut is half the body, and the identifier is its first key.
			dead := backgroundIDPattern.FindString(string(res.body))
			require.NotEmpty(t, dead, "a default truncation keeps the identifier: %.200s", res.body)
			r.dead = append(r.dead, dead)
		}
		if n == deliver {
			require.NotEmpty(t, id, "create %d delivers a queued snapshot", n)
			r.id = id
			break
		}
		require.Empty(t, id, "create %d is scripted not to deliver a job", n)
	}
	return r
}

// cancel sends a cancel of the run the way the specification declares it: a POST
// with no body, and so no Content-Type either.
func (r *cancelRun) cancel(id, key, label string) wire {
	r.t.Helper()
	res := r.send(http.MethodPost, "/v1/agent/"+id+"/cancel", "", key, label)
	require.NoError(r.t, res.err)
	return res
}

// retrieve retrieves the run and checks the snapshot against the one the scenario
// scripts for it. It returns the body.
func (r *cancelRun) retrieve(want backgroundSnapshot) []byte {
	r.t.Helper()
	res := r.send(http.MethodGet, "/v1/agent/"+r.id, "", r.key, agentJournalLabelRetrieved+want.status())
	require.NoError(r.t, res.err)
	require.Equal(r.t, http.StatusOK, res.status, "body: %.300s", res.body)
	assertSnapshot(r.t, r.s, res.body, r.id, want)
	return res.body
}

// requireAcknowledged checks a cancel's 200: exactly the response id and the one
// status the specification's enum has, with no snapshot beside them.
func (r *cancelRun) requireAcknowledged(res wire) {
	r.t.Helper()
	require.Equal(r.t, http.StatusOK, res.status, "body: %.300s", res.body)
	var got map[string]any
	require.NoError(r.t, json.Unmarshal(res.body, &got), "body: %.300s", res.body)
	assert.Equal(r.t, map[string]any{"response_id": r.id, "status": "cancelling"}, got)
}

// requireTerminal checks a cancel of a run that has ended or is about to: the
// specification's 400, in the vendor's error shape.
func (r *cancelRun) requireTerminal(res wire) {
	r.t.Helper()
	require.Equal(r.t, http.StatusBadRequest, res.status, "body: %.300s", res.body)
	var got struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(r.t, json.Unmarshal(res.body, &got), "body: %.300s", res.body)
	assert.NotEmpty(r.t, got.Error.Message, "ErrorInfo.message is required, body: %.300s", res.body)
	var top map[string]any
	require.NoError(r.t, json.Unmarshal(res.body, &top))
	assert.Equal(r.t, []string{"error"}, slices.Collect(maps.Keys(top)), "a refused cancel carries only the error envelope")
}

// assertJournal checks the journal against the requests sent, in order: each
// request's label. A request that was answered carries no finding; a refused one
// carries the finding that says why.
func (r *cancelRun) assertJournal() {
	r.t.Helper()

	entries := r.sim.AwaitRequests(r.t, perplexity.Name, len(r.labels))
	require.Len(r.t, entries, len(r.labels))
	for i, e := range entries {
		assert.Equalf(r.t, r.labels[i], e.Outcome.Label, "request %d: %s %s", i, e.Method, e.Path)
		if e.Outcome.Label != labelUnauthorized && e.Outcome.Label != labelNotFound {
			testkit.AssertNoFindings(r.t, e)
		}
	}
}

// TestBuiltins_ACancelOfABackgroundRunReachesCancelled runs the cancel the shipped
// corpus now supports end to end, on every built-in whose background run is still
// pending at its first retrieve and whose create can mint a job (the set
// [pendingAtFirstRetrieve] derives, the same rule the static guard applies).
// Their creates differ: brownout's six attempts are four delays and two 503s,
// rate-limited's first is a 429, hang-then-abort's first three leave no job, and
// credential-rotation expects one key. The walk of creates is derived from each
// scenario's plan, so one sequence holds for all of them:
//
//   - a create, a first retrieve (queued), then a cancel, which is recorded
//     because the run's next retrieve is still pending, and answered `200
//     {"response_id":…,"status":"cancelling"}` with no snapshot beside it;
//   - the retrieves after it serve the cancel script, not the run's: each is
//     checked against the snapshot the scenario scripts for it, the first still
//     pending (an acknowledgement is not proof billing stopped) and the last
//     `cancelled` with the usage and cost it billed, and a retrieve past the end
//     answers the last again, byte for byte;
//   - a cancel repeated before each of those retrieves is judged by the snapshot
//     that retrieve is about to serve: acknowledged again while it is pending, and
//     a 400 once it is `cancelled`, so a repeat after the run is cancelled is
//     refused.
//
// The test reads every expectation from the scenario it loaded, and the wire check
// asserts a billed snapshot's cost is not a zero, so the cost cannot silently fall
// back to a placeholder. It sends requests one after another and synchronises on
// the responses and on testkit's bounded AwaitRequests; nothing here sleeps.
func TestBuiltins_ACancelOfABackgroundRunReachesCancelled(t *testing.T) {
	t.Parallel()

	for _, name := range pendingAtFirstRetrieve(t) {
		if !mintsAJob(t, name) {
			continue // TestBuiltins_ACancelOfARunNoCreateMintedIsRefused's
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := startCancelRun(t, name)
			require.NotEmpty(t, r.id)
			script, cancelScript := retrieveSnapshots(t, r.entry), cancelSnapshots(t, r.entry)
			require.False(t, terminalBackgroundStatus(script[1].status()),
				"a cancel after one retrieve is recorded only while the next retrieve is pending")
			require.False(t, terminalBackgroundStatus(cancelScript[0].status()), "TestBuiltins_ABackgroundRunStillPendingCanBeCancelled fails first")
			final := cancelScript[len(cancelScript)-1]
			require.Equal(t, "cancelled", final.status(), "TestBuiltins_ABackgroundRunStillPendingCanBeCancelled fails first")
			require.Empty(t, final.scriptsBilling(), "TestBuiltins_ABackgroundRunStillPendingCanBeCancelled fails first")

			// An identifier from a body that was cut short is a real-looking identifier no
			// job backs: a cancel of it is refused as a retrieve of it is.
			for _, dead := range r.dead {
				res := r.cancel(dead, r.key, labelNotFound)
				assert.Equal(t, http.StatusNotFound, res.status, "a cancel of an identifier no job backs: %.300s", res.body)
			}

			// The run has been polled once, so the cancel lands on its second position.
			r.retrieve(script[0])

			// A cancel refused for its credential records nothing and advances nothing.
			if r.wrongKey != "" {
				res := r.cancel(r.id, r.wrongKey, labelUnauthorized)
				assert.Equal(t, http.StatusUnauthorized, res.status)
				for _, j := range r.sim.Jobs() {
					assert.Falsef(t, j.CancelRequested, "a cancel with the wrong key was recorded for job %s", j.ID)
				}
			}

			r.requireAcknowledged(r.cancel(r.id, r.key, cancelLabelAccepted))

			var previous []byte
			var bodies [][]byte
			for k, want := range cancelScript {
				// A repeat is judged by the snapshot the next retrieve serves.
				if terminalBackgroundStatus(want.status()) {
					r.requireTerminal(r.cancel(r.id, r.key, cancelLabelTerminal))
				} else {
					r.requireAcknowledged(r.cancel(r.id, r.key, cancelLabelRepeated))
				}
				body := r.retrieve(want)
				if k == len(cancelScript)-1 {
					assert.Equal(t, string(previous), string(body),
						"a retrieve past the end of the cancel script answers the last snapshot again, byte for byte")
				}
				previous = body
				bodies = append(bodies, body)
			}

			// The cancelled snapshot's total_cost is the one the scenario scripts, exactly:
			// decoded, and as the bytes on the wire, so the float sum of its parts
			// (0.00015000000000000001) cannot come back as a long tail.
			cancelled := 0
			for k, body := range bodies {
				if cancelScript[k].status() != "cancelled" {
					continue
				}
				cancelled++
				scripted := cancelScript[k].Usage.Cost.TotalCost
				require.Positivef(t, scripted, "cancel.turns[%d] scripts a total_cost", k)
				var decoded struct {
					Usage struct {
						Cost struct {
							TotalCost float64 `json:"total_cost"`
						} `json:"cost"`
					} `json:"usage"`
				}
				require.NoError(t, json.Unmarshal(body, &decoded), "body: %.300s", body)
				assert.Equalf(t, scripted, decoded.Usage.Cost.TotalCost, "retrieve %d since the cancel: total_cost, decoded", k)
				text, err := json.Marshal(scripted)
				require.NoError(t, err)
				assert.Regexpf(t, `"total_cost":\s*`+regexp.QuoteMeta(string(text))+`[,}\s]`, string(body),
					"retrieve %d since the cancel: the wire carries total_cost %s", k, text)
				assert.NotContainsf(t, string(body), "00000000001", "retrieve %d since the cancel: a float-sum artifact", k)
			}
			require.Positive(t, cancelled, "no cancelled snapshot was checked")

			// Of the four built-ins whose name promises a behaviour, the two whose
			// promise is about what a snapshot's body carries keep it on this path too.
			// async-failed's and async-stuck's are about how the run ends, which the
			// cancel decides instead.
			switch name {
			case "malicious-content":
				// A cancelled run answered nothing, so no snapshot after the cancel carries
				// the marker vocabulary.
				for k, body := range bodies {
					for _, marker := range maliciousContentMarkers {
						assert.NotContainsf(t, string(body), marker, "retrieve %d since the cancel", k)
					}
				}
			case "extra-fields":
				for k, body := range bodies {
					var got map[string]any
					require.NoError(t, json.Unmarshal(body, &got))
					assert.Equalf(t, "trace-0", got["experimental_trace_id"], "retrieve %d since the cancel", k)
					assert.Equalf(t, "default", got["service_tier"], "retrieve %d since the cancel", k)
				}
			}

			// The cancel was recorded at the run's second position, and nothing sent after
			// it moved it.
			jobs := r.sim.Jobs()
			require.Len(t, jobs, 1)
			assert.True(t, jobs[0].CancelRequested)
			assert.Equal(t, 1, jobs[0].CancelAtPoll)
			assert.Equal(t, 1+len(cancelScript), jobs[0].Polls, "one retrieve before the cancel, one per cancel snapshot after it")
			r.assertJournal()
		})
	}
}

// TestBuiltins_ACancelOfARunNoCreateMintedIsRefused covers the built-ins whose
// background run is pending but whose create can mint no job: server-error and
// malformed-json fault the create for good, and unauthorized refuses every
// credential. There is no run to cancel, so a cancel of a well-formed identifier is
// refused as a retrieve of one is: 404, claiming nothing, or 401 where the
// scenario rejects the credential.
func TestBuiltins_ACancelOfARunNoCreateMintedIsRefused(t *testing.T) {
	t.Parallel()

	var covered []string
	for _, name := range pendingAtFirstRetrieve(t) {
		if mintsAJob(t, name) {
			continue
		}
		covered = append(covered, name)
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := startCancelRun(t, name)
			require.Empty(t, r.id, "no create of this scenario delivers a job")
			wantStatus, wantLabel := http.StatusNotFound, labelNotFound
			if a := r.entry.Auth; a != nil && a.Mode == scenario.AuthReject {
				wantStatus, wantLabel = http.StatusUnauthorized, labelUnauthorized
			}
			res := r.cancel(ghostRunID, r.key, wantLabel)
			assert.Equal(t, wantStatus, res.status, "body: %.300s", res.body)
			assert.Empty(t, r.sim.Jobs(), "no job was ever minted")
			r.assertJournal()
		})
	}
	assert.NotEmpty(t, covered, "no built-in refuses every create, so the cancel of a run nobody minted is checked nowhere")
}

// TestBuiltins_ACancelAfterABackgroundRunEndsIsRefused proves completion wins, on
// every built-in whose background run reaches a terminal snapshot and whose create
// can mint a job. A cancel is judged by the snapshot the run's next retrieve would
// serve, so it is refused with the specification's 400 both at the boundary (the
// next retrieve is the one that ends the run, which has not been served yet) and
// after the run has been retrieved to its end. Neither records a cancel, so the
// retrieves that follow stay on the run's own script and a retrieve past its end
// answers the last snapshot again, byte for byte.
//
// backgroundNeverEnds is the named exemption: its run has no terminal snapshot for a
// cancel to lose to, so every cancel of it is recorded (the previous test).
func TestBuiltins_ACancelAfterABackgroundRunEndsIsRefused(t *testing.T) {
	t.Parallel()

	var skipped []string
	for _, name := range pendingAtFirstRetrieve(t) {
		if !mintsAJob(t, name) {
			continue
		}
		script := retrieveSnapshots(t, loadBuiltin(t, name).Provider(perplexity.NameAgent))
		endsAt := slices.IndexFunc(script, func(snap backgroundSnapshot) bool { return terminalBackgroundStatus(snap.status()) })
		if endsAt < 0 {
			skipped = append(skipped, name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := startCancelRun(t, name)
			require.NotEmpty(t, r.id)

			// Every retrieve up to the one that ends the run.
			for poll := range endsAt {
				r.retrieve(script[poll])
			}
			// Completion wins at the boundary: the next retrieve is the terminal one.
			r.requireTerminal(r.cancel(r.id, r.key, cancelLabelTerminal))
			assert.False(t, r.sim.Jobs()[0].CancelRequested, "a cancel the run's end wins is not recorded")

			var last []byte
			for poll := endsAt; poll < len(script); poll++ {
				last = r.retrieve(script[poll])
			}

			// And after the run has ended, a cancel is refused again, and leaves the
			// run's script as it was.
			r.requireTerminal(r.cancel(r.id, r.key, cancelLabelTerminal))
			after := r.retrieve(script[len(script)-1])
			assert.Equal(t, string(last), string(after), "a refused cancel leaves the run on its own script")

			jobs := r.sim.Jobs()
			require.Len(t, jobs, 1)
			assert.False(t, jobs[0].CancelRequested)
			assert.Equal(t, len(script)+1, jobs[0].Polls)
			r.assertJournal()
		})
	}
	assert.Equal(t, []string{backgroundNeverEnds}, skipped, "only %s's run has no terminal snapshot for a cancel to lose to", backgroundNeverEnds)
}

// sharedCancelBlock is the cancel block the built-ins' comments describe for every
// built-in but one: the run reads in progress on the first retrieve after a cancel,
// is cancelled from the next on, and bills what it accrued before it stopped — its
// 24 input tokens and 12 output tokens, at the per-token rates the completed run
// bills, which is less than any run that ends bills — and its total_cost scripted
// (0.00015) rather than left to the renderer's float sum of the parts, with no
// answer, no sources, no extra field and no fault.
const sharedCancelBlock = `
turns:
  - when: {call_index: 0}
    respond: {status: in_progress}
  - respond:
      status: cancelled
      usage: {input_tokens: 24, output_tokens: 12, cost: {input_cost: 0.0001, output_cost: 0.00005, total_cost: 0.00015}}
`

// variantCancelBlocks are the built-ins whose cancel block is not the shared one,
// each with the block its comments describe. They are written out here, not read
// from the built-in, so a drift in either is a failure.
var variantCancelBlocks = map[string]string{
	// The shared shape, but every snapshot carries the synchronous response's two
	// extra fields, as every snapshot of its retrieve script does.
	"extra-fields": `
turns:
  - when: {call_index: 0}
    respond: {status: in_progress, extra_fields: {experimental_trace_id: trace-0, service_tier: default}}
  - respond:
      status: cancelled
      extra_fields: {experimental_trace_id: trace-0, service_tier: default}
      usage: {input_tokens: 24, output_tokens: 12, cost: {input_cost: 0.0001, output_cost: 0.00005, total_cost: 0.00015}}
`,
}

// TestBuiltins_TheCancelBlockIsTheOneTheCommentsDescribe pins the cancel block each
// built-in's own comment describes: the shared block in all but one, and its own in
// each of the built-ins in variantCancelBlocks. backgroundCancelGap accepts any
// billed script that ends cancelled, so a copy that drifted in one file would
// otherwise falsify that prose silently.
//
// It pins the built-ins a cancel can land on, pendingAtFirstRetrieve — every
// built-in today. A built-in whose run is terminal at its first retrieve is
// backgroundCancelGap's to keep free of a cancel block, so requiring one here
// would contradict it.
func TestBuiltins_TheCancelBlockIsTheOneTheCommentsDescribe(t *testing.T) {
	t.Parallel()

	documented := func(t *testing.T, block string) []map[string]any {
		t.Helper()
		var c scenario.CancelPolicy
		require.NoError(t, yaml.Unmarshal([]byte(block), &c))
		return canonicalTurns(t, c.Turns)
	}
	shared := documented(t, sharedCancelBlock)

	var sharing, varied []string
	for _, name := range pendingAtFirstRetrieve(t) {
		want := shared
		if block, ok := variantCancelBlocks[name]; ok {
			want = documented(t, block)
			require.NotEqualf(t, shared, want, "%s's documented cancel block is the shared one", name)
			varied = append(varied, name)
		} else {
			sharing = append(sharing, name)
		}
		entry := loadBuiltin(t, name).Provider(perplexity.NameAgent)
		require.NotNilf(t, entry, "%s declares no %q block", name, perplexity.NameAgent)
		require.NotNilf(t, entry.Background, "%s declares no background block", name)
		require.NotNilf(t, entry.Background.Cancel, "%s declares no background.cancel block", name)
		assert.Equalf(t, want, canonicalTurns(t, entry.Background.Cancel.Turns), "%s: providers.%s.background.cancel.turns",
			name, perplexity.NameAgent)
	}
	// A variant keyed by a name the registry does not ship, or by a built-in no
	// cancel can land on, pins nothing, and the count the comments state goes
	// stale when a built-in is added.
	assert.ElementsMatch(t, slices.Collect(maps.Keys(variantCancelBlocks)), varied)
	assert.Lenf(t, sharing, sharedCancelBuiltins, "the comments say %d built-ins share the cancel block: %v",
		sharedCancelBuiltins, sharing)
}

// sharedCancelBuiltins is how many built-ins the comments say share
// sharedCancelBlock.
const sharedCancelBuiltins = 19
