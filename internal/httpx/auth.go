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

// ObserveAll converts every presented credential into one observation. The first
// fills the scalar fields, for the many consumers that only ever ask "was a
// credential presented, and where"; all of them, including that first, are listed
// in Placements.
//
// Recording only the first is what this replaced, and it lost real information: a
// request carrying both an Authorization header and an x-api-key journaled as
// though it had sent one. A consumer proving its adapter sends exactly the
// placement a vendor documents could not see the extra one it was also sending.
func ObserveAll(creds []Credential) journal.AuthObservation {
	if len(creds) == 0 {
		return journal.AuthObservation{}
	}
	obs := Observe(creds[0], true)
	obs.Placements = make([]journal.AuthPlacement, 0, len(creds))
	for _, c := range creds {
		obs.Placements = append(obs.Placements, placementOf(c))
	}
	return obs
}

// AddPlacement records one more placement on an existing observation, promoting
// it to the primary if none was recorded yet.
//
// It exists for placements no generic header scan can find: Tavily's credential
// arrives as a property of the JSON body, so it is discovered by the provider
// package after Handle has already observed the headers. Appending rather than
// replacing is what lets a request presenting a header AND a body key journal
// both, which is precisely the misconfiguration worth being able to see.
func AddPlacement(obs journal.AuthObservation, c Credential) journal.AuthObservation {
	p := placementOf(c)
	if !obs.Present {
		obs.Present = true
		obs.Header = p.Header
		obs.Scheme = p.Scheme
		obs.Fingerprint = p.Fingerprint
	}
	for _, existing := range obs.Placements {
		if existing == p {
			return obs
		}
	}
	obs.Placements = append(obs.Placements, p)
	return obs
}

// placementOf renders one credential as a journal placement, fingerprinting the
// value rather than carrying it.
//
// Header and Scheme are passed through redact.String before retention. For a
// Credential built by this package's own header scan that is a no-op — the
// header is always one of credentialHeaders and the scheme, if any, is always
// a canonicalSchemes value, neither of which is credential-shaped. It matters
// for provider.ObserveCredential (provider/framework.go), the exported write
// path a profile author reaches with a hand-built Credential: nothing stops
// them from passing the raw token itself as Header or Scheme, and Placements
// are never masked by journal.Redact ("Placements hold fingerprints, never
// values, so there is nothing here to mask" — true only if these two fields
// are safe by construction, which is what this call now guarantees).
func placementOf(c Credential) journal.AuthPlacement {
	return journal.AuthPlacement{
		Header:      redact.String(strings.ToLower(c.Header)),
		Scheme:      redact.String(c.Scheme),
		Fingerprint: redact.Fingerprint(c.Value),
	}
}
