package scenario

// FaultKind names one transport or protocol failure mode.
type FaultKind string

// Supported fault kinds. FaultNone renders the scenario response normally and is
// what a trailing "- status: 200" attempt means.
const (
	FaultNone               FaultKind = ""
	FaultStatus             FaultKind = "status"
	FaultCloseBeforeHeaders FaultKind = "close_before_headers"
	FaultTruncateBody       FaultKind = "truncate_body"
	FaultInvalidJSON        FaultKind = "invalid_json"
	FaultWrongContentType   FaultKind = "wrong_content_type"
	FaultEmptyBody          FaultKind = "empty_body"
	FaultExtraFields        FaultKind = "extra_fields"

	// FaultOversizedBody serves the response this attempt would otherwise have
	// produced — the rendered scenario body, or the provider's error shape when
	// Status is also set, with ExtraFields merged as for every kind — padded
	// with insignificant JSON whitespace to at least BodyBytes bytes. Nothing
	// semantic changes: every JSON decoder accepts trailing whitespace after a
	// complete value, so the decoded value is byte-identical to the unpadded
	// response and only the size differs, which is exactly what a size-limit
	// ingress gate measures. If the unpadded body is already >= BodyBytes,
	// nothing is appended and the response is served as is.
	FaultOversizedBody FaultKind = "oversized_body"

	// FaultStreamDisconnect writes chunks [0, AfterChunk) in full and then
	// destroys the connection before chunk AfterChunk is written at all: the
	// previous chunk is the last complete frame, so the client sees a clean
	// frame boundary followed by a dead connection. See
	// docs/design/streaming.md §9's after_chunk-at-the-terminal-chunk example,
	// which pins this reading against an earlier draft's "writes AfterChunk in
	// full, then aborts" (that draft matched the design's own illustrative
	// execute loop but contradicted §9's prose; prose wins, see this package's
	// doc comment and the streaming design's banner).
	FaultStreamDisconnect FaultKind = "stream_disconnect"

	// FaultStreamTruncateChunk writes chunks [0, AfterChunk) in full, then
	// TruncateAfterBytes bytes of chunk AfterChunk, then destroys the
	// connection. The distinction from FaultStreamDisconnect is not cosmetic:
	// this delivers a MALFORMED FRAME — a partial "data:" line — which is a
	// different branch of a consumer's SSE parser than a stream that ended at
	// a frame boundary.
	FaultStreamTruncateChunk FaultKind = "stream_truncate_chunk"

	// FaultStreamStall inserts Delay before chunk AfterChunk and then
	// continues normally. Nothing is aborted; the client's own deadline
	// decides what happens, which is the point for a Temporal activity
	// timeout or a missed heartbeat.
	FaultStreamStall FaultKind = "stream_stall"
)

// CodeAcceptedRedundant is raised when `accepted: true` is declared on a fault
// attempt that already delivers its body ([FaultAttempt.DeliversBody]), which
// includes every stream_* kind: the job is kept whether or not the modifier is
// there, so declaring it claims a lost reply the attempt does not script. Checked
// in [Scenario.Validate] from the attempt alone, and defined as the predicate's
// result rather than as a list of kinds so it cannot drift from what a create
// actually keeps.
const CodeAcceptedRedundant = "scenario.fault.accepted.redundant"

// IsStream reports whether k is one of the three fault kinds that assume a
// chunked SSE transport and cannot apply to an ordinary JSON exchange. It is
// exported so provider (whose own execution-time switch needs the same
// grammar) shares this one predicate rather than carrying a second copy: a
// fourth stream_* kind added in one and not the other would validate at load
// but be mis-handled at request time, or vice versa.
func (k FaultKind) IsStream() bool {
	switch k {
	case FaultStreamDisconnect, FaultStreamTruncateChunk, FaultStreamStall:
		return true
	default:
		return false
	}
}

// FaultAfter selects what happens once the attempt list is exhausted.
type FaultAfter string

// Supported post-exhaustion behaviours.
const (
	FaultAfterSuccess    FaultAfter = "success"     // default: serve the scenario response
	FaultAfterRepeatLast FaultAfter = "repeat_last" // permanent failure
)

// Fault is a deterministic per-turn failure plan. Attempt N of a route receives
// Attempts[N] after Repeat expansion; see docs/design/package-design.md §4.
type Fault struct {
	Attempts []FaultAttempt `yaml:"attempts"`
	After    FaultAfter     `yaml:"after,omitempty"`
}

// FaultAttempt is what one attempt against a route receives.
//
// Kind may be omitted and is then inferred: a Status of 400 or above with no
// other mangling field means FaultStatus; everything else unset means FaultNone.
// Delay is orthogonal and composes with every kind.
type FaultAttempt struct {
	Kind FaultKind `yaml:"kind,omitempty"`

	Status     int               `yaml:"status,omitempty"`
	Delay      Duration          `yaml:"delay,omitempty"`
	RetryAfter *int              `yaml:"retry_after,omitempty"` // seconds, sets Retry-After
	Headers    map[string]string `yaml:"headers,omitempty"`

	// DelayAfterHeaders pauses AFTER the status line and headers have been
	// written and flushed, before the body — or, for truncate_body, before the
	// partial write and reset. Delay is a pre-dispatch hang, before anything
	// reaches the client at all; this is the shape a mid-flight cancellation
	// actually has on the wire — headers arrive, then silence, then the rest —
	// which Delay alone cannot express. It composes with Delay (hang, then
	// headers, then hang again) and with every non-streaming kind except
	// close_before_headers, which never writes headers for there to be a hang
	// after. It cannot apply to a stream_* kind or to an exchange that will
	// stream; stream_stall with after_chunk: 0 is the streaming equivalent.
	DelayAfterHeaders Duration `yaml:"delay_after_headers,omitempty"`

	// Body is the verbatim error body. When nil the provider package synthesises
	// its documented shape for Status.
	Body map[string]any `yaml:"body,omitempty"`

	// Error and Tag fill the provider's error envelope without spelling out the
	// whole body. Tag is Exa-only.
	Error string `yaml:"error,omitempty"`
	Tag   string `yaml:"tag,omitempty"`

	// RawBody overrides the response bytes entirely, for FaultInvalidJSON.
	RawBody string `yaml:"raw_body,omitempty"`

	// ContentType overrides the Content-Type header, for FaultWrongContentType.
	ContentType string `yaml:"content_type,omitempty"`

	// TruncateAfterBytes is how many body bytes reach the client before the
	// connection dies, for FaultTruncateBody. Zero means half the body.
	TruncateAfterBytes int `yaml:"truncate_after_bytes,omitempty"`

	// Reset sends a TCP RST instead of a clean FIN for FaultTruncateBody, or
	// for either aborting stream_* kind, so a client sees "connection reset by
	// peer" rather than "unexpected EOF" — one spelling of "RST not FIN"
	// across the streaming and non-streaming catalogue.
	Reset bool `yaml:"reset,omitempty"`

	// BodyBytes is the minimum size, in bytes, FaultOversizedBody pads the
	// response body to: "at least this many bytes". Zero means unset — unlike
	// TruncateAfterBytes, oversized_body has no default size to fall back to
	// (there is no "half the body" analogue for padding upward), so a zero
	// value under an explicit kind: oversized_body is a load error rather than
	// a fallback.
	BodyBytes int `yaml:"body_bytes,omitempty"`

	// AfterChunk is the zero-based index of the first chunk a stream_* kind
	// affects. Chunks before it are always delivered whole. It is meaningful
	// only for the three stream_* kinds; a nonzero value on any other kind is
	// scenario.fault.after_chunk.not_streaming. Zero is a legitimate index
	// (the very first chunk), so — matching this file's existing convention
	// for TruncateAfterBytes and every other "zero means default/absent"
	// field — an unset AfterChunk is indistinguishable from an explicit zero;
	// the not_streaming check therefore only fires for a nonzero value, which
	// is a deliberate, documented limitation rather than an oversight.
	//
	// For FaultStreamStall, Delay is the mid-stream pause inserted before this
	// chunk rather than the time-to-first-byte delay every other kind gives
	// it. A stall that also wants a slow first byte declares two attempts, or
	// a scripted first-chunk pace.
	AfterChunk int `yaml:"after_chunk,omitempty"`

	ExtraFields ExtraFields `yaml:"extra_fields,omitempty"`

	// Accepted says the request TOOK EFFECT before the failure this attempt
	// scripts: the job it created is kept even though the response that carries
	// its identifier does not arrive intact. "Accepted, reply lost" is the
	// failure a create-then-poll client cannot distinguish from "rejected" — it
	// holds an error either way — and the two have different consequences, an
	// orphaned run that is still being billed versus nothing at all. Without
	// this modifier a create attempt whose body does not arrive leaves no job,
	// which is the rejected case.
	//
	// Accepted keeps the JOB; it does not hide the identifier, and whether the
	// client learns it depends on the shape. close_before_headers, empty_body,
	// invalid_json and a status of 400 or above withhold it, provided the
	// provider's error body is built from the attempt alone. truncate_body does
	// not by default: it sends a PREFIX of the rendered body, and the identifier
	// is the first key of both in-tree creates, so the default truncation (half
	// the body) and any larger one deliver the whole identifier — a complete,
	// ordinary response once truncate_after_bytes reaches the body's length.
	// Only a cut that stops short of the identifier's last byte withholds it;
	// for Exa's create, whose body opens with `{"id":"` (7 bytes) and then a
	// 42-character identifier, that is a truncate_after_bytes below 49.
	//
	// It is meaningful only where the attempt does NOT deliver its body
	// ([FaultAttempt.DeliversBody]): where the client does receive the
	// identifier the job is kept anyway, so declaring it is a load error,
	// [CodeAcceptedRedundant]. The check is that predicate's result and not a
	// list of kinds, so it covers a Body: override below 400 and a 204 or 304 as
	// well as close_before_headers, truncate_body, empty_body, invalid_json and
	// any status of 400 or above.
	//
	// Load checks the attempt in isolation and allows it under any fault plan.
	// Whether the request that claims it mints a job is a runtime fact only a
	// profile's routes know; an accepted attempt claimed by a request that mints
	// nothing is reported per request, as provider.CodeAcceptedUnreachable.
	//
	// It is not idempotency. A client that retries claims the next attempt and
	// mints a SECOND job; neither vendor documents an idempotency key, and the
	// simulator does not invent one.
	Accepted bool `yaml:"accepted,omitempty"`

	// Repeat applies this attempt to N consecutive attempts. Zero and one are
	// equivalent. "Fail the first three then succeed" is one attempt with
	// Repeat: 3 and the default After.
	Repeat int `yaml:"repeat,omitempty"`
}

// EffectiveKind returns Kind with the documented inference applied: a Status of
// 400 or above with no other mangling field means FaultStatus, and everything
// else unset means FaultNone. Selection and execution both consult it so the two
// cannot disagree about what an attempt with only "status: 429" means.
func (a FaultAttempt) EffectiveKind() FaultKind {
	if a.Kind != FaultNone {
		return a.Kind
	}
	if a.RawBody != "" {
		return FaultInvalidJSON
	}
	if a.ContentType != "" {
		return FaultWrongContentType
	}
	if a.TruncateAfterBytes > 0 || a.Reset {
		return FaultTruncateBody
	}
	if a.BodyBytes > 0 {
		return FaultOversizedBody
	}
	if a.Status >= 400 {
		return FaultStatus
	}
	return FaultNone
}

// HTTP statuses DeliversBody reasons about, spelled out rather than imported:
// this package has no dependency on net/http and a framework consumers import
// for its data model should not acquire one for three constants.
const (
	statusSwitchingProtocols = 101
	statusNoContent          = 204
	statusNotModified        = 304
	statusBadRequest         = 400
)

// DeliversBody reports whether the client of a non-streaming exchange still
// receives the body the handler rendered under this attempt: the response is
// complete, is not an error status, and carries the handler's own bytes.
//
// It is exported so provider, which decides at MintJob time whether a create's
// job is kept, shares this one predicate with scenario's load validation
// ([FaultAttempt.Accepted] is redundant on an attempt that delivers its body)
// rather than carrying a second copy — the precedent is [FaultKind.IsStream].
// Getting it wrong is expensive in both directions. Keeping a job when the body
// is replaced leaves a phantom: a record consuming a slot that no client has an
// identifier for. NOT keeping one when the body IS delivered is worse — the
// client holds a real identifier that no record backs, so every poll returns the
// vendor's 404 for a job the create said it made.
//
// [FaultAttempt.Delay] and [FaultAttempt.DelayAfterHeaders] do not decide it:
// "was this request faulted" is the wrong question, because a pure delay writes
// the body after sleeping. The question is whether the client still receives
// what the handler rendered.
//
// The table below is read off what provider's Handle and executor actually do,
// not off what a kind is named for. Three of its rows are not the kind's own
// doing, and all were wrong before this was derived from the executor:
//
//   - The executor replaces the body with the attempt's Body: at ANY status, and
//     asks the provider's FaultBody for an envelope at status >= 400. Every kind
//     that would otherwise write the rendered body therefore writes it only when
//     the attempt has no Body: and a status below 400.
//   - net/http writes no body under some statuses it is asked for one under: 204
//     and 304, and 101, which it sends as the final status. Every other 1xx goes
//     out as an interim response and the body follows it under a 200, so that
//     client still receives the identifier.
//   - Handle drops an attempt that cannot apply to the exchange — a stream_* kind
//     on a response that does not stream — so the rendered body is written
//     untouched and the mismatch is reported as a finding. This method answers for
//     a response that does not stream, so those kinds deliver.
//
// Per kind, as [FaultAttempt.EffectiveKind] resolves it:
//
//	EffectiveKind                        the client receives the handler's body when
//	-----------------------------------  ------------------------------------------
//	FaultNone, FaultStatus,              the attempt has no Body:, a status below
//	FaultWrongContentType,               400, and a status net/http writes a body
//	FaultExtraFields,                    under. Otherwise the override or the
//	FaultOversizedBody                   provider's error envelope replaces it, and
//	                                     these kinds then only change the header,
//	                                     merge fields into the replacement, or pad it.
//	FaultStreamDisconnect,               always: the attempt is dropped, so even a
//	FaultStreamTruncateChunk,            status or Body: it declares is never applied.
//	FaultStreamStall
//	FaultInvalidJSON                     never: raw non-JSON bytes replace it.
//	FaultEmptyBody                       never: nothing is written.
//	FaultTruncateBody                    never: a prefix, then an abort.
//	FaultCloseBeforeHeaders              never: nothing reaches the client at all.
//
// Error, Tag, RawBody, ContentType, RetryAfter, Delay and DelayAfterHeaders do
// not appear because none of them can change the answer: Error and Tag are read
// only by the provider's FaultBody, which runs only for a Body: or a status >= 400
// — both already "never" above — and the rest change a timing, a header that does
// not frame the body, or (RawBody) the kind EffectiveKind infers. Headers is the
// one that can, and is the third self-defeating script below.
//
// A status >= 400 is "never" even for a provider that registers no FaultBody, in
// which case the handler's body is served under the error status: no client
// treats that as a created job, so no job is the right answer there too.
//
// The prediction is made before the body exists, so these outcomes are beyond
// it, and the first three are scripts defeating themselves rather than shapes
// worth a branch here: truncate_body with TruncateAfterBytes at or past the
// rendered body's length writes the whole body before it aborts; extra_fields can
// overwrite the "id" it merges into; a Headers: override of a framing header —
// Content-Length, Transfer-Encoding or Content-Encoding — can leave the client
// unable to read the body that was written; and a client whose own deadline fires
// during a delay receives nothing.
//
// The switch names every kind rather than defaulting, so a new kind has to be
// decided here rather than absorbed by a default. The result is verified against
// what a real HTTP client receives, kind by kind, in this repository's own tests.
func (a FaultAttempt) DeliversBody() bool {
	switch a.EffectiveKind() {
	case FaultNone, FaultStatus, FaultWrongContentType, FaultExtraFields, FaultOversizedBody:
		return len(a.Body) == 0 && a.Status < statusBadRequest && !writesNoBody(a.Status)

	case FaultStreamDisconnect, FaultStreamTruncateChunk, FaultStreamStall:
		return true

	case FaultInvalidJSON, FaultEmptyBody, FaultTruncateBody, FaultCloseBeforeHeaders:
		return false

	default:
		// A kind this switch does not name. Not delivering is the conservative
		// answer, but the provider test above is what is meant to catch it first.
		return false
	}
}

// writesNoBody reports whether net/http sends a response under status without
// the body the handler wrote to it. That is 204 and 304 — bodyAllowedForStatus
// refuses them — and 101, which the server sends as the final status. It is not
// every 1xx, though that is what bodyAllowedForStatus says: the server sends any
// other 1xx as an interim response and writes the body under the 200 that
// follows, which provider's TestACreateLeavesAJobExactlyWhenTheClientHoldsItsIdentifier
// pins with a real client.
func writesNoBody(status int) bool {
	return status == statusSwitchingProtocols || status == statusNoContent || status == statusNotModified
}

// Repeats returns the number of consecutive attempts this entry covers. Zero and
// one are equivalent, so the minimum is one.
func (a FaultAttempt) Repeats() int {
	if a.Repeat < 1 {
		return 1
	}
	return a.Repeat
}

// HasAttempts reports whether the plan declares at least one attempt. It is
// nil-safe so a caller can ask a provider entry that has no fault at all.
func (f *Fault) HasAttempts() bool {
	return f != nil && len(f.Attempts) > 0
}
