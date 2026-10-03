# Cancellation lifecycles and the accepted-create fault

## Status

**Proposal, revised 2026-10-01 after an independent design review (PR #9); ready to build once #10 has merged.** Written
against `main` @ `aeb86e1` for [#6](https://github.com/C360Studio/servicesim/issues/6) (cancellation lifecycles) and
[#7](https://github.com/C360Studio/servicesim/issues/7) (an accepted create whose response is lost), and sequenced after
[#8](https://github.com/C360Studio/servicesim/issues/8) (the Agent contract audit), whose corrections are prerequisites.
It adds new exported framework surface — a fault modifier, two scenario blocks, mutable job state and two job-store
operations — so it was read independently before implementation ([ADR 0003](../adr/0003-framework-seam.md): consumers
pay for every exported symbol).

Provenance: an architect pass read the code and both vendor specs; the defects below were re-verified by reading the
code; the design review then re-read the code, reproduced one claim against a built binary, and walked the
interleavings. Nothing here has been compiled or spiked. Where this document and the verified vendor contract disagree,
the contract wins ([ADR 0002](../adr/0002-verified-contract-precedence.md)).

### Review outcome

The question put to the reviewer was whether design **1A** — job state on the existing record, "terminal at cancel time"
judged by the job's *next* poll, a compare-and-set `MarkCancel`, and `cancel:` / `background:` scenario blocks — is the
smallest sound mechanism, and whether its exported names are the ones to lock in pre-1.0. The verdict:

- **1A is the right size.** The poll position and the cancel marker must change under one lock, and a peek added to
  `provider.Faults` would split them across two stores. Walking a poll that has claimed an index but not yet advanced
  racing a cancel (both orders), two racing cancels, out-of-order advances and a faulted poll, every interleaving
  resolves to "cancel before poll" or "poll before cancel", never a mix. One invariant makes that hold and is now part
  of the design: **an Exa cancel response is always exactly the snapshot the next poll would return.**
- **The names read fine** (`accepted`, `cancel:`, `background:`, `Advance`, `MarkCancel`), with three details pinned
  in [Q1](#q1--job-state-for-cancel-recommended-1a).
- **Six things had to change before code, all adopted below:** `accepted` had no home on the Perplexity background
  create; "terminal" must be absorbing in serve order and is not enforced today; `accepted.redundant` needs the delivery
  predicate inside `scenario`; the step list left open when the cancel attempt is claimed; the label over-claims on a
  faulted attempt; and one scope narrowing needed an owner ruling.

### Rulings (owner, 2026-10-01)

1. The vendor specification is the authority and parity with the real service is preferred.
2. The project is greenfield with one user who expects breaking fixes: breaking changes are approved, with no
   compatibility shims.
3. Retrieve and cancel are the **spec-declared routes only** — `GET /v1/agent/{id}`, `POST /v1/agent/{id}/cancel` and
   `POST /agent/runs/{id}/cancel`. No `/v1/responses/{id}` alias is served; the spec's prose mention of it is recorded
   as an unresolved inconsistency in the contract notes.
4. Perplexity `background: true` with no scripted lifecycle **fails closed**: a provider-shaped error plus a named
   finding, never an invented poll script. The same applies to `background: true` with `stream: true`.
5. The two job-lifecycle operations are added to `jobs.Store` itself (a compile-time break for a custom store) rather
   than as an optional capability.
6. **One cost rule for every terminal Exa snapshot.** The spec requires `costDollars`, so an unscripted cost renders
   zero placeholders and the loader raises a warning on every terminal turn that scripts none — `cancelled` included,
   with no special case. A zero the scenario did not script is a placeholder, never a billing fact; strict validation
   promotes the warning to an error.

   **Correction (2026-10-02).** The last clause's premise was false. `validation.strict`, `promote` and `demote`
   apply only to findings raised while serving a request; a finding raised when a scenario loads, as this warning
   is, never sees the policy. The owner's decision: U4 lands `exa.agent_run.cost_unscripted` as a load-time warning
   that strict does not promote, the documentation says so, and the framework gap is tracked in
   [#18](https://github.com/C360Studio/servicesim/issues/18), outside U4.

   **Tightened (2026-10-02).** The owner then tightened the rule to require `total`: a terminal snapshot that scripts
   no `cost_dollars.total` warns, whether it declares no `cost_dollars`, an empty one, or one that scripts only other
   meters, because each of those still renders an unscripted `0` for `total`. An explicit `total: 0` is a statement and
   counts as scripted. The ruling text above is unchanged.

7. **Only `background: true` Perplexity responses are retrievable.** A synchronous response with `store` unset is
   retrievable on the real API; this simulator answers `404` for it. That is a deliberate, named divergence, recorded in
   the contract notes with its reasons (see [Q2](#q2--the-perplexity-lifecycle)).

### U5 note (2026-10-03)

U5, the Perplexity `background: true` create and `GET /v1/agent/{id}` retrieve, is built. The rulings above are
unchanged and bind it. The contract notes record the named divergence of ruling 7 with its reasons, and the poll path
inconsistency of ruling 1 as unresolved (`profiles/perplexity/contracts/README.md`, "Lifecycle: background runs and
retrieve"). Where the build departs from the design below:

- **The opt-in is framework-level.** The owner decided on 2026-10-03 that `background:` mirrors `cancel:`, the
  mechanism of ruling 9 on issue #6: `provider.Profile.Backgroundable`, rejected by default with one code,
  `provider.CodeBackgroundUnsupported`, rather than a block known to `perplexity_agent` alone. The exported surface the
  "Exported surface" table below calls "`background:` block type and field on `perplexity_agent`" is therefore
  `scenario.BackgroundPolicy`, `scenario.ProviderEntry.Background`, `provider.Profile.Backgroundable` and
  `provider.CodeBackgroundUnsupported`, plus in `profiles/perplexity` the finding codes
  `perplexity.agent.background.unscripted`, `.stream`, `.unstored` and `.field`. The code
  `perplexity.agent.background.unsupported` is gone (ruling 2).
- **`background:` carries `turns` only.** Q2 sketches `{turns, cancel}`; a `cancel:` key under `background:` is a load
  error until U6 serves Perplexity's cancel, so a block that could never run does not load.
- **A retrieve turn may carry `fault:`**, unlike a cancel turn: the retrieve route reads its plan from
  `background.turns`. A background turn's `when.route` is checked by the Perplexity validator against the retrieve route
  alone, never by the framework, so a create spelling in a background turn is a load error.
- **`HEAD /v1/agent/{id}` is refused** with a `405`, claiming and resolving nothing: Go's mux would otherwise deliver it
  to the `GET` handler and spend a snapshot, and the specification declares no `HEAD` (ruling 1).
- **Q2's "No finding can fire on that `404`"** holds for a namespace that has minted no job. Once it has, a well-formed
  id that resolves to none, a synchronous response's id included, also raises the warning `job.foreign_id`.
- **The spec was re-read on 2026-10-03** and its hash has moved since the 2026-10-01 audit. The lifecycle operations
  agree with everything the 2026-10-01 audit recorded about them; with the old bytes gone, that is all that can be
  compared. Elsewhere the document grew what looks like one image-search feature: `ResponsesRequest` gained
  `tool_choice`, `ResponsesCost` a `tool_calls_cost_details`, `EventType` and `ResponseStreamEvent` two
  `response.reasoning.image_search_*` members (16 where 14 were recorded), the `OutputItem` discriminator an
  `image_search_results` type (11 where ten were counted), and `sequence_number` occurs 32 times where 28 were counted.
  The lifecycle renders or requires none of them. No whole-bundle re-audit has been done (tracked in #27), so
  `provenance.yaml`'s `spec:` block, the provider-level `verified:` date and the index table's Perplexity date stay at
  2026-10-01 (`docs/audits/2026-10-01-perplexity-agent.md`, "Notes after 2026-10-01").

Not part of U5: Perplexity cancel (U6), and the documentation pass and tag note (U7).

## Facts that force the design

- `internal/jobs.Job` is immutable coordinates — ID, namespace, entry, lane key, create index, creation time. A poll
  re-renders from the scenario by poll *count*, never by elapsed time (`docs/design/async-jobs.md` §5.3).
  `jobs.Store` is Create, Lookup, StatsIn, ResetIn and Reset, with `List` an optional extra. `testkit.Job` and
  `testkit.Jobs` re-export them and document the store as implementable by consumers, so both are exported surface.
- `provider.Faults` is `Next` plus `Reset`: an attempt counter can be read only by *claiming* it. `provider.SelectTurn`
  has no side effects; `SelectTurnFor` claims by `x.CallIndex()`, so a faulted poll consumes a snapshot position today.
- Faults are applied after the handler returns (`provider/fault_exec.go`), so `MintJob` decides at handler time whether
  the job is kept, by predicting whether the client will still receive the body that carries its id. The journal label
  is the *handler's* label even when the attempt is faulted (`faultOutcome` copies `resp.Label`).
- A response returned after an error finding has its fault stripped and, if an index was already claimed, raises
  `fault.attempt_on_rejection`; a fault-ineligible response that claimed nothing consumes no index
  (`provider/handle.go`).
- Perplexity today has no job lifecycle (as of the design, 2026-10-01; U5 built one, see the U5 note under Status).
  `background: true` only raises `perplexity.agent.background.unsupported` and
  the synchronous body is served; only POST aliases are routed; `perplexity_agent` turns are already *create*
  responses, so they cannot double as poll snapshots. Its create reads the **turn-level** fault plan
  (`agentFault` is `provider.TurnFault(s, NameAgent)`), not a `create.fault` block, and the three create spellings share
  one route key, so there is one plan per key.
- Exa today has create, poll and `HEAD`, with a fault key per route and a per-job poll lane (`Route.LaneFrom` =
  `path:id`).
- Verified vendor behaviour (specs fetched 2026-10-01). Exa cancel returns `200 AgentRun`; cancelling a terminal run
  returns the existing run; usage accrued before cancellation is billed; `/stop` is a separate, ultra-only operation and
  is not aliased. Perplexity cancel returns `200 {response_id, status}` where the status enum is *only* `cancelling`; a
  terminal run is `400`; an unknown or other-account id is `404`; the run `Status` enum has no `cancelling`; retrieval
  is `404` for `store: false`.

## Defects found on the way

- **D1** (fixed in #10). `deliversBody` had drifted from what the executor does: `oversized_body` delivered the id yet
  saved no job, and a `body:` override saved a job no client held an id for. It is now derived from the executor and
  checked against a real client. After #10 it reads only the attempt, which is what lets it move into `scenario`
  ([Q3](#q3--a-lost-reply-after-an-accepted-create-recommended-a)).
- **D2** (fixed in #10). `ResolveJob` never compared the entry, so one provider's poll could resolve another's job.
- **D3** (found by the review; owned by U3). **Terminal is not absorbing in serve order.**
  `exa.agent_run.terminal_then_pending` walks turns in *declaration* order, but `SelectTurn` serves by first match.
  Reproduced on `main`: `scenarios/protocol/async-failed.yaml` with the first Exa turn's `call_index` changed from 0 to
  1 loads with no finding, and three polls return `failed`, `running`, `failed`. Under "terminal at cancel time" a
  client could watch a run finish and then have it cancelled. U3 owns a shared check that evaluates a script *by index*
  (0 through the highest `call_index` plus one) and rejects a non-terminal snapshot after a terminal one, applied to
  `turns`, `cancel.turns` and `background.turns`.

## Q1 — job state for cancel (recommended: 1A)

Cancel needs the job's poll position and a durable "cancel accepted" fact. The position is a fault-engine counter that
can be read only by claiming it, so the job itself gets a little mutable state.

- **State on the existing record:** `Polls`, `CancelRequested`, `CancelAtPoll`. Living on the record means a reset drops
  them with the job, they stay inside the slot bound, and they are isolated by namespace and id.
- **Two atomic store operations,** each one critical section with no callback under the store's lock:
  - `Advance(ns, id, i)` sets `Polls = max(Polls, i+1)` and returns the record, or not-found.
  - `MarkCancel(ns, id, atPoll)` is a compare-and-set that succeeds only when there is no marker yet and
    `Polls == atPoll`. It returns the current record together with an outcome — marked, already marked, position
    moved, or not found — so a caller decides the next iteration without a second `Lookup` and the reasons stay
    distinguishable. The caller loops, bounded; each retry means another poll completed `Advance` in between, so
    exhausting the bound takes a poll storm on one job, and it answers the vendor's `500` plus an error finding with
    nothing recorded.
- **Reset.** A poll can resolve a job, a reset can drop the jobs and the cursors, a new create can re-mint the same
  derived id, and the old poll's `Advance` then lands on the new job. `max` keeps the inflated count. House rule 6
  already says reset is not a concurrency mechanism, so a request in flight across a reset is **undefined**, and the
  store's documentation says so, so that `max` is not read as a guard against it. A reset can also land between
  `ResolveJob` and either operation, which is why both report not-found.
- **"Terminal at cancel time"** means the snapshot the job's *next* poll would receive — `SelectTurn(script, Polls,
  pollRoute, nil)`, claiming nothing — is terminal. It is chosen over "last poll served" because only it lets a scenario
  script "the client saw `running` while the run had already completed". It requires D3's absorbing-terminal check.
- **Poll.** `provider.SelectPollTurn` owns the claim *and* `Advance`, so no profile calls `CallIndex` on a poll route
  itself. `Advance` runs immediately after the claim and unconditionally, including when turn selection then fails,
  because the index is spent either way; `Polls` must equal the poll lane's claimed count or "the next snapshot" is
  computed from the wrong index. If a cancel is marked and `i >= CancelAtPoll`, the snapshot is
  `cancel.turns[i-CancelAtPoll]`; otherwise `turns[i]`. Journal attempt indices stay absolute, so `AssertPollSequence`
  keeps working.
- **Cancel route.** Its own fault key (`exa:agent_runs.cancel`, `perplexity:agent.cancel`) and `LaneFrom path:id`, so
  every job has its own cancel attempt budget, planned by `cancel.fault` through a selector beside `createFault`.
- **The claim rule.** Every authenticated cancel that resolves a job claims **exactly one** cancel-lane attempt and
  returns a **served, fault-eligible** response, for all three outcomes below. Perplexity's terminal `400` is a *served*
  response — the vendor's documented answer — and is not built as a rejection: a rejection strips its fault and raises
  `fault.attempt_on_rejection`, which would turn "completion wins" into a finding that fails `AssertNoErrors` and would
  make the index a retry draws depend on the job's state rather than on how many cancels were sent.
- **Cancel steps:** authenticate, `ResolveJob`, decide, record the cancel if the attempt commits (see Q3), render.
  - *Not yet marked.* Peek `turns[Polls]`. Terminal: Exa returns `200` with that snapshot, Perplexity `400`. Otherwise
    `MarkCancel(Polls)`: Exa returns `200` with `cancel.turns[0]` (peeked, not claimed); Perplexity returns
    `200 {response_id, status: "cancelling"}`.
  - *Already marked.* Peek `cancel.turns[Polls-CancelAtPoll]`. Exa returns it; Perplexity returns `400` if it is
    terminal, otherwise `200 cancelling` (simulator policy — the spec is silent).
  - *Accepted but no `cancel.turns`, or a peek with no matching turn* (a script with no unconditional final turn is only
    a warning, `exa.agent_run.script_exhausted`). Error finding `job.cancel_unscripted` and the vendor's `500`, and
    **no marker is recorded**, so later polls never index into a `cancel.turns` that does not exist. An already-terminal
    cancel needs no `cancel:` block.

```yaml
exa_agent_runs:
  cancel:
    fault: {attempts: [{status: 500}, {}]}   # each job's first cancel fails; nothing is recorded
    turns:                                    # call_index counts polls since the cancel
      - when: {call_index: 0}
        respond: {status: running}            # an acknowledgement is not proof billing stopped
      - respond: {status: cancelled, usage: {agentComputeUnits: 3}, cost_dollars: {total: 0.012}}
  turns:
    - when: {call_index: 0}
      respond: {status: running}
    - when: {call_index: 1}
      respond: {status: running}
    - respond: {status: completed, cost_dollars: {total: 0.045}}
```

Create, poll, cancel: the cancel wins. Create, poll, poll, cancel: completion wins — Exa returns the completed run,
Perplexity returns `400`.

**Validation.** Run `validateFault` on `cancel.fault` *and* on `create.fault` (closing an existing gap), reachability
and projection validators on `cancel.turns`, and D3's absorbing-terminal check on every script. `completed` is allowed
in `cancel.turns`, to script "acknowledged, then completed anyway". An entry-level `cancel:` on an entry with no poll
lifecycle (`perplexity_agent`, `exa`) is a **load error**; otherwise it decodes and does nothing. Per ruling 6, a
terminal Exa turn — `cancelled` like any other — that scripts no `cost_dollars` (tightened to `cost_dollars.total` on
2026-10-02, see the correction under ruling 6) raises the warning `exa.agent_run.cost_unscripted` and renders zero
placeholders; the warning tells the author to script the cost.

**Documented numbering.** In `cancel.turns`, `when.call_index` counts from the cancel, while `Match.CallIndex` is
documented as the count of prior requests in the lane and the journal's `attempt_index` stays absolute. That is the
right call over a new `when:` axis; it is documented on `Match.CallIndex`, the one place the two numbers differ for the
same request.

**Rejected.** A new `when:` axis (changes exported `Match.Matches` and `SelectTurn`); a cancel response that declares
its own outcome (two sources of truth that can contradict); a new lane after cancel (lane resolution would depend on
stored state and the poll fault plan would restart); "last poll served" terminality (cheaper, but cannot express the
race).

## Q2 — the Perplexity lifecycle

- **Which requests mint a job:** `background: true` with `stream: false` only. Minting for every `store`-not-false
  response was rejected: each synchronous call would burn a slot of the 256-per-namespace bound; synchronous ids already
  collide across `turn_key` lanes (`profiles/perplexity/render.go` derives them from the route's fault key and the
  index, not the lane key), which would become `ErrDuplicate` failures; and a `response_id:` override collides on the
  second call.
- **The divergence this accepts (ruling 7).** The spec hides a response from retrieve only when `store` is false, so on
  the real API a synchronous response with `store` unset is retrievable. Here it answers `404` in the vendor's error
  shape — a plausible wrong answer. It is recorded as a named divergence in the contract notes. No finding can fire on
  that `404`: the handler cannot tell a synchronous id from a stale one, and `job.foreign_id` only fires once the
  namespace has minted a job, so the contract note is the signal.
- **Where the poll turns live:** a `background: {turns, cancel}` block on `perplexity_agent`, not a new entry kind. A
  new entry would force a block into all 20 built-in scenarios (`scenarios/scenarios_test.go` requires every entry kind)
  and break consumers' `AssertCovers`.
- **Create:** with the block present, `MintJob(x, NameAgent, "resp_", Hex32)` and a queued snapshot — `id`, `object`,
  `created_at` from the scenario base time, `status: queued`, `model`, `output: []` — with `usage` omitted (optional in
  the spec) unless scripted. Without the block: fail closed (ruling 4).
- **A shared index.** A background create and the synchronous calls in the same lane share one call index (`MintJob`
  claims on the create lane), so a background create shifts which `turns[i]` the next synchronous call receives. The
  contract notes say so.
- **Retrieve:** `GET /v1/agent/{id}` on fault key `perplexity:agent.retrieve`, `LaneFrom path:id`; `ResolveJob` with the
  entry check, a miss being the `404` `{error}`; then `SelectPollTurn` over `background.turns` or
  `background.cancel.turns`.
- **Simulator policy where the spec is silent** (recorded as such in the contract notes): `background` with `store:
  false` returns the queued response, saves no job, and raises `perplexity.agent.background.unstored` — later polls are
  `404`, as the spec says; a second cancel while cancelling returns `200 cancelling` unless the run is terminal.
- **`background: true` with `stream: true` fails closed** with its own finding code, the same as the non-streaming case
  with no script. Falling through to a synchronous stream would be the invented behaviour ruling 4 removes.

## Q3 — a lost reply after an accepted create (recommended: A)

- **`FaultAttempt.Accepted`**, YAML `accepted: true`. One predicate, `commits(dec) = deliversBody(dec) ||
  dec.Attempt.Accepted`, is used by `MintJob` (the job is kept) and by the cancel record.
- **Where it can appear.** Allowed in `create.fault`, in `cancel.fault`, and in the **turn-level plan** of an entry
  whose create is a turn. The last is needed because Perplexity's create reads `provider.TurnFault`, not `create.fault`,
  and the engine holds one plan per route key, so a background create cannot have a plan separate from synchronous calls
  without a seam change (plan selection by request body). A path-based placement rule would make the mechanism
  unscriptable on the Perplexity lifecycle, which #7 requires it to support. So placement is validated by the profile,
  which knows which routes mint, plus a **runtime finding** when an `accepted` attempt is claimed by a request that
  mints nothing — the pattern `scenario.stream_abort_unreachable` already uses for a claimed attempt that cannot apply.
  The exact spelling of that finding is settled in U2.
- **`redundant` is the predicate's result.** `accepted` is an error on an attempt that already delivers its body, and on
  a `stream_*` kind. "Already delivers its body" is `deliversBody`, which is unexported in `provider` while `scenario`
  cannot import `provider`; a second copy is the drift D1 was. So the predicate becomes an **exported method on
  `scenario.FaultAttempt`**, called by both the validator and `MintJob` — the precedent is `FaultKind.IsStream`,
  exported so `provider` "shares this one predicate rather than carrying a second copy". After #10 it reads only the
  attempt, so it can move. Defining `redundant` as the result, not as a list of kinds, also covers the shapes #10 found
  do **not** deliver, where `accepted` is therefore meaningful: a `body:` override below 400, and a `204` or `304`
  status, alongside `close_before_headers`, `truncate_body`, `empty_body`, `invalid_json` and any status of 400 or
  above.
- **Rejected before acceptance** is the same attempt *without* the modifier: no job. A client retry claims index 1 and
  mints a *second* job — no invented idempotency; neither Agent API documents any. At the job bound the attempt gets a
  provider rejection and the scripted fault is not applied, which is loud. An older binary rejects the file at load
  because fault attempts are decoded strictly.
- **No new race:** the fault decision is cached, so `MintJob` and `Handle` read the same attempt; the job is saved
  before the first byte, so `Sim.Jobs()` is correct as soon as the client sees the error. The aborted journal entry may
  land later, so a test uses the existing bounded `AwaitRequests`.
- **Evidence that already exists:** the journal's create entry (`fault_kind`, `aborted`, `attempt_index`, `fault_key`)
  and `GET /__admin/jobs` (entry and create index), matched on namespace, entry and create index. The listing carries no
  lane field and must not gain one: a lane key can embed a header-derived `turn_key` value, which could carry a
  credential into an admin listing. A read-only job listing is evidence for a test controller, not a recovery API — it
  is not reachable by application code under test.
- **What `accepted` does and does not hide (found building U2).** `accepted` keeps the *job*; whether the *client* learns
  the id depends on the shape. `close_before_headers`, `empty_body`, `invalid_json` and a status of 400 or above withhold
  it, provided the profile's `FaultBody` is built from the attempt alone — a profile that registers none serves its own
  rendered body, id included, under an error status. `truncate_body` sends a *prefix* of the rendered body, and the id is
  the first key of both in-tree creates, so a default or large truncation delivers the whole id (a clean `200` when the
  cut is at or past the body length); the id is withheld only when `truncate_after_bytes` is below the id's offset plus
  length. The mechanism does not warn about that combination, since a warning would be a further exported finding code;
  the documentation says it plainly and the tests pin it.
- **Rejected:** a new `FaultKind` (one per failure shape, every switch over kinds must learn it, and `fault_kind`
  filters would miss it); a plan-level flag or index list (too coarse, and it drifts against `repeat:`).

## Exported surface

| Addition | Where | Land early? |
|---|---|---|
| `FaultAttempt.Accepted` / `accepted:`; code `scenario.fault.accepted.redundant`; a runtime finding for an unreachable `accepted` | `scenario`, YAML | yes |
| the delivery predicate as an exported method on `scenario.FaultAttempt` (name settled in U2) | `scenario` | with the field |
| `validateFault` on `create.fault` (behaviour change) | `scenario` | with the field; tag note |
| `CancelPolicy{Fault, Turns}`, `ProviderEntry.Cancel` / `cancel:`; a load error for an entry-level `cancel:` with no poll lifecycle | `scenario`, YAML | yes |
| `background:` block type and field on `perplexity_agent`; fail-closed finding codes for no block and for `stream: true` | `scenario`, YAML | yes — the name lasts |
| the absorbing-terminal script check (applies to existing `turns` too) | `scenario` / profile validators | U3 |
| `Job.Polls`, `CancelRequested`, `CancelAtPoll`; `Advance` and `MarkCancel` on `jobs.Store`, with their return shapes and the exported outcome type | `internal/jobs` via `testkit` | decide early |
| `provider.SelectPollTurn` (claim and advance together), `provider.CancelJob` and their outcome constants | `provider` | with the mechanism unit |
| finding codes `job.cancel_unscripted`, a cancel-loop-exhausted code, `exa.agent_run.cost_unscripted` (warning), `perplexity.agent.background.unstored` | findings | with their units |
| `polls` and `cancel_at_poll` on `GET /__admin/jobs`; `cancel_at_poll` is **absent** when no cancel is recorded, because 0 is a real position | admin JSON | with the mechanism unit |
| route labels: `…cancel.accepted` only for a cancel that was *recorded*, a distinct label otherwise; Exa poll label becomes `exa.agent_runs.polled.<status>` | journal strings | the relabel is a behaviour change |
| `Match.CallIndex` documentation (numbering inside `cancel.turns`) | `scenario` | with `cancel:` |
| Perplexity retrieve and cancel patterns, fault-key constants | `profiles/perplexity` | keep unexported |
| `testkit.AssertJobTimeline` | `testkit` | defer |

Nothing changes in the root `servicesim` package or the `contracts` Go API.

## Evidence for consumers

No new journal field. A consumer tells the three situations apart without sleeps or mutable admin scripting, with one
caveat the review found: **the journal label is the handler's label even on a faulted attempt**, so a label alone can
over-claim.

1. *The client closed the connection*: no cancel entry for the job and no cancel marker on it; where the server saw the
   hang-up, `aborted` with `fault_kind: delay`, or `stream.state: client_gone`.
2. *A vendor cancel request that took effect*: a cancel-route entry labelled `…cancel.accepted` with fault key
   `…cancel|path:id=<id>`. The handler knows `commits(dec)` when it picks the label (the decision is cached), so a
   scripted `{status: 500}` that recorded nothing carries the distinct non-recorded label and `…cancel.accepted` is kept
   for cancels that were recorded. `cancel_at_poll` on the listing separates "accepted but the reply was lost" from
   "rejected".
3. *Confirmed terminal cancellation*: a poll entry whose label carries `cancelled` **and whose entry carries no fault**
   — a poll labelled `…polled.cancelled` that went out as a scripted `503` is not a cancellation the client observed —
   ordered after the cancel by `seq`. Exa needs the label change because the journal keeps no response bodies.

The sequence is read by filtering `Namespace.Requests()` by the id path segment.

## Sequencing

| Unit | Content | Depends on |
|---|---|---|
| U1 | `fix(provider)`: `deliversBody` (D1) and the entry check (D2) — PR #10 | — |
| U2 | #7: `accepted:`, the exported delivery predicate, the unreachable-`accepted` finding, `validateFault` on `create.fault` | U1 **merged** |
| U3 | cancel mechanism: job state, store operations, `SelectPollTurn` / `CancelJob`, the absorbing-terminal check (D3), admin listing fields; no routes | U2 |
| U4 | Exa cancel route, the `cost_unscripted` warning (ruling 6), the `/stop` contract note, goldens | #8 (Exa), U3 |
| U5 | Perplexity `background` create and retrieve, the named divergence (ruling 7) | #8 (Perplexity), U3 |
| U6 | Perplexity cancel | U5 |
| U7 | documentation and the tag note | U2–U6 |

U2 waits for #10 to merge because the predicate it exports is the one #10 rewrites. U2–U6 ship in one tag. #8 comes
first because the cancel response *is* an Exa `AgentRun`, and the error envelope (`RUN_NOT_FOUND`), the create status,
the enums, the cost policy and Perplexity's required fields are all prerequisites.
