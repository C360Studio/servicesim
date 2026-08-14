package contracts_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/c360studio/servicesim/contracts"
)

// TestGoldensAreValidJSON decodes every embedded fixture. A golden that is not
// valid JSON fails a provider test with a parse error a long way from here, so
// it is caught at the source instead.
func TestGoldensAreValidJSON(t *testing.T) {
	t.Parallel()

	for _, golden := range allGoldens(t) {
		t.Run(golden.Path, func(t *testing.T) {
			t.Parallel()

			decoder := json.NewDecoder(bytes.NewReader(golden.Data))
			decoder.UseNumber()

			var body any
			if err := decoder.Decode(&body); err != nil {
				t.Fatalf("decoding %s: %v", golden.Path, err)
			}
			if _, ok := body.(map[string]any); !ok {
				t.Fatalf("%s: top level is %T, want a JSON object", golden.Path, body)
			}
			if decoder.More() {
				t.Fatalf("%s: trailing content after the JSON body", golden.Path)
			}
		})
	}
}

// TestEveryGoldenHasProvenance is the reason this package exists. A fixture with
// no provenance entry cannot be reviewed: when it changes, nobody can say
// whether the vendor moved or Servicesim did.
func TestEveryGoldenHasProvenance(t *testing.T) {
	t.Parallel()

	for _, provider := range contracts.Providers() {
		records := provenance(t, provider)

		for _, golden := range goldens(t, provider) {
			if _, ok := records[golden.Name]; !ok {
				t.Errorf("%s has no entry in %s/%s", golden.Path, provider, contracts.ProvenanceFile)
			}
		}
	}
}

// TestEveryProvenanceEntryHasGolden catches the opposite drift: a record left
// behind by a deleted or renamed fixture, which would make the provenance file
// describe a shape nothing serves.
func TestEveryProvenanceEntryHasGolden(t *testing.T) {
	t.Parallel()

	for _, provider := range contracts.Providers() {
		present := make(map[string]bool)
		for _, golden := range goldens(t, provider) {
			present[golden.Name] = true
		}

		for name := range provenance(t, provider) {
			if !present[name] {
				t.Errorf("%s/%s names %s, which does not exist", provider, contracts.ProvenanceFile, name)
			}
		}
	}
}

// TestProvenanceRecordsAreComplete checks the fields a reviewer actually uses:
// where the shape was read, when, and whether the vendor or Servicesim chose it.
// A record missing any of those is not a provenance record.
func TestProvenanceRecordsAreComplete(t *testing.T) {
	t.Parallel()

	for _, provider := range contracts.Providers() {
		for name, record := range provenance(t, provider) {
			if record.Endpoint == "" {
				t.Errorf("%s/%s: no endpoint", provider, name)
			}
			if record.Status < 100 || record.Status > 599 {
				t.Errorf("%s/%s: status %d is not an HTTP status", provider, name, record.Status)
			}
			switch record.Kind {
			case contracts.VendorDocumented, contracts.SimulatorChosen:
			default:
				t.Errorf("%s/%s: kind %q is neither %q nor %q",
					provider, name, record.Kind, contracts.VendorDocumented, contracts.SimulatorChosen)
			}
			if !strings.HasPrefix(record.DocumentationURL, "https://") {
				t.Errorf("%s/%s: documentation_url %q is not an https URL",
					provider, name, record.DocumentationURL)
			}
			if record.Verified != contracts.VerifiedOn {
				t.Errorf("%s/%s: verified %q, want %q", provider, name, record.Verified, contracts.VerifiedOn)
			}
			if _, err := time.Parse(time.DateOnly, record.Verified); err != nil {
				t.Errorf("%s/%s: verified %q is not a YYYY-MM-DD date", provider, name, record.Verified)
			}
			if record.Note == "" {
				t.Errorf("%s/%s: no note saying what is load-bearing about this fixture", provider, name)
			}
		}
	}
}

// TestEveryProviderHasHappyAndEmptyAndErrorGoldens asserts the coverage the
// simulator's own tests depend on. A provider directory holding only success
// fixtures leaves every error path in the handler ungoldened.
func TestEveryProviderHasHappyAndEmptyAndErrorGoldens(t *testing.T) {
	t.Parallel()

	for _, provider := range contracts.Providers() {
		var happy, empty, errors int
		for name, record := range provenance(t, provider) {
			switch {
			case record.Status >= 400:
				errors++
			case strings.Contains(name, "empty"):
				empty++
			default:
				happy++
			}
		}

		if happy == 0 {
			t.Errorf("%s has no success golden", provider)
		}
		if empty == 0 {
			t.Errorf("%s has no empty-result golden", provider)
		}
		if errors < 2 {
			t.Errorf("%s has %d error goldens, want at least 2", provider, errors)
		}
	}
}

// TestExaResultsCarryNoScore guards the single most likely Exa mistake. The plan
// document says results carry a score float; Exa's schema has no such field, and
// a golden containing one would teach every consumer to parse something the real
// API never sends. The walk is recursive because subpages nest results.
func TestExaResultsCarryNoScore(t *testing.T) {
	t.Parallel()

	for _, golden := range goldens(t, contracts.Exa) {
		body := decode(t, golden)
		walk(body, func(where string, object map[string]any) {
			if _, found := object["score"]; found {
				t.Errorf("%s: %s carries a score field; Exa's result schema has none", golden.Path, where)
			}
		})
	}
}

// TestExaRateLimitBodyIsReduced pins the one Exa status that does not use the
// flat three-key envelope. A simulator that always emitted {requestId, error,
// tag} would be wrong for exactly the response retry logic reads.
func TestExaRateLimitBodyIsReduced(t *testing.T) {
	t.Parallel()

	body := decodeNamed(t, contracts.Exa, "exa-search-429.json")
	if len(body) != 1 {
		t.Fatalf("429 body has %d keys, want exactly one (error)", len(body))
	}
	if _, ok := body["error"].(string); !ok {
		t.Fatalf("429 body error is %T, want a string", body["error"])
	}
}

// TestTavilyResponseTimeIsANumber guards the plan document's other verified
// error. Tavily's schema declares response_time as a number; the plan encodes
// the string "1.15", and a string breaks every typed consumer.
func TestTavilyResponseTimeIsANumber(t *testing.T) {
	t.Parallel()

	for _, golden := range goldens(t, contracts.Tavily) {
		body := decode(t, golden)
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
// TestExaResultsCarryNoScore: Tavily's result schema does declare score, and it
// is required, so dropping it here would be just as wrong as adding it to Exa.
func TestTavilyResultsCarryScore(t *testing.T) {
	t.Parallel()

	for _, golden := range goldens(t, contracts.Tavily) {
		body := decode(t, golden)
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

// TestPerplexitySonarChoicesCarryMessageAndDelta pins the specification's
// unusual requirement: delta is declared required alongside message, so even a
// non-streaming completion carries both.
func TestPerplexitySonarChoicesCarryMessageAndDelta(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"perplexity-sonar-happy.json", "perplexity-sonar-empty.json"} {
		body := decodeNamed(t, contracts.Perplexity, name)

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

// TestPerplexitySonarUsageRequiresCost guards the field the plan document omits.
// cost is required inside UsageInfo, so a consumer validating against the real
// schema would reject a usage object without it.
func TestPerplexitySonarUsageRequiresCost(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"perplexity-sonar-happy.json", "perplexity-sonar-empty.json"} {
		body := decodeNamed(t, contracts.Perplexity, name)

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
// this repository that is not a string. Encoding it as a string is the single
// most likely implementation error on the Agent surface, and it survives a
// round trip through any permissive decoder, so the assertion is on the raw
// JSON number rather than on a decoded struct field.
func TestPerplexityAgentSearchResultIDsAreIntegers(t *testing.T) {
	t.Parallel()

	body := decodeNamed(t, contracts.Perplexity, "perplexity-agent-happy.json")

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

// TestPerplexityErrorEnvelopesStaySeparate pins the asymmetry the addendum calls
// out: 422 is FastAPI's array-valued detail on both surfaces, non-422 Sonar is a
// string-valued detail, and non-422 Agent is the specification's ErrorInfo.
// Unifying them would be wrong for two of the three.
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
			tc.check(t, decodeNamed(t, contracts.Perplexity, tc.golden))
		})
	}
}

// TestReadRejectsUnknownProviderAndPaths checks the two ways a caller can ask
// for something that is not here. Both must name what was wrong rather than
// return an empty result.
func TestReadRejectsUnknownProviderAndPaths(t *testing.T) {
	t.Parallel()

	if _, err := contracts.Read("openai", "anything.json"); err == nil {
		t.Error("Read accepted an unknown provider")
	}
	if _, err := contracts.Read(contracts.Exa, "../tavily/tavily-401.json"); err == nil {
		t.Error("Read accepted a path outside the provider directory")
	}
	if _, err := contracts.Goldens("openai"); err == nil {
		t.Error("Goldens accepted an unknown provider")
	}
	if _, err := contracts.Provenance("openai"); err == nil {
		t.Error("Provenance accepted an unknown provider")
	}
}

// TestFSHoldsOnlyGoldensAndProvenance keeps stray files out of the embedded
// set: anything else here would ship to consumers as contract data. The
// README.md files are deliberately not embedded — they are the human record,
// not wire data.
func TestFSHoldsOnlyGoldensAndProvenance(t *testing.T) {
	t.Parallel()

	err := fs.WalkDir(contracts.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(name, ".json"), path.Base(name) == contracts.ProvenanceFile:
		default:
			t.Errorf("%s is embedded but is neither a golden nor a provenance record", name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded contracts: %v", err)
	}
}

// allGoldens fails the test rather than returning an error, because every caller
// here would do the same thing with it.
func allGoldens(t *testing.T) []contracts.Golden {
	t.Helper()

	all, err := contracts.AllGoldens()
	if err != nil {
		t.Fatalf("loading goldens: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no goldens are embedded")
	}
	return all
}

func goldens(t *testing.T, p contracts.Provider) []contracts.Golden {
	t.Helper()

	list, err := contracts.Goldens(p)
	if err != nil {
		t.Fatalf("loading %s goldens: %v", p, err)
	}
	if len(list) == 0 {
		t.Fatalf("%s has no goldens", p)
	}
	return list
}

func provenance(t *testing.T, p contracts.Provider) map[string]contracts.Record {
	t.Helper()

	records, err := contracts.Provenance(p)
	if err != nil {
		t.Fatalf("loading %s provenance: %v", p, err)
	}
	if len(records) == 0 {
		t.Fatalf("%s has no provenance records", p)
	}
	return records
}

// decode returns a golden as a generic object. Numbers are kept as json.Number
// so a test can tell 1 from "1" and from 1.0, which is the whole point of the
// Perplexity integer-id assertion.
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

func decodeNamed(t *testing.T, p contracts.Provider, name string) map[string]any {
	t.Helper()

	data, err := contracts.Read(p, name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return decode(t, contracts.Golden{Provider: p, Name: name, Path: path.Join(string(p), name), Data: data})
}

// walk visits every JSON object in a decoded body, including objects nested in
// arrays, and reports each one's dotted location. Exa results nest through
// subpages, so a top-level-only check would miss a score field one level down.
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
