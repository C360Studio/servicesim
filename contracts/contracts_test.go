package contracts_test

import (
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/c360studio/servicesim/contracts"
)

// goodBundle returns a fresh, well-formed synthetic contract bundle: one
// success golden, one empty-result golden, two error goldens and one SSE
// transcript golden, with a provenance.yaml (carrying a spec: block) that
// fully covers all five. It exists so this package's own tests exercise
// Read, Goldens, Provenance, ProviderSpec, OldestVerified and Conform
// without depending on any real vendor's fixtures — those live beside their
// own profile now (profiles/<name>/contracts), not here, because contracts
// no longer knows which providers exist.
//
// Returned fresh every call (fstest.MapFS is a map) so a test that mutates
// its copy — every "broken bundle" test below does — never leaks into
// another.
func goodBundle() fstest.MapFS {
	return fstest.MapFS{
		"happy.json":      &fstest.MapFile{Data: []byte(`{"result":"ok"}`)},
		"empty.json":      &fstest.MapFile{Data: []byte(`{"result":[]}`)},
		"error-400.json":  &fstest.MapFile{Data: []byte(`{"error":"bad request"}`)},
		"error-429.json":  &fstest.MapFile{Data: []byte(`{"error":"rate limited"}`)},
		"stream.sse":      &fstest.MapFile{Data: []byte("data: {\"delta\":\"hi\"}\n\n")},
		"README.md":       &fstest.MapFile{Data: []byte("# Acme contract\n")},
		"provenance.yaml": &fstest.MapFile{Data: []byte(goodProvenanceYAML)},
	}
}

var goodProvenanceYAML = `
provider: acme
verified: "2026-08-10"
spec:
  url: https://api.example.test/openapi.json
  version: "1.0.0"
  sha256: "` + strings.Repeat("a", 64) + `"
  retrieved: "2026-08-10"
goldens:
  - golden: happy.json
    endpoint: POST /v1/answer
    status: 200
    kind: vendor-documented
    documentation_url: https://api.example.test/openapi.json
    verified: "2026-08-01"
    note: happy path
    api_version: "1.0.0"
  - golden: empty.json
    endpoint: POST /v1/answer
    status: 200
    kind: vendor-documented
    documentation_url: https://docs.example.test/answer
    verified: "2026-08-02"
    note: empty result set
  - golden: error-400.json
    endpoint: POST /v1/answer
    status: 400
    kind: simulator-chosen
    documentation_url: https://docs.example.test/errors
    verified: "2026-08-03"
    note: bad request shape
  - golden: error-429.json
    endpoint: POST /v1/answer
    status: 429
    kind: vendor-documented
    documentation_url: https://docs.example.test/errors
    verified: "2026-08-04"
    note: rate limited shape
  - golden: stream.sse
    endpoint: POST /v1/answer (stream)
    status: 200
    kind: vendor-documented
    documentation_url: https://docs.example.test/stream
    verified: "2026-08-05"
    note: one streamed delta
`

// cloneBundle returns a shallow copy of b, so a test can add, remove or
// replace an entry without mutating a bundle another test still holds.
func cloneBundle(b fstest.MapFS) fstest.MapFS {
	return maps.Clone(b)
}

// TestRead pins Read's contract: it serves a bare name's bytes verbatim, and
// rejects anything that is not a bare name or does not exist.
func TestRead(t *testing.T) {
	t.Parallel()

	bundle := goodBundle()

	data, err := contracts.Read(bundle, "happy.json")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(data) != `{"result":"ok"}` {
		t.Errorf("Read returned %q, want the golden's literal bytes", data)
	}

	for _, name := range []string{"../elsewhere/happy.json", "sub/happy.json"} {
		if _, err := contracts.Read(bundle, name); err == nil {
			t.Errorf("Read(%q) succeeded, want an error for a non-bare name", name)
		}
	}

	if _, err := contracts.Read(bundle, "nonexistent.json"); err == nil {
		t.Error("Read of a name absent from the bundle succeeded")
	}
}

// TestReadRejectsANestedNameEvenWhenItExists strengthens TestRead's
// "sub/happy.json" case above: goodBundle has no file at that path, so
// fstest.MapFS.Open itself refuses it (fs.ValidPath's own rule) before
// Read's OWN bare-name guard is ever reached — a mutant that deletes
// Read's `if name != path.Base(name)` check entirely still passes that
// assertion, because the underlying fs.FS was already going to error for
// an unrelated reason. This test builds a bundle that DOES have a file at
// a nested path, so Read("sub/nested.json") can only fail if Read's own
// guard is the thing doing the rejecting — proving the guard, not fs.FS's.
func TestReadRejectsANestedNameEvenWhenItExists(t *testing.T) {
	t.Parallel()

	bundle := cloneBundle(goodBundle())
	bundle["sub/nested.json"] = &fstest.MapFile{Data: []byte(`{"result":"reachable"}`)}

	if _, err := contracts.Read(bundle, "sub/nested.json"); err == nil {
		t.Error("Read(\"sub/nested.json\") succeeded for a name with a directory component that " +
			"genuinely exists in the bundle — Read's own bare-name guard did not fire")
	} else if !strings.Contains(err.Error(), "not a bare file name") {
		t.Errorf("Read error = %q, want it to name the bare-file-name requirement", err)
	}
}

// TestGoldens pins sorting and the JSON-only filter: Goldens must not depend
// on the underlying fs.FS's own enumeration order, and must not enumerate
// the SSE transcript golden.
func TestGoldens(t *testing.T) {
	t.Parallel()

	goldens, err := contracts.Goldens(goodBundle())
	if err != nil {
		t.Fatalf("Goldens: %v", err)
	}

	var names []string
	for _, g := range goldens {
		names = append(names, g.Name)
	}
	want := []string{"empty.json", "error-400.json", "error-429.json", "happy.json"}
	if len(names) != len(want) {
		t.Fatalf("Goldens returned %v, want %v", names, want)
	}
	for i, name := range names {
		if name != want[i] {
			t.Errorf("Goldens()[%d].Name = %q, want %q (sorted order)", i, name, want[i])
		}
	}
}

// TestProvenance pins Provenance's error cases: an entry with no golden name
// and two entries naming the same golden must both fail closed, because
// either would let a fixture change with no reviewable record.
func TestProvenance(t *testing.T) {
	t.Parallel()

	records, err := contracts.Provenance(goodBundle())
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	if len(records) != 5 {
		t.Fatalf("Provenance returned %d records, want 5", len(records))
	}
	if records["happy.json"].Endpoint != "POST /v1/answer" {
		t.Errorf("happy.json endpoint = %q, want %q", records["happy.json"].Endpoint, "POST /v1/answer")
	}

	unnamed := cloneBundle(goodBundle())
	unnamed["provenance.yaml"] = &fstest.MapFile{Data: []byte(`
provider: acme
verified: "2026-08-10"
goldens:
  - endpoint: POST /v1/answer
    status: 200
    kind: vendor-documented
    documentation_url: https://docs.example.test/answer
    verified: "2026-08-01"
    note: no golden name
`)}
	if _, err := contracts.Provenance(unnamed); err == nil {
		t.Error("Provenance accepted an entry naming no golden")
	}

	dup := cloneBundle(goodBundle())
	dup["provenance.yaml"] = &fstest.MapFile{Data: []byte(`
provider: acme
verified: "2026-08-10"
goldens:
  - golden: happy.json
    endpoint: POST /v1/answer
    status: 200
    kind: vendor-documented
    documentation_url: https://docs.example.test/answer
    verified: "2026-08-01"
    note: first
  - golden: happy.json
    endpoint: POST /v1/answer
    status: 200
    kind: vendor-documented
    documentation_url: https://docs.example.test/answer
    verified: "2026-08-01"
    note: duplicate
`)}
	if _, err := contracts.Provenance(dup); err == nil {
		t.Error("Provenance accepted two entries for the same golden")
	}
}

// TestProviderSpec pins the ok=false case: a bundle with no spec: block at
// all is not an error, only the absence of one.
func TestProviderSpec(t *testing.T) {
	t.Parallel()

	spec, ok, err := contracts.ProviderSpec(goodBundle())
	if err != nil {
		t.Fatalf("ProviderSpec: %v", err)
	}
	if !ok {
		t.Fatal("ProviderSpec reported ok=false for a bundle with a spec: block")
	}
	if spec.Version != "1.0.0" {
		t.Errorf("spec.Version = %q, want %q", spec.Version, "1.0.0")
	}

	noSpec := cloneBundle(goodBundle())
	noSpec["provenance.yaml"] = &fstest.MapFile{Data: []byte(`
provider: acme
verified: "2026-08-10"
goldens:
  - golden: happy.json
    endpoint: POST /v1/answer
    status: 200
    kind: vendor-documented
    documentation_url: https://docs.example.test/answer
    verified: "2026-08-01"
    note: happy path
`)}
	_, ok, err = contracts.ProviderSpec(noSpec)
	if err != nil {
		t.Fatalf("ProviderSpec: %v", err)
	}
	if ok {
		t.Error("ProviderSpec reported ok=true for a bundle with no spec: block")
	}
}

// TestOldestVerified pins the replacement for the deleted package-level
// VerifiedOn: the oldest per-entry date in THIS bundle, computed rather than
// declared, so it cannot drift from the entries it summarises.
func TestOldestVerified(t *testing.T) {
	t.Parallel()

	oldest, err := contracts.OldestVerified(goodBundle())
	if err != nil {
		t.Fatalf("OldestVerified: %v", err)
	}
	if oldest != "2026-08-01" {
		t.Errorf("OldestVerified = %q, want %q (happy.json's date, the earliest entry)", oldest, "2026-08-01")
	}

	empty := cloneBundle(goodBundle())
	empty["provenance.yaml"] = &fstest.MapFile{Data: []byte("provider: acme\nverified: \"2026-08-10\"\ngoldens: []\n")}
	if _, err := contracts.OldestVerified(empty); err == nil {
		t.Error("OldestVerified accepted a provenance file naming no goldens")
	}
}

// TestConformPassesOnAWellFormedBundle runs the real, unwrapped Conform
// against goodBundle through a genuine *testing.T, so every generic guard
// actually produces the individually selectable subtest its name promises
// (go test -run TestConformPassesOnAWellFormedBundle/GoldensAreValidJSON).
func TestConformPassesOnAWellFormedBundle(t *testing.T) {
	t.Parallel()
	contracts.Conform(t, goodBundle())
}

// TestConformCatchesEachBreak proves each generic guard actually guards
// something, by breaking goodBundle one way at a time and checking Conform's
// recorded failure names what broke. Run through stubTB rather than a real
// *testing.T (as [contracts.Conform]'s own doc comment says a caller may):
// testing.TB has no Run method, so Conform falls back to running each check
// inline against the stub, and every check reports through Errorf rather
// than Fatalf for exactly this reason — see stubTB's doc comment.
func TestConformCatchesEachBreak(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mutate  func(fstest.MapFS)
		wantErr string
	}{
		{
			name: "a golden with no provenance entry",
			mutate: func(b fstest.MapFS) {
				b["orphan.json"] = &fstest.MapFile{Data: []byte(`{"ok":true}`)}
			},
			wantErr: "orphan.json has no entry",
		},
		{
			name: "a provenance entry naming a golden that does not exist",
			mutate: func(b fstest.MapFS) {
				b["provenance.yaml"] = &fstest.MapFile{Data: []byte(goodProvenanceYAML + `
  - golden: missing.json
    endpoint: POST /v1/answer
    status: 200
    kind: vendor-documented
    documentation_url: https://docs.example.test/answer
    verified: "2026-08-01"
    note: names a golden that is not here
`)}
			},
			wantErr: "missing.json, which does not exist",
		},
		{
			name: "an incomplete record",
			mutate: func(b fstest.MapFS) {
				b["provenance.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(
					goodProvenanceYAML, "note: happy path", "note:", 1))}
			},
			wantErr: "no note",
		},
		{
			name: "fewer than two error goldens",
			mutate: func(b fstest.MapFS) {
				delete(b, "error-429.json")
				b["provenance.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(
					goodProvenanceYAML,
					"  - golden: error-429.json\n    endpoint: POST /v1/answer\n    status: 429\n"+
						"    kind: vendor-documented\n    documentation_url: https://docs.example.test/errors\n"+
						"    verified: \"2026-08-04\"\n    note: rate limited shape\n",
					"", 1))}
			},
			wantErr: "want at least 2",
		},
		{
			name: "a malformed spec sha256",
			mutate: func(b fstest.MapFS) {
				b["provenance.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(
					goodProvenanceYAML, strings.Repeat("a", 64), "not-a-hash", 1))}
			},
			wantErr: "not 64 lowercase hex",
		},
		{
			// Boundary case for hexSHA256's own regexp: one character short
			// of 64 must still be rejected, not accepted by a `{63,64}`-style
			// off-by-one in the length quantifier.
			name: "a spec sha256 one character short of 64",
			mutate: func(b fstest.MapFS) {
				b["provenance.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(
					goodProvenanceYAML, strings.Repeat("a", 64), strings.Repeat("a", 63), 1))}
			},
			wantErr: "not 64 lowercase hex",
		},
		{
			// The other boundary: one character too many.
			name: "a spec sha256 one character longer than 64",
			mutate: func(b fstest.MapFS) {
				b["provenance.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(
					goodProvenanceYAML, strings.Repeat("a", 64), strings.Repeat("a", 65), 1))}
			},
			wantErr: "not 64 lowercase hex",
		},
		{
			// hexSHA256 must require LOWERCASE hex — a vendor's spec bytes
			// hash the same regardless of how the digest is later cased, so
			// an uppercase digest is a formatting slip this bundle should
			// still catch, not silently accept.
			name: "an uppercase spec sha256",
			mutate: func(b fstest.MapFS) {
				b["provenance.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(
					goodProvenanceYAML, strings.Repeat("a", 64), strings.Repeat("A", 64), 1))}
			},
			wantErr: "not 64 lowercase hex",
		},
		{
			name: "an entry read from the spec URL with no api_version",
			mutate: func(b fstest.MapFS) {
				b["provenance.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(
					goodProvenanceYAML, "    api_version: \"1.0.0\"\n", "", 1))}
			},
			wantErr: "carries no api_version",
		},
		{
			name: "a stray file in the bundle",
			mutate: func(b fstest.MapFS) {
				b["notes.txt"] = &fstest.MapFile{Data: []byte("scratch")}
			},
			wantErr: "neither a golden, README.md nor provenance.yaml",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bundle := cloneBundle(goodBundle())
			tc.mutate(bundle)

			stub := &stubTB{}
			contracts.Conform(stub, bundle)

			if !stub.Failed() {
				t.Fatalf("Conform did not fail on: %s", tc.name)
			}
			if !strings.Contains(stub.Message(), tc.wantErr) {
				t.Errorf("Conform's recorded failures = %q, want a message containing %q", stub.Message(), tc.wantErr)
			}
		})
	}
}

// stubTB captures what Conform reports instead of failing the real test, so
// TestConformCatchesEachBreak can assert Conform fails when it should.
// Mirrors testkit's own stubTB (testkit/assertions_test.go): every check in
// this package reports through Errorf, never Fatalf, which is what lets a
// plain embedded testing.TB stand in — the embedded interface is nil, so a
// method this stub does not override would panic rather than silently pass.
type stubTB struct {
	testing.TB

	mu       sync.Mutex
	messages []string
}

func (s *stubTB) Helper()        {}
func (s *stubTB) Cleanup(func()) {}

func (s *stubTB) Errorf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, fmt.Sprintf(format, args...))
}

func (s *stubTB) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages) > 0
}

func (s *stubTB) Message() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.messages, "\n")
}
