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

type recordingHook struct {
	starts    atomic.Int64
	completes atomic.Int64
	missed    atomic.Int64
	scheds    atomic.Int64
	lastErr   atomic.Pointer[error]
}

func (h *recordingHook) OnSchedule(_ cron.EventSchedule) { h.scheds.Add(1) }
func (h *recordingHook) OnJobStart(_ cron.EventJobStart) { h.starts.Add(1) }
func (h *recordingHook) OnJobComplete(e cron.EventJobComplete) {
	h.completes.Add(1)
	if e.Err != nil {
		err := e.Err
		h.lastErr.Store(&err)
	}
}
func (h *recordingHook) OnMissedFire(_ cron.EventMissed) { h.missed.Add(1) }

func TestHook_StartCompleteFire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &recordingHook{}
		c := cron.New(cron.WithLocation(time.UTC), cron.WithHooks(h))
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error { return nil }))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())

		if got := h.starts.Load(); got < 2 || got > 4 {
			t.Fatalf("starts = %d", got)
		}
		if got := h.completes.Load(); got != h.starts.Load() {
			t.Fatalf("starts %d != completes %d", h.starts.Load(), got)
		}
	})
}

type panicHook struct{ recordingHook }

func (p *panicHook) OnJobStart(e cron.EventJobStart) {
	p.recordingHook.OnJobStart(e)
	panic("hook boom")
}

func TestHook_PanicIsIsolated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ph := &panicHook{}
		c := cron.New(cron.WithLocation(time.UTC), cron.WithHooks(ph))
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
			t.Fatalf("calls = %d", calls.Load())
		}
		if ph.starts.Load() < 2 {
			t.Fatalf("starts = %d (hook should have been invoked)", ph.starts.Load())
		}
	})
}

type blockingHook struct {
	recordingHook
	release chan struct{}
}

func (h *blockingHook) OnJobStart(e cron.EventJobStart) {
	h.recordingHook.OnJobStart(e)
	<-h.release
}

type jobCompleteOnlyHook struct {
	completes atomic.Int64
}

func (h *jobCompleteOnlyHook) OnJobComplete(cron.EventJobComplete) { h.completes.Add(1) }

func TestHook_PartialImplementation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hk := &jobCompleteOnlyHook{}
		c := cron.New(cron.WithLocation(time.UTC), cron.WithHooks(hk))
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error { return nil }))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if hk.completes.Load() < 2 {
			t.Fatalf("OnJobComplete fired %d times, want >=2", hk.completes.Load())
		}
	})
}

type jobStartCaptureHook struct {
	ch chan cron.EventJobStart
}

func (h *jobStartCaptureHook) OnJobStart(e cron.EventJobStart) { h.ch <- e }

func TestHook_FireAtIsActualDispatchTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hk := &jobStartCaptureHook{ch: make(chan cron.EventJobStart, 1)}
		c := cron.New(
			cron.WithLocation(time.UTC),
			cron.WithMissedFire(cron.MissedRunOnce),
			cron.WithMissedTolerance(100*time.Millisecond),
			cron.WithHooks(hk),
		)
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error { return nil }))
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}

		var ev cron.EventJobStart
		select {
		case ev = <-hk.ch:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for OnJobStart")
		}
		_ = c.Stop(context.Background())

		if !ev.FireAt.After(ev.ScheduledAt) {
			t.Fatalf("FireAt = %v, ScheduledAt = %v; want actual dispatch time after missed scheduled time",
				ev.FireAt, ev.ScheduledAt)
		}
	})
}

func TestHook_NonHookValueIgnored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type notAHook struct{}
		c := cron.New(cron.WithLocation(time.UTC), cron.WithHooks(notAHook{}))
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error { return nil }))
		_ = c.Start()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
	})
}

func TestHook_DropsWhenChannelFull(t *testing.T) {
	bh := &blockingHook{release: make(chan struct{})}
	r := &counterRecorder{}
	c := cron.New(
		cron.WithLocation(time.UTC),
		cron.WithHooks(bh),
		cron.WithHookBuffer(1),
		cron.WithRecorder(r),
		cron.WithLogger(slog.New(slog.DiscardHandler)),
	)
	id, _ := c.AddSchedule(cron.TriggeredSchedule(),
		cron.JobFunc(func(ctx context.Context) error { return nil }))
	_ = c.Start()
	for range 200 {
		_ = c.Trigger(id)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.dropped.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	close(bh.release)
	_ = c.Stop(context.Background())
	if r.dropped.Load() == 0 {
		t.Fatal("expected at least one dropped hook event")
	}
}
