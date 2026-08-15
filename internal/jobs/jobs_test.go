package jobs

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Registry must satisfy the seam the provider package consumes. A compile-time
// assertion, because the interface exists to be substituted and a drift between
// the two would otherwise surface only at the wiring site.
var _ Store = (*Registry)(nil)

func job(namespace, id string) Job {
	return Job{
		ID:          id,
		Namespace:   namespace,
		Entry:       "exa_agent_runs",
		LaneKey:     "exa:agent_runs.create",
		CreateIndex: 0,
		CreatedAt:   time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestCreateAndLookup(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{})

	stats, err := r.Create(job("t-1", "run_a"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if stats.Count != 1 || stats.Bound != DefaultMaxJobs {
		t.Errorf("stats = %+v, want {1 %d}", stats, DefaultMaxJobs)
	}

	got, ok := r.Lookup("t-1", "run_a")
	if !ok {
		t.Fatal("a record just created must be findable")
	}
	if got.Entry != "exa_agent_runs" || got.LaneKey != "exa:agent_runs.create" {
		t.Errorf("record = %+v", got)
	}

	if _, ok := r.Lookup("t-1", "run_missing"); ok {
		t.Error("an unknown id must not resolve")
	}
}

// The namespace is half the key. Identifiers are derived WITHOUT the namespace
// so a golden file is portable between a namespaced run and an unnamespaced one,
// which means two concurrent namespaces legitimately mint the same identifier at
// the same call position. Keying on id alone would collide them.
func TestNamespacesAreIsolated(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{})

	if _, err := r.Create(job("t-1", "run_same")); err != nil {
		t.Fatalf("first namespace: %v", err)
	}
	if _, err := r.Create(job("t-2", "run_same")); err != nil {
		t.Fatalf("the same identifier in another namespace is not a duplicate: %v", err)
	}

	if _, ok := r.Lookup("t-1", "run_same"); !ok {
		t.Error("t-1 lost its record")
	}
	if _, ok := r.Lookup("t-2", "run_same"); !ok {
		t.Error("t-2 lost its record")
	}
	if _, ok := r.Lookup("t-3", "run_same"); ok {
		t.Error("a namespace that created nothing must resolve nothing")
	}
}

// An empty namespace is the default lane in both directions. A record created
// with none must be findable by a poll that names none, or the unnamespaced
// single-test case — the common one — never works.
func TestEmptyNamespaceIsTheDefaultLane(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{})

	if _, err := r.Create(job("", "run_a")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, name := range []string{"", DefaultNamespace} {
		if _, ok := r.Lookup(name, "run_a"); !ok {
			t.Errorf("Lookup(%q) did not find the record created with no namespace", name)
		}
	}
	if got := r.StatsIn("").Count; got != 1 {
		t.Errorf("StatsIn(\"\").Count = %d, want 1", got)
	}
}

func TestDuplicateIsRefused(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{})

	if _, err := r.Create(job("t-1", "run_a")); err != nil {
		t.Fatalf("first create: %v", err)
	}

	second := job("t-1", "run_a")
	second.CreateIndex = 7
	stats, err := r.Create(second)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
	if stats.Count != 1 {
		t.Errorf("a refused create must not change the count: %+v", stats)
	}

	// The original record survives intact — a duplicate must not overwrite.
	got, _ := r.Lookup("t-1", "run_a")
	if got.CreateIndex != 0 {
		t.Errorf("the refused create overwrote the live record: %+v", got)
	}
}

// The bound refuses and never evicts. An evicted record would turn a poll for a
// successfully created job into the vendor's 404 — indistinguishable from the
// multi-replica divergence, manufactured locally.
func TestBoundRefusesAndNeverEvicts(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{MaxJobs: 3})

	for i := range 3 {
		if _, err := r.Create(job("t-1", "run_"+strconv.Itoa(i))); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	stats, err := r.Create(job("t-1", "run_overflow"))
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("err = %v, want ErrLimit", err)
	}
	if stats.Count != 3 || stats.Bound != 3 {
		t.Errorf("stats = %+v, want {3 3}", stats)
	}

	// Every earlier record is still resolvable: nothing was evicted to make room.
	for i := range 3 {
		if _, ok := r.Lookup("t-1", "run_"+strconv.Itoa(i)); !ok {
			t.Errorf("run_%d was evicted; the bound must refuse, not evict", i)
		}
	}
	if _, ok := r.Lookup("t-1", "run_overflow"); ok {
		t.Error("a refused create must record nothing")
	}
}

// The bound is per namespace, so one full namespace does not refuse another.
func TestBoundIsPerNamespace(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{MaxJobs: 1})

	if _, err := r.Create(job("t-1", "run_a")); err != nil {
		t.Fatalf("t-1: %v", err)
	}
	if _, err := r.Create(job("t-1", "run_b")); !errors.Is(err, ErrLimit) {
		t.Fatalf("t-1 second: err = %v, want ErrLimit", err)
	}
	if _, err := r.Create(job("t-2", "run_a")); err != nil {
		t.Fatalf("a full namespace must not refuse another: %v", err)
	}
}

func TestStatsNearAndFull(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		stats    Stats
		wantNear bool
		wantFull bool
	}{
		{"empty", Stats{Count: 0, Bound: 10}, false, false},
		{"below the mark", Stats{Count: 7, Bound: 10}, false, false},
		{"exactly at the mark", Stats{Count: 8, Bound: 10}, true, false},
		{"above the mark", Stats{Count: 9, Bound: 10}, true, false},
		{"full", Stats{Count: 10, Bound: 10}, true, true},
		{"over-full cannot happen but must not panic", Stats{Count: 11, Bound: 10}, true, true},
		// A zero Stats must not report Near: a caller asking about a namespace
		// that holds nothing would otherwise warn about it.
		{"zero value", Stats{}, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.stats.Near(); got != tc.wantNear {
				t.Errorf("Near() = %v, want %v", got, tc.wantNear)
			}
			if got := tc.stats.Full(); got != tc.wantFull {
				t.Errorf("Full() = %v, want %v", got, tc.wantFull)
			}
		})
	}
}

// The high-water signal has to arrive while creates still succeed, or it is not
// a warning, it is a second way of saying the same failure.
func TestNearFiresBeforeTheBoundIsReached(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{MaxJobs: 10})

	firstNear := -1
	for i := range 10 {
		stats, err := r.Create(job("t-1", "run_"+strconv.Itoa(i)))
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		if stats.Near() && firstNear < 0 {
			firstNear = i
		}
	}
	if firstNear != 7 {
		t.Fatalf("first Near at create %d, want 7 (the 8th of 10)", firstNear)
	}
	if firstNear >= 9 {
		t.Error("the warning must arrive with creates still to spare")
	}
}

func TestResetInReturnsSlotsAndLeavesOthersAlone(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{MaxJobs: 2})

	for _, ns := range []string{"t-1", "t-2"} {
		for i := range 2 {
			if _, err := r.Create(job(ns, "run_"+strconv.Itoa(i))); err != nil {
				t.Fatalf("%s create %d: %v", ns, i, err)
			}
		}
	}
	if _, err := r.Create(job("t-1", "run_x")); !errors.Is(err, ErrLimit) {
		t.Fatalf("t-1 should be full: %v", err)
	}

	r.ResetIn("t-1")

	if got := r.StatsIn("t-1").Count; got != 0 {
		t.Errorf("t-1 count after reset = %d, want 0", got)
	}
	if got := r.StatsIn("t-2").Count; got != 2 {
		t.Errorf("t-2 count = %d, want 2 — ResetIn must not touch another namespace", got)
	}
	if _, ok := r.Lookup("t-2", "run_0"); !ok {
		t.Error("t-2 lost a record to t-1's reset")
	}

	// The slots came back: a suite that runs more tests than the bound depends
	// on teardown handing them back.
	if _, err := r.Create(job("t-1", "run_fresh")); err != nil {
		t.Errorf("ResetIn must return the namespace's slots: %v", err)
	}
}

// Resetting a namespace that holds nothing is not an error. Teardown runs
// whether or not a test created a job.
func TestResetInOnAnEmptyNamespace(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{})
	r.ResetIn("never-used")

	if got := r.StatsIn("never-used").Count; got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
}

func TestReset(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{})
	for _, ns := range []string{"t-1", "t-2", ""} {
		if _, err := r.Create(job(ns, "run_a")); err != nil {
			t.Fatalf("%q: %v", ns, err)
		}
	}

	r.Reset()

	for _, ns := range []string{"t-1", "t-2", "", DefaultNamespace} {
		if got := r.StatsIn(ns).Count; got != 0 {
			t.Errorf("StatsIn(%q).Count = %d after Reset, want 0", ns, got)
		}
	}
	if _, err := r.Create(job("t-1", "run_a")); err != nil {
		t.Errorf("Reset must return every slot: %v", err)
	}
}

func TestLimitsDefaults(t *testing.T) {
	t.Parallel()

	for _, in := range []int{0, -1, -256} {
		if got := NewRegistry(Limits{MaxJobs: in}).StatsIn("t-1").Bound; got != DefaultMaxJobs {
			t.Errorf("MaxJobs %d resolved to bound %d, want %d", in, got, DefaultMaxJobs)
		}
	}
	if got := NewRegistry(Limits{MaxJobs: 7}).StatsIn("t-1").Bound; got != 7 {
		t.Errorf("bound = %d, want 7", got)
	}
}

// --- concurrency -----------------------------------------------------------

// The bound must be exact under concurrency: N goroutines racing to create into
// one namespace bounded at K must produce exactly K records, never K+1. A
// check-then-insert that released the lock between the two would overshoot.
func TestConcurrentCreatesRespectTheBoundExactly(t *testing.T) {
	t.Parallel()

	const (
		bound   = 32
		writers = 200
	)
	r := NewRegistry(Limits{MaxJobs: bound})

	var wg sync.WaitGroup
	var mu sync.Mutex
	var created, limited int

	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Create(job("t-1", "run_"+strconv.Itoa(i)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, ErrLimit):
				limited++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if created != bound {
		t.Errorf("created = %d, want exactly %d", created, bound)
	}
	if limited != writers-bound {
		t.Errorf("limited = %d, want %d", limited, writers-bound)
	}
	if got := r.StatsIn("t-1").Count; got != bound {
		t.Errorf("StatsIn count = %d, want %d", got, bound)
	}
}

// Exactly one of N goroutines racing on the SAME identifier may win. The rest
// must see ErrDuplicate rather than silently overwriting each other.
func TestConcurrentCreatesOfOneIDElectOneWinner(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{})

	var wg sync.WaitGroup
	var mu sync.Mutex
	var won, duplicate int

	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j := job("t-1", "run_contested")
			j.CreateIndex = i
			_, err := r.Create(j)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrDuplicate):
				duplicate++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if won != 1 {
		t.Errorf("winners = %d, want exactly 1", won)
	}
	if duplicate != 99 {
		t.Errorf("duplicates = %d, want 99", duplicate)
	}
}

// Reads, writes and resets across namespaces must not race. This asserts no
// invariant beyond "-race stays quiet", which is the point: the store is reached
// from every request goroutine and the failure it guards against is a corrupt
// map rather than a wrong answer.
func TestConcurrentMixedAccess(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{MaxJobs: 16})

	var wg sync.WaitGroup
	for w := range 8 {
		ns := fmt.Sprintf("t-%d", w%3)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				id := "run_" + strconv.Itoa(i)
				_, _ = r.Create(job(ns, id))
				_, _ = r.Lookup(ns, id)
				_ = r.StatsIn(ns)
				if i%17 == 0 {
					r.ResetIn(ns)
				}
			}
		}()
	}
	wg.Wait()
}

// A reset racing creates must leave the store coherent: every record the store
// still reports must be resolvable, and the count must never exceed the bound.
func TestResetRacingCreatesLeavesTheStoreCoherent(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{MaxJobs: 8})

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := range 500 {
			_, _ = r.Create(job("t-1", "run_"+strconv.Itoa(i)))
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			r.ResetIn("t-1")
		}
	}()
	wg.Wait()

	stats := r.StatsIn("t-1")
	if stats.Count > stats.Bound {
		t.Errorf("count %d exceeds bound %d after a reset race", stats.Count, stats.Bound)
	}
}
