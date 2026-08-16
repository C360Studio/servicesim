# ADR 0001: One repository, one binary, one listener per provider

## Status

Accepted — 2026-08-14. Still in effect through Phase 6 (2026-08-16).
[D9](../proposals/d9-framework-framing.md) does not disturb the one-repository, one-binary,
one-listener-per-provider decision this ADR made: its Tier 1 (framing) and Tier 2 (exporting the provider seam)
leave the decision untouched either way they resolve, and its Tier 3 raises a framework/profiles module split
only as an open question, not a proposal to relitigate it.

## Context

C360 consumers call three research APIs — Exa, Tavily and Perplexity — and need to test their adapters against all
three deterministically, offline, and without spending money. Two structural questions had to be answered before
any code was written.

**How many repositories and artefacts?** The obvious alternative to one repository is one per simulated provider:
`exasim`, `tavilysim`, `perplexitysim`. It looks tidy, and it is wrong for this workload. The value of the
simulator is concentrated in exactly the places that are *not* provider-specific:

- The fusion tests that matter most are cross-provider. They assert that the same document returned by two vendors
  is deduplicated, and that a claim asserted by two sources is counted as corroborated. A scenario expressing that
  must project **one canonical corpus into every wire format**, which means one scenario schema, loaded by one
  process, in one repository.
- The chassis is shared: scenario loading, the request journal, credential redaction, fault selection and
  execution, the admin surface, `testkit`. Split across repositories this becomes a fourth shared library that
  every simulator must be released against, and version skew between the simulators becomes a real failure mode in
  consumers' CI.
- Consumers pay per artefact. Three repositories means three Go modules to pin, three images to pull, three
  release cadences to track, and three Compose services to wire up for what is one logical dependency.

**How many listeners?** The driver here is not preference, it is the vendors' paths. **Exa and Tavily both serve
`POST /search`.** A single listener therefore cannot preserve both vendors' real paths. The alternatives were:

- Prefix the paths (`/exa/search`, `/tavily/search`). This changes the URL the adapter under test constructs, so
  the test no longer exercises the adapter's real request-building code — which is a substantial part of what is
  being tested.
- Route on the `Host` header. This works, but it makes the consumer's configuration a hostname trick rather than a
  base URL, and it fails the moment a client normalises or omits the header.
- Give each provider its own port.

## Decision

**One repository, one Go module, one binary, one container image.** The binary composes handlers that are also
exported for in-process use, so `testkit` and the container serve the identical code paths.

**One listener per provider, on its own port**, plus a separate admin listener:

| Listener | Port | Why it is separate |
|---|---:|---|
| admin | 8080 | Health, readiness and the journal must not collide with a vendor path, and must not be reachable through a provider base URL. |
| exa | 8081 | `POST /search`, `POST /answer` at the vendor's real paths. |
| tavily | 8082 | `POST /search` — the collision with Exa is the whole reason for the split. |
| perplexity | 8083 | `POST /v1/sonar`, `/chat/completions`, `/v1/agent`, `/v1/responses`. |

Consumers select a provider by **base URL** (`EXA_BASE_URL`, `TAVILY_BASE_URL`, `PERPLEXITY_BASE_URL`), which is
the same mechanism they already use to point at a staging environment. Nothing about the adapter's request
construction changes between the simulator and the real API.

## Consequences

### Positive

- Cross-provider scenarios are expressible at all: one corpus, one file, one process, three wire formats.
- The adapter under test builds the same URL path it would build against the real vendor, so path construction,
  content negotiation and auth placement are genuinely exercised.
- One version to pin, one image to pull, one release to cut. A provider handler fix and the contract change that
  motivated it ship together, atomically.
- The shared chassis has exactly one implementation, so redaction and determinism guarantees cannot drift between
  providers.

### Negative

- Four ports to expose instead of one. Compose files and CI service definitions are correspondingly wordier, and
  a consumer running several simulators in parallel must manage port ranges (or bind port 0 in-process, which
  `testkit` does by default).
- Every consumer's build graph gains all three provider packages even if it uses one. This is bounded by the
  dependency budget — the entire non-test dependency set is `gopkg.in/yaml.v3` — and was judged cheaper than the
  version-skew cost of splitting.
- A change to one provider's handler causes a release that every consumer of the other two also sees.

### Neutral

- Adding a fourth provider adds a listener and a port default, not a repository. The open provider registry in the
  scenario schema was designed so that this does not change the schema either.
- Running a subset is supported (`--providers exa,tavily`), so the port cost is opt-out for consumers that need
  only one surface.
