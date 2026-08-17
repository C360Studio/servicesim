package testkit

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c360studio/servicesim/internal/faults"
	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/provider/exa"
	"github.com/c360studio/servicesim/provider/mcp"
	"github.com/c360studio/servicesim/provider/perplexity"
	"github.com/c360studio/servicesim/provider/tavily"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/scenarios"
)

// The journal alias set. It aliases every internal journal type reachable from
// Entry, plus the constants of those types, so a consumer outside this module can
// name them in assertions, in helper signatures, and in a Journal implementation
// of its own.

// Entry is one recorded request in the journal. It aliases the internal journal
// entry so consumers outside this module can name it in assertions.
type Entry = journal.Entry

// Finding is one validation warning or error recorded against a request.
type Finding = journal.Finding

// Journal is the append-only request journal contract. A consumer that wants to
// stream entries somewhere of its own implements this and passes it as
// provider.Deps{Journal: ...}.
type Journal = journal.Journal

// Stats describes journal retention. It is part of the Journal method set, so a
// consumer cannot implement Journal without naming it.
type Stats = journal.Stats

// Outcome is what a recorded request produced.
type Outcome = journal.Outcome

// OutcomeKind classifies what the client received.
type OutcomeKind = journal.OutcomeKind

// AuthObservation is what was observed about a request's credentials. It never
// holds a credential value.
type AuthObservation = journal.AuthObservation

// Severity classifies a validation finding.
type Severity = journal.Severity

// StreamOutcome is what a streamed exchange planned and then delivered,
// reachable as Entry.Outcome.Stream. It aliases the internal journal type so
// a consumer can name it in assertions and in helper signatures.
type StreamOutcome = journal.StreamOutcome

// StreamState is how far a streamed exchange got.
type StreamState = journal.StreamState

// StreamClose is the observed reality of a streamed exchange, the argument a
// consumer's own Journal implementation of journal.StreamCloser receives.
type StreamClose = journal.StreamClose

// Stream states, re-exported as constants of the aliased type for the same
// reason the outcome kinds and severities above are.
const (
	// StreamOpen means the entry was appended and has not closed yet.
	StreamOpen = journal.StreamOpen
	// StreamCompleted means every scripted chunk was delivered.
	StreamCompleted = journal.StreamCompleted
	// StreamAborted means the scenario's scripted abort fired.
	StreamAborted = journal.StreamAborted
	// StreamClientGone means the client hung up or timed out first.
	StreamClientGone = journal.StreamClientGone
)

// Outcome kinds, re-exported as constants of the aliased type so a consumer's
// table-driven test keeps its compile-time check instead of falling back to
// string literals.
const (
	// OutcomeScenario means the client received the scenario's response.
	OutcomeScenario = journal.OutcomeScenario
	// OutcomeError means the client received a provider-shaped validation or
	// authentication error.
	OutcomeError = journal.OutcomeError
	// OutcomeFault means the client received a declared fault.
	OutcomeFault = journal.OutcomeFault
	// OutcomeUnmatched means the request failed closed: unknown route or method.
	OutcomeUnmatched = journal.OutcomeUnmatched
)

// Finding severities, re-exported for the same reason.
const (
	// SeverityWarning is a finding that does not fail the request.
	SeverityWarning = journal.SeverityWarning
	// SeverityError is a finding that produced a provider-shaped error.
	SeverityError = journal.SeverityError
)

// The job alias set. A job is the create-then-poll record an async surface
// mints, and a consumer needs to name it wherever [Sim.Jobs] hands one back or
// its own provider.Deps.Jobs implementation has to satisfy the store contract.

// Job is one create-then-poll record: what a create minted, and what a later
// poll in the same namespace resolves. [Sim.Jobs] and [Namespace.Jobs] return
// these.
type Job = jobs.Job

// Jobs is the job-store contract the provider seam consumes. A consumer
// wiring provider.Deps by hand passes one, and an implementation of its own is
// nameable through this alias, on the same terms as [Journal].
//
// The two failure sentinels Create can report — a duplicate id, and a
// namespace at its bound — are not reachable from outside this module: they
// are internal/jobs.ErrDuplicate and internal/jobs.ErrLimit, and that
// package is not importable. Whatever error an own implementation's Create
// returns instead, the provider seam reports it as job.limit_reached with a
// generic message rather than matching it to a specific finding code — do
// not expect a testkit.ErrJobDuplicate to exist to compare against.
type Jobs = jobs.Store

// JobStats reports one namespace's job occupancy against its bound. It is
// part of the [Jobs] method set — Create and StatsIn both return it — so a
// consumer cannot implement Jobs without naming it.
type JobStats = jobs.Stats

// NewJobs returns an in-process job store bounded to 256 live jobs per
// namespace — jobs.DefaultMaxJobs, the same default the binary uses — wired
// the way [Start] already wires one. It exists because internal/jobs is not
// importable from another module, and without it a consumer building
// provider.Deps by hand gets a nil Deps.Jobs: a create still answers, but no
// poll can ever resolve the identifier it returned — see [provider.Deps]'s
// Jobs field.
//
//	s, _, _ := scenario.Parse(src)
//	h := exa.New(provider.Deps{Scenario: s, Faults: testkit.NewFaults(s), Jobs: testkit.NewJobs()})
func NewJobs() Jobs {
	return jobs.NewRegistry(jobs.Limits{})
}

// defaultJournalCapacity is how many entries a Sim retains before the oldest is
// dropped. It matches the binary's default so a test and a container disagree
// about nothing.
const defaultJournalCapacity = 1000

// awaitTimeout bounds AwaitRequests. It is generous enough to survive a loaded
// CI machine under -race and short enough that a genuinely missing entry fails
// the test rather than the suite's own timeout, where the cause is invisible.
const awaitTimeout = 5 * time.Second

// awaitPoll is how often AwaitRequests re-reads the journal. There is no
// completion signal to wait on — Journal is an interface a consumer may
// implement — so this is a poll, kept short enough to be invisible.
const awaitPoll = 250 * time.Microsecond

// allProviders is the listener set, in the stable order BaseURLs and Close walk.
// It is a function rather than a package-level slice so no consumer can mutate
// the order a package it merely imported serves in.
func allProviders() []provider.Name {
	return []provider.Name{provider.Exa, provider.Tavily, provider.Perplexity, provider.MCP}
}

// routes returns every route all four provider packages declare. The fault
// engine registers all of them regardless of which listeners a Sim starts, so a
// key set never depends on the subset under test: a missing key would make its
// route report fault.unknown_key instead of serving the scenario's fault.
func routes() []provider.Route {
	return slices.Concat(exa.Routes(), tavily.Routes(), perplexity.Routes(), mcp.Routes())
}

// validators returns the projection validators for every provider kind this
// module implements, keyed as provider.ValidateScenario expects.
func validators() map[string]provider.Validator {
	out := map[string]provider.Validator{
		string(provider.Exa): exa.Validator{},
		exa.NameAgentRuns:    exa.AgentRunValidator{},
		string(tavily.Name):  tavily.Validator{},
		tavily.NameResearch:  tavily.ResearchValidator{},
		string(mcp.Name):     mcp.Validator{},
	}
	maps.Copy(out, perplexity.Validators())
	return out
}

// NewFaults returns the fault engine for a scenario, wired to every route all
// four provider packages declare. It exists because internal/faults is not
// importable from another module, and without it a consumer building
// provider.Deps by hand gets silently fault-free behaviour from a scenario that
// declares faults:
//
//	s, _, _ := scenario.Parse(src)
//	h := exa.New(provider.Deps{Scenario: s, Faults: testkit.NewFaults(s)})
//
// [Start] does this for you.
//
// Deprecated: registers only the four in-tree profiles' routes, so a route
// [WithProfiles] adds is fault-blind — exactly the failure class
// [provider.Set.Faults] exists to make unconstructible. Use
// provider.NewSet(ps...).Faults(s) instead, or pass [WithProfiles] to [Start]
// and read faults off the returned [Sim]. Removed in Phase 10 unit 4.
func NewFaults(s *scenario.Scenario) provider.Faults {
	return faults.New(s, routes())
}

// NewJournal returns a fresh, unbounded-capacity in-process journal, wired the
// way [Start] already wires one. It exists because internal/journal is not
// importable from another module, and without it a consumer building
// provider.Deps by hand — provider.Deps{Scenario: s, Faults: set.Faults(s)},
// registering an out-of-tree profile through [WithProfiles] rather than
// calling [Start] — has no journal constructor to reach for at all (AUDIT 1
// gap 7): [NewJobs] already exists for the same shape and this closes the
// journal's own gap.
//
//	set := provider.MustSet(acme.Profile())
//	p, _ := set.Lookup(acme.Name)
//	h := p.Handler(provider.Deps{Scenario: s, Faults: set.Faults(s), Journal: testkit.NewJournal()})
func NewJournal() Journal {
	return journal.NewRing(defaultJournalCapacity, provider.DefaultMaxJournalBodyBytes)
}

// options is the resolved configuration a Sim is built from.
type options struct {
	// load resolves the scenario. It is a closure rather than a *scenario.Scenario
	// because an Option cannot report an error, and a file that does not exist
	// must fail the test at Start with the path named, not at option-application
	// time where there is no testing.TB to fail.
	load   func() (*scenario.Scenario, scenario.Report, error)
	source string

	providers    []provider.Name
	setProviders bool
	clock        provider.Clock
	delayMode    provider.DelayMode

	// profiles is nil unless WithProfiles was passed. Additive only in this
	// unit (Phase 10 unit 2 §5): when nil, build derives routes, validators,
	// the fault engine and handlers from the four in-tree defaults exactly as
	// it always has; when set, it derives every one of those from a
	// provider.Set built from this slice instead. The "required" half of D-5
	// — WithProfiles mandatory, the four defaults deleted — lands in unit 4.
	profiles []provider.Profile

	capacity    int
	setCapacity bool
}

// Option configures a Sim.
type Option func(*options)

// WithScenarioFile loads a scenario from disk.
func WithScenarioFile(path string) Option {
	return func(o *options) {
		o.source = path
		o.load = func() (*scenario.Scenario, scenario.Report, error) { return scenario.Load(path) }
	}
}

// WithScenarioYAML parses a scenario from a literal, which keeps a single-purpose
// fixture next to the test that needs it.
func WithScenarioYAML(src string) Option {
	return func(o *options) {
		o.source = "the inline scenario"
		o.load = func() (*scenario.Scenario, scenario.Report, error) { return scenario.Parse([]byte(src)) }
	}
}

// WithScenario uses an already-built scenario.
func WithScenario(s *scenario.Scenario) Option {
	return func(o *options) {
		o.source = "the supplied scenario"
		o.load = func() (*scenario.Scenario, scenario.Report, error) { return s, scenario.Report{}, nil }
	}
}

// WithBuiltin selects an embedded protocol scenario by name, for example "happy"
// or "rate-limited". scenarios.Names lists them.
func WithBuiltin(name string) Option {
	return func(o *options) {
		o.source = scenarios.Prefix + name
		o.load = func() (*scenario.Scenario, scenario.Report, error) { return scenarios.Load(name) }
	}
}

// WithProviders limits which provider servers start. The fault engine still
// registers every route, so restricting the listeners cannot change what a
// running one does.
func WithProviders(names ...provider.Name) Option {
	return func(o *options) {
		o.providers = slices.Clone(names)
		o.setProviders = true
	}
}

// WithProfiles registers the profile set a Sim is built from, in place of the
// four in-tree defaults. When passed, [Start] and the handler constructors
// derive routes, validators (unfiltered — every registered profile's, not
// only the enabled subset) and the fault engine from
// provider.NewSet(ps...).{Routes,Validators,Faults}, and each enabled
// listener's handler from Lookup(name).Handler(deps) — closing AUDIT 1 gap 8,
// "testkit cannot host a fifth profile at all". NewSet's own refusal fails tb
// immediately, named, rather than surfacing as an inscrutable panic three
// calls later.
//
// Additive only in this unit (Phase 10 unit 2 §5): omitting it keeps the four
// in-tree defaults exactly as before. Phase 10 unit 4 makes it required and
// deletes those defaults (owner decision D-5) — a default Sim that pulls in
// four vendors' contracts and goldens for a team simulating one API is the
// cost D-5 names.
func WithProfiles(ps ...provider.Profile) Option {
	return func(o *options) {
		o.profiles = slices.Clone(ps)
	}
}

// WithClock injects the clock that stamps journal timestamps. The default is
// provider.SystemClock{} — real time — and a caller should have a specific reason
// to change it: [AssertOverlapped] compares arrival and completion instants
// across two entries, and a clock pinned to a constant makes every entry claim
// the same instant, so the assertion becomes either always-false or meaningless.
// It cannot affect a response body; nothing in one is ever read from a clock.
func WithClock(c provider.Clock) Option {
	return func(o *options) {
		o.clock = c
	}
}

// WithSkippedDelays makes delay faults return immediately instead of waiting,
// while still recording the requested delay in Outcome.DelayMS. Use it for
// backoff tests that assert "the scenario asked for 30s" without paying 30s.
//
// Do not use it for timeout, deadline or cancellation tests. Those are observed
// by bytes not arriving, so they need a real delay: declare a short one
// (delay: 150ms) in the scenario and leave delays real. The default is real
// delays precisely so that a scenario behaves identically in-process and in the
// container.
func WithSkippedDelays() Option {
	return func(o *options) {
		o.delayMode = provider.DelaySkip
	}
}

// WithJournalCapacity bounds journal retention. Zero retains nothing while still
// allocating sequence numbers, so ordering assertions keep working with
// retention switched off.
func WithJournalCapacity(n int) Option {
	return func(o *options) {
		o.capacity = n
		o.setCapacity = true
	}
}

// Sim is a running set of provider servers.
type Sim struct {
	scenario *scenario.Scenario
	journal  *journal.Ring
	faults   provider.Faults
	jobs     *jobs.Registry

	// names is the enabled provider set in stable order. handlers and servers are
	// written once during construction and only read afterwards.
	names    []provider.Name
	handlers map[provider.Name]http.Handler
	servers  map[provider.Name]*httptest.Server

	// derived maps a namespace name [Sim.NamespaceFor] produced to the test name
	// it was produced from, so two tests whose names sanitise to one namespace
	// fail loudly instead of silently sharing a lane. It is a sync.Map because
	// the whole point of the shared-container pattern is that parallel subtests
	// claim namespaces concurrently.
	derived sync.Map // string -> string

	client    *http.Client
	closeOnce sync.Once
}

// Start brings up one httptest.Server per enabled provider and registers
// cleanup on tb, so a caller needs no defer. It uses httptest.NewServer rather
// than httptest.NewRecorder because only a real server's ResponseWriter
// implements http.Hijacker and http.Flusher, which the transport faults require.
//
// The defaults are the production ones, deliberately: a real clock, real delays,
// and a fault engine wired from the scenario over every declared route. A test
// that opts out of one of those ([WithSkippedDelays], [WithClock]) is opting out
// of fidelity, and each option says what it costs.
//
// The scenario is validated exactly as the binary validates it — envelope first,
// then every provider's projection bodies — and a scenario with an error finding
// fails tb here rather than serving a subtly wrong contract.
func Start(tb testing.TB, opts ...Option) *Sim {
	tb.Helper()

	s := build(tb, nil, opts)
	s.servers = make(map[provider.Name]*httptest.Server, len(s.names))
	for _, name := range s.names {
		s.servers[name] = httptest.NewServer(s.handlers[name])
	}
	return s
}

// ExaHandler returns the Exa handler for a consumer wiring one provider into its
// own httptest.Server, together with the Sim that owns its journal and fault
// engine. The Sim is returned rather than discarded because every assertion in
// this package needs an Entry, and the only source of an Entry is the Sim: a
// bare http.Handler lets a consumer assert "I got a response" and nothing about
// the vendor request it sent, which is the whole point of the journal.
//
// The Sim's provider servers are not started; only the handler is built. Close is
// still registered on tb.
func ExaHandler(tb testing.TB, opts ...Option) (http.Handler, *Sim) {
	tb.Helper()
	return handlerOnly(tb, provider.Exa, opts)
}

// TavilyHandler returns the Tavily handler and its Sim, on the same terms as
// [ExaHandler].
func TavilyHandler(tb testing.TB, opts ...Option) (http.Handler, *Sim) {
	tb.Helper()
	return handlerOnly(tb, provider.Tavily, opts)
}

// PerplexityHandler returns the Perplexity handler and its Sim, on the same terms
// as [ExaHandler]. The one handler serves both the Sonar and Agent surfaces,
// which are separate scenario entries on one listener.
func PerplexityHandler(tb testing.TB, opts ...Option) (http.Handler, *Sim) {
	tb.Helper()
	return handlerOnly(tb, provider.Perplexity, opts)
}

// MCPHandler returns the MCP handler and its Sim, on the same terms as
// [ExaHandler].
func MCPHandler(tb testing.TB, opts ...Option) (http.Handler, *Sim) {
	tb.Helper()
	return handlerOnly(tb, provider.MCP, opts)
}

// handlerOnly builds a Sim serving exactly one provider and starts nothing.
//
// The provider set is forced rather than taken from WithProviders: a caller that
// asked for ExaHandler wants the Exa handler, and honouring a WithProviders that
// excluded it would return nil for no reason a reader could see.
func handlerOnly(tb testing.TB, p provider.Name, opts []Option) (http.Handler, *Sim) {
	tb.Helper()
	s := build(tb, []provider.Name{p}, opts)
	return s.handlers[p], s
}

// build resolves the options, validates the scenario and constructs every
// enabled provider's handler over one shared journal and one shared fault
// engine. It starts nothing. force, when non-nil, overrides the enabled provider
// set.
func build(tb testing.TB, force []provider.Name, opts []Option) *Sim {
	tb.Helper()

	o := options{
		load:      func() (*scenario.Scenario, scenario.Report, error) { return scenarios.Load(scenarios.Default) },
		source:    scenarios.Prefix + scenarios.Default,
		clock:     provider.SystemClock{},
		delayMode: provider.DelayReal,
		capacity:  defaultJournalCapacity,
	}
	for _, opt := range opts {
		opt(&o)
	}
	if force != nil {
		o.providers = force
		o.setProviders = true
	}
	if !o.setCapacity {
		o.capacity = defaultJournalCapacity
	}

	// set is nil unless WithProfiles was passed — see profileSet's doc
	// comment for what changes below when it is not.
	set := profileSet(tb, o.profiles)

	// requested defaults to every name the active registry (set, or the four
	// in-tree defaults) serves, so a caller that passes WithProfiles alone —
	// no WithProviders — gets every profile it registered started, rather
	// than being filtered against the four hardcoded names WithProviders'
	// own default used to mean.
	available := namesFor(set)
	requested := available
	if o.setProviders {
		requested = o.providers
	}

	sc := loadScenario(tb, o, validatorsFor(set))
	s := &Sim{
		scenario: sc,
		journal:  journal.NewRing(o.capacity, provider.DefaultMaxJournalBodyBytes),
		faults:   faultsFor(set, sc),
		jobs:     jobs.NewRegistry(jobs.Limits{}),
		names:    enabled(tb, requested, available),
		handlers: map[provider.Name]http.Handler{},
		client:   newClient(),
	}

	deps := provider.Deps{
		Scenario:  sc,
		Journal:   s.journal,
		Faults:    s.faults,
		Clock:     o.clock,
		DelayMode: o.delayMode,
		Logger:    slog.New(slog.DiscardHandler),
		Jobs:      s.jobs,
	}
	for _, name := range s.names {
		if set != nil {
			// enabled already proved name is in set.Names(), so the miss
			// branch Lookup offers is unreachable here.
			p, _ := set.Lookup(name)
			s.handlers[name] = p.Handler(deps)
			continue
		}
		switch name {
		case provider.Exa:
			s.handlers[name] = exa.New(deps)
		case provider.Tavily:
			s.handlers[name] = tavily.New(deps)
		case provider.Perplexity:
			s.handlers[name] = perplexity.New(deps)
		case provider.MCP:
			s.handlers[name] = mcp.New(deps)
		}
	}

	tb.Cleanup(s.Close)
	return s
}

// profileSet resolves ps into a validated provider.Set, failing tb — named,
// immediately — on a refusal, or returns nil when ps is empty (WithProfiles
// was not passed), which is every build call site's signal to fall back to
// the four in-tree defaults.
func profileSet(tb testing.TB, ps []provider.Profile) *provider.Set {
	tb.Helper()
	if len(ps) == 0 {
		return nil
	}
	set, err := provider.NewSet(ps...)
	if err != nil {
		tb.Fatalf("testkit: WithProfiles: %v", err)
		return nil
	}
	return set
}

// namesFor returns the provider names a Sim may be built from: set.Names()
// when a Set was given, the four in-tree defaults otherwise.
func namesFor(set *provider.Set) []provider.Name {
	if set == nil {
		return allProviders()
	}
	return set.Names()
}

// validatorsFor returns the projection validators a scenario is checked
// against: set.Validators() — unfiltered, every registered profile's, not
// only the providers this particular Sim enables, matching what
// provider.ValidateScenario already does for the four in-tree defaults below
// — or the package-level default map.
func validatorsFor(set *provider.Set) map[string]provider.Validator {
	if set == nil {
		return validators()
	}
	return set.Validators()
}

// faultsFor builds the fault engine sc's scripted attempts are drawn from:
// registered against every route in set when one was given (so a route
// WithProfiles added is never fault-blind), or the four in-tree defaults'
// routes otherwise.
func faultsFor(set *provider.Set, sc *scenario.Scenario) provider.Faults {
	if set == nil {
		return NewFaults(sc)
	}
	return set.Faults(sc)
}

// loadScenario resolves and validates the configured scenario, failing tb with
// every finding rather than only the first. Projection bodies are checked here
// for the same reason internal/server checks them before readiness: the scenario
// package leaves a respond body as a raw YAML node, so nothing before this point
// has looked inside it.
func loadScenario(tb testing.TB, o options, vals map[string]provider.Validator) *scenario.Scenario {
	tb.Helper()

	sc, report, err := o.load()
	if err != nil {
		tb.Fatalf("testkit: loading %s: %v%s", o.source, err, formatFindings(report.Findings))
		return nil
	}
	if sc == nil {
		tb.Fatalf("testkit: loading %s: the scenario is nil", o.source)
		return nil
	}

	report.Findings = append(report.Findings, provider.ValidateScenario(sc, vals)...)
	if !report.OK() {
		tb.Fatalf("testkit: validating %s:%s", o.source, formatFindings(report.Errors()))
		return nil
	}
	return sc
}

// formatFindings renders findings one per line, in the order they were recorded,
// for a failure message.
func formatFindings(findings []scenario.Finding) string {
	var b strings.Builder
	for _, f := range findings {
		b.WriteString(fmt.Sprintf("\n  %s %s %s: %s", f.Severity, f.Code, f.Path, f.Message))
	}
	return b.String()
}

// enabled returns the requested provider set in available's stable order,
// failing tb on a name available does not list. Duplicates collapse, so
// WithProviders("exa", "exa") starts one server rather than two sharing a port.
func enabled(tb testing.TB, requested, available []provider.Name) []provider.Name {
	tb.Helper()

	want := make(map[provider.Name]bool, len(requested))
	for _, name := range requested {
		if !slices.Contains(available, name) {
			tb.Fatalf("testkit: unknown provider %q; this build serves %v", name, available)
			return nil
		}
		want[name] = true
	}

	var out []provider.Name
	for _, name := range available {
		if want[name] {
			out = append(out, name)
		}
	}
	return out
}

// URL returns the base URL for a provider, suitable for an adapter's base-URL
// configuration. It is empty for a provider this Sim does not serve, and for
// every provider of a Sim returned by [ExaHandler] and its siblings, which start
// no servers.
func (s *Sim) URL(p provider.Name) string {
	if srv, ok := s.servers[p]; ok {
		return srv.URL
	}
	return ""
}

// BaseURLs returns the environment-variable-shaped base URLs, keyed
// EXA_BASE_URL, TAVILY_BASE_URL, PERPLEXITY_BASE_URL and MCP_BASE_URL, so a
// test can configure
// a consumer exactly as Compose would. Only running servers appear.
func (s *Sim) BaseURLs() map[string]string {
	out := make(map[string]string, len(s.servers))
	for _, name := range s.names {
		if url := s.URL(name); url != "" {
			out[strings.ToUpper(string(name))+"_BASE_URL"] = url
		}
	}
	return out
}

// Handler returns a provider's handler for a caller wiring its own server, or nil
// for a provider this Sim does not serve.
func (s *Sim) Handler(p provider.Name) http.Handler {
	return s.handlers[p]
}

// Client returns an http.Client with keep-alives disabled, so a connection-abort
// fault is observed rather than absorbed by a pooled connection. The same client
// is returned on every call, and it never consults the proxy environment: a
// simulator that could be pointed at a real host by an ambient variable would
// defeat its own purpose.
func (s *Sim) Client() *http.Client {
	return s.client
}

// Journal returns every recorded entry, in completion order. Entries from every
// namespace are included and interleave exactly as they were recorded;
// [Sim.Namespace] returns a view scoped to one of them.
func (s *Sim) Journal() []Entry {
	return s.journal.Snapshot()
}

// Requests returns the entries for one provider, in arrival-sequence order.
//
// After a request that ended at the transport level, call [Sim.AwaitRequests]
// instead: the client returns while the server goroutine is still completing the
// entry, and reading here is a race.
func (s *Sim) Requests(p provider.Name) []Entry {
	return requestsOf(s.journal.Snapshot(), p)
}

// Jobs returns every live job record across every namespace, in the same
// declared order GET /__admin/jobs uses: namespace, then entry, then create
// index, then id.
func (s *Sim) Jobs() []Job {
	return s.jobs.List()
}

// requestsOf filters entries to one provider and orders them by arrival
// sequence. The sort is stable, so entries a journal handed back in completion
// order keep that order among themselves if two ever shared a sequence number.
func requestsOf(entries []Entry, p provider.Name) []Entry {
	var out []Entry
	for _, e := range entries {
		if e.Provider == string(p) {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b Entry) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

// AwaitRequests blocks until p has recorded n entries, or fails tb after a short
// deadline. It is mandatory before asserting on the journal following a request
// that ended at the transport level — an aborting fault the client saw as
// "connection reset by peer", or a request the client cancelled or timed out.
// In those cases the client returns while the server goroutine is still
// completing the entry, and a bare [Sim.Requests] call is a race that passes on a
// slow machine and fails on a fast one.
func (s *Sim) AwaitRequests(tb testing.TB, p provider.Name, n int) []Entry {
	tb.Helper()
	return await(tb, p, n, "", s.Requests)
}

// await polls read until it reports at least n entries, or fails tb. scope names
// the namespace in the failure message and is empty for a whole-Sim wait, so the
// message says which lane came up short instead of leaving the reader to guess.
func await(tb testing.TB, p provider.Name, n int, scope string, read func(provider.Name) []Entry) []Entry {
	tb.Helper()

	in := ""
	if scope != "" {
		in = " in namespace " + scope
	}

	deadline := time.Now().Add(awaitTimeout)
	for {
		got := read(p)
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			tb.Fatalf("testkit: %s recorded %d requests%s, want %d, after waiting %s",
				p, len(got), in, n, awaitTimeout)
			return got
		}
		time.Sleep(awaitPoll)
	}
}

// Scenario returns the loaded, validated, resolved scenario the servers serve.
func (s *Sim) Scenario() *scenario.Scenario {
	return s.scenario
}

// Reset clears the journal and every fault counter, which also rewinds the turn
// cursor: attempt counting and turn selection share one counter so they cannot
// disagree about which call this is.
//
// It is a convenience for a test that reuses one Sim across phases, not a
// concurrency mechanism. A reset racing an in-flight request renumbers it.
func (s *Sim) Reset() {
	s.journal.Reset()
	s.faults.Reset()

	// Jobs drop with the cursors, in the same call. Resetting the fault counters
	// rewinds the turn cursor, and a job identifier is derived from the call
	// index — so cursors reset without records dropped means the next create
	// re-mints an identifier that is still live and fails with a collision.
	//
	// That path needs no concurrency at all: this method is documented for a test
	// that reuses one Sim across phases, which is exactly when it happens. Every
	// surface that resets fault counters resets jobs alongside them.
	s.jobs.Reset()
}

// Close stops every server. [Start] and the handler constructors already register
// it with tb.Cleanup, so an explicit call is only for a test that wants the
// listeners gone early; a second call does nothing.
func (s *Sim) Close() {
	s.closeOnce.Do(func() {
		for _, name := range slices.Backward(s.names) {
			if srv, ok := s.servers[name]; ok {
				srv.Close()
			}
		}
		s.client.CloseIdleConnections()
	})
}

// Namespace returns a view of s scoped to name: base URLs that already carry the
// /n/<name> prefix, and a journal already filtered to that namespace. It fails
// tb when name is not a usable namespace, which is where the mistake was made —
// a name the seam rejects would otherwise surface as a provider-shaped error on
// the first request, several frames away from the typo.
//
// The namespace is created by the first request that names it; this call
// allocates nothing on the server. Two handles to one name are two views of one
// lane, which is what lets a helper take a name and a test take a handle.
//
// [Sim.NamespaceFor] is the one-liner for the common case; use this when the
// namespace name has to be a value the test chose, for example one shared with a
// subprocess through an environment variable.
func (s *Sim) Namespace(tb testing.TB, name string) *Namespace {
	tb.Helper()

	if !provider.ValidNamespace(name) {
		tb.Fatalf("testkit: %q cannot be a namespace: 1 to %d characters of [A-Za-z0-9_-]. "+
			"Sim.NamespaceFor(t) derives a valid one from the test's own name.",
			name, provider.MaxNamespaceNameLen)
		return nil
	}
	return &Namespace{sim: s, name: name}
}

// NamespaceFor returns a view of s scoped to a namespace derived from tb.Name(),
// which is the whole shared-container pattern in one line:
//
//	ns := sim.NamespaceFor(t)
//
// A test name is not a legal namespace — t.Name() carries '/' for a subtest and
// '#' for a duplicate — so every byte outside [A-Za-z0-9_-] becomes '-'. A name
// longer than the seam's bound keeps its last MaxNamespaceNameLen bytes, because
// sibling subtests differ at the end and truncating the front would collide them.
//
// Two tests whose names derive one namespace fail tb rather than sharing a lane.
// Silently merging two tests' cursors is precisely the failure namespaces exist
// to prevent, and it would present as one test receiving the other's scripted
// turn — the failure that is hardest to trace back to here. Calling it twice for
// one test is fine and returns an equivalent handle.
func (s *Sim) NamespaceFor(tb testing.TB) *Namespace {
	tb.Helper()

	test := tb.Name()
	name := namespaceForTest(test)
	if !provider.ValidNamespace(name) {
		tb.Fatalf("testkit: the test name %q yields no usable namespace; "+
			"pass one explicitly with Sim.Namespace", test)
		return nil
	}
	if prior, loaded := s.derived.LoadOrStore(name, test); loaded && prior.(string) != test {
		tb.Fatalf("testkit: tests %q and %q both derive the namespace %q, so they would share "+
			"one set of cursors; name one of them explicitly with Sim.Namespace",
			prior.(string), test, name)
		return nil
	}
	return &Namespace{sim: s, name: name}
}

// namespaceForTest maps a test name onto the namespace grammar the seam accepts:
// every byte outside [A-Za-z0-9_-] becomes '-', and an over-long name keeps its
// tail. It is a pure function of the name, so a test that reruns addresses the
// same namespace.
func namespaceForTest(test string) string {
	out := []byte(test)
	for i, c := range out {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_':
		default:
			out[i] = '-'
		}
	}
	if len(out) > provider.MaxNamespaceNameLen {
		out = out[len(out)-provider.MaxNamespaceNameLen:]
	}
	return string(out)
}

// Namespace is a state-isolated view of a [Sim]: one simulator, many concurrent
// tests. It is what makes the shared-container pattern every sibling repository
// already uses safe, rather than what makes it the recommended one — process
// isolation is still the default.
//
// A namespace isolates state, not behaviour. Fault attempt counters, turn
// cursors and journal entries are per namespace; the scenario, the routes and
// the listeners are shared. So two tests can run the same scenario concurrently
// and each see attempt 0 first, which is the property that lets a retry test and
// a fan-out test share one Sim:
//
//	sim := testkit.Start(t, testkit.WithBuiltin("rate-limited"))
//	t.Run("adapter retries", func(t *testing.T) {
//	    t.Parallel()
//	    ns := sim.NamespaceFor(t)
//	    adapter := myrepo.New(ns.URL(provider.Exa), "test-key")
//	    ...
//	    testkit.AssertNoErrors(t, ns.Requests(provider.Exa)[0])
//	})
//
// There is deliberately no per-namespace Reset. The journal can be dropped per
// namespace but the fault and turn cursors cannot, and a journal that restarted
// at 1 while the cursors kept counting is exactly the "two counters that
// disagree" state this feature exists to prevent. A test that wants fresh state
// names a fresh namespace, which costs nothing because namespaces are created on
// first use.
type Namespace struct {
	sim  *Sim
	name string
}

// Name returns the namespace this view is scoped to.
func (ns *Namespace) Name() string {
	return ns.name
}

// URL returns the provider's base URL with this namespace's /n/ prefix already
// appended, so an adapter configured with it needs no code change to be
// isolated. It is empty for a provider this Sim does not serve.
//
// The path prefix is the documented mechanism because a base URL is something
// every SDK accepts. For an SDK that pins the path, send the namespace in the
// provider.NamespaceHeader header instead; [Namespace.Name] is the value.
func (ns *Namespace) URL(p provider.Name) string {
	base := ns.sim.URL(p)
	if base == "" {
		return ""
	}
	return base + "/n/" + ns.name
}

// BaseURLs returns the environment-variable-shaped base URLs, each carrying this
// namespace's prefix, so a test configures a consumer exactly as Compose would
// and gets isolation from the same line.
func (ns *Namespace) BaseURLs() map[string]string {
	out := make(map[string]string, len(ns.sim.names))
	for _, name := range ns.sim.names {
		if url := ns.URL(name); url != "" {
			out[strings.ToUpper(string(name))+"_BASE_URL"] = url
		}
	}
	return out
}

// Client returns the Sim's client, which is shared: a client holds no namespace
// state, and keep-alives are already disabled so an abort fault is observed
// rather than absorbed.
func (ns *Namespace) Client() *http.Client {
	return ns.sim.Client()
}

// Journal returns this namespace's entries, in completion order. Entries from
// every other namespace are already filtered out, so a parallel subtest asserts
// on its own traffic without knowing what else the process is serving.
func (ns *Namespace) Journal() []Entry {
	return ns.sim.journal.SnapshotIn(ns.name)
}

// Requests returns this namespace's entries for one provider, in
// arrival-sequence order.
//
// After a request that ended at the transport level, call
// [Namespace.AwaitRequests] instead, for the reason [Sim.Requests] gives.
func (ns *Namespace) Requests(p provider.Name) []Entry {
	return requestsOf(ns.Journal(), p)
}

// AwaitRequests blocks until p has recorded n entries in this namespace, or
// fails tb. It is [Sim.AwaitRequests] scoped to the namespace, and mandatory
// before asserting on a request that ended at the transport level.
func (ns *Namespace) AwaitRequests(tb testing.TB, p provider.Name, n int) []Entry {
	tb.Helper()
	return await(tb, p, n, ns.name, ns.Requests)
}

// Jobs returns this namespace's live job records, filtered from [Sim.Jobs] and
// so in the same declared order. It is never nil, matching [Sim.Jobs]: an
// empty namespace reports an empty slice, not nil, so a consumer comparing
// against []testkit.Job{} or JSON-encoding the result sees the same shape
// either view returns.
func (ns *Namespace) Jobs() []Job {
	out := make([]Job, 0)
	for _, j := range ns.sim.jobs.List() {
		if j.Namespace == ns.name {
			out = append(out, j)
		}
	}
	return out
}

// dropped reports how many entries this namespace's journal evicted. An
// assertion that reasons about gaps in a sequence has to know, because eviction
// makes a gap that means nothing.
func (ns *Namespace) dropped() uint64 {
	return ns.sim.journal.StatsIn(ns.name).Dropped
}

// newClient builds the Sim's client. Transport.Proxy is left nil deliberately:
// http.DefaultTransport reads HTTP_PROXY from the environment, and a simulator
// whose own client could be diverted to a real host by an ambient variable is
// exactly the failure the no-live-hosts guard exists to prevent.
func newClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
}
