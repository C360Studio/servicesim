package examples_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/examples"
	"github.com/c360studio/servicesim/provider/mcp"
	"github.com/c360studio/servicesim/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpToken is the credential these tests send when a request should carry
// one. It is a fake, and naming it as a constant is what lets
// testkit.AssertNoCredentialLeak be handed the same literal the assertion
// is only as strong as.
const mcpToken = "test-mcp-key"

// newMCPClient builds the example MCP client from an environment-shaped
// base URL map — sim.BaseURLs() for a whole simulator, ns.BaseURLs() for
// one namespace — on the same terms newAdapter does for the three research
// providers.
func newMCPClient(client *http.Client, baseURLs map[string]string, opts ...examples.MCPClientOption) *examples.MCPClient {
	return examples.NewMCPClient(baseURLs["MCP_BASE_URL"], append([]examples.MCPClientOption{
		examples.WithMCPHTTPClient(client),
	}, opts...)...)
}

// TestMCPClientSendsACorrectToolsCallRequest is the canonical first test to
// copy for MCP, the same role TestAdapterSendsACorrectExaRequest plays for
// the three research providers.
//
// It asserts on the journal, not on a 200: the request carried every
// standard header the specification requires
// (contracts/mcp/README.md "Request-metadata headers") and the required
// _meta fields, and it recorded no findings at all — not even the
// WARNING-severity mcp.meta.client_info_missing, because this client sends
// clientInfo even though the specification only SHOULDs it.
func TestMCPClientSendsACorrectToolsCallRequest(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(mcp.Profile()), testkit.WithBuiltin("happy"))
	client := newMCPClient(sim.Client(), sim.BaseURLs(), examples.WithMCPBearerToken(mcpToken))

	resp, err := client.CallTool(t.Context(), "search", map[string]any{"query": "report a"})
	require.NoError(t, err)
	require.NotNil(t, resp.Result)
	assert.False(t, resp.Result.IsError)

	testkit.AssertRequestCount(t, sim, mcp.Name, 1)
	entry := sim.Requests(mcp.Name)[0]

	headers := http.Header(entry.Headers)
	assert.Equal(t, "2026-07-28", headers.Get("MCP-Protocol-Version"))
	assert.Equal(t, "tools/call", headers.Get("Mcp-Method"))
	assert.Equal(t, "search", headers.Get("Mcp-Name"))

	testkit.AssertBearerAuth(t, entry)
	testkit.AssertNoFindings(t, entry)
	testkit.AssertNoCredentialLeak(t, sim, mcpToken)
}

// TestMCPClientListsToolsInDeclarationOrder covers tools/list: the
// catalogue order the happy scenario declares, and the caching hints it
// leaves at their defaults (contracts/mcp/doc.go decision 9:
// DefaultTTLMs, DefaultCacheScope).
func TestMCPClientListsToolsInDeclarationOrder(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(mcp.Profile()), testkit.WithBuiltin("happy"))
	client := newMCPClient(sim.Client(), sim.BaseURLs())

	result, err := client.ListTools(t.Context())
	require.NoError(t, err)

	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	assert.Equal(t, []string{"search", "fetch"}, names, "tools/list order is declaration order")

	assert.Equal(t, int64(60000), result.TTLMs, "the happy scenario declares no ttl_ms override; DefaultTTLMs applies")
	assert.Equal(t, "private", result.CacheScope, "the happy scenario declares no cache_scope override; DefaultCacheScope applies")

	testkit.AssertNoErrors(t, sim.Requests(mcp.Name)[0])
}

// TestMCPClientCallToolParsesContentAndStructuredContent is the decoding
// half of the tools/call contract: both content shapes the happy scenario
// scripts (a text block resolved from a source, and a resource_link) and
// structuredContent, plus determinism — the same call twice decodes to the
// same result, proven from the consumer's own side.
func TestMCPClientCallToolParsesContentAndStructuredContent(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(mcp.Profile()), testkit.WithBuiltin("happy"))
	client := newMCPClient(sim.Client(), sim.BaseURLs())

	first, err := client.CallTool(t.Context(), "search", map[string]any{"query": "report a"})
	require.NoError(t, err)
	require.Len(t, first.Result.Content, 2)

	assert.Equal(t, "text", first.Result.Content[0].Type)
	assert.Equal(t, "Full source text of Report A.", first.Result.Content[0].Text)

	assert.Equal(t, "resource_link", first.Result.Content[1].Type)
	assert.Equal(t, "https://example.test/report-b", first.Result.Content[1].URI)
	assert.Equal(t, "source-b", first.Result.Content[1].Name)
	assert.Equal(t, "Report B", first.Result.Content[1].Title)

	var structured map[string]any
	require.NoError(t, json.Unmarshal(first.Result.StructuredContent, &structured))
	assert.Equal(t, "Report A and Report B agree on the finding.", structured["answer"])

	second, err := client.CallTool(t.Context(), "search", map[string]any{"query": "report a"})
	require.NoError(t, err)
	assert.Equal(t, first.Result, second.Result, "the same call twice is byte-identical")

	testkit.AssertRequestCount(t, sim, mcp.Name, 2)
}

// TestMCPClientCallToolUnknownToolIsNotRetryable is the failure half of the
// tools/call contract: an unknown tool is a method-level JSON-RPC error at
// HTTP 200 (contracts/mcp/doc.go decision 6), which a retry loop keyed on
// HTTP status — the only signal the specification assigns any meaning to
// here — must never mistake for a transient fault.
func TestMCPClientCallToolUnknownToolIsNotRetryable(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(mcp.Profile()), testkit.WithBuiltin("happy"))
	client := newMCPClient(sim.Client(), sim.BaseURLs())

	_, err := client.CallTool(t.Context(), "nonexistent", nil)

	var mcpErr *examples.MCPError
	require.ErrorAs(t, err, &mcpErr, "a JSON-RPC error must reach the caller as a classifiable error")
	assert.Equal(t, http.StatusOK, mcpErr.StatusCode)
	assert.Equal(t, -32602, mcpErr.Code)
	assert.Equal(t, "Unknown tool: nonexistent", mcpErr.Message)
	assert.False(t, mcpErr.Retryable(), "a 200 carrying a method-level JSON-RPC error is never worth retrying")

	testkit.AssertRequestCount(t, sim, mcp.Name, 1)
	testkit.AssertFindings(t, sim.Requests(mcp.Name)[0], "mcp.tool.unknown")
}

// mcpMaliciousContentMarkers is the marker vocabulary
// scenarios/protocol/malicious-content.yaml documents: every hostile
// vector in the corpus carries at least one of these substrings, chosen so
// a consumer's own guardrail can strings.Contains() for them. Kept here
// rather than imported, on the same terms the built-in scenario's own
// header comment describes: this list, the README's built-in-scenario
// table and scenarios/scenarios_test.go's maliciousContentMarkers must
// stay in sync by hand.
var mcpMaliciousContentMarkers = []string{
	"IGNORE ALL PREVIOUS INSTRUCTIONS",
	"<|im_start|>system",
	"sk-live-FAKE",
	"AKIAFAKE",
	"xoxb-FAKE",
	"-----BEGIN RSA PRIVATE KEY-----",
	"<script>",
	"exfil.example",
}

// TestMCPClientMaliciousContentReachesEveryShape proves a consumer's
// guardrail sees the malicious-content corpus on every wire shape MCP can
// carry it on: server/discover's instructions, tools/call's text content
// blocks, its resource_link blocks, and structuredContent.
//
// It then exercises the two guardrail postures a consuming team actually
// chooses between: a strict one that fails the whole result closed the
// moment any marker appears, and a choosier one that quarantines only the
// hostile content blocks. The two are told apart by what happens to the
// corpus's one deliberately clean source, benign-report: fail-closed
// discards it along with everything else, and quarantine keeps it.
func TestMCPClientMaliciousContentReachesEveryShape(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(mcp.Profile()), testkit.WithBuiltin("malicious-content"))
	client := newMCPClient(sim.Client(), sim.BaseURLs())

	discover, err := client.Discover(t.Context())
	require.NoError(t, err)

	result, err := client.CallTool(t.Context(), "search", map[string]any{"query": "report a"})
	require.NoError(t, err)

	var textBlocks []string
	var resourceLinks int
	for _, cb := range result.Result.Content {
		switch cb.Type {
		case "text":
			textBlocks = append(textBlocks, cb.Text)
		case "resource_link":
			resourceLinks++
		}
	}
	assert.Positive(t, resourceLinks, "the resource_link content shape is exercised too")

	haystack := discover.Instructions + "\n" + strings.Join(textBlocks, "\n") + "\n" + string(result.Result.StructuredContent)
	for _, marker := range mcpMaliciousContentMarkers {
		assert.Contains(t, haystack, marker,
			"every published marker must reach text blocks, structured_content and instructions")
	}

	require.False(t, mcpGuardrailFailClosed(haystack),
		"the corpus is hostile, so a fail-closed guardrail must reject the whole result")

	kept := mcpGuardrailQuarantine(result.Result.Content)
	require.Less(t, len(kept), len(result.Result.Content), "the quarantining guardrail actually dropped something")
	assert.True(t, mcpContainsBenignReport(kept),
		"the one clean source in the corpus survives quarantine — the property that tells the two postures apart")
}

// mcpGuardrailFailClosed is the strict consumer guardrail: any marker
// anywhere in the combined text is reason enough to reject the whole
// result. It reports whether the result is safe to use as-is.
func mcpGuardrailFailClosed(haystack string) bool {
	return !mcpContainsAnyMarker(haystack)
}

// mcpGuardrailQuarantine is the choosier consumer guardrail: it drops only
// the text content blocks that carry a marker, keeping every other block —
// including every resource_link, which names its source but carries no
// marker text of its own.
func mcpGuardrailQuarantine(content []examples.MCPContentBlock) []examples.MCPContentBlock {
	kept := make([]examples.MCPContentBlock, 0, len(content))
	for _, cb := range content {
		if cb.Type == "text" && mcpContainsAnyMarker(cb.Text) {
			continue
		}
		kept = append(kept, cb)
	}
	return kept
}

// mcpContainsAnyMarker reports whether s carries any of the published
// malicious-content markers.
func mcpContainsAnyMarker(s string) bool {
	for _, marker := range mcpMaliciousContentMarkers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// mcpContainsBenignReport reports whether content carries the corpus's one
// clean source, identified by name — the property
// TestMCPClientMaliciousContentReachesEveryShape checks survived
// quarantine.
func mcpContainsBenignReport(content []examples.MCPContentBlock) bool {
	for _, cb := range content {
		if cb.Name == "benign-report" {
			return true
		}
	}
	return false
}

// TestMCPClientToolsCallStreamsProgressThenResponse is the streaming
// counterpart to the tools/call tests above. It issues one request
// directly, the way stream_test.go does for Perplexity, to pin the raw SSE
// transcript against a golden — a real Server-Sent Events client is a
// per-consumer choice, and this is the shape of that choice, not a feature
// of the example adapter. It then exercises mcpclient.go's own SSE
// decoding through two calls to the same deterministic, unconditional
// scripted turn: one with a progress token, which collects both scripted
// notifications/progress frames ahead of the final response, and one
// without, which collects none — decision 5's "no token, no frames" rule,
// checked from the consumer's own side.
func TestMCPClientToolsCallStreamsProgressThenResponse(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(mcp.Profile()), testkit.WithBuiltin("streaming"), testkit.WithProviders(mcp.Name))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, sim.URL(mcp.Name)+"/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{`+
			`"io.modelcontextprotocol/protocolVersion":"2026-07-28",`+
			`"io.modelcontextprotocol/clientCapabilities":{},`+
			`"progressToken":"abc123"},"name":"search","arguments":{"query":"report a"}}}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "search")

	resp, err := sim.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	require.Equal(t, "no", resp.Header.Get("X-Accel-Buffering"))

	transcript, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	testkit.AssertGoldenSSE(t, "testdata/mcp-tools-call-stream.sse", transcript)

	testkit.AssertRequestCount(t, sim, mcp.Name, 1)
	entry := sim.Requests(mcp.Name)[0]
	testkit.AssertNoErrors(t, entry)

	closed := sim.AwaitStreamClosed(t, entry.Seq)
	require.NotNil(t, closed.Outcome.Stream)
	assert.Equal(t, testkit.StreamCompleted, closed.Outcome.Stream.State)

	client := newMCPClient(sim.Client(), sim.BaseURLs())

	withToken, err := client.CallTool(t.Context(), "search", map[string]any{"query": "report a"},
		examples.WithMCPProgressToken("abc123"))
	require.NoError(t, err)
	require.Len(t, withToken.Progress, 2, "one notifications/progress frame per scripted delta")
	assert.Equal(t, 1, withToken.Progress[0].Progress)
	assert.Equal(t, 2, withToken.Progress[0].Total)
	assert.Equal(t, "searching…", withToken.Progress[0].Message)
	assert.Equal(t, 2, withToken.Progress[1].Progress)
	assert.Equal(t, "ranking…", withToken.Progress[1].Message)
	require.Len(t, withToken.Result.Content, 1)
	assert.Equal(t, "Full source text of Report A.", withToken.Result.Content[0].Text)

	withoutToken, err := client.CallTool(t.Context(), "search", map[string]any{"query": "report a"})
	require.NoError(t, err)
	assert.Empty(t, withoutToken.Progress, "no progressToken, no progress frames")
	assert.Equal(t, withToken.Result, withoutToken.Result, "the script is unconditional: the token changes only the framing")

	testkit.AssertRequestCount(t, sim, mcp.Name, 3)
}

// TestMCPClientClassifiesARateLimitAsRetryable is the fault-classification
// counterpart to TestSearchExaClassifiesARateLimitAndRetriesIt: a scripted
// 429 arrives as the fixed JSON-RPC fault envelope
// (contracts/mcp/doc.go decision 6) with its message overridden, and a
// retry loop keyed on HTTP status classifies it correctly and succeeds on
// the counted retry.
func TestMCPClientClassifiesARateLimitAsRetryable(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(mcp.Profile()), testkit.WithBuiltin("rate-limited"), testkit.WithProviders(mcp.Name))
	client := newMCPClient(sim.Client(), sim.BaseURLs())

	_, err := client.CallTool(t.Context(), "search", map[string]any{"query": "report a"})

	var mcpErr *examples.MCPError
	require.ErrorAs(t, err, &mcpErr)
	assert.Equal(t, http.StatusTooManyRequests, mcpErr.StatusCode)
	assert.Equal(t, -32603, mcpErr.Code)
	assert.Equal(t, "Rate limit exceeded.", mcpErr.Message)
	assert.True(t, mcpErr.Retryable(), "a 429 is worth sending again")

	result, err := client.CallTool(t.Context(), "search", map[string]any{"query": "report a"})
	require.NoError(t, err)
	require.NotEmpty(t, result.Result.Content)

	entries := sim.Requests(mcp.Name)
	require.Len(t, entries, 2)
	assert.Equal(t, testkit.OutcomeFault, entries[0].Outcome.Kind)
	assert.Equal(t, testkit.OutcomeScenario, entries[1].Outcome.Kind)
}

// TestMCPClientParallelNamespacesEachHaveTheirOwnCursor is the namespace
// pattern from namespace_test.go, applied to MCP: one Sim, one listener,
// two callers each isolated by their own /n/<namespace> lane and their own
// turn cursor.
func TestMCPClientParallelNamespacesEachHaveTheirOwnCursor(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(mcp.Profile()), testkit.WithBuiltin("namespaced"), testkit.WithProviders(mcp.Name))

	names := []string{"alpha", "beta"}
	lanes := make([]*testkit.Namespace, len(names))

	// The wrapper subtest exists so the isolation assertion below runs
	// after the parallel children have finished, on the same terms
	// namespace_test.go's own "group" wrapper does.
	t.Run("group", func(t *testing.T) {
		for i, name := range names {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				ns := sim.NamespaceFor(t)
				lanes[i] = ns

				client := newMCPClient(ns.Client(), ns.BaseURLs())

				first, err := client.ListTools(t.Context())
				require.NoError(t, err)
				second, err := client.ListTools(t.Context())
				require.NoError(t, err)
				assert.Equal(t, first, second,
					"the namespaced scenario's mcp block is single-shot: both calls in this lane see the same catalogue")

				own := ns.Requests(mcp.Name)
				require.Len(t, own, 2, "this namespace saw only its own two calls")
				for _, e := range own {
					assert.Equal(t, ns.Name(), e.Namespace)
					testkit.AssertNoErrors(t, e)
				}
			})
		}
	})

	testkit.AssertNamespacesIsolated(t, lanes[0], lanes[1])
}

// TestMCPClientReListsToolsAfterAnUnknownToolError is the conversation
// built-in's catalogue-drift story: a turn IS a server state
// (contracts/mcp/README.md's own framing), so the scripted catalogue
// changes between call 0 and call 1 on the mcp route's own cursor. A
// client that calls a not-yet-advertised tool sees -32602, re-lists rather
// than retrying blind, discovers the tool now exists, and succeeds on the
// next call.
func TestMCPClientReListsToolsAfterAnUnknownToolError(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(mcp.Profile()), testkit.WithBuiltin("conversation"), testkit.WithProviders(mcp.Name))
	client := newMCPClient(sim.Client(), sim.BaseURLs())

	// Call 0: the catalogue at this point in the script is "search" only.
	_, err := client.CallTool(t.Context(), "search_deep", map[string]any{"query": "report a"})
	var mcpErr *examples.MCPError
	require.ErrorAs(t, err, &mcpErr)
	assert.Equal(t, -32602, mcpErr.Code)

	// Call 1: the client re-lists rather than retrying blind, and observes
	// the catalogue has drifted — search_deep is now advertised.
	list, err := client.ListTools(t.Context())
	require.NoError(t, err)
	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	assert.Equal(t, []string{"search", "search_deep"}, names, "the second scripted turn advertises search_deep")

	// Call 2: the retried call now succeeds against the updated catalogue.
	result, err := client.CallTool(t.Context(), "search_deep", map[string]any{"query": "report a"})
	require.NoError(t, err)
	require.NotEmpty(t, result.Result.Content)

	testkit.AssertRequestCount(t, sim, mcp.Name, 3)
}
