package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/contracts"
	"github.com/c360studio/servicesim/internal/faults"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// mcpGoldenBytes reads a golden fixture from contracts/mcp, failing t if
// it cannot be read.
func mcpGoldenBytes(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := contracts.Read(contracts.MCP, name)
	require.NoError(t, err)
	return raw
}

// streamScenario is a tools/call entry scripted to stream (decision 5).
const streamScenario = `
version: 1
name: mcp-stream
providers:
  mcp:
    stream:
      when_requested: stream
      deltas: ["searching…", "ranking…"]
    tools:
      - name: search
        input_schema: {type: object}
    results:
      search:
        content: [{type: text, text: "final answer"}]
`

// sseSim is a running MCP listener over a real fault engine, for the tests
// that need an actual streamed HTTP response — httptest.NewRecorder does
// not support the flush/hijack semantics executeStream depends on.
type sseSim struct {
	server  *httptest.Server
	journal *journal.Ring
}

func newSSESim(t *testing.T, src string) *sseSim {
	t.Helper()
	loaded, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)
	require.Empty(t, provider.ValidateScenario(loaded, map[string]provider.Validator{string(Name): Validator{}}))

	ring := journal.NewRing(64, 1<<16)
	srv := httptest.NewServer(New(provider.Deps{
		Scenario: loaded, Journal: ring, Faults: faults.New(loaded, Routes()),
	}))
	t.Cleanup(srv.Close)
	return &sseSim{server: srv, journal: ring}
}

func (s *sseSim) do(t *testing.T, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.server.URL+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.server.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, out
}

// doRaw posts body and returns whatever bytes arrived before a transport
// error, along with that error. Unlike do it does not fail the test on a
// read error, because a disconnect test EXPECTS one: io.ReadAll returns the
// bytes it already read alongside the error, which is exactly the
// client-observed transcript these tests pin.
func (s *sseSim) doRaw(t *testing.T, body string, headers map[string]string) (*http.Response, []byte, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.server.URL+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.server.Client().Do(req)
	require.NoError(t, err, "headers must still arrive")
	defer func() { _ = resp.Body.Close() }()
	read, readErr := io.ReadAll(resp.Body)
	return resp, read, readErr
}

// frames splits a raw SSE transcript into its "data:" line values, one per
// frame, dropping the blank separator lines.
func frames(transcript []byte) []string {
	var out []string
	for _, block := range strings.Split(strings.TrimRight(string(transcript), "\n"), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var lines []string
		for _, line := range strings.Split(block, "\n") {
			lines = append(lines, strings.TrimPrefix(line, "data: "))
		}
		out = append(out, strings.Join(lines, "\n"))
	}
	return out
}

func TestToolsCallStreamWithProgressToken(t *testing.T) {
	t.Parallel()
	sim := newSSESim(t, streamScenario)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":` +
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},` +
		`"progressToken":"tok-1"},"name":"search"}}`
	resp, transcript := sim.do(t, body, stdHeaders(MethodToolsCall, "search"))

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	require.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	require.Equal(t, "no", resp.Header.Get("X-Accel-Buffering"))
	require.NotContains(t, string(transcript), "event:")
	require.NotContains(t, string(transcript), "[DONE]")

	fs := frames(transcript)
	require.Len(t, fs, 3, "two progress frames plus the final response")
	require.Contains(t, fs[0], `"method":"notifications/progress"`)
	require.Contains(t, fs[0], `"progressToken":"tok-1"`)
	require.Contains(t, fs[0], `"progress":1`)
	require.Contains(t, fs[0], `"total":2`)
	require.Contains(t, fs[0], `"message":"searching…"`)
	require.Contains(t, fs[1], `"progress":2`)
	require.Contains(t, fs[1], `"message":"ranking…"`)
	require.Contains(t, fs[2], `"result"`)
	require.Contains(t, fs[2], `"final answer"`)
	require.Contains(t, fs[2], `"id":1`)
}

func TestToolsCallStreamWithoutProgressTokenCarriesOnlyTheResponse(t *testing.T) {
	t.Parallel()
	sim := newSSESim(t, streamScenario)

	resp, transcript := sim.do(t, callRequest("1", "search", ""), stdHeaders(MethodToolsCall, "search"))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	fs := frames(transcript)
	require.Len(t, fs, 1)
	require.Contains(t, fs[0], `"result"`)
}

func TestToolsCallStreamIntegerProgressTokenEchoedExactly(t *testing.T) {
	t.Parallel()
	sim := newSSESim(t, streamScenario)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":` +
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},` +
		`"progressToken":42},"name":"search"}}`
	_, transcript := sim.do(t, body, stdHeaders(MethodToolsCall, "search"))

	fs := frames(transcript)
	require.Contains(t, fs[0], `"progressToken":42`)
}

func TestToolsListNeverStreamsEvenWhenEntryPolicyIsStream(t *testing.T) {
	t.Parallel()
	sim := newSSESim(t, streamScenario)

	resp, transcript := sim.do(t, listRequest("1", ""), stdHeaders(MethodToolsList, ""))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.NotContains(t, string(transcript), "data:")
}

func TestToolsCallStreamJournalRecordsPlannedAndAmends(t *testing.T) {
	t.Parallel()
	sim := newSSESim(t, streamScenario)

	_, _ = sim.do(t, callRequest("1", "search", ""), stdHeaders(MethodToolsCall, "search"))

	entries := sim.journal.Snapshot()
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].Outcome.Stream)
	require.Equal(t, journal.StreamCompleted, entries[0].Outcome.Stream.State)
	require.Equal(t, 1, entries[0].Outcome.Stream.ChunksSent, "no progressToken: only the terminal frame is sent")
}

// TestToolsCallStreamDisconnectFault proves a stream_disconnect fault
// applies to the SSE path exactly as it does on every other provider: the
// connection dies before the scripted chunk, and the journal amends to
// StreamAborted.
func TestToolsCallStreamDisconnectFault(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: mcp-stream-disconnect
providers:
  mcp:
    stream:
      when_requested: stream
      deltas: ["searching…", "ranking…"]
    fault:
      attempts:
        - kind: stream_disconnect
          after_chunk: 1
    tools:
      - name: search
        input_schema: {type: object}
    results:
      search:
        content: [{type: text, text: "final answer"}]
`
	sim := newSSESim(t, src)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":` +
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},` +
		`"progressToken":"tok-1"},"name":"search"}}`
	resp, transcript, readErr := sim.doRaw(t, body, stdHeaders(MethodToolsCall, "search"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Error(t, readErr, "the connection must die before the second progress frame ever reaches the client")

	want := mcpGoldenBytes(t, "mcp-tools-call-stream-disconnect.sse")
	require.Equal(t, string(want), string(transcript))

	fs := frames(transcript)
	require.Len(t, fs, 1, "only the first progress frame is delivered before the disconnect")

	entries := sim.journal.Snapshot()
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].Outcome.Stream)
	require.Equal(t, journal.StreamAborted, entries[0].Outcome.Stream.State)
}

// TestFaultStatusRendersFixedJSONRPCErrorShape drives a non-streaming
// status-only fault through the real engine and pins decision 6's fixed
// error envelope.
func TestFaultStatusRendersFixedJSONRPCErrorShape(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: mcp-fault-503
providers:
  mcp:
    fault:
      attempts:
        - status: 503
`
	sim := newSSESim(t, src)
	resp, body := sim.do(t, discoverRequest("7"), stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, `{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"servicesim scripted fault: status"}}`,
		strings.TrimSpace(string(body)))
}

// TestFaultAttemptErrorOverridesFixedMessage pins the third rule in
// decision 6's fault shape (provider/mcp/doc.go, jsonrpc.go's faultBody):
// an attempt's own error: replaces only the message text, keeping code
// -32603 and the envelope shape — the same scenario.FaultAttempt grammar
// every other provider package already honours, and the reason the
// rate-limited/brownout/server-error built-ins put a human-readable
// message on the wire instead of the generic templated one.
func TestFaultAttemptErrorOverridesFixedMessage(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: mcp-fault-error-override
providers:
  mcp:
    fault:
      attempts:
        - status: 429
          error: Rate limit exceeded.
`
	sim := newSSESim(t, src)
	resp, body := sim.do(t, discoverRequest("3"), stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Equal(t, `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Rate limit exceeded."}}`,
		strings.TrimSpace(string(body)))
}

// TestFaultArbitraryStatusAndBodyPair proves decision 7's "fault supports
// an arbitrary status + body pair" for mcp: an explicit body: override wins
// outright over the synthesised shape.
func TestFaultArbitraryStatusAndBodyPair(t *testing.T) {
	t.Parallel()

	src := `
version: 1
name: mcp-fault-403
providers:
  mcp:
    fault:
      attempts:
        - status: 403
          body: {jsonrpc: "2.0", error: {code: -32000, message: "forbidden by origin policy"}}
`
	sim := newSSESim(t, src)
	resp, body := sim.do(t, discoverRequest("1"), stdHeaders(MethodDiscover, ""))
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, string(body), `"forbidden by origin policy"`)
	require.Contains(t, string(body), `-32000`)
}

// TestStreamPolicyIsReadFromTurnZero pins decision 5's "policy is a
// property of the entry, read from turn 0" on the one shape the load-time
// guard lets through with a WARNING rather than an ERROR: a later turn that
// declares when_requested: stream and no deltas. scenario.ValidateStreamScripts
// answers that with CodeStreamPolicyIgnored ("read from the first turn
// only; this value is ignored") — and an earlier draft of wantsStream then
// read the SELECTED turn's policy anyway and switched that turn's answer to
// SSE, contradicting the warning it had just issued. (Every other divergent
// shape — a later turn with deltas under a non-streaming entry, or a
// streaming entry with a delta-less turn — is a load ERROR and never
// reaches the handler.)
func TestStreamPolicyIsReadFromTurnZero(t *testing.T) {
	t.Parallel()

	const laterTurnDeclaresStream = `
version: 1
name: mcp-later-turn-declares-stream
providers:
  mcp:
    turns:
      - when: {call_index: 0}
        respond:
          tools: [{name: search, input_schema: {type: object}}]
          results: {search: {content: [{type: text, text: "first"}]}}
      - respond:
          stream: {when_requested: stream}
          tools: [{name: search, input_schema: {type: object}}]
          results: {search: {content: [{type: text, text: "second"}]}}
`
	loaded, report, err := scenario.Parse([]byte(laterTurnDeclaresStream))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)
	findings := provider.ValidateScenario(loaded, map[string]provider.Validator{string(Name): Validator{}})
	require.Len(t, findings, 1, "exactly the policy-ignored warning: %v", findings)
	require.Equal(t, scenario.CodeStreamPolicyIgnored, findings[0].Code)
	require.Equal(t, scenario.SeverityWarning, findings[0].Severity)

	ring := journal.NewRing(64, 1<<16)
	srv := httptest.NewServer(New(provider.Deps{
		Scenario: loaded, Journal: ring, Faults: faults.New(loaded, Routes()),
	}))
	t.Cleanup(srv.Close)
	sim := &sseSim{server: srv, journal: ring}

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":` +
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},` +
		`"progressToken":"tok"},"name":"search"}}`
	first, _ := sim.do(t, body, stdHeaders(MethodToolsCall, "search"))
	require.Equal(t, "application/json", first.Header.Get("Content-Type"))
	second, out := sim.do(t, body, stdHeaders(MethodToolsCall, "search"))
	require.Equal(t, "application/json", second.Header.Get("Content-Type"),
		"turn 1 declares when_requested: stream, but the entry policy is turn 0's: still JSON, as the warning said")
	require.Contains(t, string(out), `"second"`)
}
