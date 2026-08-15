# Async job lifecycle

> ## IMPLEMENTED — Phase 3 shipped in v0.2.0 (2026-08-15)
>
> This document is now the record of the async-job design **as built**, not a proposal awaiting another round of
> review. The compiler and the test suite were the round-3 review: units A1–A7 (§11) all landed, `task check` was
> green, and the async surfaces are live on the `exa` and `tavily` listeners today.
>
> **The code is authoritative.** Where anything below disagrees with `internal/jobs`, `provider/jobs.go`,
> `provider/lane.go`, `scenario/alias.go`, `provider/exa/agentrun*.go`, `provider/tavily/research*.go`,
> `internal/admin`, `internal/config`, `internal/server`, `testkit`, `scenario/model.go` or
> `scenarios/protocol/*.yaml`, the disagreement is a defect in this document, never in the code. Read the source
> before trusting a claim made here.
>
> **The Go blocks stay illustrative.** Round 3 (below) already demoted them from normative to illustrative, and
> that has not changed now that the code exists: they are not being reconciled line-by-line against shipped
> signatures, and they are not deleted, because they carry the reasoning that produced the shipped shape. Read them
> for intent, not for a signature to depend on.
>
> **Verified against the shipped code on 2026-08-15**, section by section: the scenario YAML shapes and validation
> findings (§2), the route table and the create/poll fault-key split (§3), the identifier derivation and the
> `Route.Entry` fix (§5.1), HEAD semantics (§6), isolation and the three-store reset order (§7), the multi-replica
> diagnostic (§8), the schema-versioning compatibility table (§9), the non-goals (§10), the implementation fan-out
> (§11) and the companion corrections (§12). Every contradiction found in the process is corrected in place, with a
> one-line "Shipped as: …" note where the deviation is worth explaining; everything that shipped without being
> described here is added with a one-line "Added at implementation" note.
>
> <details><summary>Review history (rounds 1–3)</summary>
>
> Round 1 was re-reviewed and failed: one blocker, three majors. **Round 2 answers all of them**, and each fix is
> recorded in place with the reasoning that produced it. Summary of what changed:
>
> - **Reset window (blocker).** Round 1's reordering claimed to close the collision window "by construction" and did
>   not — the window is symmetric, and jobs-first additionally collided with the all-or-nothing invariant at
>   `internal/admin/handler.go:307-326`. Reverted. The race is **out of contract** (house rule 6: reset is a
>   local-development convenience, not a concurrency mechanism), and `job.id_collision` now names that cause so the
>   precondition is discoverable when violated instead of only in a document.
> - **`create:` prerequisite (major).** Round 1 named the wrong lever. `reservedEnvelopeKeys` is read by nothing but
>   a test; the envelope split is a hardcoded switch. §3.1 now lists the real changes and states the forward-
>   compatibility cost plainly — an older binary gives `body_with_turns`, not a clean rejection.
> - **Phantom job (major).** `FaultDecision.Faulted()` is true for a pure `delay:` whose body IS written, so using it
>   would have inverted the bug. The predicate is `EffectiveKind() == FaultNone`, with the case table and the one
>   honest limit (a client deadline during a delay) written out.
> - **`diagnoseForeignID` (major).** Now uses the existing `StatsIn` rather than a `CountIn` the seam never declared,
>   names its third and likeliest cause (a stale or hand-written id), stops describing a charset check as a scheme
>   check, and logs at WARN.
> - **`Route.Entry`.** `Exchange.Entry()` itself honours it, which fixes `policy()` and `entryTurnKey` together
>   rather than one of three call sites; §12 no longer calls it out of scope.
> - **HEAD.** Reversed again, and this one is worth reading: round 1's 405 was justified by a claim that a
>   non-claiming resolution "exists nowhere else", which §4.2's own `ResolveJob` contradicts. `HEAD` is now served
>   properly, on its own fault key, and the divergence from `allowHeader` disappears with it.
>
> ### The Go blocks below are ILLUSTRATIVE, not normative
>
> This is the one structural change round 3 makes, and it is a correction to how this document was written rather
> than to what it says.
>
> **Normative:** the decisions, the reasoning behind them, the finding codes and severities, the ordering
> constraints, and the invariants. Those are what a reviewer should hold this to and what an implementer must
> honour.
>
> **Illustrative:** every `go` block. Signatures, arities, receiver types, exported-ness and registration details in
> them are sketches. They have been wrong repeatedly in exactly those dimensions — an unregistered fault key, a
> `StatsIn` arity that did not match its own seam two sections earlier, a predicate missing two enum values, a helper
> borrowed from a package that does not export it — and each round of prose review has caught some and introduced
> more.
>
> The reason is structural: **prose cannot be type-checked.** Two adversarial review rounds produced a flat rate of
> mechanical defects in these blocks while the conceptual layer converged and stayed converged. Continuing to review
> Go-in-markdown for compilability is the wrong instrument. The compiler is the right one, and it runs in seconds.
>
> So: read the blocks for *shape and intent*. Do not treat a signature here as a contract. Where a block and the
> surrounding prose disagree, **the prose wins** — and where the code, once written, disagrees with a block, the code
> wins and the block should be deleted rather than patched.
>
> **Still a design, not an instruction to start on the phase as a whole.** Round 2 was re-reviewed and returned two
> blockers and four majors; round 3 answers the conceptual ones and demotes the rest. Unit **A1** (`internal/jobs`)
> is unblocked and being built, because it depends on none of the open items. **A2 onward remains gated** — it
> touches the mux, `Route.Entry` and the scenario schema, which is where the remaining questions bite.
>
> <details><summary>Round-1 findings, all now addressed above</summary>
>
> - **Blocker — §7.3's "closes the window by construction" is false, and jobs-first breaks a shipped invariant.**
>   The window is symmetric: under jobs → faults → journal, a create that claims index *i* before the reset and
>   commits after it collides exactly as before — reversing the order only changes *which* index collides, and the
>   k>0 case is *harder* to diagnose because no reset is near the failing request. Only a reset epoch in the
>   derivation tuple, or resetting jobs and faults under one lock, actually closes it. Separately, "jobs strictly
>   first" is mutually exclusive with `internal/admin/handler.go:307-326`, which deliberately runs both capability
>   checks before dropping anything so that a store which cannot scope drops nothing — dropping jobs first means a
>   later 501 leaves job state already destroyed.
> - **Major — the `create:` prerequisite names a lever that does not exist.** `reservedEnvelopeKeys`
>   (`scenario/model.go:115`) is read by nothing but a test; the envelope split is a hardcoded `switch key.Value`
>   at `scenario/model.go:546-577` whose `default:` sweeps everything else into the projection body. Adding
>   `create` to that slice is a no-op. The real prerequisites — a `keyCreate` const, a `case` arm with a strict
>   decode, a `Create` field on `ProviderEntry`, and the `Validate`/`HasFaults` follow-through — are unstated. The
>   paragraph also claims `KnownFields(true)` gives an older binary a loud rejection; it does not, it gives
>   `body_with_turns`, which the previous paragraph calls "precisely the wrong advice".
> - **Major — the phantom-job fix names a predicate that does not exist, and the nearest real one inverts the bug.**
>   `FaultDecision` exposes only `Faulted()` (`provider/deps.go:63-65`), which is true when `Delay > 0` even with no
>   fault kind. Using it would skip the record for a pure `delay:` attempt whose body IS written — handing the
>   client a valid identifier no record backs, so every poll 404s. The correct predicate is
>   `dec.Attempt == nil || dec.Attempt.EffectiveKind() == scenario.FaultNone`. (The mechanism is otherwise
>   available: `Exchange.Fault()` memoises on `x.claimed`, `provider/exchange.go:200-206`.)
> - **Major — `diagnoseForeignID` does not compile against §4.1 and hides its third cause.** `Deps.Jobs` is a
>   `jobs.Store`, which declares no `CountIn`; `StatsIn` already reports the count. And `ValidJobID` is a charset
>   check (`[A-Za-z0-9_-]{1,64}`), not a scheme check, so `GET /agent/runs/typo` raises `job.foreign_id` and an
>   error-level log blaming replicas. The most common real cause — a stale or hand-written fixture id — is never
>   named.
> - Also open: `Route.Entry` fixes `entryTurnKey` and `policy()` but leaves `Exchange.Entry()` resolving by listener
>   name with three live callers (`provider/exa/handler.go:116,147`, `provider/tavily/handler.go:179`), and §12
>   still calls the `policy()` asymmetry out of scope while §5.1 says it is fixed. Minors: `job.limit_reached` vs
>   `job.limit_exceeded` for one condition; §7.3 repeats a paragraph verbatim; §6's `mux.HandleFunc` snippet matches
>   no real signature; §6's stated reason for refusing HEAD is contradicted by §4.2's own non-claiming `ResolveJob`.
>
> **Answered in round 1 and still sound:** the fault-budget blocker (§3.1's `Plan read from` column is correctly
> grounded in `answerFault`, `provider/exa/handler.go:42-49` vs `:55-86`); the registry refusing rather than
> evicting, with the 80% warning; the HEAD cursor-advance diagnosis; and the four documentation minors.
>
> ---
>
> And the round-1 revision notes those findings were written against. The original adversarial review returned
> **needs-revision** on 2026-08-15 with one blocker and five majors; round 1 claimed all were answered:
>
> - ~~**Blocker:** §3.1 asserts create and poll draw on separate fault budgets, while §2.5 specifies exactly one
>   plan per route.~~ **Answered.** Separate budgets need two things and the draft supplied one: separate fault keys
>   give separate *counters*, separate `Route.Fault` selectors give separate *plans*. §3.1 now carries a
>   `Plan read from` column following the shipped `answerFault` precedent — create reads `create.fault`, polls read
>   the first turn declaring `attempts`, because a turn IS a poll.
> - ~~`HEAD` on a poll route silently advances the job's poll cursor.~~ **Answered.** `HEAD` is registered
>   explicitly and answers 405; `Allow` on a poll path is `GET` only. §6 records why this deliberately diverges
>   from `internal/admin/handler.go:187`, whose GET→HEAD promotion is right for stateless admin reads and wrong for
>   a cursor advance.
> - ~~A faulted create leaves a phantom job.~~ **Answered.** `MintJob` claims the index but commits the record only
>   when the attempt it claims will actually serve. §4.3 covers the identifier-density consequence.
> - ~~§7.3's reset order opens the very window it exists to close.~~ **Answered.** The order is now
>   **jobs → faults → journal**, which closes the window by construction rather than narrowing it, with a required
>   race test.
> - ~~`turn_key` extractors resolve against the listener's primary entry.~~ **Answered** by `Route.Entry`, which
>   keeps the single-resolution rule intact because the route is known before the handler runs. Note this is a
>   **live bug on shipped code**, not an async-only one: `perplexity_agent`'s `turn_key` is ignored today.
> - ~~§8's `diagnoseForeignID` cannot be built, and the registry has no release path.~~ **Answered.** The
>   derivation scan is replaced by a shape check needing neither a cursor nor the create lane — §8 records both
>   reasons it was unbuildable. The bound **refuses rather than evicts**, deliberately, with a `job.limit_near`
>   warning at 80%; §4.1 explains why FIFO eviction is right for the journal ring and wrong here.
>
> That re-review has now run, and the verdict is at the top of this banner.
>
> </details>
>
> </details>

An addendum to [`package-design.md`](package-design.md) and [`extended-surfaces.md`](extended-surfaces.md). Where the
three disagree, this file is newest and wins for the create-then-poll surfaces it defines; it changes nothing about
the one-shot surfaces that already ship.

It exists because the first adopter's client calls two create-then-poll APIs — Exa's `POST /agent/runs` plus
`GET /agent/runs/{id}`, and Tavily's `POST /research` plus `GET /research/{request_id}` — and Servicesim models one-shot
request/response only. This is the highest-value gap in their backlog and the one with the largest blast radius if it
is designed badly, because a job identifier minted by one request and presented by another is the first piece of
*cross-request* state this simulator has ever held.

The design constraint that governs every decision below: **reuse the turn model, the turn lane, the fault engine's
per-lane counter and namespaces. Do not build a parallel mechanism.** Where that is not honestly possible it is said
plainly, in [§1.3](#13-the-one-thing-the-turn-model-cannot-carry).

## 0. Mechanics verified against the toolchain before writing this

Every non-obvious routing claim below was executed against Go 1.26.4 rather than assumed. Four results change the
design and are quoted at the point of use:

1. `GET /agent/runs/{id}` registered alongside the method-less `/agent/runs/{id}` and the `/` catch-all behaves
   exactly as the existing table does: `POST /agent/runs/run_abc` yields the wrapped 405 with `Allow: GET`, and
   `/agent/runs/run_abc/events` and `/agent/runs/` both fall to the catch-all. `{id}` is single-segment and does not
   match empty. No new registration shape is needed.
2. **A `GET` pattern also serves `HEAD`.** `HEAD /agent/runs/run_abc` returned 200 from the `GET` handler — which
   means it ran turn selection and advanced the job's poll cursor for a request whose body is discarded. That is the
   real consequence, and it is larger than the `Allow` header question it was first noticed through; a dedicated
   non-claiming handler is needed. See [§6](#6-get-routes-on-a-post-shaped-mux).
3. **`PathValue` returns the percent-DECODED segment.** `GET /agent/runs/run%2Fabc` produced
   `r.PathValue("id") == "run/abc"` — a literal `/` inside a single path segment. The lane key joins its namespace
   with `/`, so an unvalidated path value reaching a lane key can be re-split into a different `(namespace, key)`
   pair. This is why identifier validation in [§7.1](#71-the-identifier-charset-is-load-bearing) is mandatory rather
   than defensive.
4. Prefix stripping preserves path values: `GET /n/t-42/agent/runs/run_abc` through `provider.NewMux`'s outer mux
   still yields `id == "run_abc"`, because the stripper clones and rewrites the path *before* the inner mux matches.
   Namespaces and wildcards compose with no extra work.

---

## 1. The shape of the problem

### 1.1 What a job is on the wire

Both vendors do the same thing with different spellings:

| | Exa | Tavily |
|---|---|---|
| Create | `POST /agent/runs` | `POST /research` |
| Create response | `{"id": "...", "status": "queued", ...}` | `{"request_id": "...", "status": "pending", ...}` |
| Poll | `GET /agent/runs/{id}` | `GET /research/{request_id}` |
| Poll response | same envelope, `status` advances, `output` appears at terminal | `status` advances, `content` appears at terminal, HTTP status is 202 while pending and 200 at terminal |
| Credential on create | `x-api-key` header | `Authorization: Bearer`, **and also** a body `api_key` (§12 item 2) |
| Credential on poll | `x-api-key` header | `Authorization: Bearer` only — no body to carry a key in |
| Identifier shape | opaque; 32 lowercase hex matches Exa's house style | UUID, matching Tavily's `request_id` convention |

Shipped as: the Exa create's initial status is `queued`, not `running` — `AgentRunProjection`'s own lifecycle
comment and `handleAgentRunCreate`'s `renderRunCreated` agree, and `TestAgentRunCreateThenPollToCompletion` asserts
it. The Tavily credential row was corrected once already, in §12 item 2: the vendor documents `Authorization:
Bearer` on both routes, and the shipped clients' body `api_key` is accepted **in addition to** it on the POST,
never as its sole scheme — the difference between the two routes is which placements a GET has room to carry, not
which scheme the vendor requires. The Exa credential row also applies to all three routes, not just create and
poll: `authHeaders = []string{"authorization", "x-api-key"}` (`provider/exa/request.go`) is declared as
`Route.Credentials` on `HEAD` as well.

**Shipped as: the create response is not `{identifier, initial status}` and nothing else.** Both vendors' create
bodies carry other required fields, derived from `Scenario.BaseTime()` and (for Tavily) echoed from the request —
see the "Shipped as" note under [§4.3](#43-a-create-handler-end-to-end) for the exact field sets. The create
response remains an *envelope* in the sense that matters here: none of those extra fields is something a scenario
author can script, because a projection body alongside `turns:` is already a load error. Everything interesting —
the run's progress, its terminal payload, its failure — is still on the poll.

### 1.2 What already fits, exactly

A poll sequence is *N responses in order, on one route, for one job*. That is the turn model's primitive with the
nouns changed. Turn selection is keyed on the lane cursor, `call_index` counts prior calls in that lane, and the
fallback rule ("when nothing matches, the last turn with no `when` is used") is precisely "keep returning the
terminal snapshot forever".

So if the poll route's lane is **one lane per job**, then:

- `when: {call_index: 0}` is *the first poll of this job*;
- `N` turns with `call_index: 0…N-1` are *N pending polls*;
- one unconditional terminal turn is *completed / failed, and stable under continued polling*;
- an unconditional **pending** turn with no terminal turn after it is *stuck-pending*, in one line;
- a single-shot provider block — one unconditional turn — is *a job that is already finished on its first poll*,
  which is the same degenerate-case framing the turn model already uses for one-shot providers.

No new scenario concept. No second cursor. Every fault kind, every `turn_key` discriminator, every namespace rule
and the whole journal apply unchanged. [§2](#2-the-scenario-yaml) is that YAML.

### 1.3 The one thing the turn model cannot carry

**A job identifier is server-minted, and the turn model has no way to carry a fact from one request to a later one.**

Every lane discriminator that exists today — `route`, `body_json:<path>`, `header:<name>` — is a *client-supplied
property of the request being served*. That is not an accident of the implementation; it is what makes the lane a
pure function of the request and therefore replayable. A job identifier is the opposite: the create invents it, the
client echoes it back, and the poll has to know that *this* name was minted *here*, by *which* call, in *which*
namespace.

Three concrete things fail without a create-side record:

1. **`GET /agent/runs/never-existed` cannot 404.** With derivation alone, any well-shaped identifier opens a fresh
   lane and is answered with poll 0 of the script. The adopter's client would then never be caught polling a stale or
   garbage identifier, which is exactly the defect class a simulator is supposed to catch.
2. **A poll cannot be attributed to its create.** The GET carries no body, so no `when` axis can reach whatever the
   create's request said. Without a record, "the job created for query X polls to payload P" is inexpressible.
3. **A pasted identifier crosses namespaces silently.** Identifiers are derived without the namespace so that golden
   files stay portable ([§5.1](#51-identifier-derivation)), which makes two concurrent tests minting the *same*
   identifier the normal case. Keyed on the identifier alone, test A's poll would be answered out of test B's job.

That record is a genuinely new piece of state and this design does not pretend otherwise. It is made as small as
state can be: it holds **coordinates, never rendered bytes**. The terminal payload is re-rendered from the scenario
on every poll, so a job's response stays a pure function of `(scenario, lane, call index)` exactly as a one-shot
response is. See [§4.1](#41-internaljobs).

### 1.4 The alternative that was rejected

The tempting shape is to make the create and its polls **one lane**, so a script reads top to bottom as
`turn 0 = create, turns 1…N = polls`. It needs the create to pre-claim index 0 in the poll lane at mint time, and it
was rejected for two reasons:

- `Faults.Next` is the only claim primitive on the seam, and it *consumes a fault attempt*. A create burning index 0
  of the poll route's plan means a scripted `attempts: [{status: 429}]` on polls fires against nothing and vanishes —
  a silently consumed budget, which is the failure class the whole fault design is written against. Fixing it means
  widening `provider.Faults` with a non-consuming claim, which is a seam change for cosmetics.
- Half the `when` axes become meaningless past turn 0. `body_contains` and `body_json` cannot match a GET with no
  body, so a script whose first turn accepts body predicates and whose remaining turns silently cannot is a trap.

The chosen split — the create lane is route-keyed, the poll lane is job-keyed — keeps both counters honest about
what they count.

---

## 2. The scenario YAML

The async surface is **its own provider entry**, following the `perplexity` / `perplexity_agent` precedent exactly:
independent `auth`, `validation`, `fault` and `turns`, resolved by the handler through `x.Entry()` rather than by
listener name. A scenario that uses only the sync surfaces omits the entry and is unaffected.

Shipped as: the handler calls `x.Entry()` (`Exchange.Entry()`, [§5.1](#51-identifier-derivation)), not a hardcoded
`x.Deps.Scenario.Provider(NameAgentRuns)` — the fix that makes `Route.Entry` actually resolve the second entry on a
listener is what this same design needed for its own `turn_key` and `validation:` block to be read at all, so the
async handlers use the general mechanism rather than a special case of it.

**A turn of an async entry is one poll snapshot.** That sentence is the whole schema addition, and it adds no keys.

### 2.1 Two pending polls, then completed

```yaml
version: 1
name: exa-agent-run-completes

sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    text: The report states the finding.

providers:
  exa:                                   # the existing sync surface, untouched
    results: [source-a]
    cost_dollars: {total: 0.005}

  exa_agent_runs:                        # POST /agent/runs + GET /agent/runs/{id}
    auth:
      mode: required
    turns:
      - when: {call_index: 0}            # poll 1
        respond: &pending
          status: running
      - when: {call_index: 1}            # poll 2
        respond: *pending
      - respond:                         # poll 3 and every poll after it
          status: completed
          output:
            text: Report A states the finding.
            grounding:
              - field: answer
                citations: [source-a]
                confidence: high
          cost_dollars: {total: 0.045}
```

Shipped as: `output` is `{text, structured, grounding}`, not `{content, citations}` — `AgentRunProjection.Output`
is `*AgentOutputProjection{Text, Structured, Grounding}`, and each `grounding[]` entry is `{field, citations,
confidence}` (`GroundingProjection`, resolved against the corpus exactly like `exa`'s own `output.grounding`), not
a flat citation list on `output` itself. An earlier draft of this example used `output.content`/`output.citations`;
that shape was never built, and `docs/scenario-schema.md`'s async section and `scenarios/protocol/happy.yaml` are
both authoritative for the shipped one, which this example now matches.

The YAML anchor is doing the de-duplication a `repeat:` key would otherwise do. That is deliberate — see
[§2.5](#25-the-sugar-that-was-rejected).

This anchor loads: `Providers.UnmarshalYAML` resolves every alias in the providers tree into a deep copy before any
raw node is retained; it briefly did not, because retained nodes were re-marshalled and strict-decoded one turn at
a time (`scenario/alias.go`).

The terminal turn is unconditional and therefore also answers polls 4, 5, 6…, which is what every real job API does:
a finished run keeps returning its result. Nothing special-cases it; it is the existing fallback rule.

### 2.2 Failed

```yaml
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - respond:
          status: failed
          error:
            code: AGENT_RUN_FAILED
            message: the run could not be completed
```

`status: failed` with no `error` is a load-time **error** finding (`exa.agent_run.failed_without_error`), for the same
reason `extended-surfaces.md` rejects a failed Agent status with no error object: a consumer's terminal-state handler
is being tested, and handing it a failure with no reason tests nothing.

### 2.3 Stuck pending

```yaml
  exa_agent_runs:
    turns:
      - respond: {status: running}       # one unconditional turn: never terminal
```

One line. The job is created, every poll answers `running`, and the *consumer's* timeout is what fires — which is the
behaviour under test. Servicesim does not decide the job is stuck; it simply never terminates it.

### 2.4 The zero-poll degenerate case

```yaml
  exa_agent_runs:
    status: completed                    # single-shot form: one unconditional turn
    output:
      text: Report A states the finding.
      grounding:
        - field: answer
          citations: [source-a]
```

A single-shot block normalises into exactly one unconditional turn, so the first poll is already terminal. The
one-shot form is the zero-pending-poll job in the same way that a single projection is the length-1 conversation.
Everything downstream sees one shape.

**Added at implementation: `tavily_research` uses the same schema with different projection keys, and no example
of it appeared anywhere in this section.** Every YAML example above is `exa_agent_runs`; an author copying one onto
`tavily_research` would hit a strict-decode error, because the shapes are not interchangeable. The shipped
`ResearchProjection` (`provider/tavily/research.go`) is:

```yaml
  tavily_research:
    turns:
      - when: {call_index: 0}
        respond: {status: in_progress}
      - respond:
          status: completed
          content: The report states the finding.
          sources: [source-a]
          response_time: 1.2
```

`status` is one of `pending`/`in_progress`/`completed`/`failed`; there is no `cancelled` status and no Exa-style
`error` object — a failed task says what happened through `content`, the same key a completed one uses. The poll's
HTTP status is 202 while non-terminal and 200 once terminal (`ResearchProjection.HTTPStatus`), which has no Exa
analogue: Exa's poll always answers 200 and carries the state in `status`. `docs/scenario-schema.md`'s async
section and `scenarios/protocol/happy.yaml` are authoritative for this shape.

### 2.5 The sugar that was rejected

`repeat: 2` on a turn would be tidier than the anchor, and it is not in this design:

- It makes turn selection **positional** ("this turn answers the next N calls") in a model that is otherwise
  **predicate-based** ("the first turn whose `when` matches"). The two have to be reconciled in one selector, and the
  reconciliation is where a fixture author's mental model breaks.
- It would change what `call_index` means for existing files the moment the two are combined, which is a
  version-2 event ([§9](#9-schema-versioning-additive-to-version-1)) bought for syntax.
- YAML anchors are standard and are the intended solution to the duplication, and they do work across turns in this
  loader ([§2.1](#21-two-pending-polls-then-completed)); `repeat:` would only duplicate a mechanism that already
  exists.

`fault:` on an async entry keeps the documented rule: a plan is registered **per route**, the first turn declaring
non-empty `attempts:` supplies it, and it starts consuming from that route's first request.

The route it supplies is the **poll** route. A turn of an async entry is one poll snapshot, so a plan declared on a
turn belongs to the route those snapshots answer; the create call has no turn to hang a plan on and reads
`create.fault` instead. [§3.1](#31-the-route-table) is the authority on that split and gives the mechanism.

Because the poll route's lane is per job ([§3](#3-routes-fault-keys-and-the-per-job-lane)), a poll plan consumes
**per job** — which is what makes `attempts: [{status: 200}, {status: 429}, {status: 200}]` mean "every job's second
poll rate-limits" instead of "whichever job happens to poll second".

A create plan and a poll plan are therefore two separate `attempts:` lists in two separate places, resolved by two
separate selectors. They are never one list read twice.

### 2.6 Validation the provider package owns

Each async entry has its own validator — `AgentRunValidator` for `exa_agent_runs`, `ResearchValidator` for
`tavily_research` — and the two do not check the same things.

`AgentRunValidator.ValidateProjections` runs before readiness and reports:

| Finding | Severity | Condition |
|---|---|---|
| `exa.agent_run.failed_without_error` | error | `status: failed` with no `error` |
| `exa.agent_run.terminal_then_pending` | error | a non-terminal turn declared after a terminal one — a job that un-completes |
| `exa.agent_run.script_exhausted` | warning | no unconditional final turn: poll N+1 gets `scenario.no_matching_turn` and a 404 the author did not intend |
| `exa.agent_run.body_predicate_on_poll` | warning | `body_contains` or `body_json` on a turn of an async entry; a GET carries no body, so the predicate can never match |
| `exa.agent_run.completed_without_output` | warning | `status: completed` with no `output` |
| `exa.agent_run.status.unknown` | error | a poll status outside the documented `queued`/`running`/`completed`/`failed`/`cancelled` set |
| `exa.agent_run.stop_reason.unknown` | error | a `stop_reason` outside the documented enum |

The script/predicate pair are the ones a fixture author actually hits, and both are silent failures without a
load-time check.

`ResearchValidator.ValidateProjections` reports a smaller set, because Tavily's projection has no `stop_reason` and
`tavily_research` has no equivalent check for an unconditional final turn or a body predicate on a poll:

| Finding | Severity | Condition |
|---|---|---|
| `tavily.research.status.unknown` | error | a poll status outside `pending`/`in_progress`/`completed`/`failed` |
| `tavily.research.completed_without_content` | warning | `status: completed` with no `content` |
| `tavily.research.terminal_then_pending` | error | a non-terminal turn declared after a terminal one |

`docs/scenario-schema.md`'s async section is the authoritative, kept-current list for both validators, sourced
directly from `provider/exa/agentrun.go` and `provider/tavily/research.go`; this table exists so a reader does not
have to leave this document to see the shape of the check.

---

## 3. Routes, fault keys and the per-job lane

### 3.1 The route table

| Listener | Route | Fault key | Plan read from | `LaneFrom` |
|---|---|---|---|---|
| exa | `POST /agent/runs` | `exa:agent_runs.create` | `create.fault` on the entry | — |
| exa | `GET /agent/runs/{id}` | `exa:agent_runs.poll` | the first turn declaring `attempts` | `["path:id"]` |
| exa | `HEAD /agent/runs/{id}` | `exa:agent_runs.head` | none — never claims an attempt | `["path:id"]` |
| tavily | `POST /research` | `tavily:research.create` | `create.fault` on the entry | — |
| tavily | `GET /research/{request_id}` | `tavily:research.poll` | the first turn declaring `attempts` | `["path:request_id"]` |
| tavily | `HEAD /research/{request_id}` | `tavily:research.head` | none — never claims an attempt | `["path:request_id"]` |

**Added at implementation:** `HEAD` on either provider is a fourth and fifth route, not a variant of the poll row.
See [§6](#6-get-routes-on-a-post-shaped-mux) for why it needs its own `FaultKey` and never reaches `SelectTurnFor`.

Create and poll draw on **separate** budgets, for the same reason `exa:search` and `exa:answer` do: a poll retry must
not consume the create's retries, and a retry of one must not be answered from the other's plan.

Separate budgets take **two** things, and an earlier draft of this document supplied only the first:

1. **Separate fault keys** give separate *counters*. That is the `Fault key` column, and it is sufficient for
   attempts to be counted independently.
2. **Separate `Route.Fault` selectors** give separate *plans*. Without this, both keys resolve the same
   `attempts:` list — two independent counters walking one script. `attempts: [{status: 429}, {status: 200}]`
   would then rate-limit the create AND, separately, every job's first poll, which is not what its author wrote.

The `Plan read from` column is that second half. It follows the shipped `answerFault` precedent exactly
(`provider/exa/handler.go`'s `answerFault`): two routes on one entry already get two distinct plans there, by
giving each route a selector that reads a *different location* in the scenario. `/search` reads the block-level
`fault:`; `/answer` reads `answer.fault` inside the projection. Nothing new is being invented here.

For an async entry the split falls out of the schema itself. [§2](#2-the-scenario-yaml) establishes that **a turn is
one poll snapshot**, so a plan declared on a turn is unambiguously the *poll* plan — it is attached to the thing
polls are made of. The create call has no turn at all, so it needs its own key:

```yaml
  exa_agent_runs:
    create:                          # the POST plan
      fault:
        attempts:
          - {status: 429, retry_after: 1}
          - {status: 201}           # the create's real status — see the note below
    turns:                           # each turn is a poll; a turn plan is the POLL plan
      - when: {call_index: 0}        # turn 0 must be conditional, or turn 1 is unreachable
        fault:
          attempts:
            - {status: 200}
            - {status: 503}          # every job's SECOND poll fails
            - {status: 200}
        respond: {status: running}
      - respond: {status: completed}
```

**Fixed in place, in this doc-truth pass** (2026-08-15): turn 0 above was unconditional in an earlier version of
this example, which `scenario.validateProviderEntry` rejects at load as `scenario.turn.unreachable` — turn 0 having
no `when` means turn 1 can never be selected. `docs/scenario-schema.md`'s equivalent example had this right; this
one did not, and a fixture author copying it would have hit the load error immediately.

Because the poll route's lane is per job ([§3.2](#32-routelanefrom)), that poll plan consumes **per job**: the
`503` is every job's second poll, not whichever job happens to poll second globally.

**Added at implementation: a kind-none attempt that names `status` pins the wire status, not just the create's
success attempt above.** This is a general `Faults`/`execute` behaviour (`provider/fault_exec.go`'s `execute`
applies `a.Status` whenever it is set, before it switches on `EffectiveKind`), not something this design added, but
it bites specifically here because the async routes are the first ones whose baseline status is not a flat 200:
Exa's create answers `201`, and a `tavily_research` poll answers `202` while non-terminal and `200` once terminal.
A pass-through attempt written as `{status: 200}` — the pattern every earlier fault example in this repository
uses, because every earlier route answers 200 — silently downgrades a create's `201` or a pending Tavily poll's
`202` to `200` on that attempt, which is wrong and easy to miss because the body still renders correctly. The fix
is to name the route's real status when pinning one is not the point (`{status: 201}` above), or to write the
pass-through attempt with no `status` and no fault kind at all — `- {}` — which lets `execute` fall through to
whatever the handler actually rendered. `docs/scenario-schema.md`'s async section documents this explicitly and is
the authoritative statement of the rule; this paragraph exists so a reader of this design does not have to
discover it by writing the wrong fixture first.

`create.fault` is resolved by a `createFault` selector written next to the route that uses it — the same shape as
`answerFault`, nil-safe on every hop, because it runs at composition time against a scenario that may not have been
validated yet. The poll route's own selector is the exported `provider.TurnFault(s, name)`, shared by both
providers rather than reimplemented per package.

**`create:` had to be made an envelope key, or none of the above would load** — and the lever was not the one it
looked like at design time.

`reservedEnvelopeKeys` looked authoritative and was not: it was referenced by its own definition and by one test,
and **the loader never read it**. The envelope split is a hardcoded `switch key.Value` in `decodeProviderEntry`
(`scenario/model.go`) whose `default:` arm sweeps everything else into the projection body. Adding `create` to the
slice alone would have changed nothing.

**Shipped as:** all of the following landed together, in `scenario/model.go`:

| Change | Where |
|---|---|
| `keyCreate = "create"` const | beside the other `key*` consts |
| `case keyCreate:` arm with a strict decode | the switch in `decodeProviderEntry` |
| `Create *CreatePolicy` field | on `ProviderEntry` |
| `HasFaults` follow-through | `Scenario.HasFaults()` counts a create-only plan, so `create.fault` with no turn-level fault still reports the scenario as having faults |
| the slice updated too | `reservedEnvelopeKeys`, keeping its own test honest |

**Shipped as: `Validate` did *not* get the follow-through the table above promised.** `scenario/validate.go`'s
`validateProviderEntry` calls `validateFault` for a turn's fault and for the block-level fault, but never for
`e.Create.Fault` — a malformed `create.fault` (an unknown attempt kind, a negative `retry_after`, an empty
`attempts:` list) loads silently rather than failing at load the way a malformed turn-level fault does. This is a
real gap, not a documentation slip: `HasFaults` reads `e.Create.Fault` and `Validate` does not, so the two halves
of "follow-through" this design asked for landed unevenly.

**Compatibility is worse than additive, and stayed that way.** An older binary meeting a file with `create:` does
*not* reject it cleanly — `KnownFields(true)` never sees the key, because the `default:` arm has already swept it
into the projection body. What the author gets is `scenario.provider.body_with_turns`: *"move create inside a
turn's `respond:`"* — advice aimed at a key that cannot go there. That is a real forward-compatibility cost, which
is why the envelope key landed **before** adopters authored async fixtures rather than after.

Note also that this is why the create plan is nested under `create:` rather than written as a second block-level
`fault:`: a block-level `fault:` alongside `turns:` is already a load error
(`scenario.provider.fault_with_turns`), and it is the right error — in the multi-turn form there is genuinely no way
to tell which route a bare `fault:` meant. Nesting makes the route explicit in the key itself.

### 3.2 `Route.LaneFrom`

```go
// LaneFrom names lane discriminators this ROUTE contributes, in the same
// extractor grammar as scenario.TurnKey and evaluated after the scenario's, so
// the lane key stays "<route key> | <scenario extractors> | <route extractors>".
//
// It exists because a poll route's per-job lane is not a scenario author's
// choice. Two jobs polled concurrently in one namespace share a route, and a
// route-keyed cursor hands each poll the snapshot scripted for the other job —
// the fan-out failure turn_key exists to prevent, arriving from the routing side
// rather than the scenario side. A wrong status code fails loudly; a coherent
// "running" that belongs to somebody else's job fails somewhere else entirely,
// much later.
//
// It is also what keeps the scenario schema additive. turn_key rejects an
// unrecognised extractor as a LOAD ERROR, so a "path:" extractor written in a
// scenario file would be the one change in this design that an older binary
// refuses to load. Declaring it on the route means no scenario file mentions it
// and no older binary ever sees it. See docs/design/async-jobs.md §9.
LaneFrom []string
```

The extractor form is declared in `provider`, not in `scenario`, because it is deliberately not scenario-facing:

```go
// LaneFromPath extracts a ServeMux wildcard: "path:id" reads r.PathValue("id").
// It is reachable only through Route.LaneFrom; scenario.Validate does not accept
// it in turn_key.
const LaneFromPath = "path:"
```

`turnLaneKey` gains one branch and one correction:

```go
func turnLaneKey(x *Exchange) string {
	extractors := entryTurnKey(x).Extractors()
	// Route discriminators come last, so a scenario's turn_key subdivides the
	// route and this subdivides that. Order is part of the key: reversing it
	// would rename every lane.
	extractors = append(extractors, x.Route.LaneFrom...)

	// hasDiscriminator must see the route's extractors too. Without this a poll
	// route with the default turn_key takes the allocation-free path and returns
	// Route.FaultKey verbatim — one lane for every job, which is the bug.
	if !hasDiscriminator(extractors) {
		return x.Route.FaultKey
	}
	// …existing loop, plus:
	case strings.HasPrefix(extractor, LaneFromPath):
		name := strings.TrimPrefix(extractor, LaneFromPath)
		value := x.Request.PathValue(name)
		parts = appendLanePart(x, parts, extractor, value, ValidJobID(value))
	}
```

**A failing `LaneFrom` extractor raises its own finding, not `scenario.turn_key_unresolved`.** Reusing that code
would address a `turn_key` field to an author who never wrote one — `LaneFrom` is declared on the route, in Go, and
is deliberately not scenario-facing. Someone reading `scenario.turn_key_unresolved` on `field: turn_key` goes
looking through their YAML for a `turn_key:` that is not there and cannot be added.

| Code | Field | Raised when |
|---|---|---|
| `job.id_invalid` | the wildcard's own name | a `path:` extractor resolves to empty, or to a value `ValidJobID` rejects |

**Shipped as: the field is the wildcard's own name, not always `id`.** `pathFault(name)` (`provider/lane.go`) sets
the field to whatever the extractor names — `id` for `exa_agent_runs`'s `path:id`, `request_id` for
`tavily_research`'s `path:request_id` — which is the thing the client actually got wrong, since it is in the URL
they sent. `appendLanePart` therefore takes the code and field to raise rather than assuming the `turn_key` pair.

A poll lane key therefore reads:

```text
key   t-42/exa:agent_runs.poll|path:id=run_9f2c1ab4e5d67890abcdef0123456789
      └──┘ └─────────────────┘ └──────────────────────────────────────────┘
       ns    Route.FaultKey     Route.LaneFrom contribution: the job itself
```

Three properties of that string are load-bearing and all three already hold:

- `SplitCursorKey` recovers `("t-42", ["exa:agent_runs.poll", "path:id=run_…"])`, and `Engine.planFor` finds the
  registered route key as the first part, so the poll route's declared fault plan still resolves. Nothing in
  `internal/faults` learns that jobs exist.
- The namespace is the outermost component, so two tests polling the same job identifier never share a cursor.
- Neither separator (`/`, `|`) can appear in the identifier — enforced, not assumed
  ([§7.1](#71-the-identifier-charset-is-load-bearing)).

**Added at implementation: a credential-named or credential-shaped lane component is fingerprinted before it joins
the key.** This design's own `path:id` contribution is never a credential, so it is unaffected by this directly —
but it landed in the same lane-key code path (`turnLaneKey`, `provider/lane.go`) this design changed, on the
argument that a lane key is exactly the kind of retained structure CLAUDE.md house rule 4 forbids a credential from
reaching: it becomes `FaultDecision.Key`, then `journal.Entry.Outcome.FaultKey`, retained in the ring and served by
`GET /__admin/requests`. A scenario's own `turn_key: [header:authorization]` or `turn_key: [body_json:api_key]`
extractor — "route by which key was presented", the credential-rotation shape Phase 6 anticipates — is valid
(`scenario.Validate` checks an extractor's form, never its value) and would otherwise write the raw credential into
that retained key. `fingerprintLaneValue` and `fingerprintHeaderLaneValue` substitute a deterministic
`redact.Fingerprint` for the value under two independent tests, by property name (`redact.IsCredentialHeader`, or
any dotted `body_json` segment `redact.IsCredentialKey` flags) and by shape (whatever `redact.String` would itself
change) — reusing the exact fingerprint `httpx.ObserveAll` already computed for `header:authorization` and
`header:x-api-key` placements, so a lane component and `journal.Entry.Auth.Fingerprint` agree byte-for-byte.

Does this touch anything [§5.1](#51-identifier-derivation) states? **No — verified, not merely asserted.** §5.1's
derivation tuple names `lane.Key` as one of `mintID`'s inputs and is silent about what populates it; nothing in
§5.1 claims a lane-key component is the credential's raw bytes, so there is nothing there to contradict. The two
routes this design defines never put a credential in a `turn_key` or `Route.LaneFrom` extractor in the first
place — `path:id`/`path:request_id` name a path wildcard, never a credential — so this mechanism is inert for
`exa_agent_runs` and `tavily_research` specifically and only matters for a *scenario* that declares its own
credential-bearing `turn_key` on the async entry, same as it would on any other multi-route entry.

### 3.3 What comes free once the lane is per job

Because the poll cursor *is* the fault attempt counter — one counter, the property the base design insists on —
making it per job makes every existing mechanism per job with no further work:

| Existing mechanism | Per-job meaning, for free |
|---|---|
| `when: {call_index: N}` | poll N of this job |
| `fault: {attempts: [...]}` on the poll route | every job's Nth poll faults identically |
| `delay:` on a poll attempt | this job's Nth poll is slow — a Temporal heartbeat test |
| `truncate_body`, `close_before_headers` | a poll dies mid-response, per job |
| `/n/<namespace>` isolation | two tests' jobs never share a cursor |
| `POST /__admin/reset?namespace=` | one test's jobs and cursors drop together |
| journal `arrived_at` / `completed_at` | poll pacing is already assertable, per poll |

The journal entry's `outcome.fault_key` carries the composed lane key, so filtering a namespace's entries down to
"the polls of job X" needs no new admin surface — it is a substring match on a field that is already there.

---

## 4. Types and the seam

### 4.1 `internal/jobs`

A new level-1 package: it imports nothing in-module, exactly like `internal/redact` and `internal/ids`.

```go
// Package jobs is the async-job registry: the create-side fact a later poll has
// to be able to read.
//
// It is deliberately the smallest store that can answer "does this identifier
// exist in this namespace, and which call minted it". It holds COORDINATES,
// never rendered bytes: a job's payload is re-rendered from the scenario on
// every poll, so a replayed scenario stays byte-identical and this store stays
// O(jobs) in small structs rather than in response bodies.
package jobs

// Job is one create-then-poll job.
type Job struct {
	// ID is the minted identifier, derived and therefore reproducible.
	ID string

	// Namespace is the state boundary the job was created in.
	Namespace string

	// LaneKey is the CREATE's lane key without its namespace prefix — the same
	// string ID was derived from, which is what makes a job's identity and its
	// lane impossible to disagree.
	LaneKey string

	// Entry is the scenario provider entry that answered the create, for example
	// "exa_agent_runs". A plain string, not a provider.Name and not a
	// *scenario.ProviderEntry: this package sits below the provider seam and
	// below scenario, and naming either type is the import cycle that
	// journal.Entry.Provider avoids for the same reason.
	Entry string

	// CreateIndex is the call index the create claimed in LaneKey. It is the only
	// number a poll needs to attribute itself to its create.
	CreateIndex int

	// TurnIndex is which turn of the create route's script answered the create,
	// recorded for diagnostics and for the admin listing.
	TurnIndex int

	// CreatedAt is real time, for the admin listing and for diagnostics. It is
	// NEVER rendered into a response body: house rule 2 keeps clocks off anything
	// a replay has to reproduce.
	CreatedAt time.Time
}

// Store is the seam provider.Deps carries. It is an interface for the same
// reason journal.Journal is one: a consumer must be able to substitute a
// recording implementation without importing internal/....
type Store interface {
	// Create records j. It returns ErrLimit when j.Namespace is at its job bound
	// and ErrDuplicate when j.ID already exists in it.
	Create(j Job) error

	// Lookup returns the job with this identifier IN THIS NAMESPACE.
	Lookup(namespace, id string) (Job, bool)

	// ResetIn drops one namespace's jobs and returns its slots to the bound.
	ResetIn(namespace string)

	// Reset drops every namespace's jobs.
	Reset()

	// StatsIn reports one namespace's job count and bound, for the admin surface.
	StatsIn(namespace string) Stats
}

// Errors Create reports. They are sentinels rather than a bool because the two
// refusals need different provider-shaped answers: a bound breach is the
// simulator saying no, and a duplicate identifier means two different jobs were
// asked to answer to one name — a reset that dropped cursors without dropping
// jobs, which is a wiring bug worth naming.
var (
	ErrLimit     = errors.New("jobs: namespace is at its job bound")
	ErrDuplicate = errors.New("jobs: an identifier was minted twice in one namespace")
)

// Registry is this package's in-memory Store. Return structs, accept
// interfaces: callers assign it to a Store field.
//
// Jobs are held per namespace, bounded per namespace, and NEVER evicted.
// Evicting a job breaks a running test's poll loop with a 404 on an identifier
// it legitimately holds — the same "single worst failure" the namespace design
// names, arrived at one level down. A create beyond the bound is refused
// loudly instead.
type Registry struct {
	mu     sync.Mutex
	byNS   map[string]map[string]Job
	limits Limits
}

// Limits bounds what a Registry holds. MaxJobs is PER NAMESPACE, so the total
// ceiling is MaxNamespaces × MaxJobs — the same product shape journal.Limits
// documents, and bounded by the same --max-namespaces on the way in.
type Limits struct {
	// MaxJobs is how many live jobs one namespace may hold. Zero or negative
	// means DefaultMaxJobs.
	MaxJobs int
}

// DefaultMaxJobs bounds one namespace's live jobs. At the default namespace
// bound of 1024 the worst case is 262144 records of roughly 150 bytes, about
// 40 MiB — a ceiling a shared container can survive, reached only by a suite
// that creates 256 jobs in each of 1024 namespaces and never resets.
const DefaultMaxJobs = 256

// NewRegistry returns a Registry bounded by l, with every out-of-range field
// replaced by its documented value.
func NewRegistry(l Limits) *Registry
```

The registry deliberately does **not** admit namespaces. `provider.Handle` already asks the fault engine's
`NamespaceAdmitter` before any handler runs, so a job can only ever be created in a namespace that was admitted a few
lines earlier. Adding a third admission authority would mean three stores that must agree about exactly which
namespaces are live, and the existing design already spends a careful paragraph on making two agree.

#### The bound refuses; it does not evict

There is **no release path and no eviction**, and that is the decision rather than an omission. `ResetIn` returns a
namespace's slots ([§7.3](#73-reset-must-drop-cursors-and-jobs-together)), which covers every namespaced suite. The
gap is the other shape: a long-lived shared container with no namespacing, where the 257th create in the default
namespace is refused and stays refused.

FIFO eviction — the journal `Ring`'s behaviour — was considered and **rejected**, because the two stores lose
different things. An evicted journal entry costs *observability*: the request still happened and was still served
correctly. An evicted job record costs *correctness*: a poll for a job the client successfully created now returns
the vendor's 404, with no finding, intermittently, depending on how many other jobs a co-tenant test happened to
create. That is precisely the failure [§8](#8-multi-replica-the-consequence-stated-explicitly) spends its length
teaching operators to recognise, manufactured locally by the simulator itself.

A create refused at the boundary is the better failure by a wide margin: it is loud, it is attributable to the
request that hit the wall, and it names its own remedy.

So the bound is enforced and made **visible before it is reached**:

| Signal | When | Severity |
|---|---|---|
| `job.limit_near` | a create takes a namespace past 80% of `MaxJobs` | warning, on the journal entry |
| `job.limit_reached` | a create is refused | error, plus the provider-shaped 503 |

**Shipped as: only `job.limit_reached`'s message names all three remedies.** `CodeJobLimitReached`'s text
(`provider/jobs.go`) reads "reset it with `POST /__admin/reset`, give each test its own namespace, or raise the
bound" — all three. `CodeJobLimitNear`'s text names only the first two ("reset it or use per-test namespaces"),
because raising the bound is not something a warning fired mid-suite can act on the same way; the create it warns
about still succeeds. Both name the namespace and its occupancy so the reader knows which one to act on. The status
is 503 specifically on both vendors (`provider/exa/errors.go`, `provider/tavily/errors.go`), not "5xx" generically.

The 80% mark exists because the wall is otherwise reached with no warning on the request *before* the failing one.
A suite creeping toward the bound over weeks gets a warning in its journal long before a create fails in CI.

**Shipped as: `internal/jobs`'s exported shape differs from the block above in three ways, all illustrative per the
banner.** `Stats{Count, Bound}` is defined, with `Near()` and `Full()` methods and a `HighWaterPercent = 80`
constant `Near` reads against — the sketch above uses `Stats` as a return type and never defines it.
`Store.Create` returns `(Stats, error)`, not `error` alone, so a caller can report occupancy on every path including
a refusal. `Job` has no `TurnIndex` field — [§7.4](#74-optional-a-read-only-admin-listing) explains why: the poll
cursor lives in the fault engine, which offers no non-claiming read, so there is nothing to populate it from.

### 4.2 `provider` additions

`provider` gains one `Deps` field, one `Route` field, one constant, and two helpers, at design time. **Shipped as:**
two `Deps` fields (`Jobs`, `MaxJobs`); two `Route` fields (`Entry`, `LaneFrom`); one lane-extractor constant
(`LaneFromPath`) plus `MaxJobIDLen` and five `CodeJob*` finding-code constants; and three helpers (`ValidJobID`,
`MintJob`, `ResolveJob`). The illustrative count undersold the surface; every addition is still additive.

```go
// Jobs records async-job identifiers so a later poll can resolve one. nil means
// a fresh jobs.NewRegistry, which is per-Deps and never shared — the same rule
// as journal.NewDiscard, and for the same reason: two handlers in parallel
// tests must not resolve identifiers out of one another's registry.
//
// Note the asymmetry with Faults, which substitutes a NO-OP for nil. A no-op
// job registry would 404 every poll, so exa.New(provider.Deps{}) would serve a
// broken async surface; the substitute here has to work.
Jobs jobs.Store

// MaxJobs bounds live jobs per namespace. Zero means jobs.DefaultMaxJobs.
MaxJobs int
```

**Shipped as: nil `Jobs` means NO job state, and `Normalized` does not substitute a registry.** The bullet above
argued for the opposite and shipped code rejected it. `Deps.Jobs`'s own doc comment now reads: "nil means no job
state at all: a create still derives and returns an identifier, and the poll that follows simply cannot resolve
it. That is the same shape as a nil Faults... rather than a nil-check in every handler. `Normalized` does NOT
substitute a registry, deliberately: a store created per Deps would be invisible shared state for anyone who built
two Deps expecting one process, and the async surfaces are opt-in." `internal/server` and `testkit.NewJobs` wire a
real registry explicitly instead. This is a real asymmetry with `Faults` — a nil `Faults` IS normalized into a
counting no-op — and the two decisions read as inconsistent until the reason is stated: a no-op *fault* substitute
is indistinguishable from "no faults declared," which is a legitimate scenario shape; a *silently per-Deps* job
registry would let two handlers built from the same nil-Jobs `Deps` literal believe they share state when they do
not, which is the opposite of what determinism promises. `MintJob` and `ResolveJob` both nil-check `x.Deps.Jobs`
and degrade gracefully — a create still answers, the poll after it cannot resolve. Note this is unrelated to
`internal/admin.Deps`, a different struct, whose own `normalized()` DOES substitute `jobs.NewRegistry(jobs.Limits{})`
for a nil `Jobs` field — the admin surface has to answer `/__admin/jobs` for a zero-value `Deps` the same way it
answers `/healthz`, which is a different obligation from the provider seam's.

```go
// MintJob derives this request's job identifier, records it, and returns it.
//
// It must be called only AFTER authentication and validation have passed,
// because it claims the call index the identifier is derived from and a
// rejected request must not consume one (package-design §4.4). It is normally
// the create handler's first act once it has decided to answer.
//
// encode is how the provider spells an identifier — ids.Hex32 for Exa's 32
// lowercase hex characters, ids.UUIDv5 for Tavily's request_id — so the shape
// stays the vendor's while the derivation stays here and stays one function.
// prefix is prepended after encoding and is excluded from the hash input, so
// "run_" is cosmetic rather than load-bearing.
//
// The finding is recorded and (nil, false) returned when the registry refuses:
// job.limit_reached for a bound breach, job.id_collision for a duplicate.
func MintJob(x *Exchange, entry, prefix string, encode func(...string) string) (jobs.Job, bool)

// ResolveJob returns the job a poll addresses, or false with the finding
// already recorded (job.unknown for a miss, job.id_invalid for a malformed
// identifier).
//
// It looks up (namespace, id), and the pair is mandatory rather than
// defensive. Identifiers are derived WITHOUT the namespace so a golden file is
// portable between a namespaced and an unnamespaced run (§5.1), which makes two
// concurrent tests minting the same identifier the normal case rather than an
// edge one. Keyed on the identifier alone, test A's second poll would be
// answered out of test B's job — silently, and with a coherent-looking body.
//
// It claims no attempt. A poll for an unknown identifier is a rejection, so it
// must not advance a cursor, and — because the cursor is what creates a lane
// entry in the fault engine's never-evicting lane map — must not be able to
// mint a permanent map key from client-supplied text either. That is the whole
// bound on lane growth: a lane exists only for a job that exists.
func ResolveJob(x *Exchange, id string) (jobs.Job, bool)
```

**Shipped as: `ResolveJob` records a finding on exactly one of its miss paths, not both.** The block above claims
"job.unknown for a miss, job.id_invalid for a malformed identifier"; the shipped function records nothing for
either of those on its own. `job.id_invalid` (`CodeJobIDInvalid`) is never raised by `ResolveJob` — a malformed
identifier just fails `ValidJobID` and falls straight through to `(jobs.Job{}, false)`, silently. `CodeJobIDInvalid`
IS raised elsewhere, at two different severities depending on where the identifier came from: a WARNING from lane
resolution's `pathFault`, before the handler ever runs, when a route's `LaneFrom` path extractor is empty or fails
`ValidJobID` ([§3.2](#32-routelanefrom)'s table); and an ERROR from `MintJob` itself, only if the identifier IT
derives somehow fails its own `ValidJobID` check — a defensive branch against a caller's encoder bug, not a request
condition. There is no `job.unknown` code at all. The one finding `ResolveJob` raises is `CodeJobForeignID`
(`job.foreign_id`), and only on the narrower condition its own godoc states: a well-formed, unresolved identifier
in a namespace that has minted at least one job ([§8](#8-multi-replica-the-consequence-stated-explicitly)). A miss
in a namespace that has minted nothing, and a miss whose identifier fails `ValidJobID`, both record nothing from
`ResolveJob` itself — the first is an ordinary typo, the second is never this process's own identifier.

**That is not the same as "a GET poll miss carries no warning at all."** The caller almost always adds one of its
own: `handleAgentRunPoll`'s `runNotFound` (`provider/exa/agentrun_handler.go`) unconditionally raises
`exa.agent_run.not_found`, and `handleResearchPoll` (`provider/tavily/research.go`) unconditionally raises
`tavily.research.not_found`, on every miss regardless of what `ResolveJob` did or did not record — so a `GET` miss
carries at least one warning always, and up to two (`job.foreign_id` plus the provider's own) when the identifier
is well-formed in a namespace that has minted something. `HEAD`'s miss path is the one that is genuinely silent:
`handleAgentRunHead` and `handleResearchHead` return a bare 404 with no `x.Warn` call of their own, so a `HEAD`
miss records only what `ResolveJob` records, which may be nothing.

```go
// ValidJobID reports whether id is a short safe identifier: 1 to MaxJobIDLen
// characters of ASCII letters, digits, '-' and '_'.
//
// It is ValidNamespace's rule and it is restrictive for the same reasons, plus
// one this design adds: PathValue returns the percent-DECODED segment, verified
// on Go 1.26.4 to turn "run%2Fabc" into "run/abc". '/' is the lane key's
// namespace separator and '|' joins its parts, so an unvalidated path value
// reaching a lane key can be re-split into a different (namespace, key) pair.
func ValidJobID(id string) bool

// MaxJobIDLen bounds an identifier taken from a request path. Derived
// identifiers are 36 characters at most; the bound exists for what a client
// sends, not for what this package mints.
const MaxJobIDLen = 64
```

### 4.3 A create handler, end to end

```go
// handleAgentRunCreate serves POST /agent/runs.
//
// The order is the fail-closed order package-design §4.4 requires: everything
// that can reject runs before MintJob, because MintJob claims the call index.
func handleAgentRunCreate(x *provider.Exchange) provider.Response {
	entry := x.Deps.Scenario.Provider(NameAgentRuns)
	authenticate(x, entry, credentialsFor(x.Route))
	validateAgentRunCreate(x)
	if x.Failed() {
		return rejection(x)
	}

	// MintJob claims the call index AND commits the record — but only if the
	// attempt it claims is one that will actually serve. See "A faulted create
	// must not leave a phantom job" below.
	job, ok := provider.MintJob(x, NameAgentRuns, runIDPrefix, ids.Hex32)
	if !ok {
		return rejection(x)
	}

	body, err := renderRunCreated(x, job.ID)
	if err != nil {
		x.Fail(codeRenderFailed, "", "rendering the Exa agent run failed: %v", err)
		return rejection(x)
	}
	return provider.Response{
		Status:        http.StatusCreated,
		Body:          body,
		Label:         "exa.agent_runs.created",
		FaultEligible: true,
		FaultBody:     func(a scenario.FaultAttempt) []byte { return faultBody(job.ID, a) },
	}
}
```

The create response **body** is derived in full: nothing in it can be scripted by a scenario. That is a deliberate
v1 limitation rather than an oversight — a projection body alongside `turns:` is already a load error
(`scenario.provider.body_with_turns`), so there is nowhere honest to put create-side body keys.

**Shipped as: "derived in full" is not "the identifier and the vendor's initial status constant, and nothing
else."** Both vendors' create bodies carry more than that pair. Exa's `renderRunCreated`
(`provider/exa/agentrun_handler.go`) renders `{id, status: "queued", stopReason: null, createdAt}` — `stopReason`
has no `omitempty` because the contract documents it as present-and-null while queued, not absent, and `createdAt`
is `Scenario.BaseTime()`. Tavily's `handleResearchCreate` (`provider/tavily/research.go`) renders
`{request_id, created_at, status: "pending", input, model, response_time: 0}` — `input` is echoed from the request
body and `model` is echoed too, defaulting to `"auto"` when the request omits it. None of these extra fields is
scenario-scriptable — they are still fully derived, from `BaseTime` and the request rather than from `turns:` —
but "identifier plus status and nothing else" undersells what a consumer actually receives.

Note that `create:` itself is no longer hypothetical: [§3.1](#31-the-route-table) makes it a reserved envelope key
carrying the create route's `fault:` plan, because create and poll need distinct plans and a turn can only speak for
polls. That key is the migration path if create-side *body* scripting is ever needed too — it is additive, and it is
not needed for either vendor today.

#### A faulted create must not leave a phantom job

Faults are applied by `Handle` **after** the handler returns (`provider/handle.go`). A create handler that commits
its record unconditionally therefore produces one on requests the client never receives an identifier for: a
scripted `attempts: [{status: 429}, {status: 200}]` on the create route mints a job, returns a `FaultEligible`
response, and `Handle` replaces it with a 429. The record survives; the client has no id for it.

The cost is not cosmetic. Every faulted attempt consumes a slot from `MaxJobs`, so a plan with one retry burns two
slots per usable job, and a bound of 256 becomes an effective 128 — reached sooner than any author computed, with
`job.limit_reached` naming a number that does not match the jobs they can see.

**`MintJob` commits the record only when the attempt it claims will actually serve.** It already claims the attempt,
so the decision is in hand: `Exchange.Fault()` memoises on `x.claimed` (`provider/exchange.go`), so an early call
here and `Handle`'s own call return the same decision.

**The predicate is not `FaultDecision.Faulted()`.** That is the obvious reach and it inverts the bug. `Faulted()` is
`Attempt != nil && (EffectiveKind() != FaultNone || Delay > 0)` (`provider/deps.go`) — true for a pure `delay:`
attempt with no fault kind, whose body **is** written after the sleep. Skipping the record there hands the client a
valid identifier that no record backs, so every subsequent poll returns the vendor's 404: the same failure as the
phantom job, pointing the other way, and harder to spot because the create looked completely successful.

The predicate asks whether this attempt *replaces the body*, and that is not answered by `EffectiveKind() ==
FaultNone` alone:

```go
serves := dec.Attempt == nil || dec.Attempt.EffectiveKind() == scenario.FaultNone
```

**Shipped as: `deliversBody` (`provider/jobs.go`) commits on three kinds, not one — the sketch above was wrong in
exactly the way the banner warns illustrative Go can be wrong.** The question is not "did `EffectiveKind()` land on
`FaultNone`" but "does the client still receive what the handler rendered", and three kinds answer yes:

| attempt | body written | commits? |
|---|---|---|
| none | `resp.Body` | yes |
| `delay: 5s`, no kind (`FaultNone`) | `resp.Body`, after the sleep | yes |
| `{status: 200}` | `resp.Body` — `EffectiveKind()` only promotes at ≥400 | yes |
| `extra_fields` only (`FaultExtraFields`) | merged `resp.Body` | **yes** |
| `wrong_content_type` (`FaultWrongContentType`) | `resp.Body`, under a wrong `Content-Type` header | **yes** |
| `{status: 429}`, truncate, empty body, close | error or partial | no |

The set is read directly off `execute`'s switch (`provider/fault_exec.go`): those three kinds are handled
explicitly and fall through to writing `resp.Body`; everything else replaces or withholds it. An earlier version of
this table's `extra_fields` row said "yes" while the predicate above it said `FaultNone` only — internally
inconsistent, since `extra_fields` is never `FaultNone`. **Fixed in place, in this doc-truth pass** (2026-08-15).

Claiming the attempt inside the handler is established practice, not a new risk: `handleSearch` already claims in
`selectProjection` before its own `codeRenderFailed` path (`provider/exa/handler.go`).

One honest limit: a client whose deadline fires *during* a scripted delay receives nothing, yet the record is
committed, because the fault decision said this attempt would serve and that was true when it was made. That is a
job record for a response the client never read — not a phantom job in the sense above, and not fixable by any
predicate evaluated before the write, since it depends on the client's own timeout.

The identifier is still derived from the claimed index, so this does not renumber anything: the retry that succeeds
mints from index 1 and gets the identifier index 1 implies. Two consequences worth stating, because both are
deliberate:

- Identifiers are **not** dense across a faulted create. A plan that faults the first attempt produces a first live
  job whose identifier derives from index 1. That is correct — the identifier tuple includes the attempt, exactly as
  it already does for every other faulted route — and it is why goldens ignore derived identifiers ([§5.2](#52-replay)).
- The record is committed before the response is written but after the fault decision is known, so no record is
  created for a response the *scenario* replaces. The client-deadline case above is the one exception, and it is
  stated rather than claimed away.

### 4.4 A poll handler, end to end

```go
// handleAgentRunPoll serves GET /agent/runs/{id}.
func handleAgentRunPoll(x *provider.Exchange) provider.Response {
	entry := x.Deps.Scenario.Provider(NameAgentRuns)
	authenticate(x, entry, credentialsFor(x.Route))
	if x.Failed() {
		return rejection(x)
	}

	// Resolution precedes everything, and precedes any claim. An unknown
	// identifier is answered with the vendor's own 404 envelope and leaves no
	// cursor, no lane and no map key behind it.
	job, ok := provider.ResolveJob(x, x.Request.PathValue("id"))
	if !ok {
		return notFoundRun(x)
	}

	// The lane was resolved once by Handle and already carries path:id=<job>, so
	// this call index is the poll number OF THIS JOB. SelectTurnFor draws it from
	// the one counter fault selection also draws on.
	turn, index := provider.SelectTurnFor(x, entry)
	if turn == nil {
		return rejection(x)
	}

	p := &RunProjection{}
	if err := turn.DecodeProjection(NameAgentRuns, index, p); err != nil {
		x.Fail(codeProjectionInvalid, "", "the scenario's agent-run projection could not be decoded: %v", err)
		return rejection(x)
	}
	for _, f := range x.Deps.Scenario.ResolveRefs(respondPath(entry, index), p) {
		x.Warn(codeSourceUnresolved, "", "%s: %s", f.Path, f.Message)
	}

	body, err := renderRunSnapshot(x, p, job)
	if err != nil {
		x.Fail(codeRenderFailed, "", "rendering the Exa agent run failed: %v", err)
		return rejection(x)
	}
	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         "exa.agent_runs.poll." + string(p.EffectiveStatus()),
		FaultEligible: true,
		FaultBody:     func(a scenario.FaultAttempt) []byte { return faultBody(job.ID, a) },
	}
}
```

The projection, which is the decoded form of one turn's `respond:`:

```go
// RunProjection is one POLL SNAPSHOT — the decoded form of an async entry
// turn's `respond:` body. Turn N answers poll N of a job, which is why status
// is the only meaningful key and why there is no create-side field here.
type RunProjection struct {
	// Status defaults to StatusRunning, so `respond: {}` is a pending poll and
	// the shortest possible stuck-pending scenario is one empty turn.
	Status RunStatus `yaml:"status,omitempty"`

	// Output is the terminal payload. Rendering it on a non-terminal status is a
	// load-time warning: a real run has no output until it finishes.
	Output *RunOutput `yaml:"output,omitempty"`

	// Error renders the failure envelope. Required when Status is failed.
	Error *RunError `yaml:"error,omitempty"`

	// CostDollars is on EVERY Exa response, terminal or not
	// (REQ-PRICING-EXA-COST-CAPTURE-001). Absent renders the flat-rate default
	// rather than omitting the key.
	CostDollars *CostProjection `yaml:"cost_dollars,omitempty"`

	OmitFields  []string             `yaml:"omit_fields,omitempty"`
	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// RunStatus is the wire status. The zero value renders as StatusRunning.
type RunStatus string

// Run statuses. Terminal reports which of these end the run.
const (
	StatusRunning   RunStatus = "running"
	StatusCompleted RunStatus = "completed"
	StatusFailed    RunStatus = "failed"
	StatusCanceled  RunStatus = "canceled"
)
```

**Shipped as: the poll wire is richer than this sketch, in ways worth naming explicitly.** `renderRunSnapshot`
(`provider/exa/agentrun_handler.go`):

- The status constant is spelled `cancelled` (two Ls), matching `contracts/exa/README.md` — the sketch's
  `"canceled"` would fail `exa.agent_run.status.unknown` if copied into a fixture.
- `costDollars` is emitted only when the snapshot `IsTerminal()`, not on every response as the comment above
  claims; a non-terminal run has spent nothing yet and carries none.
- `stopReason` is derived, not scripted directly: `null` while queued or running, and at terminal it defaults to
  `schema_satisfied`/`error`/`cancelled` from the status unless the projection's own `stop_reason:` overrides it.
- `usage: {agentComputeUnits, dataSources}` and `createdAt` (`Scenario.BaseTime()`) are rendered on every poll,
  terminal or not — neither key exists on the sketch above.
- There is no `omit_fields` key on the shipped `AgentRunProjection`; `OmitFields` above was never built.

### 4.5 Import edges

Additive; no edge reverses and the acyclicity proof's labelling is unchanged.

| Level | Package | Change |
|---:|---|---|
| 0 | `internal/jobs` | **new**; imports nothing in-module |
| 3 | `provider` | `+ internal/jobs` (level 3 → 0, legal) |
| 5 | `provider/exa`, `provider/tavily` | consume `MintJob`/`ResolveJob`'s returned `jobs.Job` values |
| 5 | `internal/admin` | `+ internal/jobs` for the scoped reset and the read-only listing |
| 6 | `internal/server` | `+ internal/jobs` to construct the registry from `--max-jobs` |
| 7 | `testkit` | `+ internal/jobs`; adds `type Job = jobs.Job`, `type Jobs = jobs.Store` and `type JobStats = jobs.Stats` to the alias set |

**Shipped as: `provider/exa` and `provider/tavily` do not import `internal/jobs` in non-test code.** Both handlers
read fields off the `jobs.Job` value `MintJob` and `ResolveJob` return without ever naming the `jobs.Job` type
themselves, so there is no import edge to add at level 5 for them — the row above described an edge that was never
needed. `internal/admin`, `internal/server` and `testkit` do add the edge, as the table says.

The alias set is closed under "types a consumer has to name", and the compile-time guard lives in `examples`
(package `examples`, `examples/adapter.go` and `examples/async_test.go` — there is no `examples/adapter`
directory): it builds a `provider.Deps{Jobs: testkit.NewJobs()}` reading `job.ID` through the aliases, so the gap
does not stay invisible until an adopter hits it.

---

## 5. Determinism

### 5.1 Identifier derivation

```go
// mintID derives a job identifier from stable fixture keys only.
//
//	id = prefix + encode(SeedKey, entry, lane.Key, "job", strconv.Itoa(createIndex))
//
// Four choices, each load-bearing:
//
//   - lane.Key, NOT lane.CursorKey(). Key is the cursor key without its
//     namespace prefix, so the identifier is namespace-INDEPENDENT: the same
//     scenario replayed in namespace "t-42" and unnamespaced mints the same
//     identifier at the same call position, and a golden file is portable
//     between them. That is the established rule for every other derived
//     identifier here, and the price is that two concurrent tests mint the same
//     identifier — which is exactly why the registry is keyed on
//     (namespace, id) and never on id alone (§4.2).
//
//   - lane.Key rather than Route.FaultKey, so a create route with a turn_key
//     discriminator ("one lane per model") mints distinct identifiers for two
//     creates that each claim index 0 in their own lane. Route.FaultKey alone
//     would collide them inside one namespace. This needs the entry-resolution
//     fix below to be true at all.
//
//   - createIndex, so every create mints a distinct identifier, which is what a
//     real vendor does and what lets a test tell two runs apart in the journal.
//
//   - The literal "job" domain-separates from the requestId derivation, whose
//     leading parts are the same. ids.Derive length-prefixes each part so the
//     two tuples cannot collide by construction, but relying on arity for
//     domain separation is the kind of subtlety that survives review and then
//     breaks when a part is added.
func mintID(x *Exchange, entry, prefix string, encode func(...string) string) string
```

Exa mints `run_` + `ids.Hex32(...)` — 32 lowercase hex, matching the house shape its `requestId` already uses.
Tavily mints a bare `ids.UUIDv5(...)`, matching its documented `request_id`.

#### `turn_key` must resolve against the route's entry, not the listener's

**Shipped — this subsection describes a prerequisite that was met, not an open gap.** At design time, the bullet
above assumed a `turn_key` written on an async entry takes effect, and it did not yet: `entryTurnKey` read
`x.Entry()`, which resolved by *listener* name — `Deps.Scenario.Provider(string(x.Provider))`, i.e. `"exa"` — and an
`exa_agent_runs` entry, being a second entry on the `exa` listener, never had its `turn_key:` read. `Exchange.Entry()`
(`provider/exchange.go`) now honours `Route.Entry`, and `entryTurnKey` (`provider/lane.go`) resolves against it, so
the async entries' own `turn_key:` is read. The reasoning below is kept because it is still the record of why the
fix had to be the accessor and not one caller. Two failure modes followed, and both were silent:

1. A `turn_key` on the async entry is **ignored**. The author writes "one lane per model", gets one lane for
   everything, and the only symptom is two jobs sharing a cursor.
2. Moving it to the `exa` entry to make it take effect makes it apply to the poll route too — and a `body_json:`
   extractor cannot resolve against a `GET` with no body, so **every poll** raises
   `scenario.turn_key_unresolved`. The fix for one problem manufactures the other.

**This was not an async-only bug.** `perplexity_agent`, a second entry on the perplexity listener, had its
`turn_key:` ignored the same way on already-shipped code. The async surfaces made it load-bearing rather than
latent, which is why fixing it landed as part of this design rather than as a separate follow-on — see
"A behaviour change for `perplexity_agent`" below for what shipping the fix cost that surface.

The fix keeps the single-resolution rule intact:

```go
// Entry names the scenario provider entry this route serves. Empty means the
// listener's own name, which is what every route registered before a listener
// carried two entries relies on.
//
// It exists so lane resolution can read the RIGHT entry's turn_key without
// waiting for a handler to pick one. The route is known to Handle before the
// handler runs, so this is still resolved once, in one place — it is not the
// "derive it twice" shape the single-resolution rule forbids.
Entry string
```

**`Exchange.Entry()` itself honours it** — not `entryTurnKey` alone:

```go
// Entry returns the scenario provider entry this request is served from:
// Route.Entry when the route names one, and the listener's own provider name
// otherwise.
func (x *Exchange) Entry() *scenario.ProviderEntry {
	if x.Route.Entry != "" {
		return x.Deps.Scenario.Provider(x.Route.Entry)
	}
	return x.Deps.Scenario.Provider(string(x.Provider))
}
```

Fixing the accessor rather than one caller is strictly cheaper and strictly safer. At design time `Entry()` had five
callers: `Exchange.policy()`, `entryTurnKey`, two calls in `provider/exa/handler.go`, and one in
`provider/tavily/handler.go`. Patching only `entryTurnKey` would have left **three** ways to resolve an entry and
fixed one of them.

Shipped as: fixing the accessor paid off exactly as argued. The async handlers this design adds — three call sites
in `provider/exa/agentrun_handler.go`, three more in `provider/tavily/research.go` — call `x.Entry()` too
([§2](#2-the-scenario-yaml)'s "resolved by the handler through `x.Entry()`"), for free, because they came after the
fix rather than needing their own. `Exchange.Entry()` has eleven non-test callers today across `provider/lane.go`,
`provider/exchange.go`, both `exa` handler files, both `tavily` files — every one of them reading the route's
entry through the one accessor this design changed, not through six new copies of the listener-name fallback.

#### A behaviour change for `perplexity_agent`, stated rather than implied

**Shipped.** `provider/perplexity/handler.go`'s three Agent routes now declare `Entry: NameAgent`, so this
subsection describes a change that has already happened, not one this design is about to cause. It is kept, not
collapsed to a one-line note, because it is still the record of *why* the change was correct to make and what it
cost — a reader auditing a lane-key or call-index difference in a `perplexity_agent` fixture written before this
shipped needs exactly this reasoning, not just the fact that something moved.

An earlier draft claimed "behaviour for shipped code is identical" and then, in the next sentence, applied
`Route.Entry` to `perplexity_agent`. Both cannot be true. **`perplexity_agent` was shipped** before `Route.Entry`
existed, its three routes sat on the `perplexity` listener, and they resolved their entry by listener name — a rule
`provider/lane.go`'s `turnLaneKey` and `entryTurnKey` used to document inline before the fix landed and their
comments were rewritten to describe the new behaviour instead (the pre-fix wording no longer exists verbatim
anywhere in the tree, which is exactly what a shipped fix should do to a comment describing the bug it closed):

> The entry is the one named for this listener's provider. A listener serving a second scenario entry — Perplexity's
> Agent surface is one — keys its lanes on the primary entry's `turn_key`.

So a `turn_key:` written on the `perplexity` block used to subdivide the **agent** lane too. Giving the agent
routes `Route.Entry = NameAgent` stopped that. For a scenario that declares `turn_key` on `perplexity` *and*
exercises the Agent surface, the lane key changed, and with it the call indices, the turn selected for a given
call, every derived identifier, and `outcome.fault_key`. `Exchange.policy()` moved in the same step: the agent
surface stopped inheriting `perplexity`'s `validation:` block and reads its own now.

**Shipped anyway, owned.** The old behaviour was not a feature — it was an entry's own `turn_key:` being silently
ignored, which is the silent-wrong-behaviour class this repository exists to eliminate. A consumer relying on it
was relying on a bug, and §9's compatibility test does not claim what it cannot:

- [§9](#9-schema-versioning-additive-to-version-1)'s "every existing lane key is unchanged" line names this
  exception explicitly, in its own table's last row.
- A regression fixture pins the **new** agent lane key.
- The narrowness of the affected combination — `turn_key` on `perplexity`, plus use of the Agent surface — was the
  reason it was safe to ship, not a reason to leave undocumented, and it is documented here and in §9.

Every other existing route leaves `Route.Entry` empty and takes the branch it takes today.

This also fixes the `Exchange.policy()` asymmetry recorded in
[§12](#12-companion-corrections-this-design-depends-on) rather than leaving it as separate work, and it lets the
async handlers call `x.Entry()` instead of hardcoding `Provider(NameAgentRuns)`.

### 5.2 Replay

Given the same scenario and the same request sequence, everything is reproduced:

| Quantity | Why it repeats |
|---|---|
| the identifier | a pure function of `(seed, entry, lane key, create index)` |
| which turn answers poll N | the lane cursor is a position, not a clock |
| the rendered body | re-rendered from the scenario, never replayed from a stored copy |
| timestamps in the body | `Scenario.BaseTime()`, as everywhere else |

`Job.CreatedAt` is the one real-time field in the record, and it is never rendered — same rule as
`journal.Entry.ArrivedAt`.

**No testkit change is needed here.** An earlier draft asked for `id` and `request_id` to be added to
`AssertGoldenJSON`'s default ignore set; `testkit/golden.go`'s `derivedIDPaths` already carries all three —
`{"requestId", "request_id", "id"}`. The reasoning still holds and is why it already works: the identifier folds in
the call index by design, so a golden that pinned it would fail on the second create.

### 5.3 What is deliberately not deterministic — and what is deliberately not time

**A job advances by poll count, never by elapsed time.** A run completes on its third poll, not after three seconds.
This is the single most likely wrong expectation on this surface and it is stated here so nobody has to discover it:

- A consumer polling with exponential backoff and a consumer polling in a tight loop see the *same* sequence.
- A test cannot make a job finish by sleeping. There is no clock in the state machine at all.
- A consumer that needs elapsed-time behaviour — an activity timeout, a Temporal heartbeat, a rising-latency
  brownout — asks for it with a `delay:` fault on the poll route, which is real time on the server side of the
  socket and is the only honest way to produce it (`DelayMode`, package-design §2.2).

A time-driven state machine was rejected on house rule 2: the same scenario and the same request must produce
byte-identical responses, and "which poll got the terminal payload" would otherwise depend on how fast the test
machine was.

The already-nondeterministic set is unchanged: `RemoteAddr`, journal timestamps, and derived identifiers on a route
with a fault plan.

---

## 6. GET routes on a POST-shaped mux

`provider.NewMux` needs **no change at all**. Two drafts of this section claimed it needed one; A2 established
otherwise by writing the tests, and the record is corrected here rather than left for a third reader to re-derive.

The registration loop already splits a pattern on its method (`provider/mux.go:58`) and accumulates the methods a
path answers (`:60`), so `HEAD /agent/runs/{id}` registers like any other route, gets its own handler, and appears in
the `Allow` header for free. Everything below is about what a provider declares, not about the mux.

What already works, verified ([§0](#0-mechanics-verified-against-the-toolchain-before-writing-this)):

- `strings.Cut(rt.Pattern, " ")` splits `GET /agent/runs/{id}` into `GET` and `/agent/runs/{id}`. Registration is
  unchanged.
- The method-less pattern loop keys on the path, so `/agent/runs/{id}` gets its own 405 registration and
  `POST /agent/runs/run_abc` answers 405 with `Allow: GET` and a provider-shaped body.
- `{id}` is single-segment and does not match empty, so `/agent/runs/` and `/agent/runs/x/events` fall to the
  catch-all's provider-shaped 404. Fail-closed still holds.
- The lane-prefix stripper clones and rewrites the path before the inner mux matches, so `PathValue` works under
  `/x/<scenario>/n/<namespace>/…` with no change.
- `readRequest` already tolerates an absent body — "an absent body is not a finding here" — so a GET needs no
  special case.

The one change: **register `HEAD` explicitly, bound to a handler that resolves the job without claiming an
attempt.**

The problem it solves is real. `HEAD /agent/runs/{id}` reaches the GET handler (Go routes it there from a `GET`
pattern), which runs `SelectTurnFor` end to end. That **claims an attempt and advances the job's poll cursor**, and
then `net/http` discards the body. A client sending one `HEAD` to check whether a run exists silently consumes a
poll: the response its next real `GET` receives is the one the scenario wrote for the poll after that. The author
sees a job reach `completed` a poll early, with nothing in the journal explaining why — because from the journal's
point of view a poll did happen. That is a scripted sequence changing meaning based on a method the scenario never
mentioned.

Two drafts got the *fix* wrong in opposite directions. The first advertised `HEAD` in `Allow` and left it reaching
the GET handler — the bug above, announced. The second registered `HEAD` to a 405, arguing that serving it properly
"would need a non-claiming turn resolution that exists nowhere else". **That reason was false**:
[§4.2](#42-provider-additions) specifies `ResolveJob`, which claims no attempt, and existence is all a `HEAD` needs.
Refusing it bought a real fidelity divergence — RFC 9110 makes `HEAD` mandatory wherever `GET` is served — for
nothing.

A provider declares it in `Routes()` beside the others, which is also what registers its fault key — the engine's key
set is built by `faults.New` from the routes it is handed (`internal/faults/engine.go`). **Shipped as: a HEAD never
actually reaches `fault.unknown_key`, for a different reason than an unregistered key would suggest.** A `HEAD`
response never sets `FaultEligible` (`handleAgentRunHead`, `handleResearchHead`), so `Handle` never calls `x.Fault()`
for it at all — the warning this paragraph originally worried about would only fire on a route that claims an
attempt with no matching key. Registering the key is still worth doing, because a HEAD's own budget (below) has to
exist somewhere for the lane-key derivation to name, but the reason is not the warning:

```go
// HEAD is a route of its own so it does NOT fall through to the GET handler,
// which would claim an attempt and advance the job cursor for a request whose
// body net/http discards.
//
// Its own FaultKey: a HEAD must not draw on the poll budget for the same reason
// it must not advance the cursor.
provider.Route{
	Pattern:  "HEAD /agent/runs/{id}",
	FaultKey: "exa:agent_runs.head",
	LaneFrom: []string{provider.LaneFromPath + "id"},
}
```

`headJob` looks the job up with `ResolveJob`, returns `404` when it is absent and `200` with the streaming-free
header set when it is present, and writes no body. It never selects a turn, so it cannot report *status* — which is
correct: a client that wants the status sends `GET`, and a `HEAD` that guessed at one would be inventing a wire
contract no vendor page verifies.

`headRoute` carries its own `FaultKey`, distinct from the poll route's. A `HEAD` must not draw on the poll budget
for the same reason it must not advance the cursor.

Because `HEAD` is genuinely served, `Allow` includes it — matching the rule `internal/admin`'s own `allowHeader`
states for its own GET→HEAD promotion, "HEAD is added wherever GET is answered", rather than diverging from it. The
mechanism differs by package: `internal/admin`'s `allowHeader` promotes HEAD into the header for a route that never
registered one, because ServeMux serves HEAD from a GET pattern regardless; `provider/mux.go`'s registration loop
needs no such promotion here, because [§6](#6-get-routes-on-a-post-shaped-mux) has `HEAD /agent/runs/{id}` register
as an explicit route of its own, so `paths[path]` collects `HEAD` the same way it collects any other declared
method. Both arrive at the same `Allow` value; an earlier draft's divergence — 405 on `HEAD`, and no promotion of
any kind — disappears along with the 405.

The signature is `methodNotAllowed(spec MuxSpec, allow []string) Handler` (`provider/mux.go`), and every route
handler in `NewMux`'s registration loop is wrapped in `Handle(d, p, route, h)` — there is no bare `mux` variable and
no `http.HandlerFunc` at that layer. An earlier draft's snippet matched neither, which would have sent an
implementer looking for a seam that does not exist.

No vendor page verifies `HEAD` for either surface, so nothing here is confirmed against live documentation in either
direction — `contracts/exa/README.md` marks its `HEAD /agent/runs/{id}` row "canonical, simulated" rather than
"verified" for exactly that reason, and `contracts/README.md` lists both `HEAD` routes as the two with no golden
fixtures, because `HEAD` carries no body to pin. That is why `headJob` answers only the question the *protocol*
defines — existence — and invents nothing beyond it. Authentication runs before the existence check on both
providers' `HEAD` handlers, so a bad credential is a 401, not a 404 — the same order the GET and POST handlers use.

Two hazards to keep in the mux test table rather than in a reviewer's head:

- `http.ServeMux` **panics** on conflicting patterns. `/agent/runs` and `/agent/runs/{id}` do not conflict, and the
  `/` catch-all loses to both on specificity, but the panic is at construction, so a future route that does conflict
  fails at startup rather than at request time. That is the right direction and should be asserted, not merely
  assumed.
- Percent-encoding. `GET /agent/runs/run%2Fabc` yields `PathValue("id") == "run/abc"`. Every route that feeds a path
  value into a lane key must validate it first; see [§7.1](#71-the-identifier-charset-is-load-bearing).

`provider/mux_test.go`'s table grows five rows: `GET /agent/runs/{id}` 200, `POST` 405 with `Allow: GET, HEAD`, the
percent-encoded identifier rejected, `HEAD` 200 on a live job and 404 on an unknown one, and — the one that actually
guards the bug — **a `HEAD` followed by a `GET`, asserting the `GET` receives poll 0**. A test that only checked
`HEAD`'s status code would still pass if the dedicated handler were later removed and the cursor advance came back,
because `HEAD` reaching the GET handler also returns 200.

---

## 7. Isolation and bounds

### 7.1 The identifier charset is load-bearing

An identifier arrives from a client-controlled path segment, is percent-decoded by `PathValue`, becomes a component
of a lane key, and that lane key becomes a **permanent** entry in the fault engine's `lanes` map, which never evicts
by design. Three consequences follow and all three are enforced at the same point:

1. `ValidJobID` rejects `/` and `|`, so the composed lane key cannot be re-split into a different
   `(namespace, key)` pair by `SplitCursorKey`.
2. `ValidJobID` rejects everything outside `[A-Za-z0-9_-]` and bounds the length, closing log injection and
   unbounded map-key cardinality at the same point `ValidNamespace` already closes them.
3. A poll whose identifier fails validation, or resolves to no job, **claims no attempt**. No claim means no counter,
   which means no lane, which means no map entry. Lane growth on the poll routes is therefore bounded by the number
   of jobs that actually exist, which is bounded per namespace by `--max-jobs`, which is bounded overall by
   `--max-namespaces`. That chain is the entire growth argument and each link is enforced rather than assumed.

A scenario-supplied identifier override, if one is ever added, is validated by the same predicate at load time.

### 7.2 Cross-test isolation

| Boundary | Mechanism |
|---|---|
| namespace A's job invisible to namespace B | registry keyed `(namespace, id)`; a pasted identifier gets the vendor's 404 |
| job X's polls independent of job Y's | lane key carries `path:id=`, so separate cursors and separate fault budgets |
| create budget independent of poll budget | separate `Route.FaultKey` per route |
| one test's reset does not touch another's | `ResetIn(namespace)` on the registry, alongside the journal's and the engine's |

### 7.3 Reset must drop cursors and jobs together

This is the one new invariant, and it is not optional:

> `POST /__admin/reset?namespace=X` must drop **X's job records and X's lane cursors in the same call.**

If cursors dropped and jobs survived, the next create would claim index 0 again, re-mint the identifier it minted
before the reset, and collide with a live record — `ErrDuplicate`, surfaced as a `job.id_collision` finding and a
provider-shaped 500 on a request the author expected to succeed. If jobs dropped and cursors survived, every
identifier in the namespace would 404 while the create kept advancing.

#### Ordering cannot close the window, and does not have to

An earlier draft reordered the drops to jobs → faults → journal and claimed that closed the window "by
construction". It does not. **The window is symmetric.** A create C is three steps — claim index *i*, mint id(*i*),
commit the record — and a reset is three non-atomic drops. Under jobs-first:

> C claims *i*=0 → reset drops jobs (C has not committed yet) → reset drops faults, cursor→0 → C commits id(0) →
> the next create claims 0 and mints id(0) → **`ErrDuplicate`, `job.id_collision`, 500.**

Identical failure, reached by straddling the other pair of steps. Worse, if the cursor was at *k*>0 the collision
surfaces *k* creates later, with no reset anywhere near the failing request — a harder diagnosis than the order it
replaced. Reordering only changes which index collides.

Closing it for real needs a reset epoch folded into the derivation tuple, or jobs and faults dropped under one lock.
**Neither is worth buying**, because the window only exists when a reset races live traffic in the same namespace,
and that is already outside this simulator's contract:

> `POST /__admin/reset` is a local-development convenience, not a concurrency mechanism. Parallel test suites start
> separate processes or containers.

That is house rule 6, and it predates this design. An epoch counter would add per-namespace state that the reset
itself must not reset — a recursive wart — and a shared lock would put a mutex on the create path to protect a
dev-only operation while coupling two deliberately independent stores. Both spend real complexity defending a
configuration the repository already tells people not to use.

So the order stays as `Deps.resetNamespace` already has it, and for the reason recorded there rather than a new
one:

> Both capability checks run **before anything is dropped**, so a surface that can scope only some of the stores
> scopes none of them.

Jobs-first is mutually exclusive with that invariant: dropping jobs before checking whether the journal can scope
would leave job state destroyed by a request that then returns 501 having "changed nothing".

Shipped as: `admin.Deps` gains a `Jobs` field, but **not** a third asserted capability. `jobs.Store` declares
`ResetIn` unconditionally as part of its interface ([§4.1](#41-internaljobs)) rather than as an optional capability
a caller type-asserts for the way `namespacedFaults` and `journal.Namespaced` are — so `resetNamespace` still runs
exactly two capability checks (`Faults`, then `journal.ResetIn`) before it drops anything, and `d.Jobs.ResetIn`
runs unconditionally once both pass. An earlier draft of this paragraph said "a third capability check in the same
shape"; that was never built, because `jobs.Store` has no optional-capability variant to check for — every
implementation of the seam can scope a reset by construction, which is a stronger guarantee than the other two
stores offer, not a matching one.

**Make the violation self-explaining instead.** A documented precondition nobody rereads is worth little, so
`job.id_collision` names the cause the operator can act on. The design-time draft of that message pointed at the
concurrent reset race this subsection is titled after:

```text
job.id_collision: this identifier is already live in namespace "t-42".
The usual cause is POST /__admin/reset running while requests were still in
flight in this namespace — reset is a local-development convenience, not a
concurrency mechanism. Use one process or one namespace per parallel test.
```

**Shipped as: a different message, pointing at the sequential cause instead.** `CodeJobIDCollision`'s shipped text
(`provider/jobs.go`) reads: "job %q is already live in namespace %q; the usual cause is a reset that dropped the
fault cursors without dropping the job records, so this create re-minted an identifier it had already used." That
names the wiring bug the very next subsection exists to close — a `Sim.Reset()` or admin reset that drops cursors
without dropping jobs — not the concurrent race the draft above was written against. Both are real causes of
`ErrDuplicate`, but the shipped message leads with the one a single-process test suite actually hits; the race hint
("reset is a local-development convenience, not a concurrency mechanism") lives only in this document and in
`testkit.Sim.Reset`'s own godoc, not in the finding text.

That converts the one reachable symptom into its own diagnosis at the moment it fires, which is the same job
[§8](#8-multi-replica-the-consequence-stated-explicitly)'s `job.foreign_id` does for the multi-replica case. No race
test is required for a race that is out of contract; the sequential reset test asserts the slots come back.

#### `testkit.Sim.Reset()` must drop jobs too

The argument above covers the *concurrent* window. There is a **sequential** path to the same collision that is
squarely in contract, and it is the one most consumers will actually take.

The pre-fix `testkit/server.go`:

```go
func (s *Sim) Reset() {
	s.journal.Reset()
	s.faults.Reset()
}
```

Its own godoc calls it "a convenience for a test that reuses one `Sim` across phases" — one process, sequential, no
race anywhere. Resetting the fault counters rewinds the turn cursor. If jobs are not reset with them, the next create
claims index 0, re-mints the identifier it minted before the reset, and collides with a record that is still live.

That is §7.3's own opening failure mode, reached **deterministically, on the documented happy path**. "Reset is not a
concurrency mechanism" is a true statement that says nothing about it, because nothing here is concurrent.

**Shipped as:** `Sim.Reset()` (`testkit/server.go`) resets all three stores — `s.journal.Reset()`, `s.faults.Reset()`,
`s.jobs.Reset()` — and its godoc's existing warning about racing an in-flight request stays as it is. This landed in
[§11](#11-implementation-fan-out)'s **A6** alongside the other `testkit` work — an earlier draft listed only the
aliases, `Sim.Jobs()` and the poll-sequence assertion, and would have shipped a testkit whose `Reset` left the
process in exactly the state this section forbids.

The general rule, worth stating once: **every surface that resets fault counters must reset jobs in the same call.**
There are two such surfaces — the admin endpoint and `Sim.Reset()` — and a third would inherit the same obligation.

`Registry.ResetIn` returns the namespace's slots to the `MaxJobs` budget, for the same reason
`faults.Engine.ResetIn` and `journal.Ring.ResetIn` return theirs: a suite that runs more tests than the bound
depends on teardown handing slots back.

### 7.4 Optional: a read-only admin listing

`GET /__admin/jobs?namespace=<name>` returning `[{id, namespace, entry, create_index, turn_index, created_at}]` is
worth adding. It mutates nothing, so house rule 6 is untouched, and it is the fastest way to answer the two questions
this surface generates: "did the create I think I made actually happen here?" and "does *this replica* hold the job I
am polling?" — which is the multi-replica diagnostic below.

**Shipped as a wrapper object, not a bare array, and with more machinery than the sketch above implies.**
`internal/admin/jobs.go`'s `JobsResponse` is `{jobs: [...], bound: <int>}`: each entry in `jobs` is
`{id, namespace, entry, create_index, created_at}` — `turn_index` is absent, for the same reason given below — and
`bound` is the process's per-namespace `--max-jobs` limit, alongside the list rather than folded into it, because
it is a process-wide value and not a per-record one. Three more things the array-only sketch left out:

- The listing is in a **declared total order** — namespace ascending, then entry, then create index, then id — and
  that order is pinned by a test, not left to map iteration (CLAUDE.md house rule 2).
- `?namespace=` absent or blank returns **every** namespace's jobs, not `"default"`'s; a job created with no
  `/n/` prefix is still reachable with `?namespace=default`.
- The wired job store is asserted for an optional `List() []jobs.Job` capability (`jobLister`), not added to
  `jobs.Store` itself — `jobs.Store` is exported and consumers implement it (house rule 7), so adding a method
  there would break every implementation outside this repository. A store that does not implement it answers
  `501` with a message naming the reason, rather than an empty list that would read as "no jobs" instead of
  "cannot answer".

`turn_index` stays absent for the reason already given: the poll cursor lives in the fault engine's attempt
counter, and that seam offers no non-claiming read (§8, "localCursor is not readable"), so there is no value to put
behind the key without the listing itself claiming an attempt. `lane_key` is also absent, on house-rule-4 grounds:
a lane key embeds a route's `turn_key` or `Route.LaneFrom` extractor values verbatim, which can include a
credential a scenario author wrote into a `header:` or `body_json:` extractor — the `779d23c` fingerprinting fix
([§3.2](#32-routelanefrom)) closes the leak in the retained lane key itself, but that does not make the field one
worth taking on as a wire compatibility obligation for nothing a consumer currently needs.

---

## 8. Multi-replica: the consequence, stated explicitly

**Job state is per-process, exactly like `journal.Ring.lanes` and `faults.Engine.counters`/`lanes`. Nothing about
this design changes that, and this surface makes the existing hazard sharper.**

At two replicas behind a Service with no sticky routing:

| State | Behaviour at N ≥ 2 replicas |
|---|---|
| `call_index` sequencing (existing) | diverges silently; each replica counts only the calls it saw |
| journal entries (existing) | split across replicas; `/__admin/requests` on either shows half |
| **job identifiers (new)** | a poll has a `1 − 1/N` chance of landing on a replica that never saw the create, and receives the vendor's **404 on a job that exists** |

The new failure is intermittent and, at face value, indistinguishable from the adopter's own client bug — a poll loop
that works, then 404s, then works. That is precisely the "silent divergence" the adopter refuses to accept.

**The supported configuration is one replica.** Concretely, the deliverable is:

1. `replicas: 1` in the published K8s manifest, with a comment naming this document, not a bare number.
2. A startup log line at info level, emitted unconditionally, naming the constraint:
   `servicesim.single_replica_required — namespace lane state, turn cursors and async job records are per-process;
   run one replica, or route stickily on the /n/<namespace> path prefix.`
3. A `README` section and a line in `docs/troubleshooting.md` keyed on the symptom ("polls 404 intermittently"),
   because that is the string somebody will search for.
4. Sticky routing keyed on the `/n/<namespace>` path prefix is the *only* supported multi-replica arrangement, and it
   is supported only because it makes every request of one test land on one replica. It is not a fix; it is the same
   exemption with a load balancer holding it in place.

**Shipped, 2026-08-15: items 2 and 3.** `internal/server` emits `servicesim.single_replica_required` unconditionally
at info level before `server.ready`, with the exact hint text above (`internal/server/server.go`). `README.md`'s
"Single replica by design" section and `docs/troubleshooting.md`'s replica-symptom table both carry the job-poll
row — "`POST /agent/runs` then `GET /agent/runs/{id}` ... 'polls 404 intermittently'" — alongside the four
pre-existing per-process hazards (`call_index` sequencing, journal entries, retry budgets, scoped reset). **Item 1
does not apply to this repository**: Servicesim ships an image, not a Kubernetes manifest, so `replicas: 1` has no
file to land in here — the deliverable is the README/troubleshooting language a consuming repository's own manifest
review can point at, which is what shipped. Item 4 is documented in prose (README's "Single replica by design",
the startup log's own hint text) rather than as a separate how-to; no consuming repository has asked for one yet.

**Make the divergence self-diagnosing.** An earlier draft proposed recomputing the derivation for create indices
`0 … min(localCursor + window, MaxJobs)` in the presented lane. **That cannot be built**, for two independent
reasons, and both are worth recording so it is not proposed again:

- **`localCursor` is not readable.** The `Faults` seam offers exactly `Next(key) FaultDecision` and `Reset()`
  (`provider/deps.go`'s `Faults` interface). `Next` *claims*. There is no non-claiming read, and adding one so that
  an error path could peek would mean a 404 diagnostic advancing the very cursor whose sequencing is under test —
  the diagnostic would corrupt the thing it exists to explain.
- **"the presented lane" is not obtainable from a poll.** A poll's lane is keyed on `path:id`, so reconstructing
  what a *create* would have minted needs the **create** lane key — which folds in any `turn_key` discriminators
  drawn from the create request's body. A poll has no body and never saw that request.

The replacement needs neither, because the question does not actually require a derivation. "Would this scenario
mint this id?" is a **shape** question, and `ValidJobID` already answers it:

```go
// diagnoseForeignID reports whether id is SHAPED like one this provider mints
// but is held by no record in this namespace.
//
// It changes nothing about the RESPONSE, which is still the vendor's 404. It
// adds a named finding and a log line, so an intermittent 404 carries a hint
// instead of looking like a consumer bug.
//
// It needs no cursor and no create lane, which is why it is buildable at all:
// a shape check plus "this namespace has minted something" is enough to
// separate "that is not one of our identifiers" from "that could be one of
// ours". The cost is one charset scan, not a bounded number of SHA-256s.
//
// It CANNOT tell which of three things happened, and must not pretend to:
// another replica minted it, a reset dropped it, or the client sent an
// identifier this process never minted at all — a stale one carried between
// tests, or one hand-written into a fixture. In a test suite the third is the
// likeliest, so the finding names all three and the log line is a WARNING.
func diagnoseForeignID(x *Exchange, id string) bool {
	stats, ok := x.Deps.Jobs.StatsIn(x.Lane().Namespace)
	return ValidJobID(id) && ok && stats.Count > 0
}
```

**It uses the existing `StatsIn`, not a new `CountIn`.** An earlier draft called `CountIn` on `Deps.Jobs`, which is
typed `jobs.Store` ([§4.2](#42-provider-additions)) and declares `Create`, `Lookup`, `ResetIn`, `Reset` and
`StatsIn` — no such method, so it did not compile against its own seam. `StatsIn` already reports one namespace's
job count and bound, which is exactly what this needs; a second accessor for the same number would be one more thing
to keep in agreement. (`Stats` was used as a return type in §4.1 and not defined there at the time this note was
written; it is now — see §4.1's Shipped-as note.)

**`ValidJobID` is a charset check, not a scheme check**, and the wording above is careful about that.
[§7.1](#71-the-identifier-charset-is-load-bearing) defines it as `[A-Za-z0-9_-]{1,64}`, so `GET /agent/runs/typo`
in a namespace holding one job satisfies it. Tightening it to the real schemes — Exa's `run_` + 32 hex, Tavily's
UUID — would sharpen the diagnostic considerably, and is worth doing if it is cheap; but the finding must be honest
at whatever precision it has, because an error-level line blaming replica count for a typo'd fixture id sends a
reader to their deployment when the problem is in their test.

For that reason the log line is `WARN`, not `ERROR`. The multi-replica case it was written for is real, but it is
not the most common way to reach this code path in a suite that runs one process — which is every supported
configuration.

A hit raises `job.foreign_id` on the journal entry and logs `servicesim.job_foreign` at **WARN** — matching the
paragraph above; an earlier draft of this sentence said "error level" here, which contradicted it, and was a
drafting slip rather than a second decision. It is a diagnostic, not a correctness mechanism — the response is
unchanged — and it is what converts a silent intermittent 404 into a message naming the replica count. **Shared
job state is explicitly out of scope**: it means a network dependency in a simulator whose value proposition is
being fast, offline and hermetic.

**Shipped, 2026-08-15.** Built as specified, with one difference from the illustrative code above: it lives
directly in `provider.ResolveJob` rather than a separate `diagnoseForeignID` helper, so one seam serves both
providers and both `GET` and `HEAD` rather than each caller wiring its own. `CodeJobForeignID` is the finding
code; `servicesim.job_foreign` is the log event, at WARN.

---

## 9. Schema versioning: additive to version 1

**This is additive to version 1. It does not need version 2.** The justification is a falsifiable test rather than an
assertion:

> Every scenario file that loads and behaves identically on v0.1.0 must load and behave identically on this build.

Check it clause by clause against what this design actually changes:

| Change | Where it lands | Effect on an existing v1 file |
|---|---|---|
| new provider entry `exa_agent_runs`, `tavily_research` | the **open provider registry**, built for exactly this | none; and an *older* binary loading a newer file emits `scenario.provider.unimplemented`, a WARNING by design |
| new `respond:` keys (`status`, `output`, `error`) | the **undecoded `respond:` node**, owned by the provider package | none; `scenario` never sees them |
| "a turn is a poll" | a reinterpretation of `call_index` **on new routes only** | none; no existing route has a per-job lane |
| `Route.LaneFrom` and the `path:` extractor | **Go**, on `provider.Route` | none; the lane key changes only for routes that declare it, and every such route is new |
| `Deps.Jobs`, `Deps.MaxJobs` | **Go**; `MaxJobs` defaults from `Normalized`, `Jobs` does not (§4.2's Shipped-as) | none |
| a `HEAD` route declared per provider | **Go**, in each provider's `Routes()`, not in `NewMux` (§6's Shipped-as) | none; no existing listener serves a GET route |
| `Route.Entry` on `perplexity_agent` | **Go**, on `provider.Route` | **NOT none — see below** |

The third column is what actually matters for regression risk, so state it as a check to run rather than a claim to
believe: **for every row but the last, no existing lane key changes, therefore no existing call index changes,
therefore no existing derived identifier changes, therefore every existing golden still passes.**
`hasDiscriminator` returning `false` for an empty `LaneFrom` is what makes that true, and it is why that one line is
called out explicitly in [§3.2](#32-routelanefrom).

**The last row is a real exception and must not be folded into that sentence.** `perplexity_agent` is shipped, and
giving its routes an `Entry` stops them resolving their entry by listener name. A scenario that declares `turn_key`
on the `perplexity` block **and** exercises the Agent surface currently has that key subdivide the agent lane; after
this change it does not. The lane key, call indices, derived identifiers, `outcome.fault_key` and the effective
`validation:` block all move for that combination.

It ships regardless — the current behaviour is an entry's own `turn_key` being silently ignored, which is a bug, not
a contract — but it ships **named**: a regression fixture pins the new agent lane key, and the release notes call it
a behaviour change rather than a fix. See [§5.1](#51-identifier-derivation) for the reasoning. A compatibility claim
with one unstated exception is worth less than one with a stated exception, because the reader cannot tell which
kind they are holding.

**The one change that would have forced version 2 is deliberately kept out of the schema.** Adding `path:` to the
`turn_key` extractor allow-list is tempting and is refused: `scenario.Validate` rejects an unrecognised extractor as
a **load error**, so a v1 file using `path:id` would be *unloadable* on any already-released binary — a hard failure
in a consumer's CI, not a warning. It also buys a scenario author nothing they need, because the per-job poll lane is
not optional and must not be author-configurable. Putting the discriminator on the route costs one Go field and keeps
the schema clean.

The migration-cost argument from `extended-surfaces.md` now cuts harder than it did: **v0.1.0 is shipped and there is
an adopter with fixtures.** A `version: 1 → 2` bump is nearly free in this repository and is paid for in every
consuming repository, by people who did not choose it. When that argument was first made, N was zero. It is no longer
zero, so "additive" has stopped being a preference and become the default that needs a forcing reason to leave.

What *would* need version 2, recorded so the boundary is usable:

- **Positional turn selection** (`repeat:` on a turn), because `call_index` changes meaning for existing files the
  moment positional and predicate turns coexist.
- **An event-sequence projection for SSE.** `extended-surfaces.md` already says so, and streaming is the next item in
  the adopter's backlog. A turn gaining an `events:` sibling to `respond:` is a schema change; this design
  deliberately does not pre-empt its shape.
- **Any change to what `call_index` counts on an existing route.**

None of those are in this design.

---

## 10. What this does not do

- **No time-driven completion.** A job advances by poll count, never elapsed time — see
  [§5.3](#53-what-is-deliberately-not-deterministic--and-what-is-deliberately-not-time).
- **No create-side scripting.** The create response's fields are all derived — from `Scenario.BaseTime()` and, for
  Tavily, echoed request fields (§4.3's Shipped-as note has the full field set for both vendors) — and none of them
  can be set by a scenario's `turns:`. `create:` as a reserved envelope key is the additive migration path if a
  vendor ever needs a scriptable create body.
- **No list, cancel, events or delete routes.** Exa publishes `GET /agent/runs`, `POST /agent/runs/{id}/cancel`,
  `GET /agent/runs/{id}/events` and `DELETE /agent/runs/{id}`. Each is a bounded addition behind this same model —
  cancel is a route with the same `LaneFrom` and a `cancelled` status (two Ls, matching the shipped enum), events is
  the streaming surface and is deferred with it — and none should be added speculatively. Unimplemented routes fall
  to the catch-all's provider-shaped 404, which is loud enough.
- **No shared job state across replicas** ([§8](#8-multi-replica-the-consequence-stated-explicitly)).
- **No per-turn or per-lane fault plans.** Still deferred, still one plan per route — but the poll route's plan is now
  per job *in effect*, because the lane is.

---

## 11. Implementation fan-out

Every row is done. `Shipped in` names the commit(s) on `main` between `v0.1.1` and `v0.2.0` (`git log --oneline
v0.1.1..v0.2.0`) that landed it; short hashes, not full ones, because they are provenance for a reader with the
repository open, not a pin.

| Unit | Owns | Depends on | Shipped in |
|---|---|---|---|
| **A1** | `internal/jobs`: `Job`, `Store`, `Registry`, `Limits`, bounds, `ResetIn`, race tests | — | `b905bc9` |
| **A2** | `provider`: `Deps.Jobs`, `Deps.MaxJobs`, `Route.Entry` (incl. the `perplexity_agent` break, [§5.1](#51-identifier-derivation)), `Route.LaneFrom`, `LaneFromPath`, `ValidJobID`, `MintJob`, `ResolveJob`, `turnLaneKey` and `hasDiscriminator` changes, the served-`HEAD` route and its own fault key ([§6](#6-get-routes-on-a-post-shaped-mux)), mux table rows | A1 | `46bdfb1`, `6d653b7`, `7e9b14a`, `61521c8` |
| **A3** | `provider/exa`: routes, request validation, `RunProjection`, render, error envelopes, validator findings | A2 | `0e6feaf` |
| **A4** | `provider/tavily`: the same, plus per-route credential placement (see §12) | A2 | `85fceac` |
| **A5** | `internal/admin` scoped reset across three stores, `GET /__admin/jobs`; `internal/server` `--max-jobs` wiring and the single-replica startup log | A1, A2 | `b1fc0e0` |
| **A6** | `testkit`: `Job`/`Jobs`/`JobStats` aliases, `Sim.Jobs()`, `AssertPollSequence`, **`Sim.Reset()` dropping jobs with the cursors** ([§7.3](#73-reset-must-drop-cursors-and-jobs-together)), the `examples` package's alias guard | A1–A5 | `ccd6e17` |
| **A7** | `scenarios/protocol/*.yaml` (async entries on every built-in, plus `async-failed`/`async-stuck`), `docs/scenario-schema.md` async section, README + troubleshooting multi-replica sections | A3, A4 | `fb7a44d`, `85c2b1e` |

A1 and A2 were the critical path; A3 and A4 were independent of each other, and both landed.

**A1–A4 done, 2026-08-15.** `internal/jobs` (A1) shipped first and alone, importing nothing in-module as designed.
A2 shipped as four commits rather than one — entry resolution (`Route.Entry`, fixing `Exchange.Entry()` for every
caller per [§5.1](#51-identifier-derivation)), `Route.LaneFrom` and the `path:` extractor, `MintJob`/`ResolveJob`
wired into `Deps`, and the served-`HEAD` route last — which matches the design's own ordering: the entry fix has to
land before `LaneFrom` can key a real per-job lane, and the job store has to exist before `HEAD` can resolve
against it non-claiming. A3 and A4 each shipped as one commit apiece once A2 was in place.

**A5 done, 2026-08-15.** The known defect this unit exists to fix, scoped reset dropping cursors without dropping
jobs (§7.3), is closed: `admin.Deps.Jobs`, three-store `resetAll`/`resetNamespace`, `GET /__admin/jobs`,
`--max-jobs`/`SERVICESIM_MAX_JOBS`, and the unconditional `servicesim.single_replica_required` startup log all
shipped together. Two more commits landed after A5 and before A6, both completeness-pass items rather than fan-out
rows of their own: `779d23c` fingerprints a credential-named or credential-shaped `turn_key`/`LaneFrom` value
before it joins a lane key ([§3.2](#32-routelanefrom)'s `turnLaneKey`, CLAUDE.md house rule 4), and `21a3191` adds
the `job.foreign_id` diagnostic ([§8](#8-multi-replica-the-consequence-stated-explicitly)) directly to
`provider.ResolveJob` rather than as the separate `diagnoseForeignID` helper this design sketched.

**A6 done, 2026-08-15.** `testkit.Job`, `testkit.Jobs`, `testkit.JobStats` (closing the alias set over `Store`'s
own method set, per §4.5), `testkit.NewJobs` (bounded to `jobs.DefaultMaxJobs`), `Sim.Jobs()`, `Namespace.Jobs()`
(returning the admin's declared total order and never nil) and `testkit.AssertPollSequence` shipped together,
confirming §7.3's `Sim.Reset()` behaviour rather than re-implementing it.

**Added at implementation: `AssertPollSequence`'s signature and refusal condition.**
`func AssertPollSequence(tb testing.TB, entries []Entry, id string, wantStatuses ...int)` (`testkit/assertions.go`)
filters `entries` down to the `GET` requests whose last path segment equals `id`, then requires both
`Outcome.Status == wantStatuses[i]` and `Outcome.AttemptIndex == i` for each one in order. It refuses — `tb.Errorf`,
not a panic — when those polls span more than one namespace: a job identifier is namespace-independent by design
(§5.1), so reading one id across every namespace conflates two different jobs that merely share a name.

**Added at implementation: `--max-jobs` / `SERVICESIM_MAX_JOBS`.** Named in passing three times above without its
own paragraph: `internal/config` wires it with default `jobs.DefaultMaxJobs` (256, agreeing with `internal/jobs`'
own default) and rejects a value below 1 at validation time — a stricter rule than `jobs.Limits`, which treats any
non-positive `MaxJobs` as "use the default" rather than as an error, because a library caller building `Limits{}`
by hand should get the default silently and a CLI flag typed wrong should not.

**A7 done, 2026-08-15,** in two commits: `fb7a44d` shipped `scenarios/protocol/*.yaml` gaining `exa_agent_runs` and
`tavily_research` entries on every built-in, plus two new built-ins (`async-failed`, `async-stuck`);
`docs/scenario-schema.md` gained the async
section this row promised; the README and `docs/troubleshooting.md` multi-replica sections gained the job row
("polls 404 intermittently") the design predicted in [§8](#8-multi-replica-the-consequence-stated-explicitly).
`85c2b1e`, the same day, is item 1 below: it fixes the YAML-alias defect the first commit's fixtures had just
worked around. Four corrections against this document were found during A7; all four are fixed in place, the last
one in this doc-truth pass:

1. [§2.1](#21-two-pending-polls-then-completed)/[§2.5](#25-the-sugar-that-was-rejected) told fixture authors to
   de-duplicate the repeated pending snapshot with a YAML anchor/alias across turns (`respond: &pending` /
   `respond: *pending`). It did not load: `scenario.decodeTurns` re-marshalled each turn independently, so an
   alias whose anchor lived in a different turn was unresolvable. **Fixed in place** at the time, by rewriting
   §2.1/§2.5 to tell fixture authors to write the snapshot out literally instead, with the verified error text.
   **Superseded, same day:** that was documenting a real defect, not a permanent limitation, and the defect is now
   fixed in the loader itself (`scenario/alias.go`) rather than worked around in fixtures — see §2.1, which the
   anchor form has been restored to.
2. [§3.1](#31-the-route-table)'s route table gave Tavily's poll as `GET /research/{id}` with `LaneFrom`
   `["path:id"]`; the shipped route is `GET /research/{request_id}` / `["path:request_id"]`. **Fixed in place.**
3. §2.1's and §2.4's YAML examples showed `output.content`/`output.citations`; the shipped Exa projection is
   `output.text` + `output.grounding[]`, each entry `{field, citations, confidence}`. **Fixed in place, in this
   doc-truth pass** (2026-08-15): both examples now match `AgentRunProjection`, `docs/scenario-schema.md`'s async
   table and `scenarios/protocol/happy.yaml` exactly. A7's own spec had flagged this and left it for a later pass
   rather than fixing it inline; this is that pass.
4. [§2.6](#26-validation-the-provider-package-owns)'s finding table named only five of `AgentRunValidator`'s seven
   codes and carried no `tavily_research` codes at all, under a heading that implied one validator served both
   providers. **Fixed in place, in this doc-truth pass** (2026-08-15): the section now has one table per validator,
   `exa.agent_run.status.unknown` and `exa.agent_run.stop_reason.unknown` are listed, and `ResearchValidator`'s
   three codes are listed alongside a note that it has no equivalent to `script_exhausted` or
   `body_predicate_on_poll`. `docs/scenario-schema.md`'s async section remains the authoritative, kept-current
   list for both.

---

## 12. Companion corrections this design depends on

Four already-verified defects blocked or contradicted this work. **All four have since shipped**; they are kept
here, marked, rather than deleted, because a reader coming to this design cold needs to know the dependency existed
and was met — a silently-removed prerequisite reads as one that was never needed.

1. ~~**`contracts/exa/README.md` is wrong and must be corrected.**~~ **DONE — `f7149af`.** It stated that
   `/agent/runs` is "not simulated: no C360 consumer uses it", and the adopter's client at `src/pkg/agent/exa.go`
   calls it. The false clause was *struck rather than reworded*, on the grounds that whether some consumer calls a
   route is not something a vendor-contract file can verify in either direction. The paragraph that followed it —
   that the create-then-poll lifecycle "needs a different scenario shape than a single request/response
   projection" — is the part that was right, and is what this document answers. (Backlog item #17.) **Shipped as:**
   this correction is the one row [§11](#11-implementation-fan-out)'s fan-out table leaves out — it landed in
   `f7149af` between A4 and A5, not inside any unit's own `Shipped in` cell.
2. ~~**Tavily currently returns 401 for a body-placed key.**~~ **DONE — v0.1.1 and Phase 1.** The body placement now
   authenticates on POST routes (v0.1.1), and `Route.Credentials` landed in Phase 1 with the precedence rule
   `auth.headers` > `Route.Credentials` > the package default, resolved once in `Exchange.AcceptedPlacements`.
   **Correction, 2026-08-15: the reason stated here was wrong.** This item asserted that the vendor requires
   different schemes per route — a body key on the POST, a Bearer header on the GET. Verification against the live
   `/research` documentation shows **`Authorization: Bearer` on both**, and no documented body placement on this
   surface at all.

   `Route.Credentials` is still the right mechanism and the surface is still unblocked, for a better-evidenced
   reason: `contracts/tavily/README.md` records that Tavily's shipped clients send the key as a body `api_key`
   despite the docs saying Bearer only, and v0.1.1 accepts that. A POST has a body to carry one and a GET does not,
   so the two routes accept different placement SETS — the difference comes from the request shape, not from the
   vendor requiring different schemes. Declare it on each route:

   ```go
   // POST /research
   Credentials: []string{provider.PlacementAuthorization, provider.PlacementBodyAPIKey},
   // GET /research/{request_id} — no body to carry a key in
   Credentials: []string{provider.PlacementAuthorization},
   ```

   (Backlog item #16.)
3. ~~**The multi-replica warning is correct and undocumented.**~~ **DONE — v0.2.0.**
   [§8](#8-multi-replica-the-consequence-stated-explicitly) is the documentation, and it shipped as this work's own
   deliverable rather than a follow-on note: the unconditional `servicesim.single_replica_required` startup log,
   `README.md`'s "Single replica by design" section and `docs/troubleshooting.md`'s replica-symptom table all carry
   the async job-poll row §8 predicted. (Backlog item #18.)

4. ~~**Entry resolution by listener name is a live bug, and it is now in scope.**~~ **DONE — v0.2.0.**
   `Exchange.Entry()` used to resolve by *listener*, so a second entry on one listener —
   `perplexity_agent` at design time, `exa_agent_runs` and `tavily_research` since — silently inherited the primary
   entry's `validation:` block through `Exchange.policy()`, and never had its own `turn_key:` read through
   `entryTurnKey`. An earlier draft recorded only the `policy()` half and called it out of scope; both halves had the
   same root cause and are fixed by the same field, `Route.Entry` ([§5.1](#51-identifier-derivation)), which now
   ships on all six async routes — create, poll and `HEAD` on each of `exa_agent_runs` and `tavily_research` — and
   on `perplexity_agent`. `authenticate` took its entry explicitly and was never affected.

   It was in scope because this design is what made it bite: `perplexity_agent` got away with it before only because
   nothing it declared depended on either lookup.
