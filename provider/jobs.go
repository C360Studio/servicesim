package provider

import (
	"errors"
	"log/slog"
	"net/http"
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

// deliversBody reports whether the client of a create will receive the job
// identifier: whether the response this attempt produces is complete, is not an
// error status, and still carries the handler's rendered body.
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
// what the handler rendered".
//
// The table below is read off what Handle and execute (provider/handle.go,
// provider/fault_exec.go) actually do, not off what a kind is named for. Three of
// its rows are not the kind's own doing, and all were wrong before this was
// derived from the executor:
//
//   - faultBody replaces the body with the attempt's body: at ANY status, and
//     asks the provider's FaultBody for an envelope at status >= 400. Every kind
//     that would otherwise write the rendered body therefore writes it only when
//     the attempt has no body: and a status below 400.
//   - net/http writes no body under some statuses it is asked for one under: 204
//     and 304, and 101, which it sends as the final status. Every other 1xx goes
//     out as an interim response and the body follows it under a 200, so that
//     client still receives the identifier. See [writesNoBody].
//   - Handle drops an attempt that cannot apply to the exchange — a stream_*
//     kind on a response that does not stream, which a create never does — so the
//     rendered body is written untouched and the mismatch is reported as a
//     finding.
//
// Per kind, as EffectiveKind resolves it:
//
//	EffectiveKind                        the client receives the handler's body when
//	-----------------------------------  ------------------------------------------
//	FaultNone, FaultStatus,              the attempt has no body:, a status below
//	FaultWrongContentType,               400, and a status net/http writes a body
//	FaultExtraFields,                    under. Otherwise the override or the
//	FaultOversizedBody                   provider's error envelope replaces it, and
//	                                     these kinds then only change the header,
//	                                     merge fields into the replacement, or pad it.
//	FaultStreamDisconnect,               always: the attempt is dropped, so even a
//	FaultStreamTruncateChunk,            status or body: it declares is never applied.
//	FaultStreamStall
//	FaultInvalidJSON                     never: raw non-JSON bytes replace it.
//	FaultEmptyBody                       never: nothing is written.
//	FaultTruncateBody                    never: a prefix, then an abort.
//	FaultCloseBeforeHeaders              never: nothing reaches the client at all.
//
// Error, Tag, RawBody, ContentType, RetryAfter, Delay and DelayAfterHeaders do
// not appear because none of them can change the answer: Error and Tag are read
// only by the provider's FaultBody, which runs only for a body: or a status >= 400
// — both already "never" above — and the rest change a timing, a header that
// does not frame the body, or (RawBody) the kind EffectiveKind infers. Headers
// is the one that can, and is the third self-defeating script below.
//
// A status >= 400 is "never" even for a provider that registers no FaultBody, in
// which case the handler's body is served under the error status: no client
// treats that as a created job, so no job is the right answer there too.
//
// The prediction assumes a create that does not stream and whose response stays
// fault-eligible. It is made before the body exists, so these outcomes are
// beyond it, and the first three are scripts defeating themselves rather than
// shapes worth a branch here: truncate_body with truncate_after_bytes at or past
// the rendered body's length writes the whole body before it aborts; extra_fields
// can overwrite the "id" it merges into; a headers: override of a framing header
// — Content-Length, Transfer-Encoding or Content-Encoding — can leave the client
// unable to read the body that was written; and a client whose own deadline fires
// during a delay receives nothing.
//
// The switch names every kind rather than defaulting, and
// TestDeliveryTableNamesEveryFaultKind fails when scenario declares a kind
// constant — as `X FaultKind = "lit"` or `X = FaultKind("lit")`; a kind declared
// any other way is outside what that test can read — that the table in
// jobs_delivery_test.go does not cover. Each row of that table is checked
// against a real client, so the next kind has to be decided, not absorbed by a
// default.
func deliversBody(dec FaultDecision) bool {
	a := dec.Attempt
	if a == nil {
		return true
	}
	switch a.EffectiveKind() {
	case scenario.FaultNone, scenario.FaultStatus, scenario.FaultWrongContentType,
		scenario.FaultExtraFields, scenario.FaultOversizedBody:
		return len(a.Body) == 0 && a.Status < http.StatusBadRequest && !writesNoBody(a.Status)

	case scenario.FaultStreamDisconnect, scenario.FaultStreamTruncateChunk, scenario.FaultStreamStall:
		return true

	case scenario.FaultInvalidJSON, scenario.FaultEmptyBody,
		scenario.FaultTruncateBody, scenario.FaultCloseBeforeHeaders:
		return false

	default:
		// A kind this switch does not name. Not recording is the conservative
		// answer, but the test above is what is meant to catch it first.
		return false
	}
}

// writesNoBody reports whether net/http sends a response under status without
// the body the handler wrote to it. That is 204 and 304 — bodyAllowedForStatus
// refuses them — and 101, which the server sends as the final status. It is not
// every 1xx, though that is what bodyAllowedForStatus says: the server sends any
// other 1xx as an interim response and writes the body under the 200 that
// follows, which TestACreateLeavesAJobExactlyWhenTheClientHoldsItsIdentifier
// pins with a real client.
func writesNoBody(status int) bool {
	return status == http.StatusSwitchingProtocols || status == http.StatusNoContent || status == http.StatusNotModified
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
//
// # What the record decision assumes
//
// It is made before the handler has rendered anything or Handle has applied a
// fault, so it rests on two things the handler must keep true. The create does
// not stream: [deliversBody]'s table is for a response with no Stream, and
// against a stream a stream_* kind applies instead of being dropped while
// truncate_body and oversized_body are the ones reported unreachable. And the
// create's response stays fault-eligible: Handle clears a claimed attempt on a
// response that opts out of faults, so a handler that returned FaultEligible
// false after MintJob would be served unfaulted while the record was decided as
// if the fault applied. Every in-tree create satisfies both.
func MintJob(x *Exchange, entry, prefix string, encode func(...string) string) (id string, ok bool) {
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

	if !deliversBody(x.Fault()) {
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
// exists. A poll that means to consume one calls SelectTurnFor separately, after
// this has confirmed the job is real.
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
