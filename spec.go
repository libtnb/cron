package cron

import (
	"iter"
	"log/slog"
	"time"

	"github.com/libtnb/cron/internal/bitmask"
)

// nextYearLimit caps SpecSchedule.Next search; matches robfig/cron.
const nextYearLimit = 5

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

// Next returns the next firing after t, or zero if none is found.
//
// The hour branch reconstructs the wall clock at the target hour instead of
// adding an absolute (h-hour)*time.Hour, which would overshoot a DST
// spring-forward gap and skip the day. The minute/second branches keep their
// absolute jumps: they never straddle a whole-hour DST boundary, and stepping
// through the repeated hour on fall-back keeps both firings observable.
func (s *SpecSchedule) Next(t time.Time) time.Time {
	origLoc := t.Location()
	loc := s.loc
	if loc == nil { // zero-value SpecSchedule
		loc = time.Local
	}
	if loc != origLoc {
		t = t.In(loc)
	}

	t = t.Add(time.Second - time.Duration(t.Nanosecond())*time.Nanosecond)
	yearLimit := t.Year() + nextYearLimit

	for {
		year, month, day := t.Date()
		if year > yearLimit {
			return time.Time{}
		}

		if 1<<uint(month)&s.month == 0 {
			m := bitmask.NextInRange(s.month, uint(month)+1, 12)
			if m < 0 {
				m = bitmask.NextInRange(s.month, 1, 12)
				if m < 0 {
					return time.Time{} // zero-value SpecSchedule: empty month set
				}
				t = time.Date(year+1, time.Month(m), 1, 0, 0, 0, 0, loc)
			} else {
				t = time.Date(year, time.Month(m), 1, 0, 0, 0, 0, loc)
			}
			continue
		}

		if !dayMatches(s, t, day) {
			t = time.Date(year, month, day+1, 0, 0, 0, 0, loc)
			continue
		}

		hour, minute, sec := t.Clock()

		if 1<<uint(hour)&s.hour == 0 {
			h := bitmask.NextInRange(s.hour, uint(hour)+1, 23)
			if h < 0 {
				t = time.Date(year, month, day+1, 0, 0, 0, 0, loc)
				continue
			}
			// time.Date resolves a nonexistent spring-forward hour to the pre-gap
			// wall time (02:00 -> 01:00), which wouldn't advance; step one wall
			// hour in that case and let the loop re-validate.
			cand := time.Date(year, month, day, h, 0, 0, 0, loc)
			if cand.Hour() == h && cand.After(t) {
				t = cand
			} else {
				t = t.Add(time.Hour -
					time.Duration(minute)*time.Minute -
					time.Duration(sec)*time.Second)
			}
			continue
		}

		if 1<<uint(minute)&s.minute == 0 {
			m := bitmask.NextInRange(s.minute, uint(minute)+1, 59)
			if m < 0 {
				t = t.Add(time.Hour -
					time.Duration(minute)*time.Minute -
					time.Duration(sec)*time.Second)
				continue
			}
			t = t.Add(time.Duration(m-minute)*time.Minute -
				time.Duration(sec)*time.Second)
			continue
		}

		if 1<<uint(sec)&s.second == 0 {
			secN := bitmask.NextInRange(s.second, uint(sec)+1, 59)
			if secN < 0 {
				t = t.Add(time.Minute - time.Duration(sec)*time.Second)
				continue
			}
			t = t.Add(time.Duration(secN-sec) * time.Second)
			continue
		}

		return t.In(origLoc)
	}
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

// dayMatches implements the DOM/DOW coupling: AND when either field was "*",
// OR otherwise. day is the already-computed t.Date() day, reused to avoid a
// second civil-time conversion.
func dayMatches(s *SpecSchedule, t time.Time, day int) bool {
	domMatch := 1<<uint(day)&s.dom != 0
	dowMatch := 1<<uint(t.Weekday())&s.dow != 0
	if s.dom&starBit != 0 || s.dow&starBit != 0 {
		return domMatch && dowMatch
	}
	return domMatch || dowMatch
}
