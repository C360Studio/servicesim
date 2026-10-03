package scenarios_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/profiles/perplexity"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/scenarios"
)

// perplexityRetrieveRoute is the fault key of Perplexity's retrieve route, which
// is what a background turn's `when.route:` selects on. The profile keeps it
// unexported, so this asserts on the string, as a consumer would.
const perplexityRetrieveRoute = "perplexity:agent.retrieve"

// backgroundSnapshot is what the guards read off a background turn's respond
// body: the status, and the usage a billed snapshot has to carry. Pointers where
// absence is the question, so "scripted nothing" is not confused with "scripted
// zero".
type backgroundSnapshot struct {
	Status string `yaml:"status"`
	Model  string `yaml:"model"`
	Answer string `yaml:"answer"`
	Usage  *struct {
		InputTokens  int `yaml:"input_tokens"`
		OutputTokens int `yaml:"output_tokens"`
		TotalTokens  int `yaml:"total_tokens"`
		Cost         *struct {
			InputCost  float64 `yaml:"input_cost"`
			OutputCost float64 `yaml:"output_cost"`
			TotalCost  float64 `yaml:"total_cost"`
		} `yaml:"cost"`
	} `yaml:"usage"`
}

// decodeBackgroundSnapshot reads one background turn's respond body.
func decodeBackgroundSnapshot(t *testing.T, turn *scenario.Turn) backgroundSnapshot {
	t.Helper()
	var snap backgroundSnapshot
	require.NoError(t, turn.Respond.Decode(&snap))
	return snap
}

// status is the status the snapshot reports. An absent one is completed, the
// projection's zero value.
func (s backgroundSnapshot) status() string {
	if s.Status == "" {
		return "completed"
	}
	return s.Status
}

// terminalBackgroundStatus is the set of statuses a background run stops at. It
// is the profile's SIMULATOR-POLICY, restated: the vendor's Status enum has no
// terminal/non-terminal split.
func terminalBackgroundStatus(status string) bool {
	switch status {
	case "completed", "failed", "incomplete", "cancelled":
		return true
	}
	return false
}

// backgroundPollsToWalk is how many retrieves of one job are worth walking: poll
// 0, every call_index a turn names, and the polls after the last of them, which
// is where an unconditional last turn takes over. Selection varies with nothing
// else a GET carries.
func backgroundPollsToWalk(turns []scenario.Turn) int {
	last := 0
	for i := range turns {
		if w := turns[i].When; w != nil && w.CallIndex != nil {
			last = max(last, *w.CallIndex)
		}
	}
	return last + 3
}

// scriptsBilling reports why a terminal snapshot does not script what a billed
// run reports, or "" when it does: usage, with tokens, and a cost whose parts are
// scripted and not zero. An unscripted usage renders nothing, and a scripted
// zero reads as free; either hides the billing fact a consumer's cost accounting
// is under test for.
func (s backgroundSnapshot) scriptsBilling() string {
	switch {
	case s.Usage == nil:
		return "scripts no usage"
	case s.Usage.InputTokens <= 0 || s.Usage.OutputTokens <= 0:
		return "scripts no token counts"
	case s.Usage.Cost == nil:
		return "scripts usage without a cost"
	case s.Usage.Cost.InputCost <= 0 || s.Usage.Cost.OutputCost <= 0:
		return "scripts a zero cost"
	}
	return ""
}

// lastTurnCanMiss reports whether a last background turn's `when` can fail to
// match a retrieve: the rule the profile's script_exhausted warning applies. A
// route condition naming the retrieve route matches every retrieve, so only what
// is left once it is set aside can miss.
func lastTurnCanMiss(w *scenario.Match) bool {
	if w == nil {
		return false
	}
	rest := *w
	if scenario.RouteMatches(rest.Route, perplexityRetrieveRoute) {
		rest.Route = ""
	}
	return !rest.IsEmpty()
}

// backgroundGap reports why a background request on this entry would not be
// answered by a script the corpus can stand behind, or "" when it would be. A
// request is answered when the entry declares a `background:` block (otherwise
// the framework fails `background: true` closed), every retrieve of the job is
// served a snapshot, the script ends unconditional, a terminal snapshot is
// absorbing in the order retrieves are SERVED, every terminal snapshot scripts the
// usage it billed, and no turn scripts a fault.
//
// The fault rule is the one a consumer can least see. A fault on a background turn
// is read for the retrieve route only, so a fault in the shared corpus would make
// every built-in's retrieve fail for a reason unrelated to the built-in's story.
// The create's faults belong to the entry's own turns and are not this block's.
func backgroundGap(t *testing.T, e *scenario.ProviderEntry) string {
	t.Helper()

	if e.Background == nil || len(e.Background.Turns) == 0 {
		return "the entry declares no background.turns, so background: true fails closed with perplexity.agent.background.unscripted"
	}
	turns := e.Background.Turns

	for i := range turns {
		if turns[i].Fault != nil {
			return fmt.Sprintf("background.turns[%d] scripts a fault, which every retrieve of every job would draw on", i)
		}
	}
	if last := turns[len(turns)-1]; lastTurnCanMiss(last.When) {
		return "the last background.turns snapshot is conditional, so a retrieve past it matches no turn and answers 404 for a job that exists"
	}

	// The turns are selected by the rule the retrieve route applies, with no body:
	// first match, then the last unconditional turn.
	probe := &scenario.ProviderEntry{Turns: turns}
	terminalAt := -1
	var billed []int
	for poll := range backgroundPollsToWalk(turns) {
		turn, at, err := provider.SelectTurn(probe, poll, perplexityRetrieveRoute, nil)
		if err != nil {
			return fmt.Sprintf("retrieve %d is served no snapshot, so it answers 404 for a job that exists", poll)
		}
		status := decodeBackgroundSnapshot(t, turn).status()
		switch {
		case terminalBackgroundStatus(status):
			if terminalAt < 0 {
				terminalAt = poll
			}
			if !slices.Contains(billed, at) {
				billed = append(billed, at)
			}
		case terminalAt >= 0:
			return fmt.Sprintf("retrieve %d is served a %q snapshot (turn %d) after retrieve %d was served a terminal one: "+
				"a run does not un-complete", poll, status, at, terminalAt)
		}
	}
	if terminalAt < 0 {
		return "no retrieve is ever served a terminal snapshot, so a consumer polling for completion never sees it"
	}
	for _, at := range billed {
		snap := decodeBackgroundSnapshot(t, &turns[at])
		if reason := snap.scriptsBilling(); reason != "" {
			return fmt.Sprintf("the terminal snapshot (turn %d, %s) %s", at, snap.status(), reason)
		}
	}
	return ""
}

// TestBuiltins_ABackgroundRunCanBeRetrieved keeps the reference corpus answering
// `background: true`. Without a `background:` block the framework fails such a
// request closed, so a consumer that tries a background run on any shipped
// scenario would get the profile's 404 instead of a run. Every built-in therefore
// scripts the same lifecycle, with a script a consumer can poll to a terminal,
// billed snapshot.
//
// The first subtests prove the check matches what it says it matches; a check
// that matched nothing would pass forever.
func TestBuiltins_ABackgroundRunCanBeRetrieved(t *testing.T) {
	t.Parallel()

	const (
		queued  = "{status: queued}"
		running = "{status: in_progress}"
		done    = "{status: completed, answer: ok, usage: {input_tokens: 1, output_tokens: 2, cost: {input_cost: 0.1, output_cost: 0.2}}}"
	)
	turn := func(when, respond string) string {
		if when == "" {
			return "        - respond: " + respond + "\n"
		}
		return "        - when: " + when + "\n          respond: " + respond + "\n"
	}
	background := func(turns ...string) string {
		out := "    answer: x\n    background:\n      turns:\n"
		for _, tn := range turns {
			out += tn
		}
		return out
	}
	parse := func(t *testing.T, body string) *scenario.ProviderEntry {
		t.Helper()
		s, report, err := scenario.Parse([]byte("version: 1\nname: probe\nproviders:\n  perplexity_agent:\n" + body))
		require.NoErrorf(t, err, "%v", report.Findings)
		return s.Provider(perplexity.NameAgent)
	}

	for _, tc := range []struct {
		name string
		body string
		want string // a fragment of the reason, or "" for no gap
	}{
		{"no background block at all", "    answer: x\n", "no background.turns"},
		{"queued, running, completed with billing",
			background(turn("{call_index: 0}", queued), turn("{call_index: 1}", running), turn("", done)), ""},
		{"completed straight away", background(turn("", done)), ""},
		{"the last turn is conditional",
			background(turn("{call_index: 0}", queued), turn("{call_index: 1}", done)), "conditional"},
		// A route-only last turn matches every retrieve, so the script cannot run
		// out: the loader does not warn about it, and neither does this guard.
		{"a last turn conditioned only on the retrieve route",
			background(turn("{call_index: 0}", queued), turn("{route: \""+perplexityRetrieveRoute+"\"}", done)), ""},
		{"a last turn on the retrieve route that also names a call_index",
			background(turn("{call_index: 0}", queued),
				turn("{route: \""+perplexityRetrieveRoute+"\", call_index: 1}", done)), "conditional"},
		{"a script that never completes",
			background(turn("{call_index: 0}", queued), turn("", running)), "ever served a terminal"},
		{"a terminal snapshot that un-completes",
			background(turn("{call_index: 0}", done), turn("", running)), "does not un-complete"},
		{"declared pending then terminal serves terminal, pending, terminal",
			background(turn("{call_index: 1}", running), turn("", done)), "does not un-complete"},
		{"a terminal snapshot with no usage",
			background(turn("{call_index: 0}", queued), turn("", "{status: completed, answer: ok}")), "scripts no usage"},
		{"a terminal snapshot with usage but no cost",
			background(turn("", "{status: completed, answer: ok, usage: {input_tokens: 1, output_tokens: 2}}")),
			"usage without a cost"},
		{"a terminal snapshot with a zero cost",
			background(turn("",
				"{status: completed, answer: ok, usage: {input_tokens: 1, output_tokens: 2, cost: {input_cost: 0, output_cost: 0}}}")),
			"zero cost"},
		{"a terminal snapshot with no token counts",
			background(turn("", "{status: completed, answer: ok, usage: {cost: {input_cost: 0.1, output_cost: 0.2}}}")),
			"no token counts"},
		{"a second terminal snapshot without billing",
			background(turn("{call_index: 0}", queued), turn("{call_index: 1}", done),
				turn("", "{status: failed, error: {message: x}}")), "scripts no usage"},
		{"a scripted fault on a snapshot",
			background(turn("{call_index: 0}", queued),
				"        - respond: "+done+"\n          fault: {attempts: [{status: 503}]}\n"), "scripts a fault"},
		{"a scripted fault on the first snapshot",
			background("        - when: {call_index: 0}\n          respond: "+queued+
				"\n          fault: {attempts: [{status: 503}]}\n", turn("", done)), "scripts a fault"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := backgroundGap(t, parse(t, tc.body))
			if tc.want == "" {
				assert.Empty(t, got)
				return
			}
			assert.Contains(t, got, tc.want)
		})
	}

	names := scenarios.Names()
	require.NotEmpty(t, names, "no built-in ships, so the guard would check nothing")
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entry := loadBuiltin(t, name).Provider(perplexity.NameAgent)
			require.NotNilf(t, entry, "%s declares no %q block", name, perplexity.NameAgent)
			assert.Emptyf(t, backgroundGap(t, entry), "%s: providers.%s", name, perplexity.NameAgent)
		})
	}
}

// TestBuiltins_TheBackgroundBlockIsTheOneTheDocsDescribe pins the block that
// scenarios/doc.go and each built-in's own comment describe: identical in every
// built-in, queued, then in progress, then completed for good with a fixed
// answer and the usage and cost it billed, with no model, no sources and no
// fault. backgroundGap accepts any billed script that ends terminal, so a copy
// that drifted in one file would otherwise falsify that prose silently.
func TestBuiltins_TheBackgroundBlockIsTheOneTheDocsDescribe(t *testing.T) {
	t.Parallel()

	documented, report, err := scenario.Parse([]byte(`version: 1
name: probe
providers:
  perplexity_agent:
    answer: x
    background:
      turns:
        - when: {call_index: 0}
          respond: {status: queued}
        - when: {call_index: 1}
          respond: {status: in_progress}
        - respond:
            status: completed
            answer: The background run has completed.
            usage: {input_tokens: 24, output_tokens: 96, cost: {input_cost: 0.0001, output_cost: 0.0004}}
`))
	require.NoErrorf(t, err, "%v", report.Findings)

	// The respond body is decoded rather than compared as a YAML node, so two
	// files that spell the same mapping differently (flow or block style) agree.
	canonical := func(t *testing.T, b *scenario.BackgroundPolicy) []map[string]any {
		t.Helper()
		require.NotNil(t, b)
		out := make([]map[string]any, 0, len(b.Turns))
		for i := range b.Turns {
			var respond map[string]any
			require.NoError(t, b.Turns[i].Respond.Decode(&respond))
			out = append(out, map[string]any{"when": b.Turns[i].When, "fault": b.Turns[i].Fault, "respond": respond})
		}
		return out
	}
	want := canonical(t, documented.Provider(perplexity.NameAgent).Background)

	names := scenarios.Names()
	require.NotEmpty(t, names, "no built-in ships, so the guard would check nothing")
	for _, name := range names {
		entry := loadBuiltin(t, name).Provider(perplexity.NameAgent)
		require.NotNilf(t, entry, "%s declares no %q block", name, perplexity.NameAgent)
		assert.Equalf(t, want, canonical(t, entry.Background), "%s: providers.%s.background", name, perplexity.NameAgent)
	}
}
