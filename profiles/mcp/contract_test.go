package mcp

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/c360studio/servicesim/contracts"
)

// This file holds the vendor-specific wire-shape pins that used to live in
// contracts/contracts_test.go's Providers() loops (Phase 10 unit 6: contracts
// genericised over fs.FS, and stopped knowing which vendors exist — a shape
// assertion specific to MCP belongs beside MCP, reading through
// mcpGoldenBytes (stream_test.go), which itself reads through
// contracts.Read(Profile().Contracts, name) the same way an out-of-tree
// profile's own conformance test would). The generic provenance/golden
// discipline these fixtures are also subject to is contracts.Conform.

// TestHasASpecBlock pins the requirement that used to be
// contracts.TestEveryProviderHasSpecRecorded, generalised across all four
// reference providers but recorded once per profile, because
// contracts.Conform itself only checks a spec: block is well-formed WHEN
// present — an out-of-tree profile's vendor may publish no machine-readable
// specification at all, so Conform cannot demand one generically. The
// Model Context Protocol does publish one, a machine-readable schema.json
// per revision, so this profile's own bundle must not silently drop it.
func TestHasASpecBlock(t *testing.T) {
	t.Parallel()

	_, ok, err := contracts.ProviderSpec(Profile().Contracts)
	if err != nil {
		t.Fatalf("ProviderSpec: %v", err)
	}
	if !ok {
		t.Error("no spec: block recorded, want one — the Model Context Protocol publishes a " +
			"machine-readable schema.json per revision")
	}
}

// TestMCPResultsCarryResultTypeAndServerInfo guards decision 9/14's two
// wire-visible constants every MCP success result carries: resultType is
// always "complete" and _meta.io.modelcontextprotocol/serverInfo names this
// build. A result missing either would teach a consumer's tolerance test the
// wrong lesson — that the field is truly optional, when the specification
// MUSTs the first and SHOULDs the second and this profile promises both
// every time (profiles/mcp/doc.go's decisions 9 and 14).
func TestMCPResultsCarryResultTypeAndServerInfo(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"mcp-discover-happy.json", "mcp-tools-list-happy.json", "mcp-tools-list-empty.json",
		"mcp-tools-call-happy.json", "mcp-tools-call-empty.json", "mcp-tools-call-tool-error.json",
		"mcp-tools-call-structured.json",
	} {
		body := decodeContractGolden(t, name)
		result, ok := body["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s: result is %T, want an object", name, body["result"])
		}
		if result["resultType"] != "complete" {
			t.Errorf("%s: resultType is %v, want \"complete\"", name, result["resultType"])
		}
		meta, ok := result["_meta"].(map[string]any)
		if !ok {
			t.Fatalf("%s: result._meta is %T, want an object", name, result["_meta"])
		}
		info, ok := meta["io.modelcontextprotocol/serverInfo"].(map[string]any)
		if !ok {
			t.Fatalf("%s: result._meta.io.modelcontextprotocol/serverInfo is %T, want an object",
				name, meta["io.modelcontextprotocol/serverInfo"])
		}
		if info["name"] != "servicesim" {
			t.Errorf("%s: serverInfo.name is %v, want \"servicesim\"", name, info["name"])
		}
		if _, ok := info["version"].(string); !ok {
			t.Errorf("%s: serverInfo.version is %T, want a string", name, info["version"])
		}
	}
}

// TestMCPErrorEnvelopesCarryCodes pins decision 6's status/code pairing for
// every error golden: each is a JSON-RPC error object carrying exactly the
// integer code its filename claims, at the HTTP status its provenance entry
// records — the two facts a consumer's retry logic and its JSON-RPC error
// handling key on independently, so a golden that got one right and the
// other wrong would still look correct at a glance.
func TestMCPErrorEnvelopesCarryCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		golden        string
		status        int
		code          int
		wantIDOmitted bool // true when the response's id cannot be attributed
		// to a request at all — schema.json's RequestId admits only string or
		// integer (no null variant), so JSONRPCErrorResponse.id, which the
		// schema itself marks optional, must be an ABSENT key in that case,
		// never a JSON null (see profiles/mcp/contracts/README.md, "Error
		// response" row, and profiles/mcp/jsonrpc.go's nullID).
	}{
		{"mcp-error-header-mismatch.json", 400, -32020, false},
		{"mcp-error-unsupported-version.json", 400, -32022, false},
		{"mcp-error-method-not-found.json", 404, -32601, false},
		{"mcp-error-unknown-tool.json", 200, -32602, false},
		{"mcp-error-invalid-request.json", 400, -32600, true},
		{"mcp-error-parse.json", 400, -32700, true},
		{"mcp-401.json", 401, -32600, true},
		{"mcp-405.json", 405, -32600, true},
		{"mcp-fault-503.json", 503, -32603, false},
	}

	records, err := contracts.Provenance(Profile().Contracts)
	if err != nil {
		t.Fatalf("loading provenance: %v", err)
	}
	for _, tc := range tests {
		t.Run(tc.golden, func(t *testing.T) {
			t.Parallel()

			record, ok := records[tc.golden]
			if !ok {
				t.Fatalf("%s has no provenance entry", tc.golden)
			}
			if record.Status != tc.status {
				t.Errorf("%s: provenance status is %d, want %d", tc.golden, record.Status, tc.status)
			}

			body := decodeContractGolden(t, tc.golden)
			if _, ok := body["result"]; ok {
				t.Fatalf("%s carries a result, not an error", tc.golden)
			}
			errObj, ok := body["error"].(map[string]any)
			if !ok {
				t.Fatalf("%s: error is %T, want an object", tc.golden, body["error"])
			}
			code, ok := errObj["code"].(json.Number)
			if !ok {
				t.Fatalf("%s: error.code is %T, want a JSON integer", tc.golden, errObj["code"])
			}
			got, err := code.Int64()
			if err != nil {
				t.Fatalf("%s: error.code %s is not an integer: %v", tc.golden, code, err)
			}
			if got != int64(tc.code) {
				t.Errorf("%s: error.code is %d, want %d", tc.golden, got, tc.code)
			}
			if _, ok := errObj["message"].(string); !ok {
				t.Errorf("%s: error.message is %T, want a string", tc.golden, errObj["message"])
			}
			_, hasID := body["id"]
			switch {
			case tc.wantIDOmitted && hasID:
				t.Errorf("%s: carries an id member, but this response's id cannot be attributed to a "+
					"request — schema.json's RequestId has no null variant, so the member must be "+
					"absent, never a JSON null", tc.golden)
			case !tc.wantIDOmitted && !hasID:
				t.Errorf("%s: no top-level id key; this response's id is the request's own and must be echoed", tc.golden)
			}
		})
	}
}

// decodeContractGolden reads and decodes one contract golden by its bare
// file name, through mcpGoldenBytes (stream_test.go). Numbers are kept as
// json.Number so a test can tell a JSON-RPC integer error code from a
// string.
func decodeContractGolden(t *testing.T, name string) map[string]any {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(mcpGoldenBytes(t, name)))
	decoder.UseNumber()

	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		t.Fatalf("decoding %s: %v", name, err)
	}
	return body
}
