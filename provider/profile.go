package provider

import (
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"slices"

	"github.com/c360studio/servicesim/scenario"
)

// Finding codes the framework seam raises. See handle.go's Code* block for
// the ones the shared lifecycle already owned before this unit.
const (
	// CodeHandlerPanic is recorded when a route handler panics with anything
	// other than http.ErrAbortHandler. The panic is converted to a
	// RefuseInternal 500 in the profile's own error shape rather than
	// reaching the client as a connection reset with no status and no body
	// (verified: HTTP 000, before this finding existed).
	CodeHandlerPanic = "handler.panic"

	// CodeRefusalEmptyBody is recorded when a Profile's ErrorBody returns no
	// bytes for a Refusal. The response is still served, with whatever
	// ErrorBody returned (nothing): NewSet already refuses a Profile with a
	// nil ErrorBody outright, so a runtime empty body means the function is
	// present but returned nothing for this particular Refusal — a profile
	// bug worth surfacing, not a reason to invent a body the profile never
	// asked to send.
	CodeRefusalEmptyBody = "refusal.empty_body"

	// CodeStreamGrammarMissing is recorded when a Response.Stream declares an
	// empty Grammar. Handle refuses the request as RefuseInternal: an open
	// grammar vocabulary (see Stream.Grammar) means the framework can no
	// longer default a missing value to one dialect's framing, so a profile
	// that forgets to set it must fail loudly rather than silently inherit
	// behaviour that was never asked for.
	CodeStreamGrammarMissing = "stream.grammar_missing"
)

// RefusalKind names one of the framework-generated refusals a Profile renders
// in its vendor's own error shape.
type RefusalKind string

// The five refusal kinds. The proposal that designed this seam
// (docs/proposals/framework-seam.md) lists four; RefuseRequest is the
// compiler-settled fifth, added in this unit for [Exchange.Reject] (Phase 10
// unit 2 §4): a rejected request needs its own kind so a profile can render
// the vendor's validation-error envelope from [Refusal.X]'s Findings, which
// is exactly what each in-tree profile's own errorResponse already does.
const (
	// RefuseNotFound is raised for a path no route serves.
	RefuseNotFound RefusalKind = "not_found"

	// RefuseMethodNotAllowed is raised for a known path with an unsupported
	// method. Refusal.Allow carries the sorted list of methods the path does
	// support.
	RefuseMethodNotAllowed RefusalKind = "method_not_allowed"

	// RefuseScenarioUnknown is raised when a request's /x/<scenario> prefix
	// names a scenario this process does not serve, or names none where
	// there is no default. It is the one kind that may be raised outside any
	// request the profile could handle — see Refusal.X.
	RefuseScenarioUnknown RefusalKind = "scenario_unknown"

	// RefuseInternal is raised when a handler panics with anything other
	// than http.ErrAbortHandler, and when a Response declares a Stream with
	// an empty Grammar.
	RefuseInternal RefusalKind = "internal"

	// RefuseRequest is raised by [Exchange.Reject] for a request rejected on
	// its own terms — a missing field, a bad enum value, a missing
	// credential. Status is always supplied by the caller; Refuse's
	// Kind-to-Status default does not cover this kind, because no one status
	// is right for every rejection.
	RefuseRequest RefusalKind = "request"
)

// Refusal describes a framework-generated refusal a Profile renders in its
// vendor's own error shape, through Profile.ErrorBody.
type Refusal struct {
	Kind RefusalKind

	// Status is the HTTP status the refusal serves. Profile.Refuse fills it
	// from Kind when zero, for every kind except RefuseRequest, whose caller
	// always supplies one.
	Status int

	// Allow is the sorted list of methods a path supports, set only for
	// RefuseMethodNotAllowed.
	Allow []string

	// X is the request this refusal answers, or nil when the refusal was
	// raised outside any request a profile could handle — today, that is
	// exactly RefuseScenarioUnknown answered before any listener resolved a
	// scenario. Every other kind always carries one.
	X *Exchange
}

// Profile is the registration record a Set is built from: everything the
// framework needs to serve one simulated vendor. Every field cites the
// composition-layer enumeration site it replaces (docs/proposals/
// framework-seam.md, "The Profile registration record"); a field that
// cannot cite one is not added.
type Profile struct {
	// Name is the listener's identity: the base-URL env prefix
	// (<NAME>_BASE_URL), the flag/env port name (-<name>-port,
	// SERVICESIM_<NAME>_PORT), and the journal's "provider" field.
	Name Name

	// Kind is the handler implementation this profile is an instance OF.
	// Empty means Name. It is the forward-looking field for an
	// OpenAI-compatible shape served as several distinct listeners from one
	// handler; see the package design's "Kind" discussion. Stored now,
	// threaded through config/server/testkit in unit 8.
	//
	// Not yet threaded: two instances of one Kind (Kind != Name, single
	// entry, accepted by NewSet — see "instancing" in Validate's doc
	// comment) still share one fault counter and one fault plan per Route,
	// because Route.FaultKey and (*Set).Faults are keyed on the route, not
	// on which listener is asking. A scenario author who wants the two
	// instances to draw on independent budgets must give each instance's
	// Routes a distinct FaultKey; unit 8's threading of Kind through the
	// fault engine is what makes that automatic.
	Kind string

	// Title is the human-readable vendor name: "Exa", "Perplexity", "MCP" —
	// flag usage text, --help, the docs index.
	Title string

	// Summary is one clause for the --help banner.
	Summary string

	// Port is the default listener port. Zero asks the OS for an ephemeral
	// one.
	Port int

	// Handlers maps a Route.Pattern to the handler serving it.
	Handlers map[string]Handler

	// Routes are every route this profile serves, each carrying the fault
	// budget it draws on.
	Routes []Route

	// Validators are this profile's projection validators, keyed by scenario
	// ENTRY KIND — one listener may contribute several (Perplexity's Sonar
	// and Agent surfaces today).
	Validators map[string]Validator

	// ErrorBody renders a Refusal in this vendor's own error shape. REQUIRED:
	// NewSet refuses a Profile whose ErrorBody is nil (house rule 3 — an
	// unmatched path, method, provider or scenario must never answer with an
	// empty body).
	ErrorBody func(Refusal) []byte

	// DefaultAuth is the mode an entry with no auth: block of its own gets.
	// Empty means scenario.AuthRequired. It is per profile and deliberately
	// opposite in-tree: MCP defaults optional, the three research profiles
	// default required — see [Exchange.AuthPolicy] and internal/server's
	// relaxAuth, which consults this field rather than flattening every
	// profile to one strict-auth behaviour.
	DefaultAuth scenario.AuthMode

	// Announce runs once, at Handler construction, for a startup line that is
	// a property of the simulated API rather than of any request —
	// Perplexity's Sonar sunset date is the shipped example. Nil means
	// nothing is announced.
	Announce func(Deps)

	// Contracts is this profile's golden fixtures, provenance record and
	// README, as an fs.FS. The four reference profiles pass a sub-FS of
	// contracts.FS() for now (Phase 10 unit 6 genericises contracts around
	// this field).
	Contracts fs.FS

	// Hosts are the vendor's real hostnames — never dialled, and what
	// scripts/lint-no-live-hosts.sh (and, out of tree, testkit.AssertNoLiveHosts
	// in a later unit) refuses a fixture or a Go default for naming.
	Hosts []string

	// DerivedIDs are response JSON paths whose value is derived per call
	// (an attempt-varying request/response identifier), pruned by default
	// from a golden compare — see testkit/golden.go's derivedIDPaths, which
	// this field replaces the hand-maintained union of.
	DerivedIDs []string

	// StreamDerivedIDs are the same, for paths inside a decoded SSE frame
	// rather than an ordinary response body.
	StreamDerivedIDs []string

	// CredentialNames are this vendor's credential header or JSON-body
	// property names, merged into redaction at composition (house rule 4).
	CredentialNames []string
}

// maxProfileNameLen bounds Profile.Name. It is well inside a URL path
// segment or a shell flag's practical length, and existing names (the
// longest in tree is "perplexity", ten characters) are nowhere near it.
const maxProfileNameLen = 63

// validProfileName reports whether name is flag- and path-safe: one lowercase
// letter, then any number of lowercase letters, digits, '-' or '_'. It is the
// shape a "-<name>-port" flag and a "/n/<namespace>" path segment both need,
// and it matches [ValidNamespace]'s character class with the added
// requirement that the first character is a letter — Name feeds an
// identifier position (a flag name, an env var suffix) that ValidNamespace's
// own callers do not.
func validProfileName(name Name) bool {
	s := string(name)
	if s == "" || len(s) > maxProfileNameLen {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// Validate reports the first reason p may not be registered. NewSet calls it
// for every profile before any cross-profile check runs, so one bad profile
// is named precisely instead of folding into a generic NewSet failure.
//
// It does not check anything that needs comparing p against its siblings —
// duplicate names, duplicate ports, an entry kind claimed by two different
// Kinds — because Validate has no siblings to compare against; NewSet checks
// those itself once every profile has passed this.
func (p Profile) Validate() error {
	if p.ErrorBody == nil {
		return fmt.Errorf("provider: profile %q: ErrorBody is required (house rule 3: "+
			"an unmatched path, method or scenario must render a provider-shaped body, never an empty one)", p.Name)
	}
	if len(p.Handlers) == 0 {
		return fmt.Errorf("provider: profile %q: Handlers is empty", p.Name)
	}
	if len(p.Routes) == 0 {
		return fmt.Errorf("provider: profile %q: Routes is empty", p.Name)
	}
	for _, rt := range p.Routes {
		if rt.FaultKey == "" {
			return fmt.Errorf("provider: profile %q: route %q declares no FaultKey", p.Name, rt.Pattern)
		}
		if p.Handlers[rt.Pattern] == nil {
			return fmt.Errorf(
				"provider: profile %q: route %q has no entry in Handlers (house rule 3: a registered "+
					"route must never serve an empty body)", p.Name, rt.Pattern)
		}
	}
	for pattern := range p.Handlers {
		if !slices.ContainsFunc(p.Routes, func(rt Route) bool { return rt.Pattern == pattern }) {
			return fmt.Errorf(
				"provider: profile %q: Handlers has an entry for %q with no matching Route; "+
					"it would be registered and silently unreachable", p.Name, pattern)
		}
	}
	if !validProfileName(p.Name) {
		return fmt.Errorf(
			"provider: profile %q: Name must be flag- and path-safe: one lowercase letter then "+
				"lowercase letters, digits, '-' or '_'", p.Name)
	}
	switch p.DefaultAuth {
	case "", scenario.AuthRequired, scenario.AuthOptional:
	default:
		return fmt.Errorf("provider: profile %q: DefaultAuth %q must be \"\", %q or %q",
			p.Name, p.DefaultAuth, scenario.AuthRequired, scenario.AuthOptional)
	}
	if p.Kind != "" && p.Kind != string(p.Name) && len(p.Validators) > 1 {
		return fmt.Errorf(
			"provider: profile %q: instances kind %q but declares %d entry kinds; "+
				"v0.5.0 supports instancing only for a single-entry profile (refusing rather than guessing "+
				"how the secondary entries rename)",
			p.Name, p.Kind, len(p.Validators))
	}
	return nil
}

// effectiveKind returns p.Kind, defaulting to p.Name — the same default
// AuthPolicy documents for Kind in general.
func (p Profile) effectiveKind() string {
	if p.Kind != "" {
		return p.Kind
	}
	return string(p.Name)
}

// Handler builds this profile's listener handler over d, through NewMux. It
// is what every reference profile's <pkg>.New did before this unit; <pkg>.New
// is now a deprecated one-line wrapper over Profile().Handler(d).
//
// d is normalised once here (NewMux normalises again, idempotently) so
// Announce — if set — observes the same defaulted Deps every request does.
func (p Profile) Handler(d Deps) http.Handler {
	d = d.Normalized()
	if p.Announce != nil {
		p.Announce(d)
	}
	handlers := make(map[string]Handler, len(p.Handlers))
	for pattern, h := range p.Handlers {
		handlers[pattern] = installProfile(p, h)
	}
	return NewMux(d, p.Name, p.Refuse, MuxSpec{Routes: p.Routes, Handlers: handlers})
}

// installProfile wraps h so every Exchange it receives carries this
// profile's ErrorBody, DefaultAuth and effective Kind (see the Exchange
// fields' own doc comments) before the handler runs — what [Exchange.Reject],
// [Exchange.AuthPolicy] and [Exchange.EntryFor] read and cannot otherwise
// reach, since Handle's own signature carries only the profile's Name, not
// the Profile itself. It wraps the ROUTE handlers only: NewMux's own
// built-in 404/405/panic refusals call p.Refuse directly through a bound
// closure and never read these fields.
func installProfile(p Profile, h Handler) Handler {
	kind := p.effectiveKind()
	return func(x *Exchange) Response {
		x.errorBody = p.ErrorBody
		x.defaultAuth = p.DefaultAuth
		x.kind = kind
		return h(x)
	}
}

// defaultRefusalStatus is the HTTP status [Profile.Refuse] fills in for a
// Refusal whose Status is zero. RefuseRequest has none: no single status is
// right for every rejection, and its caller ([Exchange.Reject]) always
// supplies one.
func defaultRefusalStatus(kind RefusalKind) int {
	switch kind {
	case RefuseNotFound:
		return http.StatusNotFound
	case RefuseMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case RefuseScenarioUnknown:
		return http.StatusNotFound
	case RefuseInternal:
		return http.StatusInternalServerError
	default:
		return 0
	}
}

// Refuse renders r through p.ErrorBody, filling r.Status from r.Kind when
// zero (see defaultRefusalStatus).
//
// An empty return is not papered over with an invented body: NewSet already
// refuses a Profile whose ErrorBody is nil outright (house rule 3's
// registration-time half), so a runtime empty body means ErrorBody is
// present but chose to return nothing for this particular Refusal — a
// profile bug, not a framework hole. It is journaled as
// [CodeRefusalEmptyBody] when r.X is non-nil, and served as-is either way:
// inventing a `{}` here would hide the bug behind a body that looks
// intentional.
func (p Profile) Refuse(r Refusal) []byte {
	if r.Status == 0 {
		r.Status = defaultRefusalStatus(r.Kind)
	}
	var body []byte
	if p.ErrorBody != nil {
		body = p.ErrorBody(r)
	}
	if len(body) == 0 && r.X != nil {
		r.X.Warn(CodeRefusalEmptyBody, "",
			"profile %q's ErrorBody returned no bytes for refusal kind %q", p.Name, r.Kind)
	}
	return body
}

// cloneProfileFields returns p with every reference-typed field replaced by
// an independent copy: Handlers and Validators (maps.Clone), and Routes,
// Hosts, DerivedIDs, StreamDerivedIDs and CredentialNames (slices.Clone).
// Contracts (an fs.FS), ErrorBody, Announce and each Route's own Fault func
// are left shared — a func value and an fs.FS expose no mutable state a
// caller could reach through the clone, unlike a map or a slice's backing
// array.
//
// NewSet calls this once per profile as it is registered, so a caller
// mutating the Profile value it passed in — its own Handlers map, say —
// after NewSet returns cannot reach what the Set stores. All and Lookup call
// it again on the way OUT, so a caller mutating what either returned cannot
// reach the Set's own copy, and two separate calls to All (or Lookup) return
// two copies that cannot reach each other either: "a caller mutating the
// slice it got back must not be able to reach the Set's own copy" (this
// file's own doc comment on Set) holds in both directions, not only in.
func cloneProfileFields(p Profile) Profile {
	p.Handlers = maps.Clone(p.Handlers)
	p.Validators = maps.Clone(p.Validators)
	p.Routes = slices.Clone(p.Routes)
	p.Hosts = slices.Clone(p.Hosts)
	p.DerivedIDs = slices.Clone(p.DerivedIDs)
	p.StreamDerivedIDs = slices.Clone(p.StreamDerivedIDs)
	p.CredentialNames = slices.Clone(p.CredentialNames)
	return p
}

// Set is an ordered, validated, immutable registry of profiles — the ONLY
// input to composition. There is no package-level registry and no init():
// registration is an explicit value built at the composition root (house
// rule 6), and registration order is load-bearing (house rule 2) — a map
// range would make listener order, and any message listing profiles, differ
// between runs.
type Set struct {
	profiles []Profile
	byName   map[Name]Profile
}

// NewSet validates every profile and the registry as a whole, and refuses
// rather than builds a Set it cannot vouch for. Each profile is checked with
// [Profile.Validate] first; NewSet itself then checks what only comparing
// profiles against each other can find: duplicate names, duplicate non-zero
// ports, and one scenario entry kind claimed by two different Kinds
// (same-Kind duplicates — two instances of one Kind — are accepted).
func NewSet(ps ...Profile) (*Set, error) {
	s := &Set{byName: make(map[Name]Profile, len(ps))}
	ports := make(map[int]Name, len(ps))
	kindOf := make(map[string]string, len(ps)) // entry kind -> the Kind that claimed it

	for _, p := range ps {
		if err := p.Validate(); err != nil {
			return nil, err
		}
		// Cloned once here, after validation and before storage: p (the
		// caller's own value) still shares its Handlers/Validators/Routes
		// with whatever the caller holds, and the Set must never be
		// mutable through that shared reference (see cloneProfileFields).
		p = cloneProfileFields(p)
		if _, dup := s.byName[p.Name]; dup {
			return nil, fmt.Errorf("provider: NewSet: duplicate profile name %q", p.Name)
		}
		if p.Port != 0 {
			if other, dup := ports[p.Port]; dup {
				return nil, fmt.Errorf("provider: NewSet: profiles %q and %q both claim port %d", other, p.Name, p.Port)
			}
			ports[p.Port] = p.Name
		}

		kind := p.effectiveKind()
		for ek := range p.Validators {
			if owner, claimed := kindOf[ek]; claimed && owner != kind {
				return nil, fmt.Errorf(
					"provider: NewSet: scenario entry kind %q is claimed by both profile kind %q and %q; "+
						"two implementations of one scenario vocabulary is exactly the ambiguity registration must refuse",
					ek, owner, kind)
			}
			kindOf[ek] = kind
		}

		s.profiles = append(s.profiles, p)
		s.byName[p.Name] = p
	}
	return s, nil
}

// MustSet is [NewSet], panicking on error — for main() only, the way
// regexp.MustCompile is: a composition root that cannot vouch for its own
// registered profiles has no better answer than failing immediately.
func MustSet(ps ...Profile) *Set {
	s, err := NewSet(ps...)
	if err != nil {
		// err's own message already starts with "provider: " (every
		// NewSet/Validate failure is built that way), so panicking with
		// err.Error() directly avoids a doubled "provider: provider: " prefix.
		panic(err.Error())
	}
	return s
}

// All returns every registered profile, in registration order. It returns a
// deep clone: a Set is immutable, and a caller mutating anything it got
// back — the top-level slice, or a returned Profile's own Handlers,
// Routes, Validators or any of its other reference-typed fields — must not
// be able to reach the Set's own copy (see cloneProfileFields).
func (s *Set) All() []Profile {
	out := make([]Profile, len(s.profiles))
	for i, p := range s.profiles {
		out[i] = cloneProfileFields(p)
	}
	return out
}

// Names returns every registered profile's Name, in registration order.
func (s *Set) Names() []Name {
	out := make([]Name, len(s.profiles))
	for i, p := range s.profiles {
		out[i] = p.Name
	}
	return out
}

// Lookup returns the profile registered under name, and whether one was. It
// is the fail-closed hook composition is built on: a miss is never served.
//
// Like All, it returns a deep clone (cloneProfileFields): a caller mutating
// the returned Profile's Handlers, Routes or any other reference-typed field
// must not be able to reach what the Set itself holds.
func (s *Set) Lookup(name Name) (Profile, bool) {
	p, ok := s.byName[name]
	if !ok {
		return Profile{}, false
	}
	return cloneProfileFields(p), true
}

// Routes returns every route of every registered profile, in registration
// order — what a fault engine must be built from so that no registered
// route is fault-blind.
func (s *Set) Routes() []Route {
	var out []Route
	for _, p := range s.profiles {
		out = append(out, p.Routes...)
	}
	return out
}

// Validators returns the merged, entry-kind-keyed validator map of the named
// profiles, or of every registered profile when only is empty. Two profiles
// that share one Kind may safely contribute the same key (NewSet already
// proved they agree); Validators does not need to re-check that here.
func (s *Set) Validators(only ...Name) map[string]Validator {
	profiles := s.profiles
	if len(only) > 0 {
		profiles = make([]Profile, 0, len(only))
		for _, name := range only {
			if p, ok := s.byName[name]; ok {
				profiles = append(profiles, p)
			}
		}
	}
	out := make(map[string]Validator)
	for _, p := range profiles {
		maps.Copy(out, p.Validators)
	}
	return out
}

// EntryKinds returns every scenario entry kind any registered profile
// declares a validator for, sorted. Sorted, not registration order: the
// source is a union of maps, which carries no registration order of its own
// to preserve, and a readiness message that reshuffled between runs would be
// miserable to diff (the same reasoning ValidateScenario's own findings
// ordering already follows).
func (s *Set) EntryKinds() []string {
	seen := make(map[string]struct{})
	for _, p := range s.profiles {
		for k := range p.Validators {
			seen[k] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// DerivedIDs returns the union of every registered profile's DerivedIDs and
// StreamDerivedIDs, in registration order.
func (s *Set) DerivedIDs() (paths, streamPaths []string) {
	for _, p := range s.profiles {
		paths = append(paths, p.DerivedIDs...)
		streamPaths = append(streamPaths, p.StreamDerivedIDs...)
	}
	return paths, streamPaths
}

// LiveHosts returns the union of every registered profile's Hosts, in
// registration order.
func (s *Set) LiveHosts() []string {
	var out []string
	for _, p := range s.profiles {
		out = append(out, p.Hosts...)
	}
	return out
}

// Faults builds a fault engine over sc, pre-registered against every route
// in the Set — not just the routes of whichever listeners a consumer happens
// to have enabled. It is the ONLY exported fault-engine constructor:
// internal/faults.New stays unexported to the module, because a caller that
// could build an engine from its own route slice could build one that does
// not know an out-of-tree profile's route, and that engine would silently
// serve a clean 200 where the scenario scripted a 429 (the quietest failure
// class this framework has).
func (s *Set) Faults(sc *scenario.Scenario, opts ...FaultOption) Faults {
	return newSetFaultEngine(sc, s.Routes(), opts...)
}
