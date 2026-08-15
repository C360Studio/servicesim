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
	if !versionSupported(version, SchemaVersion) {
		r.add(SeverityError, "scenario.version.unsupported", "version",
			"%s", unsupportedVersionMessage(version, SchemaVersion))
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

// versionSupported reports whether a scenario declaring schema version declared
// can be loaded by a build whose own schema version is build.
//
// The comparison is deliberately asymmetric. An older file loads on a newer
// build, because the only compatible way to extend this schema is to add
// OPTIONAL keys — so every key a v1 file can carry is still a known key under a
// v2 build's KnownFields(true) decode. A newer file does not load on an older
// build, because the keys it carries do not exist here, and failing loudly is
// the entire point of the strict decoder.
//
// Strict equality — what this replaced — meant that the day SchemaVersion became
// 2, every scenario file in every adopting repository would stop loading until
// someone hand-edited it. That is an N-repository migration bought for nothing,
// and it contradicts the compatibility argument the open registry was adopted on.
//
// Versions below 1 are rejected rather than waved through as
// older-and-therefore-fine. Zero is what an author gets from a typo or an
// unrendered template, never from a released schema, and accepting it would
// decode a file against an envelope nobody ever specified.
func versionSupported(declared, build int) bool {
	return declared >= 1 && declared <= build
}

// unsupportedVersionMessage explains a rejected version in one sentence, naming
// the range this build accepts so the reader knows whether the fix is to upgrade
// Servicesim or to correct the file.
func unsupportedVersionMessage(declared, build int) string {
	if declared < 1 {
		return fmt.Sprintf(
			"scenario declares version %d, but a schema version is at least 1; this build of Servicesim understands %s",
			declared, versionRange(build))
	}
	return fmt.Sprintf(
		"scenario declares version %d, but this build of Servicesim understands only %s; upgrade Servicesim or pin the scenario to version %d",
		declared, versionRange(build), build)
}

// versionRange renders the accepted range, collapsing the single-version case so
// a v1 build says "version 1" rather than "versions 1 through 1".
func versionRange(build int) string {
	if build <= 1 {
		return fmt.Sprintf("version %d", build)
	}
	return fmt.Sprintf("versions 1 through %d", build)
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
