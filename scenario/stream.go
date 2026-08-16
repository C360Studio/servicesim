package scenario

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// StreamScript is the provider-neutral streaming projection: what to say, in
// what order. Frame assembly — each chunk's role+content delta and running
// aggregate message, search_results repeated on every chunk, the one terminal
// chunk carrying finish_reason, the full message, usage and search_results
// together, the [DONE] sentinel — is the vendor's contract and belongs to the
// provider package, which is the same split the non-streaming path already
// makes between a projection and a renderer. This is the full-mode
// (provider.GrammarDelta) sequence, the only one this build renders; concise
// mode is deferred (docs/design/streaming.md §7).
//
// It lives in scenario rather than in a provider package because every
// provider that streams shares this grammar, and a second copy of it per
// provider is a second chance for two of them to spell it differently.
type StreamScript struct {
	// Policy is the YAML key "when_requested". Empty means StreamServe when
	// Deltas is non-empty and StreamWarn otherwise: writing a script and
	// forgetting the switch must not silently serve JSON. See
	// [StreamScript.EffectivePolicy].
	//
	// Only the FIRST turn's Policy is read — it is a property of the
	// provider entry, not of the call position, because rejection has to be
	// decided before turn selection claims an attempt. A Policy on any later
	// turn is reported ([ValidateStreamScripts]) rather than being dropped
	// in silence. Deltas below are the opposite: genuinely per turn, since
	// they are the answer.
	Policy StreamPolicy `yaml:"when_requested,omitempty"`

	// Pace is the default minimum gap before every chunk this turn writes —
	// every scripted delta, the terminal chunk (unless StreamTerminal.Pace
	// overrides it) and, on GrammarDelta, the [DONE] sentinel, which has no
	// per-frame override of its own since it is never an indexed chunk. Zero
	// writes the whole sequence as fast as the socket accepts it.
	//
	// Landing here, rather than staying a unit-1 seam with no scenario key,
	// is what docs/design/streaming.md's unit-1 "Shipped as" note already
	// named as unit 2's job: pacing is otherwise inert, since stream_stall
	// (also unit 2) is the only OTHER thing that ever produces a nonzero
	// gap, and the two are designed together.
	Pace Duration `yaml:"pace,omitempty"`

	// Deltas are the incremental content fragments, in order. Concatenated
	// their text should equal the projection's non-streaming answer;
	// [ValidateStreamScripts] warns when they do not, because a consumer
	// that reassembles the stream and compares it against a non-streaming
	// golden would otherwise fail for a fixture reason rather than a code
	// one.
	Deltas []StreamDelta `yaml:"deltas,omitempty"`

	// Terminal tunes the closing frames. Nil means the vendor-faithful
	// default.
	Terminal *StreamTerminal `yaml:"terminal,omitempty"`
}

// StreamDelta is one content fragment and the gap that precedes it.
type StreamDelta struct {
	Text string `yaml:"text"`

	// Pace overrides StreamScript.Pace for this one chunk. Zero means no
	// override — matching this package's existing "zero is default, not an
	// explicit choice" convention for Duration fields — so a delta cannot
	// currently ask for a literal zero gap when its script default is
	// nonzero; nothing in this unit's scope needs that distinction.
	Pace Duration `yaml:"pace,omitempty"`
}

// rawStreamDelta is the decode target for the mapping form of a StreamDelta.
type rawStreamDelta struct {
	Text string   `yaml:"text"`
	Pace Duration `yaml:"pace,omitempty"`
}

// UnmarshalYAML accepts a bare scalar as the shorthand for {text: <scalar>},
// so a script that never overrides pacing keeps writing a plain list of
// strings, and only a delta that needs its own gap pays for the mapping form.
func (d *StreamDelta) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Kind == 0 {
		return nil
	}
	if value.Kind == yaml.ScalarNode {
		var text string
		if err := value.Decode(&text); err != nil {
			return fmt.Errorf("line %d: expected delta text: %w", value.Line, err)
		}
		d.Text = text
		return nil
	}
	var raw rawStreamDelta
	if err := DecodeStrict(value, &raw); err != nil {
		return err
	}
	d.Text, d.Pace = raw.Text, raw.Pace
	return nil
}

// StreamTerminal scripts the closing frames. Every field exists to express a
// vendor-drift shape a consumer must survive, and each is a scenario knob
// rather than a fault because the stream still closes cleanly: a missing
// usage object is a well-formed response with a hole in it, not a transport
// failure.
type StreamTerminal struct {
	// OmitUsage drops the usage object from the terminal chunk. It is the
	// streaming half of the adopter's usage/cost edge pack.
	OmitUsage bool `yaml:"omit_usage,omitempty"`

	// OmitDone drops the "data: [DONE]" sentinel on the chat-completions
	// grammar while still closing the connection cleanly. A consumer that
	// waits for the sentinel hangs until its own deadline; a consumer that
	// waits for EOF does not. That difference is worth being able to
	// script, and it is NOT the same as a mid-stream disconnect, which
	// produces an unexpected EOF instead of a clean close.
	OmitDone bool `yaml:"omit_done,omitempty"`

	// Pace overrides StreamScript.Pace for the terminal chunk specifically.
	// Zero means no override, the same convention StreamDelta.Pace uses.
	Pace Duration `yaml:"pace,omitempty"`
}

// rawStreamScript is the decode target for the mapping form of a StreamScript.
// It restates the fields rather than being `type rawStreamScript
// StreamScript`, for the same reason PerplexityResult's decode target does:
// a defined-type copy keeps embedded/promoted methods, and StreamScript has
// none to worry about here, but restating keeps every UnmarshalYAML in this
// package following one pattern rather than two.
type rawStreamScript struct {
	Policy   StreamPolicy    `yaml:"when_requested,omitempty"`
	Pace     Duration        `yaml:"pace,omitempty"`
	Deltas   []StreamDelta   `yaml:"deltas,omitempty"`
	Terminal *StreamTerminal `yaml:"terminal,omitempty"`
}

// UnmarshalYAML accepts the scalar shorthand, so `stream: warn` — the form
// the scalar-or-mapping pattern already supports for [StreamPolicy] — keeps
// parsing as {when_requested: warn}. The mapping branch decodes strictly,
// via [DecodeStrict].
//
// An unrecognised scalar or `when_requested` value is stored as-is rather
// than rejected here: rejecting inside a custom UnmarshalYAML surfaces as an
// opaque decode failure addressed at the whole projection body, not at
// `.stream.when_requested` specifically. [ValidateStreamScripts] is where
// the enum is actually checked, which is what lets it raise
// [CodeStreamPolicyUnknown] at the right path instead.
func (s *StreamScript) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Kind == 0 {
		return nil
	}
	if value.Kind == yaml.ScalarNode {
		if value.Tag == "!!null" {
			return nil
		}
		var p string
		if err := value.Decode(&p); err != nil {
			return fmt.Errorf("line %d: expected a stream policy: %w", value.Line, err)
		}
		s.Policy = StreamPolicy(p)
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: expected a stream policy or a mapping, got a %s",
			value.Line, nodeKindName(value.Kind))
	}
	var raw rawStreamScript
	if err := DecodeStrict(value, &raw); err != nil {
		return err
	}
	s.Policy, s.Pace, s.Deltas, s.Terminal = raw.Policy, raw.Pace, raw.Deltas, raw.Terminal
	return nil
}

// EffectivePolicy applies the "deltas imply stream" default. Nil-safe: a
// projection that never set a Stream field at all behaves exactly as it did
// before StreamScript existed.
func (s *StreamScript) EffectivePolicy() StreamPolicy {
	if s == nil {
		return StreamWarn
	}
	if s.Policy != "" {
		return s.Policy
	}
	if len(s.Deltas) > 0 {
		return StreamServe
	}
	return StreamWarn
}

// Streaming finding codes. These are declared here, not per provider package,
// because [StreamScript]'s decode and the entry/turn split it enforces are
// generic across every provider that streams — a second copy per provider is
// a second chance for two of them to disagree about what the check means.
// [ValidateStreamScripts] and [ValidateStreamFaultMismatch] raise them; a
// provider package's own Validator calls those and folds the result into its
// own findings.
const (
	// CodeStreamPolicyUnknown is raised when a turn-0 `when_requested` value
	// is not warn, reject or stream. It is an error, not a warning: the
	// silent fallback is warn, the very permissive behaviour an author
	// writing `reject` or `stream` was trying to switch OFF, so a typo that
	// quietly re-enables it must stop the process rather than change what a
	// consumer's test observes.
	CodeStreamPolicyUnknown = "scenario.stream.policy.unknown"

	// CodeStreamPolicyIgnored is raised when `when_requested` is declared on
	// any turn but the first, where it cannot be honoured — policy is a
	// property of the provider entry, decided before turn selection claims
	// an attempt (see the package doc). It is a warning: the scenario is
	// servable, but the author needs to know the knob they set on this turn
	// does nothing.
	CodeStreamPolicyIgnored = "scenario.stream.policy.ignored"

	// CodeStreamDeltasEmpty is raised when the entry's effective policy is
	// stream and some turn declares no deltas at all — that turn would
	// serve an empty stream, which is never what a scenario author wants
	// silently.
	CodeStreamDeltasEmpty = "scenario.stream.deltas_empty"

	// CodeStreamDeltasIgnored is raised when a turn declares deltas while
	// the entry's effective policy is NOT stream. The script is dead and
	// would otherwise be served as JSON with no hint that the deltas were
	// ever read — the mirror of CodeStreamDeltasEmpty.
	CodeStreamDeltasIgnored = "scenario.stream.deltas_ignored"

	// CodeStreamAnswerMismatch is raised when a turn's concatenated deltas
	// do not equal its non-streaming answer. A consumer that reassembles
	// the stream and diffs it against a non-streaming golden would
	// otherwise fail for a fixture-authoring reason, not a code one.
	CodeStreamAnswerMismatch = "scenario.stream.answer_mismatch"

	// CodeStreamFaultMismatch is raised when a fault attempt cannot apply to
	// the transport its entry actually uses: today, that is truncate_body
	// declared on an entry whose effective policy is stream. truncateBody
	// (provider/fault_exec.go) sets the full Content-Length before writing a
	// prefix, which is correct for JSON and invalid for SSE — a byte-offset
	// cut on a chunked stream lands mid-frame, testing a consumer's JSON
	// parser rather than its SSE reconnect logic. The check keys on the
	// entry's EFFECTIVE POLICY, never on the presence of a `stream:` key:
	// `warn` and `reject` both produce an ordinary JSON body, so
	// truncate_body stays valid with them exactly as it always has.
	CodeStreamFaultMismatch = "scenario.fault.stream_mismatch"

	// CodeStreamAbortUnreachable is raised per request, not at load, when a
	// claimed fault attempt cannot apply to this specific exchange's actual
	// transport — the request-time mirror of CodeStreamFaultMismatch,
	// reachable because an entry's streaming POLICY does not force every
	// REQUEST in its lane to ask to stream (a consumer may legitimately send
	// stream: true on one call and stream: false on the next). The claimed
	// attempt is reported, never silently absorbed into an ordinary 200.
	CodeStreamAbortUnreachable = "scenario.stream.abort_unreachable"

	// CodeAfterChunkNotStreaming is raised when `after_chunk` is declared on
	// a fault attempt whose kind is not one of the three stream_* kinds —
	// after_chunk has no meaning for any other kind. Checked in
	// [Scenario.Validate] itself (validateFaultAttempt), not here: it needs
	// only the attempt in isolation, never the entry's streaming policy or
	// its turns' chunk counts, unlike every other code in this block.
	CodeAfterChunkNotStreaming = "scenario.fault.after_chunk.not_streaming"

	// CodeStreamAfterChunkOutOfRange is raised when a stream_* attempt's
	// after_chunk is not in [0, chunk_count), checked against the SMALLEST
	// chunk_count across the entry's turns — [ValidateStreamFaultMismatch]
	// explains why the minimum is the only bound correct for every turn the
	// fault plan can reach.
	CodeStreamAfterChunkOutOfRange = "scenario.fault.after_chunk.out_of_range"
)

// StreamTurn is one turn's streaming-relevant projection state, gathered by
// the calling provider package so [ValidateStreamScripts] can check a whole
// entry's script coherence without knowing what a provider projection's
// answer field is called or how its Stream field is reached.
type StreamTurn struct {
	// Path addresses this turn for a finding, matching the convention every
	// provider package's own turn validation already uses:
	// "providers.<name>.turns[i].respond".
	Path string

	// Script is the turn's decoded StreamScript, or nil when the turn
	// declares no `stream:` key at all.
	Script *StreamScript

	// Answer is the turn's non-streaming answer text, or "" when it has
	// none. It is compared against Script.Deltas joined, when both are
	// non-empty.
	Answer string

	// ChunkCount overrides chunkCount's default "len(Script.Deltas) + 1"
	// formula for this turn's after_chunk bound. The default fits
	// GrammarDelta, the only grammar this formula was written against: one
	// chunk per delta plus the one terminal chunk. A grammar whose SSE
	// sequence carries more indexed frames than that — GrammarTyped's
	// envelope events (response.created, response.output_item.added,
	// response.output_text.done, response.output_item.done,
	// response.completed) around the N deltas, for instance — sets this to
	// its own true count so [ValidateStreamFaultMismatch]'s after_chunk
	// bound describes the chunks a fault plan can actually reach, not a
	// GrammarDelta-shaped undercount. Zero, the Go zero value, means "use
	// the default formula" — no existing caller has to set it.
	ChunkCount int
}

// ValidateStreamScripts checks the streaming grammar's load-time coherence
// across one provider entry's turns: the policy enum, that only the first
// turn's policy is read, that a streaming entry's every turn has a non-empty
// script and a non-streaming entry's turns have none, and that a scripted
// script's deltas reassemble the turn's own answer.
//
// It is exported so every provider package whose projection carries a
// StreamScript field can share one implementation instead of writing this
// coherence check once per provider, which is precisely how the shipped
// build's warn/reject checks ended up duplicated (and briefly
// out-of-step) between Exa and Perplexity before this design.
func ValidateStreamScripts(turns []StreamTurn) []Finding {
	if len(turns) == 0 {
		return nil
	}

	var findings []Finding
	for i, t := range turns {
		if t.Script == nil || t.Script.Policy == "" {
			continue
		}
		if i > 0 {
			findings = append(findings, Finding{
				Severity: SeverityWarning,
				Code:     CodeStreamPolicyIgnored,
				Path:     t.Path + ".stream.when_requested",
				Message: "the streaming policy is a property of the provider entry and is read from " +
					"the first turn only; this value is ignored",
			})
			// The value is never read, but a typo an author intended for
			// turn 0 and pasted onto a later turn should still surface as
			// an error rather than pass silently under only a warning
			// about it being ignored.
		}
		switch t.Script.Policy {
		case StreamWarn, StreamReject, StreamServe:
		default:
			findings = append(findings, Finding{
				Severity: SeverityError,
				Code:     CodeStreamPolicyUnknown,
				Path:     t.Path + ".stream.when_requested",
				Message: "stream policy " + strconv.Quote(string(t.Script.Policy)) +
					" is not warn, reject or stream",
			})
		}
	}

	entryPolicy := turns[0].Script.EffectivePolicy()
	for _, t := range turns {
		hasDeltas := t.Script != nil && len(t.Script.Deltas) > 0
		switch {
		case entryPolicy == StreamServe && !hasDeltas:
			findings = append(findings, Finding{
				Severity: SeverityError,
				Code:     CodeStreamDeltasEmpty,
				Path:     t.Path + ".stream.deltas",
				Message:  "the provider entry streams but this turn declares no deltas; it would serve an empty stream",
			})
		case entryPolicy != StreamServe && hasDeltas:
			findings = append(findings, Finding{
				Severity: SeverityError,
				Code:     CodeStreamDeltasIgnored,
				Path:     t.Path + ".stream.deltas",
				Message: "this turn declares deltas but the provider entry's effective streaming policy is not " +
					"stream; the script is dead and would never be served",
			})
		case entryPolicy == StreamServe && hasDeltas && t.Answer != "":
			if joined := joinDeltaText(t.Script.Deltas); joined != t.Answer {
				findings = append(findings, Finding{
					Severity: SeverityWarning,
					Code:     CodeStreamAnswerMismatch,
					Path:     t.Path + ".stream.deltas",
					Message: "concatenated deltas do not equal this turn's answer; a consumer that reassembles " +
						"the stream and compares it against the non-streaming body would see a mismatch",
				})
			}
		}
	}
	return findings
}

// joinDeltaText concatenates a script's delta text, ignoring pace, for the
// answer-reassembly comparison [ValidateStreamScripts] makes.
func joinDeltaText(deltas []StreamDelta) string {
	var b strings.Builder
	for _, d := range deltas {
		b.WriteString(d.Text)
	}
	return b.String()
}

// chunkCount is how many indexed chunks a turn's script will produce, for
// ValidateStreamFaultMismatch's after_chunk bound. t.ChunkCount, when set,
// wins outright: it is the calling provider's own true count for a grammar
// whose SSE sequence does not fit the default formula (see its doc
// comment). Otherwise this is deltas plus the one terminal chunk — the
// GrammarDelta shape this formula was written against. A turn with no
// script, or one that declares no deltas, counts as 1 — the terminal chunk
// alone — which is already reported by CodeStreamDeltasEmpty; it is not
// treated as 0 here so a mis-scripted turn cannot make the minimum bound
// below negative or zero in a way that would reject every after_chunk
// outright.
func chunkCount(t StreamTurn) int {
	if t.ChunkCount > 0 {
		return t.ChunkCount
	}
	if t.Script == nil {
		return 1
	}
	return len(t.Script.Deltas) + 1
}

// ValidateStreamFaultMismatch checks a fault plan's coherence against the
// entry's effective streaming transport, across every turn together because
// the plan is per ROUTE (TurnFault) while a turn's script is per TURN:
//
//   - a stream_* kind cannot apply to an entry that will not stream (its
//     transport is an ordinary JSON body, never chunked SSE);
//   - truncate_body — the one fault kind that assumes a JSON body it can set
//     a Content-Length over — cannot apply to an entry that WILL stream;
//   - a stream_* attempt's after_chunk must be < the SMALLEST chunk_count any
//     of the entry's turns will produce. The minimum is the only bound
//     correct for every turn the plan can reach: TurnFault supplies one plan
//     for the whole route, so a single after_chunk may fire on whichever turn
//     answers that call, and validating against the declaring turn alone
//     would pass a fixture that aborts past the end of a shorter sibling —
//     a fault that silently does nothing, the worst outcome for a test
//     written to prove reconnect logic.
//
// entryPolicy is a parameter, not derived here, because only a provider
// package can compute it: it depends on decoding the opaque `respond:` body
// into that provider's own projection type, which scenario cannot do without
// importing it and closing an import cycle. turns supplies the same per-turn
// script state ValidateStreamScripts uses, for the chunk-count bound; e
// supplies each turn's *Fault, which ValidateStreamScripts does not see.
// Both are required, but only turns is walked to compute minChunks — e.Turns
// is walked separately, by its own index, to reach each turn's *Fault. turns
// need not be the same length as e.Turns: a shorter or reordered turns slice
// only weakens the minimum-chunk-count bound it can compute, it does not
// panic or misindex. The caller should still build turns the same way
// ValidateStreamScripts expects, one entry per turn in declaration order, or
// the bound this function reports will not describe the entry it was asked
// about.
func ValidateStreamFaultMismatch(e *ProviderEntry, entryPolicy StreamPolicy, turns []StreamTurn) []Finding {
	if e == nil || len(e.Turns) == 0 {
		return nil
	}

	minChunks := -1
	for _, t := range turns {
		if n := chunkCount(t); minChunks < 0 || n < minChunks {
			minChunks = n
		}
	}

	var findings []Finding
	for i := range e.Turns {
		f := e.Turns[i].Fault
		if f == nil {
			continue
		}
		for j := range f.Attempts {
			a := &f.Attempts[j]
			path := fmt.Sprintf("%s.fault.attempts[%d]", e.turnPath(i), j)
			switch {
			case a.EffectiveKind() == FaultTruncateBody && entryPolicy == StreamServe:
				findings = append(findings, Finding{
					Severity: SeverityError,
					Code:     CodeStreamFaultMismatch,
					Path:     path + ".kind",
					Message: "kind truncate_body cannot apply to a streaming entry: it sets a Content-Length that " +
						"is wrong for a chunked SSE body; the streaming-aware equivalent is stream_truncate_chunk",
				})
			case a.EffectiveKind().IsStream() && entryPolicy != StreamServe:
				findings = append(findings, Finding{
					Severity: SeverityError,
					Code:     CodeStreamFaultMismatch,
					Path:     path + ".kind",
					Message: fmt.Sprintf("kind %q assumes a chunked SSE transport, but this entry's effective "+
						"streaming policy is not stream", a.EffectiveKind()),
				})
			case a.EffectiveKind().IsStream() && entryPolicy == StreamServe:
				if minChunks >= 0 && (a.AfterChunk < 0 || a.AfterChunk >= minChunks) {
					findings = append(findings, Finding{
						Severity: SeverityError,
						Code:     CodeStreamAfterChunkOutOfRange,
						Path:     path + ".after_chunk",
						Message: fmt.Sprintf("after_chunk %d is not in [0, %d): the smallest chunk_count "+
							"any of this entry's turns can produce is %d, and a fault plan is shared by every "+
							"turn the route may answer with", a.AfterChunk, minChunks, minChunks),
					})
				}
			}
		}
	}
	return findings
}
