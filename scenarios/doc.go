// Package scenarios embeds the built-in protocol scenarios that ship inside the
// Servicesim binary and image, and loads them by name.
//
// They cover protocol behaviour, which is identical for every consumer: a
// well-formed success, an empty result set, each vendor's 401, 429 and 500
// envelopes, a body that is not JSON, additive unknown fields, a body padded
// past a size-limit ingress gate, a hang past a client's own deadline, a
// rising-latency ladder followed by an outage then recovery, three mid-flight
// abort shapes — two preceded by a hang before any header arrives and one
// preceded by headers arriving normally followed by a hang — a credential
// that must be rotated to a specific value before any call succeeds,
// deliberate cross-provider source overlap, a scripted multi-turn
// conversation, an async run that reaches a terminal failed status, an async
// run that never reaches a terminal status on its own (an Exa run the consumer
// cancels reports cancelled), two concurrent callers on one
// route kept in separate turn lanes, Server-Sent Events on the
// streaming-capable surfaces (a clean stream, a mid-stream disconnect, a
// truncated frame, and a transient blip a same-lane retry recovers from), a
// generic hostile-content pack (prompt injection, credential-shaped bait,
// active markup, exfiltration instructions and long content) exercising a
// consumer's guardrail on every dispatch path (an Exa run the consumer cancels
// renders no output, and a Perplexity background run a fixed answer with no
// sources, so neither carries a marker), and a provider this build has
// no handler for.
//
// Every built-in also scripts, identically, the background run a Perplexity
// Agent request with background: true starts: GET /v1/agent/{id} answers queued,
// then in progress, then completed for good, with a fixed answer and the usage and
// cost the run billed. That includes async-failed and async-stuck, whose failed and
// never-terminal runs are Exa's and Tavily's. The block carries no fault, so a
// scenario that faults the Agent create (rate-limited, server-error,
// malformed-json, brownout, timeout, oversized-body, hang-then-abort) faults a
// background create the same way, because it shares the create's budget, while the
// retrieve has a budget of its own and is never faulted. A create that fails mints
// no job, so there is nothing to retrieve; a create whose body is delayed or padded
// still does.
//
// A product-specific corpus — including a specific adopter's own
// guardrail-classifier vectors — belongs in the consuming repository and is
// mounted, not embedded here.
//
// Every built-in covers every implemented provider, so one --scenario flag
// configures all listeners coherently rather than leaving one of them serving
// something unrelated.
//
// Selection is by name, with the "builtin:" prefix on the command line
// (--scenario builtin:happy) and without it through [Load].
package scenarios
