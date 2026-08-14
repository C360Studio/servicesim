package perplexity

import (
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/c360studio/servicesim/internal/httpx"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Finding codes shared with the other provider packages. They are declared here
// as well as raised here because a consumer asserting on them through
// testkit.AssertFindings needs a name to write, and a string literal in a
// consumer's test is a typo waiting to happen.
const (
	// CodeContentType is raised when a request body arrives without a JSON
	// content type. It is a warning by default: no vendor documents a 415 for
	// this, so returning one would invent vendor behaviour. Promote it in a
	// scenario's validation policy to make it a rejection.
	CodeContentType = "request.content_type"

	// CodeUnknownField is raised, as a warning, for a top-level request property
	// this build does not model. Being stricter than the vendor is its own
	// failure mode: a consumer sending a legitimate field Servicesim has not
	// modelled yet would fail here and pass against production.
	CodeUnknownField = "request.unknown_field"

	// CodeAuthMissing is raised when no accepted credential placement carried a
	// value and the scenario's auth mode is required.
	CodeAuthMissing = "auth.missing"

	// CodeAuthWrongPlacement is raised, as a warning, when a credential arrived
	// in a placement this API does not read. Perplexity authenticates with
	// Authorization: Bearer and nothing else.
	CodeAuthWrongPlacement = "auth.wrong_placement"

	// CodeAuthMismatch is raised when the presented credential does not match the
	// scenario's expect_key. Neither the expected nor the presented value is ever
	// quoted in the message.
	CodeAuthMismatch = "auth.mismatch"

	// CodeAuthRejected is raised when the scenario's auth mode is reject, which
	// always answers 401 whatever the request presented.
	CodeAuthRejected = "auth.rejected"
)

// Sonar request finding codes.
const (
	CodeModelMissing    = "perplexity.model.missing"
	CodeModelInvalid    = "perplexity.model.invalid"
	CodeModelRemoved    = "perplexity.model.removed"
	CodeMessagesMissing = "perplexity.messages.missing"
	CodeMessagesEmpty   = "perplexity.messages.empty"
	CodeMessagesInvalid = "perplexity.messages.invalid"
	CodeMessageInvalid  = "perplexity.messages.item.invalid"
	CodeRoleInvalid     = "perplexity.messages.role.invalid"
	CodeContentInvalid  = "perplexity.messages.content.invalid"
	CodeTemperature     = "perplexity.temperature.range"
	CodeTopP            = "perplexity.top_p.range"
	CodeMaxTokens       = "perplexity.max_tokens.range"
	CodeSearchMode      = "perplexity.search_mode.invalid"
	CodeReasoningEffort = "perplexity.reasoning_effort.invalid"
	CodeRecencyFilter   = "perplexity.search_recency_filter.invalid"

	// CodeStreamUnimplemented is raised, as a warning, for stream: true. Streaming
	// is a plan non-goal; the request still receives the ordinary non-streaming
	// body. It warns rather than staying silent because a consumer must not be
	// able to believe it exercised a path it never touched.
	CodeStreamUnimplemented = "perplexity.stream.unimplemented"
)

// Codes raised while serving a request rather than while validating one.
const (
	// CodeProjectionInvalid reports a projection body that failed to decode at
	// request time. Startup validation should have caught it, so reaching this at
	// request time means the scenario was not validated — it answers 500, not
	// 422, because the fault is the simulator's configuration and not the
	// consumer's request.
	CodeProjectionInvalid = "perplexity.projection.invalid"

	// CodeProjectionUnresolved reports a source reference that did not resolve
	// while rendering. It is a warning: the response still renders, with the
	// unresolved entry carrying only the reference it names.
	CodeProjectionUnresolved = "perplexity.projection.unresolved_source"

	// CodeRenderFailed reports a response body that could not be marshalled,
	// which in practice means a scenario's extra_fields carried a value
	// encoding/json cannot represent.
	CodeRenderFailed = "perplexity.render.failed"
)

// Models is the Sonar model enum. "sonar-reasoning" without the -pro suffix was
// removed from the API on 2025-12-15 and is rejected rather than accepted, so it
// is deliberately absent here and named by [ModelRemoved].
var Models = []string{"sonar", "sonar-pro", "sonar-deep-research", "sonar-reasoning-pro"}

// ModelRemoved is the model identifier withdrawn on 2025-12-15. It gets its own
// finding code so a test can assert on the removal specifically rather than on a
// generic enum failure.
const ModelRemoved = "sonar-reasoning"

// Sonar request enums.
var (
	// Roles is the chat message role enum.
	Roles = []string{"system", "user", "assistant"}

	// SearchModes is the search_mode enum.
	SearchModes = []string{"web", "academic", "sec"}

	// ReasoningEfforts is the reasoning_effort enum.
	ReasoningEfforts = []string{"minimal", "low", "medium", "high"}

	// RecencyFilters is the search_recency_filter enum.
	RecencyFilters = []string{"hour", "day", "week", "month", "year"}

	// StreamModes is the stream_mode enum.
	StreamModes = []string{"full", "concise"}
)

// MaxTokensLimit is the upper bound accepted for max_tokens.
const MaxTokensLimit = 128000

// sonarFields are the Sonar request properties this build models, in the order
// the specification declares them. The order is load-bearing twice over: it is
// the order the 422 detail array is sorted into, and it is the set an unmodelled
// property is measured against.
var sonarFields = []string{
	"max_tokens", "model", "stream", "stop", "temperature", "top_p",
	"response_format", "messages", "web_search_options", "search_mode",
	"return_images", "return_related_questions", "enable_search_classifier",
	"disable_search", "search_domain_filter", "search_language_filter",
	"search_recency_filter", "search_after_date_filter", "search_before_date_filter",
	"last_updated_before_filter", "last_updated_after_filter", "image_format_filter",
	"image_domain_filter", "stream_mode", "reasoning_effort", "language_preference",
}

// bearerOnly is the credential placement Perplexity accepts. The specification
// declares HTTPBearer and nothing else, so an x-api-key is observed, flagged and
// not accepted.
var bearerOnly = []string{"authorization"}

// checkContentType records the shared content-type warning. An absent body is
// not checked: an unmatched route has no body by definition, and a route that
// requires one reports the fields it is missing, which is a better message than
// a header complaint.
func checkContentType(x *provider.Exchange) {
	if len(x.Raw) == 0 {
		return
	}
	if ct := x.Request.Header.Get("Content-Type"); !httpx.IsJSONContentType(ct) {
		x.Warn(CodeContentType, "", "content type %q is not JSON; this API reads application/json", ct)
	}
}

// checkAuth applies the scenario's authentication policy.
//
// It never interpolates a credential value, not even truncated, into a finding
// message: these messages reach the journal, the admin API and the log, and a
// finding whose subject is a credential must state presence and nothing else.
func checkAuth(x *provider.Exchange, e *scenario.ProviderEntry) {
	var policy *scenario.AuthPolicy
	if e != nil {
		policy = e.Auth
	}

	mode := scenario.AuthRequired
	allowed := bearerOnly
	if policy != nil {
		if policy.Mode != "" {
			mode = policy.Mode
		}
		if len(policy.Headers) > 0 {
			allowed = make([]string, 0, len(policy.Headers))
			for _, h := range policy.Headers {
				allowed = append(allowed, strings.ToLower(h))
			}
		}
	}

	if mode == scenario.AuthReject {
		x.Fail(CodeAuthRejected, "", "this scenario rejects every credential")
		return
	}

	creds := httpx.ExtractCredentials(x.Request)
	var accepted *httpx.Credential
	for i := range creds {
		if slices.Contains(allowed, creds[i].Header) {
			accepted = &creds[i]
			break
		}
		x.Warn(CodeAuthWrongPlacement, creds[i].Header,
			"a credential was presented in %s; this API authenticates with Authorization: Bearer",
			creds[i].Header)
	}

	if accepted == nil {
		if mode == scenario.AuthOptional {
			return
		}
		x.Fail(CodeAuthMissing, "", "no Authorization: Bearer credential was presented")
		return
	}
	if policy != nil && policy.ExpectKey != "" && accepted.Value != policy.ExpectKey {
		x.Fail(CodeAuthMismatch, "", "the presented credential does not match the scenario's expected key")
	}
}

// bodyUnusable reports that the shared lifecycle already rejected the body, so
// per-field validation would only pile "field required" onto a request whose
// real problem is that it never parsed. FastAPI reports the decode error alone,
// and so does this.
func bodyUnusable(x *provider.Exchange) bool {
	return x.HasFinding(provider.CodeMalformedJSON) ||
		x.HasFinding(provider.CodeBodyNotObject) ||
		x.HasFinding(provider.CodeBodyTooLarge) ||
		x.HasFinding(provider.CodeBodyRead)
}

// checkUnknownFields warns for every top-level property this build does not
// model. Keys are walked in sorted order rather than in map order: the messages
// are generated here and the 422 body is built from them, so Go's randomised map
// iteration would otherwise reach the wire.
func checkUnknownFields(x *provider.Exchange, known []string) {
	if x.Body == nil {
		return
	}
	for _, key := range slices.Sorted(maps.Keys(x.Body)) {
		if slices.Contains(known, key) {
			continue
		}
		x.Warn(CodeUnknownField, "body."+key, "request property %q is not modelled by this build", key)
	}
}

// validateSonarRequest applies §6.3's Sonar checks and returns the model the
// response should echo, which is the request's own model when it names a valid
// one.
func validateSonarRequest(x *provider.Exchange) string {
	if bodyUnusable(x) {
		return ""
	}
	checkUnknownFields(x, sonarFields)

	model := validateModel(x)
	validateMessages(x)
	validateNumericRange(x, "temperature", CodeTemperature, 0, 2)
	validateNumericRange(x, "top_p", CodeTopP, 0, 1)
	validateMaxTokens(x)
	validateEnum(x, "search_mode", CodeSearchMode, SearchModes)
	validateEnum(x, "reasoning_effort", CodeReasoningEffort, ReasoningEfforts)
	validateEnum(x, "search_recency_filter", CodeRecencyFilter, RecencyFilters)

	if stream, ok := x.Bool("stream"); ok && stream {
		x.Warn(CodeStreamUnimplemented, "body.stream",
			"streaming is not simulated; this request receives the ordinary non-streaming body")
	}
	return model
}

// validateModel checks the required model property and returns it when it names
// a currently served model.
func validateModel(x *provider.Exchange) string {
	if !x.Has("model") {
		x.Fail(CodeModelMissing, "body.model", "model is required")
		return ""
	}
	model, ok := x.String("model")
	if !ok {
		x.Fail(CodeModelInvalid, "body.model", "model must be a string")
		return ""
	}
	switch {
	case model == ModelRemoved:
		x.Fail(CodeModelRemoved, "body.model",
			"model %q was removed from the API on 2025-12-15; use sonar-reasoning-pro", model)
		return ""
	case !slices.Contains(Models, model):
		x.Fail(CodeModelInvalid, "body.model", "model %q is not one of %s", model, strings.Join(Models, ", "))
		return ""
	}
	return model
}

// validateMessages checks the required messages array and every element of it.
func validateMessages(x *provider.Exchange) {
	if !x.Has("messages") {
		x.Fail(CodeMessagesMissing, "body.messages", "messages is required")
		return
	}
	messages, ok := x.Body["messages"].([]any)
	if !ok {
		x.Fail(CodeMessagesInvalid, "body.messages", "messages must be an array")
		return
	}
	if len(messages) == 0 {
		x.Fail(CodeMessagesEmpty, "body.messages", "messages must contain at least one message")
		return
	}
	for i, raw := range messages {
		at := "body.messages." + strconv.Itoa(i)
		message, ok := raw.(map[string]any)
		if !ok {
			x.Fail(CodeMessageInvalid, at, "message %d must be an object", i)
			continue
		}
		role, ok := message["role"].(string)
		if !ok || !slices.Contains(Roles, role) {
			x.Fail(CodeRoleInvalid, at+".role", "message %d has a role that is not one of %s",
				i, strings.Join(Roles, ", "))
		}
		switch message["content"].(type) {
		case string, []any:
		default:
			x.Fail(CodeContentInvalid, at+".content",
				"message %d content must be a string or an array of content chunks", i)
		}
	}
}

// validateNumericRange checks an optional numeric property against an inclusive
// range. A present-but-not-a-number value fails the same check: the vendor's
// validator would reject it too, and a separate code per type error would be
// noise.
func validateNumericRange(x *provider.Exchange, key, code string, lo, hi float64) {
	if !x.Has(key) {
		return
	}
	v, ok := x.Number(key)
	if !ok || v < lo || v > hi {
		x.Fail(code, "body."+key, "%s must be a number between %g and %g", key, lo, hi)
	}
}

// validateMaxTokens checks max_tokens, which is bounded below by zero exclusive
// rather than inclusive and so does not fit validateNumericRange.
func validateMaxTokens(x *provider.Exchange) {
	if !x.Has("max_tokens") {
		return
	}
	v, ok := x.Number("max_tokens")
	if !ok || v <= 0 || v > MaxTokensLimit || v != float64(int64(v)) {
		x.Fail(CodeMaxTokens, "body.max_tokens",
			"max_tokens must be an integer greater than 0 and at most %d", MaxTokensLimit)
	}
}

// validateEnum checks an optional string property against an enum.
func validateEnum(x *provider.Exchange, key, code string, members []string) {
	if !x.Has(key) {
		return
	}
	v, ok := x.String(key)
	if !ok || !slices.Contains(members, v) {
		x.Fail(code, "body."+key, "%s must be one of %s", key, strings.Join(members, ", "))
	}
}

// errorStatus maps a request's error findings onto the status the surface
// answers with. Authentication outranks validation: a request that presented no
// credential has not earned a field-by-field critique of its body.
func errorStatus(findings []journal.Finding) int {
	authFailed := false
	tooLarge := false
	for _, f := range findings {
		if f.Severity != journal.SeverityError {
			continue
		}
		switch f.Code {
		case CodeAuthMissing, CodeAuthMismatch, CodeAuthRejected:
			authFailed = true
		case provider.CodeBodyTooLarge:
			tooLarge = true
		}
	}
	switch {
	case authFailed:
		return http.StatusUnauthorized
	case tooLarge:
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusUnprocessableEntity
	}
}
