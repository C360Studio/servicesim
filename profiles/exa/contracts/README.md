# Exa consumed contract

Verified against live vendor documentation on **2026-08-14**.

This file records only what Servicesim simulates and what consumers parse. It is not a
redistribution of the vendor's OpenAPI document. Re-verify and update the date above on the
sanctioned dated re-verification cadence (the repository root's `contracts/README.md` "Keeping them honest" — there
is no live contract canary).

## Documentation sources

- <https://exa.ai/docs/reference/search>
- <https://exa.ai/docs/reference/search-api-guide-for-coding-agents>
- <https://exa.ai/docs/reference/answer>
- <https://exa.ai/docs/reference/error-codes>
- <https://apis.io/apis/exa-ai/exa-ai-search-api/>
- <https://exa.ai/docs/exa-spec.yaml> — the vendor's own OpenAPI spec; source of record for `/findSimilar` and,
  since 2026-10-01, for `/agent/runs` (see that section), and used to cross-check `/contents`.

## Endpoints

| Method | Path | Status | Note |
|---|---|---|---|
| `POST` | `/search` | canonical, simulated | Base URL <https://api.exa.ai>. Confirmed on both the OpenAPI-backed reference page and the coding-agents guide. |
| `POST` | `/answer` | canonical, simulated | Separate documented endpoint at <https://exa.ai/docs/reference/answer>. Same auth. Request: query (string, required), stream, text, outputSchema. Response: answer (string\|object), citations[] (title,url,publishedDate,author,id,image,text), requestId, costDollars. The plan doc does not mention /answer at all. |
| `POST` | `/contents` | canonical, verified 2026-08-15, simulated | See the "POST /contents" section below. |
| `POST` | `/findSimilar` | canonical per the live OpenAPI spec, DEPRECATED by the vendor, verified 2026-08-15, simulated | See the "POST /findSimilar" section below. The vendor's own OpenAPI spec documents this route in full and marks it `deprecated: true`, steering callers to `/search` instead; a prose reference page for it 404s, which is why an earlier pass wrongly recorded "no documentation found". |
| `POST` | `/agent/runs` | canonical per the live OpenAPI spec, verified 2026-10-01, simulated | Create-then-poll create route; answers `200`, the only documented success status. See the "POST /agent/runs" section below. |
| `GET` | `/agent/runs/{id}` | canonical per the live OpenAPI spec, verified 2026-10-01, simulated | Poll route. |
| `HEAD` | `/agent/runs/{id}` | simulator affordance, simulated | The spec documents no `head` operation on this path. Existence check; claims no turn or attempt. |
| `POST` | `/agent/runs/{id}/cancel` | canonical per the live OpenAPI spec, verified 2026-10-02, simulated | Cancel route. See "Cancel" in the "POST /agent/runs" section below. |
| — | `/agent/runs` (list), `/agent/runs/{id}/events`, `/agent/runs/{id}/stop`, `/agent/runs/{id}` (`DELETE`) | NOT SIMULATED | Documented by the spec; on the backlog. `/stop` is deliberately not aliased to cancel. See the "POST /agent/runs" section below. |

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

Contracted against the recorded /search contract in this same file's "POST /search" section. Same host (`https://api.exa.ai`), same auth headers, same error envelope family — but the request surface is drastically smaller and the response is a different document, not a variant of the /search one.

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
- DO NOT IMPLEMENT /research. No vendor doc page exists for it; only a third-party page claims it was retired in favour of the Agent API. If an agentic surface is ever needed, the real one is the async POST /agent/runs + GET /agent/runs/{id} poll pattern, which is a fundamentally different lifecycle (create returns immediately and the run is polled to a terminal status) and must not be folded into /answer's synchronous shape.

## POST /agent/runs and GET /agent/runs/{id}

Verified against the vendor's own OpenAPI document on **2026-10-01**. Every classification below is one of **VERIFIED**
(read from the spec), **SIMULATOR-POLICY** (the spec fixes a shape but not a value, and Servicesim chose one),
**INFERENCE** (a reading, with its evidence stated) or **UNVERIFIED** (not read, not simulated, not to be invented).
Line numbers are of the fetch below.

- <https://exa.ai/docs/exa-spec.yaml> — `info.version` 2.0.0, retrieved 2026-10-01, sha256
  `cbb9a1429456270527cec49cb495ec00671dd2b7702c411a168988e22222416d`. Operations `createAgentRun` (line 814) and
  `getAgentRun` (line 976); schemas `CreateAgentRunRequest` (4754), `AgentRun` (5793) and `AgentErrorResponse` (6044),
  with everything they reference. Re-fetched 2026-10-02 with an **identical** hash, and `cancelAgentRun` (line 1153)
  and `stopAgentRun` (line 1242) read against it for the cancel route.
- The full field-by-field record — each consumed field's classification after the fixes, how to re-fetch, and what
  changed against the profile — is [`docs/audits/2026-10-01-exa-agent.md`](../../../docs/audits/2026-10-01-exa-agent.md).

**The spec is the authority, including over this section's own earlier text.** This section was first written
(2026-08-15) from the vendor's prose pages, and the spec disagrees with several of its claims: `max` is not an
`effort` value, run ids carry an `agent_run_` prefix, the create answers 200 and not 201, `costDollars.search` is a
required number, `output` exists on every run, `createdAt` is a date-time, the grounding shape is known, and the
request fields are camelCase throughout. Each is corrected in place below.

The spec has **no example bodies**. Where it fixes a key's presence and type but not its value, that value is
SIMULATOR-POLICY and no vendor guarantee — and this applies above all to billing and metering.

### Lifecycle

`POST /agent/runs` returns the run object immediately; the only documented success status is `200`
(`createAgentRun`, line 831). The client then polls `GET /agent/runs/{id}` until the run is terminal. The run object
has the same ten keys on every status (below), so `output`, `usage` and `costDollars` exist on a queued or running
run too.

```text
queued -> running -> completed | failed | cancelled
```

`completed`, `failed` and `cancelled` are terminal (`AgentRunStatus`, line 5840). A queued or running run can be
cancelled — see [Cancel](#cancel-post-agentrunsidcancel). The spec also documents list, `/events`, `/stop` and `DELETE`
operations; none of those are simulated and none are described here beyond the `/stop` note under Cancel.

### Request fields (POST /agent/runs)

`CreateAgentRunRequest`, line 4754. It has no `additionalProperties` keyword, so whether the live API rejects an
unknown key is UNVERIFIED; the profile accepts and ignores them.

| Field | Type | Required | Spec | Servicesim |
|---|---|---|---|---|
| `query` | `string` | yes | `minLength: 1` (4757) | rejected when absent, non-string or blank (blank is stricter than the spec) |
| `systemPrompt` | `string` | no | 4761 | accepted, ignored |
| `effort` | `string` | no | closed enum, default `auto` (`AgentEffort`, 4819) | enum enforced: a 400 outside it, `null` and non-strings included |
| `input` | `object` | no | `{data[], exclusion[]}` of JSON object records (4766) | accepted, ignored |
| `outputSchema` | `object\|null` | no | camelCase (4790); draft-07, 2019-09 or 2020-12 via `$schema` | accepted, ignored |
| `previousRunId` | `string` | no | `AgentRunId`; "must belong to the same team" (4799) | accepted, ignored |
| `metadata` | `map<string,string>` | no | 4802 | accepted, ignored |
| `dataSources` | `array` | no | `maxItems: 5` of `{provider}` over nine providers (4809, 4837-4857) | accepted, ignored |
| `budget` | `object` | no | `AgentBudget` (4858) | type and range of the two limits enforced, below |

`effort` values, in the spec's order: `minimal`, `low`, `medium`, `high`, `xhigh`, `auto`, `ultra`. `max` is **not** a
member. An earlier reading of the prose pages listed it (and a beta header for it) and left `ultra` out; the spec has
it the other way round. The spec states the enum but not the live API's response to a violation, so rejecting with a
400 (`exa.effort.invalid`, whose message quotes the offending value as JSON — `null`, `"max"`) is strict-request
policy (house rule 5), not a vendor guarantee.

`budget` is `{maxCostDollars, maxDurationSeconds}`. The ranges are stated in the spec's prose only, not as
`minimum`/`maximum` keywords: `maxCostDollars` is a number that "accepts $1–$100" (line 4863) and
`maxDurationSeconds` an integer that "accepts 300–10,800" (line 4866), the latter ending a run with
`stopReason: "time_limit_reached"`. The same prose says `maxCostDollars` "applies only to `auto` and `ultra`" and
`maxDurationSeconds` "only to `ultra`". What the live API answers to a violation is UNVERIFIED, so the enforcement
below is **SIMULATOR-POLICY**, including how it reads "applies only to":

- A member of the wrong type — a non-number `maxCostDollars`, a non-integer `maxDurationSeconds`, a non-object
  `budget` — is always a 400 (`exa.request.field_type`), whatever the effort.
- An out-of-range value is a 400 (`exa.budget.maxCostDollars.range`, `exa.budget.maxDurationSeconds.range`) only when
  the limit applies to the request's effort: `maxCostDollars` on `auto` and `ultra`, `maxDurationSeconds` on `ultra`
  alone, and an omitted `effort` is the default, `auto`. Under any other effort the limit has no meaning, so the
  request is served unchanged and the same code is a warning finding in the journal.
- The out-of-range message formats the offending number as sent (`1e+300`, not an integer conversion), so the bytes do
  not depend on the platform.

Two header parameters exist: `Accept` (`application/json` or `text/event-stream`, line 11173) and `Exa-Beta`
(comma-separated beta tokens, line 11183). Neither is read: a streamed create (`AgentRunEvent`, line 840) is not
simulated and no beta token is enforced. Both are UNVERIFIED beyond their declarations.

### Response fields (the run object, returned by both routes)

`AgentRun`, line 5793: ten required keys, `additionalProperties: false`. The profile renders these ten keys, in the
order below, on every response. A snapshot whose scenario uses `extra_fields` also carries the extras and is keyed
alphabetically, because `provider.Render` re-sorts a body that merges extras; that is the one case where neither the
key set nor the order is the schema's. `profiles/exa/agentrun_envelope_test.go` pins the key sets and the value rules
named in the tables below (closed enums, required nullability, finite `>= 0` numbers, integer counters); it does not
validate a body against the whole schema.

| Field | Type | Spec | Servicesim |
|---|---|---|---|
| `id` | `string` | `AgentRunId`: pattern `^[A-Za-z0-9_.:-]+$`, 1-200, "new run IDs are returned with the `agent_run_` prefix" (4831-4836) | `agent_run_` plus 32 hex characters, derived from the scenario seed, the create's lane and its call index (SIMULATOR-POLICY for the suffix) |
| `object` | `string` | `const: agent_run` (5798) | the constant |
| `status` | `string` | the five-value enum (5840) | as scripted |
| `stopReason` | `string\|null` | six values, `null` while queued or running (5803, 5848) | `null` until terminal; a terminal status derives one unless scripted (SIMULATOR-POLICY, below) |
| `createdAt` | `string` | `date-time` (5808) | `time.base`, RFC 3339 with milliseconds |
| `completedAt` | `string\|null` | `date-time` or `null` (5812) | `null` until terminal, then `time.base` (SIMULATOR-POLICY) |
| `request` | `object\|null` | `AgentRunRequest`, "canonicalized request fields stored with the run" (5818, 5857) | always `null` (SIMULATOR-POLICY, below) |
| `output` | `object` | `AgentRunOutput`, required, non-nullable (5822, 5921) | scripted, else the empty placeholder |
| `usage` | `object` | `AgentUsage`, required, non-nullable (5824, 5978) | scripted, else zeros |
| `costDollars` | `object` | `AgentCostDollars`, required, non-nullable (5826, 6009) | scripted, else zeros |

`stopReason` members: `schema_satisfied`, `budget_reached`, `time_limit_reached`, `stopped`, `error`, `cancelled`.

| Nested field | Type | Spec | Servicesim |
|---|---|---|---|
| `output.text` | `string` | required (5924, 5938) | always present, `""` when unscripted |
| `output.structured` | `JsonValue\|null` | required; `null` when no schema was provided (5927) | always present, `null` when unscripted |
| `output.grounding` | `array` | required (5932, 5940) | always present, `[]` when unscripted |
| `output.grounding[].field` | `string` | required (5945) | as scripted |
| `output.grounding[].citations` | `array` | required (5948) | always present, `[]` when empty |
| `output.grounding[].confidence` | `low\|medium\|high\|null` | optional (5952) | present only when scripted |
| `…citations[].url` | `string` | required, `format: uri` (5968) | resolved from the corpus |
| `…citations[].title` | `string` | optional (5972) | resolved from the corpus |
| `usage.agentComputeUnits` | `number` | required, `>= 0` (5981) | scripted, else `0` |
| `usage.searches`, `.emails`, `.phoneNumbers` | `integer` | required, `>= 0` (5984-5992) | scripted, else `0` |
| `usage.dataSources` | `map<string,integer>` | optional; only providers with non-zero usage (5993, 6001) | present only when scripted and non-empty |
| `costDollars.total` | `number` | required, `>= 0` (6012) | scripted, else `0` |
| `costDollars.agentCompute`, `.search`, `.emails`, `.phoneNumbers` | `number` | required, `>= 0` (6015-6026) | scripted, else `0` |
| `costDollars.dataSources` | `map<string,number>` | optional; only providers with non-zero usage (6027, 6036) | present only when scripted and non-empty |

A citation is `{url, title}` and **nothing else**: an earlier version of the profile also emitted an `id`, which the
schema (`additionalProperties: false`, line 5977) does not allow.

#### Decision: `costDollars.search` is a number on this surface

The earlier version of this file recorded that `costDollars.search` was *not confirmed* on this surface and
deliberately omitted it, reading the agent's `costDollars` as a superset of the shared `CostDollarsOutput`. The spec
shows otherwise: `AgentCostDollars` is its own schema, `search` is a plain **number** in it and is required (lines
6018 and 6032), and it is not the `{neural}` object `/search` carries (`CostDollarsOutput.search`, line 7252). The
decision is reversed. The earlier "recorded inference" that `total` is emitted is now VERIFIED by the same schema
(line 6012), and its evidence chain is no longer needed.

#### Simulator policy where the spec is silent

None of the following is a claim about what the live service sends:

- **Placeholders.** A zero (or empty value) that the scenario did not script is a **placeholder for a required key**,
  not a claim about billing or metering. The spec marks `stopReason`, `completedAt` and `request` as the nullable
  keys and `output`, `usage` and `costDollars` as non-nullable, which suggests the live API sends zero-valued
  objects on a run that has not finished — that is an INFERENCE from the nullability marking, and the runtime is
  UNVERIFIED. The cost components are not derived from, and need not sum to, `total`. A scripted value always wins
  over a placeholder, on any snapshot that declares it.
- **`completedAt`** is `null` while queued or running and `time.base` once terminal. There is no elapsed-time model, so
  a run never takes any time and `createdAt` equals `completedAt`.
- **`request`** is `null`, which the schema permits. Echoing the request would mean retaining the create body, and the
  job record is immutable and body-free by design (house rule 4 would also apply: redact before retention). The echo
  is deferred.
- **`stopReason` derivation.** The spec pairs no status with a reason. A terminal run derives `error` for `failed`,
  `cancelled` for `cancelled` and `schema_satisfied` otherwise, unless the scenario scripts one.
- **Run-level `error`.** `AgentRun` has no `error` key and is `additionalProperties: false` (line 5839), so a failed
  run is `status: failed` plus `stopReason: error` and nothing more. The earlier `{code, message}` object was
  Servicesim's own construction, and a scenario that still declares one is a load error
  (`exa.agent_run.error.not_in_schema`).
- **`extra_fields`** can still add keys outside the schema, on purpose: it exists so a consumer can prove it tolerates
  additions (house rule 5), which the schema itself does not. Such a snapshot is keyed alphabetically (above); the
  create body carries none.
- **HEAD `/agent/runs/{id}`.** The spec defines `get` (line 977) and `delete` (line 1065) on that path and no `head`.
  The route is a Servicesim existence check that claims no poll; it carries `x-request-id` for uniformity.
- **A run id the process never minted** is a 404 `RUN_NOT_FOUND`, whatever its shape. The spec lists a 400 for `GET`
  (line 1001) and `AgentRunId` permits `.` and `:`, which the profile's id check does not; which one a malformed id
  gets from the live API is UNVERIFIED.

### Error bodies

Every agent operation returns `AgentErrorResponse` on failure (for example lines 851, 860, 869 and 878 on the create,
and 1009 through 1045 on the poll):

```json
{"error": {"type": "NOT_FOUND", "code": "RUN_NOT_FOUND", "message": "Run not found"}}
```

`type` is one of `INVALID_REQUEST`, `AUTHENTICATION_ERROR`, `RATE_LIMIT_ERROR`, `NOT_FOUND`, `SERVER_ERROR`; `code` is
one of `INVALID_REQUEST`, `TEAM_NOT_FOUND`, `RUN_NOT_FOUND`, `PREVIOUS_RUN_NOT_FOUND`, `PREVIOUS_RUN_NOT_COMPLETED`,
`CONCURRENCY_LIMIT_REACHED`, `INVALID_OUTPUT_SCHEMA`, `INVALID_DATA_SOURCE`, `TIMEOUT`, `SERVER_ERROR`; all three keys
are required and extra keys are allowed (lines 6052-6083). This is **not** the flat `{requestId, error, tag}` body
the other Exa routes use, which is unchanged.

`createAgentRun` documents 200, 400, 401, 429 and 500; `getAgentRun` and `cancelAgentRun` document 200, 400, 401, 404,
429 and 500. The spec **does not pair a `type` and `code` with a status**. The pairing below is **INFERENCE** from the enum names and
the response descriptions, and the messages are the response descriptions reused:

| Status | `type` | `code` | `message` |
|---|---|---|---|
| 400 | `INVALID_REQUEST` | `INVALID_REQUEST` | the rejecting finding's text; "Invalid request" for a scripted fault |
| 401 | `AUTHENTICATION_ERROR` | `TEAM_NOT_FOUND` | "Team context or authentication was not found" |
| 404 | `NOT_FOUND` | `RUN_NOT_FOUND` | "Run not found" |
| 429 | `RATE_LIMIT_ERROR` | `CONCURRENCY_LIMIT_REACHED` | "Agent run concurrency limit reached" |
| 500 | `SERVER_ERROR` | `SERVER_ERROR` | "Server error" |

Statuses the spec does not document for these operations (402, 405, 413, 422, 501, 503, …) are SIMULATOR-POLICY: any
other 4xx is `INVALID_REQUEST`/`INVALID_REQUEST`, any other 5xx `SERVER_ERROR`/`SERVER_ERROR`, and the job-bound 503
keeps its remedy as the message. The codes `PREVIOUS_RUN_NOT_FOUND`, `PREVIOUS_RUN_NOT_COMPLETED`,
`INVALID_OUTPUT_SCHEMA`, `INVALID_DATA_SOURCE` and `TIMEOUT` exist in the enum and are not simulated: no check the
profile makes corresponds to them.

**Scenario faults.** A fault attempt carries the flat envelope's `error:` and `tag:`. On the agent routes `error:`
becomes `message` and `tag:` becomes `code`, verbatim — a code outside the enum is allowed on purpose — while `type`
follows the status. An attempt's own `body:` wins outright. This mapping is Servicesim's.

**`x-request-id`.** The spec documents the header on every status of every agent operation (line 11084); agent bodies
carry no `requestId`, so the header is the only request id a response has. Its value uses the same deterministic
derivation as `requestId` on the other routes, per call once an attempt is claimed: a create's id follows the create
lane's call index, and a poll's also folds in the run id, so two polls of one run differ and so do the first polls of
two runs. Two rejections in one lane, and a HEAD, carry the unclaimed id, which has no call index. Whether the live
header is per call is UNVERIFIED. The header is **absent** on a refusal the framework renders without a handler's
response — an unrouted path, a method the mux refuses, a handler panic — because a profile's error renderer returns a
body only. The unrouted and method refusals keep the flat body; a panic on an agent route answers the nested body,
just without the header.

### Findings this profile raises on the agent routes

All of these are Servicesim's, not the vendor's; the schema and conditions are in `docs/scenario-schema.md`.

- **At request time** (`POST /agent/runs`): `exa.effort.invalid` (error), `exa.budget.maxDurationSeconds.range` and
  `exa.budget.maxCostDollars.range` (an error when the limit applies to the effort, otherwise a warning — see
  "Request fields").
- **At request time** (`POST /agent/runs/{id}/cancel`): `job.cancel_unscripted` and `job.cancel_contended` (errors,
  answered with the 500 under Cancel).
- **At scenario load**: `exa.agent_run.error.not_in_schema` (error: a run-level `error:`),
  `exa.agent_run.value.range` (error: a negative or non-finite `usage` or `cost_dollars` value, `data_sources`
  included), `exa.agent_run.fault_tag.unknown` (warning: a `tag:` outside the ten `code` values, still rendered
  verbatim), `exa.agent_run.cost_unscripted` (warning: a terminal snapshot that scripts no `cost_dollars.total`, under
  Cancel) and `exa.output.grounding.confidence.unknown` (warning).

### Cancel (POST /agent/runs/{id}/cancel)

Verified 2026-10-02 against the same spec, hash unchanged. Classifications as above.

**What the spec says** (`cancelAgentRun`, lines 1153-1241) — VERIFIED:

- The path is `/agent/runs/{id}/cancel`, `POST`, with the run id as its only parameter (`AgentRunId`). There is **no
  request body**: the operation declares no `requestBody`.
- "Cancel a queued or running Agent run immediately without returning any results. You are billed for usage accrued
  before cancellation. If the run has already reached a terminal status, the API returns the existing run." (line 1157)
- `200` is an `AgentRun`. The errors are `400` ("Invalid request."), `401`, `404` ("Run not found."), `429` and `500`
  ("Server error or run timeout."), every one an `AgentErrorResponse` with the `x-request-id` header.

**What the profile does:**

- **The response is the run's next poll.** A `200` cancel answers exactly the snapshot the run's next `GET` will
  return, rendered by the same projection and renderer, so the cancel and the polls after it cannot disagree. Which
  snapshot that is depends on where the run is (SIMULATOR-POLICY; the mechanism is
  [`docs/scenario-schema.md`](../../../docs/scenario-schema.md#cancelling-a-job)'s "Cancelling a job"):
  - The next poll would be **terminal**: the cancel answers that terminal run and records nothing — the spec's "returns
    the existing run", judged by the run's next poll rather than the last one a client happened to see.
  - Otherwise the cancel is **recorded** at the run's position and answers the first snapshot of the scenario's
    `cancel.turns`; every later poll is served from `cancel.turns`.
  - A cancel of a run already cancelling records nothing new and answers the `cancel.turns` snapshot for its next poll.
- **No body is read.** A JSON object sent anyway is accepted and ignored — the spec declares none, so no rule is
  invented for one — and the request answers exactly as a bare one does. A body that is not a JSON object is refused
  by the shared request lifecycle before the handler runs, as on every route: a `400` `INVALID_REQUEST` that claims
  nothing (SIMULATOR-POLICY: with no body declared, the spec says nothing about a malformed one). No `Content-Type`
  is required; the spec's own sample sends none.
- **Billing.** "Usage accrued before cancellation" is billed, so the cancelled snapshot's `usage` and `costDollars` are
  what the scenario scripts in `cancel.turns`; unscripted they are the zero placeholders, which are no billing fact.
  The load warning below says so. What the live API reports between a cancel and the terminal `cancelled` status, and
  whether a cancelled run's `output` is always empty ("without returning any results"), are UNVERIFIED: the scenario
  scripts both, and nothing checks them.
- **Statuses this profile can produce:**

  | Status | When | Body |
  |---|---|---|
  | `200` | the cancel was recorded, repeated, or found the run's next poll terminal | `AgentRun` |
  | `400` | a body that is not a JSON object (SIMULATOR-POLICY); or a scripted `cancel.fault` attempt | `INVALID_REQUEST` / `INVALID_REQUEST`, the finding's text (a scripted fault: its own body, "Invalid request" by default, pinned by `exa-agent-runs-cancel-400.json`) |
  | `401` | no credential in an accepted header; a credential that does not match `auth.expect_key`, only when the scenario sets one; or any request under `auth.mode: reject` | `AUTHENTICATION_ERROR` / `TEAM_NOT_FOUND` |
  | `404` | a run this namespace does not hold: never minted, minted in another namespace, or a malformed id; or a run a reset removed after the cancel resolved it | `NOT_FOUND` / `RUN_NOT_FOUND` |
  | `429` | only a scripted `cancel.fault` attempt | `RATE_LIMIT_ERROR` / `CONCURRENCY_LIMIT_REACHED`, "Agent run concurrency limit reached" by default, pinned by `exa-agent-runs-cancel-429.json` (an attempt's own `error:` replaces the message and `tag:` the code) |
  | `500` | the cancel would take effect but the scenario cannot answer the polls after it (`job.cancel_unscripted`), or the run's position kept moving (`job.cancel_contended`); or a scripted `cancel.fault` attempt | `SERVER_ERROR` / `SERVER_ERROR`, "Internal server error" (SIMULATOR-POLICY, below; a scripted fault: its own body, "Server error" by default) |
  | `413` | a body over `--max-request-bytes`, refused by the shared request lifecycle as on every route (SIMULATOR-POLICY) | `INVALID_REQUEST` / `INVALID_REQUEST`, the finding's text |
  | `503` | a request in a new namespace while the process is at its `--max-namespaces` bound (SIMULATOR-POLICY) | `SERVER_ERROR` / `SERVER_ERROR`, the remedy as the message |
  | any other | only a scripted `cancel.fault` attempt, whose status the spec does not document for this operation | the fault's, mapped as SIMULATOR-POLICY (see "Error bodies": any other 4xx `INVALID_REQUEST`, any other 5xx `SERVER_ERROR`) |

  The 401, the refused-body 400 and 413, the 503 and the 404 of a run that never resolved claim no cancel attempt and
  record nothing.
  Every cancel that resolves a run claims exactly one attempt of the run's own cancel budget. The 404 of a run a reset
  removed after the cancel resolved it is the served response to that attempt, so a scripted `cancel.fault` on it
  applies. The 500 spends its attempt too: its index stays spent, its scripted fault is not applied
  (`fault.attempt_on_rejection`, a warning beside the error finding), and nothing is recorded on the run.

  The 500 of a cancel the simulator could not decide carries the message "Internal server error", the fixed message
  of the profile's other simulator-internal 500s (SIMULATOR-POLICY). It is deliberately neither the "Server error" a
  scripted `{status: 500}` renders by default (the "Error bodies" table) nor the spec's "Server error or run timeout.":
  it reports the simulator's own failure to answer, which a consumer can tell apart from a scripted vendor error.
- **Faults.** `cancel.fault` is the route's own attempt budget (`exa:agent_runs.cancel`), per run, independent of the
  create's and the poll's. A cancel is recorded only when its attempt commits — the client receives the body, or the
  attempt says `accepted: true` (the cancel took effect and its reply was lost). A scripted `{status: 500}` records
  nothing.
- **`x-request-id`** on a served response — a `200`, a scripted fault, or the `404` of a run a reset removed after
  the cancel resolved it — is per call and folds in the run id, like the poll's; the route is part of the derivation,
  so a cancel and a poll of one run at the same call index carry different ids. The `400`, `401`, `413`, `503` and
  never-resolved `404` refusals and the `500` of a cancel that could not be decided carry the route's unclaimed id
  instead, which is the same for every run and every call.
- **Journal labels** (the journal keeps no bodies, so these are how a consumer reads what happened):
  `exa.agent_runs.cancel.accepted` for a recorded cancel and nothing else; `exa.agent_runs.cancel.repeated` for a
  cancel of a run already cancelling; `exa.agent_runs.cancel.terminal` when completion won;
  `exa.agent_runs.cancel.unrecorded` when the attempt did not commit (its scripted fault replaced the body). Refusals
  keep `exa.error.<tag>`. A poll is labelled `exa.agent_runs.polled.<status>` with the status its snapshot scripts
  (`running` when it scripts none), so a confirmed cancellation is a `…polled.cancelled` entry that carries no fault.
  An `extra_fields` key that overrides `status` changes the wire and not the label. The label is the handler's even on
  a faulted attempt.
- **The cost rule** (ruling 6 on issue #6, tightened by the owner on 2026-10-02): every terminal Exa snapshot —
  `completed`, `failed` and `cancelled` alike, in `turns` and in `cancel.turns` — that scripts no `cost_dollars.total`
  raises the load warning `exa.agent_run.cost_unscripted`. That is a snapshot with no `cost_dollars`, with an empty
  one, or with one that scripts only other meters (`{search: 0.01}`): `total` still renders an unscripted `0` in each.
  An explicit `total: 0` is a statement and counts as scripted. Rendering is unchanged: `costDollars` is required, so
  it renders zero placeholders, and the warning is the signal that those zeros are not a billing fact. The rule judges
  the decoded scenario data, not the rendered response: `extra_fields` replaces whole top-level keys on the wire, so it
  can defeat the rule in either direction (`cost_dollars: {total: 0.01}` plus
  `extra_fields: {costDollars: {search: 0.5}}` raises no warning yet renders no `total`), an older gap tracked in
  [#22](https://github.com/C360Studio/servicesim/issues/22).
- **Other methods on the path** are the mux's, measured: `GET`, `HEAD`, `PUT` and `DELETE` on
  `/agent/runs/{id}/cancel` answer `405` with `Allow: POST`, in the flat refusal shape (no body for `HEAD`) and with no
  `x-request-id`, and claim or record nothing. So is an empty id: `POST /agent/runs//cancel` gets the mux's path-clean
  `307` to `/agent/runs/cancel`, with no provider-shaped body, no `x-request-id` and no journal entry.
- **Known limit, measured, no promise: jobs are scoped by namespace and entry, not by scenario.** In a process serving
  several scenarios, a run created under one and cancelled through another is answered from the cancelling
  scenario's `cancel.turns`, while its next poll through the creating scenario serves that scenario's `cancel.turns`,
  so "a cancel answers the next poll's snapshot" does not hold across scenarios.

**`/stop` is not cancel.** `stopAgentRun` (line 1242) is a separate operation: it "complete[s] a running Agent run early,
returning the results gathered so far", is "currently supported only for `ultra` effort runs", and is billed the same
way. It is NOT simulated and deliberately NOT aliased to cancel — a stopped run ends with its results and a different
outcome, and answering it with a cancel would hand a consumer the wrong lifecycle. It answers `404` until it is built.

### What is NOT verified, and must not be invented

1. **Values the spec does not give.** No example body exists, so every value in the golden fixtures that the scenario
   scripts is the scenario's, and every placeholder is SIMULATOR-POLICY. Nothing here asserts a vendor value for
   `usage`, `costDollars`, `completedAt` or `request`, and `AgentCostDollars` carries no description: do not assert
   that it is an estimate (the "estimated" wording at line 7249 belongs to `CostDollarsOutput`).
2. **Streaming.** `Accept: text/event-stream` on the create, and the `AgentRunEvent` stream, are not simulated.
3. **Request-side semantics beyond the enum and the budget ranges:** `previousRunId` continuation, `dataSources`
   validation, `outputSchema` validation, the `Exa-Beta` header, unknown request keys, and what a budget limit sent
   with the wrong effort does.
4. **The other lifecycle operations.** List, `/events`, `/stop` and `DELETE` are documented by the spec and are NOT
   simulated; the mux refuses them in the flat shape and with no `x-request-id`. `GET /agent/runs` and
   `DELETE /agent/runs/{id}` share a path with a simulated operation, so they answer `405` with `Allow: POST` and
   `Allow: GET, HEAD`; `/stop` and `/events` are not routed and answer `404`. Nothing about their bodies is recorded
   here.
5. **What a malformed run id returns,** and whether the live `x-request-id` is per call.

### Exa Agent API — create, poll, HEAD and cancel are simulated; the rest of the lifecycle is not

Exa also exposes an asynchronous agentic surface: `POST /agent/runs` mints a run, and everything interesting —
progress, the terminal output, failure — lives on `GET /agent/runs/{id}`, which a consumer polls. Servicesim
simulates the create, poll, `HEAD /agent/runs/{id}` (existence-only) and `POST /agent/runs/{id}/cancel` routes, driven
by the scenario provider entry `exa_agent_runs` — see `docs/scenario-schema.md`'s async section for the projection
shape, and `docs/design/async-jobs.md` for why this needed a different scenario shape than a single request/response
projection (a create returns immediately and the run is polled through successive snapshots).

Exa's remaining lifecycle routes — `GET /agent/runs` (list), `GET /agent/runs/{id}/events`,
`POST /agent/runs/{id}/stop` and `DELETE /agent/runs/{id}` — are NOT simulated. They are on the backlog and are
refused in the flat shape until built — a `405` for the list and `DELETE` (which share a path with a simulated
operation), a `404` for the other two. Exa's own guidance is "for simpler low-latency retrieval, prefer /search".

A `POST /research` endpoint appears in third-party integration documentation but not in Exa's own docs index.
Treat it as retired; do not simulate it.

## POST /contents

Verified against live vendor documentation on **2026-08-15**.

Sources:

- <https://exa.ai/docs/reference/get-contents>
- <https://exa.ai/docs/reference/contents-api-guide-for-coding-agents> — cross-checked; no mention of `findSimilar`
  anywhere on it, but it is not a strict subset of the reference page above: it types `summary` as `boolean|object`
  where the reference page's OpenAPI types it `object|null`, and its Request-Level Errors table names `400`, `401`,
  `422` and `429` for `/contents`, where 422 does not otherwise appear on the reference page. Both disagreements are
  recorded below rather than silently resolved one way.
- <https://exa.ai/docs/reference/error-codes> — already cited above for `/search` and `/answer`; the page states
  the `CRAWL_*` status tags below are "specific to the `/contents` endpoint and are not returned by `/search`." It
  also names `/contents`-specific fault tags outside the `statuses[]` mechanism: `ROBOTS_FILTER_FAILED` (403,
  "`/contents` only — all requested URLs were blocked by robots.txt"), `FETCH_DOCUMENT_ERROR` (422, "A specific URL
  could not be processed"), `NO_CONTENT_FOUND` (400, "No contents could be found for the given URLs") and
  `INVALID_URLS` (400, "One or more URLs/IDs are in an invalid format").
- <https://exa.ai/docs/exa-spec.yaml> — the vendor's own OpenAPI spec (linked from the docs index's "OpenAPI
  Specification" entry), `title: Exa Public API`, `version: 2.0.0`. Confirmed as canonical for `/contents` and used
  above for the `anyOf [<type>, null]` nullability and the `entities`/`extras`/`statuses` schemas below.

**Status: canonical, verified, simulated.** Implemented on `profiles/exa`, exercised by
`profiles/exa/contents_test.go` and `profiles/exa/contents_golden_test.go`, with fixtures in this directory.
The repository root's `contracts/README.md`'s index table and `scripts/check-docs.sh` are the source of truth for what the binary
registers.

### Auth

The OpenAPI spec's top-level `security` for this route lists two schemes: `apiKey` (the `x-api-key` header,
matching the code samples on the page) and `bearer` (`Authorization: Bearer <token>`, matching the guide's "Pass
your API key via the `Authorization: Bearer` header."). Both forms are vendor-documented for `/contents`.

### Request

Exactly one of `ids` or `urls` is required — **one-of, not both, not neither.** A validator that requires both,
or silently prefers one over the other, rejects valid production traffic.

| Field | Type | Required | Range / Enum | Default | Notes |
|---|---|---|---|---|---|
| `ids` | `array[string]` | one-of with `urls` | 1–100 items, 1–2048 chars each | — | Document IDs from a prior Exa search. |
| `urls` | `array[string]` | one-of with `ids` | 1–100 items, 1–2048 chars each | — | URLs to crawl directly. |
| `compliance` | `string` | no | `hipaa` | — | Enterprise-only, cache-only retrieval. |
| `text` | `boolean\|object` | no | — | `false` | **Union**, same duality as `/search`'s `contents.text`. See below. |
| `text.maxCharacters` | `integer` | no | 1–10000 | — | |
| `text.includeHtmlTags` | `boolean` | no | — | `false` | |
| `text.verbosity` | `string` | no | `compact`, `standard`, `full` | `compact` | |
| `text.includeSections` | `array[string]` | no | `header`, `navigation`, `banner`, `body`, `sidebar`, `footer`, `metadata` | — | |
| `text.excludeSections` | `array[string]` | no | same enum as `includeSections` | — | |
| `highlights` | `boolean\|object` | no | — | `false` | **Union.** |
| `highlights.query` | `string` | no | — | — | Guides LLM highlight selection. |
| `highlights.maxCharacters` | `integer` | no | 1–10000 | — | Total per URL. |
| `highlights.numSentences` | `integer` | no | ≥1 | — | **DEPRECATED.** |
| `highlights.highlightsPerUrl` | `integer` | no | ≥1 | — | **DEPRECATED, ignored.** |
| `summary` | `boolean\|object` (per the guide) / `object` (per the OpenAPI spec) | no | — | — | **Vendor pages disagree — see below.** |
| `summary.query` | `string` | no | — | — | |
| `summary.schema` | `object` | no | — | — | JSON Schema draft-07 for structured summary output. |
| `extras.links` | `integer` | no | 0–1000 | `0` | |
| `extras.imageLinks` | `integer` | no | 0–1000 | `0` | **Not on `/search`'s `contents.extras`** — see divergence note below. |
| `extras.richImageLinks` | `integer` | no | 0–1000 | `0` | **Not on `/search`'s `contents.extras`** — see divergence note below. |
| `extras.richLinks` | `integer` | no | 0–1000 | `0` | **Not on `/search`'s `contents.extras`** — see divergence note below. |
| `extras.codeBlocks` | `integer` | no | 0–1000 | `0` | |
| `maxAgeHours` | `integer` | no | -1 to 720 | — | `-1` = always serve cached; `0` = force a fresh crawl. |
| `livecrawl` | `string` | no | `never`, `always`, `fallback`, `preferred` | — | **DEPRECATED**, use `maxAgeHours`. Do not send `livecrawl` and `maxAgeHours` together — the page says so verbatim; whether the API rejects the pair is not stated. |
| `livecrawlTimeout` | `integer` | no | 1–90000 (ms) | `10000` | |
| `subpages` | `integer` | no | 0–100 | `0` | |
| `subpageTarget` | `string\|array[string]` | no | string: 1–100 chars; array: 0–100 items, each 1–100 chars | — | **Union.** An empty array is allowed (`minItems: 0`). |
| `context` | `boolean\|object` | no | `{maxCharacters: 1-10000}` | — | **DEPRECATED**, use `highlights` or `text` instead. **Union**, same shape family as `/search`'s deprecated `context`. |

**`summary`'s type is contradicted between the two vendor pages.** The OpenAPI spec embedded on the
`get-contents` reference page types `summary` as `anyOf [object{query, schema}, null]` — object or null, no
boolean form. The companion guide page's Request Parameters table lists it as `boolean or object` with the note
"Return LLM-generated summary. Object form: `{query, schema}`." A validator built strictly from the OpenAPI spec
would reject `"summary": true`, which the guide presents as valid. Recorded as a disagreement, not resolved here.

**Every optional request field above (and every nested option field) is documented `anyOf [<type>, null]` on the
OpenAPI spec**, not merely optional-by-omission: `compliance`, `text`, `highlights`, `summary`, `extras`,
`context`, `livecrawl`, `livecrawlTimeout`, `maxAgeHours`, `subpages`, `subpageTarget`, and every nested key under
`text`/`highlights`/`summary`/`extras` (`maxCharacters`, `includeHtmlTags`, `verbosity`, `includeSections`,
`excludeSections`, `query`, `numSentences`, `highlightsPerUrl`, `schema`, `links`, `imageLinks`, `richImageLinks`,
`richLinks`, `codeBlocks`) all carry an explicit `{type: "null"}` arm. Explicit JSON `null` (e.g. `"text": null`,
`"maxAgeHours": null`) is therefore documented-valid input, not merely an absent key. A validator that rejects an
explicit `null` on any of these fields rejects documented-valid traffic. The Type column above omits the `|null`
per field for table width; treat every "no" in the Required column as "optional, and null is a valid explicit
value" unless noted otherwise.

**Divergence from `/search`'s documented `contents.extras`:** this file's `/search` Request fields table (above)
lists only `links`, `imageLinks` and `codeBlocks` under `contents.extras`. The `/contents` page documents two more
keys, `richImageLinks` and `richLinks`, that do not appear in the `/search` reference page fetched on 2026-08-14.
Not resolved here — flagged so an implementer does not silently assume the two `extras` shapes are identical.

### Response

| Field | Type | Always present | Notes |
|---|---|---|---|
| `requestId` | `string` | yes | Same format family as `/search`. |
| `results` | `array[object]` | yes | `SearchResultOutput[]`, the same object family as `/search`'s `results[]`. |
| `results[].title` | `string` | yes (within the object) | |
| `results[].url` | `string` | yes (within the object) | |
| `results[].publishedDate` | `string` | conditional | OpenAPI types it plain `string` (no `null` arm) with description "Format is YYYY-MM-DD"; the guide instead says `string or null` and its example is full ISO 8601. Vendor pages disagree; recorded as-is rather than resolved. |
| `results[].author` | `string\|null` | conditional | Explicit null, not merely absent. |
| `results[].id` | `string` | conditional | Document ID, per the guide "same as URL". |
| `results[].image` | `string` | conditional | URI. |
| `results[].favicon` | `string` | conditional | URI. |
| `results[].text` | `string` | conditional | Gated on the `text` request option. |
| `results[].highlights` | `array[string]` | conditional | Gated on the `highlights` request option. |
| `results[].highlightScores` | `array[number]` | conditional | Cosine similarity per highlight. |
| `results[].summary` | `string` | conditional | Gated on the `summary` request option. |
| `results[].subpages` | `array[object]` | conditional | OpenAPI: seven fields (`title`, `url`, `publishedDate`, `author`, `id`, `image`, `favicon`), `additionalProperties: false`. The guide instead describes it as "Same shape as results" (i.e. the full result object, not the seven-field subset). Vendor pages disagree on this element's shape; not resolved here. |
| `results[].entities` | `array[object]` | conditional | Fully itemised in the OpenAPI spec, not merely described by name — see below. |
| `results[].extras` | `object` | conditional | `{links: string[]}` only, `additionalProperties: false` — see below. The request's other four `extras` keys (`imageLinks`, `richImageLinks`, `richLinks`, `codeBlocks`) have no documented output counterpart. |
| `statuses` | `array[object]` | yes | One element per requested `id`/URL. `required: [id, status]` only — `source` and `error` are both optional per the schema. |
| `statuses[].id` | `string` | yes | The requested URL or document ID, echoed back. |
| `statuses[].status` | `string` | yes | Enum: `success`, `error`. |
| `statuses[].source` | `string` | no (schema `required` omits it) | Enum: `cached`, `crawled`. Both the reference page's and the guide's success examples omit this field even on `status: success`, so "conditional, and often absent" is closer than "always present". |
| `statuses[].error` | `object\|null` | no (schema `required` omits it) | Description: "Error details, only present when status is \"error\"." Both vendor examples of `status: success` omit `error` entirely rather than sending `error: null`. |
| `statuses[].error.tag` | `string` | conditional | Enum: `CRAWL_NOT_FOUND`, `CRAWL_TIMEOUT`, `CRAWL_LIVECRAWL_TIMEOUT`, `SOURCE_NOT_AVAILABLE`, `UNSUPPORTED_URL`, `CRAWL_UNKNOWN_ERROR`. |
| `statuses[].error.httpStatusCode` | `integer\|null` | conditional | `anyOf [{integer, 100–599}, null]`; `UNSUPPORTED_URL`'s row on the error-codes page gives no code, i.e. the null case. |
| `costDollars` | `object` | yes | **Verified directly on this route** — unlike the Agent API's `costDollars`, this is a reading, not an inference. |
| `costDollars.total` | `number` | yes | The page's only JSON response example shows `{"total": 0.003}`; the schema's own per-field example is `0.007`. Both are illustrative example values, not a documented range. |
| `costDollars.search` | `object` | conditional | Description: "Endpoint-dependent estimated search cost breakdown by retrieval mode ... Deep search modes may be reflected only in total." Shown with a `.neural` key in the schema's per-field example; the page's one JSON example omits `search` entirely, matching the same sparse-by-default pattern already recorded for `/answer`. |
| `costDollars.search.neural` | `number` | conditional | |
| `context` | `string` | no | **DEPRECATED**, per the request field of the same name. No example value shown. |

**`results[].entities` is fully itemised in the OpenAPI spec**, not merely named by category. It is
`oneOf` three discriminated object shapes, each `required: [id, type, version, properties]`, with `type` a
`const` of `company`, `person` or `publication` and `version` an integer ≥ 1:

- **company** `properties`: `name`, `foundedYear`, `description`, `workforce.total`, `headquarters.{address,
  city, postalCode, country}`, `financials.{revenueAnnual, fundingTotal, fundingLatestRound}`, `webTraffic`,
  `research`.
- **person** `properties`: `name`, `firstName`, `lastName`, `location`, `workHistory[]`, `educationHistory[]`,
  `research`.
- **publication** `properties`: `title`, `year`, `date`, `type` (enum `article`, `book`, `book-chapter`,
  `dataset`, `dissertation`, `preprint`, `report`, `review`, or `null`), `language`, `citationCount`,
  `authors[].{name, id}`, `referenceCount`, `abstract`, `doi`.

Nested field types within each entity kind (e.g. exact numeric vs. string typing inside `financials`) are not
re-transcribed here; read the spec directly before implementing.

### Error bodies

The fetched `/contents` reference page itself names only two HTTP statuses: **200 OK** (successful retrieval) and
**402 Payment Required**. The reference page's own 402 has no description beyond the name; "insufficient account
balance" is the error-codes page's wording for 402 in general, not text found on the `/contents` page itself — cited
here, not attributed to the wrong page. It does not reprint the full error envelope on its own page. This file
already records the shared flat `{requestId, error, tag}` envelope (with the reduced 429 `{error}`-only shape) from
the error-codes page cited for `/search` and `/answer`; that page is written as a cross-endpoint reference rather
than `/contents`-specific, so the same envelope is assumed here for validation failures (400), auth failures (401)
and rate limiting (429) — **assumed by cross-reference, not independently re-quoted for `/contents`.**

**The coding-agent guide names a different, `/contents`-specific request-level error set: 400, 401, 422 and 429**,
with 422 described as "Validation error." — a status the reference page's own 200/402 pair does not mention at
all. Separately, the error-codes page names `/contents`-specific fault *tags* that are not part of the `statuses[]`
per-URL mechanism below: `ROBOTS_FILTER_FAILED` (`403`, "`/contents` only — all requested URLs were blocked by
robots.txt"), `FETCH_DOCUMENT_ERROR` (`422`, "A specific URL could not be processed"), `NO_CONTENT_FOUND` (`400`,
"No contents could be found for the given URLs") and `INVALID_URLS` (`400`, "One or more URLs/IDs are in an
invalid format"). None of 422, `ROBOTS_FILTER_FAILED`, `FETCH_DOCUMENT_ERROR`, `NO_CONTENT_FOUND` or
`INVALID_URLS` has an independently-confirmed `/contents` example body; recorded as named-but-not-exemplified.

**Per-URL crawl failures are a distinct mechanism from top-level HTTP errors.** A `CRAWL_NOT_FOUND` on one
requested URL out of ten does not make the overall response non-200 — it surfaces inside that element's
`statuses[].error`, with the response still `200 OK` and `results[]` still populated for the URLs that succeeded.
A simulator that maps a per-URL crawl failure onto the response's own HTTP status would be wrong.

### What is NOT verified, and must not be invented

1. **Whether `statuses[].source` is ever guaranteed present.** The schema's `required` list omits it and both
   vendor success examples omit the field; treat it as conditional, not merely "not verified as always-present".
2. **The full top-level HTTP error set for `/contents` specifically, with example bodies.** 200 and 402 are named
   with an example on the reference page; 400/401/422/429 are named (without bodies) on the guide's Request-Level
   Errors table; 500 is assumed shared with `/search` and `/answer` by cross-reference to the error-codes page.
   None of 400/401/422/429/500 has an independently-confirmed `/contents`-specific example body.
3. **Nested field types inside each `results[].entities` kind** (e.g. exact typing inside `financials`,
   `workHistory[]`, `educationHistory[]`) — the top-level shape and property names are documented in full (see
   above), but this file does not re-transcribe every nested primitive type.
4. **Whether `results[].subpages[]` matches the OpenAPI's seven-field element or the guide's "same shape as
   results"** — the two vendor pages disagree (see the Response table above); not resolved here.
5. **Whether `extras.richImageLinks` / `extras.richLinks` are also accepted (silently or otherwise) on `/search`'s
   `contents.extras`.** The two reference pages disagree on the request-side key set; neither says the other route
   rejects the extra keys. (The *response*-side `extras` shape is documented as `{links: string[]}` only — see
   above; it does not mirror the request's five keys.)
6. **`context`'s response shape** (the deprecated field) — no example value is shown on this page.
7. **Whether `compliance: hipaa` changes the response contract** beyond restricting retrieval to cached content.
   The guide's one relevant line — "Uses cache-only retrieval; summaries and livecrawl are not supported." —
   answers the "cache-only" half but not the response-shape question.
8. **Whether `summary` is boolean-or-object or object-only.** The OpenAPI spec and the guide disagree (see the
   Request table above); not resolved here.

## POST /findSimilar

Verified against live vendor documentation on **2026-08-15**.

**This section previously claimed no live documentation page could be found for this route, and recommended
treating its existence as unconfirmed.** That was wrong. The prose reference page URL the earlier search targeted
(`https://exa.ai/docs/reference/find-similar-links`) does 404, and several plausible URL guesses and a
third-party mirror also came back negative or inconclusive — but the vendor's **own OpenAPI spec**, one link away
from the docs index the earlier search already had open, documents the route in full. The lesson: a prose
reference page 404ing does not mean the route is gone, and "OpenAPI Specification" in a docs index should be the
first link followed, not the last — CONTRIBUTING.md already says "prefer a machine-readable OpenAPI document if
one exists, because prose pages describe while the spec decides."

Sources:

- <https://exa.ai/docs/exa-spec.yaml> — the vendor's own OpenAPI spec, `title: Exa Public API`, `version: 2.0.0`,
  HTTP 200, linked from <https://exa.ai/docs/reference/openapi-spec> under "OpenAPI Specification" in the docs
  index (`llms.txt`). That page states verbatim: "The raw OpenAPI specs are the source of truth for request and
  response schemas."

**Status: canonical per the live OpenAPI spec, DEPRECATED by the vendor, simulated.** The spec marks the
operation `deprecated: true` with `x-exa-lifecycle: deprecated`. Its description, verbatim: "Find links similar to
the provided URL and optionally retrieve their contents. Deprecated: prefer `/search` with a query describing the
source." Given the vendor's own steer toward `/search`, whether to simulate this route at all was a decision for
the adopter; per D-b (`docs/adopter-backlog.md`, Phase 4) it is simulated anyway — Phase 0's lesson is that
rejecting valid production traffic is the worst failure a simulator can produce, and a client written before the
deprecation is still production traffic. See "Open questions for the adopter" at the end of this section for what
remains unconfirmed about whether the adopter's client actually calls it.

### Request

`operationId: findSimilar`. Request body: `FindSimilarRequest`, required.

| Field | Type | Required | Range / Enum | Default | Notes |
|---|---|---|---|---|---|
| `url` | `string` | **yes** | minLength 3 | — | The only required field. |
| `numResults` | `integer\|null` | no | 1–100 | `10` | |
| `includeDomains` | `array[string]\|null` | no | max 1200 items | — | |
| `excludeDomains` | `array[string]\|null` | no | max 1200 items | — | |
| `startPublishedDate` | `string\|null` (date-time) | no | — | — | |
| `endPublishedDate` | `string\|null` (date-time) | no | — | — | |
| `startCrawlDate` | `string\|null` | no | — | — | **DEPRECATED, no effect**, per the spec's own description. |
| `endCrawlDate` | `string\|null` | no | — | — | **DEPRECATED, no effect**, per the spec's own description. |
| `contents` | `object\|null` | no | — | — | `$ref` to the same `ContentsOptions` object as `/contents` (`text`/`highlights`/`summary`/`extras`/`livecrawl`/`maxAgeHours`/`subpages`). |
| `category` | `string\|null` | no | `company`, `publication`, `news`, `personal site`, `financial report`, `people` | — | |
| `excludeSourceDomain` | `boolean\|null` | no | — | — | |

The spec's own components section for `ContentsOptions` was not re-fetched in full for this pass (the raw spec
file is ~405 KB and truncates in the fetch tool before reaching the nested schema each time); it is asserted here
only by `$ref` name and by analogy to the identically-named object already recorded in this file's `/contents`
section. Treat that nesting as **not independently re-verified for this route** until the component itself is
read directly (e.g. via `curl` rather than a summarising fetch).

### Response

`FindSimilarResponse`, `additionalProperties: false`.

| Field | Type | Notes |
|---|---|---|
| `requestId` | `string` | |
| `context` | `string` | **DEPRECATED**, per the spec. |
| `results` | `array[object]` | `SearchResultOutput[]` — the same result object family as `/search` and `/contents`. |
| `costDollars` | `object` | `CostDollarsOutput` — the same shape as `/contents`'s `costDollars`. |

### What is NOT verified, and must not be invented

1. **`ContentsOptions`'s exact nested shape as referenced from this route** — asserted by `$ref` name and analogy
   to `/contents` above, not re-read component-by-component for `/findSimilar` specifically in this pass.
2. **`SearchResultOutput`'s and `CostDollarsOutput`'s exact field lists as referenced from this route** — asserted
   by `$ref` name and analogy to `/search`/`/contents` above, same caveat as above.
3. **Whether the vendor's SDKs (`exa-js`, the TypeScript SDK spec) expose a `findSimilar` method at all.** The
   earlier search round found none in `exa-js`'s README or the TypeScript SDK specification page; that check was
   not repeated in this pass, and a deprecated-but-spec-present route may or may not have a corresponding SDK
   method.
4. **The route's HTTP error set** — not separately checked against the error-codes page in this pass; treat as
   unverified rather than assumed shared with `/contents`.
5. **Whether the vendor plans to remove the route**, versus keeping it indefinitely in deprecated form. The spec
   states deprecation, not a removal date.

### Open questions for the adopter

Decision D7 says a re-verification that contradicts the adopter's working client must surface as a decision, not
be silently resolved by siding with the documentation. The adopter's client (`src/pkg/agent/`) is not in this
repository, so that check could not be run as part of this verification pass. Ask the adopter:

1. **Does your client call `findSimilar` at all?** The vendor marks it deprecated and steers callers to `/search`.
   If the adopter's client does not call it, deprioritise Phase 4 simulation of this route in favour of `/search`
   and `/contents`, which are both confirmed live and non-deprecated.
2. **If your client does call it, does it send `startCrawlDate` / `endCrawlDate`?** The spec says these are
   deprecated and have no effect; a client still relying on them for filtering would be silently getting
   unfiltered results from the real API today, independent of Servicesim.
3. **Does your client's `contents` sub-object on this route send any field not already covered by this file's
   `/contents` section?** This section asserts the shared `ContentsOptions` shape by `$ref` name and analogy,
   not by an independent re-read of the component for this route specifically.
