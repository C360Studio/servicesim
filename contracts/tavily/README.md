# Tavily consumed contract

Verified against live vendor documentation on **2026-08-14**.

This file records only what Servicesim simulates and what consumers parse. It is not a
redistribution of the vendor's OpenAPI document. Re-verify and update the date above on the
sanctioned dated re-verification cadence (`contracts/README.md` "Keeping them honest" — there
is no live contract canary).

## Documentation sources

- <https://docs.tavily.com/documentation/api-reference/endpoint/search>
- <https://docs.tavily.com/documentation/api-reference/endpoint/search.md>
- <https://docs.tavily.com/api-reference/endpoint/search>
- <https://docs.tavily.com/documentation/best-practices/best-practices-search.md>
- <https://docs.tavily.com/documentation/api-credits.md>

## Endpoints

| Method | Path | Status | Note |
|---|---|---|---|
| `POST` | `/search` | canonical | OpenAPI 3.0.3 `paths./search.post`, server `https://api.tavily.com/`. No version prefix (no /v1). Both requested doc URLs resolve to the same reference page; <https://docs.tavily.com/api-reference/endpoint/search> serves the identical content (docs site accepts the path with and without the /documentation segment). NOTE: the historical `days` request parameter (news recency window) is COMPLETELY ABSENT from the current /search OpenAPI schema — it is not listed among the request properties. Date filtering is now time_range / start_date / end_date. |

## Authentication

- Authorization: Bearer `<token>` (OpenAPI `security: [{bearerAuth: []}]`; components.securitySchemes.bearerAuth = {type: http, scheme: bearer, bearerFormat: JWT}, described as 'Bearer authentication header in the form Bearer `<token>`, where `<token>` is your Tavily API key (e.g., Bearer tvly-YOUR_API_KEY)')

### Accepted credential placements (recorded 2026-08-15)

This is a case where **observed client behaviour and the vendor documentation disagree**. The
vendor provenance above is unchanged and still says what it always said: the only documented
placement is `Authorization: Bearer`. It is not the only placement the live API accepts, and it
is not the only placement real client code sends.

| Placement | Vendor documents it | Observed in client code | Authenticates in Servicesim | Finding raised |
|---|---|---|---|---|
| `Authorization: Bearer <key>` header | yes | yes (on GET polls) | yes | `auth.wrong_placement` only if the header carries another scheme |
| `api_key` property in the JSON request body | no | yes (on POSTs) | yes | `tavily.api_key.in_body`, warning severity |
| `x-api-key` header | no | no | no | `tavily.auth.wrong_header` plus `auth.missing` |

Presenting both accepted placements at once is fine and is not a finding of its own; each is
recorded on its own terms.

Why both are accepted:

- A consumer's production client sends the key as a body property on `POST /search` and as a
  Bearer header on its GET polls. `api.tavily.com` serves that traffic.
- Until 2026-08-15 Servicesim followed the vendor document alone and answered a body-placed key
  with `401` and `auth.missing`. That failed requests which succeed against the real API, which
  is worse than not simulating the surface at all: it sends the consumer hunting a bug in code
  that is correct.

Accepting the body placement does not lose the assertion that the placement existed for:

- The journal's auth observation names the placement. `auth.header` is `authorization` for the
  header and `body:api_key` for the body property; the value itself is never journaled, only its
  fingerprint. When a request carries both, the documented header keeps the observation and the
  body placement is carried by its finding.
- `tavily.api_key.in_body` is still raised for every body-placed key, as a warning, so
  `testkit.AssertNoErrors` passes while the placement stays visible.
- A consumer that wants the body placement to FAIL — because it is migrating off it — declares
  `validation: {promote: [tavily.api_key.in_body]}`, or narrows the accepted set with
  `auth: {headers: [authorization]}`, in which case a body-placed key is rejected with
  `auth.missing`. `auth: {headers: ["body:api_key"]}` is the mirror of that.

The body key is redacted everywhere it is retained: the journal's request body renders it as
`[REDACTED]`, and the finding message states presence only and never interpolates the value.

## Request fields

| Field | JSON type | Required | Enum | Default |
|---|---|---|---|---|
| `query` | `string` | yes | — | — |
| `search_depth` | `string` | no | `advanced`, `basic`, `fast`, `ultra-fast` | `basic` |
| `chunks_per_source` | `integer` | no | — | `3` |
| `max_results` | `integer` | no | — | `5` |
| `topic` | `string` | no | `general`, `news`, `finance` | `general` |
| `time_range` | `string` | no | `day`, `week`, `month`, `year`, `d`, `w`, `m`, `y` | `null` |
| `start_date` | `string` | no | — | `null` |
| `end_date` | `string` | no | — | `null` |
| `include_answer` | `boolean\|string` | no | `true`, `false`, `basic`, `advanced` | `false` |
| `include_raw_content` | `boolean\|string` | no | `true`, `false`, `markdown`, `text` | `false` |
| `include_images` | `boolean` | no | — | `false` |
| `include_image_descriptions` | `boolean` | no | — | `false` |
| `include_favicon` | `boolean` | no | — | `false` |
| `include_domains` | `array<string>` | no | — | `[]` |
| `exclude_domains` | `array<string>` | no | — | `[]` |
| `country` | `string` | no | — | `null` |
| `auto_parameters` | `boolean` | no | — | `false` |
| `exact_match` | `boolean` | no | — | `false` |
| `include_usage` | `boolean` | no | — | `false` |
| `safe_search` | `boolean` | no | — | `false` |

## Response fields

| Field | JSON type | Always present | Nullable |
|---|---|---|---|
| `query` | `string` | yes | no |
| `answer` | `string` | yes | yes |
| `images` | `array` | yes | no |
| `images[].url` | `string` | no | no |
| `images[].description` | `string` | no | no |
| `results` | `array<object>` | yes | no |
| `results[].title` | `string` | yes | no |
| `results[].url` | `string` | yes | no |
| `results[].content` | `string` | yes | no |
| `results[].score` | `number` | yes | no |
| `results[].raw_content` | `string` | no | yes |
| `results[].favicon` | `string` | no | no |
| `results[].images` | `array<object>` | no | no |
| `results[].id` | `string` | no | no |
| `results[].published_date` | `string` | no | no |
| `response_time` | `number` | yes | no |
| `request_id` | `string` | no | no |
| `auto_parameters` | `object` | no | no |
| `usage` | `object` | no | no |
| `usage.credits` | `number` | no | no |

## Error bodies

### 400

```json
{"detail":{"error":"<400 Bad Request, (e.g Invalid topic. Must be 'general' or 'news'.)>"}}
```

Description: 'Bad Request - Your request is invalid.' Schema is {detail: {error: string}}. The angle-bracket placeholder is literally what the doc example contains.

### 401

```json
{"detail":{"error":"Unauthorized: missing or invalid API key."}}
```

Description: 'Unauthorized - Your API key is wrong or missing.'

### 429

```json
{"detail":{"error":"Your request has been blocked due to excessive requests. Please reduce rate of requests."}}
```

Description: 'Too many requests - Rate limit exceeded'. No Retry-After header is documented.

### 432

```json
{"detail":{"error":"<432 Custom Forbidden Error (e.g This request exceeds your plan's set usage limit. Please upgrade your plan or contact support@tavily.com)>"}}
```

Description: 'Key limit or Plan Limit exceeded'. This is the plan-limit status the plan alludes to — a non-standard 432 code.

### 433

```json
{"detail":{"error":"This request exceeds the pay-as-you-go limit. You can increase your limit on the Tavily dashboard."}}
```

Description: 'PayGo limit exceeded'. A SECOND non-standard status the plan does not mention at all.

### 500

```json
{"detail":{"error":"Internal Server Error"}}
```

Description: 'Internal Server Error - We had a problem with our server.'

## Corrections to the original plan document

The following claims in `docs/architecture-and-implementation-plan.md` were contradicted by the live
documentation on 2026-08-14. The verified contract above is authoritative.

- **Plan said:** `search_depth` is a two-value enum, basic|advanced (plan example uses "basic").
  - **Live docs say:** Live enum has FOUR values: `[advanced, basic, fast, ultra-fast]`, default `basic`. A validator that rejects `fast`/`ultra-fast` would reject valid current traffic.
- **Plan said:** `include_answer` is a boolean (plan's request example sets it to `true` and its response model implies a boolean toggle).
  - **Live docs say:** Schema is `oneOf: [{type: boolean}, {type: string, enum: [basic, advanced]}]`, default false. Accepted values are true, false, 'basic', 'advanced'. Boolean-only validation is wrong.
- **Plan said:** `include_raw_content` is a boolean (plan example: false).
  - **Live docs say:** Schema is `oneOf: [{type: boolean}, {type: string, enum: [markdown, text]}]`, default false. Accepted values are true, false, 'markdown', 'text'.
- **Plan said:** The plan's request model is the complete initial surface (query, search_depth, include_answer, include_images, include_raw_content, max_results).
  - **Live docs say:** The live schema documents 20 properties. Missing from the plan: topic, time_range, start_date, end_date, chunks_per_source, include_image_descriptions, include_favicon, include_domains, exclude_domains, country, auto_parameters, exact_match, include_usage, safe_search.
- **Plan said:** `response_time` is a JSON string (plan encodes `"response_time": "1.15"`).
  - **Live docs say:** Schema declares `response_time: {type: number, format: float, description: 'Time in seconds it took to complete the request.'}`. The spec's example is rendered as the quoted YAML scalar '1.67', which is almost certainly the source of the plan's string assumption, but the declared JSON type is number. Encoding it as a string will break typed consumers (Go float64, Pydantic float).
- **Plan said:** `results[]` items are exactly {title, url, content, raw_content, score}.
  - **Live docs say:** Live result item schema has EIGHT properties: title, url, content, score, raw_content, favicon, images, id. The plan omits favicon, images, and id. It also omits published_date, which the best-practices page says is returned for topic=news.
- **Plan said:** The plan's response model is complete.
  - **Live docs say:** Missing top-level fields `auto_parameters` (object, when auto_parameters=true) and `usage` (object, e.g. {credits: 1}, gated by include_usage).
- **Plan said:** The plan specifies no error body envelope for Tavily.
  - **Live docs say:** Every documented error (400/401/429/432/433/500) uses the identical envelope `{"detail": {"error": "<string>"}}` — a nested object, not a flat {"error": …}. A simulator must emit this exact shape; the plan gives implementers nothing to build against.
- **Plan said:** Implicit in the plan's field list: a `days` request parameter and `topic` enum of general|news.
  - **Live docs say:** The plan never names either, but the task asked me to verify them. `days` is entirely ABSENT from the current /search request schema (grep over the full OpenAPI returns zero hits) — recency is now time_range / start_date / end_date. `topic` has THREE values: [general, news, finance], default general. Note the 400 error example text still says "Must be 'general' or 'news'", which is stale relative to the enum.

## POST /research and GET /research/{request_id}

Verified against live vendor documentation on **2026-08-15**.

Read from:

- <https://docs.tavily.com/documentation/api-reference/endpoint/research> — create
- <https://docs.tavily.com/documentation/api-reference/endpoint/research-get> — poll

Tavily Research performs multi-step web research asynchronously: a create returns immediately with a
`request_id`, and the client polls until the task reaches a terminal status.

### Lifecycle

```text
pending -> in_progress -> completed | failed
```

`completed` and `failed` are terminal. Note there is **no `cancelled`** status, unlike Exa's agent runs — a
simulator must not offer one here on the strength of the sibling surface.

### The poll's status code varies with the task state

This is the single most important thing on this surface and the easiest to get wrong:

| Task state | HTTP status | Body carries |
|---|---|---|
| `pending`, `in_progress` | **202 Accepted** | `request_id`, `status`, `response_time` |
| `completed` | **200 OK** | the above plus `created_at`, `content`, `sources` |
| `failed` | **200 OK** | `request_id`, `status`, `response_time` only — no additional fields (corrected 2026-08-15, verified against <https://docs.tavily.com/documentation/api-reference/endpoint/research-get>, "Failed Tasks Only: no additional fields beyond the three common fields") |

A poll is therefore **not** a constant 200 with a status field — the HTTP status is itself part of the contract, and
a client may well branch on it before parsing. Emitting 200 throughout would let a consumer's `202 == still working`
path go completely untested.

### POST /research — request

| Field | Type | Required |
|---|---|---|
| `input` | `string` | **yes** |
| `model` | `string` — enum `mini`, `pro`, `auto` | no |
| `stream` | `boolean` | no |
| `output_schema` | `object` | no |
| `citation_format` | `string` — enum `numbered`, `mla`, `apa`, `chicago` | no |
| `include_domains` | `array[string]` | no |
| `exclude_domains` | `array[string]` | no |
| `output_length` | `string` — enum `short`, `standard`, `long` | no |
| `files` | `array[object]` | no |

Note `input`, not `query`: this surface does **not** reuse `/search`'s request field name, and a simulator that
accepted `query` here would accept traffic the live API rejects.

### POST /research — response (201 Created)

All six fields are documented as required: `request_id`, `created_at`, `status`, `input`, `model`, `response_time`.

The create echoes `input` and `model` back, which `/search` does not do for its own request fields. A consumer may
read either to confirm what the task was created with.

### GET /research/{request_id} — response

| Field | Type | When |
|---|---|---|
| `request_id` | `string` | always |
| `status` | `string` | always |
| `response_time` | number | always |
| `created_at` | `string` | completed only |
| `content` | `string` or `object` | completed only |
| `sources` | `array[object]` of `{title, url, favicon}` | completed only |

`content` is genuinely string-OR-object: it is a report when no `output_schema` was supplied and structured JSON
when one was. A consumer's decoder has to handle both, so a scenario must be able to produce both.

### Divergences and what is NOT verified

1. **`response_time` type.** The `/research` reference describes it as an integer; `/search`'s OpenAPI schema
   declares `{type: number, format: float}` and this file already records that. Servicesim emits a JSON number for
   both, which satisfies either reading — Go marshals a whole float as `1` rather than `1.0` — but the divergence is
   recorded rather than resolved, because nothing verifies the two surfaces agree.
2. **Credential placement per route is NOT what the async design assumed.** See below.
3. **`files[]`, `output_schema` and the `sources[]` entry beyond `{title, url, favicon}`** have no verified
   sub-shapes here.
4. **The `request_id` format** is not documented. `/search`'s is a UUID; nothing verifies this surface matches.

### Credential placement: the design's stated reason was wrong

`docs/design/async-jobs.md` justified `Route.Credentials` by asserting that *"`POST /research` takes its credential
in the JSON body and `GET /research/{id}` takes a Bearer header, so this surface cannot work at all until placement
is resolvable per route."*

**The vendor documents `Authorization: Bearer` for BOTH routes.** There is no documented body placement on
`/research` at all. That justification does not hold, and it should not be repeated.

The feature is still needed, for a different and better-evidenced reason. This file already records (2026-08-15,
`/search`) that Tavily's shipped clients send the key as a body `api_key` even though the vendor documents Bearer
only — that is observed client behaviour, and v0.1.1 accepts it. Applying the same rule here:

| Route | Accepted placements | Why |
|---|---|---|
| `POST /research` | `authorization`, `body:api_key` | Same as `POST /search`: documented Bearer, plus the body placement real clients use on a POST. |
| `GET /research/{request_id}` | `authorization` only | A GET has no body to carry a key in. This is physics, not policy. |

So the routes genuinely accept different placement sets, and `Route.Credentials` is genuinely the mechanism — the
difference comes from the request having a body rather than from the vendor requiring different schemes.

## POST /extract

Verified against live vendor documentation on **2026-08-15**.

Sources:

- <https://docs.tavily.com/documentation/api-reference/endpoint/extract>
- <https://docs.tavily.com/documentation/api-reference/endpoint/extract.md> — the markdown source of the same
  page, fetched separately; the two fetches agree on every field.

No separate Tavily error-code reference page was found for this route. A search result cited
`https://docs.tavily.com/docs/rest-api/api-reference#error-codes` as a general error-codes page; that URL was
fetched directly and returns **HTTP 404**, so it is a stale reference, not a source. `/extract`'s error bodies
below come from its own page, matching the pattern this file already records for `/search`: every documented
status uses its own fully-shown example rather than deferring to a shared reference.

**Status: canonical, verified, simulated.** Implemented on `profiles/tavily`, exercised by
`profiles/tavily/extract_test.go` and `profiles/tavily/extract_golden_test.go`, with fixtures in this directory.
`contracts/README.md`'s index table and `scripts/check-docs.sh` are the source of truth for what the binary
registers.

### Auth

The page's own auth description documents **Bearer token in the `Authorization` header only**
("`Bearer <token>`, where `<token>` is your Tavily API key"), and does not document a body-placed `api_key`
field for `/extract` — neither fetch of it found one.

**Servicesim accepts exactly that: `Authorization: Bearer` only.** A body-placed `api_key` is recognised (so it
is flagged, not silently treated as an unmodelled field) but does not authenticate; a request presenting only a
body `api_key` fails the same way any other unaccepted placement does on this provider.

This route briefly accepted a body-placed `api_key` too, on decision D2's (`docs/adopter-backlog.md`, "Decisions
already taken") client-level reasoning — that a consumer's production client sends the key as a body property on
every POST, so the placement is a property of the client rather than of the endpoint — extended here by analogy.
**The owner reaffirmed on 2026-08-15 that vendor documentation, not a consumer's client, is the authority for a
wire contract** (ADR-0002): "we have no idea if their client works; we use the vendor docs." D2 itself is a
shipped v0.1.1 decision, made and applied when `/search` and `/research` were the only routes on this contract's
authentication rule, and it stands exactly as shipped — see "Decisions already taken" in
`docs/adopter-backlog.md` for the D2 row and the paragraph beneath it recording this reaffirmation. It is not a
precedent for extending acceptance to routes verified after it, `/extract` included. See "Open questions for the
adopter" below.

### Request

| Field | JSON type | Required | Enum | Range | Default | Notes |
|---|---|---|---|---|---|---|
| `urls` | `string\|array[string]` | **yes** | — | max 20 URLs (inferred — see below) | — | **Union** — the page's own request examples show both a bare string and an array; a validator accepting only one form rejects valid traffic. |
| `query` | `string` | no | — | — | — | Reranks extracted chunks by relevance. |
| `chunks_per_source` | `integer` | no | — | 1–5 | `3` | Documented as usable "only when `query` is provided" — a request field with a conditional precondition on another field. Chunks are "short content snippets (maximum 500 characters each)" pulled from the source; when returned, they appear in `raw_content` joined by a literal `[...]` separator (verbatim from the page). |
| `extract_depth` | `string` | no | `basic`, `advanced` | — | `basic` | Cost differs by value: 1 credit per 5 successful extractions at `basic`, 2 credits per 5 at `advanced`, per the page. |
| `include_images` | `boolean` | no | — | — | `false` | |
| `include_favicon` | `boolean` | no | — | — | `false` | |
| `format` | `string` | no | `markdown`, `text` | — | `markdown` | |
| `timeout` | `float` (seconds) | no | — | 1.0–60.0 | `10` at `basic`, `30` at `advanced` | Default is itself conditional on `extract_depth`. |
| `include_usage` | `boolean` | no | — | — | `false` | |

**The 20-URL cap on `urls` is not a declared schema constraint** — no `maxItems` appears on the `urls` field
itself. It is inferred from the 400 error example's text, `"<400 Bad Request, (e.g Max 20 URLs are allowed.)>"`.
Marked as inferred rather than schema-declared, per house style.

### Response

The response schema carries **no top-level `required` list at all** — every "yes" below is inferred from the
page's example payloads and field descriptions, not from a declared requirement.

| Field | Type | Always present | Notes |
|---|---|---|---|
| `results` | `array[object]` | yes (inferred) | |
| `results[].url` | `string` | yes (inferred, within the object) | Source URL. |
| `results[].raw_content` | `string` | yes (inferred, within the object) | Full or chunked page content, per `format`. When `query` is provided, contains "the top-ranked chunks joined by `[...]` separator" (verbatim). |
| `results[].images` | `array[string]` | conditional | Explicitly documented: "This is only available if `include_images` is set to `true`." |
| `results[].favicon` | `string` | conditional (inferred) | The page's description ("The favicon URL for the result.") does not itself state the `include_favicon` gating; the gating is inferred from the field name and the analogous `include_images`/`images` pair, not read verbatim on this field. |
| `failed_results` | `array[object]` | yes (inferred) | Description: "A list of URLs that could not be processed." No page text states it is always returned (possibly empty) rather than conditional; inferred from the absence of a documented gating condition, same as `results`. |
| `failed_results[].url` | `string` | yes (inferred, within the object) | The failed URL. |
| `failed_results[].error` | `string` | yes (inferred, within the object) | Failure reason as free text. |
| `response_time` | `number` (float, seconds) | yes (inferred) | Same JSON type this file already records for `/search`'s `response_time` — no string-vs-number divergence on this route. |
| `usage` | `object` | conditional (inferred) | Description: "Credit usage details for the request." No page text states the `include_usage` gating on this field itself; inferred from the field name and the request field's own note (below), not read verbatim on this field. |
| `usage.credits` | `number` | conditional | Docs example shows `1`; same field name and shape as `/search`'s `usage.credits` already recorded in this file. The `include_usage` request field's own note states verbatim: "The value may be 0 if the total successful URL extractions has not yet reached 5 calls." — so `usage.credits: 0` is a documented, not merely possible, value. |
| `request_id` | `string` | yes (inferred) | Example on the page is UUID-shaped (`123e4567-e89b-12d3-a456-426614174111`), matching `/search`'s documented UUID `request_id` format elsewhere in this file, but nothing on this page states the format as a rule for `/extract`. |

### Error bodies

Same envelope family as `/search`, shown with `/extract`-specific example text:

#### 400

```json
{"detail":{"error":"<400 Bad Request, (e.g Max 20 URLs are allowed.)>"}}
```

The angle-bracket placeholder is literally what the doc example contains, same convention as the `/search` 400
example already recorded in this file. The example text itself confirms the 20-URL cap on `urls`.

#### 401

```json
{"detail":{"error":"Unauthorized: missing or invalid API key."}}
```

Identical text to the `/search` 401 example already recorded in this file.

#### 429

```json
{"detail":{"error":"Your request has been blocked due to excessive requests. Please reduce rate of requests."}}
```

Identical text to the `/search` 429 example.

#### 432

```json
{"detail":{"error":"<432 Custom Forbidden Error (e.g This request exceeds your plan's set usage limit. Please upgrade your plan or contact support@tavily.com)>"}}
```

Identical text to the `/search` 432 example.

#### 433

```json
{"detail":{"error":"This request exceeds the pay-as-you-go limit. You can increase your limit on the Tavily dashboard."}}
```

Identical text to the `/search` 433 example.

#### 500

```json
{"detail":{"error":"Internal Server Error"}}
```

Identical text to the `/search` 500 example.

All six statuses and their example bodies are reprinted verbatim on `/extract`'s own page rather than by
cross-reference, so — unlike Exa's `/contents` section above — nothing here is assumed by analogy to a sibling
route.

### What is NOT verified, and must not be invented

1. **Whether the live API itself authenticates a body-placed `api_key` on `/extract`.** Not documented — the
   page's own auth description names `Authorization: Bearer` only, with no body-placed alternative. Per the
   "Auth" section above, Servicesim does not accept one either: this is a statement about what the vendor's own
   page does not say, and it is also now what Servicesim does.
2. **`request_id`'s format.**
3. **`failed_results[].error`'s possible values** — documented only as free text, with no enum or set of causes
   listed.
4. **Whether `results[]` and `failed_results[]` are mutually exclusive per URL** (i.e., a URL appears in exactly
   one of the two arrays) — the page describes both arrays independently and never states this explicitly, though
   it is the only sensible reading.
5. **Whether `chunks_per_source` is rejected, ignored, or silently accepted when sent without `query`.** The page
   says the field is usable only when `query` is provided but does not state what happens if it is sent alone.

### Open questions for the adopter

The adopter's client (`src/pkg/agent/`) is not in this repository, so its actual behaviour on this route could
not be checked as part of this verification pass. These are informational — under the repository's authority
rule (ADR-0002, reaffirmed by the owner 2026-08-15), vendor documentation decides this contract, and the answers
below do not change it. Tell us what your client sends so we can tell you where it diverges from the vendor's
documentation:

1. **Does your client send a body-placed `api_key` on `POST /extract`?** The vendor documents `Authorization:
   Bearer` only for this route, and that is all Servicesim accepts here. If your client sends a body `api_key`,
   Servicesim's `/extract` will 401 it — that is a divergence to fix in the client, not a placement for this
   simulator to widen; see the "Auth" section above for why D2 does not extend to this route.
2. **Does your client send `chunks_per_source` without `query`?** The vendor page does not document the result.
3. **Does your client rely on `usage.credits` being present when `include_usage` is unset**, or only when set?
   This file records it as conditional on `include_usage`, inferred rather than read verbatim on the field itself.
4. **Does your client parse `results[].favicon` unconditionally**, or does it gate on `include_favicon` the way
   this file infers (by analogy to `include_images`/`images`) rather than the page stating it directly?
