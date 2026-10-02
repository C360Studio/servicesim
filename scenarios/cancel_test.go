package scenarios_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/profiles/exa"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/testkit"
)

// exaPollRoute is the fault key of Exa's agent-run poll route, which is what a
// turn's `when.route:` selects on and so what picks the snapshot a cancel judges
// "terminal at cancel time" by. The profile keeps it unexported, so this asserts
// on the string, as a consumer would.
const exaPollRoute = "exa:agent_runs.poll"

// scriptedRunStatus is the status a turn's respond body scripts. An empty one is
// a pending snapshot, which the projection reads as running.
func scriptedRunStatus(t *testing.T, turn *scenario.Turn) string {
	t.Helper()
	var body struct {
		Status string `yaml:"status"`
	}
	require.NoError(t, turn.Respond.Decode(&body))
	if body.Status == "" {
		return "running"
	}
	return body.Status
}

// scriptedStopReason is the stop_reason a turn's respond body scripts, "" when it
// leaves the reason to the renderer.
func scriptedStopReason(t *testing.T, turn *scenario.Turn) string {
	t.Helper()
	var body struct {
		StopReason string `yaml:"stop_reason"`
	}
	require.NoError(t, turn.Respond.Decode(&body))
	return body.StopReason
}

// terminalRunStatus is the set of statuses a run stops at.
func terminalRunStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

// firstPollTerminal reports whether the first snapshot the entry serves a poll is
// already terminal. It asks the poll route's key, because a turn's `when.route:`
// picks the snapshot by route and a create-only turn is never what a poll sees.
func firstPollTerminal(t *testing.T, e *scenario.ProviderEntry) bool {
	t.Helper()

	first, _, err := provider.SelectTurn(e, 0, exaPollRoute, nil)
	require.NoError(t, err, "the poll script must answer its first poll")
	return terminalRunStatus(scriptedRunStatus(t, first))
}

// runningAtFirstPoll is every built-in that declares an Exa agent-run script whose
// first-served snapshot is not terminal: the ones a cancel can land on while the
// run is running, which is the rule the static guard and the end-to-end tests
// share. Deriving it, rather than naming the built-ins, keeps both from drifting
// when a script gains or loses its running turns.
func runningAtFirstPoll(t *testing.T) []string {
	t.Helper()

	var names []string
	for _, name := range builtins {
		if e := loadBuiltin(t, name).Provider(exa.NameAgentRuns); e != nil && !firstPollTerminal(t, e) {
			names = append(names, name)
		}
	}
	require.NotEmpty(t, names, "no built-in scripts a running Exa run, so the cancel guards would check nothing")
	return names
}

// cancelGap reports why a cancel of one of this entry's runs would not be
// answered, or "" when it would be. A cancel is answered when the run's first
// poll is already terminal (completion wins and no cancel is recorded), or when
// the entry scripts what the polls after the cancel serve. The one case left is
// the shipped corpus's old gap: a run still running at its first poll and no
// `cancel:` block, which the framework fails closed with `500` and
// `job.cancel_unscripted`.
func cancelGap(t *testing.T, e *scenario.ProviderEntry) string {
	t.Helper()

	if firstPollTerminal(t, e) {
		return ""
	}

	if e.Cancel == nil || len(e.Cancel.Turns) == 0 {
		return "the first poll is not terminal and the entry has no cancel.turns, so a cancel answers 500 job.cancel_unscripted"
	}
	last := e.Cancel.Turns[len(e.Cancel.Turns)-1]
	if !last.When.IsEmpty() {
		return "the last cancel.turns snapshot is conditional, so a poll past it matches no turn"
	}
	if status := scriptedRunStatus(t, &last); status != "cancelled" {
		return fmt.Sprintf("cancel.turns ends in a %q snapshot, not a cancelled one", status)
	}
	return ""
}

// TestBuiltins_AnExaRunStillRunningCanBeCancelled keeps the reference corpus
// answering a cancel: a built-in whose Exa run is not terminal at its first poll
// scripts a cancel that ends in `cancelled`, so a consumer that tries a cancel on
// the shipped corpus gets a run back rather than the framework's fail-closed
// 500. A built-in whose run starts terminal needs none.
//
// The first subtests prove the check matches what it says it matches; a check
// that matched nothing would pass forever.
func TestBuiltins_AnExaRunStillRunningCanBeCancelled(t *testing.T) {
	t.Parallel()

	parse := func(t *testing.T, body string) *scenario.ProviderEntry {
		t.Helper()
		s, report, err := scenario.Parse([]byte("version: 1\nname: probe\nproviders:\n  exa_agent_runs:\n" + body))
		require.NoErrorf(t, err, "%v", report.Findings)
		return s.Provider(exa.NameAgentRuns)
	}

	for _, tc := range []struct {
		name string
		body string
		want string // a fragment of the reason, or "" for no gap
	}{
		{"running with no cancel block", "    status: running\n", "no cancel.turns"},
		{"running with an empty cancel block", "    status: running\n    cancel: {}\n", "no cancel.turns"},
		{"running with a cancel that ends running",
			"    status: running\n    cancel:\n      turns:\n        - respond: {status: running}\n",
			`ends in a "running"`},
		{"running with a cancel that ends completed",
			"    status: running\n    cancel:\n      turns:\n        - respond: {status: completed}\n",
			`ends in a "completed"`},
		{"running with a cancel whose last turn is conditional",
			"    status: running\n    cancel:\n      turns:\n        - when: {call_index: 0}\n          respond: {status: cancelled}\n",
			"conditional"},
		{"running with a cancel that ends cancelled",
			"    status: running\n    cancel:\n      turns:\n        - respond: {status: cancelled}\n", ""},
		{"running first, completed later, with no cancel block",
			"    turns:\n      - when: {call_index: 0}\n        respond: {status: running}\n      - respond: {status: completed}\n",
			"no cancel.turns"},
		{"running on the poll route only, then completed, with no cancel block",
			"    turns:\n      - when: {route: \"" + exaPollRoute + "\"}\n        respond: {status: running}\n      - respond: {status: completed}\n",
			"no cancel.turns"},
		{"running on the poll route only, then completed, with a cancel that ends cancelled",
			"    turns:\n      - when: {route: \"" + exaPollRoute + "\"}\n        respond: {status: running}\n      - respond: {status: completed}\n" +
				"    cancel:\n      turns:\n        - respond: {status: cancelled}\n", ""},
		{"running on the create route only is not what a poll sees",
			"    turns:\n      - when: {route: \"exa:agent_runs.create\"}\n        respond: {status: running}\n      - respond: {status: completed}\n", ""},
		{"terminal first needs no cancel block", "    status: completed\n", ""},
		{"failed first needs no cancel block", "    status: failed\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cancelGap(t, parse(t, tc.body))
			if tc.want == "" {
				assert.Empty(t, got)
				return
			}
			assert.Contains(t, got, tc.want)
		})
	}

	for _, name := range builtins {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entry := loadBuiltin(t, name).Provider(exa.NameAgentRuns)
			require.NotNilf(t, entry, "%s declares no %q block", name, exa.NameAgentRuns)
			assert.Emptyf(t, cancelGap(t, entry), "%s: providers.%s", name, exa.NameAgentRuns)

			// The renderer derives a terminal snapshot's stopReason and renders null for
			// a non-terminal one, whatever is scripted, so a stop_reason here changes
			// nothing on the wire and no end-to-end test can see it. The corpus scripts
			// none, so a built-in's cancel teaches the derived shape.
			if entry.Cancel != nil {
				for i := range entry.Cancel.Turns {
					assert.Emptyf(t, scriptedStopReason(t, &entry.Cancel.Turns[i]),
						"%s: cancel.turns[%d] scripts stop_reason, which the renderer derives", name, i)
				}
			}
		})
	}
}

// exaCall sends one request to the Exa listener the way an application under test
// does and returns the status and the whole body.
func exaCall(t *testing.T, sim *testkit.Sim, method, path, body string) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, sim.URL(exa.Name)+path, reader)
	require.NoError(t, err)
	req.Header.Set("x-api-key", "test-exa-key")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := sim.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, raw
}

// TestBuiltins_ACancelOfAnExaRunReachesCancelled runs the cancel the shipped
// corpus now supports end to end, on every built-in whose Exa run is still running
// at its first poll (the set [runningAtFirstPoll] derives, the same rule the static
// guard applies). They differ in how the run is scripted, `happy` running twice and
// then completing, `async-stuck` being the single-shot never-terminal form with a
// cancel block beside it, `async-failed` running once and then failing, but none of
// them faults its agent-run create or poll, so one sequence holds for all.
//
// The cancel is recorded at the run's position and acknowledged as still running,
// because an acknowledgement is not proof billing stopped; the polls that follow
// confirm `cancelled` with the usage and cost the built-in scripts. The test reads
// every expectation from the scenario it loaded and asserts it is not a zero, so
// the billed cost cannot silently fall back to a placeholder. It sends requests
// one after another and synchronises on the responses and on testkit's bounded
// AwaitRequests; nothing here sleeps.
func TestBuiltins_ACancelOfAnExaRunReachesCancelled(t *testing.T) {
	t.Parallel()

	for _, name := range runningAtFirstPoll(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// What the built-in scripts for a cancelled run.
			var want struct {
				Usage struct {
					AgentComputeUnits float64 `yaml:"agent_compute_units"`
					Searches          int     `yaml:"searches"`
				} `yaml:"usage"`
				Cost struct {
					Total float64 `yaml:"total"`
				} `yaml:"cost_dollars"`
			}
			entry := loadBuiltin(t, name).Provider(exa.NameAgentRuns)
			require.NotNil(t, entry.Cancel)
			last := entry.Cancel.Turns[len(entry.Cancel.Turns)-1]
			require.NoError(t, last.Respond.Decode(&want))
			require.Positive(t, want.Cost.Total, "the cancelled snapshot scripts the cost it billed")
			require.Positive(t, want.Usage.AgentComputeUnits, "and the usage it accrued")

			sim := testkit.Start(t, testkit.WithProfiles(referenceProfiles()...),
				testkit.WithBuiltin(name), testkit.WithProviders(exa.Name))

			run := func(raw []byte) map[string]any {
				t.Helper()
				var out map[string]any
				require.NoError(t, json.Unmarshal(raw, &out), "body: %s", raw)
				return out
			}

			status, raw := exaCall(t, sim, http.MethodPost, "/agent/runs", `{"query":"find the finding"}`)
			require.Equal(t, http.StatusOK, status, "create: %s", raw)
			id, _ := run(raw)["id"].(string)
			require.NotEmpty(t, id)
			cancelPath := "/agent/runs/" + id + "/cancel"
			pollPath := "/agent/runs/" + id

			// The cancel: accepted, and acknowledged as still running.
			status, firstCancel := exaCall(t, sim, http.MethodPost, cancelPath, "")
			require.Equal(t, http.StatusOK, status, "a cancel of a running built-in run: %s", firstCancel)
			assert.Equal(t, "running", run(firstCancel)["status"])
			assert.Nil(t, run(firstCancel)["stopReason"], "an acknowledgement is not a stop")

			// A second cancel is a repeat and answers the same snapshot.
			status, secondCancel := exaCall(t, sim, http.MethodPost, cancelPath, "")
			require.Equal(t, http.StatusOK, status, "a repeated cancel: %s", secondCancel)
			assert.Equal(t, string(firstCancel), string(secondCancel), "a repeated cancel answers the same snapshot")

			// The polls: the acknowledgement, then cancelled, and it stays cancelled.
			_, acknowledged := exaCall(t, sim, http.MethodGet, pollPath, "")
			assert.Equal(t, string(firstCancel), string(acknowledged),
				"a cancel answers exactly the snapshot the next poll serves")
			_, confirmed := exaCall(t, sim, http.MethodGet, pollPath, "")
			final := run(confirmed)
			assert.Equal(t, "cancelled", final["status"])
			assert.Equal(t, "cancelled", final["stopReason"])
			assert.NotNil(t, final["completedAt"], "a cancelled run is terminal")
			assert.Equal(t, want.Cost.Total, final["costDollars"].(map[string]any)["total"],
				"the cancelled run reports the cost the built-in scripts, not a placeholder")
			usage := final["usage"].(map[string]any)
			assert.Equal(t, want.Usage.AgentComputeUnits, usage["agentComputeUnits"], "usage accrued before the cancel is billed")
			assert.EqualValues(t, want.Usage.Searches, usage["searches"])
			_, stays := exaCall(t, sim, http.MethodGet, pollPath, "")
			assert.Equal(t, string(confirmed), string(stays), "a cancelled run stays cancelled")

			// The cancel was recorded at the run's position, before any poll.
			jobs := sim.Jobs()
			require.Len(t, jobs, 1)
			assert.True(t, jobs[0].CancelRequested)
			assert.Equal(t, 0, jobs[0].CancelAtPoll)
			assert.Equal(t, 3, jobs[0].Polls)

			entries := sim.AwaitRequests(t, exa.Name, 6)
			var labels []string
			for _, e := range entries {
				labels = append(labels, e.Outcome.Label)
				testkit.AssertNoFindings(t, e)
			}
			assert.Equal(t, []string{
				"exa.agent_runs.created",
				"exa.agent_runs.cancel.accepted",
				"exa.agent_runs.cancel.repeated",
				"exa.agent_runs.polled.running",
				"exa.agent_runs.polled.cancelled",
				"exa.agent_runs.polled.cancelled",
			}, labels)
		})
	}
}
