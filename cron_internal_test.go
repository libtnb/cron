package cron

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type blockingNextSchedule struct {
	first   time.Time
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (s *blockingNextSchedule) Next(time.Time) time.Time {
	if s.calls.Add(1) == 1 {
		return s.first
	}
	select {
	case <-s.entered:
	default:
		close(s.entered)
	}
	<-s.release
	return time.Time{}
}

func TestCron_FireDueComputesNextOutsideMu(t *testing.T) {
	now := time.Now()
	s := &blockingNextSchedule{
		first:   now.Add(-time.Second),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	c := MustNew(WithLocation(time.UTC), WithMissedTolerance(time.Hour))
	id, err := c.AddSchedule(s, JobFunc(func(context.Context) error {
		t.Fatal("job should not run after Remove")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		c.fireDue(context.Background(), now)
		close(done)
	}()
	<-s.entered

	removed := make(chan error, 1)
	go func() { removed <- c.Remove(id) }()
	select {
	case err := <-removed:
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(s.release)
		<-done
		t.Fatal("Remove blocked while fireDue was computing Schedule.Next")
	}

	close(s.release)
	<-done
}

type blockingFirstSchedule struct {
	enteredOnce atomic.Bool
	entered     chan struct{}
	release     chan struct{}
}

func (s *blockingFirstSchedule) Next(time.Time) time.Time {
	if s.enteredOnce.CompareAndSwap(false, true) {
		close(s.entered)
	}
	<-s.release
	return time.Time{}
}

func TestFindMostRecentMissed_LastFireZero(t *testing.T) {
	now := time.Now()
	got := findMostRecentMissed(ConstantDelay(time.Minute), time.Time{}, now)
	if !got.IsZero() {
		t.Fatalf("got %v, want zero", got)
	}
}

func TestFindMostRecentMissed_LastFireAfterNow(t *testing.T) {
	now := time.Now()
	got := findMostRecentMissed(ConstantDelay(time.Minute), now.Add(time.Hour), now)
	if !got.IsZero() {
		t.Fatalf("got %v, want zero", got)
	}
}

type neverEndingSchedule struct{ step time.Duration }

func (s neverEndingSchedule) Next(now time.Time) time.Time {
	return now.Add(s.step)
}

func TestFindMostRecentMissed_LongBacklogIsRecent(t *testing.T) {
	now := time.Now()
	got := findMostRecentMissed(neverEndingSchedule{step: time.Nanosecond}, now.Add(-time.Hour), now)
	if got.IsZero() || now.Sub(got) > time.Millisecond {
		t.Fatalf("got %v, want the firing right before now", got)
	}

	// 108000 missed one-second firings: the result must still be the latest one.
	lastFire := now.Add(-30 * time.Hour)
	got = findMostRecentMissed(ConstantDelay(time.Second), lastFire, now)
	if got.IsZero() || now.Sub(got) > time.Second || !got.After(lastFire) {
		t.Fatalf("got %v (%v before now), want within one second of now", got, now.Sub(got))
	}
}

func TestFindMostRecentMissed_OnlyLastFireMissed(t *testing.T) {
	now := time.Now()
	lastFire := now.Add(-10 * time.Second)
	got := findMostRecentMissed(ConstantDelay(time.Minute), lastFire, now)
	if !got.Equal(lastFire) {
		t.Fatalf("got %v, want lastFire %v", got, lastFire)
	}
}

func TestFireKey_IndependentOfZone(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	shanghai := time.FixedZone("CST", 8*3600)
	if a, b := fireKey("k", at), fireKey("k", at.In(shanghai)); a != b {
		t.Fatalf("fire keys differ by zone: %q vs %q", a, b)
	}
}

type zeroNextSchedule struct{ calls atomic.Int32 }

func (s *zeroNextSchedule) Next(now time.Time) time.Time {
	if s.calls.Add(1) == 1 {
		return now.Add(time.Second)
	}
	return time.Time{}
}

func TestFindMostRecentMissed_ZeroNextStopsLoop(t *testing.T) {
	now := time.Now()
	_ = findMostRecentMissed(&zeroNextSchedule{}, now.Add(-time.Hour), now)
}

func TestAdvancePrev_NoOpWhenEntryRemoved(t *testing.T) {
	c := MustNew(WithLocation(time.UTC))
	id, _ := c.AddSchedule(ConstantDelay(time.Minute), JobFunc(func(context.Context) error { return nil }))
	if err := c.Remove(id); err != nil {
		t.Fatal(err)
	}
	// Should not panic and should not republish a view for a removed entry.
	c.advancePrev(id, time.Now())
	if _, ok := c.Entry(id); ok {
		t.Fatal("entry should remain removed")
	}
}

func TestAdvancePrev_NoOpWhenFireAtNotAfterPrev(t *testing.T) {
	c := MustNew(WithLocation(time.UTC))
	id, _ := c.AddSchedule(ConstantDelay(time.Minute), JobFunc(func(context.Context) error { return nil }))
	now := time.Now()
	c.advancePrev(id, now)
	first, _ := c.Entry(id)
	if !first.Prev.Equal(now) {
		t.Fatalf("first advancePrev did not set Prev: %v", first.Prev)
	}
	c.advancePrev(id, now.Add(-time.Hour))
	second, _ := c.Entry(id)
	if !second.Prev.Equal(now) {
		t.Fatalf("Prev regressed: first=%v second=%v", first.Prev, second.Prev)
	}
}

func TestStop_CtxCancelDuringLoopShutdown(t *testing.T) {
	now := time.Now()
	s := &blockingNextSchedule{
		first:   now.Add(-time.Second),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	c := MustNew(WithLocation(time.UTC), WithMissedTolerance(time.Hour))
	_, err := c.AddSchedule(s, JobFunc(func(context.Context) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	<-s.entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stopErr := c.Stop(ctx)
	close(s.release)
	if stopErr == nil {
		t.Fatal("expected ctx error from Stop while loop is blocked")
	}
}

func TestCron_AddScheduleComputesNextOutsideMu(t *testing.T) {
	c := MustNew(WithLocation(time.UTC))
	id, err := c.AddSchedule(ConstantDelay(time.Hour), JobFunc(func(context.Context) error {
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	s := &blockingFirstSchedule{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	addDone := make(chan struct{})
	go func() {
		_, _ = c.AddSchedule(s, JobFunc(func(context.Context) error { return nil }))
		close(addDone)
	}()
	<-s.entered

	removed := make(chan error, 1)
	go func() { removed <- c.Remove(id) }()
	select {
	case err := <-removed:
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(s.release)
		<-addDone
		t.Fatal("Remove blocked while AddSchedule was computing Schedule.Next")
	}

	close(s.release)
	<-addDone
}

func TestSpecSchedule_ZeroValueReturnsZero(t *testing.T) {
	var s SpecSchedule
	if got := s.Next(time.Now()); !got.IsZero() {
		t.Fatalf("zero-value Next = %v, want zero", got)
	}
}

func TestFindAllMissed(t *testing.T) {
	now := time.Now()

	if got := findAllMissed(ConstantDelay(time.Minute), time.Time{}, now); got != nil {
		t.Fatalf("zero lastFire: got %v", got)
	}
	if got := findAllMissed(ConstantDelay(time.Minute), now.Add(time.Hour), now); got != nil {
		t.Fatalf("future lastFire: got %v", got)
	}

	got := findAllMissed(ConstantDelay(time.Minute), now.Add(-150*time.Second), now)
	if len(got) < 2 || len(got) > 4 {
		t.Fatalf("~2.5min backlog at 1min cadence: got %d instants", len(got))
	}
	for i := 1; i < len(got); i++ {
		if !got[i].After(got[i-1]) {
			t.Fatalf("instants not increasing: %v", got)
		}
	}
}

func TestFindAllMissed_CapKeepsNewest(t *testing.T) {
	now := time.Now()
	for _, backlog := range []time.Duration{time.Duration(missedRunAllCap+500) * time.Second, 30 * time.Hour} {
		lastFire := now.Add(-backlog)
		got := findAllMissed(ConstantDelay(time.Second), lastFire, now)
		if len(got) != missedRunAllCap {
			t.Fatalf("backlog %v: len = %d, want cap %d", backlog, len(got), missedRunAllCap)
		}
		newest := got[len(got)-1]
		if now.Sub(newest) > time.Second {
			t.Fatalf("backlog %v: newest kept is %v before now, want the latest firing", backlog, now.Sub(newest))
		}
		if oldest := got[0]; now.Sub(oldest) > time.Duration(missedRunAllCap+1)*time.Second {
			t.Fatalf("backlog %v: oldest kept is %v before now, want about %d seconds", backlog, now.Sub(oldest), missedRunAllCap)
		}
		for i := 1; i < len(got); i++ {
			if !got[i].After(got[i-1]) {
				t.Fatalf("backlog %v: instants not increasing at %d", backlog, i)
			}
		}
	}
}

func TestFindAllMissed_ExactlyCapPlusLastFire(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// lastFire plus exactly missedRunAllCap later firings: the oldest (lastFire) is dropped.
	lastFire := now.Add(-time.Duration(missedRunAllCap) * time.Second)
	got := findAllMissed(ConstantDelay(time.Second), lastFire, now)
	if len(got) != missedRunAllCap || got[0].Equal(lastFire) || !got[len(got)-1].Equal(now) {
		t.Fatalf("len=%d first=%v last=%v", len(got), got[0], got[len(got)-1])
	}
}

func TestMakeFirePlan_Policies(t *testing.T) {
	c := MustNew(WithLocation(time.UTC), WithMissedTolerance(time.Second))
	now := time.Now()
	sched := ConstantDelay(time.Minute)

	onTime := c.makeFirePlan(dueFire{
		e: &entry{missed: MissedSkip}, schedule: sched, scheduled: now.Add(-10 * time.Millisecond),
	}, now)
	if onTime.missed || onTime.fireOne.IsZero() || onTime.fireAll != nil {
		t.Fatalf("on-time plan = %+v", onTime)
	}

	skip := c.makeFirePlan(dueFire{
		e: &entry{missed: MissedSkip}, schedule: sched, scheduled: now.Add(-time.Hour),
	}, now)
	if !skip.missed || !skip.fireOne.IsZero() || skip.fireAll != nil {
		t.Fatalf("skip plan = %+v", skip)
	}

	runOnce := c.makeFirePlan(dueFire{
		e: &entry{missed: MissedRunOnce}, schedule: sched, scheduled: now.Add(-time.Hour),
	}, now)
	if !runOnce.missed || runOnce.fireOne.IsZero() {
		t.Fatalf("run-once plan = %+v", runOnce)
	}

	runAll := c.makeFirePlan(dueFire{
		e: &entry{missed: MissedRunAll}, schedule: sched, scheduled: now.Add(-3 * time.Minute),
	}, now)
	if !runAll.missed || len(runAll.fireAll) < 2 {
		t.Fatalf("run-all plan = %+v", runAll)
	}
}

func TestCommitAndDispatch_StaleGenDiscarded(t *testing.T) {
	c := MustNew(WithLocation(time.UTC))
	var runs atomic.Int32
	id, _ := c.AddSchedule(ConstantDelay(time.Hour), JobFunc(func(context.Context) error {
		runs.Add(1)
		return nil
	}))
	c.mu.Lock()
	e := c.byEntry[id]
	plan := firePlan{
		e: e, schedule: e.schedule, gen: e.gen + 1, // stale
		scheduled: time.Now(), fireOne: time.Now(),
	}
	c.mu.Unlock()

	c.commitAndDispatch(context.Background(), plan)
	if runs.Load() != 0 {
		t.Fatal("stale-gen plan must not dispatch")
	}
}

// gateSchedule blocks its second Next call until released, so a test can
// interleave another mutation while Resume computes the next fire.
type gateSchedule struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (g *gateSchedule) Next(now time.Time) time.Time {
	if g.calls.Add(1) == 2 {
		g.entered <- struct{}{}
		<-g.release
	}
	return now.Add(time.Hour)
}

func TestResume_LosesRaceToConcurrentUpdate(t *testing.T) {
	c := MustNew(WithLocation(time.UTC))
	g := &gateSchedule{entered: make(chan struct{}), release: make(chan struct{})}
	id, _ := c.AddSchedule(g, JobFunc(func(context.Context) error { return nil })) // Next call #1
	if err := c.Pause(id); err != nil {
		t.Fatal(err)
	}

	resumed := make(chan error)
	go func() { resumed <- c.Resume(id) }() // blocks in Next call #2

	<-g.entered
	if err := c.UpdateSchedule(id, ConstantDelay(time.Hour)); err != nil { // bumps gen
		t.Fatal(err)
	}
	close(g.release)

	if err := <-resumed; err != nil {
		t.Fatalf("Resume should succeed when losing the race: %v", err)
	}
	e, _ := c.Entry(id)
	if !e.Paused {
		t.Fatal("the racing Update's paused state must stand")
	}
}

func TestCompareNext(t *testing.T) {
	z := time.Time{}
	early := time.Unix(100, 0)
	late := time.Unix(200, 0)
	cases := []struct {
		name string
		a, b time.Time
		want int // sign
	}{
		{"both zero", z, z, 0},
		{"a zero sorts after", z, early, 1},
		{"b zero sorts before", early, z, -1},
		{"earlier first", early, late, -1},
		{"later after", late, early, 1},
		{"equal non-zero", early, early, 0},
	}
	for _, c := range cases {
		got := compareNext(c.a, c.b)
		if (got < 0) != (c.want < 0) || (got > 0) != (c.want > 0) {
			t.Errorf("%s: compareNext = %d, want sign of %d", c.name, got, c.want)
		}
	}
}

func TestResume_EntryRemovedDuringNext(t *testing.T) {
	c := MustNew(WithLocation(time.UTC))
	g := &gateSchedule{entered: make(chan struct{}), release: make(chan struct{})}
	id, _ := c.AddSchedule(g, JobFunc(func(context.Context) error { return nil })) // Next call #1
	if err := c.Pause(id); err != nil {
		t.Fatal(err)
	}

	resumed := make(chan error)
	go func() { resumed <- c.Resume(id) }() // blocks in Next call #2

	<-g.entered
	if err := c.Remove(id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	close(g.release)

	if err := <-resumed; !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("Resume after the entry vanished = %v, want ErrEntryNotFound", err)
	}
}
