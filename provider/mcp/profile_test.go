package mcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/provider"
)

// TestRefusalBodyRefuseInternalNeverEchoesTheFinding proves RefuseInternal's
// body carries a fixed message, never the finding text a caller recorded —
// unlike RefuseRequest and the default branch, which quote Findings()
// deliberately. mux.go's recoverPanic records CodeHandlerPanic with the raw
// panic value interpolated by %v, and handle.go's stream.grammar_missing
// refusal records arbitrary route/profile text the same way; neither has
// passed through redact.String the way a journal entry has, so echoing it
// onto the wire would be the one profile of the four that does — exa,
// tavily and perplexity each serve a fixed "internal server error" text for
// this refusal kind instead of the finding, and MCP must match them.
func TestRefusalBodyRefuseInternalNeverEchoesTheFinding(t *testing.T) {
	t.Parallel()
	x := &provider.Exchange{}
	x.Fail(provider.CodeHandlerPanic, "", "handler panicked: runtime error: invalid memory address or nil pointer dereference")

	body := refusalBody(provider.Refusal{Kind: provider.RefuseInternal, Status: 500, X: x})

	require.NotContains(t, string(body), "invalid memory address",
		"RefuseInternal must never put the raw panic text on the wire")
	require.NotContains(t, string(body), "handler panicked")
	require.Contains(t, string(body), "Internal error")
}

// TestRefusalBodyRefuseInternalWithNilX proves the fixed message still
// renders with no Exchange at all — RefuseInternal's one other caller shape.
func TestRefusalBodyRefuseInternalWithNilX(t *testing.T) {
	t.Parallel()
	body := refusalBody(provider.Refusal{Kind: provider.RefuseInternal, Status: 500})
	require.True(t, strings.Contains(string(body), "Internal error"))
}
