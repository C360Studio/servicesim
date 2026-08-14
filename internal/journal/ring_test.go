package journal_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/internal/redact"
)

const bigBody = 1 << 20

func TestNewRing_ClampsNegativeCapacity(t *testing.T) {
	t.Parallel()

	// A direct library caller must not be able to reach make([]Entry, -1).
	r := journal.NewRing(-1, -1)

	r.Append(journal.Entry{Provider: "exa", Body: json.RawMessage(`{"query":"go"}`)})

	if got := r.Stats(); got.Capacity != 0 || got.Stored != 0 || got.Appended != 1 || got.Dropped != 1 {
		t.Errorf("Stats() = %+v, want capacity 0, stored 0, appended 1, dropped 1", got)
	}
	if got := r.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot() = %+v, want nothing stored", got)
	}
	if got := r.Next(); got != 1 {
		t.Errorf("Next() = %d, want 1: retention off must not stop sequencing", got)
	}
}

func TestRing_EvictsOldestAtCapacity(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(3, bigBody)
	for i := 1; i <= 5; i++ {
		r.Append(journal.Entry{Provider: "exa", Seq: uint64(i)})
	}

	got := r.Snapshot()
	want := []uint64{3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("Snapshot() has %d entries, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Seq != w {
			t.Errorf("Snapshot()[%d].Seq = %d, want %d (oldest first, oldest evicted)", i, got[i].Seq, w)
		}
	}
	if s := r.Stats(); s.Capacity != 3 || s.Stored != 3 || s.Appended != 5 || s.Dropped != 2 {
		t.Errorf("Stats() = %+v, want capacity 3, stored 3, appended 5, dropped 2", s)
	}
}

func TestRing_SnapshotIsDeepCopy(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(2, bigBody)
	r.Append(journal.Entry{
		Provider: "exa",
		Headers:  map[string][]string{"X-Trace": {"one"}},
		Body:     json.RawMessage(`{"query":"go"}`),
		Findings: []journal.Finding{{Severity: journal.SeverityWarning, Code: "a"}},
	})

	first := r.Snapshot()
	first[0].Headers["X-Trace"][0] = "mutated"
	first[0].Headers["X-Injected"] = []string{"mutated"}
	first[0].Body[2] = 'X'
	first[0].Findings[0].Code = "mutated"

	second := r.Snapshot()
	if got := second[0].Headers["X-Trace"][0]; got != "one" {
		t.Errorf("header value = %q, want %q: Snapshot must copy each value slice", got, "one")
	}
	if _, ok := second[0].Headers["X-Injected"]; ok {
		t.Error("caller's insertion reached the stored entry: Snapshot must copy the map")
	}
	if got := string(second[0].Body); got != `{"query":"go"}` {
		t.Errorf("body = %s, want the stored bytes: Snapshot must copy them", got)
	}
	if got := second[0].Findings[0].Code; got != "a" {
		t.Errorf("finding code = %q, want %q: Snapshot must copy the findings slice", got, "a")
	}
}

// TestRing_AppendDoesNotRetainCallerState proves the stored entry is not
// aliased to the caller's maps and slices, which a handler reuses.
func TestRing_AppendDoesNotRetainCallerState(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(2, bigBody)
	e := journal.Entry{
		Provider: "exa",
		Headers:  map[string][]string{"X-Trace": {"one"}},
		Body:     json.RawMessage(`{"query":"go"}`),
		Findings: []journal.Finding{{Severity: journal.SeverityWarning, Code: "a"}},
	}
	r.Append(e)

	e.Headers["X-Trace"][0] = "mutated"
	e.Body[2] = 'X'
	e.Findings[0].Code = "mutated"

	got := r.Snapshot()[0]
	if got.Headers["X-Trace"][0] != "one" || string(got.Body) != `{"query":"go"}` || got.Findings[0].Code != "a" {
		t.Errorf("stored entry changed with the caller's: %+v", got)
	}
}

// spacedSecret is a credential the *structural* matcher masks whole and the
// free-text matcher only masks up to the first space. It is what makes the
// redact-then-clip ordering observable: clipping first turns the document into
// text, and text is where this value survives.
const spacedSecret = "hunter2 correct horse battery"

// TestRing_OversizedBodyIsRedactedBeforeTruncation is normative. Clipping first
// leaves a prefix that is no longer valid JSON, so the body is masked by the
// weaker text matcher instead of the structural one.
func TestRing_OversizedBodyIsRedactedBeforeTruncation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		maxBodyBytes int
		body         string
		leaked       string
	}{
		{
			// The case the package design names: a credential in the first hundred
			// bytes of a body that is over the limit.
			name:         "credential early in an oversized body",
			maxBodyBytes: 64,
			body:         fmt.Sprintf(`{"api_key":%q,"query":%q}`, secret, strings.Repeat("z", 4096)),
			leaked:       secret,
		},
		{
			// The discriminating case. Redacted first, the whole value is masked
			// structurally and the result then fits under the limit. Clipped first,
			// the retained prefix is text and the text matcher stops at the space,
			// storing the rest of the credential verbatim.
			name:         "value the text matcher would only half-mask",
			maxBodyBytes: 45,
			body:         fmt.Sprintf(`{"api_key":%q,"query":"go"}`, spacedSecret),
			leaked:       "correct horse",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if len(tt.body) <= tt.maxBodyBytes {
				t.Fatalf("test body is not oversized: %d bytes", len(tt.body))
			}

			r := journal.NewRing(1, tt.maxBodyBytes)
			r.Append(journal.Entry{Provider: "exa", Body: json.RawMessage(tt.body)})

			got := r.Snapshot()[0]
			stored := string(got.Body)
			if strings.Contains(stored, tt.leaked) {
				t.Fatalf("credential survived into the stored body: %s", stored)
			}
			if !strings.Contains(stored, redact.Mask) {
				t.Fatalf("stored body carries no mask, so redaction did not run first: %s", stored)
			}
			if len(got.Body) > tt.maxBodyBytes {
				t.Errorf("stored body is %d bytes, want it bounded by %d", len(got.Body), tt.maxBodyBytes)
			}
		})
	}
}

// TestRing_BodyTruncatedFlagsAClippedBody keeps the flag honest: a reader has
// to be able to tell a whole body from a retained prefix.
func TestRing_BodyTruncatedFlagsAClippedBody(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(2, 64)
	r.Append(journal.Entry{Provider: "exa", Body: json.RawMessage(`{"query":"go"}`)})
	r.Append(journal.Entry{Provider: "exa",
		Body: json.RawMessage(fmt.Sprintf(`{"query":%q}`, strings.Repeat("z", 4096)))})

	got := r.Snapshot()
	if got[0].BodyTruncated {
		t.Error("BodyTruncated = true for a body under the limit")
	}
	if !got[1].BodyTruncated {
		t.Error("BodyTruncated = false for a body over the limit")
	}
}

// TestRing_TruncationNeverStoresPartialCredential drives the cut through the
// middle of the credential. Redaction runs first, so what the cut lands in is
// the mask, never a fragment of the value.
func TestRing_TruncationNeverStoresPartialCredential(t *testing.T) {
	t.Parallel()

	body := fmt.Sprintf(`{"api_key":%q,"query":"go"}`, secret)

	// Every cut point from "inside the property name" to "past the value".
	for cut := 4; cut < len(body); cut += 3 {
		r := journal.NewRing(1, cut)
		r.Append(journal.Entry{Provider: "exa", Body: json.RawMessage(body)})

		stored := string(r.Snapshot()[0].Body)
		for n := 8; n <= len(secret); n++ {
			if strings.Contains(stored, secret[:n]) {
				t.Fatalf("cut at %d stored a %d-character fragment of the credential: %s", cut, n, stored)
			}
		}
	}
}

// TestRing_StoredEntryAlwaysMarshals guards /__admin/requests: a clipped body
// left in a json.RawMessage as invalid JSON would fail the whole encode.
func TestRing_StoredEntryAlwaysMarshals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		maxBodyBytes int
		body         string
	}{
		{"clipped mid-document", 12, `{"api_key":"` + secret + `","query":"go"}`},
		{"clipped to nothing", 0, `{"query":"go"}`},
		{"clipped mid-rune", 9, `{"q":"日本語のクエリ"}`},
		{"never was JSON", 4096, "api_key=" + secret + "&query=go"},
		{"fits", 4096, `{"query":"go"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := journal.NewRing(1, tt.maxBodyBytes)
			r.Append(journal.Entry{Provider: "exa", Body: json.RawMessage(tt.body)})

			stored := r.Snapshot()[0]
			if limit := max(tt.maxBodyBytes, 2); len(stored.Body) > limit {
				t.Errorf("stored body is %d bytes, want it bounded by %d", len(stored.Body), limit)
			}
			encoded, err := json.Marshal(stored)
			if err != nil {
				t.Fatalf("Marshal(entry) = %v, want a stored entry that always encodes", err)
			}
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("credential reached the encoded entry: %s", encoded)
			}
		})
	}
}

// TestAppend_RedactsQueryString is named in the design because "redaction is
// enforced at the storage boundary" does not make an implementer think of the
// query string.
func TestAppend_RedactsQueryString(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(1, bigBody)
	r.Append(journal.Entry{Provider: "exa", Query: "api_key=" + secret + "&q=go"})

	got := r.Snapshot()[0].Query
	assertMasked(t, "query", got, secret)
	if !strings.Contains(got, "api_key=") {
		t.Errorf("query = %q, want the parameter name preserved as evidence", got)
	}
}

// TestAppend_RedactsFindingMessages is named for the same reason: an
// error-severity finding message also reaches the HTTP error body.
func TestAppend_RedactsFindingMessages(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(1, bigBody)
	r.Append(journal.Entry{Provider: "exa", Findings: []journal.Finding{
		{Severity: journal.SeverityError, Code: "exa.body.invalid",
			Message: "cannot decode api_key=" + secret},
	}})

	assertMasked(t, "findings[0].message", r.Snapshot()[0].Findings[0].Message, secret)
}

// TestSnapshot_HasNoCredentialByAnyPath is the test that matters. It plants the
// same credential in every field a request can carry one in, appends without
// redacting first — a handler must not have to — and scans the encoded
// snapshot, so a field added later without a redaction rule fails here.
func TestSnapshot_HasNoCredentialByAnyPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry func() journal.Entry
	}{
		{"every field at once", entryWithCredentialEverywhere},
		{"form-encoded body", func() journal.Entry {
			e := entryWithCredentialEverywhere()
			e.Body = json.RawMessage("api_key=" + secret + "&query=go")
			return e
		}},
		{"truncated JSON body", func() journal.Entry {
			e := entryWithCredentialEverywhere()
			e.Body = json.RawMessage(`{"results":[{"api_key":"` + secret)
			return e
		}},
		{"unparseable query", func() journal.Entry {
			e := entryWithCredentialEverywhere()
			e.Query = "%zz&api_key=" + secret
			return e
		}},
		{"absolute-form userinfo quoted in an error", func() journal.Entry {
			e := entryWithCredentialEverywhere()
			e.BodyParseError = "reading http://user:" + secret + "@host.test/search failed"
			return e
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := journal.NewRing(4, bigBody)
			r.Append(tt.entry())

			encoded, err := json.Marshal(r.Snapshot())
			if err != nil {
				t.Fatalf("Marshal(snapshot) = %v", err)
			}
			assertMasked(t, "snapshot", string(encoded), secret)
		})
	}
}

func TestRing_Reset(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(2, bigBody)
	for range 3 {
		r.Next()
		r.Append(journal.Entry{Provider: "exa"})
	}

	r.Reset()

	if got := r.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot() after Reset = %+v, want empty", got)
	}
	if got := (r.Stats()); got != (journal.Stats{Capacity: 2}) {
		t.Errorf("Stats() after Reset = %+v, want every counter zeroed", got)
	}
	if got := r.Next(); got != 1 {
		t.Errorf("Next() after Reset = %d, want 1", got)
	}
}

func TestNewDiscard_StoresNothingAndStillSequences(t *testing.T) {
	t.Parallel()

	d := journal.NewDiscard()
	d.Append(entryWithCredentialEverywhere())

	if got := d.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot() = %+v, want nothing stored", got)
	}
	if got := d.Stats(); got.Capacity != 0 || got.Stored != 0 || got.Appended != 1 || got.Dropped != 1 {
		t.Errorf("Stats() = %+v, want capacity 0, stored 0, appended 1, dropped 1", got)
	}
	if first, second := d.Next(), d.Next(); first != 1 || second != 2 {
		t.Errorf("Next() = %d then %d, want 1 then 2", first, second)
	}
}

// TestNewDiscard_InstancesAreIndependent is why there is no package-level
// Discard value: a shared counter would make two parallel subtests disagree
// about which request they are looking at, and only under parallelism.
func TestNewDiscard_InstancesAreIndependent(t *testing.T) {
	t.Parallel()

	first, second := journal.NewDiscard(), journal.NewDiscard()
	if first == second {
		t.Fatal("NewDiscard returned the same instance twice")
	}

	first.Next()
	first.Next()

	if got := second.Next(); got != 1 {
		t.Errorf("second journal's Next() = %d, want 1: counters must not be shared", got)
	}
	first.Reset()
	if got := second.Next(); got != 2 {
		t.Errorf("second journal's Next() = %d after the first was Reset, want 2", got)
	}
}

// seqsOf renders the sequence numbers of a snapshot, which is what almost every
// namespace assertion is really about.
func seqsOf(entries []journal.Entry) []uint64 {
	out := make([]uint64, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Seq)
	}
	return out
}

// TestRing_SequencesAreIndependentPerNamespace is the reason namespaces reach
// this package at all. Two concurrent tests sharing one process must each see 1,
// 2, 3; halves of one interleaved sequence would make "was this my request?"
// unanswerable, which is the journal's whole job.
func TestRing_SequencesAreIndependentPerNamespace(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(8, bigBody)

	if got := []uint64{r.NextIn("t-1"), r.NextIn("t-1"), r.NextIn("t-1")}; !slices.Equal(got, []uint64{1, 2, 3}) {
		t.Errorf("NextIn(t-1) = %v, want 1, 2, 3", got)
	}
	if got := []uint64{r.NextIn("t-2"), r.NextIn("t-2")}; !slices.Equal(got, []uint64{1, 2}) {
		t.Errorf("NextIn(t-2) = %v, want a sequence of its own starting at 1", got)
	}
	if got := r.Next(); got != 1 {
		t.Errorf("Next() = %d, want 1: the default namespace is a lane like any other", got)
	}
	if got := r.NextIn(""); got != 2 {
		t.Errorf(`NextIn("") = %d, want 2: the empty namespace is the default one`, got)
	}
}

// TestRing_SnapshotScopesAndInterleaves covers both halves of the read surface:
// one namespace in isolation, and the unfiltered view that /__admin/requests
// serves, which must interleave namespaces in append order rather than in map
// order.
func TestRing_SnapshotScopesAndInterleaves(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(8, bigBody)
	appends := []struct {
		namespace string
		seq       uint64
	}{
		{"t-1", 1}, {"t-2", 1}, {"t-1", 2}, {"", 1}, {"t-2", 2}, {"t-1", 3},
	}
	for _, a := range appends {
		r.Append(journal.Entry{Provider: "exa", Namespace: a.namespace, Seq: a.seq})
	}

	tests := []struct {
		name      string
		namespace string
		want      []uint64
	}{
		{name: "one namespace", namespace: "t-1", want: []uint64{1, 2, 3}},
		{name: "another namespace", namespace: "t-2", want: []uint64{1, 2}},
		{name: "the default lane holds unnamespaced entries", namespace: "default", want: []uint64{1}},
		{name: "the empty name is the default lane", namespace: "", want: []uint64{1}},
		{name: "a namespace nothing was appended to", namespace: "t-99", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := seqsOf(r.SnapshotIn(tt.namespace)); !slices.Equal(got, tt.want) {
				t.Errorf("SnapshotIn(%q) seqs = %v, want %v", tt.namespace, got, tt.want)
			}
		})
	}

	// Unfiltered, in append order. The sequence numbers repeat across namespaces
	// by design, so the order is the assertion.
	wantAll := []uint64{1, 1, 2, 1, 2, 3}
	if got := seqsOf(r.Snapshot()); !slices.Equal(got, wantAll) {
		t.Errorf("Snapshot() seqs = %v, want %v in append order across namespaces", got, wantAll)
	}
	if got := r.Namespaces(); !slices.Equal(got, []string{"default", "t-1", "t-2"}) {
		t.Errorf("Namespaces() = %v, want the live lanes sorted", got)
	}
}

// TestRing_SnapshotOrderIsStableAcrossReads guards the merge: entries from
// several namespaces are held in several buffers, and walking a map to join them
// would let iteration order reach output (CLAUDE.md house rule 2).
func TestRing_SnapshotOrderIsStableAcrossReads(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(8, bigBody)
	for i := range 24 {
		r.Append(journal.Entry{Provider: "exa", Namespace: fmt.Sprintf("t-%d", i%6), Seq: uint64(i)})
	}

	first := seqsOf(r.Snapshot())
	for range 50 {
		if got := seqsOf(r.Snapshot()); !slices.Equal(got, first) {
			t.Fatalf("Snapshot() order changed between reads:\nfirst: %v\n then: %v", first, got)
		}
	}
}

// TestRing_AppendKeepsTheEntryNamespaceAsGiven protects the admin API's JSON: an
// entry recorded without a namespace belongs to the default lane, but rewriting
// its field to "default" would add a key to every historical entry's encoding.
func TestRing_AppendKeepsTheEntryNamespaceAsGiven(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(4, bigBody)
	r.Append(journal.Entry{Provider: "exa", Seq: 1})
	r.Append(journal.Entry{Provider: "exa", Seq: 2, Namespace: "t-1"})

	got := r.Snapshot()
	if got[0].Namespace != "" {
		t.Errorf("Namespace = %q for an entry appended without one, want it left empty", got[0].Namespace)
	}
	if got[1].Namespace != "t-1" {
		t.Errorf("Namespace = %q, want it stored as given", got[1].Namespace)
	}
}

// TestRing_CapacityIsPerNamespace is the retention bound: capacity applies to
// each lane, so total retention is max-namespaces × capacity. A shared bound
// would let one busy test evict a quiet test's entries, and a journal that
// cannot show a test its own requests is no use to it.
func TestRing_CapacityIsPerNamespace(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(2, bigBody)
	for _, ns := range []string{"t-1", "t-2", "t-3"} {
		for seq := uint64(1); seq <= 3; seq++ {
			r.Append(journal.Entry{Provider: "exa", Namespace: ns, Seq: seq})
		}
	}

	for _, ns := range []string{"t-1", "t-2", "t-3"} {
		if got := seqsOf(r.SnapshotIn(ns)); !slices.Equal(got, []uint64{2, 3}) {
			t.Errorf("SnapshotIn(%q) seqs = %v, want 2, 3: each lane evicts its own oldest", ns, got)
		}
		want := journal.Stats{Capacity: 2, Stored: 2, Appended: 3, Dropped: 1}
		if got := r.StatsIn(ns); got != want {
			t.Errorf("StatsIn(%q) = %+v, want %+v", ns, got, want)
		}
	}

	// Capacity stays the per-lane figure; the total is that times the lane count.
	want := journal.Stats{Capacity: 2, Stored: 6, Appended: 9, Dropped: 3}
	if got := r.Stats(); got != want {
		t.Errorf("Stats() = %+v, want %+v", got, want)
	}
}

// TestRing_MaxNamespacesRefusesRatherThanEvicts pins the failure mode the design
// names as the worst this feature can produce. At the bound, a new namespace is
// refused; an existing lane is never evicted, because that would silently reset a
// running test's sequence mid-loop.
func TestRing_MaxNamespacesRefusesRatherThanEvicts(t *testing.T) {
	t.Parallel()

	r := journal.NewRingWithLimits(journal.Limits{Capacity: 4, MaxBodyBytes: bigBody, MaxNamespaces: 2})
	for _, ns := range []string{"t-1", "t-2"} {
		if got := r.NextIn(ns); got != 1 {
			t.Fatalf("NextIn(%q) = %d, want 1", ns, got)
		}
		r.Append(journal.Entry{Provider: "exa", Namespace: ns, Seq: 1})
	}

	if got := r.NextIn("t-3"); got != 0 {
		t.Errorf("NextIn(t-3) at the bound = %d, want 0: nothing is retained, so no sequence was held", got)
	}
	r.Append(journal.Entry{Provider: "exa", Namespace: "t-3", Seq: 1})

	if got := r.SnapshotIn("t-3"); len(got) != 0 {
		t.Errorf("SnapshotIn(t-3) = %+v, want nothing retained for a refused namespace", got)
	}
	if got := r.Namespaces(); !slices.Equal(got, []string{"t-1", "t-2"}) {
		t.Errorf("Namespaces() = %v, want the admitted lanes and no third", got)
	}
	for _, ns := range []string{"t-1", "t-2"} {
		if got := seqsOf(r.SnapshotIn(ns)); !slices.Equal(got, []uint64{1}) {
			t.Errorf("SnapshotIn(%q) = %v, want the admitted lane untouched by the refusal", ns, got)
		}
		if got := r.NextIn(ns); got != 2 {
			t.Errorf("NextIn(%q) = %d, want 2: a refusal must not reset a live lane", ns, got)
		}
	}

	// The refused append is still an append, and it was dropped. Losing it from
	// the counters would hide the bound from whoever is debugging the refusal.
	want := journal.Stats{Capacity: 4, Stored: 2, Appended: 3, Dropped: 1}
	if got := r.Stats(); got != want {
		t.Errorf("Stats() = %+v, want %+v", got, want)
	}
}

func TestNewRingWithLimits_ClampsAndDefaults(t *testing.T) {
	t.Parallel()

	// Negative capacity must not reach make, and a zero namespace bound is the
	// documented default rather than "unbounded".
	r := journal.NewRingWithLimits(journal.Limits{Capacity: -1, MaxBodyBytes: -1})
	r.Append(journal.Entry{Provider: "exa", Namespace: "t-1", Body: json.RawMessage(`{"query":"go"}`)})

	if got := r.Stats(); got != (journal.Stats{Appended: 1, Dropped: 1}) {
		t.Errorf("Stats() = %+v, want retention off but still counted", got)
	}
	for i := range journal.DefaultMaxNamespaces {
		if got := r.NextIn(fmt.Sprintf("t-%d", i)); got == 0 {
			t.Fatalf("namespace %d was refused below the default bound of %d", i, journal.DefaultMaxNamespaces)
		}
	}
	if got := r.NextIn("one-too-many"); got != 0 {
		t.Errorf("NextIn past DefaultMaxNamespaces = %d, want 0", got)
	}
}

// TestRing_ResetInDropsOneNamespace is the counterpart to Reset: dropping one
// test's lane must leave every other concurrent test's journal exactly as it was.
func TestRing_ResetInDropsOneNamespace(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(4, bigBody)
	for _, ns := range []string{"t-1", "t-2"} {
		r.NextIn(ns)
		r.Append(journal.Entry{Provider: "exa", Namespace: ns, Seq: 1})
	}

	r.ResetIn("t-1")

	if got := r.SnapshotIn("t-1"); len(got) != 0 {
		t.Errorf("SnapshotIn(t-1) after ResetIn = %+v, want empty", got)
	}
	if got := r.NextIn("t-1"); got != 1 {
		t.Errorf("NextIn(t-1) after ResetIn = %d, want the sequence back at 1", got)
	}
	if got := seqsOf(r.SnapshotIn("t-2")); !slices.Equal(got, []uint64{1}) {
		t.Errorf("SnapshotIn(t-2) = %v, want the other namespace untouched", got)
	}
	if got := r.NextIn("t-2"); got != 2 {
		t.Errorf("NextIn(t-2) = %d, want the other namespace's sequence to carry on", got)
	}
}

// TestRing_ResetDropsEveryNamespace is the explicit "everything" reset. It is
// the verbose form precisely because a bare reset that wiped a hundred
// concurrent tests' journals is a trap.
func TestRing_ResetDropsEveryNamespace(t *testing.T) {
	t.Parallel()

	r := journal.NewRing(4, bigBody)
	for _, ns := range []string{"t-1", "t-2", ""} {
		r.NextIn(ns)
		r.Append(journal.Entry{Provider: "exa", Namespace: ns, Seq: 1})
	}

	r.Reset()

	if got := r.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot() after Reset = %+v, want empty", got)
	}
	if got := r.Namespaces(); len(got) != 0 {
		t.Errorf("Namespaces() after Reset = %v, want no live lanes", got)
	}
	if got := r.Stats(); got != (journal.Stats{Capacity: 4}) {
		t.Errorf("Stats() after Reset = %+v, want every counter zeroed", got)
	}
	for _, ns := range []string{"t-1", "t-2", ""} {
		if got := r.NextIn(ns); got != 1 {
			t.Errorf("NextIn(%q) after Reset = %d, want 1", ns, got)
		}
	}
}

// TestRing_RedactsInEveryNamespace is the namespace-shaped restatement of the
// house rule: a namespace is a state boundary, and it does not move the storage
// boundary redaction is enforced at. A lane created implicitly by a request must
// not be a lane where redaction was never wired.
func TestRing_RedactsInEveryNamespace(t *testing.T) {
	t.Parallel()

	namespaces := []string{"", "default", "t-1", "t-2"}

	r := journal.NewRing(4, bigBody)
	for _, ns := range namespaces {
		e := entryWithCredentialEverywhere()
		e.Namespace = ns
		r.Append(e)
	}

	for _, ns := range namespaces {
		t.Run("namespace="+ns, func(t *testing.T) {
			got := r.SnapshotIn(ns)
			if len(got) == 0 {
				t.Fatalf("SnapshotIn(%q) retained nothing", ns)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("Marshal(snapshot) = %v", err)
			}
			assertMasked(t, "snapshot of "+ns, string(encoded), secret)
		})
	}

	encoded, err := json.Marshal(r.Snapshot())
	if err != nil {
		t.Fatalf("Marshal(snapshot) = %v", err)
	}
	assertMasked(t, "unfiltered snapshot", string(encoded), secret)
}

// TestRing_ConcurrentNamespaces is the -race test for the shared-container case
// this feature exists for: many namespaces appending at once while readers scan
// the journal. The per-lane assertions afterwards are exact, because one writer
// per lane means a lane's sequence order and its append order agree.
func TestRing_ConcurrentNamespaces(t *testing.T) {
	t.Parallel()

	const (
		namespaces = 8
		perGoro    = 50
		capacity   = 16
		readers    = 4
	)
	r := journal.NewRing(capacity, 128)

	var wg sync.WaitGroup
	start := make(chan struct{})

	for n := range namespaces {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ns := fmt.Sprintf("t-%d", n)
			<-start
			for i := range perGoro {
				seq := r.NextIn(ns)
				r.Append(journal.Entry{
					Provider:  "exa",
					Namespace: ns,
					Seq:       seq,
					Headers:   map[string][]string{"Authorization": {"Bearer " + secret}},
					Body:      json.RawMessage(fmt.Sprintf(`{"api_key":%q,"i":%d}`, secret, i)),
				})
			}
		}()
	}
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range perGoro {
				for _, e := range r.Snapshot() {
					if strings.Contains(string(e.Body), secret) {
						t.Errorf("credential observed in a concurrent snapshot: %s", e.Body)
					}
				}
				ns := fmt.Sprintf("t-%d", i%namespaces)
				for _, e := range r.SnapshotIn(ns) {
					if e.Namespace != ns {
						t.Errorf("SnapshotIn(%q) returned an entry from %q", ns, e.Namespace)
					}
					if strings.Contains(string(e.Body), secret) {
						t.Errorf("credential observed in a concurrent namespace snapshot: %s", e.Body)
					}
				}
				_ = r.Stats()
				_ = r.Namespaces()
			}
		}()
	}

	close(start)
	wg.Wait()

	// Every lane ran its own sequence to perGoro and retained its own last
	// capacity entries: no lane's numbering or retention was disturbed by the
	// seven lanes appending alongside it.
	want := make([]uint64, 0, capacity)
	for seq := uint64(perGoro - capacity + 1); seq <= perGoro; seq++ {
		want = append(want, seq)
	}
	for n := range namespaces {
		ns := fmt.Sprintf("t-%d", n)
		if got := seqsOf(r.SnapshotIn(ns)); !slices.Equal(got, want) {
			t.Errorf("SnapshotIn(%q) seqs = %v, want %v", ns, got, want)
		}
		wantStats := journal.Stats{Capacity: capacity, Stored: capacity, Appended: perGoro, Dropped: perGoro - capacity}
		if got := r.StatsIn(ns); got != wantStats {
			t.Errorf("StatsIn(%q) = %+v, want %+v", ns, got, wantStats)
		}
	}
	if got := len(r.Snapshot()); got != namespaces*capacity {
		t.Errorf("Snapshot() holds %d entries, want %d: retention is capacity per namespace", got, namespaces*capacity)
	}
}

// plainJournal is a Journal with no namespace support, which is what a consumer
// wiring its own implementation through testkit.Journal has. The helpers must
// degrade honestly against it rather than assuming a Ring.
type plainJournal struct {
	seq     uint64
	entries []journal.Entry
	resets  int
}

func (j *plainJournal) Next() uint64 { j.seq++; return j.seq }

func (j *plainJournal) Append(e journal.Entry) { j.entries = append(j.entries, journal.Redact(e)) }

func (j *plainJournal) Snapshot() []journal.Entry { return j.entries }

func (j *plainJournal) Reset() { j.entries, j.seq, j.resets = nil, 0, j.resets+1 }

func (j *plainJournal) Stats() journal.Stats { return journal.Stats{Stored: len(j.entries)} }

// TestHelpers_UseNamespacesWhenTheJournalHasThem keeps the two paths through the
// package helpers honest: a Ring isolates, and anything else degrades in the one
// direction that cannot lose another test's data.
func TestHelpers_UseNamespacesWhenTheJournalHasThem(t *testing.T) {
	t.Parallel()

	t.Run("namespaced journal", func(t *testing.T) {
		t.Parallel()

		var j journal.Journal = journal.NewRing(4, bigBody)
		if got := journal.NextIn(j, "t-1"); got != 1 {
			t.Errorf("NextIn = %d, want 1", got)
		}
		if got := journal.NextIn(j, "t-2"); got != 1 {
			t.Errorf("NextIn in a second namespace = %d, want its own sequence", got)
		}
		j.Append(journal.Entry{Provider: "exa", Namespace: "t-1", Seq: 1})
		j.Append(journal.Entry{Provider: "exa", Namespace: "t-2", Seq: 1})

		if got := seqsOf(journal.SnapshotIn(j, "t-1")); !slices.Equal(got, []uint64{1}) {
			t.Errorf("SnapshotIn = %v, want one entry", got)
		}
		if !journal.ResetIn(j, "t-1") {
			t.Error("ResetIn = false for a Ring, want true")
		}
		if got := len(journal.SnapshotIn(j, "t-2")); got != 1 {
			t.Errorf("the other namespace holds %d entries after ResetIn, want 1", got)
		}
	})

	t.Run("plain journal", func(t *testing.T) {
		t.Parallel()

		j := &plainJournal{}
		if first, second := journal.NextIn(j, "t-1"), journal.NextIn(j, "t-2"); first != 1 || second != 2 {
			t.Errorf("NextIn = %d then %d, want the one shared sequence a plain journal has", first, second)
		}
		j.Append(journal.Entry{Provider: "exa", Namespace: "t-1", Seq: 1})
		j.Append(journal.Entry{Provider: "exa", Seq: 2})

		if got := seqsOf(journal.SnapshotIn(j, "t-1")); !slices.Equal(got, []uint64{1}) {
			t.Errorf("SnapshotIn = %v, want the entries filtered on Namespace", got)
		}
		if got := seqsOf(journal.SnapshotIn(j, "default")); !slices.Equal(got, []uint64{2}) {
			t.Errorf("SnapshotIn(default) = %v, want the entry recorded without a namespace", got)
		}

		// The one thing that must never happen: asking to drop one namespace and
		// getting every namespace dropped instead.
		if journal.ResetIn(j, "t-1") {
			t.Error("ResetIn = true for a journal that cannot scope a reset")
		}
		if j.resets != 0 {
			t.Errorf("Reset was called %d times, want a scoped reset never to wipe the whole journal", j.resets)
		}
	})
}

// TestRing_ConcurrentUse is a -race test: every exported method is exercised
// from many goroutines at once, with no sleeps and no ordering assumptions.
func TestRing_ConcurrentUse(t *testing.T) {
	t.Parallel()

	const (
		writers = 8
		perGoro = 50
	)
	r := journal.NewRing(16, 128)

	var wg sync.WaitGroup
	start := make(chan struct{})

	seqs := make([][]uint64, writers)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range perGoro {
				seq := r.Next()
				seqs[w] = append(seqs[w], seq)
				r.Append(journal.Entry{
					Provider: "exa",
					Seq:      seq,
					Headers:  map[string][]string{"Authorization": {"Bearer " + secret}},
					Body:     json.RawMessage(fmt.Sprintf(`{"api_key":%q,"i":%d}`, secret, i)),
				})
			}
		}()
	}
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range perGoro {
				for _, e := range r.Snapshot() {
					if strings.Contains(string(e.Body), secret) {
						t.Errorf("credential observed in a concurrent snapshot: %s", e.Body)
					}
				}
				_ = r.Stats()
			}
		}()
	}

	close(start)
	wg.Wait()

	// Every sequence number is claimed exactly once, which is the only ordering
	// guarantee Next makes.
	seen := make(map[uint64]bool, writers*perGoro)
	for _, claimed := range seqs {
		for _, seq := range claimed {
			if seen[seq] {
				t.Fatalf("sequence %d was claimed twice", seq)
			}
			seen[seq] = true
		}
	}
	if len(seen) != writers*perGoro {
		t.Fatalf("claimed %d distinct sequence numbers, want %d", len(seen), writers*perGoro)
	}

	if got := r.Stats(); got.Appended != writers*perGoro || got.Stored != 16 {
		t.Errorf("Stats() = %+v, want appended %d and stored 16", got, writers*perGoro)
	}
}

// TestRing_MaxNamespacesExemptsTheDefaultLane pins the count the bound is over.
//
// The default lane is not created by a request naming a namespace, so charging
// it against --max-namespaces would put this store one namespace out of step
// with the fault engine, which exempts it. That disagreement has a direction
// that matters: the engine would admit the namespace and count its cursors while
// this store refused its entries, and a journal refusal carries no log line, so
// a live test would lose every request it made with nothing anywhere saying why.
func TestRing_MaxNamespacesExemptsTheDefaultLane(t *testing.T) {
	t.Parallel()

	r := journal.NewRingWithLimits(journal.Limits{Capacity: 4, MaxBodyBytes: bigBody, MaxNamespaces: 2})

	// The default lane first, so it would consume budget if it were counted.
	if got := r.Next(); got != 1 {
		t.Fatalf("Next() = %d, want 1", got)
	}
	for _, ns := range []string{"t-1", "t-2"} {
		if got := r.NextIn(ns); got != 1 {
			t.Errorf("NextIn(%q) = %d, want 1: the default lane must not spend namespace budget", ns, got)
		}
	}
	if got := r.NextIn("t-3"); got != 0 {
		t.Errorf("NextIn(t-3) = %d, want 0: two named namespaces is the whole bound", got)
	}
	if got := r.Namespaces(); !slices.Equal(got, []string{"default", "t-1", "t-2"}) {
		t.Errorf("Namespaces() = %v, want the default lane alongside both named ones", got)
	}

	// A bound of one is a usable configuration rather than a simulator that
	// refuses every namespace it is given.
	one := journal.NewRingWithLimits(journal.Limits{Capacity: 4, MaxBodyBytes: bigBody, MaxNamespaces: 1})
	if got := one.Next(); got != 1 {
		t.Fatalf("Next() on a bound of one = %d, want 1", got)
	}
	if got := one.NextIn("t-1"); got != 1 {
		t.Errorf("NextIn(t-1) on a bound of one = %d, want 1", got)
	}
	if got := one.NextIn("t-2"); got != 0 {
		t.Errorf("NextIn(t-2) on a bound of one = %d, want 0", got)
	}
}

// TestRing_ResetInReturnsTheNamespaceToTheBudget is why ResetIn drops the lane
// rather than emptying it. A shared container that runs more tests than the
// bound allows depends on a finished test's cleanup freeing its slot; a lane
// kept as an empty husk would exhaust the bound with dead state and refuse every
// later test.
func TestRing_ResetInReturnsTheNamespaceToTheBudget(t *testing.T) {
	t.Parallel()

	r := journal.NewRingWithLimits(journal.Limits{Capacity: 4, MaxBodyBytes: bigBody, MaxNamespaces: 2})
	for _, ns := range []string{"t-1", "t-2"} {
		r.Append(journal.Entry{Provider: "exa", Namespace: ns, Seq: r.NextIn(ns)})
	}
	if got := r.NextIn("t-3"); got != 0 {
		t.Fatalf("NextIn(t-3) = %d, want 0 at the bound", got)
	}

	r.ResetIn("t-1")

	if got := r.NextIn("t-3"); got != 1 {
		t.Errorf("NextIn(t-3) after t-1 was dropped = %d, want 1: the slot was returned", got)
	}
	if got := seqsOf(r.SnapshotIn("t-2")); !slices.Equal(got, []uint64{1}) {
		t.Errorf("SnapshotIn(t-2) = %v, want the untouched neighbour's entry", got)
	}
}
