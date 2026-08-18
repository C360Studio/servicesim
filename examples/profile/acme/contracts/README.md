# Acme contract bundle

Acme is a fictional vendor invented for Servicesim's own out-of-tree migration proof (Phase 10
unit 9) and for `docs/building-a-profile.md`'s worked example. This bundle exists to prove that
`contracts.Conform` and `testkit.ValidateProfile` run against a profile's own contract bundle
outside the `servicesim` module — it is **not** a real vendor's verified contract in the sense
the repository root's `contracts/README.md` means for the four reference profiles (Exa, Tavily,
Perplexity, MCP).

A real profile's bundle looks the same shape as this one — goldens, a `provenance.yaml`, this
README — but every entry in its `provenance.yaml` records an actual verification: a real
documentation URL, a date someone read it, and (when the vendor publishes one) the SHA-256 of a
really-fetched machine-readable specification. See `profiles/tavily/contracts/README.md` in the
repository root for what that looks like in full.

## Keeping them honest

There is no live contract canary (D10, `docs/adopter-backlog.md`): drift detection is manual and
dated. `provenance.yaml`'s `spec:` block records the SHA-256 of a placeholder string, not a
fetched document, and says so in its own header — a real profile's `spec:` block is what a dated
re-verification hash-checks before re-reading the consumed fields by hand.

## Endpoints

| Method | Path | Fault key | Note |
|---|---|---|---|
| `POST` | `/v1/answer` | `acme:answer` | Requires `query` (a non-empty string) and `Authorization: Bearer <token>`. |
| `GET` | `/v1/status` | `acme:status` | Requires `Authorization: Bearer <token>`. No request body. |

Both routes read the one `providers.acme.fault` plan; the distinct fault keys give each route its own
attempt *cursor* into that plan, not a plan of its own. A 429 consumed by `/v1/answer` therefore does not
advance where `/v1/status` sits.

## Authentication

`Authorization: Bearer <token>` on every route (`Profile.DefaultAuth: scenario.AuthRequired`). No
other placement is accepted — `x-acme-key` is declared in `Profile.CredentialNames` so it is
redacted if a scenario or a scripted fault ever echoes it, but it does not authenticate a request
on its own.

## Response fields

`POST /v1/answer`:

| Field | JSON type | Note |
|---|---|---|
| `request_id` | `string` | 32-character lowercase hex, derived — never pinned in a golden compare (`testkit.GoldenDerivedIDs("request_id")`). |
| `answer` | `string` | From the scenario's `answer:`, or `""` when unset. |
| `confidence` | `number` | Omitted when the scenario leaves it zero. |

`GET /v1/status`:

| Field | JSON type | Note |
|---|---|---|
| `request_id` | `string` | Same derivation as `POST /v1/answer`'s. |
| `status` | `string` | From the scenario's `status:`, or `"operational"` when unset. |

## Errors

Every refusal — 400, 401, 404, 405, 500 — renders one envelope:

```json
{"error":{"code":"...","message":"..."}}
```

`code` is a stable symbol, not the numeric status, so a consumer asserts on it rather than on prose. The
whole vocabulary:

| Status | `code` | When |
|---|---|---|
| 400 | `bad_request` | A documented request field is absent, empty or the wrong type (`acme.query.missing`). |
| 401 | `unauthorized` | No credential, or one that does not match the scenario's `expect_key`. |
| 404 | `not_found` | An unmatched path, or a scenario naming no Acme block at all. |
| 404 | `no_matching_turn` | The scenario declares Acme but no turn answers this request. |
| 405 | `method_not_allowed` | A known path reached with a method it does not serve. |
| 429 | `rate_limited` | A scripted fault attempt. |
| 500 | `internal_error` | A projection that failed to decode or render. |

A scripted fault reuses this same table (`errors.go`'s `statusCodes`), so `fault: {attempts: [{status:
429}]}` renders `rate_limited`, never `"429"`.

## Fixtures

| Golden | Status | Note |
|---|---|---|
| `acme-answer-happy.json` | 200 | `POST /v1/answer`, a normal answer |
| `acme-answer-empty.json` | 200 | the empty-result case |
| `acme-status-happy.json` | 200 | `GET /v1/status` |
| `acme-error-400.json` | 400 | a request missing the documented `query` field |
| `acme-error-401.json` | 401 | missing or mismatched credential |
| `acme-error-404.json` | 404 | unmatched path |
| `acme-error-405.json` | 405 | a known path, an unsupported method |

Every one of these is driven from a live response by `acme_test.go`'s `TestAcmeGoldensMatchTheWire`: a
bundle nothing compares against drifts silently.
