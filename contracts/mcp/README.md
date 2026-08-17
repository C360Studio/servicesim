# MCP consumed contract

Verified against the live specification on **2026-08-16**.

This file records only what Servicesim will simulate and what consumers parse. It is not a
redistribution of the specification. Re-verify and update the date above on the sanctioned dated
re-verification cadence (`contracts/README.md` "Keeping them honest" — there is no live contract
canary).

**Status: not yet simulated.** This contract was recorded ahead of the handler (Phase 8 unit 1). No
route, listener, `contracts.Provider` constant or golden exists for MCP yet; those arrive with the
handler unit (unit 2), which registers the provider. Until then `contracts.Providers()` does not
return `mcp` and nothing embeds this directory. Everything below is what the specification says, so
that the handler is written from a record and not from memory.

## Authority and revision

The "vendor" here is a protocol specification, not a company API. The authority for this contract
is the **Model Context Protocol specification revision `2026-07-28`**, because:

- <https://modelcontextprotocol.io/specification/latest> answers `307` with
  `location: /specification/2026-07-28` (checked 2026-08-16), and every specification page in
  <https://modelcontextprotocol.io/llms.txt> is under `/specification/2026-07-28/`.
- The official SDKs shipped support for it the day it was published or the day before (TypeScript
  `@modelcontextprotocol/*@2.0.0` on 2026-07-27, go-sdk `v1.7.0` and python-sdk `v2.0.0` on
  2026-07-28) — see "Protocol eras" below for the `gh api` evidence.
- Its machine-readable schema, `schema/2026-07-28/schema.json`, is the `spec:` block in
  `provenance.yaml` beside this file (sha256
  `ef70b61f99b6d2e5e3b46863822eab08dff6a45bedc7a08914e0e5b133f40203`, 181474 bytes, fetched
  2026-08-16 from `main` at commit `271ecc9accafdd9b83a3c869fa67c22953b2af80`, whose committer date
  is 2026-07-28T16:42:34Z). The specification page's own words: the TypeScript `schema.ts` "is the
  source of truth for all protocol messages and structures" and the JSON Schema "is automatically
  generated from the TypeScript source of truth" (`basic/index`, "Schema"). Where a rendered page
  and `schema.json` disagree, this file records both and says which said what.

Revision **2025-11-25** is recorded below **only** as the last legacy revision a dual-era server
would additionally speak (the specification's own terms — see "Protocol eras"). It is not the
authority for anything the profile simulates unless the owner decides D11 that way.

`2026-07-28` is a large change from `2025-11-25`. In the changelog's words (`changelog`, "Major
changes", quoted in part): "Remove protocol-level sessions and the `Mcp-Session-Id` header from the
Streamable HTTP transport"; "Make MCP stateless: remove the `initialize`/`notifications/initialized`
handshake. Every request now carries its protocol version and client capabilities in `_meta`";
"Add `server/discover`: servers MUST implement this RPC"; "Replace the HTTP GET endpoint and
`resources/subscribe`/`resources/unsubscribe` with `subscriptions/listen`"; "Remove `ping`,
`logging/setLevel`, and `notifications/roots/list_changed`"; "All results now carry a required
`resultType` field"; "Remove SSE stream resumability and message redelivery (the `Last-Event-ID`
header and SSE event IDs)".

## Documentation sources

Every modelcontextprotocol.io page below was read on 2026-08-16 as raw markdown (`curl -sSL
"<url>.md"`, HTTP 200 for each); the two `schema.json` files were fetched as-is (see "Provenance").
Statements in this file cite the page they came from by its short name.

Revision 2026-07-28 (`https://modelcontextprotocol.io/specification/2026-07-28/<page>`):

- <https://modelcontextprotocol.io/specification/2026-07-28/index> — `index`
- <https://modelcontextprotocol.io/specification/2026-07-28/changelog> — `changelog`
- <https://modelcontextprotocol.io/specification/2026-07-28/deprecated> — `deprecated`
- <https://modelcontextprotocol.io/specification/2026-07-28/architecture/index> — `architecture`
- <https://modelcontextprotocol.io/specification/2026-07-28/basic/index> — `basic/index`
- <https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning> — `versioning`
- <https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/index> — `patterns`
- <https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/mrtr> — `mrtr`
- <https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/subscriptions> — `subscriptions`
- <https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/cancellation> — `cancellation`
- <https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/progress> — `progress`
- <https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/index> — `transports`
- <https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http> — `streamable-http`
- <https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/index> — `authorization`
  (read only for what a server must or may require of authentication; the OAuth flow is not
  recorded)
- <https://modelcontextprotocol.io/specification/2026-07-28/server/index> — `server/index`
- <https://modelcontextprotocol.io/specification/2026-07-28/server/discover> — `discover`
- <https://modelcontextprotocol.io/specification/2026-07-28/server/tools> — `tools`
- <https://modelcontextprotocol.io/specification/2026-07-28/server/utilities/caching> — `caching`
- <https://modelcontextprotocol.io/specification/2026-07-28/server/utilities/pagination> — `pagination`
- <https://modelcontextprotocol.io/specification/2026-07-28/server/utilities/logging> — `logging`
- <https://modelcontextprotocol.io/specification/2026-07-28/schema> — `schema` (the rendered
  schema reference; a typedoc dump of `schema.ts` with JSON examples)
- <https://raw.githubusercontent.com/modelcontextprotocol/modelcontextprotocol/main/schema/2026-07-28/schema.json>
  — `schema.json` (the machine-readable schema; the `spec:` block)

Revision 2025-11-25 (legacy record only):

- <https://modelcontextprotocol.io/specification/2025-11-25/basic/transports> — `2025 transports`
- <https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle> — `2025 lifecycle`
- <https://modelcontextprotocol.io/specification/2025-11-25/server/tools> — `2025 tools`
- <https://modelcontextprotocol.io/specification/2025-11-25/changelog> — `2025 changelog`
- <https://modelcontextprotocol.io/specification/2025-06-18/changelog> — `2025-06-18 changelog`
  (the batching removal lives here; the 2025-11-25 changelog only names 2025-06-18 as its
  predecessor)
- <https://raw.githubusercontent.com/modelcontextprotocol/modelcontextprotocol/main/schema/2025-11-25/schema.json>
  — `2025 schema.json` (sha256 `268a5f82ba70fd7e4b6dc4aa1e64f116f74b4d0edcb69dc046829c79dd4e97e7`,
  174323 bytes, fetched 2026-08-16; last commit on `main` touching it
  `c4c367f9f58296a7053f5c78a52fd02bfbb56a49`, 2026-07-27T14:20:44Z)

SDK evidence (release metadata, not specification text) was read with `gh api` on 2026-08-16 from
`repos/modelcontextprotocol/go-sdk`, `repos/modelcontextprotocol/typescript-sdk` and
`repos/modelcontextprotocol/python-sdk`; see "Protocol eras".

## Transport — Streamable HTTP

Source unless stated: `streamable-http`. This is the only transport in scope; stdio is not
simulated.

### The MCP endpoint

- "The server **MUST** provide a single HTTP endpoint path (hereafter referred to as the **MCP
  endpoint**) that supports POST. For example, this could be a URL like `https://example.com/mcp`."
  The path is server-chosen; every example on the page uses `/mcp`. **The simulated path is a
  unit-2 decision** (see "Simulation decisions" below); this file calls it "the endpoint".
- "The client sends every JSON-RPC request or notification as its own HTTP POST."
- "The server answers each request with either a single JSON object or a Server-Sent Events (SSE)
  stream scoped to that request, carrying request-related notifications followed by the final
  response."
- `transports`: "servers do not initiate JSON-RPC requests and clients do not send JSON-RPC
  responses."

### Client-side requirements ("Sending Messages", verbatim numbering)

1. "The client **MUST** use HTTP POST to send JSON-RPC messages."
2. "The client **MUST** include an `Accept` header listing both `application/json` and
   `text/event-stream` as supported content types."
3. "The client **MUST** include the request metadata headers on each POST request."
4. "The body of the HTTP POST **MUST** be a single JSON-RPC *request* or *notification*. The client
   **MUST NOT** send JSON-RPC *responses*."
5. Notification body: "If the server accepts it, the server **MUST** return HTTP status code
   `202 Accepted` with no body. If the server cannot accept it, it **MUST** return an HTTP error
   status code (e.g., `400 Bad Request`). The HTTP response body **MAY** comprise a JSON-RPC *error
   response* that has no `id`."
6. Request body: "the server **MUST** return either `Content-Type: application/json` (a single JSON
   object) or `Content-Type: text/event-stream` (an SSE response stream). The client **MUST** support
   both."

The page adds, in a note: "This revision of the core protocol defines no client-to-server
*notifications* over Streamable HTTP. The only client-sent notification in the core protocol,
`notifications/cancelled`, is used only on the stdio transport; on Streamable HTTP, closing the SSE
response stream is itself the cancellation signal and no `notifications/cancelled` message is
expected … header requirements for notification POSTs are not defined by this revision."

Where the specification is silent on the client-side rules:

- **The specification does not say** what a server does when the request's `Accept` header is
  missing or lists only one of the two types (client MUST; no server reaction is assigned).
- **The specification does not say** anything normative about the request `Content-Type` header;
  the examples show `Content-Type: application/json` and nothing more.
- **The specification does not say** which HTTP status carries a JSON-object answer. The
  normative rule (item 6) speaks only of the two `Content-Type` values; `200 OK` appears only in
  the page's sequence diagram (`Server-->>Client: 200 OK, application/json`).

### Request-metadata headers

"The Streamable HTTP transport mirrors selected JSON-RPC body fields into HTTP headers so that
intermediaries (load balancers, gateways, observability tooling) can route and inspect requests
without parsing the body." `transports` adds: "The body remains the source of truth; bindings that
mirror metadata define how mismatches are rejected."

| Header | Source field | Required for | Source |
|---|---|---|---|
| `MCP-Protocol-Version` | `params._meta["io.modelcontextprotocol/protocolVersion"]` | "Every POST request to the MCP endpoint **MUST** include" it, e.g. `MCP-Protocol-Version: 2026-07-28` | `streamable-http` "Protocol Version Header" |
| `Mcp-Method` | `method` | "All requests" | `streamable-http` "Standard Request Headers" table |
| `Mcp-Name` | `params.name` or `params.uri` | "`tools/call`, `resources/read`, `prompts/get` requests" | same table |
| `Mcp-Param-{name}` | the argument at the annotated property path | Only when a tool's `inputSchema` property carries `x-mcp-header` and the argument is present and non-`null` | `streamable-http` "Custom Headers from Tool Parameters" |

Rules recorded verbatim:

- "These headers are **REQUIRED** for compliance." (the standard-headers table)
- Case: "Header names … are case-insensitive. Clients and servers **MUST** use case-insensitive
  comparisons for header names. Header *values* (such as method names) are case-sensitive."
  (The page itself spells one header `MCP-Protocol-Version` and the others `Mcp-Method`,
  `Mcp-Name`, and — interchangeably — `Mcp-Param-{name}` / `Mcp-Param-{Name}`; the mixed
  spelling is the page's, and is immaterial under this rule.)
- Version header vs body: "The header value **MUST** match the
  `io.modelcontextprotocol/protocolVersion` field carried in the request body's `_meta`. If the
  values do not match, the server **MUST** reject the request with `400 Bad Request` and a
  `HeaderMismatch` JSON-RPC error". `schema.json`'s `RequestMetaObject` description says the same:
  "For the HTTP transport, this value MUST match the `MCP-Protocol-Version` header; otherwise the
  server MUST return a `400 Bad Request`."
- Missing header for pre-2025-06-18 clients: "A server that supports clients implementing protocol
  versions earlier than `2025-06-18` (which did not define the `MCP-Protocol-Version` header)
  **MAY** treat a request that omits the header as protocol version `2025-03-26`. A server that
  does not support such clients **MUST** reject a request without the header per Server
  Validation." (i.e. `400` + `-32020`).
- Server validation ("Server Validation"): "Servers that process the request body **MUST** reject
  requests where the values specified in the headers do not match the corresponding values in the
  request body." "When rejecting a request due to header validation failure, servers **MUST**
  return HTTP status `400 Bad Request` and **MUST** include a JSON-RPC error response using the
  following error code: `-32020` `HeaderMismatch` — The HTTP headers do not match the corresponding
  values in the request body, or required headers are missing/malformed." Failure conditions listed:
  "A required standard header (`MCP-Protocol-Version`, `Mcp-Method`, `Mcp-Name`) is missing"; "A
  header value does not match the corresponding request body value. For headers that permit the
  Base64 sentinel encoding (`Mcp-Name` and `Mcp-Param-{Name}`), servers **MUST** decode encoded
  values … before comparing them to the body value"; "A header value contains invalid characters."
  Note: "When validating integer parameter values, servers **SHOULD** compare the header value and
  the body value numerically rather than as strings (e.g., `42.0` and `42` are considered equal)."
- The page's `-32020` example (also the `schema` page's `HeaderMismatchError` example) carries the
  request `id` and no `data` member:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32020,
    "message": "Header mismatch: Mcp-Name header value 'foo' does not match body value 'bar'"
  }
}
```

Only `Mcp-Name` and `Mcp-Param-{Name}` are named as sentinel-permitting headers; `Mcp-Method` is
compared as-is against `method` (values are case-sensitive), so a Base64-wrapped `Mcp-Method` value
would not match and fails validation (`400` + `-32020`). **The specification does not say** what a
terminal server does with an `Mcp-Param-{Name}` header that maps to no
`x-mcp-header` in the named tool's `inputSchema` (only "Intermediate servers that do not recognize
an `Mcp-Param-{Name}` header **MUST** forward it and otherwise ignore it" and "Servers **MUST**
reject requests with a recognized `Mcp-Param-{Name}` header that contains invalid characters").

### Value encoding — the Base64 sentinel ("Value Encoding")

- Type conversion: "`string`: Use the value as-is; `integer`: Convert to decimal string
  representation (e.g., `42`, `-7`); `boolean`: Convert to lowercase `"true"` or `"false"`".
- "When a value cannot be safely represented as a plain ASCII header value (e.g., it contains
  non-ASCII characters, control characters, or has leading/trailing whitespace), clients **MUST**
  use Base64 encoding of the UTF-8 representation with the following format:
  `Mcp-Param-{Name}: =?base64?{Base64EncodedValue}?=`". "The same encoding rule applies to the
  `Mcp-Name` header value."
- "The prefix `=?base64?` and suffix `?=` indicate that the value is Base64-encoded. These markers
  are case-sensitive and **MUST** appear exactly as shown (lowercase). Servers and intermediaries
  that need to inspect these values **MUST** decode them accordingly. In particular, servers
  **MUST** decode an encoded `Mcp-Name` or `Mcp-Param-{Name}` value before comparing it to the
  corresponding request body value during Server Validation."
- "To avoid ambiguity, clients **MUST** also Base64-encode any plain-ASCII value that matches the
  sentinel pattern (i.e., starts with `=?base64?` and ends with `?=`)."
- Examples from the page: `"Hello, 世界"` → `=?base64?SGVsbG8sIOS4lueVjA==?=`; `" padded "` →
  `=?base64?IHBhZGRlZCA=?=`; `"line1\nline2"` → `=?base64?bGluZTEKbGluZTI=?=`;
  `"=?base64?literal?="` → `=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?=`.
- **The specification does not say** which Base64 alphabet is meant; the examples use `=` padding
  and `/`, which is the standard (not URL-safe) alphabet, but the page does not name it.

### The `x-mcp-header` schema extension

Sources: `streamable-http` "Custom Headers from Tool Parameters" and `tools` "x-mcp-header".
Whether the profile honours this extension is a unit-2 decision (OWNER) — the rules are recorded so
that decision is informed.

- "MCP servers **MAY** designate specific tool parameters to be mirrored into HTTP headers using an
  `x-mcp-header` extension property in the parameter's schema within the tool's `inputSchema`."
  "While the use of `x-mcp-header` is optional for servers, clients **MUST** support this feature."
- "The `x-mcp-header` property specifies the name portion used to construct the header name
  `Mcp-Param-{name}`."
- Constraints on `x-mcp-header` values, verbatim: "**MUST NOT** be empty"; "**MUST** match HTTP
  field-name token syntax (`1*tchar`, RFC 9110 Section 5.1)"; "**MUST NOT** contain control
  characters, including carriage return (CR, `\r`) or line feed (LF, `\n`)"; "**MUST** be
  case-insensitively unique among all `x-mcp-header` values in the `inputSchema`"; "**MUST** only be
  applied to parameters with primitive types (integer, string, boolean). Parameters with type
  `number` are not permitted. Integer values **MUST** be within the safe range for JavaScript
  (−2^53+1 to 2^53−1)"; "**MUST** only be applied to properties that are *statically reachable* from
  the schema root: reachable via a chain consisting solely of `properties` keys. The chain **MUST
  NOT** pass through `items` (or any other array keyword), composition keywords (`oneOf`, `anyOf`,
  `allOf`, `not`), conditional keywords (`if`/`then`/`else`), or `$ref`. … An `x-mcp-header`
  annotation anywhere else makes the annotation — and thus the tool definition — invalid."
- "Header extraction is defined as reading the instance value at the exact property path of the
  annotated property … If no value is present at that path in the call arguments, the header is
  omitted."
- Client side: "Clients using the Streamable HTTP transport **MUST** reject tool definitions where
  any `x-mcp-header` value violates these constraints. Rejection means the client **MUST** exclude
  the invalid tool from the result of `tools/list`."
- Server side: "Any server that processes the message body **MUST** validate that encoded header
  values, after decoding if Base64-encoded, match the corresponding values in the request body.
  Servers **MUST** reject requests with a `400 Bad Request` HTTP status and JSON-RPC error code
  `-32020` (`HeaderMismatch`) if any validation fails." Table (verbatim): value provided → client
  MUST include the header, server MUST validate header matches body; value is `null` → client MUST
  omit, server MUST NOT expect; parameter not in arguments → client MUST omit, server MUST NOT
  expect; client omits header but value is in body → "Non-conforming client", "Server MUST reject
  the request".
- `tools`: "Server developers **SHOULD NOT** mark sensitive parameters (passwords, API keys,
  tokens, PII) with `x-mcp-header`, as header values are visible to network intermediaries."

### Server responses by case

Every row cites where it was read. HTTP status and body are both part of the contract.

| Case | HTTP status | Body | Source |
|---|---|---|---|
| Notification accepted | `202 Accepted` | none ("with no body") | `streamable-http` "Sending Messages" 5 |
| Notification not accepted | "an HTTP error status code (e.g., `400 Bad Request`)" | MAY be a JSON-RPC error response with no `id` | same |
| Request answered as one object | `Content-Type: application/json`; **the specification does not say** the status — `200 OK` only in the sequence diagram | one JSON-RPC response (result or error) | "Sending Messages" 6; "Message Flow" |
| Request answered as a stream | `Content-Type: text/event-stream`; status as above | request-scoped notifications then the final response; see "SSE response streams" | same; "Receiving Messages" |
| `Origin` present and invalid | `403 Forbidden` | MAY be a JSON-RPC error response with no `id` | "Security" |
| Header validation failure (required header missing; header/body mismatch; invalid characters) | `400 Bad Request` | MUST include JSON-RPC error `-32020` `HeaderMismatch` | "Server Validation"; `schema.json` `HeaderMismatchError` |
| Requested protocol version not implemented | `400 Bad Request` | `UnsupportedProtocolVersionError` `-32022` "listing its supported versions" — `data.supported`, `data.requested` (both required per `schema.json`) | "Protocol Version Header"; `versioning`; `schema.json` |
| Request lacks a required `_meta` field | `400 Bad Request` | `-32602` Invalid params | `basic/index` "_meta": "A request missing any required field is malformed; the server **MUST** reject it with JSON-RPC error code `-32602` (Invalid params). On HTTP, the response status **MUST** be `400 Bad Request`." |
| Request needs a client capability the client did not declare | `400 Bad Request` | `MissingRequiredClientCapabilityError` `-32021` with `data.requiredCapabilities` | `basic/index` "_meta"; `schema.json` |
| RPC method not implemented | `404 Not Found` | JSON-RPC error `-32601` (`Method not found`) — "The JSON-RPC error body distinguishes this case from a `404` returned by a legacy HTTP+SSE server that does not host the modern MCP endpoint" | "Protocol Version Header" |
| GET or DELETE to the endpoint (modern-only server) | `405 Method Not Allowed` (SHOULD) | **the specification does not say** (no body or `Allow` header is specified) | "Backward Compatibility" |
| Other HTTP methods (PUT, PATCH, HEAD, OPTIONS) | **the specification does not say** | — | — |

The `-32022` example (`versioning`, reproduced also on the `schema` page):

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32022,
    "message": "Unsupported protocol version",
    "data": {
      "supported": ["2026-07-28", "2025-11-25"],
      "requested": "1900-01-01"
    }
  }
}
```

**The specification does not say** which HTTP status carries an ordinary JSON-RPC error that is
not one of the cases above — for example `-32602` for an unknown tool or an invalid cursor. Only
the four `400` cases in the table (and `404` for `-32601`) are assigned a status; a JSON-object
answer is otherwise described only by its `Content-Type`, with `200 OK` in the sequence diagram.
This is a **simulator-chosen / OWNER** point for unit 2 and is listed under "Simulation decisions".

**The specification does not say** in what order a server applies the `Origin` (403), header
validation (400) and authentication (401) checks when more than one would fail.

### Legacy traffic at a modern-only server ("Backward Compatibility")

"Protocol versions `2025-03-26` through `2025-11-25` also used the Streamable HTTP transport, but in
a different shape: servers could assign a session via the `Mcp-Session-Id` header (terminated with
HTTP DELETE), clients could open a standalone SSE stream with HTTP GET to receive server-initiated
messages, servers could send JSON-RPC *requests* on SSE streams, and streams were resumable via
`Last-Event-ID`. None of these mechanisms are part of this revision."

"A server that supports only this revision and receives such traffic from an older client
**SHOULD** respond as follows:

- HTTP GET or DELETE to the MCP endpoint: respond with `405 Method Not Allowed`.
- An `Mcp-Session-Id` header on a request: ignore it, and do not mint or echo session IDs.
- A `Last-Event-ID` header: ignore it; streams are not resumable."

An `initialize` request at a modern-only server (`versioning`, "Backward Compatibility with
Initialization-Based Versions"): "A server that supports only modern versions **SHOULD** name the
protocol versions it supports in any error it returns to an `initialize` request, on any transport:
legacy clients have no fall-forward mechanism, and this message may be the only diagnostic they can
surface to users." The compatibility matrix's Legacy-client/Modern-server row says how that arrives
on HTTP: "the request is missing the required headers and is rejected per server validation with
`400 Bad Request`" — and "Server Validation" assigns that rejection its code: `400` + `-32020`
(MUST). The same request also lacks the required `_meta` fields, which `basic/index` answers with
`400` + `-32602` (MUST). **The specification does not say** which of those two checks a server
applies first; the "exact code is implementation-defined" wording is the matrix's stdio cell only.
Either way the error SHOULD name the supported versions (`versioning`, above). The order the
profile applies is simulator-chosen and is for unit 2 to record.

The client-side detection algorithm the profile must not defeat: "A client that supports both
modern … and a legacy version … **MAY** detect which era the server implements by attempting a
modern request first. On `400 Bad Request`, the client **SHOULD** inspect the response body before
falling back … If the body contains a recognized modern JSON-RPC error, the server speaks a modern
version of MCP … If the body is empty or is not a recognized modern JSON-RPC error, fall back to
`initialize`". So a modern-only simulator's `400` bodies must be recognisable modern errors
(`-32020`, `-32021`, `-32022`), or a dual-era SDK client will fall back to `initialize`.

### SSE response streams ("Receiving Messages")

- "The server **MAY** send JSON-RPC *notifications* — for example, `notifications/progress` or
  `notifications/message` — before the final response. These notifications **MUST** relate to the
  originating client request."
- "The server **MUST NOT** send independent JSON-RPC *requests* on this stream."
- "The final JSON-RPC *response* **SHOULD** terminate the stream."
- "When initiating an SSE stream, servers **SHOULD** include the `X-Accel-Buffering: no` header in
  the HTTP response."
- Keep-alive: "For long-lived streams — in particular the `subscriptions/listen` response stream —
  servers are encouraged to periodically emit an SSE comment line (a line beginning with a colon,
  e.g. `:\r\n`) as a keep-alive … clients must ignore such lines and must not treat them as
  malformed input."
- "Resumable SSE streams via `Last-Event-ID` are not supported." `changelog` Major 9: "A broken
  response stream loses the in-flight request; clients **MUST** re-issue it as a new request with a
  new request ID".
- Cancellation ("Cancellation"; `cancellation`): "Closing the SSE response stream **MUST** be
  treated by the server as cancellation of that request … The server **SHOULD** stop work on the
  cancelled request as soon as practical and **MUST NOT** send any further messages for it."
- Request-scoped notifications "flow only on the response stream of the request they relate to"
  and are not delivered on a `subscriptions/listen` stream.

**The specification does not say** what SSE `event:` name (if any) or `id:` field a server uses,
nor how a JSON-RPC message is laid out across `data:` lines — the page gives no SSE frame example
and cites only the SSE standard. **The specification does not say** whether the keep-alive comment
is permitted on a short request-scoped stream (it is "encouraged" for long-lived ones and forbidden
nowhere).

### `Origin` and authentication

`streamable-http` "Security":

1. "Servers **MUST** validate the `Origin` header on all incoming connections to prevent DNS
   rebinding attacks. If the `Origin` header is present and invalid, servers **MUST** respond with
   HTTP 403 Forbidden. The HTTP response body **MAY** comprise a JSON-RPC *error response* that has
   no `id`."
2. "When running locally, servers **SHOULD** bind only to localhost (127.0.0.1) rather than all
   network interfaces (0.0.0.0)."
3. "Servers **SHOULD** implement proper authentication for all connections."

**The specification does not say** what to do when `Origin` is absent, nor which JSON-RPC code goes
in the optional 403 body.

`authorization` (recorded only to the extent of what a server must or may require):

- "Authorization is **OPTIONAL** for MCP implementations. When supported: Implementations using an
  HTTP-based transport **SHOULD** conform to this specification".
- When supported, the token travels as `Authorization: Bearer <access-token>` ("MCP client **MUST**
  use the Authorization request header field"; "Access tokens **MUST NOT** be included in the URI
  query string"; "authorization **MUST** be included in every HTTP request from client to server").
- "Invalid or expired tokens **MUST** receive a HTTP 401 response." Status table: `401`
  "Authorization required or token invalid"; `403` "Invalid scopes or insufficient permissions";
  `400` "Malformed authorization request". "MCP servers **SHOULD** include a `scope` parameter in
  the `WWW-Authenticate` header".
- When supported, the page also binds the server to the OAuth framework: "MCP servers **MUST**
  implement OAuth 2.0 Protected Resource Metadata (RFC9728)"; "MCP servers … **MUST** validate
  access tokens"; "MCP servers **MUST** validate that access tokens were issued specifically for
  them as the intended audience, according to RFC 8707 Section 2"; the page's 401 example carries
  `WWW-Authenticate: Bearer resource_metadata="…/.well-known/oauth-protected-resource",
  scope="…"`.
- `basic/index` "Auth": "clients and servers **MAY** negotiate their own custom authentication and
  authorization strategies."

The specification therefore requires no particular credential of a server. A static
`Authorization: Bearer` check in the profile would be a `basic/index` "custom authentication
strategy" (MAY) — **not** the OPTIONAL authorization framework, which a server opts into whole (RFC
9728 metadata document, audience validation, `WWW-Authenticate` challenge) or not at all — and
would be **simulator-chosen** (see "Simulation decisions"). **The specification does not say**
whether a 401 body should be a JSON-RPC error.

## JSON-RPC envelope and `_meta`

Source unless stated: `basic/index`; shapes cross-checked against `schema.json`.

### Message shapes

"All messages between MCP clients and servers **MUST** follow the JSON-RPC 2.0 specification."
`transports`: "JSON-RPC messages **MUST** be UTF-8 encoded." Throughout this file, `field`† marks a
field that `schema.json` lists in `required`; an unmarked field is optional there.

| Message | Fields (`schema.json` required marked †) | Rules (verbatim) |
|---|---|---|
| Request | `jsonrpc`† (`"2.0"`), `id`† (`string \| integer`), `method`† (`string`), `params` (`object`) | "Requests **MUST** include a string or integer ID." "Unlike base JSON-RPC, the ID **MUST NOT** be `null`." "The request ID **MUST NOT** match the ID of any other request the sender has issued and not yet received a response for." |
| Result response | `jsonrpc`†, `id`†, `result`† (`object`, must carry `resultType`) | "Result responses **MUST** include the same ID as the request they correspond to." "The `result` **MAY** follow any JSON object structure." "The `result` **MUST** include a `resultType` field to indicate the type of the result." |
| Error response | `jsonrpc`†, `id` (optional in `schema.json`), `error`† (`code`† `integer`, `message`† `string`, `data` any) | "Error responses **MUST** include the same ID as the request they correspond to (except in error cases where the ID could not be read due a malformed request)." "Error codes **MUST** be integers." "Error responses **MAY** include a `data` member with additional information of any type". `schema.json` `Error.message`: "The message SHOULD be limited to a concise single sentence." |
| Notification | `jsonrpc`†, `method`†, `params` | "The receiver **MUST NOT** send a response." "Notifications **MUST NOT** include an ID." |

`schema.json` `JSONRPCMessage` is `anyOf [JSONRPCRequest, JSONRPCNotification, JSONRPCResultResponse,
JSONRPCErrorResponse]` — there is no array variant.

### `_meta` — key naming rules and reserved keys

- "Certain key names are reserved by MCP for protocol-level metadata … implementations **MUST NOT**
  make assumptions about values at these keys."
- Key format: an optional **prefix** and a **name**. Prefix: "If specified, MUST be a series of
  labels separated by dots (`.`), followed by a slash (`/`). Labels MUST start with a letter and
  end with a letter or digit; interior characters can be letters, digits, or hyphens (`-`).
  Implementations SHOULD use reverse DNS notation". "Any prefix where the second label is
  `modelcontextprotocol` or `mcp` is **reserved** for MCP use." Name: "Unless empty, MUST begin and
  end with an alphanumeric character (`[a-z0-9A-Z]`). MAY contain hyphens (`-`), underscores
  (`_`), dots (`.`), and alphanumerics in between."
- Reserved keys and where each appears (types from the page's tables and `schema.json`):

| Key | Type | Required | Where | Source |
|---|---|---|---|---|
| `io.modelcontextprotocol/protocolVersion` | `string` | **Yes**, on every request | request `params._meta` | `basic/index` "Per-request protocol fields"; `schema.json` `RequestMetaObject` |
| `io.modelcontextprotocol/clientCapabilities` | `ClientCapabilities` object | **Yes**, on every request ("an empty object means the client supports no optional capabilities. Servers MUST NOT infer capabilities from prior requests" — `schema.json`) | request `params._meta` | same |
| `io.modelcontextprotocol/clientInfo` | `Implementation` (`name`† `string`, `version`† `string`, `title`, `description`, `icons[]`, `websiteUrl`) | No ("Clients **SHOULD** include … on every request") | request `params._meta` | same |
| `io.modelcontextprotocol/logLevel` | `LoggingLevel` (`debug`, `info`, `notice`, `warning`, `error`, `critical`, `alert`, `emergency`) | No | request `params._meta` | `basic/index`; `logging`; `schema.json` `LoggingLevel` |
| `progressToken` (bare key, no prefix) | `string \| integer` | No | request `params._meta` | `basic/index` reserved-keys table; `progress`; `schema.json` `ProgressToken` |
| `io.modelcontextprotocol/serverInfo` | `Implementation` | No ("Servers **SHOULD** include … in every result's `_meta`, unless specifically configured not to do so") | result `_meta` | `basic/index` "Per-response protocol fields"; `schema.json` `ResultMetaObject` |
| `io.modelcontextprotocol/subscriptionId` | `string \| integer` (`RequestId`) | MUST on every notification delivered on a `subscriptions/listen` stream; absent otherwise | notification `params._meta` | `basic/index`; `subscriptions`; `schema.json` `NotificationMetaObject` |
| `traceparent`, `tracestate`, `baggage` | W3C Trace Context / Baggage strings | No | any `_meta` | `basic/index` "OpenTelemetry trace context" |

- "`io.modelcontextprotocol/clientInfo` and `io.modelcontextprotocol/serverInfo` are self-reported
  by the sender and are not verified by the protocol … Implementations **SHOULD NOT** use them to
  change the behavior of the client or server, and **SHOULD NOT** rely on them for security
  decisions."
- Statelessness ("Statelessness"): "Servers **MUST NOT** rely on prior requests over the same
  connection to establish context (e.g., capabilities, protocol version, client identity). Every
  request supplies this metadata in its `_meta` field." "State that needs to span multiple requests
  … **MUST** be referenced by an explicit identifier the client passes on each request."

`ServerCapabilities` (`schema.json`; no `required`): `completions`, `experimental`, `extensions`,
`logging`, `prompts{listChanged}`, `resources{listChanged, subscribe}`, `tools{listChanged}`.
`ClientCapabilities` (`schema.json`; no `required`): `elicitation{form, url}`, `experimental`,
`extensions`, `roots`, `sampling{context, tools}`. `tools`: "Servers that support tools **MUST**
declare the `tools` capability".

### `resultType`

- "The `result` **MUST** include a `resultType` field". `schema.json` `Result.required` is
  `["resultType"]` and every result definition's `resultType` description reads "Servers
  implementing this protocol version MUST include this field."
- Values ("ResultType"): "`\"complete\"` indicates the request completed successfully and the result
  contains the final content"; "`\"input_required\"` indicates the request is incomplete and more
  information is needed to process the request. The result contains an `InputRequiredResult`";
  "Extensions **MAY** add additional `ResultType` values"; "A `resultType` of any value unrecognized
  by the client **MUST** be considered invalid"; "clients **MUST** treat an absent `resultType` as
  `\"complete\"`" (backward compatibility).
- In `schema.json` `ResultType` is `"type": "string"` with **no `enum`** — the two values are named
  only in the description text and on the rendered `schema` page
  (`"complete" | "input_required" | string`).

### Error codes

`basic/index` "Error Codes", verbatim in the parts that bind:

- "MCP uses the standard JSON-RPC 2.0 error codes (`-32700`, `-32600` to `-32603`) for general
  protocol failures."
- Allocation policy for `-32000..-32099`: "**`-32000` to `-32019` — legacy.** … New codes **MUST
  NOT** be allocated in this sub-range, and new implementations **SHOULD NOT** use codes from this
  sub-range at all." "**`-32020` to `-32099` — reserved for the MCP specification.** … Implementations
  **MUST NOT** emit any code from this sub-range that is not defined by this specification and
  **MUST** use defined codes only with their specified meanings."
- "Implementations of this protocol version **MUST NOT** emit these codes: `-32002` — resource not
  found (2025-11-25 and earlier; replaced by `-32602`) … `-32042` — URL elicitation required
  (2025-11-25 only)."
- "New error codes for purposes not defined by this specification **SHOULD** be allocated outside
  the JSON-RPC reserved range (`-32768` to `-32000`)".

Every named error in `schema.json` (each is `Error` with `code: const`; `data` shape only where the
schema defines one; HTTP status only where the schema or a page assigns one):

| Code | Name (`schema.json`) | `data` shape | HTTP status | Description / where it applies |
|---|---|---|---|---|
| `-32700` | `ParseError` | none defined | **the specification does not say** | "invalid JSON was received by the server … the server cannot parse the JSON text of a message." `schema` example: `{"code": -32700, "message": "Parse error: Invalid JSON"}` |
| `-32600` | `InvalidRequestError` | none defined | **the specification does not say** | "the message structure does not conform to the JSON-RPC 2.0 specification requirements for a request (e.g., missing required fields like `jsonrpc` or `method`, or using invalid types for these fields)." |
| `-32601` | `MethodNotFoundError` | none defined (the `schema` example shows `data.reason`, an example only) | `404 Not Found` on HTTP (`streamable-http`) | "a server returns this error when a client invokes a method the server does not implement — either a genuinely unknown method, or one gated behind a server capability the server did not advertise (e.g., calling `prompts/list` when the `prompts` capability was not advertised)." |
| `-32602` | `InvalidParamsError` | none defined | `400 Bad Request` only for a missing required `_meta` field (`basic/index`); otherwise **the specification does not say** | "**Tools**: Unknown tool name or invalid tool arguments; **Prompts**: Unknown prompt name or missing required arguments; **Pagination**: Invalid or expired cursor values; **Logging**: Invalid log level; …" `schema` examples: `Unknown tool: invalid_tool_name`; `Invalid arguments for tool calculate: Missing required property 'expression'`; `Invalid cursor` |
| `-32603` | `InternalError` | none defined | **the specification does not say** | "an unexpected condition that prevents it from fulfilling the request." |
| `-32020` | `HeaderMismatchError` | none defined | `400 Bad Request` (schema description: "For HTTP, the response status code MUST be `400 Bad Request`") | header/body mismatch, required header missing or malformed |
| `-32021` | `MissingRequiredClientCapabilityError` | `data.requiredCapabilities`† (`ClientCapabilities`) | `400 Bad Request` | "processing a request requires a capability the client did not declare in `clientCapabilities`" |
| `-32022` | `UnsupportedProtocolVersionError` | `data.requested`† (`string`), `data.supported`† (`string[]`) | `400 Bad Request` | "the request's protocol version is unknown to the server or unsupported (e.g., a known experimental or draft version the server has chosen not to implement)" |

`changelog` Minor 12 records the renumbering into the reserved sub-range: "`HeaderMismatch` `-32001`
→ `-32020`, `MissingRequiredClientCapability` `-32003` → `-32021`, `UnsupportedProtocolVersion`
`-32004` → `-32022`".

### Bodies that are not a single JSON-RPC request or notification

- **A JSON array (batch).** The only normative sentence is `streamable-http` "Sending Messages" 4:
  "The body of the HTTP POST **MUST** be a single JSON-RPC *request* or *notification*." Batching
  was removed in revision 2025-06-18 (`2025-06-18 changelog`, Major 1: "Remove support for JSON-RPC
  **batching**"). No 2026-07-28 page contains the word "batch", and `schema.json` `JSONRPCMessage`
  has no array variant. **The specification does not assign an HTTP status or JSON-RPC code to an
  array body.** What the simulator answers is therefore **simulator-chosen** (a top-level array is
  not conformant traffic; the nearest defined code is `-32600` `InvalidRequestError`, whose HTTP
  status is itself unassigned). Recorded for the backlog: the Phase 8 "fix JSON-RPC batch rejection
  first" item was written against 2025-03-26 and is refuted by every revision since 2025-06-18.
- **Unparseable JSON.** `-32700` `ParseError` exists in `schema.json`; **the specification does not
  say** which HTTP status carries it.
- **A JSON-RPC response sent by a client.** "The client **MUST NOT** send JSON-RPC *responses*";
  **the specification does not say** how a server answers one.
- **A JSON-RPC message with the wrong shape** (no `method`, `id: null`, wrong types): `-32600`
  `InvalidRequestError` describes it; **the specification does not say** the HTTP status.

## Methods the profile will simulate

Every request below **MUST** carry the required `_meta` fields (`io.modelcontextprotocol/protocolVersion`,
`io.modelcontextprotocol/clientCapabilities`); the `tools` and `pagination` pages omit them from
their examples "for brevity" and say so. `schema.json` makes `params` (and `params._meta`) required
on `server/discover`, `tools/list` and `tools/call`.

### `server/discover`

Source: `discover`; `schema.json` `DiscoverRequest`, `DiscoverResult`.

- "`server/discover` lets a client query a server's supported protocol versions, capabilities, and
  identity before sending any other requests. Servers **MUST** implement it." (`versioning`:
  "Servers **MUST** implement `server/discover`. Clients **MAY** call it before sending any other
  requests".)
- Request params: "The request carries no body parameters beyond the standard `_meta`". `schema.json`
  `DiscoverRequest.params` is `RequestParams` = `{ _meta* }`.
- Headers on HTTP: `MCP-Protocol-Version` and `Mcp-Method: server/discover`; no `Mcp-Name`.

Result (`DiscoverResult`, `schema.json`; `required`: `cacheScope`, `capabilities`, `resultType`,
`supportedVersions`, `ttlMs`):

| Field | Type | Required | Note |
|---|---|---|---|
| `resultType` | `string` | yes | `"complete"` |
| `supportedVersions` | `string[]` | yes | "MCP Protocol Versions this server supports. The client should choose a version from this list for use in subsequent requests." |
| `capabilities` | `ServerCapabilities` | yes | "The capabilities of the server." |
| `instructions` | `string` | no | "Natural-language guidance describing the server and its features … should not duplicate information already in tool descriptions." |
| `ttlMs` | `integer`, `minimum: 0` | yes | `CacheableResult` — see `tools/list` |
| `cacheScope` | `"public" \| "private"` | yes | `CacheableResult` |
| `_meta["io.modelcontextprotocol/serverInfo"]` | `Implementation` | no ("Servers **SHOULD** include this field") | `_meta` itself is optional |

The page's example result: `resultType: "complete"`, `supportedVersions: ["2026-07-28"]`,
`capabilities: {tools: {}, resources: {}}`, `_meta.io.modelcontextprotocol/serverInfo:
{name: "ExampleServer", version: "1.0.0"}`, `instructions`, `ttlMs: 3600000`, `cacheScope: "public"`.
"This operation supports caching" — `caching`: "Servers MUST include caching hints on results with
`resultType: "complete"` returned by … `server/discover`". Errors: nothing method-specific; the
transport-level cases apply. **The specification does not say** whether `supportedVersions` has a
required order (the `-32022` example lists newest first).

### `tools/list`

Sources: `tools` "Listing Tools", `pagination`, `caching`; `schema.json` `ListToolsRequest`,
`PaginatedRequestParams`, `ListToolsResult`, `Tool`, `ToolAnnotations`, `Icon`.

Request params (`PaginatedRequestParams`; `required`: `_meta`):

| Field | Type | Required | Note |
|---|---|---|---|
| `_meta` | `RequestMetaObject` | yes | as above |
| `cursor` | `string` | no | "An opaque token representing the current pagination position. If provided, the server should return results starting after this cursor." |

Headers on HTTP: `MCP-Protocol-Version`, `Mcp-Method: tools/list`; no `Mcp-Name`.

Result (`ListToolsResult`; `required`: `cacheScope`, `resultType`, `tools`, `ttlMs`):

| Field | Type | Required | Note |
|---|---|---|---|
| `resultType` | `string` | yes | `"complete"` |
| `tools` | `Tool[]` | yes | may be empty: the set "**MAY** be empty and **MAY** change over time … but **MUST NOT** vary per-connection or as a side effect of other requests on the connection. The set **MAY** vary by the authorization presented on the request" |
| `nextCursor` | `string` | no | "An opaque token representing the pagination position after the last returned result. If present, there may be more results available." |
| `ttlMs` | `integer`, `minimum: 0` | yes | `caching`: "Servers **MUST** provide a `ttlMs` value that is `>= 0`"; `0` = "immediately stale"; positive = "fresh for that many milliseconds"; absent → clients assume `0` ("This should only occur in older server versions"); negative → clients SHOULD ignore it and treat it as `0` |
| `cacheScope` | `"public"` \| `"private"` | yes | `"public"`: "Any client, shared gateway, or caching proxy **MAY** store and serve the cached response to any user"; `"private"`: "Caches **MUST NOT** be shared across authorization contexts". "Servers **MUST** apply the same `cacheScope` to all response pages for a given list request." |
| `_meta` | `ResultMetaObject` | no | `serverInfo` SHOULD |

`Tool` (`schema.json`; `required`: `inputSchema`, `name`):

| Field | Type | Required | Note |
|---|---|---|---|
| `name` | `string` | yes | Tool-name rules are all SHOULD (`tools` "Tool Names"): "between 1 and 128 characters"; "case-sensitive"; only `A-Z a-z 0-9 _ - .`; "**SHOULD NOT** contain spaces, commas, or other special characters"; "unique within a server". Examples: `getUser`, `DATA_EXPORT_v2`, `admin.tools.list`. A name outside the header-safe set travels in `Mcp-Name` Base64-encoded. |
| `title` | `string` | no | display name; "Display name precedence order is: `title`, `annotations.title`, then `name`" |
| `description` | `string` | no | "A human-readable description of the tool." |
| `inputSchema` | JSON Schema object; `type`† must be `"object"`; `$schema` optional; any other 2020-12 keyword allowed | yes | "**MUST** be a valid JSON Schema object (not `null`)"; "Defaults to 2020-12 if no `$schema` field is present"; no-parameter forms `{ "type": "object", "additionalProperties": false }` (recommended) or `{ "type": "object" }`; properties MAY carry `x-mcp-header` |
| `outputSchema` | JSON Schema object (`$schema` optional; not restricted to `type: object` in 2026-07-28) | no | "Optional JSON Schema defining expected output structure" |
| `annotations` | `ToolAnnotations`: `title` `string`, `readOnlyHint` `boolean` (default false), `destructiveHint` `boolean` (default true), `idempotentHint` `boolean` (default false), `openWorldHint` `boolean` (default true) | no | "clients **MUST** consider tool annotations to be untrusted unless they come from trusted servers"; `schema.json`: "all properties in `ToolAnnotations` are **hints**" |
| `icons` | `Icon[]` (`src`† `string` uri, `mimeType`, `sizes[]`, `theme` `"light" \| "dark"`) | no | `basic/index` "icons": consumers MUST treat icon URIs and bytes as untrusted |
| `_meta` | `MetaObject` | no | key-naming rules apply |

Deterministic order — quoted in full because it is Servicesim's own doctrine (`tools`
"Capabilities"; also `changelog` Minor 3): "Servers **SHOULD** return tools in a deterministic order
(i.e., the same ordering across requests when the underlying set of tools has not changed).
Deterministic ordering enables clients to reliably cache the tool list and improves LLM prompt
cache hit rates when tools are included in model context."

Pagination (`pagination`): "The **cursor** is an opaque string token"; "**Page size** is determined
by the server, and clients **MUST NOT** assume a fixed page size"; "Servers **SHOULD**: Provide
stable cursors; Handle invalid cursors gracefully"; "Clients **MUST** treat cursors as opaque
tokens … an empty string is a valid cursor and thus **MUST NOT** be treated as the end of results";
"Invalid cursors **SHOULD** result in an error with code -32602 (Invalid params)." Caching and
pagination: "each page is an independently cacheable response"; "Servers **MAY** return different
`ttlMs` values on different pages".

Errors: `-32602` for an invalid cursor (SHOULD); `-32601` if the server does not advertise the
`tools` capability (`schema.json` `MethodNotFoundError` description). **The specification does not
say** the HTTP status for either.

### `tools/call`

Sources: `tools` "Calling Tools", "Tool Result", "Output Schema", "Error Handling"; `schema.json`
`CallToolRequestParams`, `CallToolResult`, `ContentBlock` and its members, `Annotations`.

Request params (`CallToolRequestParams`; `required`: `_meta`, `name`):

| Field | Type | Required | Note |
|---|---|---|---|
| `_meta` | `RequestMetaObject` | yes | as above; `progressToken` opts into `notifications/progress` |
| `name` | `string` | yes | "The name of the tool." Mirrored into `Mcp-Name` (Base64 sentinel if not header-safe) |
| `arguments` | `object` (any properties) | no | "Arguments to use for the tool call." Argument values annotated with `x-mcp-header` are mirrored into `Mcp-Param-{name}` |
| `inputResponses` | `InputResponses` | no | MRTR retry only — NOT SIMULATED |
| `requestState` | `string` | no | MRTR retry only — NOT SIMULATED |

Headers on HTTP: `MCP-Protocol-Version`, `Mcp-Method: tools/call`, `Mcp-Name: <name>`, and any
`Mcp-Param-{name}` the tool's `inputSchema` declares.

Result (`CallToolResult`; `required`: `content`, `resultType`; no `ttlMs`/`cacheScope` — `tools/call`
is not in `caching`'s list):

| Field | Type | Required | Note |
|---|---|---|---|
| `resultType` | `string` | yes | `"complete"` (or `"input_required"` for MRTR — NOT SIMULATED) |
| `content` | `ContentBlock[]` | yes | "A list of content objects that represent the unstructured result of the tool call." May be empty per the schema (no `minItems`); **the specification does not say** whether an empty array is meaningful |
| `structuredContent` | any JSON value | no | "This can be any JSON value (object, array, string, number, boolean, or null) that conforms to the tool's `outputSchema` if one is defined." "For backwards compatibility, a tool that returns structured content SHOULD also return the serialized JSON in a TextContent block." |
| `isError` | `boolean` | no | "If not set, this is assumed to be false (the call was successful)." |
| `_meta` | `ResultMetaObject` | no | `serverInfo` SHOULD |

`ContentBlock` is `anyOf` exactly five types (`schema.json`; required marked †):

| `type` | Fields | Note |
|---|---|---|
| `text` | `type`†, `text`† `string`, `annotations`, `_meta` | "Text provided to or from an LLM." |
| `image` | `type`†, `data`† `string` (base64, `format: byte`), `mimeType`† `string`, `annotations`, `_meta` | |
| `audio` | `type`†, `data`† `string` (base64), `mimeType`† `string`, `annotations`, `_meta` | |
| `resource_link` | `type`†, `uri`† `string` uri, `name`† `string`, `title`, `description`, `mimeType`, `size` `integer`, `icons[]`, `annotations`, `_meta` | "Resource links returned by tools are not guaranteed to appear in the results of a `resources/list` request." |
| `resource` | `type`†, `resource`† = `TextResourceContents` (`uri`†, `text`†, `mimeType`, `_meta`) or `BlobResourceContents` (`uri`†, `blob`† base64, `mimeType`, `_meta`), `annotations`, `_meta` | "Servers that use embedded resources **SHOULD** implement the `resources` capability". Note: the `tools` page's Embedded Resources example nests `annotations` inside `resource`; `schema.json` puts it on the block (`TextResourceContents` has no `annotations`) — follow the schema. |

`Annotations` (optional on every content type): `audience` `("user" \| "assistant")[]`, `priority`
`number` 0..1, `lastModified` `string` ISO 8601. "All content types … support optional annotations".

`outputSchema` / `structuredContent` rules ("Output Schema"): "If an output schema is provided:
Servers **MUST** provide structured results that conform to this schema. Clients **SHOULD**
validate structured results against this schema." **The specification does not say** what happens
when `structuredContent` is present but the tool declared no `outputSchema`, nor whether a client
rejects a non-conforming result.

Protocol error versus tool execution error ("Error Handling", verbatim in the parts that bind):

1. "**Protocol Errors** indicate issues with the request structure itself that models are less
   likely to be able to fix: Unknown tool; Malformed requests (requests that fail to satisfy
   CallToolRequest schema); Server errors. They are returned as standard JSON-RPC errors" — the
   example is `{"code": -32602, "message": "Unknown tool: invalid_tool_name"}`, and `schema.json`
   `InvalidParamsError` names "Unknown tool name or invalid tool arguments" for `-32602`.
   `schema.json` `CallToolResult.isError`: "any errors in *finding* the tool, an error indicating
   that the server does not support tool calls, or any other exceptional conditions, should be
   reported as an MCP error response."
2. "**Tool Execution Errors** contain actionable feedback that language models can use to
   self-correct and retry with adjusted parameters: API failures; Input validation errors (e.g.,
   date in wrong format, value out of range); Business logic errors. They are reported in tool
   results with `isError: true`" — the example is `resultType: "complete"`, one `text` content
   block, `isError: true`.

**The specification does not say** a distinct code for "invalid arguments" versus "unknown tool"
(both are `-32602` per `schema.json`), nor which code "Server errors" take on this page (`-32603`
`InternalError` is the schema's internal-error code). Also `tools` "Security Considerations":
"Servers **MUST**: Validate all tool inputs; Implement proper access controls; Rate limit tool
invocations; Sanitize tool outputs" — the first of these is what makes argument validation against
`inputSchema` a server obligation, though the page does not say whether a schema-invalid argument is
a protocol error (`-32602`) or an `isError` result; the 2025-11-25 changelog Minor 5 said "input
validation errors should be returned as Tool Execution Errors rather than Protocol Errors to enable
model self-correction", and the 2026-07-28 text above keeps that split (validation of a value's
meaning → `isError`; a request that "fail[s] to satisfy CallToolRequest schema" → protocol error).

## Server-to-client messages the profile may emit on a response stream

Only these two notification types are in scope; both are request-scoped and travel only on the SSE
response stream of the request they relate to (`streamable-http` "Receiving Messages"; `logging`).

### `notifications/progress`

Source: `progress`; `schema.json` `ProgressNotificationParams` (`required`: `progress`,
`progressToken`).

- Opt-in: the request carries `params._meta.progressToken`; "Progress tokens **MUST** be a string or
  integer value"; "**MUST** be unique across all active requests".
- Params: `progressToken`† (the token from the request), `progress`† `number` ("**MUST** increase
  with each notification, even if the total is unknown"), `total` `number` (optional; "Omit the
  total value if unknown"), `message` `string` (optional; "**SHOULD** provide relevant human readable
  progress information"), `_meta` (`NotificationMetaObject`, optional). "The `progress` and the
  `total` values **MAY** be floating point."
- "Servers receiving a request with a progress token **MAY**: Choose not to send any progress
  notifications; Send notifications at whatever frequency they deem appropriate". "Progress
  notifications **MUST** stop after completion." "Progress notifications **MUST** only reference
  tokens that: Were provided in an active request; Are associated with an in-progress operation".
- The page's example:

```json
{
  "jsonrpc": "2.0",
  "method": "notifications/progress",
  "params": {
    "progressToken": "abc123",
    "progress": 50,
    "total": 100,
    "message": "Reticulating splines..."
  }
}
```

### `notifications/message`

Source: `logging`; `schema.json` `LoggingMessageNotificationParams` (`required`: `data`, `level`).

- **Deprecated as of 2026-07-28** (`logging`, `deprecated`, `changelog` "Deprecated" 1, SEP-2577):
  "New implementations **SHOULD NOT** adopt it"; it "remains in the specification for at least
  twelve months after this revision's release before it becomes eligible for removal". Recorded so
  the profile decision (see "Simulation decisions") is made knowingly.
- "Servers that emit log message notifications **MUST** declare the `logging` capability".
- Opt-in: "To receive log messages for a specific request, include
  `io.modelcontextprotocol/logLevel` in the request's `_meta`. The server **MUST NOT** emit
  `notifications/message` for a request that does not include this field." "When the field is
  present, the server **MAY** send `notifications/message` notifications at or above the requested
  level on the response stream of that request, before the final response."
- Params: `level`† (`LoggingLevel`: `debug`, `info`, `notice`, `warning`, `error`, `critical`,
  `alert`, `emergency` — RFC 5424 order), `data`† (any JSON), `logger` `string` (optional), `_meta`
  (`NotificationMetaObject`, optional).
- Errors: an unrecognised `io.modelcontextprotocol/logLevel` → the server **SHOULD** reject the
  request with `-32602`; internal errors `-32603`.
- "Log messages **MUST NOT** contain: Credentials or secrets; Personal identifying information;
  Internal system details that could aid attacks".

### `subscriptions/listen` — recorded, NOT planned for the profile

Source: `subscriptions`; `streamable-http` "Message Flow". "`subscriptions/listen` opens a
long-lived notification stream from the server to the client … It replaces the former
`resources/subscribe` RPC and the HTTP GET endpoint." The request carries a `notifications` filter
(`toolsListChanged`, `promptsListChanged`, `resourcesListChanged` booleans; `resourceSubscriptions`
`string[]`; all optional). "The server **MUST** send `notifications/subscriptions/acknowledged` as
the first message carrying the subscription's ID in `_meta` under
`io.modelcontextprotocol/subscriptionId`"; the ID "is the JSON-RPC ID of the `subscriptions/listen`
request". Ends when the client closes the SSE stream, or the server "**SHOULD** respond to the
original `subscriptions/listen` request with an empty result before closing the stream"; the
`cancellation` page additionally says "A server **MUST** send `notifications/cancelled` referencing
a `subscriptions/listen` request ID when it tears down that subscription stream" — the two pages
differ, and neither binds the profile while `subscriptions/listen` is not simulated. Not
planned: it is a long-lived stream of server-initiated change notifications, and the profile
simulates request/response tools; `tools/list` `ttlMs` is the freshness signal a consumer gets.

## Not simulated / out of scope

Listed rather than omitted (the `contracts/README.md` "NOT SIMULATED" discipline). No row asserts a
wire shape for what it names.

| Surface | Status | Why / what the spec says |
|---|---|---|
| Resources (`resources/list`, `resources/read`, `resources/templates/list`, `notifications/resources/*`) | NOT SIMULATED | A separate server feature; the adopter's mcp-adapter calls tools |
| Prompts (`prompts/list`, `prompts/get`) | NOT SIMULATED | As above |
| Completion (`completion/complete`) | NOT SIMULATED | Utility for argument autocompletion |
| `subscriptions/listen` and every `notifications/*/list_changed`, `notifications/resources/updated`, `notifications/subscriptions/acknowledged` | NOT SIMULATED | See above; recorded for the record |
| MRTR — `resultType: "input_required"`, `InputRequiredResult`, `inputRequests`/`inputResponses`/`requestState`; elicitation, sampling, roots | NOT SIMULATED | `changelog` Major 7 replaced server-initiated requests with MRTR; Roots and Sampling are Deprecated as of 2026-07-28 (`deprecated`); a deterministic simulator does not need a round-trip through the client |
| The tasks extension (`io.modelcontextprotocol/tasks`), MCP Apps (`io.modelcontextprotocol/ui`), any `capabilities.extensions` | NOT SIMULATED | Extensions are opt-in and negotiated via `extensions` (`versioning` "Extension Negotiation"); the core profile advertises none |
| stdio transport | NOT SIMULATED | Servicesim is an HTTP simulator; the profile's transport is Streamable HTTP only |
| Authorization flows (OAuth 2.1 discovery, registration, PKCE, scopes, `WWW-Authenticate` challenges) | NOT SIMULATED | The spec makes authorization OPTIONAL and requires only `Origin` validation of a server; whether the profile demands a bearer credential is a simulator choice — see "Simulation decisions" |
| The deprecated HTTP+SSE transport (2024-11-05; GET-first with an `endpoint` event) | NOT SIMULATED | "Deprecated since protocol version `2025-03-26`"; "New implementations **SHOULD NOT** adopt it" (`streamable-http`; `deprecated`) |
| `Mcp-Session-Id` sessions, HTTP GET stream, `Last-Event-ID` resumability, `initialize` / `notifications/initialized`, `ping`, `logging/setLevel` | NOT SIMULATED under 2026-07-28 | Removed by 2026-07-28 (`changelog`). Whether a **legacy** 2025-11-25 path is built later is D11 — see "Protocol eras" |
| Batched JSON-RPC (array body) | NOT ACCEPTED (simulator-chosen response) | Removed 2025-06-18; the body MUST be a single request or notification |

## Protocol eras — the D11 record

### Terminology and compatibility matrix

`versioning` "Terminology", verbatim:

- "**Modern**: protocol versions that convey version, identity, and capabilities as per-request
  metadata (revision `2026-07-28` and later)."
- "**Legacy**: protocol versions that establish a session with an `initialize` handshake
  (`2025-11-25` and earlier)."
- "**Dual-era**: an implementation that supports both modern and legacy versions."

"A server that wishes to support both legacy clients … and modern clients … **MAY** implement both
behaviors." "The era determination is a property of the server, not of an individual request.
Clients **SHOULD** cache the result for the lifetime of the server process (stdio) or origin
(HTTP)". "A dual-era **server** selects its behavior from how the client opens: A request carrying
modern per-request `_meta` is served statelessly according to this revision. An `initialize` request
selects legacy semantics, scoped to … the session (HTTP), as specified by the negotiated legacy
protocol version. A dual-era server **MAY** serve both eras concurrently on the same endpoint or
process."

The compatibility matrix (`versioning`, reproduced faithfully):

| Client | Server | Outcome |
|---|---|---|
| Modern | Modern | Works. `server/discover` is optional; version mismatches surface as `UnsupportedProtocolVersionError` and the client retries with a mutually supported version. |
| Modern | Legacy | Fails. The server may reject the request with an implementation-defined error, stay silent, or even process an era-ambiguous method under legacy semantics. On stdio, clients **SHOULD** send `server/discover` first to fail deterministically; the client then surfaces an actionable error to the user. |
| Dual-era | Modern | Works. The stdio probe returns a `DiscoverResult` (or `UnsupportedProtocolVersionError`); on HTTP, the first modern request succeeds or returns a modern error. The client stays modern. |
| Dual-era | Legacy | Works. stdio: the probe returns a non-modern error or times out, and the client falls back to `initialize`. HTTP: the modern request returns a `4xx` without a recognized modern error body, and the client falls back to `initialize` (and possibly further to the deprecated HTTP+SSE transport). |
| Legacy | Modern | Fails. stdio: the server rejects `initialize` with a JSON-RPC error; the exact code is implementation-defined (`initialize` is an unknown method and the request also lacks the required `_meta` fields). HTTP: the request is missing the required headers and is rejected per server validation with `400 Bad Request` (a client on the deprecated HTTP+SSE transport fails at its opening `GET` instead). Legacy clients have no fall-forward mechanism. |
| Legacy | Dual-era | Works. The server answers `initialize` and serves the client according to the negotiated legacy revision. |
| Legacy | Legacy | Works according to the legacy revision; out of scope for this document. |

### SDK evidence (release metadata read with `gh api`, 2026-08-16)

These are SDK release notes — evidence for the D11 decision, **not** authority for the wire.

| SDK | Release | Published | What the release says |
|---|---|---|---|
| go-sdk | `v1.7.0` (`releases/latest` on 2026-08-16) | 2026-07-28T13:09:53Z | "This release brings full support for protocol version **`2026-07-28`**." "The streamable HTTP transport accepts requests at protocol version `2026-07-28` only when `StreamableHTTPOptions.Stateless = true`. If you want to expose the new protocol over HTTP, set `Stateless = true`; if you want to keep stateful sessions, your clients will negotiate down to `2025-11-25`." "The new protocol is enabled by default for new clients; existing legacy clients and servers continue to work unchanged." "the SDK falls back to legacy `initialize` if discover fails." "stateless servers ignore session IDs entirely and return `405 Method Not Allowed` for `DELETE`." |
| typescript-sdk | `@modelcontextprotocol/server@2.0.0` (with `client`, `node`, `hono`, `fastify`, `express`, `core`, `codemod`, `server-legacy` `@2.0.0`, all published the same minute) | 2026-07-27T23:55:41Z | The 2.0.0 changelog: "Align the 2026-07-28 wire with the final revision (spec PR #3002): `serverInfo` moves from the `DiscoverResult` body to the result `_meta`, and the per-request envelope's `clientInfo` demotes from required to SHOULD." "Servers stamp `_meta['io.modelcontextprotocol/serverInfo']` on every 2026-era response". |
| typescript-sdk | `1.30.0` | 2026-07-27T17:54:36Z | A v1.x maintenance release; its release notes do **not** mention 2026-07-28 (bug fixes and "Validate Content-Type by parsed media type", SSE keep-alive frames "(v1.x)"). The 2026-07-28 support in TypeScript is the `2.0.0` package line, not `1.30.0`. |
| python-sdk | `v2.0.0` | 2026-07-28T13:41:36Z | "It supports the 2026-07-28 revision of the Model Context Protocol and serves every earlier revision from the same server. `pip install mcp` now installs 2.x." "v2 speaks the 2026-07-28 revision (stateless requests with no handshake, `server/discover`, `subscriptions/listen`, multi-round-trip requests) and still serves every 2025-era client from the same `MCPServer`, over Streamable HTTP and stdio, with nothing to configure." "`Client(target)` negotiates the version automatically." |

go-sdk `main` on 2026-08-16 (`mcp/shared.go`, corroboration only): `latestProtocolVersion =
protocolVersion20260728`; `supportedProtocolVersions` = `2026-07-28, 2025-11-25, 2025-06-18,
2025-03-26, 2024-11-05`; a legacy `initialize` is capped at `2025-11-25`.

What this means for D11: every official SDK's current client sends the modern protocol by default
and falls back to `initialize` only when the server's answer to its first modern request (go-sdk:
`server/discover`, per `mcp/client.go` on `main`) is not a recognised modern error. A
modern-only simulator is therefore reachable from every current SDK client; a legacy-only simulator
is reachable only from a client pinned below those releases; a dual-era simulator serves both at the
cost of building the session machinery the specification removed.

### Legacy revision 2025-11-25 — what a dual-era server would additionally speak

This subsection is a **record for a decision, not a commitment to build**. Everything here is from
the 2025-11-25 pages and `2025 schema.json` (sha256
`268a5f82ba70fd7e4b6dc4aa1e64f116f74b4d0edcb69dc046829c79dd4e97e7`); it is what a dual-era server
would speak in addition to the modern surface above.

**Lifecycle** (`2025 lifecycle`):

- "The initialization phase **MUST** be the first interaction between client and server." "The
  client **MUST** initiate this phase by sending an `initialize` request containing: Protocol
  version supported; Client capabilities; Client implementation information."
- `initialize` params (`InitializeRequestParams`; `required`: `capabilities`, `clientInfo`,
  `protocolVersion`): `protocolVersion` `string`, `capabilities` `ClientCapabilities`, `clientInfo`
  `Implementation`, `_meta` (optional; `progressToken` inside).
- `initialize` result (`InitializeResult`; `required`: `capabilities`, `protocolVersion`,
  `serverInfo`): `protocolVersion` `string`, `capabilities` `ServerCapabilities`, `serverInfo`
  `Implementation`, `instructions` `string` (optional), `_meta` (optional).
- "After successful initialization, the client **MUST** send an `initialized` notification":
  `{"jsonrpc":"2.0","method":"notifications/initialized"}`. "The server **SHOULD NOT** send
  requests other than pings and logging before receiving the `initialized` notification."
- Version negotiation: "In the `initialize` request, the client **MUST** send a protocol version it
  supports. This **SHOULD** be the *latest* version supported by the client. If the server supports
  the requested protocol version, it **MUST** respond with the same version. Otherwise, the server
  **MUST** respond with another protocol version it supports. This **SHOULD** be the *latest*
  version supported by the server. If the client does not support the version in the server's
  response, it **SHOULD** disconnect." (`2025 schema.json` `InitializeResult.protocolVersion` says
  "If the client cannot support this version, it MUST disconnect.")
- The page's example initialization error is `-32602`, `"Unsupported protocol version"`, with
  `data.supported` / `data.requested`. **The specification does not say** which HTTP status
  accompanies it.

**Streamable HTTP with sessions** (`2025 transports`):

- The endpoint "supports both POST and GET methods". The POST body "**MUST** be a single JSON-RPC
  *request*, *notification*, or *response*" (a client may POST a response to a server-initiated
  request in this revision). Notification or response body → `202 Accepted` with no body, or an
  HTTP error status; request body → `application/json` or `text/event-stream`.
- `MCP-Protocol-Version` (spelled so on the page): "the client **MUST** include the
  `MCP-Protocol-Version: <protocol-version>` HTTP header on all subsequent requests" (i.e. after
  `initialize`; the header requirement was introduced in 2025-06-18 — `2025-06-18 changelog` Major
  8). "if the server does *not* receive an `MCP-Protocol-Version` header, and has no other way to
  identify the version … the server **SHOULD** assume protocol version `2025-03-26`." "If the server
  receives a request with an invalid or unsupported `MCP-Protocol-Version`, it **MUST** respond with
  `400 Bad Request`."
- `MCP-Session-Id` (spelled so on the page, eleven times): "A server using the Streamable HTTP
  transport **MAY** assign a session ID at initialization time, by including it in an
  `MCP-Session-Id` header on the HTTP response containing the `InitializeResult`." "The session ID
  **MUST** only contain visible ASCII characters (ranging from 0x21 to 0x7E)." "If an
  `MCP-Session-Id` is returned by the server during initialization, clients … **MUST** include it in
  the `MCP-Session-Id` header on all of their subsequent HTTP requests." "Servers that require a
  session ID **SHOULD** respond to requests without an `MCP-Session-Id` header (other than
  initialization) with HTTP 400 Bad Request." "The server **MAY** terminate the session at any time,
  after which it **MUST** respond to requests containing that session ID with HTTP 404 Not Found."
  "When a client receives HTTP 404 in response to a request containing an `MCP-Session-Id`, it
  **MUST** start a new session by sending a new `InitializeRequest` without a session ID attached."
  "Clients that no longer need a particular session … **SHOULD** send an HTTP DELETE to the MCP
  endpoint with the `MCP-Session-Id` header … The server **MAY** respond to this request with HTTP
  405 Method Not Allowed, indicating that the server does not allow clients to terminate sessions."
  **The specification does not say** the success status of an honoured DELETE, nor whether the
  server echoes `MCP-Session-Id` on later responses.
- The GET stream: "The client **MAY** issue an HTTP GET to the MCP endpoint … The server **MUST**
  either return `Content-Type: text/event-stream` in response to this HTTP GET, or else return HTTP
  405 Method Not Allowed, indicating that the server does not offer an SSE stream at this endpoint."
- Resumability (`Last-Event-ID`): "Servers **MAY** attach an `id` field to their SSE events … If
  the client wishes to resume after a disconnection … it **SHOULD** issue an HTTP GET to the MCP
  endpoint, and include the `Last-Event-ID` header". Optional throughout. On a POST-initiated
  stream: "The server **SHOULD** immediately send an SSE event consisting of an event ID and an
  empty `data` field in order to prime the client to reconnect". "Disconnection **SHOULD NOT** be
  interpreted as the client cancelling its request. To cancel, the client **SHOULD** explicitly send
  an MCP `CancelledNotification`." Servers **MAY** send JSON-RPC *requests* on SSE streams.
- Batching: removed in 2025-06-18 (`2025-06-18 changelog` Major 1); the 2025-11-25 body rule above
  carries it. **The specification does not say** what status or code an array body receives in this
  revision either.

**`tools/list` / `tools/call` result shapes where 2025-11-25 differs from 2026-07-28** (schema diff
of the two files, this session):

| Field | 2025-11-25 | 2026-07-28 |
|---|---|---|
| `Result.resultType` (base) | absent | present, required |
| `ListToolsResult.resultType` | absent | required |
| `ListToolsResult.ttlMs` | absent | required (`integer >= 0`) |
| `ListToolsResult.cacheScope` | absent | required (`"public"` \| `"private"`) |
| `CallToolResult.resultType` | absent | required |
| `CallToolResult.structuredContent` | optional, `type: object` | optional, any JSON value |
| `Tool.execution` (`taskSupport`: `forbidden` \| `optional` \| `required`) | present, optional | absent |
| `ListToolsRequest.params` | optional | required (and `_meta` required within it) |
| `CallToolRequestParams` required | `["name"]` | `["_meta", "name"]` |
| `CallToolRequestParams.task` | present (tasks, experimental) | absent |
| `CallToolRequestParams.inputResponses` / `requestState` | absent | present (MRTR) |
| `_meta` on requests | free-form object with `progressToken` | `RequestMetaObject` with the required protocol keys |
| `ServerCapabilities` / `ClientCapabilities` | carry `tasks` | carry `extensions` instead |

The five content types, `Annotations`, `Icon`, `ToolAnnotations`, `Implementation`, the JSON-RPC
envelope definitions, `LoggingLevel`, `notifications/progress` and `notifications/message` params
are the same in both revisions (deep-identical after normalisation, apart from `_meta` being a
`$ref` in 2026-07-28 and doc-comment wording). The `2025 tools` page has no "deterministic order"
sentence and no `resultType`, `ttlMs` or `cacheScope` anywhere (grep count 0 on page and schema).
The 2025-11-25 tools error-handling section is the same two-mechanism split with the same `-32602`
"Unknown tool" example.

`2025 schema.json` definitions absent from 2026-07-28 (for orientation): `InitializeRequest`,
`InitializeRequestParams`, `InitializeResult`, `InitializedNotification`, `PingRequest`,
`SetLevelRequest`, `SubscribeRequest`, `UnsubscribeRequest`, `RootsListChangedNotification`,
`ElicitationCompleteNotification`, `URLElicitationRequiredError`, `ToolExecution`, `ServerRequest`,
and every `Task*` type. `initialize`, `session`, `batch` and `Last-Event-ID` occur zero times in
the 2026-07-28 `schema.json`.

## Simulation decisions the profile needs before a handler is written (OWNER / unit 2)

Each item names the contract fact that constrains it and, where there is one, a recommendation with
its reason. Nothing here is decided by this file.

1. **Era(s) served — D11 (pending owner).** Fact: `2026-07-28` is `latest`; it is stateless; every
   official SDK's current client sends it by default and only falls back to `initialize` on a
   non-modern `400` body; `2025-11-25` needs `initialize`, `MCP-Session-Id`, GET streams and
   optional resumability. **Recommendation: modern (`2026-07-28`) first** — it is the authority, it
   is stateless (which is exactly Servicesim's request/response model; nothing to hold between
   calls), and it is what every official SDK now sends. Build the legacy `2025-11-25` path as a
   follow-on unit only if the adopter's client is pinned below go-sdk `v1.7.0`, the TypeScript
   `@modelcontextprotocol/*@2.0.0` packages, or python-sdk `v2.0.0`. A dual-era server is a
   superset of both and is not free: it re-introduces the session state the profile would otherwise
   never hold.
2. **The endpoint path.** Fact: server-chosen; the specification's own example is `/mcp`. Choose in
   unit 2; the docs guard will then require the `METHOD /path` row in `contracts/README.md`.
3. **Whether the profile requires an `Authorization: Bearer` credential** like the three research
   profiles. Fact: the spec says authentication SHOULD exist and leaves the scheme to the
   deployment (authorization is OPTIONAL; when used, Bearer in the `Authorization` header, never
   the query string; invalid/expired → `401`). Fact: a server that "supports" the authorization
   framework takes on its MUSTs (RFC 9728 Protected Resource Metadata, RFC 8707 audience
   validation, the `WWW-Authenticate: Bearer resource_metadata=… scope=…` challenge — see
   "`Origin` and authentication"). A static bearer check is therefore a `basic/index` "custom
   authentication strategy" (MAY), not that framework: the profile must not advertise or
   half-implement OAuth (no PRM document, no `WWW-Authenticate` challenge) unless it takes on
   those MUSTs. Whichever is chosen is **simulator-chosen** and must be recorded as such in
   `provenance.yaml`. Fact bearing on the choice: house rule 4 —
   whatever is accepted is redacted; and consumers' contract tests want to prove they sent the
   credential.
4. **Whether `x-mcp-header` is honoured.** Fact: optional for servers, but "clients **MUST** support
   this feature", and a conforming client will send `Mcp-Param-{name}` for any annotated argument.
   If the profile's fixture tools carry no `x-mcp-header`, no `Mcp-Param-*` header is ever expected;
   if any does, the server MUST validate it (`400` + `-32020` on mismatch). **The specification does
   not say** what a terminal server does with an unrecognised `Mcp-Param-*` header (see above).
5. **Whether the profile ever answers with an SSE stream, and for which scripted outcomes.** Fact:
   the server chooses per request; a stream may carry only request-scoped `notifications/progress`
   (opt-in by `progressToken`) and `notifications/message` (opt-in by `logLevel`; the feature is
   deprecated) before the response; the response SHOULD terminate the stream; closing the stream is
   cancellation; `X-Accel-Buffering: no` SHOULD. Also **the specification does not say** the SSE
   framing (event names, `data:` layout), so a scripted stream's frame shape is simulator-chosen and
   must be recorded.
6. **The HTTP status carrying an ordinary JSON-RPC error** (`-32602` unknown tool / invalid cursor,
   `-32603`, `-32700`, `-32600`, an array body). Fact: only `-32020`/`-32021`/`-32022`/missing-`_meta`
   `-32602` are assigned `400`, `-32601` is assigned `404`; a JSON-object answer is otherwise
   described only by `Content-Type`, with `200 OK` in the sequence diagram. Simulator-chosen; record
   the choice as `kind: simulator-chosen` on the golden.
7. **The `Origin` policy.** Fact: present-and-invalid → `403` MUST; absent → **the specification does
   not say**. What "invalid" means for a localhost test simulator is a simulator choice.
8. **The tool catalogue.** A scenario concern — the tools are fixture data. Contract facts that
   bind fixture authors: names SHOULD be header-safe (`A-Z a-z 0-9 _ - .`, 1–128 chars, unique);
   `inputSchema` is JSON Schema 2020-12 with `type: "object"` at the root; `outputSchema` optional;
   `structuredContent` MUST conform to `outputSchema` when one is declared, and SHOULD also appear
   serialised in a `text` block; `tools/list` order SHOULD be deterministic; `ttlMs >= 0` and
   `cacheScope` are required on every `tools/list` page.
9. **`server/discover` values.** `supportedVersions` (`["2026-07-28"]` for a modern-only profile),
   `capabilities` (`tools: {}` at minimum; `logging: {}` only if `notifications/message` is emitted),
   `instructions`, `ttlMs`, `cacheScope`, and whether `_meta.io.modelcontextprotocol/serverInfo` is
   stamped (SHOULD). All fixture or profile constants.
10. **`Accept` and request `Content-Type` strictness.** Fact: the client MUST send `Accept` listing
    both types; **the specification does not say** the server's reaction to a violation, and says
    nothing normative about the request `Content-Type`. Whether the profile rejects (house rule 5,
    "strict about requests") is simulator-chosen.

## Open questions for the adopter

The adopter's mcp-adapter is not in this repository, so its behaviour could not be checked as part
of this verification. These are informational — under ADR-0002 the specification decides this
contract, and the answers do not change it. They do decide D11 and the unit-2 shape:

1. **Which SDK and version does your mcp-adapter use?** go-sdk `>= v1.7.0`, the TypeScript
   `@modelcontextprotocol/*@2.0.0` packages, or python-sdk `>= v2.0.0` speak `2026-07-28` by
   default; anything older speaks `2025-11-25` (or earlier) with `initialize` and needs the legacy
   path. This decides the era.
2. **Which tools does it call, and does it consume `structuredContent`** (and validate it against
   `outputSchema`) or only `content[]`?
3. **Does it open `subscriptions/listen`** (for `notifications/tools/list_changed`), or rely on
   `ttlMs`?
4. **Does it send an `Origin` header**, and what value?
5. **Does it send `progressToken` or `io.modelcontextprotocol/logLevel`** on `tools/call` — i.e.
   does it expect SSE answers?
6. **Does it send `Authorization: Bearer`**, and does it treat `401` as fatal?
7. **Does it send `Accept: application/json, text/event-stream`** on every POST, and does it handle
   both answer content types?

## Provenance

- `schema/2026-07-28/schema.json`: fetched 2026-08-16 from
  `https://raw.githubusercontent.com/modelcontextprotocol/modelcontextprotocol/main/schema/2026-07-28/schema.json`;
  sha256 `ef70b61f99b6d2e5e3b46863822eab08dff6a45bedc7a08914e0e5b133f40203`; 181474 bytes;
  byte-identical to the copy fetched earlier the same day. `main` moves, so the last commit on
  `main` touching that path at fetch time is recorded: `271ecc9accafdd9b83a3c869fa67c22953b2af80`
  (committer date 2026-07-28T16:42:34Z), from
  `gh api 'repos/modelcontextprotocol/modelcontextprotocol/commits?path=schema/2026-07-28/schema.json&per_page=1'`.
  A refresh compares a fresh fetch's hash against `provenance.yaml`'s `spec.sha256`; if it differs,
  the commit sha says which bytes this file was read against.
- The schema has no `info.version` and no `$id`; its top level is only `$schema`
  (`https://json-schema.org/draft/2020-12/schema`) and `$defs` (155 definitions). The `version` in
  `provenance.yaml` is therefore the protocol revision, which is the schema directory name.
- `schema/2025-11-25/schema.json` (legacy record only): sha256
  `268a5f82ba70fd7e4b6dc4aa1e64f116f74b4d0edcb69dc046829c79dd4e97e7`; 174323 bytes; last commit on
  `main` `c4c367f9f58296a7053f5c78a52fd02bfbb56a49` (2026-07-27T14:20:44Z). Not in the `spec:`
  block — it is not the authority.
