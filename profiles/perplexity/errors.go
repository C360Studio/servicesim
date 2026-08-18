package perplexity

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// surface identifies which of the two Perplexity APIs an error body is shaped
// for. The two error models are genuinely different and must not be unified: 422
// is FastAPI's HTTPValidationError on both, every other Agent status is the
// published errorInfo envelope, and every other Sonar status is the
// simulator-chosen {"detail": "<string>"} form.
type surface string

// The two simulated surfaces.
const (
	// surfaceSonar is POST /v1/sonar and its /chat/completions alias.
	surfaceSonar surface = "sonar"

	// surfaceAgent is POST /v1/agent and its /v1/responses alias.
	surfaceAgent surface = "agent"
)

// agentErrorCode maps an HTTP status onto the Agent API's error code and type.
// The specification declares the fields without enumerating their members, so
// these values are Servicesim's; they are recorded as such in
// contracts/perplexity/provenance.yaml.
var agentErrorCode = map[int][2]string{
	http.StatusBadRequest:            {"invalid_request", "invalid_request_error"},
	http.StatusUnauthorized:          {"unauthorized", "authentication_error"},
	http.StatusForbidden:             {"forbidden", "permission_error"},
	http.StatusNotFound:              {"not_found", "invalid_request_error"},
	http.StatusMethodNotAllowed:      {"method_not_allowed", "invalid_request_error"},
	http.StatusRequestEntityTooLarge: {"payload_too_large", "invalid_request_error"},
	http.StatusTooManyRequests:       {"rate_limit_exceeded", "rate_limit_error"},
	http.StatusInternalServerError:   {"internal_error", "server_error"},
}

// statusMessage is the human-readable message an error body carries. It is
// http.StatusText, which reproduces the goldens exactly ("Bad Request",
// "Too Many Requests", "Internal Server Error") and never invents vendor prose.
func statusMessage(status int, override string) string {
	if override != "" {
		return override
	}
	if text := http.StatusText(status); text != "" {
		return text
	}
	return "Error"
}

// errorBody renders the provider-shaped error body for a status on a surface.
// message overrides the default text, which is what a scenario's `error:` on a
// fault attempt supplies.
//
// It never returns an error: the shapes it marshals are closed structs of
// strings and cannot fail to encode, and a handler that had to branch on a
// marshalling failure while already rendering an error would be reporting the
// wrong problem.
func errorBody(surface surface, status int, message string) []byte {
	text := statusMessage(status, message)
	if surface == surfaceAgent {
		info := errorInfo{Message: text}
		if pair, ok := agentErrorCode[status]; ok {
			info.Code, info.Type = pair[0], pair[1]
		}
		body, err := provider.Render(agentErrorResponse{Error: info}, nil, nil)
		if err != nil {
			return []byte(`{"error":{"message":"Internal Server Error"}}`)
		}
		return body
	}
	body, err := provider.Render(messageErrorResponse{Detail: text}, nil, nil)
	if err != nil {
		return []byte(`{"detail":"Internal Server Error"}`)
	}
	return body
}

// faultBody builds the body a fault attempt sends, so that a declared 429 on
// either surface still looks like that surface's own error shape rather than
// like the rendered success body under a failure status.
//
// The attempt's own `body:` wins outright when it declares one, because an
// author who spelled out a body meant it. Otherwise the attempt's `error:`
// becomes the message inside this surface's envelope.
//
// provider/fault_exec.go's faultBody only calls this at all when a.Status is
// an error status or a.Body is set (Phase 10 unit 2): the "a non-error
// status returns nil, which leaves the rendered scenario body in place — a
// delay-only or status: 200 attempt is not an error at all" guard this
// closure used to carry itself is now the framework's guarantee.
func faultBody(surface surface) func(scenario.FaultAttempt) []byte {
	return func(a scenario.FaultAttempt) []byte {
		if len(a.Body) > 0 {
			if b, err := json.Marshal(a.Body); err == nil {
				return b
			}
		}
		return errorBody(surface, a.Status, a.Error)
	}
}

// validationErrorMessage maps a finding code onto the FastAPI msg and type a 422
// entry carries. The strings are FastAPI's own vocabulary, so a consumer that
// matches on `type` sees what the real surface sends.
var validationErrorMessage = map[string][2]string{
	CodeModelMissing:    {"Field required", "missing"},
	CodeModelInvalid:    {"Input should be 'sonar', 'sonar-pro', 'sonar-reasoning-pro' or 'sonar-deep-research'", "enum"},
	CodeModelRemoved:    {"Input should be 'sonar', 'sonar-pro', 'sonar-reasoning-pro' or 'sonar-deep-research'", "enum"},
	CodeMessagesMissing: {"Field required", "missing"},
	CodeMessagesEmpty:   {"List should have at least 1 item after validation, not 0", "too_short"},
	CodeMessagesInvalid: {"Input should be a valid list", "list_type"},
	CodeMessageInvalid:  {"Input should be a valid dictionary", "dict_type"},
	CodeRoleInvalid:     {"Input should be 'system', 'user' or 'assistant'", "enum"},
	CodeContentInvalid:  {"Input should be a valid string", "string_type"},
	CodeTemperature:     {"Input should be greater than or equal to 0 and less than or equal to 2", "value_error"},
	CodeTopP:            {"Input should be greater than or equal to 0 and less than or equal to 1", "value_error"},
	CodeMaxTokens:       {"Input should be greater than 0 and less than or equal to 128000", "value_error"},
	CodeSearchMode:      {"Input should be 'web', 'academic' or 'sec'", "enum"},
	CodeReasoningEffort: {"Input should be 'minimal', 'low', 'medium' or 'high'", "enum"},
	CodeRecencyFilter:   {"Input should be 'hour', 'day', 'week', 'month' or 'year'", "enum"},
	CodeStreamMode:      {"Input should be 'full' or 'concise'", "enum"},

	CodeInputMissing:      {"Field required", "missing"},
	CodeInputInvalid:      {"Input should be a valid string or list", "value_error"},
	CodeModelsTooMany:     {"List should have at most 5 items after validation", "too_long"},
	CodeMaxSteps:          {"Input should be greater than or equal to 1", "greater_than_equal"},
	CodeMaxOutputTokens:   {"Input should be greater than 0", "greater_than"},
	CodeStoreInvalid:      {"Input should be a valid boolean", "bool_type"},
	CodeBackgroundInvalid: {"Input should be a valid boolean", "bool_type"},

	provider.CodeBodyNotObject: {"Input should be a valid dictionary", "dict_type"},
	provider.CodeMalformedJSON: {"JSON decode error", "json_invalid"},
}

// validationErrorBody renders the FastAPI 422 body from the request's error
// findings.
//
// Entry order is semantic — the array is what a consumer reads to find out
// which field it got wrong — so it is sorted by the request schema's own field
// declaration order rather than left to the order checks happen to run in. The
// findings arriving here are already totally ordered by Exchange.Findings; this
// re-sort makes the array read the way the vendor's own schema reads, and both
// orderings are deterministic, which is what a golden needs.
func validationErrorBody(findings []provider.Finding, order []string) []byte {
	rank := make(map[string]int, len(order))
	for i, name := range order {
		rank[name] = i
	}

	entries := make([]validationErrorEntry, 0, len(findings))
	fields := make([]string, 0, len(findings))
	for _, f := range findings {
		if f.Severity != provider.SeverityError {
			continue
		}
		entries = append(entries, validationError(f))
		fields = append(fields, f.Field)
	}

	// Sort entry and field in lockstep by sorting an index permutation, so the
	// rank lookup can read the original field path rather than re-deriving it
	// from the rendered loc.
	idx := make([]int, len(entries))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		ra, rb := fieldRank(rank, fields[a]), fieldRank(rank, fields[b])
		if ra != rb {
			return ra - rb
		}
		return strings.Compare(fields[a], fields[b])
	})
	sorted := make([]validationErrorEntry, len(entries))
	for i, at := range idx {
		sorted[i] = entries[at]
	}

	body, err := provider.Render(validationErrorResponse{Detail: sorted}, nil, nil)
	if err != nil {
		return []byte(`{"detail":[]}`)
	}
	return body
}

// fieldRank places a finding's field in the request schema's declaration order.
// An unranked field sorts after every ranked one, so a promoted warning on a
// field this table does not know about lands at the end instead of silently
// displacing a required-field error.
func fieldRank(rank map[string]int, field string) int {
	name := field
	if rest, ok := strings.CutPrefix(field, "body."); ok {
		name, _, _ = strings.Cut(rest, ".")
	}
	if r, ok := rank[name]; ok {
		return r
	}
	return len(rank) + 1
}

// validationError converts one finding into a FastAPI 422 entry. A code with no
// table entry falls back to the finding's own message, which keeps a promoted
// warning representable instead of rendering an empty msg.
func validationError(f provider.Finding) validationErrorEntry {
	entry := validationErrorEntry{Loc: locOf(f.Field), Msg: f.Message, Type: "value_error"}
	if pair, ok := validationErrorMessage[f.Code]; ok {
		entry.Msg, entry.Type = pair[0], pair[1]
	}
	return entry
}

// locOf converts a dotted field path into a FastAPI loc array, turning a numeric
// segment into a JSON integer. "body.messages.0.role" becomes
// ["body","messages",0,"role"] — note that 0 is a number, not the string "0",
// which is why Loc is []any.
func locOf(field string) []any {
	if field == "" {
		return []any{"body"}
	}
	segments := strings.Split(field, ".")
	loc := make([]any, 0, len(segments))
	for _, seg := range segments {
		if n, err := strconv.Atoi(seg); err == nil {
			loc = append(loc, n)
			continue
		}
		loc = append(loc, seg)
	}
	return loc
}
