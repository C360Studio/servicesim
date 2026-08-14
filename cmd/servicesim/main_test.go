package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// noEnv is the hermetic environment every test runs with. run threads it into
// config.Load, so a developer with SERVICESIM_* exported in their shell gets the
// same results as CI, and these tests can run in parallel because none of them
// touches process state.
func noEnv(string) (string, bool) { return "", false }

// invoke runs the binary's entry point in-process and returns its exit code with
// whatever it wrote to each stream.
func invoke(ctx context.Context, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(ctx, args, noEnv, &out, &errOut)
	return code, out.String(), errOut.String()
}

// cancelled returns a context that is already done, which is how the serving
// tests below reach shutdown without sleeping or sending a real signal: Run
// binds every listener, observes the done context and drains. A test that waited
// on a timer for "the server is probably up now" would be a future flake.
func cancelled(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// stubBuild sets the ldflags-injected variables for one test and restores them
// afterwards. Tests that call it must not run in parallel with each other, since
// the variables are package-level by necessity — ldflags cannot write anything
// else.
func stubBuild(t *testing.T, version, commit, built string) {
	t.Helper()
	oldVersion, oldCommit, oldBuilt := Version, GitCommit, BuildTime
	t.Cleanup(func() { Version, GitCommit, BuildTime = oldVersion, oldCommit, oldBuilt })
	Version, GitCommit, BuildTime = version, commit, built
}

// freePort returns a port that was bound and released, plus a closed listener's
// address. It is used to point the health probe at nothing listening.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// portOf extracts the port from an httptest server's URL.
func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	require.NoError(t, err)
	n, err := strconv.Atoi(port)
	require.NoError(t, err)
	return n
}

func TestVersionReportsEveryInjectedBuildVariable(t *testing.T) {
	stubBuild(t, "v1.2.3", "abc1234", "2026-08-14T00:00:00Z")

	code, stdout, stderr := invoke(context.Background(), "--version")

	require.Equal(t, exitOK, code, "CI's build job gates on --version exiting 0")
	require.Empty(t, stderr)
	for _, want := range []string{"v1.2.3", "abc1234", "2026-08-14T00:00:00Z"} {
		require.Contains(t, stdout, want)
	}
}

func TestVersionReportsDefaultsForAnUnstampedBuild(t *testing.T) {
	stubBuild(t, "dev", "unknown", "unknown")

	code, stdout, _ := invoke(context.Background(), "--version")

	require.Equal(t, exitOK, code)
	require.Contains(t, stdout, "servicesim dev",
		"an unstamped build must still say which build it is")
}

func TestHelpExitsZeroAndListsTheRealFlagSet(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := invoke(context.Background(), "--help")

	require.Equal(t, exitOK, code, "--help is a request, not an error")
	require.Empty(t, stderr)
	for _, want := range []string{"-scenario", "-admin-port", "-healthcheck", "-version"} {
		require.Contains(t, stdout, want)
	}
}

func TestConfigurationErrorsExitTwo(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		args []string
		want string
	}{
		"unknown flag":      {args: []string{"--nope"}, want: "nope"},
		"positional arg":    {args: []string{"serve"}, want: "serve"},
		"unknown provider":  {args: []string{"--providers", "exa,tavly"}, want: "tavly"},
		"bad log format":    {args: []string{"--log-format", "xml"}, want: "log-format"},
		"negative capacity": {args: []string{"--journal-capacity", "-1"}, want: "journal-capacity"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code, stdout, stderr := invoke(context.Background(), tc.args...)

			require.Equal(t, exitUsage, code)
			require.Empty(t, stdout, "diagnostics belong on stderr")
			require.Contains(t, stderr, tc.want)
			require.Contains(t, stderr, "--help")
		})
	}
}

// Docker documents exit code 2 as reserved for HEALTHCHECK, so a probe that
// fails before its configuration resolved must still report 1.
func TestProbeNeverReportsTheReservedExitCode(t *testing.T) {
	t.Parallel()

	code, _, stderr := invoke(context.Background(), "--healthcheck", "--admin-port", "-1")

	require.Equal(t, exitFailure, code)
	require.Contains(t, stderr, "admin-port")
}

func TestHealthcheckMapsTheAdminResponseToAnExitCode(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		status int
		want   int
	}{
		"healthy":     {status: http.StatusOK, want: exitOK},
		"not ready":   {status: http.StatusServiceUnavailable, want: exitFailure},
		"broken":      {status: http.StatusInternalServerError, want: exitFailure},
		"redirecting": {status: http.StatusFound, want: exitFailure},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Location", "http://example.test/elsewhere")
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			code, stdout, _ := invoke(context.Background(),
				"--healthcheck", "--admin-port", strconv.Itoa(portOf(t, srv.URL)))

			require.Equal(t, tc.want, code)
			require.Empty(t, stdout, "the probe's answer is its exit code, not its output")
			require.Equal(t, "/healthz", gotPath,
				"the probe must use the liveness endpoint, which answers before the provider listeners bind")
		})
	}
}

func TestHealthcheckFailsWhenNothingIsListening(t *testing.T) {
	t.Parallel()

	code, _, stderr := invoke(context.Background(),
		"--healthcheck", "--admin-port", strconv.Itoa(freePort(t)))

	require.Equal(t, exitFailure, code)
	require.Contains(t, stderr, "healthcheck")
}

func TestServeBindsTheBuiltinScenarioAndShutsDownCleanly(t *testing.T) {
	t.Parallel()

	code, _, stderr := invoke(cancelled(t),
		"--scenario", "builtin:happy",
		"--admin-port", "0",
		"--exa-port", "0",
		"--tavily-port", "0",
		"--perplexity-port", "0",
	)

	require.Equal(t, exitOK, code, "a shutdown after a done context is a success")
	require.Contains(t, stderr, "server.ready")
	require.Contains(t, stderr, "server.stopped")
}

func TestServeFailsBeforeBindingWhenTheScenarioIsInvalid(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "broken.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\nname: broken\nsources: [oops\n"), 0o600))

	code, _, stderr := invoke(cancelled(t),
		"--scenario", path,
		"--admin-port", "0",
		"--exa-port", "0",
		"--tavily-port", "0",
		"--perplexity-port", "0",
	)

	require.Equal(t, exitFailure, code,
		"a scenario that does not validate must fail the process, not serve a wrong contract")
	require.Contains(t, stderr, "server.start_failed")
	require.NotContains(t, stderr, "server.ready",
		"readiness must never be reached by a scenario that failed validation")
}

func TestServeFailsWhenTheAdminPortIsAlreadyBound(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	code, _, stderr := invoke(cancelled(t),
		"--scenario", "builtin:happy",
		"--providers", "",
		"--admin-port", strconv.Itoa(ln.Addr().(*net.TCPAddr).Port),
	)

	require.Equal(t, exitFailure, code)
	require.Contains(t, stderr, "server.run_failed",
		"a port collision must fail fast and name the port, never retry into a startup hang")
}

// The ldflags variable names are a contract with the build, not an internal
// detail: nothing fails to compile if main.Version is renamed, every release
// binary just quietly reports "dev". This test is the only thing that notices.
func TestBuildFilesInjectOnlyDeclaredVariables(t *testing.T) {
	t.Parallel()

	declared := map[string]bool{"Version": true, "GitCommit": true, "BuildTime": true}
	injection := regexp.MustCompile(`-X main\.([A-Za-z0-9_]+)=`)

	for _, rel := range []string{"Dockerfile", "Taskfile.yml", ".github/workflows/ci.yml"} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()

			src, err := os.ReadFile(filepath.Join("..", "..", rel))
			require.NoError(t, err)

			matches := injection.FindAllStringSubmatch(string(src), -1)
			require.NotEmpty(t, matches, "%s must stamp the binary it builds", rel)
			for _, m := range matches {
				require.True(t, declared[m[1]],
					"%s injects main.%s, which this package does not declare", rel, m[1])
			}
		})
	}

	src, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	require.NoError(t, err)
	for name := range declared {
		require.Contains(t, string(src), "-X main."+name+"=",
			"the image must stamp every build variable --version reports")
	}
}
