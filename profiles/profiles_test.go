package profiles_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/c360studio/servicesim/profiles"
	"github.com/c360studio/servicesim/provider"
)

// TestReference_NamesAndOrder pins the two-registration-lists claim
// (docs/proposals/framework-seam.md, "Add a two-line test that they are
// equal"): cmd/servicesim/main.go now calls profiles.Reference() directly,
// so the "two lists match" property collapses to Reference() itself
// returning exactly the four names, in the order main registers them.
func TestReference_NamesAndOrder(t *testing.T) {
	t.Parallel()

	ps := profiles.Reference()

	got := make([]provider.Name, 0, len(ps))
	for _, p := range ps {
		got = append(got, p.Name)
	}

	assert.Equal(t, []provider.Name{"exa", "tavily", "perplexity", "mcp"}, got)
}

// TestReference_NewSetSucceeds proves the four reference profiles compose
// into a valid registry: nothing about the move to profiles/<name> or the
// exported-surface trim broke NewSet's own validation of them.
func TestReference_NewSetSucceeds(t *testing.T) {
	t.Parallel()

	set, err := provider.NewSet(profiles.Reference()...)
	require.NoError(t, err)
	require.NotNil(t, set)
}

// TestProviderVerifiedMatchesReadmeIndexTable keeps each reference profile's
// contract bundle (profiles/<name>/contracts/provenance.yaml) and
// contracts/README.md's index-table "Verified" column mechanically in
// agreement. That column is exactly the field that used to lie under the
// single-global-date model this repository shipped before provenance went
// per-entry — the Exa and Tavily READMEs were re-verified 2026-08-15 while
// every provenance entry was still compelled to claim 2026-08-14 — and this
// test is what keeps it from lying again.
//
// It lived inside package contracts before Phase 10 unit 6 (as
// TestProviderVerifiedMatchesReadmeIndexTable, contracts/provenance_internal_test.go,
// with access to the unexported provenanceFile.Verified field). Genericising
// contracts over fs.FS moved every reference bundle out from under it
// (profiles/<name>/contracts) and made contracts stop knowing which
// providers exist at all, so a cross-bundle, cross-document check that
// needs to name all four bundles cannot live there: house rule 7's
// no-privilege rule places contracts strictly below profiles, so contracts
// may not import profiles to ask it. profiles both knows all four
// (profiles.Reference()) and sits above contracts, so it is the only
// package that can run this check — and it does so without a new exported
// contracts symbol: the provider-level `verified:` field stays unexported
// inside package contracts (a test-only need is not a reason to add one),
// so this test parses just that one line out of each bundle's
// provenance.yaml directly, the way an external reader would.
func TestProviderVerifiedMatchesReadmeIndexTable(t *testing.T) {
	t.Parallel()

	index := readmeVerifiedDates(t)
	for _, p := range profiles.Reference() {
		want, ok := index[p.Title]
		if !ok {
			t.Fatalf("contracts/README.md's index table has no dated row for %q", p.Title)
		}

		got := bundleVerified(t, string(p.Name))
		if got != want {
			t.Errorf("%s: %s/contracts/provenance.yaml's provider-level verified is %q, "+
				"contracts/README.md's index table says %q", p.Title, p.Name, got, want)
		}
	}
}

// readmeVerifiedDates parses the "Verified" column of contracts/README.md's
// index table, keyed by the table's first-column provider title (matching
// [provider.Profile.Title], "Exa"/"Tavily"/"Perplexity"/"MCP" today). A row
// is kept only when its Verified cell (cells[3]; cells[0] is the empty text
// before the leading pipe) parses as a YYYY-MM-DD date — which is what lets
// this walk every table row in the file, including the unrelated "Vendor
// endpoints that are NOT simulated" table further down, whose corresponding
// cell is prose like "NOT SIMULATED" rather than a date, without a
// hand-rolled notion of "where the first table ends" that would break the
// moment a table is reordered. Read from disk rather than through any
// profile's embedded Contracts: contracts/README.md is the repository-level
// index, not part of any one profile's bundle.
func readmeVerifiedDates(t *testing.T) map[string]string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "contracts", "README.md"))
	if err != nil {
		t.Fatalf("reading contracts/README.md: %v", err)
	}

	dates := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			continue
		}
		title := strings.TrimSpace(cells[1])
		date := strings.TrimSpace(cells[3])
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			continue
		}
		dates[title] = date
	}
	return dates
}

// bundleVerified reads the provider-level `verified:` date at the top of
// name's own provenance.yaml (profiles/<name>/contracts/provenance.yaml),
// parsing only that one field: everything else in the file is contracts
// package territory, reached through contracts.Provenance/ProviderSpec, not
// duplicated here.
func bundleVerified(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(name, "contracts", "provenance.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var parsed struct {
		Verified string `yaml:"verified"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return parsed.Verified
}
