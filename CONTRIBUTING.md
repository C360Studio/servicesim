# Contributing to Servicesim

Servicesim is imported by other repositories' test suites. Everything exported here is a compatibility obligation
someone else pays, and a flaky simulator makes every consumer's suite flaky — so the bar is a little higher than the
size of the codebase suggests.

## The one rule that matters most

**Never write a wire field from memory.** [`contracts/<provider>/README.md`](contracts/README.md) records what the
vendor's live documentation actually says, with the URLs it was read from and the date. It outranks every other
document here, including the design and this file.

This is not theoretical caution. The original plan document — written carefully, by someone with the docs open —
was wrong about Exa's `score` field (it does not exist), Tavily's `response_time` type (number, not string), and
Perplexity's `usage.cost` (required, not optional). All three would have shipped into golden fixtures and taught
consumers to parse things the real APIs never send. See [ADR 0002](docs/adr/0002-verified-contract-precedence.md).

### The same rule applied to consumers

**Never claim what a consumer does or does not call.** Either cite the evidence — the adopter, the repository,
the request in a journal — or say nothing about consumers at all and give the reason that actually applies:
"no verified vendor contract recorded yet", "the scenario model has no shape for this lifecycle", "on the
backlog". A reason about Servicesim is checkable; a reason about somebody else's client is not.

This is not hypothetical either. `contracts/exa/README.md` justified leaving Exa's `/agent/runs` unsimulated with
"no C360 consumer uses it". The first adopter's client calls it. The sentence sat in the directory that
[outranks every other document here](#the-one-rule-that-matters-most), which is exactly what made a casual
assumption read as a verified fact. An unevidenced claim about a consumer is worse than an omission, because it
closes the question.

## Before you push

```bash
task check
```

That is exactly what CI gates on: `gofmt`, `go vet`, revive, the live-host guard, the docs guard, markdownlint,
race tests, the image build and the container smoke test. A green `task check` should mean a green CI.

revive runs with `warningCode = 1`, so warnings fail. In practice: a doc comment on every exported symbol beginning
with the symbol's name, a package comment in `doc.go` only, capitalised initialisms (`ID`, `URL`, `API`, `JSON`,
`HTTP`), unused parameters named `_`, and nothing shadowing a builtin.

Commits follow `<type>(scope): subject` — `feat`, `fix`, `docs`, `refactor`, `test`, `chore`.

## Adding a provider

The scenario schema is an open registry, so this needs **no change to the `scenario` package** and no schema
version bump — the fourth profile, MCP (Phase 8, `provider/mcp`), added a protocol rather than a vendor and
touched nothing under `scenario/`. What it did touch is the checklist below, written from that diff
(`git show 39d5809`) rather than from memory. Tick every box; the guards will tell you about most of the
ones you miss, but not all.

### 1. Verify the contract first — before any Go

- [ ] Fetch the authority: a vendor's OpenAPI document **or** a protocol's machine-readable schema. Prose pages
      describe; the spec decides. Where a rendered page and the schema disagree, record both and say which said
      what.
- [ ] Write `contracts/<name>/README.md`: what is simulated and what consumers parse, each statement citing the
      page it was read from, with the URLs and the date. List what is **not** simulated as a table rather than
      omitting it. Every point where the specification is silent is a **simulator-chosen** decision — record it as
      such, and once the handler ships, record what was chosen beside it.
- [ ] Write `contracts/<name>/provenance.yaml` with a `spec:` block (`url`, `version`, `sha256`, `retrieved`) and a
      provider-level `verified:` date. `TestEveryProviderHasSpecRecorded` will insist.
- [ ] Add the provider's row to `contracts/README.md`'s index table — before a route exists, say so in the row
      (`contracts/mcp/` is the example of a contract recorded before its provider registered; the docs guard checks
      that table against `Routes()` in both directions, so an unregistered route claim fails and a registered
      route with no row fails).

Getting this wrong is the expensive mistake: everything downstream is generated from it, and a wrong field type
propagates into fixtures that then look authoritative.

### 2. Write the package

```text
provider/<name>/
  doc.go       package comment — what is simulated, what is NOT, and every simulator-chosen default, numbered to
               match the contract file's "Simulation decisions"
  handler.go   Name, Pattern*, FaultKey*, Routes() []provider.Route (a function, not a var), New(provider.Deps),
               Validator, selectProjection
  request.go   finding codes (exported), request decoding and validation, checkAuth
  response.go  the wire types, json tags exactly as the vendor names them
  render.go    Projection (yours, in your package) + canonical -> this vendor's shape
  errors.go    this vendor's error envelope, faultBody, the 404/405 bodies for NewMux
```

- [ ] **Your projection type lives in your package**, not in `scenario`. Get it by calling `Turn.DecodeProjection`
      on the turn `provider.SelectTurnFor` chose. That is what keeps `scenario` from importing provider packages.
- [ ] **Validate before you claim.** `SelectTurnFor` claims a fault attempt; every request-side check that needs
      nothing from the projection — auth, headers, method shape, params shape — runs before it, or a rejected
      request spends a scripted fault's budget. `Handle` refuses to let the claimed attempt reach the wire on a
      rejection (`fault.attempt_on_rejection` in the journal), so the rejection no longer wears the fault's
      status — but the claimed index stays spent regardless, so validating first is still what keeps a lane's
      attempt budget matching the calls it actually served.
- [ ] **Implement `provider.Validator`** (and `provider.RouteLister` if your entries use `when.route`) so
      projections are decoded and checked at startup. A bad fixture must fail at boot, not on the first request.
- [ ] **One route, many methods?** `provider.NewMux` keys handlers by pattern, so two routes sharing one pattern
      collide: dispatch on the body inside one handler (MCP dispatches on `body.method`), and give the listener its
      own JSON-shaped 404/405 through `MuxSpec.NotFound`/`MethodNotAllowed`.
- [ ] **`Route.FaultKey` groups aliases.** Two routes that are the same operation share a key so a retry through
      the alias draws on the same attempt budget. Two genuinely different surfaces get different keys.
- [ ] **Register `Response.FaultBody`** on every fault-eligible response so a scripted 429/503 renders in *your*
      envelope, honouring the shared `body:`/`error:` overrides.
- [ ] **Handle the multi-turn form**, not just the single-shot one. They are the same shape after load
      normalisation, so this is usually free — but test it.
- [ ] **Streaming?** Reuse `provider.Stream`/`EncodeSSE` and the two grammars; call `scenario.ValidateStreamScripts`
      and `ValidateStreamFaultMismatch` from your Validator. Add nothing to the transport.
- [ ] **Redaction.** If the protocol defines a header family that carries a value under a wrapper name, check
      `internal/redact` judges it — MCP's `Mcp-Param-*`/`Mcp-Session-Id` needed `stripMirrorPrefix`. Then try to
      get a credential into a retained structure by any path; that is the review lens that finds it.

### 3. Register it — every site, all mechanical

There is no registry; a provider is enumerated by hand in each of these. Every one is a switch or a list keyed on
the name. Missing one is usually a compile error or a failing guard, but not always — `image-smoke.sh` and the
docs tables fail only in CI.

| File | What to add |
|---|---|
| `provider/provider.go` | the `Name` constant |
| `internal/config/config.go` | ten sites: the port default, `DefaultProviders`, `allProviders`, the `Config` field, the env binding, the raw flag target, the flag registration, `assemble`, the validate port table, the `listener()` switch |
| `internal/server/listeners.go` | four switches: the import, `newSurfaces`, `newProviderHandler` (`<name>.New(deps)`), and `scenarioNotFoundBody` — the vendor-shaped body for an `/x/<unknown>` refusal |
| `internal/server/server.go` | `<name>.Routes()` in the routes concat; `<name>.Validator{}` in the entry-kind validators map |
| `testkit/server.go` | the import, `allProviders`, `routes`, `validators`, the `build` switch, a `<Name>Handler` constructor (exported — a compatibility obligation), the `BaseURLs` doc comment (the env var itself derives from the name) |
| `testkit/golden.go` | only if your responses mint an identifier that must be pruned from goldens: `derivedIDPaths` / `streamDerivedIDPaths` |
| `contracts/contracts.go` | the `//go:embed` line, the `Provider` constant, `Providers()`; plus the `byName` map in `contracts/provenance_internal_test.go` |
| `contracts/<name>/` | goldens satisfying `TestEveryProviderHasHappyAndEmptyAndErrorGoldens` (a happy, an empty and an error case), each with a `provenance.yaml` entry (a golden with no provenance fails the build) — and a golden test in your package pinning each to the live handler |
| `scenarios/protocol/*.yaml` | a block in **every** built-in — `TestBuiltins_CoverEveryImplementedProvider` requires it — expressing that file's intent on your surface; `malicious-content` needs every hostile source projected with a marker-bearing field (`TestMaliciousContent_EveryHostileSourceCarriesAMarker`) |
| `scenarios/scenarios_test.go` | `implementedProviders`; `documentedProjectionKeys` (your `respond:` keys, cross-checked with `docs/scenario-schema.md`) |
| `scripts/image-smoke.sh` | a route check and the per-provider journal loop; `Dockerfile` `EXPOSE` (and the description label); `docker-compose.example.yml` port and `*_BASE_URL` |
| `cmd/servicesim/main.go` | the help banner |
| Documents | the listener/port/base-URL tables in `README.md`, `docs/troubleshooting.md`, `docs/architecture-and-implementation-plan.md`, `CLAUDE.md`'s diagram; `docs/scenario-schema.md`'s entry-name list **and** a projection-body section for your keys; `contracts/README.md`'s index row now carries the `METHOD /path` |

The docs guard (`scripts/check-docs.sh`) reads every backticked `METHOD /path` in the scanned docs as a route
claim, every `builtin:<name>` as a scenario name, every backticked `testkit.` or `provider.` symbol as an exported
name, and every dash-prefixed token as a CLI flag — and it checks the contracts index table against `Routes()` in
both directions. Expect it to fail while the tables and the code are half-updated; that is what it is for.

### 4. Prove it

- [ ] **Compare raw JSON bytes, not round-tripped structs.** A wrong JSON type round-trips cleanly through a
      permissive decoder, so a struct-level assertion lets exactly the bugs that matter survive.
- [ ] Prove determinism from the consumer's side: the same request twice is byte-identical.
- [ ] Prove a credential sent on every probe never appears in `/__admin/requests`.
- [ ] Add a worked consumer under `examples/` — a compiled, CI-run test file is the one piece of documentation that
      cannot rot (`examples/mcp_test.go` is the shape).
- [ ] Write the design record under `docs/design/` (what shipped; Go blocks illustrative; the code wins).
- [ ] `task check` — as a plain command, reading the real exit code.

## Changing an existing wire shape

Only in response to a real vendor change, and the contract file and its **Verified** date change in the same
commit. If a re-verification found the drift, say so in the commit message. Provider handler and contract
changes are release-worthy; product-specific scenario changes in consuming repositories are not.

## Releasing: publish the image, then update the docs

**Order matters, and CI enforces it.** `scripts/check-docs.sh` resolves every `ghcr.io` reference in the scanned
documentation against the registry. A commit that documents a tag before that tag is published fails the docs
guard, because at that moment the documentation is wrong: a reader following it gets `manifest unknown`.

So a release is two steps in this order:

1. Tag and let the publish workflow push the image. Confirm the tag resolves.
2. In a follow-up commit, update the version pins in `README.md`, `docker-compose.example.yml` and anywhere else
   the tag appears.

The guard distinguishes an unpublished tag from an unreachable registry: a network failure skips loudly rather
than failing, so a registry outage cannot fail an unrelated pull request. Note also that the metadata action
strips the leading `v`, so both spellings are published against one digest — check the one you are about to
write down, not the one you assume exists. This check was added after that exact mistake shipped twice in one
day.

## Things that will get a change sent back

- A `time.Now()`, `math/rand` or UUID on a response path. Determinism is the product.
- A dependency. The entire non-test dependency set is `gopkg.in/yaml.v3`; tests may add `testify` and `go-cmp`.
  Anything else needs to survive the question *"this goes in every consuming repository's build graph — is it
  worth that?"*
- A test that sleeps to wait for a goroutine. Use explicit synchronisation.
- A credential reachable in the journal, logs or an error by any path.
- An admin endpoint that mutates scenario state. Tests select behaviour by URL against scenarios validated at
  startup; they do not push new behaviour into a live process.
- A real vendor hostname in scenario or fixture data. `scripts/lint-no-live-hosts.sh` will catch it, and the
  failure it prevents — a base URL quietly reaching a paid API — is discovered in a billing statement.
- Widening the exported surface without a reason. Prefer `internal/`.
- A claim about what some consumer does, sends or needs, with nothing behind it. Evidence it or drop it.
- A documentation commit that pins an image tag the registry does not serve yet. Publish first.

## Where to read next

| If you are… | Read |
|---|---|
| using Servicesim in your tests | [`README.md`](README.md), then [`docs/scenario-schema.md`](docs/scenario-schema.md) |
| debugging a failing test | [`docs/troubleshooting.md`](docs/troubleshooting.md) |
| changing Servicesim itself | this file, then [`docs/design/package-design.md`](docs/design/package-design.md) |
| wondering why something is the way it is | [`docs/adr/`](docs/adr) and the deviation register at the end of the package design |
