package cron

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Hook sub-interfaces. WithHooks subscribers may implement any subset.
type ScheduleHook interface{ OnSchedule(EventSchedule) }
type JobStartHook interface{ OnJobStart(EventJobStart) }
type JobCompleteHook interface{ OnJobComplete(EventJobComplete) }
type MissedHook interface{ OnMissedFire(EventMissed) }

// EventSchedule is emitted when an entry is added or its next firing is
// recomputed after a fire.
type EventSchedule struct {
	EntryID  EntryID
	Name     string
	Schedule Schedule
	Next     time.Time
}

// EventJobStart is emitted just before the chain runs. ScheduledAt is the
// schedule-selected time; FireAt is the actual start time after jitter/queueing.
type EventJobStart struct {
	EntryID     EntryID
	Name        string
	ScheduledAt time.Time
	FireAt      time.Time
}

// EventJobComplete is emitted after the chain returns. Err is the chain result.
type EventJobComplete struct {
	EntryID     EntryID
	Name        string
	ScheduledAt time.Time
	FireAt      time.Time
	Duration    time.Duration
	Err         error
}

// EventMissed is emitted for missed fires and MaxConcurrent rejections.
type EventMissed struct {
	EntryID     EntryID
	Name        string
	ScheduledAt time.Time
	Lateness    time.Duration
	Policy      MissedFirePolicy
}

// hookDispatcher serialises hook delivery and recovers hook panics.
type hookDispatcher struct {
	hooks    []any
	ch       chan func([]any)
	log      *slog.Logger
	recorder any
	dropped  atomic.Int64
	closed   atomic.Bool

	closeOnce sync.Once
	done      chan struct{}
}

func newHookDispatcher(hooks []any, log *slog.Logger, recorder any, bufSize int) *hookDispatcher {
	d := &hookDispatcher{log: log, recorder: recorder}
	if len(hooks) == 0 {
		return d
	}
	if bufSize <= 0 {
		bufSize = 1024
	}
	d.hooks = hooks
	d.ch = make(chan func([]any), bufSize)
	d.done = make(chan struct{})
	go d.loop()
	return d
}

func (d *hookDispatcher) loop() {
	defer close(d.done)
	for fn := range d.ch {
		fn(d.hooks)
	}
}

func (d *hookDispatcher) invokeOne(name string, h any, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			d.log.Error("cron: hook panic recovered",
				slog.String("event", name),
				slog.Any("hook", h),
				slog.Any("panic", r))
		}
	}()
	fn()
}

func (d *hookDispatcher) emit(fn func([]any)) {
	if d == nil || d.ch == nil || d.closed.Load() {
		return
	}
	defer func() { _ = recover() }()
	select {
	case d.ch <- fn:
	default:
		n := d.dropped.Add(1)
		if r, ok := d.recorder.(HookDroppedRecorder); ok {
			r.HookDropped()
		}
		d.log.Warn("cron: hook channel full, dropping event",
			slog.Int64("dropped_total", n))
	}
}

func (d *hookDispatcher) emitSchedule(e EventSchedule) {
	if d == nil || d.ch == nil {
		return
	}
	d.emit(func(hs []any) {
		for _, h := range hs {
			if x, ok := h.(ScheduleHook); ok {
				d.invokeOne("schedule", h, func() { x.OnSchedule(e) })
			}
		}
	})
}

func (d *hookDispatcher) emitJobStart(e EventJobStart) {
	if d == nil || d.ch == nil {
		return
	}
	d.emit(func(hs []any) {
		for _, h := range hs {
			if x, ok := h.(JobStartHook); ok {
				d.invokeOne("job_start", h, func() { x.OnJobStart(e) })
			}
		}
	})
}

func (d *hookDispatcher) emitJobComplete(e EventJobComplete) {
	if d == nil || d.ch == nil {
		return
	}
	d.emit(func(hs []any) {
		for _, h := range hs {
			if x, ok := h.(JobCompleteHook); ok {
				d.invokeOne("job_complete", h, func() { x.OnJobComplete(e) })
			}
		}
	})
}

func (d *hookDispatcher) emitMissed(e EventMissed) {
	if d == nil || d.ch == nil {
		return
	}
	d.emit(func(hs []any) {
		for _, h := range hs {
			if x, ok := h.(MissedHook); ok {
				d.invokeOne("missed_fire", h, func() { x.OnMissedFire(e) })
			}
		}
	})
}

// close drains the dispatcher loop, capped by ctx. Idempotent.
func (d *hookDispatcher) close(ctx context.Context) error {
	if d == nil || d.ch == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		close(d.ch)
	})
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
