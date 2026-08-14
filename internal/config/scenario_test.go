package config

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	insideBody  = "# inside the root\n"
	nestedBody  = "# nested inside the root\n"
	outsideBody = "# outside the root, must never be readable\n"
)

// tree builds the fixture every containment test resolves against:
//
//	<base>/secret.yaml          outside the root
//	<base>/scen/happy.yaml      the ordinary case
//	<base>/scen/nested/deep.yaml
//	<base>/scen/escape.yaml     a symlink to <base>/secret.yaml
//
// The base is the symlink-resolved temporary directory. On macOS t.TempDir()
// hands back a path under /var, which is a symlink to /private/var; resolving it
// once here keeps the fixture's idea of "absolute" the same as os.Getwd's, so a
// test failure means a containment bug rather than a platform quirk.
func tree(t *testing.T) (base, root string) {
	t.Helper()

	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	root = filepath.Join(base, "scen")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "nested"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(base, "secret.yaml"), []byte(outsideBody), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "happy.yaml"), []byte(insideBody), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "nested", "deep.yaml"), []byte(nestedBody), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(base, "secret.yaml"), filepath.Join(root, "escape.yaml")))

	return base, root
}

func readAll(t *testing.T, f fs.File) string {
	t.Helper()
	b, err := io.ReadAll(f)
	require.NoError(t, err)
	return string(b)
}

// TestOpenScenarioContainment is the security test. The symlink case is the one
// os.Root is here for and the one a filepath.Clean prefix check would miss.
func TestOpenScenarioContainment(t *testing.T) {
	t.Parallel()

	base, root := tree(t)

	tests := []struct {
		name     string
		path     string
		root     string
		wantName string
		wantBody string
		wantErr  string
	}{
		{
			name:     "absolute path inside the root",
			path:     filepath.Join(root, "happy.yaml"),
			root:     root,
			wantName: "happy.yaml",
			wantBody: insideBody,
		},
		{
			name:     "nested path inside the root",
			path:     filepath.Join(root, "nested", "deep.yaml"),
			root:     root,
			wantName: "nested/deep.yaml",
			wantBody: nestedBody,
		},
		{
			name:     "an unset root defaults to the scenario's own directory",
			path:     filepath.Join(root, "happy.yaml"),
			wantName: "happy.yaml",
			wantBody: insideBody,
		},
		{
			name:    "parent traversal",
			path:    filepath.Join(root, "..", "secret.yaml"),
			root:    root,
			wantErr: "escapes scenario root",
		},
		{
			name:    "traversal disguised by a deeper prefix",
			path:    filepath.Join(root, "nested", "..", "..", "secret.yaml"),
			root:    root,
			wantErr: "escapes scenario root",
		},
		{
			name:    "an absolute path outside the root",
			path:    filepath.Join(base, "secret.yaml"),
			root:    root,
			wantErr: "escapes scenario root",
		},
		{
			name:    "a symlink inside the root pointing outside it",
			path:    filepath.Join(root, "escape.yaml"),
			root:    root,
			wantErr: "opening scenario",
		},
		{
			name:    "a file that does not exist",
			path:    filepath.Join(root, "absent.yaml"),
			root:    root,
			wantErr: "opening scenario",
		},
		{
			name:    "the root itself",
			path:    root,
			root:    root,
			wantErr: "not a file",
		},
		{
			name:    "a directory inside the root",
			path:    filepath.Join(root, "nested"),
			root:    root,
			wantErr: "is a directory",
		},
		{
			name:    "a root that does not exist",
			path:    filepath.Join(base, "absent", "happy.yaml"),
			root:    filepath.Join(base, "absent"),
			wantErr: "opening scenario root",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := Config{ScenarioPath: tc.path, ScenarioRoot: tc.root}
			file, name, err := cfg.OpenScenario()

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, file, "no file may be returned alongside an error")
				assert.Empty(t, name)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, file)
			defer file.Close()
			assert.Equal(t, tc.wantName, name)
			assert.Equal(t, tc.wantBody, readAll(t, file))
		})
	}
}

// TestOpenScenarioSymlinkNeverYieldsTheTarget states the property the previous
// test's symlink case exists to protect, separately from the error text: the
// bytes outside the root must not be readable by any spelling.
func TestOpenScenarioSymlinkNeverYieldsTheTarget(t *testing.T) {
	t.Parallel()

	_, root := tree(t)

	cfg := Config{ScenarioPath: filepath.Join(root, "escape.yaml"), ScenarioRoot: root}
	file, _, err := cfg.OpenScenario()
	require.Error(t, err)
	if file != nil {
		defer file.Close()
		assert.NotEqual(t, outsideBody, readAll(t, file))
	}
}

func TestOpenScenarioMissingFileIsNotExist(t *testing.T) {
	t.Parallel()

	_, root := tree(t)

	cfg := Config{ScenarioPath: filepath.Join(root, "absent.yaml"), ScenarioRoot: root}
	_, _, err := cfg.OpenScenario()
	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrNotExist,
		"the caller distinguishes a missing scenario from a containment failure")
}

// TestOpenScenarioRelativePaths runs against a working directory rather than in
// parallel, because t.Chdir is process-wide. The relative spelling is what a
// developer types outside a container.
func TestOpenScenarioRelativePaths(t *testing.T) {
	base, _ := tree(t)
	t.Chdir(base)

	tests := []struct {
		name     string
		path     string
		root     string
		wantName string
		wantBody string
		wantErr  string
	}{
		{
			name:     "relative path with a relative root",
			path:     filepath.Join("scen", "happy.yaml"),
			root:     "scen",
			wantName: "happy.yaml",
			wantBody: insideBody,
		},
		{
			name:     "relative path with an unset root",
			path:     filepath.Join("scen", "nested", "deep.yaml"),
			wantName: "deep.yaml",
			wantBody: nestedBody,
		},
		{
			name:     "relative path with an absolute root",
			path:     filepath.Join("scen", "happy.yaml"),
			root:     filepath.Join(base, "scen"),
			wantName: "happy.yaml",
			wantBody: insideBody,
		},
		{
			name:     "absolute path with a relative root",
			path:     filepath.Join(base, "scen", "happy.yaml"),
			root:     "scen",
			wantName: "happy.yaml",
			wantBody: insideBody,
		},
		{
			name:    "relative traversal out of the root",
			path:    filepath.Join("scen", "..", "secret.yaml"),
			root:    "scen",
			wantErr: "escapes scenario root",
		},
		{
			name:    "a sibling directory is still outside",
			path:    "secret.yaml",
			root:    "scen",
			wantErr: "escapes scenario root",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{ScenarioPath: tc.path, ScenarioRoot: tc.root}
			file, name, err := cfg.OpenScenario()

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, file)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, file)
			defer file.Close()
			assert.Equal(t, tc.wantName, name)
			assert.Equal(t, tc.wantBody, readAll(t, file))
		})
	}
}

func TestBuiltin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path     string
		wantName string
		wantOK   bool
	}{
		{path: "builtin:happy", wantName: "happy", wantOK: true},
		{path: "builtin:", wantName: "", wantOK: true},
		{path: "/scenarios/happy.yaml"},
		{path: "happy.yaml"},
		{path: ""},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()

			name, ok := Config{ScenarioPath: tc.path}.Builtin()
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantName, name)
		})
	}
}

// TestOpenScenarioRefusesBuiltin keeps a built-in from being resolved as a file
// named "builtin:happy" in the working directory.
func TestOpenScenarioRefusesBuiltin(t *testing.T) {
	t.Parallel()

	cfg := Config{ScenarioPath: "builtin:happy"}
	file, name, err := cfg.OpenScenario()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBuiltinScenario)
	assert.Nil(t, file)
	assert.Empty(t, name)
}

func TestOpenScenarioRefusesEmptyPath(t *testing.T) {
	t.Parallel()

	_, _, err := Config{}.OpenScenario()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--scenario")
}

// TestOpenScenarioFromLoad is the end-to-end path the binary takes: flags in,
// bytes out, with nothing outside the mount reachable.
func TestOpenScenarioFromLoad(t *testing.T) {
	t.Parallel()

	base, root := tree(t)

	cfg, err := Load([]string{"--scenario", filepath.Join(root, "happy.yaml")}, env(nil))
	require.NoError(t, err)
	assert.Equal(t, root, cfg.ScenarioRoot)

	file, name, err := cfg.OpenScenario()
	require.NoError(t, err)
	defer file.Close()
	assert.Equal(t, "happy.yaml", name)
	assert.Equal(t, insideBody, readAll(t, file))

	escaping, err := Load([]string{
		"--scenario", filepath.Join(base, "secret.yaml"),
		"--scenario-root", root,
	}, env(nil))
	require.NoError(t, err)
	_, _, err = escaping.OpenScenario()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes scenario root")
}
