package server

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/config"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// kindInstancedAcmeSet is unit 8's own-package fixture: one profile literal
// registered twice, Name/Port/Kind overridden by the caller — Profile{Name:
// "acme", Kind: ""} (Kind defaults to Name) and Profile{Name:
// "acme_fallback", Kind: "acme"}, single-entry, exactly the shape
// docs/proposals/framework-seam.md's "Kind" discussion names.
func kindInstancedAcmeSet(t *testing.T) *provider.Set {
	t.Helper()
	primary := provider.Profile{
		Name: "acme", Title: "Acme", Summary: "a hand-built Kind-instancing fixture",
		Handlers: map[string]provider.Handler{
			"POST /v1/answer": func(x *provider.Exchange) provider.Response {
				// A nil Entry() means this request's own block was not
				// scripted (TestUnscriptedInstanceIsWarnedByItsOwnName's
				// case): house rule 3 says a registered profile still
				// serves the well-shaped empty answer, never a refusal, so
				// this renders "resolved_entry": null rather than
				// dereferencing a nil entry.
				var resolved any
				if e := x.Entry(); e != nil {
					resolved = e.Name
				}
				body, _ := json.Marshal(map[string]any{"resolved_entry": resolved})
				return provider.Response{Status: http.StatusOK, Body: body, Label: "acme.ok"}
			},
		},
		Routes:      []provider.Route{{Pattern: "POST /v1/answer", FaultKey: "acme:answer"}},
		ErrorBody:   func(provider.Refusal) []byte { return []byte(`{"error":"acme refused"}`) },
		DefaultAuth: scenario.AuthOptional,
	}
	fallback := primary
	fallback.Name, fallback.Port, fallback.Kind = "acme_fallback", 0, "acme"

	set, err := provider.NewSet(primary, fallback)
	require.NoError(t, err)
	return set
}

// kindTestConfig resolves a Config over kindInstancedAcmeSet with every
// listener on an ephemeral port, both profiles enabled.
func kindTestConfig(t *testing.T, args ...string) config.Config {
	t.Helper()
	base := []string{
		"--bind-address", "127.0.0.1",
		"--admin-port", "0",
		"--acme-port", "0",
		"--acme_fallback-port", "0",
		"--providers", "acme,acme_fallback",
		"--shutdown-grace", "10s",
	}
	cfg, err := config.Load(kindInstancedAcmeSet(t), append(base, args...), nil)
	require.NoError(t, err)
	return cfg
}

// TestUnscriptedInstanceIsWarnedByItsOwnName proves the unit 8 half of
// codeProfileUnscripted: a scenario scripting only the primary's block
// ("acme") leaves the SINGLE-ENTRY INSTANCE ("acme_fallback", kind "acme")
// unscripted, and the warning names the instance by its own Name — not the
// Kind it shares with the primary, which is scripted and must not be
// reported as unscripted itself.
func TestUnscriptedInstanceIsWarnedByItsOwnName(t *testing.T) {
	t.Parallel()

	args := writeScenario(t, `
version: 1
name: acme-only
providers:
  acme:
    respond: {}
`)
	cfg := kindTestConfig(t, args...)
	h := start(t, cfg, discard())

	require.Equal(t, http.StatusOK, post(t, h.Addr("acme"), "/v1/answer", `{}`).StatusCode)

	status, body := get(t, h.Addr(SurfaceAdmin), "/__admin/scenario")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, string(body), codeProfileUnscripted)
	require.Contains(t, string(body), `"path":"providers.acme_fallback"`)
	require.NotContains(t, string(body), `"path":"providers.acme"`,
		"acme has a block in the scenario and must not be warned as unscripted")

	// The instance still serves the well-shaped empty answer (house rule 3:
	// registered means served, whether or not the scenario scripted a
	// block for it) rather than a refusal.
	resp := post(t, h.Addr("acme_fallback"), "/v1/answer", `{}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestValidateScenarioAcceptsAnInstanceBlockAddressedByItsOwnName proves the
// other half: a scenario naming the instance BY ITS OWN NAME with an
// explicit `kind: acme` validates against the one validator registered
// under the shared Kind — no scenario.provider.unimplemented, no
// scenario.profile.unscripted for either block — and each listener renders
// its own block, by Name, unaffected by the shared Kind.
func TestValidateScenarioAcceptsAnInstanceBlockAddressedByItsOwnName(t *testing.T) {
	t.Parallel()

	args := writeScenario(t, `
version: 1
name: acme-both
providers:
  acme:
    respond: {}
  acme_fallback:
    kind: acme
    respond: {}
`)
	cfg := kindTestConfig(t, args...)
	h := start(t, cfg, discard())

	status, body := get(t, h.Addr(SurfaceAdmin), "/__admin/scenario")
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, string(body), provider.CodeProviderUnimplemented,
		"acme_fallback: {kind: acme} must validate against the ONE registered validator, not read as unimplemented")
	require.NotContains(t, string(body), codeProfileUnscripted,
		"both blocks are scripted; neither profile is unscripted")

	respPrimary := post(t, h.Addr("acme"), "/v1/answer", `{}`)
	require.Equal(t, http.StatusOK, respPrimary.StatusCode)
	respFallback := post(t, h.Addr("acme_fallback"), "/v1/answer", `{}`)
	require.Equal(t, http.StatusOK, respFallback.StatusCode)

	primaryBody := readBody(t, respPrimary)
	fallbackBody := readBody(t, respFallback)
	require.JSONEq(t, `{"resolved_entry":"acme"}`, primaryBody, "the primary renders its own block, by Name")
	require.JSONEq(t, `{"resolved_entry":"acme_fallback"}`, fallbackBody,
		"the instance renders ITS OWN block, by Name, unaffected by the shared Kind")
}

// readBody drains and returns resp's body as a string.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}
