package exa

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Every agent-run response is an AgentErrorResponse on failure: one `error` key
// holding {type, code, message} (openapi lines 6044-6083, retrieved 2026-10-01).
// That is NOT the flat {requestId, error, tag} the other Exa routes use, which
// stay as they are.
//
// The spec closes the type and code enums and lists which statuses each
// operation can return, but it never pairs a type and code with a status. The
// pairing below is INFERENCE from the enum names and the response descriptions,
// recorded as such in contracts/README.md; it is not a vendor guarantee.

// agentErrorResponseWire is AgentErrorResponse.
type agentErrorResponseWire struct {
	Error agentErrorWire `json:"error"`
}

// agentErrorWire is AgentError. The schema allows extra keys; this simulator
// emits none.
type agentErrorWire struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AgentError.type and AgentError.code members the pairing below uses.
const (
	agentTypeInvalidRequest = "INVALID_REQUEST"
	agentTypeAuthentication = "AUTHENTICATION_ERROR"
	agentTypeRateLimit      = "RATE_LIMIT_ERROR"
	agentTypeNotFound       = "NOT_FOUND"
	agentTypeServer         = "SERVER_ERROR"

	agentCodeInvalidRequest   = "INVALID_REQUEST"
	agentCodeTeamNotFound     = "TEAM_NOT_FOUND"
	agentCodeRunNotFound      = "RUN_NOT_FOUND"
	agentCodeConcurrencyLimit = "CONCURRENCY_LIMIT_REACHED"
	agentCodeServerError      = "SERVER_ERROR"
)

// agentErrorCodeEnum is AgentError.code (openapi lines 6063-6075), the closed set a
// fault attempt's tag is checked against.
var agentErrorCodeEnum = []string{
	"INVALID_REQUEST", "TEAM_NOT_FOUND", "RUN_NOT_FOUND", "PREVIOUS_RUN_NOT_FOUND", "PREVIOUS_RUN_NOT_COMPLETED",
	"CONCURRENCY_LIMIT_REACHED", "INVALID_OUTPUT_SCHEMA", "INVALID_DATA_SOURCE", "TIMEOUT", "SERVER_ERROR",
}

// codeAgentRunFaultTagUnknown warns that a fault attempt's `tag:` on an
// exa_agent_runs entry is outside AgentError's code enum. The tag is rendered
// verbatim as `code` anyway — scripting an off-enum code is allowed on purpose —
// but a flat-envelope tag copied from another Exa route (RATE_LIMIT, INTERNAL,
// INVALID_API_KEY) would otherwise reach the consumer as that code without a word
// of warning. It is unexported: a consumer asserts on the string.
const codeAgentRunFaultTagUnknown = "exa.agent_run.fault_tag.unknown"

// validateAgentRunFaultTags reports every fault attempt of the entry — the create
// plan and each turn's poll plan — whose tag is not an AgentError code. An
// attempt that declares its own `body:` renders no tag, so it is not checked.
func validateAgentRunFaultTags(e *scenario.ProviderEntry) []scenario.Finding {
	var findings []scenario.Finding
	check := func(path string, f *scenario.Fault) {
		if f == nil {
			return
		}
		for i, a := range f.Attempts {
			if a.Tag == "" || len(a.Body) > 0 || slices.Contains(agentErrorCodeEnum, a.Tag) {
				continue
			}
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityWarning,
				Code:     codeAgentRunFaultTagUnknown,
				Path:     fmt.Sprintf("%s.attempts[%d].tag", path, i),
				Message: fmt.Sprintf("tag %q is not an AgentError code (%s); on an agent route it is emitted verbatim as "+
					"the error's `code`, so a flat-envelope tag copied from another Exa route would reach the consumer "+
					"as code %q — drop the tag to get the status's default code", a.Tag,
					strings.Join(agentErrorCodeEnum, ", "), a.Tag),
			})
		}
	}

	if e.Create != nil {
		check(fmt.Sprintf("providers.%s.create.fault", e.Name), e.Create.Fault)
	}
	if e.Cancel != nil {
		check(fmt.Sprintf("providers.%s.cancel.fault", e.Name), e.Cancel.Fault)
	}
	for i := range e.Turns {
		check(fmt.Sprintf("providers.%s.turns[%d].fault", e.Name, i), e.Turns[i].Fault)
	}
	return findings
}

// agentError is the AgentError for a status. detail is the message a rejection
// carries (a validation finding's text, a job-bound instruction); it is used
// only for the statuses whose message is not fixed, and an empty detail falls
// back to the default.
//
//   - 401, 404 and 429 carry the spec's own response description as their
//     message, because their type and code already say everything a finding
//     could.
//   - Every other 4xx is INVALID_REQUEST and every 5xx is SERVER_ERROR. The spec
//     documents only 400, 401, 404, 429 and 500 for these operations, so 402,
//     405, 413, 422, 501, 503 and the rest are simulator policy.
func agentError(status int, detail string) agentErrorWire {
	orDefault := func(def string) string {
		if detail != "" {
			return detail
		}
		return def
	}

	switch {
	case status == http.StatusUnauthorized:
		return agentErrorWire{agentTypeAuthentication, agentCodeTeamNotFound, "Team context or authentication was not found"}
	case status == http.StatusNotFound:
		return agentErrorWire{agentTypeNotFound, agentCodeRunNotFound, "Run not found"}
	case status == http.StatusTooManyRequests:
		return agentErrorWire{agentTypeRateLimit, agentCodeConcurrencyLimit, "Agent run concurrency limit reached"}
	case status >= http.StatusInternalServerError:
		return agentErrorWire{agentTypeServer, agentCodeServerError, orDefault("Server error")}
	default:
		return agentErrorWire{agentTypeInvalidRequest, agentCodeInvalidRequest, orDefault("Invalid request")}
	}
}

// agentErrorBody renders an AgentErrorResponse. A marshalling failure cannot
// happen for this fixed shape, so the error is dropped for the reason errorBody
// gives.
func agentErrorBody(e agentErrorWire) []byte {
	body, _ := provider.Render(agentErrorResponseWire{Error: e}, nil, nil)
	return body
}

// agentFaultBody builds the body a fault attempt sends on an agent route, the way
// faultBody does for the others.
//
// A scenario's fault attempt carries `error:` and `tag:`, which are the FLAT
// envelope's fields. The nested envelope has no tag, so they map onto it as
// `error:` -> message and `tag:` -> code, verbatim: a scenario may script a code
// outside the spec's enum to test a consumer's handling of one the vendor adds
// later (validateAgentRunFaultTags warns about one at load). type is always
// derived from the status. An attempt's own `body:` still wins outright.
func agentFaultBody(a scenario.FaultAttempt) []byte {
	if body, ok := verbatimFaultBody(a); ok {
		return body
	}

	e := agentError(a.Status, "")
	if a.Error != "" {
		e.Message = a.Error
	}
	if a.Tag != "" {
		e.Code = a.Tag
	}
	return agentErrorBody(e)
}

// isAgentRoute reports whether x is being served by one of the three agent-run
// routes, the only ones that speak AgentErrorResponse.
func isAgentRoute(x *provider.Exchange) bool {
	return slices.Contains([]string{faultKeyRunCreate, faultKeyRunPoll, faultKeyRunHead}, x.Route.FaultKey)
}

// requestIDHeader is the x-request-id response header the spec documents on every
// agent response, success and error alike (XRequestId, openapi line 11084).
//
// Agent bodies carry no requestId, so the header is the only request id an agent
// response has. Its value is derived exactly as requestId is on the other routes.
// It cannot be set on a response the framework refuses before a handler runs — an
// unrouted path, a method the mux refuses, a handler panic — because a
// provider.Profile's ErrorBody returns a body only.
func requestIDHeader(id string) http.Header {
	h := http.Header{}
	h.Set("x-request-id", id)
	return h
}
