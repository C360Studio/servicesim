package testkit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// UpdateGoldenEnv names the environment variable that rewrites a golden file
// instead of comparing against it:
//
//	SERVICESIM_UPDATE_GOLDEN=1 go test ./...
//
// It is an environment variable and not a -update flag on purpose. testkit is an
// ordinary imported package, so a flag.Bool at its package scope would be
// registered at init in every consumer's test binary; a consumer that already
// defines its own -update flag — a near-universal Go convention — would panic
// with "flag redefined: update" and lose the whole binary, with no visible link
// to Servicesim.
const UpdateGoldenEnv = "SERVICESIM_UPDATE_GOLDEN"

// goldenOptions is the resolved configuration of one AssertGoldenJSON call.
type goldenOptions struct {
	ignore   []string
	exactIDs bool
}

// GoldenOption tunes [AssertGoldenJSON].
type GoldenOption func(*goldenOptions)

// GoldenIgnore excludes additional top-level or dotted JSON paths from the
// comparison, for example "usage.total_tokens". A path segment names an object
// key; array indices are not addressable, because a golden that needs to ignore
// one element of an array is a golden that is pinning the wrong thing.
func GoldenIgnore(paths ...string) GoldenOption {
	return func(o *goldenOptions) {
		o.ignore = append(o.ignore, paths...)
	}
}

// GoldenExactIDs opts back into comparing the derived identifier fields that are
// ignored by default. Use it for a route with no declared fault plan, where the
// identifiers are stable across runs and worth pinning.
func GoldenExactIDs() GoldenOption {
	return func(o *goldenOptions) {
		o.exactIDs = true
	}
}

// derivedIDPaths are the identifier fields ignored by default: Exa's requestId,
// Tavily's request_id and Perplexity's top-level id. A route with a declared
// fault plan varies them per attempt by design, because the attempt index is
// part of the identifier tuple, so comparing them would make a retry test fail
// for the one reason it should not.
var derivedIDPaths = []string{"requestId", "request_id", "id"}

// AssertGoldenJSON compares got against the golden file at path, semantically:
// both sides are decoded into any and diffed with go-cmp, because extra-field
// merging reorders keys and JSON object order is not part of any of these wire
// contracts.
//
// By default it ignores the derived identifier fields — requestId, request_id
// and the top-level Perplexity id. [GoldenExactIDs] opts back in, and
// [GoldenIgnore] excludes more.
//
// It performs a plain comparison and knows nothing about provenance: a consumer's
// goldens live in the consumer's repository, and failing their first call with an
// error about a convention that exists only inside Servicesim would be
// indefensible. This repository's own provenance requirement — every golden has
// an entry recording the documentation URL and verification date — is enforced
// over contracts/, where it belongs.
//
// Updating a golden is opt-in through [UpdateGoldenEnv], never through a flag.
func AssertGoldenJSON(tb testing.TB, path string, got []byte, opts ...GoldenOption) {
	tb.Helper()

	o := goldenOptions{}
	for _, opt := range opts {
		opt(&o)
	}

	gotValue, err := decodeJSON(got)
	if err != nil {
		tb.Errorf("the response is not JSON: %v\n%s", err, got)
		return
	}

	if os.Getenv(UpdateGoldenEnv) == "1" {
		writeGolden(tb, path, gotValue)
		return
	}

	raw, err := os.ReadFile(path) //nolint:gosec // the caller names its own golden
	if err != nil {
		tb.Errorf("reading the golden file: %v\nrerun with %s=1 to write it", err, UpdateGoldenEnv)
		return
	}
	wantValue, err := decodeJSON(raw)
	if err != nil {
		tb.Errorf("the golden file %s is not JSON: %v", path, err)
		return
	}

	ignore := o.ignore
	if !o.exactIDs {
		ignore = slices.Concat(derivedIDPaths, ignore)
	}
	for _, p := range ignore {
		wantValue = prune(wantValue, p)
		gotValue = prune(gotValue, p)
	}

	if diff := cmp.Diff(wantValue, gotValue); diff != "" {
		tb.Errorf("response does not match %s (-golden +got):\n%s\nrerun with %s=1 to update it",
			path, diff, UpdateGoldenEnv)
	}
}

// writeGolden rewrites path with the observed response, indented so the file is
// reviewable in a diff. The bytes are not compared to anything afterwards: the
// comparison is semantic, so re-encoding is free, and a golden nobody can read is
// a golden nobody checks.
func writeGolden(tb testing.TB, path string, value any) {
	tb.Helper()

	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		tb.Errorf("encoding the golden file %s: %v", path, err)
		return
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			tb.Errorf("creating the directory for %s: %v", path, err)
			return
		}
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		tb.Errorf("writing the golden file %s: %v", path, err)
		return
	}
	tb.Logf("%s=1: wrote %s", UpdateGoldenEnv, path)
}

// prune removes a dotted path from a decoded JSON value, returning the value
// with the key gone. A path that does not exist is not an error: a golden that
// ignores a field one provider does not emit must not fail for that reason.
func prune(v any, path string) any {
	key, rest, nested := strings.Cut(path, ".")
	obj, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if !nested {
		delete(obj, key)
		return obj
	}
	if child, present := obj[key]; present {
		obj[key] = prune(child, rest)
	}
	return obj
}
