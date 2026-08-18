package acme_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.test/acmesim/acme"

	"github.com/c360studio/servicesim/contracts"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/testkit"
)

// plainScenarioYAML is the fixture the byte-identity, redaction and golden
// tests below share: one answer turn with no fault: block, so every request
// against it is unconditionally the scripted response.
const plainScenarioYAML = `
version: 1
name: acme-scenario
providers:
  acme:
    turns:
      - respond:
          answer: "the answer to everything"
          confidence: 0.99
          status: "operational"
`

// faultScenarioYAML scripts a 429 then a bare "- {}" (no fault; the rendered
// scenario response) — the sequence testkit.ValidateProfile's own
// FaultKeysResolve subtest proves is even reachable for an out-of-tree
// profile (AUDIT 1's ranked #1 blocker: "an out-of-tree route is
// fault-blind").
const faultScenarioYAML = `
version: 1
name: acme-fault-scenario
providers:
  acme:
    fault:
      attempts:
        - status: 429
        - {}
`

// goldenScenarioYAML scripts exactly the bodies acme/contracts' committed
// success goldens hold, so TestAcmeGoldensMatchTheWire can drive each one from
// a live response instead of hand-maintaining the bundle.
const goldenScenarioYAML = `
version: 1
name: acme-golden-scenario
providers:
  acme:
    turns:
      - when:
          route: answer
        respond:
          answer: "a fictional answer, for Servicesim's own out-of-tree proof"
          confidence: 0.87
      - when:
          route: status
        respond:
          status: "operational"
`

// goldenEmptyScenarioYAML is the empty-result case acme-answer-empty.json
// records: an explicit empty answer and no confidence, so provider.Render's
// omitempty drops the field rather than rendering a zero.
const goldenEmptyScenarioYAML = `
version: 1
name: acme-golden-empty-scenario
providers:
  acme:
    turns:
      - respond:
          answer: ""
`

// bearerToken is the credential every test below presents. It carries no
// vendor-shaped prefix (no "sk-", "tvly-", "pplx-") on purpose: house rule 4
// applies to any credential shape, not only ones the four reference profiles
// happen to use.
const bearerToken = "Bearer acme-test-token"

func authed(t *testing.T, req *http.Request) *http.Request {
	t.Helper()
	req.Header.Set("Authorization", bearerToken)
	return req
}

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

// TestAcmeRejectsAMissingRequiredFieldInItsOwnShape covers "an invalid
// request is rejected in the vendor's own shape": a query-less body answers
// 400 in Acme's {"error":{...}} envelope and journals acme.query.missing.
func TestAcmeRejectsAMissingRequiredFieldInItsOwnShape(t *testing.T) {
	sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(plainScenarioYAML))

	req, err := http.NewRequest(http.MethodPost, sim.URL(acme.Name)+"/v1/answer", strings.NewReader(`{}`))
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
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.StatusCode, body)
	}

	var decoded struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	if decoded.Error.Code != "bad_request" {
		t.Fatalf("error.code = %q, want %q", decoded.Error.Code, "bad_request")
	}

	entries := sim.AwaitRequests(t, acme.Name, 1)
	testkit.AssertFindings(t, entries[0], acme.CodeQueryMissing)
}

// TestAcmeMissingCredentialAnswers401 covers Profile.DefaultAuth: required —
// a request with no Authorization at all is refused before it ever reaches
// turn selection, in Acme's own shape.
func TestAcmeMissingCredentialAnswers401(t *testing.T) {
	sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(plainScenarioYAML))

	req, err := http.NewRequest(http.MethodGet, sim.URL(acme.Name)+"/v1/status", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := sim.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /v1/status: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", resp.StatusCode, body)
	}

	entries := sim.AwaitRequests(t, acme.Name, 1)
	testkit.AssertFindings(t, entries[0], acme.CodeAuthMissing)
}

// TestAcmeScriptedFaultAppliesWithNoUnknownKey proves AUDIT 1's ranked #1
// blocker cannot recur here: a scripted [{status: 429}, {}] answers 429 then
// 200, and neither entry carries fault.unknown_key — the route's own
// FaultKey (acme:answer) was registered with the Set's fault engine, exactly
// as testkit.ValidateProfile's FaultKeysResolve subtest already proves in
// general.
func TestAcmeScriptedFaultAppliesWithNoUnknownKey(t *testing.T) {
	sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(faultScenarioYAML))
	client := sim.Client()

	post := func() int {
		req, err := http.NewRequest(http.MethodPost, sim.URL(acme.Name)+"/v1/answer",
			strings.NewReader(`{"query":"anything"}`))
		if err != nil {
			t.Fatalf("building the request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		authed(t, req)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /v1/answer: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	status1 := post()
	status2 := post()

	if status1 != http.StatusTooManyRequests {
		t.Fatalf("attempt 0: status = %d, want 429 (the first scripted attempt)", status1)
	}
	if status2 != http.StatusOK {
		t.Fatalf("attempt 1: status = %d, want 200 (the second scripted attempt, a bare '- {}')", status2)
	}

	entries := sim.AwaitRequests(t, acme.Name, 2)
	for _, e := range entries {
		if hasFinding(e, provider.CodeUnknownFaultKey) {
			t.Fatalf("entry seq %d carries fault.unknown_key: the fault engine did not know this route", e.Seq)
		}
	}
}

// TestAcmeNamespacesAreIsolated proves two callers naming different
// namespaces draw from independent journals and independent fault cursors —
// testkit.AssertNamespacesIsolated is the library half of this claim; this
// proves it against an out-of-tree profile.
func TestAcmeNamespacesAreIsolated(t *testing.T) {
	sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(plainScenarioYAML))

	nsA := sim.Namespace(t, "tenant-a")
	nsB := sim.Namespace(t, "tenant-b")

	for _, ns := range []*testkit.Namespace{nsA, nsB} {
		req, err := http.NewRequest(http.MethodPost, ns.URL(acme.Name)+"/v1/answer",
			strings.NewReader(`{"query":"anything"}`))
		if err != nil {
			t.Fatalf("building the request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		authed(t, req)
		resp, err := ns.Client().Do(req)
		if err != nil {
			t.Fatalf("POST /v1/answer: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("namespace %s: status = %d, want 200", ns.Name(), resp.StatusCode)
		}
	}

	entriesA := nsA.AwaitRequests(t, acme.Name, 1)
	entriesB := nsB.AwaitRequests(t, acme.Name, 1)
	testkit.AssertNamespacesIsolated(t, nsA, nsB)
	if entriesA[0].Namespace == entriesB[0].Namespace {
		t.Fatalf("both entries share namespace %q, want distinct", entriesA[0].Namespace)
	}
}

// TestAcmeGoldenPruningIsCallerDeclared proves testkit.GoldenDerivedIDs is
// what makes a golden compare stable against Acme's own derived request_id:
// a golden holding a deliberately WRONG request_id fails an ordinary
// AssertGoldenJSON compare, and passes once GoldenDerivedIDs("request_id")
// names the field.
func TestAcmeGoldenPruningIsCallerDeclared(t *testing.T) {
	sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(plainScenarioYAML))

	req, err := http.NewRequest(http.MethodPost, sim.URL(acme.Name)+"/v1/answer",
		strings.NewReader(`{"query":"anything"}`))
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

	path := filepath.Join(t.TempDir(), "acme-answer.json")
	stale := `{"request_id":"00000000000000000000000000000000","answer":"the answer to everything","confidence":0.99}`
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatalf("writing the golden fixture: %v", err)
	}

	stub := &fatalRecorder{}
	testkit.AssertGoldenJSON(stub, path, body)
	if !stub.failed {
		t.Fatal("comparing request_id exactly must fail against a deliberately mismatched golden")
	}

	testkit.AssertGoldenJSON(t, path, body, testkit.GoldenDerivedIDs("request_id"))

	derived, streamDerived := sim.DerivedIDs()
	if len(derived) != 1 || derived[0] != "request_id" {
		t.Fatalf("sim.DerivedIDs() = %v, %v; want []string{\"request_id\"} and no stream paths", derived, streamDerived)
	}
}

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

// TestAcmeGoldensMatchTheWire drives every committed golden from a live
// response, which is what keeps the bundle from drifting: contracts.Conform
// checks that each golden is valid JSON with a complete provenance record, and
// nothing else — it never looks at the CONTENT. A bundle nothing compares
// against is documentation that stops being true silently, and it is the one
// habit an adopter copying this package twenty times most needs to copy
// (profiles/exa/render_test.go's assertGoldenWire is the in-tree equivalent).
//
// Comparison goes through testkit.AssertGoldenJSON with
// testkit.GoldenDerivedIDs("request_id") — the same option Profile.DerivedIDs
// exists to feed — so the derived identifier is pruned rather than pinned.
func TestAcmeGoldensMatchTheWire(t *testing.T) {
	tests := []struct {
		name     string
		golden   string
		scenario string
		request  func(t *testing.T, sim *testkit.Sim) *http.Request
		status   int
	}{
		{
			name:     "answer happy",
			golden:   "acme-answer-happy.json",
			scenario: goldenScenarioYAML,
			request:  postAnswer(`{"query":"what is the answer?"}`, true),
			status:   http.StatusOK,
		},
		{
			name:     "answer empty",
			golden:   "acme-answer-empty.json",
			scenario: goldenEmptyScenarioYAML,
			request:  postAnswer(`{"query":"anything"}`, true),
			status:   http.StatusOK,
		},
		{
			name:     "status happy",
			golden:   "acme-status-happy.json",
			scenario: goldenScenarioYAML,
			request: func(t *testing.T, sim *testkit.Sim) *http.Request {
				t.Helper()
				req := newRequest(t, http.MethodGet, sim.URL(acme.Name)+"/v1/status", "")
				return authed(t, req)
			},
			status: http.StatusOK,
		},
		{
			name:     "400, the documented required field is absent",
			golden:   "acme-error-400.json",
			scenario: goldenScenarioYAML,
			request:  postAnswer(`{}`, true),
			status:   http.StatusBadRequest,
		},
		{
			name:     "401, no credential at all",
			golden:   "acme-error-401.json",
			scenario: goldenScenarioYAML,
			request:  postAnswer(`{"query":"anything"}`, false),
			status:   http.StatusUnauthorized,
		},
		{
			name:     "404, an unmatched path",
			golden:   "acme-error-404.json",
			scenario: goldenScenarioYAML,
			request: func(t *testing.T, sim *testkit.Sim) *http.Request {
				t.Helper()
				return authed(t, newRequest(t, http.MethodGet, sim.URL(acme.Name)+"/v1/nope", ""))
			},
			status: http.StatusNotFound,
		},
		{
			name:     "405, a known path with an unsupported method",
			golden:   "acme-error-405.json",
			scenario: goldenScenarioYAML,
			request: func(t *testing.T, sim *testkit.Sim) *http.Request {
				t.Helper()
				return authed(t, newRequest(t, http.MethodGet, sim.URL(acme.Name)+"/v1/answer", ""))
			},
			status: http.StatusMethodNotAllowed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(tc.scenario))

			resp, err := sim.Client().Do(tc.request(t, sim))
			if err != nil {
				t.Fatalf("issuing the request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading the response body: %v", err)
			}
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d; body = %s", resp.StatusCode, tc.status, body)
			}

			testkit.AssertGoldenJSON(t, goldenOnDisk(t, tc.golden), body, testkit.GoldenDerivedIDs("request_id"))
		})
	}
}

// goldenOnDisk materialises one golden out of the profile's own EMBEDDED
// bundle — Profile().Contracts, the bytes a consumer actually ships — so this
// test cannot pass against a stale embed while the working tree looks right.
// testkit.AssertGoldenJSON takes a path, so the bytes are written to the test's
// temp dir on the way through.
func goldenOnDisk(t *testing.T, name string) string {
	t.Helper()

	raw, err := contracts.Read(acme.Profile().Contracts, name)
	if err != nil {
		t.Fatalf("reading %s out of the embedded bundle: %v", name, err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

// TestAcmeValidatorReportsBadProjections covers the Validator, which
// testkit.ValidateProfile does NOT: its subtests never load a scenario of
// yours, so every load-time finding your ValidateProjections produces is
// yours to prove. Note where the protections actually come from — the
// unknown-key finding is yaml.v3's strict decode inside DecodeProjection, not
// ProjectionKeys, which out of tree is read by nothing at runtime.
func TestAcmeValidatorReportsBadProjections(t *testing.T) {
	tests := []struct {
		name     string
		respond  string
		wantCode string
		wantErr  bool
	}{
		{
			name:     "every key ProjectionKeys documents decodes",
			respond:  "          answer: \"a\"\n          confidence: 0.5\n          status: \"operational\"\n          omit_fields: [confidence]\n          extra_fields: {surprise: 1}\n",
			wantCode: "",
		},
		{
			name:     "a wrong-typed field",
			respond:  "          answer: [1, 2]\n",
			wantCode: acme.CodeProjectionInvalid,
			wantErr:  true,
		},
		{
			name:     "a key no projection field claims",
			respond:  "          bogus: 1\n",
			wantCode: acme.CodeProjectionInvalid,
			wantErr:  true,
		},
		{
			name:     "a confidence outside the documented range",
			respond:  "          confidence: 7\n",
			wantCode: "acme.confidence.range",
		},
	}

	// The first case above exercises every key ProjectionKeys() reports, so
	// pin the two together: a key added to ProjectionKeys without a case
	// covering it, or one removed while the case still names it, fails here.
	// The reverse direction — a projection struct field ProjectionKeys forgot
	// — needs reflection over the unexported struct, which is why
	// profiles/perplexity/agent_test.go's own equivalent lives IN the package.
	documented := acme.Profile().Validators[string(acme.Name)].ProjectionKeys()
	for _, key := range documented {
		if !strings.Contains(tests[0].respond, key+":") {
			t.Fatalf("ProjectionKeys() reports %q, which the every-key case does not exercise", key)
		}
	}
	if len(documented) != 5 {
		t.Fatalf("ProjectionKeys() = %v; the every-key case covers 5 keys", documented)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := "version: 1\nname: acme-validator-scenario\nproviders:\n  acme:\n    turns:\n      - respond:\n" + tc.respond
			s, _, err := scenario.Parse([]byte(src))
			if err != nil {
				t.Fatalf("the scenario must parse — this test is about the PROJECTION validator: %v", err)
			}

			findings := provider.ValidateScenario(s, acme.Profile().Validators)
			if tc.wantCode == "" {
				if len(findings) != 0 {
					t.Fatalf("findings = %+v, want none", findings)
				}
				return
			}
			if !hasScenarioFinding(findings, tc.wantCode) {
				t.Fatalf("findings = %+v, want one carrying %q", findings, tc.wantCode)
			}
			if got := hasScenarioError(findings); got != tc.wantErr {
				t.Fatalf("an error-severity finding present = %v, want %v (%+v)", got, tc.wantErr, findings)
			}
		})
	}
}

// TestAcmeValidatorRejectsAnUnknownRoute proves the provider.RouteLister half
// of the Validator: a `when.route:` naming a route this package does not serve
// is a load-time error, not a turn that quietly never fires.
func TestAcmeValidatorRejectsAnUnknownRoute(t *testing.T) {
	const src = `
version: 1
name: acme-unknown-route-scenario
providers:
  acme:
    turns:
      - when:
          route: nope
        respond:
          answer: "a"
`
	s, _, err := scenario.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parsing the scenario: %v", err)
	}
	findings := provider.ValidateScenario(s, acme.Profile().Validators)
	if !hasScenarioError(findings) {
		t.Fatalf("findings = %+v, want an error naming the unknown route", findings)
	}
}

// TestAcmeFaultCursorsAreIndependentPerRoute pins what distinct FaultKeys
// actually buy, which is easy to state wrongly: both routes read the ONE
// providers.acme.fault plan, and each keeps its own cursor into it. So the
// first request to each route is that route's attempt 0 — the 429 — and
// /v1/answer consuming it does not advance /v1/status.
func TestAcmeFaultCursorsAreIndependentPerRoute(t *testing.T) {
	sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(faultScenarioYAML))

	answer := func() int {
		return statusOf(t, sim, authed(t, jsonRequest(t, http.MethodPost,
			sim.URL(acme.Name)+"/v1/answer", `{"query":"anything"}`)))
	}
	status := func() int {
		return statusOf(t, sim, authed(t, newRequest(t, http.MethodGet, sim.URL(acme.Name)+"/v1/status", "")))
	}

	if got := answer(); got != http.StatusTooManyRequests {
		t.Fatalf("answer attempt 0: status = %d, want 429", got)
	}
	if got := answer(); got != http.StatusOK {
		t.Fatalf("answer attempt 1: status = %d, want 200", got)
	}
	if got := status(); got != http.StatusTooManyRequests {
		t.Fatalf("status attempt 0: status = %d, want 429 — /v1/status keeps its own cursor into the "+
			"one scripted plan, so /v1/answer's two calls must not have advanced it", got)
	}
}

// TestAcmeAuthPolicyModes covers the scenario auth surface testkit's
// MissingCredential subtest does not reach: it proves only the
// no-credential-at-all 401. Everything below — reject, expect_key, optional,
// and the auth.headers placement override — is the handler's own work, and
// therefore the profile author's own test.
func TestAcmeAuthPolicyModes(t *testing.T) {
	scenarioWithAuth := func(block string) string {
		return "version: 1\nname: acme-auth-scenario\nproviders:\n  acme:\n" + block +
			"    turns:\n      - respond:\n          answer: \"a\"\n"
	}

	tests := []struct {
		name       string
		scenario   string
		authHeader string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "mode reject refuses even a well-formed credential",
			scenario:   scenarioWithAuth("    auth:\n      mode: reject\n"),
			authHeader: bearerToken,
			wantStatus: http.StatusUnauthorized,
			wantCode:   acme.CodeAuthMismatch,
		},
		{
			name:       "mode optional serves a request with no credential",
			scenario:   scenarioWithAuth("    auth:\n      mode: optional\n"),
			wantStatus: http.StatusOK,
		},
		{
			name:       "expect_key refuses a credential that is not the expected one",
			scenario:   scenarioWithAuth("    auth:\n      expect_key: the-expected-key\n"),
			authHeader: bearerToken,
			wantStatus: http.StatusUnauthorized,
			wantCode:   acme.CodeAuthMismatch,
		},
		{
			name:       "expect_key accepts the expected one",
			scenario:   scenarioWithAuth("    auth:\n      expect_key: the-expected-key\n"),
			authHeader: "Bearer the-expected-key",
			wantStatus: http.StatusOK,
		},
		{
			name:       "a non-Bearer scheme still authenticates, and says so",
			scenario:   scenarioWithAuth(""),
			authHeader: "Basic YWNtZTphY21l",
			wantStatus: http.StatusOK,
			wantCode:   acme.CodeAuthWrongScheme,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(tc.scenario))

			req := jsonRequest(t, http.MethodPost, sim.URL(acme.Name)+"/v1/answer", `{"query":"anything"}`)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			if got := statusOf(t, sim, req); got != tc.wantStatus {
				t.Fatalf("status = %d, want %d", got, tc.wantStatus)
			}

			entries := sim.AwaitRequests(t, acme.Name, 1)
			if tc.wantCode == "" {
				return
			}
			if !hasFinding(entries[0], tc.wantCode) {
				t.Fatalf("findings = %+v, want one carrying %q", entries[0].Findings, tc.wantCode)
			}
		})
	}
}

// TestAcmeUnacceptedCredentialHeaderSaysWhatDoes proves house rule 5's strict
// half for placement: x-acme-key is declared in Profile.CredentialNames so the
// journal redacts it, which is not the same as accepting it. A request
// carrying only that header is still a 401, but it journals WHICH header
// would have worked rather than leaving the caller to guess.
func TestAcmeUnacceptedCredentialHeaderSaysWhatDoes(t *testing.T) {
	sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(plainScenarioYAML))

	req := jsonRequest(t, http.MethodPost, sim.URL(acme.Name)+"/v1/answer", `{"query":"anything"}`)
	req.Header.Set("X-Acme-Key", "acme-do-not-leak-this-value")
	if got := statusOf(t, sim, req); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}

	entries := sim.AwaitRequests(t, acme.Name, 1)
	for _, code := range []string{acme.CodeAuthMissing, acme.CodeAuthWrongHeader} {
		if !hasFinding(entries[0], code) {
			t.Fatalf("findings = %+v, want one carrying %q", entries[0].Findings, code)
		}
	}
}

// TestAcmeDemotedFindingRendersNormally proves the handler honours a
// scenario's validation.demote, which is the reason handleAnswer raises its
// required-field finding with x.Fail and gates on x.Failed() rather than
// returning x.Reject's status directly: Reject answers whatever status it was
// handed, so a demoted code would be refused on the wire while the journal
// recorded it as a warning.
func TestAcmeDemotedFindingRendersNormally(t *testing.T) {
	const src = `
version: 1
name: acme-demote-scenario
providers:
  acme:
    validation:
      demote: [acme.query.missing]
    turns:
      - respond:
          answer: "the answer to everything"
`
	sim := testkit.Start(t, testkit.WithProfiles(acme.Profile()), testkit.WithScenarioYAML(src))

	req := authed(t, jsonRequest(t, http.MethodPost, sim.URL(acme.Name)+"/v1/answer", `{}`))
	if got := statusOf(t, sim, req); got != http.StatusOK {
		t.Fatalf("status = %d, want 200: the scenario demoted %s to a warning", got, acme.CodeQueryMissing)
	}

	entries := sim.AwaitRequests(t, acme.Name, 1)
	if !hasFinding(entries[0], acme.CodeQueryMissing) {
		t.Fatalf("findings = %+v, want the demoted finding still recorded", entries[0].Findings)
	}
}

// newRequest builds a request, failing the test rather than returning an error
// no caller here can do anything useful with.
func newRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, url, err)
	}
	return req
}

// jsonRequest is newRequest with the Content-Type Acme documents.
func jsonRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	req := newRequest(t, method, url, body)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// postAnswer builds the table-driven POST /v1/answer request the golden test
// uses, optionally credentialled.
func postAnswer(body string, credentialled bool) func(t *testing.T, sim *testkit.Sim) *http.Request {
	return func(t *testing.T, sim *testkit.Sim) *http.Request {
		t.Helper()
		req := jsonRequest(t, http.MethodPost, sim.URL(acme.Name)+"/v1/answer", body)
		if credentialled {
			authed(t, req)
		}
		return req
	}
}

// statusOf issues a request and drains its body, returning the status.
func statusOf(t *testing.T, sim *testkit.Sim, req *http.Request) int {
	t.Helper()
	resp, err := sim.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// fatalRecorder is a minimal testing.TB double that records a failure without
// needing testkit's own internal stub. Every method the double could be asked
// for is implemented rather than inherited from an embedded nil TB: relying on
// "AssertGoldenJSON only ever calls Errorf" makes this test's failure mode a
// nil-pointer panic the day testkit adds a Fatalf, and testkit's contract
// never promised otherwise.
type fatalRecorder struct {
	testing.TB
	failed bool
}

func (f *fatalRecorder) Helper() {}

func (f *fatalRecorder) Name() string { return "fatalRecorder" }

func (f *fatalRecorder) Errorf(string, ...any) { f.failed = true }

func (f *fatalRecorder) Error(...any) { f.failed = true }

func (f *fatalRecorder) Log(...any) {}

func (f *fatalRecorder) Logf(string, ...any) {}

// Fatalf records the failure and returns. It cannot stop the calling
// goroutine the way testing.T's does — runtime.Goexit here would tear down the
// real test — so a testkit helper that starts calling Fatalf would run on past
// this point. That is a deliberate, bounded compromise for a double used by
// exactly one assertion.
func (f *fatalRecorder) Fatalf(string, ...any) { f.failed = true }

func (f *fatalRecorder) Fatal(...any) { f.failed = true }

func hasScenarioFinding(findings []scenario.Finding, code string) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func hasScenarioError(findings []scenario.Finding) bool {
	for _, f := range findings {
		if f.Severity == scenario.SeverityError {
			return true
		}
	}
	return false
}

func hasFinding(e testkit.Entry, code string) bool {
	for _, f := range e.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}
