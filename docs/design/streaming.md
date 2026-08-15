# SSE streaming

> ## ⚠ DRAFT — DO NOT IMPLEMENT FROM THIS YET
>
> An adversarial review returned **needs-revision** on 2026-08-15 with one blocker and one major:
>
> - **Blocker:** §9 raises `scenario.fault.stream_mismatch` on the *presence* of a `stream:` key, which would fire
>   against Exa's already-shipped projection. The check must key on the effective policy, not on key presence.
> - Stream suppression is decided inside `execute` against its own copy of the response, so `Handle` journals an
>   entry advertising a stream that never happens.
>
> Revising this is Phase 2 of the adopter plan and gates implementation. Until then treat it as a considered
> starting point, not a specification.

An addendum to [`package-design.md`](package-design.md) and
[`extended-surfaces.md`](extended-surfaces.md). Where the three disagree, this file is newest and wins for streaming
only; nothing here changes the non-streaming path.

It supersedes exactly two prior decisions:

1. Plan non-goal 7 and `extended-surfaces.md`'s "Streaming is still out of scope". The first adopter's primary
   deep-research path **always** streams — `POST /chat/completions`, `stream: true`, `model: sonar-deep-research` —
   so streaming is a must-have, not an option. A simulator that cannot serve their main path cannot test it.
2. `extended-surfaces.md`'s closing note, "Adding streaming means adding an event-sequence projection, and that is a
   scenario-schema version bump." The premise was true when it was written and is false now. See
   [Schema versioning](#8-schema-versioning).

Streaming currently produces a journal warning and an ordinary JSON body: `perplexity.stream.unimplemented`
(`provider/perplexity/request.go:74`), `perplexity.agent.stream.unsupported` (`provider/perplexity/agent.go:31`),
`exa.stream.unimplemented` (`provider/exa/request.go:34`).

A `stream:` scalar in a `respond:` body already chooses between that default (`warn`) and a provider-shaped rejection
(`reject`) on Exa's two routes and on Sonar. On both it is read from the provider block's **first turn only**
(`streamPolicy` in `provider/exa/handler.go` and `provider/perplexity/handler.go`), because rejection has to happen
before turn selection claims a fault attempt; Sonar warns `perplexity.stream.policy.ignored` at startup for a
`stream:` written on any later turn. The Agent surface has no policy knob and always warns.

So those codes survive as the `warn` default, and what this design adds is the third value — `stream`, which serves
the scripted sequence. That value is genuinely per turn, because the turn is what carries the `deltas:`; `warn` and
`reject` stay provider-level for the reason above. See [§2](#2-scenario-yaml) and
[§8](#8-schema-versioning).

---

## 1. Verifying the "the seam is ready" claim

`extended-surfaces.md` closes with: "Both deferred streaming surfaces … need the same thing from the fault engine that
the base design already built for truncated bodies: access to the underlying connection through `http.Hijacker`, or
`Flusher` plus `panic(http.ErrAbortHandler)`. The seam is ready."

That claim was probed against Go 1.26.4 rather than trusted. Every row below is an executed result, not an
expectation.

| Probe | Result | Consequence for this design |
|---|---|---|
| `httptest.NewServer` writer implements `Flusher` and `Hijacker` | both `true`; `http.NewResponseController(w).Flush()` returns `nil` | In-process SSE works in `testkit`. `NewResponseController` is preferred over type assertions for new code. |
| Response with no `Content-Length` | `Transfer-Encoding: chunked`, `ContentLength = -1` | SSE must never set `Content-Length`. |
| Response with `Content-Length` set | `TransferEncoding: []`, identity encoding | Setting it defeats chunked framing. The stream path must not call `applyHeader` paths that add it. |
| Three chunks flushed with 120 ms scripted gaps | client observed 0 s, 122 ms, 121 ms | Pacing is real and observable client-side. Heartbeat tests are possible. |
| `panic(http.ErrAbortHandler)` after two flushed chunks | client got both chunks, then `err` with `errors.Is(err, io.ErrUnexpectedEOF) == true` | Mid-stream disconnect works, and the client-visible error is a stable sentinel. |
| Same, preceded by `Hijack` + `SetLinger(0)` + `Close` | client got both chunks, then `*net.OpError` "connection reset by peer" | RST vs FIN is a distinguishable error class, exactly as `truncate_body` already exploits. |
| Partial frame written (no terminating blank line), then abort | client got `"data: {\"i\":1,\"choi"` then unexpected EOF | Truncated-chunk faults need no new transport mechanism. |
| Handler `defer` + `recover` + re-`panic` around a mid-stream abort | deferred function ran **after** the pre-abort work and the re-panic reached `net/http` | `Handle`'s existing defer shape survives streaming unchanged. |
| Client closes the body after one chunk | `r.Context()` cancelled; server observed `context canceled` at the next chunk boundary | Client hangup is detectable, but only at a yield point — so the write loop must `select` on `ctx.Done()`. |
| Journal append left in the `defer`, client reads chunk 0 | **journal held 0 entries at that moment**; 1 entry after the stream ended | The load-bearing finding. See below. |

**Verdict: the claim is half right, and the missing half is the important one.**

What *is* ready: the abort machinery. `closeBeforeHeaders`, `truncateBody` and `resetAndClose`
(`provider/fault_exec.go:226–281`) already do everything a mid-stream disconnect needs, and `Handle`'s
`defer`/`recover`/re-`panic` (`provider/handle.go:164–170`) already survives it. None of that changes.

What is **not** ready, and is not mentioned in the note:

- **`Response` cannot express a stream.** `Response.Body` is `[]byte` and `execute` writes it in a single
  `writeResponse` call (`provider/fault_exec.go:112`). There is no yield point between bytes, so there is nowhere for
  pacing, for a mid-stream stall, or for a `ctx.Done()` check to live.
- **The journal-visibility invariant is violated by every stream, not only by aborting ones.** Rule 3 of `Handle`'s
  contract — "an aborting fault is journaled *before* the socket is touched" — exists because the client observes the
  abort while the handler goroutine is still unwinding. A stream makes that true of *every* response: the client
  consumes chunk 0 seconds before the handler returns. The probe reproduces it: journal empty at the moment the
  client held chunk 0. Left alone, every streaming test that reads the journal after reading a chunk is a flake, and
  more often under `-race`.

Both gaps are fixed below, additively.

---

## 2. Scenario YAML

The scenario projects **content**; the provider package owns the **wire contract**. A scenario author scripts the
deltas and the pacing; it cannot hand-assemble frames, because the opening role chunk, the `finish_reason` chunk,
the terminal usage chunk and the `[DONE]` sentinel are contract-fixed and getting them wrong is what the simulator
exists to prevent.

```yaml
version: 1
name: deep-research-stream

sources:
  - id: source-a
    url: https://example.test/report-a
    title: Report A

providers:
  perplexity:
    turn_key: ["route", "body_json:model"]
    turns:
      - when:
          body_json:
            model: sonar-deep-research
        respond:
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
            when_requested: stream    # stream | warn | reject; implied "stream" when deltas are declared
            pace: 40ms                # default gap before every chunk
            deltas:
              - "Report A "
              - "finds "
              - text: "that X."
                pace: 250ms           # per-delta override
```

Rules that make this shape work:

- **`stream:` is inside `respond:`**, so it is part of the projection body. `scenario` never decodes it as a
  projection; the provider package does, through the existing `Turn.DecodeProjection`. This is what keeps the change
  out of the schema envelope entirely.
- **`usage:` is the ordinary non-streaming usage projection, reused verbatim.** It is rendered into the terminal
  chunk. One declaration serves both transports, so a scenario cannot drift into quoting one cost when it streams and
  another when it does not — which is precisely the bug an adopter's spend-attribution test would be unable to see.
- **A scalar is still accepted.** `stream: warn` and `stream: reject` keep parsing, decoded as
  `{when_requested: warn}`. This is the `SourceRef` scalar-or-mapping pattern the repository already uses
  (`scenario/model.go`, `DecodeStrict` at line 741), so every existing Exa fixture stays valid byte for byte.
- **Declaring `deltas:` implies `when_requested: stream`.** Writing the script and forgetting the switch would
  otherwise serve a JSON body and a warning, silently.
- **A turn with no `stream:` block keeps today's behaviour** — `warn` — so no existing scenario changes meaning.
- **`Body` is still rendered.** A streaming turn renders the non-streaming body too; it is what a non-streaming caller
  receives and what a stream-suppressing fault writes. See [§4.4](#44-faults-that-suppress-the-stream).

### 2.1 Scripting a stream the adopter's four ways

```yaml
# 1. Mid-stream disconnect, RST rather than FIN.
fault:
  attempts:
    - kind: stream_disconnect
      after_chunk: 3
      reset: true

# 2. Truncated chunk: chunks 0..1 complete, then 12 bytes of chunk 2, then the socket dies.
fault:
  attempts:
    - kind: stream_truncate_chunk
      after_chunk: 2
      truncate_after_bytes: 12

# 3. Transient blip then retry (REQ-AGENT-DR-INTERNAL-RETRY-001). Not a new mechanism:
#    the existing attempt list already expresses it.
fault:
  after: success
  attempts:
    - kind: stream_disconnect
      after_chunk: 2

# 4a. Slow chunk pacing is not a fault at all — it is the script.
respond:
  stream:
    pace: 12s          # every gap exceeds the Temporal heartbeat interval

# 4b. A single stall that exceeds the activity timeout, mid-stream, without aborting.
fault:
  attempts:
    - kind: stream_stall
      after_chunk: 4
      delay: 65s
```

Two traps worth naming, because both produce a green test that proved nothing:

- **The retry must land in the same lane.** Attempt 1 is served only if the retried request resolves to the same
  cursor key. With `turn_key: ["route", "body_json:model"]`, a retry that changes `model` is a *different lane* and
  draws attempt 0 again — it will be disconnected a second time, forever. `Lane.CursorKey`
  (`provider/lane.go:124`) is the authority on what "same lane" means.
- **`stream_stall` under `DelaySkip` does not stall.** Stream pacing honours `Deps.DelayMode` exactly as fault delays
  do, so `testkit.WithSkippedDelays` makes a 65 s stall free — and useless for a timeout assertion. A test that
  asserts a Temporal timeout must run under the default `DelayReal`. `Outcome.Stream.PaceMS` still records the
  planned schedule under either mode, which is what a test asserting "the scenario asked for 12 s gaps" compares
  against.

---

## 3. Go types

Every declaration is the real signature. Additions only; no existing field changes type or meaning.

### 3.1 `scenario` — the projection grammar

```go
// StreamPolicy selects what a provider does with a request that asks to stream.
type StreamPolicy string

// Supported streaming policies. Warn is unchanged and remains the default, so a
// scenario that declares no stream script behaves exactly as it did before.
const (
	StreamWarn   StreamPolicy = "warn"   // journal warning, ordinary JSON body
	StreamReject StreamPolicy = "reject" // provider-shaped 4xx
	StreamServe  StreamPolicy = "stream" // serve the scripted SSE sequence
)

// StreamScript is the provider-neutral streaming projection: what to say, and how
// slowly. It is deliberately not a list of frames. Frame assembly — the opening
// role delta, the finish_reason chunk, the terminal usage chunk, the [DONE]
// sentinel — is the vendor's contract and belongs to the provider package, which
// is the same split the non-streaming path already makes between a projection and
// a renderer.
//
// It lives in scenario rather than in a provider package because all three
// providers stream the same way and a second copy of this grammar is a second
// chance for two providers to spell pacing differently.
type StreamScript struct {
	// Policy is the YAML key "when_requested". Empty means StreamServe when
	// Deltas is non-empty and StreamWarn otherwise: writing a script and
	// forgetting the switch must not silently serve JSON.
	Policy StreamPolicy `yaml:"when_requested,omitempty"`

	// Pace is the default minimum gap before every chunk. Zero writes the whole
	// sequence as fast as the socket accepts it.
	Pace Duration `yaml:"pace,omitempty"`

	// Deltas are the incremental content fragments, in order. Concatenated they
	// should equal the projection's non-streaming answer; validation warns when
	// they do not, because a consumer that reassembles the stream and compares it
	// against a non-streaming golden would otherwise fail for a fixture reason.
	Deltas []StreamDelta `yaml:"deltas,omitempty"`

	// Terminal tunes the closing frames. Nil means the vendor-faithful default.
	Terminal *StreamTerminal `yaml:"terminal,omitempty"`
}

// UnmarshalYAML accepts the scalar shorthand, so `stream: warn` — the form every
// existing Exa fixture uses — keeps parsing as {when_requested: warn}. The mapping
// branch decodes strictly, exactly as decodeRefOrMapping does.
func (s *StreamScript) UnmarshalYAML(value *yaml.Node) error

// EffectivePolicy applies the "deltas imply stream" default. Nil-safe.
func (s *StreamScript) EffectivePolicy() StreamPolicy

// StreamDelta is one content fragment and the gap that precedes it.
type StreamDelta struct {
	Text string   `yaml:"text"`
	Pace Duration `yaml:"pace,omitempty"` // overrides StreamScript.Pace for this chunk
}

// UnmarshalYAML accepts a scalar as the shorthand for {text: <scalar>}.
func (d *StreamDelta) UnmarshalYAML(value *yaml.Node) error

// StreamTerminal scripts the closing frames. Every field exists to express a
// vendor-drift shape a consumer must survive, and each is a scenario knob rather
// than a fault because the stream still closes cleanly: a missing usage object is
// a well-formed response with a hole in it, not a transport failure.
type StreamTerminal struct {
	// OmitUsage drops the usage object from the terminal chunk. It is the
	// streaming half of the adopter's usage/cost edge pack.
	OmitUsage bool `yaml:"omit_usage,omitempty"`

	// OmitDone drops the "data: [DONE]" sentinel on the chat-completions grammar
	// while still closing the connection cleanly. A consumer that waits for the
	// sentinel hangs until its own deadline; a consumer that waits for EOF does
	// not. That difference is worth being able to script, and it is NOT the same
	// as stream_disconnect, which produces an unexpected EOF.
	OmitDone bool `yaml:"omit_done,omitempty"`

	// Pace overrides the gap before the terminal chunk.
	Pace Duration `yaml:"pace,omitempty"`
}
```

Three additive fault kinds and one additive attempt field:

```go
// Streaming fault kinds. Unlike every other kind, these are never INFERRED from
// the fields present: after_chunk alone is ambiguous between all three, and
// guessing wrong would cut a connection where the author asked for a pause.
// scenario.Validate raises scenario.fault.after_chunk.not_streaming when
// after_chunk appears on a kind that is not one of these.
const (
	// FaultStreamDisconnect writes chunks [0, AfterChunk) in full and then
	// destroys the connection. Chunk AfterChunk never reaches the client.
	FaultStreamDisconnect FaultKind = "stream_disconnect"

	// FaultStreamTruncateChunk writes chunks [0, AfterChunk) in full, then
	// TruncateAfterBytes bytes of chunk AfterChunk, then destroys the connection.
	// The distinction from FaultStreamDisconnect is not cosmetic: this delivers a
	// MALFORMED FRAME, which is a different branch of a consumer's SSE parser
	// than a stream that ended at a frame boundary.
	FaultStreamTruncateChunk FaultKind = "stream_truncate_chunk"

	// FaultStreamStall inserts Delay before chunk AfterChunk and then continues
	// normally. Nothing is aborted; the client's own deadline decides what
	// happens, which is the point for a Temporal activity timeout or a missed
	// heartbeat.
	FaultStreamStall FaultKind = "stream_stall"
)

// FaultAttempt gains one field:

	// AfterChunk is the zero-based index of the first chunk the fault affects.
	// Chunks before it are always delivered whole. It is meaningful only for the
	// three stream_* kinds.
	//
	// For FaultStreamStall, Delay is the mid-stream pause rather than the
	// time-to-first-byte delay every other kind gives it. A stall that also wants
	// a slow first byte declares two attempts, or a scripted first-chunk pace.
	AfterChunk int `yaml:"after_chunk,omitempty"`
```

`Reset` and `TruncateAfterBytes` are reused unchanged, keeping one spelling of "RST not FIN" and one of "how many
bytes first" across the streaming and non-streaming catalogue.

### 3.2 `provider` — transport

```go
// SSEGrammar names the Server-Sent Events dialect a stream is written in. The two
// in play differ only in whether frames are named; see §6.
type SSEGrammar string

// The simulated grammars.
const (
	// GrammarDelta is the OpenAI-compatible chat-completions dialect: unnamed
	// frames whose payload is a chat.completion.chunk object, closed by the bare
	// token [DONE].
	GrammarDelta SSEGrammar = "chat_completions"

	// GrammarTyped is the Responses/Agent dialect: every frame carries an
	// "event:" line naming one of the published EventType members, and the
	// payload repeats the name in its "type" property.
	GrammarTyped SSEGrammar = "responses"
)

// SSEEvent is one frame before encoding. Provider packages build these; the
// encoder below turns them into bytes.
type SSEEvent struct {
	// Name fills the "event:" line. Empty omits the line entirely, which is the
	// chat-completions grammar.
	Name string

	// Data is written verbatim after "data: ". It is normally compact JSON, and
	// is []byte rather than a struct because the [DONE] sentinel is a bare token
	// and not a JSON value at all.
	Data []byte

	// Pace is the minimum wall time between the previous frame reaching the wire
	// and this one starting.
	Pace time.Duration

	// Terminal marks the frame carrying usage and cost. Exactly zero or one frame
	// in a sequence may set it; EncodeSSE panics on a second, because a stream
	// with two terminal chunks is a fixture bug that would otherwise surface as a
	// consumer double-counting spend.
	Terminal bool
}

// StreamChunk is one fully encoded frame. Bytes are final: the plan is complete
// before the first byte is written, which is what makes the journal safe to read
// as soon as the client has seen anything.
type StreamChunk struct {
	Bytes    []byte
	Pace     time.Duration
	Name     string // the "event:" value, for the journal; empty on GrammarDelta
	Terminal bool
}

// Stream is a fully rendered SSE response.
type Stream struct {
	Grammar SSEGrammar
	Chunks  []StreamChunk

	// Usage is the terminal chunk's usage object, verbatim, lifted out so the
	// journal can carry it without re-parsing a frame. Nil when the script omits
	// usage.
	Usage json.RawMessage

	// CostTotal is the total the terminal chunk declares, lifted from whichever
	// vendor field carries it — usage.cost.total_cost on Sonar, usage.cost.total_cost
	// on the Agent surface, costDollars.total on Exa. It exists so a cross-provider
	// spend assertion is one field read rather than three vendor-specific ones.
	// Usage remains the authority; this is a convenience and is nil when the
	// script omits usage.
	CostTotal *float64
}

// Bytes returns the total the plan will write, which is known before the first
// write and is what Outcome.Stream.BytesPlanned records.
func (s *Stream) Bytes() int

// EncodeSSE encodes events into chunks. Framing is fixed and deterministic:
// an optional "event: <name>\n" line, then one "data: " line per line of Data
// (payloads are compact JSON and contain none, but the SSE grammar requires the
// split and a payload that grows a newline must not silently split a frame), then
// one blank line. Nothing here reads a clock or a map.
func EncodeSSE(events []SSEEvent) []StreamChunk
```

`Response` gains exactly one field:

```go
	// Stream, when non-nil, is written instead of Body as a Server-Sent Events
	// sequence. Nil for every non-streaming response, which is every response
	// this repository shipped before streaming existed.
	//
	// Body must STILL be populated when Stream is set. It is what a non-streaming
	// caller of the same turn receives, and what a stream-suppressing fault
	// writes; see §4.4. The two are rendered from one projection, so a scenario
	// cannot quote one cost when it streams and another when it does not.
	Stream *Stream
```

---

## 4. How a stream flows through `Handle`, `Response` and `fault_exec`

### 4.1 The handler

`handleSonar` gains one branch after rendering, and nothing before it moves. Routing, authentication, validation and
turn selection are untouched, so a rejected request still consumes no attempt (§4.4 of the package design).

```go
	body, err := renderSonar(x, &p, model)
	// ... unchanged ...
	resp := provider.Response{
		Status: http.StatusOK, Body: body, Label: "perplexity.sonar.ok",
		FaultEligible: true, FaultBody: faultBody(SurfaceSonar),
	}
	if wantsStream(x) {
		switch p.Stream.EffectivePolicy() {
		case scenario.StreamServe:
			resp.Stream = renderSonarStream(x, &p, model) // *provider.Stream, GrammarDelta
			resp.Label = "perplexity.sonar.stream"
			resp.Header = streamHeader()
		case scenario.StreamReject:
			return errorResponse(SurfaceSonar, http.StatusBadRequest, "streaming is not enabled for this turn")
		default:
			x.Warn(CodeStreamUnimplemented, "body.stream", "...") // unchanged today's behaviour
		}
	}
	return resp
```

### 4.2 `Handle` — one condition widens

```go
	out := faultOutcome(dec, resp)
	// ...
	if out.Aborted || resp.Stream != nil {
		entry.Outcome = out
		record() // journal BEFORE the client can observe ANYTHING
	}
	entry.Outcome = execute(r.Context(), w, dec.Attempt, resp, d.DelayMode, out, closer)
```

This is a two-word change and it is the same invariant, stated more generally. Today's rule is "journal before the
socket is touched destructively". A stream makes the client an observer from the first flush, so the rule becomes
**journal before the client can observe anything the handler is about to do**. The aborting case was always the
special case; streaming is the general one.

`closer` is new. It is how `execute` reports the *observed* close back without importing anything:

```go
	// closeStream applies the observed half of a streamed exchange to the entry
	// that record() already appended. It is idempotent for the same reason record
	// is: executeStream calls it before touching the socket destructively, and the
	// deferred fallback below calls it again for a request that never got there.
	closed := false
	closer := func(c journal.StreamClose) {
		if closed {
			return
		}
		closed = true
		if !journal.CloseStreamIn(d.Journal, x.lane.Namespace, x.Seq, c) {
			warnOnce(d.Logger, "journal.stream_not_amendable")
		}
	}

	defer func() {
		rec := recover()
		record()
		if resp.Stream != nil {
			closer(journal.StreamClose{State: journal.StreamClientGone})
		}
		if rec != nil {
			panic(rec)
		}
	}()
```

### 4.3 `execute` — one branch, before the existing switch

```go
func execute(ctx context.Context, w http.ResponseWriter, a *scenario.FaultAttempt,
	resp Response, mode DelayMode, out journal.Outcome, closer func(journal.StreamClose),
) journal.Outcome {
	// The delay runs first for every kind, unchanged. For a stream it is
	// time-to-first-byte, which is what a consumer's connect timeout observes.
	// ... existing delay block, verbatim ...

	switch {
	case resp.Stream == nil:
		// Everything below this line is today's code, untouched.
	case a != nil && suppressesStream(a.EffectiveKind()):
		resp = suppressStream(resp) // drop Stream, restore the JSON Content-Type
		// falls through to today's code
	default:
		return executeStream(ctx, w, a, resp, mode, out, closer)
	}
	// ... existing switch on EffectiveKind, unchanged ...
}
```

`executeStream` is the only genuinely new machinery, and it is small:

```go
// executeStream writes the scripted sequence. It never renders anything: the plan
// is complete and encoded before it is called, which is what lets Handle journal
// the whole exchange first.
func executeStream(ctx context.Context, w http.ResponseWriter, a *scenario.FaultAttempt,
	resp Response, mode DelayMode, out journal.Outcome, closer func(journal.StreamClose),
) journal.Outcome {
	plan := planStream(a, resp.Stream) // pure; resolves abort index, truncation length, stall

	applyHeader(w, resp.Header)
	// Deliberately NO Content-Length: setting it switches net/http to identity
	// encoding and defeats chunked framing (verified, §1).
	w.WriteHeader(statusOr(resp.Status))
	rc := http.NewResponseController(w)
	_ = rc.Flush() // the status line and headers reach the client now

	written, sent := 0, 0
	for i, c := range plan.Chunks {
		if err := sleep(ctx, plan.PaceOf(i), mode); err != nil {
			// The client's own deadline ended the request mid-stream. Nothing more
			// is written; the journal says how far we got.
			return closeWith(out, closer, written, sent, journal.StreamClientGone)
		}
		b := c.Bytes
		if plan.TruncateAt == i {
			b = b[:plan.TruncateBytes]
		}
		n, err := w.Write(b)
		written += n
		_ = rc.Flush() // without this the bytes sit in net/http's buffer and
		               // ErrAbortHandler discards them: a connection fault, not a
		               // truncation fault. Same reason truncateBody flushes today.
		if err != nil {
			return closeWith(out, closer, written, sent, journal.StreamClientGone)
		}
		sent++

		if plan.AbortAt == i {
			out = closeWith(out, closer, written, sent, journal.StreamAborted)
			if plan.Reset {
				hijackReset(w) // the existing resetAndClose, extracted
			}
			// A plain return would send the terminating zero-length chunk and the
			// client would see a CLEAN EOF — a complete stream, not a disconnect.
			// The panic is what produces io.ErrUnexpectedEOF (verified, §1).
			panic(http.ErrAbortHandler)
		}
	}
	return closeWith(out, closer, written, sent, journal.StreamCompleted)
}
```

Note the ordering inside the abort branch: `closeWith` runs **before** `hijackReset` and before the panic. That is the
same discipline `Handle` applies to `record()`, for the same reason — after the RST the client can already observe the
abort, so anything a test might read has to be durable by then.

### 4.4 Faults that suppress the stream

A real vendor does not answer a `stream: true` request with a 429 wrapped in SSE. The error happens before the stream
starts, so the response is an ordinary JSON error. `suppressesStream` therefore returns true for `status`,
`invalid_json`, `wrong_content_type`, `empty_body`, `extra_fields` and `close_before_headers`, and the request is
served by today's code path against `Response.Body` and `Response.FaultBody`.

`suppressStream` must reset `Content-Type`. `faultHeader` copies the handler's header first
(`provider/fault_exec.go:186–190`), so `text/event-stream` would otherwise leak onto a JSON error body — a fidelity
bug that would teach a consumer to parse the wrong thing.

`truncate_body` is the one non-streaming kind that is **rejected at load** on a streaming turn
(`scenario.fault.stream_mismatch`), naming `stream_truncate_chunk` as the streaming spelling. Silently reinterpreting
it would be the wrong kind of helpful. The mirror check applies too: a `stream_*` kind on a turn that declares no
`stream:` block is a load-time error.

---

## 5. The journal

**One entry per streamed exchange, appended before the first byte, with chunk metadata in the outcome.**

Not one entry per chunk. `AssertRequestCount`, every `Seq` expectation, the attempt/`call_index` correspondence and
`AssertNamespacesIsolated`'s "indices 0, 1, 2 with no gap" check all assume one entry per request. N entries per
stream would break all of them to record something no adopter assertion needs.

### 5.1 What is recorded, and when

```go
// StreamState is how far a streamed exchange got.
type StreamState string

const (
	StreamOpen       StreamState = "open"        // appended, not yet closed
	StreamCompleted  StreamState = "completed"   // every scripted chunk delivered
	StreamAborted    StreamState = "aborted"     // the scenario's scripted abort fired
	StreamClientGone StreamState = "client_gone" // the client hung up or timed out first
)

// StreamOutcome is what a streamed exchange planned and then delivered. Every
// field above the line is PLANNED and is final when the entry is appended; every
// field below it is OBSERVED and is filled in by CloseStream.
type StreamOutcome struct {
	Grammar SSEGrammar `json:"grammar"`

	// ---- planned: final at append, never amended -------------------------
	ChunkCount   int     `json:"chunk_count"`
	BytesPlanned int     `json:"bytes_planned"`
	PaceMS       []int64 `json:"pace_ms,omitempty"`

	// EventNames is each frame's "event:" value in order. It is empty for
	// GrammarDelta, which has no such lines — which is how a reader tells the two
	// grammars apart without parsing a byte.
	EventNames []string `json:"event_names,omitempty"`

	// TerminalIndex is the chunk carrying usage and cost, or -1.
	TerminalIndex int `json:"terminal_index"`

	// Usage is the terminal chunk's usage object verbatim. This is the adopter's
	// spend-attribution read, and it is final before the client has seen a byte.
	Usage json.RawMessage `json:"usage,omitempty"`

	// CostTotal is the same number lifted to a provider-neutral field.
	CostTotal *float64 `json:"cost_total,omitempty"`

	// AbortAfterChunk and TruncatedAtByte record the SCRIPTED fault, not what
	// happened. Nil when the script aborts nothing.
	AbortAfterChunk *int `json:"abort_after_chunk,omitempty"`
	TruncatedAtByte *int `json:"truncated_at_byte,omitempty"`
	StallBeforeMS   *int64 `json:"stall_before_ms,omitempty"`

	// ---- observed: written by CloseStream --------------------------------
	State      StreamState `json:"state"`
	ChunksSent int         `json:"chunks_sent"`
}
```

`Outcome` gains `Stream *StreamOutcome json:"stream,omitempty"`, nil for every non-streaming request, so no existing
journal consumer sees a changed shape.

Two existing fields keep their exact meanings, which is what makes them trustworthy across the change:

- **`Outcome.BytesWritten` is always observed, never planned.** It is `0` at append and is filled in by the close.
  `Stream.BytesPlanned` is the plan. A reader never has to ask which one it is holding.
- **`Outcome.Aborted` reflects the script**, set at append, exactly as it already is for `truncate_body`
  (`provider/fault_exec.go:83–85`).

`CompletedAt` is the one field whose meaning is genuinely time-dependent, so it is stated rather than left to be
inferred: **for a streamed exchange, `completed_at` is the instant the response was decided, until the close amends it
to the instant the last byte was written.** `outcome.stream.state` says which one you are holding — `open` means the
first, anything else means the second. `AssertOverlapped` is therefore correct for streams read after close and
meaningless for streams read while open, which `testkit` enforces by making the wait explicit (§5.3).

### 5.2 Amending an appended entry

The close needs to reach an entry that is already stored. `Journal` is a consumer-implementable interface — adding a
method breaks every implementation outside this repository — so the close is an **optional capability**, declared and
reached exactly the way `Namespaced` already is (`internal/journal/entry.go:236`):

```go
// StreamCloser is a Journal that can complete a streamed entry it already holds.
//
// It is deliberately narrow rather than a general Amend(func(*Entry)). A general
// mutator could rewrite the body or the findings and would need re-redaction; this
// one can only write the four observed fields of a stream, none of which can carry
// a credential.
type StreamCloser interface {
	Journal

	// CloseStream completes the entry with this sequence number in this namespace.
	// It is a no-op for an unknown sequence, which is what a journal whose ring
	// already evicted the entry does.
	CloseStream(namespace string, seq uint64, c StreamClose)
}

// StreamClose is the observed reality of a streamed exchange.
type StreamClose struct {
	CompletedAt  time.Time
	BytesWritten int
	ChunksSent   int
	State        StreamState
}

// CloseStreamIn completes a streamed entry in j, reporting whether j could.
//
// False means the journal does not implement the capability and NOTHING was
// written: the entry keeps its planned fields and state "open" forever. That is
// honest and visible, which is the only acceptable degradation — silently
// reporting a stream as completed when nothing confirmed it is the failure this
// whole section exists to avoid. Ring implements it; a consumer's own Journal
// need not.
func CloseStreamIn(j Journal, namespace string, seq uint64, c StreamClose) bool
```

`Ring` already holds entries under a lock and indexes them by namespace, so the implementation is a bounded scan of
one namespace's slice for the sequence number. `testkit` re-exports `StreamOutcome`, `StreamState`, `StreamClose`
and the four state constants, because the alias set must stay closed under "types a consumer has to name" (§1.3 of
the package design) and `examples/adapter` guards that.

### 5.3 When it is safe to read

This is the rule, and it is short because the split above was chosen to make it short:

| You want to assert on | Safe to read | How |
|---|---|---|
| request shape, headers, auth, findings | as soon as the client has seen **any** byte of the stream | `sim.Requests(...)` |
| `usage`, `cost_total`, chunk count, planned pacing, grammar, event names | as soon as the client has seen **any** byte | `outcome.stream.*` |
| `bytes_written`, `chunks_sent`, `state`, `completed_at` | only after the exchange closes | `testkit.AwaitStreamClosed(tb, sim, seq)` |

The middle row is the point of the whole design. Everything the adopter's spend attribution and request-shape
assertions read is final before the first flush, so those tests need no waiting and cannot flake.

The third row needs a wait for the same reason the second does not: the client sees `[DONE]` before the handler
returns. `AwaitStreamClosed` polls with a deadline, exactly as the existing `Sim.AwaitRequests`
(`testkit/server.go:519`) already does for arrival.

`testkit` also gains two assertions so consumers do not hand-roll them:

```go
// AssertStreamUsage asserts the terminal chunk declared this usage object.
func AssertStreamUsage(tb testing.TB, e Entry, want any)

// AssertStreamPacing asserts the entry's PLANNED inter-chunk gaps. It reads the
// plan, not the wall clock, because the server cannot prove what the client
// observed — a test that needs the observed gaps times its own reads, which is
// the only place that fact exists.
func AssertStreamPacing(tb testing.TB, e Entry, want ...time.Duration)
```

Chunk **bytes** are deliberately not journaled. A stream is unbounded where a request body is not, `MaxJournalBodyBytes`
bounds only the request, and the consumer already holds every byte — it is the client. Golden-file regression over an
SSE exchange is taken client-side, over the reassembled stream.

---

## 6. Determinism

### 6.1 Byte-identical chunk sequences

The chunk sequence is rendered by the same machinery as a non-streaming body and inherits its guarantees verbatim:
identifiers from `internal/ids` over stable fixture keys, timestamps from `Scenario.BaseTime()`, JSON through
`internal/wire` with `encoding/json`'s deterministic key ordering, no `time.Now()`, no randomness, no map iteration.
The same request at the same call index in the same lane produces the same bytes, and — as §3.1 of the package
design already establishes — a *different* call index deliberately produces different identifiers, so chunk `id`
fields advance across calls the way a real vendor's do.

Framing is fixed by `EncodeSSE`: optional `event:` line, one `data:` line per line of payload, one blank line. Payloads
are compact JSON and contain no newline, so in practice every frame is exactly two or three lines.

### 6.2 What is *not* byte-stable, and must not be asserted on

Chunked transfer encoding means the **TCP read boundaries a client observes are not deterministic**. The probe in §1
shows an 18-byte body arriving as `"data: a\n"` then `"\ndata: b\n\n"` — one frame split across two reads, purely from
segmentation. A golden taken over `read()` boundaries will flake. Goldens are taken over the reassembled stream or over
parsed frames, and `contracts/` records that as a rule rather than leaving it to be rediscovered.

### 6.3 Pacing without wall-clock dependence

Pacing is deterministic in the only sense that is achievable and the only sense that is useful:

- Every gap is a **lower bound** declared by the scenario, honoured by the existing
  `sleep(ctx, d, mode)` (`provider/clock.go:66`). Observed gaps are `d + ε`; the probe measured 121–122 ms for a
  scripted 120 ms.
- **No chunk's content depends on elapsed time.** The simulator never reads a clock to decide what to send, only to
  decide when. Bytes are identical whether the stream took 200 ms or 20 s, so a golden is stable across `DelayReal`
  and `DelaySkip` alike.
- **`DelayMode` governs pacing**, so a 12 s heartbeat scenario costs nothing in a unit test under
  `testkit.WithSkippedDelays`, and the planned schedule is still in `outcome.stream.pace_ms` for the assertion. A test
  that must observe a real timeout runs under the default `DelayReal` — the same rule `DelaySkip` already carries.
- **No fake clock.** The repository deliberately has none (`provider/clock.go:16`), and streaming does not change
  that: a client deadline and a Temporal heartbeat are both observed by *bytes not arriving*, which no server-side
  fake can produce.

---

## 7. The two SSE grammars

They are genuinely different and both are in scope, because the adopter's client uses the first and their migration
target uses the second.

**Chat completions (`GrammarDelta`)** — `POST /chat/completions`, `POST /v1/sonar`. Unnamed frames; the payload is a
`chat.completion.chunk` object; `usage` and `usage.cost` ride on the terminal chunk; the sequence closes with the bare
token `[DONE]`.

```text
data: {"id":"...","object":"chat.completion.chunk","created":1767225600,"model":"sonar-deep-research","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"...","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Report A "},"finish_reason":null}]}

data: {"id":"...","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":19,"completion_tokens":240,"total_tokens":259,"cost":{"total_cost":0.0128}},"search_results":[...]}

data: [DONE]

```

**Responses / Agent (`GrammarTyped`)** — `POST /v1/agent`, `POST /v1/responses`. Every frame carries an `event:` line
naming one of the fourteen published `EventType` members
([`contracts/perplexity/README.md`](../../contracts/perplexity/README.md#eventtype-streaming)), and the payload repeats
the name in `type`. The terminal frame is `response.completed`, whose payload is the whole `ResponsesResponse` —
`usage`, `cost` and the `output[]` trace included.

```text
event: response.created
data: {"type":"response.created","response":{"id":"resp_...","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Report A "}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_...","status":"completed","output":[...],"usage":{"input_tokens":19,"output_tokens":240,"cost":{"total_cost":0.0128,"currency":"USD"}}}}

```

**One mechanism serves both.** The split is the one the non-streaming path already makes: `provider` owns transport,
`provider/<name>` owns the wire contract. Concretely, everything in §3.2, §4 and §5 — `SSEEvent`, `EncodeSSE`,
`Stream`, `executeStream`, all three fault kinds, `StreamOutcome`, the append-before-first-byte rule, every
determinism property — is grammar-blind. The grammars differ in exactly two places, both inside a provider package:

1. Whether `SSEEvent.Name` is set. `GrammarDelta` leaves it empty and `EncodeSSE` omits the `event:` line.
2. The payload renderer, and where usage lives inside it.

Two consequences worth stating because they are the places a shared mechanism could have gone wrong:

- The `[DONE]` sentinel is a chat-completions concept only. It is an `SSEEvent` with `Data: []byte("[DONE]")` and is
  emitted by the Sonar renderer, never by the transport. `StreamTerminal.OmitDone` has no effect on `GrammarTyped`,
  and validation says so rather than silently ignoring it.
- `Stream.Usage` is populated by the renderer that knows where usage lives, so the journal's spend fields are
  identical across grammars even though the wire shapes are not. That is what lets one adopter assertion cover both
  surfaces, which is the entire reason for reconciling them now rather than after the migration.

**Route reconciliation.** The adopter's client calls `/chat/completions` with `stream: true` and
`model: sonar-deep-research` — already an accepted model (`provider/perplexity/request.go:100`). `GrammarDelta` on
`/chat/completions` and `/v1/sonar` is therefore the path that must land first; `GrammarTyped` on `/v1/agent` and
`/v1/responses` lands second, before their migration rather than before their adoption. Exa's deferred SSE on
`/search` and `/answer` stays deferred: no adopter code calls it, and unlike the `/agent/runs` claim in
`contracts/exa/README.md`, that one is still true.

---

## 8. Schema versioning

**Additive to version 1. Not version 2.** `extended-surfaces.md` proposed the bump; that proposal is withdrawn here,
for four reasons, the second of which is decisive.

**1. A bump breaks every existing fixture, in every consuming repository, on upgrade day.** The gate is strict
equality (`scenario/load.go:70`): a `version: 1` file loaded by a `version: 2` build fails with "scenario declares
version 1, but this build of Servicesim understands only version 2". `extended-surfaces.md`'s own argument for landing
the turn model early — that a schema break "is an N-repository event, not a one-repository event", and that the
migration cost lands as YAML someone else has to rewrite — applies against the bump it proposed. N is no longer zero.

**2. The premise is obsolete.** The note says streaming "means adding an event-sequence projection, and that is a
scenario-schema version bump". That was written when `Providers` was a closed struct in `scenario` and every
projection field lived there. The open-registry change moved projection bodies out: `scenario` keeps a turn's
`respond:` as an opaque `yaml.Node` and the provider package decodes it (`scenario/model.go:602`). `stream:` is a
projection field. The schema **envelope** — `version`, `sources`, `providers`, `turns`, `when`, `turn_key`, `fault` —
gains nothing but three fault-kind constants and one optional `after_chunk`. There is no event-sequence projection in
`scenario` to version.

**3. Nothing existing changes meaning.** Every added key is optional and absent from every shipped fixture. The one
key that changes *shape* — `stream:` from scalar to mapping — is widened, not replaced: the scalar form still decodes,
via the same scalar-or-mapping unmarshaler `SourceRef`, `ExaResult` and `PerplexityResult` already use
(`scenario/model.go`). A schema
version exists to signal "your file means something different now", and no file does.

**4. Feature-level capability signals beat a file-level integer, and the repository already has them.** A v1 build
meeting `kind: stream_disconnect` today produces `scenario.fault.kind.unknown` naming the kind
(`scenario/validate.go:312`). `stream:` is the same story, and this build already demonstrates it: a scalar outside
the set it understands fails startup validation with `perplexity.stream.policy.unknown` addressed at
`providers.perplexity.turns[0].respond.stream` — so a v1 build meeting `when_requested: stream` today fails with
`perplexity.projection.invalid` at `providers.perplexity.turns[0].respond`, because the mapping form does not decode
into today's scalar `StreamPolicy`. Both name the missing feature and the file location, before readiness reports
true. "This file is version 2" names neither. That matters more
with every item on the adopter's backlog: MCP-mode, an ODR provider profile, enforced rate limits, a callback
injector and a cross-provider async-job machine will land on independent timelines, and one monotonic integer cannot
express "streaming yes, MCP not yet".

**What would earn version 2**, recorded so the next author has a test rather than a precedent: a change that
*reinterprets* an existing envelope key — repurposing `when`, changing `turn_key`'s default lane, altering what
`fault.after: success` means. Streaming makes none of those. When that day comes, the gate should widen to accept a
range in the same change, so a v2 build can still load v1 files and the break is one-directional.

---

## 9. Validation findings this adds

All are load-time unless marked, so a bad streaming fixture fails at readiness rather than on a consumer's first call.

| Code | Severity | Raised when |
|---|---|---|
| `scenario.stream.policy.unknown` | error | `when_requested` is not `warn`, `reject` or `stream` |
| `scenario.stream.deltas_empty` | error | policy is `stream` and `deltas` is empty |
| `scenario.stream.answer_mismatch` | warning | concatenated `deltas` do not equal the projection's `answer` |
| `scenario.fault.after_chunk.not_streaming` | error | `after_chunk` set on a kind that is not `stream_*` |
| `scenario.fault.stream_mismatch` | error | a `stream_*` kind on a turn with no `stream:` block, or `truncate_body` on one that has |
| `scenario.fault.after_chunk.out_of_range` | error | `after_chunk` exceeds the chunk count the provider's grammar will produce |
| `stream.abort_unreachable` | error (per request) | the same, caught at request time for a hand-built entry that skipped validation |
| `perplexity.stream.done_ignored` | warning | `terminal.omit_done` declared on the typed grammar, which has no sentinel |

`scenario.stream.policy.unknown` **replaces** the per-provider codes this build already raises for the scalar form —
`perplexity.stream.policy.unknown` (`provider/perplexity/handler.go`) and Exa's `exa.stream.policy.unknown`
(`provider/exa/render.go`). Once `StreamScript` lives in `scenario` and owns the enum, one envelope-level code is
the right home; until then the per-provider codes are the shipped behaviour, and a scenario asserting on them keeps
working. Retiring them is part of landing this design, not a separate cleanup.

`scenario.fault.after_chunk.out_of_range` is a load-time check because the chunk count is computable there: the
grammar is fixed by the provider entry and the frame count is `1 + len(deltas) + 1 + terminal frames`. The provider
packages' existing `ValidateProjections` (`provider/perplexity/handler.go:267`) is where it lives, since that is the
layer that knows the grammar.

---

## 10. Contract fidelity

The two grammars in §7 are reconstructed from the vendor's OpenAPI document and from the OpenAI-compatible dialect it
mirrors. The `EventType` enum is verified — it is generated from `https://docs.perplexity.ai/openapi.json` into
`contracts/perplexity/README.md`. **The frame-level shapes are not.** The OpenAPI document declares the event *names*
and the response *schemas*; it does not pin the chunk envelope, whether `usage` rides on the `finish_reason` chunk or
a separate one, or whether Perplexity emits `[DONE]`.

Those must therefore be recorded as `simulator-chosen` in `contracts/perplexity/provenance.yaml`, exactly as the Sonar
non-422 error bodies already are, and corrected from a captured live response before this is called verified. That is
the same discipline ADR-0002 applies, and the adopter's own backlog asks for it under contract-fidelity process. The
one authority already in hand is the adopter's client code: what `src/pkg/agent/perplexity.go` actually parses is
evidence of the real wire shape, and it should be read before the fixtures are frozen.

Golden fixtures to add under `contracts/perplexity/`: `perplexity-sonar-stream.sse`,
`perplexity-sonar-stream-disconnect.sse`, `perplexity-agent-stream.sse`, each with a provenance entry naming the
vendor API version it mirrors.

---

## 11. What this does not do

- **It does not make the simulator generate.** A stream is a scripted sequence chosen by a declarative predicate over
  the request, which is plan non-goal 2 held exactly where the turn model holds it. "Split this scripted answer into
  these deltas" is in scope; "tokenise an arbitrary answer plausibly" is a fake LLM.
- **It does not stream Exa or Tavily.** The mechanism is provider-neutral and both could adopt it in a later release;
  neither has a consumer that parses SSE today.
- **It does not add background/polling mode.** `background: true` remains a warning. The adopter's async-job needs —
  Exa `/agent/runs`, Tavily `/research` — are a separate state machine and a separate design; they share nothing with
  this one but the fault catalogue.
- **It does not serve HTTP/2.** `Hijacker` does not exist there, and the container serves cleartext HTTP/1.1 only.
  The existing `closeBeforeHeaders` fallback (`provider/fault_exec.go:228`) already documents the same limit.
- **It does not survive multiple replicas.** Stream state is per-request and holds nothing across requests, but the
  fault attempt counters a `transient-blip-then-retry` scenario depends on are per-process in-memory, so the retry
  reaching a second replica draws attempt 0 again and is disconnected forever. That is the same undocumented
  multi-replica divergence the adopter already flagged, reached from a new direction, and it makes documenting the
  single-replica exemption a prerequisite for this feature rather than a parallel task.
