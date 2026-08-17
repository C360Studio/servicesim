package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/provider"
)

// goldenFixtureScenario reproduces exactly the fixture data
// contracts/mcp/provenance.yaml's notes describe for the JSON goldens:
// the instructions string, the two-tool catalogue (web_search,
// fetch_page) in declaration order, and one source (source-a) whose
// resolved text and URL back the happy/structured results. Three
// additional "hidden" tool names (declared in results only, decision 8's
// own rule) provide the empty/tool-error/structured shapes without
// multiplying the visible catalogue — contracts/mcp goldens pin one
// response body each, not one scenario each.
const goldenFixtureScenario = `
version: 1
name: mcp-goldens
sources:
  - id: source-a
    url: https://a.test/report
    title: Deterministic Simulators Report
    text: "Deterministic simulators remove flakiness from adapter test suites."
providers:
  mcp:
    instructions: "General web search and page-fetch tools for this test corpus."
    tools:
      - name: web_search
        title: Web search
        description: "Search the test corpus for relevant pages."
        input_schema: {type: object, properties: {query: {type: string}}, required: [query]}
        annotations: {read_only_hint: true, open_world_hint: true}
      - name: fetch_page
        title: Fetch page
        description: "Fetch a single page by URL."
        input_schema: {type: object, properties: {url: {type: string}}, required: [url]}
        output_schema: {type: object, properties: {url: {type: string}, text: {type: string}}}
        annotations: {read_only_hint: true}
    results:
      web_search:
        content:
          - {type: text, source: source-a}
          - {type: resource_link, uri: "https://a.test/report", name: report, title: "Deterministic Simulators Report", mime_type: text/html}
      empty_tool:
        content: []
      error_tool:
        is_error: true
        content:
          - {type: text, text: "the requested URL could not be fetched: connection refused"}
      structured_tool:
        content:
          - {type: text, source: source-a}
        structured_content:
          text: "Deterministic simulators remove flakiness from adapter test suites."
          url: "https://a.test/report"
`

// TestGoldenWireBodies drives every JSON golden in contracts/mcp through
// the real handler and compares the response SEMANTICALLY (require.JSONEq,
// which does not care that the goldens are key-sorted while the wire is
// struct-ordered) against the golden bytes. Before this test, nothing in
// `task check` would notice the 16 JSON goldens and the handler's actual
// output drifting apart.
func TestGoldenWireBodies(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenFixtureScenario, nil)

	tests := []struct {
		golden  string
		body    string
		headers map[string]string
	}{
		{"mcp-discover-happy.json", discoverRequest("1"), stdHeaders(MethodDiscover, "")},
		{"mcp-tools-list-happy.json", listRequest("1", ""), stdHeaders(MethodToolsList, "")},
		{"mcp-tools-call-happy.json", callRequest("1", "web_search", `{"query":"widgets"}`), stdHeaders(MethodToolsCall, "web_search")},
		{"mcp-tools-call-empty.json", callRequest("1", "empty_tool", ""), stdHeaders(MethodToolsCall, "empty_tool")},
		{"mcp-tools-call-tool-error.json", callRequest("1", "error_tool", ""), stdHeaders(MethodToolsCall, "error_tool")},
		{"mcp-tools-call-structured.json", callRequest("1", "structured_tool", ""), stdHeaders(MethodToolsCall, "structured_tool")},
		{
			"mcp-error-header-mismatch.json", discoverRequest("1"),
			withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{headerMethod: "tools/list"}),
		},
		{
			"mcp-error-unsupported-version.json",
			`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":` +
				`{"io.modelcontextprotocol/protocolVersion":"1900-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{headerProtocolVersion: "1900-01-01"}),
		},
		{
			"mcp-error-method-not-found.json",
			`{"jsonrpc":"2.0","id":1,"method":"resources/list","params":{"_meta":` + defaultMeta + `}}`,
			stdHeaders("resources/list", ""),
		},
		{
			"mcp-error-unknown-tool.json", callRequest("1", "no_such_tool", ""),
			stdHeaders(MethodToolsCall, "no_such_tool"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.golden, func(t *testing.T) {
			t.Parallel()
			want := mcpGoldenBytes(t, tc.golden)
			rec := do(handler, tc.body, tc.headers)
			require.JSONEq(t, string(want), rec.Body.String())
		})
	}
}

// TestGoldenToolsListEmpty pins mcp-tools-list-empty.json against the
// zero-configuration server (no "mcp" block at all).
func TestGoldenToolsListEmpty(t *testing.T) {
	t.Parallel()
	handler := Profile().Handler(provider.Deps{})
	want := mcpGoldenBytes(t, "mcp-tools-list-empty.json")
	rec := do(handler, listRequest("1", ""), stdHeaders(MethodToolsList, ""))
	require.JSONEq(t, string(want), rec.Body.String())
}

// TestGoldenToolsCallStream pins mcp-tools-call-stream.sse byte-for-byte:
// two notifications/progress frames (one per delta) plus the final
// response frame, carrying only the single content block the golden
// shows — the streaming case's fixture is deliberately simpler than the
// happy JSON goldens' two-block result, matching the golden exactly.
func TestGoldenToolsCallStream(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: mcp-goldens-stream
sources:
  - id: source-a
    url: https://a.test/report
    title: Deterministic Simulators Report
    text: "Deterministic simulators remove flakiness from adapter test suites."
providers:
  mcp:
    stream: {when_requested: stream, deltas: ["searching…", "ranking results…"]}
    tools:
      - name: web_search
        input_schema: {type: object}
    results:
      web_search:
        content:
          - {type: text, source: source-a}
`
	sim := newSSESim(t, src)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":` +
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},` +
		`"progressToken":"tok-1"},"name":"web_search"}}`
	resp, transcript := sim.do(t, body, stdHeaders(MethodToolsCall, "web_search"))
	require.Equal(t, 200, resp.StatusCode)

	want := mcpGoldenBytes(t, "mcp-tools-call-stream.sse")
	require.Equal(t, string(want), string(transcript))
}
