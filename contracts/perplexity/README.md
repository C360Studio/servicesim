# Perplexity consumed contract

Verified against the vendor's own machine-readable specification on **2026-08-14**.

Source of truth: <https://docs.perplexity.ai/openapi.json> (OpenAPI 3.1.0, 176,777 bytes, `servers: [https://api.perplexity.ai]`). Every table below is generated from that document, not from prose documentation pages and not from memory.

> **Why this matters.** An earlier pass built this contract by reading Mintlify documentation pages and
> produced fields borrowed from OpenAI's Responses API by analogy, plus one quotation that does not exist in
> any Perplexity source. Two independent challenge agents caught it. Prose docs describe; the OpenAPI
> document decides. Regenerate this file from the spec rather than editing it by hand.

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
spellings were missing before that date and returned 404.)*

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
| `max_output_tokens` | `integer` | no | — | Maximum tokens to generate. This is a shared optional Agent API request parameter, but it is required when using anthrop |
| `max_steps` | `integer` | no | — | Maximum number of research loop steps. If provided, overrides the preset's max_steps value. Must be >= 1 if specified. M |
| `model` | `string` | no | — | Model ID in provider/model format (e.g., "openai/gpt-5", "anthropic/claude-sonnet-4-6"). If models is also provided, mod |
| `models` | array[`string`] | no | — | Model fallback chain. Each model is in provider/model format. Models are tried in order until one succeeds. Max 5 models |
| `preset` | `string` | no | — | Preset configuration name (e.g., "fast", "low", "medium", "high", "xhigh"). Pre-configured model with system prompt and  |
| `previous_response_id` | `string` | no | — | OpenAI-compatible previous response id for multi-turn response chains. When set, the new response continues from the com |
| `reasoning` | `ReasoningConfig` | no | — |  |
| `response_format` | `ResponseFormat` | no | — |  |
| `store` | `boolean` | no | — | OpenAI-compatible storage toggle. When false, the response is hidden from later retrieve calls, and the echoed response  |
| `stream` | `boolean` | no | — | If true, returns SSE stream instead of JSON |
| `tools` | array[`Tool`] | no | — | Tools available to the model |
| `skills` | array[`Skill`] | no | — | Built-in and request-scoped inline skills available to the model. Skill metadata is disclosed to the model up front; ful |
| `temperature` | `number` | no | — | OpenAI-compatible sampling temperature forwarded to generation. |
| `top_p` | `number` | no | — | OpenAI-compatible nucleus sampling parameter forwarded to generation. |

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
| `type` | `ContentPartType` | **yes** | — |  |

### `Annotation`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `end_index` | `integer` | no | — | End character index of the annotated text |
| `start_index` | `integer` | no | — | Start character index of the annotated text |
| `title` | `string` | no | — | Title of the cited source |
| `type` | `string` | no | — | Annotation type (url_citation) |
| `url` | `string` | no | — | URL of the cited source |

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

### `Status`

Enum: `completed`, `failed`, `incomplete`, `in_progress`, `queued`, `cancelled`

### `ErrorInfo`

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `code` | `string` | no | — | Error code |
| `message` | `string` | **yes** | — | Human-readable error message |
| `type` | `string` | no | — | Error type category |

### `EventType` (streaming)

Enum: `response.created`, `response.in_progress`, `response.completed`, `response.failed`, `response.output_item.added`, `response.output_item.done`, `response.output_text.delta`, `response.output_text.done`, `response.reasoning.started`, `response.reasoning.search_queries`, `response.reasoning.search_results`, `response.reasoning.fetch_url_queries`, `response.reasoning.fetch_url_results`, `response.reasoning.stopped`

## What Servicesim simulates

Per the plan's principle *model the consumed contract, not the entire vendor*, Servicesim implements the
subset a C360 research adapter parses:

- `POST /v1/sonar` and its `/chat/completions` and `/v1/chat/completions` aliases — full request
  validation, `choices`, `citations`, `search_results`, `usage` with required `cost`.
- `POST /v1/agent` and its `/v1/responses` and `/responses` aliases — non-streaming, with `message` and
  `search_results` output items, `usage`/`cost`, and the `ErrorInfo` envelope.

Deliberately **not** simulated in the initial release, because no consumer parses them yet:

- Streaming (the 14 `EventType` members above). A `stream: true` request is answered with a complete
  non-streaming body plus a `perplexity.stream.unimplemented` warning. Since **2026-08-15** a scenario can
  set `providers.perplexity.stream: reject` to turn that warning into a `422` naming `body.stream`
  instead — which is what a consumer whose primary path always streams should do, so its fixtures are not
  recorded against a body the real API would never have sent.
- The `sandbox_results`, `mcp_list_tools`, `mcp_call`, `function_call`, `finance_results`,
  `people_search_results`, `fetch_url_results` and `tool_search_output` output-item types.
- Background mode and the `GET /v1/agent/{id}` polling lifecycle, the files endpoints, and
  `POST /v1/agent/{id}/cancel`.
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
