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
)

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

	// Reset sends a TCP RST instead of a clean FIN for FaultTruncateBody, so a
	// client sees "connection reset by peer" rather than "unexpected EOF".
	Reset bool `yaml:"reset,omitempty"`

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
