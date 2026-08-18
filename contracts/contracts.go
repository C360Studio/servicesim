package contracts

import (
	"fmt"
	"io/fs"
	"path"
	"slices"

	"gopkg.in/yaml.v3"
)

// ProvenanceFile is the name of the provenance record inside a contract
// bundle.
const ProvenanceFile = "provenance.yaml"

// Kind records where a golden's shape came from. It is the difference between
// "the vendor publishes this" and "Servicesim had to choose", and it is what
// tells a dated re-verification (contracts/README.md "Keeping them honest")
// which fixtures it may correct from the vendor's documentation or a real
// response captured by a person. There is no live contract canary; drift
// detection is manual and dated, by design (D10, docs/adopter-backlog.md).
type Kind string

const (
	// VendorDocumented means the shape and the values both come from the
	// vendor's own documentation or machine-readable specification.
	VendorDocumented Kind = "vendor-documented"

	// SimulatorChosen means Servicesim picked the shape or the values because
	// the vendor publishes none. Perplexity's non-422 Sonar error bodies and
	// every provider's fail-closed routing body are in this class.
	SimulatorChosen Kind = "simulator-chosen"
)

// Record is one provenance.yaml entry: everything a reviewer needs to decide
// whether a changed golden reflects vendor drift or a local mistake.
type Record struct {
	// Golden is the fixture file name within the bundle.
	Golden string `yaml:"golden"`

	// Endpoint is the route the fixture is a response for, or a description of
	// the routing case for fail-closed bodies.
	Endpoint string `yaml:"endpoint"`

	// Status is the HTTP status the fixture is served with.
	Status int `yaml:"status"`

	// Kind is VendorDocumented or SimulatorChosen.
	Kind Kind `yaml:"kind"`

	// DocumentationURL is the page the shape was read from.
	DocumentationURL string `yaml:"documentation_url"`

	// Verified is the date THIS golden's shape was last checked against
	// DocumentationURL, in YYYY-MM-DD form. It answers only for this one entry —
	// refreshing a single fixture moves only its own date, never any other
	// entry's, and never implies the rest of the bundle's contract was
	// re-checked. See the bundle-level `verified:` on provenanceFile for that.
	Verified string `yaml:"verified"`

	// Note records what is load-bearing or inferred about this fixture.
	Note string `yaml:"note"`

	// APIVersion is the vendor's own version string for the document THIS
	// entry's DocumentationURL was read from, when that document is
	// versioned — for example an OpenAPI document's info.version. It is
	// empty for an entry read from an undated prose page: a page has no
	// version to record, only the day someone read it (Verified).
	APIVersion string `yaml:"api_version,omitempty"`
}

// Spec records the machine-readable specification a bundle's contract was
// generated from. It exists so a refresh can answer "did the spec change?"
// mechanically — fetch URL, compare SHA256 — as the first, cheap step before
// a person re-reads the consumed fields against the vendor's documentation.
//
// A changed hash is a DRIFT SIGNAL, not a diff of what changed: most entries
// in any given bundle are still verified against rendered prose pages (their
// own documentation_url), and the spec's bytes can move for reasons a given
// consumed contract never touches — so a changed hash means "re-read the
// consumed fields against the per-entry pages and the spec", never "here is
// exactly what moved". Only an entry whose own documentation_url IS the
// spec's URL was read from a versioned document and carries APIVersion;
// every other entry was read from an undated prose page and carries none.
// contracts/README.md "Keeping them honest" explains the procedure in full.
type Spec struct {
	// URL is the machine-readable specification the contract was generated
	// from.
	URL string `yaml:"url"`

	// Version is the vendor's own version string carried by the
	// specification — for an OpenAPI document, its info.version.
	Version string `yaml:"version"`

	// SHA256 is the hex-encoded SHA-256 of the specification's bytes exactly
	// as fetched, lowercase.
	SHA256 string `yaml:"sha256"`

	// Retrieved is the date the bytes behind SHA256 were fetched, in
	// YYYY-MM-DD form. It names when THIS hash was taken, not when the
	// contract's fields were last verified: it is never earlier than the
	// bundle-level verified date on provenanceFile, because the hash is
	// taken from (or after) the document the contract was verified against
	// — see contracts/README.md "Keeping them honest".
	Retrieved string `yaml:"retrieved"`
}

// Golden is one wire fixture read out of a bundle's fs.FS.
type Golden struct {
	// Name is the file name, for example "exa-search-happy.json".
	Name string

	// Path is the name within the fs.FS the bundle was read from — the same
	// as Name for a bundle with no subdirectories, which is every bundle
	// today.
	Path string

	// Data is the fixture's bytes.
	Data []byte
}

// provenanceFile is the on-disk shape of a bundle's provenance.yaml.
//
// Verified, at this level, is the most recent date any part of the bundle's
// contract was verified against its vendor documentation — not any one
// golden's date, but the claim contracts/README.md's index table makes in its
// "Verified" column for this provider. It must be at least as recent as every
// Goldens[i].Verified in the file: a bundle cannot claim a whole-contract
// verification older than a fixture that was individually re-checked since.
type provenanceFile struct {
	Provider string   `yaml:"provider"`
	Verified string   `yaml:"verified"`
	Spec     *Spec    `yaml:"spec,omitempty"`
	Goldens  []Record `yaml:"goldens"`
}

// Read returns the bytes of one named fixture out of fsys, where name is the
// bare file name within the bundle — never a path, so a caller cannot escape
// the bundle it was handed.
func Read(fsys fs.FS, name string) ([]byte, error) {
	if name != path.Base(name) {
		return nil, fmt.Errorf("contracts: %q is not a bare file name", name)
	}

	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("contracts: reading golden %s: %w", name, err)
	}
	return data, nil
}

// Goldens returns every JSON golden fixture in fsys, sorted by file name.
// Sorting is not cosmetic: a caller that ranges over the result and writes a
// report would otherwise depend on the underlying fs.FS's ordering, which an
// embed.FS does not guarantee.
//
// SSE transcript fixtures (*.sse, docs/design/streaming.md §5.4) are NOT
// enumerated here: every caller of this function assumes a JSON object,
// which an SSE transcript is not. Read an SSE fixture directly by name
// through [Read] instead.
func Goldens(fsys fs.FS) ([]Golden, error) {
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, fmt.Errorf("contracts: listing goldens: %w", err)
	}
	slices.Sort(names)

	goldens := make([]Golden, 0, len(names))
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("contracts: reading %s: %w", name, err)
		}
		goldens = append(goldens, Golden{
			Name: path.Base(name),
			Path: name,
			Data: data,
		})
	}
	return goldens, nil
}

// Provenance returns fsys's provenance records keyed by golden file name. It
// reports an error on a duplicate or unnamed entry, because a duplicate
// silently shadows a record and an unnamed one attaches to nothing.
func Provenance(fsys fs.FS) (map[string]Record, error) {
	parsed, err := loadProvenanceFile(fsys)
	if err != nil {
		return nil, err
	}

	records := make(map[string]Record, len(parsed.Goldens))
	for i, record := range parsed.Goldens {
		if record.Golden == "" {
			return nil, fmt.Errorf("contracts: %s entry %d names no golden", ProvenanceFile, i)
		}
		if _, seen := records[record.Golden]; seen {
			return nil, fmt.Errorf("contracts: %s has two entries for %s", ProvenanceFile, record.Golden)
		}
		records[record.Golden] = record
	}
	return records, nil
}

// ProviderSpec returns fsys's [Spec] — the machine-readable specification its
// contract was generated from — and reports whether the provenance file
// records one at all. It is named ProviderSpec rather than Provenance, which
// already names the per-golden accessor above with an incompatible signature
// (Go has no overloading); a consumer wanting a bundle's spec provenance
// reaches for this the same way it reaches for [Provenance] to reach a
// golden's.
func ProviderSpec(fsys fs.FS) (Spec, bool, error) {
	parsed, err := loadProvenanceFile(fsys)
	if err != nil {
		return Spec{}, false, err
	}
	if parsed.Spec == nil {
		return Spec{}, false, nil
	}
	return *parsed.Spec, true, nil
}

// OldestVerified returns the oldest per-entry `verified:` date across fsys's
// provenance.yaml — the age of the single stalest golden fixture in THIS
// bundle. It replaces the package's former global VerifiedOn constant: a
// framework package that is handed an arbitrary fsys cannot declare a single
// constant answering "how stale is the oldest thing" for every bundle a
// caller might ever pass it, only compute the answer for the one it was
// given. A caller wanting the number across several bundles (this
// repository's own four reference profiles, for instance) calls
// OldestVerified once per bundle and takes the minimum itself — that
// aggregation is repository-specific, not something this package can know.
func OldestVerified(fsys fs.FS) (string, error) {
	parsed, err := loadProvenanceFile(fsys)
	if err != nil {
		return "", err
	}
	if len(parsed.Goldens) == 0 {
		return "", fmt.Errorf("contracts: %s names no goldens", ProvenanceFile)
	}

	oldest := ""
	for _, record := range parsed.Goldens {
		if oldest == "" || record.Verified < oldest {
			oldest = record.Verified
		}
	}
	return oldest, nil
}

// loadProvenanceFile reads and parses fsys's provenance.yaml. [Provenance],
// [ProviderSpec], [OldestVerified] and this package's own tests (which read
// provenanceFile.Verified directly, being in package contracts) share it so
// nothing parses the file twice or differently. There is deliberately no
// exported VerifiedFor: nothing outside this package's tests needs the
// bundle-level date on its own, and house rule 7 (every exported symbol is a
// compatibility obligation) says a test-only need is not a reason to add
// one — see contracts_test.go and conform.go's checkProviderVerifiedIsAtLeastEveryEntry.
func loadProvenanceFile(fsys fs.FS) (*provenanceFile, error) {
	data, err := fs.ReadFile(fsys, ProvenanceFile)
	if err != nil {
		return nil, fmt.Errorf("contracts: reading %s: %w", ProvenanceFile, err)
	}

	var parsed provenanceFile
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("contracts: parsing %s: %w", ProvenanceFile, err)
	}
	return &parsed, nil
}
