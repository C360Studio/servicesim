# Extended provider surfaces

An addendum to [`package-design.md`](package-design.md). That document was authored before two scope decisions
were taken and before Perplexity's machine-readable specification was located. Everything here amends it; where the
two disagree, this file is newer and wins.

Read this together with [`../../contracts/perplexity/README.md`](../../contracts/perplexity/README.md), which is
generated directly from `https://docs.perplexity.ai/openapi.json` and is the authority on every field name and type
below.

## What changed and why

1. **Perplexity gets its successor surface as well as Sonar.** Sonar is supported only until 2026-09-27. The owner's
   direction is that Servicesim must support the old and the new endpoints, so `POST /v1/agent` is now in the initial
   release rather than a later phase.
2. **The Perplexity error model was wrong in the base design.** It states that "Perplexity publishes no body shape for
   400, 401, 403, 404, 429 or 500" and treats those bodies as simulator inventions. The specification does publish a
   shape: `ErrorInfo`. See [Error model](#error-model) below.
3. **Exa `/answer` is confirmed in scope.** The base design already reached this conclusion independently
   (deviation register #5) and its `ExaAnswer` projection stands unchanged. No amendment needed beyond noting that
   the decision is now explicit rather than inferred.

## Route map after this addendum

| Listener | Route | Surface |
|---|---|---|
| `:8081` exa | `POST /search` | Exa search |
| `:8081` exa | `POST /answer` | Exa synthesis |
| `:8082` tavily | `POST /search` | Tavily search |
| `:8083` perplexity | `POST /v1/sonar` | Sonar (sunset 2026-09-27) |
| `:8083` perplexity | `POST /chat/completions` | Sonar, OpenAI SDK alias |
| `:8083` perplexity | `POST /v1/agent` | Agent API |
| `:8083` perplexity | `POST /v1/responses` | Agent API, OpenAI SDK alias |

`/chat/completions` and `/v1/responses` are **not** in Perplexity's OpenAPI document. They exist because the OpenAI
SDK appends those paths to its configured `base_url`, and Perplexity accepts them. They are aliases in the strict
sense — same handler, same request and response shapes as their canonical partner. A consumer using the OpenAI SDK
against Perplexity will hit them, so the simulator must serve them, and the journal must record *which* path was used
so an adapter test can assert its intended route.

## Scope: the consumed contract, not the whole Agent API

The Agent API is materially larger than Sonar. Its `output` array is an ordered, typed execution trace with ten item
types, and the surface includes tool calling, a Python sandbox, MCP, background execution with polling, file
download, and streaming with fourteen event types.

Servicesim implements what a research adapter parses:

| Simulated | Deferred |
|---|---|
| `POST /v1/agent`, `POST /v1/responses` | Streaming (`EventType`'s 14 members) |
| `output[]` items of type `message` and `search_results` | `sandbox_results`, `mcp_call`, `mcp_list_tools`, `function_call`, `finance_results`, `people_search_results`, `fetch_url_results`, `tool_search_output` |
| `usage` with `ResponsesCost` | `background: true` and the `GET /v1/agent/{id}` polling lifecycle |
| `ErrorInfo` error envelope, `Status` enum | `GET /v1/agent/{id}/files`, file download, `POST /v1/agent/{id}/cancel` |
| Request validation over the 18 `ResponsesRequest` properties | `POST /search`, embeddings, async Sonar, analytics endpoints |

This follows the plan's first design principle. Each deferred item is a bounded addition behind the same scenario
model — a Servicesim release, not an architecture change. Do not add one speculatively; add it when a consumer
actually parses it.

A deferred feature must fail *loudly*, not silently: `stream: true` and `background: true` produce a named journal
warning (`perplexity.agent.stream.unsupported`, `perplexity.agent.background.unsupported`) alongside the ordinary
non-streaming response, matching the `StreamPolicy` treatment the base design gives Exa. Silence would let a consumer
believe it had exercised a path it never touched.

> Streaming is no longer unconditionally deferred: `docs/design/streaming.md` §7/§9 lands `GrammarTyped` on this
> surface (Phase 5 unit 3), under the same `warn`/`reject`/`stream` switch Sonar has, and `perplexity.agent.stream.unsupported`
> is renamed to `perplexity.stream.agent_unsupported` in the process. `background: true` is untouched by that unit
> and still always warns as this paragraph describes.

## Scenario model amendment

Under the open registry the Agent surface is its own provider entry, `perplexity_agent`, decoded into its own
projection type. `PerplexityProjection` (Sonar) is untouched, so every existing scenario keeps working, and a
scenario that only uses Sonar simply omits the entry.

```yaml
providers:
  perplexity:              # Sonar surface
    answer: A grounded answer citing Report A.
    citations: [source-a]
  perplexity_agent:        # Agent surface — independent auth, validation, faults
    answer: A grounded answer citing Report A.
    search_results:
      - source: source-a
```

Both projection types live in `provider/perplexity` and are decoded from their turn via `Turn.DecodeProjection`.

```go
// PerplexityAgent projects canonical sources into an Agent API response.
//
// The Agent envelope shares no fields with the Sonar envelope: Sonar returns
// choices[] with a message, the Agent API returns an ordered output[] trace.
// The two are rendered by separate functions from the same canonical sources,
// which is the point of the scenario model.
type PerplexityAgent struct {
	Auth       *AuthPolicy       `yaml:"auth,omitempty"`
	Validation *ValidationPolicy `yaml:"validation,omitempty"`

	// Fault is keyed independently of the Sonar fault so that a scenario can
	// rate-limit the Agent surface while leaving Sonar healthy, which is how a
	// consumer's migration fallback path gets tested.
	Fault *Fault `yaml:"fault,omitempty"`

	// ResponseID overrides the derived "resp_<32 hex>" identifier.
	ResponseID string `yaml:"response_id,omitempty"`

	// MessageID overrides the derived "msg_<32 hex>" identifier of the
	// message output item.
	MessageID string `yaml:"message_id,omitempty"`

	// Model is echoed as ResponsesResponse.model. Agent model IDs are
	// "provider/model" strings such as "openai/gpt-5"; when empty the
	// request's model is echoed, and when the request omits it too the
	// scenario default applies.
	Model string `yaml:"model,omitempty"`

	// Status defaults to StatusCompleted. Setting it to "failed" or
	// "incomplete" renders Error and is how a consumer's terminal-state
	// handling is exercised without an HTTP-level fault.
	Status AgentStatus `yaml:"status,omitempty"`

	// Answer becomes the text of the single message output item.
	Answer string `yaml:"answer,omitempty"`

	// Queries populates SearchResultsOutputItem.queries — the searches the
	// agent reports having run. Independent of SearchResults so that a
	// scenario can project "searched but found nothing".
	Queries []string `yaml:"queries,omitempty"`

	// SearchResults become the search_results output item. Ordering is the
	// scenario's; results[].id is the 1-based index as an integer, matching
	// the specification's integer id.
	SearchResults []SourceRef `yaml:"search_results,omitempty"`

	// Annotations attach url_citation spans to the answer text. An empty
	// slice emits [] rather than omitting the key.
	Annotations []AgentAnnotation `yaml:"annotations,omitempty"`

	// Error renders ResponsesResponse.error. Required when Status is
	// StatusFailed; validation rejects a failed status with no error.
	Error *AgentError `yaml:"error,omitempty"`

	Usage       *AgentUsage `yaml:"usage,omitempty"`
	ExtraFields ExtraFields `yaml:"extra_fields,omitempty"`
}

// AgentStatus is ResponsesResponse.status. The zero value renders as
// StatusCompleted so that a minimal scenario projects a successful response.
type AgentStatus string

const (
	StatusCompleted  AgentStatus = "completed"
	StatusFailed     AgentStatus = "failed"
	StatusIncomplete AgentStatus = "incomplete"
	StatusInProgress AgentStatus = "in_progress"
	StatusQueued     AgentStatus = "queued"
	StatusCancelled  AgentStatus = "cancelled"
)

// AgentAnnotation is a url_citation span over the answer text.
//
// StartIndex and EndIndex are byte offsets into Answer. Validate rejects
// offsets outside the answer or with End <= Start, because an out-of-range
// span is a fixture bug that would otherwise surface as a consumer panic.
type AgentAnnotation struct {
	Source     SourceRef `yaml:"source"`
	StartIndex int       `yaml:"start_index"`
	EndIndex   int       `yaml:"end_index"`
}

// AgentError is ResponsesResponse.error, the published ErrorInfo shape.
type AgentError struct {
	Message string `yaml:"message"`          // required by the specification
	Code    string `yaml:"code,omitempty"`
	Type    string `yaml:"type,omitempty"`
}

// AgentUsage is ResponsesUsage. Note the field names differ from Sonar's
// UsageInfo: input_tokens/output_tokens here, prompt_tokens/completion_tokens
// there. Do not share a Go type between them.
type AgentUsage struct {
	InputTokens  int        `yaml:"input_tokens"`
	OutputTokens int        `yaml:"output_tokens"`
	TotalTokens  int        `yaml:"total_tokens,omitempty"` // derived when zero
	Cost         *AgentCost `yaml:"cost,omitempty"`
}

// AgentCost is ResponsesCost. Currency, InputCost, OutputCost and TotalCost
// are required by the specification; the cache and tool fields are optional
// and are omitted when zero rather than emitted as 0.
type AgentCost struct {
	Currency          string  `yaml:"currency,omitempty"` // defaults to "USD"
	InputCost         float64 `yaml:"input_cost"`
	OutputCost        float64 `yaml:"output_cost"`
	TotalCost         float64 `yaml:"total_cost,omitempty"` // derived when zero
	CacheCreationCost float64 `yaml:"cache_creation_cost,omitempty"`
	CacheReadCost     float64 `yaml:"cache_read_cost,omitempty"`
	ToolCallsCost     float64 `yaml:"tool_calls_cost,omitempty"`
}
```

## Rendering rules

The wire types live in `provider/perplexity/response.go` alongside the Sonar types and follow the same convention:
`json` tags exactly as the specification names them, `omitempty` only where the field is genuinely optional.

Ordering of `output[]` is fixed and deterministic: **`search_results` first, then `message`**. This mirrors the
execution order the trace represents — the agent searches, then answers — and gives consumers a stable index. A
scenario cannot reorder it; a scenario that needs a different trace shape is asking for a feature that is deferred.

Derivations, all from stable fixture keys and never from a clock or a random source:

| Wire field | Derivation |
|---|---|
| `id` | `"resp_" + ids.Hex32(scenario.Name, "perplexity", "agent", requestKey)` |
| `output[0].id` (message) | `"msg_" + ids.Hex32(...same inputs..., "message")` |
| `created_at` | `Scenario.Time.Base.Unix()`, never `time.Now()` |
| `object` | constant `"response"` |
| `output[].results[].id` | 1-based index within the item, as a JSON integer |
| `usage.total_tokens` | `input_tokens + output_tokens` when the scenario leaves it zero |
| `usage.cost.total_cost` | `input_cost + output_cost` when the scenario leaves it zero |
| `usage.cost.currency` | `"USD"` when the scenario leaves it empty |

`SearchResult.id` being an **integer** is the one place Perplexity differs from every other identifier in this
repository. It is not a string, not the source ID, and not a URL. Encoding it as a string is the single most likely
implementation error on this surface, so `render_test.go` must assert on the raw JSON bytes, not on a round-tripped
struct — a Go `int` field and a `string` field both round-trip cleanly through a permissive decoder and the bug
survives.

## Error model

This supersedes the base design's claim that Perplexity publishes no error body shape.

| Status | Body | Source |
|---|---|---|
| 422 | `{"detail":[{"loc":["body","model"],"msg":"...","type":"..."}]}` | `HTTPValidationError` in the specification |
| 400, 401, 403, 404, 429, 500 on Agent routes | `{"error":{"message":"...","code":"...","type":"..."}}` | `ErrorInfo`, declared on the agent operations |
| Non-422 on Sonar routes | `{"detail":"<string>"}` | Still simulator-chosen; the specification declares no body |

Record the Sonar non-422 bodies as `simulator-chosen` in `contracts/perplexity/provenance.yaml` so the live canary
knows they are unverified and can correct them from a real response.

Note the asymmetry is real, not an oversight: Sonar is a FastAPI surface whose validation errors follow FastAPI's
convention, while the Agent API declares its own `ErrorInfo`. Do not unify them.

## Amendments to the parallelisation plan

| Unit | Amendment |
|---|---|
| **U0** | **Do not split `Taskfile.yml` into `taskfiles/`.** The base design proposes this for house consistency. The plan document's repository layout specifies a single root `Taskfile.yml`, `semsource` also uses a single file, and the existing one is complete and working. Churn without benefit; U0's scope is `go.mod`/`go.sum` only. |
| **U5** | `contracts/{exa,tavily,perplexity}/README.md` are **already written** and are generated provenance — Perplexity's from `openapi.json` directly. U5 owns golden JSON fixtures and `provenance.yaml` in those directories and must **not** rewrite the README files. |
| **U13** | Extended: also owns `provider/perplexity/agent.go` and `provider/perplexity/agent_test.go`, and serves all four Perplexity routes. |
| **U19**, **U20** | **Deferred.** Consumer examples are plan Phase 4 and the live canary is Phase 5; this release is Phases 0–3. `contracts/README.md` already documents the canary's contract for when it is built. |
| **U21** | Also owns this file. |

Wave structure is unchanged. U13 grows but stays in wave 5 alongside U11 and U12, and remains independent of both.

## Routes and fault keys

`Route.FaultKey` is what attempt counting is keyed on, and aliases deliberately share a key so that a retry through
the alias draws on the same budget. The Agent surface gets its **own** key, separate from Sonar:

Under the open registry, a fault selector resolves a provider by name and reads the fault off the *turn* that was
selected, so a script can rate-limit one turn and not the others.

```go
// Provider names in the scenario. Sonar and Agent are separate entries so a
// scenario can rate-limit one surface while the other stays healthy — which is
// exactly how a consumer's Sonar-to-Agent migration fallback gets tested.
const (
	NameSonar = "perplexity"
	NameAgent = "perplexity_agent"
)

// Routes returns the four Perplexity routes across two surfaces. The two
// aliases of each surface share a FaultKey, so a retry through the alias draws
// on the same attempt budget.
func Routes() []provider.Route {
	sonar := func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, NameSonar) }
	agent := func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, NameAgent) }

	return []provider.Route{
		{Pattern: "POST /v1/sonar", FaultKey: "perplexity:completions", Fault: sonar},
		{Pattern: "POST /chat/completions", FaultKey: "perplexity:completions", Fault: sonar},
		{Pattern: "POST /v1/agent", FaultKey: "perplexity:agent", Fault: agent},
		{Pattern: "POST /v1/responses", FaultKey: "perplexity:agent", Fault: agent},
	}
}
```

`provider.TurnFault(s, name)` is nil-safe on every hop — `Scenario`, `Providers`, the entry, and its turns may each
be absent — which is why it is a helper rather than an inline chain of field reads that would panic on a partial
scenario.

Declaring the Agent surface as its own provider entry rather than a nested field is what makes the fault, auth and
validation policies independently settable per surface, and it falls out of the registry model for free. A scenario
that only uses Sonar simply omits the `perplexity_agent` entry.

## Open provider registry and the turn model

**This section supersedes the base design's `Providers` struct (§2.1) and is a decision taken deliberately for the
initial release, not a future option.**

The base design hardwires `Providers` to three typed fields. Two consequences made that the wrong shape:

1. Adding a fourth provider means editing the `scenario` package, which is a level-1 package that has no business
   knowing what providers exist.
2. More importantly, the migration cost of a schema change is not in this repository's Go code — it is in the
   scenario YAML sitting in every consuming repository. Fixtures written this month should survive the arrival of
   LLM providers next quarter. That argues for landing the shape now, while there are zero files to migrate.

### The schema

```yaml
version: 1
name: fusion-overlap

sources:            # the canonical corpus, unchanged — shared by every provider
  - id: source-a
    url: https://example.test/report-a
    title: Report A

providers:

  # Single-shot form. Unchanged from the plan document; every existing example
  # stays valid. Reserved envelope keys are stripped, and everything left over
  # is this provider's projection body.
  exa:
    auth: {...}          # reserved
    validation: {...}    # reserved
    fault: {...}         # reserved
    results:             # projection body
      - source: source-a

  # Conversation form. Turns are matched in declaration order; the first whose
  # `when` matches wins. A turn with no `when` matches anything.
  openai:
    turns:
      - when:
          call_index: 0
        respond:
          tool_calls:
            - name: search
              arguments: {query: "report a"}
      - when:
          last_message_contains: "report-a"
        respond:
          content: "Report A says ..."
          citations: [source-a]
      - respond:                       # fallback
          content: "I don't know."
```

The two forms are the same thing. At load time a provider with no `turns:` is normalised into exactly one turn with
no `when`, so **everything downstream sees one shape** and no handler branches on which form the author wrote.

Reserved envelope keys are `kind`, `auth`, `validation`, `fault`, `turns` and `extra_fields`. Everything else in a
provider block is projection body. This is what keeps the plan's original example YAML valid verbatim.

### Go types

```go
// Providers is an open registry keyed by provider name. It is deliberately not
// a struct with one field per provider: adding a provider must not require
// editing this package.
//
// Iteration order is declaration order, never Go map order — rendering must be
// deterministic.
type Providers struct {
	order   []string
	entries map[string]*ProviderEntry
}

// Names returns provider names in declaration order.
func (p *Providers) Names() []string

// Get returns the entry for name, or nil. Nil-safe on a nil receiver so that
// route selector functions stay one-liners.
func (p *Providers) Get(name string) *ProviderEntry

// ProviderEntry is one provider's block. The scenario package decodes the
// envelope and leaves each turn's projection body undecoded, because a level-1
// package cannot know what an Exa result looks like without importing
// provider/exa and creating a cycle.
type ProviderEntry struct {
	// Name is the map key.
	Name string

	// Kind selects the handler implementation. Defaults to Name, so the common
	// case needs no `kind:`. It exists so one scenario can declare two
	// instances of the same provider — an "openai" and an "openai_fallback" —
	// which multi-provider failover tests need.
	Kind string

	Auth       *AuthPolicy
	Validation *ValidationPolicy

	// Turns always has at least one element after Load. A single-shot block is
	// normalised into one unconditional turn.
	Turns []Turn

	// Implemented reports whether this build has a handler for Kind. An
	// unimplemented provider is a validation WARNING, not a load failure, so a
	// scenario file shared across repositories does not break the moment one
	// consumer pins an older Servicesim.
	Implemented bool
}

// Turn is one request/response exchange in a provider's script.
type Turn struct {
	// When is the predicate. Nil matches any request.
	When *Match `yaml:"when,omitempty"`

	// Respond is the provider-specific projection body, left undecoded here.
	// The provider package decodes it into its own typed projection via
	// DecodeProjection. For a single-shot block this is the provider block
	// minus its reserved envelope keys.
	Respond yaml.Node `yaml:"respond"`

	// Fault applies to this turn only, which is what lets a script say
	// "rate-limit the third call, then succeed".
	Fault *Fault `yaml:"fault,omitempty"`
}

// Match selects a turn from a request. Every field is optional; all present
// fields must match (AND, not OR). An empty Match matches everything.
//
// Matching is over the request only. It never considers wall-clock time or
// anything outside the request and the call counter, because a predicate that
// depends on the clock is a flaky test waiting to happen.
//
// PREFER CallIndex. A survey of the sibling sem* repositories found five
// independent LLM mocks using four different matcher axes, and the one whose
// authors documented their reasoning (semmachina) explicitly REJECTED
// prompt-substring matching, because a fixture keyed on prompt text breaks
// the next time someone rewords a prompt — a failure that looks like a model
// regression and is not one. BodyContains is provided because it is what most
// existing mocks do and migration needs it, but a scenario that can be written
// with CallIndex should be.
type Match struct {
	// CallIndex matches the zero-based count of prior requests in this turn
	// lane (see TurnKey). This is the primitive an agentic loop needs and the
	// one the fault engine already maintains.
	CallIndex *int `yaml:"call_index,omitempty"`

	// BodyContains matches when the raw request body contains this substring.
	// Deliberately crude: a full expression language here would be a product.
	// Fragile against prompt rewording — see the type comment.
	BodyContains string `yaml:"body_contains,omitempty"`

	// BodyJSON matches decoded request fields by dotted path, e.g.
	// {"model": "sonar", "messages.0.role": "system"}. Values compare as
	// strings after JSON scalar formatting. Preferred over BodyContains
	// because it is structural rather than textual.
	BodyJSON map[string]string `yaml:"body_json,omitempty"`
}

// DecodeProjection decodes a turn's Respond node into a provider's typed
// projection. Provider packages call this; the scenario package never knows
// the concrete type.
//
// It reports an error naming the provider and turn index, because "cannot
// unmarshal string into int" with no location is unactionable in a fixture
// with twelve turns.
func (t *Turn) DecodeProjection(name string, index int, into any) error
```

### The turn lane: why the cursor is not keyed on the route

An earlier draft of this section said the turn cursor could simply reuse the fault engine's per-`FaultKey` counter.
A survey of the sibling `sem*` repositories showed that is **wrong under agent fan-out**, and it is the one thing
here that is genuinely expensive to change after fixtures exist.

The failure: one LLM route — `POST /v1/chat/completions` — serves N concurrent agent roles. Route-keyed means one
counter, so two roles running concurrently draw indices 0 and 1 from the same sequence and each receives the turn
scripted for the other. The base design already documents this honestly for *faults* (§4.4: "two concurrent requests
receive indices 0 and 1 and may complete in either order"), and for faults it is tolerable — you get an unexpected
status code, which fails loudly. For turns it is not: you get a coherent-looking response from the wrong lane, and
the test fails somewhere else entirely, much later.

This is not a hypothesis. Three sibling repositories hit it and each independently re-keyed the cursor: `semteams`
per role, `semmachina` per `(scenario, role)`, `semspec` per model. Three independent rediscoveries of the same fix
is a design requirement, not a coincidence.

```go
// TurnKey declares what the turn cursor is keyed on — the "lane" a request
// belongs to. Default is ["route"], which reproduces one sequence per route
// and is correct for a single serial caller.
//
// Extractors, evaluated in order and joined to form the lane key:
//   "route"                  the Route.FaultKey (default)
//   "body_json:<dotted.path>"  a scalar from the decoded request body
//   "header:<name>"          a request header value
//
// Example — one lane per model, which is what semspec needed:
//   turn_key: ["route", "body_json:model"]
//
// A request whose extractor finds nothing falls into the lane named by the
// extractors that did resolve, and collects a scenario.turn_key_unresolved
// warning. It does NOT silently share the default lane: silently merging lanes
// is precisely the bug this field exists to prevent, so it must be visible in
// the journal.
type TurnKey []string
```

`TurnKey` is a field on `ProviderEntry` (envelope key `turn_key`). The lane key it produces replaces `Route.FaultKey`
as the cursor key for turn selection **and** for fault attempt counting on that provider, so the two cannot disagree
about which call this is — the single-counter property the base design was right to insist on.

Landing this now is the whole point of landing the turn model now. Shipping `turns:` with a route-keyed cursor would
mean the first multi-turn fixtures anyone writes are written against a cursor that breaks under concurrency, and
fixing it afterwards is the schema migration this section exists to avoid.

### Namespaces: one container, many concurrent tests

The plan lists this as a deferred decision ("whether the administration API needs per-test namespaces in addition to
process-level isolation"). It is now **decided: namespaces are in scope.**

The evidence is that all five sibling `sem*` e2e suites run ONE shared mock container across many tests. Telling
every adopting repository to restructure its e2e harness is friction at exactly the moment adoption should be cheap,
and the plan's stated preference for process isolation was a principle adopted before anyone had looked at how the
consumers actually run.

Process isolation stays the **default and the recommended pattern**. Namespaces are what make the shared-container
pattern *safe* rather than what make it the norm.

#### Extraction

Two optional path prefixes, stripped before route matching:

```text
http://servicesim:8081/x/<scenario>/n/<namespace>/search
                       └─ selects behaviour   └─ isolates state
```

Both are optional and order-fixed. `/search` alone still works and means `(startup scenario, namespace "default")`.

A path prefix rather than a header is deliberate: consumers already inject base URLs, and every SDK supports a base
URL with a path. Many LLM SDKs make per-request headers awkward or impossible, which is exactly the population this
feature exists for. `X-Servicesim-Namespace` is also accepted for SDKs that pin the path, but the base URL is the
documented mechanism because it needs no consumer code change at all:

```text
EXA_BASE_URL=http://servicesim:8081/n/${TEST_ID}
```

#### What a namespace isolates

A namespace is a **state** boundary, not a behaviour boundary:

| Isolated per namespace | Shared process-wide |
|---|---|
| Fault attempt counters | The loaded, validated scenarios |
| Turn cursors (the lane key gains a namespace component) | Route tables and handlers |
| Journal entries and sequence numbers | Configuration and listeners |

`/x/<scenario>` is the behaviour dimension and is separate, because two tests frequently need the *same* behaviour
with *independent* state — the common case, and one a scenario-only mechanism cannot express. Scenarios are loaded
and validated from `--scenario-dir` at startup, so acceptance criterion 4 holds unchanged: nothing is loaded lazily,
readiness still means every scenario is valid.

#### Keying

The namespace becomes the outermost component of the lane key defined above, so one function produces the key used
by turn selection, fault attempt counting and journal scoping alike:

```go
// Lane identifies an isolated state lane. Turn cursors, fault attempt counters
// and journal sequence numbers are all keyed on it, so they cannot disagree
// about which call this is.
type Lane struct {
	Namespace string // "default" when the request carries no /n/ prefix
	Scenario  string // the startup scenario's name when no /x/ prefix
	Key       string // TurnKey extractors joined; falls back to Route.FaultKey
}
```

#### Lifecycle and bounds

Namespaces are created implicitly on first use — requiring registration would mean a setup call in every consumer
test, which is the friction this is meant to remove. That makes them an unbounded-growth surface, so:

- `--max-namespaces` (default 1024) bounds live namespaces. Exceeding it returns a provider-shaped error and logs
  loudly rather than evicting silently; silent eviction would reset a running test's cursor mid-loop, which is the
  single worst failure this design can produce.
- Each namespace's journal obeys the existing per-journal capacity bound, so total retention is bounded by
  `max-namespaces × journal-capacity` and both are configurable.
- `POST /__admin/reset?namespace=<name>` drops one namespace's state. Resetting **everything** requires explicit
  `?all=true` — a bare reset that silently wiped a hundred concurrent tests' cursors is a trap, and the verbose form
  is the point.
- `GET /__admin/requests?namespace=<name>` scopes the journal. Without the parameter it returns all namespaces, with
  each entry carrying its `namespace` field.

#### What this does not change

`POST /__admin/reset` is still not a concurrency mechanism, and the CLAUDE.md house rule stands: **do not add admin
endpoints that mutate scenario state.** Namespaces isolate state that already exists per request; they do not
introduce a way to reconfigure a running simulator. A test that wants different behaviour selects a different
scenario by URL, which was validated at startup — it does not push new behaviour into a live process.

### Version gating: check the version before the strict decode

`scenario.Load` decodes with `yaml.Decoder.KnownFields(true)`, and `Validate` checks the schema version afterwards.
That ordering means a `version: 2` file loaded by a `version: 1` binary fails with a wall of unknown-key errors
instead of the one sentence the reader needs.

Versioning is the stated migration mechanism for every deferral in this document — streaming event sequences, new
matcher axes, new providers. A migration mechanism whose failure message does not name the migration is not usable
by the person who hits it, who is a developer in another repository with no context on this design.

**Peek the `version` key first, compare against `SchemaVersion`, and if it does not match, return one error finding
naming both versions and the build — before the strict decode runs.** It is a few lines, and it is the entire cost of
making every future deferral safe.

### Where validation moved, and why readiness is still safe

`scenario.Validate` can no longer check a projection body — it does not know the type. It validates the envelope:
version, source references, turn ordering, `when` well-formedness, fault plans, and that every `Respond` node is a
mapping.

Projection-body validation moves to the provider packages, driven at composition time:

```go
// ValidateScenario asks every provider named in the scenario to decode and
// validate its own projections. internal/server calls this after
// scenario.Validate and before readiness reports true, so acceptance criterion
// 4 — "startup scenarios are deterministic and validated before readiness
// succeeds" — still holds end to end.
//
// A provider named in the scenario with no registered handler yields a warning
// naming it, never an error.
func ValidateScenario(s *scenario.Scenario, handlers map[string]Validator) []scenario.Finding
```

This is a real seam with several implementations, so it does not repeat the anti-pattern the design review rejected
when it declined a single-implementation `FaultExecutor` interface.

### Turn selection

Selection lives in `provider` alongside fault selection and shares its counter — one counter keyed on
`Route.FaultKey`, not two counters that can disagree:

```go
// SelectTurn returns the first turn whose When matches, along with its index.
// Turns are evaluated in declaration order. When no turn matches, the last
// turn with a nil When is used; if there is none, the request gets a
// scenario.no_matching_turn finding and a provider-shaped 404-equivalent.
//
// callIndex comes from the same per-FaultKey counter the fault engine uses, so
// a scenario that rate-limits call 2 and answers differently on call 3 stays
// coherent.
//
// route is the Route.FaultKey serving the request, which is what a turn's
// `when.route:` selects on. Aliases share a key, so a scenario written against
// one spelling of a route also serves requests through the others.
func SelectTurn(e *scenario.ProviderEntry, callIndex int, route string, body []byte) (*scenario.Turn, int, error)
```

### What this does not do

It does not make Servicesim generate anything. A turn is a *scripted* response chosen by a *declarative* predicate
over the request. "On the call whose body contains tool result X, return Y" is in scope. "Work out a sensible answer
to an arbitrary prompt" is a fake LLM, violates plan non-goal 2, and is a different product.

Streaming is still out of scope. When it arrives, a turn gains an event-sequence projection alongside `respond` —
additive, and the reason the turn model needed to exist first.

## Why this was worth doing now rather than later

Servicesim's chassis is not search-specific. The provider seam, `internal/faults`, `internal/journal`,
`internal/redact`, `testkit` and base-URL injection would all transfer unchanged to simulating OpenAI, Anthropic or
any other LLM API — and several sibling `sem*` repositories currently hand-roll their own mocks for exactly that.

There is one structural gap. Servicesim is one-shot: a request selects a projection and renders it. An agentic loop
needs the simulator to return a *different* response on each successive call, usually selected by inspecting the
request — which tool result came back, what the last message says. Today's single projection is the length-1
degenerate case of a conversation script.

An earlier revision of this document argued for deferring the turn model, on the grounds that the versioned schema
already provides a migration path — a conversation script could simply be `version: 2`. That argument was wrong, and
recording why matters more than recording the conclusion, because the same reasoning error is easy to repeat.

**It measured the migration cost in the wrong repository.** A `version: 1` → `version: 2` schema change is nearly free
*here*: the loader accepts both, and the Go work is small. The cost lands in every consuming repository, as scenario
YAML that has to be rewritten by people who did not choose the change and get no benefit from it. Servicesim exists
to be depended on by several repositories; a schema break is therefore an N-repository event, not a one-repository
event. Today N is zero. That is the entire argument, and it only holds now.

The second deferral argument — that a selection seam would repeat the anti-pattern the review rejected when it
declined a `FaultExecutor` interface — does not survive contact with the actual shape. That rejection was specifically
about *a nil-able seam with exactly one implementation, forever*. `Validator` and the turn model have a genuine
plurality of implementations the moment a second class of provider exists, which is the case being designed for. The
patterns look alike and are not.

What remains true from the deferral argument, and is now load-bearing rather than hypothetical: the primitive a
conversation script needs — *which call number is this, for this route* — is the fault engine's per-`FaultKey`
attempt counter. `SelectTurn` shares that counter rather than introducing a second one. Two counters that can
disagree about "which call is this" would be a genuinely nasty bug class.

The line that must hold is plan non-goal 2. Servicesim **replays a scripted conversation deterministically**; it never
generates. "On the call whose body contains tool result X, return Y" is in scope. "Respond sensibly to an arbitrary
prompt" is a fake LLM, and it is a different product.

## A note for whoever adds streaming

Both deferred streaming surfaces — Exa's SSE on `/search` and `/answer`, and the Agent API's fourteen event types —
need the same thing from the fault engine that the base design already built for truncated bodies: access to the
underlying connection through `http.Hijacker`, or `Flusher` plus `panic(http.ErrAbortHandler)`. The seam is ready.
What is *not* ready is the scenario model, which currently projects one response rather than a sequence of events.
Adding streaming means adding an event-sequence projection, and that is a scenario-schema version bump.
