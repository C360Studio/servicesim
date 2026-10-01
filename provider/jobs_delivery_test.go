package provider

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/scenario"
)

// deliveryRow is one scripted create attempt and the single fact that decides
// whether it may leave a job behind: does the CLIENT end up holding the
// identifier the create minted, in a response it could act on?
type deliveryRow struct {
	name    string
	attempt *scenario.FaultAttempt
	// delivered is whether the client receives a usable identifier — a complete
	// response, status below 400, carrying the handler's own body. A job is
	// recorded if and only if this is true.
	delivered bool
	why       string
}

// scriptedBody is the body: override the rows below use. It deliberately carries
// no "id", so a response built from it can never be mistaken for the handler's.
var scriptedBody = map[string]any{"scripted": true}

// deliveryTable is the truth table deliversBody must agree with, one row per
// shape the executor treats differently. It is shared by the unit test that
// asks MintJob directly and by the end-to-end test that asks a real client, so
// the two cannot drift apart.
//
// Every fault kind scenario declares must appear here at least once
// (TestDeliveryTableNamesEveryFaultKind), and the end-to-end test checks each
// row's `delivered` against what a real client observes, so a row cannot be
// written from belief alone.
func deliveryTable() []deliveryRow {
	return []deliveryRow{
		// --- the client receives the handler's body --------------------------------
		{name: "no attempt", attempt: nil, delivered: true,
			why: "an unfaulted create delivers its body"},
		{name: "an empty attempt", attempt: &scenario.FaultAttempt{}, delivered: true,
			why: "kind none renders the scenario response"},
		{name: "an explicit 2xx", attempt: &scenario.FaultAttempt{Status: 201}, delivered: true,
			why: "EffectiveKind only promotes to FaultStatus at 400 and above"},
		{name: "a 205", attempt: &scenario.FaultAttempt{Status: 205}, delivered: true,
			why: "only 204 and 304 are the 2xx and 3xx statuses net/http strips the body from"},
		{name: "a 103", attempt: &scenario.FaultAttempt{Status: 103}, delivered: true,
			why: "net/http sends a 1xx other than 101 as an interim response and the body follows it under a 200"},
		{name: "a pure delay", attempt: &scenario.FaultAttempt{Delay: scenario.Duration(5)}, delivered: true,
			why: "a delay defers the body, it does not replace it"},
		{name: "delay_after_headers", attempt: &scenario.FaultAttempt{DelayAfterHeaders: scenario.Duration(5)}, delivered: true,
			why: "the hang sits between the headers and the body, which is still written"},
		{name: "error and tag below 400", attempt: &scenario.FaultAttempt{Error: "boom", Tag: "T"}, delivered: true,
			why: "faultBody only reaches the provider's FaultBody at status >= 400 or with a body:, so these are never read"},
		{name: "kind status below 400", attempt: &scenario.FaultAttempt{Kind: scenario.FaultStatus, Status: 200}, delivered: true,
			why: "an explicit kind: status below 400 still writes the rendered body"},
		{name: "extra_fields", attempt: &scenario.FaultAttempt{Kind: scenario.FaultExtraFields, ExtraFields: map[string]any{"x": 1}},
			delivered: true, why: "extra_fields merges into the rendered body rather than replacing it"},
		{name: "wrong_content_type", attempt: &scenario.FaultAttempt{Kind: scenario.FaultWrongContentType, ContentType: "text/plain"},
			delivered: true, why: "the body is unchanged; only the header is wrong"},
		{name: "oversized_body", attempt: &scenario.FaultAttempt{Kind: scenario.FaultOversizedBody, BodyBytes: 4096},
			delivered: true, why: "the rendered body is written whole, then padded with insignificant whitespace"},
		{name: "oversized_body inferred from body_bytes", attempt: &scenario.FaultAttempt{BodyBytes: 4096}, delivered: true,
			why: "EffectiveKind infers oversized_body from body_bytes alone"},
		{name: "stream_disconnect on a non-streaming create",
			attempt:   &scenario.FaultAttempt{Kind: scenario.FaultStreamDisconnect},
			delivered: true, why: "Handle drops an attempt that cannot apply and serves the body, reporting the mismatch"},
		{name: "stream_truncate_chunk on a non-streaming create",
			attempt:   &scenario.FaultAttempt{Kind: scenario.FaultStreamTruncateChunk},
			delivered: true, why: "Handle drops an attempt that cannot apply and serves the body, reporting the mismatch"},
		{name: "stream_stall on a non-streaming create",
			attempt:   &scenario.FaultAttempt{Kind: scenario.FaultStreamStall},
			delivered: true, why: "Handle drops an attempt that cannot apply and serves the body, reporting the mismatch"},

		// --- the body: override replaces it, whatever the status -------------------
		{name: "body override with no status", attempt: &scenario.FaultAttempt{Body: scriptedBody}, delivered: false,
			why: "faultBody swaps in the attempt's body even below 400, so the response carries no identifier"},
		{name: "body override at 201", attempt: &scenario.FaultAttempt{Status: 201, Body: scriptedBody}, delivered: false,
			why: "a success status does not stop the override"},
		{name: "extra_fields over a body override",
			attempt:   &scenario.FaultAttempt{Kind: scenario.FaultExtraFields, ExtraFields: map[string]any{"x": 1}, Body: scriptedBody},
			delivered: false, why: "the extras are merged into the override, not the rendered body"},
		{name: "wrong_content_type over a body override",
			attempt:   &scenario.FaultAttempt{Kind: scenario.FaultWrongContentType, Body: scriptedBody},
			delivered: false, why: "the override replaces the body; only then is the header changed"},
		{name: "oversized_body over a body override",
			attempt:   &scenario.FaultAttempt{Kind: scenario.FaultOversizedBody, BodyBytes: 4096, Body: scriptedBody},
			delivered: false, why: "the override is what gets padded"},

		// --- a status that carries no body -----------------------------------------
		{name: "a 204", attempt: &scenario.FaultAttempt{Status: 204}, delivered: false,
			why: "net/http writes no body for a 204, so the identifier never leaves the server"},
		{name: "a 304", attempt: &scenario.FaultAttempt{Status: 304}, delivered: false,
			why: "net/http writes no body for a 304, so the identifier never leaves the server"},
		{name: "a 101", attempt: &scenario.FaultAttempt{Status: 101}, delivered: false,
			why: "net/http writes 101 as the final status, and a 101 carries no body"},

		// --- an error status replaces it -------------------------------------------
		{name: "a 400", attempt: &scenario.FaultAttempt{Status: 400}, delivered: false,
			why: "400 is the lowest status the provider's error envelope replaces the body for"},
		{name: "a 429", attempt: &scenario.FaultAttempt{Status: 429}, delivered: false,
			why: "the provider's error envelope replaces the body"},
		{name: "a 500 with error and tag", attempt: &scenario.FaultAttempt{Status: 500, Error: "boom", Tag: "T"}, delivered: false,
			why: "error and tag fill the envelope that replaces the body"},
		{name: "extra_fields at 429",
			attempt:   &scenario.FaultAttempt{Kind: scenario.FaultExtraFields, Status: 429, ExtraFields: map[string]any{"x": 1}},
			delivered: false, why: "the extras are merged into the error envelope"},
		{name: "wrong_content_type at 503",
			attempt:   &scenario.FaultAttempt{Kind: scenario.FaultWrongContentType, Status: 503},
			delivered: false, why: "the error envelope replaces the body"},
		{name: "oversized_body at 503",
			attempt:   &scenario.FaultAttempt{Kind: scenario.FaultOversizedBody, BodyBytes: 4096, Status: 503},
			delivered: false, why: "the error envelope is what gets padded"},

		// --- the transport or the bytes are destroyed ------------------------------
		{name: "empty_body", attempt: &scenario.FaultAttempt{Kind: scenario.FaultEmptyBody}, delivered: false,
			why: "nothing is written"},
		{name: "invalid_json", attempt: &scenario.FaultAttempt{Kind: scenario.FaultInvalidJSON}, delivered: false,
			why: "raw non-JSON bytes replace the body"},
		{name: "invalid_json with a raw_body", attempt: &scenario.FaultAttempt{RawBody: "not json"}, delivered: false,
			why: "raw_body infers invalid_json"},
		{name: "close_before_headers", attempt: &scenario.FaultAttempt{Kind: scenario.FaultCloseBeforeHeaders}, delivered: false,
			why: "nothing reaches the client at all"},
		{name: "truncate_body", attempt: &scenario.FaultAttempt{Kind: scenario.FaultTruncateBody, TruncateAfterBytes: 4},
			delivered: false, why: "a prefix then an abort; the client cannot decode it"},
	}
}

// TestMintJobRecordsExactlyWhenTheBodyIsDelivered asks MintJob directly, with a
// pre-claimed decision, for every row of the table.
func TestMintJobRecordsExactlyWhenTheBodyIsDelivered(t *testing.T) {
	t.Parallel()

	for _, tc := range deliveryTable() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := jobs.NewRegistry(jobs.Limits{})
			x := mintExchange(t, store, tc.attempt)

			id, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode)
			require.True(t, ok, "MintJob refused: %+v", x.Findings())
			require.NotEmpty(t, id, "MintJob must derive an identifier even when it records nothing")

			_, found := store.Lookup(DefaultNamespace, id)
			require.Equal(t, tc.delivered, found, "record present = %v, want %v — %s", found, tc.delivered, tc.why)
		})
	}
}

// TestACreateLeavesAJobExactlyWhenTheClientHoldsItsIdentifier is the end-to-end
// half: it sends a real create through Handle and the real executor, reads what a
// client reads, and requires the job store to agree.
//
// deliversBody predicts the response at MintJob time, before the handler has
// returned and before Handle applies the fault; the executor is what decides. A
// prediction that drifts from it fails in one of two ways, both silent: a client
// holding an identifier no record backs (every poll 404s), or a record no client
// holds an identifier for (a slot gone, and a bound reached sooner than the
// scenario's author computed).
func TestACreateLeavesAJobExactlyWhenTheClientHoldsItsIdentifier(t *testing.T) {
	t.Parallel()

	for _, tc := range deliveryTable() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := jobs.NewRegistry(jobs.Limits{})
			engine := &scriptedFaults{}
			if tc.attempt != nil {
				engine.attempts = []scenario.FaultAttempt{*tc.attempt}
			}

			minted := make(chan string, 1)
			handler := func(x *Exchange) Response {
				id, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode)
				if !ok {
					t.Errorf("MintJob refused: %+v", x.Findings())
				}
				minted <- id
				return Response{
					Status:        http.StatusCreated,
					Body:          []byte(`{"id":"` + id + `","status":"queued"}`),
					Label:         "test.created",
					FaultEligible: true,
					// Shaped like the in-tree providers' FaultBody: the attempt's own
					// body when it has one, otherwise an error envelope.
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

			client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
			t.Cleanup(client.CloseIdleConnections)

			held := clientHeldIdentifier(t, client, srv.URL+"/agent/runs")
			id := <-minted
			require.NotEmpty(t, id)

			// The table row is itself checked against reality, so it documents what
			// the executor does rather than what its author believed.
			if tc.delivered {
				require.Equal(t, id, held, "the client should hold the minted identifier — %s", tc.why)
			} else {
				require.Empty(t, held, "the client must not hold an identifier — %s", tc.why)
			}

			_, found := store.Lookup(DefaultNamespace, id)
			require.Equal(t, held != "", found,
				"a job must exist exactly when the client holds its identifier (held=%q, record present=%v)", held, found)
			if found {
				require.Equal(t, 1, store.StatsIn(DefaultNamespace).Count)
			} else {
				require.Zero(t, store.StatsIn(DefaultNamespace).Count, "a phantom job consumes a slot no client can use")
			}
		})
	}
}

// clientHeldIdentifier does what a create-then-poll client does: it POSTs, and
// takes the identifier only from a complete response with a status below 400 and
// a JSON body carrying "id". It returns "" for every other outcome — a transport
// error, a truncated read, an error status, a body with no id.
func clientHeldIdentifier(t *testing.T, client *http.Client, url string) string {
	t.Helper()

	resp, err := client.Post(url, "application/json", strings.NewReader(`{"query":"q"}`))
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode >= http.StatusBadRequest {
		return ""
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return ""
	}
	return out.ID
}

// TestDeliveryTableNamesEveryFaultKind is what makes the NEXT fault kind force a
// decision here instead of silently falling out of deliversBody as "no job".
//
// The kinds are read from the scenario package's own source — every constant
// declared as `X FaultKind = "lit"` or `X = FaultKind("lit")`, and any other form
// of a FaultKind constant fails the test — rather than from a second list that
// would itself need remembering. A new kind that has no row in deliveryTable fails this test, and
// the row it needs is checked against the real executor by
// TestACreateLeavesAJobExactlyWhenTheClientHoldsItsIdentifier, so the decision
// cannot be made from belief.
func TestDeliveryTableNamesEveryFaultKind(t *testing.T) {
	t.Parallel()

	covered := map[scenario.FaultKind]bool{}
	for _, row := range deliveryTable() {
		if row.attempt != nil {
			covered[row.attempt.EffectiveKind()] = true
		}
	}

	kinds := declaredFaultKinds(t)
	// A parse that silently matched nothing would pass forever.
	require.GreaterOrEqual(t, len(kinds), 12, "found too few FaultKind constants in ../scenario: %v", kinds)

	for _, kind := range kinds {
		if !covered[kind] {
			t.Errorf("fault kind %q has no row in deliveryTable: decide in deliversBody (provider/jobs.go) whether "+
				"a create attempt of this kind still delivers the handler's body, then add the row", kind)
		}
	}
}

// declaredFaultKinds returns every fault kind the scenario package's non-test
// sources declare as a constant, in either of the two spellings it can be read
// from: `X FaultKind = "lit"` and `X = FaultKind("lit")`. A constant declared
// with the FaultKind type whose value is neither fails the test rather than being
// skipped, so a new spelling is a loud failure here instead of a kind that
// silently escapes the table. What this cannot see is a kind that carries
// neither the type nor a conversion — an iota continuation, or a var.
func declaredFaultKinds(t *testing.T) []scenario.FaultKind {
	t.Helper()

	const dir = "../scenario"
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var kinds []scenario.FaultKind
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err)

		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				typed := isIdent(vs.Type, "FaultKind")
				for _, v := range vs.Values {
					value, converted, ok := faultKindLiteral(v)
					switch {
					case ok && (typed || converted):
						kinds = append(kinds, scenario.FaultKind(value))
					case typed:
						t.Errorf("%s: a FaultKind constant whose value is not a string literal or a "+
							"FaultKind(\"...\") conversion cannot be read by this test; spell it one of those ways "+
							"or teach declaredFaultKinds the new form", fset.Position(v.Pos()))
					}
				}
			}
		}
	}
	return kinds
}

// faultKindLiteral reads the string out of "lit" or FaultKind("lit"), and says
// which of the two it was: a bare literal is a fault kind only when its constant
// is typed as one, a conversion is one wherever it appears.
func faultKindLiteral(expr ast.Expr) (value string, converted, ok bool) {
	if call, isCall := expr.(*ast.CallExpr); isCall && isIdent(call.Fun, "FaultKind") && len(call.Args) == 1 {
		expr, converted = call.Args[0], true
	}
	lit, isLit := expr.(*ast.BasicLit)
	if !isLit || lit.Kind != token.STRING {
		return "", false, false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, converted, err == nil
}

func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}
