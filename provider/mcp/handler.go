package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Name is the provider's key in a scenario's providers registry, the kind
// a handler is registered under, and the listener's identity. It was
// already exported and untyped before this unit (framework-seam.md:
// "tavily.Name and mcp.Name exist (untyped today; they become typed)");
// typed here as provider.Name.
const Name provider.Name = "mcp"

// FaultKeyMCP is the attempt budget POST /mcp draws on. There is one route
// on this listener, so there is one key.
const FaultKeyMCP = "mcp:mcp"

// PatternMCP is the ServeMux pattern for POST /mcp — the specification's
// own example path (decision 2).
const PatternMCP = "POST /mcp"

// mcpFault selects the fault plan out of a scenario.
func mcpFault(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, string(Name)) }

// RouteMCP returns POST /mcp, keyed "mcp:mcp".
func RouteMCP() provider.Route {
	return provider.Route{
		Pattern:     PatternMCP,
		FaultKey:    FaultKeyMCP,
		Credentials: defaultPlacements,
		Fault:       mcpFault,
	}
}

// Routes returns the routes this provider serves, in registration order.
// It is a function, not a package-level var, so no consumer can mutate the
// route table of a package it merely imported.
func Routes() []provider.Route {
	return []provider.Route{RouteMCP()}
}

// handlers maps every route this profile serves to its handler, shared by
// Profile() and the deprecated New.
func handlers() map[string]provider.Handler {
	return map[string]provider.Handler{
		PatternMCP: handleMCP,
	}
}

// New returns the MCP handler, built with provider.NewMux over Routes().
//
// The zero Deps is usable: mcp.New(provider.Deps{}) serves a well-shaped
// empty server/discover, an empty tools/list, and an "unknown tool" for
// any tools/call — no scenario, no journal, no faults, a real clock. A
// zero Deps means no faults even if the Scenario declares them; pass
// testkit.NewFaults(s) as Deps.Faults, or use testkit.Start, to get the
// scenario's declared faults.
//
// Deprecated: use Profile().Handler(deps). New is removed once
// internal/server and testkit are rewired onto provider.Set (Phase 10 units
// 3-4).
func New(deps provider.Deps) http.Handler {
	return Profile().Handler(deps)
}

// handleMCP dispatches every POST /mcp request. The order is fixed
// (deliverable A's own words, and decisions 6/11/12/13 in sequence): parse
// the envelope → notification short-circuit → the legacy "initialize"
// header-first probe → transport/_meta validation → auth → if the
// request failed any of those, respond and consume no attempt → the
// method-shape and per-method request-side checks (unknown method,
// tools/list's cursor, tools/call's name/arguments — none of these needs
// the scripted projection) → selectProjection (claims the attempt) →
// projection-dependent dispatch → render.
//
// The method-shape and request-side checks run BEFORE selectProjection on
// purpose: SelectTurnFor's own contract (provider/turn.go) is "call it
// only once the request has passed validation: it claims an attempt, and
// a rejected request must not consume one." An invalid cursor, a missing
// tools/call name or a request naming a method this profile does not
// implement are all validation rejections exactly like a missing header —
// they must never spend a scripted fault's attempt budget, and (because
// Exchange.Fault is memoised on first call) claiming the attempt even a
// moment before one of these checks runs would apply that budget's status
// to a response the request never earned.
func handleMCP(x *provider.Exchange) provider.Response {
	entry := x.Entry()
	checkLegacyHeaders(x)

	env, id, parseResp, ok := parseEnvelope(x)
	if !ok {
		return parseResp
	}
	if env.isNotification() {
		return provider.Response{Status: http.StatusAccepted, Label: "mcp.notification.accepted"}
	}

	if env.Method == methodInitialize && !hasModernHeaders(x) {
		x.Warn(CodeLegacyInitialize, "", "a legacy client sent %q without the modern per-request headers", methodInitialize)
		msg := fmt.Sprintf("Header mismatch: this server speaks protocol version(s) %v only; required headers were not sent",
			supportedVersions)
		x.Fail(CodeHeaderRequired, "", "%s", msg)
		return statusResponse(errAt{
			Status: http.StatusBadRequest, Code: CodeHeaderMismatchError, Message: msg, ID: id,
		}, "mcp.error.400")
	}

	if resp, failed := checkTransport(x, env, id); failed {
		return resp
	}

	checkAuth(x)
	if x.Failed() {
		return unauthorizedResponse()
	}

	pp := parseParams(env)

	var toolName string
	switch env.Method {
	case MethodDiscover:
		// No request-side params beyond the standard _meta, already checked.
	case MethodToolsList:
		if resp, failed := checkToolsListParams(x, id, pp); failed {
			return resp
		}
	case MethodToolsCall:
		name, resp, failed := checkToolsCallParams(x, id, pp)
		if failed {
			return resp
		}
		toolName = name
	default:
		x.Fail(CodeMethodUnknown, "method", "unknown method %q", env.Method)
		return methodNotFoundResponse(id, env.Method)
	}

	projection, ok := selectProjection(x, entry)
	if !ok {
		return internalErrorResponse(id, fmt.Errorf("no scripted turn matches this request"))
	}

	switch env.Method {
	case MethodDiscover:
		return handleDiscover(id, projection)
	case MethodToolsList:
		return handleToolsList(id, projection)
	case MethodToolsCall:
		return handleToolsCall(x, id, pp, toolName, projection)
	default:
		// Unreachable: env.Method was already checked against the three
		// known methods above, and the default case there returns.
		return methodNotFoundResponse(id, env.Method)
	}
}

// parseEnvelope validates the request body is a single well-formed
// JSON-RPC request or notification, per decision 6's shape rules. It
// reads the shared lifecycle's own findings (body too large, malformed,
// not an object) rather than re-reading x.Raw, so this profile's shape
// findings can never disagree with what Handle already decided about the
// same body.
func parseEnvelope(x *provider.Exchange) (envelope, json.RawMessage, provider.Response, bool) {
	switch {
	case x.HasFinding(provider.CodeBodyTooLarge):
		x.Fail(CodeParseFailed, "", "request body could not be read in full")
		return envelope{}, nullID, parseErrorResponse(), false
	case x.HasFinding(provider.CodeMalformedJSON):
		x.Fail(CodeParseFailed, "", "request body is not valid JSON")
		return envelope{}, nullID, parseErrorResponse(), false
	case x.HasFinding(provider.CodeBodyNotObject):
		x.Fail(CodeInvalidRequest, "", "request body is not a JSON-RPC request or notification")
		return envelope{}, nullID, invalidRequestResponse("request body is not a JSON-RPC request or notification"), false
	}
	if len(bytes.TrimSpace(x.Raw)) == 0 {
		x.Fail(CodeParseFailed, "", "request body is empty")
		return envelope{}, nullID, parseErrorResponse(), false
	}

	env, err := decodeEnvelope(x.Raw)
	if err != nil {
		// Unreachable through provider.Handle, which already confirmed the
		// body is a JSON object before this runs; kept as a defended
		// fallback for a caller that built an Exchange by hand.
		x.Fail(CodeParseFailed, "", "request body is not valid JSON")
		return envelope{}, nullID, parseErrorResponse(), false
	}

	if env.HasResult || env.HasError {
		x.Fail(CodeResponseNotAccepted, "", "a client must not send a JSON-RPC response")
		return env, nullID, invalidRequestResponse("a client must not send a JSON-RPC response"), false
	}
	if !env.isWellFormed() {
		x.Fail(CodeInvalidRequest, "", "request body is not a well-formed JSON-RPC request or notification")
		return env, nullID, invalidRequestResponse("request body is not a JSON-RPC request or notification"), false
	}
	return env, env.ID, provider.Response{}, true
}

// parseErrorResponse renders decision 6's -32700 shape.
func parseErrorResponse() provider.Response {
	return statusResponse(errAt{
		Status: http.StatusBadRequest, Code: CodeParseError, Message: "Parse error: invalid JSON", ID: nullID,
	}, "mcp.error.parse")
}

// invalidRequestResponse renders decision 6's -32600 shape for a
// request-shape failure.
func invalidRequestResponse(message string) provider.Response {
	return statusResponse(errAt{
		Status: http.StatusBadRequest, Code: CodeInvalidRequestError, Message: message, ID: nullID,
	}, "mcp.error.invalid_request")
}

// selectProjection picks the turn serving this request and decodes its
// projection body, matching the sibling providers' zero-configuration
// rule: a scenario with no "mcp" block at all renders a well-shaped empty
// server (empty discover, empty tools/list, "unknown tool" for any call);
// a block that declares turns but none matches this request is an error.
func selectProjection(x *provider.Exchange, entry *scenario.ProviderEntry) (*Projection, bool) {
	if entry == nil {
		return &Projection{}, true
	}
	turn, index := provider.SelectTurnFor(x, entry)
	if turn == nil {
		return nil, false
	}
	p := &Projection{}
	if err := turn.DecodeProjection(entry.Name, index, p); err != nil {
		// Unreachable through internal/server, which validates every
		// projection before readiness. Reachable by a caller that built a
		// Scenario by hand.
		x.Fail(CodeProjectionInvalid, "", "%v", err)
		return nil, false
	}
	for _, finding := range resolveContentSources(x.Deps.Scenario, projectionPath(entry, index), p) {
		x.Warn(CodeProjectionUnresolved, finding.Path, "%s", finding.Message)
	}
	return p, true
}

// projectionPath addresses a turn's respond body the way a scenario
// finding does.
func projectionPath(entry *scenario.ProviderEntry, index int) string {
	return fmt.Sprintf("providers.%s.turns[%d].respond", entry.Name, index)
}

// -----------------------------------------------------------------------
// Startup validation
// -----------------------------------------------------------------------

// Validator decodes and checks this package's projection bodies at
// startup. internal/server registers it under [Name] and calls
// provider.ValidateScenario after scenario.Validate and before readiness
// reports true, so a fixture with a bad MCP field fails at boot rather
// than on a consumer's first request.
type Validator struct{}

// Routes implements provider.RouteLister, so a `when.route:` in an "mcp"
// entry is checked against the route this package actually serves.
func (Validator) Routes() []provider.Route { return Routes() }

// Compile-time proof that the seam is implemented.
var _ provider.Validator = Validator{}

// toolNamePattern is the specification's SHOULD for tool names (decision
// 8): 1 to 128 characters of A-Z a-z 0-9 _ - .
var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// ValidateProjections decodes every turn's projection body and reports
// what it finds, addressed by the entry's YAML path. It does not mutate
// the scenario beyond resolving each turn's own content source
// references, and it is safe to call more than once.
func (Validator) ValidateProjections(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	if e == nil {
		return nil
	}
	var findings []scenario.Finding
	streamTurns := make([]scenario.StreamTurn, len(e.Turns))

	for i := range e.Turns {
		path := projectionPath(e, i)
		p := &Projection{}
		if err := e.Turns[i].DecodeProjection(e.Name, i, p); err != nil {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError, Code: CodeProjectionInvalid, Path: path, Message: err.Error(),
			})
			streamTurns[i] = scenario.StreamTurn{Path: path}
			continue
		}
		findings = append(findings, resolveContentSources(s, path, p)...)
		findings = append(findings, checkProjection(path, p)...)
		streamTurns[i] = scenario.StreamTurn{Path: path, Script: &p.Stream}
	}

	findings = append(findings, scenario.ValidateStreamScripts(streamTurns)...)

	var entryPolicy scenario.StreamPolicy
	if len(streamTurns) > 0 {
		entryPolicy = streamTurns[0].Script.EffectivePolicy()
	}
	if entryPolicy == scenario.StreamReject {
		// Decision 5: the client has no field of its own that asks this
		// profile to stream, so "reject" has nothing to reject.
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError,
			Code:     CodeStreamRejectMeaningless,
			Path:     streamTurns[0].Path + ".stream.when_requested",
			Message: "stream.when_requested: reject is meaningless for mcp: the client has no field that " +
				"asks to stream, so a scripted rejection could never be reached by any request",
		})
	}
	findings = append(findings, scenario.ValidateStreamFaultMismatch(e, entryPolicy, streamTurns)...)
	return findings
}

// checkProjection reports the projection values that would render an
// implausible or invalid response: decision 8's tool-name and
// input_schema rules, decision 4's x-mcp-header rejection, and the
// output_schema/structured_content pairing that this profile does not
// validate against the schema. Findings come back in declaration order,
// never Go map order.
func checkProjection(path string, p *Projection) []scenario.Finding {
	var findings []scenario.Finding
	seen := make(map[string]bool, len(p.Tools))

	for i, t := range p.Tools {
		at := fmt.Sprintf("%s.tools[%d]", path, i)
		switch {
		case seen[t.Name]:
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError, Code: CodeToolNameDuplicate, Path: at + ".name",
				Message: fmt.Sprintf("duplicate tool name %q", t.Name),
			})
		case !toolNamePattern.MatchString(t.Name):
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityWarning, Code: CodeToolNameInvalid, Path: at + ".name",
				Message: fmt.Sprintf("tool name %q does not match ^[A-Za-z0-9_.-]{1,128}$ (SHOULD)", t.Name),
			})
		}
		seen[t.Name] = true

		if !isObjectSchema(t.InputSchema) {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError, Code: CodeToolInputSchemaInvalid, Path: at + ".input_schema",
				Message: "input_schema must be a JSON object with type: \"object\" at the root",
			})
		}
		if containsXMcpHeader(t.InputSchema) {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError, Code: CodeToolXMcpHeaderUnsupported, Path: at + ".input_schema",
				Message: "x-mcp-header is not honoured in this build; remove it from the fixture",
			})
		}
		if t.OutputSchema != nil {
			if rp, ok := p.Results[t.Name]; ok && rp.StructuredContent != nil {
				findings = append(findings, scenario.Finding{
					Severity: scenario.SeverityWarning, Code: CodeToolOutputSchemaUnchecked,
					Path: fmt.Sprintf("%s.results.%s.structured_content", path, t.Name),
					Message: fmt.Sprintf(
						"tool %q declares output_schema; its structured_content is not validated against it "+
							"(no JSON Schema validator in stdlib)", t.Name),
				})
			}
		}
	}

	for _, name := range resultKeys(p.Results) {
		rp := p.Results[name]
		for i, cb := range rp.Content {
			if !slices.Contains(contentTypes, cb.Type) {
				findings = append(findings, scenario.Finding{
					Severity: scenario.SeverityError, Code: CodeContentTypeUnknown,
					Path:    fmt.Sprintf("%s.results.%s.content[%d].type", path, name, i),
					Message: fmt.Sprintf("content type %q is not one of %v", cb.Type, contentTypes),
				})
			}
		}
	}
	return findings
}

// isObjectSchema reports whether schema is a well-formed inputSchema root:
// absent (renderTool substitutes the no-parameter default) or an object
// whose own "type" is "object".
func isObjectSchema(schema map[string]any) bool {
	if schema == nil {
		return true
	}
	t, ok := schema["type"]
	return ok && t == "object"
}

// containsXMcpHeader reports whether v's JSON tree contains an
// "x-mcp-header" key anywhere (decision 4).
func containsXMcpHeader(v any) bool {
	switch val := v.(type) {
	case map[string]any:
		for k, vv := range val {
			if k == "x-mcp-header" {
				return true
			}
			if containsXMcpHeader(vv) {
				return true
			}
		}
	case []any:
		for _, vv := range val {
			if containsXMcpHeader(vv) {
				return true
			}
		}
	}
	return false
}
