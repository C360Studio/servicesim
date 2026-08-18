package examples

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

// mcpclient.go is a minimal Model Context Protocol client over the
// Streamable HTTP transport, the way a consuming team's own adapter would
// write one: no Servicesim import anywhere in this file — imports_test.go
// enforces it — because production code has no dependency on the simulator
// it is tested against. Every header, every _meta field and every response
// shape below is read from contracts/mcp/README.md, never from memory; the
// section names in the comments below name where in that file to look.
//
// It speaks protocol revision 2026-07-28 only — the one revision
// profiles/mcp simulates (contracts/mcp/README.md "Protocol eras") — and
// implements exactly the three methods that profile serves:
// server/discover, tools/list and tools/call. It is deliberately small: no
// retries, no backoff, no pagination beyond a bare tools/list call, no
// resources or prompts. Copy its shape, not its feature set.

// MCPProtocolVersion is the revision this client speaks, sent as both the
// MCP-Protocol-Version header and _meta's protocolVersion on every request
// (contracts/mcp/README.md "Request-metadata headers").
const MCPProtocolVersion = "2026-07-28"

// Standard Streamable HTTP request-metadata headers this client sends
// (contracts/mcp/README.md "Request-metadata headers"). Header names are
// case-insensitive on the wire; these are spelled exactly as the
// specification spells them.
const (
	mcpHeaderProtocolVersion = "MCP-Protocol-Version"
	mcpHeaderMethod          = "Mcp-Method"
	mcpHeaderName            = "Mcp-Name"
)

// Reserved _meta keys this client sends or reads (contracts/mcp/README.md
// "_meta — key naming rules and reserved keys").
const (
	mcpMetaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	mcpMetaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	mcpMetaClientInfo         = "io.modelcontextprotocol/clientInfo"
	mcpMetaProgressToken      = "progressToken"
)

// JSON-RPC methods this client calls (contracts/mcp/README.md "Methods the
// profile will simulate").
const (
	mcpMethodDiscover      = "server/discover"
	mcpMethodToolsList     = "tools/list"
	mcpMethodToolsCall     = "tools/call"
	mcpMethodNotifProgress = "notifications/progress"
)

// The Base64 sentinel markers a header-unsafe Mcp-Name value is wrapped in
// (contracts/mcp/README.md "Value encoding — the Base64 sentinel").
const (
	mcpSentinelPrefix = "=?base64?"
	mcpSentinelSuffix = "?="
)

// MCPClient is a Streamable HTTP MCP client bound to one server's endpoint.
type MCPClient struct {
	baseURL       string
	httpClient    *http.Client
	bearerToken   string
	clientName    string
	clientVersion string
	nextID        atomic.Int64
}

// MCPClientOption configures a [MCPClient].
type MCPClientOption func(*MCPClient)

// WithMCPHTTPClient sets the HTTP client a [MCPClient] sends requests
// through. A test should pass sim.Client(), whose keep-alives are disabled
// so a connection-abort fault is observed rather than absorbed by a pooled
// connection. The default is http.DefaultClient.
func WithMCPHTTPClient(hc *http.Client) MCPClientOption {
	return func(c *MCPClient) { c.httpClient = hc }
}

// WithMCPBearerToken sets the credential presented as
// Authorization: Bearer <token> on every request. Authorization is the one
// credential placement the specification documents
// (contracts/mcp/README.md "Origin and authentication"), and it is the
// only one this profile's default policy accepts
// (contracts/mcp/doc.go decision 3) — a scenario opts into requiring it,
// it is never required by default.
func WithMCPBearerToken(token string) MCPClientOption {
	return func(c *MCPClient) { c.bearerToken = token }
}

// NewMCPClient returns a client bound to baseURL, the value
// sim.BaseURLs()["MCP_BASE_URL"] provides — already carrying a namespace
// prefix when it came from a [*testkit.Namespace] instead of a whole Sim.
// The endpoint path, /mcp, is appended by every call; baseURL names only
// the host and any namespace prefix.
func NewMCPClient(baseURL string, opts ...MCPClientOption) *MCPClient {
	c := &MCPClient{
		baseURL:       strings.TrimSuffix(baseURL, "/"),
		httpClient:    http.DefaultClient,
		clientName:    "servicesim-examples-mcpclient",
		clientVersion: "1.0.0",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// MCPImplementation is the schema's Implementation shape, used for both a
// request's clientInfo and a result's serverInfo.
type MCPImplementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// MCPDiscoverResult is DiscoverResult (contracts/mcp/README.md
// "server/discover"). Capabilities is left as a raw JSON object: this
// client never inspects it beyond passing it through, which is the
// specification's own advice for clientInfo/serverInfo — "SHOULD NOT rely
// on them for security decisions" applies just as well to a value a client
// merely reads and forwards.
type MCPDiscoverResult struct {
	ResultType        string          `json:"resultType"`
	SupportedVersions []string        `json:"supportedVersions"`
	Capabilities      json.RawMessage `json:"capabilities"`
	Instructions      string          `json:"instructions,omitempty"`
	TTLMs             int64           `json:"ttlMs"`
	CacheScope        string          `json:"cacheScope"`
}

// MCPTool is one tools/list entry — the fields this client reads, on the
// same "declare what you consume" terms as adapter.go's response types.
type MCPTool struct {
	Name         string         `json:"name"`
	Title        string         `json:"title,omitempty"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
}

// MCPListToolsResult is ListToolsResult (contracts/mcp/README.md
// "tools/list"). NextCursor is read but this client never sends a cursor
// of its own: the profile it is written against always serves a single
// page (contracts/mcp/doc.go decision 8), and pagination is out of scope
// for a client meant to be small and readable.
type MCPListToolsResult struct {
	ResultType string    `json:"resultType"`
	Tools      []MCPTool `json:"tools"`
	NextCursor string    `json:"nextCursor,omitempty"`
	TTLMs      int64     `json:"ttlMs"`
	CacheScope string    `json:"cacheScope"`
}

// MCPResourceContents is TextResourceContents / BlobResourceContents,
// merged: exactly one of Text or Blob is set on a well-formed response.
type MCPResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// MCPContentBlock is one entry of a CallToolResult's content array
// (contracts/mcp/README.md "tools/call", the ContentBlock table). One
// struct for all five wire shapes, on the same terms profiles/mcp's own
// wireContent uses server-side: Type selects which fields are meaningful.
type MCPContentBlock struct {
	Type string `json:"type"`

	// Text serves type: text.
	Text string `json:"text,omitempty"`

	// Data and MimeType serve type: image and type: audio.
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`

	// URI, Name, Title and Description serve type: resource_link.
	// MimeType above doubles for its mimeType too.
	URI         string `json:"uri,omitempty"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`

	// Resource serves type: resource.
	Resource *MCPResourceContents `json:"resource,omitempty"`
}

// MCPCallToolResult is CallToolResult (contracts/mcp/README.md
// "tools/call"). StructuredContent is left as raw JSON: "any JSON value
// (object, array, string, number, boolean, or null)" per the
// specification, which no single Go type represents without also losing
// the caller's ability to tell "absent" from "null".
type MCPCallToolResult struct {
	ResultType        string            `json:"resultType"`
	Content           []MCPContentBlock `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           bool              `json:"isError,omitempty"`
}

// MCPProgress is one notifications/progress frame observed on a streamed
// tools/call answer (contracts/mcp/README.md "notifications/progress").
type MCPProgress struct {
	Progress int
	Total    int
	Message  string
}

// MCPCallToolResponse is CallTool's return value: the final result, plus
// every notifications/progress frame observed ahead of it on the SSE
// stream. Progress is empty for a JSON-object answer, and empty for an SSE
// answer too when the call carried no progress token
// (contracts/mcp/doc.go decision 5: the server never emits a progress
// frame for a request that did not ask for one).
type MCPCallToolResponse struct {
	Result   *MCPCallToolResult
	Progress []MCPProgress
}

// mcpCallOptions is the resolved configuration of one CallTool call.
type mcpCallOptions struct {
	progressToken string
}

// MCPCallOption tunes one [MCPClient.CallTool] call.
type MCPCallOption func(*mcpCallOptions)

// WithMCPProgressToken opts a tools/call request into progress
// notifications by setting _meta.progressToken. The profile this client is
// written against streams only when the entry's own script asks for it
// (contracts/mcp/doc.go decision 5: the server decides unilaterally
// whether to stream at all), but progress FRAMES on that stream are gated
// by this token regardless: no token, no frames, only the final response.
func WithMCPProgressToken(token string) MCPCallOption {
	return func(o *mcpCallOptions) { o.progressToken = token }
}

// MCPError is a JSON-RPC error mapped onto a typed Go error, carrying the
// three fields a caller needs to act on it: the HTTP status (what a
// consumer's retry logic actually keys on — contracts/mcp/README.md's
// status table has 429/503 arriving as an ordinary 200-shaped method error
// would never be retried, while the transport-level status is), the
// JSON-RPC code and message, and any data payload the error carried (for
// example -32022's supported/requested versions).
type MCPError struct {
	StatusCode int
	Code       int
	Message    string
	Data       json.RawMessage
}

// Error implements error.
func (e *MCPError) Error() string {
	return fmt.Sprintf("mcp: HTTP %d: JSON-RPC %d: %s", e.StatusCode, e.Code, e.Message)
}

// Retryable reports whether the same call is worth sending again. It
// classifies by HTTP status, not by JSON-RPC code: contracts/mcp/README.md
// records that a scripted 429 or 503 arrives as the same fixed -32603
// envelope an unrelated internal error would, so the code alone cannot
// tell a transient fault from a permanent one — only the status can.
func (e *MCPError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= http.StatusInternalServerError
}

// mcpRPCError is the JSON-RPC error object's wire shape.
type mcpRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// mcpEnvelope is the JSON-RPC response envelope, decoded field by field so
// the same type reads a plain JSON-object answer and one SSE frame alike:
// a frame carrying "method" is a notifications/progress notification, and
// one carrying "result" or "error" is the final response.
type mcpEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

// mcpProgressParams is notifications/progress's params object.
type mcpProgressParams struct {
	Progress int    `json:"progress"`
	Total    int    `json:"total"`
	Message  string `json:"message"`
}

// Discover calls server/discover.
func (c *MCPClient) Discover(ctx context.Context) (*MCPDiscoverResult, error) {
	raw, _, err := c.call(ctx, mcpMethodDiscover, "", nil, "")
	if err != nil {
		return nil, err
	}
	var out MCPDiscoverResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp: decoding server/discover result: %w", err)
	}
	return &out, nil
}

// ListTools calls tools/list. It sends no cursor: the profile this client
// is written against always serves a single page.
func (c *MCPClient) ListTools(ctx context.Context) (*MCPListToolsResult, error) {
	raw, _, err := c.call(ctx, mcpMethodToolsList, "", nil, "")
	if err != nil {
		return nil, err
	}
	var out MCPListToolsResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp: decoding tools/list result: %w", err)
	}
	return &out, nil
}

// CallTool calls tools/call. The answer may arrive as a plain JSON object
// or, when the server's own script decides to stream, as an SSE response
// carrying zero or more notifications/progress frames — gated by
// [WithMCPProgressToken] — followed by the final response frame; either
// way CallTool returns the same [MCPCallToolResponse] shape.
func (c *MCPClient) CallTool(
	ctx context.Context, name string, arguments map[string]any, opts ...MCPCallOption,
) (*MCPCallToolResponse, error) {
	var o mcpCallOptions
	for _, opt := range opts {
		opt(&o)
	}

	params := map[string]any{"name": name}
	if arguments != nil {
		params["arguments"] = arguments
	}

	raw, progress, err := c.call(ctx, mcpMethodToolsCall, name, params, o.progressToken)
	if err != nil {
		return nil, err
	}
	var result MCPCallToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp: decoding tools/call result: %w", err)
	}
	return &MCPCallToolResponse{Result: &result, Progress: progress}, nil
}

// call sends one JSON-RPC request and returns its result, decoding either
// answer shape. toolName is used for the Mcp-Name header and is empty for
// server/discover and tools/list, which do not carry one
// (contracts/mcp/README.md "Request-metadata headers"). progressToken is
// empty unless the caller opted in through [WithMCPProgressToken].
func (c *MCPClient) call(
	ctx context.Context, method, toolName string, params map[string]any, progressToken string,
) (json.RawMessage, []MCPProgress, error) {
	meta := map[string]any{
		mcpMetaProtocolVersion:    MCPProtocolVersion,
		mcpMetaClientCapabilities: map[string]any{},
		mcpMetaClientInfo:         MCPImplementation{Name: c.clientName, Version: c.clientVersion},
	}
	if progressToken != "" {
		meta[mcpMetaProgressToken] = progressToken
	}

	body := map[string]any{"_meta": meta}
	for k, v := range params {
		body[k] = v
	}

	id := c.nextID.Add(1)
	encoded, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  body,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("mcp: encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/mcp", bytes.NewReader(encoded))
	if err != nil {
		return nil, nil, fmt.Errorf("mcp: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(mcpHeaderProtocolVersion, MCPProtocolVersion)
	req.Header.Set(mcpHeaderMethod, method)
	if toolName != "" {
		req.Header.Set(mcpHeaderName, mcpEncodeHeaderValue(toolName))
	}
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return decodeMCPStream(resp.Body, resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp: reading response: %w", err)
	}
	var env mcpEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil, fmt.Errorf("mcp: the response is not JSON: %w", err)
	}
	result, err := mcpResultOrError(env, resp.StatusCode)
	return result, nil, err
}

// mcpResultOrError extracts a decoded envelope's result, or builds the
// typed [MCPError] its error member describes.
func mcpResultOrError(env mcpEnvelope, status int) (json.RawMessage, error) {
	if env.Error != nil {
		return nil, &MCPError{StatusCode: status, Code: env.Error.Code, Message: env.Error.Message, Data: env.Error.Data}
	}
	if env.Result == nil {
		return nil, &MCPError{StatusCode: status, Message: "response carried neither result nor error"}
	}
	return env.Result, nil
}

// decodeMCPStream reads an SSE response body frame by frame — unnamed
// "data:" lines, one or more per frame, a blank line closing each
// (contracts/mcp/README.md "SSE response streams"; profiles/mcp/doc.go
// decision 5's framing choice) — classifying each decoded frame as a
// notifications/progress notification or the final response, and
// returning as soon as the final one arrives.
func decodeMCPStream(body io.Reader, status int) (json.RawMessage, []MCPProgress, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var progress []MCPProgress
	var dataLines []string

	frame := func(raw []byte) (json.RawMessage, bool, error) {
		var env mcpEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, true, fmt.Errorf("mcp: decoding SSE frame: %w", err)
		}
		if env.Method == mcpMethodNotifProgress {
			var p mcpProgressParams
			_ = json.Unmarshal(env.Params, &p) // best-effort: a malformed progress frame is dropped, not fatal
			progress = append(progress, MCPProgress{Progress: p.Progress, Total: p.Total, Message: p.Message})
			return nil, false, nil
		}
		result, err := mcpResultOrError(env, status)
		return result, true, err
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimPrefix(after, " "))
			continue
		}
		if line != "" || len(dataLines) == 0 {
			continue // an "event:" line (this profile sends none) or a blank line between frames
		}
		raw := []byte(strings.Join(dataLines, "\n"))
		dataLines = nil
		if result, final, err := frame(raw); final {
			return result, progress, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, progress, fmt.Errorf("mcp: reading SSE stream: %w", err)
	}
	if len(dataLines) > 0 {
		if result, final, err := frame([]byte(strings.Join(dataLines, "\n"))); final {
			return result, progress, err
		}
	}
	return nil, progress, fmt.Errorf("mcp: SSE stream ended with no final response frame")
}

// mcpEncodeHeaderValue applies the specification's Value Encoding rule to
// a header-bound string (contracts/mcp/README.md "Value encoding — the
// Base64 sentinel"): used as-is when it is a safe plain-ASCII header value
// and does not itself look sentinel-wrapped; Base64-encoded inside the
// sentinel markers otherwise, which also covers a plain value that would
// be ambiguous with the sentinel pattern.
func mcpEncodeHeaderValue(v string) string {
	if mcpHeaderSafeASCII(v) && !strings.HasPrefix(v, mcpSentinelPrefix) {
		return v
	}
	return mcpSentinelPrefix + base64.StdEncoding.EncodeToString([]byte(v)) + mcpSentinelSuffix
}

// mcpHeaderSafeASCII reports whether v can travel as a plain HTTP header
// value with no encoding: printable ASCII, no leading or trailing
// whitespace, no control characters.
func mcpHeaderSafeASCII(v string) bool {
	if v == "" || v != strings.TrimSpace(v) {
		return false
	}
	for _, r := range v {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}
