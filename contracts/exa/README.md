# Exa consumed contract

Verified against live vendor documentation on **2026-08-14**.

This file records only what Servicesim simulates and what consumers parse. It is not a
redistribution of the vendor's OpenAPI document. Re-verify and update the date above when
the live contract canary reports drift.

## Documentation sources

- <https://exa.ai/docs/reference/search>
- <https://exa.ai/docs/reference/search-api-guide-for-coding-agents>
- <https://exa.ai/docs/reference/answer>
- <https://exa.ai/docs/reference/error-codes>
- <https://apis.io/apis/exa-ai/exa-ai-search-api/>

## Endpoints

| Method | Path | Status | Note |
|---|---|---|---|
| `POST` | `/search` | canonical, simulated | Base URL <https://api.exa.ai>. Confirmed on both the OpenAPI-backed reference page and the coding-agents guide. |
| `POST` | `/answer` | canonical, simulated | Separate documented endpoint at <https://exa.ai/docs/reference/answer>. Same auth. Request: query (string, required), stream, text, outputSchema. Response: answer (string\|object), citations[] (title,url,publishedDate,author,id,image,text), requestId, costDollars. The plan doc does not mention /answer at all. |
| — | `/contents` | NOT SIMULATED | No verified vendor contract recorded yet; scheduled for verification. Referenced indirectly by the error-codes page's `statuses[]` / CRAWL_* tags, which describe a contents-fetch surface. No /contents reference page was fetched, so no method or route shape is asserted here. |
| — | `/findSimilar` | NOT SIMULATED | No verified vendor contract recorded yet; scheduled for verification. Named here so a reader can tell it was considered rather than overlooked; nothing about its method, request or response is asserted. |
| — | `/agent/runs` (+ run lifecycle) | NOT SIMULATED | On the backlog. See the "Exa Agent API" section at the end of this file for the lifecycle and the reason. |

## Authentication

- x-api-key: `<key>`
- Authorization: Bearer `<key>`

## Request fields

| Field | JSON type | Required | Enum | Default |
|---|---|---|---|---|
| `query` | `string` | yes | — | — |
| `type` | `string` | no | `auto`, `fast`, `instant`, `deep-lite`, `deep`, `deep-reasoning` | `auto` |
| `numResults` | `integer` | no | — | `10` |
| `category` | `string` | no | `company`, `publication`, `news`, `personal site`, `financial report`, `people` | — |
| `moderation` | `boolean` | no | — | `false` |
| `userLocation` | `string` | no | — | — |
| `compliance` | `string` | no | `hipaa` | — |
| `includeDomains` | `array[string]` | no | — | — |
| `excludeDomains` | `array[string]` | no | — | — |
| `startPublishedDate` | `string` | no | — | — |
| `endPublishedDate` | `string` | no | — | — |
| `startCrawlDate` | `string` | no | — | — |
| `endCrawlDate` | `string` | no | — | — |
| `additionalQueries` | `array[string]` | no | — | — |
| `systemPrompt` | `string` | no | — | — |
| `outputSchema` | `object` | no | — | — |
| `stream` | `boolean` | no | — | `false` |
| `contents` | `object` | no | — | — |
| `contents.text` | `boolean\|object` | no | — | — |
| `contents.highlights` | `boolean\|object` | no | — | — |
| `contents.summary` | `boolean\|object` | no | — | — |
| `contents.extras.links` | `integer` | no | — | `0` |
| `contents.extras.imageLinks` | `integer` | no | — | `0` |
| `contents.extras.codeBlocks` | `integer` | no | — | `0` |
| `contents.maxAgeHours` | `integer` | no | — | — |
| `contents.livecrawlTimeout` | `integer` | no | — | `10000` |
| `contents.subpages` | `integer` | no | — | `0` |
| `contents.subpageTarget` | `string\|array[string]` | no | — | — |
| `useAutoprompt` | `boolean` | no | — | — |
| `livecrawl` | `string` | no | — | — |
| `context` | `boolean\|object` | no | — | — |

## Response fields

| Field | JSON type | Always present | Nullable |
|---|---|---|---|
| `requestId` | `string` | yes | no |
| `results` | `array[object]` | yes | no |
| `results[].title` | `string` | yes | no |
| `results[].url` | `string` | yes | no |
| `results[].id` | `string` | no | no |
| `results[].publishedDate` | `string` | no | yes |
| `results[].author` | `string\|null` | no | yes |
| `results[].text` | `string` | no | no |
| `results[].highlights` | `array[string]` | no | no |
| `results[].highlightScores` | `array[number]` | no | no |
| `results[].summary` | `string` | no | no |
| `results[].image` | `string` | no | no |
| `results[].favicon` | `string` | no | no |
| `results[].subpages` | `array[object]` | no | no |
| `results[].extras` | `object` | no | no |
| `results[].entities` | `array[object]` | no | no |
| `costDollars` | `object` | yes | no |
| `costDollars.total` | `number` | yes | no |
| `costDollars.search` | `object` | no | no |
| `costDollars.search.neural` | `number` | no | no |
| `costDollars.contents` | `object` | no | no |
| `resolvedSearchType` | `string` | no | no |
| `context` | `string` | no | no |
| `output` | `object\|null` | no | yes |
| `output.content` | `string\|object` | no | no |
| `output.grounding` | `array[object]` | no | no |
| `output.grounding[].field` | `string` | no | no |
| `output.grounding[].citations` | `array[object]` | no | no |
| `output.grounding[].confidence` | `string` | no | no |
| `statuses` | `array[object]` | no | no |

## Error bodies

### 400

```json
{"requestId":"67207943fab9832d162b5317f4cca830","error":"Invalid request body | Validation error: Invalid enum value. Expected 'auto' | 'fast' | 'instant' | 'deep-lite' | 'deep' | 'deep-reasoning', received 'slow' at \"type\"","tag":"INVALID_REQUEST_BODY"}
```

This is the one verbatim error body the docs give, and it doubles as proof of the exact `type` enum. Canonical error shape is a flat {requestId, error, tag} object — NOT a nested {error:{code,message}}.

### 429

```json
{"error":"You've exceeded your Exa rate limit of 10 requests per second. If you want this increased, please email hello@exa.ai :)"}
```

Rate-limit responses use a REDUCED shape: `error` only, no requestId and no tag. A simulator that always emits {requestId,error,tag} would be wrong here.

### 401

```json
{"requestId":"<hex>","error":"<message>","tag":"INVALID_API_KEY"}
```

RECONSTRUCTED, not quoted. 401 is listed among documented statuses and INVALID_API_KEY is a documented tag, but I did not see a verbatim 401 body. Treat the exact message text as unverified.

### 402

```json
{"requestId":"<hex>","error":"<message>","tag":"NO_MORE_CREDITS"}
```

RECONSTRUCTED. 402 Payment Required is the only error status enumerated in the /search OpenAPI reference (description "Payment Required", no body schema). Credit/budget tags NO_MORE_CREDITS, API_KEY_BUDGET_EXCEEDED, TEAM_BUDGET_EXCEEDED exist. 402 is also used by the x402 payment flow with PAYMENT-REQUIRED headers.

### 422

```json
{"requestId":"<hex>","error":"<message>","tag":"INVALID_REQUEST"}
```

RECONSTRUCTED. 422 is listed as a documented status by the coding-agents guide; body not quoted anywhere I fetched.

### 500

```json
{"requestId":"<hex>","error":"<message>","tag":"INTERNAL_ERROR"}
```

RECONSTRUCTED. INTERNAL_ERROR and DEFAULT_ERROR are documented tags; body not quoted.

## Corrections to the original plan document

The following claims in `docs/architecture-and-implementation-plan.md` were contradicted by the live
documentation on 2026-08-14. The verified contract above is authoritative.

- **Plan said:** Result objects carry a `score` field (float, e.g. 0.95)
  - **Live docs say:** No `score` field exists on SearchResultOutput on either doc page. The reference schema enumerates title, url, publishedDate, author, id, image, favicon, text, highlights, highlightScores, summary, subpages, entities, extras. The only score-like field is highlightScores (array of floats). A simulator emitting a top-level per-result `score` would be teaching consumers to parse a field the real API never sends.
- **Plan said:** The /search response's top-level fields are `results` and `requestId` (per the plan's response example)
  - **Live docs say:** The response also always carries `costDollars` ({total: float, search:{neural: float}}), plus deprecated `resolvedSearchType` and `context`, and `output` when outputSchema was supplied. costDollars is entirely absent from the plan and is exactly the sort of field a cost-tracking consumer parses.
- **Plan said:** Result `id` is an opaque slug like "source-1" (plan's example)
  - **Live docs say:** Docs describe id as "The temporary ID for the document" and their own example uses a URL: "id": "<https://arxiv.org/abs/2307.06435".> A simulator emitting slug-shaped ids will let consumers build assumptions that break against the real API.
- **Plan said:** requestId is a readable slug like "exa-request-1" (plan's example)
  - **Live docs say:** Docs example is a 32-char lowercase hex string: "b5947044c4b78efa9552a7c89b306d95" (and "67207943fab9832d162b5317f4cca830" in the error example). Any consumer regex or length assumption trained on the plan's slug would be wrong.
- **Plan said:** Only the fields listed in the plan need simulating (implicitly: no image, favicon, summary, subpages, extras, entities)
  - **Live docs say:** image (uri), favicon (uri), summary (string), subpages (array), extras (object), and entities (array) are all documented result fields a consumer would plausibly parse. favicon and image in particular are commonly consumed for UI rendering.
- **Plan said:** The plan says nothing about error response bodies for Exa
  - **Live docs say:** This is a gap rather than a wrong statement, but a load-bearing one: the canonical error body is flat {requestId, error, tag} with a documented tag enum (INVALID_REQUEST_BODY, INVALID_API_KEY, NO_MORE_CREDITS, ...), and 429 uses a REDUCED {error} only shape. Documented statuses: 400, 401, 402, 403, 404, 409, 422, 429, 500, 501, 502, 503.
- **Plan said:** The plan scopes Exa to /search only
  - **Live docs say:** POST /answer is a documented sibling endpoint (query/stream/text/outputSchema in; answer, citations[], requestId, costDollars out) with a citation object shape distinct from a search result. Since the task brief names the "search/answer API", the plan's Exa surface is incomplete.
- **Plan said:** The plan does not mention outputSchema / structured output
  - **Live docs say:** outputSchema (object, max nesting depth 2, max 10 properties) is a first-class request field, and supplying it changes the response oneOf branch by adding the `output` object with content and grounding[]. A simulator without this cannot exercise structured-output consumers.
- **Plan said:** The plan does not flag startCrawlDate/endCrawlDate or livecrawl as deprecated
  - **Live docs say:** startCrawlDate and endCrawlDate are documented as deprecated with no effect; `livecrawl` is deprecated in favor of contents.maxAgeHours:0; numSentences and highlightsPerUrl are deprecated highlight params. The plan flags only useAutoprompt.

## POST /answer

Verified against live vendor documentation on **2026-08-14**.

Sources:

- <https://exa.ai/docs/reference/answer>
- <https://exa.ai/docs/reference/answer.md>
- <https://exa.ai/docs/reference/error-codes.md>
- <https://exa.ai/docs/reference/search-api-guide-for-coding-agents>
- <https://exa.ai/docs/sdks/typescript-sdk-specification>
- <https://exa.ai/docs/llms.txt>
- <https://exa.ai/docs/reference/agent-api/overview.md>
- <https://exa.ai/docs/.mintlify/skills/build-with-exa/references/agent.md>
- <https://docs.exa.ai/reference/answer> (307 redirect to `exa.ai/docs/reference/answer`)

Exa's synthesis endpoint: same credential and base URL as `/search`, but it returns a written answer plus
the sources that support it. It is the Exa analogue of Tavily's `include_answer` and of Perplexity's whole
surface.

**Status: simulated, and retained.** `POST /answer` is implemented on the Exa listener, covered by the golden
fixtures in this directory (`exa-answer-happy.json`, `exa-answer-empty.json`, `exa-answer-structured.json`,
`exa-answer-501.json`, `exa-answer-400-invalid-json-schema.json`) and exercised by the provider tests.

**It is not evidenced by an observed consumer.** Servicesim's first adopter reports that their client does not
call `/answer`. No other consumer has been observed calling it either, so nothing here should be read as "a
consumer needs this". The reason it was built — that an *answer plus cited evidence* shape lets a fusion test
treat all three providers symmetrically, instead of leaving Exa the one provider that contributes evidence but
never a claim — was a design argument, and a design argument is not evidence of demand. It is recorded as the
rationale it was. The endpoint stays because it is written, verified against the vendor documentation above,
and tested; removing working simulated surface would cost more than keeping it.

### Request

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `query` | `string` | **yes** | — | minLength: 1. 'Natural-language question or instructions for the request.' Same field name as /search but semantically a question, not a search query. |
| `stream` | `boolean` | no | `false` | 'If true, the response is returned as a server-sent events (SSE) stream.' Changes the response Content-Type to text/event-stream and the body to chunk objects rather than AnswerResponse. |
| `text` | `boolean` | no | `false` | RESHAPED vs /search. On /answer this is a TOP-LEVEL boolean only. On /search the equivalent is contents.text and accepts boolean\|object. /answer docs say 'If true, returns full page text with default |
| `outputSchema` | `object` | no | — | JSON Schema Draft 7 spec for structured answer output. Documented sub-properties: type, properties (object of schemas), required (array), description, additionalProperties (boolean). When supplied, th |
| `model` | `string` | no | — | UNCONFIRMED on the wire. Appears in the TypeScript SDK's AnswerOptions type as model: "exa", but is NOT a property of the AnswerRequest schema on the OpenAPI-backed reference page, which lists exactly |
| `systemPrompt` | `string` | no | — | UNCONFIRMED on the wire. Present in the SDK AnswerOptions type (systemPrompt?: string) and is a documented /search request field, but absent from the /answer AnswerRequest schema. Accept-and-ignore. |
| `userLocation` | `string` | no | — | UNCONFIRMED on the wire. Present in the SDK AnswerOptions type (userLocation?: string) and is a documented /search request field, but absent from the /answer AnswerRequest schema. Accept-and-ignore. |

### Response

| Field | Type | Always present | Nullable | Notes |
|---|---|---|---|---|
| `requestId` | `string` | yes | no | 'Unique identifier for the request.' Same 32-char lowercase hex format as /search — docs example: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6". |
| `answer` | `string\|object` | yes | no | REQUIRED field, and the /answer-only payload. 'string by default, or a structured object matching the provided outputSchema.' This is a true oneOf on the SAME key — unlike /search, which keeps prose o |
| `citations` | `array[object]` | no | no | Marked optional in the schema (only `answer` is required), but present in every documented example. A simulator should emit it by default and only omit it to exercise a defensive-parsing path. |
| `citations[].title` | `string` | yes | no | Required within the citation object. |
| `citations[].url` | `string` | yes | no | Required within the citation object. format: uri. |
| `citations[].id` | `string` | no | no | 'The temporary ID for the document. Useful for the /contents endpoint.' Same URL-shaped value convention as /search — the docs example uses the article URL as the id, identical to the url field. |
| `citations[].publishedDate` | `string` | no | yes | format: date-time. 'An estimate of the creation date, from parsing HTML content.' Docs example: "2024-12-11T00:00:00.000Z" — full ISO-8601 with milliseconds and Z. |
| `citations[].author` | `string\|null` | no | yes | Explicitly string OR null in the schema. Docs example: "Dan Milmo". |
| `citations[].image` | `string` | no | no | format: uri. 'The URL of an image associated with the search result, if available.' |
| `citations[].favicon` | `string` | no | no | format: uri. Favicon URL for the domain. Confirmed present on the /answer citation object (both the reference schema and the .md source list it). |
| `citations[].text` | `string` | no | no | 'The full text content of each source. Only present when text contents are requested.' Gated on request field text: true. |
| `costDollars` | `object` | no | no | Optional in the /answer schema (vs always-present on /search). 'Endpoint-dependent estimated dollar cost breakdown for the completed request.' |
| `costDollars.total` | `number` | yes | no | Required within costDollars. Float. Docs example: 0.005. |
| `costDollars.search` | `object` | no | no | Optional sub-object. The minimal /answer example emits {"total": 0.005} with NO search key at all — a simulator must not assume the breakdown is always populated. |
| `costDollars.search.neural` | `number` | no | no | Float. Same CostDollarsOutput schema object as /search reuses. |

### Error bodies

#### 400 — INVALID_REQUEST_BODY

```json
{"requestId":"67207943fab9832d162b5317f4cca830","error":"Invalid request body | Validation error...","tag":"INVALID_REQUEST_BODY"}
```

#### 400 — INVALID_JSON_SCHEMA

```json
{"requestId":"<hex>","error":"<message>","tag":"INVALID_JSON_SCHEMA"}
```

#### 401

```json
{"requestId":"<hex>","error":"<message>","tag":"INVALID_API_KEY"}
```

#### 402

```json
{"requestId":"<hex>","error":"<message>","tag":"NO_MORE_CREDITS"}
```

#### 422

```json
{"requestId":"<hex>","error":"<message>","tag":"INVALID_REQUEST"}
```

#### 429

```json
{"error":"You've exceeded your Exa rate limit of 10 requests per second. If you want this increased, please email hello@exa.ai :)"}
```

#### 500

```json
{"requestId":"<hex>","error":"<message>","tag":"INTERNAL_ERROR"}
```

#### 501

```json
{"requestId":"<hex>","error":"<message>","tag":"UNABLE_TO_GENERATE_RESPONSE"}
```

#### 200

```json
{"tag":"ERROR","payload":{"error":{"code":400,"message":"..."},"requestId":"..."}}
```

### How /answer differs from /search

Contracted against the recorded /search contract at `contracts/exa/README.md`. Same host (`https://api.exa.ai`), same auth headers, same error envelope family — but the request surface is drastically smaller and the response is a different document, not a variant of the /search one.

REQUEST — REMOVED (present on /search, absent from the /answer AnswerRequest schema, which has exactly four properties): type, numResults, category, moderation, compliance, includeDomains, excludeDomains, startPublishedDate, endPublishedDate, startCrawlDate, endCrawlDate, additionalQueries, contents (and its whole subtree: contents.text/highlights/summary/extras.links/extras.imageLinks/extras.codeBlocks/maxAgeHours/livecrawlTimeout/subpages/subpageTarget), useAutoprompt, livecrawl, context. Also absent from the schema though present on /search AND in the TS SDK's AnswerOptions: systemPrompt, userLocation — plus an SDK-only model:"exa". Those three are the only genuinely ambiguous request fields; everything else is a clean removal.
REQUEST — SHARED: query (string, required), stream (boolean, default false), outputSchema (object).
REQUEST — RESHAPED: `text`. On /search it is contents.text and accepts boolean|object; on /answer it is a top-level plain boolean with no object form. A simulator that shares one request decoder across both endpoints will wrongly accept {"contents":{"text":{...}}} on /answer and wrongly accept a text object.
REQUEST — ADDED: nothing. /answer's field set is a strict subset of /search's names.

RESPONSE — SHARED: requestId (string, 32-char lowercase hex), costDollars with the same CostDollarsOutput object ({total: number, search?: {neural?: number}}).
RESPONSE — ADDED: `answer` (string|object) — required, and the whole point of the endpoint.
RESPONSE — RENAMED + NARROWED: `results[]` becomes `citations[]`. The citation object is a SUBSET of the search result object: it carries id, url, title, author, publishedDate, text, image, favicon — and DROPS highlights, highlightScores, summary, subpages, extras, entities. Neither object has a `score`; the /search README's correction about the plan's fictional `score` applies verbatim to citations too, and there is no score-like field at all on /answer (not even highlightScores, since highlights don't exist here).
RESPONSE — REMOVED (present on /search, absent on /answer): results, resolvedSearchType, context, statuses, and the entire `output` object with output.content and output.grounding[] (field/citations/confidence).
RESPONSE — RESHAPED, the load-bearing difference for structured output: BOTH endpoints accept outputSchema, but they return the structured result in different places. /search returns output.content plus per-field output.grounding[] with confidence. /answer overloads the SAME `answer` key from string to object and provides NO grounding array — field-level provenance simply does not exist on /answer. A consumer written against /search's structured-output branch will find nothing at output.* on /answer.
RESPONSE — NULLABILITY DIFFERENCE: costDollars is ALWAYS present on /search but OPTIONAL in the /answer schema, and the documented /answer example emits {"total": 0.005} with no nested search breakdown.

ERRORS — SHARED: the flat {requestId, error, tag} envelope and the reduced 429 {error}-only shape.
ERRORS — ADDED: 501 / UNABLE_TO_GENERATE_RESPONSE, documented as /answer-only.
ERRORS — ADDED + RESHAPED: the in-stream error chunk {tag:"ERROR", payload:{error:{code,message}, requestId}}, which nests the error and uses a numeric code, contradicting the flat envelope used everywhere else.

The README's existing one-line /answer row is directionally right but incomplete and slightly wrong in one place: it omits `favicon` from the citation field list (favicon IS documented on /answer citations), omits the streaming contract entirely, omits the 501/UNABLE_TO_GENERATE_RESPONSE exclusive, and states costDollars without noting it is optional here.

### Simulator notes

- DISTINCT ENVELOPE, NOT A VARIANT. /answer returns {requestId, answer, citations[], costDollars} — there is no `results` key. Do not render it from the /search response builder with a rename; the citation object is a strict subset (no highlights, highlightScores, summary, subpages, extras, entities) and emitting those extra keys would teach consumers to parse fields the real API never sends on this route.
- NO SCORE ANYWHERE. Citations carry no `score`, and unlike /search there is not even a highlightScores fallback, because highlights do not exist on /answer. Any relevance-ranking consumer test must rank by array order alone.
- THE `answer` KEY IS A ONEOF ON ITSELF. Emit a JSON string when outputSchema is absent, and a JSON object matching the supplied schema when it is present. This is the highest-value branch to simulate and the easiest to get wrong: /search puts structured output under `output.content`, so a shared serialiser will put it in the wrong place. There is NO grounding array on /answer — do not synthesise output.grounding here.
- ID EQUALS URL IN PRACTICE. The documented citation example sets id to the same article URL as `url`. Keep the /search README's correction: ids are URL-shaped, not slugs like "source-1". requestId stays 32-char lowercase hex (example: a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6).
- GATE citations[].text ON THE REQUEST. text defaults to false; when false, omit the text key from every citation rather than emitting an empty string. This is the only content-gating knob on the endpoint — the whole contents{} subtree is gone.
- publishedDate FORMAT IS FULL ISO-8601 WITH MILLISECONDS AND Z ("2024-12-11T00:00:00.000Z"), and author is genuinely string-OR-null, not string-or-absent. Encode the null case explicitly — a simulator that only ever omits author will never exercise the null branch consumers must handle.
- costDollars IS OPTIONAL ON /answer (required on /search) AND ITS BREAKDOWN IS SPARSE. The documented example is {"total": 0.005} with no `search` sub-object. Default to the sparse form; make the {total, search:{neural}} form an explicit scenario knob.
- STREAMING IS A SEPARATE RENDER PATH, NOT A CHUNKED VERSION OF THE JSON BODY. stream:true switches Content-Type to text/event-stream. Four documented chunk kinds, in this order: (1) OpenAI-compatible content deltas {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"..."},"finish_reason":null}]}; (2) a citations chunk {"citations":[...]}; (3) a terminal metadata chunk carrying {"costDollars":{...},"requestId":"..."}; (4) an error chunk. requestId and costDollars arrive ONLY in that late chunk — a streaming consumer cannot read them from a header or a first chunk.
- RAW SSE FRAMING IS UNCONFIRMED. The docs describe the chunk JSON but never show raw wire lines, so the `data:` line prefix and the presence/absence of a terminating [DONE] sentinel are NOT verified. Pick one framing, mark it clearly as an assumption in the simulator, and put it behind a knob so it can be corrected without touching chunk construction. Do not present it as vendor-confirmed.
- IN-STREAM ERRORS BREAK THE ENVELOPE RULE. A fault injected mid-stream must serialise as {"tag":"ERROR","payload":{"error":{"code":400,"message":"..."},"requestId":"..."}} on an already-200 text/event-stream response — nested error object, numeric code, and `tag` holding the literal "ERROR" rather than a value from the documented tag enum. Reusing the flat {requestId,error,tag} serialiser here is the single most likely simulator bug on this surface.
- SIMULATE 501 / UNABLE_TO_GENERATE_RESPONSE. It is documented as /answer-only and is the 'model could not answer' path that has no /search analogue. A research product's error handling is incomplete without it, and it must NOT be reachable on /search in the simulator.
- REQUEST VALIDATION MUST DIVERGE FROM /search. Reject or ignore the /search-only fields — a request with type/numResults/includeDomains/contents against /answer should not be validated by the /search decoder. Treat model, systemPrompt and userLocation as accept-and-ignore (SDK sends them; the OpenAPI schema does not list them), and do not echo them.
- DO NOT IMPLEMENT /research. No vendor doc page exists for it; only a third-party page claims it was retired in favour of the Agent API. If an agentic surface is ever needed, the real one is the async POST /agent/runs + GET /agent/runs/{id} poll pattern, which is a fundamentally different lifecycle (create returns immediately, output only at terminal status) and must not be folded into /answer's synchronous shape.

## POST /agent/runs and GET /agent/runs/{id}

Verified against live vendor documentation on **2026-08-15**.

Read from:

- <https://exa.ai/docs/reference/agent-api/overview> — lifecycle, status and stopReason enums, `output` shape
- <https://exa.ai/docs/reference/agent-api-guide> — request example, run-object top-level fields, `usage`
- <https://exa.ai/docs/reference/agent-api/examples> — request fields, `budget`, `previous_run_id`
- <https://github.com/exa-labs/exa-js> — `costDollars.dataSources` and `usage.dataSources`

Note the documentation host moved: `docs.exa.ai/reference/*` now 307-redirects to `exa.ai/docs/reference/*`. The
older host still resolves, so a URL recorded before this date is not wrong, only indirect.

### Lifecycle

`POST /agent/runs` returns immediately with a run in a non-terminal status. The client then polls
`GET /agent/runs/{id}` until terminal. Output exists **only at a terminal status** — that is the whole reason this
surface needs a scenario shape a single request/response projection cannot express.

```text
queued -> running -> completed | failed | cancelled
```

`completed`, `failed` and `cancelled` are terminal. The vendor also documents
`GET /agent/runs`, `GET /agent/runs/{id}/events`, `POST /agent/runs/{id}/cancel` and `DELETE /agent/runs/{id}`;
none of those are in scope here and none are verified below.

### Request fields (POST /agent/runs)

| Field | Type | Required | Notes |
|---|---|---|---|
| `query` | `string` | yes | The research task. |
| `outputSchema` | `object` | no | JSON Schema shaping `output.structured`. **camelCase** — see the naming conflict below. |
| `effort` | `string` | no | Enum: `minimal`, `low`, `medium`, `high`, `xhigh`, `auto`, `max`. `max` is beta and requires a beta header. |
| `input` | `object` | no | Carries `data`, `exclusion`, or both. Sub-shapes NOT verified. |
| `previous_run_id` | `string` | no | Continues from a prior run. Note the snake_case, unlike `outputSchema`. |
| `data_sources` | — | no | Exa Connect premium partners. Shape NOT verified. |
| `budget` | `object` | no | Documented key: `maxCostDollars`. Other keys NOT verified. |
| `betas` | — | no | Beta feature tokens. Shape NOT verified. |

### Response fields (the run object, returned by both routes)

| Field | Type | Notes |
|---|---|---|
| `id` | `string` | The identifier a poll presents. Format NOT verified. |
| `status` | `string` | `queued`, `running`, `completed`, `failed`, `cancelled`. |
| `stopReason` | `string\|null` | `null` while queued or running. Terminal: `schema_satisfied`, `budget_reached`, `error`, `cancelled`. |
| `createdAt` | — | Type and format NOT verified. |
| `request` | `object` | Echo of the submitted request. Shape NOT verified. |
| `output` | `object\|null` | Present at terminal status. |
| `output.text` | `string` | Natural-language answer or summary. |
| `output.structured` | `object\|null` | Shaped by `outputSchema`; `null` when no schema was supplied. |
| `output.grounding` | — | Citations for text or structured fields. Shape NOT verified. |
| `usage` | `object` | Documented keys: `agentComputeUnits`, `dataSources`. |
| `costDollars` | `object` | The run's cost breakdown. Documented key: `dataSources`. **See below.** |

### What is NOT verified, and must not be invented

The vendor documentation confirms these fields exist without showing a complete example run object, so the
following are open and a simulator must not assert them from memory:

1. **`costDollars`' nested shape on this surface — see the recorded inference below.** No vendor page prints a
   complete agent-run JSON body, so the exact shape is derived rather than read. It is written down as an inference
   with its evidence rather than presented as verified.
2. **`createdAt`'s type and format.** `/search` has no analogue; do not assume the `publishedDate` ISO-8601 shape.
3. **The `id` format.** No example identifier appears. `/search`'s `requestId` is 32 lowercase hex, but nothing
   verifies that this surface matches it.
4. **`output.grounding`, `input`, `data_sources`, `betas` and `request` sub-shapes.**
5. **`outputSchema` versus `output_schema`.** The guide's example JSON and the overview's prose both use
   `outputSchema`; the examples page's prose says `output_schema`. Two sources to one, and the one that disagrees
   is prose rather than a code sample, so **camelCase is recorded** — but it is a documentation conflict rather
   than a settled fact, and a consumer sending snake_case should be accepted rather than rejected until it is.

### Recorded inference: `costDollars.total` is emitted on terminal runs

**Status: INFERRED, not read from a vendor example. Recorded 2026-08-15.** Servicesim emits it; this section is the
reason, so a future reader can re-open the decision against evidence rather than re-derive it.

The evidence chain, strongest first:

1. The Agent API documentation states that completed runs include `costDollars`, described as **"the run's cost
   breakdown"**. The field's presence on this surface is verified; only its interior is not.
2. `CostDollarsOutput` is a **shared schema component** in Exa's OpenAPI specification, used by `/search`,
   `/answer` and `/contents`. Within it, `total` is the aggregate float and is required. Two of those three are
   independently verified in this file already (`/search` above, `/answer` in its own section).
3. Exa's official JS SDK reads `completedRun.costDollars?.dataSources`, confirming the agent's `costDollars` is a
   real object with per-operation breakdown keys — the same shape family as `costDollars.search` elsewhere.

An aggregate-plus-breakdown object that leads with `total` across every other endpoint, on an orchestration surface
whose cost is by definition the sum of the operations it ran, is a strong inference. It is not a reading.

**The shape is a SUPERSET of `CostDollarsOutput`, not a copy**, and the distinction matters for what a simulator
emits. `dataSources` is confirmed on agent runs and does not appear in `CostDollarsOutput`; `search` appears in
`CostDollarsOutput` and is **not** confirmed on agent runs. So:

| Key | On agent runs | Basis |
|---|---|---|
| `costDollars.total` | emitted | inferred, this section |
| `costDollars.dataSources` | emitted only when a scenario declares it | verified, JS SDK |
| `costDollars.search` | **not emitted by default** | not confirmed on this surface — do not copy it across from `/search` |

**Why emit an inferred field at all**, when house rule 1 says never write a wire field from memory: this is not
memory, it is a documented inference with a citation trail, and the cost of deferring is asymmetric. Adding a cost
key *after* adopters hold golden files rewrites the bytes of every one of those files. Emitting it now and being
wrong costs one field's removal; omitting it now and being wrong costs an N-repository golden refresh. The rule
exists to stop unsourced invention, which this is not.

**How to falsify it.** One captured terminal run from the live API settles it in either direction. If it turns out
`costDollars` on this surface carries no `total`, correct this section first and the projection second — and record
the correction here rather than silently dropping the field.

### Exa Agent API — lifecycle routes not in scope

Exa also exposes an asynchronous agentic surface — `POST /agent/runs` plus a run lifecycle
(`GET /agent/runs`, `GET /agent/runs/{id}`, `GET /agent/runs/{id}/events`, `POST /agent/runs/{id}/cancel`,
`DELETE /agent/runs/{id}`). Servicesim does not simulate it today: its create-then-poll lifecycle needs a
different scenario shape than a single request/response projection, because a create returns immediately and the
output only exists at a terminal status. That is the whole reason, and it is a reason about Servicesim's scenario
model, not about who calls the endpoint. The surface is on the backlog. Exa's own guidance is "for simpler
low-latency retrieval, prefer /search".

This section previously carried a second reason — that no consumer used the endpoint. That claim was false: the
first adopter's client calls `POST /agent/runs` and `GET /agent/runs/{id}`. It has been struck rather than
corrected in place, because the claim should never have been made from either direction. This file records what
the *vendor's* contract is; whether some consumer does or does not call a route is not something it can verify,
and asserting it here is how a wrong statement acquired the authority of a verified one. State a scenario-model
reason, a verification-status reason, or no reason.

A `POST /research` endpoint appears in third-party integration documentation but not in Exa's own docs index.
Treat it as retired; do not simulate it.
