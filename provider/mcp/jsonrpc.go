package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// JSON-RPC 2.0 error codes this profile emits, spelled the way
// contracts/mcp/README.md's schema.json table names them.
const (
	// CodeParseError is -32700, "ParseError": the request body could not
	// be parsed as JSON, or was empty, or exceeded the size limit and so
	// could never be read in full (decision 6).
	CodeParseError = -32700

	// CodeInvalidRequestError is -32600, "InvalidRequestError": the body
	// parsed as JSON but is not a JSON-RPC request or notification —
	// wrong or missing jsonrpc, missing or non-string method, an id that
	// is explicitly null, a top-level array or scalar, or a body carrying
	// "result"/"error" (a response, which a client MUST NOT send). Also
	// the 405 and unrecognised-route bodies.
	CodeInvalidRequestError = -32600

	// CodeMethodNotFoundError is -32601, "MethodNotFoundError": the
	// method is not server/discover, tools/list or tools/call.
	CodeMethodNotFoundError = -32601

	// CodeInvalidParamsError is -32602, "InvalidParamsError": a missing
	// required _meta field, an unknown tool, an invalid cursor, or
	// malformed tools/call arguments.
	CodeInvalidParamsError = -32602

	// CodeInternalError is -32603, "InternalError": the profile could not
	// render a response it had already decided to serve, and every
	// scripted-fault error body (decision 6).
	CodeInternalError = -32603

	// CodeHeaderMismatchError is -32020, "HeaderMismatchError": a
	// required standard header is missing, a header disagrees with the
	// corresponding body value, or a Base64-sentinel header value does
	// not decode.
	CodeHeaderMismatchError = -32020

	// CodeMissingRequiredClientCapabilityError is -32021,
	// "MissingRequiredClientCapabilityError". Named for completeness —
	// the contract records it as not reachable in this unit, since
	// nothing this profile implements is gated behind a client
	// capability.
	CodeMissingRequiredClientCapabilityError = -32021

	// CodeUnsupportedProtocolVersionError is -32022,
	// "UnsupportedProtocolVersionError": the request's protocol version,
	// once the header and body agree on what it is, is not
	// supportedVersions[0].
	CodeUnsupportedProtocolVersionError = -32022
)

// resultType is the constant value every result carries (decision 14).
const resultType = "complete"

// ResultType is the JSON-RPC result envelope every method's success path
// shares. Every provider-specific result type below embeds it, so that
// resultType and _meta are spelled once rather than once per method.
type ResultType struct {
	ResultTypeField string  `json:"resultType"`
	Meta            *result `json:"_meta,omitempty"`
}

// result is the reserved _meta shape every result stamps
// (io.modelcontextprotocol/serverInfo, decision 9).
type result struct {
	ServerInfo implementation `json:"io.modelcontextprotocol/serverInfo"`
}

// implementation is the Implementation shape (name, version) the schema
// uses for both clientInfo and serverInfo. Only the fields this profile
// ever reads or writes are declared: it stamps serverInfo and never
// inspects a request's clientInfo beyond noting its absence.
type implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// serverInfoMeta returns the _meta block every result stamps.
func serverInfoMeta() *result {
	return &result{ServerInfo: implementation{Name: ServerName, Version: ServerVersion}}
}

// envelope is a parsed JSON-RPC request or notification, decoded field by
// field so that a value this profile only ever echoes — id, and every
// _meta value compared against a header — survives as the exact bytes the
// client sent rather than a round-tripped re-encoding of them (decision
// 14).
type envelope struct {
	// ID is nil when the "id" key is entirely absent (a notification).
	// It is the literal bytes "null" when the key is present with a JSON
	// null value, which decision 12's shape rules treat as an invalid
	// request rather than a notification: JSON-RPC 2.0 permits a null
	// response id but MCP's own request rule is "MUST NOT be null".
	ID json.RawMessage

	JSONRPC      string
	JSONRPCFound bool
	Method       string
	MethodFound  bool
	Params       json.RawMessage
	HasResult    bool
	HasError     bool
}

// rawEnvelope is the decode target: every field a json.RawMessage, so that
// decoding never fails on a value of the wrong JSON type (a numeric
// "method", for instance) — envelope validity is judged afterwards, field
// by field, so every shape problem is reported as ITS OWN finding rather
// than one opaque decode error naming none of them.
type rawEnvelope struct {
	JSONRPC json.RawMessage `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  json.RawMessage `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

// decodeEnvelope decodes raw (already known to be a JSON object) into an
// envelope. The only error it can return is a raw's-not-an-object error,
// which cannot occur when raw came from a body httpx.DecodeObject already
// accepted — callers that reach here only after that check can safely
// ignore it, but it is still returned rather than panicked so a caller
// built for a different context is not surprised.
func decodeEnvelope(raw []byte) (envelope, error) {
	var re rawEnvelope
	if err := json.Unmarshal(raw, &re); err != nil {
		return envelope{}, err
	}

	e := envelope{
		ID:        re.ID,
		Params:    re.Params,
		HasResult: re.Result != nil,
		HasError:  re.Error != nil,
	}
	if re.JSONRPC != nil {
		var s string
		if json.Unmarshal(re.JSONRPC, &s) == nil {
			e.JSONRPC, e.JSONRPCFound = s, true
		}
	}
	if re.Method != nil {
		var s string
		if json.Unmarshal(re.Method, &s) == nil && s != "" {
			e.Method, e.MethodFound = s, true
		}
	}
	return e, nil
}

// isNotification reports whether the "id" key was absent entirely — the
// one shape decision 11 answers with 202 and nothing else.
func (e envelope) isNotification() bool {
	return e.ID == nil
}

// idIsExplicitNull reports whether "id" is present and is the JSON literal
// null, which MCP's request rule (id MUST NOT be null) makes a shape error
// rather than a notification.
func (e envelope) idIsExplicitNull() bool {
	return e.ID != nil && bytes.Equal(bytes.TrimSpace(e.ID), []byte("null"))
}

// idHasValidShape reports whether a present "id" is a JSON string or a
// plain integer literal — the only two types schema.json's RequestId
// admits (contracts/mcp/README.md, "Request" row: "the ID MUST be a
// string or integer"). A boolean, object, array, or a fractional/
// exponential number (1.5, 1e3) is therefore a shape error, exactly as an
// explicit null already is.
func (e envelope) idHasValidShape() bool {
	if e.ID == nil {
		return true // a notification: no id to judge.
	}
	t := bytes.TrimSpace(e.ID)
	if len(t) == 0 {
		return false
	}
	if t[0] == '"' {
		var s string
		return json.Unmarshal(t, &s) == nil
	}
	return isPlainIntegerLiteral(t)
}

// isPlainIntegerLiteral reports whether b is a JSON number token with no
// fraction and no exponent: "1" and "-7" pass; "1.5", "1e3" and "1E3" do
// not, even though json.Unmarshal would decode all of them into a float64
// that looks integral.
func isPlainIntegerLiteral(b []byte) bool {
	i := 0
	if i < len(b) && b[i] == '-' {
		i++
	}
	if i == len(b) {
		return false
	}
	for ; i < len(b); i++ {
		if b[i] < '0' || b[i] > '9' {
			return false
		}
	}
	return true
}

// isWellFormed reports whether e could be either a request or a
// notification: jsonrpc is exactly "2.0", method is a non-empty string,
// neither "result" nor "error" is present, and id (when present) is not
// explicitly null and has a type the schema admits.
func (e envelope) isWellFormed() bool {
	return e.JSONRPCFound && e.JSONRPC == "2.0" && e.MethodFound &&
		!e.HasResult && !e.HasError && !e.idIsExplicitNull() && e.idHasValidShape()
}

// nullID is the sentinel "id" for every response this profile cannot, or
// must not, attribute to a request id — a shape failure the id could not
// be read from, and every 401 and 405 (decisions 3 and 6). It is nil, not
// the JSON literal "null": schema.json's RequestId is "string | integer"
// with no null variant, so JSONRPCErrorResponse.id — optional in the
// schema — must be OMITTED, never sent as `"id":null`, or the body fails
// schema validation and a spec-conforming client's era-detection no
// longer recognises it as a modern JSON-RPC error (contracts/mcp/README.md,
// "Error response" row and "Legacy traffic at a modern-only server").
// errorEnvelope's `id` field carries the matching "omitempty".
var nullID json.RawMessage

// errorEnvelope is the JSON-RPC error response shape. id is omitempty:
// schema.json makes it optional on JSONRPCErrorResponse, and nullID (the
// only nil value ever passed here) must render as an absent key, not a
// JSON null — see nullID's own doc comment.
type errorEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Error   rpcError        `json:"error"`
}

// rpcError is one JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// buildError renders a JSON-RPC error envelope. id is echoed verbatim
// (pass nullID for every case decision 3/6 require a null id).
func buildError(id json.RawMessage, code int, message string, data any) []byte {
	body, err := provider.Render(errorEnvelope{
		JSONRPC: "2.0",
		ID:      id,
		Error:   rpcError{Code: code, Message: message, Data: data},
	}, nil, nil)
	if err != nil {
		// errorEnvelope is fully concrete; marshalling it cannot fail.
		return []byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal error"}}`)
	}
	return body
}

// resultEnvelope is the JSON-RPC success response shape. Result is
// json.RawMessage so a method's own render function controls field order
// and can merge extra_fields before this envelope closes over it.
type resultEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
}

// buildResult renders a JSON-RPC success envelope around an already-
// rendered result body.
func buildResult(id json.RawMessage, resultBody []byte) ([]byte, error) {
	return provider.Render(resultEnvelope{JSONRPC: "2.0", ID: id, Result: resultBody}, nil, nil)
}

// errAt is the shape of a shape-failure this package's checks build
// directly: a status, a JSON-RPC error code, and a message. It exists so
// every check site returns one value rather than three, and so faultBody
// and the dispatch error paths share exactly one Response constructor
// (statusResponse) for the envelope.
type errAt struct {
	Status  int
	Code    int
	Message string
	Data    any
	ID      json.RawMessage
}

// statusResponse renders e as a provider.Response. label follows this
// package's convention of "mcp.<what>.<detail>", matching every other
// provider package's Label field.
func statusResponse(e errAt, label string) provider.Response {
	return provider.Response{
		Status: e.Status,
		Body:   buildError(e.ID, e.Code, e.Message, e.Data),
		Label:  label,
	}
}

// faultBody returns the FaultBody function this profile registers on
// every Response: the fixed JSON-RPC error shape decision 6 records
// ("servicesim scripted fault: <kind>"), with two overrides an attempt may
// supply, evaluated in this order — both are the shared scenario.FaultAttempt
// grammar every other provider package already honours (see
// provider/tavily/errors.go, provider/exa/errors.go), not an MCP-specific
// addition: an attempt's own body: replaces the whole envelope outright
// (decision 7's "an arbitrary status + body pair"), and an attempt's own
// error: replaces only the message text, keeping code -32603 and the
// envelope shape — this is how every built-in fault scenario
// (rate-limited, brownout, server-error) puts a human-readable message
// ("Rate limit exceeded.") on the wire instead of the generic
// "servicesim scripted fault: status" text.
func faultBody(id json.RawMessage) func(scenario.FaultAttempt) []byte {
	return func(a scenario.FaultAttempt) []byte {
		if len(a.Body) > 0 {
			body, err := json.Marshal(a.Body)
			if err == nil {
				return body
			}
			return nil
		}
		if a.Error != "" {
			return buildError(id, CodeInternalError, a.Error, nil)
		}
		if a.Status < http.StatusBadRequest {
			// A delay, a truncation, a wrong content type or a trailing
			// "status: 200" attempt all keep the scenario's own rendered
			// body — nothing here to override.
			return nil
		}
		return buildError(id, CodeInternalError,
			"servicesim scripted fault: "+string(a.EffectiveKind()), nil)
	}
}
