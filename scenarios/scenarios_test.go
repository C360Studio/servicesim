package scenarios_test

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/scenarios"
	"github.com/c360studio/servicesim/testkit"
)

// builtins is the set this build ships. Naming them here rather than deriving
// them from the directory is deliberate: deleting a file would otherwise delete
// its coverage silently, and every one of these is referenced by name from the
// README, the Dockerfile or a consumer's testkit call.
var builtins = []string{
	"async-failed",
	"async-stuck",
	"conversation",
	"empty-results",
	"extra-fields",
	"fusion-overlap",
	"happy",
	"malformed-json",
	"namespaced",
	"rate-limited",
	"server-error",
	"streaming",
	"unauthorized",
	"unknown-provider",
}

// implementedProviders are the provider entries every built-in must declare, so
// that one --scenario flag configures every listener coherently instead of
// leaving one of them serving something unrelated.
var implementedProviders = []string{
	"exa", "tavily", "perplexity", "perplexity_agent", "exa_agent_runs", "tavily_research",
}

// documentedProjectionKeys is the projection body key set docs/scenario-schema.md
// documents per provider, minus the reserved envelope keys the scenario package
// strips. The scenario package cannot check these — a projection body is an
// undecoded node whose type only its provider package knows — so a typo in a
// built-in would otherwise survive until provider.ValidateScenario runs.
var documentedProjectionKeys = map[string]map[string]bool{
	"exa": keySet("request_id", "results", "cost_dollars", "output", "answer", "contents", "find_similar",
		"stream", "resolved_search_type", "context", "extra_fields"),
	"tavily": keySet("request_id", "answer", "images", "results", "response_time",
		"auto_parameters", "usage", "extract", "extra_fields"),
	"perplexity": keySet("completion_id", "created", "model", "answer", "finish_reason",
		"citations", "search_results", "usage", "images", "related_questions", "stream", "extra_fields"),
	"perplexity_agent": keySet("response_id", "message_id", "model", "status", "answer", "queries",
		"search_results", "annotations", "error", "usage", "stream", "extra_fields"),
	"exa_agent_runs":  keySet("status", "stop_reason", "output", "error", "cost_dollars", "usage", "extra_fields"),
	"tavily_research": keySet("status", "content", "sources", "response_time", "extra_fields"),
}

// refListKeys are the projection keys whose list elements may be the scalar
// shorthand for a source reference. Every other scalar list — queries,
// snippets, highlights — is content, not a reference.
var refListKeys = keySet("citations", "search_results", "results")

func keySet(keys ...string) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out
}

func loadBuiltin(t *testing.T, name string) *scenario.Scenario {
	t.Helper()
	s, report, err := scenarios.Load(name)
	require.NoErrorf(t, err, "%s: %v", name, report.Findings)
	require.NotNil(t, s)
	return s
}

func TestNames_MatchTheShippedSet(t *testing.T) {
	t.Parallel()
	assert.Equal(t, builtins, scenarios.Names(),
		"Names must return every built-in in a stable, sorted order")
	assert.True(t, scenarios.Has(scenarios.Default), "the default built-in must be embedded")
}

// TestBuiltins_LoadValidateResolve is the guard the whole package exists for: a
// built-in that does not load is a container that will not start.
func TestBuiltins_LoadValidateResolve(t *testing.T) {
	t.Parallel()
	for _, name := range builtins {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, report, err := scenarios.Load(name)
			require.NoErrorf(t, err, "findings: %+v", report.Findings)
			require.NotNil(t, s)

			// Envelope validation must be clean, warnings included: the only
			// warning the scenario package raises on this data is a source URL
			// that is not a reserved domain, and that one must never ship.
			assert.Emptyf(t, report.Findings, "expected no findings, got %+v", report.Findings)
			assert.True(t, report.OK())

			assert.Equal(t, name, s.Name, "scenario name must match its file name")
			assert.Equal(t, scenario.SchemaVersion, s.Version)
			assert.Equal(t, scenario.DefaultBaseTime, s.BaseTime(),
				"every built-in pins the same deterministic time base")

			// Resolve ran: the source index exists and answers.
			for i := range s.Sources {
				got, ok := s.SourceByID(s.Sources[i].ID)
				require.Truef(t, ok, "source %q missing from the index", s.Sources[i].ID)
				assert.Same(t, &s.Sources[i], got)
			}
		})
	}
}

// TestBuiltins_CoverEveryImplementedProvider keeps a single --scenario flag
// coherent across all four listeners.
func TestBuiltins_CoverEveryImplementedProvider(t *testing.T) {
	t.Parallel()
	for _, name := range builtins {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := loadBuiltin(t, name)
			for _, provider := range implementedProviders {
				entry := s.Provider(provider)
				require.NotNilf(t, entry, "%s declares no %q block", name, provider)
				assert.Equal(t, provider, entry.Kind, "kind defaults to the block name")
				require.NotEmptyf(t, entry.Turns, "%s.%s has no turns", name, provider)
				for i := range entry.Turns {
					assert.Equalf(t, yaml.MappingNode, entry.Turns[i].Respond.Kind,
						"%s.%s.turns[%d].respond must be a mapping", name, provider, i)
				}
			}
		})
	}
}

// TestBuiltins_ProjectionKeysAreDocumented catches a misspelled projection key
// in a built-in, which the scenario package deliberately cannot see.
func TestBuiltins_ProjectionKeysAreDocumented(t *testing.T) {
	t.Parallel()
	for _, name := range builtins {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := loadBuiltin(t, name)
			for _, provider := range s.Providers.Names() {
				allowed, known := documentedProjectionKeys[provider]
				if !known {
					continue // an unimplemented provider's body is nobody's business here
				}
				entry := s.Provider(provider)
				for i := range entry.Turns {
					for _, key := range mappingKeys(&entry.Turns[i].Respond) {
						assert.Truef(t, allowed[key],
							"%s: providers.%s.turns[%d].respond declares %q, which docs/scenario-schema.md does not document for %s",
							name, provider, i, key, provider)
					}
				}
			}
		})
	}
}

// TestBuiltins_SourceReferencesResolve walks the undecoded projection bodies.
// Scenario.Resolve cannot reach inside them, so an unknown source reference in a
// built-in would only surface when a provider package decoded it.
func TestBuiltins_SourceReferencesResolve(t *testing.T) {
	t.Parallel()
	for _, name := range builtins {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := loadBuiltin(t, name)

			referenced := map[string]bool{}
			for _, provider := range s.Providers.Names() {
				entry := s.Provider(provider)
				for i := range entry.Turns {
					var refs []string
					collectRefs(&entry.Turns[i].Respond, "", &refs)
					for _, ref := range refs {
						_, ok := s.SourceByID(ref)
						assert.Truef(t, ok,
							"%s: providers.%s.turns[%d] references unknown source %q",
							name, provider, i, ref)
						referenced[ref] = true
					}
				}
			}
			for i := range s.Sources {
				assert.Truef(t, referenced[s.Sources[i].ID],
					"%s: source %q is declared and never projected", name, s.Sources[i].ID)
			}
		})
	}
}

// hostPattern finds every URL host in a scenario file, including the ones inside
// undecoded projection bodies that scenario.Validate never sees.
var hostPattern = regexp.MustCompile(`https?://([^/\s"'?#]+)`)

var reservedSuffixes = []string{".test", ".example", ".invalid", "example.com", "localhost"}

// TestBuiltins_UseReservedHostsOnly is the same guard scripts/lint-no-live-hosts.sh
// applies, run where the failure is cheap. A scenario URL that resolves to a real
// host is one copy-paste from a consumer's base URL pointing at a paid API.
func TestBuiltins_UseReservedHostsOnly(t *testing.T) {
	t.Parallel()
	for _, name := range builtins {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			src, err := scenarios.Read(name)
			require.NoError(t, err)
			for _, match := range hostPattern.FindAllStringSubmatch(string(src), -1) {
				host := strings.ToLower(match[1])
				assert.Truef(t, isReserved(host),
					"%s: host %q is not a reserved domain", name, host)
			}
		})
	}
}

func isReserved(host string) bool {
	for _, suffix := range reservedSuffixes {
		if host == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// TestHappy_ProjectsOneSourceThroughEverySurface pins the property the image's
// default scenario is chosen for.
func TestHappy_ProjectsOneSourceThroughEverySurface(t *testing.T) {
	t.Parallel()
	s := loadBuiltin(t, "happy")

	surfaces := map[string][]string{
		"exa /search":      refsUnder(t, s, "exa", "results"),
		"exa /answer":      refsUnder(t, s, "exa", "answer"),
		"tavily /search":   refsUnder(t, s, "tavily", "results"),
		"perplexity sonar": refsUnder(t, s, "perplexity", "search_results"),
		"perplexity agent": refsUnder(t, s, "perplexity_agent", "search_results"),
		"perplexity cites": refsUnder(t, s, "perplexity", "citations"),
	}
	for surface, refs := range surfaces {
		assert.Containsf(t, refs, "source-a", "%s must project the canonical source", surface)
	}
}

// TestFusionOverlap_OverlapsOnPurpose asserts the three arrangements the file's
// header comment promises, because a fusion test that stops overlapping stops
// testing anything.
func TestFusionOverlap_OverlapsOnPurpose(t *testing.T) {
	t.Parallel()
	s := loadBuiltin(t, "fusion-overlap")

	canonical, ok := s.SourceByID("source-a")
	require.True(t, ok)
	variant, ok := s.SourceByID("source-a-variant")
	require.True(t, ok)

	// A URL variant that canonicalises onto the same document: same scheme,
	// host and path, differing only in query and fragment.
	require.NotEqual(t, canonical.URL, variant.URL, "the variant must not be byte-identical")
	assert.Truef(t, strings.HasPrefix(variant.URL, canonical.URL+"?"),
		"the variant %q must canonicalise onto %q", variant.URL, canonical.URL)

	// The same canonical source arrives from more than one provider.
	providersReturning := 0
	for _, provider := range implementedProviders {
		entry := s.Provider(provider)
		var refs []string
		for i := range entry.Turns {
			collectRefs(&entry.Turns[i].Respond, "", &refs)
		}
		if contains(refs, "source-a") || contains(refs, "source-a-variant") {
			providersReturning++
		}
	}
	assert.GreaterOrEqual(t, providersReturning, 3,
		"the overlapping document must arrive from at least three providers")

	// Corroboration: claim-1 is asserted by the two distinct documents and by
	// the variant, so a consumer that counts before canonicalising over-counts.
	claim := s.SourcesForClaim("claim-1")
	require.Len(t, claim, 3)
	assert.Equal(t, []string{"source-a", "source-a-variant", "source-b"}, ids(claim),
		"SourcesForClaim must report in corpus declaration order")
}

// TestConversation_ScriptsAnAgenticLoop is the regression test for the turn
// model. Selection itself lives in provider.SelectTurn; what is assertable here
// is that the three documented predicate forms are present, ordered so the
// fallback is reachable last, and that they match the requests they claim to.
func TestConversation_ScriptsAnAgenticLoop(t *testing.T) {
	t.Parallel()
	s := loadBuiltin(t, "conversation")

	agent := s.Provider("perplexity_agent")
	require.NotNil(t, agent)
	require.GreaterOrEqual(t, len(agent.Turns), 3)
	require.NotNil(t, agent.Auth)
	assert.Equal(t, scenario.AuthRequired, agent.Auth.Mode)

	first, second, fallback := agent.Turns[0], agent.Turns[1], agent.Turns[len(agent.Turns)-1]

	require.NotNil(t, first.When)
	require.NotNil(t, first.When.CallIndex, "the first turn is matched by call_index")
	assert.Equal(t, 0, *first.When.CallIndex)

	require.NotNil(t, second.When)
	assert.NotEmpty(t, second.When.BodyContains, "the second turn is matched by body_contains")

	assert.True(t, fallback.When.IsEmpty(), "the last turn must be the unconditional fallback")
	for i := 0; i < len(agent.Turns)-1; i++ {
		assert.Falsef(t, agent.Turns[i].When.IsEmpty(),
			"turn %d is unconditional, so every turn after it is unreachable", i)
	}

	// The predicates match what the script claims they match. Turn selection is
	// first-match-wins in declaration order, so these three requests select
	// turns 0, 1 and 2 respectively.
	toolResult := []byte(`{"input":[{"type":"tool_result","content":"https://example.test/report-a"}]}`)
	unrelated := []byte(`{"input":[{"type":"message","content":"something else entirely"}]}`)

	// None of these turns constrains a route, so the serving key does not change
	// any outcome here; it is the real Agent key so the call reads as a real one.
	const route = "perplexity:agent"

	assert.True(t, first.When.Matches(0, route, unrelated), "turn 0 matches the first call")
	assert.False(t, first.When.Matches(1, route, toolResult), "turn 0 is confined to the first call")
	assert.True(t, second.When.Matches(1, route, toolResult), "turn 1 matches the tool result coming back")
	assert.False(t, second.When.Matches(1, route, unrelated), "turn 1 needs its substring")
	assert.True(t, fallback.When.Matches(7, route, unrelated), "the fallback matches anything")

	// The other three providers stay in the single-shot form, so this scenario
	// still configures every listener.
	for _, provider := range []string{"exa", "tavily", "perplexity"} {
		entry := s.Provider(provider)
		require.NotNil(t, entry)
		require.Lenf(t, entry.Turns, 1, "%s must stay single-shot", provider)
		assert.True(t, entry.Turns[0].When.IsEmpty())
	}
}

// TestNamespaced_DeclaresOneLanePerModel pins the shape of the file: the two
// scripted lanes must be keyed on something the request carries, and they must
// script the same call indices as each other. Two lanes whose call indices did
// not overlap would pass whether or not the cursor was shared, which would make
// the end-to-end test below prove nothing.
func TestNamespaced_DeclaresOneLanePerModel(t *testing.T) {
	t.Parallel()
	s := loadBuiltin(t, "namespaced")

	entry := s.Provider("perplexity")
	require.NotNil(t, entry)
	assert.Equal(t, []string{"route", "body_json:model"}, entry.TurnKey.Extractors(),
		"the lane must be keyed on a request field, not on the route alone")

	// Collect (model, call_index) per scripted turn. Every turn but the trailing
	// fallback names both, because a lane predicate that omits the model would
	// match in the other lane too.
	scripted := map[string][]int{}
	for i := 0; i < len(entry.Turns)-1; i++ {
		when := entry.Turns[i].When
		require.NotNilf(t, when, "turn %d must be conditional", i)
		require.NotNilf(t, when.CallIndex, "turn %d must name a call index", i)
		model, ok := when.BodyJSON["model"]
		require.Truef(t, ok, "turn %d must name the model whose lane it belongs to", i)
		scripted[model] = append(scripted[model], *when.CallIndex)
	}

	require.Len(t, scripted, 2, "the file demonstrates lanes, so it needs exactly two")
	assert.Equal(t, []int{0, 1}, scripted["sonar"])
	assert.Equal(t, []int{0, 1}, scripted["sonar-pro"],
		"both lanes must script the same indices, or a shared cursor would be indistinguishable")

	fallback := entry.Turns[len(entry.Turns)-1]
	assert.True(t, fallback.When.IsEmpty(), "a lane that runs off the end of its script must terminate")
}

// TestNamespaced_EachLaneAdvancesItsOwnCursor is the regression test for the
// failure this scenario exists to demonstrate, and it runs the real listener
// rather than reasoning about the file: one route, two callers, interleaved.
//
// Under a route-keyed cursor the three requests below are calls 0, 1 and 2 of one
// sequence, so the second request — the first in the sonar-pro lane — would be
// call 1 and would draw the turn scripted for sonar's second call. Under a
// lane-keyed cursor it is call 0 of its own sequence. The two outcomes differ in
// the response body, which is what this asserts on.
func TestNamespaced_EachLaneAdvancesItsOwnCursor(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithBuiltin("namespaced"),
		testkit.WithProviders(provider.Perplexity))

	// Interleaved deliberately: sonar, sonar-pro, sonar. A shared cursor is
	// invisible when each caller runs to completion before the next one starts.
	want := []struct {
		model  string
		answer string
	}{
		{"sonar", "sonar lane, call 0 — searching for Report A."},
		{"sonar-pro", "sonar-pro lane, call 0 — searching for Report B."},
		{"sonar", "sonar lane, call 1 — Report A states the finding."},
	}
	for i, step := range want {
		assert.Equalf(t, step.answer, sonarAnswer(t, sim, step.model), "request %d (model %q)", i, step.model)
	}
}

// sonarAnswer posts one well-formed Sonar request and returns the assistant
// message content the scenario answered with.
func sonarAnswer(t *testing.T, sim *testkit.Sim, model string) string {
	t.Helper()

	body := `{"model":"` + model + `","messages":[{"role":"user","content":"report"}]}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		sim.URL(provider.Perplexity)+"/v1/sonar", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-perplexity-key")

	resp, err := sim.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "body: %s", raw)

	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.NotEmpty(t, decoded.Choices, "a Sonar response must carry a choice")
	return decoded.Choices[0].Message.Content
}

// TestAsyncFailed_BothSurfacesReachATerminalFailure drives a create and then
// polls each async surface through its own listener to a terminal FAILED
// status — the behaviour a consumer's failure branch is written against, and
// the reason this built-in exists rather than only `happy`'s completed runs.
func TestAsyncFailed_BothSurfacesReachATerminalFailure(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithBuiltin("async-failed"),
		testkit.WithProviders(provider.Exa, provider.Tavily))

	exaHeaders := map[string]string{"x-api-key": "test-exa-key"}
	exaID := asyncCreate(t, sim, provider.Exa, "/agent/runs", `{"query":"find the finding"}`, exaHeaders)

	running := asyncPoll(t, sim, provider.Exa, "/agent/runs/"+exaID, exaHeaders)
	assert.Equal(t, "running", running["status"], "the first poll is still running")

	failed := asyncPoll(t, sim, provider.Exa, "/agent/runs/"+exaID, exaHeaders)
	assert.Equal(t, "failed", failed["status"])
	errObj, ok := failed["error"].(map[string]any)
	require.True(t, ok, "a failed run must carry an error object: %v", failed)
	assert.Equal(t, "AGENT_RUN_FAILED", errObj["code"])

	tavilyHeaders := map[string]string{"authorization": "Bearer test-tavily-key"}
	tavilyID := asyncCreate(t, sim, provider.Tavily, "/research", `{"input":"find the finding"}`, tavilyHeaders)

	pending := asyncPoll(t, sim, provider.Tavily, "/research/"+tavilyID, tavilyHeaders)
	assert.Equal(t, "pending", pending["status"], "the first poll is still pending")

	taskFailed := asyncPoll(t, sim, provider.Tavily, "/research/"+tavilyID, tavilyHeaders)
	assert.Equal(t, "failed", taskFailed["status"])
	// Verified 2026-08-15 against the vendor's research-get reference: a failed
	// poll carries ONLY the three common fields. content and sources are gated
	// on completed alone, so a failed task has nowhere on this surface to say
	// what went wrong — a consumer's failure branch has the status and nothing
	// else to read.
	assert.NotContains(t, taskFailed, "content", "a failed poll must not carry content; only a completed one does")
}

// TestAsyncStuck_NeitherSurfaceEverTerminates is the regression test for the
// built-in's whole point: Servicesim never decides a job is stuck, so every
// poll — however many — answers with the same non-terminal status.
func TestAsyncStuck_NeitherSurfaceEverTerminates(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithBuiltin("async-stuck"),
		testkit.WithProviders(provider.Exa, provider.Tavily))

	exaHeaders := map[string]string{"x-api-key": "test-exa-key"}
	exaID := asyncCreate(t, sim, provider.Exa, "/agent/runs", `{"query":"q"}`, exaHeaders)
	for i := range 3 {
		got := asyncPoll(t, sim, provider.Exa, "/agent/runs/"+exaID, exaHeaders)
		assert.Equalf(t, "running", got["status"], "poll %d", i)
	}

	tavilyHeaders := map[string]string{"authorization": "Bearer test-tavily-key"}
	tavilyID := asyncCreate(t, sim, provider.Tavily, "/research", `{"input":"q"}`, tavilyHeaders)
	for i := range 3 {
		got := asyncPoll(t, sim, provider.Tavily, "/research/"+tavilyID, tavilyHeaders)
		assert.Equalf(t, "pending", got["status"], "poll %d", i)
	}
}

// asyncCreate posts a create request against an async entry's create route and
// returns the minted identifier, under either wire spelling ("id" for Exa,
// "request_id" for Tavily).
func asyncCreate(
	t *testing.T, sim *testkit.Sim, p provider.Name, path, body string, headers map[string]string,
) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, sim.URL(p)+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := sim.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusCreated, resp.StatusCode, "create failed: %s", raw)

	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	id, _ := out["id"].(string)
	if id == "" {
		id, _ = out["request_id"].(string)
	}
	require.NotEmpty(t, id)
	return id
}

// asyncPoll issues one GET against a job's poll route and decodes the body.
func asyncPoll(
	t *testing.T, sim *testkit.Sim, p provider.Name, path string, headers map[string]string,
) map[string]any {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, sim.URL(p)+path, nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := sim.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out), "body: %s", raw)
	return out
}

// TestUnknownProvider_LoadsWithTheImplementedProvidersIntact is the promise a
// scenario file shared across repositories depends on: a provider this build
// cannot serve must never fail the load.
func TestUnknownProvider_LoadsWithTheImplementedProvidersIntact(t *testing.T) {
	t.Parallel()
	s, report, err := scenarios.Load("unknown-provider")
	require.NoErrorf(t, err, "an unimplemented provider must not fail the load: %+v", report.Findings)
	require.Empty(t, report.Errors())

	unknown := s.Provider("openai")
	require.NotNil(t, unknown, "the unimplemented block must survive the load")
	assert.Equal(t, "openai", unknown.Kind)
	assert.False(t, unknown.Implemented,
		"Implemented is set by the composition layer, which owns the handler registry")
	assert.GreaterOrEqual(t, len(unknown.Turns), 3, "the unimplemented block keeps its script")

	// The warning naming it is raised by provider.ValidateScenario, which owns
	// the handler registry; the scenario package deliberately does not know
	// which providers exist. What must hold here is that the implemented
	// providers are untouched and still serve.
	for _, provider := range implementedProviders {
		entry := s.Provider(provider)
		require.NotNilf(t, entry, "%s must still be declared", provider)
		require.Len(t, entry.Turns, 1)
		assert.NotEmptyf(t, mappingKeys(&entry.Turns[0].Respond),
			"%s must still carry a projection body", provider)
	}
	assert.Equal(t,
		[]string{"exa", "tavily", "perplexity", "perplexity_agent", "exa_agent_runs", "tavily_research", "openai"},
		s.Providers.Names(), "declaration order is preserved")
}

// planExample is the scenario YAML from docs/architecture-and-implementation-plan.md
// §"Scenario model", verbatim. Normalising a block with no turns: into exactly
// one unconditional turn is what keeps every fixture written against the plan
// valid, so it gets a test that fails loudly if someone breaks it.
const planExample = `version: 1
name: fusion-overlap

sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
    author: Example Author
    published_at: 2026-05-20T00:00:00Z
    text: Full source text.
    snippets:
      - A relevant excerpt.
    claims:
      - id: claim-1
        text: A normalized claim represented by this source.

providers:
  exa:
    results:
      - source: source-a
        score: 0.95
  tavily:
    answer: A short synthesis.
    results:
      - source: source-a
        score: 0.98
  perplexity:
    answer: A grounded answer citing Report A.
    citations:
      - source-a
    search_results:
      - source-a
`

func TestPlanExample_SingleShotFormStillLoadsVerbatim(t *testing.T) {
	t.Parallel()
	s, report, err := scenario.Parse([]byte(planExample))
	require.NoErrorf(t, err, "findings: %+v", report.Findings)
	require.Empty(t, report.Findings)

	assert.Equal(t, []string{"exa", "tavily", "perplexity"}, s.Providers.Names())
	for _, provider := range s.Providers.Names() {
		entry := s.Provider(provider)
		require.Lenf(t, entry.Turns, 1,
			"a block with no turns: must normalise into exactly one turn, not %d", len(entry.Turns))
		assert.Truef(t, entry.Turns[0].When.IsEmpty(), "the normalised turn is unconditional")
		assert.Equal(t, yaml.MappingNode, entry.Turns[0].Respond.Kind)
		assert.NotEmptyf(t, mappingKeys(&entry.Turns[0].Respond),
			"the projection body must survive normalisation")
	}
	assert.Equal(t, []string{"results"}, mappingKeys(&s.Provider("exa").Turns[0].Respond),
		"the body is the block minus its reserved envelope keys")
}

func TestLoad_UnknownNameNamesTheKnownOnes(t *testing.T) {
	t.Parallel()
	s, report, err := scenarios.Load("no-such-scenario")
	require.Error(t, err)
	assert.Nil(t, s)
	require.Len(t, report.Errors(), 1)
	for _, name := range builtins {
		assert.Contains(t, err.Error(), name)
	}

	_, err = scenarios.Read("../../etc/passwd")
	require.Error(t, err, "a name that is not a built-in never reaches the file system")
}

func TestRead_IsByteIdenticalAcrossCalls(t *testing.T) {
	t.Parallel()
	first, err := scenarios.Read(scenarios.Default)
	require.NoError(t, err)
	second, err := scenarios.Read(scenarios.Default)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))
	assert.Contains(t, string(first), "name: "+scenarios.Default)
}

// mappingKeys returns a mapping node's keys in document order.
func mappingKeys(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]string, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, n.Content[i].Value)
	}
	return out
}

// refsUnder returns the source references reachable under one projection key of
// one provider's single-shot turn.
func refsUnder(t *testing.T, s *scenario.Scenario, provider, key string) []string {
	t.Helper()
	entry := s.Provider(provider)
	require.NotNilf(t, entry, "no %q block", provider)
	require.Lenf(t, entry.Turns, 1, "%s is not single-shot", provider)

	body := &entry.Turns[0].Respond
	for i := 0; i+1 < len(body.Content); i += 2 {
		if body.Content[i].Value != key {
			continue
		}
		var refs []string
		collectRefs(body.Content[i+1], key, &refs)
		return refs
	}
	require.Failf(t, "missing projection key", "providers.%s declares no %q", provider, key)
	return nil
}

// collectRefs walks an undecoded projection body and reports every source
// reference in it: a mapping's "source" key, and the scalar shorthand wherever a
// list element may be one. parentKey is the key the node hangs off, which is
// what distinguishes a list of references from a list of prose.
func collectRefs(n *yaml.Node, parentKey string, out *[]string) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode:
		for _, child := range n.Content {
			collectRefs(child, parentKey, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Value == "source" && value.Kind == yaml.ScalarNode {
				*out = append(*out, value.Value)
				continue
			}
			collectRefs(value, key.Value, out)
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if item.Kind == yaml.ScalarNode {
				if refListKeys[parentKey] {
					*out = append(*out, item.Value)
				}
				continue
			}
			collectRefs(item, parentKey, out)
		}
	default:
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func ids(sources []*scenario.Source) []string {
	out := make([]string, 0, len(sources))
	for _, src := range sources {
		out = append(out, src.ID)
	}
	return out
}
