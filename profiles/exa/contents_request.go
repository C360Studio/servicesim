package exa

import (
	"fmt"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/provider"
)

// contentsEndpointFields is the POST /contents request surface. A key outside
// it raises request.unknown_field, following searchFields' precedent.
var contentsEndpointFields = fieldSet(
	"ids", "urls", "compliance", "text", "highlights", "summary", "extras",
	"maxAgeHours", "livecrawl", "livecrawlTimeout", "subpages", "subpageTarget", "context",
)

// Documented enums for /contents. complianceValues and livecrawlValues are
// specific to this route: /search accepts the same field names but this
// package has never enum-validated them there, and retrofitting that is out of
// this unit's scope.
var (
	complianceValues    = []string{"hipaa"}
	livecrawlValues     = []string{"never", "always", "fallback", "preferred"}
	textVerbosityValues = []string{"compact", "standard", "full"}
	contentsSections    = []string{"header", "navigation", "banner", "body", "sidebar", "footer", "metadata"}
)

// Documented /contents bounds.
const (
	minContentsItems = 1
	maxContentsItems = 100

	minMaxAgeHours = -1
	maxMaxAgeHours = 720

	minLivecrawlTimeoutMS = 1
	maxLivecrawlTimeoutMS = 90000

	minContentsSubpages = 0
	maxContentsSubpages = 100

	minContentsExtraValue = 0
	maxContentsExtraValue = 1000

	minContentsCharacters = 1
	maxContentsCharacters = 10000
)

// contentsExtrasKeys is the five documented extras.* keys, each an integer
// count 0-1000. The request's own key set is not itself enum-checked here:
// an unrecognised key inside extras is accepted the same way an unrecognised
// nested key inside text/highlights is, following this file's existing
// precedent of only type-checking the keys it names.
var contentsExtrasKeys = []string{"links", "imageLinks", "richImageLinks", "richLinks", "codeBlocks"}

// Finding codes /contents' own request validation raises.
const (
	codeContentsTargetMissing     = "exa.contents.target.missing"
	codeContentsIDsAndURLs        = "exa.contents.ids_and_urls"
	codeContentsItemInvalid       = "exa.contents.item.invalid"
	codeContentsItemsRange        = "exa.contents.items.range"
	codeContentsComplianceInvalid = "exa.contents.compliance.invalid"
	codeContentsLivecrawlInvalid  = "exa.contents.livecrawl.invalid"
	codeContentsVerbosityInvalid  = "exa.contents.text.verbosity.invalid"
	codeContentsSectionInvalid    = "exa.contents.text.section.invalid"
	codeContentsMaxAgeHoursRange  = "exa.contents.maxAgeHours.range"
	codeContentsExtrasRange       = "exa.contents.extras.range"
	codeContentsTimeoutRange      = "exa.contents.livecrawlTimeout.range"
	codeContentsSubpagesRange     = "exa.contents.subpages.range"
	codeContentsCharactersRange   = "exa.contents.characters.range"

	// codeContentsStatusUnrequested is raised at RESPONSE time (see
	// applyContentsOverrides in contents.go), not by this file's request
	// validation, but it is declared here beside the route's other finding codes
	// because D-a names it as part of /contents' contract.
	codeContentsStatusUnrequested = "exa.contents.status_unrequested"
)

// validateContentsRequest is the POST /contents request contract. See
// contracts/exa/README.md's "POST /contents" section for what each check
// enforces, and D-c through D-e for the decisions the vendor's own pages left
// open (summary's boolean-vs-object disagreement, field gating, the documented
// enums).
func validateContentsRequest(x *provider.Exchange) {
	validateContentType(x)
	validateContentsTarget(x)
	validateContentsCompliance(x)
	validateContentsTextOption(x)
	validateContentsHighlightsOption(x)
	validateContentsSummaryOption(x)
	validateContentsExtras(x)
	validateContentsMaxAgeHours(x)
	validateContentsLivecrawl(x)
	validateContentsLivecrawlTimeout(x)
	validateContentsSubpages(x)
	validateContentsSubpageTarget(x)
	validateContentsContext(x)
	validateUnknownFields(x, contentsEndpointFields)
}

// validateContentsTarget enforces D-d: exactly one of ids/urls, or both (with a
// warning, since the docs say "one of" and reject-if-both would be inventing),
// is accepted. Neither present is the one required-field failure this route
// has — D-d names the finding exa.contents.target.missing for it.
func validateContentsTarget(x *provider.Exchange) {
	hasIDs := validateContentsIdentifierList(x, "ids")
	hasURLs := validateContentsIdentifierList(x, "urls")

	if !hasIDs && !hasURLs {
		x.Fail(codeContentsTargetMissing, "", "one of ids or urls is required")
		return
	}
	if hasIDs && hasURLs {
		x.Warn(codeContentsIDsAndURLs, "",
			"both ids and urls were sent; the documentation describes them as one-of, "+
				"so this is accepted rather than rejected")
	}
}

// validateContentsIdentifierList checks one of ids/urls: null-accepted
// presence, type, item shape and the documented 1-100 item count. It reports
// whether the field carried a meaningful (non-null) value at all, which is
// what validateContentsTarget's one-of check needs.
func validateContentsIdentifierList(x *provider.Exchange, field string) bool {
	raw, ok := presentNonNull(x, field)
	if !ok {
		return false
	}
	list, ok := raw.([]any)
	if !ok {
		x.Fail(codeFieldType, field, "%s must be an array of strings", field)
		return true
	}
	if len(list) < minContentsItems || len(list) > maxContentsItems {
		x.Fail(codeContentsItemsRange, field, "%s accepts between %d and %d items, got %d",
			field, minContentsItems, maxContentsItems, len(list))
	}
	for i, item := range list {
		if s, ok := item.(string); !ok || s == "" {
			// INVALID_URLS (D-g) is the documented tag for a malformed id/url; a
			// non-string or empty element is the shape this simulator treats as
			// malformed without attempting real URL parsing.
			x.Fail(codeContentsItemInvalid, fmt.Sprintf("%s[%d]", field, i),
				"%s[%d] must be a non-empty string", field, i)
		}
	}
	return true
}

// validateContentsCompliance checks the one enum /contents shares with /search
// by name (`hipaa`) but that this package has only ever enum-validated here.
func validateContentsCompliance(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "compliance")
	if !ok {
		return
	}
	value, ok := raw.(string)
	if !ok || !slices.Contains(complianceValues, value) {
		x.Fail(codeContentsComplianceInvalid, "compliance", "compliance must be one of %s, got %v",
			strings.Join(complianceValues, ", "), raw)
	}
}

// validateContentsExtras checks each documented extras.* key: an integer
// 0-1000 when present. A key outside contentsExtrasKeys is not itself flagged,
// following validateContentsTextObject's precedent of only type-checking the
// nested keys this file names.
func validateContentsExtras(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "extras")
	if !ok {
		return
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		x.Fail(codeFieldType, "extras", "extras must be an object")
		return
	}
	for _, key := range contentsExtrasKeys {
		v, ok := presentInMap(obj, key)
		if !ok {
			continue
		}
		field := "extras." + key
		n, ok := v.(float64)
		if !ok || n != float64(int(n)) {
			x.Fail(codeFieldType, field, "%s must be an integer", field)
			continue
		}
		if int(n) < minContentsExtraValue || int(n) > maxContentsExtraValue {
			x.Fail(codeContentsExtrasRange, field, "%s must be between %d and %d, got %d",
				field, minContentsExtraValue, maxContentsExtraValue, int(n))
		}
	}
}

// validateContentsLivecrawlTimeout checks the documented 1-90000ms range.
func validateContentsLivecrawlTimeout(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "livecrawlTimeout")
	if !ok {
		return
	}
	n, ok := raw.(float64)
	if !ok || n != float64(int(n)) {
		x.Fail(codeFieldType, "livecrawlTimeout", "livecrawlTimeout must be an integer")
		return
	}
	if int(n) < minLivecrawlTimeoutMS || int(n) > maxLivecrawlTimeoutMS {
		x.Fail(codeContentsTimeoutRange, "livecrawlTimeout", "livecrawlTimeout must be between %d and %d, got %d",
			minLivecrawlTimeoutMS, maxLivecrawlTimeoutMS, int(n))
	}
}

// validateContentsSubpages checks the documented 0-100 range.
func validateContentsSubpages(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "subpages")
	if !ok {
		return
	}
	n, ok := raw.(float64)
	if !ok || n != float64(int(n)) {
		x.Fail(codeFieldType, "subpages", "subpages must be an integer")
		return
	}
	if int(n) < minContentsSubpages || int(n) > maxContentsSubpages {
		x.Fail(codeContentsSubpagesRange, "subpages", "subpages must be between %d and %d, got %d",
			minContentsSubpages, maxContentsSubpages, int(n))
	}
}

// validateContentsCharacterCount checks a nested *.maxCharacters field against
// the documented 1-10000 range shared by text.maxCharacters and
// highlights.maxCharacters.
func validateContentsCharacterCount(x *provider.Exchange, obj map[string]any, field string) {
	raw, ok := presentInMap(obj, "maxCharacters")
	if !ok {
		return
	}
	path := field + ".maxCharacters"
	n, ok := raw.(float64)
	if !ok || n != float64(int(n)) {
		x.Fail(codeFieldType, path, "%s must be an integer", path)
		return
	}
	if int(n) < minContentsCharacters || int(n) > maxContentsCharacters {
		x.Fail(codeContentsCharactersRange, path, "%s must be between %d and %d, got %d",
			path, minContentsCharacters, maxContentsCharacters, int(n))
	}
}

// validateContentsTextOption checks the boolean|object union (D-c: field
// gating is not enforced by request options, only shape).
func validateContentsTextOption(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "text")
	if !ok {
		return
	}
	switch v := raw.(type) {
	case bool:
	case map[string]any:
		validateContentsTextObject(x, v)
	default:
		x.Fail(codeFieldType, "text", "text must be a boolean or an object")
	}
}

func validateContentsTextObject(x *provider.Exchange, obj map[string]any) {
	if raw, ok := presentInMap(obj, "verbosity"); ok {
		value, ok := raw.(string)
		if !ok || !slices.Contains(textVerbosityValues, value) {
			x.Fail(codeContentsVerbosityInvalid, "text.verbosity", "text.verbosity must be one of %s, got %v",
				strings.Join(textVerbosityValues, ", "), raw)
		}
	}
	validateContentsCharacterCount(x, obj, "text")
	if raw, ok := presentInMap(obj, "includeHtmlTags"); ok {
		if _, ok := raw.(bool); !ok {
			x.Fail(codeFieldType, "text.includeHtmlTags", "text.includeHtmlTags must be a boolean")
		}
	}
	validateContentsSectionList(x, obj, "includeSections")
	validateContentsSectionList(x, obj, "excludeSections")
}

func validateContentsSectionList(x *provider.Exchange, obj map[string]any, field string) {
	raw, ok := presentInMap(obj, field)
	if !ok {
		return
	}
	list, ok := raw.([]any)
	if !ok {
		x.Fail(codeFieldType, "text."+field, "text.%s must be an array of strings", field)
		return
	}
	for i, item := range list {
		value, ok := item.(string)
		if !ok || !slices.Contains(contentsSections, value) {
			x.Fail(codeContentsSectionInvalid, fmt.Sprintf("text.%s[%d]", field, i),
				"text.%s[%d] must be one of %s, got %v", field, i, strings.Join(contentsSections, ", "), item)
		}
	}
}

// validateContentsHighlightsOption checks the boolean|object union and flags
// the two deprecated highlight sub-parameters, reusing the same finding codes
// /search's nested contents.highlights uses for the identical vendor
// parameter — this route names it at the top level (`highlights.*`, not
// `contents.highlights.*`), which is what the `field` argument records.
func validateContentsHighlightsOption(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "highlights")
	if !ok {
		return
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		if _, isBool := raw.(bool); !isBool {
			x.Fail(codeFieldType, "highlights", "highlights must be a boolean or an object")
		}
		return
	}
	if raw, ok := presentInMap(obj, "query"); ok {
		if _, ok := raw.(string); !ok {
			x.Fail(codeFieldType, "highlights.query", "highlights.query must be a string")
		}
	}
	validateContentsCharacterCount(x, obj, "highlights")
	if _, has := presentInMap(obj, "numSentences"); has {
		x.Warn(codeNumSentencesDeprecated, "highlights.numSentences",
			"numSentences is a deprecated highlight parameter")
	}
	if _, has := presentInMap(obj, "highlightsPerUrl"); has {
		x.Warn(codeHighlightsPerURLDeprecated, "highlights.highlightsPerUrl",
			"highlightsPerUrl is a deprecated highlight parameter")
	}
}

// validateContentsSummaryOption accepts BOTH the guide's boolean|object form
// and the OpenAPI spec's object-only form, per D-c: the two vendor pages
// disagree, and this is recorded rather than resolved one way.
func validateContentsSummaryOption(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "summary")
	if !ok {
		return
	}
	switch v := raw.(type) {
	case bool:
	case map[string]any:
		validateContentsSummaryObject(x, v)
	default:
		x.Fail(codeFieldType, "summary", "summary must be a boolean or an object")
	}
}

func validateContentsSummaryObject(x *provider.Exchange, obj map[string]any) {
	if raw, ok := presentInMap(obj, "query"); ok {
		if _, ok := raw.(string); !ok {
			x.Fail(codeFieldType, "summary.query", "summary.query must be a string")
		}
	}
	if raw, ok := presentInMap(obj, "schema"); ok {
		if _, ok := raw.(map[string]any); !ok {
			x.Fail(codeFieldType, "summary.schema", "summary.schema must be an object")
		}
	}
}

// validateContentsMaxAgeHours checks the documented -1 to 720 range: -1 means
// "always serve cached", 0 means "force a fresh crawl".
func validateContentsMaxAgeHours(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "maxAgeHours")
	if !ok {
		return
	}
	n, ok := raw.(float64)
	if !ok || n != float64(int(n)) {
		x.Fail(codeFieldType, "maxAgeHours", "maxAgeHours must be an integer")
		return
	}
	if int(n) < minMaxAgeHours || int(n) > maxMaxAgeHours {
		x.Fail(codeContentsMaxAgeHoursRange, "maxAgeHours", "maxAgeHours must be between %d and %d, got %d",
			minMaxAgeHours, maxMaxAgeHours, int(n))
	}
}

// validateContentsLivecrawl checks the enum and flags the field deprecated,
// reusing /search's codeLivecrawlDeprecated for the identical vendor parameter.
func validateContentsLivecrawl(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "livecrawl")
	if !ok {
		return
	}
	value, ok := raw.(string)
	if !ok || !slices.Contains(livecrawlValues, value) {
		x.Fail(codeContentsLivecrawlInvalid, "livecrawl", "livecrawl must be one of %s, got %v",
			strings.Join(livecrawlValues, ", "), raw)
		return
	}
	x.Warn(codeLivecrawlDeprecated, "livecrawl", "livecrawl is deprecated in favour of maxAgeHours: 0")
}

// validateContentsSubpageTarget checks the string|array[string] union.
func validateContentsSubpageTarget(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "subpageTarget")
	if !ok {
		return
	}
	switch v := raw.(type) {
	case string:
	case []any:
		for i, item := range v {
			if _, ok := item.(string); !ok {
				x.Fail(codeFieldType, fmt.Sprintf("subpageTarget[%d]", i), "subpageTarget[%d] must be a string", i)
			}
		}
	default:
		x.Fail(codeFieldType, "subpageTarget", "subpageTarget must be a string or an array of strings")
	}
}

// validateContentsContext checks the boolean|object union and flags the field
// deprecated, reusing /search's codeContextDeprecated.
func validateContentsContext(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "context")
	if !ok {
		return
	}
	switch raw.(type) {
	case bool, map[string]any:
	default:
		x.Fail(codeFieldType, "context", "context must be a boolean or an object")
		return
	}
	x.Warn(codeContextDeprecated, "context", "context is deprecated; use highlights or text instead")
}

// presentInMap is presentNonNull for a nested object: every documented nested
// option field under text/highlights/summary/extras carries the same
// anyOf[<type>, null] shape as the top-level fields do (D-d), so a null nested
// value is accepted the same way an absent one is.
func presentInMap(obj map[string]any, key string) (any, bool) {
	raw, present := obj[key]
	if !present || raw == nil {
		return nil, false
	}
	return raw, true
}
