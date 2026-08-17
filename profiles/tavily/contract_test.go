package tavily

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/c360studio/servicesim/contracts"
)

// This file holds the vendor-specific wire-shape pins that used to live in
// contracts/contracts_test.go's Providers() loops (Phase 10 unit 6: contracts
// genericised over fs.FS, and stopped knowing which vendors exist — a shape
// assertion specific to Tavily belongs beside Tavily, reading through
// contracts.Read(Profile().Contracts, name) the same way an out-of-tree
// profile's own conformance test would). The generic provenance/golden
// discipline these fixtures are also subject to is contracts.Conform.
//
// contractGoldens is named to avoid handler_test.go's own decode, which
// decodes a live HTTP response recorder, not a contract fixture.

// TestHasASpecBlock pins the requirement that used to be
// contracts.TestEveryProviderHasSpecRecorded, generalised across all four
// reference providers but recorded once per profile, because
// contracts.Conform itself only checks a spec: block is well-formed WHEN
// present — an out-of-tree profile's vendor may publish no machine-readable
// specification at all, so Conform cannot demand one generically. Tavily
// does publish one (an OpenAPI document covering every route this profile
// simulates), so this profile's own bundle must not silently drop it.
func TestHasASpecBlock(t *testing.T) {
	t.Parallel()

	_, ok, err := contracts.ProviderSpec(Profile().Contracts)
	if err != nil {
		t.Fatalf("ProviderSpec: %v", err)
	}
	if !ok {
		t.Error("no spec: block recorded, want one — Tavily publishes a machine-readable specification " +
			"covering the routes this profile simulates")
	}
}

// TestTavilyResponseTimeIsANumber guards the plan document's other verified
// error. Tavily's schema declares response_time as a number; the plan
// encodes the string "1.15", and a string breaks every typed consumer.
func TestTavilyResponseTimeIsANumber(t *testing.T) {
	t.Parallel()

	for _, golden := range contractGoldens(t) {
		body := decodeContractGolden(t, golden)
		value, present := body["response_time"]
		if !present {
			continue
		}
		if _, ok := value.(json.Number); !ok {
			t.Errorf("%s: response_time is %T, want a JSON number", golden.Path, value)
		}
	}
}

// TestTavilyResultsCarryScore is the deliberate counterpart to
// TestExaResultsCarryNoScore (profiles/exa/contract_test.go): POST /search's
// result schema does declare score, and it is required, so dropping it here
// would be just as wrong as adding it to Exa.
//
// It is scoped to POST /search goldens via their provenance Endpoint, not to
// every Tavily fixture: POST /extract shares the "results" field NAME for an
// entirely different, vendor-documented object shape (url, raw_content,
// images, favicon — no score at all, see
// profiles/tavily/contracts/README.md's "POST /extract" § "Response"), and
// an unscoped walk would wrongly demand a field /extract's own contract
// never lists.
func TestTavilyResultsCarryScore(t *testing.T) {
	t.Parallel()

	records, err := contracts.Provenance(Profile().Contracts)
	if err != nil {
		t.Fatalf("loading provenance: %v", err)
	}

	for _, golden := range contractGoldens(t) {
		if records[golden.Name].Endpoint != "POST /search" {
			continue
		}
		body := decodeContractGolden(t, golden)
		results, ok := body["results"].([]any)
		if !ok {
			continue
		}
		for i, item := range results {
			result, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("%s: results[%d] is %T, want an object", golden.Path, i, item)
			}
			if _, ok := result["score"].(json.Number); !ok {
				t.Errorf("%s: results[%d].score is %T, want a JSON number",
					golden.Path, i, result["score"])
			}
		}
	}
}

// contractGoldens loads every JSON golden in this profile's own contract
// bundle, failing t rather than returning an error, because every caller
// here would do the same thing with it.
func contractGoldens(t *testing.T) []contracts.Golden {
	t.Helper()

	list, err := contracts.Goldens(Profile().Contracts)
	if err != nil {
		t.Fatalf("loading goldens: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("no goldens embedded")
	}
	return list
}

// decodeContractGolden returns a contract golden as a generic object.
// Numbers are kept as json.Number so a test can tell 1 from "1" and from
// 1.0.
func decodeContractGolden(t *testing.T, golden contracts.Golden) map[string]any {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(golden.Data))
	decoder.UseNumber()

	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		t.Fatalf("decoding %s: %v", golden.Path, err)
	}
	return body
}
