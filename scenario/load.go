package scenario

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads, decodes, validates and resolves a scenario file. It returns the
// report even on failure so a caller can log every finding, not just the first.
// Decoding uses yaml.Decoder.KnownFields(true): an unknown scenario key is an
// authoring mistake and must fail loudly, unlike an unknown key in a provider
// request body, which must be tolerated.
//
// Load performs no path containment. It is for tests and testkit, which name
// their own fixtures. The binary must never call it: internal/server resolves a
// mounted scenario through config.OpenScenario, which is os.Root-confined, and
// then calls Parse on the bytes. Wiring Load into the server would silently
// bypass security requirement 7.
func Load(path string) (*Scenario, Report, error) {
	src, err := os.ReadFile(path) //nolint:gosec // documented above: callers name their own fixtures
	if err != nil {
		var r Report
		r.add(SeverityError, "scenario.unreadable", path, "%s", err.Error())
		return nil, r, fmt.Errorf("reading scenario %s: %w", path, err)
	}
	s, report, err := Parse(src)
	if s != nil {
		s.path = path
	}
	return s, report, err
}

// LoadFS reads a scenario from an fs.FS, which is how the embedded built-in
// protocol scenarios are loaded.
func LoadFS(fsys fs.FS, name string) (*Scenario, Report, error) {
	src, err := fs.ReadFile(fsys, name)
	if err != nil {
		var r Report
		r.add(SeverityError, "scenario.unreadable", name, "%s", err.Error())
		return nil, r, fmt.Errorf("reading scenario %s: %w", name, err)
	}
	s, report, err := Parse(src)
	if s != nil {
		s.path = name
	}
	return s, report, err
}

// Parse decodes and validates a scenario from bytes without touching the file
// system. testkit's WithScenarioYAML option uses it.
//
// The schema version is peeked before the strict decode. Without that ordering a
// version: 2 file loaded by a version: 1 binary fails with a wall of unknown-key
// errors instead of the one sentence its reader needs, and versioning is the
// stated migration mechanism for every deferral in this design.
func Parse(src []byte) (*Scenario, Report, error) {
	var r Report

	version, err := peekVersion(src)
	if err != nil {
		r.add(SeverityError, "scenario.version.unreadable", "version", "%s", err.Error())
		return nil, r, r.Err()
	}
	if version != SchemaVersion {
		r.add(SeverityError, "scenario.version.unsupported", "version",
			"scenario declares version %d, but this build of Servicesim understands only version %d; upgrade Servicesim or pin the scenario to version %d",
			version, SchemaVersion, SchemaVersion)
		return nil, r, r.Err()
	}

	s := &Scenario{}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(s); err != nil && !errors.Is(err, io.EOF) {
		r.add(SeverityError, "scenario.decode.failed", "", "%s", err.Error())
		return nil, r, r.Err()
	}

	r = s.Validate()
	if !r.OK() {
		return s, r, r.Err()
	}
	if err := s.Resolve(); err != nil {
		r.add(SeverityError, "scenario.resolve.failed", "", "%s", err.Error())
		return s, r, r.Err()
	}
	return s, r, nil
}

// peekVersion reads only the version key, tolerating every other key, so that a
// version mismatch is reported as one sentence rather than as the strict
// decoder's unknown-key cascade.
func peekVersion(src []byte) (int, error) {
	var probe struct {
		Version *int `yaml:"version"`
	}
	if err := yaml.Unmarshal(src, &probe); err != nil {
		return 0, fmt.Errorf("scenario is not valid YAML: %w", err)
	}
	if probe.Version == nil {
		return 0, fmt.Errorf("scenario declares no version; add \"version: %d\"", SchemaVersion)
	}
	return *probe.Version, nil
}
