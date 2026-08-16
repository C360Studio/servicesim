package scenario

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only scenario schema version this build understands.
const SchemaVersion = 1

// DefaultBaseTime is the instant Scenario.BaseTime falls back to. Nothing in a
// response body is ever read from a real clock, so a scenario that pins no time
// still renders byte-identical timestamps on every run.
var DefaultBaseTime = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// Scenario is one deterministic corpus plus the per-provider projections and
// behaviour that render it. A Scenario is immutable once Load returns.
type Scenario struct {
	Version     int    `yaml:"version"`
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`

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
	// Defaults to DefaultBaseTime.
	Base time.Time `yaml:"base,omitempty"`
}

// Source is one canonical document in the corpus. A single Source renders into
// every provider wire format, which is what makes cross-provider overlap
// deliberate rather than accidental.
type Source struct {
	ID          string     `yaml:"id"`
	URL         string     `yaml:"url"`
	Title       string     `yaml:"title"`
	Author      string     `yaml:"author,omitempty"`
	PublishedAt *time.Time `yaml:"published_at,omitempty"`
	Text        string     `yaml:"text,omitempty"`
	Snippets    []string   `yaml:"snippets,omitempty"`
	Claims      []Claim    `yaml:"claims,omitempty"`

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

// Reserved envelope keys inside a provider block. Everything else in the block is
// that provider's projection body.
//
// extra_fields is documented as an envelope key but is deliberately NOT stripped:
// every provider projection declares its own extra_fields, and leaving the key in
// the body is what makes the single-shot and multi-turn forms literally the same
// thing rather than two forms with a silent difference.
const (
	keyKind       = "kind"
	keyAuth       = "auth"
	keyValidation = "validation"
	keyFault      = "fault"
	keyTurns      = "turns"
	keyTurnKey    = "turn_key"
	keyCreate     = "create"
)

// reservedEnvelopeKeys are the provider-block keys stripped before what remains
// becomes the projection body.
//
// It is NOT what the loader reads. decodeProviderEntry's switch is authoritative
// — a key with a case arm is envelope, and everything reaching default is body —
// and this slice exists for a test to assert the two agree. Adding a key here
// without adding a case arm changes nothing at all, which is a trap worth naming
// because the slice reads as if it were the definition.
var reservedEnvelopeKeys = []string{
	keyKind, keyAuth, keyValidation, keyFault, keyTurns, keyTurnKey, keyCreate,
}

// Providers is an open registry keyed by provider name. It is deliberately not
// a struct with one field per provider: adding a provider must not require
// editing this package.
//
// Iteration order is declaration order, never Go map order — rendering must be
// deterministic, and a map range here would produce a test that passes locally
// and flakes in CI.
type Providers struct {
	order   []string
	entries map[string]*ProviderEntry
}

// Names returns provider names in declaration order.
func (p *Providers) Names() []string {
	if p == nil {
		return nil
	}
	return slices.Clone(p.order)
}

// Get returns the entry for name, or nil. Nil-safe on a nil receiver so that
// route selector functions stay one-liners.
func (p *Providers) Get(name string) *ProviderEntry {
	if p == nil || p.entries == nil {
		return nil
	}
	return p.entries[name]
}

// Len returns the number of declared providers. Nil-safe.
func (p *Providers) Len() int {
	if p == nil {
		return 0
	}
	return len(p.order)
}

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

	// Create is the create-side envelope of an async provider entry.
	//
	// It exists because a create-then-poll surface has two routes on one entry
	// that need SEPARATE fault plans, and a turn of an async entry is one poll
	// snapshot — so a plan declared on a turn belongs to the poll route and the
	// create has nowhere to hang one. A second block-level `fault:` could not
	// work: alongside `turns:` it is already a load error, and rightly, because
	// in the multi-turn form nothing says which route a bare `fault:` meant.
	//
	// Nesting makes the route explicit in the key itself.
	Create *CreatePolicy

	// TurnKey declares what the turn cursor is keyed on. Empty means ["route"].
	TurnKey TurnKey

	// Turns always has at least one element after Load. A single-shot block is
	// normalised into one unconditional turn.
	Turns []Turn

	// Implemented reports whether this build has a handler for Kind. An
	// unimplemented provider is a validation WARNING, not a load failure, so a
	// scenario file shared across repositories does not break the moment one
	// consumer pins an older Servicesim.
	//
	// It is set by the composition layer, which owns the handler registry;
	// scenario.Validate deliberately does not know which providers exist. The
	// warning is raised by provider.ValidateScenario.
	Implemented bool

	// scripted records that the author wrote `turns:` rather than a single-shot
	// block. It only affects the YAML paths validation findings are addressed by.
	scripted bool

	// strayKeys are projection-body keys found alongside `turns:`, which is an
	// authoring error reported by Validate rather than silently dropped.
	strayKeys []string

	// faultWithTurns records a block-level `fault:` alongside `turns:`, where the
	// fault belongs on a turn instead.
	faultWithTurns bool
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

	// Fault declares a fault plan. It is written on a turn because that is where
	// the schema puts it, NOT because the plan is scoped to that turn.
	//
	// Fault plans are registered per ROUTE. provider.TurnFault hands the engine
	// the first turn of the provider that declares a plan with attempts, and that
	// one plan is what the route's fault key expands — for every turn, and for
	// every lane drawn from that route. Writing a fault under the second turn does
	// not delay it to the second call: the plan starts consuming from the route's
	// very first request, whichever turn answers it.
	//
	// So "rate-limit the third call, then succeed" is expressed in the attempts
	// list, not by which turn the fault is written under. A leading
	// "- status: 200" is an unfaulted attempt, which is what makes
	// [{status: 200}, {status: 200}, {status: 429}] fault only the third call.
	//
	// Per-turn and per-lane plans are deferred work. The engine cannot select a
	// different plan per turn today, so when two turns of one provider each
	// declare attempts, only the earlier plan is ever expanded; the later one is
	// silently unused rather than rejected by Validate. See
	// docs/troubleshooting.md, "A fault fired on the wrong call".
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
	// Route matches the route serving the request, so one provider's turn list
	// can script several routes independently. Without it a multi-route provider
	// is unscriptable: a GET poll carries no body, so BodyContains and BodyJSON
	// cannot see it, and CallIndex alone cannot say WHICH route's third call is
	// meant. "The third poll returns completed, whatever the create call did" is
	// the shape every async surface needs and the reason this axis exists.
	//
	// The value is a route key. Two spellings match:
	//
	//	route: search        the bare key, which is what you normally write
	//	route: "exa:search"  the fully qualified key, when disambiguating
	//
	// Route keys deliberately collapse ALIASES: Perplexity serves /v1/sonar,
	// /chat/completions and /v1/chat/completions from one key, so `route:
	// completions` matches a request through any of the three. That is the same
	// grouping the fault engine counts attempts on, which is what keeps a retry
	// through an alias drawing on the budget the scenario scripted.
	//
	// A name that matches no route the provider registers is an ERROR at load,
	// not a turn that quietly never fires. See provider.ValidateScenario.
	Route string `yaml:"route,omitempty"`

	// CallIndex matches the zero-based count of prior requests in this turn
	// lane (see TurnKey). This is the primitive an agentic loop needs and the
	// one the fault engine already maintains.
	//
	// Note the interaction with Route: the default TurnKey is ["route"], so the
	// cursor is ALREADY per route. `{route: poll, call_index: 2}` means the third
	// call to the poll route, not the third call to the provider.
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

// TurnKey declares what the turn cursor is keyed on — the "lane" a request
// belongs to. Default is ["route"], which reproduces one sequence per route
// and is correct for a single serial caller.
//
// Extractors, evaluated in order and joined to form the lane key:
//
//	"route"                    the Route.FaultKey (default)
//	"body_json:<dotted.path>"  a scalar from the decoded request body
//	"header:<name>"            a request header value
//
// Example — one lane per model, which is what semspec needed:
//
//	turn_key: ["route", "body_json:model"]
//
// A request whose extractor finds nothing falls into the lane named by the
// extractors that did resolve, and collects a scenario.turn_key_unresolved
// warning. It does NOT silently share the default lane: silently merging lanes
// is precisely the bug this field exists to prevent, so it must be visible in
// the journal.
type TurnKey []string

// Turn-key extractor names and prefixes.
const (
	TurnKeyRoute          = "route"
	TurnKeyBodyJSONPrefix = "body_json:"
	TurnKeyHeaderPrefix   = "header:"
)

// Extractors returns the extractor list, defaulting to ["route"] when unset.
func (k TurnKey) Extractors() []string {
	if len(k) == 0 {
		return []string{TurnKeyRoute}
	}
	return slices.Clone(k)
}

// ExtraFields are additional response properties merged into a rendered body.
// They exercise a consumer's tolerance of additive vendor changes.
type ExtraFields map[string]any

// AuthMode selects how a provider treats credentials on a request.
type AuthMode string

// Supported authentication modes.
const (
	AuthRequired AuthMode = "required" // default
	AuthOptional AuthMode = "optional"
	AuthReject   AuthMode = "reject" // always 401, for unauthorized scenarios
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

// CreatePolicy is the create-side envelope of an async provider entry: what a
// scenario declares about the POST that mints a job, as opposed to the polls
// that read it.
//
// It carries only a fault plan today. The create RESPONSE is derived in full —
// the identifier and the vendor's initial status — because a projection body
// alongside `turns:` is a load error, so there is nowhere honest to put
// create-side body keys. If create-side body scripting is ever needed, this is
// where it goes, and adding a field here is additive.
type CreatePolicy struct {
	// Fault is the create route's attempt budget, independent of the poll
	// route's. A poll retry must not consume the create's retries, and a retry of
	// one must not be answered from the other's plan.
	Fault *Fault `yaml:"fault,omitempty"`
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

// StreamPolicy selects what a provider does with a request that asks to
// stream.
type StreamPolicy string

// Supported streaming policies. Warn is unchanged and remains the default, so
// a scenario that declares no stream script behaves exactly as it did before
// StreamServe existed.
const (
	StreamWarn   StreamPolicy = "warn"   // default: journal warning, ordinary JSON body
	StreamReject StreamPolicy = "reject" // provider-shaped 4xx
	StreamServe  StreamPolicy = "stream" // serve the scripted SSE sequence
)

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
// the YAML line.
func (r *SourceRef) UnmarshalYAML(value *yaml.Node) error {
	type rawSourceRef SourceRef
	return DecodeRefOrMapping(value, r, (*rawSourceRef)(r))
}

// SeedKey returns the stable key derived identifiers hang off: Seed when set,
// otherwise Name, otherwise "servicesim".
func (s *Scenario) SeedKey() string {
	if s == nil {
		return "servicesim"
	}
	if s.Seed != "" {
		return s.Seed
	}
	if s.Name != "" {
		return s.Name
	}
	return "servicesim"
}

// BaseTime returns Time.Base, defaulting to DefaultBaseTime.
func (s *Scenario) BaseTime() time.Time {
	if s == nil || s.Time.Base.IsZero() {
		return DefaultBaseTime
	}
	return s.Time.Base
}

// Path returns the file this scenario was loaded from, or the empty string for a
// scenario built in memory. It is for diagnostics only.
func (s *Scenario) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Provider returns the entry declared under name, or nil. It is nil-safe on every
// hop — the scenario, the registry and the entry may each be absent — so that a
// route's fault selector stays a one-liner instead of a chain of field reads that
// panics on a partial scenario.
func (s *Scenario) Provider(name string) *ProviderEntry {
	if s == nil {
		return nil
	}
	return s.Providers.Get(name)
}

// HasFaults reports whether any turn of any provider declares a fault plan with at
// least one attempt. provider.Deps.Normalized consults it to warn when a scenario
// declares faults but no fault engine was supplied.
func (s *Scenario) HasFaults() bool {
	if s == nil {
		return false
	}
	for _, name := range s.Providers.Names() {
		e := s.Providers.Get(name)
		if e == nil {
			continue
		}
		// A create-only plan counts. Without this a scenario whose only fault is
		// `create.fault` reports no faults, so Deps.Normalized skips the
		// deps.faults_ignored warning and the author's scripted create fault
		// silently never fires — the exact wiring mistake that warning exists to
		// catch.
		if e.Create != nil && e.Create.Fault.HasAttempts() {
			return true
		}
		for i := range e.Turns {
			if e.Turns[i].Fault.HasAttempts() {
				return true
			}
		}
	}
	return false
}

// Empty returns a valid scenario with no sources and no projections. Every
// provider renders a well-shaped empty success against it, which makes
// exa.New(provider.Deps{}) a usable zero-configuration handler.
func Empty() *Scenario {
	s := &Scenario{Version: SchemaVersion, Name: "servicesim"}
	// Resolve on an empty corpus cannot fail; the indexes must still exist so
	// SourceByID never nil-maps.
	_ = s.Resolve()
	return s
}

// UnmarshalYAML decodes the open provider registry, preserving declaration order.
//
// Reserved envelope keys are decoded into the entry; everything else is the
// provider's projection body and is left as an undecoded node. A block with no
// "turns:" is normalised into exactly one unconditional turn, so everything
// downstream sees one shape and no handler branches on which form the author
// wrote.
func (p *Providers) UnmarshalYAML(value *yaml.Node) error {
	p.order = nil
	p.entries = make(map[string]*ProviderEntry)

	if value == nil || value.Kind == 0 {
		return nil
	}
	if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
		return nil
	}

	// Resolve every alias in the tree before anything downstream retains a raw
	// *yaml.Node. decodeProviderEntry, decodeTurns, normalizeRespond and every
	// nested auth/validation/create/turn_key node all descend from value and are
	// all retained un-decoded — see resolveAliases' doc comment for why an
	// AliasNode cannot survive past this point. A self-referencing anchor or a
	// pathological fan-out is reported here, at load time, rather than
	// surfacing later as a stack overflow or a strict-decode error whose
	// message names the wrong symptom.
	var err error
	value, err = resolveAliases(value)
	if err != nil {
		return fmt.Errorf("providers: %w", err)
	}

	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("providers: line %d: expected a mapping of provider name to provider block, got a %s",
			value.Line, nodeKindName(value.Kind))
	}

	for i := 0; i+1 < len(value.Content); i += 2 {
		key, block := value.Content[i], value.Content[i+1]
		name := key.Value
		if key.Kind != yaml.ScalarNode || name == "" {
			return fmt.Errorf("providers: line %d: provider name must be a non-empty string", key.Line)
		}
		if _, dup := p.entries[name]; dup {
			return fmt.Errorf("providers: line %d: duplicate provider %q", key.Line, name)
		}
		entry, err := decodeProviderEntry(name, block)
		if err != nil {
			return err
		}
		p.order = append(p.order, name)
		p.entries[name] = entry
	}
	return nil
}

func decodeProviderEntry(name string, node *yaml.Node) (*ProviderEntry, error) {
	entry := &ProviderEntry{Name: name, Kind: name}
	base := "providers." + name

	if node == nil || node.Kind == 0 || (node.Kind == yaml.ScalarNode && node.Tag == "!!null") {
		entry.Turns = []Turn{{Respond: emptyMapping(node)}}
		return entry, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: line %d: provider block must be a mapping, got a %s",
			base, node.Line, nodeKindName(node.Kind))
	}

	body := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: node.Line, Column: node.Column}
	var turnsNode, faultNode *yaml.Node

	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i], node.Content[i+1]
		switch key.Value {
		case keyKind:
			if err := val.Decode(&entry.Kind); err != nil {
				return nil, fmt.Errorf("%s.kind: line %d: %w", base, val.Line, err)
			}
			if entry.Kind == "" {
				entry.Kind = name
			}
		case keyAuth:
			entry.Auth = &AuthPolicy{}
			if err := DecodeStrict(val, entry.Auth); err != nil {
				return nil, fmt.Errorf("%s.auth: %w", base, err)
			}
		case keyValidation:
			entry.Validation = &ValidationPolicy{}
			if err := DecodeStrict(val, entry.Validation); err != nil {
				return nil, fmt.Errorf("%s.validation: %w", base, err)
			}
		case keyTurnKey:
			if err := val.Decode(&entry.TurnKey); err != nil {
				return nil, fmt.Errorf("%s.turn_key: line %d: %w", base, val.Line, err)
			}
		case keyCreate:
			entry.Create = &CreatePolicy{}
			if err := DecodeStrict(val, entry.Create); err != nil {
				return nil, fmt.Errorf("%s.create: %w", base, err)
			}
		case keyFault:
			faultNode = val
		case keyTurns:
			turnsNode = val
		default:
			body.Content = append(body.Content, key, val)
		}
	}

	if turnsNode == nil {
		turn := Turn{Respond: *body}
		if faultNode != nil {
			turn.Fault = &Fault{}
			if err := DecodeStrict(faultNode, turn.Fault); err != nil {
				return nil, fmt.Errorf("%s.fault: %w", base, err)
			}
		}
		entry.Turns = []Turn{turn}
		return entry, nil
	}

	entry.scripted = true
	entry.faultWithTurns = faultNode != nil
	for i := 0; i+1 < len(body.Content); i += 2 {
		entry.strayKeys = append(entry.strayKeys, body.Content[i].Value)
	}
	turns, err := decodeTurns(base, turnsNode)
	if err != nil {
		return nil, err
	}
	entry.Turns = turns
	return entry, nil
}

func decodeTurns(base string, node *yaml.Node) ([]Turn, error) {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s.turns: line %d: expected a list of turns, got a %s",
			base, node.Line, nodeKindName(node.Kind))
	}
	turns := make([]Turn, 0, len(node.Content))
	for i, item := range node.Content {
		var turn Turn
		if err := DecodeStrict(item, &turn); err != nil {
			return nil, fmt.Errorf("%s.turns[%d]: %w", base, i, err)
		}
		normalizeRespond(&turn.Respond, item)
		turns = append(turns, turn)
	}
	return turns, nil
}

// normalizeRespond turns an absent or explicitly null respond body into an empty
// mapping, so every downstream consumer sees one shape. A body that is present
// but not a mapping is left alone for Validate to report.
func normalizeRespond(n *yaml.Node, at *yaml.Node) {
	if n.Kind == 0 || (n.Kind == yaml.ScalarNode && n.Tag == "!!null") {
		*n = emptyMapping(at)
	}
}

func emptyMapping(at *yaml.Node) yaml.Node {
	n := yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if at != nil {
		n.Line, n.Column = at.Line, at.Column
	}
	return n
}

// DecodeProjection decodes a turn's Respond node into a provider's typed
// projection. Provider packages call this; the scenario package never knows
// the concrete type.
//
// It reports an error naming the provider and turn index, because "cannot
// unmarshal string into int" with no location is unactionable in a fixture
// with twelve turns.
func (t *Turn) DecodeProjection(name string, index int, into any) error {
	if t == nil {
		return fmt.Errorf("providers.%s.turns[%d]: no turn to decode", name, index)
	}
	if err := DecodeStrict(&t.Respond, into); err != nil {
		return fmt.Errorf("providers.%s.turns[%d].respond: %w", name, index, err)
	}
	return nil
}

// Matches reports whether the request described by callIndex and body satisfies
// every field this predicate declares. A nil or empty Match matches everything,
// and every present field must match — an AND, never an OR.
//
// It reads the request and the call counter only. It never consults a clock,
// because a predicate that depends on wall time is a flaky test waiting to happen.
func (m *Match) Matches(callIndex int, route string, body []byte) bool {
	if m == nil {
		return true
	}
	if m.Route != "" && !RouteMatches(m.Route, route) {
		return false
	}
	if m.CallIndex != nil && *m.CallIndex != callIndex {
		return false
	}
	if m.BodyContains != "" && !bytes.Contains(body, []byte(m.BodyContains)) {
		return false
	}
	if len(m.BodyJSON) == 0 {
		return true
	}
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return false
	}
	for path, want := range m.BodyJSON {
		got, ok := lookupJSONPath(decoded, path)
		if !ok || got != want {
			return false
		}
	}
	return true
}

// IsEmpty reports whether the predicate constrains nothing, which is the same as
// having no `when` at all. Nil-safe.
func (m *Match) IsEmpty() bool {
	return m == nil ||
		(m.Route == "" && m.CallIndex == nil && m.BodyContains == "" && len(m.BodyJSON) == 0)
}

// RouteMatches reports whether an authored `when.route:` name selects the route
// key serving a request.
//
// Two spellings are accepted, and the asymmetry is deliberate. A bare name
// ("search") matches the last segment of any key, because inside one provider's
// block the qualifier is constant noise. A qualified name ("exa:search") must
// match the key EXACTLY — it is never reduced to its own last segment first,
// which would make `route: "exa:search"` silently match tavily:search if someone
// pasted it into the wrong block. Being explicit must never widen a match.
//
// It is exported for one reason: provider.ValidateScenario must reject a route
// name that matches nothing, and a validator using a second, separately-written
// copy of this rule would eventually disagree with the matcher about what a
// scenario means. Two derivations are two chances to disagree.
func RouteMatches(authored, key string) bool {
	if authored == key {
		return true
	}
	if strings.Contains(authored, ":") {
		return false
	}
	return authored == RouteKeySuffix(key)
}

// RouteKeySuffix returns the part of a route key after its last colon, which is
// the bare name a scenario author writes. Exported alongside RouteMatches so a
// validator can tell an author which names it accepts using the same reduction
// the matcher applies.
func RouteKeySuffix(key string) string {
	if i := strings.LastIndex(key, ":"); i >= 0 {
		return key[i+1:]
	}
	return key
}

// lookupJSONPath walks a dotted path over a decoded JSON value, treating a
// numeric segment as an array index, and formats the scalar it lands on.
func lookupJSONPath(v any, path string) (string, bool) {
	cur := v
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return "", false
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return "", false
			}
			cur = node[idx]
		default:
			return "", false
		}
	}
	return formatJSONScalar(cur)
}

func formatJSONScalar(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "null", true
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case json.Number:
		return x.String(), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	default:
		return "", false
	}
}

// DecodeRefOrMapping decodes value into out, accepting a scalar node as the
// shorthand for {source: <scalar>}. It is the single implementation behind the
// SourceRef, ExaResult, TavilyResult and PerplexityResult UnmarshalYAML methods;
// ref points at out's embedded SourceRef (at out itself for a bare SourceRef) and
// is what the scalar branch fills in.
//
// Callers pass a pointer to a *defined-type copy* — type rawExaResult ExaResult —
// which does not carry the UnmarshalYAML method, so the mapping branch cannot
// re-enter the method that called it. Forgetting that is an infinite recursion,
// not a compile error.
//
// The mapping branch calls DecodeStrict rather than value.Decode, because
// yaml.Node.Decode builds a fresh decoder with KnownFields off: without this, a
// custom UnmarshalYAML would silently reopen the door to typo'd scenario keys
// that Load's KnownFields(true) is there to close.
func DecodeRefOrMapping(value *yaml.Node, ref *SourceRef, out any) error {
	if value == nil || value.Kind == 0 {
		return nil
	}
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag == "!!null" {
			return fmt.Errorf("line %d: expected a source id or a mapping, got null", value.Line)
		}
		var id string
		if err := value.Decode(&id); err != nil {
			return fmt.Errorf("line %d: expected a source id: %w", value.Line, err)
		}
		ref.Ref = id
		return nil
	case yaml.MappingNode:
		return DecodeStrict(value, out)
	default:
		return fmt.Errorf("line %d: expected a source id or a mapping, got a %s",
			value.Line, nodeKindName(value.Kind))
	}
}

// DecodeStrict decodes a mapping node into out, rejecting any key that out's type
// does not declare, at every level of nesting. An absent or explicitly null node
// leaves out untouched.
//
// Before decoding it checks out's type for a YAML key or a Go field name that is
// declared twice across an inline boundary. yaml.v3 v3.0.1 does not return an
// error for that case — it *panics*, taking the process down on the first scenario
// that contains such an entry — so the check has to happen before the decode, not
// after.
func DecodeStrict(value *yaml.Node, out any) error {
	if value == nil || value.Kind == 0 {
		return nil
	}
	if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: expected a mapping, got a %s", value.Line, nodeKindName(value.Kind))
	}
	if err := CheckInlineKeys(reflect.TypeOf(out)); err != nil {
		return err
	}

	// yaml.Node.Decode builds a decoder with KnownFields off and there is no way
	// to configure it, so the node is re-encoded and fed to a strict decoder. The
	// cost is paid once per provider block at load time.
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("line %d: %w", value.Line, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(encoded))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// openInlineMarker stands for "this type inlines a map, so any key is known".
const openInlineMarker = "\x00inline-map"

var inlineCheckCache sync.Map // reflect.Type -> error

// CheckInlineKeys reports a YAML key or Go field name declared twice across an
// inline boundary anywhere in t's type graph.
//
// It exists because a duplicate inline key is a runtime panic that no compiler
// and no vet pass catches, and because the panic surfaces at container startup in
// a consumer's CI rather than in this repository's tests. Provider packages get
// the check for free through DecodeStrict; the schema round-trip test calls it
// directly over every projection struct.
func CheckInlineKeys(t reflect.Type) error {
	if t == nil {
		return nil
	}
	if cached, ok := inlineCheckCache.Load(t); ok {
		if cached == nil {
			return nil
		}
		err, _ := cached.(error)
		return err
	}
	err := checkInlineKeysAt(t, map[reflect.Type]bool{})
	if err == nil {
		inlineCheckCache.Store(t, nil)
	} else {
		inlineCheckCache.Store(t, err)
	}
	return err
}

func checkInlineKeysAt(t reflect.Type, seen map[reflect.Type]bool) error {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return nil
	}
	seen[t] = true
	if _, err := structKeys(t); err != nil {
		return err
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if err := checkInlineKeysAt(f.Type, seen); err != nil {
			return err
		}
	}
	return nil
}

// structKeys derives the YAML key set yaml.v3 would accept for t, flattening
// inline structs, and reports the first duplicate it finds.
func structKeys(t reflect.Type) (map[string]bool, error) {
	keys := make(map[string]bool)
	goNames := make(map[string]bool)
	if err := collectKeys(t, t, keys, goNames, 0); err != nil {
		return nil, err
	}
	return keys, nil
}

func collectKeys(root, t reflect.Type, keys, goNames map[string]bool, depth int) error {
	if depth > 8 {
		return nil
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if slices.Contains(strings.Split(opts, ","), "inline") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			switch ft.Kind() {
			case reflect.Struct:
				if err := collectKeys(root, ft, keys, goNames, depth+1); err != nil {
					return err
				}
			case reflect.Map:
				keys[openInlineMarker] = true
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if keys[name] {
			return fmt.Errorf("duplicated YAML key %q in struct %s: an inlined struct shares one key namespace with its outer struct, and yaml.v3 panics on the collision", name, root)
		}
		keys[name] = true
		if goNames[f.Name] {
			return fmt.Errorf("duplicated Go field name %q in struct %s: an outer field silently shadows the promoted one, so render code and Resolve read different values", f.Name, root)
		}
		goNames[f.Name] = true
	}
	return nil
}

func nodeKindName(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "list"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return "value"
	}
}
