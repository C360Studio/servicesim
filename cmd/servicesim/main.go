package main

import (
	"context"
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
	"syscall"
	"time"

	"github.com/c360studio/servicesim/internal/config"
	"github.com/c360studio/servicesim/internal/server"
)

// Version is the release this binary was built from. The build injects it with
// -ldflags "-X main.Version=...", and the Dockerfile, the Taskfile and the CI
// build job all name exactly this symbol; renaming it does not break the build,
// it silently reverts every release binary to reporting "dev", which is the kind
// of defect that is only noticed from a bug report against the wrong version.
var Version = "dev"

// GitCommit is the commit this binary was built from, injected with
// -ldflags "-X main.GitCommit=...".
var GitCommit = "unknown"

// BuildTime is when this binary was built, injected with
// -ldflags "-X main.BuildTime=...". It is reported by --version and read nowhere
// else: it is constant for a given build, but a build timestamp still has no
// business anywhere near a simulated wire response.
var BuildTime = "unknown"

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
// HEALTHCHECK --timeout=3s so that a wedged instance is reported by this process
// as an explicit failure with a message, rather than by Docker killing the probe
// and leaving no explanation in the container's health log.
const healthcheckTimeout = 2 * time.Second

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

// run is main with its process-global inputs and outputs injected, which is what
// makes every mode of the binary testable in-process: no subprocess, no
// os.Exit, no t.Setenv serialising the package's tests.
//
// lookupEnv is threaded through to config.Load for the same reason that function
// takes it rather than calling os.Getenv itself.
func run(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stdout, stderr io.Writer) int {
	cfg, err := config.Load(args, lookupEnv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage())
			return exitOK
		}
		fmt.Fprintf(stderr, "servicesim: %v\n", err)
		fmt.Fprintln(stderr, `servicesim: run "servicesim --help" for the full flag list`)
		return usageExitCode(args)
	}

	switch {
	case cfg.ShowVersion:
		fmt.Fprint(stdout, versionText())
		return exitOK
	case cfg.Healthcheck:
		return healthcheck(ctx, cfg, stderr)
	default:
		return serve(ctx, cfg, stderr)
	}
}

// usageExitCode picks the exit code for a configuration error.
//
// A health probe reports only 0 or 1, because Docker reserves 2, and a
// configuration error is reachable in probe mode: SERVICESIM_ADMIN_PORT=abc in
// the container's environment fails inside config.Load, before anything knows
// which mode this invocation is. Scanning args is complete rather than a
// heuristic — --healthcheck is deliberately flag-only, so it cannot arrive by
// any other route.
func usageExitCode(args []string) int {
	if slices.Contains(args, "--healthcheck") || slices.Contains(args, "-healthcheck") {
		return exitFailure
	}
	return exitUsage
}

// serve loads the scenario, binds every enabled listener, and blocks until a
// signal or a listener failure.
//
// Signal handling is installed after server.New rather than before it, so that
// a signal arriving during scenario load keeps its default disposition and kills
// the process outright. That is the correct behaviour while nothing is bound:
// capturing the signal earlier would only mean holding it until Run started, and
// dropping it entirely if New failed.
func serve(ctx context.Context, cfg config.Config, stderr io.Writer) int {
	logger := server.NewLogger(cfg, stderr)

	srv, err := server.New(cfg, logger, server.WithVersion(Version))
	if err != nil {
		// Logged rather than printed. Every scenario finding that explains this
		// failure has already gone through the same logger in the same format;
		// splitting the explanation across two streams makes a container log
		// that cannot be read as one document.
		logger.Error("server.start_failed", slog.String("error", err.Error()))
		return exitFailure
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("server.starting",
		slog.String("version", Version),
		slog.String("commit", GitCommit),
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
// SERVICESIM_BIND_ADDRESS=0.0.0.0, which names every interface to a listener and
// is not an address to connect to.
func healthcheck(ctx context.Context, cfg config.Config, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()

	url := cfg.HealthcheckURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(stderr, "servicesim: healthcheck: %v\n", err)
		return exitFailure
	}

	resp, err := healthcheckClient().Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "servicesim: healthcheck %s: %v\n", url, err)
		return exitFailure
	}
	// The body is closed without being drained because keep-alives are off: the
	// connection is not going to be reused, and this process is about to exit.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "servicesim: healthcheck %s: HTTP %d\n", url, resp.StatusCode)
		return exitFailure
	}
	return exitOK
}

// healthcheckClient builds the probe's HTTP client. Three of its settings are
// deliberate departures from the defaults.
//
// Proxy is nil. http.DefaultTransport honours HTTP_PROXY and HTTPS_PROXY, so a
// proxy variable in the container's environment would send a loopback health
// probe out to a network host — the exact "Servicesim never dials outward"
// failure that scripts/lint-no-live-hosts.sh exists to prevent, arriving through
// the one component whose whole job is to talk to localhost.
//
// Redirects are not followed, for the same reason: a 3xx is returned as-is and
// fails the probe, rather than being a way out of loopback.
//
// Keep-alives are disabled. The process exits immediately after one request, so
// a pooled connection would only leave the server holding a half-open socket for
// every health check, every ten seconds, for the container's lifetime.
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

// versionText is what --version prints. The three injected variables come first
// and are always printed, even at their defaults, because "dev"/"unknown" is
// itself the answer to "which build is this?" — omitting them would leave a
// reader unable to tell an unstamped local build from a release.
func versionText() string {
	return fmt.Sprintf("servicesim %s\n  commit    %s\n  built     %s\n  go        %s\n  platform  %s/%s\n",
		Version, GitCommit, BuildTime, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// usage is the --help text. The flag list itself comes from config.Usage so that
// this file cannot describe a flag set that differs from the one config.Load
// parses.
func usage() string {
	return "servicesim simulates the Exa, Tavily and Perplexity research APIs and an MCP server\n" +
		"deterministically, offline, and without credentials.\n\n" +
		"Usage:\n" +
		"  servicesim [flags]              serve until SIGINT or SIGTERM\n" +
		"  servicesim --version            print build information and exit\n" +
		"  servicesim --healthcheck        probe a running instance's admin port and exit 0 or 1\n\n" +
		"Flags:\n" +
		config.Usage()
}
