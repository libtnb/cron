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
	for t := range upcomingSeq(s, from) {
		out = append(out, t)
		if len(out) == n {
			break
		}
	}
	return out
}

// Between returns every firing in (start, end].
func Between(s Schedule, start, end time.Time) []time.Time {
	if !end.After(start) {
		return nil
	}
	var out []time.Time
	for t := range upcomingSeq(s, start) {
		if t.After(end) {
			break
		}
		out = append(out, t)
	}
	return out
}

// upcomingSeq is a lazy iterator over firings strictly after from. Uses
// Schedule's Upcoming if it implements that interface, otherwise loops
// Schedule.Next.
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
