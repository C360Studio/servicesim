// Package perplexity simulates Perplexity's two research surfaces: the Sonar
// chat-completions API and its announced successor, the Agent API.
//
// One listener serves four routes across those two surfaces:
//
//	POST /v1/sonar          Sonar, canonical
//	POST /chat/completions  Sonar, OpenAI SDK alias
//	POST /v1/agent          Agent API, canonical
//	POST /v1/responses      Agent API, OpenAI SDK alias
//
// The aliases are aliases in the strict sense — same handler, same request and
// response shapes, same fault budget — because the OpenAI SDK appends those
// paths to its configured base URL and consumers really do arrive on them. The
// journal records which path was used, so an adapter test can still assert its
// intended route.
//
// The two surfaces share nothing on the wire. Sonar answers with a
// [CompletionResponse]: an OpenAI-shaped envelope carrying choices[], a usage
// object whose token counts are prompt_tokens and completion_tokens, and the
// deprecated citations array. The Agent API answers with a
// [ResponsesResponse]: an ordered output[] execution trace whose usage counts
// are input_tokens and output_tokens. No Go type is shared between them, and
// none should be: the field names differ, and a shared type would eventually
// leak one surface's spelling onto the other.
//
// Each surface is its own scenario provider entry — [NameSonar] and
// [NameAgent] — so a scenario can rate-limit one while the other stays
// healthy, which is how a consumer's Sonar-to-Agent migration fallback gets
// tested. Their projections, [PerplexityProjection] and [PerplexityAgent], are
// decoded from the selected turn by this package rather than by the scenario
// package, which is what keeps scenario free of provider knowledge.
//
// Error bodies differ by surface and that asymmetry is deliberate, not an
// oversight: 422 is FastAPI's HTTPValidationError on both, every other Agent
// status is the published ErrorInfo envelope, and every other Sonar status is
// the simulator-chosen {"detail": "<string>"} shape recorded as unverified in
// contracts/perplexity/provenance.yaml.
package perplexity
