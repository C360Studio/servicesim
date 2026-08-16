package scenario

import (
	"testing"

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
		require.Equal(t, []string{"Report A ", "finds ", "that X."}, s.Deltas)
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

	t.Run("an unknown mapping key is a strict-decode error", func(t *testing.T) {
		t.Parallel()
		_, err := decodeStream(t, `
stream:
  when_requested: stream
  pace: 40ms
`)
		require.Error(t, err, "pace is not decoded by this build; writing it must fail loudly, not silently no-op")
	})

	t.Run("absent stream key leaves the zero value", func(t *testing.T) {
		t.Parallel()
		s, err := decodeStream(t, `{}`)
		require.NoError(t, err)
		require.Equal(t, StreamScript{}, s)
		require.Equal(t, StreamWarn, s.EffectivePolicy())
	})
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
		{"deltas with no explicit policy imply stream", &StreamScript{Deltas: []string{"a"}}, StreamServe},
		{"an explicit policy always wins", &StreamScript{Policy: StreamReject, Deltas: []string{"a"}}, StreamReject},
		{"an explicit warn wins even with deltas present", &StreamScript{Policy: StreamWarn, Deltas: []string{"a"}}, StreamWarn},
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
				Script: &StreamScript{Policy: StreamServe, Deltas: []string{"a ", "b"}}, Answer: "a b"},
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
			{Path: "p0", Script: &StreamScript{Policy: StreamServe, Deltas: []string{"a"}}},
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
			{Path: "p0", Script: &StreamScript{Policy: StreamWarn, Deltas: []string{"dead"}}},
		})
		require.Len(t, findings, 1)
		require.Equal(t, CodeStreamDeltasIgnored, findings[0].Code)
		require.Equal(t, SeverityError, findings[0].Severity)
		require.Equal(t, "p0.stream.deltas", findings[0].Path)
	})

	t.Run("mismatched deltas warn, matched deltas do not", func(t *testing.T) {
		t.Parallel()
		mismatch := ValidateStreamScripts([]StreamTurn{
			{Path: "p0", Script: &StreamScript{Policy: StreamServe, Deltas: []string{"a", "b"}}, Answer: "totally different"},
		})
		require.Len(t, mismatch, 1)
		require.Equal(t, CodeStreamAnswerMismatch, mismatch[0].Code)
		require.Equal(t, SeverityWarning, mismatch[0].Severity)

		match := ValidateStreamScripts([]StreamTurn{
			{Path: "p0", Script: &StreamScript{Policy: StreamServe, Deltas: []string{"a", "b"}}, Answer: "ab"},
		})
		require.Empty(t, match)
	})

	t.Run("an empty answer is not compared, so a turn with no non-streaming answer is not flagged", func(t *testing.T) {
		t.Parallel()
		findings := ValidateStreamScripts([]StreamTurn{
			{Path: "p0", Script: &StreamScript{Policy: StreamServe, Deltas: []string{"a"}}, Answer: ""},
		})
		require.Empty(t, findings)
	})
}

func TestValidateStreamFaultMismatch(t *testing.T) {
	t.Parallel()

	t.Run("nil entry produces no findings", func(t *testing.T) {
		t.Parallel()
		require.Empty(t, ValidateStreamFaultMismatch(nil, StreamServe))
	})

	t.Run("truncate_body under a non-streaming policy stays valid — the required regression fixture", func(t *testing.T) {
		t.Parallel()
		e := &ProviderEntry{Name: "exa", Turns: []Turn{
			{Fault: &Fault{Attempts: []FaultAttempt{{Kind: FaultTruncateBody, TruncateAfterBytes: 40}}}},
		}}
		for _, policy := range []StreamPolicy{StreamWarn, StreamReject, ""} {
			require.Emptyf(t, ValidateStreamFaultMismatch(e, policy), "policy %q", policy)
		}
	})

	t.Run("truncate_body under a streaming policy is an error", func(t *testing.T) {
		t.Parallel()
		e := &ProviderEntry{Name: "perplexity", Turns: []Turn{
			{Fault: &Fault{Attempts: []FaultAttempt{{Kind: FaultTruncateBody, TruncateAfterBytes: 40}}}},
		}}
		findings := ValidateStreamFaultMismatch(e, StreamServe)
		require.Len(t, findings, 1)
		require.Equal(t, CodeStreamFaultMismatch, findings[0].Code)
		require.Equal(t, SeverityError, findings[0].Severity)
		require.Equal(t, "providers.perplexity.fault.attempts[0].kind", findings[0].Path)
	})

	t.Run("a non-truncate_body kind under a streaming policy is untouched", func(t *testing.T) {
		t.Parallel()
		e := &ProviderEntry{Name: "perplexity", Turns: []Turn{
			{Fault: &Fault{Attempts: []FaultAttempt{{Status: 429}}}},
		}}
		require.Empty(t, ValidateStreamFaultMismatch(e, StreamServe))
	})
}
