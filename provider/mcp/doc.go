// Package mcp simulates a Model Context Protocol Streamable HTTP server,
// modern era only (protocol revision 2026-07-28). One listener, one route
// (POST /mcp), JSON-RPC 2.0 dispatch on body.method to server/discover,
// tools/list and tools/call.
//
// The legacy 2025-11-25 era (initialize/session/GET stream) is out of scope
// for this unit; nothing here precludes adding it later behind the same
// endpoint. handler.go and jsonrpc.go are era-neutral on purpose (the
// envelope, dispatch and tool machinery); transport.go is where the
// era-specific header/_meta validation and the legacy-traffic detection
// live, so a follow-on legacy unit has one file to extend rather than a
// scattered set of special cases.
//
// contracts/mcp/README.md is the wire authority. It records what the
// specification says and, where the specification is silent, what this
// build chose. Every choice below is also recorded as a `note:` on the
// golden it shapes in contracts/mcp/provenance.yaml.
//
// # Recorded simulator-chosen defaults
//
// The contract's "Simulation decisions" section numbers these; the numbers
// match here so a reader can cross-reference the two files.
//
//  1. Era: modern only. supportedVersions is exactly ["2026-07-28"]. A
//     request whose MCP-Protocol-Version header or _meta protocol version
//     names anything else is 400 + UnsupportedProtocolVersionError (-32022)
//     with data.supported/data.requested.
//  2. Endpoint path: POST /mcp, listener "mcp", default port 8084. The
//     specification's own example path; the profile does not have to invent
//     one.
//  3. Credentials are OPTIONAL by default. The profile's AuthPolicy default,
//     when a scenario declares no auth: block, is "optional" — the existing
//     grammar, no new field — with PlacementAuthorization the only accepted
//     placement. A scenario that sets auth: {mode: required} turns a
//     missing or mismatched credential into 401 with a JSON-RPC error body
//     that carries NO id member at all (omitted, regardless of the
//     request's own id — never rendered as a JSON null: schema.json's
//     RequestId admits only string or integer, so JSONRPCErrorResponse.id,
//     which the schema itself marks optional, must be an absent key when
//     it cannot be attributed to a request), code -32600, message exactly
//     "authorization required", and no WWW-Authenticate header: the
//     profile must not half-implement OAuth.
//  4. x-mcp-header is NOT honoured in this unit. Validator rejects, at
//     load, any fixture tool whose input_schema contains an "x-mcp-header"
//     key anywhere in its tree (finding mcp.tool.x_mcp_header_unsupported,
//     ERROR). Any Mcp-Param-* request header is ignored with a WARNING
//     (mcp.header.param_ignored) — the specification does not say what a
//     terminal server does with an unrecognised one.
//  5. SSE answers. The stream policy comes from the entry's first turn
//     (scenario.StreamScript.EffectivePolicy, read before any turn is
//     selected — see doc comments on streamPolicy). absent/warn: the
//     answer to every method is always a JSON object. stream: the answer
//     to tools/call ONLY is always a per-request SSE stream (server/discover
//     and tools/list never stream, regardless of policy) carrying one
//     notifications/progress frame per delta — params: {progressToken,
//     progress: i+1, total: len(deltas), message: deltas[i].text} — when
//     the request carried _meta.progressToken, followed by the final
//     JSON-RPC response frame; a request with no progressToken carries only
//     the response frame (the specification's MUST NOT). reject is a load
//     ERROR (mcp.stream.reject_meaningless): the client has no field that
//     asks MCP to stream (the server decides unilaterally), so nothing is
//     ever rejected on that basis. Framing (the specification does not
//     say): unnamed events, one "data:" line per line of JSON, blank line
//     after each, no "event:" name, no "id:", no [DONE] — GrammarDelta with
//     no Stream.Sentinel. Headers: provider.StreamHeader() plus
//     X-Accel-Buffering: no (the specification SHOULDs this one, unlike
//     Perplexity's streaming headers, so it is added here). resp.Body is
//     always the JSON-object answer for the same turn, streamed or not.
//     Journal note: the retained outcome.stream.grammar reads
//     "chat_completions" — GrammarDelta's wire label, shared with
//     Perplexity's Sonar dialect, since both render unnamed data:-only
//     frames the same way. It names the FRAMING this profile's stream
//     shares with Sonar's, not an OpenAI/Perplexity dialect on the wire;
//     accepted as-is rather than adding a neutral grammar name for one
//     journal-only string with no wire effect.
//  6. HTTP status carrying an ordinary JSON-RPC error: 200. A well-formed
//     JSON-RPC request that reaches dispatch and fails at the method level
//     (unknown tool, invalid params, invalid cursor: -32602; an internal
//     render failure: -32603) is 200 with a JSON-RPC error body — the
//     reason 400 stays reserved is that the specification's client
//     era-detection algorithm treats a 400 body as an era signal. Request
//     SHAPE failures are 400: unparseable JSON or an empty body -32700;
//     a body that is not a JSON-RPC request or notification (wrong/missing
//     jsonrpc, missing/non-string method, a top-level array or scalar, or
//     an id of a type schema.json's RequestId does not admit — anything
//     but a string or a plain integer literal) or an id explicitly null
//     -32600 (id omitted); a client-sent JSON-RPC *response* body -32600
//     (id omitted); missing/wrong Accept -32600 naming the header; request
//     Content-Type not application/json -32600 naming the header. A
//     request body that exceeds the size limit is treated the same as
//     unparseable (-32700): this build could not read enough of it to know
//     what it was — an additional simulator-chosen point beyond the
//     contract's own list, since the contract does not address body-size
//     limits at all. The four spec-assigned 400s (-32020, -32022, missing
//     required _meta -32602, -32021 not reachable here) and the 404 -32601
//     keep their assigned status. 405 for every method but POST (Allow:
//     POST) with a JSON-RPC error body without an id member, code -32600,
//     message "the MCP endpoint accepts POST only". A route this listener
//     does not serve at all (any path but /mcp) is 404 with -32601, no id
//     member, naming the one path this listener serves. Every response
//     whose id cannot be attributed to a request (a shape failure, 401,
//     405, or an unmatched route) OMITS the id member rather than sending
//     it as a JSON null — schema.json's JSONRPCErrorResponse.id is
//     optional but, when present, is RequestId-typed (string or integer,
//     no null variant), so "null" would be schema-invalid and would also
//     defeat the specification's own client-side era-detection algorithm,
//     which inspects a 400 body for a "recognized modern JSON-RPC error"
//     before falling back to legacy initialize. Every fault-attempt error
//     status renders {"jsonrpc":"2.0","id":<echoed, or omitted when
//     unknown>,"error":{"code":-32603,"message":"servicesim scripted fault:
//     <kind>"}}, with two overrides an attempt may supply (in this order):
//     its own body: replaces the whole envelope outright (decision 7), and
//     its own error: replaces only the fixed message text — the shared
//     scenario.FaultAttempt grammar every other provider package already
//     honours, which is how the rate-limited/brownout/server-error
//     built-ins put a human-readable message on the wire instead of the
//     generic "servicesim scripted fault: status" text. A consumer's retry
//     logic keys on the HTTP status, and the specification assigns no
//     JSON-RPC code to a transport-level 429/503.
//  7. Origin is not validated in this unit. Absent: nothing. Present:
//     accepted and journaled as an ordinary header (no finding) — this is
//     a no-op, since the shared request lifecycle already journals every
//     header. A scenario that wants to prove a client's 403 handling
//     scripts it through fault: {status: 403, body: {...}}, which this
//     profile's faultBody already supports (an attempt's own Body wins
//     outright over the synthesised shape).
//  8. The tool catalogue and results are fixture data. Validator enforces:
//     tool names unique and matching ^[A-Za-z0-9_.-]{1,128}$ (SHOULD in the
//     spec, so a non-matching name is a WARNING; a DUPLICATE name is an
//     ERROR); input_schema a JSON object with type: "object" at the root
//     (ERROR otherwise); output_schema optional; a tool that declares
//     output_schema and whose scripted result carries structured_content is
//     NOT validated against the schema (no JSON Schema validator in
//     stdlib) — a load-time WARNING (mcp.tool.output_schema_unchecked) says
//     so once per such tool. tools/list order is declaration order.
//  9. server/discover values: supportedVersions is the package constant
//     supportedVersions; capabilities is exactly {"tools":{}} (no
//     listChanged: this profile emits no list-change notifications);
//     _meta["io.modelcontextprotocol/serverInfo"] is {name: ServerName,
//     version: ServerVersion} on every result (server/discover, tools/list
//     and tools/call alike) — both are Go constants, so the bytes are
//     identical across builds; instructions comes from the projection when
//     set; ttl_ms and cache_scope come from the projection with defaults
//     DefaultTTLMs (60000) and DefaultCacheScope ("private").
//  10. Accept and request Content-Type are strict for every request (never
//     for a notification, whose header requirements the specification
//     says are undefined) — see 6.
//  11. Notifications. A well-formed JSON-RPC notification (no id) of any
//     method is 202 Accepted with no body — journaled with the label
//     "mcp.notification.accepted"; the modern core defines no
//     client-to-server notification over HTTP, so nothing about the
//     notification's own method is checked. A malformed notification
//     follows the 400 shape rules above, exactly as a malformed request
//     does, because a notification is malformed before this profile can
//     even tell it apart from a request.
//  12. Legacy traffic. Mcp-Session-Id present: ignored, never minted or
//     echoed, WARNING mcp.legacy.session_id. Last-Event-ID present:
//     ignored, WARNING mcp.legacy.last_event_id. Both checks run
//     unconditionally, notification or request alike, because they are
//     pure observations with no bearing on how the request is served. A
//     request whose method is "initialize": the header check runs FIRST,
//     ahead of the missing-_meta-fields check — a legacy client's
//     initialize lacking MCP-Protocol-Version or Mcp-Method is 400 +
//     -32020 (HeaderMismatchError) naming the supported versions, WARNING
//     mcp.legacy.initialize. A client that sends initialize WITH valid
//     modern headers and _meta falls through to the ordinary method
//     dispatch, where "initialize" is simply not one of the three
//     implemented methods — 404 + -32601, with the message also naming
//     the supported versions (the specification SHOULDs this one message,
//     specifically, because a legacy client that got this far has no
//     other diagnostic to show a human).
//  13. _meta requirements: io.modelcontextprotocol/protocolVersion and
//     io.modelcontextprotocol/clientCapabilities are required on every
//     request (server/discover, tools/list, tools/call alike) — missing
//     either is 400 + -32602 naming which field(s). clientInfo is
//     optional; its absence is a WARNING (mcp.meta.client_info_missing) —
//     house rule 5's "prove the correct request", not a spec MUST.
//     MCP-Protocol-Version vs _meta.protocolVersion: checkTransport's own
//     fixed order decides which finding a mismatch produces, since the
//     two checks answer different questions and run one after the other,
//     not simultaneously. The header's presence is checked first: if it
//     is missing, that is -32020 (HeaderMismatchError) regardless of
//     _meta, because the header is itself a required standard header. A
//     present header with an absent _meta.protocolVersion is instead the
//     missing-required-_meta-field case above, -32602 (Invalid params) —
//     the contract's own assigned code for that row — because the
//     missing-_meta check runs before the two values are ever compared.
//     Only once both are present does a HeaderMismatch, -32020, apply:
//     when they disagree. Once they agree, a value other than
//     supportedVersions[0] is the -32022 case (item 1). Mcp-Method MUST
//     equal method exactly (case-sensitive). Mcp-Name is required for
//     tools/call only, decoded through the Base64 sentinel
//     (=?base64?...?=) before being compared against params.name — a
//     value inside the sentinel markers that is not valid standard-alphabet
//     base64 is -32020 "invalid characters". Mcp-Name present on a method
//     that does not need one is ignored with WARNING
//     mcp.header.name_unexpected. Header names are compared
//     case-insensitively (net/http already folds this).
//  14. JSON-RPC id is echoed exactly as the client sent it — the raw JSON
//     token is carried through as json.RawMessage end to end, never
//     decoded and re-encoded, so "1" never becomes "1.0" or "\"1\"".
//     Every result's _meta carries serverInfo; every result carries
//     resultType: "complete".
//
// # Redaction and the Base64 sentinel (house rule 4)
//
// Mcp-Name and Mcp-Param-{name} may carry the specification's own
// reversible Base64 sentinel encoding (=?base64?...?=, "Value Encoding").
// The DECODED value — params.name, an unknown-tool finding, a header-
// mismatch finding — is masked exactly as every other free-text field is,
// through the ordinary redact.String / journal.Redact path. The RETAINED
// HEADER VALUE is not: internal/redact has no sentinel awareness (it is a
// generic package with no knowledge of any one provider's wire encodings),
// so a credential-shaped tool name sent sentinel-encoded survives in the
// journal in its Base64 form. This is a documented boundary, not a gap
// this unit closes: no client-shaped input ever puts a real credential in
// a tool name (Mcp-Param-{name} is where the specification itself warns
// server developers away from marking sensitive parameters, "tools",
// "Security Considerations"), and the same opacity-to-a-generic-masker
// property already exists for any free-text field on any profile. See
// [TestMcpNameSentinelDecodedValueIsRedactedHeaderIsOpaque].
//
// # Wire field names
//
// Every wire field name below is read from contracts/mcp/README.md's
// tables, never from memory: resultType, supportedVersions, capabilities,
// instructions, ttlMs, cacheScope, tools, nextCursor, name, title,
// description, inputSchema, outputSchema, annotations (readOnlyHint,
// destructiveHint, idempotentHint, openWorldHint), content, structuredContent,
// isError, type (text/image/audio/resource_link/resource), text, data,
// mimeType, uri, resource (uri/text/blob/mimeType).
//
// # Content source references
//
// A content block of type "text" may carry a `source:` reference (the
// scenario-wide source-ref shorthand) instead of a literal `text:`. It
// resolves to the referenced source's text, falling back to its title when
// the source has no text — never the other way around, because a hostile
// fixture's malicious-content markers live in Text, and a fallback order
// that preferred Title would silently drop them for a source authored
// text-only. This is NOT threaded through scenario.ResolveRefs's generic
// reflection walk: ContentBlock is decoded from a slice of tagged-union
// mappings, none of which is a bare scenario.SourceRef, and an inlined
// SourceRef there would hit the same yaml.v3 whole-mapping-Unmarshaler
// collision documented on provider/tavily's rawResultProjection. Resolution
// is instead a dedicated pass (resolveContentSources) that Validator and
// selectProjection both call.
package mcp

// supportedVersions is the one protocol revision this profile speaks.
// Unexported and never handed out by reference: nothing outside this
// package needs to read it, and an exported mutable slice would let an
// importing consumer change every discover result, -32022 data payload
// and version check across the process — a determinism hazard (house
// rule 2) that costs nothing to avoid, since no consumer ever named it.
var supportedVersions = []string{"2026-07-28"}

// Server identity constants, stamped into every result's
// _meta["io.modelcontextprotocol/serverInfo"]. Both are Go constants so the
// bytes are identical across builds (decision 9).
const (
	ServerName    = "servicesim"
	ServerVersion = "1"
)

// Documented defaults for server/discover and tools/list caching hints
// (decision 9).
const (
	DefaultTTLMs      = 60000
	DefaultCacheScope = "private"
)

// JSON-RPC methods this profile dispatches.
const (
	MethodDiscover  = "server/discover"
	MethodToolsList = "tools/list"
	MethodToolsCall = "tools/call"

	// methodInitialize is recognised only to special-case decision 12's
	// legacy-traffic message; it is never served.
	methodInitialize = "initialize"
)
