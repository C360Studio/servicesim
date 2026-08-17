package provider

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/internal/redact"
	"github.com/c360studio/servicesim/scenario"
)

// Exchange is one request in flight. Provider handlers read the decoded body and
// record findings on it; everything else is filled in by Handle.
//
// An Exchange belongs to exactly one request and one goroutine. It is not safe
// for concurrent use and does not need to be: Handle constructs one per request.
type Exchange struct {
	Deps     Deps
	Provider Name
	Route    Route
	Request  *http.Request

	// Seq is the arrival-ordered journal sequence, claimed before the handler runs.
	Seq uint64

	// ArrivedAt is stamped from Deps.Clock before the body is read.
	ArrivedAt time.Time

	// Raw is the request body, bounded by Deps.MaxRequestBytes.
	Raw []byte

	// Body is Raw decoded as a generic JSON object. nil when the body was absent
	// or unparseable; findings say which.
	Body map[string]any

	// auth is what was observed about credentials, produced by httpx.Observe
	// over httpx.ExtractCredentials. Never holds a credential value.
	//
	// It is unexported: ObserveCredential (framework.go) is the only write
	// path into it, so a profile can add a placement it discovered itself
	// (Tavily's body-placed api_key, for instance) without being able to
	// replace the observation outright.
	auth journal.AuthObservation

	findings []journal.Finding

	// lane is resolved once by Handle, before the handler runs. It is unexported
	// so nothing downstream can re-key a request mid-flight: one request, one lane,
	// decided in one place.
	lane Lane

	// decision and claimed memoise the single attempt claim this request makes.
	decision FaultDecision
	claimed  bool
}

// Lane returns the state lane this request was resolved into: its namespace, the
// scenario selected for it, and the turn-key lane its cursor is drawn from.
// Handle resolves it once, before the handler runs; a handler reads it and never
// recomputes it.
//
// It is the zero Lane on an Exchange built by hand rather than by Handle.
func (x *Exchange) Lane() Lane { return x.lane }

// AcceptedPlacements resolves which credential placements authenticate this
// request. It is the one precedence rule the three provider packages share, kept
// here rather than written out three times, because three copies of a rule about
// credentials are three chances to disagree about what authenticates:
//
//	scenario auth.headers  >  Route.Credentials  >  the provider's own default
//
// auth.headers wins outright. It is the scenario author's explicit assertion
// about what their client should be sending, and a negative test — "prove a
// body-placed key is now rejected" — is worthless if a route default quietly
// re-admits it.
//
// providerDefault is what the package accepted before routes could speak for
// themselves; it still applies to any route that declares no Credentials.
//
// The result is lower-cased and trimmed, so a route or a scenario may spell a
// header "X-API-Key" without it silently matching nothing. Callers must treat
// the slice as read-only.
func (x *Exchange) AcceptedPlacements(policy scenario.AuthPolicy, providerDefault []string) []string {
	switch {
	case len(policy.Headers) > 0:
		return normalizePlacements(policy.Headers)
	case len(x.Route.Credentials) > 0:
		return normalizePlacements(x.Route.Credentials)
	default:
		return normalizePlacements(providerDefault)
	}
}

// normalizePlacements lower-cases and trims a placement list. Credential headers
// are matched case-insensitively everywhere else, so a policy that did not
// normalise would fail in the one direction nobody tests: a correctly-spelled
// header that authenticates nothing.
func normalizePlacements(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Warn records a warning finding. Warnings are journal-only: the request still
// receives its scenario response.
func (x *Exchange) Warn(code, field, format string, args ...any) {
	x.record(journal.SeverityWarning, code, field, format, args...)
}

// Fail records an error finding. One error means the handler returns a
// provider-shaped 4xx instead of the scenario response.
func (x *Exchange) Fail(code, field, format string, args ...any) {
	x.record(journal.SeverityError, code, field, format, args...)
}

func (x *Exchange) record(sev journal.Severity, code, field, format string, args ...any) {
	x.findings = append(x.findings, journal.Finding{
		Severity: sev,
		Code:     code,
		Field:    field,
		Message:  fmt.Sprintf(format, args...),
	})
}

// Failed reports whether any error finding was recorded, after the scenario's
// ValidationPolicy has been applied. A warning promoted by validation.strict or
// validation.promote fails the request, which is the entire point of the policy.
func (x *Exchange) Failed() bool {
	policy := x.policy()
	for _, f := range x.findings {
		if effectiveSeverity(policy, f) == journal.SeverityError {
			return true
		}
	}
	return false
}

// HasFinding reports whether a finding with this code was recorded, at any
// severity. Provider packages use it to avoid raising a finding the shared
// lifecycle already raised.
func (x *Exchange) HasFinding(code string) bool {
	for _, f := range x.findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

// Findings returns the findings recorded so far, after the scenario's
// ValidationPolicy promotions and demotions have been applied, sorted into a
// total order: error severity before warning, then Field, then Code.
//
// The sort is not cosmetic. request.unknown_field is produced by walking the
// decoded body, which is a map[string]any, and Go randomises map iteration per
// run. Without a total order here, journal entries differ run to run — and
// Perplexity's 422 body is built directly from this slice, so its detail array
// would differ too, making a golden for a two-error request flake. Handlers must
// additionally walk body keys through slices.Sorted(maps.Keys(...)) so the
// *messages* are generated in a stable order as well; see §3.3.
func (x *Exchange) Findings() []Finding {
	in := x.journalFindings()
	if len(in) == 0 {
		return nil
	}
	out := make([]Finding, len(in))
	for i, f := range in {
		out[i] = Finding{Severity: Severity(f.Severity), Code: f.Code, Field: f.Field, Message: f.Message}
	}
	return out
}

// journalFindings is Findings in the journal's own type, for Handle: the
// journal entry's Findings field stays journal.Finding, since journal must
// not import provider (journal.doc.go).
//
// Message is passed through redact.String here, at the one place both
// Findings() and the journal entry's Findings field are built from, so
// "redacted before it is stored, logged or served" (Finding's doc comment)
// is true of the served path too: a handler that renders Findings() into a
// 4xx body (exa's classify, tavily's errorFindings, perplexity's
// validationErrorBody) does so before Handle's own record() ever runs
// journal.Redact over the entry. redact.String is identity on ordinary
// finding text, so this changes no existing wire body.
func (x *Exchange) journalFindings() []journal.Finding {
	if len(x.findings) == 0 {
		return nil
	}
	policy := x.policy()
	out := make([]journal.Finding, len(x.findings))
	for i, f := range x.findings {
		f.Severity = effectiveSeverity(policy, f)
		f.Message = redact.String(f.Message)
		out[i] = f
	}
	slices.SortStableFunc(out, func(a, b journal.Finding) int {
		if c := cmp.Compare(severityRank(a.Severity), severityRank(b.Severity)); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Field, b.Field); c != 0 {
			return c
		}
		return cmp.Compare(a.Code, b.Code)
	})
	return out
}

// Fault returns this request's fault decision, claiming an attempt index from
// Deps.Faults on the first call and memoising it for every later one. The
// attempt is claimed on the resolved lane's cursor key, not on Route.FaultKey:
// one route serving N concurrent callers is N lanes, and counting them as one
// hands each caller the attempt scripted for another.
//
// It is lazy for a reason that is a rule, not an optimisation: only a request
// that survived routing, authentication and validation may consume an attempt
// (§4.4), yet turn selection needs the call index *while* it renders. A handler
// therefore calls Fault only once it has decided to render a scenario response,
// and Handle claims it after the handler returns if the handler did not. Either
// way exactly one index is claimed, from the one counter turn selection and fault
// selection share.
//
// Index is -1 until an attempt is claimed.
func (x *Exchange) Fault() FaultDecision {
	if !x.claimed {
		x.claimed = true
		x.decision = x.Deps.Faults.Next(x.cursorKey())
	}
	return x.decision
}

// cursorKey is the counter key this request's attempts and turns are drawn from.
//
// An Exchange that Handle did not build — a provider package's unit test
// constructing one directly — has no resolved lane, and falls back to
// Route.FaultKey, which is exactly what the whole request path used before lanes
// existed.
func (x *Exchange) cursorKey() string {
	if x.lane.Key == "" {
		return x.Route.FaultKey
	}
	return x.lane.CursorKey()
}

// CallIndex returns the zero-based number of this call within its lane, claiming
// an attempt if one has not been claimed yet. It is what a turn's
// `when: {call_index: N}` matches against.
func (x *Exchange) CallIndex() int {
	return x.Fault().Index
}

// String returns the request's string property at key, and whether it was present
// and of the right type. A present-but-wrong-type value records no finding by
// itself; the caller decides whether that is an error.
func (x *Exchange) String(key string) (string, bool) {
	v, ok := x.Body[key].(string)
	return v, ok
}

// Number returns the request's numeric property at key.
func (x *Exchange) Number(key string) (float64, bool) {
	v, ok := x.Body[key].(float64)
	return v, ok
}

// Bool returns the request's boolean property at key.
func (x *Exchange) Bool(key string) (bool, bool) {
	v, ok := x.Body[key].(bool)
	return v, ok
}

// Object returns the request's object property at key.
func (x *Exchange) Object(key string) (map[string]any, bool) {
	v, ok := x.Body[key].(map[string]any)
	return v, ok
}

// Has reports whether key is present at the top level of the request body.
func (x *Exchange) Has(key string) bool {
	if x.Body == nil {
		return false
	}
	_, ok := x.Body[key]
	return ok
}

// Entry returns the scenario provider entry this request is served from, or nil:
// [Route.Entry] when the route names one, and the listener's own provider name
// otherwise.
//
// The route is consulted first because a listener may serve more than one entry.
// Resolving by listener name alone — which this did until Route.Entry existed —
// meant every entry after the first had its own turn_key and validation block
// silently ignored, since both reach the scenario through here.
func (x *Exchange) Entry() *scenario.ProviderEntry {
	if x.Route.Entry != "" {
		return x.Deps.Scenario.Provider(x.Route.Entry)
	}
	return x.Deps.Scenario.Provider(string(x.Provider))
}

// policy returns the ValidationPolicy governing this request, or nil.
func (x *Exchange) policy() *scenario.ValidationPolicy {
	e := x.Entry()
	if e == nil {
		return nil
	}
	return e.Validation
}

// effectiveSeverity applies a scenario's promotions and demotions to one finding.
// Demotion is applied last, so a code listed in both promote and demote — or
// demoted under strict — ends up a warning. Silently failing a request the author
// explicitly demoted is the worse of the two surprises.
func effectiveSeverity(p *scenario.ValidationPolicy, f journal.Finding) journal.Severity {
	if p == nil {
		return f.Severity
	}
	sev := f.Severity
	if p.Strict || slices.Contains(p.Promote, f.Code) {
		sev = journal.SeverityError
	}
	if slices.Contains(p.Demote, f.Code) {
		sev = journal.SeverityWarning
	}
	return sev
}

// severityRank orders errors before warnings, and anything unrecognised last.
func severityRank(s journal.Severity) int {
	switch s {
	case journal.SeverityError:
		return 0
	case journal.SeverityWarning:
		return 1
	default:
		return 2
	}
}
