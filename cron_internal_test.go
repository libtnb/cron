package cron

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type commitRecorder struct {
	scheduled atomic.Int64
	missed    atomic.Int64
}

func (r *commitRecorder) JobScheduled(string)                       { r.scheduled.Add(1) }
func (r *commitRecorder) JobStarted(string)                         {}
func (r *commitRecorder) JobCompleted(string, time.Duration, error) {}
func (r *commitRecorder) JobMissed(string, time.Duration)           { r.missed.Add(1) }
func (r *commitRecorder) QueueDepth(int)                            {}
func (r *commitRecorder) HookDropped()                              {}

func TestCron_CommitCanceledByStopDoesNotRecordMissedOrAdvance(t *testing.T) {
	rec := &commitRecorder{}
	c := New(WithLocation(time.UTC), WithRecorder(rec))
	id, err := c.AddSchedule(ConstantDelay(time.Minute), JobFunc(func(context.Context) error {
		t.Fatal("job should not run after stop cancellation")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	before, ok := c.Entry(id)
	if !ok {
		t.Fatal("entry missing")
	}

	c.mu.Lock()
	e := c.byEntry[id]
	c.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.commitAndDispatch(ctx, firePlan{
		e:         e,
		scheduled: before.Next,
		fireOne:   before.Next,
		nextFire:  before.Next.Add(time.Minute),
	})

	after, ok := c.Entry(id)
	if !ok {
		t.Fatal("entry missing after canceled commit")
	}
	if !after.Next.Equal(before.Next) {
		t.Fatalf("Next advanced after stop-canceled commit: before=%v after=%v", before.Next, after.Next)
	}
	if got := rec.missed.Load(); got != 0 {
		t.Fatalf("JobMissed = %d, want 0", got)
	}
	if got := rec.scheduled.Load(); got != 1 {
		t.Fatalf("JobScheduled = %d, want only initial Add", got)
	}
}

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
	c := New(WithLocation(time.UTC), WithMissedTolerance(time.Hour))
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

	removed := make(chan bool, 1)
	go func() { removed <- c.Remove(id) }()
	select {
	case ok := <-removed:
		if !ok {
			t.Fatal("Remove returned false")
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

func TestRePushDue_RestoresPoppedItems(t *testing.T) {
	c := New(WithLocation(time.UTC))
	idA, _ := c.AddSchedule(ConstantDelay(time.Minute), JobFunc(func(context.Context) error { return nil }))
	idB, _ := c.AddSchedule(ConstantDelay(time.Minute), JobFunc(func(context.Context) error { return nil }))

	c.mu.Lock()
	eA := c.byEntry[idA]
	eB := c.byEntry[idB]
	c.h.Remove(eA.item)
	c.h.Remove(eB.item)
	eA.item = nil
	eB.item = nil
	due := []dueFire{{e: eA, scheduled: eA.next}, {e: eB, scheduled: eB.next}}
	heapLenBefore := c.h.Len()
	c.mu.Unlock()
	if heapLenBefore != 0 {
		t.Fatalf("heap not empty before re-push: %d", heapLenBefore)
	}

	c.rePushDue(due)

	c.mu.Lock()
	heapLenAfter := c.h.Len()
	hasA := eA.item != nil
	hasB := eB.item != nil
	c.mu.Unlock()
	if heapLenAfter != 2 || !hasA || !hasB {
		t.Fatalf("re-push failed: len=%d aHas=%v bHas=%v", heapLenAfter, hasA, hasB)
	}
}

func TestRePushDue_SkipsRemovedEntries(t *testing.T) {
	c := New(WithLocation(time.UTC))
	id, _ := c.AddSchedule(ConstantDelay(time.Minute), JobFunc(func(context.Context) error { return nil }))

	c.mu.Lock()
	e := c.byEntry[id]
	c.h.Remove(e.item)
	e.item = nil
	delete(c.byEntry, id)
	c.publishViewRemoveLocked(id)
	due := []dueFire{{e: e, scheduled: e.next}}
	c.mu.Unlock()

	c.rePushDue(due)

	c.mu.Lock()
	heapLen := c.h.Len()
	c.mu.Unlock()
	if heapLen != 0 {
		t.Fatalf("rePushDue should skip removed entries; heap=%d", heapLen)
	}
}

type canceledOnNextSchedule struct {
	first     time.Time
	cancel    context.CancelFunc
	cancelled atomic.Bool
}

func (s *canceledOnNextSchedule) Next(time.Time) time.Time {
	if s.cancelled.CompareAndSwap(false, true) && s.cancel != nil {
		s.cancel()
	}
	return s.first
}

func TestFireDue_CtxCancelMidLoopInvokesRePush(t *testing.T) {
	c := New(WithLocation(time.UTC), WithMissedTolerance(time.Hour))
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())

	s := &canceledOnNextSchedule{first: now.Add(time.Hour), cancel: cancel}
	idA, _ := c.AddSchedule(s, JobFunc(func(context.Context) error { return nil }))
	idB, _ := c.AddSchedule(ConstantDelay(time.Minute), JobFunc(func(context.Context) error { return nil }))

	c.mu.Lock()
	for _, id := range []EntryID{idA, idB} {
		e := c.byEntry[id]
		if e.item != nil {
			c.h.Remove(e.item)
		}
		e.next = now.Add(-time.Second)
		e.item = c.h.Push(e.next.UnixNano(), e)
	}
	c.mu.Unlock()

	c.fireDue(ctx, now)

	c.mu.Lock()
	heapLen := c.h.Len()
	c.mu.Unlock()
	if heapLen == 0 {
		t.Fatal("rePushDue didn't restore the un-committed entry")
	}
}

func TestFindMostRecentMissed_LastFireZero(t *testing.T) {
	c := New()
	now := time.Now()
	got, ok := c.findMostRecentMissed(context.Background(), ConstantDelay(time.Minute), time.Time{}, now)
	if !ok {
		t.Fatal("ok=false for zero lastFire")
	}
	if !got.IsZero() {
		t.Fatalf("got %v, want zero", got)
	}
}

func TestFindMostRecentMissed_LastFireAfterNow(t *testing.T) {
	c := New()
	now := time.Now()
	got, ok := c.findMostRecentMissed(context.Background(), ConstantDelay(time.Minute), now.Add(time.Hour), now)
	if !ok || !got.IsZero() {
		t.Fatalf("got=%v ok=%v, want zero/true", got, ok)
	}
}

func TestFindMostRecentMissed_CtxCancelMidScan(t *testing.T) {
	c := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	now := time.Now()
	_, ok := c.findMostRecentMissed(ctx, ConstantDelay(time.Second), now.Add(-time.Hour), now)
	if ok {
		t.Fatal("ok=true with canceled ctx, want false")
	}
}

type neverEndingSchedule struct{ step time.Duration }

func (s neverEndingSchedule) Next(now time.Time) time.Time {
	return now.Add(s.step)
}

func TestFindMostRecentMissed_HitsCap(t *testing.T) {
	c := New()
	now := time.Now()
	got, ok := c.findMostRecentMissed(context.Background(),
		neverEndingSchedule{step: time.Nanosecond},
		now.Add(-time.Hour), now)
	if !ok {
		t.Fatal("ok=false")
	}
	if got.IsZero() {
		t.Fatal("got zero, expected last set during iteration")
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
	c := New()
	now := time.Now()
	got, ok := c.findMostRecentMissed(context.Background(),
		&zeroNextSchedule{},
		now.Add(-time.Hour), now)
	if !ok {
		t.Fatal("ok=false")
	}
	_ = got
}

func TestCron_AddScheduleComputesNextOutsideMu(t *testing.T) {
	c := New(WithLocation(time.UTC))
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

	removed := make(chan bool, 1)
	go func() { removed <- c.Remove(id) }()
	select {
	case ok := <-removed:
		if !ok {
			t.Fatal("Remove returned false")
		}
	case <-time.After(200 * time.Millisecond):
		close(s.release)
		<-addDone
		t.Fatal("Remove blocked while AddSchedule was computing Schedule.Next")
	}

	close(s.release)
	<-addDone
}
