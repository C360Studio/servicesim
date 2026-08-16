// Package scenarios embeds the built-in protocol scenarios that ship inside the
// Servicesim binary and image, and loads them by name.
//
// They cover protocol behaviour, which is identical for every consumer: a
// well-formed success, an empty result set, each vendor's 401, 429 and 500
// envelopes, a body that is not JSON, additive unknown fields, a body padded
// past a size-limit ingress gate, a hang past a client's own deadline, a
// rising-latency ladder followed by an outage then recovery, two mid-flight
// abort shapes each preceded by a hang, a credential that must be rotated to
// a specific value before any call succeeds, deliberate cross-provider
// source overlap, a scripted multi-turn conversation, a generic
// hostile-content pack (prompt injection, credential-shaped bait, active
// markup, exfiltration instructions and long content) exercising a
// consumer's guardrail on every dispatch path, and a provider this build has
// no handler for.
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
