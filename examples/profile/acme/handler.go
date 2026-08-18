package acme

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// Finding codes this package raises. Exported because they are the
// assertable half of the journal (the reference profiles' own convention):
// a test proves Acme rejected the request it meant to reject by asserting on
// the code, not by re-deriving the status Acme happened to answer with.
const (
	// CodeQueryMissing reports an absent, non-string or empty query on
	// POST /v1/answer, the one required field this vendor documents.
	CodeQueryMissing = "acme.query.missing"

	// CodeAuthMissing reports a request that presented no credential at all
	// on a route where one is required.
	CodeAuthMissing = "acme.auth.missing"

	// CodeAuthMismatch reports a presented credential that does not match
	// the scenario's expected key, or a scenario that rejects every
	// credential outright (auth: {mode: reject}).
	CodeAuthMismatch = "acme.auth.mismatch"

	// CodeAuthWrongScheme reports an Authorization header carrying a scheme
	// other than the documented Bearer. A warning, not a rejection: the
	// credential still authenticates here, but an adapter sending Basic is
	// one vendor deploy away from a 401 it cannot reproduce, and house rule 5
	// is what makes that visible now rather than then.
	CodeAuthWrongScheme = "acme.auth.wrong_scheme"

	// CodeAuthWrongHeader reports a credential presented in x-acme-key, the
	// header Profile.CredentialNames declares so the journal redacts it —
	// which is not the same as accepting it. Acme's contract documents
	// Authorization: Bearer and no other placement.
	CodeAuthWrongHeader = "acme.auth.wrong_header"

	// CodeContentType reports a POST body whose Content-Type is not a JSON
	// media type. It is a warning, not a rejection: Acme's own documentation
	// does not describe a 415, the same convention the three research
	// reference profiles follow (docs/building-a-profile.md, step 2).
	CodeContentType = "acme.request.content_type"

	// CodeProjectionInvalid reports a scenario projection that failed to
	// decode or render. Reaching this in production means a fixture skipped
	// startup validation (validator.ValidateProjections runs before
	// readiness); testkit builds always run it first.
	CodeProjectionInvalid = "acme.projection.invalid"

	// CodeProjectionUnresolved reports a `$ref` in a turn's respond body
	// that named no declared source.
	CodeProjectionUnresolved = "acme.projection.unresolved"
)

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

// defaultPlacements is the credential placement Acme documents: an
// Authorization header, no other. A real vendor with more than one
// documented placement (Tavily's body-placed api_key, for instance) lists
// them all here; profiles/tavily/request.go's acceptedPlacements is the
// worked example docs/building-a-profile.md points readers at.
var defaultPlacements = []string{provider.PlacementAuthorization}

// Routes returns the routes this profile serves, in registration order. It is
// a function, not a package-level var, so no consumer can mutate the route
// table of a package it merely imported (the same convention every reference
// profile's own Routes follows).
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

// handlers maps every route this profile serves to its handler, read by
// Profile().
func handlers() map[string]provider.Handler {
	return map[string]provider.Handler{
		PatternAnswer: handleAnswer,
		PatternStatus: handleStatus,
	}
}

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

// handleStatus serves GET /v1/status. It carries no request body to
// validate, so its order is shorter than handleAnswer's: authenticate, then
// select and render.
func handleStatus(x *provider.Exchange) provider.Response {
	checkAuth(x)
	if x.Failed() {
		return errorResponse(x)
	}

	projection, ok := selectProjection(x)
	if !ok {
		return errorResponse(x)
	}

	body, err := renderStatus(x, projection)
	if err != nil {
		// Unreachable for the same reason as handleAnswer's own render call.
		x.Fail(CodeProjectionInvalid, "", "rendering status: %v", err)
		return errorResponse(x)
	}

	return provider.Response{
		Status:        http.StatusOK,
		Body:          body,
		Label:         string(Name) + ".status.ok",
		FaultEligible: true,
		FaultBody:     faultBody,
	}
}

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

// projectionPath addresses a turn's respond body the way a scenario finding
// does, so a request-time resolution warning reads like the startup one.
func projectionPath(entry *scenario.ProviderEntry, index int) string {
	return fmt.Sprintf("providers.%s.turns[%d].respond", entry.Name, index)
}
