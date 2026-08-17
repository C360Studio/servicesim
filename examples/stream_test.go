package examples_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/profiles/perplexity"
	"github.com/c360studio/servicesim/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamScenario scripts a two-delta Perplexity Sonar stream, inline rather
// than testkit.WithBuiltin("streaming"): a scenario dedicated to this test
// keeps testdata/stream-happy.sse stable regardless of future edits to the
// built-in streaming scenario, and keeps the test readable without a detour
// into scenarios/protocol/streaming.yaml to see what it will stream. The two
// deltas concatenate to the answer, which keeps the scenario free of
// scenario.stream.answer_mismatch.
const streamScenario = `
version: 1
name: examples-stream
providers:
  perplexity:
    answer: "Report A finds that X."
    stream:
      when_requested: stream
      deltas:
        - "Report A "
        - "finds that X."
`

// TestAdapterReceivesAStreamedPerplexityResponse is the streaming
// counterpart to TestAdapterSendsACorrectPerplexityRequest. It issues the
// request directly rather than through examples.Adapter, whose SearchPerplexity
// method is a plain JSON caller: a real Server-Sent Events client is a
// per-consumer choice, and this test exists to show the shape of that
// choice, not to add SSE support to the example adapter itself.
//
// It demonstrates docs/design/streaming.md §5.3's own three-row table in
// order: the request shape is provable as soon as any byte arrived
// (testkit.AssertNoErrors, testkit.AssertBearerAuth); the planned schedule
// is final before the first byte (testkit.AssertStreamPacing); and
// bytes_written/chunks_sent/state are only safe to read after the exchange
// closes (testkit.AwaitStreamClosed) — the journal read a consumer's own
// AssertPollSequence-style assertions already use for a create-then-poll
// job, applied here to a streamed exchange's own lifecycle instead. The
// reassembled transcript itself is pinned against a golden checked into
// testdata/, diffed frame by frame rather than byte for byte
// (testkit.AssertGoldenSSE), because raw TCP read boundaries are not
// deterministic (§6.2).
func TestAdapterReceivesAStreamedPerplexityResponse(t *testing.T) {
	t.Parallel()

	sim := testkit.Start(t, testkit.WithProfiles(perplexity.Profile()), testkit.WithScenarioYAML(streamScenario), testkit.WithProviders(perplexity.Name))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		sim.URL(perplexity.Name)+"/v1/sonar", strings.NewReader(
			`{"model":"sonar-deep-research","messages":[{"role":"user","content":"report a"}],"stream":true}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+perplexityKey)

	resp, err := sim.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// The reassembled transcript: whatever chunked-transfer read boundaries
	// this specific run happened to see are already gone once io.ReadAll
	// returns, which is what makes comparing it against a golden safe.
	transcript, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	testkit.AssertGoldenSSE(t, "testdata/stream-happy.sse", transcript)

	testkit.AssertRequestCount(t, sim, perplexity.Name, 1)
	entry := sim.Requests(perplexity.Name)[0]
	testkit.AssertNoErrors(t, entry)
	testkit.AssertBearerAuth(t, entry)
	testkit.AssertNoCredentialLeak(t, sim, exaKey, tavilyKey, perplexityKey)

	// Final before the first byte reached the client: no wait needed. Three
	// planned gaps — one per delta, plus the terminal chunk that carries
	// finish_reason and usage.
	testkit.AssertStreamPacing(t, entry, 0, 0, 0)

	// Only safe to read after the exchange closes.
	closed := sim.AwaitStreamClosed(t, entry.Seq)
	require.NotNil(t, closed.Outcome.Stream)
	assert.Equal(t, testkit.StreamCompleted, closed.Outcome.Stream.State)
	assert.Equal(t, closed.Outcome.Stream.ChunkCount, closed.Outcome.Stream.ChunksSent,
		"a completed stream delivers every indexed chunk it planned")
}
