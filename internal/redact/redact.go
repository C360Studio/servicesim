package redact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Mask replaces every redacted value.
const Mask = "[REDACTED]"

// fingerprintDomain separates Fingerprint's hash from a bare SHA-256 of the
// credential, so a journal reader cannot confirm a guessed key with sha256sum.
const fingerprintDomain = "servicesim/redact/fingerprint/v1\x00"

// exactCredentialNames match only as a whole normalised name. "auth" is here and
// nowhere else: as a substring it matches "author", which is a documented result
// field on both Exa and Tavily. Redacting every author name would corrupt the
// journal's evidence while protecting nothing.
//
// The known header placements are seeded here rather than in a separate
// header-only list, because applying a weaker rule to headers than to JSON
// properties would leak exactly the adapter mistake the journal exists to
// surface.
var exactCredentialNames = map[string]bool{
	"auth": true, "authorization": true, "key": true, "apikey": true,
	"xapikey": true, "token": true, "secret": true, "password": true,
	"passwd": true, "pwd": true, "credential": true, "credentials": true,
	"signature": true, "sig": true, "sessionid": true, "cookie": true,
	"proxyauthorization": true, "xapitoken": true, "xauthtoken": true,
	"xgoogapikey": true, "xamzsecuritytoken": true, "setcookie": true,
}

// substringCredentialFragments match anywhere in a normalised name. Every entry
// here must be checked against the three providers' documented field names before
// it is added.
var substringCredentialFragments = []string{
	"apikey", "accesstoken", "refreshtoken", "idtoken", "bearertoken",
	"clientsecret", "secretkey", "accesskey", "privatekey", "password",
	"credential",
}

// nameNormalizer strips the separators that distinguish api_key, api-key,
// api.key and "api key" from apikey. Case is folded separately.
var nameNormalizer = strings.NewReplacer("_", "", "-", "", ".", "", " ", "")

// authSchemes are the Authorization schemes whose name is kept while their
// credential is masked. Placement is what an adapter contract test asserts, so
// masking the scheme would destroy evidence while protecting nothing.
var authSchemes = map[string]bool{"bearer": true, "basic": true, "digest": true}

var (
	// userinfoPattern matches the credential half of a URL's userinfo. A legal
	// absolute-form request target (POST http://user:secret@host/search) renders
	// it verbatim in anything that quotes the raw request.
	userinfoPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s:]+):([^/@\s]*)@`)

	// schemePattern matches "Bearer <token>" and friends in free text. It runs
	// before pairPattern so that "Authorization: Bearer x" masks x rather than
	// masking the scheme and leaving x behind. What follows the scheme is only
	// masked when looksLikeCredential accepts it: in a finding message the scheme
	// name is prose, not a prefix.
	schemePattern = regexp.MustCompile(`(?i)\b(bearer|basic|digest)\s+([A-Za-z0-9._~+/=\-]+)`)

	// pairPattern matches name=value and "name": "value" in free text. The value
	// class stops at "/" and "?" so that scanning a URL does not swallow a
	// credential parameter further along it; maskPairs widens the span again once
	// the name is known to be a credential, because base64 credentials contain
	// "/" and "+".
	// The value class must exclude exactly valueTerminators, plus "/" and "?"
	// which extendValue puts back. When the two disagree the two passes disagree,
	// and redaction stops being idempotent.
	pairPattern = regexp.MustCompile(`([A-Za-z0-9_.\-]{1,64})"?[ \t]*[:=][ \t]*"?([^` + valueTerminators + `/?]*)`)

	// vendorKeyPattern matches a bare provider key with no credential-shaped name
	// beside it. It requires a digit in the suffix so that ordinary prose such as
	// "sk-learning-resources" is not masked: over-redaction corrupts the journal's
	// evidence, and the name matcher is the primary defence.
	vendorKeyPattern = regexp.MustCompile(`(?i)\b(sk|pplx|tvly)[-_][A-Za-z0-9_\-]{12,}`)

	digitPattern = regexp.MustCompile(`[0-9]`)
)

// valueTerminators end a credential value once maskPairs knows the name is a
// credential and the span may safely be widened. It is spelled as a character
// class so pairPattern and extendValue cannot drift apart; every character here
// must be one the regexp value class also excludes.
const valueTerminators = `"'\t\n\f\r &,;)}`

// valueTerminatorRunes is valueTerminators with the escapes resolved, for the
// byte-wise scan in extendValue.
const valueTerminatorRunes = "\"'\t\n\f\r &,;)}"

// maxProseWord is the longest run of letters looksLikeCredential will accept as
// a word of the sentence a finding is written in. The words that actually follow
// a scheme name in this repository's findings are "is", "scheme", "credential"
// and "authenticates" (13); the bound is set at "misconfiguration" so that no
// plausible finding word is mistaken for a key, and any longer run is one.
const maxProseWord = 16

// normalizeName lower-cases a name and strips the separators that would
// otherwise hide a credential behind a spelling variant.
func normalizeName(name string) string {
	return nameNormalizer.Replace(strings.ToLower(name))
}

// isCredentialName is the single matcher behind IsCredentialKey and
// IsCredentialHeader. There is deliberately no second, weaker rule.
func isCredentialName(name string) bool {
	n := normalizeName(name)
	if n == "" {
		return false
	}
	if exactCredentialNames[n] {
		return true
	}
	for _, fragment := range substringCredentialFragments {
		if strings.Contains(n, fragment) {
			return true
		}
	}
	return false
}

// IsCredentialKey reports whether a JSON property name looks like a credential.
func IsCredentialKey(name string) bool {
	return isCredentialName(name)
}

// IsCredentialHeader reports whether a header name carries a credential. It runs
// the same normalise-then-two-tier matcher as IsCredentialKey, so a
// vendor-prefixed header is caught: X-Exa-Api-Key, X-Tavily-Api-Key, Api_Key and
// X-Api-Key-2 all normalise into the substring tier. Applying a weaker rule to
// headers than to JSON properties would leak exactly the adapter mistake the
// journal exists to surface.
func IsCredentialHeader(name string) bool {
	return isCredentialName(name)
}

// Headers returns a copy of h with credential-bearing values masked. For
// Authorization the scheme is preserved — "Bearer [REDACTED]" — because
// authentication placement is exactly what an adapter contract test asserts,
// and masking the scheme would destroy the evidence while protecting nothing.
//
// A nil header stays nil so a caller can store the result unchanged. The copy is
// deep: neither the map nor any value slice is shared with h.
func Headers(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for name, values := range h {
		masked := make([]string, len(values))
		for i, value := range values {
			masked[i] = HeaderValue(name, value)
		}
		out[name] = masked
	}
	return out
}

// HeaderValue masks a single header value according to its name. A value under a
// name that is not credential-bearing still passes through String, because a
// credential quoted in an unrelated header is still a credential in the journal.
func HeaderValue(name, value string) string {
	if value == "" {
		return ""
	}
	if !IsCredentialHeader(name) {
		return String(value)
	}
	if isSchemeHeader(name) {
		if scheme, rest, found := strings.Cut(value, " "); found && isAuthScheme(scheme) && strings.TrimSpace(rest) != "" {
			return scheme + " " + Mask
		}
	}
	return Mask
}

// isSchemeHeader reports whether a header's value is scheme-prefixed, and so
// whether the scheme must survive masking.
func isSchemeHeader(name string) bool {
	n := normalizeName(name)
	return n == "authorization" || n == "proxyauthorization"
}

// isAuthScheme reports whether a token is an authentication scheme rather than a
// credential.
func isAuthScheme(token string) bool {
	return authSchemes[strings.ToLower(token)]
}

// Query masks credential-named parameter values in a raw query string. It parses
// with url.ParseQuery, masks every value whose key satisfies IsCredentialKey, and
// re-encodes with url.Values.Encode, which sorts keys and is therefore
// deterministic. On a parse failure it falls back to String(raw) — never to raw.
//
// Parameter names are preserved and only values are masked: an adapter that puts
// its key in the query string is exactly the misconfiguration the journal exists
// to surface. Values under names that are not credential-bearing pass through
// String, so a credential riding inside an unrelated parameter is still masked,
// and so does each name, for the adapter that sends its key as a bare flag.
//
// Encode percent-escapes the mask's brackets; they are restored afterwards so
// the stored query reads as [REDACTED] rather than %5BREDACTED%5D. The result
// still parses back to the same values, so the substitution costs nothing.
func Query(raw string) string {
	if raw == "" {
		return ""
	}
	if values, err := url.ParseQuery(raw); err == nil {
		return encodeQuery(values)
	}
	// The fallback is re-parsed rather than returned directly, because masking
	// can turn an unparseable query into a parseable one — a bad percent escape
	// often sits inside the credential itself. Without this second attempt the
	// first pass would produce text the second pass canonicalised differently,
	// and Query would not be idempotent.
	masked := String(raw)
	if values, err := url.ParseQuery(masked); err == nil {
		return encodeQuery(values)
	}
	return masked
}

// encodeQuery masks parsed parameters and re-encodes them. Names are visited in
// sorted order so that two names masking to the same string merge the same way
// on every run; a parameter name can itself be a credential.
func encodeQuery(values url.Values) string {
	out := make(url.Values, len(values))
	for _, key := range slices.Sorted(maps.Keys(values)) {
		credential := IsCredentialKey(key)
		masked := make([]string, len(values[key]))
		for i, v := range values[key] {
			switch {
			case v == "":
				// An absent value is not a credential, and masking it would
				// invent evidence of one.
			case credential:
				v = Mask
			default:
				v = String(v)
			}
			masked[i] = v
		}
		name := String(key)
		out[name] = append(out[name], masked...)
	}
	return strings.ReplaceAll(out.Encode(), url.QueryEscape(Mask), Mask)
}

// JSON walks a decoded JSON value and masks every string whose property name
// looks like a credential. Maps and slices are rebuilt rather than mutated, so
// the caller's value is untouched.
//
// Masking under a credential-named property applies to the whole subtree: a
// number, an array of strings and a nested object under "credentials" are all
// credentials. Strings elsewhere pass through String, which catches a credential
// quoted inside a free-text field such as a search query.
func JSON(v any) any {
	return redactValue(v, false)
}

// redactValue is JSON's recursion. masked is true once an enclosing property
// name was credential-bearing.
func redactValue(v any, masked bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		// Property names are visited in sorted order because a name can itself be
		// a credential — a map keyed by API key is a real shape — and two names
		// masking to the same string must resolve the same way on every run.
		// Iterating the map directly would let map order decide which value
		// survived, which is exactly the nondeterminism this repository forbids.
		for _, key := range slices.Sorted(maps.Keys(t)) {
			out[String(key)] = redactValue(t[key], masked || IsCredentialKey(key))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, value := range t {
			out[i] = redactValue(value, masked)
		}
		return out
	case string:
		if masked {
			return Mask
		}
		return String(t)
	case nil:
		// A null under a credential name is the absence of a credential.
		return nil
	default:
		if masked {
			return Mask
		}
		return v
	}
}

// JSONBytes decodes raw, redacts it, and re-encodes. When raw is not valid JSON
// it returns []byte(String(raw)) — redacted, not verbatim. The non-JSON branch is
// not an edge case: a form-encoded body (api_key=sk-live-...&query=x) reaches it,
// a truncated or trailing-comma body reaches it, and the repository ships a
// malformed-json scenario that exercises it deliberately. Returning raw there
// would store a credential byte-for-byte in the journal and in
// /__admin/requests. The body is still bounded and still stored, just masked.
//
// Numbers are carried as json.Number so re-encoding does not reformat them, and
// object keys are re-encoded in sorted order, so the output is byte-stable for a
// given input.
func JSONBytes(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw
	}

	if encoded, ok := redactJSONDocument(raw); ok {
		return encoded
	}

	// The text fallback is decoded again, because masking can turn an invalid
	// document into a valid one — an unescaped control character inside the
	// credential is enough. Without this second attempt the first pass would
	// produce bytes the second pass redacted structurally and differently, and
	// JSONBytes would not be idempotent.
	masked := []byte(String(string(raw)))
	if encoded, ok := redactJSONDocument(masked); ok {
		return encoded
	}
	return masked
}

// redactJSONDocument redacts raw structurally, reporting false when raw is not
// exactly one JSON document.
func redactJSONDocument(raw []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return nil, false
	}
	// Trailing bytes mean the body is not one JSON document, however well the
	// prefix parsed. Storing only the prefix would drop evidence; the text
	// matcher covers the whole thing instead.
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false
	}

	encoded, err := encodeJSON(JSON(decoded))
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// encodeJSON re-encodes a redacted document with HTML escaping off, so an "&" in
// a query survives as itself rather than becoming &.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// String masks credentials inside free text: name=value and "name: value" pairs
// whose name satisfies IsCredentialKey (covering form-encoded bodies), Bearer
// tokens, URL userinfo, and known vendor key prefixes. It is what protects
// finding messages, body parse errors and non-JSON bodies, all of which may quote
// part of a request.
//
// The order is load-bearing. Userinfo runs first because a URL's password half is
// delimited differently from everything else; scheme-prefixed credentials run
// before pairs so "Authorization: Bearer x" masks x instead of masking the
// scheme and leaving x behind.
//
// Free text is prose as often as it is a quoted request, so what follows a scheme
// name is masked only when it is shaped like a credential — see
// looksLikeCredential. Nothing else is conditional: a value under a credential
// name and a vendor-prefixed key are masked wherever they appear.
func String(s string) string {
	if s == "" {
		return s
	}
	s = maskUserinfo(s)
	s = maskSchemes(s)
	s = maskPairs(s)
	return maskVendorKeys(s)
}

// maskUserinfo masks the password half of a URL's userinfo. The username is kept:
// it is placement evidence, and it is not the secret.
func maskUserinfo(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	return userinfoPattern.ReplaceAllString(s, "${1}${2}:"+Mask+"@")
}

// maskSchemes masks the credential after an authentication scheme, keeping the
// scheme itself — and keeping the word after it when that word is prose rather
// than a credential. It rebuilds the string around the value spans, so the
// scheme and the whitespace after it survive byte for byte.
func maskSchemes(s string) string {
	matches := schemePattern.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}

	var b strings.Builder
	written := 0
	for _, m := range matches {
		valueStart, valueEnd := m[4], m[5]
		if valueStart < 0 || valueEnd <= valueStart {
			continue
		}
		if !looksLikeCredential(s[valueStart:valueEnd]) {
			continue
		}
		b.WriteString(s[written:valueStart])
		b.WriteString(Mask)
		written = valueEnd
	}
	if written == 0 {
		return s
	}
	b.WriteString(s[written:])
	return b.String()
}

// looksLikeCredential reports whether the token following an authentication
// scheme in free text is a credential value rather than the next word of the
// sentence.
//
// It exists because a finding message names the scheme as its subject:
// "Authorization: Bearer is required", "only Authorization: Bearer
// authenticates", "no Authorization: Bearer credential was presented". Masking
// the word after the scheme turned the first of those into "Authorization:
// Bearer [REDACTED] required", which reads as though a credential were
// presented and sends the consumer hunting for a bug in correct code.
//
// The two are told apart by shape, never by vocabulary: a rule that knew the
// phrase "is required" would mangle the next message anyone writes, and three of
// the four messages above were mangled by exactly that oversight. A word of
// English carries only letters, and carries at most one capital, at its start.
// A credential carries a digit or one of the opaque-key separators ". _ ~ + / =
// -", or an interior capital (base64 and camel-cased keys), or it runs longer
// than any word a finding is written with.
//
// The relaxation is confined to free text. HeaderValue still masks whatever
// follows the scheme in an actual Authorization header unconditionally, because
// there the token after the scheme is the credential by definition, and the pair
// and vendor-prefix matchers are untouched. What remains reachable is a
// credential that is letters only, uniformly cased, no longer than a word, and
// carried in prose under no credential-shaped name — a shape no vendor issues.
func looksLikeCredential(v string) bool {
	if v == "" {
		return false
	}
	if len(v) > maxProseWord {
		return true
	}
	// schemePattern's value class admits only ASCII letters, digits and the
	// separators, so every rune here is one byte and "not a letter" means
	// "a character no English word contains".
	for i := range len(v) {
		switch c := v[i]; {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
			if i > 0 {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// maskPairs masks the value of every credential-named name=value or name: value
// pair, rebuilding the string around the value spans so names, separators and
// surrounding punctuation survive untouched.
func maskPairs(s string) string {
	matches := pairPattern.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}

	var b strings.Builder
	written := 0
	for _, m := range matches {
		nameStart, nameEnd, valueStart, valueEnd := m[2], m[3], m[4], m[5]
		if valueStart < 0 || valueEnd <= valueStart {
			continue
		}
		if !IsCredentialKey(s[nameStart:nameEnd]) {
			continue
		}
		valueEnd = extendValue(s, valueEnd)

		value := s[valueStart:valueEnd]
		if value == Mask {
			// Already redacted: masking again must not change the string.
			continue
		}
		if isAuthScheme(value) {
			// maskSchemes has already handled what follows the scheme, and the
			// scheme itself is placement evidence.
			continue
		}

		b.WriteString(s[written:valueStart])
		b.WriteString(Mask)
		written = valueEnd
	}
	if written == 0 {
		return s
	}
	b.WriteString(s[written:])
	return b.String()
}

// extendValue widens a matched value span now that the name is known to be a
// credential. pairPattern stops at "/" and "?" so that scanning a URL finds the
// parameters inside it; a credential value, by contrast, may legitimately
// contain both — base64 and JWTs do.
func extendValue(s string, end int) int {
	for end < len(s) && !strings.ContainsRune(valueTerminatorRunes, rune(s[end])) {
		end++
	}
	return end
}

// maskVendorKeys masks a bare provider key that no credential-shaped name
// accompanies.
func maskVendorKeys(s string) string {
	return vendorKeyPattern.ReplaceAllStringFunc(s, func(match string) string {
		if !digitPattern.MatchString(match) {
			return match
		}
		return Mask
	})
}

// Fingerprint returns the first eight hex characters of a domain-separated
// SHA-256 of a credential, for AuthObservation.Fingerprint. An empty credential
// fingerprints to the empty string: absence must not look like a presented key.
func Fingerprint(credential string) string {
	if credential == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fingerprintDomain + credential))
	return hex.EncodeToString(sum[:])[:8]
}
