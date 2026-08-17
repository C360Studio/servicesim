package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/ids"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/internal/wire"
)

// --- Render ------------------------------------------------------------

// renderSample mirrors the shape a provider package renders: a few ordered
// struct fields plus a nested slice, the same shape internal/wire's own test
// suite uses.
type renderSample struct {
	RequestID string             `json:"requestId"`
	Count     json.Number        `json:"count"`
	Query     string             `json:"query"`
	Results   []renderSampleItem `json:"results"`
}

type renderSampleItem struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

func sampleValue() renderSample {
	return renderSample{
		RequestID: "b5947044c4b78efa9552a7c89b306d95",
		Count:     json.Number("1000000"),
		Query:     "climate & <policy>",
		Results:   []renderSampleItem{{Title: "First", URL: "https://alpha.example.com/a"}},
	}
}

// TestRenderMapKeyOrderIsStableAcrossRuns pins determinism: the same value and
// the same extra map render byte-identical output across many runs, so a
// consumer's golden compare cannot flake on Go's randomised map iteration.
func TestRenderMapKeyOrderIsStableAcrossRuns(t *testing.T) {
	t.Parallel()

	extra := map[string]any{"z_field": 1, "a_field": 2, "m_field": 3}

	first, err := Render(sampleValue(), extra, nil)
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		got, err := Render(sampleValue(), extra, nil)
		require.NoError(t, err)
		require.Equal(t, string(first), string(got), "run %d produced different bytes", i)
	}
}

// TestRenderPreservesIntegerLiteralNumberFidelity is house rule 2's numeric
// fidelity guarantee: a json.Number carrying "1000000" must not come back as
// exponent-form "1e+06" once it has round-tripped through the merge path,
// which is exactly the wire-contract change a naive float64 decode produces.
func TestRenderPreservesIntegerLiteralNumberFidelity(t *testing.T) {
	t.Parallel()

	got, err := Render(sampleValue(), map[string]any{"extra": true}, nil)
	require.NoError(t, err)
	assert.Contains(t, string(got), `"count":1000000`)
	assert.NotContains(t, string(got), "1e+06")
}

// TestRenderDoesNotEscapeHTML is house rule 2's escaping guarantee: real
// vendor responses do not escape "&", "<" or ">", and neither may this.
func TestRenderDoesNotEscapeHTML(t *testing.T) {
	t.Parallel()

	got, err := Render(sampleValue(), nil, nil)
	require.NoError(t, err)
	// The literal characters survive unescaped: a bare json.Marshal would
	// instead have rewritten them to the six-character unicode escapes below.
	assert.Contains(t, string(got), "climate & <policy>")
	assert.NotContains(t, string(got), "\\u0026")
	assert.NotContains(t, string(got), "\\u003c")
	assert.NotContains(t, string(got), "\\u003e")
}

// TestRenderMergesExtraFieldsAtTopLevel pins that extra is merged into the
// top-level object, not nested under a wrapper key.
func TestRenderMergesExtraFieldsAtTopLevel(t *testing.T) {
	t.Parallel()

	got, err := Render(sampleValue(), map[string]any{"newField": "value"}, nil)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(got, &decoded))
	assert.Equal(t, "value", decoded["newField"])
	assert.Equal(t, "b5947044c4b78efa9552a7c89b306d95", decoded["requestId"])
}

// TestRenderOmitsAfterMerge is the ordering case docs/scenario-schema.md
// documents: "the merge happens first and the omission second, so
// omit_fields can remove a key extra_fields added". This is the property
// that fixes exa's response-ordering bug (see provider/exa's
// TestRender_OmitFieldsWinsOverExtraFields for the end-to-end case).
func TestRenderOmitsAfterMerge(t *testing.T) {
	t.Parallel()

	// A key ONLY extra_fields adds, then omitted: proves omit runs after
	// merge, not before it — if omit ran first there would be nothing yet to
	// omit and the key would survive.
	got, err := Render(sampleValue(), map[string]any{"transient": "should not survive"}, []string{"transient"})
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(got, &decoded))
	_, present := decoded["transient"]
	assert.False(t, present, "a key added by extra_fields and named by omit_fields must not reach the wire")

	// A struct field, omitted: proves omission still applies to the base
	// value's own fields, not only to extra-added ones. A fresh map, because
	// json.Unmarshal into an already-populated map leaves keys the new
	// document does not mention untouched rather than clearing them.
	got, err = Render(sampleValue(), nil, []string{"query"})
	require.NoError(t, err)
	decoded = nil
	require.NoError(t, json.Unmarshal(got, &decoded))
	_, present = decoded["query"]
	assert.False(t, present)
}

// TestRenderOmitOfAbsentKeyIsNotAnError matches internal/wire.Omit's own
// contract: omission expresses "this must not reach the client", and a
// scenario that omits an already-absent field has got what it asked for.
func TestRenderOmitOfAbsentKeyIsNotAnError(t *testing.T) {
	t.Parallel()

	_, err := Render(sampleValue(), nil, []string{"noSuchField"})
	require.NoError(t, err)
}

// TestRenderNonObjectValueWithExtrasIsAnError matches what internal/wire does
// today: extra and omit are defined on object properties, so a value whose
// top level is not a JSON object has nothing to merge into.
func TestRenderNonObjectValueWithExtrasIsAnError(t *testing.T) {
	t.Parallel()

	_, err := Render([]string{"a", "b"}, map[string]any{"x": 1}, nil)
	require.Error(t, err)
}

// --- Hex32 / UUIDv5 / FloatIn --------------------------------------------

// TestHex32IsByteIdenticalToInternalIDs is the delegation guarantee: the
// goldens depend on Render/Hex32/UUIDv5/FloatIn producing exactly what
// internal/ids produces for the same parts, because that is what every
// shipped golden was verified against.
func TestHex32IsByteIdenticalToInternalIDs(t *testing.T) {
	t.Parallel()

	for _, parts := range [][]string{
		{},
		{"demo", "exa", "exa:/search"},
		{"golden-seed", "exa", "exa:/answer", "3"},
	} {
		assert.Equal(t, ids.Hex32(parts...), Hex32(parts...))
	}
	// Pinned literal, copied from internal/ids/ids_test.go's own
	// TestHex32IsPinned — which exists precisely so a derivation change
	// breaks loudly rather than silently rewriting every shipped golden's
	// requestId. provider.Hex32 must reproduce it exactly.
	assert.Equal(t, "f6d1be191056db0bb37501d374a8b338", Hex32("demo", "exa", "exa:/search"))
}

func TestUUIDv5IsByteIdenticalToInternalIDs(t *testing.T) {
	t.Parallel()

	for _, parts := range [][]string{
		{},
		{"golden-seed", "tavily", "tavily:/search"},
		{"golden-seed", "perplexity", "perplexity:/chat/completions", "2"},
	} {
		assert.Equal(t, ids.UUIDv5(parts...), UUIDv5(parts...))
	}
}

func TestFloatInIsByteIdenticalToInternalIDs(t *testing.T) {
	t.Parallel()

	for _, parts := range [][]string{
		{"golden-seed", "tavily", "score", "source-a"},
		{"golden-seed", "exa", "score", "source-b"},
	} {
		assert.InDelta(t, ids.Float(0.5, 1.0, parts...), FloatIn(0.5, 1.0, parts...), 0)
	}
	// A degenerate range returns lo, on both sides of the delegation.
	assert.Equal(t, ids.Float(1, 1, "x"), FloatIn(1, 1, "x"))
}

// --- Credentials / ObserveCredential -------------------------------------

// TestCredentialsReturnsEveryPresentedPlacement mirrors
// httpx.ExtractCredentials' own contract: every recognised placement, in
// header order, with a bare scheme token (nothing after it) reported absent
// rather than empty.
func TestCredentialsReturnsEveryPresentedPlacement(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/search", nil)
	r.Header.Set("Authorization", "Bearer sk-test-123")
	r.Header.Set("x-api-key", "second-key")

	x := &Exchange{Request: r}
	creds := x.Credentials()

	require.Len(t, creds, 2)
	assert.Equal(t, "authorization", creds[0].Header)
	assert.Equal(t, "Bearer", creds[0].Scheme)
	assert.Equal(t, "sk-test-123", creds[0].Value)
	assert.Equal(t, "x-api-key", creds[1].Header)
	assert.Equal(t, "second-key", creds[1].Value)
}

// TestObserveCredentialReachesTheJournalFingerprintedNotRaw drives a full
// request through Handle with a handler that discovers a credential the
// generic header scan cannot see (a body-placed key, Tavily's shape) and
// calls ObserveCredential — the only write path into the auth observation —
// then asserts the raw value never reaches the journal, only a fingerprint,
// and that no Findings() message interpolates it either.
func TestObserveCredentialReachesTheJournalFingerprintedNotRaw(t *testing.T) {
	t.Parallel()

	const secret = "tvly-super-secret-body-key"

	h := Handler(func(x *Exchange) Response {
		x.ObserveCredential(Credential{Header: "body:api_key", Value: secret})
		x.Warn("test.observed", "api_key", "api_key was sent in the request body")
		return Response{Status: http.StatusOK, Body: []byte(`{"ok":true}`), Label: "test.ok", FaultEligible: true}
	})

	j := journal.NewRing(8, 4096)
	d := Deps{Journal: j}
	body := `{"query":"climate","api_key":"` + secret + `"}`
	w := serve(d, h, postJSON(body))
	require.Equal(t, http.StatusOK, w.Code)

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	e := entries[0]

	require.True(t, e.Auth.Present)
	require.Equal(t, "body:api_key", e.Auth.Header)
	require.NotEmpty(t, e.Auth.Fingerprint)
	assert.NotEqual(t, secret, e.Auth.Fingerprint)

	require.Len(t, e.Findings, 1)
	assert.NotContains(t, e.Findings[0].Message, secret)

	// The raw secret must not reach the journal by ANY path: not the
	// observation, not the findings, not the redacted request body copy.
	blob, err := json.Marshal(e)
	require.NoError(t, err)
	assert.NotContains(t, string(blob), secret,
		"the raw credential must never reach a retained structure by any path (house rule 4)")
}

// TestObserveCredentialRedactsHeaderAndScheme is the adversarial case a
// generic header scan can never produce but an exported write path can: a
// profile author who copies the raw token into Header or Scheme instead of
// Value, either by mistake or because a vendor places it somewhere
// splitScheme's own canonical set does not cover. journal.Redact does not
// touch Placements ("Placements hold fingerprints, never values, so there is
// nothing here to mask") — so unlike Value, which is fingerprinted before
// ObserveCredential ever runs, Header and Scheme must already be safe by the
// time they are retained, at the ObserveCredential/placementOf boundary
// itself (internal/httpx/auth.go).
func TestObserveCredentialRedactsHeaderAndScheme(t *testing.T) {
	t.Parallel()

	const secret = "sk-zz-adversarial-raw-secret-0123456789abcdef"

	h := Handler(func(x *Exchange) Response {
		x.ObserveCredential(Credential{Header: "hdr-" + secret, Scheme: "Bearer " + secret, Value: secret})
		x.ObserveCredential(Credential{Header: "authorization", Scheme: secret, Value: "x"})
		return Response{Status: http.StatusOK, Body: []byte(`{"ok":true}`), Label: "test.ok", FaultEligible: true}
	})

	j := journal.NewRing(8, 4096)
	d := Deps{Journal: j}
	w := serve(d, h, postJSON(`{}`))
	require.Equal(t, http.StatusOK, w.Code)

	entries := j.Snapshot()
	require.Len(t, entries, 1)

	blob, err := json.Marshal(entries[0])
	require.NoError(t, err)
	assert.NotContains(t, string(blob), secret,
		"a raw credential placed in Header or Scheme must not reach a retained structure by any path (house rule 4)")
}

// TestFindingsRedactsMessage is the served-path half of Finding.Message's doc
// comment ("passed through redaction before it is stored, logged or
// served"): a profile renders Findings() straight into a 4xx body (exa's
// classify, tavily's errorFindings, perplexity's validationErrorBody), before
// Handle's own record() ever runs journal.Redact over the entry — so
// Findings() itself must not return raw credential-shaped text.
func TestFindingsRedactsMessage(t *testing.T) {
	t.Parallel()

	const secret = "sk-zz-adversarial-raw-secret-0123456789abcdef"

	x := &Exchange{Request: httptest.NewRequest(http.MethodPost, "/search", nil)}
	x.Warn("test.warn", "authorization", "saw Bearer %s on the request", secret)

	findings := x.Findings()
	require.Len(t, findings, 1)
	assert.NotContains(t, findings[0].Message, secret)
}

// --- HasJSONContentType ---------------------------------------------------

func TestHasJSONContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ct   string
		want bool
	}{
		{"application/json", "application/json", true},
		{"application/json with charset parameter", "application/json; charset=utf-8", true},
		{"a +json structured suffix", "application/vnd.api+json", true},
		{"missing header", "", false},
		{"text/json is not JSON", "text/json", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader("{}"))
			if tc.ct != "" {
				r.Header.Set("Content-Type", tc.ct)
			}
			x := &Exchange{Request: r}
			assert.Equal(t, tc.want, x.HasJSONContentType())
		})
	}
}

// TestHasJSONContentType_NilRequest is the hand-built-Exchange case Lane's own
// doc comment contemplates ("the zero Lane on an Exchange built by hand
// rather than by Handle"): a nil Request must report false, not panic.
func TestHasJSONContentType_NilRequest(t *testing.T) {
	t.Parallel()

	x := &Exchange{}
	assert.False(t, x.HasJSONContentType())
}

// --- ContentTypeJSON -------------------------------------------------------

func TestContentTypeJSONMatchesWire(t *testing.T) {
	t.Parallel()
	assert.Equal(t, wire.ContentTypeJSON, ContentTypeJSON)
}
