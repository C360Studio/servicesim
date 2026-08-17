package tavily

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Finding codes this package raises. They are exported because they are the
// assertable half of the journal: a consumer proves it sent the request Tavily
// documents by asserting on these, not by asserting on a status code the
// simulator could have returned for another reason.
//
// The three shared codes are spelled here rather than imported because they are
// wire-visible strings shared by convention across the provider packages, not a
// dependency between them.
const (
	// CodeQueryMissing reports an absent, non-string or empty query, the only
	// member of the schema's required list.
	CodeQueryMissing = "tavily.query.missing"

	// CodeSearchDepthInvalid reports a search_depth outside the four-value enum.
	CodeSearchDepthInvalid = "tavily.search_depth.invalid"

	// CodeTopicInvalid reports a topic outside general|news|finance.
	CodeTopicInvalid = "tavily.topic.invalid"

	// CodeMaxResultsRange reports a max_results outside 0–20.
	CodeMaxResultsRange = "tavily.max_results.range"

	// CodeChunksPerSourceRange reports a chunks_per_source outside 1–3.
	CodeChunksPerSourceRange = "tavily.chunks_per_source.range"

	// CodeIncludeAnswerInvalid reports an include_answer that is neither a
	// boolean nor "basic"/"advanced". Boolean-only validation rejects valid
	// current traffic.
	CodeIncludeAnswerInvalid = "tavily.include_answer.invalid"

	// CodeIncludeRawContentInvalid reports an include_raw_content that is
	// neither a boolean nor "markdown"/"text".
	CodeIncludeRawContentInvalid = "tavily.include_raw_content.invalid"

	// CodeTimeRangeInvalid reports a time_range outside the eight-value enum.
	CodeTimeRangeInvalid = "tavily.time_range.invalid"

	// CodeIncludeDomainsMax reports more than 300 include_domains.
	CodeIncludeDomainsMax = "tavily.include_domains.max"

	// CodeExcludeDomainsMax reports more than 150 exclude_domains. The limit is
	// deliberately asymmetric with include_domains.
	CodeExcludeDomainsMax = "tavily.exclude_domains.max"

	// CodeAPIKeyInBody reports an api_key property in the request body. It is a
	// WARNING and the request authenticates: the vendor documents
	// Authorization: Bearer, and real client code sends the key in the body, so
	// refusing it rejected traffic that works in production. The finding stays
	// so a consumer moving off the body placement can still assert on it —
	// promote it to an error with validation.promote to make that a failure.
	//
	// Its message states presence and nothing else: the finding's entire subject
	// is a credential, and the message reaches the journal, the admin API and
	// the log.
	CodeAPIKeyInBody = "tavily.api_key.in_body"

	// CodeDaysRemoved reports the days parameter, which is absent from the
	// current search schema; recency is time_range, start_date and end_date.
	CodeDaysRemoved = "tavily.days.removed"

	// CodeCountryRequiresGeneralTopic reports a country sent with a non-general
	// topic, which the vendor documents as having no effect.
	CodeCountryRequiresGeneralTopic = "tavily.country.requires_general_topic"

	// CodeAuthWrongHeader reports an x-api-key on Tavily, which is seen,
	// journaled and not accepted: only Authorization: Bearer and the body's
	// api_key authenticate.
	CodeAuthWrongHeader = "tavily.auth.wrong_header"

	// CodeProjectionInvalid reports a scenario projection body this package
	// cannot decode. Validator raises it at startup; the handler raises it only
	// if a scenario reached a request path without being validated.
	CodeProjectionInvalid = "tavily.projection.invalid"

	// CodeProjectionUnresolved reports a projection whose source reference names
	// no declared source, seen at request time rather than at startup.
	CodeProjectionUnresolved = "tavily.projection.unresolved"

	// CodeContentType reports a request that is not application/json. It is a
	// warning: no vendor documents a 415 here, so returning one would invent
	// behaviour, and a scenario that wants the strict form promotes the code.
	CodeContentType = "request.content_type"

	// CodeUnknownField reports a request property this build does not model.
	// Warning, not error: being stricter than the vendor would fail a consumer
	// against the simulator that passes against production.
	CodeUnknownField = "request.unknown_field"

	// CodeAuthMissing reports that no accepted credential placement carried a
	// value.
	CodeAuthMissing = "auth.missing"

	// CodeAuthMismatch reports a credential that does not match the scenario's
	// expected key, or any credential at all under auth mode "reject".
	CodeAuthMismatch = "auth.mismatch"

	// CodeAuthWrongPlacement reports a credential in an accepted header carrying
	// an authentication scheme Tavily does not document.
	CodeAuthWrongPlacement = "auth.wrong_placement"
)

// PlacementBodyAPIKey names the credential placement that is not a header: the
// request body's api_key property. It is what the journal's auth observation
// records as Header for a request whose credential arrived that way, and what a
// scenario writes in providers.tavily.auth.headers to keep the body placement
// accepted while narrowing the header set.
//
// The colon is deliberate. A header name cannot contain one (RFC 9110 §5.1), so
// this value can never collide with a real placement, and a consumer asserting
// Auth.Header == "authorization" still proves its adapter sent the header the
// vendor documents rather than merely sending *something*.
const PlacementBodyAPIKey = "body:api_key"

// Documented request defaults and the topic the response's published_date is
// gated on.
const (
	// DefaultSearchDepth is the documented default for search_depth.
	DefaultSearchDepth = "basic"

	// DefaultTopic is the documented default for topic.
	DefaultTopic = "general"

	// DefaultMaxResults is the documented default for max_results. It truncates
	// a scenario that declares more results, because that is what the real API
	// does to a request that did not ask for more.
	DefaultMaxResults = 5

	// DefaultChunksPerSource is the documented default for chunks_per_source.
	DefaultChunksPerSource = 3

	// topicNews is the topic that makes results[].published_date appear. The
	// field is documented in prose for topic=news only and is not in the
	// response schema at all.
	topicNews = "news"
)

// Documented validation bounds.
const (
	maxResultsMin      = 0
	maxResultsMax      = 20
	chunksPerSourceMin = 1
	chunksPerSourceMax = 3
	includeDomainsMax  = 300
	excludeDomainsMax  = 150
)

// knownFields is every property the live search schema documents, plus the two
// this package recognises in order to flag them. A property outside this set
// raises request.unknown_field.
var knownFields = map[string]bool{
	"query": true, "search_depth": true, "chunks_per_source": true, "max_results": true,
	"topic": true, "time_range": true, "start_date": true, "end_date": true,
	"include_answer": true, "include_raw_content": true, "include_images": true,
	"include_image_descriptions": true, "include_favicon": true, "include_domains": true,
	"exclude_domains": true, "country": true, "auto_parameters": true, "exact_match": true,
	"include_usage": true, "safe_search": true,

	// Recognised in order to be flagged, not in order to be accepted.
	"api_key": true, "days": true,
}

// searchRequest is a POST /search request after validation, with every
// documented default applied. A request that failed validation still produces
// one: the handler decides whether to render it, and a half-parsed request is
// easier to reason about than a nil.
type searchRequest struct {
	Query           string
	SearchDepth     string
	ChunksPerSource int
	MaxResults      int
	Topic           string
	TimeRange       string
	StartDate       string
	EndDate         string

	// IncludeAnswer and IncludeRawContent are oneOf[boolean, string] in the
	// schema, so each carries both the switch and the mode it was switched on
	// with.
	IncludeAnswer     option
	IncludeRawContent option

	IncludeImages            bool
	IncludeImageDescriptions bool
	IncludeFavicon           bool
	IncludeDomains           []string
	ExcludeDomains           []string
	Country                  string
	AutoParameters           bool
	ExactMatch               bool
	IncludeUsage             bool
	SafeSearch               bool
}

// option is a request switch that is either a boolean or one of a small set of
// mode strings. Enabled is what rendering gates on; Mode is what a scenario or
// a consumer test can distinguish "basic" from "advanced" by.
type option struct {
	Enabled bool
	Mode    string
}

// parseSearchRequest validates the request body and returns it with defaults
// applied, recording one finding per problem. It never returns nil.
func parseSearchRequest(x *provider.Exchange) *searchRequest {
	req := &searchRequest{
		SearchDepth:     DefaultSearchDepth,
		Topic:           DefaultTopic,
		MaxResults:      DefaultMaxResults,
		ChunksPerSource: DefaultChunksPerSource,
	}

	checkContentType(x)
	if x.Body == nil {
		// A body that was malformed or not an object already carries its own
		// finding from the shared lifecycle; adding "query is required" on top
		// would report a consequence as though it were a second cause.
		if !x.HasFinding(provider.CodeMalformedJSON) && !x.HasFinding(provider.CodeBodyNotObject) &&
			!x.HasFinding(provider.CodeBodyTooLarge) && !x.HasFinding(provider.CodeBodyRead) {
			x.Fail(CodeQueryMissing, "query", "Missing query. 'query' is a required string.")
		}
		return req
	}

	warnUnknownFields(x)
	warnRemovedAndCredentialFields(x)

	req.Query = parseQuery(x)
	req.SearchDepth = parseEnum(x, "search_depth", DefaultSearchDepth, SearchDepths, CodeSearchDepthInvalid)
	req.Topic = parseEnum(x, "topic", DefaultTopic, Topics, CodeTopicInvalid)
	req.TimeRange = parseEnum(x, "time_range", "", TimeRanges, CodeTimeRangeInvalid)
	req.MaxResults = parseInt(x, "max_results", DefaultMaxResults, maxResultsMin, maxResultsMax, CodeMaxResultsRange)
	req.ChunksPerSource = parseInt(x, "chunks_per_source", DefaultChunksPerSource,
		chunksPerSourceMin, chunksPerSourceMax, CodeChunksPerSourceRange)

	req.IncludeAnswer = parseOption(x, "include_answer", IncludeAnswerModes, CodeIncludeAnswerInvalid)
	req.IncludeRawContent = parseOption(x, "include_raw_content", IncludeRawContentModes,
		CodeIncludeRawContentInvalid)

	req.IncludeImages = parseBool(x, "include_images")
	req.IncludeImageDescriptions = parseBool(x, "include_image_descriptions")
	req.IncludeFavicon = parseBool(x, "include_favicon")
	req.AutoParameters = parseBool(x, "auto_parameters")
	req.ExactMatch = parseBool(x, "exact_match")
	req.IncludeUsage = parseBool(x, "include_usage")
	req.SafeSearch = parseBool(x, "safe_search")

	req.StartDate = parseString(x, "start_date")
	req.EndDate = parseString(x, "end_date")
	req.Country = parseString(x, "country")

	req.IncludeDomains = parseDomains(x, "include_domains", includeDomainsMax, CodeIncludeDomainsMax)
	req.ExcludeDomains = parseDomains(x, "exclude_domains", excludeDomainsMax, CodeExcludeDomainsMax)

	if req.Country != "" && req.Topic != DefaultTopic {
		x.Warn(CodeCountryRequiresGeneralTopic, "country",
			"country applies only when topic is %q; it has no effect for topic %q", DefaultTopic, req.Topic)
	}
	return req
}

// checkContentType records the warning §6.2 specifies for a body that does not
// declare JSON. It says nothing about a request with no body at all: an empty
// body has no content type to be wrong about.
func checkContentType(x *provider.Exchange) {
	if len(x.Raw) == 0 {
		return
	}
	if !x.HasJSONContentType() {
		x.Warn(CodeContentType, "content-type",
			"Content-Type is not application/json")
	}
}

// warnUnknownFields flags properties this build does not model.
//
// Keys are walked in sorted order, never in Go map order: the findings list
// feeds the journal and, for one of the sibling providers, an error body whose
// array order is semantic, so an unsorted walk would make a two-unknown-field
// request differ run to run.
func warnUnknownFields(x *provider.Exchange) {
	for _, key := range slices.Sorted(maps.Keys(x.Body)) {
		if !knownFields[key] {
			x.Warn(CodeUnknownField, key, "unknown request field %q", key)
		}
	}
}

// warnRemovedAndCredentialFields flags the two properties that are recognised
// only in order to be reported. days is a /search-only field — see
// warnAPIKeyInBody for the credential half, which /research shares.
func warnRemovedAndCredentialFields(x *provider.Exchange) {
	warnAPIKeyInBody(x)
	if x.Has("days") {
		x.Warn(CodeDaysRemoved, "days",
			"days was removed from the search API; recency is time_range, start_date and end_date")
	}
}

// warnAPIKeyInBody flags a body-placed api_key with the warning-severity
// CodeAPIKeyInBody finding. Shared by /search's warnRemovedAndCredentialFields
// and /research's validateResearchCreate: contracts/tavily/README.md documents
// the identical placement and finding on both routes ("Same as POST /search"
// in the research route table), so the message text must not fork between
// them.
func warnAPIKeyInBody(x *provider.Exchange) {
	if x.Has("api_key") {
		// A warning, not an error: the request authenticates on it. The finding
		// is what a consumer asserts on to prove which placement its adapter
		// used, so accepting the placement must not make it invisible.
		//
		// States presence and nothing else. Not the value, not a prefix of it,
		// not its length: this message reaches the journal, /__admin/requests
		// and the process log.
		x.Warn(CodeAPIKeyInBody, "api_key",
			"api_key was sent in the request body; the vendor documents Authorization: Bearer")
	}
}

// parseQuery reads the one required property.
func parseQuery(x *provider.Exchange) string {
	value, ok := x.String("query")
	if !ok || strings.TrimSpace(value) == "" {
		x.Fail(CodeQueryMissing, "query", "Missing query. 'query' is a required string.")
		return value
	}
	return value
}

// parseEnum reads an optional string property constrained to a set of values.
func parseEnum(x *provider.Exchange, field, fallback string, allowed []string, code string) string {
	if !x.Has(field) {
		return fallback
	}
	value, ok := x.String(field)
	if !ok || !slices.Contains(allowed, value) {
		x.Fail(code, field, "Invalid %s. Must be %s.", field, quoteAlternatives(allowed))
		return fallback
	}
	return value
}

// parseInt reads an optional integer property constrained to an inclusive range.
func parseInt(x *provider.Exchange, field string, fallback, low, high int, code string) int {
	if !x.Has(field) {
		return fallback
	}
	value, ok := x.Number(field)
	if !ok || value != math.Trunc(value) || value < float64(low) || value > float64(high) {
		x.Fail(code, field, "Invalid %s. Must be an integer between %d and %d.", field, low, high)
		return fallback
	}
	return int(value)
}

// parseOption reads a property that is either a boolean or one of a small set
// of mode strings — include_answer and include_raw_content, both of which the
// plan document wrongly assumes are boolean-only.
func parseOption(x *provider.Exchange, field string, modes []string, code string) option {
	if !x.Has(field) {
		return option{}
	}
	if value, ok := x.Bool(field); ok {
		return option{Enabled: value}
	}
	if value, ok := x.String(field); ok && slices.Contains(modes, value) {
		return option{Enabled: true, Mode: value}
	}
	x.Fail(code, field, "Invalid %s. Must be a boolean or %s.", field, quoteAlternatives(modes))
	return option{}
}

// parseBool reads an optional boolean property.
func parseBool(x *provider.Exchange, field string) bool {
	if !x.Has(field) {
		return false
	}
	value, ok := x.Bool(field)
	if !ok {
		x.Fail(invalidCode(field), field, "Invalid %s. Must be a boolean.", field)
		return false
	}
	return value
}

// parseString reads an optional string property.
func parseString(x *provider.Exchange, field string) string {
	if !x.Has(field) {
		return ""
	}
	value, ok := x.String(field)
	if !ok {
		x.Fail(invalidCode(field), field, "Invalid %s. Must be a string.", field)
		return ""
	}
	return value
}

// parseDomains reads an optional array of domain strings with a length bound.
func parseDomains(x *provider.Exchange, field string, limit int, code string) []string {
	if !x.Has(field) {
		return nil
	}
	raw, ok := x.Body[field].([]any)
	if !ok {
		x.Fail(invalidCode(field), field, "Invalid %s. Must be an array of strings.", field)
		return nil
	}
	out := make([]string, 0, len(raw))
	for i, item := range raw {
		value, isString := item.(string)
		if !isString {
			x.Fail(invalidCode(field), fmt.Sprintf("%s[%d]", field, i),
				"Invalid %s. Must be an array of strings.", field)
			return nil
		}
		out = append(out, value)
	}
	if len(out) > limit {
		x.Fail(code, field, "Invalid %s. At most %d domains may be supplied.", field, limit)
	}
	return out
}

// invalidCode is the finding code for a documented property of the wrong JSON
// type. §6.3 names a code per field for the enum and range checks; a type error
// on any other documented property follows the same shape.
func invalidCode(field string) string {
	return string(Name) + "." + field + ".invalid"
}

// describeAcceptedPlacements renders a route's accepted credential placements
// for a finding message, e.g. "Authorization: Bearer or a body api_key". It
// exists so checkAuth's messages describe what the SERVING route actually
// accepts (acceptedPlacements' own precedence: scenario auth.headers, then
// Route.Credentials, then the package default) rather than a fixed phrase that
// is wrong for a route narrower than the default, such as the research GET
// polls (researchPollPlacements).
func describeAcceptedPlacements(accepted []string) string {
	parts := make([]string, 0, len(accepted))
	for _, p := range accepted {
		switch p {
		case provider.PlacementAuthorization:
			parts = append(parts, "Authorization: Bearer")
		case PlacementBodyAPIKey:
			parts = append(parts, "a body api_key")
		default:
			parts = append(parts, p)
		}
	}
	switch len(parts) {
	case 0:
		return "a credential"
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
	}
}

// quoteAlternatives renders an enum as the vendor's own error text does:
// 'a', 'b' or 'c'.
func quoteAlternatives(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, "'"+v+"'")
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	default:
		return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
	}
}

// checkAuth applies §6.4's rule for Tavily: on the default route, either the
// documented Authorization: Bearer header or a body api_key authenticates; a
// narrower route (its own Route.Credentials, e.g. the research GET polls' and
// /extract's Bearer-only placement sets) or a
// scenario's AuthPolicy can shrink or replace that set. An x-api-key is seen
// and journaled but never authenticates. Every finding message this raises is
// derived from the route's own accepted set (describeAcceptedPlacements), not
// a fixed phrase, so a narrowed route never tells a caller a placement it will
// still 401.
//
// Two placements authenticate rather than one because the vendor's
// documentation and observed client code disagree: the OpenAPI security scheme
// is bearerAuth, and real adapters send the key as a body property on POSTs.
// Refusing the body placement returned 401 for requests that succeed against
// the real Tavily API, which sends a consumer hunting a bug in correct code —
// the one failure mode a simulator must never have. Accepting it costs nothing a
// consumer needs, because which placement arrived is still recorded: the
// journal's auth observation names it and tavily.api_key.in_body flags it.
func checkAuth(x *provider.Exchange) {
	policy := x.AuthPolicy()
	accepted := acceptedPlacements(x, policy)
	presented := presentedCredentials(x, accepted)

	if policy.Mode == scenario.AuthReject {
		// Deliberately a mismatch rather than a missing credential: something
		// was or was not presented, and neither can ever match under this mode.
		x.Fail(CodeAuthMismatch, "authorization", "the scenario rejects every credential")
		return
	}
	if len(presented) == 0 {
		if policy.Mode != scenario.AuthOptional {
			// Derived from the route's own accepted set — not a hardcoded
			// "Bearer or a body api_key" — so a Bearer-only route like the
			// research GET polls never tells a caller a placement it will
			// still 401.
			x.Fail(CodeAuthMissing, "authorization",
				"%s is required", describeAcceptedPlacements(accepted))
		}
		return
	}
	for _, cred := range presented {
		if cred.Header == "authorization" && cred.Scheme != "Bearer" {
			x.Warn(CodeAuthWrongPlacement, "authorization",
				"Authorization does not carry the documented Bearer scheme")
		}
	}
	// Any accepted placement carrying the expected key authenticates the
	// request. A consumer that sends the key in both places has presented the
	// right credential once, not the wrong one once.
	if policy.ExpectKey != "" && !slices.ContainsFunc(presented, func(c provider.Credential) bool {
		return c.Value == policy.ExpectKey
	}) {
		// The value is never quoted; the journal holds a fingerprint of it.
		x.Fail(CodeAuthMismatch, "authorization", "the presented credential is not the expected key")
	}
}

// presentedCredentials returns every accepted placement that carried a value —
// headers in httpx order first, the body last — and records what was seen but
// not accepted.
//
// It also fills in the journal's auth observation for a body-placed credential,
// which the shared lifecycle cannot: provider.Handle observes headers only, so a
// request authenticated by its body would otherwise journal auth.present false
// while being served, telling a consumer its adapter sent no credential at all.
// A header credential that was already observed keeps the slot — it is the
// documented placement — and the body placement stays visible as its own
// finding, so a request carrying both records both.
func presentedCredentials(x *provider.Exchange, accepted []string) []provider.Credential {
	var presented []provider.Credential
	for _, cred := range x.Credentials() {
		if slices.Contains(accepted, cred.Header) {
			presented = append(presented, cred)
			continue
		}
		if cred.Header == "x-api-key" {
			x.Warn(CodeAuthWrongHeader, "x-api-key",
				"x-api-key is not accepted; only %s authenticates", describeAcceptedPlacements(accepted))
		}
	}

	body, ok := bodyCredential(x)
	if !ok {
		return presented
	}
	// The body placement is journaled whether or not a header was already
	// observed. Handle's header scan cannot see it, so without this a request
	// sending BOTH a Bearer header and a body api_key would record only the
	// header — and "my client sends exactly one credential" is the assertion
	// that needs both to be visible.
	x.ObserveCredential(body)
	if slices.Contains(accepted, PlacementBodyAPIKey) {
		presented = append(presented, body)
	}
	return presented
}

// bodyCredential returns the credential presented as the body's api_key
// property, and whether one was presented at all.
//
// An api_key that is absent, not a string, or blank is not a credential. That
// matches httpx.ExtractCredentials, where a placement with no value after it is
// absent rather than empty: counting `"api_key": ""` as presented would put
// auth.missing out of reach for a request that sent nothing.
func bodyCredential(x *provider.Exchange) (provider.Credential, bool) {
	value, ok := x.String("api_key")
	if !ok {
		return provider.Credential{}, false
	}
	if value = strings.TrimSpace(value); value == "" {
		return provider.Credential{}, false
	}
	return provider.Credential{Header: PlacementBodyAPIKey, Value: value}, true
}

// defaultPlacements are the placements a Tavily route accepts when it declares
// no Credentials of its own: the Authorization header the vendor documents, and
// the body api_key property real client code sends.
var defaultPlacements = []string{provider.PlacementAuthorization, PlacementBodyAPIKey}

// acceptedPlacements returns the credential placements that authenticate,
// lower-cased, applying the shared precedence: a scenario's auth.headers first,
// then the serving route's own Credentials, then the package default.
//
// A scenario's auth.headers replaces the set outright, and may name
// PlacementBodyAPIKey ("body:api_key") alongside header names. Listing only
// header names is how a consumer that has moved off the body placement asserts
// it: a body-placed key then fails with auth.missing rather than being served.
func acceptedPlacements(x *provider.Exchange, policy scenario.AuthPolicy) []string {
	return x.AcceptedPlacements(policy, defaultPlacements)
}
