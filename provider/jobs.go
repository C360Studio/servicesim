package provider

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/c360studio/servicesim/internal/jobs"
)

// Job identifier bounds.
const (
	// MaxJobIDLen bounds a job identifier taken from a request path.
	//
	// It is deliberately shorter than maxLaneValueLen (128), so a well-formed
	// identifier can never be the thing appendLanePart drops for length. If the
	// two were equal, a legal identifier at the boundary would silently fall back
	// to the shared route lane and two jobs would answer from one cursor.
	//
	// Both vendors fit comfortably: Exa mints "agent_run_" plus 32 hex characters (42),
	// Tavily a UUID (36).
	MaxJobIDLen = 64
)

// LaneFromPath is the [Route.LaneFrom] extractor that reads a ServeMux wildcard:
// "path:id" resolves r.PathValue("id").
//
// It is reachable only through Route.LaneFrom. scenario.Validate does not accept
// it in a turn_key, and that is deliberate: an unrecognised extractor is a LOAD
// ERROR there, so a scenario file using "path:id" would be unloadable on any
// already-released binary — a hard failure in a consumer's CI rather than a
// warning. Keeping it on the route means no scenario file mentions it and no
// older binary ever sees it.
const LaneFromPath = "path:"

// Finding codes the async job surfaces raise.
const (
	// CodeJobIDInvalid is raised when a route's path-derived lane discriminator
	// resolves to nothing usable — an absent segment, or one that is not a
	// well-formed identifier.
	//
	// It is deliberately NOT CodeTurnKeyUnresolved. A Route.LaneFrom extractor is
	// declared in Go by the provider package, not in YAML by a scenario author,
	// so reporting it on the field "turn_key" would send a reader hunting through
	// their scenario for a key they never wrote and cannot add. The field is the
	// path wildcard's own name, which is the part of the request the client
	// actually got wrong.
	//
	// It is raised a second way, as an error: [SelectPollTurn] or [CancelJob]
	// called on a request that resolved no job. That is a programming error in
	// the profile — it must call [ResolveJob] first and act only when it returns
	// true — and it fails loudly rather than serving a default.
	CodeJobIDInvalid = "job.id_invalid"

	// CodeJobLimitNear warns that a create took its namespace past
	// jobs.HighWaterPercent of the bound. It fires while creates still succeed,
	// which is the entire point: without it the first signal is the create that
	// fails, and by then the request that filled the namespace is already gone.
	CodeJobLimitNear = "job.limit_near"

	// CodeJobLimitReached is raised when a create is refused because its
	// namespace is at the bound. Nothing is evicted to make room; see
	// internal/jobs for why refusing beats evicting here.
	CodeJobLimitReached = "job.limit_reached"

	// CodeJobIDCollision is raised when a create mints an identifier that is
	// already live in its namespace.
	//
	// In practice this has one cause worth naming, and the message names it:
	// something reset the fault cursors without dropping the job records, so the
	// next create claimed an index it had already used and re-minted the
	// identifier that index derives. Every surface that resets cursors must
	// reset jobs in the same call.
	CodeJobIDCollision = "job.id_collision"

	// CodeJobForeignID is raised by [ResolveJob] on a poll whose identifier is
	// SHAPED like one this process mints ([ValidJobID] holds) but resolves to
	// no record, in a namespace that has minted at least one job. The response
	// is unchanged — the vendor's ordinary 404 — this only adds a finding and a
	// log line so an intermittent miss carries a hint instead of looking like a
	// consumer bug.
	//
	// It CANNOT tell apart the three ways that happens, and the finding's
	// message must not pretend to: another replica minted the job and this
	// process never saw the create (docs/design/async-jobs.md §8), a reset
	// dropped the record without dropping the client's copy of the identifier,
	// or the identifier is one this process never minted at all — stale between
	// tests, or hand-written into a fixture. In a one-process test suite, which
	// is every supported configuration, the third is the likeliest cause; that
	// is also why this is a warning, not an error.
	//
	// It is raised a second way, with its own text: the identifier DOES resolve
	// to a record, but its create was served from another entry, so this route
	// treats it as a miss. That case can name its cause — the poll went to the
	// wrong surface — and does, naming both entries rather than listing the three
	// above.
	//
	// Like every warning, scenario validation.strict or a validation.promote
	// entry for this code turns it into an error, which fails Exchange.Failed
	// and AssertNoErrors for that request even though the response body is
	// still the vendor's ordinary 404. A suite that polls HEAD or GET for ids
	// it knows are absent should demote this code rather than run strict.
	CodeJobForeignID = "job.foreign_id"

	// CodeJobCancelUnscripted is raised by [CancelJob] when a cancel would take
	// effect but the scenario cannot say what the job's polls answer next: the
	// entry scripts no `cancel.turns`, or the snapshot the job's next poll would
	// be served — from its turns or from its cancel.turns — matches no turn. The
	// profile answers the vendor's 500 and NOTHING is recorded, so later polls
	// never index into a cancel script that does not exist. It is an error:
	// the scenario is incomplete for the request it was sent. A cancel of a run
	// that is already terminal needs no cancel script and never raises it.
	CodeJobCancelUnscripted = "job.cancel_unscripted"

	// CodeJobCancelContended is raised by [CancelJob] when it gave up recording a
	// cancel because the job's poll position kept moving under it: each retry
	// means another poll of the same job completed in between, so reaching the
	// bound takes a storm of concurrent polls on one job. The profile answers
	// the vendor's 500 and nothing is recorded; the client's retry is a fresh
	// cancel. It is an error, because the cancel the client sent did not take
	// effect for a reason no vendor would give.
	CodeJobCancelContended = "job.cancel_contended"
)

// foreignIDMessage is the [CodeJobForeignID] finding text, split at its
// semicolons so a future edit to one clause shows as one line in a diff
// rather than rewriting a single ~560-character literal.
const foreignIDMessage = "no job %q exists in namespace %q, but this namespace has minted %d job(s); " +
	"this identifier is well-formed for this process's own scheme, so the likeliest causes are: " +
	"another replica minted it and this process never saw the create (run one replica, or route stickily " +
	"on /n/<namespace>); a reset dropped the record without dropping the client's copy of the identifier " +
	"(POST /__admin/reset drops a namespace's jobs along with its fault cursors); or the client sent an " +
	"identifier this process never minted at all — stale between tests, or hand-written into a fixture " +
	"— check the fixture id"

// wrongEntryMessage is the [CodeJobForeignID] finding text for a job that exists
// but whose create was served from a different entry. Unlike foreignIDMessage it
// can state its cause, because here the cause is known rather than one of several.
const wrongEntryMessage = "job %q exists in namespace %q but its create was served from entry %q, not %q; " +
	"a job resolves only through the entry its create was served from, so poll it through that entry's own route"

// ValidJobID reports whether id is safe to use as a job identifier and as a
// component of a lane key: one to [MaxJobIDLen] characters of ASCII letters,
// digits, '-' and '_'.
//
// The charset is load-bearing rather than cosmetic. A path value reaches this
// after http.ServeMux has PERCENT-DECODED it, so "GET /agent/runs/run%2Fabc"
// arrives as "run/abc" — a literal separator inside what the caller believes is
// one segment. A lane key joins its namespace with '/', and SplitCursorKey
// splits on it, so an unvalidated identifier can be re-split into a different
// (namespace, key) pair and read another namespace's state. Rejecting the
// separators closes that at the only point where the value is still known to be
// one segment.
//
// It is a shape check, not a scheme check. It says an identifier COULD be one
// this simulator minted; it cannot say that one WAS. Exa's "agent_run_" prefix and
// Tavily's UUID layout are each provider knowledge, and a caller wanting that
// precision has to ask the provider package.
func ValidJobID(id string) bool {
	if id == "" || len(id) > MaxJobIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// deliversBody reports whether the client of a create will receive the job
// identifier: no attempt means the response is untouched, and an attempt answers
// through [scenario.FaultAttempt.DeliversBody], which owns the truth table and
// the reasoning behind each row.
//
// It is the predicate MintJob commits on. FaultDecision.Faulted() is the obvious
// reach and is wrong here: it is true whenever Delay > 0, and a pure delay writes
// the body after sleeping. The question is not "was this request faulted" but
// "does the client still receive what the handler rendered".
//
// The prediction assumes a create that does not stream and whose response stays
// fault-eligible; see [MintJob]. TestDeliveryTableNamesEveryFaultKind and
// TestACreateLeavesAJobExactlyWhenTheClientHoldsItsIdentifier keep the method
// honest against a real client.
func deliversBody(dec FaultDecision) bool {
	return dec.Attempt == nil || dec.Attempt.DeliversBody()
}

// commits reports whether the job a create mints is kept: the client will
// receive the identifier ([deliversBody]), or the attempt says the request took
// effect regardless ([scenario.FaultAttempt.Accepted]) — the accepted-create,
// lost-reply case, where a job exists that no response ever names.
//
// It is the single predicate MintJob keeps a job on. The two halves are not
// redundant at runtime even though load rejects `accepted` on a delivering
// attempt: the same attempt reaches this from a hand-built scenario that was
// never validated, and the answer there must still be "kept".
func commits(dec FaultDecision) bool {
	return deliversBody(dec) || (dec.Attempt != nil && dec.Attempt.Accepted)
}

// MintJob claims this request's call index and records a job derived from it,
// returning the identifier and whether the request may proceed.
//
// False means the request was refused and a finding says why; the caller renders
// its own provider-shaped error. True means the handler should render normally —
// including when the record was deliberately not written, because a scripted
// fault is about to replace the response anyway.
//
// entry feeds the identifier's derivation and nothing else, so a stable constant
// — the profile's own package constant — is the right thing to pass. The job
// records the name of the entry THIS create was served from (its Route.Entry, or
// the listener's own name when the route names none), and [ResolveJob] resolves
// it only for a request served from that same name. A profile's create and poll
// routes must therefore be served from the same entry; a poll served from any
// other — another async surface, or another instance of the same profile — is a
// miss, and its [CodeJobForeignID] finding names both entries.
//
// # The claim always happens; the record does not
//
// The call index is claimed unconditionally, so a fault plan advances exactly as
// scripted and the retry after a faulted create draws attempt 1. The RECORD is
// written only when the response will actually carry the identifier to the
// client ([deliversBody]) or the attempt is marked accepted ([commits]).
//
// An accepted attempt is how a scenario scripts "the request took effect, the
// reply was lost": the job is kept whatever the attempt does to the response.
// Whether the client still learns the identifier depends on the shape —
// truncate_body sends a prefix of the rendered body, which can carry it. It is
// not idempotency. The client's retry claims the next attempt and mints a
// SECOND job under a different identifier, and a namespace at its job bound
// refuses the accepted create like any other, so the scripted fault is not
// applied. With a nil Deps.Jobs no job state exists and nothing is kept at all.
//
// A profile whose create may run under an accepted attempt must register a
// [Response.FaultBody] built from the attempt alone — never from the rendered
// body or the minted identifier. A create with no FaultBody serves its own
// rendered body, identifier included, under an accepted status of 400 or above,
// and the client learns the id of a job the scenario said it lost.
//
// Faults are applied by Handle AFTER the handler returns, so a create that
// committed unconditionally would leave a record behind on every faulted
// attempt: a plan with one retry would burn two slots per usable job, and a
// bound of 256 would become an effective 128 — reached sooner than any author
// computed, reporting a number that does not match the jobs they can see.
//
// Identifiers are therefore not dense across a faulted create: a plan that
// faults the first attempt produces a first live job whose identifier derives
// from index 1. That is correct, for the same reason every other faulted route's
// derived identifier moves with the attempt, and it is why goldens ignore
// derived identifiers.
//
// One honest limit: a client whose own deadline fires during a scripted delay
// receives nothing, yet the record is committed — the decision said this attempt
// would serve, and that was true when it was made. No predicate evaluated before
// the write can know otherwise.
//
// # What the record decision assumes
//
// It is made before the handler has rendered anything or Handle has applied a
// fault, so it rests on two things the handler must keep true. The create does
// not stream: [scenario.FaultAttempt.DeliversBody]'s table is for a response with no Stream, and
// against a stream a stream_* kind applies instead of being dropped while
// truncate_body and oversized_body are the ones reported unreachable. And the
// create's response stays fault-eligible: Handle clears a claimed attempt on a
// response that opts out of faults, so a handler that returned FaultEligible
// false after MintJob would be served unfaulted while the record was decided as
// if the fault applied. Every in-tree create satisfies both.
func MintJob(x *Exchange, entry, prefix string, encode func(...string) string) (id string, ok bool) {
	lane := x.Lane()
	index := x.CallIndex()
	x.recordable = true

	job := jobs.Job{
		ID: prefix + encode(
			x.Deps.Scenario.SeedKey(),
			entry,
			lane.Key,
			"job",
			strconv.Itoa(index),
		),
		Namespace:   lane.Namespace,
		Entry:       x.entryName(),
		LaneKey:     lane.Key,
		CreateIndex: index,
		CreatedAt:   x.Deps.Clock.Now(),
	}

	// A derived identifier that is not a valid job identifier would record fine
	// and never resolve: ResolveJob rejects it before reaching the store, so
	// every poll would 404 on a job the create reported making. That is a
	// programming error in the caller's prefix or encoder rather than anything a
	// request did, and it is caught here because the alternative is discovering
	// it as an unexplained 404 in a consumer's suite.
	if !ValidJobID(job.ID) {
		x.Fail(CodeJobIDInvalid, "",
			"provider %q derived the job identifier %q, which is not a valid identifier; a poll could never resolve it",
			entry, job.ID)
		return "", false
	}

	if !commits(x.Fault()) {
		// The claim stands, the record does not. The handler still renders a body
		// so the response has a shape; Handle replaces it with the fault before
		// any of it reaches the client.
		return job.ID, true
	}

	store := x.Deps.Jobs
	if store == nil {
		// A zero Deps serves without job state at all, matching how a nil Faults
		// serves without faults. The identifier is still derived and returned, so
		// a create answers normally; only the poll that follows cannot resolve it.
		return job.ID, true
	}

	stats, err := store.Create(job)
	switch {
	case errors.Is(err, jobs.ErrDuplicate):
		x.Fail(CodeJobIDCollision, "",
			"job %q is already live in namespace %q; the usual cause is a reset that dropped the fault cursors without dropping the job records, so this create re-minted an identifier it had already used",
			job.ID, job.Namespace)
		return "", false

	case errors.Is(err, jobs.ErrLimit):
		x.Fail(CodeJobLimitReached, "",
			"namespace %q holds its maximum of %d jobs; reset it with POST /__admin/reset, give each test its own namespace, or raise the bound",
			job.Namespace, stats.Bound)
		return "", false

	case err != nil:
		x.Fail(CodeJobLimitReached, "", "recording the job failed: %v", err)
		return "", false
	}

	if stats.Near() {
		x.Warn(CodeJobLimitNear, "",
			"namespace %q holds %d of %d jobs; creates still succeed, but reset it or use per-test namespaces before it fills",
			job.Namespace, stats.Count, stats.Bound)
	}
	return job.ID, true
}

// ResolveJob reports whether an identifier names a live job in this request's
// namespace whose create was served from THIS request's entry.
//
// A job resolves only through the entry its create was served from: the name
// [MintJob] recorded, compared with the one this request is served from
// (Route.Entry, or the listener's own name when the route names none; see
// [Exchange.Entry]). The store keys a record by namespace and identifier alone,
// and every async entry mints into the same namespace, so without the comparison
// one entry's poll resolves another's job, serves it from the wrong script and
// claims attempts on a lane that job was never minted for. Both names are
// served-from names, never the constant a handler passes MintJob, which is how
// an instanced listener — Name "acme-fallback", Kind "acme" — polls its own jobs
// while the primary listener cannot. A profile's create and poll routes must
// therefore be served from the same entry. A job found under a different entry is
// a miss exactly like an unknown identifier: the provider's ordinary 404, with
// the wire left byte-identical so a client learns nothing about which surface the
// identifier belongs to.
//
// It CLAIMS NO ATTEMPT and advances no cursor, which is what makes it usable
// from a request that must not consume a poll — a HEAD asking only whether a run
// exists. A poll that means to consume one calls [SelectPollTurn] separately,
// after this has confirmed the job is real, and a cancel calls [CancelJob]. Both
// act on the job this resolved, which it retains on the Exchange; neither takes
// an identifier of its own.
//
// It records one finding, [CodeJobForeignID], on exactly two conditions, each
// logged once at WARN as servicesim.job_foreign:
//
//   - the identifier is shaped like one this provider mints and resolves to no
//     record, in a namespace that has minted at least one job; or
//   - a record exists but its create was served from another entry. The
//     finding says so, naming both entries, because the first condition's text —
//     that no such job exists, and the replica, reset and stale-fixture causes it
//     lists — would be false and would send the reader away from the one fix that
//     applies: poll it through the entry that served the create.
//
// Every other miss records nothing: a malformed identifier is never this
// process's own, and a miss in a namespace that has minted nothing is a typo,
// not a divergence. Whether the 404 itself carries anything beyond this is the
// provider's decision, and the vendors differ.
func ResolveJob(x *Exchange, id string) bool {
	if x.Deps.Jobs == nil || !ValidJobID(id) {
		return false
	}

	namespace := x.Lane().Namespace
	entry := x.entryName()
	if job, found := x.Deps.Jobs.Lookup(namespace, id); found {
		if job.Entry == entry {
			x.resolvedJob = id
			return true
		}
		x.Warn(CodeJobForeignID, "id", wrongEntryMessage, id, namespace, job.Entry, entry)
		x.Deps.Logger.Warn("servicesim.job_foreign",
			slog.String("provider", string(x.Provider)),
			slog.String("namespace", namespace),
			slog.String("id", id),
			slog.String("minted_by", job.Entry),
			slog.String("polled_as", entry))
		return false
	}

	if stats := x.Deps.Jobs.StatsIn(namespace); stats.Count > 0 {
		x.Warn(CodeJobForeignID, "id", foreignIDMessage, id, namespace, stats.Count)
		x.Deps.Logger.Warn("servicesim.job_foreign",
			slog.String("provider", string(x.Provider)),
			slog.String("namespace", namespace),
			slog.String("id", id))
	}

	return false
}
