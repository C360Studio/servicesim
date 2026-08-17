package perplexity

import (
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

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
	return provider.Route{Pattern: PatternSonar, FaultKey: FaultKeyCompletions,
		Credentials: bearerOnly, Fault: sonarFault}
}

// RouteChatCompletions returns POST /chat/completions, the OpenAI SDK alias of
// the Sonar endpoint reached from a base_url that already ends in /v1, on the
// same fault key.
func RouteChatCompletions() provider.Route {
	return provider.Route{Pattern: PatternChatCompletions, FaultKey: FaultKeyCompletions,
		Credentials: bearerOnly, Fault: sonarFault}
}

// RouteChatCompletionsV1 returns POST /v1/chat/completions, the OpenAI SDK alias
// of the Sonar endpoint reached from a base_url with no /v1 suffix, on the same
// fault key.
func RouteChatCompletionsV1() provider.Route {
	return provider.Route{Pattern: PatternChatCompletionsV1, FaultKey: FaultKeyCompletions,
		Credentials: bearerOnly, Fault: sonarFault}
}

// RouteAgent returns POST /v1/agent, the canonical Agent API endpoint.
func RouteAgent() provider.Route {
	return provider.Route{Pattern: PatternAgent, FaultKey: FaultKeyAgent,
		Entry:       NameAgent,
		Credentials: bearerOnly, Fault: agentFault}
}

// RouteResponses returns POST /v1/responses, the OpenAI SDK alias of the Agent
// endpoint reached from a base_url with no /v1 suffix, on the same fault key.
func RouteResponses() provider.Route {
	return provider.Route{Pattern: PatternResponses, FaultKey: FaultKeyAgent,
		Entry:       NameAgent,
		Credentials: bearerOnly, Fault: agentFault}
}

// RouteResponsesBare returns POST /responses, the OpenAI SDK alias of the Agent
// endpoint reached from a base_url that already ends in /v1, on the same fault
// key.
func RouteResponsesBare() provider.Route {
	return provider.Route{Pattern: PatternResponsesBare, FaultKey: FaultKeyAgent,
		Entry:       NameAgent,
		Credentials: bearerOnly, Fault: agentFault}
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
	return slices.Concat(SonarRoutes(), AgentRoutes())
}

// SonarRoutes returns the three spellings of the Sonar surface, which is what the
// "perplexity" scenario entry scripts. It is separate from AgentRoutes because a
// `when.route:` is validated against the routes ITS entry serves: the two
// surfaces are independent entries with independent turn cursors, so naming an
// Agent route from a Sonar block is an authoring error worth reporting.
func SonarRoutes() []provider.Route {
	return []provider.Route{RouteSonar(), RouteChatCompletions(), RouteChatCompletionsV1()}
}

// AgentRoutes returns the three spellings of the Agent surface, which is what the
// "perplexity_agent" scenario entry scripts.
func AgentRoutes() []provider.Route {
	return []provider.Route{RouteAgent(), RouteResponses(), RouteResponsesBare()}
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
func validationResponse(surface Surface, findings []provider.Finding, order []string) provider.Response {
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
	policy := streamPolicy(entry)
	model := validateSonarRequest(x, policy)
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

	// Body is rendered unconditionally: it is what a non-streaming caller of
	// this same turn receives, and what a stream-suppressing fault writes
	// (provider.suppressStream). The two transports are rendered from one
	// projection, so a scenario cannot quote one cost when it streams and
	// another when it does not.
	body, err := renderSonar(x, &p, model)
	if err != nil {
		x.Fail(CodeRenderFailed, "", "response body could not be rendered: %s", err)
		return errorResponse(SurfaceSonar, http.StatusInternalServerError, "")
	}
	resp := provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         "perplexity.sonar.ok",
		FaultEligible: true,
		FaultBody:     faultBody(SurfaceSonar),
	}

	if wantsStream(x) && policy == scenario.StreamServe {
		stream, err := renderSonarStream(x, &p, model)
		if err != nil {
			x.Fail(CodeRenderFailed, "", "response stream could not be rendered: %s", err)
			return errorResponse(SurfaceSonar, http.StatusInternalServerError, "")
		}
		resp.Stream = stream
		resp.Label = "perplexity.sonar.stream"
		resp.Header = provider.StreamHeader()

		if mode, ok := x.String("stream_mode"); ok && mode == streamModeConcise {
			x.Warn(CodeStreamModeConciseUnscripted, "body.stream_mode",
				"stream_mode: concise is not simulated yet; this stream is served in stream_mode: full")
		}
	}
	return resp
}

// wantsStream reports whether this specific request asked to stream. It is a
// property of the REQUEST, not of the entry's policy: a consumer may
// legitimately send stream: true on one call and stream: false on the next
// in the same lane (docs/design/streaming.md preamble), so the entry's
// policy answers "does this surface serve a stream when asked" and this
// answers "did this call ask".
func wantsStream(x *provider.Exchange) bool {
	stream, ok := x.Bool("stream")
	return ok && stream
}

// handleAgent serves POST /v1/agent and its /v1/responses alias.
func handleAgent(x *provider.Exchange) provider.Response {
	entry := x.Deps.Scenario.Provider(NameAgent)

	checkContentType(x)
	checkAuth(x, entry)
	if rejectAgentStream(x, entry) {
		return validationResponse(SurfaceAgent, x.Findings(), agentFields)
	}
	policy := agentStreamPolicy(entry)
	model := validateAgentRequest(x, policy)
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

	// Body is rendered unconditionally, mirroring handleSonar: it is what a
	// non-streaming caller of this same turn receives and what a
	// stream-suppressing fault writes.
	body, err := renderAgent(x, &p, model)
	if err != nil {
		x.Fail(CodeRenderFailed, "", "response body could not be rendered: %s", err)
		return errorResponse(SurfaceAgent, http.StatusInternalServerError, "")
	}
	resp := provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         "perplexity.agent.ok",
		FaultEligible: true,
		FaultBody:     faultBody(SurfaceAgent),
	}

	if wantsStream(x) && policy == scenario.StreamServe {
		stream, err := renderAgentStream(x, &p, model)
		if err != nil {
			x.Fail(CodeRenderFailed, "", "response stream could not be rendered: %s", err)
			return errorResponse(SurfaceAgent, http.StatusInternalServerError, "")
		}
		resp.Stream = stream
		resp.Label = "perplexity.agent.stream"
		resp.Header = provider.StreamHeader()
	}
	return resp
}

// streamPolicy returns the Sonar provider entry's EFFECTIVE streaming
// policy — [scenario.StreamScript.EffectivePolicy] on turn 0's decoded
// Stream field.
//
// It reads the first turn rather than the selected one because the policy decides
// whether a request is *rejected*, and rejection has to happen before turn
// selection claims an attempt — a request refused for streaming must not eat a
// retry budget. A policy that varied per turn could therefore never be honoured,
// so it is treated as a property of the provider block; scenario.ValidateStreamScripts
// (called from SonarValidator.ValidateProjections) warns about a later turn
// that declares one anyway rather than ignoring it in silence.
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
	return p.Stream.EffectivePolicy()
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
// The default stays warn: a scenario has to opt in to either reject or stream.
// Opting into reject is what lets a consumer whose primary path always
// streams stop recording fixtures against a complete non-streaming 200 that the
// real API would never have sent.
func rejectStream(x *provider.Exchange, e *scenario.ProviderEntry) bool {
	if streamPolicy(e) != scenario.StreamReject {
		return false
	}
	if !wantsStream(x) {
		return false
	}
	x.Fail(CodeStreamUnimplemented, "body.stream",
		"streaming responses are not simulated and this scenario rejects them; "+
			"a non-streaming request is served normally")
	return true
}

// agentStreamPolicy returns the Agent provider entry's EFFECTIVE streaming
// policy — the Agent analogue of streamPolicy. It reads turn 0 for the same
// ordering reason: rejection has to happen before turn selection claims an
// attempt.
func agentStreamPolicy(e *scenario.ProviderEntry) scenario.StreamPolicy {
	if e == nil || len(e.Turns) == 0 {
		return scenario.StreamWarn
	}
	var p PerplexityAgent
	if err := e.Turns[0].DecodeProjection(e.Name, 0, &p); err != nil {
		// Unreachable through internal/server, which validates before readiness.
		return scenario.StreamWarn
	}
	return p.Stream.EffectivePolicy()
}

// rejectAgentStream applies a `stream: reject` policy to a streaming Agent
// request and reports whether it rejected one — the Agent analogue of
// rejectStream, for the same ordering reason: it runs ahead of
// validateAgentRequest and returns immediately, because validateAgentRequest's
// own stream warning promises "this request receives the ordinary
// non-streaming body", which is a lie under this policy.
func rejectAgentStream(x *provider.Exchange, e *scenario.ProviderEntry) bool {
	if agentStreamPolicy(e) != scenario.StreamReject {
		return false
	}
	if !wantsStream(x) {
		return false
	}
	x.Fail(CodeAgentStreamUnsupported, "body.stream",
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

// Routes implements provider.RouteLister, so a `when.route:` in a "perplexity"
// entry is checked against the Sonar surface alone.
func (SonarValidator) Routes() []provider.Route { return SonarRoutes() }

// ValidateProjections decodes every turn's Sonar projection body and reports
// what it finds, addressed by the turn's YAML path.
//
// The streaming grammar's own coherence — the policy enum, that only turn 0's
// policy is read, that a streaming entry's every turn has a non-empty script
// and a non-streaming entry's turns have none, that a scripted turn's deltas
// reassemble its answer, that a fault kind never targets a transport its
// entry does not use (truncate_body on a streaming entry, or a stream_* kind
// on one that is not), and that a stream_* attempt's after_chunk stays inside
// every turn's chunk count — is checked once here, across every turn together
// (scenario.ValidateStreamScripts, scenario.ValidateStreamFaultMismatch),
// rather than turn by turn inside validateSonarProjection: several of those
// checks are properties of the ENTRY, not of one turn in isolation.
func (SonarValidator) ValidateProjections(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	if s == nil || e == nil {
		return nil
	}
	streamTurns := make([]scenario.StreamTurn, len(e.Turns))
	findings := validateTurns(s, e, func(path string, turn *scenario.Turn, index int) []scenario.Finding {
		var p PerplexityProjection
		if err := turn.DecodeProjection(e.Name, index, &p); err != nil {
			streamTurns[index] = scenario.StreamTurn{Path: path}
			return []scenario.Finding{decodeFinding(path, err)}
		}
		streamTurns[index] = scenario.StreamTurn{Path: path, Script: &p.Stream, Answer: p.Answer}
		out := s.ResolveRefs(path, &p)
		return append(out, validateSonarProjection(path, &p)...)
	})

	findings = append(findings, scenario.ValidateStreamScripts(streamTurns)...)
	var entryPolicy scenario.StreamPolicy
	if len(streamTurns) > 0 {
		entryPolicy = streamTurns[0].Script.EffectivePolicy() // nil-safe: StreamWarn when turn 0 has none
	}
	findings = append(findings, scenario.ValidateStreamFaultMismatch(e, entryPolicy, streamTurns)...)
	return findings
}

// AgentValidator decodes and checks the Agent API projections in a scenario.
type AgentValidator struct{}

// Routes implements provider.RouteLister, so a `when.route:` in a
// "perplexity_agent" entry is checked against the Agent surface alone.
func (AgentValidator) Routes() []provider.Route { return AgentRoutes() }

// ValidateProjections decodes every turn's Agent projection body and reports
// what it finds, addressed by the turn's YAML path.
//
// The streaming grammar's own coherence is checked once here, across every
// turn together, exactly as SonarValidator does — same reasoning, same
// shared scenario.ValidateStreamScripts/ValidateStreamFaultMismatch calls
// (docs/design/streaming.md §7: "landing GrammarTyped is what gives the
// Agent entry a stream: key at all"). One difference from Sonar: each
// turn's StreamTurn carries an explicit ChunkCount, because GrammarTyped's
// after_chunk bound is len(Deltas)+5 (the five envelope events) for a
// completed/incomplete/in_progress/queued turn, 2 for a failed or cancelled
// one (renderAgentOutput emits no message item for those, so renderAgentStream
// has nothing to attach the envelope events around) — not scenario's
// GrammarDelta-shaped len(Deltas)+1 default — see agentChunkCount.
func (AgentValidator) ValidateProjections(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	if s == nil || e == nil {
		return nil
	}
	streamTurns := make([]scenario.StreamTurn, len(e.Turns))
	findings := validateTurns(s, e, func(path string, turn *scenario.Turn, index int) []scenario.Finding {
		var p PerplexityAgent
		if err := turn.DecodeProjection(e.Name, index, &p); err != nil {
			streamTurns[index] = scenario.StreamTurn{Path: path}
			return []scenario.Finding{decodeFinding(path, err)}
		}
		streamTurns[index] = scenario.StreamTurn{
			Path: path, Script: &p.Stream, Answer: p.Answer,
			ChunkCount: agentChunkCount(&p.Stream, p.Status),
		}
		out := s.ResolveRefs(path, &p)
		out = append(out, validateAgentProjection(path, &p)...)
		if p.Stream.Terminal != nil && p.Stream.Terminal.OmitDone {
			out = append(out, scenario.Finding{
				Severity: scenario.SeverityWarning,
				Code:     CodeStreamDoneIgnored,
				Path:     path + ".stream.terminal.omit_done",
				Message: "omit_done has no effect on the Agent API's typed SSE grammar, which has no " +
					"[DONE] sentinel to omit",
			})
		}
		return out
	})

	findings = append(findings, scenario.ValidateStreamScripts(streamTurns)...)
	var entryPolicy scenario.StreamPolicy
	if len(streamTurns) > 0 {
		entryPolicy = streamTurns[0].Script.EffectivePolicy() // nil-safe: StreamWarn when turn 0 has none
	}
	findings = append(findings, scenario.ValidateStreamFaultMismatch(e, entryPolicy, streamTurns)...)
	return findings
}

// agentChunkCount computes the scenario.StreamTurn.ChunkCount override
// GrammarTyped needs: renderAgentStream's own sequence is response.created,
// response.output_item.added, one response.output_text.delta per scripted
// delta, response.output_text.done, response.output_item.done and
// response.completed — five envelope events around the N deltas, not the
// one terminal chunk scenario's provider-neutral default (written against
// GrammarDelta) assumes. A turn with no script returns 0, scenario's own
// "no override" sentinel, so chunkCount's nil-Script fallback (1) still
// applies.
//
// status matters because renderAgentOutput omits the message output item
// entirely for a failed or cancelled turn, and renderAgentStream then has
// nothing to attach output_item.added/the N deltas/output_text.done/
// output_item.done to — it emits only response.created and
// response.completed, two chunks, regardless of how many deltas the turn
// still scripts. Using the five-envelope formula unconditionally for such a
// turn overstates its true chunk count, which understates nothing about
// after_chunk's UPPER bound (still checked against the smaller number) but
// makes the LOWER bound wrong: an after_chunk in [2, N+4] would load clean
// as "in range" while the stream that actually plays back has only 2 chunks
// to abort, so the fault never fires and the stream completes normally —
// exactly the silent no-op scenario.ValidateStreamFaultMismatch exists to
// catch. See TestAgentStreamAfterChunkOutOfRangeAtLoadFailedStatus.
func agentChunkCount(s *scenario.StreamScript, status AgentStatus) int {
	if s == nil {
		return 0
	}
	if status == StatusFailed || status == StatusCancelled {
		return 2
	}
	return len(s.Deltas) + 5
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

// validateSonarProjection checks one decoded Sonar projection at startup,
// everything EXCEPT the streaming grammar's own coherence — that is
// scenario.ValidateStreamScripts and scenario.ValidateStreamFaultMismatch,
// called once per entry by SonarValidator.ValidateProjections, because both
// need to see every turn together rather than one at a time.
func validateSonarProjection(path string, p *PerplexityProjection) []scenario.Finding {
	var findings []scenario.Finding
	add := func(code, at, message string) {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError, Code: code, Path: at, Message: message,
		})
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
