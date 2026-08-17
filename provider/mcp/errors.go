package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/c360studio/servicesim/provider"
)

// internalErrorResponse renders decision 6's -32603 shape for a response
// this profile had already decided to serve but could not render. Status
// 200: an ordinary JSON-RPC error, not a request-shape failure.
//
// FaultEligible is set: every caller of this function runs after
// selectProjection has already claimed the attempt (the "no scripted turn
// matches" case included — SelectTurnFor claims before it can know the
// turn search failed), so a scripted fault applying to this exchange must
// render through faultBody's fixed envelope rather than leave this
// response's own body under an overridden status. A caller whose own
// x.Fail already ran (SelectTurnFor's failure path) still answers exactly
// as before: Handle forces FaultEligible back to false once x.Failed() is
// true, regardless of what is set here.
func internalErrorResponse(id json.RawMessage, err error) provider.Response {
	resp := statusResponse(errAt{
		Status: http.StatusOK, Code: CodeInternalError,
		Message: fmt.Sprintf("the response could not be rendered: %v", err), ID: id,
	}, "mcp.error.internal")
	resp.FaultEligible = true
	resp.FaultBody = faultBody(id)
	return resp
}

// methodNotFoundResponse renders decision 6's -32601 shape, 404. "initialize"
// carries a special message (decision 12): every other unrecognised method
// gets a generic one.
func methodNotFoundResponse(id json.RawMessage, method string) provider.Response {
	message := fmt.Sprintf("method not found: %q", method)
	if method == methodInitialize {
		message = fmt.Sprintf(
			"method not found: %q; this server speaks protocol version(s) %v only", method, supportedVersions)
	}
	return statusResponse(errAt{Status: http.StatusNotFound, Code: CodeMethodNotFoundError, Message: message, ID: id},
		"mcp.error.method_not_found")
}

// notFoundResponse renders the fail-closed body for any path this
// listener does not serve. The only known path is POST /mcp, so this is
// what a request to any other path receives — status and code follow
// decision 6's "404 + -32601 keep their assigned status" the same way a
// dispatch-time unknown method does, since a wrong path is, from this
// listener's point of view, the same kind of "nothing here answers that"
// refusal.
func notFoundResponse() provider.Response {
	resp := statusResponse(errAt{
		Status: http.StatusNotFound, Code: CodeMethodNotFoundError,
		Message: "no route serves this request; the MCP endpoint is POST /mcp", ID: nullID,
	}, "mcp.error.not_found")
	resp.FaultEligible = false
	return resp
}

// methodNotAllowedResponse renders decision 6's 405 shape: Allow: POST is
// added by provider.NewMux's wrapper, not here.
func methodNotAllowedResponse() provider.Response {
	resp := statusResponse(errAt{
		Status: http.StatusMethodNotAllowed, Code: CodeInvalidRequestError,
		Message: "the MCP endpoint accepts POST only", ID: nullID,
	}, "mcp.error.method_not_allowed")
	resp.FaultEligible = false
	return resp
}
