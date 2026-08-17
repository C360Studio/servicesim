package contracts

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"

	"gopkg.in/yaml.v3"
)

//go:embed exa/*.json exa/provenance.yaml
//go:embed tavily/*.json tavily/provenance.yaml
//go:embed perplexity/*.json perplexity/*.sse perplexity/provenance.yaml
//go:embed mcp/*.json mcp/*.sse mcp/provenance.yaml
var files embed.FS

// VerifiedOn is the OLDEST per-entry `verified:` date across every provider's
// provenance.yaml — the age of the single stalest golden fixture in this
// package. It is not a claim that everything was checked on one day: entries
// are dated individually (see [Record.Verified]), because a single golden
// refresh must be able to move one date without forcing every other entry to
// claim a verification that did not happen. VerifiedOn is the number a refresh
// cadence actually needs — "how stale is the oldest thing here" (there is no
// live contract canary; see contracts/README.md "Keeping them honest").
//
// It is declared rather than computed at init so importing this package does
// no work and has no panic path, and TestVerifiedOnIsTheOldestEntry pins it to
// the minimum recomputed from the provenance files: refreshing the last
// fixture still dated this old moves this constant, and the test says so.
const VerifiedOn = "2026-08-14"

// ProvenanceFile is the name of the provenance record in each provider
// directory.
const ProvenanceFile = "provenance.yaml"

// Provider names one simulated vendor. The value is also the directory holding
// that vendor's goldens.
type Provider string

// The simulated vendors, in the order Providers returns them.
const (
	// Exa is the Exa search and answer API.
	Exa Provider = "exa"
	// Tavily is the Tavily search API.
	Tavily Provider = "tavily"
	// Perplexity is the Perplexity Sonar and Agent APIs.
	Perplexity Provider = "perplexity"
	// MCP is the Model Context Protocol Streamable HTTP server.
	MCP Provider = "mcp"
)

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
	// Golden is the fixture file name within the provider directory.
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
	// entry's, and never implies the rest of the provider's contract was
	// re-checked. See the provider-level `verified:` on provenanceFile for that.
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

// Spec records the machine-readable specification a provider's contract was
// generated from. It exists so a refresh can answer "did the spec change?"
// mechanically — fetch URL, compare SHA256 — as the first, cheap step before
// a person re-reads the consumed fields against the vendor's documentation.
//
// Every provider Providers returns carries one: Exa, Tavily and Perplexity
// each publish a machine-readable OpenAPI document covering every route
// Servicesim simulates for that vendor, and each provenance.yaml records its
// URL, version and SHA256. A changed hash is a DRIFT SIGNAL, not a diff of
// what changed: most entries in any given provider are still verified
// against rendered prose pages (their own documentation_url), and the spec's
// bytes can move for reasons a given consumed contract never touches — so a
// changed hash means "re-read the consumed fields against the per-entry
// pages and the spec", never "here is exactly what moved". Only an entry
// whose own documentation_url IS the spec's URL was read from a versioned
// document and carries APIVersion; every other entry was read from an
// undated prose page and carries none.
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
	// provider-level verified date on provenanceFile, because the hash is
	// taken from (or after) the document the contract was verified against
	// — see contracts/README.md "Keeping them honest" and
	// TestSpecRetrievedIsAtLeastProviderVerified for the procedure that
	// keeps the two in that order.
	Retrieved string `yaml:"retrieved"`
}

// Golden is one embedded wire fixture.
type Golden struct {
	// Provider owns the fixture.
	Provider Provider

	// Name is the file name, for example "exa-search-happy.json".
	Name string

	// Path is the name within FS, for example "exa/exa-search-happy.json".
	Path string

	// Data is the fixture's bytes.
	Data []byte
}

// provenanceFile is the on-disk shape of a provider's provenance.yaml.
//
// Verified, at this level, is the most recent date any part of the provider's
// contract was verified against its vendor documentation — not any one
// golden's date, but the claim contracts/README.md's index table makes in its
// "Verified" column for this provider. It must be at least as recent as every
// Goldens[i].Verified in the file: a provider cannot claim a whole-contract
// verification older than a fixture that was individually re-checked since.
type provenanceFile struct {
	Provider string   `yaml:"provider"`
	Verified string   `yaml:"verified"`
	Spec     *Spec    `yaml:"spec,omitempty"`
	Goldens  []Record `yaml:"goldens"`
}

// Providers returns every provider directory, in a stable order. It is a
// function rather than a package-level slice so no caller can reorder or
// truncate the set for everyone else.
func Providers() []Provider {
	return []Provider{Exa, Tavily, Perplexity, MCP}
}

// FS returns a read-only file system over the goldens and the provenance
// records, for a caller that would rather walk the tree than use the typed
// accessors.
func FS() fs.FS {
	return files
}

// Goldens returns every JSON golden fixture for p, sorted by file name.
// Sorting is not cosmetic: a caller that ranges over the result and writes a
// report would otherwise depend on the embed package's ordering.
//
// SSE transcript fixtures (*.sse, docs/design/streaming.md §5.4) are NOT
// enumerated here: every caller of this function — TestGoldensAreValidJSON
// chief among them — assumes a JSON object, which an SSE transcript is not.
// Read an SSE fixture directly by name through [Read] instead.
func Goldens(p Provider) ([]Golden, error) {
	if err := validate(p); err != nil {
		return nil, err
	}

	names, err := fs.Glob(files, path.Join(string(p), "*.json"))
	if err != nil {
		return nil, fmt.Errorf("contracts: listing %s goldens: %w", p, err)
	}
	slices.Sort(names)

	goldens := make([]Golden, 0, len(names))
	for _, name := range names {
		data, err := files.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("contracts: reading %s: %w", name, err)
		}
		goldens = append(goldens, Golden{
			Provider: p,
			Name:     path.Base(name),
			Path:     name,
			Data:     data,
		})
	}
	return goldens, nil
}

// AllGoldens returns every golden fixture, provider by provider in the order
// Providers reports and by file name within each.
func AllGoldens() ([]Golden, error) {
	var all []Golden
	for _, p := range Providers() {
		goldens, err := Goldens(p)
		if err != nil {
			return nil, err
		}
		all = append(all, goldens...)
	}
	return all, nil
}

// Read returns the bytes of one named fixture, where name is the bare file name
// within the provider directory.
func Read(p Provider, name string) ([]byte, error) {
	if err := validate(p); err != nil {
		return nil, err
	}
	if name != path.Base(name) {
		return nil, fmt.Errorf("contracts: %q is not a bare file name", name)
	}

	data, err := files.ReadFile(path.Join(string(p), name))
	if err != nil {
		return nil, fmt.Errorf("contracts: reading %s golden %s: %w", p, name, err)
	}
	return data, nil
}

// Provenance returns p's provenance records keyed by golden file name. It
// reports an error on a duplicate or unnamed entry, because a duplicate silently
// shadows a record and an unnamed one attaches to nothing.
func Provenance(p Provider) (map[string]Record, error) {
	parsed, err := loadProvenanceFile(p)
	if err != nil {
		return nil, err
	}

	name := path.Join(string(p), ProvenanceFile)
	records := make(map[string]Record, len(parsed.Goldens))
	for i, record := range parsed.Goldens {
		if record.Golden == "" {
			return nil, fmt.Errorf("contracts: %s entry %d names no golden", name, i)
		}
		if _, seen := records[record.Golden]; seen {
			return nil, fmt.Errorf("contracts: %s has two entries for %s", name, record.Golden)
		}
		records[record.Golden] = record
	}
	return records, nil
}

// ProviderSpec returns p's [Spec] — the machine-readable specification its
// contract was generated from — and reports whether the provenance file
// records one at all. Every provider Providers returns does today; ok is
// false only for a provider whose provenance.yaml carries no spec: block,
// which contracts_test.go's TestEveryProviderHasSpecRecorded treats as a
// failure. See [Spec]'s doc comment for why every provider carries one.
//
// This is named ProviderSpec rather than Provenance, which already names the
// per-golden accessor above with an incompatible signature (Go has no
// overloading); a consumer wanting a provider's spec provenance reaches for
// this the same way it reaches for [Provenance] to reach a golden's.
func ProviderSpec(p Provider) (Spec, bool, error) {
	parsed, err := loadProvenanceFile(p)
	if err != nil {
		return Spec{}, false, err
	}
	if parsed.Spec == nil {
		return Spec{}, false, nil
	}
	return *parsed.Spec, true, nil
}

// loadProvenanceFile reads and parses p's provenance.yaml. [Provenance] and
// this package's own tests (which read provenanceFile.Verified directly,
// being in package contracts) share it so nothing parses the file twice or
// differently. There is deliberately no exported VerifiedFor: nothing outside
// this package's tests needs the provider-level date on its own, and house
// rule 7 (every exported symbol is a compatibility obligation) says a
// test-only need is not a reason to add one — see
// provenance_internal_test.go.
func loadProvenanceFile(p Provider) (*provenanceFile, error) {
	if err := validate(p); err != nil {
		return nil, err
	}

	name := path.Join(string(p), ProvenanceFile)
	data, err := files.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("contracts: reading %s: %w", name, err)
	}

	var parsed provenanceFile
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("contracts: parsing %s: %w", name, err)
	}
	return &parsed, nil
}

// validate rejects a provider that has no directory here, so a typo fails with
// its own name rather than as an empty result set.
func validate(p Provider) error {
	if slices.Contains(Providers(), p) {
		return nil
	}
	return fmt.Errorf("contracts: unknown provider %q", p)
}
