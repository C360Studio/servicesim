package testkit_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenario"
	"github.com/c360studio/servicesim/testkit"
)

// kindAnswerHandler renders which scenario entry THIS request resolved to.
// Route.Entry is empty on both listeners below, so Exchange.Entry() resolves
// through the listener's own Name (Route.Entry's own doc comment) — proving
// a scenario block is addressed by Name, not by the shared Kind
// (docs/proposals/framework-seam.md, "Kind").
func kindAnswerHandler(x *provider.Exchange) provider.Response {
	e := x.Entry()
	body, _ := json.Marshal(map[string]any{"resolved_entry": e.Name})
	return provider.Response{Status: http.StatusOK, Body: body, Label: "acme.ok", FaultEligible: true}
}

// acmeKindProfile is a hand-built, single-entry profile — the shape unit 8's
// spec names: one Profile() a caller registers twice with Name/Port/Kind
// overridden, exactly as a real out-of-tree instanceable Profile() would be
// called from a consumer's main.go. Its Route.Fault closure is written once
// against the shared Kind's own scenario entry ("acme"), the realistic
// authoring shape: an instanced profile's fault plan is scripted where the
// Kind's primary block lives, and both registrations read it — proving the
// COUNTER, not the plan, is what has to be independent.
func acmeKindProfile() provider.Profile {
	return provider.Profile{
		Name: "acme", Title: "Acme", Summary: "a hand-built Kind-instancing fixture",
		Handlers: map[string]provider.Handler{"POST /v1/answer": kindAnswerHandler},
		Routes: []provider.Route{{
			Pattern:  "POST /v1/answer",
			FaultKey: "acme:answer",
			Fault:    func(s *scenario.Scenario) *scenario.Fault { return provider.TurnFault(s, "acme") },
		}},
		ErrorBody:   func(provider.Refusal) []byte { return []byte(`{"error":"acme refused"}`) },
		DefaultAuth: scenario.AuthOptional,
	}
}

// kindInstancingScenario scripts "acme" (the primary, carrying a [429, {}]
// fault plan) and "acme_fallback" (the single-entry instance, kind: acme,
// unfaulted) — exactly the two blocks the spec names, each rendering a
// distinguishable turn so a request's own block is provable from its body.
const kindInstancingScenario = `
version: 1
name: kind-instancing
providers:
  acme:
    fault:
      attempts:
        - status: 429
        - {}
    respond: {turn: A}
  acme_fallback:
    kind: acme
    respond: {turn: B}
`

// TestKindInstancingThroughStart is Phase 10 unit 8's testkit-level proof:
// register Profile{Name: "acme", Kind: "acme"} and Profile{Name:
// "acme_fallback", Kind: "acme", Port: +1} from one Profile() in
// testkit.Start; requests to each listener render their own block's answer,
// by Name; a scripted 429 the primary draws does not consume the instance's
// own attempt.
func TestKindInstancingThroughStart(t *testing.T) {
	t.Parallel()

	primary := acmeKindProfile()
	fallback := acmeKindProfile()
	fallback.Name, fallback.Port, fallback.Kind = "acme_fallback", 0, "acme"

	sim := testkit.Start(t, testkit.WithProfiles(primary, fallback), testkit.WithScenarioYAML(kindInstancingScenario))

	post := func(name provider.Name) (status int, body string) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			sim.URL(name)+"/v1/answer", nil)
		if err != nil {
			t.Fatalf("building the request: %v", err)
		}
		resp, err := sim.Client().Do(req)
		if err != nil {
			t.Fatalf("POST %s /v1/answer: %v", name, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(b)
	}

	// The primary's first call draws the scripted 429 (attempt 0 of the
	// plan scripted on "acme").
	status1, _ := post("acme")
	if status1 != http.StatusTooManyRequests {
		t.Fatalf("acme call 1 status = %d, want 429", status1)
	}

	// The instance's OWN first call must ALSO see attempt 0 of that SAME
	// plan — the 429 — because its counter is independent of the primary's,
	// not attempt 1 (the plan's unfaulted second entry), which is what a
	// shared, unnamespaced counter would have handed it.
	status2, _ := post("acme_fallback")
	if status2 != http.StatusTooManyRequests {
		t.Fatalf("acme_fallback call 1 status = %d, want 429 (its own fresh budget, unaffected by acme's already-consumed attempt)",
			status2)
	}

	// The primary's second call reaches the plan's unfaulted second attempt
	// and renders its OWN block ("acme"), by Name.
	status3, body3 := post("acme")
	if status3 != http.StatusOK {
		t.Fatalf("acme call 2 status = %d, want 200", status3)
	}
	if want := `{"resolved_entry":"acme"}`; body3 != want {
		t.Fatalf("acme call 2 body = %s, want %s (its own block, by Name)", body3, want)
	}

	// The instance's second call likewise reaches its own unfaulted second
	// attempt and renders ITS OWN block ("acme_fallback"), by Name — proving
	// entry resolution is unaffected by the shared Kind.
	status4, body4 := post("acme_fallback")
	if status4 != http.StatusOK {
		t.Fatalf("acme_fallback call 2 status = %d, want 200", status4)
	}
	if want := `{"resolved_entry":"acme_fallback"}`; body4 != want {
		t.Fatalf("acme_fallback call 2 body = %s, want %s (ITS OWN block, not the primary's)", body4, want)
	}

	// The journal names which namespaced key each listener actually drew
	// on, proving the two counters really were different ones.
	acmeEntries := sim.AwaitRequests(t, "acme", 2)
	fallbackEntries := sim.AwaitRequests(t, "acme_fallback", 2)

	if got := acmeEntries[0].Outcome.FaultKey; got != "acme:answer" {
		t.Fatalf("acme entry 0 fault_key = %q, want %q (Kind == Name: unnamespaced)", got, "acme:answer")
	}
	if got := fallbackEntries[0].Outcome.FaultKey; got != "acme_fallback:acme:answer" {
		t.Fatalf("acme_fallback entry 0 fault_key = %q, want %q (Kind != Name: namespaced by its own Name)",
			got, "acme_fallback:acme:answer")
	}
}
