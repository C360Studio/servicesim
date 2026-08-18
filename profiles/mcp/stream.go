package mcp

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// progressNotification is the notifications/progress shape
// (contracts/mcp/README.md, "notifications/progress").
type progressNotification struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  progressParams `json:"params"`
}

// progressParams carries the request's own progressToken back verbatim —
// json.RawMessage, never decoded and re-encoded, for the same reason id is
// (decision 14 applies to every echoed client value, not only id).
type progressParams struct {
	ProgressToken json.RawMessage `json:"progressToken"`
	Progress      int             `json:"progress"`
	Total         int             `json:"total"`
	Message       string          `json:"message"`
}

// renderProgressStream builds decision 5's SSE sequence: one
// notifications/progress frame per scripted delta — only when the request
// carried _meta.progressToken — followed by the final JSON-RPC response
// frame, which is finalBody (the exact bytes [handleToolsCall] already
// built for the non-streaming Body). Framing is GrammarDelta with no
// Stream.Sentinel — unnamed "data:" frames, no [DONE] sentinel, matching
// the specification's silence on SSE framing plus this profile's own
// choice (decision 5).
func renderProgressStream(pp parsedParams, p *projection, finalBody []byte) (*provider.Stream, error) {
	var events []provider.SSEEvent

	if token, hasToken := pp.meta[metaProgressToken]; hasToken {
		deltas := p.Stream.Deltas
		total := len(deltas)
		for i, d := range deltas {
			data, err := provider.Render(progressNotification{
				JSONRPC: "2.0",
				Method:  "notifications/progress",
				Params: progressParams{
					ProgressToken: token, Progress: i + 1, Total: total, Message: d.Text,
				},
			}, nil, nil)
			if err != nil {
				return nil, err
			}
			events = append(events, provider.SSEEvent{Data: data, Pace: deltaPace(p.Stream, i)})
		}
	}

	events = append(events, provider.SSEEvent{Data: finalBody, Terminal: true, Pace: terminalPace(p.Stream)})

	return &provider.Stream{
		Grammar: provider.GrammarDelta,
		Chunks:  provider.EncodeSSE(events),
		// Sentinel stays nil: no [DONE] frame, per this profile's own
		// choice (decision 5) — the framework never infers one from Grammar.
	}, nil
}

// deltaPace returns the planned gap before delta i's progress frame: the
// delta's own pace override, or the script's default.
func deltaPace(s scenario.StreamScript, i int) time.Duration {
	if i < len(s.Deltas) && s.Deltas[i].Pace != 0 {
		return s.Deltas[i].Pace.Duration()
	}
	return s.Pace.Duration()
}

// terminalPace returns the planned gap before the final response frame:
// the script's terminal override, or its default.
func terminalPace(s scenario.StreamScript) time.Duration {
	if s.Terminal != nil && s.Terminal.Pace != 0 {
		return s.Terminal.Pace.Duration()
	}
	return s.Pace.Duration()
}

// streamHeader returns provider.StreamHeader() plus X-Accel-Buffering: no
// — the one place a fidelity claim is backed by the specification itself
// (decision 5), unlike Perplexity's streaming headers, so this profile
// adds it where the shared helper deliberately does not.
func streamHeader() http.Header {
	h := provider.StreamHeader()
	h.Set("X-Accel-Buffering", "no")
	return h
}
