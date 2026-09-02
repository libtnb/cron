package cron

import (
	"context"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"runtime/debug"
	"time"
)

// This file is the fire pipeline. The loop pops due entries under c.mu and
// hands each one to a planner goroutine, which decides what to run without
// holding the lock or blocking the loop:
//
//	loop: pop due entries
//	  |
//	  v
//	makeFirePlan (per entry, own goroutine): lateness, missed-fire policy,
//	  |                                      Schedule.Next for the next fire
//	  v
//	commitAndDispatch (under c.mu): drop the plan if the entry changed since
//	  |                             the pop, reserve concurrency slots,
//	  |                             re-queue, publish events
//	  v
//	dispatch (per fire, own goroutine): jitter -> elector -> claimer ->
//	                                    timeout ctx -> job -> events
//
// Generation numbers make plans safe to compute outside the lock: Pause,
// Resume and Update bump entry.gen, and a plan whose gen no longer matches is
// discarded instead of firing stale state.

// dueFire captures everything a planner needs while still under c.mu;
// schedule and gen are snapshotted because Update can swap them concurrently.
type dueFire struct {
	e         *entry
	schedule  Schedule
	scheduled time.Time
	gen       uint64
}

// firePlan is a planner's decision for one popped entry.
type firePlan struct {
	e         *entry
	schedule  Schedule
	gen       uint64
	scheduled time.Time
	fireOne   time.Time   // zero if no fire (MissedSkip or exhausted catch-up)
	fireAll   []time.Time // MissedRunAll catch-up instants
	nextFire  time.Time
	lateness  time.Duration
	missed    bool
}

// fireOpts controls one dispatched invocation.
type fireOpts struct {
	advancePrev bool
	manual      bool         // Trigger: skip jitter and coordination
	result      chan<- error // if non-nil (cap >= 1), receives the outcome
}

// fireDue pops every entry due at now and hands each one to its own planner
// goroutine. Schedule.Next therefore runs neither under c.mu nor on the loop
// goroutine: a slow or blocking schedule delays only its own entry.
//
// Planners join c.wg from the loop goroutine, before runDone closes, so the
// shutdown wait observes them and every job they dispatch. loopCtx is done
// after Stop or Drain, at which point a finished plan is discarded instead of
// firing.
func (c *Cron) fireDue(loopCtx context.Context, now time.Time) {
	nowNano := now.UnixNano()

	c.mu.Lock()
	var due []dueFire
	for {
		it, ok := c.h.Peek()
		if !ok || it.Key > nowNano {
			break
		}
		c.h.Pop()
		e := it.Value
		e.item = nil
		due = append(due, dueFire{e: e, schedule: e.schedule, scheduled: e.next, gen: e.gen})
	}
	c.mu.Unlock()

	for _, d := range due {
		c.wg.Go(func() {
			p := c.makeFirePlan(d, now)
			if loopCtx.Err() != nil {
				return
			}
			c.commitAndDispatch(c.runCtx, p)
		})
	}
}

// makeFirePlan decides which instants to fire for a due entry and computes its
// next fire. It calls Schedule.Next and so must not hold c.mu.
func (c *Cron) makeFirePlan(d dueFire, now time.Time) firePlan {
	p := firePlan{
		e:         d.e,
		schedule:  d.schedule,
		gen:       d.gen,
		scheduled: d.scheduled,
		lateness:  now.Sub(d.scheduled),
	}
	if p.lateness > c.cfg.missedTolerance {
		p.missed = true
		switch d.e.missed {
		case MissedRunOnce:
			p.fireOne = findMostRecentMissed(d.schedule, d.scheduled, now)
		case MissedRunAll:
			p.fireAll = findAllMissed(d.schedule, d.scheduled, now)
		}
	} else {
		p.fireOne = d.scheduled
	}
	p.nextFire = d.schedule.Next(now)
	if !p.nextFire.IsZero() && !p.nextFire.After(now) {
		// Contract violation: Next must return a time strictly after its
		// argument. Re-queueing the entry would spin the loop, so the entry is
		// treated as exhausted and the fault is logged.
		c.cfg.logger.Error(
			"cron: schedule returned a non-future time; entry exhausted",
			slog.String("name", d.e.name),
			slog.Time("now", now),
			slog.Time("next", p.nextFire),
		)
		p.nextFire = time.Time{}
	}
	return p
}

// commitAndDispatch applies a plan if the entry is unchanged since the pop:
// it reserves a concurrency slot per fire, re-queues the entry, publishes the
// snapshot and starts the job goroutines.
func (c *Cron) commitAndDispatch(ctx context.Context, p firePlan) {
	c.mu.Lock()
	cur, ok := c.byEntry[p.e.id]
	stalePlan := !ok || cur != p.e || cur.gen != p.gen
	if stalePlan {
		// Removed, paused, resumed, or updated since the pop; the plan is stale.
		c.mu.Unlock()
		return
	}

	fires := p.fireAll
	if len(fires) == 0 && !p.fireOne.IsZero() {
		fires = []time.Time{p.fireOne}
	}
	var run []time.Time
	var rejected []time.Time
	for _, ft := range fires {
		if !c.tryReserveInflight() {
			rejected = append(rejected, ft)
			continue
		}
		run = append(run, ft)
	}

	cur.next = p.nextFire
	if !cur.next.IsZero() {
		cur.item = c.h.Push(cur.next.UnixNano(), cur)
	}
	view := entryView(cur)
	cur.view.p.Store(&view)

	for _, ft := range run {
		c.dispatch(
			ctx,
			cur,
			ft,
			fireOpts{advancePrev: true},
		)
	}
	nextEmit := cur.next
	name := cur.name
	policy := cur.missed
	heapLen := c.h.Len()
	c.mu.Unlock()

	// The loop re-armed its timer when this entry was popped; the new next
	// fire is only visible to it after a wake.
	c.wake()

	ref := EntryRef{ID: p.e.id, Key: p.e.key, Name: name}
	if p.missed {
		c.events.publish(MissedFireEvent{
			Entry: ref, ScheduledAt: p.scheduled, Lateness: p.lateness, Policy: policy,
		})
	}
	for _, scheduledAt := range rejected {
		c.events.publish(RejectedFireEvent{
			Entry: ref, ScheduledAt: scheduledAt, Reason: RejectConcurrencyLimit,
		})
	}
	if !nextEmit.IsZero() {
		c.events.publish(ScheduleEvent{Entry: ref, Schedule: p.schedule, Next: nextEmit})
	}
	c.events.publish(QueueDepthEvent{Depth: heapLen})
}

// dispatch runs one invocation on its own goroutine. The caller has already
// reserved the concurrency slot that the goroutine releases on exit.
func (c *Cron) dispatch(parent context.Context, e *entry, scheduledAt time.Time, opts fireOpts) {
	c.wg.Go(func() {
		defer c.inflight.Add(-1)

		// Manual triggers mean "run it HERE now": no jitter, no coordination.
		// They are also the only fires carrying opts.result, so the abort
		// paths below never owe a result.
		if !opts.manual {
			// Jitter waits on the run ctx, not the job-timeout ctx, so it never
			// eats the timeout budget.
			if !c.applyJitter(parent, e.jitter) {
				c.events.publish(CanceledFireEvent{
					Entry: entryRef(e), ScheduledAt: scheduledAt,
					Cause: context.Cause(parent),
				})
				return
			}
			// Coordination runs after jitter (which spreads the fleet's backend
			// calls) and before the timeout ctx.
			if reason, err := c.coordinate(parent, e, scheduledAt); reason != SkipUnknown {
				c.events.publish(SkippedFireEvent{
					Entry:       entryRef(e),
					ScheduledAt: scheduledAt,
					Reason:      reason,
					Err:         err,
				})
				return
			}
		}

		// Build the timeout ctx after jitter so it covers only runtime. The
		// e.timeout > 0 guard matters: WithTimeoutCause(parent, 0) is born expired.
		jobCtx := parent
		if e.timeout > 0 {
			var cancel context.CancelFunc
			jobCtx, cancel = context.WithTimeoutCause(parent, e.timeout, ErrJobTimeout)
			defer cancel()
		}
		jobCtx = context.WithValue(jobCtx, entryInfoKey{}, EntryInfo{
			ID: e.id, Name: e.name, Key: e.key, ScheduledAt: scheduledAt,
		})

		fireAt := time.Now()
		c.events.publish(JobStartEvent{
			Entry: entryRef(e), ScheduledAt: scheduledAt, StartedAt: fireAt,
		})
		err := c.runJob(jobCtx, e)
		dur := time.Since(fireAt)
		c.events.publish(JobCompleteEvent{
			Entry:       entryRef(e),
			ScheduledAt: scheduledAt,
			StartedAt:   fireAt,
			Duration:    dur,
			Err:         err,
		})
		if opts.advancePrev {
			c.advancePrev(e.id, scheduledAt)
		}
		if opts.result != nil {
			opts.result <- err
		}
	})
}

// coordinate consults the Elector and then the Claimer for an automatic fire.
// It returns SkipUnknown when the job may run; otherwise the reason the fire
// must be suppressed and the backend error, if any. Backend errors fail
// closed.
func (c *Cron) coordinate(ctx context.Context, e *entry, scheduledAt time.Time) (SkipReason, error) {
	if el := c.cfg.elector; el != nil {
		leader, err := el.IsLeader(ctx)
		if err != nil {
			return SkipElectionError, err
		}
		if !leader {
			return SkipNotLeader, nil
		}
	}
	if e.claimer != nil {
		claimed, err := e.claimer.Claim(ctx, fireKey(e.key, scheduledAt))
		if err != nil {
			return SkipClaimError, err
		}
		if !claimed {
			return SkipAlreadyClaimed, nil
		}
	}
	return SkipUnknown, nil
}

// runJob executes the wrapped job, converting panics into ErrJobPanic unless
// WithoutRecover was set.
func (c *Cron) runJob(ctx context.Context, e *entry) (err error) {
	if !c.cfg.recoverDisabled {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%w: %v", ErrJobPanic, r)
				c.cfg.logger.Error(
					"cron: job panic recovered",
					slog.String("name", e.name),
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
				)
			}
		}()
	}
	return e.wrapped.Run(ctx)
}

// advancePrev records fireAt as the entry's Prev unless a later fire already
// did; MissedRunAll dispatches several fires concurrently.
func (c *Cron) advancePrev(id EntryID, fireAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.byEntry[id]
	if !ok {
		return
	}
	if !fireAt.After(cur.prev) {
		return
	}
	cur.prev = fireAt
	view := entryView(cur)
	cur.view.p.Store(&view)
}

// tryReserveInflight claims one WithMaxConcurrent slot; the dispatched
// goroutine releases it. Without a limit it only counts.
func (c *Cron) tryReserveInflight() bool {
	if c.cfg.maxConcurrent <= 0 {
		c.inflight.Add(1)
		return true
	}
	limit := int64(c.cfg.maxConcurrent)
	for {
		cur := c.inflight.Load()
		if cur >= limit {
			return false
		}
		if c.inflight.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// applyJitter sleeps for a random duration in [0, max) and reports false if
// ctx was cancelled first.
func (c *Cron) applyJitter(ctx context.Context, max time.Duration) bool {
	if max <= 0 {
		return true
	}
	d := mathrand.N(max)
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
