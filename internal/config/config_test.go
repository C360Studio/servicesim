package config

import (
	"errors"
	"flag"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/provider"
)

// env builds a hermetic lookupEnv. Tests never touch the real environment, so
// they can all run in parallel.
func env(pairs map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := pairs[key]
		return value, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	got, err := Load(nil, env(nil))
	require.NoError(t, err)

	want := Config{
		BindAddress:         "127.0.0.1",
		Admin:               Listener{Port: 8080, Enabled: true},
		Exa:                 Listener{Port: 8081, Enabled: true},
		Tavily:              Listener{Port: 8082, Enabled: true},
		Perplexity:          Listener{Port: 8083, Enabled: true},
		ScenarioPath:        "builtin:happy",
		ScenarioRoot:        "",
		JournalCapacity:     1000,
		MaxRequestBytes:     1 << 20,
		MaxJournalBodyBytes: 1 << 16,
		ReadHeaderTimeout:   5 * time.Second,
		ShutdownGrace:       5 * time.Second,
		LogLevel:            slog.LevelInfo,
		LogFormat:           "json",
		StrictAuth:          true,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Load() defaults mismatch (-want +got):\n%s", diff)
	}
}

// TestLoadPrecedence is the reason Load parses before it reads the environment.
// The case that matters is "explicit flag equal to the default": a flag package
// cannot tell that apart from an unset flag, and without FlagSet.Visit the
// environment would silently win.
func TestLoadPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		vars map[string]string
		want int
	}{
		{
			name: "built-in default when neither is set",
			want: 8081,
		},
		{
			name: "environment overrides the default",
			vars: map[string]string{"SERVICESIM_EXA_PORT": "9101"},
			want: 9101,
		},
		{
			name: "explicit flag overrides the environment",
			args: []string{"--exa-port", "9202"},
			vars: map[string]string{"SERVICESIM_EXA_PORT": "9101"},
			want: 9202,
		},
		{
			name: "explicit flag set to the default value still beats the environment",
			args: []string{"--exa-port", "8081"},
			vars: map[string]string{"SERVICESIM_EXA_PORT": "9101"},
			want: 8081,
		},
		{
			name: "single-dash flag form is explicit too",
			args: []string{"-exa-port=8081"},
			vars: map[string]string{"SERVICESIM_EXA_PORT": "9101"},
			want: 8081,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(tc.args, env(tc.vars))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Exa.Port)
		})
	}
}

// TestLoadEnvironmentAppliesToEveryBinding walks the whole table so a flag added
// without its environment variable, or bound to the wrong name, fails here
// rather than in a container three weeks later.
func TestLoadEnvironmentAppliesToEveryBinding(t *testing.T) {
	t.Parallel()

	vars := map[string]string{
		"SERVICESIM_SCENARIO":               "/scenarios/custom.yaml",
		"SERVICESIM_SCENARIO_ROOT":          "/scenarios",
		"SERVICESIM_BIND_ADDRESS":           "0.0.0.0",
		"SERVICESIM_ADMIN_PORT":             "9080",
		"SERVICESIM_EXA_PORT":               "9081",
		"SERVICESIM_TAVILY_PORT":            "9082",
		"SERVICESIM_PERPLEXITY_PORT":        "9083",
		"SERVICESIM_PROVIDERS":              "exa",
		"SERVICESIM_JOURNAL_CAPACITY":       "7",
		"SERVICESIM_MAX_REQUEST_BYTES":      "2048",
		"SERVICESIM_MAX_JOURNAL_BODY_BYTES": "1024",
		"SERVICESIM_READ_HEADER_TIMEOUT":    "11s",
		"SERVICESIM_SHUTDOWN_GRACE":         "12s",
		"SERVICESIM_LOG_LEVEL":              "debug",
		"SERVICESIM_LOG_FORMAT":             "text",
		"SERVICESIM_STRICT_AUTH":            "false",
	}
	for _, b := range bindings {
		_, ok := vars[b.env]
		assert.Truef(t, ok, "binding %q (--%s) is not covered by this test", b.env, b.flag)
	}

	got, err := Load(nil, env(vars))
	require.NoError(t, err)

	want := Config{
		BindAddress:         "0.0.0.0",
		Admin:               Listener{Port: 9080, Enabled: true},
		Exa:                 Listener{Port: 9081, Enabled: true},
		Tavily:              Listener{Port: 9082},
		Perplexity:          Listener{Port: 9083},
		ScenarioPath:        "/scenarios/custom.yaml",
		ScenarioRoot:        "/scenarios",
		JournalCapacity:     7,
		MaxRequestBytes:     2048,
		MaxJournalBodyBytes: 1024,
		ReadHeaderTimeout:   11 * time.Second,
		ShutdownGrace:       12 * time.Second,
		LogLevel:            slog.LevelDebug,
		LogFormat:           "text",
		StrictAuth:          false,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Load() from environment mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadScenarioRootDefaultsToScenarioDirectory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantPath string
		wantRoot string
	}{
		{
			name:     "a built-in gets no root, because it never touches the file system",
			args:     []string{"--scenario", "builtin:happy"},
			wantPath: "builtin:happy",
			wantRoot: "",
		},
		{
			name:     "an absolute path roots at its directory",
			args:     []string{"--scenario", "/scenarios/fusion-overlap.yaml"},
			wantPath: "/scenarios/fusion-overlap.yaml",
			wantRoot: "/scenarios",
		},
		{
			name:     "a relative path roots at its directory",
			args:     []string{"--scenario", "testdata/happy.yaml"},
			wantPath: "testdata/happy.yaml",
			wantRoot: "testdata",
		},
		{
			name:     "a bare file name roots at the working directory",
			args:     []string{"--scenario", "happy.yaml"},
			wantPath: "happy.yaml",
			wantRoot: ".",
		},
		{
			name:     "an explicit root is kept",
			args:     []string{"--scenario", "/scenarios/nested/deep.yaml", "--scenario-root", "/scenarios"},
			wantPath: "/scenarios/nested/deep.yaml",
			wantRoot: "/scenarios",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(tc.args, env(nil))
			require.NoError(t, err)
			assert.Equal(t, tc.wantPath, got.ScenarioPath)
			assert.Equal(t, tc.wantRoot, got.ScenarioRoot)
		})
	}
}

func TestLoadProviders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want []provider.Name
	}{
		{
			name: "default enables every provider in a stable order",
			want: []provider.Name{provider.Exa, provider.Tavily, provider.Perplexity},
		},
		{
			name: "a subset enables only those listeners",
			args: []string{"--providers", "tavily,exa"},
			want: []provider.Name{provider.Exa, provider.Tavily},
		},
		{
			name: "order in the flag does not change the reported order",
			args: []string{"--providers", "perplexity,exa"},
			want: []provider.Name{provider.Exa, provider.Perplexity},
		},
		{
			name: "whitespace and case are tolerated",
			args: []string{"--providers", " Exa , TAVILY "},
			want: []provider.Name{provider.Exa, provider.Tavily},
		},
		{
			name: "an empty list serves the admin surface only",
			args: []string{"--providers", ""},
			want: []provider.Name{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(tc.args, env(nil))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Enabled())
			assert.True(t, got.Admin.Enabled, "the admin surface is always enabled")
		})
	}
}

// TestDefaultProvidersMatchesEveryProvider keeps the documented default string
// and the enumeration Enabled() walks from drifting apart.
func TestDefaultProvidersMatchesEveryProvider(t *testing.T) {
	t.Parallel()

	names := make([]string, 0, len(allProviders))
	for _, name := range allProviders {
		names = append(names, string(name))
	}
	assert.Equal(t, DefaultProviders, strings.Join(names, ","))
}

// TestLoadRejects covers every value that would otherwise fail late: a panic in
// make(), a limit that silently rejects all traffic, or an exposure nobody
// asked for.
func TestLoadRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		vars     map[string]string
		wantErrs []string
	}{
		{
			name:     "negative journal capacity",
			args:     []string{"--journal-capacity", "-1"},
			wantErrs: []string{"--journal-capacity", "negative"},
		},
		{
			name:     "negative max request bytes",
			args:     []string{"--max-request-bytes", "-1"},
			wantErrs: []string{"--max-request-bytes", "negative"},
		},
		{
			name:     "negative max journal body bytes",
			args:     []string{"--max-journal-body-bytes", "-1"},
			wantErrs: []string{"--max-journal-body-bytes", "negative"},
		},
		{
			name:     "negative read header timeout",
			args:     []string{"--read-header-timeout", "-1s"},
			wantErrs: []string{"--read-header-timeout", "negative"},
		},
		{
			name:     "negative shutdown grace",
			args:     []string{"--shutdown-grace", "-1s"},
			wantErrs: []string{"--shutdown-grace", "negative"},
		},
		{
			name:     "port above the sixteen-bit range",
			args:     []string{"--exa-port", "70000"},
			wantErrs: []string{"--exa-port", "65535"},
		},
		{
			name:     "negative port",
			args:     []string{"--admin-port", "-1"},
			wantErrs: []string{"--admin-port"},
		},
		{
			name:     "unknown provider",
			args:     []string{"--providers", "exa,tavly"},
			wantErrs: []string{"--providers", "tavly", "exa,tavily,perplexity"},
		},
		{
			name:     "unknown log level",
			args:     []string{"--log-level", "chatty"},
			wantErrs: []string{"--log-level", "chatty"},
		},
		{
			name:     "unknown log format",
			args:     []string{"--log-format", "xml"},
			wantErrs: []string{"--log-format", "xml"},
		},
		{
			name:     "empty bind address, which would otherwise bind every interface",
			args:     []string{"--bind-address", ""},
			wantErrs: []string{"--bind-address", "0.0.0.0"},
		},
		{
			name:     "empty scenario",
			args:     []string{"--scenario", ""},
			wantErrs: []string{"--scenario"},
		},
		{
			name:     "unknown built-in scenario",
			args:     []string{"--scenario", "builtin:nope"},
			wantErrs: []string{"builtin:nope", "happy"},
		},
		{
			name:     "unparseable flag value",
			args:     []string{"--exa-port", "eightyeightyone"},
			wantErrs: []string{"exa-port"},
		},
		{
			name:     "unparseable environment value names the variable",
			vars:     map[string]string{"SERVICESIM_EXA_PORT": "eightyeightyone"},
			wantErrs: []string{"SERVICESIM_EXA_PORT", "eightyeightyone"},
		},
		{
			name:     "an environment value is validated like a flag value",
			vars:     map[string]string{"SERVICESIM_JOURNAL_CAPACITY": "-1"},
			wantErrs: []string{"--journal-capacity", "negative"},
		},
		{
			name:     "unknown flag",
			args:     []string{"--nope"},
			wantErrs: []string{"nope"},
		},
		{
			name:     "positional argument",
			args:     []string{"scenario.yaml"},
			wantErrs: []string{"scenario.yaml", "flags only"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(tc.args, env(tc.vars))
			require.Error(t, err)
			for _, want := range tc.wantErrs {
				assert.Containsf(t, err.Error(), want, "error %q should mention %q", err, want)
			}
		})
	}
}

// TestLoadZeroKeepsItsDocumentedMeaning pins the asymmetry: zero disables
// retention, but zero on a byte limit means "the default", because a zero
// reaching http.MaxBytesReader rejects every request.
func TestLoadZeroKeepsItsDocumentedMeaning(t *testing.T) {
	t.Parallel()

	got, err := Load([]string{
		"--journal-capacity", "0",
		"--max-request-bytes", "0",
		"--max-journal-body-bytes", "0",
	}, env(nil))
	require.NoError(t, err)

	assert.Equal(t, 0, got.JournalCapacity, "zero capacity disables retention")
	assert.Equal(t, DefaultMaxRequestBytes, got.MaxRequestBytes)
	assert.Equal(t, DefaultMaxJournalBodyBytes, got.MaxJournalBodyBytes)
}

func TestLoadEphemeralPortsAreAllowed(t *testing.T) {
	t.Parallel()

	got, err := Load([]string{"--admin-port", "0", "--exa-port", "0"}, env(nil))
	require.NoError(t, err)
	assert.Zero(t, got.Admin.Port)
	assert.Zero(t, got.Exa.Port)
}

// TestLoadProcessModes checks the two flag-only modes. They are deliberately not
// environment-bound: a variable that could turn the server into a health probe
// would be a footgun.
func TestLoadProcessModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		args            []string
		vars            map[string]string
		wantHealthcheck bool
		wantVersion     bool
	}{
		{name: "neither by default"},
		{name: "healthcheck", args: []string{"--healthcheck"}, wantHealthcheck: true},
		{name: "version", args: []string{"--version"}, wantVersion: true},
		{
			name: "no environment variable can request a process mode",
			vars: map[string]string{
				"SERVICESIM_HEALTHCHECK": "true",
				"SERVICESIM_VERSION":     "true",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(tc.args, env(tc.vars))
			require.NoError(t, err)
			assert.Equal(t, tc.wantHealthcheck, got.Healthcheck)
			assert.Equal(t, tc.wantVersion, got.ShowVersion)
		})
	}
}

func TestLoadHelpIsIdentifiable(t *testing.T) {
	t.Parallel()

	_, err := Load([]string{"--help"}, env(nil))
	require.ErrorIs(t, err, flag.ErrHelp)
}

// TestLoadWritesNothing is what lets cmd/servicesim own the failure output. The
// flag package writes usage to stderr by default; Load must not.
func TestLoadWritesNothing(t *testing.T) {
	t.Parallel()

	usage := Usage()
	for _, want := range []string{"-scenario", "-bind-address", "-healthcheck", "-version"} {
		assert.Contains(t, usage, want)
	}
}

func TestLoadNilLookupEnvReadsNoEnvironment(t *testing.T) {
	t.Parallel()

	got, err := Load(nil, nil)
	require.NoError(t, err)
	assert.Equal(t, DefaultBindAddress, got.BindAddress)
}

func TestAddrAndHealthcheckURL(t *testing.T) {
	t.Parallel()

	got, err := Load(nil, env(map[string]string{"SERVICESIM_BIND_ADDRESS": "0.0.0.0"}))
	require.NoError(t, err)

	assert.Equal(t, "0.0.0.0:8081", got.Addr(got.Exa))
	// The probe dials loopback: 0.0.0.0 names every interface to a listener and
	// is not a destination to dial.
	assert.Equal(t, "http://127.0.0.1:8080/healthz", got.HealthcheckURL())
}

func TestConfigErrorsAreNotFlagErrHelp(t *testing.T) {
	t.Parallel()

	_, err := Load([]string{"--log-format", "xml"}, env(nil))
	require.Error(t, err)
	assert.False(t, errors.Is(err, flag.ErrHelp))
}
