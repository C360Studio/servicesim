// Package httpx holds the request-side checks every provider shares: the
// bounded body read, the generic JSON decode, the content-type check and the
// credential observation.
//
// It exists so those four things have exactly one implementation. In
// particular [ErrBodyTooLarge] is the single source of the
// request.body_too_large finding: provider.Handle calls [ReadBody] and maps
// this error, and no provider package may reimplement http.MaxBytesReader
// handling locally. A second implementation is how one provider ends up with a
// different limit, a different status, or an oversized body fully buffered in
// memory (§2.6, §6.2 of the package design).
//
// Nothing here decides anything provider-specific. It reports what arrived —
// how large the body was, whether it parsed, where a credential was placed —
// and the caller turns that into findings and a provider-shaped error body.
//
// The auth helpers never return a credential to anything that retains it.
// [ExtractCredentials] hands the value straight to the validator that must
// compare it against scenario.AuthPolicy.ExpectKey, and [Observe] converts a
// placement into a journal observation carrying a fingerprint rather than the
// value, so no path leads from a presented key to the journal, the admin API or
// a log record (house rule 4).
package httpx
