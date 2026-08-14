# Servicesim

A deterministic HTTP simulator for the **Exa**, **Tavily** and **Perplexity** research APIs. One binary, one image,
one listener per provider.

It exists so that a repository with a research adapter can test that adapter *fast, offline, and for free* — and,
more importantly, can prove the adapter sent the **correct vendor request**, not merely that it got a response back.

- **Deterministic.** The same scenario and the same request at the same call position produce byte-identical
  responses, identifiers and ordering. No clocks, no randomness, no UUIDs on a response path. Identifiers move with
  the call position, the way a real vendor's do, and a fresh state lane starts at call 0 again.
- **Strict about requests, tolerant about responses.** Method, route, content type, credential placement, required
  fields, field types and enum values are all validated, and every finding lands in a redacted request journal.
- **Fails closed.** An unmatched method, path, provider or scenario returns a provider-shaped error. Servicesim
  never dials outward, so a mis-set base URL can never quietly reach a real paid API.
- **Credential-safe.** `Authorization`, `x-api-key` and credential-shaped JSON properties are redacted *before*
  anything is retained — in the journal, in logs, and in error messages. There is no flag that turns it off.

What it is *not*: a proxy, a ranking engine, or a fake LLM. It does not generate answers from arbitrary input, and
it does not implement every field of every vendor — only the *consumed contract*.

## Listeners

Each provider gets its own listener. That is not a stylistic choice: Exa and Tavily both serve `POST /search`, so
preserving each vendor's real path requires a separate port per provider.

| Listener | Port | Routes |
|---|---:|---|
| admin | `8080` | `GET /healthz`, `GET /readyz`, `GET /__admin/requests`, `GET /__admin/scenario`, `POST /__admin/reset` |
| exa | `8081` | `POST /search`, `POST /answer` |
| tavily | `8082` | `POST /search` |
| perplexity | `8083` | `POST /v1/sonar`, `POST /chat/completions`, `POST /v1/agent`, `POST /v1/responses` |

`/chat/completions` and `/v1/responses` are aliases the OpenAI SDK produces when pointed at Perplexity — same
handler, same shapes, and the journal records which path was actually used. Sonar is supported by Perplexity until
2026-09-27; `/v1/agent` is its successor and both are simulated.

Every port is configurable (`--exa-port`, `SERVICESIM_EXA_PORT`, …), and `--providers` limits which listeners bind.

## Two ways to use it

### In-process, from a Go test

No Docker, no ports, no credentials. `testkit` starts one `httptest.Server` per provider and registers its own
cleanup, so there is nothing to defer.

```go
import (
    "testing"

    "github.com/c360studio/servicesim/provider"
    "github.com/c360studio/servicesim/testkit"
)

func TestAdapterSendsACorrectExaRequest(t *testing.T) {
    sim := testkit.Start(t, testkit.WithBuiltin("happy"))

    adapter := research.New(research.Config{
        ExaBaseURL:        sim.URL(provider.Exa),
        TavilyBaseURL:     sim.URL(provider.Tavily),
        PerplexityBaseURL: sim.URL(provider.Perplexity),
    })

    if _, err := adapter.Search(t.Context(), "report a"); err != nil {
        t.Fatalf("search: %v", err)
    }

    entries := sim.Requests(provider.Exa)
    testkit.AssertRequestCount(t, sim, provider.Exa, 1)
    testkit.AssertAPIKeyHeader(t, entries[0])
    testkit.AssertNoErrors(t, entries[0]) // the adapter sent a valid Exa request
}
```

`testkit.WithScenarioYAML` keeps a single-purpose fixture inline next to the test; `WithScenarioFile` and
`WithScenario` cover the rest. `sim.BaseURLs()` returns the same URLs keyed exactly as the environment variables
below, so a test can configure a consumer the way Compose would.

### As a container

Same handlers, same scenarios, reachable from any language.

```bash
docker run --rm -p 8080-8083:8080-8083 \
  ghcr.io/c360studio/servicesim:v0.1.0 --scenario builtin:happy
```

```yaml
services:
  servicesim:
    image: ghcr.io/c360studio/servicesim:v0.1.0
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

[`docker-compose.example.yml`](docker-compose.example.yml) is the fuller version of that file: a pinned image tag,
a read-only scenario mount, a healthcheck, and the namespaced base-URL forms described
[below](#one-container-many-concurrent-tests).

The image runs as a non-root user on a `scratch` base with no shell and no CA bundle — introspection goes through
the admin listener, not through `exec`.

## Pointing a consumer at it

Consumers inject a **base URL** rather than mocking an HTTP client, which is what makes the adapter's own transport,
retry and decode paths part of the test:

| Variable | Points at | Value under Compose |
|---|---|---|
| `EXA_BASE_URL` | Exa listener | `http://servicesim:8081` |
| `TAVILY_BASE_URL` | Tavily listener | `http://servicesim:8082` |
| `PERPLEXITY_BASE_URL` | Perplexity listener | `http://servicesim:8083` |

Credentials are still required by default — the point is to prove the adapter *sends* one, in the right place. Any
fake value works. Servicesim never stores it; the journal keeps only a fingerprint.

| Provider | Accepted credential placement |
|---|---|
| Exa | `x-api-key`, or `Authorization: Bearer` |
| Tavily | `Authorization: Bearer` |
| Perplexity | `Authorization: Bearer` |

To inspect what a consumer actually sent, read the journal:

```bash
curl -s 'http://localhost:8080/__admin/requests?provider=exa&pretty=1'
```

## One container, many concurrent tests

**Process isolation is still the recommended default.** A separate process or container per parallel suite is the
simplest thing that works, and nothing below changes that. Namespaces exist because a survey of the sibling
repositories found every e2e suite already sharing *one* mock container across many tests — so the choice was not
between two patterns but between the shared pattern being safe and it being silently wrong. Namespaces make it
safe; they do not make it the norm.

Two optional path prefixes are stripped before route matching, so a prefixed request reaches exactly the same
handler as a plain one:

```text
http://servicesim:8081/x/<scenario>/n/<namespace>/search
                       └─ selects behaviour   └─ isolates state
```

Both are optional and their order is fixed. `/search` on its own still means "the startup scenario, namespace
`default`", so nothing that works today changes.

```bash
EXA_BASE_URL=http://servicesim:8081/n/${TEST_ID}
```

That one line is the entire integration. **It is a path prefix rather than a header on purpose:** consumers already
inject base URLs — that is how they are pointed at Servicesim at all — and every SDK supports a base URL carrying a
path, whereas many LLM SDKs make a per-request header awkward or impossible, and those are exactly the consumers
this is for. `X-Servicesim-Namespace` is accepted for an SDK that pins the path, but the base URL is the documented
mechanism because it needs no consumer code change at all.

A namespace is a **state** boundary, not a behaviour one:

| Isolated per namespace | Shared process-wide |
|---|---|
| Fault attempt counters | The loaded, validated scenarios |
| Turn cursors | Route tables and handlers |
| Journal entries and their scoping | Configuration and listeners |

Behaviour is the separate `/x/<scenario>` dimension, served from `--scenario-dir`, because the common case is two
tests wanting the *same* behaviour with *independent* state — which a scenario-only mechanism cannot express. Every
scenario in that directory is loaded and validated at startup, so readiness still means "every scenario is valid"
and nothing is loaded lazily.

The rest of it, briefly:

- Namespaces are created on first use. There is no registration call, because a setup call in every consumer test
  is the friction this removes. `--max-namespaces` (default 1024) bounds them, and exceeding the bound is a loud
  provider-shaped error rather than a silent eviction — evicting would reset a running test's turn cursor mid-loop.
- `GET /__admin/requests?namespace=<name>` scopes the journal; without the parameter every namespace is returned
  and each entry names its own.
- `POST /__admin/reset?namespace=<name>` drops one namespace. Resetting everything needs an explicit `?all=true`,
  because a bare reset that silently wiped a hundred concurrent tests' cursors is a trap.
- Concurrency *within* one namespace is a separate problem with a separate answer: when one route serves several
  callers at once, key the turn cursor on something that tells them apart with
  [`turn_key`](docs/scenario-schema.md#turn_key--what-the-cursor-counts-per). The built-in `namespaced` scenario is
  the worked example.

[`docker-compose.example.yml`](docker-compose.example.yml) shows both base-URL forms alongside a pinned image, a
read-only scenario mount and a healthcheck.

## Built-in protocol scenarios

Eleven scenarios ship inside the binary. Select one with `--scenario builtin:<name>` or
`testkit.WithBuiltin("<name>")`. They cover *protocol* behaviour, which is the same for every consumer;
product-specific corpora belong in the consuming repository.

| Scenario | What it proves about the consumer |
|---|---|
| `happy` | The baseline: a well-formed 200 from every provider parses into the adapter's own model. |
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

## Mounting a product-specific scenario

Anything that is specific to *your* product — your corpus, your claims, your provider projections — stays in your
repository and is mounted read-only. Changing it does not require a Servicesim release.

```yaml
services:
  servicesim:
    image: ghcr.io/c360studio/servicesim:v0.1.0
    command: ["--scenario", "/scenarios/fusion-overlap.yaml"]
    volumes:
      - ./test/fixtures/research:/scenarios:ro
```

Scenario resolution is confined to `--scenario-root` (defaulting to the scenario's own directory) through `os.Root`,
so nothing outside the mount can be opened — symlinks included.

The schema is documented in [`docs/scenario-schema.md`](docs/scenario-schema.md): the single-shot form that most
authors ever need, and the multi-turn form for scripting an agentic loop.

## The verified wire shapes live in `contracts/`

[`contracts/`](contracts/README.md) records, per provider, the subset of the vendor API that Servicesim simulates,
with the documentation URLs each field was read from and the date it was verified, plus the golden JSON fixtures the
handlers are tested against.

**That directory outranks every other document in this repository, including the design.** The plan was written
from a snapshot and has already been wrong about Exa's `score` field, Tavily's `response_time` type and
Perplexity's required `cost` object. Never write a wire field from memory — see
[ADR 0002](docs/adr/0002-verified-contract-precedence.md).

## Working on Servicesim

```bash
task check   # everything CI gates on: lint, race tests, build, image smoke test
task test    # go test -race -count=1 ./...
task lint    # vet, gofmt, revive, live-host guard
task build   # bin/servicesim
```

| Document | What it holds |
|---|---|
| [`CLAUDE.md`](CLAUDE.md) | House rules — determinism, redaction, fail-closed, dependency budget. |
| [`docs/scenario-schema.md`](docs/scenario-schema.md) | The scenario YAML reference for authors in consuming repositories. |
| [`docs/design/package-design.md`](docs/design/package-design.md) | The Go design of record: packages, import levels, exact signatures. |
| [`docs/design/extended-surfaces.md`](docs/design/extended-surfaces.md) | Addendum: the Agent surface, the open provider registry, the turn model. |
| [`docs/architecture-and-implementation-plan.md`](docs/architecture-and-implementation-plan.md) | The product requirements the design implements. |
| [`docs/adr/`](docs/adr) | Decisions already taken, with the reasoning that forced them. |

## Licence

See [`LICENSE`](LICENSE).
