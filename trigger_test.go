package cron_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libtnb/cron"
)

func TestTriggerByName_NotRunning(t *testing.T) {
	c := cron.New()
	got, err := c.TriggerByName("none")
	if got != 0 || !errors.Is(err, cron.ErrSchedulerNotRunning) {
		t.Fatalf("got %d, %v; want 0, ErrSchedulerNotRunning", got, err)
	}
}

func TestTriggerByName_NoMatchIsNotError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		_ = c.Start()
		got, err := c.TriggerByName("nobody")
		_ = c.Stop(context.Background())
		if got != 0 || err != nil {
			t.Fatalf("got %d, %v; want 0, nil", got, err)
		}
	})
}

func TestTriggerByName_DispatchesMatching(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := cron.New(cron.WithLocation(time.UTC))
		var called atomic.Int32
		_, _ = c.AddSchedule(cron.TriggeredSchedule(),
			cron.JobFunc(func(ctx context.Context) error { called.Add(1); return nil }),
			cron.WithName("group"))
		_, _ = c.AddSchedule(cron.TriggeredSchedule(),
			cron.JobFunc(func(ctx context.Context) error { called.Add(1); return nil }),
			cron.WithName("group"))
		_, _ = c.AddSchedule(cron.TriggeredSchedule(),
			cron.JobFunc(func(ctx context.Context) error {
				t.Fatal("'other' should not run")
				return nil
			}),
			cron.WithName("other"))
		_ = c.Start()
		got, err := c.TriggerByName("group")
		if got != 2 || err != nil {
			t.Fatalf("dispatched=%d err=%v, want 2, nil", got, err)
		}
		synctest.Wait()
		_ = c.Stop(context.Background())
		if got := called.Load(); got != 2 {
			t.Fatalf("called=%d, want 2", got)
		}
	})
}

func TestTriggerByName_PartialFailureJoinsErrors(t *testing.T) {
	release := make(chan struct{})
	c := cron.New(cron.WithLocation(time.UTC), cron.WithMaxConcurrent(1))
	for range 3 {
		_, _ = c.AddSchedule(cron.TriggeredSchedule(),
			cron.JobFunc(func(ctx context.Context) error { <-release; return nil }),
			cron.WithName("g"))
	}
	_ = c.Start()
	got, err := c.TriggerByName("g")
	close(release)
	_ = c.Stop(context.Background())
	if got != 1 {
		t.Fatalf("dispatched=%d, want 1", got)
	}
	if !errors.Is(err, cron.ErrConcurrencyLimit) {
		t.Fatalf("err=%v, want errors.Is ErrConcurrencyLimit", err)
	}
}
