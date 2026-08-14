package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/httpx"
	"github.com/c360studio/servicesim/internal/journal"
)

const testKey = "sk-test-abcdefghijklmnop"

func requestWithHeaders(headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{}`))
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	return r
}

func TestExtractCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers map[string]string
		want    []httpx.Credential
	}{
		{name: "no credentials", headers: nil},
		{
			name:    "bearer authorization",
			headers: map[string]string{"Authorization": "Bearer " + testKey},
			want:    []httpx.Credential{{Header: "authorization", Scheme: "Bearer", Value: testKey}},
		},
		{
			name:    "lower case scheme is canonicalised",
			headers: map[string]string{"Authorization": "bearer " + testKey},
			want:    []httpx.Credential{{Header: "authorization", Scheme: "Bearer", Value: testKey}},
		},
		{
			name:    "upper case scheme is canonicalised",
			headers: map[string]string{"Authorization": "BEARER " + testKey},
			want:    []httpx.Credential{{Header: "authorization", Scheme: "Bearer", Value: testKey}},
		},
		{
			name:    "extra whitespace after the scheme",
			headers: map[string]string{"Authorization": "Bearer    " + testKey + "  "},
			want:    []httpx.Credential{{Header: "authorization", Scheme: "Bearer", Value: testKey}},
		},
		{
			name:    "basic is a recognised scheme",
			headers: map[string]string{"Authorization": "Basic dXNlcjpwYXNz"},
			want:    []httpx.Credential{{Header: "authorization", Scheme: "Basic", Value: "dXNlcjpwYXNz"}},
		},
		{
			name:    "opaque authorization has no scheme",
			headers: map[string]string{"Authorization": testKey},
			want:    []httpx.Credential{{Header: "authorization", Scheme: "", Value: testKey}},
		},
		{
			name:    "unrecognised scheme keeps the whole value",
			headers: map[string]string{"Authorization": "Token " + testKey},
			want:    []httpx.Credential{{Header: "authorization", Scheme: "", Value: "Token " + testKey}},
		},
		{
			name:    "x-api-key",
			headers: map[string]string{"X-Api-Key": testKey},
			want:    []httpx.Credential{{Header: "x-api-key", Scheme: "", Value: testKey}},
		},
		{
			name: "both placements come back in header order",
			headers: map[string]string{
				"X-Api-Key":     testKey + "-x",
				"Authorization": "Bearer " + testKey + "-a",
			},
			want: []httpx.Credential{
				{Header: "authorization", Scheme: "Bearer", Value: testKey + "-a"},
				{Header: "x-api-key", Scheme: "", Value: testKey + "-x"},
			},
		},
		{name: "bare scheme carries no credential", headers: map[string]string{"Authorization": "Bearer"}},
		{name: "scheme with only whitespace after it", headers: map[string]string{"Authorization": "Bearer   "}},
		{name: "empty authorization", headers: map[string]string{"Authorization": ""}},
		{name: "whitespace-only x-api-key", headers: map[string]string{"X-Api-Key": "   "}},
		{name: "an unrelated header is not a credential", headers: map[string]string{"X-Request-Id": "abc"}},
		{name: "cookie is not a recognised placement", headers: map[string]string{"Cookie": "session=" + testKey}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := httpx.ExtractCredentials(requestWithHeaders(tt.headers))

			assert.Empty(t, cmp.Diff(tt.want, got))
		})
	}
}

func TestExtractCredentials_OrderIsStableAcrossCalls(t *testing.T) {
	t.Parallel()

	// Two placements on one request: without a fixed order this is exactly where
	// Go's randomised map iteration would reach the journal's Auth.Header.
	r := requestWithHeaders(map[string]string{
		"Authorization": "Bearer " + testKey,
		"X-Api-Key":     testKey,
	})

	first := httpx.ExtractCredentials(r)
	require.Len(t, first, 2)

	for range 64 {
		assert.Empty(t, cmp.Diff(first, httpx.ExtractCredentials(r)))
	}
}

func TestExtractCredentials_NilRequest(t *testing.T) {
	t.Parallel()

	assert.Empty(t, httpx.ExtractCredentials(nil))
}

func TestObserve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cred    httpx.Credential
		present bool
		want    journal.AuthObservation
	}{
		{
			name:    "absent credential observes nothing",
			cred:    httpx.Credential{},
			present: false,
			want:    journal.AuthObservation{},
		},
		{
			name:    "a credential is never carried into an absent observation",
			cred:    httpx.Credential{Header: "authorization", Scheme: "Bearer", Value: testKey},
			present: false,
			want:    journal.AuthObservation{},
		},
		{
			name:    "bearer placement",
			cred:    httpx.Credential{Header: "authorization", Scheme: "Bearer", Value: testKey},
			present: true,
			want: journal.AuthObservation{
				Present:     true,
				Header:      "authorization",
				Scheme:      "Bearer",
				Fingerprint: fingerprintOf(t, testKey),
			},
		},
		{
			name:    "x-api-key placement has no scheme",
			cred:    httpx.Credential{Header: "x-api-key", Value: testKey},
			present: true,
			want: journal.AuthObservation{
				Present:     true,
				Header:      "x-api-key",
				Fingerprint: fingerprintOf(t, testKey),
			},
		},
		{
			name:    "header name is lower-cased",
			cred:    httpx.Credential{Header: "X-Api-Key", Value: testKey},
			present: true,
			want: journal.AuthObservation{
				Present:     true,
				Header:      "x-api-key",
				Fingerprint: fingerprintOf(t, testKey),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Empty(t, cmp.Diff(tt.want, httpx.Observe(tt.cred, tt.present)))
		})
	}
}

// fingerprintOf derives the expected fingerprint through Observe itself, so this
// test pins Observe's contract (same key, same fingerprint) without duplicating
// redact's domain-separation constant, which would silently drift.
func fingerprintOf(t *testing.T, key string) string {
	t.Helper()

	got := httpx.Observe(httpx.Credential{Header: "authorization", Value: key}, true).Fingerprint
	require.NotEmpty(t, got)
	return got
}

func TestObserve_FingerprintIsStableAndDistinguishing(t *testing.T) {
	t.Parallel()

	a := httpx.Observe(httpx.Credential{Header: "authorization", Scheme: "Bearer", Value: testKey}, true)
	again := httpx.Observe(httpx.Credential{Header: "x-api-key", Value: testKey}, true)
	other := httpx.Observe(httpx.Credential{Header: "authorization", Scheme: "Bearer", Value: testKey + "-other"}, true)

	// The same key fingerprints identically wherever it was placed: that is what
	// lets a consumer assert two calls used one key.
	assert.Equal(t, a.Fingerprint, again.Fingerprint)
	assert.NotEqual(t, a.Fingerprint, other.Fingerprint)
	assert.Len(t, a.Fingerprint, 8)
}

func TestObserve_NeverCarriesTheCredential(t *testing.T) {
	t.Parallel()

	// The value must be absent from the observation by every path a reader has:
	// the fields, and the JSON the admin API and the log serialise.
	for _, cred := range []httpx.Credential{
		{Header: "authorization", Scheme: "Bearer", Value: testKey},
		{Header: "x-api-key", Value: testKey},
		{Header: "authorization", Value: testKey},
	} {
		obs := httpx.Observe(cred, true)

		assert.NotContains(t, obs.Fingerprint, testKey)
		assert.NotEqual(t, testKey, obs.Fingerprint)

		encoded, err := json.Marshal(obs)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), testKey)
	}
}

func TestObserve_EndToEndFromRequest(t *testing.T) {
	t.Parallel()

	r := requestWithHeaders(map[string]string{"Authorization": "Bearer " + testKey})

	creds := httpx.ExtractCredentials(r)
	require.Len(t, creds, 1)

	obs := httpx.Observe(creds[0], true)

	assert.True(t, obs.Present)
	assert.Equal(t, "authorization", obs.Header)
	assert.Equal(t, "Bearer", obs.Scheme)

	encoded, err := json.Marshal(obs)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), testKey)
}
