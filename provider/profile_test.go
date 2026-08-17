package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/scenario"
)

// --- test doubles and a fifteen-line hand-built profile ---------------------

// fixedErrorBody is the simplest possible Profile.ErrorBody: it ignores the
// Refusal entirely and always answers the same bytes, which is enough for
// every test in this file that only cares whether ErrorBody was reached, not
// what it renders per kind (see exa/tavily/perplexity/mcp's own profile.go
// for a real per-kind ErrorBody).
func fixedErrorBody(body string) func(Refusal) []byte {
	return func(_ Refusal) []byte { return []byte(body) }
}

// acmeProfile is the fifteen-line hand-built profile the panic and
// Status-guard tests are run against — the shape the out-of-tree module
// under scratchpad/u2-outoftree/ registers, minus its own package
// boundary.
func acmeProfile(h Handler) Profile {
	return Profile{
		Name:        "acme",
		Title:       "Acme",
		Summary:     "a fifteen-line test profile",
		Handlers:    map[string]Handler{"POST /v1/answer": h},
		Routes:      []Route{{Pattern: "POST /v1/answer", FaultKey: "acme:answer"}},
		ErrorBody:   fixedErrorBody(`{"error":"acme refused"}`),
		DefaultAuth: scenario.AuthOptional,
	}
}

// --- Profile.Validate / NewSet refusals -------------------------------------

func TestNewSetRefusesNilErrorBody(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.ErrorBody = nil
	_, err := NewSet(p)
	require.ErrorContains(t, err, "ErrorBody is required")
}

func TestNewSetRefusesEmptyHandlers(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.Handlers = nil
	_, err := NewSet(p)
	require.ErrorContains(t, err, "Handlers is empty")
}

func TestNewSetRefusesEmptyRoutes(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.Routes = nil
	_, err := NewSet(p)
	require.ErrorContains(t, err, "Routes is empty")
}

func TestNewSetRefusesEmptyFaultKey(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.Routes = []Route{{Pattern: "POST /v1/answer"}}
	_, err := NewSet(p)
	require.ErrorContains(t, err, "no FaultKey")
}

// TestNewSetRefusesRouteWithNoHandler proves the rule-3 hole a registration-time
// validator exists to close: a Route whose Pattern has no entry in Handlers
// would otherwise reach handle.go's noHandler at request time — a 500 with an
// EMPTY body, bypassing ErrorBody entirely — rather than being refused before
// the process ever starts serving.
func TestNewSetRefusesRouteWithNoHandler(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.Routes = append(p.Routes, Route{Pattern: "POST /v1/orphan", FaultKey: "acme:orphan"})
	_, err := NewSet(p)
	require.ErrorContains(t, err, "no entry in Handlers")
}

// TestNewSetRefusesHandlerWithNoRoute is the mirror case: a Handlers entry
// with no matching Route would be registered by NewMux against nothing (only
// Routes are walked to build the mux), so it is silently unreachable — a
// dead handler nobody notices at registration time.
func TestNewSetRefusesHandlerWithNoRoute(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.Handlers["POST /v1/unreachable"] = okHandler(`{}`)
	_, err := NewSet(p)
	require.ErrorContains(t, err, "no matching Route")
}

func TestNewSetRefusesUnsafeName(t *testing.T) {
	t.Parallel()
	tests := []string{"", "Acme", "1acme", "acme!", "acme.com", "-acme", "a very long name indeed"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := acmeProfile(okHandler(`{}`))
			p.Name = Name(name)
			_, err := NewSet(p)
			require.Error(t, err, "%q must be refused", name)
		})
	}
}

func TestNewSetAcceptsSafeNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"acme", "a", "acme-two", "acme_two", "acme2"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := acmeProfile(okHandler(`{}`))
			p.Name = Name(name)
			_, err := NewSet(p)
			require.NoError(t, err)
		})
	}
}

func TestNewSetRefusesDuplicateNames(t *testing.T) {
	t.Parallel()
	a := acmeProfile(okHandler(`{}`))
	b := acmeProfile(okHandler(`{}`))
	b.Port = 9091
	_, err := NewSet(a, b)
	require.ErrorContains(t, err, `duplicate profile name "acme"`)
}

func TestNewSetRefusesDuplicatePorts(t *testing.T) {
	t.Parallel()
	a := acmeProfile(okHandler(`{}`))
	a.Name, a.Port = "acme-a", 9090
	b := acmeProfile(okHandler(`{}`))
	b.Name, b.Port = "acme-b", 9090
	_, err := NewSet(a, b)
	require.ErrorContains(t, err, "both claim port 9090")
}

func TestNewSetAllowsSharedZeroPort(t *testing.T) {
	t.Parallel()
	// Port 0 ("ask the OS") is not a claim, so two profiles may both leave it
	// unset without colliding.
	a := acmeProfile(okHandler(`{}`))
	a.Name = "acme-a"
	b := acmeProfile(okHandler(`{}`))
	b.Name = "acme-b"
	_, err := NewSet(a, b)
	require.NoError(t, err)
}

func TestNewSetRefusesUnknownDefaultAuth(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.DefaultAuth = "reject"
	_, err := NewSet(p)
	require.ErrorContains(t, err, "DefaultAuth")
}

func TestNewSetAcceptsEveryValidDefaultAuth(t *testing.T) {
	t.Parallel()
	for _, mode := range []scenario.AuthMode{"", scenario.AuthRequired, scenario.AuthOptional} {
		t.Run(string(mode)+"(empty means required)", func(t *testing.T) {
			t.Parallel()
			p := acmeProfile(okHandler(`{}`))
			p.DefaultAuth = mode
			_, err := NewSet(p)
			require.NoError(t, err)
		})
	}
}

func TestNewSetRefusesOneEntryKindClaimedByTwoKinds(t *testing.T) {
	t.Parallel()
	a := acmeProfile(okHandler(`{}`))
	a.Name = "acme-a"
	a.Validators = map[string]Validator{"answer": stubValidator{}}
	b := acmeProfile(okHandler(`{}`))
	b.Name, b.Port = "acme-b", 9091
	b.Kind = "different-kind"
	b.Validators = map[string]Validator{"answer": stubValidator{}}

	_, err := NewSet(a, b)
	require.ErrorContains(t, err, `entry kind "answer" is claimed by both`)
}

func TestNewSetAcceptsSameKindClaimingOneEntryKindTwice(t *testing.T) {
	t.Parallel()
	// Two instances of ONE Kind sharing an entry-kind key is not the
	// ambiguity NewSet refuses: they come from one Profile() and are the
	// same function (framework-seam.md, "Kind" discussion).
	a := acmeProfile(okHandler(`{}`))
	a.Name, a.Kind = "acme-a", "acme"
	a.Validators = map[string]Validator{"acme": stubValidator{}}
	b := acmeProfile(okHandler(`{}`))
	b.Name, b.Port, b.Kind = "acme-b", 9091, "acme"
	b.Validators = map[string]Validator{"acme": stubValidator{}}

	_, err := NewSet(a, b)
	require.NoError(t, err)
}

func TestNewSetRefusesInstancingAMultiEntryProfile(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.Kind = "different-kind"
	p.Validators = map[string]Validator{"answer": stubValidator{}, "answer_extra": stubValidator{}}

	_, err := NewSet(p)
	require.ErrorContains(t, err, "instances kind")
}

func TestNewSetAcceptsInstancingASingleEntryProfile(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.Kind = "different-kind"
	p.Validators = map[string]Validator{"answer": stubValidator{}}

	_, err := NewSet(p)
	require.NoError(t, err)
}

// stubValidator satisfies Validator with no behaviour, for the NewSet tests
// above that only care about the map's keys.
type stubValidator struct{}

func (stubValidator) ValidateProjections(*scenario.Scenario, *scenario.ProviderEntry) []scenario.Finding {
	return nil
}

var _ Validator = stubValidator{}

func TestMustSetPanicsOnRefusal(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.ErrorBody = nil
	require.Panics(t, func() { MustSet(p) })
}

// TestMustSetPanicMessageHasNoDoublePrefix proves MustSet does not prepend a
// second "provider: " to a NewSet error that already starts with one: every
// NewSet/Validate failure's message is built with that prefix already, so
// MustSet must panic with the error's own text, not wrap it again.
func TestMustSetPanicMessageHasNoDoublePrefix(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.ErrorBody = nil
	defer func() {
		rec := recover()
		msg, ok := rec.(string)
		require.True(t, ok, "MustSet must panic with a string")
		require.False(t, strings.Contains(msg, "provider: provider:"),
			"MustSet must not double-prefix an error that already starts with \"provider: \": got %q", msg)
		require.True(t, strings.HasPrefix(msg, "provider: "), "got %q", msg)
	}()
	MustSet(p)
}

func TestMustSetReturnsTheSetOnSuccess(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	s := MustSet(p)
	require.Equal(t, []Name{"acme"}, s.Names())
}

// --- Set methods -------------------------------------------------------------

func TestSetAllClonesRatherThanAliases(t *testing.T) {
	t.Parallel()
	s := MustSet(acmeProfile(okHandler(`{}`)))
	got := s.All()
	got[0].Name = "mutated"
	require.Equal(t, Name("acme"), s.All()[0].Name, "All must return a clone the caller cannot corrupt")
}

// TestSetAllDeepClonesNestedFields proves the clone All returns is deep, not
// merely a fresh top-level slice: mutating a returned Profile's Routes,
// Handlers or Hosts must not reach the Set's own copy, and must not even
// reach a SECOND call to All — two independent copies, not two aliases of
// one.
func TestSetAllDeepClonesNestedFields(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.Hosts = []string{"api.acme.test"}
	s := MustSet(p)

	first := s.All()
	first[0].Routes[0].FaultKey = "MUTATED"
	first[0].Handlers["POST /injected"] = okHandler(`{}`)
	first[0].Hosts[0] = "mutated.test"

	second := s.All()
	require.Equal(t, "acme:answer", second[0].Routes[0].FaultKey,
		"mutating a Route returned by All must not reach the Set's own copy")
	require.NotContains(t, second[0].Handlers, "POST /injected",
		"mutating Handlers returned by All must not reach the Set's own copy")
	require.Equal(t, "api.acme.test", second[0].Hosts[0],
		"mutating Hosts returned by All must not reach the Set's own copy")
	require.Equal(t, "api.acme.test", s.LiveHosts()[0],
		"mutating Hosts returned by All must not reach what LiveHosts derives from")
}

// TestSetLookupDeepClonesNestedFields is TestSetAllDeepClonesNestedFields for
// Lookup: it returns a Profile by value, but Profile's reference-typed
// fields (Routes, Handlers, ...) still alias the Set's own copy unless
// Lookup clones them too.
func TestSetLookupDeepClonesNestedFields(t *testing.T) {
	t.Parallel()
	s := MustSet(acmeProfile(okHandler(`{}`)))

	got, ok := s.Lookup("acme")
	require.True(t, ok)
	got.Routes[0].FaultKey = "MUTATED"
	got.Handlers["POST /injected"] = okHandler(`{}`)

	again, ok := s.Lookup("acme")
	require.True(t, ok)
	require.Equal(t, "acme:answer", again.Routes[0].FaultKey,
		"mutating a Route returned by Lookup must not reach the Set's own copy")
	require.NotContains(t, again.Handlers, "POST /injected",
		"mutating Handlers returned by Lookup must not reach the Set's own copy")
}

// TestNewSetClonesAwayFromTheCallersOwnProfile proves NewSet copies a
// profile's reference-typed fields on the way IN: mutating the caller's own
// Profile.Handlers map after NewSet returns must not change what the Set
// serves.
func TestNewSetClonesAwayFromTheCallersOwnProfile(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	s := MustSet(p)

	p.Handlers["POST /injected"] = okHandler(`{}`)

	got, ok := s.Lookup("acme")
	require.True(t, ok)
	require.NotContains(t, got.Handlers, "POST /injected",
		"mutating the caller's own Profile after NewSet must not reach the Set")
}

func TestSetLookupMiss(t *testing.T) {
	t.Parallel()
	s := MustSet(acmeProfile(okHandler(`{}`)))
	_, ok := s.Lookup("nope")
	require.False(t, ok, "a Set must fail closed on an unregistered name")
}

func TestSetRoutesConcatenatesInRegistrationOrder(t *testing.T) {
	t.Parallel()
	a := acmeProfile(okHandler(`{}`))
	a.Name = "acme-a"
	b := acmeProfile(okHandler(`{}`))
	b.Name, b.Port = "acme-b", 9092
	s := MustSet(a, b)
	got := s.Routes()
	require.Len(t, got, 2)
	require.Equal(t, "acme:answer", got[0].FaultKey)
	require.Equal(t, "acme:answer", got[1].FaultKey)
}

func TestSetValidatorsFiltersByName(t *testing.T) {
	t.Parallel()
	a := acmeProfile(okHandler(`{}`))
	a.Name = "acme-a"
	a.Validators = map[string]Validator{"acme-a": stubValidator{}}
	b := acmeProfile(okHandler(`{}`))
	b.Name, b.Port = "acme-b", 9093
	b.Validators = map[string]Validator{"acme-b": stubValidator{}}
	s := MustSet(a, b)

	require.ElementsMatch(t, []string{"acme-a"}, keysOf(s.Validators("acme-a")))
	require.ElementsMatch(t, []string{"acme-a", "acme-b"}, keysOf(s.Validators()))
}

func keysOf(m map[string]Validator) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSetEntryKindsIsSortedAndDeduplicated(t *testing.T) {
	t.Parallel()
	// acme-a and acme-b are single-entry instances sharing Kind "acme" and
	// therefore the same entry-kind key ("acme") — NewSet accepts that (see
	// TestNewSetAcceptsSameKindClaimingOneEntryKindTwice); EntryKinds must
	// collapse the duplicate rather than list it twice. acme-c contributes a
	// distinct key, so the result also proves the sort.
	a := acmeProfile(okHandler(`{}`))
	a.Name, a.Kind = "acme-a", "acme"
	a.Validators = map[string]Validator{"acme": stubValidator{}}
	b := acmeProfile(okHandler(`{}`))
	b.Name, b.Port, b.Kind = "acme-b", 9094, "acme"
	b.Validators = map[string]Validator{"acme": stubValidator{}}
	c := acmeProfile(okHandler(`{}`))
	c.Name, c.Port = "acme-c", 9095
	c.Validators = map[string]Validator{"alpha": stubValidator{}}

	s := MustSet(a, b, c)
	require.Equal(t, []string{"acme", "alpha"}, s.EntryKinds())
}

func TestSetDerivedIDsAndLiveHostsConcatenate(t *testing.T) {
	t.Parallel()
	a := acmeProfile(okHandler(`{}`))
	a.Name = "acme-a"
	a.DerivedIDs = []string{"requestId"}
	a.StreamDerivedIDs = []string{"item.id"}
	a.Hosts = []string{"api.acme.test"}
	b := acmeProfile(okHandler(`{}`))
	b.Name, b.Port = "acme-b", 9095
	b.DerivedIDs = []string{"request_id"}

	s := MustSet(a, b)
	paths, streamPaths := s.DerivedIDs()
	require.Equal(t, []string{"requestId", "request_id"}, paths)
	require.Equal(t, []string{"item.id"}, streamPaths)
	require.Equal(t, []string{"api.acme.test"}, s.LiveHosts())
}

// --- (*Set).Faults ------------------------------------------------------------

func TestSetFaultsRegistersEveryRouteFaultKey(t *testing.T) {
	t.Parallel()
	s := MustSet(acmeProfile(okHandler(`{}`)))
	f := s.Faults(nil)

	dec := f.Next("acme:answer")
	require.False(t, dec.Unknown, "a route registered on the Set must never report fault.unknown_key")
	dec2 := f.Next("some-unregistered-key")
	require.True(t, dec2.Unknown)
}

func TestSetFaultsOptionsApply(t *testing.T) {
	t.Parallel()
	s := MustSet(acmeProfile(okHandler(`{}`)))
	f := s.Faults(nil, WithMaxNamespaces(1))

	admitter, ok := f.(NamespaceAdmitter)
	require.True(t, ok)
	require.True(t, admitter.AdmitNamespace("t-1"))
	require.False(t, admitter.AdmitNamespace("t-2"), "WithMaxNamespaces(1) must bound a second namespace")
}

// --- Profile.Refuse -----------------------------------------------------------

func TestProfileRefuseFillsStatusFromKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind RefusalKind
		want int
	}{
		{RefuseNotFound, http.StatusNotFound},
		{RefuseMethodNotAllowed, http.StatusMethodNotAllowed},
		{RefuseScenarioUnknown, http.StatusNotFound},
		{RefuseInternal, http.StatusInternalServerError},
	}
	var gotStatus int
	p := acmeProfile(okHandler(`{}`))
	p.ErrorBody = func(r Refusal) []byte {
		gotStatus = r.Status
		return []byte(`{}`)
	}
	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			p.Refuse(Refusal{Kind: tc.kind})
			require.Equal(t, tc.want, gotStatus)
		})
	}
}

func TestProfileRefuseJournalsEmptyBodyWhenXIsPresent(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.ErrorBody = func(Refusal) []byte { return nil }

	x := &Exchange{}
	body := p.Refuse(Refusal{Kind: RefuseNotFound, X: x})
	require.Empty(t, body, "an empty ErrorBody is served as-is, never papered over")
	require.True(t, x.HasFinding(CodeRefusalEmptyBody))
}

func TestProfileRefuseWithNilXDoesNotPanicOnEmptyBody(t *testing.T) {
	t.Parallel()
	p := acmeProfile(okHandler(`{}`))
	p.ErrorBody = func(Refusal) []byte { return nil }

	require.NotPanics(t, func() {
		body := p.Refuse(Refusal{Kind: RefuseScenarioUnknown})
		require.Empty(t, body)
	})
}

// --- refusals by construction: NewMux / Profile.Handler ----------------------

func TestProfileHandlerRendersNotFoundThroughErrorBody(t *testing.T) {
	t.Parallel()
	j := journal.NewRing(8, 4096)
	p := acmeProfile(okHandler(`{"ok":true}`))
	h := p.Handler(Deps{Journal: j})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/nope", nil))

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Equal(t, `{"error":"acme refused"}`, w.Body.String())
	require.Equal(t, CodeUnmatched, j.Snapshot()[0].Errors()[0].Code)
}

func TestProfileHandlerRendersMethodNotAllowedThroughErrorBody(t *testing.T) {
	t.Parallel()
	j := journal.NewRing(8, 4096)
	p := acmeProfile(okHandler(`{"ok":true}`))
	h := p.Handler(Deps{Journal: j})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/answer", nil))

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Equal(t, "POST", w.Header().Get("Allow"))
	require.Equal(t, `{"error":"acme refused"}`, w.Body.String())
	require.Equal(t, CodeMethodNotAllowed, j.Snapshot()[0].Errors()[0].Code)
}

// --- house rule 3: a non-abort handler panic becomes a RefuseInternal 500 ----

// handlerThatPanics dereferences nil, exactly as CLAUDE.md's Deliverable 2
// asks: "a handler that dereferences nil".
func handlerThatPanics(_ *Exchange) Response {
	var e *Exchange
	return Response{Status: http.StatusOK, Label: string(e.Provider)}
}

func TestHandlerPanicBecomesRefuseInternal(t *testing.T) {
	t.Parallel()
	j := journal.NewRing(8, 4096)
	p := acmeProfile(handlerThatPanics)
	h := p.Handler(Deps{Journal: j})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Equal(t, `{"error":"acme refused"}`, w.Body.String())

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	errs := entries[0].Errors()
	require.Len(t, errs, 1)
	require.Equal(t, CodeHandlerPanic, errs[0].Code)
	require.Equal(t, journal.OutcomeError, entries[0].Outcome.Kind)
}

func TestAbortHandlerPanicIsUntouched(t *testing.T) {
	t.Parallel()
	p := acmeProfile(func(_ *Exchange) Response {
		panic(http.ErrAbortHandler)
	})
	h := p.Handler(Deps{})

	w := httptest.NewRecorder()
	require.PanicsWithValue(t, http.ErrAbortHandler, func() {
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))
	})
}

func TestNoHandlerRoutePanicIsUnreachedByRecoverPanic(t *testing.T) {
	t.Parallel()
	// A route with no handler at all (spec.Handlers has no entry) is
	// noHandler's territory (handle.go), not recoverPanic's: recoverPanic
	// must pass a nil Handler straight through rather than calling it.
	j := journal.NewRing(8, 4096)
	p := acmeProfile(okHandler(`{}`))
	p.Handlers = map[string]Handler{} // no handler registered for the route
	h := p.Handler(Deps{Journal: j})

	w := httptest.NewRecorder()
	require.NotPanics(t, func() {
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))
	})
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Equal(t, CodeNoHandler, j.Snapshot()[0].Errors()[0].Code)
}

// --- rule 5: the Status < 400 fault-body guard lives in fault_exec.go -------

// TestFaultBodyGuardSkipsANonErrorAttempt is Deliverable 2's required test:
// a `{status: 200}` (equivalently, a bare `- {}`) attempt serves the
// scenario body at 200 with no fault body, even from a hand-built profile
// whose own FaultBody function carries none of the three in-tree packages'
// (now-deleted) `if a.Status < 400 { return nil }` guards.
func TestFaultBodyGuardSkipsANonErrorAttempt(t *testing.T) {
	t.Parallel()

	faultBodyCalled := false
	handler := func(_ *Exchange) Response {
		return Response{
			Status:        http.StatusOK,
			Body:          []byte(`{"ok":true}`),
			Label:         "acme.ok",
			FaultEligible: true,
			// Deliberately naive: no a.Status guard of its own, the shape a
			// spike wrote before this unit's fix (framework-seam.md, rule 5:
			// "a spike reproduced HTTP 200 carrying a 4xx-shaped body on a
			// fifteen-line out-of-tree profile").
			FaultBody: func(scenario.FaultAttempt) []byte {
				faultBodyCalled = true
				return []byte(`{"error":"should never reach the wire"}`)
			},
		}
	}
	p := acmeProfile(handler)
	faults := &scriptedFaults{attempts: []scenario.FaultAttempt{{}}} // a bare "- {}" attempt
	h := p.Handler(Deps{Faults: faults})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, `{"ok":true}`, w.Body.String())
	require.False(t, faultBodyCalled, "FaultBody must not be called for a non-error, bodyless attempt")
}

// TestFaultBodyGuardStillAppliesAnErrorAttempt proves the guard's other half:
// an attempt whose status genuinely is an error still reaches FaultBody, so
// the fix above is a guard, not a way to silently drop real faults.
func TestFaultBodyGuardStillAppliesAnErrorAttempt(t *testing.T) {
	t.Parallel()

	handler := func(_ *Exchange) Response {
		return Response{
			Status:        http.StatusOK,
			Body:          []byte(`{"ok":true}`),
			Label:         "acme.ok",
			FaultEligible: true,
			FaultBody: func(scenario.FaultAttempt) []byte {
				return []byte(`{"error":"faulted"}`)
			},
		}
	}
	p := acmeProfile(handler)
	faults := &scriptedFaults{attempts: []scenario.FaultAttempt{{Status: http.StatusTooManyRequests}}}
	h := p.Handler(Deps{Faults: faults})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Contains(t, w.Body.String(), `"error":"faulted"`)
}

// --- rule 3: an open stream Grammar is refused when empty -------------------

// TestStreamWithArbitraryGrammarStreamsNormally proves the SSE grammar
// vocabulary is genuinely open: a Grammar the framework has never heard of
// streams its chunks and nothing after, exactly like GrammarDelta/GrammarTyped
// — the framework infers nothing from the value, it only requires one.
func TestStreamWithArbitraryGrammarStreamsNormally(t *testing.T) {
	t.Parallel()
	stream := &Stream{
		Grammar: "tool_call_delta",
		Chunks:  EncodeSSE([]SSEEvent{{Data: []byte(`{"i":0}`), Terminal: true}}),
	}
	handler := func(_ *Exchange) Response {
		return Response{Status: http.StatusOK, Stream: stream, Label: "acme.stream", FaultEligible: true}
	}
	p := acmeProfile(handler)
	h := p.Handler(Deps{})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, string(stream.Chunks[0].Bytes), w.Body.String(), "no sentinel: nothing follows the one chunk")
}

// TestStreamWithEmptyNonNilSentinelWritesNothing proves the two readings of
// "has a sentinel" agree: Stream.Bytes (used to plan BytesPlanned) gates on
// len(Sentinel) > 0, so executeStream's own write must gate on the same
// thing rather than on a bare nil check — a non-nil, zero-length Sentinel
// (scenario.FaultAttempt-free, hand-built out-of-tree profile territory)
// must neither sleep SentinelPace nor write an empty frame.
func TestStreamWithEmptyNonNilSentinelWritesNothing(t *testing.T) {
	t.Parallel()
	stream := &Stream{
		Grammar:      "tool_call_delta",
		Chunks:       EncodeSSE([]SSEEvent{{Data: []byte(`{"i":0}`), Terminal: true}}),
		Sentinel:     []byte{}, // non-nil, zero-length — deliberately not nil
		SentinelPace: time.Hour,
	}
	handler := func(_ *Exchange) Response {
		return Response{Status: http.StatusOK, Stream: stream, Label: "acme.stream", FaultEligible: true}
	}
	p := acmeProfile(handler)
	h := p.Handler(Deps{})

	done := make(chan struct{})
	w := httptest.NewRecorder()
	go func() {
		defer close(done)
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not complete: an empty non-nil Sentinel must not sleep SentinelPace")
	}

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, string(stream.Chunks[0].Bytes), w.Body.String(),
		"a non-nil, zero-length Sentinel must write nothing, same as nil")
}

// TestStreamWithEmptyGrammarIsRefused proves the mirror case: a handler bug
// that leaves Grammar empty is refused as RefuseInternal with a
// stream.grammar_missing ERROR finding, rendered through the profile's own
// ErrorBody, rather than the framework silently defaulting to one dialect's
// framing the way a closed Grammar enum once let it.
func TestStreamWithEmptyGrammarIsRefused(t *testing.T) {
	t.Parallel()
	j := journal.NewRing(8, 4096)
	handler := func(_ *Exchange) Response {
		return Response{
			Status: http.StatusOK,
			Stream: &Stream{Chunks: EncodeSSE([]SSEEvent{{Data: []byte(`{}`), Terminal: true}})},
			Label:  "acme.stream", FaultEligible: true,
		}
	}
	p := acmeProfile(handler)
	h := p.Handler(Deps{Journal: j})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Equal(t, `{"error":"acme refused"}`, w.Body.String())

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	errs := entries[0].Errors()
	require.Len(t, errs, 1)
	require.Equal(t, CodeStreamGrammarMissing, errs[0].Code)
	require.Nil(t, entries[0].Outcome.Stream, "the refusal replaces the response outright; nothing streamed")
}

// --- Deliverable 4: AuthPolicy / EntryFor / Reject --------------------------

// authPolicyScenario builds a tiny scenario with three provider entries,
// covering AuthPolicy's three branches: an entry with an explicit mode, an
// entry declaring an auth: block with no mode of its own (which must be
// normalised to the profile's default, not hard-coded to AuthRequired), and
// an entry with no auth: block at all (the pure DefaultAuth fallback).
func authPolicyScenario(t *testing.T) *scenario.Scenario {
	t.Helper()
	s, report, err := scenario.Parse([]byte(`
version: 1
name: authpolicy-test
providers:
  required_entry:
    auth:
      mode: required
    respond: {}
  bare_auth_entry:
    auth: {}
    respond: {}
  no_auth_entry:
    respond: {}
`))
	require.NoError(t, err)
	require.True(t, report.OK(), "%+v", report.Findings)
	return s
}

func TestExchangeAuthPolicyEntryDeclaresItsOwnMode(t *testing.T) {
	t.Parallel()
	x := &Exchange{
		Deps:     Deps{Scenario: authPolicyScenario(t)},
		Provider: "required_entry",
		// defaultAuth left at its zero value (optional would prove nothing
		// here: the entry's OWN mode must win regardless of the profile's
		// default).
		defaultAuth: scenario.AuthOptional,
	}
	require.Equal(t, scenario.AuthRequired, x.AuthPolicy().Mode)
}

func TestExchangeAuthPolicyBareAuthBlockFallsBackToProfileDefault(t *testing.T) {
	t.Parallel()
	x := &Exchange{
		Deps:        Deps{Scenario: authPolicyScenario(t)},
		Provider:    "bare_auth_entry",
		defaultAuth: scenario.AuthOptional,
	}
	require.Equal(t, scenario.AuthOptional, x.AuthPolicy().Mode,
		"an auth: block with no mode of its own is normalised to the PROFILE's default, not hard-coded to AuthRequired")
}

func TestExchangeAuthPolicyNoAuthBlockUsesProfileDefault(t *testing.T) {
	t.Parallel()

	t.Run("empty DefaultAuth means AuthRequired", func(t *testing.T) {
		t.Parallel()
		x := &Exchange{Deps: Deps{Scenario: authPolicyScenario(t)}, Provider: "no_auth_entry"}
		require.Equal(t, scenario.AuthRequired, x.AuthPolicy().Mode)
	})

	t.Run("DefaultAuth optional (MCP's own default)", func(t *testing.T) {
		t.Parallel()
		x := &Exchange{
			Deps: Deps{Scenario: authPolicyScenario(t)}, Provider: "no_auth_entry",
			defaultAuth: scenario.AuthOptional,
		}
		require.Equal(t, scenario.AuthOptional, x.AuthPolicy().Mode)
	})
}

func TestExchangeEntryForResolvesByKind(t *testing.T) {
	t.Parallel()
	x := &Exchange{Deps: Deps{Scenario: authPolicyScenario(t)}}

	e, ok := x.EntryFor("required_entry")
	require.True(t, ok)
	require.Equal(t, "required_entry", e.Name)

	_, ok = x.EntryFor("no_such_entry")
	require.False(t, ok, "a miss is false, not a nil the caller has to check for separately")
}

// TestExchangeEntryForOnAnInstancedListenerResolvesToItsOwnBlock is the
// instancing case EntryFor's own doc comment names: a scenario block is
// addressed by Name, not by Kind, so a handler shared between a primary
// listener and a single-entry instance (Kind != Name, NewSet's accepted
// instancing shape) must read the INSTANCE's own block when it asks for the
// entry its shared Kind names — not the primary's, which
// x.Deps.Scenario.Provider(kind) alone (kind spelled literally) would
// return regardless of which listener served the request.
func TestExchangeEntryForOnAnInstancedListenerResolvesToItsOwnBlock(t *testing.T) {
	t.Parallel()
	sc, report, err := scenario.Parse([]byte(`
version: 1
name: entryfor-instancing-test
providers:
  acme-primary:
    respond: {}
  acme-fallback:
    respond: {}
`))
	require.NoError(t, err)
	require.True(t, report.OK(), "%+v", report.Findings)

	var gotEntryName string
	// handler is deliberately shared between both listeners, and hardcodes
	// the Kind literal the way a route's static Entry string already does
	// — the shape the doc comment on EntryFor describes as "the ordinary
	// way".
	handler := func(x *Exchange) Response {
		e, ok := x.EntryFor("acme-primary")
		require.True(t, ok)
		gotEntryName = e.Name
		return Response{Status: http.StatusOK, Body: []byte(`{}`)}
	}

	primary := acmeProfile(handler)
	primary.Name = "acme-primary"

	fallback := acmeProfile(handler)
	fallback.Name, fallback.Port, fallback.Kind = "acme-fallback", 9096, "acme-primary"

	set := MustSet(primary, fallback)

	t.Run("the fallback instance reads its own block", func(t *testing.T) {
		p, ok := set.Lookup("acme-fallback")
		require.True(t, ok)
		h := p.Handler(Deps{Scenario: sc, Journal: journal.NewRing(8, 4096)})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))
		require.Equal(t, "acme-fallback", gotEntryName)
	})

	t.Run("the primary instance still reads its own block, unaffected", func(t *testing.T) {
		p, ok := set.Lookup("acme-primary")
		require.True(t, ok)
		h := p.Handler(Deps{Scenario: sc, Journal: journal.NewRing(8, 4096)})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))
		require.Equal(t, "acme-primary", gotEntryName)
	})
}

// TestExchangeRejectRendersThroughInstalledErrorBody proves Reject gives a
// rejection one shape end to end: through Profile.Handler, a route handler
// that calls x.Reject fails the request, marks it fault-ineligible, and
// answers in the profile's own RefuseRequest shape — installProfile is what
// makes the ErrorBody reachable from inside the handler at all.
func TestExchangeRejectRendersThroughInstalledErrorBody(t *testing.T) {
	t.Parallel()
	j := journal.NewRing(8, 4096)
	handler := func(x *Exchange) Response {
		return x.Reject(http.StatusUnprocessableEntity, "acme.field_missing", "query", "query is required")
	}
	p := acmeProfile(handler)
	faults := &scriptedFaults{attempts: []scenario.FaultAttempt{{Status: http.StatusTooManyRequests}}}
	h := p.Handler(Deps{Journal: j, Faults: faults})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/answer", nil))

	require.Equal(t, http.StatusUnprocessableEntity, w.Code,
		"Reject's own status wins; a rejected request must never wear a scripted fault's status")
	require.Equal(t, `{"error":"acme refused"}`, w.Body.String())

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	errs := entries[0].Errors()
	require.Len(t, errs, 1)
	require.Equal(t, "acme.field_missing", errs[0].Code)
	require.Equal(t, journal.OutcomeError, entries[0].Outcome.Kind, "a rejected request never consumes a fault attempt")
}

// TestExchangeRejectWithNoInstalledErrorBodyReturnsEmptyBody proves the
// documented fallback: a hand-built Exchange (or one Handle constructed
// directly, outside Profile.Handler) has no installed ErrorBody, so Reject
// answers with an empty body and journals CodeRefusalEmptyBody rather than
// inventing one.
func TestExchangeRejectWithNoInstalledErrorBodyReturnsEmptyBody(t *testing.T) {
	t.Parallel()
	x := &Exchange{}
	resp := x.Reject(http.StatusBadRequest, "acme.bad", "", "bad request")
	require.Empty(t, resp.Body)
	require.False(t, resp.FaultEligible)
	require.True(t, x.HasFinding(CodeRefusalEmptyBody))
}

// TestExchangeRejectSetsLabel proves Reject's Response carries a Label, the
// same "<provider>.error.<code>" convention every in-tree profile's own
// errorResponse already follows (exa: "exa.error.INVALID_REQUEST_BODY",
// tavily: "tavily.error.400") — an empty Label would make Reject the one
// Response kind a journal reader could not group by provider and code
// without decoding Findings.
func TestExchangeRejectSetsLabel(t *testing.T) {
	t.Parallel()
	x := &Exchange{Provider: "acme"}
	resp := x.Reject(http.StatusBadRequest, "acme.field_missing", "query", "query is required")
	require.Equal(t, "acme.error.acme.field_missing", resp.Label)
}

// TestExchangeRefuseDistinguishesNilFromEmptyErrorBody proves refuse's
// CodeRefusalEmptyBody warning says which of the two happened: no ErrorBody
// was installed at all (a hand-built Exchange, or Handle called outside
// Profile.Handler), versus an installed ErrorBody that was reached and chose
// to return nothing for this particular Refusal. The two are different bugs
// in different places, and one message claiming "no ErrorBody was
// installed" for both was false in the second case.
func TestExchangeRefuseDistinguishesNilFromEmptyErrorBody(t *testing.T) {
	t.Parallel()

	t.Run("no ErrorBody installed at all", func(t *testing.T) {
		t.Parallel()
		x := &Exchange{}
		x.Reject(http.StatusBadRequest, "acme.bad", "", "bad request")
		warnings := x.journalFindings()
		require.Len(t, warnings, 2) // acme.bad (error) + refusal.empty_body (warning)
		msg := findingMessage(t, warnings, CodeRefusalEmptyBody)
		require.Contains(t, msg, "no ErrorBody was installed")
	})

	t.Run("installed ErrorBody returned nothing", func(t *testing.T) {
		t.Parallel()
		x := &Exchange{errorBody: func(Refusal) []byte { return nil }}
		x.Reject(http.StatusBadRequest, "acme.bad", "", "bad request")
		warnings := x.journalFindings()
		msg := findingMessage(t, warnings, CodeRefusalEmptyBody)
		require.NotContains(t, msg, "no ErrorBody was installed",
			"an installed ErrorBody that returned nothing is a different bug than none being installed at all")
		require.Contains(t, msg, "returned no bytes")
	})
}

// findingMessage returns the message of the first finding with code, failing
// the test if none is found.
func findingMessage(t *testing.T, findings []journal.Finding, code string) string {
	t.Helper()
	for _, f := range findings {
		if f.Code == code {
			return f.Message
		}
	}
	t.Fatalf("no finding with code %q", code)
	return ""
}

// TestValidatorlessProfileClaimsItsOwnEntryKind pins the fix unit 3's review
// asked for: the simplest foreign profile — no Validators at all — still owns
// the scenario entry kind named after it, so a scripted block for it is
// neither "unimplemented" (ValidateScenario) nor invisible to Set.Validators /
// Set.EntryKinds, and a second profile of a DIFFERENT kind claiming that same
// name is still refused at registration.
func TestValidatorlessProfileClaimsItsOwnEntryKind(t *testing.T) {
	t.Parallel()

	set, err := NewSet(acmeProfile(okHandler(`{}`)))
	require.NoError(t, err)

	require.Equal(t, []string{"acme"}, set.EntryKinds())
	vs := set.Validators()
	require.Contains(t, vs, "acme")
	require.NotNil(t, vs["acme"], "the claim must be a real (no-op) validator: ValidateScenario treats nil as unimplemented")
	require.Contains(t, set.Validators("acme"), "acme")

	sc, report, err := scenario.Parse([]byte(`
version: 1
name: acme-scripted
providers:
  acme:
    fault:
      attempts:
        - {status: 429}
        - {}
`))
	require.NoError(t, err)
	require.True(t, report.OK(), "%v", report.Findings)
	for _, f := range ValidateScenario(sc, set.Validators()) {
		require.NotEqual(t, CodeProviderUnimplemented, f.Code,
			"a scripted acme block must not read as unimplemented: %v", f)
	}

	// The claim is a claim: another kind may not take the same entry name.
	other := acmeProfile(okHandler(`{}`))
	other.Name = "acme-two"
	other.Port = 0
	other.Validators = map[string]Validator{"acme": noopValidator{}}
	_, err = NewSet(acmeProfile(okHandler(`{}`)), other)
	require.Error(t, err)
	require.Contains(t, err.Error(), `entry kind "acme"`)
}
