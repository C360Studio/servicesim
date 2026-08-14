package exa

import (
	"net/http"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/internal/wire"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// ErrorResponse is Exa's canonical error body: a flat object, not a nested
// {error: {code, message}}.
type ErrorResponse struct {
	RequestID string `json:"requestId"`
	Error     string `json:"error"`
	Tag       string `json:"tag"`
}

// RateLimitResponse is Exa's 429 body, which uses a reduced shape carrying only
// "error" — no requestId and no tag. A simulator that always emitted
// ErrorResponse would be wrong for exactly this status, which is the one a
// consumer's retry logic branches on.
type RateLimitResponse struct {
	Error string `json:"error"`
}

// Documented error tags.
const (
	TagInvalidRequestBody = "INVALID_REQUEST_BODY"
	TagInvalidJSONSchema  = "INVALID_JSON_SCHEMA"
	TagInvalidAPIKey      = "INVALID_API_KEY"
	TagNoMoreCredits      = "NO_MORE_CREDITS"
	TagInvalidRequest     = "INVALID_REQUEST"
	TagInternalError      = "INTERNAL_ERROR"
	TagNotFound           = "NOT_FOUND"
	TagMethodNotAllowed   = "METHOD_NOT_ALLOWED"

	// TagUnableToGenerateResponse is documented for /answer only and has no
	// /search analogue. It is the "the model could not answer" path, and a
	// research product's error handling is incomplete without it.
	TagUnableToGenerateResponse = "UNABLE_TO_GENERATE_RESPONSE"
)

// defaultRateLimitMessage is Exa's documented 429 body text, reproduced verbatim
// so a consumer's error-message assertions match the real API.
const defaultRateLimitMessage = "You've exceeded your Exa rate limit of 10 requests per second. " +
	"If you want this increased, please email hello@exa.ai :)" // servicesim:allow-live-host

// Canonical messages for the statuses this package produces on its own. They are
// used for routing and authentication failures, where quoting the internal
// reason would leak simulator detail into a body that is meant to look like the
// vendor's. A field-validation failure quotes its finding message instead, which
// is what makes the verbatim enum error in contracts/exa reachable.
const (
	messageInvalidRequestBody = "Invalid request body"
	messageInvalidAPIKey      = "Invalid API key"
	messageNoMoreCredits      = "You have no more credits"
	messageInvalidRequest     = "Invalid request"
	messageInternalError      = "Internal server error"
	messageNotFound           = "Not Found"
	messageMethodNotAllowed   = "Method Not Allowed"
	messageUnableToGenerate   = "Unable to generate a response"
	messageInvalidJSONSchema  = "Invalid JSON schema supplied in outputSchema"
)

// errorBody renders the error envelope for status. A 429 gets the reduced
// {error} shape and every other status gets the flat {requestId, error, tag}.
//
// A marshalling failure cannot happen for these two fixed shapes, so the error
// is dropped rather than propagated to a caller that has no better answer than
// writing nothing.
func errorBody(requestID, message, tag string, status int) []byte {
	if status == http.StatusTooManyRequests {
		body, _ := wire.Render(RateLimitResponse{Error: message}, nil)
		return body
	}
	body, _ := wire.Render(ErrorResponse{RequestID: requestID, Error: message, Tag: tag}, nil)
	return body
}

// faultBody builds the provider-shaped body a fault attempt sends. It is what
// keeps §2.5's rule — "the body is provider-shaped and built by the provider
// package" — true without provider knowledge leaking into fault execution.
//
// An attempt that declares its own `body:` gets it verbatim. That precedence is
// applied here rather than left to fault execution: execution consults an
// attempt's body only when the provider supplies no FaultBody at all, and this
// package always supplies one, so a scenario's verbatim body would otherwise be
// silently ignored in favour of the rendered success body.
//
// It returns nil when the attempt's status is not an error at all, which is how
// invalid_json and wrong_content_type mangle an otherwise successful response
// without replacing it.
func faultBody(requestID string, a scenario.FaultAttempt) []byte {
	if len(a.Body) > 0 {
		body, err := wire.Render(a.Body, nil)
		if err != nil {
			return nil
		}
		return body
	}
	if a.Status < 400 {
		return nil
	}

	message, tag := defaultsFor(a.Status)
	if a.Error != "" {
		message = a.Error
	}
	if a.Tag != "" {
		tag = a.Tag
	}
	return errorBody(requestID, message, tag, a.Status)
}

// defaultsFor returns the message and tag Exa documents for a status. The 429
// message is the vendor's verbatim text; its tag is returned for completeness
// and dropped by errorBody, because the reduced rate-limit shape carries none.
func defaultsFor(status int) (message, tag string) {
	switch status {
	case http.StatusBadRequest:
		return messageInvalidRequestBody, TagInvalidRequestBody
	case http.StatusUnauthorized:
		return messageInvalidAPIKey, TagInvalidAPIKey
	case http.StatusPaymentRequired:
		return messageNoMoreCredits, TagNoMoreCredits
	case http.StatusNotFound:
		return messageNotFound, TagNotFound
	case http.StatusMethodNotAllowed:
		return messageMethodNotAllowed, TagMethodNotAllowed
	case http.StatusUnprocessableEntity:
		return messageInvalidRequest, TagInvalidRequest
	case http.StatusTooManyRequests:
		return defaultRateLimitMessage, ""
	case http.StatusNotImplemented:
		return messageUnableToGenerate, TagUnableToGenerateResponse
	default:
		if status >= http.StatusInternalServerError {
			return messageInternalError, TagInternalError
		}
		return messageInvalidRequest, TagInvalidRequest
	}
}

// classify maps the first error finding on a rejected request onto the status,
// tag and message its body carries.
//
// It reads Exchange.Findings rather than a local variable because that method
// applies the scenario's validation policy and imposes a total order on the
// result: without the ordering, a request with two errors would pick its status
// from Go's map iteration and a golden over the body would flake.
func classify(x *provider.Exchange) (status int, tag, message string) {
	for _, f := range x.Findings() {
		if f.Severity != journal.SeverityError {
			continue
		}
		return classifyCode(f.Code, f.Message)
	}
	return http.StatusBadRequest, TagInvalidRequestBody, messageInvalidRequestBody
}

func classifyCode(code, message string) (int, string, string) {
	switch code {
	case codeAuthMissing, codeAuthMismatch:
		return http.StatusUnauthorized, TagInvalidAPIKey, messageInvalidAPIKey

	case provider.CodeUnmatched:
		return http.StatusNotFound, TagNotFound, messageNotFound

	case provider.CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed, TagMethodNotAllowed, messageMethodNotAllowed

	case provider.CodeNoMatchingTurn:
		// The scenario has nothing to say about this call. It is an authoring
		// problem, but from the client's side the requested thing does not
		// exist, so it is reported the way an absent route is.
		return http.StatusNotFound, TagNotFound, messageNotFound

	case provider.CodeBodyTooLarge:
		return http.StatusRequestEntityTooLarge, TagInvalidRequestBody, message

	case provider.CodeNoHandler, codeProjectionInvalid, codeRenderFailed:
		return http.StatusInternalServerError, TagInternalError, messageInternalError

	case provider.CodeNamespaceLimit:
		// 503 rather than 500: the simulator is refusing to take on more state,
		// not reporting a broken scenario, and the operator's fix is to raise
		// --max-namespaces. The finding's own message carries that instruction,
		// so it is passed through rather than flattened to a generic string —
		// this is a Servicesim configuration problem and the consumer needs to
		// be told which knob, not handed a plausible-looking vendor error.
		return http.StatusServiceUnavailable, TagInternalError, message

	case codeOutputSchemaDepth, codeOutputSchemaProperties:
		return http.StatusBadRequest, TagInvalidJSONSchema, messageInvalidJSONSchema

	default:
		return http.StatusBadRequest, TagInvalidRequestBody, message
	}
}
