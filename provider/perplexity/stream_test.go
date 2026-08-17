package perplexity

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
// is rendered from: the three-delta example docs/design/streaming.md §2 scripts.
// It omits pace: keys because the golden transcript is pace-independent (bytes
// are identical whether the stream is paced or not, §6.3) — pace: decodes fine
// as of unit 2, this fixture just has nothing that needs it.
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

// newSimDelaySkip is newSim with DelayMode: DelaySkip — for a pacing
// assertion that must read the PLANNED schedule without paying real
// wall-clock cost. This is a faster read of the same fact newSim would give:
// PaceMS is the plan (streamPlan.paceMS()), never a wall-clock measurement,
// so it is identical under DelayReal and DelaySkip alike (§6.3).
func newSimDelaySkip(t *testing.T, s *scenario.Scenario) *sim {
	t.Helper()
	ring := journal.NewRing(64, 1<<16)
	srv := httptest.NewServer(New(provider.Deps{
		Scenario:  s,
		Journal:   ring,
		Faults:    provider.MustSet(Profile()).Faults(s),
		DelayMode: provider.DelaySkip,
	}))
	t.Cleanup(srv.Close)
	return &sim{server: srv, journal: ring}
}

// TestSonarStreamPaceWiring proves the three-level pace resolution rule
// (docs/design/streaming.md §4.3: per-delta override > script default;
// StreamTerminal.Pace overrides the terminal chunk) reaches
// Outcome.Stream.PaceMS through the real YAML decode, SonarValidator and
// renderSonarStream — not only through provider-level tests that build
// SSEEvent.Pace by hand (TestPlanStream, TestHandleStreamStallFoldsIntoPaceMS
// in package provider).
func TestSonarStreamPaceWiring(t *testing.T) {
	t.Parallel()

	s := newSimDelaySkip(t, mustScenario(t, `
version: 1
name: stream-pace-wiring
providers:
  perplexity:
    answer: "a b"
    stream:
      when_requested: stream
      pace: 40ms
      deltas:
        - "a "
        - text: "b"
          pace: 250ms
      terminal:
        pace: 10ms
`))
	resp, body := s.do(t, http.MethodPost, "/v1/sonar",
		`{"model":"sonar-deep-research","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, []int64{40, 250, 10}, so.PaceMS,
		"chunk 0: the script default (40ms, no override); chunk 1: its own per-delta override (250ms); "+
			"the terminal chunk: StreamTerminal.Pace (10ms)")
}

// TestSonarStreamStallPaceWiring is stream_stall's own YAML-level pacing
// proof, the sibling TestSonarStreamPaceWiring above does not cover: a
// scenario's own script pace, folded with a fault-declared stall Delay,
// reaching PaceMS and StallBeforeMS through the real validator and handler —
// not only through a hand-built scenario.FaultAttempt (as
// TestHandleStreamStallFoldsIntoPaceMS in package provider does).
func TestSonarStreamStallPaceWiring(t *testing.T) {
	t.Parallel()

	s := newSimDelaySkip(t, mustScenario(t, `
version: 1
name: stream-pace-stall-wiring
providers:
  perplexity:
    answer: "a b"
    stream:
      when_requested: stream
      pace: 5ms
      deltas: ["a ", "b"]
    fault:
      attempts:
        - kind: stream_stall
          after_chunk: 1
          delay: 65s
`))
	resp, body := s.do(t, http.MethodPost, "/v1/sonar",
		`{"model":"sonar-deep-research","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, []int64{5, 65_000 + 5, 5}, so.PaceMS,
		"PaceMS[after_chunk] already folds the stall's 65s into that chunk's own 5ms script pace")
	require.NotNil(t, so.StallBeforeMS)
	require.Equal(t, int64(65_000), *so.StallBeforeMS)
	require.Equal(t, journal.StreamCompleted, so.State, "a stall never aborts")
	require.Equal(t, 3, so.ChunksSent)
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

// -----------------------------------------------------------------------------
// Phase 5 unit 2: stream_disconnect, stream_truncate_chunk, stream_stall
// -----------------------------------------------------------------------------

// doStreamRaw posts to path and returns the response plus whatever body bytes
// arrived before a transport error, along with that error. Unlike sim.do it
// does not fail the test on a read error, because a disconnect or truncation
// test EXPECTS one: io.ReadAll returns the bytes it already read alongside
// the error, which is exactly the client-observed transcript these tests pin.
func (s *sim) doStreamRaw(t *testing.T, path, body string) (*http.Response, []byte, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := s.server.Client().Do(req)
	require.NoError(t, err, "headers must still arrive")
	defer func() { _ = resp.Body.Close() }()
	read, readErr := io.ReadAll(resp.Body)
	return resp, read, readErr
}

// TestSonarStreamDisconnectGolden pins the byte-for-byte transcript up to and
// including the last flushed chunk: docs/design/streaming.md §9's "aborting
// on the final indexed chunk" edge case, applied one chunk earlier so the
// golden also proves a MID-sequence disconnect (chunk 2, the third delta,
// never arrives) rather than only the boundary case. Deterministic, per the
// spec's requirement that this golden be pinned even though pacing/aborts
// exist now — nothing here reads a clock.
func TestSonarStreamDisconnectGolden(t *testing.T) {
	t.Parallel()

	want := sseGoldenBytes(t, "perplexity-sonar-stream-disconnect.sse")
	src := streamGoldenScenario + "\n    fault:\n      attempts:\n        - kind: stream_disconnect\n" +
		"          after_chunk: 2\n          reset: true\n"

	s := newSim(t, mustScenario(t, src))
	resp, body, readErr := s.doStreamRaw(t, "/v1/sonar", streamGoldenRequest)
	require.Error(t, readErr, "the connection must die before chunk 2 (the third delta) ever reaches the client")
	require.Equal(t, string(want), string(body))
	require.Empty(t, resp.Header.Get("Content-Length"), "no Content-Length anywhere on the stream path")

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	require.True(t, entries[0].Outcome.Aborted)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, journal.StreamAborted, so.State)
	require.Equal(t, 2, so.ChunksSent, "chunks 0 and 1 (the first two deltas) were fully delivered")
	require.NotNil(t, so.AbortAfterChunk)
	require.Equal(t, 2, *so.AbortAfterChunk)
	require.Nil(t, so.TruncatedAtByte)
}

// TestSonarStreamTruncateChunkGolden is the malformed-frame sibling: unlike
// the disconnect golden above, the connection dies mid-frame rather than at
// a boundary, which is the entire reason this fault kind exists (a
// byte-offset cut tests a consumer's SSE reconnect logic, not its JSON
// parser — docs/design/streaming.md §2.1's SSE-aware truncation note).
func TestSonarStreamTruncateChunkGolden(t *testing.T) {
	t.Parallel()

	want := sseGoldenBytes(t, "perplexity-sonar-stream-truncate.sse")
	src := streamGoldenScenario + "\n    fault:\n      attempts:\n        - kind: stream_truncate_chunk\n" +
		"          after_chunk: 1\n          truncate_after_bytes: 40\n"

	s := newSim(t, mustScenario(t, src))
	resp, body, readErr := s.doStreamRaw(t, "/v1/sonar", streamGoldenRequest)
	require.Error(t, readErr, "the connection must die mid-frame")
	require.Equal(t, string(want), string(body))
	require.Empty(t, resp.Header.Get("Content-Length"), "no Content-Length anywhere on the stream path")
	require.False(t, strings.HasSuffix(string(body), "\n\n"),
		"chunk 0's own frame legitimately ends in a blank line; the truncated chunk 1 fragment after it must not")

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	require.True(t, entries[0].Outcome.Aborted)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, journal.StreamAborted, so.State)
	require.Equal(t, 1, so.ChunksSent, "chunk 0 was fully delivered; chunk 1 was truncated, not sent")
	require.NotNil(t, so.AbortAfterChunk)
	require.Equal(t, 1, *so.AbortAfterChunk)
	require.NotNil(t, so.TruncatedAtByte)
	require.Equal(t, 40, *so.TruncatedAtByte)
}

// TestSonarStreamBlipThenRetry pins REQ-AGENT-DR-INTERNAL-RETRY-001
// (docs/design/streaming.md §2.1, example 3): transient-blip-then-retry is
// NOT a new fault kind, it is the existing attempt list — a disconnect on
// attempt 0, nothing scripted on attempt 1 — and the retry, sent as a
// second, ordinary request against the SAME lane, streams to completion.
func TestSonarStreamBlipThenRetry(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, streamGoldenScenario+`
    fault:
      attempts:
        - kind: stream_disconnect
          after_chunk: 1
        - {}
`))

	// Call 0 draws the disconnect attempt. after_chunk: 1 aborts before chunk
	// 1 (the terminal chunk) is written, so chunk 0's own complete frame —
	// the golden's first frame, since it is the same scenario — is exactly
	// what call 0 delivers.
	want := sseGoldenBytes(t, "perplexity-sonar-stream.sse")
	blankLine := strings.Index(string(want), "\n\n")
	require.GreaterOrEqual(t, blankLine, 0, "golden must contain at least one frame")
	firstFrameEnd := blankLine + len("\n\n")

	_, body0, readErr := s.doStreamRaw(t, "/v1/sonar", streamGoldenRequest)
	require.Error(t, readErr, "call 0 draws the scripted disconnect")
	require.Equal(t, string(want[:firstFrameEnd]), string(body0),
		"call 0 must deliver exactly chunk 0's frame — the disconnect fires before chunk 1")

	// Call 1, the retry, is a plain "kind: none" attempt: the stream
	// completes in full.
	resp1, body1 := s.do(t, http.MethodPost, "/v1/sonar", streamGoldenRequest)
	require.Equal(t, http.StatusOK, resp1.StatusCode)
	require.Contains(t, string(body1), "data: [DONE]", "the retry streams to completion")

	entries := s.journal.Snapshot()
	require.Len(t, entries, 2)
	require.Equal(t, journal.StreamAborted, entries[0].Outcome.Stream.State)
	require.Equal(t, 1, entries[0].Outcome.Stream.ChunksSent, "chunk 0 was fully delivered; chunk 1 (after_chunk) never was")
	require.NotNil(t, entries[0].Outcome.Stream.AbortAfterChunk)
	require.Equal(t, 1, *entries[0].Outcome.Stream.AbortAfterChunk)
	require.Equal(t, journal.StreamCompleted, entries[1].Outcome.Stream.State)
	require.Equal(t, 4, entries[1].Outcome.Stream.ChunksSent, "the retry's stream is not itself truncated")
}

// TestSonarStreamAfterChunkOutOfRangeAtLoad pins
// scenario.CodeStreamAfterChunkOutOfRange reachable through the real Sonar
// validator: the entry's single turn scripts 2 deltas (chunk_count 3, valid
// range 0..2) and the fault targets chunk 3.
func TestSonarStreamAfterChunkOutOfRangeAtLoad(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: stream-after-chunk-oor
providers:
  perplexity:
    answer: "ab"
    stream:
      when_requested: stream
      deltas: ["a", "b"]
    fault:
      attempts:
        - kind: stream_disconnect
          after_chunk: 3
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameSonar: SonarValidator{}})
	var found *scenario.Finding
	for i := range findings {
		if findings[i].Code == scenario.CodeStreamAfterChunkOutOfRange {
			found = &findings[i]
		}
	}
	require.NotNil(t, found, "findings: %+v", findings)
	require.Equal(t, scenario.SeverityError, found.Severity)
	require.Equal(t, "providers.perplexity.fault.attempts[0].after_chunk", found.Path)
}

// TestSonarStreamAfterChunkAtTheLastValidIndexLoadsClean is the boundary
// companion: after_chunk == chunk_count-1 (the terminal chunk itself) is a
// legitimate script, not an off-by-one error — §9's explicit carve-out.
func TestSonarStreamAfterChunkAtTheLastValidIndexLoadsClean(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: stream-after-chunk-boundary
providers:
  perplexity:
    answer: "ab"
    stream:
      when_requested: stream
      deltas: ["a", "b"]
    fault:
      attempts:
        - kind: stream_disconnect
          after_chunk: 2
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameSonar: SonarValidator{}})
	require.Empty(t, findings, "after_chunk 2 targets the terminal chunk (chunk_count-1), a valid target")
}

// TestSonarStreamMismatchStreamKindOnNonStreamingEntry is the mirror
// direction docs/design/streaming.md §9 requires alongside the existing
// truncate_body-on-a-streaming-entry case
// (TestSonarStreamFaultMismatchThroughValidator): a stream_* kind cannot
// apply to an entry whose effective policy is not stream.
func TestSonarStreamMismatchStreamKindOnNonStreamingEntry(t *testing.T) {
	t.Parallel()

	for _, kind := range []scenario.FaultKind{
		scenario.FaultStreamDisconnect, scenario.FaultStreamTruncateChunk, scenario.FaultStreamStall,
	} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			sc := mustScenario(t, `
version: 1
name: stream-mismatch-mirror
providers:
  perplexity:
    answer: hi
    fault:
      attempts:
        - kind: `+string(kind)+`
          after_chunk: 0
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
		})
	}
}

// TestStreamFaultFindingsTable pins every §9 fault-related finding this unit
// adds, in one place: the code, its severity, and whether it fires at load
// time (through the real validator) or at request time (through the real
// handler and a real Faults engine). A single table test is what the P5U2
// spec asks for explicitly, so a reviewer can see the whole catalogue at a
// glance rather than needing to find each case scattered across this file.
func TestStreamFaultFindingsTable(t *testing.T) {
	t.Parallel()

	t.Run("scenario.fault.after_chunk.not_streaming — load time, non-stream_* kind", func(t *testing.T) {
		t.Parallel()
		_, report, err := scenario.Parse([]byte(`
version: 1
name: t
providers:
  perplexity:
    answer: hi
    fault:
      attempts:
        - status: 500
          after_chunk: 2
`))
		require.Error(t, err)
		require.True(t, hasScenarioCode(report.Findings, scenario.CodeAfterChunkNotStreaming))
	})

	t.Run("scenario.fault.stream_mismatch — load time, truncate_body on a streaming entry", func(t *testing.T) {
		t.Parallel()
		sc := mustScenario(t, `
version: 1
name: t
providers:
  perplexity:
    answer: hi
    stream: {when_requested: stream, deltas: ["hi"]}
    fault: {attempts: [{kind: truncate_body, truncate_after_bytes: 1}]}
`)
		findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameSonar: SonarValidator{}})
		require.True(t, hasScenarioCode(findings, scenario.CodeStreamFaultMismatch))
	})

	t.Run("scenario.fault.stream_mismatch — load time, stream_disconnect on a non-streaming entry", func(t *testing.T) {
		t.Parallel()
		sc := mustScenario(t, `
version: 1
name: t
providers:
  perplexity:
    answer: hi
    fault: {attempts: [{kind: stream_disconnect, after_chunk: 0}]}
`)
		findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameSonar: SonarValidator{}})
		require.True(t, hasScenarioCode(findings, scenario.CodeStreamFaultMismatch))
	})

	t.Run("scenario.fault.after_chunk.out_of_range — load time", func(t *testing.T) {
		t.Parallel()
		sc := mustScenario(t, `
version: 1
name: t
providers:
  perplexity:
    answer: hi
    stream: {when_requested: stream, deltas: ["a", "b"]}
    fault: {attempts: [{kind: stream_stall, after_chunk: 3, delay: 1s}]}
`)
		findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameSonar: SonarValidator{}})
		require.True(t, hasScenarioCode(findings, scenario.CodeStreamAfterChunkOutOfRange))
	})

	t.Run("scenario.stream.abort_unreachable — request time, truncate_body claimed on a streaming request", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: t
providers:
  perplexity:
    answer: hi
    stream: {when_requested: stream, deltas: ["hi"]}
    fault: {attempts: [{kind: truncate_body, truncate_after_bytes: 1}]}
`))
		_, _ = s.do(t, http.MethodPost, "/v1/sonar", streamGoldenRequest)
		require.True(t, hasCode(s.findings(t), scenario.CodeStreamAbortUnreachable))
	})

	t.Run("scenario.stream.abort_unreachable — request time, stream_disconnect claimed on a non-streaming request", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: t
providers:
  perplexity:
    answer: hi
    stream: {when_requested: stream, deltas: ["hi"]}
    fault: {attempts: [{kind: stream_disconnect, after_chunk: 0}]}
`))
		// stream: false means this call does not stream, even against a
		// stream-policy entry — the claimed attempt cannot apply.
		resp, body := s.do(t, http.MethodPost, "/v1/sonar",
			`{"model":"sonar-deep-research","messages":[{"role":"user","content":"hi"}],"stream":false}`)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
		require.True(t, hasCode(s.findings(t), scenario.CodeStreamAbortUnreachable))
	})
}

// -----------------------------------------------------------------------------
// Phase 5 unit 3: GrammarTyped on the Agent surface
// -----------------------------------------------------------------------------

// agentStreamGoldenScenario is the corpus
// contracts/perplexity/perplexity-agent-stream.sse is rendered from: a
// three-delta turn on the perplexity_agent entry, deliberately including a
// search_results item so the golden also pins the message item's
// output_index at 1, not 0.
const agentStreamGoldenScenario = `
version: 1
name: deep-research-agent-stream
sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A
providers:
  perplexity_agent:
    response_id: resp_9f2c1d8b7a6e5f4c3b2a1908f7e6d5c4
    message_id: msg_4e3d2c1b0a9f8e7d6c5b4a39281706f5
    model: openai/gpt-5
    answer: "Report A finds that X."
    queries:
      - deterministic simulator adapter tests
    search_results:
      - source: source-a
        snippet: Report A finds that deterministic simulators remove flakiness.
        date: "2025-11-16"
    usage:
      input_tokens: 42
      output_tokens: 128
      total_tokens: 170
      cost:
        input_cost: 0.00021
        output_cost: 0.00128
        total_cost: 0.00149
    stream:
      when_requested: stream
      deltas:
        - "Report A "
        - "finds "
        - "that X."
`

// agentStreamGoldenRequest is the Agent surface's own primary streaming
// shape: stream: true against the input the non-streaming Agent goldens
// already use.
const agentStreamGoldenRequest = `{"input":"what does report A find?","model":"openai/gpt-5","stream":true}`

// TestAgentStreamGolden is the byte-for-byte transcript pin: the
// GrammarTyped sequence for a three-delta turn, rendered through the real
// handler on every one of the three Agent route spellings that share one
// fault budget — the same pattern TestSonarStreamGolden pins for
// GrammarDelta. response_id and message_id are pinned in the fixture, the
// same convention perplexity-agent-happy.json already uses.
func TestAgentStreamGolden(t *testing.T) {
	t.Parallel()

	want := sseGoldenBytes(t, "perplexity-agent-stream.sse")

	for _, path := range []string{"/v1/agent", "/v1/responses", "/responses"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			s := newSim(t, mustScenario(t, agentStreamGoldenScenario))
			resp, body := s.do(t, http.MethodPost, path, agentStreamGoldenRequest)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
			require.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
			require.Empty(t, resp.Header.Get("Content-Length"))
			require.Equal(t, string(want), string(body))
		})
	}
}

// TestAgentStreamDeterministic proves the same scenario and the same request
// render byte-identical GrammarTyped transcripts across separate server
// instances — house rule 2, restated for the second grammar.
func TestAgentStreamDeterministic(t *testing.T) {
	t.Parallel()

	render := func() []byte {
		s := newSim(t, mustScenario(t, agentStreamGoldenScenario))
		_, body := s.do(t, http.MethodPost, "/v1/agent", agentStreamGoldenRequest)
		return body
	}
	require.Equal(t, string(render()), string(render()))
}

// TestAgentStreamFalseServesOrdinaryJSON is the Agent-surface counterpart of
// TestSonarStreamFalseOnAStreamingEntryServesOrdinaryJSON: a request that
// does not itself ask to stream gets the ordinary JSON body, even against an
// entry whose policy is stream.
func TestAgentStreamFalseServesOrdinaryJSON(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, agentStreamGoldenScenario))
	resp, body := s.do(t, http.MethodPost, "/v1/agent",
		`{"input":"what does report A find?","model":"openai/gpt-5","stream":false}`)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.Contains(t, string(body), `"object":"response"`)
	require.NotContains(t, string(body), "data:")

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	require.Nil(t, entries[0].Outcome.Stream)
	require.False(t, hasCode(entries[0].Findings, CodeAgentStreamUnsupported),
		"stream: false never asked to stream, so the unimplemented warning must not fire either")
}

// TestAgentStreamPolicySwitch pins the warn/reject/stream switch on the
// Agent entry (P5U3 spec item 1): the same three-way behaviour Sonar has,
// now on the surface that used to warn unconditionally.
func TestAgentStreamPolicySwitch(t *testing.T) {
	t.Parallel()

	t.Run("default (no stream: key) warns and serves JSON", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, agentCorpus))
		resp, body := s.do(t, http.MethodPost, "/v1/agent", `{"input":"hi","stream":true}`)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
		require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
		require.True(t, hasCode(s.findings(t), CodeAgentStreamUnsupported))
	})

	t.Run("warn: explicit, same as default", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: agent-stream-warn
providers:
  perplexity_agent:
    stream: warn
    answer: hi
`))
		resp, body := s.do(t, http.MethodPost, "/v1/agent", `{"input":"hi","stream":true}`)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
		require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
		require.True(t, hasCode(s.findings(t), CodeAgentStreamUnsupported))
	})

	t.Run("reject: 422 naming body.stream, no attempt consumed", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: agent-stream-reject
providers:
  perplexity_agent:
    stream: reject
    answer: hi
`))
		resp, body := s.do(t, http.MethodPost, "/v1/agent", `{"input":"hi","stream":true}`)
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, "body: %s", body)
		require.Contains(t, string(body), `"stream"`)
		require.True(t, hasCode(s.findings(t), CodeAgentStreamUnsupported))
	})

	t.Run("reject: a request that does not itself ask to stream is served normally", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, `
version: 1
name: agent-stream-reject-not-requested
providers:
  perplexity_agent:
    stream: reject
    answer: hi
`))
		resp, body := s.do(t, http.MethodPost, "/v1/agent", `{"input":"hi","stream":false}`)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
		require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
		require.False(t, hasCode(s.findings(t), CodeAgentStreamUnsupported),
			"a reject policy only fires against a request that itself asks to stream")
	})

	t.Run("stream: serves the GrammarTyped sequence, no unsupported warning", func(t *testing.T) {
		t.Parallel()
		s := newSim(t, mustScenario(t, agentStreamGoldenScenario))
		resp, body := s.do(t, http.MethodPost, "/v1/agent", agentStreamGoldenRequest)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
		require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
		require.False(t, hasCode(s.findings(t), CodeAgentStreamUnsupported),
			"a request that will actually receive a stream must not also carry a warning promising it will not")

		entries := s.journal.Snapshot()
		require.Len(t, entries, 1)
		require.Equal(t, "perplexity.agent.stream", entries[0].Outcome.Label)
	})
}

// typedSSEFrame is one GrammarTyped frame: its "event:" line, its decoded
// "data:" JSON payload (for shape assertions), and that same payload's raw
// bytes verbatim (for byte-equality assertions the map round-trip through
// data would otherwise silently defeat — encoding/json always alphabetises
// a map[string]any on marshal, regardless of the original wire order).
type typedSSEFrame struct {
	event string
	data  map[string]any
	raw   []byte
}

// typedSSEFrames splits a raw GrammarTyped transcript into its frames, in
// order. Unlike sseDataFrames (GrammarDelta, unnamed frames, one bare
// [DONE] sentinel), every frame here carries both an "event:" line and a
// JSON "data:" line, and there is no sentinel to special-case.
func typedSSEFrames(t *testing.T, body []byte) []typedSSEFrame {
	t.Helper()
	var frames []typedSSEFrame
	for _, block := range strings.Split(strings.TrimRight(string(body), "\n"), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.SplitN(block, "\n", 2)
		require.Lenf(t, lines, 2, "frame missing either the event: or data: line: %q", block)
		const eventPrefix, dataPrefix = "event: ", "data: "
		require.True(t, strings.HasPrefix(lines[0], eventPrefix), "frame missing the event: prefix: %q", lines[0])
		require.True(t, strings.HasPrefix(lines[1], dataPrefix), "frame missing the data: prefix: %q", lines[1])
		raw := []byte(strings.TrimPrefix(lines[1], dataPrefix))
		var data map[string]any
		require.NoError(t, json.Unmarshal(raw, &data))
		frames = append(frames, typedSSEFrame{event: strings.TrimPrefix(lines[0], eventPrefix), data: data, raw: raw})
	}
	return frames
}

// TestAgentStreamEventSequence pins the exact event sequence the P5U3 spec
// requires for a turn with N=3 deltas — N+5 events — and that every event's
// "event:" line matches its own payload's "type", and that sequence_number
// is monotonic from 0 (contracts/perplexity/README.md: "Monotonically
// increasing sequence number for event ordering").
func TestAgentStreamEventSequence(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, agentStreamGoldenScenario))
	resp, body := s.do(t, http.MethodPost, "/v1/agent", agentStreamGoldenRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	frames := typedSSEFrames(t, body)
	wantNames := []string{
		EventResponseCreated, EventOutputItemAdded,
		EventOutputTextDelta, EventOutputTextDelta, EventOutputTextDelta,
		EventOutputTextDone, EventOutputItemDone, EventResponseCompleted,
	}
	require.Len(t, frames, len(wantNames), "3 deltas + 5 envelope events")
	for i, want := range wantNames {
		require.Equalf(t, want, frames[i].event, "frame %d event: line", i)
		require.Equalf(t, want, frames[i].data["type"], "frame %d payload type", i)
		require.Equalf(t, float64(i), frames[i].data["sequence_number"], "frame %d sequence_number", i)
	}
	require.NotContains(t, string(body), "[DONE]", "GrammarTyped never writes the chat-completions sentinel")
}

// TestAgentStreamCompletedResponseMatchesNonStreamingBody pins the design's
// "one adopter assertion covers both surfaces" property: response.completed's
// embedded response object is byte-equal — not merely structurally equal — to
// the non-streaming body for the SAME turn — same scenario, same call
// position (a fresh lane each, so both are call index 0), one streamed and
// one not. The comparison decodes the frame's "response" property into
// json.RawMessage rather than through typedSSEFrame.data (map[string]any),
// because a map round-trip always comes back key-alphabetised on remarshal
// (encoding/json's documented behaviour) and would pass even a key-order or
// number-formatting divergence between the two render paths.
func TestAgentStreamCompletedResponseMatchesNonStreamingBody(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		extra string
	}{
		{name: "plain"},
		{name: "with extra_fields", extra: "\n    extra_fields:\n      beta_notice: preview\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sc := agentStreamGoldenScenario + tc.extra

			nonStreaming := newSim(t, mustScenario(t, sc))
			_, jsonBody := nonStreaming.do(t, http.MethodPost, "/v1/agent",
				`{"input":"what does report A find?","model":"openai/gpt-5"}`)

			streaming := newSim(t, mustScenario(t, sc))
			resp, body := streaming.do(t, http.MethodPost, "/v1/agent", agentStreamGoldenRequest)
			require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

			frames := typedSSEFrames(t, body)
			last := frames[len(frames)-1]
			require.Equal(t, EventResponseCompleted, last.event)

			var completed struct {
				Response json.RawMessage `json:"response"`
			}
			require.NoError(t, json.Unmarshal(last.raw, &completed))

			require.Equal(t, string(jsonBody), string(completed.Response),
				"response.completed's response object must be byte-identical to the non-streaming body: "+
					"agentResponse/wire.Render is shared by both transports, never re-implemented for the stream")
		})
	}
}

// TestAgentStreamDisconnect proves the unit-2 stream_* fault kinds are
// genuinely grammar-blind (P5U3 spec item 3, "prove it with one disconnect
// test on /v1/agent"): stream_disconnect aborts the GrammarTyped sequence
// exactly as it aborts GrammarDelta's — chunks before AfterChunk delivered
// whole, the chunk at AfterChunk never reaching the client.
func TestAgentStreamDisconnect(t *testing.T) {
	t.Parallel()

	src := agentStreamGoldenScenario + "\n    fault:\n      attempts:\n        - kind: stream_disconnect\n" +
		"          after_chunk: 3\n          reset: true\n"
	s := newSim(t, mustScenario(t, src))
	resp, body, readErr := s.doStreamRaw(t, "/v1/agent", agentStreamGoldenRequest)
	require.Error(t, readErr, "the connection must die before chunk 3 (the second delta) ever reaches the client")
	require.Empty(t, resp.Header.Get("Content-Length"))

	frames := typedSSEFrames(t, body)
	require.Len(t, frames, 3, "response.created, response.output_item.added and the first delta (chunks 0-2) were "+
		"fully delivered; chunk 3 (the second delta) never was")
	require.Equal(t, EventResponseCreated, frames[0].event)
	require.Equal(t, EventOutputItemAdded, frames[1].event)
	require.Equal(t, EventOutputTextDelta, frames[2].event)

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	require.True(t, entries[0].Outcome.Aborted)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, "responses", so.Grammar)
	require.Equal(t, journal.StreamAborted, so.State)
	require.Equal(t, 3, so.ChunksSent)
	require.NotNil(t, so.AbortAfterChunk)
	require.Equal(t, 3, *so.AbortAfterChunk)
}

// TestAgentStreamJournalOutcome pins the planned half of Outcome.Stream that
// §5.1 says is final before the first byte, on the second grammar: chunk
// count (N+5), terminal index, EventNames (the field this unit adds), usage
// and cost.
func TestAgentStreamJournalOutcome(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, agentStreamGoldenScenario))
	resp, body := s.do(t, http.MethodPost, "/v1/agent", agentStreamGoldenRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, "responses", so.Grammar)
	require.Equal(t, 8, so.ChunkCount, "3 deltas + 5 envelope events")
	require.Equal(t, 7, so.TerminalIndex)
	require.Equal(t, []string{
		EventResponseCreated, EventOutputItemAdded,
		EventOutputTextDelta, EventOutputTextDelta, EventOutputTextDelta,
		EventOutputTextDone, EventOutputItemDone, EventResponseCompleted,
	}, so.EventNames)
	require.JSONEq(t, `{
		"input_tokens":42,"output_tokens":128,"total_tokens":170,
		"cost":{"currency":"USD","input_cost":0.00021,"output_cost":0.00128,"total_cost":0.00149}
	}`, string(so.Usage))
	require.NotNil(t, so.CostTotal)
	require.InDelta(t, 0.00149, *so.CostTotal, 1e-12)
	require.Equal(t, 8, so.ChunksSent)
	require.Equal(t, journal.StreamCompleted, so.State)
}

// TestAgentStreamPacing pins the simulator-chosen envelope pacing rule the
// unit-3 banner (docs/design/streaming.md) states but TestAgentStreamJournalOutcome
// never exercised, since the golden scenario scripts no pace at all:
// response.created and the terminal response.completed use the script's
// ordinary chunk-pace resolution (own override else script default), exactly
// like GrammarDelta's chunk 0 and terminal chunk, while the three
// envelope-only frames with no scenario-authored content of their own —
// response.output_item.added, response.output_text.done and
// response.output_item.done — carry no gap at all. Without this test, a
// regression to "gate every frame" or "gate nothing" would pass every other
// Agent streaming test.
func TestAgentStreamPacing(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, agentStreamGoldenScenario+"      pace: 40ms\n"))
	resp, body := s.do(t, http.MethodPost, "/v1/agent", agentStreamGoldenRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Equal(t, []int64{40, 0, 40, 40, 40, 0, 0, 40}, so.PaceMS,
		"created, delta x3, and completed carry the script's 40ms default; "+
			"output_item.added, output_text.done and output_item.done carry none")
}

// TestAgentStreamOmitUsageNilsUsageInCompletedResponse pins the P5U3-item-3
// rule: terminal.omit_usage nils usage inside response.completed's response
// object, and the journal's planned Usage/CostTotal follow.
func TestAgentStreamOmitUsageNilsUsageInCompletedResponse(t *testing.T) {
	t.Parallel()

	s := newSim(t, mustScenario(t, agentStreamGoldenScenario+`
      terminal:
        omit_usage: true
`))
	resp, body := s.do(t, http.MethodPost, "/v1/agent", agentStreamGoldenRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	frames := typedSSEFrames(t, body)
	last := frames[len(frames)-1]
	require.Equal(t, EventResponseCompleted, last.event)
	response, ok := last.data["response"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, response, "usage", "omit_usage must drop the usage key from response.completed's response")

	entries := s.journal.Snapshot()
	require.Len(t, entries, 1)
	so := entries[0].Outcome.Stream
	require.NotNil(t, so)
	require.Nil(t, so.Usage)
	require.Nil(t, so.CostTotal)
}

// TestAgentStreamDoneIgnoredWarning pins terminal.omit_done's no-op-on-
// GrammarTyped rule (§7, §9): the key is meaningless on the Agent surface,
// which never writes a [DONE] sentinel with or without it, and the load-time
// validator says so rather than silently ignoring it.
func TestAgentStreamDoneIgnoredWarning(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, agentStreamGoldenScenario+`
      terminal:
        omit_done: true
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameAgent: AgentValidator{}})
	var found *scenario.Finding
	for i := range findings {
		if findings[i].Code == CodeStreamDoneIgnored {
			found = &findings[i]
		}
	}
	require.NotNil(t, found, "findings: %+v", findings)
	require.Equal(t, scenario.SeverityWarning, found.Severity)

	s := newSim(t, sc)
	resp, body := s.do(t, http.MethodPost, "/v1/agent", agentStreamGoldenRequest)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	require.NotContains(t, string(body), "[DONE]")
}

// TestAgentStreamAfterChunkOutOfRangeAtLoad pins the ChunkCount override
// this unit adds to scenario.StreamTurn: the entry's single turn scripts 3
// deltas (chunk_count 8, valid range 0..7), and the fault targets chunk 8 —
// in range against the WRONG default formula (len(Deltas)+1 = 4) but out of
// range against the real GrammarTyped count.
func TestAgentStreamAfterChunkOutOfRangeAtLoad(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, agentStreamGoldenScenario+`
    fault:
      attempts:
        - kind: stream_disconnect
          after_chunk: 8
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameAgent: AgentValidator{}})
	var found *scenario.Finding
	for i := range findings {
		if findings[i].Code == scenario.CodeStreamAfterChunkOutOfRange {
			found = &findings[i]
		}
	}
	require.NotNil(t, found, "findings: %+v", findings)
	require.Equal(t, scenario.SeverityError, found.Severity)
}

// TestAgentStreamAfterChunkOutOfRangeAtLoadFailedStatus is
// TestAgentStreamAfterChunkOutOfRangeAtLoad's failed-status regression: a
// turn whose status is failed produces no message output item
// (renderAgentOutput's own rule), so renderAgentStream emits only
// response.created and response.completed — 2 chunks, valid range 0..1 —
// regardless of how many deltas the turn still scripts. Before
// agentChunkCount was made status-aware it returned len(Deltas)+5 = 7 for
// this turn, so after_chunk: 3 loaded clean as "in range" against a bound
// that described a stream that never actually plays back that far; the
// fault would then silently never fire, exactly the outcome
// scenario.ValidateStreamFaultMismatch exists to catch at load rather than
// on a consumer's first request.
func TestAgentStreamAfterChunkOutOfRangeAtLoadFailedStatus(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: agent-stream-failed-chunk-count
providers:
  perplexity_agent:
    status: failed
    error:
      message: it broke
    stream:
      when_requested: stream
      deltas:
        - "a"
        - "b"
    fault:
      attempts:
        - kind: stream_disconnect
          after_chunk: 3
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameAgent: AgentValidator{}})
	var found *scenario.Finding
	for i := range findings {
		if findings[i].Code == scenario.CodeStreamAfterChunkOutOfRange {
			found = &findings[i]
		}
	}
	require.NotNil(t, found, "findings: %+v", findings)
	require.Equal(t, scenario.SeverityError, found.Severity)
}

// TestAgentStreamAfterChunkAtTheLastValidIndexLoadsClean is the boundary
// companion: after_chunk == chunk_count-1 (7, the response.completed frame
// itself) is a legitimate script, not an off-by-one error.
func TestAgentStreamAfterChunkAtTheLastValidIndexLoadsClean(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, agentStreamGoldenScenario+`
    fault:
      attempts:
        - kind: stream_disconnect
          after_chunk: 7
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameAgent: AgentValidator{}})
	require.Empty(t, findings, "after_chunk 7 targets response.completed itself (chunk_count-1), a valid target")
}

// TestAgentStreamDeltasEmptyFailsAtLoad and
// TestAgentStreamWarnWithTruncateBodyStillLoads prove
// scenario.ValidateStreamScripts/ValidateStreamFaultMismatch are wired
// through AgentValidator, the same generic checks TestSonarStreamDeltasEmptyFailsAtLoad
// and TestSonarStreamWarnWithTruncateBodyStillLoads already exercise on the
// Sonar side — both surfaces sharing one implementation is the entire point
// of those functions being exported from scenario rather than duplicated.
func TestAgentStreamDeltasEmptyFailsAtLoad(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: agent-stream-no-deltas
providers:
  perplexity_agent:
    stream: stream
    answer: hello
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameAgent: AgentValidator{}})
	var found *scenario.Finding
	for i := range findings {
		if findings[i].Code == scenario.CodeStreamDeltasEmpty {
			found = &findings[i]
		}
	}
	require.NotNil(t, found, "findings: %+v", findings)
	require.Equal(t, scenario.SeverityError, found.Severity)
}

func TestAgentStreamWarnWithTruncateBodyStillLoads(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: agent-stream-warn-truncate-regression
providers:
  perplexity_agent:
    stream: warn
    answer: hi
    fault:
      attempts:
        - kind: truncate_body
          truncate_after_bytes: 5
`)
	findings := provider.ValidateScenario(sc, map[string]provider.Validator{NameAgent: AgentValidator{}})
	require.Empty(t, findings, "a warn-policy entry combined with truncate_body must load with no findings at all")
}

// hasScenarioCode is hasCode's counterpart for scenario.Finding (a load-time
// authoring problem addressed by YAML path) rather than journal.Finding (a
// per-request observation) — the two types this package's tests check
// findings against, kept distinct rather than unified behind one signature.
func hasScenarioCode(findings []scenario.Finding, code string) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}
