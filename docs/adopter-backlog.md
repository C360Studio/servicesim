# Adopter backlog and delivery plan

The first adopter reviewed Servicesim against their target architecture and returned a gap backlog, verified against
their own client code. This document is the durable record of that backlog, the evidence-based status of each item,
the phased plan, and the decisions already taken. It exists so the work can be picked up cold.

## Where this stands — read this first

Recorded 2026-08-16 (evening), against **v0.4.0**, tagged from `main` at `b788f00` — the merge of Phase 6 — the same day.

| Phase | State |
|---|---|
| 0 — stop rejecting valid traffic | **shipped** in v0.1.1 |
| 1 — schema-envelope changes | **shipped** in v0.2.0 |
| 2 — revise the two design documents | **DONE** — both documents are now records of what shipped |
| 3 — the async job machine | **shipped** in v0.2.0 (A1–A7) |
| 4 — the remaining synchronous routes | **shipped** in v0.3.0 |
| 5 — SSE streaming for the Perplexity deep-research path | **shipped** in v0.3.0 — units 1–4; concise mode and the reasoning events deliberately deferred |
| 6 — G-3 depth: hostile content, oversized bodies, hangs after headers, resilience built-ins, pacing evidence | **shipped** in v0.4.0 — units 1–6, the closing docs sweep, D9 tier 1; the adopter's own guardrail vectors are still an append to `malicious-content` when they arrive |
| 7 — packaging, deployment and the contract-fidelity process | **DONE (rescoped), on `phase-7`, not yet merged** — items 1–2 (the Gitea mirror, the Kubernetes manifest) dropped 2026-08-16 (owner, D3 reworded); items 3–5b shipped, this pass adding item 4 (`contracts.Record.APIVersion`, `contracts.Spec`, `contracts.ProviderSpec`) and item 5b (the refresh procedure rewritten around D10 — no live canary, ever) |
| 8 onward | open — **Phase 8 (MCP/ODR) is next**; D9 tier 2 (the seam export) is decided after it; the adopter's own guardrail vectors when they arrive |

**v0.4.0** is the last tag (2026-08-16, `b788f00`): Phase 6 end to end — `completed_at` observing every scripted
hang, the `malicious-content`, `oversized-body`, `timeout`, `brownout`, `hang-then-abort` and `credential-rotation`
built-ins, `oversized_body`/`body_bytes:` and `delay_after_headers:`, `AssertDifferentCredential`, `AssertMaxRate`,
`AssertMinGap` and `AssertObservedDuration`, the docs sweep, and D9 tier 1 (Servicesim describes itself as a
service-simulator framework shipping three research-API profiles). Two behaviour changes a consumer holding v0.3.0
assertions could notice are in the tag note: Tavily's `faultBody` serves its error envelope for any kind paired with
an error status, and `POST /research` warns `tavily.api_key.in_body` as `/search` does. The annotated tag message is
the release note; every spelling resolves to one digest (`sha256:5a7d6d05…`) and the README/Compose pins follow it.
`main` is where work lands now; `phase-6` is merged.

**Authority rule, reaffirmed by the owner 2026-08-15 evening:** vendor documentation decides every wire contract; the
adopter's client code and remarks are not evidence ("we have no idea if their client works — we use the vendor
docs"). D2 stands as shipped and is not a precedent; the questions inlined for the adopter in the contract files
are informational — they tell us where the adopter diverges from the vendor, they do not change what a route
accepts. The Phase 5 backlog line "read the adopter's `perplexity.go` as evidence of the real wire shape" was struck
under this rule; the SSE contract was recorded from `docs.perplexity.ai` alone.

### Start here

1. ~~**Cut v0.3.0**~~ **DONE 2026-08-16.** Tagged from `ae4c8e6` with the annotated message as the release note (no
   CHANGELOG, no GitHub Release object — v0.1.1 and v0.2.0 set that convention), published, both spellings confirmed,
   pins moved in a follow-up commit — the CONTRIBUTING.md "Releasing" order.
2. ~~**Phase 6 — G-3 depth**~~ **DONE — merged to `main` (`b788f00`) and shipped in v0.4.0.**
   Unit 1 (the journal `CompletedAt` defect: stamped before the delay on aborting faults, so `observed_ms` read 0
   on a hang) is done; unit 2 (the generic `malicious-content` built-in) is done; unit 3 (the `oversized_body`
   fault kind and the `oversized-body` built-in) is done; unit 4 (the `timeout`/`brownout`/`hang-then-abort`/
   `credential-rotation` built-ins and `testkit.AssertDifferentCredential`) is done; unit 5 (`delay_after_headers`,
   the fault modifier and its `hang-then-abort` third attempt) is done; unit 6 (the request-level pacing
   assertions per D5 — `testkit.AssertMaxRate`, `testkit.AssertMinGap`, `testkit.AssertObservedDuration` —
   plus a consumer-facing example) is done. The closing docs sweep for onboarding and correctness (owner,
   2026-08-16) is done, and it wrote up the D9 framing question as a concrete proposal —
   [`docs/proposals/d9-framework-framing.md`](proposals/d9-framework-framing.md) — and the owner decided the same
   day: **tier 1 (framing) is applied, tier 2 (the seam, re-opening D6) waits for Phase 8, tier 3 stays open.**
   PR #2 is merged and v0.4.0 is cut. Trickle bodies now have
   Phase 5's chunked-write path, and unit 5's after-headers seam, to sit on. Note the over-redaction defect listed
   in the Phase 6 section below was fixed in v0.1.1 already — verify before re-fixing.
3. ~~**Phase 7 — packaging, deployment and the contract-fidelity process**~~ **DONE (rescoped), 2026-08-16, on
   `phase-7`.** Items 1 (the Gitea mirror) and 2 (the Kubernetes manifest) are dropped — the adopter's deployment,
   not this repository's; the guidance that matters already ships (README "Single replica by design",
   troubleshooting, the Compose example, the startup log, `job.foreign_id`), so D3 is reworded to say that rather
   than promise a manifest. Item 3 shipped 2026-08-15. Items 4 and 5b ship in this pass: `api_version` on
   `contracts.Record`, `contracts.Spec`/`contracts.ProviderSpec` with Perplexity's `openapi.json` version and
   SHA-256 recorded for real, and `contracts/README.md` "Keeping them honest" rewritten around **D10** — there is
   no live contract canary, and none is planned; drift detection is dated, manual re-verification against the
   recorded hash (Perplexity) or the cited documentation pages (Exa, Tavily). ADR 0002 carries an "Amended
   2026-08-16" section recording the same change. Next: Phase 8 (the MCP listener and the ODR profile), after
   which D9 tier 2 (the seam export) is decided on what those two profiles actually needed; the adopter's own
   guardrail vectors when they bring them.
4. Tell the adopter v0.3.0 and v0.4.0 exist; their questions in the two contract READMEs are still open.

### How the work has been run, for whoever picks it up

Every unit since Phase 3 ran the same way and it held up: a written spec (what is in scope, what is explicitly out,
which document is the authority, the definition of done) → one developer implementing test-first → three or four
independent review lenses in parallel (correctness; a house rule to *break*; test quality by mutation; contract
fidelity where there is a wire) → a fix pass that verifies each finding before acting → an independent `task check`
→ the orchestrator reads the diff → one commit per unit with a body that says why. Two guards worth stating: the
verified vendor documentation is the authority for every wire field (`contracts/<provider>/README.md`, dated); and a
"green" report from an agent or a piped linter is not evidence — check the exit status and the CI run yourself. The
design documents (`docs/design/*.md`) are records of what shipped; their Go blocks are illustrative and the code
wins where they disagree.

### What closed on 2026-08-15 beyond the planned units

Four things review found while building the planned units, each committed separately with the reasoning in the
commit body, all in v0.2.0:

- **A credential could reach the journal, admin API and log through a `turn_key` extractor** —
  `header:authorization` or `body_json:api_key` composed its raw value into the lane key, which is
  `outcome.fault_key`, and `journal.Redact` never touched it. Present since v0.1.0. Credential-named or
  credential-shaped extractor values are now fingerprinted at composition, so "route by which key was presented"
  still works and the raw value never reaches a retained structure; `testkit.AssertNoCredentialLeak` scans
  `outcome.fault_key` too.
- **`job.foreign_id` was specified (async design §8) and never built** — and `runNotFound`'s comment claimed it
  was. Built at the seam, WARN not ERROR, with the condition and the three causes in the finding text.
- **A create refused at the job bound rendered as the vendor's 400 "invalid request body"** — a configuration wall
  is now the 503/500 the namespace-limit precedent set, on both providers.
- **The design's own YAML anchor pattern (`respond: &pending` / `*pending` across turns) was rejected by the
  loader**, because retained provider nodes were re-marshalled one at a time for strict decoding. Aliases are now
  resolved into deep copies where the nodes are retained (`scenario/alias.go`).

### What changed about how this work is being done

Two process findings from today, both earned the hard way and both worth not rediscovering.

**A design document cannot be reviewed into correctness by its own author.** Phase 2 round 1 self-certified two
findings it had merely restated, and introduced three new defects. Round 2 fixed those and introduced six more. The
*conceptual* layer converged and stayed converged; the *mechanical* layer — signatures, arities, enum completeness,
registration wiring — did not, because prose cannot be type-checked. Round 3 therefore **demoted every Go block in
both design documents to illustrative**, and the mechanical questions were settled by writing A2 and letting the
compiler answer them. That worked: A2 resolved in one pass what two adversarial review rounds could not.

**The guards in this repository have blind spots that surface exactly when a surface is extended, and they are
defects in their own right.** Three today: `task lint` did not run the markdownlint CI runs (so `task check` passed
on a tree CI rejected); `check-docs.sh` read provider routes from `provider/*/handler.go` only, so a provider that
split its routes across files had them read as unregistered; and its method regexes omitted `HEAD` entirely, making
a served HEAD route invisible in both directions. Each time the guard was right to fail and wrong about why. Budget
for this when adding a surface.

**Contract verification is the first step of any provider unit, not a formality.** Both A3 and A4 were blocked at
the start because the contract recorded that an endpoint existed and nothing about its shape. Verifying turned up
things no plan had: a seven-value `effort` enum on Exa with one value beta-gated, `stopReason` and its own enum, and
for Tavily a poll whose **HTTP status varies with task state** plus a required field named `input` rather than
`query`. A4's verification also **disproved the async design's stated reason** for per-route credentials.

**Every unit's review found something the unit's own spec did not ask for, and none of it was style.** A5–A7 and
the two small units between them each ran implement → three review lenses → fix → independent verify, and the
review lenses — not the implementer's green `task check` — produced the four fixes listed under "What closed
today" plus a signature change to `AssertPollSequence` before it could ship. The lens that paid most was the one
told to attack a house rule directly ("get a credential into a served structure by any path the fix did not
close"), which found the array-index body path and the credential-shaped-value path a first, careful fix had
missed. Budget for the review round as part of the unit, not as a gate after it, and give at least one reviewer a
house rule to break rather than a diff to read.

## Decisions already taken

These are settled. Re-open one only with new evidence, and record why.

|  # | Question | Decision  |
|---|---|---|
|  D1 | Keep or drop Exa POST /answer? The adopter says their client does not call it. | **Keep** Exa `/answer`, and fix only the documentation. Still unverified whether their client calls it — ask the adopter.  |
|  D2 | Does a body-placed Tavily api_key AUTHENTICATE, or merely stop being an error? This reverses a documented contract decision, where ADR-0002 (verifi... | **Body-placed Tavily key authenticates** on POST routes. Shipped in v0.1.1.  |
|  D3 | Multi-replica namespace state: document a single-replica exemption, or share state across replicas? | **Documented single-replica exemption**, enforced by README's "Single replica by design" section, troubleshooting, the `servicesim.single_replica_required` startup log, the `job.foreign_id` finding, and the Compose example — not by a shipped Kubernetes manifest, which is the adopter's own deployment artifact, out of this repository's scope (reworded 2026-08-16, Phase 7 pass). **No replicas, ever, by design:** this is a test simulator, not a production service; its only durable state is scenario YAML, which lives in the consumer's repository under version control, and the journal is ephemeral by design, so there is nothing to back up. Capacity for a shared tier is more processes with namespaces (house rule 6), never replicas of one instance.  |
|  D4 | How is the callback injector bounded against the never-dials-outward property? | **Ship the no-dialer half** of the callback injector first; the outbound dialer is a separate, later decision.  |
|  D5 | Enforced per-lane RPS limiting, which makes response status a function of wall-clock time and contradicts the repo's stated determinism doctrine (s... | **Assertion over journal timestamps first**; build enforced RPS only if that proves insufficient. The assertion half — `testkit.AssertMaxRate`, `AssertMinGap`, `AssertObservedDuration` — shipped in Phase 6 unit 6.  |
|  D6 | MCP and ODR are two new provider profiles for G-3. Build them in-tree, or make out-of-tree providers a supported path? | **Build MCP and ODR in-tree** (owner overrode the recommendation to export the seam instead).  |
|  D7 | Exa /contents, /findSimilar and Tavily /extract have no verified contract in this repository — contracts/exa/README.md:23 explicitly declines to as... | **Re-verify against vendor docs first** for `/contents`, `/findSimilar`, `/extract` — ADR-0002 holds as written.  |
|  D8 | What should the adopter do about stream:true fixtures in the window before SSE ships (Phase 5)? | Tell the adopter **not to record `stream:true` fixtures** yet, and ship a `stream: reject` policy so their path fails loudly. **Reversed 2026-08-15: Phase 5 has shipped.** `stream: {when_requested: stream, deltas: [...]}` now serves a real, golden-tested SSE sequence on both Perplexity surfaces — the adopter can record `stream:true` fixtures today, against `testkit.AssertGoldenSSE`. `stream: reject` remains available for a suite that wants a hard failure instead. |
|  D9 | **Pending (owner, 2026-08-16).** "We lean hard on the first three services as the only thing we sim, and servicesim is quickly becoming a service-simulator framework." Does that reframing change how the repository describes itself (README / CLAUDE.md lead with the framework, Exa/Tavily/Perplexity as three shipped profiles; the "What Servicesim is not" section), and does it re-open D6 (export the provider seam so out-of-tree profiles are a supported path, with MCP/ODR still shippable in-tree as reference profiles)? | **Decided 2026-08-16: tier 1 adopted** — README and CLAUDE.md now lead with "a deterministic service-simulator framework shipping three research-API profiles", the non-goals hold for any profile, README says what is provider-neutral versus profile-specific. **Tier 2 (export the seam, re-opening D6) is deferred until Phase 8's MCP/ODR have exercised the seam in-tree; tier 3 (positioning) stays open.** The proposal, with the reasoning and the tier 2 shape, is [`docs/proposals/d9-framework-framing.md`](proposals/d9-framework-framing.md). |
|  D10 | How is contract drift detected without a canary? | **Dated, manual re-verification**, never a live canary — none is built or planned. Every provider's contract is generated with a machine-readable spec behind it — Exa's `exa-spec.yaml`, Tavily's `openapi.json` and Perplexity's `openapi.json`, each covering every route this repository simulates for that vendor — and each carries a RECORDED spec version and SHA-256 (`contracts.Spec`, `contracts/<provider>/provenance.yaml`'s `spec:` block) that a fresh fetch is compared against as the first, cheap step. That hash comparison is a drift SIGNAL, not a substitute for reading: most entries in every provider are still verified against the vendor's rendered prose pages (each entry's own `documentation_url`), which have no stable byte hash of their own — a page's bytes change with every site deploy independent of the content that matters — so a changed spec hash means a person re-reads the consumed fields against both the cited pages and the spec, never a hash of the prose itself. Only entries whose `documentation_url` IS the spec (all of Perplexity's, and Exa's three `/findSimilar` entries) were read from the spec directly and carry `api_version`; every other entry was read from prose and carries none. Reason for no canary: a canary is outbound infrastructure and a scheduled dependency on vendor availability, for a test simulator whose value is determinism; the recorded hash or the cited page gives a reviewer the same answer ("did the vendor change or did we?") on demand, without the outbound dependency. `contracts/README.md` "Keeping them honest" is the sanctioned procedure; ADR 0002 carries an "Amended 2026-08-16" section recording the same change. Owner, 2026-08-16.  |

Two of these reversed a recommendation, and the reasoning is worth keeping. On D6 the owner chose in-tree because the
adopter's G-3 should not wait on their own team's out-of-tree build. On D7 the owner held ADR-0002 — vendor
documentation outranks other sources — even though applying that same rule to Tavily's credential is what produced a
401 against working production code. The distinction that makes it defensible: for Tavily we had vendor docs
CONTRADICTED by a working client, whereas these three endpoints have no vendor verification at all. If a
re-verification contradicts the adopter's working client again, surface it as a decision rather than silently siding
with the documentation.

On 2026-08-15 the owner reaffirmed vendor documentation as the authority for a wire contract and, on that basis,
reversed a same-day extension of D2's body-`api_key` placement to Tavily `/extract` (D2 was cited for the
extension, but D2 itself is untouched): D2 stands exactly as shipped in v0.1.1 for `/search` and `/research` and is
not a precedent for extending acceptance to routes verified after it. This qualifies the D7 sentence above: a
contradiction between vendor documentation and an adopter's client is reported to the adopter as a divergence, not
silently sided with — but it is no longer, by itself, grounds to change what a route accepts.

## Phases

### Phase 0 — Stop rejecting valid production traffic

> Phase 0 — Stop rejecting valid production traffic, and correct the documents that are false

Nothing here is a feature; every item is servicesim being wrong about traffic or about itself. Two items reject
requests the adopter's real client sends and the live vendor accepts — a simulator that fails valid production traffic
is worse than one missing an endpoint, because the consumer goes hunting for a bug in code that is already correct.
The rest are documentation that is affirmatively false, including one line that bricks the container at startup if
followed and one godoc that pkg.go.dev renders to the world. The adopter built their 'servicesim models one-shot
request/response only' inventory partly from a contracts index table that is wrong in both directions, so correcting
the record is a prerequisite for them trusting anything else in this plan. All of it is small and none of it depends
on any other phase.

**Unblocks:** Tier-1 adoption at all — until the Tavily fix lands the adopter's client cannot authenticate against the
simulator, and any client with an OpenAI base_url ending in /v1 gets a 404 on chat completions. Also restores
contracts/ as a trustworthy fidelity record before the adopter reads it to plan their own work.

- Tavily: a body-placed api_key AUTHENTICATES on POST routes (task #16). x.Body is already populated before the
  handler runs — readRequest(x, &entry) at provider/handle.go:172 precedes h(x) at :195 — so this needs no
  restructuring. Keep the tavily.api_key.in_body finding but demote it to informational so the vendor-doc divergence
  stays visible. Deliberately do NOT invent a scenario key for placement yet; Phase 1 owns that vocabulary, and shipping
  a throwaway one here would be the migration this plan exists to avoid.
- Perplexity: register all four OpenAI-SDK alias spellings — /chat/completions, /v1/chat/completions, /responses,
  /v1/responses — sharing the existing FaultKeys so a retry through an alias draws on the same budget. Today only
  /chat/completions and /v1/responses are registered (provider/perplexity/handler.go:37-40) while
  contracts/perplexity/README.md:38 justifies both with a rule that yields either all-prefixed or all-unprefixed, never
  the served mixture. Four lines in Routes(), and it removes an entire class of works-in-prod-404s-in-the-sim confusion.
- contracts/exa/README.md:326 (task #17): strike ONLY the false 'no C360 consumer uses it' clause. Keep the second
  clause — that the create-then-poll lifecycle needs a different scenario shape — because it is true and is what Phase 3
  answers. Deleting both would tell the next reader the lifecycle objection was also wrong.
- contracts/README.md:9-11: correct the 'Base URL simulated' table. It omits Exa POST /answer and Perplexity /v1/agent
  and /v1/responses, whose goldens sit in the same directory. This is the first page an adopter reads to decide what to
  trust.
- docs/design/package-design.md:3199 documents providers.perplexity.stream: reject, which does not exist — following
  the documentation fails startup validation with perplexity.projection.invalid and server.start_failed, taking down
  every provider in the container, not just Perplexity. Correct the line now; decision 8 settles whether the field is
  also added.
- scenario/model.go:213-215: the godoc on Turn.Fault claims per-turn fault scoping that provider/turn.go:85-96 does
  not implement, and the example it gives ('rate-limit the third call, then succeed') actually rate-limits the first.
  docs/scenario-schema.md:397-399 already says the opposite. Point the godoc at that section.
- Give Exa /contents, /findSimilar and Tavily /extract explicit rows in contracts/ — unconfirmed, or deferred with a
  reason. Today /findSimilar and /extract return zero hits repo-wide, so a reader cannot tell they were ever considered;
  that is the same failure mode as the /agent/runs claim, just silent instead of wrong.
- Publish a short adopter advisory covering what NOT to record fixtures against yet: stream:true responses (both the
  JSON body and the perplexity.stream.unimplemented finding vanish when Phase 5 lands), anything asserting on
  aborting-fault journal timestamps (broken, fixed in Phase 6), and the multi-replica hazard. **The `stream:true`
  clause is reversed as of Phase 5 shipping (decision 8): that response and finding no longer vanish, they become
  a real SSE transcript, so fixtures recorded against `stream: stream` are stable ground now, not a landmine. The
  other two clauses stand.**

### Phase 1 — Schema-envelope changes, before anyone writes a scenario file — SHIPPED

> Shipped in v0.2.0. All six items landed; what each turned into is recorded under the item.
>
> The one API break, noted in the v0.2.0 tag message: `provider.SelectTurn` takes a `route string` before `body`.
> The scenario-file surface is additive only — `when.route:` is a new optional key, so every existing
> scenario file loads unchanged.

Everything here changes the shape or the loading rules of scenario YAML. Each is small today and an N-repository
migration once the adopter and their peers have authored fixtures — this is the entire second ordering rule,
concentrated. The version gate is the sharpest case: it is a two-line change now, and the day SchemaVersion becomes 2
every consumer file in every adopting repo stops loading until someone hand-edits it, which directly contradicts the
argument the open registry was adopted on. The route-addressable turn model is the sequencing chokepoint for every
multi-route item in Phases 3 and 4: today Match has only call_index, body_contains and body_json, and a GET poll
carries no body, so nothing can say which route a turn belongs to. The Exa answer-sub-key trick works for two routes
and collapses at six.

**Unblocks:** Every multi-route provider item in Phases 3, 4, 5 and 8; the three-way credential matrix; and the
adopter's ability to author scenario YAML in Tier-1 that survives the rest of this roadmap without a rewrite.

- **DONE — schema gate widened.** The gate is now a RANGE, not an upper bound: `1 <= version <= SchemaVersion`.
  Widening to `<=` alone would also have accepted `version: 0`, which is what a typo or an unrendered template
  produces and was never a released schema. Both gates moved, not one — `scenario/validate.go` had a second strict
  equality that would have kept rejecting what `Parse` had just accepted; they now share one predicate. The missing
  reverse direction (a v1 file on a v2 build) is covered by `TestVersionSupported`, which required extracting
  `versionSupported(declared, build)` so the build's version is a parameter rather than a constant. The conclusion is
  recorded in docs/scenario-schema.md under "What actually forces a version bump": optional keys are additive, so
  nothing else on this roadmap forces `version: 2`.
- **DONE — `when.route:` shipped.** Matching is on `Route.FaultKey`, not the URL pattern, which means aliases
  collapse for free: `route: completions` matches all three Perplexity spellings and draws the budget the scenario
  scripted. Two spellings are accepted — a bare name matches any key's last segment, a qualified name matches
  exactly and is never reduced to its own suffix first, so `route: "exa:search"` pasted into a Tavily block fails
  rather than quietly matching `tavily:search`. An unknown route name is a load-time ERROR listing the served names,
  via a new optional `provider.RouteLister` interface (type-asserted, so `Validator` did not change and out-of-tree
  providers keep loading). Perplexity's route set is split into `SonarRoutes()`/`AgentRoutes()` so `route: agent`
  written in a Sonar block is caught. `provider.SelectTurn` gained a `route` parameter — the one API break.
- **DONE — per-route credential placement.** `Route.Credentials` holds the placements a route accepts, in a shared
  vocabulary (`provider.PlacementAuthorization`, `PlacementXAPIKey`, `PlacementBodyAPIKey`) lifted out of
  provider/tavily so a scenario author need not know which provider invented the spelling. The precedence rule lives
  in ONE place, `Exchange.AcceptedPlacements` — `auth.headers` > `Route.Credentials` > the package default — because
  three copies of a rule about what authenticates are three chances to disagree, and the disagreement shows up as a
  401 against working client code. The scenario-level override stays entry-wide on purpose: it exists for negative
  tests ("prove my client no longer sends the key in the body"), which a route default must not be able to re-admit.
- **DONE — every placement journaled.** `AuthObservation.Placements []AuthPlacement` lists all of them; the scalar
  fields still describe the first, so existing consumers of `auth.header` read what they always read. Additive, so
  no existing journal assertion changed. Tavily's body-placed key is appended after the body decodes, because
  Handle's header scan cannot see it — a request sending both a Bearer header and a body `api_key` now journals two
  placements instead of losing one. `len(placements) == 1` is the assertion this existed to make writable.
- **DONE — the call-index commitment is in writing.** docs/scenario-schema.md now carries "What claims a call index,
  and what does not", with the table of outcomes and the verified `attempt_index: -1` precedent, and states that an
  enforcing 429 will follow the same rule if Phase 9 ever builds one.
- **DONE — and it immediately found two live bugs.** scripts/check-docs.sh now proves the contracts index table
  against the registered provider routes in BOTH directions. The omission direction is the half a name checker
  cannot normally do: every other category proves documented names are real, this one also proves real names are
  documented. It found that the table still omitted `POST /v1/chat/completions` and `POST /responses` — the two bare
  aliases Phase 0 added — months of that being invisible ended the first time the check ran. Both directions were
  verified to fail on a deliberately broken table before being trusted.

### Phase 2 — Revise the two design documents — DONE

> **Both documents are closed out.** `docs/design/async-jobs.md` moved from "pending re-review" to **IMPLEMENTED**:
> Phase 3 (A1–A7) shipped in v0.2.0 on 2026-08-15, the compiler and the test suite were the round-3 review, and the
> document has now been reconciled against the shipped code — every NORMATIVE statement (decisions, YAML shapes,
> finding codes and severities, ordering constraints, the route table, identifier derivation, HEAD and reset
> semantics, §7.4, §8, §9, §10, §11's fan-out table, §12) was checked against `internal/jobs`, `provider/jobs.go`,
> `provider/lane.go`, `scenario/model.go`, the shipped `scenarios/protocol/*.yaml`, `docs/scenario-schema.md` and
> `git log v0.1.1..v0.2.0`; contradictions were corrected in place, superseded subsections were prefixed rather than
> deleted, and what shipped beyond the original design (`GET /__admin/jobs`'s exact field set, `--max-jobs`,
> `AssertPollSequence`'s `[]Entry` signature, `job.foreign_id`'s WARN condition, YAML alias resolution, lane-key
> credential fingerprinting) is marked "Added at implementation".
>
> `docs/design/streaming.md` went through the round-3 challenge re-review this section always required: two
> independent adversarial reviews read the document fresh (B1), one writer answered every finding in place rather
> than restating it (B2), and a third fresh reviewer re-verified every disposition against the current tree (B3).
> **Verdict: PASS**, on the first B3 cycle — every round-3 blocker and major is answered consistently everywhere the
> document touches the topic. Six minor/nit items survived that cycle (a leftover frame-shape wording in an
> illustrative Go comment, a route-count drift now corrected to three spellings per surface, an unstated pacing
> fold-in, an under-specified mirror case for `scenario.stream.abort_unreachable`, an inverted verb on
> `faultHeader`, and an unstated consequence of `GrammarTyped` landing) — none was a decision ambiguity, and all six
> are fixed in place in the same pass. **OPEN owner decisions: none, for either document.**
>
> Both documents' own banners carry the full round-by-round trail (`streaming.md`'s "Review history" details block;
> `async-jobs.md`'s "Review history (rounds 1–3)" details block) — that is the record of provenance, not this
> section.
>
> **Follow-up, 2026-08-15: a second doc-truth pass over `async-jobs.md` found the first pass's "every contradiction
> corrected in place" claim was itself not fully true.** One blocker (§3.1's route-table example YAML had an
> unconditional turn 0, which `scenario.turn.unreachable` rejects at load — it would not have loaded for a fixture
> author who copied it) and roughly a dozen majors/minors survived the first pass: the phantom-job commit predicate
> was stated as `EffectiveKind() == FaultNone` while the shipped `deliversBody` commits on three kinds, not one;
> `job.id_collision`'s quoted message was the design-time draft rather than the shipped text; `GET /__admin/jobs`'s
> shape was a bare array rather than the shipped `{jobs, bound}` wrapper with a declared order and a `501` path;
> §2.6's finding-code table covered only `exa_agent_runs` and missed two of its own seven codes; §2 had no
> `tavily_research` YAML example at all; and §3.1's `create:` prerequisites subsection was still written in future
> tense with stale line numbers. All are fixed in place in `docs/design/async-jobs.md` as of this pass; none changed
> a decision, only the record of what shipped. The lesson for the next design document: "verified against the
> shipped code, section by section" needs an independent second reader even after the author believes it, for the
> same reason self-review did not converge in rounds 1–2 above.

The findings list below is kept as the historical record of the original review and round 1, which is what round 2
and round 3 (above) answered; every item in it is now either fixed in the shipped code (async-jobs) or answered in
the document's prose (streaming) — none is open.

- async-jobs BLOCKER: §3.1 asserts create and poll draw on separate fault budgets, but §2.5 specifies exactly one plan
  per route selected by the first turn declaring attempts, so both routes on one entry resolve the SAME plan. Follow the
  existing answerFault precedent explicitly (provider/exa/handler.go:51-67 already gives two routes on one entry two
  distinct plans) and say which in §2.5 and in §3.1's table.
- streaming BLOCKER: §9 raises scenario.fault.stream_mismatch on key PRESENCE, but Exa's shipped projection already
  carries stream: on every turn that uses it (provider/exa/render.go:49), so an existing v1 fixture pairing stream: warn
  with truncate_body becomes unloadable — failing §8's own 'every scenario that loads on v0.1.0 must load on this build'
  test, in consumer repos, on upgrade day. Key both directions on the effective policy instead, and add the regression
  fixture.
- async-jobs: HEAD reaches the GET handler and runs SelectTurnFor end to end, silently advancing the job's poll cursor
  while net/http discards the body — and §6 actively advertises HEAD in the Allow header. Register an explicit HEAD
  pattern bound to a 405 or to a handler that resolves without claiming an attempt.
- async-jobs: a faulted create leaves a phantom job. Faults apply after the handler returns
  (provider/handle.go:203-218), so a scripted 429 on create commits a record the client never receives an id for —
  consuming the MaxJobs bound at N+1 records per usable job. Record the job only when the claimed attempt will actually
  serve.
- async-jobs: §7.3 specifies reset order journal -> faults -> jobs, which opens the exact window it exists to close
  (cursor back at 0 while identifiers minted from those indices are still live, producing a job.id_collision 500).
  Specify jobs strictly first, and add the race test.
- async-jobs: turn_key extractors resolve against the LISTENER's primary entry (provider/exchange.go:222-224,
  provider/lane.go:495-501), so turn_key written on an async entry is silently ignored, and a body_json: extractor
  written on the sync entry fires scenario.turn_key_unresolved on EVERY bodyless poll. §5.1 leans on the opposite
  assumption.
- async-jobs: §8's diagnoseForeignID cannot be built as specified — neither localCursor (the seam offers only a
  claiming Next()) nor 'the presented lane' is obtainable. Bound the scan by MaxJobs alone, or drop the diagnostic and
  keep the log line and troubleshooting docs. It is currently the only mitigation offered for the intermittent-404
  failure mode the adopter refuses to accept.
- async-jobs: the job registry has neither a release path nor a drop path, so a non-namespaced shared container
  permanently refuses creates after the 257th. Either add high-water-mark eviction or state the operational consequence
  and surface it before it is hit.
- streaming: suppression is decided inside execute against its own copy of resp, so Handle appends an entry
  advertising a planned stream — chunk count, bytes, usage, cost — for a stream that is never written, then stamps
  client_gone. Decide suppression where the outcome is computed.
- Minor but worth doing in the same pass: give LaneFrom extractors their own finding code (job.id_invalid on field id)
  rather than reusing turn_key_unresolved for a request whose author never wrote a turn_key; rename provider-raised
  stream findings to perplexity.stream.* mirroring the existing exa.stream.policy.unknown; reference
  internal/admin/handler.go:187 rather than writing a second copy of allowHeader; and delete the §5.2 golden-ignore
  item, which testkit/golden.go:60 already does.

### Phase 3 — The async job machine — SHIPPED in v0.2.0 (A1–A7)

> **Delivered** (all in v0.2.0):
>
> - **A1 `internal/jobs`** — `Job`, `Store`, `Registry`, `Limits`, `Stats`, race tests. 100% statement coverage.
>   Two decisions are load-bearing and argued in the source: the bound **refuses rather than evicts** (an evicted
>   journal entry costs observability, an evicted job record costs correctness), and records are keyed on
>   **(namespace, id)** because identifiers are derived without the namespace so two namespaces legitimately mint
>   the same one.
> - **A2 the provider seam** — `ValidJobID`, `Route.Entry`, `Route.LaneFrom` + `LaneFromPath`, `Deps.Jobs`,
>   `Deps.MaxJobs`, `MintJob`, `ResolveJob`, served `HEAD`. Note `Route.Entry` is a **live behaviour change for the
>   shipped `perplexity_agent` surface** — its `turn_key` was silently ignored before and is honoured now; three
>   tests pin it and it needs a release-note line as a behaviour change, not a fix.
> - **A3 Exa** — `POST /agent/runs`, `GET /agent/runs/{id}`, `HEAD /agent/runs/{id}`, `create.fault`,
>   `costDollars.total` from the first release.
> - **A4 Tavily** — `POST /research`, `GET /research/{request_id}`, `HEAD /research/{request_id}`. The poll's HTTP
>   status varies with task state (202 running, 200 terminal) and that is the easiest thing on this surface to lose.
> - **A5 admin/config** — `POST /__admin/reset` drops job records with the cursors (the defect this morning's header
>   named); `GET /__admin/jobs` (id, namespace, entry, create_index, created_at — no lane key, on house-rule-4
>   grounds; no turn index, because the cursor is not readable without claiming); `--max-jobs` /
>   `SERVICESIM_MAX_JOBS`; the unconditional `servicesim.single_replica_required` startup line.
> - **A6 testkit** — `Job`, `Jobs`, `JobStats`, `NewJobs`, `Sim.Jobs`, `Namespace.Jobs`, `AssertPollSequence`
>   (takes `[]Entry`, so `ns.Requests(p)` scopes it — job ids repeat across namespaces by design, and reading the
>   whole Sim when an id is live in two is refused as ambiguous rather than merged), the examples alias guard.
> - **A7 scenarios and docs** — every built-in declares `exa_agent_runs` and `tavily_research`; `async-failed` and
>   `async-stuck` are new; the `docs/scenario-schema.md` async section; the multi-replica job row in README and
>   troubleshooting; the image smoke creates a job on both listeners.
>
> **Corrections this work forced into other documents**, so nobody re-derives them:
>
> - `scenario.ProviderEntry` gained `Create` (`create.fault`), and **`reservedEnvelopeKeys` is not what the loader
>   reads** — the authoritative list is `decodeProviderEntry`'s switch. `docs/scenario-schema.md` said otherwise and
>   is corrected.
> - `HasFaults` had to learn about `create.fault`, or a create-only plan reports no faults and the
>   `deps.faults_ignored` warning never fires.
> - The async design's claim that Tavily requires a body key on the POST and a header on the GET is **wrong** — the
>   vendor documents Bearer for both. `Route.Credentials` is still right, because a POST has a body to carry an
>   `api_key` and a GET does not. Corrected in both the design and `contracts/tavily`.
> - `MintJob`'s commit predicate is **not** `FaultDecision.Faulted()`, which is true for a pure `delay:` whose body
>   IS written. It is the set of kinds that still deliver the rendered body.
> - Source refs must be resolved **at request time**, not only in the validator. Both A3 and A4 had this latent;
>   A3's was invisible because its test asserted `output.text` and never a citation URL.
> - The design's `output.content/citations` is stale: the shipped Exa projection is `output.text` +
>   `output.grounding[]`. The design's Tavily route row named `{id}`; the real wildcard is `{request_id}`.
> - Tavily's terminal poll must carry `created_at` (contract), and Exa's non-terminal poll renders `stopReason` as
>   an explicit `null` (contract `string|null`) — both were wrong on the wire until A7's doc pass read the contract
>   files instead of the design.
> - **A kind-none attempt that names a `status` pins the wire status.** Invisible on a 200 route; on a 201 create or
>   a pending 202 Tavily poll it is wrong. The success attempt is `- {}` there. Documented in the schema.
> - **`GET /__admin/jobs` and the fingerprinted lane key resolve one open question the design left**: whether a
>   lane key can be served. It cannot, even now that credential-named extractor values are fingerprinted, because
>   a served field is a compatibility obligation nothing currently needs.

### Phase 3 — The async job machine

> Phase 3 — The async job machine: one state store, Exa /agent/runs and Tavily /research

This is the largest Tier-1 unblocker and the most expensive item in the backlog to retrofit, for a specific reason: if
the GET poll route ships without real POST-to-GET id correlation, every adopter fixture bakes in a hand-written run id
and adding correlation later rewrites all of them. Ship the correlation with the route or not at all. It is one state
machine serving both providers, not two — and the same machine Perplexity's background mode and Phase 8's ODR profile
will need, so building it once at the seam is the whole point. It also forces the multi-replica documentation to land
here rather than later: today a second replica halves call_index sequencing non-deterministically, but once job state
exists a POST on replica A followed by a GET on replica B is a hard 404, which is worse than a subtle divergence and
cannot be discovered by reading a response body.

**Unblocks:** Tier-1 TDD inner loop for the adopter's Exa agent path and Tavily research path — the two async flows
they called highest value. Also lays the state store that Phase 8's ODR profile and Perplexity's background/poll
surface both consume, and delivers the documented single-replica exemption (task #18).

- A1: internal/jobs — Job, Store, Registry, Limits, bounds, ResetIn, and the race tests. This is the first
  cross-request state store in the process; today the only per-lane state is the journal ring and the fault attempt
  counters, and internal/journal/ring.go:442-470 documents why the stores must admit exactly the same namespace set. The
  new one has to join that agreement.
- A2: the provider seam — Deps.Jobs, Deps.MaxJobs, Route.LaneFrom, LaneFromPath, ValidJobID, MintJob, ResolveJob, the
  turnLaneKey change, the Allow/HEAD fix, and the mux table rows. A1 and A2 are the critical path; A3 and A4 are
  independent of each other and can run in parallel.
- A3: provider/exa — POST /agent/runs and GET /agent/runs/{id}, with costDollars.total on the terminal response from
  the FIRST release. Adding a cost key after adopters hold goldens changes the bytes of every one of those files, which
  is exactly the expensive-later test; the two existing routes already honour the flat-rate quirk including the zero
  case, so the only risk is the new ones.
- A4: provider/tavily — POST /research and GET /research/{id}, consuming Phase 1's per-route credential placement (key
  in the JSON body on the POST, Bearer on the GET poll). This surface literally cannot work until placement is
  resolvable per route, which is why Phase 1 precedes this.
- A5: internal/admin scoped reset across all three stores in the corrected order, the optional GET /__admin/jobs
  listing with a declared total order, a job-count bound wired through configuration (the flag does not exist
  yet; naming it here would assert a flag the binary does not have), and the single-replica startup log line.
- A6: testkit — Job and Jobs aliases, Sim.Jobs(), a poll-sequence assertion, and the examples/adapter alias guard.
- A7: scenarios/protocol/async-job.yaml covering completed, failed and stuck-pending; the docs/scenario-schema.md
  async section; and the multi-replica sections in README.md and docs/troubleshooting.md. The README isolation table at
  :229-235 is the specific text that currently misleads — it frames the sharing boundary as the service when it is the
  process, and it names the two counters that break.
- Close the cross-provider single-transient-5xx-then-success item explicitly rather than building it: the audit
  verified live that N-pending-then-completed and one-429-then-200 are already expressible per route and per namespace
  with the existing attempt model. What was missing was only id issuance and correlation, which A1-A4 supply.

### Phase 4 — The remaining synchronous routes — DONE

Purely additive: a new route cannot invalidate a fixture recorded against an existing one, which is why these came
after the expensive-later async work despite the adopter listing some of them earlier. The real prerequisite was
contract verification, not code — contracts/exa/README.md's earlier text explicitly declined to assert /contents'
route shape, and /findSimilar and /extract had no contract at all. Verification and implementation both landed in
this pass, run as three parallel provider units (U-EXA, U-TAV, U-PPLX) plus a sequential composition step (U-COMP)
that wired the built-ins, docs and image smoke once all three landed.

**Unblocked:** The rest of the adopter's Tier-1 inner loop — their client's full Exa surface (/search, /contents,
/findSimilar) and Tavily surface (/search, /extract) now dispatch identically against the simulator.

- Exa POST /contents — **DONE.** Verified against `https://exa.ai/docs/reference/get-contents` and the vendor's own
  `exa-spec.yaml`, both 2026-08-15; implemented as a fetch-shaped route (D-a): each requested `ids`/`urls` element
  resolves against the selected turn's own `results`, then the corpus, in request order, with no results list of
  its own. `NO_CONTENT_FOUND` (400) fires when no requested identifier resolves at all; a per-identifier
  `CRAWL_NOT_FOUND` in `statuses[]` otherwise. Goldens: happy, empty, per-URL-failure, 400 (neither `ids` nor
  `urls`), 400 (`INVALID_URLS`), 401, 429 — `contracts/exa/`. Along the way, `output.structured` was also fixed to
  render an explicit `null` rather than being dropped by `omitempty`, matching the contract's `object|null`.
- Exa POST /findSimilar — **DONE.** Verified against the vendor's `exa-spec.yaml` (a prose reference page 404s; the
  spec was the actual source of truth, confirming CONTRIBUTING.md's "prefer a machine-readable OpenAPI document"
  guidance the hard way). Implemented as a relevance route (D-b), a second projection over the same result
  renderer `/search` uses, with its own `find_similar:` results and its own fault budget. Simulated despite the
  vendor's `deprecated: true` marking, on the same "do not reject valid production traffic" reasoning as decision
  D1 for Exa `/answer`. Goldens: happy, empty, 400, 401, 429 — `contracts/exa/`.
- Tavily POST /extract — **DONE.** Verified against `https://docs.tavily.com/documentation/api-reference/endpoint/extract`,
  2026-08-15. Same fetch-shaped pattern as `/contents`: requested `urls` (string or array, both accepted) resolve
  against the turn's `results`, then the corpus; an unresolved URL renders a `failed_results[]` entry rather than a
  top-level error, so an all-failed request still answers 200. Auth accepts `Authorization: Bearer` only, per the
  vendor's `/extract` page — a body-placed `api_key` was briefly accepted here too on D2's client-level reasoning
  extended by analogy, but the owner reaffirmed on 2026-08-15 that vendor documentation, not a consumer's client,
  decides a wire contract, and D2 stands exactly as shipped for `/search`/`/research` without extending to routes
  verified after it. A body `api_key` is still recognised and still raises `tavily.api_key.in_body`, but it does
  not authenticate; `auth.headers` overrides still work. Goldens: happy, empty, partial-failure, 400, 401 —
  `contracts/tavily/`.
  Alongside the new route, a real defect surfaced and was fixed in the same pass: a `failed` research poll (the
  async surface Phase 3 shipped) was rendering `created_at` on every terminal status; the verified contract says
  only `completed` carries it. Fixed, golden and the one disagreeing README row corrected with citation and date.
- costDollars.total on both new Exa routes from their first release — **DONE**, avoiding the expensive-later
  fixture rewrite A3 warned about.
- Wire the stream_mode full|concise enum — **DONE.** `provider/perplexity/request.go`'s already-declared
  `StreamModes` constant is now enforced on `/v1/sonar` and every alias, with the same enum-error style as
  `search_mode`/`reasoning_effort`/`search_recency_filter`, unblocking Phase 5's need to validate it before adding
  the streaming behaviour it selects between.

`docs/scenario-schema.md` documents the three new sub-keys (`exa.contents`, `exa.find_similar`, `tavily.extract`),
the D-a fetch-shaped-route echo rule and the D-b relevance-route distinction. Every built-in scenario under
`scenarios/protocol/` gained the new sub-keys where its own behaviour has an analogue (own fault budgets on
`rate-limited`/`server-error`/`malformed-json`, `extra_fields` tolerance on `extra-fields`, and a documenting
comment everywhere D-a already covers the case with no authoring, as on `happy`); `documentedProjectionKeys` in
`scenarios/scenarios_test.go` was extended to match. `scripts/image-smoke.sh` gained a 200 check for each of the
three routes against the shipped `happy` scenario. `contracts/README.md`'s index table now lists all three routes
in both directions (`scripts/check-docs.sh` proves it), and their "NOT YET SIMULATED" rows are gone from the
not-simulated table; the two provider contract files' own status lines read "simulated".

### Phase 5 — SSE streaming for the Perplexity deep-research path — DONE

> **Delivered**, across four units (`docs/design/streaming.md`'s banner carries each one's own "Shipped as" note
> against every place the design's illustrative sketches diverged from what shipped):
>
> - **Unit 1** — `scenario.StreamScript`/`StreamServe`, `provider.SSEEvent`/`EncodeSSE`/`Stream`/`Response.Stream`/
>   `executeStream`, `Handle`'s widened journal-early condition, `journal.StreamOutcome`/`StreamCloser`/
>   `Ring.CloseStream`, and the Sonar `GrammarDelta` full-mode renderer on all three Sonar route spellings.
>   Golden: `contracts/perplexity/perplexity-sonar-stream.sse`.
> - **Unit 2** — the three `stream_*` fault kinds (`stream_disconnect`, `stream_truncate_chunk`, `stream_stall`),
>   `FaultAttempt.AfterChunk`, per-delta/per-script/per-terminal `pace:`, and `journal.StreamOutcome`'s remaining
>   fields (`PaceMS`, `AbortAfterChunk`, `TruncatedAtByte`, `StallBeforeMS`). Goldens:
>   `contracts/perplexity/perplexity-sonar-stream-disconnect.sse`,
>   `contracts/perplexity/perplexity-sonar-stream-truncate.sse`.
> - **Unit 3** — the Agent API's `GrammarTyped` grammar on `perplexity_agent`: `agentStreamPolicy`/
>   `rejectAgentStream` mirroring Sonar's pair, the renamed finding `perplexity.stream.agent_unsupported` (from
>   `perplexity.agent.stream.unsupported`), and `renderAgentStream` emitting six of the fourteen `EventType`
>   members plus the shared `agentResponse` helper that makes the terminal `response.completed` byte-identical to
>   the non-streaming body. Golden: `contracts/perplexity/perplexity-agent-stream.sse`.
> - **Unit 4** — the consumer surface: `testkit.AssertGoldenSSE`, `AwaitStreamClosed` (`Sim` and `Namespace`
>   forms), `AssertStreamPacing`; the built-in `streaming` scenario, scripting the adopter's four cases plus a
>   happy Agent-surface stream (`scenarios/protocol/streaming.yaml`); `examples/stream_test.go`; and this pass's
>   documentation — `docs/scenario-schema.md`'s "Streaming (`stream:`)" section, README's SSE paragraph and
>   testkit sentence, the two troubleshooting entries, and decision 8's reversal (above).
>
> Every wire fact this section used to describe as a plan is now what those four units actually built — see
> `docs/design/streaming.md` for the full record, and `contracts/perplexity/README.md`'s "What Servicesim
> simulates" section for the exact frame sequences.

The adopter called this a MUST-HAVE, not optional: their primary deep-research path always streams. It was late in
the order only because Phase 2 had to first fix the design's compatibility blocker and Phase 1 had to land the
schema moves — not because it was low value. The audit's prediction held: the three hard parts of mid-stream
disconnect already existed and needed no new transport mechanism — flush-then-abort, RST-versus-FIN via
`SetLinger(0)`, and journal-append-before-the-socket-is-touched all reused unchanged. Streaming faults turned out
to be exactly the reuse job at the transport layer the audit expected, not new research.

**Unblocked:** The adopter's primary deep-research path in Tier-1, and their Tier-3 golden-file regression for
it, now buildable against `testkit.AssertGoldenSSE`. Phase 8's MCP streamable HTTP transport and the
trickle-body vector in Phase 6 can now build on the same chunked-write path this phase landed.

- Contract first — **DONE.** `contracts/perplexity/README.md`'s "Streaming (SSE)" section was regenerated from
  the vendor's `openapi.json`, and every frame-level choice the vendor does not pin is recorded as
  simulator-chosen in `contracts/perplexity/provenance.yaml`, correctable from a captured live response. Reading
  the adopter's client code as evidence was considered and struck: `docs/design/streaming.md` §10 records why —
  vendor documentation decides a wire contract, never a consumer's client, under the same authority rule ADR-0002
  already states.
- Provider core — **DONE.** `Response.Stream`, the `executeStream` branch, and `Handle`'s widened journal-early
  condition landed once in the shared core (`provider/stream.go`, `provider/handle.go`), grammar- and
  provider-blind by construction, so a future Exa/Tavily streaming surface would reuse them unchanged.
- The SSE writer — **DONE.** `EncodeSSE`'s frame envelope, the Sonar `GrammarDelta` full-mode sequence and the
  Agent `GrammarTyped` sequence, with `usage`/cost on the terminal chunk (`response.completed` on the Agent
  surface) exactly as the non-streaming projection already declares them — one declaration renders both
  transports.
- Streaming faults, all four the adopter named — **DONE**, plus `stream_stall`: `stream_disconnect`,
  `stream_truncate_chunk`, `stream_stall` (Temporal heartbeat/activity-timeout pacing) and
  transient-blip-then-retry, which needed no new mechanism — the existing attempt list already expresses it. The
  SSE-aware truncation variant counts frames via `after_chunk`, not bytes, exactly as this bullet anticipated.
- Journal a stream outcome — **DONE.** `outcome.stream` (`journal.StreamOutcome`), `omitempty` and nil on every
  non-streaming response, so no existing golden's shape changed. Chunk count, planned per-chunk pacing, usage/cost
  and the observed close state are all there — see `docs/scenario-schema.md`'s Streaming section for the field
  table.
- `testkit.AssertGoldenSSE` — **DONE**, exactly for the reason this bullet named: it diffs parsed `(event, data)`
  frames rather than raw bytes, so a one-delta change reports as a one-frame diff.
- The Perplexity stream policy field per decision 8 — **DONE.** `when_requested` is now a three-value enum
  (`warn`/`reject`/`stream`) on both Perplexity surfaces, `reject` is expressible and tested, and
  `docs/design/package-design.md`'s stream-policy lines are corrected in place rather than merely flagged as
  stale. Decision 8 itself is reversed above: the adopter can record `stream:true` fixtures now.

### Phase 6 — G-3 depth — DONE

> Phase 6 — G-3 depth: hostile content, brownout, timeouts, rotation, and the pacing evidence fix
>
> **DONE 2026-08-16.** Units 1 (`CompletedAt`), 2 (the generic `malicious-content` built-in), 3 (the
> `oversized_body` fault kind and the `oversized-body` built-in), 4 (the `timeout`/`brownout`/`hang-then-abort`/
> `credential-rotation` built-ins and `testkit.AssertDifferentCredential`), 5 (the `delay_after_headers` fault
> modifier, its request-time streaming mirror, and `hang-then-abort`'s third attempt) and 6 (the request-level
> pacing assertions per D5 — `testkit.AssertMaxRate`, `AssertMinGap`, `AssertObservedDuration` — plus a
> consumer-facing example) shipped on `phase-6`; every code unit of this phase shipped, and the closing docs sweep
> for onboarding and correctness (owner, 2026-08-16) is done too — see the closing-unit bullet below, and
> [`docs/proposals/d9-framework-framing.md`](proposals/d9-framework-framing.md) for the D9 write-up it carried. PR
> #2 is mergeable at the owner's call. Read the current `provider/handle.go` and `provider/fault_exec.go` before
> trusting the line numbers quoted here; Phase 5 rewrote the execute path (a stream branch, `hijackReset`, per-chunk
> `sleep`), and the journal-early record now also fires for streams (`if out.Aborted || resp.Stream != nil {
> record() }`), a shape `phase-6` unit 1 has since split in two: streams still record before the delay; a
> non-streaming aborting fault now waits out its delay, then records, so `CompletedAt` observes the hang
> instead of the instant the attempt was decided — except `truncate_body` carrying `delay_after_headers`, unit
> 5's one further split: that record waits for the AFTER-headers hang too, from inside `truncateBody` itself,
> immediately before the destructive write.

The highest value-per-effort phase in the backlog, because the audit found most of these primitives already exist and
are missing only packaging. Delays are unbounded and context-aware with no WriteTimeout to cut them off; the
rising-latency ladder was verified end to end at 50/100/200ms; hang-then-abort works at the wire level; credential
rotation via auth.expect_key was verified working with two different keys. What is missing is built-in scenarios and
a corpus; the one real defect the audit found — journal CompletedAt stamped BEFORE the delay for aborting faults, so
observed_ms read 0 on a 700ms hang, wrong in exactly the class where a hang is the thing under test, and directly
contradicting the maintainer's pre-verified claim that pacing assertions already worked — shipped on `phase-6`
(unit 1).

**Unblocks:** The adopter's Gate-2 fail-closed ingress gate, and G-3/G-4 depth for brownout, timeout and mid-flight
registry disable. The pacing fix is what makes journal timestamps usable as the evidence half of decision 5.

- ~~Fix journal CompletedAt for aborting faults~~ **DONE on `phase-6` (unit 1).** The pre-dispatch delay now runs
  before `record` for a non-streaming aborting fault, so `completed_at - arrived_at` observes the hang instead of
  reading ~0; a client cancellation during that hang still lands its own entry, stamped at the instant the server
  observed it. The streaming path (`resp.Stream != nil`) already recorded before the delay and is unchanged.
- ~~A malicious-content built-in scenario~~ **DONE on `phase-6` (unit 2), generic pack only.**
  `scenarios/protocol/malicious-content.yaml`: one corpus (prompt injection, credential-shaped bait, active markup,
  exfiltration instructions, long content, plus one benign source) rendered through all six provider blocks — exa,
  tavily, perplexity, perplexity_agent, exa_agent_runs, tavily_research — so one scenario exercises a consumer's
  guardrail / fail-closed ingress gate on every dispatch path (the fusion-overlap pattern). The backlog's own claim
  was verified in code, not assumed: source text is free-form and rendered verbatim (`SetEscapeHTML(false)`), and
  journal redaction touches only request-side fields, so the bait survives to the consumer and never reaches the
  journal — a request that itself echoes a token matching the vendor prefix `sk`, `pplx` or `tvly` is still masked
  there by `internal/redact`'s vendor-key pattern, which is the one corner the AKIA/xoxb/JWT/PEM shapes fall
  outside of.
  The adopter's own guardrail-classifier vectors were not available (owner's decision 2026-08-16) and are deferred:
  they are an APPEND to this file — new sources and new projection entries — not a restructuring. Give each new
  source one of the existing category prefixes (`inj-`/`cred-`/`markup-`/`exfil-`/`long-`), or add its new prefix to
  `hostileSourcePrefixes` in `scenarios/scenarios_test.go` in the same change: that variable, not this paragraph, is
  what the every-provider-projects-it guard actually enforces, and an unlisted prefix would silently escape it.
- ~~An oversized-body knob (body_bytes: padding)~~ **DONE on `phase-6` (unit 3).** A new `oversized_body` fault
  kind, inferred from `body_bytes > 0` exactly as `truncate_after_bytes` infers `truncate_body`, pads the rendered
  response with insignificant JSON whitespace to at least `body_bytes` — decoded value unchanged, only the size on
  the wire differs — writing the padding from a fixed 64 KiB buffer in bounded chunks so a scenario asking for
  hundreds of MiB never costs the process more than that one buffer. It cannot apply to a streaming entry (it sets
  an exact `Content-Length`, which is wrong for chunked SSE), reported at load time and, per request, the same way
  `truncate_body`'s mismatch already was. The `oversized-body` built-in pads the first response on every
  synchronous route — Exa `/search`, `/answer`, `/contents`, `/findSimilar`, Tavily `/search`, `/extract`,
  Perplexity Sonar and Agent, one per-route plan each, as `rate-limited` does — past 1 MiB, then serves a clean
  retry; the async surfaces carry no plan, for the reason the built-in's header gives.
- Trickle/slow-drip bodies — a genuinely new execution kind sharing its entire machinery with Phase 5's chunked
  writes. **Phase 5 has shipped that path** (`provider/stream.go`'s `executeStream`, per-chunk `sleep` through the
  injectable clock, flush per frame): build trickle as a JSON-body user of the same writer, never a second one.
  Unit 5's `afterHeadersDelay` seam (`provider/fault_exec.go`: headers → flush → hang → body) is the other half:
  trickle is headers, then several paced partial writes, sitting on that same after-headers point.
- ~~Built-in scenarios for timeout, brownout, hang-then-abort and credential-rotation, plus a testkit
  AssertDifferentCredential~~ **DONE on `phase-6` (unit 4).** Four fixtures under `scenarios/protocol/`, all six
  provider blocks each: `timeout` (a 30s delay-only attempt per route, then `after: success`), `brownout` (the
  50/100/200/400ms ladder, then two 503s carrying `Retry-After: 1`, then recovery), `hang-then-abort`
  (`close_before_headers` then `truncate_body`+`reset`, each behind a 700ms delay — the hang unit 1 made visible in
  `completed_at`; unit 5 adds a third attempt, `truncate_body`+`delay_after_headers: 700ms`+`reset`, see below),
  and `credential-rotation` (`auth.expect_key: rotated-key-EXAMPLE` on every block, sync and async, and no fault
  plan anywhere, on purpose). `testkit.AssertDifferentCredential` mirrors
  `AssertSameCredential` exactly — same "cannot compare" shape when a credential is absent, fingerprints compared
  rather than values. Combining `expect_key` with a fault plan is pinned as a regression test: an inline scenario
  proves the plan's first attempt fires on the first AUTHENTICATED call, not the first call overall. The
  `WithSkippedDelays` trap is documented (the built-in's own header, the README row, and troubleshooting's "My
  timeout test passes instantly") and the live timeout test runs under real delays by construction, never
  `WithSkippedDelays`; the DelaySkip shape itself (instant 200, `delay_ms: 30000`) is already pinned generically by
  testkit's own server tests, not re-pinned here.
- ~~Delay-after-headers, so 'send headers, hang, then abort' is expressible. Today Delay is applied once,
  pre-dispatch (`preDispatchDelay`, called from `Handle` before anything is journaled or written), so the shape a
  mid-flight cancellation actually has cannot be scripted at all.~~ **DONE on `phase-6` (unit 5).**
  `FaultAttempt.DelayAfterHeaders` (`delay_after_headers:`): the status line and headers are written and flushed,
  THEN the attempt hangs, then execution continues — the body for `none`/`status`/`extra_fields`/
  `wrong_content_type`/`invalid_json`/`oversized_body`, the partial body + RST for `truncate_body`. One helper,
  `afterHeadersDelay` (`provider/fault_exec.go`), called at the single point after `WriteHeader` each writer
  already reaches. Composes with `delay:` and every kind except `close_before_headers` (no headers ever go out —
  `scenario.fault.delay_after_headers.no_headers`); a load-time warning on `empty_body` (`Content-Length: 0` means
  the hang is unobservable); a load-time error on any `stream_*` kind or on a streaming entry's non-suppressing
  kind (`scenario.fault.delay_after_headers.streaming` / `scenario.fault.stream_mismatch`), with
  `stream_stall`+`after_chunk: 0` named as the streaming equivalent; a request-time mirror
  (`scenario.stream.abort_unreachable`) for a hand-built entry that skipped validation. The one deliberate design
  decision: for `truncate_body` carrying this modifier, the journal record cannot move before headers (headers ARE
  the socket being touched), so it is recorded after the hang, immediately before the destructive partial write +
  reset, instead of before `execute` runs at all as every other aborting shape still is — `completed_at` then
  observes the whole exchange, and the client-cannot-observe-the-abort-before-the-entry-exists property
  (package-design.md §2.2 rule 3) holds exactly, just one hang later. `Journal.Outcome.DelayAfterHeadersMS`
  (`delay_after_headers_ms`, `omitempty`) is `DelayMS`'s sibling, reported for every kind that can carry it. The
  `hang-then-abort` built-in gained the third attempt this item named as future work when it shipped in unit 4.
- ~~Fix the over-redaction defect that mangles finding text~~ **DONE in v0.1.1** ("Redaction no longer mangles
  finding text; real credential values in free text still mask" — the tag message). Verify with the journal before
  re-fixing; the item is kept so the audit list stays complete.
- ~~Ship the observed-pacing assertion helper over journal arrival timestamps, per decision 5.~~ **DONE on
  `phase-6` (unit 6).** Phase 5 shipped `testkit.AssertStreamPacing` over a streamed entry's
  `outcome.stream.pace_ms`; this unit ships the request-level half: `testkit.AssertMaxRate` (no window of
  length `per` holds more than `limit` arrivals), `testkit.AssertMinGap` (every consecutive pair of
  arrivals is at least `gap` apart) and `testkit.AssertObservedDuration` (one entry's
  `completed_at - arrived_at` is at least `atLeast` — the `CompletedAt` fix from unit 1 consumed directly).
  All three compare real journal timestamps and are safe on a loaded machine in only one direction (real
  time can only spread arrivals out or lengthen a duration), with no "minimum rate" / "maximum gap" /
  "maximum duration" mirror — those would be upper bounds on wall-clock elapsed time, the flake this
  repository refuses to add. `examples/pacing_test.go` is the consumer-facing pattern: a tiny client-side
  limiter proven against `AssertMaxRate`.

- ~~Closing unit — a docs sweep for onboarding and correctness (owner, 2026-08-16)~~ **DONE.** Run as a unit with
  an outside-reader lens: whether a new consumer can go from README to `testkit.WithBuiltin` to a passing test
  without asking anyone, and whether every statement in README, CONTRIBUTING, `docs/*.md` and CLAUDE.md is true
  against the code as shipped (the design documents are records of what shipped; their Go blocks are
  illustrative). It also carried the D9 proposal — whether the repository should describe itself as a
  service-simulator framework shipping three research-API profiles —
  [`docs/proposals/d9-framework-framing.md`](proposals/d9-framework-framing.md) writes it up concretely, in three
  independently choosable tiers, for the owner to decide; **the sweep does not apply any part of it**.

### Phase 7 — Packaging, deployment and the contract-fidelity process — DONE (rescoped)

> **DONE 2026-08-16, rescoped to items 3, 4 and 5b (owner).** Items 1 (the Gitea mirror) and 2 (the Kubernetes
> manifest) are **dropped**: they are the adopter's deployment, not this repository's, and the guidance that
> matters already ships — README's "Single replica by design" section, troubleshooting, the Compose example, the
> startup log `servicesim.single_replica_required`, and the `job.foreign_id` finding (D3, reworded in the same
> pass). Item 3 (the provenance date model) shipped 2026-08-15, pulled forward for Phase 4. Item 4
> (`contracts.Record.APIVersion`, `contracts.Spec`, `contracts.ProviderSpec`) and item 5b (the sanctioned refresh
> procedure, rewritten around D10) both shipped 2026-08-16. D10 (above) is the owner decision this phase now
> records: drift detection is dated, manual re-verification against a recorded spec hash or a cited documentation
> page — there is no live contract canary, and none is planned.

Track E work that is small, independent of every code phase, and gated on nothing — it can be pulled forward whenever
there is slack. One item WAS a genuine blocker for the adopter's first fixture refresh and is now done (2026-08-15,
pulled forward for Phase 4): contracts_test.go used to require every provenance entry's verified date to equal a
single global constant, so refreshing ONE fixture forced you either to re-date all 40 across three vendors as freshly
verified when they were not, or to fail CI. A provenance record that lies is worse than none, and the test compelled
the lie. Per-entry dates are real now, the provider-level date must match the index table, and `VerifiedOn` is the
oldest entry — that is the concrete thing to hand the adopter when they ask what the sanctioned procedure is.

**Unblocks:** The adopter's contract-fidelity process — `contracts/README.md` "Keeping them honest" is now the
concrete, dated procedure to hand them, and Perplexity's recorded `spec:` block is the concrete hash a refresh
compares against. Their cluster-shared deployment tier (a Gitea mirror, a Kubernetes manifest) is their own
artifact to build, not this repository's; the single-replica guidance it needs already ships in README,
troubleshooting, the Compose example, the startup log and `job.foreign_id`.

- ~~Mirror the digest-pinned image to Gitea, copying BY DIGEST (regctl image copy keyed off the build's digest
  output) rather than re-pushing — a second build produces a different digest for byte-identical inputs, and
  pinning is the adopter's entire point.~~ **Dropped 2026-08-16 (owner)** — the adopter's deployment; the guidance
  that matters already ships (README "Single replica by design", troubleshooting, the Compose example, the
  startup log, `job.foreign_id`); no replicas by design.
- ~~A Kubernetes manifest shipping replicas: 1 with a comment explaining why, the readiness probe on /readyz, and
  a digest-pinned image reference.~~ **Dropped 2026-08-16 (owner)** — the adopter's deployment; the guidance that
  matters already ships (README "Single replica by design", troubleshooting, the Compose example, the startup
  log, `job.foreign_id`); no replicas by design.
- **DONE 2026-08-15 (pulled forward for Phase 4)** — Fix the provenance date model before the first refresh: per-entry
  dates are now real, the provider-level date must match the index table (a test parses it), and `VerifiedOn` is the
  oldest entry, pinned by a test to the recomputed minimum. Goldens for the async routes were added in the same
  change; the async routes had shipped without any.
- **DONE 2026-08-16** — `api_version` on `contracts.Record` (`yaml:"api_version,omitempty"`, populated for every
  entry read from a versioned document) and `contracts.Spec` (`url`/`version`/`sha256`/`retrieved`, with
  `contracts.ProviderSpec(p)` as the small accessor a consumer's own test can read it through). **Every provider
  carries a `spec:` block** — all three vendors publish a machine-readable specification covering every route this
  repository simulates for them, confirmed by fetching each live for this change: Perplexity's
  `contracts/perplexity/provenance.yaml` records `openapi.json`, version `1.0.0`, sha256
  `95305c44ed99cf4e51463de55994b3bd26063194b78668d5e8753534ee3551ab`; Exa's `contracts/exa/provenance.yaml` records
  `exa-spec.yaml`, version `2.0.0`, sha256
  `6fcf299032d8b52fb614315cf4f251f055633bb9e21da7aeeaa0e9bcfb532e30`; Tavily's `contracts/tavily/provenance.yaml`
  records `openapi.json`, version `1.0.0`, sha256
  `f13ba20f158a40ca5776fff3d665ded0010c8f71bce15795defa72b863ef26f8` — all three retrieved 2026-08-16. An earlier
  draft of this pass wrongly recorded Exa and Tavily as having no machine-readable spec at all (or only Exa's
  `/findSimilar` sourced from one); that was wrong and is corrected here. The spec hash is a drift SIGNAL, not a
  diff of what changed: most entries in Exa and Tavily are still verified against the vendor's rendered prose pages
  (each entry's own `documentation_url`) rather than the spec, so a changed hash means re-reading the consumed
  fields against both those pages and the spec — only entries whose `documentation_url` IS the spec (all of
  Perplexity's, and Exa's three `/findSimilar` entries) carry `api_version`.
- **DONE 2026-08-16** — Complete the sanctioned fixture-refresh procedure, rewritten around D10 rather than
  finished as originally scoped: `contracts/README.md` "Keeping them honest" no longer describes a canary that
  does not exist. It states D10 plainly and gives the numbered steps a person actually follows — compare a fresh
  Perplexity spec fetch's SHA-256 against the recorded one, or re-read Exa/Tavily's cited pages for the prose
  path — and what moves where once drift is found. ADR 0002 carries an "Amended 2026-08-16" section recording the
  same change without rewriting the original accepted text; CONTRIBUTING.md and both provider README headlines
  are reworded to match.
- Note that the multi-replica README and troubleshooting text ships in Phase 3, not here — it must land with the job
  store that makes the divergence a hard 404.

### Phase 8 — MCP-mode listener and the ODR provider profile

This is the adopter's G-3 second adapter and the largest single commitment in the backlog, which is why decision 6
recommends opening the seam before committing to build both in-tree. It is genuinely last among the feature phases
because it depends on two earlier ones: MCP's streamable HTTP transport needs Phase 5's SSE-as-a-normal-outcome, and
ODR's long-running-job semantics need Phase 3's job store. The encouraging finding is how much is already free —
tools/call versus tools/list dispatch is body_json with zero schema work, tool-schema drift between calls is exactly
the turn model, and injection-bearing output and oversized results are just projection bytes. Budget from
provider/tavily's real number, though: a ONE-ROUTE provider is 1579 non-test lines plus ~1650 test lines.

**Unblocks:** The adopter's G-3 chokepoint, whose two adapters currently have only one simulated — all four dispatch
paths finally test identically.

- Export a provider.Faults constructor taking a route set (decision 6). Without it an out-of-tree provider's routes
  register in no plan, so every request returns FaultDecision{Unknown:true} and is served fault-free with only a warning
  — the silent-wrong-behaviour class, and the reason the adopter cannot prototype either profile while waiting.
- Fix JSON-RPC batch rejection before anything else in this phase: DecodeObject returns ErrNotObject for a top-level
  array, which becomes request.body_not_object before any handler runs. MCP 2025-03-26 permits batched arrays, so the
  simulator currently 400s a conformant client — the same reject-valid-traffic class as Phase 0, just not yet reachable.
- The MCP listener itself: streamable HTTP transport over Phase 5's stream path, tools/list and tools/call.
- The hostile-behaviour pack for their mcp-adapter defence kit: tool-schema drift between calls (turn model, free),
  oversized results and injection-bearing output (projection bytes, free), handshake and version mismatches, and
  mid-call disconnects (Phase 5's transport faults).
- The ODR provider profile on Phase 3's job store, so long-running-job semantics come from the same state machine
  rather than a third implementation.
- The composition-layer edits both profiles need are mechanical and enumerated: internal/config (8 sites),
  internal/server/listeners.go (3 switches), testkit/server.go (4 lists), contracts (3 sites), and the image and docs
  surface. Note testkit derives the base-URL env var from the provider name, so ODR_BASE_URL and MCP_BASE_URL arrive
  with no mapping table.

### Phase 9 — The two doctrine-contradicting features

> Phase 9 — The two doctrine-contradicting features: enforced rate limiting and the callback injector

Last, and deliberately so: each contradicts a stated repository doctrine, neither blocks Tier-1, and decisions 4 and 5
recommend shipping only the half of each that needs no exception. Enforced RPS makes response status a function of
wall-clock time, which the repo explicitly rejects because a clock-dependent predicate is a flaky test waiting to
happen — and it is the adopter's own Tier-3 golden-file regression that would suffer most. The callback injector's
outbound dialer would reverse a headline safety claim after N repos adopted on the strength of it. In both cases the
adopter's actual requirement is reachable without the exception, so the recommendation is to ship the cheap half,
measure, and build the expensive half only on evidence.

**Unblocks:** The adopter's G-4 / Track E callback handling (dedupe, replay, HMAC rejection) and their task-queue
limiter proof — both reachable in Tier-1 without any new doctrine exception.

- Callback injector, no-dialer half: a journal-visible 'callback due' record after request X with delay D,
  deterministically, plus a testkit helper the consumer's own test fires. Duplicate, replay and forged-auth callbacks
  are payload SHAPE, not destination, so this gives the in-process TDD tier the complete loop with the container staying
  outbound-free.
- Regardless of whether the dialer is ever built, restate the never-dials-outward invariant honestly in all four
  places it appears — README.md:15, CLAUDE.md:64, Dockerfile:13-16, package-design.md:2981 — because --healthcheck
  already dials and the claim was inaccurate at v0.1.0. The defensible version is 'unmatched traffic fails closed and is
  never proxied'.
- Observed-RPS assertion over journal arrival timestamps (shipped in Phase 6 unit 6 — `testkit.AssertMaxRate`,
  `AssertMinGap`, `AssertObservedDuration`) as the answer to the limiter requirement. Revisit an enforcing mode
  only if that is demonstrably insufficient.
- If an enforcing mode is ever built: explicitly opt-in, never fires unless configured, and honouring the Phase 1
  decision that a dynamic 429 does not claim a call index.
- If an outbound dialer is ever built, the bounds are already specified by the --healthcheck precedent and are
  non-negotiable: destination from an operator flag only and never from scenario data or the request, Proxy: nil, no
  redirect following, the scratch image kept CA-bundle-free so HTTPS to public hosts cannot verify, a runtime denylist
  sharing lint-no-live-hosts.sh's pattern so CI and runtime cannot drift, and every attempt including every refusal
  journaled — an outbound dial absent from /__admin/requests puts the one action that can cost money outside the surface
  the whole tool exists to provide.

## Audited gap status

Every item below was checked against the code with file:line evidence or a live probe. `contradicts-current-design`
means the gap cannot be closed without changing a stated design property.

|  Status | Retrofit | Effort | Gap  |
|---|---|---|---|
|  contradicts-current-design | expensive-later | small | TAVILY: a body-placed api_key is rejected with 401 — the simulator would reject the adopter's real client  |
|  contradicts-current-design | expensive-later | medium | TAVILY/EXA: validate credential placement PER ROUTE (Bearer on GET polls, key in body on POSTs; the Perplexity-Bearer / Exa-x-api-ke...  |
|  contradicts-current-design | expensive-later | large | CROSS: the scenario turn model cannot address a route, so a provider with 3+ routes cannot be scripted per route  |
|  contradicts-current-design | cheap-later | small | godoc on scenario.Turn.Fault claims per-turn scoping that the implementation does not have  |
|  contradicts-current-design | cheap-later | medium | A consumer adding a provider out-of-tree (no servicesim release)  |
|  contradicts-current-design | cheap-later | medium | Callback injector: POST a scripted callback to a configured URL after request X (plus duplicate, replay, forged-auth variants)  |
|  contradicts-current-design | cheap-later | large | Handler contract can express a stream at all  |
|  contradicts-current-design | cheap-later | small | EXA: contracts/exa/README.md states a capability rationale that is now false  |
|  not-started | expensive-later | large | Enforced per-lane RPS limiting (a mode that enforces configurable RPS and 429s dynamically)  |
|  not-started | expensive-later | large | SSE streaming on the Sonar surface (POST /chat/completions or /v1/sonar with stream:true) — the adopter's primary deep-research path  |
|  not-started | expensive-later | large | EXA: async /agent/runs pair (POST create -> GET /agent/runs/{id} poll) is not simulated, and the contract doc's stated reason is false  |
|  not-started | expensive-later | large | TAVILY: async /research flow (POST -> {status: pending, request_id} -> GET /research/{id} poll) is not simulated  |
|  not-started | expensive-later | large | CROSS: per-lane async job-state machine (scriptable N pending polls -> completed / failed / stuck-pending) serving Exa agent-runs an...  |
|  not-started | cheap-later | medium | Malicious-content scenario pack (injection markers) for their Gate-2 fail-closed ingress gate  |
|  not-started | cheap-later | large | MCP-shaped listener (streamable HTTP, tools/list + tools/call, hostile-behaviour pack)  |
|  not-started | cheap-later | large | ODR (open-deep-research) provider profile with long-running-job semantics  |
|  not-started | cheap-later | small | Mirror the digest-pinned image to Gitea  |
|  not-started | cheap-later | small | Kubernetes manifest for the cluster-shared deployment  |
|  not-started | cheap-later | small | Document that namespace lane state is per-instance in-memory; at 2 replicas call_index sequencing breaks  |
|  not-started | cheap-later | small | Version each contracts/ entry against the vendor API version it mirrors  |
|  not-started | cheap-later | medium | Any SSE machinery anywhere in the tree  |
|  not-started | cheap-later | medium | Slow chunk pacing (Temporal heartbeat testing)  |
|  not-started | cheap-later | medium | Transient-blip-then-retry mid-stream (REQ-AGENT-DR-INTERNAL-RETRY-001)  |
|  not-started | cheap-later | small | A scenario-level streaming policy for Perplexity (warn / reject), as the design document promises  |
|  not-started | cheap-later | medium | A contract-grade spec of the Perplexity SSE wire format to implement against  |
|  not-started | cheap-later | medium | Journal can record a stream (chunk count, per-chunk timing) so tests can assert pacing  |
|  not-started | cheap-later | medium | Golden-file support for an SSE transcript  |
|  not-started | cheap-later | medium | Perplexity's own async surface: GET /v1/agent/{id}, background:true, files, cancel  |
|  not-started | cheap-later | medium | EXA: POST /contents is not simulated  |
|  not-started | cheap-later | medium | EXA: POST /findSimilar is not simulated and is not documented anywhere, not even as a non-goal  |
|  not-started | cheap-later | medium | TAVILY: POST /extract is not simulated  |
|  partial | expensive-later | small | Scenario schema version gate is strict equality, with no backward-compatibility path  |
|  partial | expensive-later | small | EXA: costDollars.total on EVERY endpoint (REQ-PRICING-EXA-COST-CAPTURE-001)  |
|  partial | cheap-later | small | What the existing `when: {call_index}` turn model already covers, precisely  |
|  partial | cheap-later | small | Rising-latency brownout profile  |
|  partial | cheap-later | small | Hang-then-abort (mid-flight abort for registry disable)  |
|  partial | cheap-later | small | Journal timestamps as evidence of observed pacing  |
|  partial | cheap-later | medium | Oversized and trickle (slow-drip) hostile bodies  |
|  partial | cheap-later | small | Adding a provider without editing the composition layer — how much the 'open provider registry' actually buys  |
|  partial | cheap-later | medium | A sanctioned fixture-refresh procedure with provenance notes  |
|  partial | cheap-later | small | contracts/README.md's index table states which routes are simulated — and it is FALSE  |
|  partial | cheap-later | medium | Mid-stream disconnect fault (cut the stream after chunk N)  |
|  partial | cheap-later | small | usage + cost in the TERMINAL chunk  |
|  partial | cheap-later | small | `stream_mode` (full\|concise) request validation, while the contract claims 'full request validation'  |
|  partial | cheap-later | small | OpenAI-SDK alias paths are asymmetric: /responses and /v1/chat/completions are 404  |
|  already-done | expensive-later | small | EXA: is POST /answer reachable/used, and what does dropping vs keeping cost?  |
|  already-done | cheap-later | small | Delays exceeding an activity timeout (Temporal activity timeout / heartbeat)  |
|  already-done | cheap-later | small | Credential rotation: 401 on call N, success with a DIFFERENT key on N+1  |
|  already-done | cheap-later | small | Digest-pinned, provenance-carrying multi-arch image  |
|  already-done | cheap-later | small | Route/model reconciliation: are /v1/sonar, /chat/completions, /v1/agent, /v1/responses registered, and is sonar-deep-research accepted?  |
|  already-done | cheap-later | small | Transport-level fault primitives that a streaming implementation would reuse  |
|  already-done | cheap-later | small | TAVILY: redact body-embedded keys in the journal, not only headers  |

## Open questions for the adopter

- Does their client genuinely never call Exa `POST /answer`? This decides between deleting ~220 lines
  across five categories and simply recording it as retained-but-unevidenced. It could not be verified
  from this repository — their `src/pkg/agent/` tree is not here.
- Does the single-replica exemption hold in practice for their cluster-shared tier, not only in principle?
  That tier is the one place per-process lane state actually bites.
- Which OpenAI SDK base-URL convention do their cores use, and which Perplexity routes do they actually
  call? All four spellings now route, but the reconciliation they asked for still needs their answer.

## Sequencing rule

**Phase 1 before anything that generates fixtures.** Everything in it changes the shape or the loading rules of
scenario YAML. Each item is small today and an N-repository migration once the adopter and their peers have authored
scenario files. This is the same argument that put the turn model into v0.1.0 before there were any consumers, and it
is the one that keeps paying.
