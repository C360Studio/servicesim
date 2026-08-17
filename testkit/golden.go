package testkit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// UpdateGoldenEnv names the environment variable that rewrites a golden file
// instead of comparing against it:
//
//	SERVICESIM_UPDATE_GOLDEN=1 go test ./...
//
// It is an environment variable and not a -update flag on purpose. testkit is an
// ordinary imported package, so a flag.Bool at its package scope would be
// registered at init in every consumer's test binary; a consumer that already
// defines its own -update flag — a near-universal Go convention — would panic
// with "flag redefined: update" and lose the whole binary, with no visible link
// to Servicesim.
const UpdateGoldenEnv = "SERVICESIM_UPDATE_GOLDEN"

// goldenOptions is the resolved configuration of one AssertGoldenJSON call.
type goldenOptions struct {
	ignore     []string
	derivedIDs []string
	exactIDs   bool
}

// GoldenOption tunes [AssertGoldenJSON] and [AssertGoldenSSE]; for the
// latter it applies inside every frame's decoded data, not once for the
// whole transcript.
type GoldenOption func(*goldenOptions)

// GoldenIgnore excludes additional top-level or dotted JSON paths from the
// comparison, for example "usage.total_tokens". A path segment names an object
// key; array indices are not addressable, because a golden that needs to ignore
// one element of an array is a golden that is pinning the wrong thing.
func GoldenIgnore(paths ...string) GoldenOption {
	return func(o *goldenOptions) {
		o.ignore = append(o.ignore, paths...)
	}
}

// GoldenDerivedIDs names dotted JSON paths a caller declares as derived per
// call — a response field or SSE-frame field whose value advances with call
// index (an attempt-scripted identifier, most often), so comparing it would
// make a retry or fault-plan test fail for the one reason it should not.
//
// There is no default set: a package that names no vendor cannot know a
// vendor's field spelling, so nothing is pruned unless a caller says so.
// [Sim.DerivedIDs] returns the registered profiles' own declared paths —
// sourcing GoldenDerivedIDs from it, rather than naming a field by hand, is
// what proves the pruning is caller-declared, not testkit-assumed:
//
//	paths, streamPaths := sim.DerivedIDs()
//	testkit.AssertGoldenSSE(t, path, transcript, testkit.GoldenDerivedIDs(append(paths, streamPaths...)...))
//
// [GoldenExactIDs] opts back out of every path this option named, for a test
// that always asserts at the same call position: an identifier advances with
// call index on every call, whether or not the route has a declared fault
// plan, so it is stable across runs only for the same call position, never
// merely because no fault plan is declared.
func GoldenDerivedIDs(paths ...string) GoldenOption {
	return func(o *goldenOptions) {
		o.derivedIDs = append(o.derivedIDs, paths...)
	}
}

// GoldenExactIDs opts out of pruning the paths [GoldenDerivedIDs] named, so a
// golden compares them exactly rather than ignoring them.
func GoldenExactIDs() GoldenOption {
	return func(o *goldenOptions) {
		o.exactIDs = true
	}
}

// AssertGoldenJSON compares got against the golden file at path, semantically:
// both sides are decoded into any and diffed with go-cmp, because extra-field
// merging reorders keys and JSON object order is not part of any of these wire
// contracts.
//
// It ignores nothing by default: pass [GoldenDerivedIDs] naming the paths
// this golden's own vendor derives per call, and [GoldenIgnore] to exclude
// more.
//
// It performs a plain comparison and knows nothing about provenance: a consumer's
// goldens live in the consumer's repository, and failing their first call with an
// error about a convention that exists only inside Servicesim would be
// indefensible. This repository's own provenance requirement — every golden has
// an entry recording the documentation URL and verification date — is enforced
// over contracts/, where it belongs.
//
// Updating a golden is opt-in through [UpdateGoldenEnv], never through a flag.
func AssertGoldenJSON(tb testing.TB, path string, got []byte, opts ...GoldenOption) {
	tb.Helper()

	o := goldenOptions{}
	for _, opt := range opts {
		opt(&o)
	}

	gotValue, err := decodeJSON(got)
	if err != nil {
		tb.Errorf("the response is not JSON: %v\n%s", err, got)
		return
	}

	if os.Getenv(UpdateGoldenEnv) == "1" {
		writeGolden(tb, path, gotValue)
		return
	}

	raw, err := os.ReadFile(path) //nolint:gosec // the caller names its own golden
	if err != nil {
		tb.Errorf("reading the golden file: %v\nrerun with %s=1 to write it", err, UpdateGoldenEnv)
		return
	}
	wantValue, err := decodeJSON(raw)
	if err != nil {
		tb.Errorf("the golden file %s is not JSON: %v", path, err)
		return
	}

	ignore := o.ignore
	if !o.exactIDs {
		ignore = slices.Concat(o.derivedIDs, ignore)
	}
	for _, p := range ignore {
		wantValue = prune(wantValue, p)
		gotValue = prune(gotValue, p)
	}

	if diff := cmp.Diff(wantValue, gotValue); diff != "" {
		tb.Errorf("response does not match %s (-golden +got):\n%s\nrerun with %s=1 to update it",
			path, diff, UpdateGoldenEnv)
	}
}

// writeGolden rewrites path with the observed response, indented so the file is
// reviewable in a diff. The bytes are not compared to anything afterwards: the
// comparison is semantic, so re-encoding is free, and a golden nobody can read is
// a golden nobody checks.
func writeGolden(tb testing.TB, path string, value any) {
	tb.Helper()

	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		tb.Errorf("encoding the golden file %s: %v", path, err)
		return
	}
	writeGoldenBytes(tb, path, append(encoded, '\n'))
}

// AssertGoldenSSE compares an SSE transcript against a golden file, frame by
// frame.
//
// transcript is the reassembled response body — whatever chunked-transfer
// reassembly the caller's own read already did. Comparing raw bytes would
// flake: TCP read boundaries are not deterministic (docs/design/streaming.md
// §6.2), so a frame split across two reads on one run and one read on the
// next would fail a byte comparison even though the frames are identical.
// Both sides are parsed into (event, data) frames — the same shape
// provider.EncodeSSE produces — and diffed frame by frame, which is the
// entire reason this exists as its own assertion rather than a second call
// to [AssertGoldenJSON]: a one-character change in one delta reports as a
// one-frame diff, not a whole-file diff.
//
// Each frame's data line is decoded as JSON and compared semantically,
// through the same [GoldenOption]s AssertGoldenJSON accepts: pass
// [GoldenDerivedIDs] naming every path this golden's own vendor derives per
// call, inside every frame's decoded data — for a typed-event grammar whose
// frames wrap a payload rather than a bare response body, that is commonly a
// nested path such as response.id or item_id, not only a top-level one. One
// path shape needs no declaring at all: every element's "id" inside a
// response.output array (a typed-event grammar's list of output items) is
// pruned whenever any pruning applies (whenever [GoldenExactIDs] was not
// passed), because it sits inside a JSON array, which the dotted
// GoldenIgnore/GoldenDerivedIDs path syntax cannot address by design (see
// [GoldenIgnore]) — pruned unconditionally rather than left permanently
// unmatchable. [GoldenExactIDs] opts back into comparing all of the above,
// including that one, for a route with no declared fault plan, and
// [GoldenIgnore] excludes more. The bare "[DONE]" token is not JSON and is
// compared as the literal string it is — its presence or absence is itself
// part of the golden, which is what lets a scripted terminal.omit_done be
// pinned rather than silently ignored.
//
// UPDATE_GOLDEN behaviour matches AssertGoldenJSON's: SERVICESIM_UPDATE_GOLDEN=1
// rewrites the file from transcript. Unlike AssertGoldenJSON, the bytes are
// written verbatim rather than re-encoded: SSE framing is itself part of the
// wire contract (§6.1), so the golden file must stay the literal bytes a
// client observed, not a pretty-printed re-derivation of them.
func AssertGoldenSSE(tb testing.TB, path string, transcript []byte, opts ...GoldenOption) {
	tb.Helper()

	if os.Getenv(UpdateGoldenEnv) == "1" {
		writeGoldenSSE(tb, path, transcript)
		return
	}

	o := goldenOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	ignore := o.ignore
	pruneOutputIDs := false
	if !o.exactIDs {
		ignore = slices.Concat(o.derivedIDs, ignore)
		pruneOutputIDs = true
	}

	raw, err := os.ReadFile(path) //nolint:gosec // the caller names its own golden
	if err != nil {
		tb.Errorf("reading the golden file: %v\nrerun with %s=1 to write it", err, UpdateGoldenEnv)
		return
	}

	want := decodeSSEFrames(parseSSEFrames(raw), ignore, pruneOutputIDs)
	got := decodeSSEFrames(parseSSEFrames(transcript), ignore, pruneOutputIDs)

	if len(want) != len(got) {
		tb.Errorf("%s: got %d frames, want %d\nrerun with %s=1 to update it",
			path, len(got), len(want), UpdateGoldenEnv)
	}
	for i := 0; i < len(want) && i < len(got); i++ {
		if diff := cmp.Diff(want[i], got[i]); diff != "" {
			tb.Errorf("%s: frame %d does not match (-golden +got):\n%s\nrerun with %s=1 to update it",
				path, i, diff, UpdateGoldenEnv)
		}
	}
}

// writeGoldenSSE rewrites path with transcript verbatim. Unlike writeGolden,
// there is no re-encoding step: SSE framing is part of the wire contract
// (docs/design/streaming.md §6.1), so the golden file has to stay the
// literal bytes a client observed rather than a JSON-indented re-derivation
// of them.
func writeGoldenSSE(tb testing.TB, path string, transcript []byte) {
	tb.Helper()
	writeGoldenBytes(tb, path, transcript)
}

// writeGoldenBytes creates path's parent directory if needed and writes data
// verbatim, the tail both writeGolden and writeGoldenSSE share — only the
// encoding step before it differs between them.
func writeGoldenBytes(tb testing.TB, path string, data []byte) {
	tb.Helper()

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			tb.Errorf("creating the directory for %s: %v", path, err)
			return
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		tb.Errorf("writing the golden file %s: %v", path, err)
		return
	}
	tb.Logf("%s=1: wrote %s", UpdateGoldenEnv, path)
}

// sseRawFrame is one parsed (event, data) pair, before its data is decoded as
// JSON.
type sseRawFrame struct {
	event string
	data  string
}

// parseSSEFrames splits an SSE transcript into (event, data) frames, matching
// the framing provider.EncodeSSE produces: an optional "event:" line, one or
// more "data:" lines joined by "\n" per the SSE spec, then a blank line. A
// final frame with no trailing blank line — a transcript that ends
// mid-frame, such as a stream_truncate_chunk fixture — is still captured
// rather than silently dropped, so its partial content stays comparable
// instead of vanishing from the diff.
//
// A field's value is taken per the SSE spec, not per EncodeSSE's own output
// convention: the colon may be followed directly by the value ("data:foo"),
// by exactly one space that is stripped ("data: foo"), or by nothing at all
// (a bare "data:" or "event:" line, an empty value). EncodeSSE always emits
// the single-space form, so every golden this repository writes parses
// either way; the wider match only matters for a transcript a consumer
// hand-edited or captured from a real vendor.
func parseSSEFrames(transcript []byte) []sseRawFrame {
	var frames []sseRawFrame
	var cur sseRawFrame
	var data []string
	open := false

	flush := func() {
		if open {
			cur.data = strings.Join(data, "\n")
			frames = append(frames, cur)
		}
		cur, data, open = sseRawFrame{}, nil, false
	}

	for _, line := range strings.Split(string(transcript), "\n") {
		line = strings.TrimSuffix(line, "\r")
		field, value, hasField := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch {
		case line == "":
			flush()
		case hasField && field == "event":
			cur.event = value
			open = true
		case hasField && field == "data":
			data = append(data, value)
			open = true
		}
	}
	flush()
	return frames
}

// sseFrame is one frame's comparable value: the event name, and its data
// decoded as JSON (or the literal string, when it does not parse as JSON) and
// pruned of whatever paths the caller's [GoldenOption]s named.
type sseFrame struct {
	Event string
	Data  any
}

// decodeSSEFrames decodes each raw frame's data as JSON, falling back to the
// literal string when it does not parse — the bare "[DONE]" sentinel, or a
// truncated fixture's partial payload — and prunes ignore from the decoded
// value exactly as AssertGoldenJSON prunes a decoded body. When
// pruneOutputIDs is set, it additionally strips the derived "id" field from
// every element of a response.completed frame's response.output array (see
// pruneOutputIDs): those ids are not reachable through a dotted GoldenIgnore
// path because they sit inside an array, not because they are any less
// derived from call index than the paths ignore already covers.
func decodeSSEFrames(raw []sseRawFrame, ignore []string, pruneOutputIDs bool) []sseFrame {
	out := make([]sseFrame, len(raw))
	for i, f := range raw {
		var v any
		if err := json.Unmarshal([]byte(f.data), &v); err != nil {
			v = f.data
		}
		for _, p := range ignore {
			v = prune(v, p)
		}
		if pruneOutputIDs {
			v = pruneResponseOutputIDs(v)
		}
		out[i] = sseFrame{Event: f.event, Data: v}
	}
	return out
}

// pruneResponseOutputIDs strips the "id" field from every element of
// response.output, when v decodes to a frame shaped that way (the
// GrammarTyped response.created/response.completed events). It is not
// expressible through the dotted-path GoldenIgnore/GoldenDerivedIDs mechanism
// because response.output is a JSON array and prune does not address array
// elements by index — deliberately, per GoldenIgnore's doc comment, because a
// golden that pins one element of an array is pinning the wrong thing. This
// is the opposite of that: every element's derived id is stripped, not one
// chosen by index, so no element's position is being singled out.
func pruneResponseOutputIDs(v any) any {
	obj, ok := v.(map[string]any)
	if !ok {
		return v
	}
	resp, ok := obj["response"].(map[string]any)
	if !ok {
		return v
	}
	output, ok := resp["output"].([]any)
	if !ok {
		return v
	}
	for _, item := range output {
		if m, ok := item.(map[string]any); ok {
			delete(m, "id")
		}
	}
	return v
}

// prune removes a dotted path from a decoded JSON value, returning the value
// with the key gone. A path that does not exist is not an error: a golden that
// ignores a field one provider does not emit must not fail for that reason.
func prune(v any, path string) any {
	key, rest, nested := strings.Cut(path, ".")
	obj, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if !nested {
		delete(obj, key)
		return obj
	}
	if child, present := obj[key]; present {
		obj[key] = prune(child, rest)
	}
	return obj
}
