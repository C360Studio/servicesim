# Perplexity Agent contract audit, 2026-10-01

An audit of the Perplexity profile's Agent surface (`POST /v1/agent` and its aliases `POST /v1/responses` and
`POST /responses`) against the vendor's current OpenAPI document, and a record of what was changed because of it.
It is the Perplexity half of issue #8. Sonar, the retrieve/cancel/files routes and the `background` lifecycle
(issue #6) are out of scope, and are named here only where the spec says something a later unit will need.

The verified contract wins (house rule 1). Where this record and `profiles/perplexity/contracts/README.md`
disagree, the README is the standing statement and this record is the dated evidence it was corrected from.

## Baseline

| Item | Value |
|---|---|
| Document | Perplexity AI API, OpenAPI 3.1.0 |
| URL | <https://docs.perplexity.ai/openapi.json> |
| Fetched | 2026-10-01 |
| `info.version` | `1.0.0` (`#/info/version`), unchanged from the previous record |
| Size | 208,564 bytes (the previous record: 176,997) |
| sha256 | `e0b92edf13c7596c5f830e7e61a30af2c534b878a7a505953bbd6829382c3981` |
| Previous sha256 | `95305c44ed99cf4e51463de55994b3bd26063194b78668d5e8753534ee3551ab` (fetched 2026-08-16) |

JSON pointers below are written against that document. `S` abbreviates `#/components/schemas`, and path pointers
escape `/` as `~1`, so `#/paths/~1v1~1agent/post` is `POST /v1/agent`.

### Reproducing it

The audited bytes are not committed (208 KB of someone else's document). Fetch, hash, and compare:

```bash
curl -sS -o openapi.json https://docs.perplexity.ai/openapi.json
shasum -a 256 openapi.json     # expect the sha256 above
jq '.paths["/v1/agent"].post.responses | keys' openapi.json     # ["200","400"]
jq '.components.schemas.ResponsesRequest.properties | keys | length' openapi.json     # 19
```

A different hash means the vendor moved again after this audit. Treat that as drift, not as a failure of this record:
the JSON pointers below may no longer point where they did, and the refresh procedure in `contracts/README.md`
applies. Nothing here needs a vendor account or a network call beyond the fetch.

The full-text counts in "Method" are `grep -o '<text>' openapi.json | wc -l` for each string named there. The
README-versus-spec comparison is this script, run against the README as it stood before this unit
(`git show aeb86e1:profiles/perplexity/contracts/README.md > readme-before.md`):

```python
import json, sys

spec = json.load(open(sys.argv[1]))          # openapi.json
readme = open(sys.argv[2]).read()            # readme-before.md
schemas = spec["components"]["schemas"]

def table_after(heading):
    rows = []
    for line in readme[readme.index(heading):].splitlines()[1:]:
        if line.startswith("|") and not line.startswith(("|---", "| Field")):
            rows.append([c.strip() for c in line.strip().strip("|").split("|")])
        elif rows and not line.startswith("|"):
            break
    return rows

for heading, name in {
    "### Request — `ResponsesRequest`": "ResponsesRequest",
    "### Response — `ResponsesResponse`": "ResponsesResponse",
    "### `MessageOutputItem`": "MessageOutputItem",
    "### `SearchResultsOutputItem`": "SearchResultsOutputItem",
    "### `ContentPart`": "ContentPart",
    "### `Annotation`": "Annotation",
    "### `SearchResult`": "SearchResult",
    "### `ResponsesUsage`": "ResponsesUsage",
    "### `ResponsesCost`": "ResponsesCost",
    "### `ErrorInfo`": "ErrorInfo",
}.items():
    rows = {r[0].strip("`"): "**yes**" in r[2] for r in table_after(heading)}
    props = set(schemas[name].get("properties", {}))
    required = set(schemas[name].get("required", []))
    print(name, "only in spec:", sorted(props - set(rows)), "only in README:", sorted(set(rows) - props),
          "required-flag diffs:", [p for p in rows if p in props and rows[p] != (p in required)])
```

Its output on 2026-10-01: every schema agrees except `ResponsesRequest`, which has `profile` only in the spec.

## Method and limits

- **The previous document is gone.** The bytes with sha256 `95305c44...` were not kept anywhere reachable, so a true
  old-versus-new diff cannot be computed. What was done instead is a comparison against the profile README's own
  tables, which were generated from the 2026-08-14 reading: for each Agent schema the profile consumes
  (`ResponsesRequest`, `ResponsesResponse`, `MessageOutputItem`, `SearchResultsOutputItem`, `ContentPart`,
  `Annotation`, `SearchResult`, `ResponsesUsage`, `ResponsesCost`, `ErrorInfo`) the property names and required flags
  in the README were compared programmatically with `S/<name>`. Only `ResponsesRequest` differs: the spec has 19
  properties, the README 18, and the extra one is `profile`. README description cells are truncated at about 120
  characters, so a change confined to a description tail could not be detected this way.
- **Full-text counts** the README recorded on 2026-08-15 were re-run on the current document and are identical:
  `sequence_number` 28, `[DONE]` 0, `event:` 0, `ChatCompletionChunk` 0, `chat.completion.chunk` 0,
  `stream_options` 0. That is strong but not conclusive evidence the stream schemas did not move. New counts taken
  now: `response.incomplete` 0, `response.cancelled` 0, `incomplete_details` 0.
- **Every Agent operation and schema this profile touches was read directly** from the current document, and every
  row below was re-checked against it rather than copied from the lead audit this record started from.
- **What a spec cannot say.** An OpenAPI document gives shapes, enums and the prose its authors wrote. It says
  nothing reliable about billing, retries, idempotency, ordering or termination of a stream, or what the real service
  does with a request the document does not describe. Rows that depend on any of those are `SIMULATOR-POLICY` or
  `UNVERIFIED`, never a vendor guarantee.

## What moved in the spec

Against the README snapshot, the consumed Agent wire shape is unchanged: request fields, response envelope, output
items, usage and cost, `Status`, `ErrorInfo`, the 14 `EventType` members and each event's required fields. What did
move:

1. A new optional request property `profile` (`S/ResponsesRequest/properties/profile`, a `S/ProfileReference`).
2. `skills` now covers organization-owned `custom` skills and has `maxItems: 16`; new `CustomSkill`, `ManagedSkill`,
   `SkillSummary`, `SkillRevision*`, `SkillDownload` and `ApiError` schemas and seven `/v1/skills*` operations.
   Unrelated to anything this profile consumes.
3. The `model` description's example changed (`openai/gpt-5` to `openai/gpt-5.6-terra`). Cosmetic.
4. `ContentPartType` is now enumerated (`["output_text"]`); `Annotation.type` is described `Annotation type
   (url_citation)`. Both were recorded as inferred. The profile's values were right; the notes were stale.
5. `retrieveAgent`, `listAgentFiles`, `downloadAgentFile` and `cancelAgentResponse` are **not** new: the 2026-08-14
   README already listed all four, and `Status` already had `cancelled`. What the README never recorded is their
   bodies, which are noted under "Lifecycle" for issue #6.
6. The larger finding is a mismatch the drift review exposed, not drift: the current document documents only `200`
   and `400` on `createAgent` (`#/paths/~1v1~1agent/post/responses`), and no Agent operation documents `422`, `401`,
   `403`, `429` or `500`. `S/HTTPValidationError` is referenced by exactly seven operations, all outside the Agent
   surface (`/v1/sonar`, `/search`, `/v1/embeddings`, `/v1/contextualizedembeddings`, and the three
   `/v1/async/sonar` operations). Whether the 2026-08-14 document declared more and later dropped it, or the first
   reading over-generalised, cannot be decided without the old bytes. The current document is the authority either way.

## Classification key

The classification states the profile's relationship to the spec **after** this unit's changes.

| Class | Meaning |
|---|---|
| `UNCHANGED` | The profile matches the spec and this unit did not touch it |
| `CHANGED` | This unit changed behaviour, shape or labelling to match the spec |
| `NEWLY-REQUIRED` | This unit began enforcing a requirement the spec states |
| `REMOVED` | This unit removed behaviour or a fixture the spec does not support |
| `UNVERIFIED` | The spec is silent or ambiguous; nothing was implemented on a guess |
| `INFERENCE` | The spec is silent or ambiguous and the profile implements one reading of its text anyway; a reading, not a vendor statement |
| `SIMULATOR-POLICY` | A deliberate Servicesim choice the spec does not state |

A rule the spec states plainly and the profile still does not enforce is classed `UNCHANGED` and listed again under
"Stated by the spec, not enforced", so it is findable.

## Post-fix classification

### Auth and transport

| Field or operation | Spec pointer | Class | Note |
|---|---|---|---|
| Security scheme | `#/components/securitySchemes/HTTPBearer`; `#/paths/~1v1~1agent/post/security`; no global `#/security` | `UNCHANGED` | Every Agent operation declares `HTTPBearer`. Only `GET /v1/models` is unauthenticated (`security: []`). |
| 401 on a missing or wrong credential | none: `#/paths/~1v1~1agent/post/responses` has `200`, `400` | `SIMULATOR-POLICY` | The status is the ordinary HTTP answer and the profile must fail closed on auth, so it stays. The `ErrorInfo` body is an extrapolation from the documented `400`. Its golden was labelled `vendor-documented`; it is now `simulator-chosen`. |
| Request media type | `#/paths/~1v1~1agent/post/requestBody/content` has only `application/json` | `UNCHANGED` | Warn-only, as before. |
| 200 media types | `#/paths/~1v1~1agent/post/responses/200/content`: `application/json` (`S/ResponsesResponse`), `text/event-stream` (`S/ResponseStreamEvent`) | `UNCHANGED` | The spec pins only the media type. `Cache-Control: no-cache` is `SIMULATOR-POLICY`. |

### Request: `S/ResponsesRequest`

| Field | Spec pointer | Class | Note |
|---|---|---|---|
| Required set | `S/ResponsesRequest/required` = `["input"]` | `UNCHANGED` | |
| `input` string or array | `S/Input` `oneOf` | `UNCHANGED` | |
| `input[]` item `type` | `S/InputItem` (discriminator `type`, mapping `message`, `function_call`, `function_call_output`); `S/InputMessage/required` | `NEWLY-REQUIRED` | An item must be an object whose `type` is a mapping key. `agent_test.go` used to pin a type-less item as accepted; that encoded the wrong shape and now carries a `type`. |
| `input[]` per-variant properties | `S/InputMessage/required` = type, role, content; `S/FunctionCallInput/required`; `S/FunctionCallOutputInput/required` | `UNCHANGED` | Not enforced. See "Stated by the spec, not enforced". |
| `model`, `models`, `preset`, `profile` presence | `S/ResponsesRequest/properties/model/description`: "Required if neither models nor preset is provided"; `.../preset/description`: "Required if model is not provided" | `NEWLY-REQUIRED` | One of `model`, `models`, `preset` or a valid `profile` must be a non-empty selection. |
| The `preset` text against a models-only request | `.../preset/description`, read literally, requires `preset` whenever `model` is absent, which conflicts with a request that sends only `models` and with the `model` text that exempts it | `INFERENCE` | The lenient reading is taken: `models` alone is accepted. The `model` text is the one that names `models`. |
| An empty `model` string, or an empty entry of `models` | spec silent | `SIMULATOR-POLICY` | Selects nothing. Treating `""` as a selection would render `"model": ""`, the defect being fixed. An empty entry is skipped when choosing the echoed model; a `models` array of only empty strings is a missing model. |
| `models` shape | `S/ResponsesRequest/properties/models`: array of string, `minItems` 1, `maxItems` 5 | `NEWLY-REQUIRED` | `minItems`, array-ness and string items are now enforced; the cap of 5 already was. |
| `models` precedence | same description: "If set, takes precedence over single model field" | `CHANGED` | The echoed model is now the first entry of `models` even when `model` is also sent. It used to prefer `model`. |
| `model` format | description says `provider/model`; no `pattern` | `SIMULATOR-POLICY` | A warning, not a rejection, deliberately. |
| `preset` | `S/ResponsesRequest/properties/preset` | `UNCHANGED` | Type is checked (string); what a preset configures is not simulated. |
| `profile` | `S/ResponsesRequest/properties/profile` (`allOf` `S/ProfileReference`: `additionalProperties: false`, required `type` = `custom` and `id` 1 to 128 characters, optional `version` string); "Cannot be combined with preset" | `CHANGED` | Newly added to the spec and now a modelled property: no spurious `request.unknown_field`, shape validated, combination with `preset` rejected. Its behaviour is not simulated. |
| `profile` as a model selection | `.../model/description` does not mention `profile`; `.../profile/description`: "Saved, versioned configuration to run with ... Cannot be combined with preset" | `INFERENCE` | A valid profile alone is accepted, as a preset is: the two read as alternatives and nothing says a profile does not supply the model. An invalid profile selects nothing and is reported once, as itself. See "Decisions". |
| `max_output_tokens` for `anthropic/*` | `S/ResponsesRequest/properties/max_output_tokens/description`: "If omitted for an Anthropic model, the API returns HTTP 400 with: validation failed: max_output_tokens is required when using Anthropic models." | `NEWLY-REQUIRED` | The only Agent rule where the spec gives status and message. Reproduced exactly. A `models` chain counts if any non-empty entry is `anthropic/*`: `INFERENCE`, since the spec does not say whether a chain is checked up front or only when it falls through. A profile or preset request is not checked: its model is not knowable. |
| `max_output_tokens` range | same property: integer, int32, `minimum: 1` | `UNCHANGED` | `<= 0` and non-numbers rejected. Integer-ness and the int32 bound are not enforced. |
| `max_steps` | `S/ResponsesRequest/properties/max_steps`: integer, int32, `minimum: 1`, `maximum: 100` | `NEWLY-REQUIRED` | Integer-ness and the maximum are now enforced; 101 and 1.5 used to pass. |
| `stream` | `S/ResponsesRequest/properties/stream`: boolean | `NEWLY-REQUIRED` | A non-boolean used to be ignored silently, unlike `store` and `background`. |
| `store` | `S/ResponsesRequest/properties/store`: boolean | `UNCHANGED` | Type-checked, not echoed. `S/ResponsesResponse` declares no `store` property, so "the echoed response reports `store: false`" is prose only. |
| `background` | `S/ResponsesRequest/properties/background`: boolean | `UNCHANGED` | `true` still warns `perplexity.agent.background.unsupported` and returns the synchronous body. Issue #6. *Superseded 2026-10-03 (issue #6, unit U5): that code no longer exists. `background: true` now mints a job and answers a `queued` snapshot, or fails closed; see "Notes after 2026-10-01".* |
| `temperature`, `top_p` | `.../temperature` 0 to 2, `.../top_p` 0 to 1 | `UNCHANGED` | |
| `previous_response_id` | `#/paths/~1v1~1agent/post/responses/400/description` | `SIMULATOR-POLICY` | The profile is stateless by design (house rule 6), so it never resolves one. |
| `reasoning` | `S/ReasoningConfig/properties/effort/enum` = minimal, low, medium, high, xhigh, max | `UNVERIFIED` | Accepted opaque. |
| `response_format` | `S/ResponseFormat` (required `type` = `json_schema`; `S/JSONSchemaFormat` requires `name` 1 to 64 and `schema`) | `UNVERIFIED` | Accepted opaque. |
| `tools` | `S/Tool`, a union of 8 | `UNVERIFIED` | Accepted opaque. |
| `skills` | `S/ResponsesRequest/properties/skills`: `maxItems` 16; `S/Skill` `oneOf` | `UNCHANGED` | Accepted opaque. The description gained `custom`. |
| `instructions`, `language_preference` | strings | `UNCHANGED` | Types not checked. |
| Unknown properties | `S/ResponsesRequest` has no `additionalProperties: false` | `UNCHANGED` | Warns, does not reject. |

### Response envelope: `S/ResponsesResponse`

| Field | Spec pointer | Class | Note |
|---|---|---|---|
| Required set | `S/ResponsesResponse/required` = id, object, created_at, status, model, output | `UNCHANGED` | `usage` is optional in the spec and always emitted by the profile (legal). |
| `id` | `S/ResponsesResponse/properties/id`: string, no pattern | `SIMULATOR-POLICY` | The `resp_<32 hex>` form is consistent with the path-parameter descriptions of the retrieve and cancel operations but is not a documented response format. |
| `model` for a request that selects one | `.../properties/model`: "Model used for generation" | `CHANGED` | Never empty now: the first non-empty entry of `models`, else `model`. A scenario's own `model:` still wins. |
| `model` for a preset-only or profile-only request | spec silent on what either echoes | `SIMULATOR-POLICY` | Rendered `preset/<name>` or `profile/<id>`: deterministic, non-empty, `provider/model` shaped. The configuration's real model is not knowable here. |
| `object`, `created_at`, `status` | `S/ResponsesObjectType/enum`, int64, `S/Status/enum` (6 members) | `UNCHANGED` | `cancelling` is not a `Status` member; it appears only in the cancel response. |
| `output[]` | `S/OutputItem` `oneOf` of 10 | `UNCHANGED` | Only `search_results` and `message` are simulated. Their fixed order is `SIMULATOR-POLICY`. |
| `error` | `S/ResponsesResponse/properties/error` | `SIMULATOR-POLICY` | A `failed` body delivered as HTTP 200 is policy: the spec states no HTTP status for a failed run. |
| `incomplete_details` | absent: 0 occurrences | `UNCHANGED` | An `incomplete` response cannot carry a reason, which matches the spec. |
| Tolerance | no `additionalProperties: false` | `UNCHANGED` | |

### Output items, usage, cost

| Field | Spec pointer | Class | Note |
|---|---|---|---|
| `message` item | `S/MessageOutputItem/required`; `S/RoleType/enum` = `["assistant"]` | `UNCHANGED` | `msg_<32 hex>` ids are `SIMULATOR-POLICY`. |
| `content[].type` | `S/ContentPartType/enum` = `["output_text"]` | `UNCHANGED` | The value was right; the spec now enumerates it. Stale "inferred" notes corrected. |
| `annotations[]` | `S/Annotation`: no required properties; `type` free string described "(url_citation)"; indices int32 "character index" | `UNCHANGED` | `url_citation` now has support in the description. The load check compares indices to the answer's length in **bytes** while the spec says "character": the unit is `UNVERIFIED` and only matters for non-ASCII fixtures. |
| `search_results` item | `S/SearchResultsOutputItem/required` = type, results | `UNCHANGED` | |
| `results[].id` | `S/SearchResult/properties/id`: integer int64 | `UNCHANGED` | A JSON integer, pinned by a golden. |
| `results[].source` | `S/SearchSource/enum` = `["web"]` | `CHANGED` | Load check: an Agent fixture `source_type: attachment` is an error. `attachment` is Sonar's `ApiPublicSearchResult`, a different schema; Sonar keeps both values. |
| Usage tokens | `S/ResponsesUsage/required` = input_tokens, output_tokens, total_tokens | `UNCHANGED` | |
| Cost required set | `S/ResponsesCost/required` = currency, input_cost, output_cost, total_cost | `UNCHANGED` | |
| `currency` | `S/Currency/enum` = `["USD"]` | `CHANGED` | Load check: any other value in a fixture is an error. |
| `total_cost` derivation | `S/ResponsesCost/properties/total_cost`: "Total cost for the request in USD", no formula | `SIMULATOR-POLICY` | Input plus output cost when unset; cache and tool costs are not folded in. The spec gives no billing formula, and none should be read into this. |

### Errors

| Case | Spec pointer | Class | Note |
|---|---|---|---|
| Validation failure | `#/paths/~1v1~1agent/post/responses/400`: `{error: S/ErrorInfo}`; `max_output_tokens` text gives "HTTP 400 ... validation failed: ..." | `CHANGED` | Now 400 with `ErrorInfo` and `validation failed: <message>`. Naming the first failing field in request-schema order, and applying the prefix to rules other than `max_output_tokens`, are `SIMULATOR-POLICY`. Both Agent 400 goldens are labelled `vendor-documented` on one rule: the spec documents the `400` status and the `ErrorInfo` shape; the code, type and message strings inside are Servicesim's. |
| Agent 422 `HTTPValidationError` | referenced by no Agent operation | `REMOVED` | `perplexity-agent-422.json` and its assertions are gone. Sonar's 422 is untouched. |
| `stream: reject` policy | n/a | `CHANGED` | Moves with validation: 400 `ErrorInfo`. |
| Malformed JSON, non-object body | spec silent | `SIMULATOR-POLICY` | Now 400 on the Agent surface. Whether the real service answers a framework-level 4xx of another shape is `UNVERIFIED`. |
| 401, 403, 429, 500 | none documented on any Agent operation | `SIMULATOR-POLICY` | Behaviour kept; the four goldens are relabelled `simulator-chosen`. The `ErrorInfo` body is by extrapolation. |
| 404, 405 routing | n/a | `SIMULATOR-POLICY` | The unserved-route 404 is Sonar-shaped `{"detail": ...}`, whereas the spec's Agent 404 (retrieve, files, cancel) is `ErrorInfo`. Noted for issue #6. |
| `ErrorInfo` shape | `S/ErrorInfo/required` = `["message"]`; `code`, `type` optional strings | `UNCHANGED` | The `code` and `type` values are `SIMULATOR-POLICY`. Do not unify with `S/ApiError` (Skills operations only; `code` is an integer there). |

### Streaming

| Case | Spec pointer | Class | Note |
|---|---|---|---|
| Event vocabulary | `S/EventType/enum` and `S/ResponseStreamEvent`: 14 members | `CHANGED` | Seven are emitted now: created, output_item.added, output_text.delta, output_text.done, output_item.done, completed, and **failed**. The reasoning family and `response.in_progress` are not (`SIMULATOR-POLICY`; no scenario vocabulary). |
| Required fields on emitted events | `S/ResponseCreatedEvent`, `...CompletedEvent`: type, sequence_number; item events add item, output_index; delta and done events add item_id, output_index, content_index, delta or text | `UNCHANGED` | |
| Failure form | `S/ResponseFailedEvent/required` = type, sequence_number, **error**; "Contains error details when streaming fails" | `CHANGED` | A scripted `failed` turn streams `response.created` then a terminal `response.failed` with the scenario's error at top level. It used to stream `response.completed` carrying `status: failed`. |
| Ordering and termination around `response.failed` | spec silent | `SIMULATOR-POLICY` | Created first, `failed` terminal, no `response.completed` after it, no usage and no `extra_fields` on it (the schema has no response object). Whether the real service ever sends `response.completed` with `status: failed` is `UNVERIFIED`. |
| `incomplete` form | no `response.incomplete` in `EventType`; `S/ResponseCompletedEvent`: "full or partial response object" | `SIMULATOR-POLICY` | Unchanged: `response.completed` carrying `status: incomplete`, message item kept. Now pinned by a test so a change is deliberate. |
| `cancelled` form | no `response.cancelled` in `EventType` | `SIMULATOR-POLICY` | Unchanged: `response.created` then `response.completed` carrying `status: cancelled`. Pinned by the same test. |
| `event: <type>` line | 0 occurrences of `event:` | `SIMULATOR-POLICY` | |
| `[DONE]` sentinel | 0 occurrences | `UNVERIFIED` | Not emitted. |
| `sequence_number` start | `S/ResponseCreatedEvent/properties/sequence_number`: "Monotonically increasing", start unstated | `SIMULATOR-POLICY` | Starts at 0. |
| `response.created` payload | `S/ResponseCreatedEvent`: "the initial response object" | `UNVERIFIED` | The spec gives no initial-state shape. |
| Termination of a successful stream | nothing documented | `UNVERIFIED` | The connection closes after `response.completed`. |
| Usage in the terminal frame | `S/ResponsesResponse/properties/usage` optional | `UNCHANGED` | `terminal.omit_usage` is legal. |

### Lifecycle (issue #6)

Recorded on 2026-10-01 so issue #6 does not have to re-read the document. At that date none of this was served; what
has been built since, and what the 2026-10-03 re-read found, is under "Notes after 2026-10-01" below. The table is the
2026-10-01 reading and is not edited.

| Operation | Spec pointer | Note |
|---|---|---|
| `retrieveAgent` | `#/paths/~1v1~1agent~1{id}/get`: 200 `S/ResponsesResponse`; 404 `{error: ErrorInfo}` | 404 for an unknown id, another account's, or a `store: false` response. |
| `cancelAgentResponse` | `#/paths/~1v1~1agent~1{id}~1cancel/post`: 200 object, required `response_id` and `status` (enum `["cancelling"]`, one member); 400 already terminal "or the request is invalid"; 404 | Asynchronous; the poll is `GET /v1/agent/{id}`. `cancelling` is not a `Status` member. What the poll returns mid-cancel is unstated. |
| `listAgentFiles`, `downloadAgentFile` | `#/paths/~1v1~1agent~1{id}~1files/get` (200 `S/ResponseFileList`, 404) and `.../files/{file_id}/content/get` (404) | |
| `background: true` | `S/ResponsesRequest/properties/background/description` | Says "poll `GET /v1/responses/{id}`", but the only declared poll route is `GET /v1/agent/{id}`. That `/v1/responses/{id}` is the same SDK alias the profile uses for `POST` is an inference. No reconnect mechanism is documented. |
| Terminal and non-terminal statuses | `S/Status` has no split | `UNVERIFIED`. Terminal is plausibly completed, failed, incomplete and cancelled; the spec does not say. |

## Defects and how they were resolved

| # | Defect | Resolution |
|---|---|---|
| D1 | Agent validation failures answered 422 `HTTPValidationError`; the spec documents 400 `ErrorInfo` and no Agent 422 | `70af301` fix(perplexity): answer Agent validation failures with 400 ErrorInfo, not 422 |
| D2 | `model`, `models`, `preset` presence not enforced; a preset-only request rendered `"model": ""` | `dbf846f` fix(perplexity): require a model selection and max_output_tokens for Anthropic |
| D3 | `max_output_tokens` not required for `anthropic/*` | same commit |
| D4 | `profile` unmodelled, drawing a spurious `request.unknown_field` | `9abf671` feat(perplexity): accept and validate the Agent request's profile field |
| D5 | `input[]` items unvalidated; a test pinned a type-less item as accepted | `77a9f67` fix(perplexity): enforce the Agent request rules the spec states outright |
| D6 | `max_steps` maximum and integer-ness; `models` `minItems`; non-boolean `stream` | `77a9f67` for `max_steps` and `stream`; `dbf846f` for `models` |
| D7 | Agent `source_type: attachment` accepted | `77a9f67` |
| D8 | `usage.cost.currency` unconstrained | `77a9f67` |
| D9 | `provenance.yaml` labelled Agent 401, 403, 422, 429, 500 `vendor-documented`; `ContentPartType` and `Annotation.type` notes stale; `spec:` block out of date | `70af301` relabelled the goldens; the commit that adds this record corrected the notes and updated `spec:` |
| D10 | A failed stream used `response.completed` where the spec has `response.failed` | `0878145` fix(perplexity): stream a failed Agent turn as response.failed |

D10 was not in the lead audit's ranked list; it was listed there as a flagged known gap.

## Breaking changes

All approved by the owner on 2026-10-01 (breaking wire corrections are approved; the vendor spec is the authority).

1. An Agent request that fails validation is **400** with `{"error": {...}}`, not **422** with `{"detail": [...]}`.
   `perplexity-agent-422.json` is removed; `perplexity-agent-400-validation.json` replaces it.
2. A request with no `model`, `models`, `preset` or valid `profile` is now a 400. Three Agent requests in `scenarios/scenarios_test.go`
   and the Agent body in `scripts/image-smoke.sh` were model-less and now carry one.
3. An `anthropic/*` model without `max_output_tokens` is a 400.
4. `models` must be a non-empty array of strings, and `models` now takes precedence over `model` in the echoed model.
   An empty-string entry of `models` selects nothing, as an empty `model` does.
5. `max_steps` above 100 or non-integer, a non-boolean `stream`, and an `input[]` item without a valid `type` are
   400s.
6. An Agent fixture with `source_type: attachment` or a `usage.cost.currency` other than `USD` fails at load.
7. A scripted `failed` turn streams `response.failed` instead of `response.completed`.
8. A request combining `profile` and `preset`, or carrying a malformed `profile`, is a 400.

## Recorded, not implemented

**Stated by the spec, not enforced.** The spec is unambiguous; they were outside the five-rule list this unit was
given, and each is a small bounded addition.

- `input[]` per-variant required properties: `role` and `content` on a message, `call_id`, `name` and `arguments` on
  a `function_call`, `call_id` and `output` on a `function_call_output`.
- `max_output_tokens` integer-ness and its int32 bound; `max_steps` and `max_output_tokens` int32 bounds.
- `skills` `maxItems: 16`; `reasoning.effort`, `response_format`, `tools` and `skills` item shapes (all accepted
  opaque); string types of `instructions` and `language_preference`.

**`UNVERIFIED`, not implemented.** Anything that would have needed inference.

- Whether the real service ever sends `response.completed` with `status: failed`; ordering and termination of any
  stream; the `[DONE]` sentinel; the initial `response.created` payload.
- The shape of a framework-level failure for a body that is not JSON.
- The unit of `annotations[].start_index` and `end_index` ("character index").
- Terminal and non-terminal `Status` members, and what a retrieve returns mid-cancel.

**Implemented on inference.** These ARE implemented, as the lenient or strict reading of text that does not settle
them; each is labelled `INFERENCE` here and in the contract README.

- A valid `profile` counts as a model selection.
- The `preset` description's "Required if model is not provided" is not enforced against a `models`-only request.
- Any non-empty entry of a `models` chain being `anthropic/*` triggers the `max_output_tokens` requirement.

## Decisions

1. **A valid profile counts as a model selection** (decided in review, 2026-10-01; `INFERENCE`). The spec says `model`
   is "Required if neither models nor preset is provided" and does not mention `profile`. But it describes `profile`
   as a "Saved, versioned configuration to run with" that "Cannot be combined with preset", which makes the two
   alternatives, and nothing says a profile does not supply the model. Rejecting `{input, profile}` would make a field
   the profile was told to accept unusable, and this simulator prefers not to reject traffic the live API may accept
   where the spec is silent. The echoed model for it, `profile/<id>`, mirrors `preset/<name>` and is
   `SIMULATOR-POLICY`. An earlier draft of this record took the strict reading; it was reversed in review.
2. **`profile` combined with `preset` is rejected** with `perplexity.agent.profile.invalid`. The spec states the rule
   ("Cannot be combined with preset") but not the failure status; 400 follows from the rest of the Agent surface.
3. **A preset-only or profile-only request echoes `preset/<name>` or `profile/<id>`.** The spec is silent. A consumer
   that parses `response.model` as `provider/model` still works, and a scenario's `model:` overrides it.
4. **Provenance dates.** The sanctioned refresh procedure in `contracts/README.md` moves the provider-level `verified:`
   date whenever any entry is re-checked later. It is now 2026-10-01, with every Agent golden re-dated except
   `perplexity-agent-stream.sse` (whose prose documentation page was not re-read), while the
   Sonar and routing entries keep their earlier dates because they were not re-read. The bundle date therefore
   means "most recent re-check of anything", as that procedure defines it, not "every route re-read".

## Where this record disagrees with the lead audit

- The lead audit folded `profile` into the model requirement ("and, with D5, profile"). This record now agrees, as
  an `INFERENCE`; see decision 1.
- The lead audit named the stream failure form as a known gap and left it. This record changed it, because the
  spec's `ResponseFailedEvent` is an unambiguous failure signal; ordering and termination remain policy.
- Nothing else in the lead audit's table was found wrong. Every row was re-checked against the current document and
  the pointers, counts and enums above agree with it.

## Notes after 2026-10-01

Dated additions. The findings above are the 2026-10-01 record and are not rewritten; where a later note changes what a
row says, the row carries a pointer here.

### 2026-10-03: the background lifecycle is served (issue #6, unit U5)

`retrieveAgent` and `background: true` are now simulated; `cancelAgentResponse` and the files operations are not. In
the terms of the "Lifecycle" table above:

| Row | Status after U5 |
|---|---|
| `retrieveAgent` | Served as `GET /v1/agent/{id}`, for a background run only. The `404` shape is `{error: ErrorInfo}`. A synchronous response's id is also `404`, a deliberate and named divergence (owner ruling 7 on issue #6): the real API retrieves a response stored by default. |
| `cancelAgentResponse` | Not served. A `cancel:` key under a scenario's `background:` block is a load error. |
| `listAgentFiles`, `downloadAgentFile` | Not served. |
| `background: true` | Mints a job and answers `queued`, or fails closed: with no scripted `background:` block, a `404` and `perplexity.agent.background.unscripted`; with `stream: true`, a `400` and `perplexity.agent.background.stream`. With `store: false` it answers `queued` under the synchronous id, keeps no job and warns `perplexity.agent.background.unstored`. `perplexity.agent.background.unsupported`, named in the "Request" table above, was removed (a breaking change approved under ruling 2). The poll served is `GET /v1/agent/{id}`; `GET /v1/responses/{id}` is not, and the inconsistency stays unresolved. |
| Terminal and non-terminal statuses | Still `UNVERIFIED`. The profile now takes `completed`, `failed`, `incomplete` and `cancelled` as terminal, as `SIMULATOR-POLICY`, and rejects a script that serves a non-terminal snapshot after a terminal one. |

Two rows of the tables above read differently for a background snapshot, and the synchronous path is unchanged:
`usage` is rendered only when the snapshot scripts it, and its `cost` only when that is scripted too, because the spec
makes both optional and a zero the scenario did not script is no billing fact; and `model` falls back to the fixed
placeholder `servicesim/unscripted` when a snapshot scripts none, since a retrieve has no request to echo. The
contract notes, `profiles/perplexity/contracts/README.md` "Lifecycle: background runs and retrieve", have the rest.

### 2026-10-03: a re-read against a document whose hash has moved

The lifecycle operations were read again, against a fresh fetch (215,432 bytes, sha256
`1a269d5596e506d3189e57c3ae84874a21c3532afe8c95de6f21e55f17001f15`, `info.version` still `1.0.0`). `retrieveAgent`,
`cancelAgentResponse`, the `background` and `store` descriptions, `Status`, `ResponsesResponse` and `ErrorInfo` are
unchanged from the rows above.

The document has moved nonetheless. Its hash is not the one in "Baseline" above (`e0b92edf…`, 208,564 bytes), and the
bytes audited on 2026-10-01 were not kept, so the full difference cannot be computed, exactly as the "Method and limits"
section already said of the previous one. One difference is confirmed: `ResponsesRequest` now has a `tool_choice`
property (`allOf` `S/ToolChoice`), making 20 properties where this record counted 19. This record does not list it and
the profile's `agentFields` does not name it, so a request carrying it draws a `request.unknown_field` warning. It is a
known difference, left for a re-audit.

A whole-bundle re-audit was **not** done. The provider-level `verified:` date, the `spec:` block in `provenance.yaml`
and the Perplexity cell of the `contracts/README.md` index therefore stay at 2026-10-01, and the three golden entries
added for the lifecycle carry that date as well, with a comment in `provenance.yaml` saying why. Moving them would have
recorded bytes the bundle was not audited against and erased the drift signal that "Reproducing it" above relies on.
