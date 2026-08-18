package testkit

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
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

// AssertDifferentCredential asserts two requests presented different
// credentials, by comparing fingerprints rather than values — the mirror of
// [AssertSameCredential]. Both entries must have presented a credential, on
// the same terms as that assertion, and fail with the same "cannot compare"
// shape when one did not. The journal never holds a credential, which is
// what makes fingerprint comparison the only available one; this is the
// assertion that proves a client actually rotated to a new credential rather
// than retried with the one it already had.
func AssertDifferentCredential(tb testing.TB, a, b Entry) {
	tb.Helper()

	if !a.Auth.Present || !b.Auth.Present {
		tb.Errorf("cannot compare credentials: seq %d presented one=%t, seq %d presented one=%t",
			a.Seq, a.Auth.Present, b.Seq, b.Auth.Present)
		return
	}
	if a.Auth.Fingerprint == b.Auth.Fingerprint {
		tb.Errorf("seq %d and seq %d presented the same credential (fingerprint %s), want different ones",
			a.Seq, b.Seq, a.Auth.Fingerprint)
	}
}

// AssertNoCredentialLeak scans every journal entry for the given literals and
// fails if any appears. The scan covers every text-bearing field, not just the
// two obvious ones: Headers, Body, Query, Path, Outcome.Label, BodyParseError,
// Outcome.FaultKey and every finding's Message and Field.
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
		// outcome.fault_key is the turn_key extractor's composed lane key
		// (provider/lane.go turnLaneKey). A credential-named extractor is
		// fingerprinted there, and journal.Redact masks a credential-shaped one
		// as a second pass, but this scan exists precisely so a consumer's suite
		// does not have to trust either of those in isolation.
		{"outcome.fault_key", e.Outcome.FaultKey},
	}
	// Sorted, not map order: a failure message must name the same header on every
	// run, and Go randomises map iteration.
	for _, name := range slices.Sorted(maps.Keys(e.Headers)) {
		out = append(out, leakField{"header " + name, name + ": " + strings.Join(e.Headers[name], ", ")})
	}
	for _, f := range e.Findings {
		out = append(out, leakField{"finding " + f.Code, f.Message})
		out = append(out, leakField{"finding.field " + f.Code, f.Field})
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

// AssertMaxRate asserts that no per-long window of arrivals held more than
// limit of them — "no per contains more than limit calls" — which is the RPS
// evidence a client-side limiter is proven against under decision D5
// (docs/adopter-backlog.md): Servicesim ships no enforced rate limiter,
// because an enforced one would make response status a function of
// wall-clock time and contradict the determinism doctrine. Entries are
// sorted by ArrivedAt first — never assume journal order, which reflects
// completion and carries no guarantee at all across namespaces or providers
// — then, for every arrival i, the half-open window [ArrivedAt[i],
// ArrivedAt[i]+per) is counted: an arrival exactly per after the anchor is
// NOT in that window. A window over limit fails, naming the anchor's seq and
// arrival stamp, the count, the limit, and every seq the window held.
//
// It compares real, wall-clock arrival timestamps — provider.SystemClock by
// default; see [WithClock]'s warning — and is safe on a loaded machine in
// only one direction: a slow scheduler can only SPREAD arrivals further
// apart, which can never manufacture a window that looks busier than the
// client actually made it. Only a client that really exceeded its declared
// budget fails this. There is deliberately no "minimum observed rate" form:
// that would be an upper bound on wall-clock elapsed time, exactly the flake
// this repository refuses to add.
//
// limit < 1 or per <= 0 is a caller error and fails tb with a message rather
// than panicking. Fewer than two entries passes trivially.
func AssertMaxRate(tb testing.TB, entries []Entry, limit int, per time.Duration) {
	tb.Helper()

	if limit < 1 {
		tb.Errorf("testkit: AssertMaxRate called with limit %d, want at least 1", limit)
		return
	}
	if per <= 0 {
		tb.Errorf("testkit: AssertMaxRate called with per %s, want a positive duration", per)
		return
	}
	if len(entries) < 2 {
		return
	}

	sorted := sortedByArrival(entries)
	for i, anchor := range sorted {
		end := anchor.ArrivedAt.Add(per)

		var window []Entry
		for _, e := range sorted[i:] {
			if !e.ArrivedAt.Before(end) {
				break // sorted ascending: nothing further can re-enter the window
			}
			window = append(window, e)
		}
		if len(window) <= limit {
			continue
		}

		seqs := make([]uint64, len(window))
		for j, e := range window {
			seqs[j] = e.Seq
		}
		tb.Errorf("seq %d at %s: %d arrivals within %s (limit %d), seqs %v",
			anchor.Seq, anchor.ArrivedAt.Format(stampLayout), len(window), per, limit, seqs)
	}
}

// AssertMinGap asserts every pair of consecutive arrivals — sorted by
// ArrivedAt, not journal order — is at least gap apart, which is the D5
// evidence for a client that paces by spacing rather than by budget (see
// [AssertMaxRate] for the sibling evidence and the reasoning both share). A
// pair closer than gap fails, naming both seqs and the observed gap.
//
// Like [AssertMaxRate], it compares real wall-clock timestamps and is safe
// on a loaded machine in only one direction: a slow scheduler can only WIDEN
// the observed gap, never close it, so a client that really paced itself
// never fails this on a busy CI runner. Fewer than two entries passes
// trivially. There is deliberately no "maximum gap" form, for the same
// reason [AssertMaxRate] has no "minimum rate" form: it would be an upper
// bound on wall-clock elapsed time.
func AssertMinGap(tb testing.TB, entries []Entry, gap time.Duration) {
	tb.Helper()

	if len(entries) < 2 {
		return
	}

	sorted := sortedByArrival(entries)
	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		if got := cur.ArrivedAt.Sub(prev.ArrivedAt); got < gap {
			tb.Errorf("seq %d arrived %s after seq %d (%s and %s), want at least %s",
				cur.Seq, got, prev.Seq, prev.ArrivedAt.Format(stampLayout), cur.ArrivedAt.Format(stampLayout), gap)
		}
	}
}

// AssertObservedDuration asserts CompletedAt - ArrivedAt is at least
// atLeast, which is how a consumer proves a hang was REALLY observed rather
// than merely requested — the D5 counterpart to [AssertMaxRate] and
// [AssertMinGap] for a single entry's duration, after a delay: or
// delay_after_headers: fault attempt. It fails naming the seq, both
// timestamps and the observed duration.
//
// What CompletedAt means depends on the entry's class
// (docs/design/package-design.md §2.2 rule 3):
//
//   - a non-streaming exchange: the instant the response finished, or, for an
//     aborting fault, the instant just before the connection was destroyed —
//     after every scripted hang, including an after-headers one;
//   - a streamed exchange: provisional until the stream closes — call
//     [Sim.AwaitStreamClosed] (or [Namespace.AwaitStreamClosed]) first and
//     read the entry it returns, not one from an earlier Requests or Journal
//     call;
//   - a client-cancelled exchange: the instant the server observed the
//     cancellation, not the instant the client gave up — and the entry
//     lands only after the client has already returned, so read it through
//     [Sim.AwaitRequests] (or [Namespace.AwaitRequests]), never a
//     synchronous Requests call.
//
// It compares real, wall-clock timestamps — provider.SystemClock by default;
// see [WithClock]'s warning — and is safe on a loaded machine in only one
// direction: a slow machine can only LENGTHEN the observed duration, never
// shorten it, so a hang that really happened never fails to be observed here
// just because CI was busy. There is deliberately no "maximum duration"
// form: that is an upper bound on wall-clock elapsed time, exactly the flake
// this repository refuses to add.
func AssertObservedDuration(tb testing.TB, e Entry, atLeast time.Duration) {
	tb.Helper()

	if got := e.CompletedAt.Sub(e.ArrivedAt); got < atLeast {
		tb.Errorf("seq %d observed %s..%s (%s), want at least %s",
			e.Seq, e.ArrivedAt.Format(stampLayout), e.CompletedAt.Format(stampLayout), got, atLeast)
	}
}

// sortedByArrival returns a copy of entries ordered by ArrivedAt, so a
// pacing assertion never mutates the caller's slice and never assumes
// journal order — which reflects completion, not arrival, and carries no
// guarantee at all across namespaces or providers. The sort is stable so
// that entries sharing an identical ArrivedAt — real timestamps can coincide
// exactly — keep the caller's order, and a failure message names the same
// seq on every run.
func sortedByArrival(entries []Entry) []Entry {
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(a, b Entry) int { return a.ArrivedAt.Compare(b.ArrivedAt) })
	return sorted
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

// AssertPollSequence asserts that one job's polls, read from the journal
// alone, are exactly wantStatuses in order.
//
// entries is the journal slice to read — [Sim.Requests] for the whole process,
// or [Namespace.Requests] for one lane. It takes entries rather than a *Sim for
// the same reason [AssertOverlapped] and [AssertJSONBody] take an [Entry]:
// the caller chooses the scope, and a caller with its own [Journal]
// implementation can still use it. From those it keeps the GET entries whose
// path's last segment equals id (job identifiers are drawn from
// [A-Za-z0-9_-], so no decoding question arises), in the order given, which
// for both Requests methods is arrival order. Any such GET counts, including
// one a route failed to match — a poll of a path that merely ends in this id
// but was never a real job route still consumes a slot in the sequence, and a
// wrong count is how that shows up.
//
// Pass one namespace's entries when the test uses namespaces. [Job]
// identifiers are namespace-independent by design — unique within a
// namespace, deliberately not across them — so the identical id can be live
// in two namespaces at once, which is exactly what happens when two
// namespaces each create at the same call index: the per-test-namespace idiom
// [Sim.NamespaceFor] exists to support that. Passing the whole Sim's entries
// is fine as long as the id turns out not to be live elsewhere — if it is,
// AssertPollSequence reports the ambiguity and refuses to merge the two jobs'
// polls into one sequence, rather than silently mis-scoring both.
//
// It requires exactly len(wantStatuses) such entries, and for entry i requires
// both e.Outcome.Status == wantStatuses[i] AND e.Outcome.AttemptIndex == i. The
// attempt-index half is the point: it is what proves the polls drew
// consecutive positions from the one per-job lane a create and its polls
// share — the create-then-poll correlation — so that nothing else (a HEAD, a
// poll of a different id, a request the route rejected) consumed one of this
// job's polls. HEAD is already excluded by the method filter.
//
// The journal retains request bodies, not response bodies, so the vendor's
// own `status` string in the poll response is not something this assertion
// can read; the HTTP status and the cursor position are, and together they
// pin the same lifecycle a consumer's poll loop observes.
func AssertPollSequence(tb testing.TB, entries []Entry, id string, wantStatuses ...int) {
	tb.Helper()

	var polls []Entry
	for _, e := range entries {
		if e.Method != http.MethodGet {
			continue
		}
		if lastPathSegment(e.Path) != id {
			continue
		}
		polls = append(polls, e)
	}

	if lanes := pollLanes(polls); len(lanes) > 1 {
		tb.Errorf("job %q was polled in %d different namespaces (%s): job identifiers are "+
			"namespace-independent by design, so reading this id across every namespace is ambiguous; pass "+
			"one Namespace's Requests instead\n%s",
			id, len(lanes), strings.Join(lanes, ", "), pollTable(polls))
		return
	}

	if len(polls) != len(wantStatuses) {
		tb.Errorf("job %q recorded %d polls, want %d\n%s",
			id, len(polls), len(wantStatuses), pollTable(polls))
		return
	}
	for i, e := range polls {
		if e.Outcome.Status != wantStatuses[i] || e.Outcome.AttemptIndex != i {
			tb.Errorf("job %q poll %d: got status %d attempt_index %d, want status %d attempt_index %d\n%s",
				id, i, e.Outcome.Status, e.Outcome.AttemptIndex, wantStatuses[i], i, pollTable(polls))
			return
		}
	}
}

// pollLanes returns the distinct, sorted namespaces the given poll entries
// were recorded in. AssertPollSequence uses it to detect when an id's polls
// came from more than one namespace, which means they are two different
// jobs that merely happen to share an identifier.
func pollLanes(entries []Entry) []string {
	seen := map[string]bool{}
	for _, e := range entries {
		seen[laneOf(e)] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// lastPathSegment returns the final "/"-delimited segment of path, which is
// where a job identifier appears on every poll route this repository serves.
func lastPathSegment(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// pollTable renders the observed (method, path, namespace, status,
// attempt_index) rows for an AssertPollSequence failure, the same way
// AssertNamespacesIsolated's failures name the entries they compared
// against. The namespace column is what makes a cross-namespace id
// collision (see AssertPollSequence's doc comment) diagnosable from the
// failure output instead of just from the count.
func pollTable(entries []Entry) string {
	var b strings.Builder
	b.WriteString("observed polls:")
	for _, e := range entries {
		fmt.Fprintf(&b, "\n  %s %s ns=%q status=%d attempt_index=%d",
			e.Method, e.Path, laneOf(e), e.Outcome.Status, e.Outcome.AttemptIndex)
	}
	return b.String()
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

// AssertCovers asserts that every scenario file under fsys — every ".yaml" or
// ".yml" file found by walking it — declares a non-empty block for every name
// in kinds. It is the library form of the coverage guard the four reference
// profiles have run against scenarios.FS since before this package existed
// (scenarios/scenarios_test.go's TestBuiltins_CoverEveryImplementedProvider):
// a consumer whose own corpus should mean the same thing on every listener it
// registers runs this in its own CI, in place of copying that test.
//
// kinds names scenario ENTRY KINDS — [provider.Set.EntryKinds] lists a
// registered set's own — not listener names: a multi-entry profile such as
// Perplexity (Sonar plus Agent) or Exa (search plus its async agent-run
// surface) has more entry kinds than listeners, and each is checked on its
// own.
//
// A file that fails to load is reported and skipped rather than aborting the
// walk, so one broken fixture does not hide every other file's own coverage
// gaps in the same run.
func AssertCovers(tb testing.TB, fsys fs.FS, kinds ...string) {
	tb.Helper()

	files, err := scenarioFiles(fsys)
	if err != nil {
		tb.Errorf("testkit.AssertCovers: walking the scenario tree: %v", err)
		return
	}
	if len(files) == 0 {
		tb.Errorf("testkit.AssertCovers: no .yaml or .yml file found")
		return
	}
	if len(kinds) == 0 {
		tb.Errorf("testkit.AssertCovers: no kinds given")
		return
	}

	for _, name := range files {
		s, report, err := scenario.LoadFS(fsys, name)
		if err != nil {
			tb.Errorf("testkit.AssertCovers: %s: %v%s", name, err, formatFindings(report.Findings))
			continue
		}
		for _, kind := range kinds {
			entry := s.Provider(kind)
			if entry == nil {
				tb.Errorf("testkit.AssertCovers: %s declares no %q block", name, kind)
				continue
			}
			if len(entry.Turns) == 0 {
				tb.Errorf("testkit.AssertCovers: %s: %q has no turns", name, kind)
			}
		}
	}
}

// scenarioFiles walks fsys and returns every ".yaml"/".yml" file's path, in
// the stable, sorted order fs.WalkDir already visits directory entries in —
// a coverage failure that reordered its own file list between runs would be
// miserable to diff.
func scenarioFiles(fsys fs.FS) ([]string, error) {
	var out []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ext := path.Ext(p); ext == ".yaml" || ext == ".yml" {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}
