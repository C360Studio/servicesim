package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/scenario"
)

// strayBackgroundCancel scripts a cancel: block under the acme entry's
// background: block.
const strayBackgroundCancel = `
version: 1
name: stray-background-cancel
providers:
  acme:
    background:
      turns:
        - respond: {status: completed}
      cancel:
        fault: {attempts: [{status: 500}]}
        turns:
          - respond: {status: cancelled}
    turns:
      - respond: {answer: a}
`

// TestValidateScenarioRejectsABackgroundCancelUnlessTheProfileOptsIn is the
// framework's background-cancel rule, the third sibling of the cancel and
// background rules. A `cancel:` under `background:` can only take effect on an
// entry whose profile serves a cancel of a background job, and only the
// profile knows which entries those are — so the framework rejects the nested
// block on every entry the profile has not named in
// Profile.BackgroundCancellable. Opting in to the background lifecycle, or to
// an entry-level cancel, is not opting in to it.
func TestValidateScenarioRejectsABackgroundCancelUnlessTheProfileOptsIn(t *testing.T) {
	t.Parallel()

	backgroundOnly := acmeProfile(okHandler(`{}`))
	backgroundOnly.Backgroundable = []string{"acme"}

	backgroundAndCancel := acmeProfile(okHandler(`{}`))
	backgroundAndCancel.Backgroundable = []string{"acme"}
	backgroundAndCancel.Cancellable = []string{"acme"}

	optedIn := acmeProfile(okHandler(`{}`))
	optedIn.Backgroundable = []string{"acme"}
	optedIn.BackgroundCancellable = []string{"acme"}

	tests := []struct {
		name         string
		validators   map[string]Validator
		wantRejected bool
	}{
		{name: "a profile that opts the entry in to background only", validators: MustSet(backgroundOnly).Validators(),
			wantRejected: true},
		{
			// The entry-level cancel opt-in says nothing about a cancel of a
			// background job.
			name:         "a profile that opts the entry in to background and to an entry-level cancel",
			validators:   MustSet(backgroundAndCancel).Validators(),
			wantRejected: true,
		},
		{name: "a profile that opts the entry in to both", validators: MustSet(optedIn).Validators()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings := ValidateScenario(mustScenario(t, strayBackgroundCancel), tc.validators)
			if !tc.wantRejected {
				assert.Empty(t, findings)
				return
			}
			require.Len(t, findings, 1, "%+v", findings)
			f := findings[0]
			assert.Equal(t, CodeBackgroundCancelUnsupported, f.Code)
			assert.Equal(t, scenario.SeverityError, f.Severity, "a block that can never take effect is a load error")
			assert.Equal(t, "providers.acme.background.cancel", f.Path)
			assert.Contains(t, f.Message, `"acme"`, "the message names the entry")
			assert.Contains(t, f.Message, "provider.Profile.BackgroundCancellable", "the message names the opt-in")
		})
	}
}

// On an entry with no background lifecycle at all, the background block is
// rejected and the cancel inside it goes with it: the one error that says the
// block can never run is the whole story, as it is for a rejected cancel
// script's routes.
func TestValidateScenarioRejectsABackgroundCancelWithItsBlock(t *testing.T) {
	t.Parallel()

	cancelOnly := acmeProfile(okHandler(`{}`))
	cancelOnly.Cancellable = []string{"acme"}

	for _, tc := range []struct {
		name       string
		validators map[string]Validator
	}{
		{"a profile that names no backgroundable entry", MustSet(acmeProfile(okHandler(`{}`))).Validators()},
		{"a profile that opts the entry in to cancel only", MustSet(cancelOnly).Validators()},
		// The opt-ins reach ValidateScenario only through Set.Validators. A map
		// built by hand carries none, so it fails closed.
		{"a validator map built by hand", map[string]Validator{"acme": stubValidator{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings := ValidateScenario(mustScenario(t, strayBackgroundCancel), tc.validators)
			require.Len(t, findings, 1, "%+v", findings)
			assert.Equal(t, CodeBackgroundUnsupported, findings[0].Code)
			assert.Equal(t, "providers.acme.background", findings[0].Path)
		})
	}
}

// A background block with no cancel in it needs no BackgroundCancellable: the
// opt-in is checked only when the nested block is there.
func TestValidateScenarioAcceptsABackgroundBlockWithoutACancel(t *testing.T) {
	t.Parallel()

	p := acmeProfile(okHandler(`{}`))
	p.Backgroundable = []string{"acme"}
	assert.Empty(t, ValidateScenario(mustScenario(t, strayBackground), MustSet(p).Validators()))
}

// All three marks may nest in any order, and ValidateScenario still accepts
// every block they opt in and finds the RouteLister underneath, so the entry's
// own turns keep their route check.
func TestValidateScenarioUnwrapsAllThreeOptInsInAnyOrder(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: every-lifecycle
providers:
  acme:
    cancel:
      turns:
        - respond: {status: cancelled}
    background:
      turns:
        - respond: {status: completed}
      cancel:
        turns:
          - respond: {status: cancelled}
    turns:
      - when: {route: answr}
        respond: {answer: a}
      - respond: {answer: b}
`
	routes := []Route{{Pattern: "POST /v1/answer", FaultKey: "acme:answer"}}
	lister := func() Validator { return &routeListingValidator{routes: routes} }

	all := acmeProfile(okHandler(`{}`))
	all.Validators = map[string]Validator{"acme": lister()}
	all.Cancellable = []string{"acme"}
	all.Backgroundable = []string{"acme"}
	all.BackgroundCancellable = []string{"acme"}

	for _, tc := range []struct {
		name       string
		validators map[string]Validator
	}{
		{"through Set.Validators", MustSet(all).Validators()},
		{"background-cancel mark outermost", map[string]Validator{
			"acme": backgroundCancellable{cancellable{backgroundable{lister()}}},
		}},
		{"background-cancel mark innermost", map[string]Validator{
			"acme": backgroundable{cancellable{backgroundCancellable{lister()}}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings := ValidateScenario(mustScenario(t, src), tc.validators)
			require.Len(t, findings, 1, "no block is rejected, and only the route typo is reported: %+v", findings)
			assert.Equal(t, CodeTurnRouteUnknown, findings[0].Code)
			assert.Equal(t, "providers.acme.turns[0].when.route", findings[0].Path)
		})
	}
}

// BackgroundCancellable is checked at registration like its siblings: a name
// that is not one of the profile's own entry kinds opts in nothing and is almost
// certainly a typo. So is a name the profile does not also list in
// Backgroundable: the block it opts in lives inside a background: block, which
// that entry rejects, so the name could never take effect either.
func TestNewSetRefusesABackgroundCancellableEntryThatOptsInNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		validators            map[string]Validator
		backgroundable        []string
		backgroundCancellable []string
		wantErr               []string
	}{
		{name: "the profile's own kind", backgroundable: []string{"acme"}, backgroundCancellable: []string{"acme"}},
		{
			name:                  "an entry kind the profile declares",
			validators:            map[string]Validator{"acme": stubValidator{}, "acme_agent": stubValidator{}},
			backgroundable:        []string{"acme_agent"},
			backgroundCancellable: []string{"acme_agent"},
		},
		{name: "a typo", backgroundable: []string{"acme"}, backgroundCancellable: []string{"acme_agnet"},
			wantErr: []string{`"acme_agnet"`, "not one of its entry kinds"}},
		{name: "an empty name", backgroundable: []string{"acme"}, backgroundCancellable: []string{""},
			wantErr: []string{`""`}},
		{
			name:                  "an entry with no background lifecycle",
			backgroundCancellable: []string{"acme"},
			wantErr:               []string{`"acme"`, "Backgroundable"},
		},
		{
			name:                  "an entry whose background lifecycle is another entry's",
			validators:            map[string]Validator{"acme": stubValidator{}, "acme_agent": stubValidator{}},
			backgroundable:        []string{"acme_agent"},
			backgroundCancellable: []string{"acme"},
			wantErr:               []string{`"acme"`, "Backgroundable"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := acmeProfile(okHandler(`{}`))
			p.Validators = tc.validators
			p.Backgroundable = tc.backgroundable
			p.BackgroundCancellable = tc.backgroundCancellable
			_, err := NewSet(p)
			if len(tc.wantErr) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "BackgroundCancellable")
			for _, want := range tc.wantErr {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// The Set keeps its own copy of BackgroundCancellable, like every other slice
// field, in both directions.
func TestSetClonesBackgroundCancellable(t *testing.T) {
	t.Parallel()

	p := acmeProfile(okHandler(`{}`))
	p.Backgroundable = []string{"acme"}
	p.BackgroundCancellable = []string{"acme"}
	s := MustSet(p)
	p.BackgroundCancellable[0] = "mutated"

	got := s.All()
	require.Equal(t, []string{"acme"}, got[0].BackgroundCancellable)
	got[0].BackgroundCancellable[0] = "mutated"
	assert.Empty(t, ValidateScenario(mustScenario(t, strayBackgroundCancel), s.Validators()),
		"mutating what All returned must not reach the Set's opt-in")
}

// TestBackgroundCancelFindingCodeIsTheDocumentedString pins the string itself.
// Every other test compares against the constant, so a changed string would
// break none of them, while a consumer filters on it.
func TestBackgroundCancelFindingCodeIsTheDocumentedString(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "scenario.provider.background_cancel_unsupported", CodeBackgroundCancelUnsupported)
}
