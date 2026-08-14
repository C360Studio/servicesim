package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// admitStub is a Faults implementation that also bounds namespaces, so Handle's
// type assertion for NamespaceAdmitter finds something.
type admitStub struct {
	allow map[string]bool
	asked []string
}

func (a *admitStub) Next(key string) FaultDecision { return FaultDecision{Index: 0, Key: key} }
func (a *admitStub) Reset()                        {}

func (a *admitStub) AdmitNamespace(ns string) bool {
	a.asked = append(a.asked, ns)
	return a.allow[ns]
}

// plainFaults bounds nothing, so it must NOT implement NamespaceAdmitter.
type plainFaults struct{}

func (plainFaults) Next(key string) FaultDecision { return FaultDecision{Index: 0, Key: key} }
func (plainFaults) Reset()                        {}

// TestHandleRefusesUnadmittedNamespaceBeforeTheHandlerRuns pins the fix for a
// defect that survived a full implementation pass while being reported as
// closed.
//
// The bound was originally enforced inside the fault engine, which a request
// reaches only AFTER its handler has produced a response. The refusal was
// logged at error level and the client still received a 200 with no journal
// entry, so a test in the refused namespace saw success, collected nothing, and
// failed later on an assertion counting requests it appeared never to have
// sent. The only evidence was in the simulator's own stderr, which is the one
// place a consumer's test output does not reach.
//
// The assertion that matters is therefore that the refusal is an ERROR FINDING
// the handler can see before it decides its response — the same seam auth and
// body-too-large failures already use, which is what lets each provider render
// the refusal in its own error envelope rather than a generic one.
func TestHandleRefusesUnadmittedNamespaceBeforeTheHandlerRuns(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace string
		admitted  bool
		wantFail  bool
	}{
		{name: "admitted namespace is served", namespace: "ok", admitted: true},
		{name: "refused namespace fails the exchange", namespace: "no", admitted: false, wantFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			faults := &admitStub{allow: map[string]bool{tc.namespace: tc.admitted}}

			var sawFailed, sawLimitFinding bool
			var sawNamespace string
			// Mimics a real provider handler: consult the exchange BEFORE
			// deciding a response.
			h := func(x *Exchange) Response {
				sawFailed = x.Failed()
				sawLimitFinding = x.HasFinding(CodeNamespaceLimit)
				sawNamespace = x.Lane().Namespace
				if x.Failed() {
					return Response{Status: http.StatusServiceUnavailable, Body: []byte(`{"error":"refused"}`)}
				}
				return Response{Status: http.StatusOK, Body: []byte(`{}`)}
			}

			deps := Deps{Faults: faults}.Normalized()
			mux := NewMux(deps, Exa, MuxSpec{
				Routes:   []Route{{Pattern: "POST /search", FaultKey: "exa:search"}},
				Handlers: map[string]Handler{"POST /search": h},
			})

			req := httptest.NewRequest(http.MethodPost,
				"/n/"+tc.namespace+"/search", strings.NewReader(`{"query":"q"}`))
			req.Header.Set("content-type", "application/json")
			req.Header.Set("x-api-key", "k")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if len(faults.asked) == 0 {
				t.Fatal("AdmitNamespace was never consulted")
			}
			if faults.asked[0] != tc.namespace {
				t.Fatalf("asked about namespace %q, want %q", faults.asked[0], tc.namespace)
			}
			if sawNamespace != tc.namespace {
				t.Fatalf("handler saw namespace %q, want %q", sawNamespace, tc.namespace)
			}
			if sawFailed != tc.wantFail {
				t.Fatalf("handler saw Failed() = %v, want %v", sawFailed, tc.wantFail)
			}
			if sawLimitFinding != tc.wantFail {
				t.Fatalf("handler saw %s finding = %v, want %v — without it a provider "+
					"cannot tell a refusal from a validation error and will pick the wrong status",
					CodeNamespaceLimit, sawLimitFinding, tc.wantFail)
			}
			wantStatus := http.StatusOK
			if tc.wantFail {
				wantStatus = http.StatusServiceUnavailable
			}
			if rec.Code != wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, wantStatus)
			}
		})
	}
}

// TestHandleAdmitsEveryNamespaceWhenFaultsDoesNotBound proves the interface is
// genuinely optional: a Faults implementation that bounds nothing must not have
// its requests refused, and must not be asked.
func TestHandleAdmitsEveryNamespaceWhenFaultsDoesNotBound(t *testing.T) {
	if _, ok := any(plainFaults{}).(NamespaceAdmitter); ok {
		t.Fatal("plainFaults must not implement NamespaceAdmitter, or this test proves nothing")
	}

	handlerRan := false
	deps := Deps{Faults: plainFaults{}}.Normalized()
	mux := NewMux(deps, Exa, MuxSpec{
		Routes: []Route{{Pattern: "POST /search", FaultKey: "exa:search"}},
		Handlers: map[string]Handler{
			"POST /search": func(*Exchange) Response {
				handlerRan = true
				return Response{Status: http.StatusOK, Body: []byte(`{}`)}
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/n/anything/search", strings.NewReader(`{"query":"q"}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", "k")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if !handlerRan {
		t.Fatal("handler did not run: an unbounded Faults must refuse nothing")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
