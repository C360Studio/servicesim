package contracts

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestProviderVerifiedIsAtLeastEveryEntry checks the provider-level `verified:`
// at the top of each provenance.yaml: it must be no older than any entry it
// covers, because it claims to be the most recent whole-contract verification
// and a fixture individually re-checked after it would contradict that claim.
//
// This lives in package contracts, rather than contracts_test, because it
// reads provenanceFile.Verified directly: that field is unexported on
// purpose (see loadProvenanceFile's doc comment), so this check cannot be
// written from outside the package without adding a symbol nothing else
// needs.
func TestProviderVerifiedIsAtLeastEveryEntry(t *testing.T) {
	t.Parallel()

	for _, provider := range Providers() {
		parsed, err := loadProvenanceFile(provider)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if _, err := time.Parse(time.DateOnly, parsed.Verified); err != nil {
			t.Fatalf("%s: provider-level verified %q is not a YYYY-MM-DD date", provider, parsed.Verified)
		}

		for _, record := range parsed.Goldens {
			if record.Verified > parsed.Verified {
				t.Errorf("%s/%s: verified %q is newer than %s's provider-level verified %q",
					provider, record.Golden, record.Verified, provider, parsed.Verified)
			}
		}
	}
}

// TestProviderVerifiedMatchesReadmeIndexTable keeps provenance.yaml's
// provider-level date and contracts/README.md's index-table "Verified" column
// mechanically in agreement. That column is exactly the field that used to lie
// under the old single-global-date model — the Exa and Tavily READMEs were
// re-verified 2026-08-15 while every provenance entry was still compelled to
// claim 2026-08-14 — and this test is what keeps it from lying again.
func TestProviderVerifiedMatchesReadmeIndexTable(t *testing.T) {
	t.Parallel()

	index := readmeVerifiedDates(t)
	for _, provider := range Providers() {
		want, ok := index[provider]
		if !ok {
			t.Fatalf("contracts/README.md's index table has no dated row for %s", provider)
		}

		parsed, err := loadProvenanceFile(provider)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if parsed.Verified != want {
			t.Errorf("%s: provenance.yaml's provider-level verified is %q, "+
				"contracts/README.md's index table says %q", provider, parsed.Verified, want)
		}
	}
}

// TestSpecRetrievedIsAtLeastProviderVerified checks the one cross-field
// constraint a spec: block makes: spec.retrieved must be no EARLIER than the
// provider-level verified date, never the reverse.
//
// This is the direction the sanctioned refresh procedure produces
// (contracts/README.md "Keeping them honest" step 3), not an arbitrary pick:
// spec.retrieved is either bumped on a clean check (retrieved >= verified,
// nothing else moved) or re-fetched and re-hashed on the day a drift
// verification re-dates verified (retrieved == verified, both done together
// in one pass) — so a refresh that follows the procedure never records
// retrieved older than verified, even though the two can be produced days
// apart. For every provider's original data here, `verified` records the day
// a person checked the contract's FIELDS against the vendor's documentation,
// during the 2026-08-14/15 pass; spec.retrieved records the day THIS unit
// later fetched the specification's bytes to hash them for the first time —
// a later, dated re-fetch, done to pin a hash the original pass never
// recorded.
//
// This lives here rather than in contracts_test.go for the same reason
// TestProviderVerifiedIsAtLeastEveryEntry does: it needs
// provenanceFile.Verified directly, and that field is unexported on purpose.
func TestSpecRetrievedIsAtLeastProviderVerified(t *testing.T) {
	t.Parallel()

	for _, provider := range Providers() {
		parsed, err := loadProvenanceFile(provider)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if parsed.Spec == nil {
			continue
		}

		retrieved, err := time.Parse(time.DateOnly, parsed.Spec.Retrieved)
		if err != nil {
			t.Fatalf("%s: spec.retrieved %q is not a YYYY-MM-DD date", provider, parsed.Spec.Retrieved)
		}
		verified, err := time.Parse(time.DateOnly, parsed.Verified)
		if err != nil {
			t.Fatalf("%s: provider-level verified %q is not a YYYY-MM-DD date", provider, parsed.Verified)
		}
		if retrieved.Before(verified) {
			t.Errorf("%s: spec.retrieved %q is older than the provider-level verified %q — "+
				"a refresh must re-fetch and re-hash the spec on or after the day it re-dates verified",
				provider, parsed.Spec.Retrieved, parsed.Verified)
		}
	}
}

// readmeVerifiedDates parses the "Verified" column of contracts/README.md's
// index table, keyed by provider. It reads the file from disk rather than
// through the embedded FS: README.md files are deliberately not embedded (see
// TestFSHoldsOnlyGoldensAndProvenance in contracts_test.go).
//
// A row is kept only when its Verified cell (cells[3]; cells[0] is the empty
// text before the leading pipe) parses as a YYYY-MM-DD date. That is what lets
// this walk every table row in the file, including the unrelated "Vendor
// endpoints that are NOT simulated" table further down, whose corresponding
// cell is prose like "NOT SIMULATED" rather than a date, without a hand-rolled
// notion of "where the first table ends" that would break the moment a table
// is reordered.
func readmeVerifiedDates(t *testing.T) map[Provider]string {
	t.Helper()

	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("reading contracts/README.md: %v", err)
	}

	byName := map[string]Provider{
		"Exa":        Exa,
		"Tavily":     Tavily,
		"Perplexity": Perplexity,
	}

	dates := make(map[Provider]string, len(byName))
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			continue
		}
		provider, ok := byName[strings.TrimSpace(cells[1])]
		if !ok {
			continue
		}
		date := strings.TrimSpace(cells[3])
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			continue
		}
		dates[provider] = date
	}
	return dates
}
