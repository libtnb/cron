package cron

import (
	"iter"
	"log/slog"
	"math/bits"
	"time"
)

// SpecSchedule is a parsed cron expression.
type SpecSchedule struct {
	second uint64
	minute uint64
	hour   uint64
	dom    uint64
	month  uint64
	dow    uint64
	loc    *time.Location
}

// Location returns the evaluation timezone.
func (s *SpecSchedule) Location() *time.Location {
	return s.loc
}

// nextYearLimit caps SpecSchedule.Next search; matches robfig/cron.
const nextYearLimit = 5

// Next returns the next firing after t, or zero if none is found.
func (s *SpecSchedule) Next(t time.Time) time.Time {
	origLoc := t.Location()
	loc := s.loc
	if loc != origLoc {
		t = t.In(loc)
	}

	t = t.Add(time.Second - time.Duration(t.Nanosecond())*time.Nanosecond)
	yearLimit := t.Year() + nextYearLimit

	for t.Year() <= yearLimit {
		if 1<<uint(t.Month())&s.month == 0 {
			m := nextBitInRange(s.month, uint(t.Month())+1, 12)
			if m < 0 {
				m = nextBitInRange(s.month, 1, 12)
				if m < 0 {
					return time.Time{}
				}
				t = time.Date(t.Year()+1, time.Month(m), 1, 0, 0, 0, 0, loc)
			} else {
				t = time.Date(t.Year(), time.Month(m), 1, 0, 0, 0, 0, loc)
			}
			continue
		}

		if !dayMatches(s, t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}

		if 1<<uint(t.Hour())&s.hour == 0 {
			h := nextBitInRange(s.hour, uint(t.Hour())+1, 23)
			if h < 0 {
				t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
				continue
			}
			t = t.Add(time.Duration(h-t.Hour())*time.Hour -
				time.Duration(t.Minute())*time.Minute -
				time.Duration(t.Second())*time.Second)
			continue
		}

		if 1<<uint(t.Minute())&s.minute == 0 {
			m := nextBitInRange(s.minute, uint(t.Minute())+1, 59)
			if m < 0 {
				t = t.Add(time.Hour -
					time.Duration(t.Minute())*time.Minute -
					time.Duration(t.Second())*time.Second)
				continue
			}
			t = t.Add(time.Duration(m-t.Minute())*time.Minute -
				time.Duration(t.Second())*time.Second)
			continue
		}

		if 1<<uint(t.Second())&s.second == 0 {
			sec := nextBitInRange(s.second, uint(t.Second())+1, 59)
			if sec < 0 {
				t = t.Add(time.Minute - time.Duration(t.Second())*time.Second)
				continue
			}
			t = t.Add(time.Duration(sec-t.Second()) * time.Second)
			continue
		}

		return t.In(origLoc)
	}
	return time.Time{}
}

// nextBitInRange returns the lowest set bit in [from, until], or -1.
func nextBitInRange(bm uint64, from, until uint) int {
	if from > until || from >= 64 {
		return -1
	}
	masked := bm >> from << from
	if until < 63 {
		masked &= (uint64(1) << (until + 1)) - 1
	}
	if masked == 0 {
		return -1
	}
	return bits.TrailingZeros64(masked)
}

// Upcoming is a lazy iterator over future firings.
func (s *SpecSchedule) Upcoming(from time.Time) iter.Seq[time.Time] {
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

// dayMatches implements the DOM/DOW OR/AND coupling: AND when either
// field was "*", OR otherwise.
func dayMatches(s *SpecSchedule, t time.Time) bool {
	domMatch := 1<<uint(t.Day())&s.dom != 0
	dowMatch := 1<<uint(t.Weekday())&s.dow != 0
	if s.dom&starBit != 0 || s.dow&starBit != 0 {
		return domMatch && dowMatch
	}
	return domMatch || dowMatch
}

func (s *SpecSchedule) LogValue() slog.Value {
	loc := "Local"
	if s.loc != nil {
		loc = s.loc.String()
	}
	return slog.GroupValue(
		slog.String("kind", "spec"),
		slog.String("loc", loc),
	)
}
