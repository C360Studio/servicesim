package journal_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/internal/redact"
)

// secret is the literal that must not survive into anything the journal
// retains. It carries a vendor prefix and digits so redact.String recognises it
// even where no credential-shaped name accompanies it — which is what makes a
// field the journal forgot to redact fail these tests loudly.
const secret = "sk-live-SUPERSECRET1234567890"

// assertMasked fails when leaked survives in got, or when nothing was masked at
// all — a field that silently dropped its content would otherwise pass.
func assertMasked(t *testing.T, field, got, leaked string) {
	t.Helper()
	if strings.Contains(got, leaked) {
		t.Fatalf("%s: credential survived redaction\n got: %s\nleak: %s", field, got, leaked)
	}
	if !strings.Contains(got, redact.Mask) {
		t.Fatalf("%s: no mask in output, so nothing was redacted\ngot: %s", field, got)
	}
}

// entryWithCredentialEverywhere returns an entry carrying the same credential in
// every field a request can put one in.
func entryWithCredentialEverywhere() journal.Entry {
	return journal.Entry{
		Provider:  "exa",
		Namespace: "default",
		Seq:       1,
		Method:    "POST",
		Path:      "/search/" + secret,
		Query:     "api_key=" + secret + "&redirect=https://user:" + secret + "@host.test/x",
		Route:     "POST /search",
		Headers: map[string][]string{
			"Authorization": {"Bearer " + secret},
			"X-Exa-Api-Key": {secret},
			"X-Trace":       {"trace of " + secret},
		},
		Body:           json.RawMessage(`{"query":"go","auth":{"api_key":"` + secret + `"},"notes":["` + secret + `"]}`),
		BodyParseError: "invalid character in api_key=" + secret,
		Auth: journal.AuthObservation{
			Present: true, Header: "authorization", Scheme: "Bearer",
			Fingerprint: redact.Fingerprint(secret),
		},
		Findings: []journal.Finding{
			{Severity: journal.SeverityWarning, Code: "exa.auth.in_body", Field: "api_key",
				Message: "credential quoted verbatim: api_key=" + secret},
			{Severity: journal.SeverityError, Code: "exa.query.missing", Field: "query",
				Message: "query is required"},
		},
	}
}

// TestRedact_MasksEveryTextField covers each field individually, because the
// fields an implementer forgets are the query string and the finding message,
// not the header.
func TestRedact_MasksEveryTextField(t *testing.T) {
	t.Parallel()

	got := journal.Redact(entryWithCredentialEverywhere())

	tests := []struct {
		field string
		got   string
	}{
		{"path", got.Path},
		{"query", got.Query},
		{"headers.authorization", strings.Join(got.Headers["Authorization"], ",")},
		{"headers.x-exa-api-key", strings.Join(got.Headers["X-Exa-Api-Key"], ",")},
		{"headers.x-trace", strings.Join(got.Headers["X-Trace"], ",")},
		{"body", string(got.Body)},
		{"body_parse_error", got.BodyParseError},
		{"findings[0].message", got.Findings[0].Message},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			assertMasked(t, tt.field, tt.got, secret)
		})
	}
}

// TestRedact_PreservesEvidence checks that redaction masks values without
// destroying the placement evidence an adapter contract test asserts on.
func TestRedact_PreservesEvidence(t *testing.T) {
	t.Parallel()

	got := journal.Redact(entryWithCredentialEverywhere())

	if want := "Bearer " + redact.Mask; got.Headers["Authorization"][0] != want {
		t.Errorf("Authorization = %q, want %q: the scheme is the evidence", got.Headers["Authorization"][0], want)
	}
	if !strings.Contains(got.Query, "api_key=") {
		t.Errorf("query = %q, want the parameter name preserved", got.Query)
	}
	if got.Auth.Fingerprint != redact.Fingerprint(secret) {
		t.Errorf("Auth.Fingerprint = %q, want it untouched: it is not a credential", got.Auth.Fingerprint)
	}
	if got.Findings[1].Message != "query is required" {
		t.Errorf("findings[1].Message = %q, want free text that quotes nothing to survive", got.Findings[1].Message)
	}
	if got.Findings[0].Code != "exa.auth.in_body" || got.Findings[0].Field != "api_key" {
		t.Errorf("finding code/field = %q/%q, want them untouched", got.Findings[0].Code, got.Findings[0].Field)
	}
}

// TestRedact_IsIdempotent is what allows provider.Handle to redact where the
// entry is built and Append to redact again where it is stored.
func TestRedact_IsIdempotent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry journal.Entry
	}{
		{"credential in every field", entryWithCredentialEverywhere()},
		{"non-JSON body", journal.Entry{
			Body:  json.RawMessage("api_key=" + secret + "&query=go"),
			Query: "%zz&api_key=" + secret,
		}},
		{"zero entry", journal.Entry{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			once := journal.Redact(tt.entry)
			twice := journal.Redact(once)
			if !reflect.DeepEqual(once, twice) {
				t.Errorf("second pass changed the entry:\n once: %+v\ntwice: %+v", once, twice)
			}
		})
	}
}

// TestRedact_DoesNotMutateItsArgument matters because provider.Handle passes an
// entry whose Headers map is the live r.Header.
func TestRedact_DoesNotMutateItsArgument(t *testing.T) {
	t.Parallel()

	in := entryWithCredentialEverywhere()
	body := append(json.RawMessage(nil), in.Body...)

	_ = journal.Redact(in)

	if got := in.Headers["Authorization"][0]; got != "Bearer "+secret {
		t.Errorf("caller's header map was mutated: %q", got)
	}
	if got := in.Findings[0].Message; !strings.Contains(got, secret) {
		t.Errorf("caller's findings slice was mutated: %q", got)
	}
	if string(in.Body) != string(body) {
		t.Errorf("caller's body slice was mutated: %q", in.Body)
	}
}

// TestRedact_NilHeadersStayNil keeps an entry that never carried headers from
// growing an empty map, which would change the admin API's JSON.
func TestRedact_NilHeadersStayNil(t *testing.T) {
	t.Parallel()

	if got := journal.Redact(journal.Entry{}); got.Headers != nil {
		t.Errorf("Headers = %v, want nil", got.Headers)
	}
}

func TestEntry_WarningsAndErrors(t *testing.T) {
	t.Parallel()

	e := journal.Entry{Findings: []journal.Finding{
		{Severity: journal.SeverityWarning, Code: "a"},
		{Severity: journal.SeverityError, Code: "b"},
		{Severity: journal.SeverityWarning, Code: "c"},
	}}

	warnings := e.Warnings()
	if len(warnings) != 2 || warnings[0].Code != "a" || warnings[1].Code != "c" {
		t.Errorf("Warnings() = %+v, want a and c in declaration order", warnings)
	}
	errs := e.Errors()
	if len(errs) != 1 || errs[0].Code != "b" {
		t.Errorf("Errors() = %+v, want b", errs)
	}
	if got := (journal.Entry{}).Warnings(); got != nil {
		t.Errorf("Warnings() on an entry with no findings = %+v, want nil", got)
	}
}
