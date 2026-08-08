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

type recordingObserver struct {
	starts    atomic.Int64
	completes atomic.Int64
	missed    atomic.Int64
	rejected  atomic.Int64
	schedules atomic.Int64
	lastErr   atomic.Pointer[error]
}

func (o *recordingObserver) Observe(event cron.Event) {
	switch event := event.(type) {
	case cron.ScheduleEvent:
		o.schedules.Add(1)
	case cron.JobStartEvent:
		o.starts.Add(1)
	case cron.JobCompleteEvent:
		o.completes.Add(1)
		if event.Err != nil {
			err := event.Err
			o.lastErr.Store(&err)
		}
	case cron.MissedFireEvent:
		o.missed.Add(1)
	case cron.RejectedFireEvent:
		o.rejected.Add(1)
	}
}

func TestObserver_StartCompleteFire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &recordingObserver{}
		c := cron.MustNew(cron.WithLocation(time.UTC), cron.WithObservers(o))
		_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error { return nil }))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())

		if got := o.starts.Load(); got < 2 || got > 4 {
			t.Fatalf("starts = %d", got)
		}
		if got := o.completes.Load(); got != o.starts.Load() {
			t.Fatalf("starts %d != completes %d", o.starts.Load(), got)
		}
	})
}

type panicObserver struct{ recordingObserver }

func (o *panicObserver) Observe(event cron.Event) {
	o.recordingObserver.Observe(event)
	if _, ok := event.(cron.JobStartEvent); ok {
		panic("observer boom")
	}
}

func TestObserver_PanicIsIsolated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &panicObserver{}
		c := cron.MustNew(
			cron.WithLocation(time.UTC),
			cron.WithLogger(slog.New(slog.DiscardHandler)),
			cron.WithObservers(o),
		)
		var calls atomic.Int64
		_, _ = c.Add("@every 1s", cron.JobFunc(func(context.Context) error {
			calls.Add(1)
			return nil
		}))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if calls.Load() < 2 || o.starts.Load() < 2 {
			t.Fatalf("calls = %d, observed starts = %d", calls.Load(), o.starts.Load())
		}
	})
}

type blockingObserver struct {
	recordingObserver
	release chan struct{}
}

func (o *blockingObserver) Observe(event cron.Event) {
	o.recordingObserver.Observe(event)
	if _, ok := event.(cron.JobStartEvent); ok {
		<-o.release
	}
}

func TestObserver_FilterWithFunc(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var completed atomic.Int64
		o := cron.ObserverFunc(func(event cron.Event) {
			if _, ok := event.(cron.JobCompleteEvent); ok {
				completed.Add(1)
			}
		})
		c := cron.MustNew(cron.WithLocation(time.UTC), cron.WithObservers(o))
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

func TestObserver_EventCarriesEntryIdentityAndStartTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		starts := make(chan cron.JobStartEvent, 1)
		o := cron.ObserverFunc(func(event cron.Event) {
			if event, ok := event.(cron.JobStartEvent); ok {
				starts <- event
			}
		})
		c := cron.MustNew(
			cron.WithLocation(time.UTC),
			cron.WithMissedFire(cron.MissedRunOnce),
			cron.WithMissedTolerance(100*time.Millisecond),
			cron.WithObservers(o),
		)
		id, _ := c.Add("@every 1s", cron.JobFunc(func(context.Context) error { return nil }),
			cron.WithKey("billing"), cron.WithName("billing report"))
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}

		var event cron.JobStartEvent
		select {
		case event = <-starts:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for start event")
		}
		_ = c.Stop(context.Background())

		if event.Entry != (cron.EntryRef{ID: id, Key: "billing", Name: "billing report"}) {
			t.Fatalf("entry = %+v", event.Entry)
		}
		if !event.StartedAt.After(event.ScheduledAt) {
			t.Fatalf("started at %v, scheduled at %v", event.StartedAt, event.ScheduledAt)
		}
	})
}

func TestObserver_NilRejected(t *testing.T) {
	if _, err := cron.New(cron.WithObservers(nil)); err == nil {
		t.Fatal("nil observer accepted")
	}
	var observer *recordingObserver
	if _, err := cron.New(cron.WithObservers(observer)); err == nil {
		t.Fatal("typed nil observer accepted")
	}
}

func TestObserver_CloseRespectsContextDeadline(t *testing.T) {
	o := &blockingObserver{release: make(chan struct{})}
	c := cron.MustNew(
		cron.WithLocation(time.UTC),
		cron.WithObservers(o),
		cron.WithObserverBuffer(8),
		cron.WithLogger(slog.New(slog.DiscardHandler)),
	)
	id, _ := c.AddSchedule(cron.TriggeredSchedule(),
		cron.JobFunc(func(context.Context) error { return nil }))
	_ = c.Start()
	_ = c.Trigger(id)
	for o.starts.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.Stop(ctx)
	close(o.release)
	if err == nil {
		t.Fatal("expected context error from blocked observer close")
	}
}

func TestObserver_DropsWhenQueueIsFull(t *testing.T) {
	o := &blockingObserver{release: make(chan struct{})}
	r := &counterRecorder{}
	c := cron.MustNew(
		cron.WithLocation(time.UTC),
		cron.WithObservers(o),
		cron.WithObserverBuffer(1),
		cron.WithRecorder(r),
		cron.WithLogger(slog.New(slog.DiscardHandler)),
	)
	id, _ := c.AddSchedule(cron.TriggeredSchedule(),
		cron.JobFunc(func(context.Context) error { return nil }))
	_ = c.Start()
	for range 200 {
		_ = c.Trigger(id)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.dropped.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	close(o.release)
	_ = c.Stop(context.Background())
	if r.dropped.Load() == 0 {
		t.Fatal("expected at least one dropped observer event")
	}
}
