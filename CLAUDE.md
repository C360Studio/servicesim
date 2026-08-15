# Servicesim Project Context

A deterministic HTTP simulator for the Exa, Tavily and Perplexity research APIs. One binary, one image, one
listener per provider. It exists so that consuming repositories can test their research adapters fast, offline,
and without spending money on paid APIs.

## Tech Stack

- Go 1.26, standard library first
- `net/http` with Go 1.22+ `ServeMux` pattern routing (`"POST /search"`) — no router dependency
- `log/slog` for structured, credential-free logs
- Task (task runner) — run `task --list` for all commands
- revive for lint, pinned through go.mod's `tool` directive

### Dependency budget

The entire non-test dependency set is `gopkg.in/yaml.v3`. Tests may additionally use
`github.com/stretchr/testify` and `github.com/google/go-cmp`.

Adding a dependency needs a reason that survives this question: *Servicesim is imported by other repositories'
test suites — is this dependency worth putting in their build graph?* Usually the answer is no. There is no chi,
gin, viper, zap, cobra or mock framework here, and that is deliberate.

## Architecture

```text
scenario YAML ──► canonical sources ──► per-provider projection ──► wire response
                                             │
                                             ├─► exa      listener :8081  POST /search
                                             ├─► tavily   listener :8082  POST /search
                                             └─► perplexity listener :8083  POST /v1/sonar, /chat/completions

                        admin listener :8080  /healthz  /readyz  /__admin/requests  /__admin/jobs  /__admin/reset
```

Separate listeners are not a stylistic choice — Exa and Tavily both serve `POST /search`, so preserving each
vendor's real path requires one listener per provider.

The design of record is `docs/design/package-design.md`. The requirements it implements are
`docs/architecture-and-implementation-plan.md`.

## The house rules

### 1. The verified contract wins, always

`contracts/<provider>/README.md` records what the vendor's live documentation actually says, with the URLs it was
read from and the date. When any other document in this repository disagrees with it — including the plan — the
contract file is right and the other document is stale. The plan document was written from a snapshot and has
already been wrong about Exa's `score` field, Tavily's `response_time` type, and Perplexity's `usage` shape.

Never write a wire field from memory. Read the contract file.

### 2. Determinism is the product

The same scenario and the same request must produce byte-identical responses, identifiers and ordering. That
means: no `time.Now()` on a response path, no `math/rand`, no `crypto/rand`, no UUIDs, no map iteration order
leaking into output. IDs derive from stable fixture keys. Time comes from an injectable clock.

A flaky simulator is worse than no simulator, because it makes every consumer's test suite flaky and they will
blame their own code first.

### 3. Fail closed, never proxy

An unmatched method, path, provider or scenario returns a provider-shaped error. Servicesim never dials outward.
The `scripts/lint-no-live-hosts.sh` guard exists because the failure mode it prevents — a base URL quietly
resolving to a real paid API — is silent, expensive, and discovered in a billing statement.

Scenario and fixture data may only use reserved domains (`.test`, `.example`, `.invalid`, `example.com`).

### 4. Credentials never survive a round trip

Redaction happens before anything is retained, not on the way out. `Authorization`, `x-api-key`, and JSON
properties whose names indicate credentials are redacted in the journal, in structured logs, and in error
messages. There is no configuration flag that turns this off.

The test that matters is not "does redaction work" but "can a credential reach a retained structure by *any*
path" — headers, body, query string, URL userinfo, or an error that wraps the raw request.

### 5. Strict about requests, tolerant about responses

Validate the request hard: method, route, content type, auth placement, required fields, field types, enum
values. A consumer must be able to prove it sent the *correct vendor request*, not merely that it got a response.

Consumers, by contrast, must tolerate unknown response fields, because real APIs evolve additively. The
`extra-fields` scenario exists to prove they do.

### 6. Process isolation over shared mutable state

Parallel test suites start separate processes or containers. `POST /__admin/reset` is a local-development
convenience, not a concurrency mechanism. Do not add admin endpoints that mutate scenario state — a mutable
admin API becomes hidden shared state between concurrent tests, and the resulting flakes are miserable to debug.

### 7. Consumers pay for every exported symbol

Other repositories import `provider/*`, `scenario` and `testkit` and pin a released version. Anything exported
is a compatibility obligation. Keep the exported surface small; put everything else in `internal/`.

## Commands

```bash
task check          # everything CI gates on: lint, race tests, build, image smoke test
task test           # go test -race -count=1 ./...
task lint           # vet, gofmt, revive, live-host guard
task build          # bin/servicesim
task image:smoke    # build the image, assert non-root + healthy + all listeners answer
```

`task check` is the pre-push gate. CI runs the same steps, so a green `task check` should mean a green CI.

## Lint expectations

revive runs with `warningCode = 1`, so warnings fail the build. In practice that means every exported symbol
needs a doc comment, every package needs a package comment, initialisms are capitalised (`ID`, `URL`, `API`,
`JSON`, `HTTP`), unused parameters are named `_`, and nothing shadows a builtin (`max`, `min`, `cap`, `len`,
`new`).

## Testing

- Table-driven tests; test behaviour and outcomes, not implementation details.
- Explicit synchronisation over sleeps. A test that sleeps to wait for a goroutine is a future flake.
- Fault timing tests use the injectable clock, not real durations.
- Golden wire fixtures cover only the consumed contract and carry their documentation URL and verification date.

## What Servicesim is not

It does not reproduce ranking or semantic relevance, does not generate realistic answers from arbitrary input,
is not a proxy or gateway, does not store real credentials or unsanitised recorded traffic, and does not
implement every field of every vendor. Requests to make it "more realistic" in these directions are out of
scope — the value here is determinism, not fidelity to a vendor's ML behaviour.
