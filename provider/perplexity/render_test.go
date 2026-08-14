package perplexity

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/c360studio/servicesim/scenario"
)

// TestSonarHappyGolden compares the rendered body with the contract fixture as
// raw JSON bytes.
//
// Byte comparison rather than a decode-and-diff is the point. `"created":
// 1767225600` and `"created": "1767225600"` are indistinguishable once both have
// been through a permissive decoder, and so are an integer and a string
// anywhere else in the body — the wrong one round-trips cleanly and the bug
// survives a struct-level test.
func TestSonarHappyGolden(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, sonarScenario))

	resp, body := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, string(goldenBytes(t, "perplexity-sonar-happy.json")), string(body))
}

// TestSonarEmptyGolden pins the zero-result success. search_results and
// citations are omitted rather than emitted empty; usage.cost stays present,
// because the specification requires it even at zero cost.
func TestSonarEmptyGolden(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: golden-sonar-empty
providers:
  perplexity:
    completion_id: 7a2f4b6c-8d0e-5f13-9a2b-4c6d8e0f1a3b
    model: sonar
    usage:
      prompt_tokens: 12
`))

	resp, body := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, string(goldenBytes(t, "perplexity-sonar-empty.json")), string(body))
}

// TestSonarWireTypes asserts the traps this surface sets, at the byte level.
// Each of these survives a struct round trip with the wrong JSON type.
func TestSonarWireTypes(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, sonarScenario))
	_, raw := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	body := string(raw)

	tests := []struct {
		name string
		want string
	}{
		{"created is a JSON number", `"created":1767225600`},
		{"the choice carries a delta as well as a message even when not streaming",
			`"delta":{"role":"assistant","content":""}`},
		{"usage carries the required cost object", `"cost":{"input_tokens_cost":0.00021`},
		{"search_context_size is a string, not a count", `"search_context_size":"medium"`},
		{"citation_tokens is a number", `"citation_tokens":64`},
		{"a result with no date carries an explicit null, not an absent key",
			`"snippet":"Report B corroborates Report A across a second dataset.","date":null,"last_updated":null`},
		{"citations is still emitted for legacy consumers",
			`"citations":["https://example.test/report-a","https://example.test/report-b"]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Contains(t, body, tc.want)
		})
	}
}

// TestSonarDateFallsBackToTheSource proves the projection can leave dates to the
// canonical corpus, and that the corpus renders them at the millisecond
// precision the repository standardises on.
func TestSonarDateFallsBackToTheSource(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: derived-date
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    published_at: 2025-11-16T01:36:32.547Z
    snippets:
      - An excerpt from Report A.
providers:
  perplexity:
    answer: derived
    search_results:
      - source-a
`))

	_, body := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	require.Contains(t, string(body), `"date":"2025-11-16T01:36:32.547Z"`)
	require.Contains(t, string(body), `"snippet":"An excerpt from Report A."`)
	require.Contains(t, string(body), `"source":"web"`)
}

// TestSonarOmitFields proves a scenario can prove a consumer tolerates a missing
// optional field.
func TestSonarOmitFields(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: omitting
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  perplexity:
    answer: omitted
    search_results:
      - source: source-a
        snippet: An excerpt.
        omit_fields: [last_updated, date]
`))

	_, body := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	require.Contains(t, string(body), `"snippet":"An excerpt."`)
	require.NotContains(t, string(body), `"last_updated"`)
	require.NotContains(t, string(body), `"date"`)
}

// TestSonarExtraFields is the additive-vendor-change case: a consumer must
// tolerate properties this build does not model.
func TestSonarExtraFields(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: extra
providers:
  perplexity:
    answer: hello
    extra_fields:
      future_field: a value the vendor added last week
`))

	_, body := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	require.Contains(t, string(body), `"future_field":"a value the vendor added last week"`)
	require.Contains(t, string(body), `"object":"chat.completion"`)
}

// TestSonarUsageDerivations pins the two derived totals, so a scenario only has
// to state the numbers it cares about.
func TestSonarUsageDerivations(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: derived-usage
providers:
  perplexity:
    answer: hello
    usage:
      prompt_tokens: 10
      completion_tokens: 5
      cost:
        input_tokens_cost: 0.001
        output_tokens_cost: 0.002
`))

	_, body := s.do(t, http.MethodPost, "/v1/sonar", sonarRequest)
	require.Contains(t, string(body), `"total_tokens":15`)
	require.Contains(t, string(body), `"total_cost":0.003`)
}

// TestSonarEchoesTheRequestModel proves an unpinned projection echoes what the
// caller asked for, which is what makes a model-selection assertion possible on
// the consumer's side.
func TestSonarEchoesTheRequestModel(t *testing.T) {
	t.Parallel()
	s := newSim(t, mustScenario(t, `
version: 1
name: echo-model
providers:
  perplexity:
    answer: hello
`))

	_, body := s.do(t, http.MethodPost, "/v1/sonar",
		`{"model":"sonar-deep-research","messages":[{"role":"user","content":"hi"}]}`)
	require.Contains(t, string(body), `"model":"sonar-deep-research"`)
}

// yamlKeys returns the YAML key set a type accepts, flattening inline structs.
func yamlKeys(t reflect.Type) []string {
	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if strings.Contains(opts, "inline") {
			keys = append(keys, yamlKeys(f.Type)...)
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		keys = append(keys, name)
	}
	slices.Sort(keys)
	return keys
}

// TestRawDecodeTargetsMatchTheirProjections guards rawPerplexityResult and
// rawAgentResult against drifting from the types they decode into.
//
// The raw structs restate their fields rather than being defined-type copies,
// and that is forced rather than chosen. yaml.v3 v3.0.1 treats an inline struct
// whose pointer implements Unmarshaler as an "inline unmarshaler": it hands that
// method the whole parent mapping instead of merging the struct's keys. A
// defined-type copy keeps the embedded scenario.SourceRef, so it keeps that
// behaviour, and every key except "source" is rejected by a method that has
// never heard of them. The same rule explains why yaml.Marshal drops the
// "source" key from these projections, which is why this test compares key sets
// rather than round-tripping a value.
//
// Restating is safe only while the two lists agree, so this is the test that
// notices when they stop.
func TestRawDecodeTargetsMatchTheirProjections(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		yamlKeys(reflect.TypeOf(PerplexityResult{})),
		yamlKeys(reflect.TypeOf(rawPerplexityResult{})))
	require.Equal(t,
		yamlKeys(reflect.TypeOf(AgentResult{})),
		yamlKeys(reflect.TypeOf(rawAgentResult{})))
}

// TestProjectionMappingFormDecodes proves every field of the mapping form
// reaches the projection, which is what the restated raw structs put at risk.
func TestProjectionMappingFormDecodes(t *testing.T) {
	t.Parallel()

	var sonar PerplexityResult
	require.NoError(t, yaml.Unmarshal([]byte(`
source: source-a
snippet: An excerpt.
date: "2025-11-16"
last_updated: "2025-12-01"
source_type: attachment
omit_fields: [date]
`), &sonar))
	require.Equal(t, PerplexityResult{
		SourceRef:   scenario.SourceRef{Ref: "source-a"},
		Snippet:     "An excerpt.",
		Date:        scenario.SetNullable("2025-11-16"),
		LastUpdated: scenario.SetNullable("2025-12-01"),
		SourceType:  "attachment",
		OmitFields:  []string{"date"},
	}, sonar)

	var agent AgentResult
	require.NoError(t, yaml.Unmarshal([]byte(`
source: source-a
snippet: An excerpt.
date: "2025-11-16"
last_updated: "2025-12-01"
source_type: attachment
`), &agent))
	require.Equal(t, AgentResult{
		SourceRef:   scenario.SourceRef{Ref: "source-a"},
		Snippet:     "An excerpt.",
		Date:        "2025-11-16",
		LastUpdated: "2025-12-01",
		SourceType:  "attachment",
	}, agent)
}

// TestScalarShorthandDecodes proves the plan document's original YAML still
// loads verbatim: a bare source id where a result mapping would go.
func TestScalarShorthandDecodes(t *testing.T) {
	t.Parallel()
	s := mustScenario(t, `
version: 1
name: shorthand
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  perplexity:
    answer: A grounded answer citing Report A.
    citations:
      - source-a
    search_results:
      - source-a
`)

	entry := s.Provider(NameSonar)
	require.NotNil(t, entry)
	require.Len(t, entry.Turns, 1, "a single-shot block normalises into exactly one turn")

	var p PerplexityProjection
	require.NoError(t, entry.Turns[0].DecodeProjection(entry.Name, 0, &p))
	require.Len(t, p.SearchResults, 1)
	require.Equal(t, "source-a", p.SearchResults[0].Ref)
	require.Len(t, p.Citations, 1)
	require.Equal(t, "source-a", p.Citations[0].Ref)
}

// TestSonarValidatorRejectsBadProjections proves a bad fixture fails at boot
// rather than on a consumer's first request.
func TestSonarValidatorRejectsBadProjections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		respond  string
		wantCode string
	}{
		{
			name:     "an unknown source reference",
			respond:  "    search_results:\n      - source: source-z\n",
			wantCode: "scenario.source.unknown",
		},
		{
			name:     "an invalid finish reason",
			respond:  "    finish_reason: truncated\n",
			wantCode: "perplexity.finish_reason.invalid",
		},
		{
			name:     "an invalid source type",
			respond:  "    search_results:\n      - source: source-a\n        source_type: telepathy\n",
			wantCode: "perplexity.source_type.invalid",
		},
		{
			name:     "a field this build does not model",
			respond:  "    not_a_field: true\n",
			wantCode: CodeProjectionInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := mustScenario(t, `
version: 1
name: bad-fixture
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  perplexity:
`+tc.respond)

			findings := SonarValidator{}.ValidateProjections(s, s.Provider(NameSonar))
			require.NotEmpty(t, findings)

			codes := make([]string, 0, len(findings))
			for _, f := range findings {
				require.Equal(t, scenario.SeverityError, f.Severity)
				require.True(t, strings.HasPrefix(f.Path, "providers.perplexity.turns[0].respond"),
					"finding is not addressed by its YAML path: %+v", f)
				codes = append(codes, f.Code)
			}
			require.Contains(t, codes, tc.wantCode)
		})
	}
}

// TestValidatorsCoverBothSurfaces proves both scenario entries are registered.
// Registering only one would mean the other's fixtures were never decoded until
// the first request arrived.
func TestValidatorsCoverBothSurfaces(t *testing.T) {
	t.Parallel()
	v := Validators()
	require.Contains(t, v, NameSonar)
	require.Contains(t, v, NameAgent)
	require.Len(t, v, 2)
}
