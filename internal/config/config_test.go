package config

import (
	"errors"
	"flag"
	"log/slog"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/provider/exa"
	"github.com/c360studio/servicesim/provider/mcp"
	"github.com/c360studio/servicesim/provider/perplexity"
	"github.com/c360studio/servicesim/provider/tavily"
)

// env builds a hermetic lookupEnv. Tests never touch the real environment, so
// they can all run in parallel.
func env(pairs map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := pairs[key]
		return value, ok
	}
}

// referenceSet builds the Set every test in this file loads against: the
// four reference profiles, exactly what cmd/servicesim registers. Phase 10
// unit 3 moved Load's provider knowledge out of this package entirely, so
// every test that used to enumerate provider.Exa/Tavily/Perplexity/MCP now
// builds a Set from the profiles' own Profile() constructors instead.
func referenceSet(t *testing.T) *provider.Set {
	t.Helper()
	set, err := provider.NewSet(exa.Profile(), tavily.Profile(), perplexity.Profile(), mcp.Profile())
	require.NoError(t, err)
	return set
}

// ignoreSetAndOrder excludes the two fields a cmp.Diff against a literal
// Config cannot construct: Set (a *provider.Set, compared instead with
// assert.Same — it must be the exact pointer Load was given) and order (an
// unexported field cmp cannot see into without an Exporter, populated the
// same way Set's own registration order is).
var ignoreSetAndOrder = cmpopts.IgnoreFields(Config{}, "Set", "order")

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	set := referenceSet(t)
	got, err := Load(set, nil, env(nil))
	require.NoError(t, err)

	want := Config{
		BindAddress: "127.0.0.1",
		Admin:       Listener{Port: 8080, Enabled: true},
		Listeners: map[provider.Name]*Listener{
			exa.Name:        {Port: 8081, Enabled: true},
			tavily.Name:     {Port: 8082, Enabled: true},
			perplexity.Name: {Port: 8083, Enabled: true},
			mcp.Name:        {Port: 8084, Enabled: true},
		},
		ScenarioPath:        "builtin:happy",
		ScenarioRoot:        "",
		ScenarioDir:         "",
		MaxNamespaces:       1024,
		MaxJobs:             256,
		JournalCapacity:     1000,
		MaxRequestBytes:     1 << 20,
		MaxJournalBodyBytes: 1 << 16,
		ReadHeaderTimeout:   5 * time.Second,
		ShutdownGrace:       5 * time.Second,
		LogLevel:            slog.LevelInfo,
		LogFormat:           "json",
		StrictAuth:          true,
	}
	if diff := cmp.Diff(want, got, ignoreSetAndOrder); diff != "" {
		t.Errorf("Load() defaults mismatch (-want +got):\n%s", diff)
	}
	assert.Same(t, set, got.Set, "Load must store the exact Set it was given")
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

			got, err := Load(referenceSet(t), tc.args, env(tc.vars))
			require.NoError(t, err)
			l, ok := got.Listener(exa.Name)
			require.True(t, ok)
			assert.Equal(t, tc.want, l.Port)
		})
	}
}

// TestLoadEnvironmentAppliesToEveryBinding walks the whole table so a flag added
// without its environment variable, or bound to the wrong name, fails here
// rather than in a container three weeks later.
func TestLoadEnvironmentAppliesToEveryBinding(t *testing.T) {
	t.Parallel()

	set := referenceSet(t)
	vars := map[string]string{
		"SERVICESIM_SCENARIO":               "/scenarios/custom.yaml",
		"SERVICESIM_SCENARIO_ROOT":          "/scenarios",
		"SERVICESIM_BIND_ADDRESS":           "0.0.0.0",
		"SERVICESIM_ADMIN_PORT":             "9080",
		"SERVICESIM_EXA_PORT":               "9081",
		"SERVICESIM_TAVILY_PORT":            "9082",
		"SERVICESIM_PERPLEXITY_PORT":        "9083",
		"SERVICESIM_MCP_PORT":               "9084",
		"SERVICESIM_PROVIDERS":              "exa",
		"SERVICESIM_MAX_NAMESPACES":         "64",
		"SERVICESIM_MAX_JOBS":               "32",
		"SERVICESIM_JOURNAL_CAPACITY":       "7",
		"SERVICESIM_MAX_REQUEST_BYTES":      "2048",
		"SERVICESIM_MAX_JOURNAL_BODY_BYTES": "1024",
		"SERVICESIM_READ_HEADER_TIMEOUT":    "11s",
		"SERVICESIM_SHUTDOWN_GRACE":         "12s",
		"SERVICESIM_LOG_LEVEL":              "debug",
		"SERVICESIM_LOG_FORMAT":             "text",
		"SERVICESIM_STRICT_AUTH":            "false",
	}
	all := append(append([]binding{}, bindings...), portBindings(set)...)
	for _, b := range all {
		// --scenario-dir cannot share an environment with --scenario, which the
		// rest of this table sets: the two are mutually exclusive by design.
		// TestLoadScenarioDirFromEnvironment covers its binding instead.
		if b.flag == "scenario-dir" {
			continue
		}
		_, ok := vars[b.env]
		assert.Truef(t, ok, "binding %q (--%s) is not covered by this test", b.env, b.flag)
	}

	got, err := Load(set, nil, env(vars))
	require.NoError(t, err)

	want := Config{
		BindAddress: "0.0.0.0",
		Admin:       Listener{Port: 9080, Enabled: true},
		Listeners: map[provider.Name]*Listener{
			exa.Name:        {Port: 9081, Enabled: true},
			tavily.Name:     {Port: 9082},
			perplexity.Name: {Port: 9083},
			mcp.Name:        {Port: 9084},
		},
		ScenarioPath:        "/scenarios/custom.yaml",
		ScenarioRoot:        "/scenarios",
		MaxNamespaces:       64,
		MaxJobs:             32,
		JournalCapacity:     7,
		MaxRequestBytes:     2048,
		MaxJournalBodyBytes: 1024,
		ReadHeaderTimeout:   11 * time.Second,
		ShutdownGrace:       12 * time.Second,
		LogLevel:            slog.LevelDebug,
		LogFormat:           "text",
		StrictAuth:          false,
	}
	if diff := cmp.Diff(want, got, ignoreSetAndOrder); diff != "" {
		t.Errorf("Load() from environment mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadScenarioRootDefaultsToScenarioDirectory(t *testing.T) {
	t.Parallel()
	set := referenceSet(t)

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

			got, err := Load(set, tc.args, env(nil))
			require.NoError(t, err)
			assert.Equal(t, tc.wantPath, got.ScenarioPath)
			assert.Equal(t, tc.wantRoot, got.ScenarioRoot)
		})
	}
}

// TestLoadScenarioDir pins the second scenario mode. The interesting part is
// what it clears: there is no single startup scenario in directory mode, so
// ScenarioPath must not still be holding the built-in default a caller would
// otherwise load and serve.
func TestLoadScenarioDir(t *testing.T) {
	t.Parallel()
	set := referenceSet(t)

	tests := []struct {
		name    string
		args    []string
		wantDir string
	}{
		{
			name:    "an absolute directory",
			args:    []string{"--scenario-dir", "/scenarios"},
			wantDir: "/scenarios",
		},
		{
			name:    "a trailing separator is cleaned away",
			args:    []string{"--scenario-dir", "/scenarios/"},
			wantDir: "/scenarios",
		},
		{
			name:    "a relative directory",
			args:    []string{"--scenario-dir", "testdata/scenarios"},
			wantDir: "testdata/scenarios",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(set, tc.args, env(nil))
			require.NoError(t, err)
			assert.Equal(t, tc.wantDir, got.ScenarioDir)
			assert.True(t, got.ScenarioDirMode())
			assert.Empty(t, got.ScenarioPath, "directory mode has no single startup scenario")
			assert.Empty(t, got.ScenarioRoot, "the directory is its own containment root")
		})
	}
}

// TestLoadScenarioDirFromEnvironment covers the binding
// TestLoadEnvironmentAppliesToEveryBinding has to skip, because --scenario-dir
// and --scenario cannot be set in the same environment.
func TestLoadScenarioDirFromEnvironment(t *testing.T) {
	t.Parallel()

	got, err := Load(referenceSet(t), nil, env(map[string]string{"SERVICESIM_SCENARIO_DIR": "/scenarios"}))
	require.NoError(t, err)
	assert.Equal(t, "/scenarios", got.ScenarioDir)
	assert.True(t, got.ScenarioDirMode())
	assert.Empty(t, got.ScenarioPath)
}

// TestLoadSingleScenarioIsUnchangedByDirectoryMode states the compatibility
// property directly: every existing invocation, including the image's CMD, must
// keep resolving exactly as it did before --scenario-dir existed.
func TestLoadSingleScenarioIsUnchangedByDirectoryMode(t *testing.T) {
	t.Parallel()

	set := referenceSet(t)
	got, err := Load(set, []string{"--scenario", "/scenarios/fusion-overlap.yaml"}, env(nil))
	require.NoError(t, err)
	assert.Equal(t, "/scenarios/fusion-overlap.yaml", got.ScenarioPath)
	assert.Equal(t, "/scenarios", got.ScenarioRoot)
	assert.Empty(t, got.ScenarioDir)
	assert.False(t, got.ScenarioDirMode())

	builtin, err := Load(set, nil, env(nil))
	require.NoError(t, err)
	assert.Equal(t, DefaultScenario, builtin.ScenarioPath)
	assert.False(t, builtin.ScenarioDirMode())
}

// TestLoadScenarioModesAreMutuallyExclusive checks the combination from both
// mechanisms, because the operator who typed both must not have to discover
// which one won by watching responses.
func TestLoadScenarioModesAreMutuallyExclusive(t *testing.T) {
	t.Parallel()
	set := referenceSet(t)

	tests := []struct {
		name     string
		args     []string
		vars     map[string]string
		wantErrs []string
	}{
		{
			name:     "both flags",
			args:     []string{"--scenario", "/scenarios/a.yaml", "--scenario-dir", "/scenarios"},
			wantErrs: []string{"--scenario", "--scenario-dir", "mutually exclusive"},
		},
		{
			name:     "the scenario from the environment and the directory from a flag",
			args:     []string{"--scenario-dir", "/scenarios"},
			vars:     map[string]string{"SERVICESIM_SCENARIO": "/scenarios/a.yaml"},
			wantErrs: []string{"mutually exclusive"},
		},
		{
			name:     "the directory from the environment and the scenario from a flag",
			args:     []string{"--scenario", "/scenarios/a.yaml"},
			vars:     map[string]string{"SERVICESIM_SCENARIO_DIR": "/scenarios"},
			wantErrs: []string{"mutually exclusive"},
		},
		{
			name:     "both from the environment",
			vars:     map[string]string{"SERVICESIM_SCENARIO": "a.yaml", "SERVICESIM_SCENARIO_DIR": "/scenarios"},
			wantErrs: []string{"mutually exclusive"},
		},
		{
			name: "a scenario root would be silently ignored, so it is rejected",
			args: []string{"--scenario-dir", "/scenarios", "--scenario-root", "/elsewhere"},
			wantErrs: []string{
				"--scenario-root", "--scenario-dir", "containment root",
			},
		},
		{
			name:     "an empty directory value",
			args:     []string{"--scenario-dir", ""},
			wantErrs: []string{"--scenario-dir", "empty"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(set, tc.args, env(tc.vars))
			require.Error(t, err)
			for _, want := range tc.wantErrs {
				assert.Containsf(t, err.Error(), want, "error %q should mention %q", err, want)
			}
		})
	}
}

// TestLoadMaxNamespaces covers the bound on a surface that grows implicitly:
// namespaces are created on first use, so nothing else limits them.
func TestLoadMaxNamespaces(t *testing.T) {
	t.Parallel()
	set := referenceSet(t)

	tests := []struct {
		name    string
		args    []string
		vars    map[string]string
		want    int
		wantErr []string
	}{
		{
			name: "the default bounds a shared container",
			want: DefaultMaxNamespaces,
		},
		{
			name: "an explicit flag",
			args: []string{"--max-namespaces", "4"},
			want: 4,
		},
		{
			name: "one namespace is a legitimate bound",
			args: []string{"--max-namespaces", "1"},
			want: 1,
		},
		{
			name: "the environment binding",
			vars: map[string]string{"SERVICESIM_MAX_NAMESPACES": "16"},
			want: 16,
		},
		{
			// Zero is not "unlimited" and not "the default": every request
			// belongs to a namespace, so zero would reject all traffic.
			name:    "zero",
			args:    []string{"--max-namespaces", "0"},
			wantErr: []string{"--max-namespaces", "at least 1"},
		},
		{
			name:    "negative",
			args:    []string{"--max-namespaces", "-1"},
			wantErr: []string{"--max-namespaces", "at least 1"},
		},
		{
			name:    "a negative environment value is validated like a flag value",
			vars:    map[string]string{"SERVICESIM_MAX_NAMESPACES": "-1"},
			wantErr: []string{"--max-namespaces", "at least 1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(set, tc.args, env(tc.vars))
			if len(tc.wantErr) > 0 {
				require.Error(t, err)
				for _, want := range tc.wantErr {
					assert.Containsf(t, err.Error(), want, "error %q should mention %q", err, want)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.MaxNamespaces)
		})
	}
}

// TestLoadMaxJobs mirrors TestLoadMaxNamespaces: the bound on a surface that
// also grows implicitly, one create at a time.
func TestLoadMaxJobs(t *testing.T) {
	t.Parallel()
	set := referenceSet(t)

	tests := []struct {
		name    string
		args    []string
		vars    map[string]string
		want    int
		wantErr []string
	}{
		{
			name: "the default bounds a shared container",
			want: DefaultMaxJobs,
		},
		{
			name: "an explicit flag",
			args: []string{"--max-jobs", "4"},
			want: 4,
		},
		{
			name: "one job is a legitimate bound",
			args: []string{"--max-jobs", "1"},
			want: 1,
		},
		{
			name: "the environment binding",
			vars: map[string]string{"SERVICESIM_MAX_JOBS": "16"},
			want: 16,
		},
		{
			// Zero is not "unlimited" and not "the default": every create would
			// be refused.
			name:    "zero",
			args:    []string{"--max-jobs", "0"},
			wantErr: []string{"--max-jobs", "at least 1"},
		},
		{
			name:    "negative",
			args:    []string{"--max-jobs", "-1"},
			wantErr: []string{"--max-jobs", "at least 1"},
		},
		{
			name:    "a negative environment value is validated like a flag value",
			vars:    map[string]string{"SERVICESIM_MAX_JOBS": "-1"},
			wantErr: []string{"--max-jobs", "at least 1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(set, tc.args, env(tc.vars))
			if len(tc.wantErr) > 0 {
				require.Error(t, err)
				for _, want := range tc.wantErr {
					assert.Containsf(t, err.Error(), want, "error %q should mention %q", err, want)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.MaxJobs)
		})
	}
}

// TestLoadMaxJobsFlagBeatsEnvironment follows the precedence tests above: an
// explicit flag, even one equal to the default, must not be shadowed by the
// environment.
func TestLoadMaxJobsFlagBeatsEnvironment(t *testing.T) {
	t.Parallel()

	got, err := Load(referenceSet(t), []string{"--max-jobs", "256"}, env(map[string]string{"SERVICESIM_MAX_JOBS": "9"}))
	require.NoError(t, err)
	assert.Equal(t, 256, got.MaxJobs)
}

// TestDefaultMaxJobsAgreesWithJobsPackage guards a duplication the import
// graph forces: config must not import internal/jobs to read the constant (it
// would be an odd dependency for a flag-parsing package to carry for one
// integer), so both packages declare DefaultMaxJobs independently. Two
// diverging values would mean the documented flag default lies about what a
// fresh registry actually bounds itself to.
func TestDefaultMaxJobsAgreesWithJobsPackage(t *testing.T) {
	t.Parallel()
	assert.Equal(t, jobs.DefaultMaxJobs, DefaultMaxJobs)
}

func TestLoadProviders(t *testing.T) {
	t.Parallel()
	set := referenceSet(t)

	tests := []struct {
		name string
		args []string
		want []provider.Name
	}{
		{
			name: "default enables every provider in a stable order",
			want: []provider.Name{exa.Name, tavily.Name, perplexity.Name, mcp.Name},
		},
		{
			name: "a subset enables only those listeners",
			args: []string{"--providers", "tavily,exa"},
			want: []provider.Name{exa.Name, tavily.Name},
		},
		{
			name: "order in the flag does not change the reported order",
			args: []string{"--providers", "perplexity,exa"},
			want: []provider.Name{exa.Name, perplexity.Name},
		},
		{
			name: "whitespace and case are tolerated",
			args: []string{"--providers", " Exa , TAVILY "},
			want: []provider.Name{exa.Name, tavily.Name},
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

			got, err := Load(set, tc.args, env(nil))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Enabled())
			assert.True(t, got.Admin.Enabled, "the admin surface is always enabled")
		})
	}
}

// TestDefaultProvidersMatchesEveryProvider keeps the --providers default and
// the enumeration Enabled() walks from drifting apart. Phase 10 unit 3
// deleted the hand-maintained DefaultProviders string and allProviders
// slice — both are now derived from the Set Load was given — so what this
// test pins is that Enabled() with no --providers flag reports exactly
// set.Names(), in the same order.
func TestDefaultProvidersMatchesEveryProvider(t *testing.T) {
	t.Parallel()

	set := referenceSet(t)
	got, err := Load(set, nil, env(nil))
	require.NoError(t, err)
	assert.Equal(t, set.Names(), got.Enabled())
}

// TestLoadRejects covers every value that would otherwise fail late: a panic in
// make(), a limit that silently rejects all traffic, or an exposure nobody
// asked for.
func TestLoadRejects(t *testing.T) {
	t.Parallel()
	set := referenceSet(t)

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
			wantErrs: []string{"scenario.yaml", "only flags are accepted"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(set, tc.args, env(tc.vars))
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

	got, err := Load(referenceSet(t), []string{
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

	got, err := Load(referenceSet(t), []string{"--admin-port", "0", "--exa-port", "0"}, env(nil))
	require.NoError(t, err)
	assert.Zero(t, got.Admin.Port)
	l, ok := got.Listener(exa.Name)
	require.True(t, ok)
	assert.Zero(t, l.Port)
}

// TestLoadProcessModes checks the two flag-only modes. They are deliberately not
// environment-bound: a variable that could turn the server into a health probe
// would be a footgun.
func TestLoadProcessModes(t *testing.T) {
	t.Parallel()
	set := referenceSet(t)

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

			got, err := Load(set, tc.args, env(tc.vars))
			require.NoError(t, err)
			assert.Equal(t, tc.wantHealthcheck, got.Healthcheck)
			assert.Equal(t, tc.wantVersion, got.ShowVersion)
		})
	}
}

func TestLoadHelpIsIdentifiable(t *testing.T) {
	t.Parallel()

	_, err := Load(referenceSet(t), []string{"--help"}, env(nil))
	require.ErrorIs(t, err, flag.ErrHelp)
}

// TestLoadWritesNothing is what lets cmd/servicesim own the failure output. The
// flag package writes usage to stderr by default; Load must not.
func TestLoadWritesNothing(t *testing.T) {
	t.Parallel()

	usage := Usage(referenceSet(t))
	for _, want := range []string{
		"-scenario", "-scenario-dir", "-max-namespaces", "-bind-address", "-healthcheck", "-version",
		"-exa-port", "-tavily-port", "-perplexity-port", "-mcp-port",
		"-print-routes", "-print-ports", "-print-hosts",
	} {
		assert.Contains(t, usage, want)
	}
}

func TestLoadNilLookupEnvReadsNoEnvironment(t *testing.T) {
	t.Parallel()

	got, err := Load(referenceSet(t), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, DefaultBindAddress, got.BindAddress)
}

func TestAddrAndHealthcheckURL(t *testing.T) {
	t.Parallel()

	got, err := Load(referenceSet(t), nil, env(map[string]string{"SERVICESIM_BIND_ADDRESS": "0.0.0.0"}))
	require.NoError(t, err)

	l, ok := got.Listener(exa.Name)
	require.True(t, ok)
	assert.Equal(t, "0.0.0.0:8081", got.Addr(l))
	// The probe dials loopback: 0.0.0.0 names every interface to a listener and
	// is not a destination to dial.
	assert.Equal(t, "http://127.0.0.1:8080/healthz", got.HealthcheckURL())
}

func TestConfigErrorsAreNotFlagErrHelp(t *testing.T) {
	t.Parallel()

	_, err := Load(referenceSet(t), []string{"--log-format", "xml"}, env(nil))
	require.Error(t, err)
	assert.False(t, errors.Is(err, flag.ErrHelp))
}

// TestLoadPrintFlags covers the three mode flags Set.Routes/Set.LiveHosts and
// the resolved Listeners map feed: [Config.PrintRoutes], [Config.PrintPorts]
// and [Config.PrintHosts] parse like --version and --healthcheck (a flag,
// never an environment variable — see [binding]'s own doc comment on why)
// and are documented in --help ([TestLoadWritesNothing]).
func TestLoadPrintFlags(t *testing.T) {
	t.Parallel()

	set := referenceSet(t)
	tests := []struct {
		name string
		args []string
		want Config
	}{
		{name: "none by default"},
		{name: "print-routes", args: []string{"--print-routes"}, want: Config{PrintRoutes: true}},
		{name: "print-ports", args: []string{"--print-ports"}, want: Config{PrintPorts: true}},
		{name: "print-hosts", args: []string{"--print-hosts"}, want: Config{PrintHosts: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(set, tc.args, env(nil))
			require.NoError(t, err)
			assert.Equal(t, tc.want.PrintRoutes, got.PrintRoutes)
			assert.Equal(t, tc.want.PrintPorts, got.PrintPorts)
			assert.Equal(t, tc.want.PrintHosts, got.PrintHosts)
		})
	}
}

// TestLoadRejectsDuplicatePorts covers the registration-time gap NewSet
// cannot catch: two DEFAULT ports never collide (NewSet already refuses
// that), but an operator can point two flags at the same port, or a flag at
// the admin port, and only the resolved Config can see it.
func TestLoadRejectsDuplicatePorts(t *testing.T) {
	t.Parallel()

	set := referenceSet(t)
	tests := []struct {
		name     string
		args     []string
		wantErrs []string
	}{
		{
			name:     "two enabled listeners share a port",
			args:     []string{"--exa-port", "9999", "--tavily-port", "9999"},
			wantErrs: []string{"--exa-port", "--tavily-port", "9999"},
		},
		{
			name:     "a provider listener collides with the admin port",
			args:     []string{"--exa-port", "8080"},
			wantErrs: []string{"--admin-port", "--exa-port", "8080"},
		},
		{
			name: "a disabled listener sharing a port is not a collision",
			args: []string{
				"--providers", "exa",
				"--exa-port", "9999", "--tavily-port", "9999",
			},
		},
		{
			name: "two listeners both asking for an ephemeral port is not a collision",
			args: []string{"--exa-port", "0", "--tavily-port", "0"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(set, tc.args, env(nil))
			if len(tc.wantErrs) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tc.wantErrs {
				assert.Containsf(t, err.Error(), want, "error %q should mention %q", err, want)
			}
		})
	}
}
