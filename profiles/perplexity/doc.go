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
// [completionResponse]: an OpenAI-shaped envelope carrying choices[], a usage
// object whose token counts are prompt_tokens and completion_tokens, and the
// deprecated citations array. The Agent API answers with a
// [responsesResponse]: an ordered output[] execution trace whose usage counts
// are input_tokens and output_tokens. No Go type is shared between them, and
// none should be: the field names differ, and a shared type would eventually
// leak one surface's spelling onto the other.
//
// Each surface is its own scenario provider entry — [NameSonar] and
// [NameAgent] — so a scenario can rate-limit one while the other stays
// healthy, which is how a consumer's Sonar-to-Agent migration fallback gets
// tested. Their projections, [perplexityProjection] and [perplexityAgent], are
// decoded from the selected turn by this package rather than by the scenario
// package, which is what keeps scenario free of provider knowledge.
//
// Error bodies differ by surface and that asymmetry is deliberate, not an
// oversight: 422 is FastAPI's HTTPValidationError on both, every other Agent
// status is the published errorInfo envelope, and every other Sonar status is
// the simulator-chosen {"detail": "<string>"} shape recorded as unverified in
// contracts/perplexity/provenance.yaml.
//
// # Exported surface
//
// Phase 10 unit 5 trimmed this package's exported surface from 83 top-level
// declarations (go doc -short, before the move to profiles/perplexity; 85
// lines, two of which are AgentStatus's and Surface's nested const, not
// top-level) to 10: what a consumer of this profile legitimately names —
// Name, the two entry-kind names (NameSonar, NameAgent), Profile(), Routes(),
// the route patterns and fault keys (PatternSonar and its five siblings,
// FaultKeyCompletions, FaultKeyAgent), and the finding codes a test asserts
// on directly (CodeInputMissing, CodeContentType, CodeModelMissing and
// CodeProjectionInvalid and their blocks — "declared here as well as raised
// here because a consumer asserting on them ... needs a name to write", per
// this package's own finding-code doc comments). Everything else — the
// model/role/status/source-type enums and their vars, the wire-value
// constants (object/event/output-type discriminators),
// routeSonar/routeAgent/sonarRoutes/agentRoutes' per-route and per-surface
// helpers, errorBody, validators, sonarValidator, agentValidator, surface,
// and every wire/projection type this package decodes or renders
// (perplexityResult, perplexityUsage, perplexityProjection, perplexityAgent,
// completionResponse, responsesResponse and the rest) — is the package's own
// business: nothing outside profiles/perplexity names them. Two of those
// types, perplexityResult and perplexityUsage, are named by their Go type
// (not only their YAML key) in docs/scenario-schema.md's field tables — the
// same construction that document uses for other packages' unexported
// projection types (ExaResult, TavilyResult, AgentResult, ToolProjection,
// ContentBlock among them), so the doc mention does not by itself keep a
// type exported.
package perplexity
