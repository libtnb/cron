package cron

import (
	"iter"
	"time"
)

// Schedule yields successive firing times. Next must return the first firing
// strictly after now, or zero when exhausted.
type Schedule interface {
	Next(now time.Time) time.Time
}

// Upcoming is an optional lazy iteration capability.
type Upcoming interface {
	Upcoming(from time.Time) iter.Seq[time.Time]
}

// Parser turns a textual spec into a Schedule. Cron caches parser results.
type Parser interface {
	Parse(spec string) (Schedule, error)
}

// ConstantDelay is a fixed-interval Schedule. The interval has a 1s floor:
// sub-second durations fire every 1s. Whole-second intervals are aligned to
// the second boundary; fractional intervals (e.g. 1500ms) keep their exact
// period.
type ConstantDelay time.Duration

func (d ConstantDelay) Next(now time.Time) time.Time {
	delay := max(time.Duration(d), time.Second)
	// Only snap to the second boundary for whole-second periods. Subtracting the
	// sub-second phase from a fractional period (e.g. 1.5s) collapses every
	// interval after the first to floor(d), firing far more often than asked.
	if delay%time.Second == 0 {
		delay -= time.Duration(now.Nanosecond()) * time.Nanosecond
	}
	return now.Add(delay)
}

func (d ConstantDelay) String() string {
	return "@every " + time.Duration(d).String()
}

// AlignedDelay is a fixed-interval Schedule aligned to the Unix epoch: every
// instance computes identical fire instants, which makes it the right
// interval schedule under a distributed Locker. ConstantDelay, by contrast,
// anchors its phase at Add time, so staggered instances never share fire
// keys. Non-positive intervals never fire.
type AlignedDelay time.Duration

func (d AlignedDelay) Next(now time.Time) time.Time {
	step := time.Duration(d)
	if step <= 0 {
		return time.Time{}
	}
	return now.Truncate(step).Add(step)
}

func (d AlignedDelay) String() string {
	return "@aligned " + time.Duration(d).String()
}

type triggeredSchedule struct{}

// TriggeredSchedule never fires automatically. Combine with Trigger.
func TriggeredSchedule() Schedule { return triggeredSchedule{} }

func (triggeredSchedule) Next(time.Time) time.Time { return time.Time{} }
func (triggeredSchedule) String() string           { return "@triggered" }

// IsTriggered reports whether s came from TriggeredSchedule.
func IsTriggered(s Schedule) bool {
	_, ok := s.(triggeredSchedule)
	return ok
}

type onceAt time.Time

// OnceAt fires exactly once, at t. If t is already past, it never fires.
func OnceAt(t time.Time) Schedule { return onceAt(t) }

func (o onceAt) Next(now time.Time) time.Time {
	if at := time.Time(o); now.Before(at) {
		return at
	}
	return time.Time{}
}

func (o onceAt) String() string {
	return "@at " + time.Time(o).Format(time.RFC3339Nano)
}

type unionSchedule []Schedule

// Union fires whenever any of the schedules fires. Nil members are ignored;
// an empty union never fires.
func Union(schedules ...Schedule) Schedule {
	u := make(unionSchedule, 0, len(schedules))
	for _, s := range schedules {
		if s != nil {
			u = append(u, s)
		}
	}
	return u
}

func (u unionSchedule) Next(now time.Time) time.Time {
	var best time.Time
	for _, s := range u {
		n := s.Next(now)
		if !n.IsZero() && (best.IsZero() || n.Before(best)) {
			best = n
		}
	}
	return best
}

// filterScanCap bounds Filter's search for the next kept firing.
const filterScanCap = 100000

type filterSchedule struct {
	s    Schedule
	keep func(time.Time) bool
}

// Filter wraps s, skipping firings for which keep returns false — e.g. a
// holiday calendar. A nil keep passes everything through. The search gives up
// (returns zero) after filterScanCap consecutive rejections.
func Filter(s Schedule, keep func(time.Time) bool) Schedule {
	return filterSchedule{s: s, keep: keep}
}

func (f filterSchedule) Next(now time.Time) time.Time {
	if f.s == nil {
		return time.Time{}
	}
	cur := now
	for range filterScanCap {
		n := f.s.Next(cur)
		if n.IsZero() || f.keep == nil || f.keep(n) {
			return n
		}
		cur = n
	}
	return time.Time{}
}
