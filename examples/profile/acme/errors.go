package acme

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// errorEnvelope is Acme's one documented error shape:
// {"error":{"code":"...","message":"..."}}. Every refusal this profile
// serves — 404, 405, 401, 400 and 500 alike — renders through it, because a
// consumer's error-path test should not have to learn five envelopes for one
// vendor.
type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// The fixed messages for the framework-generated refusals — the three kinds
// with no request-specific finding to quote.
const (
	messageNotFound         = "the requested resource was not found"
	messageMethodNotAllowed = "method not allowed"
	messageInternal         = "internal server error"
)

// authCodes are the findings that make a rejection a 401 rather than a 400.
var authCodes = []string{CodeAuthMissing, CodeAuthMismatch}

// statusCodes is Acme's one error-code vocabulary, indexed by the status that
// carries it. Every code this profile can serve is here, and
// contracts/README.md's "Errors" table is this map written out — a scripted
// fault and a routing refusal that answer the same status must answer with
// the same code, or one vendor ships two vocabularies depending on where the
// refusal came from (profiles/tavily/errors.go keeps one table for the same
// reason).
var statusCodes = map[int]string{
	http.StatusBadRequest:          "bad_request",
	http.StatusUnauthorized:        "unauthorized",
	http.StatusNotFound:            "not_found",
	http.StatusMethodNotAllowed:    "method_not_allowed",
	http.StatusTooManyRequests:     "rate_limited",
	http.StatusInternalServerError: "internal_error",
}

// statusCode returns the documented code for a status, or a generic one for a
// status a scenario scripted that Acme's contract does not describe.
func statusCode(status int) string {
	if code, ok := statusCodes[status]; ok {
		return code
	}
	return "error"
}

// errorBody renders Acme's one envelope.
func errorBody(code, message string) []byte {
	body, err := provider.Render(errorEnvelope{Error: errorDetail{Code: code, Message: message}}, nil, nil)
	if err != nil {
		// errorEnvelope is two strings; marshalling it cannot fail. A
		// hand-written fallback keeps a bug here from taking a request path
		// down rather than serving a 500 with no body at all.
		return []byte(`{"error":{"code":"internal_error","message":"internal server error"}}`)
	}
	return body
}

// ErrorBody renders a provider.Refusal in Acme's own error shape, for every
// [provider.RefusalKind] — the REQUIRED field house rule 3 exists for (an
// unmatched path, method, provider or scenario must never answer with an
// empty body).
//
// RefuseRequest's branch reuses errorResponse(r.X): a request rejected
// through [provider.Exchange.Reject] (handleAnswer's CodeQueryMissing check)
// reaches ErrorBody exactly the way a request rejected through checkAuth's
// x.Fail reaches errorResponse directly, so the two paths cannot render two
// different envelopes for what is, from a consumer's point of view, one
// class of failure: "my request was refused, here is why."
func ErrorBody(r provider.Refusal) []byte {
	switch r.Kind {
	case provider.RefuseNotFound, provider.RefuseScenarioUnknown:
		return errorBody("not_found", messageNotFound)
	case provider.RefuseMethodNotAllowed:
		return errorBody("method_not_allowed", messageMethodNotAllowed)
	case provider.RefuseInternal:
		return errorBody("internal_error", messageInternal)
	case provider.RefuseRequest:
		if r.X == nil {
			return errorBody("bad_request", "bad request")
		}
		return errorResponse(r.X).Body
	default:
		return errorBody("internal_error", messageInternal)
	}
}

// errorResponse builds the response for a request that failed authentication
// or validation outside a direct [provider.Exchange.Reject] call (checkAuth's
// x.Fail, and selectProjection's scenario.no_matching_turn). It reads the
// findings the handler recorded, so the status and the body text cannot
// drift apart from what the journal says happened — the same discipline
// profiles/tavily/errors.go's own errorResponse follows.
func errorResponse(x *provider.Exchange) provider.Response {
	status := http.StatusBadRequest
	code := "bad_request"
	message := "bad request"

	errs := errorFindings(x)
	switch {
	case containsAny(errs, authCodes):
		status, code = http.StatusUnauthorized, "unauthorized"
	case containsAny(errs, []string{provider.CodeNoMatchingTurn}):
		status, code = http.StatusNotFound, "no_matching_turn"
	case containsAny(errs, []string{CodeProjectionInvalid}):
		status, code = http.StatusInternalServerError, "internal_error"
	}
	if len(errs) > 0 {
		// The first error in Findings order — a total order (severity, then
		// field, then code) — so a request with two problems reports the
		// same one on every run.
		message = errs[0].Message
	}
	return provider.Response{
		Status: status,
		Body:   errorBody(code, message),
		Label:  string(Name) + ".error." + strconv.Itoa(status),
	}
}

// errorFindings returns the error-severity findings in Findings order, after
// the scenario's validation policy has promoted or demoted them.
func errorFindings(x *provider.Exchange) []provider.Finding {
	var errs []provider.Finding
	for _, f := range x.Findings() {
		if f.Severity == provider.SeverityError {
			errs = append(errs, f)
		}
	}
	return errs
}

// containsAny reports whether any finding carries one of the codes.
func containsAny(findings []provider.Finding, codes []string) bool {
	for _, f := range findings {
		if slices.Contains(codes, f.Code) {
			return true
		}
	}
	return false
}

// faultBody builds the provider-shaped body for a fault attempt — how §2.5's
// rule that "the body is provider-shaped and built by the provider package"
// is honoured without provider knowledge leaking into fault execution.
//
// Returning nil leaves the rendered scenario body in place, which is the
// right answer for a fault with nothing provider-shaped to say (a delay, a
// truncation, a wrong content type).
func faultBody(a scenario.FaultAttempt) []byte {
	if len(a.Body) > 0 {
		if body, err := json.Marshal(a.Body); err == nil {
			return body
		}
		return nil
	}
	if a.Error != "" {
		return errorBody(statusCode(a.Status), a.Error)
	}
	if text := http.StatusText(a.Status); text != "" {
		return errorBody(statusCode(a.Status), text)
	}
	return nil
}
