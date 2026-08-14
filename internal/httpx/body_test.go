package httpx_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/httpx"
)

// newRequest builds a POST with body as the entity, the way the server hands one
// to a handler.
func newRequest(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(body))
}

func TestReadBody_Bound(t *testing.T) {
	t.Parallel()

	const limit = 32

	tests := []struct {
		name    string
		body    string
		limit   int64
		want    string
		wantErr error
	}{
		{name: "empty body", body: "", limit: limit, want: ""},
		{name: "one byte under the limit", body: strings.Repeat("a", limit-1), limit: limit, want: strings.Repeat("a", limit-1)},
		{name: "exactly at the limit", body: strings.Repeat("a", limit), limit: limit, want: strings.Repeat("a", limit)},
		{name: "one byte over the limit", body: strings.Repeat("a", limit+1), limit: limit, wantErr: httpx.ErrBodyTooLarge},
		{name: "far over the limit", body: strings.Repeat("a", limit*100), limit: limit, wantErr: httpx.ErrBodyTooLarge},
		{name: "zero limit accepts an empty body", body: "", limit: 0, want: ""},
		{name: "zero limit rejects one byte", body: "a", limit: 0, wantErr: httpx.ErrBodyTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := httpx.ReadBody(newRequest(tt.body), tt.limit)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				// An oversized body must not come back as a truncated prefix: a caller
				// handed one would parse it as if it were the request.
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

// countingReader records how many bytes the consumer actually pulled, which is
// how "an oversized body is never fully buffered" is proved rather than asserted.
type countingReader struct {
	src  io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.read += n
	return n, err
}

func (c *countingReader) Close() error { return nil }

func TestReadBody_OversizedBodyIsNotFullyBuffered(t *testing.T) {
	t.Parallel()

	const limit = 1024
	counter := &countingReader{src: strings.NewReader(strings.Repeat("a", limit*64))}

	r := newRequest("")
	r.Body = counter
	r.ContentLength = -1

	_, err := httpx.ReadBody(r, limit)

	require.ErrorIs(t, err, httpx.ErrBodyTooLarge)
	// limit+1 is the smallest read that can distinguish "at the limit" from "over
	// it"; anything more means the limit is being enforced after buffering.
	assert.LessOrEqual(t, counter.read, limit+1, "read %d bytes of a %d-byte body for a %d-byte limit", counter.read, limit*64, limit)
}

func TestReadBody_IgnoresContentLengthClaim(t *testing.T) {
	t.Parallel()

	const limit = 16

	t.Run("small claim does not let an oversized body through", func(t *testing.T) {
		t.Parallel()

		r := newRequest(strings.Repeat("a", limit*10))
		r.ContentLength = 4
		r.Header.Set("Content-Length", "4")

		_, err := httpx.ReadBody(r, limit)

		require.ErrorIs(t, err, httpx.ErrBodyTooLarge)
	})

	t.Run("huge claim does not reject a body within the limit", func(t *testing.T) {
		t.Parallel()

		r := newRequest(`{"query":"a"}`)
		r.ContentLength = 1 << 40
		r.Header.Set("Content-Length", "1099511627776")

		got, err := httpx.ReadBody(r, limit)

		require.NoError(t, err)
		assert.Equal(t, `{"query":"a"}`, string(got))
	})
}

func TestReadBody_NilBody(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/search", nil)
	r.Body = nil

	got, err := httpx.ReadBody(r, 32)

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestReadBody_OverTheWire(t *testing.T) {
	t.Parallel()

	const limit = 64

	type result struct {
		body string
		err  string
	}
	// The result travels by channel rather than a shared variable: the handler and
	// the test are different goroutines, and a socket write is not a
	// happens-before edge the race detector can see.
	results := make(chan result, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		raw, err := httpx.ReadBody(r, limit)
		res := result{body: string(raw)}
		if err != nil {
			res.err = err.Error()
		}
		results <- res
	}))
	t.Cleanup(srv.Close)

	// A chunked body: the server sees no Content-Length at all, so only the byte
	// count can enforce the limit.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader(strings.Repeat("a", limit+1)))
	require.NoError(t, err)
	req.ContentLength = -1

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	got := <-results
	assert.Equal(t, httpx.ErrBodyTooLarge.Error(), got.err)
	assert.Empty(t, got.body)
}

func TestDecodeObject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    map[string]any
		wantErr error
		// malformed marks a body that is not valid JSON at all, which must not be
		// reported as ErrNotObject: the two produce different findings.
		malformed bool
	}{
		{
			name: "object with unknown properties preserved",
			raw:  `{"query":"go","futureField":{"nested":[1,"two",null]},"n":3}`,
			want: map[string]any{
				"query":       "go",
				"futureField": map[string]any{"nested": []any{float64(1), "two", nil}},
				"n":           float64(3),
			},
		},
		{name: "empty object", raw: `{}`, want: map[string]any{}},
		{name: "array", raw: `[{"query":"go"}]`, wantErr: httpx.ErrNotObject},
		{name: "string", raw: `"query"`, wantErr: httpx.ErrNotObject},
		{name: "number", raw: `7`, wantErr: httpx.ErrNotObject},
		{name: "null", raw: `null`, wantErr: httpx.ErrNotObject},
		{name: "truncated object", raw: `{"query":`, malformed: true},
		{name: "trailing garbage", raw: `{"query":"go"} oops`, malformed: true},
		{name: "empty body", raw: ``, malformed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := httpx.DecodeObject([]byte(tt.raw))

			switch {
			case tt.malformed:
				require.Error(t, err)
				assert.NotErrorIs(t, err, httpx.ErrNotObject)
				assert.NotErrorIs(t, err, httpx.ErrBodyTooLarge)
				assert.Nil(t, got)
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, got)
			default:
				require.NoError(t, err)
				assert.Empty(t, cmp.Diff(tt.want, got))
			}
		})
	}
}

func TestDecodeObject_NumbersAreFloat64(t *testing.T) {
	t.Parallel()

	got, err := httpx.DecodeObject([]byte(`{"numResults":10}`))

	require.NoError(t, err)
	// provider.Exchange.Number returns float64; a json.Number here would make every
	// range check do its own conversion.
	assert.IsType(t, float64(0), got["numResults"])
}

func TestDecodeObject_MalformedErrorDoesNotQuoteTheBody(t *testing.T) {
	t.Parallel()

	// The decode error reaches journal.Entry.BodyParseError and the log, so it must
	// not carry the body it failed on — that body may be a credential.
	_, err := httpx.DecodeObject([]byte(`{"api_key":"sk-live-super-secret" `))

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sk-live-super-secret")
}

func TestReadThenDecode(t *testing.T) {
	t.Parallel()

	raw, err := httpx.ReadBody(newRequest(`{"query":"go"}`), 1024)
	require.NoError(t, err)

	body, err := httpx.DecodeObject(raw)
	require.NoError(t, err)
	assert.Equal(t, "go", body["query"])

	// The pair round-trips: what was decoded re-encodes to the same document.
	reencoded, err := json.Marshal(body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"query":"go"}`, string(reencoded))
}

func TestIsJSONContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ct   string
		want bool
	}{
		{name: "plain", ct: "application/json", want: true},
		{name: "with charset parameter", ct: "application/json; charset=utf-8", want: true},
		{name: "with charset and no space", ct: "application/json;charset=UTF-8", want: true},
		{name: "upper case", ct: "APPLICATION/JSON", want: true},
		{name: "surrounding whitespace", ct: "  application/json  ", want: true},
		{name: "structured suffix", ct: "application/vnd.api+json", want: true},
		{name: "linked data suffix", ct: "application/ld+json; profile=\"x\"", want: true},
		{name: "non-application structured suffix", ct: "text/vnd.thing+json", want: true},
		{name: "missing", ct: "", want: false},
		{name: "form encoded", ct: "application/x-www-form-urlencoded", want: false},
		{name: "text plain", ct: "text/plain", want: false},
		{name: "text json is not application json", ct: "text/json", want: false},
		{name: "json without a type", ct: "json", want: false},
		{name: "bare suffix is not a media type", ct: "+json", want: false},
		{name: "empty subtype", ct: "application/", want: false},
		{name: "empty type", ct: "/json", want: false},
		{name: "suffix with empty base subtype", ct: "application/+json", want: false},
		{name: "prefix match only", ct: "application/jsonish", want: false},
		{name: "parameter only", ct: "; charset=utf-8", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, httpx.IsJSONContentType(tt.ct))
		})
	}
}
