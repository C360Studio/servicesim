# Async job lifecycle

> ## ⚠ DRAFT — DO NOT IMPLEMENT FROM THIS YET
>
> An adversarial review returned **needs-revision** on 2026-08-15 with one blocker and five majors. The document
> contradicts itself in places, so building from it would encode the contradiction:
>
> - **Blocker:** §3.1 asserts create and poll draw on separate fault budgets, while §2.5 specifies exactly one plan
>   per route. Both cannot be true.
> - `HEAD` on a poll route reaches the GET handler and silently advances the job's poll cursor.
> - A faulted create leaves a phantom job, because faults apply after the handler returns.
> - §7.3's reset order (journal → faults → jobs) opens the very window it exists to close.
> - `turn_key` extractors resolve against the listener's primary entry, so a `turn_key` on an async entry is
>   silently ignored.
> - §8's `diagnoseForeignID` cannot be built as specified, and the job registry has no release path — a shared
>   container permanently refuses creates once the bound is reached.
>
> Revising this is Phase 2 of the adopter plan and gates implementation. Until then treat it as a considered
> starting point, not a specification.

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
non-empty `attempts:` supplies it, and it starts consuming from that route's first request. Because the poll route's
lane is per job ([§3](#3-routes-fault-keys-and-the-per-job-lane)), a poll plan consumes **per job** — which is what
makes `attempts: [{status: 200}, {status: 429}, {status: 200}]` mean "every job's second poll rate-limits" instead of
"whichever job happens to poll second".

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

| Listener | Route | Fault key | `LaneFrom` |
|---|---|---|---|
| exa | `POST /agent/runs` | `exa:agent_runs.create` | — |
| exa | `GET /agent/runs/{id}` | `exa:agent_runs.poll` | `["path:id"]` |
| tavily | `POST /research` | `tavily:research.create` | — |
| tavily | `GET /research/{id}` | `tavily:research.poll` | `["path:id"]` |

Create and poll draw on **separate** budgets, for the same reason `exa:search` and `exa:answer` do: a poll retry must
not consume the create's retries, and a retry of one must not be answered from the other's plan.

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

The create response body is derived in full: the identifier, the vendor's initial status constant, and nothing else.
A scenario cannot script it, and that is a deliberate v1 limitation rather than an oversight — a projection body
alongside `turns:` is already a load error (`scenario.provider.body_with_turns`), so there is nowhere honest to put
create-side keys without adding a reserved envelope key. If one is ever needed, `create:` is additive and is the
migration path; it is not needed for either vendor today.

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
//     would collide them inside one namespace.
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

### 5.2 Replay

Given the same scenario and the same request sequence, everything is reproduced:

| Quantity | Why it repeats |
|---|---|
| the identifier | a pure function of `(seed, entry, lane key, create index)` |
| which turn answers poll N | the lane cursor is a position, not a clock |
| the rendered body | re-rendered from the scenario, never replayed from a stored copy |
| timestamps in the body | `Scenario.BaseTime()`, as everywhere else |

`Job.CreatedAt` is the one real-time field in the record, and it is never rendered — same rule as
`journal.Entry.ArrivedAt`. `testkit.AssertGoldenJSON` should add `id` and `request_id` on these surfaces to its
default ignore set for the same reason it already ignores `requestId`: the identifier folds in the call index by
design, so a golden that pinned it would fail on the second create.

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

The one change:

```go
// A GET pattern also serves HEAD (verified on Go 1.26.4: HEAD /agent/runs/{id}
// reaches the GET handler and returns 200 with an empty body). Advertising only
// GET would be a lie about a method this listener answers, and Allow is the
// header a consumer's client library reads to decide whether to retry.
for _, path := range slices.Sorted(maps.Keys(paths)) {
	allow := slices.Sorted(slices.Values(paths[path]))
	if slices.Contains(allow, http.MethodGet) && !slices.Contains(allow, http.MethodHead) {
		allow = append(allow, http.MethodHead)
		slices.Sort(allow)
	}
	…
}
```

Two hazards to keep in the mux test table rather than in a reviewer's head:

- `http.ServeMux` **panics** on conflicting patterns. `/agent/runs` and `/agent/runs/{id}` do not conflict, and the
  `/` catch-all loses to both on specificity, but the panic is at construction, so a future route that does conflict
  fails at startup rather than at request time. That is the right direction and should be asserted, not merely
  assumed.
- Percent-encoding. `GET /agent/runs/run%2Fabc` yields `PathValue("id") == "run/abc"`. Every route that feeds a path
  value into a lane key must validate it first; see [§7.1](#71-the-identifier-charset-is-load-bearing).

`provider/mux_test.go`'s table grows four rows: `GET /agent/runs/{id}` 200, `HEAD` 200, `POST` 405 with
`Allow: GET, HEAD`, and the percent-encoded identifier rejected.

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
identifier in the namespace would 404 while the create kept advancing. `admin.Deps` gains a `Jobs` field and
`resetNamespace` calls all three stores, in the order journal → faults → jobs, with the same "the wired store does
not isolate namespaces" refusal path the other two already have.

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

**Make the divergence self-diagnosing.** Because identifiers are *derived* rather than random, a replica that does
not hold an identifier can cheaply decide whether it is one this scenario *would* mint. Recompute the derivation for
create indices `0 … min(localCursor + window, MaxJobs)` in the presented lane and compare:

```go
// diagnoseForeignID reports whether id is one this scenario would mint in this
// lane but this process does not hold — which means another replica minted it,
// or a reset dropped it. It changes nothing about the RESPONSE, which is still
// the vendor's 404: it adds a named finding and one error-level log line, so an
// intermittent 404 names its own cause instead of looking like a consumer bug.
//
// The window is bounded by MaxJobs, so the cost is a bounded number of SHA-256s
// on a path that is already answering an error.
func diagnoseForeignID(x *Exchange, id string) bool
```

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
| `Allow` gains `HEAD` on GET paths | **Go**, in `NewMux` | none; no existing listener serves a GET route |

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
| **A2** | `provider`: `Deps.Jobs`, `Deps.MaxJobs`, `Route.LaneFrom`, `LaneFromPath`, `ValidJobID`, `MintJob`, `ResolveJob`, `turnLaneKey` and `hasDiscriminator` changes, the `Allow`/`HEAD` fix, mux table rows | A1 |
| **A3** | `provider/exa`: routes, request validation, `RunProjection`, render, error envelopes, validator findings | A2 |
| **A4** | `provider/tavily`: the same, plus per-route credential placement (see §12) | A2 |
| **A5** | `internal/admin` scoped reset across three stores, optional `GET /__admin/jobs`; `internal/server` `--max-jobs` wiring and the single-replica startup log | A1, A2 |
| **A6** | `testkit`: `Job`/`Jobs` aliases, `Sim.Jobs()`, a poll-sequence assertion, `examples/adapter` alias guard | A1–A5 |
| **A7** | `scenarios/protocol/async-job.yaml` (completed / failed / stuck), `docs/scenario-schema.md` async section, `contracts/exa/README.md` correction, README + troubleshooting multi-replica sections | A3, A4 |

A1 and A2 are the critical path; A3 and A4 are independent of each other.

---

## 12. Companion corrections this design depends on

Three already-verified defects block or contradict this work and are not re-litigated here — they are named so the
dependency is explicit:

1. **`contracts/exa/README.md` is wrong and must be corrected.** It states that `/agent/runs` is "not simulated: no
   C360 consumer uses it". The adopter's client at `src/pkg/agent/exa.go` calls it. The claim is false, the
   correction is required before this ships, and the paragraph that follows it — that the create-then-poll lifecycle
   "needs a different scenario shape than a single request/response projection" — is the part that was right and is
   what this document answers. (Backlog item #17.)
2. **Tavily currently returns 401 for a body-placed key** and raises `tavily.api_key.in_body`, so it would reject the
   adopter's real client. `POST /research` takes its credential **in the JSON body** and `GET /research/{id}` takes a
   **Bearer** header, so this surface cannot work at all until placement is resolvable per route. The minimal seam is
   a `Route.Credentials []string` default that an entry's `auth.headers` overrides; the full three-way credential
   matrix is its own design. (Backlog item #16.)
3. **The multi-replica warning is correct and undocumented.** [§8](#8-multi-replica-the-consequence-stated-explicitly)
   is the documentation, and it is a deliverable of this work rather than a note about it. (Backlog item #18.)

One pre-existing asymmetry is worth recording while this surface is being built, though fixing it is out of scope:
`Exchange.policy()` resolves the validation policy by *listener* name, so a second entry on one listener —
`perplexity_agent` today, `exa_agent_runs` tomorrow — silently inherits the primary entry's `validation:` block.
`authenticate` takes its entry explicitly and is unaffected. The async handlers above pass the entry explicitly
everywhere for this reason.
