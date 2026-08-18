package mcp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
)

// TestDecodeSentinelContractExamples pins the four examples
// contracts/mcp/README.md quotes verbatim from the specification's "Value
// Encoding" section, plus the "not sentinel-shaped" pass-through case.
func TestDecodeSentinelContractExamples(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"non-ASCII", "=?base64?SGVsbG8sIOS4lueVjA==?=", "Hello, 世界"},
		{"leading and trailing whitespace", "=?base64?IHBhZGRlZCA=?=", " padded "},
		{"embedded newline", "=?base64?bGluZTEKbGluZTI=?=", "line1\nline2"},
		{"a literal that looks sentinel-shaped", "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?=", "=?base64?literal?="},
		{"an ordinary header-safe name, not wrapped", "search", "search"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeSentinel(tc.value)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestDecodeSentinelInvalidCharacters(t *testing.T) {
	t.Parallel()
	_, err := decodeSentinel("=?base64?not valid base64!?=")
	require.Error(t, err)
}

// TestMcpNameSentinelEndToEnd proves the header comparison decodes the
// sentinel before comparing, using the specification's own first example.
func TestMcpNameSentinelEndToEnd(t *testing.T) {
	t.Parallel()
	src := `
version: 1
name: v
providers:
  mcp:
    tools:
      - name: "Hello, 世界"
        input_schema: {type: object}
    results:
      "Hello, 世界":
        content: [{type: text, text: hi}]
`
	handler := newHandler(t, src, nil)

	headers := withHeaders(stdHeaders(methodToolsCall, "=?base64?SGVsbG8sIOS4lueVjA==?="), nil)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":` + defaultMeta +
		`,"name":"Hello, 世界"}}`
	rec := do(handler, body, headers)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestMcpNameSentinelInvalidCharactersIsHeaderMismatch(t *testing.T) {
	t.Parallel()
	ring := journal.NewRing(10, 1<<20)
	handler := newHandler(t, goldenScenario, ring)

	headers := withHeaders(stdHeaders(methodToolsCall, "=?base64?not valid!?="), nil)
	rec := do(handler, callRequest("1", "search", ""), headers)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	got := decodeError(t, rec)
	require.Equal(t, codeHeaderMismatchError, got.Code)

	// An undecodable sentinel and an ordinary value mismatch both render
	// -32020 (the wire code is the schema's, and it is the only one this
	// case is assigned) — but they must be journaled under DIFFERENT
	// finding codes, so a consumer can tell "your Mcp-Name value doesn't
	// match params.name" from "your Mcp-Name value isn't valid base64"
	// apart. Skipping the sentinel decode entirely would leave this test
	// green under the ordinary-mismatch code, since an undecoded sentinel
	// also fails to equal "search" — only the finding CODE and the
	// message's own wording distinguish the two.
	entry := ring.Snapshot()[0]
	require.True(t, hasFinding(entry, CodeHeaderInvalidChars))
	require.False(t, hasFinding(entry, CodeHeaderMismatch),
		"invalid characters is its own finding code, not the ordinary mismatch one")
	found := false
	for _, f := range entry.Findings {
		if f.Code == CodeHeaderInvalidChars {
			found = true
			require.Contains(t, f.Message, "invalid characters")
		}
	}
	require.True(t, found)
}

// TestMcpNameSentinelDecodedValueIsRedactedHeaderIsOpaque pins the
// documented redaction boundary (profiles/mcp/doc.go, "Redaction and the
// Base64 sentinel"): a credential-shaped tool name sent sentinel-encoded
// is masked everywhere it reaches the journal DECODED — the unknown-tool
// finding message — but the retained Mcp-Name header itself, which
// internal/redact has no sentinel awareness to decode, is not.
func TestMcpNameSentinelDecodedValueIsRedactedHeaderIsOpaque(t *testing.T) {
	t.Parallel()
	ring := journal.NewRing(10, 1<<20)
	handler := newHandler(t, goldenScenario, ring)

	const decoded = "sk-live-ZQ4NUXW7PL06060606"
	// base64 of decoded, computed once and pinned as a literal so this test
	// does not depend on encoding/base64 to prove encoding/base64 correct.
	const sentinel = "=?base64?c2stbGl2ZS1aUTROVVhXN1BMMDYwNjA2MDY=?="

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":` + defaultMeta +
		`,"name":"` + decoded + `"}}`
	headers := withHeaders(stdHeaders(methodToolsCall, sentinel), nil)
	rec := do(handler, body, headers)
	require.Equal(t, http.StatusOK, rec.Code, "decision 6: unknown tool is 200")

	entry := ring.Snapshot()[0]

	// The decoded value reached an "unknown tool" finding built from
	// params.name; journal.Redact masks it like any other free-text field.
	found := false
	for _, f := range entry.Findings {
		if f.Code == CodeToolUnknown {
			found = true
			require.NotContains(t, f.Message, decoded)
		}
	}
	require.True(t, found)

	// The retained Mcp-Name header, by contrast, is the sentinel-encoded
	// wire value as sent: redact has no sentinel awareness, so this is
	// opaque to it, not masked. Documented, not accidental — see doc.go.
	require.Equal(t, []string{sentinel}, entry.Headers[headerName])
}
