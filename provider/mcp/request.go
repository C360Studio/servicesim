package mcp

import (
	"net/http"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Finding codes this package raises. Every one is exported so a consumer's
// test can assert on it directly rather than on a status code the
// simulator could have produced for another reason (house rule 5).
const (
	// CodeParseFailed reports a body this profile could not read as a
	// single JSON value at all: unparseable JSON, an empty body, or one
	// that exceeded the size limit before it could be read in full.
	CodeParseFailed = "mcp.request.parse_error"

	// CodeInvalidRequest reports a body that parsed as JSON but is not a
	// well-formed JSON-RPC request or notification.
	CodeInvalidRequest = "mcp.request.invalid"

	// CodeResponseNotAccepted reports a body carrying "result" or
	// "error" — a JSON-RPC *response*, which a client MUST NOT send.
	CodeResponseNotAccepted = "mcp.request.is_response"

	// CodeAcceptInvalid reports a missing or incomplete Accept header.
	CodeAcceptInvalid = "mcp.request.accept_invalid"

	// CodeContentTypeInvalid reports a request Content-Type other than
	// application/json.
	CodeContentTypeInvalid = "mcp.request.content_type_invalid"

	// CodeHeaderRequired reports a required standard header
	// (MCP-Protocol-Version, Mcp-Method, Mcp-Name) that is missing.
	CodeHeaderRequired = "mcp.header.required"

	// CodeHeaderMismatch reports a header value that disagrees with the
	// corresponding request-body value.
	CodeHeaderMismatch = "mcp.header.mismatch"

	// CodeHeaderInvalidChars reports a Base64-sentinel header value that
	// does not decode.
	CodeHeaderInvalidChars = "mcp.header.invalid_characters"

	// CodeVersionUnsupported reports a protocol version — header and body
	// agree on it — that is not supportedVersions[0].
	CodeVersionUnsupported = "mcp.version.unsupported"

	// CodeMetaRequired reports a request missing one or both required
	// _meta fields.
	CodeMetaRequired = "mcp.meta.required"

	// CodeMetaClientInfoMissing is a WARNING for a request whose _meta
	// carries no clientInfo — the spec SHOULDs it, not MUSTs it.
	CodeMetaClientInfoMissing = "mcp.meta.client_info_missing"

	// CodeHeaderNameUnexpected is a WARNING for an Mcp-Name header on a
	// method that does not use one.
	CodeHeaderNameUnexpected = "mcp.header.name_unexpected"

	// CodeHeaderParamIgnored is a WARNING for any Mcp-Param-* header,
	// which this unit never honours (decision 4).
	CodeHeaderParamIgnored = "mcp.header.param_ignored"

	// CodeLegacySessionID is a WARNING for a present Mcp-Session-Id
	// header, ignored under the modern-only profile.
	CodeLegacySessionID = "mcp.legacy.session_id"

	// CodeLegacyLastEventID is a WARNING for a present Last-Event-ID
	// header, ignored under the modern-only profile.
	CodeLegacyLastEventID = "mcp.legacy.last_event_id"

	// CodeLegacyInitialize is a WARNING for a legacy-shaped "initialize"
	// request reaching a modern-only server.
	CodeLegacyInitialize = "mcp.legacy.initialize"

	// CodeMethodUnknown reports a method that is not server/discover,
	// tools/list or tools/call.
	CodeMethodUnknown = "mcp.method.unknown"

	// CodeAuthMissing reports that no accepted credential placement
	// carried a value while the scenario requires one.
	CodeAuthMissing = "mcp.auth.missing"

	// CodeAuthMismatch reports a credential that does not match the
	// scenario's expected key, or any credential at all under auth mode
	// "reject".
	CodeAuthMismatch = "mcp.auth.mismatch"

	// CodeAuthWrongPlacement reports an Authorization header carrying a
	// scheme other than the documented Bearer.
	CodeAuthWrongPlacement = "mcp.auth.wrong_placement"

	// CodeCursorInvalid reports a tools/list request naming a cursor —
	// this profile always serves a single page and has no cursor to
	// resume from.
	CodeCursorInvalid = "mcp.cursor.invalid"

	// CodeNameRequired reports a tools/call request with no params.name.
	CodeNameRequired = "mcp.name.required"

	// CodeArgumentsInvalid reports a tools/call request whose arguments
	// is present but not a JSON object.
	CodeArgumentsInvalid = "mcp.arguments.invalid"

	// CodeToolUnknown is a WARNING for a tools/call naming a tool absent
	// from both the projection's tools list and its results map.
	CodeToolUnknown = "mcp.tool.unknown"

	// CodeToolUnscripted is a WARNING for a tools/call naming a declared
	// tool with no scripted result.
	CodeToolUnscripted = "mcp.tool.unscripted"

	// CodeProjectionInvalid reports a scenario projection body this
	// package cannot decode.
	CodeProjectionInvalid = "mcp.projection.invalid"

	// CodeProjectionUnresolved reports a projection whose source
	// reference names no declared source, seen at request time rather
	// than at startup.
	CodeProjectionUnresolved = "mcp.projection.unresolved"

	// CodeRenderFailed reports a response this profile could not render
	// after deciding to serve it.
	CodeRenderFailed = "mcp.render.failed"

	// Load-time-only codes, raised by Validator.

	// CodeToolNameInvalid is a WARNING for a tool name outside
	// ^[A-Za-z0-9_.-]{1,128}$ (SHOULD in the spec).
	CodeToolNameInvalid = "mcp.tool.name_invalid"

	// CodeToolNameDuplicate is an ERROR for two tools of one entry
	// sharing a name.
	CodeToolNameDuplicate = "mcp.tool.name_duplicate"

	// CodeToolInputSchemaInvalid is an ERROR for a tool whose
	// input_schema is not a JSON object with type: "object" at the root.
	CodeToolInputSchemaInvalid = "mcp.tool.input_schema_invalid"

	// CodeToolXMcpHeaderUnsupported is an ERROR for a tool's input_schema
	// containing an "x-mcp-header" key anywhere (decision 4).
	CodeToolXMcpHeaderUnsupported = "mcp.tool.x_mcp_header_unsupported"

	// CodeToolOutputSchemaUnchecked is a WARNING, once per tool, for a
	// tool that declares output_schema whose scripted result also
	// carries structured_content: this profile does not validate the
	// instance against the schema.
	CodeToolOutputSchemaUnchecked = "mcp.tool.output_schema_unchecked"

	// CodeStreamRejectMeaningless is an ERROR for stream.when_requested:
	// reject — the client has no field that asks this profile to stream,
	// so nothing is ever rejected on that basis.
	CodeStreamRejectMeaningless = "mcp.stream.reject_meaningless"

	// CodeSourceUnknown reports a content block's source: reference
	// naming no declared scenario source.
	CodeSourceUnknown = "mcp.source.unknown"

	// CodeContentTypeUnknown is an ERROR, at load, for a content block
	// whose type is not one of the five the schema defines.
	CodeContentTypeUnknown = "mcp.content.type_unknown"
)

// defaultPlacements is the credential placement this profile accepts when
// a scenario declares no auth.headers of its own: the Authorization
// header, the only form the specification documents (decision 3).
var defaultPlacements = []string{provider.PlacementAuthorization}

// acceptedPlacements resolves which credential placements authenticate,
// applying the shared precedence (scenario auth.headers, then the route's
// own Credentials, then this package's default).
func acceptedPlacements(x *provider.Exchange, policy scenario.AuthPolicy) []string {
	return x.AcceptedPlacements(policy, defaultPlacements)
}

// presentedCredentials returns every accepted placement that carried a
// value, and records a WARNING for a placement that was seen but is not
// accepted by this route's policy — there is only one recognised
// placement (Authorization), so today that can only be an
// AcceptedPlacements narrowed by a scenario's own auth.headers.
func presentedCredentials(x *provider.Exchange, accepted []string) []provider.Credential {
	var presented []provider.Credential
	for _, cred := range x.Credentials() {
		if slices.Contains(accepted, cred.Header) {
			presented = append(presented, cred)
		}
	}
	return presented
}

// checkAuth applies decision 3: optional by default — x.AuthPolicy() reads
// Profile.DefaultAuth, which this profile sets to scenario.AuthOptional,
// deliberately the opposite default from the three research profiles,
// because the specification leaves authentication to the deployment and
// this profile must not invent a requirement the specification does not
// make — PlacementAuthorization the only accepted placement, a wrong scheme
// WARNs but still authenticates (matching the other profiles' "seen and
// journaled, not silently rejected" treatment of an off-placement
// credential).
func checkAuth(x *provider.Exchange) {
	policy := x.AuthPolicy()
	accepted := acceptedPlacements(x, policy)
	presented := presentedCredentials(x, accepted)

	if policy.Mode == scenario.AuthReject {
		x.Fail(CodeAuthMismatch, "authorization", "the scenario rejects every credential")
		return
	}
	if len(presented) == 0 {
		if policy.Mode == scenario.AuthRequired {
			x.Fail(CodeAuthMissing, "authorization", "authorization is required")
		}
		return
	}
	for _, cred := range presented {
		if cred.Header == provider.PlacementAuthorization && cred.Scheme != "Bearer" {
			x.Warn(CodeAuthWrongPlacement, "authorization",
				"Authorization does not carry the documented Bearer scheme")
		}
	}
	if policy.ExpectKey != "" && !slices.ContainsFunc(presented, func(c provider.Credential) bool {
		return c.Value == policy.ExpectKey
	}) {
		x.Fail(CodeAuthMismatch, "authorization", "the presented credential is not the expected key")
	}
}

// unauthorizedResponse builds decision 3's fixed 401 shape: no id, code
// -32600, message exactly "authorization required", no WWW-Authenticate.
func unauthorizedResponse() provider.Response {
	return statusResponse(errAt{
		Status:  http.StatusUnauthorized,
		Code:    CodeInvalidRequestError,
		Message: "authorization required",
		ID:      nullID,
	}, "mcp.error.401")
}

// checkLegacyHeaders records decision 12's two unconditional observations.
// They apply to every request and every notification alike: they are
// observations, not gates, so nothing about the outcome depends on them.
func checkLegacyHeaders(x *provider.Exchange) {
	if x.Request.Header.Get(headerSessionID) != "" {
		x.Warn(CodeLegacySessionID, "", "Mcp-Session-Id is ignored; this profile never mints or echoes a session")
	}
	if x.Request.Header.Get(headerLastEventID) != "" {
		x.Warn(CodeLegacyLastEventID, "", "Last-Event-ID is ignored; streams are not resumable")
	}
}

// headerHasParamPrefix reports whether name is one of the Mcp-Param-*
// custom headers decision 4 says this unit never honours.
func headerHasParamPrefix(name string) bool {
	return len(name) > len(headerParamPrefix) &&
		strings.EqualFold(name[:len(headerParamPrefix)], headerParamPrefix)
}

// warnIgnoredParamHeaders records one WARNING per distinct Mcp-Param-*
// header present on the request.
func warnIgnoredParamHeaders(x *provider.Exchange) {
	for name := range x.Request.Header {
		if headerHasParamPrefix(name) {
			x.Warn(CodeHeaderParamIgnored, name, "%s is ignored; x-mcp-header is not honoured in this build", name)
		}
	}
}
