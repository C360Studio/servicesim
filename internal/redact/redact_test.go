package redact_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/internal/redact"
)

// secret is the literal that must not survive any path through this package. It
// carries a vendor prefix and a digit so the free-text matcher can recognise it
// even when no credential-shaped name accompanies it.
const secret = "sk-live-SUPERSECRET1234567890"

// altSecret has no vendor prefix, so it is only ever caught by a credential
// name. Cases that use it prove the name matcher, not the prefix matcher.
const altSecret = "hunter2-correct-horse-battery"

func assertMasked(t *testing.T, path, got, leaked string) {
	t.Helper()
	if strings.Contains(got, leaked) {
		t.Fatalf("%s: credential survived redaction\n got: %s\nleak: %s", path, got, leaked)
	}
	if !strings.Contains(got, redact.Mask) {
		t.Fatalf("%s: no mask in output, so nothing was redacted\ngot: %s", path, got)
	}
}

// TestNoCredentialReachesARetainedStructure is the test that matters. Each case
// is one path a credential can take into something Servicesim keeps: a header,
// a JSON property at any depth, a query parameter, URL userinfo, a body that is
// not valid JSON, or free text derived from a request.
func TestNoCredentialReachesARetainedStructure(t *testing.T) {
	tests := []struct {
		name   string
		leaked string
		got    func() string
	}{
		{
			name:   "authorization header",
			leaked: altSecret,
			got:    func() string { return redact.HeaderValue("Authorization", "Bearer "+altSecret) },
		},
		{
			name:   "authorization header without a scheme",
			leaked: altSecret,
			got:    func() string { return redact.HeaderValue("Authorization", altSecret) },
		},
		{
			name:   "x-api-key header",
			leaked: altSecret,
			got:    func() string { return redact.HeaderValue("X-Api-Key", altSecret) },
		},
		{
			name:   "vendor prefixed header",
			leaked: altSecret,
			got:    func() string { return redact.HeaderValue("X-Exa-Api-Key", altSecret) },
		},
		{
			name:   "cookie header",
			leaked: altSecret,
			got:    func() string { return redact.HeaderValue("Cookie", "session="+altSecret) },
		},
		{
			name:   "header map",
			leaked: altSecret,
			got: func() string {
				h := http.Header{
					"Authorization": {"Bearer " + altSecret},
					"X-Api-Key":     {altSecret},
					"Api_key":       {altSecret},
				}
				return headerString(redact.Headers(h))
			},
		},
		{
			name:   "non credential header carrying a credential anyway",
			leaked: secret,
			got:    func() string { return redact.HeaderValue("X-Debug-Note", "retrying with "+secret) },
		},
		{
			name:   "json api_key at top level",
			leaked: altSecret,
			got:    func() string { return string(redact.JSONBytes([]byte(`{"api_key":"` + altSecret + `"}`))) },
		},
		{
			name:   "json apiKey at top level",
			leaked: altSecret,
			got:    func() string { return string(redact.JSONBytes([]byte(`{"apiKey":"` + altSecret + `"}`))) },
		},
		{
			name:   "json API_KEY at top level",
			leaked: altSecret,
			got:    func() string { return string(redact.JSONBytes([]byte(`{"API_KEY":"` + altSecret + `"}`))) },
		},
		{
			name:   "json token at top level",
			leaked: altSecret,
			got:    func() string { return string(redact.JSONBytes([]byte(`{"token":"` + altSecret + `"}`))) },
		},
		{
			name:   "json secret at top level",
			leaked: altSecret,
			got:    func() string { return string(redact.JSONBytes([]byte(`{"secret":"` + altSecret + `"}`))) },
		},
		{
			name:   "json credential nested three deep",
			leaked: altSecret,
			got: func() string {
				body := `{"outer":{"middle":{"api_key":"` + altSecret + `"}}}`
				return string(redact.JSONBytes([]byte(body)))
			},
		},
		{
			name:   "json credential inside an array of objects",
			leaked: altSecret,
			got: func() string {
				body := `{"calls":[{"url":"https://a.example"},{"apiKey":"` + altSecret + `"}]}`
				return string(redact.JSONBytes([]byte(body)))
			},
		},
		{
			name:   "json credential key holding an array of strings",
			leaked: altSecret,
			got:    func() string { return string(redact.JSONBytes([]byte(`{"api_keys":["` + altSecret + `"]}`))) },
		},
		{
			name:   "json credential key holding an object",
			leaked: altSecret,
			got: func() string {
				body := `{"credentials":{"name":"prod","value":"` + altSecret + `"}}`
				return string(redact.JSONBytes([]byte(body)))
			},
		},
		{
			name:   "json credential key holding a number",
			leaked: "8675309123456",
			got:    func() string { return string(redact.JSONBytes([]byte(`{"api_key":8675309123456}`))) },
		},
		{
			name:   "json free text value quoting a credential",
			leaked: secret,
			got: func() string {
				body := `{"query":"why did ` + secret + ` stop working"}`
				return string(redact.JSONBytes([]byte(body)))
			},
		},
		{
			name:   "json credential as a property name",
			leaked: secret,
			got:    func() string { return string(redact.JSONBytes([]byte(`{"` + secret + `":{"used":true}}`))) },
		},
		{
			name:   "query credential as a parameter name",
			leaked: secret,
			got:    func() string { return redact.Query(secret + "=1&q=weather") },
		},
		{
			name:   "query string",
			leaked: altSecret,
			got:    func() string { return redact.Query("api_key=" + altSecret + "&query=weather") },
		},
		{
			name:   "query string with a vendor prefixed parameter name",
			leaked: altSecret,
			got:    func() string { return redact.Query("x-exa-api-key=" + altSecret) },
		},
		{
			name:   "query string that will not parse",
			leaked: altSecret,
			got:    func() string { return redact.Query("api_key=" + altSecret + "&bad=%zz") },
		},
		{
			name:   "url userinfo in free text",
			leaked: secret,
			got: func() string {
				return redact.String("POST http://user:" + secret + "@exa.test/search HTTP/1.1")
			},
		},
		{
			name:   "url userinfo inside a json value",
			leaked: secret,
			got: func() string {
				body := `{"upstream":"https://user:` + secret + `@exa.test/search"}`
				return string(redact.JSONBytes([]byte(body)))
			},
		},
		{
			name:   "malformed json body: trailing comma",
			leaked: altSecret,
			got: func() string {
				body := `{"query":"weather","api_key":"` + altSecret + `",}`
				return string(redact.JSONBytes([]byte(body)))
			},
		},
		{
			name:   "malformed json body: truncated mid document",
			leaked: altSecret,
			got:    func() string { return string(redact.JSONBytes([]byte(`{"api_key":"` + altSecret + `"`))) },
		},
		{
			name:   "malformed json body: trailing garbage after a valid document",
			leaked: altSecret,
			got:    func() string { return string(redact.JSONBytes([]byte(`{"api_key":"` + altSecret + `"} oops`))) },
		},
		{
			name:   "form encoded body",
			leaked: altSecret,
			got:    func() string { return string(redact.JSONBytes([]byte("api_key=" + altSecret + "&query=weather"))) },
		},
		{
			name:   "error message wrapping a raw request body",
			leaked: altSecret,
			got: func() string {
				msg := `decode body {"api_key": "` + altSecret + `", "query": "weather"}: unexpected EOF`
				return redact.String(msg)
			},
		},
		{
			name:   "finding message quoting a bearer token",
			leaked: altSecret,
			got:    func() string { return redact.String("received header Authorization: Bearer " + altSecret) },
		},
		{
			name:   "finding message quoting basic auth",
			leaked: "dXNlcjpwYXNzd29yZA==",
			got:    func() string { return redact.String("received Basic dXNlcjpwYXNzd29yZA==") },
		},
		{
			name:   "finding message quoting a bare vendor key",
			leaked: secret,
			got:    func() string { return redact.String("upstream rejected " + secret + " with 401") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertMasked(t, tt.name, tt.got(), tt.leaked)
		})
	}
}

// TestEveryFunctionIsIdempotent locks the property provider.Handle relies on:
// it redacts before logging and journal.Append redacts again at the storage
// boundary, so a second pass must be a no-op.
func TestEveryFunctionIsIdempotent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		fn    func(string) string
	}{
		{"header value with scheme", "Bearer " + secret, func(s string) string {
			return redact.HeaderValue("Authorization", s)
		}},
		{"header value without scheme", secret, func(s string) string {
			return redact.HeaderValue("X-Api-Key", s)
		}},
		{"non credential header", "retrying with " + secret, func(s string) string {
			return redact.HeaderValue("X-Debug-Note", s)
		}},
		{"query", "api_key=" + secret + "&query=weather", redact.Query},
		{"query that will not parse", "api_key=" + secret + "&bad=%zz", redact.Query},
		{"json bytes", `{"api_key":"` + secret + `","query":"weather"}`, func(s string) string {
			return string(redact.JSONBytes([]byte(s)))
		}},
		{"non json bytes", "api_key=" + secret + "&query=weather", func(s string) string {
			return string(redact.JSONBytes([]byte(s)))
		}},
		{"string with a bearer token", "Authorization: Bearer " + secret, redact.String},
		{"string with a pair", `{"api_key": "` + secret + `"}`, redact.String},
		{"string with userinfo", "POST http://user:" + secret + "@exa.test/search", redact.String},
		{"string with a bare vendor key", "rejected " + secret, redact.String},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			once := tt.fn(tt.input)
			twice := tt.fn(once)
			if once != twice {
				t.Fatalf("not idempotent\nonce:  %s\ntwice: %s", once, twice)
			}
			if strings.Contains(once, secret) {
				t.Fatalf("credential survived the first pass: %s", once)
			}
		})
	}
}

// TestJSON_DoesNotRedactAuthorField guards the single most likely redaction bug
// in this repository: "author" is a documented result field on both Exa and
// Tavily, and a substring match on "auth" would corrupt it.
func TestJSON_DoesNotRedactAuthorField(t *testing.T) {
	body := `{"results":[{"author":"Ada Lovelace","authors":["Ada"],"title":"On Engines",` +
		`"url":"https://example.com/a","published_date":"1843-01-01"}]}`

	got := string(redact.JSONBytes([]byte(body)))

	for _, want := range []string{"Ada Lovelace", "On Engines", "https://example.com/a", "1843-01-01"} {
		if !strings.Contains(got, want) {
			t.Fatalf("redaction destroyed evidence %q\ngot: %s", want, got)
		}
	}
	if strings.Contains(got, redact.Mask) {
		t.Fatalf("nothing here is a credential, yet something was masked: %s", got)
	}
}

func TestIsCredentialKey(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"api_key", true},
		{"apiKey", true},
		{"API_KEY", true},
		{"api-key", true},
		{"x_api_key", true},
		{"token", true},
		{"access_token", true},
		{"refreshToken", true},
		{"id_token", true},
		{"secret", true},
		{"client_secret", true},
		{"secretKey", true},
		{"password", true},
		{"passwd", true},
		{"pwd", true},
		{"authorization", true},
		{"auth", true},
		{"key", true},
		{"credential", true},
		{"credentials", true},
		{"signature", true},
		{"sig", true},
		{"session_id", true},
		{"cookie", true},
		{"private_key", true},

		{"author", false},
		{"authors", false},
		{"autocomplete", false},
		{"keywords", false},
		{"keyword", false},
		{"tokenizer", false},
		// Perplexity's usage block. Redacting these would corrupt the evidence a
		// consumer asserts its token accounting against.
		{"tokens", false},
		{"max_tokens", false},
		{"prompt_tokens", false},
		{"completion_tokens", false},
		{"total_tokens", false},
		{"title", false},
		{"url", false},
		{"query", false},
		{"published_date", false},
		{"score", false},
		{"answer", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redact.IsCredentialKey(tt.name); got != tt.want {
				t.Fatalf("IsCredentialKey(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestIsCredentialHeader_VendorPrefixedVariants covers the header names an
// adapter under test actually invents. An exact-only header list would miss
// every one of them.
func TestIsCredentialHeader_VendorPrefixedVariants(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Authorization", true},
		{"authorization", true},
		{"Proxy-Authorization", true},
		{"X-Api-Key", true},
		{"x-api-key", true},
		{"X-Exa-Api-Key", true},
		{"X-Tavily-Api-Key", true},
		{"Api_Key", true},
		{"X-Api-Key-2", true},
		{"X-Auth-Token", true},
		{"X-Goog-Api-Key", true},
		{"X-Amz-Security-Token", true},
		{"Cookie", true},
		{"Set-Cookie", true},

		{"Content-Type", false},
		{"Accept", false},
		{"User-Agent", false},
		{"X-Request-Id", false},
		{"X-Author", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redact.IsCredentialHeader(tt.name); got != tt.want {
				t.Fatalf("IsCredentialHeader(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestHeaderValue_PreservesAuthorizationScheme keeps the evidence an adapter
// contract test asserts on: placement, not value.
func TestHeaderValue_PreservesAuthorizationScheme(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{"bearer", "Authorization", "Bearer " + secret, "Bearer " + redact.Mask},
		{"basic", "Authorization", "Basic dXNlcjpwYXNz", "Basic " + redact.Mask},
		{"lower case scheme", "authorization", "bearer " + secret, "bearer " + redact.Mask},
		{"no scheme", "Authorization", secret, redact.Mask},
		{"proxy authorization", "Proxy-Authorization", "Bearer " + secret, "Bearer " + redact.Mask},
		{"x-api-key has no scheme", "X-Api-Key", secret, redact.Mask},
		{"empty value stays empty", "Authorization", "", ""},
		{"non credential header untouched", "Content-Type", "application/json", "application/json"},
		{"non credential header with charset", "Content-Type", "application/json; charset=utf-8", "application/json; charset=utf-8"},
		{"accept untouched", "Accept", "*/*", "*/*"},
		{"user agent untouched", "User-Agent", "servicesim-test/1.0 (+https://example.com)", "servicesim-test/1.0 (+https://example.com)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redact.HeaderValue(tt.key, tt.value); got != tt.want {
				t.Fatalf("HeaderValue(%q, %q) = %q, want %q", tt.key, tt.value, got, tt.want)
			}
		})
	}
}

func TestHeaders_DoesNotMutateTheCaller(t *testing.T) {
	in := http.Header{
		"Authorization": {"Bearer " + secret},
		"X-Api-Key":     {secret, secret},
		"Accept":        {"application/json"},
	}

	out := redact.Headers(in)

	if got := in.Get("Authorization"); got != "Bearer "+secret {
		t.Fatalf("input header was mutated: %q", got)
	}
	if got := out.Get("Authorization"); got != "Bearer "+redact.Mask {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer "+redact.Mask)
	}
	if got := out["X-Api-Key"]; !reflect.DeepEqual(got, []string{redact.Mask, redact.Mask}) {
		t.Fatalf("X-Api-Key = %v, want both values masked", got)
	}
	if got := out.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want it untouched", got)
	}
	if redact.Headers(nil) != nil {
		t.Fatal("Headers(nil) should stay nil so a caller can store it unchanged")
	}
}

func TestQuery_IsDeterministicAndPreservesParameterNames(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"no credentials", "q=weather&num=5", "num=5&q=weather"},
		{"credential value masked, name kept", "api_key=" + altSecret + "&q=weather", "api_key=" + redact.Mask + "&q=weather"},
		{"sorted regardless of input order", "z=1&api_key=" + altSecret + "&a=2", "a=2&api_key=" + redact.Mask + "&z=1"},
		{"repeated credential parameter", "token=" + altSecret + "&token=" + altSecret, "token=" + redact.Mask + "&token=" + redact.Mask},
		{"empty credential value stays empty", "api_key=&q=weather", "api_key=&q=weather"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redact.Query(tt.raw)
			if got != tt.want {
				t.Fatalf("Query(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			// Whatever comes out must still parse, or the journal has produced a
			// query string a consumer cannot read back.
			if _, err := url.ParseQuery(got); err != nil {
				t.Fatalf("Query(%q) produced an unparseable result %q: %v", tt.raw, got, err)
			}
		})
	}
}

func TestJSONBytes_PreservesShapeAndNumbers(t *testing.T) {
	body := `{"query":"weather","num_results":10,"ratio":0.125,"live":true,"missing":null,"tags":["a","b"]}`

	got := string(redact.JSONBytes([]byte(body)))

	for _, want := range []string{`"num_results":10`, `"ratio":0.125`, `"live":true`, `"missing":null`, `["a","b"]`} {
		if !strings.Contains(got, want) {
			t.Fatalf("JSONBytes changed %s\ngot: %s", want, got)
		}
	}
}

func TestJSONBytes_IsDeterministic(t *testing.T) {
	body := []byte(`{"z":1,"a":2,"api_key":"` + altSecret + `","m":{"y":1,"b":2}}`)

	first := string(redact.JSONBytes(body))
	for range 32 {
		if got := string(redact.JSONBytes(body)); got != first {
			t.Fatalf("JSONBytes is not deterministic:\nfirst: %s\nlater: %s", first, got)
		}
	}
	want := `{"a":2,"api_key":"` + redact.Mask + `","m":{"b":2,"y":1},"z":1}`
	if first != want {
		t.Fatalf("JSONBytes = %s, want %s", first, want)
	}
}

func TestJSONBytes_EmptyBodyStaysEmpty(t *testing.T) {
	for _, raw := range [][]byte{nil, {}, []byte("   ")} {
		if got := redact.JSONBytes(raw); len(got) != len(raw) {
			t.Fatalf("JSONBytes(%q) = %q, want it unchanged", raw, got)
		}
	}
}

// TestJSONBytes_FormEncodedCredentialIsMasked covers the non-JSON branch that a
// real adapter reaches by sending the wrong content type.
func TestJSONBytes_FormEncodedCredentialIsMasked(t *testing.T) {
	got := string(redact.JSONBytes([]byte("api_key=" + altSecret + "&query=weather")))

	want := "api_key=" + redact.Mask + "&query=weather"
	if got != want {
		t.Fatalf("JSONBytes = %q, want %q", got, want)
	}
}

// TestJSONBytes_TruncatedJSONCredentialIsMasked covers the other half of the
// non-JSON branch: returning raw here would store the credential byte for byte.
func TestJSONBytes_TruncatedJSONCredentialIsMasked(t *testing.T) {
	raw := []byte(`{"query":"weather","api_key":"` + altSecret + `"`)

	got := string(redact.JSONBytes(raw))

	if strings.Contains(got, altSecret) {
		t.Fatalf("truncated body stored verbatim: %s", got)
	}
	if !strings.Contains(got, `"query":"weather"`) {
		t.Fatalf("the masked body should still be readable evidence: %s", got)
	}
}

func TestJSON_RebuildsRatherThanMutates(t *testing.T) {
	var in any
	if err := json.Unmarshal([]byte(`{"api_key":"`+altSecret+`","nested":{"token":"`+altSecret+`"},"list":[{"secret":"`+altSecret+`"}]}`), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out := redact.JSON(in)

	original, ok := in.(map[string]any)
	if !ok {
		t.Fatalf("test setup: input is %T", in)
	}
	if original["api_key"] != altSecret {
		t.Fatalf("JSON mutated the caller's map: %v", original["api_key"])
	}
	nested, ok := original["nested"].(map[string]any)
	if !ok {
		t.Fatalf("test setup: nested is %T", original["nested"])
	}
	if nested["token"] != altSecret {
		t.Fatalf("JSON mutated a nested map belonging to the caller: %v", nested["token"])
	}

	redacted, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("JSON returned %T, want map[string]any", out)
	}
	if redacted["api_key"] != redact.Mask {
		t.Fatalf("api_key = %v, want %s", redacted["api_key"], redact.Mask)
	}
}

func TestJSON_PassesThroughNonJSONValues(t *testing.T) {
	if got := redact.JSON(nil); got != nil {
		t.Fatalf("JSON(nil) = %v, want nil", got)
	}
	if got := redact.JSON(true); got != true {
		t.Fatalf("JSON(true) = %v, want true", got)
	}
	if got := redact.JSON("weather"); got != "weather" {
		t.Fatalf("JSON(%q) = %v, want it untouched", "weather", got)
	}
}

func TestString_LeavesInnocentTextAlone(t *testing.T) {
	tests := []string{
		"",
		"missing required field query",
		"author: Ada Lovelace",
		"expected content-type application/json, got text/plain",
		"unexpected end of JSON input",
		"num_results=10",
		"https://exa.test/search",
		"score=0.87",
	}

	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			if got := redact.String(in); got != in {
				t.Fatalf("String(%q) = %q, want it untouched", in, got)
			}
		})
	}
}

func TestString_MasksCredentialShapedText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"pair with equals", "api_key=" + altSecret, "api_key=" + redact.Mask},
		{"pair with colon", "api_key: " + altSecret, "api_key: " + redact.Mask},
		{"quoted json pair", `"api_key": "` + altSecret + `"`, `"api_key": "` + redact.Mask + `"`},
		{"bearer token", "Authorization: Bearer " + altSecret, "Authorization: Bearer " + redact.Mask},
		{"base64 value keeps going past a slash", "token=YWJj/ZGVm+Z2hp==", "token=" + redact.Mask},
		{"userinfo", "http://user:" + altSecret + "@exa.test/s", "http://user:" + redact.Mask + "@exa.test/s"},
		{"bare vendor key", "rejected " + secret, "rejected " + redact.Mask},
		{"non credential pair beside a credential pair", "q=weather&api_key=" + altSecret, "q=weather&api_key=" + redact.Mask},
		{"credential inside a url query", "https://exa.test/s?q=x&api_key=" + altSecret, "https://exa.test/s?q=x&api_key=" + redact.Mask},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redact.String(tt.in); got != tt.want {
				t.Fatalf("String(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestString_TellsRequirementTextFromCredentials is the regression test for the
// finding message a consumer read as evidence of a credential that was never
// presented: "Authorization: Bearer is required" was journaled as
// "Authorization: Bearer [REDACTED] required".
//
// Both halves are asserted in one table on purpose. Relaxing what follows a
// scheme name is only correct if a credential in the same position is still
// masked, so every prose case sits beside a credential case that must not
// regress, and every case is redacted twice: the journal redacts again at the
// storage boundary, so a second pass must change nothing.
func TestString_TellsRequirementTextFromCredentials(t *testing.T) {
	// The prose cases are the live finding messages this repository emits, verbatim
	// from provider/tavily/request.go and provider/perplexity/request.go. Four of
	// the five were mangled, which is why the fix cannot be a rule about the phrase
	// "is required".
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "requirement sentence",
			in:   "Authorization: Bearer is required",
			want: "Authorization: Bearer is required",
		},
		{
			name: "scheme followed by a verb",
			in:   "x-api-key is not accepted; only Authorization: Bearer authenticates",
			want: "x-api-key is not accepted; only Authorization: Bearer authenticates",
		},
		{
			name: "scheme followed by a noun",
			in:   "Authorization does not carry the documented Bearer scheme",
			want: "Authorization does not carry the documented Bearer scheme",
		},
		{
			name: "scheme followed by the word credential",
			in:   "no Authorization: Bearer credential was presented",
			want: "no Authorization: Bearer credential was presented",
		},
		{
			name: "scheme named as a capitalised word",
			in:   "Basic Auth is not accepted by this API",
			want: "Basic Auth is not accepted by this API",
		},
		{
			name: "bearer token in a finding message",
			in:   "received header Authorization: Bearer " + altSecret,
			want: "received header Authorization: Bearer " + redact.Mask,
		},
		{
			name: "vendor prefixed bearer token in a finding message",
			in:   "upstream rejected Authorization: Bearer " + secret,
			want: "upstream rejected Authorization: Bearer " + redact.Mask,
		},
		{
			name: "basic credential in a finding message",
			in:   "received Basic dXNlcjpwYXNzd29yZA==",
			want: "received Basic " + redact.Mask,
		},
		{
			name: "interior capital is not a word",
			in:   "Authorization: Bearer aBcDeFgHiJkL",
			want: "Authorization: Bearer " + redact.Mask,
		},
		{
			name: "letters only but longer than any word",
			in:   "Authorization: Bearer abcdefghijklmnopqrstuvwxyz",
			want: "Authorization: Bearer " + redact.Mask,
		},
		{
			name: "x-api-key value in a finding message",
			in:   "x-api-key: " + altSecret + " is not accepted",
			want: "x-api-key: " + redact.Mask + " is not accepted",
		},
		{
			name: "x-api-key value quoted with its header name",
			in:   "received header x-api-key=" + altSecret,
			want: "received header x-api-key=" + redact.Mask,
		},
		{
			name: "body embedded key in a finding message",
			in:   `body {"api_key": "` + altSecret + `"} placed the key in the body`,
			want: `body {"api_key": "` + redact.Mask + `"} placed the key in the body`,
		},
		{
			name: "body embedded key in a form encoded body",
			in:   "body api_key=" + altSecret + "&query=weather was rejected",
			want: "body api_key=" + redact.Mask + "&query=weather was rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redact.String(tt.in)
			if got != tt.want {
				t.Fatalf("String(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if twice := redact.String(got); twice != got {
				t.Fatalf("String is not idempotent\nonce:  %q\ntwice: %q", got, twice)
			}
		})
	}
}

// TestHeaderValue_MasksAWordShapedCredential pins the half of the scheme rule
// that must not follow free text: in an Authorization header the token after the
// scheme is the credential by definition, whatever it is shaped like.
func TestHeaderValue_MasksAWordShapedCredential(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"lower case word", "Bearer opensesame", "Bearer " + redact.Mask},
		{"the word required", "Bearer required", "Bearer " + redact.Mask},
		{"capitalised word", "Basic Secret", "Basic " + redact.Mask},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redact.HeaderValue("Authorization", tt.value); got != tt.want {
				t.Fatalf("HeaderValue(Authorization, %q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	first := redact.Fingerprint(secret)

	if len(first) != 8 {
		t.Fatalf("Fingerprint(%q) = %q, want eight characters", secret, first)
	}
	if strings.ContainsAny(first, "ghijklmnopqrstuvwxyzGHIJKLMNOPQRSTUVWXYZ") {
		t.Fatalf("Fingerprint = %q, want lower-case hex", first)
	}
	if redact.Fingerprint(secret) != first {
		t.Fatal("Fingerprint is not deterministic")
	}
	if strings.Contains(first, secret) {
		t.Fatal("Fingerprint leaked the credential")
	}
	if redact.Fingerprint(altSecret) == first {
		t.Fatal("distinct credentials produced the same fingerprint")
	}
	if got := redact.Fingerprint(""); got != "" {
		t.Fatalf("Fingerprint(\"\") = %q, want empty: absence is not a credential", got)
	}
	// Domain separation: the value must not be a bare SHA-256 prefix, or anyone
	// holding the journal could confirm a guessed credential with sha256sum.
	bare := sha256.Sum256([]byte(secret))
	if first == hex.EncodeToString(bare[:])[:8] {
		t.Fatal("Fingerprint is an undomained SHA-256 prefix")
	}
}

// FuzzIdempotence exercises the property the whole two-point redaction scheme
// rests on — provider.Handle redacts before logging, journal.Append redacts
// again at the storage boundary — against inputs a table would not think of.
// Its seed corpus runs on every go test; the fuzzer only widens it.
func FuzzIdempotence(f *testing.F) {
	seeds := []string{
		"",
		"api_key=" + secret,
		"api_key=" + redact.Mask,
		"api_key=" + redact.Mask + "x",
		`{"api_key":"` + secret + `"}`,
		`{"api_key":"` + redact.Mask + `"}`,
		"Authorization: Bearer " + secret,
		"Authorization: Bearer " + redact.Mask,
		"http://user:" + secret + "@exa.test/search?api_key=" + secret,
		"secret: Bearer",
		// Prose that names a scheme: the redactor must leave these alone, and must
		// still leave them alone on the second pass.
		"Authorization: Bearer is required",
		"no Authorization: Bearer credential was presented",
		"token=YWJj/ZGVm+Z2hp==&q=1",
		"a=1;b=2,c=3)d=4}e=5",
		"key:::::" + secret,
		"?????api_key=" + secret,
		"[[[api_key=" + secret + "]]]",
		// Both found by the fuzzer. The first proved that a query which only
		// parses *after* masking must be re-parsed, the second that the value
		// character class and the terminator set must agree exactly.
		"token=AAAA%0X00000000 000",
		"token=AAAC/001b'000000000000000",
		"{\"api_key\":\"" + secret + "\x00 \"}",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		once := redact.String(in)
		if twice := redact.String(once); twice != once {
			t.Fatalf("String is not idempotent\nin:    %q\nonce:  %q\ntwice: %q", in, once, twice)
		}

		onceBytes := string(redact.JSONBytes([]byte(in)))
		if twiceBytes := string(redact.JSONBytes([]byte(onceBytes))); twiceBytes != onceBytes {
			t.Fatalf("JSONBytes is not idempotent\nin:    %q\nonce:  %q\ntwice: %q", in, onceBytes, twiceBytes)
		}

		onceQuery := redact.Query(in)
		if twiceQuery := redact.Query(onceQuery); twiceQuery != onceQuery {
			t.Fatalf("Query is not idempotent\nin:    %q\nonce:  %q\ntwice: %q", in, onceQuery, twiceQuery)
		}
	})
}

// vendorKeyOracle is an independent restatement of what a provider key looks
// like. FuzzNoVendorKeySurvives uses it to assert that nothing this package
// returns still contains one, whichever branch the input took.
var (
	vendorKeyOracle = regexp.MustCompile(`(?i)\b(sk|pplx|tvly)[-_][A-Za-z0-9_\-]{12,}`)
	digitOracle     = regexp.MustCompile(`[0-9]`)
)

func FuzzNoVendorKeySurvives(f *testing.F) {
	seeds := []string{
		secret,
		"q=" + secret,
		`{"query":"` + secret + `"}`,
		`{"` + secret + `":1}`,
		`{"nested":{"list":["` + secret + `"]}}`,
		`{"api_key":"` + secret + `"} trailing`,
		"http://user:" + secret + "@exa.test/s",
		`{"a":"sk-live-ABCDEF123456789"}`,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		outputs := []struct {
			name string
			out  string
		}{
			{"String", redact.String(in)},
			{"Query", redact.Query(in)},
			{"JSONBytes", string(redact.JSONBytes([]byte(in)))},
			{"HeaderValue", redact.HeaderValue("X-Trace", in)},
		}
		for _, o := range outputs {
			for _, match := range vendorKeyOracle.FindAllString(o.out, -1) {
				if !digitOracle.MatchString(match) {
					continue
				}
				t.Fatalf("%s left a provider key in its output\nin:    %q\nout:   %q\nmatch: %q",
					o.name, in, o.out, match)
			}
		}
	})
}

func headerString(h http.Header) string {
	var b strings.Builder
	for _, name := range sortedKeys(h) {
		for _, v := range h[name] {
			b.WriteString(name)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func sortedKeys(h http.Header) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
