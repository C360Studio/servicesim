package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/config"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// acmeCredentialProfile mirrors testkit's own hand-built fixture (see
// testkit/server_test.go's acmeProfile and testkit/credentialnames_test.go):
// a fifteen-line profile whose CredentialNames name a header and a JSON
// property no reference profile uses, over a value not shaped like any of
// internal/redact's own vendor-key patterns, so masking here is
// attributable only to Profile.CredentialNames.
func acmeCredentialProfile() provider.Profile {
	return provider.Profile{
		Name:    "acme-cred",
		Title:   "Acme (credential names fixture)",
		Summary: "proves refusalHandler's Deps sees Set.CredentialNames() too",
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

// TestRefusalHandlerMasksSetCredentialNames covers the second Deps
// construction site Phase 10 unit 7 threads (*provider.Set).CredentialNames()
// into: refusalHandler's own Deps, built once per listener at startup for the
// unknown-scenario refusal path (listeners.go), separate from the per-scenario
// Deps [Server.add] builds. A request that never resolves any scenario still
// carries whatever headers and body a real client sent, so it needs the same
// redaction vocabulary as an ordinary request — this hits that exact path (an
// unregistered scenario name) with the fixture's credential header and body
// property, and checks both the admin journal and the structured log line.
func TestRefusalHandlerMasksSetCredentialNames(t *testing.T) {
	t.Parallel()

	set, err := provider.NewSet(acmeCredentialProfile())
	require.NoError(t, err)
	cfg, err := config.Load(set, []string{
		"--bind-address", "127.0.0.1",
		"--admin-port", "0",
		"--acme-cred-port", "0",
		"--shutdown-grace", "10s",
	}, nil)
	require.NoError(t, err)

	var logs logBuffer
	h := start(t, cfg, NewLogger(cfg, &logs))

	const rawValue = "not-vendor-shaped-1234567890"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://"+h.Addr("acme-cred")+"/x/nope/v1/answer", strings.NewReader(`{"acme_token":"`+rawValue+`"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Acme-Key", rawValue)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "an unregistered scenario name must fail closed")

	// The admin journal (storage boundary, Ring.Append) must not carry the
	// raw value.
	status, journalBody := get(t, h.Addr(SurfaceAdmin), "/__admin/requests")
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, string(journalBody), rawValue,
		"the raw acme_token/x-acme-key value must not reach the admin journal")
	require.Contains(t, string(journalBody), "[REDACTED]")

	// The structured log line (Handle-time redaction, before logRequest) must
	// not carry it either — this is refusalHandler's own Deps.CredentialNames,
	// not Server.add's, since this request never resolved a scenario.
	require.NotContains(t, logs.String(), rawValue,
		"the raw acme_token/x-acme-key value must not reach the structured log")
}
