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
