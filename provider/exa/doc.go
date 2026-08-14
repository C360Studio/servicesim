// Package exa simulates the Exa search and answer API: POST /search and
// POST /answer on one listener, with Exa's own request validation, its flat
// error envelope, and the reduced rate-limit body that envelope makes an
// exception for.
//
// Everything on the wire follows contracts/exa/README.md, which records what
// the vendor's live documentation actually says. Four of its corrections are
// easy to get wrong from memory and are therefore restated here:
//
//   - A result carries no score. The only score-like field is highlightScores,
//     and a scenario that sets a per-result score gets it accepted, dropped and
//     reported as exa.result.score.not_emitted at load time.
//   - costDollars is always present, even on an empty result set.
//   - requestId is thirty-two lowercase hex characters, never a readable slug,
//     and results[].id defaults to the source URL, never a slug either.
//   - A 429 body is {"error": ...} alone — no requestId and no tag. Every other
//     status uses the flat {requestId, error, tag} envelope. Rate limiting is
//     exactly the case a consumer's retry logic branches on, so a simulator
//     that always emitted the three-key shape would be wrong where it matters
//     most.
//
// /search and /answer are rendered by separate functions from separate
// projections. They are not one response with a renamed array: an /answer
// citation is a strict subset of a /search result, and /answer places
// structured output on the answer key itself rather than under output.content.
// Sharing a renderer between them is the single most likely bug on this
// surface, so the code does not offer the opportunity.
//
// The package holds no mutable state. A handler renders a response from the
// scenario, the request and the attempt counter, so the same scenario and the
// same request produce byte-identical bytes on every run.
package exa
