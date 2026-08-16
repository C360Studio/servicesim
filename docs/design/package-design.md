# Servicesim Go Package Design

## Status and scope

This document is the Go-level engineering design that sits *under*
`docs/architecture-and-implementation-plan.md`. The plan owns the product decisions: repository layout, the scenario
concept, the fault catalogue, the admin surface, the security requirements, and the acceptance criteria. This document
owns the Go decisions: packages, import direction, exact type and interface declarations, synchronisation, and the
implementation fan-out.

Where this document contradicts the plan it is because a **freshly verified vendor wire contract** contradicts the plan.
Those cases are called out inline and collected in [Verified-contract deviation register](#verified-contract-deviation-register).
Every other plan decision is treated as settled and is not revisited here.

House conventions this design is written against:

- Module `github.com/c360studio/servicesim`, Go 1.26.
- revive pinned through the `tool` directive in `go.mod`, configured by `revive.toml` with `warningCode = 1`. Every
  exported symbol carries a doc comment starting with its own name; every package carries a package comment; no unused
  parameters; no shadowing of `max`, `min`, `cap`, `len`; `ID`/`URL`/`API` initialisms.
- stdlib first. Total dependency budget: `gopkg.in/yaml.v3`, `github.com/stretchr/testify`, `github.com/google/go-cmp`.
  `net/http`, `log/slog`, `encoding/json` only. No router dependency — Go 1.22 `http.ServeMux` pattern routing.
- `Taskfile.yml` with a `taskfiles/` include directory.

### Mechanics verified against the toolchain before writing this document

Every non-obvious mechanism below was executed against Go 1.26.4 rather than assumed. The results are quoted at the
point of use, and the four that change the design are:

1. `http.ServeMux` with a `/` catch-all **suppresses the built-in 405**. `GET /search` falls through to `/` and yields
   404 unless a method-less `/search` pattern is also registered. `scripts/image-smoke.sh` already asserts 405, so the
   method-less pattern is mandatory, not stylistic.
2. `httptest.NewServer`'s `ResponseWriter` implements both `http.Hijacker` and `http.Flusher`, so every transport-level
   fault works in-process. `httptest.NewRecorder` does not, and must never be used for fault tests.
3. `encoding/json` sorts map keys deterministically and recursively, so extra-field merging is byte-stable — but it
   re-orders struct-derived keys, which is why golden comparison is semantic.
4. A type alias in a non-internal package to an `internal/...` type is fully usable from another module, including
   *implementing* an interface whose method set mentions it. This is what lets `testkit` hand consumers a typed journal.

---

## 1. Package map

### 1.1 Responsibilities

| Package | Visibility | Single responsibility |
|---|---|---|
| `scenario` | exported | The versioned YAML scenario schema: parse, validate, resolve source references, apply defaults. Knows nothing about HTTP. |
| `provider` | exported | The handler seam. Provider identity, routes, `Deps`, `Clock`, the fault-selection *interface*, fault *execution*, the mux builder, the per-request `Exchange`, and the shared request lifecycle wrapper. |
| `provider/exa` | exported | Exa wire contract: routing, request validation, response/error encoding for `POST /search` and `POST /answer`. |
| `provider/tavily` | exported | Tavily wire contract for `POST /search`. |
| `provider/perplexity` | exported | Perplexity Sonar wire contract for `POST /v1/sonar` and `POST /chat/completions`. |
| `testkit` | exported | In-process consumer helpers: start `httptest` servers, read the journal, assert on it. |
| `scenarios` | exported | `embed.FS` of the built-in protocol scenarios plus a lookup by name. |
| `contracts` | exported (test data) | Golden wire fixtures and their provenance records, plus an `embed.FS` over them and a provenance lookup used by this repository's own contract test. Imports `embed` and nothing else; no provider logic, and nothing in the module imports it except `contracts/contracts_test.go`. |
| `internal/journal` | internal | The redacted, bounded request journal. Owns `Entry`, `Finding`, `Outcome` and the `Journal` interface. |
| `internal/redact` | internal | Credential redaction for headers, query strings, decoded JSON bodies, and free text. |
| `internal/faults` | internal | Deterministic fault *selection* (attempt counting) only. Execution belongs to `provider`: a package `provider.Handle` calls into cannot also import `provider`. |
| `internal/httpx` | internal | Request-side shared checks: bounded body read, JSON decode, auth extraction, content-type check. |
| `internal/wire` | internal | Response-side rendering: typed struct to JSON, extra-field merge. |
| `internal/ids` | internal | Deterministic identifier derivation from stable fixture keys. |
| `internal/config` | internal | Flag/env configuration with defined precedence and path-traversal-safe scenario resolution. |
| `internal/admin` | internal | `/healthz`, `/readyz`, `/__admin/requests`, `/__admin/scenario`, `/__admin/jobs`, `/__admin/reset`. |
| `internal/server` | internal | Composition and lifecycle: build handlers, bind listeners, graceful shutdown, readiness gate. |
| `cmd/servicesim` | binary | Flag entry point, `--version`, `--healthcheck`, signal handling. |

Deviations from the plan's layout, both additive:

- `provider/` gains files of its own (`provider` is a real package, not just a directory). This is what makes the
  exported handlers constructible by an external consumer without leaking `internal/...` types into a position the
  consumer must supply.
- `internal/httpx`, `internal/wire`, `internal/ids`, `internal/config`, `internal/server` and `scenarios` are new. The
  plan's `internal/{admin,faults,journal,redact}` are all present with the responsibilities the plan gave them.
- `scenario/render.go` survives, but it renders a *provider-neutral* view of a source (defaults applied, snippets
  normalised). Wire rendering is provider-specific and lives in `provider/<name>/render.go`.

### 1.2 Import edges and the acyclicity proof

Assign every package a level. An import is legal only from a strictly higher level to a strictly lower one. A directed
graph that admits a strictly decreasing integer labelling on every edge cannot contain a cycle, because a cycle would
require a level to be strictly less than itself. The table below is that labelling, and it is complete: no package
imports anything not listed.

| Level | Package | Imports (in-module) |
|---:|---|---|
| 0 | `internal/redact` | — |
| 0 | `internal/ids` | — |
| 0 | `internal/wire` | — |
| 0 | `contracts` | — (`embed` only) |
| 1 | `scenario` | — (`gopkg.in/yaml.v3` only) |
| 1 | `internal/journal` | `internal/redact` |
| 2 | `internal/httpx` | `internal/journal` |
| 2 | `scenarios` | `scenario` |
| 3 | `provider` | `scenario`, `internal/journal`, `internal/httpx`, `internal/redact` |
| 4 | `internal/faults` | `scenario`, `provider` |
| 5 | `provider/exa` | `scenario`, `provider`, `internal/httpx`, `internal/wire`, `internal/ids`, `internal/journal` |
| 5 | `provider/tavily` | same as `provider/exa` |
| 5 | `provider/perplexity` | same as `provider/exa` |
| 5 | `internal/config` | `provider` |
| 5 | `internal/admin` | `scenario`, `provider`, `internal/journal` |
| 6 | `internal/server` | `scenario`, `scenarios`, `provider`, `provider/{exa,tavily,perplexity}`, `internal/{admin,faults,journal,config}` |
| 7 | `testkit` | `scenario`, `scenarios`, `provider`, `provider/{exa,tavily,perplexity}`, `internal/{server,journal,faults}` |
| 8 | `cmd/servicesim` | `internal/config`, `internal/server` |

Every edge in the table goes from a higher level to a lower one, therefore the import graph is acyclic. The table is
exhaustive: a package imports exactly the in-module packages its row lists, and nothing else. Five rows deserve comment
because they are the ones a reviewer will want to check:

- **`provider` &rarr; `internal/journal`, and never the reverse.** `journal.Entry.Provider` is a plain `string`, not
  `provider.Name`. That single choice is what breaks the otherwise-inevitable cycle between the seam and the journal.
  The cost is a `string(p)` conversion at three call sites; the benefit is that the journal stays at level 1 with no
  dependency on HTTP or provider concepts at all.
- **`provider` &rarr; `internal/httpx`.** `Handle` performs the bounded body read (`httpx.ReadBody`), the JSON decode
  (`httpx.DecodeObject`) and the credential observation (`httpx.Observe`) itself, so this edge is real and is listed.
  It is legal — level 3 to level 2 — and it is what keeps `httpx.ErrBodyTooLarge` the *single* source of the
  `request.body_too_large` finding. No provider package may reimplement `http.MaxBytesReader` handling locally.
- **`internal/faults` &rarr; `provider`, and never the reverse.** The seam declares the `Faults` *interface*; the
  engine implements it, and the engine does **selection only**. Fault *execution* lives in `provider` itself
  (`provider/fault_exec.go`), because `Handle` calls it and a package `Handle` calls cannot import `provider`. Provider
  handler packages therefore never import `internal/faults`, so a consumer importing `provider/exa` does not drag in
  the engine's mutable state.
- **`provider/*` never imports `provider/*`.** The three provider packages are siblings with no edges between them.
  Everything they share lives in `provider`, `internal/httpx` or `internal/wire`.
- **Nothing imports `contracts`.** It is `embed`ded fixture data plus a provenance lookup, consumed by
  `contracts/contracts_test.go` and by nothing else in the module. In particular `testkit` does **not** import it:
  `testkit.AssertGoldenJSON` performs a plain semantic comparison against the path it is given and knows nothing about
  provenance, because a consumer's goldens live in the consumer's repository. The provenance requirement is enforced
  only by U5's own contract test. See §2.10.

### 1.3 Exported versus internal, and how the seam stays usable

`provider/*`, `scenario`, `testkit`, `scenarios` and `contracts` are the public API. Everything under `internal/` is
invisible to consuming repositories.

That creates one real problem. `provider.Deps` needs a journal field, but `journal.Journal` is internal. A consumer can
still *construct* `provider.Deps{Journal: x}` — the `internal` rule restricts import paths, not composite literals — but
they cannot *name* the type to implement it or to hold a snapshot.

The fix is a type alias in a non-internal package. This was verified cross-module against Go 1.26.4: a second module
importing `example.test/lib/testkit` was able to declare `func (m *myJournal) Append(e testkit.Entry)` and satisfy
`testkit.Journal` — which is `journal.Journal` — with no ability to import `example.test/lib/internal/journal`.

The alias set must be **closed under "types a consumer has to name"**, not just the two obvious ones. `Journal`
requires `Stats() Stats`, so a consumer that cannot name `Stats` cannot implement `Journal` at all; a consumer writing
`func assertFault(o Outcome)` or a table with a `want OutcomeKind` column needs those too. `testkit` therefore
re-exports every type reachable from `Entry` and every constant of those types:

```go
// Entry is one recorded request in the journal. It aliases the internal journal
// entry so consumers outside this module can name it in assertions.
type Entry = journal.Entry

// Finding is one validation warning or error recorded against a request.
type Finding = journal.Finding

// Journal is the append-only request journal contract.
type Journal = journal.Journal

// Stats describes journal retention. It is part of the Journal method set, so a
// consumer cannot implement Journal without naming it.
type Stats = journal.Stats

// Outcome is what a recorded request produced.
type Outcome = journal.Outcome

// OutcomeKind classifies what the client received.
type OutcomeKind = journal.OutcomeKind

// AuthObservation is what was observed about a request's credentials.
type AuthObservation = journal.AuthObservation

// Severity classifies a validation finding.
type Severity = journal.Severity

// Outcome kinds, re-exported so a consumer's table-driven test keeps its
// compile-time check instead of falling back to string literals.
const (
	OutcomeScenario  = journal.OutcomeScenario
	OutcomeError     = journal.OutcomeError
	OutcomeFault     = journal.OutcomeFault
	OutcomeUnmatched = journal.OutcomeUnmatched
)

// Finding severities, re-exported for the same reason.
const (
	SeverityWarning = journal.SeverityWarning
	SeverityError   = journal.SeverityError
)
```

The aliases live in `testkit`, not in `provider`, because putting them in `provider` would require
`provider` &rarr; `internal/journal` *and* `internal/journal` &rarr; `provider`, which is the cycle we just eliminated.
`testkit` sits at level 7 and can safely see both.

Nothing in the compiler keeps this set complete, so **U19's `examples/adapter` module — already a separate module —
owns the guard**: `examples/adapter/journal_test.go` declares a type implementing `testkit.Journal` (all five methods,
`Stats() testkit.Stats` included), passes it as `provider.Deps{Journal: ...}`, and reads `e.Outcome.Kind`,
`e.Auth.Fingerprint` and `e.Findings[0].Severity` through the aliased types. If an alias is missing, that module stops
compiling; without it, the gap is invisible until a consumer hits it.

---

## 2. Type and interface declarations

Every declaration below is the real signature. A package can be implemented from this section without inventing a
cross-package type.

### 2.1 `scenario`

The YAML-mapped struct tree. `Scenario` is immutable after `Load` returns; `Resolve` takes addresses of slice elements,
so appending to `Sources` afterwards invalidates every resolved pointer.

```go
// Package scenario defines the versioned YAML scenario schema, loads it, validates
// it, and resolves source references into pointers so that request handling never
// performs a lookup that can fail.
package scenario

// SchemaVersion is the only scenario schema version this build understands.
const SchemaVersion = 1

// Scenario is one deterministic corpus plus the per-provider projections and
// behaviour that render it. A Scenario is immutable once Load returns.
type Scenario struct {
	Version     int       `yaml:"version"`
	Name        string    `yaml:"name"`
	Description string    `yaml:"description,omitempty"`

	// Seed is the stable key every derived identifier hangs off. It defaults to
	// Name, so two scenarios with different names never collide on request IDs.
	Seed string `yaml:"seed,omitempty"`

	// Time pins every timestamp the wire responses carry. Nothing in a response
	// body is ever read from a clock; see docs/design/package-design.md §3.
	Time TimeConfig `yaml:"time,omitempty"`

	Sources   []Source  `yaml:"sources,omitempty"`
	Providers Providers `yaml:"providers,omitempty"`

	// path is the file this scenario was loaded from, for diagnostics. Empty for
	// scenarios built in memory.
	path string

	// bySourceID indexes Sources by ID. Built by Resolve, nil before it.
	bySourceID map[string]*Source

	// byClaimID maps a claim identity to every source asserting it. Claim IDs are
	// deliberately repeatable across sources; that repetition is the corroboration
	// signal fusion tests assert on.
	byClaimID map[string][]*Source
}

// TimeConfig pins the deterministic time base for rendered responses.
type TimeConfig struct {
	// Base is the instant every synthesised response timestamp derives from.
	// Defaults to 2026-01-01T00:00:00Z.
	Base time.Time `yaml:"base,omitempty"`
}

// Source is one canonical document in the corpus. A single Source renders into
// all three provider wire formats, which is what makes cross-provider overlap
// deliberate rather than accidental.
type Source struct {
	ID          string    `yaml:"id"`
	URL         string    `yaml:"url"`
	Title       string    `yaml:"title"`
	Author      string    `yaml:"author,omitempty"`
	PublishedAt *time.Time `yaml:"published_at,omitempty"`
	Text        string    `yaml:"text,omitempty"`
	Snippets    []string  `yaml:"snippets,omitempty"`
	Claims      []Claim   `yaml:"claims,omitempty"`

	// Image and Favicon feed the Exa result fields of the same name and the
	// Tavily per-result image list. Both are optional.
	Image   string `yaml:"image,omitempty"`
	Favicon string `yaml:"favicon,omitempty"`

	// AuthorNull forces an explicit JSON null for the author field rather than
	// omitting it. Exa documents author as anyOf[string,null]; a consumer that
	// only ever sees the field absent will not have exercised the null branch.
	AuthorNull bool `yaml:"author_null,omitempty"`
}

// Claim is a normalised assertion carried by a Source. Claim IDs are intentionally
// shared across sources: two sources asserting claim-1 is how a scenario expresses
// corroboration.
type Claim struct {
	ID   string `yaml:"id"`
	Text string `yaml:"text"`
}

// Providers holds the per-provider projections of the shared source corpus.
type Providers struct {
	Exa        *ExaProjection        `yaml:"exa,omitempty"`
	Tavily     *TavilyProjection     `yaml:"tavily,omitempty"`
	Perplexity *PerplexityProjection `yaml:"perplexity,omitempty"`
}
```

Source references. Every place a projection names a source uses one embedded struct so resolution is written once.
yaml.v3 `,inline` on an embedded struct was verified to work — with one hard constraint that governs every field name
below: **an inlined struct's YAML keys share one namespace with the outer struct's, and a collision is fatal.**
`yaml.v3` v3.0.1 does not return an error for a duplicate key across an inline boundary, it *panics*
(`duplicated key 'source' in struct scenario.PerplexityResult`), taking the process down on the first scenario that
contains such an entry. The Go field names collide just as badly: an outer field with the same name as a promoted one
silently shadows it, with no compiler or `go vet` signal, so render code reads one thing and `Resolve` writes another.

Both traps are avoided by naming, not by discipline. The embedded reference type uses names that no projection
overrides:

```go
// SourceRef is a reference from a provider projection to a canonical Source.
// The YAML carries only the reference; Resolve fills in Target.
//
// Ref and Target are named so that no result type embedding SourceRef can shadow
// them: every result type declares its own ID (the wire results[].id override),
// and Perplexity declares its own SourceType (the web/attachment discriminator).
// Neither name, and neither YAML key, collides with this struct's.
type SourceRef struct {
	// Ref is the scenario-local Source.ID this projection entry points at. Its
	// YAML key is "source", which is the only key this struct contributes to an
	// inline namespace.
	Ref string `yaml:"source"`

	// Target is the resolved source. Non-nil after Scenario.Resolve succeeds;
	// handlers may dereference it without a nil check.
	Target *Source `yaml:"-" json:"-"`
}

// UnmarshalYAML accepts either form, because both appear in the wild and in the
// plan's own scenario example:
//
//	citations:
//	  - source-a            # scalar shorthand: Ref only
//	  - source: source-b    # mapping form
//
// A scalar node sets Ref; a mapping node is decoded strictly (unknown keys are an
// authoring error, exactly as at the top level); anything else is an error naming
// the YAML path.
func (r *SourceRef) UnmarshalYAML(value *yaml.Node) error

// Resolve links every SourceRef in the scenario to its Source and builds the
// source and claim indexes. It returns an error naming the first unresolvable
// reference by its YAML path, for example
// "providers.exa.results[2].source: unknown source \"source-z\"".
func (s *Scenario) Resolve() error

// SourceByID returns the canonical source with the given ID.
func (s *Scenario) SourceByID(id string) (*Source, bool)

// SourcesForClaim returns every source asserting the given claim ID, in corpus
// declaration order.
func (s *Scenario) SourcesForClaim(claimID string) []*Source

// HasFaults reports whether any provider projection declares a fault plan with at
// least one attempt. provider.Deps.Normalized consults it to warn when a scenario
// declares faults but no fault engine was supplied; see §2.2.
func (s *Scenario) HasFaults() bool
```

Resolution writes `ref.Target`, and every projection reads `ref.Target` or goes through `Render(ref)`. Reviewers should
grep for `.Source` on a `SourceRef`: it must not exist.

The scalar shorthand is not confined to `[]SourceRef` fields. The plan's example writes Perplexity's `search_results`
as a bare list of source IDs while its element type is a full struct, so `ExaResult`, `TavilyResult` and
`PerplexityResult` each implement `UnmarshalYAML` too: a scalar node means "this entry references that source and
takes every other default", and a mapping node decodes normally. All four unmarshalers share one helper so the rule
exists once:

```go
// decodeRefOrMapping decodes value into out, accepting a scalar node as the
// shorthand for {source: <scalar>}. It is the single implementation behind
// SourceRef, ExaResult, TavilyResult and PerplexityResult UnmarshalYAML methods.
// ref points at out's embedded SourceRef (at out itself for a bare SourceRef) and
// is what the scalar branch fills in.
//
// Callers pass a pointer to a *defined-type copy* — type rawExaResult ExaResult —
// which does not carry the UnmarshalYAML method, so the mapping branch cannot
// re-enter the method that called it. Forgetting that is an infinite recursion,
// not a compile error.
//
// The mapping branch calls decodeStrict rather than value.Decode, because
// yaml.Node.Decode builds a fresh decoder with KnownFields off: without this, a
// custom UnmarshalYAML would silently reopen the door to typo'd scenario keys
// that Load's KnownFields(true) is there to close.
func decodeRefOrMapping(value *yaml.Node, ref *SourceRef, out any) error

// decodeStrict decodes a mapping node into out, rejecting any key that out's type
// does not declare. The known-key set is derived once per type by reflection over
// the yaml tags, inline fields included, and cached.
func decodeStrict(value *yaml.Node, out any) error
```

These two helpers and their tests live in `scenario/decode.go` and `scenario/decode_test.go` (U4). The decode test is
also where the inline-collision class is caught: it round-trips **one instance of every projection struct** through
`yaml.Unmarshal`, so a future field whose key duplicates an inlined one fails in U4's own package rather than at
container startup — `TestDecode_EveryProjectionStructRoundTrips`.

The resolution loop is the one place a reviewer should look twice, because the obvious form is wrong:

```go
// Correct: address the slice element.
s.bySourceID = make(map[string]*Source, len(s.Sources))
for i := range s.Sources {
	s.bySourceID[s.Sources[i].ID] = &s.Sources[i]
}

// Wrong: &src addresses the per-iteration copy, so every projection would point
// at a value that is not in s.Sources and that mutations never reach.
// for _, src := range s.Sources { s.bySourceID[src.ID] = &src }
```

Provider projections. Field sets follow the verified wire contracts, not the plan's examples.

```go
// ExaProjection is how the shared corpus renders through the Exa API.
type ExaProjection struct {
	Auth       *AuthPolicy       `yaml:"auth,omitempty"`
	Validation *ValidationPolicy `yaml:"validation,omitempty"`
	Fault      *Fault            `yaml:"fault,omitempty"`

	// RequestID overrides the derived 32-character lowercase hex request ID.
	RequestID string `yaml:"request_id,omitempty"`

	Results     []ExaResult `yaml:"results,omitempty"`
	CostDollars *ExaCost    `yaml:"cost_dollars,omitempty"`
	Output      *ExaOutput  `yaml:"output,omitempty"`

	// Answer projects the POST /answer endpoint, which the plan does not mention
	// at all. Absent means /answer returns an empty answer with no citations.
	Answer *ExaAnswer `yaml:"answer,omitempty"`

	// Stream selects the behaviour for a request carrying "stream": true.
	// Defaults to StreamWarn: a journal warning plus the ordinary JSON body.
	Stream StreamPolicy `yaml:"stream,omitempty"`

	ExtraFields ExtraFields `yaml:"extra_fields,omitempty"`
}

// ExaResult is one entry of the Exa /search results array.
type ExaResult struct {
	SourceRef `yaml:",inline"`

	// ID overrides the wire field results[].id. The default is the source URL,
	// because Exa's own documented example uses a URL there, not an opaque slug.
	// It shadows nothing: the inlined reference is SourceRef.Ref.
	ID string `yaml:"id,omitempty"`

	Text            string    `yaml:"text,omitempty"`
	Highlights      []string  `yaml:"highlights,omitempty"`
	HighlightScores []float64 `yaml:"highlight_scores,omitempty"`
	Summary         string    `yaml:"summary,omitempty"`

	// Score is accepted and never emitted. Exa's result schema has no top-level
	// score field; emitting one would teach consumers to parse something the real
	// API never sends. Setting it raises the exa.result.score.not_emitted warning.
	Score *float64 `yaml:"score,omitempty"`

	// OmitFields drops named response fields that would otherwise be present, for
	// tests that assert a consumer fails on a missing required field.
	OmitFields []string `yaml:"omit_fields,omitempty"`

	ExtraFields ExtraFields `yaml:"extra_fields,omitempty"`
}

// ExaCost projects the costDollars object, which is always present on a real Exa
// response and entirely absent from the plan's example.
type ExaCost struct {
	Total  float64  `yaml:"total"`
	Neural *float64 `yaml:"neural,omitempty"`
}

// ExaOutput projects the structured-output branch of the response, present only
// when the request supplied outputSchema.
type ExaOutput struct {
	Content   any             `yaml:"content,omitempty"`
	Grounding []ExaGrounding  `yaml:"grounding,omitempty"`
}

// ExaGrounding ties a JSON path in the structured output to its citations.
type ExaGrounding struct {
	Field      string       `yaml:"field"`
	Citations  []SourceRef  `yaml:"citations,omitempty"`
	Confidence string       `yaml:"confidence,omitempty"` // low | medium | high
}

// ExaAnswer projects the POST /answer response.
type ExaAnswer struct {
	Fault       *Fault      `yaml:"fault,omitempty"`
	Answer      any         `yaml:"answer,omitempty"` // string or object
	Citations   []SourceRef `yaml:"citations,omitempty"`
	CostDollars *ExaCost    `yaml:"cost_dollars,omitempty"`
	ExtraFields ExtraFields `yaml:"extra_fields,omitempty"`
}
```

```go
// TavilyProjection is how the shared corpus renders through the Tavily API.
type TavilyProjection struct {
	Auth       *AuthPolicy       `yaml:"auth,omitempty"`
	Validation *ValidationPolicy `yaml:"validation,omitempty"`
	Fault      *Fault            `yaml:"fault,omitempty"`

	// RequestID overrides the derived UUID. Tavily's documented example is a UUID,
	// not the plan's readable slug.
	RequestID string `yaml:"request_id,omitempty"`

	// Answer is a pointer so a scenario can distinguish "no answer requested"
	// (nil, rendered as JSON null) from "an empty answer" ("").
	Answer *string `yaml:"answer,omitempty"`

	Images  []TavilyImage  `yaml:"images,omitempty"`
	Results []TavilyResult `yaml:"results,omitempty"`

	// ResponseTime is a JSON number, not a string. The plan encodes "1.15";
	// Tavily's schema declares type: number, format: float.
	ResponseTime float64 `yaml:"response_time,omitempty"`

	AutoParameters map[string]any `yaml:"auto_parameters,omitempty"`
	Usage          *TavilyUsage   `yaml:"usage,omitempty"`
	ExtraFields    ExtraFields    `yaml:"extra_fields,omitempty"`
}

// TavilyResult is one entry of the Tavily results array.
type TavilyResult struct {
	SourceRef `yaml:",inline"`

	// ID overrides the wire field results[].id, whose default is the derived
	// xxxxxx-NN shape. It shadows nothing: the inlined reference is SourceRef.Ref.
	ID      string   `yaml:"id,omitempty"`
	Content string   `yaml:"content,omitempty"`
	Score   float64  `yaml:"score,omitempty"`

	// RawContent is tri-state: absent, explicit null, or a string. Tavily's own
	// documented example value for raw_content is null.
	RawContent  Nullable `yaml:"raw_content,omitempty"`
	Favicon     string   `yaml:"favicon,omitempty"`
	Images      []TavilyImage `yaml:"images,omitempty"`
	OmitFields  []string `yaml:"omit_fields,omitempty"`
	ExtraFields ExtraFields `yaml:"extra_fields,omitempty"`
}

// TavilyImage is one entry of a Tavily images array. Items are objects, not bare
// URL strings.
type TavilyImage struct {
	URL         string `yaml:"url"`
	Description string `yaml:"description,omitempty"`
}

// TavilyUsage projects the credit-usage object gated by include_usage.
type TavilyUsage struct {
	Credits int `yaml:"credits"`
}
```

```go
// PerplexityProjection is how the shared corpus renders through the Perplexity
// Sonar chat-completions API.
type PerplexityProjection struct {
	Auth       *AuthPolicy       `yaml:"auth,omitempty"`
	Validation *ValidationPolicy `yaml:"validation,omitempty"`
	Fault      *Fault            `yaml:"fault,omitempty"`

	CompletionID string `yaml:"completion_id,omitempty"`

	// Created overrides the derived Unix timestamp. When zero it is
	// Scenario.Time.Base.Unix(), never time.Now().
	Created int64 `yaml:"created,omitempty"`

	// Model overrides the echoed model. When empty the request's model is echoed.
	Model string `yaml:"model,omitempty"`

	Answer       string `yaml:"answer,omitempty"`
	FinishReason string `yaml:"finish_reason,omitempty"` // stop | length

	Citations        []SourceRef          `yaml:"citations,omitempty"`
	SearchResults    []PerplexityResult   `yaml:"search_results,omitempty"`
	Usage            *PerplexityUsage     `yaml:"usage,omitempty"`
	Images           []PerplexityImage    `yaml:"images,omitempty"`
	RelatedQuestions []string             `yaml:"related_questions,omitempty"`

	// Stream selects the behaviour for a request carrying "stream": true.
	// Superseded: docs/design/streaming.md is the design of record for
	// streaming and is newest where the two disagree (its own banner says
	// so). Shipped, this field's type is scenario.StreamScript, not
	// StreamPolicy below — one exported field retyped rather than only
	// added to, because the mapping form that also carries deltas: and
	// pace: cannot share a YAML key with a plain string field
	// (streaming.md §3). The old two values still decode as the scalar
	// shorthand (StreamScript.UnmarshalYAML); a third, StreamServe, serves
	// the scripted SSE sequence. ExaProjection.Stream above is unaffected
	// and keeps the plain StreamPolicy type below: Exa does not stream. See
	// docs/scenario-schema.md's "Streaming (stream:)" section for the full
	// grammar.
	Stream StreamPolicy `yaml:"stream,omitempty"` // shipped: scenario.StreamScript

	ExtraFields ExtraFields `yaml:"extra_fields,omitempty"`
}

// PerplexityResult is one entry of the search_results array.
type PerplexityResult struct {
	SourceRef `yaml:",inline"`

	Snippet     string   `yaml:"snippet,omitempty"`
	Date        Nullable `yaml:"date,omitempty"`
	LastUpdated Nullable `yaml:"last_updated,omitempty"`

	// SourceType is the web/attachment discriminator. It is deliberately *not*
	// named Source and its key is deliberately *not* "source": the inlined
	// SourceRef already owns the YAML key "source", and a second field claiming it
	// makes yaml.v3 panic with "duplicated key 'source'" on every scenario that
	// declares a search result. The wire field is still named "source" —
	// provider/perplexity/render.go maps SourceType onto
	// perplexity.SearchResult.Source, whose JSON tag is `json:"source"`.
	// Values: web (default) | attachment.
	SourceType string   `yaml:"source_type,omitempty"`
	OmitFields  []string `yaml:"omit_fields,omitempty"`
}

// PerplexityUsage projects the usage object. Cost is required by the live schema
// and absent from the plan's example, so a nil Cost is filled with a derived
// zero-cost object rather than omitted.
type PerplexityUsage struct {
	PromptTokens      int              `yaml:"prompt_tokens"`
	CompletionTokens  int              `yaml:"completion_tokens"`
	TotalTokens       int              `yaml:"total_tokens"`
	SearchContextSize string           `yaml:"search_context_size,omitempty"` // string, not a count
	CitationTokens    *int             `yaml:"citation_tokens,omitempty"`
	NumSearchQueries  *int             `yaml:"num_search_queries,omitempty"`
	ReasoningTokens   *int             `yaml:"reasoning_tokens,omitempty"`
	Cost              *PerplexityCost  `yaml:"cost,omitempty"`
}

// PerplexityCost projects the required usage.cost object.
type PerplexityCost struct {
	InputTokensCost     float64  `yaml:"input_tokens_cost"`
	OutputTokensCost    float64  `yaml:"output_tokens_cost"`
	TotalCost           float64  `yaml:"total_cost"`
	ReasoningTokensCost *float64 `yaml:"reasoning_tokens_cost,omitempty"`
	RequestCost         *float64 `yaml:"request_cost,omitempty"`
	CitationTokensCost  *float64 `yaml:"citation_tokens_cost,omitempty"`
	SearchQueriesCost   *float64 `yaml:"search_queries_cost,omitempty"`
}

// PerplexityImage is one entry of the images array.
type PerplexityImage struct {
	ImageURL string `yaml:"image_url"`
	OriginURL string `yaml:"origin_url,omitempty"`
	Height   int    `yaml:"height,omitempty"`
	Width    int    `yaml:"width,omitempty"`
}
```

Shared projection helper types:

```go
// ExtraFields are additional response properties merged into a rendered body.
// They exercise a consumer's tolerance of additive vendor changes.
type ExtraFields map[string]any

// Nullable is a three-state YAML/JSON scalar: absent, explicit null, or a string.
// It exists because several verified fields are anyOf[string, null] and a
// consumer that only ever sees a field absent has not exercised the null branch.
type Nullable struct {
	set   bool
	null  bool
	value string
}

// SetNullable returns a Nullable holding v.
func SetNullable(v string) Nullable

// NullNullable returns a Nullable that renders as an explicit JSON null.
func NullNullable() Nullable

// IsZero reports whether the value is absent, which is what encoding/json's
// omitzero option consults.
func (n Nullable) IsZero() bool

// Get returns the string and whether a non-null value is present.
func (n Nullable) Get() (string, bool)

// UnmarshalYAML accepts a string scalar, an explicit null, or absence.
func (n *Nullable) UnmarshalYAML(value *yaml.Node) error

// MarshalJSON renders null for the null and absent states.
func (n Nullable) MarshalJSON() ([]byte, error)

// AuthMode selects how a provider treats credentials on a request.
type AuthMode string

// Supported authentication modes.
const (
	AuthRequired AuthMode = "required" // default
	AuthOptional AuthMode = "optional"
	AuthReject   AuthMode = "reject"   // always 401, for unauthorized scenarios
)

// AuthPolicy is the per-provider credential policy for a scenario.
type AuthPolicy struct {
	Mode AuthMode `yaml:"mode,omitempty"`

	// ExpectKey, when set, requires the presented credential to match exactly.
	// It must be a fake key; the journal stores only a fingerprint of what arrived.
	ExpectKey string `yaml:"expect_key,omitempty"`

	// Headers overrides the accepted credential placements, for example
	// ["authorization"] to reject an x-api-key that Exa would normally allow.
	Headers []string `yaml:"headers,omitempty"`
}

// ValidationPolicy tunes how validation findings map onto HTTP outcomes.
type ValidationPolicy struct {
	// Strict promotes every warning to an error.
	Strict bool `yaml:"strict,omitempty"`

	// Promote lists finding codes to raise from warning to error.
	Promote []string `yaml:"promote,omitempty"`

	// Demote lists finding codes to lower from error to warning.
	Demote []string `yaml:"demote,omitempty"`
}

// StreamPolicy selects the behaviour for a streaming request. Streaming was a
// plan non-goal (7) when this type was written; docs/design/streaming.md is
// now the design of record for it and Sonar/Agent streaming has shipped
// (Phase 5). This two-value type still stands as written below, but only for
// Exa's own Stream field: Perplexity's two projections retyped their own
// Stream field to scenario.StreamScript, whose third value, StreamServe, adds
// the scripted SSE sequence — see PerplexityProjection.Stream above and
// docs/scenario-schema.md's "Streaming (stream:)" section.
type StreamPolicy string

// Supported streaming policies.
const (
	StreamWarn   StreamPolicy = "warn"   // default: journal warning, ordinary JSON body
	StreamReject StreamPolicy = "reject" // provider-shaped 4xx
)
```

The fault model:

```go
// FaultKind names one transport or protocol failure mode.
type FaultKind string

// Supported fault kinds. FaultNone renders the scenario response normally and is
// what a trailing "- status: 200" attempt means.
const (
	FaultNone               FaultKind = ""
	FaultStatus             FaultKind = "status"
	FaultCloseBeforeHeaders FaultKind = "close_before_headers"
	FaultTruncateBody       FaultKind = "truncate_body"
	FaultInvalidJSON        FaultKind = "invalid_json"
	FaultWrongContentType   FaultKind = "wrong_content_type"
	FaultEmptyBody          FaultKind = "empty_body"
	FaultExtraFields        FaultKind = "extra_fields"
)

// FaultAfter selects what happens once the attempt list is exhausted.
type FaultAfter string

// Supported post-exhaustion behaviours.
const (
	FaultAfterSuccess    FaultAfter = "success"     // default: serve the scenario response
	FaultAfterRepeatLast FaultAfter = "repeat_last" // permanent failure
)

// Fault is a deterministic per-provider failure plan. Attempt N of a route
// receives Attempts[N] after Repeat expansion; see §4.
type Fault struct {
	Attempts []FaultAttempt `yaml:"attempts"`
	After    FaultAfter     `yaml:"after,omitempty"`
}

// FaultAttempt is what one attempt against a route receives.
//
// Kind may be omitted and is then inferred: a Status of 400 or above with no
// other mangling field means FaultStatus; everything else unset means FaultNone.
// Delay is orthogonal and composes with every kind.
type FaultAttempt struct {
	Kind FaultKind `yaml:"kind,omitempty"`

	Status     int               `yaml:"status,omitempty"`
	Delay      Duration          `yaml:"delay,omitempty"`
	RetryAfter *int              `yaml:"retry_after,omitempty"` // seconds, sets Retry-After
	Headers    map[string]string `yaml:"headers,omitempty"`

	// Body is the verbatim error body. When nil the provider package synthesises
	// its documented shape for Status.
	Body map[string]any `yaml:"body,omitempty"`

	// Error and Tag fill the provider's error envelope without spelling out the
	// whole body. Tag is Exa-only.
	Error string `yaml:"error,omitempty"`
	Tag   string `yaml:"tag,omitempty"`

	// RawBody overrides the response bytes entirely, for FaultInvalidJSON.
	RawBody string `yaml:"raw_body,omitempty"`

	// ContentType overrides the Content-Type header, for FaultWrongContentType.
	ContentType string `yaml:"content_type,omitempty"`

	// TruncateAfterBytes is how many body bytes reach the client before the
	// connection dies, for FaultTruncateBody. Zero means half the body.
	TruncateAfterBytes int `yaml:"truncate_after_bytes,omitempty"`

	// Reset sends a TCP RST instead of a clean FIN for FaultTruncateBody, so a
	// client sees "connection reset by peer" rather than "unexpected EOF".
	Reset bool `yaml:"reset,omitempty"`

	ExtraFields ExtraFields `yaml:"extra_fields,omitempty"`

	// Repeat applies this attempt to N consecutive attempts. Zero and one are
	// equivalent. "Fail the first three then succeed" is one attempt with
	// Repeat: 3 and the default After.
	Repeat int `yaml:"repeat,omitempty"`
}

// Duration is a time.Duration that reads from a YAML scalar such as "250ms".
// It implements encoding.TextUnmarshaler, which yaml.v3 honours for scalar nodes
// (verified against yaml.v3 v3.0.1) and which encoding/json honours too.
type Duration time.Duration

// UnmarshalText parses a Go duration string.
func (d *Duration) UnmarshalText(text []byte) error

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration

// MarshalJSON renders the duration as a millisecond count so journal consumers in
// other languages do not have to parse Go duration syntax.
func (d Duration) MarshalJSON() ([]byte, error)
```

Loading, validation and rendering:

`scenario.Finding` and `journal.Finding` are deliberately separate types with the same name. They answer different
questions at different times: a scenario finding is an authoring problem found once at load, addressed by a YAML path,
and it can prevent the process from starting. A journal finding is a per-request observation about a *consumer's*
request, addressed by a JSON field name, and it never affects startup. Unifying them would force `scenario` to import
`journal` (or the reverse) for no shared behaviour, and would make "which findings block readiness" ambiguous.

```go
// Severity classifies a scenario validation finding.
type Severity string

// Finding severities.
const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Finding is one scenario validation result. It is distinct from journal.Finding,
// which records a per-request observation; see the note above.
type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Path     string   `json:"path,omitempty"` // YAML path, e.g. providers.exa.results[0].score
	Message  string   `json:"message"`
}

// Report is the outcome of validating a scenario.
type Report struct {
	Findings []Finding `json:"findings"`
}

// Errors returns only the error-severity findings.
func (r Report) Errors() []Finding

// Warnings returns only the warning-severity findings.
func (r Report) Warnings() []Finding

// OK reports whether the scenario is loadable, that is, has no error findings.
func (r Report) OK() bool

// Load reads, decodes, validates and resolves a scenario file. It returns the
// report even on failure so a caller can log every finding, not just the first.
// Decoding uses yaml.Decoder.KnownFields(true): an unknown scenario key is an
// authoring mistake and must fail loudly, unlike an unknown key in a provider
// request body, which must be tolerated.
//
// Load performs no path containment. It is for tests and testkit, which name
// their own fixtures. The binary must never call it: internal/server resolves a
// mounted scenario through config.OpenScenario, which is os.Root-confined, and
// then calls Parse on the bytes. Wiring Load into the server would silently
// bypass security requirement 7.
func Load(path string) (*Scenario, Report, error)

// LoadFS reads a scenario from an fs.FS, which is how the embedded built-in
// protocol scenarios are loaded.
func LoadFS(fsys fs.FS, name string) (*Scenario, Report, error)

// Parse decodes and validates a scenario from bytes without touching the file
// system. testkit's WithScenarioYAML option uses it.
func Parse(src []byte) (*Scenario, Report, error)

// Validate checks schema version, required fields, reference integrity, enum
// values and fault-plan coherence. It does not mutate the scenario.
func (s *Scenario) Validate() Report

// Empty returns a valid scenario with no sources and no projections. Every
// provider renders a well-shaped empty success against it, which makes
// exa.New(provider.Deps{}) a usable zero-configuration handler.
func Empty() *Scenario

// SeedKey returns the stable key derived identifiers hang off: Seed when set,
// otherwise Name, otherwise "servicesim".
func (s *Scenario) SeedKey() string

// BaseTime returns Time.Base, defaulting to 2026-01-01T00:00:00Z.
func (s *Scenario) BaseTime() time.Time

// RenderedSource is a provider-neutral view of a Source with defaults applied.
// Provider packages project this rather than reading Source directly, so
// defaulting rules live in one place.
type RenderedSource struct {
	ID          string
	URL         string
	Title       string
	Author      Nullable
	PublishedAt Nullable // RFC 3339, millisecond precision
	Text        string
	Snippets    []string
	Image       string
	Favicon     string
}

// Render applies defaults to a resolved reference and returns the neutral view.
// It reads ref.Target, which Resolve guarantees is non-nil; a zero RenderedSource
// with the ID set to ref.Ref is returned for the unresolved case so a render bug
// cannot become a nil dereference on a request path.
func Render(ref SourceRef) RenderedSource
```

### 2.2 `provider` — the seam every handler is built on

This is the answer to "how a handler gets its scenario projection, the journal, the fault engine, and the clock".

```go
// Package provider is the seam every Servicesim provider handler is built on. It
// owns provider identity, route declarations, the dependency set a handler is
// constructed with, the injectable clock, the fault-selection contract, fault
// execution, the mux builder, and the shared per-request lifecycle.
package provider

// Name identifies a simulated provider.
type Name string

// The simulated providers.
const (
	Exa        Name = "exa"
	Tavily     Name = "tavily"
	Perplexity Name = "perplexity"
)

// Route binds a ServeMux pattern to the fault budget it draws on and to the
// scenario field that budget is declared in.
type Route struct {
	// Pattern is a Go 1.22 ServeMux pattern, for example "POST /search".
	Pattern string

	// FaultKey is what attempt counting is keyed on. Two routes that are aliases
	// of one operation share a key, so a retry through the alias draws on the same
	// budget: Perplexity's /v1/sonar and /chat/completions are both
	// "perplexity:completions".
	FaultKey string

	// Fault selects this route's fault plan out of a scenario, for example
	// func(s *scenario.Scenario) *scenario.Fault { return s.Providers.Exa.Answer.Fault }
	// (nil-safe on every hop). It exists so the fault engine never has to know
	// which scenario field a key maps to: the package that declares the key
	// declares the mapping next to it, and internal/faults stays at level 4 with no
	// knowledge of provider/exa, provider/tavily or provider/perplexity. A nil
	// Fault means the route declares no plan.
	Fault func(*scenario.Scenario) *scenario.Fault
}

// Clock is injectable time with exactly one job: stamping journal timestamps.
// Response bodies never read it (§3.2), and fault delays deliberately do not go
// through it — see DelayMode. One method, because the two-method version made
// "how long the server sleeps" and "what the journal says the time is" the same
// knob, and any fake then broke client-deadline tests and AssertOverlapped at
// once.
type Clock interface {
	// Now returns the current instant. Two requests in flight must receive
	// distinguishable arrival and completion instants, which is what makes
	// testkit.AssertOverlapped meaningful; time.Now satisfies this and a clock
	// pinned to a constant does not.
	Now() time.Time
}

// SystemClock is the real-time Clock. It is the default in the binary, in the
// zero Deps, and in testkit.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time

// DelayMode selects what a delay fault does to the goroutine serving a request.
type DelayMode int

// Delay modes.
const (
	// DelayReal waits for the declared duration, cancellable by the request
	// context. It is the default, including in testkit, because a client deadline,
	// a context cancellation and a transport timeout are observed by *bytes not
	// arriving*: no in-process fake on the server side of the socket can produce
	// them. Timeout and deadline tests therefore use short scenario delays — the
	// plan's "very short configured durations" branch — not a fake clock.
	DelayReal DelayMode = iota

	// DelaySkip returns immediately and still records the requested delay in
	// Outcome.DelayMS, which is what a test asserting "the scenario asked for 30s"
	// compares against. It is the plan's "injectable clock" branch and it is what
	// keeps a 30-second backoff scenario free in unit tests. A test that asserts
	// context.DeadlineExceeded must not use it.
	DelaySkip
)

// sleep waits for d under mode, returning ctx.Err() when the context ended first
// so a delay fault yields promptly to a client deadline instead of pinning a
// goroutine. Under DelaySkip it returns nil immediately. Unexported: DelayMode is
// the entire public surface, and there is no FakeClock to keep in step with it.
func sleep(ctx context.Context, d time.Duration, mode DelayMode) error

// FaultDecision is the outcome of asking the fault engine what this attempt gets.
type FaultDecision struct {
	// Attempt is nil when this attempt is not faulted.
	Attempt *scenario.FaultAttempt

	// Index is the zero-based attempt number for the key, unique per arrival.
	Index int

	// Key is the fault budget this decision was drawn from.
	Key string

	// Planned reports whether Key has a non-empty expanded fault plan. It is what
	// keeps derived identifiers stable: the attempt index enters the identifier
	// tuple only for a route that actually declares a fault plan, so two identical
	// happy-path requests against one Sim render byte-identical bodies. See §3.1.
	Planned bool

	// Unknown reports that the engine holds no plan for Key at all — a route whose
	// key was never registered. Handle records a fault.unknown_key warning on the
	// entry so the drift is visible in /__admin/requests instead of silently
	// serving a fault-free 200 where the scenario declares a 429.
	Unknown bool
}

// Faulted reports whether a fault applies.
func (d FaultDecision) Faulted() bool

// Faults selects a fault for an attempt. Implementations must be safe for
// concurrent use; see §4.
type Faults interface {
	// Next claims the next attempt index for key and returns what it receives.
	Next(key string) FaultDecision

	// Reset returns every counter to zero.
	Reset()
}

// Deps is everything a provider handler is constructed with. The zero value is
// usable: exa.New(provider.Deps{}) serves well-shaped empty successes with no
// journal, no faults and a real clock.
type Deps struct {
	// Scenario is the loaded, validated, resolved corpus. nil means scenario.Empty().
	Scenario *scenario.Scenario

	// Journal records every request. nil means a fresh journal.NewDiscard(), which
	// is per-Deps and never shared, so two handlers in parallel tests cannot draw
	// sequence numbers from one another's counter.
	Journal journal.Journal

	// Faults selects deterministic failures. nil means no faults are applied at
	// all — including faults the Scenario declares. Because that combination is
	// almost always a wiring mistake rather than an intent, Normalized logs a
	// deps.faults_ignored warning when Scenario.HasFaults() is true and Faults is
	// nil. testkit.Start and internal/server always wire it; a consumer building
	// Deps by hand gets one from testkit.NewFaults(s).
	//
	// Normalized substitutes a no-op implementation for nil — returning
	// FaultDecision{Index: -1} with Unknown false, so the request path never
	// nil-checks and no request collects a spurious fault.unknown_key warning.
	Faults Faults

	// Clock stamps journal timestamps and nothing else. nil means SystemClock{}.
	Clock Clock

	// DelayMode selects whether a delay fault actually waits. The zero value is
	// DelayReal, so a scenario that declares delay: 250ms blocks a client for
	// 250ms whether it is served in-process or from the container.
	DelayMode DelayMode

	// Logger receives one structured event per completed request. nil means
	// slog.New(slog.DiscardHandler).
	Logger *slog.Logger

	// MaxRequestBytes bounds the request body read. Zero means 1 MiB.
	MaxRequestBytes int64

	// MaxJournalBodyBytes bounds the body stored per journal entry. Zero means 64 KiB.
	MaxJournalBodyBytes int
}

// Normalized returns a copy of d with every nil and zero field replaced by its
// documented default. Handler constructors call it once, so no request path ever
// nil-checks a dependency. It is also the one place a misconfiguration is
// reported: a scenario that declares faults with no Faults engine logs
// deps.faults_ignored at warn level, once per handler construction, rather than
// silently serving fault-free responses.
func (d Deps) Normalized() Deps
```

The per-request lifecycle. `Handle` is the single place routing, body reading, journaling, fault selection and fault
execution are wired together, so each provider package only writes provider semantics.

```go
// Exchange is one request in flight. Provider handlers read the decoded body and
// record findings on it; everything else is filled in by Handle.
type Exchange struct {
	Deps     Deps
	Provider Name
	Route    Route
	Request  *http.Request

	// Seq is the arrival-ordered journal sequence, claimed before the handler runs.
	Seq uint64

	// ArrivedAt is stamped from Deps.Clock before the body is read.
	ArrivedAt time.Time

	// Raw is the request body, bounded by Deps.MaxRequestBytes.
	Raw []byte

	// Body is Raw decoded as a generic JSON object. nil when the body was absent
	// or unparseable; findings say which.
	Body map[string]any

	// Auth is what was observed about credentials, produced by httpx.Observe over
	// httpx.ExtractCredentials. Never holds a credential value.
	Auth journal.AuthObservation

	findings []journal.Finding
}

// Warn records a warning finding. Warnings are journal-only: the request still
// receives its scenario response.
func (x *Exchange) Warn(code, field, format string, args ...any)

// Fail records an error finding. One error means the handler returns a
// provider-shaped 4xx instead of the scenario response.
func (x *Exchange) Fail(code, field, format string, args ...any)

// Failed reports whether any error finding was recorded.
func (x *Exchange) Failed() bool

// Findings returns the findings recorded so far, after the scenario's
// ValidationPolicy promotions and demotions have been applied, sorted into a
// total order: error severity before warning, then Field, then Code.
//
// The sort is not cosmetic. request.unknown_field is produced by walking the
// decoded body, which is a map[string]any, and Go randomises map iteration per
// run. Without a total order here, journal entries differ run to run — and
// Perplexity's 422 body is built directly from this slice, so its detail array
// would differ too, making a golden for a two-error request flake. Handlers must
// additionally walk body keys through slices.Sorted(maps.Keys(...)) so the
// *messages* are generated in a stable order as well; see §3.3.
func (x *Exchange) Findings() []journal.Finding

// String returns the request's string property at key, and whether it was present
// and of the right type. A present-but-wrong-type value records no finding by
// itself; the caller decides whether that is an error.
func (x *Exchange) String(key string) (string, bool)

// Number returns the request's numeric property at key.
func (x *Exchange) Number(key string) (float64, bool)

// Bool returns the request's boolean property at key.
func (x *Exchange) Bool(key string) (bool, bool)

// Object returns the request's object property at key.
func (x *Exchange) Object(key string) (map[string]any, bool)

// Has reports whether key is present at the top level of the request body.
func (x *Exchange) Has(key string) bool

// Response is what a provider handler decided to send, before faults are applied.
type Response struct {
	Status int
	Header http.Header

	// Body is the fully rendered response bytes, extras already merged.
	Body []byte

	// Label names the selection for the journal and logs, for example
	// "exa.search.ok" or "exa.error.INVALID_REQUEST_BODY".
	Label string

	// FaultEligible is set only when routing, authentication and validation all
	// passed. A rejected request must not consume a retry budget.
	FaultEligible bool
}

// Handler is a provider route handler: it turns an Exchange into a Response.
type Handler func(*Exchange) Response

// Handle wraps h with the shared lifecycle and returns an http.HandlerFunc:
// sequence claim, arrival stamp, bounded body read through httpx.ReadBody, JSON
// decode through httpx.DecodeObject, credential observation through
// httpx.Observe, handler call, fault selection through Deps.Faults, fault
// execution, journal append and one structured log event.
func Handle(d Deps, p Name, route Route, h Handler) http.HandlerFunc

// MuxSpec is everything a provider package supplies to build its listener's mux.
type MuxSpec struct {
	// Routes are the provider's real routes.
	Routes []Route

	// Handlers maps Route.Pattern to the handler serving it.
	Handlers map[string]Handler

	// NotFound answers any unknown path with a provider-shaped 404.
	NotFound Handler

	// MethodNotAllowed answers a known path with an unsupported method. allow is
	// the sorted list of methods that path does support, for the Allow header.
	MethodNotAllowed func(allow []string) Handler
}

// NewMux builds a provider listener's mux. It is exported and lives here, rather
// than being written three times in three provider packages, because the
// registration shape is load-bearing and easy to get subtly wrong: see §5.1. Its
// verification table (POST /search 200, GET /search 405 with Allow, POST /nope
// 404, POST /search/ 404) is provider/mux_test.go's test table.
func NewMux(d Deps, p Name, spec MuxSpec) *http.ServeMux
```

#### `Handle`'s body: four subtleties that are not optional

```go
func Handle(d Deps, p Name, route Route, h Handler) http.HandlerFunc {
	d = d.Normalized()
	return func(w http.ResponseWriter, r *http.Request) {
		x := &Exchange{Deps: d, Provider: p, Route: route, Request: r,
			Seq: d.Journal.Next(), ArrivedAt: d.Clock.Now()}

		entry := journal.Entry{
			Provider: string(p), Seq: x.Seq, Method: r.Method,
			// Path is r.URL.Path and Query is r.URL.RawQuery — never r.RequestURI
			// and never r.URL.String(), both of which render userinfo verbatim for
			// an absolute-form request target. journal.Redact masks the query.
			Path: r.URL.Path, Query: r.URL.RawQuery,
			Route: route.Pattern, RemoteAddr: r.RemoteAddr, ArrivedAt: x.ArrivedAt,
		}

		appended := false
		record := func() {
			if appended {
				return
			}
			appended = true
			entry.CompletedAt = d.Clock.Now()
			entry.Findings = x.Findings()
			entry.Headers = r.Header
			entry.Body = x.Raw
			entry.Auth = x.Auth

			// Redact BEFORE the logger sees it. Append redacts too, but Append takes
			// Entry by value, so the local entry would still hold the raw
			// Authorization header when logRequest ran. journal.Redact is idempotent
			// precisely so it can be called at both points.
			entry = journal.Redact(entry)
			d.Journal.Append(entry)
			logRequest(d.Logger, entry)
		}

		defer func() {
			rec := recover()
			record()
			if rec != nil {
				panic(rec)
			}
		}()

		// ... read body, decode, observe credentials, call h ...

		dec := d.Faults.Next(route.FaultKey) // only for a fault-eligible response
		if dec.Unknown {
			x.Warn("fault.unknown_key", "", "no fault plan registered for key %q", dec.Key)
		}
		out := faultOutcome(dec, resp)      // pure: computes the journal Outcome, writes nothing
		if out.Aborted {
			entry.Outcome = out
			record()                        // journal BEFORE the client can observe the abort
		}
		entry.Outcome = execute(r.Context(), w, dec.Attempt, resp, d.DelayMode, out)
	}
}
```

1. **The journal append is in a `defer`.** Transport faults abort the handler by panicking with `http.ErrAbortHandler`;
   without the defer their entries would be lost, and "unmatched traffic is journaled" would silently not hold for the
   most interesting cases.
2. **The recover re-panics.** `http.ErrAbortHandler` is a sentinel `net/http` interprets; swallowing it turns a
   connection-abort fault into a 200 with an empty body. Re-panicking after recording is the only correct shape.
3. **An aborting fault is journaled *before* the socket is touched, and `record` is idempotent.** `close_before_headers`
   hijacks and `Close`s the connection with `SetLinger(0)`; the client's `client.Do` returns "connection reset by peer"
   while the server goroutine is still unwinding. A test that called `sim.Requests(provider.Exa)` at that moment saw
   zero entries — on a fast machine, sometimes. Recording before the abort closes that window for the fault the design
   itself creates; `testkit.Sim.AwaitRequests` closes it for the symmetrical case the *client* creates (its own
   cancellation or deadline), where the server necessarily finishes after the client returns.

   **Shipped as (Phase 6 unit 1):** when the claimed attempt also carries a `delay:`, the hang is served FIRST and
   `record` runs after it, not before — so `completed_at` is the instant the socket was touched, not the instant
   the attempt was decided. The ordering relative to the abort itself does not change: the record still precedes
   `close_before_headers`/`truncate_body` touching the connection. A client cancellation during that hang is the
   symmetrical case named above: nothing is written, but the entry still lands, stamped at the instant the server
   observed the cancellation. Consequently the entry for a client-cancelled DELAYED aborting fault now lands
   *after* the client has returned — read it through `testkit.Sim.AwaitRequests`, not a synchronous
   `Requests()`/`Snapshot()`; before this unit it was already present (with the wrong `completed_at` and, for
   `truncate_body`, the wrong `bytes_written`) at that moment, because the early record ran before the delay.
4. **The entry is redacted where it is built, not only where it is stored.** `Journal.Append` takes `Entry` by value, so
   redacting inside `Append` leaves the caller's copy — the one `logRequest` is about to serialise — holding the raw
   `Authorization` header and the raw body. That leak is silent, because `testkit.AssertNoCredentialLeak` scans the
   journal, not the log. `TestHandle_LoggerNeverSeesRawCredential` installs a capturing `slog.Handler` and asserts the
   literal token is absent from every record.

#### Fault execution lives here, not in `internal/faults`

`Handle` is the only caller of fault execution, and `internal/faults` imports `provider`. A `provider` &rarr;
`internal/faults` call edge would therefore close a cycle and fail to compile. Execution needs nothing the engine has:
`net/http`, `scenario.FaultAttempt`, `journal.Outcome` and `DelayMode` are all already in `provider`. So
`provider/fault_exec.go` (U9) owns it:

```go
// faultOutcome computes the journal Outcome an attempt will produce, without
// writing anything. Splitting this out of execute is what lets Handle journal an
// aborting fault before the client can observe the abort. For aborting kinds the
// byte count is known in advance: zero for close_before_headers, the truncation
// length for truncate_body.
func faultOutcome(dec FaultDecision, resp Response) journal.Outcome

// execute applies a fault to the wire and returns the completed outcome (out with
// BytesWritten filled in). Delay runs first for every kind, under mode. For
// aborting kinds it does not return: it panics with http.ErrAbortHandler, and
// Handle's deferred record — already run for those kinds — is a no-op before the
// re-panic.
func execute(ctx context.Context, w http.ResponseWriter, a *scenario.FaultAttempt,
	resp Response, mode DelayMode, out journal.Outcome) journal.Outcome
```

**Shipped as (Phase 6 unit 1):** the pre-dispatch delay is no longer `execute`'s job — `preDispatchDelay(ctx, a,
mode)` (same file) runs it from `Handle`, before the non-streaming aborting record described under rule 3 above;
`execute` keeps `mode` only because `executeStream` still paces chunks through `sleep`.

There is no second `Response` type. The engine deals in `FaultDecision` and `scenario.FaultAttempt`; execution deals in
`provider.Response`; nothing converts between two near-identical structs.

### 2.3 `internal/journal`

```go
// Package journal records every request a provider listener handled, with
// credentials removed at the storage boundary and retention bounded.
package journal

// OutcomeKind classifies what the client received.
type OutcomeKind string

// Outcome kinds.
const (
	OutcomeScenario  OutcomeKind = "scenario"  // the scenario's response
	OutcomeError     OutcomeKind = "error"     // provider-shaped validation or auth error
	OutcomeFault     OutcomeKind = "fault"     // a declared fault
	OutcomeUnmatched OutcomeKind = "unmatched" // failed closed: unknown route or method
)

// Severity classifies a validation finding.
type Severity string

// Finding severities.
const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Finding is one validation warning or error recorded against a request.
type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Field    string   `json:"field,omitempty"`

	// Message is free text that may quote part of the request, so it is passed
	// through redact.String by Redact before it is stored, logged or served. A
	// finding raised *about* a credential-named field must never interpolate the
	// value at all: tavily.api_key.in_body states presence and nothing else.
	Message string `json:"message"`
}

// AuthObservation is what was observed about a request's credentials. It never
// holds a credential value.
type AuthObservation struct {
	// Present reports whether any recognised credential placement carried a value.
	Present bool `json:"present"`

	// Header is the placement used, lower-cased, for example "authorization".
	Header string `json:"header,omitempty"`

	// Scheme is the Authorization scheme, for example "Bearer". Empty for
	// non-scheme placements such as x-api-key.
	Scheme string `json:"scheme,omitempty"`

	// Fingerprint is the first eight hex characters of a domain-separated SHA-256
	// of the presented credential. It lets a test assert that two calls used the
	// same key without the journal ever holding one. It is not a secrecy boundary
	// for a low-entropy value: never point Servicesim at a real credential.
	Fingerprint string `json:"fingerprint,omitempty"`

	// Placements lists EVERY recognised placement that carried a value; the
	// fields above describe only the first. A client sending both a Bearer header
	// and an x-api-key is misconfigured in a way that works against a permissive
	// server and fails against a strict one, and recording one placement made
	// that invisible. len(placements) == 1 is how a consumer proves its adapter
	// sends exactly the credential the vendor documents.
	Placements []AuthPlacement `json:"placements,omitempty"`
}

// AuthPlacement is one credential placement observed on a request. It never holds
// a credential value, only the fingerprint of one.
type AuthPlacement struct {
	// Header is the placement, lower-cased: a header name such as
	// "authorization", or a non-header placement such as "body:api_key".
	Header string `json:"header"`

	// Scheme is the Authorization scheme, for example "Bearer".
	Scheme string `json:"scheme,omitempty"`

	// Fingerprint is on the same terms as AuthObservation.Fingerprint.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Outcome is what the request produced.
type Outcome struct {
	Kind         OutcomeKind `json:"kind"`
	Label        string      `json:"label,omitempty"`
	Status       int         `json:"status,omitempty"`
	FaultKind    string      `json:"fault_kind,omitempty"`
	FaultKey     string      `json:"fault_key,omitempty"`

	// AttemptIndex is the zero-based attempt number claimed for FaultKey, or -1
	// when the route had no fault plan.
	AttemptIndex int `json:"attempt_index"`

	// DelayMS is the delay the handler requested, not the delay it observed. Under
	// provider.DelaySkip no time passes, and this is still the asserted value.
	DelayMS int64 `json:"delay_ms,omitempty"`

	BytesWritten int  `json:"bytes_written"`
	Aborted      bool `json:"aborted,omitempty"`
}

// Entry is one recorded request.
type Entry struct {
	// Provider is the provider name as a plain string. It is deliberately not a
	// provider.Name: journal must not import provider, or the seam and the journal
	// form an import cycle.
	Provider string `json:"provider"`

	Seq    uint64 `json:"seq"`
	Method string `json:"method"`

	// Path is r.URL.Path and nothing else. r.RequestURI and r.URL.String() are
	// forbidden here, in logs, and inside wrapped errors: for a legal absolute-form
	// request target (POST http://user:secret@host/search HTTP/1.1) both render the
	// userinfo verbatim, which would put a credential in the journal, the admin API
	// and the log with no redaction pass able to recognise it. scripts/lint-no-
	// request-uri.sh fails the build on a RequestURI reference outside _test.go,
	// and TestJournal_AbsoluteFormRequestDoesNotLeakUserinfo replays such a request
	// through httptest.NewServer and asserts the literal appears in neither the
	// journal, the log, nor the response body.
	Path string `json:"path"`

	// Query is r.URL.RawQuery with credential-named parameter values masked by
	// redact.Query. An adapter that puts its key in the query string is exactly the
	// misconfiguration the journal exists to surface, so the parameter names are
	// preserved and only the values are masked.
	Query string `json:"query,omitempty"`

	Route      string `json:"route,omitempty"`
	RemoteAddr string `json:"remote_addr,omitempty"`

	// ArrivedAt and CompletedAt are real time, from provider.SystemClock by
	// default. They are the evidence testkit.AssertOverlapped rests on and are
	// never part of a response body; see §3.2.
	ArrivedAt   time.Time `json:"arrived_at"`
	CompletedAt time.Time `json:"completed_at"`

	// Headers are the request headers with credential-bearing values masked.
	Headers map[string][]string `json:"headers"`

	// Body is the decoded request body, re-encoded after redaction and bounded by
	// the configured limit. It is the parsed body, not the raw bytes, so a
	// credential-looking property is masked rather than stored verbatim.
	Body json.RawMessage `json:"body,omitempty"`

	// BodyTruncated reports that Body was clipped to the size limit.
	BodyTruncated bool `json:"body_truncated,omitempty"`

	// BodyParseError is set when the body was not valid JSON.
	BodyParseError string `json:"body_parse_error,omitempty"`

	Auth     AuthObservation `json:"auth"`
	Outcome  Outcome         `json:"outcome"`
	Findings []Finding       `json:"findings,omitempty"`
}

// Warnings returns the warning-severity findings.
func (e Entry) Warnings() []Finding

// Errors returns the error-severity findings.
func (e Entry) Errors() []Finding

// Stats describes journal retention.
type Stats struct {
	Capacity int    `json:"capacity"`
	Stored   int    `json:"stored"`
	Appended uint64 `json:"appended"`
	Dropped  uint64 `json:"dropped"`
}

// Journal is the request journal contract.
type Journal interface {
	// Next claims the next arrival-ordered sequence number. It is called before
	// the handler runs so sequence reflects arrival, while Snapshot order reflects
	// completion; an entry carries both timestamps so a test can sort either way.
	Next() uint64

	// Append stores a completed entry. Implementations must Redact the entry and
	// then bound it, in that order: redaction is enforced at the storage boundary
	// so no handler can forget it, and Redact is idempotent so a handler that
	// already redacted (provider.Handle does, before logging) costs only a second
	// pass. Reversing the order is a leak; see the Ring notes below.
	Append(e Entry)

	// Snapshot returns a deep copy of the stored entries in append order.
	Snapshot() []Entry

	// Reset clears stored entries and returns the sequence counter to zero.
	Reset()

	// Stats reports retention counters.
	Stats() Stats
}

// Ring is a bounded, concurrency-safe Journal. When full it drops the oldest
// entry and increments Stats.Dropped.
type Ring struct {
	seq      atomic.Uint64
	mu       sync.RWMutex
	entries  []Entry
	// ... head, appended, dropped, capacity, maxBodyBytes
}

// NewRing returns a Ring retaining at most capacity entries and at most
// maxBodyBytes of body per entry. A capacity of zero (or negative — a direct
// library caller must not be able to panic make) stores nothing while still
// allocating sequence numbers, so ordering assertions keep working with retention
// switched off. It returns a concrete *Ring: return structs, accept interfaces.
// Callers assign it to a Journal field.
func NewRing(capacity, maxBodyBytes int) *Ring

// NewDiscard returns a Journal that allocates sequence numbers and stores
// nothing. Each call returns a *fresh* instance with its own atomic counter.
//
// There is deliberately no package-level Discard value. A shared one is
// process-global mutable state in a design whose stated isolation boundary is the
// process: two Sims in parallel subtests would draw Seq from one counter, and
// Reset in one test would zero the sequence another test was mid-way through
// asserting on. That failure reproduces only under parallelism, in the helper the
// plan's Layer 2 tells consumers to use.
func NewDiscard() Journal

// Redact returns a copy of e with every credential-bearing value masked:
//
//	Headers            -> redact.Headers
//	Path               -> redact.String
//	Query              -> redact.Query
//	Body               -> redact.JSONBytes (which redacts even a non-JSON body)
//	BodyParseError     -> redact.String
//	Findings[].Message -> redact.String
//	Outcome.FaultKey   -> redact.String
//
// Outcome.FaultKey is the turn_key lane key (provider/lane.go turnLaneKey).
// turnLaneKey itself fingerprints a credential-NAMED or credential-SHAPED
// extractor value at composition time, before the key ever reaches Outcome, so
// by the time this pass runs the key should already carry no credential; this
// pass is belt-and-braces for whatever future extractor form does not go
// through turnLaneKey's own checks.
//
// It is idempotent, which is what allows provider.Handle to call it before
// logging and Append to call it again at the storage boundary. Every text field
// an entry carries is covered, not just the two obvious ones: a finding message
// that quotes a misplaced credential lands in the journal, the admin API, the log
// and — for error-severity findings — the HTTP error body.
func Redact(e Entry) Entry
```

Five implementation requirements:

- **`Append` redacts, then clips — never the reverse.** `Append` calls `Redact` and only then bounds `Body` to
  `maxBodyBytes`, setting `BodyTruncated`. Clipping first leaves a prefix that is no longer valid JSON, so
  `redact.JSONBytes` falls to its non-JSON branch and the document is masked by the weaker text matcher instead of the
  structural one. Clipping an already-redacted document can only drop trailing bytes, never expose a masked one. This
  is normative, and it is tested by `TestRing_OversizedBodyIsRedactedBeforeTruncation`: a body over `maxBodyBytes`
  carrying `"api_key"` in its first hundred bytes must store `[REDACTED]` and must not store the literal.
- **`Redact` covers every text field, and has a test per field.** `TestAppend_RedactsFindingMessages` and
  `TestAppend_RedactsQueryString` are named because those two are the fields an implementer reading "redaction is
  enforced at the storage boundary" would not think to cover.
- **`Deps.Normalized` substitutes `NewDiscard()`, not a shared value.** So does `NewRing` for a non-positive capacity.
- **`Snapshot` deep-copies** `Headers` (map and each slice) and `Body` (byte slice). Returning the stored maps would let
  a caller race with a concurrent `Append` under `go test -race`.
- **`NewRing` clamps a negative capacity to zero** rather than reaching `make([]Entry, capacity)`. `internal/config`
  rejects negative values with a named error before that ever happens; the clamp is for direct library callers.

`/__admin/requests` encodes with `json.Encoder` and **no indentation** by default. `scripts/image-smoke.sh` greps for
the literal `"provider":"exa"`; `SetIndent` would insert a space after the colon and the existing smoke test would fail.
`?pretty=1` opts into indentation.

### 2.4 `internal/redact`

```go
// Package redact removes credentials from anything Servicesim stores or emits.
package redact

// Mask replaces every redacted value.
const Mask = "[REDACTED]"

// Headers returns a copy of h with credential-bearing values masked. For
// Authorization the scheme is preserved — "Bearer [REDACTED]" — because
// authentication *placement* is exactly what an adapter contract test asserts,
// and masking the scheme would destroy the evidence while protecting nothing.
func Headers(h http.Header) http.Header

// IsCredentialHeader reports whether a header name carries a credential. It runs
// the same normalise-then-two-tier matcher as IsCredentialKey, so a
// vendor-prefixed header is caught: X-Exa-Api-Key, X-Tavily-Api-Key, Api_Key and
// X-Api-Key-2 all normalise into the substring tier. Applying a weaker rule to
// headers than to JSON properties would leak exactly the adapter mistake the
// journal exists to surface.
func IsCredentialHeader(name string) bool

// HeaderValue masks a single header value according to its name.
func HeaderValue(name, value string) string

// Query masks credential-named parameter values in a raw query string. It parses
// with url.ParseQuery, masks every value whose key satisfies IsCredentialKey, and
// re-encodes with url.Values.Encode, which sorts keys and is therefore
// deterministic. On a parse failure it falls back to String(raw) — never to raw.
func Query(raw string) string

// JSON walks a decoded JSON value and masks every string whose property name
// looks like a credential. Maps and slices are rebuilt rather than mutated, so
// the caller's value is untouched.
func JSON(v any) any

// JSONBytes decodes raw, redacts it, and re-encodes. When raw is not valid JSON
// it returns []byte(String(raw)) — redacted, not verbatim. The non-JSON branch is
// not an edge case: a form-encoded body (api_key=sk-live-...&query=x) reaches it,
// a truncated or trailing-comma body reaches it, and the repository ships a
// malformed-json scenario that exercises it deliberately. Returning raw there
// would store a credential byte-for-byte in the journal and in
// /__admin/requests. The body is still bounded and still stored, just masked.
func JSONBytes(raw []byte) []byte

// IsCredentialKey reports whether a JSON property name looks like a credential.
func IsCredentialKey(name string) bool

// String masks credentials inside free text: name=value and "name: value" pairs
// whose name satisfies IsCredentialKey (covering form-encoded bodies), Bearer
// tokens, and known vendor key prefixes. It is what protects finding messages,
// body parse errors and non-JSON bodies, all of which may quote part of a
// request.
func String(s string) string

// Fingerprint returns the first eight hex characters of a domain-separated
// SHA-256 of a credential, for AuthObservation.Fingerprint.
func Fingerprint(credential string) string
```

The matcher is where this package earns its keep. Names are normalised by lower-casing and stripping `_`, `-` and `.`,
then matched in two tiers:

```go
// exactCredentialNames match only as a whole normalised name. "auth" is here and
// nowhere else: as a substring it matches "author", which is a documented result
// field on both Exa and Tavily. Redacting every author name would corrupt the
// journal's evidence while protecting nothing.
var exactCredentialNames = map[string]bool{
	"auth": true, "authorization": true, "key": true, "apikey": true,
	"xapikey": true, "token": true, "secret": true, "password": true,
	"passwd": true, "pwd": true, "credential": true, "credentials": true,
	"signature": true, "sig": true, "sessionid": true, "cookie": true,
}

// substringCredentialFragments match anywhere in a normalised name. Every entry
// here must be checked against the three providers' documented field names before
// it is added.
var substringCredentialFragments = []string{
	"apikey", "accesstoken", "refreshtoken", "idtoken", "bearertoken",
	"clientsecret", "secretkey", "accesskey", "privatekey", "password",
	"credential",
}
```

**Header names run through the same matcher**, not a separate exact list. The exact tier seeds it with the known
placements — `authorization`, `proxy-authorization`, `x-api-key`, `api-key`, `x-api-token`, `x-auth-token`,
`x-goog-api-key`, `x-amz-security-token`, `cookie`, `set-cookie` — and the substring tier then catches the
vendor-prefixed variants an adapter under test actually invents: `X-Exa-Api-Key`, `X-Tavily-Api-Key`, `Api_Key`,
`X-Api-Key-2`. An earlier draft used an exact-only header list while the JSON matcher normalised and used substrings;
the asymmetry had no defence and a concrete leak behind it. The `Authorization` scheme is still preserved
(`Bearer [REDACTED]`), because placement is what an adapter contract test asserts.

Three named tests are mandatory:

- `TestJSON_DoesNotRedactAuthorField` — the `author` trap is the single most likely redaction bug in this repository.
- `TestIsCredentialHeader_VendorPrefixedVariants` — `X-Exa-Api-Key`, `Api_Key`, `X-Api-Key-2`.
- `TestJSONBytes_FormEncodedCredentialIsMasked` and `TestJSONBytes_TruncatedJSONCredentialIsMasked` — the non-JSON
  branch.

### 2.5 Fault selection (`internal/faults`) and fault execution (`provider`)

Two responsibilities, two packages, and the split is forced rather than stylistic. **Selection** — which attempt index
this request claims and what that attempt declares — is stateful, and its state must not be reachable from a consumer
importing `provider/exa`; it lives in `internal/faults` at level 4, which imports `provider`. **Execution** — how those
bytes reach the socket, or fail to — is called by `provider.Handle`, so it must live at or below `provider`; it lives
in `provider/fault_exec.go` (§2.2). Putting execution in `internal/faults` would require
`provider` &rarr; `internal/faults` &rarr; `provider`, which does not compile.

```go
// Package faults selects deterministic per-attempt failures. It does not touch
// the wire: execution lives in package provider, which calls it, and which this
// package imports.
package faults

// Engine selects faults. It is safe for concurrent use without locking on the hot
// path: the key set is fixed at construction from the loaded scenario, so the map
// is read-only afterwards and only the per-key counters are mutable.
type Engine struct {
	plans    map[string]plan          // read-only after New
	counters map[string]*atomic.Int64 // read-only map, mutable values
}

// plan is a Fault with Repeat expanded into a flat per-attempt slice.
type plan struct {
	attempts []scenario.FaultAttempt
	after    scenario.FaultAfter
}

// New builds an Engine from a scenario and the routes whose budgets it must
// serve. It expands each Repeat into consecutive attempts and pre-creates a
// counter for every route's FaultKey, including keys whose plan is empty.
// Pre-creation is what makes the counter map immutable and therefore lock-free to
// read; routes sharing a key (Perplexity's two patterns) collapse into one entry.
//
// The routes are passed in rather than derived here, because the keys are
// declared in provider/exa, provider/tavily and provider/perplexity at level 5,
// which this package must not import. Each route carries its own
// Route.Fault selector, so the scenario-field mapping travels with the key
// instead of being duplicated as a switch here — where a typo would silently
// disable a scenario's declared fault. internal/server and testkit build the
// slice by concatenating exa.Routes(), tavily.Routes() and perplexity.Routes().
func New(s *scenario.Scenario, routes []provider.Route) *Engine

// Next claims the next attempt index for key and returns what that attempt
// receives. It is the only mutating operation on the request path and is a single
// atomic add. An unregistered key yields FaultDecision{Index: -1, Key: key,
// Unknown: true}: no fault, and a fault.unknown_key warning recorded by
// provider.Handle, so a route added without a registered key is visible in
// /__admin/requests rather than silently serving a 200 where the scenario
// declares a 429.
func (e *Engine) Next(key string) provider.FaultDecision

// Reset stores zero into every counter.
func (e *Engine) Reset()
```

`Engine` implements `provider.Faults`. It is the only implementation in the module; `internal/server` and `testkit`
construct one and assign it to `Deps.Faults`. Consumers building `provider.Deps` by hand get one through
`testkit.NewFaults` (§2.10), because `internal/faults` is not importable from another module.

#### How each fault type is actually executed

Everything below is `provider/fault_exec.go` (U9), not `internal/faults` (U10). `delay` is orthogonal and runs first
for every kind.

| Kind | Mechanism | Notes |
|---|---|---|
| `delay` (modifier) | `sleep(ctx, d, mode)` | `DelayReal` (the default, including in testkit) selects on a `time.Timer` and `ctx.Done()`, so the delay is real on the wire — which is the only way a client deadline, a cancellation or a transport timeout can be exercised — while a client deadline still releases the goroutine instead of pinning it. `DelaySkip` returns immediately: this is what "no arbitrary multi-second sleeps in unit tests" means concretely. The journal records the *requested* delay under both modes. |
| `status` | `w.Header().Set(...)`, `w.WriteHeader(status)`, `w.Write(body)` | Plain `ResponseWriter`. `RetryAfter` sets `Retry-After` in seconds. The body is provider-shaped and built by the provider package, not here. |
| `wrong_content_type` | Same as a normal write with `Content-Type` overridden | Body bytes are the valid JSON response. Default override `text/html; charset=utf-8`. |
| `invalid_json` | Normal write, `Content-Type: application/json`, body replaced | Transport-valid, JSON-invalid: correct `Content-Length`, connection reusable. Distinct from truncation. Default body when `RawBody` is empty: `{"results": [{"title": "unterminated"` |
| `empty_body` | `w.Header().Set("Content-Length", "0")`, `w.WriteHeader(status)`, no write | A 200 with a zero-length body. Distinct from the `empty-results` *scenario*, which is a well-formed JSON body with an empty results array. |
| `extra_fields` | No transport change | Merged into the body by `wire.MergeJSON` before `execute` is reached. Listed as a fault kind for symmetry with the plan's catalogue. |
| `close_before_headers` | **`http.Hijacker`** | Cannot be done with a plain `ResponseWriter`: any write commits a status line. |
| `truncate_body` | **`http.Flusher` + `panic(http.ErrAbortHandler)`** | Cannot be done with a plain `ResponseWriter`: `net/http` computes `Content-Length` from the completed write, so a short write is indistinguishable from a short body. |
| `oversized_body` | Explicit `Content-Length` set before `w.WriteHeader`, then `w.Write(body)` followed by bounded `w.Write` calls against a reused 64 KiB whitespace buffer | **Shipped as (Phase 6 unit 3):** `Content-Length` must be declared exactly and up front — `net/http` falls back to chunked transfer encoding once the body spans several `Write` calls with no declared length, which a `Content-Length`-based size gate cannot see. The padding buffer is fixed-size and reused; `BodyBytes` is never allocated. |

The two transport faults, concretely. Both were executed against `httptest.NewServer` on Go 1.26.4 and the observed
client-side results are quoted.

```go
// closeBeforeHeaders sends nothing and destroys the connection.
func closeBeforeHeaders(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		// HTTP/2 has no Hijacker. Servicesim serves cleartext HTTP/1.1 only, so
		// this branch is unreachable in practice; aborting is the honest fallback.
		panic(http.ErrAbortHandler)
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	// SetLinger(0) makes Close emit RST rather than FIN. This matters: after a
	// clean FIN with zero bytes written, net/http.Transport may transparently
	// retry an idempotent request with a rewindable body, and the test that was
	// meant to observe a connection failure quietly observes a success instead.
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = conn.Close()
}
// Observed client-side: Post "...": read tcp ...: read: connection reset by peer
```

```go
// truncateBody declares the full length, sends a prefix, then aborts.
func truncateBody(w http.ResponseWriter, resp Response, n int) {
	w.Header().Set("Content-Type", contentTypeOf(resp))
	w.Header().Set("Content-Length", strconv.Itoa(len(resp.Body)))
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body[:n])

	// Flush pushes the partial bytes onto the wire. Without it net/http still
	// holds them in its buffer, ErrAbortHandler discards the buffer, and the
	// client sees a connection error with zero bytes — a connection fault, not a
	// truncation fault.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// ErrAbortHandler aborts the response without writing the remaining bytes and
	// without logging a stack trace. Returning normally instead would let net/http
	// decide, which is not a contract we should depend on.
	panic(http.ErrAbortHandler)
}
// Observed client-side: status 200, Content-Length: 64, 20 body bytes, then
// io.ErrUnexpectedEOF on read.
// With FaultAttempt.Reset the hijack variant is used instead and the client sees
// "connection reset by peer" after the same 20 bytes.
```

Two consequences worth stating once:

- `httptest.NewServer` is required for fault tests. Its `ResponseWriter` implements `Hijacker` and `Flusher` (verified).
  `httptest.NewRecorder` implements neither and will silently record a complete body for a truncation fault.
- The `panic(http.ErrAbortHandler)` must travel through `provider.Handle`'s recover. See §2.2.
- **Both of these destroy the connection while the handler goroutine is still unwinding**, so the journal entry for an
  aborting fault is written by `Handle` *before* `execute` is called, not by the deferred append. The deferred append
  is idempotent and becomes a no-op. Without that ordering, a client that observed the reset and immediately read the
  journal saw nothing — intermittently, and more often under `-race`.

### 2.6 `internal/httpx`, `internal/wire`, `internal/ids`

```go
// Package httpx holds the request-side checks every provider shares.
package httpx

// ReadBody reads at most limit bytes of r.Body. It returns ErrBodyTooLarge when
// the body exceeds the limit, using an http.MaxBytesReader so an oversized body
// is never fully buffered.
func ReadBody(r *http.Request, limit int64) ([]byte, error)

// ErrBodyTooLarge reports a request body over the configured limit. It is the
// single source of the request.body_too_large finding: provider.Handle calls
// ReadBody and maps this error, and no provider package may reimplement
// http.MaxBytesReader handling locally.
var ErrBodyTooLarge = errors.New("httpx: request body too large")

// DecodeObject decodes raw as a generic JSON object. Unknown properties are
// preserved, not rejected: a provider request body must tolerate additive vendor
// fields, unlike a scenario file.
func DecodeObject(raw []byte) (map[string]any, error)

// Credential is one observed credential placement.
type Credential struct {
	Header string // lower-cased header name
	Scheme string // "Bearer" for Authorization, empty otherwise
	Value  string // the credential; never journaled
}

// ExtractCredentials returns every recognised credential placement on r, in
// header order: authorization, x-api-key.
func ExtractCredentials(r *http.Request) []Credential

// Observe converts a credential into a journal observation, fingerprinting the
// value rather than carrying it.
func Observe(c Credential, present bool) journal.AuthObservation

// IsJSONContentType reports whether ct is application/json or a +json media type,
// ignoring parameters.
func IsJSONContentType(ct string) bool
```

```go
// Package wire renders provider response bodies.
package wire

// Render marshals v and merges extra into the resulting top-level object.
func Render(v any, extra map[string]any) ([]byte, error)

// MergeJSON merges extra into the top-level object of base. Keys in extra win.
// The merged output has alphabetically ordered keys because it round-trips
// through a map, which is why golden comparison is semantic rather than
// byte-for-byte; encoding/json's map key ordering is itself deterministic and
// recursive, so the bytes are stable run to run.
func MergeJSON(base []byte, extra map[string]any) ([]byte, error)

// Omit removes the named top-level properties from a rendered object, backing
// scenario.*Result.OmitFields.
func Omit(base []byte, fields []string) ([]byte, error)

// WriteJSON writes status, Content-Type and body, returning the byte count.
func WriteJSON(w http.ResponseWriter, status int, body []byte) int
```

```go
// Package ids derives every generated identifier from stable fixture keys, so a
// scenario replayed tomorrow produces the identifiers it produced today.
package ids

// Derive returns SHA-256 over the length-prefixed concatenation of parts. Length
// prefixing is not decoration: without it Derive("ab","c") and Derive("a","bc")
// collide, and a scenario named "exa" with route "search" would collide with one
// named "exas" with route "earch".
func Derive(parts ...string) [32]byte

// Hex32 returns the first sixteen derived bytes as thirty-two lowercase hex
// characters. This is the shape Exa documents for requestId — for example
// "b5947044c4b78efa9552a7c89b306d95" — not a readable slug.
func Hex32(parts ...string) string

// UUIDv5 returns an RFC 4122 version 5 UUID derived from parts under the
// Servicesim namespace. This is the shape Tavily documents for request_id and a
// safe default for a Perplexity completion id. crypto/sha1 is used because
// RFC 4122 specifies it for version 5; this is a naming use, not a security one.
func UUIDv5(parts ...string) string

// Float returns a stable value in [lo, hi) derived from parts, for synthesising
// plausible scores without a random source.
func Float(lo, hi float64, parts ...string) float64
```

### 2.7 `internal/config`

```go
// Package config resolves Servicesim's runtime configuration from flags,
// environment variables and defaults, in that order of precedence.
package config

// Listener is one bound HTTP surface.
type Listener struct {
	Port    int
	Enabled bool
}

// Config is the fully resolved runtime configuration.
type Config struct {
	BindAddress string

	Admin      Listener
	Exa        Listener
	Tavily     Listener
	Perplexity Listener

	// ScenarioPath is the scenario to load. A "builtin:" prefix selects an
	// embedded protocol scenario, for example "builtin:happy".
	ScenarioPath string

	// ScenarioRoot bounds scenario resolution. Defaults to the directory of
	// ScenarioPath. Nothing outside it can be opened, symlinks included.
	ScenarioRoot string

	JournalCapacity     int
	MaxRequestBytes     int64
	MaxJournalBodyBytes int

	ReadHeaderTimeout time.Duration
	ShutdownGrace     time.Duration

	LogLevel  slog.Level
	LogFormat string // json | text

	// StrictAuth makes a missing credential a 401 when the scenario does not say
	// otherwise.
	StrictAuth bool

	Healthcheck bool
	ShowVersion bool
}

// Load resolves configuration from args and an environment lookup. lookupEnv is
// injected rather than calling os.Getenv so config tests are hermetic and can run
// in parallel without t.Setenv serialising them; cmd/servicesim passes os.LookupEnv.
//
// Load validates every numeric bound before returning: a negative
// JournalCapacity, MaxRequestBytes or MaxJournalBodyBytes is an error naming the
// flag, not a value passed downstream. Otherwise --journal-capacity -1 panics in
// make([]Entry, capacity) at startup and the container reports unhealthy with a
// stack trace, and a negative --max-request-bytes clamps http.MaxBytesReader to
// zero, silently rejecting every request as too large. Zero keeps its documented
// meanings: retention disabled for capacity, "use the default" for the two byte
// limits.
func Load(args []string, lookupEnv func(string) (string, bool)) (Config, error)

// OpenScenario opens the configured scenario within ScenarioRoot. It uses
// os.Root, which refuses to traverse outside the root at the syscall level —
// including through a symlink, which a filepath.Clean prefix check does not catch.
// Verified on Go 1.26.4: opening "../etc/passwd" through an os.Root returns
// "path escapes from parent".
//
// The name handed to os.Root must be root-relative. Verified on Go 1.26.4:
// Root.Open rejects an absolute name even when it resolves inside the root
// ("openat /.../scen/x.yaml: path escapes from parent"), while Open("x.yaml")
// succeeds — and the plan's own documented invocation is
// --scenario /scenarios/fusion-overlap.yaml with ScenarioRoot defaulting to
// /scenarios, which is exactly that case. So OpenScenario computes
// rel, err := filepath.Rel(c.ScenarioRoot, c.ScenarioPath), rejects a rel that is
// absolute or starts with "..", and opens filepath.ToSlash(rel). A table test
// covers absolute-path-inside-root (succeeds), "../etc/passwd" (fails), and a
// symlink inside the root pointing outside it (fails) — the last is the case
// os.Root is here for and the one a prefix check would miss.
func (c Config) OpenScenario() (fs.File, string, error)

// Enabled returns the providers that have a listener enabled, in a stable order.
func (c Config) Enabled() []provider.Name
```

Precedence is **explicit flag > environment variable > built-in default**. The implementation detail that makes this
correct: `flag.FlagSet` cannot distinguish "set to the default value" from "not set", so `Load` registers flags with
built-in defaults, parses, collects the explicitly-set names with `fs.Visit`, and applies the environment only to names
`fs.Visit` did not report. Without that step `--exa-port 8081` and an unset flag are indistinguishable, and the
environment would silently override an explicit flag.

| Flag | Environment variable | Default |
|---|---|---|
| `--scenario` | `SERVICESIM_SCENARIO` | `builtin:happy` |
| `--scenario-root` | `SERVICESIM_SCENARIO_ROOT` | directory of `--scenario` |
| `--bind-address` | `SERVICESIM_BIND_ADDRESS` | `127.0.0.1` (the image sets `0.0.0.0`) |
| `--admin-port` | `SERVICESIM_ADMIN_PORT` | `8080` |
| `--exa-port` | `SERVICESIM_EXA_PORT` | `8081` |
| `--tavily-port` | `SERVICESIM_TAVILY_PORT` | `8082` |
| `--perplexity-port` | `SERVICESIM_PERPLEXITY_PORT` | `8083` |
| `--providers` | `SERVICESIM_PROVIDERS` | `exa,tavily,perplexity` |
| `--max-namespaces` | `SERVICESIM_MAX_NAMESPACES` | `1024` |
| `--max-jobs` | `SERVICESIM_MAX_JOBS` | `256` |
| `--journal-capacity` | `SERVICESIM_JOURNAL_CAPACITY` | `1000` (`0` disables retention) |
| `--max-request-bytes` | `SERVICESIM_MAX_REQUEST_BYTES` | `1048576` |
| `--max-journal-body-bytes` | `SERVICESIM_MAX_JOURNAL_BODY_BYTES` | `65536` |
| `--read-header-timeout` | `SERVICESIM_READ_HEADER_TIMEOUT` | `5s` |
| `--shutdown-grace` | `SERVICESIM_SHUTDOWN_GRACE` | `5s` |
| `--log-level` | `SERVICESIM_LOG_LEVEL` | `info` |
| `--log-format` | `SERVICESIM_LOG_FORMAT` | `json` |
| `--strict-auth` | `SERVICESIM_STRICT_AUTH` | `true` |
| `--healthcheck` | — | `false` |
| `--version` | — | `false` |

`--healthcheck` and `--version` are process modes, not configuration, and are deliberately flag-only: the container
`HEALTHCHECK` already invokes `/servicesim --healthcheck`, and an environment variable that could silently turn the
server into a health probe would be a footgun.

### 2.8 Provider packages

All three follow the same file shape: `doc.go`, `handler.go` (routing and lifecycle wiring), `request.go` (validation),
`response.go` (wire types), `render.go` (projection to wire types), `errors.go` (provider-shaped error bodies).

```go
// Package exa simulates the Exa search and answer API.
package exa

// Routes returns the routes this provider serves, in registration order. Each
// carries the fault budget it draws on and the selector for the scenario field
// that budget is declared in, so internal/server and testkit can build the fault
// engine's key set by concatenating exa.Routes(), tavily.Routes() and
// perplexity.Routes(). The keys are declared here, beside the patterns, and
// nowhere else — internal/faults never has to know that "exa:answer" means
// s.Providers.Exa.Answer.Fault.
//
// It is a function, not a package-level var, so no consumer can mutate the route
// table of a package it merely imported.
//
// /search and /answer draw on separate fault budgets: an /answer call must not
// consume /search's retries.
func Routes() []provider.Route

// RouteSearch returns POST /search, keyed "exa:search".
func RouteSearch() provider.Route

// RouteAnswer returns POST /answer, keyed "exa:answer". The plan does not mention
// this endpoint; it is documented at https://exa.ai/docs/reference/answer and a
// "search/answer API" simulator without it is incomplete.
func RouteAnswer() provider.Route

// New returns the Exa handler, built with provider.NewMux over Routes(). The zero
// Deps is usable: it serves well-shaped empty successes with no journal, no
// faults and a real clock. Note that a zero Deps means no faults *even if the
// Scenario declares them* — pass testkit.NewFaults(s) as Deps.Faults, or use
// testkit.Start, to get the scenario's declared faults; Deps.Normalized logs
// deps.faults_ignored if you do not.
func New(deps provider.Deps) http.Handler

// SearchResponse is the POST /search response body.
type SearchResponse struct {
	RequestID string   `json:"requestId"`
	Results   []Result `json:"results"`

	// CostDollars is always present on a real response and entirely absent from
	// the plan's example. A cost-tracking consumer parses it.
	CostDollars CostDollars `json:"costDollars"`

	// ResolvedSearchType is a deprecated legacy field that current production
	// responses may return as an empty string. Emitted only when the scenario asks
	// for it, so consumers are not encouraged to branch on it.
	ResolvedSearchType *string `json:"resolvedSearchType,omitempty"`

	// Context is a deprecated combined-context string.
	Context *string `json:"context,omitempty"`

	// Output is present only when the request supplied outputSchema.
	Output *Output `json:"output,omitempty"`
}

// Result is one Exa search result. There is deliberately no top-level score
// field: Exa's result schema has none, and emitting one would teach consumers to
// parse something the real API never sends. The only score-like field is
// HighlightScores.
type Result struct {
	Title string `json:"title"`
	URL   string `json:"url"`

	// ID is documented as "the temporary ID for the document" and Exa's own
	// example is a URL, not an opaque slug.
	ID string `json:"id,omitempty"`

	// PublishedDate is ISO 8601 and tri-state. Exa's reference page types it
	// non-nullable while the coding-agents guide types it string|null; the
	// simulator can emit absent, null or a value so a consumer is forced to handle
	// all three.
	PublishedDate scenario.Nullable `json:"publishedDate,omitzero"`

	// Author is explicitly anyOf[string, null].
	Author scenario.Nullable `json:"author,omitzero"`

	Text            string    `json:"text,omitempty"`
	Highlights      []string  `json:"highlights,omitempty"`
	HighlightScores []float64 `json:"highlightScores,omitempty"`
	Summary         string    `json:"summary,omitempty"`
	Image           string    `json:"image,omitempty"`
	Favicon         string    `json:"favicon,omitempty"`
	Subpages        []Result  `json:"subpages,omitempty"`
	Extras          *Extras   `json:"extras,omitempty"`
	Entities        []Entity  `json:"entities,omitempty"`
}

// CostDollars is the per-request cost breakdown.
type CostDollars struct {
	Total float64 `json:"total"`

	// Search carries the breakdown. Its "neural" key survives on the response side
	// even though "neural" was removed from the request type enum, so the key name
	// is emitted verbatim.
	Search *CostSearch `json:"search,omitempty"`
}

// CostSearch is the search-cost breakdown.
type CostSearch struct {
	Neural float64 `json:"neural"`
}

// ErrorResponse is Exa's canonical error body: a flat object, not a nested
// {error: {code, message}}.
type ErrorResponse struct {
	RequestID string `json:"requestId"`
	Error     string `json:"error"`
	Tag       string `json:"tag"`
}

// RateLimitResponse is Exa's 429 body, which uses a reduced shape carrying only
// "error" — no requestId and no tag. A simulator that always emitted
// ErrorResponse would be wrong for exactly this status.
type RateLimitResponse struct {
	Error string `json:"error"`
}

// Documented error tags.
const (
	TagInvalidRequestBody = "INVALID_REQUEST_BODY"
	TagInvalidAPIKey      = "INVALID_API_KEY"
	TagNoMoreCredits      = "NO_MORE_CREDITS"
	TagInvalidRequest     = "INVALID_REQUEST"
	TagInternalError      = "INTERNAL_ERROR"
	TagNotFound           = "NOT_FOUND"
	TagMethodNotAllowed   = "METHOD_NOT_ALLOWED"
)

// SearchTypes is the request `type` enum, quoted verbatim from Exa's own
// validation error message. "neural" is not a member.
var SearchTypes = []string{"auto", "fast", "instant", "deep-lite", "deep", "deep-reasoning"}

// Categories is the request `category` enum. Two members contain a space.
var Categories = []string{"company", "publication", "news", "personal site", "financial report", "people"}
```

One build-system detail the implementer must not discover the hard way: Exa's documented 429 message contains
`hello@exa.ai`, and `scripts/lint-no-live-hosts.sh` scans `provider/` for `exa\.ai`. The constant needs the marker
comment the script already supports:

```go
// defaultRateLimitMessage is Exa's documented 429 body text, reproduced verbatim
// so a consumer's error-message assertions match the real API.
const defaultRateLimitMessage = "You've exceeded your Exa rate limit of 10 requests per second. " +
	"If you want this increased, please email hello@exa.ai :)" // servicesim:allow-live-host
```

Tavily's 432 and 433 bodies mention `support@tavily.com` and need the same marker.

```go
// Package tavily simulates the Tavily search API.
package tavily

// Routes returns the routes this provider serves. See exa.Routes for why this is
// a function and why the fault-key selectors live here.
func Routes() []provider.Route

// RouteSearch returns POST /search, keyed "tavily:search". Its path collides with
// Exa's, which is why each provider gets its own listener.
func RouteSearch() provider.Route

// New returns the Tavily handler, built with provider.NewMux over Routes().
func New(deps provider.Deps) http.Handler

// SearchResponse is the POST /search response body.
type SearchResponse struct {
	Query string `json:"query"`

	// Answer is in the 200 schema's required list even though its description says
	// it appears only when include_answer is requested — the spec contradicts
	// itself. The simulator emits the key always, null when not requested, because
	// a required key that is sometimes missing breaks stricter consumers than a
	// present null does.
	Answer *string `json:"answer"`

	// Images items are objects, not bare URL strings.
	Images  []Image  `json:"images"`
	Results []Result `json:"results"`

	// ResponseTime is a JSON number. The plan encodes it as the string "1.15";
	// the schema declares type: number, format: float, and a string breaks any
	// typed consumer.
	ResponseTime float64 `json:"response_time"`

	RequestID      string         `json:"request_id,omitempty"`
	AutoParameters map[string]any `json:"auto_parameters,omitempty"`
	Usage          *Usage         `json:"usage,omitempty"`
}

// Result is one Tavily search result.
type Result struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`

	// RawContent is tri-state; Tavily's own documented example value is null.
	RawContent scenario.Nullable `json:"raw_content,omitzero"`

	Favicon string  `json:"favicon,omitempty"`
	Images  []Image `json:"images,omitempty"`
	ID      string  `json:"id,omitempty"`

	// PublishedDate is documented in prose for topic=news only, not in the
	// response schema. Emitted only when the request's topic is "news".
	PublishedDate string `json:"published_date,omitempty"`
}

// Image is one entry of an images array.
type Image struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// Usage is the credit-usage object gated by include_usage.
type Usage struct {
	Credits int `json:"credits"`
}

// ErrorResponse is the envelope every documented Tavily error uses — a nested
// object, not a flat {"error": ...}.
type ErrorResponse struct {
	Detail ErrorDetail `json:"detail"`
}

// ErrorDetail carries the message.
type ErrorDetail struct {
	Error string `json:"error"`
}

// Non-standard plan-limit statuses Tavily documents alongside the usual ones.
const (
	StatusPlanLimitExceeded  = 432
	StatusPayGoLimitExceeded = 433
)

// SearchDepths is the search_depth enum. Four values, not two.
var SearchDepths = []string{"advanced", "basic", "fast", "ultra-fast"}

// Topics is the topic enum. Three values; "finance" is current.
var Topics = []string{"general", "news", "finance"}

// TimeRanges is the time_range enum, long and single-letter aliases alike.
var TimeRanges = []string{"day", "week", "month", "year", "d", "w", "m", "y"}
```

```go
// Package perplexity simulates the Perplexity Sonar chat-completions API.
package perplexity

// Routes returns both patterns. They are aliases of one operation and therefore
// share a single fault budget: a client retrying through the OpenAI-compatible
// alias must not get a fresh set of retries. faults.New collapses the duplicate
// key into one counter.
func Routes() []provider.Route

// RouteSonar returns POST /v1/sonar, the canonical Sonar endpoint, keyed
// "perplexity:completions".
func RouteSonar() provider.Route

// RouteChatCompletions returns POST /chat/completions, the OpenAI SDK alias, on
// the same key.
func RouteChatCompletions() provider.Route

// New returns the Perplexity handler, built with provider.NewMux over Routes().
func New(deps provider.Deps) http.Handler

// CompletionResponse is the chat-completion response body.
type CompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"` // "chat.completion"
	Model   string   `json:"model"`
	Created int64    `json:"created"` // Unix seconds, a number
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`

	// SearchResults is the field consumers should parse.
	SearchResults []SearchResult `json:"search_results,omitempty"`

	// Citations is a bare URL array, deprecated in favour of SearchResults but
	// still present in the response schema. Emitted for legacy consumers; the plan
	// should not present it as something to depend on.
	Citations []string `json:"citations,omitempty"`

	Images           []Image  `json:"images,omitempty"`
	RelatedQuestions []string `json:"related_questions,omitempty"`
}

// Choice is one completion choice.
type Choice struct {
	Index        int     `json:"index"`
	FinishReason string  `json:"finish_reason"` // stop | length
	Message      Message `json:"message"`
}

// Message is a chat message.
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string, or an array of content chunks
}

// SearchResult is one entry of search_results.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`

	// Snippet defaults to the empty string rather than null or absent.
	Snippet string `json:"snippet"`

	Date        scenario.Nullable `json:"date,omitzero"`
	LastUpdated scenario.Nullable `json:"last_updated,omitzero"`

	// Source is "web" or "attachment". render.go fills it from
	// scenario.PerplexityResult.SourceType, which carries the YAML key
	// "source_type" because the scenario struct's inlined SourceRef already owns
	// the YAML key "source". The wire key is unaffected: it stays "source".
	Source string `json:"source"`
}

// Usage is the token and cost accounting.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// SearchContextSize echoes low|medium|high as a string. Encoding it as a
	// number is a common simulator bug.
	SearchContextSize *string `json:"search_context_size,omitempty"`

	CitationTokens   *int `json:"citation_tokens,omitempty"`
	NumSearchQueries *int `json:"num_search_queries,omitempty"`
	ReasoningTokens  *int `json:"reasoning_tokens,omitempty"`

	// Cost is required by the schema and omitted from the plan's example. A
	// consumer validating against the real schema would reject the plan's usage
	// object for missing it.
	Cost Cost `json:"cost"`
}

// Cost is the per-request dollar breakdown.
type Cost struct {
	InputTokensCost     float64  `json:"input_tokens_cost"`
	OutputTokensCost    float64  `json:"output_tokens_cost"`
	TotalCost           float64  `json:"total_cost"`
	ReasoningTokensCost *float64 `json:"reasoning_tokens_cost,omitempty"`
	RequestCost         *float64 `json:"request_cost,omitempty"`
	CitationTokensCost  *float64 `json:"citation_tokens_cost,omitempty"`
	SearchQueriesCost   *float64 `json:"search_queries_cost,omitempty"`
}

// ValidationErrorResponse is the only error body Perplexity formally schematises:
// the FastAPI HTTPValidationError returned for 422.
type ValidationErrorResponse struct {
	Detail []ValidationError `json:"detail"`
}

// ValidationError is one FastAPI validation failure.
type ValidationError struct {
	Loc  []any  `json:"loc"` // string or integer elements
	Msg  string `json:"msg"`
	Type string `json:"type"`
}

// MessageErrorResponse is the simulator-chosen body for every non-422 error,
// modelled on FastAPI's default {"detail": "<string>"}. Perplexity publishes no
// body shape for 400, 401, 403, 404, 429 or 500, so this is an inference and is
// recorded as unverified in contracts/perplexity/provenance.yaml. Note that
// `detail` is an array for 422 and a string here — both are legal FastAPI, and a
// consumer must not assume one type.
type MessageErrorResponse struct {
	Detail string `json:"detail"`
}

// Models is the model enum. "sonar-reasoning" without the -pro suffix was removed
// from the API on 2025-12-15 and is rejected rather than accepted.
var Models = []string{"sonar", "sonar-pro", "sonar-reasoning-pro", "sonar-deep-research"}

// SunsetDate is when Sonar chat completions stop being supported. The successor
// is the Agent API at /v1/agent. Logged once at startup; not a per-request finding.
var SunsetDate = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
```

### 2.9 `internal/admin` and `internal/server`

```go
// Package admin serves health, readiness and the redacted request journal.
package admin

// Handler builds the admin mux.
func Handler(deps Deps) http.Handler

// Deps is what the admin surface needs.
type Deps struct {
	Journal  journal.Journal
	Faults   provider.Faults
	Scenario *scenario.Scenario
	Report   scenario.Report
	Ready    *atomic.Bool
	Version  string
	Logger   *slog.Logger
}

// RequestsResponse is the GET /__admin/requests body.
type RequestsResponse struct {
	Entries []journal.Entry `json:"entries"`
	Stats   journal.Stats   `json:"stats"`
}

// ScenarioResponse is the GET /__admin/scenario body: what was loaded, and every
// validation warning that did not prevent loading.
type ScenarioResponse struct {
	Name     string             `json:"name"`
	Version  int                `json:"version"`
	Seed     string             `json:"seed"`
	Sources  int                `json:"sources"`
	Findings []scenario.Finding `json:"findings,omitempty"`
}
```

Routes: `GET /healthz`, `GET /readyz`, `GET /__admin/requests` (filters `?provider=`, `?since=`, `?limit=`,
`?pretty=1`), `GET /__admin/scenario`, `GET /__admin/jobs` (`?namespace=`, `?pretty=1`), `POST /__admin/reset`.
`POST /__admin/reset` clears the journal, the fault counters *and* the async job registry, and is the only mutating
admin endpoint. Per the plan it is a local-development convenience; parallel CI isolates by process, not by reset.

```go
// Package server composes handlers, binds listeners and manages lifecycle.
package server

// Server is a running Servicesim instance.
type Server struct { /* ... */ }

// New builds a Server from configuration. It loads and validates the scenario
// before binding anything: a scenario with error findings fails the process
// rather than serving a subtly wrong contract.
//
// It is also where the fault engine is constructed, because this is the lowest
// level that can see all three provider packages:
//
//	routes := slices.Concat(exa.Routes(), tavily.Routes(), perplexity.Routes())
//	engine := faults.New(s, routes)
//
// The same engine instance is wired into every provider's Deps.Faults and into
// admin.Deps.Faults, so POST /__admin/reset zeroes the counters the handlers
// actually consult. testkit does the identical wiring at level 7.
func New(cfg config.Config, logger *slog.Logger) (*Server, error)

// Run binds every enabled listener and blocks until ctx is done. The admin
// listener binds first so /healthz answers during provider bind, and readiness
// flips only once every listener is accepting.
func (s *Server) Run(ctx context.Context) error

// Addr returns the bound address of a surface, which is how a test that asked for
// port 0 discovers the real port.
func (s *Server) Addr(name string) string

// Shutdown stops every listener within the configured grace period.
func (s *Server) Shutdown(ctx context.Context) error
```

### 2.10 `testkit`

```go
// Package testkit runs Servicesim's provider handlers in-process for Go consumer
// tests, with no Docker and no credentials.
package testkit

// The journal alias set. It aliases every internal journal type reachable from
// Entry, plus the constants of those types, so a consumer outside this module can
// name them in assertions, in helper signatures, and in a Journal implementation
// of its own. The reason it must stay closed is in §1.3;
// examples/adapter/journal_test.go is the compile-time guard that keeps it so.
//
// In the source each alias carries its own doc comment starting with its name, as
// revive.toml requires; they are written out in §1.3 and elided here.
type (
	Entry           = journal.Entry
	Finding         = journal.Finding
	Journal         = journal.Journal
	Stats           = journal.Stats
	Outcome         = journal.Outcome
	OutcomeKind     = journal.OutcomeKind
	AuthObservation = journal.AuthObservation
	Severity        = journal.Severity
)

// Outcome kinds and finding severities, re-exported as constants of the aliased
// types so a consumer's table-driven test keeps its compile-time check.
const (
	OutcomeScenario  = journal.OutcomeScenario
	OutcomeError     = journal.OutcomeError
	OutcomeFault     = journal.OutcomeFault
	OutcomeUnmatched = journal.OutcomeUnmatched
	SeverityWarning  = journal.SeverityWarning
	SeverityError    = journal.SeverityError
)

// NewFaults returns the fault engine for a scenario, wired to every route all
// three provider packages declare. It exists because internal/faults is not
// importable from another module, and without it a consumer building
// provider.Deps by hand gets silently fault-free behaviour from a scenario that
// declares faults:
//
//	s, _, _ := scenario.Parse(src)
//	h := exa.New(provider.Deps{Scenario: s, Faults: testkit.NewFaults(s)})
//
// testkit.Start does this for you.
func NewFaults(s *scenario.Scenario) provider.Faults

// Option configures a Sim.
type Option func(*options)

// WithScenarioFile loads a scenario from disk.
func WithScenarioFile(path string) Option

// WithScenarioYAML parses a scenario from a literal, which keeps a single-purpose
// fixture next to the test that needs it.
func WithScenarioYAML(src string) Option

// WithScenario uses an already-built scenario.
func WithScenario(s *scenario.Scenario) Option

// WithBuiltin selects an embedded protocol scenario by name, for example "happy"
// or "rate-limited".
func WithBuiltin(name string) Option

// WithProviders limits which provider servers start.
func WithProviders(names ...provider.Name) Option

// WithClock injects the clock that stamps journal timestamps. The default is
// provider.SystemClock{} — real time — and a caller should have a specific reason
// to change it: AssertOverlapped compares arrival and completion instants across
// two entries, and a clock pinned to a constant makes every entry claim the same
// instant, so the assertion becomes either always-false or meaningless. It cannot
// affect a response body; nothing in one is ever read from a clock (§3.2).
func WithClock(c provider.Clock) Option

// WithSkippedDelays makes delay faults return immediately instead of waiting,
// while still recording the requested delay in Outcome.DelayMS. Use it for
// backoff tests that assert "the scenario asked for 30s" without paying 30s.
//
// Do not use it for timeout, deadline or cancellation tests. Those are observed
// by bytes not arriving, so they need a real delay: declare a short one
// (delay: 150ms) in the scenario and leave delays real. The default is real
// delays precisely so that a scenario behaves identically in-process and in the
// container.
func WithSkippedDelays() Option

// WithJournalCapacity bounds journal retention.
func WithJournalCapacity(n int) Option

// Sim is a running set of provider servers.
type Sim struct { /* ... */ }

// Start brings up one httptest.Server per enabled provider and registers
// cleanup on tb, so a caller needs no defer. It uses httptest.NewServer rather
// than httptest.NewRecorder because only a real server's ResponseWriter
// implements http.Hijacker and http.Flusher, which the transport faults require.
//
// The defaults are the production ones, deliberately: a real clock, real delays,
// and a fault engine wired from the scenario over every declared route. A test
// that opts out of one of those (WithSkippedDelays, WithClock) is opting out of
// fidelity, and each option says what it costs.
func Start(tb testing.TB, opts ...Option) *Sim

// URL returns the base URL for a provider, suitable for an adapter's base-URL
// configuration.
func (s *Sim) URL(p provider.Name) string

// BaseURLs returns the environment-variable-shaped base URLs, keyed
// EXA_BASE_URL, TAVILY_BASE_URL and PERPLEXITY_BASE_URL, so a test can configure
// a consumer exactly as Compose would.
func (s *Sim) BaseURLs() map[string]string

// Handler returns a provider's handler for a caller wiring its own server.
func (s *Sim) Handler(p provider.Name) http.Handler

// Client returns an http.Client with keep-alives disabled, so a connection-abort
// fault is observed rather than absorbed by a pooled connection.
func (s *Sim) Client() *http.Client

// Journal returns every recorded entry, in completion order.
func (s *Sim) Journal() []Entry

// Requests returns the entries for one provider, in arrival-sequence order.
func (s *Sim) Requests(p provider.Name) []Entry

// AwaitRequests blocks until p has recorded n entries, or fails tb after a short
// deadline. It is mandatory before asserting on the journal following a request
// that ended at the transport level — an aborting fault the client saw as
// "connection reset by peer", or a request the client cancelled or timed out.
// In those cases the client returns while the server goroutine is still
// completing the entry, and a bare Requests call is a race that passes on a slow
// machine and fails on a fast one.
func (s *Sim) AwaitRequests(tb testing.TB, p provider.Name, n int) []Entry

// Reset clears the journal and every fault counter.
func (s *Sim) Reset()

// Close stops every server. Start already registers it with tb.Cleanup.
func (s *Sim) Close()

// ExaHandler returns the Exa handler for a consumer wiring one provider into its
// own httptest.Server, together with the Sim that owns its journal and fault
// engine. The Sim is returned rather than discarded because every assertion in
// this package needs an Entry, and the only source of an Entry is the Sim: a
// bare http.Handler lets a consumer assert "I got a response" and nothing about
// the vendor request it sent, which is the whole point of the journal.
//
// The Sim's provider servers are not started; only the handler is built. Close is
// still registered on tb.
func ExaHandler(tb testing.TB, opts ...Option) (http.Handler, *Sim)

// TavilyHandler returns the Tavily handler and its Sim.
func TavilyHandler(tb testing.TB, opts ...Option) (http.Handler, *Sim)

// PerplexityHandler returns the Perplexity handler and its Sim.
func PerplexityHandler(tb testing.TB, opts ...Option) (http.Handler, *Sim)
```

```go
// Assertions. Each takes testing.TB and reports through tb.Helper so a failure
// points at the caller's line.

// AssertRequestCount asserts how many requests a provider received.
func AssertRequestCount(tb testing.TB, s *Sim, p provider.Name, want int)

// AssertBearerAuth asserts the request presented a Bearer credential in the
// Authorization header.
func AssertBearerAuth(tb testing.TB, e Entry)

// AssertAPIKeyHeader asserts the request presented an x-api-key credential.
func AssertAPIKeyHeader(tb testing.TB, e Entry)

// AssertSameCredential asserts two requests presented the same credential, by
// comparing fingerprints rather than values.
func AssertSameCredential(tb testing.TB, a, b Entry)

// AssertNoCredentialLeak scans every journal entry for the given literals and
// fails if any appears. The scan covers every text-bearing field, not just the
// two obvious ones: Headers, Body, Query, Path, Outcome.Label,
// BodyParseError and every Finding.Message. It is the assertion form of security
// requirement 1.
func AssertNoCredentialLeak(tb testing.TB, s *Sim, literals ...string)

// AssertJSONBody asserts the recorded request body matches want semantically,
// using go-cmp over decoded values so JSON key order is not part of the contract.
func AssertJSONBody(tb testing.TB, e Entry, want any)

// AssertFindings asserts the exact set of finding codes recorded, in any order.
func AssertFindings(tb testing.TB, e Entry, wantCodes ...string)

// AssertNoErrors asserts the request produced no error-severity findings. This is
// the default way a test says "my adapter sent a correct vendor request".
//
// It is deliberately not AssertNoFindings. §6.2 makes request.unknown_field and
// request.content_type warnings so that a consumer sending a legitimate field
// Servicesim has not modelled yet still gets a green request and a 200; asserting
// on *no findings at all* re-imposes exactly the strictness that policy exists to
// avoid, and forces the consumer to enumerate warnings to express "no errors".
func AssertNoErrors(tb testing.TB, e Entry)

// AssertNoFindings asserts the request produced no warnings and no errors. It is
// the strict form, for a test that wants to pin the warning set too.
func AssertNoFindings(tb testing.TB, e Entry)

// AssertOverlapped asserts two requests were in flight simultaneously, which is
// how a fusion test proves its provider calls ran concurrently rather than
// serially: a arrived strictly before b completed and b arrived strictly before a
// completed. It compares real-time journal timestamps, which is why the default
// clock is SystemClock and why WithClock carries a warning.
func AssertOverlapped(tb testing.TB, a, b Entry)

// GoldenOption tunes AssertGoldenJSON.
type GoldenOption func(*goldenOptions)

// GoldenIgnore excludes additional top-level or dotted JSON paths from the
// comparison.
func GoldenIgnore(paths ...string) GoldenOption

// GoldenExactIDs opts back into comparing the derived identifier fields that are
// ignored by default.
func GoldenExactIDs() GoldenOption

// AssertGoldenJSON compares got against the golden file at path, semantically:
// both sides are decoded into any and diffed with go-cmp, because extra-field
// merging reorders keys and JSON object order is not part of any of these wire
// contracts (§3.3).
//
// By default it ignores the derived identifier fields — requestId, request_id and
// the top-level Perplexity id — because a route with a declared fault plan varies
// them per attempt by design (§3.1). GoldenExactIDs opts back in.
//
// It performs a plain comparison and knows nothing about provenance: a consumer's
// goldens live in the consumer's repository, and failing their first call with an
// error about a provenance.yaml convention that exists only inside this repo
// would be indefensible. This repository's own provenance requirement — every
// golden has an entry recording the documentation URL and verification date — is
// enforced by contracts/contracts_test.go (U5), over contracts/**, where it
// belongs.
//
// Updating a golden is opt-in through the environment, never through a flag:
// SERVICESIM_UPDATE_GOLDEN=1 rewrites it. testkit must not call flag.Bool at
// package scope, because a consumer package that already defines its own -update
// golden flag — a near-universal Go convention — would panic at init with "flag
// redefined: update" and take down the whole test binary with no visible link to
// Servicesim.
func AssertGoldenJSON(tb testing.TB, path string, got []byte, opts ...GoldenOption)
```

Importing `testing` from a non-test package is safe on modern Go: `testing` registers its flags in `testing.Init()`,
called from the generated test main, not at package initialisation, so a consumer's production binary that transitively
imports `testkit` does not acquire `-test.*` flags. The same reasoning is why `AssertGoldenJSON` reads an environment
variable rather than registering a `-update` flag: `flag.Bool` at package scope *does* run at init, and it collides.

Three `testkit` tests are not optional, because they are the ones that would have caught the defects this section is
written against, and all three must run **under the default configuration**:

- Two provider calls issued concurrently, asserting `AssertOverlapped` passes; and two issued serially, asserting it
  fails. Plan acceptance criterion 9 and Phase 4's "confirm request concurrency using journal timestamps" rest on this.
- A scenario declaring `delay: 150ms` served to a client with a 50 ms deadline, asserting the client observes
  `context.DeadlineExceeded` — fusion invariant 9's mechanism, and the thing a fake clock silently made untestable.
- An aborting fault (`close_before_headers`) followed by `AwaitRequests`, asserting the entry is present with
  `Outcome.Aborted`.

---

## 3. Determinism strategy

The rule is one sentence: **nothing in a response body is ever read from a clock or a random source.** Everything
derives from the scenario's stable keys.

### 3.1 Identifiers

All three identifiers come from `internal/ids` over the same tuple, and the tuple has **two shapes**:

```text
parts = (scenario.SeedKey(), provider, faultKey)                // FaultDecision.Planned == false
parts = (scenario.SeedKey(), provider, faultKey, attemptIndex)  // FaultDecision.Planned == true
```

| Field | Derivation | Shape it must match |
|---|---|---|
| Exa `requestId` | `ids.Hex32(parts...)` | 32 lowercase hex characters, matching Exa's documented `b5947044c4b78efa9552a7c89b306d95`. **Not** the plan's `exa-request-1`: a consumer regex or length assumption trained on a slug breaks against the real API. |
| Exa `results[].id` | the source URL, or the projection's explicit `id` | Exa's own example puts a URL there. **Not** the plan's `source-1`. |
| Tavily `request_id` | `ids.UUIDv5(parts...)` | RFC 4122 v5 UUID, matching Tavily's documented `123e4567-...`. **Not** the plan's `tavily-request-1`. |
| Perplexity `id` | `ids.UUIDv5(parts...)` | Docs say only "opaque completion id" and pin no prefix, so an unprefixed UUID is the safest default. Overridable with `completion_id`. |
| Tavily `results[].id` | `ids.Hex32(seed, "tavily", sourceID)[:8]` shaped as `xxxxxx-NN` | Matches the documented example `a3f9c2-04`. |

`ids.Derive` length-prefixes each part before hashing. Without that, `("exa","search")` and `("exas","earch")` produce
the same digest — a collision class that would be nearly impossible to debug from a golden diff.

The attempt index enters the tuple **only when the route actually declares a fault plan**, and that condition is the
whole point of `FaultDecision.Planned`. Where a plan exists, a retried request gets a *different* request ID — what a
real API does, and what lets a test tell the retry apart from the original in the journal. Where no plan exists, the
identifier must not move: the engine pre-creates a counter for every key, so every fault-eligible request claims a
fresh index whether or not anything is faulted, and folding that index into the tuple would mean the same request sent
twice against one `Sim` rendered two different bodies. A table-driven consumer test comparing both cases to one golden
would fail on the second, with a diff on an opaque hex string and no hint that call ordinal was the cause; two
concurrent requests would claim indices in nondeterministic order and produce run-to-run-different identifiers, which
contradicts both the plan's "the same scenario and request must produce the same response, identifiers, ordering" and
this section's own opening rule.

Because a route *with* a plan still varies its identifier per attempt by design, `testkit.AssertGoldenJSON` ignores
`requestId`, `request_id` and the top-level Perplexity `id` by default, with `GoldenExactIDs()` to opt back in (§2.10).

### 3.2 Timestamps

Two time sources, deliberately separated:

- **Response payload timestamps** come from `Scenario.BaseTime()`, default `2026-01-01T00:00:00Z`. Perplexity's
  `created` is `Providers.Perplexity.Created` when set, otherwise `BaseTime().Unix()`. It is therefore identical across
  runs, across machines, and whatever `Deps.Clock` is. A golden containing `"created": 1767225600` stays valid forever.
- **Journal timestamps** (`ArrivedAt`, `CompletedAt`) come from `Deps.Clock`, which defaults to `SystemClock{}`
  everywhere — the binary, the zero `Deps`, and `testkit`. They are the only place a clock is read, and they never
  reach a response body, so no consumer assertion on a *response* can be made flaky by them.

  They are real time deliberately. `AssertOverlapped` is specified over these two instants, and it is how the plan's
  acceptance criterion 9 and Phase 4 are satisfied; a clock that only moves when a test tells it to gives every entry
  the same instant, which makes the assertion either always-false (strict comparison) or vacuous (non-strict). A
  virtual clock shared across in-flight requests is worse: one delayed request inflates the recorded duration of every
  other request in flight. That is why there is no `FakeClock` in this design at all, and why *delays* are governed by
  `Deps.DelayMode` (§2.2) rather than by a clock — a delay that a client is meant to observe cannot be faked on the
  server side of the socket.

`results[].publishedDate` renders from `Source.PublishedAt` in RFC 3339 with **millisecond precision**
(`2023-11-16T01:36:32.547Z`), matching Exa's documented example. The plan's whole-second example is compatible with
"ISO 8601" but does not match the shape Exa actually publishes, and precision is exactly the kind of thing a consumer's
date parser gets wrong.

Tavily's `response_time` is a scenario constant, never a measurement. Measuring it would make it the one
nondeterministic field in an otherwise deterministic response.

### 3.3 Ordering

- Results are emitted in **scenario declaration order**. No sorting by score, ever. Sorting would make output depend on
  float comparison and would silently reorder equal scores.
- `numResults` and `max_results` truncate by taking the first N in declaration order.
- Go map iteration is never used to build a JSON array. Where a map is marshalled directly — `ExtraFields`,
  `auto_parameters` — `encoding/json` sorts keys deterministically and recursively (verified: three consecutive
  marshals of a nested map produced byte-identical output), so map-valued fields are stable too.
- Extra-field merging round-trips the body through a map, which re-orders struct-derived keys alphabetically. The bytes
  are still deterministic; they are simply not in struct order. **Golden comparison is therefore semantic** —
  `json.Unmarshal` both sides into `any` and `cmp.Diff` — for every golden. JSON object key order is not part of any of
  these wire contracts, so a byte-level golden would only ever manufacture false failures.
- **Go map iteration is never used to order findings, and never to order a response array.** Two places would otherwise
  do exactly that. `request.unknown_field` is raised by walking the decoded body, a `map[string]any`: handlers iterate
  `slices.Sorted(maps.Keys(body))`, and `Exchange.Findings()` then applies a total order (error before warning, then
  `Field`, then `Code`). This is load-bearing beyond tidiness, because Perplexity's 422 body is
  `{"detail":[{loc,msg,type},…]}` built straight from that slice — array order *is* semantic, so an unsorted findings
  list makes the 422 body itself differ run to run. `provider.NewMux` has the same hazard: it registers method-less
  patterns from a `map[path][]method`, so it iterates sorted paths and emits a sorted `Allow` header. The required
  test decodes a body with five unknown fields, runs the handler twenty times, and asserts identical finding order and
  identical 422 bytes every time.

### 3.4 What is deliberately not deterministic

`Fingerprint` is deterministic. Three things are not, and are excluded from golden comparison:

- `RemoteAddr` and the ephemeral ports `httptest` chooses.
- `Entry.ArrivedAt` and `Entry.CompletedAt`, which are real time by design (§3.2). A golden over an
  `/__admin/requests` body must ignore both.
- The derived identifier fields on a route that declares a fault plan, because the attempt index is part of their
  derivation there (§3.1). `testkit.AssertGoldenJSON` ignores them by default.

A scenario may opt into random IDs later for tests that specifically exercise nondeterministic identifiers; that is the
only escape hatch and it does not exist in the initial release.

---

## 4. Attempt counting and concurrency

### 4.1 What the counter is keyed on

The key is `provider.Route.FaultKey`, a string the route declares:

| Route | Fault key |
|---|---|
| `POST /search` on the Exa listener | `exa:search` |
| `POST /answer` on the Exa listener | `exa:answer` |
| `POST /search` on the Tavily listener | `tavily:search` |
| `POST /v1/sonar` on the Perplexity listener | `perplexity:completions` |
| `POST /chat/completions` on the Perplexity listener | `perplexity:completions` |

Two decisions are encoded there. Exa's `/search` and `/answer` have **separate** budgets, so an answer call cannot
silently consume the retries a search test declared. Perplexity's two routes **share** a budget, because they are
aliases of one operation and a client that retries through the OpenAI-compatible alias must not be handed a fresh set of
retries.

Rejected: keying on a request-body fingerprint. It would make "retry the identical call" work and "retry with a
slightly different body" mysteriously not work, and it would add hashing to the hot path to buy a behaviour no consumer
asked for.

### 4.2 Synchronisation

The scenario is immutable after load, so the *set* of fault keys is fixed at engine construction — provided the set is
handed in. It is: `New` takes the routes, and each route carries both its key and the selector for the scenario field
that key's plan lives in. `New` pre-creates a counter for **every** key, including keys whose plan is empty. That makes
`Engine.counters` read-only after construction, and a read-only map needs no lock:

```go
func New(s *scenario.Scenario, routes []provider.Route) *Engine {
	e := &Engine{plans: map[string]plan{}, counters: map[string]*atomic.Int64{}}
	for _, rt := range routes { // every declared route, not just faulted ones
		if _, seen := e.counters[rt.FaultKey]; seen {
			continue // aliases share one budget: perplexity's two patterns
		}
		var f *scenario.Fault
		if rt.Fault != nil {
			f = rt.Fault(s)
		}
		e.plans[rt.FaultKey] = expand(f)
		e.counters[rt.FaultKey] = new(atomic.Int64)
	}
	return e
}

func (e *Engine) Next(key string) provider.FaultDecision {
	c, ok := e.counters[key]
	if !ok {
		// Unknown key: fail open to "no fault" rather than panicking, but say so.
		// Handle turns Unknown into a fault.unknown_key warning, because the silent
		// version of this branch means a scenario's declared 429 never fires and
		// the consumer sees a 200 with no log line, no finding and no failing test
		// inside servicesim.
		return provider.FaultDecision{Index: -1, Key: key, Unknown: true}
	}
	idx := int(c.Add(1) - 1) // claim a unique zero-based index
	p := e.plans[key]
	return provider.FaultDecision{Attempt: p.at(idx), Index: idx, Key: key,
		Planned: len(p.attempts) > 0}
}

func (e *Engine) Reset() {
	for _, c := range e.counters {
		c.Store(0)
	}
}
```

So: **one atomic add per request, no mutex anywhere on the request path.** `plan.at` is a pure function over an
immutable slice and is trivially safe to call concurrently.

The alternative — a `sync.Mutex` around a `map[string]int` — would also be correct, and is what most implementations
reach for because the read-select-increment looks like a compound operation. It is not: `Add` returns the claimed value,
so selection can happen after the claim with no window in between. Pre-materialising the key set is what removes the
last reason for the lock.

`go test -race` over concurrent requests against a faulted route is a required test, not an optional one.

### 4.3 Selection, `Repeat`, and the N+1th attempt

`expand` flattens `Repeat` at construction, so the request path indexes a flat slice:

```yaml
fault:
  attempts:
    - status: 429
      retry_after: 1
      repeat: 3
  after: success       # the default
```

expands to `[429, 429, 429]` with `after: success`. For attempt index `i` against a plan of length `L`:

| Condition | Result |
|---|---|
| `i < L` | `attempts[i]` |
| `i >= L` and `after == success` | no fault — the scenario response is served |
| `i >= L` and `after == repeat_last` | `attempts[L-1]`, permanently |

The N+1th attempt is therefore index `N`, which is `>= L`, which is the success branch. "Fail the first N attempts then
succeed" is a one-line scenario, and "one provider fails permanently" (fusion invariant 9) is `after: repeat_last`.

The plan's own example — `attempts: [{status: 429, ...}, {status: 200}]` — still works unchanged: the trailing
`status: 200` infers `FaultNone` and renders the scenario response.

### 4.4 Concurrency semantics that must be documented, not discovered

**The counter counts arrivals, not completions.** The index is claimed before the response is written. Two concurrent
requests receive indices 0 and 1 and may complete in either order — so a test that fires two requests in parallel
against `[429, 200]` cannot assert which one got the 429. That is correct behaviour, and a test needing a strict
sequence must issue its requests serially. This is stated loudly because the alternative is an intermittently failing
test that looks like a simulator bug.

**Only a request that survived routing, authentication and validation consumes an attempt.** The pipeline is:

```text
route match -> authentication -> request validation -> fault selection -> render
```

A 404, a 405, a 401 or a 4xx validation failure returns before `Faults.Next` is called and leaves the counter untouched.
A malformed request can therefore never silently eat a retry budget, which would otherwise make a fault scenario
sensitive to a consumer's unrelated request bug. The one consequence: the fault list cannot express "hang up on a
malformed request". Nothing in the plan asks for that.

**`POST /__admin/reset` zeroes every counter and clears the journal.** It is a local convenience. Parallel CI isolates
by process or container, exactly as the plan requires.

---

## 5. Fail-closed routing

Servicesim never dials outward. There is no `http.Client` and no `net.Dial` on any non-test path, the image ships no CA
bundle, and `scripts/lint-no-live-hosts.sh` already fails CI on a real provider hostname in `provider/`, `scenario/`,
`internal/`, `cmd/`, `testkit/` or `scenarios/`. An unmatched request is answered locally with a provider-shaped error
and journaled.

### 5.1 Mux construction

Each provider listener gets its own `http.ServeMux` carrying **only** that provider's patterns. This is what preserves
the `POST /search` collision between Exa and Tavily without a host-based hack. Three registrations per route.

This logic has **one home**: `provider.NewMux` (§2.2), owned by U9 in `provider/mux.go`. It is not reimplemented in
`provider/exa`, `provider/tavily`, `provider/perplexity` or `internal/server`. Those three provider packages are
written in parallel by different agents against this document; any one of them that registered only `POST /search` plus
`/` would return 404 for `GET /search`, `scripts/image-smoke.sh` asserts 405, and before CI caught it the in-process
handler and the container would disagree on a documented status code — precisely the divergence `testkit` exists to
prevent. Provider packages supply a `MuxSpec` — routes, handlers, and the two error-body builders — and nothing else.

```go
func NewMux(d Deps, p Name, spec MuxSpec) *http.ServeMux {
	m := http.NewServeMux()
	paths := map[string][]string{} // path -> allowed methods

	for _, rt := range spec.Routes {
		method, path, _ := strings.Cut(rt.Pattern, " ")
		m.Handle(rt.Pattern, Handle(d, p, rt, spec.Handlers[rt.Pattern]))
		paths[path] = append(paths[path], method)
	}

	// A method-less pattern per known path produces a provider-shaped 405.
	// Paths and methods are both walked in sorted order: map iteration would make
	// the Allow header's method order differ run to run (§3.3).
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		allow := slices.Sorted(slices.Values(paths[path]))
		m.Handle(path, Handle(d, p, Route{Pattern: path}, spec.MethodNotAllowed(allow)))
	}

	// A catch-all produces a provider-shaped 404 for every unknown path.
	m.Handle("/", Handle(d, p, Route{Pattern: "/"}, spec.NotFound))
	return m
}
```

The method-less registration is **mandatory, not stylistic**. Verified on Go 1.26.4: with only `POST /search` and `/`
registered, `GET /search` returns **404** from the catch-all with `Content-Type: text/plain`, because `/` matched and
ServeMux's built-in 405 path is never reached. `scripts/image-smoke.sh` asserts `405` for `GET /search`, so an
implementation that skips this fails the image smoke test — and, worse, would answer a method error with a body no
consumer can parse.

With the method-less pattern registered, the verified behaviour is:

| Request | Result |
|---|---|
| `POST /search` | the handler, 200 |
| `GET /search`, `PUT /search` | provider-shaped 405 with `Allow: POST` |
| `POST /nope` | provider-shaped 404 |
| `GET /` | provider-shaped 404 |
| `POST /search/` | provider-shaped 404 — `/search` is an exact pattern, not a subtree, so no redirect fires |

That table is `provider/mux_test.go`'s test table, verbatim. It runs once, against `NewMux`, rather than three times
against three copies of the registration logic.

`http.ServeMux` also cleans paths and issues a 301 for things like `/search/../search`. That is a redirect to a local
path, never a proxy, so failing closed still holds.

### 5.2 Error bodies

Because unmatched routing goes through `provider.Handle` like everything else, it is journaled with
`Outcome.Kind = OutcomeUnmatched` and `Findings` carrying `route.unmatched` or `route.method_not_allowed`. Bodies are
provider-shaped so a consumer's error decoder does not have to special-case the simulator:

| Provider | 404 body | 405 body |
|---|---|---|
| Exa | `{"requestId":"<hex>","error":"Not Found","tag":"NOT_FOUND"}` | same envelope, `"tag":"METHOD_NOT_ALLOWED"`, plus `Allow` |
| Tavily | `{"detail":{"error":"Not Found"}}` | `{"detail":{"error":"Method Not Allowed"}}`, plus `Allow` |
| Perplexity | `{"detail":"Not Found"}` | `{"detail":"Method Not Allowed"}`, plus `Allow` |

Only Tavily's envelope is vendor-verified for these statuses. Exa's tag values and Perplexity's `detail`-as-string are
simulator inferences and are recorded as such in `contracts/*/provenance.yaml`, so a live canary can correct them
without archaeology.

### 5.3 The admin listener

The admin mux fails closed the same way, with a plain `{"error":"not found"}` — it is not a provider surface and has no
vendor shape to imitate. Provider paths are deliberately *not* registered on the admin listener: a request to
`:8080/search` is a 404, which catches the common misconfiguration of pointing a base URL at the admin port.

---

## 6. Request validation model

### 6.1 Findings, and how they map onto HTTP

Validation produces `journal.Finding` values. The mapping is one rule:

- **Error** — at least one error means the request receives a provider-shaped 4xx and **no** scenario response, and
  consumes no fault attempt.
- **Warning** — journal-only. The request receives its scenario response as if nothing happened, and the warning is
  visible in `/__admin/requests` and assertable with `testkit.AssertFindings`.

That split is the whole point of the journal. It lets a test distinguish "my adapter received a response" from "my
adapter sent the request the vendor documents", which is the plan's stated reason for the journal existing. A
deprecated-but-accepted field must not break a request — the real API accepts it — but it must be visible.

Scenarios reshape the mapping without changing any code:

```yaml
providers:
  exa:
    validation:
      strict: true                       # every warning becomes an error
      promote: [request.content_type]    # or promote individual codes
      demote:  [exa.numResults.range]
```

`Exchange.Findings()` applies promotions and demotions before returning, so the policy is enforced in one place.

### 6.2 Shared checks

These live in `internal/httpx` and behave identically for all three providers. Only the error body differs.

| Code | Default severity | Status when an error |
|---|---|---|
| `route.unmatched` | error | 404 |
| `route.method_not_allowed` | error | 405 |
| `request.body_too_large` | error | 413 |
| `request.malformed_json` | error | provider-specific (Exa 400, Tavily 400, Perplexity 422) |
| `request.body_not_object` | error | provider-specific |
| `request.content_type` | **warning** | 415 only when promoted |
| `auth.missing` | error | 401 |
| `auth.wrong_placement` | warning | — |
| `auth.mismatch` | error | 401 |
| `request.unknown_field` | warning | — |
| `fault.unknown_key` | warning | — |

`fault.unknown_key` is not a request problem at all: it says the route that served this request asked the fault engine
for a budget the engine has no plan for, which means a route was added without being added to `Routes()`, or a key was
typo'd. It is recorded as a warning rather than swallowed because the silent version is a scenario whose declared 429
never fires, observed only as a failing retry test in the *consumer's* repository. A related misconfiguration —
a scenario that declares faults handed to a handler with no fault engine — cannot be a finding, because it is known
before any request arrives; `Deps.Normalized` logs `deps.faults_ignored` once at handler construction instead (§2.2).

Every finding message is passed through `redact.String` at the storage boundary (`journal.Redact`), because these
messages quote request values by design. A finding raised *about* a credential-named field goes further and quotes
nothing: see `tavily.api_key.in_body` below.

Two of those are deliberate and worth defending.

**`request.content_type` is a warning by default.** None of the three vendors documents a 415 for a wrong content type,
so returning one would be inventing vendor behaviour — precisely what a contract simulator must not do. A journal
warning gives the adapter test everything it needs (`AssertFindings` pins the code, and the strict `AssertNoFindings`
fails if the adapter forgot the header) without teaching consumers a status code the real API may never send. A
scenario that wants the strict behaviour promotes the code.

**`request.unknown_field` is a warning.** The plan's design principle is "be strict about requests", but being
*stricter than the vendor* is its own failure mode: a consumer that legitimately sends a field this simulator has not
modelled yet would fail against the simulator and pass against production. Warning is strict enough to be assertable and
loose enough to stay correct.

### 6.3 Provider-specific checks

Exa — errors return `{"requestId","error","tag"}` with `tag: INVALID_REQUEST_BODY` and status 400 unless noted:

| Code | Severity | Notes |
|---|---|---|
| `exa.query.missing`, `exa.query.empty` | error | `query` is required, `minLength: 1`. |
| `exa.type.invalid` | error | Message reproduced verbatim from Exa's own validator: `Invalid request body \| Validation error: Invalid enum value. Expected 'auto' \| 'fast' \| 'instant' \| 'deep-lite' \| 'deep' \| 'deep-reasoning', received '<got>' at "type"`. |
| `exa.type.legacy_neural` | error | A distinct code for `type: "neural"` so a test can assert the legacy value specifically. The body is still the enum error. |
| `exa.numResults.range` | error | 1–100. |
| `exa.category.invalid` | error | Six values; two contain a space. |
| `exa.includeDomains.max`, `exa.excludeDomains.max` | error | 1200 each. |
| `exa.outputSchema.depth`, `exa.outputSchema.properties` | error | Max nesting depth 2, max 10 properties. |
| `exa.useAutoprompt.deprecated` | warning | The plan explicitly asks for accept-and-flag. |
| `exa.livecrawl.deprecated` | warning | Superseded by `contents.maxAgeHours: 0`. |
| `exa.startCrawlDate.deprecated`, `exa.endCrawlDate.deprecated` | warning | Documented as having no effect. The plan flags only `useAutoprompt`. |
| `exa.contents.highlights.numSentences.deprecated` | warning | Deprecated highlight parameter. |
| `exa.contents.highlights.highlightsPerUrl.deprecated` | warning | Deprecated highlight parameter. |
| `exa.context.deprecated` | warning | Deprecated request field. |
| `exa.stream.unimplemented` | warning | Streaming is a plan non-goal. Default: warn and return the ordinary JSON body. `providers.exa.stream: reject` makes it a 400 for tests that want that. |

Tavily — errors return `{"detail":{"error":"..."}}`, status 400 unless noted:

| Code | Severity | Notes |
|---|---|---|
| `tavily.query.missing` | error | The only member of the schema's `required` list. |
| `tavily.search_depth.invalid` | error | Four values: `advanced`, `basic`, `fast`, `ultra-fast`. |
| `tavily.topic.invalid` | error | Three values: `general`, `news`, `finance`. |
| `tavily.max_results.range` | error | 0–20. |
| `tavily.chunks_per_source.range` | error | 1–3. |
| `tavily.include_answer.invalid` | error | Accepts a boolean **or** `"basic"`/`"advanced"`. Boolean-only validation rejects valid traffic. |
| `tavily.include_raw_content.invalid` | error | Accepts a boolean **or** `"markdown"`/`"text"`. |
| `tavily.time_range.invalid` | error | Eight values, long and single-letter. |
| `tavily.include_domains.max` | error | 300. |
| `tavily.exclude_domains.max` | error | 150 — deliberately asymmetric with include. |
| `tavily.api_key.in_body` | warning | Redacted, flagged, and **not** accepted as authentication. The plan's rule that `api_key` is not the canonical REST contract, made assertable. The message states presence only — `api_key must not be sent in the request body` — and never interpolates the value, not even truncated: this is a finding whose entire subject is a credential, and its message reaches the journal, the admin API and the log. `TestExchange_APIKeyFindingDoesNotQuoteValue` enforces it. |
| `tavily.days.removed` | warning | `days` is entirely absent from the current schema; recency is `time_range`/`start_date`/`end_date`. |
| `tavily.country.requires_general_topic` | warning | `country` is documented as available only when `topic` is `general`. |
| `tavily.auth.wrong_header` | warning | An `x-api-key` on Tavily is flagged; only Bearer authenticates. |

Perplexity — **every** field validation error returns 422 with the FastAPI `HTTPValidationError` shape, because that is
the only error body Perplexity formally schematises:

```json
{"detail":[{"loc":["body","model"],"msg":"Input should be 'sonar', 'sonar-pro', 'sonar-reasoning-pro' or 'sonar-deep-research'","type":"enum"}]}
```

| Code | Severity | `loc` |
|---|---|---|
| `perplexity.model.missing`, `perplexity.model.invalid` | error | `["body","model"]` |
| `perplexity.model.removed` | error | `["body","model"]` — a distinct code for `sonar-reasoning`, removed from the API on 2025-12-15. |
| `perplexity.messages.missing`, `perplexity.messages.empty` | error | `["body","messages"]` |
| `perplexity.messages.role.invalid` | error | `["body","messages",<i>,"role"]` — note the integer element, which is why `Loc` is `[]any`. |
| `perplexity.messages.content.invalid` | error | `["body","messages",<i>,"content"]` |
| `perplexity.temperature.range` | error | 0–2. |
| `perplexity.top_p.range` | error | 0–1. |
| `perplexity.max_tokens.range` | error | Greater than 0, at most 128000. |
| `perplexity.search_mode.invalid` | error | `web`, `academic`, `sec`. |
| `perplexity.reasoning_effort.invalid` | error | `minimal`, `low`, `medium`, `high`. |
| `perplexity.search_recency_filter.invalid` | error | `hour`, `day`, `week`, `month`, `year`. |
| `perplexity.stream.unimplemented` | warning | Streaming was a plan non-goal when this row was written; `docs/design/streaming.md` is now the design of record for it, and both Sonar and Agent streaming have shipped (Phase 5). `stream:` (`when_requested`) is now a THREE-value enum, not two: `warn` (default, journal this warning and return the ordinary non-streaming body), `reject` (promote it to an error — a 422 `{"detail":[{"loc":["body","stream"],…}]}` on this surface, not Exa's 400), and `stream`, which serves the scripted `text/event-stream` sequence instead of either — see `docs/scenario-schema.md`'s "Streaming (`stream:`)" section for the scripting grammar. Unlike Exa, whose `stream:` field stays this document's original two-value `StreamPolicy` (Exa does not stream), Perplexity's own `Stream` field is retyped to `scenario.StreamScript` to carry the third value and its script (`docs/design/streaming.md` §3) — the enums no longer match. The Agent surface's own separate code was `perplexity.agent.stream.unsupported`; renamed to `perplexity.stream.agent_unsupported` when Agent streaming shipped (Phase 5 unit 3) — see `docs/design/streaming.md` §9. |

The Sonar sunset (2026-09-27) is logged once at startup and surfaced on `/__admin/scenario`. It is deliberately **not**
a per-request finding: it is a property of the simulated API, not of any request, and per-request noise would drown the
findings that are actionable.

### 6.4 Authentication

| Provider | Accepted placements | Rationale |
|---|---|---|
| Exa | `x-api-key`, `Authorization: Bearer` | Both documented. The reference page leads with `x-api-key`; the coding-agents guide mentions only Bearer. Accept both. |
| Tavily | `Authorization: Bearer` | The only documented scheme. An `api_key` body property is warned about, redacted, and not accepted. |
| Perplexity | `Authorization: Bearer` | HTTPBearer security scheme. |

`AuthPolicy.Mode` overrides per scenario: `required` (default), `optional`, `reject` (always 401 — this is what
`unauthorized.yaml` uses). `ExpectKey` makes a wrong-key test possible; the journal still records only a fingerprint.

---

## 7. Parallelisation plan for implementation

Each unit below owns an exact, disjoint file list. No file appears in two units. Agents may work in the same tree
simultaneously as long as each writes only its own files.

Two tree-wide rules that make this safe:

1. **`go.mod` and `go.sum` are owned exclusively by U0.** No other unit may run `go mod tidy` or `go get`. U0 promotes
   `gopkg.in/yaml.v3`, `github.com/stretchr/testify` and `github.com/google/go-cmp` to direct requirements in wave 0
   before any code lands, and runs a final `go mod tidy` in the last wave.
2. **Every package has exactly one `doc.go` carrying the package comment.** No other file in that package may carry a
   package comment. `doc.go` is owned by the unit that owns the package. This removes the only way two agents could
   collide inside one package.

| Unit | Owns (exact files) | Depends on |
|---|---|---|
| **U0** Module and tasks | `go.mod`, `go.sum`, `Taskfile.yml`, `taskfiles/build.yml`, `taskfiles/test.yml`, `taskfiles/lint.yml`, `taskfiles/image.yml`, `scripts/lint-no-request-uri.sh` | — |
| **U1** Redaction | `internal/redact/doc.go`, `internal/redact/redact.go`, `internal/redact/redact_test.go` | U0 |
| **U2** Identifiers | `internal/ids/doc.go`, `internal/ids/ids.go`, `internal/ids/ids_test.go` | U0 |
| **U3** Wire rendering | `internal/wire/doc.go`, `internal/wire/render.go`, `internal/wire/render_test.go` | U0 |
| **U4** Scenario schema | `scenario/doc.go`, `scenario/model.go`, `scenario/fault.go`, `scenario/nullable.go`, `scenario/duration.go`, `scenario/decode.go`, `scenario/load.go`, `scenario/validate.go`, `scenario/resolve.go`, `scenario/render.go`, `scenario/model_test.go`, `scenario/decode_test.go`, `scenario/load_test.go`, `scenario/validate_test.go`, `scenario/resolve_test.go`, `scenario/testdata/**` | U0 |
| **U5** Golden contracts | `contracts/doc.go`, `contracts/contracts.go`, `contracts/contracts_test.go`, `contracts/exa/**`, `contracts/tavily/**`, `contracts/perplexity/**` | U0 |
| **U6** Journal | `internal/journal/doc.go`, `internal/journal/entry.go`, `internal/journal/ring.go`, `internal/journal/entry_test.go`, `internal/journal/ring_test.go` | U1 |
| **U7** Built-in scenarios | `scenarios/doc.go`, `scenarios/embed.go`, `scenarios/scenarios_test.go`, `scenarios/protocol/happy.yaml`, `scenarios/protocol/empty-results.yaml`, `scenarios/protocol/unauthorized.yaml`, `scenarios/protocol/rate-limited.yaml`, `scenarios/protocol/server-error.yaml`, `scenarios/protocol/malformed-json.yaml`, `scenarios/protocol/extra-fields.yaml`, `scenarios/protocol/fusion-overlap.yaml` | U4 |
| **U8** Request helpers | `internal/httpx/doc.go`, `internal/httpx/body.go`, `internal/httpx/auth.go`, `internal/httpx/contenttype.go`, `internal/httpx/body_test.go`, `internal/httpx/auth_test.go` | U6 |
| **U9** Provider seam | `provider/doc.go`, `provider/provider.go`, `provider/clock.go`, `provider/deps.go`, `provider/exchange.go`, `provider/response.go`, `provider/handle.go`, `provider/mux.go`, `provider/fault_exec.go`, `provider/clock_test.go`, `provider/handle_test.go`, `provider/exchange_test.go`, `provider/mux_test.go`, `provider/fault_exec_test.go` | U4, U6 |
| **U10** Fault selection | `internal/faults/doc.go`, `internal/faults/engine.go`, `internal/faults/engine_test.go` | U9 |
| **U11** Exa provider | `provider/exa/doc.go`, `provider/exa/handler.go`, `provider/exa/request.go`, `provider/exa/response.go`, `provider/exa/render.go`, `provider/exa/errors.go`, `provider/exa/handler_test.go`, `provider/exa/request_test.go`, `provider/exa/render_test.go`, `provider/exa/testdata/**` | U9, U8, U3, U2, U5 |
| **U12** Tavily provider | `provider/tavily/doc.go`, `provider/tavily/handler.go`, `provider/tavily/request.go`, `provider/tavily/response.go`, `provider/tavily/render.go`, `provider/tavily/errors.go`, `provider/tavily/handler_test.go`, `provider/tavily/request_test.go`, `provider/tavily/render_test.go`, `provider/tavily/testdata/**` | U9, U8, U3, U2, U5 |
| **U13** Perplexity provider | `provider/perplexity/doc.go`, `provider/perplexity/handler.go`, `provider/perplexity/request.go`, `provider/perplexity/response.go`, `provider/perplexity/render.go`, `provider/perplexity/errors.go`, `provider/perplexity/handler_test.go`, `provider/perplexity/request_test.go`, `provider/perplexity/render_test.go`, `provider/perplexity/testdata/**` | U9, U8, U3, U2, U5 |
| **U14** Configuration | `internal/config/doc.go`, `internal/config/config.go`, `internal/config/scenario.go`, `internal/config/config_test.go`, `internal/config/scenario_test.go` | U9 |
| **U15** Admin surface | `internal/admin/doc.go`, `internal/admin/handler.go`, `internal/admin/requests.go`, `internal/admin/handler_test.go` | U9, U6 |
| **U16** Server composition | `internal/server/doc.go`, `internal/server/server.go`, `internal/server/listeners.go`, `internal/server/logging.go`, `internal/server/server_test.go` | U10, U11, U12, U13, U14, U15 |
| **U17** Testkit | `testkit/doc.go`, `testkit/server.go`, `testkit/assertions.go`, `testkit/golden.go`, `testkit/server_test.go`, `testkit/assertions_test.go` | U16 |
| **U18** Binary | `cmd/servicesim/doc.go`, `cmd/servicesim/main.go`, `cmd/servicesim/main_test.go` | U14, U16 |
| **U19** Consumer examples | `docker-compose.example.yml`, `docs/consumer-guide.md`, `examples/adapter/exa_test.go`, `examples/adapter/fusion_test.go`, `examples/adapter/journal_test.go`, `examples/adapter/go.mod` | U17 |
| **U20** Live canary | `.github/workflows/live-contract-canary.yml`, `scripts/live-canary.sh`, `docs/live-canary.md` | — |
| **U21** Documentation | `README.md`, `docs/design/package-design.md`, `docs/scenario-schema.md`, `docs/adr/0001-single-repository.md`, `docs/adr/0002-verified-contract-precedence.md` | — |

Waves, given those dependencies:

| Wave | Units | Notes |
|---:|---|---|
| 0 | U0, U5, U20, U21 | No Go dependencies. U5 authors the goldens from the verified contracts, so the provider units have something to compare against the moment they start. |
| 1 | U1, U2, U3, U4 | Level 0 and 1 packages. |
| 2 | U6, U7 | |
| 3 | U8, U9 | U9 is the critical path: nothing above level 3 starts until the seam is fixed. |
| 4 | U10, U14, U15 | |
| 5 | U11, U12, U13 | The three provider packages are fully independent of each other and are the widest point of the fan-out. |
| 6 | U16 | |
| 7 | U17, U18 | |
| 8 | U19, U0 (final `go mod tidy`) | |

U9 is the schedule's bottleneck, and it grew deliberately: it now owns mux construction (`provider/mux.go`) and fault
execution (`provider/fault_exec.go`) as well as the seam itself. Both moved *into* U9 because the alternatives do not
compile or do not survive parallel authorship — execution cannot live in `internal/faults` without closing an import
cycle (§2.5), and mux construction written three times in three parallel units diverges on a documented status code
(§5.1). U10 is correspondingly smaller: selection only, one file plus its test. U9's signatures are fully specified in
§2.2 precisely so that U10 through U15 can be written against this document while U9 is still being implemented, and
only integration-tested once it lands.

`scripts/lint-no-request-uri.sh` is new and belongs to U0, which also wires it into `taskfiles/lint.yml` beside the
existing `lint-no-live-hosts.sh` invocation. It greps `provider/`, `internal/`, `testkit/` and `cmd/` for
`RequestURI` outside `_test.go` files and fails the build on a hit; see the `journal.Entry.Path` contract in §2.3.

Existing files not listed above — `Dockerfile`, `revive.toml`, `.markdownlint.yaml`, `.gitignore`, `LICENSE`,
`.github/workflows/ci.yml`, `.github/workflows/image.yml`, `scripts/image-smoke.sh`, `scripts/lint-no-live-hosts.sh`,
`docs/architecture-and-implementation-plan.md` — are already correct and are owned by **no unit**. Nothing in this plan
should modify them. If a unit believes it must, that is a design change and belongs in a review, not in a parallel
write.

One convention change U0 must make: the current root `Taskfile.yml` carries a comment arguing that a single file is
sufficient for a repository this size. House convention across `semstreams` and `semsource` is `Taskfile.yml` plus a
`taskfiles/` include directory, and consistency across the workspace outranks the local argument. U0 splits the existing
tasks into `taskfiles/{build,test,lint,image}.yml` and reduces the root file to `includes` plus `vars`, preserving every
existing task name so no CI step or muscle-memory invocation changes.

---

## Verified-contract deviation register

Every place this design departs from `docs/architecture-and-implementation-plan.md`, and why. The plan's examples were
written before the wire contracts were re-verified; where they disagree, the vendor documentation wins.

| # | Plan says | This design does | Consequence of following the plan |
|---:|---|---|---|
| 1 | Exa results carry a top-level `score` float | No `score` field. `scenario.ExaResult.Score` is accepted and raises `exa.result.score.not_emitted` | Consumers learn to parse a field the real API never sends |
| 2 | Exa response is `{results, requestId}` | Adds `costDollars` (always present), and `resolvedSearchType`, `context`, `output`, `statuses` when applicable | A cost-tracking consumer has nothing to parse; `costDollars` is exactly what such a consumer reads |
| 3 | Exa `results[].id` is a slug (`source-1`) | Defaults to the source URL | Consumers build slug assumptions that break in production |
| 4 | Exa `requestId` is `exa-request-1` | 32 lowercase hex characters from `ids.Hex32` | Any consumer regex or length check trained on the slug is wrong |
| 5 | Exa scoped to `/search` | Adds `POST /answer` with its own projection, citation shape and fault key | The "search/answer API" simulator cannot exercise the answer surface at all |
| 6 | Exa result fields are the eight listed | Adds `summary`, `image`, `favicon`, `subpages`, `extras`, `entities` | `favicon` and `image` are routinely consumed for UI rendering and would be untestable |
| 7 | Exa `author` is a plain string | Tri-state `Nullable` — Exa types it `anyOf[string, null]` | A consumer never exercises the null branch and panics in production |
| 8 | Exa `publishedDate` is `2026-05-20T00:00:00Z` | RFC 3339 with millisecond precision, matching Exa's own example, and tri-state | A date parser tuned to whole seconds may reject `...:32.547Z` |
| 9 | No Exa error bodies specified | Flat `{requestId, error, tag}` with a documented tag enum, **and a separate reduced `{error}` shape for 429** | A simulator that always emits the three-key shape is wrong for exactly the rate-limit case retry logic depends on |
| 10 | Only `useAutoprompt` flagged deprecated | Also flags `livecrawl`, `startCrawlDate`, `endCrawlDate`, `numSentences`, `highlightsPerUrl`, `context` | Adapters keep emitting fields documented as having no effect |
| 11 | Streaming is an open question for Exa | Resolved: it exists (`stream`, `text/event-stream`, SSE types `text-delta`/`grounding`/`results`/`done`/`error`). Still out of scope per non-goal 7, but `stream: true` produces a named warning rather than silence | The gap is invisible instead of assertable |
| 12 | No mention of `outputSchema` | Modelled as `ExaOutput` with `content` and `grounding[]`, on the response's `oneOf` branch | Structured-output consumers cannot be tested |
| 13 | Tavily `search_depth` is `basic`/`advanced` | Four values: `advanced`, `basic`, `fast`, `ultra-fast` | The simulator rejects valid current traffic |
| 14 | Tavily `include_answer` is a boolean | Boolean **or** `"basic"`/`"advanced"` | Same |
| 15 | Tavily `include_raw_content` is a boolean | Boolean **or** `"markdown"`/`"text"` | Same |
| 16 | Tavily `response_time` is the string `"1.15"` | JSON number `1.15` | Every typed consumer (Go `float64`, Pydantic `float`) breaks on the string |
| 17 | Tavily results are `{title, url, content, raw_content, score}` | Adds `favicon`, `images`, `id`, and `published_date` for `topic: news` | Fields consumers parse are untestable |
| 18 | Tavily `images` shown as `[]` | Items are objects `{url, description}`, not bare URL strings | An empty-array example pins nothing; a consumer assuming strings breaks on first use |
| 19 | No Tavily error envelope specified | Every documented status uses `{"detail":{"error":"<string>"}}` | Implementers have nothing to build against and will invent a flat `{"error":...}` |
| 20 | Tavily plan-limit responses unnamed | Non-standard **432** (plan limit) and **433** (pay-as-you-go limit) | 433 is never simulated |
| 21 | Tavily request surface is six fields | Twenty documented properties, including `topic`, `time_range`, `start_date`, `end_date`, `chunks_per_source`, `include_favicon`, `country`, `auto_parameters`, `exact_match`, `include_usage`, `safe_search` | Most of the request surface is unvalidated |
| 22 | Implicit `days` recency parameter | Absent from the current schema; raises `tavily.days.removed` | Adapters keep sending a parameter the API dropped |
| 23 | Perplexity `usage` is exactly three token counts | Adds required `cost`, plus optional `search_context_size` (a **string**), `citation_tokens`, `num_search_queries`, `reasoning_tokens` | A consumer validating against the real schema rejects the simulator's response for missing `cost` |
| 24 | Perplexity `search_results` items are five fields | Adds `last_updated`, and makes `date` nullable | `last_updated` is never emitted despite paired request filters existing |
| 25 | "Consumers should preserve both `citations` and `search_results`" | Both are emitted, but `citations` is documented as deprecated: applications should use `search_results` | Guidance actively steers consumers toward a deprecated field |
| 26 | Sonar is the current provider surface | Still simulated, with the sunset date (2026-09-27) and successor (`/v1/agent`) recorded and logged at startup | No signal that the simulated surface has an end date |
| 27 | `sonar-reasoning` unmentioned | Rejected with a distinct `perplexity.model.removed` code — removed from the API on 2025-12-15 | The simulator accepts a model the API no longer serves |
| 28 | Shared error scenarios imply a shared error model | Three genuinely different envelopes: Exa flat `{requestId,error,tag}` (reduced for 429), Tavily nested `{detail:{error}}`, Perplexity FastAPI `{detail:[{loc,msg,type}]}` for 422 and simulator-chosen `{detail:"<string>"}` elsewhere | A single shared error envelope would be wrong for all three |

Two contradictions inside the vendors' own documentation are resolved here rather than left to the implementer:

- **Exa `publishedDate` nullability.** The OpenAPI-backed reference page shows no null branch; the coding-agents guide
  types it `string|null`. The simulator can emit absent, null or a value, and the default scenario emits a value —
  tolerating the widest contract while defaulting to the common case.
- **Tavily `answer` presence.** It is in the 200 response's `required` list *and* its description says it appears only
  when `include_answer` is requested. The simulator always emits the key, `null` when no answer was requested. A
  required key that is sometimes absent breaks stricter consumers than a present `null` does.

Everything Perplexity returns for a non-422 error is a simulator invention, because Perplexity publishes no body shape
for 400, 401, 403, 404, 429 or 500. Those bodies are recorded as `simulator-chosen` in
`contracts/perplexity/provenance.yaml`, and correcting them is an explicit job for the live canary in U20.

---

## Rejected review findings

Every *defect* raised against this design is fixed in the body above. Four proposed *remedies* were not adopted,
because a different fix in the same section covers the same defect at lower cost. They are recorded here so they are
not re-proposed in a later review.

| Proposed remedy | Rejected because | Defect fixed instead by |
|---|---|---|
| Break the `provider` &harr; `internal/faults` cycle by declaring a `FaultExecutor` interface and injecting it through `Deps` | Adds a nil-able seam and a second `Response` type for behaviour that will only ever have one implementation, and leaves "who sets `entry.Outcome`" split across two packages | Moving execution into `provider/fault_exec.go`; `internal/faults` keeps selection only (§2.2, §2.5) |
| Keep `FakeClock` but have `Now()` advance a fixed tick per call so instants are strictly increasing | A fake whose correctness depends on being *called* often enough is a trap, and it still cannot make a client deadline fire — a deadline is observed by bytes not arriving | `Clock` reduced to `Now()`, defaulting to real time everywhere; delays governed by `Deps.DelayMode` (§2.2, §3.2) |
| Make `AssertOverlapped` use non-strict comparisons | With real timestamps, strict comparison is exactly right; the non-strict form also passes for strictly serial calls, which is the one thing the assertion exists to reject | Real-time journal timestamps by default (§2.10, §3.2) |
| Export `provider.NewFaults` so the direct `exa.New(Deps{...})` path can build an engine | Would pull selection state into `provider`, so a consumer importing `provider/exa` drags in the engine's mutable state — the property the level table is built to preserve | `testkit.NewFaults`, plus the `deps.faults_ignored` warning from `Deps.Normalized` (§2.2, §2.10) |
