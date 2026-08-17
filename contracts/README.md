# Consumed contracts

Each provider directory records the subset of its vendor API that Servicesim simulates and that C360 consumers
parse — the *consumed contract*, not the whole vendor surface. Every file carries the documentation URLs it was
derived from and the date the shape was verified.

| Provider | Contract | Verified | Base URL simulated |
|---|---|---|---|
| Exa | [`exa/README.md`](exa/README.md) | 2026-08-15 | `POST /search`, `POST /answer`, `POST /contents`, `POST /findSimilar`, `POST /agent/runs`, `GET /agent/runs/{id}`, `HEAD /agent/runs/{id}` |
| Tavily | [`tavily/README.md`](tavily/README.md) | 2026-08-15 | `POST /search`, `POST /extract`, `POST /research`, `GET /research/{request_id}`, `HEAD /research/{request_id}` |
| Perplexity | [`perplexity/README.md`](perplexity/README.md) | 2026-08-15 | `POST /v1/sonar`, `POST /chat/completions`, `POST /v1/chat/completions`, `POST /v1/agent`, `POST /v1/responses`, `POST /responses` |
| MCP | [`mcp/README.md`](mcp/README.md) | 2026-08-16 | not yet simulated — the contract is recorded ahead of the handler (Phase 8 unit 2); no `METHOD /path` claim until a route registers |

Every route in that column has golden fixtures in this directory, except the two `HEAD` routes: `HEAD` carries no
body, so it has no fixture to pin — its behaviour is covered by the provider tests instead. The MCP row claims no
route yet, so no golden is owed for it until unit 2 registers one. Treat the column as the complete list of what is
simulated, not as a summary of the interesting parts.

**`scripts/check-docs.sh` now proves that column against the routes the binary actually registers, in both
directions**, so the table cannot claim a route that does not exist *or* omit one that does. Both failures had
already happened: the column once omitted Exa `POST /answer` and Perplexity `POST /v1/agent` and
`POST /v1/responses` while their goldens sat committed beside it, and a later pass still omitted
`POST /v1/chat/completions` and `POST /responses`. An adopter built a "Servicesim models one-shot
request/response only" inventory partly from this table, which is why an omission here is not cosmetic — a reader
concludes a capability does not exist.

`POST /chat/completions`, `POST /v1/chat/completions`, `POST /v1/responses` and `POST /responses` are the
SDK-routing aliases described in [`perplexity/README.md`](perplexity/README.md); each pair shares the shapes of
`/v1/sonar` and `/v1/agent` respectively.

## Vendor endpoints that are NOT simulated

Listed rather than omitted. A surface that is simply absent from this table reads as an oversight, and a reader
cannot tell whether it was considered and declined or never looked at — which is the same failure as a wrong claim.

| Provider | Endpoint | Status | Why |
|---|---|---|---|
| Exa | `/agent/runs` lifecycle beyond create and poll | NOT SIMULATED | Create and poll ARE simulated — see the table above. The rest of the lifecycle (listing all runs, `/agent/runs/{id}/events`, `/agent/runs/{id}/cancel`, and deleting a run) has no verified contract and is not simulated. Paths here are written without a method on purpose: the index guard reads a backticked method-plus-path as a claim that the route IS simulated. |

A `NOT SIMULATED` row never asserts a method, a request shape or a response shape for the paths it names — that
would be writing a wire field from memory, which [the one rule](../CONTRIBUTING.md#the-one-rule-that-matters-most)
forbids regardless of a row's status. Verification comes first; simulation is a separate decision after it.

## Why these files exist

Golden wire fixtures are only trustworthy if a reviewer can tell what they were checked against. When a fixture
changes, the reviewer's question is "did the vendor change, or did we?" — and that question is unanswerable without
a dated provenance record. These files are that record.

They are deliberately *not* copies of the vendors' OpenAPI documents. Whether those can be redistributed under their
respective terms is an open question in the plan's deferred decisions, so this repository keeps only reviewed
minimal schemas for the fields it actually implements.

## Keeping them honest

Simulator tests cannot detect vendor drift — a simulator agrees with itself by construction. **There is no live
contract canary, and none is planned (D10, `docs/adopter-backlog.md`).** A canary is outbound infrastructure and a
scheduled dependency on vendor availability, for a test simulator whose entire value is determinism — the wrong
kind of moving part for a repository that dials outward on its own schedule for nothing else. Drift detection
instead is a **dated, manual re-verification**, and its first, cheap step is the same for every provider: Exa,
Tavily and Perplexity each publish a machine-readable OpenAPI document — `exa-spec.yaml`, `openapi.json` and
`openapi.json` respectively — covering every route this repository simulates for that vendor, and the Model Context
Protocol publishes a machine-readable `schema.json` per revision (a JSON Schema of every message shape, not an
OpenAPI document — MCP is JSON-RPC over one POST endpoint, so there are no paths to describe); each provider's
`contracts/<provider>/provenance.yaml` records that document's URL, version and `sha256` in a `spec:` block.
Comparing a fresh fetch's hash against the recorded one is mechanical and answers "did the vendor's machine-readable
surface move at all?" in seconds. It does **not** by itself answer "did anything we consume change?": a provider's
consumed fields are still verified mostly against the vendor's rendered prose pages (each entry's own
`documentation_url`), and the spec's bytes can move for reasons a given consumed contract never touches — a new
Exa Websets endpoint, an unrelated Tavily `/crawl` field, and so on. A changed hash is therefore the SIGNAL that
something may have moved, not a diff of what moved: the next step is always a person re-reading the consumed fields
against both the cited `documentation_url` pages and the spec itself. Only the entries whose own `documentation_url`
IS the spec's URL (all of Perplexity's, and Exa's three `/findSimilar` entries) were read from the spec directly;
every other entry was read from an undated prose page, and re-checking it means re-reading that page, not re-hashing
anything.

Every provider's `provenance.yaml` carries two kinds of `verified:` date — the provider-level one at the top of the
file, matching the **Verified** column above, and one per golden entry. Both move on a refresh, but not for the
same reason: an entry's date moves because that golden's shape was re-checked; the provider-level date and the
**Verified** column above move together whenever any entry is checked later than they currently claim, because a
whole-contract verification cannot be older than a fixture that was individually re-checked since. See the header of
any `provenance.yaml` for how the two relate. Every provider's `provenance.yaml` also carries a `spec:` block —
`url`, `version`, `sha256`, `retrieved` — recording the bytes its consumed contract's machine-readable source was
generated from, readable from Go via `contracts.ProviderSpec(p)`; `contracts/contracts_test.go`'s
`TestEveryProviderHasSpecRecorded` fails the build if a provider drops one. The fourth profile is doing exactly
this ahead of its handler: [`mcp/provenance.yaml`](mcp/provenance.yaml) records the `spec:` block and the
provider-level `verified:` date for a provider that is not yet registered (its header says why), so that when unit 2
registers `mcp` the guards find the record already in place rather than an empty directory. It is the example of a
contract recorded before its provider registers, and any later profile should start the same way.

### The sanctioned refresh procedure

1. **Check whether the spec changed.** Fetch the provider's machine-readable specification (`spec.url` in its
   `provenance.yaml`), compute its SHA-256, and compare against the recorded `spec.sha256`.
   - **Unchanged** — move nothing (bumping `spec.retrieved` to today is optional and does not itself imply a
     re-verification).
   - **Changed** — re-read the fields this repository consumes against BOTH the per-entry `documentation_url` pages
     and the spec itself, comparing against the provider's README tables, then continue to steps 2–4 below. A
     changed hash is not itself a diff: most entries were verified against prose, not the spec, so the hash change
     alone does not say which of them moved.
2. Update the affected `contracts/<provider>/README.md` tables and the golden fixtures for whatever changed, and
   each re-checked entry's own `verified:` date — a re-read that finds no change still moves it, because that is
   what the date means; only entries you did not re-read keep their date — and its `api_version`, if the document
   it came from is versioned, in the same change.
3. Bring the provider-level `verified:` at the top of `provenance.yaml`, this file's index-table **Verified**
   column, and `spec.sha256`, `spec.version` and `spec.retrieved` into agreement. The pairs
   `TestProviderVerifiedMatchesReadmeIndexTable` and `TestSpecRetrievedIsAtLeastProviderVerified` mechanically
   enforce are exactly the ones that must agree here; `TestSpecRetrievedIsAtLeastProviderVerified` rejects a
   `spec.retrieved` older than the provider-level `verified:` it backs, so if a refresh spans several days,
   re-fetch and re-hash the spec on the day you set `verified:`, not before it. The provider README's own headline
   "Verified against..." date is a separate, currently-unenforced line and updating it is good practice but not
   mechanically checked.
4. Cut a Servicesim release — provider handler and contract changes are release-worthy; product-specific scenario
   changes in consuming repositories are not.

After a refresh, `contracts.VerifiedOn` reads the oldest per-entry `verified:` date across every provider. A
hash-only check that finds a provider's spec unchanged re-checks no fixture, so nothing moves and `VerifiedOn` still
reads whatever it read before. A check that finds the spec changed re-checks every entry step 1 sends it to re-read,
so `VerifiedOn` moves to the oldest entry that pass did not cover — it moves only because step 2 actually
re-checked and re-dated the entries that were holding it down, never merely because a check ran and found nothing.

## Known upstream deprecations

These were observed during the 2026-08-14 verification and affect what new consumer code should emit or parse.

- **Perplexity Sonar has an announced end date.** [`perplexity/README.md`](perplexity/README.md) records it as
  supported until 2026-09-27, per the banner on every Sonar documentation page, with `POST /v1/agent` as its
  announced successor and `/v1/responses` as that route's OpenAI-compatible alias. Simulating Sonar remains
  correct for existing adapters, but new adapter work should be scoped against the Agent API.
- **Perplexity `citations` is deprecated** (changelog, May 2025) in favour of `search_results`, which carries
  titles, URLs and publication dates. Servicesim still emits `citations` so that existing consumers keep working,
  but adapter contract tests should assert on `search_results`.
- **Exa `useAutoprompt`, `livecrawl`, `startCrawlDate`, `endCrawlDate`, `numSentences` and `highlightsPerUrl` are
  deprecated.** Servicesim accepts them and flags them as journal warnings rather than rejecting them, so a consumer
  can prove its adapter has stopped sending them.
- **Tavily `days` has been removed** from the `/search` request schema; recency is expressed through `time_range`,
  `start_date` and `end_date`.
