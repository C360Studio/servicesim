package servicesim

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// buildingAProfileGuide is the guide whose Go and YAML blocks are excerpts of
// examples/profile/**, and excerptRoot is the only tree those excerpts may
// come from — the guide's whole claim is "this code is that module", so a
// marker naming any other path is a mistake in the marker, not a wider
// convention.
const (
	buildingAProfileGuide = "docs/building-a-profile.md"
	excerptRoot           = "examples/profile/"
)

// minExcerpts is a floor on the number of marked blocks the guide is expected
// to carry. It guards against this test passing vacuously: a guide rewritten
// with no markers at all, or a marker regexp that stopped matching, would
// otherwise report success forever — the same reason examples/profile's
// imports_test.go carries wantGoFiles.
const minExcerpts = 12

// excerptMarker is the convention docs/building-a-profile.md documents at its
// top: an HTML comment naming a file under examples/profile/ and an inclusive
// 1-based line range, on the line immediately before the fenced block it
// vouches for. Line ranges rather than named regions in the source, so the
// module's own files stay exactly what an adopter would copy — free of
// excerpt bookkeeping — at the cost that an edit above a quoted range moves
// it; when that happens this test reports the range the block now occupies.
var excerptMarker = regexp.MustCompile(`^<!-- excerpt: (` + regexp.QuoteMeta(excerptRoot) + `[^ #]+)#L([0-9]+)-L([0-9]+) -->$`)

// fenceOpener matches a fenced-code opening line and captures its language.
// Leading whitespace is allowed so an indented fence — legal Markdown inside a
// list item — cannot slip a hand-typed block past the marker requirement
// below; readFence's closing match is equally lenient for the same reason.
var fenceOpener = regexp.MustCompile("^\\s*```([A-Za-z0-9_-]*)$")

// fenceCloser matches a fenced-code closing line, at any indentation.
var fenceCloser = regexp.MustCompile("^\\s*```\\s*$")

// codeLanguages are the fence languages every block in the guide must be an
// excerpt of, spelled the several ways Markdown accepts. The comparison
// lower-cases first, so `Go` and `YAML` are covered too: a language alias is
// the cheapest way to write a block that only LOOKS checked.
var codeLanguages = map[string]bool{
	"go":     true,
	"golang": true,
	"yaml":   true,
	"yml":    true,
}

// TestBuildingAProfileExcerptsMatchTheModule proves docs/building-a-profile.md
// describes code that exists: every fenced Go or YAML block in the guide
// carries an excerpt marker, and the block's text is byte-for-byte the lines
// the marker names in examples/profile/**. A block that drifted from its
// source — because the module was edited, or the guide was — fails here,
// with the range the block moved to when it still exists contiguously in the
// file, and the first differing line when it does not.
func TestBuildingAProfileExcerptsMatchTheModule(t *testing.T) {
	f, err := os.Open(buildingAProfileGuide)
	if err != nil {
		t.Fatalf("opening the guide: %v", err)
	}
	defer func() { _ = f.Close() }()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading the guide: %v", err)
	}

	excerpts := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if m := excerptMarker.FindStringSubmatch(line); m != nil {
			markerLine := i + 1
			if i+1 >= len(lines) || !fenceOpener.MatchString(lines[i+1]) {
				t.Errorf("%s:%d: excerpt marker is not immediately followed by a fenced code block", buildingAProfileGuide, markerLine)
				continue
			}
			block, end, ok := readFence(lines, i+1)
			if !ok {
				t.Errorf("%s:%d: fenced block after the excerpt marker is never closed", buildingAProfileGuide, markerLine+1)
				return
			}
			excerpts++
			checkExcerpt(t, markerLine, m[1], m[2], m[3], block)
			i = end
			continue
		}

		if m := fenceOpener.FindStringSubmatch(line); m != nil {
			lang := strings.ToLower(m[1])
			if codeLanguages[lang] {
				t.Errorf("%s:%d: a ```%s block with no excerpt marker on the line before it — every Go and YAML block in this guide is an excerpt of %s (see the guide's own top)",
					buildingAProfileGuide, i+1, m[1], excerptRoot)
			}
			_, end, ok := readFence(lines, i)
			if !ok {
				t.Errorf("%s:%d: fenced block is never closed", buildingAProfileGuide, i+1)
				return
			}
			i = end
		}
	}

	if excerpts < minExcerpts {
		t.Fatalf("found %d excerpt marker(s) in %s, want at least %d — either the guide lost its excerpts or the marker regexp stopped matching",
			excerpts, buildingAProfileGuide, minExcerpts)
	}
}

// readFence returns the lines strictly inside the fence opened at lines[open]
// and the index of its closing line.
func readFence(lines []string, open int) (block []string, end int, ok bool) {
	for j := open + 1; j < len(lines); j++ {
		if fenceCloser.MatchString(lines[j]) {
			return lines[open+1 : j], j, true
		}
	}
	return nil, 0, false
}

// TestBuildingAProfileModuleTreeIsCurrent checks the guide's "module layout"
// tree, which is the one block describing the module that the excerpt
// mechanism cannot vouch for: it is prose in a ```text fence, not a line range
// of any file. Without this, a file added to or removed from examples/profile
// leaves the guide's own map of the module stale with no gate firing — the
// exact drift class the excerpt markers exist to kill.
//
// The check is deliberately shallow: every file name the tree lists must exist
// in the module, and every file in the module must be listed. Directory
// structure and the per-file descriptions are left to review.
func TestBuildingAProfileModuleTreeIsCurrent(t *testing.T) {
	listed := moduleTreeLeaves(t)
	if len(listed) == 0 {
		t.Fatalf("found no file names in %s's module-layout tree; the block or this parser moved", buildingAProfileGuide)
	}

	actual := map[string]bool{}
	err := filepath.WalkDir(excerptRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		// go.sum is deliberately absent from the guide's tree: it is a lock
		// file, not something an adopter writes or reads. The contracts
		// bundle is summarised as one line rather than enumerated.
		if name == "go.sum" || strings.Contains(filepath.ToSlash(path), "/contracts/") {
			return nil
		}
		actual[name] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", excerptRoot, err)
	}

	for name := range listed {
		if !actual[name] {
			t.Errorf("%s's module-layout tree lists %q, which no longer exists under %s", buildingAProfileGuide, name, excerptRoot)
		}
	}
	for name := range actual {
		if !listed[name] {
			t.Errorf("a file named %q exists under %s but is not in %s's module-layout tree", name, excerptRoot, buildingAProfileGuide)
		}
	}
}

// treeEntry matches one line of the guide's module-layout tree: the drawing
// prefix, then the name. A name ending in "/" is a directory and is not a leaf.
var treeEntry = regexp.MustCompile(`^[ │]*[├└]── ([^ /]+)(/?)`)

// moduleTreeLeaves returns the file names the guide's module-layout block
// lists — the name on every tree line that is not a directory.
func moduleTreeLeaves(t *testing.T) map[string]bool {
	t.Helper()

	src, err := os.ReadFile(buildingAProfileGuide)
	if err != nil {
		t.Fatalf("opening the guide: %v", err)
	}
	lines := strings.Split(string(src), "\n")

	for i, line := range lines {
		if !strings.HasPrefix(line, "```text") || i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], excerptRoot) {
			continue
		}
		block, _, ok := readFence(lines, i)
		if !ok {
			t.Fatalf("%s: the module-layout block is never closed", buildingAProfileGuide)
		}
		out := map[string]bool{}
		for _, entry := range block {
			m := treeEntry.FindStringSubmatch(entry)
			if m == nil || m[2] == "/" {
				continue
			}
			out[m[1]] = true
		}
		return out
	}
	t.Fatalf("%s: found no ```text block whose first line is %s", buildingAProfileGuide, excerptRoot)
	return nil
}

// checkExcerpt compares one marked block against the named lines of its
// source file.
func checkExcerpt(t *testing.T, markerLine int, path, fromStr, toStr string, block []string) {
	t.Helper()
	where := fmt.Sprintf("%s:%d", buildingAProfileGuide, markerLine)

	from, err1 := strconv.Atoi(fromStr)
	to, err2 := strconv.Atoi(toStr)
	if err1 != nil || err2 != nil || from < 1 || to < from {
		t.Errorf("%s: malformed line range L%s-L%s", where, fromStr, toStr)
		return
	}

	src, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Errorf("%s: excerpt names %s, which cannot be read: %v", where, path, err)
		return
	}
	srcLines := strings.Split(strings.TrimSuffix(string(src), "\n"), "\n")
	if to > len(srcLines) {
		t.Errorf("%s: excerpt names %s#L%d-L%d but the file has %d line(s)", where, path, from, to, len(srcLines))
		return
	}

	want := srcLines[from-1 : to]
	if equalLines(block, want) {
		return
	}

	// The block still exists in the file, somewhere else: the common,
	// mechanical case (an edit above the range moved it). Say where.
	if start := findRun(srcLines, block); start > 0 {
		t.Errorf("%s: excerpt %s#L%d-L%d has moved — the block now sits at L%d-L%d; update the marker",
			where, path, from, to, start, start+len(block)-1)
		return
	}

	// It does not: the source changed under the guide, or the guide was
	// hand-edited. Show the first difference.
	n := min(len(block), len(want))
	for k := 0; k < n; k++ {
		if block[k] != want[k] {
			t.Errorf("%s: excerpt %s#L%d-L%d has drifted at L%d:\n  guide:  %q\n  source: %q\nre-copy the block from the source, or fix the source",
				where, path, from, to, from+k, block[k], want[k])
			return
		}
	}
	t.Errorf("%s: excerpt %s#L%d-L%d has drifted: the guide's block is %d line(s), the source range is %d",
		where, path, from, to, len(block), len(want))
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// findRun returns the 1-based line at which block appears contiguously in
// src, or 0.
func findRun(src, block []string) int {
	if len(block) == 0 {
		return 0
	}
	for i := 0; i+len(block) <= len(src); i++ {
		if equalLines(src[i:i+len(block)], block) {
			return i + 1
		}
	}
	return 0
}
