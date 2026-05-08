package cron

import (
	"context"
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

func TestFindMostRecentMissed_LastFireZero(t *testing.T) {
	c := New()
	now := time.Now()
	got := c.findMostRecentMissed(ConstantDelay(time.Minute), time.Time{}, now)
	if !got.IsZero() {
		t.Fatalf("got %v, want zero", got)
	}
}

func TestFindMostRecentMissed_LastFireAfterNow(t *testing.T) {
	c := New()
	now := time.Now()
	got := c.findMostRecentMissed(ConstantDelay(time.Minute), now.Add(time.Hour), now)
	if !got.IsZero() {
		t.Fatalf("got %v, want zero", got)
	}
}

type neverEndingSchedule struct{ step time.Duration }

func (s neverEndingSchedule) Next(now time.Time) time.Time {
	return now.Add(s.step)
}

func TestFindMostRecentMissed_HitsCap(t *testing.T) {
	c := New()
	now := time.Now()
	got := c.findMostRecentMissed(neverEndingSchedule{step: time.Nanosecond}, now.Add(-time.Hour), now)
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
	_ = c.findMostRecentMissed(&zeroNextSchedule{}, now.Add(-time.Hour), now)
}

func TestAdvancePrev_NoOpWhenEntryRemoved(t *testing.T) {
	c := New(WithLocation(time.UTC))
	id, _ := c.AddSchedule(ConstantDelay(time.Minute), JobFunc(func(context.Context) error { return nil }))
	c.Remove(id)
	// Should not panic and should not republish a view for a removed entry.
	c.advancePrev(id, time.Now())
	if _, ok := c.Entry(id); ok {
		t.Fatal("entry should remain removed")
	}
}

func TestAdvancePrev_NoOpWhenFireAtNotAfterPrev(t *testing.T) {
	c := New(WithLocation(time.UTC))
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
	c := New(WithLocation(time.UTC), WithMissedTolerance(time.Hour))
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
