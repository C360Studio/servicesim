package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ContentTypeJSON is the media type every rendered body is served with. It is
// the bare media type, without a charset parameter, because that is what the
// three simulated vendors return and a consumer matching on the exact header
// value must not have to tolerate a Servicesim-only parameter.
const ContentTypeJSON = "application/json"

// ErrNotObject reports a body whose top level is not a JSON object. Merging and
// omission are defined on object properties, so an array, a scalar or a null
// body has nothing to merge into and is a programming error in the calling
// provider rather than a scenario error.
var ErrNotObject = errors.New("wire: body is not a JSON object")

// Render marshals v and merges extra into the resulting top-level object.
//
// With no extra fields the struct's own field order is preserved. With extra
// fields the body round-trips through a map and comes back with alphabetically
// ordered keys; both forms are byte-stable run to run, and JSON object order is
// not part of any wire contract simulated here.
func Render(v any, extra map[string]any) ([]byte, error) {
	base, err := marshal(v)
	if err != nil {
		return nil, fmt.Errorf("wire: render: %w", err)
	}
	return MergeJSON(base, extra)
}

// MergeJSON merges extra into the top-level object of base. Keys in extra win.
// The merged output has alphabetically ordered keys because it round-trips
// through a map, which is why golden comparison is semantic rather than
// byte-for-byte; encoding/json's map key ordering is itself deterministic and
// recursive, so the bytes are stable run to run.
//
// The merge is shallow by design: an extra field whose value is an object
// replaces the base object at that key rather than being merged into it, so the
// scenario author can always predict the result from the scenario file alone.
//
// An empty extra returns a copy of base untouched — the body is not decoded, so
// struct field order survives — and base is validated only on the merging path.
// It is returned as a copy so a caller cannot alias, and later mutate, bytes it
// believes this package produced.
func MergeJSON(base []byte, extra map[string]any) ([]byte, error) {
	if len(extra) == 0 {
		return bytes.Clone(base), nil
	}
	obj, err := decodeObject(base)
	if err != nil {
		return nil, err
	}
	// Iterating extra is safe despite Go's randomised map order: the destination
	// is a map keyed by the same strings, so every iteration order produces the
	// same final map, and encoding/json then sorts the keys.
	for k, v := range extra {
		obj[k] = v
	}
	return marshal(obj)
}

// Omit removes the named top-level properties from a rendered object, backing
// scenario.*Result.OmitFields.
//
// Naming a property that is not present is not an error: omission expresses
// "this must not reach the client", and a scenario that omits an already-absent
// field has got what it asked for. Like MergeJSON, an empty field list returns a
// copy of base without decoding it.
func Omit(base []byte, fields []string) ([]byte, error) {
	if len(fields) == 0 {
		return bytes.Clone(base), nil
	}
	obj, err := decodeObject(base)
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		delete(obj, f)
	}
	return marshal(obj)
}

// WriteJSON writes status, Content-Type and body, returning the byte count.
//
// An already-set Content-Type is left alone rather than overwritten, which is
// what makes the wrong_content_type fault expressible: the caller sets the
// mangled value on the header before writing and still routes the write through
// here. The returned count is the number of body bytes actually written, for
// the journal's bytes-out field.
func WriteJSON(w http.ResponseWriter, status int, body []byte) int {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", ContentTypeJSON)
	}
	w.WriteHeader(status)
	n, _ := w.Write(body)
	return n
}

// marshal encodes v with HTML escaping disabled and without the trailing
// newline json.Encoder appends. json.Marshal is not used directly because it
// has no escaping switch.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("wire: marshal: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// decodeObject decodes base as a JSON object, keeping numbers as json.Number so
// a re-marshal reproduces the original literal instead of a float64 rendering
// of it.
func decodeObject(base []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(base))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("wire: decode body: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("wire: decode body: %w", errTrailingData)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, ErrNotObject
	}
	return obj, nil
}

// errTrailingData reports more than one JSON value in a body. Rendered bodies
// are single values; anything else means the caller concatenated bytes it
// should have merged.
var errTrailingData = errors.New("trailing data after top-level JSON value")
