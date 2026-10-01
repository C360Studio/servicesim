package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/scenario"
)

// accepted returns a copy of the row's attempt with the accepted modifier set.
func accepted(a *scenario.FaultAttempt) *scenario.FaultAttempt {
	out := *a
	out.Accepted = true
	return &out
}

// TestMintJobKeepsTheJobOfAnAcceptedAttempt asks MintJob directly, for every row
// of the delivery table that scripts an attempt. `accepted` keeps the job whether
// or not the body is delivered: where it is not, that is the whole point, and
// where it is, the job is kept anyway, so the runtime has no case in which the
// modifier loses a job. (Load rejects the redundant spelling; MintJob does not
// have to.)
func TestMintJobKeepsTheJobOfAnAcceptedAttempt(t *testing.T) {
	t.Parallel()

	for _, tc := range deliveryTable() {
		if tc.attempt == nil {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := jobs.NewRegistry(jobs.Limits{})
			x := mintExchange(t, store, accepted(tc.attempt))

			id, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode)
			require.True(t, ok, "MintJob refused: %+v", x.Findings())

			_, found := store.Lookup(DefaultNamespace, id)
			require.True(t, found, "an accepted attempt must keep its job")
			require.Equal(t, 1, store.StatsIn(DefaultNamespace).Count)
		})
	}
}

// TestAnAcceptedCreateKeepsAJobTheClientNeverLearnsOf is the end-to-end half, for
// every shape of the delivery table the client does NOT receive an identifier
// from: through the real Handle and executor, `accepted` leaves exactly one job
// and the client still holds no identifier, while the same attempt without it
// leaves none. Both halves are asserted so the modifier is the only difference.
func TestAnAcceptedCreateKeepsAJobTheClientNeverLearnsOf(t *testing.T) {
	t.Parallel()

	for _, tc := range deliveryTable() {
		if tc.delivered {
			continue
		}
		for _, withAccepted := range []bool{true, false} {
			name := tc.name + " without accepted"
			attempt := tc.attempt
			if withAccepted {
				name, attempt = tc.name+" with accepted", accepted(tc.attempt)
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				store := jobs.NewRegistry(jobs.Limits{})
				url, minted := serveCreate(t, store, attempt)

				held := clientHeldIdentifier(t, newCreateClient(t), url)
				id := <-minted
				require.NotEmpty(t, id)
				require.Empty(t, held, "the client must not learn the identifier — %s", tc.why)

				_, found := store.Lookup(DefaultNamespace, id)
				require.Equal(t, withAccepted, found, "job present = %v, want %v — %s", found, withAccepted, tc.why)
				if withAccepted {
					require.Equal(t, 1, store.StatsIn(DefaultNamespace).Count)
				} else {
					require.Zero(t, store.StatsIn(DefaultNamespace).Count)
				}
			})
		}
	}
}

// newCreateClient is a client whose connections are not reused, so a scripted
// abort on one request cannot be mistaken for a stale keep-alive on the next.
func newCreateClient(t *testing.T) *http.Client {
	t.Helper()

	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

// serveCreate serves a create through the real Handle and executor, scripted
// with attempt as its only fault attempt, and returns the URL to POST to and a
// channel carrying the identifier each request minted. The handler is shaped like
// the in-tree providers' creates: it mints, renders a success body carrying the
// identifier, and is fault-eligible with a FaultBody built from the attempt alone,
// never from the rendered body or the minted identifier.
func serveCreate(t *testing.T, store jobs.Store, attempt *scenario.FaultAttempt) (url string, minted <-chan string) {
	t.Helper()

	engine := &scriptedFaults{}
	if attempt != nil {
		engine.attempts = []scenario.FaultAttempt{*attempt}
	}

	ids := make(chan string, 4)
	handler := func(x *Exchange) Response {
		id, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode)
		if !ok {
			t.Errorf("MintJob refused: %+v", x.Findings())
		}
		ids <- id
		return Response{
			Status:        http.StatusCreated,
			Body:          []byte(`{"id":"` + id + `","status":"queued"}`),
			Label:         "test.created",
			FaultEligible: true,
			FaultBody: func(a scenario.FaultAttempt) []byte {
				if len(a.Body) > 0 {
					b, _ := json.Marshal(a.Body)
					return b
				}
				b, _ := json.Marshal(map[string]any{"error": a.Error, "status": a.Status})
				return b
			},
		}
	}

	srv := httptest.NewServer(Handle(
		Deps{Faults: engine, Jobs: store, DelayMode: DelaySkip},
		testProviderExa, Route{Pattern: "POST /agent/runs", FaultKey: "exa:agent_runs.create", Entry: "exa_agent_runs"}, handler))
	t.Cleanup(srv.Close)
	return srv.URL + "/agent/runs", ids
}
