package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/contracts"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// defaultConformanceScenarioYAML is the scenario [ValidateProfile] and
// [AssertDeterministic]'s own derived requests run against by default:
// version and name only, no `providers:` block at all — the empty scenario
// every profile must serve as its well-shaped empty answer
// (docs/proposals/framework-seam.md, "Built-in scenarios: reference-only").
// It carries no entry for the profile under test, on purpose: ValidateProfile
// is a library function called against a profile it has never heard of, so it
// cannot script a "correct" request for that vendor's own required fields —
// only the framework-level conventions this file checks.
const defaultConformanceScenarioYAML = "version: 1\nname: conformance\n"

// allRefusalKinds is every [provider.RefusalKind] as of this unit. It is
// deliberately a package-private literal, not [provider.Refusal]'s own
// export: a new kind added later should make this list visibly stale in
// review rather than silently skip a kind ValidateProfile has never heard of.
var allRefusalKinds = []provider.RefusalKind{
	provider.RefuseNotFound,
	provider.RefuseMethodNotAllowed,
	provider.RefuseScenarioUnknown,
	provider.RefuseInternal,
	provider.RefuseRequest,
}

// refusalKindNames maps each [provider.RefusalKind] to the Go constant name
// that spells it, kept in step with allRefusalKinds above. A failure message
// built from a bare RefusalKind prints only its wire string (RefuseRequest's
// is "request"), which is not a symbol an adopter can grep their own switch
// for; kindLabel names both.
var refusalKindNames = map[provider.RefusalKind]string{
	provider.RefuseNotFound:         "RefuseNotFound",
	provider.RefuseMethodNotAllowed: "RefuseMethodNotAllowed",
	provider.RefuseScenarioUnknown:  "RefuseScenarioUnknown",
	provider.RefuseInternal:         "RefuseInternal",
	provider.RefuseRequest:          "RefuseRequest",
}

// kindLabel renders kind as "provider.RefuseX (\"wire_value\")" when it is
// one of allRefusalKinds, falling back to just the quoted wire value for
// anything else (defensive only: ValidateProfile never calls it with a kind
// outside that list).
func kindLabel(kind provider.RefusalKind) string {
	if name, ok := refusalKindNames[kind]; ok {
		return fmt.Sprintf("provider.%s (%q)", name, kind)
	}
	return fmt.Sprintf("%q", kind)
}

// ValidateProfile runs the conformance suite an adopter's own CI calls
// against their profile — the same call each reference profile's own
// profiles/<name>/conformance_test.go makes
// (docs/proposals/framework-seam.md, "the eleven CONTRIBUTING.md
// conventions"). Every check is a named subtest, real when tb supports
// t.Run (a genuine *testing.T, directly or nested under a caller's own
// t.Run), inline against tb otherwise — the same shape [contracts.Conform]
// uses, and for the same reason: ValidateProfile's own tests
// (conformance_test.go) prove it fails a broken profile through a stub
// testing.TB, which has no Run method to speak of.
//
// Every subtest reports through Errorf, never Fatalf, so one failing
// convention does not hide the next — the same discipline
// [contracts.Conform] follows, and the reason a stub TB never needs to
// implement FailNow.
//
// What it checks, in order:
//
//   - NewSet: p registers cleanly through [provider.NewSet] on its own.
//   - Contracts: p.Contracts is non-nil (house rule 1 — no exception) and
//     conforms through [contracts.Conform].
//   - ErrorBody: every [provider.RefusalKind] renders a non-empty body
//     through p.Refuse, with a request in hand and without one.
//   - UnknownPath: an unregistered path answers 404 in the profile's own
//     shape and journals route.unmatched.
//   - WrongMethod: a known path with the wrong method answers 405 with an
//     Allow header and a non-empty body.
//   - WrongContentType: a POST route with a non-JSON Content-Type records a
//     finding naming it (status and severity are the profile's own choice —
//     three of the four reference profiles warn and serve 200 by default,
//     because none of their vendors documents a 415).
//   - MissingCredential: a route with no credential answers 401 when
//     p.DefaultAuth is required (empty counts as required); skipped, named,
//     when it is optional.
//   - FaultKeysResolve: every Route.FaultKey is known to the Set's own fault
//     engine — the AUDIT 1 blocker "an out-of-tree route is fault-blind"
//     cannot reoccur for a profile this check has run against.
//   - RejectionDoesNotClaimAnAttempt: a malformed-JSON request never claims a
//     fault attempt, and the first request afterward that IS accepted claims
//     attempt 0 — CONTRIBUTING's "validate before you claim".
//   - Deterministic: [AssertDeterministic], required — see its own doc
//     comment for what "required" means here.
//   - RenderShape: [AssertRenderShape] over every JSON golden in p.Contracts
//     and every response this suite's own requests produced.
func ValidateProfile(tb testing.TB, p provider.Profile) {
	tb.Helper()

	runSubtest(tb, "NewSet", func(tb testing.TB) { validateNewSet(tb, p) })
	runSubtest(tb, "Contracts", func(tb testing.TB) { validateContracts(tb, p) })
	runSubtest(tb, "ErrorBody", func(tb testing.TB) { validateErrorBody(tb, p) })
	runSubtest(tb, "UnknownPath", func(tb testing.TB) { validateUnknownPath(tb, p) })
	runSubtest(tb, "WrongMethod", func(tb testing.TB) { validateWrongMethod(tb, p) })
	runSubtest(tb, "WrongContentType", func(tb testing.TB) { validateWrongContentType(tb, p) })
	runSubtest(tb, "MissingCredential", func(tb testing.TB) { validateMissingCredential(tb, p) })
	runSubtest(tb, "FaultKeysResolve", func(tb testing.TB) { validateFaultKeysResolve(tb, p) })
	runSubtest(tb, "RejectionDoesNotClaimAnAttempt", func(tb testing.TB) { validateAttemptZero(tb, p) })

	reqs := minimalRequests(p)
	runSubtest(tb, "Deterministic", func(tb testing.TB) {
		AssertDeterministic(tb, p, defaultConformanceScenarioYAML, reqs...)
	})
	runSubtest(tb, "RenderShape", func(tb testing.TB) { validateRenderShape(tb, p, reqs) })
}

// runSubtest runs f as a real Go subtest named name when tb supports it, and
// inline against tb otherwise. It is testkit's own copy of the same pattern
// contracts.Conform uses (unexported there too, and this package may not
// reach across the package boundary for it): testing.TB declares no Run
// method — *testing.T and *testing.B each have one, with incompatible
// signatures, so the interface cannot unify them.
func runSubtest(tb testing.TB, name string, f func(testing.TB)) {
	tb.Helper()

	type runner interface {
		Run(string, func(*testing.T)) bool
	}
	if r, ok := tb.(runner); ok {
		r.Run(name, func(t *testing.T) { f(t) })
		return
	}
	f(tb)
}

// validateNewSet checks p registers on its own, cheaply, before anything
// else here spins up a server over it.
func validateNewSet(tb testing.TB, p provider.Profile) {
	tb.Helper()
	if _, err := provider.NewSet(p); err != nil {
		tb.Errorf("provider.NewSet(p) refused this profile: %v", err)
	}
}

// validateContracts checks house rule 1's whole claim: a profile ships its
// contract, with no exception for a nil Contracts, and the bundle it ships
// conforms through [contracts.Conform].
func validateContracts(tb testing.TB, p provider.Profile) {
	tb.Helper()
	if p.Contracts == nil {
		tb.Errorf("Profile.Contracts is nil; a profile must ship its own contract bundle — a golden set, " +
			"provenance.yaml and README, embedded beside the profile's package (//go:embed contracts) — " +
			"with no exception (docs/proposals/framework-seam.md, \"Profile.Contracts\": " +
			"\"goldens + provenance.yaml + README\")")
		return
	}
	contracts.Conform(tb, p.Contracts)
}

// validateErrorBody checks every RefusalKind renders a non-empty body
// through p.Refuse (not p.ErrorBody directly: Refuse is the real call path,
// and it fills in the Kind-appropriate default Status an ErrorBody
// implementation is written to expect — calling ErrorBody bare would leave
// Status at zero for every kind but RefuseRequest, which is not what a
// profile's own switch was written against). Each kind is tried with a
// freshly built Exchange, and again with none: Refusal.X is nil for exactly
// one kind in production (RefuseScenarioUnknown, raised before any listener
// resolves a scenario), but nothing stops a profile's own ErrorBody from
// being called either way, and all four reference profiles handle both for
// every kind.
func validateErrorBody(tb testing.TB, p provider.Profile) {
	tb.Helper()
	if p.ErrorBody == nil {
		tb.Errorf("Profile.ErrorBody is nil; house rule 3 (fail closed, never proxy; " +
			"docs/proposals/framework-seam.md, \"Rule 3\") requires every refusal to render in the " +
			"vendor's own shape, never an empty body — NewSet already refuses a Profile whose " +
			"ErrorBody is nil, so this profile could never actually be registered")
		return
	}

	x := &provider.Exchange{Deps: provider.Deps{}.Normalized(), Provider: p.Name}
	for _, kind := range allRefusalKinds {
		refuseSafely(tb, p, provider.Refusal{Kind: kind, X: x}, true)
		refuseSafely(tb, p, provider.Refusal{Kind: kind}, false)
	}
}

// refuseSafely calls p.Refuse(r) under recover, so an ErrorBody that
// dereferences r.X unconditionally — a natural mistake to write, since
// every RefusalKind but RefuseScenarioUnknown carries a non-nil X in
// production — fails this one check with a named message instead of
// crashing the whole test binary and losing every subtest ValidateProfile
// has not run yet ([provider.Refusal]'s doc comment: "X is nil for a
// refusal raised outside any request the profile could handle").
func refuseSafely(tb testing.TB, p provider.Profile, r provider.Refusal, withExchange bool) {
	tb.Helper()

	shape := "with a request in hand (X != nil)"
	if !withExchange {
		shape = "with no Exchange (X == nil) — Refusal.X is nil for RefuseScenarioUnknown in " +
			"production; ErrorBody must not dereference it unconditionally"
	}

	defer func() {
		if rec := recover(); rec != nil {
			tb.Errorf("ErrorBody (via Refuse) panicked for %s %s: %v", kindLabel(r.Kind), shape, rec)
		}
	}()

	if body := p.Refuse(r); len(body) == 0 {
		tb.Errorf("Refuse(Refusal{Kind: %s} %s) returned no bytes; house rule 3 (fail closed, never "+
			"proxy; docs/proposals/framework-seam.md, \"Rule 3\") requires a non-empty body for every "+
			"refusal", kindLabel(r.Kind), shape)
	}
}

// validateUnknownPath checks CONTRIBUTING's first mechanisable convention: a
// path no route serves answers the profile's own 404 shape and journals
// route.unmatched.
func validateUnknownPath(tb testing.TB, p provider.Profile) {
	tb.Helper()

	sim := Start(tb, WithProfiles(p), WithScenarioYAML(defaultConformanceScenarioYAML))
	defer sim.Close()

	count := 0
	resp, body, entry, ok := requestAndEntry(tb, sim, p.Name, &count,
		http.MethodGet, "/__validateprofile-unknown-path__", nil, "")
	if !ok {
		return
	}
	if resp.StatusCode != http.StatusNotFound {
		tb.Errorf("GET an unregistered path: status = %d, want 404", resp.StatusCode)
	}
	if len(body) == 0 {
		tb.Errorf("GET an unregistered path: empty body, want the profile's own 404 shape " +
			"(house rule 3, fail closed, never proxy; docs/proposals/framework-seam.md, \"Rule 3\")")
	}
	if !hasFindingCode(entry, provider.CodeUnmatched) {
		tb.Errorf("GET an unregistered path: findings = %v, want %q", entry.Findings, provider.CodeUnmatched)
	}
}

// validateWrongMethod checks the second convention: a known path with an
// unsupported method answers 405, carries Allow, and a non-empty body. It
// runs once per distinct path p.Routes declares.
func validateWrongMethod(tb testing.TB, p provider.Profile) {
	tb.Helper()

	sim := Start(tb, WithProfiles(p), WithScenarioYAML(defaultConformanceScenarioYAML))
	defer sim.Close()

	count := 0
	seen := map[string]bool{}
	for _, rt := range p.Routes {
		method, path, ok := strings.Cut(rt.Pattern, " ")
		if !ok || seen[path] {
			continue
		}
		seen[path] = true

		wrong := http.MethodPost
		if method == http.MethodPost {
			wrong = http.MethodGet
		}

		resp, body, _, ok := requestAndEntry(tb, sim, p.Name, &count, wrong, path, nil, "")
		if !ok {
			continue
		}
		if resp.StatusCode != http.StatusMethodNotAllowed {
			tb.Errorf("%s %s (a route that serves %s): status = %d, want 405", wrong, path, method, resp.StatusCode)
		}
		if resp.Header.Get("Allow") == "" {
			tb.Errorf("%s %s: no Allow header on a 405 response", wrong, path)
		}
		if len(body) == 0 {
			tb.Errorf("%s %s: empty 405 body, want the profile's own shape", wrong, path)
		}
	}
}

// wrongContentTypeBody is the probe body validateWrongContentType sends. It
// is shaped as a well-formed JSON-RPC request/notification — jsonrpc,
// method and id all present — not because every profile speaks JSON-RPC,
// but because MCP's own transport-shape check (checkTransport) runs only
// after its envelope-shape check passes, and a bare "{}" never gets that
// far: it is rejected earlier, for a different reason, before Content-Type
// is ever consulted. The three research profiles ignore the extra
// jsonrpc/method/id keys as harmless unknown fields; none of their content-
// type checks depends on the body's shape.
const wrongContentTypeBody = `{"jsonrpc":"2.0","method":"ping","id":1}`

// wrongContentTypeAccept is the Accept header the probe sends alongside it,
// for the same reason: MCP's checkTransport checks Accept before
// Content-Type and returns immediately on the first violation, so an Accept
// header missing text/event-stream would mask the content-type check this
// probe exists to reach. The other three profiles do not consult Accept at
// all, so this is a no-op for them.
const wrongContentTypeAccept = "application/json, text/event-stream"

// wrongContentTypeAuth is the placeholder credential the probe presents,
// for a third reason of the same shape: the default conformance scenario
// (defaultConformanceScenarioYAML) scripts no entry for the profile under
// test, so [scenario.AuthPolicy]'s ExpectKey is always empty here, and a
// profile whose own request order checks "is a credential present" before
// it checks Content-Type — a legitimate, common order this package cannot
// assume against — would otherwise refuse this probe for a missing
// credential before ever reaching the content-type check, on any profile
// whose DefaultAuth is required. Presenting SOME credential, of any value,
// satisfies "present" without needing to match anything (there is nothing
// to match). MCP's own checks ignore Authorization entirely, so this is a
// no-op there.
const wrongContentTypeAuth = "Bearer validateprofile-probe"

// validateWrongContentType checks that at least one POST route records a
// finding naming Content-Type when the request declares one Servicesim
// does not treat as JSON. "At least one", not "every route": a vendor may
// document several POST surfaces with different validation depth (Tavily's
// /research create, for one, checks neither JSON shape nor its content
// type — a real, existing gap this check does not exist to invent an
// opinion about), so this proves the CAPABILITY exists in the profile
// somewhere, not that every route implements it.
//
// It does not assert a status: request.content_type is a WARNING by
// default for three of the four reference profiles (none of their vendors
// documents a 415), and an ERROR for MCP, which is a real protocol
// requirement — both are the profile's own choice, and this check exists to
// prove the finding is recorded, not to pick a side.
func validateWrongContentType(tb testing.TB, p provider.Profile) {
	tb.Helper()

	sim := Start(tb, WithProfiles(p), WithScenarioYAML(defaultConformanceScenarioYAML))
	defer sim.Close()

	count, tried, found := 0, 0, false
	var lastFindings any
	for _, rt := range p.Routes {
		method, path, ok := strings.Cut(rt.Pattern, " ")
		if !ok || method == http.MethodGet || method == http.MethodHead {
			continue // no body to mis-type on a route this check can assume takes none
		}
		tried++

		_, _, entry, ok := requestAndEntry(tb, sim, p.Name, &count, method, path,
			map[string]string{
				"Content-Type":  "text/plain; charset=utf-8",
				"Accept":        wrongContentTypeAccept,
				"Authorization": wrongContentTypeAuth,
			},
			wrongContentTypeBody)
		if !ok {
			continue
		}
		lastFindings = entry.Findings
		if hasFindingContaining(entry, "content_type", "content-type") {
			found = true
			break
		}
	}
	if tried > 0 && !found {
		tb.Errorf("no POST route recorded a finding whose code contains \"content_type\" or "+
			"\"content-type\" for a text/plain body sent as %s with Authorization: %s and Accept: %s "+
			"(last tried route's findings = %v); a profile must record such a finding — via "+
			"x.HasJSONContentType() and x.Warn/x.Fail — before any auth or shape refusal, on at least one "+
			"POST route (docs/proposals/framework-seam.md, \"the eleven CONTRIBUTING.md conventions\", "+
			"wrong content type)", wrongContentTypeBody, wrongContentTypeAuth, wrongContentTypeAccept, lastFindings)
	}
}

// validateMissingCredential checks the fourth convention: a request with no
// credential at all answers 401 when p.DefaultAuth is required — the
// meaning its own empty value already carries
// (docs/proposals/framework-seam.md's Profile.DefaultAuth). It is skipped,
// named, when DefaultAuth is optional — MCP's own default, deliberately
// opposite the three research profiles — because there is nothing to prove
// generically about a policy that admits an unauthenticated request on
// purpose.
func validateMissingCredential(tb testing.TB, p provider.Profile) {
	tb.Helper()

	mode := p.DefaultAuth
	if mode == "" {
		mode = scenario.AuthRequired
	}
	if mode != scenario.AuthRequired {
		skip(tb, fmt.Sprintf("Profile.DefaultAuth is %q, not required: a missing credential is not "+
			"expected to be refused, so there is nothing to prove here", mode))
		return
	}

	sim := Start(tb, WithProfiles(p), WithScenarioYAML(defaultConformanceScenarioYAML))
	defer sim.Close()

	count := 0
	for _, rt := range p.Routes {
		method, path, ok := strings.Cut(rt.Pattern, " ")
		if !ok {
			continue
		}
		var headers map[string]string
		body := ""
		if method != http.MethodGet && method != http.MethodHead {
			headers = map[string]string{"Content-Type": "application/json"}
			body = "{}"
		}

		resp, respBody, _, ok := requestAndEntry(tb, sim, p.Name, &count, method, path, headers, body)
		if !ok {
			continue
		}
		if resp.StatusCode != http.StatusUnauthorized {
			tb.Errorf("%s %s with no credential presented: status = %d, want 401 (DefaultAuth is %q)",
				method, path, resp.StatusCode, mode)
		}
		// HEAD carries no body by HTTP's own rule (net/http strips it at the
		// transport layer regardless of what the handler wrote), so an empty
		// body here says nothing about the profile's error shape.
		if method != http.MethodHead && len(respBody) == 0 {
			tb.Errorf("%s %s with no credential presented: empty body, want the profile's own 401 shape",
				method, path)
		}
	}
}

// validateFaultKeysResolve checks AUDIT 1's ranked #1 blocker cannot recur
// for a profile this check has run against: every Route.FaultKey is known to
// a fault engine built over this profile's own Set (the only exported
// constructor, (*provider.Set).Faults). It builds the engine directly rather
// than issuing requests, because Unknown is a property of the key's
// registration, not of any one call.
func validateFaultKeysResolve(tb testing.TB, p provider.Profile) {
	tb.Helper()

	set, err := provider.NewSet(p)
	if err != nil {
		return // the NewSet subtest already reports this
	}
	sc, report, err := scenario.Parse([]byte(defaultConformanceScenarioYAML))
	if err != nil || !report.OK() {
		tb.Errorf("testkit: parsing the default conformance scenario: %v (%v)", err, report.Findings)
		return
	}

	engine := set.Faults(sc)
	for _, rt := range p.Routes {
		if dec := engine.Next(rt.FaultKey); dec.Unknown {
			tb.Errorf("route %q: FaultKey %q is unknown to its own Set's fault engine — "+
				"(*provider.Set).Faults(sc) must be built from a Set that includes every registered route, "+
				"or a scripted fault silently serves a clean 200", rt.Pattern, rt.FaultKey)
		}
	}
}

// validateAttemptZero checks CONTRIBUTING's "validate before you claim": a
// request the shared lifecycle itself rejects (malformed JSON, decoded
// before any handler runs) must never claim a fault attempt, and the first
// request afterward that the profile DOES accept claims attempt 0 — the
// budget a validate-before-claim rejection must never have spent.
//
// It cannot force a route-specific "valid" request generically (this
// package has never heard of the vendor's own required fields), so a second,
// still-rejected {} body is an accepted outcome too: what it insists on is
// that NO rejection, on this route, ever claims an attempt — and that IF a
// request is accepted, it is the first to claim, at index 0.
func validateAttemptZero(tb testing.TB, p provider.Profile) {
	tb.Helper()

	sim := Start(tb, WithProfiles(p), WithScenarioYAML(defaultConformanceScenarioYAML))
	defer sim.Close()

	count := 0
	for _, rt := range p.Routes {
		method, path, ok := strings.Cut(rt.Pattern, " ")
		if !ok || method == http.MethodGet || method == http.MethodHead {
			continue // this check needs a body-bearing route to force a malformed-JSON rejection
		}

		_, _, rejected, ok := requestAndEntry(tb, sim, p.Name, &count, method, path,
			map[string]string{"Content-Type": "application/json"}, "{not valid json")
		if !ok {
			continue
		}
		if rejected.Outcome.AttemptIndex != -1 {
			tb.Errorf("%s %s: a malformed-JSON request claimed fault attempt %d; CONTRIBUTING's "+
				"\"validate before you claim\" requires a rejected request to draw no attempt",
				method, path, rejected.Outcome.AttemptIndex)
		}

		_, _, next, ok := requestAndEntry(tb, sim, p.Name, &count, method, path,
			map[string]string{"Content-Type": "application/json"}, "{}")
		if !ok {
			continue
		}
		switch {
		case next.Outcome.Status >= 200 && next.Outcome.Status < 300:
			if next.Outcome.AttemptIndex != 0 {
				tb.Errorf("%s %s: the first accepted request after a rejection claimed attempt %d, "+
					"want 0 (the earlier rejection must not have spent the budget)",
					method, path, next.Outcome.AttemptIndex)
			}
		default:
			if next.Outcome.AttemptIndex != -1 {
				tb.Errorf("%s %s: a rejected request (a bare {} body) claimed fault attempt %d, "+
					"want none claimed (-1): validate before you claim",
					method, path, next.Outcome.AttemptIndex)
			}
		}
	}
}

// validateRenderShape runs [AssertRenderShape] over every JSON golden in
// p.Contracts and every response reqs produces against a fresh Sim — one
// call per body, each wrapped in a renderShapeLabelTB so a failure names
// WHERE the body came from (a golden's file name, or the request that
// produced a response) instead of an anonymous index into a concatenated
// slice, and so a golden's failure carries the fixture's own remedy
// (edit the file) rather than AssertRenderShape's default advice to render
// through provider.Render, which does not apply to a hand-written fixture.
func validateRenderShape(tb testing.TB, p provider.Profile, reqs []*http.Request) {
	tb.Helper()

	if p.Contracts != nil {
		goldens, err := contracts.Goldens(p.Contracts)
		if err != nil {
			tb.Errorf("testkit: RenderShape: loading goldens: %v", err)
		}
		for _, g := range goldens {
			label := renderShapeLabelTB{TB: tb, label: fmt.Sprintf("golden %s", g.Name), golden: true}
			AssertRenderShape(label, g.Data)
		}
	}

	sim := Start(tb, WithProfiles(p), WithScenarioYAML(defaultConformanceScenarioYAML))
	for _, req := range reqs {
		snap := snapshotRequest(tb, sim, p.Name, req)
		if snap.body != nil {
			label := renderShapeLabelTB{
				TB:    tb,
				label: fmt.Sprintf("response to %s %s", req.Method, req.URL.Path),
			}
			AssertRenderShape(label, snap.body)
		}
	}
	sim.Close()
}

// renderShapeLabelTB wraps a testing.TB so an [AssertRenderShape] failure
// names its source (golden name, or the request that produced a response)
// and, for a golden, replaces AssertRenderShape's "render through
// provider.Render" advice — which assumes a handler wrote the body — with
// the remedy that actually applies to a hand-written fixture file.
type renderShapeLabelTB struct {
	testing.TB
	label  string
	golden bool
}

func (l renderShapeLabelTB) Errorf(format string, args ...any) {
	l.TB.Helper()
	msg := fmt.Sprintf(format, args...)
	if l.golden {
		msg = strings.NewReplacer(
			"render through provider.Render, which turns HTML escaping off",
			"check the vendor's own documented literal, or re-record this golden through provider.Render",
			"render through provider.Render, which decodes with json.Number",
			"check the vendor's own documented literal, or re-record this golden through provider.Render "+
				"(which decodes with json.Number)",
		).Replace(msg)
	}
	l.TB.Errorf("%s: %s", l.label, msg)
}

// AssertDeterministic proves rule 2's mechanical half for p: the same
// requests, issued in the same order against two INDEPENDENT, freshly
// started Sims, produce byte-identical response bodies and the headers a
// profile controls (every header but Date; Content-Length included — a
// profile that varies it is not byte-identical, whatever its own status
// code). Two fresh Sims, not one Sim called twice, because a shared Sim
// would prove only that the SAME process answers itself consistently, not
// that the response is a pure function of the request and the scenario —
// the actual determinism claim (house rule 2).
//
// It is a REQUIRED step of [ValidateProfile] — not an opt-in a profile could
// silently skip — because a `time.Now()` or `math/rand` on a response path
// is exactly the failure class house rule 2 exists to catch, and it is the
// one convention here with no cheap manual substitute: a person reading a
// diff cannot see a clock read that happens to return the same value in a
// two-second test run.
//
// reqs carry no host: AssertDeterministic resolves each one against
// whichever Sim it is currently driving, through sim.URL(p.Name) +
// req.URL.RequestURI(), and rebuilds the body from req.GetBody (set
// automatically by http.NewRequest for a *bytes.Reader, *bytes.Buffer or
// *strings.Reader body) so the same *http.Request can be replayed against
// both Sims without either one draining the other's copy of the body.
// [ValidateProfile] derives reqs from p.Routes itself — one request per
// route, the method and, for anything but GET/HEAD, a bare {} body — and a
// caller wanting richer coverage passes its own.
func AssertDeterministic(tb testing.TB, p provider.Profile, scenarioYAML string, reqs ...*http.Request) {
	tb.Helper()

	if len(reqs) == 0 {
		tb.Error("testkit: AssertDeterministic called with no requests; nothing to compare")
		return
	}

	var runs [2][]requestSnapshot
	for i := range runs {
		sim := Start(tb, WithProfiles(p), WithScenarioYAML(scenarioYAML))
		for _, req := range reqs {
			runs[i] = append(runs[i], snapshotRequest(tb, sim, p.Name, req))
		}
		sim.Close()
	}

	for i, req := range reqs {
		a, b := runs[0][i], runs[1][i]
		label := fmt.Sprintf("request %d (%s %s)", i, req.Method, req.URL.String())

		if a.status != b.status {
			tb.Errorf("%s: status differs across two fresh Sims: %d vs %d", label, a.status, b.status)
		}
		if !bytes.Equal(a.body, b.body) {
			tb.Errorf("%s: body differs across two fresh Sims:\n  run 1: %s\n  run 2: %s\n"+
				"house rule 2 (determinism; docs/proposals/framework-seam.md, \"Rule 2\"): a response must "+
				"be a pure function of the request and the scenario — no time.Now(), math/rand, crypto/rand "+
				"or a freshly generated UUID on a response path; derive any identifier deterministically "+
				"with provider.Hex32/UUIDv5/FloatIn instead", label, a.body, b.body)
		}
		if diff := diffControlledHeaders(a.header, b.header); diff != "" {
			tb.Errorf("%s: headers differ across two fresh Sims:\n%s"+
				"house rule 2 (determinism; docs/proposals/framework-seam.md, \"Rule 2\"): a controlled "+
				"header must not vary between two runs of the same request", label, diff)
		}
	}
}

// requestSnapshot is one response AssertDeterministic captured.
type requestSnapshot struct {
	status int
	header http.Header
	body   []byte
}

// snapshotRequest issues req against sim's own server for p, rebuilding the
// body from req.GetBody so the same *http.Request can be replayed against a
// second Sim afterward.
func snapshotRequest(tb testing.TB, sim *Sim, name provider.Name, req *http.Request) requestSnapshot {
	tb.Helper()

	var bodyReader io.Reader
	if req.GetBody != nil {
		rc, err := req.GetBody()
		if err != nil {
			tb.Errorf("testkit: rebuilding the request body for %s %s: %v", req.Method, req.URL, err)
		} else {
			bodyReader = rc
		}
	}

	full := sim.URL(name) + req.URL.RequestURI()
	out, err := http.NewRequestWithContext(context.Background(), req.Method, full, bodyReader)
	if err != nil {
		tb.Errorf("testkit: building request %s %s: %v", req.Method, full, err)
		return requestSnapshot{}
	}
	out.Header = req.Header.Clone()

	resp, err := sim.Client().Do(out)
	if err != nil {
		tb.Errorf("testkit: %s %s: %v", req.Method, full, err)
		return requestSnapshot{}
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Errorf("testkit: reading the response body for %s %s: %v", req.Method, full, err)
		return requestSnapshot{status: resp.StatusCode}
	}
	return requestSnapshot{status: resp.StatusCode, header: resp.Header.Clone(), body: data}
}

// diffControlledHeaders reports every header that differs between a and b,
// except Date, which no profile controls (it is net/http's own clock).
func diffControlledHeaders(a, b http.Header) string {
	keys := make(map[string]struct{})
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}

	var buf strings.Builder
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		if strings.EqualFold(k, "Date") {
			continue
		}
		av, bv := a.Values(k), b.Values(k)
		if !slices.Equal(av, bv) {
			fmt.Fprintf(&buf, "  %s: %v vs %v\n", k, av, bv)
		}
	}
	return buf.String()
}

// exponentForIntegral matches a bare (unquoted) exponent-form numeric
// literal following a JSON structural character — the signature of a
// float64 round trip through encoding/json's default formatting (1000000
// becomes "1e+06"). It is a heuristic, not a JSON tokenizer: a string VALUE
// that happens to contain similar text is vanishingly unlikely to be
// preceded by a bare structural character with no closing quote between
// them, which is what keeps false positives rare without writing a decoder.
var exponentForIntegral = regexp.MustCompile(`[:,\[]\s*-?\d+e\+\d+`)

// htmlEscapeSequences are the literal byte sequences default json.Marshal
// writes in place of <, > and & when HTML escaping is on (the zero value of
// json.Encoder.SetEscapeHTML, and what a bare json.Marshal call always
// does). The check is for these six-byte ESCAPE sequences appearing verbatim
// in the rendered bytes, not for the raw characters themselves: a
// correctly-rendered body (HTML escaping off, exactly what provider.Render
// does) is free to carry a literal "&" — a title reading "Foo & Bar" is
// legitimate content — so testing for the raw character would be a false
// positive on exactly the bodies this check exists to pass.
var htmlEscapeSequences = [][]byte{[]byte("\\u003c"), []byte("\\u003e"), []byte("\\u0026")}

// AssertRenderShape fails on a JSON body carrying either of the two
// encoding/json divergences internal/wire's package doc exists to prevent
// (docs/proposals/framework-seam.md, rule 2's byte-fidelity paragraph):
//
//   - a <, > or & escape sequence — default json.Marshal's
//     HTML escaping, which provider.Render turns off;
//   - a bare exponent-form numeral for what looks like an integral value —
//     the signature of a float64 round trip, which provider.Render avoids by
//     decoding with json.Number.
//
// It is a HEURISTIC, not a proof: both divergences are perfectly
// deterministic, so [AssertDeterministic] cannot see them, and a body that
// happens to avoid both patterns is not thereby proven to have been rendered
// through provider.Render. provider.Render is the answer that needs no
// heuristic; this is what the framework can still do after the fact, for a
// profile that reached for encoding/json directly instead.
//
// A body that is not valid JSON is skipped — a stream chunk, an SSE
// transcript, or any of the non-JSON content MCP's protocol carries. Neither
// divergence is expressible outside a JSON document, so there is nothing
// here to check.
func AssertRenderShape(tb testing.TB, bodies ...[]byte) {
	tb.Helper()

	for i, body := range bodies {
		if !json.Valid(body) {
			continue
		}
		for _, esc := range htmlEscapeSequences {
			if bytes.Contains(body, esc) {
				tb.Errorf("body %d contains the escape sequence %q — the signature of encoding/json's "+
					"default HTML escaping; render through provider.Render, which turns HTML escaping off: %s",
					i, esc, body)
			}
		}
		if exponentForIntegral.Match(body) {
			tb.Errorf("body %d contains a bare exponent-form numeric literal (for example 1e+06) — "+
				"the signature of a float64 round trip through encoding/json; render through "+
				"provider.Render, which decodes with json.Number: %s", i, body)
		}
	}
}

// minimalRequests derives one request per distinct route pattern in p: the
// route's own method, and — for anything but GET or HEAD — a bare {} JSON
// body with Content-Type set. It is deliberately vendor-agnostic: this
// package cannot know a route's required fields, so the request is only ever
// "minimally well-formed", not "valid" in the vendor's own terms — a
// property [AssertDeterministic] does not need (a deterministic rejection is
// just as good a proof as a deterministic success) and
// [validateAttemptZero] accounts for explicitly.
func minimalRequests(p provider.Profile) []*http.Request {
	var out []*http.Request
	seen := make(map[string]bool, len(p.Routes))
	for _, rt := range p.Routes {
		if seen[rt.Pattern] {
			continue
		}
		seen[rt.Pattern] = true

		method, path, ok := strings.Cut(rt.Pattern, " ")
		if !ok {
			continue
		}

		var body io.Reader
		if method != http.MethodGet && method != http.MethodHead {
			body = strings.NewReader("{}")
		}
		req, err := http.NewRequest(method, path, body) //nolint:noctx // template request; AssertDeterministic supplies its own context per Sim
		if err != nil {
			continue
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		out = append(out, req)
	}
	return out
}

// requestAndEntry issues one request against sim, waits for its journal
// entry through [Sim.AwaitRequests] (never a bare Requests call: a
// convention check must never race the server goroutine that is still
// completing the entry), and returns the response, its body, and that
// entry. count is the caller's own running request count against this Sim
// and profile; it is incremented here.
//
// ok is false when the request could not even be issued — building it or
// reading its response failed, already reported through tb.Errorf — so the
// caller can skip the rest of its check for this one route without
// double-reporting.
func requestAndEntry(
	tb testing.TB, sim *Sim, name provider.Name, count *int,
	method, path string, headers map[string]string, body string,
) (*http.Response, []byte, Entry, bool) {
	tb.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, sim.URL(name)+path, reader)
	if err != nil {
		tb.Errorf("testkit: building request %s %s: %v", method, path, err)
		return nil, nil, Entry{}, false
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := sim.Client().Do(req)
	if err != nil {
		tb.Errorf("testkit: %s %s: %v", method, path, err)
		return nil, nil, Entry{}, false
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Errorf("testkit: reading the response body for %s %s: %v", method, path, err)
		return resp, nil, Entry{}, false
	}

	*count++
	entries := sim.AwaitRequests(tb, name, *count)
	return resp, data, entries[len(entries)-1], true
}

// hasFindingCode reports whether e carries a finding with exactly this code.
func hasFindingCode(e Entry, code string) bool {
	for _, f := range e.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

// hasFindingContaining reports whether e carries a finding whose code
// contains any of the substrings — used where the convention is real but
// the exact code spelling is a profile's own choice (MCP's
// mcp.request.content_type_invalid versus the other three profiles'
// request.content_type).
func hasFindingContaining(e Entry, substrings ...string) bool {
	for _, f := range e.Findings {
		for _, s := range substrings {
			if strings.Contains(f.Code, s) {
				return true
			}
		}
	}
	return false
}

// skip calls tb.Skip when tb is a genuine *testing.T or *testing.B (a real
// one, or the one runSubtest produced via Run), and does nothing otherwise.
//
// The check is on the CONCRETE type, deliberately, not a structural
// interface assertion for a Skip method: testing.TB itself declares Skip,
// so any stub that embeds testing.TB to stand in for it — exactly the
// pattern [ValidateProfile]'s own broken-profile tests use — would satisfy
// a structural "has a Skip method" assertion too, and calling it would
// panic on the stub's nil embedded interface (the same trap
// contracts.Conform's stubTB doc comment warns about, met here from the
// other direction: not "a method we didn't override", but "a method every
// testing.TB has by construction, real or not"). A silent no-op is the
// correct behaviour for a stub: the check simply declines to run.
func skip(tb testing.TB, reason string) {
	tb.Helper()
	switch v := tb.(type) {
	case *testing.T:
		v.Skip(reason)
	case *testing.B:
		v.Skip(reason)
	}
}
