# ADR 0003: The framework seam

## Status

Accepted — 2026-08-17 (owner). Shipped across `phase-10` units 0–9 (nine commits, `4d10178`..`71a9a5e`), merging
into `main` as v0.5.0. Supersedes the parts of [ADR 0001](0001-single-repository.md) recorded in its own "Amended
2026-08-17" section; does not reopen the one-repository conclusion.

## Context

The decision this ADR records, verbatim (owner, 2026-08-17, `docs/proposals/framework-seam.md`):

> as soon as you try to take over mocks for sem\* it's a framework. not to mention the three providers we started
> with are not even close to the number of providers our early adopter will end up with. so yes, we need to
> commit to being a proper framework so we do not need to babysit PRs — treat what we have as examples.

This is D9's tier 2, decided (`docs/proposals/d9-framework-framing.md`, tier 1 was framing-only and shipped in
Phase 6; tier 2 — exporting the provider seam so a profile is buildable without a Servicesim release — was
deferred until Phase 8 exercised a fourth profile in-tree, then decided here). Three audits measured what an
out-of-tree profile needed and could not get: no profile's non-test code reached below `internal/{wire, ids,
httpx, journal}`, six exported `provider` declarations named an unreachable internal type, and the fault-engine
constructor `testkit` then exposed built its engine from a hardcoded concatenation of the four in-tree
`Routes()` — an out-of-tree route was fault-blind, silently serving a clean 200 where a scenario scripted a 429.
`docs/proposals/framework-seam.md` is the full design history; this record is what shipped.

## Decision

**`provider.Profile` and `provider.Set`.** `Profile` is the registration record a vendor's package returns from
its own `Profile()` function: `Name`, `Kind`, `Title`, `Summary`, `Port`, `Handlers`, `Routes`, `Validators`,
the required `ErrorBody`, `DefaultAuth`, `Announce`, `Contracts`, `Hosts`, `DerivedIDs`/`StreamDerivedIDs`,
and `CredentialNames` — sixteen in all — every field cites the composition-layer enumeration site it replaces
(`provider/profile.go`). `provider.NewSet`/`MustSet` build an ordered, validated, immutable `*Set` — the ONLY
input to composition — refusing a missing `ErrorBody`, a duplicate name or port, an unknown `DefaultAuth`, or one
scenario entry kind claimed by two different `Kind`s. `(*Set).Faults` is the one exported fault-engine
constructor, so an engine that does not know a registered route is unconstructible (`67053f65`).

**The root `servicesim` composition package.** `servicesim.Main`/`Run`/`Build` are what `cmd/servicesim/main.go`
now wraps: `os.Exit(servicesim.Main(servicesim.Build{...}, provider.MustSet(profiles.Reference()...)))`.
`internal/config` and `internal/server` derive every flag, listener, and readiness check from the `*Set` they are
handed; neither imports a profile package (`92008a33`). A consumer's own binary is the same three lines with its
own profile list.

**`profiles/<name>` as reference examples.** The four shipped profiles moved from `provider/<name>` to
`profiles/{exa,tavily,perplexity,mcp}` (`9f08da03`), each a package returning its own `Profile()` and embedding
its own `contracts/` bundle. `profiles/no_privilege_test.go` parses every non-test file under `provider/`, `internal/`,
`testkit/`, `scenario/`, `contracts/` and the repository root and fails the build if any imports a profile
package — a reference profile has no privilege an out-of-tree one lacks, proved by construction rather than by
review. `profiles.Reference()` (`profiles/profiles.go`) is the single place that names all four;
`cmd/servicesim/main.go` calls it and names none of them, and `TestReference_NamesAndOrder` pins the four names
and the order main registers them in.

**Guards as libraries.** The in-tree discipline is now callable, not merely followed: `testkit.ValidateProfile`
(`conformance.go`) runs `NewSet`, `contracts.Conform` over `Profile.Contracts`, `ErrorBody` for every
`RefusalKind`, unknown-path/wrong-method/wrong-content-type/missing-credential checks, fault-key resolution,
`AssertDeterministic` (two fresh simulators, byte-compared) and `AssertRenderShape` (a heuristic for the two
`encoding/json` divergences `provider.Render` exists to prevent) — all in one call (`cfc5e190`).
`testkit.AssertNoLiveHosts` travels the reserved-host guard into a consumer's own CI, seeded from
`(*Set).LiveHosts()` and the framework's own paid-host base list; `testkit.AssertNoCredentialLeak` travels the
no-credential-leak guard, scanning the journal for whatever secret literals the consumer's suite actually sends
— `(*Set).CredentialNames()` feeds redaction, not this assertion (`72610932`).
`testkit.AssertCovers` is the scenario-coverage guard. `contracts.Conform` and `contracts.Read`/`Goldens`/
`Provenance`/`ProviderSpec`/`OldestVerified` are genericised over `fs.FS`; the closed `Provider` enum and the
global `VerifiedOn` are deleted (`cfc5e190`).

**What stays internal, and why.** `internal/{admin, config, server, redact, wire, ids, httpx, journal, jobs,
paidhosts}` gained zero new importable packages — and `internal/faults` is gone entirely, the engine now being a
`provider` file built by `(*Set).Faults`. `internal/redact` stays unreachable so redaction remains a property
of the retention boundary, not of review (rule 4); `internal/journal` and `internal/jobs` stay behind
`provider.Finding`/`Credential` and `MintJob`/`ResolveJob`'s narrowed signatures (`(string, bool)`/`bool`, no
`jobs.Job`) so a consumer can never hold an unnameable type (`8524fc35`); `internal/admin` has no `Profile` field
and `servicesim.Run` accepts none — the admin surface is framework-owned and closed to composition (D-3, below);
`internal/config`/`internal/server` stay internal because `servicesim.Run` is the whole contract exporting them
would otherwise fragment.

**House rules by construction:**

- Rule 2 (determinism) — `provider.Hex32`/`UUIDv5`/`FloatIn` read no clock, and `(*Set).Faults` being the only
  engine constructor closes the fault-blind-route class live (`67053f65`).
- Rule 3 (fail closed) — `Profile.ErrorBody` is required; `NewSet` refuses a `Profile` without one, closing the
  empty-404/405 and nil-panic gaps `NewMux`'s old defaults and the deleted `internal/server` MCP-envelope
  duplicate left (`67053f65`).
- Rule 4 (credentials) — `Exchange.Auth` is unexported, `ObserveCredential` is the only write path into a
  retained credential, and `Profile.CredentialNames` widens redaction as data, never as a call (`8524fc35`,
  `72610932`).
- Rule 5 (strict requests) — the memoised-fault fix (`CodeAttemptOnRejection`) stops a rejected request from
  wearing a claimed fault attempt's status, shipped first and independently as unit 0 (`4d10178`).
- Rule 6 (process isolation) — `Set` is immutable, built at the composition root with no `init()` or
  package-level registry; the admin surface carries no `Profile` field at all (D-3), so this is structural, not a
  review gate that disappears with the PR.
- Rule 7 (consumers pay) — zero new importable packages for the six audited; the reference profiles' own exported
  surface is trimmed to what a consumer actually needs to name (`9f08da03`).

## Consequences

**The eleven decisions, as taken (2026-08-17, all per the proposal's recommendation):**

- D-1 `Profile.Kind` shipped in v0.5.0, single-entry instancing only (`72610932`).
- D-2 no production journal substitute in v0.5.0; `Deps.Journal`/`Deps.Jobs` stay typed on internal interfaces.
- D-3 the admin surface is closed to composition — no admin-route field on `Profile`, none on `Run`.
- D-4 the four moved to `profiles/<name>` (`9f08da03`).
- D-5 `testkit.WithProfiles` is required; `testkit.Start(t)` with no profiles is a build error.
- D-6 `CLAUDE.md`'s "What Servicesim is not" line is re-earned, not rewritten (this unit).
- D-7 built-in scenarios stay reference-only; `scenario.profile.unscripted` is the whole accommodation.
- D-8 v0.5.0 holds all of units 0–10.
- D-9 one module; ADR 0001 amended, not split; tier 3's module question stays open.
- D-10 the 1.0 trigger is written into `README.md` (Phase 10 docs sweep).
- D-11 the sem\* `chat/completions` profile lands out-of-tree, in a sem\* repository; its sequencing against
  semstreams v1 is the owner's, tracked on the adopter backlog.

**Compatibility stance.** v0.5.0 is the break, taken once rather than incrementally. The checkable reason it
could be: v0.4.0 predates the seam entirely, so every symbol units 0–9 renamed or unexported was pre-seam
surface, and the adopter has not yet been pointed at a release at all (`docs/adopter-backlog.md`, "Start here"
item 7 — telling them v0.3.0 and v0.4.0 exist is still an open step). Pre-1.0 with a written, testable trigger (`docs/proposals/framework-seam.md`,
"Compatibility and versioning"; carried into `README.md`): at least one profile written by someone who has not
read this repository must ship and survive a framework minor release without source changes, and the
`chat/completions` profile must have landed. Until then a `v0.x` pin means the seam may still move.

**What a consumer now does without a PR here.** Writes a `provider.Profile` against six importable packages
(`provider`, `scenario`, `testkit`, `contracts`, `scenarios`, the root `servicesim`), embeds their own contract
bundle, composes `servicesim.Main` over their own `provider.MustSet`, and runs `testkit.ValidateProfile` plus
`contracts.Conform` in their own CI — `docs/building-a-profile.md` and the compiled `examples/profile/` module are
the worked instance.

**What they still cannot.** An admin route of their own (D-3) — `/healthz`, `/readyz`, `/__admin/*` are
framework-owned and closed, on purpose: a mutable admin API is hidden shared state between concurrent test
suites (house rule 6). A built-in scenario naming their profile (D-7) — `builtin:happy` must mean the same bytes
in every build; `scenario.profile.unscripted` is the diagnosis, not an overlay. And the turn model's current
limits, which block the sem\* mock specifically: a turn key counting array elements matching a predicate, a
substring predicate folded over `messages[]`, and a response projecting a field from the request's own body are
all unrepresented today (`docs/proposals/framework-seam.md`, risk 7) — `Kind` and the opened `Stream` grammar
solve the multiplicity and framing halves of that workload; the turn-model gap needs its own design pass.

**Links:** [`docs/proposals/framework-seam.md`](../proposals/framework-seam.md) (the design history);
[`docs/building-a-profile.md`](../building-a-profile.md) (the guide); [`examples/profile/`](../../examples/profile)
(the compiled proof); [ADR 0001](0001-single-repository.md) (amended alongside this record).
