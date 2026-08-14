package testkit

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/provider"
	"github.com/google/go-cmp/cmp"
)

// Every assertion reports through tb.Errorf rather than tb.Fatalf, so one failing
// expectation does not hide the next one, and calls tb.Helper so the failure
// points at the caller's line.

// AssertRequestCount asserts how many requests a provider received.
//
// For a request that ended at the transport level, prefer
// [Sim.AwaitRequests], which waits for the entry instead of racing it.
func AssertRequestCount(tb testing.TB, s *Sim, p provider.Name, want int) {
	tb.Helper()

	got := s.Requests(p)
	if len(got) != want {
		tb.Errorf("%s received %d requests, want %d", p, len(got), want)
	}
}

// AssertBearerAuth asserts the request presented a Bearer credential in the
// Authorization header.
func AssertBearerAuth(tb testing.TB, e Entry) {
	tb.Helper()

	switch {
	case !e.Auth.Present:
		tb.Errorf("%s %s presented no credential, want a Bearer token in Authorization", e.Method, e.Path)
	case e.Auth.Header != "authorization":
		tb.Errorf("%s %s presented its credential in %q, want %q", e.Method, e.Path, e.Auth.Header, "authorization")
	case e.Auth.Scheme != "Bearer":
		tb.Errorf("%s %s used the %q authorization scheme, want %q", e.Method, e.Path, e.Auth.Scheme, "Bearer")
	}
}

// AssertAPIKeyHeader asserts the request presented an x-api-key credential.
func AssertAPIKeyHeader(tb testing.TB, e Entry) {
	tb.Helper()

	switch {
	case !e.Auth.Present:
		tb.Errorf("%s %s presented no credential, want one in x-api-key", e.Method, e.Path)
	case e.Auth.Header != "x-api-key":
		tb.Errorf("%s %s presented its credential in %q, want %q", e.Method, e.Path, e.Auth.Header, "x-api-key")
	}
}

// AssertSameCredential asserts two requests presented the same credential, by
// comparing fingerprints rather than values. The journal never holds a
// credential, which is what makes this the only available comparison — and the
// reason it is safe to run against a suite's real configuration.
func AssertSameCredential(tb testing.TB, a, b Entry) {
	tb.Helper()

	if !a.Auth.Present || !b.Auth.Present {
		tb.Errorf("cannot compare credentials: seq %d presented one=%t, seq %d presented one=%t",
			a.Seq, a.Auth.Present, b.Seq, b.Auth.Present)
		return
	}
	if a.Auth.Fingerprint != b.Auth.Fingerprint {
		tb.Errorf("seq %d and seq %d presented different credentials (fingerprints %s and %s)",
			a.Seq, b.Seq, a.Auth.Fingerprint, b.Auth.Fingerprint)
	}
}

// AssertNoCredentialLeak scans every journal entry for the given literals and
// fails if any appears. The scan covers every text-bearing field, not just the
// two obvious ones: Headers, Body, Query, Path, Outcome.Label, BodyParseError
// and every Finding.Message.
//
// It is the assertion form of the rule that credentials never survive a round
// trip, and it is worth running in a consumer's suite against whatever key that
// suite actually sends.
func AssertNoCredentialLeak(tb testing.TB, s *Sim, literals ...string) {
	tb.Helper()

	for _, e := range s.Journal() {
		for _, literal := range literals {
			if literal == "" {
				continue // an empty literal matches everything and asserts nothing
			}
			for _, f := range leakFields(e) {
				if strings.Contains(f.text, literal) {
					tb.Errorf("seq %d leaked the credential %q into %s: %s", e.Seq, literal, f.name, f.text)
				}
			}
		}
	}
}

// leakField is one text-bearing journal field and the name a failure reports it
// under.
type leakField struct {
	name string
	text string
}

// leakFields returns every field of an entry a credential could reach. Header
// names are included alongside their values: a key placed in a header whose name
// is itself the credential is absurd but has happened, and the scan costs
// nothing.
func leakFields(e Entry) []leakField {
	out := []leakField{
		{"path", e.Path},
		{"query", e.Query},
		{"body", string(e.Body)},
		{"body_parse_error", e.BodyParseError},
		{"outcome.label", e.Outcome.Label},
	}
	// Sorted, not map order: a failure message must name the same header on every
	// run, and Go randomises map iteration.
	for _, name := range slices.Sorted(maps.Keys(e.Headers)) {
		out = append(out, leakField{"header " + name, name + ": " + strings.Join(e.Headers[name], ", ")})
	}
	for _, f := range e.Findings {
		out = append(out, leakField{"finding " + f.Code, f.Message})
	}
	return out
}

// AssertJSONBody asserts the recorded request body matches want semantically,
// using go-cmp over decoded values so JSON key order is not part of the contract.
//
// want is encoded to JSON and decoded back before the comparison, so a struct, a
// map and a raw JSON string all compare on equal terms and integer literals do
// not spuriously differ from the float64 a decoded body carries.
func AssertJSONBody(tb testing.TB, e Entry, want any) {
	tb.Helper()

	gotValue, err := decodeJSON(e.Body)
	if err != nil {
		tb.Errorf("seq %d recorded a body that is not JSON: %v", e.Seq, err)
		return
	}
	wantValue, err := normalize(want)
	if err != nil {
		tb.Errorf("the wanted body cannot be encoded as JSON: %v", err)
		return
	}
	if diff := cmp.Diff(wantValue, gotValue); diff != "" {
		tb.Errorf("seq %d request body mismatch (-want +got):\n%s", e.Seq, diff)
	}
}

// AssertFindings asserts the exact set of finding codes recorded, in any order.
func AssertFindings(tb testing.TB, e Entry, wantCodes ...string) {
	tb.Helper()

	got := make([]string, 0, len(e.Findings))
	for _, f := range e.Findings {
		got = append(got, f.Code)
	}
	want := slices.Clone(wantCodes)
	slices.Sort(got)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		tb.Errorf("seq %d recorded findings %v, want %v", e.Seq, got, want)
	}
}

// AssertNoErrors asserts the request produced no error-severity findings. This is
// the default way a test says "my adapter sent a correct vendor request".
//
// It is deliberately not [AssertNoFindings]: an unknown request field and an
// unexpected content type are warnings, so that a consumer sending a legitimate
// field Servicesim has not modelled yet still gets a green request and a 200.
// Asserting on no findings at all re-imposes exactly the strictness that policy
// exists to avoid.
func AssertNoErrors(tb testing.TB, e Entry) {
	tb.Helper()

	for _, f := range e.Errors() {
		tb.Errorf("seq %d %s %s: %s (%s)", e.Seq, e.Method, e.Path, f.Message, f.Code)
	}
}

// AssertNoFindings asserts the request produced no warnings and no errors. It is
// the strict form, for a test that wants to pin the warning set too.
func AssertNoFindings(tb testing.TB, e Entry) {
	tb.Helper()

	for _, f := range e.Findings {
		tb.Errorf("seq %d %s %s: %s %s: %s", e.Seq, e.Method, e.Path, f.Severity, f.Code, f.Message)
	}
}

// AssertOverlapped asserts two requests were in flight simultaneously, which is
// how a fusion test proves its provider calls ran concurrently rather than
// serially: a arrived strictly before b completed and b arrived strictly before a
// completed.
//
// It compares real-time journal timestamps, which is why the default clock is
// provider.SystemClock and why [WithClock] carries a warning.
func AssertOverlapped(tb testing.TB, a, b Entry) {
	tb.Helper()

	if a.ArrivedAt.Before(b.CompletedAt) && b.ArrivedAt.Before(a.CompletedAt) {
		return
	}
	tb.Errorf("requests did not overlap: seq %d ran %s..%s, seq %d ran %s..%s",
		a.Seq, a.ArrivedAt.Format(stampLayout), a.CompletedAt.Format(stampLayout),
		b.Seq, b.ArrivedAt.Format(stampLayout), b.CompletedAt.Format(stampLayout))
}

// AssertNamespacesIsolated asserts two namespaces of one [Sim] did not interfere:
// the property namespaces exist to provide, stated directly rather than left for
// a consumer to infer from a response that looks plausible.
//
// It checks four things, and each one is a failure mode that has actually
// happened:
//
//   - Every entry a namespace's journal returned belongs to that namespace, so a
//     test reading its own lane cannot be reading another's traffic.
//   - Neither namespace is empty. Two namespaces nothing was sent to are trivially
//     isolated, and a typo in a namespace name would otherwise pass silently.
//   - No cursor key appears in both namespaces. A cursor key that two namespaces
//     share is one counter serving both, which is the route-keyed cursor bug: two
//     concurrent callers draw attempt indices 0 and 1 from one sequence and each
//     receives the call scripted for the other.
//   - Within each namespace, the attempt indices claimed against one cursor key
//     are 0, 1, 2 … with no duplicate and no gap. A gap is the fingerprint of the
//     bug above even when the keys look distinct, because the missing index went
//     to somebody else.
//
// Call it after both namespaces' traffic has completed. An index gap is also what
// a request still in flight looks like, so a suite that asserts while a goroutine
// is mid-request should call [Namespace.AwaitRequests] first.
func AssertNamespacesIsolated(tb testing.TB, a, b *Namespace) {
	tb.Helper()

	if a == nil || b == nil {
		tb.Errorf("cannot compare namespaces: one of the handles is nil")
		return
	}
	if a.sim != b.sim {
		tb.Errorf("namespaces %q and %q belong to different Sims, which share no state to interfere over",
			a.name, b.name)
		return
	}
	if a.name == b.name {
		tb.Errorf("namespace %q was compared with itself, which proves nothing", a.name)
		return
	}

	entries := map[*Namespace][]Entry{a: a.Journal(), b: b.Journal()}
	for _, ns := range []*Namespace{a, b} {
		if len(entries[ns]) == 0 {
			tb.Errorf("namespace %q recorded no requests, so isolation from %q holds vacuously",
				ns.name, other(ns, a, b).name)
			continue
		}
		if n := ns.dropped(); n > 0 {
			tb.Errorf("namespace %q evicted %d entries, so its journal can no longer show whether "+
				"a cursor skipped a call; raise testkit.WithJournalCapacity", ns.name, n)
		}
		assertEntriesInNamespace(tb, ns, entries[ns])
		assertAttemptsUnbroken(tb, ns, entries[ns])
	}
	assertCursorKeysDisjoint(tb, a, entries[a], b, entries[b])
}

// other returns whichever of a and b is not ns, for a message that names the
// namespace the reader is comparing against.
func other(ns, a, b *Namespace) *Namespace {
	if ns == a {
		return b
	}
	return a
}

// assertEntriesInNamespace asserts every entry the namespace's journal returned
// was recorded in that namespace. An entry recorded before the request named a
// namespace carries none, which is the default lane.
func assertEntriesInNamespace(tb testing.TB, ns *Namespace, entries []Entry) {
	tb.Helper()

	for _, e := range entries {
		if got := laneOf(e); got != ns.name {
			tb.Errorf("namespace %q returned seq %d (%s %s), which was recorded in namespace %q",
				ns.name, e.Seq, e.Method, e.Path, got)
		}
	}
}

// assertAttemptsUnbroken asserts that, per cursor key, this namespace claimed the
// attempt indices 0..n-1 exactly once each.
//
// Entries that never claimed an attempt carry index -1 and are skipped: a request
// rejected by routing, authentication or validation draws on no budget and so can
// leave no gap.
func assertAttemptsUnbroken(tb testing.TB, ns *Namespace, entries []Entry) {
	tb.Helper()

	claimed := map[string]map[int]uint64{}
	for _, e := range entries {
		if e.Outcome.AttemptIndex < 0 || e.Outcome.FaultKey == "" {
			continue
		}
		key := e.Outcome.FaultKey
		if claimed[key] == nil {
			claimed[key] = map[int]uint64{}
		}
		if prior, taken := claimed[key][e.Outcome.AttemptIndex]; taken {
			tb.Errorf("namespace %q claimed attempt %d of %q twice, at seq %d and seq %d",
				ns.name, e.Outcome.AttemptIndex, key, prior, e.Seq)
			continue
		}
		claimed[key][e.Outcome.AttemptIndex] = e.Seq
	}

	// Sorted, not map order: a failure must name the same key on every run.
	for _, key := range slices.Sorted(maps.Keys(claimed)) {
		indices := slices.Sorted(maps.Keys(claimed[key]))
		for want, got := range indices {
			if got == want {
				continue
			}
			tb.Errorf("namespace %q claimed attempts %v of %q, want %d consecutive from 0: "+
				"the missing index went to another lane, or a request is still in flight",
				ns.name, indices, key, len(indices))
			break
		}
	}
}

// assertCursorKeysDisjoint asserts the two namespaces drew on no common cursor
// key. Sharing one is sharing one counter, whatever the indices happen to look
// like on the run that observed it.
func assertCursorKeysDisjoint(tb testing.TB, a *Namespace, aEntries []Entry, b *Namespace, bEntries []Entry) {
	tb.Helper()

	inA := cursorKeys(aEntries)
	for _, key := range cursorKeys(bEntries) {
		if slices.Contains(inA, key) {
			tb.Errorf("namespaces %q and %q both drew on the cursor key %q, so one counter served both",
				a.name, b.name, key)
		}
	}
}

// cursorKeys returns the distinct, sorted cursor keys the entries drew on.
func cursorKeys(entries []Entry) []string {
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Outcome.FaultKey != "" {
			seen[e.Outcome.FaultKey] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// laneOf returns the namespace an entry was recorded in, reading an empty one as
// the default namespace — the lane every request that named none is served in.
func laneOf(e Entry) string {
	if e.Namespace == "" {
		return provider.DefaultNamespace
	}
	return e.Namespace
}

// stampLayout renders journal instants at microsecond resolution, which is the
// scale an overlap failure is argued at. Second resolution would print two
// identical timestamps and explain nothing.
const stampLayout = "15:04:05.000000"

// decodeJSON decodes raw into a generic value. An empty body decodes to nil
// rather than failing, so an assertion about a request that carried no body
// reads as a comparison against nil instead of a parse error.
func decodeJSON(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// normalize round-trips a value through JSON so it compares on the same terms as
// a decoded body: every number is a float64 and every struct is a map.
func normalize(v any) (any, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decodeJSON(encoded)
}
