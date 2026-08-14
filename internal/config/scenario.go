package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/c360studio/servicesim/scenarios"
)

// ErrBuiltinScenario reports that [Config.OpenScenario] was called for a
// scenario that lives inside the binary. It is a distinct error so a caller can
// branch on it rather than on the shape of a message: internal/server tries the
// built-in path first, and testkit does the same.
var ErrBuiltinScenario = errors.New("scenario names a built-in, which has no file to open")

// Builtin reports whether ScenarioPath selects an embedded protocol scenario and
// returns its name with the "builtin:" prefix removed. It shares one literal
// with package scenarios rather than repeating it here, so the flag parser and
// the loader cannot drift.
//
// The name is empty when the path is not a built-in, unlike strings.CutPrefix,
// which hands back the whole input: a caller that ignores the boolean must not
// be left holding a file path that looks like a scenario name.
func (c Config) Builtin() (string, bool) {
	name, ok := strings.CutPrefix(c.ScenarioPath, scenarios.Prefix)
	if !ok {
		return "", false
	}
	return name, true
}

// OpenScenario opens the configured scenario within ScenarioRoot. It uses
// os.Root, which refuses to traverse outside the root at the syscall level —
// including through a symlink, which a filepath.Clean prefix check does not
// catch. Verified on Go 1.26.4: opening "../etc/passwd" through an os.Root
// returns "path escapes from parent".
//
// The name handed to os.Root must be root-relative. Verified on Go 1.26.4:
// Root.Open rejects an absolute name even when it resolves inside the root
// ("openat /.../scen/x.yaml: path escapes from parent"), while Open("x.yaml")
// succeeds — and the documented container invocation is
// --scenario /scenarios/fusion-overlap.yaml with ScenarioRoot defaulting to
// /scenarios, which is exactly that case. So OpenScenario computes the path
// relative to the root and rejects a result that is absolute or starts with
// "..", then lets os.Root enforce the rest.
//
// Both paths are made absolute first. filepath.Rel requires its two arguments to
// agree about being absolute, and a relative --scenario against an absolute
// --scenario-root is an ordinary way to invoke this from a shell. Making them
// absolute also applies filepath.Clean, which collapses "scen/../../etc/passwd"
// into a path the relative check above catches lexically, before any syscall.
//
// The second result is the root-relative name that was opened, which is what
// belongs in a log line or a parse error: it identifies the scenario without
// printing the host's directory layout.
func (c Config) OpenScenario() (fs.File, string, error) {
	if name, ok := c.Builtin(); ok {
		return nil, "", fmt.Errorf("--scenario %q: %w; load it with scenarios.Load(%q)",
			c.ScenarioPath, ErrBuiltinScenario, name)
	}
	if c.ScenarioPath == "" {
		return nil, "", errors.New("--scenario: must not be empty")
	}

	root := c.ScenarioRoot
	if root == "" {
		root = filepath.Dir(c.ScenarioPath)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, "", fmt.Errorf("resolving scenario root %q: %w", root, err)
	}
	absPath, err := filepath.Abs(c.ScenarioPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolving scenario %q: %w", c.ScenarioPath, err)
	}

	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return nil, "", fmt.Errorf("scenario %q is not reachable from scenario root %q: %w",
			c.ScenarioPath, root, err)
	}
	if escapes(rel) {
		return nil, "", fmt.Errorf("scenario %q escapes scenario root %q", c.ScenarioPath, root)
	}
	if rel == "." {
		return nil, "", fmt.Errorf("scenario %q is the scenario root %q, not a file", c.ScenarioPath, root)
	}
	name := filepath.ToSlash(rel)

	// The Root's own descriptor is only needed for the openat below. Files
	// obtained from it hold their own descriptor and stay valid after it closes.
	dir, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, "", fmt.Errorf("opening scenario root %q: %w", root, err)
	}
	defer dir.Close()

	file, err := dir.Open(name)
	if err != nil {
		return nil, "", fmt.Errorf("opening scenario %q under root %q: %w", name, root, err)
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, "", fmt.Errorf("stat scenario %q under root %q: %w", name, root, err)
	}
	if info.IsDir() {
		file.Close()
		return nil, "", fmt.Errorf("scenario %q under root %q is a directory, not a file", name, root)
	}

	return file, name, nil
}

// escapes reports whether a root-relative path leaves its root. An absolute
// result cannot happen for two absolute inputs, but is checked anyway because
// the cost of being wrong here is reading an arbitrary file off the host.
func escapes(rel string) bool {
	if filepath.IsAbs(rel) {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
