package paidhosts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/internal/paidhosts"
)

// basePatternLine finds scripts/lint-no-live-hosts.sh's hand-kept
// BASE_PATTERN='...' assignment.
var basePatternLine = regexp.MustCompile(`(?m)^BASE_PATTERN='([^']*)'`)

// TestBaseMatchesGuardScript is what makes paidhosts.Base "the one source"
// the shell script and this package's Go callers both answer to, even
// though the script cannot import a Go package and keeps its own literal
// copy. A shell string can encode a host list the script's author read
// differently than Base was written (an extra host, a dropped one, a typo
// in the escaping) with no compiler to catch it; this test is that
// compiler.
func TestBaseMatchesGuardScript(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot locate the repository root")
	}
	// this file is internal/paidhosts/paidhosts_test.go; the repository root
	// is two directories up.
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	scriptPath := filepath.Join(root, "scripts", "lint-no-live-hosts.sh")

	src, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("reading %s: %v", scriptPath, err)
	}

	m := basePatternLine.FindSubmatch(src)
	if m == nil {
		t.Fatalf("%s: no BASE_PATTERN='...' line found", scriptPath)
	}

	var scriptHosts []string
	for _, alt := range strings.Split(string(m[1]), "|") {
		// The script escapes regex metacharacters (only "." appears in this
		// list) with a backslash; undo that to recover the literal host.
		scriptHosts = append(scriptHosts, strings.ReplaceAll(alt, `\.`, "."))
	}

	want := slices.Clone(paidhosts.Base)
	slices.Sort(want)
	slices.Sort(scriptHosts)

	if !slices.Equal(want, scriptHosts) {
		t.Fatalf("scripts/lint-no-live-hosts.sh's BASE_PATTERN and paidhosts.Base have drifted:\n"+
			"script: %v\ngo:     %v\n"+
			"update both in the same change", scriptHosts, want)
	}
}
