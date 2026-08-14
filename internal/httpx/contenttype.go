package httpx

import "strings"

// IsJSONContentType reports whether ct is application/json or a +json media type,
// ignoring parameters.
//
// Parameters are ignored rather than validated because "application/json;
// charset=utf-8" is what a great many HTTP clients send by default, and
// charset is meaningless for JSON but harmless. Structured suffixes
// (application/vnd.api+json, application/ld+json) are accepted for the same
// reason the vendors accept them: they are JSON.
//
// A missing header is not JSON, and neither is text/json. Both produce the
// request.content_type finding, which is a warning by default because none of
// the three vendors documents a 415 — see §6.2.
func IsJSONContentType(ct string) bool {
	media, _, _ := strings.Cut(ct, ";")
	media = strings.ToLower(strings.TrimSpace(media))

	typ, subtype, ok := strings.Cut(media, "/")
	if !ok || typ == "" || subtype == "" {
		return false
	}
	if typ == "application" && subtype == "json" {
		return true
	}
	return len(subtype) > len("+json") && strings.HasSuffix(subtype, "+json")
}
