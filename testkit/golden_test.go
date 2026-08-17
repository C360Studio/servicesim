package testkit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sseTranscript builds a three-frame SSE transcript by hand — two unnamed
// data frames and the bare [DONE] sentinel — in exactly the framing
// provider.EncodeSSE produces: an optional "event:" line, one "data:" line,
// a blank line. Building it by hand, rather than through a live Sim,
// exercises AssertGoldenSSE's own parser and frame-diff in isolation;
// TestAwaitStreamClosedWaitsForCompletion and its siblings in stream_test.go
// already prove the shape matches a real handler's output.
func sseTranscript(id, secondDeltaText string) []byte {
	var b strings.Builder
	b.WriteString(`data: {"id":"` + id + `","i":0,"text":"Report A "}` + "\n\n")
	b.WriteString(`data: {"id":"` + id + `","i":1,"text":"` + secondDeltaText + `"}` + "\n\n")
	b.WriteString("data: [DONE]\n\n")
	return []byte(b.String())
}

// agentTranscript builds a minimal four-frame GrammarTyped (Perplexity
// Agent) SSE transcript by hand: response.created and response.completed
// carry a call-index-derived response id nested under "response", while the
// output_item/output_text events carry a call-index-derived message id under
// "item" or "item_id" — respId and msgID stand in for both, matching
// renderAgentIdentity's shape (provider/perplexity/agent.go) closely enough
// to exercise AssertGoldenSSE's default id pruning without a live Sim.
func agentTranscript(respID, msgID string) []byte {
	var b strings.Builder
	b.WriteString(`event: response.created` + "\n")
	b.WriteString(`data: {"type":"response.created","response":{"id":"` + respID + `","output":[]}}` + "\n\n")
	b.WriteString(`event: response.output_item.added` + "\n")
	b.WriteString(`data: {"type":"response.output_item.added","item":{"type":"message","id":"` + msgID + `"}}` + "\n\n")
	b.WriteString(`event: response.output_text.delta` + "\n")
	b.WriteString(`data: {"type":"response.output_text.delta","item_id":"` + msgID + `","delta":"hi"}` + "\n\n")
	b.WriteString(`event: response.completed` + "\n")
	b.WriteString(`data: {"type":"response.completed","response":{"id":"` + respID +
		`","output":[{"type":"message","id":"` + msgID + `"}]}}` + "\n\n")
	return []byte(b.String())
}

func TestAssertGoldenSSE(t *testing.T) {
	body := sseTranscript("call-0", "finds that X.")
	path := filepath.Join(t.TempDir(), "nested", "stream.sse")

	t.Run("missing golden names the update variable", func(t *testing.T) {
		stub := &stubTB{}
		testkit.AssertGoldenSSE(stub, path, body)

		assert.True(t, stub.Failed())
		assert.Contains(t, stub.Message(), testkit.UpdateGoldenEnv+"=1")
	})

	t.Run("the environment variable writes the transcript verbatim", func(t *testing.T) {
		t.Setenv(testkit.UpdateGoldenEnv, "1")
		testkit.AssertGoldenSSE(t, path, body)

		written, err := os.ReadFile(path) //nolint:gosec // the path is this test's own temp dir
		require.NoError(t, err)
		assert.Equal(t, body, written,
			"SSE framing is the wire contract (§6.1): the golden must be the literal bytes, not a re-encoding")
	})

	t.Run("a written golden then compares clean", func(t *testing.T) {
		testkit.AssertGoldenSSE(t, path, body)
	})

	t.Run("GoldenDerivedIDs prunes a named path inside every frame", func(t *testing.T) {
		varied := sseTranscript("call-9", "finds that X.")

		stub := &stubTB{}
		testkit.AssertGoldenSSE(stub, path, varied)
		assert.True(t, stub.Failed(), "no GoldenDerivedIDs was passed, so \"id\" is compared exactly")

		testkit.AssertGoldenSSE(t, path, varied, testkit.GoldenDerivedIDs("id"))

		stub2 := &stubTB{}
		testkit.AssertGoldenSSE(stub2, path, varied, testkit.GoldenDerivedIDs("id"), testkit.GoldenExactIDs())
		assert.True(t, stub2.Failed(), "GoldenExactIDs opts back out of every path GoldenDerivedIDs named")
	})

	t.Run("GoldenIgnore excludes a dotted path inside a frame's data", func(t *testing.T) {
		changed := sseTranscript("call-0", "finds that X.")
		changed = []byte(strings.Replace(string(changed), `"i":1`, `"i":9`, 1))

		stub := &stubTB{}
		testkit.AssertGoldenSSE(stub, path, changed)
		assert.True(t, stub.Failed())

		testkit.AssertGoldenSSE(t, path, changed, testkit.GoldenIgnore("i"))
	})

	t.Run("a one-delta change reports one frame, not the whole file", func(t *testing.T) {
		changed := sseTranscript("call-0", "finds that Y.")

		stub := &stubTB{}
		testkit.AssertGoldenSSE(stub, path, changed)

		require.True(t, stub.Failed())
		msg := stub.Message()
		assert.Contains(t, msg, "frame 1")
		assert.NotContains(t, msg, "frame 0 does not match")
		assert.NotContains(t, msg, "frame 2 does not match")
	})

	t.Run("the DONE sentinel is compared literally and its absence is caught", func(t *testing.T) {
		noDone := body[:strings.LastIndex(string(body), "data: [DONE]")]

		stub := &stubTB{}
		testkit.AssertGoldenSSE(stub, path, noDone)
		assert.True(t, stub.Failed())
		assert.Contains(t, stub.Message(), "got 2 frames, want 3")
	})

	t.Run("Agent (GrammarTyped) derived ids named via GoldenDerivedIDs are pruned inside response and item", func(t *testing.T) {
		typedPath := filepath.Join(t.TempDir(), "typed-ids.sse")
		t.Setenv(testkit.UpdateGoldenEnv, "1")
		testkit.AssertGoldenSSE(t, typedPath, agentTranscript("resp_A", "msg_A"))
		require.NoError(t, os.Unsetenv(testkit.UpdateGoldenEnv))

		agentDerivedIDs := []string{"response.id", "item.id", "item_id"}

		// response.id, item.id and item_id are named through GoldenDerivedIDs;
		// response.output[].id is pruned unconditionally whenever any pruning
		// applies (see AssertGoldenSSE's own doc comment). None of them should
		// fail the comparison.
		testkit.AssertGoldenSSE(t, typedPath, agentTranscript("resp_B", "msg_B"), testkit.GoldenDerivedIDs(agentDerivedIDs...))

		stub := &stubTB{}
		testkit.AssertGoldenSSE(stub, typedPath, agentTranscript("resp_B", "msg_B"),
			testkit.GoldenDerivedIDs(agentDerivedIDs...), testkit.GoldenExactIDs())
		assert.True(t, stub.Failed(), "GoldenExactIDs opts back into comparing the Agent grammar's ids too")
	})

	t.Run("an event: line is part of the comparison", func(t *testing.T) {
		typedPath := filepath.Join(t.TempDir(), "typed.sse")
		t.Setenv(testkit.UpdateGoldenEnv, "1")
		testkit.AssertGoldenSSE(t, typedPath, []byte("event: response.created\ndata: {\"id\":\"c-0\"}\n\n"))
		require.NoError(t, os.Unsetenv(testkit.UpdateGoldenEnv))

		stub := &stubTB{}
		testkit.AssertGoldenSSE(stub, typedPath, []byte("event: response.completed\ndata: {\"id\":\"c-0\"}\n\n"))
		assert.True(t, stub.Failed(), "the event name differs even though the data does not")
	})
}
