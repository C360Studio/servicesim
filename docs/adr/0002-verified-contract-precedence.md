# ADR 0002: The verified contract outranks the plan

## Status

Accepted — 2026-08-14. Reaffirmed by the owner on 2026-08-15 (`docs/adopter-backlog.md`'s "Authority rule,
reaffirmed by the owner" paragraph). The one recorded departure, D2's body-placed Tavily `api_key` on
`/search`/`/research` (v0.1.1), stands as shipped and is explicitly not a precedent; every route verified since
follows this ADR as written. **Amended 2026-08-16** (drift-detection mechanism only — no live canary, D10; see the
closing section below. The Decision and Consequences text is left as accepted).

## Context

`docs/architecture-and-implementation-plan.md` specifies what Servicesim must do, and it includes illustrative
request and response examples. Those examples were written from a snapshot of the vendors' documentation taken
before implementation began. Some of them were already wrong when the code started.

That matters more here than it would in an ordinary service. Servicesim's *only* product is the wire shape it
emits. A consumer writes its decoder against what the simulator sends, its assertions against what the simulator
sends, and then ships that decoder to production against the real API. **A wrong field in the simulator is not a
simulator bug — it is a production bug distributed to every consumer, with a green test suite vouching for it.**

Re-verifying every field against live vendor documentation produced three concrete cases that forced the rule:

1. **Exa's `score` field does not exist.** The plan's example scenario and response both carry a top-level
   `score` float on each result. Exa's published result schema has no such field. Emitting it would teach every
   consumer to parse — and quite possibly to rank on — a value the real API never sends. The failure surfaces in
   production as a field that is silently always zero.
2. **Tavily's `response_time` is a number, not a string.** The plan encodes it as `"1.15"`. Tavily's schema
   declares `type: number, format: float`. Every typed consumer breaks on the string: a Go `float64` field fails
   to unmarshal, a Pydantic `float` raises. This is a test suite that passes against the simulator and fails on
   the first real call.
3. **Perplexity's `usage.cost` object is required and the plan omits it.** The plan documents `usage` as exactly
   three token counts. The live schema requires a `cost` object as well. A consumer validating the response
   against the real schema *rejects the simulator's response* for a missing required field — so the simulator is
   not merely incomplete, it is unusable for the cost-tracking consumers that most need it.

Each of these was caught by reading the vendor's documentation rather than the plan. None would have been caught
by a test, because a simulator agrees with itself by construction: handler, golden fixture and consumer assertion
all derive from the same mistaken belief.

## Decision

**`contracts/<provider>/README.md` is the authority on every wire field. When any other document in this
repository disagrees with it — including the plan and the package design — the contract file is right and the
other document is stale.**

Concretely:

- Every contract file records the documentation URLs it was derived from and the date the shape was verified.
  Provenance is part of the artefact, not a commit message.
- **No wire field is ever written from memory.** Implementing a field means opening the contract file first.
- Golden JSON fixtures live in `contracts/<provider>/` beside the contract they encode, and every golden has an
  entry in `provenance.yaml` recording its documentation URL and verification date. This repository's own
  contract test enforces that; a golden with no provenance fails the build.
- A body Servicesim invents because the vendor publishes no shape — Perplexity's non-422 Sonar error bodies, for
  instance — is recorded as `simulator-chosen`, so it is visibly unverified rather than indistinguishable from a
  verified one.
- Every departure from the plan is recorded in the design's verified-contract deviation register, with what the
  plan said, what the design does, and the consequence of having followed the plan. The register is the audit
  trail; twenty-eight entries in, it is also the evidence for this ADR.
- Drift is detected by a live contract canary that makes one bounded request per provider against the real API on
  a schedule, validates only the consumed fields, and fails on removed or incompatible ones. Additive fields are
  reported without failing, because vendors evolve additively and consumers are required to tolerate that.
- When the canary reports drift: update the contract file and its verification date, update the handler and its
  goldens in the same change, and cut a release.

## Consequences

### Positive

- The question a reviewer actually has when a fixture changes — *did the vendor change, or did we?* — is
  answerable, because the dated provenance record exists.
- Consumers can trust that a field the simulator emits is a field the vendor emits. That trust is the entire
  value proposition; without this rule the simulator is a shared fiction.
- Disagreements between documents are resolved by a rule rather than by whoever is arguing. There is no debate
  about which document wins.
- Vendor deprecations become assertable rather than invisible: a deprecated field can be accepted and warned
  about, so a consumer can prove it has stopped sending it.

### Negative

- Implementation is slower. Reading a vendor's schema is more work than copying the plan's example, and the
  contract files have to be written and maintained.
- Verification dates decay. A contract file is a claim about a moment in time, and without the canary it silently
  becomes as stale as the plan it replaced. The rule is only as good as the canary that maintains it.
- The plan document now contains examples that are knowingly wrong. They are left in place with the deviation
  register pointing at each one, rather than edited, so that the history of the decision survives — but a reader
  who finds the plan first can still be misled. Every document that quotes a wire field carries a pointer to
  `contracts/` for this reason.

### Neutral

- Fields the vendor documents but no consumer parses stay unimplemented. The contract is the **consumed**
  contract, not the whole vendor surface; adding a field is a bounded change made when a consumer actually needs
  it.

## Amended 2026-08-16

The Decision and Consequences sections above are the accepted text and are left as written — this section
supersedes only the drift-detection mechanism they describe, and records why.

**There is no live contract canary, and none is built or planned (D10, `docs/adopter-backlog.md`).** A canary is
outbound infrastructure and a scheduled dependency on vendor availability, for a test simulator whose entire
value is determinism (house rule 2) — in the spirit of house rule 3's fail-closed, never-dials-outward design,
even though that rule governs the served process, not repository tooling. Building the thing this ADR's own
Decision section describes as the drift-detection mechanism would work against the property it is meant to serve.

Drift detection instead is a **dated, manual re-verification** against a recorded reference. Every provider's
contract has a machine-readable specification behind it — Exa's `exa-spec.yaml`, Tavily's `openapi.json` and
Perplexity's `openapi.json`, each covering every route this repository simulates for that vendor — and each
`contracts/<provider>/provenance.yaml` records that document's version and SHA-256 (`contracts.Spec`, the `spec:`
block) that a fresh fetch is compared against, as the first, cheap step. That hash comparison is a drift SIGNAL,
not a substitute for reading: most entries in every provider, Perplexity included, are verified against the
vendor's rendered prose pages (each entry's own `documentation_url`), which have no stable byte hash of their own —
a page's bytes change with every site deploy independent of whether the content that matters changed. A changed
spec hash means a person re-reads the consumed fields against both the cited `documentation_url` pages and the spec
itself; only entries whose `documentation_url` IS the spec (all of Perplexity's, and Exa's three `/findSimilar`
entries) were read from the spec directly. `contracts/README.md` "Keeping them honest" is the sanctioned procedure
for all three providers.

This changes nothing about the Decision's central claim — the contract file, not the plan, is the authority on a
wire field — and nothing about how a correction is applied once found: update the contract file and its
verification date, update the handler and its goldens in the same change, cut a release. It changes only how
drift is *found*: on a person's dated schedule, backed by a mechanically comparable hash where one exists, rather
than by an automated, scheduled outbound probe.

The Consequences section's closing line — "the rule is only as good as the canary that maintains it" — is
corrected in the same spirit: **the rule is only as good as the re-verification cadence and the recorded hash
that make staleness visible.**
