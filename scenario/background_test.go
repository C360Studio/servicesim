package scenario

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// backgroundScenario spells an entry whose `background:` block is body, indented
// under the block, beside a one-turn synchronous script.
func backgroundScenario(body string) string {
	return "version: 1\nname: n\nproviders:\n  perplexity_agent:\n    background:\n" + body +
		"    turns:\n      - respond: {answer: sync}\n"
}

// The block decodes into the entry: a script of retrieve snapshots, each turn
// normalised the way `turns:` is — and, unlike a cancel turn, a turn may carry a
// fault plan, because the retrieve route reads its plan from these turns.
func TestBackgroundBlockDecodes(t *testing.T) {
	t.Parallel()

	s, report, err := Parse([]byte(backgroundScenario(
		"      turns:\n" +
			"        - when: {call_index: 0}\n" +
			"          fault: {attempts: [{status: 503}, {}]}\n" +
			"          respond: {status: queued}\n" +
			"        - {}\n")))
	if err != nil {
		t.Fatalf("Parse: %v (%+v)", err, report.Findings)
	}

	e := s.Provider("perplexity_agent")
	if e.Background == nil {
		t.Fatal("background: did not decode onto the entry")
	}
	if got := len(e.Background.Turns); got != 2 {
		t.Fatalf("background.turns = %d, want 2", got)
	}
	first := e.Background.Turns[0]
	if w := first.When; w == nil || w.CallIndex == nil || *w.CallIndex != 0 {
		t.Errorf("background.turns[0].when = %+v, want call_index 0", w)
	}
	if got := len(first.Fault.Attempts); got != 2 {
		t.Errorf("background.turns[0].fault attempts = %d, want 2", got)
	}
	var body map[string]any
	if err := first.Respond.Decode(&body); err != nil || body["status"] != "queued" {
		t.Errorf("background.turns[0].respond = %v (err %v), want status queued", body, err)
	}
	if e.Background.Turns[1].Respond.Kind != yaml.MappingNode {
		t.Errorf("a background turn with no respond must normalise to an empty mapping, got kind %v",
			e.Background.Turns[1].Respond.Kind)
	}
	if len(e.Turns) != 1 {
		t.Errorf("the synchronous script must be untouched by background:, got %d turns", len(e.Turns))
	}
	if e.Cancel != nil {
		t.Error("background: must not populate the entry's cancel: block")
	}
}

// `background` is an envelope key: in the single-shot form it is stripped from
// the projection body rather than handed to the provider as a response field.
func TestBackgroundIsNotAProjectionKey(t *testing.T) {
	t.Parallel()

	s, report, err := Parse([]byte("version: 1\nname: n\nproviders:\n  perplexity_agent:\n" +
		"    background:\n      turns:\n        - respond: {status: completed}\n" +
		"    answer: sync\n"))
	if err != nil {
		t.Fatalf("Parse: %v (%+v)", err, report.Findings)
	}
	var body map[string]any
	if err := s.Provider("perplexity_agent").Turns[0].Respond.Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, leaked := body["background"]; leaked {
		t.Errorf("background: leaked into the projection body: %v", body)
	}
	if body["answer"] != "sync" {
		t.Errorf("the projection body lost its own key: %v", body)
	}
}

// An empty `background:` is a declared block with nothing in it, not an absent
// one: the framework must still see it, both to reject it on an entry with no
// background lifecycle and to refuse a script that can answer no retrieve.
func TestEmptyBackgroundBlockIsDeclared(t *testing.T) {
	t.Parallel()

	s, _, _ := Parse([]byte("version: 1\nname: n\nproviders:\n  exa:\n    background:\n"))
	if s == nil {
		t.Fatal("an envelope that decodes must return its scenario alongside the findings")
	}
	if s.Provider("exa").Background == nil {
		t.Error("an empty background: block must decode to a non-nil BackgroundPolicy")
	}
}

// The block is decoded strictly. A typo is a load error rather than a block that
// quietly does nothing — and so is `cancel:`, which is not served under
// background: yet and must not decode into something no route reads.
func TestBackgroundBlockDecodesStrictly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr []string
	}{
		{
			name:    "an unknown key",
			body:    "      turnz: []\n",
			wantErr: []string{"providers.perplexity_agent.background", "turnz", "scenario.BackgroundPolicy"},
		},
		{
			name: "a cancel block",
			body: "      cancel:\n        turns:\n          - respond: {status: cancelled}\n" +
				"      turns:\n        - respond: {status: queued}\n",
			wantErr: []string{"providers.perplexity_agent.background", "cancel", "scenario.BackgroundPolicy"},
		},
		{
			name:    "a block-level fault",
			body:    "      fault: {attempts: [{status: 503}]}\n      turns:\n        - respond: {status: queued}\n",
			wantErr: []string{"providers.perplexity_agent.background", "fault", "scenario.BackgroundPolicy"},
		},
		{
			name:    "not a mapping",
			body:    "      - respond: {status: queued}\n",
			wantErr: []string{"providers.perplexity_agent.background"},
		},
		{
			name:    "turns that are not a list",
			body:    "      turns: {respond: {status: queued}}\n",
			wantErr: []string{"providers.perplexity_agent.background.turns"},
		},
		{
			name:    "an unknown key on a background turn",
			body:    "      turns:\n        - respnd: {status: queued}\n",
			wantErr: []string{"providers.perplexity_agent.background.turns[0]"},
		},
		{
			name:    "an unknown key in a background turn's fault",
			body:    "      turns:\n        - fault: {attempts: [{status: 503}], aftr: success}\n",
			wantErr: []string{"providers.perplexity_agent.background.turns[0]"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := Parse([]byte(backgroundScenario(tc.body)))
			if err == nil {
				t.Fatal("expected a load error")
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
	}
}

// Every background turn goes through the same checks as its counterpart on the
// entry — its fault plan included — addressed by its own path, and a block with
// no turns is an error rather than a job whose every retrieve 404s at runtime.
func TestBackgroundBlockIsValidated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		wantCode string
		wantPath string
	}{
		{
			name:     "an empty turns list",
			body:     "      turns: []\n",
			wantCode: "scenario.provider.background.turns.empty",
			wantPath: "providers.perplexity_agent.background.turns",
		},
		{
			name:     "a block with no turns key",
			body:     "",
			wantCode: "scenario.provider.background.turns.empty",
			wantPath: "providers.perplexity_agent.background.turns",
		},
		{
			name:     "an empty fault on a background turn",
			body:     "      turns:\n        - fault: {attempts: []}\n          respond: {status: queued}\n",
			wantCode: "scenario.fault.attempts.empty",
			wantPath: "providers.perplexity_agent.background.turns[0].fault.attempts",
		},
		{
			name:     "an unknown fault kind on a background turn",
			body:     "      turns:\n        - fault: {attempts: [{kind: explode}]}\n          respond: {status: queued}\n",
			wantCode: "scenario.fault.kind.unknown",
			wantPath: "providers.perplexity_agent.background.turns[0].fault.attempts[0].kind",
		},
		{
			name: "accepted on a background attempt that delivers its body",
			body: "      turns:\n        - fault: {attempts: [{status: 200, accepted: true}]}\n" +
				"          respond: {status: queued}\n",
			wantCode: CodeAcceptedRedundant,
			wantPath: "providers.perplexity_agent.background.turns[0].fault.attempts[0].accepted",
		},
		{
			name: "an unconditional background turn before another",
			body: "      turns:\n        - respond: {status: queued}\n" +
				"        - respond: {status: completed}\n",
			wantCode: "scenario.turn.unreachable",
			wantPath: "providers.perplexity_agent.background.turns[0]",
		},
		{
			name:     "a negative call_index",
			body:     "      turns:\n        - when: {call_index: -1}\n          respond: {status: queued}\n",
			wantCode: "scenario.turn.when.invalid",
			wantPath: "providers.perplexity_agent.background.turns[0].when.call_index",
		},
		{
			name:     "a respond that is not a mapping",
			body:     "      turns:\n        - respond: queued\n",
			wantCode: "scenario.turn.respond.not_mapping",
			wantPath: "providers.perplexity_agent.background.turns[0].respond",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			found, err := findingsWith(t, backgroundScenario(tc.body), tc.wantCode)
			if err == nil || len(found) != 1 {
				t.Fatalf("expected one %s error, got err=%v findings=%+v", tc.wantCode, err, found)
			}
			if found[0].Severity != SeverityError {
				t.Errorf("severity = %q, want error", found[0].Severity)
			}
			if found[0].Path != tc.wantPath {
				t.Errorf("path = %q, want %q", found[0].Path, tc.wantPath)
			}
		})
	}
}

// A background turn's fault plan is a fault plan: without counting it, a
// scenario whose only fault is on a background turn would skip the
// deps.faults_ignored warning and the scripted retrieve fault would silently
// never fire.
func TestHasFaultsCountsABackgroundTurnPlan(t *testing.T) {
	t.Parallel()

	s, report, err := Parse([]byte(backgroundScenario(
		"      turns:\n        - fault: {attempts: [{status: 503}]}\n          respond: {status: queued}\n")))
	if err != nil {
		t.Fatalf("Parse: %v (%+v)", err, report.Findings)
	}
	if !s.HasFaults() {
		t.Error("HasFaults() = false for a scenario whose only plan is on a background turn")
	}

	clean, report, err := Parse([]byte(backgroundScenario("      turns:\n        - respond: {status: queued}\n")))
	if err != nil {
		t.Fatalf("Parse: %v (%+v)", err, report.Findings)
	}
	if clean.HasFaults() {
		t.Error("HasFaults() = true for a scenario that declares no plan at all")
	}
}

// Anchors and aliases resolve inside a background block exactly as they do
// anywhere else under providers: — a snapshot aliased across background turns,
// and a whole background block aliased onto a second entry — so the retained
// nodes are plain mappings, never AliasNodes a later strict decode would choke on.
func TestBackgroundBlockResolvesAnchorsAndAliases(t *testing.T) {
	t.Parallel()

	src := `version: 1
name: background-aliases
providers:
  perplexity_agent:
    background: &lifecycle
      turns:
        - when: {call_index: 0}
          respond: &pending
            status: queued
        - when: {call_index: 1}
          respond: *pending
        - respond:
            status: completed
            answer: Done.
  perplexity_agent_b:
    kind: perplexity_agent
    background: *lifecycle
`
	s, report, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v (%+v)", err, report.Findings)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("unexpected findings: %+v", report.Findings)
	}

	type snapshot struct {
		Status string `yaml:"status"`
		Answer string `yaml:"answer"`
	}
	for _, name := range []string{"perplexity_agent", "perplexity_agent_b"} {
		e := s.Provider(name)
		if e.Background == nil || len(e.Background.Turns) != 3 {
			t.Fatalf("%s: background = %+v, want three turns", name, e.Background)
		}
		for i, want := range []string{"queued", "queued", "completed"} {
			turn := e.Background.Turns[i]
			if turn.Respond.Kind != yaml.MappingNode {
				t.Fatalf("%s: background.turns[%d].respond.Kind = %v, want MappingNode", name, i, turn.Respond.Kind)
			}
			var got snapshot
			if err := turn.DecodeProjection(name, i, &got); err != nil {
				t.Fatalf("%s: background.turns[%d]: %v", name, i, err)
			}
			if got.Status != want {
				t.Errorf("%s: background.turns[%d].status = %q, want %q", name, i, got.Status, want)
			}
		}
	}
}
