package jobs

import (
	"strconv"
	"sync"
	"testing"
)

// A new job has made no polls and carries no cancel, whatever the caller put in
// those fields: the lifecycle state is the store's to advance, through Advance
// and MarkCancel alone, so a create cannot smuggle a cancel past the
// compare-and-set that guards it.
func TestCreateRecordsAZeroLifecycle(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{})
	j := job("t-1", "run_a")
	j.Polls, j.CancelRequested, j.CancelAtPoll = 7, true, 3
	if _, err := r.Create(j); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, ok := r.Lookup("t-1", "run_a")
	if !ok {
		t.Fatal("the record just created must resolve")
	}
	if got.Polls != 0 || got.CancelRequested || got.CancelAtPoll != 0 {
		t.Errorf("lifecycle = {Polls:%d CancelRequested:%v CancelAtPoll:%d}, want the zero state",
			got.Polls, got.CancelRequested, got.CancelAtPoll)
	}
}

// Advance keeps the HIGHEST claimed poll position, so polls whose Advance calls
// land out of claim order still leave Polls equal to the poll lane's claimed
// count.
func TestAdvanceKeepsTheHighestClaimedPosition(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		advances []int
		want     int
	}{
		{name: "no poll", advances: nil, want: 0},
		{name: "first poll", advances: []int{0}, want: 1},
		{name: "in order", advances: []int{0, 1, 2}, want: 3},
		{name: "out of order", advances: []int{2, 0, 1}, want: 3},
		{name: "a repeated index does not count twice", advances: []int{0, 0}, want: 1},
		{name: "a lower index never lowers it", advances: []int{4, 1}, want: 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			r := NewRegistry(Limits{})
			if _, err := r.Create(job("t-1", "run_a")); err != nil {
				t.Fatalf("Create: %v", err)
			}
			for _, i := range c.advances {
				after, ok := r.Advance("t-1", "run_a", i)
				if !ok {
					t.Fatalf("Advance(%d) reported not found for a live job", i)
				}
				if after.Polls < i+1 {
					t.Fatalf("Advance(%d) returned Polls %d, want at least %d", i, after.Polls, i+1)
				}
			}
			got, _ := r.Lookup("t-1", "run_a")
			if got.Polls != c.want {
				t.Errorf("Polls = %d, want %d", got.Polls, c.want)
			}
		})
	}
}

// Advance returns the record as it stands after the write, and the copy it
// returns is the caller's: changing it changes nothing in the store.
func TestAdvanceReturnsACopy(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{})
	if _, err := r.Create(job("t-1", "run_a")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	after, _ := r.Advance("t-1", "run_a", 0)
	after.Polls = 99
	after.CancelRequested = true

	got, _ := r.Lookup("t-1", "run_a")
	if got.Polls != 1 || got.CancelRequested {
		t.Errorf("stored record = {Polls:%d CancelRequested:%v}, want {1 false}", got.Polls, got.CancelRequested)
	}
}

// MarkCancel is a compare-and-set, and each of its four outcomes is a different
// reason a caller must be able to tell apart: marked is done, already marked is
// "answer from the cancel script", position moved is "a poll landed, re-peek and
// retry", and not found is "a reset dropped the job".
func TestMarkCancelOutcomes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		create     bool
		polls      int // advances applied before the mark
		premark    bool
		atPoll     int
		want       MarkOutcome
		wantAtPoll int
	}{
		{name: "marked at the current position", create: true, polls: 2, atPoll: 2, want: Marked, wantAtPoll: 2},
		{name: "marked before any poll", create: true, polls: 0, atPoll: 0, want: Marked, wantAtPoll: 0},
		{name: "a poll landed in between", create: true, polls: 3, atPoll: 2, want: PositionMoved},
		{name: "a stale position from the future", create: true, polls: 1, atPoll: 2, want: PositionMoved},
		{name: "already marked", create: true, polls: 1, premark: true, atPoll: 1, want: AlreadyMarked, wantAtPoll: 1},
		{
			// The marker is the more important fact: once a cancel is recorded the
			// caller answers from the cancel script, wherever the position is now.
			name: "already marked wins over a moved position", create: true, polls: 1, premark: true, atPoll: 0,
			want: AlreadyMarked, wantAtPoll: 1,
		},
		{name: "not found", create: false, atPoll: 0, want: NotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			r := NewRegistry(Limits{})
			if c.create {
				if _, err := r.Create(job("t-1", "run_a")); err != nil {
					t.Fatalf("Create: %v", err)
				}
				for i := range c.polls {
					r.Advance("t-1", "run_a", i)
				}
				if c.premark {
					if _, got := r.MarkCancel("t-1", "run_a", c.polls); got != Marked {
						t.Fatalf("premark = %q, want %q", got, Marked)
					}
				}
			}

			got, outcome := r.MarkCancel("t-1", "run_a", c.atPoll)
			if outcome != c.want {
				t.Fatalf("outcome = %q, want %q", outcome, c.want)
			}
			if outcome == NotFound {
				if got != (Job{}) {
					t.Errorf("a not-found mark returned %+v, want the zero Job", got)
				}
				return
			}

			// Every other outcome returns the record as it stands, so a caller
			// decides its next step without a second Lookup.
			stored, _ := r.Lookup("t-1", "run_a")
			if got != stored {
				t.Errorf("returned record %+v, stored %+v; they must agree", got, stored)
			}
			marked := c.want == Marked || c.want == AlreadyMarked
			if got.CancelRequested != marked {
				t.Errorf("CancelRequested = %v, want %v", got.CancelRequested, marked)
			}
			if marked && got.CancelAtPoll != c.wantAtPoll {
				t.Errorf("CancelAtPoll = %d, want %d", got.CancelAtPoll, c.wantAtPoll)
			}
		})
	}
}

// Advance on a job that has been cancelled keeps counting polls and leaves the
// marker exactly where it was recorded.
func TestAdvanceAfterAMarkKeepsTheMarker(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{})
	if _, err := r.Create(job("t-1", "run_a")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	r.Advance("t-1", "run_a", 0)
	if _, outcome := r.MarkCancel("t-1", "run_a", 1); outcome != Marked {
		t.Fatalf("mark = %q, want %q", outcome, Marked)
	}

	after, _ := r.Advance("t-1", "run_a", 1)
	if after.Polls != 2 || !after.CancelRequested || after.CancelAtPoll != 1 {
		t.Errorf("record = {Polls:%d CancelRequested:%v CancelAtPoll:%d}, want {2 true 1}",
			after.Polls, after.CancelRequested, after.CancelAtPoll)
	}
}

// State is keyed on (namespace, id) exactly like the record it lives on: two
// namespaces legitimately hold the same derived identifier, and advancing or
// cancelling one must not touch the other.
func TestLifecycleIsIsolatedByNamespaceAndID(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{})
	for _, k := range [][2]string{{"t-1", "run_a"}, {"t-2", "run_a"}, {"t-1", "run_b"}} {
		if _, err := r.Create(job(k[0], k[1])); err != nil {
			t.Fatalf("Create %v: %v", k, err)
		}
	}

	r.Advance("t-1", "run_a", 0)
	r.Advance("t-1", "run_a", 1)
	if _, outcome := r.MarkCancel("t-1", "run_a", 2); outcome != Marked {
		t.Fatalf("mark = %q, want %q", outcome, Marked)
	}

	for _, k := range [][2]string{{"t-2", "run_a"}, {"t-1", "run_b"}} {
		got, _ := r.Lookup(k[0], k[1])
		if got.Polls != 0 || got.CancelRequested {
			t.Errorf("%v = {Polls:%d CancelRequested:%v}, want untouched", k, got.Polls, got.CancelRequested)
		}
	}
}

// A reset drops the state with the record. Both operations then report
// not-found — a reset can land between a request resolving its job and either
// call — and a later create of the same identifier starts from zero.
func TestResetDropsLifecycleState(t *testing.T) {
	t.Parallel()

	for _, reset := range []struct {
		name string
		do   func(*Registry)
	}{
		{name: "ResetIn", do: func(r *Registry) { r.ResetIn("t-1") }},
		{name: "Reset", do: func(r *Registry) { r.Reset() }},
	} {
		t.Run(reset.name, func(t *testing.T) {
			t.Parallel()

			r := NewRegistry(Limits{})
			if _, err := r.Create(job("t-1", "run_a")); err != nil {
				t.Fatalf("Create: %v", err)
			}
			r.Advance("t-1", "run_a", 0)
			r.MarkCancel("t-1", "run_a", 1)

			reset.do(r)

			if _, ok := r.Advance("t-1", "run_a", 1); ok {
				t.Error("Advance after a reset must report not found")
			}
			if _, outcome := r.MarkCancel("t-1", "run_a", 1); outcome != NotFound {
				t.Errorf("MarkCancel after a reset = %q, want %q", outcome, NotFound)
			}

			if _, err := r.Create(job("t-1", "run_a")); err != nil {
				t.Fatalf("re-Create: %v", err)
			}
			got, _ := r.Lookup("t-1", "run_a")
			if got.Polls != 0 || got.CancelRequested || got.CancelAtPoll != 0 {
				t.Errorf("re-created record = {Polls:%d CancelRequested:%v CancelAtPoll:%d}, want the zero state",
					got.Polls, got.CancelRequested, got.CancelAtPoll)
			}
		})
	}
}

// A storm of polls advancing one job concurrently: every index is counted,
// nothing is lost to a torn read-modify-write, and no caller ever sees a
// position below its own claim.
func TestConcurrentAdvanceStorm(t *testing.T) {
	t.Parallel()

	const polls = 200
	r := NewRegistry(Limits{})
	if _, err := r.Create(job("t-1", "run_a")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range polls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			after, ok := r.Advance("t-1", "run_a", i)
			if !ok {
				t.Errorf("Advance(%d): not found", i)
				return
			}
			if after.Polls < i+1 {
				t.Errorf("Advance(%d) returned Polls %d, below its own claim", i, after.Polls)
			}
		}()
	}
	close(start)
	wg.Wait()

	got, _ := r.Lookup("t-1", "run_a")
	if got.Polls != polls {
		t.Errorf("Polls = %d, want %d", got.Polls, polls)
	}
}

// Many cancels contending for one job with no poll in flight: the
// compare-and-set elects exactly one, and every loser is told the job is
// already marked.
func TestConcurrentMarkCancelElectsOneWinner(t *testing.T) {
	t.Parallel()

	const markers = 100
	r := NewRegistry(Limits{})
	if _, err := r.Create(job("t-1", "run_a")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	outcomes := make(chan MarkOutcome, markers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range markers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, outcome := r.MarkCancel("t-1", "run_a", 0)
			outcomes <- outcome
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	counts := map[MarkOutcome]int{}
	for o := range outcomes {
		counts[o]++
	}
	if counts[Marked] != 1 || counts[AlreadyMarked] != markers-1 {
		t.Errorf("outcomes = %v, want exactly 1 %q and %d %q", counts, Marked, markers-1, AlreadyMarked)
	}
}

// Cancels racing a poll storm on the same job. Each canceller runs the loop a
// real caller runs — read the position, try to mark there, retry when a poll
// moved it — so exactly one wins once the polls stop, every loser saw
// already-marked or position-moved, and the recorded marker sits at a position
// no later than the final poll count.
func TestMarkCancelRacingAdvance(t *testing.T) {
	t.Parallel()

	const (
		polls   = 200
		markers = 20
	)
	r := NewRegistry(Limits{})
	if _, err := r.Create(job("t-1", "run_a")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range polls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r.Advance("t-1", "run_a", i)
		}()
	}

	var mu sync.Mutex
	counts := map[MarkOutcome]int{}
	for range markers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				current, ok := r.Lookup("t-1", "run_a")
				if !ok {
					t.Error("the job vanished mid-race")
					return
				}
				_, outcome := r.MarkCancel("t-1", "run_a", current.Polls)
				mu.Lock()
				counts[outcome]++
				mu.Unlock()
				if outcome != PositionMoved {
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	if counts[Marked] != 1 {
		t.Errorf("winners = %d, want exactly 1 (outcomes %v)", counts[Marked], counts)
	}
	if counts[NotFound] != 0 {
		t.Errorf("not-found outcomes = %d with no reset in flight", counts[NotFound])
	}
	if counts[AlreadyMarked] != markers-1 {
		t.Errorf("already-marked = %d, want %d: every loser ends on the marker (outcomes %v)",
			counts[AlreadyMarked], markers-1, counts)
	}

	got, _ := r.Lookup("t-1", "run_a")
	if !got.CancelRequested || got.Polls != polls || got.CancelAtPoll > got.Polls {
		t.Errorf("record = {Polls:%d CancelRequested:%v CancelAtPoll:%d}, want Polls %d and a marker at or before it",
			got.Polls, got.CancelRequested, got.CancelAtPoll, polls)
	}
}

// The operations must be race-free against creates and resets in other
// namespaces and in their own. -race is the assertion.
func TestLifecycleRaceSafeAgainstReset(t *testing.T) {
	t.Parallel()

	r := NewRegistry(Limits{MaxJobs: 8})

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := range 300 {
			_, _ = r.Create(job("t-1", "run_"+strconv.Itoa(i%8)))
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 300 {
			r.Advance("t-1", "run_"+strconv.Itoa(i%8), i)
			r.MarkCancel("t-1", "run_"+strconv.Itoa(i%8), i)
		}
	}()
	go func() {
		defer wg.Done()
		for range 300 {
			r.ResetIn("t-1")
		}
	}()
	wg.Wait()
}
