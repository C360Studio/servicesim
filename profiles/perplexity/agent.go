package perplexity

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Agent request finding codes.
const (
	CodeInputMissing  = "perplexity.input.missing"
	CodeInputInvalid  = "perplexity.input.invalid"
	CodeModelsTooMany = "perplexity.agent.models.max"
	CodeModelFormat   = "perplexity.agent.model.format"

	// CodeModelRequired is raised when a request names no model, models or preset:
	// ResponsesRequest.model is "Required if neither models nor preset is
	// provided".
	CodeModelRequired = "perplexity.agent.model.required"

	// CodeModelsInvalid is raised when models is not a non-empty array of strings
	// (minItems 1, items of type string). The upper bound is [CodeModelsTooMany].
	CodeModelsInvalid = "perplexity.agent.models.invalid"

	// CodeMaxOutputTokensRequired is raised when an anthropic/* model is selected
	// without max_output_tokens. Its message is the one the specification quotes.
	CodeMaxOutputTokensRequired = "perplexity.agent.max_output_tokens.required"

	// CodeProfileInvalid is raised when profile is not a valid ProfileReference
	// or is combined with preset.
	CodeProfileInvalid = "perplexity.agent.profile.invalid"

	// CodeInputItemInvalid is raised when an element of an input array is not an
	// object carrying one of the InputItem discriminator values in type.
	CodeInputItemInvalid = "perplexity.input.item.invalid"

	// CodeAgentStreamInvalid is raised when stream is present and not a boolean.
	// It sorts under perplexity.stream. with the other streaming codes, for the
	// reason [CodeAgentStreamUnsupported] gives.
	CodeAgentStreamInvalid = "perplexity.stream.agent_invalid"

	CodeMaxSteps          = "perplexity.agent.max_steps.range"
	CodeMaxOutputTokens   = "perplexity.agent.max_output_tokens.range"
	CodeStoreInvalid      = "perplexity.agent.store.invalid"
	CodeBackgroundInvalid = "perplexity.agent.background.invalid"

	// CodeAgentStreamUnsupported is raised for stream: true under an entry
	// whose effective streaming policy is not stream — a warning under the
	// warn default, an error (400, ErrorInfo) under reject, mirroring
	// [CodeStreamUnimplemented]'s two-severity use on the Sonar surface. It
	// does NOT fire under a stream-policy entry: a request that will
	// actually receive the scripted GrammarTyped sequence must not also
	// carry a warning promising it will not.
	//
	// The code was renamed from perplexity.agent.stream.unsupported
	// (docs/design/streaming.md §9): the surface qualifier used to sort
	// before the subject, which broke a consumer's perplexity.stream. prefix
	// filter. Landing GrammarTyped (Phase 5 unit 3) is what gives this
	// surface the warn/reject/stream switch Sonar already had — before that,
	// the warning fired unconditionally.
	CodeAgentStreamUnsupported = "perplexity.stream.agent_unsupported"

	// CodeAgentBackgroundUnscripted is raised, as an error, for
	// background: true against a scenario whose perplexity_agent entry
	// declares no `background:` block — or no entry at all. The request is
	// refused with the Agent 404 before anything is claimed and no job is
	// minted: a run the scenario scripts no retrieve for could only be
	// answered with invented snapshots (ruling 4 on issue #6).
	CodeAgentBackgroundUnscripted = "perplexity.agent.background.unscripted"

	// CodeAgentBackgroundStream is raised, as an error, for background: true
	// together with stream: true. The vendor documents streaming a background
	// run; this build does not simulate it, and falling through to a
	// synchronous stream would be invented behaviour, so the request fails
	// validation (400) before anything is claimed (ruling 4 on issue #6). A
	// scenario whose stream policy is reject refuses the request first, with
	// [CodeAgentStreamUnsupported].
	CodeAgentBackgroundStream = "perplexity.agent.background.stream"

	// CodeAgentBackgroundUnstored is raised, as a warning, for background: true
	// with store: false. The request receives the queued snapshot under the
	// identifier a synchronous call would derive, and no job is kept: the
	// specification hides a store: false response from retrieve, so every
	// later GET /v1/agent/{id} of it is a 404.
	CodeAgentBackgroundUnstored = "perplexity.agent.background.unstored"

	// CodeStreamDoneIgnored is raised, as a warning, when a turn declares
	// terminal.omit_done on the Agent surface. GrammarTyped never writes a
	// [DONE] sentinel — it is a chat-completions concept only
	// (docs/design/streaming.md §7) — so the key has nothing to omit; this
	// says so rather than silently ignoring it. It is load-time, not
	// per-request: the grammar is fixed by the provider entry (§9), so
	// whether the key is meaningful never depends on what a specific request
	// asks for.
	CodeStreamDoneIgnored = "perplexity.stream.done_ignored"
)

// maxModelChain is the longest model fallback chain the Agent API accepts.
const maxModelChain = 5

// agentFields are the Agent request properties this build models, in the order
// the specification declares them. As with sonarFields the order is what a
// validation failure is sorted by: Sonar's 422 detail array, and the one finding
// an Agent 400 names.
var agentFields = []string{
	"input", "background", "instructions", "language_preference",
	"max_output_tokens", "max_steps", "model", "models", "preset", "profile",
	"previous_response_id", "reasoning", "response_format", "store", "stream",
	"tools", "skills", "temperature", "top_p",
}

// agentStatus is ResponsesResponse.status. The zero value renders as
// statusCompleted, so a minimal scenario projects a successful response.
type agentStatus string

// The Status enum members.
const (
	statusCompleted  agentStatus = "completed"
	statusFailed     agentStatus = "failed"
	statusIncomplete agentStatus = "incomplete"
	statusInProgress agentStatus = "in_progress"
	statusQueued     agentStatus = "queued"
	statusCancelled  agentStatus = "cancelled"
)

// agentStatuses is the Status enum, for validation and for tests that want to
// walk every member.
var agentStatuses = []agentStatus{
	statusCompleted, statusFailed, statusIncomplete,
	statusInProgress, statusQueued, statusCancelled,
}

// perplexityAgent projects the shared corpus into an Agent API response.
//
// The Agent envelope shares no fields with the Sonar envelope: Sonar returns
// choices[] with a message, the Agent API returns an ordered output[] trace. The
// two are rendered by separate functions from the same canonical sources, which
// is the point of the scenario model.
type perplexityAgent struct {
	// ResponseID overrides the derived "resp_<32 hex>" identifier.
	ResponseID string `yaml:"response_id,omitempty"`

	// MessageID overrides the derived "msg_<32 hex>" identifier of the message
	// output item.
	MessageID string `yaml:"message_id,omitempty"`

	// Model is echoed as responsesResponse.model. Agent model IDs are
	// "provider/model" strings; when empty the model the request selected is
	// echoed (see validateAgentModel), which is never empty for a request that
	// passed validation.
	Model string `yaml:"model,omitempty"`

	// CreatedAt overrides the derived Unix timestamp. When zero it is
	// Scenario.BaseTime().Unix(), never time.Now().
	CreatedAt int64 `yaml:"created_at,omitempty"`

	// Status defaults to statusCompleted. Setting it to "failed" renders Error
	// and is how a consumer's terminal-state handling is exercised without an
	// HTTP-level fault — a consumer that only branches on the status code misses
	// it entirely.
	Status agentStatus `yaml:"status,omitempty"`

	// Answer becomes the text of the single message output item.
	Answer string `yaml:"answer,omitempty"`

	// Queries populates the search_results item's queries — the searches the
	// agent reports having run. It is independent of SearchResults so that a
	// scenario can project "searched but found nothing".
	Queries []string `yaml:"queries,omitempty"`

	// SearchResults become the search_results output item. Ordering is the
	// scenario's; results[].id is the 1-based index within the item, rendered as
	// a JSON integer.
	SearchResults []agentResult `yaml:"search_results,omitempty"`

	// Annotations attach url_citation spans to the answer text.
	Annotations []agentAnnotation `yaml:"annotations,omitempty"`

	// Error renders responsesResponse.error. It is required when Status is
	// statusFailed; validation rejects a failed status with no error.
	Error *agentError `yaml:"error,omitempty"`

	// Stream selects the behaviour for a request carrying "stream": true —
	// the Agent surface's own when_requested/deltas/terminal script, decoded
	// exactly as PerplexityProjection.Stream is
	// (docs/design/streaming.md §7: "landing GrammarTyped is what gives the
	// Agent entry a stream: key at all"). Defaults to StreamWarn:
	// [CodeAgentStreamUnsupported] plus the ordinary non-streaming body,
	// exactly as this surface has always behaved. StreamReject turns that
	// into this surface's own error envelope naming body.stream.
	// StreamServe serves the GrammarTyped sequence renderAgentStream builds
	// instead — see that function's doc comment for the exact event
	// sequence. Only the first turn's policy is read, for the same ordering
	// reason PerplexityProjection.Stream's doc comment gives.
	Stream scenario.StreamScript `yaml:"stream,omitempty"`

	Usage       *agentUsage          `yaml:"usage,omitempty"`
	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}

// agentResult is one entry of the Agent API's search_results item.
//
// The addendum types this field []scenario.SourceRef. This is a superset of that
// shape — the scalar shorthand and the bare `- source: x` mapping both still
// decode — and it exists because the Agent search result carries a snippet, a
// date and a last_updated that the canonical corpus cannot always supply in the
// form a fixture needs: Source.PublishedAt renders RFC 3339 with millisecond
// precision, and this surface's goldens carry plain calendar dates.
type agentResult struct {
	scenario.SourceRef `yaml:",inline"`

	Snippet     string `yaml:"snippet,omitempty"`
	Date        string `yaml:"date,omitempty"`
	LastUpdated string `yaml:"last_updated,omitempty"`

	// SourceType is the web/attachment discriminator. It is named and keyed
	// exactly as perplexityResult.SourceType is, and for the same reason: the
	// inlined SourceRef already owns the YAML key "source".
	SourceType string `yaml:"source_type,omitempty"`
}

// rawAgentResult is the decode target for the mapping form. It restates the
// fields for the reason rawPerplexityResult does: a defined-type copy of a
// struct embedding scenario.SourceRef still promotes SourceRef.UnmarshalYAML,
// and yaml.v3 would hand that method the whole result mapping.
type rawAgentResult struct {
	Source      string `yaml:"source"`
	Snippet     string `yaml:"snippet,omitempty"`
	Date        string `yaml:"date,omitempty"`
	LastUpdated string `yaml:"last_updated,omitempty"`
	SourceType  string `yaml:"source_type,omitempty"`
}

// UnmarshalYAML accepts the scalar shorthand for a source reference.
func (r *agentResult) UnmarshalYAML(value *yaml.Node) error {
	var raw rawAgentResult
	if err := scenario.DecodeRefOrMapping(value, &r.SourceRef, &raw); err != nil {
		return err
	}
	if value == nil || value.Kind != yaml.MappingNode {
		return nil
	}
	r.SourceRef.Ref = raw.Source
	r.Snippet = raw.Snippet
	r.Date = raw.Date
	r.LastUpdated = raw.LastUpdated
	r.SourceType = raw.SourceType
	return nil
}

// agentAnnotation is a url_citation span over the answer text.
//
// StartIndex and EndIndex are byte offsets into Answer. Validation rejects
// offsets outside the answer or with End <= Start, because an out-of-range span
// is a fixture bug that would otherwise surface as a consumer panic.
type agentAnnotation struct {
	Source     scenario.SourceRef `yaml:"source"`
	StartIndex int                `yaml:"start_index"`
	EndIndex   int                `yaml:"end_index"`
}

// agentError is responsesResponse.error, the published errorInfo shape.
type agentError struct {
	Message string `yaml:"message"`
	Code    string `yaml:"code,omitempty"`
	Type    string `yaml:"type,omitempty"`
}

// agentUsage is responsesUsage. The field names differ from Sonar's usage:
// input_tokens and output_tokens here, prompt_tokens and completion_tokens
// there. Do not share a Go type between them.
type agentUsage struct {
	InputTokens  int        `yaml:"input_tokens,omitempty"`
	OutputTokens int        `yaml:"output_tokens,omitempty"`
	TotalTokens  int        `yaml:"total_tokens,omitempty"` // derived when zero
	Cost         *agentCost `yaml:"cost,omitempty"`
}

// agentCost is responsesCost. Currency, InputCost, OutputCost and TotalCost are
// required by the specification; the cache and tool fields are optional and are
// omitted when zero rather than emitted as 0.
type agentCost struct {
	Currency          string  `yaml:"currency,omitempty"` // defaults to USD
	InputCost         float64 `yaml:"input_cost,omitempty"`
	OutputCost        float64 `yaml:"output_cost,omitempty"`
	TotalCost         float64 `yaml:"total_cost,omitempty"` // derived when zero
	CacheCreationCost float64 `yaml:"cache_creation_cost,omitempty"`
	CacheReadCost     float64 `yaml:"cache_read_cost,omitempty"`
	ToolCallsCost     float64 `yaml:"tool_calls_cost,omitempty"`
}

// validateAgentRequest applies the Agent API's request checks.
//
// background must be a boolean; background: true together with stream: true
// fails here, with [CodeAgentBackgroundStream], because a streamed background
// run is not simulated. Everything else about a background request — whether
// the scenario can answer it — is handleAgentBackground's, after validation.
// stream is no longer always deferred —
// policy is the entry's effective streaming policy (agentStreamPolicy(entry)),
// the same call rejectAgentStream already makes before turn selection.
// Threading it through, rather than re-deriving it here, is what lets the
// stream: true check below be conditional on it: reject is unreachable at
// this point (rejectAgentStream already returned), warn fires exactly as it
// always has, and stream does NOT fire it, because a request that will
// actually receive the scripted GrammarTyped sequence must not also carry a
// warning promising the opposite. This mirrors validateSonarRequest exactly.
func validateAgentRequest(x *provider.Exchange, policy scenario.StreamPolicy) string {
	if bodyUnusable(x) {
		return ""
	}
	checkUnknownFields(x, agentFields)

	if !x.Has("input") {
		x.Fail(CodeInputMissing, "body.input", "input is required")
	} else {
		switch input := x.Body["input"].(type) {
		case string:
		case []any:
			validateInputItems(x, input)
		default:
			x.Fail(CodeInputInvalid, "body.input", "input must be a string or an array of input items")
		}
	}

	profileID, profileOK := validateAgentProfile(x)
	model, effective := validateAgentModel(x, profileID, profileOK)
	requireMaxOutputTokensForAnthropic(x, effective)
	validateNumericRange(x, "temperature", CodeTemperature, 0, 2)
	validateNumericRange(x, "top_p", CodeTopP, 0, 1)

	// max_steps is an integer from 1 to 100
	// (#/components/schemas/ResponsesRequest/properties/max_steps).
	if x.Has("max_steps") {
		if v, ok := x.Number("max_steps"); !ok || v < 1 || v > maxStepsLimit || v != math.Trunc(v) {
			x.Fail(CodeMaxSteps, "body.max_steps",
				"max_steps must be an integer from 1 to %d", maxStepsLimit)
		}
	}
	if x.Has("max_output_tokens") {
		if v, ok := x.Number("max_output_tokens"); !ok || v <= 0 {
			x.Fail(CodeMaxOutputTokens, "body.max_output_tokens",
				"max_output_tokens must be an integer greater than 0")
		}
	}
	if x.Has("store") {
		if _, ok := x.Bool("store"); !ok {
			x.Fail(CodeStoreInvalid, "body.store", "store must be a boolean")
		}
	}

	if x.Has("background") {
		if _, ok := x.Bool("background"); !ok {
			x.Fail(CodeBackgroundInvalid, "body.background", "background must be a boolean")
		}
	}
	if x.Has("stream") {
		stream, ok := x.Bool("stream")
		switch {
		case !ok:
			x.Fail(CodeAgentStreamInvalid, "body.stream", "stream must be a boolean")
		case stream && wantsBackground(x):
			// Ahead of the warning below, which promises an ordinary
			// non-streaming body this request will not receive.
			x.Fail(CodeAgentBackgroundStream, "body.stream",
				"background: true with stream: true is not simulated; send stream: false, or omit it, to "+
					"receive the queued response and retrieve it with GET /v1/agent/{id}")
		case stream && policy != scenario.StreamServe:
			x.Warn(CodeAgentStreamUnsupported, "body.stream",
				"streaming is not simulated; this request receives the ordinary non-streaming body")
		}
	}
	return model
}

// inputItemTypes are the InputItem discriminator mapping keys
// (#/components/schemas/InputItem/discriminator/mapping): the only values an
// input item's type may take.
var inputItemTypes = []string{"message", "function_call", "function_call_output"}

// maxStepsLimit is max_steps' maximum (ResponsesRequest.max_steps).
const maxStepsLimit = 100

// validateInputItems checks each element of an input array is an object whose
// type is one of the InputItem discriminator values. It checks nothing else: the
// per-variant required properties (role and content for a message, and so on)
// are not enforced, so a malformed message body still passes.
func validateInputItems(x *provider.Exchange, items []any) {
	for i, raw := range items {
		at := "body.input." + strconv.Itoa(i)
		item, ok := raw.(map[string]any)
		if !ok {
			x.Fail(CodeInputItemInvalid, at, "input item %d must be an object", i)
			continue
		}
		typ, ok := item["type"].(string)
		if !ok || !slices.Contains(inputItemTypes, typ) {
			x.Fail(CodeInputItemInvalid, at+".type", "input item %d type must be one of %s",
				i, strings.Join(inputItemTypes, ", "))
		}
	}
}

// anthropicModelPrefix marks the models for which max_output_tokens is required.
const anthropicModelPrefix = "anthropic/"

// validateAgentModel checks the model-selecting properties, model, models,
// preset and a valid profile (profileID, from validateAgentProfile), and returns
// the model to echo as responsesResponse.model together with the models the
// request may run on.
//
// Selection follows #/components/schemas/ResponsesRequest/properties: model is
// "Required if neither models nor preset is provided", and models "takes
// precedence over single model field". A request naming none of them is a
// failure, as is a models chain that is empty, is not an array of strings, or
// exceeds the cap. An empty string selects nothing, whether it is model or an
// entry of models — the specification says nothing about it, and treating it as
// selected would render "model": "".
//
// Two choices here are the specification's silence filled in, not its words.
// INFERENCE: a valid profile counts as a selection, like a preset. The model
// text names only models and preset, but profile is described as a "Saved,
// versioned configuration to run with" that "Cannot be combined with preset",
// which makes the two alternatives, and nothing says a profile does not supply
// the model; rejecting a profile-only request would make an accepted field
// unusable. Likewise the preset text, "Required if model is not provided",
// read literally conflicts with a models-only request; the lenient reading is
// taken.
//
// SIMULATOR-POLICY: the echoed model is the first non-empty entry of models,
// else model, else, for a preset-only request, "preset/<name>", else, for a
// profile-only request, "profile/<id>". The specification does not say what a
// preset or profile request echoes (the configuration's own model is not
// knowable here), so those forms are deterministic, non-empty and
// provider/model shaped. A scenario's own model: still overrides all of it.
//
// A model that is not in provider/model form is a warning rather than an error:
// the Agent API is a multi-provider router whose model set is not enumerated
// anywhere Servicesim can verify, and rejecting an unknown-but-well-formed model
// would mean rejecting valid traffic the moment a router adds a provider.
func validateAgentModel(x *provider.Exchange, profileID string, profileOK bool) (echo string, effective []string) {
	model, modelOK := optionalString(x, "model")
	preset, presetOK := optionalString(x, "preset")
	chain, chainOK := validateModelChain(x)

	if model != "" && !strings.Contains(model, "/") {
		x.Warn(CodeModelFormat, "body.model",
			"model %q is not in provider/model form, for example openai/gpt-5", model)
	}

	if model == "" && preset == "" && len(chain) == 0 && profileID == "" &&
		modelOK && presetOK && chainOK && profileOK {
		x.Fail(CodeModelRequired, "body.model", "model is required if neither models nor preset is provided")
	}

	switch {
	case len(chain) > 0:
		return chain[0], chain
	case model != "":
		return model, []string{model}
	case preset != "":
		return "preset/" + preset, nil
	case profileID != "":
		return "profile/" + profileID, nil
	}
	return "", nil
}

// optionalString reads an optional string property. It reports ok=false, after
// recording a failure, only when the property is present and not a string.
func optionalString(x *provider.Exchange, key string) (value string, ok bool) {
	if !x.Has(key) {
		return "", true
	}
	value, ok = x.String(key)
	if !ok {
		x.Fail(CodeModelInvalid, "body."+key, "%s must be a string", key)
	}
	return value, ok
}

// validateModelChain checks models: an array of 1 to maxModelChain strings. It
// returns the chain without its empty entries, so an empty string selects nothing
// here exactly as it does for model; the chain is nil when models is absent,
// invalid or holds only empty strings. ok=false, after recording a failure, means
// models is present and invalid.
func validateModelChain(x *provider.Exchange) (chain []string, ok bool) {
	if !x.Has("models") {
		return nil, true
	}
	raw, isArray := x.Body["models"].([]any)
	if !isArray {
		x.Fail(CodeModelsInvalid, "body.models", "models must be an array of model IDs")
		return nil, false
	}
	if len(raw) == 0 {
		x.Fail(CodeModelsInvalid, "body.models", "models must contain at least one model")
		return nil, false
	}
	if len(raw) > maxModelChain {
		x.Fail(CodeModelsTooMany, "body.models",
			"models accepts at most %d entries, got %d", maxModelChain, len(raw))
	}
	chain = make([]string, 0, len(raw))
	for i, item := range raw {
		id, isString := item.(string)
		if !isString {
			x.Fail(CodeModelsInvalid, "body.models."+strconv.Itoa(i), "models entry %d must be a string", i)
			return nil, false
		}
		if id != "" {
			chain = append(chain, id)
		}
	}
	return chain, true
}

// profileFields are the properties of ProfileReference, which is
// additionalProperties: false.
var profileFields = []string{"type", "id", "version"}

// maxProfileIDLength is ProfileReference.id's maxLength.
const maxProfileIDLength = 128

// validateAgentProfile checks the request's profile property against
// #/components/schemas/ProfileReference and the one rule the specification
// attaches to the property itself, that it "Cannot be combined with preset".
// It validates shape only: what a saved profile would configure is not
// simulated.
//
// It returns the profile's id and ok=true when profile is absent or valid; id is
// empty unless profile is present and valid. ok=false, after recording a failure,
// means profile is present and invalid. A valid profile counts as a model
// selection (see validateAgentModel).
func validateAgentProfile(x *provider.Exchange) (id string, ok bool) {
	if !x.Has("profile") {
		return "", true
	}
	valid := true
	fail := func(field, format string, args ...any) {
		valid = false
		x.Fail(CodeProfileInvalid, field, format, args...)
	}

	profile, isObject := x.Object("profile")
	if !isObject {
		fail("body.profile", "profile must be an object")
		return "", false
	}
	if x.Has("preset") {
		fail("body.profile", "profile cannot be combined with preset")
	}
	// Sorted, so the findings and therefore the 400 message never depend on
	// Go's randomised map iteration.
	for _, key := range slices.Sorted(maps.Keys(profile)) {
		if !slices.Contains(profileFields, key) {
			fail("body.profile."+key, "profile has no property %q", key)
		}
	}

	switch typ, present := profile["type"]; {
	case !present:
		fail("body.profile.type", "profile.type is required")
	case typ != "custom":
		fail("body.profile.type", "profile.type must be custom")
	}

	switch rawID, present := profile["id"]; {
	case !present:
		fail("body.profile.id", "profile.id is required")
	default:
		s, isString := rawID.(string)
		if n := utf8.RuneCountInString(s); !isString || n < 1 || n > maxProfileIDLength {
			fail("body.profile.id", "profile.id must be a string of 1 to %d characters", maxProfileIDLength)
		} else {
			id = s
		}
	}

	if version, present := profile["version"]; present {
		if _, isString := version.(string); !isString {
			fail("body.profile.version", "profile.version must be a string")
		}
	}
	if !valid {
		return "", false
	}
	return id, true
}

// requireMaxOutputTokensForAnthropic fails a request that may run on an
// anthropic/* model without max_output_tokens. The specification quotes the
// status and the message: "If omitted for an Anthropic model, the API returns
// HTTP 400 with: validation failed: max_output_tokens is required when using
// Anthropic models." The 400 and the "validation failed: " prefix come from
// validationResponse; this supplies the rest.
//
// A models chain counts if ANY entry is anthropic/*. The specification does not
// say whether a chain is checked up front or only when it falls through to such
// a model; checking every entry is the strict reading, and adding
// max_output_tokens to a request is harmless on any model.
func requireMaxOutputTokensForAnthropic(x *provider.Exchange, effective []string) {
	if x.Has("max_output_tokens") {
		return
	}
	for _, id := range effective {
		if strings.HasPrefix(id, anthropicModelPrefix) {
			x.Fail(CodeMaxOutputTokensRequired, "body.max_output_tokens",
				"max_output_tokens is required when using Anthropic models.")
			return
		}
	}
}

// renderAgentIdentity computes the id, message id, created timestamp and
// status shared by the JSON body and the GrammarTyped SSE stream for one
// turn — the Agent analogue of renderSonarIdentity (render.go) — so both
// transports render identical values everywhere docs/design/streaming.md §7
// says they must agree.
//
// The attempt index is claimed whether or not the scenario pins the
// identifiers, so the journal's attempt number never depends on whether a
// fixture happened to declare them. Both identifiers hang off idParts, so
// both move with the call index and neither moves with anything else. The
// "agent" and "message" discriminators are what keep resp_ distinct from the
// Sonar completion id and msg_ distinct from resp_ within one call.
func renderAgentIdentity(x *provider.Exchange, p *perplexityAgent, callIndex int) (id, messageID string, created int64, status agentStatus) {
	id = p.ResponseID
	if id == "" {
		id = "resp_" + provider.Hex32(idParts(x, callIndex, "agent")...)
	}
	messageID = p.MessageID
	if messageID == "" {
		messageID = "msg_" + provider.Hex32(idParts(x, callIndex, "agent", "message")...)
	}
	created = p.CreatedAt
	if created == 0 {
		created = x.Deps.Scenario.BaseTime().Unix()
	}
	status = p.Status
	if status == "" {
		status = statusCompleted
	}
	return id, messageID, created, status
}

// agentResponse builds the responsesResponse struct for p from already-computed
// identity fields and an already-built output trace. It is shared, unchanged,
// by renderAgent's non-streaming body and renderAgentStream's
// response.completed event, so the two transports render the identical
// usage/cost/output[] for one turn rather than the stream re-implementing this
// object (docs/design/streaming.md §7's "one mechanism serves both" rule).
func agentResponse(p *perplexityAgent, id string, created int64, model string, status agentStatus, output []outputItem) responsesResponse {
	resp := responsesResponse{
		ID:        id,
		Object:    objectResponse,
		Model:     model,
		CreatedAt: created,
		Status:    string(status),
		Output:    output,
		Usage:     renderAgentUsage(p.Usage),
	}
	if p.Error != nil {
		resp.Error = &errorInfo{Code: p.Error.Code, Message: p.Error.Message, Type: p.Error.Type}
	}
	return resp
}

// renderAgent projects p into the Agent API wire envelope.
//
// The order of output[] is fixed and deterministic: search_results first, then
// message. That mirrors the execution order the trace represents — the agent
// searches, then it answers — and gives consumers a stable index. A scenario
// cannot reorder it.
func renderAgent(x *provider.Exchange, p *perplexityAgent, requestModel string) ([]byte, error) {
	callIndex := x.CallIndex()
	id, messageID, created, status := renderAgentIdentity(x, p, callIndex)
	model := firstNonEmpty(p.Model, requestModel)
	output := renderAgentOutput(p, messageID, status)
	resp := agentResponse(p, id, created, model, status, output)
	return provider.Render(resp, p.ExtraFields, nil)
}

// renderAgentStream projects p into the GrammarTyped SSE sequence for the
// Agent surface (docs/design/streaming.md §7 "Responses / Agent";
// contracts/perplexity/README.md "Streaming (SSE)" → "Responses / Agent"):
//
//  1. response.created — the responsesResponse in its initial in_progress
//     state (empty output, zero usage).
//  2. response.output_item.added — the message item, in progress, with no
//     content yet.
//  3. one response.output_text.delta per scripted delta.
//  4. response.output_text.done — the aggregate text.
//  5. response.output_item.done — the completed message item.
//  6. response.completed — the terminal frame, whose response is the SAME
//     responsesResponse agentResponse builds for the non-streaming route:
//     never re-implemented for the stream.
//
// response.in_progress is deliberately NOT emitted. §7's own worked example
// shows only response.created → response.output_text.delta →
// response.completed, and — per the design's own "where a block and the
// prose disagree, the prose wins; where the code disagrees with a block, the
// code wins" rule — this build keeps that minimal sequence rather than
// inventing a fourth envelope-only event the design's own illustration never
// shows (P5U3 spec item 2's explicit instruction on this point). The
// reasoning.* event family has no scenario vocabulary and is never emitted
// (contracts/perplexity/README.md "What Servicesim simulates").
//
// A turn whose Status is failed or cancelled produces no message output item
// at all (renderAgentOutput's own rule) and therefore has nothing for steps
// 2-5 above to attach to; this renderer then emits only two frames.
//
// For failed, the second frame is response.failed — the specification's own
// failure event, carrying the scenario's error at top level — and it is
// terminal: no response.completed follows. Which events surround it, and that
// it ends the stream, are Servicesim's policy, not the specification's
// (docs: contracts README "Streaming (SSE)"). The event has no response object,
// so it carries no usage and extra_fields are not rendered on it.
//
// For cancelled, EventType has no response.cancelled, so the second frame is
// response.completed carrying status cancelled; the same is true of an
// incomplete turn, which keeps its message item. Neither is a statement about
// what the vendor sends.
func renderAgentStream(x *provider.Exchange, p *perplexityAgent, requestModel string) (*provider.Stream, error) {
	callIndex := x.CallIndex()
	id, messageID, created, status := renderAgentIdentity(x, p, callIndex)
	model := firstNonEmpty(p.Model, requestModel)
	output := renderAgentOutput(p, messageID, status)

	outputIndex := -1
	var msgItem messageOutput
	for i, item := range output {
		if m, ok := item.(messageOutput); ok {
			msgItem, outputIndex = m, i
			break
		}
	}

	// pace resolves one event's gap the same way renderSonarStream's does:
	// its own override when set (nonzero — StreamDelta.Pace's doc comment
	// explains why zero means "no override"), falling back to the script's
	// own default otherwise.
	pace := func(override scenario.Duration) time.Duration {
		if override != 0 {
			return override.Duration()
		}
		return p.Stream.Pace.Duration()
	}
	var seq int64
	nextSeq := func() int64 {
		n := seq
		seq++
		return n
	}

	events := make([]provider.SSEEvent, 0, len(p.Stream.Deltas)+5)

	initial := responsesResponse{
		ID: id, Object: objectResponse, Model: model, CreatedAt: created,
		Status: string(statusInProgress), Output: []outputItem{}, Usage: renderAgentUsage(nil),
	}
	initialBytes, err := provider.Render(initial, nil, nil)
	if err != nil {
		return nil, err
	}
	createdData, err := provider.Render(responseCreatedEvent{
		Type: eventResponseCreated, SequenceNumber: nextSeq(), Response: json.RawMessage(initialBytes),
	}, nil, nil)
	if err != nil {
		return nil, err
	}
	events = append(events, provider.SSEEvent{Name: eventResponseCreated, Data: createdData, Pace: pace(0)})

	if status == statusFailed {
		// The failure event has no response object, so there is no usage on the
		// wire to lift into provider.Stream.Usage: it stays unset, as it does
		// under terminal.omit_usage.
		var info errorInfo
		if p.Error != nil {
			info = errorInfo{Code: p.Error.Code, Message: p.Error.Message, Type: p.Error.Type}
		}
		failedData, err := provider.Render(responseFailedEvent{
			Type: eventResponseFailed, SequenceNumber: nextSeq(), Error: info,
		}, nil, nil)
		if err != nil {
			return nil, err
		}
		var terminalPaceOverride scenario.Duration
		if p.Stream.Terminal != nil {
			terminalPaceOverride = p.Stream.Terminal.Pace
		}
		events = append(events, provider.SSEEvent{
			Name: eventResponseFailed, Data: failedData, Terminal: true, Pace: pace(terminalPaceOverride),
		})
		return &provider.Stream{Grammar: provider.GrammarTyped, Chunks: provider.EncodeSSE(events)}, nil
	}

	var aggregate strings.Builder
	if outputIndex >= 0 {
		addedData, err := provider.Render(outputItemAddedEvent{
			Type: eventOutputItemAdded, SequenceNumber: nextSeq(),
			Item: messageOutput{
				Type: outputTypeMessage, ID: messageID, Role: roleAssistant,
				Status: string(statusInProgress), Content: []contentPart{},
			},
			OutputIndex: outputIndex,
		}, nil, nil)
		if err != nil {
			return nil, err
		}
		events = append(events, provider.SSEEvent{Name: eventOutputItemAdded, Data: addedData})

		for _, d := range p.Stream.Deltas {
			aggregate.WriteString(d.Text)
			data, err := provider.Render(textDeltaEvent{
				Type: eventOutputTextDelta, SequenceNumber: nextSeq(),
				ItemID: messageID, OutputIndex: outputIndex, ContentIndex: 0, Delta: d.Text,
			}, nil, nil)
			if err != nil {
				return nil, err
			}
			events = append(events, provider.SSEEvent{Name: eventOutputTextDelta, Data: data, Pace: pace(d.Pace)})
		}

		textDoneData, err := provider.Render(textDoneEvent{
			Type: eventOutputTextDone, SequenceNumber: nextSeq(),
			ItemID: messageID, OutputIndex: outputIndex, ContentIndex: 0, Text: aggregate.String(),
		}, nil, nil)
		if err != nil {
			return nil, err
		}
		events = append(events, provider.SSEEvent{Name: eventOutputTextDone, Data: textDoneData})

		itemDoneData, err := provider.Render(outputItemDoneEvent{
			Type: eventOutputItemDone, SequenceNumber: nextSeq(), Item: msgItem, OutputIndex: outputIndex,
		}, nil, nil)
		if err != nil {
			return nil, err
		}
		events = append(events, provider.SSEEvent{Name: eventOutputItemDone, Data: itemDoneData})
	}

	final := agentResponse(p, id, created, model, status, output)
	// terminal.omit_usage nils usage inside response.completed's response
	// object (P5U3 spec item 3), by omission rather than a pointer field on
	// responsesResponse: see responseCompletedEvent's doc comment for why.
	omitUsage := p.Stream.Terminal != nil && p.Stream.Terminal.OmitUsage
	var omit []string
	if omitUsage {
		// provider.Render's omit path always round-trips through a map, so the
		// response object comes out with alphabetised keys at every nesting
		// level when this fires — every OTHER response.completed frame stays
		// in struct order. Deterministic either way, and the same divergence
		// renderSonarStream documents for an extra-fields terminal frame;
		// noted here only so a captured transcript's mixed ordering does not
		// read as a bug later.
		omit = []string{"usage"}
	}
	finalBytes, err := provider.Render(final, p.ExtraFields, omit)
	if err != nil {
		return nil, err
	}
	var terminalPaceOverride scenario.Duration
	if p.Stream.Terminal != nil {
		terminalPaceOverride = p.Stream.Terminal.Pace
	}
	completedData, err := provider.Render(responseCompletedEvent{
		Type: eventResponseCompleted, SequenceNumber: nextSeq(), Response: json.RawMessage(finalBytes),
	}, nil, nil)
	if err != nil {
		return nil, err
	}
	events = append(events, provider.SSEEvent{
		Name: eventResponseCompleted, Data: completedData, Terminal: true, Pace: pace(terminalPaceOverride),
	})

	stream := &provider.Stream{Grammar: provider.GrammarTyped, Chunks: provider.EncodeSSE(events)}
	if !omitUsage {
		usageBytes, err := json.Marshal(final.Usage)
		if err != nil {
			return nil, err
		}
		stream.Usage = json.RawMessage(usageBytes)
		costTotal := final.Usage.Cost.TotalCost
		stream.CostTotal = &costTotal
	}
	return stream, nil
}

// renderAgentOutput builds the ordered trace.
//
// The search_results item appears when the scenario declares queries or results,
// so that "searched and found nothing" is expressible as queries with an empty
// results array. The message item appears unless the response failed: a failed
// response reports its reason in error and carries no answer, which is what the
// failed golden shows.
func renderAgentOutput(p *perplexityAgent, messageID string, status agentStatus) []outputItem {
	out := make([]outputItem, 0, 2)

	if len(p.Queries) > 0 || len(p.SearchResults) > 0 {
		results := make([]agentSearchResult, 0, len(p.SearchResults))
		for i := range p.SearchResults {
			r := &p.SearchResults[i]
			src := scenario.Render(r.SourceRef)
			snippet := r.Snippet
			if snippet == "" {
				snippet = src.FirstSnippet()
			}
			sourceType := r.SourceType
			if sourceType == "" {
				sourceType = sourceTypeWeb
			}
			results = append(results, agentSearchResult{
				// The specification types this an integer, and it is the 1-based
				// index within the item — not the source ID and not a URL.
				ID:          i + 1,
				Title:       src.Title,
				URL:         src.URL,
				Snippet:     snippet,
				Date:        r.Date,
				LastUpdated: r.LastUpdated,
				Source:      sourceType,
			})
		}
		out = append(out, searchResultsOutput{
			Type:    outputTypeSearchResults,
			Queries: p.Queries,
			Results: results,
		})
	}

	if status == statusFailed || status == statusCancelled {
		return out
	}

	annotations := make([]annotation, 0, len(p.Annotations))
	for _, a := range p.Annotations {
		src := scenario.Render(a.Source)
		annotations = append(annotations, annotation{
			Type:       annotationTypeURLCitation,
			StartIndex: a.StartIndex,
			EndIndex:   a.EndIndex,
			Title:      src.Title,
			URL:        src.URL,
		})
	}
	return append(out, messageOutput{
		Type:   outputTypeMessage,
		ID:     messageID,
		Role:   roleAssistant,
		Status: string(status),
		Content: []contentPart{{
			Type:        contentPartTypeOutputText,
			Text:        p.Answer,
			Annotations: annotations,
		}},
	})
}

// renderAgentUsage projects the usage object, deriving the totals and the
// currency when the scenario leaves them at their zero values. Its cost is
// always set, as the synchronous path has always rendered it.
func renderAgentUsage(u *agentUsage) *responsesUsage {
	out := &responsesUsage{Cost: &responsesCost{Currency: currencyUSD}}
	if u == nil {
		return out
	}
	out.InputTokens = u.InputTokens
	out.OutputTokens = u.OutputTokens
	out.TotalTokens = u.TotalTokens
	if out.TotalTokens == 0 {
		out.TotalTokens = u.InputTokens + u.OutputTokens
	}
	if c := u.Cost; c != nil {
		out.Cost = &responsesCost{
			Currency:          c.Currency,
			InputCost:         c.InputCost,
			OutputCost:        c.OutputCost,
			CacheCreationCost: c.CacheCreationCost,
			CacheReadCost:     c.CacheReadCost,
			ToolCallsCost:     c.ToolCallsCost,
			TotalCost:         c.TotalCost,
		}
		if out.Cost.Currency == "" {
			out.Cost.Currency = currencyUSD
		}
		if out.Cost.TotalCost == 0 {
			out.Cost.TotalCost = c.InputCost + c.OutputCost
		}
	}
	return out
}

// validateAgentProjection checks one decoded Agent projection at startup.
func validateAgentProjection(path string, p *perplexityAgent) []scenario.Finding {
	var findings []scenario.Finding
	add := func(code, at, message string) {
		findings = append(findings, scenario.Finding{
			Severity: scenario.SeverityError, Code: code, Path: at, Message: message,
		})
	}

	if p.Status != "" && !slices.Contains(agentStatuses, p.Status) {
		add("perplexity.agent.status.invalid", path+".status",
			"status "+strconv.Quote(string(p.Status))+" is not a member of the Status enum")
	}
	if p.Status == statusFailed && p.Error == nil {
		add("perplexity.agent.error.missing", path+".error",
			"a failed status must declare an error; the specification requires ErrorInfo.message")
	}
	if p.Error != nil && p.Error.Message == "" {
		add("perplexity.agent.error.message", path+".error.message",
			"error.message is required by the specification")
	}
	// SearchSource is ["web"] on the Agent surface (#/components/schemas/SearchSource);
	// "attachment" belongs to Sonar's ApiPublicSearchResult, a different schema.
	for i := range p.SearchResults {
		if st := p.SearchResults[i].SourceType; st != "" && st != sourceTypeWeb {
			add("perplexity.source_type.invalid",
				path+".search_results["+strconv.Itoa(i)+"].source_type",
				"source_type "+strconv.Quote(st)+" is not web, the only SearchSource value")
		}
	}
	// Currency is ["USD"] (#/components/schemas/Currency).
	if u := p.Usage; u != nil && u.Cost != nil && u.Cost.Currency != "" && u.Cost.Currency != currencyUSD {
		add("perplexity.agent.currency.invalid", path+".usage.cost.currency",
			"currency "+strconv.Quote(u.Cost.Currency)+" is not "+currencyUSD+", the only Currency value")
	}
	// An annotation span outside the answer is a fixture bug that would otherwise
	// reach a consumer as an index into a string that is too short.
	for i, a := range p.Annotations {
		at := path + ".annotations[" + strconv.Itoa(i) + "]"
		switch {
		case a.StartIndex < 0 || a.EndIndex <= a.StartIndex:
			add("perplexity.agent.annotation.range", at,
				"end_index must be greater than start_index and both must be non-negative")
		case a.EndIndex > len(p.Answer):
			add("perplexity.agent.annotation.range", at,
				"end_index "+strconv.Itoa(a.EndIndex)+" is past the end of the answer, which is "+
					strconv.Itoa(len(p.Answer))+" bytes")
		}
	}
	return findings
}
