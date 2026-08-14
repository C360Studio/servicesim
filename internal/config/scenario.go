package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/scenarios"
)

// ErrBuiltinScenario reports that [Config.OpenScenario] was called for a
// scenario that lives inside the binary. It is a distinct error so a caller can
// branch on it rather than on the shape of a message: internal/server tries the
// built-in path first, and testkit does the same.
var ErrBuiltinScenario = errors.New("scenario names a built-in, which has no file to open")

// ScenarioEntry names one scenario found under --scenario-dir.
type ScenarioEntry struct {
	// Name is the selector the /x/<scenario> path prefix matches: the file's
	// base name with its extension removed, so "roles.yaml" is served as
	// "/x/roles". Names are unique within a directory; [Config.ScenarioEntries]
	// refuses to return two entries that share one.
	Name string

	// File is the entry's name within --scenario-dir. It is what belongs in a
	// log line or a parse error: it identifies the scenario without printing
	// the host's directory layout, and it is what [Config.OpenScenarioEntry]
	// takes.
	File string
}

// scenarioExtensions are the file extensions [Config.ScenarioEntries] treats as
// scenarios. Everything else in the directory is ignored rather than rejected,
// because a scenario directory is frequently a mounted ConfigMap or a checked-in
// fixture folder that also holds a README, a checksum or the projected-volume
// bookkeeping Kubernetes writes beside the keys.
var scenarioExtensions = []string{".yaml", ".yml"}

// ScenarioDirMode reports whether this process serves a directory of scenarios
// rather than the single scenario ScenarioPath names. The two are mutually
// exclusive, so it is the one question a caller has to ask before deciding
// between [Config.OpenScenario] and [Config.ScenarioEntries].
func (c Config) ScenarioDirMode() bool {
	return c.ScenarioDir != ""
}

// ScenarioEntries lists every scenario under --scenario-dir, ordered by name.
// The caller loads and validates all of them before readiness reports true;
// nothing about the directory is resolved lazily at request time.
//
// The directory is untrusted input — it is typically a mount — so every name is
// resolved through [os.Root], which refuses to traverse outside it at the
// syscall level, symlinks included. A symlink that stays inside the directory
// resolves normally, which is what a Kubernetes ConfigMap mount is made of.
//
// Two files whose names collide after the extension is dropped are an error
// naming both, because /x/<name> could then mean either. Discovering that at
// request time, as a response from the wrong scenario, would be miserable; the
// point of loading the directory at startup is that it cannot happen.
//
// Ordering is by name rather than by directory order, so a log line listing the
// loaded scenarios and an error naming a colliding pair read the same on every
// run and on every file system.
//
// An empty result is an error rather than a process that answers nothing: a
// mount that resolved to the wrong path, or a directory of files nobody named
// with a scenario extension, is a startup mistake and belongs in the startup
// error.
func (c Config) ScenarioEntries() ([]ScenarioEntry, error) {
	root, err := c.scenarioDirRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()

	dir, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("opening scenario directory %q: %w", c.ScenarioDir, err)
	}
	listing, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return nil, fmt.Errorf("reading scenario directory %q: %w", c.ScenarioDir, err)
	}

	// Sorted before the scan, so the pair a collision error names is the same
	// pair on every run: ReadDir promises no order at all.
	names := make([]string, 0, len(listing))
	for _, item := range listing {
		names = append(names, item.Name())
	}
	slices.Sort(names)

	entries := make([]ScenarioEntry, 0, len(names))
	byName := make(map[string]string, len(names))
	for _, file := range names {
		name, ok := scenarioName(file)
		if !ok {
			continue
		}
		// Stat through the root rather than trusting the directory entry's
		// type: an entry may be a symlink, and only the root can say whether
		// its target is a regular file inside the directory.
		info, err := root.Stat(file)
		if err != nil {
			return nil, fmt.Errorf("scenario %q in scenario directory %q: %w", file, c.ScenarioDir, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if first, dup := byName[name]; dup {
			return nil, fmt.Errorf(
				"scenario directory %q: %q and %q both define scenario %q, so /x/%s would be ambiguous",
				c.ScenarioDir, first, file, name, name)
		}
		byName[name] = file
		entries = append(entries, ScenarioEntry{Name: name, File: file})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("scenario directory %q: holds no scenario files (%s)",
			c.ScenarioDir, strings.Join(scenarioExtensions, ", "))
	}

	slices.SortFunc(entries, func(a, b ScenarioEntry) int { return strings.Compare(a.Name, b.Name) })
	return entries, nil
}

// OpenScenarioEntry opens one file within --scenario-dir. The name is the File
// field of a [ScenarioEntry], and containment is enforced here rather than
// assumed from where the name came: the caller may have taken it from anywhere,
// and the cost of being wrong is reading an arbitrary file off the host.
//
// A scenario directory is flat by design, so anything but a plain file name is
// rejected before [os.Root] is asked. That keeps "../secret.yaml" and
// "nested/deep.yaml" alike out of the directory's name space, and it means
// [ScenarioEntry.Name] can stay a single path segment for /x/<scenario>.
func (c Config) OpenScenarioEntry(file string) (fs.File, error) {
	if file == "" {
		return nil, fmt.Errorf("scenario directory %q: no scenario file named", c.ScenarioDir)
	}
	if file != filepath.Base(file) || file == "." || file == ".." {
		return nil, fmt.Errorf("scenario %q in scenario directory %q: not a plain file name",
			file, c.ScenarioDir)
	}

	root, err := c.scenarioDirRoot()
	if err != nil {
		return nil, err
	}
	// The Root's own descriptor is only needed for the openat below. Files
	// obtained from it hold their own descriptor and stay valid after it closes.
	defer root.Close()

	opened, err := root.Open(file)
	if err != nil {
		return nil, fmt.Errorf("opening scenario %q in scenario directory %q: %w", file, c.ScenarioDir, err)
	}
	info, err := opened.Stat()
	if err != nil {
		opened.Close()
		return nil, fmt.Errorf("stat scenario %q in scenario directory %q: %w", file, c.ScenarioDir, err)
	}
	if !info.Mode().IsRegular() {
		opened.Close()
		return nil, fmt.Errorf("scenario %q in scenario directory %q is not a regular file", file, c.ScenarioDir)
	}
	return opened, nil
}

// DefaultScenarioEntry reports which entry serves a request that carries no
// /x/<scenario> prefix, and whether the directory can supply one at all.
//
// The entry named [ScenarioDirDefault] wins. Failing that, a directory holding
// exactly one scenario supplies it, because there is nothing else it could
// mean. Otherwise there is no default: several scenarios and no stated default
// is a directory whose unprefixed URL is genuinely ambiguous, and the caller
// must fail that request closed rather than pick one.
func DefaultScenarioEntry(entries []ScenarioEntry) (ScenarioEntry, bool) {
	for _, e := range entries {
		if e.Name == ScenarioDirDefault {
			return e, true
		}
	}
	if len(entries) == 1 {
		return entries[0], true
	}
	return ScenarioEntry{}, false
}

// scenarioDirRoot opens --scenario-dir as a containment root. The path is made
// absolute first, which also applies filepath.Clean, so the root a name is
// resolved against does not depend on the working directory a shell happened to
// use.
func (c Config) scenarioDirRoot() (*os.Root, error) {
	if c.ScenarioDir == "" {
		return nil, errors.New("--scenario-dir: must not be empty")
	}
	abs, err := filepath.Abs(c.ScenarioDir)
	if err != nil {
		return nil, fmt.Errorf("resolving scenario directory %q: %w", c.ScenarioDir, err)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("opening scenario directory %q: %w", c.ScenarioDir, err)
	}
	return root, nil
}

// scenarioName returns the /x/<scenario> selector a file name supplies, and
// whether the file is a scenario at all.
//
// A dot-prefixed name is not: editors leave ".happy.yaml.swp" behind and
// Kubernetes projects "..data" beside the keys of a ConfigMap, and neither is a
// scenario an operator meant to serve. That check also settles the only way the
// selector could come out empty — a file named exactly ".yaml" — so what is
// returned is always an ordinary path segment, which is what /x/<scenario>
// needs it to be.
//
// The extension is matched case-insensitively but trimmed by its original
// spelling, so a file mounted as "Roles.YAML" is served as /x/Roles rather than
// as a name with a stray suffix.
func scenarioName(file string) (string, bool) {
	if strings.HasPrefix(file, ".") {
		return "", false
	}
	ext := filepath.Ext(file)
	if !slices.Contains(scenarioExtensions, strings.ToLower(ext)) {
		return "", false
	}
	return strings.TrimSuffix(file, ext), true
}

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
