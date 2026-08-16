package perplexity

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/contracts"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// streamGoldenScenario is the corpus contracts/perplexity/perplexity-sonar-stream.sse
// is rendered from: the three-delta example docs/design/streaming.md §2 scripts,
// minus the pace: keys unit 1 does not decode (see §3.1's scope note).
const streamGoldenScenario = `
version: 1
name: deep-research-stream
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  perplexity:
    completion_id: stream-golden-completion
    model: sonar-deep-research
    answer: "Report A finds that X."
    citations: [source-a]
    search_results:
      - source: source-a
    usage:
      prompt_tokens: 19
      completion_tokens: 240
      total_tokens: 259
      reasoning_tokens: 5120
      cost:
        input_tokens_cost: 0.0002
        output_tokens_cost: 0.0024
        reasoning_tokens_cost: 0.0102
        total_cost: 0.0128
    stream:
      when_requested: stream
      deltas:
        - "Report A "
        - "finds "
        - "that X."
`

// streamGoldenRequest is the adopter's own shape: stream: true against
// model: sonar-deep-research, the primary path §10 of the design names as
// the one that must land first.
const streamGoldenRequest = `{"model":"sonar-deep-research","messages":[{"role":"user","content":"what does report A find?"}],"stream":true}`

// sseGoldenBytes reads a raw SSE transcript fixture verbatim — no JSON
// compaction, unlike goldenBytes, because the golden IS the wire bytes:
// frame boundaries and the bare [DONE] token are part of what is being
// pinned, and re-encoding through encoding/json would corrupt both.
func sseGoldenBytes(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := contracts.Read(contracts.Perplexity, name)
	require.NoError(t, err)
	return raw
}

// TestSonarStreamGolden is the byte-for-byte transcript pin
// docs/design/streaming.md §10 and the P5U1 spec require: the full-mode
// GrammarDelta sequence for a three-delta turn, rendered through the real
// handler on every one of the three route spellings that share one fault
// budget. completion_id is pinned in the fixture — the same convention
// perplexity-sonar-happy.json already uses — rather than derived, which is
// what lets this be a plain byte comparison with no separate id-substitution
// step: a pinned id already reads as a real value in the golden, exactly as
// the JSON goldens' pinned ids do.
func TestSonarStreamGolden(t *testing.T) {
	t.Parallel()

	want := sseGoldenBytes(t, "perplexity-sonar-stream.sse")

	for _, path := range []string{"/v1/sonar", "/chat/completions", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, streamGoldenScenario))
			resp, body := s.do(t, http.MethodPost, path, streamGoldenRequest)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
			require.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
			require.Empty(t, resp.Header.Get("Content-Length"))
			require.Equal(t, string(want), string(body))
		})
	}
}

// TestSonarStreamDeterministic proves the same scenario and the same request
// render byte-identical transcripts across separate server instances — the
// house rule 2 property, restated for a transport that writes more than one
// frame.
func TestSonarStreamDeterministic(t *testing.T) {
	t.Parallel()

	render := func() []byte {
		s := newSim(t, mustScenario(t, streamGoldenScenario))
		_, body := s.do(t, http.MethodPost, "/v1/sonar", streamGoldenRequest)
		return body
	}
	require.Equal(t, string(render()), string(render()))
}

// TestSonarStreamFalseOnAStreamingEntryServesOrdinaryJSON is the case §2 and
// the preamble of docs/design/streaming.md build the whole per-request
// (rather than per-entry) design around: a consumer sends stream: false on
// an entry whose policy is stream, and gets the ordinary JSON body, unaffected.
func TestSonarStreamFalseOnAStreamingEntryServesOrdinaryJSON(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, streamGoldenScenario))
	resp, body := s.do(t, http.MethodPost, "/v1/sonar",
		`{"model":"sonar-deep-research","messages":[{"role":"user","content":"hi"}],"stream":false}`)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.Contains(t, string(body), `"object":"chat.completion"`)
	require.NotContains(t, string(body), "data:")

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	require.Nil(t, entries[0].Outcome.Stream)
	require.False(t, hasCode(entries[0].Findings, CodeStreamUnimplemented),
		"stream: false never asked to stream, so the unimplemented warning — which is about a REQUEST that did — must not fire either")
}

// TestSonarStreamPolicyDoesNotWarnUnimplemented is the retirement §9 of the
// design and item 5 of the P5U1 spec require: perplexity.stream.unimplemented
// stops firing for a request that will actually receive a scripted stream —
// promising "you got the ordinary body" would be a lie for this request.
func TestSonarStreamPolicyDoesNotWarnUnimplemented(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, streamGoldenScenario))
	resp, body := s.do(t, http.MethodPost, "/v1/sonar", streamGoldenRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	require.False(t, hasCode(entries[0].Findings, CodeStreamUnimplemented))
	require.Equal(t, "perplexity.sonar.stream", entries[0].Outcome.Label)
}

// TestSonarStreamModeConciseUnscripted pins §7 A2: a stream_mode: concise
// request against a streaming entry is served the full-mode transcript
// anyway, with a warning naming the divergence — not rejected, and not the
// concise sequence, because the request is valid per the vendor's own enum.
func TestSonarStreamModeConciseUnscripted(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, streamGoldenScenario))
	resp, body := s.do(t, http.MethodPost, "/v1/sonar",
		`{"model":"sonar-deep-research","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_mode":"concise"}`)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"),
		"served anyway, in stream_mode: full — this build renders no other grammar")
	require.Contains(t, string(body), `"object":"chat.completion.chunk"`)

	findings := s.findings(t)
	require.True(t, hasCode(findings, CodeStreamModeConciseUnscripted))
	for _, f := range findings {
		if f.Code == CodeStreamModeConciseUnscripted {
			require.Equal(t, journal.SeverityWarning, f.Severity)
		}
	}
}

// TestSonarStreamModeConciseWithoutStreamingDoesNotWarn is the negative case
// §7 A2 names explicitly: stream_mode: concise on a request that does not
// itself ask to stream never reaches the warning, because nothing streams
// for it to diverge from.
func TestSonarStreamModeConciseWithoutStreamingDoesNotWarn(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, streamGoldenScenario))
	resp, body := s.do(t, http.MethodPost, "/v1/sonar",
		`{"model":"sonar-deep-research","messages":[{"role":"user","content":"hi"}],"stream_mode":"concise"}`)

	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.False(t, hasCode(s.findings(t), CodeStreamModeConciseUnscripted))
}

// TestSonarStreamModeConciseOnAWarnPolicyEntryDoesNotWarn is the other
// negative case §9 guards: the warning is gated on the entry's policy being
// StreamServe (handler.go's "wantsStream(x) && policy == scenario.StreamServe"),
// not merely on the request wanting to stream. A warn-policy entry never
// streams at all, so stream_mode: concise on a request that DOES set
// stream: true against it must not warn either — nothing streams for it to
// diverge from here, same as the no-stream case above, for a different
// reason.
func TestSonarStreamModeConciseOnAWarnPolicyEntryDoesNotWarn(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, `
version: 1
name: stream-mode-concise-warn-policy
providers:
  perplexity:
    stream: warn
    answer: hi
`))
	resp, body := s.do(t, http.MethodPost, "/v1/sonar",
		`{"model":"sonar-deep-research","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_mode":"concise"}`)

	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"), "warn never streams")
	require.False(t, hasCode(s.findings(t), CodeStreamModeConciseUnscripted))
	require.True(t, hasCode(s.findings(t), CodeStreamUnimplemented),
		"warn's own existing warning must still fire for a request that asked to stream")
}

// TestSonarStreamJournalOutcome pins the planned half of Outcome.Stream that
// §5.1 says is final before the first byte: grammar, chunk count, terminal
// index, usage and cost, lifted out of the wire frames the golden already
// pins.
func TestSonarStreamJournalOutcome(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, streamGoldenScenario))
	resp, body := s.do(t, http.MethodPost, "/v1/sonar", streamGoldenRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, "chat_completions", so.Grammar)
	require.Equal(t, 4, so.ChunkCount, "3 deltas + 1 terminal chunk; [DONE] is never a chunk")
	require.Equal(t, 3, so.TerminalIndex)
	require.JSONEq(t, `{
		"prompt_tokens":19,"completion_tokens":240,"total_tokens":259,"reasoning_tokens":5120,
		"cost":{"input_tokens_cost":0.0002,"output_tokens_cost":0.0024,"reasoning_tokens_cost":0.0102,"total_cost":0.0128}
	}`, string(so.Usage))
	require.NotNil(t, so.CostTotal)
	require.InDelta(t, 0.0128, *so.CostTotal, 1e-12)
	require.Equal(t, 4, so.ChunksSent, "the exchange completed by the time the journal was read")
	require.Equal(t, journal.StreamCompleted, so.State)
}

// TestSonarStreamRegressionSchemaFixture pins §8's "additive to version 1"
// claim from the Sonar side: a v1 file with no stream: key at all keeps
// loading and rendering exactly as before, and provider.ValidateScenario
// (SonarValidator) raises nothing for it.
func TestSonarStreamRegressionSchemaFixture(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, sonarScenario)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameSonar: SonarValidator{}})
	require.Empty(t, findings)
}

// TestSonarStreamDeltasEmptyFailsAtLoad pins scenario.CodeStreamDeltasEmpty
// reachable through the real Sonar validator: a streaming entry whose turn
// declares no deltas is a load-time error, not a request-time surprise.
func TestSonarStreamDeltasEmptyFailsAtLoad(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: stream-no-deltas
providers:
  perplexity:
    stream: stream
    answer: hello
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameSonar: SonarValidator{}})
	var found *scenario.Finding
	for i := range findings {
		if findings[i].Code == scenario.CodeStreamDeltasEmpty {
			found = &findings[i]
		}
	}
	require.NotNil(t, found, "findings: %+v", findings)
	require.Equal(t, scenario.SeverityError, found.Severity)
}

// TestSonarStreamWarnWithTruncateBodyStillLoads is the required regression
// fixture docs/design/streaming.md §9 names explicitly: a `stream: warn`
// entry combined with `truncate_body` must keep loading with NO findings at
// all through the real SonarValidator, because the distinction that governs
// scenario.CodeStreamFaultMismatch is declared POLICY versus produced
// OUTCOME, never the presence of a `stream:` key — `warn` produces an
// ordinary JSON body, so truncating it is exactly as meaningful as it always
// was. This is the Sonar-side counterpart to
// provider/exa/stream_regression_test.go's TestStreamWarnWithTruncateBodyStillLoads.
func TestSonarStreamWarnWithTruncateBodyStillLoads(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: sonar-stream-warn-truncate-regression
providers:
  perplexity:
    stream: warn
    answer: hi
    fault:
      attempts:
        - kind: truncate_body
          truncate_after_bytes: 5
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameSonar: SonarValidator{}})
	require.Empty(t, findings, "a warn-policy entry combined with truncate_body must load with no findings at all")
}

// TestSonarStreamFaultMismatchThroughValidator pins the load-time direction
// docs/design/streaming.md §9 requires reachable through the real Sonar
// validator: truncate_body declared on an entry whose effective policy IS
// stream must fail at load, addressed at the attempt's own path. Deleting
// SonarValidator.ValidateProjections's call to
// scenario.ValidateStreamFaultMismatch fails this test; nothing else in this
// package's test suite would have caught that (TestSonarStreamAbortUnreachableViaRealFaultEngine
// below covers only the REQUEST-time mirror case, through a scenario that
// itself already loads clean).
func TestSonarStreamFaultMismatchThroughValidator(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: sonar-stream-fault-mismatch
providers:
  perplexity:
    answer: hi
    fault:
      attempts:
        - kind: truncate_body
          truncate_after_bytes: 5
    stream:
      when_requested: stream
      deltas: ["hi"]
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameSonar: SonarValidator{}})
	var found *scenario.Finding
	for i := range findings {
		if findings[i].Code == scenario.CodeStreamFaultMismatch {
			found = &findings[i]
		}
	}
	require.NotNil(t, found, "findings: %+v", findings)
	require.Equal(t, scenario.SeverityError, found.Severity)
	require.Equal(t, "providers.perplexity.fault.attempts[0].kind", found.Path)
}

// TestSonarStreamAnswerMismatchThroughValidator pins the Answer wiring in
// SonarValidator.ValidateProjections: deleting "Answer: p.Answer" from the
// StreamTurn it builds leaves this test the only one that fails, because
// scenario/stream_test.go's own unit tests of ValidateStreamScripts never
// go through the real projection decode this test does.
func TestSonarStreamAnswerMismatchThroughValidator(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: sonar-stream-answer-mismatch
providers:
  perplexity:
    answer: "the real answer"
    stream:
      when_requested: stream
      deltas: ["something else entirely"]
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameSonar: SonarValidator{}})
	var found *scenario.Finding
	for i := range findings {
		if findings[i].Code == scenario.CodeStreamAnswerMismatch {
			found = &findings[i]
		}
	}
	require.NotNil(t, found, "findings: %+v", findings)
	require.Equal(t, scenario.SeverityWarning, found.Severity)
}

// TestSonarStreamDeltasIgnoredThroughValidator pins scenario.CodeStreamDeltasIgnored
// reachable through the real Sonar validator: a turn declaring deltas under
// an entry whose effective policy is not stream is a load-time error, the
// mirror of TestSonarStreamDeltasEmptyFailsAtLoad above.
func TestSonarStreamDeltasIgnoredThroughValidator(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: sonar-stream-deltas-ignored
providers:
  perplexity:
    turns:
      - when:
          call_index: 0
        respond:
          answer: first
      - respond:
          answer: second
          stream:
            deltas: ["second"]
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameSonar: SonarValidator{}})
	var found *scenario.Finding
	for i := range findings {
		if findings[i].Code == scenario.CodeStreamDeltasIgnored {
			found = &findings[i]
		}
	}
	require.NotNil(t, found, "findings: %+v", findings)
	require.Equal(t, scenario.SeverityError, found.Severity)
}

// sseDataFrames splits a raw SSE transcript into its "data:" payloads, in
// order. The bare "[DONE]" token is returned as a frame with done=true and no
// raw bytes, so a caller can distinguish it from a JSON frame without a
// separate presence check.
type sseDataFrame struct {
	raw  []byte
	done bool
}

func sseDataFrames(t *testing.T, body []byte) []sseDataFrame {
	t.Helper()
	var frames []sseDataFrame
	for _, block := range strings.Split(strings.TrimRight(string(body), "\n"), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		const prefix = "data: "
		require.True(t, strings.HasPrefix(block, prefix), "frame missing the data: prefix: %q", block)
		data := strings.TrimPrefix(block, prefix)
		if data == "[DONE]" {
			frames = append(frames, sseDataFrame{done: true})
			continue
		}
		frames = append(frames, sseDataFrame{raw: []byte(data)})
	}
	return frames
}

// streamRenderRulesScenario is a rich single turn: two deltas, a search
// result (so every chunk's search_results can be checked), an image, a
// related question, an extra field, a non-default finish_reason, and a
// created value that must NEVER reach the wire — renderSonarStream pins
// created at Scenario.BaseTime() regardless of p.Created (docs/design/streaming.md
// §7 A3's deliberate departure from the JSON body's own p.Created-or-BaseTime
// rule). terminalYAML is spliced in so a table case can add omit_usage/omit_done
// without duplicating the rest of the fixture.
func streamRenderRulesScenario(terminalYAML string) string {
	return `
version: 1
name: stream-render-rules
sources:
  - id: source-a
    url: https://example.test/a
    title: A
providers:
  perplexity:
    completion_id: render-rules-completion
    model: sonar-deep-research
    created: 1700000000
    finish_reason: length
    answer: "Hello there."
    search_results:
      - source: source-a
    images:
      - image_url: https://example.test/image.png
    related_questions: ["what else?"]
    extra_fields:
      future_field: 1
    usage:
      prompt_tokens: 1
      completion_tokens: 2
      total_tokens: 3
      cost:
        total_cost: 0.01
    stream:
      when_requested: stream
      deltas: ["Hello ", "there."]
` + terminalYAML
}

// TestSonarStreamRenderRules pins every §7 A3 rendering rule the golden's
// single happy-path transcript does not exercise: created held constant
// despite p.Created, finish_reason echoed onto the terminal chunk,
// search_results on every chunk, images/related_questions/extra_fields
// terminal-only, and terminal.omit_usage/omit_done. Each assertion here maps
// to one of the mutations that left the existing suite green: (M2) omit_usage
// ignored, (M7) omit_done never copied onto Stream, (M1) OmitDone ignored on
// the wire, (M3) p.Created leaking through, (M5) finish_reason hardcoded to
// "stop", (M4) images/related_questions/extra_fields dropped or leaked onto
// every chunk instead of the terminal one.
func TestSonarStreamRenderRules(t *testing.T) {
	t.Parallel()

	t.Run("no omit flags: full terminal payload, DONE present", func(t *testing.T) {
		t.Parallel()

		s := newSim(t, mustScenario(t, streamRenderRulesScenario("")))
		resp, body := s.do(t, http.MethodPost, "/v1/sonar", streamGoldenRequest)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

		frames := sseDataFrames(t, body)
		require.Len(t, frames, 4, "2 deltas + 1 terminal chunk + [DONE]")
		require.False(t, frames[0].done)
		require.False(t, frames[1].done)
		require.False(t, frames[2].done)
		require.True(t, frames[3].done, "the last frame must be the [DONE] sentinel")

		for i, f := range frames[:3] {
			require.Containsf(t, string(f.raw), `"created":1767225600`, "chunk %d", i)
			require.NotContainsf(t, string(f.raw), `"created":1700000000`, "chunk %d: p.Created must never reach the wire", i)
			require.Containsf(t, string(f.raw), `"search_results"`, "chunk %d: search_results must repeat on every chunk", i)
		}
		require.JSONEqf(t, `null`, mustField(t, frames[0].raw, "choices.0.finish_reason"), "non-terminal chunk 0")
		require.JSONEqf(t, `null`, mustField(t, frames[1].raw, "choices.0.finish_reason"), "non-terminal chunk 1")
		require.NotContains(t, string(frames[0].raw), `"usage"`, "usage is terminal-only")
		require.NotContains(t, string(frames[0].raw), `"images"`, "images is terminal-only")
		require.NotContains(t, string(frames[0].raw), `"related_questions"`, "related_questions is terminal-only")
		require.NotContains(t, string(frames[0].raw), `"future_field"`, "extra_fields is terminal-only")

		terminal := string(frames[2].raw)
		require.Contains(t, terminal, `"created":1767225600`)
		require.Contains(t, terminal, `"finish_reason":"length"`, "the terminal chunk echoes p.FinishReason, not a hardcoded stop")
		require.Contains(t, terminal, `"usage"`)
		require.Contains(t, terminal, `"search_results"`)
		require.Contains(t, terminal, `"images"`)
		require.Contains(t, terminal, `"related_questions"`)
		require.Contains(t, terminal, `"future_field":1`, "extra_fields merges onto the terminal chunk")

		entries := s.journal.Snapshot()
		require.Len(t, entries, 1)
		so := entries[0].Outcome.Stream
		require.NotNil(t, so)
		require.Equal(t, 2, so.TerminalIndex, "2 deltas + 1 terminal chunk: terminal is index 2")
		require.NotNil(t, so.Usage)
		require.NotNil(t, so.CostTotal)
	})

	t.Run("omit_usage and omit_done", func(t *testing.T) {
		t.Parallel()

		s := newSim(t, mustScenario(t, streamRenderRulesScenario(`
      terminal:
        omit_usage: true
        omit_done: true
`)))
		resp, body := s.do(t, http.MethodPost, "/v1/sonar", streamGoldenRequest)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

		frames := sseDataFrames(t, body)
		require.Len(t, frames, 3, "2 deltas + 1 terminal chunk, and NO [DONE] frame")
		for _, f := range frames {
			require.False(t, f.done, "omit_done must drop the [DONE] sentinel from the transcript entirely")
		}
		terminal := string(frames[2].raw)
		require.NotContains(t, terminal, `"usage"`, "omit_usage must drop the usage key from the terminal chunk")
		require.Contains(t, terminal, `"finish_reason":"length"`,
			"omit_usage must not affect any field besides usage")

		entries := s.journal.Snapshot()
		require.Len(t, entries, 1)
		so := entries[0].Outcome.Stream
		require.NotNil(t, so)
		require.Nil(t, so.Usage, "Outcome.Stream.Usage must be nil when the script omits usage")
		require.Nil(t, so.CostTotal)
		require.Equal(t, 2, so.TerminalIndex, "the terminal chunk is still marked terminal; omitting usage does not move it")
	})
}

// mustField extracts one dotted field path from a JSON frame for a single
// assertion, decoding into a generic tree rather than the full wire type —
// this table test is checking presence and a handful of values, not
// re-deriving the whole struct.
func mustField(t *testing.T, raw []byte, path string) string {
	t.Helper()
	var tree any
	require.NoError(t, json.Unmarshal(raw, &tree))
	for _, part := range strings.Split(path, ".") {
		switch node := tree.(type) {
		case map[string]any:
			tree = node[part]
		case []any:
			idx, err := strconv.Atoi(part)
			require.NoError(t, err, "path segment %q is not an index into a JSON array", part)
			require.Lessf(t, idx, len(node), "path %q: index out of range", path)
			tree = node[idx]
		default:
			t.Fatalf("path %q: cannot descend into %T", path, tree)
		}
	}
	out, err := json.Marshal(tree)
	require.NoError(t, err)
	return string(out)
}

// TestSonarStreamAbortUnreachableViaRealFaultEngine drives the mirror case
// through the real handler and a real Faults engine rather than a hand-built
// FaultDecision, proving the wiring from scenario YAML through to Handle's
// mismatch branch: a truncate_body attempt claimed on a streaming request is
// reported and the stream is served in full.
func TestSonarStreamAbortUnreachableViaRealFaultEngine(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, `
version: 1
name: stream-abort-unreachable
sources:
  - id: source-a
    url: https://example.test/a
    title: A
providers:
  perplexity:
    completion_id: abort-unreachable
    model: sonar-deep-research
    answer: hi
    fault:
      attempts:
        - kind: truncate_body
          truncate_after_bytes: 10
    stream:
      when_requested: stream
      deltas: ["hi"]
`))

	resp, body := s.do(t, http.MethodPost, "/v1/sonar", streamGoldenRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	require.Contains(t, string(body), "data: [DONE]")

	findings := s.findings(t)
	require.True(t, hasCode(findings, scenario.CodeStreamAbortUnreachable), "findings: %+v", findings)
}
