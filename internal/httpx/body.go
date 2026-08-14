package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrBodyTooLarge reports a request body over the configured limit. It is the
// single source of the request.body_too_large finding: provider.Handle calls
// [ReadBody] and maps this error, and no provider package may reimplement
// http.MaxBytesReader handling locally.
var ErrBodyTooLarge = errors.New("httpx: request body too large")

// ErrNotObject reports a body that is valid JSON but not a JSON object, backing
// the request.body_not_object finding.
//
// It is a distinct sentinel because request.body_not_object and
// request.malformed_json are distinct findings with — for Exa and Tavily —
// distinct messages, and a caller cannot tell them apart from a
// *json.SyntaxError alone: `[]` and `"x"` parse perfectly and are still not a
// request. See the design gap noted in §2.6: the design document lists only
// ErrBodyTooLarge, yet §6.2 requires both codes.
var ErrNotObject = errors.New("httpx: request body is not a JSON object")

// ReadBody reads at most limit bytes of r.Body. It returns ErrBodyTooLarge when
// the body exceeds the limit, using an http.MaxBytesReader so an oversized body
// is never fully buffered.
//
// The limit is applied to the bytes that actually arrive, never to the
// Content-Length the client claims. A claim is client-controlled and can be
// wrong in both directions: trusting a small claim lets an oversized body
// through, and trusting a large one rejects a request that was within the limit
// all along. Only the reader's own count decides.
//
// A body at exactly limit bytes is accepted; limit+1 is the first size
// rejected. On ErrBodyTooLarge no bytes are returned: the prefix read so far is
// not a request, and handing back a truncated document invites a caller to
// parse it.
func ReadBody(r *http.Request, limit int64) ([]byte, error) {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	if limit < 0 {
		limit = 0
	}

	// A nil ResponseWriter is deliberate: MaxBytesReader only uses it to tell the
	// server not to read the rest of an oversized body before replying, and
	// provider.Handle always replies immediately on ErrBodyTooLarge anyway. Passing
	// nil keeps ReadBody usable from a test that has no writer.
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, ErrBodyTooLarge
		}
		// The wrapped error is a transport error. It must never be given the body or
		// the request target to quote: a wrapped absolute-form URL carries userinfo,
		// which is exactly the leak house rule 4 forbids.
		return nil, fmt.Errorf("httpx: read request body: %w", err)
	}
	return raw, nil
}

// DecodeObject decodes raw as a generic JSON object. Unknown properties are
// preserved, not rejected: a provider request body must tolerate additive vendor
// fields, unlike a scenario file.
//
// It returns ErrNotObject for valid JSON that is not an object, and a wrapped
// decode error — neither sentinel — for malformed JSON, including an empty
// body. Numbers decode to float64 rather than json.Number so that
// provider.Exchange.Number can hand back a float64 without a second conversion;
// no vendor field validated here needs more precision than that.
func DecodeObject(raw []byte) (map[string]any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("httpx: decode request body: %w", err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, ErrNotObject
	}
	return obj, nil
}
