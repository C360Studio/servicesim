# The MCP profile

> ## Status: record of what shipped — Phase 8 unit 2, 2026-08-16, on `phase-8` (`39d5809`)
>
> This document is a **record**, not a specification. It describes the fourth provider profile, `profiles/mcp`, as
> it was built: a Model Context Protocol Streamable HTTP server, modern era only (protocol revision `2026-07-28`),
> one listener (`mcp`, default port 8084), one route (`POST /mcp`), JSON-RPC 2.0 dispatch on `body.method` to
> `server/discover`, `tools/list` and `tools/call`. **The code is authoritative** wherever this document and
> `profiles/mcp/*.go` disagree — read the source first. **Every Go block here is illustrative**, not a compiled
> contract. On any wire field, [`contracts/mcp/README.md`](../../contracts/mcp/README.md) outranks this document
> ([ADR 0002](../adr/0002-verified-contract-precedence.md)); on any simulator-chosen default, `profiles/mcp/doc.go`'s
> numbered list ("Recorded simulator-chosen defaults", 1–14) is the record this document paraphrases, and the
> contract file's "Simulation decisions" carry the same numbers with a `chosen:` line each.
>
> The last section, "Seam observations for D9 tier 2", is the factual evidence the owner asked for before deciding
> whether the provider seam is exported. It recommends nothing.

Read this together with [`package-design.md`](package-design.md) (the chassis: `provider.Handle`, `Exchange`, the
mux builder, faults, journal), [`extended-surfaces.md`](extended-surfaces.md) (the open provider registry and the
turn model MCP's catalogue drift rides on) and [`streaming.md`](streaming.md) (the SSE transport MCP's `tools/call`
answers over). Nothing in those three changed for MCP; this document says what was built on top of them.

## 1. Purpose and the era decision (D11)

The adopter's second G-3 adapter is an MCP client. Servicesim gives it what it gave the three research adapters: a
deterministic, offline server that proves the client sent the *correct request* (house rule 5), scripted from
scenario YAML, with the same journal, faults, namespaces and `testkit` as every other profile.

**"Modern" means revision `2026-07-28`** — the specification's own term (`basic/versioning`, "Terminology"):
protocol versions that carry version, identity and capabilities as per-request `_meta`, with no `initialize`
handshake, no `Mcp-Session-Id` session, no HTTP GET stream, no `Last-Event-ID` resumability, and a required
`server/discover`. **"Legacy" means `2025-11-25` and earlier**: an `initialize`/`notifications/initialized`
handshake that establishes a session. The contract file's "Protocol eras" section records both, with the
compatibility matrix and the SDK release evidence (go-sdk `v1.7.0`, TypeScript `@modelcontextprotocol/*@2.0.0`,
python-sdk `v2.0.0` all speak `2026-07-28` by default and fall back to `initialize` only when the server's answer
to their first modern request is not a recognised modern error).

Unit 2 shipped **modern only**, per the recommendation on record; **D11 itself is still the owner's** — a legacy
follow-on is built only if the adopter's client is pinned below those SDK releases. Shipping modern-only precludes
nothing:

- `handler.go` (envelope parsing, dispatch, projection selection) and `jsonrpc.go` (envelope shapes, error codes,
  the id-echo rule) are era-neutral on purpose.
- `transport.go` is where "modern-only" actually lives: the standard request-metadata headers, the Base64
  sentinel, `_meta` validation, the legacy-traffic detection. A legacy follow-on has one file to extend, and would
  add: an `initialize` result (`InitializeResult`: `protocolVersion`, `capabilities`, `serverInfo`), the
  `notifications/initialized` acceptance, `Mcp-Session-Id` minting and echo, `DELETE` termination, optionally the
  GET stream — all recorded in the contract file's "Legacy revision 2025-11-25" subsection so the decision is made
  from a record rather than a rebuild. A dual-era server re-introduces the session state this profile otherwise
  never holds; that cost is the reason the recommendation was modern first.

`supportedVersions` is the unexported package slice `["2026-07-28"]` — deliberately not exported by reference,
since an importing consumer mutating it would change every `server/discover` result and every `-32022` payload
process-wide (house rule 2).

## 2. The request lifecycle, mapped onto `provider.Handle`

Every request to the `mcp` listener enters the shared pipeline exactly as an Exa or Tavily request does:
`provider.Handle` strips the `/x/<scenario>` and `/n/<namespace>` prefixes, opens the journal entry, reads and
JSON-decodes the body (`httpx.ReadBody`, `httpx.DecodeObject`), observes credential placements, resolves the lane,
admits the namespace, and calls the handler. `provider.NewMux` gives the listener its 404 for any other path and
its 405 (with `Allow: POST`) for any other method on `/mcp` — both bodies are MCP's own, supplied through
`MuxSpec.NotFound` and `MuxSpec.MethodNotAllowed` (`errors.go`), so the fail-closed answer is JSON-RPC-shaped.

Inside the one handler, `handleMCP` (`handler.go`), the order is fixed and load-bearing:

```text
checkLegacyHeaders          Mcp-Session-Id / Last-Event-ID present → WARNING each, never a gate (decision 12)
parseEnvelope               body-too-large / malformed / not-an-object (Handle's own findings) → 400 -32700/-32600
                            empty body → 400 -32700; a client-sent JSON-RPC *response* → 400 -32600;
                            not well-formed (jsonrpc≠"2.0", no method, id null or wrong type) → 400 -32600
isNotification              no "id" key at all → 202, no body, label mcp.notification.accepted (decision 11)
legacy initialize probe     method "initialize" WITHOUT MCP-Protocol-Version+Mcp-Method → 400 -32020 naming the
                            supported versions, WARNING mcp.legacy.initialize (decision 12) — header check first
checkTransport              Accept, Content-Type, the standard headers, the sentinel, _meta — §5, fixed order
checkAuth                   optional by default; required/reject → 401 -32600, no id (decision 3)
method-shape checks         unknown method → 404 -32601; tools/list cursor → 200 -32602;
                            tools/call name missing / arguments not an object → 200 -32602
selectProjection            provider.SelectTurnFor — THIS is where the attempt is claimed
dispatch → render           server/discover | tools/list | tools/call (+ SSE when the script streams)
```

Everything above `selectProjection` responds **without consuming an attempt**. That is not incidental: the
review of the first draft found the method-shape checks running *after* the projection had been selected, and
moved them. `provider.SelectTurnFor`'s own contract (`provider/turn.go`) is "call it only once the request has
passed validation: it claims an attempt, and a rejected request must not consume one" — and because
`Exchange.Fault` is memoised on first call, claiming even a moment before one of these checks runs would apply a
scripted fault's status to a response the request never earned. An unknown method, an invalid cursor and a
missing tool name are validation rejections exactly like a missing header. The shared-pipeline follow-up this
surfaced (`provider/handle.go` reads the memoised decision regardless of `Response.FaultEligible`, so "validation
has the last word" holds by convention, not by a gate) is recorded in `docs/adopter-backlog.md`, Phase 8.

Two dispatch outcomes *are* fault-eligible, on purpose: the unknown-tool `-32602` and the internal `-32603`. By the
time they run the request has passed every check and the attempt is legitimately claimed, so they carry a scripted
fault through the fixed `-32603` envelope like a rendered success would (`tools.go`, `errors.go`).

## 3. The projection model

One `Projection` serves every method, because **a turn is a server state** (abridged from `render.go`; the yaml
tags are omitted here — the tag names are the seven projection keys listed below):

```go
type Projection struct {
	Instructions string                      // server/discover instructions (optional)
	TTLMs        *int64                      // ttlMs on discover and tools/list; nil → DefaultTTLMs (60000)
	CacheScope   string                      // cacheScope; "" → DefaultCacheScope ("private")
	Tools        []ToolProjection            // tools/list, in declaration order — the order always answered in
	Results      map[string]ResultProjection // tools/call outcomes, keyed by tool name
	Stream       scenario.StreamScript       // the tools/call SSE script (decision 5)
	ExtraFields  scenario.ExtraFields        // merged into EVERY result body this projection renders
}
```

- **Catalogue drift is turns.** `tools/list` on call 0 lists one tool and on call 1 lists two because turn 0 and
  turn 1 are two projections — not because the profile has any notion of a mutable catalogue. The `conversation`
  built-in scripts exactly this; `examples/mcp_test.go` proves a client re-lists after a `-32602`.
- **Per-method cursors** are one `turn_key` away: `turn_key: [route, "body_json:method"]` gives each JSON-RPC
  method its own call index; `["route", "body_json:params.name"]` gives each tool its own. Nothing in the
  scenario package was added for this — `when.body_json` already took nested dotted paths.
- **The seven projection keys** — `instructions`, `ttl_ms`, `cache_scope`, `tools`, `results`, `stream`,
  `extra_fields` — are the set `scenarios/scenarios_test.go`'s `documentedProjectionKeys` pins against
  `docs/scenario-schema.md`'s `### mcp` table.
- **`results:` versus `tools:`.** A key in `results` naming no declared tool is legal (a hidden tool); a declared
  tool with no `results` entry renders `isError: true` with one text block saying so and a WARNING
  (`mcp.tool.unscripted`), never silently; a name in neither is the `-32602` unknown-tool error.
- **Source references on content blocks.** A `type: text` block may carry `source: <id>` instead of `text:`; it
  resolves to the source's own `text`, falling back to its `title` — never the other way, because a hostile
  fixture's malicious-content markers live in `Text`. Resolution is a dedicated pass, `resolveContentSources`,
  called by both `Validator` and `selectProjection`, **not** `scenario.ResolveRefs`'s reflection walk:
  `ContentBlock` is decoded from a slice of tagged-union mappings, none of which is a bare `scenario.SourceRef`, and
  an inlined `SourceRef` there hits the same yaml.v3 whole-mapping-Unmarshaler collision `profiles/tavily`'s
  `rawResultProjection` documents.
- **Zero configuration.** A scenario with no `mcp` block at all renders a well-shaped empty server (empty
  discover, `"tools":[]`, unknown tool for any call); a block that declares turns none of which match is an
  error, matching the sibling providers' rule.

## 4. The JSON-RPC envelope

`jsonrpc.go` decodes the request envelope **field by field into raw tokens** (`rawEnvelope`, every field a
`json.RawMessage`), then judges validity afterwards, so each shape problem is its own finding rather than one
opaque decode error. Two consequences the consumer can see on the wire:

- **`id` is echoed as the raw token** (decision 14). It is never decoded and re-encoded, so `1` never becomes
  `1.0` or `"1"`. The same treatment applies to `progressToken`. Its shape is judged, not converted: a string or a
  plain integer literal passes; `null`, `1.5`, `1e3`, a boolean, an object or an array is a `400` `-32600`,
  because `schema.json`'s `RequestId` is `string | integer` and MCP's own rule is "MUST NOT be null".
- **An unattributable error OMITS the id member — never `"id": null`.** Every 400 the id could not be read from
  — the `-32700`/`-32600` body-shape failures in `parseEnvelope` — plus the 401, the 405 and the unmatched-route
  404 render `{"jsonrpc":"2.0","error":{...}}` with no `id` key (`nullID` is a nil `json.RawMessage`;
  `errorEnvelope.ID` is `omitempty`). The header, `_meta` and version 400s (`-32020`, `-32022`, `-32602`, and the
  `-32600` for a wrong `Accept`/`Content-Type`) are raised after the envelope parsed, so they echo the id.
  `schema.json`'s `JSONRPCErrorResponse.id` is optional but, when present, `RequestId`-typed with no null
  variant, so a JSON null would be schema-invalid — and it
  would defeat the specification's client-side era-detection algorithm, which inspects a `400` body for a
  "recognized modern JSON-RPC error" before falling back to `initialize`. A first draft wrote `"id": null`; the
  golden/schema-fidelity review lens caught it against `jq` on the live schema.

Every result carries `resultType: "complete"` and `_meta["io.modelcontextprotocol/serverInfo"]` =
`{name: "servicesim", version: "1"}` — `ServerName`/`ServerVersion` are Go constants, so the bytes are identical
across builds (decision 9). `server/discover` answers `supportedVersions: ["2026-07-28"]`, `capabilities:
{"tools":{}}` exactly (no `listChanged`, no `logging`), `instructions` from the projection when set, and
`ttlMs`/`cacheScope` from the projection with the two defaults above.

## 5. The transport layer (`transport.go`)

The era-specific file. `checkTransport` runs its checks in a **fixed, simulator-chosen order** — the specification
does not say in what order a server applies header, `_meta` and authentication checks when several would fail,
and several of these violations carry a distinct `data` payload that a generic "first finding wins" renderer
would have nowhere to hang. The order, from the code (decision 13's own paragraph in `doc.go` says the same):

1. `Accept` must list both `application/json` and `text/event-stream` (parameters and whitespace ignored) →
   else `400` `-32600` naming the header, `mcp.request.accept_invalid`.
2. Request `Content-Type` must be exactly `application/json` (parameters ignored; narrower than the shared
   `+json` tolerance) → else `400` `-32600`, `mcp.request.content_type_invalid`. Neither check applies to a
   notification, whose header requirements the specification leaves undefined (decision 10).
3. Any `Mcp-Param-*` header → WARNING `mcp.header.param_ignored`, one per distinct header (decision 4).
4. Required standard headers present: `MCP-Protocol-Version`, `Mcp-Method`, and `Mcp-Name` for `tools/call` →
   else `400` `-32020` listing what is missing, `mcp.header.required`.
5. `Mcp-Method` equals the body's `method`, case-sensitively → else `400` `-32020`, `mcp.header.mismatch`.
6. On `tools/call`: `Mcp-Name` is decoded through the Base64 sentinel (`=?base64?…?=`, standard alphabet, outer
   markers only — a decoded value that itself looks sentinel-shaped is never decoded twice); undecodable → `400`
   `-32020` "invalid characters", `mcp.header.invalid_characters`; decoded value ≠ `params.name` (when `name` is a
   string) → `400` `-32020`, `mcp.header.mismatch`. On any other method a present `Mcp-Name` is a WARNING,
   `mcp.header.name_unexpected`.
7. Required `_meta`: `io.modelcontextprotocol/protocolVersion` (as a string) and
   `io.modelcontextprotocol/clientCapabilities` (present) → else `400` `-32602` naming the field(s),
   `mcp.meta.required`. Absent `clientInfo` → WARNING `mcp.meta.client_info_missing`.
8. Header `MCP-Protocol-Version` equals `_meta.protocolVersion` → else `400` `-32020`, `mcp.header.mismatch`.
9. That agreed value is in `supportedVersions` → else `400` `-32022` with `data.supported`/`data.requested`,
   `mcp.version.unsupported`.

The order decides which finding a mismatch produces: a missing header is `-32020` regardless of `_meta`; a present
header with an absent `_meta.protocolVersion` is the missing-`_meta` `-32602`, because step 7 runs before step 8;
only once both are present does a disagreement become `-32020`; only once they agree does an unknown value become
`-32022`. Header names compare case-insensitively (`net/http` folds them); values compare case-sensitively.

`hasModernHeaders` (both `MCP-Protocol-Version` and `Mcp-Method` present) is read only by the `initialize` probe
in §2. A legacy `initialize` *with* valid modern headers and `_meta` falls through to ordinary dispatch, where
`initialize` is simply not one of the three methods: `404` `-32601` with a message that also names the supported
versions — the one message the specification specifically SHOULDs, because a legacy client that got this far has
no other diagnostic to show a human.

## 6. Status policy

| Case | Status | Code | Why |
|---|---|---|---|
| Method-level error after a well-formed request: unknown tool, invalid params, invalid cursor; internal render failure | `200` | `-32602`; `-32603` | The specification assigns no status. `200` is the JSON-RPC-over-HTTP convention **and** keeps `400` reserved: the client era-detection algorithm treats a `400` body as an era signal (decision 6). |
| Request-shape failure: unparseable/empty/too-large body; not a JSON-RPC request or notification (array, scalar, wrong `jsonrpc`, missing `method`, bad `id`); a client-sent response; wrong `Accept`/`Content-Type` | `400` | `-32700`; `-32600` | Simulator-chosen; every `400` body is a recognisable modern JSON-RPC error. A too-large body is `-32700`: this build could not read enough of it to know what it was. |
| The four spec-assigned `400`s | `400` | `-32020`, `-32022`, missing-`_meta` `-32602`; `-32021` declared but unreachable | Assigned by the specification; kept. |
| Unknown method | `404` | `-32601` | Assigned by the specification. |
| Any path but `/mcp` | `404` | `-32601`, no id | The listener's fail-closed refusal, JSON-RPC-shaped, naming the one path it serves. |
| Any method but POST on `/mcp` | `405` + `Allow: POST` | `-32600`, no id | Decision 6; the specification SHOULDs 405 for GET/DELETE and says nothing about a body. |
| Missing/mismatched credential when the scenario requires one | `401` | `-32600` "authorization required", no id, no `WWW-Authenticate` | Decision 3 — never a half-implemented OAuth challenge. |
| Notification accepted | `202`, no body | — | Decision 11. |
| Scripted fault with an error status | the scripted status | `-32603` `servicesim scripted fault: <kind>` | §8. |

## 7. SSE — the streamed `tools/call`

The shared stream path (`streaming.md`) carries it; what MCP decides is:

- **The script decides, not the request — and the ENTRY's script, read from turn 0.** `wantsStream(entry)`
  (`tools.go`) reads turn 0's effective policy through `streamPolicy(entry)`, the same rule
  `profiles/perplexity.streamPolicy` applies and the rule `scenario.StreamScript` documents ("read from turn 0
  only"); the *selected* turn's script still supplies the deltas and paces. The client has no field that asks
  MCP to stream — the server decides unilaterally — so `reject` has nothing to reject and is a load ERROR
  (`mcp.stream.reject_meaningless`). The load-time guard (`ValidateProjections` + the shared
  `scenario.ValidateStreamScripts`) makes every divergent shape an ERROR — deltas on a non-streaming entry, a
  streaming entry with a delta-less turn — except one: an explicit `when_requested` on a later turn with no
  deltas, which draws the WARNING `scenario.stream.policy.ignored` ("read from the first turn only; this value is
  ignored"). Review of this record found an earlier draft of `wantsStream` reading the *selected* turn's policy
  and honouring exactly that warned value at request time; unit 3 fixed it to turn 0 and pinned the case with
  `TestStreamPolicyIsReadFromTurnZero`, which fails against the earlier draft.
- **`tools/call` only.** `server/discover` and `tools/list` always answer JSON, whatever the policy.
- **Progress frames are gated by `progressToken`** — the specification's MUST NOT. With a token: one
  `notifications/progress` frame per scripted delta, `params: {progressToken: <echoed raw>, progress: i+1,
  total: len(deltas), message: delta text}`, then the final response frame. Without: the response frame alone.
- **Framing** (the specification is silent): `GrammarDelta` with `OmitDone: true` — unnamed `data:` frames, one
  `data:` line per line of JSON, blank line after each, no `event:`, no `id:`, no `[DONE]`. Headers:
  `provider.StreamHeader()` plus `X-Accel-Buffering: no` (the specification SHOULDs this one, so this profile adds
  it where the shared helper deliberately does not).
- `resp.Body` is always the JSON-object answer for the same turn, streamed or not; the final SSE frame *is* those
  bytes.
- Faults and journal semantics are inherited unchanged: `stream_disconnect`/`stream_truncate_chunk`/
  `stream_stall` apply as on Perplexity; the entry is journaled before the first byte and amended at close;
  `sim.AwaitStreamClosed` is the wait. The retained `outcome.stream.grammar` reads `chat_completions` — the wire
  label of the framing MCP shares with Sonar, not a claim of an OpenAI dialect (`doc.go` item 5, accepted as-is).

## 8. Faults

Every `Response` this profile builds after the attempt is claimed registers `faultBody(id)`, so a scripted fault
renders the fixed shape:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"servicesim scripted fault: status"}}
```

with the id echoed (or the member omitted when unknown), and two overrides an attempt may supply, in this order:
its own `body:` replaces the whole envelope outright (decision 7 — how a scenario proves a client's `403`), and its
own `error:` replaces only the message text (the shared `scenario.FaultAttempt` grammar every other provider
package honours; how `rate-limited`, `brownout` and `server-error` put "Rate limit exceeded." on the wire). An
attempt with a status below 400 keeps the scenario's own rendered body. A consumer's retry logic keys on the HTTP
status: a scripted `429`/`503` and an unrelated internal error carry the same `-32603`.

## 9. Redaction (house rule 4)

The profile's own code interpolates only decoded values, and every one of them (`params.name`, an unknown-tool
finding, a header-mismatch finding) goes through the ordinary `redact.String`/`journal.Redact` path. House rule 4
was **broken at the shared journal-header boundary** by two MCP-defined header families the profile recognises but
`internal/redact` did not: `Mcp-Param-{name}` (a mirrored tool argument — `Mcp-Param-Token`) and `Mcp-Session-Id`.
The one framework change unit 2 made is `internal/redact.stripMirrorPrefix`: `IsCredentialHeader` now also judges
the plain name a wrapper header mirrors, so those mask exactly as their bare names would (`Mcp-Param-Query` does
not; `Mcp-Param-Api-Key` does). It affects any profile's journal if such a header were ever sent to it.

The recorded boundary: a **sentinel-encoded** `Mcp-Name` carrying a credential-shaped tool name is masked in every
finding (the decoded value) but survives in the retained header in its Base64 form — `internal/redact` has no
sentinel awareness, being generic. Documented with a test (`TestMcpNameSentinelDecodedValueIsRedactedHeaderIsOpaque`)
rather than closed: no client-shaped input puts a real credential in a tool name, and the same opacity exists for
any free-text field on any profile.

## 10. Journal labels and finding codes

Response labels (`Response.Label`, journaled as `outcome.label`): `mcp.server_discover.ok`, `mcp.tools_list.ok`,
`mcp.tools_call.ok`, `mcp.tools_call.stream`, `mcp.notification.accepted`, `mcp.tools_list.error.invalid_cursor`,
`mcp.tools_call.error.name_required`, `mcp.tools_call.error.arguments_invalid`,
`mcp.tools_call.error.unknown_tool`, `mcp.error.parse`, `mcp.error.invalid_request`, `mcp.error.400`,
`mcp.error.401`, `mcp.error.method_not_found`, `mcp.error.internal`; the composition layer's `/x/<unknown>`
refusal is `mcp.scenario.unknown`.

**Undocumented until Phase 10 unit 2, corrected here:** an unmatched path and an unsupported method no longer
journal `mcp.error.not_found`/`mcp.error.method_not_allowed`. `provider.Profile.Handler` now builds these two
refusals itself (`provider/mux.go`'s `notFound`/`methodNotAllowed`), on every listener, and labels them the
framework's own `route.not_found`/`route.method_not_allowed` — `Profile.ErrorBody`'s signature is
`func(Refusal) []byte`, bytes only, so a per-vendor Label has no path back onto the wire through it.
`profiles/mcp/errors.go`'s `notFoundResponse`/`methodNotAllowedResponse` still carry the old strings on their own
`Response.Label` field for symmetry with this file's other `statusResponse` calls, but that field is unreachable
here: only `.Body` is read. Wire bytes are unchanged; only `outcome.label` in the journal differs. The other three
profiles' 404/405 labels changed the same way (`exa.error.NOT_FOUND` → `route.not_found`, etc.) — this is a
framework-wide simplification, not an MCP-specific regression.

Finding codes (`profiles/mcp/request.go`; every one exported so a consumer's test asserts on the code, not on a
status the simulator could produce for another reason):

| Code | Severity | When |
|---|---|---|
| `mcp.request.parse_error` | error | Body unparseable, empty, or too large to read in full |
| `mcp.request.invalid` | error | Parsed JSON that is not a well-formed JSON-RPC request or notification |
| `mcp.request.is_response` | error | Body carries `result` or `error` — a client-sent response |
| `mcp.request.accept_invalid` | error | `Accept` missing or not listing both media types |
| `mcp.request.content_type_invalid` | error | Request `Content-Type` not `application/json` |
| `mcp.header.required` | error | `MCP-Protocol-Version`, `Mcp-Method` or (`tools/call`) `Mcp-Name` missing; also the legacy `initialize` probe |
| `mcp.header.mismatch` | error | A header value disagrees with the corresponding body value |
| `mcp.header.invalid_characters` | error | A sentinel-wrapped `Mcp-Name` that does not decode |
| `mcp.version.unsupported` | error | Header and body agree on a version that is not `2026-07-28` |
| `mcp.meta.required` | error | `_meta.protocolVersion` or `_meta.clientCapabilities` missing |
| `mcp.meta.client_info_missing` | warning | No `_meta.clientInfo` (a SHOULD) |
| `mcp.header.name_unexpected` | warning | `Mcp-Name` on a method that does not use one |
| `mcp.header.param_ignored` | warning | Any `Mcp-Param-*` header — `x-mcp-header` is not honoured |
| `mcp.legacy.session_id` | warning | `Mcp-Session-Id` present; ignored, never minted or echoed |
| `mcp.legacy.last_event_id` | warning | `Last-Event-ID` present; ignored, streams are not resumable |
| `mcp.legacy.initialize` | warning | A legacy-shaped `initialize` reached a modern-only server |
| `mcp.method.unknown` | error | Method other than the three implemented |
| `mcp.auth.missing` | error | No accepted credential while the scenario requires one |
| `mcp.auth.mismatch` | error | Wrong `expect_key`, or any credential under `mode: reject` |
| `mcp.auth.wrong_placement` | warning | `Authorization` with a scheme other than Bearer (still authenticates) |
| `mcp.cursor.invalid` | error | `tools/list` names a cursor; this profile serves one page |
| `mcp.name.required` | error | `tools/call` with no `params.name` |
| `mcp.arguments.invalid` | error | `params.arguments` present but not an object |
| `mcp.tool.unknown` | warning | `tools/call` names a tool in neither `tools` nor `results` |
| `mcp.tool.unscripted` | warning | A declared tool with no `results` entry |
| `mcp.projection.invalid` | error | A projection body this package cannot decode (load, or a hand-built scenario) |
| `mcp.projection.unresolved` | warning | A `source:` naming no source, seen at request time |
| `mcp.render.failed` | — | Declared for a render failure; not raised by any path today (`-32603` answers without it) |
| `mcp.tool.name_invalid` | warning (load) | Tool name outside `^[A-Za-z0-9_.-]{1,128}$` |
| `mcp.tool.name_duplicate` | error (load) | Two tools of one entry share a name |
| `mcp.tool.input_schema_invalid` | error (load) | `input_schema` not an object with `type: "object"` at the root |
| `mcp.tool.x_mcp_header_unsupported` | error (load) | `x-mcp-header` anywhere in a tool's `input_schema` |
| `mcp.tool.output_schema_unchecked` | warning (load) | `output_schema` declared and `structured_content` scripted; not validated |
| `mcp.stream.reject_meaningless` | error (load) | `stream.when_requested: reject` |
| `mcp.source.unknown` | error (load) | A content block's `source:` names no declared source |
| `mcp.content.type_unknown` | error (load) | A content block `type` not one of the five |

## 11. What is NOT simulated, and why

- **`x-mcp-header`** — not honoured; a fixture tool declaring it is rejected at load
  (`mcp.tool.x_mcp_header_unsupported`), so no scenario can believe it is validated, and any `Mcp-Param-*` header
  is ignored with a WARNING. The specification does not say what a terminal server does with an unrecognised
  `Mcp-Param-*` header (decision 4).
- **`Origin` validation** — absent: nothing; present: journaled as an ordinary header, no finding. A scenario that
  wants a `403` scripts it through `fault: {status: 403, body: {...}}` (decision 7).
- **Resources, prompts, completion, `subscriptions/listen` and every `notifications/*/list_changed`, MRTR
  (`resultType: "input_required"`), elicitation/sampling/roots, the tasks and apps extensions, `notifications/message`
  (deprecated in `2026-07-28`), the OAuth authorization framework, stdio, the deprecated HTTP+SSE transport** —
  listed with reasons in the contract file's "Not simulated / out of scope" table.
- **The legacy `2025-11-25` era** — D11, §1.
- **JSON Schema validation of `structuredContent` against `outputSchema`** — no JSON Schema validator in stdlib and
  none is worth the dependency budget; a load-time WARNING says so once per such tool.
- **Pagination** — one page always; any cursor is `-32602`.

Two nits recorded rather than fixed (see the backlog): the `202` for a notification carries a
`Content-Type: application/json` header on an empty body; `extra_fields` is envelope-level only (no per-tool or
per-content-block extras). Two more were found while writing and reviewing this record and were fixed in unit 3
rather than left: the composition layer's `/x/<unknown>` refusal for the `mcp` listener
(`internal/server/listeners.go`, `scenarioNotFoundBody`) rendered `"id":null` where every body the profile itself
builds omits the member — it now omits it too, pinned by exact bytes in `TestUnknownScenarioFailsClosed`; and
`wantsStream` read the selected turn's stream policy rather than turn 0's (§7), fixed and pinned by
`TestStreamPolicyIsReadFromTurnZero`.

## 12. Seam observations for D9 tier 2

> **Superseded by Phase 10 (2026-08-17) — kept as the evidence the decision was made on.** D9 tier 2 was decided
> yes on this section's numbers: every enumeration site counted below now derives from the registered
> `*provider.Set`, `internal/faults` is gone, `testkit`'s per-vendor handler constructors are one generic
> `testkit.Handler`, and the exports the last paragraph says are missing all shipped. Read what follows as the
> before picture. [ADR 0003](../adr/0003-framework-seam.md) is the record of what replaced it.

What the fourth in-tree profile actually needed from the composition layer, counted from `git show --stat 39d5809`
and the seam survey the unit was scoped from. Facts only; the D9 proposal frames the question.

**Needed no change at all** — the framework held:

- `scenario`: `providers:` is an open registry; `when.body_json` already takes nested dotted paths; `turn_key`
  already accepts `body_json:<path>`; `StreamScript`, `ExtraFields`, `AuthPolicy` were reused as-is.
- `provider`: `Handle`, `Exchange`, `NewMux`/`MuxSpec`, `SelectTurnFor`, `TurnFault`, `Response.FaultBody`,
  `Stream`/`EncodeSSE`/`GrammarDelta`, `StreamHeader` — reused as-is.
- `internal/faults`, `internal/journal`, `internal/jobs`, `internal/httpx`, `internal/wire`, `internal/admin`: no
  change.
- `testkit` assertions (`AssertNoFindings`, `AssertBearerAuth`, `AssertNoCredentialLeak`, `AssertGoldenSSE`,
  `AwaitStreamClosed`, `AssertNamespacesIsolated`, …): no change. `Sim.BaseURLs`/`Namespace.BaseURLs` derive
  `MCP_BASE_URL` from the name with no mapping table.

**The one framework change:** `internal/redact.stripMirrorPrefix` (§9), plus its test — a hardening any profile's
journal benefits from, not an MCP feature.

**Hand-maintained enumeration sites a fourth in-tree profile touched** (code, scripts, image — tests and documents
listed separately):

| File | Sites |
|---|---|
| `provider/provider.go` | 1 — the `Name` constant |
| `internal/config/config.go` | 10 — port default, `DefaultProviders`, `allProviders`, `Config` field, env binding, raw flag target, flag registration, `assemble`, the validate port table, the `listener()` switch |
| `internal/server/listeners.go` | 4 — import, `newSurfaces` listener selection, `newProviderHandler`, `scenarioNotFoundBody`'s vendor-shaped `/x/<unknown>` body |
| `internal/server/server.go` | 3 — import, the routes concat, the entry-kind validators map |
| `testkit/server.go` | 7 — import, `allProviders`, `routes`, `validators`, the `build` switch, a new exported constructor `MCPHandler`, the `BaseURLs` doc comment |
| `contracts/contracts.go` | 3 — embed line, constant, `Providers()` |
| `contracts/provenance_internal_test.go` | 1 — the `byName` map (a test, but a hand-maintained enumeration) |
| `scenarios/scenarios_test.go` | 3 — `implementedProviders`, `documentedProjectionKeys`, the unknown-provider expected list |
| `Dockerfile` | 2 — `EXPOSE`, the description label |
| `docker-compose.example.yml` | 2 — the port mapping, `MCP_BASE_URL` |
| `scripts/image-smoke.sh` | 4 — port variable, `-p`, a `server/discover` check, the per-provider journal loop |
| `cmd/servicesim/main.go` | 1 — the help banner |
| **Total** | **41 sites in 12 files** (imports counted wherever a file gained one) |

Plus: an `mcp:` block in **all 20** built-in YAML files (`TestBuiltins_CoverEveryImplementedProvider` forces one in
every built-in; `TestMaliciousContent_EveryHostileSourceReachesEveryProvider` forces every implemented provider's
turns to reference every hostile source, and `TestMaliciousContent_WireResponsesCarryMarkersVerbatim` checks the
markers on the wire); test-expectation updates in `internal/config/config_test.go`,
`internal/server/server_test.go`, `testkit/server_test.go`, `contracts/contracts_test.go`; a doc note in
`testkit/golden.go`; and seven documents (README, CLAUDE.md, troubleshooting, scenario-schema, the plan, the
backlog, `contracts/README.md`) updated by hand — `check-docs` verifies that what they name exists (flags, routes,
`testkit.`/`provider.` symbols) and that the contracts index matches both ways, but does not force them to mention
a new listener or to agree on its port or base-URL variable.

**Constraints the fourth profile hit:**

- `provider.NewMux` keys handlers by pattern (`MuxSpec.Handlers` map, one `inner.Handle` per route), so two routes
  sharing one pattern collide: a one-route/many-method profile dispatches in-handler on `body.method`. That was the
  right shape for MCP anyway (the specification's single endpoint) but it is a property of the mux, not a choice.
- `testkit/golden.go`'s `derivedIDPaths` prunes a top-level `id` by default (for Perplexity's sake). MCP's `id` is
  the client's, echoed, and not attempt-varying, so the default pruning is harmless — recorded, and
  `GoldenExactIDs` restores it — but a profile whose top-level `id` *mattered* would have had to change the
  shared list.
- `TestEveryProviderHasHappyAndEmptyAndErrorGoldens` requires goldens named for a happy, an empty and an error case
  under `contracts/<name>/`; `TestEveryProviderHasSpecRecorded` requires a `spec:` block; check-docs §6 checks the
  `contracts/README.md` index table in both directions. All three fired, correctly, during unit 2.
- `contracts.VerifiedOn` is the oldest per-entry `verified:` date across every provider, so a new provider's
  entries participate in a global. *(Phase 10, 2026-08-17: that global is gone — a framework cannot compute "the
  oldest date across every provider" over profiles it does not know. `contracts.OldestVerified(fsys)` answers the
  same question for one bundle's own filesystem, which is what an out-of-tree profile can run.)*

**What an out-of-tree profile would need that is not exported today** (from the survey and the D9 proposal, not
re-derived here): a `provider.Faults` constructor taking a route set (`internal/faults.New(s, routes, …)` exists;
`testkit.NewFaults(s)` hardcodes the four in-tree `Routes()`); the composition switches above, which are all
`internal/` or in `testkit`'s unexported plumbing; the aliases testkit already re-exports for `internal/faults`,
`internal/journal`, `internal/jobs`. The scenario package, the `provider` seam and the assertions it would build on
are already exported and, on this evidence, sufficient. Whether that export is worth house rule 7's cost is the
owner's decision — [`../proposals/d9-framework-framing.md`](../proposals/d9-framework-framing.md), "Evidence from
Phase 8".
