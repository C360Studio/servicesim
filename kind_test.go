package servicesim

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
)

// kindInstancedSet builds the two-listener, one-Kind Set unit 8's spec
// names: one profile literal registered twice, Name/Port/Kind overridden by
// the caller — the shape a real out-of-tree Profile() function produces
// when a consumer instances it, exercised here at the composition root
// rather than only inside package provider.
func kindInstancedSet(t *testing.T) *provider.Set {
	t.Helper()
	primary := provider.Profile{
		Name: "acme", Title: "Acme", Summary: "a hand-built Kind-instancing fixture",
		Handlers: map[string]provider.Handler{
			"POST /v1/answer": func(_ *provider.Exchange) provider.Response {
				return provider.Response{Status: 200, Body: []byte(`{}`), Label: "acme.ok"}
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

// TestPrintPortsListsBothInstancesOfOneKind is unit 8's --print-ports proof:
// a consumer composing two listeners of one Kind sees both in the printed
// registry, each under its OWN Name — --print-ports (deliverable 4,
// servicesim.go's printPorts) derives from Set.All() without any notion of
// Kind, so instancing needed no change here to already work.
func TestPrintPortsListsBothInstancesOfOneKind(t *testing.T) {
	t.Parallel()

	set := kindInstancedSet(t)
	var out, errOut bytes.Buffer
	code := Run(context.Background(), testBuild(), set, []string{"--print-ports"}, noEnv, &out, &errOut)

	require.Equal(t, exitOK, code)
	require.Empty(t, errOut.String())

	type row struct {
		Name        string `json:"name"`
		Port        int    `json:"port"`
		DefaultAuth string `json:"default_auth"`
	}
	var got []row
	require.NoError(t, json.Unmarshal([]byte(out.String()), &got))
	require.Equal(t, []row{
		{Name: "acme", Port: 0, DefaultAuth: string(scenario.AuthOptional)},
		{Name: "acme_fallback", Port: 0, DefaultAuth: string(scenario.AuthOptional)},
	}, got, "both instances are listed, each under its own Name, registration order")
}
