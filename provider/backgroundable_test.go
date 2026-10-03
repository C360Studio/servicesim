package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/scenario"
)

// strayBackground scripts a background: block on the acme entry.
const strayBackground = `
version: 1
name: stray-background
providers:
  acme:
    background:
      turns:
        - respond: {status: completed}
    turns:
      - respond: {answer: a}
`

// TestValidateScenarioRejectsBackgroundUnlessTheProfileOptsIn is the framework's
// background rule, the sibling of the cancel rule. A `background:` block can only
// take effect on an entry whose profile mints and retrieves background jobs, and
// only the profile knows which entries those are — so the framework rejects the
// block on every entry the profile has not named in Profile.Backgroundable. The
// default is the rejection: a profile that never thought about background runs
// fails closed without a line of code.
func TestValidateScenarioRejectsBackgroundUnlessTheProfileOptsIn(t *testing.T) {
	t.Parallel()

	optedIn := acmeProfile(okHandler(`{}`))
	optedIn.Backgroundable = []string{"acme"}

	cancelOnly := acmeProfile(okHandler(`{}`))
	cancelOnly.Cancellable = []string{"acme"}

	tests := []struct {
		name         string
		validators   map[string]Validator
		wantRejected bool
	}{
		{
			name:         "a profile that names no backgroundable entry",
			validators:   MustSet(acmeProfile(okHandler(`{}`))).Validators(),
			wantRejected: true,
		},
		{
			name:       "a profile that opts the entry in",
			validators: MustSet(optedIn).Validators(),
		},
		{
			// The two opt-ins are independent: a cancel lifecycle says nothing
			// about a background one.
			name:         "a profile that opts the entry in to cancel only",
			validators:   MustSet(cancelOnly).Validators(),
			wantRejected: true,
		},
		{
			// The opt-in reaches ValidateScenario only through Set.Validators. A
			// map built by hand carries no opt-in, so it fails closed.
			name:         "a validator map built by hand",
			validators:   map[string]Validator{"acme": stubValidator{}},
			wantRejected: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings := ValidateScenario(mustScenario(t, strayBackground), tc.validators)
			if !tc.wantRejected {
				assert.Empty(t, findings)
				return
			}
			require.Len(t, findings, 1, "%+v", findings)
			f := findings[0]
			assert.Equal(t, CodeBackgroundUnsupported, f.Code)
			assert.Equal(t, scenario.SeverityError, f.Severity, "a block that can never take effect is a load error")
			assert.Equal(t, "providers.acme.background", f.Path)
			assert.Contains(t, f.Message, `"acme"`, "the message names the entry")
			assert.Contains(t, f.Message, "no background lifecycle")
			assert.Contains(t, f.Message, "provider.Profile.Backgroundable", "the message names the opt-in")
		})
	}
}

// An entry with no registered handler is reported as unimplemented, a warning,
// and nothing more, background block or not.
func TestValidateScenarioLeavesAnUnimplementedEntrysBackgroundAlone(t *testing.T) {
	t.Parallel()

	findings := ValidateScenario(mustScenario(t, strayBackground), map[string]Validator{})
	require.Len(t, findings, 1)
	assert.Equal(t, CodeProviderUnimplemented, findings[0].Code)
}

// The framework route-checks an entry's turns against the routes its validator
// lists, but never its background.turns: the two scripts are selected by
// different routes (a create and a retrieve), so one list for both would let a
// route that never selects a script load clean in it. Checking background
// routes is the opted-in profile's own validator's job.
func TestValidateScenarioDoesNotRouteCheckABackgroundScript(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: background-routes
providers:
  acme:
    background:
      turns:
        - when: {route: answer}
          respond: {status: queued}
        - when: {route: not-a-route-the-lister-names}
          respond: {status: in_progress}
        - respond: {status: completed}
    turns:
      - when: {route: answr}
        respond: {answer: a}
      - respond: {answer: b}
`
	lister := &routeListingValidator{routes: []Route{{Pattern: "POST /v1/answer", FaultKey: "acme:answer"}}}
	p := acmeProfile(okHandler(`{}`))
	p.Validators = map[string]Validator{"acme": lister}
	p.Backgroundable = []string{"acme"}

	findings := ValidateScenario(mustScenario(t, src), MustSet(p).Validators())
	require.Len(t, findings, 1, "only the entry's own turn is route-checked: %+v", findings)
	assert.Equal(t, CodeTurnRouteUnknown, findings[0].Code)
	assert.Equal(t, "providers.acme.turns[0].when.route", findings[0].Path)
	assert.Equal(t, []string{"acme"}, lister.seen,
		"the profile's own validator still sees the entry, background block and all")
}

// A kind may be opted in to both lifecycles — an out-of-tree profile may well do
// it — and the two marks may nest either way. Either order must accept both
// blocks and still find the RouteLister underneath, so the entry's own turns
// keep their route check.
func TestValidateScenarioUnwrapsBothOptInsInEitherOrder(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: both-lifecycles
providers:
  acme:
    cancel:
      turns:
        - respond: {status: cancelled}
    background:
      turns:
        - respond: {status: completed}
    turns:
      - when: {route: answr}
        respond: {answer: a}
      - respond: {answer: b}
`
	routes := []Route{{Pattern: "POST /v1/answer", FaultKey: "acme:answer"}}

	both := acmeProfile(okHandler(`{}`))
	both.Validators = map[string]Validator{"acme": &routeListingValidator{routes: routes}}
	both.Cancellable = []string{"acme"}
	both.Backgroundable = []string{"acme"}

	tests := []struct {
		name       string
		validators map[string]Validator
	}{
		{name: "through Set.Validators", validators: MustSet(both).Validators()},
		{
			name: "cancel mark outermost",
			validators: map[string]Validator{
				"acme": cancellable{backgroundable{&routeListingValidator{routes: routes}}},
			},
		},
		{
			name: "background mark outermost",
			validators: map[string]Validator{
				"acme": backgroundable{cancellable{&routeListingValidator{routes: routes}}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings := ValidateScenario(mustScenario(t, src), tc.validators)
			require.Len(t, findings, 1, "neither block is rejected, and only the route typo is reported: %+v", findings)
			assert.Equal(t, CodeTurnRouteUnknown, findings[0].Code)
			assert.Equal(t, "providers.acme.turns[0].when.route", findings[0].Path)
		})
	}
}

// Backgroundable is checked at registration like Cancellable: a name that is not
// one of the profile's own entry kinds opts in nothing and is almost certainly a
// typo, so NewSet refuses it rather than leave the real entry rejecting every
// background: block for a reason nobody can see.
func TestNewSetRefusesABackgroundableEntryTheProfileDoesNotOwn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		validators     map[string]Validator
		backgroundable []string
		wantErr        string
	}{
		{name: "the profile's own kind", backgroundable: []string{"acme"}},
		{
			name:           "an entry kind the profile declares",
			validators:     map[string]Validator{"acme": stubValidator{}, "acme_agent": stubValidator{}},
			backgroundable: []string{"acme_agent"},
		},
		{name: "a typo", backgroundable: []string{"acme_agnet"}, wantErr: `"acme_agnet"`},
		{name: "an empty name", backgroundable: []string{""}, wantErr: `""`},
		{
			// With Validators declared, the profile owns exactly their keys.
			name:           "the profile's name when it declares other entries",
			validators:     map[string]Validator{"acme_agent": stubValidator{}},
			backgroundable: []string{"acme"},
			wantErr:        `"acme"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := acmeProfile(okHandler(`{}`))
			p.Validators = tc.validators
			p.Backgroundable = tc.backgroundable
			_, err := NewSet(p)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "Backgroundable")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// The Set keeps its own copy of Backgroundable, like every other slice field, in
// both directions.
func TestSetClonesBackgroundable(t *testing.T) {
	t.Parallel()

	p := acmeProfile(okHandler(`{}`))
	p.Backgroundable = []string{"acme"}
	s := MustSet(p)
	p.Backgroundable[0] = "mutated"

	got := s.All()
	require.Equal(t, []string{"acme"}, got[0].Backgroundable)
	got[0].Backgroundable[0] = "mutated"
	assert.Empty(t, ValidateScenario(mustScenario(t, strayBackground), s.Validators()),
		"mutating what All returned must not reach the Set's opt-in")
}
