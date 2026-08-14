package httpx

import (
	"net/http"
	"strings"
	"unicode"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/internal/redact"
)

// Credential is one observed credential placement.
type Credential struct {
	Header string // lower-cased header name
	Scheme string // "Bearer" for Authorization, empty otherwise
	Value  string // the credential; never journaled
}

// credentialHeaders is the ordered set of placements Servicesim recognises. The
// order is the order ExtractCredentials returns, and it is fixed rather than
// derived from the request's header map because ranging over an http.Header
// would put Go's randomised map iteration into the journal's Auth.Header field
// for any request that presented two placements (house rule 2).
var credentialHeaders = []string{"authorization", "x-api-key"}

// canonicalSchemes maps a lower-cased Authorization scheme token to the spelling
// recorded in the journal. Canonicalising means a test asserting Scheme ==
// "Bearer" holds whether the adapter sent "Bearer", "bearer" or "BEARER", all of
// which RFC 9110 makes equivalent. The set matches redact's, so a scheme this
// package strips is a scheme redaction knows to preserve while masking the token
// after it.
var canonicalSchemes = map[string]string{
	"bearer": "Bearer",
	"basic":  "Basic",
	"digest": "Digest",
}

// ExtractCredentials returns every recognised credential placement on r, in
// header order: authorization, x-api-key.
//
// A placement that carries no value is not returned — "Authorization: Bearer"
// with nothing after the scheme is an absent credential, not an empty one, and
// reporting it would make auth.missing unreachable for that request. Only the
// first value of a repeated header is read, which is what every real vendor
// gateway does.
//
// The returned Value is the credential itself, for the one caller that must
// compare it against scenario.AuthPolicy.ExpectKey. It is not safe to journal,
// log or interpolate into a finding message: pass it through [Observe] first.
func ExtractCredentials(r *http.Request) []Credential {
	if r == nil || r.Header == nil {
		return nil
	}

	var found []Credential
	for _, header := range credentialHeaders {
		raw := strings.TrimSpace(r.Header.Get(header))
		if raw == "" {
			continue
		}
		scheme, value := splitScheme(header, raw)
		if value == "" {
			continue
		}
		found = append(found, Credential{Header: header, Scheme: scheme, Value: value})
	}
	return found
}

// splitScheme separates a recognised Authorization scheme from the credential
// after it. A value with no recognised scheme is returned whole: an opaque
// "Authorization: sk-test-…" is a credential, and guessing that the first word
// of an unrecognised scheme is not part of the key would change its fingerprint.
func splitScheme(header, raw string) (scheme, value string) {
	if header != "authorization" {
		return "", raw
	}
	// A bare scheme token with no credential after it splits into a token and an
	// empty rest, which ExtractCredentials drops as an absent credential.
	token, rest := raw, ""
	if split := strings.IndexFunc(raw, unicode.IsSpace); split >= 0 {
		token, rest = raw[:split], strings.TrimSpace(raw[split:])
	}

	canonical, ok := canonicalSchemes[strings.ToLower(token)]
	if !ok {
		return "", raw
	}
	return canonical, rest
}

// Observe converts a credential into a journal observation, fingerprinting the
// value rather than carrying it.
//
// present is the caller's determination that a credential was actually seen; a
// false present yields the zero observation, so an absent credential can never
// be mistaken for a presented one. It is deliberately the caller's call and not
// this function's, because "seen" and "accepted" are different questions: an
// x-api-key on Tavily was seen — it belongs in the journal with its placement
// and fingerprint — and is still not accepted, which the caller records as
// tavily.auth.wrong_header plus auth.missing.
func Observe(c Credential, present bool) journal.AuthObservation {
	if !present {
		return journal.AuthObservation{}
	}
	return journal.AuthObservation{
		Present:     true,
		Header:      strings.ToLower(c.Header),
		Scheme:      c.Scheme,
		Fingerprint: redact.Fingerprint(c.Value),
	}
}
