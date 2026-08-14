# Scenario schema

A **scenario** is a single YAML file describing one deterministic corpus of sources plus the per-provider
projections that render it onto the wire. It is the only input Servicesim takes: the same scenario and the same
request at the same call position always produce byte-identical responses. Derived identifiers move with the call
position, so two successive calls are distinguishable and a fresh state lane reproduces call 0 exactly.

This is the reference for scenario authors in consuming repositories. `version: 1` is the only schema version this
build understands.

Two rules govern the whole file:

- **Unknown keys are an authoring error.** Decoding uses `KnownFields(true)`, so a typo inside a structure
  Servicesim knows about fails loudly at load rather than being silently ignored. (Unknown keys in a *request
  body* are the opposite — those are tolerated and merely warned about.)
- **Unknown provider *names* are only a warning.** See [Validation](#validation-what-fails-and-what-only-warns).

## The shape of a scenario

```yaml
version: 1                 # required, must be 1
name: fusion-overlap       # required
description: ...           # optional
seed: fusion-overlap       # optional, defaults to name
time:
  base: 2026-01-01T00:00:00Z   # optional, this is the default

sources:                   # the canonical corpus, shared by every provider
  - id: source-a
    url: https://example.test/report-a
    title: Report A

providers:                 # per-provider projections of that corpus
  exa:
    results:
      - source: source-a
```

One corpus renders into every provider's wire format. That is the point: cross-provider source overlap becomes
deliberate rather than accidental, which is what makes a consumer's deduplication and corroboration logic testable.

### Top-level keys

| Key | Type | Required | Renders to |
|---|---|---|---|
| `version` | integer | yes | Nothing. Must be `1`; any other value is a load error. |
| `name` | string | yes | The default `seed`, and `GET /__admin/scenario`. |
| `description` | string | no | Nothing. Documentation for whoever reads the file next. |
| `seed` | string | no | The stable key every derived identifier hangs off. Defaults to `name`, so two scenarios never collide on request IDs. |
| `time.base` | RFC 3339 timestamp | no | Every timestamp a response carries. Defaults to `2026-01-01T00:00:00Z`. Nothing is ever read from a real clock. |
| `sources` | list of [Source](#sources) | no | The canonical corpus. |
| `providers` | map of [provider blocks](#providers) | no | The per-provider projections. |

An empty scenario is valid: every provider then renders a well-shaped empty success.

## `sources`

One entry is one canonical document. Provider projections reference it by `id`; they never restate its URL or
title, which is how two providers end up returning the *same* source.

| Key | Type | Required | Renders to |
|---|---|---|---|
| `id` | string | yes | Nothing on the wire. The scenario-local handle projections point at. |
| `url` | string | yes | Exa `results[].url`, Tavily `results[].url`, Perplexity `search_results[].url`. Also the default Exa `results[].id`. |
| `title` | string | yes | Each provider's result title field. |
| `author` | string | no | Exa `results[].author`. Omitted when empty. |
| `author_null` | boolean | no | Forces an explicit JSON `null` for `author` instead of omitting it. Exa types it `anyOf[string, null]`; a consumer that only ever sees the field absent has not exercised the null branch. |
| `published_at` | RFC 3339 timestamp | no | Exa `publishedDate` (millisecond precision), Perplexity `search_results[].date`. |
| `text` | string | no | The full document text: Exa `results[].text`, the default Tavily `results[].content`. |
| `snippets` | list of string | no | Short excerpts. The default Perplexity `search_results[].snippet` is the first one. |
| `claims` | list of [Claim](#sourcesclaims) | no | Nothing directly. See below. |
| `image` | string | no | Exa `results[].image`. |
| `favicon` | string | no | Exa `results[].favicon`, Tavily `results[].favicon`. |

Source and fixture URLs must use a reserved domain — `.test`, `.example`, `.invalid` or `example.com`. A lint guard
enforces it, because a scenario URL that resolves to a real host is the exact failure this simulator exists to
prevent.

### `sources[].claims`

| Key | Type | Required | Renders to |
|---|---|---|---|
| `id` | string | yes | Nothing on the wire. |
| `text` | string | yes | Nothing on the wire. |

Claims never reach a response body. They exist so a scenario can express **corroboration**: two sources declaring
the same claim `id` is the signal a fusion test asserts on. Repeating an `id` across sources is intended, not a
mistake.

## `providers`

`providers` is an **open registry** keyed by provider name, not a fixed set of fields. Adding a provider to
Servicesim never changes this schema, and a scenario written today keeps working when new providers arrive.

The known names in this build are `exa`, `tavily`, `perplexity` (Sonar) and `perplexity_agent` (the Agent API).
Sonar and the Agent surface are separate entries deliberately: a scenario can rate-limit one and leave the other
healthy, which is how a consumer's migration fallback gets tested.

### Reserved envelope keys

Inside a provider block, seven keys are reserved. **Everything else is that provider's projection body.**

| Key | Type | Required | Effect |
|---|---|---|---|
| `kind` | string | no | Selects the handler implementation. Defaults to the block's own name. Set it to declare two instances of one provider — an `openai` and an `openai_fallback` — for failover tests. |
| `auth` | [AuthPolicy](#auth) | no | Credential policy for this provider. |
| `validation` | [ValidationPolicy](#validation) | no | Remaps finding severities for this provider. |
| `fault` | [Fault](#fault) | no | The deterministic failure plan. In the multi-turn form, `fault` belongs on the turn instead. |
| `turns` | list of [Turn](#the-multi-turn-form) | no | A conversation script. Mutually exclusive with a projection body at block level. |
| `turn_key` | list of string | no | What the turn cursor is keyed on. Defaults to `["route"]`. See [`turn_key`](#turn_key--what-the-cursor-counts-per). |
| `extra_fields` | map | no | Additive properties merged into the rendered response body, to exercise a consumer's tolerance of vendor evolution. |

### The single-shot form

This is the form almost every scenario ever needs: the projection fields sit directly on the provider block.

```yaml
providers:
  exa:
    auth:                    # reserved envelope key
      mode: required
    fault:                   # reserved envelope key
      attempts:
        - status: 429
          retry_after: 1
        - status: 200
    results:                 # projection body
      - source: source-a
    cost_dollars:            # projection body
      total: 0.005
```

### The multi-turn form

A `turns:` list scripts successive calls. Turns are matched in declaration order and the first whose `when` matches
wins; a turn with no `when` matches anything.

```yaml
providers:
  perplexity:
    turns:
      - when:
          call_index: 0
        respond:
          answer: Let me search for that.
      - when:
          body_contains: report-a
        respond:
          answer: Report A says ...
          citations: [source-a]
      - respond:                       # fallback: no `when`, matches anything
          answer: I don't know.
```

**The two forms are the same thing.** At load time a provider block with no `turns:` is normalised into exactly one
turn with no `when`, whose `respond` body is the block minus its reserved envelope keys. Everything downstream sees
one shape, and no handler branches on which form you wrote. There is no second system here to learn.

The `respond` body of a turn is precisely what a single-shot block's projection body would have been — the same
keys, documented in [Provider projection bodies](#provider-projection-bodies).

### `when` — the turn predicate

Every field is optional and every *present* field must match. It is an AND, never an OR; an empty `when` matches
everything.

| Key | Type | Required | Matches when |
|---|---|---|---|
| `call_index` | integer | no | This is the zero-based count of prior requests **in this turn lane** — see [`turn_key`](#turn_key--what-the-cursor-counts-per), whose default of `["route"]` makes the lane the route. |
| `body_contains` | string | no | The raw request body contains this substring. Deliberately crude — it covers "which tool result came back" without becoming an expression language. |
| `body_json` | map of string to string | no | Every dotted path matches, for example `{model: sonar, "messages.0.role": system}`. Values compare as strings after JSON scalar formatting. |

Matching considers **only the request and the call counter**. It never consults wall-clock time, because a
predicate that depends on the clock is a flaky test waiting to happen.

### Turn selection rules

1. Turns are evaluated in declaration order; the first match wins.
2. When nothing matches, the last turn with no `when` is used.
3. When nothing matches and there is no unconditional turn, the request records a `scenario.no_matching_turn`
   finding and receives a provider-shaped not-found error. It is never silently a 200.

`call_index` is drawn from the *same* per-lane counter the fault engine uses, so a scenario that rate-limits call
two and answers differently on call three stays coherent. There are not two counters that can disagree.

### `turn_key` — what the cursor counts per

`turn_key` declares the **lane** a request belongs to. One cursor exists per lane, so `call_index` counts prior
requests in that lane and nowhere else.

The default is `["route"]`: one sequence per route, which is what a single serial caller wants and what every
scenario written without this key gets.

```yaml
providers:
  perplexity:
    turn_key: ["route", "body_json:model"]     # one lane per model
    turns:
      - when:
          call_index: 0
          body_json:
            model: sonar
        respond:
          answer: sonar lane, first call.
      - when:
          call_index: 0
          body_json:
            model: sonar-pro
        respond:
          answer: sonar-pro lane, first call.
      - respond:                               # fallback, once a lane runs out of script
          answer: No further scripted turn in this lane.
```

Extractors are evaluated in the order written and joined into one key:

| Extractor | Resolves to |
|---|---|
| `route` | The route's fault key, which is what the default keys on. Aliases of one surface share it, so a retry through `/chat/completions` stays in the lane `/v1/sonar` opened. |
| `body_json:<dotted.path>` | A scalar from the decoded JSON request body, for example `body_json:model` or `body_json:messages.0.role`. A numeric segment indexes an array. Values are compared as strings after JSON scalar formatting, exactly as `when: {body_json: ...}` formats them. |
| `header:<name>` | A request header value, for example `header:x-role`. Case-insensitive in the header name, as HTTP is. |

Each contribution carries its own extractor name, so `["header:a", "header:b"]` keeps `a: x` and `b: x` in
different lanes rather than merging them on the shared value `x`.

**Why this exists.** One LLM-shaped route serving N concurrent callers has, under a route-keyed cursor, exactly one
sequence: two callers draw call indices 0 and 1 out of it and each receives the turn scripted for the other. The
response looks coherent, so the test fails somewhere else entirely and much later. Three sibling repositories hit
this independently and each re-keyed its cursor — per role, per `(scenario, role)`, and per model. The built-in
[`namespaced`](../scenarios/protocol/namespaced.yaml) scenario is the worked example.

**An unresolvable extractor warns; it never silently merges lanes.** A `body_json:` path the body does not carry, a
path landing on an object or an array rather than a scalar, a JSON `null`, an absent header, or a value longer than
128 bytes each contribute nothing and record a `scenario.turn_key_unresolved` warning against that request in the
journal. The lane is then named by the extractors that *did* resolve, and by the route's fault key when none did.
The warning is mandatory rather than a convenience: silently sharing a lane is the exact failure `turn_key` exists
to prevent, so it has to be visible where a consumer already looks.

Three further properties are worth knowing before you write a fixture against this:

- The lane key replaces the route key for **fault attempt counting on that provider as well**, so `fault` and
  `call_index` cannot disagree about which call a request is.
- The lane is resolved **once per request, by the listener**, before a handler has chosen which provider entry
  answers. A listener that serves two entries — Perplexity's Sonar and Agent surfaces do — therefore keys both on
  the primary entry's `turn_key`, and declaring a second one on the other entry describes a resolution that never
  happens.
- An unrecognised extractor form is a **load error**, `scenario.turn_key.invalid`, naming the offending index. A
  typo here would otherwise present as one silently shared lane.

Namespaces compose with this and need no declaration: a request arriving with a `/n/<namespace>` base-URL prefix
gets the namespace as the outermost component of its lane key, so two tests running the same scenario through one
container advance separate cursors. See the [README](../README.md#one-container-many-concurrent-tests).

## Referring to a source

Every projection field that names a source accepts two interchangeable forms:

```yaml
citations:
  - source-a               # scalar shorthand: reference only, every other field defaulted
  - source: source-b       # mapping form: reference plus per-entry overrides
    snippet: An override.
```

The scalar shorthand works even where the element type is a full struct. In the mapping form, the reference key is
always `source`, and unknown sibling keys are rejected. A reference to an ID that no source declares is a load
error naming the YAML path, for example `providers.exa.results[2].source: unknown source "source-z"`.

## Shared policy blocks

### `auth`

| Key | Type | Required | Effect |
|---|---|---|---|
| `mode` | `required` \| `optional` \| `reject` | no | `required` is the default. `reject` always answers 401, which is what the `unauthorized` built-in uses. |
| `expect_key` | string | no | The presented credential must match exactly. Use a fake value; only a fingerprint is ever retained. |
| `headers` | list of string | no | Overrides the accepted credential placements, for example `[authorization]` to reject an `x-api-key` that Exa would otherwise allow. |

Accepted placements by default: Exa takes `x-api-key` or `Authorization: Bearer`; Tavily and Perplexity take
`Authorization: Bearer` only.

### `validation`

| Key | Type | Required | Effect |
|---|---|---|---|
| `strict` | boolean | no | Promotes every warning to an error. |
| `promote` | list of string | no | Finding codes to raise from warning to error, for example `[request.unknown_field]`. |
| `demote` | list of string | no | Finding codes to lower from error to warning. |

### `extra_fields`

A free-form map merged into the rendered response body. Servicesim never validates its contents — the point is to
emit fields the schema does not know about, so a consumer can prove its decoder tolerates additive vendor changes.

At provider-block level it merges into the top-level response object. Several result types accept their own nested
`extra_fields`, which merge into that result entry instead.

## `fault`

A deterministic failure plan. Attempt *N* of a route receives `attempts[N]` after `repeat` expansion.

| Key | Type | Required | Effect |
|---|---|---|---|
| `attempts` | list of [FaultAttempt](#faultattempts) | yes | What each successive attempt receives. |
| `after` | `success` \| `repeat_last` | no | What happens once the list is exhausted. `success` (default) serves the scenario response; `repeat_last` makes the failure permanent. |

### `fault.attempts[]`

| Key | Type | Required | Effect |
|---|---|---|---|
| `kind` | [fault kind](#fault-kinds) | no | Inferred when omitted: a `status` of 400 or above with no other mangling field means `status`; everything unset means no fault. |
| `status` | integer | no | The HTTP status for this attempt. |
| `delay` | duration string | no | For example `250ms`. Orthogonal — it composes with every kind. |
| `retry_after` | integer | no | Seconds; sets the `Retry-After` header. |
| `headers` | map of string to string | no | Additional response headers. |
| `body` | map | no | The verbatim error body. When absent, the provider synthesises its own documented shape for `status`. |
| `error` | string | no | Fills the provider's error envelope without spelling out the whole body. |
| `tag` | string | no | Exa only: the `tag` field of its error envelope. |
| `raw_body` | string | no | Overrides the response bytes entirely. This is how `invalid_json` is expressed. |
| `content_type` | string | no | Overrides the `Content-Type` header, for `wrong_content_type`. |
| `truncate_after_bytes` | integer | no | How many body bytes reach the client before the connection dies. Zero means half the body. |
| `reset` | boolean | no | Sends a TCP RST instead of a clean FIN, so the client sees "connection reset by peer" rather than "unexpected EOF". |
| `extra_fields` | map | no | Additive properties merged into this attempt's body. |
| `repeat` | integer | no | Applies this attempt to N consecutive attempts. "Fail the first three, then succeed" is one attempt with `repeat: 3` and the default `after`. |

### Fault kinds

| `kind` | What the client observes |
|---|---|
| *(omitted)* | The scenario response, rendered normally. A trailing `- status: 200` means this. |
| `status` | A provider-shaped error body at the given status. |
| `close_before_headers` | The connection closes before any response headers arrive. |
| `truncate_body` | Headers and a partial body, then the connection dies. |
| `invalid_json` | A `200 application/json` response whose body is not valid JSON. |
| `wrong_content_type` | A valid body under the wrong `Content-Type`. |
| `empty_body` | A successful response with a zero-length body. |
| `extra_fields` | A successful response carrying additional unknown properties. |

Delays are real by default, in-process and in the container alike, so a scenario behaves identically either way.
A Go test can opt out with `testkit.WithSkippedDelays()` — but not for a timeout or cancellation test, which is
observed by bytes *not arriving* and therefore needs a genuinely short real delay.

## Provider projection bodies

These are the keys allowed in a single-shot provider block (after the reserved envelope keys) and, identically, in
a turn's `respond` mapping. Every field is optional; a projection body may be empty, and the provider then renders
a well-shaped empty success.

### `exa`

| Key | Type | Renders to |
|---|---|---|
| `request_id` | string | `requestId`. Defaults to 32 lowercase hex characters derived from the seed. |
| `results` | list of ExaResult | `results[]`. |
| `cost_dollars` | `{total, neural}` | `costDollars`, which a real Exa response always carries. `total` is required within the object. |
| `output` | `{content, grounding}` | The structured-output branch, present only when the request supplied `outputSchema`. Each `grounding` entry is `{field, citations, confidence}` where `confidence` is `low`, `medium` or `high`. |
| `answer` | ExaAnswer | The `POST /answer` response: `{fault, answer, citations, cost_dollars, extra_fields}`. Absent means `/answer` returns an empty answer with no citations. |
| `stream` | `warn` \| `reject` | Behaviour for a request carrying `"stream": true`. `warn` (default) records a journal warning and serves the ordinary JSON body; `reject` returns a provider-shaped 4xx. Streaming itself is out of scope. |

ExaResult:

| Key | Type | Renders to |
|---|---|---|
| `source` | source reference | The source this entry projects. |
| `id` | string | `results[].id`. Defaults to the source URL, matching Exa's own documented example — not a slug. |
| `text` | string | `results[].text`. Defaults to the source `text`. |
| `highlights` | list of string | `results[].highlights`. |
| `highlight_scores` | list of number | `results[].highlightScores`. |
| `summary` | string | `results[].summary`. |
| `score` | number | **Nothing.** Exa's result schema has no top-level score field; setting it raises `exa.result.score.not_emitted`. It is accepted so old fixtures load, and never emitted, because emitting it would teach consumers to parse something the real API never sends. |
| `omit_fields` | list of string | Drops named fields that would otherwise be present, for tests asserting a consumer fails on a missing required field. |
| `extra_fields` | map | Merged into this result entry. |

### `tavily`

| Key | Type | Renders to |
|---|---|---|
| `request_id` | string | `request_id`. Defaults to a derived UUID, matching Tavily's documented example. |
| `answer` | string | `answer`. The key is always emitted; omitting it here renders an explicit `null`, which distinguishes "no answer requested" from an empty answer. |
| `images` | list of `{url, description}` | `images[]`. Items are objects, never bare URL strings. |
| `results` | list of TavilyResult | `results[]`. |
| `response_time` | number | `response_time`. A JSON **number**, not a string. |
| `auto_parameters` | map | `auto_parameters`. |
| `usage` | `{credits}` | `usage`, gated by the request's `include_usage`. |

TavilyResult:

| Key | Type | Renders to |
|---|---|---|
| `source` | source reference | The source this entry projects. |
| `id` | string | `results[].id`. |
| `content` | string | `results[].content`. Defaults to the source text. |
| `score` | number | `results[].score`. |
| `raw_content` | string or `null` | `results[].raw_content`. Tri-state: absent, explicit `null`, or a string. Tavily's own documented example value is `null`. |
| `favicon` | string | `results[].favicon`. |
| `images` | list of `{url, description}` | `results[].images`. |
| `omit_fields` | list of string | Drops named fields from this entry. |
| `extra_fields` | map | Merged into this result entry. |

### `perplexity`

The Sonar surface, served on `POST /v1/sonar` and `POST /chat/completions`.

| Key | Type | Renders to |
|---|---|---|
| `completion_id` | string | The completion `id`. |
| `created` | integer | `created`. Defaults to `time.base` as a Unix timestamp, never the real clock. |
| `model` | string | `model`. Defaults to echoing the request's model. |
| `answer` | string | The assistant message content. |
| `finish_reason` | `stop` \| `length` | `choices[0].finish_reason`. |
| `citations` | list of source references | `citations[]`. Still emitted, but deprecated upstream — assert on `search_results` in new tests. |
| `search_results` | list of PerplexityResult | `search_results[]`. |
| `usage` | PerplexityUsage | `usage`. |
| `images` | list of `{image_url, origin_url, height, width}` | `images[]`. |
| `related_questions` | list of string | `related_questions[]`. |

PerplexityResult:

| Key | Type | Renders to |
|---|---|---|
| `source` | source reference | The source this entry projects. |
| `snippet` | string | `search_results[].snippet`. |
| `date` | string or `null` | `search_results[].date`. Tri-state. |
| `last_updated` | string or `null` | `search_results[].last_updated`. Tri-state. |
| `source_type` | `web` \| `attachment` | The wire field `search_results[].source`. It is **not** named `source` here: that key already belongs to the source reference, and a second field claiming it makes the YAML parser panic. |
| `omit_fields` | list of string | Drops named fields from this entry. |

PerplexityUsage: `prompt_tokens`, `completion_tokens`, `total_tokens` (integers), `search_context_size` (a
**string**, not a count), and the optional `citation_tokens`, `num_search_queries`, `reasoning_tokens`. Its `cost`
object — `input_tokens_cost`, `output_tokens_cost`, `total_cost`, plus optional `reasoning_tokens_cost`,
`request_cost`, `citation_tokens_cost` and `search_queries_cost` — is **required by the live schema**, so leaving
it out renders a derived zero-cost object rather than omitting the key.

### `perplexity_agent`

The Agent API, served on `POST /v1/agent` and `POST /v1/responses`. Its envelope shares no fields with Sonar's:
Sonar returns `choices[]`, the Agent API returns an ordered `output[]` execution trace. Ordering within `output[]`
is fixed — `search_results` first, then `message` — and a scenario cannot reorder it.

| Key | Type | Renders to |
|---|---|---|
| `response_id` | string | `id`. Defaults to a derived `resp_<32 hex>`. |
| `message_id` | string | The message output item's `id`. Defaults to a derived `msg_<32 hex>`. |
| `model` | string | `model`. Agent model IDs are `provider/model` strings such as `openai/gpt-5`. |
| `status` | `completed` \| `failed` \| `incomplete` \| `in_progress` \| `queued` \| `cancelled` | `status`. Defaults to `completed`. `failed` requires `error`. |
| `answer` | string | The text of the single `message` output item. |
| `queries` | list of string | The searches the agent reports having run. Independent of `search_results`, so a scenario can project "searched but found nothing". |
| `search_results` | list of source references | The `search_results` output item. `results[].id` is the 1-based index as a JSON **integer**. |
| `annotations` | list of `{source, start_index, end_index}` | `url_citation` spans over the answer text. Indices are byte offsets into `answer`; an out-of-range span is a load error. An empty list emits `[]` rather than omitting the key. |
| `error` | `{message, code, type}` | `error`. `message` is required by the specification. |
| `usage` | `{input_tokens, output_tokens, total_tokens, cost}` | `usage`. Note the field names differ from Sonar's. `total_tokens` is derived when zero; `cost` is `{currency, input_cost, output_cost, total_cost, cache_creation_cost, cache_read_cost, tool_calls_cost}`, with `currency` defaulting to `USD` and `total_cost` derived when zero. |

## Validation: what fails, and what only warns

Validation happens in two passes, both before the server reports ready.

1. **Envelope validation, at load.** Schema version, required fields, source-reference integrity, turn ordering,
   `when` well-formedness, fault-plan coherence, and that every `respond` node is a mapping. An error here fails
   the process rather than serving a subtly wrong contract.
2. **Projection-body validation, at composition.** Each provider decodes its own `respond` bodies and validates
   them — the `scenario` package deliberately does not know what an Exa result looks like. Readiness flips only
   after this pass.

**An unrecognised provider name is a warning, not an error.** The scenario loads, the unknown block is ignored, and
the warning appears in `GET /__admin/scenario`. The reason is concrete: a scenario file shared across repositories
must not break the moment one consumer pins an older Servicesim that has not learned the new provider yet. The
failure mode that policy avoids — one team's upgrade breaking another team's suite — costs far more than the
failure mode it accepts.

Errors from within a known structure are still hard failures, and they name the YAML path, the provider and the
turn index. A typo in a key you *did* mean to write is never silently tolerated.

## Worked example: one source through every provider

This is the shape most scenarios take. One canonical source, projected four ways.

```yaml
version: 1
name: fusion-overlap
description: One source returned by every provider, so deduplication is exercised deliberately.

time:
  base: 2026-01-01T00:00:00Z

sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    author: Example Author
    published_at: 2026-05-20T00:00:00Z
    text: Full source text of Report A.
    snippets:
      - A relevant excerpt from Report A.
    favicon: https://example.test/favicon.ico
    claims:
      - id: claim-1
        text: A normalised claim represented by this source.

  - id: source-b
    url: https://example.test/report-b
    title: Report B
    text: Full source text of Report B.
    claims:
      - id: claim-1          # the same claim: this is the corroboration signal
        text: A normalised claim represented by this source.

providers:

  exa:
    results:
      - source: source-a
        highlights:
          - A relevant excerpt from Report A.
      - source: source-b
    cost_dollars:
      total: 0.005

  tavily:
    answer: A short synthesis of Report A and Report B.
    response_time: 1.15
    results:
      - source: source-a
        score: 0.98
        raw_content: null
      - source: source-b
        score: 0.71

  perplexity:
    answer: A grounded answer citing Report A.
    finish_reason: stop
    citations:
      - source-a
    search_results:
      - source-a
      - source: source-b
        snippet: An overridden snippet for Report B.
    usage:
      prompt_tokens: 24
      completion_tokens: 96
      total_tokens: 120
      cost:
        input_tokens_cost: 0.0001
        output_tokens_cost: 0.0004
        total_cost: 0.0005

  perplexity_agent:
    answer: A grounded answer citing Report A.
    queries:
      - report a
    search_results:
      - source-a
```

Both `source-a` entries render the same URL through Exa, Tavily and Perplexity, so a consumer that fails to
deduplicate reports three results where there is one document. Both sources assert `claim-1`, so a consumer that
fails to count corroboration reports one supporting source where there are two.

## Worked example: a scripted agentic loop

A turn list scripts successive calls to one provider. This one searches on the first call, answers once the tool
result comes back, and rate-limits the retry in between.

```yaml
version: 1
name: agent-loop

sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: Full source text of Report A.

providers:
  perplexity_agent:
    auth:
      mode: required
    turns:

      - when:
          call_index: 0
        respond:
          answer: Searching for Report A.
          queries:
            - report a

      - when:
          body_contains: report-a
        fault:                       # this turn is rate-limited once, then serves its response
          attempts:
            - status: 429
              retry_after: 1
            - status: 200
        respond:
          answer: Report A states the finding.
          search_results:
            - source-a
          annotations:
            - source: source-a
              start_index: 0
              end_index: 7

      - respond:                     # fallback for anything unmatched
          status: incomplete
          answer: No further information.
```

The `fault` on the second turn draws on the same per-route attempt counter that `call_index` reads, so "the retry
after the 429" and "call index 2" describe the same request. That is why there is one counter and not two.
