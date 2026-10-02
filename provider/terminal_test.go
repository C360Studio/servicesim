package provider

import (
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"

	"github.com/c360studio/servicesim/scenario"
)

// snap is one turn of a test script: its predicate and whether its snapshot is
// terminal. unknown stands for a turn whose projection did not decode.
type snap struct {
	callIndex *int
	route     string
	terminal  bool
	unknown   bool
}

func at(i int) *int { return &i }

func script(snaps ...snap) ([]scenario.Turn, func(int) (bool, bool)) {
	turns := make([]scenario.Turn, len(snaps))
	for i, s := range snaps {
		if s.callIndex != nil || s.route != "" {
			turns[i].When = &scenario.Match{CallIndex: s.callIndex, Route: s.route}
		}
		turns[i].Respond = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	return turns, func(i int) (bool, bool) { return snaps[i].terminal, !snaps[i].unknown }
}

func TestTerminalRegressions(t *testing.T) {
	t.Parallel()

	const pollRoute = "acme:jobs.poll"

	tests := []struct {
		name  string
		snaps []snap
		want  []TerminalRegression
	}{
		{
			name:  "pending, pending, terminal is absorbing",
			snaps: []snap{{callIndex: at(0)}, {callIndex: at(1)}, {terminal: true}},
		},
		{
			// D3's reproduction: declared pending-then-terminal, served
			// terminal, pending, terminal.
			name:  "declared in order but served out of it",
			snaps: []snap{{callIndex: at(1)}, {terminal: true}},
			want:  []TerminalRegression{{Poll: 1, Turn: 0, TerminalPoll: 0, TerminalTurn: 1}},
		},
		{
			name:  "declared terminal-then-pending, never served that way",
			snaps: []snap{{callIndex: at(0), terminal: true}, {callIndex: at(0)}, {terminal: true}},
		},
		{
			name:  "a fallback served after a scheduled terminal snapshot",
			snaps: []snap{{callIndex: at(2), terminal: true}, {}},
			want:  []TerminalRegression{{Poll: 3, Turn: 1, TerminalPoll: 2, TerminalTurn: 0}},
		},
		{
			name: "each offending turn is reported once, at its first poll",
			snaps: []snap{
				{callIndex: at(0), terminal: true}, {callIndex: at(1)}, {callIndex: at(3)}, {},
			},
			want: []TerminalRegression{
				{Poll: 1, Turn: 1, TerminalPoll: 0, TerminalTurn: 0},
				{Poll: 2, Turn: 3, TerminalPoll: 0, TerminalTurn: 0},
				{Poll: 3, Turn: 2, TerminalPoll: 0, TerminalTurn: 0},
			},
		},
		{
			name:  "a turn for another route is never served on a poll",
			snaps: []snap{{route: "create", terminal: true}, {}},
		},
		{
			name:  "a script that runs out serves nothing past its end",
			snaps: []snap{{callIndex: at(0), terminal: true}},
		},
		{
			name:  "a turn that did not decode neither sets nor breaks the order",
			snaps: []snap{{callIndex: at(0), unknown: true}, {callIndex: at(1), terminal: true}, {unknown: true}},
		},
		{
			name:  "a huge call_index is evaluated without walking every poll before it",
			snaps: []snap{{callIndex: at(1 << 40), terminal: true}, {}},
			want:  []TerminalRegression{{Poll: 1<<40 + 1, Turn: 1, TerminalPoll: 1 << 40, TerminalTurn: 0}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			turns, terminal := script(tc.snaps...)
			assert.Equal(t, tc.want, TerminalRegressions(turns, pollRoute, terminal))
		})
	}
}

// TestTerminalRegressionsKnowsEveryAxisOfMatch pins the assumption
// TerminalRegressions rests on. It evaluates only poll 0, every named
// call_index and each of those plus one, which is exact only while call_index is
// the one axis of scenario.Match that varies from one poll of a job to the
// next: a poll's route is fixed, and it carries no body, so a body predicate
// never matches. A new axis — anything a poll could vary on without a body —
// would let the served turn change at a position the check never evaluates, and
// a script that un-terminates would load clean. This test fails the day Match
// grows a field, so the candidate positions are revisited rather than silently
// wrong.
func TestTerminalRegressionsKnowsEveryAxisOfMatch(t *testing.T) {
	t.Parallel()

	known := map[string]string{
		"Route":        "fixed for every poll of a job: the poll route",
		"CallIndex":    "the position axis the candidates are built from",
		"BodyContains": "never matches a poll, whose body is nil",
		"BodyJSON":     "never matches a poll, whose body is nil",
	}
	typ := reflect.TypeFor[scenario.Match]()
	var fields []string
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		fields = append(fields, name)
		if _, ok := known[name]; !ok {
			t.Errorf("scenario.Match has a new field %q. TerminalRegressions evaluates polls 0, c and c+1 for each "+
				"named call_index c, which is exact only while call_index is the one axis that varies across a "+
				"job's polls. Decide whether %q can vary from one poll to the next with no request body; if it "+
				"can, derive its positions in TerminalRegressions too. Then add it to this test's list.", name, name)
		}
	}
	for name := range known {
		assert.True(t, slices.Contains(fields, name),
			"scenario.Match no longer has %q; revisit TerminalRegressions' candidate positions and this list", name)
	}
}
