package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/scenario"
)

// strayCancel scripts a cancel: block on the acme entry.
const strayCancel = `
version: 1
name: stray-cancel
providers:
  acme:
    cancel:
      turns:
        - respond: {status: cancelled}
    turns:
      - respond: {status: running}
`

// TestValidateScenarioRejectsCancelUnlessTheProfileOptsIn is the framework's
// cancel rule. A `cancel:` block can only take effect on an entry whose profile
// serves a cancel, and only the profile knows which entries those are — so the
// framework rejects the block on every entry the profile has not named in
// Profile.Cancellable. The default is the rejection: a profile written before
// cancels existed, or one that never thought about them, fails closed without a
// line of code.
func TestValidateScenarioRejectsCancelUnlessTheProfileOptsIn(t *testing.T) {
	t.Parallel()

	optedIn := acmeProfile(okHandler(`{}`))
	optedIn.Cancellable = []string{"acme"}

	tests := []struct {
		name         string
		validators   map[string]Validator
		wantRejected bool
	}{
		{
			name:         "a profile that names no cancellable entry",
			validators:   MustSet(acmeProfile(okHandler(`{}`))).Validators(),
			wantRejected: true,
		},
		{
			name:       "a profile that opts the entry in",
			validators: MustSet(optedIn).Validators(),
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

			findings := ValidateScenario(mustScenario(t, strayCancel), tc.validators)
			if !tc.wantRejected {
				assert.Empty(t, findings)
				return
			}
			require.Len(t, findings, 1, "%+v", findings)
			f := findings[0]
			assert.Equal(t, CodeCancelUnsupported, f.Code)
			assert.Equal(t, scenario.SeverityError, f.Severity, "a block that can never take effect is a load error")
			assert.Equal(t, "providers.acme.cancel", f.Path)
			assert.Contains(t, f.Message, `"acme"`, "the message names the entry")
			assert.Contains(t, f.Message, "no cancel lifecycle")
		})
	}
}

// An entry with no registered handler is reported as unimplemented, a warning,
// and nothing more: a scenario shared across repositories must not break on a
// build that does not serve one of its providers, cancel block or not.
func TestValidateScenarioLeavesAnUnimplementedEntrysCancelAlone(t *testing.T) {
	t.Parallel()

	findings := ValidateScenario(mustScenario(t, strayCancel), map[string]Validator{})
	require.Len(t, findings, 1)
	assert.Equal(t, CodeProviderUnimplemented, findings[0].Code)
}

// The cancel script of an opted-in entry is route-checked like its turns; a
// rejected block is not, because the one error that says it can never run is
// the whole story.
func TestValidateScenarioRouteChecksOnlyAnOptedInCancelScript(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: cancel-route-typo
providers:
  acme:
    cancel:
      turns:
        - when: {route: answr}
          respond: {status: running}
        - respond: {status: cancelled}
    turns:
      - respond: {status: running}
`
	lister := &routeListingValidator{routes: []Route{{Pattern: "POST /v1/answer", FaultKey: "acme:answer"}}}
	p := acmeProfile(okHandler(`{}`))
	p.Validators = map[string]Validator{"acme": lister}

	findings := ValidateScenario(mustScenario(t, src), MustSet(p).Validators())
	require.Len(t, findings, 1)
	assert.Equal(t, CodeCancelUnsupported, findings[0].Code)

	p.Cancellable = []string{"acme"}
	findings = ValidateScenario(mustScenario(t, src), MustSet(p).Validators())
	require.Len(t, findings, 1)
	assert.Equal(t, CodeTurnRouteUnknown, findings[0].Code)
	assert.Equal(t, "providers.acme.cancel.turns[0].when.route", findings[0].Path)
}

// Cancellable is checked at registration like every other Profile field: a
// name that is not one of the profile's own entry kinds opts in nothing and is
// almost certainly a typo, so NewSet refuses it rather than leave the real
// entry rejecting every cancel: block for a reason nobody can see.
func TestNewSetRefusesACancellableEntryTheProfileDoesNotOwn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		validators  map[string]Validator
		cancellable []string
		wantErr     string
	}{
		{name: "the profile's own kind", cancellable: []string{"acme"}},
		{
			name:        "an entry kind the profile declares",
			validators:  map[string]Validator{"acme": stubValidator{}, "acme_runs": stubValidator{}},
			cancellable: []string{"acme_runs"},
		},
		{name: "a typo", cancellable: []string{"acme_rnus"}, wantErr: `"acme_rnus"`},
		{name: "an empty name", cancellable: []string{""}, wantErr: `""`},
		{
			// With Validators declared, the profile owns exactly their keys.
			name:        "the profile's name when it declares other entries",
			validators:  map[string]Validator{"acme_runs": stubValidator{}},
			cancellable: []string{"acme"},
			wantErr:     `"acme"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := acmeProfile(okHandler(`{}`))
			p.Validators = tc.validators
			p.Cancellable = tc.cancellable
			_, err := NewSet(p)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "Cancellable")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// The Set keeps its own copy of Cancellable, like every other slice field.
func TestSetClonesCancellable(t *testing.T) {
	t.Parallel()

	p := acmeProfile(okHandler(`{}`))
	p.Cancellable = []string{"acme"}
	s := MustSet(p)
	p.Cancellable[0] = "mutated"

	got := s.All()
	require.Equal(t, []string{"acme"}, got[0].Cancellable)
	got[0].Cancellable[0] = "mutated"
	assert.Empty(t, ValidateScenario(mustScenario(t, strayCancel), s.Validators()),
		"mutating what All returned must not reach the Set's opt-in")
}
