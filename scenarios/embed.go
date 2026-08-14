package scenarios

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/c360studio/servicesim/scenario"
)

// FS holds the built-in protocol scenario files, each under the "protocol/"
// directory and named "<scenario name>.yaml". Prefer [Load], [Read] and
// [Names]; FS is exported so a consumer can copy a built-in out as the starting
// point for a scenario of its own.
//
//go:embed protocol/*.yaml
var FS embed.FS

// Dir is the directory inside [FS] the built-in scenarios live in. It is also
// the path they take inside the image, so --scenario builtin:happy and
// --scenario /scenarios/protocol/happy.yaml select the same file.
const Dir = "protocol"

// Ext is the file extension every built-in scenario carries.
const Ext = ".yaml"

// Default is the built-in the image serves when no scenario is selected. The
// Dockerfile CMD names the same file.
const Default = "happy"

// Prefix marks a --scenario value as naming a built-in rather than a path, as
// in "builtin:happy". It is exported so the flag parser and testkit share one
// literal instead of two that can drift.
const Prefix = "builtin:"

// names is computed once from the embedded file system. fs.ReadDir returns
// entries sorted by filename, so the order is stable across processes — a Go
// map range here would make an error message that lists the known scenarios
// differ between runs.
var names = sync.OnceValue(func() []string {
	entries, err := fs.ReadDir(FS, Dir)
	if err != nil {
		// Unreachable: the directory is embedded at compile time. Panicking
		// beats returning nil, which would turn every lookup into "unknown
		// scenario" and send the reader hunting in the wrong place.
		panic(fmt.Sprintf("scenarios: reading embedded %s: %v", Dir, err))
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), Ext) {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), Ext))
	}
	return out
})

// Names returns every built-in scenario name in a stable order.
func Names() []string {
	return append([]string(nil), names()...)
}

// Has reports whether name is a built-in scenario. It is also the containment
// check [Read] and [Load] rely on: a name that is not in this set never reaches
// the file system, so no lookup can escape the embedded directory.
func Has(name string) bool {
	for _, n := range names() {
		if n == name {
			return true
		}
	}
	return false
}

// Read returns the raw YAML of a built-in scenario, which is what an admin
// endpoint or a "copy this and edit it" workflow wants.
func Read(name string) ([]byte, error) {
	if !Has(name) {
		return nil, unknownError(name)
	}
	src, err := FS.ReadFile(path.Join(Dir, name+Ext))
	if err != nil {
		return nil, fmt.Errorf("reading built-in scenario %q: %w", name, err)
	}
	return src, nil
}

// Load parses, validates and resolves a built-in scenario. Like
// [scenario.Load], it returns the report even on failure so a caller can log
// every finding rather than only the first.
//
// The projection bodies are not decoded here; provider.ValidateScenario checks
// those, which is why internal/server calls it before readiness reports true.
func Load(name string) (*scenario.Scenario, scenario.Report, error) {
	if !Has(name) {
		err := unknownError(name)
		return nil, scenario.Report{Findings: []scenario.Finding{{
			Severity: scenario.SeverityError,
			Code:     "scenario.builtin.unknown",
			Path:     name,
			Message:  err.Error(),
		}}}, err
	}
	return scenario.LoadFS(FS, path.Join(Dir, name+Ext))
}

// unknownError names every built-in, because the reader hitting this is
// typically in another repository and has no way to list them.
func unknownError(name string) error {
	return fmt.Errorf("unknown built-in scenario %q; this build ships %s",
		name, strings.Join(names(), ", "))
}
