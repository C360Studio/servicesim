package tavily

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/internal/wire"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// The documented error messages, reproduced verbatim where the vendor publishes
// one. Where it does not — the 400 example text is stale against the current
// three-value topic enum, and 404 and 405 are this simulator's fail-closed
// routing rather than vendor behaviour — the envelope is still the documented
// one and only the text is Servicesim's. contracts/tavily/provenance.yaml
// records which is which.
const (
	// MessageUnauthorized is the documented 401 body text.
	MessageUnauthorized = "Unauthorized: missing or invalid API key."

	// MessageRateLimited is the documented 429 body text. No Retry-After header
	// is documented, so none is emitted unless a fault declares one.
	MessageRateLimited = "Your request has been blocked due to excessive requests. " +
		"Please reduce rate of requests."

	// MessagePlanLimitExceeded is the documented 432 body text, with the
	// example's angle-bracket placeholder removed.
	MessagePlanLimitExceeded = "This request exceeds your plan's set usage limit. " +
		"Please upgrade your plan or contact support@tavily.com" // servicesim:allow-live-host

	// MessagePayGoLimitExceeded is the documented 433 body text.
	MessagePayGoLimitExceeded = "This request exceeds the pay-as-you-go limit. " +
		"You can increase your limit on the Tavily dashboard."

	// MessageInternalServerError is the documented 500 body text.
	MessageInternalServerError = "Internal Server Error"

	// MessageNotFound is the fail-closed 404 body text.
	MessageNotFound = "Not Found"

	// MessageMethodNotAllowed is the fail-closed 405 body text.
	MessageMethodNotAllowed = "Method Not Allowed"

	// MessageBadRequest is the fallback 400 text, used only when a request was
	// rejected with no finding message to quote.
	MessageBadRequest = "Bad Request"

	// MessageBodyTooLarge is the 413 body text.
	MessageBodyTooLarge = "Request body is too large."

	// MessageNoMatchingTurn reports a scenario whose script has no turn for this
	// request. It is an authoring problem in the fixture, surfaced per request
	// because whether it applies depends on the request.
	MessageNoMatchingTurn = "No scripted response matches this request."
)

// statusMessages maps a status onto the documented body text a fault uses when
// it declares none of its own.
var statusMessages = map[int]string{
	http.StatusBadRequest:            MessageBadRequest,
	http.StatusUnauthorized:          MessageUnauthorized,
	http.StatusNotFound:              MessageNotFound,
	http.StatusMethodNotAllowed:      MessageMethodNotAllowed,
	http.StatusRequestEntityTooLarge: MessageBodyTooLarge,
	http.StatusTooManyRequests:       MessageRateLimited,
	StatusPlanLimitExceeded:          MessagePlanLimitExceeded,
	StatusPayGoLimitExceeded:         MessagePayGoLimitExceeded,
	http.StatusInternalServerError:   MessageInternalServerError,
}

// authCodes are the findings that make a rejection a 401 rather than a 400.
var authCodes = []string{CodeAuthMissing, CodeAuthMismatch}

// errorBody renders the one envelope every documented Tavily status uses. It is
// a nested object; a flat {"error": …} is the shape implementers invent when
// they have not read the contract, and no consumer decoder written against the
// real API would parse it.
func errorBody(message string) []byte {
	body, err := wire.Render(ErrorResponse{Detail: ErrorDetail{Error: message}}, nil)
	if err != nil {
		// ErrorResponse is two strings; marshalling it cannot fail. Returning a
		// hand-written body rather than panicking keeps a bug here from taking
		// the process down on a request path.
		return []byte(`{"detail":{"error":"Internal Server Error"}}`)
	}
	return body
}

// errorResponse builds the response for a request that failed validation,
// authentication or routing. It reads the findings the handler recorded, so the
// status and the body text cannot drift apart from what the journal says
// happened.
func errorResponse(x *provider.Exchange) provider.Response {
	status := http.StatusBadRequest
	message := MessageBadRequest

	errs := errorFindings(x)
	switch {
	case containsAny(errs, authCodes):
		status, message = http.StatusUnauthorized, MessageUnauthorized
	case containsAny(errs, []string{provider.CodeBodyTooLarge}):
		status, message = http.StatusRequestEntityTooLarge, MessageBodyTooLarge
	case containsAny(errs, []string{provider.CodeNoMatchingTurn}):
		status, message = http.StatusNotFound, MessageNoMatchingTurn
	case containsAny(errs, []string{CodeProjectionInvalid}):
		status, message = http.StatusInternalServerError, MessageInternalServerError
	case containsAny(errs, []string{provider.CodeNamespaceLimit}):
		// 503, and the finding's own message rather than a constant: this is a
		// Servicesim configuration problem, and the consumer needs to be told
		// which knob to turn, not handed a plausible-looking vendor error.
		status, message = http.StatusServiceUnavailable, errs[0].Message
	case len(errs) > 0:
		// The first error in Findings order. That order is total — severity,
		// then field, then code — so a request with two problems reports the
		// same one on every run.
		message = errs[0].Message
	}
	return staticResponse(status, message)
}

// staticResponse is a provider-shaped error with no scenario behind it.
func staticResponse(status int, message string) provider.Response {
	return provider.Response{
		Status: status,
		Body:   errorBody(message),
		Label:  label(status),
	}
}

// label names an error selection for the journal and the logs.
func label(status int) string {
	return Name + ".error." + strconv.Itoa(status)
}

// errorFindings returns the error-severity findings in Findings order, after
// the scenario's validation policy has promoted or demoted them.
func errorFindings(x *provider.Exchange) []journal.Finding {
	var errs []journal.Finding
	for _, f := range x.Findings() {
		if f.Severity == journal.SeverityError {
			errs = append(errs, f)
		}
	}
	return errs
}

// containsAny reports whether any finding carries one of the codes.
func containsAny(findings []journal.Finding, codes []string) bool {
	for _, f := range findings {
		if slices.Contains(codes, f.Code) {
			return true
		}
	}
	return false
}

// faultBody builds the provider-shaped body for a fault attempt. It is how
// §2.5's rule that "the body is provider-shaped and built by the provider
// package" is honoured without provider knowledge leaking into fault execution.
//
// Returning nil means "the fault has nothing provider-shaped to say", which
// leaves the rendered scenario body in place — the right answer for a truncated
// or wrongly-typed body, where the point of the fault is the transport rather
// than the payload.
//
// The attempt's own body: wins outright, and is marshalled here rather than
// left to the caller: provider.faultBody consults the attempt's body only when
// no FaultBody function was supplied at all, so a provider that declares one
// owns that precedence.
func faultBody(a scenario.FaultAttempt) []byte {
	if len(a.Body) > 0 {
		if body, err := json.Marshal(a.Body); err == nil {
			return body
		}
		return nil
	}
	if a.Error != "" {
		return errorBody(a.Error)
	}
	if a.EffectiveKind() != scenario.FaultStatus {
		// A delay, a truncation, a wrong content type or a trailing
		// "status: 200" attempt all keep the scenario's own body.
		return nil
	}
	if message, ok := statusMessages[a.Status]; ok {
		return errorBody(message)
	}
	if text := http.StatusText(a.Status); text != "" {
		return errorBody(text)
	}
	return nil
}
