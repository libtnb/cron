package cron_test

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libtnb/cron"
)

type counterRecorder struct {
	scheduled atomic.Int64
	started   atomic.Int64
	completed atomic.Int64
	missed    atomic.Int64
	rejected  atomic.Int64
	canceled  atomic.Int64
	skipped   atomic.Int64
	depth     atomic.Int64
	dropped   atomic.Int64
}

func (r *counterRecorder) Record(event cron.Event) {
	switch event := event.(type) {
	case cron.ScheduleEvent:
		r.scheduled.Add(1)
	case cron.JobStartEvent:
		r.started.Add(1)
	case cron.JobCompleteEvent:
		r.completed.Add(1)
	case cron.MissedFireEvent:
		r.missed.Add(1)
	case cron.RejectedFireEvent:
		r.rejected.Add(1)
	case cron.CanceledFireEvent:
		r.canceled.Add(1)
	case cron.SkippedFireEvent:
		r.skipped.Add(1)
	case cron.QueueDepthEvent:
		r.depth.Store(int64(event.Depth))
	case cron.ObserverDropEvent:
		r.dropped.Store(event.Dropped)
	}
}

func TestRecorder_StartedAndCompletedMatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &counterRecorder{}
		c := cron.MustNew(cron.WithLocation(time.UTC), cron.WithRecorder(r))
		_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error { return nil }))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())

		if r.started.Load() != r.completed.Load() {
			t.Fatalf("started %d != completed %d", r.started.Load(), r.completed.Load())
		}
		if r.scheduled.Load() < 2 {
			t.Fatalf("scheduled = %d, want at least 2", r.scheduled.Load())
		}
		if r.depth.Load() == 0 {
			t.Fatal("queue depth was never updated")
		}
	})
}

func TestRecorder_FilterWithFunc(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var completed atomic.Int64
		recorder := cron.RecorderFunc(func(event cron.Event) {
			if _, ok := event.(cron.JobCompleteEvent); ok {
				completed.Add(1)
			}
		})
		c := cron.MustNew(cron.WithLocation(time.UTC), cron.WithRecorder(recorder))
		_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error { return nil }))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if completed.Load() < 2 {
			t.Fatalf("completed events = %d, want at least 2", completed.Load())
		}
	})
}

func TestRecorder_PanicIsIsolated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int64
		c := cron.MustNew(
			cron.WithLocation(time.UTC),
			cron.WithLogger(slog.New(slog.DiscardHandler)),
			cron.WithRecorder(cron.RecorderFunc(func(cron.Event) { panic("boom") })),
		)
		_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			runs.Add(1)
			return nil
		}))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if runs.Load() < 2 {
			t.Fatalf("runs = %d, recorder panic affected scheduling", runs.Load())
		}
	})
}

func TestRecorder_NoopByDefault(t *testing.T) {
	c := cron.MustNew()
	_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error { return nil }))
}

func TestRecorder_NilRejected(t *testing.T) {
	if _, err := cron.New(cron.WithRecorder(nil)); err == nil {
		t.Fatal("nil recorder accepted")
	}
	var recorder *counterRecorder
	if _, err := cron.New(cron.WithRecorder(recorder)); err == nil {
		t.Fatal("typed nil recorder accepted")
	}
}
