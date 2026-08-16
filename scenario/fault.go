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
