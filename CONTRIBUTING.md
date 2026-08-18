# Contributing to Servicesim

Servicesim is imported by other repositories' test suites. Everything exported here is a compatibility obligation
someone else pays, and a flaky simulator makes every consumer's suite flaky — so the bar is a little higher than the
size of the codebase suggests.

## The one rule that matters most

**Never write a wire field from memory.** [`profiles/<provider>/contracts/README.md`](contracts/README.md) records
what the vendor's live documentation actually says, with the URLs it was read from and the date (the
repository-root `contracts/` package README linked here is the index across the four bundles). It outranks every
other document here, including the design and this file.

This is not theoretical caution. The original plan document — written carefully, by someone with the docs open —
was wrong about Exa's `score` field (it does not exist), Tavily's `response_time` type (number, not string), and
Perplexity's `usage.cost` (required, not optional). All three would have shipped into golden fixtures and taught
consumers to parse things the real APIs never send. See [ADR 0002](docs/adr/0002-verified-contract-precedence.md).

### The same rule applied to consumers

**Never claim what a consumer does or does not call.** Either cite the evidence — the adopter, the repository,
the request in a journal — or say nothing about consumers at all and give the reason that actually applies:
"no verified vendor contract recorded yet", "the scenario model has no shape for this lifecycle", "on the
backlog". A reason about Servicesim is checkable; a reason about somebody else's client is not.

This is not hypothetical either. `profiles/exa/contracts/README.md` justified leaving Exa's `/agent/runs` unsimulated with
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

## Writing a profile in your own repository

This is the path almost everyone reading this file wants. If your vendor is not one of the four this repository
ships, you do not need a pull request here at all — see "Adding a reference profile here" below only if your
profile is genuinely worth maintaining inside this repository.

**What a profile is**, in two lines: a verified vendor contract plus deterministic scenarios, written against
`provider.Profile` in your own repository, with your own embedded contracts and your own scenario files. **What
proves it**: `testkit.ValidateProfile` and `contracts.Conform` pass in your own CI, over your own `Profile()` and
your own contract bundle. There is no registration step in this repository, no PR and no review by this
framework's authors — nothing stands between "a Go package that answers HTTP" and "a Servicesim profile" except
that conformance call passing.

[`docs/building-a-profile.md`](docs/building-a-profile.md) is the guide, written against
[`examples/profile/`](examples/profile) — a separate Go module this repository's own `task test` and CI build and
run, so its code blocks cannot rot. Starting your own is three lines:

```sh
go mod init example.test/yoursim
go get github.com/c360studio/servicesim@latest
go mod tidy
```

Six packages are the framework surface: `provider`, `scenario`, `testkit`, `contracts`, `scenarios` (this
repository's built-in corpus, reference-only — see D-7 below) and the root `servicesim` package (`Main`, for
composing a binary) — plus `profiles/<name>` for any of ours you want to compose alongside your own. Everything
else is under `internal/` and unreachable by Go's own import rules. The four reference profiles this repository
ships carry no privilege yours would lack — `profiles/no_privilege_test.go` proves it by parsing every non-test
file under `provider/`, `internal/`, `testkit/`, `scenario/` and `contracts/`, and the repository root, and
failing the build on an import of a profile package.

**The seam is pre-1.0.** Read `docs/building-a-profile.md`'s "The 1.0 trigger" before pinning a version: until at
least one profile written by someone who has not read this repository has shipped and survived a framework minor
release without source changes, and the `chat/completions` profile has landed, a `v0.x` pin means the seam may
still move.

## Adding a reference profile here

The scenario schema is an open registry, so this needs **no change to the `scenario` package** and no schema
version bump — the fourth profile, MCP (Phase 8, `profiles/mcp`), added a protocol rather than a vendor and
touched nothing under `scenario/`. What it did touch is most of the checklist below, written from that diff
(`git show 39d5809`) rather than from memory; the registration step itself has since collapsed to one line
([ADR 0003](docs/adr/0003-framework-seam.md), Phase 10). Tick every box; the guards will tell you about most of
the ones you miss, but not all.

### 1. Verify the contract first — before any Go

- [ ] Fetch the authority: a vendor's OpenAPI document **or** a protocol's machine-readable schema. Prose pages
      describe; the spec decides. Where a rendered page and the schema disagree, record both and say which said
      what.
- [ ] Write `profiles/<name>/contracts/README.md`: what is simulated and what consumers parse, each statement
      citing the page it was read from, with the URLs and the date. List what is **not** simulated as a table
      rather than omitting it. Every point where the specification is silent is a **simulator-chosen**
      decision — record it as such, and once the handler ships, record what was chosen beside it.
- [ ] Write `profiles/<name>/contracts/provenance.yaml` with a `spec:` block (`url`, `version`, `sha256`,
      `retrieved`) and a provider-level `verified:` date. `testkit.ValidateProfile` (via `contracts.Conform`)
      checks it is well-formed when present; add your own `TestHasASpecBlock` beside the other three reference
      profiles' if the vendor publishes one, the way `profiles/exa/contract_test.go` does.
- [ ] Add the provider's row to `contracts/README.md`'s index table — before a route exists, say so in the row
      (`profiles/mcp/contracts/` is the example of a contract recorded before its provider registered; the docs guard checks
      that table against `Routes()` in both directions, so an unregistered route claim fails and a registered
      route with no row fails).

Getting this wrong is the expensive mistake: everything downstream is generated from it, and a wrong field type
propagates into fixtures that then look authoritative.

### 2. Write the package

```text
profiles/<name>/
  doc.go               package comment — what is simulated, what is NOT, and every simulator-chosen default,
                       numbered to match the contract file's "Simulation decisions"
  profile.go           Name, defaultPort, //go:embed contracts, Profile() — every field a real profile sets
  handler.go           Pattern*, FaultKey*, Routes() []provider.Route (a function, not a var), the Validator,
                       the route handlers, selectProjection
  request.go           finding codes (exported), request decoding and validation, checkAuth
  response.go          the wire types, json tags exactly as the vendor names them
  render.go            Projection (yours, in your package) + canonical -> this vendor's shape, via provider.Render
  errors.go            this vendor's error envelope, ErrorBody for every provider.RefusalKind, faultBody
  conformance_test.go  testkit.ValidateProfile(t, Profile())
  contracts/           README.md, provenance.yaml, goldens
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
      collide: dispatch on the body inside one handler (MCP dispatches on `body.method`). Your listener's own
      JSON-shaped 404/405 comes from `Profile.ErrorBody`, which `provider.NewSet` refuses to register without —
      `MuxSpec` itself no longer carries a `NotFound`/`MethodNotAllowed` field (Phase 10).
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

### 3. Register it

**The registration is one line.** `profiles/profiles.go`'s `Reference()` returns the four reference profiles in
registration order; add `<name>.Profile()` to that slice and nothing else compiles a provider by hand. Everything
that used to be a hand-kept switch derives from the registered `*provider.Set` (Phase 10 units 3–4, ADR 0003):
`internal/config` and `internal/server` build every flag, listener and readiness check from it, `testkit` derives
routes, validators, the fault engine and every handler from it (pass your `Profile()` to `testkit.WithProfiles`
and it is served through the one generic `testkit.Handler`), and `cmd/servicesim/main.go`'s usage banner comes
from `Profile.Title`/`Summary`, not a line written per vendor. Golden pruning is the same: declare
`Profile.DerivedIDs`/`StreamDerivedIDs` and a caller prunes them with `testkit.GoldenDerivedIDs(...)` — nothing
in `testkit/golden.go` names a provider.

That one line is what the guards force. It does not force any of the following, and missing one is usually a
failing test or a failing guard, but not always — `image-smoke.sh` and the docs tables fail only in CI:

| File | What to add |
|---|---|
| `profiles/<name>/contracts/` | goldens satisfying `contracts.Conform`'s coverage minimum (a happy, an empty and an error case), each with a `provenance.yaml` entry — a golden with no provenance fails the build — and a golden test in your package pinning each to the live handler |
| `profiles/<name>/conformance_test.go` | `testkit.ValidateProfile(t, Profile())` — the same call an out-of-tree profile's own CI makes, held to no lesser standard |
| `scenarios/protocol/*.yaml` | a block in **every** built-in — `TestBuiltins_CoverEveryImplementedProvider` requires it, because built-in scenarios are reference-only (D-7, ADR 0003) and cover only the profiles shipped here; `malicious-content` needs every hostile source projected with a marker-bearing field (`TestMaliciousContent_EveryHostileSourceCarriesAMarker`) |
| `scenarios/scenarios_test.go` | `implementedProviders` — still a hand-kept list, so this cross-check knows which entries every built-in must declare; `documentedProjectionKeys` is now derived from `profiles.Reference()` and needs nothing from you |
| `scripts/image-smoke.sh` | a request against your listener under criterion 2 — the per-provider journal loop is derived from `--print-ports` and will demand a journal entry for your profile whether or not you add one |
| `Dockerfile`, `docker-compose.example.yml` | `EXPOSE`, the port map, and the `*_BASE_URL`/`*_API_KEY` rows — check these against `bin/servicesim --print-ports`, which is the source of truth even though the files themselves stay hand-written (the smoke probes are not generated either — see `docs/proposals/framework-seam.md`, "Risks and what is deliberately NOT exported") |
| Documents | the listener/port/base-URL tables in `README.md`, `docs/troubleshooting.md`, `docs/architecture-and-implementation-plan.md`, `CLAUDE.md`'s diagram; `docs/scenario-schema.md`'s entry-name list **and** a projection-body section for your keys; `contracts/README.md`'s index row now carries the `METHOD /path` |

The docs guard (`scripts/check-docs.sh`) reads every backticked `METHOD /path` in the scanned docs as a route
claim (checked against `servicesim --print-routes`), every `builtin:<name>` as a scenario name, every backticked
`testkit.` or `provider.` symbol as an exported name, and every dash-prefixed token as a CLI flag — and it checks
the contracts index table against the printed routes in both directions. Expect it to fail while the tables and
the code are half-updated; that is what it is for.

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
- An admin endpoint that mutates scenario state — though this is no longer a review gate to fail. The admin
  surface is framework-owned and closed to composition (D-3, ADR 0003): `provider.Profile` has no admin-route
  field and the composition root accepts none, so there is structurally nowhere to add one, in a reference
  profile or anywhere else. Tests select behaviour by URL against scenarios validated at startup; they do not push
  new behaviour into a live process.
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
