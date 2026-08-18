# Troubleshooting

Organised by **symptom**, because that is how you arrive here.

Almost everything below is something Servicesim already told you about, in the request journal — the first place
to look and the place people forget:

```bash
curl -s 'http://localhost:8080/__admin/requests?pretty=1' | less
```

In a Go test the same data is `sim.Requests(exa.Name)`, and `testkit.AssertNoErrors(t, entry)` turns "my
adapter sent a valid request" into an assertion instead of a hope. Two fields on an entry answer most questions
before you read anything else: `findings` says what was wrong with the request, and `outcome.fault_key` says which
state lane served it.

Scenario-level problems — a file that will not load, a provider block that does nothing — surface on
`GET /__admin/scenario` instead, which reports what was loaded plus every warning that did not prevent loading.

## My request came back 401 and I did set a key

Servicesim validates credential **placement**, not value. Any fake value works; the wrong header does not.

| Provider | Accepted placement |
|---|---|
| Exa | `x-api-key`, or `Authorization: Bearer` |
| Tavily | `Authorization: Bearer`, or a body `api_key` property (warns `tavily.api_key.in_body`) |
| Perplexity | `Authorization: Bearer` only |
| MCP | `Authorization: Bearer` — and **optional by default**: a missing credential is a 401 only when the scenario's `mcp` block sets `auth: {mode: required}` (or `reject`) — see [MCP: I got a 401](#mcp-i-got-a-401-and-i-thought-credentials-were-optional) |

Sending Tavily an `x-api-key` header is a 401 on purpose: Tavily's REST contract is Bearer, and an adapter that
gets this wrong against Servicesim would get it wrong against production too. The journal names it:

```json
{"severity":"error","code":"auth.missing",
 "message":"no credential was presented in an accepted placement (authorization, x-api-key)"}
```

A scenario can also tighten this deliberately: `auth.mode: reject` always answers 401 (that is what the
`unauthorized` built-in does), `auth.expect_key` requires an exact match, and `auth.headers` overrides the
accepted placements.

## My request came back 400 and the body looked fine

Servicesim is deliberately strict about requests — that is the point of it. The journal finding names the exact
field:

```json
{"severity":"error","code":"exa.query.missing","field":"query","message":"query is required"}
```

Common causes: a required field missing, a field with the wrong JSON type, or an enum value the vendor does not
accept. Enum drift is the usual surprise — for example Tavily's `search_depth` accepts four values
(`basic`, `advanced`, `fast`, `ultra-fast`), and `include_answer` accepts a boolean **or** the strings `"basic"` /
`"advanced"`. See [`contracts/`](../contracts/README.md) for the verified shape of every field.

An *unknown* request field is the opposite: a `request.unknown_field` **warning** and a 200, because real clients
send fields Servicesim has not modelled. Promote it to an error for a specific provider with
`validation: {promote: [request.unknown_field]}` if you want your adapter held to the documented field set.

## I get 404 on a path the vendor definitely serves

Each provider has its **own listener on its own port**, because Exa and Tavily both serve `POST /search` and a
single port cannot disambiguate them.

| Port | Provider | Routes |
|---:|---|---|
| 8081 | exa | `POST /search`, `POST /answer`, `POST /contents`, `POST /findSimilar`, `POST /agent/runs`, `GET /agent/runs/{id}`, `HEAD /agent/runs/{id}` |
| 8082 | tavily | `POST /search`, `POST /extract`, `POST /research`, `GET /research/{request_id}`, `HEAD /research/{request_id}` |
| 8083 | perplexity | `POST /v1/sonar`, `POST /chat/completions`, `POST /v1/chat/completions`, `POST /v1/agent`, `POST /v1/responses`, `POST /responses` |
| 8084 | mcp | `POST /mcp` |

Sending Tavily's request to port 8081 reaches Exa's handler, which will reject it as a malformed Exa request. Check
the `provider` field on the journal entry — it tells you which listener actually received the call.

## I get 405 rather than 404

The path exists but the method is wrong. This is deliberate: a 404 for a wrong method would let an adapter that
sends `GET /search` look like it hit an unknown endpoint rather than a routing bug of its own.

## I get 404 on a URL carrying an `/x/<scenario>` prefix

The `/x/` prefix selects **behaviour**, and the name has to be one this process actually serves. The journal
entry carries a `scenario.unknown` finding and the server logs the list it does serve:

```json
{"level":"WARN","msg":"scenario.unknown","scenario":"one-source","namespace":"default",
 "served":"example1,example2,faultturn"}
```

Two things trip people up here:

- Under `--scenario-dir`, a scenario is named by its **file name minus the extension**, not by the `name:` key
  inside it. `sc/example1.yaml` is `/x/example1`, whatever it calls itself. A file named `default.yaml` is also
  what serves requests carrying no `/x/` prefix at all.
- Under a single `--scenario`, the only accepted name is that scenario's own `name:`. Any other `/x/` value is
  refused rather than quietly falling back to the loaded scenario.

## I get a 400 or 422 complaining about the `/n/` path prefix

The namespace name failed validation. A name is 1 to 64 characters of `[A-Za-z0-9_-]` — no dots, no slashes, no
percent-encoding — and the same rule applies to the `/x/<scenario>` segment and to the `X-Servicesim-Namespace`
header:

```json
{"requestId":"6c19404215fa0b2e878faa18675b4d90",
 "error":"the /n/ path prefix must name a namespace of 1 to 64 characters from [A-Za-z0-9_-]",
 "tag":"INVALID_REQUEST_BODY"}
```

The status is each vendor's own shape for a rejected request: 400 from Exa and Tavily, 422 from Perplexity. The
journal finding is `namespace.invalid`, and the offending value is deliberately **not** echoed anywhere — it
arrives from the request path and must not reach a log line.

The usual cause is interpolating something that is not a bare identifier: a Go test name (`TestFoo/case_one`
contains a slash), a UUID with braces, or a `t.Name()` used raw. `sim.NamespaceFor(t)` derives a legal name from
`t.Name()` for you; `provider.ValidNamespace` is the same check the server applies if you are building the name
yourself.

A rejected request is served in the `default` namespace so that nothing ever allocates state under a name that
failed validation — which means the request also does not appear under the namespace you thought you were using.

## I get a 503 saying the process is at its `--max-namespaces` bound

Namespaces are created on first use and are **never evicted**, because evicting one would reset a running test's
turn cursor mid-loop. Once the bound (default 1024) is full, a request naming a new namespace is refused before the
handler runs:

```json
{"requestId":"6c19404215fa0b2e878faa18675b4d90",
 "error":"namespace \"c\" cannot be served: the process is at its --max-namespaces bound",
 "tag":"INTERNAL_ERROR"}
```

It is a hard refusal rather than a warning on purpose. Serving a 200 anyway was tried and is worse: the test in the
refused namespace saw success, collected no journal entries, and failed later on an assertion counting requests it
had apparently never sent — with the real cause visible only in the simulator's stderr.

Three ways out, in order of preference:

1. Stop minting a namespace per *request*. A namespace is a per-test boundary; one per test case is the intended
   cardinality, one per HTTP call is not.
2. Free the ones you are finished with: `POST /__admin/reset?namespace=<name>` drops that lane's journal entries,
   fault counters and job records **and releases its slot**.
3. Raise `--max-namespaces`.

`GET /__admin/namespaces` shows every live lane, including ones holding no entries, because those still count
against the bound:

```json
{"namespaces":[{"namespace":"a","entries":1},{"namespace":"b","entries":1},{"namespace":"default","entries":8}]}
```

The `default` namespace never consumes budget — it is the lane every unprefixed request has always been served in,
so counting it would make `--max-namespaces=1` mean "no namespaces at all".

## A create is refused: the namespace is at its `--max-jobs` bound

Async jobs — Exa `POST /agent/runs`, Tavily `POST /research` — are held per namespace and are **never evicted**,
for the same reason namespaces themselves are not: evicting a job would turn a later poll for a job the client
successfully created into the vendor's own 404. Once a namespace holds `--max-jobs` (default 256) live jobs, the
next create in it is refused with a `job.limit_reached` finding:

```text
namespace "t-42" holds its maximum of 256 jobs; reset it with POST /__admin/reset, give each test its own
namespace, or raise the bound
```

Those are the three remedies, in the order the finding names them:

1. `POST /__admin/reset?namespace=<name>` drops that namespace's journal entries, fault counters and job
   records together, and releases its job slots.
2. Give each test its own namespace, the same fix `--max-namespaces` bounds describe above.
3. Raise `--max-jobs`.

`GET /__admin/jobs?namespace=<name>` shows what is currently live in a namespace — every job's id, entry and
creation time — which is the fastest way to tell whether the bound was reached by a genuine backlog or by a
suite that is not tearing down after itself.

A related finding, `job.id_collision`, means a create re-minted an identifier that is still live:

```text
job "run_9f2c1ab4e5d67890abcdef0123456789" is already live in namespace "t-42"; the usual cause is a reset that
dropped the fault cursors without dropping the job records, so this create re-minted an identifier it had
already used
```

Reset drops all three stores in one call, which leaves one way to reach it: `POST /__admin/reset` racing live
traffic in the same namespace. Reset is a local-development convenience, not a concurrency mechanism (house rule
6). Use one process, or one namespace per parallel test.

## A poll returns 404 for a job the create just returned

Also known as "polls 404 intermittently." A well-formed identifier — shaped like this process's own scheme —
that resolves to nothing, in a namespace that has minted at least one job, raises a `job.foreign_id` **warning**
and logs `servicesim.job_foreign`. The response itself is unchanged: still the vendor's ordinary 404.

The finding cannot tell which of three things happened, and names all three rather than picking one: another
replica minted the job and this process never saw the create (run one replica, or route stickily on
`/n/<namespace>`); a reset dropped the record without dropping the client's copy of the identifier
(`POST /__admin/reset` drops a namespace's jobs along with its fault cursors); or the client sent an identifier
this process never minted at all — stale between tests, or hand-written into a fixture. In a suite that runs one
process — every supported configuration — the third is by far the likeliest cause, which is also why this is a
warning and not an error.

A poll in a namespace that has minted nothing raises no finding at all: that miss is a typo, not a divergence.
See ["Counts and cursors differ per run"](#counts-and-cursors-differ-per-run-or-a-fault-fires-twice) for the
multi-replica case this diagnostic exists to name.

Like every warning, `validation.strict` or a `validation.promote` entry for `job.foreign_id` turns it into an
error — which fails `AssertNoErrors`, even though the response is still the vendor's unchanged 404. A suite that
runs strict and also polls `HEAD`/`GET` for ids it knows are absent should demote `job.foreign_id` instead.

## `POST /__admin/reset` came back 400

Reset requires an **explicit scope**. A bare reset is refused:

```json
{"error":"reset requires an explicit scope: either ?namespace=<name> to drop one namespace, or ?all=true to drop every namespace"}
```

That is a deliberate change from a bare reset meaning "everything". On a shared container, one test's cleanup would
otherwise zero a hundred other tests' cursors mid-run — and the result is not a failed reset, it is another test
receiving the turn scripted for a different call and failing somewhere else entirely.

The other two 400s on this endpoint:

```json
{"error":"?namespace= and ?all=true are mutually exclusive: reset one namespace or every namespace, not both"}
{"error":"invalid namespace parameter: expected 1 to 64 characters from [A-Za-z0-9_-]"}
```

A successful scoped reset echoes the lane it dropped, which is your confirmation that the state which disappeared
is the state you asked about:

```json
{"status":"reset","namespace":"t1"}
```

## The container will not become ready

Readiness only succeeds after the scenario has loaded, validated and resolved. A scenario error therefore shows up
as "never becomes healthy" rather than as a failing request. Read the logs:

```bash
docker logs <container> 2>&1 | head -20
```

Each error is logged as a `scenario.finding` event carrying a severity, a code, the YAML path and a message:

```text
level=ERROR msg=scenario.finding severity=error code=scenario.source.unknown
  path=providers.exa.turns[0].respond.results[0].source message="unknown source \"nope\""
```

Frequent causes, with the code each produces:

| Code | What it means |
|---|---|
| `scenario.version.unsupported` | The file declares a schema version this build does not understand. |
| `scenario.version.unreadable` | No `version:` key at all. |
| `scenario.decode.failed` | A typo'd key: `field titel not found in type scenario.Source`. |
| `scenario.source.unknown` | A `source:` reference no `sources:` entry declares. |
| `scenario.provider.body_with_turns` | A provider block declares `turns:` and projection keys at the same time. |
| `scenario.provider.fault_with_turns` | A block-level `fault:` alongside `turns:`. |
| `scenario.turn.unreachable` | An unconditional turn is not last, so later turns can never be selected. |
| `scenario.turn_key.invalid` | An unrecognised `turn_key` extractor form. |

The `turns[0].respond` in that path is normalisation, not a typo: a single-shot provider block becomes one
unconditional turn at load, so findings against its body are addressed that way even when the file contains no
`turns:`. See [`scenario-schema.md`](scenario-schema.md#the-multi-turn-form).

Servicesim refuses to start rather than serving a broken scenario. A container that comes up healthy while silently
serving something wrong is a worse outcome than one that will not start.

## My scenario file cannot be found inside the container

Scenario resolution is confined to `--scenario-root` (defaulting to the scenario file's own directory) through
`os.Root`, so nothing outside the mount can be opened — symlinks included. Mount the directory, then reference the
file by its path *inside* the container:

```yaml
volumes:
  - ./test/fixtures/research:/scenarios:ro
command: ["--scenario", "/scenarios/my-scenario.yaml"]
```

## A provider block in my scenario does nothing

Check `GET /__admin/scenario`. A provider name this build has no handler for is a **warning**, never a load
failure, and the block is ignored:

```json
{"severity":"warning","code":"scenario.provider.unimplemented","path":"providers.openai",
 "message":"no handler is registered for provider kind \"openai\"; this build serves nothing for it"}
```

That policy exists so a scenario file shared across repositories does not break the moment one consumer pins an
older Servicesim. The same warning appears for a provider that *is* implemented but was not selected by
`--providers`, so a scenario declaring `tavily` while the process runs `--providers exa` looks identical here.

Two other things produce the same "nothing happened" feeling:

- A misspelled provider name — `perplexity_agents` — is just an unimplemented provider, with the warning above.
- A `kind:` that names no registered implementation. `kind` defaults to the block's own name; set it only when you
  are deliberately declaring two instances of one implementation.

An undeclared provider is *not* an error either: its listener still answers, with a well-shaped empty success.

## My scenario declares more results than I got back

The projection is what the scenario *can* say; the request decides how much reaches the wire. Truncation is by
declaration order — nothing is ever sorted by relevance.

| Request field | Default | Effect |
|---|---:|---|
| Exa `numResults` | 10 | Truncates `results[]`. |
| Tavily `max_results` | 5 | Truncates `results[]`. |

## Tavily returned `"answer": null` and my scenario declares an answer

Several Tavily fields are gated on the request, not on the scenario. If your adapter does not ask for them, they
render null, empty or absent however the scenario is written:

| Request field | Gates |
|---|---|
| `include_answer` | `answer` (renders explicit `null` when not asked for) |
| `include_raw_content` | `results[].raw_content` (renders `null`) |
| `include_favicon` | `results[].favicon` |
| `include_images` | `images[]` and `results[].images` |
| `include_usage` | `usage` |
| `topic: news` | `results[].published_date` |

Exa has two of its own: a declared `output:` is emitted only when the request supplies `outputSchema`, and
`/answer` citations carry `text` only when the request sets `"text": true`.

This is not the simulator being unhelpful — a consumer that expects an answer it never requested has a bug that
production would show it too.

## My `stream: true` test gets a JSON body

The default streaming policy is `warn`: a Perplexity entry (`perplexity` or `perplexity_agent`) that declares no
`stream:` key, or one whose `when_requested` is `warn`, always serves the ordinary non-streaming body — even
when the request sets `"stream": true`. The journal names it:

```json
{"severity":"warning","code":"perplexity.stream.unimplemented",
 "message":"streaming is not simulated; this request receives the ordinary non-streaming body"}
```

(`perplexity.stream.agent_unsupported` on the Agent surface.) To get a real `text/event-stream` response, script
one: add `stream: {when_requested: stream, deltas: [...]}` inside the turn's `respond:` block — or just declare
`deltas:` with no `when_requested` at all, since a script implies `stream` on its own. `when_requested` is read
from the entry's **first turn only**, because rejecting a request has to happen before turn selection claims a
fault attempt, so writing it on a later turn does nothing but a `scenario.stream.policy.ignored` warning. See
[`docs/scenario-schema.md`](scenario-schema.md#streaming-stream) for the full grammar and the built-in
`streaming` scenario for a worked example.

If the intent is the opposite — proving an adapter's primary path always streams and must not fall back to a
non-streaming body — set `stream: reject` instead. That turns a `stream: true` request into a 4xx
(`perplexity.stream.unimplemented`/`perplexity.stream.agent_unsupported` promoted to an error) rather than a
silently-served JSON body a fixture could pass against by accident.

## My SSE golden diff is unreadable

`testkit.AssertGoldenJSON` decodes both sides as JSON and diffs the decoded values; pointed at an SSE transcript
it either fails to parse the whole body as one JSON value or, at best, reports the entire multi-frame byte string
as a single differing string — a one-character change in one delta looks identical to a completely different
stream. Use `testkit.AssertGoldenSSE(tb, path, transcript, opts...)` instead: it parses both sides into
`(event, data)` frames — the same shape `provider.EncodeSSE` produces — and diffs frame by frame, so the report
names the one frame that changed. Each frame's `data` line is decoded as JSON and compared semantically, exactly
as `AssertGoldenJSON` compares a body; the bare `[DONE]` token is compared as the literal string it is.

Derived identifiers are pruned from every frame only when you ask, exactly as `AssertGoldenJSON` works: pass
`testkit.GoldenDerivedIDs(paths...)`, most often sourced from `sim.DerivedIDs()` so the pruned paths always match
what the registered profile actually derives rather than a vendor field named by hand. An id advances with call
index by design, so a golden bound to one call position would otherwise fail on every other one. For the Sonar
grammar that is the top-level `requestId`/`request_id`/`id`; for the Agent grammar, whose frames wrap a typed
event payload, it is also `response.id`, `item.id` and `item_id` — declare whichever of these this golden's own
vendor derives. One shape needs no declaring at all: every element's `id` inside `response.completed`'s
`response.output` array is stripped whenever any pruning applies (`testkit.GoldenExactIDs()` was not passed),
because a dotted `GoldenDerivedIDs`/`GoldenIgnore` path cannot address an array element.
`testkit.GoldenExactIDs()` opts back into comparing every declared path (and that one array shape) — worth it for
a test that always asserts at the same call position, where the identifiers stay the same run to run, but not
otherwise: ids advance with call index on every call, whether or not the route has a declared fault plan.
`SERVICESIM_UPDATE_GOLDEN=1` rewrites the golden from the observed transcript, verbatim — unlike
`AssertGoldenJSON`, the bytes are not re-encoded, because SSE framing is itself part of the wire contract being
pinned.

A `stream_truncate_chunk` fault's golden is the one case the id-pruning above cannot fully save: the transcript
ends mid-frame, and when the truncated bytes fall inside that frame's own derived id (rather than after it), the
partial frame compares as a raw string, not JSON, so no `GoldenIgnore` path can reach into it. Golden a truncated
transcript at the one call index it was recorded at, or pass a fixed identifier for that turn
(`response_id`/`message_id`/`completion_id` in `docs/scenario-schema.md`) so the fragment never varies.

## A fault fired on the wrong call

Fault plans are registered **per route**, not per turn. In a multi-turn script, the first turn declaring a
non-empty `attempts:` list supplies the plan for the whole route, and that plan starts consuming from the route's
very first request — whichever turn answers it. Writing a `fault:` under the second turn does *not* delay it to the
second call.

To fault a specific call, say so with attempts. A leading `- status: 200` is an unfaulted attempt, so
`[{status: 200}, {status: 429}, {status: 200}]` faults only the second call.

The other half of this: a request answered by a fault attempt has still consumed its call index. `call_index: 0`
and `attempts[0]` describe the *same* request, so a turn scripted for `call_index: 0` behind a leading 429 is
unreachable.

A related trap, specific to combining `auth.expect_key` with a fault plan on the same provider entry: an auth
rejection claims **no** call index at all (see
["What claims a call index, and what does not"](scenario-schema.md#what-claims-a-call-index-and-what-does-not)), so
a plan written next to `expect_key` does not start consuming from the first request a client sends — it starts
consuming from the first request that actually **authenticates**. Two calls with the wrong key, then the correct
one, land the plan's first attempt on that third call, not the first. `outcome.attempt_index: -1` on the rejected
entries is how you tell the two apart in the journal. The `credential-rotation` built-in never combines the two, on
purpose, and pins this exact behaviour in a regression test.

## My timeout test passes instantly

You called `testkit.WithSkippedDelays()`. It exists for a backoff test that only needs to assert "the scenario
asked for this delay" without paying for it — under it a delay fault returns immediately and `outcome.delay_ms`
still records the requested value. A client *deadline*, by contrast, is observed by bytes not arriving: nothing on
the server side of a real socket can fake that, so `WithSkippedDelays` turns a 30-second hang into an instant 200
and the "timeout" your test meant to exercise never happens at all.

For a deadline, timeout or cancellation test, leave delays real (the default) and give the scenario attempt a delay
LONGER than your client's deadline — the `timeout` built-in's 30s, or a `delay: 150ms` of your own against a client
deadline shorter than that. The client's own context (a short `context.WithTimeout`, or the deadline your adapter
sets) is what ends the request; the server's sleep is released by that same cancellation, so the test does not
actually wait out the declared delay. See
[`provider/clock.go`](../provider/clock.go)'s `DelayMode` doc comment, which is the authority on this, and the
`timeout` built-in's own header comment for a worked example.

## My timeout test's abandoned call never appears in the journal

Your client deadline was too short for the runner it ran on. The deadline runs from before the client dials, and
a starved CI runner (race detector, packages tested in parallel, few vCPUs) can take tens of milliseconds just to
deliver the request; if the deadline fires before the simulator has read it, no handler ever ran, nothing was
abandoned server-side, and there is no entry for `AwaitRequests` to find — the route looks idle and the await
times out. Give the deadline real margin over request-delivery latency: a second or two is safe and still tiny
beside the `timeout` built-in's 30s hang. `100ms` has flaked in this repository's own CI for exactly this reason.

## The wrong turn answered, or two callers got each other's responses

Turn cursors are per **lane**, and the default lane is one per route. One route serving several concurrent callers
therefore hands them indices 0 and 1 out of a single sequence, and each receives the turn scripted for the other —
a coherent-looking response from the wrong lane, which fails somewhere else entirely and much later.

Read `outcome.fault_key` on the journal entry to see which lane actually served a request:

```json
{"outcome":{"kind":"scenario","fault_key":"demo/perplexity:completions|body_json:model=sonar","attempt_index":0}}
```

The parts are the namespace, the route's fault key, and one segment per `turn_key` extractor that resolved. If the
discriminator you expected is missing from that string, look for a warning on the same entry:

```json
{"severity":"warning","code":"scenario.turn_key_unresolved","field":"turn_key",
 "message":"turn_key extractor \"body_json:model\" resolved nothing on this request; the lane is named by the route and the extractors that did"}
```

An extractor resolves nothing when the path is absent from the body, lands on an object or an array rather than a
scalar, is JSON `null`, names an absent header, or yields a value longer than 128 bytes. The request is still
served — loudly, rather than silently in a lane you did not intend. Fix the extractor, or add the discriminator to
the request. See [`turn_key`](scenario-schema.md#turn_key--what-the-cursor-counts-per).

## Two tests interfere with each other

Servicesim's default and recommended concurrency boundary is **one simulator per test** — in Go,
`testkit.Start(t, testkit.WithProfiles(...))` per test, which is cheap because it is in-process.

If you are sharing one container across tests, isolate them with a namespace prefix on the base URL:

```text
EXA_BASE_URL=http://servicesim:8081/n/${TEST_ID}
```

Each namespace gets its own fault attempt counters, turn cursors and journal sequence numbers. Without one, two
concurrent tests share a lane: a scenario that fails the first attempt then succeeds will have its plan consumed
once *between* them, and one test sees an unexpected success.

Namespaces isolate **state**, not behaviour. Two tests needing different responses want two scenarios, selected
with `/x/<scenario>` from a `--scenario-dir`; two tests needing the same behaviour with independent state want two
namespaces. `testkit.AssertNamespacesIsolated` asserts the property rather than assuming it.

## A retry test passes locally and fails in CI, or vice versa

Fault plans are consumed per lane, and a lane persists for the life of the process. If a previous test in the same
process already consumed the "fail once, then succeed" plan, the next one starts at the success. Either use a fresh
simulator per test or a distinct namespace.

`POST /__admin/reset` exists for local iteration and requires an explicit scope — see
[the 400 above](#post-__adminreset-came-back-400).

## Counts and cursors differ per run, or a fault fires twice

Check how many Servicesim **replicas** are behind that base URL. If the answer is more than one, that is the bug,
and it is the only failure in this document that Servicesim cannot tell you about itself.

All lane state — fault attempt counters, turn cursors, async job records, journal entries and their sequence
numbers, and the namespace registry `--max-namespaces` bounds — is held in the serving process's memory. It is
never shared, replicated or persisted. **Servicesim is single-replica by design.** Namespaces isolate lanes
*within* one process; they do nothing across processes.

Two replicas therefore do not share a sequence, they run two of them, and each request is counted only by the
replica that received it. The five ways that surfaces:

| Symptom | What is happening |
|---|---|
| A scripted loop gets the same turn twice, or skips one | Each replica keeps its own turn cursor, so both hand out `call_index: 0` and neither ever reaches the turn scripted for call 1. |
| A "fail once, then succeed" retry test sees two failures | Each replica holds a complete copy of the attempt plan, so the 429 is served once per replica before either gets to its 200. |
| `testkit.AssertRequestCount` is short, by a number that changes per run | The journal read reaches one replica and sees only that replica's share of the traffic. |
| A scoped reset does not reset | It clears the replica that answered the reset call. The others keep their cursors and their journal. |
| A poll 404s for a job the create just made — "polls 404 intermittently" | Each replica holds its own job registry, so a poll landing on a different replica than its create finds no record and answers the vendor's 404 for a job that exists. See [the section above](#a-poll-returns-404-for-a-job-the-create-just-returned). |

Every row but the last is silent by design: nothing is logged, no finding is raised, and the journal looks
internally consistent on whichever replica you happen to ask. The job-poll row carries one unconditional signal and
one conditional one, where the other four carry none. First, the process logs `servicesim.single_replica_required`
once, unconditionally, at startup — before any request can hit this — naming the exact constraint. Second, a poll
of a well-formed identifier raises the `job.foreign_id` **warning** finding described above, but only if the
*polling* replica has itself already minted at least one job in this namespace — its own registry is what the
check reads. A poll that diverges to a replica that has minted nothing in this namespace yet — the common shape
with per-test namespaces used once — gets no `job.foreign_id` finding either, and the startup log is what is left
to lean on.

Neither signal changes the response, which is still the vendor's ordinary 404, and neither can tell a divergent
replica apart from a stale or hand-written fixture id — that ambiguity is inherent, not a gap in the diagnostic.

What makes the other four rows expensive is that **nothing reports them**. Every response is a well-shaped 200, no
finding is raised, no warning is logged, and the journal is internally consistent on whichever replica you happen
to ask — because from inside one process nothing went wrong. The failure surfaces as an assertion about *your*
client's behaviour, so the natural reading is that the client has a bug it does not have.

A replica cannot detect a sibling, so there is no check to switch on. Pin the count: `replicas: 1` in a
Kubernetes Deployment, `deploy.replicas: 1` in Compose, one container in CI. If you need more capacity or more
isolation, run more *simulators* — a process per suite, or namespaces within one process — rather than more
replicas of one.

## Two responses have the same `requestId`

Identifiers derive from stable fixture keys rather than a clock or a random source, and the tuple they hang off
includes the call's position within its state lane. So two successive calls get different identifiers, the way a
real vendor's do, while the same request at the same call position always renders the same bytes — which is what
makes golden-file assertions possible. A fresh lane (a new namespace, a new process, a scoped
`POST /__admin/reset`) starts at call 0 again and reproduces the first identifier exactly.

Two cases legitimately repeat an identifier:

- **The scenario pins it.** `request_id:`, `completion_id:`, `response_id:` and `message_id:` override the derived
  value, so every call carrying that projection renders the pinned string. Remove the key to get derived ones.
- **The responses are rejections.** A request refused by routing, authentication or validation must not consume a
  fault attempt, and claiming the call index is what consuming one means — so a rejected request has no call
  position to derive from. Its identifier is the tuple with no index, which is distinct from every served response
  and shared with the other rejections in its lane.

Golden helpers can account for this, but only when told to: `testkit.AssertGoldenJSON` prunes nothing by
default, so pass `testkit.GoldenDerivedIDs("requestId", "request_id", "id")` — or, sourced from the registered
profile rather than named by hand, `testkit.GoldenDerivedIDs(sim.DerivedIDs()...)` — and `testkit.GoldenExactIDs()`
opts back out of whatever `GoldenDerivedIDs` named.

## `task check` fails on revive but the code looks fine

revive runs with `warningCode = 1`, so warnings fail the build. The usual causes are a missing doc comment on an
exported symbol, a missing package comment in `doc.go`, an initialism that should be capitalised (`ID`, `URL`,
`API`, `JSON`, `HTTP`), or an unused parameter that should be named `_`.

Note that revive exits 0 even when it fails to *load* a package, so a clean revive run is not by itself proof the
package was linted. `go build ./...` and `go vet ./...` do catch that case, which is why `task lint` runs all three.

## MCP: `400` with `-32020` "required headers were not sent"

The MCP listener speaks the modern, stateless protocol revision `2026-07-28` only. This body is what a **legacy
client** gets — one that opens with `initialize` and no per-request headers, which is any SDK below go-sdk
`v1.7.0`, the TypeScript `@modelcontextprotocol/*@2.0.0` packages, or python-sdk `v2.0.0`:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32020,
 "message":"Header mismatch: this server speaks protocol version(s) [2026-07-28] only; required headers were not sent"}}
```

The journal carries `mcp.header.required` (error) and `mcp.legacy.initialize` (warning). Upgrade the client — every
current official SDK sends `2026-07-28` by default — or ask for the legacy `2025-11-25` era as a follow-on unit
(decision D11 in [`docs/adopter-backlog.md`](adopter-backlog.md)); this build does not serve it. A **hand-rolled**
client that simply forgot a header gets the same code with the header named:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32020,"message":"required header(s) missing: Mcp-Method"}}
```

Every request needs `MCP-Protocol-Version: 2026-07-28` and `Mcp-Method: <the body's method>`; `tools/call` also
needs `Mcp-Name: <params.name>`. `-32020` is also what a header that *disagrees* with the body draws
(`mcp.header.mismatch`) — `Mcp-Method` is compared case-sensitively against `method`, and `Mcp-Name` is decoded
through the Base64 sentinel before being compared against `params.name`.

## MCP: `400` with `-32022` "Unsupported protocol version"

The header and the body agree on a version, and it is not one this build speaks. The body says what to send:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"Unsupported protocol version",
 "data":{"requested":"2025-11-25","supported":["2026-07-28"]}}}
```

Send `2026-07-28` in both `MCP-Protocol-Version` and `_meta["io.modelcontextprotocol/protocolVersion"]`. Finding:
`mcp.version.unsupported`.

## MCP: `405` on a `GET` (or `DELETE`) to `/mcp`

```text
HTTP/1.1 405 Method Not Allowed
Allow: POST

{"jsonrpc":"2.0","error":{"code":-32600,"message":"the MCP endpoint accepts POST only"}}
```

Your client is opening the deprecated HTTP+SSE transport (a `GET` first, expecting an `endpoint` event) or a
legacy standalone GET stream, or terminating a session with `DELETE`. None of those exists in `2026-07-28`; the
specification SHOULDs exactly this `405`. Every message is its own `POST`.

## MCP: `202` and an empty body

You sent a **notification** — a JSON-RPC message with no `id` member. The server accepts it and, as the
specification requires, answers `202 Accepted` with no body; nothing about the notification's own method is
checked, because the modern core defines no client-to-server notification over HTTP. Journal label:
`mcp.notification.accepted`. If you meant to send a request, add an `id` (a string or an integer — `null` is a
`400` `-32600`, since MCP forbids it).

## MCP: `200` with `-32602` "Unknown tool: …"

```json
{"jsonrpc":"2.0","id":7,"error":{"code":-32602,"message":"Unknown tool: nope"}}
```

The request was well-formed — that is why it is a `200`, not a `400` — and the tool is not in the scenario's
`mcp` block: neither declared under `tools:` nor scripted under `results:`. Call `tools/list` and use a name it
returns; if you are writing the scenario, script the outcome under `results:` (a tool declared under `tools:` but
not scripted answers `isError: true` with one text block saying so, and warns `mcp.tool.unscripted`). Do **not**
retry a `-32602`: it will not change. Finding: `mcp.tool.unknown` (warning — the request itself was fine).

The same code at `400` means something else: a missing required `_meta` field —

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32602,
 "message":"missing required _meta field(s): io.modelcontextprotocol/protocolVersion, io.modelcontextprotocol/clientCapabilities"}}
```

Every request's `params._meta` must carry `io.modelcontextprotocol/protocolVersion` and
`io.modelcontextprotocol/clientCapabilities` (an empty object is fine). Finding: `mcp.meta.required`.
`io.modelcontextprotocol/clientInfo` is only a SHOULD, but leaving it out draws the warning
`mcp.meta.client_info_missing`, which `testkit.AssertNoFindings` will report — send it.

## MCP: I got a `401`, and I thought credentials were optional

They are, by default — the specification leaves authentication to the deployment, so a scenario with no `auth:`
on its `mcp` block accepts a request with no credential at all. This body means the scenario opted in:

```json
{"jsonrpc":"2.0","error":{"code":-32600,"message":"authorization required"}}
```

Either `auth: {mode: required}` (send `Authorization: Bearer <anything>`; with `expect_key` it must be that key —
the `credential-rotation` built-in does this) or `auth: {mode: reject}` (every credential is refused; the
`unauthorized` built-in). No `id` member, no `WWW-Authenticate` challenge — this profile does not half-implement
OAuth. Findings: `mcp.auth.missing` or `mcp.auth.mismatch`.

## MCP: my `tools/call` answer came back as `text/event-stream`

That is the scenario's decision, not the client's. When the entry's `mcp` block scripts a stream —
`stream: {when_requested: stream, deltas: [...]}`, or just `deltas:` — every `tools/call` is answered as SSE
(`server/discover` and `tools/list` never are), because MCP has no request-side field that asks for a stream;
the server decides. What you receive:

```text
Content-Type: text/event-stream
Cache-Control: no-cache
X-Accel-Buffering: no

data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"p1","progress":1,"total":2,"message":"searching…"}}

data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"p1","progress":2,"total":2,"message":"ranking…"}}

data: {"jsonrpc":"2.0","id":3,"result":{"resultType":"complete","content":[{"type":"text","text":"Full source text of Report A."}],"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"servicesim","version":"1"}}}}
```

Unnamed `data:` frames, no `event:`, no `id:`, no `[DONE]`; the last frame *is* the JSON-RPC response, byte for
byte what the non-streaming answer would have been. The `notifications/progress` frames appear only when your
request carried `_meta.progressToken` — without one, the stream is the response frame alone. A conformant client
handles both content types on every `POST` (the specification's MUST); if yours does not yet, run against a
scenario whose `mcp` block does not script a stream (`builtin:happy`), or read the built-in `streaming` scenario's
`mcp:` block for the shape to parse.

## MCP: the journal warns `mcp.header.param_ignored`

You sent an `Mcp-Param-<name>` header. The `x-mcp-header` extension that would make a server expect one is not
honoured in this build: any `Mcp-Param-*` header is ignored with this warning, and a scenario tool whose
`input_schema` declares `x-mcp-header` is rejected at load (`mcp.tool.x_mcp_header_unsupported`), so no scenario
can believe it is validated. The request is otherwise served normally. If the header carried a credential-shaped
name (`Mcp-Param-Token`), the journal masks its value as it would the bare `Token` header.

## MCP: `400` with `-32600` and the body was one JSON-RPC request

Two causes, both header-side. The `Accept` header must list **both** `application/json` and `text/event-stream`
(the specification's MUST — the server, not the client, chooses which one answers), and the request
`Content-Type` must be `application/json`:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"the Accept header must list both application/json and text/event-stream"}}
```

Findings: `mcp.request.accept_invalid`, `mcp.request.content_type_invalid`. The same code with no `id` and
"request body is not a JSON-RPC request or notification" means the body was not a single JSON-RPC object — a
top-level array is the usual cause: JSON-RPC batching was removed from MCP in `2025-06-18`, and every message is
its own `POST`.

## Something else

Open an issue with the journal entry for the failing request. It contains the method, path, provider, route,
sanitised headers, parsed body, the response or fault selected, and every validation finding — which is almost
always enough to diagnose without a reproduction. It contains no credential values, so it is safe to paste.
