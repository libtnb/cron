package cron

import (
	"iter"
	"time"
)

// NextN returns the next n firings strictly after from.
func NextN(s Schedule, from time.Time, n int) []time.Time {
	if n <= 0 {
		return nil
	}
	out := make([]time.Time, 0, n)
	for t := range UpcomingSeq(s, from) {
		out = append(out, t)
		if len(out) == n {
			break
		}
	}
	return out
}

// Between returns every firing in (start, end].
func Between(s Schedule, start, end time.Time) []time.Time {
	return BetweenWithLimit(s, start, end, -1)
}

// BetweenWithLimit returns up to limit firings in (start, end];
// non-positive limit disables the cap.
func BetweenWithLimit(s Schedule, start, end time.Time, limit int) []time.Time {
	if !end.After(start) {
		return nil
	}
	var out []time.Time
	for t := range UpcomingSeq(s, start) {
		if t.After(end) {
			break
		}
		out = append(out, t)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// Count returns the number of firings in (start, end].
func Count(s Schedule, start, end time.Time) int {
	return CountWithLimit(s, start, end, -1)
}

// CountWithLimit caps the count at limit; non-positive disables.
func CountWithLimit(s Schedule, start, end time.Time, limit int) int {
	if !end.After(start) {
		return 0
	}
	n := 0
	for t := range UpcomingSeq(s, start) {
		if t.After(end) {
			break
		}
		n++
		if limit > 0 && n == limit {
			break
		}
	}
	return n
}

// Matches reports whether s would fire exactly at t.
func Matches(s Schedule, t time.Time) bool {
	probe := t.Add(-time.Nanosecond)
	got := s.Next(probe)
	return !got.IsZero() && got.Equal(t)
}

// UpcomingSeq is a lazy iterator over firings strictly after from. Uses
// Schedule's Upcoming if it implements that interface, otherwise loops
// Schedule.Next.
func UpcomingSeq(s Schedule, from time.Time) iter.Seq[time.Time] {
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
