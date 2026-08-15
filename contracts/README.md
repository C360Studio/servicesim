# Consumed contracts

Each provider directory records the subset of its vendor API that Servicesim simulates and that C360 consumers
parse — the *consumed contract*, not the whole vendor surface. Every file carries the documentation URLs it was
derived from and the date the shape was verified.

| Provider | Contract | Verified | Base URL simulated |
|---|---|---|---|
| Exa | [`exa/README.md`](exa/README.md) | 2026-08-15 | `POST /search`, `POST /answer`, `POST /agent/runs`, `GET /agent/runs/{id}`, `HEAD /agent/runs/{id}` |
| Tavily | [`tavily/README.md`](tavily/README.md) | 2026-08-15 | `POST /search`, `POST /research`, `GET /research/{request_id}`, `HEAD /research/{request_id}` |
| Perplexity | [`perplexity/README.md`](perplexity/README.md) | 2026-08-14 | `POST /v1/sonar`, `POST /chat/completions`, `POST /v1/chat/completions`, `POST /v1/agent`, `POST /v1/responses`, `POST /responses` |

Every route in that column has golden fixtures in this directory. Treat it as the complete list of what is
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
| Exa | `/contents` | NOT SIMULATED | No verified vendor contract recorded yet; scheduled for verification. Referenced only indirectly, by the error-codes page's `statuses[]` / `CRAWL_*` tags. |
| Exa | `/findSimilar` | NOT SIMULATED | No verified vendor contract recorded yet; scheduled for verification. |
| Exa | `/agent/runs` lifecycle beyond create and poll | NOT SIMULATED | Create and poll ARE simulated — see the table above. The rest of the lifecycle (listing all runs, `/agent/runs/{id}/events`, `/agent/runs/{id}/cancel`, and deleting a run) has no verified contract and is not simulated. Paths here are written without a method on purpose: the index guard reads a backticked method-plus-path as a claim that the route IS simulated. |
| Tavily | `/extract` | NOT SIMULATED | No verified vendor contract recorded yet; scheduled for verification. |

"Scheduled for verification" means exactly that: the vendor documentation has not been fetched, dated and recorded
under [the one rule](../CONTRIBUTING.md#the-one-rule-that-matters-most), so nothing here asserts a method, a request
shape or a response shape for those paths. Verification comes first; simulation is a separate decision after it.

## Why these files exist

Golden wire fixtures are only trustworthy if a reviewer can tell what they were checked against. When a fixture
changes, the reviewer's question is "did the vendor change, or did we?" — and that question is unanswerable without
a dated provenance record. These files are that record.

They are deliberately *not* copies of the vendors' OpenAPI documents. Whether those can be redistributed under their
respective terms is an open question in the plan's deferred decisions, so this repository keeps only reviewed
minimal schemas for the fields it actually implements.

## Keeping them honest

Simulator tests cannot detect vendor drift — a simulator agrees with itself by construction. The live contract
canary (plan Phase 5) makes one bounded request per provider against the real API on a manual or scheduled trigger,
validates only the consumed fields listed here, and fails on removed or incompatible fields. Additive fields are
reported without failing, because external APIs evolve additively and consumers are expected to tolerate that.

When the canary reports drift:

1. Update the affected `contracts/<provider>/README.md` and its **Verified** date.
2. Update the provider handler and its golden fixtures in the same change.
3. Cut a Servicesim release — provider handler and contract changes are release-worthy; product-specific scenario
   changes in consuming repositories are not.

## Known upstream deprecations

These were observed during the 2026-08-14 verification and affect what new consumer code should emit or parse.

- **Perplexity Sonar has an announced end date.** Every Sonar documentation page carries "Sonar Chat Completions is
  now Agent API. Sonar will be supported until September 27, 2026", with `POST /v1/agent` as the successor
  canonical endpoint and `/v1/responses` as its OpenAI-compatible alias. Simulating Sonar remains correct for
  existing adapters, but new adapter work should be scoped against the Agent API.
- **Perplexity `citations` is deprecated** (changelog, May 2025) in favour of `search_results`, which carries
  titles, URLs and publication dates. Servicesim still emits `citations` so that existing consumers keep working,
  but adapter contract tests should assert on `search_results`.
- **Exa `useAutoprompt`, `livecrawl`, `startCrawlDate`, `endCrawlDate`, `numSentences` and `highlightsPerUrl` are
  deprecated.** Servicesim accepts them and flags them as journal warnings rather than rejecting them, so a consumer
  can prove its adapter has stopped sending them.
- **Tavily `days` has been removed** from the `/search` request schema; recency is expressed through `time_range`,
  `start_date` and `end_date`.
