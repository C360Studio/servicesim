package perplexity

import (
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Provider entry names in a scenario.
//
// Sonar and the Agent API are separate entries so that a scenario can rate-limit
// one surface while the other stays healthy — which is exactly how a consumer's
// Sonar-to-Agent migration fallback gets tested. A scenario that only uses Sonar
// simply omits the Agent entry.
const (
	NameSonar = "perplexity"
	NameAgent = "perplexity_agent"
)

// Fault budget keys. Every alias of a surface shares that surface's key, so a
// retry through an alias draws on the same attempt budget rather than getting a
// fresh set of retries — an alias with a budget of its own would make a
// scenario's fault plan silently stop meaning anything. The Agent surface gets
// its own key, which is what makes the per-surface fault policy above possible.
const (
	FaultKeyCompletions = "perplexity:completions"
	FaultKeyAgent       = "perplexity:agent"
)

// Route patterns, in registration order.
//
// Each surface is served under three spellings: its canonical vendor path, and
// both of the paths the OpenAI SDK produces. The SDK appends /chat/completions
// or /responses to its configured base_url, and that base URL may or may not
// already end in /v1 — so https://host and https://host/v1 are both conventions
// a consumer picks arbitrarily, and both must work. Registering only one of each
// pair made whether the simulator answered at all depend on which convention the
// consumer happened to choose, and the other convention 404ed with no hint that
// the path was the problem.
const (
	PatternSonar             = "POST /v1/sonar"
	PatternChatCompletions   = "POST /chat/completions"
	PatternChatCompletionsV1 = "POST /v1/chat/completions"
	PatternAgent             = "POST /v1/agent"
	PatternResponses         = "POST /v1/responses"
	PatternResponsesBare     = "POST /responses"
)

// sonarFault selects the Sonar fault plan out of a scenario.
func sonarFault(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, NameSonar) }

// agentFault selects the Agent fault plan out of a scenario.
func agentFault(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, NameAgent) }

// RouteSonar returns POST /v1/sonar, the canonical Sonar endpoint.
func RouteSonar() provider.Route {
	return provider.Route{Pattern: PatternSonar, FaultKey: FaultKeyCompletions, Fault: sonarFault}
}

// RouteChatCompletions returns POST /chat/completions, the OpenAI SDK alias of
// the Sonar endpoint reached from a base_url that already ends in /v1, on the
// same fault key.
func RouteChatCompletions() provider.Route {
	return provider.Route{Pattern: PatternChatCompletions, FaultKey: FaultKeyCompletions, Fault: sonarFault}
}

// RouteChatCompletionsV1 returns POST /v1/chat/completions, the OpenAI SDK alias
// of the Sonar endpoint reached from a base_url with no /v1 suffix, on the same
// fault key.
func RouteChatCompletionsV1() provider.Route {
	return provider.Route{Pattern: PatternChatCompletionsV1, FaultKey: FaultKeyCompletions, Fault: sonarFault}
}

// RouteAgent returns POST /v1/agent, the canonical Agent API endpoint.
func RouteAgent() provider.Route {
	return provider.Route{Pattern: PatternAgent, FaultKey: FaultKeyAgent, Fault: agentFault}
}

// RouteResponses returns POST /v1/responses, the OpenAI SDK alias of the Agent
// endpoint reached from a base_url with no /v1 suffix, on the same fault key.
func RouteResponses() provider.Route {
	return provider.Route{Pattern: PatternResponses, FaultKey: FaultKeyAgent, Fault: agentFault}
}

// RouteResponsesBare returns POST /responses, the OpenAI SDK alias of the Agent
// endpoint reached from a base_url that already ends in /v1, on the same fault
// key.
func RouteResponsesBare() provider.Route {
	return provider.Route{Pattern: PatternResponsesBare, FaultKey: FaultKeyAgent, Fault: agentFault}
}

// Routes returns the six Perplexity routes across two surfaces, in registration
// order. Each carries the fault budget it draws on and the selector for the
// scenario entry that budget is declared in, so the composition layer can build
// the fault engine's key set by concatenating the providers' Routes().
//
// Six routes, not six endpoints: three spellings of Sonar followed by three
// spellings of the Agent API. The fault engine registers one counter per distinct
// FaultKey, so the extra spellings cost nothing and cannot fork a budget.
//
// It is a function, not a package-level var, so no consumer can mutate the route
// table of a package it merely imported.
func Routes() []provider.Route {
	return []provider.Route{
		RouteSonar(), RouteChatCompletions(), RouteChatCompletionsV1(),
		RouteAgent(), RouteResponses(), RouteResponsesBare(),
	}
}

// New returns the Perplexity handler, built with provider.NewMux over Routes().
//
// The zero Deps is usable: it serves well-shaped empty successes on all six
// routes with no journal, no faults and a real clock. Note that a zero Deps
// means no faults even if the Scenario declares them — pass testkit.NewFaults(s)
// as Deps.Faults, or use testkit.Start, to get the scenario's declared faults;
// Deps.Normalized logs deps.faults_ignored if you do not.
func New(deps provider.Deps) http.Handler {
	d := deps.Normalized()

	// The Sonar sunset is a property of the simulated API rather than of any
	// request, so it is announced once here instead of as per-request noise that
	// would drown the findings a consumer can act on.
	d.Logger.Info("perplexity.sonar.sunset",
		slog.String("date", SunsetDate.Format("2006-01-02")),
		slog.String("successor", PatternAgent))

	return provider.NewMux(d, provider.Perplexity, provider.MuxSpec{
		Routes: Routes(),
		Handlers: map[string]provider.Handler{
			PatternSonar:             handleSonar,
			PatternChatCompletions:   handleSonar,
			PatternChatCompletionsV1: handleSonar,
			PatternAgent:             handleAgent,
			PatternResponses:         handleAgent,
			PatternResponsesBare:     handleAgent,
		},
		NotFound: func(_ *provider.Exchange) provider.Response {
			// An unmatched path cannot know which of the two surfaces was
			// intended, so fail-closed routing uses the Sonar-shaped body.
			return errorResponse(SurfaceSonar, http.StatusNotFound, "")
		},
		MethodNotAllowed: func(allow []string) provider.Handler {
			return func(_ *provider.Exchange) provider.Response {
				resp := errorResponse(SurfaceSonar, http.StatusMethodNotAllowed, "")
				resp.Header = http.Header{"Allow": []string{strings.Join(allow, ", ")}}
				return resp
			}
		},
	})
}

// errorResponse builds a provider-shaped error response for a surface.
func errorResponse(surface Surface, status int, message string) provider.Response {
	return provider.Response{
		Status: status,
		Body:   ErrorBody(surface, status, message),
		Label:  "perplexity." + string(surface) + ".error." + strconv.Itoa(status),
	}
}

// validationResponse builds the FastAPI 422 body, or the surface's own envelope
// for the statuses that are not field validation failures.
func validationResponse(surface Surface, findings []journal.Finding, order []string) provider.Response {
	status := errorStatus(findings)
	if status != http.StatusUnprocessableEntity {
		return errorResponse(surface, status, "")
	}
	return provider.Response{
		Status: status,
		Body:   validationErrorBody(findings, order),
		Label:  "perplexity." + string(surface) + ".error.422",
	}
}

// handleSonar serves POST /v1/sonar and its two OpenAI SDK aliases.
//
// The order is the pipeline §4.4 fixes: route match, authentication, request
// validation, then — and only then — fault selection and rendering. A request
// rejected before that point consumes no attempt, so a consumer's unrelated
// request bug can never silently eat a retry budget.
func handleSonar(x *provider.Exchange) provider.Response {
	entry := x.Deps.Scenario.Provider(NameSonar)

	checkContentType(x)
	checkAuth(x, entry)
	if rejectStream(x, entry) {
		return validationResponse(SurfaceSonar, x.Findings(), sonarFields)
	}
	model := validateSonarRequest(x)
	if x.Failed() {
		return validationResponse(SurfaceSonar, x.Findings(), sonarFields)
	}

	var p PerplexityProjection
	if entry != nil {
		turn, index := provider.SelectTurnFor(x, entry)
		if turn == nil {
			return errorResponse(SurfaceSonar, http.StatusNotFound, "")
		}
		if err := turn.DecodeProjection(entry.Name, index, &p); err != nil {
			x.Fail(CodeProjectionInvalid, "", "projection could not be decoded: %s", err)
			return errorResponse(SurfaceSonar, http.StatusInternalServerError, "")
		}
		noteUnresolved(x, x.Deps.Scenario.ResolveRefs("", &p))
	} else {
		// No scenario entry at all: still claim the attempt index, so that a
		// zero-configuration handler counts calls the way a configured one does
		// and `when: {call_index: N}` stays meaningful.
		x.Fault()
	}

	body, err := renderSonar(x, &p, model)
	if err != nil {
		x.Fail(CodeRenderFailed, "", "response body could not be rendered: %s", err)
		return errorResponse(SurfaceSonar, http.StatusInternalServerError, "")
	}
	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         "perplexity.sonar.ok",
		FaultEligible: true,
		FaultBody:     faultBody(SurfaceSonar),
	}
}

// handleAgent serves POST /v1/agent and its /v1/responses alias.
func handleAgent(x *provider.Exchange) provider.Response {
	entry := x.Deps.Scenario.Provider(NameAgent)

	checkContentType(x)
	checkAuth(x, entry)
	model := validateAgentRequest(x)
	if x.Failed() {
		return validationResponse(SurfaceAgent, x.Findings(), agentFields)
	}

	var p PerplexityAgent
	if entry != nil {
		turn, index := provider.SelectTurnFor(x, entry)
		if turn == nil {
			return errorResponse(SurfaceAgent, http.StatusNotFound, "")
		}
		if err := turn.DecodeProjection(entry.Name, index, &p); err != nil {
			x.Fail(CodeProjectionInvalid, "", "projection could not be decoded: %s", err)
			return errorResponse(SurfaceAgent, http.StatusInternalServerError, "")
		}
		noteUnresolved(x, x.Deps.Scenario.ResolveRefs("", &p))
	} else {
		x.Fault()
	}

	body, err := renderAgent(x, &p, model)
	if err != nil {
		x.Fail(CodeRenderFailed, "", "response body could not be rendered: %s", err)
		return errorResponse(SurfaceAgent, http.StatusInternalServerError, "")
	}
	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         "perplexity.agent.ok",
		FaultEligible: true,
		FaultBody:     faultBody(SurfaceAgent),
	}
}

// CodeStreamPolicyUnknown is raised at startup for a Sonar `stream:` value that
// is neither warn nor reject. It is an error rather than a warning because the
// silent fallback would be warn — the very behaviour the author was trying to
// turn off — and a typo that quietly re-enables a permissive default is exactly
// the failure a reject policy exists to prevent.
const CodeStreamPolicyUnknown = "perplexity.stream.policy.unknown"

// CodeStreamPolicyIgnored is raised at startup for a `stream:` declared on any
// turn but the first, where it cannot be honoured. It is a warning, not an
// error: the scenario is servable, but the author needs to know the knob they
// set is doing nothing.
const CodeStreamPolicyIgnored = "perplexity.stream.policy.ignored"

// streamPolicy returns the Sonar provider block's streaming policy.
//
// It reads the first turn rather than the selected one because the policy decides
// whether a request is *rejected*, and rejection has to happen before turn
// selection claims an attempt — a request refused for streaming must not eat a
// retry budget. A policy that varied per turn could therefore never be honoured,
// so it is treated as a property of the provider block; validateSonarProjection
// warns about a later turn that declares one anyway rather than ignoring it in
// silence.
func streamPolicy(e *scenario.ProviderEntry) scenario.StreamPolicy {
	if e == nil || len(e.Turns) == 0 {
		return scenario.StreamWarn
	}
	var p PerplexityProjection
	if err := e.Turns[0].DecodeProjection(e.Name, 0, &p); err != nil {
		// Unreachable through internal/server, which validates before readiness.
		// The permissive default is the right fallback for an undecodable body:
		// the decode failure is reported on its own further down the pipeline.
		return scenario.StreamWarn
	}
	if p.Stream == "" {
		return scenario.StreamWarn
	}
	return p.Stream
}

// rejectStream applies a `stream: reject` policy to a streaming request and
// reports whether it rejected one.
//
// It runs ahead of validateSonarRequest and returns immediately, rather than
// alongside the field checks, because validateSonarRequest's own stream warning
// promises "this request receives the ordinary non-streaming body" — true under
// the default warn policy and a lie under this one. Journalling both would leave
// a consumer reading two findings that contradict each other.
//
// Streaming is not simulated at all yet, so the default stays warn: a scenario
// has to opt in. Opting in is what lets a consumer whose primary path always
// streams stop recording fixtures against a complete non-streaming 200 that the
// real API would never have sent.
func rejectStream(x *provider.Exchange, e *scenario.ProviderEntry) bool {
	if streamPolicy(e) != scenario.StreamReject {
		return false
	}
	if stream, ok := x.Bool("stream"); !ok || !stream {
		return false
	}
	x.Fail(CodeStreamUnimplemented, "body.stream",
		"streaming responses are not simulated and this scenario rejects them; "+
			"a non-streaming request is served normally")
	return true
}

// noteUnresolved records a warning per source reference that did not resolve.
// Startup validation should have caught these, so reaching one here means the
// scenario was served without being validated; the response still renders, with
// the unresolved entry carrying only what the reference named.
func noteUnresolved(x *provider.Exchange, findings []scenario.Finding) {
	for _, f := range findings {
		x.Warn(CodeProjectionUnresolved, f.Path, "%s", f.Message)
	}
}

// -----------------------------------------------------------------------------
// Startup validation
// -----------------------------------------------------------------------------

// Validators returns this package's projection validators keyed on the
// scenario provider kind they serve, ready to be merged into the registry
// provider.ValidateScenario is given.
//
// Both surfaces validate independently, because both are independent scenario
// entries. Registering only one would mean the other's fixtures were never
// decoded until the first request arrived — and a fixture with a bad field must
// fail at boot, not on a consumer's first call.
func Validators() map[string]provider.Validator {
	return map[string]provider.Validator{
		NameSonar: SonarValidator{},
		NameAgent: AgentValidator{},
	}
}

// SonarValidator decodes and checks the Sonar projections in a scenario.
type SonarValidator struct{}

// ValidateProjections decodes every turn's Sonar projection body and reports
// what it finds, addressed by the turn's YAML path.
func (SonarValidator) ValidateProjections(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	return validateTurns(s, e, func(path string, turn *scenario.Turn, index int) []scenario.Finding {
		var p PerplexityProjection
		if err := turn.DecodeProjection(e.Name, index, &p); err != nil {
			return []scenario.Finding{decodeFinding(path, err)}
		}
		findings := s.ResolveRefs(path, &p)
		return append(findings, validateSonarProjection(path, &p, index)...)
	})
}

// AgentValidator decodes and checks the Agent API projections in a scenario.
type AgentValidator struct{}

// ValidateProjections decodes every turn's Agent projection body and reports
// what it finds, addressed by the turn's YAML path.
func (AgentValidator) ValidateProjections(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	return validateTurns(s, e, func(path string, turn *scenario.Turn, index int) []scenario.Finding {
		var p PerplexityAgent
		if err := turn.DecodeProjection(e.Name, index, &p); err != nil {
			return []scenario.Finding{decodeFinding(path, err)}
		}
		findings := s.ResolveRefs(path, &p)
		return append(findings, validateAgentProjection(path, &p)...)
	})
}

// validateTurns walks an entry's turns in declaration order and addresses each
// finding by the turn's YAML path. Declaration order, never map order: a
// readiness failure that reordered its own reasons between runs would be
// miserable to diff.
func validateTurns(s *scenario.Scenario, e *scenario.ProviderEntry,
	check func(path string, turn *scenario.Turn, index int) []scenario.Finding,
) []scenario.Finding {
	if s == nil || e == nil {
		return nil
	}
	var findings []scenario.Finding
	for i := range e.Turns {
		path := "providers." + e.Name + ".turns[" + strconv.Itoa(i) + "].respond"
		findings = append(findings, check(path, &e.Turns[i], i)...)
	}
	return findings
}

// decodeFinding reports a projection body this build cannot decode. It is an
// error rather than a warning: a scenario whose projection does not decode
// cannot render, and discovering that on a consumer's first request instead of
// at readiness is precisely what startup validation exists to prevent.
func decodeFinding(path string, err error) scenario.Finding {
	return scenario.Finding{
		Severity: scenario.SeverityError,
		Code:     CodeProjectionInvalid,
		Path:     path,
		Message:  err.Error(),
	}
}

// validateSonarProjection checks one decoded Sonar projection at startup. index
// is the turn's position, which only the streaming policy cares about.
func validateSonarProjection(path string, p *PerplexityProjection, index int) []scenario.Finding {
	var findings []scenario.Finding
	add := func(code, at, message string) {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError, Code: code, Path: at, Message: message,
		})
	}

	switch p.Stream {
	case "", scenario.StreamWarn, scenario.StreamReject:
		if p.Stream != "" && index > 0 {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityWarning,
				Code:     CodeStreamPolicyIgnored,
				Path:     path + ".stream",
				Message: "the streaming policy is a property of the provider block and is read from the first " +
					"turn only; this value is ignored",
			})
		}
	default:
		add(CodeStreamPolicyUnknown, path+".stream",
			"stream policy "+strconv.Quote(string(p.Stream))+" is not warn or reject")
	}

	if fr := p.FinishReason; fr != "" && !slices.Contains(FinishReasons, fr) {
		add("perplexity.finish_reason.invalid", path+".finish_reason",
			"finish_reason "+strconv.Quote(fr)+" is not stop or length")
	}
	for i := range p.SearchResults {
		if st := p.SearchResults[i].SourceType; st != "" && !slices.Contains(SourceTypes, st) {
			add("perplexity.source_type.invalid",
				path+".search_results["+strconv.Itoa(i)+"].source_type",
				"source_type "+strconv.Quote(st)+" is not web or attachment")
		}
	}
	if u := p.Usage; u != nil {
		if u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 {
			add("perplexity.usage.negative", path+".usage", "token counts must not be negative")
		}
	}
	return findings
}
