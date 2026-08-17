package testkit_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/c360studio/servicesim/profiles"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/testkit"
)

// fixtureAnswer and fixtureError are fixtureProfile's own wire shapes — a
// minimal, otherwise-conformant registration record this file mutates one
// property at a time, so each TestValidateProfileFails… test proves
// ValidateProfile fails for exactly the reason its name says, with nothing
// else broken to confuse the signal.
type fixtureAnswer struct {
	Answer string `json:"answer"`
}

type fixtureError struct {
	Error string `json:"error"`
}

// fixtureContracts is a small, otherwise-well-formed contract bundle
// satisfying contracts.Conform on its own: one happy golden, one empty
// result, and two error goldens, each with a complete provenance record.
// Every test below that does not deliberately break the contract starts
// from a fresh copy of this map.
func fixtureContracts() map[string]string {
	return map[string]string{
		"fixture-happy.json": `{"answer":"ok"}`,
		"fixture-empty.json": `{"answer":""}`,
		"fixture-404.json":   `{"error":"not found"}`,
		"fixture-405.json":   `{"error":"method not allowed"}`,
		"provenance.yaml": `
provider: fixture
verified: "2026-08-17"
goldens:
  - golden: fixture-happy.json
    endpoint: POST /v1/answer
    status: 200
    kind: simulator-chosen
    documentation_url: https://docs.fixture.example/happy
    verified: "2026-08-17"
    note: fixture golden for testkit's own ValidateProfile tests
  - golden: fixture-empty.json
    endpoint: POST /v1/answer
    status: 200
    kind: simulator-chosen
    documentation_url: https://docs.fixture.example/empty
    verified: "2026-08-17"
    note: fixture golden for testkit's own ValidateProfile tests
  - golden: fixture-404.json
    endpoint: POST /v1/answer
    status: 404
    kind: simulator-chosen
    documentation_url: https://docs.fixture.example/error
    verified: "2026-08-17"
    note: fixture golden for testkit's own ValidateProfile tests
  - golden: fixture-405.json
    endpoint: POST /v1/answer
    status: 405
    kind: simulator-chosen
    documentation_url: https://docs.fixture.example/error
    verified: "2026-08-17"
    note: fixture golden for testkit's own ValidateProfile tests
`,
	}
}

// mapFS turns a map of bare file names to file content into an fs.FS
// through testing/fstest.MapFS, which every contracts.* function and
// ValidateProfile's own Contracts subtest accept on the same terms as a
// real embed.FS.
func mapFS(files map[string]string) fstest.MapFS {
	out := make(fstest.MapFS, len(files))
	for name, data := range files {
		out[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return out
}

// fixtureProfile returns a minimal, otherwise-conformant provider.Profile:
// one route, a valid provider.Render'd body, a valid ErrorBody covering
// every RefusalKind, DefaultAuth optional (so MissingCredential has
// nothing to prove and cannot mask the one property each test below
// deliberately breaks), and a valid contract bundle.
func fixtureProfile() provider.Profile {
	return provider.Profile{
		Name:    "fixture",
		Title:   "Fixture",
		Summary: "a minimal in-package profile for testkit's own ValidateProfile tests",
		Handlers: map[string]provider.Handler{
			"POST /v1/answer": fixtureHandler,
		},
		Routes: []provider.Route{{
			Pattern:  "POST /v1/answer",
			FaultKey: "fixture:answer",
		}},
		ErrorBody:   fixtureErrorBody,
		DefaultAuth: scenario.AuthOptional,
		Contracts:   mapFS(fixtureContracts()),
	}
}

// fixtureHandler always answers 200 with a fixed, provider.Render'd body —
// deterministic, HTML-escaping off, exactly what a conformant profile does
// — after recording a warning for a non-JSON Content-Type, the same
// convention every reference profile follows and
// TestValidateProfilePassesOnAConformantFixture's WrongContentType subtest
// exercises.
func fixtureHandler(x *provider.Exchange) provider.Response {
	if !x.HasJSONContentType() {
		x.Warn("request.content_type", "", "Content-Type %q is not a JSON media type",
			x.Request.Header.Get("Content-Type"))
	}
	body, err := provider.Render(fixtureAnswer{Answer: "ok"}, nil, nil)
	if err != nil {
		return provider.Response{Status: http.StatusInternalServerError}
	}
	return provider.Response{Status: http.StatusOK, Body: body, Label: "fixture.answer", FaultEligible: true}
}

// fixtureErrorBody renders every RefusalKind in one shared shape.
func fixtureErrorBody(_ provider.Refusal) []byte {
	body, err := provider.Render(fixtureError{Error: "error"}, nil, nil)
	if err != nil {
		return []byte(`{"error":"internal error"}`)
	}
	return body
}

// TestValidateProfileFailsOnAGoldenMissingProvenance breaks exactly one
// property of an otherwise-conformant profile: its contract bundle names a
// golden provenance.yaml has no record for. ValidateProfile must fail
// through a stub testing.TB (as the existing tests in this module do; see
// contracts/contracts_test.go's stubTB and this package's own).
func TestValidateProfileFailsOnAGoldenMissingProvenance(t *testing.T) {
	files := fixtureContracts()
	files["fixture-orphan.json"] = `{"answer":"no provenance for me"}`
	// fixture-orphan.json is now on disk with no entry in provenance.yaml.

	p := fixtureProfile()
	p.Contracts = mapFS(files)

	stub := &stubTB{}
	testkit.ValidateProfile(stub, p)
	if !stub.Failed() {
		t.Fatal("ValidateProfile did not fail on a golden with no provenance record")
	}
	if !containsAny(stub.Message(), "no entry in provenance.yaml", "fixture-orphan.json") {
		t.Errorf("failure message = %q, want it to name the orphaned golden or the missing entry", stub.Message())
	}
}

// TestValidateProfileFailsOnAJSONMarshalBodyWithAmpersand breaks a
// different property: the handler reaches for encoding/json.Marshal
// directly instead of provider.Render, on a value containing "&" — default
// json.Marshal HTML-escapes it to &, which AssertRenderShape (run
// inside ValidateProfile's RenderShape subtest) exists to catch.
func TestValidateProfileFailsOnAJSONMarshalBodyWithAmpersand(t *testing.T) {
	p := fixtureProfile()
	p.Handlers = map[string]provider.Handler{
		"POST /v1/answer": func(_ *provider.Exchange) provider.Response {
			body, err := json.Marshal(fixtureAnswer{Answer: "salt & pepper"})
			if err != nil {
				return provider.Response{Status: http.StatusInternalServerError}
			}
			return provider.Response{Status: http.StatusOK, Body: body, Label: "fixture.answer", FaultEligible: true}
		},
	}

	stub := &stubTB{}
	testkit.ValidateProfile(stub, p)
	if !stub.Failed() {
		t.Fatal("ValidateProfile did not fail on a body encoding/json.Marshal HTML-escaped")
	}
	if !containsAny(stub.Message(), "\\u0026", "escape sequence") {
		t.Errorf("failure message = %q, want it to name the \\u0026 escape", stub.Message())
	}
}

// TestValidateProfileFailsOnAnErrorBodyReturningNilForOneKind breaks a
// third property: ErrorBody returns no bytes for exactly one RefusalKind —
// still non-nil overall (NewSet would refuse a nil ErrorBody outright), so
// the gap is only reachable at the per-kind check ValidateProfile's
// ErrorBody subtest runs. Table-driven over every provider.RefusalKind
// (not hardcoded to RefuseInternal alone): a mutant that drops any ONE
// kind from ValidateProfile's own internal list must be caught regardless
// of which kind it is, not only the one this test happened to pick.
func TestValidateProfileFailsOnAnErrorBodyReturningNilForOneKind(t *testing.T) {
	kinds := []provider.RefusalKind{
		provider.RefuseNotFound,
		provider.RefuseMethodNotAllowed,
		provider.RefuseScenarioUnknown,
		provider.RefuseInternal,
		provider.RefuseRequest,
	}
	for _, broken := range kinds {
		t.Run(string(broken), func(t *testing.T) {
			p := fixtureProfile()
			p.ErrorBody = func(r provider.Refusal) []byte {
				if r.Kind == broken {
					return nil
				}
				return fixtureErrorBody(r)
			}

			stub := &stubTB{}
			testkit.ValidateProfile(stub, p)
			if !stub.Failed() {
				t.Fatalf("ValidateProfile did not fail on an ErrorBody returning nil for %s", broken)
			}
			if !containsAny(stub.Message(), "returned no bytes", string(broken)) {
				t.Errorf("failure message = %q, want it to name the empty body or %s", stub.Message(), broken)
			}
		})
	}
}

// TestValidateProfileFailsOnATimeNowBody breaks the fourth property: the
// handler stamps time.Now() into its body, so the same request against two
// fresh Sims produces two different bytes — exactly what
// AssertDeterministic (a REQUIRED step of ValidateProfile) exists to catch.
func TestValidateProfileFailsOnATimeNowBody(t *testing.T) {
	p := fixtureProfile()
	p.Handlers = map[string]provider.Handler{
		"POST /v1/answer": func(_ *provider.Exchange) provider.Response {
			body, err := provider.Render(struct {
				Now string `json:"now"`
			}{Now: time.Now().Format(time.RFC3339Nano)}, nil, nil)
			if err != nil {
				return provider.Response{Status: http.StatusInternalServerError}
			}
			return provider.Response{Status: http.StatusOK, Body: body, Label: "fixture.answer", FaultEligible: true}
		},
	}

	stub := &stubTB{}
	testkit.ValidateProfile(stub, p)
	if !stub.Failed() {
		t.Fatal("ValidateProfile did not fail on a body carrying time.Now()")
	}
	if !containsAny(stub.Message(), "body differs across two fresh Sims") {
		t.Errorf("failure message = %q, want it to name the body divergence AssertDeterministic reports", stub.Message())
	}
}

// TestValidateProfileFailsOnAMissingCredentialUnder401Required breaks a
// fifth property: DefaultAuth is required, but the handler never checks
// for a credential at all, so a request presenting none still answers 200
// — exactly what ValidateProfile's MissingCredential subtest exists to
// catch. fixtureProfile() itself sets DefaultAuth optional (so
// MissingCredential is skipped, not exercised, by every other test in this
// file, including the control), so this needs its own profile: nothing
// else drives the required path with a handler that never enforces it.
func TestValidateProfileFailsOnAMissingCredentialUnder401Required(t *testing.T) {
	p := fixtureProfile()
	p.DefaultAuth = scenario.AuthRequired
	// Handlers left as fixtureHandler, which never inspects the request's
	// credentials — DefaultAuth required has nothing enforcing it.

	stub := &stubTB{}
	testkit.ValidateProfile(stub, p)
	if !stub.Failed() {
		t.Fatal("ValidateProfile did not fail when DefaultAuth is required and the handler never checks a credential")
	}
	if !containsAny(stub.Message(), "want 401") {
		t.Errorf("failure message = %q, want it to name the missing 401", stub.Message())
	}
}

// TestValidateProfileFailsOnAFloat64RoundTripBody breaks a sixth property:
// the handler formats a scripted number with Go's general float formatting
// (fmt's %v/%g, exactly what a handler decoding into a bare float64 and
// re-stringifying it by hand — rather than through provider.Render's
// json.Number path — would produce) rather than encoding/json's own
// formatter, so an integral value renders as bare exponent form ("1e+06")
// — the second of AssertRenderShape's two documented divergences, and the
// one neither this file nor any reference profile's conformance test
// exercised until now. (encoding/json.Marshal itself never emits exponent
// form for a value in [1e-6, 1e21), so the probe body is built directly
// rather than by round-tripping through json.Marshal of a float64.)
func TestValidateProfileFailsOnAFloat64RoundTripBody(t *testing.T) {
	p := fixtureProfile()
	p.Handlers = map[string]provider.Handler{
		"POST /v1/answer": func(x *provider.Exchange) provider.Response {
			if !x.HasJSONContentType() {
				x.Warn("request.content_type", "", "Content-Type %q is not a JSON media type",
					x.Request.Header.Get("Content-Type"))
			}
			body := []byte(fmt.Sprintf(`{"answer":"ok","count":%v}`, float64(1_000_000)))
			return provider.Response{Status: http.StatusOK, Body: body, Label: "fixture.answer", FaultEligible: true}
		},
	}

	stub := &stubTB{}
	testkit.ValidateProfile(stub, p)
	if !stub.Failed() {
		t.Fatal("ValidateProfile did not fail on a body carrying a bare exponent-form numeric literal")
	}
	if !containsAny(stub.Message(), "exponent-form", "1e+06") {
		t.Errorf("failure message = %q, want it to name the exponent-form literal", stub.Message())
	}
}

// TestValidateProfilePassesOnAConformantFixture is the control: the SAME
// fixtureProfile(), unmodified, must pass — proving each test above fails
// for the one reason it introduces, not because fixtureProfile() itself is
// broken.
func TestValidateProfilePassesOnAConformantFixture(t *testing.T) {
	testkit.ValidateProfile(t, fixtureProfile())
}

// TestValidateProfilePassesOnEveryReferenceProfile is this package's own
// copy of the DoD line each reference profile's profiles/<name>/
// conformance_test.go also proves: nothing about ValidateProfile privileges
// the four shipped profiles over an out-of-tree one.
func TestValidateProfilePassesOnEveryReferenceProfile(t *testing.T) {
	for _, p := range profiles.Reference() {
		t.Run(string(p.Name), func(t *testing.T) {
			t.Parallel()
			testkit.ValidateProfile(t, p)
		})
	}
}

// containsAny reports whether s contains any of substrs.
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
