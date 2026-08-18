package servicesim

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/c360studio/servicesim/internal/config"
	"github.com/c360studio/servicesim/internal/server"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Version is the release this binary was built from. The build injects it
// with -ldflags "-X github.com/c360studio/servicesim.Version=...", and the
// Dockerfile, the Taskfile and the CI build job all name exactly this symbol;
// renaming it does not break the build, it silently reverts every release
// binary to reporting "dev", which is the kind of defect that is only
// noticed from a bug report against the wrong version.
var Version = "dev"

// GitCommit is the commit this binary was built from, injected with
// -ldflags "-X github.com/c360studio/servicesim.GitCommit=...".
var GitCommit = "unknown"

// BuildTime is when this binary was built, injected with
// -ldflags "-X github.com/c360studio/servicesim.BuildTime=...". It is
// reported by --version and read nowhere else: it is constant for a given
// build, but a build timestamp still has no business anywhere near a
// simulated wire response.
var BuildTime = "unknown"

// Build names the binary and the release it was built from. Program exists
// because an adopter's own binary is not named "servicesim" — every usage
// line, --help banner and error prefix reads Program, not a literal, so a
// consumer composing "acmesim" gets their own name back rather than ours.
type Build struct {
	// Program is the binary's own name, used in usage text, the --help
	// banner and every error prefix. cmd/servicesim passes "servicesim".
	Program string

	// Version, Commit and BuiltAt are normally [Version], [GitCommit] and
	// [BuildTime] — the ldflags-injected package vars — passed through by the
	// caller. They are fields rather than read directly so [Run] stays
	// testable with values a build never actually injected.
	Version string
	Commit  string
	BuiltAt string
}

// Process exit codes.
//
// Docker documents exit code 2 as reserved for HEALTHCHECK, so a probe
// invocation must resolve to 0 or 1 whatever goes wrong — see [usageExitCode].
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// healthcheckTimeout bounds the probe. It is under the image's
// HEALTHCHECK --timeout=3s so that a wedged instance is reported by this
// process as an explicit failure with a message, rather than by Docker
// killing the probe and leaving no explanation in the container's health log.
const healthcheckTimeout = 2 * time.Second

// Main is the entry point a consumer's main() calls: os.Exit(servicesim.Main(b, set)).
// It supplies Run's process-global inputs — os.Args, os.LookupEnv,
// os.Stdout/os.Stderr and a background context whose cancellation Run
// installs its own signal handling on top of (see [Run]'s doc comment) — so
// nothing in this package touches process state except through this one
// function.
func Main(b Build, set *provider.Set) int {
	return Run(context.Background(), b, set, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr)
}

// Run is main with every process-global input injected, which is what makes
// every mode of the binary testable in-process: no subprocess, no os.Exit, no
// t.Setenv serialising the caller's tests.
//
// lookupEnv is threaded through to config.Load for the same reason that
// function takes it rather than calling os.Getenv itself.
func Run(ctx context.Context, b Build, set *provider.Set, args []string, lookupEnv func(string) (string, bool), stdout, stderr io.Writer) int {
	cfg, err := config.Load(set, args, lookupEnv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage(b, set))
			return exitOK
		}
		fmt.Fprintf(stderr, "%s: %v\n", b.Program, err)
		fmt.Fprintf(stderr, "%s: run \"%s --help\" for the full flag list\n", b.Program, b.Program)
		return usageExitCode(args)
	}

	switch {
	case cfg.ShowVersion:
		fmt.Fprint(stdout, versionText(b))
		return exitOK
	case cfg.PrintRoutes:
		return printRoutes(stdout, set)
	case cfg.PrintPorts:
		return printPorts(stdout, cfg)
	case cfg.PrintHosts:
		return printHosts(stdout, set)
	case cfg.Healthcheck:
		return healthcheck(ctx, b, cfg, stderr)
	default:
		return serve(ctx, b, cfg, stderr)
	}
}

// usageExitCode picks the exit code for a configuration error.
//
// A health probe reports only 0 or 1, because Docker reserves 2, and a
// configuration error is reachable in probe mode: SERVICESIM_ADMIN_PORT=abc
// in the container's environment fails inside config.Load, before anything
// knows which mode this invocation is. Scanning args is complete rather than
// a heuristic — --healthcheck is deliberately flag-only, so it cannot arrive
// by any other route.
func usageExitCode(args []string) int {
	if slices.Contains(args, "--healthcheck") || slices.Contains(args, "-healthcheck") {
		return exitFailure
	}
	return exitUsage
}

// serve loads the scenario, binds every enabled listener, and blocks until a
// signal or a listener failure.
//
// Signal handling is installed after server.New rather than before it, so
// that a signal arriving during scenario load keeps its default disposition
// and kills the process outright. That is the correct behaviour while
// nothing is bound: capturing the signal earlier would only mean holding it
// until Run started, and dropping it entirely if New failed.
func serve(ctx context.Context, b Build, cfg config.Config, stderr io.Writer) int {
	logger := server.NewLogger(cfg, stderr)

	srv, err := server.New(cfg, logger, server.WithVersion(b.Version))
	if err != nil {
		// Logged rather than printed. Every scenario finding that explains
		// this failure has already gone through the same logger in the same
		// format; splitting the explanation across two streams makes a
		// container log that cannot be read as one document.
		logger.Error("server.start_failed", slog.String("error", err.Error()))
		return exitFailure
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("server.starting",
		slog.String("version", stampedOrDefault(b.Version, "dev")),
		slog.String("commit", stampedOrDefault(b.Commit, "unknown")),
		slog.String("scenario", cfg.ScenarioPath),
		slog.String("bind_address", cfg.BindAddress))

	if err := srv.Run(ctx); err != nil {
		logger.Error("server.run_failed", slog.String("error", err.Error()))
		return exitFailure
	}

	logger.Info("server.stopped")
	return exitOK
}

// healthcheck performs one GET against the running instance's admin health
// endpoint and reports 0 for healthy, 1 for anything else.
//
// The URL comes from config rather than being composed here, so the port the
// probe dials and the port the admin listener binds cannot drift apart. It
// dials loopback, never the configured bind address: the image sets
// SERVICESIM_BIND_ADDRESS=0.0.0.0, which names every interface to a listener
// and is not an address to connect to.
func healthcheck(ctx context.Context, b Build, cfg config.Config, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()

	url := cfg.HealthcheckURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(stderr, "%s: healthcheck: %v\n", b.Program, err)
		return exitFailure
	}

	resp, err := healthcheckClient().Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "%s: healthcheck %s: %v\n", b.Program, url, err)
		return exitFailure
	}
	// The body is closed without being drained because keep-alives are off:
	// the connection is not going to be reused, and this process is about to
	// exit.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "%s: healthcheck %s: HTTP %d\n", b.Program, url, resp.StatusCode)
		return exitFailure
	}
	return exitOK
}

// healthcheckClient builds the probe's HTTP client. Three of its settings are
// deliberate departures from the defaults.
//
// Proxy is nil. http.DefaultTransport honours HTTP_PROXY and HTTPS_PROXY, so
// a proxy variable in the container's environment would send a loopback
// health probe out to a network host — the exact "Servicesim never dials
// outward" failure that scripts/lint-no-live-hosts.sh exists to prevent,
// arriving through the one component whose whole job is to talk to
// localhost.
//
// Redirects are not followed, for the same reason: a 3xx is returned as-is
// and fails the probe, rather than being a way out of loopback.
//
// Keep-alives are disabled. The process exits immediately after one request,
// so a pooled connection would only leave the server holding a half-open
// socket for every health check, every ten seconds, for the container's
// lifetime.
func healthcheckClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// stampedOrDefault substitutes fallback for an empty ldflags-injected field.
// versionText and serve's server.starting log both report the same three
// Build fields, so they share this rather than each answering "which build is
// this?" with a different default for the same empty string.
func stampedOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// versionText is what --version prints. The three injected fields come first
// and are always printed, even at their defaults, because "dev"/"unknown" is
// itself the answer to "which build is this?" — omitting them would leave a
// reader unable to tell an unstamped local build from a release.
func versionText(b Build) string {
	version := stampedOrDefault(b.Version, "dev")
	commit := stampedOrDefault(b.Commit, "unknown")
	builtAt := stampedOrDefault(b.BuiltAt, "unknown")
	return fmt.Sprintf("%s %s\n  commit    %s\n  built     %s\n  go        %s\n  platform  %s/%s\n",
		b.Program, version, commit, builtAt, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// summaryList joins every registered profile's Summary into one Oxford-comma
// clause for the --help banner — "the Exa search and answer API, the Tavily
// search API, the Perplexity Sonar and Agent APIs, and a Model Context
// Protocol Streamable HTTP server" for the reference Set. A profile with no
// Summary contributes nothing rather than an empty clause.
func summaryList(set *provider.Set) string {
	profiles := set.All()
	parts := make([]string, 0, len(profiles))
	for _, p := range profiles {
		if p.Summary != "" {
			parts = append(parts, p.Summary)
		}
	}
	switch len(parts) {
	case 0:
		return "no registered profiles"
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
}

// usage is the --help text. The flag list itself comes from config.Usage so
// that this file cannot describe a flag set that differs from the one
// config.Load parses. The banner is derived from set's own Title/Summary
// fields rather than naming a vendor here (docs/proposals/framework-seam.md,
// "servicesim — the composition root").
func usage(b Build, set *provider.Set) string {
	return fmt.Sprintf("%s simulates %s\n", b.Program, summaryList(set)) +
		"deterministically, offline, and without credentials.\n\n" +
		"Usage:\n" +
		fmt.Sprintf("  %s [flags]              serve until SIGINT or SIGTERM\n", b.Program) +
		fmt.Sprintf("  %s --version            print build information and exit\n", b.Program) +
		fmt.Sprintf("  %s --healthcheck        probe a running instance's admin port and exit 0 or 1\n", b.Program) +
		fmt.Sprintf("  %s --print-routes       print every route this build serves and exit\n", b.Program) +
		fmt.Sprintf("  %s --print-ports        print every listener's resolved port as JSON and exit\n", b.Program) +
		fmt.Sprintf("  %s --print-hosts        print every registered profile's live hostnames and exit\n\n", b.Program) +
		"Flags:\n" +
		config.Usage(set)
}

// printRoutes writes one "METHOD /path" per line — [provider.Route.Pattern]
// is already in that shape — in the stable order [config.Load]'s deliverable
// promises: registration order across profiles, then pattern within one
// profile (a profile's own Routes() is written in the order its handler file
// declares them, not necessarily sorted, so this sorts a defensive copy
// rather than assuming the source is).
func printRoutes(stdout io.Writer, set *provider.Set) int {
	for _, p := range set.All() {
		routes := slices.Clone(p.Routes)
		slices.SortFunc(routes, func(a, b provider.Route) int { return strings.Compare(a.Pattern, b.Pattern) })
		for _, r := range routes {
			fmt.Fprintln(stdout, r.Pattern)
		}
	}
	return exitOK
}

// portRow is one entry of --print-ports' JSON array.
type portRow struct {
	Name        string `json:"name"`
	Port        int    `json:"port"`
	DefaultAuth string `json:"default_auth"`
}

// printPorts writes the resolved port of every REGISTERED listener (not only
// the enabled subset --providers selected), in registration order, as a JSON
// array. It is what generates the Dockerfile's EXPOSE list and
// docker-compose.example.yml's port map instead of hand-copying them, so it
// deliberately reports every listener this build could bind, not just the
// ones this particular invocation turned on.
func printPorts(stdout io.Writer, cfg config.Config) int {
	names := cfg.Set.Names()
	rows := make([]portRow, 0, len(names))
	for _, name := range names {
		l, ok := cfg.Listener(name)
		if !ok {
			continue // unreachable: cfg.Set.Names() and cfg.Listener share one Set
		}
		auth := string(scenario.AuthRequired)
		if p, ok := cfg.Set.Lookup(name); ok && p.DefaultAuth != "" {
			auth = string(p.DefaultAuth)
		}
		rows = append(rows, portRow{Name: string(name), Port: l.Port, DefaultAuth: auth})
	}
	// portRow is a plain struct of string/int fields, which encoding/json
	// never fails to marshal — the error is discarded rather than branched
	// on for a case that cannot occur.
	out, _ := json.Marshal(rows)
	fmt.Fprintln(stdout, string(out))
	return exitOK
}

// printHosts writes [provider.Set.LiveHosts], sorted and deduplicated, one
// per line. scripts/lint-no-live-hosts.sh unions its own hand-kept base list
// with this output, so a profile's Hosts travel into the guard automatically.
func printHosts(stdout io.Writer, set *provider.Set) int {
	hosts := slices.Clone(set.LiveHosts())
	slices.Sort(hosts)
	hosts = slices.Compact(hosts)
	for _, h := range hosts {
		fmt.Fprintln(stdout, h)
	}
	return exitOK
}
