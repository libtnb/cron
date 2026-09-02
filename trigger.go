package cron

import (
	"context"
	"errors"
	"time"
)

// Trigger fires id immediately on this process, bypassing jitter and
// distributed coordination; paused entries can be triggered. The invocation
// runs asynchronously and does not advance Entry.Prev. Returns
// ErrSchedulerNotRunning, ErrEntryNotFound, or ErrConcurrencyLimit when
// WithMaxConcurrent has no free slot (a RejectedFireEvent is published too).
func (c *Cron) Trigger(id EntryID) error { return c.trigger(id, nil) }

// TriggerAndWait fires id like Trigger and blocks until the invocation
// returns, yielding the job's error (including ErrJobPanic and retry
// aggregates). ctx bounds only the wait: on cancellation it returns ctx.Err()
// while the job keeps running under the scheduler's context. Returns
// ErrNilContext for a nil ctx and the same dispatch errors as Trigger.
func (c *Cron) TriggerAndWait(ctx context.Context, id EntryID) error {
	if ctx == nil {
		return ErrNilContext
	}
	result := make(chan error, 1)
	if err := c.trigger(id, result); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TriggerByName fires every entry whose Name equals name; names are not
// unique. It returns the number of successful dispatches and the errors.Join
// of the failed ones. No match yields (0, nil); a scheduler that is not
// running yields (0, ErrSchedulerNotRunning).
func (c *Cron) TriggerByName(name string) (int, error) {
	if !c.running.Load() {
		return 0, ErrSchedulerNotRunning
	}
	c.mu.Lock()
	var ids []EntryID
	for id, e := range c.byEntry {
		if e.name == name {
			ids = append(ids, id)
		}
	}
	c.mu.Unlock()

	var count int
	var errs []error
	for _, id := range ids {
		if err := c.Trigger(id); err != nil {
			errs = append(errs, err)
			continue
		}
		count++
	}
	return count, errors.Join(errs...)
}

// trigger dispatches a manual fire. It holds startMu so the run context
// cannot be cancelled by a concurrent Stop between the running check and the
// dispatch.
func (c *Cron) trigger(id EntryID, result chan<- error) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if !c.running.Load() {
		return ErrSchedulerNotRunning
	}
	fireAt := time.Now()

	c.mu.Lock()
	e, ok := c.byEntry[id]
	if !ok {
		c.mu.Unlock()
		return ErrEntryNotFound
	}
	if !c.tryReserveInflight() {
		c.mu.Unlock()
		c.events.publish(RejectedFireEvent{
			Entry: entryRef(e), ScheduledAt: fireAt,
			Reason: RejectConcurrencyLimit,
		})
		return ErrConcurrencyLimit
	}
	c.dispatch(
		c.runCtx,
		e,
		fireAt,
		fireOpts{manual: true, result: result},
	)
	c.mu.Unlock()
	return nil
}
