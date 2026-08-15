package provider

import (
	"strings"
	"testing"

	"github.com/c360studio/servicesim/internal/jobs"
	"github.com/c360studio/servicesim/scenario"
)

// --- MintJob and ResolveJob ------------------------------------------------

// mintExchange builds an Exchange with a PRE-CLAIMED fault decision, so a test
// can state the attempt directly rather than driving a fault engine to produce
// one. Fault() memoises on x.claimed, so seeding both is what a real claim
// leaves behind.
func mintExchange(t *testing.T, store jobs.Store, attempt *scenario.FaultAttempt) *Exchange {
	t.Helper()
	x := &Exchange{
		Deps:     Deps{Jobs: store}.Normalized(),
		Provider: Exa,
		Route:    Route{Pattern: "POST /agent/runs", FaultKey: "exa:agent_runs.create"},
		claimed:  true,
		decision: FaultDecision{Index: 0, Key: "exa:agent_runs.create", Attempt: attempt},
	}
	return x
}

// stubEncode is a deterministic stand-in for ids.Hex32: it joins its parts so a
// test can read the derivation out of the identifier instead of asserting on a
// hash nobody can verify by eye.
//
// It joins with '_' and strips everything outside the identifier charset,
// because the real encoders produce hex and UUIDs and a stub that produced
// something ValidJobID rejects would be testing a shape no caller can ship.
func stubEncode(parts ...string) string {
	joined := strings.Join(parts, "_")
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, joined)
}

func TestMintJobRecordsAServingCreate(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{})
	x := mintExchange(t, store, nil)

	job, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode)
	if !ok {
		t.Fatalf("MintJob refused a clean create: %+v", x.Findings())
	}
	if !strings.HasPrefix(job.ID, "run_") {
		t.Errorf("id = %q, want the prefix applied", job.ID)
	}
	if _, found := store.Lookup(job.Namespace, job.ID); !found {
		t.Error("a serving create must leave a record a poll can resolve")
	}
	if f := x.Findings(); len(f) != 0 {
		t.Errorf("unexpected findings: %+v", f)
	}
}

// The identifier is a pure function of stable inputs, so the same scenario and
// the same call position mint the same identifier on every run.
func TestMintJobDerivationIsDeterministic(t *testing.T) {
	t.Parallel()

	first, _ := MintJob(mintExchange(t, jobs.NewRegistry(jobs.Limits{}), nil), "exa_agent_runs", "run_", stubEncode)
	second, _ := MintJob(mintExchange(t, jobs.NewRegistry(jobs.Limits{}), nil), "exa_agent_runs", "run_", stubEncode)

	if first.ID != second.ID {
		t.Errorf("identifiers differ across runs: %q vs %q", first.ID, second.ID)
	}
	// The call index is part of the tuple, which is what makes two creates in one
	// lane distinguishable.
	if !strings.Contains(first.ID, "_job_0") {
		t.Errorf("id = %q, want the domain separator and the call index in the tuple", first.ID)
	}
}

// An encoder or prefix producing something ValidJobID rejects would record fine
// and never resolve, so every poll would 404 on a job the create reported
// making. It is a programming error rather than a request error, and it fails
// loudly at the first create instead of silently at every poll.
func TestMintJobRefusesAnUnresolvableIdentifier(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{})
	x := mintExchange(t, store, nil)

	// A prefix carrying the lane-key separator: recorded, then unresolvable.
	_, ok := MintJob(x, "exa_agent_runs", "run/", stubEncode)

	if ok {
		t.Fatal("an identifier a poll could never resolve must be refused at the create")
	}
	findings := x.Findings()
	if len(findings) != 1 || findings[0].Code != CodeJobIDInvalid {
		t.Fatalf("findings = %+v, want one %s", findings, CodeJobIDInvalid)
	}
	if got := store.StatsIn(DefaultNamespace).Count; got != 0 {
		t.Errorf("%d records written for a refused create, want 0", got)
	}
}

// TestMintJobCommitsOnlyWhenTheBodyIsDelivered is the predicate this whole
// function turns on, and both directions are failures worth naming.
//
// Committing when the body is replaced leaves a phantom job: a slot consumed for
// an identifier no client holds. NOT committing when the body IS delivered is
// worse — the client holds a real identifier no record backs, so every poll 404s
// on a job the create said it made.
func TestMintJobCommitsOnlyWhenTheBodyIsDelivered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		attempt    *scenario.FaultAttempt
		wantRecord bool
		why        string
	}{
		{
			name: "no attempt", attempt: nil, wantRecord: true,
			why: "an unfaulted create obviously delivers its body",
		},
		{
			name: "an explicit 200", attempt: &scenario.FaultAttempt{Status: 200}, wantRecord: true,
			why: "EffectiveKind only promotes to FaultStatus at 400 and above",
		},
		{
			// The case FaultDecision.Faulted() gets wrong. Faulted() is true here
			// because Delay > 0, but a pure delay writes the body after sleeping,
			// so the client receives the identifier.
			name:       "a pure delay with no kind",
			attempt:    &scenario.FaultAttempt{Delay: scenario.Duration(5)},
			wantRecord: true,
			why:        "a delay defers the body, it does not replace it",
		},
		{
			name:       "extra_fields",
			attempt:    &scenario.FaultAttempt{Kind: scenario.FaultExtraFields, ExtraFields: map[string]any{"x": 1}},
			wantRecord: true,
			why:        "extra_fields merges into the rendered body rather than replacing it",
		},
		{
			name:       "wrong_content_type",
			attempt:    &scenario.FaultAttempt{Kind: scenario.FaultWrongContentType, ContentType: "text/plain"},
			wantRecord: true,
			why:        "the body is unchanged; only the header is wrong",
		},

		{
			name: "a 429", attempt: &scenario.FaultAttempt{Status: 429}, wantRecord: false,
			why: "the provider's error envelope replaces the body, so no identifier reaches the client",
		},
		{
			name:       "empty_body",
			attempt:    &scenario.FaultAttempt{Kind: scenario.FaultEmptyBody},
			wantRecord: false,
			why:        "nothing is written",
		},
		{
			name:       "invalid_json",
			attempt:    &scenario.FaultAttempt{Kind: scenario.FaultInvalidJSON},
			wantRecord: false,
			why:        "raw non-JSON bytes replace the body",
		},
		{
			name:       "close_before_headers",
			attempt:    &scenario.FaultAttempt{Kind: scenario.FaultCloseBeforeHeaders},
			wantRecord: false,
			why:        "nothing reaches the client at all",
		},
		{
			name:       "truncate_body",
			attempt:    &scenario.FaultAttempt{Kind: scenario.FaultTruncateBody, TruncateAfterBytes: 4},
			wantRecord: false,
			why:        "a prefix then an abort; the identifier is not reliably delivered",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := jobs.NewRegistry(jobs.Limits{})
			x := mintExchange(t, store, tc.attempt)

			job, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode)
			if !ok {
				t.Fatalf("MintJob refused: %+v", x.Findings())
			}

			_, found := store.Lookup(job.Namespace, job.ID)
			if found != tc.wantRecord {
				t.Errorf("record present = %v, want %v — %s", found, tc.wantRecord, tc.why)
			}

			// The identifier is returned either way: the handler renders a body
			// whether or not Handle is about to replace it.
			if job.ID == "" {
				t.Error("MintJob must derive an identifier even when it records nothing")
			}
		})
	}
}

func TestMintJobRefusesAtTheBound(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{MaxJobs: 1})
	if _, err := store.Create(jobs.Job{ID: "taken", Namespace: DefaultNamespace}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	x := mintExchange(t, store, nil)
	_, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode)

	if ok {
		t.Fatal("a create past the bound must be refused")
	}
	findings := x.Findings()
	if len(findings) != 1 || findings[0].Code != CodeJobLimitReached {
		t.Fatalf("findings = %+v, want one %s", findings, CodeJobLimitReached)
	}
	// The message has to carry the remedy: the reader is debugging a suite that
	// worked yesterday.
	for _, want := range []string{"__admin/reset", "namespace"} {
		if !strings.Contains(findings[0].Message, want) {
			t.Errorf("message %q does not mention %q", findings[0].Message, want)
		}
	}
}

func TestMintJobReportsACollision(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{})
	x := mintExchange(t, store, nil)

	first, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode)
	if !ok {
		t.Fatalf("first create: %+v", x.Findings())
	}

	// A second create claiming the SAME index re-mints the same identifier, which
	// is what a reset that dropped cursors without dropping records produces.
	again := mintExchange(t, store, nil)
	_, ok = MintJob(again, "exa_agent_runs", "run_", stubEncode)

	if ok {
		t.Fatal("re-minting a live identifier must be refused")
	}
	findings := again.Findings()
	if len(findings) != 1 || findings[0].Code != CodeJobIDCollision {
		t.Fatalf("findings = %+v, want one %s", findings, CodeJobIDCollision)
	}
	if !strings.Contains(findings[0].Message, "reset") {
		t.Errorf("the collision message must name its usual cause: %q", findings[0].Message)
	}
	if _, found := store.Lookup(first.Namespace, first.ID); !found {
		t.Error("the refused create must not disturb the live record")
	}
}

// The near-limit warning has to arrive while creates still succeed.
func TestMintJobWarnsBeforeTheBound(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{MaxJobs: 4})
	for i := range 3 {
		if _, err := store.Create(jobs.Job{ID: "seed" + string(rune('a'+i)), Namespace: DefaultNamespace}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	x := mintExchange(t, store, nil)
	if _, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode); !ok {
		t.Fatalf("the create must still succeed: %+v", x.Findings())
	}

	findings := x.Findings()
	if len(findings) != 1 || findings[0].Code != CodeJobLimitNear {
		t.Fatalf("findings = %+v, want one %s", findings, CodeJobLimitNear)
	}
	if x.Failed() {
		t.Error("a near-limit warning must not fail the request")
	}
}

// A zero Deps serves without job state, matching how a nil Faults serves without
// faults: the create still answers, and only the poll after it cannot resolve.
func TestMintJobWithNoStore(t *testing.T) {
	t.Parallel()

	x := mintExchange(t, nil, nil)
	job, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode)

	if !ok || job.ID == "" {
		t.Errorf("a nil store must still answer: ok=%v id=%q", ok, job.ID)
	}
	if f := x.Findings(); len(f) != 0 {
		t.Errorf("unexpected findings: %+v", f)
	}
}

func TestResolveJob(t *testing.T) {
	t.Parallel()

	store := jobs.NewRegistry(jobs.Limits{})
	x := mintExchange(t, store, nil)
	job, ok := MintJob(x, "exa_agent_runs", "run_", stubEncode)
	if !ok {
		t.Fatalf("MintJob: %+v", x.Findings())
	}

	poll := mintExchange(t, store, nil)

	got, found := ResolveJob(poll, job.ID)
	if !found || got.ID != job.ID {
		t.Errorf("ResolveJob = (%+v, %v), want the minted job", got, found)
	}
	if _, found := ResolveJob(poll, "run_unknown"); found {
		t.Error("an unknown identifier must not resolve")
	}
	// A malformed identifier is refused before it reaches the store, so it can
	// never be used to probe another namespace's lane key.
	if _, found := ResolveJob(poll, "run/../other"); found {
		t.Error("a malformed identifier must not resolve")
	}
	if f := poll.Findings(); len(f) != 0 {
		t.Errorf("ResolveJob must record no findings of its own: %+v", f)
	}
}

func TestValidJobID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"exa shape", "run_9f2c1ab4e5d67890abcdef0123456789", true},
		{"tavily uuid shape", "3f2504e0-4f89-11d3-9a0c-0305e82c3301", true},
		{"single character", "a", true},
		{"underscores and dashes", "a_b-c", true},
		{"at the length bound", strings.Repeat("a", MaxJobIDLen), true},

		{"empty", "", false},
		{"one over the length bound", strings.Repeat("a", MaxJobIDLen+1), false},

		// The percent-decoded separator is the whole reason this exists: ServeMux
		// hands "run%2Fabc" to PathValue as "run/abc", and a lane key joins its
		// namespace with '/'.
		{"embedded slash", "run/abc", false},
		{"leading slash", "/run", false},
		{"the lane key separator", "run|abc", false},
		{"dot, which a path join would treat as traversal", "..", false},
		{"space", "run abc", false},
		{"newline, which a log parser would misread", "run\nabc", false},
		{"null byte", "run\x00abc", false},
		{"non-ascii", "rün", false},
		{"percent, undecoded", "run%2Fabc", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidJobID(tc.id); got != tc.want {
				t.Errorf("ValidJobID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

// A valid identifier must never be the value appendLanePart drops for length,
// or a legal poll would fall back to the shared route lane and two jobs would
// answer from one cursor. This pins the relationship rather than the numbers.
func TestValidJobIDFitsInALaneValue(t *testing.T) {
	t.Parallel()

	if MaxJobIDLen >= maxLaneValueLen {
		t.Fatalf("MaxJobIDLen (%d) must be below maxLaneValueLen (%d), or a valid identifier can be dropped from its own lane key",
			MaxJobIDLen, maxLaneValueLen)
	}
}

// ValidJobID must reject everything ValidNamespace does, because both feed the
// same lane key and the key is only unambiguous if neither half can carry a
// separator.
func TestValidJobIDRejectsEverySeparator(t *testing.T) {
	t.Parallel()

	for _, sep := range []string{namespaceSeparator, laneKeySeparator, "/", "|"} {
		id := "run" + sep + "abc"
		if ValidJobID(id) {
			t.Errorf("ValidJobID(%q) = true; a separator in an identifier lets a lane key be re-split", id)
		}
	}
}
