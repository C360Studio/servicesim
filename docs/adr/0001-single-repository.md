# ADR 0001: One repository, one binary, one listener per provider

## Status

Accepted — 2026-08-14. Still in effect through Phase 6 (2026-08-16).
[D9](../proposals/d9-framework-framing.md) does not disturb the one-repository, one-binary,
one-listener-per-provider decision this ADR made: its Tier 1 (framing) and Tier 2 (exporting the provider seam)
leave the decision untouched either way they resolve, and its Tier 3 raises a framework/profiles module split
only as an open question, not a proposal to relitigate it. **Amended 2026-08-17**: that claim is now false — D9
tier 2 was decided and shipped as [ADR 0003](0003-framework-seam.md) (the framework seam), which does touch this
decision, though not the one-repository conclusion itself. See the closing section below; the Decision and
Consequences text is left as accepted, with two of the Context's inferences withdrawn there.

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

## Amended 2026-08-17

The Decision and Consequences sections above are the accepted text and are left as written — this section
supersedes only the Status paragraph's now-false claim and two of the Context's inferences, and records why.

**The listener reasoning (`:35-43`) stands, and is strengthened.** Exa and Tavily both serving `POST /search` is
still why a shared listener cannot preserve every vendor's real path. [ADR 0003](0003-framework-seam.md) makes
this argument stronger, not weaker: an open profile set — a fifth, tenth, twentieth profile written in another
repository — makes a path collision *more* likely than a closed set of three or four vendors ever did, and
`provider.Profile.Port` turns each profile's port allocation into a registration input a `provider.Set` validates
(refusing a duplicate), rather than a hand-maintained constant in `internal/config`. One listener per profile is
what makes an open profile set safe to compose at all.

**The consumer-pays argument (`:32-33`) stands, and is why there is no module split.** ADR 0003's "Compatibility
stance" is the current answer to the module-split question; this ADR's own negative consequence (`:83` — a change
to one provider's handler is a release every consumer sees) is exactly why an incremental post-release reshaping
of the exported surface would have been a coordinated break for every consumer, repeatedly, rather than once.

**Two inferences are withdrawn:**

- The inference from "one scenario schema, one process" to "in one repository" (`:24-27`) does not hold: one
  process can load profiles registered from several Go modules, and `scenario` was already an open registry that
  needed no change to add MCP as a fourth profile (`CONTRIBUTING.md`, "Adding a reference profile here"; Phase 8,
  `39d5809`).
- The version-skew paragraph (`:28-31`) becomes an accepted cost with a stated mitigation, not an argument against
  an open profile set: the framework module carries the chassis *and* the four reference examples together, so
  skew is one edge — framework release versus a third-party profile's own release cadence — not the N² skew among
  N separately-versioned simulator repositories the original alternative (`exasim`, `tavilysim`, `perplexitysim`)
  considered.

**Mechanically stale, corrected:** the listener table (`:52-57`) predates the MCP profile (Phase 8) and the
framework seam (Phase 10). The reference set as shipped:

| Listener | Port | Why it is separate |
|---|---:|---|
| admin | 8080 | Health, readiness and the journal must not collide with a profile path, and must not be reachable through a profile base URL. |
| exa | 8081 | `POST /search`, `POST /answer` at the vendor's real paths. |
| tavily | 8082 | `POST /search` — the collision with Exa is the whole reason for the split. |
| perplexity | 8083 | `POST /v1/sonar`, `/chat/completions`, `/v1/agent`, `/v1/responses`. |
| mcp | 8084 | `POST /mcp` at the specification's own path — the fourth reference profile is a protocol, not a vendor (Phase 8, `39d5809`). |

These four are **the reference set this repository ships**, registered by `profiles.Reference()`
(`cmd/servicesim/main.go`) — not a closed list of every provider Servicesim can serve. A consumer composing their
own binary lists their own profiles, in place of or alongside any of these (ADR 0003, "The root `servicesim`
composition package").

**Tier 3's module question is recorded verbatim as open**, not resolved by this amendment
(`docs/proposals/framework-seam.md`, decision D-9): "Module split now, or one module with ADR 0001 amended?
*Recommend one module, amend* — the split's benefit is release noise; its cost is the version-skew mode ADR 0001
named and a second artefact every consumer pins."
