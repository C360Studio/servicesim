package servicesim

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/provider/exa"
	"github.com/c360studio/servicesim/provider/mcp"
	"github.com/c360studio/servicesim/provider/perplexity"
	"github.com/c360studio/servicesim/provider/tavily"
	"github.com/c360studio/servicesim/scenario"
)

// noEnv is the hermetic environment every test runs with. Run threads it into
// config.Load, so a developer with SERVICESIM_* exported in their shell gets
// the same results as CI, and these tests can run in parallel because none of
// them touches process state.
func noEnv(string) (string, bool) { return "", false }

// referenceSet builds the Set every test in this file invokes against: the
// four reference profiles, exactly what cmd/servicesim registers. It is
// rebuilt per call rather than shared so tests stay parallel-safe with no
// risk of one test's Lookup/All clone reaching another's.
func referenceSet(t *testing.T) *provider.Set {
	t.Helper()
	set, err := provider.NewSet(exa.Profile(), tavily.Profile(), perplexity.Profile(), mcp.Profile())
	require.NoError(t, err)
	return set
}

// testBuild is the Build every test not specifically about version stamping
// invokes with. Program is deliberately NOT "servicesim": every literal
// "servicesim" this package's source could fall back to (in Run's error
// prefix, its --help hint, or versionText) would still read as correct
// output if this helper used the real product name, so a regression to a
// hard-coded literal would pass unnoticed. "testsim" makes Build.Program
// distinguishable from the string that would leak if the parameterisation
// (deliverable 3: "Program replaces the literal \"servicesim\" in
// usage/help/error prefixes") ever regressed. Version/Commit/BuiltAt are
// left empty, which versionText and the server package both already
// default ("dev"/"unknown") — see [versionText] and server.DefaultVersion.
func testBuild() Build { return Build{Program: "testsim"} }

// invoke runs Run in-process and returns its exit code with whatever it wrote
// to each stream. Each call builds its own referenceSet, so nothing here is
// shared state between parallel subtests.
func invoke(ctx context.Context, t *testing.T, b Build, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Run(ctx, b, referenceSet(t), args, noEnv, &out, &errOut)
	return code, out.String(), errOut.String()
}

// cancelled returns a context that is already done, which is how the serving
// tests below reach shutdown without sleeping or sending a real signal: Run
// binds every listener, observes the done context and drains. A test that
// waited on a timer for "the server is probably up now" would be a future
// flake.
func cancelled(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// freePort returns a port that was bound and released, plus a closed
// listener's address. It is used to point the health probe at nothing
// listening.
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
	t.Parallel()

	b := Build{Program: "servicesim", Version: "v1.2.3", Commit: "abc1234", BuiltAt: "2026-08-14T00:00:00Z"}
	code, stdout, stderr := invoke(context.Background(), t, b, "--version")

	require.Equal(t, exitOK, code, "CI's build job gates on --version exiting 0")
	require.Empty(t, stderr)
	for _, want := range []string{"v1.2.3", "abc1234", "2026-08-14T00:00:00Z"} {
		require.Contains(t, stdout, want)
	}
}

func TestVersionReportsDefaultsForAnUnstampedBuild(t *testing.T) {
	t.Parallel()

	code, stdout, _ := invoke(context.Background(), t, testBuild(), "--version")

	require.Equal(t, exitOK, code)
	require.Contains(t, stdout, "testsim dev",
		"an unstamped build must still say which build it is, under its own Program, not a literal \"servicesim\"")
}

func TestHelpExitsZeroAndListsTheRealFlagSet(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := invoke(context.Background(), t, testBuild(), "--help")

	require.Equal(t, exitOK, code, "--help is a request, not an error")
	require.Empty(t, stderr)
	for _, want := range []string{"-scenario", "-admin-port", "-healthcheck", "-version", "-print-routes", "-print-ports", "-print-hosts"} {
		require.Contains(t, stdout, want)
	}
}

func TestHelpBannerNamesProgramAndDerivesFromTheSet(t *testing.T) {
	t.Parallel()

	code, stdout, _ := invoke(context.Background(), t, Build{Program: "acmesim"}, "--help")

	require.Equal(t, exitOK, code)
	require.Contains(t, stdout, "acmesim simulates",
		"the banner names Build.Program, not a literal \"servicesim\"")
	require.Contains(t, stdout, "Exa search and answer API",
		"the banner is derived from the set's own Profile.Summary lines")
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

			code, stdout, stderr := invoke(context.Background(), t, testBuild(), tc.args...)

			require.Equal(t, exitUsage, code)
			require.Empty(t, stdout, "diagnostics belong on stderr")
			require.True(t, strings.HasPrefix(stderr, "testsim: "),
				"the error must be prefixed with Build.Program, not a literal \"servicesim\": got %q", stderr)
			require.Contains(t, stderr, tc.want)
			require.Contains(t, stderr, "testsim --help",
				"the help hint must also name Build.Program, not a literal \"servicesim\"")
		})
	}
}

// Docker documents exit code 2 as reserved for HEALTHCHECK, so a probe that
// fails before its configuration resolved must still report 1.
func TestProbeNeverReportsTheReservedExitCode(t *testing.T) {
	t.Parallel()

	code, _, stderr := invoke(context.Background(), t, testBuild(), "--healthcheck", "--admin-port", "-1")

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

			code, stdout, _ := invoke(context.Background(), t, testBuild(),
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

	code, _, stderr := invoke(context.Background(), t, testBuild(),
		"--healthcheck", "--admin-port", strconv.Itoa(freePort(t)))

	require.Equal(t, exitFailure, code)
	require.Contains(t, stderr, "healthcheck")
}

func TestServeBindsTheBuiltinScenarioAndShutsDownCleanly(t *testing.T) {
	t.Parallel()

	code, _, stderr := invoke(cancelled(t), t, testBuild(),
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

	code, _, stderr := invoke(cancelled(t), t, testBuild(),
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

	code, _, stderr := invoke(cancelled(t), t, testBuild(),
		"--scenario", "builtin:happy",
		"--providers", "",
		"--admin-port", strconv.Itoa(ln.Addr().(*net.TCPAddr).Port),
	)

	require.Equal(t, exitFailure, code)
	require.Contains(t, stderr, "server.run_failed",
		"a port collision must fail fast and name the port, never retry into a startup hang")
}

func TestPrintRoutesListsEveryRegisteredRoute(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := invoke(context.Background(), t, testBuild(), "--print-routes")

	require.Equal(t, exitOK, code)
	require.Empty(t, stderr)

	// Built from referenceSet directly rather than a hand-copied literal
	// list, so this pins the full contract deliverable 1 promises — stable
	// order (registration, then pattern within a profile), exactly one line
	// per route, no duplicates — not just "every wanted substring appears
	// somewhere". A reversed profile order, a duplicated line or a
	// descending within-profile sort all fail this, where a bare Contains
	// check would not have.
	var want []string
	for _, p := range referenceSet(t).All() {
		routes := slices.Clone(p.Routes)
		slices.SortFunc(routes, func(a, b provider.Route) int { return strings.Compare(a.Pattern, b.Pattern) })
		for _, r := range routes {
			want = append(want, r.Pattern)
		}
	}
	got := strings.Split(strings.TrimSpace(stdout), "\n")
	require.Equal(t, want, got)
}

func TestPrintPortsListsEveryRegisteredListenerAsJSON(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := invoke(context.Background(), t, testBuild(), "--print-ports")

	require.Equal(t, exitOK, code)
	require.Empty(t, stderr)

	// Decoded and compared row-for-row against referenceSet, rather than a
	// handful of Contains checks, so this pins registration order and the
	// DefaultAuth fallback (an empty Profile.DefaultAuth resolves to
	// "required", per exa/tavily/perplexity below) together — the exact
	// shape docker-compose.example.yml's <NAME>_API_KEY rows are generated
	// from (deliverable 4).
	type row struct {
		Name        string `json:"name"`
		Port        int    `json:"port"`
		DefaultAuth string `json:"default_auth"`
	}
	var got []row
	require.NoError(t, json.Unmarshal([]byte(stdout), &got))

	want := make([]row, 0)
	for _, p := range referenceSet(t).All() {
		auth := string(p.DefaultAuth)
		if auth == "" {
			auth = string(scenario.AuthRequired)
		}
		want = append(want, row{Name: string(p.Name), Port: p.Port, DefaultAuth: auth})
	}
	require.Equal(t, want, got)
}

func TestPrintHostsListsEveryRegisteredHostSortedAndDeduplicated(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := invoke(context.Background(), t, testBuild(), "--print-hosts")

	require.Equal(t, exitOK, code)
	require.Empty(t, stderr)
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	require.Contains(t, lines, "api.exa.ai") // servicesim:allow-live-host — asserting the guard's own denylist entry
	require.True(t, slices.IsSorted(lines), "print-hosts output must already be sorted")

	seen := make(map[string]struct{}, len(lines))
	for _, h := range lines {
		_, dup := seen[h]
		require.False(t, dup, "print-hosts output must be deduplicated, saw %q twice", h)
		seen[h] = struct{}{}
	}
}

// The ldflags variable names are a contract with the build, not an internal
// detail: nothing fails to compile if servicesim.Version is renamed, every
// release binary just quietly reports "dev". This test is the only thing
// that notices.
func TestBuildFilesInjectOnlyDeclaredVariables(t *testing.T) {
	t.Parallel()

	// declared holds pointers to the real package vars, not a hand-kept name
	// list: if Version, GitCommit or BuildTime ever moves out of this
	// package (back to cmd/servicesim, say), this file fails to compile
	// instead of the test silently continuing to pass against a stale map.
	declared := map[string]*string{"Version": &Version, "GitCommit": &GitCommit, "BuildTime": &BuildTime}
	injection := regexp.MustCompile(`-X github\.com/c360studio/servicesim\.([A-Za-z0-9_]+)=`)

	for _, rel := range []string{"Dockerfile", "Taskfile.yml", ".github/workflows/ci.yml"} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()

			src, err := os.ReadFile(rel)
			require.NoError(t, err)

			matches := injection.FindAllStringSubmatch(string(src), -1)
			require.NotEmpty(t, matches, "%s must stamp the binary it builds", rel)
			for _, m := range matches {
				_, ok := declared[m[1]]
				require.True(t, ok,
					"%s injects github.com/c360studio/servicesim.%s, which this package does not declare", rel, m[1])
			}
		})
	}

	src, err := os.ReadFile("Dockerfile")
	require.NoError(t, err)
	for name := range declared {
		require.Contains(t, string(src), "-X github.com/c360studio/servicesim."+name+"=",
			"the image must stamp every build variable --version reports")
	}
}
