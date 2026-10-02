// Package pollscript holds the load-time checks over an entry's scripts that
// more than one reference profile needs: that a terminal snapshot is absorbing
// in the order polls are actually SERVED, and rejecting a `cancel:` block on an
// entry that has no cancel.
//
// It is internal on purpose. Each check is the reference profiles' policy, not
// framework surface (CLAUDE.md house rule 7), and it grants no capability an
// out-of-tree profile lacks: both are pure functions over exported types, built
// on provider.SelectTurn, which any profile can call. It may import provider and
// scenario; neither may import it.
package pollscript

import (
	"fmt"
	"slices"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Regression is one poll that is served a non-terminal snapshot although an
// earlier poll was served a terminal one: a job that un-completes.
type Regression struct {
	// Poll is the first poll position at which Turn regresses.
	Poll int
	// Turn is the index, in the script, of the non-terminal turn served there.
	Turn int
	// TerminalPoll is the earliest poll position served a terminal snapshot.
	TerminalPoll int
	// TerminalTurn is the index of the turn served at TerminalPoll.
	TerminalTurn int
}

// TerminalRegressions evaluates a poll script BY INDEX — the turn
// provider.SelectTurn would actually serve each poll on route, with no body —
// and reports every turn served non-terminal after a terminal one, once each, in
// poll order.
//
// Declaration order is not serve order. Turns are selected by first match on
// call_index with an unconditional fallback, so a script declared pending-then-
// terminal can serve terminal, pending, terminal; walking the declarations
// misses it, and under "terminal at cancel time" a client could watch a run
// finish and then have it cancelled (D3,
// docs/proposals/cancellation-and-accepted-create.md).
//
// terminal reports whether turn i's snapshot is terminal, in the profile's own
// vocabulary, and whether that is known at all: a turn whose projection did not
// decode is reported by its own finding and neither sets nor breaks the order
// here.
//
// It is equivalent to walking polls 0 through the highest call_index plus one,
// without the walk: a poll's selection can differ from its predecessor's only
// at a position some call_index names or the position just after one, so those
// positions — and 0 — are the only ones evaluated, and a script that names
// call_index 1000000 costs no more than one that names 1.
func TerminalRegressions(turns []scenario.Turn, route string, terminal func(turn int) (isTerminal, known bool)) []Regression {
	positions := []int{0}
	for i := range turns {
		if w := turns[i].When; w != nil && w.CallIndex != nil && *w.CallIndex >= 0 {
			positions = append(positions, *w.CallIndex, *w.CallIndex+1)
		}
	}
	slices.Sort(positions)
	positions = slices.Compact(positions)

	entry := &scenario.ProviderEntry{Turns: turns}
	reported := make([]bool, len(turns))
	var out []Regression
	first := Regression{TerminalPoll: -1}

	for _, poll := range positions {
		_, at, err := provider.SelectTurn(entry, poll, route, nil)
		if err != nil {
			// The script runs out here; that is its own finding, and nothing is
			// served to compare.
			continue
		}
		isTerminal, known := terminal(at)
		switch {
		case !known:
		case isTerminal:
			if first.TerminalPoll < 0 {
				first.TerminalPoll, first.TerminalTurn = poll, at
			}
		case first.TerminalPoll >= 0 && !reported[at]:
			reported[at] = true
			out = append(out, Regression{
				Poll: poll, Turn: at, TerminalPoll: first.TerminalPoll, TerminalTurn: first.TerminalTurn,
			})
		}
	}
	return out
}

// RejectCancel returns one load error, under the calling profile's own code,
// when e declares a `cancel:` block, and nil otherwise.
//
// scenario decodes `cancel:` on any entry because which entries have a cancel
// is a profile's knowledge. A profile calls this from the validator of every
// entry it serves no cancel on, so a block nothing would ever read stops the
// load instead of letting its author believe a cancellation was scripted.
func RejectCancel(e *scenario.ProviderEntry, code string) []scenario.Finding {
	if e == nil || e.Cancel == nil {
		return nil
	}
	return []scenario.Finding{{
		Severity: scenario.SeverityError,
		Code:     code,
		Path:     "providers." + e.Name + ".cancel",
		Message: fmt.Sprintf("entry %q serves no cancel operation, so this cancel: block could never take effect; "+
			"remove it, or move it to an entry whose profile serves a cancel", e.Name),
	}}
}
