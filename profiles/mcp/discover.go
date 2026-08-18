package mcp

import (
	"encoding/json"
	"net/http"

	"github.com/c360studio/servicesim/provider"
)

// handleDiscover serves server/discover: no method-specific params beyond
// the standard _meta, already validated by checkTransport.
func handleDiscover(id json.RawMessage, p *projection) provider.Response {
	body, err := renderDiscover(p)
	if err != nil {
		return internalErrorResponse(id, err)
	}
	return methodResult(id, body, "mcp.server_discover.ok")
}

// methodResult wraps a rendered result body in the JSON-RPC success
// envelope and returns the Response every successful method shares.
func methodResult(id json.RawMessage, resultBody []byte, label string) provider.Response {
	envelope, err := buildResult(id, resultBody)
	if err != nil {
		return internalErrorResponse(id, err)
	}
	return provider.Response{
		Status:        http.StatusOK,
		Body:          envelope,
		Label:         label,
		FaultEligible: true,
		FaultBody:     faultBody(id),
	}
}
