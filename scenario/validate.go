package scenario

import (
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// Severity classifies a scenario validation finding.
type Severity string

// Finding severities.
const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Finding is one scenario validation result. It is distinct from journal.Finding,
// which records a per-request observation: a scenario finding is an authoring
// problem found once at load, addressed by a YAML path, and it can prevent the
// process from starting.
type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Path     string   `json:"path,omitempty"` // YAML path, e.g. providers.exa.turns[0].respond
	Message  string   `json:"message"`
}

// Report is the outcome of validating a scenario.
type Report struct {
	Findings []Finding `json:"findings"`
}

// Errors returns only the error-severity findings.
func (r Report) Errors() []Finding {
	return r.filter(SeverityError)
}

// Warnings returns only the warning-severity findings.
func (r Report) Warnings() []Finding {
	return r.filter(SeverityWarning)
}

func (r Report) filter(s Severity) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity == s {
			out = append(out, f)
		}
	}
	return out
}

// OK reports whether the scenario is loadable, that is, has no error findings.
func (r Report) OK() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			return false
		}
	}
	return true
}

// Err returns an error naming every error-severity finding, or nil when there are
// none. Callers log the whole Report; this is what they return upward.
func (r Report) Err() error {
	errs := r.Errors()
	if len(errs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(errs))
	for _, f := range errs {
		if f.Path == "" {
			parts = append(parts, f.Message)
			continue
		}
		parts = append(parts, f.Path+": "+f.Message)
	}
	return fmt.Errorf("invalid scenario: %s", strings.Join(parts, "; "))
}

func (r *Report) add(sev Severity, code, path, format string, args ...any) {
	r.Findings = append(r.Findings, Finding{
		Severity: sev,
		Code:     code,
		Path:     path,
		Message:  fmt.Sprintf(format, args...),
	})
}

// reservedURLSuffixes are the only domains scenario and fixture data may use.
// A scenario URL that resolves to a real host is the exact failure this simulator
// exists to prevent, so it is called out even though nothing ever dials it.
var reservedURLSuffixes = []string{".test", ".example", ".invalid", "example.com"}

// Validate checks the scenario envelope: schema version, required fields, source
// integrity, provider-block coherence, turn ordering, `when` well-formedness and
// fault-plan coherence. It does not mutate the scenario.
//
// It deliberately does not validate a projection body. Under the open provider
// registry a body is an undecoded node whose type only its provider package knows;
// provider.ValidateScenario decodes and checks them, and internal/server calls it
// before readiness reports true.
func (s *Scenario) Validate() Report {
	var r Report
	if s == nil {
		r.add(SeverityError, "scenario.missing", "", "no scenario")
		return r
	}

	// Parse already gates the version before decoding; this repeats the check
	// because Validate is reachable directly on a hand-built Scenario, which is
	// how testkit and provider tests construct one. Both gates share the same
	// predicate so they cannot drift into disagreeing about what loads.
	if !versionSupported(s.Version, SchemaVersion) {
		r.add(SeverityError, "scenario.version.unsupported", "version",
			"%s", unsupportedVersionMessage(s.Version, SchemaVersion))
	}
	if strings.TrimSpace(s.Name) == "" {
		r.add(SeverityError, "scenario.name.missing", "name", "name is required")
	}

	seen := make(map[string]int, len(s.Sources))
	for i := range s.Sources {
		s.validateSource(&r, i, seen)
	}

	for _, name := range s.Providers.Names() {
		validateProvider(&r, s.Providers.Get(name))
	}
	return r
}

func (s *Scenario) validateSource(r *Report, i int, seen map[string]int) {
	src := &s.Sources[i]
	path := fmt.Sprintf("sources[%d]", i)

	switch {
	case src.ID == "":
		r.add(SeverityError, "scenario.source.id.missing", path+".id", "id is required")
	default:
		if first, dup := seen[src.ID]; dup {
			r.add(SeverityError, "scenario.source.id.duplicate", path+".id",
				"duplicate source id %q, already declared at sources[%d]", src.ID, first)
		} else {
			seen[src.ID] = i
		}
	}
	if src.URL == "" {
		r.add(SeverityError, "scenario.source.url.missing", path+".url", "url is required")
	} else if host := hostOf(src.URL); host != "" && !isReservedHost(host) {
		r.add(SeverityWarning, "scenario.source.url.not_reserved", path+".url",
			"url host %q is not a reserved domain; scenario data must use .test, .example, .invalid or example.com so a base URL can never resolve to a real paid API",
			host)
	}
	if src.Title == "" {
		r.add(SeverityError, "scenario.source.title.missing", path+".title", "title is required")
	}
	for j := range src.Claims {
		if src.Claims[j].ID == "" {
			r.add(SeverityError, "scenario.claim.id.missing",
				fmt.Sprintf("%s.claims[%d].id", path, j), "claim id is required")
		}
	}
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func isReservedHost(host string) bool {
	host = strings.ToLower(host)
	for _, suffix := range reservedURLSuffixes {
		if host == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func validateProvider(r *Report, e *ProviderEntry) {
	if e == nil {
		return
	}
	base := "providers." + e.Name

	if e.Kind == "" {
		r.add(SeverityError, "scenario.provider.kind.empty", base+".kind", "kind must not be empty")
	}
	if len(e.strayKeys) > 0 {
		r.add(SeverityError, "scenario.provider.body_with_turns", base,
			"a provider block declares `turns:` and a projection body at the same time; move %s inside a turn's `respond:`",
			strings.Join(e.strayKeys, ", "))
	}
	if e.faultWithTurns {
		r.add(SeverityError, "scenario.provider.fault_with_turns", base+".fault",
			"a provider block declares `turns:` and a block-level `fault:`; in the multi-turn form the fault belongs on the turn it applies to")
	}
	if e.Auth != nil {
		switch e.Auth.Mode {
		case "", AuthRequired, AuthOptional, AuthReject:
		default:
			r.add(SeverityError, "scenario.auth.mode.unknown", base+".auth.mode",
				"unknown auth mode %q; expected required, optional or reject", e.Auth.Mode)
		}
	}
	validateTurnKey(r, base, e.TurnKey)

	if len(e.Turns) == 0 {
		r.add(SeverityError, "scenario.provider.turns.empty", base+".turns",
			"a provider block must declare at least one turn")
		return
	}
	for i := range e.Turns {
		validateTurn(r, e, i)
	}
	for i := 0; i < len(e.Turns)-1; i++ {
		if e.Turns[i].When.IsEmpty() {
			r.add(SeverityError, "scenario.turn.unreachable", e.turnPath(i),
				"turn %d has no `when`, so it always matches and turns %d..%d can never be selected; move the unconditional turn last",
				i, i+1, len(e.Turns)-1)
			break
		}
	}
}

// validateTurnKey checks the form of each extractor and nothing else.
//
// In particular a turn_key that names no "route" is legal and needs no finding:
// the route is always part of the lane, so turn_key adds discriminators to it and
// cannot remove it. An explicit "route" is equally legal and is de-duplicated
// where the lane key is composed, which is why both spellings validate the same.
func validateTurnKey(r *Report, base string, k TurnKey) {
	for i, extractor := range k {
		path := fmt.Sprintf("%s.turn_key[%d]", base, i)
		switch {
		case extractor == TurnKeyRoute:
		case strings.HasPrefix(extractor, TurnKeyBodyJSONPrefix):
			if strings.TrimPrefix(extractor, TurnKeyBodyJSONPrefix) == "" {
				r.add(SeverityError, "scenario.turn_key.invalid", path,
					"body_json: extractor needs a dotted path, for example body_json:model")
			}
		case strings.HasPrefix(extractor, TurnKeyHeaderPrefix):
			if strings.TrimPrefix(extractor, TurnKeyHeaderPrefix) == "" {
				r.add(SeverityError, "scenario.turn_key.invalid", path,
					"header: extractor needs a header name, for example header:x-role")
			}
		default:
			r.add(SeverityError, "scenario.turn_key.invalid", path,
				"unknown turn-key extractor %q; expected route, body_json:<path> or header:<name>", extractor)
		}
	}
}

func (e *ProviderEntry) turnPath(i int) string {
	if !e.scripted {
		return "providers." + e.Name
	}
	return fmt.Sprintf("providers.%s.turns[%d]", e.Name, i)
}

func validateTurn(r *Report, e *ProviderEntry, i int) {
	turn := &e.Turns[i]
	path := e.turnPath(i)

	if turn.Respond.Kind != yaml.MappingNode {
		r.add(SeverityError, "scenario.turn.respond.not_mapping", path+".respond",
			"a turn's projection body must be a mapping, got a %s", nodeKindName(turn.Respond.Kind))
	}
	if turn.When != nil {
		if turn.When.CallIndex != nil && *turn.When.CallIndex < 0 {
			r.add(SeverityError, "scenario.turn.when.invalid", path+".when.call_index",
				"call_index must not be negative")
		}
		for key := range turn.When.BodyJSON {
			if strings.TrimSpace(key) == "" {
				r.add(SeverityError, "scenario.turn.when.invalid", path+".when.body_json",
					"body_json keys must be non-empty dotted paths")
			}
		}
	}
	validateFault(r, path+".fault", turn.Fault)
}

func validateFault(r *Report, path string, f *Fault) {
	if f == nil {
		return
	}
	if len(f.Attempts) == 0 {
		r.add(SeverityError, "scenario.fault.attempts.empty", path+".attempts",
			"a fault plan must declare at least one attempt")
	}
	switch f.After {
	case "", FaultAfterSuccess, FaultAfterRepeatLast:
	default:
		r.add(SeverityError, "scenario.fault.after.unknown", path+".after",
			"unknown after %q; expected success or repeat_last", f.After)
	}
	for i := range f.Attempts {
		validateFaultAttempt(r, fmt.Sprintf("%s.attempts[%d]", path, i), f.Attempts[i])
	}
}

func validateFaultAttempt(r *Report, path string, a FaultAttempt) {
	switch a.Kind {
	case FaultNone, FaultStatus, FaultCloseBeforeHeaders, FaultTruncateBody,
		FaultInvalidJSON, FaultWrongContentType, FaultEmptyBody, FaultExtraFields, FaultOversizedBody,
		FaultStreamDisconnect, FaultStreamTruncateChunk, FaultStreamStall:
	default:
		r.add(SeverityError, "scenario.fault.kind.unknown", path+".kind",
			"unknown fault kind %q", a.Kind)
	}
	if a.AfterChunk != 0 {
		switch a.EffectiveKind() {
		case FaultStreamDisconnect, FaultStreamTruncateChunk, FaultStreamStall:
		default:
			r.add(SeverityError, CodeAfterChunkNotStreaming, path+".after_chunk",
				"after_chunk is meaningful only for stream_disconnect, stream_truncate_chunk or stream_stall, not %q",
				a.EffectiveKind())
		}
	}
	if a.BodyBytes != 0 {
		switch a.EffectiveKind() {
		case FaultOversizedBody:
		default:
			r.add(SeverityError, "scenario.fault.body_bytes.not_oversized", path+".body_bytes",
				"body_bytes is meaningful only for kind oversized_body, not %q", a.EffectiveKind())
		}
	}
	if a.Status != 0 && (a.Status < 100 || a.Status > 599) {
		r.add(SeverityError, "scenario.fault.status.invalid", path+".status",
			"status %d is not an HTTP status code", a.Status)
	}
	if a.Delay < 0 {
		r.add(SeverityError, "scenario.fault.delay.negative", path+".delay",
			"delay must not be negative")
	}
	if a.DelayAfterHeaders < 0 {
		r.add(SeverityError, "scenario.fault.delay_after_headers.negative", path+".delay_after_headers",
			"delay_after_headers must not be negative")
	}
	if a.DelayAfterHeaders > 0 {
		switch a.EffectiveKind() {
		case FaultCloseBeforeHeaders:
			r.add(SeverityError, "scenario.fault.delay_after_headers.no_headers", path+".delay_after_headers",
				"delay_after_headers cannot apply to close_before_headers: no headers ever reach the client to hang after")
		case FaultEmptyBody:
			r.add(SeverityWarning, "scenario.fault.delay_after_headers.unobservable", path+".delay_after_headers",
				"empty_body sets Content-Length: 0, so the client considers THIS response complete at the headers "+
					"and the hang is invisible on it; it still delays the journal entry and stalls the next "+
					"request on the same keep-alive connection")
		}
		if a.EffectiveKind().IsStream() {
			r.add(SeverityError, "scenario.fault.delay_after_headers.streaming", path+".delay_after_headers",
				"delay_after_headers assumes an ordinary JSON body; the streaming equivalent is stream_stall with after_chunk: 0")
		}
	}
	if a.RetryAfter != nil && *a.RetryAfter < 0 {
		r.add(SeverityError, "scenario.fault.retry_after.negative", path+".retry_after",
			"retry_after must not be negative")
	}
	if a.TruncateAfterBytes < 0 {
		r.add(SeverityError, "scenario.fault.truncate.negative", path+".truncate_after_bytes",
			"truncate_after_bytes must not be negative")
	}
	if a.BodyBytes < 0 {
		r.add(SeverityError, "scenario.fault.body_bytes.negative", path+".body_bytes",
			"body_bytes must not be negative")
	}
	if a.Repeat < 0 {
		r.add(SeverityError, "scenario.fault.repeat.negative", path+".repeat",
			"repeat must not be negative")
	}
	if a.EffectiveKind() == FaultInvalidJSON && a.RawBody == "" {
		r.add(SeverityWarning, "scenario.fault.raw_body.missing", path+".raw_body",
			"kind invalid_json with no raw_body leaves the malformed bytes to the provider package")
	}
	if a.EffectiveKind() == FaultOversizedBody && a.BodyBytes == 0 {
		r.add(SeverityError, "scenario.fault.body_bytes.missing", path+".body_bytes",
			"kind oversized_body has no default size — unlike truncate_after_bytes, there is no "+
				"\"zero means half the body\" fallback — so body_bytes must be set")
	}
}
