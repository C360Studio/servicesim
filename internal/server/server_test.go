package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/config"
	"github.com/c360studio/servicesim/provider"
)

// testKey is the credential every request in this package presents. It is a
// literal, not a secret: the point of the credential-free logging test below is
// that this exact string never reaches a log line.
const testKey = "servicesim-test-key"

// startTimeout bounds every wait on a server coming up or going down. It is
// generous because it only ever elapses when something is genuinely wedged; a
// healthy bind takes microseconds.
const startTimeout = 15 * time.Second

// -----------------------------------------------------------------------------
// Harness
// -----------------------------------------------------------------------------

// logBuffer collects log output that several goroutines write and the test
// goroutine reads. A bare bytes.Buffer is not safe for that, and the resulting
// race is real rather than an artefact of the test: the process logs from every
// request handler at once.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testConfig resolves a configuration with every listener on an ephemeral port,
// which is what lets these tests run in parallel with each other and with any
// other package's tests.
func testConfig(t *testing.T, args ...string) config.Config {
	t.Helper()
	base := []string{
		"--bind-address", "127.0.0.1",
		"--admin-port", "0",
		"--exa-port", "0",
		"--tavily-port", "0",
		"--perplexity-port", "0",
		"--shutdown-grace", "10s",
	}
	cfg, err := config.Load(append(base, args...), nil)
	require.NoError(t, err)
	return cfg
}

// harness is a Server running in its own goroutine, with the plumbing needed to
// tell "it came up" from "it failed to come up" without a sleep anywhere.
type harness struct {
	*Server
	cancel context.CancelFunc
	done   chan error
}

// newHarness constructs a Server and lets the caller adjust it before Run. The
// hook is what a test uses to observe connection state, which is the only
// sleep-free way to know that a request is genuinely in flight on the server
// side rather than merely written by the client.
func newHarness(t *testing.T, cfg config.Config, logger *slog.Logger, before func(*Server)) *harness {
	t.Helper()

	srv, err := New(cfg, logger)
	require.NoError(t, err)
	if before != nil {
		before(srv)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{Server: srv, cancel: cancel, done: make(chan error, 1)}
	go func() { h.done <- srv.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(startTimeout):
			t.Error("Run did not return after its context was cancelled")
		}
	})
	return h
}

// awaitStarted blocks until every listener is accepting, failing the test if the
// server exited first. Selecting on both is deliberate: a bind failure never
// closes Started, so waiting on it alone would hang for the whole test timeout
// and report nothing useful.
func (h *harness) awaitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-h.Started():
	case err := <-h.done:
		h.done <- err
		t.Fatalf("server exited before it started: %v", err)
	case <-time.After(startTimeout):
		t.Fatal("server did not start")
	}
}

// start is the common case: run the server and wait until it is accepting.
func start(t *testing.T, cfg config.Config, logger *slog.Logger) *harness {
	t.Helper()
	h := newHarness(t, cfg, logger, nil)
	h.awaitStarted(t)
	return h
}

// discard is the logger for tests that assert on behaviour rather than output.
func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// post sends one authenticated JSON request to a bound listener.
func post(t *testing.T, addr, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://"+addr+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// get sends one GET to a bound listener and returns the status and body.
func get(t *testing.T, addr, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+path, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}

// writeScenario writes a scenario file into a temporary directory and returns
// the flags that select it, so a test can exercise the mounted-file path rather
// than only the built-ins.
func writeScenario(t *testing.T, src string) []string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "scenario.yaml")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))
	return []string{"--scenario", path, "--scenario-root", dir}
}

// writeScenarioDir writes a directory of scenarios and returns the flags that
// select it. The keys are file names, because the file's base name is the
// /x/<scenario> selector a request names.
func writeScenarioDir(t *testing.T, files map[string]string) []string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600))
	}
	return []string{"--scenario-dir", dir}
}

// scenarioNamed builds a minimal three-provider scenario whose rendered bodies
// carry title, so a test can tell which scenario answered a request from the
// response alone.
func scenarioNamed(name, title string) string {
	return `
version: 1
name: ` + name + `
time:
  base: 2026-01-01T00:00:00Z
sources:
  - id: source-a
    url: https://example.test/report-a
    title: ` + title + `
providers:
  exa:
    results:
      - source: source-a
  tavily:
    answer: A short synthesis.
    results:
      - source: source-a
        score: 0.98
`
}

// postBody sends one authenticated JSON request and returns the status and the
// response body, which is what a test asserting on a provider-shaped error needs.
func postBody(t *testing.T, addr, path, body string) (int, string) {
	t.Helper()
	resp := post(t, addr, path, body)
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(out)
}

// surfaceRequest is one provider call: which listener, which route, what body.
type surfaceRequest struct {
	surface string
	path    string
	body    string
}

// everySurface is one well-formed request per simulated route. Both Exa routes
// and both Perplexity surfaces are here, because "every listener answers" is not
// the same claim as "every route answers".
var everySurface = []surfaceRequest{
	{string(provider.Exa), "/search", `{"query":"report a"}`},
	{string(provider.Exa), "/answer", `{"query":"report a"}`},
	{string(provider.Tavily), "/search", `{"query":"report a"}`},
	{string(provider.Perplexity), "/v1/sonar", `{"model":"sonar","messages":[{"role":"user","content":"report a"}]}`},
	{string(provider.Perplexity), "/v1/agent", `{"input":"report a","model":"openai/gpt-5"}`},
}

// -----------------------------------------------------------------------------
// Composition
// -----------------------------------------------------------------------------

// TestListenersRunConcurrentlyInOneProcess is the whole point of the package:
// Exa and Tavily both serve POST /search, so they cannot share a listener, and a
// fusion consumer calls all of them from one process against one journal.
//
// The requests are issued concurrently rather than serially because serial
// success would not distinguish "all listeners are up" from "each one is up in
// turn", and concurrency is what a fusion adapter actually does.
func TestListenersRunConcurrentlyInOneProcess(t *testing.T) {
	t.Parallel()

	h := start(t, testConfig(t), discard())

	adminStatus, _ := get(t, h.Addr(SurfaceAdmin), "/readyz")
	require.Equal(t, http.StatusOK, adminStatus, "readiness must be true once every listener is accepting")

	var wg sync.WaitGroup
	statuses := make([]int, len(everySurface))
	for i, call := range everySurface {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addr := h.Addr(call.surface)
			if addr == "" {
				return
			}
			statuses[i] = post(t, addr, call.path, call.body).StatusCode
		}()
	}
	wg.Wait()

	for i, call := range everySurface {
		require.Equalf(t, http.StatusOK, statuses[i], "%s %s", call.surface, call.path)
	}

	// One journal across every listener is what lets a consumer prove its three
	// provider calls belonged to one run.
	status, body := get(t, h.Addr(SurfaceAdmin), "/__admin/requests")
	require.Equal(t, http.StatusOK, status)
	for _, name := range []string{"exa", "tavily", "perplexity"} {
		require.Containsf(t, string(body), `"provider":"`+name+`"`, "journal is missing %s", name)
	}
}

// TestOnlySelectedProvidersAreBound covers the image running a subset. A
// scenario entry with no listener in front of it is reported as unimplemented,
// which is a warning and never a startup failure.
func TestOnlySelectedProvidersAreBound(t *testing.T) {
	t.Parallel()

	h := start(t, testConfig(t, "--providers", "exa"), discard())

	require.NotEmpty(t, h.Addr(SurfaceAdmin))
	require.NotEmpty(t, h.Addr(string(provider.Exa)))
	require.Empty(t, h.Addr(string(provider.Tavily)), "tavily was not selected and must not be bound")
	require.Empty(t, h.Addr(string(provider.Perplexity)), "perplexity was not selected and must not be bound")

	require.Equal(t, http.StatusOK,
		post(t, h.Addr(string(provider.Exa)), "/search", `{"query":"report a"}`).StatusCode)

	unimplemented := map[string]bool{}
	for _, f := range h.Report().Warnings() {
		if f.Code == provider.CodeProviderUnimplemented {
			unimplemented[f.Path] = true
		}
	}
	require.True(t, unimplemented["providers.tavily"])
	require.True(t, unimplemented["providers.perplexity"])
	require.True(t, unimplemented["providers.perplexity_agent"])
}

// TestBindsConfiguredInterfaceOnly guards the default that keeps a developer's
// simulator off their network. Every listener must come up on the configured
// address and no other.
func TestBindsConfiguredInterfaceOnly(t *testing.T) {
	t.Parallel()

	h := start(t, testConfig(t), discard())

	for _, name := range []string{SurfaceAdmin, "exa", "tavily", "perplexity"} {
		host, port, err := net.SplitHostPort(h.Addr(name))
		require.NoErrorf(t, err, "surface %s", name)
		require.Equalf(t, "127.0.0.1", host, "surface %s bound the wrong interface", name)
		require.NotEqualf(t, "0", port, "surface %s reported an unbound port", name)
	}
}

// -----------------------------------------------------------------------------
// Startup order: acceptance criterion 4
// -----------------------------------------------------------------------------

// TestProjectionErrorFailsBeforeAnythingBinds is acceptance criterion 4 from the
// failing side. The envelope is valid — scenario.Validate cannot see inside a
// projection body — so only provider.ValidateScenario catches the unknown source
// reference, and it must do so before a listener exists to report ready.
func TestProjectionErrorFailsBeforeAnythingBinds(t *testing.T) {
	t.Parallel()

	args := writeScenario(t, `
version: 1
name: bad-projection
time:
  base: 2026-01-01T00:00:00Z
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-missing
`)
	cfg := testConfig(t, args...)

	srv, err := New(cfg, discard())
	require.Error(t, err)
	require.Nil(t, srv)
	require.Contains(t, err.Error(), "source-missing")
}

// TestUnknownProviderIsAWarningNotAFailure is the other half of the same rule: a
// scenario naming a provider this build has no handler for must still start, so
// that a file shared across repositories does not break the moment one consumer
// pins an older Servicesim.
func TestUnknownProviderIsAWarningNotAFailure(t *testing.T) {
	t.Parallel()

	var logs logBuffer
	cfg := testConfig(t, "--scenario", "builtin:unknown-provider")
	h := start(t, cfg, NewLogger(cfg, &logs))

	require.Equal(t, http.StatusOK,
		post(t, h.Addr(string(provider.Exa)), "/search", `{"query":"report a"}`).StatusCode)

	status, body := get(t, h.Addr(SurfaceAdmin), "/__admin/scenario")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, string(body), provider.CodeProviderUnimplemented)
	require.Contains(t, string(body), "providers.openai")

	require.Contains(t, logs.String(), provider.CodeProviderUnimplemented,
		"an unimplemented provider must be visible in the startup log, not only on the admin surface")
}

// TestSunsetIsAnnouncedOnceAtStartup covers the Perplexity Sonar sunset notice.
// It is a property of the simulated API, not of a request, so it belongs in the
// startup log exactly once and never in per-request noise.
func TestSunsetIsAnnouncedOnceAtStartup(t *testing.T) {
	t.Parallel()

	var logs logBuffer
	cfg := testConfig(t)
	h := start(t, cfg, NewLogger(cfg, &logs))

	for _, call := range everySurface {
		require.Equal(t, http.StatusOK, post(t, h.Addr(call.surface), call.path, call.body).StatusCode)
	}

	require.Equal(t, 1, strings.Count(logs.String(), "perplexity.sonar.sunset"),
		"the sunset date belongs in the startup log once, not once per request")
	require.Contains(t, logs.String(), "2026-09-27")
}

// TestSunsetIsSilentWithoutThePerplexityListener pins the gate: the notice is
// emitted when the Perplexity surface is constructed and not otherwise.
func TestSunsetIsSilentWithoutThePerplexityListener(t *testing.T) {
	t.Parallel()

	var logs logBuffer
	cfg := testConfig(t, "--providers", "exa,tavily")
	start(t, cfg, NewLogger(cfg, &logs))

	require.NotContains(t, logs.String(), "perplexity.sonar.sunset")
}

// -----------------------------------------------------------------------------
// Scenario registry and namespaces
// -----------------------------------------------------------------------------

// TestScenarioPrefixSelectsBehaviourPerRequest is the behaviour half of the
// shared-container feature: one process holds every scenario in --scenario-dir,
// and each request says which of them it wants.
func TestScenarioPrefixSelectsBehaviourPerRequest(t *testing.T) {
	t.Parallel()

	args := writeScenarioDir(t, map[string]string{
		"default.yaml": scenarioNamed("default-behaviour", "Default Report"),
		"alpha.yaml":   scenarioNamed("alpha-behaviour", "Alpha Report"),
	})
	h := start(t, testConfig(t, args...), discard())
	exa := h.Addr(string(provider.Exa))

	require.Equal(t, []string{"alpha", "default"}, h.ScenarioNames(),
		"the registry is keyed on the file name, in a fixed order")

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "unprefixed serves the default", path: "/search", want: "Default Report"},
		{name: "prefixed selects by name", path: "/x/alpha/search", want: "Alpha Report"},
		{name: "the default is selectable by name", path: "/x/default/search", want: "Default Report"},
		{name: "a namespace does not change behaviour", path: "/x/alpha/n/t-1/search", want: "Alpha Report"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := postBody(t, exa, tc.path, `{"query":"report a"}`)
			require.Equal(t, http.StatusOK, status)
			require.Contains(t, body, tc.want)
		})
	}
}

// TestUnknownScenarioFailsClosed is the rule that makes scenario selection safe
// to rely on. Serving the default for a scenario the URL did not name would hand
// a consumer coherent-looking behaviour from the wrong fixture, and the test that
// caught it would fail somewhere else entirely, much later.
//
// The answer is provider-shaped, because a consumer's error decoder is written
// against the vendor's envelope and a simulator-shaped body would make it fail to
// parse rather than to report.
func TestUnknownScenarioFailsClosed(t *testing.T) {
	t.Parallel()

	var logs logBuffer
	args := writeScenarioDir(t, map[string]string{
		"default.yaml": scenarioNamed("default-behaviour", "Default Report"),
		"alpha.yaml":   scenarioNamed("alpha-behaviour", "Alpha Report"),
	})
	cfg := testConfig(t, args...)
	h := start(t, cfg, NewLogger(cfg, &logs))

	for _, tc := range []struct {
		name    string
		surface string
		path    string
		body    string
		want    string
	}{
		{
			name: "exa", surface: string(provider.Exa),
			path: "/x/nope/search", body: `{"query":"report a"}`,
			want: `"tag":"NOT_FOUND"`,
		},
		{
			name: "tavily", surface: string(provider.Tavily),
			path: "/x/nope/search", body: `{"query":"report a"}`,
			want: `{"detail":{"error":"Not Found"}}`,
		},
		{
			name: "perplexity", surface: string(provider.Perplexity),
			path: "/x/nope/v1/sonar", body: `{"model":"sonar","messages":[{"role":"user","content":"q"}]}`,
			want: `{"detail":"Not Found"}`,
		},
		{
			name:    "an invalid name is refused, not sanitised into a lookup",
			surface: string(provider.Exa),
			path:    "/x/no.pe/search", body: `{"query":"report a"}`,
			want: `"tag":"NOT_FOUND"`,
		},
		{
			name:    "a namespace does not rescue an unknown scenario",
			surface: string(provider.Exa),
			path:    "/x/nope/n/t-1/search", body: `{"query":"report a"}`,
			want: `"tag":"NOT_FOUND"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := postBody(t, h.Addr(tc.surface), tc.path, tc.body)
			require.Equal(t, http.StatusNotFound, status)
			require.Contains(t, body, tc.want, "the body must be the provider's own error shape")
			require.NotContains(t, body, "Default Report",
				"an unknown scenario must never fall back to the default")
			require.NotContains(t, body, "Alpha Report")
		})
	}

	// The refusal is journaled and logged, because "my test called /x/typo" is
	// exactly the question a consumer brings to the journal.
	status, journalBody := get(t, h.Addr(SurfaceAdmin), "/__admin/requests")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, string(journalBody), CodeScenarioUnknown)
	require.Contains(t, logs.String(), CodeScenarioUnknown)

	// A rejected name is reported as rejected, never echoed into the field a log
	// consumer indexes on. The received path is a separate matter: the journal and
	// the request event record what the client sent, by design.
	require.Contains(t, logs.String(), `"msg":"scenario.unknown","scenario":"<invalid>"`)
	require.NotContains(t, logs.String(), `"scenario":"no.pe"`)
}

// TestUnprefixedRequestFailsClosedWithoutADefaultScenario covers the directory
// that names no default: several scenarios and no stated default make an
// unprefixed URL genuinely ambiguous, and picking one would be the same
// wrong-fixture failure as ignoring an unknown name.
func TestUnprefixedRequestFailsClosedWithoutADefaultScenario(t *testing.T) {
	t.Parallel()

	args := writeScenarioDir(t, map[string]string{
		"alpha.yaml": scenarioNamed("alpha-behaviour", "Alpha Report"),
		"beta.yaml":  scenarioNamed("beta-behaviour", "Beta Report"),
	})
	h := start(t, testConfig(t, args...), discard())
	exa := h.Addr(string(provider.Exa))

	status, body := postBody(t, exa, "/search", `{"query":"report a"}`)
	require.Equal(t, http.StatusNotFound, status)
	require.Contains(t, body, `"tag":"NOT_FOUND"`)

	status, body = postBody(t, exa, "/x/beta/search", `{"query":"report a"}`)
	require.Equal(t, http.StatusOK, status, "naming a scenario still works")
	require.Contains(t, body, "Beta Report")
}

// TestEveryScenarioInTheDirectoryIsValidatedBeforeReady is acceptance criterion 4
// under a directory: one broken scenario fails the whole process. A container
// that comes up healthy while silently serving a broken scenario is worse than
// one that refuses to start.
//
// Both broken scenarios are named, because an operator fixing a mounted
// directory needs to see all of its problems rather than one per restart.
func TestEveryScenarioInTheDirectoryIsValidatedBeforeReady(t *testing.T) {
	t.Parallel()

	broken := `
version: 1
name: broken
time:
  base: 2026-01-01T00:00:00Z
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-missing
`
	args := writeScenarioDir(t, map[string]string{
		"default.yaml": scenarioNamed("default-behaviour", "Default Report"),
		"broken.yaml":  broken,
		"also-bad.yaml": `
version: 99
name: from-the-future
`,
	})

	srv, err := New(testConfig(t, args...), discard())
	require.Error(t, err)
	require.Nil(t, srv)
	require.Contains(t, err.Error(), "broken.yaml")
	require.Contains(t, err.Error(), "source-missing")
	require.Contains(t, err.Error(), "also-bad.yaml",
		"every scenario is validated, so a startup failure names all of them")
}

// TestNamespacesIsolateConcurrentTestsInOneProcess is the isolation half of the
// feature, and the reason it exists: every sibling e2e suite runs one shared mock
// container across many concurrent tests.
//
// The rate-limited scenario answers 429 once and then serves its ordinary
// response, so each namespace must see exactly that sequence. With one shared
// cursor the four calls below draw indices 0..3 and only one of them is faulted,
// which leaves one namespace with two successes — a failure this test detects
// however the two goroutines interleave.
func TestNamespacesIsolateConcurrentTestsInOneProcess(t *testing.T) {
	t.Parallel()

	h := start(t, testConfig(t, "--scenario", "builtin:rate-limited"), discard())
	exa := h.Addr(string(provider.Exa))

	namespaces := []string{"t-alpha", "t-beta"}
	got := make([][]int, len(namespaces))

	var wg sync.WaitGroup
	for i, ns := range namespaces {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Sequential within a namespace: a test is one caller, and the claim
			// under test is that another caller's traffic does not move this
			// caller's cursor.
			for range 2 {
				resp := post(t, exa, "/n/"+ns+"/search", `{"query":"report a"}`)
				got[i] = append(got[i], resp.StatusCode)
			}
		}()
	}
	wg.Wait()

	for i, ns := range namespaces {
		require.Equalf(t, []int{http.StatusTooManyRequests, http.StatusOK}, got[i],
			"namespace %s drew its attempts from another namespace's cursor", ns)
	}

	// The journal keeps the two apart as well, so a consumer can ask what its own
	// namespace did without reading another test's traffic.
	status, body := get(t, h.Addr(SurfaceAdmin), "/__admin/requests")
	require.Equal(t, http.StatusOK, status)
	for _, ns := range namespaces {
		require.Contains(t, string(body), `"namespace":"`+ns+`"`)
	}
}

// -----------------------------------------------------------------------------
// Lifecycle
// -----------------------------------------------------------------------------

// TestPortInUseFailsCleanly is the failure mode that must never hang. A wedged
// startup surfaces in a consumer's suite as an unexplained timeout, which is the
// least diagnosable outcome this process can produce.
func TestPortInUseFailsCleanly(t *testing.T) {
	t.Parallel()

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = busy.Close() }()

	taken := busy.Addr().(*net.TCPAddr).Port
	cfg := testConfig(t, "--exa-port", strconv.Itoa(taken))

	srv, err := New(cfg, discard())
	require.NoError(t, err, "a port collision is a bind-time failure, not a construction-time one")

	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background()) }()

	select {
	case runErr := <-done:
		require.Error(t, runErr)
		require.Contains(t, runErr.Error(), "exa")
		require.Contains(t, runErr.Error(), strconv.Itoa(taken))
	case <-srv.Started():
		t.Fatal("the server reported started despite a listener that could not bind")
	case <-time.After(startTimeout):
		t.Fatal("Run hung on a port that was already in use")
	}

	// The listeners that did come up are released, so the operator's next
	// attempt is not blocked by the failed one.
	admin := srv.Addr(SurfaceAdmin)
	require.NotEmpty(t, admin, "the admin listener binds first and its address is still reportable")
	conn, err := net.DialTimeout("tcp", admin, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("the admin listener was left open after a failed startup")
	}
}

// TestRunIsNotRestartable states the lifecycle rule rather than discovering it:
// a Server owns its listeners for its whole life and a second Run would race the
// first one's shutdown over the same http.Server values.
func TestRunIsNotRestartable(t *testing.T) {
	t.Parallel()

	h := start(t, testConfig(t), discard())
	require.ErrorIs(t, h.Run(context.Background()), ErrAlreadyRun)
}

// TestShutdownDrainsInFlightRequests is the graceful-shutdown guarantee. The
// scenario delays the response, the test waits for the server to have begun
// reading the request — not for a duration — and then shuts down. The request
// must still be answered.
func TestShutdownDrainsInFlightRequests(t *testing.T) {
	t.Parallel()

	args := writeScenario(t, `
version: 1
name: drain
time:
  base: 2026-01-01T00:00:00Z
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    fault:
      attempts:
        - delay: 400ms
          status: 200
    results:
      - source: source-a
`)

	active := make(chan struct{}, 1)
	h := newHarness(t, testConfig(t, args...), discard(), func(s *Server) {
		s.connState = func(name string, _ net.Conn, state http.ConnState) {
			if name == string(provider.Exa) && state == http.StateActive {
				select {
				case active <- struct{}{}:
				default:
				}
			}
		}
	})
	h.awaitStarted(t)

	type result struct {
		status int
		err    error
	}
	results := make(chan result, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			"http://"+h.Addr(string(provider.Exa))+"/search", strings.NewReader(`{"query":"report a"}`))
		if err != nil {
			results <- result{err: err}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testKey)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			results <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		results <- result{status: resp.StatusCode}
	}()

	select {
	case <-active:
	case <-time.After(startTimeout):
		t.Fatal("the exa listener never began reading the request")
	}

	require.NoError(t, h.Shutdown(context.Background()))

	select {
	case got := <-results:
		require.NoError(t, got.err, "an in-flight request was cut off instead of drained")
		require.Equal(t, http.StatusOK, got.status)
	case <-time.After(startTimeout):
		t.Fatal("the in-flight request never completed")
	}

	// Readiness drops before the drain finishes, so a probe arriving during
	// shutdown is not invited to send more traffic.
	require.False(t, h.ready.Load())
}

// TestRunReturnsWhenItsContextIsCancelled covers the ordinary signal path: the
// process shuts every listener down and returns without an error.
func TestRunReturnsWhenItsContextIsCancelled(t *testing.T) {
	t.Parallel()

	h := start(t, testConfig(t), discard())
	admin := h.Addr(SurfaceAdmin)

	h.cancel()
	select {
	case err := <-h.done:
		h.done <- err
		require.NoError(t, err)
	case <-time.After(startTimeout):
		t.Fatal("Run did not return after its context was cancelled")
	}

	conn, err := net.DialTimeout("tcp", admin, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("the admin listener is still accepting after shutdown")
	}
}

// -----------------------------------------------------------------------------
// Logging
// -----------------------------------------------------------------------------

// TestRequestLogIsStructuredAndCredentialFree checks both halves of the logging
// requirement at once, because they are the same event: one line per completed
// request carrying the fields a consumer diagnoses with, and never the
// credential that produced it.
func TestRequestLogIsStructuredAndCredentialFree(t *testing.T) {
	t.Parallel()

	var logs logBuffer
	cfg := testConfig(t)
	h := start(t, cfg, NewLogger(cfg, &logs))

	require.Equal(t, http.StatusOK,
		post(t, h.Addr(string(provider.Exa)), "/search", `{"query":"report a"}`).StatusCode)

	var event map[string]any
	for line := range strings.Lines(logs.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m["msg"] == "request" {
			event = m
			break
		}
	}
	require.NotNil(t, event, "no request event was logged:\n%s", logs.String())

	require.Equal(t, "exa", event["provider"])
	require.Equal(t, "happy", event["scenario"], "the scenario name is what tells two runs apart")
	require.Equal(t, "POST /search", event["route"])
	require.Equal(t, float64(1), event["seq"])
	require.Equal(t, float64(http.StatusOK), event["status"])
	require.Equal(t, "exa.search.ok", event["label"])
	require.Contains(t, event, "duration_ms")
	require.Equal(t, float64(0), event["errors"])

	require.NotContains(t, logs.String(), testKey, "a credential must never reach a log line")
	require.NotContains(t, logs.String(), "Bearer ")
}

// TestNewLoggerHonoursFormat covers the one configuration knob that changes the
// shape of every line: CI reads JSON, a developer reads text.
func TestNewLoggerHonoursFormat(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		format string
		want   string
	}{
		{name: "json", format: config.LogFormatJSON, want: `"msg":"hello"`},
		{name: "text", format: config.LogFormatText, want: `msg=hello`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			cfg := testConfig(t, "--log-format", tc.format)
			NewLogger(cfg, &buf).Info("hello")
			require.Contains(t, buf.String(), tc.want)
		})
	}
}

// TestLogLevelIsHonoured proves the level reaches the handler, which is what a
// consumer turns up when a scenario is not behaving as they expect.
func TestLogLevelIsHonoured(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := testConfig(t, "--log-level", "error")
	logger := NewLogger(cfg, &buf)
	logger.Info("suppressed")
	logger.Error("kept")

	require.NotContains(t, buf.String(), "suppressed")
	require.Contains(t, buf.String(), "kept")
}

// -----------------------------------------------------------------------------
// Wiring
// -----------------------------------------------------------------------------

// TestResetClearsTheCountersTheHandlersConsult is the wiring assertion §2.9
// calls out by name: admin.Deps.Faults and every provider's Deps.Faults must be
// the same engine instance, or POST /__admin/reset zeroes counters nobody reads.
func TestResetClearsTheCountersTheHandlersConsult(t *testing.T) {
	t.Parallel()

	h := start(t, testConfig(t, "--scenario", "builtin:rate-limited"), discard())
	exa := h.Addr(string(provider.Exa))

	require.Equal(t, http.StatusTooManyRequests,
		post(t, exa, "/search", `{"query":"report a"}`).StatusCode)
	require.Equal(t, http.StatusOK,
		post(t, exa, "/search", `{"query":"report a"}`).StatusCode)

	// The scope is explicit because the admin surface requires it: a bare reset
	// that silently wiped every concurrent test's cursors is the trap the
	// namespace design names, so "everything" has to be asked for by name.
	resetReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://"+h.Addr(SurfaceAdmin)+"/__admin/reset?all=true", nil)
	require.NoError(t, err)
	resetResp, err := http.DefaultClient.Do(resetReq)
	require.NoError(t, err)
	require.NoError(t, resetResp.Body.Close())
	require.Equal(t, http.StatusOK, resetResp.StatusCode)

	require.Equal(t, http.StatusTooManyRequests,
		post(t, exa, "/search", `{"query":"report a"}`).StatusCode,
		"the fault plan restarted, so reset reached the counter the handler consults")
}

// TestStrictAuthOffRelaxesOnlyUndeclaredPolicies covers --strict-auth=false. It
// is expressed on the loaded scenario because the scenario entry is the only
// seam the provider handlers read, and it must leave a scenario that states its
// own policy exactly as authored.
func TestStrictAuthOffRelaxesOnlyUndeclaredPolicies(t *testing.T) {
	t.Parallel()

	args := writeScenario(t, `
version: 1
name: mixed-auth
time:
  base: 2026-01-01T00:00:00Z
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
  tavily:
    auth:
      mode: reject
    answer: A short synthesis of Report A.
    results:
      - source: source-a
        score: 0.98
`)
	h := start(t, testConfig(t, append(args, "--strict-auth=false")...), discard())

	unauthenticated := func(t *testing.T, addr, path, body string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			"http://"+addr+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		return resp.StatusCode
	}

	require.Equal(t, http.StatusOK,
		unauthenticated(t, h.Addr(string(provider.Exa)), "/search", `{"query":"report a"}`),
		"an entry that declares no auth policy follows --strict-auth")
	require.Equal(t, http.StatusUnauthorized,
		unauthenticated(t, h.Addr(string(provider.Tavily)), "/search", `{"query":"report a"}`),
		"an entry that declares its own policy is left exactly as authored")
}

// TestScenarioIsResolvedThroughTheConfinedOpener proves the binary never reaches
// outside --scenario-root, which is the containment scenario.Load deliberately
// does not provide.
func TestScenarioIsResolvedThroughTheConfinedOpener(t *testing.T) {
	t.Parallel()

	outside := filepath.Join(t.TempDir(), "outside.yaml")
	require.NoError(t, os.WriteFile(outside, []byte("version: 1\nname: outside\n"), 0o600))

	root := t.TempDir()
	cfg := testConfig(t, "--scenario", outside, "--scenario-root", root)

	srv, err := New(cfg, discard())
	require.Error(t, err)
	require.Nil(t, srv)
}

// TestVersionIsReported covers the one thing configuration cannot carry: the
// build version arrives through ldflags and has to reach GET /healthz.
func TestVersionIsReported(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	srv, err := New(cfg, discard(), WithVersion("1.2.3"))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(startTimeout):
			t.Error("Run did not return")
		}
	})

	select {
	case <-srv.Started():
	case err := <-done:
		t.Fatalf("server exited before it started: %v", err)
	case <-time.After(startTimeout):
		t.Fatal("server did not start")
	}

	status, body := get(t, srv.Addr(SurfaceAdmin), "/healthz")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, string(body), `"version":"1.2.3"`)

	require.Equal(t, "happy", srv.Scenario().Name)
	require.True(t, srv.Report().OK())
}
