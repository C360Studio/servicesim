package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// defaultMeta is the _meta object every well-formed request in this file
// carries unless a test deliberately omits or mangles it.
const defaultMeta = `{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`

// bearer is the credential a handful of tests present. It is a fake value
// in no particular vendor's shape — the specification does not document
// one — and nothing here ever reaches a real API.
const bearer = "Bearer test-key"

// goldenScenario is the fixture most tests in this file serve against: one
// scripted tool with a scripted result exercising every content type, one
// declared-but-unscripted tool, and one "hidden" scripted-only tool.
const goldenScenario = `
version: 1
name: mcp-golden
sources:
  - id: source-a
    url: https://a.test/doc
    title: Source A Title
    text: "Source A text carries an inj-marker for the malicious-content charter."
providers:
  mcp:
    instructions: "Use these tools wisely."
    tools:
      - name: search
        title: Web search
        description: "Search the web"
        input_schema: {type: object, properties: {query: {type: string}}, required: [query]}
        annotations: {read_only_hint: true}
      - name: unscripted_tool
    results:
      search:
        content:
          - {type: text, text: "hello"}
          - {type: text, source: source-a}
          - {type: resource_link, uri: "https://a.test/x", name: linkname, title: Link Title, description: desc, mime_type: text/html}
          - {type: image, data: aGVsbG8=, mime_type: image/png}
          - {type: resource, resource: {uri: "https://a.test/r", mime_type: text/plain, text: resource text}}
        structured_content: {answer: 42}
      hidden_tool:
        content:
          - {type: text, text: "a hidden tool's result"}
    extra_fields: {vendor_extra: true}
`

// discoverRequest builds a well-formed server/discover request body.
func discoverRequest(id string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"method":"server/discover","params":{"_meta":` + defaultMeta + `}}`
}

// listRequest builds a well-formed tools/list request body. extraParams, if
// non-empty, is spliced into params as additional comma-prefixed JSON.
func listRequest(id, extraParams string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/list","params":{"_meta":` + defaultMeta + extraParams + `}}`
}

// callRequest builds a well-formed tools/call request body.
func callRequest(id, name, argumentsJSON string) string {
	params := `{"_meta":` + defaultMeta + `,"name":"` + name + `"`
	if argumentsJSON != "" {
		params += `,"arguments":` + argumentsJSON
	}
	params += `}`
	return `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":` + params + `}`
}

// stdHeaders returns the standard header set a well-formed request carries
// for method, with Mcp-Name set only when name is non-empty.
func stdHeaders(method, name string) map[string]string {
	h := map[string]string{
		"Accept":              "application/json, text/event-stream",
		"Content-Type":        "application/json",
		headerProtocolVersion: "2026-07-28",
		headerMethod:          method,
	}
	if name != "" {
		h[headerName] = name
	}
	return h
}

// withHeaders returns a copy of base with overrides applied; a value of ""
// deletes the key instead of setting it, so a test can omit a header the
// base set includes.
func withHeaders(base map[string]string, overrides map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(overrides))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		if v == "" {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}

// newHandler builds the MCP handler over src, failing t if the scenario or
// its projections do not validate.
func newHandler(t *testing.T, src string, ring *journal.Ring) http.Handler {
	t.Helper()

	loaded, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)

	findings := provider.ValidateScenario(loaded, map[string]provider.Validator{string(Name): Validator{}})
	for _, f := range findings {
		require.NotEqual(t, scenario.SeverityError, f.Severity, "the fixture must validate before it is served: %+v", f)
	}

	deps := provider.Deps{Scenario: loaded, Faults: provider.MustSet(Profile()).Faults(loaded)}
	if ring != nil {
		deps.Journal = ring
	}
	return Profile().Handler(deps)
}

// do issues a POST /mcp request with the given body and headers.
func do(handler http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// decodeResult decodes a successful envelope's "result" member into out.
func decodeResult(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.NoError(t, json.Unmarshal(env.Result, out))
}

// decodeError decodes a failed envelope's "error" member.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder) rpcError {
	t.Helper()
	var env struct {
		Error rpcError `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	return env.Error
}

// -----------------------------------------------------------------------
// server/discover
// -----------------------------------------------------------------------

func TestDiscoverHappy(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, discoverRequest("1"), stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusOK, rec.Code)

	var got discoverResult
	decodeResult(t, rec, &got)
	require.Equal(t, "complete", got.ResultType)
	require.Equal(t, supportedVersions, got.SupportedVersions)
	require.Equal(t, "Use these tools wisely.", got.Instructions)
	require.Equal(t, int64(DefaultTTLMs), got.TTLMs)
	require.Equal(t, DefaultCacheScope, got.CacheScope)
	require.NotNil(t, got.Meta)
	require.Equal(t, ServerName, got.Meta.ServerInfo.Name)
	require.Equal(t, ServerVersion, got.Meta.ServerInfo.Version)

	require.JSONEq(t, `{"tools":{}}`, mustMarshal(t, got.Capabilities))

	// id is echoed as the exact JSON token sent, and extra_fields merges in.
	require.Contains(t, rec.Body.String(), `"id":1`)
	require.Contains(t, rec.Body.String(), `"vendor_extra":true`)
}

func TestDiscoverTTLAndCacheScopeOverride(t *testing.T) {
	t.Parallel()
	src := `
version: 1
name: mcp-discover-override
providers:
  mcp:
    ttl_ms: 5000
    cache_scope: public
`
	handler := newHandler(t, src, nil)
	rec := do(handler, discoverRequest(`"abc"`), stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusOK, rec.Code)

	var got discoverResult
	decodeResult(t, rec, &got)
	require.EqualValues(t, 5000, got.TTLMs)
	require.Equal(t, "public", got.CacheScope)
	require.Contains(t, rec.Body.String(), `"id":"abc"`)
}

func TestDiscoverNeverStreams(t *testing.T) {
	t.Parallel()
	src := `
version: 1
name: mcp-discover-stream
providers:
  mcp:
    stream: {when_requested: stream, deltas: ["x"]}
    tools:
      - name: search
        input_schema: {type: object}
    results:
      search: {content: [{type: text, text: hi}]}
`
	handler := newHandler(t, src, nil)
	rec := do(handler, discoverRequest("1"), stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"), "server/discover never streams, whatever the entry's policy")
	require.NotContains(t, rec.Body.String(), "data:")
}

// -----------------------------------------------------------------------
// tools/list
// -----------------------------------------------------------------------

func TestToolsListHappyAndOrder(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, listRequest("1", ""), stdHeaders(MethodToolsList, ""))
	require.Equal(t, http.StatusOK, rec.Code)

	var got listToolsResult
	decodeResult(t, rec, &got)
	require.Equal(t, "complete", got.ResultType)
	require.Len(t, got.Tools, 2)
	require.Equal(t, "search", got.Tools[0].Name)
	require.Equal(t, "unscripted_tool", got.Tools[1].Name)
	require.Equal(t, "Web search", got.Tools[0].Title)
	require.NotNil(t, got.Tools[0].Annotations)
	require.True(t, *got.Tools[0].Annotations.ReadOnlyHint)
	// unscripted_tool declared no input_schema: the no-parameter default.
	require.Equal(t, map[string]any{"type": "object"}, got.Tools[1].InputSchema)

	// tools/list is schema-required to carry ttlMs, cacheScope and
	// resultType (decision 8/9); the golden fixture sets no override, so
	// this pins the documented defaults reaching the wire, not just the
	// override path TestDiscoverTTLAndCacheScopeOverride already covers.
	require.Equal(t, int64(DefaultTTLMs), got.TTLMs)
	require.Equal(t, DefaultCacheScope, got.CacheScope)
	require.NotNil(t, got.Meta)
	require.Equal(t, ServerName, got.Meta.ServerInfo.Name)
	require.Equal(t, ServerVersion, got.Meta.ServerInfo.Version)
}

func TestToolsListEmpty(t *testing.T) {
	t.Parallel()
	src := `
version: 1
name: mcp-list-empty
providers:
  mcp: {}
`
	handler := newHandler(t, src, nil)
	rec := do(handler, listRequest("1", ""), stdHeaders(MethodToolsList, ""))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"tools":[]`)
}

func TestToolsListCursorIsInvalidParams(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, listRequest("1", `,"cursor":""`), stdHeaders(MethodToolsList, ""))
	require.Equal(t, http.StatusOK, rec.Code, "an ordinary JSON-RPC error is 200 (decision 6)")
	got := decodeError(t, rec)
	require.Equal(t, CodeInvalidParamsError, got.Code)
}

// -----------------------------------------------------------------------
// tools/call
// -----------------------------------------------------------------------

func TestToolsCallHappyEveryContentType(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, callRequest("1", "search", `{"query":"x"}`), stdHeaders(MethodToolsCall, "search"))
	require.Equal(t, http.StatusOK, rec.Code)

	var got callToolResult
	decodeResult(t, rec, &got)
	require.Equal(t, "complete", got.ResultType)
	require.False(t, got.IsError)
	require.Len(t, got.Content, 5)

	require.Equal(t, "text", got.Content[0].Type)
	require.Equal(t, "hello", got.Content[0].Text)

	require.Equal(t, "text", got.Content[1].Type)
	require.Contains(t, got.Content[1].Text, "inj-marker", "a source-ref content block must carry the source's marker text")

	require.Equal(t, "resource_link", got.Content[2].Type)
	require.Equal(t, "https://a.test/x", got.Content[2].URI)
	require.Equal(t, "linkname", got.Content[2].Name)
	require.Equal(t, "text/html", got.Content[2].MimeType)

	require.Equal(t, "image", got.Content[3].Type)
	require.Equal(t, "aGVsbG8=", got.Content[3].Data)
	require.Equal(t, "image/png", got.Content[3].MimeType)

	require.Equal(t, "resource", got.Content[4].Type)
	require.NotNil(t, got.Content[4].Resource)
	require.Equal(t, "resource text", got.Content[4].Resource.Text)

	require.EqualValues(t, map[string]any{"answer": float64(42)}, got.StructuredContent)
	require.NotNil(t, got.Meta)
	require.Equal(t, ServerName, got.Meta.ServerInfo.Name)

	// Decision 12: this profile never mints or echoes a session.
	require.Empty(t, rec.Header().Get("Mcp-Session-Id"))
}

func TestToolsCallEmptyContent(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, callRequest("1", "hidden_tool", ""), stdHeaders(MethodToolsCall, "hidden_tool"))
	require.Equal(t, http.StatusOK, rec.Code)
	var got callToolResult
	decodeResult(t, rec, &got)
	require.Len(t, got.Content, 1)
	require.Equal(t, "a hidden tool's result", got.Content[0].Text)
}

// TestContentBlockRequiredFieldsAlwaysPresent proves a content block never
// renders schema-invalid just because a fixture author left a required
// field at its Go zero value: an empty text: block still carries the
// "text" key, and a resource_link with no name: still carries "name".
func TestContentBlockRequiredFieldsAlwaysPresent(t *testing.T) {
	t.Parallel()
	src := `
version: 1
name: mcp-content-required-fields
providers:
  mcp:
    tools:
      - name: search
        input_schema: {type: object}
    results:
      search:
        content:
          - {type: text, text: ""}
          - {type: resource_link, uri: "https://a.test/x"}
`
	handler := newHandler(t, src, nil)
	rec := do(handler, callRequest("1", "search", ""), stdHeaders(MethodToolsCall, "search"))
	require.Equal(t, http.StatusOK, rec.Code)

	var env struct {
		Result struct {
			Content []map[string]any `json:"content"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Len(t, env.Result.Content, 2)

	text, ok := env.Result.Content[0]["text"]
	require.True(t, ok, `an empty "text" block must still carry the "text" key`)
	require.Equal(t, "", text)

	name, ok := env.Result.Content[1]["name"]
	require.True(t, ok, `a resource_link with no name: must still carry the "name" key (schema-required)`)
	require.Equal(t, "", name)
}

func TestToolsCallUnscriptedToolIsVisibleIsError(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, callRequest("1", "unscripted_tool", ""), stdHeaders(MethodToolsCall, "unscripted_tool"))
	require.Equal(t, http.StatusOK, rec.Code)
	var got callToolResult
	decodeResult(t, rec, &got)
	require.True(t, got.IsError)
	require.Len(t, got.Content, 1)
	require.Contains(t, got.Content[0].Text, "no scripted result for tool")
}

func TestToolsCallUnknownToolIsInvalidParamsAt200(t *testing.T) {
	t.Parallel()
	ring := journal.NewRing(10, 1<<20)
	handler := newHandler(t, goldenScenario, ring)

	rec := do(handler, callRequest("1", "nonexistent", ""), stdHeaders(MethodToolsCall, "nonexistent"))
	require.Equal(t, http.StatusOK, rec.Code, "decision 6: unknown tool is a 200 with a JSON-RPC error")
	got := decodeError(t, rec)
	require.Equal(t, CodeInvalidParamsError, got.Code)
	require.Contains(t, got.Message, "Unknown tool: nonexistent")

	// The spec's own words: "a consumer testing its unknown-tool path must
	// not see an ERROR" — this must stay a WARNING in the journal.
	entry := ring.Snapshot()[0]
	require.True(t, hasFinding(entry, CodeToolUnknown))
	for _, f := range entry.Findings {
		if f.Code == CodeToolUnknown {
			require.Equal(t, journal.SeverityWarning, f.Severity)
		}
	}
}

// TestParamHeadersAreIgnoredWithWarning pins decision 4: an unrecognised
// Mcp-Param-* request header is never honoured, but its presence is
// still observed with a WARNING (never silently dropped, never a
// rejection).
func TestParamHeadersAreIgnoredWithWarning(t *testing.T) {
	t.Parallel()
	ring := journal.NewRing(10, 1<<20)
	handler := newHandler(t, goldenScenario, ring)

	rec := do(handler, discoverRequest("1"),
		withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{"Mcp-Param-Region": "us-east"}))
	require.Equal(t, http.StatusOK, rec.Code, "an ignored header must never fail the request")

	entry := ring.Snapshot()[0]
	require.True(t, hasFinding(entry, CodeHeaderParamIgnored))
	for _, f := range entry.Findings {
		if f.Code == CodeHeaderParamIgnored {
			require.Equal(t, journal.SeverityWarning, f.Severity)
		}
	}
}

func TestToolsCallNameRequired(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":` + defaultMeta + `}}`
	rec := do(handler, body, withHeaders(stdHeaders(MethodToolsCall, ""), map[string]string{headerName: "search"}))
	require.Equal(t, http.StatusOK, rec.Code)
	got := decodeError(t, rec)
	require.Equal(t, CodeInvalidParamsError, got.Code)
}

func TestToolsCallArgumentsMustBeObject(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, callRequest("1", "search", `"not an object"`), stdHeaders(MethodToolsCall, "search"))
	require.Equal(t, http.StatusOK, rec.Code)
	got := decodeError(t, rec)
	require.Equal(t, CodeInvalidParamsError, got.Code)
}

func TestToolsCallIDIsExactlyEchoed(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	tests := []struct{ id, want string }{
		{`1`, `"id":1`},
		{`"abc-123"`, `"id":"abc-123"`},
		{`9007199254740993`, `"id":9007199254740993`}, // beyond float64's exact-integer range
	}
	for _, tc := range tests {
		rec := do(handler, discoverRequest(tc.id), stdHeaders(MethodDiscover, ""))
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), tc.want)
	}
}

// -----------------------------------------------------------------------
// determinism
// -----------------------------------------------------------------------

func TestDeterminismByteIdentical(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	first := do(handler, callRequest("1", "search", `{"query":"x"}`), stdHeaders(MethodToolsCall, "search"))
	second := do(handler, callRequest("1", "search", `{"query":"x"}`), stdHeaders(MethodToolsCall, "search"))
	require.Equal(t, first.Body.String(), second.Body.String())
}

// -----------------------------------------------------------------------
// fault budget: a rejected request must not consume an attempt (§4.4)
// -----------------------------------------------------------------------

// TestRequestRejectionsDoNotConsumeFaultBudget drives a scripted
// rate-limit plan (429 on attempt 0, 200 on attempt 1) against requests
// that are rejected before dispatch: an invalid cursor, a missing
// tools/call name, non-object arguments, and an unknown method. Each must
// answer on its own terms — never the fault's 429 — and, critically, must
// leave the fault plan's first attempt unclaimed for the next real call.
// Before this fix, selectProjection ran ahead of these checks and claimed
// (and memoised) the attempt regardless of the later rejection, so the
// scripted 429 status landed on the rejected response anyway while its
// body stayed the rejection's own -32602 shape — neither a clean
// rejection nor a clean fault.
func TestRequestRejectionsDoNotConsumeFaultBudget(t *testing.T) {
	t.Parallel()

	const budgetScenario = `
version: 1
name: mcp-fault-budget
providers:
  mcp:
    fault:
      attempts:
        - status: 429
        - status: 200
    tools:
      - name: search
        input_schema: {type: object}
    results:
      search:
        content: [{type: text, text: hi}]
`
	tests := []struct {
		name    string
		body    string
		headers map[string]string
	}{
		{
			name:    "tools/list invalid cursor",
			body:    listRequest("1", `,"cursor":""`),
			headers: stdHeaders(MethodToolsList, ""),
		},
		{
			name:    "tools/call name required",
			body:    `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":` + defaultMeta + `}}`,
			headers: withHeaders(stdHeaders(MethodToolsCall, ""), map[string]string{headerName: "search"}),
		},
		{
			name:    "tools/call arguments not an object",
			body:    callRequest("1", "search", `"not an object"`),
			headers: stdHeaders(MethodToolsCall, "search"),
		},
		{
			name:    "unknown method",
			body:    `{"jsonrpc":"2.0","id":1,"method":"resources/list","params":{"_meta":` + defaultMeta + `}}`,
			headers: stdHeaders("resources/list", ""),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler := newHandler(t, budgetScenario, nil)

			rec := do(handler, tc.body, tc.headers)
			require.NotEqual(t, http.StatusTooManyRequests, rec.Code,
				"a rejected request must not consume the scripted fault's attempt")

			rec2 := do(handler, discoverRequest("2"), stdHeaders(MethodDiscover, ""))
			require.Equal(t, http.StatusTooManyRequests, rec2.Code,
				"the fault plan's first attempt must still be unclaimed for the first real call")
		})
	}
}

// TestUnknownToolIsFaultEligible proves the OTHER half of the fix: once a
// well-formed tools/call has legitimately claimed an attempt (the tool
// name it names simply is not in the catalogue), a scripted fault DOES
// apply to it — through the fixed -32603 envelope, not a hybrid of the
// fault's status and the unknown-tool body.
func TestUnknownToolIsFaultEligible(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: mcp-fault-unknown-tool
providers:
  mcp:
    fault:
      attempts:
        - status: 503
`
	sim := newSSESim(t, src)
	resp, body := sim.do(t, callRequest("7", "nonexistent", ""), stdHeaders(MethodToolsCall, "nonexistent"))
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, `{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"servicesim scripted fault: status"}}`,
		strings.TrimSpace(string(body)))
}

// -----------------------------------------------------------------------
// decision 6's shape/status/code matrix
// -----------------------------------------------------------------------

func TestRequestShapeErrors(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	tests := []struct {
		name          string
		body          string
		headers       map[string]string
		wantStatus    int
		wantCode      int
		wantIDOmitted bool
	}{
		{
			name: "unparseable JSON", body: `{"jsonrpc":`,
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeParseError, wantIDOmitted: true,
		},
		{
			name: "empty body", body: "",
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeParseError, wantIDOmitted: true,
		},
		{
			name: "a top-level array", body: `[{"jsonrpc":"2.0","id":1,"method":"server/discover"}]`,
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeInvalidRequestError, wantIDOmitted: true,
		},
		{
			name: "wrong jsonrpc", body: `{"jsonrpc":"1.0","id":1,"method":"server/discover","params":{"_meta":` + defaultMeta + `}}`,
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeInvalidRequestError, wantIDOmitted: true,
		},
		{
			name: "missing method", body: `{"jsonrpc":"2.0","id":1,"params":{"_meta":` + defaultMeta + `}}`,
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeInvalidRequestError, wantIDOmitted: true,
		},
		{
			name: "id explicitly null", body: `{"jsonrpc":"2.0","id":null,"method":"server/discover","params":{"_meta":` + defaultMeta + `}}`,
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeInvalidRequestError, wantIDOmitted: true,
		},
		{
			name: "a client-sent response", body: `{"jsonrpc":"2.0","id":1,"result":{}}`,
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeInvalidRequestError, wantIDOmitted: true,
		},
		// schema.json's RequestId admits only string or integer; every one
		// of these carries a type or shape it does not, and must be
		// rejected the same way an explicit null already is, rather than
		// accepted and echoed as arbitrary client-controlled JSON.
		{
			name: "id is a boolean", body: `{"jsonrpc":"2.0","id":true,"method":"server/discover","params":{"_meta":` + defaultMeta + `}}`,
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeInvalidRequestError, wantIDOmitted: true,
		},
		{
			name: "id is an array", body: `{"jsonrpc":"2.0","id":[1],"method":"server/discover","params":{"_meta":` + defaultMeta + `}}`,
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeInvalidRequestError, wantIDOmitted: true,
		},
		{
			name: "id is an object", body: `{"jsonrpc":"2.0","id":{"a":1},"method":"server/discover","params":{"_meta":` + defaultMeta + `}}`,
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeInvalidRequestError, wantIDOmitted: true,
		},
		{
			name: "id is a fractional number", body: `{"jsonrpc":"2.0","id":1.5,"method":"server/discover","params":{"_meta":` + defaultMeta + `}}`,
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeInvalidRequestError, wantIDOmitted: true,
		},
		{
			name: "id is an exponential number", body: `{"jsonrpc":"2.0","id":1e3,"method":"server/discover","params":{"_meta":` + defaultMeta + `}}`,
			headers: stdHeaders(MethodDiscover, ""), wantStatus: http.StatusBadRequest,
			wantCode: CodeInvalidRequestError, wantIDOmitted: true,
		},
		{
			name: "missing Accept", body: discoverRequest("1"),
			headers:    withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{"Accept": ""}),
			wantStatus: http.StatusBadRequest, wantCode: CodeInvalidRequestError,
		},
		{
			name: "Accept missing text/event-stream", body: discoverRequest("1"),
			headers:    withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{"Accept": "application/json"}),
			wantStatus: http.StatusBadRequest, wantCode: CodeInvalidRequestError,
		},
		{
			name: "wrong Content-Type", body: discoverRequest("1"),
			headers:    withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{"Content-Type": "text/plain"}),
			wantStatus: http.StatusBadRequest, wantCode: CodeInvalidRequestError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := do(handler, tc.body, tc.headers)
			require.Equal(t, tc.wantStatus, rec.Code)
			got := decodeError(t, rec)
			require.Equal(t, tc.wantCode, got.Code)
			if tc.wantIDOmitted {
				require.NotContains(t, rec.Body.String(), `"id"`,
					"schema.json's RequestId admits no null variant; an id that cannot be "+
						"attributed to this request must be an absent member, never a JSON null")
			}
		})
	}
}

func TestHeaderAndMetaMatrix(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	tests := []struct {
		name     string
		headers  map[string]string
		wantCode int
	}{
		{
			name:     "missing MCP-Protocol-Version",
			headers:  withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{headerProtocolVersion: ""}),
			wantCode: CodeHeaderMismatchError,
		},
		{
			name:     "missing Mcp-Method",
			headers:  withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{headerMethod: ""}),
			wantCode: CodeHeaderMismatchError,
		},
		{
			name:     "Mcp-Method disagrees with body",
			headers:  withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{headerMethod: "tools/list"}),
			wantCode: CodeHeaderMismatchError,
		},
		{
			name:     "protocol version header disagrees with body",
			headers:  withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{headerProtocolVersion: "2025-11-25"}),
			wantCode: CodeHeaderMismatchError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := do(handler, discoverRequest("1"), tc.headers)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			got := decodeError(t, rec)
			require.Equal(t, tc.wantCode, got.Code)
		})
	}
}

func TestUnsupportedProtocolVersion(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":` +
		`{"io.modelcontextprotocol/protocolVersion":"1900-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`
	headers := withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{headerProtocolVersion: "1900-01-01"})
	rec := do(handler, body, headers)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	got := decodeError(t, rec)
	require.Equal(t, CodeUnsupportedProtocolVersionError, got.Code)

	var data struct {
		Supported []string `json:"supported"`
		Requested string   `json:"requested"`
	}
	raw, err := json.Marshal(got.Data)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &data))
	require.Equal(t, supportedVersions, data.Supported)
	require.Equal(t, "1900-01-01", data.Requested)
}

func TestMissingRequiredMetaFields(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`
	rec := do(handler, body, stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	got := decodeError(t, rec)
	require.Equal(t, CodeInvalidParamsError, got.Code)
}

func TestClientInfoMissingIsWarningNotFailure(t *testing.T) {
	t.Parallel()
	ring := journal.NewRing(10, 1<<20)
	handler := newHandler(t, goldenScenario, ring)

	rec := do(handler, discoverRequest("1"), stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusOK, rec.Code)

	entries := ring.Snapshot()
	require.Len(t, entries, 1)
	require.True(t, hasFinding(entries[0], CodeMetaClientInfoMissing))
}

func TestMcpNameHeaderMismatch(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, callRequest("1", "search", ""), stdHeaders(MethodToolsCall, "other-tool"))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	got := decodeError(t, rec)
	require.Equal(t, CodeHeaderMismatchError, got.Code)
}

func TestMcpNameUnexpectedIsWarning(t *testing.T) {
	t.Parallel()
	ring := journal.NewRing(10, 1<<20)
	handler := newHandler(t, goldenScenario, ring)

	rec := do(handler, discoverRequest("1"), withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{headerName: "search"}))
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, hasFinding(ring.Snapshot()[0], CodeHeaderNameUnexpected))
}

// -----------------------------------------------------------------------
// notifications
// -----------------------------------------------------------------------

func TestNotificationIs202NoBody(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, `{"jsonrpc":"2.0","method":"notifications/whatever"}`, map[string]string{
		"Content-Type": "application/json",
	})
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Empty(t, rec.Body.Bytes())
}

func TestMalformedNotificationFollowsShapeRules(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, `{"jsonrpc":"1.0","method":"notifications/whatever"}`, map[string]string{
		"Content-Type": "application/json",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	got := decodeError(t, rec)
	require.Equal(t, CodeInvalidRequestError, got.Code)
}

// -----------------------------------------------------------------------
// legacy traffic (decision 12)
// -----------------------------------------------------------------------

func TestLegacySessionAndLastEventIDAreWarningsOnly(t *testing.T) {
	t.Parallel()
	ring := journal.NewRing(10, 1<<20)
	handler := newHandler(t, goldenScenario, ring)

	rec := do(handler, discoverRequest("1"), withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{
		"Mcp-Session-Id": "sess-1", "Last-Event-ID": "5",
	}))
	require.Equal(t, http.StatusOK, rec.Code)

	entry := ring.Snapshot()[0]
	require.True(t, hasFinding(entry, CodeLegacySessionID))
	require.True(t, hasFinding(entry, CodeLegacyLastEventID))

	// "Never minted or echoed" (decision 12) means the RESPONSE, not just
	// the request headers, must carry no Mcp-Session-Id — a client-sent
	// one is observed and warned about, never reflected back.
	require.Empty(t, rec.Header().Get("Mcp-Session-Id"))
}

func TestLegacyInitializeWithoutHeaders(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, map[string]string{
		"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	got := decodeError(t, rec)
	require.Equal(t, CodeHeaderMismatchError, got.Code)
	require.Contains(t, got.Message, "2026-07-28")
}

func TestModernInitializeIsMethodNotFoundNamingVersions(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"_meta":` + defaultMeta + `}}`
	rec := do(handler, body, stdHeaders("initialize", ""))
	require.Equal(t, http.StatusNotFound, rec.Code)
	got := decodeError(t, rec)
	require.Equal(t, CodeMethodNotFoundError, got.Code)
	require.Contains(t, got.Message, "2026-07-28")
}

// -----------------------------------------------------------------------
// routing: unknown method, unknown path, wrong verb
// -----------------------------------------------------------------------

func TestUnknownMethodIs404(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	body := `{"jsonrpc":"2.0","id":1,"method":"resources/list","params":{"_meta":` + defaultMeta + `}}`
	rec := do(handler, body, stdHeaders("resources/list", ""))
	require.Equal(t, http.StatusNotFound, rec.Code)
	got := decodeError(t, rec)
	require.Equal(t, CodeMethodNotFoundError, got.Code)
}

func TestUnknownPathIs404(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	req := httptest.NewRequest(http.MethodPost, "/nope", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	got := decodeError(t, rec)
	require.Equal(t, CodeMethodNotFoundError, got.Code)
	require.NotContains(t, rec.Body.String(), `"id"`)
}

func TestWrongVerbIs405(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(method, "/mcp", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
			require.Equal(t, "POST", rec.Header().Get("Allow"))
			if method != http.MethodHead {
				got := decodeError(t, rec)
				require.Equal(t, CodeInvalidRequestError, got.Code)
				require.NotContains(t, rec.Body.String(), `"id"`)
			}
		})
	}
}

// -----------------------------------------------------------------------
// auth (decision 3)
// -----------------------------------------------------------------------

func TestAuthOptionalByDefault(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, goldenScenario, nil)

	rec := do(handler, discoverRequest("1"), stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusOK, rec.Code, "no auth block: a credential-free request must still succeed")
}

func TestAuthRequiredMissingIsFixed401Shape(t *testing.T) {
	t.Parallel()
	src := `
version: 1
name: mcp-auth-required
providers:
  mcp:
    auth: {mode: required}
`
	handler := newHandler(t, src, nil)

	rec := do(handler, discoverRequest(`"echo-me"`), stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "", rec.Header().Get("WWW-Authenticate"))

	got := decodeError(t, rec)
	require.Equal(t, CodeInvalidRequestError, got.Code)
	require.Equal(t, "authorization required", got.Message)
	require.NotContains(t, rec.Body.String(), `"id"`, "the request's own id must never be echoed on a 401, "+
		"and schema.json admits no null RequestId, so the member is omitted rather than sent as null")
}

func TestAuthRequiredWithCredentialSucceeds(t *testing.T) {
	t.Parallel()
	src := `
version: 1
name: mcp-auth-required-2
providers:
  mcp:
    auth: {mode: required, expect_key: test-key}
`
	handler := newHandler(t, src, nil)
	rec := do(handler, discoverRequest("1"), withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{"Authorization": bearer}))
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestAuthPolicyMatrix pins every branch of checkAuth: a wrong key under
// "required" must still be rejected (an attempt disabling the expect_key
// comparison would let it through silently, which the credential-rotation
// built-in depends on this profile never doing), "reject" rejects any
// credential at all including a correct-looking one, and a non-Bearer
// scheme under "optional" still authenticates with only a WARNING.
func TestAuthPolicyMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		authBlock  string
		headers    map[string]string
		wantStatus int
		wantWarn   string // CodeAuthWrongPlacement when non-empty; checked via the journal.
	}{
		{
			name:       "required, missing credential",
			authBlock:  "auth: {mode: required}",
			headers:    nil,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "required, wrong key",
			authBlock:  "auth: {mode: required, expect_key: right-key}",
			headers:    map[string]string{"Authorization": "Bearer wrong-key"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "required, right key",
			authBlock:  "auth: {mode: required, expect_key: right-key}",
			headers:    map[string]string{"Authorization": "Bearer right-key"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "reject, any credential",
			authBlock:  "auth: {mode: reject}",
			headers:    map[string]string{"Authorization": "Bearer anything"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "reject, no credential",
			authBlock:  "auth: {mode: reject}",
			headers:    nil,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "optional, non-Bearer scheme still authenticates",
			authBlock:  "auth: {mode: optional}",
			headers:    map[string]string{"Authorization": "Basic dXNlcjpwYXNz"},
			wantStatus: http.StatusOK,
			wantWarn:   CodeAuthWrongPlacement,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := "version: 1\nname: mcp-auth-matrix\nproviders:\n  mcp:\n    " + tc.authBlock + "\n"
			ring := journal.NewRing(10, 1<<20)
			handler := newHandler(t, src, ring)

			rec := do(handler, discoverRequest("1"), withHeaders(stdHeaders(MethodDiscover, ""), tc.headers))
			require.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantWarn != "" {
				require.True(t, hasFinding(ring.Snapshot()[0], tc.wantWarn))
			}
		})
	}
}

// -----------------------------------------------------------------------
// redaction (house rule 4)
// -----------------------------------------------------------------------

func TestCredentialNeverReachesJournal(t *testing.T) {
	t.Parallel()
	ring := journal.NewRing(10, 1<<20)
	handler := newHandler(t, goldenScenario, ring)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},` +
		`"io.modelcontextprotocol/clientInfo":{"name":"c","version":"1","secret":"tok-in-meta"}},` +
		`"name":"search","arguments":{"api_key":"tok-in-args"}}}`
	headers := withHeaders(stdHeaders(MethodToolsCall, "search"), map[string]string{"Authorization": "Bearer tok-in-header"})

	rec := do(handler, body, headers)
	require.Equal(t, http.StatusOK, rec.Code)

	raw, err := json.Marshal(ring.Snapshot()[0])
	require.NoError(t, err)
	blob := string(raw)
	require.NotContains(t, blob, "tok-in-header")
	require.NotContains(t, blob, "tok-in-args")
	require.NotContains(t, blob, "tok-in-meta")
}

// TestCredentialNeverReachesResponseBody proves house rule 4's "in error
// messages" clause on the HTTP response itself, not merely on the
// journal: nothing sent as a credential — a Bearer token or an
// arguments.api_key value — may appear in the BODY of a -32020 header
// error, a -32602 missing-_meta error, a 401, or the unknown-tool -32602,
// even though several of those build their message by quoting parts of
// the request back at the client.
func TestCredentialNeverReachesResponseBody(t *testing.T) {
	t.Parallel()

	const secret = "tok-in-header-ZQ4NUXW7PL"

	tests := []struct {
		name    string
		body    string
		headers map[string]string
	}{
		{
			name: "-32020 required header missing, credential in Authorization",
			body: discoverRequest("1"),
			headers: withHeaders(
				withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{headerProtocolVersion: ""}),
				map[string]string{"Authorization": "Bearer " + secret},
			),
		},
		{
			name: "-32020 Mcp-Name mismatch, credential in Authorization",
			body: callRequest("1", "search", ""),
			headers: withHeaders(stdHeaders(MethodToolsCall, "other-tool"),
				map[string]string{"Authorization": "Bearer " + secret}),
		},
		{
			name: "-32602 missing _meta, credential in Authorization",
			body: `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`,
			headers: withHeaders(stdHeaders(MethodDiscover, ""),
				map[string]string{"Authorization": "Bearer " + secret}),
		},
		{
			name:    "unknown tool, credential in arguments",
			body:    callRequest("1", "nonexistent", `{"api_key":"`+secret+`"}`),
			headers: stdHeaders(MethodToolsCall, "nonexistent"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler := newHandler(t, goldenScenario, nil)
			rec := do(handler, tc.body, tc.headers)
			require.NotContains(t, rec.Body.String(), secret,
				"a credential must never reach the HTTP response body")
		})
	}

	t.Run("401 authorization required, wrong credential", func(t *testing.T) {
		t.Parallel()
		src := `
version: 1
name: mcp-401-credential
providers:
  mcp:
    auth: {mode: required, expect_key: right-key}
`
		handler := newHandler(t, src, nil)
		rec := do(handler, discoverRequest("1"),
			withHeaders(stdHeaders(MethodDiscover, ""), map[string]string{"Authorization": "Bearer " + secret}))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.NotContains(t, rec.Body.String(), secret)
	})
}

// -----------------------------------------------------------------------
// zero configuration
// -----------------------------------------------------------------------

func TestZeroDepsIsUsable(t *testing.T) {
	t.Parallel()
	handler := Profile().Handler(provider.Deps{})

	rec := do(handler, discoverRequest("1"), stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusOK, rec.Code)
	var got discoverResult
	decodeResult(t, rec, &got)
	require.Equal(t, "complete", got.ResultType)

	rec2 := do(handler, listRequest("1", ""), stdHeaders(MethodToolsList, ""))
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Contains(t, rec2.Body.String(), `"tools":[]`)

	rec3 := do(handler, callRequest("1", "anything", ""), stdHeaders(MethodToolsCall, "anything"))
	require.Equal(t, http.StatusOK, rec3.Code)
	got3 := decodeError(t, rec3)
	require.Equal(t, CodeInvalidParamsError, got3.Code)
}

// -----------------------------------------------------------------------
// turn_key / when.body_json dispatch
// -----------------------------------------------------------------------

func TestBodyJSONDispatchPerMethod(t *testing.T) {
	t.Parallel()
	src := `
version: 1
name: mcp-turns
providers:
  mcp:
    turn_key: [route, "body_json:method"]
    turns:
      - when: {body_json: {method: server/discover}}
        respond: {instructions: "discover turn"}
      - when: {body_json: {method: tools/list}}
        respond: {tools: [{name: only-here, input_schema: {type: object}}]}
`
	handler := newHandler(t, src, nil)

	rec := do(handler, discoverRequest("1"), stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusOK, rec.Code)
	var d discoverResult
	decodeResult(t, rec, &d)
	require.Equal(t, "discover turn", d.Instructions)

	rec2 := do(handler, listRequest("1", ""), stdHeaders(MethodToolsList, ""))
	require.Equal(t, http.StatusOK, rec2.Code)
	var l listToolsResult
	decodeResult(t, rec2, &l)
	require.Len(t, l.Tools, 1)
	require.Equal(t, "only-here", l.Tools[0].Name)
}

// -----------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------

func hasFinding(e journal.Entry, code string) bool {
	for _, f := range e.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
