package cron_test

import (
	"context"
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
	depth     atomic.Int64
	dropped   atomic.Int64
}

func (r *counterRecorder) JobScheduled(string)                       { r.scheduled.Add(1) }
func (r *counterRecorder) JobStarted(string)                         { r.started.Add(1) }
func (r *counterRecorder) JobCompleted(string, time.Duration, error) { r.completed.Add(1) }
func (r *counterRecorder) JobMissed(string, time.Duration)           { r.missed.Add(1) }
func (r *counterRecorder) QueueDepth(n int)                          { r.depth.Store(int64(n)) }
func (r *counterRecorder) HookDropped()                              { r.dropped.Add(1) }

func TestRecorder_StartedAndCompletedMatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &counterRecorder{}
		c := cron.New(cron.WithLocation(time.UTC), cron.WithRecorder(r))
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error { return nil }))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())

		if r.started.Load() != r.completed.Load() {
			t.Fatalf("started %d != completed %d", r.started.Load(), r.completed.Load())
		}
		if r.scheduled.Load() < 2 {
			t.Fatalf("scheduled = %d, want at least 2 (Add + reschedule)", r.scheduled.Load())
		}
		if r.depth.Load() == 0 {
			t.Fatal("QueueDepth was never updated")
		}
	})
}

func TestRecorder_NoopByDefault(t *testing.T) {
	c := cron.New()
	_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error { return nil }))
}

func TestNoopRecorder_AllMethods(t *testing.T) {
	r := cron.NoopRecorder{}
	r.JobScheduled("x")
	r.JobStarted("x")
	r.JobCompleted("x", time.Second, nil)
	r.JobMissed("x", time.Second)
	r.QueueDepth(0)
	r.HookDropped()
}

type completedOnlyRecorder struct{ n atomic.Int64 }

func (r *completedOnlyRecorder) JobCompleted(string, time.Duration, error) { r.n.Add(1) }

func TestRecorder_PartialImplementation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &completedOnlyRecorder{}
		c := cron.New(cron.WithLocation(time.UTC), cron.WithRecorder(r))
		_, _ = c.Add("@every 1s", cron.JobFunc(func(ctx context.Context) error { return nil }))
		_ = c.Start()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		_ = c.Stop(context.Background())
		if r.n.Load() < 2 {
			t.Fatalf("JobCompleted fired %d times, want >=2", r.n.Load())
		}
	})
}

func TestRecorder_NilDoesNotPanic(t *testing.T) {
	c := cron.New(cron.WithLocation(time.UTC), cron.WithRecorder(nil))
	_, _ = c.Add("@every 1m", cron.JobFunc(func(ctx context.Context) error { return nil }))
}
