package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/c360studio/servicesim/internal/admin"
	"github.com/c360studio/servicesim/internal/config"
	"github.com/c360studio/servicesim/internal/faults"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/provider/exa"
	"github.com/c360studio/servicesim/provider/perplexity"
	"github.com/c360studio/servicesim/provider/tavily"
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

// Server is a running Servicesim instance.
type Server struct {
	cfg     config.Config
	logger  *slog.Logger
	version string

	scenario *scenario.Scenario
	report   scenario.Report

	journal *journal.Ring
	faults  *faults.Engine
	ready   *atomic.Bool

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

// New builds a Server from configuration. It loads and validates the scenario
// before binding anything: a scenario with error findings fails the process
// rather than serving a subtly wrong contract.
//
// The order is acceptance criterion 4 and is not negotiable. scenario.Parse
// validates the envelope; provider.ValidateScenario then asks each registered
// provider to decode and validate the projection bodies the scenario package
// deliberately left as raw YAML nodes. Only a scenario that survives both is
// wired into a handler, and only [Server.Run] flips readiness afterwards.
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
//
// Every route is registered with the engine, including routes whose listener is
// disabled, so that the key set does not depend on which subset of the listeners
// this process runs. A key that were missing would make its route report
// fault.unknown_key on every request instead of serving the scenario's declared
// fault.
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

	sc, report, err := loadScenario(cfg)
	if err != nil {
		logFindings(logger, report, cfg.ScenarioPath)
		return nil, fmt.Errorf("loading scenario %s: %w", cfg.ScenarioPath, err)
	}

	// Projection bodies are validated here, after the envelope and before
	// anything binds. Only the enabled providers are registered, so a scenario
	// entry this process is not serving is reported as unimplemented rather than
	// silently validated and then ignored.
	report.Findings = append(report.Findings, provider.ValidateScenario(sc, validators(cfg))...)
	logFindings(logger, report, cfg.ScenarioPath)
	if !report.OK() {
		return nil, fmt.Errorf("validating scenario %s: %w", cfg.ScenarioPath, report.Err())
	}

	relaxAuth(cfg, sc)

	s := &Server{
		cfg:      cfg,
		logger:   logger,
		version:  o.version,
		scenario: sc,
		report:   report,
		journal:  journal.NewRing(cfg.JournalCapacity, cfg.MaxJournalBodyBytes),
		faults:   faults.New(sc, slices.Concat(exa.Routes(), tavily.Routes(), perplexity.Routes())),
		ready:    new(atomic.Bool),
		started:  make(chan struct{}),
	}

	deps := provider.Deps{
		Scenario:            sc,
		Journal:             s.journal,
		Faults:              s.faults,
		Clock:               provider.SystemClock{},
		DelayMode:           provider.DelayReal,
		Logger:              requestLogger(logger, sc),
		MaxRequestBytes:     cfg.MaxRequestBytes,
		MaxJournalBodyBytes: cfg.MaxJournalBodyBytes,
	}
	adminDeps := admin.Deps{
		Journal:  s.journal,
		Faults:   s.faults,
		Scenario: sc,
		Report:   report,
		Ready:    s.ready,
		Version:  o.version,
		Logger:   logger,
	}
	s.surfaces = s.newSurfaces(deps, adminDeps)
	return s, nil
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

// validators returns the projection validators for the providers this process
// serves, keyed on the scenario provider kind each one implements.
//
// Only enabled providers are registered, and that is the point rather than an
// oversight: --providers exa runs a process with nothing bound in front of a
// tavily block, so reporting that block as unimplemented is accurate. It is a
// warning either way, never a load failure.
//
// Perplexity contributes two kinds from one listener — Sonar and the Agent API
// are independent scenario entries with independent auth, validation and fault
// policies — so its registrations come from perplexity.Validators rather than
// from a literal here that could fall out of step with it.
func validators(cfg config.Config) map[string]provider.Validator {
	out := make(map[string]provider.Validator, 4)
	for _, name := range cfg.Enabled() {
		switch name {
		case provider.Exa:
			out[string(provider.Exa)] = exa.Validator{}
		case provider.Tavily:
			out[tavily.Name] = tavily.Validator{}
		case provider.Perplexity:
			maps.Copy(out, perplexity.Validators())
		}
	}
	return out
}

// relaxAuth applies --strict-auth=false to the loaded scenario.
//
// Config documents StrictAuth as "a missing credential is a 401 when the
// scenario does not say otherwise", and the provider packages implement the
// strict half by defaulting an entry with no auth block to AuthRequired. The
// permissive half has to be expressed somewhere, and the scenario entry is the
// only seam the handlers read, so it is expressed here — on entries that declare
// no policy of their own, and never on an entry that does.
//
// It runs after validation so that a scenario is validated exactly as it was
// written, and it mutates the in-memory scenario only: nothing is written back
// to the file, and /__admin/scenario reports the findings from the file as
// authored.
func relaxAuth(cfg config.Config, s *scenario.Scenario) {
	if cfg.StrictAuth || s == nil {
		return
	}
	for _, name := range s.Providers.Names() {
		e := s.Providers.Get(name)
		if e == nil || e.Auth != nil {
			continue
		}
		e.Auth = &scenario.AuthPolicy{Mode: scenario.AuthOptional}
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
	s.logger.Info("server.ready",
		slog.String("scenario", s.scenario.Name),
		slog.String("version", s.version),
		slog.Int("listeners", len(s.surfaces)),
		slog.Int("warnings", len(s.report.Warnings())))

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

// Scenario returns the loaded, validated, resolved scenario.
func (s *Server) Scenario() *scenario.Scenario {
	return s.scenario
}

// Report returns the scenario's validation findings, envelope and projections
// together. It has no error findings — a scenario that produced one never
// reached a Server.
func (s *Server) Report() scenario.Report {
	return s.report
}
