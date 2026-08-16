package scenario

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// decodeStream is a small helper: decode YAML source into a StreamScript the
// way a provider projection would, through DecodeStrict over a struct that
// embeds one field of this type — which is what exercises the *value*
// receiver UnmarshalYAML the same way a real projection does.
func decodeStream(t *testing.T, src string) (StreamScript, error) {
	t.Helper()
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &node))
	require.Equal(t, yaml.DocumentNode, node.Kind)

	var holder struct {
		Stream StreamScript `yaml:"stream"`
	}
	err := DecodeStrict(node.Content[0], &holder)
	return holder.Stream, err
}

func TestStreamScriptUnmarshalYAML(t *testing.T) {
	t.Parallel()

	t.Run("scalar shorthand sets the policy", func(t *testing.T) {
		t.Parallel()
		for _, policy := range []string{"warn", "reject", "stream"} {
			s, err := decodeStream(t, "stream: "+policy)
			require.NoError(t, err)
			require.Equal(t, StreamPolicy(policy), s.Policy)
			require.Empty(t, s.Deltas)
			require.Nil(t, s.Terminal)
		}
	})

	t.Run("an unrecognised scalar decodes without error", func(t *testing.T) {
		t.Parallel()
		// Rejecting it here would surface as an opaque decode failure on the
		// whole projection body rather than at .stream.when_requested
		// specifically — see UnmarshalYAML's own doc comment.
		// ValidateStreamScripts is where the enum is actually checked.
		s, err := decodeStream(t, "stream: rejct")
		require.NoError(t, err)
		require.Equal(t, StreamPolicy("rejct"), s.Policy)
	})

	t.Run("mapping form decodes deltas and terminal", func(t *testing.T) {
		t.Parallel()
		s, err := decodeStream(t, `
stream:
  when_requested: stream
  deltas: ["Report A ", "finds ", "that X."]
  terminal:
    omit_usage: true
    omit_done: true
`)
		require.NoError(t, err)
		require.Equal(t, StreamServe, s.Policy)
		require.Equal(t, []StreamDelta{{Text: "Report A "}, {Text: "finds "}, {Text: "that X."}}, s.Deltas)
		require.NotNil(t, s.Terminal)
		require.True(t, s.Terminal.OmitUsage)
		require.True(t, s.Terminal.OmitDone)
	})

	t.Run("deltas imply stream when no policy is written", func(t *testing.T) {
		t.Parallel()
		s, err := decodeStream(t, `
stream:
  deltas: ["hi"]
`)
		require.NoError(t, err)
		require.Empty(t, s.Policy)
		require.Equal(t, StreamServe, s.EffectivePolicy())
	})

	t.Run("script pace and a per-delta override both decode", func(t *testing.T) {
		t.Parallel()
		s, err := decodeStream(t, `
stream:
  when_requested: stream
  pace: 40ms
  deltas:
    - "Report A "
    - "finds "
    - text: "that X."
      pace: 250ms
  terminal:
    pace: 10ms
`)
		require.NoError(t, err)
		require.Equal(t, 40*time.Millisecond, s.Pace.Duration())
		require.Equal(t, []StreamDelta{
			{Text: "Report A "},
			{Text: "finds "},
			{Text: "that X.", Pace: Duration(250 * time.Millisecond)},
		}, s.Deltas)
		require.NotNil(t, s.Terminal)
		require.Equal(t, 10*time.Millisecond, s.Terminal.Pace.Duration())
	})

	t.Run("an unknown mapping key is a strict-decode error", func(t *testing.T) {
		t.Parallel()
		_, err := decodeStream(t, `
stream:
  when_requested: stream
  bogus_key: true
`)
		require.Error(t, err)
	})

	t.Run("an unknown key on a mapping-form delta is a strict-decode error", func(t *testing.T) {
		t.Parallel()
		_, err := decodeStream(t, `
stream:
  deltas:
    - text: "hi"
      bogus_key: true
`)
		require.Error(t, err)
	})

	t.Run("absent stream key leaves the zero value", func(t *testing.T) {
		t.Parallel()
		s, err := decodeStream(t, `{}`)
		require.NoError(t, err)
		require.Equal(t, StreamScript{}, s)
		require.Equal(t, StreamWarn, s.EffectivePolicy())
	})
}

// deltas builds a []StreamDelta from plain text, for tests that do not care
// about per-delta pacing.
func deltas(texts ...string) []StreamDelta {
	out := make([]StreamDelta, len(texts))
	for i, s := range texts {
		out[i] = StreamDelta{Text: s}
	}
	return out
}

func TestStreamScriptEffectivePolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    *StreamScript
		want StreamPolicy
	}{
		{"nil is warn", nil, StreamWarn},
		{"zero value is warn", &StreamScript{}, StreamWarn},
		{"deltas with no explicit policy imply stream", &StreamScript{Deltas: deltas("a")}, StreamServe},
		{"an explicit policy always wins", &StreamScript{Policy: StreamReject, Deltas: deltas("a")}, StreamReject},
		{"an explicit warn wins even with deltas present", &StreamScript{Policy: StreamWarn, Deltas: deltas("a")}, StreamWarn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.s.EffectivePolicy())
		})
	}
}

func TestValidateStreamScripts(t *testing.T) {
	t.Parallel()

	t.Run("no turns produces no findings", func(t *testing.T) {
		t.Parallel()
		require.Empty(t, ValidateStreamScripts(nil))
	})

	t.Run("a fully scripted turn is clean", func(t *testing.T) {
		t.Parallel()
		findings := ValidateStreamScripts([]StreamTurn{
			{Path: "providers.perplexity.turns[0].respond",
				Script: &StreamScript{Policy: StreamServe, Deltas: deltas("a ", "b")}, Answer: "a b"},
		})
		require.Empty(t, findings)
	})

	t.Run("warn and reject with no deltas are clean, matching every shipped fixture", func(t *testing.T) {
		t.Parallel()
		for _, policy := range []StreamPolicy{StreamWarn, StreamReject, ""} {
			findings := ValidateStreamScripts([]StreamTurn{
				{Path: "p", Script: &StreamScript{Policy: policy}, Answer: "hello"},
			})
			require.Emptyf(t, findings, "policy %q", policy)
		}
	})

	t.Run("unknown policy on turn 0 is an error", func(t *testing.T) {
		t.Parallel()
		findings := ValidateStreamScripts([]StreamTurn{
			{Path: "p0", Script: &StreamScript{Policy: "rejct"}},
		})
		require.Len(t, findings, 1)
		require.Equal(t, CodeStreamPolicyUnknown, findings[0].Code)
		require.Equal(t, SeverityError, findings[0].Severity)
		require.Equal(t, "p0.stream.when_requested", findings[0].Path)
	})

	t.Run("a policy declared on a later turn is a warning, whatever its value", func(t *testing.T) {
		t.Parallel()
		findings := ValidateStreamScripts([]StreamTurn{
			{Path: "p0", Script: &StreamScript{Policy: StreamWarn}},
			{Path: "p1", Script: &StreamScript{Policy: StreamReject}},
		})
		require.Len(t, findings, 1)
		require.Equal(t, CodeStreamPolicyIgnored, findings[0].Code)
		require.Equal(t, SeverityWarning, findings[0].Severity)
		require.Equal(t, "p1.stream.when_requested", findings[0].Path)
	})

	t.Run("an unrecognised policy on a later turn is still an error, alongside the ignored warning", func(t *testing.T) {
		t.Parallel()
		findings := ValidateStreamScripts([]StreamTurn{
			{Path: "p0", Script: &StreamScript{Policy: StreamWarn}},
			{Path: "p1", Script: &StreamScript{Policy: "rejct"}},
		})
		require.Len(t, findings, 2, "a typo pasted onto a later turn must not pass silently under only the ignored warning")
		require.Equal(t, CodeStreamPolicyIgnored, findings[0].Code)
		require.Equal(t, CodeStreamPolicyUnknown, findings[1].Code)
		require.Equal(t, SeverityError, findings[1].Severity)
		require.Equal(t, "p1.stream.when_requested", findings[1].Path)
	})

	t.Run("a streaming entry with a turn declaring no deltas is an error", func(t *testing.T) {
		t.Parallel()
		findings := ValidateStreamScripts([]StreamTurn{
			{Path: "p0", Script: &StreamScript{Policy: StreamServe, Deltas: deltas("a")}},
			{Path: "p1", Script: nil}, // no stream: block at all on turn 1
		})
		require.Len(t, findings, 1)
		require.Equal(t, CodeStreamDeltasEmpty, findings[0].Code)
		require.Equal(t, SeverityError, findings[0].Severity)
		require.Equal(t, "p1.stream.deltas", findings[0].Path)
	})

	t.Run("a non-streaming entry with a turn declaring deltas is an error, even on turn 0", func(t *testing.T) {
		t.Parallel()
		findings := ValidateStreamScripts([]StreamTurn{
			{Path: "p0", Script: &StreamScript{Policy: StreamWarn, Deltas: deltas("dead")}},
		})
		require.Len(t, findings, 1)
		require.Equal(t, CodeStreamDeltasIgnored, findings[0].Code)
		require.Equal(t, SeverityError, findings[0].Severity)
		require.Equal(t, "p0.stream.deltas", findings[0].Path)
	})

	t.Run("mismatched deltas warn, matched deltas do not", func(t *testing.T) {
		t.Parallel()
		mismatch := ValidateStreamScripts([]StreamTurn{
			{Path: "p0", Script: &StreamScript{Policy: StreamServe, Deltas: deltas("a", "b")}, Answer: "totally different"},
		})
		require.Len(t, mismatch, 1)
		require.Equal(t, CodeStreamAnswerMismatch, mismatch[0].Code)
		require.Equal(t, SeverityWarning, mismatch[0].Severity)

		match := ValidateStreamScripts([]StreamTurn{
			{Path: "p0", Script: &StreamScript{Policy: StreamServe, Deltas: deltas("a", "b")}, Answer: "ab"},
		})
		require.Empty(t, match)
	})

	t.Run("an empty answer is not compared, so a turn with no non-streaming answer is not flagged", func(t *testing.T) {
		t.Parallel()
		findings := ValidateStreamScripts([]StreamTurn{
			{Path: "p0", Script: &StreamScript{Policy: StreamServe, Deltas: deltas("a")}, Answer: ""},
		})
		require.Empty(t, findings)
	})
}

func TestValidateStreamFaultMismatch(t *testing.T) {
	t.Parallel()

	// threeDeltaTurns is the streaming projection state a single-turn entry
	// scripting three deltas would gather: chunkCount == 4 (3 deltas + the
	// terminal chunk), so a valid after_chunk is 0..3.
	threeDeltaTurns := []StreamTurn{
		{Path: "providers.perplexity", Script: &StreamScript{Policy: StreamServe, Deltas: deltas("a", "b", "c")}},
	}

	t.Run("nil entry produces no findings", func(t *testing.T) {
		t.Parallel()
		require.Empty(t, ValidateStreamFaultMismatch(nil, StreamServe, nil))
	})

	t.Run("truncate_body under a non-streaming policy stays valid — the required regression fixture", func(t *testing.T) {
		t.Parallel()
		e := &ProviderEntry{Name: "exa", Turns: []Turn{
			{Fault: &Fault{Attempts: []FaultAttempt{{Kind: FaultTruncateBody, TruncateAfterBytes: 40}}}},
		}}
		for _, policy := range []StreamPolicy{StreamWarn, StreamReject, ""} {
			require.Emptyf(t, ValidateStreamFaultMismatch(e, policy, nil), "policy %q", policy)
		}
	})

	t.Run("truncate_body under a streaming policy is an error", func(t *testing.T) {
		t.Parallel()
		e := &ProviderEntry{Name: "perplexity", Turns: []Turn{
			{Fault: &Fault{Attempts: []FaultAttempt{{Kind: FaultTruncateBody, TruncateAfterBytes: 40}}}},
		}}
		findings := ValidateStreamFaultMismatch(e, StreamServe, threeDeltaTurns)
		require.Len(t, findings, 1)
		require.Equal(t, CodeStreamFaultMismatch, findings[0].Code)
		require.Equal(t, SeverityError, findings[0].Severity)
		require.Equal(t, "providers.perplexity.fault.attempts[0].kind", findings[0].Path)
	})

	t.Run("a non-truncate_body, non-stream_* kind under a streaming policy is untouched", func(t *testing.T) {
		t.Parallel()
		e := &ProviderEntry{Name: "perplexity", Turns: []Turn{
			{Fault: &Fault{Attempts: []FaultAttempt{{Status: 429}}}},
		}}
		require.Empty(t, ValidateStreamFaultMismatch(e, StreamServe, threeDeltaTurns))
	})

	t.Run("a stream_* kind under a non-streaming policy is an error — the mirror direction", func(t *testing.T) {
		t.Parallel()
		for _, kind := range []FaultKind{FaultStreamDisconnect, FaultStreamTruncateChunk, FaultStreamStall} {
			e := &ProviderEntry{Name: "perplexity", Turns: []Turn{
				{Fault: &Fault{Attempts: []FaultAttempt{{Kind: kind, AfterChunk: 1}}}},
			}}
			for _, policy := range []StreamPolicy{StreamWarn, StreamReject, ""} {
				findings := ValidateStreamFaultMismatch(e, policy, nil)
				require.Lenf(t, findings, 1, "kind %q policy %q", kind, policy)
				require.Equal(t, CodeStreamFaultMismatch, findings[0].Code)
				require.Equal(t, SeverityError, findings[0].Severity)
				require.Equal(t, "providers.perplexity.fault.attempts[0].kind", findings[0].Path)
			}
		}
	})

	t.Run("a stream_* kind under a streaming policy with after_chunk in range is untouched", func(t *testing.T) {
		t.Parallel()
		for _, after := range []int{0, 3} { // 3 is chunk_count-1: the terminal chunk itself is a valid target
			e := &ProviderEntry{Name: "perplexity", Turns: []Turn{
				{Fault: &Fault{Attempts: []FaultAttempt{{Kind: FaultStreamDisconnect, AfterChunk: after}}}},
			}}
			require.Emptyf(t, ValidateStreamFaultMismatch(e, StreamServe, threeDeltaTurns), "after_chunk %d", after)
		}
	})

	t.Run("after_chunk equal to chunk_count is out of range, not merely 'exceeds'", func(t *testing.T) {
		t.Parallel()
		e := &ProviderEntry{Name: "perplexity", Turns: []Turn{
			{Fault: &Fault{Attempts: []FaultAttempt{{Kind: FaultStreamDisconnect, AfterChunk: 4}}}},
		}}
		findings := ValidateStreamFaultMismatch(e, StreamServe, threeDeltaTurns)
		require.Len(t, findings, 1)
		require.Equal(t, CodeStreamAfterChunkOutOfRange, findings[0].Code)
		require.Equal(t, SeverityError, findings[0].Severity)
		require.Equal(t, "providers.perplexity.fault.attempts[0].after_chunk", findings[0].Path)
	})

	t.Run("the bound is the SMALLEST chunk_count across the entry's turns, not the declaring turn's own", func(t *testing.T) {
		t.Parallel()
		// Turn 0 has 3 deltas (chunk_count 4); turn 1 has only 1 (chunk_count
		// 2). The fault plan is per route, so after_chunk: 2 must be bounded
		// by the shorter sibling even though it is declared on turn 0.
		turns := []StreamTurn{
			{Path: "providers.perplexity", Script: &StreamScript{Policy: StreamServe, Deltas: deltas("a", "b", "c")}},
			{Path: "providers.perplexity", Script: &StreamScript{Deltas: deltas("only")}},
		}
		e := &ProviderEntry{Name: "perplexity", Turns: []Turn{
			{Fault: &Fault{Attempts: []FaultAttempt{{Kind: FaultStreamDisconnect, AfterChunk: 2}}}},
			{},
		}}
		findings := ValidateStreamFaultMismatch(e, StreamServe, turns)
		require.Len(t, findings, 1, "after_chunk 2 is >= the shorter sibling's chunk_count (2)")
		require.Equal(t, CodeStreamAfterChunkOutOfRange, findings[0].Code)
	})

	t.Run("a negative after_chunk is out of range too, not only one past the end", func(t *testing.T) {
		t.Parallel()
		e := &ProviderEntry{Name: "perplexity", Turns: []Turn{
			{Fault: &Fault{Attempts: []FaultAttempt{{Kind: FaultStreamDisconnect, AfterChunk: -1}}}},
		}}
		findings := ValidateStreamFaultMismatch(e, StreamServe, threeDeltaTurns)
		require.Len(t, findings, 1)
		require.Equal(t, CodeStreamAfterChunkOutOfRange, findings[0].Code)
		require.Equal(t, SeverityError, findings[0].Severity)
	})

	t.Run("StreamTurn.ChunkCount overrides the default len(Deltas)+1 formula", func(t *testing.T) {
		t.Parallel()
		// One delta, but the calling provider's own grammar produces 5 more
		// indexed frames around it (GrammarTyped's envelope events) — the
		// default formula (1+1=2) would wrongly reject after_chunk: 3, which
		// is in range against the overridden count (6).
		turns := []StreamTurn{
			{Path: "providers.perplexity_agent", Script: &StreamScript{Policy: StreamServe, Deltas: deltas("a")}, ChunkCount: 6},
		}
		e := &ProviderEntry{Name: "perplexity_agent", Turns: []Turn{
			{Fault: &Fault{Attempts: []FaultAttempt{{Kind: FaultStreamDisconnect, AfterChunk: 3}}}},
		}}
		require.Empty(t, ValidateStreamFaultMismatch(e, StreamServe, turns),
			"after_chunk 3 is in range against the overridden ChunkCount (6), not the default formula's (2)")

		findings := ValidateStreamFaultMismatch(e, StreamServe, []StreamTurn{
			{Path: "providers.perplexity_agent", Script: &StreamScript{Policy: StreamServe, Deltas: deltas("a")}, ChunkCount: 3},
		})
		require.Len(t, findings, 1, "after_chunk 3 is out of range against a smaller override (3)")
		require.Equal(t, CodeStreamAfterChunkOutOfRange, findings[0].Code)
	})

	t.Run("stream_truncate_chunk and stream_stall are bounded the same way as stream_disconnect", func(t *testing.T) {
		t.Parallel()
		for _, kind := range []FaultKind{FaultStreamTruncateChunk, FaultStreamStall} {
			e := &ProviderEntry{Name: "perplexity", Turns: []Turn{
				{Fault: &Fault{Attempts: []FaultAttempt{{Kind: kind, AfterChunk: 4}}}},
			}}
			findings := ValidateStreamFaultMismatch(e, StreamServe, threeDeltaTurns)
			require.Lenf(t, findings, 1, "kind %q", kind)
			require.Equal(t, CodeStreamAfterChunkOutOfRange, findings[0].Code)
		}
	})
}
