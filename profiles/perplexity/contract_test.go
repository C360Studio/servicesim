package perplexity

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/c360studio/servicesim/contracts"
)

// This file holds the vendor-specific wire-shape pins that used to live in
// contracts/contracts_test.go's Providers() loops (Phase 10 unit 6: contracts
// genericised over fs.FS, and stopped knowing which vendors exist — a shape
// assertion specific to Perplexity belongs beside Perplexity, reading
// through contracts.Read(Profile().Contracts, name) the same way an
// out-of-tree profile's own conformance test would). The generic
// provenance/golden discipline these fixtures are also subject to is
// contracts.Conform.

// TestHasASpecBlock pins the requirement that used to be
// contracts.TestEveryProviderHasSpecRecorded, generalised across all four
// reference providers but recorded once per profile, because
// contracts.Conform itself only checks a spec: block is well-formed WHEN
// present — an out-of-tree profile's vendor may publish no machine-readable
// specification at all, so Conform cannot demand one generically.
// Perplexity does publish one (its OpenAPI document), so this profile's own
// bundle must not silently drop it.
func TestHasASpecBlock(t *testing.T) {
	t.Parallel()

	_, ok, err := contracts.ProviderSpec(Profile().Contracts)
	if err != nil {
		t.Fatalf("ProviderSpec: %v", err)
	}
	if !ok {
		t.Error("no spec: block recorded, want one — Perplexity publishes a machine-readable " +
			"specification covering the routes this profile simulates")
	}
}

// TestPerplexitySonarChoicesCarryMessageAndDelta pins the specification's
// unusual requirement: delta is declared required alongside message, so even
// a non-streaming completion carries both.
func TestPerplexitySonarChoicesCarryMessageAndDelta(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"perplexity-sonar-happy.json", "perplexity-sonar-empty.json"} {
		body := decodeContractGolden(t, name)

		choices, ok := body["choices"].([]any)
		if !ok || len(choices) == 0 {
			t.Fatalf("%s: choices is %T, want a non-empty array", name, body["choices"])
		}
		for i, item := range choices {
			choice, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("%s: choices[%d] is %T, want an object", name, i, item)
			}
			for _, key := range []string{"message", "delta"} {
				if _, ok := choice[key].(map[string]any); !ok {
					t.Errorf("%s: choices[%d].%s is %T, want an object", name, i, key, choice[key])
				}
			}
		}
	}
}

// TestPerplexitySonarUsageRequiresCost guards the field the plan document
// omits. cost is required inside UsageInfo, so a consumer validating against
// the real schema would reject a usage object without it.
func TestPerplexitySonarUsageRequiresCost(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"perplexity-sonar-happy.json", "perplexity-sonar-empty.json"} {
		body := decodeContractGolden(t, name)

		usage, ok := body["usage"].(map[string]any)
		if !ok {
			t.Fatalf("%s: usage is %T, want an object", name, body["usage"])
		}
		cost, ok := usage["cost"].(map[string]any)
		if !ok {
			t.Fatalf("%s: usage.cost is %T, want an object", name, usage["cost"])
		}
		for _, key := range []string{"input_tokens_cost", "output_tokens_cost", "total_cost"} {
			if _, ok := cost[key].(json.Number); !ok {
				t.Errorf("%s: usage.cost.%s is %T, want a JSON number", name, key, cost[key])
			}
		}
	}
}

// TestPerplexityAgentSearchResultIDsAreIntegers guards the one identifier in
// this repository that is not a string. Encoding it as a string is the
// single most likely implementation error on the Agent surface, and it
// survives a round trip through any permissive decoder, so the assertion is
// on the raw JSON number rather than on a decoded struct field.
func TestPerplexityAgentSearchResultIDsAreIntegers(t *testing.T) {
	t.Parallel()

	body := decodeContractGolden(t, "perplexity-agent-happy.json")

	output, ok := body["output"].([]any)
	if !ok {
		t.Fatalf("output is %T, want an array", body["output"])
	}

	checked := 0
	for _, item := range output {
		object, ok := item.(map[string]any)
		if !ok || object["type"] != "search_results" {
			continue
		}
		results, ok := object["results"].([]any)
		if !ok {
			t.Fatalf("search_results.results is %T, want an array", object["results"])
		}
		for i, entry := range results {
			result, ok := entry.(map[string]any)
			if !ok {
				t.Fatalf("results[%d] is %T, want an object", i, entry)
			}
			number, ok := result["id"].(json.Number)
			if !ok {
				t.Fatalf("results[%d].id is %T, want a JSON integer", i, result["id"])
			}
			if _, err := number.Int64(); err != nil {
				t.Errorf("results[%d].id is %s, want a JSON integer: %v", i, number, err)
			}
			checked++
		}
	}

	if checked == 0 {
		t.Fatal("no search_results output item carried any result to check")
	}
}

// TestPerplexityErrorEnvelopesStaySeparate pins the asymmetry the addendum
// calls out: 422 is FastAPI's array-valued detail on both surfaces, non-422
// Sonar is a string-valued detail, and non-422 Agent is the specification's
// ErrorInfo. Unifying them would be wrong for two of the three.
func TestPerplexityErrorEnvelopesStaySeparate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		golden string
		check  func(t *testing.T, body map[string]any)
	}{
		{"perplexity-sonar-422.json", func(t *testing.T, body map[string]any) {
			if _, ok := body["detail"].([]any); !ok {
				t.Errorf("detail is %T, want an array of validation errors", body["detail"])
			}
		}},
		{"perplexity-agent-422.json", func(t *testing.T, body map[string]any) {
			if _, ok := body["detail"].([]any); !ok {
				t.Errorf("detail is %T, want an array of validation errors", body["detail"])
			}
		}},
		{"perplexity-sonar-429.json", func(t *testing.T, body map[string]any) {
			if _, ok := body["detail"].(string); !ok {
				t.Errorf("detail is %T, want a string", body["detail"])
			}
		}},
		{"perplexity-agent-429.json", func(t *testing.T, body map[string]any) {
			info, ok := body["error"].(map[string]any)
			if !ok {
				t.Fatalf("error is %T, want an ErrorInfo object", body["error"])
			}
			if _, ok := info["message"].(string); !ok {
				t.Errorf("error.message is %T, want a string", info["message"])
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.golden, func(t *testing.T) {
			t.Parallel()
			tc.check(t, decodeContractGolden(t, tc.golden))
		})
	}
}

// decodeContractGolden reads and decodes one contract golden by its bare
// file name. Numbers are kept as json.Number so a test can tell 1 from "1"
// and from 1.0 — the whole point of the integer-id assertion above.
func decodeContractGolden(t *testing.T, name string) map[string]any {
	t.Helper()

	data, err := contracts.Read(Profile().Contracts, name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		t.Fatalf("decoding %s: %v", name, err)
	}
	return body
}
