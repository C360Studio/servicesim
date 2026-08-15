package journal

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/c360studio/servicesim/internal/redact"
)

// OutcomeKind classifies what the client received.
type OutcomeKind string

// Outcome kinds.
const (
	OutcomeScenario  OutcomeKind = "scenario"  // the scenario's response
	OutcomeError     OutcomeKind = "error"     // provider-shaped validation or auth error
	OutcomeFault     OutcomeKind = "fault"     // a declared fault
	OutcomeUnmatched OutcomeKind = "unmatched" // failed closed: unknown route or method
)

// Severity classifies a validation finding.
type Severity string

// Finding severities.
const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Finding is one validation warning or error recorded against a request.
type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Field    string   `json:"field,omitempty"`

	// Message is free text that may quote part of the request, so it is passed
	// through redact.String by Redact before it is stored, logged or served. A
	// finding raised *about* a credential-named field must never interpolate the
	// value at all: tavily.api_key.in_body states presence and nothing else.
	Message string `json:"message"`
}

// AuthObservation is what was observed about a request's credentials. It never
// holds a credential value.
type AuthObservation struct {
	// Present reports whether any recognised credential placement carried a value.
	Present bool `json:"present"`

	// Header is the placement used, lower-cased, for example "authorization".
	Header string `json:"header,omitempty"`

	// Scheme is the Authorization scheme, for example "Bearer". Empty for
	// non-scheme placements such as x-api-key.
	Scheme string `json:"scheme,omitempty"`

	// Fingerprint is the first eight hex characters of a domain-separated SHA-256
	// of the presented credential. It lets a test assert that two calls used the
	// same key without the journal ever holding one. It is not a secrecy boundary
	// for a low-entropy value: never point Servicesim at a real credential.
	Fingerprint string `json:"fingerprint,omitempty"`

	// Placements lists EVERY recognised placement that carried a value, in the
	// order they were extracted. The fields above describe only the first.
	//
	// Recording one placement made a whole class of assertion unwritable. A
	// client that sends both a Bearer header and an x-api-key is misconfigured in
	// a way that works against a permissive server and fails against a strict
	// one, and with a single Header field the journal could not show it — the
	// second placement simply vanished. "My adapter sends exactly one credential,
	// in the placement this vendor documents" is a thing a consumer should be
	// able to prove, and len(placements) == 1 is how.
	//
	// Every entry carries a fingerprint rather than a value, on the same terms as
	// Fingerprint above.
	Placements []AuthPlacement `json:"placements,omitempty"`
}

// AuthPlacement is one credential placement observed on a request. It never
// holds a credential value, only the fingerprint of one.
type AuthPlacement struct {
	// Header is the placement, lower-cased: a header name such as
	// "authorization", or a non-header placement such as "body:api_key".
	Header string `json:"header"`

	// Scheme is the Authorization scheme, for example "Bearer". Empty for
	// placements that carry no scheme.
	Scheme string `json:"scheme,omitempty"`

	// Fingerprint is the first eight hex characters of a domain-separated
	// SHA-256 of the presented credential, on the same terms as
	// AuthObservation.Fingerprint.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Outcome is what the request produced.
type Outcome struct {
	Kind      OutcomeKind `json:"kind"`
	Label     string      `json:"label,omitempty"`
	Status    int         `json:"status,omitempty"`
	FaultKind string      `json:"fault_kind,omitempty"`
	FaultKey  string      `json:"fault_key,omitempty"`

	// AttemptIndex is the zero-based attempt number claimed for FaultKey, or -1
	// when the route had no fault plan.
	AttemptIndex int `json:"attempt_index"`

	// DelayMS is the delay the handler requested, not the delay it observed. Under
	// provider.DelaySkip no time passes, and this is still the asserted value.
	DelayMS int64 `json:"delay_ms,omitempty"`

	BytesWritten int  `json:"bytes_written"`
	Aborted      bool `json:"aborted,omitempty"`
}

// Entry is one recorded request.
type Entry struct {
	// Provider is the provider name as a plain string. It is deliberately not a
	// provider.Name: journal must not import provider, or the seam and the journal
	// form an import cycle.
	Provider string `json:"provider"`

	// Namespace is the state lane the request was served in, "default" when the
	// request carried no /n/ prefix. It is what the admin API scopes on and what
	// [Ring] indexes retention by, so every entry carries it.
	//
	// Seq is isolated per namespace only as far as the caller claimed it that
	// way: [Namespaced.NextIn] returns a sequence that restarts at 1 in each
	// namespace, while [Journal.Next] always draws on the default lane's. An
	// entry's Namespace therefore says which lane RETAINS it, and says nothing on
	// its own about which counter its Seq came from.
	Namespace string `json:"namespace,omitempty"`

	Seq    uint64 `json:"seq"`
	Method string `json:"method"`

	// Path is r.URL.Path and nothing else. r.RequestURI and r.URL.String() are
	// forbidden here, in logs, and inside wrapped errors: for a legal absolute-form
	// request target (POST http://user:secret@host/search HTTP/1.1) both render the
	// userinfo verbatim, which would put a credential in the journal, the admin API
	// and the log with no redaction pass able to recognise it.
	//
	// r.URL.Path cannot carry userinfo, but unmatched traffic is journaled too and
	// its path is entirely client-chosen, so Redact still passes it through
	// redact.String. That is identity for every route Servicesim serves and for
	// every /x/ and /n/ prefix, and it masks GET /search/sk-live-… .
	Path string `json:"path"`

	// Query is r.URL.RawQuery with credential-named parameter values masked by
	// redact.Query. An adapter that puts its key in the query string is exactly the
	// misconfiguration the journal exists to surface, so the parameter names are
	// preserved and only the values are masked.
	Query string `json:"query,omitempty"`

	Route      string `json:"route,omitempty"`
	RemoteAddr string `json:"remote_addr,omitempty"`

	// ArrivedAt and CompletedAt are real time, from provider.SystemClock by
	// default. They are the evidence testkit.AssertOverlapped rests on and are
	// never part of a response body; see §3.2 of the package design.
	ArrivedAt   time.Time `json:"arrived_at"`
	CompletedAt time.Time `json:"completed_at"`

	// Headers are the request headers with credential-bearing values masked.
	Headers map[string][]string `json:"headers"`

	// Body is the decoded request body, re-encoded after redaction and bounded by
	// the configured limit. It is the parsed body, not the raw bytes, so a
	// credential-looking property is masked rather than stored verbatim.
	Body json.RawMessage `json:"body,omitempty"`

	// BodyTruncated reports that Body was clipped to the size limit. A clipped
	// document is no longer a JSON value, so when BodyTruncated is set Body may be
	// a JSON string holding the retained prefix rather than an object; see
	// [NewRing].
	BodyTruncated bool `json:"body_truncated,omitempty"`

	// BodyParseError is set when the body was not valid JSON.
	BodyParseError string `json:"body_parse_error,omitempty"`

	Auth     AuthObservation `json:"auth"`
	Outcome  Outcome         `json:"outcome"`
	Findings []Finding       `json:"findings,omitempty"`
}

// Warnings returns the warning-severity findings.
func (e Entry) Warnings() []Finding {
	return e.findingsOf(SeverityWarning)
}

// Errors returns the error-severity findings.
func (e Entry) Errors() []Finding {
	return e.findingsOf(SeverityError)
}

// findingsOf returns the findings at one severity, as a fresh slice so a caller
// cannot reach into the entry's own backing array.
func (e Entry) findingsOf(s Severity) []Finding {
	var out []Finding
	for _, f := range e.Findings {
		if f.Severity == s {
			out = append(out, f)
		}
	}
	return out
}

// Stats describes journal retention.
type Stats struct {
	Capacity int    `json:"capacity"`
	Stored   int    `json:"stored"`
	Appended uint64 `json:"appended"`
	Dropped  uint64 `json:"dropped"`
}

// Journal is the request journal contract.
//
// It is deliberately namespace-free. Consumers implement it — testkit exports it
// as testkit.Journal and a consumer may wire its own — so every method added
// here breaks every implementation outside this repository. Namespace isolation
// is therefore an optional capability, declared by [Namespaced] and reached
// through [NextIn], [SnapshotIn] and [ResetIn], which degrade honestly for a
// journal that does not isolate namespaces. [Ring] implements both.
type Journal interface {
	// Next claims the next arrival-ordered sequence number. It is called before
	// the handler runs so sequence reflects arrival, while Snapshot order reflects
	// completion; an entry carries both timestamps so a test can sort either way.
	//
	// Sequence numbers are one-based: the first call after construction or Reset
	// returns 1, so a zero Entry.Seq unambiguously means "never journaled".
	//
	// On a [Namespaced] journal this is the default namespace's sequence; see
	// [Namespaced.NextIn] for why every namespace has its own.
	Next() uint64

	// Append stores a completed entry. Implementations must Redact the entry and
	// then bound it, in that order: redaction is enforced at the storage boundary
	// so no handler can forget it, and Redact is idempotent so a handler that
	// already redacted (provider.Handle does, before logging) costs only a second
	// pass. Reversing the order is a leak; see the Ring notes below.
	//
	// Append takes Entry by value and therefore cannot redact the caller's copy.
	// A caller that also logs or serialises its entry must call Redact itself
	// first — provider.Handle does exactly that.
	Append(e Entry)

	// Snapshot returns a deep copy of the stored entries in append order.
	Snapshot() []Entry

	// Reset clears stored entries, zeroes the retention counters and returns the
	// sequence counter to zero, so the next Next returns 1 again.
	Reset()

	// Stats reports retention counters.
	Stats() Stats
}

// Namespaced is a Journal whose state is isolated per namespace: one process
// serving many concurrent tests, each with its own sequence, its own entries and
// its own retention.
//
// A namespace is a plain string here, never a provider type. This package sits
// below the provider seam and must not import it (see [Entry.Provider]).
//
// The empty namespace means [DefaultNamespace] in every method, so a journal
// used by traffic that never carried an /n/ prefix behaves exactly as it did
// before namespaces existed.
type Namespaced interface {
	Journal

	// NextIn claims the next arrival-ordered sequence number in one namespace.
	//
	// Sequences are independent, so two concurrent tests each see 1, 2, 3. That
	// is the point: with one interleaved process-wide sequence, an entry's number
	// depends on what unrelated tests did, and "was this my request?" — what the
	// journal is for — stops being answerable.
	NextIn(namespace string) uint64

	// SnapshotIn returns a deep copy of one namespace's entries, in append order.
	SnapshotIn(namespace string) []Entry

	// ResetIn drops one namespace's state, leaving every other namespace
	// untouched.
	ResetIn(namespace string)

	// StatsIn reports one namespace's retention counters.
	StatsIn(namespace string) Stats
}

// NextIn claims the next sequence number in namespace from j.
//
// A journal that does not isolate namespaces has one sequence to give, so this
// falls back to j.Next(). The number is still unique and still arrival-ordered;
// it is simply shared, which is what a caller wiring its own Journal
// implementation into a namespaced deployment has chosen.
func NextIn(j Journal, namespace string) uint64 {
	if n, ok := j.(Namespaced); ok {
		return n.NextIn(namespace)
	}
	return j.Next()
}

// SnapshotIn returns the entries j holds for namespace, in append order.
//
// A journal that does not isolate namespaces is filtered on Entry.Namespace
// instead, which is the same answer more slowly: production code stamps the
// namespace on every entry it appends.
func SnapshotIn(j Journal, namespace string) []Entry {
	if n, ok := j.(Namespaced); ok {
		return n.SnapshotIn(namespace)
	}

	want := laneKey(namespace)
	var out []Entry
	for _, e := range j.Snapshot() {
		if laneKey(e.Namespace) == want {
			out = append(out, cloneEntry(e))
		}
	}
	return out
}

// ResetIn drops namespace's state from j and reports whether it could. False
// means j does not isolate namespaces and *nothing was reset*.
//
// It deliberately does not fall back to j.Reset(). Wiping every concurrent
// test's journal because the caller asked to drop one namespace is the single
// worst outcome this surface can produce, and it would be silent; returning
// false lets the caller say so instead.
func ResetIn(j Journal, namespace string) bool {
	n, ok := j.(Namespaced)
	if !ok {
		return false
	}
	n.ResetIn(namespace)
	return true
}

// Redact returns a copy of e with every credential-bearing value masked:
//
//	Headers            -> redact.Headers
//	Query              -> redact.Query
//	Body               -> redact.JSONBytes (which redacts even a non-JSON body)
//	BodyParseError     -> redact.String
//	Findings[].Message -> redact.String
//	Path               -> redact.String
//
// Path is not in the package design's list because r.URL.Path cannot carry
// userinfo. It is masked anyway: unmatched traffic is journaled, its path is
// client-chosen free text, and redact.String is identity on every path this
// simulator routes.
//
// It is idempotent, which is what allows provider.Handle to call it before
// logging and Append to call it again at the storage boundary. Every text field
// an entry carries is covered, not just the two obvious ones: a finding message
// that quotes a misplaced credential lands in the journal, the admin API, the
// log and — for error-severity findings — the HTTP error body.
//
// The result shares no mutable state with e: the header map, the body slice and
// the findings slice are all rebuilt, so redacting an entry whose Headers is
// the live r.Header cannot mutate the request.
func Redact(e Entry) Entry {
	e.Headers = redact.Headers(http.Header(e.Headers))

	if e.Path != "" {
		e.Path = redact.String(e.Path)
	}
	if e.Query != "" {
		e.Query = redact.Query(e.Query)
	}
	if len(e.Body) > 0 {
		e.Body = redact.JSONBytes(e.Body)
	}
	if e.BodyParseError != "" {
		e.BodyParseError = redact.String(e.BodyParseError)
	}
	if len(e.Findings) > 0 {
		findings := make([]Finding, len(e.Findings))
		copy(findings, e.Findings)
		for i := range findings {
			findings[i].Message = redact.String(findings[i].Message)
		}
		e.Findings = findings
	}
	// Placements hold fingerprints, never values, so there is nothing here to
	// mask. It is still rebuilt, because this function promises to share no
	// mutable state with its argument and a caller that appended to the returned
	// entry's placements would otherwise reach the live one.
	if e.Auth.Placements != nil {
		e.Auth.Placements = append([]AuthPlacement(nil), e.Auth.Placements...)
	}
	return e
}

// cloneEntry returns a copy of e that shares no mutable state with it, so a
// snapshot handed to a caller cannot race with a concurrent Append.
func cloneEntry(e Entry) Entry {
	if e.Headers != nil {
		headers := make(map[string][]string, len(e.Headers))
		for name, values := range e.Headers {
			headers[name] = append([]string(nil), values...)
		}
		e.Headers = headers
	}
	if e.Body != nil {
		e.Body = append(json.RawMessage(nil), e.Body...)
	}
	if e.Findings != nil {
		e.Findings = append([]Finding(nil), e.Findings...)
	}
	if e.Auth.Placements != nil {
		e.Auth.Placements = append([]AuthPlacement(nil), e.Auth.Placements...)
	}
	return e
}
