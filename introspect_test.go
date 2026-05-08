package cron

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestNextN(t *testing.T) {
	s := mustParse(t, "@every 1m")
	from := t0(2026, 1, 1, 0, 0, 0)
	got := NextN(s, from, 3)
	want := []time.Time{
		t0(2026, 1, 1, 0, 1, 0),
		t0(2026, 1, 1, 0, 2, 0),
		t0(2026, 1, 1, 0, 3, 0),
	}
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("[%d] got %v want %v", i, got[i], want[i])
		}
	}
}

func TestNextN_ZeroOrNegative(t *testing.T) {
	s := mustParse(t, "@hourly")
	if got := NextN(s, time.Now(), 0); got != nil {
		t.Fatalf("got %v", got)
	}
	if got := NextN(s, time.Now(), -1); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestNextN_TerminatesWhenScheduleExhausts(t *testing.T) {
	s := mustParse(t, "0 0 30 2 *")
	got := NextN(s, t0(2026, 1, 1, 0, 0, 0), 10)
	if len(got) != 0 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestBetween(t *testing.T) {
	s := mustParse(t, "@hourly")
	start := t0(2026, 1, 1, 0, 30, 0)
	end := t0(2026, 1, 1, 5, 30, 0)
	got := Between(s, start, end)
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
}

func TestBetween_StartAfterEnd(t *testing.T) {
	s := mustParse(t, "@hourly")
	got := Between(s, t0(2026, 1, 2, 0, 0, 0), t0(2026, 1, 1, 0, 0, 0))
	if got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestUpcomingSeq_UsesSchedulesUpcomingWhenAvailable(t *testing.T) {
	s := mustParse(t, "@hourly")
	if _, ok := s.(Upcoming); !ok {
		t.Fatal("SpecSchedule should implement Upcoming")
	}
	count := 0
	for range upcomingSeq(s, t0(2026, 1, 1, 0, 0, 0)) {
		count++
		if count == 5 {
			break
		}
	}
	if count != 5 {
		t.Fatalf("count = %d", count)
	}
}

type exhaustingSchedule struct{ n atomic.Int32 }

func (s *exhaustingSchedule) Next(now time.Time) time.Time {
	if s.n.Add(1) > 3 {
		return time.Time{}
	}
	return now.Add(time.Hour)
}

func TestUpcomingSeq_FallbackTerminatesOnZero(t *testing.T) {
	s := &exhaustingSchedule{}
	if _, ok := any(s).(Upcoming); ok {
		t.Fatal("test schedule should not implement Upcoming")
	}
	count := 0
	for range upcomingSeq(s, t0(2026, 1, 1, 0, 0, 0)) {
		count++
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
}
