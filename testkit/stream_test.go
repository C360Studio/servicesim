package testkit_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// perplexityStreamScenario is the single-shot Perplexity Sonar form (no
// turns:, no respond: wrapper), streaming two deltas whose concatenation
// equals the answer, so loading it raises no
// scenario.stream.answer_mismatch warning.
const perplexityStreamScenario = `
version: 1
name: testkit-stream
providers:
  perplexity:
    answer: "Report A finds that X."
    stream:
      when_requested: stream
      deltas:
        - "Report A "
        - text: "finds that X."
          pace: 5ms
`

// doStream issues a streaming Sonar request against base and returns the
// reassembled transcript — the read-boundary reassembly
// docs/design/streaming.md §6.2 requires of any caller comparing bytes.
func doStream(tb testing.TB, sim *testkit.Sim, base string) []byte {
	tb.Helper()

	req := newRequest(context.Background(), tb, base+"/v1/sonar", provider.Perplexity,
		`{"model":"sonar-deep-research","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	resp, err := sim.Client().Do(req)
	require.NoError(tb, err)
	defer func() { _ = resp.Body.Close() }()

	transcript, err := io.ReadAll(resp.Body)
	require.NoError(tb, err)
	return transcript
}

// TestAwaitStreamClosedWaitsForCompletion is the happy path §5.3 describes:
// the entry is open the instant it is appended and closed once the
// handler's write loop finishes, and AwaitStreamClosed is what a test uses
// to observe the second state rather than the first.
func TestAwaitStreamClosedWaitsForCompletion(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(perplexityStreamScenario),
		testkit.WithProviders(provider.Perplexity))

	transcript := doStream(t, sim, sim.URL(provider.Perplexity))
	assert.Contains(t, string(transcript), "[DONE]")

	entries := sim.AwaitRequests(t, provider.Perplexity, 1)
	require.Len(t, entries, 1)
	seq := entries[0].Seq

	closed := sim.AwaitStreamClosed(t, seq)
	require.NotNil(t, closed.Outcome.Stream)
	assert.Equal(t, testkit.StreamCompleted, closed.Outcome.Stream.State)
	assert.Equal(t, closed.Outcome.Stream.ChunkCount, closed.Outcome.Stream.ChunksSent,
		"a completed stream delivers every indexed chunk it planned")
	assert.Positive(t, closed.Outcome.BytesWritten)
}

// TestNamespaceAwaitStreamClosedScopesToItsLane mirrors
// TestAwaitStreamClosedWaitsForCompletion through the namespaced form, which
// exists because a bare seq does not identify an entry across namespaces
// (§5.3): [Namespace.AwaitStreamClosed] is the pair partner of
// [Sim.AwaitStreamClosed], on the same terms [Namespace.AwaitRequests]
// already establishes.
func TestNamespaceAwaitStreamClosedScopesToItsLane(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(perplexityStreamScenario),
		testkit.WithProviders(provider.Perplexity))
	ns := sim.NamespaceFor(t)

	_ = doStream(t, sim, ns.URL(provider.Perplexity))

	entries := ns.AwaitRequests(t, provider.Perplexity, 1)
	require.Len(t, entries, 1)

	closed := ns.AwaitStreamClosed(t, entries[0].Seq)
	require.NotNil(t, closed.Outcome.Stream)
	assert.Equal(t, testkit.StreamCompleted, closed.Outcome.Stream.State)
}

// TestAwaitStreamClosedFailsOnANonStreamingEntry proves the guard fails fast
// — no five-second wait — when seq names a real entry whose outcome.stream
// is nil: that state can never change, so waiting for it would only prove
// the deadline works, not the entry.
func TestAwaitStreamClosedFailsOnANonStreamingEntry(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithBuiltin("happy"), testkit.WithProviders(provider.Exa))
	resp := search(t, sim, provider.Exa, "/search", `{"query":"report a"}`)
	require.Equal(t, 200, resp.StatusCode)

	entries := sim.AwaitRequests(t, provider.Exa, 1)
	require.Len(t, entries, 1)
	require.Nil(t, entries[0].Outcome.Stream)

	stub := newFatalTB(t.Name())
	sim.AwaitStreamClosed(stub, entries[0].Seq)

	assert.True(t, stub.Failed())
	assert.Contains(t, stub.Message(), "is not a streamed entry")
}

// TestAssertStreamPacingComparesThePlannedSchedule pins §6.3's own claim: the
// assertion reads outcome.stream.pace_ms, the declared plan, never a
// wall-clock measurement.
func TestAssertStreamPacingComparesThePlannedSchedule(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(perplexityStreamScenario),
		testkit.WithProviders(provider.Perplexity))
	_ = doStream(t, sim, sim.URL(provider.Perplexity))

	entries := sim.AwaitRequests(t, provider.Perplexity, 1)
	require.Len(t, entries, 1)
	e := entries[0]
	require.NotNil(t, e.Outcome.Stream)

	// Two deltas (one at the script's zero default, one overridden to 5ms) and
	// one terminal chunk at the script default again.
	testkit.AssertStreamPacing(t, e, 0, 5*time.Millisecond, 0)

	stub := &stubTB{}
	testkit.AssertStreamPacing(stub, e, 0, 0, 0)
	assert.True(t, stub.Failed(), "the second delta's 5ms override must not compare equal to 0")
}

// TestAssertStreamUsageComparesTheDeclaredUsage pins §5.3's own claim: the
// terminal chunk's usage object is final before the client sees a byte, so
// AssertStreamUsage needs no AwaitStreamClosed wait — the same reason
// TestAssertStreamPacingComparesThePlannedSchedule reads outcome.stream
// straight off AwaitRequests rather than AwaitStreamClosed.
func TestAssertStreamUsageComparesTheDeclaredUsage(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithScenarioYAML(perplexityStreamScenario),
		testkit.WithProviders(provider.Perplexity))
	_ = doStream(t, sim, sim.URL(provider.Perplexity))

	entries := sim.AwaitRequests(t, provider.Perplexity, 1)
	require.Len(t, entries, 1)
	e := entries[0]
	require.NotNil(t, e.Outcome.Stream)
	require.NotNil(t, e.Outcome.Stream.Usage, "the scenario declares no terminal.omit_usage")

	var want map[string]any
	require.NoError(t, json.Unmarshal(e.Outcome.Stream.Usage, &want))
	testkit.AssertStreamUsage(t, e, want)

	stub := &stubTB{}
	testkit.AssertStreamUsage(stub, e, map[string]any{"total_tokens": -1})
	assert.True(t, stub.Failed(), "a usage object that does not match the declared one must fail")
}

// TestAssertStreamUsageOnAnOmitUsageScenario pins §5.3's own resolution: a
// terminal.omit_usage script leaves outcome.stream.usage nil, and that is
// asserted the same way any other declared shape is, by passing nil for
// want.
func TestAssertStreamUsageOnAnOmitUsageScenario(t *testing.T) {
	t.Parallel()

	const omitUsageScenario = `
version: 1
name: testkit-stream-omit-usage
providers:
  perplexity:
    answer: "Report A finds that X."
    stream:
      when_requested: stream
      deltas:
        - "Report A finds that X."
      terminal:
        omit_usage: true
`
	sim := testkit.Start(t, testkit.WithScenarioYAML(omitUsageScenario),
		testkit.WithProviders(provider.Perplexity))
	_ = doStream(t, sim, sim.URL(provider.Perplexity))

	entries := sim.AwaitRequests(t, provider.Perplexity, 1)
	require.Len(t, entries, 1)
	e := entries[0]
	require.NotNil(t, e.Outcome.Stream)
	require.Nil(t, e.Outcome.Stream.Usage)

	testkit.AssertStreamUsage(t, e, nil)

	stub := &stubTB{}
	testkit.AssertStreamUsage(stub, e, map[string]any{"total_tokens": 1})
	assert.True(t, stub.Failed(), "nil usage must not compare equal to a non-nil want")
}

// TestAssertStreamUsageFailsOnANonStreamingEntry mirrors
// TestAwaitStreamClosedFailsOnANonStreamingEntry for the assertion rather
// than the wait.
func TestAssertStreamUsageFailsOnANonStreamingEntry(t *testing.T) {
	t.Parallel()

	stub := &stubTB{}
	testkit.AssertStreamUsage(stub, testkit.Entry{Seq: 1}, nil)

	assert.True(t, stub.Failed())
	assert.Contains(t, stub.Message(), "is not a streamed entry")
}

// TestAssertStreamPacingFailsOnANonStreamingEntry mirrors
// TestAwaitStreamClosedFailsOnANonStreamingEntry for the assertion rather
// than the wait: an entry with no outcome.stream has no schedule to compare
// against.
func TestAssertStreamPacingFailsOnANonStreamingEntry(t *testing.T) {
	t.Parallel()

	stub := &stubTB{}
	testkit.AssertStreamPacing(stub, testkit.Entry{Seq: 1}, 0)

	assert.True(t, stub.Failed())
	assert.Contains(t, stub.Message(), "is not a streamed entry")
}
