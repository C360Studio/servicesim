package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/scenario"
)

// mustScenario parses a scenario or fails the test. Tests build scenarios from
// YAML rather than struct literals because Providers is an open registry with
// unexported storage: YAML is the only way in, which is exactly how a consumer
// meets it.
func mustScenario(t *testing.T, src string) *scenario.Scenario {
	t.Helper()
	s, report, err := scenario.Parse([]byte(src))
	require.NoError(t, err, "scenario must parse: %v", report.Findings)
	return s
}

func newExchange(t *testing.T, src string) *Exchange {
	t.Helper()
	d := Deps{}
	if src != "" {
		d.Scenario = mustScenario(t, src)
	}
	return &Exchange{Deps: d.Normalized(), Provider: Exa}
}

func TestExchangeAccessors(t *testing.T) {
	t.Parallel()

	x := newExchange(t, "")
	x.Body = map[string]any{
		"query":      "climate",
		"numResults": float64(5),
		"livecrawl":  true,
		"contents":   map[string]any{"text": true},
		"null":       nil,
	}

	got, ok := x.String("query")
	require.True(t, ok)
	require.Equal(t, "climate", got)

	n, ok := x.Number("numResults")
	require.True(t, ok)
	require.InDelta(t, 5, n, 0)

	b, ok := x.Bool("livecrawl")
	require.True(t, ok)
	require.True(t, b)

	obj, ok := x.Object("contents")
	require.True(t, ok)
	require.Equal(t, map[string]any{"text": true}, obj)

	require.True(t, x.Has("null"), "a present null is present")
	require.False(t, x.Has("absent"))

	// A present-but-wrong-type value reports absent and records no finding: the
	// caller decides whether that is an error.
	_, ok = x.String("numResults")
	require.False(t, ok)
	require.Empty(t, x.Findings())
}

func TestExchangeAccessorsOnAbsentBody(t *testing.T) {
	t.Parallel()

	x := newExchange(t, "")
	_, ok := x.String("query")
	require.False(t, ok)
	require.False(t, x.Has("query"))
}

func TestExchangeFindingsTotalOrder(t *testing.T) {
	t.Parallel()

	// Perplexity's 422 body is built straight from this slice, so array order is
	// semantic and an unsorted list makes the body itself differ run to run.
	want := []Finding{
		{Severity: SeverityError, Code: "b.code", Field: "alpha", Message: "1"},
		{Severity: SeverityError, Code: "a.code", Field: "beta", Message: "2"},
		{Severity: SeverityWarning, Code: "z.code", Field: "alpha", Message: "3"},
		{Severity: SeverityWarning, Code: "y.code", Field: "zeta", Message: "4"},
	}

	for range 20 {
		x := newExchange(t, "")
		x.Warn("y.code", "zeta", "4")
		x.Fail("a.code", "beta", "2")
		x.Warn("z.code", "alpha", "3")
		x.Fail("b.code", "alpha", "1")

		require.Equal(t, want, x.Findings(), "findings must be in a total order every run")
	}
}

func TestExchangeValidationPolicy(t *testing.T) {
	t.Parallel()

	const strict = `
version: 1
name: strict
providers:
  exa:
    validation:
      strict: true
`
	const promote = `
version: 1
name: promote
providers:
  exa:
    validation:
      promote: [request.content_type]
`
	const demote = `
version: 1
name: demote
providers:
  exa:
    validation:
      demote: [request.malformed_json]
`
	const both = `
version: 1
name: both
providers:
  exa:
    validation:
      strict: true
      demote: [request.content_type]
`

	tests := []struct {
		name       string
		yaml       string
		wantWarnAs Severity
		wantFailAs Severity
		wantFailed bool
	}{
		{
			name:       "no policy leaves severities alone",
			yaml:       "",
			wantWarnAs: SeverityWarning,
			wantFailAs: SeverityError,
			wantFailed: true,
		},
		{
			name:       "strict promotes every warning",
			yaml:       strict,
			wantWarnAs: SeverityError,
			wantFailAs: SeverityError,
			wantFailed: true,
		},
		{
			name:       "promote lifts one code",
			yaml:       promote,
			wantWarnAs: SeverityError,
			wantFailAs: SeverityError,
			wantFailed: true,
		},
		{
			name:       "demote lowers one code",
			yaml:       demote,
			wantWarnAs: SeverityWarning,
			wantFailAs: SeverityWarning,
			wantFailed: false,
		},
		{
			name:       "demote beats strict",
			yaml:       both,
			wantWarnAs: SeverityWarning,
			wantFailAs: SeverityError,
			wantFailed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			x := newExchange(t, tc.yaml)
			x.Warn("request.content_type", "content-type", "missing")
			x.Fail("request.malformed_json", "", "not JSON")

			got := x.Findings()
			require.Len(t, got, 2)

			bySeverity := map[string]Severity{}
			for _, f := range got {
				bySeverity[f.Code] = f.Severity
			}
			require.Equal(t, tc.wantWarnAs, bySeverity["request.content_type"])
			require.Equal(t, tc.wantFailAs, bySeverity["request.malformed_json"])
			require.Equal(t, tc.wantFailed, x.Failed(),
				"Failed must read the policy-applied severity, or promote could never return a 4xx")
		})
	}
}

// TestAcceptedPlacements pins the precedence every provider package shares.
// Three copies of a rule about what authenticates would be three chances to
// disagree, and the disagreement would show up as a 401 against working client
// code — which is exactly the bug v0.1.1 shipped to fix.
func TestAcceptedPlacements(t *testing.T) {
	t.Parallel()

	providerDefault := []string{PlacementAuthorization, PlacementXAPIKey}

	tests := []struct {
		name   string
		route  []string
		policy scenario.AuthPolicy
		want   []string
	}{
		{
			name: "no route and no policy falls back to the provider default",
			want: providerDefault,
		},
		{
			name:  "a route speaks for itself",
			route: []string{PlacementAuthorization, PlacementBodyAPIKey},
			want:  []string{PlacementAuthorization, PlacementBodyAPIKey},
		},
		{
			name:   "a scenario overrides the provider default",
			policy: scenario.AuthPolicy{Headers: []string{PlacementAuthorization}},
			want:   []string{PlacementAuthorization},
		},
		{
			// The negative test the override exists for: "prove my client no
			// longer sends the key in the body". A route default that quietly
			// re-admitted it would make that assertion unwritable.
			name:   "a scenario overrides the route too",
			route:  []string{PlacementAuthorization, PlacementBodyAPIKey},
			policy: scenario.AuthPolicy{Headers: []string{PlacementAuthorization}},
			want:   []string{PlacementAuthorization},
		},
		{
			name:  "spelling is normalised, so a capitalised header still matches",
			route: []string{"X-API-Key", "  Authorization  "},
			want:  []string{PlacementXAPIKey, PlacementAuthorization},
		},
		{
			name:   "an empty entry in a policy is dropped rather than matching nothing",
			policy: scenario.AuthPolicy{Headers: []string{PlacementAuthorization, "", "  "}},
			want:   []string{PlacementAuthorization},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := &Exchange{Route: Route{Pattern: "POST /search", Credentials: tc.route}}
			require.Equal(t, tc.want, x.AcceptedPlacements(tc.policy, providerDefault))
		})
	}
}

// A route's Credentials must not be handed out as the live slice: a caller that
// appended to the result would rewrite the route table for every later request
// in the process.
func TestAcceptedPlacementsDoesNotAliasTheRoute(t *testing.T) {
	t.Parallel()

	route := Route{Credentials: []string{PlacementAuthorization}}
	x := &Exchange{Route: route}

	got := x.AcceptedPlacements(scenario.AuthPolicy{}, nil)
	got = append(got, "injected") //nolint:staticcheck // the append is the point

	require.Equal(t, []string{PlacementAuthorization}, route.Credentials)
	require.Equal(t, []string{PlacementAuthorization},
		x.AcceptedPlacements(scenario.AuthPolicy{}, nil))
}

func TestExchangeFaultClaimsOnce(t *testing.T) {
	t.Parallel()

	engine := &countingFaults{}
	x := &Exchange{
		Deps:     Deps{Faults: engine}.Normalized(),
		Provider: Exa,
		Route:    Route{Pattern: "POST /search", FaultKey: "exa:search"},
		decision: FaultDecision{Index: -1, Key: "exa:search"},
	}

	require.Equal(t, -1, x.decision.Index, "no attempt is claimed until asked for")
	require.Equal(t, 0, x.Fault().Index)
	require.Equal(t, 0, x.Fault().Index, "the claim is memoised, never repeated")
	require.Equal(t, 0, x.CallIndex(), "turn selection reads the same claim")
	require.Equal(t, 1, engine.calls, "one request claims exactly one attempt")
}

func TestNoopFaultsCountsSoTurnsWork(t *testing.T) {
	t.Parallel()

	// The turn cursor and the fault counter are the same counter. A substitute
	// that never counted would pin every zero-Deps request to one call index and
	// make `when: {call_index: 1}` unmatchable.
	d := Deps{}.Normalized()
	require.Equal(t, 0, d.Faults.Next("exa:search").Index)
	require.Equal(t, 1, d.Faults.Next("exa:search").Index)
	require.Equal(t, 0, d.Faults.Next("exa:answer").Index, "keys count independently")

	dec := d.Faults.Next("exa:search")
	require.False(t, dec.Planned, "no plan means the index never enters a derived identifier")
	require.False(t, dec.Unknown, "the no-op engine holds no plans, so nothing is missing")
	require.Nil(t, dec.Attempt)

	d.Faults.Reset()
	require.Equal(t, 0, d.Faults.Next("exa:search").Index)
}

func TestDepsNormalizedDefaults(t *testing.T) {
	t.Parallel()

	d := Deps{}.Normalized()
	require.NotNil(t, d.Scenario)
	require.NotNil(t, d.Journal)
	require.NotNil(t, d.Faults)
	require.NotNil(t, d.Logger)
	require.Equal(t, SystemClock{}, d.Clock)
	require.Equal(t, DefaultMaxRequestBytes, d.MaxRequestBytes)
	require.Equal(t, DefaultMaxJournalBodyBytes, d.MaxJournalBodyBytes)
	require.Equal(t, DefaultMaxNamespaces, d.MaxNamespaces)
	require.Equal(t, 7, Deps{MaxNamespaces: 7}.Normalized().MaxNamespaces,
		"an explicit bound is never overwritten by the default")

	// Idempotence is what lets NewMux normalise once and share one journal
	// sequence counter and one attempt counter across every route.
	again := d.Normalized()
	require.Same(t, d.Journal, again.Journal)
	require.Same(t, d.Faults, again.Faults)
	require.Same(t, d.Scenario, again.Scenario)
}

func TestDepsNormalizedWarnsWhenFaultsAreIgnored(t *testing.T) {
	t.Parallel()

	const faulted = `
version: 1
name: faulted
providers:
  exa:
    fault:
      attempts:
        - status: 429
`
	capture := newCapturingLogger()
	d := Deps{Scenario: mustScenario(t, faulted), Logger: capture.logger}
	_ = d.Normalized()

	require.Contains(t, capture.String(), "deps.faults_ignored",
		"a scenario with faults and no engine is a wiring mistake and must say so")

	quiet := newCapturingLogger()
	_ = Deps{Logger: quiet.logger}.Normalized()
	require.NotContains(t, quiet.String(), "deps.faults_ignored")
}
