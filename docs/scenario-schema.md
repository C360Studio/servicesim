# Scenario schema

A **scenario** is a single YAML file describing one deterministic corpus of sources plus the per-provider
projections that render it onto the wire. It is the only input Servicesim takes: the same scenario and the same
request at the same call position always produce byte-identical responses. Derived identifiers move with the call
position, so two successive calls are distinguishable and a fresh state lane reproduces call 0 exactly.

This is the reference for scenario authors in consuming repositories. `version: 1` is the current schema version.
A build accepts its own version and every earlier one, so a file pinned to `version: 1` keeps loading unchanged
after Servicesim's schema moves on. See [The version gate](#the-version-gate).

Two rules govern the whole file:

- **Unknown keys are an authoring error.** Decoding uses `KnownFields(true)`, so a typo inside a structure
  Servicesim knows about fails loudly at load rather than being silently ignored. (Unknown keys in a *request
  body* are the opposite — those are tolerated and merely warned about.)
- **Unknown provider *names* are only a warning.** See [Validation](#validation-what-fails-and-what-only-warns).

## The shape of a scenario

```yaml
version: 1                 # required; 1 is the current schema version
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
| `version` | integer | yes | Nothing. At least `1`, and no greater than the build's current schema version (`1` today). A version from the future is a load error. |
| `name` | string | yes | The default `seed`, and the `name` field of `GET /__admin/scenario`. |
| `description` | string | no | Nothing. Documentation for whoever reads the file next. |
| `seed` | string | no | The stable key every derived identifier hangs off. Defaults to `name`, so two scenarios never collide on request IDs. |
| `time.base` | RFC 3339 timestamp | no | Every timestamp a response carries. Defaults to `2026-01-01T00:00:00Z`. Nothing is ever read from a real clock. |
| `sources` | list of [Source](#sources) | no | The canonical corpus. |
| `providers` | map of [provider blocks](#providers) | no | The per-provider projections. |

`version` and `name` are the only required keys. A file containing just those two is valid and every provider then
renders a well-shaped empty success — which is what makes `provider.Deps{}` a usable zero-configuration handler.

## `sources`

One entry is one canonical document. Provider projections reference it by `id`; they never restate its URL or
title, which is how two providers end up returning the *same* source.

| Key | Type | Required | Renders to |
|---|---|---|---|
| `id` | string | yes | Nothing on the wire. The scenario-local handle projections point at. |
| `url` | string | yes | Exa `results[].url`, Tavily `results[].url`, Perplexity `search_results[].url`. Also the default Exa `results[].id`. |
| `title` | string | yes | Each provider's result title field. |
| `author` | string | no | Exa `results[].author`. |
| `author_null` | boolean | no | Forces an explicit JSON `null` for `author`. Exa types it `anyOf[string, null]`; a consumer that only ever sees a value has not exercised the null branch. |
| `published_at` | RFC 3339 timestamp | no | Exa `publishedDate` (millisecond precision), Perplexity `search_results[].date`. |
| `text` | string | no | The full document text: Exa `results[].text`, Tavily `results[].content` and `raw_content` fallbacks. |
| `snippets` | list of string | no | Short excerpts. The first one is the default Perplexity `search_results[].snippet` and the default Tavily `results[].content`. |
| `claims` | list of [Claim](#sourcesclaims) | no | Nothing directly. See below. |
| `image` | string | no | Exa `results[].image`. |
| `favicon` | string | no | Exa `results[].favicon`, Tavily `results[].favicon`. |

A duplicate `id` is a load error (`scenario.source.id.duplicate`), as is a missing `id`, `url` or `title`.

Source and fixture URLs should use a reserved domain — `.test`, `.example`, `.invalid` or `example.com`. A host
outside that set is a **warning** (`scenario.source.url.not_reserved`), not a load failure, and
`scripts/lint-no-live-hosts.sh` is what makes it a hard gate inside this repository. Nothing ever dials a scenario
URL; the rule exists because a base URL quietly resolving to a real paid API is the exact failure this simulator
prevents.

### `sources[].claims`

| Key | Type | Required | Renders to |
|---|---|---|---|
| `id` | string | yes | Nothing on the wire. |
| `text` | string | no | Nothing on the wire. |

Claims never reach a response body. They exist so a scenario can express **corroboration**: two sources declaring
the same claim `id` is the signal a fusion test asserts on. Repeating an `id` across sources is intended, not a
mistake.

## `providers`

`providers` is an **open registry** keyed by provider name, not a fixed set of fields. Adding a provider to
Servicesim never changes this schema, and a scenario written today keeps working when new providers arrive.

The names this build has handlers for are `exa`, `tavily`, `perplexity` (Sonar), `perplexity_agent` (the Agent
API), `exa_agent_runs` (Exa's create-then-poll Agent surface), `tavily_research` (Tavily's create-then-poll
Research surface) and `mcp` (a Model Context Protocol Streamable HTTP server, modern era only). Each async entry
is independent of its sync sibling deliberately: a scenario can rate-limit one and leave the other healthy, which
is how a consumer's migration fallback gets tested. See
[The async surfaces](#the-async-surfaces-exa_agent_runs-and-tavily_research) and [`mcp`](#mcp).

### Reserved envelope keys

Inside a provider block, **eight** keys are reserved. Everything else in the block is that provider's projection
body.

| Key | Type | Effect |
|---|---|---|
| `kind` | string | Selects the handler implementation. Defaults to the block's own name. Set it to declare two instances of one provider — an `openai` and an `openai_fallback` — for failover tests. An empty string is a load error. |
| `auth` | [AuthPolicy](#auth) | Credential policy for this provider. |
| `validation` | [ValidationPolicy](#validation) | Remaps finding severities for this provider. |
| `fault` | [Fault](#fault) | The deterministic failure plan. Illegal alongside `turns:` — see [Faults and turns](#faults-and-turns). |
| `turns` | list of [Turn](#the-multi-turn-form) | A conversation script. Mutually exclusive with a projection body at block level. |
| `turn_key` | list of string | What the turn cursor is keyed on. Defaults to `["route"]`. See [`turn_key`](#turn_key--what-the-cursor-counts-per). |
| `create` | `{fault}` | The create route's own attempt budget, on a create-then-poll async entry (`exa_agent_runs`, `tavily_research`). See [The async surfaces](#the-async-surfaces-exa_agent_runs-and-tavily_research). |
| `cancel` | `{fault, turns}` | What cancelling a create-then-poll job answers, and what the job's polls serve after a recorded cancel. Loads only on an entry whose profile serves a cancel for it — `exa_agent_runs` in tree; every other entry rejects it. See [Cancelling a job](#cancelling-a-job). |

`extra_fields` is **not** in that list, even though it reads like envelope machinery. Every provider projection
declares its own `extra_fields`, so the key is left in the body and behaves identically in a single-shot block and
in a turn's `respond:` — which is what makes the two forms literally the same thing rather than two forms with a
silent difference.

The authoritative list is the `switch key.Value` in `decodeProviderEntry` (`scenario/model.go`): a key with a `case`
arm is envelope, and everything reaching `default:` is projection body. The `reservedEnvelopeKeys` slice beside it
names the same keys for a test to assert against, but **the loader does not read it** — adding a key there does not
make that key envelope. Both have to change together, and the switch is the one that decides.

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

The consequences are worth stating, because they are visible in error messages:

- The `respond` body of a turn is precisely what a single-shot block's projection body would have been — the same
  keys, documented in [Provider projection bodies](#provider-projection-bodies).
- Findings against a projection body are addressed by their normalised path, so a single-shot block's bad source
  reference is reported at `providers.exa.turns[0].respond.results[0].source` even though the file contains no
  `turns:`. The `turns[0]` is the normalisation, not a typo in the message.
- A `respond:` that is absent or explicitly null is an empty projection body, not an error.

### `when` — the turn predicate

Every field is optional and every *present* field must match. It is an AND, never an OR; an empty `when` matches
everything.

| Key | Type | Matches when |
|---|---|---|
| `route` | string | The route serving the request is this one. See [`route`](#route--scripting-one-providers-several-routes) below. A name the provider does not serve is a load error. |
| `call_index` | integer | The zero-based count of prior requests **in this turn lane** equals it — see [`turn_key`](#turn_key--what-the-cursor-counts-per), whose default of `["route"]` makes the lane the route. A negative value is a load error. Inside a `cancel.turns` script it counts polls **since the cancel** instead — see [Cancelling a job](#cancelling-a-job). |
| `body_contains` | string | The raw request body contains this substring. Deliberately crude — it covers "which tool result came back" without becoming an expression language. |
| `body_json` | map of string to string | Every dotted path matches, for example `{model: sonar, "messages.0.role": system}`. A numeric segment indexes an array. Values compare as strings after JSON scalar formatting. An empty key is a load error. |

Matching considers **only the request and the call counter**. It never consults wall-clock time, because a
predicate that depends on the clock is a flaky test waiting to happen.

**Prefer `call_index` over `body_contains`.** A fixture keyed on prompt text breaks the next time somebody rewords
a prompt, and the failure looks exactly like a model regression — so the person debugging it starts in the wrong
place and stays there for a while. `body_contains` exists because it is what most hand-rolled mocks already do and
migrating them needs it; a scenario that can be written with `call_index` should be. `body_json` sits in between:
it is structural rather than textual, so `body_json: {model: sonar}` survives a reworded prompt in a way
`body_contains: "summarise the report"` does not.

### `route` — scripting one provider's several routes

A provider that serves more than one route needs turns addressed to a particular one. `call_index` alone cannot do
it: it says *which call*, never *which route's* call. Nor can the body matchers, because a `GET` poll carries no
body at all.

```yaml
providers:
  exa:
    turns:
      - when: {route: answer}                  # POST /answer, however many times
        respond: {answer: a direct answer}
      - when: {route: search, call_index: 0}   # the first POST /search
        respond: {results: [{source: source-a}]}
      - when: {route: search, call_index: 1}   # the second, independently
        respond: {results: []}
      - respond: {results: []}                 # the fallback
```

The default `turn_key` is `["route"]`, so **the cursor is already per route**. `{route: search, call_index: 1}`
therefore means *the second call to the search route*, whatever the answer route did — which is the whole point.
`call_index` on its own could not say that.

**Aliases collapse.** Route names group the spellings of one operation, so Perplexity's `/v1/sonar`,
`/chat/completions` and `/v1/chat/completions` are all `route: completions`. A scenario written against one spelling
serves a client that uses another, and a retry through an alias draws on the budget the scenario scripted. This is
the same grouping the fault engine counts attempts on.

Two spellings are accepted:

| Written | Matches |
|---|---|
| `route: search` | The bare name — any route whose key ends in `search`. This is what you normally write. |
| `route: "exa:search"` | The fully qualified key, matched exactly. |

The qualified form is **never** reduced to its bare name first. `route: "exa:search"` pasted into a `tavily:` block
matches nothing and fails at load, rather than quietly matching `tavily:search`. Being explicit must not widen a
match.

**A name that matches no route the provider serves is an error at load**, not a turn that silently never fires. The
error names the routes that provider does serve, because that vocabulary lives in Servicesim's Go source and is not
visible from your scenario file:

```text
providers.exa.turns[2].when.route: route "serch" matches no route served by
provider kind "exa"; it serves search, answer
```

### Turn selection rules

1. Turns are evaluated in declaration order; the first match wins.
2. When nothing matches, the last turn with no `when` is used.
3. When nothing matches and there is no unconditional turn, the request records a `scenario.no_matching_turn`
   finding and receives a provider-shaped not-found error. It is never silently a 200.
4. An unconditional turn anywhere but last is a **load error** (`scenario.turn.unreachable`), naming the turns it
   would shadow. A silently unreachable turn is the kind of scenario bug that survives review.

`call_index` is drawn from the *same* per-lane counter the fault engine uses, so a scenario that rate-limits call
two and answers differently on call three stays coherent. There are not two counters that can disagree.

One request claims exactly one call index, whatever happens to it afterwards. A request answered by a fault attempt
has still consumed its index, so `call_index: 0` and `attempts[0]` describe the *same* request, not consecutive
ones.

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

A `header:<name>` or `body_json:<path>` extractor whose name looks like a credential — `header:authorization`,
`header:x-api-key`, `body_json:api_key`, a nested `body_json:credentials.primary` or an indexed
`body_json:api_keys.0` — is a legitimate way to key a lane on "which credential was presented", the shape a
credential-rotation scenario needs. It contributes its **fingerprint**, never its value: the request that
presented `key-a` and the request that presented `key-b` still land in two different lanes, deterministically,
but the credential itself never reaches `outcome.fault_key`, the journal, `GET /__admin/requests` or the process
log. The same substitution happens, independently of the extractor's name, whenever the resolved value is itself
shaped like a credential — a vendor key, a `Bearer …` token, an embedded `name=value` pair — so a field with no
credential-sounding name still cannot carry one into the lane key.

For `header:authorization` and `header:x-api-key`, the fingerprint is the exact one `auth.fingerprint` reports
elsewhere in the journal for the same request — the two are computed from the same credential and can be compared
directly. Every other credential-named or credential-shaped extractor fingerprints its own resolved value the same
way, which is a *different* input than `auth.fingerprint` (it covers headers `auth.fingerprint` does not observe,
and body properties, which have no `auth.fingerprint` counterpart at all), so only the two Authorization-family
headers are byte-comparable to it.

**The route is always part of the lane.** `turn_key` adds discriminators to the route; it cannot replace it, and
omitting `route` from the list does not remove it. Writing `route` explicitly is equally legal and is de-duplicated
rather than doubled, so `["route", "body_json:model"]` and `["body_json:model"]` name the same lane. This is not
tidiness: the fault engine registers one plan per route key and recovers it out of the lane key, so a routeless
lane would resolve no plan at all — every declared fault would stop firing and `call_index` would never advance.

The composed key is visible in the journal as `outcome.fault_key`, which is the fastest way to check that a request
landed in the lane you meant:

```json
{"outcome":{"kind":"scenario","fault_key":"demo/perplexity:completions|body_json:model=sonar","attempt_index":0}}
```

**An unresolvable extractor warns; it never silently merges lanes.** A `body_json:` path the body does not carry, a
path landing on an object or an array rather than a scalar, a JSON `null`, an absent header, or a value longer than
128 bytes each contribute nothing and record a `scenario.turn_key_unresolved` warning against that request in the
journal. The lane is then named by the route and by the extractors that *did* resolve. The warning is mandatory
rather than a convenience: silently sharing a lane is the exact failure `turn_key` exists to prevent, so it has to
be visible where a consumer already looks.

Two further properties are worth knowing before you write a fixture against this:

- The lane is resolved **once per request, by the listener**, before a handler has chosen which provider entry
  answers. A listener that serves two entries — Perplexity's Sonar and Agent surfaces do — therefore keys both on
  the primary entry's `turn_key`, and declaring a second one on the other entry describes a resolution that never
  happens.
- An unrecognised extractor form is a **load error**, `scenario.turn_key.invalid`, naming the offending index. A
  typo here would otherwise present as one silently shared lane.

Namespaces compose with this and need no declaration: a request arriving with a `/n/<namespace>` base-URL prefix
gets the namespace as the outermost component of its lane key, so two tests running the same scenario through one
container advance separate cursors. See the [README](../README.md#one-container-many-concurrent-tests). The
built-in [`namespaced`](../scenarios/protocol/namespaced.yaml) scenario is the worked example.

**Why this exists.** One LLM-shaped route serving N concurrent callers has, under a route-keyed cursor, exactly one
sequence: two callers draw call indices 0 and 1 out of it and each receives the turn scripted for the other. The
response looks coherent, so the test fails somewhere else entirely and much later. Three sibling repositories hit
this independently and each re-keyed its cursor — per role, per `(scenario, role)`, and per model.

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
error, `scenario.source.unknown`, naming the normalised YAML path — for example
`providers.exa.turns[0].respond.results[2].source: unknown source "source-z"`.

## Shared policy blocks

### `auth`

| Key | Type | Effect |
|---|---|---|
| `mode` | `required` \| `optional` \| `reject` | `required` is the default — except for `mcp`, whose default is `optional` (the specification leaves authentication to the deployment; see [`mcp`](#mcp)). `reject` always answers 401, which is what the `unauthorized` built-in uses. Any other value is a load error. |
| `expect_key` | string | The presented credential must match exactly. Use a fake value; only a fingerprint is ever retained. |
| `headers` | list of string | Overrides the accepted credential placements outright, for example `[authorization]` to reject an `x-api-key` that Exa would otherwise allow. Despite the name it takes placements, not only header names — see below. |

#### Credential placements

A placement is where a credential arrives. Most are header names; one is not:

| Placement | Meaning |
|---|---|
| `authorization` | The `Authorization` header, with or without a `Bearer` prefix. The vendors' own docs disagree about the scheme, so both are accepted. |
| `x-api-key` | The `x-api-key` header. |
| `body:api_key` | An `api_key` property in the JSON request body. Not a header — which is why the vocabulary exists at all. |

Defaults are **per route**, because the real vendors vary placement per route:

| Provider | Route | Accepts by default |
|---|---|---|
| Exa | `POST /search`, `POST /answer`, `POST /contents`, `POST /findSimilar` | `authorization`, `x-api-key` |
| Tavily | `POST /search`, `POST /research` | `authorization`, `body:api_key` — decision D2, a v0.1.1 owner decision on client-level evidence. |
| Tavily | `POST /extract` | `authorization` only — the vendor's `/extract` page documents Bearer only, and D2 is not extended to routes verified after it. See `contracts/tavily/README.md`'s "POST /extract" § "Auth". |
| Perplexity | all six routes | `authorization` |
| MCP | `POST /mcp` | `authorization` — **optional** by default, unlike every route above; a scenario opts into `required` explicitly. |

(The two Exa agent-run routes and Tavily's `GET /research/{request_id}` poll are omitted from this table as a
pre-existing gap — see their own route godoc for credentials.)

A route with a JSON body can take the key in that body; a `GET` has nowhere to put one and takes a header instead.
Servicesim models that per route rather than per provider, so a provider whose POST and GET differ is expressible.

`auth.headers` overrides all of it, for the whole provider entry:

```text
auth.headers  >  the route's default  >  nothing else
```

The override is deliberately blunt. It exists for negative tests — *prove my client no longer sends the key in the
body* — and an assertion like that is worthless if a route default quietly re-admits the placement you are trying to
rule out:

```yaml
providers:
  tavily:
    auth:
      headers: [authorization]   # a body-placed api_key now fails auth.missing
```

### `validation`

| Key | Type | Effect |
|---|---|---|
| `strict` | boolean | Promotes every warning raised while serving a request to an error. A finding raised when the scenario loads is not promoted today ([#18](https://github.com/C360Studio/servicesim/issues/18)). |
| `promote` | list of string | Finding codes to raise from warning to error when raised while serving a request, for example `[request.unknown_field]`. A code raised when the scenario loads is not promoted today ([#18](https://github.com/C360Studio/servicesim/issues/18)). |
| `demote` | list of string | Finding codes to lower from error to warning. |

### `extra_fields`

A free-form map merged into the rendered response body. Servicesim never validates its contents — the point is to
emit fields the schema does not know about, so a consumer can prove its decoder tolerates additive vendor changes.

At provider-projection level it merges into the top-level response object. Several result types accept their own
nested `extra_fields`, which merge into that result entry instead. Where a type also accepts `omit_fields`, the
merge happens first and the omission second, so `omit_fields` can remove a key `extra_fields` added.

## `fault`

A deterministic failure plan. Attempt *N* of a route receives `attempts[N]` after `repeat` expansion.

| Key | Type | Required | Effect |
|---|---|---|---|
| `attempts` | list of [FaultAttempt](#faultattempts) | yes | What each successive attempt receives. An empty list is a load error. |
| `after` | `success` \| `repeat_last` | no | What happens once the list is exhausted. `success` (default) serves the scenario response; `repeat_last` makes the failure permanent. |

### `fault.attempts[]`

| Key | Type | Effect |
|---|---|---|
| `kind` | [fault kind](#fault-kinds) | Inferred when omitted; see below. |
| `status` | integer | The HTTP status for this attempt. Must be 100–599. |
| `delay` | duration string | For example `250ms`. Orthogonal — it composes with every kind. |
| `delay_after_headers` | duration string | Pauses AFTER the status line and headers are written and flushed, before the body (or, for `truncate_body`, before the partial write and reset) — "headers arrive, then a hang, then the rest". Composes with `delay` (pre-dispatch hang, then headers, then this hang) and with every non-streaming kind except `close_before_headers`. See the paragraph below the fault-kind table for what it does and does not apply to. |
| `retry_after` | integer | Seconds; sets the `Retry-After` header. |
| `headers` | map of string to string | Additional response headers. |
| `body` | map | The verbatim error body. When absent, the provider synthesises its own documented shape for `status`. |
| `error` | string | Fills the provider's error envelope without spelling out the whole body. |
| `tag` | string | Exa only: the `tag` field of its error envelope. |
| `raw_body` | string | Overrides the response bytes entirely. This is how `invalid_json` is expressed. |
| `content_type` | string | Overrides the `Content-Type` header, for `wrong_content_type`. |
| `truncate_after_bytes` | integer | How many body bytes reach the client before the connection dies, for `truncate_body`. Zero means half the body. For `stream_truncate_chunk` the same key instead bounds one chunk: zero means half *that chunk's* own bytes, not half the body — see [Streaming fault kinds](#streaming-fault-kinds). |
| `reset` | boolean | Sends a TCP RST instead of a clean FIN, so the client sees "connection reset by peer" rather than "unexpected EOF". |
| `body_bytes` | integer | For `oversized_body`: the response body is padded with insignificant JSON whitespace to at least this many bytes. There is no default size — unlike `truncate_after_bytes`, a zero or absent value under `kind: oversized_body` is a load error, not a fallback. If the unpadded body is already this size or larger, nothing is appended. Setting it under any other explicit `kind` is also a load error. The padded response declares an exact `Content-Length`. |
| `after_chunk` | integer | `stream_disconnect` \| `stream_truncate_chunk` \| `stream_stall` only: the zero-based index of the first chunk this attempt affects. See [Streaming fault kinds](#streaming-fault-kinds). |
| `extra_fields` | map | Additive properties merged into this attempt's body. |
| `accepted` | boolean | The request took effect before the failure this attempt scripts, so the async job it created is kept even though the response that carries its identifier does not arrive intact — "accepted, reply lost". It keeps the job, not the secret: which shapes also withhold the identifier is below. Meaningful only where the attempt does not deliver its body; an error where it does. See [An accepted create whose reply is lost](#an-accepted-create-whose-reply-is-lost). |
| `repeat` | integer | Applies this attempt to N consecutive attempts. "Fail the first three, then succeed" is one attempt with `repeat: 3` and the default `after`. |

An omitted `kind` is inferred, in this order: `raw_body` set means `invalid_json`; `content_type` set means
`wrong_content_type`; `truncate_after_bytes` above zero or `reset: true` means `truncate_body`; `body_bytes` above
zero means `oversized_body`; a `status` of 400 or above means `status`; everything else means no fault.

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
| `oversized_body` | The scenario response (or the provider's error shape, if `status:` is also set), padded with insignificant JSON whitespace to at least `body_bytes`. The decoded value is unchanged; only the size on the wire differs. |
| `stream_disconnect` | Streaming only (see [Streaming](#streaming-stream)) — a clean mid-stream disconnect after a scripted chunk. |
| `stream_truncate_chunk` | Streaming only — a malformed partial chunk, then disconnect. |
| `stream_stall` | Streaming only — a mid-stream pause, then the stream continues. |

Delays are real by default, in-process and in the container alike, so a scenario behaves identically either way.
A Go test can opt out with `testkit.WithSkippedDelays()` — but not for a timeout or cancellation test, which is
observed by bytes *not arriving* and therefore needs a genuinely short real delay.

#### `delay_after_headers`

`delay:` is a pre-dispatch hang — nothing reaches the client until it ends. `delay_after_headers:` is the opposite
half: the status line and headers are written and flushed first, so the client sees the response has started, and
only THEN does the attempt hang, before the body (or, for `truncate_body`, before the partial write and reset).
That is the shape a mid-flight cancellation actually has on the wire — a registry disabling a provider, a load
balancer dropping a backend — which `delay:` alone cannot express, because by the time `delay:` releases, nothing
has reached the client yet to call "mid-flight". Both compose on one attempt: hang, then headers, then hang again.

It applies to every non-streaming kind except `close_before_headers`, which never writes headers for there to be a
hang after (`scenario.fault.delay_after_headers.no_headers`, a load error). On `empty_body`, whose
`Content-Length: 0` means the client considers that one response complete at the headers, the hang is invisible on
it — but not otherwise: it still delays the journal entry for that request and stalls the next request on the same
keep-alive connection. The scenario is not wrong, so this is only a warning
(`scenario.fault.delay_after_headers.unobservable`), not an error. It cannot
apply to a `stream_*` kind (`scenario.fault.delay_after_headers.streaming`), nor, on an entry whose effective
policy is `stream`, to a kind that would actually stream (`scenario.fault.stream_mismatch` — see
[Validation](#validation) below); a kind that turns the response into an ordinary JSON body first (`status`,
`invalid_json`, `wrong_content_type`, `empty_body`, `extra_fields`) stays valid there — see
[Streaming](#streaming-stream). `stream_stall` with `after_chunk: 0` is the streaming-aware equivalent
("headers, then a hang, then the first event").

The journal reports the REQUESTED duration as `outcome.delay_after_headers_ms`, the sibling of `outcome.delay_ms`:
under `testkit.WithSkippedDelays()` no time passes, and this is still the asserted value. If the client's own
deadline or cancellation ends the request during this hang, nothing more is written — `outcome.aborted: true`,
`outcome.bytes_written: 0` — and `outcome.completed_at` is stamped at the instant the server observed the
cancellation, exactly as a client cancellation during `delay:`'s pre-dispatch hang already behaves. That entry
lands only after your client has already returned, so read it with `testkit.Sim.AwaitRequests`, not a synchronous
`Requests()`; for `truncate_body`, a `bytes_written` of zero means your own deadline won the race, and a nonzero
value means the scripted reset arrived first. An attempt carrying only this modifier (no other kind) is journaled
as `outcome.fault_kind: "delay"`, the same label a bare `delay:` attempt gets.

### Faults and turns

A fault plan is registered **per route**, not per turn, and the fault engine is handed one plan for each route at
startup. Three rules follow from that, and the first two are enforced:

1. A block-level `fault:` alongside `turns:` is a load error (`scenario.provider.fault_with_turns`). It would be
   ambiguous about which turn it belonged to.
2. A projection body alongside `turns:` is a load error too (`scenario.provider.body_with_turns`), naming the stray
   keys and telling you to move them inside a turn's `respond:`.
3. A `fault:` written **on a turn** is legal, and it is *not* scoped to that turn. The first turn declaring a
   non-empty `attempts:` list supplies the plan for the whole route, and that plan starts consuming from the
   route's very first request — including requests that other turns answer.

Rule 3 is the one that surprises people. In the script below, the 429 is served to the **first** request on the
route, which is the request `call_index: 0` describes; the turn the fault was written under has nothing to do with
when it fires:

```yaml
providers:
  perplexity_agent:
    turns:
      - when:
          call_index: 0
        respond:
          answer: Searching.
      - when:
          body_contains: report-a
        fault:                       # applies to the ROUTE, from its first call
          attempts:
            - status: 429
            - status: 200
        respond:
          answer: Report A states the finding.
```

Per-turn and per-lane fault plans are deferred work, not something a scenario can express today. If you want "fail
the retry, not the first call", say so with `attempts:` — a leading `- status: 200` is an unfaulted attempt, so
`[{status: 200}, {status: 429}, {status: 200}]` faults only the second call.

### What claims a call index, and what does not

`call_index` counts the requests a lane actually *served*. A request Servicesim refused before reaching your script
does not claim an index, does not advance the cursor, and does not consume an attempt from a fault plan. It is
journaled with `"attempt_index": -1`, which is how you tell the two apart:

| Outcome | Claims a call index |
|---|---|
| A scripted response, faulted or not | yes |
| A request rejected for a validation error | **no** — `attempt_index: -1` |
| A request rejected for a missing or wrong credential | **no** — `attempt_index: -1` |
| `exa_agent_runs` / `tavily_research`: a create | yes — the create route's own lane, independent of any job |
| `exa_agent_runs` / `tavily_research`: a poll that resolves a real job | yes — the job's own per-job lane, not the route's |
| `exa_agent_runs` / `tavily_research`: `HEAD` on either route | **no** — it answers existence only and never reaches turn selection |
| `exa_agent_runs` / `tavily_research`: a poll of an identifier this process never minted | **no** — `provider.ResolveJob` claims nothing before rendering the vendor's 404 |
| `exa_agent_runs` / `tavily_research`: a poll of a job the **other** entry minted | **no** — a job resolves only through the entry that minted it, so this is the same miss as an unknown identifier |
| `exa_agent_runs` / `tavily_research`: a poll whose identifier fails `provider.ValidJobID` | **no** — treated identically to an unknown identifier above; a malformed identifier is never this process's own |
| A `stream_*` fault attempt, claimed by a request that did not itself ask to stream | yes — claimed at turn selection, before the handler has looked at `stream` on the wire; see [Streaming](#streaming-stream) |

This is deliberate, and it is the reason a scenario stays readable. If a rejection consumed an index, then adding
one malformed request to a test — or fixing an adapter so it stops sending one — would silently renumber every
`call_index` after it, and the fixture would fail somewhere unrelated to the change.

A poll whose identifier fails `ValidJobID` can raise **two** warnings for one request, and neither claims an
index: lane resolution warns `job.id_invalid` first (naming the path wildcard, not `turn_key`, because
`Route.LaneFrom` is declared in Go, not YAML), because the identifier cannot become part of the per-job lane; the
handler then treats the same identifier as unresolved and answers the vendor's 404 the way any unknown identifier
does.

**A dynamically-enforced 429 will follow the same rule.** Servicesim does not enforce rate limits today; `429` is
something a scenario scripts through `fault.attempts`, and that is a served response which does claim its index. If
an enforcing mode is ever added, a 429 it originates will *not* claim a call index, on the same terms as an auth
rejection above.

That commitment is written down here rather than settled later because settling it later is not free: consumers are
authoring fixtures keyed on `call_index` now, and a rate limiter that claimed indices would renumber every one of
them on the day it shipped — in every adopting repository at once, with no error message pointing at the cause.

## Provider projection bodies

These are the keys allowed in a single-shot provider block (after the reserved envelope keys) and, identically, in
a turn's `respond` mapping. Every field is optional; a projection body may be empty, and the provider then renders
a well-shaped empty success.

### `exa`

Served on `POST /search`, `POST /answer`, `POST /contents` and `POST /findSimilar`.

| Key | Type | Renders to |
|---|---|---|
| `request_id` | string | `requestId`. Defaults to 32 lowercase hex characters derived from the seed. |
| `results` | list of ExaResult | `results[]`, truncated to the request's `numResults` (default 10) in declaration order. Also the lookup table `/contents` resolves requested `ids`/`urls` against — see "`/contents`: a fetch-shaped route (D-a)" below. |
| `cost_dollars` | `{total, neural}` | `costDollars`, which a real Exa response always carries and which is emitted even when the scenario declares none. `total` is required within the object. |
| `output` | `{content, grounding}` | The structured-output branch, emitted only when the request supplied `outputSchema`. Each `grounding` entry is `{field, citations, confidence}` where `confidence` is `low`, `medium` or `high`. |
| `resolved_search_type` | string | `resolvedSearchType`, emitted only when the scenario asks for it. Deprecated upstream. |
| `context` | string | `context`, emitted only when the scenario asks for it. Deprecated upstream. |
| `answer` | ExaAnswer | The `POST /answer` response: `{fault, answer, citations, cost_dollars, extra_fields}`. Absent means `/answer` returns an empty answer with no citations. Its `fault` is `/answer`'s own attempt budget, kept separate so an `/answer` call cannot consume `/search`'s retries. |
| `contents` | ExaContents | The `POST /contents` route's own knobs: `{fault, statuses, cost_dollars, extra_fields}`. It declares no `results` of its own — see below. |
| `find_similar` | ExaFindSimilar | The `POST /findSimilar` route: `{fault, results, cost_dollars, context, extra_fields}`. Unlike `contents`, it carries its own `results` — see below. |
| `stream` | `warn` \| `reject` | Behaviour for a request carrying `"stream": true`. `warn` (default) records a journal warning and serves the ordinary JSON body; `reject` returns a provider-shaped 400. Streaming itself is out of scope. |
| `extra_fields` | map | Merged into the top-level response object. |

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
| `omit_fields` | list of string | Drops named fields that would otherwise be present, for tests asserting a consumer fails on a missing required field. It is also the only way to reach the "publishedDate absent" state, since a source with no date renders an explicit null. |
| `extra_fields` | map | Merged into this result entry. |

#### `/contents`: a fetch-shaped route (D-a)

`POST /contents` is "give me these documents", not a relevance query, so its response is a **pure function of the
request and the scenario**, not a scripted `results` list. For each identifier the request names — every `ids[]`
element, then every `urls[]` element, in request order — the simulator resolves it two ways in order: first
against the *selected turn's own* `results` (above), keyed by a result's source URL or its declared `id`; then
against the corpus, by an exact `scenario.Source.URL` match. A resolved identifier renders one `ExaResult` and a
`statuses[]` entry `{id, status: "success"}` (no `source` key — both vendor examples omit it); an identifier that
resolves to nothing renders no result and `statuses[]` `{id, status: "error", error: {tag: CRAWL_NOT_FOUND,
httpStatusCode: 404}}` (`http_status_code` in a `contents.statuses` override — see the ExaContents table below).
Whether **any** requested identifier resolves at all is decided from this default resolution, *before*
`contents.statuses` overrides apply — a scenario that forces every requested identifier to `status: error` still
gets a 200 with `results: []`, not a 400, because each of them resolved first. If **no** requested identifier
resolves at all, the whole response is the documented 400 `NO_CONTENT_FOUND` instead of a 200 with an empty
`results[]`. A request element that is present but not a real URL (e.g. `"not a url"`) is not parsed as a URL —
it simply fails to resolve like any other unmatched identifier, and does **not** raise `INVALID_URLS`; that tag is
reserved for a non-string or empty array element (`exa.contents.item.invalid`), checked at request-validation
time, before resolution runs.

This is what makes `search -> contents` work on the built-in `happy` scenario with **no `contents:` block at
all**: the URLs `/search` returns are corpus URLs, so a `/contents` call naming one of them resolves through the
corpus fallback on its own.

ExaContents:

| Key | Type | Renders to |
|---|---|---|
| `fault` | fault plan | `/contents`' own attempt budget (`exa:contents`), independent of `/search`'s, `/answer`'s and `/findSimilar`'s. |
| `statuses` | list of `{id, status, source, error}` | Overrides D-a's computed status for one requested identifier, matched by the identifier exactly as the request sent it. `status: error` drops that identifier's result even if it had resolved. An override naming an identifier the request did not send raises `exa.contents.status_unrequested` (warning) and is not rendered. `error` is `{tag, http_status_code}`. |
| `cost_dollars` | `{total, neural}` | `costDollars`, same shape as `/search`'s. |
| `extra_fields` | map | Merged into the top-level `/contents` response object. |

#### `/findSimilar`: a relevance route (D-b), and deprecated upstream

Unlike `/contents`, `POST /findSimilar` **is** a relevance route: its results are exactly what `find_similar`
declares, rendered through the identical `ExaResult` shape and truncated to `numResults` the same way `/search`
is — a second projection over the same renderer, not a request-driven lookup. The vendor's own OpenAPI spec marks
the operation `deprecated: true` in favour of `/search`; Servicesim simulates it anyway, because a client written
before the deprecation is still production traffic and rejecting valid traffic is worse than serving a deprecated
route (see `contracts/exa/README.md`'s "POST /findSimilar" section).

ExaFindSimilar:

| Key | Type | Renders to |
|---|---|---|
| `fault` | fault plan | `/findSimilar`'s own attempt budget (`exa:find_similar`), independent of the other three routes'. |
| `results` | list of ExaResult | `results[]`, its own list — reusing `/search`'s `results` here would silently break the moment the two scenarios diverge. |
| `cost_dollars` | `{total, neural}` | `costDollars`. |
| `context` | string | `context`, emitted only when the scenario asks for it. Deprecated upstream, same as `exa`'s own top-level `context`. |
| `extra_fields` | map | Merged into the top-level `/findSimilar` response object. |

### `tavily`

Served on `POST /search` and `POST /extract`.

| Key | Type | Renders to |
|---|---|---|
| `request_id` | string | `request_id`. Defaults to a derived UUID, matching Tavily's documented example. |
| `answer` | string | `answer`, **gated on the request's `include_answer`**. The key is always emitted; when the request did not ask for an answer, or the scenario declares none, it renders an explicit `null`. |
| `images` | list of `{url, description}` | `images[]`, gated on `include_images`. Items are objects, never bare URL strings. |
| `results` | list of TavilyResult | `results[]`, truncated to the request's `max_results` (default 5) in declaration order. Also the lookup table `/extract` resolves requested `urls` against — see "`/extract`: a fetch-shaped route (D-a)" below. |
| `response_time` | number | `response_time`. A JSON **number**, not a string. |
| `auto_parameters` | map | `auto_parameters`. |
| `usage` | `{credits}` | `usage`, gated on the request's `include_usage` — for `/search`. `/extract` reads this same key but gates it differently; see "`/extract`: a fetch-shaped route" below. |
| `extract` | TavilyExtract | The `POST /extract` route's own knobs: `{fault, failed_results, extra_fields}`. It declares no `results` of its own — see below. |
| `extra_fields` | map | Merged into the top-level response object. |

TavilyResult:

| Key | Type | Renders to |
|---|---|---|
| `source` | source reference | The source this entry projects. |
| `id` | string | `results[].id`. Defaults to six hex characters derived from the source, then the entry's 1-based position. |
| `content` | string | `results[].content`. Defaults to the source's first snippet, then to its `text`. |
| `score` | number | `results[].score`. Derived from the seed and the source when absent, rounded to two decimals. |
| `raw_content` | string or `null` | `results[].raw_content`, **gated on the request's `include_raw_content`**. Null when the request did not ask for it; otherwise the declared value, then the source `text`. |
| `favicon` | string | `results[].favicon`, gated on `include_favicon`. |
| `images` | list of `{url, description}` | `results[].images`, gated on `include_images`. |
| `omit_fields` | list of string | Drops named fields from this entry. |
| `extra_fields` | map | Merged into this result entry. |

#### `/extract`: a fetch-shaped route (D-a)

The same mechanism as Exa's `/contents`, applied to Tavily's shape. `POST /extract`'s response is a pure function
of the request's `urls` (a single string or an array — both accepted) and the scenario: each requested URL is
resolved first against the selected turn's own `results` (above), keyed by a result's source URL or its declared
`id`, then against the corpus by exact URL match, in that order and in request order. A resolved URL renders one
extracted result; an unresolved URL renders no result and a `failed_results[]` entry `{url, error}`, where `error`
defaults to a fixed, simulator-chosen string (`contracts/tavily/README.md` documents `failed_results[].error` only
as free text, with no vendor enum). Unlike `/contents`, there is no all-failed 400 branch: a `/extract` call where
every URL fails still answers 200, with `results: []` and every URL in `failed_results[]`.

As with `search -> contents`, this is what makes `search -> extract` work on the built-in `happy` scenario with no
`extract:` block: `/search`'s results are corpus URLs, so `/extract` resolves them through the corpus fallback.

A resolved result's own fields render as follows. `raw_content` is the result's own `raw_content` override if
declared, else (when resolved through the turn's `results:`) the source's `text`, then that result's `content`,
then the source's first snippet — or (when resolved through the corpus fallback) the source's `text`, then its
first snippet. `images` is **always** an array of bare URL strings here, unlike `/search`'s own `images[]`, which
is an array of `{url, description}` objects — `contracts/tavily/README.md`'s `/extract` response table documents
`results[].images` as `array[string]`; gated on the request's `include_images` the same way `favicon` is gated on
`include_favicon`. `usage.credits` is emitted when the request sends `include_usage: true` **or** the resolved
turn's top-level `usage` key (the same one `/search` renders) is declared — either is sufficient (D-f), unlike
`/search`'s own request-only gating on that field.

TavilyExtract:

| Key | Type | Renders to |
|---|---|---|
| `fault` | fault plan | `/extract`'s own attempt budget (`tavily:extract`), independent of `/search`'s. |
| `failed_results` | list of `{url, error}` | Forces one requested URL to render as a failure, overriding D-a's ordinary resolution even for a URL that would otherwise have resolved. Matched by exact URL. An entry naming a URL the request did not send raises `tavily.extract.failure_unrequested` (warning) and is not rendered. `error` defaults to the fixed not-found text when omitted. |
| `extra_fields` | map | Merged into the top-level `/extract` response object, independent of `/search`'s top-level `extra_fields`. |

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
| `stream` | `{when_requested, deltas, terminal, pace}` | Scripts a Server-Sent Events response instead of the ordinary JSON body. See [Streaming](#streaming-stream). |
| `extra_fields` | map | Merged into the top-level response object. |

PerplexityResult:

| Key | Type | Renders to |
|---|---|---|
| `source` | source reference | The source this entry projects. |
| `snippet` | string | `search_results[].snippet`. Defaults to the source's first snippet. |
| `date` | string or `null` | `search_results[].date`. |
| `last_updated` | string or `null` | `search_results[].last_updated`. |
| `source_type` | `web` \| `attachment` | The wire field `search_results[].source`. It is **not** named `source` here: that key already belongs to the source reference, and a second field claiming it makes the YAML parser panic. |
| `omit_fields` | list of string | Drops named fields from this entry. |

PerplexityUsage: `prompt_tokens`, `completion_tokens`, `total_tokens` (integers), `search_context_size` (a
**string**, not a count), and the optional `citation_tokens`, `num_search_queries`, `reasoning_tokens`. Its `cost`
object — `input_tokens_cost`, `output_tokens_cost`, `total_cost`, plus optional `reasoning_tokens_cost`,
`request_cost`, `citation_tokens_cost` and `search_queries_cost` — is **required by the live schema**, so a
scenario that declares no `usage` at all still renders a zero-filled `usage` with a zero-filled `cost` rather than
omitting the keys.

### `perplexity_agent`

The Agent API, served on `POST /v1/agent` and `POST /v1/responses`. Its envelope shares no fields with Sonar's:
Sonar returns `choices[]`, the Agent API returns an ordered `output[]` execution trace. Ordering within `output[]`
is fixed — `search_results` first, then `message` — and a scenario cannot reorder it.

| Key | Type | Renders to |
|---|---|---|
| `response_id` | string | `id`. Defaults to a derived `resp_<32 hex>`. |
| `message_id` | string | The message output item's `id`. Defaults to a derived `msg_<32 hex>`. |
| `model` | string | `model`. Agent model IDs are `provider/model` strings such as `openai/gpt-5`. When omitted the response echoes what the request selected: the first non-empty entry of `models`, else `model`, else `preset/<name>` for a preset-only request, else `profile/<id>` for a profile-only one. A request must name `model`, `models`, `preset` or a valid `profile`, or it is a 400. |
| `created_at` | integer | `created_at`. Defaults to `time.base` as a Unix timestamp. |
| `status` | `completed` \| `failed` \| `incomplete` \| `in_progress` \| `queued` \| `cancelled` | `status`. Defaults to `completed`. `failed` requires `error`. |
| `answer` | string | The text of the single `message` output item. |
| `queries` | list of string | The searches the agent reports having run. Independent of `search_results`, so a scenario can project "searched but found nothing". |
| `search_results` | list of AgentResult | The `search_results` output item. Accepts the scalar shorthand, or a mapping with `snippet`, `date`, `last_updated` and `source_type`. `source_type` is `web` only on this surface (`attachment` is Sonar's and is a load error here). `results[].id` is the 1-based index as a JSON **integer**. |
| `annotations` | list of `{source, start_index, end_index}` | `url_citation` spans over the answer text. Indices are byte offsets into `answer`; an out-of-range span is a load error. An empty list emits `[]` rather than omitting the key. |
| `error` | `{message, code, type}` | `error`. `message` is required by the specification. |
| `usage` | `{input_tokens, output_tokens, total_tokens, cost}` | `usage`. Note the field names differ from Sonar's. `total_tokens` is derived when zero; `cost` is `{currency, input_cost, output_cost, total_cost, cache_creation_cost, cache_read_cost, tool_calls_cost}`, with `currency` defaulting to `USD` (the only value the specification allows; any other is a load error) and `total_cost` derived when zero. |
| `stream` | `{when_requested, deltas, terminal, pace}` | Scripts the `responses` SSE grammar instead of the ordinary JSON body. See [Streaming](#streaming-stream). |
| `extra_fields` | map | Merged into the top-level response object. |

### `mcp`

A Model Context Protocol Streamable HTTP server, modern era only (protocol revision `2026-07-28`), served on
`POST /mcp`. One projection serves every JSON-RPC method this profile dispatches — `server/discover`,
`tools/list` and `tools/call` — because a turn IS a server state: tool-catalogue drift between calls is turn N
vs turn N+1, not three projections that could disagree about what the server currently believes its own
catalogue is. `TestBuiltins_ProjectionKeysAreDocumented` cross-checks this table's keys against every built-in's
`mcp:` block.

| Key | Type | Renders to |
|---|---|---|
| `instructions` | string | `server/discover`'s `instructions`. Optional. |
| `ttl_ms` | integer | `ttlMs` on `server/discover` and `tools/list`. Defaults to `60000`. |
| `cache_scope` | `public` \| `private` | `cacheScope` on `server/discover` and `tools/list`. Defaults to `private`. |
| `tools` | list of ToolProjection | `tools/list`'s `tools[]`, in declaration order — the order this profile always answers in. |
| `results` | map of tool name to ResultProjection | `tools/call`'s scripted outcomes, keyed by tool name. A key naming no declared `tools` entry is legal (a hidden tool); a `tools` entry with no matching `results` key renders `isError: true` with one text block saying so and warns `mcp.tool.unscripted`, never silently; a name in neither is the `-32602` unknown-tool error (`mcp.tool.unknown`). |
| `stream` | `{when_requested, deltas, terminal, pace}` | Scripts an SSE answer to `tools/call` only — `server/discover` and `tools/list` never stream, regardless of policy. See [Streaming](#streaming-stream); the shared grammar applies, with MCP's own framing (below). |
| `extra_fields` | map | Merged into **every** result this projection renders — `server/discover`, `tools/list` and `tools/call` alike, unlike every other provider's `extra_fields`, which is per-route. Envelope-level only: neither `ToolProjection` nor `ContentBlock` has an `extra_fields` field of its own, unlike Exa/Tavily's per-result-entry `extra_fields` below. |

ToolProjection:

| Key | Type | Renders to |
|---|---|---|
| `name` | string | `name`. SHOULD match `^[A-Za-z0-9_.-]{1,128}$` and be unique; a non-matching name is a load WARNING, a duplicate is a load ERROR. |
| `title` | string | `title`. Optional. |
| `description` | string | `description`. Optional. |
| `input_schema` | JSON Schema object | `inputSchema`. Must be an object with `type: "object"` at the root (load ERROR otherwise); absent renders the schema's own no-parameter default, `{"type":"object"}`. Any `x-mcp-header` key anywhere in the tree is a load ERROR — this build does not honour it. |
| `output_schema` | JSON Schema object | `outputSchema`. Optional; never validated against a scripted result's `structured_content` (no JSON Schema validator in stdlib) — declaring both raises a load WARNING saying so, once per tool. |
| `annotations` | ToolAnnotations | `annotations`. Optional. |

ToolAnnotations: `title` (string), `read_only_hint`, `destructive_hint`, `idempotent_hint`, `open_world_hint`
(booleans, all optional — a fixture that leaves one unset emits nothing for it, never a substituted default).

ResultProjection:

| Key | Type | Renders to |
|---|---|---|
| `content` | list of ContentBlock | `content[]`, in order. May be empty. |
| `structured_content` | any JSON value | `structuredContent`. Optional; not validated against `output_schema` — see above. |
| `is_error` | boolean | `isError`. Defaults to `false` (omitted on the wire). |

ContentBlock, keyed by `type` (`text` \| `image` \| `audio` \| `resource_link` \| `resource`):

| Key | Type | Applies to | Renders to |
|---|---|---|---|
| `type` | string | every block | `type`. |
| `text` | string | `text` | `text`. Mutually exclusive with `source` below (a fixture setting both has `text` ignored). |
| `source` | source reference | `text` | Resolves to the referenced source's own text, falling back to its title when the source has no text — never the other way, since a hostile fixture's malicious-content markers live in `text`. |
| `data`, `mime_type` | string, string | `image`, `audio` | `data`, `mimeType`. |
| `uri`, `name`, `title`, `description`, `mime_type` | string ×5 | `resource_link` | `uri`, `name`, `title`, `description`, `mimeType`. `uri` and `name` are required by the schema. |
| `resource` | ResourceBlock | `resource` | `resource`. |

ResourceBlock: `uri` (required), `mime_type`, `text`, `blob` — exactly one of `text`/`blob` on a well-formed
fixture (`TextResourceContents` or `BlobResourceContents`); nothing downstream rejects a fixture that sets both.

**Simulator-chosen defaults**, recorded in full in `profiles/mcp/doc.go` and `contracts/mcp/README.md`'s
"Simulation decisions": credentials are OPTIONAL by default (the opposite default from every other provider — a
scenario needs an explicit `auth: {mode: required}` to reject a missing credential, `expect_key` alone is not
enough); every JSON-RPC method-level error (unknown tool, invalid params, an internal render failure) answers
`200`, never `400`, because a `400` body is the specification's own client era-detection signal; SSE framing is
unnamed `data:` frames only, with no `event:`, no `id:` and no `[DONE]` sentinel, carrying one
`notifications/progress` frame per delta only when the request itself carried `_meta.progressToken`, followed by
the final JSON-RPC response frame. `x-mcp-header`, `Origin` validation, resources, prompts, completion,
`subscriptions/listen`, MRTR and the legacy `2025-11-25` era are all NOT SIMULATED by this profile — see
`contracts/mcp/README.md`'s "Not simulated / out of scope" table.

### Streaming (`stream:`)

`perplexity` (Sonar — `POST /v1/sonar` and its aliases) and `perplexity_agent` (the Agent API) can each serve a
scripted Server-Sent Events sequence instead of the ordinary JSON body. Sonar renders the OpenAI-compatible
`chat_completions` dialect: unnamed `data:` frames closed by `data: [DONE]`. The Agent API renders the `responses`
dialect: every frame carries an `event: <type>` line and no `[DONE]` sentinel. Exa and Tavily do not stream —
`exa`'s own `stream` key (above) stays the plain `warn` \| `reject` policy it always was.

`mcp` is the third streaming surface, and its shape differs from the two above in every way `when_requested`
otherwise assumes: only `tools/call` can stream (`server/discover` and `tools/list` never do, regardless of
policy); the client has no request-side field that asks for a stream — `when_requested: stream` answers every
`tools/call` as SSE unconditionally, so `reject` is meaningless there and is a load ERROR
(`mcp.stream.reject_meaningless`); and its framing is its own (unnamed `data:` frames, no `[DONE]`, one
`notifications/progress` frame per delta sent only when the request's own `_meta.progressToken` was present —
see [`mcp`](#mcp) above for the full grammar).

`stream:` lives inside `respond:`, alongside the rest of the projection — `scenario` never decodes it, the
provider package does, through the same `Turn.DecodeProjection` every other key uses. A bare scalar still parses,
as the shorthand for `{when_requested: <scalar>}` (`stream: warn`, `stream: reject`); the mapping form adds the
script:

| Key | Type | Effect |
|---|---|---|
| `when_requested` | `warn` \| `reject` \| `stream` | What the surface does when a request sets `"stream": true`. Read from the entry's **first turn only** — see below. Empty means `stream` when the turn declares `deltas`, `warn` otherwise. |
| `pace` | duration string | The default gap before every chunk this turn writes: each delta, the terminal chunk (unless `terminal.pace` overrides it) and, on Sonar, the `[DONE]` sentinel. Zero (the default) writes the sequence as fast as the socket accepts it. |
| `deltas` | list of string, or `{text, pace}` | The incremental content fragments, in order. A bare string is shorthand for `{text: <string>}`. Concatenated, they should equal the turn's own `answer`. |
| `terminal` | `{omit_usage, omit_done, pace}` | Tunes the closing frame. `omit_usage` drops `usage` from it. `omit_done` drops the `[DONE]` sentinel on Sonar while the connection still closes cleanly; it is meaningless on the Agent surface, which never writes one, and raises `perplexity.stream.done_ignored` there instead of being silently accepted. `pace` overrides the default gap for this one frame. |

`usage` and the rest of the ordinary non-streaming projection are reused verbatim on the terminal chunk — one
declaration serves both transports, so a scenario cannot quote one spend figure when it streams and another when
it does not. The one exception is a `perplexity_agent` turn whose `status` is `failed`: it streams
`response.created` then a terminal `response.failed` carrying the turn's `error` at top level — the specification's
own failure event, which has no response object — so there is no `usage` on it, no `extra_fields`, and nothing
for `terminal.omit_usage` to drop. A `cancelled` turn streams `response.created` then `response.completed`.

**The policy is per ENTRY; the deltas are per TURN.** `when_requested` is read once, from turn 0, because
rejecting a request has to happen before turn selection claims a fault attempt — a policy that varied per turn
could never be honoured. `when_requested` written on any turn after the first is `scenario.stream.policy.ignored`
(warning), not silently dropped. `deltas` are the opposite: each turn is a different answer, so each scripts its
own.

| Entry's effective `when_requested` | Turn declares `deltas` | Outcome |
|---|---|---|
| `stream` | yes | Serves the scripted sequence. |
| `stream` | no | `scenario.stream.deltas_empty` (error) — that turn would serve an empty stream. |
| not `stream` | yes | `scenario.stream.deltas_ignored` (error) — the script is dead and would be served as JSON with no hint it was ever read. |
| not `stream` | no | Serves JSON, exactly as before this key existed. |

#### Streaming fault kinds

Three fault `kind` values apply only under an entry whose effective policy is `stream` — see [Fault
kinds](#fault-kinds) for where they sit alongside the rest of the catalogue. Each is keyed on `after_chunk`, the
zero-based index of the first chunk it affects:

| `kind` | What the client observes |
|---|---|
| `stream_disconnect` | Chunks `[0, after_chunk)` arrive whole; the connection dies before chunk `after_chunk` is written at all. The previous chunk (or the flushed headers, if `after_chunk: 0`) is the last thing observed — a clean frame boundary, not a malformed one. |
| `stream_truncate_chunk` | Chunks `[0, after_chunk)` arrive whole, then `truncate_after_bytes` bytes of chunk `after_chunk` (half that chunk's own bytes, if unset), then the connection dies mid-frame. |
| `stream_stall` | A `delay` pause is inserted before chunk `after_chunk`, then the stream continues normally. Nothing aborts; the client's own deadline decides what happens — the point for a Temporal activity timeout or a missed heartbeat. |

`reset: true` sends a TCP RST instead of a clean FIN for either aborting kind, the same knob `truncate_body`
already uses. `after_chunk` is meaningful only for these three kinds — a **nonzero** value on any other kind is
`scenario.fault.after_chunk.not_streaming` (zero is indistinguishable from "unset", the same convention this
schema already uses for `truncate_after_bytes`). Its valid range is `[0, chunk_count)`, checked against the
**smallest** chunk count across the entry's turns, because one fault plan is shared by every turn the route may
answer with (`scenario.fault.after_chunk.out_of_range`). `chunk_count` is the scripted `deltas` plus the one
terminal chunk on Sonar (`N + 1`); on the Agent surface it also counts the five envelope events around the
deltas — `response.created`, `response.output_item.added`, `response.output_text.done`,
`response.output_item.done` and `response.completed` (`N + 5`) — or 2 for a turn whose `status` is `failed` or
`cancelled`, which renders no message item for a fault plan to abort mid-way through. The upper end of that range
is a legitimate script, not a special case: an
`after_chunk` naming the terminal chunk itself means every delta arrived but the frame confirming completion
never did.

`truncate_body` and `oversized_body` are the two existing kinds that **cannot** apply to a streaming entry
(`scenario.fault.stream_mismatch`): each sets a `Content-Length` before writing the body — `truncate_body`
before writing a prefix, `oversized_body` before writing the padded whole — which is correct for JSON and wrong
for chunked SSE; a byte-offset cut, or a padded JSON body, both land wrong on a chunked stream. `truncate_body`'s
streaming-aware equivalent is `stream_truncate_chunk`, which counts frames rather than bytes; `oversized_body`
has no streaming-aware equivalent, because a real vendor does not answer `stream: true` with a padded JSON
document at all. The mirror direction holds too — a `stream_*` kind cannot apply to an entry whose effective
policy is not `stream`.

`delay_after_headers:` is rejected the same way, on any kind that would not otherwise turn a streaming entry's
response into an ordinary JSON body: it assumes a body to hang before writing, which a chunked SSE stream has no
such point in. A kind that DOES turn the response into JSON first (`status`, `invalid_json`, `wrong_content_type`,
`empty_body`, `extra_fields`) stays valid, because `delay_after_headers` then applies to that JSON body, exactly
as it would on a non-streaming entry. `stream_stall` with `after_chunk: 0` is the streaming-aware equivalent —
"headers, then a hang, then the first event" — and is already valid on its own terms.

The four ways an adopter scripts a stream (docs/design/streaming.md §2.1; the built-in
[`streaming`](../scenarios/protocol/streaming.yaml) scenario scripts all four, keyed by `call_index` on one
Sonar entry, plus a happy Agent-surface stream):

```yaml
# 1. Mid-stream disconnect, RST rather than FIN.
fault:
  attempts:
    - kind: stream_disconnect
      after_chunk: 3
      reset: true

# 2. Truncated chunk: chunks 0-1 complete, then 12 bytes of chunk 2, then the socket dies.
fault:
  attempts:
    - kind: stream_truncate_chunk
      after_chunk: 2
      truncate_after_bytes: 12

# 3. Transient blip then retry. Not a new mechanism — the existing attempt list expresses it.
fault:
  after: success
  attempts:
    - kind: stream_disconnect
      after_chunk: 2

# 4a. Slow chunk pacing is not a fault at all — it is the script.
respond:
  stream:
    pace: 12s          # every gap exceeds a Temporal heartbeat interval

# 4b. A stall that exceeds an activity timeout, mid-stream, without aborting.
fault:
  attempts:
    - kind: stream_stall
      after_chunk: 3
      delay: 65s
```

A retry (example 3) must land in the same fault lane: if `turn_key` varies by call, a retry that resolves to a
different lane draws attempt 0 again and is disconnected forever. `stream_stall` under
`testkit.WithSkippedDelays()` does not stall — a test asserting a real timeout needs the default, real delay
mode; the planned gap stays readable from the journal either way (below).

**A `stream_*` attempt still claims a call index on a request that never asked to stream.** The entry's policy
answers "does this surface serve a stream when asked", not "does this call stream" — a consumer legitimately
sends `stream: true` on one call and `stream: false` on the next, in the same lane, and the fault attempt is
claimed at turn selection, before the handler has looked at `stream` on the wire. When that happens, the claimed
attempt is reported through `scenario.stream.abort_unreachable` (a per-request finding, not load-time), never
silently served as a plain 200.

#### Validation

| Code | Severity | Condition |
|---|---|---|
| `scenario.stream.policy.unknown` | error | `when_requested` is not `warn`, `reject` or `stream`. |
| `scenario.stream.policy.ignored` | warning | `when_requested` declared on a turn after the first. |
| `scenario.stream.deltas_empty` | error | The entry streams and some turn declares no `deltas`. |
| `scenario.stream.deltas_ignored` | error | A turn declares `deltas` while the entry's effective policy is not `stream`. |
| `scenario.stream.answer_mismatch` | warning | Concatenated `deltas` do not equal the turn's own `answer`. |
| `scenario.fault.after_chunk.not_streaming` | error | `after_chunk` is nonzero on a kind that is not one of the three `stream_*` kinds. |
| `scenario.fault.delay_after_headers.streaming` | error | `delay_after_headers` set on a `stream_*` kind — regardless of the entry's policy. |
| `scenario.fault.stream_mismatch` | error | `truncate_body` or `oversized_body` on a streaming entry; a `stream_*` kind on one that is not; or `delay_after_headers` on a streaming entry, on a kind that would not otherwise turn the response into an ordinary JSON body. |
| `scenario.fault.after_chunk.out_of_range` | error | `after_chunk` is not in `[0, chunk_count)` for the smallest chunk count across the entry's turns. |
| `scenario.stream.abort_unreachable` | error, per request | A claimed attempt cannot apply to this specific exchange's actual transport — a `stream_*` kind on a call that will not stream; `truncate_body`/`oversized_body` on one that will; or `delay_after_headers`, on a kind that would not otherwise turn the response into JSON, on one that will. |
| `perplexity.stream.unimplemented` | warning | Sonar: `stream: true` under an entry whose effective policy is not `stream`. The request still receives the ordinary body. |
| `perplexity.stream.agent_unsupported` | warning under `warn`, error (400, `ErrorInfo`) under `reject` | The Agent surface's analogue of the code above — renamed from `perplexity.agent.stream.unsupported` so it sorts under the `perplexity.stream.` prefix like every other streaming code. |
| `perplexity.stream_mode.concise.unscripted` | warning | A request sets `stream_mode: concise` **and** will actually stream. Only `stream_mode: full` is rendered; the full-mode transcript is served instead of being rejected. |
| `perplexity.stream.done_ignored` | warning | `terminal.omit_done` declared on a `perplexity_agent` turn — the typed grammar never writes a `[DONE]` sentinel, so there is nothing to omit. |

`scenario.stream.policy.unknown` and `scenario.stream.policy.ignored` are checked once, in `scenario`, for both
surfaces that decode a `StreamScript` — the one implementation every provider that streams shares, rather than a
copy per provider. Exa's own `exa.stream.policy.unknown` is untouched: Exa does not stream, so its `stream` field
stays the older, scalar-only policy type.

#### The journal

`outcome.stream` is present, and non-null, only on a streamed exchange:

| Field | Meaning |
|---|---|
| `grammar` | `chat_completions` (Sonar) or `responses` (Agent). |
| `chunk_count` | How many indexed chunks the plan produced. `[DONE]`, when written, is never one of them. |
| `bytes_planned` | Total bytes the plan will write, `[DONE]` included. |
| `pace_ms` | The **planned** gap before each indexed chunk, in order — a schedule, not a measurement, so it reads identically under real and skipped delays and is final before the client sees a byte. A `stream_stall`'s extra delay is already folded into the index it stalls; `stall_before_ms` (below) is the same duration lifted back out on its own. |
| `event_names` | Each frame's `event:` value, in order. `nil` for Sonar, whose frames carry none — the way a reader tells the two grammars apart without parsing a byte. |
| `terminal_index` | The chunk carrying `usage` and cost. |
| `usage`, `cost_total` | The terminal chunk's usage object, and the same number lifted to a provider-neutral field — final before the client has seen anything. |
| `abort_after_chunk`, `truncated_at_byte`, `stall_before_ms` | The **scripted** fault, not what happened; `nil` when nothing was scripted, or when a claimed attempt could not apply to this exchange (`abort_unreachable`). |
| `state` | `open` until the exchange closes, then `completed`, `aborted` or `client_gone`. |
| `chunks_sent` | How many complete indexed chunks the client actually received. |

Every field through `stall_before_ms` above is written when the entry is appended, before the first byte reaches
the client, and is safe to read immediately — none of them reads a wall clock, `pace_ms` included, which is what
lets `testkit.AssertStreamPacing` compare against it under both real and skipped delay modes. `state` and
`chunks_sent` are filled in only once the exchange closes; `Sim.AwaitStreamClosed`/`Namespace.AwaitStreamClosed`
is the wait, mirroring `AwaitRequests`'s own `Sim`/`Namespace` pair. Chunk **bytes** are never journaled — the
client already holds every one, and golden-file regression over a transcript is taken client-side with
`testkit.AssertGoldenSSE`, which diffs parsed `(event, data)` frames rather than raw bytes so one changed delta
reports as a one-frame diff, not a whole-file one.

### The async surfaces: `exa_agent_runs` and `tavily_research`

Exa's Agent API and Tavily's Research API are **create-then-poll**: a `POST` mints a job and returns an identifier
immediately, and everything interesting — progress, the terminal payload, failure — lives on the `GET` that polls
it. Each is its own provider entry, following the `perplexity` / `perplexity_agent` precedent exactly: independent
`auth`, `validation`, `fault` and `turns`. A scenario that uses only the sync surfaces omits the entry and is
unaffected — except that an Exa agent-run create against it is refused, with the agent envelope's 404 and
`scenario.no_matching_turn`, because a run with no poll script could never be polled honestly. Builds before this
one accepted that create and served a pending snapshot to every poll.

**A turn of an async entry is one poll snapshot.** That is the whole schema addition — no new envelope key
describes a job — and every existing turn mechanism applies unchanged: `when`, `call_index`, the single-shot/
multi-turn equivalence, and the unconditional-last-turn fallback. Two pending turns followed by an unconditional
terminal one is a job that answers "still working" twice and then completes, and the terminal turn keeps answering
every poll after it — the same fallback rule that already means "keep returning the terminal snapshot forever". A
single-shot block — one unconditional turn — is the **zero-poll** case: the job is already terminal on its first
poll.

A repeated snapshot can be written once and reused with a YAML anchor and alias, exactly as anywhere else in the
file: `respond: &pending {status: running}` on one turn, `respond: *pending` on the next.

| Provider entry | Routes served |
|---|---|
| `exa_agent_runs` | `POST /agent/runs` (create), `GET /agent/runs/{id}` (poll), `HEAD /agent/runs/{id}` (existence only), `POST /agent/runs/{id}/cancel` (cancel — see [Cancelling a job](#cancelling-a-job)) |
| `tavily_research` | `POST /research` (create), `GET /research/{request_id}` (poll), `HEAD /research/{request_id}` (existence only) |

The create response is derived in full and cannot be scripted: a projection body alongside `turns:` is already a
load error, so there is nowhere honest to put create-side keys. `exa_agent_runs` creates a full `AgentRun` at `queued`
(every required key, the unscripted ones at their placeholders — see below); `tavily_research` creates at `pending`
(plus `request_id`, `created_at`, `input`, `model` and `response_time`).

`exa_agent_runs`:

| Key | Type | Renders to |
|---|---|---|
| `status` | `queued` \| `running` \| `completed` \| `failed` \| `cancelled` | `status`. Empty means `running`, so a pending poll can be written as `respond: {}`. `completed`, `failed` and `cancelled` are terminal. |
| `stop_reason` | `schema_satisfied` \| `budget_reached` \| `time_limit_reached` \| `stopped` \| `error` \| `cancelled` | `stopReason`. A non-terminal snapshot always renders `null`. A terminal one derives it from `status` — `failed` becomes `error`, `cancelled` stays `cancelled`, everything else becomes `schema_satisfied` — unless stated explicitly. |
| `output` | `{text, structured, grounding}` | `output`, on any snapshot that declares it. A snapshot that declares none renders the empty placeholder `{text: "", structured: null, grounding: []}` — the wire object is required on every run, queued and running included. `status: completed` with none is a load-time warning. `grounding[]` entries are `{field, citations, confidence}`, resolved against the corpus; each citation renders as `{url, title}` and nothing else, and `citations` renders as `[]` when empty. |
| `error` | — | **Removed.** The live `AgentRun` schema has no run-level error object, so declaring `error:` is the load error `exa.agent_run.error.not_in_schema`. A failed run is `status: failed`, which derives `stop_reason: error`. |
| `cost_dollars` | `{total, agent_compute, search, emails, phone_numbers, data_sources}` | `costDollars`, on every snapshot. All five scalars are required on the wire and `search` is a plain number here, not the `{neural}` object of `exa`'s own `costDollars`. A key the scenario omits renders `0`; a value must be a finite number `>= 0` (`exa.agent_run.value.range`). `data_sources` is a map of provider to dollars and is omitted when empty. |
| `usage` | `{agent_compute_units, searches, emails, phone_numbers, data_sources}` | `usage`, on every snapshot. The four counters are required on the wire and an omitted one renders `0`; a value must be a finite number `>= 0` (`exa.agent_run.value.range`); `searches`, `emails`, `phone_numbers` and the `data_sources` counts are integers, so a fractional value is a load error. `data_sources` is a map of provider to count and is omitted when empty. |
| `extra_fields` | map | Merged into the top-level response object. |

**What `exa_agent_runs` renders that no key scripts** (all simulator policy: the vendor's OpenAPI document gives the
shape of each key but has no example bodies, so none of this is a claim about what the live service sends):

- `object` is the constant `agent_run`, and the run id is `agent_run_` plus 32 hex characters.
- `createdAt` is `time.base`. `completedAt` is `null` until a snapshot is terminal and `time.base` after — there is
  no elapsed-time model, so a run never takes any time.
- `request` is `null`, which the schema permits. The job record holds no request body, so the request is not echoed.
- A zero that the scenario did not script is a **placeholder for a required key**, not a claim about billing or
  metering. The cost components are not derived from, and need not sum to, `total`. A scripted value always wins.
- `extra_fields` is the one way to put a key outside the schema on the wire, on purpose: it exists so a consumer can
  prove it tolerates additions, which the `AgentRun` schema itself (`additionalProperties: false`) does not.

`tavily_research`:

| Key | Type | Renders to |
|---|---|---|
| `status` | `pending` \| `in_progress` \| `completed` \| `failed` | `status`. Empty means `pending`. `completed` and `failed` are terminal — there is no `cancelled` on this surface, unlike Exa's. Also selects the poll's HTTP status: `202` while pending or in progress, `200` once terminal. |
| `content` | string or object | `content`, emitted only at `completed` — not at `failed`, even though both are terminal. Deliberately untyped: the vendor renders either a report string or a structured object, depending on whether the create supplied `output_schema`, and a scenario must be able to produce both. |
| `sources` | list of source reference | `sources[]`, emitted only at `completed` — not at `failed`. |
| `response_time` | number | `response_time`. Zero unless declared — nothing on a response path ever reads a real clock. |
| `extra_fields` | map | Merged into the top-level response object. |

Both polls also carry a `createdAt`/`created_at` string, sourced from `time.base` like the create body's — not a
projection key, so there is nothing to declare. Exa's `createdAt` appears on every poll; Tavily's `created_at`
appears only once the task reaches `completed` — a `failed` poll carries none, matching
contracts/tavily/README.md's poll-status table (`failed`: `request_id`, `status`, `response_time` only).

**`create` is the create route's own attempt budget.** It is nested rather than a second block-level `fault:`,
because a block-level `fault:` alongside `turns:` is already a load error and, in the multi-turn form, there is
genuinely no way to say which route a bare `fault:` meant. It reads a *different* scenario location from the
poll's plan — the first turn declaring a non-empty `attempts:` list, exactly as [Faults and turns](#faults-and-turns)
describes for every other multi-route entry — which is what makes the two budgets independent in substance, not
only in name:

```yaml
providers:
  exa_agent_runs:
    create:                    # the POST /agent/runs plan
      fault:
        attempts:
          - {status: 429, retry_after: 1}
          - {status: 200}     # a kind-none attempt still writes `status` to the wire;
                                # Exa's create answers 200, so naming it is a pass-through
                                # (Tavily's create answers 201, which this would downgrade)
    turns:                     # each turn is a poll; a turn's fault is the POLL plan
      - when: {call_index: 0}  # turn 0 must be conditional, or turn 1 is unreachable
        fault:
          attempts:
            - {status: 200}
            - {status: 503}    # every job's SECOND poll fails
            - {status: 200}
        respond: {status: running}
      - respond: {status: completed, output: {text: done}}
```

Because the poll route's lane is per job (below), that poll plan consumes **per job**: the `503` above is every
job's second poll, not whichever job happens to poll second globally.

`create.fault` is validated at load exactly like a turn's plan: an empty `attempts:` list, an unknown `kind`, a
negative `retry_after` and every other fault finding is a load error addressed at
`providers.<entry>.create.fault…`. Builds up to and including v0.5.0 did not validate it at all, so a malformed
create plan loaded silently and misbehaved at request time.

**A faulted create leaves a job only if the client can use its identifier.** The create claims its attempt either
way, so the plan advances as scripted, but the job record is written only when the response actually carries the
identifier. A `status` of 400 or above, a `body:` (at any status — it replaces the rendered body, it does not merge
into it), a `status` of 204, 304 or 101 (`net/http` writes no body under them), `empty_body`, `invalid_json`,
`truncate_body` and `close_before_headers` all replace or destroy it: no record is written, and the retry mints a new
identifier from the next call index — unless the attempt says `accepted: true`, which keeps the job anyway
([below](#an-accepted-create-whose-reply-is-lost)). A delay, `delay_after_headers`, `extra_fields`,
`wrong_content_type` and `oversized_body` leave the rendered body intact, so the identifier works and its job exists.
A `body:` on a create attempt therefore scripts what the client sees; it is not a way to hand a client an identifier
that polls.

The rule is decided when the create is claimed, before its body exists, so a few scripts that defeat themselves fall
outside it. `truncate_after_bytes` at or past the length of the create's response writes the whole body before the
connection is aborted: the client receives the identifier and, unless the attempt says `accepted: true`, no job was
recorded (with `accepted` the job exists and the client holds its id — see
[below](#an-accepted-create-whose-reply-is-lost)). A `headers:` override of
`Content-Length`, `Transfer-Encoding` or `Content-Encoding` can leave the client unable to read the body that was
written, and `extra_fields` can overwrite the `id` they are merged into: in both a job is recorded that the client
holds no usable identifier for.

A kind-none attempt that names a `status` pins the wire status to it, whatever the handler would have written.
That is invisible on a route that answers 200 anyway and wrong on the two that do not: a `tavily_research` create
answers `201`, and a `tavily_research` poll answers `202` until the task is terminal. Write the success attempt as
`- {}` — no status, no kind — wherever the route's real status is not 200 or varies with state, and name a status
only when pinning it is the point. `[{status: 429}, {}]` on a `tavily_research` poll plan is "rate-limit the first
poll, then serve whatever the snapshot says"; `[{status: 429}, {status: 200}]` would answer 200 to a poll that is
still pending.

**Error bodies on `exa_agent_runs`.** The three Exa agent routes answer every error — rejections, a missing run, an
injected fault — as `{error: {type, code, message}}`, the vendor's `AgentErrorResponse`, and carry an `x-request-id`
response header. Exa's other routes keep their flat `{requestId, error, tag}` body. A fault attempt's `status` picks
`type` and `code`: `400` is `INVALID_REQUEST` / `INVALID_REQUEST`, `401` is `AUTHENTICATION_ERROR` /
`TEAM_NOT_FOUND`, `404` is `NOT_FOUND` / `RUN_NOT_FOUND`, `429` is `RATE_LIMIT_ERROR` /
`CONCURRENCY_LIMIT_REACHED`, and every other `4xx` is `INVALID_REQUEST`, every `5xx` `SERVER_ERROR`. The vendor's
document lists which statuses each operation returns but never pairs them with a type and code, so that pairing is
this simulator's inference. The attempt's `error:` becomes `message` and its `tag:` becomes `code`, verbatim — a
code outside the vendor's enum is allowed, to test a consumer's handling of one the vendor adds later, and is warned
about at load (`exa.agent_run.fault_tag.unknown`) so a flat-envelope tag such as `RATE_LIMIT` copied from another Exa
route does not render silently — while `type` always follows the status. An attempt's own `body:` still wins
outright.

**Per-job lanes.** A poll route's lane is per job, not per route. `Route.LaneFrom` — `["path:id"]` for
`exa_agent_runs`, `["path:request_id"]` for `tavily_research` — adds the path wildcard's value as an extra
component, in the style of `turn_key`, declared in Go on the route rather than in the scenario, so no scenario file ever
writes it. That is what makes `call_index` on a poll mean *poll N of this job*, not *the Nth poll of any job on
this route*: two jobs polled concurrently in one namespace get two independent cursors and two independent fault
budgets, and everything [`turn_key`](#turn_key--what-the-cursor-counts-per) already lists comes free with it,
because the poll cursor *is* the fault attempt counter.

A `turn_key:` written on the async entry itself still applies on top of that job discriminator, and `header:<name>`
is the extractor to reach for if you need a second axis. `body_json:<path>` is not: a `GET` poll carries no body,
so the path can never resolve and **every poll** raises `scenario.turn_key_unresolved` — the request is still
served, just from a lane missing that discriminator. Leave `turn_key` unset unless you need one; the per-job lane
is automatic and needs no declaration.

**Validation.** Each async entry's `ValidateProjections` decodes every turn and reports these findings, in addition
to the generic ones every provider raises for a malformed `respond:` node or an unresolved source reference (see
[Validation](#validation-what-fails-and-what-only-warns)):

`exa_agent_runs`:

| Code | Severity | Condition |
|---|---|---|
| `exa.agent_run.status.unknown` | error | `status` is not one of `queued`, `running`, `completed`, `failed`, `cancelled` |
| `exa.agent_run.stop_reason.unknown` | error | `stop_reason` is set and is not one of `schema_satisfied`, `budget_reached`, `time_limit_reached`, `stopped`, `error`, `cancelled` |
| `exa.agent_run.error.not_in_schema` | error | an `error:` block — the live `AgentRun` schema has no run-level error; a failed run is `status: failed` plus `stop_reason: error` |
| `exa.agent_run.fault_tag.unknown` | warning | a fault attempt's `tag:` (on `create.fault`, `cancel.fault` or a turn's `fault`) is not one of the ten `AgentError` codes. It is still rendered verbatim as `code`; the warning is for a flat-envelope tag (`RATE_LIMIT`, `INTERNAL`, `INVALID_API_KEY`) copied from another Exa route |
| `exa.agent_run.value.range` | error | a `usage` or `cost_dollars` value (a `data_sources` count or cost included) that is negative, NaN or infinite — every `AgentUsage` and `AgentCostDollars` member is a number with `minimum: 0` |
| `exa.output.grounding.confidence.unknown` | warning | a grounding `confidence` outside `low`, `medium`, `high`; it is emitted verbatim |
| `exa.agent_run.terminal_then_pending` | error | a non-terminal turn **served** after a terminal one — a run does not un-complete. Judged in serve order, not declaration order; see [below](#terminal-is-judged-in-serve-order) |
| `exa.agent_run.script_exhausted` | warning | no unconditional final turn, in `turns` or in `cancel.turns`: the poll after the script's last snapshot gets `scenario.no_matching_turn` and a 404 the author did not intend |
| `exa.agent_run.body_predicate_on_poll` | warning | a turn's `when` uses `body_contains` or `body_json` — a `GET` poll carries no body, so it can never match |
| `exa.agent_run.completed_without_output` | warning | `status: completed` with no `output` — the vendor allows it, but it is almost always an unfinished fixture |
| `exa.agent_run.cost_unscripted` | warning | a terminal snapshot — `completed`, `failed` or `cancelled`, with no special case — that scripts no `cost_dollars.total`: it declares no `cost_dollars`, an empty one, or one with only other meters such as `{search: 0.01}`. `costDollars` is required, so it still renders zero placeholders, `total` included; the warning says those zeros are no billing fact. Script `cost_dollars`, at least `total`, on every terminal snapshot; an explicit `total: 0` is a statement and counts as scripted. It is raised when the scenario loads, so `validation.strict` does not promote it today ([#18](https://github.com/C360Studio/servicesim/issues/18)) |

Every row that judges a turn applies to `cancel.turns` too, addressed `providers.exa_agent_runs.cancel.turns[i]`, and
the two scripts are judged independently: `cancel.turns` needs its own unconditional final turn, and a terminal
snapshot in `turns` says nothing about one in `cancel.turns`.

`exa.agent_run.body_predicate_on_poll` now describes exactly what happens. An `exa_agent_runs` poll is selected by its
position alone and never reads the request body — not even a body a client sends on the `GET` — so a
`body_contains` or `body_json` on a poll turn never matches. Earlier builds matched it against whatever body a poll
happened to carry, so a poll sent with a body could be served a different turn than the same poll without one.
`tavily_research`'s poll is unchanged and still reads the body.

`exa_agent_runs` also checks the **create request** when it arrives, with these findings (the schema documents the
values; what the live API answers to a violation is not documented, so rejecting is this simulator's policy):

| Code | Severity | Condition |
|---|---|---|
| `exa.effort.invalid` | error | `effort` is not one of `minimal`, `low`, `medium`, `high`, `xhigh`, `auto`, `ultra` — `max` is not a member; `null` and non-strings are included |
| `exa.budget.maxDurationSeconds.range` | error on `ultra`, otherwise warning | `budget.maxDurationSeconds` outside 300-10,800. The limit applies only to `ultra`, so for any other effort the request is served and the finding is a warning |
| `exa.budget.maxCostDollars.range` | error on `auto` and `ultra`, otherwise warning | `budget.maxCostDollars` outside 1-100. The limit applies only to `auto` and `ultra`, and an omitted `effort` is `auto`, so for any other effort the request is served and the finding is a warning |

A budget member of the wrong type (`exa.request.field_type`) is always an error, whatever the effort.

`tavily_research`:

| Code | Severity | Condition |
|---|---|---|
| `tavily.research.status.unknown` | error | `status` is not one of `pending`, `in_progress`, `completed`, `failed` |
| `tavily.research.completed_without_content` | warning | `status: completed` with no `content` |
| `tavily.research.terminal_then_pending` | error | a non-terminal turn **served** after a terminal one, judged in serve order; see [below](#terminal-is-judged-in-serve-order) |

`tavily_research`'s validator has no equivalent to `script_exhausted` or `body_predicate_on_poll` today: a body
predicate on a Tavily poll turn is dead in exactly the same way as Exa's, silently, with no load-time warning yet.
A `cancel:` block on it is rejected ([below](#entries-that-serve-no-cancel)): Tavily's Research API documents no
cancel.

**Reset.** `POST /__admin/reset` (scoped with `?namespace=`) drops one namespace's async job records together with
its fault and turn cursors, in the same call; `testkit.Sim.Reset()` does the same but for every namespace at once,
since it carries no namespace argument. Neither may run alone: dropping only the cursors would let the next
create claim index 0 again and collide with a job record that is still live (`job.id_collision`); dropping only the
jobs would 404 every live identifier while the create kept advancing. A job's identifier derives from the call
index it was minted at, so the same create issued after a reset, at the same call position, mints the identifier
it minted before — which is what keeps a golden file portable across a reset the same way every other derived
identifier already is.

#### Terminal is judged in serve order

`exa.agent_run.terminal_then_pending` and `tavily.research.terminal_then_pending` reject a script in which a client
that has seen a terminal snapshot is then served a non-terminal one. The check follows the turns a client is actually
**served**, poll by poll, under the [selection rules](#turn-selection-rules) — first match on `call_index`, then the
unconditional fallback — not the order the turns are declared in. It evaluates poll 0, every `call_index` a turn
names, and each of those plus one, which covers every position where the served turn can change, and it reports each
offending turn once, at its `respond.status`, naming both polls.

```yaml
exa_agent_runs:
  turns:
    - when: {call_index: 1}
      respond: {status: running}   # poll 1
    - respond: {status: failed}    # polls 0, 2, 3 …
```

Declared, that reads as "running, then failed". Served, it is `failed`, `running`, `failed`: the run un-fails at poll
1, and the check rejects turn 0. Builds up to and including v0.5.0 walked declaration order instead, so they loaded
this script silently, and they flagged a non-terminal turn declared after a terminal one even when an earlier turn
with the same `call_index` meant it was never served. A turn that is never served is not judged.

On `exa_agent_runs` the check runs on `turns` and on `cancel.turns`, each on its own. The finding codes and their
severity are unchanged.

#### An accepted create whose reply is lost

A create-then-poll client cannot tell "the request was rejected" from "the request took effect and the reply was
lost": it holds an error either way. The first leaves nothing behind. The second leaves a run that is still going —
on a real API, still being billed — that the client never confirmed. `accepted: true` on a fault attempt scripts the
second: the request took effect, the job is kept, and the attempt then fails the way it says. The same attempt
without it is the first. `accepted` keeps the *job*; how much of its identifier the client learns depends on the
shape, and not every shape withholds it ([below](#which-shapes-withhold-the-identifier)).

```yaml
providers:
  exa_agent_runs:
    create:
      fault:
        attempts:
          - {kind: close_before_headers, accepted: true}  # the job is saved, then the connection dies
    turns:                                                # the retry draws attempt 1, past the plan, and is served
      - when: {call_index: 0}
        respond: {status: running}
      - respond: {status: completed, output: {text: done}}
```

The rejected counterpart is the same file with `- {kind: close_before_headers}`: the client sees the same connection
error, and no job exists.

With the plan above the client's create fails with a connection error and never learns an identifier. The evidence is
the simulator's own, and a test controller reads it without any new endpoint: `GET /__admin/jobs` (`sim.Jobs()` in
Go) lists exactly one job, and the journal's create entry shows `fault_key: exa:agent_runs.create`,
`attempt_index: 0`, `fault_kind: close_before_headers` and `aborted: true` — the job matches it on namespace, entry
and create index. A poll of the listed identifier answers like any other, which is what shows the job is real. The
listing is read-only evidence for the test, not a recovery API: it carries no lane key (a lane key can embed a
header-derived value, and credentials must never reach an admin listing), and no route the application reaches
returns a job's identifier other than the create's own response. Read the journal with `AwaitRequests` after a
request that ended at the transport level, as everywhere else.

What `accepted` does **not** do:

- **It is not idempotency.** A client retry claims the next attempt and mints a *second* job with a different
  identifier; two lost replies and a retry leave three jobs. Neither vendor documents an idempotency key, so the
  simulator does not invent one.
- **It is not a billing fact.** The simulator records no cost for the orphaned job; a poll renders the scenario's
  own `cost_dollars`, as for any other run.
- **It does not exceed the job bound.** At `--max-jobs` the create is refused with the provider's rejection and
  `job.limit_reached`, and the scripted fault is *not* applied — the rejection is what the client sees, and the
  attempt it had claimed is reported as `fault.attempt_on_rejection`. A reset drops the accepted job together
  with its cursors, so the next create is attempt 0 again and re-mints the same identifier.

**Where it can appear.** Load accepts `accepted` on any fault attempt — `create.fault`, a turn's plan, a block-level
plan — because load sees one attempt in isolation, and whether the request that claims it creates a job is a fact only
the routes know. It means something only on a route whose request can take effect: an async create, which keeps its
job, and a cancel, which records its cancel ([Cancelling a job](#cancelling-a-job); Exa's agent-run cancel is the
in-tree one). Exa's Agent create is the one this is built and tested against; `tavily_research`'s create mints
through the same seam. An accepted attempt claimed by a request that neither creates a job nor runs a cancel — an
`accepted` in a poll plan, say — raises `fault.accepted_unreachable`, an error on that request's journal entry, and
the attempt still applies as an ordinary fault.

**When it is redundant.** `accepted` on an attempt whose client still receives the create's body is a load error,
`scenario.fault.accepted.redundant`: the job is kept without it, so declaring it claims a lost reply the attempt does
not script. "Receives the body" is the delivery predicate `scenario.FaultAttempt.DeliversBody`, not a list of kinds,
so the rule follows what the executor does. It is an error on a 2xx or 3xx `status` that still delivers its body
(not 204 or 304), a `delay`, `delay_after_headers`, `extra_fields`, `wrong_content_type`, `oversized_body` and every
`stream_*` kind — a create never streams, so a `stream_*` attempt is dropped and the body is delivered. It is *not* an
error, and `accepted` is meaningful because the body does not arrive intact, on `close_before_headers`,
`truncate_body`, `empty_body`, `invalid_json`, any `status` of 400 or above, a `body:` override at any status, and a
`status` of 204, 304 or 101. Whether the application also loses the identifier there is the
[next question](#which-shapes-withhold-the-identifier); for `truncate_body` it usually does not.

| Code | Severity | Condition |
|---|---|---|
| `scenario.fault.accepted.redundant` | error, at load | `accepted: true` on an attempt that delivers the create's body, including a `stream_*` kind. |
| `fault.accepted_unreachable` | error, per request | An `accepted` attempt was claimed by a request that neither created a job nor ran a cancel. |

The runtime finding is an error rather than a warning for the reason `scenario.stream.abort_unreachable` is: a
scenario that scripts "accepted, reply lost" on a route that cannot honour it would otherwise run green while
exercising nothing of the sort, and its author would find out from an assertion about a job that was never there.

##### Which shapes withhold the identifier

The create's own response is the one place the application can learn the identifier, so whether `accepted` also
hides it is the shape's doing, not the modifier's:

| Shape | Does the application learn the identifier? |
|---|---|
| `close_before_headers` | No: nothing arrives. |
| `empty_body`, `invalid_json` | No: the body is replaced. |
| a `status` of 400 or above, a `body:` override | No, provided the provider's error body is built from the attempt alone. Exa's and Tavily's are. A `status: 504` attempt answers with the agent error envelope and an `x-request-id` derived separately from the run id. |
| a `status` of 204, 304 or 101 | No: `net/http` writes no body. |
| `truncate_body` | **Usually yes.** It sends a *prefix* of the rendered body, and the identifier is the first key of both in-tree creates. |

For Exa's create the body is 390 bytes and opens with `{"id":"` (7 bytes) followed by the 42-character
`agent_run_…` identifier, so the identifier occupies bytes 7–48. Only a `truncate_after_bytes` of **48 or less**
withholds it; 49 delivers all of it. Leaving `truncate_after_bytes` out means half the body, 195 bytes, which
delivers the whole identifier, and so does any larger value — at **390 or more** the response is complete, an
ordinary `200` with nothing lost. Tavily's research create behaves the same way: `request_id` is the first key
(`{"request_id":"` is 15 bytes, so the 36-character UUID occupies bytes 15–50), its body is well over 102 bytes, so
the default cut delivers the UUID, and only a `truncate_after_bytes` of 50 or less withholds it. To script a reply
the application really loses, use `close_before_headers`, or give `truncate_body` a small explicit cut. The tests pin
both sides — an 8-byte cut and a cut at byte 48 hide the identifier; a cut at byte 49, the default cut and a
390-byte cut deliver it with the job kept — and pin Exa's layout (390, 7 and 42) against the real body.

Three further ways a script can defeat itself under `accepted`, none of them a load error because load does not have
the rendered body: a `truncate_body` cut that reaches the identifier, above; a provider `FaultBody` that echoes the
rendered body or the minted identifier, which hands it to the client under a status of 400 or above (a profile
author's rule: [Async jobs](building-a-profile.md#async-jobs)); and a `headers:` override of `Content-Length`,
`Transfer-Encoding` or `Content-Encoding`, which can leave the client unable to read the body that was written and so
only hides the identifier further.

#### Cancelling a job

A cancel asks a job to stop. What it answers depends on where the job is when the cancel lands, and the job's later
polls have to agree with that answer. `cancel:` on a create-then-poll entry scripts both: `cancel.fault` is the cancel
route's own attempt budget, independent of the create's and the poll's, and `cancel.turns` is a script of **poll
snapshots** — what the job's polls serve once a cancel is recorded. It scripts no cancel *response*: a cancel answers
with the snapshot the job's next poll will be served, so the cancel and the polls after it cannot disagree.

In tree, `exa_agent_runs` serves it: `POST /agent/runs/{id}/cancel`, on its own fault key `exa:agent_runs.cancel`
and a per-job lane. Every other entry rejects the block at load ([below](#entries-that-serve-no-cancel)).

```yaml
providers:
  exa_agent_runs:
    turns:                            # the job's own polls
      - when: {call_index: 0}
        respond: {status: running}
      - when: {call_index: 1}
        respond: {status: running}
      - respond: {status: completed, output: {text: done}, cost_dollars: {total: 0.045}}
    cancel:
      fault:                          # the cancel route's plan
        attempts:
          - {status: 500}             # each job's first cancel fails and records nothing
          - {}
      turns:                          # the polls after a recorded cancel
        - when: {call_index: 0}       # the first poll after the cancel
          respond: {status: running}  # acknowledged, still winding down
        - respond:                    # usage accrued before the cancellation is billed
            status: cancelled
            usage: {agent_compute_units: 3}
            cost_dollars: {total: 0.012}
```

On Exa a cancel answers `200` with an `AgentRun` — the snapshot the table below names — or a vendor-shaped error,
and its journal entry says what happened, because the journal keeps no bodies: `exa.agent_runs.cancel.accepted` only
for a cancel that was **recorded**, `exa.agent_runs.cancel.repeated` for one sent while a cancel is already recorded,
`exa.agent_runs.cancel.terminal` when completion won, and `exa.agent_runs.cancel.unrecorded` when the attempt did not
commit. A poll is labelled `exa.agent_runs.polled.<status>` with the status its snapshot scripts (`running` when it
scripts none), so a confirmed cancellation is an `exa.agent_runs.polled.cancelled` entry with no fault. A label is the
handler's even when a scripted fault replaced the response, so read it beside the entry's `fault_kind`. The wire
contract is the "Cancel" section of `profiles/exa/contracts/README.md`.

**Positions.** A job's *poll position* is how many poll positions its lane has claimed — every poll counts, a poll
answered with a scripted fault included — so 0 before its first. A cancel is
recorded *at* a position, the one the job's next poll will be served at. From then on, the job's poll at position `i`
is answered from `cancel.turns` with a call index of `i` minus that position, so inside `cancel.turns`
`when.call_index` counts polls **since the cancel**. The journal's `attempt_index` for the same poll stays absolute —
the poll lane's own count — and this is the one place the two numbers differ for one request.

**The cancel judges the job by its next poll.** Not by the last snapshot served, which a client may never have seen,
but by `turns` at the job's current position — the snapshot its next poll would be served:

| The job's next poll would serve | The cancel's claimed attempt | The cancel answers with | Recorded |
|---|---|---|---|
| a terminal snapshot | any | that terminal snapshot: completion wins | nothing; no `cancel:` block is needed |
| a non-terminal snapshot | commits | `cancel.turns` at call index 0 | the cancel, at the job's position |
| a non-terminal snapshot | does not commit | that snapshot, which the fault then replaces | nothing |
| (a cancel is already recorded) | any | `cancel.turns` at the job's next position since the cancel | nothing new |

With the script above each job's first cancel answers 500 and records nothing, so read every "cancel" below as that
failure and its retry. Create, cancel: recorded at position 0, and the polls serve `running`, then `cancelled` from
then on. Create, poll, cancel: recorded at position 1. Create, poll, poll, cancel: the next poll would serve
`completed`, so completion wins, nothing is recorded, and the polls keep serving `completed`.

**A cancel is recorded only when its attempt commits** — the same predicate a create keeps its job on: the client
receives the response body (`scenario.FaultAttempt.DeliversBody`), or the attempt says `accepted: true`. A
`{status: 500}` records nothing, exactly as a vendor that never received the cancel records nothing, and the client's
retry draws the next attempt. `{kind: close_before_headers, accepted: true}` records the cancel and loses its reply:
the client holds a connection error, and its next poll shows the cancel took effect. `accepted` on an attempt that
delivers its body is `scenario.fault.accepted.redundant`, as on a create.

**Every cancel claims exactly one attempt.** A cancel that resolves a job claims exactly one attempt from its cancel
lane, first, whatever it then decides — so the attempt a client's retry draws depends on how many cancels it sent,
never on the job's state. A cancel of an identifier that resolves no job is the vendor's not-found and claims nothing,
like a poll of one.

**When the cancel cannot be scripted**, the request records an error, nothing is recorded on the job, and a profile
answers its vendor's 500. The claimed attempt stays spent, but because the request records an error its scripted fault
is not applied (`fault.attempt_on_rejection`):

| Code | Severity | Condition |
|---|---|---|
| `job.cancel_unscripted` | error, per request | The cancel would take effect, but the scenario cannot answer the polls after it: no `cancel:` block or no `cancel.turns`, no `cancel.turns` turn matching call index 0, or no `turns` turn answering the job's next poll, so whether it is terminal cannot be judged. Also a repeated cancel whose next `cancel.turns` snapshot matches nothing. |
| `job.cancel_contended` | error, per request | Polls of the same job kept landing while the cancel was being recorded, eight times in a row. A retried cancel starts afresh. Also raised, with a message naming it, when a custom job store answers an outcome outside its contract — a store bug, not contention, and never retried. |

Recording no marker in either case is what keeps a later poll from indexing into a `cancel.turns` that does not
exist.

**The polls after a cancel** follow `cancel.turns` under the usual [selection rules](#turn-selection-rules), so it
needs its own unconditional final turn (`exa.agent_run.script_exhausted` warns otherwise) and
[serve-order terminality](#terminal-is-judged-in-serve-order) judges it on its own. A terminal snapshot is allowed
there, `completed` included, to script "the cancel was acknowledged and the run completed anyway". A `cancel.turns`
turn carries no `fault:` — that is a load error, because the poll route's plan stays on `turns` and the cancel
route's is `cancel.fault`. `cancel.fault` is validated like `create.fault`, at `providers.<entry>.cancel.fault…`, and
a `when.route` in `cancel.turns` must name a route the provider serves, as in `turns` — on Exa, the poll's: a cancel
selects no turn of its own, so naming its route is `scenario.turn.route_unknown` in either script. Poll selection
reads the poll's position alone, never a request body — a `GET` poll carries none — which is what lets a cancel know
the exact snapshot the next poll will serve.

**The built-in scenarios** script a `cancel:` block on every Exa run that is still running at its first poll, so a
cancel of a shipped run is answered rather than failed closed with `job.cancel_unscripted`; a test enforces this for
every built-in. Call 0 of the block acknowledges `running`, and an unconditional `cancelled` snapshot follows, with a
scripted `usage` and `cost_dollars.total`. That acknowledgement is the corpus's reading of the vendor's "cancel
immediately" — simulator policy, not vendor fact; the [contract](../profiles/exa/contracts/README.md) marks what the
live API reports between a cancel and `cancelled` unverified — and it applies only inside a window. As the table under
"The cancel judges the job by its next poll" says, a cancel is acknowledged as `running` only while the job's next poll
would still be `running`. Once that poll would serve a terminal snapshot, completion wins and the cancel answers that
snapshot: `happy` cancelled after two polls answers `completed`, and `async-failed` cancelled after one answers
`failed`. A run that starts terminal needs no block, since completion wins from the start.

##### Entries that serve no cancel

A `cancel:` block loads only on an entry whose profile declares a cancel lifecycle for it; on every other entry the
framework itself rejects it at load, whichever profile serves the entry, because a block that could never take effect
is a scenario bug, not a no-op. In tree, `exa_agent_runs` is the one entry that accepts it — `tavily_research` has a
poll lifecycle, but Tavily's Research API documents no cancel.

| Code | Severity | Condition |
|---|---|---|
| `scenario.provider.cancel_unsupported` | error, at load | a `cancel:` block on an entry whose profile has no cancel lifecycle for it, reported at `providers.<entry>.cancel` |

An entry no registered profile serves is reported as `scenario.provider.unimplemented`, a warning, and its `cancel:`
block is left alone with the rest of it.

##### What a test can see

`GET /__admin/jobs` carries each job's `polls`, the number of poll positions claimed and so the position its next
poll will be served at, and `cancel_at_poll`,
the position a cancel was recorded at — **absent**, not 0, when none was, because 0 is a real position: a cancel
before the first poll. That is how a test tells "the cancel took effect and its reply was lost" from "the cancel was
never recorded" without a new endpoint. In Go, `sim.Jobs()` carries the same facts as `Polls`, `CancelRequested` and
`CancelAtPoll`. Both are read from the job record; the listing still mutates nothing and serves no lane key.
`tavily_research`'s poll does not record its position in this build, so its jobs always list `polls: 0`. A poll or
cancel still in flight when a reset lands is undefined: reset between requests, as everywhere else.

### What the request still controls

A projection describes what the scenario *has to say*; the request decides how much of it reaches the wire. These
are the gates worth knowing before you conclude a projection was ignored:

| Gate | Effect |
|---|---|
| Exa `numResults` (default 10), Tavily `max_results` (default 5) | Truncate `results[]` in declaration order. Nothing is ever sorted by relevance. Also applies to `/findSimilar`'s own `numResults`, over `find_similar.results`. |
| Exa `/contents` `ids`/`urls`, Tavily `/extract` `urls` | Decide which corpus/results entries are rendered at all (D-a), not just how many — see the "fetch-shaped route" sections above. A `contents:`/`extract:` block never lists results directly. |
| Exa `outputSchema` | Without it, a declared `output:` is not emitted. |
| Exa `text` on `/answer` | Without it, citations carry no `text` key. |
| Tavily `include_answer` | Without it, `answer` is `null` however the scenario declares it. |
| Tavily `include_raw_content`, `include_favicon`, `include_images`, `include_usage` | Gate `raw_content`, `favicon`, `images` and `usage` the same way. |
| Tavily `topic: news` | `published_date` appears on results only for the news topic. |

## Validation: what fails, and what only warns

Validation happens in two passes, both before the server reports ready.

1. **Envelope validation, at load.** Schema version, required fields, source-reference integrity, turn ordering,
   `when` well-formedness, `turn_key` extractor forms, fault-plan coherence, and that every `respond` node is a
   mapping. An error here fails the process rather than serving a subtly wrong contract.
2. **Projection-body validation, at composition.** Each provider decodes its own `respond` bodies and validates
   them — the `scenario` package deliberately does not know what an Exa result looks like. Readiness flips only
   after this pass.

Every finding carries a severity, a code and the YAML path it is addressed by. Warnings are readable at runtime on
`GET /__admin/scenario`; errors never get that far, because the process refuses to start.

**An unrecognised provider name is a warning, not an error.** The scenario loads, the unknown block is ignored, its
projection body is never decoded, and the warning appears in `GET /__admin/scenario`:

```json
{"severity":"warning","code":"scenario.provider.unimplemented","path":"providers.openai",
 "message":"no handler is registered for provider kind \"openai\"; this build serves nothing for it"}
```

The reason is concrete: a scenario file shared across repositories must not break the moment one consumer pins an
older Servicesim that has not learned the new provider yet. One team's upgrade breaking another team's suite costs
far more than ignoring a block nobody can serve. The built-in
[`unknown-provider`](../scenarios/protocol/unknown-provider.yaml) scenario exists to hold that policy in place.

Errors from within a known structure are still hard failures, and they name the YAML path, the provider and the
turn index. A typo in a key you *did* mean to write is never silently tolerated.

### The version gate

A build accepts **its own schema version and every earlier one**. The gate is a range, not an equality:

| Declared | On a build at version 1 | On a build at version 2 |
|---|---|---|
| `version: 0` | load error — below the floor | load error — below the floor |
| `version: 1` | loads | loads |
| `version: 2` | load error — from the future | loads |

Older-loads-on-newer is the direction that matters to you: a scenario file pinned to `version: 1` keeps loading
unchanged when Servicesim's schema version moves to 2. It does not need re-dating, re-pinning or hand-editing. The
alternative — strict equality — would mean that every scenario file in every adopting repository stopped loading on
the day the schema moved, which is an N-repository migration bought for nothing.

Newer-on-older cannot work and fails loudly, because the keys such a file carries do not exist in the older build
and `KnownFields(true)` is the whole point. `version` is read *before* the strict decode, so this is reported as one
sentence rather than as a wall of unknown-key errors:

```text
scenario declares version 2, but this build of Servicesim understands only version 1;
upgrade Servicesim or pin the scenario to version 1
```

Versions below `1` are rejected rather than treated as merely old. Zero is what a typo or an unrendered template
produces, never a released schema, and decoding such a file would validate it against an envelope nobody specified:

```text
scenario declares version 0, but a schema version is at least 1;
this build of Servicesim understands version 1
```

A file with no `version` key at all fails with `scenario declares no version; add "version: 1"`. All of these are
error findings on the path `version`, and all prevent the process from starting.

#### What actually forces a version bump

Adding an **optional** key is not a breaking change and does not require one. Under `KnownFields(true)` the strict
decode rejects keys it does not know, so the compatibility question runs one way only: an older file never carries a
key a newer build lacks. A newer build reading an older file simply finds the new keys absent and applies their
documented defaults.

The practical conclusion, recorded here so it does not have to be re-derived: **every remaining schema change on the
roadmap is additive, so none of them forces `version: 2`.** A bump is needed only to remove a key, to rename one, or
to change what an existing key means — and each of those is a decision to take deliberately, not a side effect of
adding a feature.

## Worked example: one source through every provider

This is the shape most scenarios take. One canonical corpus, projected four ways.

```yaml
version: 1
name: one-source
description: One canonical source projected through every provider this build serves.
seed: one-source

time:
  base: 2026-01-01T00:00:00Z

sources:
  - id: report-a
    url: https://example.test/report-a
    title: Report A
    author: A. Author
    published_at: 2026-05-20T00:00:00Z
    text: Full source text of Report A.
    snippets:
      - A relevant excerpt from Report A.
    favicon: https://example.test/favicon.ico
    claims:
      - id: claim-1
        text: A normalised claim represented by this source.

  - id: report-b
    url: https://example.test/report-b
    title: Report B
    text: Full source text of Report B.
    claims:
      - id: claim-1          # the same claim: this is the corroboration signal
        text: A normalised claim represented by this source.

providers:

  exa:
    auth:
      mode: required
    results:
      - source: report-a
        highlights:
          - A relevant excerpt from Report A.
      - source: report-b
    cost_dollars:
      total: 0.005
    answer:
      answer: Report A and Report B agree on the finding.
      citations:
        - report-a
        - report-b

  tavily:
    answer: A short synthesis of Report A and Report B.
    response_time: 1.15
    results:
      - source: report-a
        score: 0.98
      - source: report-b
        score: 0.71

  perplexity:
    answer: A grounded answer citing Report A.
    finish_reason: stop
    citations:
      - report-a
    search_results:
      - report-a
      - source: report-b
        snippet: An overridden snippet for Report B.
    usage:
      prompt_tokens: 24
      completion_tokens: 96
      total_tokens: 120
      search_context_size: medium
      cost:
        input_tokens_cost: 0.0001
        output_tokens_cost: 0.0004
        total_cost: 0.0005

  perplexity_agent:
    answer: Report A states the finding.
    queries:
      - report a
    search_results:
      - report-a
    annotations:
      - source: report-a
        start_index: 0
        end_index: 8
```

Both `report-a` entries render the same URL through Exa, Tavily, Sonar and the Agent surface, so a consumer that
fails to deduplicate reports four results where there is one document. Both sources assert `claim-1`, so a consumer
that fails to count corroboration reports one supporting source where there are two.

Remember the request-side gates: Tavily's `answer` renders `null` unless the request sets `include_answer`, and Exa
returns at most `numResults` entries. The projection is what the scenario *can* say, not what every request sees.

## Worked example: a scripted agentic loop

A turn list scripts successive calls to one provider. This one searches on the first call, answers once the tool
result comes back, and terminates on anything else.

```yaml
version: 1
name: agent-loop

sources:
  - id: report-a
    url: https://example.test/report-a
    title: Report A
    text: Full source text of Report A.

providers:
  perplexity_agent:
    auth:
      mode: required
    turns:

      # Call 0: nothing has come back yet, so the agent reports the search it ran
      # and returns no results. Matched by call index rather than by prompt text,
      # which is what makes it survive a reworded prompt.
      - when:
          call_index: 0
        respond:
          answer: Searching for Report A.
          queries:
            - report a
          search_results: []

      # The follow-up call carries the tool result, which names the document.
      - when:
          body_contains: report-a
        respond:
          answer: Report A states the finding.
          search_results:
            - report-a
          annotations:
            - source: report-a
              start_index: 0
              end_index: 8

      # No `when`, so it matches anything. This is what makes the loop terminate
      # rather than dead-end on a not-found error.
      - respond:
          status: incomplete
          answer: No further information.
```

The other providers are left out entirely, which is legal: an undeclared provider still serves a well-shaped empty
success on its own listener. Add them in the single-shot form when a test needs them — one `--scenario` flag
configures every listener, whichever form each block is written in.

Both examples on this page were verified against this build by loading them with `servicesim --scenario-dir` and
calling every route they project.
