package provider

import (
	"errors"
	"log/slog"
	"strconv"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/scenario"
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
	// Both vendors fit comfortably: Exa mints "run_" plus 32 hex characters (36),
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
	// Like every warning, scenario validation.strict or a validation.promote
	// entry for this code turns it into an error, which fails Exchange.Failed
	// and AssertNoErrors for that request even though the response body is
	// still the vendor's ordinary 404. A suite that polls HEAD or GET for ids
	// it knows are absent should demote this code rather than run strict.
	CodeJobForeignID = "job.foreign_id"
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
// this simulator minted; it cannot say that one WAS. Exa's "run_" prefix and
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

// deliversBody reports whether the response this attempt produces still carries
// the handler's rendered body — which, for a create, is the body carrying the
// job identifier.
//
// It is the predicate MintJob commits on, and getting it wrong is expensive in
// both directions. Committing when the body is replaced leaves a phantom job:
// a record consuming a slot that no client has an identifier for. NOT committing
// when the body IS delivered is worse — the client holds a real identifier that
// no record backs, so every poll returns the vendor's 404 for a job the create
// said it made.
//
// FaultDecision.Faulted() is the obvious reach and is wrong here: it is true
// whenever Delay > 0, and a pure delay writes the body after sleeping. The
// question is not "was this request faulted" but "does the client still receive
// what the handler rendered", and only EffectiveKind answers that.
//
// The set is read directly off execute's switch (provider/fault_exec.go): three
// kinds are handled explicitly and everything else falls through to
// writeResponse with faultBody, which returns resp.Body unless the provider's
// FaultBody overrides it — and that override only fires at status >= 400.
//
//	FaultNone             body written (a pure delay, or an explicit 2xx/3xx)
//	FaultExtraFields      body written, with the extra fields merged in
//	FaultWrongContentType body written, under a wrong Content-Type header
//	---
//	FaultStatus (>= 400)  the provider's error envelope replaces it
//	FaultInvalidJSON      raw non-JSON bytes replace it
//	FaultEmptyBody        nothing is written
//	FaultTruncateBody     a prefix, then an abort
//	FaultCloseBeforeHeaders nothing reaches the client at all
func deliversBody(dec FaultDecision) bool {
	if dec.Attempt == nil {
		return true
	}
	switch dec.Attempt.EffectiveKind() {
	case scenario.FaultNone, scenario.FaultExtraFields, scenario.FaultWrongContentType:
		return true
	default:
		return false
	}
}

// MintJob claims this request's call index and records a job derived from it,
// returning the job and whether the request may proceed.
//
// False means the request was refused and a finding says why; the caller renders
// its own provider-shaped error. True means the handler should render normally —
// including when the record was deliberately not written, because a scripted
// fault is about to replace the response anyway.
//
// # The claim always happens; the record does not
//
// The call index is claimed unconditionally, so a fault plan advances exactly as
// scripted and the retry after a faulted create draws attempt 1. The RECORD is
// written only when the response will actually carry the identifier to the
// client ([deliversBody]).
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
func MintJob(x *Exchange, entry, prefix string, encode func(...string) string) (jobs.Job, bool) {
	lane := x.Lane()
	index := x.CallIndex()

	job := jobs.Job{
		ID: prefix + encode(
			x.Deps.Scenario.SeedKey(),
			entry,
			lane.Key,
			"job",
			strconv.Itoa(index),
		),
		Namespace:   lane.Namespace,
		Entry:       entry,
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
		return jobs.Job{}, false
	}

	if !deliversBody(x.Fault()) {
		// The claim stands, the record does not. The handler still renders a body
		// so the response has a shape; Handle replaces it with the fault before
		// any of it reaches the client.
		return job, true
	}

	store := x.Deps.Jobs
	if store == nil {
		// A zero Deps serves without job state at all, matching how a nil Faults
		// serves without faults. The identifier is still derived and returned, so
		// a create answers normally; only the poll that follows cannot resolve it.
		return job, true
	}

	stats, err := store.Create(job)
	switch {
	case errors.Is(err, jobs.ErrDuplicate):
		x.Fail(CodeJobIDCollision, "",
			"job %q is already live in namespace %q; the usual cause is a reset that dropped the fault cursors without dropping the job records, so this create re-minted an identifier it had already used",
			job.ID, job.Namespace)
		return jobs.Job{}, false

	case errors.Is(err, jobs.ErrLimit):
		x.Fail(CodeJobLimitReached, "",
			"namespace %q holds its maximum of %d jobs; reset it with POST /__admin/reset, give each test its own namespace, or raise the bound",
			job.Namespace, stats.Bound)
		return jobs.Job{}, false

	case err != nil:
		x.Fail(CodeJobLimitReached, "", "recording the job failed: %v", err)
		return jobs.Job{}, false
	}

	if stats.Near() {
		x.Warn(CodeJobLimitNear, "",
			"namespace %q holds %d of %d jobs; creates still succeed, but reset it or use per-test namespaces before it fills",
			job.Namespace, stats.Count, stats.Bound)
	}
	return job, true
}

// ResolveJob returns the job an identifier names in this request's namespace.
//
// It CLAIMS NO ATTEMPT and advances no cursor, which is what makes it usable
// from a request that must not consume a poll — a HEAD asking only whether a run
// exists. A poll that means to consume one calls SelectTurnFor separately, after
// this has confirmed the job is real.
//
// It records exactly one finding, on exactly one condition: a miss whose
// identifier is shaped like one this provider mints, in a namespace that has
// minted at least one job — [CodeJobForeignID], logged once at WARN as
// servicesim.job_foreign. Every other miss records nothing: a malformed
// identifier is never this process's own, and a miss in a namespace that has
// minted nothing is a typo, not a divergence. Whether the 404 itself carries
// anything beyond this is the provider's decision, and the vendors differ.
func ResolveJob(x *Exchange, id string) (jobs.Job, bool) {
	if x.Deps.Jobs == nil || !ValidJobID(id) {
		return jobs.Job{}, false
	}

	namespace := x.Lane().Namespace
	job, found := x.Deps.Jobs.Lookup(namespace, id)
	if found {
		return job, true
	}

	if stats := x.Deps.Jobs.StatsIn(namespace); stats.Count > 0 {
		x.Warn(CodeJobForeignID, "id", foreignIDMessage, id, namespace, stats.Count)
		x.Deps.Logger.Warn("servicesim.job_foreign",
			slog.String("provider", string(x.Provider)),
			slog.String("namespace", namespace),
			slog.String("id", id))
	}

	return jobs.Job{}, false
}
