package profiles_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// noPrivilegeRoots is every directory this repository's own code is held to
// the no-privilege rule in: nothing here may import
// github.com/c360studio/servicesim/profiles/... or any of its subpackages. A
// reference profile has no privilege an out-of-tree profile lacks
// (docs/proposals/framework-seam.md, "The four reference profiles"), and the
// only way to prove that by construction — rather than by review, which
// disappears with the PR the framework is meant to stop babysitting — is to
// parse every non-test file under each root and fail on the import.
//
// Paths are relative to this package (profiles/), so ".." is the repository
// root: servicesim.go, doc.go and friends, scanned non-recursively below —
// walking it recursively would re-descend into every other root here, and
// into cmd/ and examples/, both deliberately exempt: cmd/servicesim (the
// registration site) and examples/ (a consumer) are allowed to import a
// reference profile, and each already proves it compiles by being part of
// `go build ./...`.
var noPrivilegeRoots = []string{
	"../provider",
	"../internal",
	"../testkit",
	"../scenario",
	"../contracts",
}

// repoRoot is the repository root, scanned separately and non-recursively
// (see noPrivilegeRoots's doc comment for why).
const repoRoot = ".."

// wantScannedFiles is a floor on how many non-test .go files the walk below
// must find, across every root combined. A filter that silently matched
// nothing would report success forever; this guards against that the same
// way examples/imports_test.go's wantExampleFiles does.
const wantScannedFiles = 40

// TestNoRootImportsAProfilePackage is the no-privilege proof: it parses every
// non-test .go file under the roots above and fails if any of them imports
// github.com/c360studio/servicesim/profiles or one of its subpackages.
//
// Test files are exempt on purpose — testkit's own tests and the root
// module's integration tests need all four reference profiles to exercise
// the framework end to end (docs/proposals/framework-seam.md: "testkit's own
// tests and examples/ may use it where they want all four"), and that need
// is about test fixtures, not about the framework's production code gaining
// a privilege an out-of-tree profile does not have.
func TestNoRootImportsAProfilePackage(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	scanned := 0

	for _, root := range noPrivilegeRoots {
		scanned += checkTree(t, fset, root)
	}
	scanned += checkDir(t, fset, repoRoot)

	require.GreaterOrEqual(t, scanned, wantScannedFiles,
		"the guard scanned %d non-test .go files; if package layout changed, update wantScannedFiles", scanned)
}

// checkTree walks root recursively and checks every non-test .go file it
// finds, returning how many it checked.
func checkTree(t *testing.T, fset *token.FileSet, root string) int {
	t.Helper()

	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err, "walking %s", path)
		if d.IsDir() {
			return nil
		}
		if checkFile(t, fset, path) {
			scanned++
		}
		return nil
	})
	require.NoError(t, err, "walking root %s", root)
	return scanned
}

// checkDir checks every non-test .go file directly inside dir, without
// descending into subdirectories, returning how many it checked.
func checkDir(t *testing.T, fset *token.FileSet, dir string) int {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "reading %s", dir)

	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if checkFile(t, fset, filepath.Join(dir, entry.Name())) {
			scanned++
		}
	}
	return scanned
}

// checkFile parses path (a candidate .go file) and fails the test if it
// imports a profile package. It reports whether path was a non-test .go file
// it actually checked, so callers can count the files the guard covered.
func checkFile(t *testing.T, fset *token.FileSet, path string) bool {
	t.Helper()

	if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
		return false
	}

	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	require.NoError(t, err, "parsing %s", path)

	for _, spec := range file.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		require.NoError(t, err, "unquoting an import path in %s", path)

		if isProfilePath(imp) {
			t.Errorf("%s imports %q: nothing under provider/, internal/, testkit/, "+
				"scenario/, contracts/ or the repository root may import a profile "+
				"package — a reference profile has no privilege an out-of-tree one lacks",
				path, imp)
		}
	}
	return true
}

// isProfilePath reports whether an import path names
// github.com/c360studio/servicesim/profiles itself or one of its
// subpackages (.../profiles/exa and friends).
func isProfilePath(path string) bool {
	const module = "github.com/c360studio/servicesim/profiles"
	return path == module || strings.HasPrefix(path, module+"/")
}
