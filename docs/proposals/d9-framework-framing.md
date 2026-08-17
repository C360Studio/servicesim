# D9: is Servicesim a framework now?

## Status

**Decided 2026-08-16: tier 1 adopted and applied (README and CLAUDE.md leads, the non-goals, the Documentation
table); tier 2 deferred until Phase 8's MCP/ODR have exercised the seam; tier 3 open. Evidence recorded 2026-08-16
(Phase 8, the MCP profile in-tree — see "Evidence from Phase 8" at the end); tier 2 awaits the owner.** The text
below is the proposal as the owner read it, kept for the reasoning and for tier 2's shape. It was written to make
decision D9 in [`docs/adopter-backlog.md`](../adopter-backlog.md) concrete enough to decide in one sitting: what
the observation means literally, what the code already is, three independently choosable tiers of what could
change, and a recommendation. The backlog's D9 row points here. Written during the Phase 6 closing docs sweep
(2026-08-16), on branch `phase-6`.

## The observation

Recorded against this unit's spec, verbatim:

> We lean hard on the first three services as the only thing we sim and servicesim is quickly becoming a service
> simulator framework IMO.

Two separate claims live inside that sentence, and they get different tiers below because they have different
costs:

1. **A description claim** — the repository already *is*, mostly, a provider-neutral simulation chassis with
   three provider profiles built on top of it, and the documentation does not currently say so. This costs
   nothing to fix; it is Tier 1.
2. **A capability claim** — if it is a framework, should a *fourth* profile be buildable without a Servicesim
   release, the way a framework's whole point is to admit new users of its chassis without touching the chassis
   itself? That reopens decision D6 and costs an export surface. This is Tier 2.

## What the code actually is today

Measured directly (`wc -l` over tracked `.go` files, non-test vs `_test.go`) at the close of this docs sweep on
`phase-6`, code frozen at `cdd4f3e` — only `testkit/doc.go` and `scenarios/doc.go` changed after that commit,
both doc comments, which is why those two packages' rows below carry the sweep's own +1 and +6 lines — not
estimated:

| Slice | Non-test | Test | Total | Share of all Go |
|---|---:|---:|---:|---:|
| **Provider-neutral core** (below) | 16,583 | 23,812 | 40,395 | **64.6%** |
| **Three provider profiles** (below) | 10,084 | 12,054 | 22,138 | 35.4% |
| **Total** | 26,667 | 35,866 | 62,533 | 100% |

The provider-neutral core, broken out by the packages the observation itself points at, plus the shared
infrastructure that is neutral for the same reason but wasn't named:

| Package | Non-test | Test | Total | Role |
|---|---:|---:|---:|---|
| `provider` (the seam) | 4,064 | 6,263 | 10,327 | Routes, `Deps`, `Clock`, the fault-selection interface, fault execution, the mux builder, `Exchange`, streaming |
| `internal/journal` | 1,275 | 1,468 | 2,743 | The redacted, bounded request journal |
| `internal/faults` | 477 | 882 | 1,359 | Deterministic fault selection |
| `scenario` (schema + the turn model) | 2,938 | 2,773 | 5,711 | The versioned YAML schema, source resolution, the route-addressable turn model |
| `testkit` | 2,165 | 2,457 | 4,622 | In-process consumer helpers, assertions, journal/stream/job accessors |
| `scenarios` (the built-in mechanism) | 147 | 1,984 | 2,131 | `embed.FS` + lookup; the 20 built-in `.yaml` files it serves total 3,290 lines on their own, not counted above |
| `internal/admin` | 904 | 1,227 | 2,131 | `/healthz`, `/readyz`, `/__admin/requests`, `/__admin/namespaces`, `/__admin/scenario`, `/__admin/jobs`, `/__admin/reset` |
| `cmd/servicesim` (the image entrypoint) | 263 | 282 | 545 | Flag parsing, lifecycle, graceful shutdown |
| *(subtotal, spec-named packages)* | *12,233* | *17,336* | *29,569* | |
| `internal/config`, `internal/server`, `internal/httpx`, `internal/wire`, `internal/redact`, `internal/ids`, `internal/jobs` | 3,861 | 5,421 | 9,282 | The rest of the shared chassis — config, mux composition, request-side checks, response rendering, redaction, ID derivation, the async job store |
| `examples/` | 489 | 1,055 | 1,544 | Consumer-facing worked examples (compiled by CI), themselves provider-neutral in structure even though they exercise all three profiles |
| **Neutral core total** | **16,583** | **23,812** | **40,395** | |

The three profiles, for comparison:

| Package | Non-test | Test | Total |
|---|---:|---:|---:|
| `provider/exa` | 3,753 | 4,142 | 7,895 |
| `provider/tavily` | 2,875 | 3,307 | 6,182 |
| `provider/perplexity` | 3,169 | 3,900 | 7,069 |
| `contracts/*` (Go: provenance lookup + its own test) | 287 | 705 | 992 |
| **Profiles total** | **10,084** | **12,054** | **22,138** |

(`contracts/*` also holds 78 golden fixture files — 74 JSON, 4 SSE — three provider READMEs and the index
README, which are data and prose, not counted in the `wc -l` above — they're wire evidence, not chassis code.)

**Reading the numbers:** roughly two-thirds of the tree is already provider-neutral, and that ratio has been
*growing*, not shrinking — Phase 3's job store, Phase 5's SSE transport and Phase 6's fault-execution and pacing
work all landed in the neutral core, built once and shared by whichever profile needed it next, exactly the
pattern a framework has. The three profiles are real and substantial (`provider/tavily` alone is bigger than
`internal/journal` and `internal/faults` combined), but they sit on top of a chassis that was never
provider-specific to begin with. The observation is a description of a fact already true in the code, not a
proposal to change the architecture.

## Three tiers, independently choosable

Each tier can be taken alone; none requires the ones after it.

### Tier 1 — FRAMING only

Documentation changes, no code, no export surface, no compatibility cost. Below is the exact replacement text for
each of the four surfaces the observation calls out, ready to paste.

**README.md lead** (currently: *"A deterministic HTTP simulator for the **Exa**, **Tavily** and **Perplexity**
research APIs. One binary, one image, one listener per provider."*):

```text
A deterministic service-simulator framework — one binary, one image, one listener per provider profile — shipping
three research-API profiles out of the box: **Exa**, **Tavily** and **Perplexity**.
```

The paragraph beneath it ("Point your code's base URLs at it instead...") and the four bullets (Deterministic /
Strict about requests / Fails closed / Credential-safe) need no change — they already describe the neutral core,
not the three profiles, which is exactly the point being made.

**CLAUDE.md lead** (currently: *"A deterministic HTTP simulator for the Exa, Tavily and Perplexity research APIs.
One binary, one image, one listener per provider. It exists so that consuming repositories can test their research
adapters fast, offline, and without spending money on paid APIs."*):

```text
A deterministic service-simulator framework: one binary, one image, one listener per provider profile. It ships
three research-API profiles today — Exa, Tavily and Perplexity — so that consuming repositories can test their
research adapters fast, offline, and without spending money on paid APIs.
```

**CLAUDE.md's "What Servicesim is not"** (currently ends: *"...and does not implement every field of every
vendor. Requests to make it 'more realistic' in these directions are out of scope — the value here is
determinism, not fidelity to a vendor's ML behaviour."*) gains one clause and one sentence, so the non-goals are
stated to hold for the framework, not just the three shipped profiles:

```text
It does not reproduce ranking or semantic relevance, does not generate realistic answers from arbitrary input, is
not a proxy or gateway, does not store real credentials or unsanitised recorded traffic, and does not implement
every field of every vendor — for the three shipped profiles or for any profile added later. Requests to make it
"more realistic" in these directions are out of scope — the value here is determinism, not fidelity to a vendor's
ML behaviour. It is also not a generic mock server: a profile is a verified vendor contract plus deterministic
scenarios, added the way the first three were, not free-form request/response configuration.
```

**README's Documentation table**, the "This README" row currently reads *"Quickstart, base URLs, namespaces, the
admin surface, built-in scenarios."* — append the framing:

```text
Quickstart, base URLs, namespaces, the admin surface, built-in scenarios, and what is provider-neutral versus
profile-specific.
```

Cost: doc-only, reviewable in one pass, reversible by editing four paragraphs back. No ADR needed — it does not
contradict ADR 0001 (repository/listener count) or ADR 0002 (contract authority).

### Tier 2 — SEAM (re-opens D6)

This is the capability claim. Decision D6 already asked "in-tree or out-of-tree" for MCP and ODR and the owner
chose **in-tree**, overriding the recommendation to export the seam first, specifically so Phase 8 would not wait
on a separate build (`docs/adopter-backlog.md`, decision D6 and its reasoning paragraph). D9 asks a related but
different question: once MCP and ODR exist as the second and third real users of the seam (not just
Exa/Tavily/Perplexity), should an *out-of-tree* fourth profile become a supported path, with MCP and ODR staying
in-tree as reference implementations either way?

**What would actually need to be exported.** The backlog's Phase 8 section already enumerates this precisely,
because it was scoped for MCP/ODR regardless of D9:

- **A `provider.Faults` constructor taking a route set** (the backlog's own words). Today `provider.Faults` is
  already an exported interface (`provider/deps.go:71`); what does not exist is a way for an out-of-tree
  provider's routes to register into a fault plan. Without it, "an out-of-tree provider's routes register in no
  plan, so every request returns `FaultDecision{Unknown:true}` and is served fault-free with only a warning — the
  silent-wrong-behaviour class, and the reason the adopter cannot prototype either profile while waiting" (backlog,
  Phase 8).
- **The composition-layer sites**, mechanical and already counted: `internal/config` (8 sites),
  `internal/server/listeners.go` (3 switches), `testkit/server.go` (4 lists), `contracts` (3 sites), plus the image
  and docs surface. `testkit` derives a provider's base-URL environment variable from its name with no mapping
  table, so a fourth profile's env var falls out for free once it is registered (backlog, Phase 8).

`internal/faults.New(s *scenario.Scenario, routes []provider.Route, ...)` already takes a route set — that part
exists today. `testkit.NewFaults(s)` merely calls it with the three profiles' own `routes()` [four since Phase 8]
hardcoded, so the export this tier actually needs is a signature on `testkit.NewFaults` (or a new `provider`-level constructor)
that accepts a caller's route set instead of assuming the three. The compatibility cost below, not the
implementation effort, is what makes this tier a decision rather than an afternoon's work.

**The compatibility cost, under house rule 7** (`CLAUDE.md`: *"Consumers pay for every exported symbol... Keep the
exported surface small"*): a route-set-taking `provider.Faults` constructor and whatever registration function
accompanies it become pinned public API the moment they ship, with every downstream consumer that builds an
out-of-tree profile depending on their exact shape. Today that shape is free to change under implementation,
because only in-tree code calls it. Exporting it converts an internal seam into a compatibility obligation this
repository carries indefinitely — the same trade ADR 0001 already accepted for `provider`, `scenario` and
`testkit`, extended to one more surface.

**What a consumer could do that they cannot today:** build and test a fourth provider profile — a partner API, an
internal-only service — against the shared chassis (journal, faults, redaction, admin, `testkit`) in their own
repository, on their own schedule, without a Servicesim release or a PR into this repository. That is the actual
capability a "framework" framing promises and the current architecture does not deliver: MCP and ODR, even once
built, will still live *in* this repository. The path stays in-process even after this tier ships: `provider.Deps`
plus the route-set-taking `Faults` constructor, with the consumer running its own `httptest` listener — or a
binary the consumer builds themselves. The published `ghcr.io` image and `testkit.Start`'s listener set stay the
three profiles unless `internal/config`, `internal/server` and `internal/admin`'s composition sites are exported
too, which this tier does not propose.

### Tier 3 — POSITIONING (questions only, no recommendation)

These are raised because the observation invites them, not because this sweep has evidence to answer them. Each
is a question for the owner, not a proposed change.

- **The sem\* LLM-mock consolidation idea** (deferred, blocked on semstreams v1) was scoped as absorbing the sem\*
  repositories' hand-rolled LLM-call mocks. Does a "service-simulator framework" framing make Servicesim a more
  natural home for that consolidation — or does it conflate two different kinds of mocking (HTTP API simulation
  of a vendor's *shape*, versus mocking an LLM's *behaviour*) that should stay separate regardless of what this
  repository calls itself?
- **Repository and module naming.** ADR 0001 decided one repository, one module, one binary — for reasons
  specific to the three research APIs (the Exa/Tavily `POST /search` path collision, cross-provider fusion tests
  needing one process). If Tier 2 ships and out-of-tree profiles become a supported path, does the neutral core
  (the "framework") ever need to version independently of the three shipped profiles — i.e., does `servicesim`
  stay one module, or does a framework/profiles split become worth relitigating ADR 0001 for? This is explicitly
  not answered here; ADR 0001's reasoning was never about profile count and nothing above disturbs it.

## Recommendation

**Adopt Tier 1 now. Defer Tier 2 until Phase 8 ships MCP and ODR in-tree. Leave Tier 3 open.**

- Tier 1 costs nothing and fixes a documentation gap that is, on the numbers above, simply true: two-thirds of the
  tree already is the neutral core the observation describes. There is no reason to wait.
- Tier 2's own dependency is Phase 8, not this proposal: exporting `provider.Faults`'s constructor and the
  composition-layer seam *before* a second and third real profile (MCP, ODR) have exercised it means designing an
  extension point speculatively — the same discipline `extended-surfaces.md`'s own scope section applies to
  deferred wire items: *"Do not add one speculatively; add it when a consumer actually parses it."* Phase 3's job
  store is the precedent for doing this correctly: it was built once, at the seam, *because*
  two real consumers (Exa's `/agent/runs`, Tavily's `/research`) needed the same state machine at the same time —
  not before either existed. Shipping MCP and ODR in-tree first, then exporting the seam informed by what both
  actually needed, costs one extra phase of patience and buys a seam shaped by two real profiles instead of a
  guess.
- Tier 3 has no code or documentation dependency on D9 at all; it stays open until semstreams v1 unblocks the
  sem\* question on its own timeline.

**Nothing above is applied by this sweep.** The backlog's D9 row points at this file and is marked open; Tier 1's
paragraphs are drafted so that adopting them, if the owner agrees, is a direct paste rather than a second drafting
pass.

## Evidence from Phase 8 (2026-08-16)

Recorded after Phase 8 unit 2 shipped the MCP profile in-tree (`39d5809`) and unit 3 wrote it up. The full,
file-by-file list is [`docs/design/mcp-profile.md`](../design/mcp-profile.md), §12 "Seam observations for D9 tier
2"; this section is the summary the tier-2 decision reads. ODR is out (D12), so this is the evidence from one
profile, not two — but it is the profile least like the first three (a protocol, not a vendor; one route with
in-handler JSON-RPC dispatch; a server-decided SSE answer; credentials optional by default), which is what makes it
a fair test of the seam.

**What the fourth profile needed from the seam: nothing new.** `provider.Handle`, `Exchange`, `NewMux`,
`SelectTurnFor`, `TurnFault`, `Response.FaultBody`, the SSE `Stream`/`EncodeSSE`/`GrammarDelta` transport, the
scenario package's open `providers:` registry, `when.body_json` nested paths, `turn_key: body_json:<path>`,
`StreamScript`, `ExtraFields`, `AuthPolicy`, the fault engine, the journal, `internal/httpx`, `internal/wire`,
`internal/admin`, and every `testkit` assertion were reused as-is (the job store, `internal/jobs`, is untouched —
MCP does not use it). `MCP_BASE_URL` fell out of
`Sim.BaseURLs` with no mapping table. Catalogue drift, per-method cursors, JSON-vs-SSE, scripted 429/503 with a
JSON-RPC fault body — all expressible in the existing grammar with zero schema work.

**The one framework change:** `internal/redact` now judges the plain name a wrapper header mirrors
(`Mcp-Param-Token` masks as `Token`, `Mcp-Session-Id` as a session id) — a house-rule-4 hardening every profile's
journal benefits from, found by the review lens told to break house rule 4, not an MCP feature.

**What a fourth in-tree profile touched by hand: 41 enumeration sites in 12 code/script/image files** —
`provider/provider.go` (1), `internal/config/config.go` (10), `internal/server/listeners.go` (4, including the
vendor-shaped `/x/<unknown>` body), `internal/server/server.go` (3), `testkit/server.go` (7, one of them a new
exported constructor, `MCPHandler`), `contracts/contracts.go` (3), `contracts/provenance_internal_test.go` (1),
`scenarios/scenarios_test.go` (3), `Dockerfile` (2), `docker-compose.example.yml` (2), `scripts/image-smoke.sh`
(4), `cmd/servicesim/main.go` (1) — plus an `mcp:` block in all 20 built-in YAML files (two guards force it), four
test-expectation files, and seven documents updated by hand (`check-docs` verifies that what they name exists —
flags, routes, symbols — and that the contracts index matches both ways; it does not force a document to mention
a new listener or to agree on its port).
Every one of those sites is a switch or a list keyed on the provider name; none required a design decision, and
every guard that fired (`TestEveryProviderHasHappyAndEmptyAndErrorGoldens`, `TestEveryProviderHasSpecRecorded`,
check-docs' both-direction contracts index, `TestBuiltins_CoverEveryImplementedProvider`) was right to. The one
structural constraint met: `NewMux` keys handlers by pattern, so a many-method profile dispatches in-handler —
which is also what the MCP specification's single endpoint wants, so it cost nothing here.

**What an out-of-tree profile would need exported that is not today:** a `provider.Faults` constructor taking a
route set (`internal/faults.New(s, routes, …)` already takes one; `testkit.NewFaults(s)` hardcodes the four
in-tree `Routes()`; without it an out-of-tree provider's routes register in no plan and every request is served
fault-free with only a `fault.unknown_key` warning — the silent-wrong-behaviour class); the `internal/faults`,
`internal/journal`, `internal/jobs` aliases testkit already re-exports would need to stay; and the composition
switches above — all in `internal/` or in testkit's unexported plumbing — would need an exported registration
shape, or the out-of-tree profile stays an in-process `provider.Deps` + own-listener affair with no image, no
`--providers` flag and no `testkit.Start` listener (the shape tier 2 already described). The `scenario` package,
the `provider` seam and the assertions are exported and, on this evidence, sufficient.

**The tier-2 question, crisply.** Two options; the decision is the owner's.

- **Keep the seam internal (status quo).** A fifth profile is a Servicesim PR touching those ~41 sites, reviewed
  here, released here, shipped in the image. Cost: every new profile waits on this repository; nobody outside it
  can prototype one against the chassis. Benefit: no new compatibility obligation; the composition sites stay
  free to change; a profile is a verified contract plus goldens plus built-ins by construction, because the guards
  force it (CLAUDE.md, "not a generic mock server").
- **Export the seam (re-open D6).** Ship a route-set-taking `provider.Faults` constructor and an exported
  registration shape for the composition sites — or, at minimum, the constructor alone for the in-process path.
  Cost under house rule 7: those signatures become pinned public API the moment they ship, carried indefinitely,
  and an out-of-tree profile is not held to the contract-and-goldens discipline the in-tree guards enforce.
  Benefit: a consumer builds a partner-API or internal-service profile in their own repository, on their own
  schedule, against the shared journal/faults/redaction/testkit — the capability the "framework" word promises.

The evidence says the chassis is already provider-neutral in fact (nothing was added to it for MCP) and that the
remaining cost of a new profile is the 41 mechanical sites plus the discipline the guards impose. Whether that
discipline is a feature to keep in-tree or a friction to export around is not a question the code can answer.
