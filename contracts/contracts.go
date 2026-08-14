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
//go:embed perplexity/*.json perplexity/provenance.yaml
var files embed.FS

// VerifiedOn is the date every contract in this package was last checked
// against the vendors' live documentation. It is the date carried by every
// provenance entry, and the one a live contract canary compares against when it
// decides whether a re-verification is overdue.
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
)

// Kind records where a golden's shape came from. It is the difference between
// "the vendor publishes this" and "Servicesim had to choose", and it is what
// tells a live canary which fixtures it is allowed to correct from a real
// response.
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

	// Verified is the date the shape was checked, in YYYY-MM-DD form.
	Verified string `yaml:"verified"`

	// Note records what is load-bearing or inferred about this fixture.
	Note string `yaml:"note"`
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
type provenanceFile struct {
	Provider string   `yaml:"provider"`
	Verified string   `yaml:"verified"`
	Goldens  []Record `yaml:"goldens"`
}

// Providers returns every provider directory, in a stable order. It is a
// function rather than a package-level slice so no caller can reorder or
// truncate the set for everyone else.
func Providers() []Provider {
	return []Provider{Exa, Tavily, Perplexity}
}

// FS returns a read-only file system over the goldens and the provenance
// records, for a caller that would rather walk the tree than use the typed
// accessors.
func FS() fs.FS {
	return files
}

// Goldens returns every golden fixture for p, sorted by file name. Sorting is
// not cosmetic: a caller that ranges over the result and writes a report would
// otherwise depend on the embed package's ordering.
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

// validate rejects a provider that has no directory here, so a typo fails with
// its own name rather than as an empty result set.
func validate(p Provider) error {
	if slices.Contains(Providers(), p) {
		return nil
	}
	return fmt.Errorf("contracts: unknown provider %q", p)
}
