package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/scenario"
)

// --- (*Set).Routes: FaultKey namespacing (Phase 10 unit 8) ------------------

// TestSetRoutesNamespacesFaultKeyOnlyWhenInstancing proves the registration
// half of the unit 8 contract: a profile whose Kind is "" or equals its own
// Name — every profile registered before Kind existed, and all four
// reference profiles today — gets its Route.FaultKey back verbatim, while an
// instanced profile (Kind != Name) gets it namespaced by its OWN Name, not
// by the Kind it shares with its primary.
func TestSetRoutesNamespacesFaultKeyOnlyWhenInstancing(t *testing.T) {
	t.Parallel()

	primary := acmeProfile(okHandler(`{}`))

	fallback := acmeProfile(okHandler(`{}`))
	fallback.Name, fallback.Port, fallback.Kind = "acme-fallback", 9097, "acme"

	s := MustSet(primary, fallback)
	got := s.Routes()
	require.Len(t, got, 2)
	require.Equal(t, "acme:answer", got[0].FaultKey,
		"Kind == Name (the empty-Kind default): FaultKey is untouched")
	require.Equal(t, "acme-fallback:acme:answer", got[1].FaultKey,
		"Kind != Name: FaultKey is namespaced by the INSTANCE's own Name, not by the shared Kind")
}

// TestNamespacedFaultKeyIsANoOpWhenKindIsEmptyOrEqualsName is the unit test
// of namespacedFaultKey's own three no-op guards, isolated from Set.Routes so
// a future caller of the helper inherits a test that pins its contract
// directly.
func TestNamespacedFaultKeyIsANoOpWhenKindIsEmptyOrEqualsName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "acme:answer", namespacedFaultKey("acme", "", "acme:answer"),
		"an empty Kind means Name; nothing to namespace")
	require.Equal(t, "acme:answer", namespacedFaultKey("acme", "acme", "acme:answer"),
		"Kind == Name is not instancing")
	require.Equal(t, "", namespacedFaultKey("acme-fallback", "acme", ""),
		"an empty key has nothing to namespace, whatever Kind says")
	require.Equal(t, "acme-fallback:acme:answer", namespacedFaultKey("acme-fallback", "acme", "acme:answer"),
		"Kind != Name namespaces by the LISTENER's own Name")
}

// --- the fault engine: independent counters per instance --------------------

// TestFaultEngineIsolatesInstancesOfOneKind is TestNextIsolatesNamespaces's
// sibling for instancing rather than namespaces: two routes registered under
// the namespaced keys (*Set).Routes now produces for one Kind's two
// listeners, both drawing their attempts from the SAME underlying plan (the
// realistic shape: an instanced profile's Route.Fault closure is authored
// once, against the shared Kind, and copied to both registrations by
// Profile()) — proving the two counters are independent even though the plan
// is shared.
func TestFaultEngineIsolatesInstancesOfOneKind(t *testing.T) {
	t.Parallel()

	plan := &scenario.Fault{Attempts: []scenario.FaultAttempt{{Status: 429}, {}}}
	e := newSetFaultEngine(nil, []Route{
		route("POST /v1/answer", "acme:answer", plan),
		route("POST /v1/answer", "acme-fallback:acme:answer", plan),
	})

	// The primary's first call draws attempt 0 from its own key: the
	// scripted 429.
	primary := e.Next("acme:answer")
	require.Equal(t, 0, primary.Index)
	require.NotNil(t, primary.Attempt)
	require.Equal(t, 429, primary.Attempt.Status)

	// The instance's OWN first call must ALSO draw attempt 0 — its own
	// fresh budget — not attempt 1, which is what the primary's call would
	// have left it with had the two shared one counter under one
	// unnamespaced key (the pre-unit-8 shape Profile.Kind's own doc comment
	// used to warn about).
	fallback := e.Next("acme-fallback:acme:answer")
	require.Equal(t, 0, fallback.Index,
		"the instance draws from its own budget, unaffected by the primary's already-consumed attempt")
	require.NotNil(t, fallback.Attempt)
	require.Equal(t, 429, fallback.Attempt.Status)

	// And the primary's own budget is likewise untouched by the instance's
	// call: its second call now sees the plan's second, unfaulted attempt
	// (a real, non-nil *FaultAttempt — the plan declares two explicit
	// attempts, "- {status: 429}" then "- {}" — whose zero Status means "no
	// fault", not "no attempt"; Attempt is nil only past the end of a plan
	// with no repeat_last).
	primary2 := e.Next("acme:answer")
	require.Equal(t, 1, primary2.Index)
	require.NotNil(t, primary2.Attempt)
	require.Equal(t, 0, primary2.Attempt.Status, "the plan's second attempt is unfaulted")
}

// --- end to end over HTTP: one handler, two listeners, one Set --------------

// kindInstancingScenario scripts two blocks: "acme" (the primary, carrying a
// [429, {}] fault plan so the counter-isolation proof has something to
// observe) and "acme-fallback" (the instance, unfaulted, addressed by its
// own Name exactly as scenario.ProviderEntry.Name documents — "a scenario
// block is addressed by Name; Kind selects the handler and the validator").
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
  acme-fallback:
    kind: acme
    respond: {turn: B}
`

// acmeKindFault is the Route.Fault closure both the primary and the
// instance's routes share, written once against the Kind's own scenario
// entry ("acme") — the realistic authoring shape a shared Profile()
// produces: one closure, copied by value to both registrations, exactly as
// every in-tree profile's own Route.Fault (profiles/*/handler.go) is a
// closure over its own static entry name.
func acmeKindFault(s *scenario.Scenario) *scenario.Fault { return TurnFault(s, "acme") }

// answerHandler renders which scenario entry THIS request resolved to,
// through Exchange.Entry() — Route.Entry is empty on both listeners below,
// so it resolves through the listener's own Name (x.Provider), exactly as
// TestExchangeEntryForOnAnInstancedListenerResolvesToItsOwnBlock already
// proves for EntryFor. FaultEligible is set so the shared fault plan above
// actually gets to apply.
func answerHandler(x *Exchange) Response {
	e := x.Entry()
	body, _ := json.Marshal(map[string]any{"resolved_entry": e.Name})
	return Response{Status: http.StatusOK, Body: body, Label: "acme.ok", FaultEligible: true}
}

// kindInstancedProfiles returns the primary ("acme") and its single-entry
// instance ("acme-fallback", Kind "acme") — the shape
// docs/proposals/framework-seam.md's "Kind" discussion and this unit's spec
// both name: one Profile() (here, one literal shared between two Profile
// values, which is what a real out-of-tree Profile() function does by
// returning the same struct twice with Name/Port/Kind overridden by the
// caller), registered twice.
func kindInstancedProfiles() (primary, fallback Profile) {
	routes := []Route{{Pattern: "POST /v1/answer", FaultKey: "acme:answer", Fault: acmeKindFault}}
	handlers := map[string]Handler{"POST /v1/answer": answerHandler}

	primary = Profile{
		Name: "acme", Title: "Acme", Summary: "a hand-built instancing fixture",
		Handlers: handlers, Routes: routes,
		ErrorBody: fixedErrorBody(`{"error":"acme refused"}`), DefaultAuth: scenario.AuthOptional,
	}
	fallback = primary
	fallback.Name, fallback.Port, fallback.Kind = "acme-fallback", 9098, "acme"
	return primary, fallback
}

// TestKindInstancingServesOwnBlocksAndIndependentFaultBudgets is the unit 8
// instancing proof over real HTTP, on one Set, one shared journal and one
// shared fault engine — exactly how internal/server's Server.add and
// testkit's build wire a scenario's listeners today: requests to each
// listener render its OWN scenario block (by Name), and a scripted 429 drawn
// by the primary does not consume the instance's own attempt.
func TestKindInstancingServesOwnBlocksAndIndependentFaultBudgets(t *testing.T) {
	t.Parallel()

	primary, fallback := kindInstancedProfiles()
	set := MustSet(primary, fallback)
	sc := mustScenario(t, kindInstancingScenario)

	j := journal.NewRing(16, 4096)
	faults := set.Faults(sc)
	deps := Deps{Scenario: sc, Journal: j, Faults: faults}

	pPrimary, ok := set.Lookup("acme")
	require.True(t, ok)
	pFallback, ok := set.Lookup("acme-fallback")
	require.True(t, ok)
	hPrimary := pPrimary.Handler(deps)
	hFallback := pFallback.Handler(deps)

	post := func(h http.Handler) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))
		return w
	}

	// The primary's first call draws the scripted 429 (attempt 0 of the
	// plan both listeners' Route.Fault closures read).
	w1 := post(hPrimary)
	require.Equal(t, http.StatusTooManyRequests, w1.Code)

	// The instance's OWN first call must ALSO see attempt 0 of that SAME
	// plan — the 429 — because its counter is independent of the primary's,
	// not attempt 1 (the plan's unfaulted second entry), which is what a
	// shared, unnamespaced counter would have handed it.
	w2 := post(hFallback)
	require.Equal(t, http.StatusTooManyRequests, w2.Code,
		"the instance draws its own attempt 0, unaffected by the primary's already-consumed one")

	// The primary's second call now sees the plan's second, unfaulted
	// attempt and renders its OWN block ("acme"), by Name.
	w3 := post(hPrimary)
	require.Equal(t, http.StatusOK, w3.Code)
	require.JSONEq(t, `{"resolved_entry":"acme"}`, w3.Body.String())

	// The instance's second call likewise reaches its own unfaulted second
	// attempt and renders ITS OWN block ("acme-fallback"), by Name — proving
	// EntryFor/Entry resolution is unaffected by the shared Kind, the same
	// property TestExchangeEntryForOnAnInstancedListenerResolvesToItsOwnBlock
	// already pins at the Exchange level, now exercised end to end.
	w4 := post(hFallback)
	require.Equal(t, http.StatusOK, w4.Code)
	require.JSONEq(t, `{"resolved_entry":"acme-fallback"}`, w4.Body.String())

	// The journal names which namespaced key each listener actually drew
	// on, proving the two counters really were different ones rather than
	// merely behaving as if they were.
	entries := j.Snapshot()
	require.Len(t, entries, 4)
	require.Equal(t, "acme:answer", entries[0].Outcome.FaultKey)
	require.Equal(t, "acme-fallback:acme:answer", entries[1].Outcome.FaultKey,
		"the instance's journaled fault_key is namespaced by its own Name")
	require.Equal(t, "acme:answer", entries[2].Outcome.FaultKey)
	require.Equal(t, "acme-fallback:acme:answer", entries[3].Outcome.FaultKey)
}

// --- RouteMatches on an instanced listener's namespaced key -----------------

// TestQualifiedRoutePredicateSelectsATurnOnAnInstancedListener is the
// end-to-end (through SelectTurn, the exact call turn selection makes)
// sibling of scenario.TestRouteMatches's table cases: the ONE documented
// qualified `when.route` spelling (docs/scenario-schema.md's `<kind>:<name>`
// form) must select a turn on an instanced listener's namespaced route key,
// exactly as it does on every other listener's un-namespaced one.
func TestQualifiedRoutePredicateSelectsATurnOnAnInstancedListener(t *testing.T) {
	t.Parallel()

	e := &scenario.ProviderEntry{
		Turns: []scenario.Turn{
			{When: &scenario.Match{Route: "acme:answer"}},
			{}, // the catch-all: When == nil matches any request
		},
	}

	turn, index, err := SelectTurn(e, 0, "acme-fallback:acme:answer", nil)
	require.NoError(t, err)
	require.Equal(t, 0, index, "the qualified predicate must select the FIRST turn, not fall through to the catch-all")
	require.NotNil(t, turn)
}

// --- instanceFault: an instance's own scenario block wins ------------------

// TestInstanceFaultPrefersItsOwnScenarioBlock is the unit test of
// instanceFault's own contract: an instanced listener's own scenario block —
// addressed by its own Name, exactly as its ordinary turn content already
// is — is consulted FIRST for a fault plan, and the Kind's shared closure
// (base) runs only when the instance declares no fault of its own. Before
// this fix, every instanced route's Fault closure was copied byte-for-byte
// from the Kind's own package-level authoring (e.g. `TurnFault(s, "acme")`),
// so "acme-fallback: {kind: acme, fault: {...}}" was never read at all: the
// canonical failover shape instancing exists for — the primary fails per
// its own script, the fallback succeeds per its own, independent script —
// could not be authored.
func TestInstanceFaultPrefersItsOwnScenarioBlock(t *testing.T) {
	t.Parallel()

	const ownOnlyYAML = `
version: 1
name: instance-fault-own-only
providers:
  acme:
    fault:
      attempts: [{status: 500}]
    respond: {turn: primary}
  acme-fallback:
    kind: acme
    fault:
      attempts: [{status: 503}]
    respond: {turn: fallback}
`
	sc := mustScenario(t, ownOnlyYAML)
	base := func(s *scenario.Scenario) *scenario.Fault { return TurnFault(s, "acme") }
	wrapped := instanceFault("acme-fallback", base)

	got := wrapped(sc)
	require.NotNil(t, got)
	require.Equal(t, 503, got.Attempts[0].Status,
		"the instance's OWN fault block wins over the Kind's shared one")

	// The Kind's own primary Name is untouched by the wrapping (only
	// namespacedRoutes applies instanceFault, and only to an instanced
	// route): calling base directly still reads the shared plan.
	require.Equal(t, 500, base(sc).Attempts[0].Status)
}

// TestInstanceFaultFallsBackWhenTheInstanceDeclaresNoFaultOfItsOwn proves the
// fallback half: an instance that scripts NO fault: block of its own still
// draws the Kind's shared plan, unchanged from before this fix — the shape
// every existing instancing test already relies on (a scripted 429 on the
// Kind's primary block, read by an instance that declares nothing).
func TestInstanceFaultFallsBackWhenTheInstanceDeclaresNoFaultOfItsOwn(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, kindInstancingScenario) // "acme-fallback" declares no fault: of its own
	wrapped := instanceFault("acme-fallback", acmeKindFault)

	got := wrapped(sc)
	require.NotNil(t, got)
	require.Equal(t, 429, got.Attempts[0].Status, "falls back to the Kind's shared plan")
}

// TestInstanceFaultIsANoOpWithNoBaseAndNoOwnBlock proves instanceFault is
// nil-safe when base itself is nil (a route with no Fault closure at all)
// and the instance declares nothing either — the same "nil is fine" contract
// TurnFault documents for every other hop.
func TestInstanceFaultIsANoOpWithNoBaseAndNoOwnBlock(t *testing.T) {
	t.Parallel()

	sc := mustScenario(t, `
version: 1
name: instance-fault-nil-base
providers:
  acme-fallback:
    respond: {turn: only}
`)
	wrapped := instanceFault("acme-fallback", nil)
	require.Nil(t, wrapped(sc))
}

// TestKindInstancingHonoursTheInstanceOwnFaultBlockOverHTTP is the end-to-end
// proof, over real HTTP on one Set: the primary block scripts a 500, the
// instance's OWN block scripts a distinct 503 — the canonical failover shape
// (a caller retries the fallback after the primary fails) — and each
// listener's first call renders exactly its own scripted status, never the
// other's.
func TestKindInstancingHonoursTheInstanceOwnFaultBlockOverHTTP(t *testing.T) {
	t.Parallel()

	const ownFaultScenario = `
version: 1
name: instance-fault-http
providers:
  acme:
    fault:
      attempts: [{status: 500}]
    respond: {turn: primary}
  acme-fallback:
    kind: acme
    fault:
      attempts: [{status: 503}]
    respond: {turn: fallback}
`
	primary, fallback := kindInstancedProfiles()
	set := MustSet(primary, fallback)
	sc := mustScenario(t, ownFaultScenario)

	j := journal.NewRing(16, 4096)
	faults := set.Faults(sc)
	deps := Deps{Scenario: sc, Journal: j, Faults: faults}

	pPrimary, ok := set.Lookup("acme")
	require.True(t, ok)
	pFallback, ok := set.Lookup("acme-fallback")
	require.True(t, ok)
	hPrimary := pPrimary.Handler(deps)
	hFallback := pFallback.Handler(deps)

	post := func(h http.Handler) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))
		return w
	}

	wPrimary := post(hPrimary)
	require.Equal(t, http.StatusInternalServerError, wPrimary.Code,
		"the primary's own scripted fault fires")

	wFallback := post(hFallback)
	require.Equal(t, http.StatusServiceUnavailable, wFallback.Code,
		"the instance's OWN scripted fault fires — independent of, and different from, the primary's")
}
