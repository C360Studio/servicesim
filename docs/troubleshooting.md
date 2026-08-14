# Troubleshooting

Every symptom here is something Servicesim tells you about — usually in the request journal, which is the first
place to look and the place people forget.

```bash
curl -s 'http://localhost:8080/__admin/requests?pretty=1' | less
```

In a Go test the same data is `sim.Requests(provider.Exa)`, and `testkit.AssertNoErrors(t, entry)` turns "my
adapter sent a valid request" into an assertion instead of a hope.

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

## The container will not become ready

Readiness only succeeds after the scenario has loaded, validated and resolved. A scenario error therefore shows up
as "never becomes healthy" rather than as a failing request. Read the logs:

```bash
docker logs <container> 2>&1 | head -20
```

A validation failure names the file, the key and the reason. Frequent causes: a `source:` reference that no
`sources:` entry declares, a scenario `version:` this build does not understand, or a projection field whose type
does not match the contract.

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

## A retry test passes locally and fails in CI, or vice versa

Fault plans are consumed per lane, and a lane persists for the life of the process. If a previous test in the same
process already consumed the "fail once, then succeed" plan, the next one starts at the success. Either use a fresh
simulator per test or a distinct namespace.

`POST /__admin/reset` exists for local iteration and requires either `?namespace=<name>` or an explicit `?all=true`
— a bare reset is refused because, on a shared container, silently clearing every lane would destroy other tests
that are mid-run.

## Two responses have the same `requestId`

Identifiers derive from stable fixture keys rather than a clock or a random source, and the tuple they hang off
includes the call's position within its state lane. So two successive calls get different identifiers, the way a
real vendor's do, while the same request at the same call position always renders the same bytes — which is what
makes golden-file assertions possible. A fresh lane (a new namespace, a new process, `POST /__admin/reset`) starts
at call 0 again and reproduces the first identifier exactly.

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
