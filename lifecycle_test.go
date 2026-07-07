package cron_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libtnb/cron"
)

func TestPauseResume(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	id, _ := c.Add("@every 1m", cron.JobFunc(noop))

	if c.Pause(cron.EntryID(999)) {
		t.Fatal("Pause(unknown) should be false")
	}
	if !c.Pause(id) {
		t.Fatal("Pause should be true")
	}
	if !c.Pause(id) {
		t.Fatal("Pause should be idempotent")
	}
	e, _ := c.Entry(id)
	if !e.Paused || !e.Next.IsZero() {
		t.Fatalf("paused view = %+v", e)
	}

	if c.Resume(cron.EntryID(999)) {
		t.Fatal("Resume(unknown) should be false")
	}
	if !c.Resume(id) {
		t.Fatal("Resume should be true")
	}
	if !c.Resume(id) {
		t.Fatal("Resume should be idempotent")
	}
	e, _ = c.Entry(id)
	if e.Paused || e.Next.IsZero() {
		t.Fatalf("resumed view = %+v", e)
	}
}

func TestTrigger_PausedEntryStillFires(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	ran := make(chan struct{}, 1)
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(func(context.Context) error {
		ran <- struct{}{}
		return nil
	}))
	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()

	c.Pause(id)
	if err := c.Trigger(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("paused entry should still fire on manual Trigger")
	}
}

func TestUpdate(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	id, _ := c.Add("0 0 1 1 *", cron.JobFunc(noop))

	if err := c.Update(cron.EntryID(999), "* * * * *"); !errors.Is(err, cron.ErrEntryNotFound) {
		t.Fatalf("err = %v, want ErrEntryNotFound", err)
	}
	if err := c.Update(id, "not a spec"); err == nil {
		t.Fatal("want parse error")
	}

	before, _ := c.Entry(id)
	if err := c.Update(id, "* * * * *"); err != nil {
		t.Fatal(err)
	}
	after, _ := c.Entry(id)
	if after.Spec != "* * * * *" {
		t.Fatalf("Spec = %q", after.Spec)
	}
	if !after.Next.Before(before.Next) {
		t.Fatalf("next not recomputed: before=%v after=%v", before.Next, after.Next)
	}
}

func TestUpdateSchedule(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	id, _ := c.Add("* * * * *", cron.JobFunc(noop))

	if err := c.UpdateSchedule(id, nil); !errors.Is(err, cron.ErrNilSchedule) {
		t.Fatalf("err = %v, want ErrNilSchedule", err)
	}
	// Swapping to a never-firing schedule clears Next and Spec.
	if err := c.UpdateSchedule(id, cron.TriggeredSchedule()); err != nil {
		t.Fatal(err)
	}
	e, _ := c.Entry(id)
	if e.Spec != "" || !e.Next.IsZero() {
		t.Fatalf("view after swap = %+v", e)
	}
}

func TestUpdate_PausedStaysPausedUntilResume(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	id, _ := c.Add("0 0 1 1 *", cron.JobFunc(noop))
	c.Pause(id)

	if err := c.Update(id, "* * * * *"); err != nil {
		t.Fatal(err)
	}
	e, _ := c.Entry(id)
	if !e.Paused || !e.Next.IsZero() {
		t.Fatalf("update must keep pause: %+v", e)
	}
	c.Resume(id)
	e, _ = c.Entry(id)
	if e.Paused || e.Next.IsZero() {
		t.Fatalf("resume after update: %+v", e)
	}
	if time.Until(e.Next) > 2*time.Minute {
		t.Fatalf("next should follow the updated every-minute spec, got %v", e.Next)
	}
}

func TestDrain_LetsJobsFinishUncancelled(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	release := make(chan struct{})
	var sawCancel, finished atomic.Bool
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(func(ctx context.Context) error {
		<-release
		sawCancel.Store(ctx.Err() != nil)
		finished.Store(true)
		return nil
	}))
	_ = c.Start()
	if err := c.Trigger(id); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(release)
	}()
	if err := c.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !finished.Load() {
		t.Fatal("job did not finish during Drain")
	}
	if sawCancel.Load() {
		t.Fatal("Drain must not cancel in-flight jobs")
	}
	if c.Running() {
		t.Fatal("scheduler still running after Drain")
	}
	if err := c.Start(); !errors.Is(err, cron.ErrSchedulerStopped) {
		t.Fatalf("Start after Drain = %v, want ErrSchedulerStopped", err)
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after Drain = %v", err)
	}
}

func TestDrain_TimeoutThenStopCancels(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(func(ctx context.Context) error {
		<-ctx.Done()
		return context.Cause(ctx)
	}))
	_ = c.Start()
	if err := c.Trigger(id); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain = %v, want deadline exceeded", err)
	}
	// Stop still force-cancels the stuck job.
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDrain_BeforeStart(t *testing.T) {
	c := cron.New()
	if err := c.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); !errors.Is(err, cron.ErrSchedulerStopped) {
		t.Fatalf("Start after Drain = %v, want ErrSchedulerStopped", err)
	}
}

func TestTriggerAndWait(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	boom := errors.New("boom")
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(func(context.Context) error {
		return boom
	}))

	if err := c.TriggerAndWait(context.Background(), id); !errors.Is(err, cron.ErrSchedulerNotRunning) {
		t.Fatalf("err = %v, want ErrSchedulerNotRunning", err)
	}
	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()

	if err := c.TriggerAndWait(context.Background(), id); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want job error", err)
	}
}

func TestTriggerAndWait_CtxBoundsWait(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	release := make(chan struct{})
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(func(context.Context) error {
		<-release
		return nil
	}))
	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.TriggerAndWait(ctx, id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	close(release)
}

func TestWithLastRun_CatchUpRunOnce(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC), cron.WithMissedFire(cron.MissedRunOnce))
	ran := make(chan struct{}, 4)
	last := time.Now().Add(-90 * time.Minute)
	id, err := c.Add("0 * * * *", cron.JobFunc(func(context.Context) error {
		ran <- struct{}{}
		return nil
	}), cron.WithLastRun(last))
	if err != nil {
		t.Fatal(err)
	}
	e, _ := c.Entry(id)
	if !e.Prev.Equal(last) {
		t.Fatalf("Prev = %v, want seeded %v", e.Prev, last)
	}
	if !e.Next.Before(time.Now()) {
		t.Fatalf("Next = %v, want a catch-up instant in the past", e.Next)
	}

	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()
	select {
	case <-ran:
	case <-time.After(3 * time.Second):
		t.Fatal("catch-up fire never happened")
	}
}

func TestWithLastRun_CatchUpRunAllPerEntry(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC)) // global default stays MissedSkip
	var runs atomic.Int64
	last := time.Now().Add(-3*time.Hour - time.Minute)
	_, err := c.Add("0 * * * *", cron.JobFunc(func(context.Context) error {
		runs.Add(1)
		return nil
	}), cron.WithLastRun(last), cron.WithEntryMissedFire(cron.MissedRunAll))
	if err != nil {
		t.Fatal(err)
	}

	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && runs.Load() < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runs.Load(); got < 3 {
		t.Fatalf("MissedRunAll caught up %d fires, want >= 3", got)
	}
}

func TestWithEntryJitter_OverridesGlobal(t *testing.T) {
	// Global jitter is huge; the per-entry zero must win or the fire cannot
	// happen within the test window.
	c := cron.New(cron.WithLocation(time.UTC), cron.WithJitter(time.Hour))
	ran := make(chan struct{}, 1)
	_, _ = c.AddSchedule(cron.ConstantDelay(time.Second), cron.JobFunc(func(context.Context) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}), cron.WithEntryJitter(0))
	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()

	select {
	case <-ran:
	case <-time.After(3 * time.Second):
		t.Fatal("per-entry zero jitter should fire promptly despite global 1h jitter")
	}
}

func TestStopDuringJitterEmitsMissed(t *testing.T) {
	h := &missedCounterHook{}
	c := cron.New(cron.WithLocation(time.UTC), cron.WithJitter(time.Hour), cron.WithHooks(h))
	_, _ = c.AddSchedule(cron.ConstantDelay(time.Second), cron.JobFunc(noop))
	_ = c.Start()
	time.Sleep(1200 * time.Millisecond) // the first fire is now inside its jitter wait
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.missed.Load() == 0 {
		t.Fatal("a fire abandoned during jitter must emit EventMissed")
	}
}

type missedCounterHook struct{ missed atomic.Int64 }

func (h *missedCounterHook) OnMissedFire(cron.EventMissed) { h.missed.Add(1) }

func TestWithBaseContext(t *testing.T) {
	type ctxKey struct{}
	base, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "v"))
	defer cancel()
	c := cron.New(cron.WithLocation(time.UTC), cron.WithBaseContext(base))
	got := make(chan string, 1)
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(func(ctx context.Context) error {
		v, _ := ctx.Value(ctxKey{}).(string)
		got <- v
		return nil
	}))
	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()

	if err := c.Trigger(id); err != nil {
		t.Fatal(err)
	}
	if v := <-got; v != "v" {
		t.Fatalf("job ctx value = %q, want %q", v, "v")
	}
}

func TestJobPanicRecoveredByDefault(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(func(context.Context) error {
		panic("kaboom")
	}))
	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()

	err := c.TriggerAndWait(context.Background(), id)
	if !errors.Is(err, cron.ErrJobPanic) {
		t.Fatalf("err = %v, want ErrJobPanic", err)
	}
}

func TestWithoutRecover_NormalJobsUnaffected(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC), cron.WithoutRecover())
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(noop))
	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()

	if err := c.TriggerAndWait(context.Background(), id); err != nil {
		t.Fatalf("err = %v", err)
	}
}
