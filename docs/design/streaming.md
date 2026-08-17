# SSE streaming

> ## IMPLEMENTED — Phase 5 shipped 2026-08-15/16 (units 1–4), released in v0.3.0
>
> Released in `v0.3.0` (2026-08-16). This document is now the record of the streaming design **as built**. The
> code is authoritative; each unit's banner below and every "Shipped as (Phase 5 unit N)" note in the body record
> where the illustrative sketches and the shipped code parted ways and why. What shipped: the provider-blind SSE
> transport, journal-early append and close, `GrammarDelta` in `stream_mode: full` on the three Sonar routes, the
> three `stream_*` fault kinds and per-chunk pacing, `GrammarTyped` on the three Agent routes, testkit
> `AssertGoldenSSE` / `AwaitStreamClosed` / `AssertStreamPacing` / `AssertStreamUsage`, the `streaming` built-in,
> and the consumer docs. Not shipped, by decision: `stream_mode: concise` (served as full with a warning), the
> `response.reasoning.*` and `response.failed` events, Exa/Tavily streaming. `contracts/perplexity/README.md`
> outranks this document on any wire field ([ADR 0002](../adr/0002-verified-contract-precedence.md)).
>
> ## SHIPPED (unit 3) — 2026-08-15
>
> **Phase 5 unit 3 has landed**: the Agent API's `GrammarTyped` grammar, on `perplexity_agent`. The projection
> (`PerplexityAgent.Stream scenario.StreamScript`), decode and load-time validation are wired exactly as Sonar's —
> `agentStreamPolicy`/`rejectAgentStream` mirror `streamPolicy`/`rejectStream`, and `AgentValidator.ValidateProjections`
> now calls `scenario.ValidateStreamScripts`/`ValidateStreamFaultMismatch` exactly as `SonarValidator` does — so the
> unconditional `perplexity.agent.stream.unsupported` warning this surface always raised is retired in favour of the
> same `warn`/`reject`/`stream` switch, under the renamed code `perplexity.stream.agent_unsupported`
> (`CodeAgentStreamUnsupported`, unchanged Go identifier, changed string value — §9's rename, now live). The Agent
> renderer (`renderAgentStream`, `profiles/perplexity/agent.go`) emits six of the fourteen `EventType` members, for a
> turn scripting N deltas: `response.created` (the `ResponsesResponse` in its initial `in_progress` state — empty
> `output`, zero `usage`), `response.output_item.added` (the message item, `in_progress`, empty `content`), N ×
> `response.output_text.delta`, `response.output_text.done` (the aggregate text), `response.output_item.done` (the
> completed message item), and terminal `response.completed`, whose `response` is the byte-identical
> `ResponsesResponse` the non-streaming route renders for the same turn — built once by the new `agentResponse`
> helper and shared by both transports, never re-implemented for the stream (proved by
> `TestAgentStreamCompletedResponseMatchesNonStreamingBody`). `response.in_progress` is NOT emitted — see the
> resolved ambiguity below. `sequence_number` is monotonic from 0; every frame carries an `event: <type>` line (A4,
> simulator-chosen, unchanged from unit 2's resolution). Pacing is simulator-chosen too, and narrower than §4.3's
> "gates every chunk" rule as written, because `GrammarTyped` has four envelope frames `StreamScript` has no
> per-frame override for: `response.created` and the terminal `response.completed` use the script's ordinary
> chunk-pace resolution (own override else script default) exactly like `GrammarDelta`'s chunk 0 and terminal
> chunk; `response.output_item.added`, `response.output_text.done` and `response.output_item.done` — frames with
> no scenario-authored content of their own — carry no gap at all (`Pace: 0`), since nothing in `StreamScript`
> names one for them. `StreamTerminal.OmitDone` has no effect on `GrammarTyped`
> (it never wrote `[DONE]` to begin with — that remains a `GrammarDelta`-only sentinel) and now raises the load-time
> warning `perplexity.stream.done_ignored` (`CodeStreamDoneIgnored`) when declared on an Agent turn, rather than
> being silently accepted. `terminal.omit_usage` nils `usage` specifically inside `response.completed`'s `response`
> object, via `wire.Omit` on the rendered bytes rather than a pointer field on the shared `ResponsesResponse` type —
> see the resolved ambiguity below. The three `stream_*` fault kinds needed no changes at all — `executeStream`,
> `planStream` and `EncodeSSE` are exactly as grammar-blind as §7 always said — proved by one disconnect test on
> `/v1/agent` (`TestAgentStreamDisconnect`). `journal.StreamOutcome.EventNames` (§5.1) is live for both grammars now:
> non-nil and populated for `GrammarTyped`, `nil` for `GrammarDelta` (`streamPlan.eventNames`, `provider/stream.go`),
> exactly as §5.1's unit-1/2 notes anticipated. Golden: `contracts/perplexity/perplexity-agent-stream.sse`, rendered
> through the real handler on `/v1/agent`, `/v1/responses` and `/responses` (byte-identical, `TestAgentStreamGolden`).
> `contracts/perplexity/README.md`'s "What Servicesim simulates" section is updated to describe Agent streaming
> alongside Sonar's. Out of unit 3: the `response.reasoning.*` event family and `response.failed` — no scenario
> vocabulary exists for either, both noted as later-unit additions in the contract; `testkit.AssertGoldenSSE` and the
> other `testkit` streaming surface, built-in scenarios, and Exa/Tavily streaming remain unit 4 / never.
>
> **Two points this unit had to resolve that the design left unpinned, both because the design's own text was
> written before a real `GrammarTyped` renderer existed to test it against:**
>
> - **`scenario.chunkCount`'s default formula (`len(Deltas) + 1`) undercounts `GrammarTyped`.** That formula is
>   `GrammarDelta`-shaped — one chunk per delta plus the one terminal chunk — and `ValidateStreamFaultMismatch` uses
>   it to bound `after_chunk`. `GrammarTyped`'s real count is `len(Deltas) + 5` (the five envelope events above) for
>   a turn whose message item renders at all, or 2 for a failed/cancelled turn (§7's shipped-as note on the sketch,
>   above), so using the unmodified formula would have rejected a perfectly valid `after_chunk` anywhere in
>   `[N+1, N+4]` as "out of range" against a false, too-small bound — the opposite failure from the one
>   `CodeStreamAfterChunkOutOfRange` exists to catch. Resolved additively: `scenario.StreamTurn` gains a
>   `ChunkCount int` field (zero means "use the default formula", so no existing caller — Sonar's — has to set it),
>   and `chunkCount` prefers it when set. `AgentValidator` computes it per turn via the new `agentChunkCount`
>   helper, which takes the turn's decoded `Status` as well as its `Script` for exactly this reason — a first
>   pass that computed it from `Script` alone (ignoring `Status`) shipped `len(Deltas)+5` unconditionally, which
>   overstated the bound for a failed/cancelled turn and let an unreachable `after_chunk` load clean; a review
>   pass caught it before release and `TestAgentStreamAfterChunkOutOfRangeAtLoadFailedStatus` is the regression.
>   `TestAgentStreamAfterChunkOutOfRangeAtLoad` covers the ordinary (non-failed) case: it targets chunk 8, which the
>   WRONG default formula would have accepted (only 4 chunks) and the real one correctly rejects (valid range is
>   `0..7`).
> - **`ResponsesResponse.Usage` is a plain, always-present field — `terminal.omit_usage` cannot express "absent" on
>   it directly.** Unlike Sonar's `ChatCompletionChunkResponse.Usage *Usage`, a streaming-only pointer field
>   dedicated to this one edge case, the Agent surface's `ResponsesResponse` is the SAME type the non-streaming route
>   renders — the whole point of `agentResponse` being shared. Retyping `Usage` to a pointer to accommodate one
>   streaming-only omission would ripple into every non-streaming Agent response's shape. Resolved by keeping
>   `ResponsesResponse` unchanged and applying `wire.Omit(bytes, []string{"usage"})` to the already-rendered
>   `response.completed` payload instead — the same tool `PerplexityResult.OmitFields` already uses for a structurally
>   identical problem on the Sonar side, reused rather than a new mechanism invented for this one. One consequence
>   worth naming rather than leaving implicit: `wire.Omit` always round-trips the object through a map, so a turn
>   that sets `terminal.omit_usage` gets a `response.completed` whose `response` object is key-alphabetised at every
>   nesting level, unlike every other frame in the same stream, which stays in struct order — deterministic either
>   way, and the same divergence `renderSonarStream` already documents for an extra-fields terminal frame, not a
>   new kind of non-determinism.
>
> Neither is a conceptual disagreement with anything §7 or §9 says; both are load-bearing details the prose left to
> "whoever builds this next" because nothing before this unit needed a `GrammarTyped` chunk count or a `GrammarTyped`
> usage omission to exist as real code.
>
> ## SHIPPED (unit 2) — 2026-08-15
>
> **Phase 5 unit 2 has landed**: the three `stream_*` fault kinds (`stream_disconnect`, `stream_truncate_chunk`,
> `stream_stall`) and `FaultAttempt.AfterChunk`; per-delta/per-script/per-terminal `pace:` (`scenario.StreamDelta`
> replaces the unit-1 `[]string` Deltas, accepting the same scalar-or-mapping shorthand pattern); `provider.streamPlan`/
> `planStream` (the pure abort/stall/truncate resolver `executeStream`'s loop now consults) and `provider.Stream`'s new
> `DonePace` field; the load-time checks `scenario.fault.after_chunk.not_streaming`, `scenario.fault.stream_mismatch`'s
> second direction (a `stream_*` kind on a non-streaming entry — the first direction, `truncate_body` on a streaming
> entry, was unit 1), and `scenario.fault.after_chunk.out_of_range`; the request-time mirror
> `scenario.stream.abort_unreachable`'s second direction (a `stream_*` kind claimed by a request that will not
> stream — the first direction, `truncate_body` claimed by one that will, was unit 1); and
> `journal.StreamOutcome`'s four remaining fields, `PaceMS`/`AbortAfterChunk`/`TruncatedAtByte`/`StallBeforeMS`,
> exactly as unit 1 deferred them. Goldens: `contracts/perplexity/perplexity-sonar-stream-disconnect.sse` and
> `perplexity-sonar-stream-truncate.sse`. Every "Shipped as (Phase 5 unit 2):" note inline below records where the
> shipped shape narrows, extends or corrects an illustrative block or resolves a genuine prose ambiguity this unit
> was the first to need an answer for. Out of unit 2, still: `GrammarTyped`/the Agent API surface (unit 3),
> `testkit.AssertGoldenSSE`/`AwaitStreamClosed`/`AssertStreamPacing`, built-in scenarios, and Exa/Tavily streaming
> (unit 4 / never).
>
> **One genuine ambiguity this unit had to settle, between two passages of this same document that read
> oppositely once a real implementation had to pick one.** `FaultStreamDisconnect`'s own field comment ("writes
> chunks `[0, AfterChunk)` in full ... chunk `AfterChunk` never reaches the client") and §9's own worked example
> ("aborting on the final indexed chunk ... every scripted delta arrived, but the response that confirms
> completion ... never does") both, read together, pin one answer: **`stream_disconnect` aborts BEFORE writing the
> chunk at `AfterChunk`** — chunks at every earlier index are written whole, and the client's last observation is
> the previous chunk (or the flushed headers, if `AfterChunk == 0`), never a partial one. `ChunksSent` after such an
> abort therefore equals `AfterChunk`, not `AfterChunk + 1`. This is the shipped behaviour
> (`provider.streamPlan.disconnectAt`, `provider/stream_test.go`'s `TestHandleStreamDisconnect`). The P5U2 task
> brief handed to the implementing unit described the client-observed frame count as "`after_chunk+1`", which
> reads as the opposite convention; that brief is not part of this design document and is not authoritative over
> it, and the two PROSE passages above agree with each other independently of it, so this document's own words
> are what the code follows. Those two prose passages are not corrected by this resolution — both already said
> the same thing. §4.3's own illustrative Go sketch (`if plan.AbortAt == i` checked AFTER the write) and §9's
> earlier reference to it DID read the other way, matching the brief rather than the two passages above; that is
> the thing this resolution corrects, via §4.3's own "Shipped as (Phase 5 unit 2)" note just below the sketch. A
> corollary worth stating once, here rather than only in code comments: because the abort precedes chunk
> `AfterChunk`'s own pace sleep, `PaceMS[AfterChunk]` is a planned gap that is never actually slept — the client
> sees the disconnect immediately after the previous chunk, with no observable delay contributed by the
> chunk that never arrives.
>
> ## SHIPPED (unit 1) — 2026-08-15
>
> **Phase 5 unit 1 has landed**: `scenario.StreamServe`/`StreamScript`, `provider.SSEEvent`/`EncodeSSE`/
> `Stream`/`Response.Stream`/`executeStream`, `Handle`'s widened journal-early condition and mismatch
> branch, `journal.StreamOutcome`/`StreamCloser`/`Ring.CloseStream`, and the Sonar `GrammarDelta` full-mode
> renderer, on the three Sonar route spellings — golden:
> `contracts/perplexity/perplexity-sonar-stream.sse`. Every "Shipped as (Phase 5 unit 1):" note inline below
> records where the shipped shape narrows or corrects an illustrative block; the round-3 PASS banner this
> replaces is preserved in full underneath, since its conceptual decisions are what unit 1 was reviewed
> against and remain the authority for units 2–4. Out of unit 1: `stream_stall`/`stream_disconnect`/
> `stream_truncate_chunk` and `after_chunk` (unit 2), `GrammarTyped`/the Agent API surface (unit 3),
> `testkit.AssertGoldenSSE`/`AwaitStreamClosed`/`AssertStreamPacing`, built-in scenarios, and Exa/Tavily
> streaming (unit 4 / never). Exa is untouched — see §9's shipped-as note on the retirement paragraph.
> Also out of unit 1, not scoped to any later unit by name yet: §6.2's own read-boundary-non-determinism
> and goldens-are-taken-over-parsed-frames rule is still not recorded in
> `contracts/perplexity/README.md`'s "Streaming (SSE)" section, which §6.2 itself already flags as a Phase 5
> obligation this design creates. Land it whenever that section is next touched, so it is not lost.
>
> ## REVISED (round 3) — re-reviewed 2026-08-15: PASS
>
> **Phase 5 may start with unit 1:** `Response.Stream` + the `execute` branch + the widened `Handle` journal
> condition + the SSE writer. **OPEN — owner decision: none.**
>
> Contract step (§10 step 1) done 2026-08-15; the five design deltas that step forced were resolved the same day
> — see §7 ("Resolved 2026-08-15").
>
> Amendment 2026-08-15: the five open deltas are resolved (see §7); unit 1 = GrammarDelta, full mode, on the three
> Sonar routes.
>
> Amendment re-review 2026-08-15: two majors, several minors and nits were found against the amendment above; all
> are answered in place, in the sections named, not restated here. The two majors: §7's GrammarDelta paragraph
> had the vendor's `id`/`created` example backwards (it is `created` the example shows moving, not `id`) and
> mislabelled `finish_reason: null` as vendor-pinned where the contract states it unstated for `full` mode; and
> whether `[DONE]` is an element of `Stream.Chunks` was answered two ways by different sections — settled in §3.2
> (it is not; `chunk_count == len(Stream.Chunks)`, always). Minors: §4.3's pace bullet still described a
> role-only opening chunk A3 already removed; the contract's "What Servicesim simulates" section now states the
> concise-mode limitation itself rather than being cited for a sentence it did not contain; §2.1 example 4b's
> `after_chunk` was out of range against §2's three-delta scenario; the concise warning's condition now says
> explicitly that it requires the request to actually stream, not merely to carry `stream_mode: concise`; §4.1
> now names the new per-request warning on the path it fires from; §7's frame example elides `search_results`
> instead of rendering it in the scenario's projection shape; and where `extra_fields`/`images`/`related_questions`
> land in the stream (terminal-only) is now stated. A handful of nits — a dangling pronoun, a `chunk_count`
> cross-reference pointing the wrong way, two stale "open deltas" pointers left over from resolving §7 — are
> fixed in the same pass.
>
> A third, independent reviewer re-read this document end to end against both B1 lenses and the round-3 change list
> below, and re-verified every round-3 blocker/major disposition against the current tree. Verdict: **PASS**. Every
> round-3 major is answered, not restated, everywhere the document touches the topic; no conceptual arbitration was
> left undecided. Six minor/nit items survived the re-review, none of them a decision ambiguity — each was a place
> where the prose's decision was correct but not yet applied consistently, or a factual count that had drifted from
> the tree. This pass fixes all six in place, in the sections named:
>
> - §3.1's `StreamScript` doc comment now says one terminal chunk (it listed `finish_reason` and `usage` as separate
>   frames while §2, §7, §9 and §10 all pinned one; the Go block was always illustrative and prose already won, but
>   the drift is now gone).
> - §7 now says each Perplexity surface has **three** route spellings, not two — `SonarRoutes()` and `AgentRoutes()`
>   each list three, verified in `profiles/perplexity/handler.go`.
> - §5.1 now states explicitly that `StreamOutcome.PaceMS[AfterChunk]` includes the stall and `StallBeforeMS` is the
>   same duration lifted back out, so `AssertStreamPacing` on a stall scenario needs no separate addition.
> - §4.2's mirror-case bullet now spells out the `truncate_body`-claimed-while-streaming direction of
>   `scenario.stream.abort_unreachable` with the same mechanism sentence the `stream_*`-on-non-streaming case already
>   had, rather than leaving it to be inferred from the finding table alone.
> - §4.3 now says `faultHeader` **keeps** `Content-Type: text/event-stream` on the stream path (it only overrides
>   `Content-Type` for `content_type`, `wrong_content_type` or `invalid_json`) — the prior wording, "still resets",
>   said the opposite of what the code does.
> - §7 now states explicitly that landing `GrammarTyped` is what gives the Agent entry a `stream:` key at all: the
>   Agent surface has no `Stream` field today and warns `CodeAgentStreamUnsupported` unconditionally
>   (`profiles/perplexity/agent.go`), which is what the preamble's "no policy knob" claim was resting on without
>   saying so.
>
> **OPEN — owner decision:** none. The re-review found no conceptual arbitration left to the owner; every finding it
> raised had a decision this document could make and justify from the shipped code or a rule already stated
> elsewhere in it.
>
> <details><summary>Round-3 change list (cycle 1, the pass that produced the PASS verdict above)</summary>
>
> Round 2's banner claimed a round-3 re-review had already run and answered every conceptual finding from round 2.
> No round-3 change list, and no record of what a re-reviewer found, existed anywhere in this document — that claim
> was itself unverifiable, which is the exact defect the backlog's re-review process exists to catch. This revision
> is what made "round 3" real: two independent adversarial reviews were run against the document as round 2 left
> it, and every finding they returned is answered below, in place, rather than summarised here.
>
> **Answered, not restated, this round:**
>
> - **A `stream_*` fault can be claimed by a request that does not stream, even under a `stream`-policy entry.**
>   Round 2's per-turn/per-route fix stopped a `stream_*` fault from landing on a non-streaming *turn*; it did not
>   stop one from landing on a non-streaming *request* to a streaming turn, because `when_requested` answers "does
>   the surface serve a stream when asked", and this design's own preamble endorses a client asking on call 1 and
>   not asking on call 3 in the same lane. §4.2 and §9 now say what happens: the claimed attempt is reported, never
>   silently absorbed into an ordinary 200. See [§4.2](#42-handle--one-condition-widens) and
>   [§9](#9-validation-findings-this-adds).
> - **`stream_stall`'s `Delay` does not also run as a time-to-first-byte sleep.** §3.1 and §4.3 now agree: the
>   generic pre-dispatch delay is skipped for `stream_stall`, whose `Delay`/`AfterChunk` pair is resolved entirely
>   inside the stream plan. See [§3.1](#31-scenario--the-projection-grammar) and
>   [§4.3](#43-execute--one-branch-before-the-existing-switch).
> - **The finding-code retirement table contradicted the paragraph above it.** `perplexity.stream.policy.unknown`
>   and `perplexity.stream.policy.ignored` are retired in favour of the envelope-level codes, consistently now. See
>   [§9](#9-validation-findings-this-adds).
> - **The "one entry, two grammars" premise was checked against the current route table and does not hold.**
>   `Route.Entry`, a Phase 1/3 change, already gives Sonar (`NameSonar`) and the Agent API (`NameAgent`) independent
>   scenario entries, independent validators and independent turn cursors — confirmed in
>   `profiles/perplexity/handler.go` and `profiles/perplexity/doc.go`. Grammar is fixed per entry with no case left
>   to reconcile; §9's "the grammar is fixed by the provider entry" now says why. See
>   [§9](#9-validation-findings-this-adds).
> - **The exported projection field this design needs to change type on is now named as a breaking change**, not
>   silently implied by "additions only". `PerplexityProjection.Stream` and Exa's projection `Stream` field both
>   change type from `scenario.StreamPolicy` to `scenario.StreamScript`; §3 now carries the one exception to "no
>   existing field changes type" instead of contradicting it. See [§3](#3-go-types).
> - **`AssertGoldenSSE`**, named in the backlog's Phase 5 list but absent from this document, now has a §5.4 that
>   decides what it diffs, what it ignores by default, and its file extension.
> - **§10 now states the contract-fidelity work as an explicit Phase 5 prerequisite** with its three steps in order,
>   and pins the one frame-level choice every other section depended on without saying so: `finish_reason` and
>   `usage` ride on the same terminal chunk on `GrammarDelta`, recorded as simulator-chosen.
> - **Every file:line reference in the document was re-verified against the current tree and replaced with a
>   symbol name.** Several had already drifted within the round-2 pass that "corrected" them; symbols do not drift.
> - A dozen further minors and nits — the `stream_truncate`/`stream_truncate_chunk` spelling, the
>   `after_chunk.out_of_range` bound (`>=`, not "exceeds"), `Outcome.BytesWritten`'s truncate_body exception,
>   `stream.abort_unreachable`'s naming and its now-shared home with the finding above, attempt headers on the
>   stream path, `truncate_after_bytes`'s per-chunk meaning, pacing's reach into the role/terminal/`[DONE]` frames,
>   `AwaitStreamClosed`'s namespace handling, and a handful of factual drifts — are each answered at their own
>   section rather than listed twice.
>
> **OPEN — owner decision:** none. Every conceptual arbitration the two reviews raised had a decision this document
> could make and justify from the shipped code or from a rule already stated elsewhere in it; none needed to be
> deferred.
>
> </details>
>
> ### The Go blocks below are ILLUSTRATIVE, not normative
>
> **Normative:** the decisions, their reasoning, the finding codes and severities, the ordering constraints, and the
> invariants — in particular that suppression is decided once before the append, that policy is per entry while
> content is per turn, and that a fault attempt scripted for a transport the exchange does not use is reported, not
> silently absorbed.
>
> **Illustrative:** every `go` block, including `wantsStream` and every other helper named only inside one. Signatures,
> arities and registration details in them are sketches, and have been wrong repeatedly in exactly those dimensions.
> Prose cannot be type-checked; three adversarial review rounds have now produced a flat rate of mechanical defects
> in these blocks while the conceptual layer converged. Read them for shape and intent. Where a block and the prose
> disagree, **the prose wins**; where the code, once written, disagrees with a block, the code wins and the block
> should be deleted rather than patched.
>
> **Still a design, not an instruction to start.** Implementation is Phase 5, gated behind Phase 3, which has
> shipped (see `docs/design/async-jobs.md`).
>
> <details><summary>Review history (rounds 1–2)</summary>
>
> > ## REVISED (round 2) — pending re-review
> >
> > Round 1 was re-reviewed and failed: one blocker, two majors. **Round 2 answers all of them.** Summary:
> >
> > - **Suppression (blocker).** Round 1 added §4.4 saying `execute` does not re-derive suppression, and left §4.3
> >   doing exactly that. §4.2 now decides it before `faultOutcome` and before the journal condition, so
> >   `resp.Stream != nil` means "this exchange **will** stream" everywhere downstream; §4.3 is a single branch with
> >   no `suppressesStream` call and a note explaining why adding one back would reintroduce the defect. The two
> >   declarations that must be hoisted above the existing defer (`provider/handle.go`'s deferred recover/record)
> >   are now stated.
> > - **Per-turn policy vs per-route plan (major).** Resolved by making the **policy entry-level and the content per
> >   turn**, which is what shipped code already does and, more importantly, all it *can* do: rejection must happen
> >   before turn selection claims an attempt (`rejectStream` runs before `SelectTurnFor` in `handleSonar`), so a
> >   per-turn policy could never be honoured. This dissolves the mismatch rather than validating against it — either
> >   the entry streams and every turn does, or none does. `after_chunk` is bounded by the smallest chunk count
> >   across the entry's turns, since the plan is per route.
> > - **Preamble vs §4.1 (major).** Both now say the same thing, and it matches shipped behaviour.
> > - Minors: `warnOnce` replaced with a plain `Warn` (no such helper exists; the `closed` guard already bounds it);
> >   `decodeRefOrMapping` corrected to `(*SourceRef).UnmarshalYAML`; `DecodeStrict` line reference corrected.
> >
> > **Still a design, not an instruction to start.** Round 2 was re-reviewed and its conceptual findings are
> > answered in round 3. Implementation is Phase 5, which is gated behind Phase 3 in any case.
> >
> > <details><summary>Round-1 findings, all now addressed above</summary>
> >
> > - **Blocker — §4.3 still performs the operation §4.4 forbids.** §4.4 says suppression is decided before the
> >   append and that "`execute` does not re-derive it"; §4.3's code block, unrevised, still contains
> >   `resp = suppressStream(resp)` inside `execute`. §4.2's early-journal condition and its deferred close both read
> >   the *outer* `resp`, which that local reassignment never touches — so a suppressed stream still journals a fully
> >   planned `Outcome.Stream` and still stamps `client_gone`. The finding verbatim.
> > - **Major — "effective policy" is per turn; the plan it is checked against is per route.** `TurnFault` returns
> >   the first turn declaring `attempts`, so a `stream_disconnect` declared on a streaming turn 0 can land on a
> >   non-streaming turn 3, leave `resp.Stream == nil`, and fall through to `writeResponse` silently. Load-time
> >   validation passes both. `after_chunk.out_of_range` is likewise undefined about *which* turn's chunk count.
> >   This is the async blocker's shape, reintroduced by the streaming fix.
> > - **Major — the preamble and §4.1 disagree about which policies are per turn.** The preamble says `warn` and
> >   `reject` stay provider-level; §4.1 switches on the *selected* turn's projection. Shipped code is turn-0-only
> >   for both policies that ship today (`streamPolicy` in `profiles/exa/handler.go` and
> >   `profiles/perplexity/handler.go`).
> > - Minors: `warnOnce` does not exist; §3.1 cites a non-existent `decodeRefOrMapping`; §2's `DecodeStrict` line
> >   reference is wrong; §4.2's `defer` references variables declared after the existing defer and the required
> >   hoist is unstated.
> >
> > **Answered in round 1 and still sound:** the compatibility blocker. `stream: warn` + `truncate_body` stays
> > loadable, §9's table keys on effective policy in both directions, and the regression fixture is the right guard.
> >
> > ---
> >
> > And the round-1 revision notes those findings were written against. The original adversarial review returned
> > **needs-revision** on 2026-08-15 with one blocker and one major; round 1 claimed both were answered:
> >
> > - ~~**Blocker:** §9 raises `scenario.fault.stream_mismatch` on the *presence* of a `stream:` key, which would
> >   fire against Exa's already-shipped projection.~~ **Answered.** Both directions now key on the **effective
> >   policy**: `warn` and `reject` declare a policy and produce no stream, so `truncate_body` stays valid with
> >   them. §9 carries the shipped-fixture case that would have broken, and requires it as a regression fixture.
> >   §4.4 was restating the presence rule and is corrected too.
> > - ~~Stream suppression is decided inside `execute` against its own copy of the response.~~ **Answered.**
> >   Suppression is now decided once, where `Handle` builds the entry and before the append; a suppressed stream
> >   journals `Outcome.Stream = nil` rather than a fully-specified stream that never happens. See §4.4.
> >
> > Also revised in the same pass: §8's reason 1 argued from a strict-equality version gate that **Phase 1 has
> > since widened to a range**, so that reason is now weaker and says so; and §9 records that
> > `perplexity.agent.stream.unsupported` is misnamed against every other `perplexity.stream.*` code.
> >
> > That re-review has now run, and the verdict is at the top of this banner.
> >
> > </details>
>
> </details>

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

Streaming currently produces a journal warning and an ordinary JSON body: `CodeStreamUnimplemented` —
`perplexity.stream.unimplemented` — raised in Sonar's `validateSonarRequest`; `CodeAgentStreamUnsupported` —
`perplexity.agent.stream.unsupported` — raised in the Agent surface's field validation; and Exa's unexported
`codeStreamUnimplemented` — `exa.stream.unimplemented` — raised in `validateStream`.

A `stream:` scalar in a `respond:` body already chooses between that default (`warn`) and a provider-shaped rejection
(`reject`) on Exa's `/search` and `/answer` routes and on Sonar. On both it is read from the provider block's **first
turn only** (`streamPolicy` in `profiles/exa/handler.go` and `profiles/perplexity/handler.go`), because rejection has
to happen before turn selection claims a fault attempt; Sonar warns `perplexity.stream.policy.ignored` at startup for
a `stream:` written on any later turn. The Agent surface has no policy knob and always warns — Exa's `/agent/runs`
routes are a separate surface again and are out of scope for this design either way.

So those codes survive as the `warn` default, and what this design adds is the third value — `stream`, which serves
the scripted sequence.

**The policy is per ENTRY; the content is per TURN.** That split is forced, not a simplification:

- **Policy — `warn`, `reject`, `stream` — is read once from the entry**, exactly as shipped code already does in
  `streamPolicy` on both surfaces, always from turn 0. The reason is in the shipped godoc and is a hard ordering
  constraint: the policy decides whether a request is *rejected*, and rejection must happen before turn selection
  claims an attempt, or a refused request eats a retry budget. `rejectStream` runs before `validateSonarRequest`,
  and both run before `SelectTurnFor`. **A policy that varied per turn could never be honoured** — you cannot reject
  a request on the strength of a turn you have not selected without the selection itself consuming the attempt the
  rejection must not consume.
- **`deltas:` are per turn**, necessarily: they are the answer, and each turn has its own.

An earlier draft had `stream` per turn and the other two per entry, which is unimplementable for the reason above
and left the preamble, §4.1 and shipped code each saying something different.

Per-turn policy is also **unnecessary**, which is the part worth internalising before anyone proposes it again. The
key is named `when_requested`: it answers "when the client asks for a stream, what do we do". That is a property of
the surface, not of call position. A consumer who wants call 1 streamed and call 3 not **sends `stream: true` and
then `stream: false`** — the variation already lives in the client's request, which is the thing under test. The
scenario never needed to encode it.

That freedom has a consequence a fault plan has to answer, not just the projection: a `stream_*` fault attempt is
still claimed by whichever call draws it, streamed or not, because the attempt is claimed at turn selection, before
the handler has looked at `stream` on the wire. Call 3 above can draw the entry's `stream_disconnect` attempt on a
request that never asked to stream. §4.2 and §9 say what that does — it is reported, not silently served as a plain
200 — rather than being ruled out here, because it cannot be ruled out here: the entry-level policy makes the
mismatch *rare*, not *impossible*.

See [§2](#2-scenario-yaml), [§4.2](#42-handle--one-condition-widens), [§8](#8-schema-versioning) and
[§9](#9-validation-findings-this-adds).

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

What *is* ready: the abort machinery. `closeBeforeHeaders`, `truncateBody` and `resetAndClose` in
`provider/fault_exec.go` already do everything a mid-stream disconnect needs, and `Handle`'s deferred
`recover`/`record`/re-`panic` already survives it. None of that changes.

What is **not** ready, and is not mentioned in the note:

- **`Response` cannot express a stream.** `Response.Body` is `[]byte` and `execute`'s no-attempt path writes it in a
  single `writeResponse` call. There is no yield point between bytes, so there is nowhere for pacing, for a
  mid-stream stall, or for a `ctx.Done()` check to live.
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
deltas and the pacing; it cannot hand-assemble frames, because the full-mode frame sequence — each chunk's
`role`+content delta and running aggregate `message`, `search_results` repeated on every chunk, the one terminal
chunk carrying `finish_reason`, the full `message`, `usage` and `search_results` together, and the `[DONE]`
sentinel — is contract-fixed (§7, §10) and getting it wrong is what the simulator exists to prevent. Full mode is
the only mode unit 1 renders; `stream_mode: concise` is a later unit (§7).

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
  `{when_requested: warn}`. This is the `SourceRef` scalar-or-mapping pattern `scenario` already uses, decoded
  through `DecodeStrict`. It applies to a `respond:`-wrapped `stream:` and to the **single-shot** shape too — a
  provider block with no `turns:` and no `respond:` normalises to one turn whose projection body is the block
  itself minus its reserved envelope keys (`scenario/model.go`'s single-shot normalisation), which is the shape
  `providers.exa.stream: reject` uses today. Neither shape is exercised by a shipped `scenarios/` fixture as of this
  writing — the only `stream: warn`/`stream: reject` fixtures in the tree are inline YAML strings inside
  `profiles/exa/request_test.go` and `profiles/perplexity/handler_test.go` — so the compatibility claim rests on the
  decoder accepting the key today, not on an existing fixture exercising it. §9's required regression fixture is
  what turns that claim into a checked one.
- **Declaring `deltas:` implies `when_requested: stream` — on turn 0, which is the turn the policy is read from.**
  Writing the script and forgetting the switch would otherwise serve a JSON body and a warning, silently.

  On any **later** turn the implication cannot fire, because that turn's policy is never read
  ([the preamble](#sse-streaming) explains why the policy must be per entry). Deltas there would otherwise be
  silently dead — the exact failure this implication rule exists to prevent, one turn along — so
  `scenario.stream.deltas_ignored` is a load **error**: a turn declares `deltas:` while the entry's effective policy
  is not `stream`. It is the mirror of `deltas_empty`, and between them every combination of "entry streams" and
  "turn has deltas" is either valid or reported:

  | entry policy | turn has `deltas` | outcome |
  |---|---|---|
  | `stream` | yes | serves the stream |
  | `stream` | no | `scenario.stream.deltas_empty` (error) |
  | not `stream` | yes | `scenario.stream.deltas_ignored` (error) |
  | not `stream` | no | serves JSON, unchanged |

- **A turn with no `stream:` block keeps today's behaviour under an entry that does not stream** — `warn` — so no
  existing scenario changes meaning. Under an entry whose policy *is* `stream`, such a turn is a `deltas_empty`
  error rather than a silent JSON response, per the table above.
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

# 4b. A single stall that exceeds the activity timeout, mid-stream (before the terminal chunk),
#     without aborting.
fault:
  attempts:
    - kind: stream_stall
      after_chunk: 3
      delay: 65s
```

Two traps worth naming, because both produce a green test that proved nothing:

- **The retry must land in the same lane.** Attempt 1 is served only if the retried request resolves to the same
  cursor key. With `turn_key: ["route", "body_json:model"]`, a retry that changes `model` is a *different lane* and
  draws attempt 0 again — it will be disconnected a second time, forever. `Lane.CursorKey` is the authority on what
  "same lane" means.
- **`stream_stall` under `DelaySkip` does not stall.** Stream pacing honours `Deps.DelayMode` exactly as fault delays
  do, so `testkit.WithSkippedDelays` makes a 65 s stall free — and useless for a timeout assertion. A test that
  asserts a Temporal timeout must run under the default `DelayReal`. `Outcome.Stream.PaceMS` still records the
  planned schedule under either mode, which is what a test asserting "the scenario asked for 12 s gaps" compares
  against. The 65 s in the example above is `stream_stall`'s `Delay`, not a time-to-first-byte sleep — see
  [§3.1](#31-scenario--the-projection-grammar).

---

## 3. Go types

The blocks below are illustrative, per the banner, not real signatures — the banner's rule governs here as
everywhere else in this document, and this section does not get an exception to it.

**Additions only, with one exception.** `scenario.StreamPolicy` and its two constants, `StreamWarn` and
`StreamReject`, are not new: they ship today (`scenario/model.go`), and both provider packages' exported projection
structs already carry a field of that type — `PerplexityProjection.Stream` and Exa's own projection's `Stream`
field, both `scenario.StreamPolicy`, both tagged `yaml:"stream,omitempty"`. This design adds a third policy value,
`StreamServe`, to that existing enum, and adds the new `StreamScript` struct below it. Because `stream:` in a
`respond:` body must decode into a single field — a mapping form that also carries `deltas:` and `pace:` cannot
share a YAML key with a plain string field — landing `StreamScript` means retyping those same two exported fields
from `scenario.StreamPolicy` to `scenario.StreamScript`, whose `UnmarshalYAML` accepts the old scalar shorthand and
decodes it exactly as `StreamPolicy` did. That is a breaking change to an exported field's Go type on both provider
packages' exported projection structs, and it is named as one rather than hidden inside "additions only": it is
worth taking here, in this pass, for the same reason `perplexity.agent.stream.unsupported`'s rename is (§9) — every
streaming type in both packages is already being revisited, and a second pass later to fix this one field would be
a second breaking change instead of the first one landing complete. No other exported field's type or meaning
changes.

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
// slowly. It is deliberately not a list of frames. Frame assembly — each chunk's
// role+content delta and running aggregate message, search_results repeated on
// every chunk, the one terminal chunk carrying finish_reason, the full message,
// usage and search_results together, the [DONE] sentinel — is the vendor's
// contract and belongs to the provider package, which is the same split the
// non-streaming path already makes between a projection and a renderer. This is
// the full-mode (GrammarDelta) sequence, the only one unit 1 renders; concise
// mode is a later unit (§7).
//
// It lives in scenario rather than in a provider package because all three
// providers stream the same way and a second copy of this grammar is a second
// chance for two providers to spell pacing differently.
type StreamScript struct {
	// Policy is the YAML key "when_requested". Empty means StreamServe when
	// Deltas is non-empty and StreamWarn otherwise: writing a script and
	// forgetting the switch must not silently serve JSON.
	//
	// Only the FIRST turn's Policy is read — it is a property of the provider
	// entry, not of the call position, because rejection has to be decided
	// before turn selection claims an attempt. A Policy on any later turn raises
	// scenario.stream.policy.ignored rather than being dropped in silence. Deltas
	// below are the opposite: genuinely per turn, since they are the answer.
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

// UnmarshalYAML accepts the scalar shorthand, so `stream: warn` — the form the
// scalar-or-mapping pattern already supports — keeps parsing as
// {when_requested: warn}. The mapping branch decodes strictly, following the
// same pattern (*SourceRef).UnmarshalYAML uses in scenario/model.go.
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

**Shipped as (Phase 5 unit 1):** `Deltas` is `[]string`, not `[]StreamDelta`, and neither `StreamScript` nor
`StreamTerminal` carries a `Pace` field — the P5U1 unit scope narrowed §1's scenario grammar to exactly
`when_requested`, `deltas:` (plain strings) and `terminal: {omit_usage, omit_done}`, deferring per-chunk
pacing (a `pace:` key at either level, and the `StreamDelta` wrapper type it would need) to the unit that
lands `stream_stall` and the other `stream_*` fault kinds, which are the only things that ever produce a
nonzero gap. The transport-level pace SEAM described in §3.2 and §4.3 below is still real and wired
end-to-end (`SSEEvent.Pace` → `StreamChunk.Pace` → `sleep()` in `executeStream`) — nothing in the scenario
grammar can drive it to a nonzero value yet, which is the "zero delay" the P5U1 spec asked unit 1 to stop
at. `StreamScript.UnmarshalYAML` also does not itself reject an unrecognised `when_requested` value (see
§9's revised mechanism note) — it stores it and lets `scenario.ValidateStreamScripts` raise
`scenario.stream.policy.unknown` at the specific path, because a decode-time rejection can only produce a
generic `perplexity.projection.invalid`-class finding addressed at the whole projection body, not at
`.stream.when_requested`.

**Shipped as (Phase 5 unit 2):** `Deltas` is now exactly `[]StreamDelta` as illustrated above, and both
`StreamScript` and `StreamTerminal` carry the `Pace` field the unit-1 note deferred — landed together with
`stream_stall`, as that note anticipated. `StreamDelta.UnmarshalYAML` follows the same scalar-or-mapping
pattern as `StreamScript` itself: a bare string decodes as `{text: <string>}`, so a script that never overrides
pacing keeps writing a plain list of strings. One deliberate, documented narrowing versus the field comment
above: `StreamDelta.Pace` and `StreamTerminal.Pace` treat a zero value as "no override, use the script's
default" rather than "an explicit zero-length gap" — the same "zero means absent" convention this package
already uses for `TruncateAfterBytes` and several `FaultAttempt` fields — so a script cannot currently ask one
chunk for a literal zero gap while its script default is nonzero; nothing shipped needs that distinction.

One exported type this section's sketch does not name: `scenario.StreamTurn` (`Path`, `Script *StreamScript`,
`Answer string`), the neutral carrier `ValidateStreamScripts` takes a `[]StreamTurn` of — a calling provider
package gathers one per turn, since only it knows what its own projection's answer field is called or how its
`Stream` field is reached (`scenario` cannot know that without importing the provider package and closing an
import cycle). It exists because `ValidateStreamScripts` is exported specifically so every provider that
streams shares one implementation rather than duplicating the coherence check per package (the same reasoning
this section already gives for the check itself) — today Perplexity's `SonarValidator` is its only caller,
since Exa is untouched this unit, but the type has to be exported the moment the function that takes it is.

Similarly, `provider.StreamHeader()` (§3.2 below) is exported where earlier prose in this document and §4.3's
sketch call it the unexported `streamHeader()`. It lives in `provider`, not `profiles/perplexity` — a
Perplexity-only home would not help Exa or Tavily when their turn comes (unit 4) — and a provider package's own
handler is what decides to stream and sets `Response.Header`, so the constructor has to be reachable from
outside `provider` itself.

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

**`Delay` is never a time-to-first-byte sleep for `FaultStreamStall`, and the generic pre-dispatch delay in `execute`
does not run for it either.** The comment above states the field's meaning; this states the mechanism, because §4.3
otherwise reads as though the existing "delay runs first for every kind" block is untouched, and untouched means
`stream_stall` would sleep `Delay` twice — once before the status line, once again before chunk `AfterChunk` — which
is not what any scenario author asking for one mid-stream pause wants. The rule: the pre-dispatch delay block runs
for every kind except `FaultStreamStall`, exactly as it already skips work when `Delay` is zero; `planStream`
resolves `FaultStreamStall`'s `Delay` entirely as the extra gap before chunk `AfterChunk`, folded into that chunk's
pace: `StreamOutcome.PaceMS[AfterChunk]` is the script's ordinary pace for that chunk plus the stall, one number,
because `PaceMS` records what `executeStream` actually sleeps before writing each chunk and the stall is part of
that sleep. `StallBeforeMS` is the same duration lifted back out as its own field so a reader — or
`AssertStreamPacing` (§5.3) — does not have to know which index carries a fold-in to recover it; asserting the
*planned* per-chunk gaps against `PaceMS` on a stall scenario therefore already includes the stall with no separate
addition. `faultOutcome` follows the same split: it does not set `Outcome.DelayMS` for `FaultStreamStall` — that field
means time-to-first-byte for every kind that has one — and reports the mid-stream pause only through
`StreamOutcome.StallBeforeMS` (§5.1). A stall that also wants a slow first byte still declares two attempts, or a
scripted first-chunk `pace`, exactly as the field comment above says.

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
	// vendor field carries it — usage.cost.total_cost on Sonar, response.usage.cost.total_cost
	// on the Agent surface (nested one level deeper, inside the ResponsesResponse the
	// terminal event wraps), costDollars.total on Exa. It exists so a cross-provider
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

**Shipped as (Phase 5 unit 1): `Stream` gains a fifth field, `OmitDone bool`.** This illustrative struct
has nowhere for a scripted `terminal.omit_done` to live: `executeStream` is grammar- and provider-blind (it
is in `provider`, not `profiles/perplexity`), so it cannot reach into a Perplexity-specific
`StreamTerminal` to decide whether to write `[DONE]`. `renderSonarStream` copies
`p.Stream.Terminal.OmitDone` onto `Stream.OmitDone` when building the plan, and `executeStream` reads it
directly. `Stream.Bytes()` and `EncodeSSE` are otherwise exactly as illustrated here.

**Shipped as (Phase 5 unit 2): `Stream` gains a sixth field, `DonePace time.Duration`.** The same gap this
illustrative struct has nowhere to carry: `[DONE]` is never an indexed chunk (see the `Chunks` note just below),
so it has no `StreamChunk.Pace` of its own, and `executeStream` is still grammar- and provider-blind. The
renderer sets it from the script's own default `Pace`, exactly as §4.3's pacing note below already specifies —
`renderSonarStream` copies `p.Stream.Pace.Duration()` onto it directly, with no per-frame override to consult
since `[DONE]` carries none.

**`Stream.Chunks` holds exactly the indexed sequence — the N delta chunks and the one terminal chunk,
`chunk_count = N + 1` elements — and never the `[DONE]` sentinel.** This is what makes `ChunkCount`,
`TerminalIndex`, `AfterChunk` and the abort loop's `i` all range over the same `[0, len(Chunks))` with no
separate accounting anywhere: `StreamOutcome.ChunkCount` **is** `len(Stream.Chunks)`, not a number computed a
second way. On `GrammarDelta`, unless `terminal.omit_done` is set, `executeStream` writes `[DONE]` as one more
write after the loop over `plan.Chunks` completes normally (§4.3) — it is never a `plan.Chunks[i]`, is never a
candidate `AbortAt`/`AfterChunk` target, and `scenario.fault.after_chunk.out_of_range`'s load-time bound (§9) is
exactly `chunk_count`, unaffected by whether `[DONE]` is written at all: the bound excludes it because it was
never indexed, not because of a special case carved out for it. `Stream.Bytes()` and
`Outcome.Stream.BytesPlanned` still include its bytes, because those answer "how many bytes will the wire
carry", a different question from "how many chunks are there" — the two are allowed to disagree by design.
`ChunksSent` (§5.1) counts only indexed chunks the client received, so a completed exchange always shows
`chunks_sent == chunk_count` whether or not `[DONE]` followed it — precisely the signal `terminal.omit_done`
exists to let a consumer's test read off a byte count alone.

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

`handleSonar` gains one branch after rendering, and turn selection is untouched, so a rejected request still consumes
no attempt (§4.4 of the package design). **Validation is not untouched, and saying so was wrong.**
`validateSonarRequest` already raises `CodeStreamUnimplemented` unconditionally for any `body.stream == true`, before
`renderSonar` runs and before the branch below exists — that is the shipped `warn` behaviour today, not a
post-render default. Landing `stream` policy means threading the policy into that decision, the same way Exa's
`validateStream` already takes the policy as a parameter and applies it inside validation rather than after
rendering. Concretely: `validateSonarRequest`'s stream check becomes conditional on `streamPolicy(entry)` —

- **`warn`:** unchanged. `CodeStreamUnimplemented` fires exactly as it does today.
- **`reject`:** unreachable here, because `rejectStream` already returned before `validateSonarRequest` runs.
- **`stream`:** the check does not fire. A request that will actually receive a scripted SSE sequence must not also
  carry a warning promising "this request receives the ordinary non-streaming body" — that promise would be false,
  and §4.1's own `rejectStream` comment already names exactly this failure mode ("Journalling both would leave a
  consumer reading two findings that contradict each other") for the reject case; the same reasoning applies to warn.
  A different WARNING can still fire on this same branch, once `stream_mode: concise` requests exist to trigger it:
  when `body.stream_mode == "concise"` on a request this branch is about to stream, `perplexity.stream_mode.concise.unscripted`
  is journaled (§7, A2; §9). Unlike the suppressed `CodeStreamUnimplemented` check above, this one is additive, not
  a replacement — it says something true (the concise transcript is not what unit 1 renders) rather than something
  false (this response is not a stream).

The sketch below is illustrative and shows the *effect* — a stream branch after rendering — not the *mechanism*; the
mechanism is the paragraph above, inside validation, not a second `if wantsStream(x)` gate bolted on after the body
is already rendered.

```go
	body, err := renderSonar(x, &p, model)
	// ... unchanged ...
	resp := provider.Response{
		Status: http.StatusOK, Body: body, Label: "perplexity.sonar.ok",
		FaultEligible: true, FaultBody: faultBody(SurfaceSonar),
	}
	// streamPolicy(entry) — the ENTRY's policy, read from turn 0, not the
	// selected turn's. This is the same call rejectStream already makes, before
	// SelectTurnFor. Reading the selected turn here would let `reject` and
	// `stream` disagree about the same request, since one is decided before
	// turn selection and the other after.
	if wantsStream(x) {
		switch streamPolicy(entry) {
		case scenario.StreamServe:
			// The DELTAS come from the selected turn's projection; only the policy
			// is entry-level. p is that turn.
			resp.Stream = renderSonarStream(x, &p, model) // *provider.Stream, GrammarDelta
			resp.Label = "perplexity.sonar.stream"
			resp.Header = streamHeader()
		case scenario.StreamReject:
			// Unreachable: rejectStream already returned before turn selection.
			// Kept as a total switch so a future policy value cannot fall silently
			// into the warn default.
			return errorResponse(SurfaceSonar, http.StatusBadRequest, "streaming is not enabled for this provider")
		default:
			x.Warn(CodeStreamUnimplemented, "body.stream", "...") // unchanged today's behaviour
		}
	}
	return resp
```

The `StreamReject` case above is illustrative and shows the *outcome*, not the shipped *mechanism*: shipped
`rejectStream` already returns before `renderSonar` is reachable, calling `x.Fail(CodeStreamUnimplemented, ...)`, and
`handleSonar` turns that into a 422 through `validationResponse` — not the 400 in the sketch. **The finding code stays
`perplexity.stream.unimplemented` for the reject path, unchanged, which is a decision and not an oversight.** Only
`perplexity.agent.stream.unsupported` is renamed in §9, because that rename fixes a structural naming defect — the
surface qualifier sorts before the subject, which breaks a `perplexity.stream.` prefix filter. "Unimplemented" for a
rejected stream is not that kind of defect: the word is still literally true (this exchange will not receive a
stream, whether because streaming does not exist yet or because this scenario's policy declines it), and renaming
it would be a second breaking finding-code change in the same pass for a surface the regression fixture does not
even cover, for no structural gain. It stays.

### 4.2 `Handle` — one condition widens

```go
	dec := x.decision

	// Suppression is decided HERE — before faultOutcome, before the journal
	// condition, before anything else reads resp.Stream. A fault that replaces
	// the stream with an ordinary JSON error means this exchange DOES NOT
	// STREAM, and every reader below has to see that: the early-journal
	// condition, the planned Outcome.Stream, and the deferred close.
	//
	// Deciding it inside execute instead — which an earlier draft did — leaves
	// every one of those readers looking at the outer resp, which execute's
	// local reassignment never touches. The entry then advertises a full stream
	// plan (chunk count, bytes, usage, cost) for a stream nobody writes, and the
	// deferred close stamps client_gone on it, blaming the client for a fault
	// the scenario scripted.
	if dec.Attempt != nil && suppressesStream(dec.Attempt.EffectiveKind()) {
		resp = suppressStream(resp) // drop Stream, restore the JSON Content-Type
	}

	out := faultOutcome(dec, resp)
	// ...
	if out.Aborted || resp.Stream != nil {
		entry.Outcome = out
		record() // journal BEFORE the client can observe ANYTHING
	}
	entry.Outcome = execute(r.Context(), w, dec.Attempt, resp, d.DelayMode, out, closer)
```

The journal condition itself is a two-word change and is the same invariant, stated more generally. Today's rule is
"journal before the socket is touched destructively". A stream makes the client an observer from the first flush, so
the rule becomes **journal before the client can observe anything the handler is about to do**. The aborting case was
always the special case; streaming is the general one.

`resp.Stream != nil` therefore means "this exchange **will** stream", never "this turn declares a stream". By the
time anything reads it, suppression has already been applied.

**The mirror case — a claimed attempt that cannot be honoured because this exchange will not stream — is decided in
the same place, for the same reason.** `dec.Attempt` is already claimed by the time this code runs (turn selection
inside `h(x)` claims it via `SelectTurnFor`/`x.CallIndex`, before the handler even knows whether `resp.Stream` will
be set), and `resp.Stream` reflects only whether *this specific request* asked to stream — the entry's policy can be
`stream` while call 3 in the lane sends `stream: false` (§2, the preamble). So the claimed attempt and the response's
transport can disagree in the direction suppression does not cover: a `stream_*` kind claimed against a `resp.Stream
== nil` exchange. Left alone, `execute`'s existing switch has no case for a `stream_*` `EffectiveKind` and falls into
its `default:`, which writes `resp.Body` as an ordinary 200 — the attempt is consumed and nothing about the fault
happens, silently. That is the outcome §9 already calls "the worst outcome for a test written to prove reconnect
logic", reached one gap earlier.

The fix has the same shape as suppression, checked right beside it:

- If `dec.Attempt` is non-nil, its `EffectiveKind()` is one of the three `stream_*` kinds, and `resp.Stream == nil`,
  `Handle` records a `scenario.stream.abort_unreachable` finding at ERROR severity on `x` — through the same
  recording path `Fail` uses, but without engaging `Fail`'s handler-return convention, since `resp` is already built
  and is still served exactly as scripted. This never silences the mismatch and never reroutes the response.
- `faultOutcome` and `execute` treat the attempt as `FaultNone` for every purpose except the journal's own record of
  intent: `Outcome.FaultKind` still names the scripted kind (`stream_disconnect`, and so on) so a reader can see what
  was asked for, `Outcome.Aborted` stays `false` because nothing was aborted, and `Outcome.DelayMS` is not applied —
  the attempt's `Delay`, if any, is scoped to the stream that never happens. This mirrors how `AbortAfterChunk` and
  `TruncatedAtByte` already record the *scripted* fault rather than the observed one (§5.1); here the whole attempt
  is scripted and unobserved.
- This is the same finding a hand-built entry that skipped load-time validation reaches when a `stream_*` kind is
  claimed against a `truncate_body`-only, non-`stream` entry — the general case §9's `scenario.fault.stream_mismatch`
  guards at load time. Both are "the claimed attempt cannot apply to this exchange's actual transport"; one is
  caught before any request arrives, the other cannot be, because it depends on what a specific request asked for.
  One finding code covers both, at the two points each is reachable. The mirror direction — a hand-built entry claims
  `truncate_body` while `resp.Stream != nil` — is handled identically, not just by the same finding code: `Handle`
  treats it exactly as the `stream_*`-on-non-streaming case above, mirrored. `suppressesStream` (§4.4) does not list
  `truncate_body`, so `execute` takes the streaming branch and the scripted sequence is served in full; `Handle`
  records `scenario.stream.abort_unreachable` at ERROR alongside the append, and `faultOutcome` treats the attempt as
  `FaultNone` for the same reasons as above (`Outcome.FaultKind` still names `truncate_body`, `Outcome.Aborted` stays
  `false`, no `DelayMS`). This case is reachable only when load-time validation was skipped, which is why it is minor
  in practice but not silently divergent from the rule stated here.

**Two declarations must be hoisted.** The deferred fallback below references `resp` and `closer`, and in shipped code
`resp` is declared 35 lines after `Handle`'s existing deferred `recover`/`record` block. Both become `var`
declarations above that defer, and `resp := h(x)` becomes `resp = h(x)`. This is easy to miss because the code reads
correctly in isolation and fails to compile only once assembled.

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
			// Plain Warn, not a warn-once helper: no such helper exists in this
			// repository, and the `closed` guard above already makes this at most
			// one line per request. A consumer whose Journal does not implement
			// StreamCloser wants the line every time, because every entry it
			// affects is one that will stay "open" forever.
			d.Logger.Warn("journal.stream_not_amendable", slog.Uint64("seq", x.Seq))
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
	// The delay runs first for every kind EXCEPT stream_stall, whose Delay is
	// the mid-stream pause planStream resolves instead (§3.1). For every other
	// kind, streaming included, this delay is time-to-first-byte, which is what
	// a consumer's connect timeout observes.
	// ... existing delay block, gains one kind check ...

	// One branch, not a switch. execute does not decide suppression and cannot
	// tell that it happened: Handle applied it before this was called, so a
	// suppressed stream arrives with Stream already nil and takes the ordinary
	// path below exactly as any non-streaming response does.
	if resp.Stream != nil {
		return executeStream(ctx, w, a, resp, mode, out, closer)
	}
	// ... existing switch on EffectiveKind, unchanged ...
}
```

Note what is *absent*: there is no `suppressesStream` call here. It is the single most likely thing for an
implementer to add back, because the fault kind is right there in `a` and the check reads naturally at this point.
It belongs in `Handle` ([§4.2](#42-handle--one-condition-widens)), and putting a second copy here would reintroduce
the exact defect this design already shipped once — two derivations of one decision, where the copy that runs later
is invisible to everything that already read the earlier one.

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

**Shipped as (Phase 5 unit 1):** `executeStream` does take `a *scenario.FaultAttempt` — an early cut of unit
1 dropped it and called `applyHeader(w, resp.Header)` directly, which silently discarded a non-suppressing
attempt's `headers:`/`retry_after`/`status` override on the stream path while the non-streaming default
branch kept applying them; that was a defect against this section's own prose, not an intentional
narrowing, and is fixed rather than noted as a deviation. `executeStream(ctx, w, a, resp, mode, out,
closer)` now applies `faultHeader(a, resp)` and `a.Status` (when `a != nil`) exactly as illustrated above,
before writing `resp.Stream.Chunks` straight through. There is still no
`planStream`/`plan.PaceOf`/`plan.TruncateAt`/`plan.AbortAt`/`hijackReset` — the abort branch in the middle
of this block does not exist yet. Unit 1's scope is the three `stream_*` fault kinds' happy-path absence:
`execute`'s existing pre-dispatch delay block (unmodified — the `stream_stall`-skip carve-out described
below is also a later unit, since that kind does not exist yet) already runs before dispatch, and
`executeStream` sleeps each chunk's own `Pace` (always zero this unit — see §3.1's shipped-as note) and
stops only on a write error or a cancelled context (`journal.StreamClientGone`) or successful completion of
the loop (`journal.StreamCompleted`). The mismatch case — a `truncate_body` attempt claimed on an exchange
that will stream — is handled one level up, in `Handle`: the attempt is treated as `nil` for both
`faultOutcome` and `execute`/`executeStream`'s purposes (so it can never reach a switch that does not exist
yet either), with `Outcome.FaultKind` restored afterward so the journal still names what was scripted. The
plan/abort machinery above is the real design for the unit that adds `stream_disconnect`,
`stream_truncate_chunk` and `stream_stall`; nothing here contradicts it, it is simply not reachable code
yet.

**Shipped as (Phase 5 unit 2):** `planStream`/`streamPlan` exist, but not with the field names sketched
above. `streamPlan` carries `stallAt`/`stallExtra` (folded into `paceOf(i)`), `disconnectAt` (the index
`stream_disconnect` aborts **before writing** — see this section's own banner note above for why "before",
not "after", is the reading this document's prose settles on) and `truncateAt`/`truncateBytes` (the index
`stream_truncate_chunk` writes a partial frame for, then aborts), rather than one shared `AbortAt`/`TruncateAt`
pair: `stream_disconnect` and `stream_truncate_chunk` abort at two DIFFERENT points relative to the write —
before it and after a partial one, respectively — so a single `AbortAt` checked after the write, as sketched
above, cannot express `stream_disconnect`'s frame-boundary-clean semantics at all. `streamPlan.paceMS()`
renders the whole planned schedule for `journal.StreamOutcome.PaceMS` in one pass. `hijackReset` is exported
from `provider/fault_exec.go` (unexported, package-internal) and is now genuinely shared: `truncateBody`'s own
`Reset` branch calls it too, rather than repeating the same three-line Hijack dance a second time. The
pre-dispatch delay block gains exactly the one-kind carve-out this section already described:
`a.EffectiveKind() != scenario.FaultStreamStall`. `plannedStreamOutcome` (fault_exec.go) takes the attempt as
well as the `*Stream` now, calling `planStream` itself to fill in `PaceMS`/`AbortAfterChunk`/`TruncatedAtByte`/
`StallBeforeMS` — see §5.1's shipped-as note.

Note the ordering inside the abort branch: `closeWith` runs **before** `hijackReset` and before the panic. That is the
same discipline `Handle` applies to `record()`, for the same reason — after the RST the client can already observe the
abort, so anything a test might read has to be durable by then.

Four more wire-visible details the sketch leaves to guesswork, decided here rather than at Phase 5's first unit,
because each is observable on the wire and two implementers guessing independently would build different fixtures:

- **`streamHeader()` sets exactly two headers**: `Content-Type: text/event-stream` and `Cache-Control: no-cache`.
  Nothing else — no `Connection` (chunked transfer already keeps the connection open) and no
  `X-Accel-Buffering` (that header exists for an nginx reverse proxy this container does not run, and stating a
  header for infrastructure the simulator has no control over would be a fidelity claim it cannot back).
- **Attempt headers apply on the stream path too.** `applyHeader(w, resp.Header)` above is illustrative shorthand;
  the real call is `applyHeader(w, faultHeader(a, resp))`, the same `faultHeader` the non-streaming switch already
  uses, so a `headers:`/`retry_after` declared on a non-suppressing attempt (`kind: none` with `headers:`, or a
  `stream_*` kind with `headers:`) reaches the client instead of silently vanishing. `faultHeader` still keeps
  `Content-Type: text/event-stream` in this path — it copies `resp.Header` and only overrides `Content-Type` for an
  attempt whose kind is `content_type`, `wrong_content_type` or `invalid_json` (§4.4), none of which a `stream_*`
  attempt is — which is exactly what makes it the right call to route through rather than `resp.Header` alone.
- **`truncate_after_bytes` unset on `stream_truncate_chunk` means half of that one chunk's bytes**, not half the
  stream. `planStream` resolves it with the same rule `truncationLen` already applies to `truncate_body` — zero
  means half, clamped to the chunk's length — applied to `plan.Chunks[AfterChunk].Bytes` rather than to the whole
  body. Reusing the field name without reusing this rule would make the same YAML key mean two different halves
  depending on which fault kind it appeared on. One consequence of reusing the rule exactly: a `truncate_after_bytes`
  at or beyond the target chunk's own length clamps to the WHOLE chunk, same as `truncate_body`'s equivalent clamp
  does for the whole response body — the "truncation" writes a complete, well-formed frame and the client then
  observes a disconnect indistinguishable from `stream_disconnect` at the next index. That degrades the kind's
  malformed-frame promise for this one deliberately-out-of-range input; it is not treated as an error, on the same
  reasoning `truncate_body` already applies.
- **`pace:` gates every chunk `executeStream` writes, including chunk 0, the terminal chunk, and (on
  `GrammarDelta`, when not omitted) `[DONE]`** — not only the scripted deltas after chunk 0. `plan.PaceOf(i)` is
  defined over every index in `plan.Chunks`, which `EncodeSSE` builds from the N delta chunks and the one
  terminal chunk — `chunk_count` elements, per §3.2's note that `[DONE]` is never one of them. Under A3 (§7)
  there is no separate role-only opening chunk: role and content ride together on every one of the N chunks, so
  chunk 0 IS `deltas[0]`, and the gap between headers-flushed and its write is `deltas[0]`'s own
  `StreamDelta.Pace` override when the script sets one, falling back to the script's default `Pace` otherwise —
  exactly like every other delta's chunk (in addition to, not instead of, any time-to-first-byte `Delay` a fault
  attempt declares; the two are independent gaps at the same point in the sequence). `StreamTerminal.Pace`
  overrides the default for the terminal chunk. `[DONE]`, not being indexed, has no per-frame override field to
  carry one — it always uses the script's default `Pace`, applied as one more `sleep` after the loop over
  `plan.Chunks` completes normally, not through `plan.PaceOf(i)`.

### 4.4 Faults that suppress the stream

A real vendor does not answer a `stream: true` request with a 429 wrapped in SSE. The error happens before the stream
starts, so the response is an ordinary JSON error. `suppressesStream` therefore returns true for `status`,
`invalid_json`, `wrong_content_type`, `empty_body`, `extra_fields` and `close_before_headers`, and the request is
served by today's code path against `Response.Body` and `Response.FaultBody`.

`suppressStream` must reset `Content-Type`. `faultHeader` copies the handler's header first, so `text/event-stream`
would otherwise leak onto a JSON error body — a fidelity bug that would teach a consumer to parse the wrong thing.

`truncate_body` is the one non-streaming kind that is **rejected at load** on an entry whose policy is `stream`
(`scenario.fault.stream_mismatch`), naming `stream_truncate_chunk` as the streaming spelling. Silently
reinterpreting it would be the wrong kind of helpful. The mirror check applies too: a `stream_*` kind on an entry
whose policy is *not* `stream` is a load-time error.

Both checks key on the entry's **effective policy**, never on the presence of a `stream:` key — `warn` and `reject`
declare a policy and produce no stream, so `truncate_body` remains valid with them. See
[§9](#9-validation-findings-this-adds) for why that distinction is load-bearing and for the fixture that guards it.

**Shipped as (Phase 6 unit 3):** `oversized_body` is the second non-streaming kind rejected at load under `stream`
(`scenario.fault.stream_mismatch`, same as `truncate_body`, for the same reason: it sets an exact `Content-Length`
before writing, which is wrong for chunked SSE), and its request-time mirror case raises the same
`scenario.stream.abort_unreachable` that `truncate_body`'s does. It is likewise absent from `suppressesStream` — a
real vendor does not answer `stream: true` with a padded JSON document either.

**Shipped as (Phase 6 unit 5):** `delay_after_headers` — a modifier, not a kind, so it rides along with whatever
kind an attempt carries — is rejected at load on a streaming entry too, but only for a kind that would not
otherwise reach `suppressStream` above: `truncate_body` and `oversized_body` already have their own rejection, and
a kind IN `suppressesStream`'s set stays valid, because it turns the response into the ordinary JSON body
`delay_after_headers` already knows how to hang before writing. `stream_stall` with `after_chunk: 0` is the
streaming-aware spelling. The request-time mirror raises the same `scenario.stream.abort_unreachable`, from a
third mirror direction (the fourth `case` in `Handle`'s suppression switch) beside the two §4.2 names — never a
change to `suppressesStream` itself, which the modifier does not touch.

#### Suppression is decided once, before the entry is journaled

An earlier draft decided suppression inside `execute`, against `execute`'s own copy of the response. That is too
late, and it produces a journal that lies.

[§5](#5-the-journal) appends the entry **before the first byte**, carrying every planned field — chunk count, bytes,
pace, event names, usage, cost. If suppression is decided after that, a scripted `status: 429` on a streaming turn
journals a fully-specified stream that is never written, and then stamps a state (`client_gone`) implying the client
caused it. A consumer reading `outcome.stream.usage` for spend attribution would read the cost of a response that
never existed.

The rule, matching how this repository already treats the lane and the fault claim: **one decision, made once, at the
point the outcome is computed.**

- `suppressesStream(kind)` is evaluated where `Handle` builds the entry, immediately after the attempt is claimed
  and before the append — the fault decision is already known there.
- When it is true, `Outcome.Stream` is **nil** and the entry is an ordinary non-streaming one. There is no partial
  stream outcome, no `open` state to reconcile, and nothing for the close to amend.
- `execute` does not re-derive it. It reads the decision that was already made, exactly as a handler reads
  `x.Lane()` rather than recomputing it. Two derivations are two chances to disagree, and here the disagreement is
  invisible until someone audits a cost report.

`Response.Stream` being non-nil therefore means "this exchange **will** stream", not "this turn declares a stream".
The suppressed case never reaches the streaming path at all.

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

**Shipped as (Phase 5 unit 1):** `journal.StreamOutcome` carries `Grammar`, `ChunkCount`, `BytesPlanned`,
`TerminalIndex`, `Usage`, `CostTotal`, `State` and `ChunksSent` — every field above is present except
`PaceMS`, `EventNames`, `AbortAfterChunk`, `TruncatedAtByte` and `StallBeforeMS`, all five of which are
fault- and pacing-scoped (the three `stream_*` fault kinds, `after_chunk`, and any nonzero pace) and
therefore have nothing to record in a unit that ships none of those. Adding them is additive — the next
unit's job, not a correction to this one.

**Shipped as (Phase 5 unit 2):** `PaceMS`, `AbortAfterChunk`, `TruncatedAtByte` and `StallBeforeMS` now land,
exactly as this note anticipated and exactly as illustrated above — every one is a PLANNED field, computed once
by `plannedStreamOutcome` before the first byte, never amended by `CloseStreamIn`. `EventNames` does not: it is
scoped to `GrammarTyped` (unit 3), which carries named `event:` lines; `GrammarDelta`, the only grammar this
build renders, has none, so there is nothing for the field to hold yet — the same "nothing to record" reasoning
the unit-1 note above already gives, now narrowed to the one field still genuinely out of scope. `PaceMS` is the
PLANNED schedule (`streamPlan.paceMS()`), not an observed wall-clock measurement — nothing on this path reads a
clock — which is what makes it stable under both `DelayReal` and `DelaySkip` and safe to read before the
exchange closes, exactly as this document's own `AssertStreamPacing` note (§5.3) and §6.3 already say.
`Stream.DonePace` (§3.2) is deliberately absent from `PaceMS`: `[DONE]` is never an indexed chunk, `PaceMS` is
indexed over `plan.Chunks` only, and there is no separate journal field for the sentinel's own gap either — a
reader cannot recover it from the entry. That is acceptable because `[DONE]`'s pace is always the script's default
(it has no per-frame override to carry one), so it is already the same number as `PaceMS[0]` whenever chunk 0 also
used the default; it is exercised end to end by `provider.TestHandleStreamDonePaceIsHonouredUnderDelayReal`
(`provider/stream_test.go`), which reads it from `Outcome.BytesWritten`/`State` rather than from `PaceMS`.

**Shipped as (Phase 5 unit 3):** `EventNames` now lands too, exactly as the unit-2 note anticipated. It is
`streamPlan.eventNames()` (`provider/stream.go`), computed the same PLANNED way `paceMS()` is — a pass over
`plan.chunks`, no clock read — and wired into `plannedStreamOutcome` alongside `PaceMS`. It is `nil`, not an empty
slice, whenever every chunk is unnamed, which is how a reader tells the two grammars apart by the field's presence
alone: `GrammarDelta` streams (Sonar) still produce `nil`, `GrammarTyped` streams (the Agent surface) produce the
six-or-more names in wire order. Grammar-blind by construction — `eventNames()` reads `StreamChunk.Name`, which
`EncodeSSE` already populated from `SSEEvent.Name` since unit 1 — so no branch anywhere in `provider` had to learn
that a second grammar exists. `TestAgentStreamJournalOutcome` (`profiles/perplexity/stream_test.go`) pins it
end-to-end through the real Agent handler; `TestPlanStream`'s two `eventNames` subtests (`provider/stream_test.go`)
pin the pure function against both a `nil`-producing unnamed stream and a hand-built named one.

**`Terminal`/`TerminalIndex` mark the closing frame, not the presence of usage.** Every stream this design
produces has exactly one terminal chunk — `StreamTerminal` tunes what it carries, it does not make it optional
— so `TerminalIndex` is always `chunk_count - 1` for a fully-scripted `GrammarDelta` or `GrammarTyped` stream;
the `-1` case is reserved for a future grammar this document does not define, where no chunk is ever marked
`SSEEvent.Terminal`. `StreamTerminal.OmitUsage` removes the `usage` key from that frame's JSON payload —
`Stream.Usage`/`StreamOutcome.Usage` are then `nil` and `SSEEvent.Terminal` is still `true` on that same frame,
because the frame is still the one carrying `finish_reason` and the full `message`; omitting usage does not
move which chunk is terminal. `AssertStreamUsage` on an `omit_usage` scenario therefore asserts `nil` the same
way it would assert any other declared shape — it does not need `TerminalIndex` to change meaning to do it.

`Outcome` gains `Stream *StreamOutcome json:"stream,omitempty"`, nil for every non-streaming request, so no existing
journal consumer sees a changed shape.

One existing field keeps its exact meaning for a stream, and one keeps its meaning with a pre-existing exception that
is worth restating rather than assumed:

- **For a streamed exchange, `Outcome.BytesWritten` is observed, not planned.** It is `0` at append and is filled in
  by the close. `Stream.BytesPlanned` is the plan. This is new behaviour for a streamed exchange, not a universal
  rule restated: `faultOutcome` already sets `Outcome.BytesWritten` at append time for `truncate_body`, computed by
  `truncationLen` before a byte is written, and that non-streaming exception is unchanged by this design. A reader
  checking whether a given `Outcome.BytesWritten` is planned or observed asks `outcome.stream != nil` first, and
  `dec.Attempt.EffectiveKind() == truncate_body` second; there is no single rule that answers it for every kind.
- **`Outcome.Aborted` reflects the script**, set at append, exactly as it already is for `truncate_body`.

`CompletedAt` is the one field whose meaning is genuinely time-dependent, so it is stated rather than left to be
inferred: **for a streamed exchange, `completed_at` is the instant the response was decided, until the close amends it
to the instant the last byte was written.** `outcome.stream.state` says which one you are holding — `open` means the
first, anything else means the second. `AssertOverlapped` is therefore correct for streams read after close and
meaningless for streams read while open, which `testkit` enforces by making the wait explicit (§5.3).

### 5.2 Amending an appended entry

The close needs to reach an entry that is already stored. `Journal` is a consumer-implementable interface — adding a
method breaks every implementation outside this repository — so the close is an **optional capability**, declared and
reached exactly the way `Namespaced` already is in `internal/journal/entry.go`:

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
returns. `AwaitStreamClosed` polls with a deadline, exactly as the existing `Sim.AwaitRequests` already does for
arrival.

**`AwaitStreamClosed` follows the pair `AwaitRequests` already establishes, not a bare `seq`.** `journal.NextIn`
draws `Seq` from the namespace's own counter, so a sequence number alone does not identify an entry across
namespaces — the same reason `Sim.AwaitRequests` and `Namespace.AwaitRequests` already exist as two methods rather
than one taking a namespace string. `AwaitStreamClosed` is that same pair: a `Sim` method reading the default
namespace and a `Namespace` method reading its own, both taking only `seq`, because the namespace is the receiver
rather than a parameter. This is mechanical once stated — the shape to copy already ships — so it is not left to
Phase 5's first unit to invent.

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

**Shipped as (Phase 5 unit 4):** both assertions landed with these exact signatures (`testkit/stream.go`).
`AssertStreamUsage` decodes `want` and `e.Outcome.Stream.Usage` and diffs them with `go-cmp`, the same semantic
comparison `AssertGoldenJSON` gives a response body, after round-tripping `want` through JSON so a caller's struct
or map literal compares against the decoded field on equal terms; a `terminal.omit_usage` scenario's nil `Usage`
is asserted by passing `nil` for `want`, exactly as this section's own sentence above anticipated. Units 1–3's own
"Shipped as" notes on this document did not mention it because the unit-4 spec's own symbol list for `testkit`
omitted it by name (a scope call the spec left to the unit, not a design change) — it is recorded here rather than
silently missing so a reader of this section does not go looking for it in vain.

Chunk **bytes** are deliberately not journaled. A stream is unbounded where a request body is not, `MaxJournalBodyBytes`
bounds only the request, and the consumer already holds every byte — it is the client. Golden-file regression over an
SSE exchange is taken client-side, over the reassembled stream.

### 5.4 `AssertGoldenSSE`

The backlog names this unit; this document did not, which left every shape decision to whoever builds it first. It
follows `AssertGoldenJSON`'s existing pattern (`testkit/golden.go`) rather than inventing a new one, because an SSE
transcript and a JSON body differ in framing, not in what "golden" means:

- **Unit of comparison: parsed frames, not raw bytes.** The caller passes the reassembled response body — the
  client already did the read-boundary reassembly §6.2 requires — and `AssertGoldenSSE` parses it into the same
  `(event, data)` pairs `EncodeSSE` produces, then diffs frame-by-frame with `go-cmp`, exactly as `AssertGoldenJSON`
  diffs decoded JSON rather than raw bytes. A byte-for-byte comparison would fail on the read-boundary
  non-determinism §6.2 documents even though the frames are identical; a frame comparison does not.
- **Each frame's `data` line is decoded as JSON and compared semantically**, the same way `AssertGoldenJSON` already
  compares a body — key order is not part of any wire contract here, so it is not part of the golden either. The
  bare `[DONE]` token is not JSON and is compared as the literal string it is.
- **Derived identifiers are ignored by default, mirroring `AssertGoldenJSON`'s `derivedIDPaths`.** Chunk `id`
  advances with call index by design (§6.1), so a golden bound to one call position would fail on every other one; a
  `GoldenExactIDs`-equivalent option opts back in for a route with no fault plan, where the identifier is stable.
  `[DONE]` is part of the golden — omitting it is what `terminal.omit_done` exists to script, and a golden that
  silently ignored the sentinel could not tell a completed stream from a scripted omission.
- **File extension is `.sse`**, matching the fixtures §10 already names (`perplexity-sonar-stream.sse`, and so on),
  stored under `contracts/perplexity/` beside their non-streaming counterparts.
- **A one-delta change diffs as one frame**, not a whole-file diff, because the comparison is per-frame: this is the
  entire reason `AssertGoldenJSON` is not simply pointed at the reassembled bytes, and the reason the backlog names
  this as its own unit rather than a call site of the existing one.

**Shipped as (Phase 5 unit 4):** the derived-identifier default above is `derivedIDPaths` applied per frame, exactly
as sketched, for `GrammarDelta` (Sonar) — its frames carry the same top-level `id` `AssertGoldenJSON` already
prunes. `GrammarTyped` (Agent) frames wrap a typed event payload rather than a bare response body, so their
call-index-derived ids sit at `response.id`, `item.id`, `item_id`, and — inside `response.completed` — every
element of `response.output[]`. The first three are additional dotted paths (`streamDerivedIDPaths`,
`testkit/golden.go`), pruned by the same mechanism. The array element case cannot be: `GoldenIgnore`'s path syntax
does not address array elements by design (a golden that ignores one element of an array is pinning the wrong
element), and this needs *every* element's `id` gone, not one chosen by index — so it is pruned unconditionally by
a small dedicated step (`pruneResponseOutputIDs`) rather than extended path syntax. Without this, an Agent stream
golden failed on every frame at any call index but the one it was recorded at; `TestAssertGoldenSSE`'s
"Agent (GrammarTyped) derived ids..." subtest (`testkit/golden_test.go`) pins it.

---

## 6. Determinism

### 6.1 Byte-identical chunk sequences

The chunk sequence is rendered by the same machinery as a non-streaming body and inherits its guarantees verbatim:
identifiers from `internal/ids` over stable fixture keys, timestamps from `Scenario.BaseTime()`, JSON through
`internal/wire` with `encoding/json`'s deterministic key ordering, no `time.Now()`, no randomness, no map iteration.
The same request at the same call index in the same lane produces the same bytes, and — as §3.1 of the package
design already establishes — a *different* call index deliberately produces different identifiers, so chunk `id`
fields advance across calls the way a real vendor's do.

**`created` is held CONSTANT across every chunk of one stream, not moving the way the vendor's own example shows**
(§7, A3): the value is `time.base`, simulator-chosen — a byte-stable golden needs a constant, and §6.2 already
forbids asserting on wall-clock-shaped fields, so pinning `created` at a fixed, injected value rather than a moving
one is the only choice consistent with the rest of this section. **`search_results` rides on every chunk in full
mode**, not only the terminal one (§7, A3): the vendor states they appear "multiple times during stream" without
saying which, so every-chunk is the deterministic reading, and it stays byte-stable because the array is rendered
once from the turn's projected `search_results` field and repeated verbatim, never re-derived per chunk.

Framing is fixed by `EncodeSSE`: optional `event:` line, one `data:` line per line of payload, one blank line. Payloads
are compact JSON and contain no newline, so in practice every frame is exactly two or three lines.

### 6.2 What is *not* byte-stable, and must not be asserted on

Chunked transfer encoding means the **TCP read boundaries a client observes are not deterministic**. The probe in §1
shows an 18-byte body arriving as `"data: a\n"` then `"\ndata: b\n\n"` — one frame split across two reads, purely from
segmentation. A golden taken over `read()` boundaries will flake. Goldens are taken over parsed frames, per §5.4's
`AssertGoldenSSE`. `contracts/perplexity/README.md` does not record this rule today — its "Streaming (SSE)"
section (verified 2026-08-15) covers the frame envelope, the `stream_mode` grammars and the `EventType` enum,
but nothing about read-boundary non-determinism or goldens — so writing it there is still a Phase 5 obligation
this design creates, not a rule already in place to be pointed at.

### 6.3 Pacing without wall-clock dependence

Pacing is deterministic in the only sense that is achievable and the only sense that is useful:

- Every gap is a **lower bound** declared by the scenario, honoured by the existing `sleep(ctx, d, mode)` helper in
  `provider/clock.go`. Observed gaps are `d + ε`; the probe measured 121–122 ms for a scripted 120 ms.
- **No chunk's content depends on elapsed time.** The simulator never reads a clock to decide what to send, only to
  decide when. Bytes are identical whether the stream took 200 ms or 20 s, so a golden is stable across `DelayReal`
  and `DelaySkip` alike.
- **`DelayMode` governs pacing**, so a 12 s heartbeat scenario costs nothing in a unit test under
  `testkit.WithSkippedDelays`, and the planned schedule is still in `outcome.stream.pace_ms` for the assertion. A test
  that must observe a real timeout runs under the default `DelayReal` — the same rule `DelaySkip` already carries.
- **No fake clock.** The repository deliberately has none, and streaming does not change that: a client deadline
  and a Temporal heartbeat are both observed by *bytes not arriving*, which no server-side fake can produce.

---

## 7. The two SSE grammars

### Resolved 2026-08-15

`contracts/perplexity/README.md`'s "Streaming (SSE)" section — §10 step 1, done 2026-08-15 — forced five
changes on §2/§3/§9. Each is resolved below: the vendor line that forced it, the decision (A1–A5, taken by the
orchestrator), and the reason.

- **`stream_mode` selects between two frame sequences; unit 1 builds only one.** Vendor: "Full Mode: Single type
  (`chat.completion.chunk`)" versus "Concise Mode: Multiple types for different stages"
  (`sonar/pro-search/stream-mode.md`). **A1 —** unit 1 ships `GrammarDelta` in `stream_mode: full` only, the
  vendor default and therefore what a client that sends no `stream_mode` gets. Concise mode is a later unit:
  full mode is one object type, while concise adds four object types and a reasoning-stage vocabulary the
  scenario grammar does not have, and deserves to be designed once, from the verbatim frames, not squeezed into
  unit 1.
- **Concise mode's reasoning frames have no representation in `StreamScript`/`StreamDelta`.** Vendor:
  `chat.reasoning.done` carries `usage`, `search_results` and `images` on **both** done-frames, a shape
  `StreamDelta` — text-only today — cannot express (`sonar/pro-search/stream-mode.md`). **A1, continued —**
  left unscripted for unit 1, for the same reason as above; the concise unit adds its own vocabulary rather
  than bending this one's.
- **A `stream_mode: concise` request that also sets `stream: true`, against an entry whose policy is `stream`**
  — the only combination where anything actually streams for the mismatch to apply to; a `stream_mode: concise`
  request that does not itself ask to stream never reaches this at all, because nothing streams for it to
  diverge from. **A2 —** served the full-mode sequence, and a WARNING is journaled:
  `perplexity.stream_mode.concise.unscripted` (§9). The request is valid — the enum passes — and the simulator
  can only produce one grammar today; serving-plus-warning is exactly how `stream: true` itself has been
  handled since v0.1.0 (`perplexity.stream.unimplemented`, warn + JSON body): the consumer's test still runs,
  the divergence is visible in the journal, and `validation.promote` makes it fail for a suite that must not
  accept it. Rejecting with a 422 would invent a vendor error for a request the vendor accepts.
  `contracts/perplexity/README.md`'s "What Servicesim simulates" section is updated alongside this amendment to
  say so explicitly: streaming is not simulated in the shipped build today, and once unit 1 ships,
  `stream_mode: concise` will not be either — a concise request against a streaming entry gets the full-mode
  transcript and this warning, not rejection and not the concise sequence. `warn`/`reject` policy behaviour is
  unaffected — this is a policy-`stream` matter only.
- **`usage` placement is confirmed for concise mode, not for `full` — the mode unit 1 actually renders.**
  Vendor: "Cost information is only available in the `chat.completion.done` chunk," stated for `concise`
  (`sonar/pro-search/stream-mode.md`); no fetched page states `full`-mode placement. **A3 —** pinned
  terminal-only for `full` mode: one terminal chunk carries `finish_reason`, the aggregated `message`, `usage`
  (with `cost`) and `search_results` together, simulator-chosen and correctable from a captured live response
  per §10 step 3. The full frame sequence this produces, and which parts of it are vendor-pinned versus
  simulator-chosen, is below. For a turn with N scripted deltas the sequence is **N chunks, then one terminal
  chunk, then `data: [DONE]` — N + 1 chunks in total** (`[DONE]` is a sentinel, not a chunk; §9 re-derives
  `after_chunk`'s bound from this count).
- **`[DONE]` on the Responses/Agent surface, and whether `GrammarTyped` carries a named `event:` line, are both
  unstated by the vendor, not contradicted** — the router-page misattribution that once made the first look
  contradicted is withdrawn (§10, `contracts/perplexity/README.md`). **A4 —** `[DONE]` stays a chat-completions
  concept; `GrammarTyped` emits none — the existing simulator choice stands, since delta 4 resolved as unstated
  rather than as a reason to change it. `GrammarTyped` frames do carry an `event: <type>` line: unpinned by
  Perplexity, but the OpenAI Responses streaming dialect the Agent API declares compatible with does; recorded
  as simulator-chosen, correctable from a captured live response (§10 step 3). `GrammarTyped` is unit 2, after
  unit 1.

Nothing else in this section is a sixth open item: **A5 —** §2's scenario vocabulary is unchanged for unit 1 —
`when_requested: stream`, `deltas:`, the terminal frame's `usage`/`cost`/`search_results` sourced from the
turn's existing projection fields exactly as the non-streaming body's are, and `StreamTerminal.OmitDone` as
designed. Concise mode's `chat.reasoning`/`chat.reasoning.done` get their own vocabulary in the concise unit,
additive to version 1 under §8's test — not a gap unit 1 needs to close.

The two grammars, `GrammarDelta` and `GrammarTyped`, are genuinely different and both are in scope, because the
adopter's client uses the first and their migration target uses the second.

**Chat completions (`GrammarDelta`), `stream_mode: full`** — `POST /chat/completions`, `POST /v1/sonar`,
`POST /v1/chat/completions` (`NameSonar`'s `SonarRoutes()`, all three spellings). Unnamed frames; the payload is
a `chat.completion.chunk` object. **For a turn with N scripted deltas: N chunks, then one terminal chunk, then
the bare token `data: [DONE]` — N + 1 chunks in total** (`[DONE]` is a sentinel, not a chunk).

Every one of the N chunks carries: `object: "chat.completion.chunk"` (vendor-pinned, by name); a constant `id`,
derived per §6.1 (vendor-pinned as a `chat.completion.chunk` field; that it stays constant across the stream's
chunks in `full` mode is simulator-chosen, but it agrees with, rather than contradicts, the vendor's own
`concise`-mode example: the three chunks sharing `id: "cfa38f9d-..."` keep that `id` fixed while `created`
changes under them, §10 — it is `created`, not `id`, the vendor's own example moves); `model` echoed
from the request (vendor-pinned); `created` held CONSTANT across the whole stream at `time.base`, unlike that
same `concise` example (simulator-chosen, §6.1 — a byte-stable golden needs a constant, and §6.2 already
forbids asserting on wall-clock-shaped fields, so this is the one field where the design deliberately departs
from what the vendor's own example shows rather than merely going beyond it); one `choices[0]` with `index: 0`
(vendor-pinned); `delta: {role: "assistant", content: <this chunk's piece>}` together with `message: {role:
"assistant", content: <the aggregate through this piece>}` (the vendor states full mode aggregates server-side
and includes `choices.message`; the exact field shape is taken from the `concise`-mode `chat.completion.chunk`
example, marked as an inference in `contracts/perplexity/README.md`); `finish_reason: null` (inferred from that
same `concise`-mode example, which shows `null` there; unstated for `full` mode by any fetched page per the
contract's NOT-stated table, so this is simulator-chosen for `full` mode specifically, not vendor-pinned); and
`search_results` on every chunk (simulator-chosen — the vendor states they appear "multiple times during
stream" in full mode without saying which, and the key is omitted, not emitted empty, on a turn that projects
none, exactly as the non-streaming body's `renderSonarResults` already omits it). No `citations`: they appear
in no fetched vendor frame, at any scope.

Two of the labels above — `index: 0` and `model` echoed — rest on the same `concise`-mode examples as
`finish_reason` does, and the contract's prose does not confirm either one for `full` mode specifically either.
They stay labelled vendor-pinned rather than downgraded alongside `finish_reason`, because they are structural
properties of a single-choice chat-completions payload — one request, one model, one `choices` slot — that do
not plausibly vary by `stream_mode`, unlike `finish_reason`, whose *value* depends on completion state and is
exactly the kind of field a mode split could plausibly change. Not a contradiction either way, only a lower
confidence than the other vendor-pinned fields in this list, worth naming rather than leaving implicit.

**Shipped as (Phase 5 unit 1):** this section names no Go type for the chunk payload; the shipped one is
`profiles/perplexity/response.go`'s `ChatCompletionChunkResponse` (plus `ChatCompletionChunkChoice` and the
`ObjectChatCompletionChunk` constant), following this package's existing convention of exporting every wire
type it renders (`CompletionResponse` and friends). They are a compatibility obligation from this point on,
same as any other exported type in this package (CLAUDE.md house rule 7).

Field values otherwise follow the same rules `renderSonar` already applies to the non-streaming body, not a
separate set invented for the stream: `id` is `p.CompletionID` when the turn sets one, else the same
`ids.UUIDv5` derivation (§6.1); `model` is `firstNonEmpty(p.Model, requestModel, Models[0])`; the terminal
chunk's `finish_reason` is `p.FinishReason` when the turn sets one, else `"stop"` — so a turn that scripts
`finish_reason: length` gets `length` on the terminal chunk, not a hardcoded `"stop"`. `created` is the one
field that does **not** follow the non-streaming rule (`p.Created` when non-zero, else `BaseTime().Unix()`): it
is pinned CONSTANT at `time.base` for every chunk regardless of `p.Created`, for the reason given above. A
scenario that pins `completion_id`/`model`/`finish_reason` and is served both ways therefore gets a stream and
a JSON body that agree on every field except `created`.

The terminal chunk carries: `delta: {role: "assistant", content: ""}`; the full aggregated `message`;
`finish_reason: "stop"` (or `p.FinishReason`, per the rule above); `usage` (with `cost`) — placement is **not**
stated for `full` mode by any fetched page and is pinned here as terminal-only, simulator-chosen, correctable
from a captured live response per §10 step 3; `search_results`; and, when the turn projects them, `images` and
`related_questions`, plus `extra_fields` merged onto the terminal chunk's payload exactly as `wire.Render`
merges them onto the non-streaming body. These three are terminal-only, mirroring where `usage` lands, not
scripted onto every chunk: they are properties of the completed turn, not of an individual delta, and the
`extra-fields` scenario's consumer-tolerance purpose (CLAUDE.md rule 5) is exercised by any one frame carrying
the unknown key — repeating it on N further frames buys nothing and only makes the golden noisier.

`data: [DONE]` follows — vendor-pinned for `concise` mode by the raw code sample in
`contracts/perplexity/README.md`, resting for `full` mode only on the declared OpenAI compatibility, and
recorded as such rather than as a direct Sonar statement.

JSON key order below matches `ChatCompletionChunkResponse`'s field order (id, object, model, created, ...),
which is what `contracts/perplexity/perplexity-sonar-stream.sse` actually pins — key order is not a wire
contract (§5.4), but there is no reason for an illustrative example to disagree with the golden it illustrates.

```text
data: {"id":"...","object":"chat.completion.chunk","model":"sonar-deep-research","created":1767225600,"choices":[{"index":0,"delta":{"role":"assistant","content":"Report A "},"message":{"role":"assistant","content":"Report A "},"finish_reason":null}],"search_results":[...]}

data: {"id":"...","object":"chat.completion.chunk","model":"sonar-deep-research","created":1767225600,"choices":[{"index":0,"delta":{"role":"assistant","content":"finds "},"message":{"role":"assistant","content":"Report A finds "},"finish_reason":null}],"search_results":[...]}

data: {"id":"...","object":"chat.completion.chunk","model":"sonar-deep-research","created":1767225600,"choices":[{"index":0,"delta":{"role":"assistant","content":"that X."},"message":{"role":"assistant","content":"Report A finds that X."},"finish_reason":null}],"search_results":[...]}

data: {"id":"...","object":"chat.completion.chunk","model":"sonar-deep-research","created":1767225600,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"message":{"role":"assistant","content":"Report A finds that X."},"finish_reason":"stop"}],"usage":{"prompt_tokens":19,"completion_tokens":240,"total_tokens":259,"reasoning_tokens":5120,"cost":{"input_tokens_cost":0.0002,"output_tokens_cost":0.0024,"reasoning_tokens_cost":0.0102,"total_cost":0.0128}},"search_results":[...]}

data: [DONE]

```

`search_results` is elided above (`[...]`) rather than spelled out as `{"source":"source-a"}`, which is the
scenario's projection shorthand (`PerplexityResult`), not the wire shape `EncodeSSE` actually renders: the same
`SearchResult` object (`title`/`url`/`snippet`/`date`/`last_updated`) the non-streaming body emits from that
`source:` reference, via the same `renderSonarResults` machinery §6.1 already says the stream inherits
verbatim. Eliding it here, rather than writing out one hand-picked set of wire values, keeps this text example
from being read as its own contract for a field this document does not otherwise pin the shape of; the golden
fixture `perplexity-sonar-stream.sse` (§10) is where the real wire shape gets checked byte-for-byte.

**Vendor-verified 2026-08-15** (`contracts/perplexity/README.md`'s "Streaming (SSE)" section): the vendor pins
unnamed `data:` frames terminated by `data: [DONE]` (confirmed concise-mode-specific by example; full mode's
support is only the OpenAI-compatibility declaration), and — for `stream_mode: concise` only — the four `object`
types and that `usage`/`search_results`/`images` ride on **both** `chat.reasoning.done` and
`chat.completion.done`, while `cost` rides on `chat.completion.done` only. It does **not** pin `usage` sharing a
chunk with `finish_reason` in `full` mode, the exact `id`/`created` behaviour per chunk in `full` mode (the
vendor's own `concise`-mode example shows `created` changing per chunk while `id` stays constant — a
counterexample to the OpenAI-compatibility "repeat unchanged" pattern, not a confirmation of it), or
chunk-to-token granularity; those remain simulator-chosen. The frame example above renders `full`-mode-shaped
chunks — the mode the vendor's usage-placement statement above was not verified for — pinned instead by A3 in
"Resolved 2026-08-15" as terminal-only, simulator-chosen and correctable per §10 step 3.

**Responses / Agent (`GrammarTyped`)** — `POST /v1/agent`, `POST /v1/responses`, `POST /responses`
(`NameAgent`'s `AgentRoutes()`, all three spellings). Every frame carries an `event:` line
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

**Shipped as (Phase 5 unit 3):** this three-frame sketch was always illustrative (the banner), and the shipped
sequence is eight frames for the three-delta example above, not three — `response.created`,
`response.output_item.added`, three `response.output_text.delta`, `response.output_text.done`,
`response.output_item.done`, `response.completed`. The sketch omitted `output_item.added`/`.done` and
`output_text.done` entirely; the P5U3 unit-scope spec that commissioned this build named all three explicitly, so
they are not an extension the implementing unit invented, only ones this illustrative block never showed. What the
sketch's minimalism DID settle correctly, per its own reading here: `response.in_progress` — a member the
`ResponseCreatedEvent`/`ResponseInProgressEvent`/`ResponseCompletedEvent` schemas make optional and structurally
interchangeable — is NOT emitted; this sketch's own choice to go straight from `created` to the first `delta` is
the minimal sequence the shipped renderer (`renderAgentStream`, `profiles/perplexity/agent.go`) keeps, rather than
inventing a fourth envelope-only event nothing in this document ever asked for. `sequence_number` starts at 0 and
is monotonic across every frame, matching the numbers shown here. `response.completed`'s `response` is not
hand-assembled the way this sketch's elided `output: [...]` suggests: it is the byte-identical `ResponsesResponse`
`agentResponse` builds for `renderAgent`'s non-streaming body, the same struct, not a second rendering of it — see
the "One mechanism serves both" note just below, corrected in the same pass. `response.created`'s own `response`
carries more than this sketch's `{id, status}` pair: `object`, `model`, `created_at`, an empty `output: []` and a
zero-valued `usage` object, all fields `ResponsesResponse` always carries — the sketch elided them as illustrative
shorthand, not as a narrower shape the shipped code is meant to match. The golden,
`contracts/perplexity/perplexity-agent-stream.sse`, is where the real wire bytes are pinned; this block stays what
it always was, a shape sketch, not a byte-for-byte example.

Two more points this sketch's `output_index: 0` and the P5U3 spec's literal `output_index: 0` example left
unstated, both settled by the shipped renderer rather than by this section's prose: **`output_index` is the
message item's actual position in `output[]`, not always 0** — a turn that also projects a `search_results`
item (that item always renders first) gets `output_index: 1` on every one of the four message-item events, and
the golden pins this (`provenance.yaml`: "the message output item's own output_index (1, not 0) is pinned
too"). The `search_results` item itself gets no `output_item.added`/`.done` pair of its own — it is revealed
only inside `response.completed`'s `output[]`, never announced mid-stream — which is simulator-chosen and
recorded as such in the contract's "not simulated" list, not an oversight. **A turn whose `status` is `failed`
or `cancelled` renders no message output item at all** — `renderAgentOutput`'s own rule, shared with the
non-streaming route — so `renderAgentStream` has nothing to attach `output_item.added`/the deltas/
`output_text.done`/`output_item.done` to and degrades to exactly two frames, `response.created` then
`response.completed` (the latter carrying `status: "failed"`/`"cancelled"` and `error`), never `response.failed`
itself (out of scope, "Out of scope" above). This wire shape is now recorded in
`contracts/perplexity/README.md`'s "What Servicesim simulates" bullet, not only in `renderAgentStream`'s own doc
comment — a real, on-the-wire sequence belongs in the contract, house rule 1, even when it is a simulator-chosen
degradation pending a later unit's `response.failed`. The same status split feeds
`scenario.StreamTurn.ChunkCount` (§9's `agentChunkCount`, corrected below): a failed/cancelled turn's true chunk
count is 2, not `len(Deltas)+5`, and an `agentChunkCount` that ignored status overstated it — see the
correction note at the end of this unit's banner.

**Vendor-verified 2026-08-15**: the `ResponseStreamEvent` schema is retrievable from `openapi.json` (an earlier
edition of this document and of the contract said it could not be — a fetch-tooling artefact, not a vendor gap)
and is exactly the 14 members matching the `EventType` enum, discriminated by `type`, with a monotonically
increasing `sequence_number` required on every event. It pins the `response.completed` payload's `response`
field as "the full or partial response object" — not, as an earlier edition said, "the full response object
including usage." It does **not** pin a literal `event: <type>` SSE line — `openapi.json` has 0 occurrences of
`event:` line formatting for this schema — so `SSEEvent.Name`/`EncodeSSE`'s named-frame behaviour for this
grammar is simulator-chosen, not contract-verified. `[DONE]` is **unstated**, not contradicted, for this
grammar: no Agent-API page and no occurrence in `openapi.json` (0 hits) states a `[DONE]` sentinel for
`/v1/agent`; the "ends with `data: [DONE]`" line previously attributed to `gateway-responses-post.md` describes
`POST /router/v1/responses`, Perplexity's separate, unsimulated Router API, and that attribution is withdrawn.
The "`[DONE]` sentinel is a chat-completions concept only" bullet below remains simulator-chosen on that
corrected basis (see A4 in "Resolved 2026-08-15" above), not vendor-pinned either way.

**One mechanism serves both, and landing `GrammarTyped` is what gives the Agent entry a `stream:` key at all.**
`PerplexityAgentProjection` carries no `Stream` field today — the Agent surface warns `CodeAgentStreamUnsupported`
unconditionally (`profiles/perplexity/agent.go`), which is why the preamble can say "the Agent surface has no policy
knob" without contradiction. `GrammarTyped` landing second is what adds `Stream *scenario.StreamScript` to that
projection and retires the unconditional warn in favour of the same `when_requested`/`StreamServe` switch Sonar
already has; until then, `perplexity_agent` entries have no way to opt into streaming at all. The split is the one
the non-streaming path already makes: `provider` owns transport,
`provider/<name>` owns the wire contract. Concretely, everything in §3.2, §4 and §5 — `SSEEvent`, `EncodeSSE`,
`Stream`, `executeStream`, all three fault kinds, `StreamOutcome`, the append-before-first-byte rule, every
determinism property — is grammar-blind. The grammars differ in exactly two places, both inside a provider package:

1. Whether `SSEEvent.Name` is set. `GrammarDelta` leaves it empty and `EncodeSSE` omits the `event:` line;
   `GrammarTyped` sets it to `event: <type>` — unpinned by Perplexity, but the OpenAI Responses streaming
   dialect the Agent API declares compatible with does, recorded as simulator-chosen and correctable from a
   captured live response (§10 step 3; "Resolved 2026-08-15", A4).
2. The payload renderer, and where usage lives inside it.

Two consequences worth stating because they are the places a shared mechanism could have gone wrong:

- The `[DONE]` sentinel is a chat-completions concept only. It is an `SSEEvent` with `Data: []byte("[DONE]")` and is
  emitted by the Sonar renderer, never by the transport. `StreamTerminal.OmitDone` has no effect on `GrammarTyped`,
  and validation says so rather than silently ignoring it.
- `Stream.Usage` is populated by the renderer that knows where usage lives, so the journal's spend fields are
  identical across grammars even though the wire shapes are not. That is what lets one adopter assertion cover both
  surfaces, which is the entire reason for reconciling them now rather than after the migration.

**Shipped as (Phase 5 unit 3):** this paragraph's own present tense ("carries no `Stream` field today", "until then,
`perplexity_agent` entries have no way to opt into streaming at all") described the state before this unit and is
now the state this unit replaced, not a standing fact — `PerplexityAgent` gains `Stream scenario.StreamScript`
exactly as sketched, `CodeAgentStreamUnsupported`'s unconditional warn retires in favour of
`agentStreamPolicy`/`rejectAgentStream` (the Agent-surface twins of `streamPolicy`/`rejectStream`), and
`AgentValidator.ValidateProjections` now calls `scenario.ValidateStreamScripts`/`ValidateStreamFaultMismatch` exactly
as `SonarValidator` does. The two "differs in exactly two places" bullets above hold exactly as written, with one
addition neither anticipated: `GrammarTyped`'s `ValidateStreamFaultMismatch` bound needed a THIRD reconciliation
point — not a difference in `SSEEvent.Name` or the payload renderer, but in how many indexed chunks a turn's script
produces at all. See the top-of-document unit-3 banner's second resolved point and §9's `ChunkCount` note for why
and how. `StreamTerminal.OmitUsage` — unmentioned by either bullet above, which only discuss `OmitDone` and where
`Stream.Usage` is populated from — is honoured on `GrammarTyped` exactly as on `GrammarDelta`: it drops `usage`
from `response.completed`'s `response` object specifically, via `wire.Omit` on the already-rendered bytes rather
than a pointer field on the shared `ResponsesResponse` type (the banner's resolved points explain why).

**Route reconciliation.** The adopter's client calls `/chat/completions` with `stream: true` and
`model: sonar-deep-research`, already an accepted model in `validateSonarRequest`. `GrammarDelta` on
`/chat/completions` and `/v1/sonar` is therefore the path that must land first; `GrammarTyped` on `/v1/agent` and
`/v1/responses` lands second, before their migration rather than before their adoption. Exa's deferred SSE on
`/search` and `/answer` stays deferred: no adopter code calls it, and unlike the `/agent/runs` claim in
`contracts/exa/README.md`, that one is still true. Exa also now serves `/agent/runs` routes for the async-job design
(`profiles/exa/handler.go`); those are a different surface again and are untouched by this document.

**This split is not incidental — it is the route table's own boundary.** `GrammarDelta`'s three route spellings are
exactly `NameSonar`'s (`"perplexity"`) `SonarRoutes()`, and `GrammarTyped`'s three route spellings are exactly
`NameAgent`'s (`"perplexity_agent"`) `AgentRoutes()`. `Route.Entry`, a Phase 1/3 change, is what makes Sonar and the
Agent API
independent scenario entries with independent validators (`SonarValidator`, `AgentValidator`) and independent turn
cursors rather than two route groups sharing one — see [§9](#9-validation-findings-this-adds) for why that is what
makes the grammar computable at load time.

---

## 8. Schema versioning

**Additive to version 1. Not version 2.** `extended-surfaces.md` proposed the bump; that proposal is withdrawn here,
for four reasons, the second of which is decisive.

**1. A bump used to break every existing fixture in every consuming repository — that specific harm is now fixed, and
this reason is correspondingly weaker.** When this section was written the gate was strict equality, so a `version: 1`
file loaded by a `version: 2` build failed outright. **Phase 1 of the adopter plan widened it to a range**
(`1 <= v <= SchemaVersion`), so a v1 file now loads on a v2 build and a bump no longer strands anybody's fixtures.

The honest consequence: this reason no longer *forbids* a bump, it only makes one unnecessary. `extended-surfaces.md`'s
argument for landing the turn model early — that a schema break "is an N-repository event, not a one-repository
event" — still applies to any change that reinterprets a key, because widening the gate fixes *loading*, not
*meaning*. A v1 file that loads on a v2 build and then means something different is a worse failure than one that
refuses to load, and no version gate can catch it. Reason 2 below is the decisive one and is untouched by this.

**2. The premise is obsolete.** The note says streaming "means adding an event-sequence projection, and that is a
scenario-schema version bump". That was written when `Providers` was a closed struct in `scenario` and every
projection field lived there. The open-registry change moved projection bodies out: `scenario`'s `Turn` keeps
`respond:` as an opaque `yaml.Node` and the provider package decodes it. `stream:` is a projection field. The schema
**envelope** — `version`, `sources`, `providers`, `turns`, `when`, `turn_key`, `fault` — gains nothing but three
fault-kind constants and one optional `after_chunk`. There is no event-sequence projection in `scenario` to version.

**3. Nothing existing changes meaning.** Every added key is optional and absent from every shipped fixture. The one
key that changes *shape* — `stream:` from scalar to mapping — is widened, not replaced: the scalar form still decodes,
via the same scalar-or-mapping unmarshaler `SourceRef`, `ExaResult` and `PerplexityResult` already use in
`scenario/model.go`. A schema version exists to signal "your file means something different now", and no file does.

**4. Feature-level capability signals beat a file-level integer, and the repository already has them.** A v1 build
meeting `kind: stream_disconnect` today produces `scenario.fault.kind.unknown` naming the kind, raised by
`scenario`'s validator. `stream:` is the same story, and this build already demonstrates it: a scalar outside
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
`fault.after: success` means. Streaming makes none of those.

The prerequisite that used to accompany this test — "when that day comes, widen the gate to a range in the same
change" — **shipped in Phase 1 and is no longer a condition on anyone**. A v2 build already loads v1 files. What
remains is the harder half: a reinterpreting change must still be announced some other way, because a file that
loads and then means something different cannot be caught by a version gate at all.

---

## 9. Validation findings this adds

All are load-time unless marked, so a bad streaming fixture fails at readiness rather than on a consumer's first call.

| Code | Severity | Raised when |
|---|---|---|
| `scenario.stream.policy.unknown` | error | `when_requested` is not `warn`, `reject` or `stream` |
| `scenario.stream.policy.ignored` | warning | `when_requested` declared on a turn after the first — the policy is per entry, so a later one is never read (this is the shipped `perplexity.stream.policy.ignored`, generalised) |
| `scenario.stream.deltas_empty` | error | the entry's policy is `stream` and **some turn** declares no `deltas` — that turn would serve an empty stream |
| `scenario.stream.deltas_ignored` | error | a turn declares `deltas:` while the entry's policy is **not** `stream` — the script is dead and would serve JSON silently |
| `scenario.stream.answer_mismatch` | warning | concatenated `deltas` do not equal the projection's `answer` |
| `scenario.fault.after_chunk.not_streaming` | error | `after_chunk` set on a kind that is not `stream_*` |
| `scenario.fault.delay_after_headers.streaming` | error | `delay_after_headers` set on a `stream_*` kind, regardless of the entry's policy (Phase 6 unit 5) |
| `scenario.fault.stream_mismatch` | error | a `stream_*` kind on an **entry whose policy is not `stream`**; `truncate_body` (or, since Phase 6 unit 3, `oversized_body`) on an **entry whose policy is `stream`**; or, since Phase 6 unit 5, `delay_after_headers` on an **entry whose policy is `stream`**, on a kind that would not otherwise suppress the stream |
| `scenario.fault.after_chunk.out_of_range` | error | `after_chunk` is **not less than** the smallest chunk count any of the entry's turns will produce |
| `scenario.stream.abort_unreachable` | error (per request) | a claimed attempt cannot apply to this exchange's actual transport — a `stream_*` kind claimed by a request that will not stream (§4.2; the entry's policy is `stream` but this particular request did not ask for one); the load-time `stream_mismatch` case reached at request time by a hand-built entry that skipped validation; or, since Phase 6 unit 5, an attempt carrying `delay_after_headers` claimed by a request that will stream, on a kind that would not otherwise suppress it |
| `perplexity.stream_mode.concise.unscripted` | warning (per request) | a request carries `stream_mode: concise` AND will actually stream (`resp.Stream != nil` — i.e. `stream: true`, on an entry whose policy is `stream`); unit 1 renders only the full-mode sequence, so the full-mode transcript is served anyway. A `stream_mode: concise` request that does not itself set `stream: true` never reaches this — nothing streams for it to diverge from (§7, A2) |
| `perplexity.stream.done_ignored` | warning | `terminal.omit_done` declared on the typed grammar, which has no sentinel |

**Shipped as (Phase 5 unit 1):** the first five rows and `scenario.stream.abort_unreachable` are live —
`scenario.stream.policy.unknown`/`.ignored`/`.deltas_empty`/`.deltas_ignored`/`.answer_mismatch` from
`scenario.ValidateStreamScripts`, `scenario.stream.abort_unreachable` from `Handle`'s mismatch branch — and
so is `perplexity.stream_mode.concise.unscripted`. `scenario.stream.abort_unreachable` is reachable in
only the **mirror** direction this section names explicitly (`truncate_body` claimed against an exchange
that WILL stream): the other direction (a `stream_*` kind claimed against an exchange that will not) needs
a `stream_*` fault kind to exist at all, and none does yet — see the `scenario.fault.stream_mismatch` note
right below. `scenario.fault.stream_mismatch` is live in only the
direction unit 1 can reach: `truncate_body` on a streaming entry
(`scenario.ValidateStreamFaultMismatch`); the mirror direction (a `stream_*` kind on a non-streaming entry)
cannot be authored yet, because `FaultStreamDisconnect`/`FaultStreamTruncateChunk`/`FaultStreamStall` are
not declared in `scenario.FaultKind` until the unit that adds them — writing one of those kind names today
still fails with the pre-existing `scenario.fault.kind.unknown`, which is a correct (if generic) rejection
in the meantime. `scenario.fault.after_chunk.not_streaming`, `scenario.fault.after_chunk.out_of_range` and
`perplexity.stream.done_ignored` do not exist yet: all three are scoped to `after_chunk`/`GrammarTyped`,
neither of which unit 1 adds.

**Shipped as (Phase 5 unit 2):** `scenario.fault.after_chunk.not_streaming` and
`scenario.fault.after_chunk.out_of_range` are both live now that the three `stream_*` kinds and `AfterChunk`
exist. `not_streaming` is checked in `scenario.Validate` itself (`validateFaultAttempt`), not in
`ValidateStreamFaultMismatch`: it needs only the attempt in isolation — never the entry's streaming policy or
any turn's chunk count — so it lives with the rest of the envelope-level fault checks rather than the
provider-decoded streaming ones. Like `AfterChunk` itself, it treats a zero value as "not declared" (this
package's existing convention for several `FaultAttempt` fields), which is a documented, deliberate limitation:
an author who writes `after_chunk: 0` by mistake on a non-streaming kind is not caught, since 0 is
indistinguishable from absent. `scenario.fault.stream_mismatch`'s mirror direction (a `stream_*` kind on a
non-streaming entry) is live too — `ValidateStreamFaultMismatch` gained a `turns []StreamTurn` parameter (the
same slice `ValidateStreamScripts` already takes) so it can compute the minimum chunk-count bound
`out_of_range` needs from one pass over the same per-turn state, rather than a second exported function
re-walking the entry. `perplexity.stream.done_ignored` remains unshipped: it is `GrammarTyped`-scoped (unit 3),
untouched by this unit.

**Shipped as (Phase 5 unit 3):** `perplexity.stream.done_ignored` (`CodeStreamDoneIgnored`,
`profiles/perplexity/agent.go`) is live: `AgentValidator.ValidateProjections` raises it, addressed at
`.stream.terminal.omit_done`, whenever a turn's decoded `Stream.Terminal.OmitDone` is true — unconditionally on
whether the entry's effective policy is `stream`, because the key is meaningless for this GRAMMAR, not merely
inert for this particular turn's transport. `perplexity.agent.stream.unsupported`'s rename (this section's table
above, and the "one provider code is misnamed" paragraph below) is also live now: `CodeAgentStreamUnsupported`'s
string value is `perplexity.stream.agent_unsupported`; the Go identifier is unchanged, so no source reference in
this repository needed updating, only the wire value a consumer's journal assertion matches against. It now fires
under exactly the same two-severity pattern `CodeStreamUnimplemented` already uses on Sonar — a warning under
`warn`, an error (folded into the surface's 422) under `reject` — rather than unconditionally.

**`scenario.fault.stream_mismatch` and `scenario.fault.after_chunk.out_of_range` are load-time checks only where a
provider's own `ValidateProjections` calls `ValidateStreamFaultMismatch` — today, only Perplexity's
`SonarValidator` does.** Exa's and Tavily's own validators do not, because their `Stream` field is still
`scenario.StreamPolicy`, never `scenario.StreamScript` (Exa/Tavily streaming is unit 4 / never, per the SHIPPED
banner). A `stream_*` kind claimed against an Exa or Tavily entry therefore loads clean today; it is caught
one level later, at request time, by `scenario.stream.abort_unreachable` (§4.2) the first time a request actually
reaches that entry — every request does, since neither provider ever sets `resp.Stream`. This is the same
"fail at readiness" property the load-time table above states, one exchange later than the table implies for a
provider this design has not wired yet.

### `stream_mismatch` keys on the effective policy, not key presence

This is the correction that matters most for compatibility, and an earlier draft got it wrong in a way that would
have surfaced in consumer repositories on upgrade day.

That draft raised the error on the **presence** of a `stream:` key. But `stream:` is not a new key — Exa's shipped
projection already carries a `Stream scenario.StreamPolicy` field on every turn that uses it (`profiles/exa/render.go`),
and so does `PerplexityProjection.Stream` (`profiles/perplexity/render.go`). Under presence-keying, this
**already-valid v1 fixture** stops loading:

```yaml
providers:
  exa:
    stream: warn                 # shipped since v0.1.0 — journal a warning, serve ordinary JSON
    fault:
      attempts:
        - kind: truncate_body    # perfectly valid: the body IS ordinary JSON
          truncate_after_bytes: 40
```

*Shipped as (Phase 5 unit 1, verified against `scenario.FaultAttempt`): the YAML key is
`truncate_after_bytes`, not `bytes` — this block's earlier draft named a key the schema does not have. The
shipped regression test is `profiles/exa/stream_regression_test.go`'s
`TestStreamWarnWithTruncateBodyStillLoads`, run through Exa's own unmodified `provider.Validator`.*

Nothing about that scenario streams. `warn` and `reject` both produce an ordinary JSON body — that is their entire
definition — so truncating its bytes is exactly as meaningful as it was before this design existed. Rejecting it
would break §8's central promise that nothing existing changes meaning, in every adopting repository at once, for a
fixture whose author did nothing wrong.

**The distinction is declared-policy versus produced-outcome**, and the policy is the ENTRY's — read once from turn
0, as shipped code already does — so one row describes every turn on that route:

| Entry policy | Produces | `stream_*` kinds | `truncate_body` |
|---|---|---|---|
| absent (default `warn`) | JSON body | error — nothing to cut | **valid** |
| `warn` | JSON body | error — nothing to cut | **valid** |
| `reject` | provider-shaped 4xx | error — nothing to cut | **valid** |
| `stream` | SSE transcript | **valid** | error — see below |

`truncate_body` is rejected only under `stream`, and for a real reason rather than tidiness: `truncateBody` in
`provider/fault_exec.go` sets the full `Content-Length` before writing the prefix, which is correct for JSON and
invalid for SSE, and a byte-offset cut lands mid-frame and produces a half-written `data:` line. That tests the
consumer's *parser* rather than their *reconnect* logic. The SSE-aware equivalent is `stream_truncate_chunk` with
`after_chunk`, which counts frames rather than bytes.

**Required regression fixture.** The scenario above ships as a loadable test case, asserting it still loads with no
findings. The compatibility claim in §8 is otherwise only an intention, and the failure it guards against is
invisible in this repository — it appears in someone else's test suite, after they upgrade.

`scenario.stream.policy.unknown` and `scenario.stream.policy.ignored` **retire** the per-provider codes this build
raises today for the same conditions — Perplexity's `CodeStreamPolicyUnknown` / `CodeStreamPolicyIgnored` and Exa's
own `exa.stream.policy.unknown` — because `StreamScript`'s `UnmarshalYAML` decodes and validates the policy once, in
`scenario`, for both providers; a second, provider-local validation of the same enum is exactly the "second copy of
this grammar is a second chance to disagree" the preamble already argues against for pacing. Retiring them is part
of landing this design, not a separate cleanup, on the same terms as the finding-code rename below. On Exa, `ignored`
is new behaviour rather than a rename: today's `exa/render.go` validates only unknown values on a later turn, not a
value declared there at all, so a scenario that previously loaded silent now gets a warning it did not get before —
worth calling out because it is the one place in this design where an existing valid fixture starts producing a
finding it did not produce before, even though nothing about its *response* changes.

**Shipped as (Phase 5 unit 1): the Exa half of this paragraph has not landed. Exa is untouched.** The P5U1
unit scope is `scenario`, `provider`, `internal/journal` and `profiles/perplexity` only —
`profiles/exa` is explicitly out of scope ("Exa/Tavily streaming: unit 4 / never"). Concretely: Exa's own
`codeStreamPolicy` (`exa.stream.policy.unknown`, `profiles/exa/render.go`) is **not** retired yet and keeps
firing exactly as it does today; Exa's `Stream` field stays `scenario.StreamPolicy`, never becomes
`scenario.StreamScript`, and therefore cannot decode the mapping form at all — `stream: {when_requested:
...}` under `providers.exa` is a decode error on Exa, not a `scenario.stream.policy.*` finding; and Exa
raises no new `ignored` warning for a policy declared on a later turn, because its validator was not
touched. `scenario.StreamPolicy` gaining the `StreamServe` value is what keeps this compatible rather than
broken: Exa's own unmodified switch over `p.Stream` (`case "", StreamWarn, StreamReject: ...; default:
codeStreamPolicy`) treats `stream: stream` as the same "unknown" case it always has, which is correct
today — Exa cannot serve one. The **only** part of this paragraph that shipped in unit 1 is Perplexity's
half: `CodeStreamPolicyUnknown`/`CodeStreamPolicyIgnored` (`perplexity.stream.policy.*`) are retired, in
favour of `scenario.CodeStreamPolicyUnknown`/`scenario.CodeStreamPolicyIgnored`
(`scenario.stream.policy.*`), exactly as this section says — see `scenario/stream.go`'s
`ValidateStreamScripts` and its caller, `SonarValidator.ValidateProjections`
(`profiles/perplexity/handler.go`). The mechanism differs from this section's own claim in one respect:
validation does not happen inside `StreamScript.UnmarshalYAML` (a decode-time rejection there could only
surface as a generic `perplexity.projection.invalid` addressed at the whole projection body, not at
`.stream.when_requested` specifically) — `UnmarshalYAML` decodes permissively and
`scenario.ValidateStreamScripts` is what raises the specific codes, called once per entry from each
provider's own `ValidateProjections`. `profiles/exa/stream_regression_test.go`'s
`TestStreamWarnWithTruncateBodyStillLoads` proves the compatibility half of this claim end-to-end, through
Exa's real, unmodified validator.

**One provider code is misnamed and should be corrected in the same pass.** Every Perplexity stream finding is
`perplexity.stream.*` except one:

| Shipped code | Raised by | Status after this design |
|---|---|---|
| `perplexity.stream.unimplemented` | `validateSonarRequest` (`CodeStreamUnimplemented`) | unchanged |
| `perplexity.stream.policy.unknown` | `streamPolicy`'s caller (`CodeStreamPolicyUnknown`) | retired — superseded by `scenario.stream.policy.unknown` |
| `perplexity.stream.policy.ignored` | same (`CodeStreamPolicyIgnored`) | retired — superseded by `scenario.stream.policy.ignored` |
| `perplexity.agent.stream.unsupported` | `handleAgent`'s field validation (`CodeAgentStreamUnsupported`) | renamed to `perplexity.stream.agent_unsupported` |
| `exa.stream.policy.unknown` | Exa's `render.go` | retired — superseded by `scenario.stream.policy.unknown` |

The odd one out splits on the surface (`agent`) before the subject (`stream`), so a consumer filtering its journal
for `perplexity.stream.` misses exactly the finding that says their Agent request could not stream. Exa's
`exa.stream.policy.unknown` — itself retired above — was the pattern to mirror: subject first, qualifier after; the
renamed code keeps that ordering even after the code it was mirroring is gone.

This is a **breaking change to a finding code** and must land with the rest of this design rather than on its own —
a consumer asserting on the old spelling gets no deprecation window from a rename, and there is no reason to spend
two of those on one surface. It is worth doing at all only because every streaming code is being revisited here
anyway; renaming it in isolation would be churn.

`scenario.fault.after_chunk.out_of_range` is a load-time check because the chunk count is computable there: the
grammar is fixed by the provider entry. `Route.Entry` (Phase 1/3) is why that is true without qualification —
Sonar's `GrammarDelta` routes are exactly `NameSonar`'s (`"perplexity"`), Agent's `GrammarTyped` routes are exactly
`NameAgent`'s (`"perplexity_agent"`), and the two are independent entries with independent `ValidateProjections`
methods (`SonarValidator`, `AgentValidator`). No turn under one entry can answer a request routed to the other, so
"the grammar is fixed by the provider entry" is not an approximation to be caught by a load-time coherence check —
it is the route table.

**The frame count, resolved 2026-08-15 (§7, A3) after two sections of this document gave two different counts for
the same sequence.** For a turn with N scripted deltas, `GrammarDelta`'s full-mode sequence is **N chunks + 1
terminal chunk = N + 1 chunks** (`chunk_count`, §5.1 above) — there is no longer a separate role-only opening chunk;
role and content ride together on every one of the N chunks (§7, A3) — followed by `+ terminal frames`, which is
grammar-dependent and not itself a chunk: `GrammarDelta` writes `data: [DONE]` unless `terminal.omit_done` is
set, in which case none; `GrammarTyped` never writes one, because `[DONE]` is a chat-completions concept only and
`OmitDone` has no effect there (§7). Combining `finish_reason` and `usage` on the terminal chunk rather than two
separate chunks is a frame-level choice §10 records as simulator-chosen for `full` mode specifically, not
contract-verified — the count above is only as pinned as that choice is.

**It is checked against the smallest count across the entry's turns, and that follows from the plan being per
route.** `TurnFault` supplies one plan for the whole route, so a single `after_chunk: 4` may fire on whichever turn
answers that call — a turn with three deltas, or one with nine. Validating against the declaring turn alone would
pass a fixture that aborts past the end of a shorter sibling, and the symptom would be a stream that completed
normally where the author scripted a disconnect: a fault that silently does nothing, which is the worst outcome for
a test written to prove reconnect logic.

The minimum is the only bound that is correct for every turn the plan can reach. It is conservative — it rejects an
`after_chunk` that would have been fine for the turn actually answering — and that is the right direction, because
the alternative is a scenario whose meaning depends on which turn a fault happens to land on.

**The valid range is `0 <= after_chunk < chunk_count`, not `after_chunk <= chunk_count`.** "Exceeds" is ambiguous
between those; the check must be `>=`, not `>`. `executeStream`'s loop fires the abort at `plan.AbortAt == i` for `i`
in `[0, len(plan.Chunks))`; an `after_chunk` equal to the chunk count matches no index, so `executeStream` returns
`StreamCompleted` for a stream the author scripted a disconnect into — the exact silent-no-op failure this whole
validation exists to prevent, reached through an off-by-one instead of a missing check. The same bound applies to
`stream_stall`: a stall "before chunk N" with `N == chunk_count` has no chunk to precede and never fires either: a
stall wanting to pause after every scripted chunk uses `StreamTerminal.Pace`, not `after_chunk` pointed past the end.
The upper end of the valid range (`after_chunk == chunk_count - 1`) is not a special case to reject — aborting on
the final indexed chunk (the terminal chunk itself, carrying `finish_reason` and `usage`; `[DONE]` is a sentinel
outside the indexed range, per A3, and is never a valid `after_chunk` target) is a legitimate, useful script:
every scripted delta arrived, but the response that confirms completion — and the `[DONE]` sentinel after it —
never does, which is its own edge a consumer's client should survive.

### What an entry-level policy makes impossible, and what it does not

With the policy per entry, a `stream_*` fault can no longer land on a non-streaming *turn*: either the entry's policy
is `stream` and every turn streams, or it is not and none do. The per-route plan and the per-entry policy are
addressed at the *same* granularity, so the turn-level mismatch the earlier per-turn model allowed is not
representable, and a load-time coherence check across turns is unnecessary — this makes writing the incoherent
fixture impossible rather than merely catching it.

**It does not make every mismatch load-time detectable, and an earlier version of this section overstated that it
did.** The policy answers "does this surface serve a stream when asked", not "does this call stream" — the preamble
is explicit that a consumer sends `stream: true` on one call and `stream: false` on the next in the same lane, by
design. A `stream_*` attempt claimed by a call that did not ask to stream is therefore still reachable under a
fully valid, load-time-clean `stream`-policy entry: it depends on what a specific request's body says, which does
not exist until the request arrives. §4.2 is where that case is handled — reported through
`scenario.stream.abort_unreachable`, never silently served as a plain 200 — because it is the one shape this design
cannot rule out by construction, only by reporting it every time it happens.

---

## 10. Contract fidelity

**This is a Phase 5 prerequisite, in this order, before unit 1 — not background reading.** The rest of this document
depends on one frame-level choice this section pins: §2, §7 and §9's frame-count formula all assume `finish_reason`
and `usage` ride on the same terminal chunk on `GrammarDelta` — pinned specifically for **`full`** mode, the only
mode unit 1 renders, by "Resolved 2026-08-15" (§7, A3), since the vendor confirms this placement for `concise` mode
only. Everything downstream of that choice — the frame count, `after_chunk`'s valid range, every SSE golden — is
only as verified as this section makes it, and the choice itself remains simulator-chosen, not contract-verified,
until corrected from a captured live response (step 3 below).

1. ~~**Regenerate the Perplexity SSE section from `https://docs.perplexity.ai/openapi.json`.**~~ **DONE
   2026-08-15, corrected the same day.** Recorded in `contracts/perplexity/README.md`'s "Streaming (SSE)"
   section: the frame envelope, the `stream_mode` grammars, which frame carries `usage`/`cost`/`search_results`,
   and an explicit "What is NOT stated by the vendor" table. Two pages fetched for that section
   (`gateway-chat-completions-post.md`, `gateway-responses-post.md`) turned out to document Perplexity's
   separate, unsimulated Router API rather than the Sonar/Agent SDK aliases they were assumed to be, and every
   claim sourced from them has been withdrawn or re-attributed. The `ResponseStreamEvent` schema, initially
   recorded as unretrievable, was confirmed present in `openapi.json` and is now recorded directly from it. §7
   above records the resolution that section's findings forced.
2. ~~Read the adopter's `src/pkg/agent/perplexity.go` as evidence of the real wire shape.~~ **STRUCK 2026-08-15
   — VOID under the authority rule:** vendor documentation decides every wire contract; the adopter's client
   code is not evidence, however convenient an authority it is "already in hand." Reading it here would let a
   consumer's guess about a vendor's behaviour outrank the vendor's own (even incomplete) documentation, which
   is the inversion this design's own contract-fidelity process exists to prevent.
3. **Record every frame-level choice the OpenAPI document does not pin as `simulator-chosen` in
   `contracts/perplexity/provenance.yaml`**, exactly as the Sonar non-422 error bodies already are, and correct each
   one from a captured live response before it is called verified. That is the same discipline ADR-0002 applies, and
   the adopter's own backlog asks for it under contract-fidelity process.

The two grammars in §7 were reconstructed from the vendor's OpenAPI document and from the OpenAI-compatible dialect
it mirrors. Step 1 above has now run, and — corrected 2026-08-15 — against `openapi.json` directly for the
Responses/Agent surface's `ResponseStreamEvent` schema: recorded as unretrievable in an earlier edition of both
this document and the contract, that was a fetch-tooling artefact, and the schema is now recorded in full (14
members, matching the `EventType` enum). The chat-completions surface still has no schema in `openapi.json`
(confirmed absent by full-text search), so that half remains prose-only, and the result overall is **partial, not
complete, verification**. Newly verified: the chat-completions surface's `stream_mode` split into `full`/`concise`;
that `usage`/`search_results`/`images` ride on **both** `chat.reasoning.done` and `chat.completion.done` in
**`concise`** mode, while `cost` rides on `chat.completion.done` only; that `[DONE]` is pinned for `concise` mode by
a raw code sample, with `full` mode resting only on the OpenAI-compatibility declaration; and the
`ResponseStreamEvent` schema's full `oneOf` and per-event fields. **Corrected**, not newly contradicted: claims this
document and the contract both sourced from `gateway-chat-completions-post.md` and `gateway-responses-post.md` —
including "`[DONE]` being a chat-completions concept only" being contradicted — turned out to cite Perplexity's
separate, unsimulated Router API, not the Sonar/Agent aliases those pages were assumed to be. With that
misattribution corrected, `[DONE]` on the Responses/Agent surface is **unstated** (0 occurrences in
`openapi.json`), not contradicted. Still **not** verified by any fetched page: `usage`'s placement in `full` mode
specifically, so this document's own choice that `usage` rides on the same chunk as `finish_reason` remains
simulator-chosen and provisional for that mode; and whether `GrammarTyped` frames carry a literal `event: <type>`
SSE line at all, as opposed to an anonymous `data:`-only frame whose payload names its own type — `openapi.json`
has 0 occurrences of `event:` line formatting for `ResponseStreamEvent`. See `contracts/perplexity/README.md`'s
"What is NOT stated by the vendor" table for the complete list, and §7 above ("Resolved 2026-08-15") for the
decision each of these forced before unit 1.

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
  The existing `closeBeforeHeaders`'s no-`Hijacker` fallback already documents the same limit.
- **It does not survive multiple replicas.** Stream state is per-request and holds nothing across requests, but the
  fault attempt counters a `transient-blip-then-retry` scenario depends on are per-process in-memory, so the retry
  reaching a second replica draws attempt 0 again and is disconnected forever. That is the same undocumented
  multi-replica divergence the adopter already flagged, reached from a new direction, and it makes documenting the
  single-replica exemption a prerequisite for this feature rather than a parallel task.
