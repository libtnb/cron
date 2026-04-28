package cron_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libtnb/cron"
)

func TestCron_NewIsIdle(t *testing.T) {
	c := cron.New()
	if e, ok := c.Entry(1); ok {
		t.Fatalf("zero scheduler should have no entries, got %+v", e)
	}
	if got := slices.Collect(c.Entries()); len(got) != 0 {
		t.Fatalf("Entries len = %d", len(got))
	}
}

func TestCron_AddAndEntry(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	id, err := c.Add("@every 1m", cron.JobFunc(noop), cron.WithName("ping"))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := c.Entry(id)
	if !ok {
		t.Fatal("Entry missing")
	}
	if got.ID != id || got.Name != "ping" || got.Spec != "@every 1m" {
		t.Fatalf("Entry = %+v", got)
	}
	if got.Next.IsZero() {
		t.Fatal("Next should be populated")
	}
}

func TestCron_AddInvalidSpec_ReturnsParseError(t *testing.T) {
	c := cron.New()
	_, err := c.Add("@nope", cron.JobFunc(noop))
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *cron.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("got %T", err)
	}
}

func TestCron_Remove(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	keep, _ := c.Add("@every 2m", cron.JobFunc(noop))
	id, _ := c.Add("@every 1m", cron.JobFunc(noop))
	if !c.Remove(id) {
		t.Fatal("Remove returned false")
	}
	if c.Remove(id) {
		t.Fatal("second Remove should return false")
	}
	if _, ok := c.Entry(id); ok {
		t.Fatal("Entry still present after Remove")
	}
	if _, ok := c.Entry(keep); !ok {
		t.Fatal("untouched entry should remain")
	}
}

func TestCron_MaxEntries(t *testing.T) {
	c := cron.New(cron.WithMaxEntries(2), cron.WithLocation(time.UTC))
	if _, err := c.Add("@every 1m", cron.JobFunc(noop)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add("@every 2m", cron.JobFunc(noop)); err != nil {
		t.Fatal(err)
	}
	id, err := c.Add("@every 3m", cron.JobFunc(noop))
	if !errors.Is(err, cron.ErrCapacityReached) {
		t.Fatalf("3rd Add err = %v, want ErrCapacityReached", err)
	}
	if id != 0 {
		t.Fatalf("3rd Add id = %d", id)
	}
}

func TestCron_FiresOnSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		var calls atomic.Int64
		_, err := c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			calls.Add(1)
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}

		_ = c.Start()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())

		got := calls.Load()
		if got < 9 || got > 11 {
			t.Fatalf("calls = %d, want ~10", got)
		}
	})
}

func TestCron_RemovedEntryStopsFiring(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		var calls atomic.Int64
		id, _ := c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			calls.Add(1)
			return nil
		}))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		c.Remove(id)

		before := calls.Load()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		after := calls.Load()

		if after != before {
			t.Fatalf("entry continued firing after Remove: before=%d after=%d", before, after)
		}
		_ = c.Stop(context.Background())
	})
}

func TestCron_AddAtRuntime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		_ = c.Start()

		var calls atomic.Int64
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			calls.Add(1)
			return nil
		}))
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if got := calls.Load(); got < 2 || got > 4 {
			t.Fatalf("calls = %d, want ~3", got)
		}
		_ = c.Stop(context.Background())
	})
}

func TestCron_StopWaitsForInflightJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))

		started := make(chan struct{})
		var release atomic.Bool
		var done atomic.Bool

		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			close(started)
			for !release.Load() {
				time.Sleep(100 * time.Millisecond)
			}
			done.Store(true)
			return nil
		}))
		_ = c.Start()

		<-started

		stopErr := make(chan error, 1)
		go func() {
			stopErr <- c.Stop(context.Background())
		}()
		time.Sleep(2 * time.Second)
		if done.Load() {
			t.Fatal("job should not have completed yet")
		}
		release.Store(true)
		synctest.Wait()
		if err := <-stopErr; err != nil {
			t.Fatalf("Stop err: %v", err)
		}
		if !done.Load() {
			t.Fatal("job should have completed before Stop returned")
		}
	})
}

func TestCron_StopCancelsInflightJobContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		var seenCause atomic.Pointer[error]
		jobReady := make(chan struct{})
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			select {
			case <-jobReady:
			default:
				close(jobReady)
			}
			<-ctx.Done()
			cause := context.Cause(ctx)
			seenCause.CompareAndSwap(nil, &cause)
			return ctx.Err()
		}))
		_ = c.Start()
		<-jobReady // first invocation has entered

		if err := c.Stop(context.Background()); err != nil {
			t.Fatalf("Stop err: %v", err)
		}
		got := seenCause.Load()
		if got == nil {
			t.Fatal("Stop did not cancel job ctx (job never returned)")
		}
		if !errors.Is(*got, cron.ErrCronStopping) {
			t.Fatalf("cause = %v, want ErrCronStopping", *got)
		}
	})
}

func TestCron_StopWithoutStartReturnsImmediately(t *testing.T) {
	c := cron.New()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := c.Stop(ctx); err != nil {
		t.Fatalf("Stop on unstarted cron returned %v, want nil", err)
	}
}

func TestCron_StopIsIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error { return nil }))
		_ = c.Start()
		time.Sleep(2 * time.Second)
		synctest.Wait()

		if err := c.Stop(context.Background()); err != nil {
			t.Fatalf("first Stop: %v", err)
		}
		if err := c.Stop(context.Background()); err != nil {
			t.Fatalf("second Stop: %v", err)
		}
	})
}

func TestCron_StartAfterStopReturnsErr(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hk := &recordingHook{}
		c := cron.New(cron.WithLocation(time.UTC), cron.WithHooks(hk))
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error { return nil }))
		if err := c.Start(); err != nil {
			t.Fatalf("first Start: %v", err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
		first := hk.starts.Load()
		if first == 0 {
			t.Fatal("hook never invoked before stop")
		}

		if err := c.Start(); !errors.Is(err, cron.ErrSchedulerStopped) {
			t.Fatalf("Start after Stop err = %v, want ErrSchedulerStopped", err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if hk.starts.Load() != first {
			t.Fatalf("Start after Stop dispatched jobs: before=%d after=%d", first, hk.starts.Load())
		}
	})
}

func TestCron_StartIdempotentWhileRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatalf("second Start while running should be nil, got %v", err)
		}
		_ = c.Stop(context.Background())
	})
}

func TestCron_Running(t *testing.T) {
	c := cron.New()
	if c.Running() {
		t.Fatal("Running() before Start should be false")
	}
	_ = c.Start()
	if !c.Running() {
		t.Fatal("Running() after Start should be true")
	}
	_ = c.Stop(context.Background())
	if c.Running() {
		t.Fatal("Running() after Stop should be false")
	}
}

func TestCron_StopRespectsContextDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		unblock := make(chan struct{})
		t.Cleanup(func() {
			close(unblock)
			_ = c.Stop(context.Background())
		})
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			<-unblock
			return nil
		}))
		_ = c.Start()
		time.Sleep(2 * time.Second)
		synctest.Wait()

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		err := c.Stop(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop err = %v, want DeadlineExceeded", err)
		}
	})
}

func TestCron_Trigger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		var calls atomic.Int64
		id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(func(ctx context.Context) error {
			calls.Add(1)
			return nil
		}), cron.WithName("manual"))
		_ = c.Start()

		time.Sleep(5 * time.Second)
		synctest.Wait()
		if got := calls.Load(); got != 0 {
			t.Fatalf("auto fires for triggered schedule: %d", got)
		}
		if err := c.Trigger(id); err != nil {
			t.Fatalf("Trigger: %v", err)
		}
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Fatalf("after trigger calls = %d", got)
		}
		_ = c.Stop(context.Background())
	})
}

func TestCron_Timeout_CancelsViaCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		var seenCause atomic.Pointer[error]
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			<-ctx.Done()
			cause := context.Cause(ctx)
			seenCause.CompareAndSwap(nil, &cause)
			return ctx.Err()
		}), cron.WithTimeout(500*time.Millisecond))
		_ = c.Start()
		time.Sleep(1700 * time.Millisecond)
		synctest.Wait()
		_ = c.Stop(context.Background())

		got := seenCause.Load()
		if got == nil {
			t.Fatal("no cause captured")
		}
		if !errors.Is(*got, cron.ErrJobTimeout) {
			t.Fatalf("cause = %v, want ErrJobTimeout", *got)
		}
	})
}

func TestCron_Entries_OrderedByNext(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	_, _ = c.Add("@every 5m", cron.JobFunc(noop), cron.WithName("slow"))
	_, _ = c.Add("@every 1m", cron.JobFunc(noop), cron.WithName("fast"))
	got := slices.Collect(c.Entries())
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if !got[0].Next.Before(got[1].Next) {
		t.Fatalf("not sorted: %v %v", got[0].Next, got[1].Next)
	}
	if got[0].Name != "fast" {
		t.Fatalf("first = %q", got[0].Name)
	}
}

func noop(ctx context.Context) error { return nil }

func TestCron_MissedRunOnce_FiresFirstMissedWhenOnlyOneMissed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(
			cron.WithLocation(time.UTC),
			cron.WithMissedFire(cron.MissedRunOnce),
			cron.WithMissedTolerance(100*time.Millisecond),
		)
		var calls atomic.Int32
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			calls.Add(1)
			return nil
		}))
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		_ = c.Start()
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		_ = c.Stop(context.Background())

		if got := calls.Load(); got < 1 {
			t.Fatalf("MissedRunOnce did not fire the single missed firing; calls=%d", got)
		}
	})
}

func TestCron_StopBeforeStartLocksOutFutureStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		var calls atomic.Int32
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			calls.Add(1)
			return nil
		}))
		_ = c.Stop(context.Background())
		if err := c.Start(); !errors.Is(err, cron.ErrSchedulerStopped) {
			t.Fatalf("Start err = %v, want ErrSchedulerStopped", err)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if got := calls.Load(); got != 0 {
			t.Fatalf("Start after Stop should not dispatch; calls=%d", got)
		}
	})
}

func TestCron_StopSuppressedFiringDoesNotEmitMissed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var missed atomic.Int32
		hk := &countingHook{missed: &missed}
		c := cron.New(
			cron.WithLocation(time.UTC),
			cron.WithHooks(hk),
		)
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			return nil
		}))
		_ = c.Start()
		synctest.Wait()

		_ = c.Stop(context.Background())

		if got := missed.Load(); got != 0 {
			t.Fatalf("Stop-suppressed firing recorded as missed: missed=%d", got)
		}
	})
}

type countingHook struct {
	missed *atomic.Int32
}

func (countingHook) OnSchedule(cron.EventSchedule)       {}
func (countingHook) OnJobStart(cron.EventJobStart)       {}
func (countingHook) OnJobComplete(cron.EventJobComplete) {}
func (h *countingHook) OnMissedFire(cron.EventMissed)    { h.missed.Add(1) }

func TestCron_TriggerLosesRaceWithRemove(t *testing.T) {
	for trial := range 200 {
		c := cron.New(cron.WithLocation(time.UTC))
		var ran atomic.Bool
		id, _ := c.AddSchedule(cron.TriggeredSchedule(),
			cron.JobFunc(func(ctx context.Context) error {
				ran.Store(true)
				return nil
			}))
		_ = c.Start()

		var triggerOK, removeOK atomic.Bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); triggerOK.Store(c.Trigger(id) == nil) }()
		go func() { defer wg.Done(); removeOK.Store(c.Remove(id)) }()
		wg.Wait()
		_ = c.Stop(context.Background())

		if removeOK.Load() && triggerOK.Load() {
			continue
		}
		if !triggerOK.Load() && ran.Load() {
			t.Fatalf("trial %d: Trigger errored but Job still ran", trial)
		}
	}
}

func TestCron_MaxConcurrentSaturatedDropsTrigger(t *testing.T) {
	release := make(chan struct{})
	hk := &recordingHook{}
	c := cron.New(
		cron.WithLocation(time.UTC),
		cron.WithMaxConcurrent(1),
		cron.WithHooks(hk),
	)
	var ran atomic.Int32
	id, _ := c.AddSchedule(cron.TriggeredSchedule(),
		cron.JobFunc(func(ctx context.Context) error {
			ran.Add(1)
			<-release
			return nil
		}))
	_ = c.Start()
	if err := c.Trigger(id); err != nil {
		t.Fatalf("first Trigger: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && ran.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	if ran.Load() < 1 {
		close(release)
		_ = c.Stop(context.Background())
		t.Fatal("first job never started")
	}
	if err := c.Trigger(id); !errors.Is(err, cron.ErrConcurrencyLimit) {
		t.Fatalf("second Trigger err = %v, want ErrConcurrencyLimit", err)
	}
	close(release)
	_ = c.Stop(context.Background())
	if hk.missed.Load() == 0 {
		t.Fatal("rejected Trigger should emit OnMissedFire")
	}
}

func TestCron_MaxConcurrentSchedulerSaturationEmitsMissed(t *testing.T) {
	r := &counterRecorder{}
	release := make(chan struct{})
	c := cron.New(
		cron.WithLocation(time.UTC),
		cron.WithMaxConcurrent(1),
		cron.WithRecorder(r),
	)
	_, _ = c.Add("@every 100ms", cron.JobFunc(func(ctx context.Context) error {
		<-release
		return nil
	}))
	_ = c.Start()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.missed.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	_ = c.Stop(context.Background())
	if r.missed.Load() == 0 {
		t.Fatal("expected JobMissed when MaxConcurrent saturates")
	}
}

func TestCron_TriggerOnUnknownIDReturnsErr(t *testing.T) {
	c := cron.New()
	_ = c.Start()
	defer func() { _ = c.Stop(context.Background()) }()
	if err := c.Trigger(cron.EntryID(999)); !errors.Is(err, cron.ErrEntryNotFound) {
		t.Fatalf("Trigger err = %v, want ErrEntryNotFound", err)
	}
}

func TestCron_TriggerWhenNotRunning(t *testing.T) {
	c := cron.New()
	id, _ := c.AddSchedule(cron.TriggeredSchedule(), cron.JobFunc(noop))
	if err := c.Trigger(id); !errors.Is(err, cron.ErrSchedulerNotRunning) {
		t.Fatalf("Trigger err = %v, want ErrSchedulerNotRunning", err)
	}
}

func TestCron_AddCacheHitErrorReturned(t *testing.T) {
	c := cron.New()
	if _, err := c.Add("@nope", cron.JobFunc(noop)); err == nil {
		t.Fatal("first Add expected ParseError")
	}
	if _, err := c.Add("@nope", cron.JobFunc(noop)); err == nil {
		t.Fatal("cache-hit on bad spec should still return error")
	}
}

func TestCron_AddCacheHitSuccess(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC))
	id1, err := c.Add("@every 1m", cron.JobFunc(noop))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := c.Add("@every 1m", cron.JobFunc(noop))
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatal("entry IDs must differ")
	}
}

func TestCron_JitterSucceedsBeforeStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(
			cron.WithLocation(time.UTC),
			cron.WithJitter(10*time.Millisecond),
		)
		var calls atomic.Int64
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			calls.Add(1)
			return nil
		}))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if calls.Load() < 2 {
			t.Fatalf("jitter timer success path never hit; calls=%d", calls.Load())
		}
	})
}

func TestCron_JitterZeroDurationIsNoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(
			cron.WithLocation(time.UTC),
			cron.WithJitter(1),
		)
		var calls atomic.Int64
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			calls.Add(1)
			return nil
		}))
		_ = c.Start()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if calls.Load() < 1 {
			t.Fatalf("jitter d<=0 fast path skipped firing; calls=%d", calls.Load())
		}
	})
}

func TestCron_PrevNotAdvancedWhenJitterCancelsBeforeRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(
			cron.WithLocation(time.UTC),
			cron.WithJitter(10*time.Second),
		)
		var calls atomic.Int32
		id, _ := c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			calls.Add(1)
			return nil
		}))
		_ = c.Start()
		time.Sleep(1100 * time.Millisecond)
		synctest.Wait()
		_ = c.Stop(context.Background())

		if calls.Load() != 0 {
			t.Fatalf("expected job to be cancelled in jitter; calls=%d", calls.Load())
		}
		view, ok := c.Entry(id)
		if !ok {
			t.Fatal("entry vanished")
		}
		if !view.Prev.IsZero() {
			t.Fatalf("Prev advanced for a fire that never ran: %v", view.Prev)
		}
	})
}
