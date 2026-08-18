package provider

import (
	"github.com/c360studio/servicesim/internal/httpx"
	"github.com/c360studio/servicesim/internal/ids"
	"github.com/c360studio/servicesim/internal/wire"
)

// Render marshals v deterministically — HTML escaping off, json.Number
// fidelity, no map-order leakage — then merges extra into the top-level
// object, then drops omit. That order is fixed and is not a profile's
// choice: docs/scenario-schema.md documents it as "the merge happens first
// and the omission second, so omit_fields can remove a key extra_fields
// added", and Render is the one symbol that makes a third ordering
// unrepresentable.
//
// It exists so a profile never has to reach for encoding/json directly: a
// bare json.Marshal escapes "&" as "\u0026" and can re-render an integral
// float64 such as 1000000 as "1e+06", both of which are wire-contract
// changes dressed up as formatting details (house rule 2). It delegates to
// internal/wire, which carries the byte-fidelity guarantees in full.
func Render(v any, extra map[string]any, omit []string) ([]byte, error) {
	body, err := wire.Render(v, extra)
	if err != nil {
		return nil, err
	}
	return wire.Omit(body, omit)
}

// Hex32 returns thirty-two lowercase hex characters derived from parts, the
// shape Exa documents for requestId. It is byte-identical to
// internal/ids.Hex32 for the same parts — the goldens depend on it — and
// reads no clock, no random source and no process-global counter.
func Hex32(parts ...string) string { return ids.Hex32(parts...) }

// UUIDv5 returns an RFC 4122 version 5 UUID derived from parts under the
// Servicesim namespace, the shape Tavily documents for request_id. It is
// byte-identical to internal/ids.UUIDv5 for the same parts and reads no
// clock, no random source and no process-global counter.
func UUIDv5(parts ...string) string { return ids.UUIDv5(parts...) }

// FloatIn returns a stable value in [lo, hi) derived from parts, for
// synthesising a plausible score with no random source. It is byte-identical
// to internal/ids.Float for the same inputs and reads no clock, no random
// source and no process-global counter.
func FloatIn(lo, hi float64, parts ...string) float64 { return ids.Float(lo, hi, parts...) }

// ContentTypeJSON is the media type every rendered body is served with.
const ContentTypeJSON = "application/json"

// Credential is one credential placement observed on a request. Value is the
// credential itself: compare it against scenario.AuthPolicy.ExpectKey and
// never journal, log or interpolate it.
type Credential struct {
	// Header is the placement, lower-cased: a header name such as
	// "authorization", or a non-header placement such as "body:api_key".
	Header string

	// Scheme is the Authorization scheme, for example "Bearer". Empty for
	// non-scheme placements.
	Scheme string

	// Value is the presented credential. It is never safe to journal, log or
	// interpolate into a finding message.
	Value string
}

// Severity classifies a validation finding.
type Severity string

// Finding severities.
const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Finding is one validation warning or error recorded against a request.
type Finding struct {
	Severity Severity
	Code     string
	Field    string

	// Message is free text that may quote part of the request. It is passed
	// through redaction before it is stored, logged or served, but it must
	// never interpolate a credential value in the first place.
	Message string
}

// Credentials returns every recognised credential placement this request
// carried, in header order: authorization, then x-api-key. The returned
// Value is the credential itself — compare it against
// scenario.AuthPolicy.ExpectKey and never journal, log or interpolate it.
func (x *Exchange) Credentials() []Credential {
	in := httpx.ExtractCredentials(x.Request)
	if len(in) == 0 {
		return nil
	}
	out := make([]Credential, len(in))
	for i, c := range in {
		out[i] = Credential{Header: c.Header, Scheme: c.Scheme, Value: c.Value}
	}
	return out
}

// ObserveCredential records one more credential placement on this request's
// auth observation, promoting it to the primary placement if none was
// recorded yet. It is the ONLY write path into the observation: it
// fingerprints c.Value and never retains it, exactly as the shared lifecycle's
// own header scan does. c.Header and c.Scheme are also passed through
// redaction before retention, so a profile that mistakenly copies the raw
// token into either field — rather than Value, where it belongs — still
// cannot land it in a retained structure (house rule 4).
//
// It exists for placements no generic header scan can find — a vendor whose
// credential arrives as a property of the JSON body discovers it only after
// Handle has already observed the headers.
func (x *Exchange) ObserveCredential(c Credential) {
	x.auth = httpx.AddPlacement(x.auth, httpx.Credential{Header: c.Header, Scheme: c.Scheme, Value: c.Value})
}

// HasJSONContentType reports whether the request declared a JSON media type:
// application/json or a +json structured suffix, parameters ignored. A
// missing header and text/json are both false, and so is a nil Request — an
// Exchange built by hand rather than by Handle, which Lane's own doc comment
// contemplates.
func (x *Exchange) HasJSONContentType() bool {
	if x.Request == nil {
		return false
	}
	return httpx.IsJSONContentType(x.Request.Header.Get("Content-Type"))
}
