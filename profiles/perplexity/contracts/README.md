# Perplexity consumed contract

Verified against the vendor's own machine-readable specification on **2026-08-14**, extended **2026-08-15**
with a "Streaming (SSE)" section verified against the prose pages listed under "Documentation sources" below
(and, where noted in that section, against the same OpenAPI document), and corrected the same day after two
`gateway-*-post.md` pages fetched for that section turned out to document Perplexity's **Router API**
(`/router/v1/*`) rather than the Sonar/Agent SDK aliases they were assumed to be, and after the
`ResponseStreamEvent` schema — previously recorded as unretrievable — was confirmed present in the OpenAPI
document.

**Re-verified 2026-10-01 for the Agent surface** against a fresh fetch of the same document (208,564 bytes, sha256
`e0b92edf13c7596c5f830e7e61a30af2c534b878a7a505953bbd6829382c3981`, `info.version` `1.0.0`). The Sonar tables below
were **not** re-read on that date and still describe the 2026-08-14 reading. The dated evidence, every consumed
Agent field and operation classified against the new document with its JSON pointer, and how to re-fetch it are in
[`docs/audits/2026-10-01-perplexity-agent.md`](../../../docs/audits/2026-10-01-perplexity-agent.md). Where this file
marks a behaviour `SIMULATOR-POLICY` or `INFERENCE`, the document is silent and Servicesim chose; none of it is a
vendor guarantee.

**Lifecycle operations re-read 2026-10-03** (issue #6) against a fresh fetch of the same URL whose sha256 is **not**
the one recorded above. Only the operations the background lifecycle consumes were re-read; nothing else in this file
was, and the `spec:` block in `provenance.yaml`, the provider-level `verified:` date and the **Verified** column of
[`contracts/README.md`](../../../contracts/README.md) all stay at 2026-10-01 on purpose. What was read, what moved, and
why the dates did not move are under "Lifecycle: background runs and retrieve" below.

Source of truth: <https://docs.perplexity.ai/openapi.json> (OpenAPI 3.1.0, 208,564 bytes as of 2026-10-01, `servers: [https://api.perplexity.ai]`). Every table below is generated from that document (except "Lifecycle: background runs and retrieve", read from the 2026-10-03 fetch), not from prose documentation pages and not from memory.

> **Why this matters.** An earlier pass built this contract by reading Mintlify documentation pages and
> produced fields borrowed from OpenAI's Responses API by analogy, plus one quotation that does not exist in
> any Perplexity source. Two independent challenge agents caught it. Prose docs describe; the OpenAPI
> document decides. Regenerate this file from the spec rather than editing it by hand.
>
> The "Streaming (SSE)" section below still departs from "generated from the OpenAPI document" for the
> chat-completions surface: no `ChatCompletionChunk`/`chat.completion.chunk` schema exists anywhere in
> `openapi.json` (confirmed by full-text search), so that half of the section is built from prose pages, with
> every claim attributed to the page it came from and every gap the OpenAPI document would normally settle
> recorded as unresolved rather than guessed. The Responses/Agent surface's `ResponseStreamEvent` schema, by
> contrast, **is** in the OpenAPI document and is recorded directly from it as of 2026-08-15 — an earlier
> edition of this file said that schema "could not be retrieved," which was a fetch-tooling limitation, not a
> vendor gap.

## Documentation sources

Fetched and confirmed reachable **2026-08-15**, in addition to `https://docs.perplexity.ai/openapi.json` above:

- <https://docs.perplexity.ai/docs/sonar/pro-search/stream-mode.md> — the chat-completions streaming grammar
- <https://docs.perplexity.ai/api-reference/chat-completions-post.md> — `/v1/sonar` field reference
- <https://docs.perplexity.ai/docs/sonar/openai-compatibility.md> — the Sonar OpenAI-compatibility declaration
- <https://docs.perplexity.ai/docs/agent-api/openai-compatibility.md> — the Agent API OpenAI-compatibility
  declaration
- <https://docs.perplexity.ai/docs/agent-api/output-control.md> — Agent/Responses streaming events
- <https://docs.perplexity.ai/api-reference/agent-post.md> — `/v1/agent` field reference
- <https://docs.perplexity.ai/docs/cookbook/articles/streaming-citations/README.md> — cookbook, illustrative
  only, not authoritative
- <https://docs.perplexity.ai/docs/sonar/features.md> — illustrative only, not authoritative
- <https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events> —
  cited only as a **secondary** source, only where Perplexity itself declares OpenAI compatibility, never as a
  Perplexity statement in its own right

**Fetched, then withdrawn as evidence for this contract.** Two pages were fetched on the assumption that they
were the `/chat/completions` and `/v1/responses` SDK-alias reference pages for Sonar and the Agent API. Re-fetch
on 2026-08-15 shows they document a different, unrelated, **unsimulated** surface, so nothing on either page is
cited as evidence anywhere below:

- <https://docs.perplexity.ai/api-reference/gateway-chat-completions-post.md> — declares
  `post /router/v1/chat/completions`, `info.title: "Perplexity Router API (OpenAI-compatible)"`. Not the
  `/chat/completions` alias of `/v1/sonar` — that alias is declared only on the `sonar/openai-compatibility.md`
  page above, which names `/chat/completions` and `/v1/chat/completions`, not a Router path.
- <https://docs.perplexity.ai/api-reference/gateway-responses-post.md> — declares `post /router/v1/responses`,
  `info.title: "Perplexity Router API (OpenAI Responses-compatible)"`, whose schema is "derived from the
  Apache-2.0-licensed OpenResponses specification." Not the `/v1/responses` alias of `/v1/agent`.

The Router API (`/router/v1/*`) is a distinct surface Servicesim does not simulate; it is out of scope for this
contract beyond this note.

## Endpoints in the specification

| Method | Path | Auth | Operation |
|---|---|---|---|
| `POST` | `/v1/sonar` | Bearer | `chat_completions_chat_completions_post` |
| `POST` | `/search` | Bearer | `search_search_post` |
| `POST` | `/v1/embeddings` | Bearer | `embeddings_v1_embeddings_post` |
| `POST` | `/v1/contextualizedembeddings` | Bearer | `contextualized_embeddings_v1_contextualizedembeddings_post` |
| `GET` | `/v1/async/sonar/{api_request}` | Bearer | `get_async_chat_completion_response_async_chat_completions__api_request__get` |
| `GET` | `/v1/async/sonar` | Bearer | `list_async_chat_completions_async_chat_completions_get` |
| `POST` | `/v1/async/sonar` | Bearer | `create_async_chat_completions_async_chat_completions_post` |
| `POST` | `/v1/agent` | Bearer | `createAgent` |
| `GET` | `/v1/agent/{id}` | Bearer | `retrieveAgent` |
| `GET` | `/v1/agent/{id}/files` | Bearer | `listAgentFiles` |
| `GET` | `/v1/agent/{id}/files/{file_id}/content` | Bearer | `downloadAgentFile` |
| `POST` | `/v1/agent/{id}/cancel` | Bearer | `cancelAgentResponse` |
| `GET` | `/v1/models` | **none** | `listModels` |
| `GET` | `/v1/analytics/computer/usage` | Bearer | `getComputerUsageAnalytics` |
| `GET` | `/v2/analytics/computer/usage` | Bearer | `getComputerUsageAnalyticsV2` |

Authentication is `Authorization: Bearer <token>` (`HTTPBearer`) on every operation except `GET /v1/models`,
which declares `security: []` and is genuinely unauthenticated.

### Routes that are NOT in the specification

Four SDK-routing alias paths do **not** appear in `openapi.json` and are not vendor-documented anywhere.
They exist because the OpenAI SDK appends `/chat/completions` (Chat Completions) or `/responses`
(Responses) to whatever `base_url` it was configured with, and Perplexity accepts those paths for
compatibility. They are real and consumers do use them.

| Method | Path | Aliases | In `openapi.json` |
|---|---|---|---|
| `POST` | `/v1/sonar` | — | **yes** |
| `POST` | `/chat/completions` | `/v1/sonar` | no — SDK routing convention |
| `POST` | `/v1/chat/completions` | `/v1/sonar` | no — SDK routing convention |
| `POST` | `/v1/agent` | — | **yes** |
| `POST` | `/v1/responses` | `/v1/agent` | no — SDK routing convention |
| `POST` | `/responses` | `/v1/agent` | no — SDK routing convention |

Both spellings of each alias are needed because the `/v1` prefix can come from either end. A consumer
configuring `base_url = https://api.perplexity.ai` produces `/v1/chat/completions` and `/v1/responses`;
one configuring `base_url = https://api.perplexity.ai/v1` produces `/chat/completions` and `/responses`.
Which of the two a consumer picked is arbitrary, so a simulator that served only one made whether it worked
at all depend on a choice nobody thought was a choice.

The aliases are aliases in the strict sense: same handler, same request and response shapes as their
canonical path, and the **same fault budget** — a retry through an alias draws on the attempt budget its
canonical route declares, rather than getting a fresh set of retries. The journal still records which path
was used (`path` and `route` on every entry), so an adapter test can assert its intended route.

Servicesim serves all six paths. *(Note added 2026-08-15: the two `/v1/chat/completions` and `/responses`
spellings were missing before that date and returned 404.)* Since 2026-10-03 it serves a seventh route, which is not
an alias of any of them and **is** in the specification: `GET /v1/agent/{id}`, `retrieveAgent` (see "Lifecycle:
background runs and retrieve" below).

## Surface 1 — Sonar (`POST /v1/sonar`)

Announced end of support: **2026-09-27**. Still the surface existing adapters use.

### Request — `ApiChatCompletionsRequest`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `max_tokens` | `integer` \| null | no | — | Maximum number of completion tokens to generate |
| `model` | enum(`sonar`, `sonar-pro`, `sonar-deep-research`, `sonar-reasoning-pro`) | **yes** | — | Model to use, for example, sonar-pro |
| `stream` | `boolean` \| null | no | `False` | If true, returns streaming SSE response |
| `stop` | `string` \| array[`string`] \| null | no | — | Stop sequences. Generation stops when one of these strings is produced |
| `temperature` | `number` \| null | no | — | Controls randomness in the response. Higher values make output more random. Range: 0-2 |
| `top_p` | `number` \| null | no | — | Nucleus sampling parameter. Controls diversity via nucleus sampling |
| `response_format` | `ResponseFormatText` \| `ResponseFormatJSONSchema` \| null | no | — | Optional. Controls the output format. Omit for default text output. Set `type` to `json_schema` for structured output. |
| `messages` | array[`ChatMessage-Input`] | **yes** | — | Array of messages forming the conversation history |
| `web_search_options` | `WebSearchOptions` | no | — |  |
| `search_mode` | enum(`web`, `academic`, `sec`) \| null | no | — | Source of search results (web, academic, or sec) |
| `return_images` | `boolean` \| null | no | — | When true, include image results in the response |
| `return_related_questions` | `boolean` \| null | no | — | When true, generates suggested follow-up queries based on the search results |
| `enable_search_classifier` | `boolean` \| null | no | — | When true, uses a classifier to determine if web search is needed for the query |
| `disable_search` | `boolean` \| null | no | — | When true, disables all web search capabilities. The model responds based solely on its training data |
| `search_domain_filter` | array[`string`] \| null | no | — | Limit search results to specific domains (e.g. github.com, wikipedia.org) |
| `search_language_filter` | array[`string`] \| null | no | — | Filter results by language using ISO 639-1 codes (e.g. en, fr, de) |
| `search_recency_filter` | enum(`hour`, `day`, `week`, `month`, `year`) \| null | no | — | Filter by publication recency (hour, day, week, month, or year) |
| `search_after_date_filter` | `string` \| null | no | — | Return results published after this date (MM/DD/YYYY) |
| `search_before_date_filter` | `string` \| null | no | — | Return results published before this date (MM/DD/YYYY) |
| `last_updated_before_filter` | `string` \| null | no | — | Return results last updated before this date (MM/DD/YYYY) |
| `last_updated_after_filter` | `string` \| null | no | — | Return results last updated after this date (MM/DD/YYYY) |
| `image_format_filter` | array[`string`] \| null | no | — | Filter image results by format (e.g. png, jpg) |
| `image_domain_filter` | array[`string`] \| null | no | — | Limit image results to specific domains |
| `stream_mode` | enum(`full`, `concise`) | no | `full` | Controls the format of streaming events. 'full' suppresses reasoning events and includes metadata inline; 'concise' emit |
| `reasoning_effort` | enum(`minimal`, `low`, `medium`, `high`) \| null | no | — | Controls how much effort the model spends on reasoning |
| `language_preference` | `string` \| null | no | — | ISO 639-1 language code for preferred response language |

### Response — `CompletionResponse`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `id` | `string` | **yes** | — | Unique identifier for the completion |
| `model` | `string` | **yes** | — | Model used for generation |
| `created` | `integer` | **yes** | — | Unix timestamp when the completion was created |
| `usage` | `UsageInfo` \| null | no | — |  |
| `object` | `string` | no | `chat.completion` | Object type identifier |
| `choices` | array[`Choice`] | **yes** | — | Array of completion choices |
| `citations` | array[`string`] \| null | no | — | URLs of sources used to generate the response |
| `search_results` | array[`ApiPublicSearchResult`] \| null | no | — | Search results used for context in the response |
| `images` | array[`ImageResult`] \| null | no | — | Array of images returned when return_images is true |
| `related_questions` | array[`string`] \| null | no | — | Array of related questions returned when return_related_questions is true |

### `Choice`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `index` | `integer` | **yes** | — | Index of the choice in the array |
| `finish_reason` | enum(`stop`, `length`) \| null | no | — | Reason generation stopped (stop or length) |
| `message` | `ChatMessage-Output` | **yes** | — | Complete message (non-streaming) |
| `delta` | `ChatMessage-Output` | **yes** | — | Incremental message delta (streaming) |

Note `delta` is declared **required alongside `message`**, which is unusual — a non-streaming response still
carries a `delta` object. Servicesim emits both.

### `UsageInfo`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `prompt_tokens` | `integer` | **yes** | — | Number of tokens in the prompt/input |
| `completion_tokens` | `integer` | **yes** | — | Number of tokens in the completion/output |
| `total_tokens` | `integer` | **yes** | — | Total tokens used (prompt + completion) |
| `search_context_size` | `string` \| null | no | — | Size of search context used |
| `citation_tokens` | `integer` \| null | no | — | Number of tokens used for citations |
| `num_search_queries` | `integer` \| null | no | — | Number of search queries executed |
| `reasoning_tokens` | `integer` \| null | no | — | Number of tokens used for reasoning |
| `cost` | `Cost` | **yes** | — | Cost breakdown for the request |

### `Cost`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `input_tokens_cost` | `number` | **yes** | — | Cost for input tokens in USD |
| `output_tokens_cost` | `number` | **yes** | — | Cost for output tokens in USD |
| `reasoning_tokens_cost` | `number` \| null | no | — | Cost for reasoning tokens in USD |
| `request_cost` | `number` \| null | no | — | Cost for web search requests in USD (includes pro search cost if applicable) |
| `citation_tokens_cost` | `number` \| null | no | — | Cost for citation tokens in USD |
| `search_queries_cost` | `number` \| null | no | — | Cost for search queries in USD |
| `total_cost` | `number` | **yes** | — | Total cost for the request in USD |

`cost` is **required** inside `UsageInfo`. `usage` itself is optional on `CompletionResponse`.

## Surface 2 — Agent API (`POST /v1/agent`)

The announced successor to Sonar. Note that `model` here is a `provider/model` string such as
`openai/gpt-5` or `anthropic/claude-sonnet-4-6` — the Agent API is a multi-provider router, not a
Perplexity-models-only surface.

### Request — `ResponsesRequest`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `input` | `Input` | **yes** | — |  |
| `background` | `boolean` | no | — | Run the response asynchronously. With `stream: false`, the request returns immediately with `status: "queued"`; poll `GE |
| `instructions` | `string` | no | — | System instructions for the model |
| `language_preference` | `string` | no | — | ISO 639-1 language code for response language |
| `max_output_tokens` | `integer` (int32, min 1) | no | — | Maximum tokens to generate. Shared optional Agent API request parameter, but **required when using `anthropic/*` models**: "If omitted for an Anthropic model, the API returns HTTP 400 with: validation failed: max_output_tokens is required when using Anthropic models." |
| `max_steps` | `integer` (int32, min 1, **max 100**) | no | — | Maximum number of research loop steps. If provided, overrides the preset's max_steps value. For `model` or `models` without a preset the default is 1. Must be >= 1 if specified. Maximum allowed is 100. |
| `model` | `string` | no | — | Model ID in provider/model format (e.g., `openai/gpt-5.6-terra`, `anthropic/claude-sonnet-4-6`). If `models` is also provided, `models` takes precedence. **Required if neither `models` nor `preset` is provided.** |
| `models` | array[`string`] (min 1, max 5) | no | — | Model fallback chain. Each model is in provider/model format. Models are tried in order until one succeeds. Max 5 models allowed. If set, takes precedence over the single `model` field. The `response.model` will reflect the model that actually succeeded. |
| `preset` | `string` | no | — | Preset configuration name (e.g., `fast`, `low`, `medium`, `high`, `xhigh`). Pre-configured model with system prompt and search parameters. Required if `model` is not provided. |
| `profile` | `ProfileReference` | no | — | Saved, versioned configuration to run with. The version is resolved when the request is admitted. **Cannot be combined with `preset`.** New in the 2026-10-01 document. |
| `previous_response_id` | `string` | no | — | OpenAI-compatible previous response id for multi-turn response chains. When set, the new response continues from the com |
| `reasoning` | `ReasoningConfig` | no | — |  |
| `response_format` | `ResponseFormat` | no | — |  |
| `store` | `boolean` | no | — | OpenAI-compatible storage toggle. When false, the response is hidden from later retrieve calls, and the echoed response  |
| `stream` | `boolean` | no | — | If true, returns SSE stream instead of JSON |
| `tools` | array[`Tool`] | no | — | Tools available to the model |
| `skills` | array[`Skill`] (max 16) | no | — | Built-in, request-scoped inline, and organization-owned custom skills available to the model. Skill metadata is disclosed to the model up front; full instructions are loaded on demand through the load_skill tool. Requests with skills run on the durable backend. |
| `temperature` | `number` | no | — | OpenAI-compatible sampling temperature forwarded to generation. |
| `top_p` | `number` | no | — | OpenAI-compatible nucleus sampling parameter forwarded to generation. |

### `ProfileReference`

`additionalProperties: false`. Required `type` (enum `custom`) and `id` (string, 1 to 128 characters); optional
`version` (string, "Version to bind to, or `latest`. Omitted means `latest`").

### `InputItem` (discriminated union)

`Input` is a string or an array of `InputItem`, discriminated on `type`: `message` (`InputMessage`: required
`type`, `role`, `content`; `role` is `user`, `assistant`, `system` or `developer`), `function_call`
(`FunctionCallInput`: required `type`, `call_id`, `name`, `arguments`) and `function_call_output`
(`FunctionCallOutputInput`: required `type`, `call_id`, `output`). Every variant requires `type`.

### How the profile treats the Agent request

The document states most of these rules; the profile enforces the ones marked below. Everything it does **not** state
is marked `SIMULATOR-POLICY` or `INFERENCE`, and none of that is a vendor guarantee.

| Rule | Source | Behaviour |
|---|---|---|
| A request that fails validation is **HTTP 400** with an `ErrorInfo` body, not 422 | `#/paths/~1v1~1agent/post/responses` documents only `200` and `400`; no Agent operation documents `422`; the `max_output_tokens` text gives the form "HTTP 400 with: validation failed: ..." | Enforced. The message is `validation failed: <message>`, naming the first failing field in request-schema order. Applying that wording to rules other than `max_output_tokens`, and naming only one failure, is `SIMULATOR-POLICY`. Sonar's validation 422 is unchanged. |
| One of `model`, `models`, `preset` or a valid `profile` must be present | `model`: "Required if neither models nor preset is provided"; `preset`: "Required if model is not provided" | Enforced. An empty `model` string, or an empty entry of `models`, selects nothing (`SIMULATOR-POLICY`). The `preset` text read literally conflicts with a `models`-only request, which the `model` text exempts; the lenient reading is taken and a `models`-only request is accepted (`INFERENCE`). |
| A valid `profile` counts as a model selection | the `model` text names only `models` and `preset`; `profile`: "Saved, versioned configuration to run with ... Cannot be combined with preset" | `INFERENCE`: the two read as alternatives and nothing says a profile does not supply the model, so a profile-only request is accepted rather than rejecting traffic the live API may accept. An invalid profile selects nothing and is reported once, as itself. The echoed model for it is `profile/<id>` (`SIMULATOR-POLICY`, below). |
| `models` is a non-empty array of at most 5 strings | `models`: `minItems` 1, `maxItems` 5, string items | Enforced. |
| `response.model` echoes the selection | `models` "takes precedence"; `response.model` "will reflect the model that actually succeeded" | The first non-empty entry of `models`, else `model`. A scenario's own `model:` overrides it. For a preset-only request, `preset/<name>`, and for a profile-only request, `profile/<id>`: `SIMULATOR-POLICY`, since the document does not say what either echoes. |
| `anthropic/*` requires `max_output_tokens` | `max_output_tokens` text, quoted above | Enforced with the quoted message. Any non-empty entry of a `models` chain counts: `INFERENCE`, the document not saying whether a chain is checked up front. A preset or profile request is not checked: its model is not knowable here. |
| `profile` shape; not combinable with `preset` | `ProfileReference`; the `profile` text | Enforced. What a saved profile configures is not simulated. |
| `input[]` items need a valid `type` | `InputItem` discriminator; every variant requires `type` | Enforced. The per-variant required properties are not. |
| `max_steps` integer from 1 to 100; `stream` boolean | the two properties | Enforced. |
| `background` and `store` are booleans | the two properties | Enforced: anything else is a `400` (`perplexity.agent.background.invalid`, `perplexity.agent.store.invalid`). What `background: true` and `store: false` do is a lifecycle, not a request rule: see "Lifecycle: background runs and retrieve". |
| `search_results[].source` is `web` | `SearchSource` enum is `["web"]` | A load-time error for any other `source_type` in an Agent fixture. Sonar's own `source` allows `attachment`. |
| `usage.cost.currency` is `USD` | `Currency` enum is `["USD"]` | A load-time error for any other value in a fixture. |
| 401, 403, 429, 500 | none documented on any Agent operation | `SIMULATOR-POLICY`. The status codes are ordinary HTTP and the profile must fail closed on authentication; the `ErrorInfo` body is an extrapolation from the documented `400`, and the `code` and `type` values are Servicesim's. |

### Response — `ResponsesResponse`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `created_at` | `integer` | **yes** | — | Unix timestamp when the response was created |
| `error` | `ErrorInfo` | no | — | Error details if the response failed |
| `id` | `string` | **yes** | — | Unique identifier for the response |
| `model` | `string` | **yes** | — | Model used for generation |
| `object` | `ResponsesObjectType` | **yes** | — | Object type identifier |
| `output` | array[`OutputItem`] | **yes** | — | Array of output items (messages, search results, tool calls) |
| `status` | `Status` | **yes** | — | Status of the response |
| `usage` | `ResponsesUsage` | no | — | Token usage and cost information |

### `OutputItem` (discriminated union)

Discriminated on `type`:

- `fetch_url_results` → `FetchUrlResultsOutputItem`
- `finance_results` → `FinanceResultsOutputItem`
- `function_call` → `FunctionCallOutputItem`
- `mcp_call` → `McpCallOutputItem`
- `mcp_list_tools` → `McpListToolsOutputItem`
- `message` → `MessageOutputItem`
- `people_search_results` → `PeopleSearchResultsOutputItem`
- `sandbox_results` → `SandboxResultsOutputItem`
- `search_results` → `SearchResultsOutputItem`
- `tool_search_output` → `ToolSearchOutputItem`

### `MessageOutputItem`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `content` | array[`ContentPart`] | **yes** | — |  |
| `id` | `string` | **yes** | — |  |
| `role` | `RoleType` | **yes** | — |  |
| `status` | `Status` | **yes** | — |  |
| `type` | enum(`message`) | **yes** | — |  |

### `SearchResultsOutputItem`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `queries` | array[`string`] | no | — |  |
| `results` | array[`SearchResult`] | **yes** | — |  |
| `type` | enum(`search_results`) | **yes** | — |  |

### `ContentPart`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `annotations` | array[`Annotation`] | no | — |  |
| `text` | `string` | **yes** | — |  |
| `type` | `ContentPartType` | **yes** | — | Type of a content part |

`ContentPartType` enum: `output_text` (the only member, enumerated in the 2026-10-01 document; the profile's value
was inferred before that and is now confirmed).

### `Annotation`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `end_index` | `integer` | no | — | End character index of the annotated text |
| `start_index` | `integer` | no | — | Start character index of the annotated text |
| `title` | `string` | no | — | Title of the cited source |
| `type` | `string` | no | — | Annotation type (url_citation) |
| `url` | `string` | no | — | URL of the cited source |

`Annotation.type` is a free string, not an enum; `url_citation` appears only in its description. The indices are
described as "character" indices and the profile counts bytes, so the unit is `UNVERIFIED` and matters only for
non-ASCII fixtures.

### `SearchResult`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `date` | `string` | no | — | Publication date of the result |
| `id` | `integer` | **yes** | — | Unique numeric identifier for the result |
| `last_updated` | `string` | no | — | Date the result was last updated |
| `snippet` | `string` | **yes** | — | Text snippet from the search result |
| `source` | `SearchSource` | no | — | Source type of the result |
| `title` | `string` | **yes** | — | Title of the search result page |
| `url` | `string` | **yes** | — | URL of the search result page |

`SearchResult.id` is an **integer**, not a string — it differs from every other id in this repository.

`SearchSource` enum: `web` only. Sonar's own result schema (`ApiPublicSearchResult`) is a different schema whose
`source` also allows `attachment`; the two must not be conflated.

### `ResponsesUsage`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `cost` | `ResponsesCost` | no | — | Cost breakdown for the request |
| `input_tokens` | `integer` | **yes** | — | Number of input tokens used |
| `input_tokens_details` | `object` | no | — |  |
| `output_tokens` | `integer` | **yes** | — | Number of output tokens generated |
| `tool_calls_details` | `object` | no | — | Details about tool call invocations |
| `total_tokens` | `integer` | **yes** | — | Total tokens used (input + output) |

### `ResponsesCost`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `cache_creation_cost` | `number` | no | — | Cost for cache creation in USD |
| `cache_read_cost` | `number` | no | — | Cost for cache reads in USD |
| `currency` | `Currency` | **yes** | — | Currency of the cost values |
| `input_cost` | `number` | **yes** | — | Cost for input tokens in USD |
| `output_cost` | `number` | **yes** | — | Cost for output tokens in USD |
| `tool_calls_cost` | `number` | no | — | Cost for tool call invocations in USD |
| `total_cost` | `number` | **yes** | — | Total cost for the request in USD |

`Currency` enum: `USD` only. The document gives no formula for `total_cost`; deriving it as input plus output when a
scenario leaves it unset is `SIMULATOR-POLICY`, and cache and tool costs are not folded in.

### `Status`

Enum: `completed`, `failed`, `incomplete`, `in_progress`, `queued`, `cancelled`

### `ErrorInfo`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `code` | `string` | no | — | Error code |
| `message` | `string` | **yes** | — | Human-readable error message |
| `type` | `string` | no | — | Error type category |

`createAgent` documents exactly two responses, `200` and `400` (`400`: `{error: ErrorInfo}`, "Invalid request. Includes
an unresolvable `previous_response_id`"). The retrieve, files and cancel operations document `404`, and cancel also
`400`. **No Agent operation documents `401`, `403`, `422`, `429` or `500`**; `HTTPValidationError` is referenced only
by non-Agent operations. `code` and `type` are free strings the document does not enumerate, so their values are
Servicesim's.

### `EventType` (streaming)

Enum: `response.created`, `response.in_progress`, `response.completed`, `response.failed`, `response.output_item.added`, `response.output_item.done`, `response.output_text.delta`, `response.output_text.done`, `response.reasoning.started`, `response.reasoning.search_queries`, `response.reasoning.search_results`, `response.reasoning.fetch_url_queries`, `response.reasoning.fetch_url_results`, `response.reasoning.stopped`

## Streaming (SSE)

Verified **2026-08-15** against the pages listed under "Documentation sources" above. This section pins the
wire shape a `stream: true` request receives. It was written ahead of an implementation, per
[`docs/design/streaming.md`](../../docs/design/streaming.md) §10's contract-fidelity prerequisite, and — per "What
Servicesim simulates" below — since **Phase 5 unit 1 (2026-08-15)** the Sonar surface (`POST /v1/sonar` and its
two aliases) serves this shape for a `stream: true` request against an entry whose policy is `stream`. An entry
whose policy is `warn` (the default) still receives a complete non-streaming body plus a warning, and `reject`
still answers `422` on Sonar (on the Agent surface it answers `400` with `ErrorInfo`, since 2026-10-01).
Since **Phase 5 unit 3 (2026-08-15)** the Agent API's typed SSE grammar is simulated too,
on `POST /v1/agent` and its aliases, under the same `warn`/`reject`/`stream` switch; see "What Servicesim
simulates" for the exact split.

### Chat completions (`POST /v1/sonar`, `/chat/completions`, `/v1/chat/completions`)

**Frame envelope.** Unnamed `data:` lines; no `event:` line is documented anywhere for this surface.
`chat-completions-post.md`'s `stream` field description says only "If true, returns streaming SSE response,"
without naming a chunk schema or a termination sentinel. The OpenAPI document's `/v1/sonar` operation declares
**only** an `application/json` response referencing `CompletionResponse`: no `text/event-stream` content type,
and no `ChatCompletionChunk`/`chat.completion.chunk` schema exists anywhere in the specification (confirmed by a
full-text search of the fetched document on 2026-08-15). **This surface's chunk envelope is therefore prose-only
— `sonar/pro-search/stream-mode.md` — and is not pinned by the machine-readable specification the rest of this
file is generated from.**

`stream_mode` (request field, already in the Sonar request table above) selects between two grammars, quoted
from <https://docs.perplexity.ai/docs/sonar/pro-search/stream-mode.md>:

- **`full` (default).** "Traditional streaming format with complete message objects in each chunk." One object
  type only, `chat.completion.chunk`. Aggregation is "Server-side (includes `choices.message`)."
  *Inference, not a page statement:* the page does not say in so many words that every chunk's
  `choices[].message` carries the aggregated-so-far message alongside `choices[].delta`; that reading follows
  from "Server-side" aggregation plus "complete message objects in each chunk," but is recorded here as an
  inference, not a quotation. Search results arrive "Multiple times during stream," not held back for a final
  chunk — in tension with `sonar/features.md` (illustrative only, not authoritative), which separately says
  "Search results and metadata are delivered in the **final chunk(s)** of a streaming response, not
  progressively during the stream." That tension is unresolved; this file follows `stream-mode.md` as the
  authoritative page for the two-mode grammar.
- **`concise`.** "Optimized streaming format with reduced redundancy and enhanced reasoning visibility."
  Aggregation is "Client-side (delta only)." Four object types, each `object` value quoted verbatim with the
  page's own one-line description:

  | `object` value | Page's description |
  |---|---|
  | `chat.reasoning` | "Streamed during the reasoning stage, containing real-time reasoning steps and search operations." |
  | `chat.reasoning.done` | "Marks the end of the reasoning stage and includes all search results (web, images, videos) and reasoning steps." |
  | `chat.completion.chunk` | "Streamed during the response generation stage, containing the actual content being generated." |
  | `chat.completion.done` | "Final chunk indicating the stream is complete, including final search results, usage statistics, and cost information." |

  The page states directly: "Search results and usage information only appear in `chat.reasoning.done` and
  `chat.completion.done` chunks," and "Cost information is only available in the `chat.completion.done` chunk" —
  so `usage` and `search_results` (and `images`, per the examples below) ride on **both** done-frames; only
  `cost` is `chat.completion.done`-only.

**Raw frame examples, reproduced verbatim from `stream-mode.md`'s four "Structure" examples** (`[...]` marks the
page's own elisions, not a cut made here):

```json
{
  "id": "cfa38f9d-fdbc-4ac6-a5d2-a3010b6a33a6",
  "model": "sonar-pro",
  "created": 1759441590,
  "object": "chat.reasoning",
  "choices": [{
    "index": 0,
    "finish_reason": null,
    "message": { "role": "assistant", "content": "" },
    "delta": {
      "role": "assistant",
      "content": "",
      "reasoning_steps": [{
        "thought": "Searching the web for Seattle's current weather...",
        "type": "web_search",
        "web_search": {
          "search_results": [...],
          "search_keywords": ["Seattle current weather"]
        }
      }]
    }
  }],
  "type": "message"
}
```

```json
{
  "id": "3dd9d463-0fef-47e3-af70-92f9fcc4db1f",
  "model": "sonar-pro",
  "created": 1759459505,
  "object": "chat.reasoning.done",
  "usage": {
    "prompt_tokens": 6, "completion_tokens": 0, "total_tokens": 6, "search_context_size": "low"
  },
  "search_results": [...],
  "images": [...],
  "choices": [{
    "index": 0,
    "finish_reason": null,
    "message": { "role": "assistant", "content": "", "reasoning_steps": [...] },
    "delta": { "role": "assistant", "content": "" }
  }]
}
```

```json
{
  "id": "cfa38f9d-fdbc-4ac6-a5d2-a3010b6a33a6",
  "model": "sonar-pro",
  "created": 1759441592,
  "object": "chat.completion.chunk",
  "choices": [{
    "index": 0,
    "finish_reason": null,
    "message": { "role": "assistant", "content": "" },
    "delta": { "role": "assistant", "content": " tonight" }
  }]
}
```

```json
{
  "id": "cfa38f9d-fdbc-4ac6-a5d2-a3010b6a33a6",
  "model": "sonar-pro",
  "created": 1759441595,
  "object": "chat.completion.done",
  "usage": {
    "prompt_tokens": 6, "completion_tokens": 238, "total_tokens": 244, "search_context_size": "low",
    "cost": {
      "input_tokens_cost": 0.0, "output_tokens_cost": 0.004, "request_cost": 0.006, "total_cost": 0.01
    }
  },
  "search_results": [...],
  "images": [...],
  "choices": [{
    "index": 0,
    "finish_reason": "stop",
    "message": { "role": "assistant", "content": "## Seattle Weather Forecast\n\n...", "reasoning_steps": [...] },
    "delta": { "role": "assistant", "content": "" }
  }]
}
```

Facts these examples pin that the prose above does not: every chunk in every object type carries **both**
`choices[0].message` and `choices[0].delta`, with `index` and `finish_reason` present on all four; the three
chunks sharing `id: "cfa38f9d-..."` show `created` **changing** per chunk (`1759441590`, `1759441592`,
`1759441595`) while `id` stays constant — this is Perplexity's own illustration of `id`/`created` behaviour and
it is a direct counterexample to the "repeat unchanged" pattern the OpenAI secondary source states for OpenAI's
own API (see "OpenAI compatibility" below); `images` appears on both done-frames alongside `search_results`;
`citations` appears on no streaming page fetched, at any scope.

`[DONE]`: `stream-mode.md`'s own Raw-HTTP code sample, sent with `"stream_mode": "concise"` explicitly, checks
`if data_str == '[DONE]': break` — concise-mode-specific evidence. `chat-completions-post.md`'s `stream` field
description does not mention `[DONE]` at all, and no other Sonar-surface page fetched pins `[DONE]` for `full`
mode; its only support there is the OpenAI-compatibility declaration below ("Streaming works exactly like
OpenAI's API"), recorded as a secondary source, not a direct Sonar statement.

**Not stated by any fetched page:** `finish_reason`'s placement is pinned for **`concise`** mode by the
`chat.completion.done` example above (`"finish_reason": "stop"`, `null` on the other three object types) but
unstated for **`full`** mode, where no example or prose page shows a chunk's `finish_reason`; `usage`/`cost`
placement specifically in **`full`** mode (as opposed to `concise`'s confirmed `chat.reasoning.done`/
`chat.completion.done` split); whether `id`/`created` repeat unchanged across a completion's chunks in **`full`**
mode — Perplexity's own **`concise`**-mode example above shows `created` changing while `id` stays constant,
which is at least one counterexample to the OpenAI-compatibility pattern, and no Perplexity page states the
`full`-mode behaviour either way; and chunk-to-token granularity.

### Responses / Agent (`POST /v1/agent`, `/v1/responses`, `/responses`)

`stream: true` (request field, already in the Agent request table above) switches the response's content type.
The OpenAPI document's `/v1/agent` operation declares a `200` response with a `text/event-stream` entry whose
schema is `$ref: '#/components/schemas/ResponseStreamEvent'` — unlike Sonar, this surface's SSE response **is**
declared in the machine-readable specification.

**The `ResponseStreamEvent` schema is retrievable.** A direct fetch of `openapi.json` (176,997 bytes) and its
inline reproduction in `agent-post.md` both surface it; an earlier edition of this file's claim that it "could
not be retrieved this session" was a fetch-tooling artefact, not a vendor gap. It is `oneOf` **exactly the 14
members** matching the `EventType` enum recorded above, discriminated by `type`
(`discriminator.propertyName: type`): `ResponseCreatedEvent`, `ResponseInProgressEvent`,
`ResponseCompletedEvent`, `ResponseFailedEvent`, `OutputItemAddedEvent`, `OutputItemDoneEvent`, `TextDeltaEvent`,
`TextDoneEvent`, `ReasoningStartedEvent`, `SearchQueriesEvent`, `SearchResultsEvent`, `FetchUrlQueriesEvent`,
`FetchUrlResultsEvent`, `ReasoningStoppedEvent`. Every event requires `type` and `sequence_number` (integer,
"Monotonically increasing sequence number for event ordering"). Per-event fields beyond those two:

| Event | Additional fields |
|---|---|
| `ResponseCreatedEvent`, `ResponseInProgressEvent` | optional `response` (`ResponsesResponse`) |
| `ResponseCompletedEvent` | optional `response` (`ResponsesResponse`) — description "Response event. Contains the full or partial response object," **not** "the full response object including usage" |
| `ResponseFailedEvent` | required `error` (`ErrorInfo`) |
| `OutputItemAddedEvent`, `OutputItemDoneEvent` | required `item` (`OutputItem`) + `output_index` (integer) |
| `TextDeltaEvent` | required `item_id`, `output_index`, `content_index`, `delta` (string) |
| `TextDoneEvent` | required `item_id`, `output_index`, `content_index`, `text` (string) |
| `ReasoningStartedEvent`, `ReasoningStoppedEvent` | optional `thought` |
| `SearchQueriesEvent` | required `queries` (array[string]) + optional `thought` |
| `SearchResultsEvent` | required `results` (array[`SearchResult`]) + optional `thought` + optional `usage` (`ResponsesUsage`) — this is where search results land on the typed grammar |
| `FetchUrlQueriesEvent` | required `urls` |
| `FetchUrlResultsEvent` | required `contents` (array[`UrlContent`]) |

Full-text counts in `openapi.json` on 2026-08-15: `[DONE]` 0, `event:` 0, `ChatCompletionChunk` 0,
`chat.completion.chunk` 0, `stream_options` 0, `sequence_number` 28.

**Frame structure**, per `agent-post.md`: "SSE stream event. Discriminate by the `type` field," and every event
schema carries "Monotonically increasing sequence number for event ordering" — confirmed by the schema table
above. **`agent-post.md` does not state that a frame also carries a named `event: <type>` SSE line**, as opposed
to an anonymous `data:`-only frame whose JSON payload carries `type`; a full-text search of `openapi.json` finds
0 occurrences of `event:` line formatting for this schema. `docs/design/streaming.md` §7's claim that every
frame carries an `event:` line is therefore simulator-chosen, not vendor-pinned — see that document's "Resolved
2026-08-15" block (A4).

**`response.completed` payload:** `output-control.md` and `agent-post.md` agree it carries the response object
inline as `event.response`, whose schema description is "Response event. Contains the full or partial response
object" — not, as an earlier edition of this file said, "the full response object including usage."

**`response.output_text.delta` payload:** `event.delta` is the incremental text fragment
(`output-control.md`, `agent-post.md`; confirmed in the schema table above as `TextDeltaEvent.delta`).

**`[DONE]`:** **unstated for this surface.** No Agent-API page (`output-control.md`, `agent-post.md`) and no
occurrence in `openapi.json` (0 hits, counted above) states a `[DONE]` sentinel for `/v1/agent`. An earlier
edition of this file's claim — "`gateway-responses-post.md` states a successful stream also ends with
`data: [DONE]`" — cited the **Router API** (`POST /router/v1/responses`, a different, unsimulated surface; see
"Documentation sources" above), not the Agent surface, and is withdrawn along with the "Contradicted" table row
it produced: the correct status for this row is unstated, not contradicted.

**Mid-stream error.** The failure *event* is documented: `response.failed` is a member of `EventType`, and
`ResponseFailedEvent` requires `type`, `sequence_number` and a top-level `error` (`ErrorInfo`, `message`
required), with no `response` property — "Contains error details when streaming fails." What no Agent-API page
or the OpenAPI document states is the failure's **ordering and termination**: what precedes the event, whether it
ends the stream, and whether the real service ever sends `response.completed` carrying `status: failed` instead
(`UNVERIFIED`). The "stream emits an `error` event followed by `response.failed` and closes without a `[DONE]`
trailer" sentence in an earlier edition of this file was the Router page's wording, not the Agent surface's, and
is withdrawn.

**`incomplete` and `cancelled`.** `EventType` has no `response.incomplete` and no `response.cancelled`
(0 occurrences of either string in the document), and `ResponseCompletedEvent` is described as containing "the
full or partial response object" — the only documented carrier for a partial response. Streaming those two
statuses as `response.completed` is `SIMULATOR-POLICY`, not a statement of what the vendor sends.

### OpenAI compatibility (declared)

Perplexity declares OpenAI-compatible framing for both surfaces. A declaration of compatibility is not itself a
Perplexity statement of the framing's details, so it is recorded as its own paragraph rather than folded into
the grammars above:

- **Sonar** — <https://docs.perplexity.ai/docs/sonar/openai-compatibility.md>: "Perplexity's Sonar API is fully
  compatible with OpenAI's Chat Completions format," and "Streaming works exactly like OpenAI's API."
- **Agent** — <https://docs.perplexity.ai/docs/agent-api/openai-compatibility.md>: "Perplexity's Agent API is
  fully compatible with OpenAI's Responses API interface," and "Streaming works with the Agent API," shown only
  through the OpenAI SDK's own iteration pattern, not through a description of the frame envelope itself.

Because the Sonar page declares compatibility, OpenAI's own chat-completions streaming reference is citable as a
**secondary** source for the chat-completions envelope, via declared compatibility, never as a Perplexity
statement in its own right:
<https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events> — the
`chat.completion.chunk` object's `id` and `created` are each stated to repeat unchanged across every chunk of
one completion ("Each chunk has the same ID," "Each chunk has the same timestamp"); `delta.role`'s own field
description is "The role of the author of this message" — the page does **not** state it appears "only in the
first chunk" (a claim an earlier edition of this file attributed to it; the word "first" does not occur on the
page at all, and this file withdraws that quotation); `usage` appears only when the request sets
`stream_options: {"include_usage": true}`, landing on a final chunk whose `choices` array is empty. Perplexity's
own `chat-completions-post.md` (the Sonar field reference) has no `stream_options` field, so this secondary
source's `usage`-placement mechanism should not be assumed to carry over — see "What is NOT stated by the
vendor" below. (The Router API's `gateway-chat-completions-post.md` does document `stream_options.include_usage`
for `/router/v1/chat/completions`, but the Router is a separate, unsimulated surface — see "Documentation
sources" above — so that is not evidence for Sonar.)

The Agent-API declaration above is a capability claim ("streaming works with the OpenAI SDK's iteration
pattern"), not a framing claim comparable to Sonar's "works exactly like," so OpenAI's Responses-API streaming
events are **not** cited here as a secondary source for the typed grammar.

### What is NOT stated by the vendor

| §7 assumption | Vendor pins it? | What was found |
|---|---|---|
| `finish_reason` and `usage` ride on the same terminal chunk (`GrammarDelta`) | No | Pinned only for `stream_mode: concise`, by example (`chat.completion.done` carries both); `full` mode's placement is not shown by any fetched page or example |
| Every `GrammarTyped` frame carries a named `event: <type>` SSE line | No | `agent-post.md` describes discrimination by a `type` field inside the JSON payload; `openapi.json` has 0 occurrences of `event:` line formatting for `ResponseStreamEvent` |
| `[DONE]` is a chat-completions concept only, never on `GrammarTyped` | Unstated | No Agent-API page or `openapi.json` (0 hits) states a `[DONE]` sentinel for `/v1/agent`; an earlier "Contradicted" finding here cited the Router API, a different, unsimulated surface, and is withdrawn |
| Exact `id`/`created` behaviour per chunk | Only via declared OpenAI compatibility (Sonar), and contradicted by Perplexity's own example | The secondary OpenAI source states `id`/`created` repeat unchanged per completion; Perplexity's own `concise`-mode `stream-mode.md` example shows `created` changing across three chunks sharing one `id` |
| Chunk-to-token granularity (one token per chunk vs batched) | No | Not addressed by any fetched page |
| `ResponseStreamEvent`'s own declared envelope fields | **Yes — resolved** | Retrieved directly from `openapi.json` and `agent-post.md`; recorded in full above |
| Full catalogue of `GrammarTyped` event names beyond the 14 already recorded above | **No further members — resolved** | `openapi.json`'s `ResponseStreamEvent.oneOf` is exactly the 14-member list matching the `EventType` enum above; an earlier ~25-member catalogue attributed to this page in a prior edition was a fetch-tooling artefact, not vendor content |
| Ordering and termination of a failed stream (`response.failed` follows `response.created`, is terminal, no `response.completed` after it) | **No** — the event and its payload are pinned, the ordering is not | `ResponseFailedEvent` requires `type`, `sequence_number` and a top-level `error`; no page or schema line says what precedes it or whether it ends the stream. The profile's sequence is `SIMULATOR-POLICY`, and whether the real service ever sends `response.completed` with `status: failed` is `UNVERIFIED` |
| Streaming of `incomplete` and `cancelled` turns | **No** | `EventType` has neither `response.incomplete` nor `response.cancelled`; `response.completed` is the only documented carrier of a "full or partial response object". `SIMULATOR-POLICY` |

## Lifecycle: background runs and retrieve

Added **2026-10-03** (issue #6, unit U5). A `POST /v1/agent` with `background: true` mints a job and answers at once
with a `queued` snapshot; `GET /v1/agent/{id}` then serves, one per retrieve, the snapshots a scenario's `background:`
block scripts. The run is scripted by retrieve count, not by elapsed time, so a consumer's polling loop is tested
without a clock and the same scenario gives the same ids and bodies every time. The scenario side is in
[`docs/scenario-schema.md`](../../../docs/scenario-schema.md#background-runs-background).

Not part of it, and not simulated: `POST /v1/agent/{id}/cancel` (a `cancel:` key under `background:` is a load error),
the files endpoints, and streaming a background run.

### What the specification documents

Pointers are into the document fetched on 2026-10-03; the dated note at the end of this section gives its hash and how
it differs from the one recorded in `provenance.yaml`.

| Item | Pointer | What it says |
|---|---|---|
| `retrieveAgent` | `#/paths/~1v1~1agent~1{id}/get` | Security `HTTPBearer`. `200` is `ResponsesResponse`. `404` is `{error: ErrorInfo}`: "Unknown id, or the response belongs to a different account, or the response was created with `store: false`." The description adds: "Only responses created with `store` omitted or `true` can be retrieved." |
| `background` | `#/components/schemas/ResponsesRequest/properties/background` | "Run the response asynchronously. With `stream: false`, the request returns immediately with `status: "queued"`; poll `GET /v1/responses/{id}` until the response reaches a terminal status. Background runs are durable, so you can also stream them and reconnect after a drop." |
| `store` | `#/components/schemas/ResponsesRequest/properties/store` | "When false, the response is hidden from later retrieve calls, and the echoed response reports `store: false`. It can still be used as a `previous_response_id` continuation source." |
| `ResponsesResponse` | `#/components/schemas/ResponsesResponse` | Required: `id`, `object`, `created_at`, `status`, `model`, `output`. `usage` is optional. |
| `ResponsesUsage` | `#/components/schemas/ResponsesUsage` | Required: `input_tokens`, `output_tokens`, `total_tokens`. `cost` is optional here. |
| `Status` | `#/components/schemas/Status` | `completed`, `failed`, `incomplete`, `in_progress`, `queued`, `cancelled`. Nothing says which of them end a run. |

Where that table is silent, the behaviour below is `SIMULATOR-POLICY`, `INFERENCE` or `UNVERIFIED`, labelled where it
appears.

### The create

"Claims nothing" means no call index and no fault attempt is spent, and no job exists afterwards.

| Request | Answer | Finding and journal label |
|---|---|---|
| `background: true`, the scenario declares a `background:` block | `200`, the queued snapshot below; a job is kept | label `perplexity.agent.background.created` |
| `background: true`, no `background:` block, or no `perplexity_agent` entry | `404` `ErrorInfo`; claims nothing | error `perplexity.agent.background.unscripted` at `body.background` |
| `background: true` with `stream: true` | `400` `ErrorInfo`, `validation failed: …`; claims nothing | error `perplexity.agent.background.stream` at `body.stream` |
| `background: true` with `store: false` | `200`, the queued snapshot under the id the synchronous path would derive; **no job is kept** | warning `perplexity.agent.background.unstored` at `body.store`; the same label |
| the namespace already holds `--max-jobs` jobs | `503` carrying the finding's own message | `job.limit_reached` (with the framework's `fault.attempt_on_rejection` warning: the index was claimed, then refused) |
| the job's id is already live | `500` carrying the finding's own message | `job.id_collision` (with the framework's `fault.attempt_on_rejection` warning: the index was claimed, then refused) |

The last two rows are Servicesim's own configuration errors, not vendor answers (`SIMULATOR-POLICY`), which is why they
carry the finding's message: it names the fix.

- **It fails closed** (`SIMULATOR-POLICY`; owner ruling 4 on issue #6). A scenario that scripts no retrieve for the run
  could only answer one with invented snapshots, so the create is refused instead, in the `404` shape an Agent create
  the scenario cannot answer already has. The same goes for `stream: true`: the specification says a background run can
  be streamed, which is not simulated, and serving a synchronous stream in its place would be the same invention. An
  entry whose `stream:` policy is `reject` still answers a `stream: true` request first, with
  `perplexity.stream.agent_unsupported`.
- **The queued snapshot.** `id` is the job's: `resp_` plus 32 hex characters derived from the scenario and the create's
  call index (`SIMULATOR-POLICY`: the specification says only `resp_<...>`). `object` is `response`; `created_at` is
  the scenario's base time; `status` is `queued`, the value the `background` text names; `model` echoes the request's
  selection as a synchronous response does; `output` is `[]`. There is no `usage`: it is optional, and nothing has run.
- **`store: false`.** The specification hides such a response from retrieve, so every later `GET /v1/agent/{id}` of it
  is `404`. What its create answers is not documented: queued, under the synchronous id, with no job, is
  `SIMULATOR-POLICY`. Where `validation.strict` promotes the warning the request is a `400` and claims nothing.
  Otherwise it claims the create lane's call index, which the derived id needs, and is fault-eligible; an `accepted`
  attempt on it raises `fault.accepted_unreachable`, because nothing is kept.
- **A background create shares the create lane's call index with synchronous creates.** `provider.MintJob` claims it,
  as every create does, so a background create **shifts which of the entry's `turns[i]` the next synchronous call in
  the same lane receives**. That is documented, not corrected. The three create spellings share one fault key,
  `perplexity:agent`, so the turn-level fault plan (the first `turns[*].fault` that declares attempts) serves
  synchronous and background creates alike.
- **A faulted create keeps its job under the same rule as any other create:** when the response carries its
  identifier intact, or the attempt says `accepted: true`. A `429` with no `accepted` leaves no job and a retry mints a
  new id.
- A request with `background` absent or `false` is the synchronous path, unchanged.

### The retrieve

The handler's order is `HEAD`, credentials, resolve the job, select the snapshot. Nothing is claimed before the
credentials are accepted and the id resolves, so a refused or unknown retrieve spends nothing and advances nothing.

| Request | Answer | Journal label |
|---|---|---|
| the id of a background job | `200`, the snapshot the script selects for this job's Nth retrieve | `perplexity.agent.retrieved.<status>` |
| no credential, or one the entry's `auth:` refuses | `401` `ErrorInfo` (`SIMULATOR-POLICY`: no Agent operation documents `401`) | `perplexity.agent.error.401` |
| an id this namespace never minted, one minted in another namespace, or a malformed id | `404` `ErrorInfo`; claims nothing | `perplexity.agent.error.404` |
| the id of a `store: false` background create | the same `404` (vendor-documented) | `perplexity.agent.error.404` |
| the id of a synchronous response | the same `404`: the named divergence below | `perplexity.agent.error.404` |
| a job whose script has no turn for this retrieve | `404` `ErrorInfo`; the retrieve **is** spent: `scenario.no_matching_turn` is recorded (with the framework's `fault.attempt_on_rejection` warning: the index was claimed, then refused) and the job's `polls` advances | `perplexity.agent.error.404` |
| `HEAD` | `405`, `Allow: GET`, `ErrorInfo`; claims and resolves nothing | `perplexity.agent.retrieve.head_refused`, finding `route.method_not_allowed` |
| any other method | the framework's `405`, `Allow: GET`, in the flat Sonar-shaped refusal body | `route.method_not_allowed` |
| a scripted fault attempt | the fault's status and body | the handler's label, as for every route |

- **The snapshot.** `id` is always the job's: a snapshot cannot script `response_id`, nor an `id` or a `status` in
  its `extra_fields` or in a fault attempt's, which are merged last and would win (each a load error). `object` is `response`. `created_at` is the snapshot's, else the scenario's base time. `status` is the snapshot's; an absent
  status is `completed`. `model` is the snapshot's, else the fixed placeholder `servicesim/unscripted`
  (`SIMULATOR-POLICY`): the specification requires `model`, a retrieve carries no request to echo one from, and the
  job record holds none. `output` is rendered as the synchronous path renders it, except that a `queued` or
  `in_progress` snapshot with no `answer` renders `[]` — the run has not answered yet. Its message item's id, when the
  snapshot scripts none, derives from the job id, so it is stable across one job's retrieves and distinct between jobs.
- **`usage` renders only when the snapshot scripts it, and its `cost` only when that is scripted too.** The
  specification makes both optional, and the acceptance rule for this work is that no response invents a zero cost: a
  zero the scenario did not script is a placeholder, never a billing fact. The synchronous path still always renders
  `usage.cost`, zeros included — see "Observed, not changed here".
- **The retrieve has a budget of its own.** Fault key `perplexity:agent.retrieve`, per job, so a retry on the retrieve
  never spends a create's attempt, and `call_index` in a `background:` turn counts that job's retrieves as long as the
  entry declares no `turn_key` extractor beyond `route`. The entry's `turn_key` keys the retrieve's lane too; the
  schema's [background section](../../../docs/scenario-schema.md#background-runs-background) has what that does. Its
  plan is the first `background.turns[*].fault` that declares attempts. A retrieve answered with a scripted fault still
  spends its index and advances the job's `polls`, and it keeps the label of the snapshot it would have served: read
  the label beside the entry's `fault_kind`. An attempt's `extra_fields` cannot carry `id` or `status`, because an
  attempt that sets nothing else is no fault in the journal, and its entry would carry no `fault_kind` to read.
- **`HEAD`.** Go's `ServeMux` delivers `HEAD` to a `GET` pattern, so without its own branch a `HEAD` would claim the
  job's next retrieve and advance its poll position for a body `net/http` then discards: one existence check would
  silently consume a snapshot. The specification declares no `HEAD` on this path (ruling 1: spec-declared routes only),
  so Servicesim refuses it, where Exa's explicit `HEAD` route is free.
- **Namespaces stand in for accounts, approximately.** The `404` text names "a different account"; the nearest thing
  Servicesim has is a namespace. But job ids are unique only within a namespace: call 0 in a second namespace mints the
  **same** id as call 0 in the first, so retrieving "another namespace's job" in the first namespace returns the first
  namespace's own job whenever the ids coincide. In a namespace that has minted at least one job, a well-formed id that
  resolves to none also raises the warning `job.foreign_id` on its `404`; in a namespace that has minted none the
  `404` carries no finding.

### The named divergence: a synchronous response is not retrievable

On the real API a synchronous response with `store` omitted is retrievable ("Only responses created with `store`
omitted or `true` can be retrieved"). Here its id is `404`, in the vendor's error shape: a plausible wrong answer.
Owner ruling 7 on issue #6 accepts it, with these reasons (`docs/proposals/cancellation-and-accepted-create.md`, Q2):

- A synchronous response has no scripted lifecycle for a retrieve to render.
- Minting a job for every stored synchronous response would burn one slot of the per-namespace job bound on each call.
- Synchronous ids derive from the route's fault key and the call index, not the lane key, so two `turn_key` lanes mint
  the same id and the second would fail as a duplicate; a scripted `response_id` collides on its second use.

No finding can name this divergence: the handler cannot tell a synchronous id from a stale one, and `job.foreign_id`
needs the namespace to have minted a job. This paragraph is the signal.

### Terminal statuses (`SIMULATOR-POLICY`, `UNVERIFIED`)

The `Status` enum has no terminal/non-terminal split, so which statuses end a run is Servicesim's reading:
`completed`, `failed`, `incomplete` and `cancelled` are terminal, `queued` and `in_progress` are not, and an absent
status is `completed`. The only thing that depends on it is the load-time check that a script never serves a
non-terminal snapshot after a terminal one (`perplexity.agent.background.terminal_then_pending`). Whether the real
service can leave `incomplete` or `failed` is not documented. The 2026-10-01 audit recorded the same plausible reading.

### Findings this profile raises on the lifecycle

| Code | Severity | When |
|---|---|---|
| `perplexity.agent.background.unscripted` | error, per request | `background: true` and no `background:` block |
| `perplexity.agent.background.stream` | error, per request | `background: true` with `stream: true` |
| `perplexity.agent.background.unstored` | warning, per request | `background: true` with `store: false` |
| `perplexity.agent.background.field` | error, at load | a `response_id` or `stream` key in a background snapshot, or an `id` or `status` key in its `extra_fields` or in the `extra_fields` of one of the turn's fault attempts |
| `perplexity.agent.background.terminal_then_pending` | error, at load | a non-terminal snapshot served after a terminal one, judged in serve order |
| `perplexity.agent.background.script_exhausted` | warning, at load | the last background turn has a condition a retrieve can fail (any condition but a `route` naming the retrieve, in either spelling), so the retrieve after it is a `404` for a job that exists |
| `perplexity.agent.background.body_predicate` | warning, at load | `body_contains` or `body_json` on a background turn: a retrieve carries no body, so it can never match |

The framework's own load findings for a `background:` block are in
[`docs/scenario-schema.md`](../../../docs/scenario-schema.md#background-runs-background).

### Unresolved: the poll path in the `background` text

The `background` description says to poll `GET /v1/responses/{id}`. The document declares no such path: it has 18
paths and none contains "respons", and `/v1/responses` occurs in the whole document once, in that sentence. The path
the document does declare is `GET /v1/agent/{id}`, and `cancelAgentResponse` tells a client to "poll
`GET /v1/agent/{id}` for its terminal status". That `/v1/responses/{id}` is the OpenAI-SDK spelling of the retrieve, as
`/v1/responses` is of the create, is an `INFERENCE` and nothing more. Owner ruling 1 on issue #6 serves the declared
route only, so `GET /v1/responses/{id}` answers `404` here. The inconsistency stays recorded as unresolved.

### Observed, not changed here

- The synchronous Agent path always renders `usage.cost`, zeros when the scenario scripts none, although the
  specification makes `ResponsesUsage.cost` optional. That is an existing invented zero, outside this unit.
- `ResponsesRequest.store`'s description says "the echoed response reports `store: false`", but `ResponsesResponse` has
  no `store` property, so nothing is rendered for it.
- `UNVERIFIED`: what a real `queued` or `in_progress` retrieve carries in `output` and `usage`, whether `created_at`
  is the creation time, and whether a run can be reconnected to as the `background` text promises. The snapshots are
  the scenario's.

### Goldens

`perplexity-agent-background-queued.json` is `simulator-chosen` as a whole: the envelope and the `queued` status come
from the specification, but what a queued run's body carries — the empty `output` and the omitted `usage` — is
`UNVERIFIED` and Servicesim's, and an entry carries one `kind`. `perplexity-agent-background-completed.json` and `perplexity-agent-retrieve-404.json` are
`vendor-documented` for the status and the envelope, with the values and the `ErrorInfo` strings Servicesim's. All
three are dated 2026-10-01 in `provenance.yaml`, with a comment saying why.

### Dated note, 2026-10-03: what was re-read, and what moved

The lifecycle operations were read against a fresh fetch of <https://docs.perplexity.ai/openapi.json> (215,432 bytes,
sha256 `1a269d5596e506d3189e57c3ae84874a21c3532afe8c95de6f21e55f17001f15`, `info.version` `1.0.0`). `retrieveAgent`,
`cancelAgentResponse`, the `background` and `store` descriptions, `Status`, `ResponsesResponse` and `ErrorInfo` agree
with everything the 2026-10-01 audit recorded about them in
[`docs/audits/2026-10-01-perplexity-agent.md`](../../../docs/audits/2026-10-01-perplexity-agent.md); with the old bytes
gone, that is all that can be compared.

The document has nonetheless moved. That hash is not the one recorded in `provenance.yaml`'s `spec:` block
(`e0b92edf…`, 208,564 bytes, 2026-10-01), and the old bytes were not kept, so the full difference cannot be computed.
Differences are confirmed. Re-running the audit's reproduction against the new bytes shows: `ResponsesRequest` has 20
properties where the audit counted 19, the extra one `tool_choice` (`allOf` `ToolChoice`: a string from `none`,
`auto`, `required`, or an object; its description names `{"type":"image_search"}`); `ResponsesCost` has a
`tool_calls_cost_details` property this file's table does not list; `EventType` and `ResponseStreamEvent.oneOf` have 16
members where this file records 14, the new ones `response.reasoning.image_search_queries` and
`response.reasoning.image_search_results`; the `OutputItem` discriminator has 11 types where this profile counts ten,
the new one `image_search_results`; and `sequence_number` occurs 32 times where the audit counted 28. Neither
`agentFields` nor the audit lists `tool_choice`, so a request carrying it still draws a `request.unknown_field`
warning. The lifecycle renders or requires none of these (`tool_calls_cost_details` is optional); all are left for the
re-audit, tracked in #27.

**A whole-bundle re-audit was not done.** `contracts/README.md`'s "sanctioned refresh procedure" is the way to move
`verified:` and `spec:` forward, and it starts with re-reading every consumed field. Moving them now would record bytes
the bundle was not audited against and erase the drift signal. So the `spec:` block, the provider-level `verified:` and
the Perplexity cell of the index table stay at 2026-10-01, and the three new golden entries carry that date too.

## What Servicesim simulates

Per the plan's principle *model the consumed contract, not the entire vendor*, Servicesim implements the
subset a C360 research adapter parses:

- `POST /v1/sonar` and its `/chat/completions` and `/v1/chat/completions` aliases — full request
  validation, `choices`, `citations`, `search_results`, `usage` with required `cost`.
- `POST /v1/agent` and its `/v1/responses` and `/responses` aliases — the non-streaming body, with `message`
  and `search_results` output items, `usage`/`cost`, and the `ErrorInfo` envelope — for every Agent error, a
  validation failure included, which is a `400` rather than a `422` (see "How the profile treats the Agent
  request") — plus streaming (below).
- Sonar streaming, `stream_mode: full` only. `providers.perplexity.stream:` (a Sonar entry's
  `when_requested`) selects between three behaviours. The default, `warn`, journals
  `perplexity.stream.unimplemented` and answers a `stream: true` request with a complete non-streaming
  body. `reject` turns that into a `422` naming `body.stream` — what a consumer whose primary path always
  streams should set, so its fixtures are not recorded against a body the real API would never have sent.
  Since **Phase 5 unit 1 (2026-08-15)**, `stream` serves the scripted `stream_mode: full` GrammarDelta SSE
  sequence (`data:` frames closed by `data: [DONE]`) instead. A `stream_mode: concise` request against a
  streaming entry is served that same full-mode transcript with a
  `perplexity.stream_mode.concise.unscripted` warning, not rejected and not the concise-mode sequence — see
  below.
- Agent API streaming, the `GrammarTyped` sequence, since **Phase 5 unit 3 (2026-08-15)**.
  `providers.perplexity_agent.stream:` (the Agent entry's `when_requested`) selects between the same three
  behaviours Sonar has, decoded and validated exactly the same way. The default, `warn`, journals
  `perplexity.stream.agent_unsupported` — the renamed
  (from `perplexity.agent.stream.unsupported`) unconditional warning this surface always raised before this
  unit — and answers with a complete non-streaming body. `reject` turns that into a `400` `ErrorInfo`
  (`validation failed: ...`) naming the stream, as every Agent validation failure is a `400`.
  `stream` serves six of the fourteen `EventType` members, in this exact order, for a turn
  scripting N deltas: `response.created` (the `ResponsesResponse` in its initial `in_progress` state — empty
  `output`, zero `usage`), `response.output_item.added` (the message item, `in_progress`, empty `content`),
  N × `response.output_text.delta`, `response.output_text.done` (the aggregate text),
  `response.output_item.done` (the completed message item), and terminal `response.completed`, whose
  `response` is the identical `ResponsesResponse` the non-streaming route renders for the same turn —
  `output[]`, `usage` and `cost` included, one function building both. Every one of the four message-item
  events' `output_index` is the message item's actual position within the turn's `output[]` — 1, not 0, for a
  turn that also projects a `search_results` item, which always renders first. `response.in_progress` is not
  emitted — this build's minimal reading of the design's own illustrative sketch in
  [`docs/design/streaming.md`](../../docs/design/streaming.md) §7 (repo-authored, not a vendor example), which
  shows only created → delta → completed. Every event carries a monotonically increasing `sequence_number`
  starting at 0 and an `event: <type>` SSE line (simulator-chosen — see the "Streaming (SSE)" section above).
  No `data: [DONE]` sentinel: it is a chat-completions concept only, and `terminal.omit_done` is meaningless
  here — declaring it on an Agent turn raises the load-time warning `perplexity.stream.done_ignored` rather
  than being silently accepted. `terminal.omit_usage` nils `usage` inside `response.completed`'s `response`
  object specifically, leaving every other field untouched — except key order: dropping a key round-trips the
  object through a map, so it comes back alphabetised at every nesting level, unlike every other frame's
  struct order. A turn whose `status` is `failed` or `cancelled` renders no message output item at all (the
  non-streaming route's own rule), so `stream` degrades to just two frames for such a turn rather than the
  six-event sequence above. For `failed` they are `response.created` then a terminal **`response.failed`**
  carrying the scenario's `error` at top level (since 2026-10-01; the golden is
  `perplexity-agent-stream-failed.sse`): the event is the specification's, while that created comes first, that
  `failed` ends the stream with no `response.completed` after it, and that it carries no `usage` and no
  `extra_fields` (the schema has no response object) are `SIMULATOR-POLICY`. For `cancelled` they are
  `response.created` then `response.completed` carrying `status: "cancelled"`, and an `incomplete` turn keeps
  its message item and ends in `response.completed` carrying `status: "incomplete"`; both are
  `SIMULATOR-POLICY`, see "Streaming (SSE)" above.
- Background runs, since **2026-10-03** (issue #6): `background: true` on `POST /v1/agent` and its aliases mints a job
  and answers a `queued` snapshot, and `GET /v1/agent/{id}` serves the snapshots the scenario's `background:` block
  scripts, on a fault budget of its own. A synchronous response is deliberately not retrievable. See "Lifecycle:
  background runs and retrieve".

Deliberately **not** simulated, because no consumer parses them yet:

- **`stream_mode: concise`'s own four-object-type grammar** (`chat.reasoning`, `chat.reasoning.done`,
  `chat.completion.chunk`, `chat.completion.done`). A request naming it is served the full-mode transcript
  instead, with the warning noted above, not the concise-mode sequence.
- **The Agent API's seven remaining `EventType` members**: `response.in_progress` and the
  `response.reasoning.*` family (`started`, `search_queries`, `search_results`, `fetch_url_queries`,
  `fetch_url_results`, `stopped`). None has scenario vocabulary yet — there is no scripted "reasoning step" shape
  to project them from — and each is a bounded addition behind the same scenario model whenever a consumer
  needs it, exactly like the items below. (`response.failed`, listed here until 2026-10-01, is now emitted for a
  `failed` turn.)
- **An `output_item.added`/`.done` pair for the `search_results` output item.** Only the message item gets
  one; a turn that projects search results has them appear, unannounced, inside `response.completed`'s
  `output[]` at whatever index precedes the message item (see `output_index`, below). No scenario vocabulary
  scripts a `response.reasoning.*` sequence for the search step this item represents, so there is nothing to
  hang an added/done pair off of yet.
- The `sandbox_results`, `mcp_list_tools`, `mcp_call`, `function_call`, `finance_results`,
  `people_search_results`, `fetch_url_results` and `tool_search_output` output-item types.
- The files endpoints (`GET /v1/agent/{id}/files` and the file content download), `POST /v1/agent/{id}/cancel`, and
  streaming a background run (`background: true` with `stream: true` fails closed; see "Lifecycle: background runs and
  retrieve"). Background mode itself and the `GET /v1/agent/{id}` retrieve are simulated since 2026-10-03.
- `POST /search`, `/v1/embeddings`, `/v1/contextualizedembeddings`, the async Sonar endpoints, and the
  analytics endpoints.

Each is a bounded addition behind the same scenario model if a consumer needs it. Adding one is a
Servicesim release, not an architecture change.

## Deprecations

- **Sonar is supported until 2026-09-27**, per the banner on every Sonar documentation page. New adapter
  work should target the Agent API.
- **`citations` is deprecated** (changelog, May 2025) in favour of `search_results`, which carries titles,
  URLs and dates. Servicesim still emits `citations` so existing consumers keep working; contract tests
  should assert on `search_results`.
