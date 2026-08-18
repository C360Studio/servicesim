// Package main_test carries the two guards that make this module's own
// claim to be "written the way an adopter writes one" mechanical rather than
// asserted: no file anywhere in the module imports servicesim/internal/...
// (which Go's own import rules make unreachable from outside the servicesim
// module regardless — this guard exists so that fact is proven, not merely
// relied on), and go.mod requires nothing beyond servicesim itself plus
// testify, which is the dependency budget every consumer of this module
// inherits.
package main_test

import (
	"bufio"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// wantGoFiles is the number of .go files this module holds across every
// package. It guards against the scan itself passing vacuously: a walk that
// silently found nothing to check would report success forever. It is the
// EXACT count rather than a slack floor — the module is nine files and is
// meant to stay small enough to read end to end, so a floor one below the
// real count would tolerate losing a file, which is the thing a vacuity guard
// is for.
const wantGoFiles = 9

// TestModuleUsesOnlyServicesimsPublicSurface parses every .go file in this
// module — main.go, acme/*.go, and this file itself — and fails on any
// import naming an internal package, at any depth, under any module path.
// This is examples/imports_test.go's own guard, generalised to a module
// that Go's import rules already enforce against unreachable internal
// packages from the outside; running it here proves the claim rather than
// leaving it implicit.
func TestModuleUsesOnlyServicesimsPublicSurface(t *testing.T) {
	fset := token.NewFileSet()
	scanned := 0

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		scanned++

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parsing %s: %v", path, err)
			return nil
		}

		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("unquoting an import path in %s: %v", path, err)
				continue
			}
			if isInternalPath(importPath) {
				t.Errorf("%s imports %q, which a consumer of servicesim cannot import "+
					"(Go's own import rules make internal/... unreachable from outside its module) — "+
					"an out-of-tree profile may only use servicesim/provider, servicesim/scenario, "+
					"servicesim/testkit, servicesim/contracts, servicesim/scenarios and servicesim itself",
					path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module tree: %v", err)
	}

	if scanned < wantGoFiles {
		t.Fatalf("the guard scanned %d .go files, want at least %d; if files were added or removed, "+
			"update wantGoFiles", scanned, wantGoFiles)
	}
}

// isInternalPath reports whether an import path names an internal package
// under any module — the "internal" element may be first, last or in the
// middle, and each position is equally unimportable from outside.
func isInternalPath(path string) bool {
	if path == "internal" {
		return true
	}
	return strings.HasPrefix(path, "internal/") ||
		strings.Contains(path, "/internal/") ||
		strings.HasSuffix(path, "/internal")
}

// requireLine is one module path go.mod's require block names, and whether
// it is marked "// indirect".
type requireLine struct {
	path     string
	indirect bool
}

// parseGoModRequires extracts every require line from go.mod — both the
// single-line form ("require path version") and the block form ("require (
// ... )") — with a hand-written scanner rather than golang.org/x/mod/modfile,
// which this test exists to keep OUT of go.mod: pulling in a parser to prove
// a dependency budget would be self-defeating.
func parseGoModRequires(t *testing.T) []requireLine {
	t.Helper()

	f, err := os.Open("go.mod")
	if err != nil {
		t.Fatalf("opening go.mod: %v", err)
	}
	defer func() { _ = f.Close() }()

	var out []requireLine
	inBlock := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		switch {
		case line == "require (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case inBlock:
			out = append(out, parseRequireFields(line))
		case strings.HasPrefix(line, "require "):
			out = append(out, parseRequireFields(strings.TrimPrefix(line, "require ")))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning go.mod: %v", err)
	}
	return out
}

// parseRequireFields parses one require entry's fields: "path version" with
// an optional trailing "// indirect" comment.
func parseRequireFields(line string) requireLine {
	indirect := strings.Contains(line, "// indirect")
	if idx := strings.Index(line, "//"); idx >= 0 {
		line = line[:idx]
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return requireLine{}
	}
	return requireLine{path: fields[0], indirect: indirect}
}

// TestGoModDependencyBudget proves this module's go.mod requires nothing
// beyond github.com/c360studio/servicesim itself and testify (allowed for
// tests, the way examples/ is in the main module — CLAUDE.md's "Dependency
// budget") — the exact set a consumer who writes one profile inherits by
// pinning this module.
func TestGoModDependencyBudget(t *testing.T) {
	requires := parseGoModRequires(t)
	if len(requires) == 0 {
		t.Fatal("parseGoModRequires found no require lines at all; the parser is broken, not the module")
	}

	const (
		servicesimModule = "github.com/c360studio/servicesim"
		testifyModule    = "github.com/stretchr/testify"
	)
	// Everything else in a tidy go.mod for this module is an INDIRECT
	// dependency of servicesim itself — go-cmp (testkit's own golden and
	// stream comparisons) and yaml.v3 (scenario parsing) — not something
	// this module names directly, and not something an adopter chose. If a
	// test here ever imports testify directly (CLAUDE.md's "Dependency
	// budget" allows it, the way examples/ does in the main module), its
	// own indirect dependencies (go-spew, go-difflib) join this list too.
	allowedIndirect := map[string]bool{
		"github.com/google/go-cmp":      true,
		"gopkg.in/yaml.v3":              true,
		"github.com/davecgh/go-spew":    true,
		"github.com/pmezard/go-difflib": true,
	}

	for _, req := range requires {
		switch {
		case req.path == servicesimModule:
			continue
		case req.path == testifyModule && !req.indirect:
			continue
		case req.indirect && allowedIndirect[req.path]:
			continue
		default:
			t.Errorf("go.mod requires %s (indirect=%v), which is outside the documented dependency budget "+
				"(servicesim itself, plus testify for tests); CLAUDE.md's \"Dependency budget\" applies to "+
				"this module too — a consumer pinning it should not inherit an undocumented transitive "+
				"dependency", req.path, req.indirect)
		}
	}
}
