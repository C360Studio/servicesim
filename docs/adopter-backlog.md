# Adopter backlog and delivery plan

The first adopter reviewed Servicesim against their target architecture and returned a gap backlog, verified against
their own client code. This document is the durable record of that backlog, the evidence-based status of each item,
the phased plan, and the decisions already taken. It exists so the work can be picked up cold.

## Where this stands — read this first

Recorded 2026-08-15 (evening), against **v0.1.1** plus unreleased work on `main`.

| Phase | State |
|---|---|
| 0 — stop rejecting valid traffic | **shipped** in v0.1.1 |
| 1 — schema-envelope changes | **shipped**, unreleased |
| 2 — revise the two design documents | **round 3**, conceptual findings answered; Go blocks demoted to illustrative |
| 3 — the async job machine | **A1–A7 done**, unreleased. Phase 3 is complete. |
| 4 onward | open — **the release decision comes first; then Phase 4's contract verification, or Phase 5** |

`main` is green: `task check` clean on every commit, 20 Go packages under `-race`, image smoke creates a job on
both async listeners. **Nothing since v0.1.1 has been released or tagged**, and that is now the decision to take
before more code: Phase 1 + Phase 3 is a substantial body of work sitting unversioned, and it carries one Go API
break (`provider.SelectTurn` gained a `route` parameter) and one behaviour change to the shipped `perplexity_agent`
surface (`Route.Entry` — its own `turn_key` is honoured now, where before it silently inherited the primary
entry's). A drafted v0.2.0 tag message exists in the session that closed Phase 3; the release procedure is
CONTRIBUTING.md's — publish the image, confirm the tag resolves, THEN update the version pins.

### Start here

1. **Decide the release.** Recommended: tag **v0.2.0** from `main` as it stands. Everything Phase 3 needed is in,
   including the units that were still open this morning (A5–A7) and four fixes review turned up on the way
   (below). Holding it back buys nothing: the adopter is not yet on any of this, and the alternative — releasing
   the async surface later with more unreleased change stacked on it — only makes the release note longer.
2. **Then Phase 4 or Phase 5.** Phase 4's prerequisite is contract verification of Exa `/contents`, `/findSimilar`
   and Tavily `/extract` (decision D7 — re-verify against vendor docs first), which is a reading task with no code
   dependency. Phase 5 (SSE) is the adopter's MUST-HAVE and its blocker is Phase 2's `streaming.md` revision, which
   is at round 3 like `async-jobs.md`; the lesson of Phase 3 — build A2 and let the compiler answer what prose could
   not — applies directly, so the recommendation is to start Phase 5 with its "Response can express a stream"
   provider-core unit rather than a fourth prose round.

### What closed today, beyond A5–A7

Four things review found while building the planned units, each committed separately with the reasoning in the
commit body:

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
  resolved into deep copies where the nodes are retained. (If this line is here and the fix is not on `main`, the
  fix's workflow did not land — check `git log` for `scenario` before trusting it.)

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
|  D3 | Multi-replica namespace state: document a single-replica exemption, or share state across replicas? | **Documented single-replica exemption**, enforced by a manifest defaulting to `replicas: 1`. Not shared state.  |
|  D4 | How is the callback injector bounded against the never-dials-outward property? | **Ship the no-dialer half** of the callback injector first; the outbound dialer is a separate, later decision.  |
|  D5 | Enforced per-lane RPS limiting, which makes response status a function of wall-clock time and contradicts the repo's stated determinism doctrine (s... | **Assertion over journal timestamps first**; build enforced RPS only if that proves insufficient.  |
|  D6 | MCP and ODR are two new provider profiles for G-3. Build them in-tree, or make out-of-tree providers a supported path? | **Build MCP and ODR in-tree** (owner overrode the recommendation to export the seam instead).  |
|  D7 | Exa /contents, /findSimilar and Tavily /extract have no verified contract in this repository — contracts/exa/README.md:23 explicitly declines to as... | **Re-verify against vendor docs first** for `/contents`, `/findSimilar`, `/extract` — ADR-0002 holds as written.  |
|  D8 | What should the adopter do about stream:true fixtures in the window before SSE ships (Phase 5)? | Tell the adopter **not to record `stream:true` fixtures** yet, and ship a `stream: reject` policy so their path fails loudly.  |

Two of these reversed a recommendation, and the reasoning is worth keeping. On D6 the owner chose in-tree because the
adopter's G-3 should not wait on their own team's out-of-tree build. On D7 the owner held ADR-0002 — vendor
documentation outranks other sources — even though applying that same rule to Tavily's credential is what produced a
401 against working production code. The distinction that makes it defensible: for Tavily we had vendor docs
CONTRADICTED by a working client, whereas these three endpoints have no vendor verification at all. If a
re-verification contradicts the adopter's working client again, surface it as a decision rather than silently siding
with the documentation.

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
  aborting-fault journal timestamps (broken, fixed in Phase 6), and the multi-replica hazard.

### Phase 1 — Schema-envelope changes, before anyone writes a scenario file — SHIPPED

> Shipped after v0.1.1. All six items landed; what each turned into is recorded under the item.
>
> One API break to note in the release: `provider.SelectTurn` takes a `route string` before `body`.
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

### Phase 2 — Revise the two design documents — ROUND 2 IN PROGRESS

> **Round 1 did not pass its re-review.** Tally: 4 answered, 3 partly answered, 2 restated, 3 newly broken. The
> open findings live at the top of each document; do not re-derive them from here.
>
> The two restated findings are the ones to be most careful with, because round 1 asserted a *proof* for one of
> them. §7.3's reset reordering claimed to close the collision window "by construction" and does not — the window
> is symmetric, and the reordering additionally collides with the all-or-nothing invariant at
> `internal/admin/handler.go:307-326`. Closing it needs a reset epoch in the derivation tuple or a single lock over
> both stores; that is a real design decision, not a wording fix.
>
> Round 1 was also wrong about a prerequisite in a way worth remembering: it asserted that adding `create` to
> `reservedEnvelopeKeys` would make `create.fault` loadable. That slice is read by nothing but a test — the
> envelope split is a hardcoded switch. **`docs/scenario-schema.md:121` calls it "the authoritative list", which is
> a pre-existing documentation lie that round 1 inherited without checking.** Worth fixing on its own account.
>
> Three findings from round 1 that survived scrutiny and are worth carrying forward:
>
> - **`turn_key` resolving against the listener's primary entry is a live bug on shipped code**, not an async-only
>   one — `perplexity_agent`'s `turn_key:` is ignored today. Fixed by `Route.Entry`, which also fixes the
>   `Exchange.policy()` asymmetry §12 recorded. Both land in Phase 3's A2.
> - **Two design decisions were reversed on evidence**, and each reversal is recorded with its reason: `HEAD` on a
>   poll route answers 405 rather than being advertised in `Allow` (a poll is a cursor advance, not a stateless
>   read), and the job bound refuses rather than evicting (an evicted job record costs correctness, where an
>   evicted journal entry costs only observability).
> - **`diagnoseForeignID` was unbuildable for two independent reasons** — the `Faults` seam offers only a claiming
>   `Next()`, and a poll cannot reconstruct the create lane. Replaced with a shape check needing neither.
>
> Phase 1 also made two statements in `streaming.md` §8 stale, since its argument rested on the strict-equality
> version gate; §8 now records that the gate was widened and that its reason 1 is correspondingly weaker.

docs/design/async-jobs.md and docs/design/streaming.md are both untracked, both came back needs-revision, and both are
the specifications Phases 3 and 5 will be implemented from. Two blockers are internal contradictions where the
document says one thing in one section and the opposite in another — an implementer will resolve those arbitrarily and
the arbitration will be invisible until it is expensive. One of them breaks the streaming design's own falsifiable
compatibility test in consumer repositories on upgrade day, which is the worst possible place to discover it. This is
pure document work, it is cheap, and it can run in parallel with Phases 0 and 1; the only ordering constraint is that
the async design's §12 dependency on per-route credentials is settled by Phase 1.

**Unblocks:** Phase 3 (units A1-A7) and Phase 5 can begin from a specification that does not contradict itself.
Verification is concrete: re-run the challenge review against the revised documents and confirm the blockers and
majors are answered rather than restated.

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

### Phase 3 — The async job machine — DONE (A1–A7), unreleased

> **Delivered** (all on `main`, unreleased):
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

### Phase 4 — The remaining synchronous routes

Purely additive: a new route cannot invalidate a fixture recorded against an existing one, which is why these come
after the expensive-later async work despite the adopter listing some of them earlier. The real prerequisite is
contract verification, not code — contracts/exa/README.md:23 explicitly declines to assert /contents' route shape, and
/findSimilar and /extract have no contract at all — so the verification pass has no code dependency and should start
during Phase 3. These depend on Phase 1's route-addressable turn model, not on Phase 3, so they can run in parallel
with the async work once Phase 1 lands.

**Unblocks:** The rest of the adopter's Tier-1 inner loop: their client's full Exa surface (/search, /contents,
/findSimilar) and Tavily surface (/search, /extract) dispatch identically against the simulator.

- Exa POST /contents — contract verification first, then implementation, then a provenance entry recording what the
  shape was derived from.
- Exa POST /findSimilar — response shape is the /search result array, so implementation is close to a second
  projection over the existing renderer (provider/exa/render.go:182-208). The contract work is the real cost.
- Tavily POST /extract — same pattern; contracts/tavily/README.md currently has a single-row endpoint table containing
  only /search.
- costDollars.total on both new Exa routes from their first release, for the same expensive-later reason as A3.
- Wire the stream_mode full|concise enum, which has been declared and unused since v0.1.0
  (provider/perplexity/request.go:121-122 is its only reference) while contracts/perplexity/README.md:283 claims
  /v1/sonar gets full request validation. Small, but stream_mode changes which events the vendor emits, so Phase 5 has
  to validate it anyway.

### Phase 5 — SSE streaming for the Perplexity deep-research path

The adopter called this a MUST-HAVE, not optional: their primary deep-research path always streams. It is late in the
order only because Phase 2 must first fix the design's compatibility blocker and Phase 1 must land the schema moves —
not because it is low value. The good news the audit found is that the three hard parts of mid-stream disconnect
already exist and are reasoned about in comments: flush-then-abort, RST-versus-FIN via SetLinger(0), and
journal-append-before-the-socket-is-touched. Streaming faults are a reuse job at the transport layer, not new
research. The genuinely missing pieces are a Response that can express a stream at all, an SSE writer, and — first — a
contract-grade spec of the wire format, which the audit says cannot be built from this repository's documents today.

**Unblocks:** The adopter's primary deep-research path in Tier-1, and their Tier-3 golden-file regression for it. Also
unblocks Phase 8's MCP streamable HTTP transport and the trickle-body vector in Phase 6, both of which need the same
chunked-write path.

- Contract first: regenerate the Perplexity SSE section from the vendor's openapi.json, read the adopter's
  src/pkg/agent/perplexity.go as evidence of the real wire shape, and record every frame-level choice not pinned by the
  OpenAPI document as simulator-chosen in provenance. contracts/perplexity/README.md:274-287 today lists 14 event NAMES
  and nothing else — no framing, no body shapes, no statement of which event carries usage — and its non-goal is written
  as if it covered both surfaces when the tables prove they differ.
- Provider core: a Response variant that can express a stream, the execute branch, and the widened Handle
  journal-early condition. This lands once in the shared core, so Exa's documented SSE surface gets it for free later.
  It rewrites servicesim internals, not consumer fixtures.
- The SSE writer: event framing, chunk sequences, and usage plus cost in the TERMINAL chunk. The projection already
  carries every field a terminal chunk needs, so this is a rendering decision, not a schema change — but how the answer
  splits into deltas becomes a golden-file shape and should be decided deliberately.
- Streaming faults, all four the adopter named: mid-stream disconnect, truncated chunk, transient-blip-then-retry, and
  slow chunk pacing for Temporal heartbeats. Note the SSE-aware variant of truncation must suppress Content-Length and
  count events rather than bytes — provider/fault_exec.go:246 sets the full body length before writing the prefix, which
  is correct for JSON and invalid for SSE, and a byte-offset cut produces a half-written data: line that tests the
  adopter's parser rather than their reconnect logic.
- Journal a stream outcome — chunk count and per-chunk timing — as an omitempty array that appears only on streamed
  responses, leaving every existing non-streaming golden byte-identical. This is what makes intra-response pacing
  assertable; the existing arrived_at/completed_at pair is per request only.
- testkit AssertGoldenSSE: an SSE transcript is not JSON, so AssertGoldenJSON cannot be pointed at it, and without
  event-by-event diffing a one-character change in one delta produces an unreadable whole-file diff.
- Add the Perplexity stream policy field per decision 8, so reject becomes expressible and the package-design.md:3199
  documentation becomes true rather than merely corrected.

### Phase 6 — G-3 depth

> Phase 6 — G-3 depth: hostile content, brownout, timeouts, rotation, and the pacing evidence fix

The highest value-per-effort phase in the backlog, because the audit found most of these primitives already exist and
are missing only packaging. Delays are unbounded and context-aware with no WriteTimeout to cut them off; the
rising-latency ladder was verified end to end at 50/100/200ms; hang-then-abort works at the wire level; credential
rotation via auth.expect_key was verified working with two different keys. What is missing is built-in scenarios, a
corpus, and one real defect: journal CompletedAt is stamped BEFORE the delay for aborting faults, so observed_ms reads
0 on a 700ms hang — wrong in exactly the class where a hang is the thing under test, and directly contradicting the
maintainer's pre-verified claim that pacing assertions already work.

**Unblocks:** The adopter's Gate-2 fail-closed ingress gate, and G-3/G-4 depth for brownout, timeout and mid-flight
registry disable. The pacing fix is what makes journal timestamps usable as the evidence half of decision 5.

- Fix journal CompletedAt for aborting faults: provider/handle.go:214-217 calls record() before execute() when
  out.Aborted, stamping CompletedAt before fault_exec.go sleeps. The early record is deliberate and correct — the client
  observes the abort while the goroutine unwinds — so the fix is to stamp after the delay or carry it in a separate
  field, not to move the record.
- A malicious-content built-in scenario carrying the adopter's guardrail-classifier vectors, rendered through all
  three providers so one corpus tests every dispatch path (the fusion-overlap pattern). This is corpus authoring, not
  code: the mechanism is verified — source text is free-form and rendered verbatim, and redaction applies to the journal
  only, so credential-shaped bait survives to the consumer, which is what the test needs.
- An oversized-body knob (body_bytes: padding) — cheap, and what actually exercises a size-limit ingress gate. Today
  oversized is expressible only by embedding megabytes of text in the YAML.
- Trickle/slow-drip bodies — a genuinely new execution kind sharing its entire machinery with Phase 5's chunked
  writes. Build it there or immediately after, never twice.
- Built-in scenarios for timeout, brownout, hang-then-abort and credential-rotation, plus a testkit
  AssertDifferentCredential to complete the rotation assertion. Document the two traps the audit hit: do not combine
  expect_key with a fault plan (auth rejection does not claim an attempt, so the plan misfires), and do not use
  WithSkippedDelays for timeout tests.
- Delay-after-headers, so 'send headers, hang, then abort' is expressible. Today Delay is applied once at the top of
  execute, so the shape a mid-flight cancellation actually has cannot be scripted at all.
- Fix the over-redaction defect that mangles finding text: 'Authorization: Bearer is required' is journaled as
  'Authorization: Bearer [REDACTED] required'. Harmless to secrecy, but any consumer asserting on journal finding text
  reads mangled English, and it shows free-text redaction running over messages containing no credential.
- Ship the observed-pacing assertion helper over journal arrival timestamps, per decision 5.

### Phase 7 — Packaging, deployment and the contract-fidelity process

Track E work that is small, independent of every code phase, and gated on nothing — it can be pulled forward whenever
there is slack. One item is a genuine blocker for the adopter's first fixture refresh rather than a nice-to-have:
contracts_test.go:105-107 requires every provenance entry's verified date to equal a single global constant, so
refreshing ONE fixture forces you either to re-date all 40 across three vendors as freshly verified when they were
not, or to fail CI. A provenance record that lies is worse than none, and the test currently compels the lie. That
must be fixed before any real refresh, and it is the concrete thing to hand the adopter when they ask what the
sanctioned procedure is.

**Unblocks:** The adopter's G-4 / Track E: their cluster-shared container tier and their contract-fidelity process.
Also converts the multi-replica hazard from documentation into an enforced default.

- Mirror the digest-pinned image to Gitea, copying BY DIGEST (regctl image copy keyed off the build's digest output)
  rather than re-pushing — a second build produces a different digest for byte-identical inputs, and pinning is the
  adopter's entire point. Extend scripts/check-docs.sh:302's ghcr.io grep to the Gitea host in the same change, or
  documented Gitea references silently escape the resolve-check that already caught a nonexistent tag once.
- A Kubernetes manifest shipping replicas: 1 with a comment explaining why, the readiness probe on /readyz, and a
  digest-pinned image reference. Shipping the manifest WITHOUT the replica note would actively make things worse — a
  manifest is the artifact people scale.
- Fix the provenance date model before the first refresh: add per-provider or per-entry dates and demote the global
  VerifiedOn to 'oldest entry' or drop it.
- Add an optional api_version to contracts.Record, plus spec_sha256 for the spec-derived provider. What exists today
  is a date, not a version. Perplexity's contract came from a machine-readable openapi.json that has a version and hash
  which could be diffed mechanically; Exa and Tavily came from prose pages, where the honest version is a content hash
  or archive timestamp of the cited page.
- Complete the sanctioned fixture-refresh procedure. contracts/README.md:30-35 already documents three steps, but they
  are gated on a live contract canary described as 'plan Phase 5' that does not exist — the only workflows in the repo
  are ci.yml and image.yml, and nothing compares VerifiedOn to anything.
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
- Observed-RPS assertion over journal arrival timestamps (shipped in Phase 6) as the answer to the limiter
  requirement. Revisit an enforcing mode only if that is demonstrably insufficient.
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
