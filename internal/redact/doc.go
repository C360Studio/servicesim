// Package redact removes credentials from anything Servicesim stores or emits.
//
// Redaction happens before anything is retained, not on the way out. Every
// retained structure — the request journal, the admin API's rendering of it,
// structured logs, and error messages — passes through this package first, and
// there is no configuration flag that turns it off.
//
// The package covers every path a credential can travel, not just the two
// obvious ones:
//
//	Headers, HeaderValue  request and response headers, including
//	                      vendor-prefixed variants such as X-Exa-Api-Key
//	Query                 credential-named query parameters
//	JSON, JSONBytes       request bodies, valid JSON or not
//	String                free text: finding messages, body parse errors,
//	                      form-encoded bodies, URL userinfo, bearer tokens
//	Fingerprint           a non-reversible identity for a presented credential
//
// Two properties are load-bearing. Every function is idempotent, so
// provider.Handle can redact before logging and journal.Append can redact again
// at the storage boundary without the second pass changing anything. And every
// function is deterministic: no map iteration order reaches output, query
// parameters are re-encoded in sorted order, and JSON objects are re-encoded
// with sorted keys.
package redact
