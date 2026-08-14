package wire_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/wire"
)

// searchResponse mirrors the shape a provider package renders: a few ordered
// struct fields, one of them a nested slice.
type searchResponse struct {
	RequestID string   `json:"requestId"`
	Results   []result `json:"results"`
	Cost      float64  `json:"costDollars"`
}

type result struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// tunedResponse carries a map-valued field, the auto_parameters case that would
// leak Go map iteration order into the body if encoding/json did not sort keys.
type tunedResponse struct {
	RequestID string         `json:"requestId"`
	Auto      map[string]any `json:"autoParameters"`
}

func sample() searchResponse {
	return searchResponse{
		RequestID: "b5947044c4b78efa9552a7c89b306d95",
		Results: []result{
			{Title: "First", URL: "https://alpha.example.com/a"},
			{Title: "Second", URL: "https://beta.example.com/b"},
		},
		Cost: 0.001,
	}
}

func TestRenderWithoutExtrasPreservesStructOrder(t *testing.T) {
	t.Parallel()

	got, err := wire.Render(sample(), nil)
	require.NoError(t, err)

	const want = `{"requestId":"b5947044c4b78efa9552a7c89b306d95",` +
		`"results":[{"title":"First","url":"https://alpha.example.com/a"},` +
		`{"title":"Second","url":"https://beta.example.com/b"}],` +
		`"costDollars":0.001}`
	assert.Equal(t, want, string(got))
}

func TestRenderMergesExtraFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		extra map[string]any
		want  map[string]any
	}{
		{
			name:  "no extras leaves the body alone",
			extra: nil,
			want: map[string]any{
				"requestId":   "b5947044c4b78efa9552a7c89b306d95",
				"costDollars": 0.001,
				"results": []any{
					map[string]any{"title": "First", "url": "https://alpha.example.com/a"},
					map[string]any{"title": "Second", "url": "https://beta.example.com/b"},
				},
			},
		},
		{
			name:  "an unknown scalar is added at the top level",
			extra: map[string]any{"experimentalRerank": true},
			want: map[string]any{
				"requestId":          "b5947044c4b78efa9552a7c89b306d95",
				"costDollars":        0.001,
				"experimentalRerank": true,
				"results": []any{
					map[string]any{"title": "First", "url": "https://alpha.example.com/a"},
					map[string]any{"title": "Second", "url": "https://beta.example.com/b"},
				},
			},
		},
		{
			name: "an unknown nested object is added whole",
			extra: map[string]any{
				"telemetry": map[string]any{"shard": "eu-1", "warm": false},
			},
			want: map[string]any{
				"requestId":   "b5947044c4b78efa9552a7c89b306d95",
				"costDollars": 0.001,
				"telemetry":   map[string]any{"shard": "eu-1", "warm": false},
				"results": []any{
					map[string]any{"title": "First", "url": "https://alpha.example.com/a"},
					map[string]any{"title": "Second", "url": "https://beta.example.com/b"},
				},
			},
		},
		{
			name:  "an extra field replaces a rendered field of the same name",
			extra: map[string]any{"costDollars": "unavailable"},
			want: map[string]any{
				"requestId":   "b5947044c4b78efa9552a7c89b306d95",
				"costDollars": "unavailable",
				"results": []any{
					map[string]any{"title": "First", "url": "https://alpha.example.com/a"},
					map[string]any{"title": "Second", "url": "https://beta.example.com/b"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := wire.Render(sample(), tt.extra)
			require.NoError(t, err)

			var decoded map[string]any
			require.NoError(t, json.Unmarshal(got, &decoded))
			assert.Empty(t, cmp.Diff(tt.want, decoded))
		})
	}
}

// TestRenderIsByteIdenticalAcrossRuns is the determinism guard: no map
// iteration order may reach the output, at the top level or nested inside a
// map-valued field.
func TestRenderIsByteIdenticalAcrossRuns(t *testing.T) {
	t.Parallel()

	extra := map[string]any{
		"zulu": 1, "yankee": 2, "xray": 3, "whiskey": 4, "victor": 5,
		"uniform": 6, "tango": 7, "sierra": 8, "romeo": 9, "quebec": 10,
		"papa": map[string]any{
			"nested-z": "z", "nested-a": "a", "nested-m": "m",
			"deeper": map[string]any{"q": 1, "b": 2, "k": 3},
		},
		"oscar": []any{"first", "second", "third"},
	}

	value := tunedResponse{
		RequestID: "b5947044c4b78efa9552a7c89b306d95",
		Auto: map[string]any{
			"type": "auto", "category": "company", "livecrawl": "fallback",
			"numResults": 10, "useAutoprompt": true,
		},
	}

	first, err := wire.Render(value, extra)
	require.NoError(t, err)

	for i := range 100 {
		got, err := wire.Render(value, extra)
		require.NoError(t, err)
		require.Equal(t, string(first), string(got), "render %d differed", i)
	}
}

func TestMergeJSONKeepsIntegerLiterals(t *testing.T) {
	t.Parallel()

	// Decoding into float64 would re-emit these as 1e+06 and 9.007199254740993e+15,
	// silently changing a usage-accounting body.
	base := []byte(`{"usage":{"total_tokens":1000000,"cost":0.0000125},"seq":9007199254740993}`)

	got, err := wire.MergeJSON(base, map[string]any{"note": "additive"})
	require.NoError(t, err)

	assert.JSONEq(t,
		`{"usage":{"total_tokens":1000000,"cost":0.0000125},"seq":9007199254740993,"note":"additive"}`,
		string(got))
	assert.Contains(t, string(got), `"total_tokens":1000000`)
	assert.Contains(t, string(got), `"seq":9007199254740993`)
	assert.Contains(t, string(got), `"cost":0.0000125`)
}

func TestMergeJSONSortsKeys(t *testing.T) {
	t.Parallel()

	got, err := wire.MergeJSON([]byte(`{"b":1,"a":2}`), map[string]any{"c": 3})
	require.NoError(t, err)
	assert.Equal(t, `{"a":2,"b":1,"c":3}`, string(got))
}

func TestMergeJSONWithoutExtrasReturnsAnUnalteredCopy(t *testing.T) {
	t.Parallel()

	base := []byte(`{"b":1,"a":2}`)

	got, err := wire.MergeJSON(base, nil)
	require.NoError(t, err)
	assert.Equal(t, `{"b":1,"a":2}`, string(got), "key order must survive when nothing is merged")

	got[1] = 'X'
	assert.Equal(t, `{"b":1,"a":2}`, string(base), "the result must not alias base")
}

func TestMergeJSONRejectsNonObjectBodies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base string
		// wantNotObject distinguishes well-formed JSON that is simply not an
		// object from bytes that are not JSON at all.
		wantNotObject bool
	}{
		{name: "array", base: `[1,2,3]`, wantNotObject: true},
		{name: "string scalar", base: `"body"`, wantNotObject: true},
		{name: "number scalar", base: `12`, wantNotObject: true},
		{name: "null", base: `null`, wantNotObject: true},
		{name: "malformed", base: `{"a":`},
		{name: "empty", base: ``},
		{name: "two values", base: `{"a":1} {"b":2}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := wire.MergeJSON([]byte(tt.base), map[string]any{"x": 1})
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Equal(t, tt.wantNotObject, errors.Is(err, wire.ErrNotObject))
		})
	}
}

func TestOmit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		base   string
		fields []string
		want   string
	}{
		{
			name:   "no fields returns the body unchanged",
			base:   `{"b":1,"a":2}`,
			fields: nil,
			want:   `{"b":1,"a":2}`,
		},
		{
			name:   "a named field is dropped",
			base:   `{"title":"t","url":"u","author":null}`,
			fields: []string{"author"},
			want:   `{"title":"t","url":"u"}`,
		},
		{
			name:   "several named fields are dropped",
			base:   `{"title":"t","url":"u","author":null,"text":"x"}`,
			fields: []string{"author", "text"},
			want:   `{"title":"t","url":"u"}`,
		},
		{
			name:   "an absent field is not an error",
			base:   `{"title":"t"}`,
			fields: []string{"summary"},
			want:   `{"title":"t"}`,
		},
		{
			name:   "omission is top level only",
			base:   `{"outer":{"title":"t"},"title":"top"}`,
			fields: []string{"title"},
			want:   `{"outer":{"title":"t"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := wire.Omit([]byte(tt.base), tt.fields)
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestOmitRejectsNonObjectBodies(t *testing.T) {
	t.Parallel()

	got, err := wire.Omit([]byte(`[1,2]`), []string{"a"})
	require.ErrorIs(t, err, wire.ErrNotObject)
	assert.Nil(t, got)
}

func TestOmitDoesNotAliasBase(t *testing.T) {
	t.Parallel()

	base := []byte(`{"a":1}`)
	got, err := wire.Omit(base, nil)
	require.NoError(t, err)

	got[1] = 'X'
	assert.Equal(t, `{"a":1}`, string(base))
}

// TestRenderDoesNotEscapeHTML pins the escaping decision: a query URL is the
// commonest string in these bodies and it must survive as written.
func TestRenderDoesNotEscapeHTML(t *testing.T) {
	t.Parallel()

	value := result{Title: "Cats & dogs <live>", URL: "https://search.example.com/?q=a&b=c"}

	plain, err := wire.Render(value, nil)
	require.NoError(t, err)
	merged, err := wire.Render(value, map[string]any{"extra": "x&y"})
	require.NoError(t, err)

	// The six characters encoding/json would emit for "&" with HTML escaping on.
	const ampersandEscape = "\\u0026"

	assert.Contains(t, string(plain), `?q=a&b=c`)
	assert.Contains(t, string(plain), `Cats & dogs <live>`)
	assert.Contains(t, string(merged), `?q=a&b=c`)
	assert.Contains(t, string(merged), `"extra":"x&y"`)
	assert.NotContains(t, string(plain), ampersandEscape)
	assert.NotContains(t, string(merged), ampersandEscape)
}

func TestRenderReportsUnmarshalableValues(t *testing.T) {
	t.Parallel()

	got, err := wire.Render(map[string]any{"ch": make(chan int)}, nil)
	require.Error(t, err)
	assert.Nil(t, got)
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		preset          string
		status          int
		body            string
		wantContentType string
	}{
		{
			name:            "sets the JSON content type",
			status:          http.StatusOK,
			body:            `{"a":1}`,
			wantContentType: "application/json",
		},
		{
			name:            "leaves a preset content type alone",
			preset:          "text/html; charset=utf-8",
			status:          http.StatusOK,
			body:            `{"a":1}`,
			wantContentType: "text/html; charset=utf-8",
		},
		{
			name:            "carries a non-200 status",
			status:          http.StatusTooManyRequests,
			body:            `{"error":"rate limited"}`,
			wantContentType: "application/json",
		},
		{
			name:            "writes an empty body without error",
			status:          http.StatusOK,
			body:            ``,
			wantContentType: "application/json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			if tt.preset != "" {
				rec.Header().Set("Content-Type", tt.preset)
			}

			n := wire.WriteJSON(rec, tt.status, []byte(tt.body))

			assert.Equal(t, len(tt.body), n)
			assert.Equal(t, tt.status, rec.Code)
			assert.Equal(t, tt.wantContentType, rec.Header().Get("Content-Type"))
			assert.Equal(t, tt.body, rec.Body.String())
		})
	}
}
