package scenarios_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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
	"brownout",
	"conversation",
	"credential-rotation",
	"empty-results",
	"extra-fields",
	"fusion-overlap",
	"hang-then-abort",
	"happy",
	"malformed-json",
	"malicious-content",
	"namespaced",
	"oversized-body",
	"rate-limited",
	"server-error",
	"streaming",
	"timeout",
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
// snippets, highlights — is content, not a reference. "sources" is safe to
// include here even though the scenario root also declares a top-level
// "sources:" list of source objects: collectRefs only ever walks a turn's
// respond body, never the root document, so the two never collide.
var refListKeys = keySet("citations", "search_results", "results", "sources")

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

// hostileSourcePrefixes are malicious-content's category prefixes, minus
// benign-, which is deliberately exempt from "every provider must project
// it" — see the built-in's own header comment for why.
var hostileSourcePrefixes = []string{"inj-", "cred-", "markup-", "exfil-", "long-"}

// maliciousContentMarkers is the canonical marker vocabulary
// scenarios/protocol/malicious-content.yaml carries. It is kept in sync BY
// HAND with that file's own header comment and with the README's built-in
// table — see the built-in's header comment for the cross-reference — so a
// consumer's own guardrail test can copy this exact list and
// strings.Contains() against it.
var maliciousContentMarkers = []string{
	"IGNORE ALL PREVIOUS INSTRUCTIONS",
	"<|im_start|>system",
	"sk-live-FAKE",
	"AKIAFAKE",
	"xoxb-FAKE",
	"-----BEGIN RSA PRIVATE KEY-----",
	"<script>",
	"exfil.example",
}

// maliciousContentBaitTokens are the LITERAL fake credential byte sequences
// the cred-* sources embed, verbatim, so the live-wire test can assert on
// more than a prefix: the whole placeholder token must survive rendering.
var maliciousContentBaitTokens = []string{
	"sk-live-FAKE0000000000000000EXAMPLE",
	"AKIAFAKEFAKEFAKEFAKE",
	"xoxb-FAKE-0000000000-EXAMPLE0000FAKE0000FAKE",
	"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJGQUtFIn0.FAKESIGNATUREEXAMPLE00000000",
}

// maliciousContentLongContentMinBytes is the floor TestMaliciousContent_EachCategoryHasASource
// holds long-*'s text to — comfortably below the shipped source's actual size,
// but high enough that "a few KiB" in the header comment stays a fixture
// property rather than a description nobody checks.
const maliciousContentLongContentMinBytes = 2048

// TestMaliciousContent_EachCategoryHasASource pins the vector-coverage claim
// that every hostile category is actually represented, not merely described
// in the header comment. It also pins the benign control's existence and the
// long-* category's size, both of which the header comment promises but
// neither of which the every-provider test below can see.
func TestMaliciousContent_EachCategoryHasASource(t *testing.T) {
	t.Parallel()
	s := loadBuiltin(t, "malicious-content")

	for _, prefix := range hostileSourcePrefixes {
		found := false
		for i := range s.Sources {
			if strings.HasPrefix(s.Sources[i].ID, prefix) {
				found = true
				break
			}
		}
		assert.Truef(t, found, "no source id carries the %q prefix", prefix)
	}

	benignFound := false
	for i := range s.Sources {
		if strings.HasPrefix(s.Sources[i].ID, "benign-") {
			benignFound = true
		}
		if strings.HasPrefix(s.Sources[i].ID, "long-") {
			assert.GreaterOrEqualf(t, len(s.Sources[i].Text), maliciousContentLongContentMinBytes,
				"%s: text is only %d bytes, short of the %d-byte floor the header comment's "+
					"'a few KiB' promises", s.Sources[i].ID, len(s.Sources[i].Text), maliciousContentLongContentMinBytes)
		}
	}
	assert.True(t, benignFound, "no source id carries the benign- prefix")
}

// TestMaliciousContent_EveryHostileSourceCarriesAMarker pins that every
// hostile source individually carries a canonical marker, not only the
// corpus as a whole: TestMaliciousContent_MarkerVocabularyAppearsInTheScenario
// passes as long as SOME source supplies each marker, so one source quietly
// losing its own marker (while its neighbours keep the vocabulary-level
// check green) would otherwise go uncaught. long- is exempt: it is
// documented as marker-free by design, a large but ordinary body.
func TestMaliciousContent_EveryHostileSourceCarriesAMarker(t *testing.T) {
	t.Parallel()
	s := loadBuiltin(t, "malicious-content")

	for i := range s.Sources {
		src := &s.Sources[i]
		hostile := false
		for _, prefix := range hostileSourcePrefixes {
			if prefix == "long-" {
				continue
			}
			if strings.HasPrefix(src.ID, prefix) {
				hostile = true
				break
			}
		}
		if !hostile {
			continue
		}
		found := false
		for _, marker := range maliciousContentMarkers {
			if sourceContainsMarker(src, marker) {
				found = true
				break
			}
		}
		assert.Truef(t, found, "%s carries none of the canonical markers in its title, text or snippets", src.ID)
	}
}

// TestMaliciousContent_EveryHostileSourceReachesEveryProvider is the static
// half of the fusion-overlap pattern applied to hostile content: one corpus,
// every provider, so a consumer's guardrail is exercised on every dispatch
// path by this one scenario. benign-report is deliberately not required
// here — only that it be projected somewhere, which
// TestBuiltins_SourceReferencesResolve already guarantees for every
// declared source.
func TestMaliciousContent_EveryHostileSourceReachesEveryProvider(t *testing.T) {
	t.Parallel()
	s := loadBuiltin(t, "malicious-content")

	var hostileIDs []string
	for i := range s.Sources {
		id := s.Sources[i].ID
		for _, prefix := range hostileSourcePrefixes {
			if strings.HasPrefix(id, prefix) {
				hostileIDs = append(hostileIDs, id)
				break
			}
		}
	}
	require.NotEmpty(t, hostileIDs)

	for _, p := range implementedProviders {
		entry := s.Provider(p)
		require.NotNilf(t, entry, "malicious-content declares no %q block", p)

		var refs []string
		for i := range entry.Turns {
			collectRefs(&entry.Turns[i].Respond, "", &refs)
		}
		for _, id := range hostileIDs {
			assert.Containsf(t, refs, id, "%s does not project hostile source %q", p, id)
		}
	}
}

// TestMaliciousContent_MarkerVocabularyAppearsInTheScenario asserts the
// canonical vocabulary is actually present in the loaded scenario — in a
// source's text, title or snippets, or in a provider-authored string such as
// an answer — rather than only promised in the header comment.
func TestMaliciousContent_MarkerVocabularyAppearsInTheScenario(t *testing.T) {
	t.Parallel()
	s := loadBuiltin(t, "malicious-content")

	for _, marker := range maliciousContentMarkers {
		found := markerInSources(s, marker) || markerInProviders(s, marker)
		assert.Truef(t, found, "marker %q does not appear anywhere in malicious-content", marker)
	}
}

// markerInSources reports whether any source's title, text or snippets
// contains marker.
func markerInSources(s *scenario.Scenario, marker string) bool {
	for i := range s.Sources {
		if sourceContainsMarker(&s.Sources[i], marker) {
			return true
		}
	}
	return false
}

// sourceContainsMarker reports whether one source's title, text or snippets
// contains marker.
func sourceContainsMarker(src *scenario.Source, marker string) bool {
	if strings.Contains(src.Title, marker) || strings.Contains(src.Text, marker) {
		return true
	}
	for _, snippet := range src.Snippets {
		if strings.Contains(snippet, marker) {
			return true
		}
	}
	return false
}

// markerInProviders reports whether any provider-authored scalar value —
// an answer, a summary, a related question, and so on — contains marker.
func markerInProviders(s *scenario.Scenario, marker string) bool {
	for _, name := range s.Providers.Names() {
		entry := s.Provider(name)
		for i := range entry.Turns {
			if scalarsContain(&entry.Turns[i].Respond, marker) {
				return true
			}
		}
	}
	return false
}

// scalarsContain reports whether any scalar value reachable from n contains
// substr, walking mapping and sequence nodes recursively. It is deliberately
// blind to structure — unlike collectRefs it does not care whether a node is
// a source reference — because a provider-authored answer string is not one.
func scalarsContain(n *yaml.Node, substr string) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, substr) {
		return true
	}
	for _, c := range n.Content {
		if scalarsContain(c, substr) {
			return true
		}
	}
	return false
}

// TestMaliciousContent_WireResponsesCarryMarkersVerbatim is the live-wire
// half: every marker substring, and every literal cred-* bait token, must
// reach the client byte for byte — including <script> UN-escaped — on every
// surface that has a synthesised answer or a rendered corpus, proving
// scenario/render.go and internal/wire/render.go's SetEscapeHTML(false)
// claim from the built-in's own header comment rather than trusting it.
func TestMaliciousContent_WireResponsesCarryMarkersVerbatim(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("malicious-content"))

	cases := []struct {
		name    string
		p       provider.Name
		path    string
		body    string
		headers map[string]string
		// mustContain is an extra substring unique to what THIS request's own
		// gating flag unlocks, so the flag itself is load-bearing rather than
		// decorative — a marker every other case's request would carry
		// regardless would not prove the flag did anything.
		mustContain string
	}{
		{
			name:    "exa search",
			p:       provider.Exa,
			path:    "/search",
			body:    `{"query":"malicious content probe","numResults":20}`,
			headers: map[string]string{"x-api-key": "test-exa-key"},
		},
		{
			// text:true is what makes /answer's citations carry the source's
			// full text (provider/exa/render.go renderAnswer), not only titles.
			name:    "exa answer",
			p:       provider.Exa,
			path:    "/answer",
			body:    `{"query":"malicious content probe","text":true}`,
			headers: map[string]string{"x-api-key": "test-exa-key"},
		},
		{
			// include_raw_content:true is what makes /search's raw_content field
			// carry markup-script's override sentence (provider/tavily/render.go
			// renderRawContent) — the one string on this surface that reaches the
			// wire ONLY through raw_content, since content already falls back
			// through snippet then text and would carry every other marker even
			// without this flag.
			name: "tavily search",
			p:    provider.Tavily,
			path: "/search",
			body: `{"query":"malicious content probe","max_results":20,` +
				`"include_raw_content":true,"include_answer":true}`,
			headers:     map[string]string{"authorization": "Bearer test-tavily-key"},
			mustContain: "Raw crawl capture, byte for byte:",
		},
		{
			name:    "perplexity sonar",
			p:       provider.Perplexity,
			path:    "/v1/sonar",
			body:    `{"model":"sonar","messages":[{"role":"user","content":"malicious content probe"}]}`,
			headers: map[string]string{"authorization": "Bearer test-perplexity-key"},
		},
		{
			name:    "perplexity agent",
			p:       provider.Perplexity,
			path:    "/v1/agent",
			body:    `{"input":"malicious content probe"}`,
			headers: map[string]string{"authorization": "Bearer test-perplexity-key"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
				sim.URL(tc.p)+tc.path, strings.NewReader(tc.body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}

			resp, err := sim.Client().Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equalf(t, http.StatusOK, resp.StatusCode, "body: %s", raw)

			body := string(raw)
			for _, marker := range maliciousContentMarkers {
				assert.Containsf(t, body, marker, "%s response missing marker %q", tc.name, marker)
			}
			for _, token := range maliciousContentBaitTokens {
				assert.Containsf(t, body, token, "%s response missing literal bait token %q", tc.name, token)
			}
			// `&`, alongside `<` and `>`, must survive un-entity-escaped — the
			// header comment's SetEscapeHTML(false) claim, proven rather than
			// merely stated. markup-imgonerror's title carries one, so it
			// reaches every surface regardless of paging or gating flags.
			assert.Containsf(t, body, "&", "%s response missing an unescaped '&'", tc.name)
			if tc.mustContain != "" {
				assert.Containsf(t, body, tc.mustContain,
					"%s response missing %q — the surface's own gating flag may not be doing anything",
					tc.name, tc.mustContain)
			}
		})
	}
}

// TestMaliciousContent_CredentialBaitNeverReachesTheJournal is the journal
// half of the interplay the built-in's header comment documents: response-
// side bait never touches the journal, but a REQUEST that echoes one of the
// same tokens is still redacted there — the two are different mechanisms
// (there is no response-body field on a journal Entry at all; a request body
// is redacted by internal/redact's vendor-key pattern) and both must hold at
// once for the header comment's claim to be true rather than merely stated.
func TestMaliciousContent_CredentialBaitNeverReachesTheJournal(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("malicious-content"), testkit.WithProviders(provider.Exa))

	// An ordinary probe first, so the journal holds real traffic to scan —
	// the bait is response-side and must never appear in it.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		sim.URL(provider.Exa)+"/search", strings.NewReader(`{"query":"malicious content probe","numResults":20}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-exa-key")
	resp, err := sim.Client().Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	testkit.AssertNoCredentialLeak(t, sim, maliciousContentBaitTokens...)

	// Now a request whose OWN body echoes a cred-* token — the interplay case:
	// journal redaction of request text still works in the presence of bait.
	echoed := maliciousContentBaitTokens[0] // sk-live-FAKE0000000000000000EXAMPLE
	echoReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		sim.URL(provider.Exa)+"/search", strings.NewReader(`{"query":"`+echoed+`"}`))
	require.NoError(t, err)
	echoReq.Header.Set("Content-Type", "application/json")
	echoReq.Header.Set("x-api-key", "test-exa-key")
	echoResp, err := sim.Client().Do(echoReq)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, echoResp.Body)
	_ = echoResp.Body.Close()
	require.Equal(t, http.StatusOK, echoResp.StatusCode)

	entries := sim.AwaitRequests(t, provider.Exa, 2)
	last := entries[len(entries)-1]
	// Positive half first: the journal actually masked something (internal/
	// redact.Mask, quoted literally so this fails loudly if that constant's
	// value ever changes), rather than merely not containing the raw token —
	// which would also pass, vacuously, if the body were never journaled at
	// all.
	assert.Containsf(t, string(last.Body), "[REDACTED]",
		"the journaled request body should carry a masked value, got: %s", last.Body)
	assert.NotContainsf(t, string(last.Body), echoed,
		"the journaled request body must not carry the raw echoed token: %s", last.Body)
	// The broader scan: the raw token must not have reached ANY journal field
	// (path, query, headers, findings), not only the request body.
	testkit.AssertNoCredentialLeak(t, sim, echoed)
}

// TestMaliciousContent_AsyncTerminalSnapshotsCarryAMarker is the static
// check for the two async surfaces: a live poll loop is optional per the
// unit spec, and the terminal snapshot's own text is reachable statically.
func TestMaliciousContent_AsyncTerminalSnapshotsCarryAMarker(t *testing.T) {
	t.Parallel()
	s := loadBuiltin(t, "malicious-content")

	for _, p := range []string{"exa_agent_runs", "tavily_research"} {
		entry := s.Provider(p)
		require.NotNilf(t, entry, "malicious-content declares no %q block", p)
		require.NotEmpty(t, entry.Turns)

		terminal := &entry.Turns[len(entry.Turns)-1].Respond
		assert.Truef(t, scalarsContain(terminal, "IGNORE ALL PREVIOUS INSTRUCTIONS"),
			"%s's terminal snapshot carries no injection marker", p)
	}
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

// oversizedBodyMinBytes is the built-in's own body_bytes: value, repeated here
// so the test pins the same number the YAML declares rather than a
// independently-chosen threshold that could silently drift from it.
const oversizedBodyMinBytes = 4194304

// TestOversizedBody_FirstAttemptPadsThenCleanRetry is the oversized-body
// built-in's dedicated live test: on each synchronous surface, the first
// response is padded to at least 4 MiB with an exact Content-Length, decodes
// to the same JSON value the clean retry carries — proving padding is the
// ONLY difference — and the journal shows the padding on the first attempt
// alone, so a consumer's fail-closed ingress gate and its recovery path are
// both provable from one running listener.
func TestOversizedBody_FirstAttemptPadsThenCleanRetry(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithBuiltin("oversized-body"),
		testkit.WithProviders(provider.Exa, provider.Tavily, provider.Perplexity))

	cases := []struct {
		name    string
		p       provider.Name
		path    string
		body    string
		headers map[string]string
	}{
		{
			name:    "exa search",
			p:       provider.Exa,
			path:    "/search",
			body:    `{"query":"report"}`,
			headers: map[string]string{"x-api-key": "test-exa-key"},
		},
		{
			// /answer, /contents and /findSimilar are separate routes with their
			// own attempt budgets; the built-in declares a plan on each, as
			// rate-limited does, so a consumer whose adapter uses any of them
			// gets the same first-call padding.
			name:    "exa answer",
			p:       provider.Exa,
			path:    "/answer",
			body:    `{"query":"report"}`,
			headers: map[string]string{"x-api-key": "test-exa-key"},
		},
		{
			name:    "exa contents",
			p:       provider.Exa,
			path:    "/contents",
			body:    `{"urls":["https://example.test/report-a"]}`,
			headers: map[string]string{"x-api-key": "test-exa-key"},
		},
		{
			name:    "exa findSimilar",
			p:       provider.Exa,
			path:    "/findSimilar",
			body:    `{"url":"https://example.test/report-a"}`,
			headers: map[string]string{"x-api-key": "test-exa-key"},
		},
		{
			name:    "tavily search",
			p:       provider.Tavily,
			path:    "/search",
			body:    `{"query":"report"}`,
			headers: map[string]string{"authorization": "Bearer test-tavily-key"},
		},
		{
			// /extract is its own route with its own budget, Bearer-only.
			name:    "tavily extract",
			p:       provider.Tavily,
			path:    "/extract",
			body:    `{"urls":"https://example.test/report-a"}`,
			headers: map[string]string{"authorization": "Bearer test-tavily-key"},
		},
		{
			name:    "perplexity sonar",
			p:       provider.Perplexity,
			path:    "/v1/sonar",
			body:    `{"model":"sonar","messages":[{"role":"user","content":"report"}]}`,
			headers: map[string]string{"authorization": "Bearer test-perplexity-key"},
		},
		{
			// The Agent surface's own route: providers.perplexity_agent's
			// top-level fault: is a separate plan from Sonar's, on the same
			// physical listener, which is exactly why this test must filter
			// journal entries by Route rather than assume ordinal position —
			// see filterByRoute below.
			name:    "perplexity agent",
			p:       provider.Perplexity,
			path:    "/v1/agent",
			body:    `{"input":"report"}`,
			headers: map[string]string{"authorization": "Bearer test-perplexity-key"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			route := "POST " + tc.path
			firstBody, firstLen := oversizedBodyPost(t, sim, tc.p, tc.path, tc.body, tc.headers)
			secondBody, secondLen := oversizedBodyPost(t, sim, tc.p, tc.path, tc.body, tc.headers)

			require.GreaterOrEqualf(t, firstLen, oversizedBodyMinBytes,
				"%s: the first response's Content-Length must reach the padded minimum", tc.name)
			require.Lessf(t, secondLen, oversizedBodyMinBytes,
				"%s: the retry must not be padded", tc.name)

			var firstVal, secondVal any
			require.NoErrorf(t, json.Unmarshal(bytes.TrimRight(firstBody, " "), &firstVal),
				"%s: the padded body must still decode once trailing whitespace is trimmed", tc.name)
			require.NoErrorf(t, json.Unmarshal(secondBody, &secondVal), "%s: the clean retry must decode", tc.name)
			// The route declares a fault plan, so internal/ids (§3.1) folds
			// the claimed ATTEMPT INDEX into every generated identifier —
			// deliberately, so two calls to the same faulted route never
			// collide on a request/response id. That makes the id field(s)
			// the one AUTHORED-content-independent way the two decoded
			// values legitimately differ; normalizing them out is what
			// leaves padding as the only difference this comparison is
			// actually checking for.
			require.Equalf(t, normalizeOversizedBodyIDs(secondVal), normalizeOversizedBodyIDs(firstVal),
				"%s: padding must be the only difference between the two responses, once each call's own "+
					"attempt-indexed identifier is set aside", tc.name)

			entries := awaitRouteEntries(t, sim, tc.p, route, 2)
			require.Equalf(t, "oversized_body", entries[0].Outcome.FaultKind,
				"%s: the first entry's outcome must name the padding fault", tc.name)
			require.GreaterOrEqualf(t, entries[0].Outcome.BytesWritten, oversizedBodyMinBytes,
				"%s: the first entry's bytes_written must reach the padded minimum", tc.name)
			require.Emptyf(t, entries[1].Outcome.FaultKind,
				"%s: the second entry must carry no fault at all — the retry is clean", tc.name)
		})
	}
}

// oversizedBodyPost issues one POST against path and returns the raw response
// body and its declared Content-Length, read as an int so a test can compare
// it directly against oversizedBodyMinBytes.
func oversizedBodyPost(
	t *testing.T, sim *testkit.Sim, p provider.Name, path, body string, headers map[string]string,
) (raw []byte, contentLength int) {
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
	require.Equal(t, http.StatusOK, resp.StatusCode)

	raw, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	cl, err := strconv.Atoi(resp.Header.Get("Content-Length"))
	require.NoError(t, err, "Content-Length must be declared exactly, not omitted in favour of chunked encoding")
	require.Equal(t, len(raw), cl, "the declared Content-Length must match what was actually read")
	return raw, cl
}

// awaitRouteEntries polls p's journal until at least n entries carrying
// route exist, then returns them in arrival order. Filtering by Route,
// rather than relying on ordinal position in p's whole journal, is what
// keeps this test correct when Perplexity Sonar and the Agent surface run
// as parallel subtests against the same physical listener: both routes
// share one provider.Name and therefore one journal, so their entries
// interleave, but never with each other's Route.
func awaitRouteEntries(t *testing.T, sim *testkit.Sim, p provider.Name, route string, n int) []testkit.Entry {
	t.Helper()
	var out []testkit.Entry
	require.Eventually(t, func() bool {
		out = filterByRoute(sim.Requests(p), route)
		return len(out) >= n
	}, 5*time.Second, 10*time.Millisecond, "waiting for %d entries on route %q", n, route)
	return out
}

// oversizedBodyIDKeys names every wire field an attempt-indexed identifier is
// rendered under, across the four surfaces TestOversizedBody_
// FirstAttemptPadsThenCleanRetry exercises: Exa's requestId, Tavily's
// request_id, and "id" for both Perplexity Sonar's completion id and the
// Agent surface's response id AND its nested message id (output[].id) — one
// key name covers both since the walk below is recursive.
var oversizedBodyIDKeys = map[string]bool{"requestId": true, "request_id": true, "id": true}

// normalizeOversizedBodyIDs recursively replaces the value of any map key
// named in oversizedBodyIDKeys with a fixed placeholder, walking into nested
// maps and slices. See its call site for why: comparing two decoded
// responses from different attempts of one faulted route needs identifiers
// set aside first.
func normalizeOversizedBodyIDs(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if oversizedBodyIDKeys[k] {
				out[k] = "<id>"
				continue
			}
			out[k] = normalizeOversizedBodyIDs(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeOversizedBodyIDs(val)
		}
		return out
	default:
		return v
	}
}

// filterByRoute returns, in order, the entries whose Route equals route.
func filterByRoute(entries []testkit.Entry, route string) []testkit.Entry {
	var out []testkit.Entry
	for _, e := range entries {
		if e.Route == route {
			out = append(out, e)
		}
	}
	return out
}

// syncRouteCase names one synchronous route this build serves and the
// request that reaches it. brownout and hang-then-abort's live tests both
// walk the identical eight routes TestOversizedBody_FirstAttemptPadsThenCleanRetry's
// own table declares inline; factored out once here rather than duplicated a
// third and fourth time.
type syncRouteCase struct {
	name    string
	p       provider.Name
	path    string
	body    string
	headers map[string]string
}

// syncRouteCases returns the eight synchronous routes this build serves —
// Exa /search, /answer, /contents, /findSimilar; Tavily /search, /extract;
// Perplexity Sonar and Agent — each with a well-formed request and its
// accepted credential placement.
func syncRouteCases() []syncRouteCase {
	return []syncRouteCase{
		{
			name: "exa search", p: provider.Exa, path: "/search",
			body:    `{"query":"report"}`,
			headers: map[string]string{"x-api-key": "test-exa-key"},
		},
		{
			name: "exa answer", p: provider.Exa, path: "/answer",
			body:    `{"query":"report"}`,
			headers: map[string]string{"x-api-key": "test-exa-key"},
		},
		{
			name: "exa contents", p: provider.Exa, path: "/contents",
			body:    `{"urls":["https://example.test/report-a"]}`,
			headers: map[string]string{"x-api-key": "test-exa-key"},
		},
		{
			name: "exa findSimilar", p: provider.Exa, path: "/findSimilar",
			body:    `{"url":"https://example.test/report-a"}`,
			headers: map[string]string{"x-api-key": "test-exa-key"},
		},
		{
			name: "tavily search", p: provider.Tavily, path: "/search",
			body:    `{"query":"report"}`,
			headers: map[string]string{"authorization": "Bearer test-tavily-key"},
		},
		{
			name: "tavily extract", p: provider.Tavily, path: "/extract",
			body:    `{"urls":"https://example.test/report-a"}`,
			headers: map[string]string{"authorization": "Bearer test-tavily-key"},
		},
		{
			name: "perplexity sonar", p: provider.Perplexity, path: "/v1/sonar",
			body:    `{"model":"sonar","messages":[{"role":"user","content":"report"}]}`,
			headers: map[string]string{"authorization": "Bearer test-perplexity-key"},
		},
		{
			name: "perplexity agent", p: provider.Perplexity, path: "/v1/agent",
			body:    `{"input":"report"}`,
			headers: map[string]string{"authorization": "Bearer test-perplexity-key"},
		},
	}
}

// TestTimeout_ClientDeadlineAbandonsTheHangThenRetrySucceeds is the timeout
// built-in's dedicated live test: a client with a real deadline observes
// bytes not arriving rather than any HTTP response, its own context ends in
// context.DeadlineExceeded, and its retry — which draws the SECOND attempt on
// that route, the one past the built-in's single declared attempt — gets a
// clean response. testkit.WithSkippedDelays is deliberately never used here;
// see the built-in's own header comment and provider/clock.go's DelayMode
// doc comment for why a timeout test cannot use it.
//
// All eight of the built-in's routes are exercised, from the shared
// syncRouteCases() table, rather than a hand-picked subset: each subtest is
// t.Parallel() with only a 100ms client deadline, so the eight cost about the
// same wall clock as three would, and covering all eight is what actually
// pins that the fault plan is declared on every route rather than a handful
// — a plan missing from one route was otherwise invisible to this test.
func TestTimeout_ClientDeadlineAbandonsTheHangThenRetrySucceeds(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithBuiltin("timeout"),
		testkit.WithProviders(provider.Exa, provider.Tavily, provider.Perplexity))

	for _, tc := range syncRouteCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			route := "POST " + tc.path

			// A real deadline well short of the scenario's 30s delay: long
			// enough not to flake on a loaded CI runner, short enough that
			// the test does not meaningfully wait for it.
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				sim.URL(tc.p)+tc.path, strings.NewReader(tc.body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}

			_, err = sim.Client().Do(req) //nolint:bodyclose // err is non-nil, so there is no body to close
			require.Error(t, err, "the client's own deadline must end the request")
			require.ErrorIs(t, err, context.DeadlineExceeded)

			// The property that the server's sleep actually releases on the
			// client's cancellation, rather than lingering for the full 30s, is
			// NOT provable from the client's side: Do returns at the client's own
			// 100ms deadline regardless of what the server goroutine does, so a
			// wall-clock bound on this line could never fail even if the sleep
			// never released. awaitRouteEntries below is the real guard — its 5s
			// require.Eventually times out, naming the route, if the abandoned
			// call's entry never lands.
			//
			// The abandoned call's own entry lands on the server goroutine
			// AFTER the client above already returned; AwaitRequests, never a
			// bare sim.Requests, is mandatory here.
			abandoned := awaitRouteEntries(t, sim, tc.p, route, 1)[0]
			assert.True(t, abandoned.Outcome.Aborted, "the abandoned call's outcome must be marked aborted")
			assert.Equal(t, 0, abandoned.Outcome.BytesWritten, "nothing reached the client")
			assert.Equal(t, int64(30000), abandoned.Outcome.DelayMS,
				"the journal records the requested 30s delay, not however long the client actually waited")

			// The retry: a plain request with no short deadline, drawing the
			// SECOND attempt on this route — past the plan's one declared
			// attempt, so `after: success` serves it clean.
			retryReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
				sim.URL(tc.p)+tc.path, strings.NewReader(tc.body))
			require.NoError(t, err)
			retryReq.Header.Set("Content-Type", "application/json")
			for k, v := range tc.headers {
				retryReq.Header.Set(k, v)
			}
			resp, err := sim.Client().Do(retryReq)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equalf(t, http.StatusOK, resp.StatusCode, "body: %s", raw)

			retried := awaitRouteEntries(t, sim, tc.p, route, 2)[1]
			assert.Empty(t, retried.Outcome.FaultKind, "the retry must carry no fault at all")
			assert.Equal(t, 1, retried.Outcome.AttemptIndex,
				"the retry claims the second attempt: the abandoned call already claimed the first")
		})
	}
}

// brownoutLadderMS is the delay, in milliseconds, each of the first four
// calls on a brownout route declares — repeated here so the test pins the
// same numbers the YAML declares rather than an independently chosen ladder
// that could silently drift from it.
var brownoutLadderMS = []int64{50, 100, 200, 400}

// TestBrownout_LadderThenOutageThenRecovery is the brownout built-in's
// dedicated live test, run under the default DelayReal: four calls of
// successively worse real latency, two 503s carrying Retry-After, then a
// clean seventh call whose journal entry carries no fault at all — the
// "counted, not reset" property rate-limited pins for one 429, here pinned
// for a whole ladder. Total wall clock per route is the sum of the ladder,
// about 750ms; run across all eight routes as parallel subtests, that stays
// well short of anything a loaded CI runner would call slow.
func TestBrownout_LadderThenOutageThenRecovery(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithBuiltin("brownout"),
		testkit.WithProviders(provider.Exa, provider.Tavily, provider.Perplexity))

	for _, tc := range syncRouteCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			route := "POST " + tc.path

			for i, wantMS := range brownoutLadderMS {
				start := time.Now()
				status, _ := brownoutPost(t, sim, tc.p, tc.path, tc.body, tc.headers)
				elapsed := time.Since(start)
				require.Equalf(t, http.StatusOK, status, "call %d", i)
				assert.GreaterOrEqualf(t, elapsed, time.Duration(wantMS)*time.Millisecond,
					"call %d: the client must observe the declared delay", i)
			}
			for i := range 2 {
				status, retryAfter := brownoutPost(t, sim, tc.p, tc.path, tc.body, tc.headers)
				assert.Equalf(t, http.StatusServiceUnavailable, status, "call %d", i+len(brownoutLadderMS))
				assert.Equalf(t, "1", retryAfter, "call %d", i+len(brownoutLadderMS))
			}
			status, _ := brownoutPost(t, sim, tc.p, tc.path, tc.body, tc.headers)
			require.Equal(t, http.StatusOK, status, "the seventh call must recover")

			entries := awaitRouteEntries(t, sim, tc.p, route, 7)
			for i, wantMS := range brownoutLadderMS {
				assert.Equalf(t, wantMS, entries[i].Outcome.DelayMS, "entry %d delay_ms", i)
				assert.GreaterOrEqualf(t, entries[i].CompletedAt.Sub(entries[i].ArrivedAt),
					time.Duration(wantMS)*time.Millisecond, "entry %d completed_at - arrived_at", i)
			}
			assert.Equal(t, http.StatusServiceUnavailable, entries[4].Outcome.Status, "entry 4 status")
			assert.Equal(t, http.StatusServiceUnavailable, entries[5].Outcome.Status, "entry 5 status")
			assert.Equal(t, http.StatusOK, entries[6].Outcome.Status, "entry 6 status")
			assert.Empty(t, entries[6].Outcome.FaultKind, "the seventh entry must carry no fault at all")
		})
	}
}

// brownoutPost issues one POST and returns the response's status code and
// Retry-After header, discarding the body: brownout's ladder and outage
// attempts are asserted on status and journal fields, and the body is the
// ordinary happy-shaped projection throughout, unaffected by any of them.
func brownoutPost(
	t *testing.T, sim *testkit.Sim, p provider.Name, path, body string, headers map[string]string,
) (status int, retryAfter string) {
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
	_, err = io.Copy(io.Discard, resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header.Get("Retry-After")
}

// TestHangThenAbort_TwoAbortShapesThenSuccess is the hang-then-abort
// built-in's dedicated live test: a hang then a reset before any header
// arrives, a hang then a reset mid-body, then a clean third call — the two
// abort shapes a mid-flight disconnect takes on the wire today, both
// preceded by the hang unit 1 made observable in completed_at. Total wall
// clock per route is about 1.4s (two 700ms hangs); run across all eight
// routes as parallel subtests, that stays well short of anything a loaded CI
// runner would call slow.
func TestHangThenAbort_TwoAbortShapesThenSuccess(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithBuiltin("hang-then-abort"),
		testkit.WithProviders(provider.Exa, provider.Tavily, provider.Perplexity))

	const hang = 700 * time.Millisecond

	for _, tc := range syncRouteCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			route := "POST " + tc.path

			start := time.Now()
			hangThenAbortCall(t, sim, tc.p, tc.path, tc.body, tc.headers, false)
			require.GreaterOrEqual(t, time.Since(start), hang, "call 1 must observe the hang before it errors")

			start = time.Now()
			hangThenAbortCall(t, sim, tc.p, tc.path, tc.body, tc.headers, true)
			require.GreaterOrEqual(t, time.Since(start), hang, "call 2 must observe the hang before it errors")

			status, _ := brownoutPost(t, sim, tc.p, tc.path, tc.body, tc.headers)
			require.Equal(t, http.StatusOK, status, "call 3 must recover")

			entries := awaitRouteEntries(t, sim, tc.p, route, 3)

			assert.True(t, entries[0].Outcome.Aborted, "entry 0 must be marked aborted")
			assert.Equal(t, "close_before_headers", entries[0].Outcome.FaultKind, "entry 0 fault_kind")
			assert.GreaterOrEqual(t, entries[0].CompletedAt.Sub(entries[0].ArrivedAt), hang,
				"entry 0 completed_at - arrived_at must observe the hang (Phase 6 unit 1's fix)")

			assert.True(t, entries[1].Outcome.Aborted, "entry 1 must be marked aborted")
			assert.Equal(t, "truncate_body", entries[1].Outcome.FaultKind, "entry 1 fault_kind")
			assert.GreaterOrEqual(t, entries[1].CompletedAt.Sub(entries[1].ArrivedAt), hang,
				"entry 1 completed_at - arrived_at must observe the hang (Phase 6 unit 1's fix)")

			assert.Empty(t, entries[2].Outcome.FaultKind, "entry 2 must carry no fault at all")
		})
	}
}

// hangThenAbortCall issues one POST and requires the connection to die in
// the shape wantHeaders names: false for close_before_headers, where the
// round trip itself must fail because no status line ever arrives; true for
// truncate_body+reset, where headers and a partial body must arrive first and
// only the body read fails, with the failure specifically wrapping
// syscall.ECONNRESET — an ordinary closed connection (io.ErrUnexpectedEOF)
// would NOT satisfy this, which is what lets this assertion tell a scripted
// reset apart from `reset: true` being silently dropped from the fixture.
func hangThenAbortCall(
	t *testing.T, sim *testkit.Sim, p provider.Name, path, body string, headers map[string]string, wantHeaders bool,
) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, sim.URL(p)+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, postErr := sim.Client().Do(req) //nolint:bodyclose // closed below only when postErr is nil
	if !wantHeaders {
		require.Error(t, postErr, "close_before_headers must fail before any header arrives")
		return
	}
	require.NoError(t, postErr, "truncate_body must serve headers before the reset")
	defer func() { _ = resp.Body.Close() }()
	_, readErr := io.ReadAll(resp.Body)
	require.Errorf(t, readErr, "truncate_body must fail the body read")
	require.Truef(t, errors.Is(readErr, syscall.ECONNRESET),
		"truncate_body's reset must surface as ECONNRESET, not an ordinary close (got %v)", readErr)
}

// credentialRotationCase is one placement credential-rotation's live test
// covers, naming both the request that presents the OLD key and the one that
// presents the ROTATED key so the two bodies can differ, as they must for
// Tavily's body:api_key placement.
type credentialRotationCase struct {
	name             string
	p                provider.Name
	path             string
	oldBody, newBody string
	oldHdrs, newHdrs map[string]string
}

// TestCredentialRotation_OldKeyRejectedRotatedKeyAccepted is the
// credential-rotation built-in's dedicated live test: the old key draws the
// vendor's own 401 envelope, the rotated key draws the ordinary 200, and
// AssertDifferentCredential on the two journal entries passes while
// AssertSameCredential on the same pair fails — the assertion pair this
// unit's testkit helper exists for, exercised end to end rather than only
// unit-tested against synthetic entries. It covers one credential placement
// per provider the auth table documents: Exa x-api-key, Tavily Bearer AND
// body:api_key on /search, Perplexity Bearer.
func TestCredentialRotation_OldKeyRejectedRotatedKeyAccepted(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithBuiltin("credential-rotation"),
		testkit.WithProviders(provider.Exa, provider.Tavily, provider.Perplexity))

	cases := []credentialRotationCase{
		{
			name: "exa x-api-key", p: provider.Exa, path: "/search",
			oldBody: `{"query":"report"}`, newBody: `{"query":"report"}`,
			oldHdrs: map[string]string{"x-api-key": "old-key-EXAMPLE"},
			newHdrs: map[string]string{"x-api-key": "rotated-key-EXAMPLE"},
		},
		{
			name: "tavily bearer", p: provider.Tavily, path: "/search",
			oldBody: `{"query":"report"}`, newBody: `{"query":"report"}`,
			oldHdrs: map[string]string{"authorization": "Bearer old-key-EXAMPLE"},
			newHdrs: map[string]string{"authorization": "Bearer rotated-key-EXAMPLE"},
		},
		{
			name: "tavily body api_key", p: provider.Tavily, path: "/search",
			oldBody: `{"query":"report","api_key":"old-key-EXAMPLE"}`,
			newBody: `{"query":"report","api_key":"rotated-key-EXAMPLE"}`,
			oldHdrs: nil, newHdrs: nil,
		},
		{
			name: "perplexity bearer", p: provider.Perplexity, path: "/v1/sonar",
			oldBody: `{"model":"sonar","messages":[{"role":"user","content":"report"}]}`,
			newBody: `{"model":"sonar","messages":[{"role":"user","content":"report"}]}`,
			oldHdrs: map[string]string{"authorization": "Bearer old-key-EXAMPLE"},
			newHdrs: map[string]string{"authorization": "Bearer rotated-key-EXAMPLE"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Two of these cases (tavily bearer, tavily body api_key) target
			// the SAME physical route — Tavily accepts both placements on
			// /search — so a shared journal read here would race the two
			// subtests' entries against each other. A namespace per subtest
			// is what keeps each one reading only its own traffic, the same
			// isolation testkit.NamespaceFor exists for.
			ns := sim.NamespaceFor(t)
			url := ns.URL(tc.p) + tc.path

			oldStatus := credentialRotationPost(t, ns.Client(), url, tc.oldBody, tc.oldHdrs)
			require.Equal(t, http.StatusUnauthorized, oldStatus, "the old key must draw the vendor's own 401")

			newStatus := credentialRotationPost(t, ns.Client(), url, tc.newBody, tc.newHdrs)
			require.Equal(t, http.StatusOK, newStatus, "the rotated key must draw the ordinary 200")

			// ns.AwaitRequests already scopes to this namespace and this
			// provider; a route filter on top would be redundant, since each
			// subtest's namespace only ever carries its own two calls on one
			// route.
			entries := ns.AwaitRequests(t, tc.p, 2)
			rejected, accepted := entries[0], entries[1]

			testkit.AssertDifferentCredential(t, rejected, accepted)

			stub := &rotationStubTB{}
			testkit.AssertSameCredential(stub, rejected, accepted)
			assert.True(t, stub.failed, "AssertSameCredential must fail on two different credentials")
		})
	}
}

// rotationStubTB is the minimal testing.TB stand-in this file needs to prove
// AssertSameCredential fails without failing the real test: it records
// whether Errorf was ever called and does nothing else.
type rotationStubTB struct {
	testing.TB
	failed bool
}

func (s *rotationStubTB) Helper() {}

func (s *rotationStubTB) Errorf(string, ...any) { s.failed = true }

// credentialRotationPost issues one POST against url and returns the
// response's status code, discarding the body: this test asserts on the
// status and on the journal's credential fingerprints, never on response
// content.
func credentialRotationPost(
	t *testing.T, client *http.Client, url, body string, headers map[string]string,
) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	_, err = io.Copy(io.Discard, resp.Body)
	require.NoError(t, err)
	return resp.StatusCode
}

// TestCredentialRotation_EveryBlockRequiresTheRotatedKey is a static guard on
// the credential-rotation built-in's YAML: every one of the six provider
// blocks — sync and async alike — must expect the documented rotated key and
// declare no fault plan (the trap the header comment names). The live test
// above only exercises the four placements docs/scenario-schema.md documents
// (Exa x-api-key, Tavily Bearer and body:api_key, Perplexity Bearer), which
// leaves perplexity_agent, exa_agent_runs and tavily_research unreachable
// from a real HTTP call in this package; this check reads the loaded
// scenario directly so those three blocks are still pinned against silently
// losing their auth policy or gaining a fault plan next to it.
func TestCredentialRotation_EveryBlockRequiresTheRotatedKey(t *testing.T) {
	t.Parallel()
	s := loadBuiltin(t, "credential-rotation")

	for _, name := range implementedProviders {
		t.Run(name, func(t *testing.T) {
			entry := s.Provider(name)
			require.NotNilf(t, entry, "%s: credential-rotation must declare this block", name)
			require.NotNilf(t, entry.Auth, "%s: must declare an auth policy", name)
			assert.Equalf(t, "rotated-key-EXAMPLE", entry.Auth.ExpectKey,
				"%s: must expect the documented rotated key", name)

			require.NotEmptyf(t, entry.Turns, "%s: must have at least one turn", name)
			assert.Nilf(t, entry.Turns[0].Fault, "%s: no fault plan next to expect_key — the documented trap", name)
			if entry.Create != nil {
				assert.Nilf(t, entry.Create.Fault, "%s: no create-route fault plan either", name)
			}
		})
	}
}

// credentialRotationTrapScenario pins the trap docs/troubleshooting.md and
// the credential-rotation built-in's own header comment document: combining
// expect_key with a fault plan makes the plan's first attempt fire on the
// first AUTHENTICATED call, not the first call overall, because an auth
// rejection never claims one (provider/handle.go: resp.FaultEligible is
// forced false whenever the exchange already failed).
const credentialRotationTrapScenario = `
version: 1
name: credential-rotation-trap
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    auth:
      expect_key: rotated-key-EXAMPLE
    fault:
      attempts:
        - status: 429
          error: Rate limit exceeded.
        - status: 200
    results:
      - source: source-a
`

// TestCredentialRotation_ExpectKeyPlusFaultPlanFiresOnFirstAuthenticatedCall
// is the pinned regression for the trap credential-rotation's header comment
// documents: two calls with the wrong key are rejected and claim no attempt
// at all (attempt_index -1), and the fault plan's first attempt — a 429, not
// the 200 an author counting from the first HTTP request would expect —
// fires on the THIRD call, the first one presenting the correct key.
func TestCredentialRotation_ExpectKeyPlusFaultPlanFiresOnFirstAuthenticatedCall(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t,
		testkit.WithScenarioYAML(credentialRotationTrapScenario),
		testkit.WithProviders(provider.Exa))

	post := func(key string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			sim.URL(provider.Exa)+"/search", strings.NewReader(`{"query":"report"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", key)
		resp, err := sim.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		return resp.StatusCode
	}

	require.Equal(t, http.StatusUnauthorized, post("wrong-key-EXAMPLE"))
	require.Equal(t, http.StatusUnauthorized, post("wrong-key-EXAMPLE"))
	require.Equalf(t, http.StatusTooManyRequests, post("rotated-key-EXAMPLE"),
		"the fault plan's first attempt must claim the first AUTHENTICATED call, not the first call overall")
	require.Equal(t, http.StatusOK, post("rotated-key-EXAMPLE"))

	entries := sim.AwaitRequests(t, provider.Exa, 4)
	require.Len(t, entries, 4)
	assert.Equal(t, -1, entries[0].Outcome.AttemptIndex, "an auth rejection claims no attempt")
	assert.Equal(t, -1, entries[1].Outcome.AttemptIndex, "an auth rejection claims no attempt")
	assert.Equal(t, 0, entries[2].Outcome.AttemptIndex, "the first authenticated call claims attempt 0")
	assert.Equal(t, 1, entries[3].Outcome.AttemptIndex, "the second authenticated call claims attempt 1")
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
