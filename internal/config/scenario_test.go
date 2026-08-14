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

// scenarioDir builds a scenario directory holding one file per name, each
// carrying its own name as its body so a test can prove which file it read. The
// base is symlink-resolved for the reason tree gives.
func scenarioDir(t *testing.T, files ...string) (base, dir string) {
	t.Helper()

	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	dir = filepath.Join(base, "scenarios")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(base, "secret.yaml"), []byte(outsideBody), 0o600))
	for _, name := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body(name)), 0o600))
	}
	return base, dir
}

// body is the content scenarioDir writes into a file, and what a test asserts it
// read back.
func body(file string) string {
	return "# " + file + "\n"
}

// TestScenarioEntries covers what a mounted directory actually contains: the
// scenarios, the README nobody meant to serve, a subdirectory whose name ends in
// .yaml, and the relative symlinks a Kubernetes ConfigMap mount is made of.
func TestScenarioEntries(t *testing.T) {
	t.Parallel()

	// Written in an order that is not the answer, so a passing assertion means
	// the sort ran rather than that the file system happened to agree.
	_, dir := scenarioDir(t,
		"roles.yaml", "default.yaml", "legacy.yml", "Upper.YAML", "README.md", "checksums.txt")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "nested.yaml"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".hidden.yaml"), []byte(body("hidden")), 0o600))
	require.NoError(t, os.Symlink("roles.yaml", filepath.Join(dir, "alias.yaml")))

	entries, err := Config{ScenarioDir: dir}.ScenarioEntries()
	require.NoError(t, err)
	assert.Equal(t, []ScenarioEntry{
		// The extension matches case-insensitively; the name keeps its spelling.
		{Name: "Upper", File: "Upper.YAML"},
		{Name: "alias", File: "alias.yaml"},
		{Name: "default", File: "default.yaml"},
		{Name: "legacy", File: "legacy.yml"},
		{Name: "roles", File: "roles.yaml"},
	}, entries)

	// Determinism is the product: the same directory must list the same way
	// every time, whatever order the file system reports.
	again, err := Config{ScenarioDir: dir}.ScenarioEntries()
	require.NoError(t, err)
	assert.Equal(t, entries, again)
}

// TestScenarioEntriesRejectsDuplicateNames is the reason the directory is read
// at startup: /x/happy would otherwise mean either file, and the operator would
// find out from a response served by the wrong scenario.
func TestScenarioEntriesRejectsDuplicateNames(t *testing.T) {
	t.Parallel()

	_, dir := scenarioDir(t, "happy.yaml", "happy.yml")

	_, err := Config{ScenarioDir: dir}.ScenarioEntries()
	require.Error(t, err)
	for _, want := range []string{"happy.yaml", "happy.yml", "/x/happy"} {
		assert.Containsf(t, err.Error(), want, "error %q should mention %q", err, want)
	}

	// The pair the message names must not depend on directory order.
	_, again := Config{ScenarioDir: dir}.ScenarioEntries()
	require.Error(t, again)
	assert.Equal(t, err.Error(), again.Error())
}

func TestScenarioEntriesRejects(t *testing.T) {
	t.Parallel()

	base, dir := scenarioDir(t, "roles.yaml")

	escaping := filepath.Join(base, "escaping")
	require.NoError(t, os.MkdirAll(escaping, 0o750))
	require.NoError(t, os.Symlink(filepath.Join(base, "secret.yaml"), filepath.Join(escaping, "escape.yaml")))

	// A directory of files nobody named with a scenario extension, which is what
	// a mount pointed at the wrong path usually looks like.
	unnamed := filepath.Join(base, "unnamed")
	require.NoError(t, os.MkdirAll(unnamed, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(unnamed, "README.md"), []byte(insideBody), 0o600))

	tests := []struct {
		name     string
		dir      string
		wantErrs []string
	}{
		{
			name:     "an unset directory",
			wantErrs: []string{"--scenario-dir", "empty"},
		},
		{
			name:     "a directory that does not exist",
			dir:      filepath.Join(base, "absent"),
			wantErrs: []string{"opening scenario directory"},
		},
		{
			name:     "a file where a directory was named",
			dir:      filepath.Join(dir, "roles.yaml"),
			wantErrs: []string{"scenario directory"},
		},
		{
			name:     "a directory holding no scenario files",
			dir:      unnamed,
			wantErrs: []string{"no scenario files", ".yaml"},
		},
		{
			name:     "a symlink pointing outside the directory",
			dir:      escaping,
			wantErrs: []string{"escape.yaml"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			entries, err := Config{ScenarioDir: tc.dir}.ScenarioEntries()
			require.Error(t, err)
			assert.Nil(t, entries)
			for _, want := range tc.wantErrs {
				assert.Containsf(t, err.Error(), want, "error %q should mention %q", err, want)
			}
		})
	}
}

// TestScenarioEntriesEmptyDirectoryHoldsNoSecret guards the one case that would
// otherwise be silent: a mount that resolved to a directory with no scenarios
// must fail startup, not start a simulator that serves nothing.
func TestScenarioEntriesEmptyDirectoryIsAnError(t *testing.T) {
	t.Parallel()

	_, dir := scenarioDir(t)

	_, err := Config{ScenarioDir: dir}.ScenarioEntries()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no scenario files")
}

// TestOpenScenarioEntryContainment is the security test for the directory mode.
// The name may have come from anywhere, so containment is enforced on it here
// rather than assumed from the enumeration that produced it.
func TestOpenScenarioEntryContainment(t *testing.T) {
	t.Parallel()

	base, dir := scenarioDir(t, "roles.yaml")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "nested"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nested", "deep.yaml"), []byte(nestedBody), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(base, "secret.yaml"), filepath.Join(dir, "escape.yaml")))

	tests := []struct {
		name     string
		file     string
		wantBody string
		wantErr  string
	}{
		{
			name:     "an ordinary scenario file",
			file:     "roles.yaml",
			wantBody: body("roles.yaml"),
		},
		{
			name:    "parent traversal",
			file:    filepath.Join("..", "secret.yaml"),
			wantErr: "not a plain file name",
		},
		{
			name:    "a nested path",
			file:    filepath.Join("nested", "deep.yaml"),
			wantErr: "not a plain file name",
		},
		{
			name:    "an absolute path",
			file:    filepath.Join(base, "secret.yaml"),
			wantErr: "not a plain file name",
		},
		{
			name:    "the directory itself",
			file:    ".",
			wantErr: "not a plain file name",
		},
		{
			name:    "the parent directory",
			file:    "..",
			wantErr: "not a plain file name",
		},
		{
			name:    "no name at all",
			file:    "",
			wantErr: "no scenario file named",
		},
		{
			name:    "a symlink inside the directory pointing outside it",
			file:    "escape.yaml",
			wantErr: "opening scenario",
		},
		{
			name:    "a subdirectory",
			file:    "nested",
			wantErr: "not a regular file",
		},
		{
			name:    "a file that does not exist",
			file:    "absent.yaml",
			wantErr: "opening scenario",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file, err := Config{ScenarioDir: dir}.OpenScenarioEntry(tc.file)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, file, "no file may be returned alongside an error")
				return
			}

			require.NoError(t, err)
			require.NotNil(t, file)
			defer file.Close()
			assert.Equal(t, tc.wantBody, readAll(t, file))
		})
	}
}

// TestOpenScenarioEntrySymlinkNeverYieldsTheTarget states the property the
// symlink case protects, separately from the error text.
func TestOpenScenarioEntrySymlinkNeverYieldsTheTarget(t *testing.T) {
	t.Parallel()

	base, dir := scenarioDir(t, "roles.yaml")
	require.NoError(t, os.Symlink(filepath.Join(base, "secret.yaml"), filepath.Join(dir, "escape.yaml")))

	file, err := Config{ScenarioDir: dir}.OpenScenarioEntry("escape.yaml")
	require.Error(t, err)
	if file != nil {
		defer file.Close()
		assert.NotEqual(t, outsideBody, readAll(t, file))
	}
}

func TestOpenScenarioEntryMissingFileIsNotExist(t *testing.T) {
	t.Parallel()

	_, dir := scenarioDir(t, "roles.yaml")

	_, err := Config{ScenarioDir: dir}.OpenScenarioEntry("absent.yaml")
	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrNotExist,
		"the caller distinguishes a missing scenario from a containment failure")
}

func TestDefaultScenarioEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []ScenarioEntry
		want    string
		wantOK  bool
	}{
		{
			name:    "the entry named default wins",
			entries: []ScenarioEntry{{Name: "default"}, {Name: "roles"}},
			want:    "default",
			wantOK:  true,
		},
		{
			name:    "a lone scenario is the default whatever it is called",
			entries: []ScenarioEntry{{Name: "roles"}},
			want:    "roles",
			wantOK:  true,
		},
		{
			name:    "several scenarios and no stated default is genuinely ambiguous",
			entries: []ScenarioEntry{{Name: "roles"}, {Name: "tools"}},
		},
		{
			name: "no entries at all",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := DefaultScenarioEntry(tc.entries)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got.Name)
		})
	}
}

// TestScenarioDirFromLoad is the end-to-end path the binary takes in directory
// mode: flags in, every scenario enumerated and readable, nothing outside the
// mount reachable.
func TestScenarioDirFromLoad(t *testing.T) {
	t.Parallel()

	base, dir := scenarioDir(t, "default.yaml", "roles.yaml")

	cfg, err := Load([]string{"--scenario-dir", dir}, env(nil))
	require.NoError(t, err)
	require.True(t, cfg.ScenarioDirMode())

	entries, err := cfg.ScenarioEntries()
	require.NoError(t, err)
	require.Len(t, entries, 2)

	for _, e := range entries {
		file, err := cfg.OpenScenarioEntry(e.File)
		require.NoError(t, err)
		assert.Equal(t, body(e.File), readAll(t, file))
		require.NoError(t, file.Close())
	}

	fallback, ok := DefaultScenarioEntry(entries)
	require.True(t, ok)
	assert.Equal(t, "default", fallback.Name)

	// The startup scenario path is gone in this mode, so nothing loads a
	// scenario the operator did not put in the directory.
	_, _, err = cfg.OpenScenario()
	require.Error(t, err)

	_, err = cfg.OpenScenarioEntry(filepath.Join(base, "secret.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a plain file name")
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
