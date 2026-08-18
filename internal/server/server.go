package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/c360studio/servicesim/internal/admin"
	"github.com/c360studio/servicesim/internal/config"
	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/scenarios"
)

// DefaultVersion is what GET /healthz reports when no build version was
// injected. The binary overrides it with [WithVersion] from its ldflags value.
const DefaultVersion = "dev"

// ErrAlreadyRun reports that [Server.Run] was called on a Server that has
// already been run. A Server owns its listeners for its whole lifetime and does
// not rebind them, because a second Run would race the first one's shutdown over
// the same http.Server values.
var ErrAlreadyRun = errors.New("server: Run was already called on this Server")

// Option adjusts a Server at construction.
//
// It is variadic on [New] so that the signature in docs/design/package-design.md
// §2.9 still compiles unchanged. The build version is the only thing that needs
// it today: cmd/servicesim receives its version through ldflags, which
// config.Config cannot carry because it is resolved from flags and environment.
type Option func(*options)

type options struct {
	version string
}

// WithVersion sets the build version reported by GET /healthz and GET /readyz.
func WithVersion(v string) Option {
	return func(o *options) {
		if v != "" {
			o.version = v
		}
	}
}

// loadedScenario is one validated scenario and everything derived from it.
//
// The fault engine is per scenario because its plans come from that scenario;
// the journal is not, and is deliberately absent here. A namespace is a state
// boundary and a scenario is a behaviour boundary (extended-surfaces, "What a
// namespace isolates"), so journal entries from two scenarios served in one
// namespace belong in one lane and two tests sharing a scenario still get lanes
// of their own.
type loadedScenario struct {
	// name is the /x/<scenario> selector. In directory mode it is the file's
	// base name, which is what config.ScenarioEntry documents; with --scenario it
	// is the scenario's own declared name.
	name string

	// origin is where the scenario was read from, for logs and startup errors.
	// It is a file name or a "builtin:" reference, never a host path.
	origin string

	scenario *scenario.Scenario
	report   scenario.Report

	// faults is this scenario's fault engine, built by [provider.Set.Faults]
	// — the ONLY exported fault-engine constructor, so an engine that does
	// not know an out-of-tree profile's route is unconstructible. Held as
	// the interface, not a concrete type: internal/server names no profile
	// package and no longer names internal/faults either (Phase 10 unit 3
	// deleted it, once this was its last caller alongside testkit.NewFaults).
	faults provider.Faults

	// deps is what every handler serving this scenario is constructed with.
	deps provider.Deps
}

// Server is a running Servicesim instance.
type Server struct {
	cfg     config.Config
	logger  *slog.Logger
	version string

	// scenarios is the registry, in name order, and byName indexes it for the
	// /x/<scenario> prefix. Both are fixed by New and never mutated afterwards:
	// scenarios are loaded and validated at startup, and nothing pushes new
	// behaviour into a running process (CLAUDE.md house rule 6).
	scenarios []*loadedScenario
	byName    map[string]*loadedScenario

	// def serves a request that names no scenario. It is nil only for a scenario
	// directory holding several scenarios and no default among them, where an
	// unprefixed request is genuinely ambiguous and is failed closed instead.
	def *loadedScenario

	journal *journal.Ring

	// jobs holds the async surfaces' create-then-poll records. It is process-wide
	// rather than per scenario, for the same reason the journal is: a job
	// identifier is minted by one request and presented by another, and the
	// namespace — not the scenario — is what isolates concurrent tests.
	jobs  *jobs.Registry
	ready *atomic.Bool

	// surfaces is admin first, then the enabled providers in config order. It is
	// fixed by New and never mutated afterwards.
	surfaces []*surface

	// mu guards the per-surface bound addresses, which Run writes and Addr reads
	// from another goroutine.
	mu sync.Mutex

	// started is closed once every listener is accepting and readiness has
	// flipped. It is the synchronisation point a caller that asked for ephemeral
	// ports needs: before it closes, Addr has nothing to report.
	started chan struct{}

	// running guards against a second Run.
	running atomic.Bool

	// connState, when set before Run, observes every listener connection state
	// change. It is unexported and exists so a test can synchronise on "the
	// server has begun reading a request" without sleeping; nothing in the
	// binary sets it.
	connState func(surface string, c net.Conn, state http.ConnState)
}

// New builds a Server from configuration. It loads and validates every scenario
// before binding anything: a scenario with error findings fails the process
// rather than serving a subtly wrong contract.
//
// The order is acceptance criterion 4 and is not negotiable. Every configured
// scenario is read, scenario.Parse validates its envelope, and
// provider.ValidateScenario then asks each registered provider to decode and
// validate the projection bodies the scenario package deliberately left as raw
// YAML nodes. Only a scenario that survives both is wired into a handler, and
// only [Server.Run] flips readiness afterwards. A single invalid scenario in a
// --scenario-dir fails startup: a container that comes up healthy while silently
// serving a broken scenario is worse than one that refuses to start.
//
// Every scenario is loaded even after one has failed, and the failures are
// reported together. The operator fixing a mounted directory needs to see all of
// its problems, not one per restart.
//
// It is also where the fault engine is constructed — one per scenario, because
// a plan belongs to the scenario that declares it — through the ONE seam that
// can build one at all:
//
//	engine := cfg.Set.Faults(s, provider.WithMaxNamespaces(cfg.MaxNamespaces), provider.WithFaultLogger(logger))
//
// [provider.Set.Faults] is the only exported fault-engine constructor for
// exactly this reason: an engine built from anything other than the whole
// registered Set could be missing an out-of-tree profile's route, and would
// then silently serve a clean 200 where the scenario scripted a 429. Deriving
// it from cfg.Set — rather than profiles/exa.Routes() and its three
// siblings, concatenated by hand — is also what lets this package import no
// profile package at all (Phase 10 unit 3): cfg.Set is the ONLY thing
// internal/server knows about which providers this build serves.
//
// A scenario's engine is wired into every handler serving that scenario, and the
// admin surface receives a fan-out over all of them, so POST /__admin/reset
// zeroes the counters the handlers actually consult. testkit does the identical
// wiring at level 7 for the single-scenario case.
//
// Every route in cfg.Set is registered with each engine, including routes
// whose listener is disabled, so that the key set does not depend on which
// subset of the listeners this process runs. A key that were missing would
// make its route report fault.unknown_key on every request instead of
// serving the scenario's declared fault.
//
// A nil logger discards, which keeps New usable from a test that only cares
// about the returned error.
func New(cfg config.Config, logger *slog.Logger, opts ...Option) (*Server, error) {
	o := options{version: DefaultVersion}
	for _, opt := range opts {
		opt(&o)
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	sources, err := scenarioSources(cfg)
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:     cfg,
		logger:  logger,
		version: o.version,
		byName:  make(map[string]*loadedScenario, len(sources)),
		journal: journal.NewRingWithLimits(journal.Limits{
			Capacity:        cfg.JournalCapacity,
			MaxBodyBytes:    cfg.MaxJournalBodyBytes,
			MaxNamespaces:   cfg.MaxNamespaces,
			CredentialNames: cfg.Set.CredentialNames(),
		}),
		jobs:    jobs.NewRegistry(jobs.Limits{MaxJobs: cfg.MaxJobs}),
		ready:   new(atomic.Bool),
		started: make(chan struct{}),
	}
	if err := s.loadRegistry(sources); err != nil {
		return nil, err
	}

	s.surfaces = s.newSurfaces(admin.Deps{
		Journal:  s.journal,
		Faults:   newFaultsFanout(s.scenarios),
		Jobs:     s.jobs,
		Scenario: s.Scenario(),
		Report:   s.Report(),
		Ready:    s.ready,
		Version:  o.version,
		Logger:   logger,
	})
	return s, nil
}

// loadRegistry reads, validates and wires every configured scenario.
//
// Validation runs per scenario in the fixed order acceptance criterion 4 states,
// and every scenario is validated even once one has failed, so a startup failure
// names every broken scenario rather than the first one the directory listing
// happened to reach.
func (s *Server) loadRegistry(sources []scenarioSource) error {
	handlers := s.cfg.Set.Validators(s.cfg.Enabled()...)

	var failures []error
	for _, src := range sources {
		sc, report, err := src.load()
		if err != nil {
			logFindings(s.logger, report, src.name, src.origin)
			failures = append(failures, fmt.Errorf("loading scenario %s: %w", src.origin, err))
			continue
		}

		// Projection bodies are validated here, after the envelope and before
		// anything binds. Only the enabled providers are registered, so a scenario
		// entry this process is not serving is reported as unimplemented rather
		// than silently validated and then ignored.
		report.Findings = append(report.Findings, provider.ValidateScenario(sc, handlers)...)

		name := src.name
		if name == "" {
			name = sc.Name
		}

		// The opposite direction from provider.ValidateScenario above: a
		// registered, enabled profile whose scenario entry kind(s) declare no
		// block gets a warning too, because "no block" and "no handler" are
		// symmetric authoring mistakes — a request to it renders the
		// well-shaped empty answer forever, which is silent in the same way
		// an unrecognised entry kind used to be.
		report.Findings = append(report.Findings, unscriptedProfileFindings(s.cfg, sc, name)...)

		logFindings(s.logger, report, name, src.origin)
		if !report.OK() {
			failures = append(failures, fmt.Errorf("validating scenario %s: %w", src.origin, report.Err()))
			continue
		}
		if _, clash := s.byName[name]; clash {
			failures = append(failures, fmt.Errorf(
				"scenario %s: two scenarios are named %q, so /x/%s would be ambiguous", src.origin, name, name))
			continue
		}

		relaxAuth(s.cfg, sc)
		s.add(&loadedScenario{
			name:     name,
			origin:   src.origin,
			scenario: sc,
			report:   report,
			faults: s.cfg.Set.Faults(sc,
				provider.WithMaxNamespaces(s.cfg.MaxNamespaces),
				provider.WithFaultLogger(s.logger)),
		}, src.isDefault)
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return nil
}

// codeProfileUnscripted is the finding recorded when a registered, enabled
// profile's scenario entry kind(s) have no block in the loaded scenario: a
// request to that listener still renders the well-shaped empty answer, which
// house rule 3 accepts (it is not a refusal — the profile is registered and
// its handler runs) but which is worth a startup diagnosis, the same way
// [provider.CodeProviderUnimplemented] diagnoses the opposite mistake.
const codeProfileUnscripted = "scenario.profile.unscripted"

// unscriptedProfileFindings warns once per enabled profile whose scenario
// has no block it can actually reach — the reference-only-built-ins
// accommodation (docs/proposals/framework-seam.md, "Contracts, goldens and
// built-ins for out-of-tree profiles"): builtin:happy will never cover a
// fifth profile without a PR here, so a registered profile silently getting
// no block is the ordinary case for an out-of-tree consumer, and this
// finding is the diagnosis rather than a load failure.
func unscriptedProfileFindings(cfg config.Config, sc *scenario.Scenario, scenarioName string) []scenario.Finding {
	if sc == nil || cfg.Set == nil {
		return nil
	}

	var findings []scenario.Finding
	for _, name := range cfg.Enabled() {
		p, ok := cfg.Set.Lookup(name)
		if !ok {
			continue
		}
		if profileHasAScriptedBlock(sc, p) {
			continue
		}
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityWarning,
			Code:     codeProfileUnscripted,
			Path:     "providers." + string(name),
			Message: fmt.Sprintf(
				"profile %q is registered but scenario %q scripts no %q block; "+
					"every request to it renders the well-shaped empty answer",
				name, scenarioName, name),
		})
	}
	return findings
}

// profileHasAScriptedBlock reports whether sc scripts at least one block
// this profile's own listener can actually reach.
//
// The PRIMARY entry — Route.Entry == "", which Exchange.Entry() resolves as
// Deps.Scenario.Provider(string(x.Provider)) — is checked by the LISTENER's
// OWN NAME (Phase 10 unit 8), never by its effective Kind: an instanced
// listener (Kind != Name) reads its own block by Name, so a scenario
// scripting only the PRIMARY instance's block ("acme") must not read the
// FALLBACK instance ("acme_fallback") as scripted merely because the two
// share a Kind — that was this function's bug before this unit, found by
// the instancing test that pins the fix. Name == Kind for every profile
// registered before Kind existed and for all four reference profiles today,
// so this is unchanged for them: their own block is still found by the same
// name it always was.
//
// Every OTHER entry-kind key p.Validators claims — a multi-entry profile's
// secondary surfaces, Perplexity's Agent kind being the shipped example —
// is checked by that literal kind string AS A NAME: NewSet refuses
// instancing a multi-entry profile outright (Validate's own doc comment,
// "instancing"), so a secondary entry's Route.Entry is always a static Go
// string equal to its own kind, never subject to renaming, and
// Exchange.Entry() resolves it by that same literal name
// (Deps.Scenario.Provider(x.Route.Entry)).
func profileHasAScriptedBlock(sc *scenario.Scenario, p provider.Profile) bool {
	if sc.Providers.Get(string(p.Name)) != nil {
		return true
	}
	kind := p.Kind
	if kind == "" {
		kind = string(p.Name)
	}
	for entryKind := range p.Validators {
		if entryKind == kind {
			continue // the profile's own kind, already checked above by Name
		}
		if sc.Providers.Get(entryKind) != nil {
			return true
		}
	}
	return false
}

// add registers one loaded scenario and builds the Deps every handler serving it
// receives.
//
// The journal is the same instance for every scenario, which is what lets
// /__admin/requests show a consumer's calls across scenarios as one run; state
// isolation between concurrent tests comes from the namespace, not from the
// scenario. The fault engine is per scenario, because a fault plan is behaviour
// and behaviour is exactly what a scenario is.
func (s *Server) add(ls *loadedScenario, isDefault bool) {
	ls.deps = provider.Deps{
		Scenario:            ls.scenario,
		Journal:             s.journal,
		Faults:              ls.faults,
		Clock:               provider.SystemClock{},
		DelayMode:           provider.DelayReal,
		Logger:              requestLogger(s.logger, ls.scenario),
		MaxRequestBytes:     s.cfg.MaxRequestBytes,
		MaxJournalBodyBytes: s.cfg.MaxJournalBodyBytes,
		MaxNamespaces:       s.cfg.MaxNamespaces,
		Jobs:                s.jobs,
		MaxJobs:             s.cfg.MaxJobs,
		CredentialNames:     s.cfg.Set.CredentialNames(),
	}

	s.scenarios = append(s.scenarios, ls)
	s.byName[ls.name] = ls
	if isDefault {
		s.def = ls
	}

	// A name the /x/ prefix cannot carry is a warning, never a failure: with
	// --scenario the name comes from the file's own `name:` field and has always
	// been free-form, and the scenario is still served as the default. In a
	// scenario directory the name is the file name, which the operator chooses,
	// so the warning is actionable.
	if !provider.ValidNamespace(ls.name) {
		s.logger.Warn("scenario.unselectable",
			slog.String("scenario", ls.name),
			slog.String("source", ls.origin),
			slog.String("hint", "the /x/<scenario> prefix accepts 1 to 64 characters from [A-Za-z0-9_-]"))
	}
}

// scenarioSource is one scenario this process was configured to serve, before it
// has been read. It exists so that the two configuration modes — one --scenario,
// or a directory of them — differ only in how the list is produced, and the
// load-validate-register sequence downstream stays single.
type scenarioSource struct {
	// name is the /x/<scenario> selector, or empty to take the scenario's own
	// declared name once it has been parsed. It is empty only in single-scenario
	// mode, where there is no file name to select on.
	name string

	// origin identifies the scenario in a log line or a startup error.
	origin string

	// isDefault marks the scenario that serves a request naming none.
	isDefault bool

	load func() (*scenario.Scenario, scenario.Report, error)
}

// scenarioSources lists the scenarios this configuration asks for.
//
// A --scenario-dir is listed and ordered by config.ScenarioEntries, so the log
// of what was loaded, and any error naming a scenario, read the same on every
// run and every file system. Which entry serves an unprefixed request is
// config.DefaultScenarioEntry's rule: the one named "default", or the only one
// there is, and otherwise none — a directory of several scenarios with no stated
// default has no unambiguous answer, and inventing one is exactly the "silently
// serving different behaviour than the URL asked for" failure namespaces and
// scenario selection exist to prevent.
func scenarioSources(cfg config.Config) ([]scenarioSource, error) {
	if !cfg.ScenarioDirMode() {
		return []scenarioSource{{
			origin:    cfg.ScenarioPath,
			isDefault: true,
			load:      func() (*scenario.Scenario, scenario.Report, error) { return loadScenario(cfg) },
		}}, nil
	}

	entries, err := cfg.ScenarioEntries()
	if err != nil {
		return nil, err
	}
	def, hasDefault := config.DefaultScenarioEntry(entries)

	out := make([]scenarioSource, 0, len(entries))
	for _, e := range entries {
		out = append(out, scenarioSource{
			name:      e.Name,
			origin:    e.File,
			isDefault: hasDefault && e.Name == def.Name,
			load:      func() (*scenario.Scenario, scenario.Report, error) { return loadScenarioEntry(cfg, e.File) },
		})
	}
	return out, nil
}

// loadScenario opens the configured scenario and parses it.
//
// A mounted file goes through config.OpenScenario, which is os.Root-confined and
// refuses to traverse outside --scenario-root even through a symlink.
// scenario.Load is deliberately not used here: it performs no path containment,
// and wiring it into the binary would silently bypass that containment.
func loadScenario(cfg config.Config) (*scenario.Scenario, scenario.Report, error) {
	if name, ok := cfg.Builtin(); ok {
		return scenarios.Load(name)
	}

	f, name, err := cfg.OpenScenario()
	if err != nil {
		var r scenario.Report
		return nil, r, err
	}
	defer func() { _ = f.Close() }()

	src, err := io.ReadAll(f)
	if err != nil {
		return nil, scenario.Report{}, fmt.Errorf("reading scenario %s: %w", name, err)
	}
	return scenario.Parse(src)
}

// loadScenarioEntry opens one file in --scenario-dir and parses it. Containment
// is config.OpenScenarioEntry's, which resolves the name through os.Root and so
// cannot be walked out of the directory, symlinks included — the directory is
// usually a mount, and a mount is untrusted input.
func loadScenarioEntry(cfg config.Config, file string) (*scenario.Scenario, scenario.Report, error) {
	f, err := cfg.OpenScenarioEntry(file)
	if err != nil {
		return nil, scenario.Report{}, err
	}
	defer func() { _ = f.Close() }()

	src, err := io.ReadAll(f)
	if err != nil {
		return nil, scenario.Report{}, fmt.Errorf("reading scenario %s: %w", file, err)
	}
	return scenario.Parse(src)
}

// relaxAuth applies --strict-auth=false to the loaded scenario.
//
// Config documents StrictAuth as "a missing credential is a 401 when the
// scenario does not say otherwise", and each PROFILE declares that default
// through Profile.DefaultAuth — required for the three research profiles,
// optional for MCP (decision 3), never one flattened behaviour for all four.
// The permissive half has to be expressed somewhere, and the scenario entry is
// the only seam the handlers read, so it is expressed here — on an entry
// whose OWNING profile defaults to AuthRequired and that declares no policy
// of its own; never on an entry that declares one, and never on an entry
// whose profile already defaults to AuthOptional (relaxing it would write an
// explicit policy with the same meaning the profile's own default already
// has, which is harmless but not what "consult the profile" means).
// [Exchange.AuthPolicy] reads the same DefaultAuth for the same reason: an
// entry this function leaves untouched still resolves to the right mode
// there.
//
// It runs after validation so that a scenario is validated exactly as it was
// written, and it mutates the in-memory scenario only: nothing is written back
// to the file, and /__admin/scenario reports the findings from the file as
// authored.
func relaxAuth(cfg config.Config, s *scenario.Scenario) {
	if cfg.StrictAuth || s == nil {
		return
	}
	defaults := entryDefaultAuth(cfg.Set)
	for _, name := range s.Providers.Names() {
		e := s.Providers.Get(name)
		if e == nil || e.Auth != nil {
			continue
		}
		if defaults[name] == scenario.AuthOptional {
			continue
		}
		e.Auth = &scenario.AuthPolicy{Mode: scenario.AuthOptional}
	}
}

// entryDefaultAuth maps every scenario entry kind any registered profile
// declares a validator for to that profile's DefaultAuth, so relaxAuth can
// consult the OWNING profile's default instead of flattening every profile
// to one strict-auth behaviour.
func entryDefaultAuth(set *provider.Set) map[string]scenario.AuthMode {
	out := make(map[string]scenario.AuthMode)
	for _, p := range set.All() {
		for entry := range p.Validators {
			out[entry] = p.DefaultAuth
		}
	}
	return out
}

// faultsFanout is the fault engine the admin surface resets: one Reset reaches
// every scenario's engine.
//
// It exists because a fault plan belongs to the scenario that declares it, so a
// process serving several scenarios holds several engines, while POST
// /__admin/reset means "zero the counters the handlers consult" regardless of
// which scenario a handler serves.
//
// Next delegates to the default scenario's engine. The admin surface never calls
// it — resetting is all it wants — and a key alone cannot say which scenario it
// belongs to, so answering from the default is the only honest choice available.
type faultsFanout struct {
	engines []provider.Faults
	primary provider.Faults
}

// namespacedEngine is the optional per-namespace reset a fault engine may
// support. It is asserted for rather than required, so that this package
// compiles against an engine that has not grown the capability yet and the
// fan-out reports the truth about what it can do.
type namespacedEngine interface {
	ResetIn(namespace string)
}

// scopedFaultsFanout is a faultsFanout whose every engine supports a
// namespace-scoped reset. The two types exist so that the capability the admin
// surface asserts for is present exactly when it can be honoured for every
// scenario: a fan-out that advertised ResetIn while quietly skipping an engine
// would clear one scenario's cursors and leave another's mid-plan, which is the
// wrong-lane failure this feature exists to prevent.
type scopedFaultsFanout struct {
	faultsFanout
}

// newFaultsFanout returns the fault seam the admin surface is wired with.
func newFaultsFanout(loaded []*loadedScenario) provider.Faults {
	f := faultsFanout{engines: make([]provider.Faults, 0, len(loaded))}
	scoped := true
	for _, ls := range loaded {
		f.engines = append(f.engines, ls.faults)
		if _, ok := any(ls.faults).(namespacedEngine); !ok {
			scoped = false
		}
	}
	if len(f.engines) > 0 {
		f.primary = f.engines[0]
	}
	if scoped && len(f.engines) > 0 {
		return scopedFaultsFanout{f}
	}
	return f
}

// Next claims an attempt from the first registered scenario's engine.
func (f faultsFanout) Next(key string) provider.FaultDecision {
	if f.primary == nil {
		return provider.FaultDecision{Index: -1, Key: key}
	}
	return f.primary.Next(key)
}

// Reset zeroes every scenario's counters, in registry order so that the log a
// reset produces reads the same on every run.
func (f faultsFanout) Reset() {
	for _, e := range f.engines {
		e.Reset()
	}
}

// ResetIn zeroes one namespace's counters in every scenario, leaving every other
// namespace untouched. A namespace is a state boundary that crosses scenarios: a
// test that called two scenarios under one test ID resets both halves of its own
// state and nobody else's.
func (f scopedFaultsFanout) ResetIn(namespace string) {
	for _, e := range f.engines {
		if scoped, ok := any(e).(namespacedEngine); ok {
			scoped.ResetIn(namespace)
		}
	}
}

// Run binds every enabled listener and blocks until ctx is done. The admin
// listener binds first so /healthz answers during provider bind, and readiness
// flips only once every listener is accepting.
//
// A bind failure — most often a port already in use — closes whatever came up
// before it and returns immediately with the surface and port named. It never
// retries and never waits: a simulator that hangs at startup surfaces in a
// consumer's suite as an unexplained timeout.
//
// When ctx is done Run performs the graceful shutdown [Server.Shutdown]
// describes, bounded by the configured grace period, and returns once every
// listener has stopped. A second call returns [ErrAlreadyRun].
func (s *Server) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !s.running.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}

	// Buffered by the number of listeners, so a failing Serve never blocks on a
	// send after the select below has already moved on to shutdown.
	serveErrs := make(chan error, len(s.surfaces))
	var wg sync.WaitGroup

	for _, sf := range s.surfaces {
		if err := s.bind(sf); err != nil {
			s.forceClose()
			wg.Wait()
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sf.http.Serve(sf.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveErrs <- fmt.Errorf("serving the %s listener: %w", sf.name, err)
			}
		}()
	}

	stopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(stopped)
	}()

	s.ready.Store(true)

	// Emitted unconditionally, once, and before server.ready: namespace lane
	// state, turn cursors and async job records are all per-process
	// (docs/design/async-jobs.md §8), and that is true of every configuration
	// this binary can run, not only the ones that happen to hit it. A caller
	// waiting on Started must see this before it does anything else, which is
	// why it runs before close(s.started) below rather than after.
	s.logger.Info("servicesim.single_replica_required",
		slog.String("hint", "namespace lane state, turn cursors and async job records are per-process; "+
			"run one replica, or route stickily on the /n/<namespace> path prefix"),
		slog.String("doc", "docs/troubleshooting.md"))

	s.logger.Info("server.ready",
		slog.String("scenario", s.Scenario().Name),
		slog.String("version", s.version),
		slog.Int("listeners", len(s.surfaces)),
		slog.Int("scenarios", len(s.scenarios)),
		slog.Int("max_namespaces", s.cfg.MaxNamespaces),
		slog.Int("max_jobs", s.cfg.MaxJobs),
		slog.String("namespace", provider.DefaultNamespace),
		slog.Int("warnings", len(s.Report().Warnings())))

	// Closed after the event is emitted, not before, so that a caller which
	// waits on Started and then reads the log is guaranteed to see every startup
	// event rather than racing the last one.
	close(s.started)

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-serveErrs:
	case <-stopped: // Shutdown was called from elsewhere.
	}

	// The shutdown context is derived from ctx without its cancellation, because
	// ctx is usually already done by the time we get here and a cancelled context
	// would make Shutdown close in-flight connections instead of draining them.
	shutdownCtx := context.WithoutCancel(ctx)
	if s.cfg.ShutdownGrace > 0 {
		var cancel context.CancelFunc
		shutdownCtx, cancel = context.WithTimeout(shutdownCtx, s.cfg.ShutdownGrace)
		defer cancel()
	}
	shutdownErr := s.Shutdown(shutdownCtx)
	<-stopped

	if runErr == nil {
		select {
		case runErr = <-serveErrs:
		default:
		}
	}
	if runErr != nil {
		return runErr
	}
	return shutdownErr
}

// Shutdown stops every listener within the configured grace period.
//
// Listeners stop in reverse of the order they bound, so the admin listener is
// last: a container's health probe keeps getting an answer for as long as any
// provider listener is still draining, which is what makes a rolling restart
// observable rather than a silent gap.
//
// In-flight requests are drained, not cut. When ctx expires before a listener
// has drained, that listener is closed outright — the alternative is a process
// that will not exit, and a shutdown grace period that cannot be enforced is not
// a grace period.
//
// Readiness is dropped first, so a probe that arrives during the drain reports
// not ready rather than inviting more traffic.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.ready.Store(false)

	var errs []error
	for _, sf := range slices.Backward(s.surfaces) {
		if err := sf.http.Shutdown(ctx); err != nil {
			_ = sf.http.Close()
			errs = append(errs, fmt.Errorf("shutting down the %s listener: %w", sf.name, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// forceClose closes every listener and every connection immediately. It is the
// bind-failure path only: a process that could not come up fully has nothing to
// drain, and leaving the listeners that did bind open would hold their ports
// against the operator's next attempt.
func (s *Server) forceClose() {
	s.ready.Store(false)
	for _, sf := range slices.Backward(s.surfaces) {
		_ = sf.http.Close()
	}
}

// Addr returns the bound address of a surface, which is how a test that asked for
// port 0 discovers the real port. name is [SurfaceAdmin] or a provider.Name.
//
// It is empty for a surface that is not served by this process and for one that
// has not bound yet; wait on [Server.Started] to tell the two apart.
func (s *Server) Addr(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sf := range s.surfaces {
		if sf.name == name {
			return sf.bound
		}
	}
	return ""
}

// Started returns a channel closed once every enabled listener is accepting and
// readiness reports true.
//
// It exists because [Server.Run] blocks, and a caller that configured port 0
// cannot ask [Server.Addr] anything useful until the bind has happened. Select on
// it together with Run's error: a bind failure never closes it.
func (s *Server) Started() <-chan struct{} {
	return s.started
}

// Scenario returns the loaded, validated, resolved scenario a request that names
// none is served with.
//
// It is the empty scenario — never nil — when a scenario directory holds several
// scenarios and none of them is the default, because in that configuration there
// is no such request: every request must name its scenario, and one that does not
// is failed closed.
func (s *Server) Scenario() *scenario.Scenario {
	if s.def == nil {
		return scenario.Empty()
	}
	return s.def.scenario
}

// ScenarioNames returns the /x/<scenario> selectors this process serves, in
// registry order. It is the list a consumer needs to see when a request named a
// scenario that does not exist.
func (s *Server) ScenarioNames() []string {
	out := make([]string, 0, len(s.scenarios))
	for _, ls := range s.scenarios {
		out = append(out, ls.name)
	}
	return out
}

// Report returns the default scenario's validation findings. It has no error
// findings — a scenario that produced one never reached a Server — and it is
// empty when there is no default scenario.
func (s *Server) Report() scenario.Report {
	if s.def == nil {
		return scenario.Report{}
	}
	return s.def.report
}
