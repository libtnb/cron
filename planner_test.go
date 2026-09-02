package cron_test

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libtnb/cron"
)

// sleepySchedule fires every period but spends nextCost inside Next, like a
// Filter backed by a slow calendar lookup.
type sleepySchedule struct {
	period   time.Duration
	nextCost time.Duration
}

func (s sleepySchedule) Next(now time.Time) time.Time {
	time.Sleep(s.nextCost)
	return now.Truncate(s.period).Add(s.period)
}

func TestCron_SlowScheduleNextDoesNotStarveOtherEntries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fastRuns atomic.Int64
		var fastMissed atomic.Int64
		c := cron.MustNew(cron.WithLocation(time.UTC), cron.WithRecorder(cron.RecorderFunc(func(e cron.Event) {
			if m, ok := e.(cron.MissedFireEvent); ok && m.Entry.Name == "fast" {
				fastMissed.Add(1)
			}
		})))
		_, err := c.AddSchedule(sleepySchedule{period: 2 * time.Second, nextCost: 2500 * time.Millisecond},
			cron.JobFunc(noop), cron.WithName("slow"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			fastRuns.Add(1)
			return nil
		}), cron.WithName("fast"))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())

		if got := fastRuns.Load(); got < 9 {
			t.Fatalf("@every 1s ran %d times in 10s while another schedule's Next was slow; want at least 9", got)
		}
		if got := fastMissed.Load(); got != 0 {
			t.Fatalf("fast entry reported %d missed fires; want 0", got)
		}
	})
}

func TestCron_DefaultPolicyCatchesUpOnceAfterOutage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int64
		scheduledAt := make(chan time.Time, 16)
		c := cron.MustNew(cron.WithLocation(time.UTC))
		_, err := c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			info, _ := cron.EntryInfoFromContext(ctx)
			scheduledAt <- info.ScheduledAt
			runs.Add(1)
			return nil
		}), cron.WithLastRun(time.Now().Add(-30*time.Hour)))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		if got := runs.Load(); got != 1 {
			t.Fatalf("default missed-fire policy ran %d catch-up fires, want exactly 1", got)
		}
		if at := <-scheduledAt; time.Since(at) > time.Second {
			t.Fatalf("catch-up fire was scheduled %v ago; want the most recent missed instant", time.Since(at))
		}
		_ = c.Stop(context.Background())
	})
}

func TestCron_ShortLatenessRunsLateInsteadOfMissed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var missed atomic.Int64
		c := cron.MustNew(cron.WithLocation(time.UTC), cron.WithRecorder(cron.RecorderFunc(func(e cron.Event) {
			if _, ok := e.(cron.MissedFireEvent); ok {
				missed.Add(1)
			}
		})))
		ran := make(chan time.Time, 1)
		_, err := c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error {
			info, _ := cron.EntryInfoFromContext(ctx)
			ran <- info.ScheduledAt
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		// Start 30s after the first due time: within the default tolerance, so
		// the original instant fires late rather than being classified missed.
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		at := <-ran
		synctest.Wait()
		_ = c.Stop(context.Background())
		if missed.Load() != 0 {
			t.Fatalf("a 30s late fire was reported missed under the default tolerance")
		}
		if lateness := time.Since(at); lateness < 29*time.Second {
			t.Fatalf("expected the original due instant to fire, lateness = %v", lateness)
		}
	})
}

// stuckSchedule violates the Schedule contract after its first firing by
// returning a time that is not after now.
type stuckSchedule struct{ at time.Time }

func (s stuckSchedule) Next(time.Time) time.Time { return s.at }

func TestCron_NonAdvancingScheduleIsExhaustedNotSpun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int64
		c := cron.MustNew(cron.WithLocation(time.UTC))
		id, err := c.AddSchedule(stuckSchedule{at: time.Now().Add(time.Second)}, cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())

		if got := runs.Load(); got != 1 {
			t.Fatalf("job ran %d times, want 1: a non-advancing schedule must not spin", got)
		}
		e, ok := c.Entry(id)
		if !ok || !e.Next.IsZero() {
			t.Fatalf("entry should be exhausted, got %+v ok=%v", e, ok)
		}
	})
}

func TestCron_SubSecondEverySpecFires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int64
		c := cron.MustNew(cron.WithLocation(time.UTC))
		if _, err := c.Add("@every 100ms", cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		})); err != nil {
			t.Fatal(err)
		}
		_ = c.Start()
		time.Sleep(time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if got := runs.Load(); got < 9 || got > 11 {
			t.Fatalf("@every 100ms ran %d times in 1s, want about 10", got)
		}
	})
}
