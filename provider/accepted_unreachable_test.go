package provider

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/scenario"
)

// acceptedFindings returns the journal findings of entry that carry
// CodeAcceptedUnreachable.
func acceptedFindings(e journal.Entry) []journal.Finding {
	var out []journal.Finding
	for _, f := range e.Findings {
		if f.Code == CodeAcceptedUnreachable {
			out = append(out, f)
		}
	}
	return out
}

// post sends one create-shaped POST on a client that never reuses a connection and
// reports the status, or -1 for a transport error — what a client that lost the
// reply observes. Every response is drained so the connection is released.
func post(t *testing.T, url string) int {
	t.Helper()

	resp, err := newCreateClient(t).Post(url, "application/json", strings.NewReader(`{"query":"q"}`))
	if err != nil {
		return -1
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestAnAcceptedAttemptClaimedByARequestThatMintsNothingIsReported covers the one
// thing load cannot check: only a profile's routes know which of them mint a job,
// so `accepted` is allowed under any plan and the claim is judged per request. A
// scenario author who put it on a poll plan scripted a retained job that cannot
// exist, and the finding says so — while the attempt itself still applies, so the
// request behaves exactly as the plan would without the modifier.
func TestAnAcceptedAttemptClaimedByARequestThatMintsNothingIsReported(t *testing.T) {
	t.Parallel()

	const key = "exa:agent_runs.poll"
	mints := func(x *Exchange) Response {
		_, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode)
		require.True(t, ok, "MintJob refused: %+v", x.Findings())
		return Response{Status: http.StatusCreated, Body: []byte(`{"ok":true}`), Label: "test.created", FaultEligible: true}
	}
	mintsNothing := func(_ *Exchange) Response {
		return Response{Status: http.StatusOK, Body: []byte(`{"ok":true}`), Label: "test.polled", FaultEligible: true}
	}

	tests := []struct {
		name       string
		handler    Handler
		attempt    scenario.FaultAttempt
		wantReport bool
		wantStatus int // -1 means the client sees a transport error
		wantAbort  bool
	}{
		{
			name:       "an accepted status on a request that mints nothing",
			handler:    mintsNothing,
			attempt:    scenario.FaultAttempt{Status: http.StatusGatewayTimeout, Accepted: true},
			wantReport: true,
			wantStatus: http.StatusGatewayTimeout,
		},
		{
			name:       "an accepted close_before_headers on a request that mints nothing",
			handler:    mintsNothing,
			attempt:    scenario.FaultAttempt{Kind: scenario.FaultCloseBeforeHeaders, Accepted: true},
			wantReport: true,
			wantStatus: -1,
			wantAbort:  true,
		},
		{
			name:       "the same attempt on a request that mints",
			handler:    mints,
			attempt:    scenario.FaultAttempt{Status: http.StatusGatewayTimeout, Accepted: true},
			wantReport: false,
			wantStatus: http.StatusGatewayTimeout,
		},
		{
			name:       "an attempt that is not accepted on a request that mints nothing",
			handler:    mintsNothing,
			attempt:    scenario.FaultAttempt{Status: http.StatusGatewayTimeout},
			wantReport: false,
			wantStatus: http.StatusGatewayTimeout,
		},
		{
			name:       "an accepted attempt that delivers its body, on a request that mints nothing",
			handler:    mintsNothing,
			attempt:    scenario.FaultAttempt{Accepted: true},
			wantReport: true,
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			j := journal.NewRing(8, 4096)
			engine := &scriptedFaults{attempts: []scenario.FaultAttempt{tc.attempt}}
			srv := httptest.NewServer(Handle(
				Deps{Faults: engine, Jobs: jobs.NewRegistry(jobs.Limits{}), Journal: j, DelayMode: DelaySkip},
				testProviderExa, Route{Pattern: "POST /agent/runs", FaultKey: key, Entry: "exa_agent_runs"}, tc.handler))
			t.Cleanup(srv.Close)

			require.Equal(t, tc.wantStatus, post(t, srv.URL+"/agent/runs"),
				"the attempt must still apply as an ordinary fault")

			// Journaled before the socket is touched, so the entry exists by now
			// even for an aborting shape.
			entries := j.Snapshot()
			require.Len(t, entries, 1)
			e := entries[0]
			require.Equal(t, tc.wantAbort, e.Outcome.Aborted)
			require.Equal(t, key, e.Outcome.FaultKey)
			require.Zero(t, e.Outcome.AttemptIndex)

			found := acceptedFindings(e)
			if !tc.wantReport {
				require.Empty(t, found, "findings: %+v", e.Findings)
				return
			}
			require.Len(t, found, 1, "findings: %+v", e.Findings)
			require.Equal(t, journal.SeverityError, found[0].Severity,
				"a scripted modifier that cannot apply is an authoring error, as scenario.stream.abort_unreachable is")
			require.Contains(t, found[0].Message, key, "names the plan that carried it")
		})
	}
}

// TestACreateRefusedAtTheJobBoundDoesNotApplyAnAcceptedFault: at the bound MintJob
// refuses, the handler rejects, and Handle strips the claimed attempt — the
// scripted fault is NOT applied, which is loud (job.limit_reached) rather than a
// created job that silently is not. It must not also claim the modifier was
// unreachable: the route does mint, it was refused.
func TestACreateRefusedAtTheJobBoundDoesNotApplyAnAcceptedFault(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{MaxJobs: 1})
	require.NoError(t, createJob(store, "run_filled"))

	j := journal.NewRing(8, 4096)
	engine := &scriptedFaults{attempts: []scenario.FaultAttempt{{Kind: scenario.FaultCloseBeforeHeaders, Accepted: true}}}
	handler := func(x *Exchange) Response {
		if _, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode); !ok {
			return Response{Status: http.StatusTooManyRequests, Body: []byte(`{"error":"job limit"}`), Label: "test.refused"}
		}
		return Response{Status: http.StatusCreated, Body: []byte(`{"ok":true}`), Label: "test.created", FaultEligible: true}
	}
	srv := httptest.NewServer(Handle(
		Deps{Faults: engine, Jobs: store, Journal: j, DelayMode: DelaySkip},
		testProviderExa, Route{Pattern: "POST /agent/runs", FaultKey: "exa:agent_runs.create", Entry: "exa_agent_runs"}, handler))
	t.Cleanup(srv.Close)

	require.Equal(t, http.StatusTooManyRequests, post(t, srv.URL+"/agent/runs"),
		"the provider's rejection is served and the scripted fault is not applied")

	entries := j.Snapshot()
	require.Len(t, entries, 1)
	require.False(t, entries[0].Outcome.Aborted)
	codes := map[string]journal.Severity{}
	for _, f := range entries[0].Findings {
		codes[f.Code] = f.Severity
	}
	require.Equal(t, journal.SeverityError, codes[CodeJobLimitReached])
	require.Equal(t, journal.SeverityWarning, codes[CodeAttemptOnRejection])
	require.NotContains(t, codes, CodeAcceptedUnreachable)
	require.Equal(t, 1, store.StatsIn(DefaultNamespace).Count, "nothing was added at the bound")
}

// createJob records a job directly, to fill a namespace.
func createJob(store jobs.Store, id string) error {
	_, err := store.Create(jobs.Job{ID: id, Namespace: DefaultNamespace, Entry: "exa_agent_runs", LaneKey: "k"})
	return err
}
