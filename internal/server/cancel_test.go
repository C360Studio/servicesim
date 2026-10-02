package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/profiles/exa"
	"github.com/c360studio/servicesim/provider"
	"github.com/c360studio/servicesim/scenarios"
)

// cancelScenario scripts an Exa run that is still running at its second poll,
// and what its polls serve once a cancel is recorded.
const cancelScenario = `
version: 1
name: exa-cancel
providers:
  exa_agent_runs:
    turns:
      - when: {call_index: 0}
        respond: {status: running}
      - when: {call_index: 1}
        respond: {status: running}
      - respond: {status: completed, output: {text: done}, cost_dollars: {total: 0.045}}
    cancel:
      turns:
        - respond: {status: cancelled, cost_dollars: {total: 0.012}}
`

// TestACancelIsVisibleOnTheAdminJobListing runs a cancel through the composed
// binary and reads the evidence a test controller has: GET /__admin/jobs carries
// cancel_at_poll once, and only once, a cancel is recorded, and the journal labels
// the cancel and the poll that confirms it.
func TestACancelIsVisibleOnTheAdminJobListing(t *testing.T) {
	t.Parallel()

	h := start(t, testConfig(t, writeScenario(t, cancelScenario)...), discard())
	exaAddr := h.Addr(string(exa.Name))

	resp := post(t, exaAddr, "/agent/runs", `{"query":"find the finding"}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var run struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&run))

	status, body := pollRun(t, exaAddr, run.ID)
	require.Equal(t, http.StatusOK, status, body)

	listed := adminJobs(t, h)
	require.Len(t, listed, 1)
	assert.Equal(t, 1, listed[0].Polls)
	assert.Nil(t, listed[0].CancelAtPoll, "no cancel is recorded yet, so cancel_at_poll is absent")

	cancel := post(t, exaAddr, "/agent/runs/"+run.ID+"/cancel", "")
	cancelBody, err := io.ReadAll(cancel.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, cancel.StatusCode, string(cancelBody))

	status, body = pollRun(t, exaAddr, run.ID)
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, string(cancelBody), body, "the cancel answered exactly the snapshot the next poll returns")

	listed = adminJobs(t, h)
	require.Len(t, listed, 1)
	require.NotNil(t, listed[0].CancelAtPoll, "a recorded cancel is on the listing")
	assert.Equal(t, 1, *listed[0].CancelAtPoll)
	assert.Equal(t, 2, listed[0].Polls)

	entries := awaitEntries(t, h, 4)
	var got []string
	for _, e := range entries {
		got = append(got, e.Method+" "+e.Outcome.Label)
	}
	assert.Equal(t, []string{
		"POST exa.agent_runs.created",
		"GET exa.agent_runs.polled.running",
		"POST exa.agent_runs.cancel.accepted",
		"GET exa.agent_runs.polled.cancelled",
	}, got)
}

// builtinsRunningAtFirstPoll is every built-in whose Exa agent-run script serves a
// non-terminal snapshot on its first poll: the runs a cancel can land on while
// they are running. It is the rule scenarios/cancel_test.go's static guard
// applies, derived here rather than named so the two cannot drift when a script
// gains or loses its running turns. The poll route's fault key is unexported by
// the profile, so this asserts on the string, as a consumer would.
func builtinsRunningAtFirstPoll(t *testing.T) []string {
	t.Helper()

	var names []string
	for _, name := range scenarios.Names() {
		s, report, err := scenarios.Load(name)
		require.NoErrorf(t, err, "%s: %v", name, report.Findings)
		entry := s.Provider(exa.NameAgentRuns)
		if entry == nil {
			continue
		}
		first, _, err := provider.SelectTurn(entry, 0, "exa:agent_runs.poll", nil)
		require.NoErrorf(t, err, "%s: the poll script must answer its first poll", name)
		var body struct {
			Status string `yaml:"status"`
		}
		require.NoError(t, first.Respond.Decode(&body), name)
		switch body.Status {
		case "completed", "failed", "cancelled":
		default:
			names = append(names, name)
		}
	}
	require.NotEmpty(t, names, "no built-in scripts a running Exa run, so this test would check nothing")
	return names
}

// TestACancelOfAShippedRunIsVisibleOnTheAdminJobListing is the same evidence for
// the reference corpus itself, through the composed binary: a consumer that
// cancels a run of any built-in scenario whose run is still running gets a
// recorded cancel, not the framework's fail-closed 500, and GET /__admin/jobs says
// so. The cancel lands before any poll, so it is recorded at position 0, which is
// the real position the listing must carry rather than omit.
func TestACancelOfAShippedRunIsVisibleOnTheAdminJobListing(t *testing.T) {
	t.Parallel()

	for _, name := range builtinsRunningAtFirstPoll(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := start(t, testConfig(t, "--scenario", "builtin:"+name), discard())
			addr := h.Addr(string(exa.Name))

			resp := post(t, addr, "/agent/runs", `{"query":"find the finding"}`)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			var run struct {
				ID string `json:"id"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&run))

			cancel := post(t, addr, "/agent/runs/"+run.ID+"/cancel", "")
			body, err := io.ReadAll(cancel.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, cancel.StatusCode, "a cancel of a running built-in run: %s", body)

			listed := adminJobs(t, h)
			require.Len(t, listed, 1)
			require.NotNil(t, listed[0].CancelAtPoll, "a recorded cancel is on the listing")
			assert.Equal(t, 0, *listed[0].CancelAtPoll)
			assert.Equal(t, 0, listed[0].Polls)
		})
	}
}

// rawExchange sends one request as raw bytes and returns the status and the
// whole response, headers included. Raw, because the request target here is the
// absolute form carrying URL userinfo, which net/http's client never puts on
// the wire: it moves userinfo into an Authorization header instead.
func rawExchange(t *testing.T, addr, method, target, header, body string) (int, string) {
	t.Helper()

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	request := method + " " + target + " HTTP/1.1\r\nHost: " + addr + "\r\n" + header + "\r\n"
	if body != "" {
		request += "Content-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n"
	}
	request += "Connection: close\r\n\r\n" + body
	_, err = conn.Write([]byte(request))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	raw, err := io.ReadAll(conn)
	require.NoError(t, err, "the server neither answered nor closed the connection")
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	require.NoError(t, err, "not an HTTP response: %s", raw)
	_ = resp.Body.Close()
	return resp.StatusCode, string(raw)
}

// TestAnAcceptedCancelKeyNeverSurvivesARoundTrip is house rule 4 on a SERVED
// cancel. The route's other credential tests all refuse the key with a 401; here
// the scenario expects the sentinel, so it is the key that WORKS, and every
// cancel is answered 200 — recorded, repeated and terminal — with the key in
// each accepted header placement, and also in the query string, in the body
// (top level and nested) and in the URL's userinfo. It must reach no response,
// no log line at any level, no journal entry and no admin listing.
func TestAnAcceptedCancelKeyNeverSurvivesARoundTrip(t *testing.T) {
	t.Parallel()

	const sentinel = "sk-live-SENTINEL-accepted-cancel"
	args := writeScenario(t, strings.Replace(cancelScenario, "  exa_agent_runs:\n",
		"  exa_agent_runs:\n    auth: {expect_key: "+sentinel+"}\n", 1))
	var logs logBuffer
	cfg := testConfig(t, append(args, "--log-level", "debug")...)
	h := start(t, cfg, NewLogger(cfg, &logs))
	addr := h.Addr(string(exa.Name))

	// Every request carries the key everywhere a client could put it.
	target := func(path string) string {
		return "http://user:" + sentinel + "@" + addr + path + "?api_key=" + sentinel + "&token=" + sentinel
	}
	const cancelBody = `{"api_key":"` + sentinel + `","auth":{"password":"` + sentinel + `"}}`

	var responses []string
	var cancelLabels []string
	for _, header := range []string{"x-api-key: " + sentinel, "Authorization: Bearer " + sentinel} {
		send := func(method, path, body string) string {
			t.Helper()
			status, raw := rawExchange(t, addr, method, target(path), header, body)
			require.Equal(t, http.StatusOK, status, "%s %s with %q: %s", method, path, header[:strings.Index(header, ":")], raw)
			responses = append(responses, raw)
			return raw
		}
		create := func() string {
			t.Helper()
			raw := send(http.MethodPost, "/agent/runs", `{"query":"find the finding"}`)
			var run struct {
				ID string `json:"id"`
			}
			require.NoError(t, json.Unmarshal([]byte(raw[strings.Index(raw, "\r\n\r\n")+4:]), &run), raw)
			require.NotEmpty(t, run.ID)
			return run.ID
		}

		cancelled := create()
		send(http.MethodPost, "/agent/runs/"+cancelled+"/cancel", cancelBody)
		send(http.MethodPost, "/agent/runs/"+cancelled+"/cancel", cancelBody)

		completed := create()
		send(http.MethodGet, "/agent/runs/"+completed, "")
		send(http.MethodGet, "/agent/runs/"+completed, "")
		send(http.MethodPost, "/agent/runs/"+completed+"/cancel", cancelBody)
	}

	for _, e := range awaitEntries(t, h, 14) {
		if strings.HasSuffix(e.Path, "/cancel") {
			cancelLabels = append(cancelLabels, e.Outcome.Label)
		}
	}
	served := []string{"exa.agent_runs.cancel.accepted", "exa.agent_runs.cancel.repeated", "exa.agent_runs.cancel.terminal"}
	require.Equal(t, append(served, served...), cancelLabels, "every cancel was served, under each placement")

	for i, raw := range responses {
		assert.NotContains(t, raw, sentinel, "response %d carried the credential", i)
	}
	require.NotEmpty(t, logs.String(), "the process must have logged, or this proves nothing")
	assert.NotContains(t, logs.String(), sentinel, "a log line carried the credential")
	for _, path := range []string{"/__admin/requests", "/__admin/jobs", "/__admin/namespaces", "/__admin/scenario"} {
		status, body := get(t, h.Addr(SurfaceAdmin), path)
		require.Equal(t, http.StatusOK, status, "%s: %s", path, body)
		assert.NotContains(t, string(body), sentinel, "%s carried the credential", path)
		if path == "/__admin/requests" {
			assert.Contains(t, string(body), "[REDACTED]",
				"the query and body were journaled masked, so this scan searched where the key would land")
		}
	}
}
