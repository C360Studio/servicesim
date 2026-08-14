package examples_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// wantExampleFiles is the number of .go files this directory is expected to
// hold, as a floor. It guards against the guard itself passing vacuously: a
// scan that silently found nothing to check would report success forever.
const wantExampleFiles = 6

// TestExamplesUseOnlyTheirModulesPublicSurface is the guard that recovers the
// realism these files would otherwise lose by living inside the main module.
//
// A consuming repository imports github.com/c360studio/servicesim by path, and
// Go's import rules make .../servicesim/internal/... unreachable to it. These
// files can reach it — they are inside the module — so nothing stops an example
// from being written against a package no reader could ever import. That example
// would compile here, pass CI here, and fail on the first line a consumer
// copied. Parsing the directory is cheap; discovering it that way is not.
func TestExamplesUseOnlyTheirModulesPublicSurface(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	scanned := 0

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		scanned++

		file, err := parser.ParseFile(fset, entry.Name(), nil, parser.ImportsOnly)
		require.NoError(t, err, "parsing %s", entry.Name())

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err, "unquoting an import path in %s", entry.Name())

			if isInternalPath(path) {
				t.Errorf("%s imports %q, which a consumer of this module cannot import. "+
					"An example may only use packages outside internal/ — testkit, provider and "+
					"its per-vendor subpackages, scenario and scenarios.",
					entry.Name(), path)
			}
		}
	}

	require.GreaterOrEqual(t, scanned, wantExampleFiles,
		"the guard scanned %d files; if examples were renamed, update wantExampleFiles", scanned)
}

// isInternalPath reports whether an import path names an internal package under
// any module — the "internal" element may be first, last or in the middle, and
// each position is equally unimportable from outside.
func isInternalPath(path string) bool {
	if path == "internal" {
		return true
	}
	return strings.HasPrefix(path, "internal/") ||
		strings.Contains(path, "/internal/") ||
		strings.HasSuffix(path, "/internal")
}
