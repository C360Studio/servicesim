package provider

import (
	"errors"

	"github.com/c360studio/servicesim/scenario"
)

// ErrNoMatchingTurn reports that no turn in a provider's script matched the
// request and the script declares no unconditional fallback. It is an authoring
// problem in the scenario, surfaced per request because it depends on the
// request.
var ErrNoMatchingTurn = errors.New("provider: no turn matches this request")

// SelectTurn returns the first turn whose When matches, along with its index.
// Turns are evaluated in declaration order. When no turn matches, the last turn
// with a nil When is used; if there is none, the caller gets ErrNoMatchingTurn,
// records a scenario.no_matching_turn finding and answers with a provider-shaped
// error — never a panic and never an empty 200.
//
// callIndex comes from the same per-FaultKey counter the fault engine uses, so a
// scenario that rate-limits call 2 and answers differently on call 3 stays
// coherent. Pass Exchange.CallIndex, never a counter of your own.
//
// The nil-When fallback is belt and braces: a turn with no When already matches
// everything, so the loop would have returned it. It is written out because the
// alternative to "unreachable and obvious" is "reachable and silent" the first
// time Match grows an axis whose zero value stops matching.
func SelectTurn(e *scenario.ProviderEntry, callIndex int, body []byte) (*scenario.Turn, int, error) {
	if e == nil || len(e.Turns) == 0 {
		return nil, -1, ErrNoMatchingTurn
	}
	for i := range e.Turns {
		if e.Turns[i].When.Matches(callIndex, body) {
			return &e.Turns[i], i, nil
		}
	}
	for i := len(e.Turns) - 1; i >= 0; i-- {
		if e.Turns[i].When == nil {
			return &e.Turns[i], i, nil
		}
	}
	return nil, -1, ErrNoMatchingTurn
}

// SelectTurnFor selects the turn serving x from e, claiming the call index from
// the single per-FaultKey counter that fault selection also draws on. It records
// the scenario.no_matching_turn error finding and returns a nil turn when the
// script cannot answer, leaving the caller to render its own provider-shaped
// error body.
//
// Call it only once the request has passed validation: it claims an attempt, and
// a rejected request must not consume one (§4.4).
func SelectTurnFor(x *Exchange, e *scenario.ProviderEntry) (*scenario.Turn, int) {
	index := x.CallIndex()
	turn, at, err := SelectTurn(e, index, x.Raw)
	if err != nil {
		x.Fail(CodeNoMatchingTurn, "", "no turn in provider %q matches call %d", entryName(e), index)
		return nil, -1
	}
	return turn, at
}

// TurnFault returns the fault plan a route's attempt budget is drawn from: the
// first turn of the named provider that declares one. It is nil-safe on every hop
// — the scenario, the registry, the entry and its turns may each be absent —
// which is why it is a helper rather than an inline chain of field reads that
// would panic on a partial scenario.
//
// A single-shot provider block normalises into exactly one turn, so for the
// common case this is simply that block's `fault:`. For a multi-turn script the
// engine is handed one plan per fault key and cannot yet select a different plan
// per turn; the first declared plan is what that key expands. A script that needs
// per-turn plans is the turn-lane work the addendum defers, not something a
// caller can express today.
func TurnFault(s *scenario.Scenario, name string) *scenario.Fault {
	e := s.Provider(name)
	if e == nil {
		return nil
	}
	for i := range e.Turns {
		if e.Turns[i].Fault.HasAttempts() {
			return e.Turns[i].Fault
		}
	}
	return nil
}

// entryName names an entry for a finding message, tolerating a nil entry so the
// message is still useful when the scenario declares no such provider at all.
func entryName(e *scenario.ProviderEntry) string {
	if e == nil {
		return "(undeclared)"
	}
	return e.Name
}
