package exa

import (
	"maps"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/internal/httpx"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Finding codes this package raises. They are unexported because a consumer
// asserts on the string, not on a Go symbol, and every exported name is a
// compatibility obligation (house rule 7). The set is documented in §6.3 of the
// package design.
const (
	codeAuthMissing        = "auth.missing"
	codeAuthMismatch       = "auth.mismatch"
	codeAuthWrongPlacement = "auth.wrong_placement"
	codeContentType        = "request.content_type"
	codeUnknownField       = "request.unknown_field"

	codeQueryMissing           = "exa.query.missing"
	codeQueryEmpty             = "exa.query.empty"
	codeTypeInvalid            = "exa.type.invalid"
	codeTypeLegacyNeural       = "exa.type.legacy_neural"
	codeNumResultsRange        = "exa.numResults.range"
	codeCategoryInvalid        = "exa.category.invalid"
	codeIncludeDomainsMax      = "exa.includeDomains.max"
	codeExcludeDomainsMax      = "exa.excludeDomains.max"
	codeOutputSchemaDepth      = "exa.outputSchema.depth"
	codeOutputSchemaProperties = "exa.outputSchema.properties"
	codeStreamUnimplemented    = "exa.stream.unimplemented"

	// codeFieldType is one code for every "present, but the wrong JSON type"
	// rejection. §6.3 enumerates value checks and not type checks; a single
	// code with the field name attached says the same thing without inventing a
	// dozen sibling codes a consumer would have to learn.
	codeFieldType = "exa.request.field_type"

	codeUseAutopromptDeprecated    = "exa.useAutoprompt.deprecated"
	codeLivecrawlDeprecated        = "exa.livecrawl.deprecated"
	codeStartCrawlDateDeprecated   = "exa.startCrawlDate.deprecated"
	codeEndCrawlDateDeprecated     = "exa.endCrawlDate.deprecated"
	codeNumSentencesDeprecated     = "exa.contents.highlights.numSentences.deprecated"
	codeHighlightsPerURLDeprecated = "exa.contents.highlights.highlightsPerUrl.deprecated"
	codeContextDeprecated          = "exa.context.deprecated"
)

// typeEnumErrorFormat is Exa's own validation message, the one verbatim error
// body its documentation gives. It doubles as proof of the exact `type` enum, so
// it is reproduced rather than paraphrased: a consumer asserting on the vendor's
// message text must see the vendor's message text.
const typeEnumErrorFormat = "Invalid request body | Validation error: Invalid enum value. " +
	"Expected 'auto' | 'fast' | 'instant' | 'deep-lite' | 'deep' | 'deep-reasoning', received '%v' at \"type\""

// Documented request bounds.
const (
	minNumResults     = 1
	maxNumResults     = 100
	defaultNumResults = 10

	maxIncludeDomains = 1200
	maxExcludeDomains = 1200

	maxSchemaDepth      = 2
	maxSchemaProperties = 10
)

// searchFields is the POST /search request surface. A key outside it raises the
// request.unknown_field warning — a warning, not an error, because being
// stricter than the vendor makes a consumer fail here and pass in production.
var searchFields = fieldSet(
	"query", "type", "numResults", "category", "moderation", "userLocation", "compliance",
	"includeDomains", "excludeDomains", "startPublishedDate", "endPublishedDate",
	"startCrawlDate", "endCrawlDate", "additionalQueries", "systemPrompt", "outputSchema",
	"stream", "contents", "useAutoprompt", "livecrawl", "context",
)

// answerFields is the POST /answer request surface, which is a strict subset of
// /search's names plus three the SDK sends and the schema does not declare.
//
// The AnswerRequest schema has exactly four properties — query, stream, text and
// outputSchema. model, systemPrompt and userLocation are accepted and ignored
// because the TypeScript SDK sends them; they are not echoed anywhere.
var answerFields = fieldSet("query", "stream", "text", "outputSchema", "model", "systemPrompt", "userLocation")

// contentsFields is the /search contents subtree. It does not exist on /answer.
var contentsFields = fieldSet(
	"text", "highlights", "summary", "extras", "maxAgeHours", "livecrawlTimeout",
	"subpages", "subpageTarget",
)

func fieldSet(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

// authHeaders are the credential placements Exa documents. Both are accepted:
// the reference page leads with x-api-key and the coding-agents guide mentions
// only Bearer, so an adapter written from either page must work.
var authHeaders = []string{"authorization", "x-api-key"}

// authenticate applies the scenario's credential policy, recording findings and
// nothing else. It never quotes a credential: the value reaches only
// AuthPolicy.ExpectKey comparison, and what is journaled is the fingerprint
// Handle already recorded.
func authenticate(x *provider.Exchange, e *scenario.ProviderEntry) {
	policy := authPolicy(e)
	if policy.Mode == scenario.AuthReject {
		x.Fail(codeAuthMismatch, "", "the scenario rejects every credential presented to this provider")
		return
	}

	accepted := x.AcceptedPlacements(policy, authHeaders)

	presented := httpx.ExtractCredentials(x.Request)
	var match *httpx.Credential
	for i := range presented {
		if slices.Contains(accepted, presented[i].Header) {
			match = &presented[i]
			break
		}
	}

	if match == nil {
		for i := range presented {
			x.Warn(codeAuthWrongPlacement, presented[i].Header,
				"a credential was presented in %q, which this scenario does not accept; accepted placements are %s",
				presented[i].Header, strings.Join(accepted, ", "))
		}
		if policy.Mode != scenario.AuthOptional {
			x.Fail(codeAuthMissing, "", "no credential was presented in an accepted placement (%s)",
				strings.Join(accepted, ", "))
		}
		return
	}

	if policy.ExpectKey != "" && match.Value != policy.ExpectKey {
		// The message states the mismatch and never the value, in either
		// direction: this finding's whole subject is a credential and it reaches
		// the journal, the admin API and the log.
		x.Fail(codeAuthMismatch, match.Header, "the presented credential does not match the scenario's expected key")
	}
}

// authPolicy returns the entry's policy with defaults applied, tolerating a
// scenario that declares none.
func authPolicy(e *scenario.ProviderEntry) scenario.AuthPolicy {
	if e == nil || e.Auth == nil {
		return scenario.AuthPolicy{Mode: scenario.AuthRequired}
	}
	policy := *e.Auth
	if policy.Mode == "" {
		policy.Mode = scenario.AuthRequired
	}
	return policy
}

// validateContentType warns about a body that does not claim JSON. It is a
// warning by default because none of the simulated vendors documents a 415;
// a scenario that wants the strict behaviour promotes the code.
func validateContentType(x *provider.Exchange) {
	if !httpx.IsJSONContentType(x.Request.Header.Get("Content-Type")) {
		x.Warn(codeContentType, "", "Content-Type %q is not a JSON media type",
			x.Request.Header.Get("Content-Type"))
	}
}

// ValidateSearch is the POST /search request contract. See §6.3 of the package
// design for the finding table it implements.
func validateSearch(x *provider.Exchange, stream scenario.StreamPolicy) {
	validateContentType(x)
	validateQuery(x)
	validateSearchType(x)
	validateNumResults(x)
	validateCategory(x)
	validateDomains(x, "includeDomains", maxIncludeDomains, codeIncludeDomainsMax)
	validateDomains(x, "excludeDomains", maxExcludeDomains, codeExcludeDomainsMax)
	validateOutputSchema(x)
	validateStream(x, stream)
	validateSearchDeprecations(x)
	validateContents(x)
	validateUnknownFields(x, searchFields)
}

// validateAnswer is the POST /answer request contract. It deliberately does not
// reuse the /search checks: /answer's schema has four properties, and a shared
// decoder would wrongly accept contents{} and a `text` object here.
func validateAnswer(x *provider.Exchange, stream scenario.StreamPolicy) {
	validateContentType(x)
	validateQuery(x)
	validateAnswerText(x)
	validateOutputSchema(x)
	validateStream(x, stream)
	validateUnknownFields(x, answerFields)
}

func validateQuery(x *provider.Exchange) {
	raw, present := x.Body["query"]
	if !present {
		x.Fail(codeQueryMissing, "query", "query is required")
		return
	}
	query, ok := raw.(string)
	if !ok {
		x.Fail(codeQueryMissing, "query", "query must be a string")
		return
	}
	if strings.TrimSpace(query) == "" {
		x.Fail(codeQueryEmpty, "query", "query must not be empty")
	}
}

func validateSearchType(x *provider.Exchange) {
	raw, present := x.Body["type"]
	if !present {
		return
	}
	value, ok := raw.(string)
	if ok && slices.Contains(SearchTypes, value) {
		return
	}
	if ok && value == "neural" {
		// A distinct code for the legacy value, so a test can assert on it
		// specifically. The body is still the enum error.
		x.Fail(codeTypeLegacyNeural, "type", typeEnumErrorFormat, raw)
		return
	}
	x.Fail(codeTypeInvalid, "type", typeEnumErrorFormat, raw)
}

func validateNumResults(x *provider.Exchange) {
	raw, present := x.Body["numResults"]
	if !present {
		return
	}
	n, ok := raw.(float64)
	if !ok || n != float64(int(n)) {
		x.Fail(codeFieldType, "numResults", "numResults must be an integer")
		return
	}
	if int(n) < minNumResults || int(n) > maxNumResults {
		x.Fail(codeNumResultsRange, "numResults", "numResults must be between %d and %d, got %d",
			minNumResults, maxNumResults, int(n))
	}
}

func validateCategory(x *provider.Exchange) {
	raw, present := x.Body["category"]
	if !present {
		return
	}
	value, ok := raw.(string)
	if !ok || !slices.Contains(Categories, value) {
		x.Fail(codeCategoryInvalid, "category", "category must be one of %s, got %v",
			strings.Join(Categories, ", "), raw)
	}
}

func validateDomains(x *provider.Exchange, field string, limit int, code string) {
	raw, present := x.Body[field]
	if !present {
		return
	}
	list, ok := raw.([]any)
	if !ok {
		x.Fail(codeFieldType, field, "%s must be an array of strings", field)
		return
	}
	if len(list) > limit {
		x.Fail(code, field, "%s accepts at most %d entries, got %d", field, limit, len(list))
	}
}

// validateOutputSchema enforces the two documented bounds on a structured-output
// schema: at most two levels of nesting and at most ten top-level properties.
func validateOutputSchema(x *provider.Exchange) {
	raw, present := x.Body["outputSchema"]
	if !present {
		return
	}
	schema, ok := raw.(map[string]any)
	if !ok {
		x.Fail(codeFieldType, "outputSchema", "outputSchema must be an object")
		return
	}
	if depth := schemaDepth(schema, 0); depth > maxSchemaDepth {
		x.Fail(codeOutputSchemaDepth, "outputSchema",
			"outputSchema nests %d levels; at most %d are accepted", depth, maxSchemaDepth)
	}
	if props, isObject := schema["properties"].(map[string]any); isObject && len(props) > maxSchemaProperties {
		x.Fail(codeOutputSchemaProperties, "outputSchema",
			"outputSchema declares %d properties; at most %d are accepted", len(props), maxSchemaProperties)
	}
}

// schemaDepth counts the nesting of a JSON Schema's `properties` chain. Sub-schemas
// are walked in sorted key order so that the depth a malformed schema reports
// does not depend on Go's randomised map iteration.
func schemaDepth(v any, depth int) int {
	obj, ok := v.(map[string]any)
	if !ok {
		return depth
	}
	props, ok := obj["properties"].(map[string]any)
	if !ok {
		return depth
	}
	deepest := depth + 1
	for _, key := range slices.Sorted(maps.Keys(props)) {
		if d := schemaDepth(props[key], depth+1); d > deepest {
			deepest = d
		}
	}
	return deepest
}

// validateStream applies the scenario's streaming policy. Streaming is a plan
// non-goal; the default is a named warning plus the ordinary JSON body, so the
// gap is assertable rather than invisible.
func validateStream(x *provider.Exchange, policy scenario.StreamPolicy) {
	raw, present := x.Body["stream"]
	if !present {
		return
	}
	on, ok := raw.(bool)
	if !ok {
		x.Fail(codeFieldType, "stream", "stream must be a boolean")
		return
	}
	if !on {
		return
	}
	if policy == scenario.StreamReject {
		x.Fail(codeStreamUnimplemented, "stream", "streaming responses are not simulated")
		return
	}
	x.Warn(codeStreamUnimplemented, "stream",
		"streaming responses are not simulated; the ordinary JSON body was returned")
}

// validateAnswerText enforces the one reshaped field between the two routes. On
// /search the equivalent is contents.text and accepts a boolean or an object; on
// /answer it is a top-level plain boolean with no object form.
func validateAnswerText(x *provider.Exchange) {
	raw, present := x.Body["text"]
	if !present {
		return
	}
	if _, ok := raw.(bool); !ok {
		x.Fail(codeFieldType, "text",
			"text must be a boolean on /answer; the object form belongs to /search's contents.text")
	}
}

// validateSearchDeprecations flags the fields Exa documents as deprecated or
// without effect. Every one is accepted: the real API accepts them, so rejecting
// them would fail a request production would serve.
func validateSearchDeprecations(x *provider.Exchange) {
	deprecated := []struct {
		field string
		code  string
		note  string
	}{
		{"useAutoprompt", codeUseAutopromptDeprecated, "useAutoprompt is deprecated"},
		{"livecrawl", codeLivecrawlDeprecated, "livecrawl is deprecated in favour of contents.maxAgeHours: 0"},
		{"startCrawlDate", codeStartCrawlDateDeprecated, "startCrawlDate is deprecated and has no effect"},
		{"endCrawlDate", codeEndCrawlDateDeprecated, "endCrawlDate is deprecated and has no effect"},
		{"context", codeContextDeprecated, "context is deprecated"},
	}
	for _, d := range deprecated {
		if x.Has(d.field) {
			x.Warn(d.code, d.field, "%s", d.note)
		}
	}
}

// validateContents walks the /search contents subtree, flagging the two
// deprecated highlight parameters and reporting unknown keys beneath it.
func validateContents(x *provider.Exchange) {
	contents, present := x.Body["contents"]
	if !present {
		return
	}
	obj, ok := contents.(map[string]any)
	if !ok {
		x.Fail(codeFieldType, "contents", "contents must be an object")
		return
	}
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		if !contentsFields[key] {
			x.Warn(codeUnknownField, "contents."+key, "contents.%s is not a documented request field", key)
		}
	}

	highlights, ok := obj["highlights"].(map[string]any)
	if !ok {
		return
	}
	if _, has := highlights["numSentences"]; has {
		x.Warn(codeNumSentencesDeprecated, "contents.highlights.numSentences",
			"numSentences is a deprecated highlight parameter")
	}
	if _, has := highlights["highlightsPerUrl"]; has {
		x.Warn(codeHighlightsPerURLDeprecated, "contents.highlights.highlightsPerUrl",
			"highlightsPerUrl is a deprecated highlight parameter")
	}
}

// validateUnknownFields warns about a top-level key outside the route's
// documented surface.
//
// Keys are walked in sorted order because x.Body is a map: without the sort the
// findings would be generated in Go's randomised iteration order and two
// identical requests would journal differently (§3.3).
func validateUnknownFields(x *provider.Exchange, known map[string]bool) {
	for _, key := range slices.Sorted(maps.Keys(x.Body)) {
		if !known[key] {
			x.Warn(codeUnknownField, key, "%s is not a documented request field for this route", key)
		}
	}
}

// requestedNumResults returns how many results this request asks for, applying
// the documented default of ten. Truncation takes the first N in scenario
// declaration order; nothing is ever sorted by relevance.
func requestedNumResults(x *provider.Exchange) int {
	n, ok := x.Number("numResults")
	if !ok || n < minNumResults || n > maxNumResults {
		return defaultNumResults
	}
	return int(n)
}
