# Servicesim

A deterministic service-simulator framework — one binary, one image, one listener per provider profile — shipping
four profiles out of the box: three research APIs — **Exa**, **Tavily**, **Perplexity** — and a **Model Context
Protocol** server (Streamable HTTP, revision 2026-07-28).

Point your code's base URLs at it instead of the real vendors. Your tests then run *fast, offline and for free* —
and, more importantly, they can prove your client sent the **correct vendor request**, not merely that it got a
response back.

- **Deterministic.** The same scenario and the same request at the same call position produce byte-identical
  responses, identifiers and ordering. No clocks, no randomness, no UUIDs on a response path.
- **Strict about requests, tolerant about responses.** Method, route, content type, credential placement, required
  fields, field types and enum values are all validated, and every finding lands in a redacted request journal.
- **Fails closed.** An unmatched method, path, provider or scenario returns a provider-shaped error. Servicesim
  never dials outward, so a mis-set base URL can never quietly reach a real paid API.
- **Credential-safe.** `Authorization`, `x-api-key`, credential-shaped JSON properties and headers that mirror a
  credential under a wrapper name (MCP's `Mcp-Param-Token`, `Mcp-Session-Id`) are redacted *before* anything is
  retained — in the journal, in logs and in error messages. There is no flag that turns it off.

It is not a proxy, a ranking engine or a fake LLM. It does not generate answers from arbitrary input and does not
implement every field of every vendor — only the *consumed contract*.

What is provider-neutral and what is not: the scenario schema and turn model, the fault engine and its catalogue,
the redacted journal and admin surface, `testkit`, the built-in scenarios mechanism and the image are the
framework — the same for every profile. A profile is one provider package (`profiles/exa`, `profiles/tavily`,
`profiles/perplexity`, `profiles/mcp`) plus its own verified contract, embedded beside it
(`profiles/<name>/contracts/`); that is where a vendor's
routes, request validation and wire shapes live, and it is the part that grows when a profile is added. The fourth
profile is a protocol, not a vendor — an MCP server whose contract is the specification and its machine-readable
schema — and it needed no change to the scenario schema, the fault engine, the journal, the stream path or any
`testkit` assertion: only the mechanical registration every profile does, plus one redaction hardening
(`Mcp-Param-*` and `Mcp-Session-Id` are now masked as the names they mirror). That is the evidence
[`docs/design/mcp-profile.md`](docs/design/mcp-profile.md) records for the framework question.

## 60-second quickstart

No account, no credentials, no configuration, and nothing to clone:

```bash
docker run --rm -p 8080-8084:8080-8084 ghcr.io/c360studio/servicesim:v0.4.0
```

The image is public and multi-architecture (`linux/amd64`, `linux/arm64`). Tags are published in both spellings
against one digest — `v0.4.0` and `0.4.0`, `v0.4` and `0.4`, `v0` and `0` — plus `latest` and `sha-<commit>`.

For release-critical CI, pin the digest rather than a tag, so a re-publish cannot move under you — this is the
`v0.4.0` digest:

```text
ghcr.io/c360studio/servicesim@sha256:5a7d6d055fa4d6f9662d538823e8f9274b28416fb415b152a9da39f22f03c08f
```

Working on Servicesim itself, or want the tip of `main`? `task image:build` produces `servicesim:dev` locally, and
every example below works the same with that tag substituted. The MCP listener (`:8084`) ships in v0.5.0; on
`v0.4.0` that port is not bound and `go get` resolves to a module with no MCP profile at all — until the tag
lands, use `servicesim:dev` or a `replace` directive on this repository for the MCP examples below.

In another terminal, ask Exa's listener for a search. Any fake key works:

```bash
curl -s -X POST localhost:8081/search \
  -H 'content-type: application/json' \
  -H 'x-api-key: any-fake-value' \
  -d '{"query":"report a","numResults":1}'
```

```json
{"requestId":"beed719d411ffc4ea098f2424fd57b9e","results":[{"title":"Report A",
 "url":"https://example.test/report-a","id":"https://example.test/report-a",
 "publishedDate":"2026-05-20T00:00:00.000Z","author":"A. Author",
 "text":"Full source text of Report A.","highlights":["A relevant excerpt from Report A."],
 "highlightScores":[0.82],"summary":"Report A summarised in one sentence.",
 "image":"https://example.test/report-a.png","favicon":"https://example.test/favicon.ico"}],
 "costDollars":{"total":0.005,"search":{"neural":0.005}}}
```

That is the real Exa wire shape, from the built-in `happy` scenario. Run the same command again and the body is
identical except for `requestId`, which advances with the call position the way a real vendor's does.

Now look at what the simulator saw:

```bash
curl -s 'http://localhost:8080/__admin/requests?pretty=1'
```

The journal is the point of the whole tool: it records the method, route, credential *placement* (never the
credential), body and every validation finding raised against the request. Asserting on it is how a test proves the
client is correct rather than merely lucky.

## Listeners

Each provider gets its own listener. That is not a stylistic choice: Exa and Tavily both serve `POST /search`, so
preserving each vendor's real path requires a separate port per provider — and once that is the rule, the fourth
listener (MCP, one route, JSON-RPC methods dispatched on the body) follows it rather than sharing a port.

| Listener | Port | Routes |
|---|---:|---|
| admin | `8080` | `GET /healthz`, `GET /readyz`, `GET /__admin/requests`, `GET /__admin/namespaces`, `GET /__admin/scenario`, `GET /__admin/jobs`, `POST /__admin/reset` |
| exa | `8081` | `POST /search`, `POST /answer`, `POST /contents`, `POST /findSimilar`, `POST /agent/runs`, `GET /agent/runs/{id}`, `HEAD /agent/runs/{id}` |
| tavily | `8082` | `POST /search`, `POST /extract`, `POST /research`, `GET /research/{request_id}`, `HEAD /research/{request_id}` |
| perplexity | `8083` | `POST /v1/sonar`, `POST /chat/completions`, `POST /v1/chat/completions`, `POST /v1/agent`, `POST /v1/responses`, `POST /responses` |
| mcp | `8084` | `POST /mcp` |

`/agent/runs` and `/research` are the asynchronous create-then-poll surfaces — Exa agent runs and Tavily research
— covered in [the testkit section below](#in-a-go-test-with-no-container-at-all); their `HEAD` route answers an
existence check without advancing the poll's own turn cursor. On Perplexity, each surface is served under three
spellings: its canonical vendor path, and both of the paths the OpenAI SDK produces when it appends
`/chat/completions` or `/responses` to whatever `base_url` a consumer configured — a `base_url` that may or may not
already end in `/v1`. All three spellings of a surface share one handler, one wire shape and one fault budget, and
the journal records which path was actually used. Sonar is supported by Perplexity until 2026-09-27; `/v1/agent` is
its successor and both surfaces are simulated.

Both Perplexity surfaces can also serve `text/event-stream`: a scenario turn scripts `stream: {when_requested:
stream, deltas: [...]}` and a request that sets `"stream": true` gets the real SSE dialect back — unnamed
`data:` chunks closed by `data: [DONE]` on Sonar, named `event:` frames on the Agent API — instead of the
default `warn` (an ordinary JSON body plus a journal finding) or an opt-in `reject` (a provider-shaped 4xx). Exa
and Tavily do not stream. MCP's `tools/call` streams too, unconditionally under a `stream: {when_requested:
stream}` script — there is no request-side field that asks for it, unlike Perplexity's `"stream": true` — with
`notifications/progress` frames sent only when the request itself carried `_meta.progressToken`. See
[`docs/scenario-schema.md`](docs/scenario-schema.md#streaming-stream) for the scripting grammar and the built-in
`streaming` scenario for a worked example.

Every port is configurable (`-exa-port`, `-tavily-port`, `-perplexity-port`, `-mcp-port`, `-admin-port`), and `-providers`
limits which listeners bind. Run `servicesim -h` for the full flag list. Go's `flag` package treats a single dash
and a double dash the same, so `-scenario` and `--scenario` are identical; this document uses whichever spelling
reads better in context.

## Pointing your code at it

Inject a **base URL** rather than mocking an HTTP client. That keeps your own transport, retry and decode paths
inside the test, which is where the bugs live:

| Variable | Points at | Value under Compose |
|---|---|---|
| `EXA_BASE_URL` | Exa listener | `http://servicesim:8081` |
| `TAVILY_BASE_URL` | Tavily listener | `http://servicesim:8082` |
| `PERPLEXITY_BASE_URL` | Perplexity listener | `http://servicesim:8083` |
| `MCP_BASE_URL` | MCP listener | `http://servicesim:8084` |

Credentials are required by default on the three research surfaces — the point is to prove your client *sends*
one, in the right place. MCP's default is the opposite (the specification leaves authentication to the
deployment): no credential is required unless the scenario opts in with `auth: {mode: required}`. Any fake value
works. Servicesim never stores it; the journal keeps only a fingerprint.

| Provider | Accepted credential placement |
|---|---|
| Exa | `x-api-key`, or `Authorization: Bearer` |
| Tavily | `Authorization: Bearer` |
| Perplexity | `Authorization: Bearer` |
| MCP | `Authorization: Bearer` — optional by default; a scenario's `auth: {mode: required}` makes it mandatory |

Tavily's `POST /search` and `POST /research` also authenticate a body `api_key` property — real client code sends
it — and on both routes that draws the same warning-severity finding, `tavily.api_key.in_body`, rather than an
error. `Authorization: Bearer` is the placement to send.

### MCP

The MCP profile is a Streamable HTTP server speaking protocol revision `2026-07-28` — the modern, stateless era:
no `initialize` handshake, no session header, every request carries its own protocol version and capabilities.
Point your MCP client at `MCP_BASE_URL`; the endpoint is `POST /mcp` under it. A conformant request carries:

- `Content-Type: application/json` and `Accept: application/json, text/event-stream` (both media types — the
  server, not the client, decides whether an answer is a JSON object or an SSE stream);
- `MCP-Protocol-Version: 2026-07-28`, `Mcp-Method: <method>` (identical to the body's `method`), and on
  `tools/call` an `Mcp-Name: <tool>` (identical to `params.name`; a name that is not header-safe travels Base64
  sentinel-encoded and is decoded before comparison) — `params.arguments` is an object and optional;
- in the body's `params._meta`: `io.modelcontextprotocol/protocolVersion` and
  `io.modelcontextprotocol/clientCapabilities` (required — `{}` is fine), and `io.modelcontextprotocol/clientInfo`
  (a SHOULD; leaving it out draws the warning-severity finding `mcp.meta.client_info_missing`).

Three methods are served — `server/discover`, `tools/list`, `tools/call`; any other method (`ping`,
`initialize`, `resources/*`, `prompts/*`) is `404` with `-32601` — and a well-formed notification (a body
with no `id`) is answered `202` with no body. Credentials are optional by default; a scenario that sets
`auth: {mode: required}` on its `mcp` block turns a missing `Authorization: Bearer` into a `401` with a JSON-RPC
error body. The answer to `tools/call` may be `text/event-stream` when the scenario's `mcp` block scripts a
`stream:` — one `notifications/progress` frame per scripted delta, and only when your request carried
`_meta.progressToken`, then the final JSON-RPC response frame; `server/discover` and `tools/list` never stream.

Your retry logic needs to know four statuses, because the JSON-RPC code alone cannot tell them apart (`-32602`
appears at both `200` and `400`, `-32603` at `200` and on every scripted fault):

| What arrived | What it means | Retry? |
|---|---|---|
| `200` with a JSON-RPC `error` (`-32602` unknown tool or invalid cursor, `-32603` render failure) | The request was well-formed and the method answered it with an error. | No. |
| `400` with `-32020` or `-32022`; `-32602`; `-32700` or `-32600` | Your client sent a non-conformant request. `-32020`/`-32022`: a required header is missing, disagrees with the body, or names the wrong protocol era (`-32022`'s `data.supported` says what to send). `-32602`: a required `_meta` field is missing. `-32700`/`-32600`: a body that is not a single JSON-RPC request, or an `Accept`/`Content-Type` that is not the pair above. | No; fix the client. |
| `404` with `-32601` | A method this profile does not serve (`ping`, `initialize`, `resources/*`, `prompts/*`), or a path other than `/mcp`. Not a wrong base URL to retry against. | No. |
| A scripted `429`/`503`/`500` (`rate-limited`, `brownout`, `server-error`) | Arrives as `{"jsonrpc":"2.0","id":…,"error":{"code":-32603,"message":…}}` — the same code an unrelated internal error would carry. Classify by HTTP status, never by code. | Yes, by status. |

Ask a running simulator (`--scenario builtin:happy`) what it is:

```bash
curl -s -X POST localhost:8084/mcp \
  -H 'content-type: application/json' \
  -H 'accept: application/json, text/event-stream' \
  -H 'mcp-protocol-version: 2026-07-28' \
  -H 'mcp-method: server/discover' \
  -d '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}'
```

```json
{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","supportedVersions":["2026-07-28"],
 "capabilities":{"tools":{}},"instructions":"Search the evaluation corpus, or fetch one report by URL.",
 "ttlMs":60000,"cacheScope":"private","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"servicesim","version":"1"}}}}
```

That is the body (one line on the wire, wrapped here for width), and it is byte-identical on every call:
`serverInfo` is a pair of Go constants, and the `id` is yours, echoed as the raw token you sent. The journal
entry for it carries one warning — `mcp.meta.client_info_missing`, because the curl sent no `clientInfo` — which
is the profile telling you the request was accepted but was not the request a careful client sends. Every wire
field — the response shapes `tools[]`, `content[]`, `structuredContent`, `isError`, `ttlMs`/`cacheScope`
included — is recorded in [`profiles/mcp/contracts/README.md`](profiles/mcp/contracts/README.md), and
[`examples/mcpclient.go`](examples/mcpclient.go) declares the consumed subset as Go types; every simulator-chosen
default (what the specification left open) is numbered in `profiles/mcp/doc.go`.

## In a Go test, with no container at all

Requires Go 1.26, this repository's own `go.mod` version. Add it as a dependency —
`go get github.com/c360studio/servicesim` — then import `github.com/c360studio/servicesim/testkit` and
`github.com/c360studio/servicesim/provider`, plus the profile package(s) your test simulates, for example
`github.com/c360studio/servicesim/profiles/exa`.

`testkit` starts one `httptest.Server` per provider in-process and registers its own cleanup, so there is nothing
to defer, no port to pick and no Docker to wait for.

The worked example lives in [`examples/`](examples) and is **compiled by `go build ./...` and run by
`go test -race ./...` in this repository's CI** — an example that is not built is an example that rots. Copy from
there rather than from this README (the example uses testify's `require`; `testkit` itself needs only the
standard library). This is the shape of it, excerpted from
[`examples/adapter_test.go`](examples/adapter_test.go):

```go
func TestAdapterSendsACorrectExaRequest(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(exa.Profile()), testkit.WithBuiltin("happy"))
	adapter := newAdapter(sim.Client(), sim.BaseURLs())

	results, err := adapter.SearchExa(t.Context(), "report a")
	require.NoError(t, err)
	require.Len(t, results, 2, "the happy scenario projects two canonical sources through Exa")

	testkit.AssertRequestCount(t, sim, exa.Name, 1)
	entry := sim.Requests(exa.Name)[0]

	testkit.AssertAPIKeyHeader(t, entry)
	testkit.AssertJSONBody(t, entry, map[string]any{
		"query":      "report a",
		"numResults": 5,
	})
	testkit.AssertNoErrors(t, entry)
	testkit.AssertNoCredentialLeak(t, sim, exaKey, tavilyKey, perplexityKey)
}
```

`testkit.WithProfiles` names the simulated APIs a test needs — required, not defaulted, so a team simulating one
vendor never pulls in every reference profile's contracts and goldens. The four in-tree profiles live at
`profiles/exa`, `profiles/tavily`, `profiles/perplexity` and `profiles/mcp`, each exporting a typed `Name` and a
`Profile()`.

`sim.Client()` returns an `*http.Client` with keep-alives disabled and the proxy environment ignored, so a
connection-abort fault is observed rather than absorbed by a pooled connection — the property the `hang-then-abort`
and `timeout` built-ins below rely on. Any `http.Client` reaches the simulator; reach for `sim.Client()` when that
property matters. Each journal entry is a `testkit.Entry` — an alias, so the fields are listed by
`go doc github.com/c360studio/servicesim/internal/journal Entry` (and `Finding`): the ones a test touches are
`Method`, `Path`, `Headers map[string][]string`, `Body json.RawMessage`, `Auth` and `Findings []Finding`, each
finding a `Severity`, `Code`, `Field` and `Message`. `testkit.AssertAPIKeyHeader` above is Exa's
credential-placement check, and `testkit.AssertBearerAuth(t, entry)` is its Tavily/Perplexity sibling.
`testkit.AssertJSONBody` compares the whole decoded body, not a subset.

Note what it does *not* assert: that the call returned 200. A simulator will happily return 200 for a request the
real vendor would reject, so a green status proves nothing. The assertions are on the journal.

The MCP profile's first test is the same shape. [`examples/mcpclient.go`](examples/mcpclient.go) is a minimal
Streamable HTTP client, stdlib-only, written the way a consuming team's adapter would be, and
[`examples/mcp_test.go`](examples/mcp_test.go) is the tests around it — starting with this one, which proves the
client sent the correct MCP request rather than merely got a `200`:

```go
func TestMCPClientSendsACorrectToolsCallRequest(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(mcp.Profile()), testkit.WithBuiltin("happy"))
	client := newMCPClient(sim.Client(), sim.BaseURLs(), examples.WithMCPBearerToken(mcpToken))

	resp, err := client.CallTool(t.Context(), "search", map[string]any{"query": "report a"})
	require.NoError(t, err)
	require.NotNil(t, resp.Result)
	assert.False(t, resp.Result.IsError)

	testkit.AssertRequestCount(t, sim, mcp.Name, 1)
	entry := sim.Requests(mcp.Name)[0]

	headers := http.Header(entry.Headers)
	assert.Equal(t, "2026-07-28", headers.Get("MCP-Protocol-Version"))
	assert.Equal(t, "tools/call", headers.Get("Mcp-Method"))
	assert.Equal(t, "search", headers.Get("Mcp-Name"))

	testkit.AssertBearerAuth(t, entry)
	testkit.AssertNoFindings(t, entry)
	testkit.AssertNoCredentialLeak(t, sim, mcpToken)
}
```

`testkit.AssertNoFindings` is stricter than `testkit.AssertNoErrors`: it fails on a warning too, which is what
proves this client sends the `clientInfo` the specification only SHOULDs. `newMCPClient` there is a small
helper reading `MCP_BASE_URL` out of `sim.BaseURLs()` — the same map a container's environment gives you. The
test reads headers rather than calling `testkit.AssertJSONBody` because an MCP body carries your own `id`,
`jsonrpc` and `_meta` alongside the fields you care about — decode `entry.Body` and assert the fields you own, or
assert the whole envelope.

For a client that rotates credentials, `testkit.AssertSameCredential(t, a, b)` and
`testkit.AssertDifferentCredential(t, a, b)` compare two journal entries by credential fingerprint rather than
value — the journal never holds a credential at all — so a test can prove a retry reused the same key, or that a
rotation actually switched to a new one, from two entries alone. The `credential-rotation` built-in scripts the
scenario both exist to test against.

`sim.BaseURLs()` returns the URLs keyed exactly as the environment variables above, so one helper configures your
client from either a `*testkit.Sim` or a container. `sim.URL(exa.Name)` returns one provider's base URL
directly, for a test that only needs the one. `testkit.WithScenarioYAML(yaml string)` keeps a single-purpose
fixture inline next to the test; `testkit.WithScenarioFile(path string)` loads one from disk, and
`testkit.WithScenario(s *scenario.Scenario)` takes one already parsed with the `scenario` package.

For the async create-then-poll surfaces (Exa agent runs, Tavily research), `sim.Jobs()` returns every live job
record across every namespace, and `testkit.AssertPollSequence(t, sim.Requests(exa.Name), id, 200, 200, 200)`
asserts, from the journal alone, that a job's polls arrived in order from its own per-job lane — pass
`ns.Requests(...)` when the test uses namespaces, because job identifiers repeat across namespaces by design.
`testkit.NewJobs()` is what a consumer wiring `provider.Deps` by hand passes as `Deps.Jobs`, and without it a
create still answers but no poll can ever resolve; `(*provider.Set).Faults(s)` is the equally hand-built
`Deps.Faults` — the only exported fault-engine constructor, built from whichever profiles the Set registers.

For a scripted SSE response, `testkit.AssertGoldenSSE(t, path, transcript)` regression-tests the reassembled
stream frame by frame — an SSE transcript is not JSON, and a byte-for-byte comparison would flake on TCP read
boundaries that are never deterministic, so a one-delta change reports as a one-frame diff rather than an
unreadable whole-file one. `sim.AwaitStreamClosed(t, entry.Seq)` is the wait for the fields that are only final
once the exchange closes (`bytes_written`, `chunks_sent`, `state`); everything else on `entry.Outcome.Stream` —
`testkit.AssertStreamPacing` included — is final before the client sees a byte and needs no wait at all.

For a client-side rate limiter, `testkit.AssertMaxRate(t, entries, limit, per)` and
`testkit.AssertMinGap(t, entries, gap)` are the request-level evidence decision D5, in
[`docs/adopter-backlog.md`](docs/adopter-backlog.md), chose over an enforced limiter: proving a budget held
from the journal's real `arrived_at` timestamps, rather than a simulator making
response status a function of wall-clock time. `testkit.AssertObservedDuration(t, e, atLeast)` is the
single-entry sibling, for proving a `delay:` or `delay_after_headers:` attempt was really observed. All three
are safe on a loaded machine in only one direction — real time can only spread arrivals out or lengthen a
duration, never manufacture a tighter or shorter one — so only a client that genuinely broke its budget, or a
hang that genuinely was not observed, fails them.

| Example file | What it shows |
|---|---|
| [`examples/adapter_test.go`](examples/adapter_test.go) | The canonical first test: prove the request was correct. |
| [`examples/fusion_test.go`](examples/fusion_test.go) | Three providers at once, deduplication, and a 429 classified as retryable. |
| [`examples/namespace_test.go`](examples/namespace_test.go) | Parallel subtests sharing one simulator, each in its own state lane. |
| [`examples/async_test.go`](examples/async_test.go) | Create-then-poll through `testkit`, and the same flow wired by hand through `provider.Deps`. |
| [`examples/stream_test.go`](examples/stream_test.go) | A scripted Perplexity SSE response: `testkit.AssertGoldenSSE` against a golden transcript, `testkit.AssertStreamPacing` before the first byte, `sim.AwaitStreamClosed` after the exchange closes. |
| [`examples/pacing_test.go`](examples/pacing_test.go) | A tiny client-side limiter proven against the journal with `testkit.AssertMaxRate` — decision D5's evidence-not-enforcement pattern. |
| [`examples/mcp_test.go`](examples/mcp_test.go) | The MCP profile through [`examples/mcpclient.go`](examples/mcpclient.go): the correct request proven from the journal, `tools/list` order and caching hints, `tools/call` content and `structuredContent`, an unknown tool that must not be retried, the hostile-content pack on MCP's own shapes, an SSE `tools/call` pinned with `testkit.AssertGoldenSSE`, a scripted 429 classified by status, parallel namespaces, and catalogue drift across turns. |

## As a container

Same handlers, same scenarios, reachable from any language.

```yaml
services:
  servicesim:
    image: ghcr.io/c360studio/servicesim:v0.4.0
    command: ["--scenario", "builtin:fusion-overlap"]

  app-tests:
    build: .
    depends_on: [servicesim]
    environment:
      EXA_BASE_URL: http://servicesim:8081
      TAVILY_BASE_URL: http://servicesim:8082
      PERPLEXITY_BASE_URL: http://servicesim:8083
      MCP_BASE_URL: http://servicesim:8084
      EXA_API_KEY: test-exa-key
      TAVILY_API_KEY: test-tavily-key
      PERPLEXITY_API_KEY: test-perplexity-key
```

[`docker-compose.example.yml`](docker-compose.example.yml) is the fuller version: a read-only scenario mount, a
healthcheck, and both namespaced base-URL forms described [below](#one-container-many-concurrent-tests). It pins a
published `ghcr.io` tag; swap in a digest for release-critical CI.

The image runs as a non-root user on a `scratch` base with no shell and no CA bundle — introspection goes through
the admin listener, not through `exec`.

## One container, many concurrent tests

**Process isolation is still the recommended default.** A separate process or container per parallel suite is the
simplest thing that works, and nothing below changes that. Namespaces exist because a survey of the repositories
this was built for found every e2e suite already sharing *one* mock container across many tests — so the choice was
not between two patterns but between the shared pattern being safe and it being silently wrong. Namespaces make it
safe; they do not make it the norm.

Two optional path prefixes are stripped before route matching, so a prefixed request reaches exactly the same
handler as a plain one:

```text
http://servicesim:8081/x/<scenario>/n/<namespace>/search
                       └─ selects behaviour   └─ isolates state
```

Both are optional and their order is fixed. `/search` on its own still means "the startup scenario, namespace
`default`", so nothing that works today changes. In a shell, that is one environment variable:

```bash
EXA_BASE_URL=http://servicesim:8081/n/${TEST_ID}
```

In a Go test it is one line inside the subtest — `sim.NamespaceFor(t)` derives a lane name from `t.Name()`, and the
returned `*testkit.Namespace` hands back the same environment-shaped map with the prefix already in every URL. The
code under test needs no change and no knowledge that namespaces exist:

```go
t.Run(name, func(t *testing.T) {
	t.Parallel()

	ns := sim.NamespaceFor(t)
	adapter := newAdapter(ns.Client(), ns.BaseURLs())
	// ns.Requests(exa.Name) sees this lane's traffic and nothing else.
})
```

[`examples/namespace_test.go`](examples/namespace_test.go) is the full worked version, ending in
`testkit.AssertNamespacesIsolated` — the property the whole pattern rests on, asserted rather than assumed.

**It is a path prefix rather than a header on purpose:** consumers already inject base URLs, and every SDK supports
a base URL carrying a path, whereas many LLM SDKs make a per-request header awkward or impossible.
`X-Servicesim-Namespace` is accepted for an SDK that pins the path, but the base URL is the documented mechanism
because it needs no code change at all.

A namespace is a **state** boundary, not a behaviour one:

| Isolated per namespace | Shared process-wide |
|---|---|
| Fault attempt counters | The loaded, validated scenarios |
| Turn cursors | Route tables and handlers |
| Journal entries and their scoping | Configuration and listeners |

Read the left column literally: that state is isolated per namespace *and held in one process's memory*. It is
not shared with a second replica of the same image — see [Single replica by design](#single-replica-by-design).

Behaviour is the separate `/x/<scenario>` dimension, served from `-scenario-dir`, because the common case is two
tests wanting the *same* behaviour with *independent* state. Every scenario in that directory is loaded and
validated at startup, so readiness still means "every scenario is valid" and nothing is loaded lazily.

The rest of it, briefly:

- Namespaces are created on first use. There is no registration call. A name is 1 to 64 characters of
  `[A-Za-z0-9_-]`; `provider.ValidNamespace` is the same check the server applies.
- `-max-namespaces` (default 1024) bounds them, and exceeding the bound is a **provider-shaped 503, never a silent
  success**: the refusal happens before the handler runs, so a test in a refused namespace fails loudly instead of
  reading a response that belongs to no lane. Evicting instead would reset a running test's turn cursor mid-loop.
  `-max-jobs` (default 256) is the same rule one level down: it bounds live async jobs per namespace and refuses
  rather than evicts, because an evicted job would turn a later poll into the vendor's 404 for a job the client
  successfully created.

  ```json
  {"requestId":"6c19404215fa0b2e878faa18675b4d90",
   "error":"namespace \"beta\" cannot be served: the process is at its --max-namespaces bound",
   "tag":"INTERNAL_ERROR"}
  ```

- Concurrency *within* one namespace is a separate problem with a separate answer: when one route serves several
  callers at once, key the turn cursor on something that tells them apart with
  [`turn_key`](docs/scenario-schema.md#turn_key--what-the-cursor-counts-per). The built-in `namespaced` scenario is
  the worked example.

## Single replica by design

**Run exactly one Servicesim process per base URL. Two replicas behind one address is silently wrong, not slow.**

Every piece of lane state — fault attempt counters, turn cursors, async job records, journal entries and their
sequence numbers, and the namespace registry `-max-namespaces` bounds — lives in that process's memory. Nothing is
shared, nothing is replicated and nothing is persisted. Namespaces isolate lanes *within* a process; they do not
join lanes *across* processes.

So a second replica does not halve the load, it forks the state. Requests from one client are spread across
replicas by whatever balances them, and each replica counts only the calls it happened to receive:

| What you scripted | What two replicas actually do |
|---|---|
| `call_index: 0`, then `call_index: 1` | Both replicas start at 0. The second call lands on the other replica and is served turn 0 again. |
| `attempts: [{status: 429}, {status: 200}]` | Each replica owns a full budget, so the 429 is served **twice** — once per replica — before either succeeds. |
| `AssertRequestCount(t, sim, exa.Name, 3)` | The journal read reaches one replica and sees only its share: 1 or 2, varying run to run. |
| `POST /__admin/reset?namespace=t1` | Resets the replica that answered. The others keep their cursors. |
| `POST /agent/runs` then `GET /agent/runs/{id}` (or Tavily's `/research` equivalent) | The create lands on one replica; if the poll lands on another, it holds no record of the job and answers the vendor's 404 for a job that exists — "polls 404 intermittently". |

**The symptom you would actually see** is a test suite that passes locally and fails intermittently in the
deployment that scaled out, with failures that never mention the simulator: a retry test that reports one retry
too many, an agentic-loop test that gets the first turn's answer twice and fails on an assertion about the second
turn's content, or a request-count assertion that is short by a number that changes on every run. Every response
involved is a well-shaped 200. Nothing is logged, no finding is raised and `GET /__admin/requests` looks
internally consistent on whichever replica you ask, because from inside one process nothing went wrong.

There is no detection for this, which is the honest part: a replica cannot tell that a sibling exists. Pin the
count instead — `replicas: 1` in a Kubernetes Deployment, `deploy.replicas: 1` in Compose, one container in CI.

If you need more throughput or more isolation, add **more simulators**, not more replicas of one: a separate
process or container per suite is the recommended default anyway, and namespaces cover the shared-container case
within a single process. Shared cross-process state is not on the roadmap — it would put a network hop and a
consistency model underneath a tool whose entire value is determinism.

## The admin surface

Read-only, apart from `reset`. It never reconfigures a running simulator: an admin API that could is hidden shared
state between concurrent tests.

| Endpoint | What it does |
|---|---|
| `GET /healthz` | Liveness. Answers as soon as the admin listener binds. |
| `GET /readyz` | Readiness: the scenario loaded and validated, every listener accepting. |
| `GET /__admin/requests` | The redacted journal. `?provider=`, `?namespace=`, `?limit=`, `?pretty=1`. |
| `GET /__admin/namespaces` | Every live state lane and how many entries it holds. |
| `GET /__admin/scenario` | What was loaded: name, version, seed, source count, and any validation warnings. |
| `GET /__admin/jobs` | Every live async job: `?namespace=`, `?pretty=1`. Read-only; no cursor or lane key is served. |
| `POST /__admin/reset` | Drops journal entries, zeroes fault attempt counters (which are the turn cursors), and drops async job records. |

`GET /__admin/namespaces` shows lanes that hold no entries too, because they still count against
`-max-namespaces` — which is exactly what you need to see when a namespace was refused:

```json
{"namespaces":[{"namespace":"alpha","entries":1},{"namespace":"default","entries":1}]}
```

`POST /__admin/reset` **requires an explicit scope**: `?namespace=<name>` to drop one lane, or `?all=true` to drop
every lane. A bare reset is a 400 on purpose — in a shared container, a reset that defaulted to "everything" lets
one test's cleanup zero a hundred other tests' cursors mid-run, and the resulting failure surfaces somewhere else
entirely and much later:

```json
{"error":"reset requires an explicit scope: either ?namespace=<name> to drop one namespace, or ?all=true to drop every namespace"}
```

## Built-in protocol scenarios

Twenty scenarios ship inside the binary. Select one with `--scenario builtin:<name>` — for example,
`docker run --rm -p 8080-8084:8080-8084 ghcr.io/c360studio/servicesim:v0.4.0 --scenario builtin:rate-limited` — or
`testkit.WithBuiltin("<name>")`. They cover *protocol* behaviour, which is the same for every consumer;
product-specific corpora belong in your own repository.

Several rows below tell you to read the journal with `sim.AwaitRequests` rather than a bare `sim.Requests` call:
`func (s *Sim) AwaitRequests(tb testing.TB, p provider.Name, n int) []Entry` blocks until `p` has recorded `n`
entries, or fails `tb` after a short deadline, and returns them — `entries := sim.AwaitRequests(t, exa.Name,
2)` is the shape. Use it instead of `sim.Requests` whenever a row below says the client sees the exchange end at
the transport level (a reset, a client-side timeout, an off-goroutine retry): the server goroutine can still be
completing the entry after your client already returned, and a bare `sim.Requests` call is a race that passes on a
slow machine and fails on a fast one.

| Scenario | What it proves about your client |
|---|---|
| `happy` | The baseline: a well-formed 200 from every provider parses into your own model. On MCP that is `server/discover`, a two-tool `tools/list`, and a `tools/call` carrying a text block, a `resource_link` and `structuredContent`. |
| `empty-results` | Zero results in a well-shaped envelope is handled as "no results", not as an error. On MCP the catalogue itself is empty — `tools/list` answers `"tools":[]` and any `tools/call` is the `-32602` unknown-tool error — so an empty server is handled as "no tools", not as a broken one. |
| `async-failed` | An Exa agent run and a Tavily research task each reach a terminal `failed` status — Exa's poll carries an `error` object with the detail, Tavily's carries none beyond the status itself — so your failure branch handles both shapes rather than mis-parsing either as success. |
| `async-stuck` | An Exa agent run and a Tavily research task never terminate, so your own poll timeout — not Servicesim — is what ends the loop. |
| `unauthorized` | A 401 in each vendor's own error envelope (on MCP, a JSON-RPC `-32600` body with no `id`) is surfaced as an auth failure and is not retried — every key is rejected (`mode: reject`), unlike `credential-rotation` below, where refreshing to the one accepted key and retrying once is exactly what must happen. |
| `rate-limited` | A 429 with `Retry-After`, then success — backoff and retry work, and the retry is counted. On MCP the 429 body is the JSON-RPC `-32603` fault envelope, so the retry decision has to key on the status. If your client retries off the calling goroutine, read the journal with `sim.AwaitRequests` rather than a bare `sim.Requests` call. |
| `oversized-body` | Every synchronous route (Exa `/search`, `/answer`, `/contents`, `/findSimilar`; Tavily `/search`, `/extract`; Perplexity Sonar and Agent; MCP `/mcp`) has its first response padded past a 1 MiB size-limit ingress gate (`body_bytes: 4194304`), then a clean retry — a fail-closed size gate and its recovery, from one scenario. |
| `timeout` | Every synchronous route hangs for 30s on its first call, then a clean retry — your own client deadline, not a status code, is what ends the first call. Assert the hang with `testkit.AssertObservedDuration`, and read the journal afterward with `sim.AwaitRequests`: the entry for a client-timed-out attempt is still being completed server-side the instant your client gives up. **Do not use `testkit.WithSkippedDelays()` with this scenario**: under `DelaySkip` the 30s is not slept and the "timeout" arrives as an instant 200, and a deadline is only ever observed by bytes not arriving. |
| `brownout` | Every synchronous route serves a rising-latency ladder (50ms, 100ms, 200ms, 400ms), then two 503s carrying `Retry-After`, then recovers — a latency budget or circuit breaker, a retry policy, and counted recovery, all from one scenario. `testkit.AssertObservedDuration` proves each rung's delay was really observed. |
| `hang-then-abort` | Every synchronous route hangs 700ms then resets before any header arrives, hangs 700ms again then resets mid-body, then serves headers immediately followed by a 700ms hang and a reset mid-body, then serves a clean call — the three abort shapes a mid-flight disconnect takes on the wire today, with every hang visible in `completed_at - arrived_at` (`testkit.AssertObservedDuration` reads that gap). Your client sees all three as transport failures (a round trip that never got a header, then twice a 200 whose body read fails mid-stream) — never as an empty result set — and each retry is counted. Every scripted reset here is journaled before the socket is touched, so `sim.Requests` already sees all three the instant your client returns — but read it with `sim.AwaitRequests` anyway: if your own deadline fires during the 700ms hang instead, that entry lands only after your client has already returned. Its own deadline must outlast 700ms, or a hang aborts on your side first and this degenerates into `timeout`. |
| `credential-rotation` | Every provider requires the fixed key `rotated-key-EXAMPLE` (MCP, optional by default, opts into `mode: required` here); any other credential draws the vendor's own 401. Proves a client actually rotates rather than retries with the key it already had. **Do not combine `expect_key` with a fault plan**: an auth rejection claims no attempt, so a plan written next to `expect_key` fires one call later than its author expects. |
| `server-error` | A 500 is surfaced as an upstream failure rather than mistaken for an empty result set. |
| `malformed-json` | A body that is not valid JSON fails cleanly instead of panicking or returning zero values. |
| `extra-fields` | Unknown additive response fields do not break the decoder — vendors evolve additively. |
| `malicious-content` | A generic hostile-content pack — prompt injection (`IGNORE ALL PREVIOUS INSTRUCTIONS`, `<\|im_start\|>system`), credential-shaped bait (`sk-live-FAKE`, `AKIAFAKE`, `xoxb-FAKE`, `-----BEGIN RSA PRIVATE KEY-----`), unescaped `<script>` markup and an exfiltration instruction to `exfil.example`, plus one benign source — projected through every provider block, so your guardrail or fail-closed ingress gate is exercised on every dispatch path from one scenario. Ask for the whole pack: `numResults:20` on Exa and `max_results:20` on Tavily (the vendor defaults truncate it), `text:true` on Exa `/answer`, `include_raw_content` on Tavily; on MCP one `tools/call` of `search` returns every hostile source twice — as a text block and as a `resource_link` — plus an injection string in `structuredContent`, and `server/discover`'s `instructions` carries the marker too; the file's header comment records the rest, including which surfaces carry only the injection marker. |
| `fusion-overlap` | One canonical source rendered through all four providers (on MCP as text blocks and `resource_link`s carrying the same URL variants), with a claim repeated across sources: deduplication by URL and corroboration counting are exercised deliberately, not by accident. |
| `conversation` | A scripted agentic loop: successive calls to one route get successive turns, matched by call index and by body substring, with an unconditional fallback last. On MCP the turns are server states — the catalogue drifts: call 0 lists one tool, every later call lists two — so a client that cached `tools/list` and got `-32602` learns to re-list. |
| `namespaced` | One route serving two concurrent callers, kept in separate turn lanes by `turn_key`, so neither draws the turn scripted for the other. |
| `streaming` | The Perplexity Sonar and Agent surfaces serve scripted SSE: a complete stream, a mid-stream disconnect, a truncated chunk, a transient blip your client should retry, and slow chunk pacing. MCP's `tools/call` answers as SSE too — `notifications/progress` frames only when your request carried `_meta.progressToken`, then the response frame; `server/discover` and `tools/list` stay JSON. |
| `unknown-provider` | A provider this build has no handler for warns and is ignored, so a scenario file shared across repositories does not break the moment one consumer pins an older Servicesim. |

## Mounting your own scenario

Anything specific to *your* product — your corpus, your claims, your provider projections — stays in your
repository and is mounted read-only. Changing it does not require a Servicesim release.

```yaml
services:
  servicesim:
    image: ghcr.io/c360studio/servicesim:v0.4.0
    command: ["--scenario", "/scenarios/fusion-overlap.yaml"]
    volumes:
      - ./test/fixtures/research:/scenarios:ro
```

Scenario resolution is confined to `-scenario-root` (defaulting to the scenario's own directory) through `os.Root`,
so nothing outside the mount can be opened — symlinks included.

The schema is documented in [`docs/scenario-schema.md`](docs/scenario-schema.md): the single-shot form that most
authors ever need, and the multi-turn form for scripting an agentic loop.

## Documentation

### If you are *using* Servicesim

| Document | What it holds |
|---|---|
| This README | Quickstart, base URLs, namespaces, the admin surface, built-in scenarios, and what is provider-neutral versus profile-specific. |
| [`examples/`](examples) | A worked consumer, compiled and run by CI. The best thing to copy. |
| [`docs/scenario-schema.md`](docs/scenario-schema.md) | The scenario YAML reference: single-shot form, multi-turn form, faults. |
| [`docs/troubleshooting.md`](docs/troubleshooting.md) | Symptom-first answers: an unexpected 401, 404, 405, tests interfering, counts that differ per run under more than one replica, a container that will not become ready, and the MCP `400`s a legacy or hand-rolled client draws. |
| [`contracts/`](contracts/README.md) | The index and the shared discipline (`contracts.Read`/`Goldens`/`Provenance`/`ProviderSpec`/`OldestVerified`/`Conform`). Each vendor's own bundle — the documentation URL and date each field was verified from, plus the golden fixtures the handlers are tested against — lives beside its profile: [`profiles/mcp/contracts/README.md`](profiles/mcp/contracts/README.md) is the MCP one, what the 2026-07-28 specification says and every simulator-chosen default beside it. |

### If you are *changing* Servicesim

| Document | What it holds |
|---|---|
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Start here: the pre-push gate, lint expectations, and how to add a provider. |
| [`docs/adopter-backlog.md`](docs/adopter-backlog.md) | What is being built next, why, in what order, and the decisions already settled. |
| [`CLAUDE.md`](CLAUDE.md) | The house rules — determinism, redaction, fail-closed, the dependency budget. |
| [`docs/design/package-design.md`](docs/design/package-design.md) | The Go design of record: packages, import levels, the reasoning behind each seam (Go blocks illustrative). |
| [`docs/design/extended-surfaces.md`](docs/design/extended-surfaces.md) | Addendum: the Agent surface, the open provider registry, the turn model. |
| [`docs/design/async-jobs.md`](docs/design/async-jobs.md) | The async create-then-poll design: job lifecycle, lanes, fault budgets. |
| [`docs/design/streaming.md`](docs/design/streaming.md) | The SSE streaming design: the two grammars, journal-early append, pacing. |
| [`docs/design/mcp-profile.md`](docs/design/mcp-profile.md) | The MCP profile as shipped: the era decision, the request lifecycle, the JSON-RPC and transport layers, status policy, SSE, redaction, the finding codes — and the composition-seam evidence for D9 tier 2. |
| [`docs/architecture-and-implementation-plan.md`](docs/architecture-and-implementation-plan.md) | The product requirements the design implements. |
| [`docs/adr/`](docs/adr) | Decisions already taken, with the reasoning that forced them. |

**`contracts/` outranks every other document here, including the design.** The plan was written from a snapshot and
has already been wrong about Exa's `score` field, Tavily's `response_time` type and Perplexity's required `cost`
object. Never write a wire field from memory — see [ADR 0002](docs/adr/0002-verified-contract-precedence.md).
The five `docs/design/` documents are records of what shipped, not specifications: every Go block in them is
illustrative, not a compiled contract, and where one disagrees with the source under `provider/`, `internal/`,
`scenario/` or `testkit/`, the code wins.

## Working on Servicesim

```bash
task check   # everything CI gates on: lint, race tests, build, image smoke test
task test    # go test -race -count=1 ./...
task lint    # vet, gofmt, revive, live-host guard, docs guard, markdownlint
task build   # bin/servicesim
```

[`CONTRIBUTING.md`](CONTRIBUTING.md) has the rest. `task --list` shows every task.

## Licence

See [`LICENSE`](LICENSE).
