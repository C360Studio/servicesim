package testkit_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/testkit"
)

// acmeCredentialProfile is a hand-built profile declaring CredentialNames —
// house rule 4's vendor-declared vocabulary — over a name and a header no
// in-tree vendor uses and a value not shaped like any of them
// (vendorKeyPattern's sk-/pplx-/tvly- prefixes never match it), so masking
// can only be explained by the vocabulary Profile.CredentialNames declares,
// never by internal/redact's own fixed tables.
func acmeCredentialProfile() provider.Profile {
	return provider.Profile{
		Name:    "acme-cred",
		Title:   "Acme (credential names fixture)",
		Summary: "proves Profile.CredentialNames reaches the journal",
		Handlers: map[string]provider.Handler{
			"POST /v1/answer": func(_ *provider.Exchange) provider.Response {
				return provider.Response{Status: http.StatusOK, Body: []byte(`{"ok":true}`), Label: "acme-cred.ok"}
			},
		},
		Routes: []provider.Route{{
			Pattern:  "POST /v1/answer",
			FaultKey: "acme-cred:answer",
		}},
		ErrorBody:       func(provider.Refusal) []byte { return []byte(`{"error":"acme refused"}`) },
		DefaultAuth:     scenario.AuthOptional,
		CredentialNames: []string{"x-acme-key", "acme_token"},
	}
}

// TestProfileCredentialNamesAreMaskedThroughStart is Phase 10 unit 7's
// end-to-end composition proof: (*provider.Set).CredentialNames() reaches
// both redaction points testkit.Start wires — provider.Deps.CredentialNames
// (Handle, before logging) and the Ring's own Limits.CredentialNames
// (Append, at storage) — with no change to internal/redact's tables and no
// redaction code in the profile itself.
func TestProfileCredentialNamesAreMaskedThroughStart(t *testing.T) {
	t.Parallel()

	const value = "not-vendor-shaped-1234"
	sim := testkit.Start(t, testkit.WithProfiles(acmeCredentialProfile()), testkit.WithBuiltin("happy"))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		sim.URL("acme-cred")+"/v1/answer", strings.NewReader(`{"acme_token":"`+value+`"}`))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Acme-Key", value)

	resp, err := sim.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/answer: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	entries := sim.AwaitRequests(t, "acme-cred", 1)
	entry := entries[0]

	if strings.Contains(string(entry.Body), value) {
		t.Fatalf("journal body carries the raw acme_token value: %s", entry.Body)
	}
	if !strings.Contains(string(entry.Body), "[REDACTED]") {
		t.Fatalf("journal body was not masked at all: %s", entry.Body)
	}
	if got := http.Header(entry.Headers).Get("X-Acme-Key"); got != "[REDACTED]" {
		t.Fatalf("journal X-Acme-Key header = %q, want [REDACTED]", got)
	}

	// sim.Journal() is the unfiltered admin view (the in-process form of
	// GET /__admin/requests): it must read the same already-masked storage,
	// not a copy that only the per-provider view happened to redact.
	for _, e := range sim.Journal() {
		if strings.Contains(string(e.Body), value) {
			t.Fatalf("sim.Journal() carries the raw acme_token value: %s", e.Body)
		}
	}
}
