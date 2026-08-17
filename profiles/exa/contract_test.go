package exa

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"testing"

	"github.com/c360studio/servicesim/contracts"
)

// This file holds the vendor-specific wire-shape pins that used to live in
// contracts/contracts_test.go's Providers() loops (Phase 10 unit 6: contracts
// genericised over fs.FS, and stopped knowing which vendors exist — a shape
// assertion specific to Exa belongs beside Exa, reading through
// contracts.Read(Profile().Contracts, name) the same way an out-of-tree
// profile's own conformance test would). The generic provenance/golden
// discipline these fixtures are also subject to — valid JSON, a provenance
// entry, complete records, and so on — is contracts.Conform, exercised by
// TestConformance in conformance_test.go, which calls testkit.ValidateProfile.

// TestExaResultsCarryNoScore guards the single most likely Exa mistake. The
// plan document says results carry a score float; Exa's schema has no such
// field, and a golden containing one would teach every consumer to parse
// something the real API never sends. The walk is recursive because
// subpages nest results.
func TestExaResultsCarryNoScore(t *testing.T) {
	t.Parallel()

	for _, golden := range goldens(t) {
		body := decode(t, golden)
		walk(body, func(where string, object map[string]any) {
			if _, found := object["score"]; found {
				t.Errorf("%s: %s carries a score field; Exa's result schema has none", golden.Path, where)
			}
		})
	}
}

// TestHasASpecBlock pins the requirement that used to be
// contracts.TestEveryProviderHasSpecRecorded, generalised across all four
// reference providers but recorded once per profile, because
// contracts.Conform itself only checks a spec: block is well-formed WHEN
// present — an out-of-tree profile's vendor may publish no machine-readable
// specification at all, so Conform cannot demand one generically. Exa does
// publish one (an OpenAPI document covering every route this profile
// simulates), so this profile's own bundle must not silently drop it.
func TestHasASpecBlock(t *testing.T) {
	t.Parallel()

	_, ok, err := contracts.ProviderSpec(Profile().Contracts)
	if err != nil {
		t.Fatalf("ProviderSpec: %v", err)
	}
	if !ok {
		t.Error("no spec: block recorded, want one — Exa publishes a machine-readable specification " +
			"covering the routes this profile simulates")
	}
}

// TestExaRateLimitBodyIsReduced pins the one Exa status that does not use the
// flat three-key envelope. A simulator that always emitted {requestId, error,
// tag} would be wrong for exactly the response retry logic reads.
func TestExaRateLimitBodyIsReduced(t *testing.T) {
	t.Parallel()

	body := decodeNamed(t, "exa-search-429.json")
	if len(body) != 1 {
		t.Fatalf("429 body has %d keys, want exactly one (error)", len(body))
	}
	if _, ok := body["error"].(string); !ok {
		t.Fatalf("429 body error is %T, want a string", body["error"])
	}
}

// goldens loads every JSON golden in this profile's own contract bundle,
// failing t rather than returning an error, because every caller here would
// do the same thing with it.
func goldens(t *testing.T) []contracts.Golden {
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

// decode returns a golden as a generic object. Numbers are kept as
// json.Number so a test can tell 1 from "1" and from 1.0.
func decode(t *testing.T, golden contracts.Golden) map[string]any {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(golden.Data))
	decoder.UseNumber()

	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		t.Fatalf("decoding %s: %v", golden.Path, err)
	}
	return body
}

// decodeNamed reads and decodes one golden by its bare file name.
func decodeNamed(t *testing.T, name string) map[string]any {
	t.Helper()

	data, err := contracts.Read(Profile().Contracts, name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return decode(t, contracts.Golden{Name: name, Path: name, Data: data})
}

// walk visits every JSON object in a decoded body, including objects nested
// in arrays, and reports each one's dotted location. Exa results nest
// through subpages, so a top-level-only check would miss a score field one
// level down.
func walk(body map[string]any, visit func(where string, object map[string]any)) {
	var descend func(where string, value any)
	descend = func(where string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			visit(where, typed)
			for _, key := range sortedKeys(typed) {
				descend(where+"."+key, typed[key])
			}
		case []any:
			for i, item := range typed {
				descend(where+"["+strconv.Itoa(i)+"]", item)
			}
		}
	}
	descend("$", body)
}

// sortedKeys keeps failure messages stable across runs. Go map order is not.
func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
