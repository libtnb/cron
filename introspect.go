package cron

import (
	"iter"
	"time"
)

// NextN returns up to n firings of s strictly after from, in order; fewer are
// returned when the schedule exhausts first. It returns nil when s is nil or n
// is not positive. Schedules implementing Upcoming are iterated lazily; others
// are walked with repeated Next calls.
func NextN(s Schedule, from time.Time, n int) []time.Time {
	if isNilLike(s) || n <= 0 {
		return nil
	}
	out := make([]time.Time, 0, n)
	for t := range upcomingSeq(s, from) {
		out = append(out, t)
		if len(out) == n {
			break
		}
	}
	return out
}

// Between lazily yields every firing of s in (start, end], in order. The
// sequence is empty when s is nil or end is not after start. Iteration stops
// at the first firing after end, so an unbounded schedule is safe to query.
func Between(s Schedule, start, end time.Time) iter.Seq[time.Time] {
	return func(yield func(time.Time) bool) {
		if isNilLike(s) || !end.After(start) {
			return
		}
		for t := range upcomingSeq(s, start) {
			if t.After(end) || !yield(t) {
				return
			}
		}
	}
}

// upcomingSeq is a lazy iterator over firings strictly after from. It uses
// the schedule's Upcoming when implemented and otherwise loops Schedule.Next.
func upcomingSeq(s Schedule, from time.Time) iter.Seq[time.Time] {
	if up, ok := s.(Upcoming); ok {
		return up.Upcoming(from)
	}
	return func(yield func(time.Time) bool) {
		cur := from
		for {
			next := s.Next(cur)
			if next.IsZero() {
				return
			}
			if !yield(next) {
				return
			}
			cur = next
		}
	}
}
