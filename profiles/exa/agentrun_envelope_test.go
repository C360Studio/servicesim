package exa

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
)

// The AgentRun schema (openapi line 5793, retrieved 2026-10-01) is
// additionalProperties: false with ten required keys, so a body is wrong both
// when it drops a key and when it adds one. These tests pin the key sets of
// every body this profile can produce; the values the spec leaves open are
// simulator policy and are pinned separately below.

var (
	agentRunKeys = []string{
		"id", "object", "status", "stopReason", "createdAt", "completedAt", "request", "output", "usage", "costDollars",
	}
	agentRunStatuses    = []string{"queued", "running", "completed", "failed", "cancelled"}
	agentRunStopReasons = []string{
		"schema_satisfied", "budget_reached", "time_limit_reached", "stopped", "error", "cancelled",
	}
	agentOutputKeys     = []string{"text", "structured", "grounding"}
	agentUsageRequired  = []string{"agentComputeUnits", "searches", "emails", "phoneNumbers"}
	agentCostRequired   = []string{"total", "agentCompute", "search", "emails", "phoneNumbers"}
	agentGroundingKeys  = []string{"field", "citations", "confidence"}
	agentCitationKeys   = []string{"url", "title"}
	agentConfidenceEnum = []string{"low", "medium", "high"}
)

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// assertSubsetWithRequired asserts that every required key is present and no key
// falls outside the allowed set.
func assertSubsetWithRequired(t *testing.T, where string, got map[string]any, required, allowed []string) {
	t.Helper()
	for _, k := range required {
		assert.Contains(t, got, k, "%s: required key %q is missing", where, k)
	}
	for k := range got {
		assert.Contains(t, allowed, k, "%s: key %q is outside the schema", where, k)
	}
}

// assertAgentRunSchema decodes one AgentRun body and checks it against the
// structural part of the spec: key sets, constants, enums, nullability and the
// integer-ness of the integer counters. It returns the decoded run.
func assertAgentRunSchema(t *testing.T, raw []byte) map[string]any {
	t.Helper()

	var run map[string]any
	require.NoError(t, json.Unmarshal(raw, &run), "body: %s", raw)

	assert.ElementsMatch(t, agentRunKeys, keysOf(run), "exactly the ten required keys and nothing else: %s", raw)
	assert.Equal(t, "agent_run", run["object"])
	assert.Regexp(t, `^[A-Za-z0-9_.:-]+$`, run["id"], "AgentRunId pattern")
	assert.Contains(t, agentRunStatuses, run["status"])

	if run["stopReason"] != nil {
		assert.Contains(t, agentRunStopReasons, run["stopReason"])
	}
	created, ok := run["createdAt"].(string)
	require.True(t, ok, "createdAt is a string, not null")
	_, err := time.Parse(time.RFC3339, created)
	assert.NoError(t, err, "createdAt is a date-time")
	if completed, ok := run["completedAt"].(string); ok {
		_, err := time.Parse(time.RFC3339, completed)
		assert.NoError(t, err, "completedAt is a date-time when it is not null")
	} else {
		assert.Nil(t, run["completedAt"], "completedAt is a date-time or null")
	}
	assert.Nil(t, run["request"], "request is null: the job record holds no request body")

	output, ok := run["output"].(map[string]any)
	require.True(t, ok, "output is a non-nullable object: %s", raw)
	assert.ElementsMatch(t, agentOutputKeys, keysOf(output), "AgentRunOutput requires all three keys")
	assert.IsType(t, "", output["text"])
	grounding, ok := output["grounding"].([]any)
	require.True(t, ok, "grounding is an array, never null or absent: %s", raw)
	for gi, g := range grounding {
		entry, _ := g.(map[string]any)
		assertSubsetWithRequired(t, "grounding", entry, []string{"field", "citations"}, agentGroundingKeys)
		if c, present := entry["confidence"]; present && c != nil {
			assert.Contains(t, agentConfidenceEnum, c, "grounding[%d].confidence", gi)
		}
		citations, ok := entry["citations"].([]any)
		require.True(t, ok, "grounding[%d].citations is an array: %s", gi, raw)
		for _, c := range citations {
			citation, _ := c.(map[string]any)
			assertSubsetWithRequired(t, "citation", citation, []string{"url"}, agentCitationKeys)
		}
	}

	usage, ok := run["usage"].(map[string]any)
	require.True(t, ok, "usage is a non-nullable object: %s", raw)
	assertSubsetWithRequired(t, "usage", usage, agentUsageRequired,
		append([]string{"dataSources"}, agentUsageRequired...))
	for _, k := range []string{"searches", "emails", "phoneNumbers"} {
		n, ok := usage[k].(float64)
		require.True(t, ok, "usage.%s is a number", k)
		assert.Equal(t, float64(int(n)), n, "usage.%s is an integer", k)
		assert.GreaterOrEqual(t, n, 0.0)
	}
	if ds, ok := usage["dataSources"].(map[string]any); ok {
		for k, v := range ds {
			n, _ := v.(float64)
			assert.Equal(t, float64(int(n)), n, "usage.dataSources.%s is an integer", k)
		}
	}

	cost, ok := run["costDollars"].(map[string]any)
	require.True(t, ok, "costDollars is a non-nullable object: %s", raw)
	assertSubsetWithRequired(t, "costDollars", cost, agentCostRequired,
		append([]string{"dataSources"}, agentCostRequired...))
	for _, k := range agentCostRequired {
		n, ok := cost[k].(float64)
		require.True(t, ok, "costDollars.%s is a scalar number", k)
		assert.GreaterOrEqual(t, n, 0.0)
	}
	return run
}

const envelopeBase = "2026-03-04T05:06:07.000Z"

// envelopeScenarios are the shapes of run this profile can be scripted into.
// extra_fields is excluded on purpose: it exists to add keys outside the schema
// (house rule 5), so a body carrying it is outside the AgentRun key set by design.
var envelopeScenarios = []struct {
	name string
	src  string
}{
	{
		name: "queued then running",
		src: `
version: 1
name: env-running
time: {base: ` + envelopeBase + `}
providers:
  exa_agent_runs:
    turns:
      - respond: {status: running}
`,
	},
	{
		name: "completed with grounding",
		src: `
version: 1
name: env-completed
time: {base: ` + envelopeBase + `}
sources:
  - {id: source-a, url: "https://example.test/a", title: Report A, text: body}
  - {id: source-b, url: "https://example.test/b", title: Report B, text: body}
providers:
  exa_agent_runs:
    status: completed
    output:
      text: done
      structured: {answer: 42}
      grounding:
        - {field: answer, citations: [source-a, source-b], confidence: high}
        - {field: other}
    usage: {agent_compute_units: 1.5, searches: 4, emails: 1, phone_numbers: 2, data_sources: {fiber: 3}}
    cost_dollars:
      total: 0.5
      agent_compute: 0.3
      search: 0.1
      emails: 0.05
      phone_numbers: 0.05
      data_sources: {fiber: 0.02}
`,
	},
	{
		name: "completed with nothing scripted",
		src: `
version: 1
name: env-bare
time: {base: ` + envelopeBase + `}
providers:
  exa_agent_runs:
    status: completed
`,
	},
	{
		name: "failed",
		src: `
version: 1
name: env-failed
time: {base: ` + envelopeBase + `}
providers:
  exa_agent_runs:
    status: failed
`,
	},
	{
		name: "cancelled",
		src: `
version: 1
name: env-cancelled
time: {base: ` + envelopeBase + `}
providers:
  exa_agent_runs:
    status: cancelled
`,
	},
	{
		name: "stopped early",
		src: `
version: 1
name: env-stopped
time: {base: ` + envelopeBase + `}
providers:
  exa_agent_runs:
    status: completed
    stop_reason: stopped
    output: {text: partial}
`,
	},
}

// Every AgentRun body this profile can produce — the create body, and a poll of
// each scripted shape — carries exactly the ten required keys and no key outside
// the schema, with the nested objects held to the same standard.
func TestAgentRunEveryBodyHasExactlyTheSpecKeys(t *testing.T) {
	t.Parallel()

	for _, tc := range envelopeScenarios {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := asyncSim(t, tc.src)

			create := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
			require.Equal(t, http.StatusOK, create.Code)
			created := assertAgentRunSchema(t, create.Body.Bytes())
			assert.Equal(t, "queued", created["status"])

			poll := s.do(request{method: http.MethodGet, path: "/agent/runs/" + created["id"].(string)})
			require.Equal(t, http.StatusOK, poll.Code)
			assertAgentRunSchema(t, poll.Body.Bytes())
		})
	}
}

// A run that has not finished still carries output, usage and costDollars: all
// three are required and non-nullable in the spec, while stopReason, completedAt
// and request are the ones it marks nullable. The VALUES are simulator policy —
// the spec has no example bodies — and a zero here is a placeholder for a
// required key, not a claim about billing.
func TestAgentRunNonTerminalRunsCarryPlaceholderOutputUsageAndCost(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	create := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`})
	created := assertAgentRunSchema(t, create.Body.Bytes())
	id := created["id"].(string)

	placeholderOutput := map[string]any{"text": "", "structured": nil, "grounding": []any{}}
	placeholderUsage := map[string]any{"agentComputeUnits": 0.0, "searches": 0.0, "emails": 0.0, "phoneNumbers": 0.0}
	placeholderCost := map[string]any{
		"total": 0.0, "agentCompute": 0.0, "search": 0.0, "emails": 0.0, "phoneNumbers": 0.0,
	}

	for _, run := range []map[string]any{created, assertAgentRunSchema(t, s.do(request{
		method: http.MethodGet, path: "/agent/runs/" + id,
	}).Body.Bytes())} {
		assert.Equal(t, placeholderOutput, run["output"], "status %v", run["status"])
		assert.Equal(t, placeholderUsage, run["usage"], "status %v", run["status"])
		assert.Equal(t, placeholderCost, run["costDollars"], "status %v", run["status"])
		assert.Nil(t, run["stopReason"])
		assert.Nil(t, run["completedAt"], "a run that has not finished has not completed")
	}
}

// completedAt is null until a terminal snapshot and then a deterministic value
// derived from the scenario's base time: never time.Now(), and no elapsed-time
// model. createdAt is the same base time.
func TestAgentRunCompletedAtIsNullUntilTerminalThenDerivedFromBaseTime(t *testing.T) {
	t.Parallel()

	const src = `
version: 1
name: env-times
time: {base: ` + envelopeBase + `}
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - respond: {status: completed, output: {text: done}}
`
	s := asyncSim(t, src)
	id := createRun(t, s, `{"query":"q"}`)

	running := pollRun(t, s, id)
	assert.Equal(t, envelopeBase, running["createdAt"])
	assert.Nil(t, running["completedAt"])

	done := pollRun(t, s, id)
	assert.Equal(t, envelopeBase, done["createdAt"])
	assert.Equal(t, envelopeBase, done["completedAt"], "simulator policy: a terminal run completes at the scenario's base time")

	// Determinism: a second sim over the same scenario, walked the same way,
	// renders the same bytes — the terminal snapshot included.
	other := asyncSim(t, src)
	require.Equal(t, id, createRun(t, other, `{"query":"q"}`))
	pollRun(t, other, id)
	want := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id}).Body.Bytes()
	got := other.do(request{method: http.MethodGet, path: "/agent/runs/" + id}).Body.Bytes()
	assert.NotEmpty(t, want)
	assert.Equal(t, string(want), string(got))
}

// A scenario-scripted usage or cost value always wins over a default, on any
// snapshot that declares it, and an unscripted sibling stays a zero placeholder.
func TestAgentRunScriptedUsageAndCostSurvive(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, `
version: 1
name: env-scripted
providers:
  exa_agent_runs:
    status: completed
    output: {text: done}
    usage:
      agent_compute_units: 2.5
      searches: 7
      emails: 3
      phone_numbers: 1
      data_sources: {fiber: 2, similarweb: 1}
    cost_dollars:
      total: 1.25
      agent_compute: 0.75
      search: 0.35
      emails: 0.1
      phone_numbers: 0.05
      data_sources: {fiber: 0.02}
`)
	run := assertAgentRunSchema(t, s.do(request{
		method: http.MethodGet, path: "/agent/runs/" + createRun(t, s, `{"query":"q"}`),
	}).Body.Bytes())

	assert.Equal(t, map[string]any{
		"agentComputeUnits": 2.5, "searches": 7.0, "emails": 3.0, "phoneNumbers": 1.0,
		"dataSources": map[string]any{"fiber": 2.0, "similarweb": 1.0},
	}, run["usage"])
	assert.Equal(t, map[string]any{
		"total": 1.25, "agentCompute": 0.75, "search": 0.35, "emails": 0.1, "phoneNumbers": 0.05,
		"dataSources": map[string]any{"fiber": 0.02},
	}, run["costDollars"])
}

// A scripted output renders as scripted: the structured value reaches the wire
// intact, not merely its key, and the text and grounding beside it too.
func TestAgentRunScriptedOutputValueIsRendered(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, `
version: 1
name: env-output
sources:
  - {id: source-a, url: "https://example.test/a", title: Report A, text: body}
providers:
  exa_agent_runs:
    status: completed
    output:
      text: the finding
      structured: {answer: 42, nested: {tags: [a, b], ok: true}}
      grounding:
        - {field: answer, citations: [source-a], confidence: high}
`)
	run := assertAgentRunSchema(t, s.do(request{
		method: http.MethodGet, path: "/agent/runs/" + createRun(t, s, `{"query":"q"}`),
	}).Body.Bytes())

	assert.Equal(t, map[string]any{
		"text":       "the finding",
		"structured": map[string]any{"answer": 42.0, "nested": map[string]any{"tags": []any{"a", "b"}, "ok": true}},
		"grounding": []any{map[string]any{
			"field":      "answer",
			"citations":  []any{map[string]any{"url": "https://example.test/a", "title": "Report A"}},
			"confidence": "high",
		}},
	}, run["output"])
}

// extra_fields is the one sanctioned way outside the AgentRun key set (house rule
// 5): a scripted snapshot carries the ten schema keys plus exactly the extras,
// with the extras' own values, and a body that uses it is keyed alphabetically —
// the re-sort provider.Render applies — rather than in the schema's order. A
// create and a snapshot that script none carry none.
func TestAgentRunExtraFieldsAreRenderedOnTheSnapshotThatScriptsThem(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, `
version: 1
name: env-extras
providers:
  exa_agent_runs:
    status: completed
    output: {text: done}
    extra_fields:
      experimental_trace_id: trace-0
      service_tier: {name: default, rank: 2}
`)
	id := createRun(t, s, `{"query":"q"}`)

	created := s.do(request{method: http.MethodPost, path: "/agent/runs", body: `{"query":"q"}`}).Body.Bytes()
	assertAgentRunSchema(t, created)

	raw := s.do(request{method: http.MethodGet, path: "/agent/runs/" + id}).Body.Bytes()
	var run map[string]any
	require.NoError(t, json.Unmarshal(raw, &run))
	assert.Len(t, run, 10+2, "the ten schema keys plus the two extras: %s", raw)
	assert.Equal(t, "trace-0", run["experimental_trace_id"])
	assert.Equal(t, map[string]any{"name": "default", "rank": 2.0}, run["service_tier"])
	for _, key := range agentRunKeys {
		assert.Contains(t, run, key)
	}

	assert.Equal(t, sortedTopLevelKeys(t, raw), topLevelKeys(t, raw), "extra_fields re-sorts the body alphabetically")
}

// topLevelKeys returns a JSON object's keys in the order they appear on the wire.
func topLevelKeys(t *testing.T, raw []byte) []string {
	t.Helper()

	dec := json.NewDecoder(bytes.NewReader(raw))
	_, err := dec.Token() // the opening brace
	require.NoError(t, err)

	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		require.NoError(t, err)
		key, ok := tok.(string)
		require.True(t, ok, "a top-level object key, got %v", tok)
		keys = append(keys, key)

		var skip json.RawMessage
		require.NoError(t, dec.Decode(&skip))
	}
	return keys
}

func sortedTopLevelKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	return slices.Sorted(slices.Values(topLevelKeys(t, raw)))
}

// A scripted total with no scripted components keeps the total and zero-fills the
// required components; the placeholders need not sum to it.
func TestAgentRunScriptedTotalZeroFillsTheComponents(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, asyncScenario)
	id := createRun(t, s, `{"query":"q"}`)
	pollRun(t, s, id)
	pollRun(t, s, id)

	run := assertAgentRunSchema(t, s.do(request{method: http.MethodGet, path: "/agent/runs/" + id}).Body.Bytes())
	cost, _ := run["costDollars"].(map[string]any)
	assert.InDelta(t, 0.045, cost["total"], 1e-9)
	assert.Equal(t, 0.0, cost["search"], "a zero the scenario did not script is a placeholder for a required key")
}

// costDollars.search is a scalar on this surface (AgentCostDollars, line 6018),
// not the {neural} object /search carries.
func TestAgentRunCostSearchIsAScalar(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, `
version: 1
name: env-search-cost
providers:
  exa_agent_runs:
    status: completed
    output: {text: done}
    cost_dollars: {total: 0.2, search: 0.15}
`)
	run := pollRun(t, s, createRun(t, s, `{"query":"q"}`))
	cost, _ := run["costDollars"].(map[string]any)

	assert.Equal(t, 0.15, cost["search"])
	assert.IsType(t, 0.0, cost["search"], "search is a number, not an object")
}

// AgentCitation has only url (required) and title; AgentGrounding requires
// citations even when there are none.
func TestAgentRunGroundingCitationsCarryOnlyURLAndTitle(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, `
version: 1
name: env-grounding
sources:
  - {id: source-a, url: "https://example.test/a", title: Report A, text: body}
providers:
  exa_agent_runs:
    status: completed
    output:
      text: done
      grounding:
        - {field: a, citations: [source-a], confidence: low}
        - {field: b}
`)
	run := pollRun(t, s, createRun(t, s, `{"query":"q"}`))
	output, _ := run["output"].(map[string]any)
	grounding, _ := output["grounding"].([]any)
	require.Len(t, grounding, 2)

	first, _ := grounding[0].(map[string]any)
	citation, _ := first["citations"].([]any)[0].(map[string]any)
	assert.Equal(t, map[string]any{"url": "https://example.test/a", "title": "Report A"}, citation)
	assert.Equal(t, "low", first["confidence"])

	second, _ := grounding[1].(map[string]any)
	assert.Equal(t, []any{}, second["citations"], "citations is required even when empty")
	assert.NotContains(t, second, "confidence", "confidence is optional and was not scripted")
}

// The run-level error object is not in the AgentRun schema
// (additionalProperties: false, line 5839): a failed run is status: failed plus
// stopReason: error. Declaring the old block is a load error, in both forms.
func TestAgentRunValidatorRejectsARunLevelError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
	}{
		{"single-shot", "    status: failed\n    error: {code: X, message: y}\n"},
		{"turn", "    turns:\n      - respond:\n          status: failed\n          error: {code: X, message: y}\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sc := mustScenario(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n"+tc.src)
			findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameAgentRuns: agentRunValidator{}})

			var got *struct{ msg, sev string }
			for _, f := range findings {
				if f.Code == codeAgentRunErrorNotInSchema {
					got = &struct{ msg, sev string }{f.Message, string(f.Severity)}
				}
			}
			require.NotNil(t, got, "want %s, got %+v", codeAgentRunErrorNotInSchema, findings)
			assert.Equal(t, "error", got.sev)
			assert.Contains(t, got.msg, "no run-level error")
			assert.Contains(t, got.msg, "status: failed")
			assert.Contains(t, got.msg, "stop_reason: error")
		})
	}
}

// A failed run needs nothing else: it renders status failed and stopReason
// error, with no error key anywhere on the wire.
func TestAgentRunFailedIsStatusPlusStopReasonOnly(t *testing.T) {
	t.Parallel()

	s := asyncSim(t, `
version: 1
name: env-failed-only
time: {base: `+envelopeBase+`}
providers:
  exa_agent_runs:
    status: failed
`)
	run := assertAgentRunSchema(t, s.do(request{
		method: http.MethodGet, path: "/agent/runs/" + createRun(t, s, `{"query":"q"}`),
	}).Body.Bytes())

	assert.Equal(t, "failed", run["status"])
	assert.Equal(t, "error", run["stopReason"])
	assert.NotContains(t, run, "error")
	assert.Equal(t, envelopeBase, run["completedAt"], "a terminal run has a completedAt")
}

// budget.maxDurationSeconds and budget.maxCostDollars. The spec types both and
// states their accepted ranges in prose (lines 4863, 4866), scoping each to
// certain efforts: maxDurationSeconds "applies only to ultra", maxCostDollars
// "applies only to auto and ultra", and an omitted effort is AgentEffort's
// default, auto (line 4830). SIMULATOR-POLICY for what a violation does:
//
//   - a mistyped member is always a 400;
//   - an out-of-range value is a 400 only where the limit applies, and
//     otherwise a warning finding with the request served normally.
func TestAgentRunCreateBudgetTypeAndRange(t *testing.T) {
	t.Parallel()

	const (
		ok     = http.StatusOK
		reject = http.StatusBadRequest
	)
	tests := []struct {
		name   string
		body   string
		status int
		// warns is the range code the request must also raise as a warning, or "".
		warns string
	}{
		{"no budget", `{"query":"q"}`, ok, ""},
		{"empty budget", `{"query":"q","budget":{}}`, ok, ""},

		// maxDurationSeconds applies only to ultra.
		{"duration at the floor", `{"query":"q","effort":"ultra","budget":{"maxDurationSeconds":300}}`, ok, ""},
		{"duration at the ceiling", `{"query":"q","effort":"ultra","budget":{"maxDurationSeconds":10800}}`, ok, ""},
		{"duration below the floor on ultra", `{"query":"q","effort":"ultra","budget":{"maxDurationSeconds":299}}`, reject, ""},
		{"duration above the ceiling on ultra", `{"query":"q","effort":"ultra","budget":{"maxDurationSeconds":10801}}`, reject, ""},
		{"duration below the floor, default effort", `{"query":"q","budget":{"maxDurationSeconds":299}}`, ok, codeBudgetDurationRange},
		{"duration above the ceiling on low", `{"query":"q","effort":"low","budget":{"maxDurationSeconds":10801}}`, ok, codeBudgetDurationRange},
		{"duration in range on a non-ultra effort", `{"query":"q","effort":"low","budget":{"maxDurationSeconds":600}}`, ok, ""},
		{"duration not an integer", `{"query":"q","effort":"ultra","budget":{"maxDurationSeconds":300.5}}`, reject, ""},
		{"duration not an integer on low", `{"query":"q","effort":"low","budget":{"maxDurationSeconds":300.5}}`, reject, ""},
		{"duration not a number", `{"query":"q","budget":{"maxDurationSeconds":"600"}}`, reject, ""},

		// maxCostDollars applies to auto (the default) and ultra.
		{"cost at the floor", `{"query":"q","budget":{"maxCostDollars":1}}`, ok, ""},
		{"cost fractional in range", `{"query":"q","budget":{"maxCostDollars":12.5}}`, ok, ""},
		{"cost at the ceiling", `{"query":"q","effort":"ultra","budget":{"maxCostDollars":100}}`, ok, ""},
		{"cost below the floor, default effort", `{"query":"q","budget":{"maxCostDollars":0.5}}`, reject, ""},
		{"cost below the floor on auto", `{"query":"q","effort":"auto","budget":{"maxCostDollars":0.5}}`, reject, ""},
		{"cost above the ceiling on ultra", `{"query":"q","effort":"ultra","budget":{"maxCostDollars":100.01}}`, reject, ""},
		{"cost below the floor on low", `{"query":"q","effort":"low","budget":{"maxCostDollars":0.5}}`, ok, codeBudgetCostRange},
		{"cost above the ceiling on high", `{"query":"q","effort":"high","budget":{"maxCostDollars":100.01}}`, ok, codeBudgetCostRange},
		{"cost not a number", `{"query":"q","budget":{"maxCostDollars":"5"}}`, reject, ""},
		{"cost not a number on low", `{"query":"q","effort":"low","budget":{"maxCostDollars":"5"}}`, reject, ""},

		{"budget not an object", `{"query":"q","budget":5}`, reject, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := asyncSim(t, asyncScenario)
			rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: tc.body})

			require.Equal(t, tc.status, rec.Code, "body: %s", rec.Body.String())
			if tc.warns != "" {
				assert.Equal(t, journal.SeverityWarning, s.findingSeverity(tc.warns),
					"a limit that does not apply is warned about, not rejected")
			}
		})
	}
}

// findingMessage is the text of the first finding with code on the most recent
// request. The tests below read the rejection's message from the journal, not from
// the response body, so they do not depend on which error envelope the route speaks.
func findingMessage(s *sim, code string) string {
	s.t.Helper()
	for _, f := range s.findings() {
		if f.Code == code {
			return f.Message
		}
	}
	s.t.Fatalf("no %s finding recorded: %+v", code, s.findings())
	return ""
}

// An out-of-range value is described by the float itself. Converting a huge
// float64 to int is implementation-defined in Go (it saturates to different
// values on arm64 and amd64), so formatting it that way would put different
// bytes on the wire for one request on a laptop and in CI (house rule 2).
func TestAgentRunBudgetRangeMessageIsPlatformIndependent(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
		code string
		want string
	}{
		{"duration", `{"query":"q","effort":"ultra","budget":{"maxDurationSeconds":1e300}}`,
			codeBudgetDurationRange, "got 1e+300"},
		{"negative duration", `{"query":"q","effort":"ultra","budget":{"maxDurationSeconds":-1e300}}`,
			codeBudgetDurationRange, "got -1e+300"},
		{"cost", `{"query":"q","budget":{"maxCostDollars":1e300}}`, codeBudgetCostRange, "got 1e+300"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := asyncSim(t, asyncScenario)
			rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: tc.body})

			require.Equal(t, http.StatusBadRequest, rec.Code)
			message := findingMessage(s, tc.code)
			assert.Contains(t, message, tc.want)
			assert.Contains(t, rec.Body.String(), tc.want, "the response carries the same message")
			assert.NotContains(t, message, "9223372036854775807", "no float-to-int conversion in the message")
			assert.NotContains(t, message, "9223372036854775808")
		})
	}
}

// A null or non-string effort is described as JSON, not as Go's <nil>.
func TestAgentRunEffortRejectionMessageIsJSON(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ body, want string }{
		{`{"query":"q","effort":null}`, "got null"},
		{`{"query":"q","effort":5}`, "got 5"},
		{`{"query":"q","effort":"turbo"}`, `got "turbo"`},
	} {
		s := asyncSim(t, asyncScenario)
		rec := s.do(request{method: http.MethodPost, path: "/agent/runs", body: tc.body})

		require.Equal(t, http.StatusBadRequest, rec.Code)
		message := findingMessage(s, codeEffortInvalid)
		assert.Contains(t, message, tc.want)
		assert.NotContains(t, message, "<nil>")
	}
}

// Every AgentUsage and AgentCostDollars member is a number with minimum: 0
// (spec lines 5981-6026), and the data-source maps hold non-negative integers
// and numbers. A scenario scripting a negative or non-finite value would put a
// schema-invalid body on the wire, so it is a load error. (A fractional COUNT is
// a decode error of its own and never reaches this check.)
func TestAgentRunValidatorRejectsAValueOutsideTheSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		path string // the field the finding must point at
	}{
		{"negative compute units", `usage: {agent_compute_units: -2}`, "usage.agent_compute_units"},
		{"negative searches", `usage: {searches: -3}`, "usage.searches"},
		{"negative emails", `usage: {emails: -1}`, "usage.emails"},
		{"negative phone numbers", `usage: {phone_numbers: -1}`, "usage.phone_numbers"},
		{"negative usage data source", `usage: {data_sources: {fiber: 1, similarweb: -4}}`, "usage.data_sources.similarweb"},
		{"negative total", `cost_dollars: {total: -1}`, "cost_dollars.total"},
		{"negative search cost", `cost_dollars: {total: 1, search: -0.5}`, "cost_dollars.search"},
		{"negative agent compute cost", `cost_dollars: {agent_compute: -0.1}`, "cost_dollars.agent_compute"},
		{"negative emails cost", `cost_dollars: {emails: -0.1}`, "cost_dollars.emails"},
		{"negative phone numbers cost", `cost_dollars: {phone_numbers: -0.1}`, "cost_dollars.phone_numbers"},
		{"negative cost data source", `cost_dollars: {data_sources: {fiber: -0.02}}`, "cost_dollars.data_sources.fiber"},
		{"not a number", `cost_dollars: {total: .nan}`, "cost_dollars.total"},
		{"infinite", `usage: {agent_compute_units: .inf}`, "usage.agent_compute_units"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sc := mustScenario(t, "version: 1\nname: v\nproviders:\n  exa_agent_runs:\n    status: completed\n    output: {text: x}\n    "+
				tc.src+"\n")
			findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameAgentRuns: agentRunValidator{}})

			var paths []string
			for _, f := range findings {
				if f.Code == codeAgentRunValueRange {
					assert.Equal(t, "error", string(f.Severity))
					paths = append(paths, f.Path)
				}
			}
			require.Len(t, paths, 1, "want exactly one %s, got %+v", codeAgentRunValueRange, findings)
			assert.Contains(t, paths[0], tc.path)
		})
	}
}

// Zero and positive values, including a fractional compute-unit count and a
// zero-valued data source, are fine.
func TestAgentRunValidatorAcceptsNonNegativeValues(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: v
providers:
  exa_agent_runs:
    status: completed
    output: {text: x}
    usage: {agent_compute_units: 0.25, searches: 0, emails: 2, data_sources: {fiber: 0}}
    cost_dollars: {total: 0, agent_compute: 0.001, data_sources: {fiber: 0.5}}
`)
	for _, f := range provider.ValidateScenario(sc, map[string]provider.Validator{NameAgentRuns: agentRunValidator{}}) {
		assert.NotEqual(t, codeAgentRunValueRange, f.Code, "%+v", f)
	}
}
