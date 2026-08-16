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

That is exactly what CI gates on: `gofmt`, `go vet`, revive, race tests, the live-host guard, the image build and
the container smoke test. A green `task check` should mean a green CI.

revive runs with `warningCode = 1`, so warnings fail. In practice: a doc comment on every exported symbol beginning
with the symbol's name, a package comment in `doc.go` only, capitalised initialisms (`ID`, `URL`, `API`, `JSON`,
`HTTP`), unused parameters named `_`, and nothing shadowing a builtin.

Commits follow `<type>(scope): subject` — `feat`, `fix`, `docs`, `refactor`, `test`, `chore`.

## Adding a provider

The scenario schema is an open registry, so this needs **no change to the `scenario` package** and no schema
version bump. Concretely:

### 1. Verify the contract first

Write `contracts/<name>/README.md` before any Go. Fetch the vendor's documentation — prefer a machine-readable
OpenAPI document if one exists, because prose pages describe while the spec decides. Record the URLs and the date.

Getting this wrong is the expensive mistake: everything downstream is generated from it, and a wrong field type
propagates into fixtures that then look authoritative.

### 2. Write the package

```text
provider/<name>/
  doc.go       package comment — say what is simulated and, importantly, what is NOT
  handler.go   New(provider.Deps) http.Handler, and Routes() []provider.Route
  request.go   request decoding and validation
  response.go  the wire types, json tags exactly as the vendor names them
  render.go    canonical sources -> this vendor's shape
  errors.go    this vendor's error envelope
```

Four things that are easy to get wrong:

- **Your projection type lives in your package**, not in `scenario`. Get it by calling `Turn.DecodeProjection` on
  the turn `provider.SelectTurnFor` chose. That is what keeps `scenario` from importing provider packages.
- **Implement `provider.Validator`** so projections are decoded and checked at startup. A bad fixture must fail at
  boot, not on the first request.
- **`Route.FaultKey` groups aliases.** Two routes that are the same operation share a key so a retry through the
  alias draws on the same attempt budget. Two genuinely different surfaces get different keys.
- **Handle the multi-turn form**, not just the single-shot one. They are the same shape after load normalisation,
  so this is usually free — but test it.

### 3. Register it

Three places, all mechanical:

| File | What to add |
|---|---|
| `internal/config` | the listener and its port |
| `internal/server/listeners.go` | `<name>.New(deps)` in the listener switch |
| `internal/server/server.go` | `<name>.Routes()` in the route concat, `<name>.Validator{}` in the validator map |

### 4. Prove it

- Golden fixtures in `contracts/<name>/`, each with a `provenance.yaml` entry. A golden with no provenance fails
  the contracts test — that is deliberate, because an unattributed fixture is unreviewable.
- **Compare raw JSON bytes, not round-tripped structs.** A wrong JSON type round-trips cleanly through a permissive
  decoder, so a struct-level assertion lets exactly the bugs that matter survive.
- Add the provider to every built-in under `scenarios/protocol/` — the guard in `scenarios/scenarios_test.go`
  requires each one to declare every implemented provider — so the existing protocol coverage applies to it too.
  `malicious-content` in particular needs every hostile source projected and a marker-bearing synthesised-answer
  field on the new surface.
- Extend `scripts/image-smoke.sh` with its route.

## Changing an existing wire shape

Only in response to a real vendor change, and the contract file and its **Verified** date change in the same
commit. If a live contract canary found the drift, say so in the commit message. Provider handler and contract
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
