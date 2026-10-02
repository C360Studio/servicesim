package provider

import (
	"fmt"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/scenario"
)

// maxCancelTries bounds how many times [CancelJob] tries to record one cancel.
//
// A try fails only when another poll of the SAME job claimed and advanced
// between the cancel reading the job's position and its compare-and-set, so each
// retry means a poll landed. Eight in a row takes a storm of concurrent polls on
// one job — a client polling its own run in a tight loop from many goroutines —
// and the bound exists so that storm costs the cancel a loud 500 rather than an
// unbounded spin inside a request.
const maxCancelTries = 8

// CancelOutcome is what [CancelJob] decided about one cancel request. The
// profile renders it: CancelJob decides and records, and knows nothing of any
// vendor's wire shape.
type CancelOutcome string

// The [CancelOutcome] values.
//
// For the first four, the turn CancelJob returns is EXACTLY the snapshot the
// job's next poll will be served — the invariant that makes "cancel before
// poll" and "poll before cancel" the only two ways a race can resolve. The
// profile renders a served, fault-eligible response for every outcome.
const (
	// CancelRecorded means the job was not terminal and this request recorded
	// the cancel at the job's current poll position. The turn is the first
	// snapshot of the cancel script, cancel.turns[0].
	CancelRecorded CancelOutcome = "recorded"

	// CancelUncommitted means the job was not terminal but this request's fault
	// attempt does not commit — it loses the response and is not marked
	// accepted — so nothing was recorded, exactly as a vendor that never
	// received the cancel would record nothing. The turn is the job's next poll
	// snapshot from its own turns; the scripted fault replaces whatever the
	// profile renders from it.
	CancelUncommitted CancelOutcome = "uncommitted"

	// CancelAlreadyCancelling means a cancel was recorded earlier. Nothing new
	// is recorded. The turn is the cancel script's snapshot for the job's next
	// poll.
	CancelAlreadyCancelling CancelOutcome = "already_cancelling"

	// CancelTerminal means no cancel is recorded and the job's next poll is
	// terminal: completion wins and nothing is recorded. The turn is that
	// terminal snapshot. It needs no cancel script.
	CancelTerminal CancelOutcome = "terminal"

	// CancelFailed means the cancel could not be decided, so nothing was
	// recorded on the job, and CancelJob has recorded an error finding that says
	// why: [CodeJobCancelUnscripted] when the cancel would take effect but the
	// scenario cannot answer the polls that follow it, or
	// [CodeJobCancelContended] when the job's position kept moving and CancelJob
	// gave up after [maxCancelTries], or when the job store answered an outcome
	// outside its contract. The profile answers the vendor's 500; the
	// reason is the finding's, not the outcome's, because the profile renders
	// both the same way. There is no turn.
	CancelFailed CancelOutcome = "failed"

	// CancelNotFound means the job is gone — a reset landed after ResolveJob —
	// or, with a [CodeJobIDInvalid] error, the request never resolved one. The
	// profile answers its vendor's not-found. There is no turn.
	CancelNotFound CancelOutcome = "not_found"
)

// SelectPollTurn selects the snapshot serving one poll of the job this request
// resolved, and returns it with its YAML path — base.turns[i], or
// base.cancel.turns[j] once a cancel is recorded — or a nil turn and "" when
// nothing can be served.
//
// turns is the job's poll script and cancel the block beside it (nil when there
// is none). base is the YAML path of the block that holds them —
// providers.<entry> for an entry's own scripts, or a nested path such as
// providers.<entry>.background — and is used only in the returned path and in
// finding messages, so a lifecycle nested inside an entry needs no entry of its
// own.
//
// It owns the poll's claim AND its record on the job, so an async profile never
// calls CallIndex on a poll route itself:
//
//  1. It claims the poll lane's call index, exactly as [SelectTurnFor] does.
//  2. It records that index on the job IMMEDIATELY and UNCONDITIONALLY
//     (jobs.Store.Advance), including when step 3 then fails. The index is
//     spent either way, and the job's Polls must equal its poll lane's claimed
//     count, or the snapshot a cancel judges "next" is computed from the wrong
//     position.
//  3. If a cancel is recorded and this poll's index i is at or past the
//     position it was recorded at, the snapshot is cancel.turns at
//     i-CancelAtPoll; otherwise it is turns at i. Both are selected by
//     [SelectTurn]'s rule, so inside cancel.turns `when.call_index` counts polls
//     since the cancel. The journal's attempt index stays absolute.
//
// A failed selection records [CodeNoMatchingTurn] and returns a nil turn, as
// SelectTurnFor does; the profile renders its own error.
//
// Selection reads the poll's position alone, never a request body, which is what
// lets [CancelJob] peek the same snapshot this will serve. A request that did not
// resolve a job through [ResolveJob] is a programming error: it records a
// [CodeJobIDInvalid] error, claims nothing, and returns a nil turn. A job that
// vanished after it was resolved — a reset landed — is served from its own turns
// at the claimed index; a request in flight across a reset is undefined (see
// jobs.Store).
func SelectPollTurn(
	x *Exchange, base string, turns []scenario.Turn, cancel *scenario.CancelPolicy,
) (*scenario.Turn, string) {
	if x.resolvedJob == "" {
		x.Fail(CodeJobIDInvalid, "",
			"SelectPollTurn was called on a request that resolved no job; call ResolveJob first and poll only when it returns true")
		return nil, ""
	}

	index := x.CallIndex()
	job, _ := x.Deps.Jobs.Advance(x.Lane().Namespace, x.resolvedJob, index)

	script, path, at := pollScript(base, turns, cancel, job, index)
	turn, i, err := selectTurn(script, at, x.Route.FaultKey, nil)
	if err != nil {
		x.Fail(CodeNoMatchingTurn, "", "no turn in %s matches poll %d of job %q (call %d of that script) on route %q",
			path, index, x.resolvedJob, at, x.Route.FaultKey)
		return nil, ""
	}
	return turn, fmt.Sprintf("%s[%d]", path, i)
}

// pollScript returns the script a poll at position index of job is served from,
// that script's YAML path, and the call index within it.
func pollScript(
	base string, turns []scenario.Turn, cancel *scenario.CancelPolicy, job jobs.Job, index int,
) ([]scenario.Turn, string, int) {
	if job.CancelRequested && index >= job.CancelAtPoll {
		return cancelTurns(cancel), base + ".cancel.turns", index - job.CancelAtPoll
	}
	return turns, base + ".turns", index
}

// cancelTurns is cancel's script, nil-safe.
func cancelTurns(cancel *scenario.CancelPolicy) []scenario.Turn {
	if cancel == nil {
		return nil
	}
	return cancel.Turns
}

// CancelJob decides and records one cancel of the job this request resolved,
// and returns what it decided together with the snapshot the profile renders and
// that snapshot's YAML path. It is provider-agnostic: base, turns and cancel are
// the job's scripts exactly as [SelectPollTurn] takes them; pollRoute is the poll
// route's FaultKey as the profile declares it, so a `when.route:` in the scripts
// selects exactly as it would on the poll; terminal reports whether a snapshot
// ends the job, in the profile's own vocabulary (a snapshot it cannot decode is
// not terminal — load has already refused it).
//
// # The claim rule
//
// Every cancel that resolved a job claims EXACTLY ONE attempt from its cancel
// lane, first, whatever it then decides — so the index a client's retry draws
// depends on how many cancels it sent, never on the job's state. The response
// the profile renders is a served, fault-eligible one in every outcome; it is
// never built as a rejection, which would strip the attempt. ([CancelFailed]
// records an error finding, and Handle strips any response's attempt on an
// error; the index is still spent.)
//
// # The steps
//
//   - Already cancelling: the snapshot is cancel.turns at Polls-CancelAtPoll.
//   - Not yet cancelled: peek turns at Polls, claiming nothing. Terminal: the
//     outcome is [CancelTerminal] and nothing is recorded. Otherwise, if the
//     attempt does not commit, [CancelUncommitted]: nothing is recorded.
//     Otherwise peek cancel.turns[0] and record the cancel at Polls with
//     jobs.Store.MarkCancel: [CancelRecorded].
//   - A peek that matches nothing, or a cancel with no cancel.turns to record
//     against: [CodeJobCancelUnscripted], [CancelFailed], nothing recorded.
//   - MarkCancel finding the position moved means a poll advanced in between:
//     re-peek and retry, at most [maxCancelTries] times, then
//     [CodeJobCancelContended] and [CancelFailed] with nothing recorded.
//   - MarkCancel answering an outcome outside its contract: no retry,
//     [CodeJobCancelContended] with a message naming the outcome and the
//     store as the cause, and [CancelFailed] with nothing recorded.
//     Finding a cancel already recorded answers [CancelAlreadyCancelling].
//   - The job gone (a reset landed): [CancelNotFound].
//
// "Commits" is the predicate [MintJob] keeps a job on: the client receives the
// response, or the attempt is marked `accepted` — the cancel took effect and its
// reply was lost. The fault decision is cached on the Exchange, so this and
// Handle read the same attempt.
//
// A request that did not resolve a job through [ResolveJob] is a programming
// error: it records a [CodeJobIDInvalid] error, claims nothing, and returns
// [CancelNotFound].
func CancelJob(
	x *Exchange, base string, turns []scenario.Turn, cancel *scenario.CancelPolicy,
	pollRoute string, terminal func(*scenario.Turn) bool,
) (CancelOutcome, *scenario.Turn, string) {
	if x.resolvedJob == "" {
		x.Fail(CodeJobIDInvalid, "",
			"CancelJob was called on a request that resolved no job; call ResolveJob first and cancel only when it returns true")
		return CancelNotFound, nil, ""
	}

	// The claim comes first and is the only one: every path below is a served
	// response to exactly this attempt.
	commit := commits(x.Fault())
	x.recordable = true

	namespace, id := x.Lane().Namespace, x.resolvedJob
	// The poll route's key as THIS listener serves it, so an instanced
	// listener's peek selects as its own polls do.
	route := namespacedFaultKey(x.Provider, x.kind, pollRoute)
	scripted := cancelTurns(cancel)

	peek := func(script []scenario.Turn, name string, at int) (*scenario.Turn, string, bool) {
		turn, i, err := selectTurn(script, at, route, nil)
		if err != nil {
			return nil, "", false
		}
		return turn, fmt.Sprintf("%s.%s[%d]", base, name, i), true
	}
	unscripted := func(format string, args ...any) (CancelOutcome, *scenario.Turn, string) {
		x.Fail(CodeJobCancelUnscripted, "", "the cancel of job %q could not be scripted: "+format+
			"; nothing was recorded", append([]any{id}, args...)...)
		return CancelFailed, nil, ""
	}

	cancelling := func(job jobs.Job) (CancelOutcome, *scenario.Turn, string) {
		at := job.Polls - job.CancelAtPoll
		turn, path, ok := peek(scripted, "cancel.turns", at)
		if !ok {
			return unscripted("no turn in %s.cancel.turns answers its next poll (call %d of that script)",
				base, at)
		}
		return CancelAlreadyCancelling, turn, path
	}

	job, ok := x.Deps.Jobs.Lookup(namespace, id)
	if !ok {
		return CancelNotFound, nil, ""
	}
	for range maxCancelTries {
		if job.CancelRequested {
			return cancelling(job)
		}

		next, nextPath, ok := peek(turns, "turns", job.Polls)
		if !ok {
			return unscripted("no turn in %s.turns answers its next poll (call %d), so whether it is "+
				"terminal cannot be judged", base, job.Polls)
		}
		if terminal(next) {
			return CancelTerminal, next, nextPath
		}
		if !commit {
			return CancelUncommitted, next, nextPath
		}

		first, firstPath, ok := peek(scripted, "cancel.turns", 0)
		if !ok {
			return unscripted("%s scripts no cancel.turns snapshot for the poll after a cancel "+
				"(call 0 of that script)", base)
		}

		after, outcome := x.Deps.Jobs.MarkCancel(namespace, id, job.Polls)
		switch outcome {
		case jobs.Marked:
			return CancelRecorded, first, firstPath
		case jobs.AlreadyMarked:
			// Another cancel won the compare-and-set; this one answers as a
			// repeat of it.
			return cancelling(after)
		case jobs.NotFound:
			return CancelNotFound, nil, ""
		case jobs.PositionMoved:
			// A poll landed in between, so the next snapshot may differ.
			// Re-peek from the record MarkCancel returned.
			job = after
			continue
		}
		// Anything else is outside jobs.Store's contract — a broken store, not
		// contention — so it is reported as itself and never retried.
		x.Fail(CodeJobCancelContended, "",
			"the cancel of job %q was not recorded: the job store answered MarkCancel with outcome %q, which is "+
				"none of %q, %q, %q or %q; that is a jobs.Store implementation bug, and nothing was recorded",
			id, outcome, jobs.Marked, jobs.AlreadyMarked, jobs.PositionMoved, jobs.NotFound)
		return CancelFailed, nil, ""
	}

	x.Fail(CodeJobCancelContended, "",
		"the cancel of job %q was not recorded: its poll position moved %d times in a row while the cancel was "+
			"being recorded, each time because another poll of the same job landed; nothing was recorded, and a "+
			"retried cancel starts afresh", id, maxCancelTries)
	return CancelFailed, nil, ""
}
