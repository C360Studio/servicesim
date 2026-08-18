package exa

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/c360studio/servicesim/provider"
)

// findSimilarFields is the POST /findSimilar request surface. A key outside it
// raises request.unknown_field, following searchFields' precedent.
var findSimilarFields = fieldSet(
	"url", "numResults", "includeDomains", "excludeDomains",
	"startPublishedDate", "endPublishedDate", "startCrawlDate", "endCrawlDate",
	"contents", "category", "excludeSourceDomain",
)

// Documented /findSimilar bounds. minNumResults, maxNumResults and
// codeNumResultsRange are shared with /search: the contract records the same
// 1-100 range and default of 10 for both routes.
const (
	minFindSimilarURLLength = 3
	maxFindSimilarDomains   = 1200
)

// Finding codes /findSimilar's own request validation raises.
const (
	codeFindSimilarURLMissing = "exa.findSimilar.url.missing"
	codeFindSimilarURLLength  = "exa.findSimilar.url.length"
	codeFindSimilarDomainsMax = "exa.findSimilar.domains.max"
)

// validateFindSimilarRequest is the POST /findSimilar request contract.
//
// It deliberately does not reuse validateSearch: the two routes share several
// field names (numResults, includeDomains, category, contents) but
// /findSimilar's `url` has no /search analogue and /search's `query` has no
// /findSimilar one, so a shared decoder would wrongly require or wrongly
// accept the other route's required field.
func validateFindSimilarRequest(x *provider.Exchange) {
	validateContentType(x)
	validateFindSimilarURL(x)
	validateFindSimilarNumResults(x)
	validateFindSimilarDomains(x, "includeDomains", codeFindSimilarDomainsMax)
	validateFindSimilarDomains(x, "excludeDomains", codeFindSimilarDomainsMax)
	validateFindSimilarDeprecatedDates(x)
	validateFindSimilarContents(x)
	validateFindSimilarCategory(x)
	validateFindSimilarExcludeSourceDomain(x)
	validateUnknownFields(x, findSimilarFields)
}

// validateFindSimilarURL checks the one required field. Unlike every other
// /findSimilar field it is NOT documented nullable, so it uses the same
// presence check /search's validateQuery does rather than presentNonNull.
func validateFindSimilarURL(x *provider.Exchange) {
	raw, present := x.Body["url"]
	if !present {
		x.Fail(codeFindSimilarURLMissing, "url", "url is required")
		return
	}
	value, ok := raw.(string)
	if !ok {
		x.Fail(codeFindSimilarURLMissing, "url", "url must be a string")
		return
	}
	// The contract's minLength is a JSON Schema string length, i.e. Unicode code
	// points, not bytes — utf8.RuneCountInString rather than len(value) is what
	// keeps a two-character multibyte string from being counted as long enough.
	if n := utf8.RuneCountInString(value); n < minFindSimilarURLLength {
		x.Fail(codeFindSimilarURLLength, "url", "url must be at least %d characters, got %d",
			minFindSimilarURLLength, n)
	}
}

// validateFindSimilarNumResults reuses /search's bounds and finding code: the
// contract records the identical 1-100 range and default of 10 for both
// routes, so a distinct code here would just be two names for one check.
func validateFindSimilarNumResults(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "numResults")
	if !ok {
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

func validateFindSimilarDomains(x *provider.Exchange, field, code string) {
	raw, ok := presentNonNull(x, field)
	if !ok {
		return
	}
	list, ok := raw.([]any)
	if !ok {
		x.Fail(codeFieldType, field, "%s must be an array of strings", field)
		return
	}
	for i, item := range list {
		if _, ok := item.(string); !ok {
			x.Fail(codeFieldType, fmt.Sprintf("%s[%d]", field, i), "%s[%d] must be a string", field, i)
		}
	}
	if len(list) > maxFindSimilarDomains {
		x.Fail(code, field, "%s accepts at most %d entries, got %d", field, maxFindSimilarDomains, len(list))
	}
}

// validateFindSimilarDeprecatedDates flags startCrawlDate/endCrawlDate as
// deprecated-with-no-effect (reusing /search's codes for the identical
// parameters) and type-checks the two date fields the spec does not deprecate.
func validateFindSimilarDeprecatedDates(x *provider.Exchange) {
	if _, ok := presentNonNull(x, "startCrawlDate"); ok {
		x.Warn(codeStartCrawlDateDeprecated, "startCrawlDate", "startCrawlDate is deprecated and has no effect")
	}
	if _, ok := presentNonNull(x, "endCrawlDate"); ok {
		x.Warn(codeEndCrawlDateDeprecated, "endCrawlDate", "endCrawlDate is deprecated and has no effect")
	}
	validateFindSimilarDateField(x, "startPublishedDate")
	validateFindSimilarDateField(x, "endPublishedDate")
}

func validateFindSimilarDateField(x *provider.Exchange, field string) {
	raw, ok := presentNonNull(x, field)
	if !ok {
		return
	}
	if _, ok := raw.(string); !ok {
		x.Fail(codeFieldType, field, "%s must be a string", field)
	}
}

// validateFindSimilarContents checks only that `contents` is an object when
// present. contracts/exa/README.md records this route's ContentsOptions
// nesting as asserted by $ref name and analogy to /contents, "not
// independently re-verified for this route" — so this does not walk the
// subtree the way validateContentsRequest does for /contents itself.
func validateFindSimilarContents(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "contents")
	if !ok {
		return
	}
	if _, ok := raw.(map[string]any); !ok {
		x.Fail(codeFieldType, "contents", "contents must be an object")
	}
}

// validateFindSimilarCategory reuses /search's categories enum and
// codeCategoryInvalid: the contract records the identical six-member enum for
// both routes.
func validateFindSimilarCategory(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "category")
	if !ok {
		return
	}
	value, ok := raw.(string)
	if !ok || !slices.Contains(categories, value) {
		x.Fail(codeCategoryInvalid, "category", "category must be one of %s, got %v",
			strings.Join(categories, ", "), raw)
	}
}

func validateFindSimilarExcludeSourceDomain(x *provider.Exchange) {
	raw, ok := presentNonNull(x, "excludeSourceDomain")
	if !ok {
		return
	}
	if _, ok := raw.(bool); !ok {
		x.Fail(codeFieldType, "excludeSourceDomain", "excludeSourceDomain must be a boolean")
	}
}
