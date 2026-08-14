// Package ids derives every generated identifier from stable fixture keys, so a
// scenario replayed tomorrow produces the identifiers it produced today.
//
// Nothing here reads a clock, a random source or a process-global counter: an
// identifier is a pure function of the parts it is derived from. Callers pass a
// tuple built from the scenario seed key, the provider name and the route's
// fault key, plus the attempt index when — and only when — the route declares a
// fault plan (§3.1 of the package design).
//
// The derived shapes deliberately match what the vendors publish rather than
// being readable slugs: 32 lowercase hex characters for Exa's requestId, an RFC
// 4122 version 5 UUID for Tavily's request_id and a Perplexity completion id,
// and "resp_"/"msg_"-prefixed hex for the Perplexity Agent surface. A consumer
// regex or length assumption trained on a slug breaks against the real API.
package ids
