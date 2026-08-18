# Building a profile

This is the guide a first-time profile author follows: how to write a Servicesim profile for a vendor this
repository does not ship, in your own repository, against nothing but the exported packages — and how to prove it
to the same standard the four reference profiles are held to, in your own CI. It is written against a profile
that exists and is built on every push: [`examples/profile/`](../examples/profile), a separate Go module holding a
fictional vendor, Acme, composed into its own binary. Every Go and YAML block below is copied out of that module by
line range, and a test keeps the copy honest.

## How to read the code blocks

Every fenced Go or YAML block in this guide is a literal excerpt of a file under `examples/profile/`, and the line
immediately before it says which one:

```text
<!-- excerpt: examples/profile/acme/profile.go#L31-L100 -->
```

That is an HTML comment naming a file and an inclusive, 1-based line range. `docs_test.go` in the repository root
(`TestBuildingAProfileExcerptsMatchTheModule`, run by `go test ./...` and therefore by `task test` and CI) reads
every marker, opens the file, and fails unless the block is byte-for-byte those lines — and it fails any Go or YAML
block that carries no marker at all, so a code block cannot quietly become prose that only *looks* like the module.
Line ranges were chosen over named region comments in the source so that the module's files stay exactly what an
adopter would copy, free of excerpt bookkeeping; the cost is that an edit above a quoted range moves it, and when
that happens the test reports the range the block now occupies, so the fix is to update the marker. A block that no
longer exists anywhere in the file is reported with the first line that differs: the source changed under the
guide, and the guide is the thing to fix.

The excerpts are shown without their surrounding file. Read the whole file when you copy — the module is the
example; this guide is its narration.

## What a profile is

`CLAUDE.md` says it in one sentence: a profile is *a verified vendor contract plus deterministic scenarios, added the
way the shipped profiles were, not free-form request/response configuration*. Out of tree, "the way the shipped
profiles were" means one thing concretely: `testkit.ValidateProfile` and `contracts.Conform` run in your own CI, over
your own `provider.Profile` and your own embedded contract bundle, and pass. There is no registration step in this
repository, no PR, no review by the framework's authors — and therefore nothing but that conformance call standing
between "a Go package that answers HTTP" and "a Servicesim profile". The four reference profiles under
[`profiles/`](../profiles) each call it from their own `conformance_test.go`; so does Acme.

The mechanics that make the house rules hold for a profile whose author never read them are the framework's:
redaction happens before the journal retains anything and widens by data (`CredentialNames`), never by a call;
identifiers derive through `provider.Hex32`/`provider.UUIDv5` and read no clock; the fault engine is built from
the composed route set, so an engine that does not know your route is unconstructible; a `Profile` with no
`ErrorBody` is refused by `provider.NewSet` before it can serve an empty 404. What the framework cannot make true by
construction — that a profile validates requests hard, renders through `provider.Render`, and records where every
wire field was read from — is what this guide, the reference profiles and `ValidateProfile` are for
(`docs/proposals/framework-seam.md`, "House rules by construction", says which is which and why).

## The module layout

```text
examples/profile/                   module example.test/acmesim — its own go.mod, replace ../.. for this checkout
├── go.mod                          require github.com/c360studio/servicesim; nothing else
├── main.go                         servicesim.Main over provider.MustSet(acme.Profile(), exa.Profile())
├── Dockerfile                      a two-stage build over that binary; EXPOSE from --print-ports
├── imports_test.go                 no servicesim/internal import anywhere; go.mod stays within budget
├── README.md
└── acme/                           the profile package — what a consuming team writes
    ├── doc.go                      what is simulated, what is not, the decisions made
    ├── profile.go                  Name, Profile(): every field a real one sets
    ├── handler.go                  routes, fault keys, the Validator, the two handlers, checkAuth
    ├── render.go                   the wire types, the projection body, requestID, provider.Render
    ├── errors.go                   ErrorBody for every RefusalKind, errorResponse, faultBody
    ├── acme_test.go                the tests an adopter writes, through testkit
    ├── conformance_test.go         testkit.ValidateProfile(t, acme.Profile())
    └── contracts/                  embedded: README.md, provenance.yaml, seven goldens
```

(That tree is checked too, by a second test in `docs_test.go`: a file added to or removed from the module and not
reflected here fails the build. `go.sum` is the one deliberate omission — a lock file, not something anyone
writes.)

Two things about the tree are the point rather than incidental. It is a *separate module*, so `go build ./...`
here proves the profile compiles against servicesim's exported surface alone — Go's own import rules make
`servicesim/internal/...` unreachable from it, and `imports_test.go` proves that rather than relying on it. And it
is built and tested by this repository's `task test` and CI (`Taskfile.yml`'s `test:profile`), because an example
that is not built rots ([`examples/doc.go`](../examples/doc.go)).

Starting your own is three lines. There is nothing to install and nothing to register:

```sh
go mod init example.test/yoursim
go get github.com/c360studio/servicesim@latest
go mod tidy
```

Six packages are importable, and they are the whole surface: `provider` (the profile record, the handler
lifecycle, the render and derivation helpers), `scenario` (the schema types your Validator sees), `testkit` (the
in-process simulator and the assertions), `contracts` (the golden bundle and `Conform`), `scenarios` (this
repository's built-in corpus, reference-only) and the root `servicesim` package (`Main`, for composing a binary).
Everything else is under `internal/` and unreachable by Go's own import rules. `gopkg.in/yaml.v3` and
`github.com/google/go-cmp` arrive as indirect dependencies of `testkit`; you do not name them.

While developing against a servicesim checkout rather than a release, add a `replace` — which is exactly what
this module does, and the one line a real adopter's `go.mod` would not have:

```text
replace github.com/c360studio/servicesim => ../path/to/servicesim
```

## Step 1 — verify the contract first

Before any Go. The reference profiles' contract files are the shape to copy: each `profiles/<name>/contracts/`
holds a `README.md` recording what the vendor's live documentation actually says — the endpoints, the request and
response fields consumers parse, the error envelope, each statement citing the page it was read from with the URL
and the date — a `provenance.yaml`, and the golden fixtures the handlers are tested against.
[`profiles/tavily/contracts/README.md`](../profiles/tavily/contracts/README.md) is the fullest one;
[`profiles/mcp/contracts/README.md`](../profiles/mcp/contracts/README.md) is the model for a protocol rather than a
vendor. What the specification is silent on is a **simulator-chosen** decision: record it as such, and once the
handler ships, record what was chosen beside it. List what is *not* simulated as a table rather than omitting it.

`provenance.yaml` is what `contracts.Conform` (inside `ValidateProfile`) checks. Every golden has an entry —
endpoint, status, `kind` (`vendor-documented` or `simulator-chosen`), `documentation_url`, `verified`, and
`note` — and the file carries a provider-level `verified:` date and, when the vendor publishes a machine-readable
specification, a `spec:` block: its URL, the version string the document itself carries, the SHA-256 of its bytes
as fetched, and the date. `note` is required and is not decoration: it says what is *load-bearing* about this
fixture — which field's type it pins, which error shape it is the only record of — so the next person to
re-verify knows what they would be breaking. Acme is fictional, so its `spec:` block records a placeholder hash
and says so in the file's own header:

<!-- excerpt: examples/profile/acme/contracts/provenance.yaml#L17-L23 -->
```yaml
provider: acme
verified: "2026-08-17"
spec:
  url: https://docs.acme.test/openapi.json
  version: "1.0.0"
  sha256: b2a108cd05c4142d2596a4df053af89716c594c0cd06064f1c56d81b1f2d0e16 # sha256("acme placeholder spec") -- not a fetched document
  retrieved: "2026-08-17"
```

A real profile records the real document here. The reason the block exists at all is
[ADR 0002](adr/0002-verified-contract-precedence.md): the verified contract outranks every other document, including
this repository's own plan, and a wrong wire field in a simulator is a production bug distributed to every consumer
with a green test suite vouching for it. Drift detection is manual and dated — there is no live canary — so the
hash is what a re-verification checks first, before re-reading the consumed fields by hand
([`contracts/README.md`](../contracts/README.md), "Keeping them honest").

Two mechanical rules decide the bundle's shape, and both are cheaper to design for now than to discover on your
first `ValidateProfile` run.

**The bundle is embedded whole, so it may hold only contract data**: `*.json` and `*.sse` goldens,
`provenance.yaml`, and `README.md`. Nothing else — a saved `openapi.json` you are working from, a scratch file of
sample responses, a screenshot — belongs beside them; keep working notes outside the directory the `//go:embed`
sweeps up.

**The goldens have a required minimum, and one of the classifications is by file name.** `Conform` wants at
least one 2xx golden whose name does **not** contain `empty`, at least one 2xx golden whose name **does**, and at
least **two** records with a status of 400 or more. So a bundle with one error golden fails, and so does a bundle
whose empty-result fixture is called `yoursim-lookup-unknown-id.json` — a 2xx record is classed as "empty" or
"success" by its file name, never by inspecting its body. Name them the way the reference bundles do —
`yoursim-lookup-empty.json` beside `yoursim-lookup-happy.json` — and the rule disappears. Acme ships seven
goldens: three successes, and one each for 400, 401, 404 and 405.

And a golden nothing compares against stops being true silently. `Conform` checks that each is valid JSON with a
complete provenance record; it never looks at the content, because it does not know your vendor. Drive each one
from a live response in your own tests — step 4 shows Acme's — or the bundle drifts with every handler change and
every gate stays green.

Getting this step wrong is the expensive mistake: everything downstream is generated from it, and a wrong field
type propagates into fixtures that then look authoritative.

## Step 2 — write the package

`Name` is the listener's identity everywhere at once — the base-URL env prefix, the port flag and env var, the
journal's `provider` field, and the scenario's `providers:` key — so it is a typed constant, not a string repeated
in four places. `Port` is a default, and one that must not collide with anything the binary composes alongside:

<!-- excerpt: examples/profile/acme/profile.go#L14-L21 -->
```go
const Name provider.Name = "acme"

// defaultPort is Acme's default listener port. A real profile picks one that
// does not collide with any listener it expects to run alongside — this
// example composes with servicesim's own exa (8081), so 8090 stays clear of
// every reference profile's 8081-8084 range.
const defaultPort = 8090

```

### `Profile()`, field by field

`Profile()` is a function returning a value, not a package-level variable, so no importer can mutate a
registration it did not build. Every field below is one a real profile sets; the comments in the source say why each
one is there and what depends on it, and this section only adds what the source cannot say about itself:

<!-- excerpt: examples/profile/acme/profile.go#L31-L100 -->
```go
// Profile returns Acme's registration record: everything servicesim.Main and
// testkit.WithProfiles need to serve, validate and journal this vendor,
// composed from the fields handler.go, render.go and errors.go declare.
//
// Every field here is one a real profile sets; docs/building-a-profile.md
// walks each one in turn under "step 2, write the package".
func Profile() provider.Profile {
	sub, err := fs.Sub(contractsFS, "contracts")
	if err != nil {
		// Unreachable: "contracts" is the fixed, valid fs.Sub pattern the
		// //go:embed directive above always populates.
		panic("acme: contracts sub-FS: " + err.Error())
	}

	return provider.Profile{
		Name:    Name,
		Title:   "Acme",
		Summary: "the Acme answer API (a worked example vendor, not a real one)",
		Port:    defaultPort,

		Handlers: handlers(),
		Routes:   Routes(),
		Validators: map[string]provider.Validator{
			string(Name): validator{},
		},
		ErrorBody: ErrorBody,

		// DefaultAuth is required, opposite MCP's optional default
		// (docs/proposals/framework-seam.md documents both defaults side by
		// side): Acme documents Authorization: Bearer on every route, so a
		// request presenting no credential at all is refused before it ever
		// reaches turn selection. This is what exercises the 401 convention
		// testkit.ValidateProfile's MissingCredential subtest checks.
		DefaultAuth: scenario.AuthRequired,

		Contracts: sub,

		// Hosts is the denylist scripts/lint-no-live-hosts.sh (in this
		// repository) and testkit.AssertNoLiveHosts (in an adopter's own CI)
		// refuse to see in scenario or fixture data. A real profile puts the
		// vendor's real, live hostname here, never a placeholder — see
		// profiles/exa/profile.go's own Hosts field for what that looks
		// like — so that a base URL typo'd into a scenario fixture is caught
		// before it ever dials out. Acme is fictional, so this is a reserved
		// .test domain instead, which is what proves the guard treats a
		// profile's own declared hosts as data rather than as a
		// hand-maintained list only servicesim's own four vendors can join.
		//
		// The trailing marker on the line below is the same escape hatch
		// every reference profile's own Hosts field carries (see, for
		// example, profiles/tavily/profile.go): AssertNoLiveHosts's pattern
		// is the union of every registered profile's own Hosts, so the ONE
		// place that is expected to name them literally — this declaration
		// itself — would otherwise trip the very guard it feeds.
		Hosts: []string{"api.acme.test"}, // servicesim:allow-live-host -- the DENYLIST entry Profile.Hosts exists to carry; never dialled.

		// DerivedIDs names the one response field both routes render from
		// provider.Hex32 rather than from the scenario: a golden compare
		// that pins request_id exactly would break on every re-record for no
		// reason connected to the vendor's actual documented shape.
		DerivedIDs: []string{"request_id"},

		// CredentialNames widens the journal's redaction vocabulary beyond
		// Authorization (which house rule 4's fixed tables already mask) to
		// a header this vendor invented: x-acme-key. Declaring it here is
		// the whole of what a profile author does for house rule 4 — no
		// redaction code of Acme's own exists anywhere in this package.
		CredentialNames: []string{"x-acme-key"},
	}
}
```

- `Handlers` and `Routes` are the two halves of one table: `provider.NewSet` refuses a route with no handler and a
  handler with no route, so a route added to one and forgotten in the other fails at composition, not on the first
  request.
- `Validators` is keyed by scenario **entry kind**, not by listener — one listener may contribute several
  (Perplexity's Sonar and Agent surfaces). Acme has one, under its own name.
- `ErrorBody` is **required**. House rule 3: an unmatched path, method, provider or scenario answers in the
  vendor's own error shape, never with an empty body. `provider.NewSet` refuses a `Profile` without it.
- `DefaultAuth` is the mode an entry with no `auth:` block of its own gets — a *default your handler reads*, not
  an enforcement the framework performs. Acme documents a bearer token on every route, so it is
  `scenario.AuthRequired`, and Acme's own `checkAuth` is what turns that into a 401 before turn selection, the
  convention `ValidateProfile`'s `MissingCredential` subtest checks. MCP's default is optional, because a
  protocol's own transport-level checks gate access there, not an API key. Pick the one the real vendor documents;
  do not pick the convenient one. See "Authentication is yours" below for the rest of what the handler owes.
- `Contracts` is your embedded bundle as an `fs.FS`. `ValidateProfile` fails a nil `Contracts` — a profile ships
  its contract, with no exception.
- `Hosts` is a **denylist**. Put the vendor's real, live hostname here — never a placeholder. Nothing dials it: it
  is what `testkit.AssertNoLiveHosts` in your CI, and `scripts/lint-no-live-hosts.sh` in this repository, refuse to
  find in a scenario, a fixture or a Go default, because a base URL that quietly resolves to a real paid API is
  discovered in a billing statement. Acme's is a reserved `.test` domain only because Acme is fictional; the trailing
  `servicesim:allow-live-host` marker on the declaration is the one line that is allowed to name it literally.
- `DerivedIDs` names the response fields you derive per call rather than script — a request identifier, most
  often — so a consumer's golden compare can prune them with `testkit.GoldenDerivedIDs` instead of pinning a value
  that changes on every re-record. `StreamDerivedIDs` is the same for paths inside a decoded SSE frame.
- `CredentialNames` widens redaction to the header and JSON-property names this vendor invented. That is the whole
  of what a profile author does for house rule 4: no redaction code of your own, and no way to turn it off.

### Routes and fault keys

<!-- excerpt: examples/profile/acme/handler.go#L61-L73 -->
```go
// Fault keys. Two routes, two independent attempt budgets: a scripted 429 on
// answer must never consume the budget a status poll draws on, and vice
// versa.
const (
	FaultKeyAnswer = "acme:answer"
	FaultKeyStatus = "acme:status"
)

// Route patterns.
const (
	PatternAnswer = "POST /v1/answer"
	PatternStatus = "GET /v1/status"
)
```

<!-- excerpt: examples/profile/acme/handler.go#L86-L106 -->
```go
func Routes() []provider.Route {
	return []provider.Route{routeAnswer(), routeStatus()}
}

func routeAnswer() provider.Route {
	return provider.Route{
		Pattern:     PatternAnswer,
		FaultKey:    FaultKeyAnswer,
		Credentials: defaultPlacements,
		Fault:       func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, string(Name)) },
	}
}

func routeStatus() provider.Route {
	return provider.Route{
		Pattern:     PatternStatus,
		FaultKey:    FaultKeyStatus,
		Credentials: defaultPlacements,
		Fault:       func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, string(Name)) },
	}
}
```

Two routes that are the same operation reached two ways (an SDK alias) share a `FaultKey`, so a retry through the
alias draws on the same scripted attempt budget; two genuinely different surfaces get different keys, so a 429
scripted for one can never land on the other. `Route.Fault` says which scenario block scripts this route's faults;
`provider.TurnFault` over the entry name is the ordinary answer. `Credentials` lists the placements the vendor
documents — Acme accepts an `Authorization` header and nothing else; Tavily's body-placed `api_key` is the worked
example of a second placement ([`profiles/tavily`](../profiles/tavily)).

### The handler order

<!-- excerpt: examples/profile/acme/handler.go#L169-L223 -->
```go
// handleAnswer serves POST /v1/answer.
//
// The order is the one docs/building-a-profile.md's step 2 documents and is
// not an accident. Every check that needs nothing from the scenario runs
// first — content type (a warning, never a rejection: Acme documents no 415),
// authentication, then the documented required field — and they all run
// before the ONE gate, so a request that is both unauthenticated and
// malformed journals both problems rather than sending a consumer round the
// loop twice. Only a request that survived the gate is allowed to select a
// turn, because selecting a turn claims an attempt from the fault budget and
// a rejected request must never consume one (CONTRIBUTING's "validate before
// you claim"). Every reference profile has this shape.
func handleAnswer(x *provider.Exchange) provider.Response {
	if !x.HasJSONContentType() {
		x.Warn(CodeContentType, "", "Content-Type %q is not a JSON media type", x.Request.Header.Get("Content-Type"))
	}

	checkAuth(x)

	// x.Fail, not x.Reject: Reject returns the status it was handed whatever
	// the scenario's validation policy says, so a code the author listed
	// under validation.demote would still be refused while the journal
	// recorded it as a warning. errorResponse reads the findings AFTER the
	// policy has been applied, so a demoted finding lets the request render
	// normally — which is what docs/scenario-schema.md documents demote to
	// mean.
	if query, ok := x.String("query"); !ok || strings.TrimSpace(query) == "" {
		x.Fail(CodeQueryMissing, "query", "query is required and must be a non-empty string")
	}

	if x.Failed() {
		return errorResponse(x)
	}

	projection, ok := selectProjection(x)
	if !ok {
		return errorResponse(x)
	}

	body, err := renderAnswer(x, projection)
	if err != nil {
		// Unreachable: AnswerResponse has no field provider.Render's
		// json.Marshal can fail on. A journaled 500 beats a panic.
		x.Fail(CodeProjectionInvalid, "", "rendering the answer: %v", err)
		return errorResponse(x)
	}

	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         string(Name) + ".answer.ok",
		FaultEligible: true,
		FaultBody:     faultBody,
	}
}
```

The order is the one rule in this file that is not a matter of taste. `provider.SelectTurnFor` **claims** an
attempt from the route's fault budget; every check that needs nothing from the projection — content type,
authentication, the documented required fields — runs before it, or a rejected request spends a scripted fault's
attempt. `provider.Handle` refuses to let a claimed attempt reach the wire on a rejection (`fault.attempt_on_rejection`
in the journal), so a rejection never *wears* a fault's status, but the claimed index stays spent regardless — and
`ValidateProfile`'s `RejectionDoesNotClaimAnAttempt` subtest checks that a rejection followed by a valid request
still receives attempt 0. Then render; then a `provider.Response` with `FaultEligible` set and a `FaultBody` that
renders a scripted 429 or 503 in *your* envelope rather than the framework's.

Note there is **one** gate, and every request-side check runs before it. A request that is both unauthenticated
and malformed should journal both problems; a handler that returns on the first one sends a consumer round the
loop twice, discovering a second, previously invisible bug only after fixing the first. Every reference profile
records its findings and then gates once (`profiles/tavily/handler.go`, `profiles/exa/handler.go`).

`x.Fail` records an error finding; `x.Warn` records a warning; the finding code — an exported constant, so a
consumer's test can assert on it — is what a test proves a rejection by, not the status you happened to answer
with. Prefer `x.Fail` plus the `x.Failed()` gate over `provider.Exchange.Reject`, which returns the status it was
handed **whatever the scenario's `validation:` policy says**: a code an author listed under `validation.demote`
would still be refused on the wire while the journal recorded it as a warning. `x.Failed()` applies the policy
first, so demote and promote actually mean what `docs/scenario-schema.md` says they mean.

Content type is where the vendor's documentation stops being the only input. Recording a content-type finding on
at least one POST route is **mandatory**, and the code you choose must contain the literal substring
`content_type` or `content-type` — `ValidateProfile`'s `WrongContentType` subtest sends a `text/plain` body and
fails a profile that records nothing matching. What is yours to decide is only the *severity*: Acme warns rather
than rejecting because its documentation describes no 415, the same call the three research reference profiles
make, while MCP rejects because a non-JSON content type is a protocol violation there.

### Authentication is yours, and mostly untested by the framework

Nothing in the framework enforces a scenario's `auth:` block. `Profile.DefaultAuth` sets the default *mode*, and
`ValidateProfile`'s `MissingCredential` subtest proves exactly one thing — that a request with **no credential at
all** answers 401. Every other part of the auth surface is the handler's own work, and a handler that ignores it
entirely still passes conformance. That is the single easiest way to ship a profile whose negative auth tests are
all meaningless, so this is Acme's `checkAuth` in full:

<!-- excerpt: examples/profile/acme/handler.go#L255-L312 -->
```go
// checkAuth applies Acme's one documented rule: Authorization: Bearer
// authenticates; nothing else does. A scenario's auth.headers or
// auth.expect_key can narrow or replace that (Exchange.AcceptedPlacements is
// the one precedence rule every provider package in this repository shares —
// see profiles/tavily/request.go's own checkAuth for the fuller worked
// example, including a second accepted placement).
func checkAuth(x *provider.Exchange) {
	policy := x.AuthPolicy()
	if policy.Mode == scenario.AuthReject {
		// Deliberately a mismatch rather than a missing credential: something
		// was or was not presented, and neither can ever match under this mode.
		x.Fail(CodeAuthMismatch, "authorization", "the scenario rejects every credential")
		return
	}

	accepted := x.AcceptedPlacements(policy, defaultPlacements)
	var presented []provider.Credential
	for _, cred := range x.Credentials() {
		if slices.Contains(accepted, cred.Header) {
			presented = append(presented, cred)
		}
	}

	// x-acme-key is declared in Profile.CredentialNames so the journal
	// redacts it; that is not the same as accepting it. Naming the header
	// that does authenticate is the difference between a consumer reading
	// "unauthorized" and a consumer reading what to change
	// (profiles/tavily/request.go's own CodeAuthWrongHeader is the
	// reference). x.Credentials() reports only the placements the shared
	// header scan recognises — authorization and x-api-key — so a
	// vendor-invented header is read off the request directly. Its presence
	// is all that is read: the value is never quoted into a finding.
	if x.Request != nil && x.Request.Header.Get("X-Acme-Key") != "" {
		x.Warn(CodeAuthWrongHeader, "x-acme-key",
			"x-acme-key does not authenticate; Authorization: Bearer <token> does")
	}

	if len(presented) == 0 {
		if policy.Mode != scenario.AuthOptional {
			x.Fail(CodeAuthMissing, "authorization", "Authorization: Bearer <token> is required")
		}
		return
	}

	for _, cred := range presented {
		if cred.Header == provider.PlacementAuthorization && cred.Scheme != "Bearer" {
			x.Warn(CodeAuthWrongScheme, "authorization",
				"Authorization does not carry the documented Bearer scheme")
		}
	}

	if policy.ExpectKey != "" && !slices.ContainsFunc(presented, func(c provider.Credential) bool {
		return c.Value == policy.ExpectKey
	}) {
		// The value is never quoted; the journal holds only a fingerprint of it.
		x.Fail(CodeAuthMismatch, "authorization", "the presented credential is not the expected key")
	}
}
```

Read it as four obligations:

- **`x.AuthPolicy()` returns the mode in force**, resolved from the entry's `auth:` block or, when it has none,
  `Profile.DefaultAuth`. `scenario.AuthReject` refuses everything — that is how a consumer tests its 401 path
  without knowing a key. `scenario.AuthOptional` means a request with no credential is served. `AuthRequired` is
  the ordinary case. Handle all of them: a handler that only looks at `AuthRequired` silently serves a scenario
  that declared `mode: reject`.
- **`x.AcceptedPlacements(policy, yourDefault)` is the precedence rule**, and it is the same in every provider
  package here: a scenario's `auth.headers` wins, then `Route.Credentials`, then your profile's default. So a
  scenario can narrow a two-placement vendor to one, and your handler must ask rather than hard-coding.
- **`policy.ExpectKey` is compared, never journaled.** Compare it against `Credential.Value`, then say what was
  *wrong* — never quote the value, in a finding message or anywhere else (house rule 4).
- **`x.Credentials()` reports only the placements the shared header scan knows**: `authorization` and
  `x-api-key`. A vendor whose credential arrives anywhere else — a header it invented, or a property of the JSON
  body — is yours to read, and `provider.Exchange.ObserveCredential` is how you tell the journal you found one, so
  `auth.present` is not false for a request you served. `profiles/tavily`'s body-placed `api_key` is the worked
  example.

Because none of that is checked for you, the negative auth tests are yours to write. Acme's
`TestAcmeAuthPolicyModes` and `TestAcmeUnacceptedCredentialHeaderSaysWhatDoes` are the shape: reject, optional,
`expect_key` matching and not, a non-Bearer scheme, and a credential in a header that does not authenticate.

`selectProjection` is where a request meets the scenario:

<!-- excerpt: examples/profile/acme/handler.go#L314-L347 -->
```go
// selectProjection picks the turn serving this request and decodes its
// projection body.
//
// A scenario that declares no acme block at all is not an error: it renders
// the zero projection, a well-shaped empty success — what makes
// acme.Profile().Handler(provider.Deps{}) a usable zero-configuration
// handler. A scenario that declares the block but no turn matching this
// request IS an error: the author wrote a script that cannot answer, and a
// silent empty 200 would hide that.
func selectProjection(x *provider.Exchange) (*projectionBody, bool) {
	entry := x.Entry()
	if entry == nil {
		return &projectionBody{}, true
	}

	turn, index := provider.SelectTurnFor(x, entry)
	if turn == nil {
		return nil, false
	}

	projection := &projectionBody{}
	if err := turn.DecodeProjection(entry.Name, index, projection); err != nil {
		// Unreachable through a caller that runs ValidateScenario before
		// readiness (internal/server, and testkit.Start's own build path)
		// — reachable by a caller that built a Scenario by hand.
		x.Fail(CodeProjectionInvalid, "", "%v", err)
		return nil, false
	}

	for _, finding := range x.Deps.Scenario.ResolveRefs(projectionPath(entry, index), projection) {
		x.Warn(CodeProjectionUnresolved, "", "%s: %s", finding.Path, finding.Message)
	}
	return projection, true
}
```

### Rendering: `provider.Render`, `Hex32`, and why not `encoding/json`

<!-- excerpt: examples/profile/acme/render.go#L56-L79 -->
```go
// requestID derives POST /v1/answer and GET /v1/status's shared identifier
// shape: a route-derived, 32-character lowercase hex string, the same shape
// Exa documents for requestId. It reads the scenario's seed, the listener
// name, the route's fault key and the claimed call index — never a clock,
// never math/rand — so the same request at the same call position always
// renders the same bytes (house rule 2).
func requestID(x *provider.Exchange) string {
	return provider.Hex32(x.Deps.Scenario.SeedKey(), string(x.Provider), x.Route.FaultKey, strconv.Itoa(x.CallIndex()))
}

// renderAnswer renders p through provider.Render, never encoding/json
// directly: a bare json.Marshal escapes "&" as "\u0026" and can re-render an
// integral float64 as "1e+06", both wire-contract changes dressed up as
// formatting details (house rule 2). See testkit.AssertRenderShape, which
// this profile's own conformance test runs against every response this
// package produces.
func renderAnswer(x *provider.Exchange, p *projectionBody) ([]byte, error) {
	answer := ""
	if p.Answer != nil {
		answer = *p.Answer
	}
	resp := AnswerResponse{RequestID: requestID(x), Answer: answer, Confidence: p.Confidence}
	return provider.Render(resp, p.ExtraFields, p.OmitFields)
}
```

`provider.Render` exists so a profile never marshals a response through `encoding/json` directly. A bare
`json.Marshal` escapes `&` as `\u0026` and can re-render an integral `float64` such as `1000000` as `1e+06`. Both
are perfectly deterministic — the same bytes on every run — so no determinism check can see them; both are
wire-contract changes dressed up as formatting details. `Render` also fixes the order of `extra_fields` and
`omit_fields` (merge first, then omit, as `docs/scenario-schema.md` documents), so a third ordering is
unrepresentable. `testkit.AssertRenderShape`, inside `ValidateProfile`, is a heuristic that catches the two named
divergences after the fact over every conformance response and every golden; `Render` is the answer that needs no
heuristic.

Identifiers derive: `provider.Hex32` and `provider.UUIDv5` are the exported derivations, and they read no clock.
The same request at the same call position renders the same bytes — house rule 2 — and `ValidateProfile`'s
`Deterministic` subtest starts two fresh simulators, sends the same requests to each and byte-compares, so a
`time.Now()` or a `math/rand` in a handler fails there before it flakes a consumer's suite.

### `ErrorBody`, for every kind

<!-- excerpt: examples/profile/acme/errors.go#L75-L102 -->
```go
// ErrorBody renders a provider.Refusal in Acme's own error shape, for every
// [provider.RefusalKind] — the REQUIRED field house rule 3 exists for (an
// unmatched path, method, provider or scenario must never answer with an
// empty body).
//
// RefuseRequest's branch reuses errorResponse(r.X): a request rejected
// through [provider.Exchange.Reject] (handleAnswer's CodeQueryMissing check)
// reaches ErrorBody exactly the way a request rejected through checkAuth's
// x.Fail reaches errorResponse directly, so the two paths cannot render two
// different envelopes for what is, from a consumer's point of view, one
// class of failure: "my request was refused, here is why."
func ErrorBody(r provider.Refusal) []byte {
	switch r.Kind {
	case provider.RefuseNotFound, provider.RefuseScenarioUnknown:
		return errorBody("not_found", messageNotFound)
	case provider.RefuseMethodNotAllowed:
		return errorBody("method_not_allowed", messageMethodNotAllowed)
	case provider.RefuseInternal:
		return errorBody("internal_error", messageInternal)
	case provider.RefuseRequest:
		if r.X == nil {
			return errorBody("bad_request", "bad request")
		}
		return errorResponse(r.X).Body
	default:
		return errorBody("internal_error", messageInternal)
	}
}
```

Five kinds; one envelope. `provider.RefuseNotFound`, `provider.RefuseMethodNotAllowed`,
`provider.RefuseScenarioUnknown` and `provider.RefuseInternal` arrive with no `Exchange` — the framework refused
the request before any handler ran — and `provider.RefuseRequest` arrives with the `Exchange` that recorded the
findings, so the body it renders is the same body a handler-side rejection renders. `Refusal.X` is nil for the
first four; `ValidateProfile`'s `ErrorBody` subtest calls yours for every kind, with and without an `Exchange`, and
requires a non-empty body each time.

Neither the 401 nor the 404 in that list is produced by the framework. `ErrorBody` renders a body; the *status*
comes from your own findings-to-status map, which is the piece that is easy to leave out and impossible to pass
`ValidateProfile` without:

<!-- excerpt: examples/profile/acme/errors.go#L104-L129 -->
```go
// errorResponse builds the response for a request that failed authentication
// or validation outside a direct [provider.Exchange.Reject] call (checkAuth's
// x.Fail, and selectProjection's scenario.no_matching_turn). It reads the
// findings the handler recorded, so the status and the body text cannot
// drift apart from what the journal says happened — the same discipline
// profiles/tavily/errors.go's own errorResponse follows.
func errorResponse(x *provider.Exchange) provider.Response {
	status := http.StatusBadRequest
	code := "bad_request"
	message := "bad request"

	errs := errorFindings(x)
	switch {
	case containsAny(errs, authCodes):
		status, code = http.StatusUnauthorized, "unauthorized"
	case containsAny(errs, []string{provider.CodeNoMatchingTurn}):
		status, code = http.StatusNotFound, "no_matching_turn"
	case containsAny(errs, []string{CodeProjectionInvalid}):
		status, code = http.StatusInternalServerError, "internal_error"
	}
	if len(errs) > 0 {
		// The first error in Findings order — a total order (severity, then
		// field, then code) — so a request with two problems reports the
		// same one on every run.
		message = errs[0].Message
	}
```

Three things in it are obligations rather than style. An **auth finding must map to 401**, or
`ValidateProfile`'s `MissingCredential` subtest fails — the framework raises no 401 of its own.
**`provider.CodeNoMatchingTurn`** is the exported code for "the scenario declared your provider but no turn
answers this request", and it is a real failure, not an empty 200. And `x.Findings()` returns a **total order**
(severity, then field, then code), so quoting the first error is deterministic — picking any other one, or
iterating a map, reintroduces exactly the flakiness house rule 2 exists to prevent.

`faultBody` is the other half of "in your own envelope": a scripted fault's `body:`/`error:` overrides, rendered
through your error shape rather than a generic one:

<!-- excerpt: examples/profile/acme/errors.go#L159-L180 -->
```go
// faultBody builds the provider-shaped body for a fault attempt — how §2.5's
// rule that "the body is provider-shaped and built by the provider package"
// is honoured without provider knowledge leaking into fault execution.
//
// Returning nil leaves the rendered scenario body in place, which is the
// right answer for a fault with nothing provider-shaped to say (a delay, a
// truncation, a wrong content type).
func faultBody(a scenario.FaultAttempt) []byte {
	if len(a.Body) > 0 {
		if body, err := json.Marshal(a.Body); err == nil {
			return body
		}
		return nil
	}
	if a.Error != "" {
		return errorBody(statusCode(a.Status), a.Error)
	}
	if text := http.StatusText(a.Status); text != "" {
		return errorBody(statusCode(a.Status), text)
	}
	return nil
}
```

`statusCode` there is a lookup into the one table of error codes this vendor publishes. It is worth the four
lines: without it a scripted 429 renders `"code":"429"` while every routing refusal renders a symbol like
`"not_found"`, and one vendor ships two error vocabularies depending on where the refusal came from.

### The `Validator`

<!-- excerpt: examples/profile/acme/handler.go#L117-L133 -->
```go
// validator decodes and checks this package's projection body at startup.
// internal/server (or, out of tree, a consumer's own readiness check) calls
// provider.ValidateScenario before readiness reports true, so a fixture with
// a bad Acme field fails at boot rather than on a consumer's first request.
type validator struct{}

// Routes implements provider.RouteLister, so a `when.route:` in an Acme entry
// is checked against the routes this package actually serves.
func (validator) Routes() []provider.Route { return Routes() }

// ProjectionKeys returns projectionBody's own top-level keys — the
// vocabulary a turn's `respond:` body under the "acme" entry may use.
func (validator) ProjectionKeys() []string {
	return []string{"answer", "confidence", "status", "omit_fields", "extra_fields"}
}

var _ provider.Validator = validator{}
```

`Routes` and `ProjectionKeys` are the small half. The interface's one genuinely required method is
`ValidateProjections`, and it is where every load-time finding the section promises actually comes from:

<!-- excerpt: examples/profile/acme/handler.go#L135-L167 -->
```go
// ValidateProjections decodes every turn's projection body and reports what
// it finds, addressed by the entry's YAML path. It does not mutate the
// scenario, and it is safe to call more than once.
func (validator) ValidateProjections(s *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	if e == nil {
		return nil
	}
	var findings []scenario.Finding
	for i := range e.Turns {
		path := projectionPath(e, i)

		projection := &projectionBody{}
		if err := e.Turns[i].DecodeProjection(e.Name, i, projection); err != nil {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityError,
				Code:     CodeProjectionInvalid,
				Path:     path,
				Message:  err.Error(),
			})
			continue
		}
		findings = append(findings, s.ResolveRefs(path, projection)...)
		if projection.Confidence < 0 || projection.Confidence > 1 {
			findings = append(findings, scenario.Finding{
				Severity: scenario.SeverityWarning,
				Code:     "acme.confidence.range",
				Path:     path + ".confidence",
				Message:  "confidence is documented as a value between 0 and 1",
			})
		}
	}
	return findings
}
```

`provider.ValidateScenario` calls it before readiness — in the composed binary and in `testkit.Start` alike — so
a fixture with a bad Acme field fails at boot rather than on a consumer's first request. **The misspelled-key
protection comes from `DecodeProjection`**, which decodes strictly: a `respond:` key no field of your struct
claims is an unmarshal error, and the error above turns it into a load-time finding. It does not come from
`ProjectionKeys`, which the interface requires but which nothing reads at runtime — in this repository it feeds
one docs cross-check over the four reference profiles (`scenarios/scenarios_test.go`), and out of tree it is
documentation with no enforcement behind it. Keep it accurate anyway: it is how a reader of your package learns
the `respond:` vocabulary, and a future release may well start reading it.

`Routes` — the `provider.RouteLister` interface — is what lets a `when.route:` in one of your entries be checked
against routes you actually serve; a route name that matches none is a load error, not a turn that quietly never
fires.

The struct being decoded is your own, and it is a **YAML** decode, not a JSON one. Reaching for a struct whose
fields carry `json:` tags is the obvious first guess, and it fails half-silently: single-word field names still
fold by name, so a vendor whose projection is all single words appears to work while the tags are ignored, and
only a `snake_case` field fails loudly. The tags are `yaml:`, and the decode is strict:

<!-- excerpt: examples/profile/acme/render.go#L29-L54 -->
```go
// projectionBody is how the shared corpus renders through the Acme API. It
// is the decoded form of a turn's `respond:` body — one struct serving both
// routes, because both are addressed through the one "acme" scenario entry
// (neither Route below sets its own Entry). The reserved envelope keys
// (kind, auth, validation, fault, turns, turn_key) are stripped by the
// scenario loader before this is decoded, so they are deliberately absent
// here.
type projectionBody struct {
	// Answer is a pointer so a scenario can distinguish "this scenario has
	// no answer to give" (nil, renders "") from "the answer is the empty
	// string" (an explicit ""). Read only by handleAnswer.
	Answer *string `yaml:"answer,omitempty"`

	// Confidence projects POST /v1/answer's own optional field. Read only by
	// handleAnswer.
	Confidence float64 `yaml:"confidence,omitempty"`

	// Status projects GET /v1/status's one field, defaulting to "operational"
	// when the scenario leaves it unset. Read only by handleStatus.
	Status string `yaml:"status,omitempty"`

	// OmitFields drops named response fields that would otherwise be
	// present. ExtraFields adds ones that would not.
	OmitFields  []string             `yaml:"omit_fields,omitempty"`
	ExtraFields scenario.ExtraFields `yaml:"extra_fields,omitempty"`
}
```

The reserved envelope keys — `kind`, `auth`, `validation`, `fault`, `turns`, `turn_key` — are stripped by the
scenario loader before your struct sees the node, so it holds only your vendor's vocabulary. The wire types
(`AnswerResponse`, `StatusResponse` in the same file) are separate and carry `json:` tags: one struct describes
what a scenario author writes, the other what a consumer parses, and collapsing them couples your YAML schema to
your wire contract forever.

Nothing in `ValidateProfile` covers any of this, so a Validator arrives untested unless you test it. Acme's
`TestAcmeValidatorReportsBadProjections` is the shape: a wrong-typed field, a key no field claims, a value
outside its documented range, and an unknown `when.route`.

### Streams

Acme has none. A streaming route reuses `provider.Stream`, `provider.EncodeSSE` and the two exported grammars, and
its Validator calls `scenario.ValidateStreamScripts` and `scenario.ValidateStreamFaultMismatch`; nothing is added
to the transport. [`profiles/perplexity`](../profiles/perplexity) (two SSE grammars on one listener) and
[`profiles/mcp`](../profiles/mcp) (a JSON-RPC response as an SSE stream) are the worked examples;
`docs/design/streaming.md` is the design.

## Step 3 — scenarios

Your scenarios are your own YAML files, in your own repository. The schema is `docs/scenario-schema.md`; the
`providers:` block for your listener is keyed by your `Name`, and its `respond:` body uses your `ProjectionKeys`.
Acme's tests carry theirs as constants, which is what lets this guide quote them:

<!-- excerpt: examples/profile/acme/acme_test.go#L24-L32 -->
```yaml
version: 1
name: acme-scenario
providers:
  acme:
    turns:
      - respond:
          answer: "the answer to everything"
          confidence: 0.99
          status: "operational"
```

A scripted fault plan is a `fault:` block on the entry — attempts in order, one per call, addressed to the route's
`FaultKey` through `Route.Fault`. `[{status: 429}, {}]` is "a 429, then whatever the scenario renders":

<!-- excerpt: examples/profile/acme/acme_test.go#L41-L48 -->
```yaml
version: 1
name: acme-fault-scenario
providers:
  acme:
    fault:
      attempts:
        - status: 429
        - {}
```

`when:` predicates and `turn_key:` extractors choose between turns on the request's own fields; the schema document
has the grammar. `testkit.AssertCovers` over the directory holding your corpus is the coverage guard: every file
declares a block for every entry kind you register (`provider.Set` lists them as `EntryKinds`), so a listener that
silently answers the zero projection because a fixture forgot it is a test failure rather than a surprise.

**The built-in scenarios do not cover your profile, and there is no overlay** (owner decision D-7,
`docs/proposals/framework-seam.md`). `builtin:happy` must mean the same bytes in every build, so it declares a
block only for the four reference profiles; a build that loads a built-in with your profile registered gets one
`scenario.profile.unscripted` warning at startup naming the profile with no block, and your listener renders its
zero projection. Ship your own files.

## Step 4 — tests

The tests an adopter writes are the tests Acme has. `testkit.Start` with `testkit.WithProfiles` — required, not
defaulted, so a team simulating one vendor never pulls four other vendors' contracts and goldens into its build
graph — starts one in-process server per profile with nothing to defer, and every assertion reads the journal, so a
test proves the request was *correct*, not merely answered:

<!-- excerpt: examples/profile/acme/acme_test.go#L96-L139 -->
```go
// TestAcmeAnswerServesTheScriptedTurn proves a correct request from the
// journal: the response decodes to the scripted fields, and the journal
// entry records exactly one request with no error findings.
func TestAcmeAnswerServesTheScriptedTurn(t *testing.T) {
	sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(plainScenarioYAML))

	req, err := http.NewRequest(http.MethodPost, sim.URL(acme.Name)+"/v1/answer",
		strings.NewReader(`{"query":"what is the answer?"}`))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authed(t, req)

	resp, err := sim.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/answer: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}

	var decoded acme.AnswerResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	if decoded.Answer != "the answer to everything" {
		t.Fatalf("answer = %q, want the scripted turn's answer", decoded.Answer)
	}
	if decoded.Confidence != 0.99 {
		t.Fatalf("confidence = %v, want 0.99", decoded.Confidence)
	}
	if decoded.RequestID == "" {
		t.Fatal("request_id must be populated: it is the route-derived identifier DerivedIDs names")
	}

	entries := sim.AwaitRequests(t, acme.Name, 1)
	testkit.AssertNoErrors(t, entries[0])
}
```

The rest of `acme_test.go` is the checklist: an invalid request rejected in the vendor's own shape and journaled
under the exported code (`testkit.AssertFindings`); a missing credential answering 401 under `DefaultAuth`; every
other auth mode a scenario can declare; the scripted `429` then `200` with no `fault.unknown_key` in either
journal entry; each route's fault cursor proved independent; the Validator's load-time findings; two namespaces
isolated (`testkit.AssertNamespacesIsolated`); a golden compare pruned by
`testkit.GoldenDerivedIDs("request_id")`, which first proves the compare *fails* without it.

One of them deserves naming separately, because skipping it is invisible. `TestAcmeGoldensMatchTheWire` drives
**every** committed golden from a live response and compares it — the three successes and all four error shapes.
Nothing else does: `Conform` checks provenance and coverage, not content, so without this test the contract
bundle and the handlers drift apart with every gate still green. It is the habit the four reference profiles
have (`profiles/exa/render_test.go`'s `assertGoldenWire` is the in-tree shape) and the one most easily lost when
a template is copied.

And this one, which is house rule 4 with no redaction code of Acme's own:

<!-- excerpt: examples/profile/acme/acme_test.go#L338-L371 -->
```go
// TestAcmeCredentialNeverReachesTheJournalRaw covers house rule 4 with no
// redaction code of Acme's own: a request presenting Authorization and
// x-acme-key both must never carry either raw value into the journal, in
// either the per-request Entry or the admin-equivalent sim.Journal() view.
func TestAcmeCredentialNeverReachesTheJournalRaw(t *testing.T) {
	sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(plainScenarioYAML))

	const rawSecret = "acme-do-not-leak-this-value"
	req, err := http.NewRequest(http.MethodPost, sim.URL(acme.Name)+"/v1/answer",
		strings.NewReader(`{"query":"anything"}`))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawSecret)
	req.Header.Set("X-Acme-Key", rawSecret)

	resp, err := sim.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/answer: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	testkit.AssertNoCredentialLeak(t, sim, rawSecret)

	entries := sim.AwaitRequests(t, acme.Name, 1)
	if got := http.Header(entries[0].Headers).Get("X-Acme-Key"); got != "[REDACTED]" {
		t.Fatalf("journal X-Acme-Key header = %q, want [REDACTED]", got)
	}
}
```

Two guards run over the whole module rather than one request. `testkit.AssertNoLiveHosts`, seeded from the
composed set's own `Hosts`, scans every file — Go source included, because a base-URL default lives in Go source,
not in a fixture — with your contracts directory skipped, since its provenance record legitimately names the
vendor's real documentation URL:

<!-- excerpt: examples/profile/acme/acme_test.go#L373-L386 -->
```go
// TestModuleHasNoLiveHosts is the two-line idiom testkit.AssertNoLiveHosts
// documents, run against this whole module (Go sets a test binary's working
// directory to the package under test, so ".." from acme/ is the module
// root — Go source included, matching scripts/lint-no-live-hosts.sh's own
// SEARCH_PATHS), with acme's own contracts bundle skipped, on the same terms
// this repository's own in-tree guard skips each reference profile's
// contracts/.
func TestModuleHasNoLiveHosts(t *testing.T) {
	set, err := provider.NewSet(acme.Profile())
	if err != nil {
		t.Fatalf("provider.NewSet(acme.Profile()) refused: %v", err)
	}
	testkit.AssertNoLiveHosts(t, os.DirFS(".."), []string{"acme/contracts"}, set.LiveHosts()...)
}
```

And the conformance suite, which is what "profile" means out of tree:

<!-- excerpt: examples/profile/acme/conformance_test.go#L11-L21 -->
```go
// TestValidateProfile runs the same conformance suite each in-tree reference
// profile's own profiles/<name>/conformance_test.go calls
// (docs/proposals/framework-seam.md, "the eleven CONTRIBUTING.md
// conventions"), proving it runs against an out-of-tree profile with no
// modification: NewSet, Contracts (contracts.Conform over acme's own
// embedded bundle), ErrorBody, UnknownPath, WrongMethod, WrongContentType,
// MissingCredential, FaultKeysResolve, RejectionDoesNotClaimAnAttempt,
// Deterministic and RenderShape.
func TestValidateProfile(t *testing.T) {
	testkit.ValidateProfile(t, acme.Profile())
}
```

`ValidateProfile` runs, as named subtests: `NewSet` (registration completeness); `Contracts` (`contracts.Conform`
over your bundle — every golden has a complete provenance record, the coverage minimum in step 1 is met, nothing
but contract data is in the directory, the `spec:` block is well-formed if present); `ErrorBody` for every kind;
`UnknownPath` (your 404 shape plus `route.unmatched`); `WrongMethod` (405 with `Allow`); `WrongContentType` (a
finding whose code contains `content_type` or `content-type`); `MissingCredential` (401 for a request with no
credential at all, under strict auth); `FaultKeysResolve` (every route's key known to the engine);
`RejectionDoesNotClaimAnAttempt`; `Deterministic`; `RenderShape`. It is one call. It is also the bar: a package
that does not pass it is a Go program that answers HTTP, not a profile.

It is a floor, not a ceiling, and it is worth being precise about the gap — this is the list of things a profile
can get completely wrong while every subtest passes:

- **Every scenario auth mode except "no credential at all".** `MissingCredential` covers one case.
  `mode: reject`, `mode: optional`, `expect_key` and an `auth.headers` placement override are all yours.
- **The wire shapes themselves.** `Conform` cannot check a shape it has never heard of (`contracts/doc.go` says
  so): it validates provenance and coverage, never a golden's content. Your own golden-versus-live tests are the
  only thing pinning the vendor's field names and JSON types.
- **Your Validator.** No subtest loads a scenario of yours, so every load-time finding is unproven until you
  prove it.
- **`ObserveCredential` for a credential the shared header scan cannot see** — a body property, or a header your
  vendor invented. Without it the journal records `auth.present` false for a request you authenticated and
  served, and a consumer's adapter test reads that as "I sent no credential".

## Step 5 — compose a binary and an image

<!-- excerpt: examples/profile/main.go#L26-L31 -->
```go
func main() {
	os.Exit(servicesim.Main(
		servicesim.Build{Program: "acmesim", Version: version},
		provider.MustSet(acme.Profile(), exa.Profile()),
	))
}
```

`servicesim.Main` is the composition root `cmd/servicesim` itself uses; the only difference between this binary
and the shipped one is the list of profiles handed to `provider.MustSet`. Composing one of the reference profiles
alongside your own is ordinary, not a special case. The binary derives its flags, its usage text, its `--providers`
default and its readiness from the set — there is nothing to register.

Three flags exist for the surfaces around the binary. `--print-ports` prints every listener's configured port as
JSON — the input to your `Dockerfile`'s `EXPOSE`, your compose file's port map, and the `*_BASE_URL` rows in your
own README. `--print-routes` prints every registered `METHOD /path` — your documentation's listener table, and what a
docs guard like this repository's `scripts/check-docs.sh` reads to check it. `--print-hosts` prints every
registered profile's `Hosts` — the seed for a live-host guard run outside Go, the way `scripts/lint-no-live-hosts.sh`
is. [`examples/profile/Dockerfile`](../examples/profile/Dockerfile) is the two-stage build over that binary, in the
shape of this repository's own; its smoke probes are yours to write, as ours are (`scripts/image-smoke.sh`).

## Step 6 — what the framework does not do for you

- **Admin routes.** `/healthz`, `/readyz`, `/__admin/requests` and the rest are the framework's and are closed to
  composition (owner decision D-3, `docs/proposals/framework-seam.md`): a profile gets no admin route of its own,
  because a mutable admin API is hidden shared state between concurrent tests. The journal is yours to read; it is
  not yours to extend.
- **Built-in scenarios.** Reference-only, as step 3 says. Your corpus is your own, and `AssertCovers` is how you
  keep it complete.
- **A default profile set.** `testkit.WithProfiles` is required; there is no "all of them".
- **The turn model's current limits.** A turn key that counts array elements matching a predicate, a substring
  predicate folded over an array of messages, and a response that projects a field from the request's own body are
  not expressible today (`docs/proposals/framework-seam.md`, risk 7). If your vendor needs one, it is a design
  question here, not a workaround there — record it against the backlog rather than encoding it in a handler.
- **Ranking, relevance, realistic answers.** Out of scope for any profile, yours included (`CLAUDE.md`, "What
  Servicesim is not"). The value is determinism.

And separately from what the framework declines to do, these are the things it leaves entirely to you with
**nothing checking them** — the list worth re-reading before you call a profile finished:

- Every scenario auth mode past "no credential at all" (see "Authentication is yours").
- Your vendor's actual wire shapes: `Conform` cannot check a shape it has not heard of, so the golden-versus-live
  comparison in step 4 is the only pin.
- Your Validator's load-time findings.
- `provider.Exchange.ObserveCredential` for a credential no header scan can find.
- Whether the `note:` on each golden says something true about what that fixture is load-bearing for. A guard can
  require the field; only a person can require that it means anything.

## The four reference profiles, as worked examples

If your vendor is the common case — one or two routes, a header credential, no streaming — read
[`profiles/exa`](../profiles/exa)'s search path and [`profiles/mcp`](../profiles/mcp) first: a flat error
envelope and a single-route dispatch are the two shapes most first profiles need. The table is not ordered by
difficulty; pick by what your vendor looks like.

| Profile | Read it for |
|---|---|
| [`profiles/exa`](../profiles/exa) | Multi-route with a flat error envelope and one documented exception to it (the reduced 429 body); the corrections its `doc.go` restates because they are easy to get wrong from memory. |
| [`profiles/mcp`](../profiles/mcp) | A protocol rather than a vendor: one route, JSON-RPC dispatch on the body, a non-JSON content type, a JSON-RPC refusal envelope, an SSE response to a POST. |
| [`profiles/tavily`](../profiles/tavily) | One listener, several routes; a body-placed credential as a second accepted placement (`request.go`'s `checkAuth`); a separate scenario entry per route (`Route.Entry`); the async create-then-poll surface (`research.go`). |
| [`profiles/perplexity`](../profiles/perplexity) | Two scenario entries (Sonar and Agent) on one listener; two SSE grammars; SDK-alias routes sharing a fault key. |

Each has a `doc.go` that is a decision log — what is simulated, what is not, and every simulator-chosen default —
and a `contracts/README.md` in the shape step 1 asks for. `CONTRIBUTING.md`'s "Adding a provider" is the checklist
for adding a *reference* profile to this repository; this guide is the one for a profile in yours.

## The 1.0 trigger

The exported seam is pre-1.0 and says so. The condition under which it stops being — recorded in
`docs/proposals/framework-seam.md` ("Compatibility and versioning") and carried into `README.md` by the docs sweep
that closes Phase 10 — is: at least one profile written by someone who has not read this repository has shipped and
survived a framework minor release without source changes, and the `chat/completions` profile has landed. Until
then a `v0.x` pin means the seam may still move, and a profile written today should expect to follow a release note
once.

## Keeping your contract honest

There is no live contract canary. Drift detection is a dated re-verification: hash-check the `spec:` block, re-read
the consumed fields against the vendor's current documentation, update `provenance.yaml`'s dates and the golden
that changed, and record what moved. [`contracts/README.md`](../contracts/README.md), "Keeping them honest", is the
procedure; ADR 0002 is why it outranks anything else you have written down about the vendor, including this
guide's counterpart in your own repository.
