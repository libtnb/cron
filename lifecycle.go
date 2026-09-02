package cron

import (
	"context"
	"time"
)

// Start launches the scheduler loop; entries registered earlier fire once it
// runs. Start is idempotent while running and returns ErrSchedulerStopped once
// Stop or Drain has been called, even before any Start: a Cron cannot be
// restarted.
func (c *Cron) Start() error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.started {
		if c.running.Load() {
			return nil
		}
		return ErrSchedulerStopped
	}
	c.started = true
	c.running.Store(true)
	base := c.cfg.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancelCause(base)
	c.runCtx = ctx
	c.runCancel = cancel
	loopCtx, loopCancel := context.WithCancel(ctx)
	c.loopCancel = loopCancel
	c.runDone = make(chan struct{})
	go c.loop(loopCtx)
	return nil
}

// Running reports whether the scheduler loop is active. It is observational
// only; use Trigger's returned error for race-free dispatch decisions.
func (c *Cron) Running() bool { return c.running.Load() }

// Stop halts the scheduler, cancels in-flight jobs with ErrCronStopping as the
// context cause, and waits for the loop, the jobs and the observer queue to
// drain, capped by ctx. Returns ErrNilContext for a nil ctx and ctx.Err() when
// the wait times out; jobs still running at that point keep their cancelled
// context. Calling Stop before Start marks the Cron stopped. Do not call it
// from inside a Job: it waits for that job.
func (c *Cron) Stop(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	c.startMu.Lock()
	c.started = true
	if c.runDone == nil {
		c.startMu.Unlock()
		return c.events.close(ctx)
	}
	c.running.Store(false)
	c.runCancel(ErrCronStopping)
	done := c.runDone
	c.startMu.Unlock()
	return c.awaitShutdown(ctx, done)
}

// Drain is Stop without cancelling in-flight jobs: it stops scheduling new
// fires and waits for running jobs to finish naturally, capped by ctx. Returns
// ErrNilContext or ctx.Err() like Stop. Do not call it from inside a Job.
func (c *Cron) Drain(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	c.startMu.Lock()
	c.started = true
	if c.runDone == nil {
		c.startMu.Unlock()
		return c.events.close(ctx)
	}
	if c.running.Swap(false) {
		c.loopCancel()
	}
	done := c.runDone
	c.startMu.Unlock()
	return c.awaitShutdown(ctx, done)
}

// awaitShutdown waits, in order, for the loop, then every planner and job
// goroutine, then the observer queue. jobsOnce lets Stop and Drain race
// without closing jobsDone twice.
func (c *Cron) awaitShutdown(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	c.jobsOnce.Do(func() {
		go func() {
			c.wg.Wait()
			close(c.jobsDone)
		}()
	})
	select {
	case <-c.jobsDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	return c.events.close(ctx)
}

// loop wakes for the earliest heap entry or an explicit wake and pops what is
// due. It runs no user code: planning (Schedule.Next) and jobs are dispatched
// to their own goroutines by fireDue.
func (c *Cron) loop(ctx context.Context) {
	defer close(c.runDone)
	defer c.running.Store(false)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		timer.Reset(c.peekDelay())
		select {
		case <-ctx.Done():
			return
		case <-c.wakeCh:
		case <-timer.C:
			c.fireDue(ctx, time.Now())
		}
	}
}

// peekDelay returns how long the loop may sleep before the earliest entry is
// due: 24 hours when the heap is empty (a wake cuts it short), zero when an
// entry is already overdue.
func (c *Cron) peekDelay() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.h.Peek()
	if !ok {
		return 24 * time.Hour
	}
	d := time.Until(time.Unix(0, it.Key))
	if d < 0 {
		return 0
	}
	return d
}

// wake nudges the loop to re-arm its timer after the heap changed. The
// channel is buffered with one slot, so redundant wakes coalesce.
func (c *Cron) wake() {
	select {
	case c.wakeCh <- struct{}{}:
	default:
	}
}
