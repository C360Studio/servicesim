package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/admin"
	"github.com/c360studio/servicesim/internal/journal"
	"github.com/c360studio/servicesim/profiles/exa"
	"github.com/c360studio/servicesim/provider"
)

// acceptedCreateScenario scripts an async Exa create whose first `repeat`
// attempts lose their reply after the job is saved, so a test can run past a
// job bound.
func acceptedCreateScenario(repeat string) string {
	return acceptedShapeScenario("kind: close_before_headers, accepted: true, repeat: " + repeat)
}

// acceptedShapeScenario scripts an async Exa create whose create plan is the one
// attempt written as flow-style YAML, followed by an ordinary success.
func acceptedShapeScenario(attempt string) string {
	return `
version: 1
name: accepted-create
providers:
  exa_agent_runs:
    create:
      fault: {attempts: [{` + attempt + `}]}
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - respond: {status: completed, cost_dollars: {total: 0.01}}
`
}

// rawCreate sends one create over a bare TCP connection and returns every byte the
// server sent before it closed: status line, headers, body and any chunk framing.
// That is strictly more than an HTTP client hands the application, which is what
// makes an id's absence from it worth asserting. A reset ends the read and is
// tolerated — what arrived before it is what the client saw — but a read that
// times out fails the test, because it would mean the server never finished.
func rawCreate(t *testing.T, addr string) []byte {
	t.Helper()

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	body := `{"query":"find the finding"}`
	request := "POST /agent/runs HTTP/1.1\r\nHost: " + addr + "\r\nAuthorization: Bearer " + testKey +
		"\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) +
		"\r\nConnection: close\r\n\r\n" + body
	_, err = conn.Write([]byte(request))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	raw, err := io.ReadAll(conn)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("the server neither answered nor closed the connection: %v", err)
	}
	return raw
}

// pollRun polls GET /agent/runs/{id} with the test credential.
func pollRun(t *testing.T, addr, id string) (status int, body string) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://"+addr+"/agent/runs/"+id, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+testKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(out)
}

// lostCreate sends one create and reports what the application under test sees:
// a status, or a transport error for a reply that never came. It never fails the
// test on a transport error, because that is the scripted outcome.
func lostCreate(t *testing.T, addr, path string) (status int, err error) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+path,
		strings.NewReader(`{"query":"find the finding"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testKey)

	// A client that reused connections could mistake a scripted abort for a stale
	// keep-alive; this one never does.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	t.Cleanup(client.CloseIdleConnections)

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// adminJobs reads GET /__admin/jobs.
func adminJobs(t *testing.T, h *harness) []admin.JobSummary {
	t.Helper()

	status, body := get(t, h.Addr(SurfaceAdmin), "/__admin/jobs")
	require.Equal(t, http.StatusOK, status, string(body))
	var out admin.JobsResponse
	require.NoError(t, json.Unmarshal(body, &out), string(body))
	return out.Jobs
}

// awaitEntries reads GET /__admin/requests until it holds at least n entries, or
// fails the test after a bound. It is the composed-binary counterpart of
// testkit's AwaitRequests, and for the same reason: an aborting fault is
// journaled before the socket is destroyed, but the client can observe the reset
// first, so a bare read after a client call is a race the journal's own
// documentation warns about. It polls state rather than sleeping for a duration.
func awaitEntries(t *testing.T, h *harness, n int) []journal.Entry {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		status, body := get(t, h.Addr(SurfaceAdmin), "/__admin/requests")
		require.Equal(t, http.StatusOK, status, string(body))
		var out admin.RequestsResponse
		require.NoError(t, json.Unmarshal(body, &out), string(body))
		if len(out.Entries) >= n {
			return out.Entries
		}
		if time.Now().After(deadline) {
			t.Fatalf("the journal held %d entries, want %d, after waiting", len(out.Entries), n)
		}
		runtime.Gosched()
	}
}

// adminReset sends POST /__admin/reset with query and requires it to succeed.
func adminReset(t *testing.T, h *harness, query string) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://"+h.Addr(SurfaceAdmin)+"/__admin/reset?"+query, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
}

// findingCodes lists the codes of an entry's findings.
func findingCodes(e journal.Entry) []string {
	codes := make([]string, len(e.Findings))
	for i, f := range e.Findings {
		codes[i] = f.Code
	}
	return codes
}

// TestAcceptedCreateIsVisibleToATestControllerButNotToTheApplication runs the
// issue-7 scenario through the composed binary, with the admin surface a test
// controller reads: the job listing is evidence, and a poll of a listed id shows
// the job is real — while the application, holding only a transport error, has no
// way to learn the id. Nothing here mutates state through the admin surface.
func TestAcceptedCreateIsVisibleToATestControllerButNotToTheApplication(t *testing.T) {
	t.Parallel()

	h := start(t, testConfig(t, writeScenario(t, acceptedCreateScenario("1"))...), discard())
	exaAddr := h.Addr(string(exa.Name))

	_, err := lostCreate(t, exaAddr, "/agent/runs")
	require.Error(t, err, "the reply was lost: the application sees a transport error and no identifier")

	// The listing and the journal agree on one accepted create at attempt 0.
	jobs := adminJobs(t, h)
	require.Len(t, jobs, 1)
	assert.Equal(t, "exa_agent_runs", jobs[0].Entry)
	assert.Equal(t, "default", jobs[0].Namespace)
	assert.Zero(t, jobs[0].CreateIndex)

	create := awaitEntries(t, h, 1)[0]
	assert.Equal(t, http.MethodPost, create.Method)
	assert.Equal(t, "/agent/runs", create.Path)
	assert.Equal(t, "exa:agent_runs.create", create.Outcome.FaultKey)
	assert.Equal(t, "close_before_headers", create.Outcome.FaultKind)
	assert.Equal(t, jobs[0].CreateIndex, create.Outcome.AttemptIndex)
	assert.True(t, create.Outcome.Aborted)

	// A poll of the listed id resolves: the job is real, not a listing artefact.
	status, body := pollRun(t, exaAddr, jobs[0].ID)
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, `"status":"running"`)

	// The listing is read-only evidence: it accepts no write, so it cannot become
	// a recovery API the application could drive.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://"+h.Addr(SurfaceAdmin)+"/__admin/jobs", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// The byte layout of Exa's create body, which the table below and the schema
// documentation quote for truncate_body. The create is derived in full and cannot
// be scripted; profiles/exa pins these numbers against the real body, so a change
// to the wire shape fails there and the documentation moves with it.
const (
	createBodyBytes = 390 // the whole create body
	idEnd           = 49  // `{"id":"` is 7 bytes and the identifier is 42 more
)

// TestAnAcceptedCreateKeepsItsJobAndSaysWhatTheClientLearns is the leak test, for
// every shape that keeps a job. `accepted` keeps the JOB; it does not hide the id,
// so each row says whether the application can read it, and the table pins that.
//
// Where the id is hidden, its whole text appears nowhere in the bytes the client
// received — the status line, every header including x-request-id, and the body.
// The shapes that deliver a full response matter most: an error envelope or request
// id built from the run's own id would give the application the id the scenario
// says it lost, and "an id field is absent" would never notice. truncate_body is the
// shape that does not hide it by default: it sends a prefix of the rendered body and
// the id is that body's first key, so only a cut short of the id's last byte (the
// 8-byte row) withholds it, and the default or a larger cut delivers all of it.
//
// So the test cannot pass vacuously, the same id is shown to be real from the
// other side: GET /__admin/jobs lists it and a poll of the listed id resolves.
func TestAnAcceptedCreateKeepsItsJobAndSaysWhatTheClientLearns(t *testing.T) {
	t.Parallel()

	truncate := func(n int) string { return "kind: truncate_body, truncate_after_bytes: " + strconv.Itoa(n) }
	shapes := []struct {
		name, attempt string
		responds      bool // some bytes of a response reach the client
		hidden        bool // the id appears nowhere in what the client received
	}{
		{"close_before_headers", `kind: close_before_headers`, false, true},
		// 8 < idEnd: the cut falls inside the id, after `{"id":"a`. This is the
		// hiding case; the default cut and any larger one are not.
		{"truncate_body cut at 8 bytes", truncate(8), true, true},
		{"truncate_body cut one byte short of the id's end", truncate(idEnd - 1), true, true},
		{"truncate_body cut at the id's last byte", truncate(idEnd), true, false},
		{"truncate_body with the default cut, half the body", `kind: truncate_body`, true, false},
		{"truncate_body cut at the body's length", truncate(createBodyBytes), true, false},
		{"truncate_body cut past the body's length", truncate(createBodyBytes * 10), true, false},
		{"empty_body", `kind: empty_body`, true, true},
		{"invalid_json", `kind: invalid_json, raw_body: "not json"`, true, true},
		{"a 504 status", `status: 504`, true, true},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()

			h := start(t, testConfig(t, writeScenario(t, acceptedShapeScenario(shape.attempt+", accepted: true"))...), discard())
			exaAddr := h.Addr(string(exa.Name))

			raw := rawCreate(t, exaAddr)

			// The job exists, is listed, and resolves: its id is worth hiding.
			jobs := adminJobs(t, h)
			require.Len(t, jobs, 1)
			id := jobs[0].ID
			require.NotEmpty(t, id)
			status, body := pollRun(t, exaAddr, id)
			require.Equal(t, http.StatusOK, status, body)
			require.Contains(t, body, id)

			if !shape.responds {
				require.Empty(t, raw, "nothing at all reaches the client")
				return
			}
			require.True(t, bytes.HasPrefix(raw, []byte("HTTP/1.1 ")), "a response began: %q", raw)

			// The same, over what a client parser would hand the application, in case
			// chunk framing ever split an id across the raw bytes.
			resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
			require.NoError(t, err, "the bytes received should begin a parseable response: %q", raw)
			defer func() { _ = resp.Body.Close() }()
			decoded, _ := io.ReadAll(resp.Body) // a truncated body is the scripted outcome

			if !shape.hidden {
				// Documented behaviour, pinned: the job is kept AND the client holds the
				// whole id. A cut that reaches the id's last byte delivers it.
				assert.Contains(t, string(raw), id, "this shape delivers the whole id")
				assert.Contains(t, string(decoded), id)
				return
			}
			assert.NotContains(t, string(raw), id, "the id is in the bytes the client received")
			for name, values := range resp.Header {
				for _, v := range values {
					assert.NotContains(t, v, id, "header %s carries the id", name)
				}
			}
			assert.NotContains(t, string(decoded), id)

			if shape.name == "a 504 status" {
				// The one full error response: make sure the checks above had a request
				// id and an envelope to find the id in.
				assert.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
				assert.NotEmpty(t, resp.Header.Get("x-request-id"))
				assert.NotEmpty(t, decoded)
			}
		})
	}
}

// TestAdminResetDropsAnAcceptedJobWithItsCursors: whichever scope the reset names,
// the accepted job goes with the cursor that derived it, so the next create is
// attempt 0 again, is lost again, and re-mints the same identifier without a
// job.id_collision.
func TestAdminResetDropsAnAcceptedJobWithItsCursors(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, query, path string }{
		{"every namespace", "all=true", "/agent/runs"},
		{"one namespace", "namespace=t-reset", "/n/t-reset/agent/runs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := start(t, testConfig(t, writeScenario(t, acceptedCreateScenario("1"))...), discard())
			exaAddr := h.Addr(string(exa.Name))

			_, err := lostCreate(t, exaAddr, tc.path)
			require.Error(t, err)
			before := adminJobs(t, h)
			require.Len(t, before, 1)
			awaitEntries(t, h, 1)

			adminReset(t, h, tc.query)
			require.Empty(t, adminJobs(t, h), "the reset drops the accepted job")

			_, err = lostCreate(t, exaAddr, tc.path)
			require.Error(t, err, "the cursor was reset with it: attempt 0 again, lost again")
			after := adminJobs(t, h)
			require.Len(t, after, 1)
			assert.Equal(t, before[0].ID, after[0].ID, "the same script derives the same identifier")

			// The reset cleared the journal too, so the one entry now is the second
			// create's, and it carries no collision.
			entries := awaitEntries(t, h, 1)
			require.Len(t, entries, 1)
			assert.NotContains(t, findingCodes(entries[0]), provider.CodeJobIDCollision,
				"no collision: the record went with the cursor")
		})
	}
}

// TestAcceptedCreateAtTheJobBoundIsRefusedNotFaulted: at --max-jobs the create is
// refused with the provider's rejection and the scripted fault is NOT applied, so
// the application sees a response, not a lost reply, and the namespace holds no
// more jobs than its bound. Accepting past the bound would have made the bound a
// number that does not match the jobs an author can see.
func TestAcceptedCreateAtTheJobBoundIsRefusedNotFaulted(t *testing.T) {
	t.Parallel()

	args := append(writeScenario(t, acceptedCreateScenario("2")), "--max-jobs", "1")
	h := start(t, testConfig(t, args...), discard())
	exaAddr := h.Addr(string(exa.Name))

	_, err := lostCreate(t, exaAddr, "/agent/runs")
	require.Error(t, err, "attempt 0 fills the namespace to its bound of one")
	require.Len(t, adminJobs(t, h), 1)

	status, err := lostCreate(t, exaAddr, "/agent/runs")
	require.NoError(t, err, "at the bound the scripted abort is not applied: the client gets the rejection")
	assert.GreaterOrEqual(t, status, http.StatusBadRequest)
	assert.Len(t, adminJobs(t, h), 1, "nothing was added past the bound")

	refused := awaitEntries(t, h, 2)[1]
	assert.False(t, refused.Outcome.Aborted)
	codes := findingCodes(refused)
	assert.Contains(t, codes, provider.CodeJobLimitReached)
	assert.NotContains(t, codes, provider.CodeAcceptedUnreachable,
		"the route mints; it was refused, which job.limit_reached already says")
}
