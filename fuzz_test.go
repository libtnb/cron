package cron

import (
	"testing"
	"time"
)

// fuzzZones are the timezones FuzzSpecScheduleNext exercises: UTC, a US DST
// zone, a fractional-offset DST zone (Lord Howe shifts 30 minutes), and a
// European DST zone.
var fuzzZones = func() []*time.Location {
	zones := []*time.Location{time.UTC}
	for _, name := range []string{"America/New_York", "Australia/Lord_Howe", "Europe/Berlin"} {
		if loc, err := time.LoadLocation(name); err == nil {
			zones = append(zones, loc)
		}
	}
	return zones
}()

// linearNextMinute is the golden reference for SpecSchedule.Next on 5-field
// specs: walk forward one absolute minute at a time and return the first
// wall-clock match — the semantic definition itself, immune to DST math bugs.
func linearNextMinute(s *SpecSchedule, after time.Time, window time.Duration) time.Time {
	loc := s.loc
	t := after.In(loc)
	cursor := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc).Add(time.Minute)
	limit := after.Add(window)
	for !cursor.After(limit) {
		if s.second&1 != 0 &&
			1<<uint(cursor.Minute())&s.minute != 0 &&
			1<<uint(cursor.Hour())&s.hour != 0 &&
			1<<uint(cursor.Month())&s.month != 0 &&
			dayMatches(s, cursor, cursor.Day()) {
			return cursor
		}
		cursor = cursor.Add(time.Minute)
	}
	return time.Time{}
}

// FuzzSpecScheduleNext checks Next's core properties on fuzzed specs, times,
// and DST-observing zones, and differentially compares it against a linear
// wall-clock scan over the following 48 hours.
func FuzzSpecScheduleNext(f *testing.F) {
	type seed struct {
		spec string
		sec  int64
		zone uint8
	}
	springForward := time.Date(2026, 3, 8, 0, 30, 0, 0, time.UTC).Unix()
	fallBack := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix()
	seeds := []seed{
		{"0 5 * * *", springForward, 1},
		{"30 2 * * *", springForward, 1},
		{"30 1 * * *", fallBack, 1},
		{"*/15 * * * *", springForward, 2},
		{"0 9,18 * * 1-5", springForward, 3},
		{"15,45 3 1,15 * *", time.Date(2026, 1, 31, 23, 59, 0, 0, time.UTC).Unix(), 0},
		{"0 0 29 2 *", time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC).Unix(), 0},
		{"@daily", springForward, 1},
	}
	for _, s := range seeds {
		f.Add(s.spec, s.sec, s.zone)
	}

	f.Fuzz(func(t *testing.T, spec string, sec int64, zoneIdx uint8) {
		loc := fuzzZones[int(zoneIdx)%len(fuzzZones)]
		p := NewStandardParser(WithDefaultLocation(loc))
		sched, err := p.Parse(spec)
		if err != nil {
			return
		}
		s, ok := sched.(*SpecSchedule)
		if !ok {
			return
		}

		// Clamp to 2020..2030 so the scan stays in tzdata-rich territory.
		const lo, hi = 1577836800, 1893456000
		sec = lo + ((sec%(hi-lo))+(hi-lo))%(hi-lo)
		from := time.Unix(sec, int64(sec%1e9)).In(loc)

		got := s.Next(from)
		if got.IsZero() {
			return // exhausted (e.g. Feb 30); nothing more to check
		}
		if !got.After(from) {
			t.Fatalf("spec=%q from=%v: Next=%v not strictly after", spec, from, got)
		}
		if got.Nanosecond() != 0 {
			t.Fatalf("spec=%q: Next=%v has sub-second component", spec, got)
		}
		if got.Location() != from.Location() {
			t.Fatalf("spec=%q: Next changed location %v -> %v", spec, from.Location(), got.Location())
		}
		if again := s.Next(from); !again.Equal(got) {
			t.Fatalf("spec=%q: Next not deterministic: %v vs %v", spec, got, again)
		}
		if n2 := s.Next(got); !n2.IsZero() && !n2.After(got) {
			t.Fatalf("spec=%q: chain not monotonic: %v -> %v", spec, got, n2)
		}

		const window = 48 * time.Hour
		ref := linearNextMinute(s, from, window)
		switch {
		case !ref.IsZero():
			if !got.Equal(ref) {
				t.Fatalf("spec=%q from=%v (%s): Next=%v, linear scan=%v",
					spec, from, loc, got, ref)
			}
		case !got.After(from.Add(window)):
			t.Fatalf("spec=%q from=%v (%s): Next=%v inside window but linear scan found nothing",
				spec, from, loc, got)
		}
	})
}

func FuzzParser_StandardNeverPanics(f *testing.F) {
	seeds := []string{
		"* * * * *",
		"@every 1s",
		"@hourly",
		"0 0 * * *",
		"*/5 * * * *",
		"15,45 * * * *",
		"TZ=UTC 0 0 * * *",
		"@every 90s",
		"0-30/5 * * * *",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	p := NewStandardParser()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	f.Fuzz(func(t *testing.T, spec string) {
		s, err := p.Parse(spec)
		if err != nil {
			return
		}
		cur := from
		for range 32 {
			next := s.Next(cur)
			if next.IsZero() {
				return
			}
			if !next.After(cur) {
				t.Fatalf("non-monotonic: spec=%q cur=%v next=%v", spec, cur, next)
			}
			cur = next
		}
	})
}
