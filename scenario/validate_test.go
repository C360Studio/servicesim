package scenario

import (
	"strings"
	"testing"
)

// TestValidate_FailsClosed is the gate readiness depends on: a scenario that
// reaches a serving state must have no error findings, so every authoring
// mistake below has to be an error rather than a silent default.
func TestValidate_FailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		src      string
		wantCode string
		wantPath string
		wantMsg  string
	}{
		{
			name:     "unsupported version",
			src:      "version: 1\nname: n\n",
			wantCode: "", // the happy case; see the guard below
		},
		{
			name:     "missing name",
			src:      "version: 1\n",
			wantCode: "scenario.name.missing",
			wantPath: "name",
		},
		{
			name:     "source without id",
			src:      "version: 1\nname: n\nsources:\n  - {url: https://example.test/a, title: A}\n",
			wantCode: "scenario.source.id.missing",
			wantPath: "sources[0].id",
		},
		{
			name:     "source without url",
			src:      "version: 1\nname: n\nsources:\n  - {id: a, title: A}\n",
			wantCode: "scenario.source.url.missing",
			wantPath: "sources[0].url",
		},
		{
			name:     "source without title",
			src:      "version: 1\nname: n\nsources:\n  - {id: a, url: https://example.test/a}\n",
			wantCode: "scenario.source.title.missing",
			wantPath: "sources[0].title",
		},
		{
			name:     "duplicate source id",
			src:      "version: 1\nname: n\nsources:\n  - {id: a, url: https://example.test/a, title: A}\n  - {id: a, url: https://example.test/b, title: B}\n",
			wantCode: "scenario.source.id.duplicate",
			wantPath: "sources[1].id",
			wantMsg:  "sources[0]",
		},
		{
			name:     "claim without id",
			src:      "version: 1\nname: n\nsources:\n  - {id: a, url: https://example.test/a, title: A, claims: [{text: t}]}\n",
			wantCode: "scenario.claim.id.missing",
			wantPath: "sources[0].claims[0].id",
		},
		{
			name:     "unknown auth mode",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    auth: {mode: whatever}\n",
			wantCode: "scenario.auth.mode.unknown",
			wantPath: "providers.exa.auth.mode",
		},
		{
			name:     "unreachable turn",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    turns:\n      - respond: {a: 1}\n      - when: {call_index: 1}\n        respond: {a: 2}\n",
			wantCode: "scenario.turn.unreachable",
			wantPath: "providers.exa.turns[0]",
		},
		{
			name:     "negative call index",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    turns:\n      - when: {call_index: -1}\n        respond: {}\n",
			wantCode: "scenario.turn.when.invalid",
			wantPath: "providers.exa.turns[0].when.call_index",
		},
		{
			name:     "empty body_json path",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    turns:\n      - when:\n          body_json:\n            \"\": x\n        respond: {}\n",
			wantCode: "scenario.turn.when.invalid",
			wantPath: "providers.exa.turns[0].when.body_json",
		},
		{
			name:     "respond is not a mapping",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    turns:\n      - respond: [1, 2]\n",
			wantCode: "scenario.turn.respond.not_mapping",
			wantPath: "providers.exa.turns[0].respond",
			wantMsg:  "must be a mapping",
		},
		{
			name:     "projection body alongside turns",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    results: []\n    turns:\n      - respond: {}\n",
			wantCode: "scenario.provider.body_with_turns",
			wantPath: "providers.exa",
			wantMsg:  "results",
		},
		{
			name:     "block fault alongside turns",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    fault: {attempts: [{status: 500}]}\n    turns:\n      - respond: {}\n",
			wantCode: "scenario.provider.fault_with_turns",
			wantPath: "providers.exa.fault",
		},
		{
			name:     "fault with no attempts",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    fault: {attempts: []}\n",
			wantCode: "scenario.fault.attempts.empty",
			wantPath: "providers.exa.fault.attempts",
		},
		{
			name:     "unknown fault after",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    fault: {after: retry, attempts: [{status: 500}]}\n",
			wantCode: "scenario.fault.after.unknown",
			wantPath: "providers.exa.fault.after",
		},
		{
			name:     "unknown fault kind",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    fault: {attempts: [{kind: explode}]}\n",
			wantCode: "scenario.fault.kind.unknown",
			wantPath: "providers.exa.fault.attempts[0].kind",
		},
		{
			name:     "impossible status",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    fault: {attempts: [{status: 999}]}\n",
			wantCode: "scenario.fault.status.invalid",
			wantPath: "providers.exa.fault.attempts[0].status",
		},
		{
			name:     "after_chunk on a non-streaming kind",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    fault: {attempts: [{status: 500, after_chunk: 2}]}\n",
			wantCode: "scenario.fault.after_chunk.not_streaming",
			wantPath: "providers.exa.fault.attempts[0].after_chunk",
		},
		{
			name:     "after_chunk on stream_disconnect is not flagged",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    fault: {attempts: [{kind: stream_disconnect, after_chunk: 2}]}\n",
			wantCode: "", // the happy case; see the guard below
		},
		{
			name:     "negative repeat",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    fault: {attempts: [{status: 500, repeat: -2}]}\n",
			wantCode: "scenario.fault.repeat.negative",
			wantPath: "providers.exa.fault.attempts[0].repeat",
		},
		{
			name:     "negative retry_after",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    fault: {attempts: [{status: 429, retry_after: -1}]}\n",
			wantCode: "scenario.fault.retry_after.negative",
			wantPath: "providers.exa.fault.attempts[0].retry_after",
		},
		{
			name:     "negative delay",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    fault: {attempts: [{delay: -1s}]}\n",
			wantCode: "scenario.fault.delay.negative",
			wantPath: "providers.exa.fault.attempts[0].delay",
		},
		{
			name:     "unknown turn key extractor",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    turn_key: [phase_of_the_moon]\n",
			wantCode: "scenario.turn_key.invalid",
			wantPath: "providers.exa.turn_key[0]",
			wantMsg:  "route, body_json:<path> or header:<name>",
		},
		{
			name:     "turn key extractor with no argument",
			src:      "version: 1\nname: n\nproviders:\n  exa:\n    turn_key: [\"body_json:\"]\n",
			wantCode: "scenario.turn_key.invalid",
			wantPath: "providers.exa.turn_key[0]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, report, err := Parse([]byte(tc.src))
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("expected a valid scenario, got %v (%+v)", err, report.Findings)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected validation to fail; scenario = %+v", s)
			}
			var match *Finding
			for i := range report.Findings {
				if report.Findings[i].Code == tc.wantCode {
					match = &report.Findings[i]
					break
				}
			}
			if match == nil {
				t.Fatalf("no %s finding in %+v", tc.wantCode, report.Findings)
			}
			if match.Severity != SeverityError {
				t.Errorf("severity = %q, want error so readiness cannot succeed", match.Severity)
			}
			if tc.wantPath != "" && match.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", match.Path, tc.wantPath)
			}
			if tc.wantMsg != "" && !strings.Contains(match.Message, tc.wantMsg) {
				t.Errorf("message %q does not contain %q", match.Message, tc.wantMsg)
			}
			if match.Message == "" {
				t.Error("a finding with no message is not actionable")
			}
		})
	}
}

// TestValidate_TurnKeyNeedNotNameTheRoute records the decision that the route is
// always part of the lane. A turn_key declares discriminators additional to the
// route, so omitting "route" is not an authoring mistake and must raise nothing —
// and restating it, or restating it twice, must not either, because the lane key
// de-duplicates it rather than doubling it.
func TestValidate_TurnKeyNeedNotNameTheRoute(t *testing.T) {
	t.Parallel()

	for _, turnKey := range []string{
		`["body_json:model"]`,
		`["header:x-role"]`,
		`["route"]`,
		`["route", "route"]`,
		`["body_json:model", "route"]`,
	} {
		src := "version: 1\nname: n\nproviders:\n  exa:\n    turn_key: " + turnKey +
			"\n    turns:\n      - respond: {answer: only}\n"
		_, report, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("turn_key %s must load: %v (%+v)", turnKey, err, report.Findings)
		}
		if len(report.Findings) != 0 {
			t.Errorf("turn_key %s produced findings %+v", turnKey, report.Findings)
		}
	}
}

func TestValidate_UnsupportedVersionOnAnInMemoryScenario(t *testing.T) {
	t.Parallel()

	// Parse rejects a bad version before decoding, so this path is reachable only
	// for a scenario assembled in Go. It must still fail closed.
	s := &Scenario{Version: 7, Name: "n"}
	report := s.Validate()
	if report.OK() {
		t.Fatal("version 7 must not validate")
	}
	errs := report.Errors()
	if errs[0].Code != "scenario.version.unsupported" || !strings.Contains(errs[0].Message, "version 7") {
		t.Fatalf("finding = %+v", errs[0])
	}
	if report.Err() == nil || !strings.Contains(report.Err().Error(), "version") {
		t.Fatalf("Err() = %v", report.Err())
	}
}

func TestValidate_NonReservedURLIsOnlyAWarning(t *testing.T) {
	t.Parallel()

	s, report, err := Parse([]byte("version: 1\nname: n\nsources:\n  - {id: a, url: https://real-host.io/a, title: A}\n"))
	if err != nil {
		t.Fatalf("a non-reserved host must not block loading: %v", err)
	}
	warnings := report.Warnings()
	if len(warnings) != 1 || warnings[0].Code != "scenario.source.url.not_reserved" {
		t.Fatalf("warnings = %+v", report.Findings)
	}
	if warnings[0].Path != "sources[0].url" {
		t.Errorf("path = %q", warnings[0].Path)
	}
	if _, ok := s.SourceByID("a"); !ok {
		t.Error("the scenario should still have loaded")
	}

	for _, host := range []string{"https://example.test/a", "https://sub.example/a", "https://x.invalid/a", "https://example.com/a", "https://www.example.com/a"} {
		_, r, err := Parse([]byte("version: 1\nname: n\nsources:\n  - {id: a, url: " + host + ", title: A}\n"))
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if len(r.Warnings()) != 0 {
			t.Errorf("%s: unexpected warnings %+v", host, r.Warnings())
		}
	}
}

func TestValidate_UnknownProviderNameIsNotALoadError(t *testing.T) {
	t.Parallel()

	// A scenario shared across repositories must not break the moment one
	// consumer pins an older Servicesim that has never heard of "openai".
	s, report, err := Parse([]byte("version: 1\nname: n\nproviders:\n  openai:\n    turns:\n      - respond: {content: hi}\n"))
	if err != nil {
		t.Fatalf("an unknown provider name must load: %v (%+v)", err, report.Findings)
	}
	e := s.Providers.Get("openai")
	if e == nil {
		t.Fatal("the block must still be present so the composition layer can warn about it")
	}
	if e.Implemented {
		t.Error("Implemented must stay false until a handler claims it")
	}
	if !report.OK() {
		t.Errorf("findings = %+v", report.Findings)
	}
}

func TestReport_Accessors(t *testing.T) {
	t.Parallel()

	r := Report{Findings: []Finding{
		{Severity: SeverityWarning, Code: "w", Message: "warned"},
		{Severity: SeverityError, Code: "e", Path: "p", Message: "failed"},
	}}
	if r.OK() {
		t.Error("OK() must be false when an error finding is present")
	}
	if len(r.Errors()) != 1 || len(r.Warnings()) != 1 {
		t.Errorf("Errors/Warnings = %v/%v", r.Errors(), r.Warnings())
	}
	if got := r.Err().Error(); !strings.Contains(got, "p: failed") || strings.Contains(got, "warned") {
		t.Errorf("Err() = %q", got)
	}
	if (Report{}).Err() != nil {
		t.Error("an empty report must not produce an error")
	}
}
