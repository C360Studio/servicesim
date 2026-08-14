# Troubleshooting

Organised by **symptom**, because that is how you arrive here.

Almost everything below is something Servicesim already told you about, in the request journal — the first place
to look and the place people forget:

```bash
curl -s 'http://localhost:8080/__admin/requests?pretty=1' | less
```

In a Go test the same data is `sim.Requests(provider.Exa)`, and `testkit.AssertNoErrors(t, entry)` turns "my
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
| Tavily | `Authorization: Bearer` only |
| Perplexity | `Authorization: Bearer` only |

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
| 8081 | exa | `POST /search`, `POST /answer` |
| 8082 | tavily | `POST /search` |
| 8083 | perplexity | `POST /v1/sonar`, `POST /chat/completions`, `POST /v1/agent`, `POST /v1/responses` |

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
2. Free the ones you are finished with: `POST /__admin/reset?namespace=<name>` drops that lane's journal entries
   and fault counters **and releases its slot**.
3. Raise `--max-namespaces`.

`GET /__admin/namespaces` shows every live lane, including ones holding no entries, because those still count
against the bound:

```json
{"namespaces":[{"namespace":"a","entries":1},{"namespace":"b","entries":1},{"namespace":"default","entries":8}]}
```

The `default` namespace never consumes budget — it is the lane every unprefixed request has always been served in,
so counting it would make `--max-namespaces=1` mean "no namespaces at all".

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

Servicesim's default and recommended concurrency boundary is **one simulator per test** — in Go, `testkit.Start(t)`
per test, which is cheap because it is in-process.

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

Golden helpers already account for this: `testkit.AssertGoldenJSON` ignores `requestId`, `request_id` and the
top-level `id` unless you pass `testkit.GoldenExactIDs()`.

## `task check` fails on revive but the code looks fine

revive runs with `warningCode = 1`, so warnings fail the build. The usual causes are a missing doc comment on an
exported symbol, a missing package comment in `doc.go`, an initialism that should be capitalised (`ID`, `URL`,
`API`, `JSON`, `HTTP`), or an unused parameter that should be named `_`.

Note that revive exits 0 even when it fails to *load* a package, so a clean revive run is not by itself proof the
package was linted. `go build ./...` and `go vet ./...` do catch that case, which is why `task lint` runs all three.

## Something else

Open an issue with the journal entry for the failing request. It contains the method, path, provider, route,
sanitised headers, parsed body, the response or fault selected, and every validation finding — which is almost
always enough to diagnose without a reproduction. It contains no credential values, so it is safe to paste.
