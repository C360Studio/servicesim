# Servicesim Architecture and Implementation Plan

> ## Status: original requirements plan, snapshot from before `v0.1.0`
>
> This document set the initial product scope, architecture and priorities before implementation began — "Proposed
> name" and "Initial providers" just below are exactly that, proposals, not the shipped state. It is **not** kept in
> sync with the code. `contracts/<provider>/README.md` outranks it on every wire field
> ([ADR 0002](adr/0002-verified-contract-precedence.md)); the design documents
> ([`docs/design/package-design.md`](design/package-design.md),
> [`docs/design/extended-surfaces.md`](design/extended-surfaces.md),
> [`docs/design/async-jobs.md`](design/async-jobs.md), [`docs/design/streaming.md`](design/streaming.md)) are the
> documents that record what actually shipped, phase by phase. Where this plan and the shipped code disagree, the
> code wins — see [`docs/adopter-backlog.md`](adopter-backlog.md) for the current, phased delivery record. The body
> below is left as originally written.

## Status

- Proposed name: `servicesim`
- Proposed repository: `github.com/c360studio/servicesim`
- Initial providers: Exa, Tavily, and Perplexity
- Implementation language: Go
- Distribution: Go test package, standalone binary, and one OCI image

## Decision summary

Create one shared repository containing one Go binary and one container image that simulates all three external APIs.
Do not create a repository, binary, or image per provider.

The binary exposes a small administration API plus a separate listener for each provider. Separate listeners preserve the
providers' real URL paths, including the `POST /search` collision between Exa and Tavily.

| Default port | Surface |
|---:|---|
| `8080` | Health, readiness, and redacted request journal |
| `8081` | Exa-compatible API |
| `8082` | Tavily-compatible API |
| `8083` | Perplexity-compatible API |

The same image can run all providers together or a selected subset. Consumers inject provider base URLs rather than
overriding DNS or installing test certificate authorities.

```text
EXA_BASE_URL=http://servicesim:8081
TAVILY_BASE_URL=http://servicesim:8082
PERPLEXITY_BASE_URL=http://servicesim:8083
```

## Context

Several products need deterministic tests around paid and nondeterministic external research services. Product-local mocks
would shorten the first implementation but create repeated maintenance, divergent API behavior, and duplicated container
builds across repositories.

Servicesim is shared test infrastructure. It should model the HTTP contracts and failure modes that consumers depend on,
without attempting to reproduce vendor ranking systems, neural retrieval quality, or language-model reasoning.

## Goals

1. Make adapter tests fast, deterministic, and credential-free.
2. Support full cross-process integration and end-to-end tests through a reusable container.
3. Exercise provider-specific request, authentication, response, retry, timeout, and parsing behavior.
4. Test multi-provider normalization and fusion against intentionally overlapping or conflicting source material.
5. Provide reusable failure scenarios such as rate limiting, timeouts, malformed responses, and empty results.
6. Keep domain-specific research corpora in consuming repositories so new product scenarios do not require a Servicesim
   release.
7. Detect real-provider contract drift with small, bounded live canaries outside normal pull-request CI.

## Non-goals

1. Reproduce search ranking or semantic relevance algorithms.
2. Produce realistic generative answers from arbitrary input.
3. Act as a production proxy, cache, or API gateway.
4. Store real API keys or unsanitized recorded traffic.
5. Implement every field exposed by every provider.
6. Make exact latency or quality comparisons between providers.
7. Support streaming in the first milestone unless a production adapter already requires it. Streaming can be added as a
   provider-specific follow-up without changing the overall architecture.

## Design principles

### Model the consumed contract, not the entire vendor

Each provider package implements the fields and behaviors used by C360 clients. Unknown response fields should be tolerated
by consumers because external APIs evolve additively. Servicesim should include an `extra-fields` scenario to prove that
consumer decoders do not fail on harmless additions.

### Be strict about requests

The simulator validates method, route, content type, authentication placement, required fields, field types, and supported
enumerations. A request journal records validation findings so a test can distinguish "received a response" from "sent the
correct vendor request."

### Be deterministic by default

The same scenario and request must produce the same response, identifiers, ordering, and timing configuration.
Generated IDs should derive from stable fixture keys rather than random UUIDs unless a scenario explicitly tests
nondeterministic IDs.

### Keep scenario state isolated

Startup scenario selection is preferred for CI:

```text
servicesim --scenario /scenarios/fusion-overlap.yaml
```

Parallel test suites should start separate processes or containers. A mutable administration API must not become hidden
shared state between concurrent tests.

### Preserve provider and source provenance

Provider diversity is not the same as source diversity. Multiple providers can return the same underlying URL or
repeat the same claim. Fixtures must make source overlap and provider provenance explicit so consuming fusion tests
can verify deduplication and corroboration behavior.

## Proposed repository layout

```text
servicesim/
  cmd/
    servicesim/
      main.go
  provider/
    exa/
      handler.go
      request.go
      response.go
      handler_test.go
    tavily/
      handler.go
      request.go
      response.go
      handler_test.go
    perplexity/
      handler.go
      request.go
      response.go
      handler_test.go
  scenario/
    load.go
    model.go
    validate.go
    render.go
  testkit/
    server.go
    assertions.go
  internal/
    admin/
    faults/
    journal/
    redact/
  contracts/
    exa/
    tavily/
    perplexity/
  scenarios/
    protocol/
      happy.yaml
      empty-results.yaml
      unauthorized.yaml
      rate-limited.yaml
      server-error.yaml
      malformed-json.yaml
      extra-fields.yaml
  Dockerfile
  docker-compose.example.yml
  Taskfile.yml
  go.mod
  README.md
  LICENSE
  .github/workflows/
    ci.yml
    image.yml
    live-contract-canary.yml
```

The exported `provider/*` handlers and `testkit` package allow Go consumers to use `httptest.Server` without Docker. The
standalone binary composes the exact same handlers for container-based tests.

## Provider surfaces

### Exa

Initial route:

```text
POST /search
```

Authentication:

- Accept `x-api-key`.
- Accept `Authorization: Bearer <token>` because the current API documentation permits both.
- Require at least one supported authentication form in strict scenarios.
- Redact credential values from logs and the request journal.

Initial request fields:

```json
{
  "query": "latest developments in LLMs",
  "type": "auto",
  "numResults": 5,
  "contents": {
    "text": true,
    "highlights": true
  }
}
```

Initial response fields:

```json
{
  "results": [
    {
      "id": "source-1",
      "url": "https://example.test/article",
      "title": "Example article",
      "author": "Example Author",
      "publishedDate": "2026-05-20T00:00:00Z",
      "text": "Extracted source text",
      "highlights": ["Relevant excerpt"],
      "highlightScores": [0.89],
      "score": 0.95
    }
  ],
  "requestId": "exa-request-1"
}
```

Contract notes:

- `contents.text`, `contents.highlights`, and related content options belong under `contents` on `/search`.
- `useAutoprompt` is deprecated and should not be emitted by new clients. Servicesim may accept and flag it in the journal
  for compatibility, but adapter contract tests should assert that it is absent.
- Current documented search types include `auto`, `fast`, `instant`, and deep variants. New tests should not depend on the
  legacy `neural` request value.
- Source: <https://exa.ai/docs/reference/search>
- Integration guidance: <https://exa.ai/docs/reference/search-api-guide-for-coding-agents>

### Tavily

Initial route:

```text
POST /search
```

Authentication:

- Require `Authorization: Bearer <token>`.
- Do not treat an `api_key` JSON property as the canonical REST authentication contract.
- Redact the token from all output.

Initial request fields:

```json
{
  "query": "your search query",
  "search_depth": "basic",
  "include_answer": true,
  "include_images": false,
  "include_raw_content": false,
  "max_results": 3
}
```

Initial response fields:

```json
{
  "query": "your search query",
  "answer": "A synthesized answer when requested.",
  "images": [],
  "results": [
    {
      "title": "Example source",
      "url": "https://example.test/source",
      "content": "Relevant source excerpt.",
      "raw_content": null,
      "score": 0.98
    }
  ],
  "response_time": "1.15",
  "request_id": "tavily-request-1"
}
```

Contract notes:

- The current REST documentation requires Bearer authentication in the header.
- `include_answer`, `include_raw_content`, and `max_results` remain explicit because they directly affect response
  size and credit consumption.
- Reusable error scenarios should cover at least 400, 401, 429, plan-limit responses, and 500.
- Source: <https://docs.tavily.com/documentation/api-reference/endpoint/search>

### Perplexity

Initial routes:

```text
POST /v1/sonar
POST /chat/completions
```

The first route is the canonical current Sonar endpoint. The second is the documented OpenAI SDK compatibility alias.
Supporting both prevents the simulator from forcing consumers to use one SDK style while still allowing adapter tests to
assert their intended route.

Authentication:

- Require `Authorization: Bearer <token>`.
- Redact the token from all output.

Initial request fields:

```json
{
  "model": "sonar",
  "messages": [
    {"role": "system", "content": "You are a helpful research assistant."},
    {"role": "user", "content": "Summarize recent developments."}
  ],
  "temperature": 0.2,
  "max_tokens": 1024
}
```

Initial response fields:

```json
{
  "id": "perplexity-completion-1",
  "object": "chat.completion",
  "model": "sonar",
  "created": 1723555555,
  "usage": {
    "prompt_tokens": 24,
    "completion_tokens": 150,
    "total_tokens": 174
  },
  "citations": ["https://example.test/source"],
  "search_results": [
    {
      "title": "Example source",
      "url": "https://example.test/source",
      "date": "2026-05-20",
      "snippet": "Relevant source excerpt.",
      "source": "web"
    }
  ],
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "A grounded synthesized answer."
      },
      "finish_reason": "stop"
    }
  ]
}
```

Contract notes:

- Consumers should preserve both `citations` and `search_results`; citations alone lack source titles and snippets.
- The canonical endpoint is `/v1/sonar`, while `/chat/completions` remains an accepted OpenAI-compatible alias.
- Source: <https://docs.perplexity.ai/api-reference/sonar-post>
- OpenAI compatibility: <https://docs.perplexity.ai/docs/sonar/openai-compatibility>

## Scenario model

A scenario should describe canonical sources, provider projections, and provider behavior. One source corpus should render
into all three wire formats so overlap is deliberate rather than accidental.

```yaml
version: 1
name: fusion-overlap

sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    author: Example Author
    published_at: 2026-05-20T00:00:00Z
    text: Full source text.
    snippets:
      - A relevant excerpt.
    claims:
      - id: claim-1
        text: A normalized claim represented by this source.

providers:
  exa:
    results:
      - source: source-a
        score: 0.95
  tavily:
    answer: A short synthesis.
    results:
      - source: source-a
        score: 0.98
  perplexity:
    answer: A grounded answer citing Report A.
    citations:
      - source-a
    search_results:
      - source-a
```

Product-specific scenario files remain in consuming repositories and are mounted read-only:

```yaml
services:
  servicesim:
    image: ghcr.io/c360studio/servicesim:v0.1.0
    command: ["--scenario", "/scenarios/fusion-overlap.yaml"]
    volumes:
      - ./test/fixtures/research:/scenarios:ro
```

## Fault model

Every provider response can optionally declare a deterministic fault:

```yaml
providers:
  tavily:
    fault:
      attempts:
        - status: 429
          retry_after: 1
          body:
            detail:
              error: Too many requests.
        - status: 200
    results:
      - source: source-a
```

Initial fault types:

- HTTP status with provider-shaped error body.
- Fixed response delay.
- Connection close before headers.
- Truncated response body.
- Invalid JSON.
- Wrong content type.
- Empty successful response.
- Failure for the first N attempts followed by success.
- Additional unknown response fields.

Tests of client backoff should use an injectable clock or very short configured durations. Servicesim should not require
arbitrary multi-second sleeps in normal unit tests.

## Administration surface

Initial endpoints:

```text
GET  /healthz
GET  /readyz
GET  /__admin/requests
POST /__admin/reset
```

The request journal should expose:

- Provider.
- Request sequence number.
- Method and path.
- Arrival and completion timestamps.
- Sanitized headers.
- Parsed request body.
- Scenario response or fault selected.
- Validation warnings and errors.

The journal must never expose API key values. Concurrent fusion tests can use arrival and completion timestamps to prove
that provider calls overlapped rather than running serially.

`POST /__admin/reset` is useful for local tests but should not be required by parallel CI. Isolated simulator
instances are the preferred concurrency boundary.

## Testing strategy

### Layer 1: Simulator unit and contract tests

Each provider package tests:

- Supported method and route.
- Authentication validation.
- Required request fields and types.
- Provider response encoding.
- Provider-shaped error responses.
- Fixture validation failures.
- Credential redaction.
- Deterministic identifiers and ordering.

Golden wire fixtures should cover only the consumed contract. They should be reviewed when changed and should carry the
official documentation URL and the date on which the shape was verified.

### Layer 2: In-process consumer adapter tests

Go consumers import the relevant handler or `testkit` and run it with `httptest.Server`. These tests cover:

- Correct URL and method.
- Correct authentication placement.
- Request serialization.
- Response parsing and normalization.
- Context cancellation and client deadlines.
- Retry classification.
- Tolerance of additive response fields.
- Failure on missing fields that the normalized contract requires.

### Layer 3: Container integration tests

Run the published image in Docker Compose or Testcontainers to prove:

- Base-URL configuration reaches the correct listener.
- Container health and readiness work.
- Network timeouts and connection failures behave realistically.
- The same image supports all three providers concurrently.
- The request journal can verify calls without leaking credentials.

### Layer 4: Multi-provider fusion tests

Use deterministic scenarios designed around fusion invariants rather than vendor ranking quality:

1. All providers return distinct sources.
2. All providers return the same canonical URL.
3. URL variants refer to the same source and must be canonicalized.
4. Different sources repeat the same claim.
5. Sources conflict and have different publication dates.
6. One provider returns an answer without source evidence.
7. One provider returns no results.
8. One provider is rate-limited and later succeeds.
9. One provider times out permanently while the others succeed.
10. All providers fail.
11. Providers return additional unknown fields.
12. Provider calls execute concurrently.

Fusion tests should assert normalized evidence, provenance, deduplication, partial-success policy, and terminal
behavior. They should not assert that a simulator has reproduced real-world relevance scoring.

### Layer 5: Live contract canaries

Simulator tests cannot detect vendor drift. A scheduled or manually dispatched workflow should make one bounded
request to each real provider using repository secrets.

The workflow should:

1. Use stable, low-cost queries.
2. Validate only request success and the response fields consumed by adapters.
3. Record provider, endpoint, date, status, and sanitized structural findings.
4. Fail on removed or incompatible fields.
5. Report additive fields without automatically failing unless strictness is intentional.
6. Never commit captured responses or update goldens automatically.
7. Never print credentials or full sensitive content.

Live canaries should not run on every pull request.

## Build and release

Publish one multi-architecture image:

```text
ghcr.io/c360studio/servicesim:v0.1.0
ghcr.io/c360studio/servicesim:sha-<commit>
```

Target platforms:

- `linux/amd64`
- `linux/arm64`

Consumer repositories should pin a released version, and release-critical CI may additionally pin the image digest.

The image should:

- Use a statically linked Go binary.
- Run as a non-root user.
- Include CA certificates only if live proxying is ever deliberately introduced.
- Include built-in protocol scenarios.
- Accept external scenarios through a read-only mount.
- Expose the four documented ports.
- Define a health check against port 8080.

Provider handler changes, scenario-schema changes, and behavior changes require a Servicesim release. Product-specific
fixture changes do not.

## Security requirements

1. Never store real credentials in fixtures, images, request journals, or CI artifacts.
2. Redact `Authorization`, `x-api-key`, and JSON properties whose names indicate credentials.
3. Bind to explicitly configured interfaces; local development may default to loopback, while the container binds to all
   interfaces.
4. Do not proxy unmatched traffic to real providers. An unmatched request fails closed.
5. Limit request body and journal sizes.
6. Disable or bound journal retention.
7. Validate mounted fixture paths and refuse path traversal.
8. Treat recorded production traffic as untrusted and require sanitization before fixture use.

## Observability

Servicesim should log structured, credential-free events with:

- Provider.
- Scenario.
- Request sequence.
- Route.
- Selected response or fault.
- Validation result.
- Duration.

Useful process metrics can be added later, but the MVP needs only health, readiness, structured logs, and the request
journal.

## Implementation phases

### Phase 0: Repository foundation

- Initialize the Go module and conventional repository files.
- Add CI for formatting, vetting, unit tests, race tests, and build.
- Add the non-root multi-stage Docker image.
- Implement configuration, lifecycle, health, readiness, and graceful shutdown.
- Define credential-redaction tests before provider handlers are added.

Exit criteria:

- `go test -race ./...` passes.
- The binary starts and stops cleanly.
- The image runs as non-root and reports healthy.

### Phase 1: Scenario core and Exa

- Define the versioned scenario schema.
- Implement canonical sources and Exa projection rendering.
- Implement Exa request validation and response/error behavior.
- Add the redacted request journal.
- Export an in-process Exa test handler.

Exit criteria:

- Happy, empty, unauthorized, rate-limited, malformed, and extra-field scenarios pass.
- A consuming Go test can use the handler through `httptest.Server`.

### Phase 2: Tavily

- Implement Tavily request validation and rendering from the same canonical source corpus.
- Implement Tavily-specific error bodies and status scenarios.
- Verify Bearer authentication and response field types.

Exit criteria:

- The same canonical source can render as both Exa and Tavily evidence.
- Both provider listeners operate concurrently in one process.

### Phase 3: Perplexity

- Implement `/v1/sonar` and `/chat/completions`.
- Render answers, citations, search results, choices, and usage.
- Add answer-without-evidence and citation-overlap scenarios.

Exit criteria:

- All three listeners operate concurrently.
- A single scenario can project the same source through all three providers.

### Phase 4: Container and consumer adoption

- Publish the first versioned image.
- Add Docker Compose and Testcontainers examples.
- Adopt base-URL injection in the first consuming adapters.
- Add one deterministic multi-provider integration test.
- Confirm request concurrency using journal timestamps.

Exit criteria:

- A consumer can run the full test with no external credentials or paid requests.
- The consumer pins a released Servicesim image.

### Phase 5: Drift detection and hardening

- Add manually dispatched live contract checks.
- Schedule them only after cost and secret-handling review.
- Add response-size limits, journal bounds, and malformed transport cases.
- Add streaming only for providers whose production adapters actually use it.

## Acceptance criteria for the initial release

1. One repository, binary, and image serve Exa, Tavily, and Perplexity simulations.
2. Each provider uses a separate configurable listener and preserves its real endpoint paths.
3. Go consumers can use the same handlers in-process.
4. Startup scenarios are deterministic and validated before readiness succeeds.
5. Product-specific scenarios can be mounted without rebuilding the image.
6. Authentication is validated and credentials are always redacted.
7. Built-in scenarios cover happy, empty, unauthorized, rate-limited, server-error, malformed, and extra-field behavior.
8. One canonical source corpus can render into all three provider response shapes.
9. The request journal supports payload and concurrency assertions.
10. Unmatched traffic fails closed and is never proxied to a real provider.
11. CI passes formatting, vetting, unit tests, race tests, and image build.
12. The image is published for `linux/amd64` and `linux/arm64` and runs as non-root.

## Deferred decisions

- Whether streaming is needed for Exa or Perplexity consumers.
- Whether the administration API needs per-test namespaces in addition to process-level isolation.
- Whether full official OpenAPI documents can be redistributed under their respective terms, or whether Servicesim should
  keep only reviewed minimal schemas for the consumed fields.
- Whether existing product-local external-service mocks should migrate into this repository after the initial provider set
  is stable.

These decisions do not block the initial architecture.
