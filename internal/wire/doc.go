// Package wire renders provider response bodies.
//
// It owns the response side of the pipeline that CLAUDE.md sketches as
// "per-provider projection -> wire response": a provider package builds a typed
// struct, wire turns it into bytes, merges any additive scenario fields into it,
// drops any omitted ones, and writes it with the right status and media type.
//
// Three properties make it worth having as a package rather than four lines
// repeated in each provider:
//
//   - Determinism. The same value rendered twice must produce byte-identical
//     output. Go map iteration order never reaches the output: encoding/json
//     sorts object keys, and the only place a map is iterated here is when
//     copying extra fields into a decoded body, where the destination is itself
//     a map and unique keys make the order irrelevant.
//
//   - Numeric fidelity. Merging and omission both round-trip the body through
//     map[string]any. Decoding numbers into float64 would rewrite an integer
//     such as 1000000 as 1e+06, which is a wire-contract change dressed up as a
//     formatting detail, so decoding uses json.Number and re-emits the original
//     literal.
//
//   - Escaping consistency. Every marshal in this package disables encoding/json's
//     HTML escaping, so an ampersand inside a query URL survives as itself instead
//     of being rewritten as a unicode escape. Real vendor responses do not escape
//     it, and escaping on only one of the two paths would make a merged body's
//     bytes differ from an unmerged one's for no contractual reason.
//
// Key order is not part of any vendor contract this repository simulates, which
// is why golden comparison is semantic (package-design §3.3). Merging a body
// re-orders struct-derived keys alphabetically because it round-trips through a
// map; rendering without extras preserves struct field order. Both are stable
// run to run, which is the property that matters.
package wire
