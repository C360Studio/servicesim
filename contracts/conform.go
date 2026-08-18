package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Conform runs the generic provenance and golden discipline against fsys as
// a set of named subtests: every golden is valid JSON, every golden has a
// provenance record and vice versa, the records are complete, the bundle
// carries the coverage the simulator's own tests depend on, the spec: block
// (when present) is well formed and dated no earlier than the bundle's own
// verification, [Read] rejects a non-bare name, and the fs.FS holds nothing
// but goldens, provenance.yaml and README.md.
//
// It is the whole of what used to be nine `contracts_test.go` loops and two
// `provenance_internal_test.go` ones, generalised: it knows nothing about
// which vendor fsys belongs to, so a wire-shape pin — "Exa results carry no
// score", "Tavily's response_time is a number" and the rest — is NOT here.
// Those stay the calling profile's own test, reading through
// [Read](p.Contracts, name); Conform cannot check a shape it has never
// heard of.
//
// Each subtest keeps the name of the old, provider-specific test it
// generalises, so a failure a maintainer has seen before still reads the
// same. tb is [testing.TB] rather than *testing.T because an out-of-tree
// caller's own conformance test — and this package's — may run it from a
// benchmark or a hand-built stub; subtests are still produced whenever tb
// (or, in a wrapping call from [testkit.ValidateProfile], the *testing.T it
// was ultimately given) supports Run.
func Conform(tb testing.TB, fsys fs.FS) {
	tb.Helper()

	if hint, ok := embedRootMistakeHint(fsys); ok {
		tb.Errorf("%s", hint)
		return
	}

	subtest(tb, "GoldensAreValidJSON", func(tb testing.TB) { checkGoldensAreValidJSON(tb, fsys) })
	subtest(tb, "EveryGoldenHasProvenance", func(tb testing.TB) { checkEveryGoldenHasProvenance(tb, fsys) })
	subtest(tb, "EveryProvenanceEntryHasGolden", func(tb testing.TB) { checkEveryProvenanceEntryHasGolden(tb, fsys) })
	subtest(tb, "ProvenanceRecordsAreComplete", func(tb testing.TB) { checkProvenanceRecordsAreComplete(tb, fsys) })
	subtest(tb, "EveryProviderHasHappyAndEmptyAndErrorGoldens", func(tb testing.TB) { checkCoverage(tb, fsys) })
	subtest(tb, "SpecBlockIsWellFormed", func(tb testing.TB) { checkSpecBlockIsWellFormed(tb, fsys) })
	subtest(tb, "ProviderVerifiedIsAtLeastEveryEntry", func(tb testing.TB) { checkProviderVerifiedIsAtLeastEveryEntry(tb, fsys) })
	subtest(tb, "APIVersionPresentIffFromSpec", func(tb testing.TB) { checkAPIVersionPresentIffFromSpec(tb, fsys) })
	subtest(tb, "ReadRejectsNonBareNames", func(tb testing.TB) { checkReadRejectsNonBareNames(tb, fsys) })
	subtest(tb, "FSHoldsOnlyGoldensAndProvenance", func(tb testing.TB) { checkFSHoldsOnlyGoldensAndProvenance(tb, fsys) })
}

// subtest runs f as a real Go subtest named name when tb supports it, and
// inline against tb otherwise. testing.TB declares no Run method — *testing.T
// and *testing.B each have one, but with incompatible signatures, so the
// interface cannot unify them — which is why Conform cannot simply call
// tb.Run. A caller that hands Conform a genuine *testing.T (directly, or
// through testkit.ValidateProfile) gets real, individually selectable
// subtests; Conform's own tests, which hand it a stub TB to prove a broken
// bundle is caught, get the check run directly against the stub, still
// under a name in every failure message.
func subtest(tb testing.TB, name string, f func(testing.TB)) {
	tb.Helper()

	type runner interface {
		Run(string, func(*testing.T)) bool
	}
	if r, ok := tb.(runner); ok {
		r.Run(name, func(t *testing.T) { f(t) })
		return
	}
	f(tb)
}

// skip calls tb.Skip when tb is a genuine *testing.T or *testing.B (a real
// one, or the one subtest produced via Run), and does nothing otherwise. See
// [testkit.ValidateProfile]'s own copy of this exact function for why the
// switch is on the CONCRETE type rather than a structural "has a Skip
// method" assertion: testing.TB itself declares Skip, so a stub embedding
// testing.TB to stand in for it (TestConformCatchesEachBreak's stubTB, for
// one) would satisfy that assertion too, and calling it would panic on the
// stub's nil embedded interface.
func skip(tb testing.TB, reason string) {
	tb.Helper()
	switch v := tb.(type) {
	case *testing.T:
		v.Skip(reason)
	case *testing.B:
		v.Skip(reason)
	}
}

// checkGoldensAreValidJSON decodes every JSON golden. A golden that is not
// valid JSON fails a profile's own tests with a parse error a long way from
// here, so it is caught at the source instead.
func checkGoldensAreValidJSON(tb testing.TB, fsys fs.FS) {
	tb.Helper()

	for _, golden := range mustGoldens(tb, fsys) {
		decoder := json.NewDecoder(bytes.NewReader(golden.Data))
		decoder.UseNumber()

		var body any
		if err := decoder.Decode(&body); err != nil {
			tb.Errorf("decoding %s: %v", golden.Path, err)
			continue
		}
		if _, ok := body.(map[string]any); !ok {
			tb.Errorf("%s: top level is %T, want a JSON object", golden.Path, body)
		}
		if decoder.More() {
			tb.Errorf("%s: trailing content after the JSON body", golden.Path)
		}
	}
}

// checkEveryGoldenHasProvenance is the reason this package exists. A fixture
// with no provenance entry cannot be reviewed: when it changes, nobody can
// say whether the vendor moved or Servicesim did.
//
// JSON goldens are enumerated through [Goldens]; SSE transcript goldens
// (*.sse, docs/design/streaming.md §5.4) are walked separately, directly off
// fsys, because Goldens deliberately enumerates JSON fixtures only — without
// this second pass, an *.sse fixture with no provenance entry would pass
// this check silently, which is exactly the drift it exists to catch.
func checkEveryGoldenHasProvenance(tb testing.TB, fsys fs.FS) {
	tb.Helper()

	records := mustProvenance(tb, fsys)

	for _, golden := range mustGoldens(tb, fsys) {
		if _, ok := records[golden.Name]; !ok {
			tb.Errorf("%s has no entry in %s", golden.Path, ProvenanceFile)
		}
	}
	for _, name := range sseGoldenNames(tb, fsys) {
		if _, ok := records[name]; !ok {
			tb.Errorf("%s has no entry in %s", name, ProvenanceFile)
		}
	}
}

// checkEveryProvenanceEntryHasGolden catches the opposite drift: a record
// left behind by a deleted or renamed fixture, which would make the
// provenance file describe a shape nothing serves.
//
// Existence is checked through [Read] rather than [Goldens]: the latter
// deliberately enumerates JSON fixtures only and would report every SSE
// transcript golden as missing even though it is real. Read has no such
// restriction: it serves any fixture by name, whatever its extension.
func checkEveryProvenanceEntryHasGolden(tb testing.TB, fsys fs.FS) {
	tb.Helper()

	for name := range mustProvenance(tb, fsys) {
		if _, err := Read(fsys, name); err != nil {
			tb.Errorf("%s names %s, which does not exist: %v", ProvenanceFile, name, err)
		}
	}
}

// checkProvenanceRecordsAreComplete checks the fields a reviewer actually
// uses: where the shape was read, when, and whether the vendor or Servicesim
// chose it. A record missing any of those is not a provenance record.
//
// verified is checked only for being a real YYYY-MM-DD date, never for
// equalling anything else: the whole point of the per-entry date model is
// that one golden's verification date is its own, independent of every
// other entry in the file. checkProviderVerifiedIsAtLeastEveryEntry checks
// the cross-entry constraint that actually applies.
func checkProvenanceRecordsAreComplete(tb testing.TB, fsys fs.FS) {
	tb.Helper()

	for name, record := range mustProvenance(tb, fsys) {
		if record.Endpoint == "" {
			tb.Errorf("%s: no endpoint", name)
		}
		if record.Status < 100 || record.Status > 599 {
			tb.Errorf("%s: status %d is not an HTTP status", name, record.Status)
		}
		switch record.Kind {
		case VendorDocumented, SimulatorChosen:
		default:
			tb.Errorf("%s: kind %q is neither %q nor %q", name, record.Kind, VendorDocumented, SimulatorChosen)
		}
		if !strings.HasPrefix(record.DocumentationURL, "https://") {
			tb.Errorf("%s: documentation_url %q is not an https URL", name, record.DocumentationURL)
		}
		if _, err := time.Parse(time.DateOnly, record.Verified); err != nil {
			tb.Errorf("%s: verified %q is not a YYYY-MM-DD date", name, record.Verified)
		}
		if record.Note == "" {
			tb.Errorf("%s: no note saying what is load-bearing about this fixture", name)
		}
	}
}

// checkCoverage asserts the coverage a profile's own tests depend on: at
// least one success, one empty-result and two error goldens. A bundle
// holding only success fixtures leaves every error path in the handler
// ungoldened.
func checkCoverage(tb testing.TB, fsys fs.FS) {
	tb.Helper()

	var happy, empty, errs int
	for name, record := range mustProvenance(tb, fsys) {
		switch {
		case record.Status >= 400:
			errs++
		case strings.Contains(name, "empty"):
			empty++
		default:
			happy++
		}
	}

	if happy == 0 {
		tb.Error("no success golden: at least one 2xx golden whose file name does NOT contain " +
			"\"empty\" is required")
	}
	if empty == 0 {
		tb.Error("no empty-result golden: at least one 2xx golden whose file name contains " +
			"\"empty\" is required (for example <vendor>-<route>-empty.json) — a 2xx record is classed " +
			"as \"empty\" or \"success\" by its FILE NAME, not by inspecting its body")
	}
	if errs < 2 {
		tb.Errorf("%d error goldens, want at least 2 (any provenance record with status >= 400 counts, "+
			"regardless of file name)", errs)
	}
}

// hexSHA256 matches a lowercase-hex SHA-256 digest.
var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// checkSpecBlockIsWellFormed checks every field [ProviderSpec] reports when
// a bundle carries a spec: block at all, and — the one cross-field
// constraint a spec: block makes — that spec.retrieved is no EARLIER than
// the bundle-level verified date. It does not require a bundle to carry a
// spec: block at all: an out-of-tree profile whose vendor publishes no
// machine-readable specification has nothing to record here, unlike the
// four reference profiles, each of which does (a bundle-specific pin, left
// to the profile's own conformance test the way every other vendor-specific
// fact is).
func checkSpecBlockIsWellFormed(tb testing.TB, fsys fs.FS) {
	tb.Helper()

	spec, ok, err := ProviderSpec(fsys)
	if err != nil {
		tb.Errorf("ProviderSpec: %v", err)
		return
	}
	if !ok {
		skip(tb, "no spec: block recorded in provenance.yaml; this is not a failure — an out-of-tree "+
			"vendor may publish no machine-readable specification — but if one exists, record it "+
			"(url, version, sha256, retrieved) so a dated re-verification can hash-check it mechanically "+
			"before re-reading it by hand (contracts/README.md \"Keeping them honest\")")
		return
	}

	if !strings.HasPrefix(spec.URL, "https://") {
		tb.Errorf("spec.url %q is not an https URL", spec.URL)
	}
	if spec.Version == "" {
		tb.Error("spec.version is empty")
	}
	if !hexSHA256.MatchString(spec.SHA256) {
		tb.Errorf("spec.sha256 %q is not 64 lowercase hex characters", spec.SHA256)
	}
	retrieved, err := time.Parse(time.DateOnly, spec.Retrieved)
	if err != nil {
		tb.Errorf("spec.retrieved %q is not a YYYY-MM-DD date", spec.Retrieved)
		return
	}

	parsed, err := loadProvenanceFile(fsys)
	if err != nil {
		tb.Errorf("%v", err)
		return
	}
	verified, err := time.Parse(time.DateOnly, parsed.Verified)
	if err != nil {
		tb.Errorf("provider-level verified %q is not a YYYY-MM-DD date", parsed.Verified)
		return
	}
	if retrieved.Before(verified) {
		tb.Errorf("spec.retrieved %q is older than the provider-level verified %q — "+
			"a refresh must re-fetch and re-hash the spec on or after the day it re-dates verified",
			spec.Retrieved, parsed.Verified)
	}
}

// checkProviderVerifiedIsAtLeastEveryEntry checks the bundle-level
// `verified:` at the top of provenance.yaml: it must be no older than any
// entry it covers, because it claims to be the most recent whole-contract
// verification and a fixture individually re-checked after it would
// contradict that claim.
func checkProviderVerifiedIsAtLeastEveryEntry(tb testing.TB, fsys fs.FS) {
	tb.Helper()

	parsed, err := loadProvenanceFile(fsys)
	if err != nil {
		tb.Errorf("%v", err)
		return
	}
	if _, err := time.Parse(time.DateOnly, parsed.Verified); err != nil {
		tb.Errorf("provider-level verified %q is not a YYYY-MM-DD date", parsed.Verified)
		return
	}

	for _, record := range parsed.Goldens {
		if record.Verified > parsed.Verified {
			tb.Errorf("%s: verified %q is newer than the provider-level verified %q",
				record.Golden, record.Verified, parsed.Verified)
		}
	}
}

// checkAPIVersionPresentIffFromSpec pins the pair the schema makes
// enforceable: api_version, when present, must be non-empty (whitespace-only
// is not a version nobody could act on), and an entry whose
// documentation_url IS the bundle's spec.url was read from a versioned
// document, so it must carry one.
//
// It does NOT require spec.url to be cited by at least one entry — a
// bundle's spec: block is recorded even when every entry's
// documentation_url is a prose page: the spec's sha256 is still a valid
// drift signal for the whole bundle even where no single golden was read
// from it directly. See contracts/README.md "Keeping them honest".
func checkAPIVersionPresentIffFromSpec(tb testing.TB, fsys fs.FS) {
	tb.Helper()

	records := mustProvenance(tb, fsys)
	for name, record := range records {
		if record.APIVersion != "" && strings.TrimSpace(record.APIVersion) == "" {
			tb.Errorf("%s: api_version is whitespace-only, want a real version or an absent key", name)
		}
	}

	spec, ok, err := ProviderSpec(fsys)
	if err != nil {
		tb.Errorf("ProviderSpec: %v", err)
		return
	}
	if !ok {
		return
	}
	for name, record := range records {
		if record.DocumentationURL != spec.URL {
			continue
		}
		if strings.TrimSpace(record.APIVersion) == "" {
			tb.Errorf("%s: read from spec %s but carries no api_version", name, spec.URL)
		}
	}
}

// checkReadRejectsNonBareNames checks that [Read] refuses to serve a path
// that would escape the bundle, rather than silently resolving it.
//
// Against a caller's REAL bundle this is necessarily a smoke check, not an
// exhaustive one: fs.FS's own path validation already rejects "../…" before
// Read's own guard ever runs, and "sub/…" only proves the guard when the
// bundle actually contains a file at that nested path, which a flat
// single-directory bundle (every reference profile's, and most out-of-tree
// ones) never does. The exhaustive version of this guard — proving Read's
// own check, not fs.FS's — lives in this package's own TestRead, against a
// synthetic bundle built to contain a nested file for exactly this reason.
func checkReadRejectsNonBareNames(tb testing.TB, fsys fs.FS) {
	tb.Helper()

	if _, err := Read(fsys, "../elsewhere/anything.json"); err == nil {
		tb.Error("Read accepted a path outside the bundle")
	}
	if _, err := Read(fsys, "sub/anything.json"); err == nil {
		tb.Error("Read accepted a name with a directory component")
	}
}

// checkFSHoldsOnlyGoldensAndProvenance keeps stray files out of the bundle:
// anything else here would ship to consumers as contract data. README.md is
// allowed: an in-tree reference profile's `//go:embed contracts` sweeps up
// its whole directory, README included, and an out-of-tree profile's bundle
// is expected to do the same. .sse is the SSE transcript golden extension
// docs/design/streaming.md §5.4 names; [Goldens] does not enumerate them
// (see its doc comment), but they are still legitimate contract data, not
// stray files.
func checkFSHoldsOnlyGoldensAndProvenance(tb testing.TB, fsys fs.FS) {
	tb.Helper()

	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(name, ".json"), strings.HasSuffix(name, ".sse"),
			path.Base(name) == ProvenanceFile, path.Base(name) == "README.md":
		default:
			tb.Errorf("%s is in the bundle but is neither a golden, README.md nor %s", name, ProvenanceFile)
		}
		return nil
	})
	if err != nil {
		tb.Errorf("walking the bundle: %v", err)
	}
}

// embedRootMistakeHint checks for the single most likely way to call Conform
// wrong: passing an embed.FS rooted one directory ABOVE the bundle (a bare
// //go:embed contracts, with fsys the embed.FS itself, instead of
// fs.Sub(embedFS, "contracts")). That mistake produces 10 misleading
// subtest failures — "no JSON goldens in this bundle", "loading provenance:
// open provenance.yaml: file does not exist" repeated ten times — with
// nothing naming the actual problem, because every one of fsys's real files
// is one directory down from where every check here looks.
//
// It reports ok=true only when it can name the fix with confidence:
// provenance.yaml is missing at fsys's root AND present exactly one level
// down, in a directory named "contracts". Any other shape (provenance.yaml
// missing everywhere, or present somewhere Conform cannot guess) falls
// through to the normal subtests, which already report a missing
// provenance.yaml on their own.
func embedRootMistakeHint(fsys fs.FS) (string, bool) {
	if _, err := fs.Stat(fsys, ProvenanceFile); err == nil {
		return "", false // the common case: provenance.yaml is right here
	}
	const subdir = "contracts"
	nested := path.Join(subdir, ProvenanceFile)
	if _, err := fs.Stat(fsys, nested); err != nil {
		return "", false // not this mistake; let the normal subtests report it
	}
	return fmt.Sprintf("%s not found at the bundle root; if you //go:embed a directory, pass "+
		"fs.Sub(embedFS, %q) rather than embedFS itself as Profile.Contracts — found %s one level "+
		"down, at %s", ProvenanceFile, subdir, ProvenanceFile, nested), true
}

// mustGoldens loads every JSON golden, reporting through tb.Errorf (never
// Fatalf: every check here reports through Errorf so one failing bundle
// doesn't abort the rest of Conform's subtests, and so a stub TB standing in
// for the real thing — ValidateProfile's own tests, proving it fails on a
// broken bundle — never needs to implement FailNow). A nil return on error
// or on an empty bundle is safe: every caller only ranges over it.
func mustGoldens(tb testing.TB, fsys fs.FS) []Golden {
	tb.Helper()

	goldens, err := Goldens(fsys)
	if err != nil {
		tb.Errorf("loading goldens: %v", err)
		return nil
	}
	if len(goldens) == 0 {
		tb.Error("no JSON goldens in this bundle")
		return nil
	}
	return goldens
}

// mustProvenance is mustGoldens' counterpart for [Provenance].
func mustProvenance(tb testing.TB, fsys fs.FS) map[string]Record {
	tb.Helper()

	records, err := Provenance(fsys)
	if err != nil {
		tb.Errorf("loading provenance: %v", err)
		return nil
	}
	if len(records) == 0 {
		tb.Error("no provenance records in this bundle")
		return nil
	}
	return records
}

// sseGoldenNames returns the bare file names of every *.sse fixture in fsys.
func sseGoldenNames(tb testing.TB, fsys fs.FS) []string {
	tb.Helper()

	names, err := fs.Glob(fsys, "*.sse")
	if err != nil {
		tb.Errorf("listing .sse goldens: %v", err)
		return nil
	}
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = path.Base(name)
	}
	return out
}
