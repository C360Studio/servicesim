package mcp

import (
	"encoding/json"
	"net/http"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// checkToolsListParams validates tools/list's request-side params before
// any attempt is claimed (decision 8's single-page rule): a request
// naming a cursor at all, of any value, is -32602, since this profile
// never has a follow-up page to hand out. It needs nothing from the
// scripted projection, so it runs ahead of selectProjection — see
// handleMCP's own doc comment for why that order matters.
func checkToolsListParams(x *provider.Exchange, id json.RawMessage, pp parsedParams) (provider.Response, bool) {
	if _, hasCursor := paramRaw(pp.raw, "cursor"); hasCursor {
		x.Fail(CodeCursorInvalid, "params.cursor", "this profile always serves a single page; cursor is not supported")
		return statusResponse(errAt{
			Status: http.StatusOK, Code: CodeInvalidParamsError, Message: "Invalid cursor", ID: id,
		}, "mcp.tools_list.error.invalid_cursor"), true
	}
	return provider.Response{}, false
}

// handleToolsList serves tools/list once checkToolsListParams has already
// passed: nothing further about the request needs checking, only the
// scripted projection needs rendering.
func handleToolsList(id json.RawMessage, p *Projection) provider.Response {
	body, err := renderToolsList(p)
	if err != nil {
		return internalErrorResponse(id, err)
	}
	return methodResult(id, body, "mcp.tools_list.ok")
}

// checkToolsCallParams validates params.name and params.arguments' shape
// before any attempt is claimed, returning the validated name for the
// caller to pass on to handleToolsCall. Like checkToolsListParams, it
// needs nothing from the scripted projection — the projection-dependent
// unknown-tool / unscripted-tool checks live in handleToolsCall, after
// selectProjection has legitimately claimed the attempt.
func checkToolsCallParams(x *provider.Exchange, id json.RawMessage, pp parsedParams) (string, provider.Response, bool) {
	name, ok := paramString(pp.raw, "name")
	if !ok || name == "" {
		x.Fail(CodeNameRequired, "params.name", "name is required")
		return "", statusResponse(errAt{
			Status: http.StatusOK, Code: CodeInvalidParamsError, Message: "Invalid params: name is required", ID: id,
		}, "mcp.tools_call.error.name_required"), true
	}
	if argsRaw, hasArgs := paramRaw(pp.raw, "arguments"); hasArgs {
		var obj map[string]any
		if json.Unmarshal(argsRaw, &obj) != nil {
			x.Fail(CodeArgumentsInvalid, "params.arguments", "arguments must be a JSON object when present")
			return "", statusResponse(errAt{
				Status: http.StatusOK, Code: CodeInvalidParamsError,
				Message: "Invalid params: arguments must be an object", ID: id,
			}, "mcp.tools_call.error.arguments_invalid"), true
		}
	}
	return name, provider.Response{}, false
}

// handleToolsCall serves tools/call once checkToolsCallParams has already
// validated the request shape and the attempt has been claimed: it
// resolves the scripted result (or the unknown-tool / unscripted-tool
// fallback, decision 8's "results: may name a tool absent from tools"
// rule), renders it, and — when the entry's stream policy is "stream" —
// additionally renders the SSE progress stream decision 5 describes.
// resp.Body is always the rendered JSON object, streamed or not.
//
// The unknown-tool response is FaultEligible, unlike the request-shape
// errors above: by the time this runs the request has already passed
// every check and selectProjection has already claimed the attempt for
// it, so "the tool this well-formed request named is not in the
// catalogue" is an ordinary dispatch outcome, not a rejection — exactly
// like a rendered success, it must be able to carry a scripted fault
// through the fixed -32603 envelope rather than leave this response's own
// body under an overridden status (deliverable A: "dispatch → render →
// Response{... FaultEligible: true ...}").
func handleToolsCall(x *provider.Exchange, id json.RawMessage, pp parsedParams, name string, p *Projection) provider.Response {
	rp, ok := resolveResult(x, p, name)
	if !ok {
		x.Warn(CodeToolUnknown, "params.name", "unknown tool %q", name)
		resp := statusResponse(errAt{
			Status: http.StatusOK, Code: CodeInvalidParamsError, Message: "Unknown tool: " + name, ID: id,
		}, "mcp.tools_call.error.unknown_tool")
		resp.FaultEligible = true
		resp.FaultBody = faultBody(id)
		return resp
	}

	body, err := renderCallResult(rp, p.ExtraFields)
	if err != nil {
		return internalErrorResponse(id, err)
	}
	resp := methodResult(id, body, "mcp.tools_call.ok")

	if wantsStream(x.Entry()) {
		stream, err := renderProgressStream(pp, p, resp.Body)
		if err != nil {
			return internalErrorResponse(id, err)
		}
		resp.Stream = stream
		resp.Label = "mcp.tools_call.stream"
		resp.Header = streamHeader()
	}
	return resp
}

// resolveResult applies decision 8's three-way rule: a tool declared AND
// scripted renders normally; a tool declared but not scripted renders the
// visible "no scripted result" placeholder with a WARNING; a name in
// neither is not found at all (ok == false).
func resolveResult(x *provider.Exchange, p *Projection, name string) (*ResultProjection, bool) {
	if rp, scripted := p.Results[name]; scripted {
		return &rp, true
	}
	if !hasDeclaredTool(p, name) {
		return nil, false
	}
	x.Warn(CodeToolUnscripted, "results."+name, "no scripted result for tool %q", name)
	return &ResultProjection{
		Content: []ContentBlock{{Type: "text", Text: "servicesim: no scripted result for tool " + name}},
		IsError: true,
	}, true
}

// hasDeclaredTool reports whether name appears in p.Tools.
func hasDeclaredTool(p *Projection, name string) bool {
	for _, t := range p.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// wantsStream reports whether the ENTRY's stream policy asks this profile
// to answer tools/call as SSE. It is a property of the entry — read from
// turn 0, the same rule provider/perplexity's streamPolicy applies and the
// rule decision 5, contracts/mcp/README.md and scenario.StreamScript's own
// doc all record: "Policy is read from turn 0 only … a policy on a later
// turn raises CodeStreamPolicyIgnored". Reading the SELECTED turn's policy
// instead (an earlier draft did) let a later turn's script switch the
// content type on its own, which the load-time WARNING had just told the
// author it would not. The selected turn's script still supplies the
// deltas and paces (renderProgressStream reads p.Stream); only the
// JSON-vs-SSE decision comes from turn 0. It is a property of the SCRIPT,
// not of the request: the client has no field of its own that asks to
// stream (decision 5), so — unlike every other provider package's
// wantsStream — there is no request-side signal to combine this with.
func wantsStream(entry *scenario.ProviderEntry) bool {
	return streamPolicy(entry) == scenario.StreamServe
}

// streamPolicy is the entry-level policy: turn 0's effective policy,
// StreamWarn (the permissive default) when there is no entry or no turn.
// An undecodable turn 0 also yields the default — unreachable through
// internal/server, which validates every projection before readiness, and
// reported on its own path when a caller built the scenario by hand.
func streamPolicy(entry *scenario.ProviderEntry) scenario.StreamPolicy {
	if entry == nil || len(entry.Turns) == 0 {
		return scenario.StreamWarn
	}
	var p Projection
	if err := entry.Turns[0].DecodeProjection(entry.Name, 0, &p); err != nil {
		return scenario.StreamWarn
	}
	return p.Stream.EffectivePolicy()
}
