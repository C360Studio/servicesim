# Servicesim

A deterministic HTTP simulator for the **Exa**, **Tavily** and **Perplexity** research APIs. One binary, one image,
one listener per provider.

Point your code's base URLs at it instead of the real vendors. Your tests then run *fast, offline and for free* —
and, more importantly, they can prove your client sent the **correct vendor request**, not merely that it got a
response back.

- **Deterministic.** The same scenario and the same request at the same call position produce byte-identical
  responses, identifiers and ordering. No clocks, no randomness, no UUIDs on a response path.
- **Strict about requests, tolerant about responses.** Method, route, content type, credential placement, required
  fields, field types and enum values are all validated, and every finding lands in a redacted request journal.
- **Fails closed.** An unmatched method, path, provider or scenario returns a provider-shaped error. Servicesim
  never dials outward, so a mis-set base URL can never quietly reach a real paid API.
- **Credential-safe.** `Authorization`, `x-api-key` and credential-shaped JSON properties are redacted *before*
  anything is retained — in the journal, in logs and in error messages. There is no flag that turns it off.

It is not a proxy, a ranking engine or a fake LLM. It does not generate answers from arbitrary input and does not
implement every field of every vendor — only the *consumed contract*.

## 60-second quickstart

No account, no credentials, no configuration, and nothing to clone:

```bash
docker run --rm -p 8080-8083:8080-8083 ghcr.io/c360studio/servicesim:v0.1.1
```

The image is public and multi-architecture (`linux/amd64`, `linux/arm64`). Tags are published in both spellings
against one digest — `v0.1.0` and `0.1.0`, `v0.1` and `0.1`, `v0` and `0` — plus `latest` and `sha-<commit>`.

For release-critical CI, pin the digest rather than a tag, so a re-publish cannot move under you:

```text
ghcr.io/c360studio/servicesim@sha256:8fdd3898448558bc97de73a59c4957a480304b27a19abd1eedb6f0b168f65458
```

Working on Servicesim itself, or want the tip of `main`? `task image:build` produces `servicesim:dev` locally, and
every example below works the same with that tag substituted.

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
preserving each vendor's real path requires a separate port per provider.

| Listener | Port | Routes |
|---|---:|---|
| admin | `8080` | `GET /healthz`, `GET /readyz`, `GET /__admin/requests`, `GET /__admin/namespaces`, `GET /__admin/scenario`, `POST /__admin/reset` |
| exa | `8081` | `POST /search`, `POST /answer` |
| tavily | `8082` | `POST /search` |
| perplexity | `8083` | `POST /v1/sonar`, `POST /chat/completions`, `POST /v1/agent`, `POST /v1/responses` |

`/chat/completions` and `/v1/responses` are aliases the OpenAI SDK produces when pointed at Perplexity — same
handler, same shapes, and the journal records which path was actually used. Sonar is supported by Perplexity until
2026-09-27; `/v1/agent` is its successor and both are simulated.

Every port is configurable (`-exa-port`, `-tavily-port`, `-perplexity-port`, `-admin-port`), and `-providers`
limits which listeners bind. Run `servicesim -h` for the full flag list.

## Pointing your code at it

Inject a **base URL** rather than mocking an HTTP client. That keeps your own transport, retry and decode paths
inside the test, which is where the bugs live:

| Variable | Points at | Value under Compose |
|---|---|---|
| `EXA_BASE_URL` | Exa listener | `http://servicesim:8081` |
| `TAVILY_BASE_URL` | Tavily listener | `http://servicesim:8082` |
| `PERPLEXITY_BASE_URL` | Perplexity listener | `http://servicesim:8083` |

Credentials are still required by default — the point is to prove your client *sends* one, in the right place. Any
fake value works. Servicesim never stores it; the journal keeps only a fingerprint.

| Provider | Accepted credential placement |
|---|---|
| Exa | `x-api-key`, or `Authorization: Bearer` |
| Tavily | `Authorization: Bearer` |
| Perplexity | `Authorization: Bearer` |

## In a Go test, with no container at all

`testkit` starts one `httptest.Server` per provider in-process and registers its own cleanup, so there is nothing
to defer, no port to pick and no Docker to wait for.

The worked example lives in [`examples/`](examples) and is **compiled by `go build ./...` and run by
`go test -race ./...` in this repository's CI** — an example that is not built is an example that rots. Copy from
there rather than from this README. This is the shape of it, excerpted from
[`examples/adapter_test.go`](examples/adapter_test.go):

```go
func TestAdapterSendsACorrectExaRequest(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"))
	adapter := newAdapter(sim.Client(), sim.BaseURLs())

	results, err := adapter.SearchExa(t.Context(), "report a")
	require.NoError(t, err)
	require.Len(t, results, 2, "the happy scenario projects two canonical sources through Exa")

	testkit.AssertRequestCount(t, sim, provider.Exa, 1)
	entry := sim.Requests(provider.Exa)[0]

	testkit.AssertAPIKeyHeader(t, entry)
	testkit.AssertJSONBody(t, entry, map[string]any{
		"query":      "report a",
		"numResults": 5,
	})
	testkit.AssertNoErrors(t, entry)
	testkit.AssertNoCredentialLeak(t, sim, exaKey, tavilyKey, perplexityKey)
}
```

Note what it does *not* assert: that the call returned 200. A simulator will happily return 200 for a request the
real vendor would reject, so a green status proves nothing. The assertions are on the journal.

`sim.BaseURLs()` returns the URLs keyed exactly as the environment variables above, so one helper configures your
client from either a `*testkit.Sim` or a container. `testkit.WithScenarioYAML` keeps a single-purpose fixture inline
next to the test; `testkit.WithScenarioFile` and `testkit.WithScenario` cover the rest.

| Example file | What it shows |
|---|---|
| [`examples/adapter_test.go`](examples/adapter_test.go) | The canonical first test: prove the request was correct. |
| [`examples/fusion_test.go`](examples/fusion_test.go) | Three providers at once, deduplication, and a 429 classified as retryable. |
| [`examples/namespace_test.go`](examples/namespace_test.go) | Parallel subtests sharing one simulator, each in its own state lane. |

## As a container

Same handlers, same scenarios, reachable from any language.

```yaml
services:
  servicesim:
    image: ghcr.io/c360studio/servicesim:v0.1.1
    command: ["--scenario", "builtin:fusion-overlap"]

  app-tests:
    build: .
    depends_on: [servicesim]
    environment:
      EXA_BASE_URL: http://servicesim:8081
      TAVILY_BASE_URL: http://servicesim:8082
      PERPLEXITY_BASE_URL: http://servicesim:8083
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
	// ns.Requests(provider.Exa) sees this lane's traffic and nothing else.
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

Every piece of lane state — fault attempt counters, turn cursors, journal entries and their sequence numbers,
and the namespace registry `-max-namespaces` bounds — lives in that process's memory. Nothing is shared, nothing
is replicated and nothing is persisted. Namespaces isolate lanes *within* a process; they do not join lanes
*across* processes.

So a second replica does not halve the load, it forks the state. Requests from one client are spread across
replicas by whatever balances them, and each replica counts only the calls it happened to receive:

| What you scripted | What two replicas actually do |
|---|---|
| `call_index: 0`, then `call_index: 1` | Both replicas start at 0. The second call lands on the other replica and is served turn 0 again. |
| `attempts: [{status: 429}, {status: 200}]` | Each replica owns a full budget, so the 429 is served **twice** — once per replica — before either succeeds. |
| `AssertRequestCount(t, sim, provider.Exa, 3)` | The journal read reaches one replica and sees only its share: 1 or 2, varying run to run. |
| `POST /__admin/reset?namespace=t1` | Resets the replica that answered. The others keep their cursors. |

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
| `POST /__admin/reset` | Drops journal entries and zeroes fault attempt counters (which are the turn cursors). |

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

Eleven scenarios ship inside the binary. Select one with `--scenario builtin:<name>` or
`testkit.WithBuiltin("<name>")`. They cover *protocol* behaviour, which is the same for every consumer;
product-specific corpora belong in your own repository.

| Scenario | What it proves about your client |
|---|---|
| `happy` | The baseline: a well-formed 200 from every provider parses into your own model. |
| `empty-results` | Zero results in a well-shaped envelope is handled as "no results", not as an error. |
| `unauthorized` | A 401 in each vendor's own error envelope is surfaced as an auth failure and is not retried. |
| `rate-limited` | A 429 with `Retry-After`, then success — backoff and retry work, and the retry is counted. |
| `server-error` | A 500 is surfaced as an upstream failure rather than mistaken for an empty result set. |
| `malformed-json` | A body that is not valid JSON fails cleanly instead of panicking or returning zero values. |
| `extra-fields` | Unknown additive response fields do not break the decoder — vendors evolve additively. |
| `fusion-overlap` | One canonical source rendered through all three providers, with a claim repeated across sources: deduplication by URL and corroboration counting are exercised deliberately, not by accident. |
| `conversation` | A scripted agentic loop: successive calls to one route get successive turns, matched by call index and by body substring, with an unconditional fallback last. |
| `namespaced` | One route serving two concurrent callers, kept in separate turn lanes by `turn_key`, so neither draws the turn scripted for the other. |
| `unknown-provider` | A provider this build has no handler for warns and is ignored, so a scenario file shared across repositories does not break the moment one consumer pins an older Servicesim. |

## Mounting your own scenario

Anything specific to *your* product — your corpus, your claims, your provider projections — stays in your
repository and is mounted read-only. Changing it does not require a Servicesim release.

```yaml
services:
  servicesim:
    image: ghcr.io/c360studio/servicesim:v0.1.1
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
| This README | Quickstart, base URLs, namespaces, the admin surface, built-in scenarios. |
| [`examples/`](examples) | A worked consumer, compiled and run by CI. The best thing to copy. |
| [`docs/scenario-schema.md`](docs/scenario-schema.md) | The scenario YAML reference: single-shot form, multi-turn form, faults. |
| [`docs/troubleshooting.md`](docs/troubleshooting.md) | Symptom-first answers: an unexpected 401, 404, 405, tests interfering, counts that differ per run under more than one replica, a container that will not become ready. |
| [`contracts/`](contracts/README.md) | Per provider, the subset of the vendor API that is simulated, with the documentation URL and date each field was verified from — plus the golden fixtures the handlers are tested against. |

### If you are *changing* Servicesim

| Document | What it holds |
|---|---|
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Start here: the pre-push gate, lint expectations, and how to add a provider. |
| [`CLAUDE.md`](CLAUDE.md) | The house rules — determinism, redaction, fail-closed, the dependency budget. |
| [`docs/design/package-design.md`](docs/design/package-design.md) | The Go design of record: packages, import levels, exact signatures. |
| [`docs/design/extended-surfaces.md`](docs/design/extended-surfaces.md) | Addendum: the Agent surface, the open provider registry, the turn model. |
| [`docs/architecture-and-implementation-plan.md`](docs/architecture-and-implementation-plan.md) | The product requirements the design implements. |
| [`docs/adr/`](docs/adr) | Decisions already taken, with the reasoning that forced them. |

**`contracts/` outranks every other document here, including the design.** The plan was written from a snapshot and
has already been wrong about Exa's `score` field, Tavily's `response_time` type and Perplexity's required `cost`
object. Never write a wire field from memory — see [ADR 0002](docs/adr/0002-verified-contract-precedence.md).

## Working on Servicesim

```bash
task check   # everything CI gates on: lint, race tests, build, image smoke test
task test    # go test -race -count=1 ./...
task lint    # vet, gofmt, revive, live-host guard
task build   # bin/servicesim
```

[`CONTRIBUTING.md`](CONTRIBUTING.md) has the rest. `task --list` shows every task.

## Licence

See [`LICENSE`](LICENSE).
