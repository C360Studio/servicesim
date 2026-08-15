# Async job lifecycle

> ## REVISED — pending re-review
>
> An adversarial review returned **needs-revision** on 2026-08-15 with one blocker and five majors. All were
> answered in Phase 2 of the adopter plan:
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
> **Still a design, not an instruction to start.** The plan's own verification step for Phase 2 is to re-run the
> adversarial review against the revised text and confirm the findings are *answered* rather than *restated*. That
> has not been done yet. Implementation is Phase 3 and is gated on it.

An addendum to [`package-design.md`](package-design.md) and [`extended-surfaces.md`](extended-surfaces.md). Where the
three disagree, this file is newest and wins for the create-then-poll surfaces it defines; it changes nothing about
the one-shot surfaces that already ship.

It exists because the first adopter's client calls two create-then-poll APIs — Exa's `POST /agent/runs` plus
`GET /agent/runs/{id}`, and Tavily's `POST /research` plus `GET /research/{id}` — and Servicesim models one-shot
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
2. **A `GET` pattern also serves `HEAD`.** `HEAD /agent/runs/run_abc` returned 200 from the `GET` handler. The
   existing `Allow` computation would therefore advertise `GET` on a path that also answers `HEAD`. One-line fix in
   `NewMux`; see [§6](#6-get-routes-on-a-post-shaped-mux).
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
| Create response | `{"id": "...", "status": "running"}` | `{"request_id": "...", "status": "pending"}` |
| Poll | `GET /agent/runs/{id}` | `GET /research/{id}` |
| Poll response | same envelope, `status` advances, `output` appears at terminal | same |
| Credential on create | `x-api-key` header | **key in the JSON body** |
| Credential on poll | `x-api-key` header | **`Authorization: Bearer`** |
| Identifier shape | opaque; 32 lowercase hex matches Exa's house style | UUID, matching Tavily's `request_id` convention |

The create response is an *envelope*: an identifier and an initial status, and nothing a scenario author would want
to script. Everything interesting — the run's progress, its terminal payload, its failure — is on the poll.

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
independent `auth`, `validation`, `fault` and `turns`, resolved by the handler with
`x.Deps.Scenario.Provider(NameAgentRuns)` rather than by listener name. A scenario that uses only the sync surfaces
omits the entry and is unaffected.

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
            content: Report A states the finding.
            citations: [source-a]
          cost_dollars: {total: 0.045}
```

The YAML anchor is doing the de-duplication a `repeat:` key would otherwise do. That is deliberate — see
[§2.5](#25-the-sugar-that-was-rejected).

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
      content: Report A states the finding.
      citations: [source-a]
```

A single-shot block normalises into exactly one unconditional turn, so the first poll is already terminal. The
one-shot form is the zero-pending-poll job in the same way that a single projection is the length-1 conversation.
Everything downstream sees one shape.

### 2.5 The sugar that was rejected

`repeat: 2` on a turn would be tidier than the anchor, and it is not in this design:

- It makes turn selection **positional** ("this turn answers the next N calls") in a model that is otherwise
  **predicate-based** ("the first turn whose `when` matches"). The two have to be reconciled in one selector, and the
  reconciliation is where a fixture author's mental model breaks.
- It would change what `call_index` means for existing files the moment the two are combined, which is a
  version-2 event ([§9](#9-schema-versioning-additive-to-version-1)) bought for syntax.
- YAML anchors already solve the duplication, are standard, and are already used elsewhere in fixtures.

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

`ValidateProjections` on an async entry runs before readiness and reports:

| Finding | Severity | Condition |
|---|---|---|
| `exa.agent_run.failed_without_error` | error | `status: failed` with no `error` |
| `exa.agent_run.terminal_then_pending` | error | a non-terminal turn declared after a terminal one — a job that un-completes |
| `exa.agent_run.script_exhausted` | warning | no unconditional final turn: poll N+1 gets `scenario.no_matching_turn` and a 404 the author did not intend |
| `exa.agent_run.body_predicate_on_poll` | warning | `body_contains` or `body_json` on a turn of an async entry; a GET carries no body, so the predicate can never match |
| `exa.agent_run.completed_without_output` | warning | `status: completed` with no `output` |

The last two are the ones a fixture author actually hits, and both are silent failures without a load-time check.

---

## 3. Routes, fault keys and the per-job lane

### 3.1 The route table

| Listener | Route | Fault key | Plan read from | `LaneFrom` |
|---|---|---|---|---|
| exa | `POST /agent/runs` | `exa:agent_runs.create` | `create.fault` on the entry | — |
| exa | `GET /agent/runs/{id}` | `exa:agent_runs.poll` | the first turn declaring `attempts` | `["path:id"]` |
| tavily | `POST /research` | `tavily:research.create` | `create.fault` on the entry | — |
| tavily | `GET /research/{id}` | `tavily:research.poll` | the first turn declaring `attempts` | `["path:id"]` |

Create and poll draw on **separate** budgets, for the same reason `exa:search` and `exa:answer` do: a poll retry must
not consume the create's retries, and a retry of one must not be answered from the other's plan.

Separate budgets take **two** things, and an earlier draft of this document supplied only the first:

1. **Separate fault keys** give separate *counters*. That is the `Fault key` column, and it is sufficient for
   attempts to be counted independently.
2. **Separate `Route.Fault` selectors** give separate *plans*. Without this, both keys resolve the same
   `attempts:` list — two independent counters walking one script. `attempts: [{status: 429}, {status: 200}]`
   would then rate-limit the create AND, separately, every job's first poll, which is not what its author wrote.

The `Plan read from` column is that second half. It follows the shipped `answerFault` precedent exactly
(`provider/exa/handler.go:64-87`): two routes on one entry already get two distinct plans there, by giving each route
a selector that reads a *different location* in the scenario. `/search` reads the block-level `fault:`;
`/answer` reads `answer.fault` inside the projection. Nothing new is being invented here.

For an async entry the split falls out of the schema itself. [§2](#2-the-scenario-yaml) establishes that **a turn is
one poll snapshot**, so a plan declared on a turn is unambiguously the *poll* plan — it is attached to the thing
polls are made of. The create call has no turn at all, so it needs its own key:

```yaml
  exa_agent_runs:
    create:                          # the POST plan
      fault:
        attempts:
          - {status: 429, retry_after: 1}
          - {status: 200}
    turns:                           # each turn is a poll; a turn plan is the POLL plan
      - fault:
          attempts:
            - {status: 200}
            - {status: 503}          # every job's SECOND poll fails
            - {status: 200}
        respond: {status: running}
      - respond: {status: completed}
```

Because the poll route's lane is per job ([§3.2](#32-routelanefrom)), that poll plan consumes **per job**: the
`503` is every job's second poll, not whichever job happens to poll second globally.

`create.fault` is a reserved envelope key on an async entry, resolved by a `createFault` selector written next to the
route that uses it — the same shape as `answerFault`, nil-safe on every hop, because it runs at composition time
against a scenario that may not have been validated yet.

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
| `job.id_invalid` | `id` | a `path:` extractor resolves to empty, or to a value `ValidJobID` rejects |

The field is the wildcard's own name (`id`), which is the thing the client actually got wrong — it is in the URL
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
| `job.limit_reached` | a create is refused | error, plus the provider-shaped 5xx |

Both messages name the three remedies explicitly — `POST /__admin/reset`, per-test namespaces, or a higher bound —
because the reader hitting this is debugging a suite that worked yesterday, and "namespace is at its job bound" on
its own tells them what happened without telling them what to do.

The 80% mark exists because the wall is otherwise reached with no warning on the request *before* the failing one.
A suite creeping toward the bound over weeks gets a warning in its journal long before a create fails in CI.

### 4.2 `provider` additions

`provider` gains one `Deps` field, one `Route` field, one constant, and two helpers. Nothing else moves.

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
// job.limit_exceeded for a bound breach, job.id_collision for a duplicate.
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

The create response **body** is derived in full: the identifier, the vendor's initial status constant, and nothing
else. A scenario cannot script it, and that is a deliberate v1 limitation rather than an oversight — a projection
body alongside `turns:` is already a load error (`scenario.provider.body_with_turns`), so there is nowhere honest to
put create-side body keys.

Note that `create:` itself is no longer hypothetical: [§3.1](#31-the-route-table) makes it a reserved envelope key
carrying the create route's `fault:` plan, because create and poll need distinct plans and a turn can only speak for
polls. That key is the migration path if create-side *body* scripting is ever needed too — it is additive, and it is
not needed for either vendor today.

#### A faulted create must not leave a phantom job

Faults are applied by `Handle` **after** the handler returns (`provider/handle.go:203-218`). A create handler that
commits its record unconditionally therefore produces one on requests the client never receives an identifier for:
a scripted `attempts: [{status: 429}, {status: 200}]` on the create route mints a job, returns a
`FaultEligible` response, and `Handle` replaces it with a 429. The record survives; the client has no id for it.

The cost is not cosmetic. Every faulted attempt consumes a slot from `MaxJobs`, so a plan with one retry burns two
slots per usable job, and a bound of 256 becomes an effective 128 — reached sooner than any author computed, with
`job.limit_reached` naming a number that does not match the jobs they can see.

**`MintJob` commits the record only when the attempt it claims will actually serve.** It already claims the attempt,
so the decision is in hand at that point — `x.Fault()` says whether this attempt is a serving one or one `Handle`
will replace. When it will be replaced, `MintJob` claims the index (so the plan advances exactly as scripted and the
retry gets attempt 1) and returns without writing a record.

The identifier is still derived from the claimed index, so this does not renumber anything: the retry that succeeds
mints from index 1 and gets the identifier index 1 implies. Two consequences worth stating, because both are
deliberate:

- Identifiers are **not** dense across a faulted create. A plan that faults the first attempt produces a first live
  job whose identifier derives from index 1. That is correct — the identifier tuple includes the attempt, exactly as
  it already does for every other faulted route — and it is why goldens ignore derived identifiers ([§5.2](#52-replay)).
- The record is committed before the response is written but after the fault decision is known. There is no window
  in which a record exists for a response that will be replaced.

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

### 4.5 Import edges

Additive; no edge reverses and the acyclicity proof's labelling is unchanged.

| Level | Package | Change |
|---:|---|---|
| 0 | `internal/jobs` | **new**; imports nothing in-module |
| 3 | `provider` | `+ internal/jobs` (level 3 → 0, legal) |
| 5 | `provider/exa`, `provider/tavily` | `+ internal/jobs` to name `jobs.Job` |
| 5 | `internal/admin` | `+ internal/jobs` for the scoped reset and the read-only listing |
| 6 | `internal/server` | `+ internal/jobs` to construct the registry from `--max-jobs` |
| 7 | `testkit` | `+ internal/jobs`; adds `type Job = jobs.Job` and `type Jobs = jobs.Store` to the alias set |

The alias set is closed under "types a consumer has to name", and `examples/adapter` owns the guard: its
compile-time check must grow a `provider.Deps{Jobs: ...}` construction reading `job.ID` through the aliases, or the
gap stays invisible until an adopter hits it.

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

The bullet above assumes a `turn_key` written on an async entry takes effect. **Today it does not**, and this is a
prerequisite rather than a detail.

`entryTurnKey` reads `x.Entry()` (`provider/lane.go:495`), which resolves by *listener* name —
`Deps.Scenario.Provider(string(x.Provider))`, i.e. `"exa"` (`provider/exchange.go:268`). An `exa_agent_runs` entry is
a second entry on the exa listener, so its `turn_key:` is never read. Two failure modes follow, and both are silent:

1. A `turn_key` on the async entry is **ignored**. The author writes "one lane per model", gets one lane for
   everything, and the only symptom is two jobs sharing a cursor.
2. Moving it to the `exa` entry to make it take effect makes it apply to the poll route too — and a `body_json:`
   extractor cannot resolve against a `GET` with no body, so **every poll** raises
   `scenario.turn_key_unresolved`. The fix for one problem manufactures the other.

**This is not an async-only bug.** `perplexity_agent` is a second entry on the perplexity listener today, so its
`turn_key:` is ignored right now, on shipped code. The async surfaces make it load-bearing rather than latent.

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

`entryTurnKey` then resolves `x.Route.Entry` when set and the listener name otherwise. `exa_agent_runs` and
`perplexity_agent` declare theirs; every existing route leaves it empty and is unaffected.

The `Exchange.policy()` asymmetry recorded in [§12](#12-companion-corrections-this-design-depends-on) — validation
policy also resolved by listener name — has the same root cause and is fixed by the same field. Doing both at once is
cheap; doing one and leaving the other means the next reader has to rediscover which of the two entry lookups is the
broken one.

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
`AssertGoldenJSON`'s default ignore set; `testkit/golden.go:59` already carries all three — `derivedIDPaths` is
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

`provider.NewMux` needs **one** change, and it is not the one a reader expects.

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

The one change: **register `HEAD` explicitly, and answer it 405.**

An earlier draft did the opposite — it added `HEAD` to the `Allow` header on the grounds that a `GET` pattern already
serves it, so advertising only `GET` would be a lie. The observation is correct and the conclusion was wrong, because
of what a poll *is*.

`HEAD /agent/runs/{id}` reaches the GET handler (Go routes it there from a `GET` pattern), which runs
`SelectTurnFor` end to end. That **claims an attempt and advances the job's poll cursor**, and then `net/http`
discards the body. A client sending one `HEAD` to check whether a run exists silently consumes a poll: the response
its next real `GET` receives is the one the scenario wrote for the poll after that. The author sees a job reach
`completed` a poll early, with nothing in the journal explaining why — because from the journal's point of view a
poll did happen.

That is a scripted sequence changing meaning based on a method the scenario never mentioned, which is the failure
class this simulator exists to remove. It outranks the `Allow` fidelity point.

```go
// HEAD is registered explicitly so it does NOT fall through to the GET handler.
// A GET pattern serves HEAD by default, and for a poll route that would claim an
// attempt and advance the job cursor for a request that returns no body.
mux.HandleFunc("HEAD "+pollPath, methodNotAllowed("GET"))
```

**This is where the async mux deliberately diverges from `internal/admin/handler.go:187.** That helper adds `HEAD`
to `Allow` wherever `GET` is answered, and it is right to: the admin routes are stateless reads, so serving `HEAD`
from the `GET` pattern costs nothing. A poll is not a stateless read — it is a cursor advance — so the async routes
must not reuse `allowHeader`'s promotion. `Allow` on a poll path is `GET`, with no `HEAD`.

The divergence is worth stating rather than quietly implementing, because the next person to notice the duplication
will otherwise "fix" it by reaching for the shared helper and reintroduce the bug.

Serving `HEAD` properly — same headers as `GET`, no body, no cursor advance — is not attempted. It would need a
non-claiming turn resolution that exists nowhere else and would have exactly one caller, and no vendor contract in
`contracts/` verifies that either vendor answers `HEAD` at all. If an adopter demonstrates a client that sends it,
that is a contract question first (house rule 1), not a mux question.

Two hazards to keep in the mux test table rather than in a reviewer's head:

- `http.ServeMux` **panics** on conflicting patterns. `/agent/runs` and `/agent/runs/{id}` do not conflict, and the
  `/` catch-all loses to both on specificity, but the panic is at construction, so a future route that does conflict
  fails at startup rather than at request time. That is the right direction and should be asserted, not merely
  assumed.
- Percent-encoding. `GET /agent/runs/run%2Fabc` yields `PathValue("id") == "run/abc"`. Every route that feeds a path
  value into a lane key must validate it first; see [§7.1](#71-the-identifier-charset-is-load-bearing).

`provider/mux_test.go`'s table grows five rows: `GET /agent/runs/{id}` 200, `POST` 405 with `Allow: GET`, the
percent-encoded identifier rejected, `HEAD` 405, and — the one that actually guards the bug — **a `HEAD` followed by
a `GET`, asserting the `GET` receives poll 0**. A test that only checks `HEAD`'s status code would still pass if the
405 were later removed and the cursor advance came back.

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

**Jobs are dropped first — strictly before the fault cursors.** An earlier draft specified journal → faults → jobs,
which opens the exact window the invariant exists to close. The three stores are not reset atomically with respect
to an in-flight request, so between step 2 and step 3 the cursors are back at 0 while the job records they minted
are still live. A create arriving in that window claims index 0, re-mints a live identifier and 500s — the precise
failure described in the paragraph above, produced by the order meant to prevent it.

Reversing it closes the window rather than narrowing it. With jobs gone first, a create arriving mid-reset either
finds a stale cursor and mints an identifier no record holds (harmless — the record is created by that same call),
or finds a fresh cursor and mints from index 0 into an empty store. Neither collides. The reachable interleavings
are safe by construction, not merely unlikely:

> `resetNamespace` calls **jobs → faults → journal**.

`admin.Deps` gains a `Jobs` field, with the same "the wired store does not isolate namespaces" refusal path the
other two already have.

**This needs a race test, not a unit test.** The failure only appears when a create runs concurrently with a reset,
and it is exactly the kind of ordering bug that a sequential test asserts nothing about. The test spawns creates in
a loop against a namespace being reset concurrently and asserts no `job.id_collision` finding appears in the
journal, under `-race`.

`Registry.ResetIn` returns the namespace's slots to the `MaxJobs` budget, for the same reason
`faults.Engine.ResetIn` and `journal.Ring.ResetIn` return theirs: a suite that runs more tests than the bound
depends on teardown handing slots back.

`Registry.ResetIn` returns the namespace's slots to the `MaxJobs` budget, for the same reason
`faults.Engine.ResetIn` and `journal.Ring.ResetIn` return theirs: a suite that runs more tests than the bound
depends on teardown handing slots back.

### 7.4 Optional: a read-only admin listing

`GET /__admin/jobs?namespace=<name>` returning `[{id, namespace, entry, create_index, turn_index, created_at}]` is
worth adding. It mutates nothing, so house rule 6 is untouched, and it is the fastest way to answer the two questions
this surface generates: "did the create I think I made actually happen here?" and "does *this replica* hold the job I
am polling?" — which is the multi-replica diagnostic below.

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

**Make the divergence self-diagnosing.** An earlier draft proposed recomputing the derivation for create indices
`0 … min(localCursor + window, MaxJobs)` in the presented lane. **That cannot be built**, for two independent
reasons, and both are worth recording so it is not proposed again:

- **`localCursor` is not readable.** The `Faults` seam offers exactly `Next(key) FaultDecision` and `Reset()`
  (`provider/deps.go:70-76`). `Next` *claims*. There is no non-claiming read, and adding one so that an error path
  could peek would mean a 404 diagnostic advancing the very cursor whose sequencing is under test — the diagnostic
  would corrupt the thing it exists to explain.
- **"the presented lane" is not obtainable from a poll.** A poll's lane is keyed on `path:id`, so reconstructing
  what a *create* would have minted needs the **create** lane key — which folds in any `turn_key` discriminators
  drawn from the create request's body. A poll has no body and never saw that request.

The replacement needs neither, because the question does not actually require a derivation. "Would this scenario
mint this id?" is a **shape** question, and `ValidJobID` already answers it:

```go
// diagnoseForeignID reports whether id is well-formed for this provider's
// scheme but held by no record in this namespace — the signature of another
// replica having minted it, or of a reset having dropped it.
//
// It changes nothing about the RESPONSE, which is still the vendor's 404. It
// adds a named finding and one error-level log line, so an intermittent 404
// names its own cause instead of looking like a consumer bug.
//
// It needs no cursor and no create lane: a shape check plus "this namespace
// holds at least one record" is enough to separate "your client sent a
// malformed id" from "this id is real and this process is not the one that
// minted it". The cost is one charset scan, not a bounded number of SHA-256s.
func diagnoseForeignID(x *Exchange, id string) bool {
	return ValidJobID(id) && x.Deps.Jobs.CountIn(x.Lane().Namespace) > 0
}
```

`Registry.CountIn` is the one addition, and [§7.4](#74-optional-a-read-only-admin-listing)'s listing needs the same
traversal. The `> 0` guard is what keeps a client's typo from being reported as a replica problem: a process that has
minted nothing has no opinion about whose id this is.

It cannot distinguish "another replica minted it" from "a reset dropped it", and does not try — the finding names
both, exactly as the original wording did. A diagnostic that named one cause with false confidence would be worse
than one that names two and lets the reader pick.

A hit raises `job.foreign_id` on the journal entry and logs `servicesim.job_foreign` at error level with the hint
above. It is a diagnostic, not a correctness mechanism — the response is unchanged — and it is what converts a silent
intermittent 404 into a message naming the replica count. **Shared job state is explicitly out of scope**: it means a
network dependency in a simulator whose value proposition is being fast, offline and hermetic.

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
| `Deps.Jobs`, `Deps.MaxJobs` | **Go**, with working defaults from `Normalized` | none |
| explicit `HEAD` → 405 on poll paths | **Go**, in `NewMux` | none; no existing listener serves a GET route |

The last row of the third column is the one that actually matters for regression risk, so state it as a check to run
rather than a claim to believe: **no existing lane key changes, therefore no existing call index changes, therefore
no existing derived identifier changes, therefore every existing golden still passes.** `hasDiscriminator` returning
`false` for an empty `LaneFrom` is what makes that true, and it is why that one line is called out explicitly in
[§3.2](#32-routelanefrom).

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

- **No time-driven completion.** A job advances by poll count ([§5.3](#53-what-is-deliberately-not-deterministic--and-what-is-deliberately-not-time)).
- **No create-side scripting.** The create response is `{identifier, initial status}`. `create:` as a reserved
  envelope key is the additive migration path if a vendor ever needs more.
- **No list, cancel, events or delete routes.** Exa publishes `GET /agent/runs`, `POST /agent/runs/{id}/cancel`,
  `GET /agent/runs/{id}/events` and `DELETE /agent/runs/{id}`. Each is a bounded addition behind this same model —
  cancel is a route with the same `LaneFrom` and a `canceled` status, events is the streaming surface and is deferred
  with it — and none should be added speculatively. Unimplemented routes fall to the catch-all's provider-shaped 404,
  which is loud enough.
- **No shared job state across replicas** ([§8](#8-multi-replica-the-consequence-stated-explicitly)).
- **No per-turn or per-lane fault plans.** Still deferred, still one plan per route — but the poll route's plan is now
  per job *in effect*, because the lane is.

---

## 11. Implementation fan-out

| Unit | Owns | Depends on |
|---|---|---|
| **A1** | `internal/jobs`: `Job`, `Store`, `Registry`, `Limits`, bounds, `ResetIn`, race tests | — |
| **A2** | `provider`: `Deps.Jobs`, `Deps.MaxJobs`, `Route.Entry`, `Route.LaneFrom`, `LaneFromPath`, `ValidJobID`, `MintJob`, `ResolveJob`, `turnLaneKey` and `hasDiscriminator` changes, the explicit `HEAD` → 405 registration, mux table rows | A1 |
| **A3** | `provider/exa`: routes, request validation, `RunProjection`, render, error envelopes, validator findings | A2 |
| **A4** | `provider/tavily`: the same, plus per-route credential placement (see §12) | A2 |
| **A5** | `internal/admin` scoped reset across three stores, optional `GET /__admin/jobs`; `internal/server` `--max-jobs` wiring and the single-replica startup log | A1, A2 |
| **A6** | `testkit`: `Job`/`Jobs` aliases, `Sim.Jobs()`, a poll-sequence assertion, `examples/adapter` alias guard | A1–A5 |
| **A7** | `scenarios/protocol/async-job.yaml` (completed / failed / stuck), `docs/scenario-schema.md` async section, `contracts/exa/README.md` correction, README + troubleshooting multi-replica sections | A3, A4 |

A1 and A2 are the critical path; A3 and A4 are independent of each other.

---

## 12. Companion corrections this design depends on

Three already-verified defects block or contradict this work. **Two have since shipped**; they are kept here, marked,
rather than deleted, because a reader coming to this design cold needs to know the dependency existed and was met —
a silently-removed prerequisite reads as one that was never needed.

1. ~~**`contracts/exa/README.md` is wrong and must be corrected.**~~ **DONE — v0.1.1.** It stated that `/agent/runs`
   is "not simulated: no C360 consumer uses it", and the adopter's client at `src/pkg/agent/exa.go` calls it. The
   false clause was *struck rather than reworded*, on the grounds that whether some consumer calls a route is not
   something a vendor-contract file can verify in either direction. The paragraph that followed it — that the
   create-then-poll lifecycle "needs a different scenario shape than a single request/response projection" — is the
   part that was right, and is what this document answers. (Backlog item #17.)
2. ~~**Tavily currently returns 401 for a body-placed key.**~~ **DONE — v0.1.1 and Phase 1.** The body placement now
   authenticates on POST routes (v0.1.1), and `Route.Credentials` landed in Phase 1 with the precedence rule
   `auth.headers` > `Route.Credentials` > the package default, resolved once in `Exchange.AcceptedPlacements`.
   `POST /research` takes its credential **in the JSON body** and `GET /research/{id}` takes a **Bearer** header;
   that is now expressible, so this surface is unblocked. Declare it on each route:

   ```go
   // POST /research
   Credentials: []string{provider.PlacementAuthorization, provider.PlacementBodyAPIKey},
   // GET /research/{id} — no body to carry a key in
   Credentials: []string{provider.PlacementAuthorization},
   ```

   (Backlog item #16.)
3. **The multi-replica warning is correct and undocumented.** Still open.
   [§8](#8-multi-replica-the-consequence-stated-explicitly) is the documentation, and it is a deliverable of this
   work rather than a note about it. (Backlog item #18.)

One pre-existing asymmetry is worth recording while this surface is being built, though fixing it is out of scope:
`Exchange.policy()` resolves the validation policy by *listener* name, so a second entry on one listener —
`perplexity_agent` today, `exa_agent_runs` tomorrow — silently inherits the primary entry's `validation:` block.
`authenticate` takes its entry explicitly and is unaffected. The async handlers above pass the entry explicitly
everywhere for this reason.
