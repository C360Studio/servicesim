package mcp

// transport.go is the era-specific transport layer: Accept/Content-Type
// strictness, the four standard request-metadata headers, the Base64
// sentinel encoding, _meta validation and the legacy-traffic detection a
// dual-era follow-on unit would replace or extend. handler.go and
// jsonrpc.go stay era-neutral on purpose; this file is where "modern-only"
// actually lives.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/provider"
)

// Standard Streamable HTTP request-metadata headers this profile validates
// (contracts/mcp/README.md, "Request-metadata headers"). Spelled exactly
// as the specification spells them; net/http.Header.Get compares names
// case-insensitively, so the casing here does not have to match what a
// client actually sent.
const (
	headerProtocolVersion = "MCP-Protocol-Version"
	headerMethod          = "Mcp-Method"
	headerName            = "Mcp-Name"
	headerSessionID       = "Mcp-Session-Id"
	headerLastEventID     = "Last-Event-ID"
	headerParamPrefix     = "Mcp-Param-"
)

// Reserved _meta keys this profile reads.
const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaProgressToken      = "progressToken"
)

// The Base64 sentinel markers (contracts/mcp/README.md, "Value Encoding").
const (
	sentinelPrefix = "=?base64?"
	sentinelSuffix = "?="
)

// decodeSentinel decodes a header value that may carry the Base64 sentinel
// encoding, returning the value unchanged when it is not sentinel-wrapped.
// Only the OUTER markers are consumed; a decoded value that itself looks
// sentinel-shaped (contracts/mcp/README.md's fourth example) is returned
// as-is, never decoded a second time.
func decodeSentinel(value string) (string, error) {
	if !strings.HasPrefix(value, sentinelPrefix) || !strings.HasSuffix(value, sentinelSuffix) ||
		len(value) < len(sentinelPrefix)+len(sentinelSuffix) {
		return value, nil
	}
	inner := value[len(sentinelPrefix) : len(value)-len(sentinelSuffix)]
	decoded, err := base64.StdEncoding.DecodeString(inner)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// hasModernHeaders reports whether the two headers every modern request
// carries are both present, without validating them further. Used only to
// decide decision 12's "header check runs first" branch for method
// "initialize".
func hasModernHeaders(x *provider.Exchange) bool {
	return x.Request.Header.Get(headerProtocolVersion) != "" && x.Request.Header.Get(headerMethod) != ""
}

// acceptListsBoth reports whether the Accept header lists both
// application/json and text/event-stream, ignoring parameters and
// whitespace (decision 10).
func acceptListsBoth(accept string) bool {
	hasJSON, hasSSE := false, false
	for _, part := range strings.Split(accept, ",") {
		media, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		switch strings.ToLower(strings.TrimSpace(media)) {
		case "application/json":
			hasJSON = true
		case "text/event-stream":
			hasSSE = true
		}
	}
	return hasJSON && hasSSE
}

// isApplicationJSON reports whether ct's media type, ignoring parameters,
// is exactly application/json (decision 10 — narrower than
// httpx.IsJSONContentType's +json tolerance, because the specification
// names only this one media type).
func isApplicationJSON(ct string) bool {
	media, _, _ := strings.Cut(ct, ";")
	return strings.EqualFold(strings.TrimSpace(media), "application/json")
}

// parsedParams is params and its _meta object, each keyed by its raw JSON
// token so a caller can either decode a specific value or echo it back
// unmodified (decision 14 applies to progressToken as much as to id). A
// params that is absent, or present but not a JSON object, decodes to the
// zero value — indistinguishable from "no params at all", which is
// correct: both are "no _meta was supplied".
type parsedParams struct {
	raw  map[string]json.RawMessage
	meta map[string]json.RawMessage
}

// parseParams decodes env.Params (best-effort; a non-object Params yields
// the zero parsedParams, which every caller here treats as "nothing was
// supplied" rather than an error of its own).
func parseParams(env envelope) parsedParams {
	var pp parsedParams
	if len(env.Params) == 0 {
		return pp
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(env.Params, &top) != nil {
		return pp
	}
	pp.raw = top
	if metaRaw, ok := top[reservedMetaKey]; ok {
		var meta map[string]json.RawMessage
		if json.Unmarshal(metaRaw, &meta) == nil {
			pp.meta = meta
		}
	}
	return pp
}

// reservedMetaKey is the params property carrying _meta.
const reservedMetaKey = "_meta"

// paramString decodes raw[key] as a JSON string, reporting whether the key
// was present and was in fact a string.
func paramString(raw map[string]json.RawMessage, key string) (string, bool) {
	v, ok := raw[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return "", false
	}
	return s, true
}

// paramRaw returns the raw JSON token at raw[key], and whether it was
// present at all.
func paramRaw(raw map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	v, ok := raw[key]
	return v, ok
}

// checkTransport validates decision 10 (Accept/Content-Type), the standard
// header set, the Base64 sentinel comparisons, and decision 13's required
// _meta fields, in that order. It returns the exact response to send on
// the first violation; the caller returns it immediately. A zero Response
// with ok==false means every check passed.
//
// The ORDER checks run in is simulator-chosen (contracts/mcp/README.md:
// "The specification does not say in what order a server applies the
// Origin, header validation and authentication checks when more than one
// would fail" — this profile does not validate Origin at all, but the same
// silence covers the order among these). It is fixed and documented here
// rather than left to a Findings-priority table, because several of these
// violations carry a distinct JSON-RPC `data` payload (-32022) that a
// generic "first finding wins" renderer has nowhere to hang.
func checkTransport(x *provider.Exchange, env envelope, id json.RawMessage) (provider.Response, bool) {
	if !acceptListsBoth(x.Request.Header.Get("Accept")) {
		msg := "the Accept header must list both application/json and text/event-stream"
		x.Fail(CodeAcceptInvalid, "accept", "%s", msg)
		return statusResponse(errAt{http.StatusBadRequest, codeInvalidRequestError, msg, nil, id}, "mcp.error.400"), true
	}
	if !isApplicationJSON(x.Request.Header.Get("Content-Type")) {
		msg := "Content-Type must be application/json"
		x.Fail(CodeContentTypeInvalid, "content-type", "%s", msg)
		return statusResponse(errAt{http.StatusBadRequest, codeInvalidRequestError, msg, nil, id}, "mcp.error.400"), true
	}

	pp := parseParams(env)
	warnIgnoredParamHeaders(x)

	protoHeader := x.Request.Header.Get(headerProtocolVersion)
	methodHeader := x.Request.Header.Get(headerMethod)
	nameHeader := x.Request.Header.Get(headerName)

	var missing []string
	if protoHeader == "" {
		missing = append(missing, headerProtocolVersion)
	}
	if methodHeader == "" {
		missing = append(missing, headerMethod)
	}
	if env.Method == methodToolsCall && nameHeader == "" {
		missing = append(missing, headerName)
	}
	if len(missing) > 0 {
		msg := "required header(s) missing: " + strings.Join(missing, ", ")
		x.Fail(CodeHeaderRequired, "", "%s", msg)
		return statusResponse(errAt{http.StatusBadRequest, codeHeaderMismatchError, msg, nil, id}, "mcp.error.400"), true
	}

	if methodHeader != env.Method {
		msg := fmt.Sprintf("Header mismatch: Mcp-Method header value %q does not match body value %q",
			methodHeader, env.Method)
		x.Fail(CodeHeaderMismatch, "mcp-method", "%s", msg)
		return statusResponse(errAt{http.StatusBadRequest, codeHeaderMismatchError, msg, nil, id}, "mcp.error.400"), true
	}

	if env.Method == methodToolsCall {
		decodedName, err := decodeSentinel(nameHeader)
		if err != nil {
			msg := fmt.Sprintf("Header mismatch: Mcp-Name header value %q has invalid characters: %v", nameHeader, err)
			x.Fail(CodeHeaderInvalidChars, "mcp-name", "%s", msg)
			return statusResponse(errAt{http.StatusBadRequest, codeHeaderMismatchError, msg, nil, id}, "mcp.error.400"), true
		}
		if toolName, ok := paramString(pp.raw, "name"); ok && decodedName != toolName {
			msg := fmt.Sprintf("Header mismatch: Mcp-Name header value %q does not match body value %q",
				decodedName, toolName)
			x.Fail(CodeHeaderMismatch, "mcp-name", "%s", msg)
			return statusResponse(errAt{http.StatusBadRequest, codeHeaderMismatchError, msg, nil, id}, "mcp.error.400"), true
		}
	} else if nameHeader != "" {
		x.Warn(CodeHeaderNameUnexpected, "mcp-name", "Mcp-Name is not used by method %q; ignored", env.Method)
	}

	var missingMeta []string
	protoVersion, haveProto := metaString(pp, metaProtocolVersion)
	if !haveProto {
		missingMeta = append(missingMeta, metaProtocolVersion)
	}
	if _, haveCaps := paramRaw(pp.meta, metaClientCapabilities); !haveCaps {
		missingMeta = append(missingMeta, metaClientCapabilities)
	}
	if len(missingMeta) > 0 {
		msg := "missing required _meta field(s): " + strings.Join(missingMeta, ", ")
		x.Fail(CodeMetaRequired, "_meta", "%s", msg)
		return statusResponse(errAt{http.StatusBadRequest, codeInvalidParamsError, msg, nil, id}, "mcp.error.400"), true
	}
	if _, haveInfo := paramRaw(pp.meta, metaClientInfo); !haveInfo {
		x.Warn(CodeMetaClientInfoMissing, "_meta."+metaClientInfo,
			"clientInfo was not sent; the specification SHOULDs it")
	}

	if protoHeader != protoVersion {
		msg := fmt.Sprintf("Header mismatch: MCP-Protocol-Version header value %q does not match body value %q",
			protoHeader, protoVersion)
		x.Fail(CodeHeaderMismatch, "mcp-protocol-version", "%s", msg)
		return statusResponse(errAt{http.StatusBadRequest, codeHeaderMismatchError, msg, nil, id}, "mcp.error.400"), true
	}
	if !slices.Contains(supportedVersions, protoHeader) {
		x.Fail(CodeVersionUnsupported, "mcp-protocol-version", "unsupported protocol version %q", protoHeader)
		return statusResponse(errAt{
			Status: http.StatusBadRequest, Code: codeUnsupportedProtocolVersionError,
			Message: "Unsupported protocol version",
			Data:    map[string]any{"supported": supportedVersions, "requested": protoHeader},
			ID:      id,
		}, "mcp.error.400"), true
	}

	return provider.Response{}, false
}

// metaString decodes pp.meta[key] as a string, reporting whether it was
// present and was a string.
func metaString(pp parsedParams, key string) (string, bool) {
	v, ok := pp.meta[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return "", false
	}
	return s, true
}
