# Perplexity consumed contract

Verified against the vendor's own machine-readable specification on **2026-08-14**, extended **2026-08-15**
with a "Streaming (SSE)" section verified against the prose pages listed under "Documentation sources" below
(and, where noted in that section, against the same OpenAPI document), and corrected the same day after two
`gateway-*-post.md` pages fetched for that section turned out to document Perplexity's **Router API**
(`/router/v1/*`) rather than the Sonar/Agent SDK aliases they were assumed to be, and after the
`ResponseStreamEvent` schema — previously recorded as unretrievable — was confirmed present in the OpenAPI
document.

Source of truth: <https://docs.perplexity.ai/openapi.json> (OpenAPI 3.1.0, 176,777 bytes, `servers: [https://api.perplexity.ai]`). Every table below is generated from that document, not from prose documentation pages and not from memory.

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

## Streaming (SSE)

Verified **2026-08-15** against the pages listed under "Documentation sources" above. This section pins the
wire shape a `stream: true` request receives. It exists ahead of an implementation, per
[`docs/design/streaming.md`](../../docs/design/streaming.md) §10's contract-fidelity prerequisite, and per "What
Servicesim simulates" below, streaming is not yet implemented — a `stream: true` request today still receives a
non-streaming body plus a warning, or a `422` if the scenario asks for `stream: reject`.

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
frame carries an `event:` line is therefore simulator-chosen, not vendor-pinned — see that document's "Open
design deltas" block.

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

**Mid-stream error:** likewise unstated for this surface by any Agent-API page fetched. The "stream emits an
`error` event followed by `response.failed` and closes without a `[DONE]` trailer" sentence in an earlier
edition of this file was the Router page's wording, not the Agent surface's, and is withdrawn.

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
