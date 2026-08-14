package testkit

import (
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
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/provider/exa"
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
	return []provider.Name{provider.Exa, provider.Tavily, provider.Perplexity}
}

// routes returns every route all three provider packages declare. The fault
// engine registers all of them regardless of which listeners a Sim starts, so a
// key set never depends on the subset under test: a missing key would make its
// route report fault.unknown_key instead of serving the scenario's fault.
func routes() []provider.Route {
	return slices.Concat(exa.Routes(), tavily.Routes(), perplexity.Routes())
}

// validators returns the projection validators for every provider kind this
// module implements, keyed as provider.ValidateScenario expects.
func validators() map[string]provider.Validator {
	out := map[string]provider.Validator{
		string(provider.Exa): exa.Validator{},
		tavily.Name:          tavily.Validator{},
	}
	maps.Copy(out, perplexity.Validators())
	return out
}

// NewFaults returns the fault engine for a scenario, wired to every route all
// three provider packages declare. It exists because internal/faults is not
// importable from another module, and without it a consumer building
// provider.Deps by hand gets silently fault-free behaviour from a scenario that
// declares faults:
//
//	s, _, _ := scenario.Parse(src)
//	h := exa.New(provider.Deps{Scenario: s, Faults: testkit.NewFaults(s)})
//
// [Start] does this for you.
func NewFaults(s *scenario.Scenario) provider.Faults {
	return faults.New(s, routes())
}

// options is the resolved configuration a Sim is built from.
type options struct {
	// load resolves the scenario. It is a closure rather than a *scenario.Scenario
	// because an Option cannot report an error, and a file that does not exist
	// must fail the test at Start with the path named, not at option-application
	// time where there is no testing.TB to fail.
	load   func() (*scenario.Scenario, scenario.Report, error)
	source string

	providers []provider.Name
	clock     provider.Clock
	delayMode provider.DelayMode

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

	// names is the enabled provider set in stable order. handlers and servers are
	// written once during construction and only read afterwards.
	names    []provider.Name
	handlers map[provider.Name]http.Handler
	servers  map[provider.Name]*httptest.Server

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
		providers: allProviders(),
		clock:     provider.SystemClock{},
		delayMode: provider.DelayReal,
		capacity:  defaultJournalCapacity,
	}
	for _, opt := range opts {
		opt(&o)
	}
	if force != nil {
		o.providers = force
	}
	if !o.setCapacity {
		o.capacity = defaultJournalCapacity
	}

	sc := loadScenario(tb, o)
	s := &Sim{
		scenario: sc,
		journal:  journal.NewRing(o.capacity, provider.DefaultMaxJournalBodyBytes),
		faults:   NewFaults(sc),
		names:    enabled(tb, o.providers),
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
	}
	for _, name := range s.names {
		switch name {
		case provider.Exa:
			s.handlers[name] = exa.New(deps)
		case provider.Tavily:
			s.handlers[name] = tavily.New(deps)
		case provider.Perplexity:
			s.handlers[name] = perplexity.New(deps)
		}
	}

	tb.Cleanup(s.Close)
	return s
}

// loadScenario resolves and validates the configured scenario, failing tb with
// every finding rather than only the first. Projection bodies are checked here
// for the same reason internal/server checks them before readiness: the scenario
// package leaves a respond body as a raw YAML node, so nothing before this point
// has looked inside it.
func loadScenario(tb testing.TB, o options) *scenario.Scenario {
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

	report.Findings = append(report.Findings, provider.ValidateScenario(sc, validators())...)
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

// enabled returns the requested provider set in the stable listener order,
// failing tb on a name no listener serves. Duplicates collapse, so
// WithProviders("exa", "exa") starts one server rather than two sharing a port.
func enabled(tb testing.TB, requested []provider.Name) []provider.Name {
	tb.Helper()

	want := make(map[provider.Name]bool, len(requested))
	for _, name := range requested {
		if !slices.Contains(allProviders(), name) {
			tb.Fatalf("testkit: unknown provider %q; this build serves %v", name, allProviders())
			return nil
		}
		want[name] = true
	}

	var out []provider.Name
	for _, name := range allProviders() {
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
// EXA_BASE_URL, TAVILY_BASE_URL and PERPLEXITY_BASE_URL, so a test can configure
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

// Journal returns every recorded entry, in completion order.
func (s *Sim) Journal() []Entry {
	return s.journal.Snapshot()
}

// Requests returns the entries for one provider, in arrival-sequence order.
//
// After a request that ended at the transport level, call [Sim.AwaitRequests]
// instead: the client returns while the server goroutine is still completing the
// entry, and reading here is a race.
func (s *Sim) Requests(p provider.Name) []Entry {
	var out []Entry
	for _, e := range s.journal.Snapshot() {
		if e.Provider == string(p) {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b Entry) int {
		switch {
		case a.Seq < b.Seq:
			return -1
		case a.Seq > b.Seq:
			return 1
		default:
			return 0
		}
	})
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

	deadline := time.Now().Add(awaitTimeout)
	for {
		got := s.Requests(p)
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			tb.Fatalf("testkit: %s recorded %d requests, want %d, after waiting %s",
				p, len(got), n, awaitTimeout)
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

// newClient builds the Sim's client. Transport.Proxy is left nil deliberately:
// http.DefaultTransport reads HTTP_PROXY from the environment, and a simulator
// whose own client could be diverted to a real host by an ambient variable is
// exactly the failure the no-live-hosts guard exists to prevent.
func newClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
}
