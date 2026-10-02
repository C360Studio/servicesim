package scenario

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// cancelScenario spells an async entry whose `cancel:` block is body, indented
// under the block, beside a one-turn poll script.
func cancelScenario(body string) string {
	return "version: 1\nname: n\nproviders:\n  exa_agent_runs:\n    cancel:\n" + body +
		"    turns:\n      - respond: {status: completed}\n"
}

// The block decodes into the entry: a fault plan for the cancel route, and a
// script of poll snapshots served once a cancel is recorded, each turn
// normalised the way `turns:` is.
func TestCancelBlockDecodes(t *testing.T) {
	t.Parallel()

	s, report, err := Parse([]byte(cancelScenario(
		"      fault: {attempts: [{status: 500}, {}]}\n" +
			"      turns:\n" +
			"        - when: {call_index: 0}\n" +
			"          respond: {status: running}\n" +
			"        - {}\n")))
	if err != nil {
		t.Fatalf("Parse: %v (%+v)", err, report.Findings)
	}

	e := s.Provider("exa_agent_runs")
	if e.Cancel == nil {
		t.Fatal("cancel: did not decode onto the entry")
	}
	if got := len(e.Cancel.Fault.Attempts); got != 2 {
		t.Errorf("cancel.fault attempts = %d, want 2", got)
	}
	if got := len(e.Cancel.Turns); got != 2 {
		t.Fatalf("cancel.turns = %d, want 2", got)
	}
	if w := e.Cancel.Turns[0].When; w == nil || w.CallIndex == nil || *w.CallIndex != 0 {
		t.Errorf("cancel.turns[0].when = %+v, want call_index 0", w)
	}
	var first map[string]any
	if err := e.Cancel.Turns[0].Respond.Decode(&first); err != nil || first["status"] != "running" {
		t.Errorf("cancel.turns[0].respond = %v (err %v), want status running", first, err)
	}
	if e.Cancel.Turns[1].Respond.Kind != yaml.MappingNode {
		t.Errorf("a cancel turn with no respond must normalise to an empty mapping, got kind %v",
			e.Cancel.Turns[1].Respond.Kind)
	}
	if len(e.Turns) != 1 {
		t.Errorf("the poll script must be untouched by cancel:, got %d turns", len(e.Turns))
	}
}

// A cancel-only fault plan is a fault plan: without counting it, a scenario
// whose only fault is cancel.fault would skip the deps.faults_ignored warning.
func TestHasFaultsCountsACancelPlan(t *testing.T) {
	t.Parallel()

	s, _, err := Parse([]byte(cancelScenario("      fault: {attempts: [{status: 500}]}\n")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !s.HasFaults() {
		t.Error("HasFaults() = false for a scenario whose only plan is cancel.fault")
	}
}

// An empty `cancel:` is a declared block with nothing in it, not an absent one:
// the profile that owns the entry must still see it to reject it where no cancel
// exists.
func TestEmptyCancelBlockIsDeclared(t *testing.T) {
	t.Parallel()

	s, _, err := Parse([]byte("version: 1\nname: n\nproviders:\n  exa:\n    cancel:\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Provider("exa").Cancel == nil {
		t.Error("an empty cancel: block must decode to a non-nil CancelPolicy")
	}
}

// The block is decoded strictly, like create:. A typo is a load error rather
// than a block that quietly does nothing — and so is a fault plan written on a
// cancel turn, which no route would ever read.
func TestCancelBlockDecodesStrictly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "an unknown key",
			body:    "      turnz: []\n",
			wantErr: "providers.exa_agent_runs.cancel",
		},
		{
			name:    "not a mapping",
			body:    "      - respond: {status: cancelled}\n",
			wantErr: "providers.exa_agent_runs.cancel",
		},
		{
			name:    "an unknown key on a cancel turn",
			body:    "      turns:\n        - respnd: {status: cancelled}\n",
			wantErr: "providers.exa_agent_runs.cancel.turns[0]",
		},
		{
			name:    "an unknown key in cancel.fault",
			body:    "      fault: {attempts: [{status: 500}], aftr: success}\n",
			wantErr: "providers.exa_agent_runs.cancel.fault",
		},
		{
			name: "a fault plan on a cancel turn",
			body: "      turns:\n        - fault: {attempts: [{status: 503}]}\n" +
				"          respond: {status: cancelled}\n",
			wantErr: "providers.exa_agent_runs.cancel.turns[0].fault",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := Parse([]byte(cancelScenario(tc.body)))
			if err == nil {
				t.Fatal("expected a load error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not name %q", err, tc.wantErr)
			}
		})
	}
}

// cancel.fault and every cancel.turns[i] go through the same checks as their
// counterparts on the entry, addressed by their own paths.
func TestCancelBlockIsValidated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		wantCode string
		wantPath string
	}{
		{
			name:     "an empty cancel.fault",
			body:     "      fault: {attempts: []}\n",
			wantCode: "scenario.fault.attempts.empty",
			wantPath: "providers.exa_agent_runs.cancel.fault.attempts",
		},
		{
			name:     "an unknown kind in cancel.fault",
			body:     "      fault: {attempts: [{kind: explode}]}\n",
			wantCode: "scenario.fault.kind.unknown",
			wantPath: "providers.exa_agent_runs.cancel.fault.attempts[0].kind",
		},
		{
			name:     "accepted on an attempt that delivers its body",
			body:     "      fault: {attempts: [{status: 200, accepted: true}]}\n",
			wantCode: CodeAcceptedRedundant,
			wantPath: "providers.exa_agent_runs.cancel.fault.attempts[0].accepted",
		},
		{
			name: "an unconditional cancel turn before another",
			body: "      turns:\n        - respond: {status: running}\n" +
				"        - respond: {status: cancelled}\n",
			wantCode: "scenario.turn.unreachable",
			wantPath: "providers.exa_agent_runs.cancel.turns[0]",
		},
		{
			name:     "a negative call_index",
			body:     "      turns:\n        - when: {call_index: -1}\n          respond: {status: cancelled}\n",
			wantCode: "scenario.turn.when.invalid",
			wantPath: "providers.exa_agent_runs.cancel.turns[0].when.call_index",
		},
		{
			name:     "a respond that is not a mapping",
			body:     "      turns:\n        - respond: cancelled\n",
			wantCode: "scenario.turn.respond.not_mapping",
			wantPath: "providers.exa_agent_runs.cancel.turns[0].respond",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			found, err := findingsWith(t, cancelScenario(tc.body), tc.wantCode)
			if err == nil || len(found) != 1 {
				t.Fatalf("expected one %s error, got err=%v findings=%+v", tc.wantCode, err, found)
			}
			if found[0].Path != tc.wantPath {
				t.Errorf("path = %q, want %q", found[0].Path, tc.wantPath)
			}
		})
	}
}

// A cancel plan may carry accepted: true on an attempt that loses its body — a
// cancel that took effect and whose reply was lost — so it loads clean.
func TestCancelFaultAllowsAccepted(t *testing.T) {
	t.Parallel()

	src := cancelScenario("      fault: {attempts: [{kind: close_before_headers, accepted: true}]}\n" +
		"      turns:\n        - respond: {status: cancelled}\n")
	if _, report, err := Parse([]byte(src)); err != nil {
		t.Fatalf("expected a clean load, got %v (%+v)", err, report.Findings)
	}
}
