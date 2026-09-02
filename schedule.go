package cron

import (
	"iter"
	"time"
)

// Schedule yields successive firing times. Next must return the first firing
// strictly after now, or the zero time when the schedule is exhausted, and
// must be monotone: a later argument never yields an earlier result.
// Implementations must be safe for concurrent use, because the scheduler
// evaluates schedules on per-fire planner goroutines and one parsed schedule
// may back several entries. Next should be cheap: besides one call per fire it
// runs at Add, Update and Resume, and missed-fire catch-up bisects over it
// (about 60 calls per late fire).
//
// A Next that returns a non-zero time not after its argument breaks the
// contract; the scheduler logs the fault and treats the entry as exhausted
// rather than spinning.
type Schedule interface {
	// Next returns the first firing strictly after now, or zero if none.
	Next(now time.Time) time.Time
}

// Upcoming is an optional Schedule capability used by NextN and Between to
// iterate firings lazily. Upcoming must yield strictly increasing times after
// from and stop when the schedule exhausts.
type Upcoming interface {
	// Upcoming yields every firing strictly after from, in order.
	Upcoming(from time.Time) iter.Seq[time.Time]
}

// Parser turns a textual spec into a Schedule; see WithParser and
// ValidateSpecWith. Cron memoizes results per spec, so Parse must be
// deterministic and safe for concurrent use, and the returned Schedule may
// back several entries. Parse must return a non-nil Schedule or an error; a
// nil, nil result is reported as ErrNilSchedule.
type Parser interface {
	// Parse compiles spec. Implementations should return *ParseError for
	// rejected specs so callers can inspect the fault.
	Parse(spec string) (Schedule, error)
}

// ConstantDelay fires every d, measured from each evaluation: the scheduler
// computes the next fire from the current time, so the phase is anchored to
// this process and drifts with job lateness. Whole-second periods snap to the
// second boundary; sub-second periods keep their exact length. Non-positive
// intervals never fire. "@every 10s" parses to ConstantDelay.
//
// Because the phase is process-local, replicas sharing a Claimer key never
// agree on fire instants; use AlignedDelay or a cron expression instead.
type ConstantDelay time.Duration

// Next returns now plus the interval, snapped to a whole second for
// whole-second intervals, or zero for non-positive intervals.
func (d ConstantDelay) Next(now time.Time) time.Time {
	delay := time.Duration(d)
	if delay <= 0 {
		return time.Time{}
	}
	// Only snap to the second boundary for whole-second periods. Subtracting the
	// sub-second phase from a fractional period (e.g. 1.5s) collapses every
	// interval after the first to floor(d), firing far more often than asked.
	if delay%time.Second == 0 {
		delay -= time.Duration(now.Nanosecond())
	}
	return now.Add(delay)
}

// String renders the interval in "@every" form, for example "@every 1m30s".
func (d ConstantDelay) String() string {
	return "@every " + time.Duration(d).String()
}

// AlignedDelay fires at multiples of d since the Unix epoch, so replicas
// evaluating the same interval compute identical instants and can share a
// Claimer key. Alignment follows time.Truncate, which works on absolute time:
// a 24h AlignedDelay fires at 00:00 UTC, not local midnight. Non-positive
// intervals never fire.
type AlignedDelay time.Duration

// Next returns the next multiple of the interval strictly after now, or zero
// for non-positive intervals.
func (d AlignedDelay) Next(now time.Time) time.Time {
	step := time.Duration(d)
	if step <= 0 {
		return time.Time{}
	}
	return now.Truncate(step).Add(step)
}

// String renders the interval in "@aligned" form, for example "@aligned 5m".
func (d AlignedDelay) String() string {
	return "@aligned " + time.Duration(d).String()
}

type triggeredSchedule struct{}

// TriggeredSchedule never fires on its own; the entry runs only through
// Trigger, TriggerAndWait or TriggerByName. Entry.Next stays zero for such
// entries and AnalyzeSpec reports IsTriggered.
func TriggeredSchedule() Schedule { return triggeredSchedule{} }

func (triggeredSchedule) Next(time.Time) time.Time { return time.Time{} }
func (triggeredSchedule) String() string           { return "@triggered" }

// IsTriggered reports whether s came from TriggeredSchedule.
func IsTriggered(s Schedule) bool {
	_, ok := s.(triggeredSchedule)
	return ok
}

type onceAt time.Time

// OnceAt fires exactly once, at t, then exhausts. If t is already past when
// the entry is added it never fires, unless WithLastRun seeds an anchor before
// t, in which case the missed-fire policy decides.
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

// Union fires whenever any member fires; coincident instants fire once. Nil
// members are ignored and an empty union never fires. The union exhausts only
// when every member has.
func Union(schedules ...Schedule) Schedule {
	u := make(unionSchedule, 0, len(schedules))
	for _, s := range schedules {
		if !isNilLike(s) {
			u = append(u, s)
		}
	}
	return u
}

func (u unionSchedule) Next(now time.Time) time.Time {
	var best time.Time
	for _, s := range u {
		n := s.Next(now)
		if n.IsZero() {
			continue
		}
		earlier := best.IsZero() || n.Before(best)
		if earlier {
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

// Filter wraps s, skipping firings for which keep returns false, for example
// a holiday calendar. A nil keep passes everything through. The search gives
// up and reports exhaustion (zero time) after 100000 consecutive rejections,
// so a filter that rejects everything still terminates.
func Filter(s Schedule, keep func(time.Time) bool) Schedule {
	return filterSchedule{s: s, keep: keep}
}

func (f filterSchedule) Next(now time.Time) time.Time {
	if isNilLike(f.s) {
		return time.Time{}
	}
	cur := now
	for range filterScanCap {
		n := f.s.Next(cur)
		// keep must not be called when nil, so the short-circuit stays inline.
		if n.IsZero() || f.keep == nil || f.keep(n) {
			return n
		}
		cur = n
	}
	return time.Time{}
}
