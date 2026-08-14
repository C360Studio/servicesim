package journal_test

import (
	"encoding/json"
	"fmt"
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
