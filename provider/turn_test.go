package provider

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/scenario"
)

const scriptedYAML = `
version: 1
name: scripted
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    turns:
      - when:
          call_index: 0
        respond:
          answer: searching
      - when:
          body_contains: report-a
        respond:
          answer: found it
      - when:
          body_json:
            model: sonar-pro
        respond:
          answer: pro
      - respond:
          answer: I do not know
`

const singleShotYAML = `
version: 1
name: single-shot
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    fault:
      attempts:
        - status: 429
          repeat: 2
    results:
      - source: source-a
`

const noFallbackYAML = `
version: 1
name: no-fallback
providers:
  exa:
    turns:
      - when:
          call_index: 0
        respond:
          answer: only once
`

func TestSelectTurn(t *testing.T) {
	t.Parallel()

	scripted := mustScenario(t, scriptedYAML).Provider("exa")
	single := mustScenario(t, singleShotYAML).Provider("exa")
	none := mustScenario(t, noFallbackYAML).Provider("exa")

	tests := []struct {
		name      string
		entry     *scenario.ProviderEntry
		callIndex int
		body      string
		wantIndex int
		wantErr   bool
	}{
		{
			name:  "a single-shot block is one unconditional turn",
			entry: single, callIndex: 0, wantIndex: 0,
		},
		{
			name:  "and it keeps matching on every later call",
			entry: single, callIndex: 7, wantIndex: 0,
		},
		{
			name:  "call_index selects in declaration order",
			entry: scripted, callIndex: 0, wantIndex: 0,
		},
		{
			name:  "body_contains matches the raw body",
			entry: scripted, callIndex: 1, body: `{"q":"report-a"}`, wantIndex: 1,
		},
		{
			name:  "body_json matches a decoded field",
			entry: scripted, callIndex: 2, body: `{"model":"sonar-pro"}`, wantIndex: 2,
		},
		{
			name:  "the unconditional turn is the fallback",
			entry: scripted, callIndex: 3, body: `{"model":"sonar"}`, wantIndex: 3,
		},
		{
			name:  "an earlier match wins over a later one",
			entry: scripted, callIndex: 0, body: `{"q":"report-a"}`, wantIndex: 0,
		},
		{
			name:  "no match and no fallback is an error, never an empty 200",
			entry: none, callIndex: 1, wantErr: true,
		},
		{name: "a nil entry is an error", entry: nil, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			turn, index, err := SelectTurn(tc.entry, tc.callIndex, []byte(tc.body))
			if tc.wantErr {
				require.ErrorIs(t, err, ErrNoMatchingTurn)
				require.Nil(t, turn)
				require.Equal(t, -1, index)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, turn)
			require.Equal(t, tc.wantIndex, index)
			require.Same(t, &tc.entry.Turns[tc.wantIndex], turn)
		})
	}
}

func TestSelectTurnForSharesTheFaultCounter(t *testing.T) {
	t.Parallel()

	// One counter, not two. A scenario that rate-limits call 2 and answers
	// differently on call 3 stays coherent only because these are the same claim.
	entry := mustScenario(t, scriptedYAML).Provider("exa")
	engine := &countingFaults{}
	d := Deps{Faults: engine}.Normalized()

	calls := []struct {
		raw      string
		wantTurn int
	}{
		{raw: `{"q":"anything"}`, wantTurn: 0},      // call 0 matches when.call_index: 0
		{raw: `{"q":"report-a"}`, wantTurn: 1},      // call 1 matches when.body_contains
		{raw: `{"model":"sonar-pro"}`, wantTurn: 2}, // call 2 matches when.body_json
		{raw: `{"q":"anything"}`, wantTurn: 3},      // call 3 falls through to the fallback
	}

	for callIndex, c := range calls {
		x := &Exchange{
			Deps: d, Provider: Exa, Route: testRoute,
			Raw:      []byte(c.raw),
			decision: FaultDecision{Index: -1},
		}
		_, index := SelectTurnFor(x, entry)
		require.Equal(t, c.wantTurn, index, "call %d must select turn %d", callIndex, c.wantTurn)
		require.Equal(t, callIndex, x.Fault().Index, "the turn cursor and the fault index are one value")
	}
	require.Equal(t, len(calls), engine.calls, "one claim per request — never two counters")
}

func TestSelectTurnForRecordsAFindingWhenNothingMatches(t *testing.T) {
	t.Parallel()

	entry := mustScenario(t, noFallbackYAML).Provider("exa")
	d := Deps{Faults: &scriptedFaults{}}.Normalized()

	x := &Exchange{Deps: d, Provider: Exa, Route: testRoute, decision: FaultDecision{Index: -1}}
	_ = x.Fault() // claim 0
	x.claimed = true
	x.decision = FaultDecision{Index: 1, Key: testRoute.FaultKey}

	turn, index := SelectTurnFor(x, entry)
	require.Nil(t, turn)
	require.Equal(t, -1, index)
	require.True(t, x.Failed())
	require.Equal(t, CodeNoMatchingTurn, x.Findings()[0].Code)
	require.Equal(t, journal.SeverityError, x.Findings()[0].Severity)
}

func TestTurnFault(t *testing.T) {
	t.Parallel()

	single := mustScenario(t, singleShotYAML)
	scripted := mustScenario(t, scriptedYAML)

	plan := TurnFault(single, "exa")
	require.NotNil(t, plan, "a single-shot block's fault is its one turn's fault")
	require.Len(t, plan.Attempts, 1)
	require.Equal(t, http.StatusTooManyRequests, plan.Attempts[0].Status)
	require.Equal(t, 2, plan.Attempts[0].Repeats())

	require.Nil(t, TurnFault(scripted, "exa"), "a script with no fault declares no plan")
	require.Nil(t, TurnFault(single, "tavily"), "an undeclared provider has no plan")
	require.Nil(t, TurnFault(nil, "exa"), "nil-safe on every hop, so a route selector stays a one-liner")
}

// --- provider.ValidateScenario ---------------------------------------------

// recordingValidator stands in for a provider package's own projection checks.
type recordingValidator struct {
	seen     []string
	findings []scenario.Finding
}

func (v *recordingValidator) ValidateProjections(_ *scenario.Scenario, e *scenario.ProviderEntry) []scenario.Finding {
	v.seen = append(v.seen, e.Name)
	return v.findings
}

func TestValidateScenario(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: mixed
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  exa:
    results:
      - source: source-a
  openai:
    kind: openai
    turns:
      - respond:
          content: hello
`
	s := mustScenario(t, src)
	v := &recordingValidator{findings: []scenario.Finding{{
		Severity: scenario.SeverityError,
		Code:     "exa.result.source.unknown",
		Path:     "providers.exa.turns[0].respond.results[0].source",
		Message:  "no such source",
	}}}

	findings := ValidateScenario(s, map[string]Validator{"exa": v})

	require.Equal(t, []string{"exa"}, v.seen, "only registered kinds are asked")
	require.Len(t, findings, 2)

	// Declaration order, never map order: a readiness failure that reordered its
	// own reasons between runs would be miserable to diff.
	require.Equal(t, "exa.result.source.unknown", findings[0].Code)
	require.Equal(t, scenario.SeverityError, findings[0].Severity)

	require.Equal(t, CodeProviderUnimplemented, findings[1].Code)
	require.Equal(t, scenario.SeverityWarning, findings[1].Severity,
		"an unimplemented provider must never stop a shared scenario from loading")
	require.Contains(t, findings[1].Message, "openai")
	require.Equal(t, "providers.openai", findings[1].Path)

	require.True(t, s.Provider("exa").Implemented)
	require.False(t, s.Provider("openai").Implemented)

	require.Nil(t, ValidateScenario(nil, map[string]Validator{}))
}

func TestValidateScenarioKeysOnKindNotName(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: two-instances
providers:
  openai:
    kind: openai
    turns:
      - respond: {content: a}
  openai_fallback:
    kind: openai
    turns:
      - respond: {content: b}
`
	s := mustScenario(t, src)
	v := &recordingValidator{}

	findings := ValidateScenario(s, map[string]Validator{"openai": v})

	require.Empty(t, findings, "one implementation serves both instances")
	require.Equal(t, []string{"openai", "openai_fallback"}, v.seen)
}
