# Cancellation lifecycles and the accepted-create fault

## Status

**Proposal, 2026-10-01 — awaiting an independent design review before any code.** Written against `main` @ `aeb86e1` for
[#6](https://github.com/C360Studio/servicesim/issues/6) (cancellation lifecycles) and
[#7](https://github.com/C360Studio/servicesim/issues/7) (an accepted create whose response is lost), and sequenced after
[#8](https://github.com/C360Studio/servicesim/issues/8) (the Agent contract audit), whose corrections are
prerequisites. It
adds new exported framework surface — a fault modifier, two scenario blocks, mutable job state and two job-store
operations — so it is read independently before implementation ([ADR 0003](../adr/0003-framework-seam.md): consumers pay
for every exported symbol).

Provenance: an architect pass read the code and both vendor specs; the two defects in the next section were then
re-verified by reading the code. Nothing here has been compiled or spiked. Where this document and the verified vendor
contract disagree, the contract wins ([ADR 0002](../adr/0002-verified-contract-precedence.md)).

### The question for the reviewer

One judgment, over the evidence in this file and the code it cites:

> Is design **1A** — job state on the existing record, "terminal at cancel time" judged by the job's *next* poll, a
> compare-and-set `MarkCancel`, and `cancel:` / `background:` scenario blocks — the smallest sound mechanism for a
> deterministic simulator? And are the exported names and surfaces in [Exported surface](#exported-surface) the ones to
> lock in pre-1.0?

Specifically, name any interleaving of create, poll, cancel and reset (including a faulted poll that still claims an
attempt, and a cancel racing a poll of the same job) that yields a nondeterministic or inconsistent result, and say
whether
the decided rulings below leave a hole.

### Rulings already taken (owner, 2026-10-01)

1. The vendor specification is the authority and parity with the real service is preferred.
2. The project is greenfield with one user who expects breaking fixes: breaking changes are approved, with no
   compatibility shims.
3. Retrieve and cancel are the **spec-declared routes only** — `GET /v1/agent/{id}`, `POST /v1/agent/{id}/cancel` and
   `POST /agent/runs/{id}/cancel`. No `/v1/responses/{id}` alias is served; the spec's prose mention of it is recorded
   as
   an unresolved inconsistency in the contract notes.
4. Perplexity `background: true` with no scripted lifecycle **fails closed**: a provider-shaped error plus a named
   finding, never an invented poll script.
5. The two job-lifecycle operations are added to `jobs.Store` itself (a compile-time break for a custom store) rather
   than as an optional capability.

## Facts that force the design

- `internal/jobs.Job` is immutable coordinates — ID, namespace, entry, lane key, create index, creation time. A poll
  re-renders from the scenario by poll *count*, never by elapsed time (`docs/design/async-jobs.md` §5.3). `jobs.Store`
  is
  Create, Lookup, StatsIn, ResetIn and Reset, with `List` an optional extra. `testkit.Job` and `testkit.Jobs` re-export
  them and document the store as implementable by consumers, so both are exported surface.
- `provider.Faults` is `Next` plus `Reset`: an attempt counter can be read only by *claiming* it. `provider.SelectTurn`
  has no side effects; `SelectTurnFor` claims.
- Faults are applied after the handler returns (`provider/fault_exec.go`), so `MintJob` decides at handler time whether
  the
  job is kept, by predicting whether the client will still receive the body that carries its id (`deliversBody`).
- Perplexity today has no job lifecycle. `background: true` only raises `perplexity.agent.background.unsupported` and
  the
  synchronous body is served; only POST aliases are routed; `perplexity_agent` turns are already *create* responses, so
  they
  cannot double as poll snapshots.
- Exa today has create, poll and `HEAD`, with a fault key per route and a per-job poll lane (`Route.LaneFrom` =
  `path:id`).
- Verified vendor behaviour (specs fetched 2026-10-01). Exa cancel returns `200 AgentRun`; cancelling a terminal run
  returns
  the existing run; usage accrued before cancellation is billed; `/stop` is a separate, ultra-only operation and is not
  aliased. Perplexity cancel returns `200 {response_id, status}` where the status enum is *only* `cancelling`; a
  terminal run
  is `400`; an unknown or other-account id is `404`; the run `Status` enum has no `cancelling`; retrieval is `404` for
  `store: false`.

## Two defects found on the way

Both are independent of the design and are fixed first (unit U1).

- **D1.** `deliversBody` omits `oversized_body`: the executor writes the padded body, which carries the id, yet the job
  is
  never saved, so every poll is a `404`. The reverse also holds: an attempt with a `body:` override and a status below
  400
  replaces the response yet the job *is* saved — a job no client holds an id for.
- **D2.** `ResolveJob` looks a job up by namespace and id and never compares `job.Entry`, so one provider's poll can
  resolve
  another provider's job. Once cancel exists that becomes one provider mutating another's job.

## Q1 — job state for cancel (recommended: 1A)

Cancel needs the job's poll position and a durable "cancel accepted" fact. The position is a fault-engine counter that
can
be read only by claiming it, so the job itself gets a little mutable state.

- **State on the existing record:** `Polls`, `CancelRequested`, `CancelAtPoll`. Living on the record means a reset drops
  them with the job, they stay inside the slot bound, and they are isolated by namespace and id.
- **Two atomic store operations:** `Advance(ns, id, i)` sets `Polls = max(Polls, i+1)` and returns the record;
  `MarkCancel(ns, id, atPoll)` is a compare-and-set that succeeds only when there is no marker yet and `Polls ==
  atPoll`,
  otherwise the caller re-reads and decides again in a bounded loop.
- **"Terminal at cancel time"** means the snapshot the job's *next* poll would receive — `SelectTurn(script, Polls,
  pollRoute, nil)`, claiming nothing — is terminal. It is chosen over "last poll served" because only it lets a scenario
  script "the client saw `running` while the run had already completed".
- **Poll:** `i := x.CallIndex()` then `Advance`. If a cancel is marked and `i >= CancelAtPoll`, the snapshot is
  `cancel.turns[i-CancelAtPoll]`; otherwise `turns[i]`. Journal attempt indices stay absolute, so `AssertPollSequence`
  keeps
  working.
- **Cancel route:** its own fault key (`exa:agent_runs.cancel`, `perplexity:agent.cancel`) and `LaneFrom path:id`, so
  every
  job has its own cancel attempt budget, planned by `cancel.fault` through a selector beside `createFault`.
- **Cancel steps:** authenticate, `ResolveJob` (with the entry check from D2), decide, record the cancel if the attempt
  commits (see Q3), render.
  - *Not yet marked.* Peek `turns[Polls]`. Terminal: Exa returns `200` with that snapshot, Perplexity `400`. Otherwise
    `MarkCancel(Polls)`: Exa returns `200` with `cancel.turns[0]` (peeked, not claimed); Perplexity returns
    `200 {response_id, status: "cancelling"}`.
  - *Already marked.* Peek `cancel.turns[Polls-CancelAtPoll]`. Exa returns it; Perplexity returns `400` if it is
    terminal,
    otherwise `200 cancelling` (simulator policy — the spec is silent).
  - *Accepted but no `cancel.turns`.* Error finding `job.cancel_unscripted` and the vendor's `500`. An already-terminal
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

**Validation.** Run `validateFault` on `cancel.fault` *and* on `create.fault` (closing an existing gap); reachability
and
projection validators on `cancel.turns`. A terminal Exa `cancelled` snapshot without `cost_dollars` is a load error
(`exa.agent_run.cancelled_without_cost`): the field is required, so a default would be an invented zero. `completed` is
allowed in `cancel.turns`, to script "acknowledged, then completed anyway".

**Rejected.** A new `when:` axis (changes exported `Match.Matches` and `SelectTurn`); a cancel response that declares
its own
outcome (two sources of truth that can contradict); a new lane after cancel (lane resolution would depend on stored
state
and the poll fault plan would restart); "last poll served" terminality (cheaper, but cannot express the race — the
fallback
if the reviewer rejects peeking).

## Q2 — the Perplexity lifecycle

- **Which requests mint a job:** `background: true` with `stream: false` only. Minting for every `store`-not-false
  response
  was rejected: each synchronous call would burn a slot of the 256-per-namespace bound; synchronous ids already collide
  across `turn_key` lanes (`profiles/perplexity/render.go` derives them from the route's fault key and the index, not
  the
  lane key), which would become `ErrDuplicate` failures; and a `response_id:` override collides on the second call.
- **Where the poll turns live:** a `background: {turns, cancel}` block on `perplexity_agent`, not a new entry kind. A
  new
  entry would force a block into all 20 built-in scenarios (`scenarios/scenarios_test.go` requires every entry kind) and
  break consumers' `AssertCovers`.
- **Create:** with the block present, `MintJob(x, NameAgent, "resp_", Hex32)` and a queued snapshot — `id`, `object`,
  `created_at` from the scenario base time, `status: queued`, `model`, `output: []` — with `usage` omitted (optional in
  the
  spec) unless scripted. Without the block: fail closed (ruling 4).
- **Retrieve:** `GET /v1/agent/{id}` on fault key `perplexity:agent.retrieve`, `LaneFrom path:id`; `ResolveJob` with
  the entry
  check, a miss being the `404` `{error}`; then `Advance` and select from `background.turns` or
  `background.cancel.turns`.
- **Simulator policy where the spec is silent** (recorded as such in the contract notes): `background` with `store:
  false`
  returns the queued response, saves no job, and raises `perplexity.agent.background.unstored` — later polls are `404`,
  as the
  spec says; `background` with `stream: true` stays out of scope for this slice; a second cancel while cancelling
  returns
  `200 cancelling` unless the run is terminal.

## Q3 — a lost reply after an accepted create (recommended: A)

- **`FaultAttempt.Accepted`**, YAML `accepted: true`. One predicate, `commits(dec) = deliversBody(dec) ||
  dec.Attempt.Accepted`,
  is used by `MintJob` (the job is kept) and by the cancel record.
- It is meaningful on `close_before_headers`, `truncate_body`, `empty_body`, `invalid_json` and any status of 400 or
  above
  (a `504`, say). Scenario validation needs no provider knowledge: `accepted` is allowed only under `create.fault` and
  `cancel.fault` (`scenario.fault.accepted.misplaced`), and is an error on an attempt that already delivers its body
  (`scenario.fault.accepted.redundant`) or on a `stream_*` kind. An older binary rejects the file at load because fault
  attempts are decoded strictly.
- **Rejected before acceptance** is the same attempt *without* the modifier: no job. A client retry claims index 1 and
  mints a
  *second* job — no invented idempotency; neither Agent API documents any. At the job bound the attempt gets a provider
  rejection and the scripted fault is not applied, which is loud.
- **No new race:** the fault decision is cached, so `MintJob` and `Handle` read the same attempt; the job is saved
  before the
  first byte, so `Sim.Jobs()` is correct as soon as the client sees the error. The aborted journal entry may land
  later, so a
  test uses the existing bounded `AwaitRequests`.
- **Evidence that already exists:** the journal's create entry (`fault_kind`, `aborted`, `attempt_index`, `fault_key`)
  and
  `GET /__admin/jobs` (create index, lane), matched on namespace, lane and index. A read-only job listing is evidence
  for a
  test controller, not a recovery API — it is not reachable by application code under test.
- **Rejected:** a new `FaultKind` (one per failure shape, every switch over kinds must learn it, and `fault_kind`
  filters
  would miss it); a plan-level flag or index list (too coarse, and it drifts against `repeat:`).

## Exported surface

| Addition | Where | Land early? |
|---|---|---|
| `FaultAttempt.Accepted` / `accepted:`; codes `scenario.fault.accepted.misplaced` and `.redundant` | `scenario`, YAML | yes |
| `validateFault` on `create.fault` (behaviour change) | `scenario` | with the field; tag note |
| `CancelPolicy{Fault, Turns}`, `ProviderEntry.Cancel` / `cancel:` | `scenario`, YAML | yes |
| `background:` block type and field on `perplexity_agent` | `scenario`, YAML | yes — the name lasts |
| `Job.Polls`, `CancelRequested`, `CancelAtPoll`; `Advance` and `MarkCancel` on `jobs.Store` | `internal/jobs` via `testkit` | decide early |
| `provider.SelectPollTurn`, `provider.CancelJob` and their outcome constants | `provider` | with the mechanism unit |
| finding codes `job.cancel_unscripted`, `exa.agent_run.cancelled_without_cost`, `perplexity.agent.background.unstored` and the fail-closed code | findings | with their units |
| `polls`, `cancel_at_poll` on `GET /__admin/jobs` | admin JSON | with the mechanism unit |
| route labels `…cancel.accepted` / `…cancel.terminal`; Exa poll label becomes `exa.agent_runs.polled.<status>` | journal strings | the relabel is a behaviour change |
| Perplexity retrieve and cancel patterns, fault-key constants | `profiles/perplexity` | keep unexported |
| `testkit.AssertJobTimeline` | `testkit` | defer |

Nothing changes in the root `servicesim` package or the `contracts` Go API.

## Evidence for consumers

No new journal field. A consumer tells the three situations apart without sleeps or mutable admin scripting:

1. *The client closed the connection*: no cancel entry for the job and no cancel marker on it; where the server saw the
   hang-up, `aborted` with `fault_kind: delay`, or `stream.state: client_gone`.
2. *A vendor cancel request*: a cancel-route entry labelled `…cancel.accepted` or `…cancel.terminal` with fault key
   `…cancel|path:id=<id>`; `cancel_at_poll` on the listing separates "accepted but the reply was lost" from "rejected".
3. *Confirmed terminal cancellation*: a poll entry whose label carries `cancelled`, ordered after the cancel by `seq`.
   Exa
   needs the label change because the journal keeps no response bodies.

The sequence is read by filtering `Namespace.Requests()` by the id path segment.

## Sequencing

| Unit | Content | Depends on |
|---|---|---|
| U1 | `fix(provider)`: `deliversBody` (D1) and the entry check (D2) | — |
| U2 | #7: `accepted:` and `validateFault` on `create.fault` | U1 |
| U3 | cancel mechanism: job state, store operations, `SelectPollTurn` / `CancelJob`, admin listing fields; no routes | U2 |
| U4 | Exa cancel route, the `/stop` contract note, goldens | #8 (Exa), U3 |
| U5 | Perplexity `background` create and retrieve | #8 (Perplexity), U3 |
| U6 | Perplexity cancel | U5 |
| U7 | documentation and the tag note | U2–U6 |

U2–U6 ship in one tag. #8 comes first because the cancel response *is* an Exa `AgentRun`, and the error envelope
(`RUN_NOT_FOUND`), the create status, the enums, the cost policy and Perplexity's required fields are all prerequisites.

## Open for the reviewer

- The names `accepted`, `cancel:` and `background:`.
- Whether `Advance` on a faulted poll — which claims an attempt index — is the right definition of "polls so far".
- Whether widening `jobs.Store` (ruling 5) interacts badly with the bounded compare-and-set loop.
