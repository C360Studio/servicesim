package scenario

import (
	"strings"
	"testing"
)

// acceptedScenario spells an async entry whose create plan is attempts, written
// as flow-style YAML, so the tables below read as the attempt under test.
func acceptedScenario(attempts string) string {
	return "version: 1\nname: n\nproviders:\n  exa_agent_runs:\n    create:\n      fault: {attempts: [" + attempts + "]}\n" +
		"    turns:\n      - respond: {status: completed}\n"
}

// findingsWith returns the findings of a Parse of src that carry code, and the
// error Parse returned.
func findingsWith(t *testing.T, src, code string) ([]Finding, error) {
	t.Helper()

	_, report, err := Parse([]byte(src))
	var found []Finding
	for _, f := range report.Findings {
		if f.Code == code {
			found = append(found, f)
		}
	}
	return found, err
}

// TestValidate_AcceptedIsRedundantExactlyWhenTheAttemptDeliversItsBody ties the
// load check to FaultAttempt.DeliversBody rather than to a list of kinds, which is
// the point of it: the shapes a kind list would miss — a body: override below 400,
// a 204 or a 304 — are the ones where `accepted` is meaningful, and a stream_*
// kind or a bare 200 are the ones where it is not.
func TestValidate_AcceptedIsRedundantExactlyWhenTheAttemptDeliversItsBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		attempt   string
		redundant bool
	}{
		// The client receives the handler's body, so the job is kept anyway.
		{"an empty attempt", `{accepted: true}`, true},
		{"an explicit 201", `{status: 201, accepted: true}`, true},
		{"a pure delay", `{delay: 1s, accepted: true}`, true},
		{"extra_fields", `{kind: extra_fields, extra_fields: {x: 1}, accepted: true}`, true},
		{"oversized_body", `{kind: oversized_body, body_bytes: 100, accepted: true}`, true},
		{"stream_disconnect", `{kind: stream_disconnect, accepted: true}`, true},
		{"stream_truncate_chunk", `{kind: stream_truncate_chunk, accepted: true}`, true},
		{"stream_stall", `{kind: stream_stall, delay: 1s, accepted: true}`, true},

		// The body never arrives, so `accepted` is what keeps the job.
		{"close_before_headers", `{kind: close_before_headers, accepted: true}`, false},
		{"truncate_body", `{kind: truncate_body, truncate_after_bytes: 4, accepted: true}`, false},
		{"empty_body", `{kind: empty_body, accepted: true}`, false},
		{"invalid_json", `{kind: invalid_json, raw_body: "not json", accepted: true}`, false},
		{"a 504", `{status: 504, accepted: true}`, false},
		{"a 400", `{status: 400, accepted: true}`, false},
		{"a body override with no status", `{body: {scripted: true}, accepted: true}`, false},
		{"a body override at 201", `{status: 201, body: {scripted: true}, accepted: true}`, false},
		{"a 204", `{status: 204, accepted: true}`, false},
		{"a 304", `{status: 304, accepted: true}`, false},
		{"a 101", `{status: 101, accepted: true}`, false},

		// Without the modifier nothing is redundant, whatever the shape.
		{"a 200 without accepted", `{status: 200}`, false},
		{"a delivering attempt with accepted false", `{status: 201, accepted: false}`, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			found, err := findingsWith(t, acceptedScenario(tc.attempt), CodeAcceptedRedundant)
			if !tc.redundant {
				if err != nil || len(found) != 0 {
					t.Fatalf("expected a clean load, got err=%v findings=%+v", err, found)
				}
				return
			}
			if err == nil || len(found) != 1 {
				t.Fatalf("expected one %s error, got err=%v findings=%+v", CodeAcceptedRedundant, err, found)
			}
			if found[0].Severity != SeverityError {
				t.Errorf("severity = %q, want error", found[0].Severity)
			}
			const wantPath = "providers.exa_agent_runs.create.fault.attempts[0].accepted"
			if found[0].Path != wantPath {
				t.Errorf("path = %q, want %q", found[0].Path, wantPath)
			}
			if found[0].Message == "" {
				t.Error("a finding with no message is not actionable")
			}
		})
	}
}

// TestValidate_AcceptedRedundantOnAStreamKindSaysWhy keeps the one code but tells
// the author of a stream_* attempt something true: the kind is dropped on a
// non-streaming exchange, so the body arrives untouched, which is what makes the
// modifier redundant.
func TestValidate_AcceptedRedundantOnAStreamKindSaysWhy(t *testing.T) {
	t.Parallel()

	found, _ := findingsWith(t, acceptedScenario(`{kind: stream_disconnect, accepted: true}`), CodeAcceptedRedundant)
	if len(found) != 1 || !strings.Contains(found[0].Message, "stream_disconnect") {
		t.Fatalf("expected the finding to name the stream kind, got %+v", found)
	}
}

// TestValidate_AcceptedIsAllowedUnderEveryFaultPlan records that load checks the
// attempt in isolation: whether the claiming request mints a job is a runtime
// question, because only a profile's routes know. A turn-level plan therefore
// loads with `accepted`, and so does a block-level one on a synchronous entry.
func TestValidate_AcceptedIsAllowedUnderEveryFaultPlan(t *testing.T) {
	t.Parallel()

	for name, src := range map[string]string{
		"a create plan": acceptedScenario(`{kind: close_before_headers, accepted: true}`),
		"a turn-level plan": "version: 1\nname: n\nproviders:\n  exa_agent_runs:\n    turns:\n" +
			"      - fault: {attempts: [{kind: close_before_headers, accepted: true}]}\n        respond: {status: completed}\n",
		"a block-level plan": "version: 1\nname: n\nproviders:\n  exa:\n" +
			"    fault: {attempts: [{kind: close_before_headers, accepted: true}]}\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, report, err := Parse([]byte(src)); err != nil {
				t.Fatalf("expected a clean load, got %v (%+v)", err, report.Findings)
			}
		})
	}
}

// TestValidate_CreateFaultIsValidated closes the gap docs/design/async-jobs.md
// §3.1 admitted: a malformed create plan used to load silently and misbehave at
// request time. It now fails at load exactly as a turn's plan does.
func TestValidate_CreateFaultIsValidated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		src      string
		wantCode string
		wantPath string
	}{
		{
			name:     "an empty attempts list",
			src:      acceptedScenario(``),
			wantCode: "scenario.fault.attempts.empty",
			wantPath: "providers.exa_agent_runs.create.fault.attempts",
		},
		{
			name:     "an unknown kind",
			src:      acceptedScenario(`{kind: explode}`),
			wantCode: "scenario.fault.kind.unknown",
			wantPath: "providers.exa_agent_runs.create.fault.attempts[0].kind",
		},
		{
			name:     "a negative retry_after",
			src:      acceptedScenario(`{status: 429, retry_after: -1}`),
			wantCode: "scenario.fault.retry_after.negative",
			wantPath: "providers.exa_agent_runs.create.fault.attempts[0].retry_after",
		},
		{
			name: "an unknown after",
			src: "version: 1\nname: n\nproviders:\n  exa_agent_runs:\n    create:\n" +
				"      fault: {attempts: [{status: 500}], after: forever}\n    turns:\n      - respond: {status: completed}\n",
			wantCode: "scenario.fault.after.unknown",
			wantPath: "providers.exa_agent_runs.create.fault.after",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			found, err := findingsWith(t, tc.src, tc.wantCode)
			if err == nil || len(found) != 1 {
				t.Fatalf("expected one %s error, got err=%v findings=%+v", tc.wantCode, err, found)
			}
			if found[0].Path != tc.wantPath {
				t.Errorf("path = %q, want %q", found[0].Path, tc.wantPath)
			}
		})
	}
}
