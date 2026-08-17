package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenarios"
)

// Built-in defaults, applied when neither a flag nor an environment variable
// supplies a value. They are exported so cmd/servicesim, internal/server and the
// documentation can name one value rather than restating a literal that drifts.
const (
	// DefaultScenario selects the embedded happy-path scenario, so the binary is
	// useful with no flags and no mount.
	DefaultScenario = scenarios.Prefix + scenarios.Default

	// DefaultBindAddress is loopback. The container image sets
	// SERVICESIM_BIND_ADDRESS=0.0.0.0; binding every interface by default would
	// expose a developer's simulator to their whole network.
	DefaultBindAddress = "127.0.0.1"

	// DefaultAdminPort is the admin surface: health, readiness and the journal.
	DefaultAdminPort = 8080

	// DefaultJournalCapacity bounds retained journal entries. Zero disables
	// retention.
	DefaultJournalCapacity = 1000

	// DefaultMaxNamespaces bounds the number of live namespaces. Namespaces are
	// created implicitly on first use, so they are an unbounded-growth surface
	// unless something bounds them; total journal retention is
	// MaxNamespaces × JournalCapacity, and both are configurable.
	DefaultMaxNamespaces = 1024

	// DefaultMaxJobs bounds the number of live async jobs per namespace. It
	// matches jobs.DefaultMaxJobs, checked by an agreement test rather than
	// imported directly: importing internal/jobs here would buy one constant at
	// the cost of a dependency this package does not otherwise need.
	DefaultMaxJobs = 256

	// ScenarioDirDefault is the scenario name in --scenario-dir that serves a
	// request carrying no /x/<scenario> prefix. See [DefaultScenarioEntry] for
	// the full rule, which also covers a directory holding one scenario under
	// any name.
	ScenarioDirDefault = "default"

	// DefaultMaxRequestBytes bounds a request body at 1 MiB.
	DefaultMaxRequestBytes int64 = 1 << 20

	// DefaultMaxJournalBodyBytes bounds the body bytes retained per journal entry
	// at 64 KiB.
	DefaultMaxJournalBodyBytes = 1 << 16

	// DefaultReadHeaderTimeout bounds how long a client may take to send request
	// headers.
	DefaultReadHeaderTimeout = 5 * time.Second

	// DefaultShutdownGrace bounds how long in-flight requests have to finish
	// after a signal.
	DefaultShutdownGrace = 5 * time.Second

	// DefaultLogLevel is the slog level name logging starts at.
	DefaultLogLevel = "info"

	// DefaultLogFormat is machine-readable, because these logs are usually read
	// out of a CI container rather than a terminal.
	DefaultLogFormat = "json"

	// DefaultStrictAuth makes a missing credential a 401 unless the scenario says
	// otherwise. A consumer must be able to prove it sent the credential.
	DefaultStrictAuth = true
)

// LogFormatJSON and LogFormatText are the accepted --log-format values.
const (
	LogFormatJSON = "json"
	LogFormatText = "text"
)

// defaultProvidersString joins set's registered names in registration order,
// comma-separated: the --providers flag's default value, and what an unknown
// name's error names as "this build simulates …". It is a function, not a
// package-level slice, for the same reason [Config.order] is one: a Set is
// the composition root's own value, never a package-level default the four
// reference profiles would otherwise force on this package.
func defaultProvidersString(set *provider.Set) string {
	names := set.Names()
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = string(name)
	}
	return strings.Join(out, ",")
}

// portEnvName derives the SERVICESIM_<NAME>_PORT environment variable for a
// registered profile name: upper-cased, with '-' folded to '_' since an
// environment variable name may not contain a hyphen even though
// [provider.Profile.Name] may (see validProfileName in provider/profile.go).
func portEnvName(name provider.Name) string {
	return "SERVICESIM_" + strings.ToUpper(strings.ReplaceAll(string(name), "-", "_")) + "_PORT"
}

// Listener is one bound HTTP surface.
type Listener struct {
	// Port is the TCP port to bind. Zero asks the operating system for an
	// ephemeral port, which is how an in-process test avoids collisions.
	Port int

	// Enabled reports whether the surface is served at all.
	Enabled bool
}

// Config is the fully resolved runtime configuration.
type Config struct {
	// BindAddress is the interface every listener binds. Never empty: an empty
	// value would mean "every interface" to net.Listen, which is the opposite of
	// the loopback default and too dangerous to reach by accident.
	BindAddress string

	// Admin is the admin surface. It is always enabled — health, readiness and
	// the journal are how a consumer observes the simulator.
	Admin Listener

	// Set is the profile registry this Config was resolved against. It is
	// never nil after a successful [Load]: internal/server derives every
	// listener, handler, route and validator from it rather than importing a
	// profile package itself (Phase 10 unit 3, "internal/config and
	// internal/server import NO profile package").
	Set *provider.Set

	// Listeners holds one resolved [Listener] per profile in Set, keyed by
	// name. It replaces the four Exa/Tavily/Perplexity/MCP struct fields this
	// type used to carry by name: ten hand-maintained enumeration sites
	// (docs/proposals/framework-seam.md, config #1-#10) collapse to one loop
	// over Set.All() in [newFlagSet] and one lookup here. [Config.Listener]
	// reports a miss as ok=false, which is the fail-closed hook
	// [Config.enable] relies on too.
	Listeners map[provider.Name]*Listener

	// order is Set's registration order, captured once at [Load] so
	// [Config.Enabled] and [Config.validate] never have to ask Set again —
	// and never range over Listeners, which CLAUDE.md house rule 2 forbids:
	// a map range would make listener startup order, and any message that
	// lists providers, differ between runs.
	order []provider.Name

	// ScenarioPath is the scenario to load. A "builtin:" prefix selects an
	// embedded protocol scenario, for example "builtin:happy". It is empty in
	// scenario-directory mode, where there is no single startup scenario;
	// [Config.ScenarioDirMode] is how a caller tells the two apart.
	ScenarioPath string

	// ScenarioRoot bounds scenario resolution. Defaults to the directory of
	// ScenarioPath. Nothing outside it can be opened, symlinks included. It is
	// empty when ScenarioPath names a built-in, which never touches the file
	// system, and in scenario-directory mode, where ScenarioDir is its own root.
	ScenarioRoot string

	// ScenarioDir holds a directory of scenarios, every one of them loaded and
	// validated at startup and selectable by name through the /x/<scenario>
	// path prefix. It is empty in the single-scenario mode ScenarioPath
	// describes, and the two are mutually exclusive.
	//
	// The directory is its own containment root: [Config.ScenarioEntries] and
	// [Config.OpenScenarioEntry] resolve names under it through [os.Root], on
	// the assumption that a mounted directory of files is untrusted input.
	ScenarioDir string

	// MaxNamespaces bounds the number of live namespaces. Exceeding it is an
	// error the request surface reports, never a silent eviction: evicting a
	// namespace would reset a running test's cursors mid-loop, which is the
	// worst failure this design can produce. At least 1, because every request
	// belongs to a namespace even when it names none.
	MaxNamespaces int

	// MaxJobs bounds the number of live async jobs per namespace. Exceeding it
	// refuses the create rather than evicting a record: an evicted job would
	// turn a later poll for a job the client successfully created into the
	// vendor's 404, which is a correctness failure rather than an
	// observability one (internal/jobs). At least 1, for the same reason as
	// MaxNamespaces: zero would refuse every create.
	MaxJobs int

	// JournalCapacity is the maximum number of retained journal entries. Zero
	// disables retention.
	JournalCapacity int

	// MaxRequestBytes bounds a request body.
	MaxRequestBytes int64

	// MaxJournalBodyBytes bounds the body bytes retained per journal entry.
	MaxJournalBodyBytes int

	// ReadHeaderTimeout bounds how long a client may take to send request
	// headers. Zero means no timeout.
	ReadHeaderTimeout time.Duration

	// ShutdownGrace bounds how long in-flight requests have to finish after a
	// signal. Zero closes listeners immediately.
	ShutdownGrace time.Duration

	// LogLevel is the minimum level slog emits.
	LogLevel slog.Level

	// LogFormat is [LogFormatJSON] or [LogFormatText].
	LogFormat string

	// StrictAuth makes a missing credential a 401 when the scenario does not say
	// otherwise.
	StrictAuth bool

	// Healthcheck runs the process as a health probe against a running instance
	// and exits, which is what the image's HEALTHCHECK invokes. It is a process
	// mode, not configuration.
	Healthcheck bool

	// ShowVersion prints version information and exits.
	ShowVersion bool

	// PrintRoutes prints one "METHOD /path" per line, drawn from
	// [provider.Set.Routes] in the set's own stable order (registration then
	// pattern), and exits. It is a process mode, not configuration, and
	// works against any composed Set — a consumer's own binary included —
	// which is what lets scripts/check-docs.sh and image-smoke.sh query a
	// binary instead of globbing provider/*/*.go.
	PrintRoutes bool

	// PrintPorts prints a JSON array of {name, port, default_auth} in
	// registration order — one object per entry in [Config.Listeners],
	// naming the RESOLVED port (after flags and environment, not the
	// profile's own default) — and exits. It is what generates the
	// Dockerfile's EXPOSE list and docker-compose.example.yml's port map
	// and credential rows instead of hand-copying them.
	PrintPorts bool

	// PrintHosts prints one host per line, drawn from
	// [provider.Set.LiveHosts] sorted and deduplicated, and exits. It is
	// what scripts/lint-no-live-hosts.sh unions with its own hand-kept base
	// list, so a profile's Hosts travel into the guard automatically.
	PrintHosts bool
}

// binding pairs a flag name with the environment variable that supplies its
// value when the flag was not given. --healthcheck and --version are absent by
// design: an environment variable that could silently turn the server into a
// health probe would be a footgun.
type binding struct {
	flag string
	env  string
}

// bindings is the flag-to-environment table for every flag whose name does
// not depend on the registered Set. [portBindings] adds the one
// per-profile binding — <name>-port / SERVICESIM_<NAME>_PORT — that this
// table cannot list statically. It is the single source of the mapping
// documented in docs/design/package-design.md §2.7.
var bindings = []binding{
	{"scenario", "SERVICESIM_SCENARIO"},
	{"scenario-root", "SERVICESIM_SCENARIO_ROOT"},
	{"scenario-dir", "SERVICESIM_SCENARIO_DIR"},
	{"bind-address", "SERVICESIM_BIND_ADDRESS"},
	{"admin-port", "SERVICESIM_ADMIN_PORT"},
	{"providers", "SERVICESIM_PROVIDERS"},
	{"max-namespaces", "SERVICESIM_MAX_NAMESPACES"},
	{"max-jobs", "SERVICESIM_MAX_JOBS"},
	{"journal-capacity", "SERVICESIM_JOURNAL_CAPACITY"},
	{"max-request-bytes", "SERVICESIM_MAX_REQUEST_BYTES"},
	{"max-journal-body-bytes", "SERVICESIM_MAX_JOURNAL_BODY_BYTES"},
	{"read-header-timeout", "SERVICESIM_READ_HEADER_TIMEOUT"},
	{"shutdown-grace", "SERVICESIM_SHUTDOWN_GRACE"},
	{"log-level", "SERVICESIM_LOG_LEVEL"},
	{"log-format", "SERVICESIM_LOG_FORMAT"},
	{"strict-auth", "SERVICESIM_STRICT_AUTH"},
}

// portBindings returns the <name>-port / SERVICESIM_<NAME>_PORT binding for
// every profile in set, in registration order. It is a function rather than
// a table entry per name because the name set is a runtime value (the
// registered Set), not a compile-time list of four vendors.
func portBindings(set *provider.Set) []binding {
	names := set.Names()
	out := make([]binding, len(names))
	for i, name := range names {
		out[i] = binding{flag: string(name) + "-port", env: portEnvName(name)}
	}
	return out
}

// raw holds the flag targets. Keeping them in one struct means register, parse
// and assemble cannot drift apart into three lists that disagree.
type raw struct {
	scenario            string
	scenarioRoot        string
	scenarioDir         string
	bindAddress         string
	adminPort           int
	ports               map[provider.Name]*int
	providers           string
	maxNamespaces       int
	maxJobs             int
	journalCapacity     int
	maxRequestBytes     int64
	maxJournalBodyBytes int
	readHeaderTimeout   time.Duration
	shutdownGrace       time.Duration
	logLevel            string
	logFormat           string
	strictAuth          bool
	healthcheck         bool
	showVersion         bool
	printRoutes         bool
	printPorts          bool
	printHosts          bool
}

// newFlagSet registers every flag against r and returns the set. Output is
// discarded rather than written to stderr so that [Load] is hermetic: the
// caller decides what a parse failure looks like, and [Usage] supplies the
// help text.
//
// The port flags are the one registration loop that cannot be written
// statically: [set.All] supplies each profile's Name (the flag name and the
// journal's "provider" field), Title (the usage string) and Port (the
// default — zero asks the OS), in registration order — never a map range,
// for the same reason [Config.order] exists.
func newFlagSet(r *raw, set *provider.Set) *flag.FlagSet {
	flags := flag.NewFlagSet("servicesim", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	flags.StringVar(&r.scenario, "scenario", DefaultScenario,
		`scenario to serve: a file path, or "builtin:<name>" for an embedded protocol scenario`)
	flags.StringVar(&r.scenarioRoot, "scenario-root", "",
		"directory scenario resolution is confined to (default: the directory of --scenario)")
	flags.StringVar(&r.scenarioDir, "scenario-dir", "",
		"directory of scenarios to load, each selectable as /x/<name> (mutually exclusive with --scenario)")
	flags.StringVar(&r.bindAddress, "bind-address", DefaultBindAddress,
		"interface every listener binds")
	flags.IntVar(&r.adminPort, "admin-port", DefaultAdminPort,
		"admin listener port: /healthz, /readyz, /__admin (0 for an ephemeral port)")

	profiles := set.All()
	r.ports = make(map[provider.Name]*int, len(profiles))
	for _, p := range profiles {
		port := new(int)
		r.ports[p.Name] = port
		flags.IntVar(port, string(p.Name)+"-port", p.Port, p.Title+" listener port (0 for an ephemeral port)")
	}

	flags.StringVar(&r.providers, "providers", defaultProvidersString(set),
		"comma-separated providers to serve")
	flags.IntVar(&r.maxNamespaces, "max-namespaces", DefaultMaxNamespaces,
		"maximum number of live namespaces")
	flags.IntVar(&r.maxJobs, "max-jobs", DefaultMaxJobs,
		"maximum live async jobs per namespace")
	flags.IntVar(&r.journalCapacity, "journal-capacity", DefaultJournalCapacity,
		"maximum retained journal entries (0 disables retention)")
	flags.Int64Var(&r.maxRequestBytes, "max-request-bytes", DefaultMaxRequestBytes,
		"maximum accepted request body size in bytes (0 uses the default)")
	flags.IntVar(&r.maxJournalBodyBytes, "max-journal-body-bytes", DefaultMaxJournalBodyBytes,
		"maximum body bytes retained per journal entry (0 uses the default)")
	flags.DurationVar(&r.readHeaderTimeout, "read-header-timeout", DefaultReadHeaderTimeout,
		"maximum time a client may take to send request headers")
	flags.DurationVar(&r.shutdownGrace, "shutdown-grace", DefaultShutdownGrace,
		"maximum time in-flight requests have to finish after a signal")
	flags.StringVar(&r.logLevel, "log-level", DefaultLogLevel, "debug, info, warn or error")
	flags.StringVar(&r.logFormat, "log-format", DefaultLogFormat, "json or text")
	flags.BoolVar(&r.strictAuth, "strict-auth", DefaultStrictAuth,
		"reject a missing credential with the provider's 401 unless the scenario says otherwise")
	flags.BoolVar(&r.healthcheck, "healthcheck", false,
		"probe a running instance's admin health endpoint and exit")
	flags.BoolVar(&r.showVersion, "version", false, "print version information and exit")
	flags.BoolVar(&r.printRoutes, "print-routes", false,
		"print every registered route (not only the --providers subset this invocation enabled), "+
			"one \"METHOD /path\" per line, and exit")
	flags.BoolVar(&r.printPorts, "print-ports", false,
		"print every registered listener's configured port (not its bound port; 0 means the OS assigns one at "+
			"bind time) as a JSON array of {name, port, default_auth}, and exit")
	flags.BoolVar(&r.printHosts, "print-hosts", false,
		"print every registered profile's live hostnames, one per line, and exit")

	return flags
}

// Usage returns the flag help text for set. [Load] discards the flag
// package's own output so that it writes nothing anywhere; cmd/servicesim
// prints this instead when parsing returns [flag.ErrHelp].
func Usage(set *provider.Set) string {
	var r raw
	flags := newFlagSet(&r, set)
	var b strings.Builder
	flags.SetOutput(&b)
	flags.PrintDefaults()
	return b.String()
}

// Load resolves configuration from a registered profile Set, args and an
// environment lookup. set must not be nil: it is the ONLY source of which
// providers this build knows how to serve, so every listener, port flag and
// --providers name in the returned Config traces back to it. lookupEnv is
// injected rather than calling os.Getenv so config tests are hermetic and can
// run in parallel without t.Setenv serialising them; cmd/servicesim passes
// os.LookupEnv. A nil lookupEnv reads no environment at all.
//
// Precedence is explicit flag > environment variable > built-in default. Because
// a flag package cannot distinguish "set to the default value" from "not set",
// Load parses first, records which names FlagSet.Visit reports, and applies the
// environment only to the rest.
//
// Load validates every numeric bound before returning: a negative
// JournalCapacity, MaxRequestBytes or MaxJournalBodyBytes is an error naming the
// flag, not a value passed downstream. Otherwise --journal-capacity -1 panics in
// make([]Entry, capacity) at startup and the container reports unhealthy with a
// stack trace, and a negative --max-request-bytes clamps http.MaxBytesReader to
// zero, silently rejecting every request as too large. Zero keeps its documented
// meanings: retention disabled for capacity, and, for the two byte limits, the
// default — substituted here rather than left for a caller to interpret, because
// a zero reaching http.MaxBytesReader fails exactly the way a negative one does.
//
// It also settles which of the two scenario modes the process runs. --scenario
// names one scenario, --scenario-dir loads a directory of them for the
// /x/<scenario> path prefix to select between, and supplying both is an error
// rather than a precedence rule nobody would remember.
//
// A --help request returns [flag.ErrHelp] unwrapped, so errors.Is identifies it.
func Load(set *provider.Set, args []string, lookupEnv func(string) (string, bool)) (Config, error) {
	if set == nil {
		return Config{}, errors.New("config: Load: set must not be nil")
	}
	if lookupEnv == nil {
		lookupEnv = func(string) (string, bool) { return "", false }
	}

	var r raw
	flags := newFlagSet(&r, set)

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, err
		}
		return Config{}, fmt.Errorf("parsing flags: %w", err)
	}
	if flags.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q: only flags are accepted", flags.Arg(0))
	}

	// provided records the names the operator supplied by either mechanism,
	// which is a different question from "differs from the default": mutually
	// exclusive flags such as --scenario and --scenario-dir have to reject the
	// combination the operator typed, not the values that survived it.
	provided := make(map[string]bool, flags.NFlag())
	flags.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	all := append(append([]binding{}, bindings...), portBindings(set)...)
	for _, b := range all {
		if provided[b.flag] {
			continue
		}
		value, ok := lookupEnv(b.env)
		if !ok {
			continue
		}
		// Set reuses the flag's own parser, so an unparseable environment value
		// fails here with the same message an unparseable flag value would.
		if err := flags.Set(b.flag, value); err != nil {
			return Config{}, fmt.Errorf("%s=%q: %w", b.env, value, err)
		}
		provided[b.flag] = true
	}

	return assemble(set, r, provided)
}

// assemble converts parsed values into a validated Config. It is separate from
// [Load] so the flag plumbing and the semantics can be read one at a time.
// provided names the flags the operator supplied, by flag or by environment
// variable; it is empty for a caller that supplied nothing.
func assemble(set *provider.Set, r raw, provided map[string]bool) (Config, error) {
	names := set.Names()
	listeners := make(map[provider.Name]*Listener, len(names))
	for _, name := range names {
		listeners[name] = &Listener{Port: *r.ports[name]}
	}

	cfg := Config{
		BindAddress:         strings.TrimSpace(r.bindAddress),
		Admin:               Listener{Port: r.adminPort, Enabled: true},
		Set:                 set,
		Listeners:           listeners,
		order:               names,
		ScenarioPath:        strings.TrimSpace(r.scenario),
		ScenarioRoot:        strings.TrimSpace(r.scenarioRoot),
		ScenarioDir:         strings.TrimSpace(r.scenarioDir),
		MaxNamespaces:       r.maxNamespaces,
		MaxJobs:             r.maxJobs,
		JournalCapacity:     r.journalCapacity,
		MaxRequestBytes:     r.maxRequestBytes,
		MaxJournalBodyBytes: r.maxJournalBodyBytes,
		ReadHeaderTimeout:   r.readHeaderTimeout,
		ShutdownGrace:       r.shutdownGrace,
		LogFormat:           strings.ToLower(strings.TrimSpace(r.logFormat)),
		StrictAuth:          r.strictAuth,
		Healthcheck:         r.healthcheck,
		ShowVersion:         r.showVersion,
		PrintRoutes:         r.printRoutes,
		PrintPorts:          r.printPorts,
		PrintHosts:          r.printHosts,
	}

	if err := cfg.enable(r.providers); err != nil {
		return Config{}, err
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(strings.TrimSpace(r.logLevel))); err != nil {
		return Config{}, fmt.Errorf("--log-level %q: must be debug, info, warn or error", r.logLevel)
	}
	if err := cfg.normalizeScenarioSelection(provided); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	// Zero means "use the default" for the two byte limits. Substituting here
	// keeps a zero from reaching http.MaxBytesReader, where it rejects every
	// request, and from reaching the journal, where it retains no body at all.
	if cfg.MaxRequestBytes == 0 {
		cfg.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if cfg.MaxJournalBodyBytes == 0 {
		cfg.MaxJournalBodyBytes = DefaultMaxJournalBodyBytes
	}

	return cfg, nil
}

// enable turns a --providers value into listener state. An unknown name is an
// error rather than a silently ignored entry: "--providers exa,tavly" must not
// start a simulator that answers nothing on the Tavily port.
func (c *Config) enable(spec string) error {
	for _, field := range strings.Split(spec, ",") {
		name := strings.ToLower(strings.TrimSpace(field))
		if name == "" {
			continue
		}
		listener, ok := c.Listeners[provider.Name(name)]
		if !ok {
			return fmt.Errorf("--providers: unknown provider %q; this build simulates %s",
				name, defaultProvidersString(c.Set))
		}
		listener.Enabled = true
	}
	return nil
}

// normalizeScenarioSelection settles which of the two scenario modes this
// process runs, and rejects a combination that names both.
//
// The two are mutually exclusive because they answer the same question
// differently: --scenario names the one behaviour every request gets, while
// --scenario-dir loads several and lets /x/<name> choose. Honouring both would
// mean inventing a precedence rule, and the operator who typed both would
// discover which one lost by watching responses rather than by reading an error.
//
// Exclusivity is decided on what was provided rather than on the resolved
// values, because --scenario carries a built-in default that is always present.
// --scenario-root is rejected alongside it for the same reason: in directory
// mode the directory is its own containment root, so a supplied --scenario-root
// would be silently ignored, and a silently ignored containment flag is worse
// than an error.
func (c *Config) normalizeScenarioSelection(provided map[string]bool) error {
	if !provided["scenario-dir"] {
		return c.normalizeScenario()
	}
	if c.ScenarioDir == "" {
		return errors.New("--scenario-dir: must not be empty")
	}
	if provided["scenario"] {
		return errors.New("--scenario and --scenario-dir are mutually exclusive: " +
			"--scenario-dir serves every scenario in the directory, selected by the /x/<scenario> path prefix")
	}
	if provided["scenario-root"] {
		return errors.New("--scenario-root: has no meaning with --scenario-dir, " +
			"which is its own containment root")
	}

	// There is no single startup scenario in directory mode, so ScenarioPath is
	// cleared rather than left holding the default a caller would then load.
	c.ScenarioPath = ""
	c.ScenarioRoot = ""
	c.ScenarioDir = filepath.Clean(c.ScenarioDir)
	return nil
}

// normalizeScenario applies the ScenarioRoot default. A built-in never touches
// the file system, so it gets no root at all rather than a meaningless one
// derived from the "builtin:" literal.
func (c *Config) normalizeScenario() error {
	if name, ok := c.Builtin(); ok {
		if !scenarios.Has(name) {
			return fmt.Errorf("--scenario %q: unknown built-in scenario %q; this build ships %s",
				c.ScenarioPath, name, strings.Join(scenarios.Names(), ", "))
		}
		c.ScenarioRoot = ""
		return nil
	}
	if c.ScenarioPath == "" {
		return errors.New("--scenario: must not be empty")
	}
	if c.ScenarioRoot == "" {
		c.ScenarioRoot = filepath.Dir(c.ScenarioPath)
	}
	return nil
}

// validate rejects every value that would fail later as a panic, a silent
// rejection of all traffic, or an exposure the operator did not ask for.
//
// Every listener's port is range-checked, enabled or not — an operator who
// mistypes a disabled listener's port should not have to enable it first to
// find out. Duplicate ports are checked only across the admin listener and
// the ENABLED provider listeners, because those are the only ones that
// actually bind: two disabled listeners sharing a port bind nothing and
// collide with nothing, and port 0 (ask the OS for an ephemeral one) is
// never a collision no matter how many listeners ask for it.
func (c Config) validate() error {
	if c.BindAddress == "" {
		return errors.New("--bind-address: must not be empty; use 0.0.0.0 to bind every interface")
	}
	if c.Admin.Port < 0 || c.Admin.Port > 65535 {
		return fmt.Errorf("--admin-port: must be between 0 and 65535, got %d", c.Admin.Port)
	}

	claimed := map[int]string{}
	if c.Admin.Port != 0 {
		claimed[c.Admin.Port] = "--admin-port"
	}
	for _, name := range c.order {
		l := c.Listeners[name]
		flagName := "--" + string(name) + "-port"
		if l.Port < 0 || l.Port > 65535 {
			return fmt.Errorf("%s: must be between 0 and 65535, got %d", flagName, l.Port)
		}
		if !l.Enabled || l.Port == 0 {
			continue
		}
		if other, dup := claimed[l.Port]; dup {
			return fmt.Errorf("%s and %s both bind port %d", other, flagName, l.Port)
		}
		claimed[l.Port] = flagName
	}

	// Zero is rejected rather than read as "unlimited" or "the default": every
	// request belongs to a namespace even when it names none, so a limit of zero
	// would reject all traffic, and an unbounded namespace map is the growth
	// surface --max-namespaces exists to close.
	if c.MaxNamespaces < 1 {
		return fmt.Errorf("--max-namespaces: must be at least 1, got %d", c.MaxNamespaces)
	}
	// Zero would refuse every create, for the same reason as --max-namespaces
	// above: it reads as "no jobs allowed" rather than "unlimited" or "the
	// default", and a typo here should fail loudly at startup rather than as an
	// unexplained refusal on the process's very first create.
	if c.MaxJobs < 1 {
		return fmt.Errorf("--max-jobs: must be at least 1, got %d", c.MaxJobs)
	}
	if c.JournalCapacity < 0 {
		return fmt.Errorf("--journal-capacity: must not be negative, got %d", c.JournalCapacity)
	}
	if c.MaxRequestBytes < 0 {
		return fmt.Errorf("--max-request-bytes: must not be negative, got %d", c.MaxRequestBytes)
	}
	if c.MaxJournalBodyBytes < 0 {
		return fmt.Errorf("--max-journal-body-bytes: must not be negative, got %d", c.MaxJournalBodyBytes)
	}
	if c.ReadHeaderTimeout < 0 {
		return fmt.Errorf("--read-header-timeout: must not be negative, got %s", c.ReadHeaderTimeout)
	}
	if c.ShutdownGrace < 0 {
		return fmt.Errorf("--shutdown-grace: must not be negative, got %s", c.ShutdownGrace)
	}
	if c.LogFormat != LogFormatJSON && c.LogFormat != LogFormatText {
		return fmt.Errorf("--log-format %q: must be %s or %s", c.LogFormat, LogFormatJSON, LogFormatText)
	}
	return nil
}

// Listener returns the listener a provider name binds, and whether name is
// one this build simulates. It is the fail-closed hook every miss in this
// package ultimately reduces to: a name absent from the registered Set gets
// ok=false, never a zero-value Listener a caller could mistake for a real,
// disabled one.
func (c Config) Listener(name provider.Name) (Listener, bool) {
	l, ok := c.Listeners[name]
	if !ok {
		return Listener{}, false
	}
	return *l, true
}

// Enabled returns the providers that have a listener enabled, in registration
// order — never a map range over Listeners, which would make listener
// startup order, and any message that lists providers, differ between runs.
func (c Config) Enabled() []provider.Name {
	out := make([]provider.Name, 0, len(c.order))
	for _, name := range c.order {
		if l := c.Listeners[name]; l != nil && l.Enabled {
			out = append(out, name)
		}
	}
	return out
}

// Addr returns the address a listener binds, as host:port.
func (c Config) Addr(l Listener) string {
	return net.JoinHostPort(c.BindAddress, strconv.Itoa(l.Port))
}

// HealthcheckURL returns the URL --healthcheck probes. It dials loopback rather
// than BindAddress because the image sets SERVICESIM_BIND_ADDRESS=0.0.0.0, which
// names every interface to a listener and is not a destination to dial.
func (c Config) HealthcheckURL() string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Admin.Port)) + "/healthz"
}
