# The framework seam: profiles out of tree, the four in-tree ones as examples

## Status

**Proposal, 2026-08-17, revised the same day after an independent critique. Awaiting the owner's decisions in
the last section.** Written against `main` @ `7318989` (Phase 8 merged; D9 tier 2 decided). It exists to turn the
owner's decision below into one shippable design, and it was produced from three audits (the exported-surface
gap, the 54 composition sites, the constraints and consumers), three independently written designs each backed by
a compile spike, three independent judgements of those designs, and one critique of the merged result. Where the
designs disagreed, this document resolves the disagreement by what the audits measured and the spikes compiled,
not by taste; where only the owner can resolve it, the question is in the last section with a one-line
recommendation.

The decision this serves, verbatim (owner, 2026-08-17):

> as soon as you try to take over mocks for sem\* it's a framework. not to mention the three providers we started
> with are not even close to the number of providers our early adopter will end up with. so yes, we need to
> commit to being a proper framework so we do not need to babysit PRs — treat what we have as examples.

Prior art this builds on: [`d9-framework-framing.md`](d9-framework-framing.md) (tier 2, "Evidence from Phase
8"), [`docs/adopter-backlog.md`](../adopter-backlog.md) rows D6/D9/D11, [ADR 0001](../adr/0001-single-repository.md),
[ADR 0002](../adr/0002-verified-contract-precedence.md), `CONTRIBUTING.md` "Adding a provider",
[`docs/design/mcp-profile.md`](../design/mcp-profile.md) §12.

## What the owner decided and what it implies

Four things follow from the sentence above, and each one constrains the design:

1. **Servicesim is a framework.** The chassis — `provider`, `scenario`, the journal, faults, redaction,
   composition — is the product. Two-thirds of the tree was already provider-neutral at D9 tier 1; the change
   is that the neutral core now has an *audience outside this module*, and rule 7's "consumers pay for every
   exported symbol" gets its reason without getting a licence on how much.
2. **The four in-tree profiles are reference examples.** `provider/{exa,tavily,perplexity,mcp}` stay verified,
   guarded and shipped, but they are held to exactly the discipline an outside profile is held to, through the
   same exported calls. Anything a reference profile can do that a third party cannot is a framework bug.
3. **No PR-babysitting.** A fifth, tenth, twentieth profile is written in another repository against this
   module's exported packages, with its own contracts, goldens and scenarios, and composed — with any of ours —
   into that team's own binary and image. Every house rule the framework can enforce must therefore be enforced
   *by construction*, because the CONTRIBUTING checklist and the reviewer's lens are the two things that
   disappear with the PR.
4. **v0.5.0 is held.** No adopter is on a release: `README.md:54-56` says `go get` on `v0.4.0` resolves to a
   module without `provider.MCP`, and the backlog records the adopter as blocked on the seam, not building
   against a pin. Every rename and signature change in this document is free today and a coordinated break for
   every consumer after the tag. **The reshaping is done once, before v0.5.0.**

## What an out-of-tree profile needs today and cannot get

Measured by AUDIT 1 (import survey of every non-test file in the four profiles and `testkit`, qualified-symbol
grep, `go doc` on every symbol). Three facts decide it:

- **No profile's non-test code imports `internal/faults` or `internal/jobs`.** Profiles import
  `internal/{wire, ids, httpx, journal}` only. `faults` and `jobs` appear in profile *tests* and in `testkit`.
- **Six exported `provider` declarations name an internal type**: `Deps.Journal journal.Journal`
  (`provider/deps.go:147`), `Deps.Jobs jobs.Store` (`:170`), `Exchange.Auth journal.AuthObservation`
  (`provider/exchange.go:41`, a *write* site — `provider/tavily/request.go:581`), `Exchange.Findings()
  []journal.Finding` (`:163`), `MintJob(...) (jobs.Job, bool)` and `ResolveJob(...) (jobs.Job, bool)`
  (`provider/jobs.go:213,301`). Five are reachable only through `testkit`'s aliases; `Exchange.Auth` is not
  reachable at all.
- **`testkit.NewFaults` is the hard blocker.** `testkit/server.go:205-206` calls `faults.New(s, routes())` where
  `routes()` (`:177-178`) is the hardcoded concatenation of the four in-tree `Routes()`. An out-of-tree route's
  `FaultKey` is never registered, so the seam raises `provider.CodeUnknownFaultKey` and **serves a clean 200
  where the scenario scripted a 429**. The consumer's resilience test passes vacuously. Two spikes reproduced
  this live before fixing it.

Ranked blockers for a fifth profile in another repository, exactly as AUDIT 1 ranked them:

| # | Blocker | Where |
|---|---|---|
| 1 | Fault engine — no exported constructor accepting the adopter's own routes; every out-of-tree route is fault-blind | `testkit/server.go:177-178,205-206` |
| 2 | `journal.Finding`/`Severity` — 3/4 profiles map findings onto vendor error bodies; out-of-tree that is impossible in a signature or degrades to `f.Severity == "error"` | `provider/exchange.go:163` |
| 3 | `httpx.Credential`/`ExtractCredentials`/`AddPlacement` — 4/4 profiles; `scenario.AuthPolicy.ExpectKey` is exported with no exported way to obtain the presented value; `Exchange.Auth` is a writable exported field of an unnameable type | `provider/exchange.go:41` |
| 4 | `wire.Render`/`Omit`/`MergeJSON` — 4/4 profiles, ~33 sites; carries rule 2's byte stability and rule 5's `ExtraFields`/`OmitFields`, which `scenario` exports with no exported consumer | `internal/wire` |
| 5 | `ids.Hex32`/`UUIDv5`/`Float` — 3/4; the only thing between a new profile and `math/rand` | `internal/ids` |
| 6 | `jobs.Job`/`Store` — `MintJob`/`ResolveJob` return an unnameable type | `provider/jobs.go` |
| 7 | No journal constructor outside `Start` — `testkit.NewJobs` exists, `NewJournal` does not | `testkit/server.go` |
| 8 | `testkit` cannot host a fifth profile at all — `allProviders():169`, `routes():177`, `validators():183`, the `switch` at `:452-458`, `enabled():505` | `testkit/server.go` |

And, from AUDIT 2, the composition layer is **54 name-keyed enumeration sites across 15 files** — `internal/config`
(10), `internal/server` (9, including a duplicated copy of `provider/mcp`'s unexported JSON-RPC envelope at
`listeners.go:265-286` and a `default: return nil` at `:350-356` that serves an *empty body* for any name the
switch does not know), `cmd`, `testkit` (8), `testkit/golden.go` (2), `contracts` (3 + 14 test loops),
`scenarios` tests (4 tables), `check-docs.sh` (5), image/compose/guards (8). Read positively: a profile is
`provider` + `scenario` + `wire` + `ids` + `httpx` + `journal.Finding`. `internal/{admin,config,server,redact}`
have **zero** references from any profile or from `testkit`.

## The design

The judges' winner is the minimal-export design (two of three judges outright; the third preferred registry-first
on a declared tiebreak whose objection — "there is no `Set`" — this document adopts as a graft). So the shape
is: **promote nothing, move the seam.** Every capability AUDIT 1 listed is delivered by widening `provider`, not
by making an internal package importable; one registration record, `provider.Profile`, feeds one ordered
`provider.Set` from which every enumeration site derives; and one root package exposes the composition entry
point a consumer's `main.go` calls with their own profile list.

Every mechanical claim below was compiled in at least one spike (`scratchpad/spike-minimal-export/`,
`spike-registry-first/`, `spike-consumer-first/`, each with a separate out-of-tree module). Go blocks are
**illustrative** — the code, once written, wins.

### The exported surface

| Import path | Status | Purpose |
|---|---|---|
| `github.com/c360studio/servicesim` | **new** (root) | Composition: `Main`, `Run`, `Build`. `cmd/servicesim` becomes a wrapper of it. |
| `.../provider` | widened | The profile-authoring seam plus `Profile`/`Set`. The one package a profile author reads. |
| `.../scenario` | unchanged | Already an open registry ("adding a provider must not require editing this package"). |
| `.../testkit` | generalised | Registry-driven; four `XHandler` constructors collapse to one; conformance helpers added. |
| `.../contracts` | genericised | `fs.FS`-driven; the closed `Provider` enum and the global `VerifiedOn` die. |
| `.../scenarios` | unchanged | The reference corpus (see built-ins below). |
| `.../profiles/{exa,tavily,perplexity,mcp}` + `.../profiles` | **moved** from `provider/<name>` (owner decision D-4) | Reference examples; `profiles.Reference()` is how our binary and docs opt in. |
| `internal/{admin,config,server,redact,wire,ids,httpx,journal,jobs,faults}` | **all stay internal** | Zero new importable packages for the six AUDIT 1 surveyed. |

#### `provider` — new declarations

```go
// illustrative — the code wins

// Render marshals v deterministically (HTML escaping off, json.Number fidelity), merges the
// scenario's extra fields, then drops the scenario's omitted ones — in that order, which is
// what docs/scenario-schema.md already promises. It is the framework's way to produce JSON
// bytes; Response.Body stays a public []byte (streams and non-JSON routes need it), so a
// profile CAN bypass it — see "Rule 2" below for what catches that.
func Render(v any, extra map[string]any, omit []string) ([]byte, error)

// Deterministic identifier derivations. None reads a clock or a random source.
func Hex32(parts ...string) string
func UUIDv5(parts ...string) string
func FloatIn(lo, hi float64, parts ...string) float64

const ContentTypeJSON = "application/json"

// Credential is one credential placement observed on a request. Value is the credential itself:
// compare it against scenario.AuthPolicy.ExpectKey and never journal, log or interpolate it.
type Credential struct{ Header, Scheme, Value string }

type Severity string
const ( SeverityWarning Severity = "warning"; SeverityError Severity = "error" )
type Finding struct{ Severity Severity; Code, Field, Message string }

// Refusal describes a framework-generated refusal a profile renders in its vendor's own error
// shape. X is nil for a refusal raised outside any request the profile could handle.
type RefusalKind string
const (
    RefuseNotFound         RefusalKind = "not_found"
    RefuseMethodNotAllowed RefusalKind = "method_not_allowed"
    RefuseScenarioUnknown  RefusalKind = "scenario_unknown"
    RefuseInternal         RefusalKind = "internal"          // a handler panicked
)
type Refusal struct{ Kind RefusalKind; Status int; Allow []string; X *Exchange }

// Profile is everything the framework needs to serve one simulated vendor. See below.
type Profile struct { ... }
func (p Profile) Validate() error
func (p Profile) Handler(d Deps) http.Handler
// Refuse takes the whole Refusal, because 405 needs Allow (provider/mux.go:123-130 sorts it)
// and RefuseInternal/RefuseScenarioUnknown differ in whether X exists. It fills Status from
// Kind when zero and journals refusal.empty_body if ErrorBody returned nothing.
func (p Profile) Refuse(r Refusal) []byte

// Set is an ordered, validated, immutable registry — the ONLY input to composition.
type Set struct{ /* unexported */ }
func NewSet(ps ...Profile) (*Set, error)
func MustSet(ps ...Profile) *Set                    // panics; for main() only, like regexp.MustCompile
func (s *Set) All() []Profile                       // registration order; returns a clone
func (s *Set) Names() []Name
func (s *Set) Lookup(name Name) (Profile, bool)     // the fail-closed hook: a miss is never served
func (s *Set) Routes() []Route
func (s *Set) Validators(only ...Name) map[string]Validator // keyed by entry KIND — see "Kind" below
func (s *Set) EntryKinds() []string
func (s *Set) DerivedIDs() (paths, streamPaths []string)
func (s *Set) LiveHosts() []string
func (s *Set) Faults(sc *scenario.Scenario, opts ...FaultOption) Faults // the ONLY fault-engine constructor

// FaultOption is the exported form of internal/faults.Option. Exactly the two the server
// passes today (internal/server/server.go:265-267); nothing else is promoted.
type FaultOption func(*faultConfig)
func WithFaultLogger(l *slog.Logger) FaultOption
func WithMaxNamespaces(n int) FaultOption

// Streams: the grammar set is opened (see "Rule 3" and unit 2). Grammar stays a string type
// with the two reference values as constants; the [DONE] behaviour keyed on it today becomes
// explicit data, so no framework branch is left that a third grammar can silently miss.
type Stream struct {
    Grammar  SSEGrammar   // journal metadata; REQUIRED non-empty, any value
    Sentinel []byte       // trailing frame written after Chunks; nil = none. Replaces OmitDone.
    SentinelPace time.Duration // replaces DonePace
    // Chunks, Usage, CostTotal unchanged
}
var DoneSentinel = []byte("data: [DONE]\n\n") // the chat-completions value; a profile sets it, the framework never infers it

// New methods on Exchange (no new top-level symbols)
func (x *Exchange) Credentials() []Credential
func (x *Exchange) ObserveCredential(c Credential)              // the only write path into the auth observation
func (x *Exchange) HasJSONContentType() bool
func (x *Exchange) AuthPolicy() scenario.AuthPolicy             // never nil; Profile.DefaultAuth then --strict-auth applied
func (x *Exchange) EntryFor(kind string) (*scenario.ProviderEntry, bool)
func (x *Exchange) Reject(status int, code, field, format string, args ...any) Response

// New finding codes
const CodeAttemptOnRejection = "fault.attempt_on_rejection"
const CodeHandlerPanic       = "handler.panic"
const CodeRefusalEmptyBody   = "refusal.empty_body"
const CodeStreamGrammarMissing = "stream.grammar_missing"
```

`Stream.Grammar` today is a two-value enum only by convention: `provider/stream.go:128,436` branch on `Grammar ==
GrammarDelta` and nothing validates the field, so an out-of-tree `Grammar: "my_dialect"` silently gets the typed
dialect's `[DONE]`-less framing — the silent-wrong-behaviour class rule 3 exists for, and the third grammar AUDIT
3 §6 item 5 names (OpenAI tool-call deltas) is the sem\* blocker. Per-frame framing (`event:` line, payload) is
already profile-rendered data through `SSEEvent`/`EncodeSSE`; the sentinel was the one grammar-keyed behaviour
left in the framework, and it becomes data too. `Handle` refuses a `Stream` with an empty `Grammar` as
`RefuseInternal` + `stream.grammar_missing`. What this does *not* open is the turn model (risk 7).

Changed signatures — free now, a break after v0.5.0:

```go
// illustrative
func MintJob(x *Exchange, entry, prefix string, encode func(...string) string) (id string, ok bool) // was (jobs.Job, bool)
func ResolveJob(x *Exchange, id string) bool                                                        // was (jobs.Job, bool)
func (x *Exchange) Findings() []Finding                                                             // was []journal.Finding
```

Verified: the in-tree profiles read only `job.ID` from `MintJob` (`provider/exa/agentrun_handler.go:113,123`,
`provider/tavily/research.go:222`) and discard `ResolveJob`'s job (`_, found :=` at four sites). The change costs
nothing and removes `jobs.Job` from the exported surface outright.

Removed from `provider`: `Exchange.Auth` (field becomes unexported; `ObserveCredential` is the only write path);
`MuxSpec.NotFound` and `MuxSpec.MethodNotAllowed` (replaced by the required `Profile.ErrorBody`); the four
constants `Exa`, `Tavily`, `Perplexity`, `MCP` (a framework core has no business naming four vendors); `<pkg>.New`
on each profile (replaced by `<pkg>.Profile()`). The canonical spelling that replaces the constants: **every
profile package exports `const Name provider.Name`** — `tavily.Name` and `mcp.Name` exist (untyped today; they
become typed), `exa.Name = "exa"` and `perplexity.Name = "perplexity"` are **added** (`go doc -short ./provider/exa`
has only `NameAgentRuns`; perplexity has `NameSonar`/`NameAgent`, which stay as *entry-kind* names). This also
settles AUDIT 2 #7: validator maps are keyed by entry kind, so `string(provider.Exa)` at
`internal/server/server.go:434` and `testkit/server.go:185` becomes `exa.Name` — the same spelling `tavily.Name`
and `mcp.Name` already use — and after unit 2 the maps are not hand-written anywhere. Consumer code such as
`ns.URL(provider.Exa)` (`testkit/server.go:775`) becomes `ns.URL(exa.Name)`.

`Deps.Journal` and `Deps.Jobs` **stay typed on the internal interfaces**. That is legal, invisible to
`go doc -short`, and provably usable from another module: `provider.Deps{Scenario: s, Faults: f}` compiles
out-of-tree, and the framework (`servicesim.Run`, `testkit.Start`) fills the two fields. Rule 4's guarantee is
that redaction happens at the retention boundary; not exporting the retention types is what keeps that a
property of the type system rather than of review. See owner decision D-2 for the production-sink question.

Two exported types are **owned by `provider`, not aliased**. The spikes measured why: `go doc -short ./testkit
Entry` today prints `type Entry = journal.Entry` with no fields, no methods and an unreachable target. Aliases to
internal types are a documentation dead end for exactly the types a profile must construct and inspect;
`Credential` and `Finding` are therefore provider-owned structs converted at the boundary.

#### The `Profile` registration record

Every field cites the AUDIT 2 enumeration site it replaces; a field that cannot cite one is not added.

```go
// illustrative
type Profile struct {
    Name        Name                 // #1-#54: listener identity, base-URL env prefix, journal "provider" field
    Kind        string               // handler implementation this profile is an instance OF; "" means Name (see below)
    Title       string               // #7 #21 #35 #52: "Exa", "Perplexity", "MCP" — flag usage, --help, docs index
    Summary     string               // #21: one clause for the --help banner
    Port        int                  // #1 #9 #47-#53: default listener port; 0 asks the OS

    Handlers    map[string]Handler   // #14 #28: Route.Pattern -> handler
    Routes      []Route              // #19 #24 #26 #44 #46 #49: every route with its FaultKey
    Validators  map[string]Validator // #20 #25 #38: keyed by scenario ENTRY KIND — N per listener (2,2,2,1 today)
    ErrorBody   func(Refusal) []byte // #16: REQUIRED — house rule 3
    DefaultAuth scenario.AuthMode    // #53: mode when a scenario entry declares none; "" = AuthRequired
    Announce    func(Deps)           // perplexity's Sonar sunset line, once per construction

    Contracts   fs.FS                // #32 #34 #36: goldens + provenance.yaml + README
    Hosts       []string             // #54: the vendor's real hostnames — never dialled, refused in fixtures
    DerivedIDs  []string             // #30: response paths derived per call, pruned in golden compares (rule 2)
    StreamDerivedIDs []string        // #31
    CredentialNames  []string        // rule 4: vendor credential header/property names, merged into redaction
}
```

`DefaultAuth` closes AUDIT 2 #53, the one registry row an earlier draft dropped. The default is **per profile and
opposite in-tree**: `provider/mcp/request.go:174-186` defaults an entry with no `auth:` block to `AuthOptional`
("deliberately the opposite default from the three research profiles (decision 3)"), while `provider/exa/request.go:155`,
`provider/tavily/request.go:609` and `provider/perplexity/request.go:204` default to `AuthRequired`;
`docker-compose.example.yml:84,119-121` documents exactly that split. Without a field, `x.AuthPolicy()` could not
be the single accessor it claims to be — it would either flatten MCP's contract decision 3 or need a fifth
hand-written switch. So: `AuthPolicy()` returns the entry's policy, else `{Mode: p.DefaultAuth}`; and
`internal/server`'s `relaxAuth` (`server.go:461-471`), which today applies `--strict-auth=false` by writing
`AuthOptional` onto every policy-less entry, is rewritten to consult the profile — strict-auth relaxes only a
profile whose default is `required`, and never touches an entry that declares its own mode. `NewSet` refuses a
`DefaultAuth` outside `{"", required, optional}`. The compose/README `<NAME>_API_KEY` rows (#53) are then a
generator output — emitted for a profile only when its default is `required` — not a documented convention. No
`CredentialEnv` field: `<NAME>_API_KEY` derives from `Name` exactly as `<NAME>_BASE_URL` already does.

`Kind` is the one forward-looking field, grafted from the registry-first design on the strength of AUDIT 3 §6:
the next real workload is an OpenAI-compatible `chat/completions` shape that must serve OpenAI, Ollama, Azure and
an in-house gateway as *distinct listeners with one handler*. `scenario.ProviderEntry.Kind` already exists and its
doc names `openai` and `openai_fallback`; only the registration half is missing. One field now, a break to
somebody else's registration later. Its semantics where it meets `Validators`, stated so unit 8 has something to
test against rather than invent:

- A scenario block is addressed by **`Name`** (`scenario.ProviderEntry.Name` "is the map key"); **`Kind` selects
  the handler and the validator** (`ProviderEntry.Kind` "selects the handler implementation. Defaults to Name").
  A second instance is `openai_fallback: {kind: openai, ...}` in the scenario and
  `Profile{Name: "openai_fallback", Kind: "openai", Port: 8091, ...}` at registration — same package, same
  `Handlers`/`Validators`, different `Name` and `Port`. `Exchange.EntryFor(kind)` resolves through the listener's
  `Name`, which is why it exists instead of the static `Route.Entry` string.
- `Set.Validators` merges by entry-kind key. Two profiles of one `Kind` contribute identical keys and that is
  accepted (they come from one `Profile()` and are the same function); the same key from two *different* `Kind`s
  is a `NewSet` refusal — two implementations claiming one scenario vocabulary is exactly the ambiguity that
  produced AUDIT 2 #7.
- **Instancing (`Kind != Name`) is limited to single-entry profiles in v0.5.0.** A multi-entry listener
  (perplexity's `perplexity` + `perplexity_agent`, exa's `exa` + `exa_agent_runs`) has secondary entry names that
  are today static strings in `Route.Entry`; how those rename per instance is not designed here, and `NewSet`
  refuses rather than guesses. The `chat/completions` shape is single-entry, which is the case that needs it.

Derived, never stored: `EntryKinds` = keys of `Validators` (#38, #41); the port flag `<name>-port` and env var
`SERVICESIM_<NAME>_PORT` (#5); `<NAME>_BASE_URL` and `<NAME>_API_KEY` (#29, #53 — the former already derived at
`testkit/server.go:526-546`); the `--providers` default and the "this build simulates …" error (#2,
`config:457-470`); the `--help` banner (#21).

Two shape constraints the existing code imposes and this design keeps:

1. **Ordered slice, explicit registration.** `internal/config/config.go:102-105` says why in the source: a map
   range would make listener order and every provider-listing message differ between runs. `NewSet(ps...)` is a
   value passed at the composition root — never `init()` side effects, because import order is not a stable
   order and a package-level registry is exactly the hidden mutable global rule 6 forbids.
2. **`NewSet` validates once and refuses.** Missing `ErrorBody`, `Handlers` or `Routes`; an empty `FaultKey`; a
   name that is not flag- and path-safe; duplicate names; duplicate non-zero ports; an unknown `DefaultAuth`; one
   entry kind claimed by two `Kind`s; instancing of a multi-entry profile. `testkit.Start` reports these through
   `tb.Fatalf`, not a panic.

#### How every AUDIT 2 site derives from the `Set`

| AUDIT 2 sites | Today | After |
|---|---|---|
| `config` #1-#10 (port consts, `DefaultProviders`, `allProviders`, four `Listener` fields, env table, `raw` fields, four `IntVar`s, `assemble`, port-range table, `listener()` switch) | 10 hand-maintained sites | `Config.Listeners map[Name]*Listener` + `order []Name`; one loop registers `-<name>-port`, one validates; `Listener(name) (Listener, bool)` — the miss stays the fail-closed hook |
| `server` #13 `newSurfaces`, #14 `newProviderHandler` | two 4-arm switches | `cfg.Listener(name)`; `set.Lookup(name).Handler(deps)` |
| `server` #15 duplicated JSON-RPC envelope, #16 `scenarioNotFoundBody` `default: return nil` | 90 lines, one a copy of `provider/mcp`'s unexported type | `p.Refuse(Refusal{Kind: RefuseScenarioUnknown})` — **deleted, including the duplicate**; `internal/server` imports no profile package |
| `server` #19 `slices.Concat(exa.Routes(), …)`, #20 `validators` switch | four packages named; inconsistent map keys | `set.Faults(sc)`; `set.Validators(cfg.Enabled()...)` — enabled-only validators, all-routes faults, two calls on one registry (the asymmetry AUDIT 2 flagged is kept deliberately) |
| `cmd` #21 banner | prose | `Title`/`Summary` |
| `testkit` #23-#28 (`allProviders`, `routes`, `validators`, `NewFaults`, four `XHandler`, `build` switch) | 8 sites | `set.Names()`, `set.Routes()`, `set.Validators()`, `set.Faults(sc)`, one generic `Handler(tb, name, opts...)`, `Lookup(name).Handler(deps)` |
| `golden` #30/#31 `derivedIDPaths` | three vendors' knowledge as a global default | `set.DerivedIDs()` |
| `contracts` #32-#34 embed, `Provider` enum, `Providers()` | closed | `contracts.Read(fsys, name)`, `Provenance(fsys)`, `Goldens(fsys)`, `OldestVerified(fsys)`; the enum and the global `VerifiedOn` die |
| `contracts` #35 `byName`, #36 twelve `Providers()` loops | in-tree tests | `Title`; `contracts.Conform(tb, fsys)` |
| `scenarios` #38 `implementedProviders`, #41 built-in coverage | seven entry kinds, closed corpus | `set.EntryKinds()`; `testkit.AssertCovers(tb, fsys, kinds)` — in `testkit`, not `scenarios`, because `scenarios` is on the binary's import graph (`internal/config/config.go:16`, `internal/server/server.go:27`) and a `testing.TB` signature there would put `testing` in every production binary |
| `scenarios` #39 `documentedProjectionKeys` | hand-mirrored table | `Validator.ProjectionKeys()` — an engineering call, not an owner one: four in-tree implementations, zero external, free now, a break to somebody else's type after |
| `check-docs` #44 §3 routes, #46 §6 both directions | `provider/*/*.go` glob | `servicesim --print-routes` driven by `set.Routes()` — works against a consumer's binary too; direction 1 (`check-docs.sh:420-429`, "index table claims a route no provider registers") keeps its meaning with the route list coming from the binary instead of the glob |
| image/compose #47-#53 | hand-written ports, env rows and curls | `--print-ports` emits `Name`/`Port`/`DefaultAuth` as JSON; `Dockerfile` `EXPOSE`, compose `ports:` and the `<NAME>_BASE_URL`/`<NAME>_API_KEY` rows are generated. **The smoke probes are not** — `scripts/image-smoke.sh:114-205` is twelve probes with bodies, a headers file, content-type assertions and MCP's `supportedVersions` block, and route patterns cannot generate those; they stay hand-written until `Route.ExampleRequest` (deferred, see "not exported") |
| `lint-no-live-hosts` #54 | hand-edited regex that MCP never joined | pattern = a hand-kept base list **∪** `--print-hosts` (union of `Profile.Hosts`); the check becomes `testkit.AssertNoLiveHosts` a consumer runs over their own trees. See rule 3 below for why the base list is not deleted |

After the spikes' equivalent of the composition units, the only four-name enumerations left in non-test Go are
`cmd/servicesim/main.go` and `profiles.Reference()` — two registration lists, which are the point. Add a
two-line test that they are equal.

#### `servicesim` — the composition root

```go
// illustrative
type Build struct{ Program, Version, Commit, BuiltAt string }
func Main(b Build, set *provider.Set) int
func Run(ctx context.Context, b Build, set *provider.Set, args []string,
         lookupEnv func(string) (string, bool), stdout, stderr io.Writer) int
```

`Build.Program` exists because a spike's adopter binary printed `servicesim simulates OpenAI chat/completions,
MCP` from `--help` — right about the profiles, wrong about the binary. `Version`/`GitCommit`/`BuildTime` move
from `main` to the root package: every `-ldflags "-X main.Version=…"` in `Dockerfile`, `Taskfile.yml` and CI
becomes `-X github.com/c360studio/servicesim.Version=…`, and `scripts/image-smoke.sh` gains a `--version`
assertion in the same unit (`cmd/servicesim/main.go:22-26` already warns that renaming this symbol silently reverts
every release binary to `dev`).

`cmd/servicesim/main.go` becomes:

```go
// illustrative
func main() {
    os.Exit(servicesim.Main(servicesim.Build{Program: "servicesim", Version: version, ...},
        provider.MustSet(profiles.Reference()...)))
}
```

#### How a consumer composes a binary and an image

Their `main.go` is the same shape naming their profiles plus any of ours; their `Dockerfile` is their own
two-stage build over that binary; `EXPOSE`, the compose port map and the env rows come from `--print-ports`; the
smoke probes are theirs to write, as ours are (`scripts/image-smoke.sh` is the worked example). Verified in all
three spikes with a separate module and a `replace`: `--help` lists `-acme-port` (default 8090) and `--providers`
default `acme,exa,tavily`; `POST /v1/answer` serves a byte-identical 200 across `/`, `/n/<ns>/` and
`/x/<scenario>/n/<ns>/`; a wrong path, a wrong method (with `Allow: POST`) and an unknown scenario all answer in
the profile's *own* error shape; a scripted `[{status: 429}, {}]` produced 429 then 200 with no
`fault.unknown_key`; `/__admin/requests` records `"Authorization":["Bearer [REDACTED]"]`,
`{"api_key":"[REDACTED]"}` and two fingerprints, and `grep -c` for the raw secret over the journal returns 0 —
from a profile whose author wrote no redaction code.

Ours keeps shipping unchanged: `cmd/servicesim` registers `profiles.Reference()`, the image still binds
8080–8084.

#### `testkit`

```go
// illustrative
func WithProfiles(ps ...provider.Profile) Option           // REQUIRED — see owner decision D-5
func Handler(tb testing.TB, name provider.Name, opts ...Option) (http.Handler, *Sim)
func NewJournal() Journal                                  // closes AUDIT 1 gap 7 for hand-built Deps
func ValidateProfile(tb testing.TB, p provider.Profile)    // the conformance suite an adopter runs in CI
func AssertDeterministic(tb testing.TB, p provider.Profile, scenarioYAML string, reqs ...*http.Request)
func AssertRenderShape(tb testing.TB, bodies ...[]byte)     // the two encoding/json divergences wire's doc names
func AssertNoLiveHosts(tb testing.TB, fsys fs.FS, skip []string, hosts ...string) // pass os.DirFS("."): Go source included
func AssertNoCredentialInJournal(tb testing.TB, sim *Sim, secret string)
func AssertCovers(tb testing.TB, fsys fs.FS, kinds ...string) // every scenario file declares every kind
func GoldenDerivedIDs(paths ...string) GoldenOption
```

Removed: `ExaHandler`, `TavilyHandler`, `PerplexityHandler`, `MCPHandler` (`testkit/server.go:357-391`) — four
compatibility obligations for what `Handler` does in one, and the form an out-of-tree profile can never join
because `scripts/check-docs.sh` §4 (`:260-280`) forbids docs naming a `testkit.X` that does not exist.
Removed: `testkit.NewFaults` — `(*Set).Faults` is the only constructor, so a caller cannot obtain an engine that
does not know the composed route set. Removed: `testkit.Finding`/`Severity` and their consts, in favour of
`provider.Finding`/`Severity` (one name per concept). Kept: the journal/jobs aliases the assertion signatures
are written in (`Entry`, `Journal`, `Stats`, `Outcome*`, `Stream*`, `AuthObservation`, `Job`, `Jobs`,
`JobStats`) — a test-time surface whose completeness `examples/adapter` already guards
(`docs/design/package-design.md:204-208`) — with their doc comments rewritten to **inline the field list**, since
`go doc` cannot follow them.

#### `contracts`

`Read`, `Provenance`, `Goldens` take an `fs.FS`; `OldestVerified(fsys)` replaces the global const `VerifiedOn`
("the oldest per-entry date across every provider" — a global a framework cannot compute over profiles it does not
know); `Conform(tb, fsys)` is the nine `Providers()`-iterating tests as one call. `Reference()` is *not* a
`contracts` symbol: a profile's contract bundle is `Profile.Contracts`, so "ours" is `profiles.Reference()` and the
in-tree contract tests iterate `p.Contracts` over that list — one enumeration, not two. `contracts.Provider` and
its constants (a second name enum parallel to `provider.Name`), `Providers()` and `FS()` are deleted. `Kind`,
`Record`, `Spec` stay — they are ADR 0002's vocabulary and what lets an out-of-tree profile mark a body
`simulator-chosen`.

#### The four reference profiles

They become `profiles/{exa,tavily,perplexity,mcp}` (owner decision D-4; recommendation: move), each with one new
file, `profile.go`, holding `Profile()` — the refusal bodies move out of `internal/server/listeners.go:299-357`
into it. They keep every guard they have and gain two: a **no-privilege import test** on the
`examples/imports_test.go:29-62` pattern (nothing under `provider/`, `internal/`, `testkit/`, `scenario/`,
`contracts/` or the root may import `profiles/`), and one `conformance_test.go` per profile calling the same
`testkit.ValidateProfile` an adopter calls. Their `doc.go` decision logs are the deliverable —
`provider/mcp/doc.go` is the model for a contract — and `docs/building-a-profile.md` names them as the four
worked examples in order of complexity: tavily (one route, body-placed credential via `ObserveCredential`), exa
(multi-route + async create/poll), mcp (a protocol, non-JSON content type, JSON-RPC refusal envelope),
perplexity (two scenario entries and two SSE grammars on one listener). `CONTRIBUTING.md` "Adding a provider" is
rewritten twice: "adding a reference profile here" and — the important one — "writing a profile in your own
repository", worked against the compiled `examples/profile/` module.

#### Contracts, goldens and built-ins for out-of-tree profiles

- **Contracts and goldens: theirs**, embedded beside their package (`//go:embed contracts/*`), placed on
  `Profile.Contracts`, checked by `testkit.ValidateProfile` in their CI. Verified failing-then-passing in a
  spike: `golden happy.json has no provenance record`, green once a real `provenance.yaml` with
  `kind: vendor-documented`, `documentation_url` and `verified:` was written. That is ADR 0002 running in
  someone else's CI.
- **Built-in scenarios: reference-only.** `scenarios/embed.go` is a closed `embed.FS` and
  `TestBuiltins_CoverEveryImplementedProvider` (`scenarios/scenarios_test.go:145-163`) plus three more guards
  require every built-in to declare a block for every implemented entry kind. `--scenario builtin:happy` will
  never cover profile #5 without a PR here, and all three designs reject an overlay: `builtin:happy` must mean
  the same bytes in every build. An adopter ships their own scenario files (the spikes' were nine lines) and
  validates them with the already-exported `provider.ValidateScenario` plus `testkit.AssertCovers`. A new
  startup warning, `scenario.profile.unscripted`, names any registered profile with no block in the loaded
  scenario, once — turning the silent empty answer into a diagnosis.
- **Guards a consumer runs in their CI** — the in-tree discipline as library helpers:

| In-tree guard today | Library form |
|---|---|
| nine `contracts_test.go` + three `provenance_internal_test.go` `Providers()` loops | `contracts.Conform(tb, fsys)`, inside `testkit.ValidateProfile` |
| registration completeness (four switches) | `Profile.Validate()` / `NewSet` |
| `TestMaliciousContent_CredentialBaitNeverReachesTheJournal` (`scenarios_test.go:654`) | `testkit.AssertNoCredentialInJournal` |
| `TestBuiltins_UseReservedHostsOnly` + `lint-no-live-hosts.sh` | `testkit.AssertNoLiveHosts`, seeded from `Profile.Hosts` |
| `TestBuiltins_CoverEveryImplementedProvider` | `testkit.AssertCovers(tb, fsys, kinds)` |
| the eleven `CONTRIBUTING.md:87-107` conventions | `testkit.ValidateProfile`: unknown path → own 404 shape + `route.unmatched`; wrong method → 405 + `Allow`; wrong content type → finding; missing credential under strict auth → 401; every `FaultKey` resolvable; a rejection followed by a valid request receives attempt 0 |
| determinism (nothing today) | `testkit.AssertDeterministic` — two fresh Sims, same requests, byte-compare; a **required** step of `ValidateProfile` so opting out is visible |
| byte fidelity (`internal/wire`'s package doc, enforced today by every profile calling `wire`) | `testkit.AssertRenderShape` over every conformance response and golden — a heuristic for the two named divergences, inside `ValidateProfile` |
| refusal bodies (`image-smoke.sh` asserts ours by hand) | `ValidateProfile` calls `ErrorBody` for all four `RefusalKind`s, with and without an `Exchange`, and requires a non-empty body |
| `check-docs.sh` §3, §6 both directions | `servicesim --print-routes` |

`check-docs.sh` §1 (flags), §2 (built-ins), §4 (symbols) and §5 (published image references, `:290-300`) stay
repo-local; they check our prose against our binary and our registry. §5 is the one unit 11's release step leans
on — publish first, then update the docs — and it is unchanged by any of this.

## House rules by construction

**Rule 2 — determinism.** `Hex32`/`UUIDv5`/`FloatIn` are the exported derivations and read no clock;
`(*Set).Faults` is the only engine constructor, so an engine that does not know an out-of-tree route is
unconstructible — the fix for the quietest failure in the repo, reproduced live in two spikes; `DerivedIDs` is
registry data so a foreign `foo_request_id` is pruned like `requestId`; registration is an ordered value, not
`init()`; `AssertDeterministic` is the mechanical defence against `time.Now()` in somebody else's handler. Those
are by construction. **The byte-fidelity half is not, and this document says so rather than claiming it**:
`provider.Response.Body` is a public `[]byte` (`go doc ./provider Response`) and must stay one — streams carry
pre-encoded chunks and MCP's non-JSON route needs it — so an out-of-tree profile can `encoding/json.Marshal` and
ship `\u0026` for `&` and `1e+06` for `1000000`, the two divergences `internal/wire`'s package doc exists to
prevent. Both are perfectly deterministic, so `AssertDeterministic` cannot see them. What the framework can do is
detect them after the fact: `testkit.AssertRenderShape` fails on a JSON body containing the `\u003c`/`\u003e`/
`\u0026` escapes (the signature of default `json.Marshal`) or an exponent-form literal for an integral value (the
signature of a `float64` round-trip), and `ValidateProfile` runs it over every response the conformance requests
produce and every golden in `Profile.Contracts`. That is a heuristic, not a proof; `Render` is the answer that
needs no heuristic, and `docs/building-a-profile.md` says why in the sentence that introduces it.
And `Render(v, extra, omit)` settles a **verified in-tree divergence** — but not as an owner decision, because the
schema already decided it: `docs/scenario-schema.md:452-454` says "the merge happens first and the omission
second, so `omit_fields` can remove a key `extra_fields` added". `provider/tavily/render.go:315-317` complies;
`provider/exa/response.go:73-79` does the opposite and says so in its doc comment ("so an extra field can
reinstate a key that was omitted"). That is a documented-behaviour bug in exa, fixed in unit 1 and release-noted;
one signature then makes a third ordering unrepresentable.

**Rule 3 — fail closed, never proxy.** `Profile.ErrorBody` is required and `NewSet` refuses without it. The
gap it closes is precise: `provider/mux.go:106-124`'s existing defaults *do* record a finding and set `Allow`,
but the body is empty; `internal/server/listeners.go:350-356` returns `nil` for any unknown name; and
`provider/handle.go:222` re-panics after journaling, so a profile's nil dereference reaches the client as a
connection reset with no status and no body (verified: HTTP 000). With `ErrorBody` guaranteed, all three become
vendor-shaped — 404, 405, unknown-scenario, and a `RefuseInternal` 500 with a `handler.panic` error finding. The
duplicated MCP envelope in `internal/server` is deleted. `Refuse` takes the whole `Refusal` so 405's `Allow` and
the nil-`X` cases are expressible, and `ValidateProfile` calls `ErrorBody` for all four kinds with and without an
`Exchange` and requires a non-empty body each time. The SSE grammar set is opened (declarations above): the
`Grammar == GrammarDelta` branches at `provider/stream.go:128,436` were the last framework behaviour keyed on a
closed enum a profile could miss silently, and an empty `Grammar` is now a refusal, not a fallback. `Hosts` +
`AssertNoLiveHosts` make the reserved-domain guard travel — with two things the earlier draft got wrong. First,
`scripts/lint-no-live-hosts.sh:24` is a hand-edited regex that gained no MCP row in Phase 8 *and* already names
`api\.openai\.com` although no OpenAI profile exists; a pattern generated purely from `Profile.Hosts` would delete
the one vendor the sem\* workload is about to need. The pattern is therefore the base list **∪** `--print-hosts`,
and the base list keeps the paid hosts we know about whether or not a profile names them. Second, the script scans
`SEARCH_PATHS=(scenarios provider scenario testkit internal cmd)` (`:26`) — Go source deliberately, because "a
real hostname in a code comment is still a footgun for the next reader who copies it into a default" (`:31-33`).
An adopter's `baseURL = "https://api.acme.com"` default lives in their Go, not their fixture FS, so
`AssertNoLiveHosts` is documented to be called with `os.DirFS(".")` over the module root, and skips only the
paths the caller lists — their contracts directory, whose provenance records name real documentation URLs,
exactly as the script exempts `docs/` and `contracts/` today. "Never dials outward" is already structural —
`Handle` is the only entry point and no framework path calls a profile-held client.

**Rule 4 — credentials never survive a round trip.** Redaction stays below the seam: `internal/redact` is
unreachable, `Ring.Append` redacts at storage, `Handle` redacts before logging, and no exported function accepts a
redaction policy. Two closures: `Exchange.Auth` becomes unexported so the last profile-side write path into a
retained structure (`provider/tavily/request.go:581`) is replaced by `ObserveCredential`, which can only add a
placement and a fingerprint; and `Profile.CredentialNames` makes the credential-name *vocabulary* profile-declared
data merged at composition — the class of change Phase 8 had to make in-tree for `Mcp-Param-Token` — while the
masking stays non-optional with no flag. `Credential.Value` is the one credential-bearing value handed to a
profile, because `scenario.AuthPolicy.ExpectKey` is exported and something must compare against it; no path
leads from it to retention except `ObserveCredential`. `CONTRIBUTING.md:105-107`'s review lens becomes a field
plus `AssertNoCredentialInJournal`.

**Rule 5 — strict about requests, and closing the memoised-fault gap in the framework.** No framework can force
a handler to reject a malformed request; what it can stop is a rejection being served as something else. Three
mechanisms rely on handler convention today and become construction:

- *The memoised fault decision* (`provider/handle.go:254-267`; backlog `:899-905`; `CONTRIBUTING.md:89-91`
  "Validate before you claim"). `:259` clears `FaultEligible` when the handler failed, `:267` reads `x.decision`
  unconditionally, so an attempt claimed before validation failed is still applied — the rejection wears a
  scripted 429 and the consumer cannot prove it sent a wrong request. Fix, spiked, six lines: if the response is
  not fault-eligible and a decision was claimed, zero the decision (`Index: -1`), keep the handler's status, and
  record `fault.attempt_on_rejection`. All existing tests pass with it, which was read at spike time as
  "latent, not reachable today" — that reading was wrong: `SelectTurnFor` claims via `CallIndex` and then fails
  `scenario.no_matching_turn`, and `MintJob` claims unconditionally and then fails `job.limit_reached`, so every
  shipped handler that uses either one takes this exact path on an ordinary unmatched-turn or over-limit request;
  the existing suite simply had no test exercising it. An out-of-tree test proves the pre-fix code served the 429.
  **This ships first, as unit 0, before any seam work.** Unit 0's actual implementation keeps `Index` and `Key` as
  claimed rather than zeroing them (`Attempt` alone is cleared), because testkit's namespace-isolation and
  attempt-budget assertions read the journal's `Outcome.FaultKey`/`Outcome.AttemptIndex` and a zeroed, unnamespaced
  `Index: -1` misreports which lane and which counter slot were actually drawn on. The retry *budget* half — the
  claimed index is still spent — is warn-only in unit 0; a CAS release of the claimed lane is an implementation
  choice that touches the lane counter under concurrency and belongs behind a `-race` spike with two concurrent
  requests, so it waits until a real out-of-tree profile trips the warning. An engineering call, recorded here,
  not an owner question.
- *`Response.FaultBody` is called for a no-op attempt.* Verified: `provider/fault_exec.go:307` calls
  `resp.FaultBody(*a)` for any executing attempt, and `exa/errors.go:119`, `tavily/errors.go:193` and
  `perplexity/errors.go:104` each re-implement `if a.Status < 400 { return nil }` — three profiles independently
  re-implementing a framework invariant, and `Response.FaultBody`'s doc ("called only when an attempt actually
  applies") says the opposite of the code. A spike reproduced HTTP 200 carrying a 4xx-shaped body on a
  fifteen-line out-of-tree profile. The guard moves into `fault_exec.go`; the three duplicates are deleted.
- *Nil traps.* A spike wrote an out-of-tree handler the way a new author would and got two consecutive nil
  dereferences — `x.Entry()` returns a nilable `*scenario.ProviderEntry`, `ProviderEntry.Auth` is a nilable
  `*AuthPolicy`. Hence `x.AuthPolicy()`, `x.EntryFor()`, and `x.Reject()` so the correct rejection is also the
  shortest thing to type. `--providers` with an unregistered name still errors and `testkit` still fatals, sourced
  from the registry.

**Rule 6 — process isolation over shared mutable state.** No `Register()`, no `init()`, no package-level
registry; `Set` is immutable and `All()` clones. **The admin surface stays framework-owned and closed**:
`Profile` has no admin-route field and `Run` accepts none. This is the one place "no PRs to babysit" and a house
rule genuinely conflict — `CONTRIBUTING.md:179-180`'s review gate against a scenario-mutating admin route
disappears with the PR — and the design resolves it for rule 6 (owner decision D-3 ratifies). D3's "no replicas,
ever" is unchanged and still announced at startup.

**Rule 7 — consumers pay for every exported symbol.** Zero new importable packages for the six AUDIT 1 surveyed;
one registration record; provider-owned value types rather than aliases that print `journal.` in godoc;
reductions in the same break: −4 `XHandler`, −4 `<pkg>.New`, −2 `MuxSpec` fields, −4 `provider.Exa…`
constants, −1 `testkit.NewFaults`, −2 `testkit.Finding`/`Severity`, −8 `contracts` symbols, −6 internal types in
exported signatures. Measured delta of the winning spike: `provider` +13 top-level declarations and 2 signature
changes; `testkit` net −2; root +1 function — against +50/+57 for the alternatives. The critic's additions in
this revision (`MustSet`, `FaultOption` + 2 constructors, `DoneSentinel`, 2 finding codes, `Profile.DefaultAuth`,
`Stream.Sentinel`/`SentinelPace` replacing `OmitDone`/`DonePace`) bring `provider` to roughly +20 net, all
cited to a site or a house rule. **That accounting excludes the reference profiles**, and it should not pass
without saying so: `go doc -short ./provider/exa` is 46 top-level declarations, tavily 44, perplexity 84, mcp 19,
and after the move to `profiles/<name>` every one of them stays a pinned compatibility obligation for a consumer
who imports one reference profile. Unit 5 therefore includes a per-profile trim to what `Profile()` and a
consumer's test need to name (`Name`, entry-kind names, `Validator`, the request/response types a golden test
decodes) — measured before and after with `go doc -short`, and recorded in each `doc.go`.

## Migration in units

Each unit is independently green (`task check`) and independently revertible.

| # | Scope | Definition of done |
|---|---|---|
| **0** | The memoised-fault gate: `provider/handle.go`, `CodeAttemptOnRejection`, a table test that a handler which claims then rejects (or opts out of faults) gets its own status and body, the journal keeps the claimed index and namespaced key truthfully (only the attempt is cleared — the counter really was drawn on), and the finding is recorded | New test fails on `main`, passes after; every existing test unchanged. **Independent of every other decision — merged first (`phase-10`).** Review showed the gap is not latent: `SelectTurnFor` and `MintJob` claim before they can know the request will be rejected, so every profile reached it via `scenario.no_matching_turn`. |
| **1** | `provider`: `Render`, `Hex32`, `UUIDv5`, `FloatIn`, `ContentTypeJSON`, `Credential`, `Severity`, `Finding`; `Exchange.Credentials`/`ObserveCredential`/`HasJSONContentType`/`AuthPolicy`/`EntryFor`/`Reject`; `Findings()` → `[]Finding`; unexport `Exchange.Auth`; `MintJob` → `(string, bool)`, `ResolveJob` → `bool`; the four profiles rewritten off `internal/{wire,ids,httpx,journal}`; exa's extra/omit order fixed to what `docs/scenario-schema.md:452-454` documents | **No non-test file under the four profiles imports `servicesim/internal/…`** (`go list -deps`), and — **the full test suite compiles and passes**. The winning spike ran vet excluding in-tree `_test.go`; this unit's DoD is the measurement that spike skipped. A test that a key both omitted and re-added by `extra_fields` is absent from exa's body, release-noted. |
| **2** | `Profile` (incl. `DefaultAuth`, `Kind` stored but not yet threaded), `Refusal`, `RefusalKind`, `Set`, `NewSet`, `MustSet`, `Validate`, `Handler`, `Refuse(Refusal)`, `(*Set).Faults` + `FaultOption`/`WithFaultLogger`/`WithMaxNamespaces`; `MuxSpec` loses `NotFound`/`MethodNotAllowed`; each profile gains `Profile()` and a typed `Name` (exa and perplexity add theirs) and loses `New`; the four `provider.Exa…` constants deleted; refusal bodies move out of `internal/server`, the duplicated envelope deleted; `Status < 400` guard into `fault_exec.go`; panic → `RefuseInternal` 500; `Stream.Sentinel`/`SentinelPace` replace `OmitDone`/`DonePace`, empty `Grammar` refused, perplexity's renderer sets `DoneSentinel`; `relaxAuth` consults `DefaultAuth` | **An out-of-tree profile compiles, is registered and serves** — this is the unit at which `provider/mcp` builds against exported packages only, proven by all three spikes (`go list -deps ./provider/mcp` → no `internal/` for the minimal-export shape). `NewSet` refuses missing `ErrorBody`, duplicate names and ports, an unknown `DefaultAuth`, one entry kind from two `Kind`s, and instancing of a multi-entry profile, each with a test. A `- {}` attempt serves the scenario body at 200. A `Stream{Grammar: "tool_call_delta"}` with no `Sentinel` streams its chunks and nothing after; with `Grammar: ""` it is refused. MCP's no-`auth:` entry is still optional under `--strict-auth` (default), and `--strict-auth=false` still relaxes exa. |
| **3** | `internal/config` + `internal/server` derived from `*Set`; root `servicesim` package with `Main`/`Run`/`Build`; `cmd/servicesim` a wrapper; ldflags paths in `Dockerfile`, `Taskfile.yml`, CI; `--print-routes --print-ports --print-hosts`; `scenario.profile.unscripted` | `internal/{config,server}` import no profile package; `--help` derives from the set; `image-smoke.sh` passes and asserts `--version`; an out-of-module `main.go` composing one foreign profile plus two of ours builds and serves (all three spikes). **After this unit a consumer composes their own binary and image.** |
| **4** | `testkit`: `WithProfiles`, `Handler`, `NewJournal`, `GoldenDerivedIDs`, `AssertCovers`, registry-driven `build`/`enabled`/pruning; delete the four `XHandler`, `NewFaults`, `Finding`/`Severity` aliases; inline field docs on the remaining aliases | `testkit` imports no profile package; `check-docs` §4 green; the out-of-tree module's tests use `testkit.Start(WithProfiles(...))` and pass (`AssertNoFindings`, `AssertNoCredentialLeak`, scripted 429/429/200). **After this unit an out-of-tree profile is testable.** |
| **5** | Move the four to `profiles/<name>` (per D-4), `profiles.Reference()`, the no-privilege import test, the two-registration-lists-equal test; the per-profile exported-surface trim (rule 7 above) | Nothing outside `profiles/` and `cmd/servicesim` imports a profile; `check-docs` §3 glob rewired; `go doc -short` before/after per profile recorded in its `doc.go`. |
| **6** | `contracts` over `fs.FS`; delete `Provider`, `Providers()`, `VerifiedOn`, `FS()`; `Conform`; `testkit.ValidateProfile` incl. `AssertDeterministic`, `AssertRenderShape` and the four-kind non-empty `ErrorBody` check; one `conformance_test.go` per reference profile | `ValidateProfile` fails on a contract dir missing provenance (proved in a spike), on a profile whose body carries `\u0026` (a deliberately `json.Marshal`-ed test profile), and on an `ErrorBody` returning nil for one kind; passes on all four. |
| **7** | Guards as libraries: `AssertNoLiveHosts`, `AssertNoCredentialInJournal`; `Profile.Hosts` populated for the four; `lint-no-live-hosts.sh` pattern = its base list ∪ `--print-hosts` (adding the missing MCP row, keeping `api.openai.com`), Go source still scanned; `Profile.CredentialNames` merged into `internal/redact` | Each helper has a test that fails on a deliberately broken input; a profile-declared credential name is masked without touching `internal/redact`; the script's effective pattern still matches `api.openai.com` and now matches every `Hosts` entry. |
| **8** | `Profile.Kind` threaded through config/server/testkit (per D-1) with the semantics stated above; `Exchange.EntryFor` resolves through the listener `Name`; `Validator.ProjectionKeys()` | Two profiles of one `Kind` on two ports serve from one handler, each reading its own scenario block by `Name`; a scenario `openai_fallback: {kind: openai}` validates against one registered validator; `documentedProjectionKeys` derived. |
| **9** | `examples/profile/` — its own module with a `replace`, an out-of-tree profile, `main.go`, a `testkit` test and an `imports_test.go`, wired into `task check`; `docs/building-a-profile.md` whose code blocks are that module | CI proves "a fifth profile is buildable out-of-tree" instead of claiming it. `examples/doc.go:5-9`: an example that is not built rots. |
| **10** | Docs: ADR 0001 amendment; ADR 0003 "the framework seam"; `CONTRIBUTING.md` split; `CLAUDE.md:132` re-earned (per D-6); README composition section; `docs/design/package-design.md` level model | `check-docs` green; every guard's new home documented. |
| **11** | Release v0.5.0 per `CONTRIBUTING.md:159-169` — tag, confirm it resolves, then pins in a follow-up commit | Both tag spellings and the digest written down from the registry, not assumed. |

Units 0–4 are one API break and one milestone; 5–8 the second; 9–10 the proof and the docs; 11 last.
Spike-measured blast radius of the composition units: test compile breakage confined to `internal/config`,
`internal/server`, `cmd/servicesim` and `testkit` in both the registry-first and consumer-first spikes; the
profiles, `contracts`, `scenario`, `scenarios` and `examples` passed unchanged there. The minimal-export shape adds
the profile rewrite of unit 1, which is why its DoD demands the full-suite measurement.

## Compatibility and versioning

**v0.5.0 becomes the framework release, and the whole break lands in it.** Its note: the seam is public, profiles
are buildable out of tree, the four shipped ones are examples, every exported name that had to move moved once.
ADR 0001's own negative consequence (`:80` — a change to one provider's handler is a release every consumer
sees) is exactly why an incremental post-tag reshaping would be a coordinated break for everyone, repeatedly.

**Explicitly pre-1.0, with a testable trigger.** `Profile` has sixteen fields and none has been through a third
party's hands; AUDIT 3 §6 lists four capabilities the sem\* `chat/completions` workload needs. One — SSE framing,
where a third grammar silently inherited the typed dialect — is closed above by making the sentinel data and the
grammar open. Three remain and are turn-model, not framing: a turn key counting array elements matching a
predicate; a substring predicate folded over `messages[]`; a response projecting a field from the request's own
body. Each is plausibly an API change. Write the 1.0 condition down in the README: **at least one profile written
by someone who has not read this repository has shipped and survived a framework minor release without source
changes, and the `chat/completions` profile has landed** (where it lands is D-11). Until then a v0.x pin means
"the seam may still move".

**Module split: no; amend ADR 0001.** Its listener reasoning (`:32-40`, Exa and Tavily both serve `POST /search`)
stands and is *strengthened* — an open profile set makes path collisions more likely, and one listener per profile
is what makes the open set safe; `Profile.Port` makes the allocation a registration input. Its consumer-pays
argument (`:29-31`) stands and is the reason not to split. Two sentences are withdrawn: the inference from "one
scenario schema, one process" to "in one repository" (`:21-24`) — one process can load profiles from several
modules, and `scenario` was already an open registry that needed no change for MCP (`CONTRIBUTING.md:48-49`); and
the version-skew paragraph (`:25-28`) becomes an accepted cost with a stated mitigation — the framework module
carries the chassis *and* the reference examples, so skew is one edge (framework ↔ third-party profile), not N².
The Status paragraph's claim that D9 tier 2 "leave[s] the decision untouched either way" (`:6-9`) is now false and
must say so, with tier 3's module question recorded verbatim as open. Mechanically stale too: the listener table
(`:47-54`) omits MCP:8084. Use ADR 0002's dated "Amended" pattern (`:97-128`).

## Alternatives considered

**Registry-first (design 2).** Same `Profile`/`Set`/root-`Main` skeleton, and the source of the grafts this
proposal takes — `(*Set).Faults` as the only constructor, `Kind`, `AuthPolicy()`/`EntryFor()`/`Reject()`, the
panic-to-500, `AssertDeterministic`, `--print-routes`. Not adopted whole because its recommended shape was not the
shape it spiked: the spike promoted `journal` (20 declarations) and `jobs` (7) as public packages, the document
then recommended aliasing ~16 of them into `provider` and left the choice open as its own Q1 — and the alias form
was measured to be a documentation dead end (`go doc` prints `type X = pkg.X` with no fields and an unreachable
target). It also put the registry in a separate `profile` package beside `provider`, which the consumer-first spike
showed is unnecessary (the type has to live where `Route`/`Deps`/`Validator` live to avoid the composition cycle)
and which a maintainer would be explaining in review beside `provider` and `profiles` for years. Its CAS release of
a claimed attempt is the only proposal that touches the lane counter under concurrency; it is deferred until a
real out-of-tree profile trips the unit 0 warning (rule 5 above).

**Consumer-first (design 3).** The lowest-risk mechanics — five `mv`s plus an import rewrite, all four profile
suites passing with zero source edits — and the best adopter simulation (a separate module, an OpenAI-compatible
profile, embedded contracts, `ValidateProfile` observed failing then passing). This proposal takes its unit
ordering (the fault gate first, as a bug fix), `profiles/<name>` plus the no-privilege test, `Build.Program`,
the `scenario.profile.unscripted` warning, and the deletion of `provider.Exa…`. Not adopted whole because it
exports packages where rule 7 counts symbols: ~+43 net declarations including `journal.Ring`,
`NewRingWithLimits`, `Limits`, `Namespaced`, `NextIn`, `SnapshotIn`, `Redact` and `jobs.Registry`, `Limits`,
`ErrDuplicate`, `ErrLimit` — all AUDIT 1 class (c), reachable only from composition, and the minimal-export spike
proves the composition entry point dissolves the need. Exporting the retention path also moves rule 4 from
"unreachable" toward "reachable but idempotent". And its rule-3 argument leaves `MuxSpec.NotFound` optional on
the claim that `NewMux` already substitutes a fail-closed default; the default records a finding but its body is
empty (`provider/mux.go:106-112`), so profile #15 could ship a bodyless 404 on every unmatched path with nothing
at registration noticing.

**Promoting `wire` and `ids` as packages** (both alternatives). Defensible — their doc comments are rule 2 in
prose — but `wire` as a package with `Render`, `Omit` and `MergeJSON` as separate entry points preserves the
extra/omit divergence forever and lets profile #15 invent a third ordering. `provider.Render(v, extra, omit)` is
one symbol and one answer; `Hex32`/`UUIDv5`/`FloatIn` are three symbols where a package would be four plus a
name. `Derive`'s length-prefixing subtlety stays where it is tested.

## Risks and what is deliberately NOT exported

Not exported, and why:

| Not exported | Reason |
|---|---|
| `internal/wire`, `internal/ids` as packages; `Omit`, `MergeJSON`, `Derive` | one `Render`, one ordering; zero direct users of `Derive` |
| `internal/journal` types beyond `Finding`/`Severity`; any journal constructor outside `testkit` | rule 4: the retention path stays unreachable; the framework builds the journal |
| `internal/jobs` — `Job`, `Store`, `Registry`, `ErrDuplicate`, `ErrLimit` | removed from every exported signature; `testkit/server.go:124-129` already records the sentinels as deliberately unreachable |
| `internal/faults` — `New`, `Engine`, `Option` | `(*Set).Faults` is strictly better: the wrong-route-set engine is unconstructible |
| `internal/redact` | rule 4's "no configuration flag that turns this off"; extensibility arrives as data (`CredentialNames`), never as a call |
| `internal/admin`, any admin-route field on `Profile` | rule 6 (D-3) |
| `internal/config`, `internal/server` | `servicesim.Run` is the whole contract; exporting `Config` makes every flag an obligation |
| a package-level `Register()`/`init()` registry | rule 6 and rule 2: hidden global state and import-order-dependent listener order |
| a scenario overlay for out-of-tree profiles | `builtin:happy` must mean the same bytes in every build |
| `Route.ExampleRequest` (AUDIT 2 #49/#50) | attractive for generated smoke probes; additive; design it against a real consumer's image in v0.6.0 — until then smoke probes are hand-written, ours and theirs |
| `internal/httpx.ReadBody`/`ErrBodyTooLarge` (AUDIT 1 class b) | `Handle` applies the limit before any handler runs (`provider/handle.go:466-468`) and `Exchange.Raw`/`Exchange.Body` are already public, so a non-JSON route — MCP is that shape — reads the bounded bytes without the function; nothing to export |
| a `testkit` default profile set | it would put four vendor contracts and their goldens in the build graph of a team simulating one other API (D-5) |

Risks:

1. **The guards stop binding.** A third-party "Servicesim profile" with no contract and no provenance is possible
   and the framework cannot prevent it (`d9-framework-framing.md:281-282`). Mitigation: make the bar one call
   (`ValidateProfile` + `Conform`), name it as what "profile" means (D-6). Residual risk accepted; the alternative
   is babysitting PRs, which is the decision.
2. **`Deps.Journal`/`Deps.Jobs` remain typed on internal types.** A consumer wanting a *production* journal sink
   cannot supply one without `testkit` (and `testing`). Intended for rule 4, but real (D-2).
3. **Unit 1's migration is unmeasured.** The winning spike excluded in-tree `_test.go` from vet; the other two
   bounded their breakage to four packages. Unit 1's DoD demands the full-suite run before unit 2 starts.
4. **The extra/omit fix is a behaviour change for exa** in one edge case (a key both omitted and re-added).
   Release-note item.
5. **`Profile` will grow.** `ProjectionKeys`, `ExampleRequest`, per-route stream examples and the sem\* needs are
   all pressure. Every field must cite the enumeration site it replaces, or it is not added.
6. **Rule 5 remains partly cultural.** A profile that always answers 200 is legal. The answer is the guide, the
   four examples and `ValidateProfile`; the design should not pretend otherwise.
7. **The turn model cannot express the sem\* mock.** `Kind` solves the multiplicity and the open `Stream` solves
   the framing; the three turn-model gaps above are untouched. They need their own design pass before the
   consolidation and are the strongest argument against 1.0 at this release.
8. **`Refusal.X` may be nil.** The one nil in the new surface; documented, and the reference profiles show both
   branches.

## Decisions for the owner

Each is phrased so a one-line answer suffices. Three questions an earlier draft listed are gone because they were
not the owner's: `Render`'s extra/omit order (`docs/scenario-schema.md:452-454` already decided it — exa is fixed
in unit 1), the CAS release of a claimed fault lane (an implementation choice, deferred in rule 5), and
`Validator.ProjectionKeys()` (four in-tree implementations, zero external — unit 8).

- **D-1. Add `Profile.Kind` (one handler shape, many listeners) in v0.5.0, with the single-entry limit stated
  above?** *Recommend yes* — `scenario.ProviderEntry.Kind` already exists; one field now, a break to somebody
  else's registration later; the sem\* workload needs it four times over.
- **D-2. Can a production composition substitute a journal?** *Recommend not in v0.5.0; if asked, add a
  redacted-entry sink interface, never export `Journal`* — a sink receives what `Handle` already masked; a
  journal is the retention boundary rule 4 protects.
- **D-3. Admin surface closed to consumer composition?** *Recommend yes, closed* — the one place the framework
  decision is deliberately partially withheld, and it needs your signature, not an architect's.
- **D-4. Do the four move to `profiles/<name>`?** *Recommend yes* — the decision says "treat what we have as
  examples", the no-privilege test is stronger with physical separation, and it is free before v0.5.0 and
  permanent after. Say no if the adopter is already writing imports against `provider/mcp`.
- **D-5. Is `testkit.WithProfiles` required, breaking `testkit.Start(t)`?** *Recommend yes, required* — a
  default of four pulls four vendor contracts into the build graph of a team simulating one API; our README
  examples gain one line.
- **D-6. `CLAUDE.md:132` ("added the way the shipped profiles were"): re-earn or rewrite?** *Recommend
  re-earn* — add "which, out of tree, means `testkit.ValidateProfile` and `contracts.Conform` in your own CI".
- **D-7. Built-ins reference-only, ratified in `CLAUDE.md` and `CONTRIBUTING.md`?** *Recommend yes* — all
  three designs reject an overlay; the `scenario.profile.unscripted` warning is the whole accommodation.
- **D-8. v0.5.0 holds all units 0–10, or ships units 0–4 + 9–10 and follows with 5–8?** *Recommend all* —
  `contracts` and `testkit` each break once either way, and one break is the point of doing it before the tag.
- **D-9. Module split now, or one module with ADR 0001 amended?** *Recommend one module, amend* — the split's
  benefit is release noise; its cost is the version-skew mode ADR 0001 named and a second artefact every consumer
  pins. Record tier 3's question as open.
- **D-10. Write the 1.0 trigger into the README as stated above?** *Recommend yes* — a testable condition, not a
  vibe; until then a v0.x pin means the seam may still move.
- **D-11. Where does the sem\* `chat/completions` profile land — in-tree as a fifth reference example, or in the
  sem\* repos as the framework's first out-of-tree profile — and how does it sequence against semstreams v1?**
  *Recommend out-of-tree, in a sem\* repo; the semstreams v1 sequencing is yours (the consolidation plan has it
  blocked on v1)* — the 1.0 trigger needs a profile written against the exported seam by someone who did not
  write the seam, and this is the only candidate on the horizon; a fifth in-tree example proves nothing the four
  do not. Whichever way it goes decides whether "the `chat/completions` profile has landed" is a gate we control.
  This is the question AUDIT 3 §6 and tier 3 (`d9-framework-framing.md:191-195`) left open, and it determines
  who writes the first real out-of-tree profile.
